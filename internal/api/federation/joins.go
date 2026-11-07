package federation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/roomversion"
)

// JoinAcceptor is what the join endpoints need from the room service. It is implemented by
// [*rooms.Service].
type JoinAcceptor interface {
	// JoinTemplate builds the unsigned join event a joining server will sign.
	JoinTemplate(ctx context.Context, roomID, userID string) (map[string]any, string, error)
	// StatePDUs is the room's current state as signed bytes.
	StatePDUs(ctx context.Context, roomID string) ([]json.RawMessage, error)
	// AuthChainPDUs and AuthChainIDs walk the events that authorise a set of events.
	AuthChainPDUs(ctx context.Context, seeds []string) ([]json.RawMessage, error)
	AuthChainIDs(ctx context.Context, seeds []string) ([]string, error)
	// ServersInRoom names the homeservers with a joined user in the room.
	ServersInRoom(ctx context.Context, roomID string) ([]string, error)
	// RestrictedJoin decides what a restricted room asks of a join: whether the joining user
	// satisfies one of the room's allow conditions, and which of this server's own members has
	// to be named as the join's authoriser. An empty authoriser means the room asks nothing extra.
	RestrictedJoin(ctx context.Context, roomID, userID string) (string, error)
	// JoinAuthoriserValid reports whether this server can sign behind a named authoriser.
	JoinAuthoriserValid(ctx context.Context, roomID, candidate string) (bool, error)
}

// handleMakeJoin implements GET /_matrix/federation/v1/make_join/{roomId}/{userId}.
//
// A resident server answers this only for a room it is in: it is saying "here is what a join
// by this user would look like in this room, as far as my copy of the room is concerned". The
// joining server signs what it gets back, so what matters here is that the fields the joiner
// cannot compute — where the history ends, which state authorises the join, the depth — come
// from a room this server actually holds.
//
// The room's rules are checked before the template is handed over. A server that only learns
// its user is banned after signing an event has done the cryptographic work for nothing, and
// the same check runs again when the signed event arrives at `/send_join`.
func (d *Deps) handleMakeJoin(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	userID := r.PathValue("userId")
	if roomID == "" || userID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A join needs a room ID and a user ID."))
		return
	}
	if err := matrixid.UserValid(userID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid user ID.", userID))
		return
	}
	template, roomVersion, err := d.Joins.JoinTemplate(r.Context(), roomID, userID)
	if err != nil {
		d.answerServiceError(w, "make_join", roomID, err)
		return
	}
	// `ver` is repeated once per room version and names the versions the *asking* server can
	// speak — it is not that server's guess about which version this room is, and a first value
	// taken from an unordered list proves nothing about the rest of it. What decides the answer is
	// whether this room's version is in the list: if it is, the template is handed over in the
	// room's own version and the response names that version, so the asker signs what the room
	// actually uses; if it is not, no template would help, because the asker cannot authorise the
	// join it is about to sign. Then the useful answer is the room's version itself, which is what
	// M_INCOMPATIBLE_ROOM_VERSION carries it in.
	//
	// A server that names no versions has said nothing about what it can speak, and the
	// specification's documented default for that case is the version 1 room format — a legacy
	// artifact. Refusing on that default would mean every modern room is unjoinable by a server
	// that simply asks, so silence is answered in the room's own version.
	if asked := r.URL.Query()["ver"]; len(asked) > 0 && !slices.Contains(asked, roomVersion) {
		d.Log.Debug("federation: a join was asked by a server that does not list the room's "+
			"room version among the versions it can speak",
			"room_id", roomID, "room_version", roomVersion, "asked_versions", strings.Join(asked, ","))
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Your homeserver does not support the features required to join this room: %q uses room version %s.",
			roomID, roomVersion).WithExtra(map[string]any{"room_version": roomVersion}))
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if d.refusedByRoomACL(w, r, roomID, origin) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"event":        template,
		"room_version": roomVersion,
	})
}

