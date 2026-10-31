package api

// registerRoutes wires every implemented endpoint onto the mux. Patterns use
// the method-prefixed form of net/http's ServeMux, and the list doubles as the
// registry that the endpoint status tool diffs against the specification
// index.
func (a *App) registerRoutes() {
	a.registerDiscoveryRoutes()
	a.registerAccountRoutes()
	a.registerRoomRoutes()
	a.registerEventRoutes()
	a.registerSyncRoutes()
	a.registerUserDataRoutes()
	a.registerDeviceRoutes()
	a.registerMediaRoutes()
	a.registerKeysRoutes()
	a.registerKeyBackupRoutes()
	a.registerThirdPartyRoutes()
}

// registerDiscoveryRoutes serves the public discovery endpoints.
func (a *App) registerDiscoveryRoutes() {
	a.handle("GET /.well-known/matrix/client", a.handleWellKnownClient)
	a.handle("GET /.well-known/matrix/server", a.handleWellKnownServer)
	a.handle("GET /.well-known/matrix/support", a.handleWellKnownSupport)
	a.handle("GET /_matrix/client/versions", a.handleVersions)
	a.handle("GET /_matrix/client/v1/auth_metadata", a.handleAuthMetadata)
	a.authenticated("GET /_matrix/client/v1/capabilities", a.handleCapabilities)
	a.authenticated("GET /_matrix/client/v3/capabilities", a.handleCapabilities)
}

// registerAccountRoutes serves registration, login and session endpoints.
func (a *App) registerAccountRoutes() {
	a.handle("POST /_matrix/client/v3/register", a.handleRegister)
	// GET asks for the User-Interactive Auth flows without registering anything,
	// which is how clients such as Element work out that registration is
	// possible at all.
	a.handle("GET /_matrix/client/v3/register", a.handleRegisterFlows)
	a.handle("GET /_matrix/client/v1/register/m.login.registration_token/validity", a.handleRegistrationTokenValidity)
	a.handle("GET /_matrix/client/v3/register/available", a.handleRegisterAvailable)
	a.handle("GET /_matrix/client/v3/login", a.handleLoginFlows)
	a.handle("POST /_matrix/client/v3/login", a.handleLogin)
	a.handle("POST /_matrix/client/v3/refresh", a.handleRefresh)
	a.authenticated("POST /_matrix/client/v3/logout", a.handleLogout)
	a.authenticated("POST /_matrix/client/v3/logout/all", a.handleLogoutAll)
	a.authenticated("GET /_matrix/client/v3/account/whoami", a.handleWhoami)
	a.authenticated("POST /_matrix/client/v3/account/password", a.handleChangePassword)
	a.authenticated("POST /_matrix/client/v3/account/deactivate", a.handleDeactivate)

	// Third-party identifiers: Lattice keeps no contact addresses, so the listing
	// is real and empty while everything that would change it refuses with a reason
	// (see internal/service/threepid.go). They are routed because a client that
	// cannot ask shows an error panel where an empty row belongs.
	a.authenticated("GET /_matrix/client/v3/account/3pid", a.handleThreePIDs)
	a.authenticated("POST /_matrix/client/v3/account/3pid", a.handleThreePIDAdd)
	a.authenticated("POST /_matrix/client/v3/account/3pid/add", a.handleThreePIDAdd)
	a.authenticated("POST /_matrix/client/v3/account/3pid/delete", a.handleThreePIDDelete)
	a.authenticated("POST /_matrix/client/v3/account/3pid/bind", a.handleThreePIDBind)
	a.authenticated("POST /_matrix/client/v3/account/3pid/unbind", a.handleThreePIDBind)
	a.authenticated("POST /_matrix/client/v3/account/3pid/email/requestToken", a.handleThreePIDRequestToken)
	a.authenticated("POST /_matrix/client/v3/account/3pid/msisdn/requestToken", a.handleThreePIDRequestToken)
}

