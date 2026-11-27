# Findings, gotchas and decisions not to re-derive

Operational knowledge: what a future session must not re-derive, what needs a human or a network
rather than a code step, what is deferred on purpose, the commands, and the house rules for this
repository. Step-by-step findings live in [STEPS-LOG.md](STEPS-LOG.md).


---

## Superseded state notes (2026-11-02 to 2026-11-04)

Kept because they record bugs found and fixed, not because the tallies still read this way —
the current numbers are in [TODO.md](TODO.md).

## Current state (verified 2026-11-04)

- A working homeserver with its **federation transport in production shape**:
  `make build vet lint` clean (`make lint` = golangci-lint, 0 issues),
  `go test ./...` green — **139** compatibility tests (~62 s here, ~5½ min
  with `-race` on this machine), **70** offline unit tests in `tests/unit`
  (milliseconds), and **84** two-server tests in `tests/federation` (~40 s,
  ~56 s with `-race`). Coverage: **168 of the 234** indexed endpoints, 33 extra
  routes (`make endpoints`). Steps 1–10 are closed: rate limiting, the
  client-API correctness tails, batched reads, offline unit tests, the
  federation signing/transport ground, the **event DAG** (events hashed,
  signed, identified by reference hash, stored with `prev_events`,
  `auth_events` and their exact signed JSON), **transactions** — inbound
  `PUT /send` with a replay-safe transaction log, EDU sinks for typing,
  receipts, to-device, device lists and cross-signing, an outbound per
  destination queue with retries, and `m.room.server_acl` enforced in both
  directions — and the **cross-server membership handshake**: `make_join`/`send_join`
  and `make_leave`/`send_leave` served and sent, the state a peer hands over persisted
  and usable, remote aliases resolved through `/query/directory`, and a room's decision
  about a user (ban, unban, invite, kick, leave) reaching the server that user lives on.
  Steps 9 to 13 are done and **step 14** wrapped the release: the two-server scenario
  table runs through the join handshake rather than through rows a test wrote, and both
  Complement packages have a recorded tally.
- Against [Complement](https://github.com/matrix-org/complement), the upstream
  Matrix suite, **40 of 107** client-API tests fail (`make complement`) and **51 of
  91** server-server tests fail — 40 green, up from 4 green before step 10 and 22 green
  before step 11. Green now: the membership handshakes and their guards, the graph and
  state reads a peer needs to judge what it is sent, gap filling both ways, profiles and
  the room directory, spaces and `/hierarchy`, OpenID exchange, device lists, key
  queries and claims, and `TestContentMediaV1`.
  See _Complement_ below for how they run, what a run can hide, and what each remaining
  failure is waiting for. A room's rules are read once, by one list, for what this server
  writes and what a peer sends (step 20). The transport itself is not among the reasons:
  knocking, restricted rooms and the space summary that authorises them are green (steps 17-19);
  what is left is room versions past v11, application services, presence, the federation
  `PUT /invite` endpoint, and redaction of an event the peer has never seen. The federation
  media cases are green; see step 16.
- The three bugs the federation suite found were not federation bugs: access tokens died
  at every restart (the signing secret was per process, the sessions are in the
  database), the event stream counter was re-seeded from the events table alone (so a
  restarted server re-issued positions and clients holding a later token saw nothing),
  and a device list change made by one of a user's other devices never reached that
  user's own sync.
- Federation is live behind `-federation` (default off): a persisted ed25519
  signing key, `/_matrix/key/v2/*`, `/_matrix/federation/v1/version`,
  `/_matrix/federation/v1/user/devices/{userId}`,
  `PUT /_matrix/federation/v1/send/{txnId}`, `GET /_matrix/federation/v1/event/{eventID}`,
  `GET /_matrix/federation/v1|v2/make_join/{roomId}/{userId}`,
  `PUT /_matrix/federation/v1|v2/send_join/{roomId}/{eventId}`,
  `GET /_matrix/federation/v1/query/directory`,
  `GET /_matrix/federation/v1|v2/make_leave/{roomId}/{userId}`,
  `PUT /_matrix/federation/v1|v2/send_leave/{roomId}/{eventId}`,
  `PUT /_matrix/federation/v1/invite/{roomId}/{eventId}` and its v2 counterpart, which
  carry `invite_room_state` so a server that has never been in the room can still tell its
  user they have been invited,
  an outbound client that signs, fetches an auth chain it is missing, and retries a
  queue, and inbound middleware that refuses anything it cannot attribute to a peer.
  Still deferred on purpose: SRV lookups, acting as a
  *client* of someone else's notary, key rotation, and per-peer budgets for the
  volume routes that arrive with the read-side queries of step 11.
- What federation does **not** do yet, so a two-server test is not mistaken for a
  working join: no room can be joined across servers (step 10), so the rooms of every
  two-server test are shared by hand. What an arriving event does is no longer faked:
  it is judged — shape, signature, content hash, then the room's rules against its own
  auth chain and against this server's state — and the outcome decides what happens:
  accepted moves the state, the graph and the timeline; soft-failed is stored, relayed
  and hidden; refused is kept only as a receipt with its reason. Two Lattice servers
  already exchange messages, typing, receipts, room keys and device list updates for
  rooms that both are in.
- The E2EE trust layer is done end-to-end: device keys, cross-signing
  (verified server-side over canonical JSON), key signatures, room key
  backup. The Element "unable to set up keys" thread is **closed** — pinned
  by `TestElementKeySetupChain`, `TestKeySignaturesRequestShapes`,
  `TestCrossSigningPartialUpload`, `TestKeyUploadIdentityFieldNames`,
  `TestCrossSigningAuthShapes`. Do not re-open it; the only thing left there
  is clicking through a real Element (see _Open_ below).
- Second Element thread, closed 2026-11-01 (with its follow-up the same day): an
  Element Web account spammed
  `POST /keys/upload` (48 `signed_curve25519` keys each time, every sync) while
  messages from another user would not decrypt. Two server bugs, both fixed and
  pinned: `/keys/upload` **replaced** the one-time key pool instead of adding to
  it (so the client's count never reached its target of 100 and it topped up
  forever, destroying keys other devices were about to claim), and the stored key
  object was **normalised**, which dropped `signatures` — a claimed key could not
  be verified, so no Olm session opened and the room key never arrived. See the
  gotcha under _Device one-time keys_. Long-polling was checked and is fine: an
  idle sync waits out its timeout (`TestSyncIdleWaitsForTheTimeout`). The follow-up
  was the next request in that same chain: `PUT /sendToDevice/m.room.encrypted`
  refused as a room event, so the Megolm key never reached the other device — see
  the _PUT /sendToDevice_ entry under _Behaviour already handled_.
- Federation decision (2026-11-01): **in scope for the first release**,
  gated behind a `-federation` boolean flag (default off → server-server routes
  unmounted, `M_UNRECOGNIZED` on those paths). The work is the steps 6–14 of this
  plan. Target: spec v1.19 server-server API. One part of it is deliberately **not**
  behind the flag since step 7: events are hashed, signed and given derived ids
  whether or not federation is on, because those are properties of the stored event
  and a room created unsigned can never be federated afterwards.


---

## Deferred past the first release (decisions recorded 2026-11-01)

- **3PID / identity** (`account/3pid/*` changes, `requestToken`): stays
  routed-but-refuses-with-a-reason; no identity server integration.
- **SSO, `m.login.token`, OAuth**: password + registration-token auth is
  enough for a first release.
- **Application services**, **bridges**: out; `/thirdparty/protocols`
  stays empty.
- **Presence** (client push model stays as-is; no `m.presence` EDUs).
- **Policy Servers** (`/_matrix/policy/v1/sign`, spec v1.18): rare in the
  wild; revisit after federation proves out.
- **Deprecated** `initialSync` / `GET /events` / `PUT
/state/{type}/{key}/{txnId}` (Go's mux path-cleaning makes the empty
  state key form unroutable — decided, not laziness).
- **Moderation actions** on reports (dismiss/delete, per-report GET),
  **event retention** (`max_events`/`max_age` in maintenance), **search
  word-index/ranking**, **push-rule seed migration** (fresh installs make
  it moot), **per-device to-device watermark** (`ToDeviceRepo.Prune` still
  unused), **signing-key rotation** (`old_verify_keys`), **SRV delegation**
  and being a notary of record, **`/logout/all`-style extras**.
- **Video calls**: were "out"; they are planned now, in the section below.


---

## Open (need a human or network, not a code step)

- A real **Element click-through** against a running Lattice (the whole key
  setup chain is chain-tested, but "does the browser stop complaining" is
  unconfirmed; watch the interactive-auth log lines README describes).
- `internal/coverage/SPECS.md` references a spec corpus that is **not** in this workspace;
  anything needing exact spec wording should quote the matrix-spec tag it
  was read from (e.g. `cross_signing.yaml` @ v1.19) or fetch the corpus.

## Verified facts and gotchas (condensed — do not re-derive)

### Layout and symbols

- Module `github.com/enimatek-nl/lattice`, Go 1.26. `matrix.Event` field is
  **`ID`**; `persist.Room` field is **`ID`**; `persist.Event.Matrix()`
  returns a **value**. `persist.Event` has `Depth` but no
  `prev_events`/`auth_events`/raw-JSON columns (step 7 adds them); it does
  denormalise `RelatesTo`/`RelType` from `m.relates_to` via
  `service.relationOf` (with `m.in_reply_to` fallback → `m.in_reply_to`).
- Repos are `db.XRepo` fields; key ones: `EventRepo` (`Relations`,
  `ThreadStats`, `ThreadsFor`, `ByPositions`, `GetByIDs`, `LatestActivity`,
  `Search`, `HistoryByTimestamp`), `DeviceKeyRepo`,
  `OneTimeKeyRepo` (conditional `Claim` → a prekey is handed out once),
  `DeviceListRepo`, `ToDeviceRepo` (`Pending(userID, deviceID, since, 100)`,
  unused `Prune`), `TxnRepo` (see `replayTxn` in `service/events.go`),
  `TypingRepo.ActiveUserIDs(roomID, except, nowMS)`.
- `internal/matrix/canonical.go` has the full JSON-signing layer:
  `CanonicalJSON`, `DecodeCanonical`, `SignEd25519`,
  `VerifyEd25519Signature`, `Ed25519KeyID`, `SignatureKeyID`, unpadded-base64
  helpers. Go's `json.Marshal` is **not** canonical (escapes `<>&`, floats
  numbers).
- `internal/realtime` hub wakes long-polling syncs; `service.Sync` keeps a
  500 ms fallback tick. `api.New(svc, authStore, api.Config{…})`,
  `app.Registered()` feeds the coverage tool; a catch-all answers
  404 `M_UNRECOGNIZED` and restores 405+`Allow` by matching
  `app.Registered()`.
