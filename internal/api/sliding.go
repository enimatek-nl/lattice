package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// handleSlidingSync implements POST /_matrix/client/v4/sync, and the unstable path
// org.matrix.simplified_msc3575 served beside it.
//
//	@Summary	Sliding sync
//	@Description	Sync a window of rooms, with only what changed on this connection.
//	@Tags		sync
//	@Accept		json
//	@Produce	json
//	@Param		body	body	matrix.SlidingSyncRequest	false	"Sliding sync request"
//	@Success	200		{object}	matrix.SlidingSyncResponse
//	@Router		/_matrix/client/v4/sync [post]
func (a *App) handleSlidingSync(w http.ResponseWriter, r *http.Request) {
	var req matrix.SlidingSyncRequest
	if err := decodeBody(r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	// A connection outlives the request that carries it, so it belongs to the authenticated
	// device rather than to the token: two tokens of one device share a connection, and a
	// token of another device cannot speak for it.
	userID := auth.GetUserID(r)
	deviceID := auth.GetDeviceID(r)
	response, err := a.svc.SlidingSync(r.Context(), userID, deviceID, &req)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	// set_presence is validated by the service and has nothing to act on here: presence on
	// this server is local, and the classic sync does not write it from a poll either.
	writeJSON(w, http.StatusOK, response)
}