// registerRoomRoutes serves room creation, membership and the room directory.
func (a *App) registerRoomRoutes() {
	a.authenticated("POST /_matrix/client/v3/createRoom", a.handleCreateRoom)
	a.authenticated("POST /_matrix/client/v3/join/{roomIdOrAlias}", a.handleJoinRoom)
	a.authenticated("POST /_matrix/client/v3/knock/{roomIdOrAlias}", a.handleKnockRoom)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/join", a.handleJoinRoomByID)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/knock", a.handleKnockRoom)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/leave", a.handleLeaveRoom)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/forget", a.handleForgetRoom)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/invite", a.handleInvite)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/kick", a.handleKick)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/ban", a.handleBan)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/unban", a.handleUnban)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/upgrade", a.handleUpgradeRoom)
	a.authenticated("GET /_matrix/client/v3/joined_rooms", a.handleJoinedRooms)
	a.authenticated("GET /_matrix/client/v3/rooms/{roomId}/aliases", a.handleRoomAliases)
	a.authenticated("GET /_matrix/client/v1/rooms/{roomIdOrAlias}/summary", a.handleRoomSummary)
	a.optionallyAuthenticated("GET /_matrix/client/v3/rooms/{roomId}/members", a.handleMembers)
	a.authenticated("GET /_matrix/client/v3/rooms/{roomId}/joined_members", a.handleJoinedMembers)
	a.handle("GET /_matrix/client/v3/directory/room/{roomAlias}", a.handleGetAlias)
	a.authenticated("PUT /_matrix/client/v3/directory/room/{roomAlias}", a.handleSetAlias)
	a.authenticated("DELETE /_matrix/client/v3/directory/room/{roomAlias}", a.handleDeleteAlias)
	a.handle("GET /_matrix/client/v3/publicRooms", a.handlePublicRooms)
	a.handle("POST /_matrix/client/v3/publicRooms", a.handlePublicRooms)
	a.authenticated("GET /_matrix/client/v3/rooms/{roomId}/directory/visibility", a.handleGetVisibility)
	a.authenticated("PUT /_matrix/client/v3/rooms/{roomId}/directory/visibility", a.handleSetVisibility)
	// The legacy room directory listing endpoints carry the same information.
	a.authenticated("GET /_matrix/client/v3/directory/list/room/{roomId}", a.handleGetVisibility)
	a.authenticated("PUT /_matrix/client/v3/directory/list/room/{roomId}", a.handleSetVisibility)
	a.authenticated("POST /_matrix/client/v3/user_directory/search", a.handleUserDirectorySearch)
}

// registerEventRoutes serves event sending, state, history and relations.
func (a *App) registerEventRoutes() {
	a.authenticated("PUT /_matrix/client/v3/rooms/{roomId}/send/{eventType}/{txnId}", a.handleSendEvent)
	// The state endpoints accept an empty state key, which arrives as a
	// trailing slash, so both the subtree pattern and the plain form are
	// registered.
	a.authenticated("PUT /_matrix/client/v3/rooms/{roomId}/state/{eventType}", a.handleSetRoomState)
	a.authenticated("PUT /_matrix/client/v3/rooms/{roomId}/state/{eventType}/{stateKey}", a.handleSetRoomState)
	a.authenticated("PUT /_matrix/client/v3/rooms/{roomId}/state/{eventType}/", a.handleSetRoomState)
	a.authenticated("PUT /_matrix/client/v3/rooms/{roomId}/redact/{eventId}/{txnId}", a.handleRedact)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/redact/{eventId}", a.handleRedact)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/report/{eventId}", a.handleReportEvent)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/report", a.handleReportRoom)
	a.authenticated("POST /_matrix/client/v3/users/{userId}/report", a.handleReportUser)
	a.authenticated("PUT /_matrix/client/v3/rooms/{roomId}/typing/{userId}", a.handleTyping)
	a.optionallyAuthenticated("GET /_matrix/client/v3/rooms/{roomId}/state", a.handleRoomState)
	a.optionallyAuthenticated("GET /_matrix/client/v3/rooms/{roomId}/state/{eventType}", a.handleRoomStateKey)
	a.optionallyAuthenticated("GET /_matrix/client/v3/rooms/{roomId}/state/{eventType}/{stateKey}", a.handleRoomStateKey)
	a.optionallyAuthenticated("GET /_matrix/client/v3/rooms/{roomId}/state/{eventType}/", a.handleRoomStateKey)
	a.optionallyAuthenticated("GET /_matrix/client/v3/rooms/{roomId}/event/{eventId}", a.handleGetEvent)
	a.optionallyAuthenticated("GET /_matrix/client/v3/rooms/{roomId}/messages", a.handleMessages)
	a.optionallyAuthenticated("GET /_matrix/client/v3/rooms/{roomId}/context/{eventId}", a.handleEventContext)
	a.authenticated("GET /_matrix/client/v3/rooms/{roomId}/relations/{eventId}", a.handleRelations)
	a.authenticated("GET /_matrix/client/v3/rooms/{roomId}/relations/{eventId}/{relType}", a.handleRelations)
	a.authenticated("GET /_matrix/client/v3/rooms/{roomId}/relations/{eventId}/{relType}/{eventType}", a.handleRelations)
	a.authenticated("GET /_matrix/client/v1/rooms/{roomId}/threads", a.handleThreads)
	a.authenticated("GET /_matrix/client/v1/rooms/{roomId}/timestamp_to_event", a.handleTimestampToEvent)
	// Revision v1 serves relations too.
	a.authenticated("GET /_matrix/client/v1/rooms/{roomId}/relations/{eventId}", a.handleRelations)
	a.authenticated("GET /_matrix/client/v1/rooms/{roomId}/relations/{eventId}/{relType}", a.handleRelations)
	a.authenticated("GET /_matrix/client/v1/rooms/{roomId}/relations/{eventId}/{relType}/{eventType}", a.handleRelations)
	a.authenticated("GET /_matrix/client/v1/room_summary/{roomIdOrAlias}", a.handleRoomSummary)
	a.authenticated("POST /_matrix/client/v3/search", a.handleSearch)
	a.authenticated("GET /_matrix/client/v1/rooms/{roomIdOrAlias}/hierarchy", a.handleRoomHierarchy)
	a.authenticated("GET /_matrix/client/v1/mutual_rooms", a.handleMutualRooms)
	a.authenticated("GET /_matrix/client/v1/users/{userId}/rooms", a.handleUserDirectRooms)
}

