# TODO / working state

Hand-off document for Lattice (minimal Matrix homeserver in Go). Read
[README.md](README.md) for what the server does today, [PLAN.md](PLAN.md) for
the goal, [ENDPOINTS.md](ENDPOINTS.md) for generated coverage. This file is
now a **release plan: work the steps in order, one at a time**. Steps 1-5 are
done, so the next one is **step 6**; federation is steps 6-12. The history that
used to fill this file lives in git; only facts a future session must not
re-derive were kept (below the plan).

## Current state (verified 2026-11-01)

- A working **single-server homeserver**: `make build vet lint` clean
  (`make lint` = golangci-lint, 0 issues), `go test ./...` green — **103**
  compatibility tests (~48 s here, ~5½ min with `-race` on this machine) plus
  **31** offline unit tests in `tests/unit` (milliseconds). Coverage: **149 of
  the 234** indexed endpoints, 26 extra routes (`make endpoints`). Rate limiting
  is in place (step 2), the client-API correctness tails of step 3 are closed,
  the N+1 read paths of step 4 are batched, and the pure rules are pinned
  offline (step 5) — so the plan now starts at **step 6, federation**.
- The E2EE trust layer is done end-to-end: device keys, cross-signing
  (verified server-side over canonical JSON), key signatures, room key
  backup. The Element "unable to set up keys" thread is **closed** — pinned
  by `TestElementKeySetupChain`, `TestKeySignaturesRequestShapes`,
  `TestCrossSigningPartialUpload`, `TestKeyUploadIdentityFieldNames`,
  `TestCrossSigningAuthShapes`. Do not re-open it; the only thing left there
  is clicking through a real Element (see _Open_ below).
- Federation decision (2026-11-01): **in scope for the first release**,
  gated behind a `-federation` boolean flag (default off → routes unmounted,
  behaviour byte-for-byte unchanged, `M_UNRECOGNIZED` on those paths). The
  work is the steps 6–12 of this plan. Target: spec v1.19 server-server API.

## Release plan — pick one step at a time, top to bottom

### 1. Housekeeping — done (2026-11-01)

- [x] Deleted the four verifiably dead functions: `api.rawQueryObject`,
      `service.sessionTTL`, `service.userSummary`,
      `service.roomMembersForSync`. Kept on purpose: `UserSummary` (the user
      directory returns it), `displayName`/`avatarURL` (live callers) and
      `StateRepo.MemberEvents`, now without callers — it stays as repo
      contract because federation state blocks want it.
- [x] `Makefile` with `help build test race vet fmt lint endpoints run swagger
      clean` (`help` is the default goal; `ADDR`/`SERVER_NAME`/`DB`/
      `JWT_SECRET`/`SPEC`/`ENDPOINTS` are overridable variables). `make lint`
      runs golangci-lint 2.15.0 and reports **0 issues** — its first run
      reported seven, all fixed here: a dead store in `service.newSession`
      (the device row was never read when the device already existed),
      `WriteString(fmt.Sprintf)` → `fmt.Fprintf` and two append spreads in
      `cmd/endpoints`, an append spread in `api.handleMembers`, and the
      unchecked `defer resp.Body.Close()` in the test harness.
- [x] `make swagger` works and is honest about being generated: swag v1.16.4
      is fetched with `go run` (not a module dependency), general API info
      annotations live on the `package api` comment, `createRoomBody` became a
      real type (it was an inline anonymous struct the annotation named), and
      output is `swagger.json`/`swagger.yaml` in the gitignored
      `docs/swagger/`. JSON/YAML only on purpose: swag's generated `docs.go`
      imports the swaggo runtime and would break `go build ./...`. 78 paths
      come out; there is no Swagger UI route and none is planned yet.
- [x] `make endpoints` re-run: still **149/234** endpoints, 26 extra routes,
      `ENDPOINTS.md` unchanged.

Standing duty for the steps below: `make endpoints` after each one (also in
the house rules).

### 2. Rate limiting — done (2026-11-01)

- [x] `internal/api/ratelimit.go`: lazily refilled token buckets in three
      kinds — `limitWrite` (`/send`, `/createRoom`, both `/join` forms,
      `/invite`), `limitUpload` (`POST`/`PUT /_matrix/media/v3/upload`) and
      `limitAuth` (`POST /login`, `/refresh`, `/register`). Registration
      helpers: `a.limited(pattern, kind, handler)` (auth first, then the
      bucket) and `a.anonymousLimited` for the endpoints without a token.
      Subject = `auth.GetDeviceID`, or the peer address for the auth bucket;
      `clientAddress` strips the port and reads `RemoteAddress` because there
      is no proxy support to trust a header.
