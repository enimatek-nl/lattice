package federation

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Leaving and knocking are the join handshake asked of a room the asker is not in.
//
// The shape is the one `/make_join` and `/send_join` already established: the resident server
// describes what the membership change would look like in its copy of the room — where the
// history ends, which state authorises it, how deep the event sits — and the server that speaks
// for the user signs that description and hands it back. Neither half can be skipped. A leaving
// user's own server cannot write their departure into a room it is not part of: it does not know
// that room's forward extremities, and an event citing the wrong predecessors is an event the room
// will resolve away.
//
// What differs is who is allowed to ask. A join is asked by a server that is not in the room and
// wants in; a leave is asked by a server whose user is already a member or an invitee of it — and
// in the invite case that server may hold no copy of the room at all, which is exactly why it has
// to ask. Neither is gated on the asking server being in the room: the room's rules decide the
// answer, and they are checked in the room service, where the same rules decide the same question
// for a local user.

// handleMakeLeave implements GET /_matrix/federation/v1/make_leave/{roomId}/{userId}.
func (d *Deps) handleMakeLeave(w http.ResponseWriter, r *http.Request) {
	d.handleMakeMembership(w, r, eventauth.MembershipLeave)
}

// handleMakeKnock implements GET /_matrix/federation/v1/make_knock/{roomId}/{userId}.
func (d *Deps) handleMakeKnock(w http.ResponseWriter, r *http.Request) {
	d.handleMakeMembership(w, r, eventauth.MembershipKnock)
}

// handleMakeMembership answers the first half of a leave or knock handshake.
func (d *Deps) handleMakeMembership(w http.ResponseWriter, r *http.Request, membership string) {
	roomID := r.PathValue("roomId")
	userID := r.PathValue("userId")
	if roomID == "" || userID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A membership request needs a room ID and a user ID."))
		return
	}
	if err := matrixid.UserValid(userID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid user ID.", userID))
		return
	}
	template, roomVersion, err := d.Joins.MembershipTemplate(r.Context(), roomID, userID, membership)
	if err != nil {
		d.answerServiceError(w, "make_"+membership, roomID, err)
		return
	}
	if d.refusedOnRoomVersion(w, r, roomID, roomVersion) {
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if d.refusedByRoomACL(w, r, roomID, origin) {
		return
	}
	// The field naming here is a genuine disagreement in the ecosystem rather than a stylistic
	// choice, so both names go out. The Matrix specification calls this field `membership_event`;
	// gomatrixserverlib — which is what Dendrite, and Complement's own federation server, decode
	// with — reads it as `event`. A server that answers only the spec spelling fails those clients
	// with an empty template, and the failure looks identical to "this server does not implement
	// make_leave", which is a worse answer to give than a redundant key.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"event":            template,
		"membership_event": template,
		"room_version":     roomVersion,
		"origin":           d.Cfg.ServerName,
	})
}

// handleSendLeave implements PUT /_matrix/federation/v1|v2/send_leave/{roomId}/{eventId}.
func (d *Deps) handleSendLeave(w http.ResponseWriter, r *http.Request) {
	d.handleSendMembership(w, r, eventauth.MembershipLeave)
}

// handleSendKnock implements PUT /_matrix/federation/v1|v2/send_knock/{roomId}/{eventId}.
func (d *Deps) handleSendKnock(w http.ResponseWriter, r *http.Request) {
	d.handleSendMembership(w, r, eventauth.MembershipKnock)
}

