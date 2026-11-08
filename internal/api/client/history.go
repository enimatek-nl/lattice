package client

import (
	"net/http"
	"strconv"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/rooms"
)

// handleRoomEvent implements GET /_matrix/client/v3/rooms/{roomID}/event/{eventID}.
func (d *Deps) handleRoomEvent(w http.ResponseWriter, r *http.Request) {
	eventID := pathValue(r, "eventID")
	if err := matrixid.EventIDValid(eventID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid event ID.", eventID))
		return
	}
	event, err := d.Rooms.Event(r.Context(), pathValue(r, "roomID"), eventID, auth.UserID(r))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	presentEvent(pathValue(r, "roomID"), event)
	httpx.WriteJSON(w, http.StatusOK, event)
}

// handleRoomMessages implements GET /_matrix/client/v3/rooms/{roomID}/messages.
//
// The `filter` parameter is accepted as an inline JSON object only. The specification
// also allows a stored filter ID here; this server's filter store holds the sync filter
// document, whose event filters sit under `room.timeline`, so reading a stored filter as
// the room event filter this endpoint wants would silently ignore half of it — an ID is
// refused instead. Inline, the type, sender, room and `contains_url` lists are all
// applied, and a filter of a shape this server cannot read is refused rather than kept.
func (d *Deps) handleRoomMessages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter, err := rooms.ParseHistoryFilter(query.Get("filter"))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	limit := 0
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			d.serviceError(w, matrixerr.New(matrixerr.CodeInvalidParam,
				"The limit parameter must be a non-negative integer."))
			return
		}
		limit = value
	}
	roomID := pathValue(r, "roomID")
	page, err := d.Rooms.History(r.Context(), roomID, auth.UserID(r), rooms.HistoryRequest{
		From:   query.Get("from"),
		To:     query.Get("to"),
		Dir:    query.Get("dir"),
		Limit:  limit,
		Filter: filter,
	})
	if err != nil {
		d.serviceError(w, err)
		return
	}
	presentEvents(roomID, page.Chunk)
	presentEvents(roomID, page.State)
	httpx.WriteJSON(w, http.StatusOK, page)
}

// handleRoomContext implements GET /_matrix/client/v1/rooms/{roomID}/context/{eventID}: the events
// around one event, and the two tokens that turn a jump into a walk.
//
// The `limit` is the ceiling on both sides together, which is what the specification says and is
// worth stating on the way past because a client reading `limit` as "each way" gets twice the page
// it asked for. `filter` is accepted in the same inline form `/messages` accepts and for the same
// reason, and — also as on `/messages` — a stored filter ID is refused rather than guessed at.
// A `limit` of zero is legal and asks for the event and its tokens only.
func (d *Deps) handleRoomContext(w http.ResponseWriter, r *http.Request) {
	eventID := pathValue(r, "eventID")
	if err := matrixid.EventIDValid(eventID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid event ID.", eventID))
		return
	}
	query := r.URL.Query()
	filter, err := rooms.ParseHistoryFilter(query.Get("filter"))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	limit := (*int)(nil)
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			d.serviceError(w, matrixerr.New(matrixerr.CodeInvalidParam,
				"The limit parameter must be a non-negative integer."))
			return
		}
		limit = &value
	}
	roomID := pathValue(r, "roomID")
	page, err := d.Rooms.Context(r.Context(), roomID, eventID, auth.UserID(r), rooms.ContextRequest{
		Limit:  limit,
		Filter: filter,
	})
	if err != nil {
		d.serviceError(w, err)
		return
	}
	presentEvent(roomID, page.Event)
	presentEvents(roomID, page.EventsBefore)
	presentEvents(roomID, page.EventsAfter)
	presentEvents(roomID, page.State)
	httpx.WriteJSON(w, http.StatusOK, page)
}
