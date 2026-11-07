package client

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/sync"
)

// maxSyncTimeout mirrors the service limit. The server's own request timeout is shorter
// than an unbounded poll, so a client asking to wait for an hour would be cut off with a
// 504; answering at the limit instead is what the specification's long poll means.
const maxSyncTimeout = 20 * time.Second

// handleSync implements GET /_matrix/client/v3/sync.
func (d *Deps) handleSync(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	req := sync.Request{
		Filter:      query.Get("filter"),
		FullState:   query.Get("full_state") == "true",
		SetPresence: query.Get("set_presence"),
	}
	// MSC4222's state section, under whichever name the client used. The two flags mean the
	// same thing; the name decides what the response calls it, because a client that asked
	// with the pre-stabilisation spelling looks for the pre-stabilisation field.
	switch {
	case query.Get("use_state_after") == "true":
		req.StateAfter = sync.StateAfterStable
	case query.Get("org.matrix.msc4222.use_state_after") == "true":
		req.StateAfter = sync.StateAfterUnstable
	}
	if since, present := query["since"]; present {
		req.HasSince = true
		req.Since = since[0]
	}
	if raw := query.Get("timeout"); raw != "" {
		millis, err := strconv.Atoi(raw)
		if err != nil || millis < 0 {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
				"The timeout must be a non-negative number of milliseconds."))
			return
		}
		req.Timeout = time.Duration(millis) * time.Millisecond
		if req.Timeout > maxSyncTimeout {
			req.Timeout = maxSyncTimeout
		}
	}

	userID := auth.UserID(r)
	if !d.applySyncPresence(w, r, userID, req.SetPresence) {
		return
	}
	result, err := d.Sync.Sync(r.Context(), userID, req)
	if err != nil {
		if r.Context().Err() != nil {
			// The client stopped a long poll. There is no response to write and nothing
			// to log as a fault.
			return
		}
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// handleFilterCreate implements POST /_matrix/client/v3/user/{userID}/filter.
func (d *Deps) handleFilterCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := d.requireSelf(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, d.maxBody()))
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeNotJSON,
			"The filter could not be read."))
		return
	}
	id, err := d.Sync.SaveFilter(r.Context(), userID, body)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"filter_id": strconv.FormatInt(id, 10)})
}

// handleFilterGet implements GET /_matrix/client/v3/user/{userID}/filter/{filterID}.
//
// The body is written back exactly as it was stored. A filter is the client's document:
// re-encoding it through Go maps would reorder keys and drop nothing of substance, but
// answering with something other than what the client sent invites a client that compares
// filters to conclude its filter changed.
func (d *Deps) handleFilterGet(w http.ResponseWriter, r *http.Request) {
	userID, ok := d.requireSelf(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(pathValue(r, "filterID"), 10, 64)
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
			"A filter ID is a number."))
		return
	}
	body, err := d.Sync.FilterBody(r.Context(), userID, id)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteRawJSON(w, http.StatusOK, body)
}

// requireSelf checks that the user in the path is the account the token belongs to. The
// specification scopes these resources to the owner; accepting another user ID would make
// `/user/{other}/filter` a read of someone else's stored filter.
func (d *Deps) requireSelf(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := pathValue(r, "userID")
	if err := matrixid.UserValid(userID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid user ID.", userID))
		return "", false
	}
	if userID != auth.UserID(r) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"A user may only read or store their own filters."))
		return "", false
	}
	return userID, true
}
