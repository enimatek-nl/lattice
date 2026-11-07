package client

import (
	"net/http"
	"strings"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/rooms"
)

// createRoomBody is the request body of PUT/POST /createRoom.
//
// Only the members this server acts on are decoded. `room_upgrade_trap` is part of the
// API but affects room upgrades, which are not implemented; `visibility` decides whether
// the room starts out in this server's public room directory; `room_alias_name` is refused rather than
// ignored, because a room created without the alias the client asked for is a room
// the client cannot find.
type createRoomBody struct {
	Preset                    string           `json:"preset"`
	Name                      string           `json:"name"`
	Topic                     string           `json:"topic"`
	AvatarURL                 string           `json:"avatar_url"`
	Visibility                string           `json:"visibility"`
	RoomVersion               string           `json:"room_version"`
	CreationContent           map[string]any   `json:"creation_content"`
	InitialState              []initialStateIn `json:"initial_state"`
	PowerLevelContentOverride map[string]any   `json:"power_level_content_override"`
	RoomAliasName             *string          `json:"room_alias_name"`
	Invite                    []string         `json:"invite"`
	// IsDirect says the room is being created as a private conversation with the people it
	// invites. It is not a label on the room: the specification records it in the invitations
	// themselves and in the creator's own account data, so that the invited user's other
	// devices, and their homeserver when they live elsewhere, see the same thing the inviter
	// meant.
	IsDirect bool `json:"is_direct"`
}

type initialStateIn struct {
	Type     string         `json:"type"`
	StateKey string         `json:"state_key"`
	Content  map[string]any `json:"content"`
}

// handleCreateRoom implements POST /_matrix/client/v3/createRoom.
func (d *Deps) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var body createRoomBody
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if body.Visibility != "" && body.Visibility != "private" && body.Visibility != "public" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"visibility %q is not one of private or public.", body.Visibility))
		return
	}

	request := rooms.CreateRequest{
		Published:          body.Visibility == "public",
		Creator:            auth.UserID(r),
		Preset:             rooms.Preset(body.Preset),
		RoomVersion:        body.RoomVersion,
		CreationContent:    body.CreationContent,
		PowerLevelOverride: body.PowerLevelContentOverride,
		Name:               body.Name,
		Topic:              body.Topic,
		AvatarURL:          body.AvatarURL,
	}
	if body.RoomAliasName != nil {
		request.RoomAliasName = *body.RoomAliasName
	}
	request.Invite = body.Invite
	request.IsDirect = body.IsDirect
	for _, entry := range body.InitialState {
		if err := matrixid.EventTypeValid(entry.Type); err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"initial_state contains an invalid event type %q.", entry.Type))
			return
		}
		request.InitialState = append(request.InitialState, rooms.StateContent{
			Type: entry.Type, StateKey: entry.StateKey, Content: entry.Content,
		})
	}

	roomID, err := d.Rooms.Create(r.Context(), request)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	answer := map[string]any{"room_id": roomID}
	// The room's version travels with the answer because a client that asked for one has to be
	// able to tell whether it got it, and a client that did not has to know which version it is
	// about to speak — /send, /state and /context all behave differently in a room whose events are
	// named by their hash. The room service decided which version this room is; the answer repeats
	// that decision rather than recomputing it, so the two cannot drift.
	if room, err := d.DB.Rooms.ByRoomID(r.Context(), roomID); err == nil {
		answer["room_version"] = room.RoomVersion
	} else {
		d.Log.Warn("a room was created but its row could not be read back for the answer",
			"room_id", roomID, "error", err)
	}
	httpx.WriteJSON(w, http.StatusOK, answer)
}

// sendBody is the content of the event a client wants to send. The body of
// /send/{type}/{txn} *is* the content: everything else about the event is decided by
// this server, which is what keeps a client from forging a sender or a timestamp.
type sendBody map[string]any