// handleSendJoin implements PUT /_matrix/federation/v2/send_join/{roomId}/{eventId}.
//
// This is where a remote user becomes a member of a room this server holds. The event was
// built from the template this server offered, signed by the server that speaks for the user,
// and is now checked like any other event from a peer — plus the path checks, because the URL
// is what the joining server says it is offering and a mismatch means the two halves of the
// handshake are not about the same event.
//
// The state in the answer is the state *before* the join, which is what the joining server
// needs to catch up: it has to be able to authorise the join event against the room as it was,
// and the room as it is after its own join would be circular.
//
// This server signs the event as well before storing it. That is not decoration: the resident
// server is the one attesting that this join was accepted into the room, and every other
// server in the room sees the event with that signature on it.
func (d *Deps) handleSendJoin(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	eventID := r.PathValue("eventId")
	if roomID == "" || eventID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A send_join needs a room ID and an event ID."))
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if d.refusedByRoomACL(w, r, roomID, origin) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, d.federationBodyLimit()))
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeBadRequest,
			"The join event could not be read."))
		return
	}
	event, err := events.Parse(body)
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeBadRequest,
			"The join event is not readable."))
		return
	}
	room, err := d.DB.Rooms.ByRoomID(r.Context(), roomID)
	if err != nil {
		d.answerServiceError(w, "send_join", roomID, err)
		return
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot authorise a join in.",
			roomID, room.RoomVersion).WithExtra(map[string]any{"room_version": room.RoomVersion}))
		return
	}
	if problem := d.joinEventProblem(event, roomID, eventID, rules); problem != "" {
		d.Log.Warn("federation: a send_join did not match the join it was offered as",
			"room_id", roomID, "event_id", eventID, "problem", problem)
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam, problem))
		return
	}

	// The state has to be read before the join lands in it.
	state, err := d.Joins.StatePDUs(r.Context(), roomID)
	if err != nil {
		d.Log.Error("federation: cannot read a room's state for a send_join",
			"room_id", roomID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	servers, err := d.Joins.ServersInRoom(r.Context(), roomID)
	if err != nil {
		d.Log.Warn("federation: cannot read a room's servers for a send_join",
			"room_id", roomID, "error", err)
	}

	// A restricted room's conditions are worked out again here rather than trusted from the
	// /make_join that came before. The room may have changed its join rule, its allow list or
	// who is in it between the two halves of the handshake, and a resident server that signs a
	// join is putting its name under everybody it is vouching for.
	joining, _ := event["state_key"].(string)
	authoriser, err := d.Joins.RestrictedJoin(r.Context(), roomID, joining)
	if err != nil {
		d.answerServiceError(w, "send_join", roomID, err)
		return
	}
	if authoriser != "" {
		content, _ := event["content"].(map[string]any)
		named, _ := content[eventauth.JACSKey].(string)
		signed, err := d.Joins.JoinAuthoriserValid(r.Context(), roomID, named)
		if err != nil {
			d.Log.Error("federation: cannot read the room for a join authoriser",
				"room_id", roomID, "error", err)
			matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
				"The server encountered an internal error."))
			return
		}
		if !signed {
			// The join names an authoriser this server cannot sign for — somebody else's user,
			// a user who is not in the room, or one who could not have invited anyone. Rule 4.2
			// will reject that event wherever it goes, so the joining server is told now, with
			// the code that means "a different resident server may be able to help you".
			d.Log.Warn("federation: a send_join named an authoriser this server cannot stand behind",
				"room_id", roomID, "event_id", eventID, "authoriser", named)
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeUnableToGrantJoin,
				"This server cannot authorise your join of this room."))
			return
		}
	}

	// This server adds its own signature before storing, so the event that goes on to the
	// room's other servers carries the resident server's word that the join was accepted.
	if err := events.Seal(d.Cfg.ServerName, rules, event, d.Keys); err != nil {
		d.Log.Error("federation: cannot sign a join event", "room_id", roomID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	sealed, err := events.Canonical(event)
	if err != nil {
		d.Log.Error("federation: cannot encode a join event", "room_id", roomID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}

	accepted, err := d.Rooms.AcceptRemoteEvent(r.Context(), sealed,
		func(event map[string]any, rules roomversion.Rules) error {
			return events.Verify(rules, event, d.Keyring.LookupForRoom(r.Context(), rules))
		})
	if err != nil {
		// The joining server is told the reason: a join that is refused without an answer
		// is a join that will be retried until the retry budget runs out.
		d.Log.Debug("federation: refused a join", "room_id", roomID, "event_id", eventID, "reason", err.Error())
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden, err.Error()))
		return
	}

	// The auth chain covers the state being handed over and what authorises the join, so the
	// joining server can check the room rather than believe it. The join event itself is not
	// in its own chain.
	seeds := make([]string, 0, len(state)+1)
	for _, pdu := range state {
		var entry struct {
			EventID string `json:"event_id"`
		}
		if json.Unmarshal(pdu, &entry) == nil && entry.EventID != "" {
			seeds = append(seeds, entry.EventID)
		}
	}
	seeds = append(seeds, events.ReferencedIDs(event, "auth_events")...)
	authChain, err := d.Joins.AuthChainPDUs(r.Context(), seeds)
	if err != nil {
		d.Log.Error("federation: cannot read an auth chain for a send_join",
			"room_id", roomID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"event":            event,
		"state":            state,
		"auth_chain":       authChain,
		"servers_in_room":  servers,
		"origin":           d.Cfg.ServerName,
		"origin_server_ts": time.Now().UTC().UnixMilli(),
	})
	d.Log.Info("federation: a remote user joined a room this server holds",
		"room_id", roomID, "event_id", accepted, "sender", event["sender"])
}

