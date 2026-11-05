package client

import (
	"net/http"
	"strconv"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/rooms"
)

// handleRoomMessages implements GET /_matrix/client/v3/rooms/{roomID}/messages.
//
// The `filter` parameter is accepted as an inline JSON object only. The specification
// also allows a stored filter ID here; this server's filter store holds the sync filter
// shape, and reading a stored filter as a room event filter would silently ignore fields
// it does not understand, so an ID is refused instead.
func (d *Deps) handleRoomMessages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	lazyMembers, err := rooms.ParseHistoryFilter(query.Get("filter"))
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
	page, err := d.Rooms.History(r.Context(), pathValue(r, "roomID"), auth.UserID(r), rooms.HistoryRequest{
		From:        query.Get("from"),
		To:          query.Get("to"),
		Dir:         query.Get("dir"),
		Limit:       limit,
		LazyMembers: lazyMembers,
	})
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