// registerSyncRoutes serves sync and filters.
func (a *App) registerSyncRoutes() {
	a.authenticated("GET /_matrix/client/v3/sync", a.handleSync)
	a.authenticated("POST /_matrix/client/v3/user/{userId}/filter", a.handleCreateFilter)
	a.authenticated("GET /_matrix/client/v3/user/{userId}/filter/{filterId}", a.handleGetFilter)
	a.authenticated("GET /_matrix/client/v3/user/{userId}/filter/{filterId}/{filterPath}", a.handleGetFilterSection)
	a.authenticated("GET /_matrix/client/v2/user/{userId}/filter/{filterId}", a.handleGetFilter)
}

// registerUserDataRoutes serves account data, tags, profiles, presence, read
// markers and push configuration.
func (a *App) registerUserDataRoutes() {
	a.authenticated("GET /_matrix/client/v3/user/{userId}/account_data/{type}", a.handleGetAccountData)
	a.authenticated("PUT /_matrix/client/v3/user/{userId}/account_data/{type}", a.handleSetAccountData)
	a.authenticated("GET /_matrix/client/v3/user/{userId}/rooms/{roomId}/account_data/{type}", a.handleGetRoomAccountData)
	a.authenticated("PUT /_matrix/client/v3/user/{userId}/rooms/{roomId}/account_data/{type}", a.handleSetRoomAccountData)
	a.authenticated("GET /_matrix/client/v3/user/{userId}/rooms/{roomId}/tags", a.handleRoomTags)
	a.authenticated("PUT /_matrix/client/v3/user/{userId}/rooms/{roomId}/tags/{tag}", a.handleSetRoomTag)
	a.authenticated("DELETE /_matrix/client/v3/user/{userId}/rooms/{roomId}/tags/{tag}", a.handleDeleteRoomTag)
	a.authenticated("POST /_matrix/client/v3/user/{userId}/openid/request_token", a.handleOpenIDToken)
	a.handle("GET /_matrix/client/v3/profile/{userId}", a.handleGetProfile)
	a.handle("GET /_matrix/client/v3/profile/{userId}/{key}", a.handleGetProfileKey)
	a.authenticated("PUT /_matrix/client/v3/profile/{userId}/{key}", a.handleSetProfileKey)
	a.authenticated("DELETE /_matrix/client/v3/profile/{userId}/{key}", a.handleDeleteProfileKey)
	a.handle("GET /_matrix/client/v3/presence/{userId}/status", a.handleGetPresence)
	a.authenticated("PUT /_matrix/client/v3/presence/{userId}/status", a.handleSetPresence)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/receipt/{receiptType}/{eventId}", a.handlePostReceipt)
	a.authenticated("POST /_matrix/client/v3/rooms/{roomId}/read_markers", a.handleReadMarkers)
	a.authenticated("GET /_matrix/client/v1/rooms/{roomIdOrAlias}/receipts", a.handleRoomReceipts)
	a.authenticated("GET /_matrix/client/v3/notifications", a.handleNotifications)
	a.authenticated("GET /_matrix/client/v3/pushrules/", a.handlePushRules)
	a.authenticated("PUT /_matrix/client/v3/pushrules/", a.handlePushRules)
	a.authenticated("DELETE /_matrix/client/v3/pushrules/", a.handlePushRules)
	a.authenticated("POST /_matrix/client/v3/pushers/set", a.handleSetPusher)
	a.authenticated("GET /_matrix/client/v3/pushers", a.handlePushers)
	a.authenticated("DELETE /_matrix/client/v3/pushers/{pushkey}", a.handleDeletePusher)
	a.authenticated("DELETE /_matrix/client/v3/pushers/{pushkey}/{appEncodedPushkey}", a.handleDeletePusher)
}

