package client

import (
	"net/http"
	"strconv"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/rooms"
)

// handleRoomHierarchy answers GET /_matrix/client/v1/rooms/{roomID}/hierarchy: the tree under a space,
// one page at a time.
//
// The parameters are the specification's, and the two that shape the walk — `suggested_only` and
// `max_depth` — may not change in the middle of a paginated walk: the pagination token names the tree
// the first page came out of, and a second page drawn from a differently shaped walk would be a mixture
// of two trees presented as one. `limit` is taken from the client and bounded by the server, because the
// question is "how much is under this room" and that is not the client's to answer about this server's
// resources.
func (d *Deps) handleRoomHierarchy(w http.ResponseWriter, r *http.Request) {
	roomID := pathValue(r, "roomID")
	if roomID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A hierarchy request needs a room ID."))
		return
	}
	query := r.URL.Query()
	opts := rooms.HierarchyOptions{
		// A request that names no depth is not a request for no depth. The room service walks to its
		// own bound; 0 means the client asked for the requested room alone.
		MaxDepth: -1,
		From:     query.Get("from"),
	}
	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"limit %q is not a positive integer.", raw))
			return
		}
		opts.Limit = limit
	}
	if raw := query.Get("max_depth"); raw != "" {
		depth, err := strconv.Atoi(raw)
		if err != nil || depth < 0 {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"max_depth %q is not a non-negative integer.", raw))
			return
		}
		opts.MaxDepth = depth
	}
	if raw := query.Get("suggested_only"); raw != "" {
		suggested, err := strconv.ParseBool(raw)
		if err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"suggested_only %q is not a boolean.", raw))
			return
		}
		opts.SuggestedOnly = suggested
	}
	page, err := d.Rooms.Hierarchy(r.Context(), roomID, auth.UserID(r), opts)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
