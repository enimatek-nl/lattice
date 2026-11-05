// Package client implements the Matrix Client-Server API endpoints.
package client

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/auth"

	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/httpserver"
	"github.com/enimatek/lattice/internal/keys"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/rooms"
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
	Rooms   *rooms.Service
	DB      *persist.Database
	Version string
	Started time.Time
}

// serviceError writes a service-layer failure with the status the protocol prescribes
// and logs anything that is not a protocol error, so a storage fault is visible in the
// log but not in the response.
func (d *Deps) serviceError(w http.ResponseWriter, err error) {
	if _, isProtocol := matrixerr.As(err); !isProtocol {
		d.Log.Error("a room operation failed", slog.String("error", err.Error()))
	}
	matrixerr.WriteServiceError(w, err)
}

// Register installs the Client-Server API routes on r. requireAuth is the
// middleware that enforces a valid access token.
func Register(r *httpserver.Router, d *Deps, requireAuth func(http.Handler) http.Handler) {
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			requireAuth(h).ServeHTTP(w, req)
		}
	}

	// versioned registers an endpoint under the current v3 prefix and the legacy r0
	// prefix. Real clients and Complement still call r0 paths, and a homeserver that
	// answers them with M_UNRECOGNIZED looks broken to software that is following the
	// specification's own historical prefix.
	versioned := func(method, path string, h http.HandlerFunc) {
		r.Handle(method, "/_matrix/client/v3"+path, authed(h))
		r.Handle(method, "/_matrix/client/r0"+path, authed(h))
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

	// Rooms and events.
	versioned("POST", "/createRoom", d.handleCreateRoom)
	versioned("PUT", "/rooms/{roomID}/send/{eventType}/{txnID}", d.handleRoomSend)
	versioned("PUT", "/rooms/{roomID}/state/{eventType}", d.handleRoomState)
	versioned("PUT", "/rooms/{roomID}/state/{eventType}/{stateKey}", d.handleRoomState)
	// The empty state key is addressed with a trailing slash, which Go's mux treats
	// as a subtree pattern; handleRoomState rejects the longer paths it also matches.
	versioned("PUT", "/rooms/{roomID}/state/{eventType}/", d.handleRoomState)

	// Membership. The decision about what a sender may do lives in the authorisation
	// rules, so these endpoints only say which membership change is being requested.
	versioned("POST", "/join/{roomIDOrAlias}", d.handleJoin)
	versioned("POST", "/rooms/{roomID}/join", d.handleJoin)
	versioned("POST", "/rooms/{roomID}/leave", d.handleLeave)
	versioned("POST", "/rooms/{roomID}/knock", d.handleKnock)
	versioned("POST", "/rooms/{roomID}/invite", d.handleInvite)
	versioned("POST", "/rooms/{roomID}/kick", d.handleKick)
	versioned("POST", "/rooms/{roomID}/ban", d.handleBan)
	versioned("POST", "/rooms/{roomID}/unban", d.handleUnban)

	// Reading room state and membership.
	versioned("GET", "/rooms/{roomID}/state", d.handleRoomStateRead)
	versioned("GET", "/rooms/{roomID}/state/{eventType}", d.handleRoomStateSingle)
	versioned("GET", "/rooms/{roomID}/state/{eventType}/{stateKey}", d.handleRoomStateSingle)
	versioned("GET", "/rooms/{roomID}/state/{eventType}/", d.handleRoomStateSingle)
	versioned("GET", "/rooms/{roomID}/members", d.handleMembers)
	versioned("GET", "/rooms/{roomID}/joined_members", d.handleJoinedMembers)
	versioned("GET", "/joined_rooms", d.handleJoinedRooms)

	// Room aliases.
	versioned("PUT", "/directory/room/{roomAlias}", d.handleAliasPut)
	versioned("DELETE", "/directory/room/{roomAlias}", d.handleAliasDelete)
	// Resolving an alias is how a client finds a room to join, and joining may be the
	// first thing a new client does: requiring a token here would make it impossible
	// to discover a room to join in the first place.
	r.Handle("GET", "/_matrix/client/v3/directory/room/{roomAlias}", d.handleAliasGet)
	r.Handle("GET", "/_matrix/client/r0/directory/room/{roomAlias}", d.handleAliasGet)
	versioned("GET", "/rooms/{roomID}/aliases", d.handleRoomAliases)

	// Capabilities.
	r.Handle("GET", "/_matrix/client/v1/capabilities", authed(d.handleCapabilities))
	versioned("GET", "/capabilities", d.handleCapabilities)
}
