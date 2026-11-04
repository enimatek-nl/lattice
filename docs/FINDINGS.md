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
  `PUT /invite` endpoint, and redaction of an event the peer has never seen. The
  The federation
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
