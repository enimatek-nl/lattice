// Package client implements the Matrix Client-Server API endpoints.
package client

import (
	"github.com/enimatek/lattice/internal/auth"
	"log/slog"
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/httpserver"
	"github.com/enimatek/lattice/internal/keys"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/users"
)

// Deps are the services the Client-Server API needs.
type Deps struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Users   *users.Service
	Tokens  *auth.TokenService
	UIA     *auth.UIAService
	Keys    *keys.Service
	DB      *persist.Database
	Version string
	Started time.Time
}

// Register installs the Client-Server API routes on r. requireAuth is the
// middleware that enforces a valid access token.
func Register(r *httpserver.Router, d *Deps, requireAuth func(http.Handler) http.Handler) {
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			requireAuth(h).ServeHTTP(w, req)
		}
	}

	// Discovery and versions.
	r.Handle("GET", "/_matrix/client/versions", d.handleVersions)
	r.Handle("GET", "/.well-known/matrix/client", d.handleWellKnownClient)
	r.Handle("GET", "/.well-known/matrix/server", d.handleWellKnownServer)
	r.Handle("GET", "/healthz", d.handleHealth)

	// Authentication.
	r.Handle("GET", "/_matrix/client/v1/login", d.handleLoginFlows)
	r.Handle("GET", "/_matrix/client/v3/login", d.handleLoginFlows)
	r.Handle("POST", "/_matrix/client/v1/login", d.handleLogin)
	r.Handle("POST", "/_matrix/client/v3/login", d.handleLogin)

	r.Handle("GET", "/_matrix/client/v1/register", d.handleRegisterGet)
	r.Handle("GET", "/_matrix/client/v3/register", d.handleRegisterGet)
	r.Handle("POST", "/_matrix/client/v1/register", d.handleRegister)
	r.Handle("POST", "/_matrix/client/v3/register", d.handleRegister)
	r.Handle("GET", "/_matrix/client/v1/register/available", d.handleRegisterAvailable)
	r.Handle("GET", "/_matrix/client/v3/register/available", d.handleRegisterAvailable)

	// Synapse-compatible shared-secret registration. This endpoint is not part of
	// the Matrix specification; it exists because the Complement test harness
	// depends on it, and it is disabled unless a shared secret is configured.
	r.Handle("GET", "/_synapse/admin/v1/register", d.handleAdminRegisterGet)
	r.Handle("POST", "/_synapse/admin/v1/register", d.handleAdminRegister)

	// Session and device management.
	r.Handle("GET", "/_matrix/client/v1/account/whoami", authed(d.handleWhoAmI))
	r.Handle("GET", "/_matrix/client/v3/account/whoami", authed(d.handleWhoAmI))
	r.Handle("POST", "/_matrix/client/v3/logout", authed(d.handleLogout))
	r.Handle("POST", "/_matrix/client/v3/logout/all", authed(d.handleLogoutAll))
	r.Handle("GET", "/_matrix/client/v3/devices", authed(d.handleDevicesList))
	r.Handle("GET", "/_matrix/client/v3/devices/{deviceID}", authed(d.handleDeviceGet))
	r.Handle("PUT", "/_matrix/client/v3/devices/{deviceID}", authed(d.handleDevicePut))
	r.Handle("DELETE", "/_matrix/client/v3/devices/{deviceID}", authed(d.handleDeviceDelete))

	// Capabilities.
	r.Handle("GET", "/_matrix/client/v1/capabilities", authed(d.handleCapabilities))
	r.Handle("GET", "/_matrix/client/v3/capabilities", authed(d.handleCapabilities))
}