- Pure rule packages cut out of the service layer so `tests/unit` can drive
  them (and federation can reuse them): `internal/power` (level arithmetic —
  `service.powerLevels` only reads the event and calls `power.ApplyDefaults`, and
  `power.EventRequired` expects levels that already have the defaults restored),
  `internal/authcheck` (the room version's authorization rules, and the auth-event
  selection: `Check` is the whole list, `Rules` is the list without the rule that
  reads the sender's own `auth_events`, which is what `stateres` needs),
  `internal/stateres` (state resolution v2, over an `authcheck.Graph` and state
  snapshots), `internal/push` (rule precedence and matching — `service.PushRuleKinds`
  is `push.Kinds`, `notifyParticipants` only gathers inputs), `internal/coverage`
  (the SPECS.md matcher and report — `cmd/endpoints` is a thin `main`).
  `.gitignore` used to carry `coverage.*`, which silently matched
  `internal/coverage/coverage.go` and would have kept the package out of the
  repository: the rule is now `coverage.out` / `coverage.html`, do not widen it
  back. There is no file named `coverage.go` in that package for the same
  reason.
- Auth: JWT HS256 with per-device `jti`; `auth.Require`/`Optional`/
  `RequireAdmin`, `auth.GetUserID/GetDeviceID/GetToken/IsAdmin`,
  `api.requireSelf(w, r)` gates personal-data writes.

### Behaviour already handled — do not "fix" again

- `GET /members` defaults `membership=join` (`*` = all); default invite PL
  is 0; PL changes need sender ≥ max(old,new) per user; `m.room.create` and
  `m.room.member` writes are guarded (`stateguards.go`); `m.room.create`
  and `m.room.encryption` are fixed once written; txn replay runs **before**
  these guards.
- Tombstoned rooms reject sends (even the creator), cannot upgrade twice,
  and the upgrade raises `events_default`/`invite` to `max(50,
users_default+1)`; tombstone carries `replacement_room`, successor's
  create carries `predecessor`; upgrade requires `new_version`.
- Sync: `device_one_time_keys_count` on every authenticated sync;
  `device_lists` only incremental, filtered to shared rooms, `left` always
  empty; **an invitation that already exists must not wake a long poll**
  (only an event written after the token may); a device list change is
  never echoed to the device that caused it; an abandoned long poll writes
  **no body and no log line**; **an idle sync waits out the timeout and answers
  with the token it was given** (`TestSyncIdleWaitsForTheTimeout` — answering at
  once turns every client into a busy loop, which is what an upload storm looks
  like in an access log); `Event.IsState` carries empty-state-key state
  explicitly (backfilled on migrate) and every state event serialises
  `state_key` even when it is `""`.
- Cross-signing: `usage` accepted as `self-signing` AND `self_signing`
  (rust crypto), objects stored byte-for-byte; replacement only after the
  `m.login.password` UIA stage; first upload and repeat-of-stored need no
  exchange; replacing deletes the old ids **and** their device signatures;
  `/keys/query` answers cross-signing keys in `master_keys`/
  `self_signing_keys`/`user_signing_keys`, never as devices; `POST
/keys/upload` reads `device_keys` then `device_key`; `/keys/signatures/
upload` accepts the bare-map, `signed_users` and `signed_keys` shapes;
  key backup versions are **strings**, PUT creates (POST 405), wrong version
  400 `M_WRONG_KEY_VERSION`, deletes are hard deletes.
- `PUT /sendToDevice/{eventType}/{txnId}`: the per-device value **is the event
  content** — there is no `type`/`content` wrapper, the type comes from the path.
  `m.room.encrypted` is therefore **accepted**: it is the Olm envelope, and that
  request is how every client hands a Megolm key to another device after claiming
  its one-time key (`refusesOnTheToDevicePath` still bans the other `m.room.*`
  types). The content is stored and delivered byte-for-byte, `ciphertext` and the
  client's `org.matrix.msgid` included — Lattice never reads it. Third Element
  thread, 2026-11-01: refusing it spent the claimed key and left the recipient with
  "the sender's device has not sent us the keys for this message" forever.
  `TestSendToDeviceOlmEncrypted`, `TestSendToDevice`.
  **Known divergence, not fixed:** a wildcard send to one's own user is echoed back
  to the sending device (`Pending` matches `target_device IN (device, "*")` with no
  sender exclusion), while Synapse drops the sender's own device. Delivery to the
  *other* devices of the same user — what multi-device key sharing needs — works.
- Device one-time keys are opaque and claimed once; fallback keys are
  repeat-claimed, never counted, filed under their arriving key id's
  algorithm (`fallback_curve25519`). **An upload adds to the pool**: a client
  counts the keys on offer (upload response + every sync's
  `device_one_time_keys_count`) and tops up to 100, so replacing the pool makes
  Element Web POST `/keys/upload` after every sync and throw away keys another
  device was about to claim (`OneTimeKeyRepo.Add`; `MaxOneTimeKeys` = 100 bounds
  it; re-uploading a claimed key id never revives it — `claimed_ts` is not in the
  conflict update). Synapse is the reference here: the count in its upload response
  covers the keys "previously persisted", a repeated upload is a no-op
  (matrix-org/synapse#2053), and the key object is stored and returned as uploaded
  with `fallback` and `signatures` intact. **Store the key object byte-for-byte**: the claimer verifies
  the signature the uploading device made over that object, so normalising
  `{"key":…, "signatures":…}` into `{"signed_curve25519":…}` hands out an
  unverifiable key — no Olm session, "Unable to decrypt" on the other side. Those
  two bugs together were the Element Web upload storm plus undecryptable messages
  (2026-11-01). Pinned by `TestOneTimeKeyTopUpKeepsThePool`,
  `TestOneTimeKeysComeBackAsUploaded`, `TestClaimedOneTimeKeysSurviveTopUp`, and
  the open-time repair of databases written before the fix by
  `TestUpgradeDropsUnverifiableOneTimeKeys`.
- Media: **two flows, both complete** (`create`→`PUT upload`→`commit`, with
  `-media-reservation-window`; reservations are unserved placeholder rows,
  404 not empty 200); inline in SQLite; PNG thumbnails via stdlib.
- Routed-but-refuse policy: unrouted paths are worse than honest refusals —
  3pid reads answer real empty lists, mutations answer `M_FORBIDDEN` with a
  reason; `POST /invite` refuses `third_party_signed`; `GET /login`
  advertises `m.login.password` only; `GET /v1/auth_metadata` 404 on
  purpose; `GET /room_keys/version` 404 until a backup exists;
  `POST /logout/all` keeps the calling token valid.
- `POST /register` (and GET) with no auth → 401 with `flows`/`session`
  (`m.login.dummy`, or `m.login.registration_token` with
  `-registration-secret`); guests skip; the challenge payload rides in
  `matrix.Error.Extra`. `/.well-known/matrix/client` nests
  `{"m.homeserver":{"base_url":…}}`.
- `/v1/capabilities` advertises room versions 1–11 as `stable`/`unstable`
  words, default 11; one set of state/redaction rules for all versions
  (until federation forces per-version rules — see step 7).
- Stripped state = inviter + invitee memberships only, plus create, join
  rules, aesthetics, encryption; **must include `m.room.create`**.
- `m.room_versions` default room version and `m.federate: false` currently
  in `writeInitialState` — flips with `-federation` in step 10.
- Shapes the upstream suite pinned (2026-11-01), all of them spec answers
  rather than Complement inventions: the `GET /rooms/{roomID}/members` chunk
  is the **member events** in client format (each carries `type`, `state_key`,
  `content`, `room_id`), not a wrapper of `user_id`/`membership` keys; `GET
/rooms/{roomID}/state` answers with a **bare JSON array** (both compatibility
  harnesses now keep an array body in `Response.Array`); `m.room.canonical_alias`
  stores the **full alias**, so `createRoom`'s `room_alias_name` is normalised
  before the event is written; an `m.room.member` event keeps the client's own
  content keys (`MemberContent.Extra`), which is how a join with custom content
  survives; the first `/_matrix/client/versions` answer names its
  `default_room_version`; the first `/sync` carries `pushrules`.
- Session end: `POST /logout` ends the device behind the token (its sessions and
  its device keys go with it), and `POST /logout/all` ends **everything**, the
  calling token included — a device list full of signed-out devices is what the
  other reading produces. A password change deletes the pushers **of the other
  devices**, so a pusher records the device that registered it (the body's
  `device_id`, else the device of the access token that made the request).

### Federation ground facts (verified against spec v1.19, 2026-11-01)

- PDUs/EDUs ride Transactions over `PUT /_matrix/federation/v1/send/{txnId}`
  — max 50 PDUs / 100 EDUs; no new txn to a destination until the previous
  got 200. Requests carry `Authorization: X-Matrix
origin=,destination=,key=,sig=` signing `{method, uri, origin,
destination, content}`; `uri` = full path + query, never the host; server
  names in the signature ignore delegation.
- Keys: self-signed `GET /_matrix/key/v2/server` (`valid_until_ts` capped
  at 7 days), notary `POST /_matrix/key/v2/query` /
  `GET /_matrix/key/v2/query/{serverName}`. Discovery:
  `/.well-known/matrix/server` (`m.server`) → port 8448. Unknown federation
  endpoints answer 404 `M_UNRECOGNIZED` (already Lattice's contract).
- Sinks for EDUs and primitives for signing all exist locally (see facts
  above); what is missing is the DAG storage, server signing key, and
  reference-hash event ids (steps 6–7).

### Testing harness (tests keep getting these wrong)

- Paths: room account data is
  `/user/{userId}/rooms/{roomId}/account_data/{type}` (no
  `/rooms/…/account_data` shortcut); room summary is **v1**
  (`GET /_matrix/client/v1/rooms/{roomId}/summary`); media: unauth
  `/_matrix/media/v3/…`, auth `/_matrix/client/v1/media/…` (only that one
  takes a trailing `/{fileName}`); aliases in test paths must be escaped
  (`%23wonderland:test.server`).
- Helpers: `sendTyped`, `eventsOf`, `chunkBodies`, `oldestEvent`,
  `eventOfType`, `testPNG`, `pngBounds`, `mustMediaID`,
  `actionsOf(...).notifies()/.hasTweak()`, `lenList`, `lenPushers`,
  `toDeviceOf`, `typingEvents`, `decodeReports`, `server.DoRaw`,
  `server.MustSync`, `Response.Int/String/ErrCode`. Keys: `fakeDeviceKey`,
  `oneTimeKeys` (algorithm-named shape), `elementOneTimeKeys` (`key` +
  `signatures` shape), `keyCounts`, `claimKeys`, `keyIDOf`, `keyObjectOf`,
  `keyIDs`. `server.Sync`/
  `SyncSince` do **not** update `session.NextBatch` — assign it by hand;
  `Response` has no `Sync()` helper (`json.Unmarshal(resp.Raw,
&matrix.SyncResponse{})`); time travel via `newClockServer(t, clock)` +
  the mutex clock in `maintenance_test.go` (give the token service a long
  TTL there); logs silenced unless `LATTICE_TEST_LOGS=1`.
- SQLite `:memory:` gives per-connection DBs — `NewDatabase` rewrites to
  `file::memory:?cache=shared` + pins one connection; the harness uses a
  file in `t.TempDir()` instead.
- **There is no schema version.** `NewDatabase` (in `internal/persist/persist.go`,
  not `db.go`) runs `AutoMigrate` plus the open-time repairs —
  `backfillStateFlags`, `sweepUnsignedOneTimeKeys` — on **every** open, so a
  repair has to be idempotent and cheap. That also makes an upgrade testable
  without a fake old binary: open a file, write the old-shaped rows through a
  repo, `Close()`, `NewDatabase` the same path, assert (`TestUpgradeDropsUnverifiableOneTimeKeys`).
  `db.GORM()` is the escape hatch for rows a repo will not show.

### GORM / mux / SQLite gotchas

- Every `Count` chain needs `db.Model(&X{})`. `clause.OnConflict` names
  **snake_cased columns** (`push_key`, `app_name`, `sent_ts`,
  `fully_read_event_id`); the `Where().Assign().FirstOrCreate()` form is
  banned (struct leaks into WHERE). `PushRuleRepo.Get`/`PusherRepo.Get`
  return a **non-nil zero row** + `ErrRecordNotFound` — branch on
  `persist.IsNotFound(err)`, never `x == nil`. `ReadMarkerRepo.Get` hands
  back a zero row; `service.SetReadMarker` sets the ids itself and also
  writes `m.fully_read` room account data.
- Deletes that free a unique index must be `Unscoped()` hard deletes: push
  rules, pushers, memberships (forget), aliases, profile fields, devices,
  sessions, key backups + sessions, cross-signing keys.
- mux: empty state keys arrive as a trailing `/` (both patterns
  registered); push rules served by one subtree handler per method skipping
  the deprecated `global` scope; wildcard names must match at the same
  position.
- Receipts never move backwards (`PostReceipt` keeps the newer position).
  Read markers accept the spec spellings `m.fully_read`/`m.read` plus the
  legacy ones.
- Push rules: new accounts seeded with `.m.rule.message` +
  `.m.rule.encrypted_room_one_to_one`; every message notifies, only
  mentions highlight; `.m.rule.room_one_to_one` fires first in 2-member
  rooms. Rule placement uses `PushRulePlacement{Before, After}`, moves
  renumber from 0.
- To-device: unique `(sender_id, txn_id, target_id, target_device)`; the
  on-conflict update **excludes `stream_pos` and `ts`** (a retry must not
  jump a device's timeline); delivery is positional after the sync token;
  `m.room.*` types → 400; unknown recipients skipped silently.
- Sync room summary: `matrix.JoinedRoom` carries `Heroes`/`
joined_member_count`/`invited_member_count` (`*int64`), built by
  `service.syncRoomSummary` (cap 5).
- Search = escaped `LIKE` over serialised content, newest first, joined +
  invited rooms only; `event_fields` projection returns `matrix.Object`
  (a struct would emit `null`s); `filter.rooms` checks room by room (403,
  not a silent gap).
- Hierarchy walk: breadth-first `m.space.child`, default depth 1,
  `next_batch` = offset, unreadable/unknown child sets `incomplete_chain`;
  `children_state` holds the space's own child events.
- Reports persist (`persist.Report`, copies the event's content at report
  time — redaction cannot hide it), newest first; admin listing clamps
  limit to 100 (max 1000); all three report endpoints exist.
- Housekeeping (`RunMaintenance`): prunes to-device (24h), txn ids (24h),
  stale typing (30s grace); `StartMaintenance` returns one stop fn that
  cancels **and** waits; admin trigger: `POST /_synapse/admin/v1/
maintenance`. First registered account = administrator.
- Typing rows carry `ExpiresTS`; `ActiveUserIDs` filters expired rows,
  `Recent` deliberately does not (expired state still emits a "nobody is
  typing" `m.typing`); `handleTyping` uses the token user but 403s when the
  path `{userId}` differs.

### Packaging, the image, and the release tag (added 2026-11-03)

- **Every flag is an environment variable already**: `LATTICE_` + flag name,
  upper-cased, dashes to underscores (`internal/config/env.go`). Nothing registers a
  name per setting, so a new flag in `cmd/server/main.go` is configurable in a
  container the moment it exists. Precedence is **command line > environment >
  default**; a variable set to blank counts as unset (compose writes `KEY=` for a key
  left blank), and `-version` is excluded — it is a question, not a setting, and an
  orchestrator carrying `LATTICE_VERSION` for its own bookkeeping would otherwise turn
  every start into a printout. What arrived from the environment is named at startup.
  `tests/unit/configenv_test.go` pins all of it; note that the flag package leaves a
  numeric or duration value at **zero** after a failed `Set`, which is why a bad
  variable stops the start rather than being logged and carried on.
- **Two images, deliberately.** `Dockerfile` (root) is what a release publishes:
  built inside the image, no entrypoint script, configuration through the
  environment. `deploy/complement/Dockerfile` is the test image: binary built outside
  (that image is rebuilt for every suite run) and a shell entrypoint that mints the CA
  certificate. `.dockerignore` keeps `bin/` and `deploy/` in the context so
  `make complement-image` keeps working.
- **The image runs as uid 10001** and owns `/data`. A *bind* mount keeps the host's
  ownership, so an unprivileged container answers `permission denied` opening the
  database unless the host directory is `chown 10001:10001`'d; named volumes need
  nothing. Verified end to end: register + whoami against a named volume, WAL files
  and media included, health route answering.
- **Cross-architecture**: the build stage is `--platform=$BUILDPLATFORM` and reads
  `TARGETOS`/`TARGETARCH`, so the Go compile stays native even for an arm64 image
  (checked: static aarch64 binary built on amd64). Only the Alpine runtime stage needs
  emulation, for `apk` and `adduser`; `docker/setup-qemu-action` registers it in CI.
  Rootless podman without binfmt cannot build that stage for a foreign platform —
  local `make image` builds for the host's architecture, which is the point.
- **A tag is the version**: the release job compares `GITHUB_REF_NAME` with
  `LatticeVersion` in `internal/service/capabilities.go` (a `-rc` suffix is allowed),
  because an image tagged `0.2.0` that answers `0.1.0` on `/_matrix/client/versions`
  costs a week of debugging aimed at the proxy. `v0.1.0` publishes `0.1.0`, `0.1` and
  `latest`, for `linux/amd64` and `linux/arm64`. Do **not** add an explicit
  `type=raw,value=latest` to the metadata step: `latest` comes from that action's auto
  flavour, which is what keeps it off a prerelease — `v0.2.0-rc1` publishes only
  `0.2.0-rc1` and moves nothing (verified against the action's own test table).

### Account management from the terminal (added 2026-11-03)

- **One binary, two halves.** `cmd/server/main.go` looks at `os.Args[1]`: a flag starts
  the homeserver (every existing start form — README, Makefile, container entrypoint —
  begins with a flag), a known word runs an operator command from `internal/cli`. A word
  that is *not* a known command is refused with the list, **not** treated as a plain
  `lattice`: `lattice adduser` typoed would otherwise open a listening port and write a
  fresh `lattice.db` into whatever directory the operator was standing in. `cli.IsCommand`
  is the single classifier; `tests/unit/cli_test.go` pins which words are commands.
- **The service layer has one account path each way**, and both halves use it:
  `service.CreateAccount(ctx, AccountParams)` (`internal/service/account.go`) is what the
  HTTP `Register` calls *and* what `add-user` calls, so a CLI account gets the same bcrypt
  cost, the same default push rules, the same guest and first-account-admin rules. On the
  way out, `service.DeactivateLocalAccount` (`internal/service/devices.go`) is the
  unauthenticated operator path; `DeactivateAccount` is the user-interactive-auth endpoint
  wrapper around it. Do not add a second place that creates or tears down an account.
- **Go's `flag` stops parsing at the first positional**, so `add-user alice -db path` read
  `-db`, the path and everything after them as more account names. `inOrder` in
  `internal/cli/cli.go` moves a command's flags ahead of its names, using the flag set's
  own knowledge (`IsBoolFlag`) to know whether a word after a flag belongs to it, and puts
  a typed `--` back in front of the names so a name that looks like a flag stays a name.
  It is unexported on purpose: pin the behaviour through `cli.RunWithEnv` with the name
  first, which is what every test does.
- **A password is asked for, never read from the environment.** `-password`, `-admin`,
  `-display-name`, `-yes`, `-limit`, `-all` are excluded from the `LATTICE_*` mapping in
  `internal/config/env.go`: a `LATTICE_PASSWORD` in a container's environment would become
  the password of whichever account somebody adds next, and `LATTICE_ADMIN` would make it
  an administrator. The exclusion is a table case in `configenv_test.go`. On a terminal the
  password is read twice with the echo off (`golang.org/x/term`); in a pipe it is one line,
  and an empty line is refused with the three ways to supply it. A blank `-password ""`
  means "not given" (same blank-is-unset rule as everywhere else).
- **Validate before asking for a secret**: `add-user` checks the name is one this server
  can hold *before* prompting, so nobody types a password twice for a name that was never
  going to work. It also warns when a capitalised name will be stored lower case, and
  trims spaces (a keyboard allows both; a client's JSON body does not).
- **Deactivation, not deletion.** `remove-user` clears sessions, device keys,
  cross-signing keys, key backups and room-key backups, forgets the memberships, and marks
  the row: the ID stays taken on purpose, because it is written into every event that
  account ever sent and a freed name would inherit them. `add-user` refuses to reuse it,
  and says so. Deactivated accounts drop out of `UserRepo.List`, so `users` counts them
  with a separate query (`deactivatedCount`, through `db.GORM()`); a list that showed only
  sign-in-able accounts would leave an operator hunting for one that is merely gone.
- **Confirmation is for a terminal, not for a pipe.** `remove-user` asks for the user ID
  back; with no terminal on stdin nothing is asked and the command proceeds, because
  `echo x | lattice remove-user` and `docker exec` without `-t` have no way to answer. `-yes`
  is the explicit script form. Getting this backwards either blocks every pipeline or skips
  the only check in front of a destructive command.
- **Both commands work against a running server, on the same file**, and that is the point
  (`tests/compat/operator_test.go` runs a live server and a second `persist.Database`
  handle side by side). Users and sessions are rows the middleware reads per request, so an
  account created by another process signs in immediately and `remove-user` kills a token a
  live server had already accepted — no restart, no shared state. SQLite WAL plus the busy
  timeout in `persist.NewDatabase` is what makes the second writer safe.
- **In a container the commands need no flags**: the image sets `LATTICE_DB` and
  `LATTICE_SERVER_NAME`, so `docker exec -it lattice lattice add-user alice` finds the
  right file and the right name. Verified by hand: closed registration, account added with
  `docker exec`, login from the host as `@sanne:matrix.example.org`, removal, refusal to
  reuse the name.
- **Output plumbing**: `say`, `sayLine`, `sayText` in `internal/cli` are the only writers
  and they drop fmt's error deliberately (a pipe closed by `| head` changes nothing about
  what reached the database); that keeps errcheck quiet without `_ =` noise at each site.
  Error paths print one sentence, not the whole usage block — the sentence is the
  interface, and `-h` prints the block.
- **`omitempty` cannot omit a struct** (found 2026-11-20 via Complement). MSC3771's
  `unread_thread_notifications` is optional in `GET /sync` — a client asks for it with a
  filter — so `JoinedRoom` carried it as a value with `omitempty`, and encoding/json never
  omits a struct: every joined room of every sync this server ever served announced the
  section as `{}`, which is both a claim this server cannot honour and enough to fail
  `TestThreadedReceipts`, whose check asserts the absence (`!t.Exists()`). Optional wire
  fields are pointers, maps or slices here — and the section is a **map from thread id to
  counts**, so getting the type right was also getting the shape right
  (`tests/compat/sync_test.go: TestSyncWithoutThreadCounts`).
- **404 and 400 are protocol, not style** (2026-11-20). An unknown push rule **kind** was a
  `400 M_INVALID_PARAM` while an unknown rule is `404 M_NOT_FOUND`, and Complement's
  MSC4306 setup reads the difference literally: `client.GetPushRule(t, "global",
  "postcontent", rule)` continues on 404 ("no rule to disable") and everything else is a
  server error, so two thread-receipt tests died before sending a single event. The spec
  says `404 "The push rule was not found."` for every read and every mutation of an
  existing rule; only PUT — *creating* inside a kind this server does not have — is
  honestly `400 M_INVALID_PARAM`, and that split is what
  `tests/compat/push_test.go: TestPushRuleNotFoundSaysSo` pins.
- **A repository update that matches no row says so by silence** (2026-11-20). gorm's
  `Updates` with a `WHERE` that hits nothing returns no error, so `PUT …/enabled`,
  `PUT …/actions` and `DELETE` on an absent push rule all answered `200 {}`. Measured before
  touching anything, because "the endpoint validates" is not a fact anyone should infer:
  the service now asks `pushRuleThere` first, which is also where the unknown-kind answer
  for those three lives.
- **`.agent-work/` is untracked and has served two unrelated histories — it is not this tree's
  record** (2026-11-20). Branch `retry` (tip `100668f`, 2026-11-09, on `origin/retry`, 189 commits,
  root `ef634b5 chore: empty branch (fresh start)`) shares **no ancestor** with `main`:
  `git merge-base main retry` exits 1, and the trees differ by 537 files. Its working notes sat in
  the gitignored `.agent-work/` beside this line's own and read as current: they claim a CI gate in
  `.github/workflows`, a `make verify` target, and `make lint` falling back to `go vet`. This tree
  has one tag-triggered image workflow, no `verify` target (`git log -S'verify' -- Makefile` empty),
  and a `make lint` that exits 1 without golangci-lint. The notes are archived under
  `.agent-work/retired-retry-line-20261109/` with a README of how that was established and an audit
  of all 53 tasks they left open; two survived as real gaps here and are in the queue, the rest were
  finished independently or are that branch's own tooling.
  The sharper lesson is about method, and the audit tripped over it immediately: `grep m.typing
  internal/federation` finds nothing, which reads as "typing never crosses servers", while
  `internal/service/fedsend.go:365` sends it, `fedrecv.go:407` receives it, and `TestRemoteTyping`
  passes. Code here lives in `internal/service`; a grep over the package a foreign note happens to
  name is not a measurement. Read a claim, then run the thing.

## Commands

```sh
make build vet lint                       # compiles, vet, golangci-lint
make test                                 # unit + compat suites (~45 s here)
make race                                 # same with -race
make endpoints                            # coverage report -> ENDPOINTS.md (generated, untracked)
make run                                  # dev server, 127.0.0.1:8008, /tmp/lattice.db
make image                                # the release image (Dockerfile) -> lattice:latest
make complement-image                     # bin/lattice with CGO off, then the Complement image
make swagger                              # docs/swagger/{swagger.json,swagger.yaml}
go test ./tests/compat/ -race
go test ./tests/compat/ -run 'TestAnOperator|TestOperator'   # the account commands alone
bin/lattice --addr 127.0.0.1:8008 --server-name localhost:8008 \
    --db /tmp/lattice.db --jwt-secret dev      # what `make run` executes
bin/lattice add-user alice -db /tmp/lattice.db -server-name localhost    # operator command
bin/lattice users -db /tmp/lattice.db -server-name localhost
bin/lattice remove-user alice -db /tmp/lattice.db -yes
```

Note: the compat (and future federation) suites bind loopback ports, so they
need to run outside any network sandbox.

## House rules for this repo

- Handlers stay thin: decode → `service` call → `writeJSON`; errors go
  through `a.writeError` (which uses `matrix.ErrAs`).
- New endpoint: add the route in `internal/api/routes.go`, add a case in
  `tests/compat` (or `tests/federation` once steps 6+ land), then re-run
  `make endpoints`.
- Tests live in `tests/` only — the user asked for functional tests outside
  the implementation tree. `tests/compat` is black-box HTTP against a running
  server; `tests/unit` (`package unit`) drives pure functions with tables and no
  database. When a rule deserves a unit table but lives unexported in a service
  file, cut it into a small pure package (`internal/power`, `internal/push`,
  `internal/coverage`) instead of exporting it from `service`; the service keeps
  the storage plumbing.
- Federation code mounts only under `-federation`; with the flag off the
  binary must behave exactly as a single-server Lattice.
- New repo convention (unchanged): model + interface + Impl + `NewXxxRepo`
  in `internal/persist/`, wire into `Database`, add to `AutoMigrate`,
  remember the hard-delete rule for unique indexes.


---

## The gaps, ranked as they read on 2026-11-04

Superseded by the queue in [TODO.md](TODO.md); kept because the reasoning behind each ranking is not written down twice.

## The gaps, ranked (measured 2026-11-05)

Two coverage documents sit in this directory: [coverage.md](coverage.md) is ours, area by area,
with an endpoint tally from `cmd/endpoints` (**180 of 234** indexed routes);
[spec-coverage.md](spec-coverage.md) is the same shape of document for a Rust homeserver, and
[coverage-comparison.md](coverage-comparison.md) reads them against each other. Nine things come
out of that reading - things that server does, and this one does not. In order of what each
unlocks:

1. **Room version 12** — room ids as hashes, room creators with infinite power, state resolution
   v2.1, stripped state as full events. **Nineteen** Complement tests carry those names, and v12
   is what the specification now recommends as the default. → **step 21**.
2. **Application services** — no `/_matrix/app/v1/*` at all, no registration files or
   namespaces, no `m.login.application_service`. No bridge of any kind can attach, and a bot
   cannot hold an identity on this server.
3. **Simplified Sliding Sync (MSC4186)** — `POST /_matrix/client/v4/sync` with lists, ranges,
   filters and subscriptions. **Done 2026-11-04** (step 22): both paths are served, and the holes
   left are listed there.
4. **Push delivery** — rules, pushers and unread counts are here; nothing is POSTed to a push
   gateway (`POST /_matrix/push/v1/notify` as a *client* of one, `event_id_only`, rejected
   pushkeys). Mobile push does not work, and the missing half is one outbound request loop.
5. **The federation paths peers actually ask for** — `PUT /v1|v2/invite/{roomId}/{eventId}` (so:
   no invite rejection or rescinding over federation), the specified
   `GET /v1|v2/state/{roomId}?event_id=` (this server registers the event id as a **path**
   segment, so a peer asking the specified way is answered with nothing), federation
   `POST /v1/publicRooms` (both public-room routes were registered at `/v3`), and the
   rejected-event bookkeeping that four Complement tests measure.
6. **Authentication that is not a password** — `/login/sso/redirect`, `m.login.token` issued by
   an identity provider, MSC2965 discovery, `POST /v1/login/get_token`. `/auth_metadata` answers
   a reasoned 404 today.
7. **Sync and timeline details a client feels** — `lazy_load` accepted and state served in full,
   `m.ignored_user_list` not applied to a timeline, no `state_after`, top-level `presence` and
   `org.matrix.msc3814.e2ee` sections declared and never filled, no `DELETE` on account data, no
   `m.fully_marked`, `/relations` without `recurse`, and the `timestamp_to_event` walk returning
   an event *after* the asked moment (`TestJumpToDateEndpoint`).
8. **An admin surface** — the reference has a Synapse-compatible subset (users list/get/create/
   deactivate/reset, rooms list/details/delete/force-join, purge, server notices). This server
   has four admin routes, `/_synapse/admin/v2/users/{id}`, and the CLI.
9. **The MSC surface and the quality of life around it** — delayed events (MSC4140), thread
   subscriptions (MSC4306), invite filtering (MSC4155), poll push rules (MSC3930), relation
   filters in `/messages` (MSC3874), account-data deletion (MSC3391); a full-text search index
   instead of substring matching; `m.room.retention`; fuzz targets; a load harness.


---

## Findings of 2026-11-18 — three of them cost a Complement run each

- **An `/messages` response has to be able to say it is finished.** `end` was in every answer,
  including the empty page at the start of a room, where it repeated `from`. The specification's
  only way of telling a client the walk is over is the *absence* of the property ("If no further
  events are available ... this property is omitted from the response"), so a client that obeys
  the specification — Complement's `TestMessagesOverFederation` does, in a loop with no other exit
  condition — asks for the same page forever, and each ask re-walks the room. Symptom on the
  server: one core at 100% for the rest of the suite's timeout, no log lines. The walk now fetches
  `limit+1` and never shows the extra row; that row is what it knows "beyond" from, and a page
  bounded by `to` ends at its bound.
- **A token an endpoint never looks for is still a token that works.** `POST /v3/refresh` answered
  `M_UNKNOWN_TOKEN` to every client that obeyed the specification, because it read the credential
  from the query string while the specification puts it in the JSON body — `{"refresh_token": "..."}`,
  or the older `{"access_token": "..."}` holding the same value. The case that proved it was filed
  against the wrong feature: `TestTxnIdWithRefreshToken` reads as a transaction-id failure and never
  got as far as the transaction id. The query parameter is still read when the body named nothing, and
  nothing that read the document sends one — a token in a URL is copied into every log between the
  client and the server.
- **A suite that cannot finish produces numbers nobody can disbelieve.** `docs/TODO.md` claimed 95
  green of 106 while `docs/COMPLEMENT.md` recorded that the same package "did not finish in its 40
  minute budget" — the figure cannot have come from one run, and the honest one (78 of 106, the
  package finishing in six minutes) only appeared once the hang was gone. When a suite has a test
  that never returns, every number in the file next to it is suspect; fix the hang before trusting
  the tally.
- **The Complement deployment runs without invented rate limits.** The suite says nothing about
  rate limiting and sends in bursts — `TestMessagesOverFederation` alone puts 300 events through
  one room — so answering any of them with `M_LIMIT_EXCEEDED` fails a test for a reason the
  harness never asked about. `deploy/complement/entrypoint.sh` passes `-no-rate-limit`;
  `LATTICE_RATE_LIMITING_ENABLED=true` puts them back for whoever wants to measure them. Same
  agreement Complement keeps with Synapse through its configuration.
- **`unsigned.transaction_id` belongs to a device, not to a user.** It is the reader's own note
  about a send it made — how a client that lost the answer to `PUT /send` recognises the event —
  so it is stored with the device that wrote it (`persist.Txn.DeviceID`, indexed with the event id)
  and looked up per reader by user *and* device, one batched query per page
  (`Service.transactionsFor`). It is served on `GET /rooms/{id}/event/{id}`, the `/sync` timeline
  and a page of `/messages`, and to nobody else: another device of the same user gets silence,
  which is also why the device is part of the query rather than a filter applied after it.
- **`TestMessagesOverFederation` is now a real failure, and it names a real gap.** It used to hang;
  now it walks the room, and the room's early history is not there: a member who joined after the
  events were written is not given them, because nothing backfills a late joiner's `/messages` from
  a peer. That is the backfill gap, not a pagination bug, and it is the same shape as the queue's
  "gap filling both ways" line.

- **MSC4115's `unsigned.membership` is about the reader, not the sender.** The message alice wrote
  before bob joined reads `leave` to bob and `join` to alice, and a client cannot reconstruct it
  afterwards: a room that opens its history at the join never shows the membership events that would
  have said so. No state-at-event storage was needed — the room's own `m.room.member` events in the
  order it applied them are one person's membership history, and the history gate already read them
  to decide whether an event may be shown at all. That reading became a shared `membershipTrack`, and
  an event is answered by the last change at or before its own stream position, `leave` before the
  first. Served by the `/sync` timeline, its leave sections, `GET /messages`, `GET /event` and
  `GET /context`.
- **The user directory was public-rooms-only, and answered `null`.** `results` is a required
  property, so an empty answer is `[]`; and because the candidates were the members of public rooms,
  the people of a private room were findable by nobody — including each other. A public room now
  opens its members to anybody and a room the searcher stands in opens them to the searcher, with up
  to three rooms named per entry. What an entry *displays* stays the account-level display name: a
  name chosen for one room is that room's business, so it is neither returned nor matched on. Doing
  the room-specific names properly means indexing them per room and matching a searcher only against
  the rooms they share, which costs a state read per shared room and is not yet built — Synapse does
  it, and `TestRoomSpecificUsername*` does not ask for it.

What is next at the front of the queue, in the order it reads cheapest-first: `GET /rooms/{roomID}/members?at=<sync token>`
— membership at a sync position, which the `membershipTrack` above is half of, and which
`TestGetRoomMembersAtPoint` waits on — then backfill for a late joiner, the gap
`TestMessagesOverFederation` now names, which needs a federation read this server has never made.
The six names that fail inside `MustSyncUntil` are still untriaged and belong before either.

## Findings of 2026-11-18, evening — a room that cannot be verified, and what waiting for it costs

Joining `#iaa:cluephone.org` from a real deployment produced a log full of lines like these and a
client that sat on "Joining…" for minutes:

```
federation: the state of !hezP…:cluephone.org carries $abW…, which this server cannot verify:
  the signing key ed25519:a_SkHH of rixo.nl could not be used:
  requesting /_matrix/key/v2/server?origin=lattice.cybertim.net from rixo.nl:
  dial tcp: lookup rixo.nl on 169.254.1.1:53: no such host
```

- **A room keeps the signatures of servers that stop existing, and one of them was enough to shut
  the room.** `rixo.nl` and `tessera.oele.net` no longer resolve; `home.enimatek.nl` times out; the
  state of one room carried events signed by each. Lattice asked only the server that signed an
  event, which is the specification's *first* step, and stopped there. What that costs is worse than
  a refused join: an unverifiable state event is left out of an applied join (`fedjoin.go`, "what
  this server cannot verify, it leaves out of the state it keeps"), so the room is joined with
  holes — and every later gap-fill then reports a disagreement with the peer about exactly those
  state keys (`cluephone.org and this server disagree about m.room.member/@…: this server keeps $…
  and will not take $… from a snapshot`), while the peer refuses what this server forwards for them.
  A room in that state does not heal: it has to be left, forgotten, and joined again once its events
  can be verified, because `ForgetRoom` refuses while joined and stored state is not re-verified.
- **Keys of a peer that cannot publish them now come from a key server, and the peer is still asked
  first.** `-key-perspectives` (default `matrix.org`) names the servers asked `POST
  /_matrix/key/v2/query` after the peer's own answer failed or could not be believed. The answer is
  held to the same discipline as the peer's: the document must name the server asked about and be
  signed by the keys it publishes, or it is refused. A key server is a source the operator named — it
  can relay keys and cannot invent a signature, since an event is only ever believed when the key it
  names verifies it. What is *not* done yet: verifying the key server's own signature over the query
  response, which is what would stop a tampering middlebox between this server and the key server it
  trusts over TLS.
- **An expired document is believed for the moments it covered, and for nothing newer.** The naive
  rule — refuse any document past `valid_until_ts` — is what makes a dead server unverifiable, and
  believing it without a bound would let a key retired in 2021 sign today's traffic. So the clock is
  compared against the event: `VerifyKeyOfAsOf(server, keyID, origin_server_ts)` accepts a stale
  document up to its own expiry, and a live request (a signature over *now*) still refuses it. An
  event that names no timestamp is judged as of now, because an expired key is not a generous reading
  of a missing field.
- **The waiting was the other half of the bug, and the uglier one.** Every one of those fetches used
  the transport's transaction timeout — thirty seconds each — no memory of a failure existed, and
  verifying a room means verifying a hundred events at once, each fetching on its own. Five
  unreachable names turned one join into minutes of timeouts, per event. A key document is now
  fetched under its own ten-second budget, one fetch per server is in flight at a time, and a failed
  attempt is remembered with a doubling hold-back (thirty seconds to five minutes) so the next caller
  is answered from memory. That turns "one dead server" from a per-event tax into one short wait.
- **`dial tcp 45.137.205.246:443: connect: connection refused` for a peer whose name is this box is
  a deployment fact, not a peer's fault.** Container networks often cannot reach the host's own
  public address, and a server that cannot reach `matrix.enimatek.nl` from inside a podman network
  falls back to port 8448, gets nothing, and writes a log line that reads like the peer is broken —
  or, when it lands on its own listener, like the peer's certificate is wrong. `--network=host`, or
  `--add-host=<name>:host-gateway` per name, is the fix; the log now says which address it settled on
  being unable to delegate, which is where an operator looks.

## Finding of 2026-11-18, late — the number that was wrong was the waiting

The same room produced a second bug, and this one was hiding in a log line an operator would read as
"the internet is bad today":

```
federation: rixo.nl is not answering, transaction l-…-nv4L6HNGMXO2 (0 PDU(s), 3 EDU(s))
  retried in -15278539382ms: … dial tcp: lookup rixo.nl …: no such host
```

- **A negative wait is the tell, and the unit was the crime.** The backoff drew its random shave in
  nanoseconds and subtracted it as milliseconds:
  `delay -= time.Duration(n.Int64()) * time.Millisecond` with `n` already a nanosecond count. At the
  ten-minute ceiling that is a subtraction of up to four and a half *years*, so `next_try` landed far
  in the past and the promise "asked again in a while" meant "asked again on the next flush" — every
  second, forever, for a server whose DNS record no longer exists.
- **No backoff does not merely waste time; it starves the peers that answer.** One pass takes a
  bounded number of destinations, ordered by the oldest queued work, and dead servers hold the oldest
  work: their ephemeral backlog predates everything a live peer is waiting for. So five gone-away
  servers occupied every slot of every pass and a room's message to a server that was *alive* was
  never picked up. The symptom an operator sees is a message that never arrives with no error about
  the destination it was addressed to — because nothing was ever attempted to it.
- **The pass bound has to bound the pass.** `DueDestinations` reads two sources — destinations with
  queued items, and destinations whose outstanding transaction has reached its retry time — and the
  limit applied to the first alone, so a pass could be longer than the caller agreed to. It now
  applies to the combined list, and queued events are preferred over ephemeral messages: a device
  list update owed to a server that stopped existing is not a reason for a message to wait.
- **`OutboxRepo.Prune` is written and not wired.** Backlog that a peer will never receive is kept
  forever today, which is harmless in one room and permanent in the one that holds members of servers
  that have disappeared: those device list updates are re-queued and retried indefinitely. Leaving,
  forgetting and rejoining such a room removes the reason to queue them; the alternative is the prune
  the repo already offers, run on maintenance.



## Findings of 2026-11-20 — the ten names nobody had read yet

`docs/TODO.md` carried ten Complement names labelled "not triaged". Each was run **alone**, with its own
170 s timeout, against Complement `7b38a63` and the image built from `main` @ `8c98333`. All ten are red
alone, so not one of them was a parallel-run artefact. What each is waiting for is now a sentence, not a
question mark; the durable part:

- **Three names, one cause, and the cause is a decision.** `TestSync`, `TestMembersLocal` and
  `TestPresenceSyncDifferentRooms` all die inside `MustSyncUntil` on `presence.events` — empty or absent.
  Presence is stored and answered per user and never streamed, so a checker that waits to *see* someone
  come online can never fire. And the 2026-11-04 note that kept this looking like flakiness ("`TestSync`
  times out in a parallel run while passing alone") is simply no longer true: it fails alone, in 22 s, on
  presence. The long-poll wake-up is not what keeps it red.
- **An unknown push rule kind is a rule that is not there, not a bad request.** Measured on a live server
  in the same session: `GET /pushrules/global/postcontent/<rule>` answers
  `400 M_INVALID_PARAM: unknown push rule kind "postcontent"`, while `GET /pushrules/global/content/<nosuchrule>`
  answers `404 M_NOT_FOUND: no such rule`. Complement's thread cases ask exactly that question to
  *discover* whether MSC4306's rules exist — they tolerate the 404 and move on — and so they die in their
  own setup on the 400, before a single threaded receipt is sent. A kind is a namespace; asking for a rule
  in a namespace this server does not have is the same question as asking for a rule that is absent.
  `postcontent` itself is still unimplemented, and that remains the real work behind
  `TestThreadedReceipts` and `TestThreadReceiptsInSyncMSC4102`.
- **`POST /keys/upload` validates nothing and says 200 while doing it.** Measured on a live server: a
  `device_keys` object carrying only `user_id` and `device_id` — the required `algorithms`, `keys` and
  `signatures` absent — is accepted with `200 {"one_time_key_counts":{}}`, and so is one whose `user_id`
  names a **different user** than the caller. The suite wants `400 M_BAD_JSON` and `400`. That is not
  test-sensitivity: an unverifiable key document now sits in the database and will be handed to every peer
  that queries it.
- **`/keys/query` is the one place where the suite is stricter than the specification.** Asked about a
  user with no devices this server answers `{"device_keys":{}}`; the sub-case "query for user with no keys
  returns empty key dict" asserts `device_keys.<user>` exists and is an object. v1.19's wording (under
  `failures`) says that a user reached but unknown records no failure and is instead *"missing from the
  `device_keys` result"* — which reads as a defence of the answer we give. The case carries a `// sytest:`
  comment, so it is an inherited expectation, not a protocol requirement: match it in one line, or record
  the refusal. Calling it a protocol gap would be the wrong kind of honest.
- **`/sync` carries neither a room `summary` nor a global `account_data`, and each costs a name.**
  `TestRoomSummary` reads `rooms.join.<room>.summary` for `m.joined_member_count` / `m.invited_member_count`
  and gets nothing; `TestPushRuleRoomUpgrade` waits for top-level `account_data.events` and never sees it,
  so the push-rule carry-over across a room upgrade — what that case is actually about — is never reached.
  Both are response-shape work in the sync builder, and each one *un-masks* its case rather than fixing it.
- **`/threads` answers in the right order without the bundle.** `TestThreadsEndpoint` compares
  `event_id` against `unsigned."m.relations"."m.thread".latest_event.event_id`; the root arrives at the
  right index with no `unsigned` relation at all. The ordering is done; MSC3856's bundling of the latest
  reply (and with it the per-thread `count`) is what is missing.
- **`TestSyncTimelineGap` is green where it argues and red where it asks.** Its `incremental` and `full`
  subtests pass — the `limited` flag this name used to stand for is closed. The parent fails because the
  mock federation server runs with `UnexpectedRequestsAreErrors=true`, and Lattice probes
  `GET /federation/v2/state/{roomId}/{eventId}`, then `v1/state/…`, then the `v3/state/{roomId}?event_id=`
  query form (`internal/service/fedgap.go:415`), none of which the manifest lists. It should not have to
  ask: `MustJoinRoom` delivered the state in the join response. Real peers pay those three round trips per
  join today, so this is an efficiency finding that happens to show up as a red name.
- **Running Complement from inside the pi-courier bridge needs a transient scope.** Rootless podman refuses
  under the bridge's unit (`cannot re-exec process to join the existing user namespace`) — `NoNewPrivileges=true`
  blocks the setuid id-map helpers — and the identical command works from a plain shell. `systemd-run --user`
  starts the run outside that cgroup and everything behaves:
  `systemd-run --user --collect --unit=lattice-csapi --setenv=DOCKER_HOST=unix:///run/user/1000/podman/podman.sock --property=TimeoutStartSec=45min /bin/sh <script>`.
  Keep the default `Type=simple`: with `Type=oneshot` the `systemd-run` call itself blocks until the suite
  finishes, which is not what a run you mean to watch wants.

## Findings of 2026-11-22 — the re-marshal class, found in the store path

- The pass-through is centralised here, so retry's per-endpoint version of the bug cannot recur
  endpoint by endpoint. `persist.Event.Matrix()` (`internal/persist/event.go:118`) copies `origin`,
  `hashes`, `signatures` and `unsigned` out of the event's `JSON` column and sets `Stored` to those
  bytes; `matrix.Event.MarshalJSON` (`internal/matrix/event.go:119`) then serves `Stored` verbatim,
  adding only `event_id`, `room_id` and this server's `unsigned`. `Stored` is deliberately unset for a
  redacted event: the stored copy still contains the content the redaction destroyed.
- What it does not centralise is **which bytes land in that column**, and there the bug class is live.
  An event this server creates stores `matrix.CanonicalJSON(signed)` (`internal/service/dag.go:198`). An
  event **received over federation** stores `matrix.Encode(pdu)`
  (`internal/service/fedapply.go:328` and `:546`) — and `matrix.Encode` is plain `json.Marshal`
  (`internal/matrix/event.go:384`), which escapes `&`, `<` and `>` as `\u0026`, `\u003c`, `\u003e`.
- Measured with the two encoders on one PDU whose body was `Tom & Jerry <3> a<b`: the canonical encoder
  reproduced the bytes it was given, `Encode` did not — same keys, same numbers, three escapes per
  special character. So an inbound event carrying any of those characters is stored, and later served,
  as bytes its origin server never signed: `sha256` in `hashes` will not match for anyone who checks,
  and the signature covers those same bytes.
- Why it stayed invisible. Verification at accept time works on the parsed PDU rather than on the stored
  column, so nothing fails on the way in. The client-API suite is single-server. No test we run has ever
  handed a `&` to a second server and asked it to verify what came back.
- The fix is one encoder at two call sites. What makes it a step rather than a grep is the test: accept a
  PDU whose content carries `&`, `<` and `>`, then check that the bytes this server serves hash to the
  `sha256` the peer sent. Rows already stored keep the escaped copy; the bytes are gone and cannot be
  recovered, so this is a fix for what arrives from here on.
- Fixed the same day. `signedForm()` (`internal/service/fedapply.go`) is now the one place that decides what
  bytes a received PDU is kept as, and both call sites use it. `TestAnArrivingEventIsKeptInTheFormItWasSignedIn`
  delivers an event reading `Tom & Jerry <3> a<b` across the two-server cluster and asserts three things: the
  copy holds the characters it was sent, the copy re-encodes to itself, and it matches what the origin server
  kept. Reverting the encoder fails it on the first two, which is what makes it a test. `go test ./...` is
  green, and the server-server suite re-measured against the built image is **78 green, 13 red** in 319 s —
  the same thirteen names as the recorded baseline, so the intake path changed nothing a peer could see.
- A related comparison, learned from the test failing for the wrong reason. The copy the **origin** server
  keeps carries `event_id`; the copy a **receiving** server keeps does not. That is allowed: a room version 3
  and later event does not carry its own id, so a server writes one into its own copy for its own clients, and
  anyone comparing two servers' copies has to drop the id before comparing.
- Method, the cheap kind. A commit id read out of another clone of this project named a commit `main`
  does not have, and it took a re-check to notice. `git cat-file -e <id>^{commit}` costs nothing; run it
  against **this** repository before citing a commit. Nothing wrong reached these docs.

## Findings of 2026-11-22, afternoon — a client speaking the dialect from before the MSC closed

- Reported by the author of smaltino, and it arrived as a symptom rather than as a bug report: the
  room list's preview stops updating against lattice, while the same build against their own two
  test servers is fine. The client is matrix-rust-sdk 0.19.1 on ruma-client-api 0.25.2, so the
  question was which of the two is holding a superseded wire shape.
- Read from ruma's own `sync_events/v5.rs`, which is what shipped in 0.25.2: the endpoint metadata
  points at `/_matrix/client/unstable/org.matrix.simplified_msc3575/sync` and is documented as
  MSC4186, but `pos`, `timeout` and `set_presence` carry `#[ruma_api(query)]`, `required_state` is
  `Vec<(StateEventType, String)>`, the window is `ranges: Vec<(UInt, UInt)>`, the filter is
  `is_invite`, and the room answer is read as `invite_state` plus flat `notification_count` and
  `highlight_count`. Every one of those is the shape from before the MSC closed: MSC4186's
  appendix lists them as its changes from Synapse v1.151.0 — point 8 moves the URL parameters into
  the body, 7 changes `required_state`, 9 turns `ranges` into one `range`, 11 renames `is_invite`,
  2 renames `invite_state` to `stripped_state`, 15 deletes the two counters.
- The symptom is point 8. The body carried no `pos`, lattice looked in the body, found nothing, and
  opened a fresh connection on every poll. Measured with the query token ignored: a room already
  sent on the connection comes back `initial: true` with 2 timeline events where only 1 is new —
  that is the failure text `TestSlidingSyncReadsItsParametersFromTheURL` now carries, so the claim
  stays checked. A client whose every response is initial has to redraw the whole list each round,
  which is what "the preview stops keeping up" looks like from the outside.
- What was changed is the placement of three parameters and nothing else: `readSlidingQuery`
  (`internal/api/sliding.go`) fills `pos`, `timeout` and `set_presence` from the query **only where
  the body left them out**, so a request that names a parameter in both places is read as the spec
  describes it. A bad value in the query is a bad value like any other, `M_INVALID_PARAM`, because
  `set_presence` was already checked that way in `checkSlidingRequest`.
- Why this one and not the other five. The MSC is not consistent about `set_presence` itself —
  changelog point 1 adds it as a URL parameter and point 8 moves the parameters to the body — so a
  server that reads it from either place is not inventing a dialect. And the endpoint already reads
  the sliding-sync proxy's `$ME` and `$LAZY` state-key tokens on the same grounds: reading an extra
  placement costs nothing, requires nothing of a client that sends the spec form.
- What was deliberately not done: accepting `required_state` as pairs, or `ranges`, or `is_invite`,
  and above all answering `invite_state` beside `stripped_state`. Those are not placements but
  shapes, and answering in two shapes at once puts a superseded field on the wire for every client
  of the endpoint. The clean fix is upstream in ruma, whose v5 module claims MSC4186 and sends the
  prototype; the client's author was offered that route and the maintainers of the client decide
  whether to carry a patched ruma. If lattice grows the shim later, it belongs on the unstable path
  only, behind a name, with the dialect a connection arrived in logged once.
- The same afternoon measured the rest of the dialect, and it was worse than a stale preview: a request
  carrying `required_state` as pairs was refused outright, `400 M_BAD_JSON`, in a list and in a subscription
  alike. So a client that sends the proxy shapes is not silently mis-syncing, it cannot sync at all — which
  is the good case, because it complains. Two others do not: `ranges` and `is_invite` were answered `200` and
  then ignored, `ranges` read as "no window" so the whole filtered list came along, `is_invite` as "no filter"
  so a list that wanted only invitations got everything. A field accepted and dropped is the failure mode to
  design against; a `400` at least says so.
- What was built, all of it on the request side: `required_state` in either of its shapes, `ranges` read as the
  window its pairs cover, `is_invite` answered as `is_invited`. `SlidingRequiredState.UnmarshalJSON`
  (`internal/matrix/sliding.go`) is the whole of the shape work, `SlidingSyncList.Window` and
  `SlidingRoomFilter.WantsInvite` the whole of the other two. What is still not done is on the answer side —
  `invite_state` beside `stripped_state`, and the two counters MSC4186 deleted — because that writes a
  superseded field into every response of the endpoint rather than tolerating one more request.

## Findings of 2026-11-23 — which release is running is not answerable from the server

Confirming v0.2.5 live meant reading image ids. `GET /_matrix/client/versions` returns `versions`,
`unstable_features` and `default_room_version`; `service.ServerVersion()` ("Lattice/0.2.5") has no
caller; the startup banner names settings and a listen line but no version. The release rule in
"Packaging, the image, and the release tag" — an image tagged `0.2.0` that answers `0.1.0` costs a
week — presumes an answer that is not there. Synapse and Construct include a non-spec
`server_version`, and clients and proxies paste it into bug reports, so one more field would make
the tag guard mean something over the wire. Queue item F.

**Closed 2026-11-24.** `/versions` answers `server_version: "Lattice/<version>"` — the shape Synapse
has always answered with, and the same string the release job compares against `LatticeVersion`, so
that guard can now be contradicted over the wire instead of being a check nobody can complete. The
Go runtime moved to the startup line, which names version and runtime, because the wire value is
the one that ends up pasted in a bug report. The compat test asserts the field against the constant,
which ties the three places the number lives — constant, tag, wire — into a single claim.

## Findings of 2026-11-24 — two answers that described the wrong thing

Two small ones from the same lab, both about a server that answered a question it was not asked.

**Presence of a user who does not exist read as somebody who was just active.** The route checked
that the user id belongs to *this domain* and then reported the no-record answer: `offline`,
`currently_active: false`, `last_active_ago: 0`. A name with no account behind it is not an idle
person, and `last_active_ago: 0` is the worst possible filler for a person who never existed - it
reads as "a moment ago". Synapse and asaroton answer `404 M_NOT_FOUND`, and a client that gets an
answer here shows the stranger as somebody present; with the 404 it says "nothing known". The check
is now `UserExists`, which is also what a remote user still trips, so the one behaviour nobody
disagrees about stayed: presence of another server's user is not this server's to claim.

**A room with no backed up keys was reported as missing.** `GET /room_keys/keys/{roomId}` answered
`404 M_NOT_FOUND` when the room had no sessions, and the spec reserves that status for a backup
*version* that does not exist. The consequence is larger than the field: a client restoring keys asks
for every room it is invited to, before it knows whether that room has anything, so one empty room
ended the restore. Synapse answers `200 {"sessions": {}}`, so that is the answer now, and the 404
moved to where absence actually lives - the version - which the service already distinguished on its
own.

The shared shape is worth naming: both handlers treated "no rows" as "no such thing". For a client
those are opposite statements, and only one of them is an error.

## Findings of 2026-11-24 — a private read receipt reached the whole room

A receipt of type `m.read.private` is, per the receipts section of the spec, "only sent to the user
who sent it". Lattice accepted the type, stored it correctly, and then delivered it to every other
member of the room - measured in the classic `/v3/sync` ephemeral events, in the `/v4/sync` receipts
extension, and on the `org.matrix.simplified_msc3575` path. A user who turned "do not share read
receipts" on was shown as having read everything, everywhere: a privacy fault whose storage half was
never wrong.

What makes it worth writing down is where the rule already lived. `GET /v1/rooms/{roomId}/receipts`
filtered private receipts of other users correctly - so the server had the rule, written once, in a
handler, and the sync paths had never heard of it. A visibility rule that belongs to *who is asking*
is not a property of the stored row, and every reader of that row is a place the rule has to be
stated again. The fix threads the viewer into the one function that builds `m.receipt` ephemeral
events for both sync shapes, so there is a single place left to be wrong in.

Federation was already clean, and that was checked rather than assumed: the receipt EDU a peer is
sent is built only for `m.read` (`queueReceiptForPeers` returns early for any other type), so no
private receipt ever left this server. Worth recording because the next reader of this section will
wonder, and because the fault was never "the room was told", only "other members' syncs were told".

## Findings of 2026-11-24 — the field was stored and still never arrived, twice

The pusher fix was half done, and the reporter caught it: `device_display_name` came back as the
device id. Reading the write path again found two bugs, neither of which the first test could see.

**An update list is a field list.** `Set` writes with `ON CONFLICT … DO UPDATE SET` over an
explicit column list, and that list was written when the row had fewer fields: it named `app_name`,
`device_id`, `lang`, `data`, `active` and the timestamps, and no name column. A first registration
inserted the name; every registration afterwards - which is what a client doing its normal startup
does, and what a probe reusing a pushkey does - updated everything except the name. So the row kept
the name of whoever first claimed that pushkey and app id, and a row predating the column stayed
nameless forever, which the read-side fallback then dressed up as a device id. A storage layer that
takes a column list per call is a second place where the spec's field table has to be walked, and
"the field is in the struct" proves nothing about it.

**A pusher was not owned.** The unique scope was `(push_key, kind, app_id)` with no user, and the
same update list named `user_id`. Registering an endpoint another account already claimed therefore
updated that account's row and moved it - Alice's list went empty while Bob saw a pusher named after
her phone. Anyone who could guess a pushkey could take over a stranger's notifications. Synapse keys
a pusher by user, app id and pushkey; the scope now does too.

The repair for an existing database is not free: `AutoMigrate` adds a missing index and never
rewrites an existing one, so the model asking for a fourth column changes nothing on disk. A
`RepairPusherScope` step drops the old index and recreates it, following the `backfillStateFlags`
precedent. It is load-bearing rather than tidy - with the old index in place, the new
`ON CONFLICT` target matches no constraint and every `Set` fails - so the unit test writes a
database of the old shape and opens it as the field would.

The repair runs one way, and that is worth knowing before a release is rolled back. The previous
binary's `ON CONFLICT` target names the endpoint without an owner, which matches no constraint once
the wide index is in place, so after this version has opened a database nobody can register a pusher
on an old binary until the narrow index is put back by hand:

```sql
DROP INDEX IF EXISTS "uq_pusher_scope";
CREATE UNIQUE INDEX "uq_pusher_scope" ON "pushers" ("push_key", "kind", "app_id");
```

That statement fails when two accounts have meanwhile registered the same endpoint - which is the
point of the fix - so a rollback after that also means choosing which of the two rows survives.

And the habit the report should have taught the first time: the test inserted two pushers and read
them back, which exercises the insert path and nothing else. A write path with both an insert and an
update branch needs a case that arrives twice - and a resource with an owner needs a case where
somebody else asks for the same one.

## Findings of 2026-11-24 — a Required field the server never had

`GET /v3/pushers` answered every pusher without `device_display_name`, and the Matrix Rust SDK
refuses to read the list at all: "missing field `device_display_name`". Seven of the Pusher object's
fields are Required by the spec, and a strict reader does not degrade a missing one to unset — it
rejects the object, which here meant the client's whole push settings screen.

This is not the `omitempty` bug class and no amount of tag-reading would have caught it: the server
never accepted the field into storage. `POST /pushers/set` picked eight keys out of the body and left
that one on the floor, so the row had nothing to answer with. Two habits follow. Where a route
accepts a documented object, the storage shape should be walked against the spec's field table once,
and where a field is Required, its absence should be a test rather than a reading — the new
compatibility test asserts the presence of all seven fields on every pusher the list returns.

Because `POST /v3/pushers/set` and `GET /v3/pushers` are the same object on the way in and the way
out, the field is stored verbatim rather than derived from the device: a device that is renamed after
the fact should not rewrite a notification endpoint registered under the old name. The device is the
fallback, not the source — used when a client omitted the name, and equally when a row predates the
column, since `AutoMigrate` adds columns without filling them. The two genuinely optional fields
(`profile_tag`, `device_id`) stay absent rather than empty, so a client matching `device_id` against
its own session is not told that `""` is a device.

## Findings of 2026-11-24 — `limited` measured the room, and widened state was filtered out again

Two sliding sync faults from the same reporter, both in the delta machinery, both invisible in the
tests written before them because each needed a room with history in it.

**`limited` said true for every room deeper than its window.** The field was
`total > len(window) || (!initial && total > known.TimelineCount+countAfter(...))`, so the room's
size entered the judgement on every later response: one new message in a room with six events came
back `limited: true`, and a client that believes the field drops its timeline and goes paging - on
every message, forever. MSC4186 words it as "more events **since the previous sync** than were
included in the `timeline` field, or that the client should paginate to fetch more events", so: on
an update only the events that arrived and did not fit count (`slidingWindow` reports that beside
the window now), and on a first sight of a room the history behind the window is exactly what the
client is being told to go and fetch, so `total > len(window)` stays there. Measured: initial
`limited: true` in a room deeper than the window (unchanged, and right), `limited` absent after one
new message (it was `true`), `limited: true` again for five messages into a window of three.

**A subscription asking for state the connection had never seen got none.** The state delta skipped
every row older than the connection's position, which is correct for state the client holds and
wrong for state it only asked for this round: a room's `m.room.name`, `m.room.create` and
`m.room.topic` are older than almost any connection, so widening `required_state` - the normal way
a client opens a room it has been showing in a list - answered with nothing. MSC4186 lets the server
skip state the client "has previously seen", and this client had seen none of it.

The fix exposed a second bug on the way. The connection's record of shown state was **replaced** by
each response's sent set, so `slidingWasSent` really meant "shown in the last response". Sending old
state on that record would then repeat it every round, since the next round forgets the round before.
The record is cumulative now: what was sent, plus what was sent earlier and the room still stands
behind; keys whose state disappeared are dropped, which is what the stub half reads.

Worth keeping beside each other, because the two halves of `limited` pull opposite ways and only the
MSC settles them: it is deliberately `true` on a first sight of a deep room (the client must page),
while on an update it is deliberately `false` unless something was left out (the client must not). A
server that gets either half wrong is wrong in a way that costs a client a round trip per message.

## Findings of 2026-11-24 — an extension scoped to the rooms already reported reports nothing

Typing, receipts and room account data were unreachable in sliding sync, and the long poll waited
out its timeout while agreeing that nothing had happened. The scope of those three sections was
derived from the response: `slidingExtensionRooms` walked `response.Rooms`, and a room that has
nothing else to say has no entry in a response. So the sections were asked about an empty set,
answered empty, reported "no movement", and the poll slept. Measured: 5.003 s of waiting for a
typing notification that had been posted 5 ms earlier, and the same empty
`{"typing":{},"receipts":{}}` on a request that did not wait at all.

MSC4508 says the scope in so many words, and it is worth the sentence because it is the whole
difference between a feature and a dead branch: "a room being in scope does _not_ require the room
to have an entry in the `rooms` section of the _response_: a room within a list's range is in scope
even if the room has no other updates to send". The scope is the connection's window and its
subscriptions - which the server had already computed a dozen lines earlier as `plans`, and then
threw away for this question.

The fix was checked against the whole client-API suite, not only the new test: 83 green of 106, 2
skipped, the same 21 red names as the 2026-11-20 list — and one name that used to be on it.
`TestPushSync` passes since the push rule set began travelling as `m.push_rules` account data (the
v0.2.7 work); its first assertion reads exactly that, so the gain was a side effect of closing a
different gap and only showed up because the full package was run for once.

Two consequences the sections disagree about, both from the MSCs:

* typing and receipts stay with the rooms the user is **joined** to, as ephemeral events have
  always been, while MSC4186 deliberately keeps rooms the user left or was kicked from in the
  lists - reporting them would leak read activity from a room the client can no longer read;
* room account data answers whatever the membership is, because MSC4548 says a user can set it for
  an invite or a knock.

The `lists` and `rooms` keys are now read per extension as well. They used to be read once: the
first extension that carried a restriction quietly narrowed the other two.

**The shapes the report asked for are the old ones.** It cites MSC3961, MSC3960 and MSC3959, which
the accepted extension MSCs superseded, and lists the Synapse shapes (`m.typing` wrapped, an
`m.receipt` event per room). On `/v4/sync` the accepted text wants the bare values, which is what
this server answers and what that same reporter asked for two days earlier; the unstable path still
answers the wrapped form. One extra key survives on purpose: the typing value carries
`typing: true` beside `user_ids`, because that is the content of the `m.typing` event the section
is derived from, and the MSC's table lists `user_ids` without forbidding a neighbour.

## Findings of 2026-11-24 — push rules were never account data

The `/sync` answer had a `pushrules` field of its own, filled on the first sync only, and its own
comment promised that "changes arrive as `m.push_rules` account data". Nothing wrote such a row:
the rules live in their own table, and the account data section carried only what a client had put
there. A second device therefore never learned that the first had changed a rule, and a client
that reads its rules where MSC4548 says they are read nothing at all. MSC4010 (merged 2024-03-03)
is the authority for both halves: a change made through `/pushrules` "is sent to all clients via a
`m.push_rules` event in the `account_data` section in the next `/sync`", and that type — like
`m.fully_read` — is the server's to write.

The write half is not a nicety. Before MSC4010 a client could `PUT` its own `m.push_rules`, and
Synapse accepted it: the stored blob shadowed the real rule set in every sync, while the server
went on evaluating the rules the client was no longer being told about. Lattice accepted it too,
on the global and the room route alike. Both now answer the 405 the spec documents, with the
`M_BAD_JSON` errcode the spec pairs with that status — an odd pairing, and it is the spec's, not an
invention. Reading stays allowed, and the read marker keeps writing its own `m.fully_read` through
the service, which is why the guard belongs at the HTTP edge and not in the service.

The rule set is a projection, rewritten whole by every mutator: the table stays the thing that
decides whether an event notifies, and clients replace the content they hold for a type, so a
partial entry would leave each of them merging rule lists.

**What it moved, measured.** `TestPushRuleRoomUpgrade` in Complement used to fail because the
section it reads did not exist. It now reads the `m.push_rules` entry and gets past that, and fails
one step further on: after an upgrade it waits for a `room` rule naming the new room id, and
Lattice keeps only the old id. The red is now the missing push-rule transfer of MSC2313, not a
missing sync section — a smaller claim, and a different queue item.

**A number worth not trusting.** The account data extension is MSC4548. An earlier note in
`docs/TODO.md` guessed MSC4537, which turned out to be a profile-API MSC. Every extension in this
family has an MSC of its own now (4508 common and typing, 4538 to device, 4543 keys, 4548 account
data, 4551 receipts), so read the number rather than remembering the family.

## Findings of 2026-11-23 — a null where the spec asks for a list

The same Go pattern that erased `state_key` erased a whole encryption section. MSC4543's table
marks `device_unused_fallback_key_types` Required, type `[string]`, and writes that "an empty list
means the device has no unused fallback key" — so the answer is `[]` when there is none. Lattice
built it from a nil `[]string`, which `encoding/json` renders as `null`, and left `omitempty` on
the classic `/sync` field, which erases the empty list instead. Measured on v0.2.5: both sliding
sync paths answered `"device_unused_fallback_key_types":null`.

The blast radius is the reason a null is not a detail. matrix-rust-sdk decodes the `e2ee` section
as one structure, so refusing it also discards `device_one_time_keys_count` and the device lists —
and on the same connection, `to_device` stops being processed by the crypto layer, which is how a
client ends up never uploading keys. The reporter's own mending ("smaltino fixes it after the
fact") is a workaround, not a fix.

**The rule this leaves behind:** a `Required` field that is a list or a map needs an empty one,
never a nil. `omitempty` on a slice or map is only ever right for an *optional* field, and a nil
map marshals as `null` with or without it. Where a field is genuinely optional the repo already
uses `*string`/`*int` (`matrix/event.go`, `matrix/sliding.go`), so absent and empty stay
distinguishable; the same applies to `*[]string` if an optional list ever has to be answered.

### What MSC4538 adds, and what it does not

The to-device extension was reported alongside it (fault F2) and both halves check out against the
accepted text. Its table requires `next_batch` and `messages`; changelog point 3 counts returning
them under `events` as the Synapse v1.151 shape; its unstable-prefix section leaves that shape to
the unstable URL and asks for `messages` on the stable one. Lattice answered `events` on both.
Changelog point 1 is the one that loses data quietly: a `limit` that is not a positive integer must
be refused with `M_BAD_JSON`, while Synapse answered a zero with an empty section whose
`next_batch` acknowledged everything queued — so the client deletes messages it never received.
A `ToDeviceMessage` names `type`, `sender` and `content` in that table and nothing else, so the
sliding answer names exactly those three; it used to carry `origin_server_ts` beside them.

### What the report got wrong, in Lattice's favour

`device_one_time_keys_count` listing `curve25519: 0` and `fallback_curve25519: 0` for algorithms
the device never uploaded is not a fault. MSC4543: "A server MAY omit an algorithm whose count is
`0`… An explicit `0` and an unlisted algorithm are equivalent." Sending the zero is the safer of
the two, because that MSC's own changelog point 3 records matrix-rust-sdk treating an *unlisted*
algorithm as "unchanged" rather than zero — a client-side bug that an explicit zero survives.

### Earning the extension flags

MSC4508 asks a server to advertise each extension it answers as specified, under the `.stable`
flag, without waiting for the spec revision that carries it. `org.matrix.msc4543.stable` and
`org.matrix.msc4538.stable` are advertised from this commit, and the second is only true because
of it: before, the stable path answered the Synapse shape and the flag would have been a claim
the server could not honour.

### The two shapes that answer to it

The reporter was asked, and answered the same evening: smaltino reads `messages` on `/v4/sync`,
and only falls back to `events` when the strict read fails — logging it as a fault of the server.
That is the measurement the switch needed, so `typing` and `receipts` moved with it. MSC4508 wants
a room as the bare `{"user_ids": […]}` and MSC4551 as the bare `content` of an `m.receipt`, both
MSCs arguing that the wrapper adds nothing when the section can carry only that one thing, and both
leaving the wrapped event to the unstable URL. `org.matrix.msc4508.stable` and
`org.matrix.msc4551.stable` are advertised with it. A helper now makes that one decision once, for
both sections, from the URL the request arrived on.

What MSC4551 still asks for and Lattice does not do is its own item: its "Which receipts" rules
want a room's *initial* receipts — the receipts on the events of this response's timeline, plus the
requesting user's own receipts of every type, always — and receipts again whenever a room's entry
carries `expanded_timeline`. Lattice sends what changed since the request's `pos`, which misses the
own-receipts half a client needs for unread markers. Queue item A.

## Findings of 2026-11-23 — an invited room that arrives without a name

Reported against the live `v0.2.4` by a client author, and both halves are true.

- `StrippedStateEvent.StateKey` carried `omitempty`, and for `m.room.create`, `m.room.name` and
  `m.room.join_rules` the state key **is** the empty string — so the one field the spec marks
  **Required** disappeared for exactly the three events that name a room. Measured on all three
  surfaces before the fix: `/sync`'s `invite_state`, and sliding sync's `stripped_state` on both
  `/v4/sync` and the `org.matrix.simplified_msc3575` path. A lenient reader treats the event as
  unnamed state; ruma's deserialiser rejects it and drops it, which is how "never sees an
  invitation, and no room name even on `/v4/sync`" looked from the outside.
- Absent is not empty, and this is a class rather than a line: `omitempty` on a `string` field
  whose legal value is `""` is a wire bug. Where the field really is optional, the type is a
  `*string` (`internal/matrix/event.go:85`, and the `required_state` element in
  `internal/matrix/sliding.go:194`) — that shape says "not given" and "empty" differently, and
  keeps the empty value. Grep for `json:"state_key,omitempty"` before trusting another struct.
- The `/versions` half was also real, though not as the report guessed: MSC4186 names one flag,
  `org.matrix.simplified_msc3575`, and says nothing about a stable one. But matrix-rust-sdk
  (`crates/matrix-sdk/src/sliding_sync/client.rs`) reads two — `FeatureFlag::Msc4186` selects
  its `Version::Unstable` (the unstable URL), `FeatureFlag::Msc4186Stable` selects
  `Version::Stable` (`POST /v4/sync`), and its `Discover` builder prefers stable when both are
  advertised. Lattice serves the merged MSC on both paths, so it now advertises both flags and a
  discovering client lands on `/v4/sync`.
- Stock matrix-rust-sdk 0.19 still ignores `/versions` for this and posts the unstable URL with
  the pre-MSC field names — that is what its author described on 2026-11-22, and it is what
  Element X does today. It is no longer true of smaltino: as of 2026-11-23 it ships its own
  copy of the SDK with MSC4186-v4 support on top, so it does read `/versions`, and with
  `org.matrix.simplified_msc3575.stable` present it picks `POST /_matrix/client/v4/sync` and
  converts `stripped_state` to `invite_state` itself. Corrected by that author the same day,
  against my claim that no flag would move it: with the `state_key` fix, that flag is enough
  for smaltino. What remains for stock 0.19 clients is the open TODO item — `invite_state` on
  the unstable path, behind a config name — and it now has a clear trigger: a real user on
  Element X against Lattice.

## De zoekendialoog: dezelfde endpoint, twee protocollen

`POST /search` was gebouwd op een ontwerp dat nergens buiten deze repository bestaat:
de termen werden onder `search_key.term` gelezen en het antwoord droeg `hits`,
`limits` en `groupings`. Gemeten met twee bodies op een verse server: de
gepubliceerde vorm `{"search_categories":{"room_events":{"search_term":"kiln"}}}`
werd `400 M_INVALID_PARAM "at least one search term is required"`, terwijl dezelfde
vraag met `search_key` `200` gaf. Geen enkele cliënt die de specificatie volgt kon
dus zoeken, en de tests van dit ontwerp verborgen dat. De ontwerpvorm is
verwijderd; wat overblijft is de specificatie.

Het body-lezen is nu: `search_term` (teken of lijst), `escape_regex`, `filter`
(`rooms`, `not_rooms`, `types`, `not_types`, `senders`, `not_senders`), `keys`,
`order_by` (`rank` of `recent`), `limit` en `next_batch`. Het antwoord is
`search_categories.room_events` met `count`, `highlights`, `results` (`event_id`
en `rank`) en `next_batch`. Wat er buiten valt: `include_event` en `groupings` worden aangenomen en doen
niets, en `groups` is geen categorie die deze server dient.

- **`keys` is een veldaanvraag, geen hint.** Een `content.<veld>`-sleutel wordt in
  SQLite met `json_extract` gelezen, achter `json_valid` — een rij waarvan de
  inhoud geen JSON is, kan niet matching. Een sleutel die nergens op slaat (of een
  ongevalige pad) beperkt niets; iets dat niets oplevert is erger dan iets breeds.
- **Zonder tekstindex is `rank` een tel.** Er wordt geteld hoe vaak de termen in
  een event voorkomen, bij gelijke stand wint de nieuwste; de `rank` daalt verder
  netjes over de pagina. Dat is geen bm25 en het doet ook niet alsof.
- **`count` telt hooguit het scanvenster.** De zichtbaarheid van een event valt
  pas ná de query weg (hieronder), dus een teller in SQL zou matches beloven die
  de lezer nooit krijgt. Er worden maximaal `searchScanLimit` nieuwste matches
  gelezen; Synapse hanteert dezelfde soort plafond.
- **Zoeken is een lees van dezelfde geschiedenis als `/messages`.** MSC2675 geldt
  ook hier: een kamer die zijn historie op de join opent, houdt events van vóór
  die join voor een zoeker verborgen. De oude platte weg filterde daarmee, de
  nieuwe categorie-weg liet ze doorlekken — de zichtbaarheidstest die dat
  controleerde, las het verkeerde veld en zag dus niets. Nu valt de poort vóór
  telling en pagina.
- **Ruimtenamen en -onderwerpen worden meegezocht.** Een `m.room.name` is een
  event; Synapse indexeert namen en onderwerpen, dus deze server doet hetzelfde.
- **`escape_regex` verandert niets.** Deze server matcht altijd letterlijk, dus
  `escape_regex: true` is precies wat er al gebeurt en `false` belooft een regex
  die er niet is.
- **Een term is een reeks woorden, geen zin.** `Message 4` zoekt het woord
  `Message` én het woord `4`, dus „Message number 4" vindt het; als één tekenreeks
  gematcht zou worden, niet. Complement (`TestSearch`) stuurt precies die vraag, en
  de server die woorden apart nam, werd `next_batch` kwijt omdat er niets vond.
- **`limit` thuishoort in `filter`.** Een platte `limit` wordt ook gelezen, want
  daar keek een oudere lezer, maar de specificatie zet hem in de filter.
- **`next_batch` wordt uit beide handen gegeven.** De body is de specificatie, de
  querystring is wat Synapse accepteert en wat clients sturen; de body wint. Een
  volle pagina belooft een volgende, ook als die leeg terugkomt — dat is wat een
  cliënt die tot het einde blaadt verwacht.
- **Een resultaat is het event zelf.** `results[i].result` draagt `type`, `content`,
  `sender`, `event_id`, `origin_server_ts` en `room_id`. Een id alleen dwingt elke
  cliënt om op te halen wat hij net verteld werd.
- **`event_context` levert de buren.** `before_limit` en `after_limit` (niet
  `before`/`after`) geven `context.events_before` en `context.events_after`, elk
  beginnend naast het resultaat en weggroeiend. Wat een kamer voor deze lezer
  verbhoudt, blijft verborgen — de vensters gaan door dezelfde poort.

Complement's `TestSearch` is daarmee groen, gemeten 2026-11-25: zes subtests, van
 Zoeken bij body tot terugbladeren en context. Wat rest is kwaliteit, geen vorm:
stammen en echte rangschikking (zie `docs/TODO.md`).

## Een sleutel-backup-veld op de verkeerde diepte

`first_known_index` stond in het antwoord van `GET /room_keys/keys/{roomId}` én in
elke kamervermelding van `GET /room_keys/keys`. Het veld hoort bij één sessie,
naast `forwarded_count`, `is_verified` en `session_data`; ruma's `RoomKeyBackup`
is niets dan `sessions`. Beide kamervormen geven het veld niet meer, per sessie
blijven beide spellingen (`first_known_index` en `first_message_index`) staan.

## De teller loopt voor op de kast

Een sync-beantwoordde tot v0.2.13 vanaf `StreamPos()` — de teller die posities
uitdeelt. Die teller wordt verhoogd **voordat** de rij die de positie draagt is
opgeslagen (`s.nextStreamPos()` staat als argument bij de write), dus er is een
venster waarin een positie bestaat die niets beschrijft. Een lezer die in dat
venster de teller leest en daarna de tabellen doorloopt, ziet de verandering niet
en antwoordt toch vanaf die positie. Het volgende token begint erachter en de
verandering is **definitief** kwijt.

Gemeten met een schrijver en een lezer in dezelfde proces: `bound 8 las 0
veranderingen, een ronde later leest dezelfde bound 1`. Niet zeldzaam, niet een
race die toeval vraagt — het venster zit in elke write.

Waarom een long poll het als eerste vangt: hij leest op het moment dat hij wakker
wordt, dus direct na de write die hem wekte. Waarom account data het hardst
aankomt: daar staat één rij per (gebruiker, kamer, type) met één `stream_pos` die
elke schrijver overschrijft, dus wat gemist is, is weg; events stapelen, en een
verlopen event duwt alleen de volgende voorbehoeglijk op.

De regel die het sluit: **een antwoord komt uit de kast, nooit uit de teller.**
`committedStreamPos()` leest de hoogste positie waar de database een rij voor heeft
(`LatestStreamPos`, over al tien tabellen met een `stream_pos`-index) en maakt die
antwoordpositie monotoon, zodat het vergeten van een kamer — dat rijen wist en met
die rijen de hoogste positie — een client nooit een token achter de zijne geeft.
De uitdelende teller blijft wat hij was: de verdeling van posities onder
schrijvers onderling.

- Fout op `LatestStreamPos` (schema in wording) geeft het laatst bekende antwoord
  terug, niet de teller: een dubbel gelezen verandering is onschuldig, een
  overslagen verandering is dat niet.
- Kosten: tien index-queries per antwoord. De compat-suite ging van 90,0 s naar
  91,6 s — 1,8% voor het behoud van elke verandering.
- De proef die het vasthoudt (`TestSyncAnswersFromAStoredPosition`) laat een write
  mislukken *na* de positie-anneming: de teller loopt, er is niets opgeslagen, en
  geen enkel antwoord — klassieke `/sync`, `/v4/sync` en het unstable-pad — mag de
  nieuwe positie noemen. Zonder de fix wijzen alle drie af.

## Het moment nul (2026-11-25, v0.2.15)

Gemeld door de schrijver van smaltino: `GET
/_matrix/client/v1/rooms/{roomId}/timestamp_to_event?ts=0&dir=f` antwoordde `400
M_INVALID_PARAM`, terwijl `ts=1` een normaal antwoord gaf. In het menu bij een
dagregel staat "Het begin", en dat vraagt om `ts=0&dir=f` — de client vroeg dus
om het eerste event van de kamer en kreeg een foutmelding.

De specificatie noemt `ts` een verplicht whole number, "the timestamp to search
from, as given in milliseconds since the Unix epoch", en zet er geen ondergrens
bij. 0 is de epoch zelf: een geldig moment dat vóór alles in een kamer ligt. De
melder mat Synapse en asaroton: die antwoorden `200` met het oudste event op
`dir=f` en `404 M_NOT_FOUND` op `dir=b`.

Het stond op drie plekken, allemaal als `ts <= 0`: in de zoekfunctie
(`EventByTimestamp`), in de federatie-antwoordfunctie (`ServeTimestampToEvent`)
en nergens in de HTTP-laag, want daar wordt alleen geparseerd. De grens had
`< 0` kunnen zijn, maar dan was het onderscheid tussen "geen getal" en "nul" er
nog niet geweest, en dat onderscheid is de hele fout.

De regel die het sluit: **een veld dat ontbreekt is niet hetzelfde als een veld
dat nul is.** Daarom leest `numberFieldOK` het getál, en zegt het óf er één was;
`numberField` blijft zoals het was (0 bij afwezigheid), want voor de andere
oproepers is afwezig inderdaad nul.

- De federatiekant van MSC3030 had dezelfde grens; die is op dezelfde manier
  verruild voor "ontbreekt het veld, dan is het een slecht verzoek".
- Aangenomen, niet gemeten: `ts < 0` mag ook, want de specificatie noemt geen
  grens en Synapse parseert een kaal getal. ruma kan geen negatief moment
  versturen (`MilliSecondsSinceUnixEpoch` is een unsigned type), dus geen enkele
  cliënt raakt dat spoor. De compat-test documenteert het gedrag van deze server
  zonder het als een eis van de specificatie te verkopen.
- Twee proeven zijn ladend: met `ts <= 0` terug in het bestand vallen
  `TestTimestampToEvent` en `TestAPeerCanNameAnEventByTimestamp` elk op precies één zaak — de epoch.

## Het eerste antwoord van een uitwisseling is een vraag (2026-11-25, v0.2.16)

Gemeld door de schrijver van smaltino: `POST /_matrix/client/v3/account/password`
zonder `auth` gaf `403 M_FORBIDDEN "the old password was incorrect"`. De client stuurt
dat kale verzoek eerst — het is stap één van de User-Interactive Auth — en vraagt ná
een `401` pas aan de persoon om het oude wachtwoord. Op een `403` stopt hij, met de
melding dat zijn eigen wachtwoord onjuist zou zijn terwijl er nog niets was ingevuld.

De specificatie is hier duidelijk: "This API endpoint uses the User-Interactive
Authentication API". Het antwoord op een verzoek zonder bewijs is daarom de
uitdaging: `401` met `flows`, `params` en een `session`.

Het oude lichaam van `ChangePassword` kende drie takken. Met `old_password` werd dat
wachtwoord vergeleken, mét `auth` ging het naar de uitwisseling (die wél `401`
antwoordde), en zonder allebei kwam er `403` — precies de tak waar de cliënt begint.

De regel die het sluit: **op een UIA-endpoint is een ontbrekend bewijs geen oordeel,
maar een vraag.** Alle afwijzingen van dit endpoint zijn nu uitdagingen. Daarvoor is
`authorizePasswordStage` in `internal/service/uia.go`, losgesneden uit de
uitwisseling van de cross-signing-upload, zodat beide dezelfde body geven: `401`,
`flows` met `m.login.password`, een leeg `params`-object, een `session` en `completed`.

- Gemeten voor de fix: `403` zonder `auth`, `200` met `auth`. Na de fix: `401` met
  een volledige uitdaging, enook een `401` als het ingevulde wachtwoord fout is.
- Twee builders van een uitdaging in dit huis: `adminAuthChallenge` (apparaat
  verwijderen, account deactiveren) zet `M_FORBIDDEN` in de `401`, `authChallenge`
  (registratie, cross-signing, sinds nu wachtwoord) zet `M_UNAUTHORIZED`. Synapse
  vult ook een errcode in; ruma leest alleen `flows`, `params` en `session`, dus geen
  cliënt struikelt erover. Het verschil is bekend en blijft staan tot er een pas
  komt die het voor allebei oplost.
- `old_password` is deprecated maar wordt nog steeds gestuurd en telt dus mee. Een
  verkeerde `old_password` is nu ook een uitdaging in plaats van een `403`.
- De `session` is een kaart die de cliënt teruggeeft; de bewijzing wordt uit het
  lichaam gecontroleerd en niet uit de zitting, net als op de andere eindpunten.
- `TestChangePasswordInteractiveAuth` speelt de hele wisseling: de uitdaging veld voor
  veld, een fout wachtwoord, het goede wachtwoord door de `session`, inloggen met het
  nieuwe, afwijzen van het oude, en de eigen device die binnen `logout_devices: false`
  blijft werken. Zonder de fix valt hij op het kale verzoek.

## An answer carries what its schema names (2026-11-25, v0.2.18)

Reported by the author of smaltino: `POST /_matrix/client/v3/account/deactivate` did
the work — the account was gone — and answered
`{"3pids_disconnected_json": "[]", "id_server": null, "unbind_credentials": {…}}`.
The matrix-rust-sdk deserialises that into a response type it cannot fill, so the
client reported a failed deletion for a deletion that had succeeded.

`account_deactivation.yaml` names one required field, `id_server_unbind_result`, and
settles its value in the same breath: `no-support` means "one or more identifiers
failed to unbind", and it "must be `success` if the homeserver has no identifiers to
unbind for the user". Lattice stores no identifiers bound to an identity server, so
there is never one that failed: the honest answer and the required answer are the same
word, `success`. Measured by the reporter on an account without 3PIDs: Synapse answers
`success`, asaroton `no-support` — asaroton deviates here as well, and ruma parses both.

The rule that closes this: **an answer carries what its schema names, no more and no
less.** Extra fields cost nothing to a strict client and nothing to a lenient one,
which is exactly why they survive; a missing required field ends the exchange. So the
three keys that came from copying a Synapse answer — whose identity-server plumbing this
server does not have — are gone.

- Found at the same time: `/account/password`, `/logout` and `/logout/all` answered
  `id_server_unbind_result: "no-support"`, while all three schemas name no fields and
  the worked example is `{}`. Those answers are empty objects now. `/logout` is where
  that key legitimately comes from: an older Synapse answer for logout carried it.
- `TestAccountAnswersCarryWhatTheSchemaNames` checks the three bodies, four assertions
  in all; on the old answers all four fail. No test had looked at any of these bodies
  before, which is how an answer can be wrong for this long.

## A default rule is a row, so somebody has to hand it to the accounts that already exist (2026-11-26, v0.2.19)

Reported by the author of smaltino: `GET /pushrules` listed eight override rules, the
specification this server advertises (`/versions` ends at v1.11) defines twelve, and
`PUT /pushrules/global/override/.m.rule.is_room_mention/enabled` answered
`404 M_NOT_FOUND` — the switch for "notify me when the room is mentioned" failed with an
error message on every account, and their core suite failed on the mention that arrived
without its highlight.

The rule that generalises is not about push rules: **a default that is stored once is a
promise about the future only.** Synapse computes its default rules at read time and
therefore cannot be behind; Lattice seeds rows at registration, so a rule added in 2026
is missing on every account made before 2026 and present on every account made after —
which is exactly the half a suite that registers cannot see. Fixed by
`RepairDefaultPushRules`, run from `service.New` beside the signing key, and gated on
`User.PushRulesVersion` rather than run every start: an absent row for a default rule
cannot be told apart from a default rule a person deleted, and `DELETE` of a default rule
is allowed in v1.11 (its responses are 200 and 404, and the old 400 for removing a default
rule is gone). One promise per account per rule set is the honest shape; the repair also
republishes the `m.push_rules` account data, because that copy is what MSC4010 tells
clients to read.

Four things worth not re-deriving about the rules themselves:

- **A property path escapes its dots.** The keys are `content.m\.mentions.room` and
  `content.m\.relates_to.rel_type`: a backslash escapes the dot (or backslash) after it,
  other escape sequences stay as they came, and a plain `content.m.mentions.room` walks
  into an object called `m`. That is the appendices, "Dot-separated property paths", and
  `push.PropertyValue` is the one place that reads it — `event_match`,
  `event_property_is` and `event_property_contains` share it.
- **`event_property_is` compares JSON values, not strings.** `"true"`, `1` and `true` are
  three answers, and only string / integer / boolean / null compare at all; a property
  that is absent, or is an object or an array, never matches. `event_property_contains`
  needs an array and matches its entries the same exact way, so `":example.com"` is not in
  `["@bob:example.com"]`.
- **`sender_notification_permission` is a power-level read.** The key names an entry of the
  `notifications` object of the power levels event, and `notifications.room` — the level an
  `@room` mention asks for — defaults to **50**, so an ordinary member (0) cannot wake a
  room. `power.ActionRequired` does not know that key, so the fallback lives in
  `push.DefaultNotificationLevel` and the service hands the pair
  (`senderPower`) to the matcher once per event rather than once per recipient.
- **Two of the four rules can never fire on this server, and that is fine.**
  `.m.rule.room.server_acl` is a state event and `notifyParticipants` reads
  `m.room.message`, `m.room.encrypted` and `m.sticker`; the rule is seeded so the client's
  list is the specification's list. `.m.rule.suppress_edits` does fire, and it is the one
  whose absence is heard: an edit used to notify like the message it replaced.

Measured over two binaries with one script (`.agent-work/v111/probe.py`, output in
`before.txt` and `after.txt`): 8 rules → 12, `404` → `200`, a mention of a Matrix ID that
only notified → highlighted, an `@room` mention from a room administrator → highlighted,
an edit → not heard at all.

- Left deliberately, recorded in the queue: `.m.rule.member_event`, `.m.rule.reaction` and
  `.m.rule.notice` are still recognised by identifier in `Matches()` rather than by stored
  conditions, so they publish empty condition lists; `.m.rule.master` is still read as
  "never matches", where the specification has it match everything with an empty action
  list — which is what makes it an off switch; `.m.rule.roomnotif` keeps its `dont_notify`
  actions. All three are v1.17 work, and seeding a rule whose condition the matcher cannot
  read would trade a 404 for a silent no-op — the worse of the two.

## A field your own client ignores is a field a stricter one refuses the whole document over (2026-11-26, v0.2.20)

Reported from the smaltino lab run of v0.2.19: a second device of an account did not show
the push-rule changes the first one made. The data was right everywhere the server could
see — `m.push_rules` in the account data, and in both kinds of sync. The answer for the
content kind was the cause:

```json
"content": [{"rule_id": ".m.rule.contains_user_name", "default": true, "enabled": true,
             "actions": ["notify", {"set_tweak": "highlight", "value": true}]}]
```

`.m.rule.contains_user_name` was seeded with no conditions, and `pushRuleWire` reads a
content rule's `pattern` out of a stored `content.body` condition, so nothing was published.
Ruma — the Rust SDK under smaltino and Element X — answers that with `missing field
'pattern'` for the **whole** ruleset and falls back to its own defaults, so every device on
the app reads the server's rules as though the person had never touched them.

- **The rule: a published shape is judged by its strictest reader, not by yours.** A field
  the kind always has is a field to publish even when there is nothing in it. The content
  rules now always carry `pattern` — an account's own local part where the specification
  substitutes one, and an empty string for a rule a person wrote without one — because an
  empty string is a value and an absent field is a parse error.
- **The same field then turned up where it does not belong.** Any `content.body` condition
  was enough for `pushRuleWire` to publish a `pattern`, so the *override* rule
  `.m.rule.roomnotif` carried one beside its conditions. A field derived from a shape has to
  be derived with the kind in hand: `pattern` for content rules, `conditions` for the rest.
- **`PUT /pushrules/{kind}/{ruleId}` is a replace, and the body names three fields: actions,
  conditions, pattern.** Everything else the row held was being read as empty, so a settings
  screen changing one default rule's actions cleared its conditions — turning
  `.m.rule.suppress_notices` into a rule matching every event — and cleared its `default`
  flag, making a rule of the server a rule of the person. A replace has to carry over every
  field the body cannot name: position in the rule order, `default`, and the conditions of a
  default rule.
- That last fact is also what makes the repair pass safe. For a default rule, the person owns
  `enabled` and `actions`; the server owns `conditions`, `default` and the order. Only
  server-owned fields may be rewritten by a repair, and here that list is exactly the set of
  fields no endpoint writes, so the pass cannot undo a choice anybody made.
- Two smaller ones, both already in the matcher rather than in the report: the condition kind
  `contains_display_name` was implemented before it was ever seeded, so a person copying the
  published condition into a rule of their own gets one that fires; and `WordContains`
  lowercases its haystack and not its needle, which every caller has to remember.

Measured by running one test file against both releases (a `v0.2.19` worktree beside the
fix): twelve assertions fail there — no `pattern`, no sound tweak, a `pattern` on an
override rule, a default rule stripped of conditions and flag by a `PUT` of actions, an
account written before the change still answering without a `pattern`, and its
`m.push_rules` account data missing it too.

- Left in the queue beside it: the rules whose conditions this server still hides in
  `Matches()` instead of storing, and the `dont_notify` actions `.m.rule.roomnotif` was
  seeded with where v1.11 gives it a notify and a highlight.

## Rules the server does not evaluate still have to be right, because a client evaluates them (2026-11-26, v0.2.21)

The release before this one made the push rule set readable. That was right, and it turned a
latent wrongness into an outage: the smaltino lab run came back with 52 of 85 instead of 62, and
no notification reaching any Rust client. Ruma had been ignoring Lattice's rules and using its
own defaults; now it read them, and three override rules were published **with no conditions** —
`.m.rule.member_event`, `.m.rule.reaction`, and `.m.rule.invite_for_me` with a `member_count`
condition that is not in the specification. The specification says a rule without conditions
always matches, so the third rule of twelve, carrying an empty action list, matched every event.

Evaluating the published answer with the specification's own algorithm, on the two binaries
(`.agent-work/v111/rules-eval.py`), said it in five lines: every one of a message, an encrypted
message, an invite for the recipient, an invite for somebody else and a reaction ended on
`.m.rule.member_event` with `dont_notify` on v0.2.20, and on `.m.rule.message`,
`.m.rule.encrypted`, `.m.rule.invite_for_me`, `.m.rule.member_event` and `.m.rule.reaction` —
with the actions the specification defines — on v0.2.21.

- **A published rule set is an interface, not a debug view.** Half of these rules are never
  evaluated by this server at all, because `notifyParticipants` reads `m.room.message`,
  `m.room.encrypted` and `m.sticker`. That is no excuse: the client that walks the rules is the
  other half of the feature, and it judges the published JSON.
- **Divergences between a lenient producer and a strict consumer hide until the consumer
  catches up.** This server reads "no conditions" as matching only for underride rules; the
  specification has it match everything. So the silence was invisible here and total there, and
  every test of this repository passed while it shipped. The divergence stays — it is now a row in
  `TestRuleMatches`, and the invariant that would have caught the shipping is in the compatibility
  suite: **no enabled override rule is published without conditions.**
- **Making a document readable is a risky change on its own.** It hands authority over to whatever
  the document actually says. A review that only asks "can the client parse this now?" misses the
  question "what does it now obey?" — which was the whole incident.
- Two of the three rules were also matched by identifier in `Matches()` rather than by their
  conditions. That was the reason they had no conditions to publish, and it is the general shape of
  the bug: behaviour kept in code publishes a rule with an empty body, and only a client that reads
  the body can be harmed by it. Seeded now with `type` = `m.room.member` and `type` = `m.reaction`
  behind the specification's empty action list, the identifier branch is gone. `.m.rule.notice` is
  the last rule left in that shape, and it is in no ruleset of the specification at all.
- An account's own Matrix ID appears in a stored condition twice now — `m.mentions` for a user
  mention, `state_key` for an invite — which is the only way a rule that is a row can say "me".
- The repair promise got its second half. A default rule's conditions belong to the server and are
  taken back whenever the set moves; its actions belong to the person and are taken back only while
  the row still holds exactly what the server seeded for it, which is the one case in which the
  server can tell that nobody chose them.

## Compare the whole published set with the specification once, instead of one rule per report (2026-11-26, v0.2.22)

A third client report in a row about push rules — a room of two announcing name changes,
an encrypted message for somebody else waking the phone — and the third time the answer was
a rule whose conditions were not the specification's. So this time the published answer was
diffed against the specification as a whole: `.agent-work/v111/spec-audit.py` reads the JSON
definitions out of the "Predefined Rules" section and compares conditions and actions with
`GET /pushrules`. Eight deviations came back. Two had been reported. Six had not.

- `.m.rule.room_one_to_one` named no event type, so in a room of two it matched a name change,
  a topic change and somebody else's call. `.m.rule.message` matched `content.msgtype` `m.*`
  where v1.11 matches `type` = `m.room.message`. `.m.rule.encrypted` and `.m.rule.message`
  carried sound and count tweaks the specification's `notify` does not. `.m.rule.roomnotif`
  was missing its `sender_notification_permission` condition and its notify-plus-highlight
  actions. `.m.rule.tombstone` was seeded `dont_notify` where the definition highlights.
  `.m.rule.suppress_notices` carried `dont_notify` where the definition prints `[]`, and
  `.m.rule.master` published no `conditions` field where the definition prints `[]`.
- **A report is a sample, and a sample finds one defect at a time.** The unit of work was
  wrong: not "the rule a client tripped on" but "the set a client reads". Both new tools are
  general — the audit for the whole set, `.agent-work/v111/rules-eval.py` to walk a published
  answer with the specification's algorithm and print the rule each event ends on — and the
  comparison now lives in the suite as `TestPublishedDefaultRulesAreTheSpecificationsDefinitions`,
  its table emitted from the specification's own JSON rather than retyped.
- **Publishing an empty list and publishing nothing are different answers.** `omitempty` on a
  slice cannot tell them apart, and the specification needs both: `[]` for `.m.rule.master`,
  nothing at all for a content rule. `PushRuleWire` carries a pointer for `conditions` now, as
  it already did for `pattern`, for that one reason.
- **The specification is silent about which end of an underride list wins.** This server takes
  the first matching rule of a kind, Synapse keeps the last, and ruma agrees with this server —
  which is why the room's evaluation and Lattice's agree on a DM message landing on
  `.m.rule.room_one_to_one` while a Synapse-shaped reader would land it on `.m.rule.message`.
  Recorded, not changed: a rule set that two of three readers agree on is not the place to
  start an argument.
- Some of it lands on the server itself, and that is the tell that the data was wrong rather
  than cosmetic: a group room's messages lose their seeded sound, and an `@room` in a body from
  a moderator now highlights the way `m.mentions.room` does.
- And one thing I got wrong while writing this up, corrected the same day by the report: a failing
  test called `woken_for_a_ring` led me to conclude it sent an `m.call.invite`, that the rule set
  therefore could not tell whom a call was for, and that the fix belonged to the client. The test
  sends `org.matrix.msc4075.rtc.notification`, which carries an `m.mentions` property, and it failed
  for the same reason as its neighbours — `.m.rule.room_one_to_one` without its `type` condition —
  so it passes on v0.2.22 and `.m.rule.call` is the specification's own. A test's name is not its
  request: reading the event it sends costs a minute and a wrong conclusion costs a release note.

## An endpoint that stores a URL is not a server that uses it (2026-11-27, v0.2.23)

The push pipeline had every part except the one that leaves the process. The pusher endpoint
accepted an endpoint, `GET /pushers` listed it back with `active: true` and the right
`data.url`, the rules decided that a message in a room of two is worth a wake-up, and a
notification row was written for `/notifications`. Nothing ever made a request to the URL. The
lab counted 0 requests at their gateway in 30 seconds, and their summary of what that means for
a user is the whole finding: an app that is not open gets nothing, on any platform, from this
server.

- **A coverage table cannot find this.** Routes are counted as implemented the moment they answer
  correctly, and every route in this story did. The missing half is not an endpoint — it is this
  server being a *client*, and no endpoint index has a column for that. What found it was a peer
  that counts arrivals, which is the honest form of "does the feature work": something outside
  the process noticing.
- **Asynchronous is not an optimisation here.** The request is rendered while the event is still
  being sent and handed to a queue; a worker makes the call. A push gateway is somebody else's
  server, and a phone's notification endpoint must not be able to make an accepted message fail
  — the same argument the federation outbox is built on. The queue is bounded at 256 and a full
  one drops out loud, because a notification row already exists: dropping costs a wake-up, not a
  message, and the next sync stays complete either way.
- **The body is grouped per recipient, not per device.** `counts`, `unread` and `prio` describe
  the person and the room, so devices that share a gateway *and* a format ride in one request's
  `devices[]`, each with its own `tweaks`. One request per device would repeat numbers that
  cannot differ per device; one request per gateway across recipients would publish one person's
  unread count to another person's phone.
- **A gateway's answer is an instruction, and the cases differ.** A `rejected` list or a 400 says
  the pushkey is done — forget the pusher, or every later message queues a request that is refused
  again for a device that no longer exists. A 404 says the URL is wrong, which is the client's
  mistake and says nothing about the pushkey, so the pushers stay. 429, 408, 5xx and unreachable
  are retried three times with doubling backoff and jitter, and giving up writes a line to the
  log: silence is what made "we never called it" indistinguishable from "nobody wanted us to".
- **What is still missing, said plainly.** A membership event is still not judged against the
  rules, so an invite reaches no gateway; that decision is about who the recipients are, since the
  invited person is not in the room to be listed. And this loop dials a URL any account may
  register, with no address policy in front of it — so the honest answer to the lab's question
  "does Lattice refuse local addresses, and is there a knob to allow one?" is that it refuses
  nothing yet. That knob belongs to the queued outbound address policy, not to a pusher-specific
  exception.
- The specification's own words settled the shape without interpretation: the homeserver "starts
  sending HTTP requests to the Push Gateway using the supplied URL", and rules are processed "for
  each of the local users in the room (excluding the sender)" — which is why there is now a test
  in which alice's own gateway stays quiet.
