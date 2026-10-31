# TODO / working state

Working notes for Lattice (minimal Matrix homeserver in Go). Keep this file
updated: it is the hand-off point after context compaction. Read
[PLAN.md](PLAN.md) for the goal and [ENDPOINTS.md](ENDPOINTS.md) for coverage.

## Verified facts about the existing code (do not re-derive)

* Module: `github.com/enimatek-nl/lattice`, Go 1.26. Build is green:
  `go build ./... && go vet ./...` both clean.
* Layout:
  * `internal/matrix` – identifiers, event types (`matrix.TypeCreate`, …),
    `matrix.Event` (field **`ID`**, not `EventID`), errors (`matrix.Err*`),
    sync structs, `Object`/`Array`/`RawMessage`, `matrix.Encode`,
    `matrix.DecodeContent`, `matrix.MustParseObject`.
  * `internal/persist` – GORM + gormlite SQLite. `NewDatabase(path)`,
    `db.Close()`, `db.GORM()`, `db.Tx`, `IsNotFound`, `IsDuplicate`. Repos are
    fields named `XRepo` (`db.EventRepo`, `db.SessionRepo`, …), newest additions:
    `DeviceKeyRepo` (device identity keys as opaque JSON), `OneTimeKeyRepo`
    (prekeys plus fallback keys, `Claim` is a conditional update so a key is
    handed out once) and `DeviceListRepo` (the device list change log).
    `persist.Room` uses field **`ID`** (not `RoomID`); `persist.Event.Matrix()`
    returns a value (not a pointer).
  * `internal/auth` – JWT (HS256) tokens with a `jti` so two sessions of the
    same device never collide; `auth.NewStore(tokens, db)`; middleware
    `Require`/`Optional`/`RequireAdmin`; context getters `auth.GetUserID`,
    `GetDeviceID`, `GetToken`, `IsAdmin`, `auth.WriteError`.
  * `internal/realtime` – hub that wakes long-polling syncs.
  * `internal/service` – business layer, constructor
    `service.New(db, serverName, tokens, opts...)`; in-package it uses
    `s.serverName`, `s.db.XRepo`; exported helpers: `Sync`, `History`,
    `CreateRoom`, `JoinRoom`, `InviteRoom`, `KickRoom`, `BanRoom`, `KnockRoom`,
    `LeaveRoom`, `ForgetRoom`, `Members`, `JoinedMembers`, `SendRoomEvent`,
    `SetRoomState`, `RedactEvent`, `RoomState`, `StateContent`, `StateEvent`,
    `RoomSummary`, `PublicRooms`, `CreateAlias`, `DeleteAlias`, `RoomServers`,
    `Search`/`SearchJSON`, `RoomHierarchy`, `MutualRooms`/`DirectRooms`,
    `RoomReceipts`, `RunMaintenance`/`StartMaintenance`,
    `GuestsCanRead`, `PushRules`/`SetPushRule`/…, `Notifications`, `MarkDirectRoom`,
    `OpenIDToken`, `WhoIs`, `SetAccountFlag`/`AccountFlags` (flags `lock`, `suspend`),
    `RegistrationFlows`/`RegistrationChallenge`/`RegistrationTokenSupported`
    (User-Interactive Auth for `/register`, sessions in memory, stages tracked
    in `internal/service/uia.go`), `HomeServerConfiguration`/`PublicBaseURL`, and
    the end-to-end key API in `internal/service/keys.go`: `UploadKeys`,
    `QueryKeys`, `ClaimKeys`, `DeviceListChanges`, `OneTimeKeyCounts`,
    `DropDeviceKeys`.
  * `internal/api` – `api.New(svc, authStore, api.Config{...})`,
    `app.Handler()`, `app.Registered()` (route patterns, used by the coverage
    tool). Routes live in `internal/api/routes.go`. A catch-all `"/"` handler
    answers unrouted paths with `404 M_UNRECOGNIZED` (and restores the 405 +
    `Allow` that net/http gives up when a catch-all is registered, by matching
    the request path against `app.Registered()`).
  * `cmd/server` – flags: `-addr -server-name -base-url -db -jwt-secret -token-ttl
    -max-upload-size -media-reservation-window -allow-guests -disable-registration
    -registration-secret -cors-origin -support-email -support-matrix-id -media
    -maintenance-interval -version`.
  * `cmd/endpoints` – coverage report: `go run ./cmd/endpoints -out ENDPOINTS.md`.
  * `tests/compat` – black-box compatibility suite (`harness.go` + tests).