- [x] The 429 body is `matrix.ErrLimitExceeded(retryAfterMS)` — already written,
      never called until now — so the answer is
      `{errcode: M_LIMIT_EXCEEDED, error, retry_after_ms}`.
- [x] Configuration: `api.Config.RateLimits` + `api.DefaultRateLimits()`
      (write 100 burst / 30 per second, auth 20 / 2, upload 20 / 5 — generous
      on purpose: a single-user server never notices them) and the flags
      `-no-rate-limit`, `-rate-limit-{write,auth,upload}-burst`,
      `-rate-limit-{write,auth,upload}-rate`. A zero value falls back to the
      default; `-no-rate-limit` is the off switch.
- [x] Lifecycle: no goroutine. Buckets are pruned on the request path, at most
      once a minute, dropping anything idle for 30 minutes; rejections are
      logged once per bucket plus every hundredth.
- [x] Compat tests `tests/compat/ratelimit_test.go`: 429 shape with a positive
      `retry_after_ms`, the window recovers after the stated wait, buckets are
      per device (a second login of the same user is unaffected), login/register
      share one per-address budget and even the correct password waits,
      uploads are a separate budget, and `Disabled` answers everything. The
      suite's defaults never trip the limiter: 100 compat tests green.

### 3. Client-API correctness tails — done (2026-11-01)

- [x] `GET /notifications` ordering decided and pinned: stored in arrival
      order, paged forward from a row-id token, so **oldest first** and `from`
      always continues downwards. `TestNotificationsOrderAndRead` pins it, and
      adds the mandatory `read` flag (an m.read or m.read.private receipt, or
      the fully read marker, reached the notification) and the `only=highlight`
      filter, which the specification defines and Lattice did not implement.
      `only` with another value is a 400.
- [x] `/relations` and redaction: the TODO said "hide relations whose target
      was redacted" — that is **upside down**. MSC2675 (the merged aggregations
      MSC; "Redactions" under "Event relationships", spec v1.19) reads:
      "Redacted relations should not be taken into consideration in bundled
      aggregations, nor should they be returned from `/relations`. Requesting
      `/relations` on a redacted event should still return any existing
      relation events." So the *relation* is hidden and the *target* is not:
      `EventRepo.Relations`, `Threads` and `ThreadsFor` all filter
      `redacted = false`, which also takes redacted replies out of thread
      counts and `unsigned.m.thread`. Pinned by `TestRelationRedactionRules`
      and `TestThreadPaginationAndRedaction`.
- [x] `/relations` honours `dir` and `to` (`persist.RelationQuery` carries the
      window). **The default direction changed**: relations.yaml (v1.19) orders
      the chunk "topologically most-recent first" and `dir` defaults to `b`, so
      a call without tokens now answers newest first — `TestRelations` used to
      pin oldest-first and was updated to the spec.
- [x] `/threads` honours `dir` and `to`; both bounds are exclusive, ordering is
      by the thread's most recent activity, and `relationship=latest` keeps the
      newest thread whichever way the listing walks. `parseDirection` is now the
      single place that reads `dir` (also used by `/messages` and
      `timestamp_to_event`, whose default stays `f`).
- [x] `GET /rooms/{roomId}/event/{eventId}` and the centre event of `/context`
      carry `unsigned.m.thread` (MSC2675 lists both among the bundling
      endpoints); `annotateThreads` takes a slice, so both feed it a one-element
      one.
- [x] `m.heroes` now sorts by recent activity: `EventRepo.LatestActivity(roomID)`
      (max stream position per sender) feeds `pickHeroes`, ties break on the
      membership timestamp then the user id, and the sync summary and
      `GET /summary` share the picker. The activity query is skipped for rooms
      with six members or fewer (all of them are heroes anyway), which keeps it
      off the DM hot path. Pinned by `TestHeroesOrderByActivity`.

### 4. Batch reads — done (2026-11-01)