// joinEventProblem checks the path against the event, and the event against what a join has
// to be. The specification lists these checks for the joining side as well; a resident server
// that skips them stores an event whose URL promises something else.
func (d *Deps) joinEventProblem(event map[string]any, roomID, eventID string, rules roomversion.Rules) string {
	if kind, _ := event["type"].(string); kind != eventauth.TypeMember {
		return "A join has to be an m.room.member event."
	}
	if got, _ := event["room_id"].(string); got != roomID {
		return "The join event belongs to a different room than the one it was sent to."
	}
	sender, _ := event["sender"].(string)
	stateKey, _ := event["state_key"].(string)
	if sender == "" || sender != stateKey {
		return "A join event speaks for the user named in both sender and state_key."
	}
	domain, err := matrixid.UserDomain(sender)
	if err != nil {
		return "The join event's sender is not a valid user ID."
	}
	content, _ := event["content"].(map[string]any)
	if membership, _ := content["membership"].(string); membership != eventauth.MembershipJoin {
		return "The event is not a join."
	}
	if origin, _ := event["origin"].(string); origin != "" && origin != domain {
		// The user's server is the one that speaks for the user. An event from another
		// server on their behalf is how a stranger would join somebody.
		return "The join event names a server other than its sender's."
	}
	// The joining server puts the ID it computed in the URL. Recomputing it is what makes
	// that claim mean something: under every other server's hash this content makes one ID,
	// and an event filed under a different one is a different event.
	computed, err := events.EventID(rules, event)
	if err != nil {
		return "The join event's ID cannot be computed from its content."
	}
	if computed != eventID {
		return "The join event is not the event its event ID names."
	}
	return ""
}