* Spec/behaviour details already handled (don't "fix" them again):
  * `GET /rooms/{roomId}/members` defaults to `membership=join`; `membership=*`
    means all.
  * Default `invite` power level is 0 (any joined member may invite).
  * Power-level changes require sender level ≥ max(old, new) of each user
    (`internal/service/stateguards.go`).
  * `m.room.create` cannot be replaced; `m.room.member` cannot be written via
    `/state` (stateguards).
  * `matrix.SyncResponse` fields: `device_one_time_keys_count` is a map keyed by
    algorithm and is present on every authenticated sync; `device_lists` is only
    sent for incremental syncs, is filtered to users who share a room with the
    viewer, and its `left` list is always empty (a removed device is reported as a
    `change`; the spec reserves `left` for users who stopped sharing a room, which
    Lattice derives from membership state).
  * `POST /logout/all` keeps the calling token valid (spec).
  * Sync wake-up: an outstanding invitation is membership state, so it is
    reported on every sync, but it is **not** new data — only an invitation whose
    event was written after the token may end a long poll. Treating every
    invitation as new makes the long poll answer instantly, forever, and the
    client (Element) turns that into one `/keys/query` per pass.
  * A device list change is **not reported back to the device that caused it**
    (`DeviceListChanges` takes the asking device id). Uploading an identity key
    records a change so the user's other devices and the people who share a room
    re-query; echoed to the uploader, the client fetches its own keys and — if it
    re-uploads keys after fetching them — queries keys in a loop. Changes with no
    device behind them (a cross-signing upload) are still sent to that user's other
    devices, which do want the new keys.
  * A long poll whose request context finished is answered with **nothing at all**:
    no body, no log line. Element restarts its sync loop constantly (crypto setup,
    a filter change, a tab closing), and every abandoned poll logged as
    `M_UNKNOWN: internal server error: context canceled` hides the faults that
    matter.
  * A cross-signing key's `usage` is accepted under **either spelling in use**:
    `self-signing`/`user-signing` as the specification writes them, and
    `self_signing`/`user_signing` as the Rust crypto stack Element ships sends
    them. Refusing a correctly signed key over a hyphen leaves the user unable to
    set up encryption. The object is stored exactly as it arrived, because
    rewriting the field would change what its own signature covers.
  * Media uploads have **two flows and both must be complete**. Element and
    matrix-js-sdk use the async one for every upload, including the room avatar:
    `POST /create` mints an id, `PUT /upload/{server}/{id}` sends the bytes to the
    `upload_url` from that response, and `POST /commit/{server}/{id}` says they are
    all there - with an optional `timeout` for a client that arrives first. A
    missing commit endpoint is a silent failure in the client: the upload looks
    fine, the `mxc://` URI is used, and the avatar never appears. A reservation is
    a media row with no bytes; it is not served (404, not an empty 200 that renders
    as a broken image) and maintenance drops it once the window the create response
    advertised has passed (`-media-reservation-window`, 24 h by default). There is
    no "uploaded" flag to set and no schema column for one: adding a boolean would
    have made every existing media row unreadable.
  * Endpoints the server cannot really serve are still **routed, and refuse with a
    reason**. The third-party identifiers (email, phone) are the clear case: Lattice
    has no identity server integration and stores no addresses, so `GET
    /account/3pid` answers a real empty list and everything that would change it
    answers `M_FORBIDDEN`/`M_NOT_FOUND` with the reason in the body. An unrouted
    path is not a smaller problem - Element shows the user "Unable to load email
    addresses and phone numbers", where an empty settings row belongs.
  * Answers that are **correct and should not be "fixed"**, collected because they
    look like failures in a browser's network panel: `GET /room_keys/version` is
    `404 M_NOT_FOUND` until the user has a backup; a `POST
    /keys/device_signing/upload` that would **replace an identity that already
    exists** is a `401 M_UNAUTHORIZED` with the flow list, which is the
    interactive auth exchange the client answers with its password (a request
    publishing the *first* set of keys, or repeating the stored set, is accepted
    without that exchange: since v1.11 the specification asks for the exchange
    only when an existing key would change, and Element creates a new account's
    identity expecting no challenge); and `GET /v1/auth_metadata` is `404`
    because this server does not use OAuth 2.0. Element logs the ones that stay
    and moves on.
  * Two state events are **fixed once written**: `m.room.create` (it is the root of
    the room and carries its version) and `m.room.encryption`. The specification
    never describes re-encrypting a room, and a room whose algorithm changes cannot
    decrypt its own history - the clients in use treat the first `m.room.encryption`
    as final, so accepting a second one leaves them sending under settings nobody
    agreed to. A retry of the *same* transaction id still replays the original event,
    because the transaction store is consulted before these guards run.
  * A room with a tombstone **rejects events** (`RequireSend`, which covers messages,
    state and redactions) even for its creator, and cannot be upgraded a second time.
    Membership goes through its own endpoints, so people can still leave a room that
    has been closed. The upgrade also raises `events_default` and `invite` in the old
    room to `max(50, users_default + 1)`, which is what the specification asks for
    ("the power levels in the old room should also be modified to prevent sending of
    events and inviting new users") and makes the lock visible in state rather than
    only in this server's behaviour.
  * Stripped state carries the **membership events of the inviter and the invitee
    only**. The specification names exactly those two; carrying every member hands a
    stranger the guest list the moment an invitation lands. `m.room.create`, the join
    rules and the aesthetics (name, topic, avatar, canonical alias) and
    `m.room.encryption` are all still included - without `m.room.create` a client
    assumes room version 1.
  * `POST /rooms/{id}/upgrade` **requires** `new_version` (the spec marks it
    required; defaulting it to the room's own version would make a copy nobody asked
    for). Not routed and not planned: the `PUT /state/{type}/{stateKey}/{txnId}`
    transaction form, because Go's mux cleans request paths and an empty state key
    (`/state/m.room.name//txn`) cannot survive that; matrix-js-sdk uses the
    two-segment form for empty state keys, which is routed.
  * `m.room_versions` in `/v1/capabilities` uses the words `stable`/`unstable`
    as the spec asks, not the `"1"` some early servers sent: a client builds its
    list of stable versions from those values, and an empty list makes it demand
    an upgrade of every room whose version differs from the default (the
    "room version 1 … marked as unstable" dialog). Default `11`, versions 1-11
    advertised; Lattice applies one set of state and redaction rules to all of
    them rather than varying them per version.
  * `m.room.tombstone` carries `replacement_room` (a client follows that field,
    `predecessor` alone is not enough), and the successor's `m.room.create`
    carries `predecessor`.
  * Stripped state (the `invite_state` of a sync, and the `knock` variants) must
    include `m.room.create`: it holds the room version, and a client that cannot
    find it assumes version 1.
  * Duplicate alias → 409 `M_ALIAS_EXISTS`; alias creator must be joined.
  * `GET /login` advertises `m.login.password` only; no SSO, no 3PID, no OAuth
    (`/_matrix/client/v1/auth_metadata` → 404 on purpose).
  * Every state event carries `state_key` on the wire, **including the ones whose
    key is the empty string** (m.room.name, m.room.topic, m.room.create,
    m.room.power_levels, …). A client decides whether an event is state by looking
    for that field, so omitting it makes a room rename invisible, and the client
    falls back to room version 1 because it cannot find m.room.create. Since the
    empty state key is indistinguishable from a timeline event in the row,
    `Event.IsState` records it explicitly; `NewDatabase` backfills the marker for
    databases written before it existed (a superseded empty-key state event cannot
    be recognised any more, which only means an old room name arrives as a plain
    timeline event).
  * End-to-end device keys are **opaque**: the JSON a client uploads is the JSON
    another client gets back, and no signature is verified server-side (the
    clients do that). Consequences that are deliberate: the algorithm of a key is
    the part before the `:` of its key id; a key uploaded under the field name
    `key` is rewritten to the algorithm field; an ordinary one-time key is handed
    out once (`Claim` is a conditional update, so concurrent claims cannot both
    win) while a fallback key (MSC3288) is handed out repeatedly and never counted
    in `one_time_key_counts`; a device re-uploading its keys does not lose the ones
    that were never claimed; keys and prekeys are deleted with their device and on
    account deactivation.
  * Cross-signing keys are the **one piece of key material Lattice verifies**, and
    it does not read the spec wording off a client's bytes: `internal/matrix/
    canonical.go` builds canonical JSON (Go's `json.Marshal` is not canonical — it
    escapes `<`, `>` and `&`, sorts nothing, and turns numbers into float64), and
    the master key must sign itself while the self-signing and user-signing keys
    must be signed by the master key, over the object without `signatures` and
    `unsigned`. A key id must match its key material, the usage must match the
    field it arrived in, and `user_id` must be the requester. Uploading a
    different master key **replaces the set**: answering the interactive auth
    challenge is the authorisation, because no client can send a separate reset
    flag and a refusal there is a dead end for the user (Element's "I can't
    verify" path replaces the keys). The replaced keys' ids are deleted along with
    the signatures they made on the user's devices, and a signature attributed to
    one of the requester's own keys that Lattice has never been given is refused
    with `M_INVALID_PARAM`. The endpoint sits behind a `m.login.password`
    interactive auth stage. Verified
    keys are reported by `POST /keys/query` in the same map as the device keys,
    named by their key id, which is where clients look for them, and an upload is
    a device list change so the user's other devices pick it up.
  * `POST /keys/signatures/upload` merges signature blocks into the stored device
    or cross-signing key. A signature made with a key Lattice holds is verified;
    one made by a key it does not have is stored as it came, because the clients
    that read it hold the key. An entity nobody has is skipped rather than an
    error: signing a device that was deleted is not a client bug.
  * Key backup (`/room_keys/*`) stores opaque encrypted sessions. Versions count
    up from 1 per user; writes are only accepted for the latest version
    (**400 `M_WRONG_KEY_VERSION`** otherwise), an unknown version is a 404
    `M_NOT_FOUND`, deleting a version takes its sessions with it, and the three
    granularities (all rooms, one room, one session) each have their own body
    shape. `m.room.encryption`, the backup auth_data and the sessions are never
    interpreted.
  * `POST /register` with no `auth` dict (and `GET /register`) answers **401
    `M_UNAUTHORIZED`** with `flows`/`params`/`session`/`completed` — that is the
    only way clients such as Element discover that registration is possible; a
    400 here shows up in the browser as "Unable to query for supported
    registration methods". The advertised stage is `m.login.dummy`, or
    `m.login.registration_token` when `-registration-secret` is set. Guest
    registration (`?kind=guest`, `kind: "guest"`) skips the exchange, and an
    unknown `session` is replaced rather than refused. `matrix.Error` carries the
    challenge payload in its `Extra` field, which `MarshalJSON` merges into the
    body (`auth.WriteError` encodes the error itself now).
  * `/.well-known/matrix/client` and the `well_known` field of login/register
    nest the URL: `{"m.homeserver":{"base_url":"…"}}`. A bare string breaks
    discovery. The URL comes from `-base-url` (`service.WithPublicBaseURL`),
    defaulting to `https://<-server-name>`.
  * Media is stored inline in SQLite; thumbnails are PNG via stdlib imaging.
* Go ServeMux gotchas already worked around:
  * Empty state keys arrive as a trailing `/`; both `…/state/{eventType}` and
    `…/state/{eventType}/` patterns are registered.
  * Push rules are served by one subtree handler per method
    (`/_matrix/client/v3/pushrules/`), parsing segments and skipping the
    deprecated `global` scope.
  * Wildcard names at the same path position must be identical.
* GORM gotcha: every query chain that calls `Count` needs
  `db.Model(&X{})` (fixed in `NotificationRepoImpl.List`).
* GORM gotcha: `clause.OnConflict` names **columns**, and GORM snake-cases field
  names whatever the `json` tag says: `Pusher.PushKey` → `push_key`,
  `Pusher.AppName` → `app_name`, `SentTS` → `sent_ts`,
  `ReadMarker.FullyReadEventID` → `fully_read_event_id`. `PusherRepo` used
  `pushkey`/`app_display_name`, which made `POST /pushers/set` a 500.
* Repo upsert convention: `db.Clauses(clause.OnConflict{Columns: …, DoUpdates:
  clause.AssignmentColumns(…)}).Create(…)`. The `Where(…).Assign(x).
  FirstOrCreate(x)` form misbehaves (the struct leaks into the WHERE) and is
  gone from `PresenceRepo`, `ReceiptRepo` and `ReadMarkerRepo`.
* `ReadMarkerRepo.Get` hands back a zero row when nothing is stored, so
  `service.SetReadMarker` sets `marker.UserID`/`marker.RoomID` itself.
* `service.SetReadMarker` also writes the marker as `m.fully_read` **room
  account data**, which is how sync delivers it.
* `persist.Notification` keeps the action list of the rule that fired
  (`Actions`) and a `Highlighted` flag: `GET /notifications` replays the actions
  verbatim and sync fills `unread_notifications.highlight_count`.
* A read receipt never moves backwards — `PostReceipt` keeps the newer position
  and timestamp and leaves the stored row alone.
* `GET /profile/{userId}/{key}` is 404 when the key was never set or was
  deleted; the whole-profile read stays an empty object.
* `api.requireSelf(w, r)` gates the personal-data writes (profile write/delete,
  presence write, OpenID token request) on `{userId} == auth.GetUserID(r)`.
* Media reservations: `POST /media/v3/create` inserts a placeholder row (no
  bytes); `PUT /media/v3/upload/{serverName}/{mediaId}` fills it in place
  (`MediaRepo.Save`), 409 once content exists, 403 when another user owns the id.
* The compat harness exposes `Response.Headers` and only logs “not JSON” for
  bodies that claim to be JSON, so binary media reads stay quiet.
* Lint baseline: `golangci-lint run ./…` reports 12 pre-existing findings
  (unused `api.rawQueryObject`, `service.sessionTTL`, `service.userSummary`,
  `service.roomMembersForSync`; staticcheck style in `cmd/endpoints` and
  `internal/api/directory.go`; two ineffassigns; one errcheck in the harness).
  Nothing new was introduced; cleaning them up is a separate decision.
* SQLite `:memory:` gives every pooled connection its own database;
  `NewDatabase` rewrites it to `file::memory:?cache=shared` and pins one
  connection. The compat harness therefore uses a file in `t.TempDir()` so
  tests stay isolated from each other.
* Compat harness silences server logs unless `LATTICE_TEST_LOGS=1`.
* `SyncSince(session, timeoutMS)` is the harness helper for incremental syncs.
* Lint baseline after the history chunk: 11 findings (unused
  `api.rawQueryObject`, `service.sessionTTL`, `service.userSummary`,
  `service.roomMembersForSync`; staticcheck style in `cmd/endpoints` and
  `internal/api/directory.go`; one ineffassign; one errcheck in the harness).

### History, relations, threads, typing

* `persist.Event` denormalises relations: `RelatesTo` (target event id) and
  `RelType`; `service.relationOf` (in `internal/service/events.go`) fills both
  from `m.relates_to`, falling back to `m.in_reply_to` → `m.in_reply_to`, so
  plain replies never pollute annotation/thread listings. `EventRepo.Relations`
  filters on those columns; `EventRepo.Threads`/`ThreadsFor` filter
  `rel_type = 'm.thread'` (annotations and edits are excluded by construction).
* `/relations` accepts the filters as path segments **and** as `rel_type` /
  `event_type` query parameters; the query wins when both are present.
  Ordering is `stream_pos asc` (oldest first) and `next_batch` only appears when
  the page filled up.
* `/v1/rooms/{roomId}/threads` now returns spec-shaped entries: the stored root
  event inlined plus `count` (replies only), `num_events` (root included) and
  `latest_event`. `service.Thread` has a `MarshalJSON` that does the inlining,
  so do not "simplify" it into plain fields. Params: `include=all|mine`,
  `relationship=all|latest`, `from`, `limit`; anything else is 400. Ordering is
  most recently active first and the `next_batch` token is the stream position
  of the last thread's newest event.
* A thread root in a sync timeline (and in `/messages`, `/context`) carries
  `unsigned["m.thread"] = {num_events, latest_event, current_user_participated}`
  — built by `service.annotateThreads`, which needs `ThreadsFor` and is called
  with the *requesting* user for the participation flag. The centre event of
  `/context` and the reply of `GET /event` are **not** annotated.
* `service.EventByTimestamp` validates `dir` (`f`/`b`, default `f`) and `ts > 0`;
  `EventRepo.HistoryByTimestamp` compares `origin_server_ts`, so the oldest
  event of a room is its `m.room.create` — a room with no messages still answers
  `timestamp_to_event` with 200.
* `POST /rooms/{roomId}/report/{eventId}` goes through `service.ReportEvent`:
  requires `RequireJoined`, 404 for an unknown event, 400 when `score > 0`
  (`reason` must be a string, `decodeBody` gives 400). The report itself is only
  logged.
* Typing: `persist.Typing` gained `ExpiresTS` (column `expires_ts`), set from
  the client `timeout` (default 30s) and cleared when `typing` is false.
  `TypingRepo.ActiveUserIDs(roomID, except, nowMS)` filters expired rows — the
  third argument is new. `Recent` deliberately does **not** filter, so an
  expired state still produces an `m.typing` event saying "nobody is typing".
  `typingEvents` now emits `"typing": len(user_ids) > 0`, and the requester's
  own id is never in the list. `handleTyping` ignores the path `{userId}` in
  favour of the token user but 403s when they differ.

### Paths and harness helpers that tests keep getting wrong

* Room account data: `GET/PUT /_matrix/client/v3/user/{userId}/rooms/{roomId}/account_data/{type}`
  (there is **no** `/rooms/{roomId}/account_data/...` route).
* Room summary: `GET /_matrix/client/v1/rooms/{roomId}/summary` (v1, not v3);
  the legacy `GET /_matrix/client/v1/room_summary/{roomIdOrAlias}` also exists.
* Media: uploads and unauthenticated reads are `/_matrix/media/v3/…`;
  authenticated reads are `/_matrix/client/v1/media/{download,thumbnail|url}/…`
  and only that variant accepts a trailing `/{fileName}`.
* Aliases in a path must be escaped in tests: `/join/%23wonderland:test.server`
  (a bare `#` is eaten by the URL parser and produces a mux 404).
* Reusable helpers (all in `tests/compat`): `sendTyped`, `eventsOf`,
  `chunkBodies`, `oldestEvent`, `eventOfType`, `testPNG`, `pngBounds`,
  `mustMediaID`, `actionsOf(...).notifies()/.hasTweak()`, `lenList`,
  `lenPushers`, `toDeviceOf`, `typingEvents`, `decodeReports`,
  `server.DoRaw(method, path, token, contentType, payload)`,
  `server.MustSync(session, query)`, `Response.Int/String/ErrCode`.
* `server.Sync` and `server.SyncSince` do **not** update `session.NextBatch`.
  Assign it by hand (`session.NextBatch = firstSync.NextBatch`) before using
  `SyncSince`, otherwise the "incremental" sync is an initial one and silently
  ignores the position you meant to test.
* `Response` has no `Sync()` helper: decode with
  `json.Unmarshal(resp.Raw, &matrix.SyncResponse{})`.
* Time-dependent behaviour is tested with `newClockServer(t, clock)` and the
  mutex-guarded `clock` in `maintenance_test.go`; give that server's token
  service a long TTL because session expiry follows the injected service clock.

### Read markers and push rule defaults

* `POST /rooms/{roomId}/read_markers` accepts the **spec** spellings
  `m.fully_read` and `m.read`; the older `fully_read`/`read_receipt`/
  `private_read` spellings still work (`handleReadMarkers` merges them).
* Every new account is seeded with `.m.rule.message`
  (`event_match content.msgtype "m.*"` → notify + sound default) and
  `.m.rule.encrypted_room_one_to_one`. Consequence: **every** message from
  another member creates a notification and raises `notification_count`; only
  mentions add `set_tweak: highlight` (so `highlight_count` stays about
  mentions). Rules are seeded at registration, so existing accounts keep the
  old set — a rule-version migration is still open.
* `GET /notifications` in lattice returns the list oldest first. The spec's
  wording about the ordering is ambiguous, so the tests are deliberately
  order-agnostic (they index the entries by `event.event_id`).
* In a room with exactly two members `.m.rule.room_one_to_one` fires first
  (`room_member_count == 2`), which is why small test rooms notify for plain
  messages even before `.m.rule.message` was added.

### Send-to-device, reports and the sync room summary

* `PUT /_matrix/client/v3/sendToDevice/{eventType}/{txnId}` is served by
  `service.SendToDevice` and stored in `persist.ToDevice`
  (`persist/to_device… = internal/persist/todevice.go`, repo wired as
  `db.ToDeviceRepo`, model registered in `persist.models()`).
  Unique key is `(sender_id, txn_id, target_id, target_device)` and the
  on-conflict update deliberately **excludes `stream_pos` and `ts`**: a retried
  transaction must not move the message into the future of devices that already
  received it.
* Delivery is positional: `ToDeviceRepo.Pending(userID, deviceID, since, 100)`
  selects `target_device = '*' OR target_device = <device>` rows after the sync
  token, and `buildSync` puts them in `response.ToDevice`. There is no
  per-device watermark yet, so nothing prunes old rows
  (`ToDeviceRepo.Prune` exists but is unused) and a brand-new device does not
  receive messages sent before its first token.
* Remote or unknown recipients are skipped silently; `m.room.*` event types are
  rejected with 400; an empty `messages` map is a successful no-op.
* Reports: `POST /rooms/{roomId}/report/{eventId}` → `service.ReportEvent`
  (member, 404 unknown event, 400 `score > 0`);
  `POST /rooms/{roomId}/report` → `service.ReportRoom` (room must exist and be
  readable); `POST /users/{userId}/report` → `service.ReportUser` (404 unknown
  user, 400 when reporting yourself). All three only log — moderation is out of
  scope — and all answer `{}`.
* The sync room summary lives directly on `matrix.JoinedRoom`:
  `m.heroes` (`Heroes []string`), `m.joined_member_count` and
  `m.invited_member_count` (both `*int64`, so a zero count still serialises).
  `service.syncRoomSummary` (in `syncstate.go`) names up to `syncHeroCount = 5`
  joined members excluding the requester — the choice of *which* five is
  arbitrary today.

### Search, spaces, mutual rooms and housekeeping

* Search lives in `internal/service/search.go` (flat body) and
  `internal/service/searchjson.go` (`search_categories` body). Matching is an
  escaped `LIKE` over the serialised event content (`persist.SearchQuery`,
  `EventRepo.Search`): terms are AND-ed, results come newest first, and only
  rooms the requester has joined or is invited to are searched. An explicit
  `filter.rooms` entry is checked room by room, so an unreadable room is a 403
  instead of a silent gap.
* `event_fields` projection returns `matrix.Object`, **not** `matrix.Event`: a
  struct would serialise the fields that were dropped as `null`/`0`. The same
  helper (`eventObject`) feeds both search shapes.
* The JSON form understands `search_key.term` (string or list),
  `search_key.json` (`query` + `filter`), `menu.event_types|rooms|senders`,
  `groupings` (`session_id` or `room_id`; a thread's `session_id` is its root
  event id) and `limits.limit`, and answers with
  `search_categories.room_events.{limits,order_by,groupings,hits[].results_per_group}`.
  `include_state` attaches the state events of each matching room.
* `GET /v1/rooms/{roomIdOrAlias}/hierarchy` (`service.RoomHierarchy`) walks
  `m.space.child` breadth first: default `max_depth` 1 (a `max_depth=0` is read
  as the default), `next_batch` is a plain integer offset into the walk, and an
  unknown, remote or unreadable child sets `incomplete_chain` instead of
  breaking the walk. `children_state` holds the **room's own `m.space.child`
  events**, not its name/topic/create event.
* `POST /createRoom` accepts a top-level `type` (`m.space`) and folds it into
  the creation content; `POST /rooms/{id}/invite` marks `m.direct` for the
  invitees, and `MarkDirectRoom` tolerates a missing `m.direct` account data
  (`AccountData` answers `M_NOT_FOUND` for a fresh account).
* `GET /v1/mutual_rooms?user_id=&room_id=` excludes rooms either user tagged in
  `m.direct`; `room_id` is an authorization anchor, so it 403s when the
  requester cannot read it or the target is not joined. The sibling
  `GET /v1/users/{userId}/rooms` lists the **caller's own** `m.direct` rooms
  (403 for anybody else, 404 for an unknown user).
* Housekeeping: `service.RunMaintenance` prunes expired to-device messages
  (24h), remembered transaction ids (24h) and typing indicators nobody
  refreshed (30s grace). `StartMaintenance(ctx, period)` returns one stop
  function that cancels the loop **and** waits for it; a non-positive period
  switches it off. `cmd/server` wires it with `-maintenance-interval`, and
  `POST /_synapse/admin/v1/maintenance` (adminOnly) triggers a pass by hand —
  the first registered account is the administrator.
* Push rules: `SetPushRule` takes a `service.PushRulePlacement{Before, After}`.
  A new rule without a placement gets `max(positions)+1`; a move renumbers the
  whole kind from 0 via `PushRuleRepo.Order`. Giving both `before` and `after`,
  or naming an anchor that is not a rule of that kind, is a 400.
* GORM gotcha: `PushRuleRepo.Get` and `PusherRepo.Get` return a **non-nil zero
  row** together with `ErrRecordNotFound`. Branch on the error
  (`persist.IsNotFound(err)`), never on `x == nil`.
* Deletes that free a unique index must be hard deletes (`Unscoped()`): push
  rules, pushers, room memberships (forget), aliases, profile fields, devices,
  sessions, key backup versions and their stored sessions, and cross-signing
  keys. A soft delete keeps the indexed values, so the next
  `ON CONFLICT` upsert updates the invisible row and the record stays missing -
  or the next insert of that value fails with a duplicate key, which is what a
  client recreating a deleted key backup hit.
* `tests/compat/maintenance_test.go` shows how to test time-dependent
  behaviour: `newClockServer` starts a server with a mutex-protected clock the
  test advances, and the token service needs a long TTL there because session
  expiry follows the service clock.

### Abuse reports, administration and long polling

* Abuse reports are stored, not just logged: `persist.Report` with kinds
  `persist.ReportKindEvent|Room|User`, `ReportRepo.Add/List/Count/Get` and
  `persist.ReportFilter{Kind,RoomID,EventID,UserID,Reporter,Limit,Offset}`.
  Listings come out newest first (`created_ts DESC, id DESC`).
* An event report copies the reported event's `content` into
  `Report.EventContent` at report time, so redacting the event afterwards does
  not hide what was reported. `TestAdminReports` pins that.
* `service.Reports(ctx, ReportQuery)` returns `([]ReportRecord, total, error)`;
  `total` ignores the limit, an unknown `kind` is a 400, and the limit is
  clamped to 100 (maximum 1000).
* Administrative routes live under `/_matrix/client/v1/admin/*` and
  `/_synapse/admin/*` and are registered with `a.adminOnly(...)`. **The first
  account registered on a server is its administrator**, so tests that need an
  admin register `root` before the actors. `GET /_matrix/client/v1/admin/reports`
  and `POST /_synapse/admin/v1/maintenance` are the current ones; the `{flag}`
  admin routes cannot collide with `/admin/reports` because the mux matches whole
  patterns.
* `POST /rooms/{roomId}/invite` rejects a `third_party_signed` body with
  `M_INVALID_PARAM` — Lattice does not speak to identity servers, so accepting
  the request and inviting nobody would lie.
* Long polling is pinned by `TestSyncLongPollTimeout`: an incremental sync with
  nothing new holds the connection for the whole `timeout`, `timeout=0` answers
  at once, and an account data change wakes a waiting sync. `service.Sync` only
  returns early when `since == 0`, something changed, or the timeout is zero,
  and it keeps a 500ms fallback tick in case a hub notification is dropped.

## Done and verified

* Core layer: identifiers, errors, persistence + repos, realtime hub, JWT auth
  middleware, service layer (rooms, membership, events, state, power levels,
  sync + filters, history/context/relations/threads, receipts/read markers,
  typing, presence, push rules/pushers/notifications, media, devices,
  profiles, user directory, OpenID tokens, admin flags, room upgrades,
  aliases/directory).
* HTTP layer for 110 of the 234 indexed endpoints
  (`go run ./cmd/endpoints` → "110/234 … (17 extra routes)"). The index also
  counts federation, appservice and identity endpoints, which are out of scope.
* Manual smoke run (`/tmp/smoke.sh` against a real server): register, login,
  createRoom + alias, invite, join, send (+txn idempotency), state
  get/set with empty key, sync, messages, members, joined_members, context,
  redact, notifications, publicRooms, summary, pushrules, whoami.
* `go test ./tests/compat/` passes (also with `-race`). Covered so far:
  register/available/login/whoami/logout/logout-all/refresh/devices,
  createRoom (public+private, state, aliases, summary, publicRooms),
  join by alias/ID, invite+sync invite, private room refusal, knock,
  kick/ban/unban, leave/forget + membership filters, power levels,
  send + txn replay + per-sender txn scope, state endpoints incl. guards,
  history pagination (f/b, tokens, bad `dir` → 400), redaction (full
  `redacted_because`), initial sync, incremental sync (no state resend, stable
  `next_batch`), long-poll wake on a new event, inline + stored sync filters
  (`limit`, `not_types`, v2 read, section read, ownership), leave section after
  a kick.
* User data compat suite `tests/compat/userdata_test.go`: profile get/set/delete
  (403 for other users, refreshed `m.room.member` events and `joined_members`),
  global + room account data (404 when unset, 403 for other users, sync
  delivery, not repeated on the next sync), room tags (order kept, `m.tag` in
  the sync room account data, delete + 404 on re-delete), presence (offline by
  default, status_msg, currently_active, 400 on bad values, 404 for remote
  users, own-user writes only), receipts (`m.read` and `m.read.private`
  ephemeral, 403 for strangers, 400 unknown type, 404 for an event of another
  room, never moves backwards), `read_markers` (`m.fully_read` account data,
  unread count cleared, `read_receipt` produces the receipt), notifications
  (a mention fires, sender and unmentioned members stay quiet, highlight tweak +
  `highlight_count`, display-name rule after a profile change), push rules CRUD
  (`/pushrules/` and `/global/…`, single rule and kind reads, create, pattern
  shorthand, `/enabled`, `/actions`, delete including a default rule, 400/404
  cases, per-user isolation), pushers (set/update/delete-by-identity/list, 400
  without identity, per-user isolation), media (upload, v3 + client v1 downloads
  with and without a filename, content type/disposition headers, 404 for unknown
  ids and foreign origins, `m.upload.size`, create + `PUT upload`, 409 on reuse,
  PNG thumbnails scale/crop sizes, 404 for non-images, 400 for a bad width).
* History & relations suite `tests/compat/history_test.go`: `/context` (centre
  event, `events_before` newest-first, `events_after` oldest-first, `limit`,
  state included, context tokens feed `/messages`, 404 for an unknown event,
  403 for an outsider), `GET /event` permissions (403 for strangers on event,
  messages, context and relations of another room; 404 for an unknown id;
  redacted events keep their id/type and lose `content`), reactions (ordering,
  `rel_type`/`event_type` filters from path and query, combined filters,
  impossible filter → empty chunk, relations of an unknown target, paging with
  `limit` + `from`, 403 for strangers, reacting to a missing event is 200),
  threads (root + replies listed once, `count` = 2, `num_events` = 3,
  `latest_event`, `include=mine`, bogus `include`/`relationship` → 400, 403 for
  strangers, `unsigned["m.thread"]` in the sync timeline, `?rel_type=m.thread`
  on `/relations`), `timestamp_to_event` (f/b around an event ts, before/after
  the whole room, default dir, 404 both ends, 400 for a missing/invalid `ts` and
  an unknown `dir`, 403 for strangers), reports (200 with and without a score,
  duplicate reports, 404 unknown event, 400 positive score, 400 non-string
  reason, 403 for a non-member), typing (`m.typing` in another member's sync
  with `user_ids`, own id never echoed, `typing:false` + empty `user_ids` when
  someone stops, an expired `timeout` lapses on its own).
* Holistic walkthrough `tests/compat/user_script_test.go` (`TestUserScript`):
  one session end-to-end — register ×2, whoami, profile display name, create a
  named room with an alias, resolve the alias, room state, invite → invite sync
  with invite state, join by alias, `joined_rooms`, first sync state +
  `prev_batch`, `/v1/rooms/{id}/summary` counts, five messages incl. a thread
  reply, mention → `notification_count`/`highlight_count` + `/notifications`
  (the mention is the only highlighted entry), reaction → `/relations`,
  `read_markers` with the spec body (`m.fully_read` + `m.read`) →
  `m.fully_read` room account data, `m.read` ephemeral receipt, notification
  count cleared, thread summary in the other member's timeline, `/threads`,
  media upload → `m.image` event → authenticated download + scaled thumbnail
  with PNG bounds checked, presence write + read by another user, typing seen
  by the other member, `/messages` walks from the timeline `prev_batch` and
  from the tail, leave → member event in the other sync + `leave` section +
  forget + `joined_rooms` + `joined_members`, logout → 401 on whoami and sync
  while the other session keeps working.
* Gap chunk `tests/compat/todevice_test.go` + `tests/compat/history_test.go`:
  send-to-device (delivered to every device of a recipient, `user_id` of the
  sender kept, never echoed to the sender, per-device addressing reaches one
  device only, retried transaction id is idempotent, unknown/remote recipients
  and an empty `messages` map are no-ops, `m.room.*` → 400, no token → 401),
  sync room summary (`m.heroes` excludes the requester and caps at five,
  joined/invited counts, counts move with an invite), `POST /rooms/{id}/report`
  (200 `{}`, 403 for an outsider on a private room, 404 unknown room) and
  `POST /users/{userId}/report` (200, 404 unknown user, 400 self-report,
  400 non-string reason).

## Usability snapshot (checked by hand against a running binary)

A smoke test with `curl` against `go build -o /tmp/lattice ./cmd/server` + a
fresh database went through the everyday flows, so the server is usable today as
a **single-server, unencrypted** homeserver:

* Working: `versions` (v1.0–v1.11), register/login/`POST /refresh`/logout (the
  token really stops working)/`whoami`, createRoom + invite + join +
  `joined_rooms`, sending, `/messages`, sync (incl. long poll), unread and
  notification counts, receipts, read markers, `GET /v1/rooms/{id}/receipts`,
  typing, room state, push rules + `pushers` + `/notifications`, search (both
  request forms), spaces + `/hierarchy`, media upload/download, profiles,
  filters, redaction, threads/relations, account data, device keys
  (`/keys/upload` `/keys/query` `/keys/claim` `/keys/changes` + the key sections
  of sync), the admin routes, abuse reports, and
  `/.well-known/matrix/{client,server,support}`.
* Missing, so a client will trip over them: everything under
  `/_matrix/federation/v1/*` (**no federation**: users can only reach users on
  this server), `account/3pid/*` and the identity `requestToken` endpoints,
  SSO and `m.login.token`, application services, the deprecated
  `initialSync`/`GET /events`, and rate limiting (`ErrLimitExceeded`/
  `retry_after_ms` exist but nothing calls them). The end-to-end trust layer —
  cross-signing, key signatures and server-side key backup — is implemented.
* Operational facts worth repeating to whoever deploys it: `-jwt-secret` should
  always be set — an empty one generates a fresh secret per boot, which logs out
  every client on restart; the binary serves plain HTTP, so TLS terminates in a
  reverse proxy; the first registered account is the administrator; media lives
  in the database (`Media.Blob`), so backing up means copying one SQLite file
  and `-max-upload-size` bounds its growth.

## In flight (finish first)

Nothing broken: `go build ./... && go vet ./... && go test ./tests/compat/` is
green (the race run takes ~3 minutes because of the long-poll tests, and
`golangci-lint run ./...` reports the same 11 pre-existing findings). The two
latest chunks added the housekeeping pass for ephemeral data plus the abuse
report store, and closed small loose ends (long poll timing is now pinned by a
test, `third_party_signed` invites are refused, notification ordering is fixed
by a test). The chunk before that made unrouted paths answer
`404 M_UNRECOGNIZED`, and a follow-up fixed what Element tripped over
afterwards: an outstanding invitation no longer spins the sync loop (it used to
answer every long poll at once, which the client answered with a `/keys/query`
every few milliseconds), `/v1/capabilities` advertises room versions as `stable`,
and a tombstone names its replacement room. The newest chunk finished the
end-to-end layer and the bug underneath half of Element's complaints:
**state events now carry `state_key` even when it is empty** (which is why room
renames and topics never reached a client, and why a client thought the room was
version 1), plus cross-signing key upload and key signatures (verified over
canonical JSON, behind a `m.login.password` stage, replacement allowed once that
stage is answered, reported through `/keys/query`) and server-side room key
backup across the fourteen `/room_keys/*` routes. The newest chunk fixed what a second click
through Element exposed: the async media upload flow was half-implemented (no
`POST /_matrix/media/v3/commit`, so the upload Element makes for a room avatar
never finished and the avatar silently never changed), and the third-party
identifier endpoints were unrouted (which Element renders as "Unable to load
email addresses and phone numbers"). Coverage is 149/234 indexed endpoints with
26 extra routes and 94 compatibility tests. The chunk after that closed the
Element key-setup thread: `POST /keys/device_signing/upload` now asks for the
interactive auth exchange only when a request would change an identity that
already exists, and a partial upload no longer blanks the stored master key.
The same thread had one more bug hiding behind a still-correct 401, and it is
closed too: `POST /keys/upload` read the identity key of the device from
`device_key` while the specification names the field `device_keys`.
Pick the next chunk from the list below.

### Closed thread: Element's "unable to set up keys" was the server

The challenge on the **first** upload was the bug, not the client. Since v1.11
the specification requires User-Interactive Auth for
`POST /keys/device_signing/upload` except in two cases: the user has no master
key yet - the account being created, which is exactly where Element hit it - or
the request repeats the keys the server already stores, which is what keeps the
endpoint idempotent when a response is lost. Lattice demanded
`m.login.password` every time, and Element's rust crypto does not answer an
exchange it did not expect during a first setup, so the new account stopped at
"unable to set up keys" with the payload in the network panel looking correct.
The spec corpus is not in this workspace; the wording was taken from
`data/api/client-server/cross_signing.yaml` at the v1.19 tag of matrix-spec.

`UploadCrossSigningKeys` now verifies the keys first - the two exemptions can
only be decided once the request is known - and runs `authorizeCrossSigning`
only when the request changes a stored identity. Replacing a master key, and
adding a self-signing or user-signing key next to a stored master key, still
run the exchange, which is the path Element takes when the user resets their
identity; an auth dict on a request that needs none is ignored. A request that
repeats the stored keys stores nothing new and records no device list change.
`TestElementKeySetupChain` walks the new-account sequence end to end without an
auth dict, and answers the exchange for the keys that follow.

A second bug sat in the same path and went with it: an upload carrying only the
self-signing or user-signing key rebuilt the stored master key without its
object, and the store at the end of the function wrote that empty object over
the stored identity. `storedCrossSigningKey` now carries the stored bytes along,
and `TestCrossSigningPartialUpload` pins both halves.

Element still complained afterwards, with the same 401 in the network panel -
and that 401 was the specification working. The account already had an identity
by the time the user reached "I don't have a recovery key", so Element generates
a fresh set and the exchange stands in front of the replacement; the database
shows the client answering it and the new set landing. A changed upload against
an account that has an identity will always be challenged - the first-upload
path without an exchange can only be seen with a fresh account or a fresh
database.

The bug behind the complaint moved to another endpoint: `POST /keys/upload` read
the identity key of the device from `device_key`, while the specification -
Synapse, and the Rust crypto stack Element ships - write it as `device_keys`.
Every rust-crypto upload was answered with 200 while its identity key was
dropped, so the device never appeared in `/keys/query`, the device signature on
its master key could be resolved by nobody, and the signature the self-signing
key makes on the device merged into nothing, because there was no stored device
object to merge into. The handler now reads `device_keys` first and `device_key`
after it; `TestKeyUploadIdentityFieldNames` pins both spellings, and
`TestElementKeySetupChain` uploads the identity key the way Element does.

Even with the device published, the session stayed unverified, because the next
endpoint swallowed the signature that verifies it. The specification's body for
`POST /keys/signatures/upload` is a **bare map** of user ID to key ID to signed
object with no wrapper - that is what ruma, and so the Rust crypto inside
Element, and matrix-js-sdk send - but Lattice read only the wrapped
`signed_users` and `signed_keys` shapes its own tests had been using. Every real
signature upload (the self-signing key signing the device, interactive
verification, cross-signing another user) came back 200 and was thrown away, so
no device ever turned into a verified session and Element kept asking a
brand-new account to verify itself. `handleSignatureUpload` now decodes the
three shapes - the bare map first - and `TestKeySignaturesRequestShapes` pins
all three, each on its own device so no case can mask another;
`TestKeySignatures` and `TestElementKeySetupChain` now sign the way real clients
do. The spec wording came from `cross_signing.yaml` at the v1.19 tag of
matrix-spec, ruma's `upload_signatures::v3::Request` (which puts the map in the
body with no wrapper) and matrix-js-sdk's `KeySignatures` type; Synapse passes
the whole body to its handler, which confirms the shape.

What that leaves for the next session, if Element still complains: the log lines
from one click on "Set up encryption" (see the operational notes in README), and
the secret-storage account data (`m.secret_storage.key.*`, `m.cross_signing.*`)
that follows a successful upload.

The endpoint index in `SPECS.md` points at a spec corpus that is **not** in this
workspace (there is no `references/` directory), so specification wording cannot
be re-read locally. Anything that depends on exact spec wording — the default
push rule set, `/threads` ordering, key backup formats — should either be
implementation from an already-documented decision, or the corpus has to be
fetched first.

## Next steps (pick one at a time)

1. The largest missing groups of client endpoints (`go run ./cmd/endpoints -all`
   lists them): `account/3pid/*` plus the identity `requestToken` endpoints,
   `/login/sso/redirect`, the appservice APIs (`/appservice/*/ping`,
   `directory/list/appservice/*`) and the deprecated `initialSync`/`GET /events`.
   `GET /v3/voip/turnServer` is done (it publishes no relay), and so is the
   end-to-end trust layer: `/keys/device_signing/upload`,
   `/keys/signatures/upload` and `/room_keys/*`.
2. Small correctness gaps found while following a real client. Closed by the newest
   chunk: `m.room.encryption` can no longer be rewritten once set, a room with a
   tombstone rejects events and its power levels are raised by the upgrade, stripped
   state carries only the inviter and the invitee, and `POST /rooms/{id}/upgrade`
   rejects a missing `new_version` and a second upgrade of the same room. What is
   left of that list: a device key signature is only checked at
   `/keys/signatures/upload`, never at `/keys/upload`, so an unverified device key
   is stored as uploaded (deliberate, but worth stating in the client-facing docs).
   A second pass through Element fixed three things it tripped over: cross-signing
   keys whose `usage` is spelled with an underscore (Element's Rust crypto sends
   those, so key setup failed with `M_INVALID_PARAM`), device list changes echoed
   back to the device that caused them, and abandoned long polls logged as internal
   errors. Whether Element still queries keys in a loop after that is unconfirmed:
   it needs a click through the client, and the log should then stay quiet.
   A chain test that replays what Element does when it sets up encryption
   (`TestElementKeySetupChain`) then found three bugs no per-endpoint test could
   see, because every endpoint worked and only the sequence did:
   * `PUT /room_keys/version` was never routed. The spec moved key backup version
     creation from POST to PUT; Lattice had the deprecated POST, Element sent the
     current verb, and the setup died with a 405 whose only symptom was "unable to
     set up keys".
   * `POST /keys/signatures/upload` read the deprecated `signed_users` map and
     ignored the `signed_keys` list the Rust crypto sends, so Element's device
     signatures were thrown away and no device was ever marked as belonging to its
     user. Both shapes are accepted now.
   * `device_unused_fallback_key_types` looked for stored fallback keys under
     `curve25519`/`signed_curve25519` while they are filed under the algorithm of
     the id they arrived with (`fallback_curve25519`), so the section was always
     empty. Key backup versions also travelled as numbers rather than the strings
     the spec and the clients use.
   * Replacing cross-signing keys was refused with a 409 unless the request carried
     an `org.matrix.cross_signing_reset` flag that **no client sends** - the name
     exists in Element only as an OAuth action URL, not as a field in this request.
     So the user who answers "I can't verify" and lets the client start a new
     identity got `MatrixError: [409] attempted to replace existing cross-signing
     keys without the reset flag` and nowhere to go. Answering the interactive auth
     challenge is the authorisation; the replacement now goes through, deletes the
     keys it replaces, and removes the signatures those keys made on the user's
     devices so no client is left holding a signature it cannot resolve. Checked
     against the published specification: `org.matrix.cross_signing_reset` appears
     there only as an *account management action* name (an OAuth thing), and the
     one extra auth type this endpoint defines is `m.oauth` for OAuth homeservers.
   * `POST /keys/query` reported cross-signing keys inside `device_keys`, keyed by
     their key id. A device entry has to have a `device_id`, so a client parsing
     that map either rejects the response or invents a device that does not exist;
     the specification puts those keys in the separate `master_keys`,
     `self_signing_keys` and `user_signing_keys` maps, which is what the Rust
     crypto reads. `user_signing_keys` is answered only for the requester, as the
     specification asks.
   * Deleting a key backup only marked the row as deleted (GORM's soft delete),
     while the unique index over (user, version) kept counting it. Element's reset
     path deletes the backup and creates a new one, which then took the same number
     and failed to insert: `M_UNKNOWN: internal server error: duplicated key not
     allowed`, twice in the log, once per click. Deletes of key backups and
     cross-signing keys are hard deletes now, and the next version number is chosen
     over rows an older Lattice left behind so an existing database recovers on its
     own.
   * A client that reports "unable to set up keys" after a `401` challenge says
     nothing about whether it ever answered the challenge. `TestCrossSigningAuthShapes`
     now replays Element's exact second request - `identifier: {type: m.id.user, user}`,
     no legacy `user` field, the same session id after a refused attempt, a master
     key carrying both its self-signature and the device signature the Rust crypto
     adds - and the server accepts all of it. To make the remaining possibilities
     visible, every interactive auth exchange logs the challenge (reason, session,
     completed stages, stages on offer) and the outcome of the stage the client
     sent. A stale session id is not a lockout: the password check is the gate, so
     a retry with an unknown session and valid credentials goes through, and one
     without them does not.
   The sequence has now been replayed by a test; a browser still has to click it.
3. Loose ends spotted while doing history, relations, the user script, the gap
   chunk, the search/spaces chunk and the reports chunk:
   * Seeded push rules are only installed at registration; there is no rule
     version/migration, so accounts created before `.m.rule.message` existed
     keep under-notifying. The seeded `.m.rule.default` (matches only notices,
     with `count`) and `.m.rule.notice` are not part of the specification's
     default ruleset either — worth reconciling.
   * Decide (and test) the ordering of `GET /notifications`; lattice is oldest
     first today and the tests are order-agnostic.
   * annotate the `/context` centre event and `GET /event` with `m.thread` too
     (`annotateThreads` takes a slice, so wrap the single event).
   * `GET /relations` should hide the relations of a *redacted* relation target
     per spec, and `/threads` ignores `dir`/`to` (only `from`, always newest
     first).
   * `service.Threads` walks every `m.thread` relation of the room; a
     `total_events` column or an index on `(room_id, rel_type, relates_to)`
     would make that cheap.
   * `m.heroes` picks the first five joined members by insertion order; sorting
     by recent activity would match clients' expectations better.
   * The report store has no way to act on a report: no dismiss/delete, and the
     admin listing has no per-report GET. That is where moderation would start.
   * Event retention is not implemented: `RunMaintenance` prunes to-device
     messages, transaction ids and typing rows, but events and redactions live
     forever. A per-room `max_events`/`max_age` setting applied by the same pass
     would close the last retention gap.
   * Search is a plain `LIKE` over the serialised event content: good enough
     for a small server, but there is no word index, no stemming and no ranking
     beyond the requested order. A `words` column plus an index would make
     `order_by=relevance` possible.
   * The search categories of the JSON request form are all folded into
     `room_events`; a request naming another category is refused rather than
     ignored.
4. The rest of the Phase 3 E2EE chunk is done: device key distribution,
   cross-signing, key signatures and room key backup all exist. What is left in
   that area is verifying it against a real client suite rather than against
   Lattice's own tests.
5. Unit tests in `tests/unit/` (separate dir, same convention as compat):
   identifier helpers, error mapping, glob matching + push rule matching,
   power level maths, stream token parsing, endpoints coverage matcher
   (`cmd/endpoints` compare logic deserves a table test).
6. Docs and tooling:
   * `README.md`: what this is, how to run (`make run`), flags, what is
     unsupported (federation, E2EE key storage, SSO, 3PID, URL previews) and
     the phase status. It should also carry the deployment warnings from the
     usability snapshot above (always set `-jwt-secret`, TLS lives in a reverse
     proxy, one SQLite file is the whole backup).
   * `Makefile`: `build test race vet fmt swagger endpoints run clean`.
   * OpenAPI: annotations exist on selected handlers; `make swagger` should run
     `swag init -g cmd/server/main.go -o docs` when swag is installed (it is
     not installed offline, so don't commit generated files).
   * Add the coverage number to README and keep ENDPOINTS.md regenerated by
     `make endpoints`.
7. Phase 3 candidates after tests: federation (probably out of scope — record
   the decision), application services, SSO/login token flow, `m.login.token`,
   rate limiting (`M_LIMIT_EXCEEDED` + `retry_after_ms` helper exists),
   presence/typing fan-out through sync ephemeral for other rooms.

## Commands

```sh
go build ./... && go vet ./...
go test ./tests/compat/ -race
go run ./cmd/endpoints -out ENDPOINTS.md      # coverage report
go build -o /tmp/lattice ./cmd/server && /tmp/lattice --addr 127.0.0.1:8008 \
    --server-name localhost:8008 --db /tmp/lattice.db --jwt-secret dev
```

## House rules for this repo

* Handlers stay thin: decode → `service` call → `writeJSON`; errors go through
  `a.writeError` (which uses `matrix.ErrAs`).
* New endpoints: add the route in `internal/api/routes.go`, add a case in
  `tests/compat`, then re-run `make endpoints`.
* Tests live in `tests/` only — the user asked for functional tests outside the
  implementation tree.