- [x] `EventRepo.GetByIDs(ids) []map[string]*Event` and the hydration loops are
      gone: `syncstate.stateEvents`, `syncstate.stateDelta`,
      `hierarchy.hierarchyStateEvents`, `search.searchStateEvents`,
      `markers.Notifications` and `membership.Members` each render their rows
      from one event read. `stateEventsWire(rows, keep)` is the shared renderer
      (sync state block, hierarchy, search) — filter first, then one read, then
      render in row order.
- [x] `strippedState` (the invite state of a sync) listed its types one query at
      a time and then read an event per row: now one `StateRepo.List` for all
      eight types plus the members, grouped by type so the block keeps its
      order, and `strippedEvents` renders it in one event read. The fallback for
      a state row whose event is gone is unchanged.
- [x] Profiles: `ProfileRepo.FieldsFor(userIDs, keys...)` and
      `service.profileSummaries` replace the per-member `displayName`/`avatarURL`
      calls in `Members`, `JoinedMembers`, `SearchUserDirectory` and
      `notifyParticipants` (a message to a 100-member room was 200 profile
      reads). The localpart fallback of `displayName` is preserved, so a user
      with no profile row still gets a name. `SetProfileKey` reads the one
      profile once instead of once per joined room.
- [x] Threads: `EventRepo.ThreadStats(roomID, userID)` aggregates
      `COUNT(*)`, `MAX(stream_pos)` and "how many replies did the requester send"
      per root in one grouped query — so `include=mine` no longer needs every
      reply loaded — and `EventRepo.ByPositions(roomID, positions)` fetches the
      latest reply of every listed thread in one read. Backing it, `events` now
      carries `idx_event_room_rel (room_id, rel_type, relates_to)` next to the
      existing `idx_event_relates`. `/threads` cost grows with the number of
      threads, not the number of replies. An empty listing now answers
      `"chunk": []` rather than `null` (the slice was nil), pinned in
      `TestThreadPaginationAndRedaction`.
- [x] Still one-query-per-room by design: the per-room state and timeline reads
      in sync. That is inherent to a per-room sync response, and federation will
      want those per-room reads anyway.

### 5. Unit tests in `tests/unit/` — done (2026-11-01)

- [x] `tests/unit/` (package `unit`, table-driven, no server, no database, no
      network): **31** tests over identifiers (`Alias` validity,
      `ValidLocalpart`, `UserMxid`, `DomainOf`/`LocalpartOf`, the shape of minted
      IDs), error mapping (constructor → errcode + status table, JSON body shape
      including the User-Interactive Auth `Extra` keys and the rule that an
      internal cause never reaches the client, `matrix.ErrAs` conversion,
      `auth.WriteError` through `httptest`), stream tokens, power levels, push
      rules, and the endpoint matcher.
- [x] Three pure packages were cut out of the service layer and the coverage
      command so they can be tested offline — this is the offline test bed step 7
      asked for: `internal/push` (rule precedence, `Matches`, glob / word /
      count matching, action tweaks — was `service/pushnotify.go`),
      `internal/power` (`Defaults`, `ApplyDefaults`, `Level`, `EventRequired`,
      `ActionRequired`, `ChangeRequired` — was `service/power.go` and
      `stateguards.go`), and `internal/coverage` (the SPECS.md matcher and report
      — was `cmd/endpoints/main.go`, which is now a thin `main`). The service
      keeps the storage plumbing and aliases the constants
      (`PushRuleKinds = push.Kinds`, `PowerAdmin = power.Admin`), and
      `ENDPOINTS.md` came out byte-identical after the move.
- [x] Three behaviours the tables forced into the open, decided rather than left
      fuzzy:
      - **API revisions are not interchangeable in the coverage matcher**: a v1
        index entry is not answered by a v3 route — `VersionEquivalent` compares
        the revision for equality. Its comment claimed the opposite; the comment
        now says what the code does. Making revisions interchangeable would
        report endpoints as implemented that the older client cannot reach, so
        this stays strict.
      - A `room_member_count` `is` expression tolerates padding around the
        operator (`" >= 3 "`), like Synapse's regex does; `CompareCount` trims
        before looking for the operator.
      - A display-name mention has to appear as it is written: a multi-word
        display name matches as a phrase, not one word at a time.

### 6. Federation I — identity, keys, signed transport

- [ ] Flag `-federation` in `cmd/server` (+ `api.Config` field); when off,
      none of the routes below are mounted.
- [ ] `ServerKeys` table: generate one ed25519 key per server on first boot
      with the flag on (id `ed25519:<ver>`), keep the private key; rotation
      and `old_verify_keys` are deferred.
