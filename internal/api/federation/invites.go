package federation

import (
	"context"
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

// InviteAcceptor stores an invite for one of this server's users, having signed it here as the
// inviting server asked. It is implemented by [*rooms.Service].
type InviteAcceptor interface {
	AcceptRemoteInvite(ctx context.Context, event map[string]any, roomVersion string,
		verify func(map[string]any, roomversion.Rules) error) (map[string]any, error)
}

// handleInvite implements PUT /_matrix/federation/v2/invite/{roomId}/{eventId} and its v1
// namesake.
//
// This is the endpoint where a server says "one of your users has been invited into this room",
// and asks this server to put its signature on the claim. The signature is not a formality: it is
// what lets the rest of the room believe the invite at all. So the checks here are about what this
// server is being asked to promise.
//
// The invitation may only be about a user who lives here — an event whose state key is somebody
// else's user is not this server's to sign — and the sender must live on the server that sent it,
// because otherwise one server would be speaking for another server's user. Neither of those is a
// signature question: the transaction-level authentication says who is asking, and the event's own
// signature says nobody tampered with it in between; what these two checks settle is whose claim
// this is.
//
// The room version arrives in the v2 body. v1 has nowhere to put it, which is why the
// specification says a v1 invite is a room version 1 or 2 room — if this server already holds the
// room its own record wins, and if it does not, the older form cannot promise anything better.
func (d *Deps) handleInvite(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	eventID := r.PathValue("eventId")
	if roomID == "" || eventID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"An invite needs a room ID and an event ID."))
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if d.refusedByRoomACL(w, r, roomID, origin) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, d.federationBodyLimit()))
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeBadRequest,
			"The invite could not be read."))
		return
	}
	var request struct {
		RoomVersion string         `json:"room_version"`
		InviteEvent map[string]any `json:"invite_event"`
	}
	// The v2 body names the event and the room version; the v1 body is the event itself, with
	// no envelope. Reading either into one shape keeps the checks that follow — which are the
	// same for both — from drifting apart.
	if err := json.Unmarshal(body, &request); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeNotJSON,
			"The invite request is not readable JSON."))
		return
	}
	event := request.InviteEvent
	if event == nil {
		if err := json.Unmarshal(body, &event); err != nil || event == nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeNotJSON,
				"The invite request is not readable JSON."))
			return
		}
	}
	version, problem := d.inviteRoomVersion(r.Context(), roomID, request.RoomVersion)
	if problem != "" {
		d.Log.Warn("federation: an invite arrived for a room version this server cannot handle",
			"room_id", roomID, "event_id", eventID, "problem", problem)
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam, "%s", problem))
		return
	}
	if problem := d.inviteProblem(event, roomID, eventID, origin); problem != "" {
		d.Log.Warn("federation: an invite failed the checks on what it claims",
			"room_id", roomID, "event_id", eventID, "origin", origin, "problem", problem)
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam, problem))
		return
	}
	if d.Invites == nil {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"This server does not accept invites."))
		return
	}
	countersigned, err := d.Invites.AcceptRemoteInvite(r.Context(), event, version,
		func(event map[string]any, rules roomversion.Rules) error {
			return events.Verify(rules, event, d.Keyring.LookupForRoom(r.Context(), rules))
		})
	if err != nil {
		d.Log.Debug("federation: an invite was refused here",
			"room_id", roomID, "event_id", eventID, "origin", origin, "reason", err.Error())
		d.answerServiceError(w, "invite", roomID, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"event": countersigned})
	d.Log.Info("federation: a remote server invited one of this server's users into a room",
		"room_id", roomID, "event_id", eventID, "origin", origin,
		"inviter", event["sender"], "invited", event["state_key"])
}

// inviteRoomVersion decides which room version an invite is written in. v2 says so; v1 cannot, and
// the specification's answer for that case is a room version 1 or 2 room.
func (d *Deps) inviteRoomVersion(ctx context.Context, roomID, asked string) (version, problem string) {
	if asked != "" {
		if _, ok := roomversion.Get(asked); !ok {
			return "", "This server cannot handle room version " + asked + "."
		}
		return asked, ""
	}
	if room, err := d.DB.Rooms.ByRoomID(ctx, roomID); err == nil && room != nil {
		if _, ok := roomversion.Get(room.RoomVersion); !ok {
			return "", "Room " + roomID + " uses room version " + room.RoomVersion +
				", which this server cannot handle."
		}
		return room.RoomVersion, ""
	}
	// The v1 endpoint has nowhere to put the room version, so an invite for a room this server
	// has never held can only be a version 1 or 2 room. Assuming a newer version would authorise
	// the invite against rules the room may not use, which is the mistake the specification's
	// note is there to prevent.
	return "1", ""
}

// inviteProblem is the list of checks the specification requires before signing an invite. Each
// one is a claim that this server would be putting its signature behind.
func (d *Deps) inviteProblem(event map[string]any, roomID, eventID, origin string) string {
	if kind, _ := event["type"].(string); kind != eventauth.TypeMember {
		return "An invite has to be an m.room.member event."
	}
	content, _ := event["content"].(map[string]any)
	if membership, _ := content["membership"].(string); membership != eventauth.MembershipInvite {
		return "The membership the invite claims is not invite."
	}
	if got, _ := event["room_id"].(string); got != roomID {
		return "The invite belongs to a different room than the one it was sent to."
	}
	sender, _ := event["sender"].(string)
	senderDomain, err := matrixid.UserDomain(sender)
	if err != nil {
		return "The invite's sender is not a valid user ID."
	}
	if origin != "" && senderDomain != origin {
		// The server that sent this request may speak for users on it, and for nobody else.
		// An invite from another server's user, carried by a server that is not that user's,
		// is how a stranger would invite somebody.
		return "The invite's sender does not live on the server that sent it."
	}
	if kind, _ := event["origin"].(string); kind != "" && kind != senderDomain {
		return "The invite names a server other than its sender's."
	}
	stateKey, _ := event["state_key"].(string)
	invitedDomain, err := matrixid.UserDomain(stateKey)
	if err != nil {
		return "The user the invite is for is not a valid user ID."
	}
	if !matrixid.IsLocal(invitedDomain, d.Cfg.ServerName) {
		// This server can sign for its own users only. Signing an invite for somebody else's
		// user would be this server introducing a user it cannot introduce.
		return "The user the invite is for does not live on this server."
	}
	if claimed, _ := event["event_id"].(string); claimed != "" && claimed != eventID {
		return "The invite is not the event its event ID names."
	}
	return ""
}
