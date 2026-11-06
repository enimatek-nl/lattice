package federation

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
)

// GapReader answers what a peer is missing between two points of a room's history. The room
// service implements it: the walk is over the event graph, and the event graph is the room
// service's to read.
type GapReader interface {
	EventsBetween(ctx context.Context, roomID string, earliest, latest []string, limit int, minDepth int64) ([]*persist.Event, error)
}

// handleGetMissingEvents implements POST /_matrix/federation/v1/get_missing_events/{roomId}.
//
// This is the endpoint a server uses when an event it was handed cites history it was never
// given: it names the events it already holds at both ends of the hole and asks for what lies
// between. Answering it is what stops a room's history from being permanently torn for the
// server that joined late, and it is the reason an event with a missing predecessor can be
// placed rather than parked forever as an outlier.
//
// The two lists are required, and a request that does not supply them is not asking a question
// this server can answer: without the floor, "give me the previous events" is "give me the room",
// and without the ceiling it is a request to walk a graph a peer chose. Depth and limit bound the
// walk further; the limit's default is the specification's, and its ceiling is this server's,
// because a synchronous endpoint must not be made to do unbounded work by the side that asked.
//
// Who may ask is the same question `GET /event` asks: a room that has excluded this server gets
// nothing, and neither does a server with nobody joined in the room. History is not handed to
// outsiders because they named an event in it.
func (d *Deps) handleGetMissingEvents(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if roomID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"The room ID is missing."))
		return
	}
	var request struct {
		EarliestEvents *[]string `json:"earliest_events"`
		LatestEvents   *[]string `json:"latest_events"`
		Limit          int       `json:"limit"`
		MinDepth       int64     `json:"min_depth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeNotJSON,
			"The request body is not JSON."))
		return
	}
	if request.EarliestEvents == nil || request.LatestEvents == nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"earliest_events and latest_events are both required."))
		return
	}
	earliest := *request.EarliestEvents
	latest := *request.LatestEvents
	if len(latest) == 0 {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"latest_events must name at least one event."))
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if d.roomExcludes(r, roomID, origin) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"The requesting server is excluded from this room by the room's access control."))
		return
	}
	if !d.serverIsInRoom(r, roomID, origin) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"This server does not know you to be in this room."))
		return
	}
	if d.Gaps == nil {
		// Wired without it: refuse rather than answer with an empty list, which would tell
		// the peer that there is nothing in the hole when this server never looked.
		d.Log.Error("federation: get_missing_events is not wired to a room service")
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"This server cannot answer that question."))
		return
	}
	events, err := d.Gaps.EventsBetween(r.Context(), roomID, earliest, latest, request.Limit, request.MinDepth)
	if err != nil {
		d.Log.Warn("federation: a peer's request for missing events could not be answered",
			"room_id", roomID, "origin", origin, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"This server could not read its history."))
		return
	}
	pdus := make([]json.RawMessage, 0, len(events))
	for _, event := range events {
		pdus = append(pdus, json.RawMessage(pduBytesFor(event)))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"events": pdus})
}

// pduBytesFor chooses the bytes a peer is given for a stored event. A redacted event is handed
// over in its redacted form: a redaction removes content from the event as far as the rest of the
// Matrix network is concerned too, so republishing the original body under it would undo what the
// room decided. The bytes are otherwise the event as stored, because what a peer verifies is the
// exact form that was signed.
func pduBytesFor(event *persist.Event) []byte {
	if event.Redacted && len(event.RedactedJSON) > 0 {
		return event.RedactedJSON
	}
	return event.JSON
}