- [ ] Serve `GET /_matrix/key/v2/server` (self-signed response,
      `valid_until_ts` treated as ≤7 days out), `GET
    /_matrix/federation/v1/version`, and best-effort notary endpoints
      `POST /_matrix/key/v2/query` + `GET /_matrix/key/v2/query/{serverName}`
      (fetch, cache, re-sign).
- [ ] New package `internal/federation`: outbound peer client (server-name
      resolution via `/.well-known/matrix/server` then port 8448 — skip SRV
      for now; verify-key cache with TTLs; X-Matrix request signing) and the
      inbound middleware that verifies `Authorization: X-Matrix …` and
      answers 401 on failure. Both directions reuse `matrix.CanonicalJSON`,
      `SignEd25519`, `VerifyEd25519Signature`.
- [ ] Compat tests: key endpoints respond and self-verify; a federation
      request with a broken signature gets 401; nothing is mounted when the
      flag is off.

### 7. Federation II — the event DAG (the hard part; build offline)

- [ ] Storage: `prev_events`, `auth_events` and the **exact signed PDU JSON**
      alongside `persist.Event` (it has `Depth` but no DAG fields today);
      forward-extremities bookkeeping; hard-delete rules and the GORM
      gotchas below apply as usual.
- [ ] `AppendEvent` fills `prev_events` (events with no known child) and
      `auth_events` per the spec's selection: create, power_levels, sender
      member, target member (m.room.member), join_rules for
      join/invite/knock, `join_authorised_via_users_server` member.
- [ ] Event ids become reference-hash based (`$` + unpadded base64 sha256) —
      room versions ≥3 derive the id from the event; `$`-random ids cannot
      talk to other servers.
- [ ] Content hash (sha256 over canonical JSON, `unsigned`/`signatures`/
      `hashes` removed), the redaction algorithm, hash-and-sign per the
      spec's pseudocode; outgoing events carry `hashes` + `signatures` from
      the F6 key **before** persisting or queueing.
- [ ] State resolution **v2** (room versions 2–11, Lattice creates 11) as
      pure functions over loaded events; state res v1 only if joining v1
      rooms later demands it.
- [ ] All of this unit-tested in `tests/unit/` first — no network needed.

### 8. Federation III — transactions and EDUs

- [ ] Inbound `PUT /_matrix/federation/v1/send/{txnId}`: persist
      `(origin, txnId)` → stored result so retried transactions are
      answered, not re-executed (same idea as `replayTxn`, server-to-server);
      ≤50 PDUs / 100 EDUs per txn, one outstanding txn per destination.
- [ ] EDU sinks (all exist locally): `m.typing` → typing rows + hub,
      `m.receipt` → receipts, `m.direct_to_device` → to-device store,
      `m.device_list_update` → remote device cache, `m.signing_key_update` →
      cross-signing store. PDUs answer per-event errors until step 9.
- [ ] Outbound: pending-transaction table per destination + one
      context-bound worker (errgroup, exponential backoff); txn ids unique
      per origin; batching rules above.
- [ ] `m.room.server_acl`: enforce on the spec's endpoint prefix list and
      per-PDU inside `/send`.

### 9. Federation IV — inbound PDU processing

- [ ] The spec's check order, verbatim: valid for the room version →
      origin-server signature (cached keys) → content hash (on mismatch use
      the locally redacted copy) → auth rules against its `auth_events` →
      auth against the state at the event → **soft-fail** if it fails only
      against the current resolved state.
- [ ] Semantics: rejected = kept out of state and never shown; soft-failed =
      stored and relayed as a parent, hidden from clients, not a forward
      extremity.
- [ ] Apply state via step 7's state res v2; fan out to sync through the
      realtime hub like local events.

### 10. Federation V — membership handshakes (first real remote join)

- [ ] Outbound join: `query/directory` for remote aliases → resident
      `make_join` (**must verify** the template's type/room_id/sender/
      state_key/membership before signing) → `PUT /v2/send_join` with
      `omit_members=true` → persist returned state + auth chain; next
      candidate on `M_UNABLE_TO_GRANT_JOIN`.
- [ ] Knock (`make_knock`/`send_knock`) and leave
      (`make_leave`/`v2/send_leave`) are the same handshake.