// handleStateIDs implements GET /_matrix/federation/v1/state_ids/{roomId}.
//
// A peer asks which events make up this room's state at one of its events, and gets IDs; it
// then fetches the ones it lacks over `/event`. Answering requires the room, and asking
// requires a seat in it.
func (d *Deps) handleStateIDs(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	eventID := r.URL.Query().Get("event_id")
	if roomID == "" || eventID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A state request needs a room ID and an event_id."))
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if !d.serverIsInRoom(r, roomID, origin) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"This server does not know you to be in the room whose state you are asking for."))
		return
	}
	if d.refusedByRoomACL(w, r, roomID, origin) {
		return
	}
	stored, err := d.DB.Events.ByID(r.Context(), eventID)
	if err != nil || stored == nil || stored.RoomID != roomID {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.Newf(matrixerr.CodeNotFound,
			"Event %q is not known to this server.", eventID))
		return
	}
	state, err := d.Joins.StatePDUs(r.Context(), roomID)
	if err != nil {
		d.Log.Error("federation: cannot read a room's state", "room_id", roomID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	ids := make([]string, 0, len(state))
	for _, pdu := range state {
		var entry struct {
			EventID string `json:"event_id"`
		}
		if json.Unmarshal(pdu, &entry) == nil && entry.EventID != "" {
			ids = append(ids, entry.EventID)
		}
	}
	authIDs, err := d.Joins.AuthChainIDs(r.Context(), ids)
	if err != nil {
		d.Log.Error("federation: cannot read an auth chain", "room_id", roomID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"pdu_ids":        ids,
		"auth_chain_ids": authIDs,
	})
}

// handleEventAuth implements GET /_matrix/federation/v1/event_auth/{roomId}/{eventId}.
//
// The auth chain is what a server needs to check an event it has been handed without replaying
// the room's whole history. It is the events' own account of what authorised them, so it is
// answered from what is stored rather than from what this server currently believes the state
// to be.
//
// The history-visibility trimming that the read endpoints apply (see `pduForPeer`) is deliberately
// not applied here. The events in an auth chain are not history: they are the permissions — a
// membership event, the power levels, the join rule — and taking their bodies out leaves the peer
// with a chain it cannot evaluate, so it cannot authorise the event it asked about, cannot decide
// whether to keep it, and cannot tell its own users why the room is inconsistent. A room's
// permissions being unreadable is not the same as its history being private, and a peer that cannot
// check an event is worse off than one that can. What the protocol does ask here is narrower and
// separate: a server with nobody in the room has no standing to be handed the room's membership
// events at all, which is a question about who is being sent what, not about how much of an event to
// show them.
func (d *Deps) handleEventAuth(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	eventID := r.PathValue("eventId")
	if roomID == "" || eventID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"An auth chain request needs a room ID and an event ID."))
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if !d.serverIsInRoom(r, roomID, origin) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"This server does not know you to be in the room that event belongs to."))
		return
	}
	if d.refusedByRoomACL(w, r, roomID, origin) {
		return
	}
	stored, err := d.DB.Events.ByID(r.Context(), eventID)
	if err != nil || stored == nil || stored.RoomID != roomID {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.Newf(matrixerr.CodeNotFound,
			"Event %q is not known to this server.", eventID))
		return
	}
	event, err := events.Parse(stored.JSON)
	if err != nil {
		d.Log.Error("federation: a stored event does not parse", "event_id", eventID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	seeds := events.ReferencedIDs(event, "auth_events")
	chain, err := d.Joins.AuthChainPDUs(r.Context(), seeds)
	if err != nil {
		d.Log.Error("federation: cannot read an auth chain", "event_id", eventID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"auth_chain": chain})
}

// refusedByRoomACL answers a request with 403 M_FORBIDDEN when the room has excluded the server
// that made it, and says whether it did.
//
// The specification names the join, state and auth endpoints as places a room's server ACL has to
// be applied, and prescribes the answer: a room can decide it will not deal with a particular
// server, and that decision is worth nothing if the server is first handed the room's shape and
// then told.
//
// A room service that cannot answer is not answered for: the request goes through and the failure
// is logged. A room's exclusion is a decision recorded in its state; refusing every request because
// this server could not read a table would be trading one outage for another.
func (d *Deps) refusedByRoomACL(w http.ResponseWriter, r *http.Request, roomID, origin string) bool {
	if d.ACLs == nil || roomID == "" || origin == "" {
		return false
	}
	denied, err := d.ACLs.DeniesServer(r.Context(), roomID, origin)
	if err != nil {
		d.Log.Warn("federation: a room's server ACL could not be read, so the request is not refused on it",
			"room_id", roomID, "origin", origin, "error", err)
		return false
	}
	if !denied {
		return false
	}
	d.Log.Info("federation: a server the room excludes was refused",
		"room_id", roomID, "origin", origin)
	matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
		"The requesting server is excluded from this room by the room's access control."))
	return true
}

// answerServiceError answers a room service's refusal with the status its error code carries,
// and keeps the reason in the log where an operator can read it.
func (d *Deps) answerServiceError(w http.ResponseWriter, endpoint, roomID string, err error) {
	if _, protocol := matrixerr.As(err); !protocol {
		d.Log.Error("federation: "+endpoint+" failed", "room_id", roomID, "error", err)
	}
	matrixerr.WriteServiceError(w, err)
}
