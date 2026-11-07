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

// handleRelations implements GET /rooms/{roomID}/relations/{eventID}[/{relType}[/{eventType}]]:
// what this server holds that says it relates to one event.
//
// The two optional path segments are filters, not separate endpoints, and they are named in the
// path rather than the query because a client asking for a thread's replies and a client asking
// for its reactions want different pages under the same URL shape. An empty segment — which a
// client cannot produce on this route — would mean "any".
//
// Direction and paging work exactly as they do on `/messages`, and that is deliberate: a client
// that knows how to walk a timeline already knows how to walk a thread.
func (d *Deps) handleRelations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	req := rooms.RelationsRequest{
		RoomID:        pathValue(r, "roomID"),
		TargetEventID: pathValue(r, "eventID"),
		RelType:       pathValue(r, "relType"),
		EventType:     pathValue(r, "eventType"),
		From:          query.Get("from"),
		Dir:           query.Get("dir"),
		Requester:     auth.UserID(r),
	}
	if req.Dir != "" && req.Dir != "f" && req.Dir != "b" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a direction; use \"f\" or \"b\".", req.Dir))
		return
	}
	// Both filters are event-type shaped: a relation type is a name in the same grammar, and
	// an event type has to be one. Anything else is a client's mistake, said plainly, rather
	// than a page that happens to be empty for a reason the client cannot see.
	for _, name := range []string{req.RelType, req.EventType} {
		if name == "" {
			continue
		}
		if err := matrixid.EventTypeValid(name); err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not a valid event or relation type: %s", name, err))
			return
		}
	}
	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not a limit; it has to be a non-negative integer.", raw))
			return
		}
		req.Limit = limit
	}
	page, err := d.Rooms.Relations(r.Context(), req)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
