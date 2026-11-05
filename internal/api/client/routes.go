// Package client implements the Matrix Client-Server API endpoints.
package client

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/auth"

	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/httpserver"
	"github.com/enimatek/lattice/internal/keys"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/presence"
	"github.com/enimatek/lattice/internal/pushrules"
	"github.com/enimatek/lattice/internal/rooms"
	"github.com/enimatek/lattice/internal/sync"
	"github.com/enimatek/lattice/internal/users"
)

// Deps are the services the Client-Server API needs.
type Deps struct {
	Cfg       *config.Config
	Log       *slog.Logger
	Users     *users.Service
	Tokens    *auth.TokenService
	UIA       *auth.UIAService
	Keys      *keys.Service
	Rooms     *rooms.Service
	PushRules *pushrules.Service
	Presence  *presence.Service
	Sync      *sync.Service
	DB        *persist.Database
	Version   string
	Started   time.Time
}

// serviceError writes a service-layer failure with the status the protocol prescribes
// and logs anything that is not a protocol error, so a storage fault is visible in the
// log but not in the response.
func (d *Deps) serviceError(w http.ResponseWriter, err error) {
	// A membership change that needs another homeserver is a missing feature, not a
	// permission problem and not a bad request, so it answers 501 wherever it happens:
	// on a membership endpoint, or as part of creating a room with an invite list.
	if errors.Is(err, rooms.ErrRemoteTarget) {
		d.Log.Warn("an operation was refused because it needs federation", slog.String("error", err.Error()))
		failure, _ := matrixerr.As(err)
		matrixerr.Write(w, http.StatusNotImplemented, failure)
		return
	}
	if _, isProtocol := matrixerr.As(err); !isProtocol {
		d.Log.Error("a room operation failed", slog.String("error", err.Error()))
	}
	matrixerr.WriteServiceError(w, err)
}

// unknownPath answers a path that sits under a prefix this server does handle without
// claiming to know it. The router produces the same response for a path no route
// matches; a handler that walks a path subtree needs to be able to say the same thing
// when a client asks for a part of that subtree that is not in the specification.
func (d *Deps) unknownPath(w http.ResponseWriter, req *http.Request) {
	matrixerr.Write(w, http.StatusNotFound, matrixerr.Newf(
		matrixerr.CodeUnrecognized,
		"Unrecognized endpoint %s.", req.URL.Path,
	))
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
	versioned("POST", "/rooms/{roomID}/upgrade", d.handleRoomUpgrade)
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
	versioned("GET", "/rooms/{roomID}/messages", d.handleRoomMessages)

	// The room directory: which rooms this server advertises, and who may change that.
	versioned("GET", "/publicRooms", d.handlePublicRooms)
	versioned("POST", "/publicRooms", d.handlePublicRooms)
	versioned("GET", "/publicRooms/{serverName}", d.handlePublicRoomsRemote)
	versioned("GET", "/directory/list/room/{roomID}", d.handleRoomDirectoryVisibility)
	versioned("PUT", "/directory/list/room/{roomID}", d.handleRoomDirectoryVisibility)
	versioned("GET", "/joined_rooms", d.handleJoinedRooms)

	// Synchronisation and the filters that shape it.
	versioned("GET", "/sync", d.handleSync)
	versioned("POST", "/user/{userID}/filter", d.handleFilterCreate)
	versioned("GET", "/user/{userID}/filter/{filterID}", d.handleFilterGet)

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

	// Push rules. The specification's paths under /pushrules form a tree that Go's mux
	// cannot express with a wildcard in the middle of a path, so one route per method
	// catches the subtree and the handler walks the segments.
	r.Handle("GET", "/_matrix/client/v3/pushrules/{rest...}", authed(d.handlePushRulesGet))
	r.Handle("PUT", "/_matrix/client/v3/pushrules/{rest...}", authed(d.handlePushRulesPut))
	r.Handle("DELETE", "/_matrix/client/v3/pushrules/{rest...}", authed(d.handlePushRulesDelete))
	r.Handle("GET", "/_matrix/client/v3/pushrules", authed(d.handlePushRulesGet))

	// Presence. The user ID in the path is a single segment: a colon inside a path
	// segment needs no escaping, so the mux pattern expresses it directly.
	r.Handle("GET", "/_matrix/client/v3/presence/{userID}/status", authed(d.handlePresenceGet))
	r.Handle("PUT", "/_matrix/client/v3/presence/{userID}/status", authed(d.handlePresencePut))

	// The room summary is spelled only under v1 in the specification's own path.
	r.Handle("GET", "/_matrix/client/v1/room_summary/{roomIDOrAlias}", authed(d.handleRoomSummary))
	versioned("GET", "/capabilities", d.handleCapabilities)
}
