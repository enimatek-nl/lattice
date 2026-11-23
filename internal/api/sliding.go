package api

import (
	"net/http"
	"strconv"

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
//
// slidingSyncStablePath is MSC4186's own path. The unstable one is registered beside it and
// served by the same handler.
const slidingSyncStablePath = "/_matrix/client/v4/sync"

func (a *App) handleSlidingSync(w http.ResponseWriter, r *http.Request) {
	var req matrix.SlidingSyncRequest
	if err := decodeBody(r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := readSlidingQuery(r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	// A connection outlives the request that carries it, so it belongs to the authenticated
	// device rather than to the token: two tokens of one device share a connection, and a
	// token of another device cannot speak for it.
	userID := auth.GetUserID(r)
	deviceID := auth.GetDeviceID(r)
	// Which URL a request came in on is part of the answer, not of the data. The extension MSCs
	// say so themselves: their unstable-prefix sections keep the answer shape of the
	// pre-MSC implementations on the unstable URL and ask for the accepted shape on the stable
	// one. The rooms themselves are identical either way.
	legacyEndpoint := r.URL.Path != slidingSyncStablePath
	response, err := a.svc.SlidingSync(r.Context(), userID, deviceID, &req, legacyEndpoint)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	// set_presence is validated by the service and has nothing to act on here: presence on
	// this server is local, and the classic sync does not write it from a poll either.
	writeJSON(w, http.StatusOK, response)
}

// readSlidingQuery fills the three parameters of sliding sync from the query string where the
// body left them out.
//
// MSC4186 moved pos, timeout and set_presence from the URL into the body, and a client is
// right to send them there, so the body always wins when it names one. Reading the URL as well
// costs nothing and asks for nothing: a request that sends a parameter in both places is read
// the way the spec describes it, and a client that only knows the older placement is still
// understood. The first case is not hypothetical - the MSC asks for set_presence as a "URL
// param" in one place and lists it among the body fields in another.
//
// What this does not do is accept a different *shape* anywhere else: the parameters arrive here
// in the form the MSC defines them, and everything else about a request is read strictly.
func readSlidingQuery(r *http.Request, req *matrix.SlidingSyncRequest) error {
	query := r.URL.Query()
	if req.Pos == "" {
		req.Pos = query.Get("pos")
	}
	if req.Timeout == nil {
		if raw := query.Get("timeout"); raw != "" {
			ms, err := strconv.Atoi(raw)
			if err != nil {
				return matrix.ErrInvalidParam("timeout %q is not a number of milliseconds", raw)
			}
			req.Timeout = &ms
		}
	}
	if req.SetPresence == "" {
		req.SetPresence = query.Get("set_presence")
	}
	return nil
}
