package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// Third-party lookups connect Matrix identifiers to identities on another
// network (email, phone, a bridged chat network). Lattice bridges nothing and
// runs no identity server of its own, so the protocol list is empty and any
// query for a protocol the server does not have fails. Answering is deliberate:
// a 404 on /thirdparty/protocols makes clients show an error, while an empty
// list simply means "no bridges available".

// handleThirdPartyProtocols implements GET /_matrix/client/v3/thirdparty/protocols.
//
//	@Summary	List third-party network protocols
//	@Tags		thirdparty
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/thirdparty/protocols [get]
func (a *App) handleThirdPartyProtocols(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, matrix.Object{})
}

// handleThirdPartyProtocol implements
// GET /_matrix/client/v3/thirdparty/protocol/{protocol}.
//
//	@Summary	Description of one third-party protocol
//	@Tags		thirdparty
//	@Produce	json
//	@Param		protocol	path	string	true	"Protocol identifier"
//	@Success	404			{object}	matrix.Error
//	@Router		/_matrix/client/v3/thirdparty/protocol/{protocol} [get]
func (a *App) handleThirdPartyProtocol(w http.ResponseWriter, r *http.Request) {
	a.writeError(w, r, matrix.ErrNotFound("this homeserver bridges no third-party protocols"))
}

// handleThirdPartyUser implements GET /_matrix/client/v3/thirdparty/user and
// GET /_matrix/client/v3/thirdparty/user/{protocol}.
//
//	@Summary	Query third-party user IDs
//	@Tags		thirdparty
//	@Produce	json
//	@Success	404	{object}	matrix.Error
//	@Router		/_matrix/client/v3/thirdparty/user [get]
func (a *App) handleThirdPartyUser(w http.ResponseWriter, r *http.Request) {
	a.writeError(w, r, matrix.ErrNotFound("this homeserver cannot look up third-party users"))
}

// handleThirdPartyLocation implements GET /_matrix/client/v3/thirdparty/location
// and GET /_matrix/client/v3/thirdparty/location/{protocol}.
//
//	@Summary	Query third-party locations
//	@Tags		thirdparty
//	@Produce	json
//	@Success	404	{object}	matrix.Error
//	@Router		/_matrix/client/v3/thirdparty/location [get]
func (a *App) handleThirdPartyLocation(w http.ResponseWriter, r *http.Request) {
	a.writeError(w, r, matrix.ErrNotFound("this homeserver cannot look up third-party locations"))
}

// handleTurnServer implements GET /_matrix/client/v3/voip/turnServer. Lattice
// runs no TURN server, so clients are told to place calls without relays rather
// than shown an error they cannot act on.
//
//	@Summary	TURN server credentials for VoIP
//	@Tags		voip
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/voip/turnServer [get]
func (a *App) handleTurnServer(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, matrix.Object{
		"username": "",
		"password": "",
		"ttl":      turnServerTTL,
		"uris":     []string{},
	})
}

// turnServerTTL is how long a client may cache the (empty) TURN credentials.
const turnServerTTL = 86400