// handleRoomSend implements PUT /_matrix/client/v3/rooms/{roomID}/send/{eventType}/{txnID}.
func (d *Deps) handleRoomSend(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	eventType := r.PathValue("eventType")
	txnID := r.PathValue("txnID")

	if err := matrixid.RoomIDValid(roomID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid room ID.", roomID))
		return
	}
	if err := matrixid.EventTypeValid(eventType); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid event type.", eventType))
		return
	}
	if txnID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A transaction ID is required so that a retry can be recognised."))
		return
	}
	// Membership changes have their own endpoints (join, invite, knock, leave), which
	// is where the specification puts the extra parameters they need: an invitation
	// needs the third-party invite flow, a join needs the room version check and the
	// state it returns. Accepting them here would mean a second, weaker path to the
	// same state change.
	if eventType == "m.room.member" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeUnrecognized,
			"Membership changes use the join, invite, knock and leave endpoints, not /send."))
		return
	}

	var content sendBody
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &content) {
		return
	}
	if content == nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeBadJSON,
			"The event content must be a JSON object."))
		return
	}

	eventID, err := d.Rooms.Send(r.Context(), rooms.SendRequest{
		RoomID:    roomID,
		Sender:    auth.UserID(r),
		DeviceID:  auth.DeviceID(r),
		TxnID:     txnID,
		EventType: eventType,
		Content:   content,
	})
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"event_id": eventID})
}

// handleRedact implements PUT /_matrix/client/v3/rooms/{roomID}/redact/{eventID}/{txnID}.
//
// The client names the event to redact in the path and may give a `reason`; where the
// target field goes in the resulting event is this server's decision, because the room
// version decides it.
func (d *Deps) handleRedact(w http.ResponseWriter, r *http.Request) {
	roomID := pathValue(r, "roomID")
	targetID := pathValue(r, "eventID")
	txnID := r.PathValue("txnID")

	if err := matrixid.RoomIDValid(roomID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid room ID.", roomID))
		return
	}
	if err := matrixid.EventIDValid(targetID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid event ID.", targetID))
		return
	}
	if txnID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A transaction ID is required so that a retry can be recognised."))
		return
	}

	var body map[string]any
	if r.Body != nil && r.ContentLength != 0 {
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
			return
		}
	}
	reason := ""
	if raw, ok := body["reason"]; ok {
		text, ok := raw.(string)
		if !ok {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeBadJSON,
				"The reason for a redaction must be a string."))
			return
		}
		reason = text
	}

	eventID, err := d.Rooms.Redact(r.Context(), rooms.RedactRequest{
		RoomID:   roomID,
		Sender:   auth.UserID(r),
		DeviceID: auth.DeviceID(r),
		TxnID:    txnID,
		TargetID: targetID,
		Reason:   reason,
	})
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"event_id": eventID})
}

// statePathRemainder counts the path segments after the event type in a
// /rooms/{roomID}/state/{eventType}/... path. It returns 0 for an empty state key and
// 1 for a present one; anything larger means the client sent a path this endpoint does
// not have.
func statePathRemainder(path, eventType string) int {
	index := strings.Index(path, "/state/")
	if index < 0 {
		return 0
	}
	rest := path[index+len("/state/"):]
	parts := strings.Split(rest, "/")
	// parts[0] is the event type segment.
	if len(parts) <= 1 {
		return 0
	}
	return len(parts) - 1
}

// handleRoomState implements PUT /_matrix/client/v3/rooms/{roomID}/state/{eventType}/{stateKey}.
//
// Sending state is separated from sending messages because they need different
// authorisation decisions: a state event is judged against the state_default power
// level, and its state key may be a user ID, which rule 8 treats as an action on that
// user.
func (d *Deps) handleRoomState(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	eventType := r.PathValue("eventType")
	stateKey := r.PathValue("stateKey")

	if err := matrixid.RoomIDValid(roomID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid room ID.", roomID))
		return
	}
	if err := matrixid.EventTypeValid(eventType); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid event type.", eventType))
		return
	}
	// A state event whose state key is empty is addressed as `/state/{type}/`, with
	// the trailing empty segment carrying the meaning. That pattern also catches
	// longer paths, so the shape is checked here rather than assumed: /state/a/b/c is
	// not a state event with state key "b/c", it is a malformed request.
	if extra := statePathRemainder(r.URL.Path, eventType); extra > 1 {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
			"A state event is addressed as /state/{event type}/{state key}; the state key may be empty."))
		return
	}

	var content sendBody
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &content) {
		return
	}
	if content == nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeBadJSON,
			"The event content must be a JSON object."))
		return
	}

	eventID, err := d.Rooms.Send(r.Context(), rooms.SendRequest{
		RoomID:    roomID,
		Sender:    auth.UserID(r),
		DeviceID:  auth.DeviceID(r),
		EventType: eventType,
		StateKey:  stateKey,
		IsState:   true,
		Content:   content,
	})
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"event_id": eventID})
}