// registerDeviceRoutes serves device management and the administrative API.
func (a *App) registerDeviceRoutes() {
	a.authenticated("GET /_matrix/client/v3/devices", a.handleListDevices)
	a.authenticated("GET /_matrix/client/v3/devices/{deviceId}", a.handleGetDevice)
	a.authenticated("PUT /_matrix/client/v3/devices/{deviceId}", a.handleUpdateDevice)
	a.authenticated("DELETE /_matrix/client/v3/devices/{deviceId}", a.handleDeleteDevice)
	a.authenticated("POST /_matrix/client/v3/delete_devices", a.handleDeleteDevices)
	a.authenticated("PUT /_matrix/client/v3/sendToDevice/{eventType}/{txnId}", a.handleSendToDevice)
	a.adminOnly("GET /_synapse/admin/v1/whois/{userId}", a.handleWhoIs)
	a.adminOnly("GET /_synapse/admin/v2/users/{userId}", a.handleAdminGetUser)
	a.adminOnly("PUT /_synapse/admin/v2/users/{userId}", a.handleAdminUpdateUser)
	a.adminOnly("GET /_matrix/client/v3/admin/whois/{userId}", a.handleWhoIs)
	a.adminOnly("GET /_matrix/client/v1/admin/{flag}/{userId}", a.handleAdminFlagGet)
	a.adminOnly("PUT /_matrix/client/v1/admin/{flag}/{userId}", a.handleAdminFlagSet)
	a.adminOnly("POST /_synapse/admin/v1/maintenance", a.handleAdminMaintenance)
	a.adminOnly("GET /_matrix/client/v1/admin/reports", a.handleAdminReports)
}

// registerMediaRoutes serves content upload and download when media is enabled.
func (a *App) registerMediaRoutes() {
	if !a.cfg.EnableMedia {
		return
	}
	a.authenticated("POST /_matrix/media/v3/upload", a.handleMediaUpload)
	a.authenticated("PUT /_matrix/media/v3/upload/{serverName}/{mediaId}", a.handleMediaUpload)
	a.authenticated("POST /_matrix/media/v3/create", a.handleMediaCreate)
	a.authenticated("POST /_matrix/media/v3/commit/{serverName}/{mediaId}", a.handleMediaCommit)
	a.handle("GET /_matrix/media/v3/download/{serverName}/{mediaId}", a.handleMediaDownload)
	a.handle("GET /_matrix/media/v3/thumbnail/{serverName}/{mediaId}", a.handleMediaThumbnail)
	a.authenticated("GET /_matrix/client/v1/media/download/{serverName}/{mediaId}", a.handleMediaDownload)
	a.authenticated("GET /_matrix/client/v1/media/thumbnail/{serverName}/{mediaId}", a.handleMediaThumbnail)
	a.handle("GET /_matrix/media/v3/config", a.handleMediaConfig)
	a.handle("GET /_matrix/media/v3/preview_url", a.handleMediaPreviewURL)
	// Authenticated media in revision v1 uses the client API prefix.
	a.authenticated("GET /_matrix/client/v1/media/config", a.handleMediaConfig)
	a.authenticated("GET /_matrix/client/v1/media/preview_url", a.handleMediaPreviewURL)
	a.authenticated("GET /_matrix/client/v1/media/download/{serverName}/{mediaId}/{fileName}", a.handleMediaDownload)
}

