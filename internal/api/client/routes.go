// Package client implements the Matrix Client-Server API endpoints.
package client

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/auth"

	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/e2ekeys"
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
	Cfg   *config.Config
	Log   *slog.Logger
	Users *users.Service
	// AccountData is the user's own documents — `m.direct` among them — which only the user
	// reads and writes.
	AccountData *users.AccountDataService
	Tokens      *auth.TokenService
	UIA         *auth.UIAService
	Keys        *keys.Service
	Rooms       *rooms.Service
	PushRules   *pushrules.Service
	Presence    *presence.Service
	E2EKeys     *e2ekeys.Service
	Sync        *sync.Service
	DB          *persist.Database
	Version     string
	Started     time.Time
}

// serviceError writes a service-layer failure with the status the protocol prescribes
// and logs anything that is not a protocol error, so a storage fault is visible in the
// log but not in the response.
func (d *Deps) serviceError(w http.ResponseWriter, err error) {
	// A membership change that needs another homeserver is a missing feature, not a
	// permission problem and not a bad request, so it answers 501 wherever it happens:
	// on a membership endpoint, or as part of creating a room with an invite list.
	if errors.Is(err, rooms.ErrRemoteTarget) || errors.Is(err, rooms.ErrRemoteJoinUnavailable) {
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
	// versionedV1 registers an endpoint the specification published after the r0 prefix was
	// retired: the current v3 spelling and the v1 prefix it was actually introduced under. Both
	// are served, because a client that follows the specification's own prefix has to be answered
	// rather than told the endpoint does not exist — `/relations` is one of these, and Complement
	// asks for it under v1.
	versionedV1 := func(method, path string, h http.HandlerFunc) {
		r.Handle(method, "/_matrix/client/v3"+path, authed(h))
		r.Handle(method, "/_matrix/client/v1"+path, authed(h))
	}

	// versionedOpen is the same for the endpoints that carry no token — resolving an alias,
	// reading a profile, and the login and registration dance that happens before a token
	// exists. Requiring a token there would make the first step of using a homeserver
	// impossible, and requiring the current prefix would break the clients that still ask
	// under r0.
	versionedOpen := func(method, path string, h http.HandlerFunc) {
		r.Handle(method, "/_matrix/client/v3"+path, h)
		r.Handle(method, "/_matrix/client/r0"+path, h)
	}

	// Discovery and versions.
	r.Handle("GET", "/_matrix/client/versions", d.handleVersions)
	r.Handle("GET", "/.well-known/matrix/client", d.handleWellKnownClient)
	r.Handle("GET", "/.well-known/matrix/server", d.handleWellKnownServer)
	r.Handle("GET", "/healthz", d.handleHealth)

	// Authentication.
	versionedOpen("GET", "/login", d.handleLoginFlows)
	versionedOpen("POST", "/login", d.handleLogin)

	versionedOpen("GET", "/register", d.handleRegisterGet)
	versionedOpen("POST", "/register", d.handleRegister)
	versionedOpen("GET", "/register/available", d.handleRegisterAvailable)

	// Synapse-compatible shared-secret registration. This endpoint is not part of
	// the Matrix specification; it exists because the Complement test harness
	// depends on it, and it is disabled unless a shared secret is configured.
	r.Handle("GET", "/_synapse/admin/v1/register", d.handleAdminRegisterGet)
	r.Handle("POST", "/_synapse/admin/v1/register", d.handleAdminRegister)

	// Session and device management.
	r.Handle("GET", "/_matrix/client/v1/account/whoami", authed(d.handleWhoAmI))
	versioned("GET", "/account/whoami", d.handleWhoAmI)
	// The two endpoints that end an account's ability to prove itself. Both are guarded by
	// User-Interactive Auth rather than by the token alone: the session that is asking is the
	// thing that may have been stolen.
	versioned("POST", "/account/password", d.handlePasswordChange)
	versioned("POST", "/account/deactivate", d.handleDeactivate)
	versioned("POST", "/logout", d.handleLogout)
	versioned("POST", "/logout/all", d.handleLogoutAll)
	versioned("GET", "/devices", d.handleDevicesList)
	versioned("GET", "/devices/{deviceID}", d.handleDeviceGet)
	versioned("PUT", "/devices/{deviceID}", d.handleDevicePut)
	versioned("DELETE", "/devices/{deviceID}", d.handleDeviceDelete)

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
	// Knocking has both spellings a join has: the room can be named by ID or by alias, and the
	// servers that can be asked for it are named in the query. A client that knocks on a room this
	// server does not hold is asking for the same two-half handshake over federation that a join is,
	// so answering only `/rooms/{roomID}/knock` would leave `/knock/{roomID}` — the spelling the
	// specification puts the `via` and `server_name` parameters on — unreachable.
	versioned("POST", "/knock/{roomIDOrAlias}", d.handleKnock)
	versioned("POST", "/rooms/{roomID}/knock", d.handleKnock)
	versioned("POST", "/rooms/{roomID}/invite", d.handleInvite)
	versioned("POST", "/rooms/{roomID}/kick", d.handleKick)
	// Forgetting a room is a request about the requester's own view, not about the room: the
	// membership event that took them out stays in the history, and the room stops appearing in
	// that user's sync and reads.
	versioned("POST", "/rooms/{roomID}/forget", d.handleRoomForget)
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
	// Read receipts and the markers that move them. A receipt is a statement by one user about
	// how far they have read: it is not an event, and it reaches other sessions of that user as
	// an ephemeral `m.receipt` rather than through the room's timeline.
	versioned("POST", "/rooms/{roomID}/receipt/{receiptType}/{eventID}", d.handleReceipt)
	versioned("PUT", "/rooms/{roomID}/receipt/{receiptType}/{eventID}", d.handleReceipt)
	versioned("POST", "/rooms/{roomID}/read_markers", d.handleReadMarkers)
	versioned("GET", "/rooms/{roomID}/event/{eventID}", d.handleRoomEvent)
	// What relates to an event: a thread's replies, an edit's edits, a reaction's reactions.
	// The optional segments are filters on the same read, so the three spellings are one
	// handler rather than three that could drift apart.
	versionedV1("GET", "/rooms/{roomID}/relations/{eventID}", d.handleRelations)
	versionedV1("GET", "/rooms/{roomID}/relations/{eventID}/{relType}", d.handleRelations)
	versionedV1("GET", "/rooms/{roomID}/relations/{eventID}/{relType}/{eventType}", d.handleRelations)
	// The room's threads: the same index, read across instead of down one event.
	versionedV1("GET", "/rooms/{roomID}/threads", d.handleThreads)
	versioned("PUT", "/rooms/{roomID}/redact/{eventID}/{txnID}", d.handleRedact)

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

	// A user's account data: their own documents, read and written by them alone. `m.direct`
	// lives here, and so does everything the server stores without interpreting.
	// Account data: the user's own scope, and the same store scoped to one room.
	versioned("GET", "/user/{userID}/account_data/{type}", d.handleAccountDataGet)
	versioned("PUT", "/user/{userID}/account_data/{type}", d.handleAccountDataPut)
	versioned("DELETE", "/user/{userID}/account_data/{type}", d.handleAccountDataDelete)
	versioned("GET", "/rooms/{roomID}/account_data/{type}", d.handleRoomAccountDataGet)
	versioned("PUT", "/rooms/{roomID}/account_data/{type}", d.handleRoomAccountDataPut)
	versioned("DELETE", "/rooms/{roomID}/account_data/{type}", d.handleRoomAccountDataDelete)
	// The same store, spelled with the user in front of the room. MSC3391 and Synapse write
	// room account data this way, and clients follow them; a homeserver that answers only the
	// short form tells those clients the endpoint does not exist.
	versioned("GET", "/user/{userID}/rooms/{roomID}/account_data/{type}", d.handleRoomAccountDataGet)
	versioned("PUT", "/user/{userID}/rooms/{roomID}/account_data/{type}", d.handleRoomAccountDataPut)
	versioned("DELETE", "/user/{userID}/rooms/{roomID}/account_data/{type}", d.handleRoomAccountDataDelete)
	// MSC3391's removal endpoints are still published under their unstable prefix, and both
	// spellings answer: a feature that is only reachable under one name is a feature half of
	// the clients do not have. The room form nests under the user, which is how the MSC
	// writes it, and not under /rooms/, which is how the stable endpoints would.
	unstable := "/_matrix/client/unstable/org.matrix.msc3391"
	r.Handle("DELETE", unstable+"/user/{userID}/account_data/{type}", authed(d.handleAccountDataDelete))
	r.Handle("DELETE", unstable+"/user/{userID}/rooms/{roomID}/account_data/{type}", authed(d.handleRoomAccountDataDelete))

	// Room aliases.
	versioned("PUT", "/directory/room/{roomAlias}", d.handleAliasPut)
	versioned("DELETE", "/directory/room/{roomAlias}", d.handleAliasDelete)
	// Resolving an alias is how a client finds a room to join, and joining may be the
	// first thing a new client does: requiring a token here would make it impossible
	// to discover a room to join in the first place.
	versionedOpen("GET", "/directory/room/{roomAlias}", d.handleAliasGet)
	versioned("GET", "/rooms/{roomID}/aliases", d.handleRoomAliases)

	// Capabilities.
	r.Handle("GET", "/_matrix/client/v1/capabilities", authed(d.handleCapabilities))

	// Push rules. The specification's paths under /pushrules form a tree that Go's mux
	// cannot express with a wildcard in the middle of a path, so one route per method
	// catches the subtree and the handler walks the segments.
	versioned("GET", "/pushrules/{rest...}", d.handlePushRulesGet)
	versioned("PUT", "/pushrules/{rest...}", d.handlePushRulesPut)
	versioned("DELETE", "/pushrules/{rest...}", d.handlePushRulesDelete)
	versioned("GET", "/pushrules", d.handlePushRulesGet)

	// Presence. The user ID in the path is a single segment: a colon inside a path
	// segment needs no escaping, so the mux pattern expresses it directly.
	// Device and one-time keys. `/keys/upload` answers to PUT as well as POST: the
	// specification says POST, and clients written against older text send PUT.
	versioned("POST", "/keys/upload", d.handleKeysUpload)
	versioned("PUT", "/keys/upload", d.handleKeysUpload)
	versioned("POST", "/keys/query", d.handleKeysQuery)
	versioned("POST", "/keys/claim", d.handleKeysClaim)
	// The device-list stream as a range query, for a client that wants to know whose keys
	// moved between two tokens without replaying the syncs between them.
	versioned("GET", "/keys/changes", d.handleKeysChanges)

	// Profiles. The reads carry no token: a room's members ask them about each other. The writes are
	// the account's alone, and the account layer also pushes the change out as fresh member events so
	// that a room does not go on showing a name the person stopped using.
	versionedOpen("GET", "/profile/{userID}", d.handleProfileGet)
	versionedOpen("GET", "/profile/{userID}/{keyName}", d.handleProfileFieldGet)
	versioned("PUT", "/profile/{userID}/{keyName}", d.handleProfileFieldPut)
	versioned("DELETE", "/profile/{userID}/{keyName}", d.handleProfileFieldDelete)

	r.Handle("GET", "/_matrix/client/v3/presence/{userID}/status", authed(d.handlePresenceGet))
	r.Handle("PUT", "/_matrix/client/v3/presence/{userID}/status", authed(d.handlePresencePut))

	// The room summary is spelled only under v1 in the specification's own path.
	r.Handle("GET", "/_matrix/client/v1/room_summary/{roomIDOrAlias}", authed(d.handleRoomSummary))

	// The space tree, likewise v1-only. A client drawing a space asks one question rather than
	// peeking into every child, and the server answers only with the rooms this user may see.
	r.Handle("GET", "/_matrix/client/v1/rooms/{roomID}/hierarchy", authed(d.handleRoomHierarchy))
	versioned("GET", "/capabilities", d.handleCapabilities)
}