// handleSendMembership accepts the signed membership change the first half of the handshake made
// possible, and answers the second half of a knock — the state that lets the knocker recognise the
// room it is waiting on.
//
// A signed event is checked the way every event from a peer is checked here: signatures against
// the sender's server key, then the room's own authorisation rules against the room as it is. The
// path is part of the check, because the URL is the asking server's claim about which event it is
// offering. And the membership the event claims has to be the one this endpoint is for: the
// endpoints differ only in what they accept, so a `/send_leave` that would store a join is not a
// permission question but a malformed request, answered 400 rather than 403.
//
// Unlike a join, this server does not co-sign what it receives. A resident server's signature on a
// join is its attestation that it authorised that join through its copy of the room — the reason
// rule 4.2 can demand one. Nobody is attesting anything by accepting somebody else's departure, and
// adding a signature here would be decoration with a cost: the event that goes on to the rest of
// the room is the one its own server signed.
func (d *Deps) handleSendMembership(w http.ResponseWriter, r *http.Request, membership string) {
	roomID := r.PathValue("roomId")
	eventID := r.PathValue("eventId")
	if roomID == "" || eventID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A send_"+membership+" needs a room ID and an event ID."))
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if d.refusedByRoomACL(w, r, roomID, origin) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, d.federationBodyLimit()))
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeBadRequest,
			"The membership event could not be read."))
		return
	}
	event, err := membershipFromBody(body)
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeNotJSON,
			"The send_"+membership+" request is not readable JSON."))
		return
	}
	room, err := d.DB.Rooms.ByRoomID(r.Context(), roomID)
	if err != nil {
		d.answerServiceError(w, "send_"+membership, roomID, err)
		return
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot authorise a %s in.",
			roomID, room.RoomVersion, membership).WithExtra(map[string]any{"room_version": room.RoomVersion}))
		return
	}
	if problem := d.membershipEventProblem(event, roomID, eventID, rules, membership); problem != "" {
		d.Log.Warn("federation: a send_"+membership+" did not match the membership it was offered as",
			"room_id", roomID, "event_id", eventID, "origin", origin, "problem", problem)
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam, problem))
		return
	}

	sealed, err := events.Canonical(event)
	if err != nil {
		d.Log.Error("federation: cannot encode a membership event", "room_id", roomID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	accepted, err := d.Rooms.AcceptRemoteEvent(r.Context(), sealed,
		func(event map[string]any, rules roomversion.Rules) error {
			return events.Verify(rules, event, d.Keyring.LookupForRoom(r.Context(), rules))
		})
	if err != nil {
		// The reason goes back to the peer: a membership change refused without an explanation is
		// a membership change that will be retried until the retry budget runs out.
		d.Log.Debug("federation: refused a membership event",
			"room_id", roomID, "event_id", eventID, "membership", membership, "reason", err.Error())
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden, err.Error()))
		return
	}

	if membership == eventauth.MembershipKnock {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"knock_room_state": d.knockRoomState(r, roomID)})
	} else {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{})
	}
	d.Log.Info("federation: a remote user's membership change was accepted into a room this server holds",
		"room_id", roomID, "event_id", accepted, "membership", membership, "sender", event["sender"])
}

// membershipFromBody reads the event a send_* body offers.
//
// Two shapes arrive in practice. The specification's v1 endpoints wrap the event in an `event`
// key; the implementations that actually talk to this server — gomatrixserverlib, and Complement
// through it — put the signed event in the body directly, which is also what the v2 endpoints do.
// Reading either into the same event keeps the checks that follow from drifting apart, and neither
// reading can smuggle anything: what is checked next is the event's own room ID, event ID and
// signatures, not where it was found in the body.
//
// The v1 `/send_knock` body also carries the knocker's own `knock_room_state` — the stripped state
// it holds from an earlier membership, offered so this server can recognise the event's history.
// It is not read. A knock that follows an earlier membership in a room this server holds needs no
// help recognising it, and a state list handed over by a server outside the room is not something
// this server's room should be built from.
func membershipFromBody(body []byte) (map[string]any, error) {
	var envelope struct {
		Event map[string]any `json:"event"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Event != nil {
		return envelope.Event, nil
	}
	return events.Parse(body)
}

// knockRoomState is what a server that has knocked on a room is told about it: enough of the room
// to recognise it — the room's own creation, its name, topic and avatar, how it may be joined —
// and not who is in it.
//
// The events go out as full PDUs rather than the stripped form the older text describes, because a
// knocker checks the events it is handed rather than trusting them, and a stripped event has no
// signature to check. Membership is the exception that stays out: knocking is precisely the state
// of not being allowed to see the room's members.
func (d *Deps) knockRoomState(r *http.Request, roomID string) []json.RawMessage {
	state, err := d.Joins.StatePDUs(r.Context(), roomID)
	if err != nil {
		d.Log.Error("federation: cannot read a room's state for a knock", "room_id", roomID, "error", err)
		return []json.RawMessage{}
	}
	out := make([]json.RawMessage, 0, len(state))
	for _, pdu := range state {
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(pdu, &header) != nil {
			continue
		}
		if header.Type == eventauth.TypeMember {
			continue
		}
		out = append(out, pdu)
	}
	return out
}