// registerKeysRoutes serves the end-to-end key distribution endpoints. The
// server only stores and redistributes key material; the cryptography lives in
// the clients.
func (a *App) registerKeysRoutes() {
	a.authenticated("POST /_matrix/client/v3/keys/upload", a.handleKeysUpload)
	a.authenticated("POST /_matrix/client/v3/keys/query", a.handleKeysQuery)
	a.authenticated("POST /_matrix/client/v3/keys/claim", a.handleKeysClaim)
	a.authenticated("GET /_matrix/client/v3/keys/changes", a.handleKeysChanges)
	// The cross-signing endpoints sit in front of the trust layer: the keys that
	// decide what a client believes are the only key material Lattice verifies
	// itself.
	a.authenticated("POST /_matrix/client/v3/keys/device_signing/upload", a.handleCrossSigningUpload)
	a.authenticated("POST /_matrix/client/v3/keys/signatures/upload", a.handleSignatureUpload)
}

// registerKeyBackupRoutes serves the server-side room key backup. A client
// encrypts the sessions with a key Lattice never sees, so these endpoints store
// and return opaque blobs.
func (a *App) registerKeyBackupRoutes() {
	a.authenticated("GET /_matrix/client/v3/room_keys/version", a.handleKeyBackupVersion)
	// PUT is how current clients create a backup version; POST is the deprecated
	// spelling both are accepted under. Element sends PUT, so without it the key
	// backup setup dies with a 405 and the client says it could not set up keys.
	a.authenticated("PUT /_matrix/client/v3/room_keys/version", a.handleCreateKeyBackup)
	a.authenticated("POST /_matrix/client/v3/room_keys/version", a.handleCreateKeyBackup)
	// The version-scoped forms are deprecated in favour of the version query
	// parameter, but plenty of clients still call them.
	a.authenticated("GET /_matrix/client/v3/room_keys/version/{version}", a.handleKeyBackupVersion)
	a.authenticated("PUT /_matrix/client/v3/room_keys/version/{version}", a.handleUpdateKeyBackup)
	a.authenticated("DELETE /_matrix/client/v3/room_keys/version/{version}", a.handleDeleteKeyBackup)
	a.authenticated("GET /_matrix/client/v3/room_keys/keys", a.handleGetBackupKeys)
	a.authenticated("PUT /_matrix/client/v3/room_keys/keys", a.handleStoreBackupKeys)
	a.authenticated("DELETE /_matrix/client/v3/room_keys/keys", a.handleDeleteBackupKeys)
	a.authenticated("GET /_matrix/client/v3/room_keys/keys/{roomId}", a.handleGetBackupRoom)
	a.authenticated("PUT /_matrix/client/v3/room_keys/keys/{roomId}", a.handleStoreBackupRoom)
	a.authenticated("DELETE /_matrix/client/v3/room_keys/keys/{roomId}", a.handleDeleteBackupRoom)
	a.authenticated("GET /_matrix/client/v3/room_keys/keys/{roomId}/{sessionId}", a.handleGetBackupSession)
	a.authenticated("PUT /_matrix/client/v3/room_keys/keys/{roomId}/{sessionId}", a.handleStoreBackupSession)
	a.authenticated("DELETE /_matrix/client/v3/room_keys/keys/{roomId}/{sessionId}", a.handleDeleteBackupSession)
}

// registerThirdPartyRoutes serves the third-party identifier lookups and the
// VoIP helper. Lattice bridges no networks, so the answers are an empty protocol
// list and honest not-found errors instead of a 404 on every call.
func (a *App) registerThirdPartyRoutes() {
	a.handle("GET /_matrix/client/v3/thirdparty/protocols", a.handleThirdPartyProtocols)
	a.handle("GET /_matrix/client/v3/thirdparty/protocol/{protocol}", a.handleThirdPartyProtocol)
	a.handle("GET /_matrix/client/v3/thirdparty/user", a.handleThirdPartyUser)
	a.handle("GET /_matrix/client/v3/thirdparty/user/{protocol}", a.handleThirdPartyUser)
	a.handle("GET /_matrix/client/v3/thirdparty/location", a.handleThirdPartyLocation)
	a.handle("GET /_matrix/client/v3/thirdparty/location/{protocol}", a.handleThirdPartyLocation)
	a.authenticated("GET /_matrix/client/v3/voip/turnServer", a.handleTurnServer)
}