- [ ] Inbound: serve those endpoints for local rooms (reuse the
      power-level/join-rule code, `join_authorised_via_users_server` for
      restricted rooms), `v2 /invite` with `invite_room_state` **including
      m.room.create**; remote-host side of invite signing.
- [ ] With the flag on: `writeInitialState` defaults `m.federate=true`;
      `LocalRoom` call sites become "is the state in my DAG";
      `walkHierarchy`'s remote-space skip resolves via
      `GET /federation/v1/hierarchy`.

### 11. Federation VI — read-side queries, cross-server E2EE, media

- [ ] Read off the DAG: `GET /event/{id}`, `/state`, `/state_ids`,
      `/event_auth`, `/backfill`, `/get_missing_events`,
      `/timestamp_to_event`.
- [ ] `query/directory` (local aliases), `query/profile`, `publicRooms`
      GET+POST, `openid/userinfo` (`OpenIDToken` already mints tokens).
- [ ] `GET /user/devices/{userId}`, `POST /user/keys/query` and
      `/user/keys/claim` proxy the existing key services; **emit**
      `m.device_list_update` (per-user `stream_id`, `prev_id` chain) and
      `m.signing_key_update` on the changes the local paths already record —
      cross-server E2EE is the whole point of the trust layer.
- [ ] Media: `GET /federation/v1/media/download|thumbnail` (multipart/mixed,
      exactly two parts), cache remote `mxc://` content as Media rows.

### 12. Federation VII — two-server test suite + release wrap

- [ ] `tests/federation/` (black-box style): **two** in-process Lattice
      servers with different `-server-name` in one test binary; inject peer
      base URLs (skips DNS/TLS/discovery) and seed each keypair into the
      other's verify-key cache (skips notaries).
- [ ] Scenario table: join, invite→join, messages both ways, typing,
      receipts, to-device, remote profile/keys, leave, knock, backfill,
      soft-fail (ban, then event referencing pre-ban state), transaction
      retry idempotency.
- [ ] Update README (what `-federation` turns on, the now-mandatory TLS
      note, `m.federate` default), regenerate ENDPOINTS.md, run
      `go test ./tests/compat/ -race` + the federation suite, then tag
      `0.2.0` (client API complete) / `1.0.0` (with federation) as decided
      at the time.

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

## Open (need a human or network, not a code step)

- A real **Element click-through** against a running Lattice (the whole key
  setup chain is chain-tested, but "does the browser stop complaining" is
  unconfirmed; watch the interactive-auth log lines README describes).
- `SPECS.md` references a spec corpus that is **not** in this workspace;
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
  `service.powerLevels` only reads the event and calls `power.ApplyDefaults`),
  `internal/push` (rule precedence and matching — `service.PushRuleKinds` is
  `push.Kinds`, `notifyParticipants` only gathers inputs), `internal/coverage`
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
  **no body and no log line**; `Event.IsState` carries empty-state-key state
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
- Device one-time keys are opaque and claimed once; fallback keys are
  repeat-claimed, never counted, filed under their arriving key id's
  algorithm (`fallback_curve25519`); re-upload keeps unclaimed keys.
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
  `server.MustSync`, `Response.Int/String/ErrCode`. `server.Sync`/
  `SyncSince` do **not** update `session.NextBatch` — assign it by hand;
  `Response` has no `Sync()` helper (`json.Unmarshal(resp.Raw,
&matrix.SyncResponse{})`); time travel via `newClockServer(t, clock)` +
  the mutex clock in `maintenance_test.go` (give the token service a long
  TTL there); logs silenced unless `LATTICE_TEST_LOGS=1`.
- SQLite `:memory:` gives per-connection DBs — `NewDatabase` rewrites to
  `file::memory:?cache=shared` + pins one connection; the harness uses a
  file in `t.TempDir()` instead.

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

## Commands

```sh
make build vet lint                       # compiles, vet, golangci-lint
make test                                 # unit + compat suites (~45 s here)
make race                                 # same with -race
make endpoints                            # coverage report -> ENDPOINTS.md
make run                                  # dev server, 127.0.0.1:8008, /tmp/lattice.db
make swagger                              # docs/swagger/{swagger.json,swagger.yaml}
go test ./tests/compat/ -race
bin/lattice --addr 127.0.0.1:8008 --server-name localhost:8008 \
    --db /tmp/lattice.db --jwt-secret dev      # what `make run` executes
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
