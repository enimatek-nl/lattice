package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// handleRoomStateRead implements GET /_matrix/client/v3/rooms/{roomID}/state.
//
// The events are returned exactly as stored. Re-emitting them with fields added or
// removed would produce objects whose hashes and signatures do not match their event
// IDs, and a client that checked them would conclude the server forged them.
//
// The body is the bare array this endpoint's own schema describes. The specification
// contradicts itself here: its API standards say every endpoint answers with a JSON
// object, while this endpoint documents its response as "Array of ClientEvent" and shows
// a top-level array. The endpoint-specific statement is the one clients act on — Synapse
// answers with an array and Complement's room-state test walks one — so wrapping the list
// here is not a stricter reading of the standards, it is an empty room to a client that
// walks the array and finds no key to walk.
func (d *Deps) handleRoomStateRead(w http.ResponseWriter, r *http.Request) {
	state, err := d.Rooms.CurrentState(r.Context(), r.PathValue("roomID"), auth.UserID(r))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	if state == nil {
		state = []map[string]any{}
	}
	httpx.WriteJSON(w, http.StatusOK, state)
}

// handleRoomStateSingle implements GET /_matrix/client/v3/rooms/{roomID}/state/{eventType}[/{stateKey}].
//
// This endpoint returns the *content* of the state event, not the event: that is what
// the specification says, and clients read the topic as an object rather than digging
// through an event wrapper.
func (d *Deps) handleRoomStateSingle(w http.ResponseWriter, r *http.Request) {
	eventType := r.PathValue("eventType")
	if err := matrixid.EventTypeValid(eventType); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid event type.", eventType))
		return
	}
	if extra := statePathRemainder(r.URL.Path, eventType); extra > 1 {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
			"Room state is addressed as /state/{event type}/{state key}; the state key may be empty."))
		return
	}

	event, found, err := d.Rooms.CurrentStateEvent(r.Context(), r.PathValue("roomID"), eventType,
		r.PathValue("stateKey"), auth.UserID(r))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	if !found {
		// A room that exists but has no such state is a 404 with M_NOT_FOUND, which is
		// how a client distinguishes "not set yet" from "set to nothing".
		matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeNotFound,
			"That room has no state event of that type and state key."))
		return
	}
	// `format=event` asks for the whole event rather than only its content, which a
	// client needs when it wants the sender and the event ID of the state it read.
	if r.URL.Query().Get("format") == "event" {
		httpx.WriteJSON(w, http.StatusOK, event)
		return
	}
	content, ok := event["content"]
	if !ok {
		content = map[string]any{}
	}
	httpx.WriteJSON(w, http.StatusOK, content)
}
