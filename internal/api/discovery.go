package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// handleWellKnownClient implements GET /.well-known/matrix/client.
func (a *App) handleWellKnownClient(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.svc.HomeServerConfiguration())
}

// handleWellKnownServer implements GET /.well-known/matrix/server.
func (a *App) handleWellKnownServer(w http.ResponseWriter, r *http.Request) {
	// This document is an instruction to strangers about where this server can be
	// reached, and a stranger that cannot reach it has no way to prove a word it was
	// sent. The server name alone means port 8448, so a deployment behind a proxy on
	// 443 says so here rather than leaving every peer to knock on a closed port.
	advertised := a.cfg.FederationAdvertise
	if advertised == "" {
		advertised = a.cfg.ServerName
	}
	writeJSON(w, http.StatusOK, matrix.Object{"m.server": advertised})
}

// handleWellKnownSupport implements GET /.well-known/matrix/support.
func (a *App) handleWellKnownSupport(w http.ResponseWriter, r *http.Request) {
	body := matrix.Object{}
	if a.cfg.SupportEmail != "" {
		body["matrix_id"] = a.cfg.AdminMatrixID
		body["email_address"] = a.cfg.SupportEmail
	} else {
		body["m.homeserver"] = matrix.Object{}
	}
	writeJSON(w, http.StatusOK, body)
}

// handleVersions implements GET /_matrix/client/versions.
func (a *App) handleVersions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.svc.Versions())
}

// handleCapabilities implements GET /_matrix/client/v1/capabilities.
func (a *App) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.svc.Capabilities())
}

// handleAuthMetadata implements GET /_matrix/client/v1/auth_metadata. Lattice
// authenticates with passwords and access tokens rather than OAuth 2.0.
func (a *App) handleAuthMetadata(w http.ResponseWriter, r *http.Request) {
	a.writeError(w, r, matrix.ErrNotFound("this homeserver does not use OAuth 2.0 for authentication"))
}
