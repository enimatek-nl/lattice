package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// handleSync implements GET /_matrix/client/v3/sync.
//
//	@Summary	Sync event data
//	@Description	Long polling sync of the event stream.
//	@Tags		sync
//	@Produce	json
//	@Param		since	query	string	false	"Pagination token from the previous sync"
//	@Param		timeout	query	int		false	"Long poll timeout in milliseconds"
//	@Param		filter	query	string	false	"Filter ID or inline filter JSON"
//	@Success	200		{object}	matrix.SyncResponse
//	@Router		/_matrix/client/v3/sync [get]
func (a *App) handleSync(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r)
	params := service.SyncParams{UserID: userID, DeviceID: auth.GetDeviceID(r)}

	if since := queryValue(r, "since", ""); since != "" {
		pos, err := a.svc.ParseToken(since)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		params.Since = pos
	}
	timeout, err := queryDurationMS(r, "timeout", 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	params.Timeout = timeout
	limit, err := queryInt(r, "limit", 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	params.Limit = limit

	filter := queryValue(r, "filter", "")
	if len(filter) > 0 && filter[0] == '{' {
		params.FilterRaw = filter
	} else {
		params.FilterID = filter
	}

	resp, err := a.svc.Sync(r.Context(), params)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCreateFilter implements POST /_matrix/client/v3/user/{userId}/filter.
//
//	@Summary	Create a sync filter
//	@Tags		sync
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]string
//	@Router		/_matrix/client/v3/user/{userId}/filter [post]
func (a *App) handleCreateFilter(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		a.writeError(w, r, matrix.ErrNotJSON("could not read the request body"))
		return
	}
	filterID, err := a.svc.CreateFilter(r.Context(), r.PathValue("userId"), auth.GetUserID(r), string(body))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"filter_id": filterID})
}

// handleGetFilter implements GET /_matrix/client/v3/user/{userId}/filter/{filterId}
// and the v2 alias.
func (a *App) handleGetFilter(w http.ResponseWriter, r *http.Request) {
	filter, err := a.svc.GetFilter(r.Context(), r.PathValue("userId"), auth.GetUserID(r), r.PathValue("filterId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, filter)
}

// handleGetFilterSection implements GET /_matrix/client/v3/user/{userId}/filter/{filterId}/{filterPath}.
func (a *App) handleGetFilterSection(w http.ResponseWriter, r *http.Request) {
	filter, err := a.svc.GetFilter(r.Context(), r.PathValue("userId"), auth.GetUserID(r), r.PathValue("filterId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	section, ok := filter[r.PathValue("filterPath")]
	if !ok {
		a.writeError(w, r, matrix.ErrNotFound("the filter has no %q section", r.PathValue("filterPath")))
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(mustJSON(section)))
}

// mustJSON encodes a value, returning null for failures.
func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return encoded
}
