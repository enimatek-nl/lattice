# Progress

Newest first. Every entry names the command that produced the evidence; nothing here is a claim
without a run behind it.

## 2026-11-05 — M1 closed (foundation + identity layer), Complement gate met

**Verification gate: `make verify`** → `gofmt` check, `go vet ./...`, `go test -race -count=1 ./tests/...`

```
ok  github.com/enimatek/lattice/tests/integration   4.613s
ok  github.com/enimatek/lattice/tests/unit          1.211s
```

`golangci-lint run ./...` → `0 issues.`

Test counts: 53 top-level tests in `tests/unit` (`canonicaljson`, `signjson`, `matrixid`, `config`,
logging redaction, `storage` — including tokens, UIA sessions, devices and login services — router
error semantics, and certificate generation); 38 top-level tests in `tests/integration`, driving the
assembled server over real HTTP and TLS listeners.

### What was built in this milestone

- Protocol primitives: canonical JSON with the spec's example vectors, sign/verify JSON (7-step
  procedure, unpadded base64), identifier grammar, ed25519 signing keys with `ed25519:<id>` key ids.
- HTTP layer: two listeners (client plain HTTP, federation/key HTTPS with TLS ≥ 1.2), method-prefixed
  `http.NewServeMux`, Matrix 404/405 `M_UNRECOGNIZED` semantics mirrored across listeners, body limits,
  request timeouts, panic recovery, security headers, graceful shutdown with a forced-close path.
- Storage: gormlite/SQLite, migrations, models and repositories for users, devices, access tokens,
  UIA sessions, one-time signing keys; bcrypt password hashing; SHA-256 token digests; single-writer
  connection setting recorded as a scalability limit.
- Identity layer: registration (User-Interactive Auth, `m.login.dummy` requires a session this server
  issued, downcasing, `M_INVALID_USERNAME`/`M_USER_IN_USE`/`M_WEAK_PASSWORD`), `/register/available`,
  Synapse-compatible shared-secret admin registration (HMAC-SHA1, single-use nonce, constant-time
  compare), login (password flow, uppercase and localpart identifiers), logout and logout-all,
  device list/get/update/delete with UIA and owner checks, `/account/whoami`, `/capabilities`,
  `/versions`, well-known client and server documents.
- Key API: `GET /_matrix/key/v2/server` (signature verified independently in tests) and
  `POST /_matrix/key/v2/query` for this server only.
- Deployment: `deploy/complement/Dockerfile` + `entrypoint.sh` (image contract, CA-signed certificate
  through `LATTICE_TLS_CA_*`), `scripts/complement.sh`, `Makefile`, `README.md`, `docs/*`.

### Complement evidence

Checkout `7b38a63c9db3337292bee5887fc8533be3a57b9f`; image `complement-lattice:latest` rebuilt from the
committed source and re-run afterwards (`scripts/complement.sh`, exit 1 as expected pre-`/sync`): the
targeted subset reproduces exactly — only the four `/sync`-asserting subtests fail.

Targeted M1 subset: `TestVersionStructure`, `TestUnknownEndpoints`, `TestRegistration`, `TestLogin`,
`TestServerCapabilities`, `TestCanRegisterAdmin`, `TestMediaConfig` pass.
`TestLogout` and `TestDeviceManagement` pass except the subtests that assert through `GET /sync`
(M3 not built yet); the equivalent behaviour is covered by `tests/integration/auth_test.go`.

Full `./tests ./tests/csapi`: 8 passed, 189 failed, 0 skipped. Failure causes: 309 × 404
`M_UNRECOGNIZED` (unimplemented endpoints), 28 × 501 (media upload stub), 1 × 400 `M_INVALID_PARAM`
(refresh-token refusal). Zero panics and zero HTTP 500 responses across the whole run. No Complement
assertion was modified. Details: `.agent-work/COMPLIANCE-MATRIX.md`, `docs/compliance-matrix.md`.

### Manual smoke against the built binary (curl only)

`registration_mode:"open"` startup line → UIA `POST {}` → session → register `Alice` →
`{"user_id":"@alice:smoke.local"}` (downcasing proven), login with `ALICE`, `whoami`, two devices in
`GET /devices`, `POST /logout` → `{}`, reuse of the token → 401 `M_UNKNOWN_TOKEN`,
`GET /_matrix/client/v3/sync` → 404 `M_UNRECOGNIZED`, `DELETE /login` → 405 with `Allow: GET, POST`,
`GET /_matrix/key/v2/server` over HTTPS returns a self-signed ed25519 document, and
`grep -c "S3cr3t\|access_token"` over the server log → `0`.

### Bugs found and fixed while closing the milestone

1. **Data race** between `httpserver.Serve` handing listeners to goroutines and `CloseListeners` from
   `App.Close` — caught by `-race` in the new PKI test. Fixed with a mutex-guarded listener table and
   an idempotent close; the race detector is now part of `make verify` so it cannot regress.
2. **Untrusted CA path**: the federation certificate is now signed by a CA supplied through
   `LATTICE_TLS_CA_CERT_FILE`/`LATTICE_TLS_CA_KEY_FILE` (Go-side, no openssl in the image), verified by
   an integration test that connects with a client trusting *only* that CA — no
   `InsecureSkipVerify` in that test.
3. **Shutdown could hang past the window** with connections still active: `Shutdown` timeout now logs,
   force-closes, and reports a forced shutdown instead of returning a bare `context.DeadlineExceeded`.
4. `golangci-lint` findings: an ineffectual assignment in the IPv6 server-name check, and a raw control
   character in a test fixture.

## 2026-11-05 — Stage 0/1 records

Architecture, requirement rows, pinned specification (v1.19) and the Complement analysis were written
to `ARCHITECTURE.md`, `REQUIREMENTS.md`, `SPECIFICATION-VERSION.md` and `REFERENCE-ANALYSIS.md`; the
Complement image contract, PKI mount points and env-var contract were confirmed against the checkout
rather than assumed.

## Historical note (context lost to compaction)

An earlier build in this session produced a container image and Complement blueprint images. Those
artefacts were rebuilt from the repository (`deploy/complement/Dockerfile`) so the recorded results
are reproducible from source; the earlier ad-hoc recipe is not used.

---

# Iteration 2 — M2 protocol primitives (T-2000, T-2001, T-2011, T-2012)

Timestamp: 2026-11-05 (UTC)

## Specification research

Fetched from https://spec.matrix.org/v1.19/ : `rooms/README` and all eleven per-version
pages, `rooms/redactions`, `rooms/authorization_rules`, `rooms/version_history`,
`server_server_api/signing_events` (content hash, reference hash, signing over the redacted
event), `appendices/canonical_json`, `client_server_api/capabilities`. Cross-checks: Synapse
`synapse/events/__init__.py` (`_RedactionV2`) and gomatrixserverlib `eventversion.go`
(version registry) and `eventcrypto.go` (`referenceOfEvent`), read from the local Go module
cache.

## Built

| Package | Contents |
| --- | --- |
| `internal/roomversion` | v1-v12 registry (state resolution, event ID format, auth rule set id, redaction profile, join rules, integer power levels, key validity, canonical JSON strictness, aliases special casing, creator placement, redacts placement, domain-free room IDs, privileged creators); grammar validation; `Supported` = {10, 11}, default v11; `Available()` for capabilities |
| `internal/redaction` | v1-v10 and v11/v12 strip lists, per-type content allow lists, v9 `join_authorised_via_users_server`, v6 aliases, v11 whole create content / `content.redacts` / `invite` / `third_party_invite.signed`, `IsRedacted` heuristic, non-mutating |
| `internal/events` | `Parse` (numbers kept exact), `Canonical`, `ContentHash`, `ReferenceHash`, `EventID`, `Seal`, transport-key stripping |

## Evidence

- `go test -count=1 ./tests/...`: integration ok, unit ok.
- `make verify` (vet + `-race`): ok. `golangci-lint run ./...`: 0 issues.
- New tests: `tests/unit/roomversion_test.go`, `tests/unit/redaction_test.go`,
  `tests/unit/eventseal_test.go`.
- Cross-implementation goldens from gomatrixserverlib
  v0.0.0-20260716140101-4fe595dc7f58 for identical input bytes: v3 and v10 give
  `$1YYoe3Q9H0V7opN6VO8xuNAlY6Gyk4uxxW9qIEi3SxE`, v11 gives
  `$F0qOSjkTdQqqMJmoiJAQY5guE-jgC6M0CMmPOzfWxOg`. The ID changes between v10 and v11
  because room version 11 changed which keys survive redaction — that is exactly what the
  test is there to catch.

## Docs

`docs/requirements.md` §6 replaced by 22 normative rows with status and tests, plus three
documented interpretation decisions. `docs/compliance-matrix.md` gained an "M2 protocol
primitives" section. `.agent-work/ARCHITECTURE.md` gained the M2 design (storage plan and
five behaviour-changing decisions). `.agent-work/TASKS.md` M2 replanned into T-2002..T-2012
with T-2000/T-2001/T-2011/T-2012 checked off. `.agent-work/BLOCKERS.md` gained F-006 (the
`redacts` ambiguity) and F-007 (environment limits on code search).

## Honest status

M2 is not endpoint-reachable: no `createRoom`, no authorisation rules, no state resolution,
no event storage. `/capabilities` still advertises a room version the server cannot create;
the registry now knows the honest set and the handler gets wired together with `createRoom`.

---

# Iteration 3 — M2 event storage (T-2002)

Timestamp: 2026-11-05 (UTC)

## Built (`internal/persist/event.go`, wiring in `database.go`)

| Table | Purpose / notable choice |
| --- | --- |
| `rooms` | room id, **room version**, creator, latest stream. The version is stored per room because every rule (auth, redaction, event ID format, state resolution) is read from it. |
| `events` | canonical JSON stored verbatim (`json`) plus lookup columns; `state_key` is NULL for non-state events and `""` for state events with an empty key (`m.room.create`); `redacted`/`redacted_json`; `outlier`/`soft_failed`; global `stream_ordering`. |
| `event_edges` | `prev` and `auth` references as rows — history is a graph walk. **No foreign key on `reference_id`**: a server legitimately holds events whose predecessors it has not fetched yet, and a constraint would reject valid Matrix state. |
| `room_state` | current state keyed by (room, type, state_key), separate from history. |
| `event_auth_context` | the state that authorised each event, so re-authorisation and federation never recompute the past. |
| `event_txn` | (device, txn) → event id, unique per device: the basis of `/send/{type}/{txn}` idempotency. |

Indexes: unique event id (dedup), (room, stream_ordering) for history and read
positions, (room, type, state_key), sender, edge reverse lookup for forward
extremities, unique room-state key, unique auth-context pair, unique (device, txn).

`ForwardExtremities` excludes soft-failed and outlier events, so new events never
attach to a branch this server has already declined.

## Evidence

`go test -count=1 ./tests/...` → integration ok, unit ok. `make verify` (`-race`) → ok.
`golangci-lint run ./...` → 0 issues.

`tests/unit/eventstore_test.go` covers: NULL-vs-empty state key preserved, event JSON
stored unchanged, duplicate event id → `ErrConflict`, forward extremities move as the
graph grows and ignore soft-failed events, links to missing events are allowed and
idempotent, batch reads tolerate gaps, stream ordering monotone and not burned by a
rolled-back transaction, room state is an upsert map including the empty state key, auth
context is replaced not accumulated, transaction ids are per device.

## Docs

`TASKS.md` T-2002 checked; `docs/compliance-matrix.md` and `docs/requirements.md`
REQ-TXN-001 updated to reflect storage-only status.

## Honest status

No endpoint can create or send an event yet. Still open in M2: authorisation rules
(T-2004), state resolution (T-2005), `createRoom` (T-2003), `send`/txn endpoint (T-2006),
membership (T-2007), state reads (T-2008), redaction application (T-2009), upgrades (T-2010).

---

# Iteration 4 — M2 event authorisation (T-2004)

Timestamp: 2026-11-05 (UTC)

## Specification basis

Rules were read from the pinned v1.19 pages rather than inferred: `rooms/v3`, `rooms/v6`,
`rooms/v9`, `rooms/v10`, `rooms/v11` (full numbered rule lists quoted for v11; rule 1
verified verbatim for v3, v6, v9, v10) and `server-server-api` §Auth events selection.
Cross-checks: gomatrixserverlib `eventauth.go` (`allowerContext.allowed`,
`userPowerLevel`, `NewPowerLevelContentFromAuthEvents`, `aliasEventAllowed`) from the local
module cache.

## Built: `internal/eventauth`

| Piece | Notes |
| --- | --- |
| `Check` | rules 1-10 in specification order for room versions 3-11; room version 1 returns `ErrRulesetUnsupported` instead of borrowing the wrong table |
| `SelectKeys` / `SelectEventIDs` | the auth events selection algorithm; the sender's and the target's member event are the same event when a user acts on their own membership, so keys are deduplicated (listing twice would make a valid event look like it has duplicate auth events) |
| rule 2 | duplicate `(type, state_key)`, set equality with the selection, unresolved auth events, rejected auth events, foreign `room_id`, missing create event |
| rule 4 | the whole membership table: join (public, invite, knock, restricted, knock_restricted), invite (including the third-party invite chain), leave (self-leave and kick), ban, knock, unknown membership |
| rule 9 | the scalar/maps/users change rules, plus the room version 10 integer validation; versions before 10 keep their lenient behaviour and the test says so |
| version gating | `knock`, `restricted`, `knock_restricted` are unknown to versions that predate them; `join_authorised_via_users_server` in a version without restricted rooms is refused |
| `powerLevelsFor` | the creator holds 2^53-1 while the room has no power levels event, otherwise rule 7 would make rule 9.4 unreachable (F-009) |
| `MayJoinRestricted` | the restricted-room allow list, applied to locally created joins and deliberately **not** inside `Check`: the auth rules are the federation contract, and being stricter than a peer splits history (F-010/REQ-AUTH-023) |
| `Snapshot` / `MemoryState` | the rules run over an interface, so the same code serves tests, iterative auth checks and (later) the database |
| `Opts` | signature verification for `join_authorised_via_users_server` and third-party invites, and the cross-room membership lookup. A nil verifier means reject, never trust |

## Evidence

`go test -count=1 ./tests/...`: integration ok, unit ok. `make verify` (`-race`): ok.
`golangci-lint run ./...`: 0 issues.

`tests/unit/eventauth_test.go`: ~45 named rule cases across membership, knock gating,
restricted joins (including an unsigned authoriser being refused), create rules and the v11
creator change, the rule 2 variants, `m.federate: false`, rules 5-9, power-level change
rules including the version 9 versus 10 integer difference, the aliases exemption and its
removal, selection algorithm outputs, and the room version 1 refusal.

Three findings became tracked items rather than quiet choices: F-009 (rule 7 blocking rule
9.4), F-010 (aliases special case divergence between gomatrixserverlib and the spec), and
REQ-AUTH-023 (restricted-room allow list not evaluated by the auth rules).

## Honest status

Still nothing endpoint-reachable: `createRoom` (T-2003), state resolution (T-2005), the send
endpoint (T-2006), membership endpoints (T-2007), state reads (T-2008) and redaction
application (T-2009) remain. Authorisation for room versions 1 and 2 is unimplemented by
decision, and the restricted-room allow list is only enforced on the local send path.

---

# Iteration 5 — M2 State Resolution 2 (T-2005)

Timestamp: 2026-11-05 (UTC)

## Built: `internal/state`

The algorithm was taken from the "State resolution" section quoted from the room version 11
page (definitions and the five numbered steps) and implemented step by step:

| Piece | Note |
| --- | --- |
| `Resolve` | refuses room versions whose algorithm is not implemented instead of running the wrong one; a single state is returned unchanged |
| `splitStates` | unconflicted map versus conflicted set |
| `authDifference` | ∪ of the auth chains minus ∩, walked from every event in every state |
| `powerEventsInFullConflictedSet` | power events (`m.room.power_levels`, `m.room.join_rules`, a leave/ban applied by someone else) widened by their auth chains |
| `reverseTopologicalPowerOrder` | Kahn's algorithm choosing the smallest available event, comparing sender power level, then `origin_server_ts`, then event ID; leftover events on a cycle are kept rather than dropped |
| `mainlineOrder` | mainline positions from the power levels event in the partially resolved state |
| `iterativeAuthChecks` | applies `eventauth.Check` over the state being built, filling missing keys from the event's own auth events and never from a rejected one |

`Snapshot` adapts a `Graph` plus a `State` to the authorisation rules, so resolution and
authorisation share one event view.

## Evidence

`go test -count=1 ./tests/...`: integration ok, unit ok. `make verify` (`-race`): ok.
`golangci-lint run ./...`: 0 issues.

`tests/unit/stateresolve_test.go`:
- a ban applied by a powerful user on one fork beats a self-join on the other, whichever
  order the forks are handed in;
- of two conflicting power levels events, the later by the ordering wins, and flipping
  `origin_server_ts` flips the result — convergence comes from the ordering, not input order;
- a second `m.room.create` reachable only through an auth chain cannot displace the create
  event every fork agrees on (step 5);
- a power levels event granting its sender power from nowhere is dropped, and a rejected
  event never enters the resolved state;
- room versions 1 and 12 are refused.

## Honest status and the next check

State resolution is implemented and tested against the specification's own wording, but it
has **not** been compared with another implementation's resolver on shared fixtures. A
divergence there is invisible locally and fatal in a federated room, so T-2005a (a
gomatrixserverlib cross-check with shared fixtures) is recorded as a required follow-up
before federation work builds on it, alongside REQ-STATE-007.

Still endpoint-inert: `createRoom` (T-2003), the send endpoint (T-2006), membership (T-2007),
state reads (T-2008), redaction application (T-2009), upgrades (T-2010).

---

# Iteration 6 — M2 room creation and event sending (T-2003, T-2006)

Timestamp: 2026-11-05 (UTC)

## Built

`internal/rooms` (service), `internal/api/client/rooms.go` (transport), and the wiring in
`internal/app`. One service owns the orchestration: room version selection → initial state
construction → auth event selection → authorisation → sealing (hash, event ID, signature) →
persistence of the event, its prev/auth edges, its state row and its auth context, all
inside one transaction.

Endpoints now reachable:
- `POST /_matrix/client/v3/createRoom` — presets, `room_version` negotiation,
  `creation_content` restrictions (the client cannot pick creator, version or predecessor),
  `initial_state`, `power_level_content_override` merged one level deep so an override
  cannot erase the creator, name/topic/avatar shorthand.
- `PUT /_matrix/client/v3/rooms/{roomID}/send/{eventType}/{txnID}` — per-device transaction
  idempotency, server-computed `depth`/`prev_events`/`auth_events`, authorisation before
  signing.
- `PUT /_matrix/client/v3/rooms/{roomID}/state/{eventType}[/{stateKey}]` — including the
  empty state key.
- `/capabilities` now reads `roomversion.Default()`/`Available()`, closing the recorded
  deviation that the server advertised versions it could not create.

## Findings worth recording

1. **`M_NOT_JSON` versus `M_BAD_JSON`.** `httpx` answered `M_BAD_JSON` for a truncated
   body because Go reports truncation as `io.ErrUnexpectedEOF`, not `*json.SyntaxError`.
   The specification separates the two and clients act on them differently.
2. **Empty state key routing.** Go's mux treats `.../state/{eventType}/` as a subtree
   pattern, so it also matches `.../state/a/b/c`. The handler counts the remaining segments
   and refuses the deeper path rather than reading it as an empty state key.
3. **`m.` event types are lowercase.** A client-supplied `m.Room.Message` would create
   state and power-level entries that no specification event matches.
4. **Signature key ID is `ed25519:v1`**, not `v1` — a test asserted the wrong form, not the
   server.
5. **The end-to-end check that matters** (`TestCreatedEventIsVerifiable`): read the stored
   event back, recompute its event ID from what is stored, confirm this server's signature,
   and run the authorisation rules against the stored state and auth context. This is what
   distinguishes "the code ran" from "the event we produced is one the protocol accepts".

## Evidence

`go test -count=1 ./tests/...`: integration ok, unit ok. `make verify` (`-race`): ok.
`golangci-lint run ./...`: 0 issues. New tests: `tests/integration/rooms_test.go` (creation,
sending, idempotency, refusal of non-members, refusal of `m.room.member` via `/send`,
version negotiation against the advertised set, error codes, state send with and without
power, stored-event verifiability) and `tests/unit/roomslogic_test.go` (event type grammar,
power-level override merge).

## Honest status

No room on this server can have a second member yet: join/invite/knock/leave are unimplemented
and `/send` refuses membership events by design, so multi-user Complement room tests still
cannot pass (F-012). The rejection outcome of on-receipt checks is not stored, so
`Snapshot.Rejected` reports false for stored events (F-011) — harmless while every event is
locally authored, must be fixed before federation ingest.

---

# Iteration 7 — membership, room aliases and state reads (T-2007, T-2008, T-2011)

Timestamp: 2026-11-05 (UTC)

## Built

`POST /join/{roomIDOrAlias}`, `POST /rooms/{roomID}/join`, `leave`, `invite`, `kick`,
`ban`, `unban`, `knock`; `PUT/GET/DELETE /directory/room/{alias}`,
`GET /rooms/{roomID}/aliases`, `room_alias_name` in `createRoom`, join by local alias,
canonical-alias validation; `GET /rooms/{roomID}/state[/{type}[/{key}]]` with
`format=event`, `/members` with `not_membership`, `/joined_members`, `/joined_rooms`.
Room aliases are a new table (`room_aliases`) with a unique index.
The offered room version range widened from {10,11} to {7,8,9,10,11}: those are the
versions whose authorisation rules and state resolution are implemented and where knock
and restricted join rules exist.

Two findings about the endpoints, not the protocol:

* The same action has two spellings — `POST /join/{roomID}` and
  `POST /rooms/{roomID}/join`. Answering the second with `M_UNRECOGNIZED` looked like a
  broken homeserver in `TestRoomMembers`, so both are served.
* Complement still calls `r0` paths. A homeserver that only serves `v3` fails
  `TestRoomState` on `r0/rooms/…/joined_members`, so every client endpoint is registered
  under both `v3` and `r0`.

## Findings worth recording

1. **A repository without `WithTx` deadlocks the server.** `RoomAliasRepo` was written
   without a transaction variant, so `WithTx` handed it the *outer* connection while a
   transaction held the only SQLite writer. The request never returned: no error, no
   timeout, just a hung PUT. Found by a test that timed out, and it would have been a
   production hang. `WithTx` is now part of the interface and the reason is commented.
2. **Kicking someone who is not in the room.** The room version rules permit a leave
   event for a user with no membership, so `eventauth` correctly allowed it; Complement
   and sytest expect 403. The rule belongs to the Client-Server endpoint, so it lives in
   `rooms.SetMembership`, and `eventauth` stays faithful to the room version rules.
3. **Room reads were unauthenticated-by-membership.** Any token could read any room's
   state and member list. Now a current membership is required, and a left member loses
   the read (Complement asserts 403 after leaving).
4. **Alias localparts are opaque Unicode.** `#老虎£яඞ:hs1` must work; a bare `%` must
   not. Both are Complement-asserted.
5. **`GET /directory/room/{alias}` must not require a token** — it is how a client finds
   a room to join.
6. **`format=event`** on a state read returns the whole event, not the content; returning
   content made `TestRoomState` report a missing `sender`.
7. **`M_BAD_ALIAS` and `M_DUPLICATE_ALIAS`** are needed for a canonical alias that names
   another room and for an alias already in use; a plain `M_UNKNOWN` would have lost the
   distinction the client acts on.

## Complement evidence (targeted run, not the full suite)

`TestRoomAlias` — including Unicode aliases and "only room members can list aliases" —
and `TestCannotKickNonPresentUser` now **pass**. `TestRoomCreate`, `TestRoomMembers`,
`TestRoomState`, `TestRoomDeleteAlias`, `TestRoomCanonicalAlias`,
`TestNotPresentUserCannotBanOthers` and `TestCannotKickLeftUser` now get through every
protocol step and fail at `GET /sync` (F-013). `TestKnocking*` refuses a remote-user
invite with 501 (F-014). No M1 regression: `TestVersionStructure`,
`TestUnknownEndpoints`, `TestRegistration`, `TestLogin`, `TestCanRegisterAdmin`,
`TestServerCapabilities`, `TestMediaConfig` still pass. Zero panics, zero HTTP 500.
No Complement assertion was changed.

## Local evidence

`go test -count=1 ./tests/...` ok; `make verify` (`-race`) ok; `golangci-lint run ./...`
0 issues. New tests: `tests/integration/membership_test.go`,
`tests/integration/aliases_test.go`, plus `tests/unit/roomslogic_test.go` additions and a
corrected `TestPick` for the widened version range.

## Honest status

Still single-server only: any membership change or alias resolution that needs another
homeserver is refused with 501. No `/sync`, so no client-observable room flow end to end.
The on-receipt rejection outcome is still not persisted (F-011).

---

## 2026-11-05 — T-3001a/T-3001b/T-3001c, T-2007 to T-2011, room versions (M2/M3)

Scope: `internal/sync` (`/sync`, filters, device data, read markers), room versions end to
end, aliases, room reads and history visibility, report and joined_rooms.

What is now true:

* A client can create a room in any version in the advertised range and use it. Complement
  accepts v3, v4, v6, v8, v9, v10, v11 and rejects v42 with `M_UNSUPPORTED_ROOM_VERSION`.
* `/sync` works incrementally with stored and inline filters, `since` position tokens,
  `limited`/`prev_batch`, unread counts, ephemeral and account data, and the `leave`
  section. Complement `TestSyncFilter`, `TestSyncSendersFilter`, `TestSyncLeaveSection`,
  `TestGappedSyncLeaveSection`, `TestOlderLeftRoomsNotInLeaveSection`,
  `TestArchivedRoomsHistory`, `TestLeaveEventVisibility`, `TestCumulativeJoinLeaveJoinSync`
  pass.
* What a room the user is out of may show is decided by the room, not by the request: the
  timeline stops at the membership event that took them out, and state is shown only where
  `m.room.history_visibility` permits it.
* Room reads split into three rules (current member / former member up to their leaving /
  outsider where the room is world readable), and `/joined_members` and `/aliases` stay
  behind membership even in a world readable room.
* A deleted alias corrects the room's `m.room.canonical_alias` event.

Complement (filter: sync, filters, room state/alias/membership/reporting, devices,
registration, login, versions, media config, room summary, left rooms):
**6 failing top level tests**, down from 15 at the start of this round.

| Failing | Cause |
| --- | --- |
| `TestRoomCreate` | v2 and v6 rooms cannot be created locally (v1-spelled invite, signed remote join) |
| `TestRoomState` | `GET /publicRooms` is not implemented (T-3003) |
| `TestRoomSummaryAllowedRoomIDs` | room summary `allowed_room_ids` needs the invite/ban state the summary does not carry |
| `TestSync` | `TestSyncTimelineGap` needs two homeservers (T-4005) |
| `TestSyncOmitsStateChangeOnFilteredEvents` | needs a federated join and lazy-loaded member state |
| `TestPushRuleCacheHealth` | push rules are read-only; `PUT`/`DELETE` missing (T-3005) |

`TestLeftRoomFixture` now fails only on `GET /rooms/{roomId}/messages` (T-3002); its state
and member reads pass.

Local evidence: `make verify` `-race` ok — 156 integration, 169 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was changed.

Health during the run: no HTTP 500, no panics.

---

## 2026-11-05 — correction to the two records above

The commit messages for `941e3e3` and `0e3bf5d` said `.agent-work/TASKS.md` and
`.agent-work/REQUIREMENTS.md` were updated. They were not: the edits were anchored on text
that this project's files do not contain, so they silently matched nothing. The code, the
tests and the Complement numbers in those messages are accurate; the tracking files were
left stale. `0e3bf5d`'s successor fixes that — the requirement rows for the room, sync and
read behaviour added during M2/M3, and the task list, are written in this file pair now, in
the formats these files actually use. Recorded here because AGENTS.md §16 treats an
inaccurate record as a defect, not a detail.

## 2026-11-05 — history paging, the room directory, room summaries (M3)

Scope: `internal/stream` (pagination positions), `internal/rooms/history.go`,
`internal/rooms/directory.go`, `internal/persist/directory.go`, `internal/sync` summary
nesting, four new endpoints.

What is now true:

* `GET /rooms/{id}/messages` pages a room in both directions from a sync token. A page
  stops promising more events when the timeline ends **or** when what remains is beyond
  what the requester may read — a departed member asking what came after they left gets an
  empty chunk and no token.
* The room directory is a decision rather than a consequence: `visibility: public` on
  `/createRoom` publishes a room, `PUT /directory/list/room/{id}` changes that, and only a
  moderator of the room may do it. `GET`/`POST /publicRooms` search, page and describe the
  published rooms.
* A room created with `room_alias_name` now also names itself in `m.room.canonical_alias`.
* `GET /v1/room_summary/{roomIdOrAlias}` describes one room, including `allowed_room_ids`
  for restricted rooms.
* `/sync` puts its member counts in `summary` under `m.joined_member_count` and
  `m.invited_member_count`. They were being emitted beside the room, where no client
  would find them.
* A latent number bug: power levels and depth read numbers out of decoded event JSON, and
  the decoder that preserves canonical JSON yields `json.Number`. A plain `float64` type
  switch returned zero, which in `powerLevel` meant the creator could not publish their
  own room. Both readers now handle every form the value arrives in.

Complement (filter: room directory, messages, left/archived rooms, aliases, membership,
auth, devices, versions, sync filters, summaries): **5 failing top level tests**
(`TestLeftRoomFixture`, `TestRoomCreate`, `TestSync`, `TestSyncTimelineGap`, and the
`tests/` package `TestSync`).

| Failing | Cause |
| --- | --- |
| `TestLeftRoomFixture` | a former member's *old* state value needs per-event state snapshots (T-3004); its `/messages` window is ordered by stream position where the reference server orders by depth (T-3006) |
| `TestRoomCreate` | v2 and v6 rooms cannot be created locally (v1-spelled invite, signed remote join) — federation (T-4005) |
| `TestSync` (csapi) | MSC4222 `state_after`, an unstable extension; deliberately not implemented |
| `TestSync` (tests) | presence in `/sync` (T-3007) |
| `TestSyncTimelineGap` | needs two homeservers (T-4005) |

Newly passing since the round before: `TestPublicRooms`, `TestRoomState`,
`TestRoomSummary`, `TestRoomSummaryAllowedRoomIDs`, `TestSendAndFetchMessage`,
`TestRoomMessagesLazyLoading*`, `TestFetchMessagesFromNonExistentRoom`.

Local evidence: `make verify` `-race` ok — 170 integration, 169 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

---

## 2026-11-05 — push rules, and getting them to the client (M3)

Scope: `internal/pushrules` (new), `internal/persist/accountdata.go`,
`internal/api/client/pushrules.go`, `internal/sync/accountdata.go`, a registration hook in
`internal/users`, one index pair and one table in the migration.

What is now true:

* A user has push rules from the moment they exist. The server-default set is the
  specification's, including the rules added in v1.4, v1.7 and v1.9, with the three
  v1.7-deprecated ones still present because the specification's condition on them is
  about matching, not existence. Rules that name the user name *them*: an invite rule whose
  `state_key` pattern was a placeholder would look right and notify nothing.
* The whole API under `/pushrules` answers, at every depth, including a rule's `enabled`
  and `actions`, and writes keep the order that *is* the priority: a new rule goes at the
  head of the user's section, the server's defaults stay behind it, and `before`/`after`
  cannot refer to a default.
* The rule set reaches the client. `/sync` now has a top-level `account_data` section, and
  editing a rule bumps the stream position, so a long-polling sync wakes up instead of
  timing out.
* Defaults are created with the account (`users.Service.AfterRegister`) and swept once at
  start-up for accounts that predate the code.

Three things found on the way, all recorded because each was a trap for the next writer:

1. **A transaction inside a transaction deadlocks on a single-writer database.** The first
   version of the service opened a transaction and then called a helper that opened
   another, waiting for the connection the first was holding. The store now has one writer
   that never opens a transaction (`write`) and one wrapper for callers that hold none
   (`save`), and the reason is in the comment rather than in a commit message.
2. **A storage "not found" is not a protocol "not found".** Reading a deleted rule reached
   the client as a 500. `ruleError` maps it to `M_NOT_FOUND`, the code the push rule
   endpoints prescribe.
3. **A rule ID that starts with a dot has to be escaped, and the escape is easy to get
   wrong.** `%2E.m.rule.master` decodes to `..m.rule.master`, not `.m.rule.master`: the
   literal dot after the escape is a second dot. It is `%2Em.rule.master`. The test
   originally asserted against the wrong path, not the wrong server.

Complement (filter: push rules and push sync, room directory, summaries, `/messages`, sync
filters, logout/devices, registration/login, room creation): **no push-rule failure**;
`TestPushRuleCacheHealth` and `TestPushSync` pass, including the four "wakes an incremental
/sync" subtests. Still failing: `TestRoomCreate` (v2/v6, federation), `TestSync` (csapi:
presence, `device_lists`, a two-server join, MSC4222 `state_after`; tests package:
presence).

Local evidence: `make verify` `-race` ok — 174 integration, 175 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

Honest gap: the rules are stored, edited and delivered, but nothing acts on them yet —
there is no pusher, no push gateway call, no `GET /notifications`, and the unread and
highlight counts in `/sync` are still zero. That is task T-3005a, not a footnote.

---

## 2026-11-05 — the rule evaluator (M3, not yet applied)

Scope: `internal/pushrules/evaluate.go`.

`Ruleset.Evaluate(event, context)` decides what one event means for one user: the most
important matching rule wins, the master rule is consulted before everything else, and the
actions of that one rule are the answer.

The conditions are implemented as the specification defines them, including the two that
are easy to get wrong: `event_match` on `content.body` matches a *substring bounded by
words* rather than the whole value, and a dot inside a property name is escaped
(`content.m\.mentions.room`), so a path split without caring about the backslash reaches
the wrong object. A condition kind this server does not know never matches — the rule is
kept, and it is silent, which is what the specification asks for.

An event the user sent themselves never notifies, whatever the rules say.

One expectation of mine was wrong while writing the tests: between the kinds, the
specification orders room rules *above* sender rules. A sender rule feels more specific to
a person reading it, and the test said so; the specification does not, and the code follows
the specification.

What is NOT done: nothing calls this yet. `/sync` still reports zero unread and highlight
counts, and there is no pusher, push gateway request or `GET /notifications`. The missing
piece for the counts is not the evaluation — it is a read marker to make "unread" mean
something (T-3005a).

Local evidence: `make verify` `-race` ok — 174 integration, 199 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement test changed, because none of this is
reachable from the API yet.

---

## 2026-11-05 — presence, and one stream instead of three (M3)

Scope: `internal/presence` (new), `internal/persist/presence.go`,
`internal/persist/stream.go` (new), `internal/api/client/presence.go`, the presence section
in `internal/sync`, a join hook in `internal/rooms`.

What is now true:

* `GET`/`PUT /presence/{userID}/status` answer. A user may only set their own; an unknown
  presence value is refused; a user on another server is answered as not known rather than
  guessed; a user this server has never reported is `offline`, because "are they around"
  has that answer before it has "I don't know them".
* `/sync` has a `presence` section. Presence is not room data, but the room is what decides
  whose presence you may see, so a join announces the arrival to the members already there
  and their presence to the joiner.
* `set_presence` is honoured. It defaults to `online`, and an unchanged status is *not*
  written again: a client polls, and writing every poll would announce the user to everyone
  they share a room with over and over. `MarkActive` exists for the same split — being seen
  refreshes `last_active_ago` and deliberately takes no stream position.

A real defect was found on the way, and it was in code shipped two days ago:

* Positions were taken as `MAX(events.stream_ordering) + 1` by every writer — events *and*
  account data. So the position `/sync` reported as `next_batch` (the events maximum) could
  sit below positions account data had already taken, and a later event could be handed a
  position an account data write had taken. A sync token is supposed to mean "everything up
  to here"; under that scheme it meant "every event up to here, and possibly some things
  after it, and a client holding it may be told the same thing again on every poll".
  Positions now come from one counter — the maximum across `events`, `account_data` and
  `presence` — and `next_batch` is that same maximum. `TestPresenceStreamSharedWithEvents`
  pins the cross-table ordering, because that is the invariant, not the implementation.
* The join hook runs *after* the membership transaction commits. Calling the presence
  service inside it would have reproduced the deadlock already recorded in the push rule
  service: the presence write goes through the pool, and the pool has one writer which the
  open transaction is holding.

Honest gaps: nothing makes a user go quiet, so `online` sticks until the user or a room
event says otherwise (T-3001e — it needs a policy decision, so it is a task, not a guess);
no federation presence EDUs, so remote users are not known (M4); the filter's `presence`
section is not applied (T-3001f).

Complement: `TestPresence` passes with all its subtests, and both presence subtests of
`TestSync` pass. `TestSync` now fails on three things: `Device_list_tracking` (device keys,
T-3008), a sync token across a redaction of an unknown event (needs federation to create
the event, T-4005), and MSC4222 `state_after` (unstable, deliberately out).

Local evidence: `make verify` `-race` ok — 176 integration, 203 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

---

## 2026-11-05 — device list tracking in `/sync` (M3)

Scope: `internal/persist/device_lists.go` (new), `internal/rooms/membership_effects.go`
(renamed from `presence.go`), the `device_lists` section in `internal/sync`.

`device_lists.changed` names a user whose keys are worth asking for again;
`device_lists.left` names one whose keys are no longer worth keeping. Both take a position
in the shared stream, and both record the room the change happened in — the member list of a
room cannot answer "who may hear about this", because the user who left is no longer in it.

The section is absent rather than empty when there is nothing to say: an empty object reads
as "nothing changed", which is a different answer from "there is nothing to report".

Membership is the only trigger today. Key uploads, new devices and cross-signing updates are
the other triggers in the specification, and none of those endpoints exist on this server
yet, so the requirement row says "partially" and the task list carries T-3008b rather than
letting a passing Complement test imply that encryption works. It does not: a client told to
re-fetch keys has nowhere to ask.

Complement: `TestSync/parallel/Device_list_tracking` passes, including the retry case.
`TestSync` now fails on two things only — a redaction of an unknown event that needs a
second homeserver to create (T-4005), and MSC4222 `state_after` (unstable, deliberately out,
along with the whole `tests/msc4222` package).

Local evidence: `make verify` `-race` ok — 178 integration, 204 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

---

## 2026-11-05 — room upgrades (M3)

Scope: `internal/rooms/upgrade.go` (new), `internal/persist/alias.go` (`MoveRoom`), the
tombstone branch of `internal/rooms/membership_effects.go`, `pushrules.Service.RoomUpgraded`,
`POST /rooms/{roomID}/upgrade`.

Both ways an upgrade happens are supported, because clients do both: the endpoint, and a
client that creates the new room with a `predecessor` in `creation_content` and then closes
the old room by hand. What the server does:

* The replacement's `m.room.create` carries `predecessor` with **both** fields the
  specification requires. A client that names only the old room is not wrong — filling in the
  event is the server's job, because it is the one that knows the room.
* The `type` and `m.federate` of the old room's create event are copied. A space that
  upgraded into a plain room would stop being a space, and nobody asked for that.
* The nine state types the specification recommends are carried; membership is not, because
  signing a join for a user on another server would be forging their arrival.
* The aliases move. A name left on a room whose own state says it is finished leads clients
  somewhere dead.
* A tombstone anywhere — from the endpoint or from a client — carries each local user's
  room-scoped push rules across. That behaviour is not in the specification's upgrade
  section, so it is recorded as an implementation convention rather than as a requirement.

One bug found while testing it, in code written an hour earlier: `pushrules.load` reports
whether the ruleset had to be *created*, and the new copy carried the case backwards — every
user with real rules was skipped, and only a user with none was processed. The integration
test caught it; the Complement test would have caught it too but only after a federation
join that never gets that far today.

Decisions recorded rather than defaulted: an upgraded room still accepts events (T-2010b —
refusing them affects clients that have not seen the tombstone, so it is a decision, not a
default), and `m.room.canonical_alias` is not copied even though the alias itself moves
(the person upgrading did not choose the new room's name).

Complement: `TestPushRuleRoomUpgrade`'s two local subtests pass. The two remote ones fail at
`POST /join` for a room on another homeserver — T-4005.

Local evidence: `make verify` `-race` ok — 181 integration, 204 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

---

## 2026-11-05 — device keys, one-time keys, and who may hear about them (M3)

Scope: `internal/e2ekeys` (new), `internal/persist/devicekeys.go` (new),
`internal/persist/device_lists.go` (observer-addressed), `internal/api/client/keys.go`,
`persist.CurrentMembers`.

`POST /keys/upload` (and PUT, for clients written against older text), `POST /keys/query`
with `since`, `POST /keys/claim`. A key document is stored as the bytes that arrived, because
its signature is over exactly those contents. A one-time key is only accepted signed, and its
signature is checked against the identity keys the device published — not against anything in
the key's own document, because a key that vouches for itself vouches for nothing. Keys are
claimed once, in upload order: a claim that reordered them would hand out sessions in an order
the client never offered.

Two behaviours were worth the bug hunt:

* A key upload must tell the other clients of the rooms this user is in to fetch the keys
  again. `TestDeviceListUpdates` checks that, and it also checks the case a room-shaped model
  cannot serve: a user who leaves must be told that the people still in the room are no longer
  theirs to track. A notice shelved in a room cannot reach the person who just left it, so a
  device list change is now addressed to its observer at write time (DEC-017).
* Joining announces both directions. The members are told about the newcomer *and* the
  newcomer is told about the members — presence already did that; device lists did only half.

One deliberate relaxation, recorded rather than hidden (DEC-016): an offered device-key
signature is verified and a bad one refused, but a document that offers no signature at all is
stored. The specification says the device signs its identity keys, and it also says the client
that is about to encrypt to them is the one that verifies them, and that a client must not rely
on the server being well behaved. Complement's device list tests upload documents signed by
nobody — Synapse and Dendrite store them — and the behaviour those tests describe is worth more
here than a claim this server is not entitled to make. One-time keys are not relaxed: their
format requires a signature, and it is checked.

Local evidence: `make verify` `-race` ok — 186 integration, 205 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues.

Complement: the four key-upload/claim tests pass; five of the ten `TestDeviceListUpdates`
subtests pass — all the ones that need a single homeserver. The other five, and the remaining
failures in `TestPushRuleRoomUpgrade` and `TestSync`, all stop at the same place:
`POST /join` for a room this server has never heard of. Federation (T-4005) is now the largest
single cause of Complement failures here.

## 2026-11-05 — federation begins: two homeservers, one process (M4)

Federation stopped being a placeholder. A homeserver can now authenticate another
homeserver, decide what its keys are worth, accept its events after checking everything
the specification says a receiver checks, answer for the ones it took, and keep trying to
deliver the ones it owes — without any of that being able to block a local user.

What was built:

* `internal/federation`: the X-Matrix credential in both directions (`auth.go`); server
  name to address, with `.well-known` delegation, both SRV service names, IP literals and
  explicit ports, and answers cached under a TTL (`target.go`); the addresses this server
  is willing to dial, denied by default for loopback, link-local — where the instance
  metadata services live — CGNAT, multicast and the private ranges (`ippolicy.go`); other
  servers' signing keys, fetched direct or through a notary, trusted only when that server
  signed a key it publishes, with the trust window clamped to a week whatever the document
  claims and retired keys honoured until their own `expired_ts` (`keyring.go`); the
  outbound client and the queue that retries with a bounded schedule and remembers a
  remote's refusal instead of retrying it forever (`client.go`, `sender.go`).
* `PUT /_matrix/federation/v1/send/{txnId}`: authenticated before a byte of the body is
  read, the body's `origin` required to agree with the credential, the PDU and EDU caps
  enforced, a replayed transaction answered identically, and every PDU answered by its own
  event ID with its own reason. `GET /_matrix/federation/v1/event/{eventId}` hands an event
  to a server the room's own state says is present, and to nobody else.
* `rooms.Service.AcceptRemoteEvent`: the receiving checks in the specification's order —
  valid in its room version, its ID equal to the hash its content makes, its content hash
  matching, its origin server's signature checking, its cited auth events authorising it —
  and a room introduced by a remote `m.room.create` adopted at the room version that create
  event asked for, not this server's default. An event its branch authorises but the room's
  current state does not is stored and soft-failed (DEC-018): it is kept out of what this
  server relays and out of the forward extremities, and still shown to peers over `/event`,
  because the specification says a soft-failed event is part of the history.
* Federation configuration: `federation.enabled`, `notary_servers`, `ip_allow`, `ip_deny`,
  `timeout`, `queue_max_age`, `queue_max_attempts`, with the environment overrides
  (`LATTICE_FEDERATION_ENABLED`, `..._IP_ALLOW`, `..._IP_DENY`, `..._NOTARIES`), and
  malformed CIDRs stopping startup rather than vanishing into an open door.

Four defects the work exposed, each fixed with a test that used to pass:

* I "fixed" `GET /rooms/{roomID}/state` from a bare array to `{"state": […]}`, on the
  strength of the API-standards sentence that every endpoint answers with a JSON object, and
  rewrote two of this project's tests to assert the new shape. That was wrong, and the
  Complement run in this window is what said so: the endpoint's own schema documents
  "Array of `ClientEvent`" and shows a top-level array, `TestRoomState` walks that array, and
  Synapse answers with one. Reverted; the array is the contract (REQ-HTTP-007 now records the
  contradiction rather than a rule that does not exist). The lesson is recorded too: the local
  suite had been written around the wrong shape, so it defended the bug.
* Event signature verification used a generic JSON signer. In the room versions whose event
  IDs are hashes, the identifier is not part of the signed document, so keeping it makes every
  honestly signed event look forged: this server would have refused real events from every
  other server on the network, and had no test that could notice (DEC reference: the seal
  tests predate any receiver).
* Verification never checked the `hashes` an origin server records. In room versions 3 and up
  the event ID is the hash of the *redacted* form, so the body of a message is not in it — the
  content hash is the only thing that puts the body into the event's identity. Without that
  check, a homeserver in the middle could rewrite a message's text and present the same,
  correctly identified, correctly signed event (REQ-FED-010).
* `federation_queue.dropped_at` was a plain `time.Time`, so every queued row carried the zero
  date instead of NULL, and every "is this still owing?" query — including the claim that
  feeds the sender — answered no. The queue silently emptied itself and undelivered events
  were lost. Nullable columns in the storage layer are pointers now, where NULL is the answer.

Local evidence: full suite green — `go vet ./...` clean, `golangci-lint run ./...` 0 issues,
`tests/unit` and `tests/integration` pass under `-race` (`make verify`). New: `tests/unit/federationdiscovery_test.go`,
`tests/unit/federationkeys_test.go`, `tests/unit/eventsignature_test.go` (all supported room
versions), and `tests/integration/federation_test.go`, which runs two homeservers on real
listeners with real TLS certificates from a test CA and real `X-Matrix` signatures, moves a
room's beginning from one to the other, reads an event back and compares every field that a
hash depends on, and then checks the refusals: no credential, an unknown key ID, a body changed
after signing, a credential addressed to somebody else, an origin that disagrees with its
credential, a forged event signature, an event in an unknown room, a transaction of 51 PDUs,
and a replay.

A Complement run in the same window, and what it said:

* It caught the `/state` shape error above. The local suite could not: its assertions had
  been written around the wrong shape in the same window.
* `TestInboundFederationKeys` failed, and the reason was worth having: `GET
  /_matrix/key/v2/server` signed a document and then served it through Go's encoder. The
  bytes on the wire were not the bytes the signature covers — a trailing newline, Go's
  escaping, Go's number formatting. Complement verifies the body it received with the
  `signatures` member removed, and correctly called our own key invalid. The endpoint now
  answers with the canonical form (REQ-FED-000), and the integration test asserts both that
  the body is its own canonical form and that the signature checks over it.
  `TestInboundFederationKeys` passes. This is the second Complement-caught defect in this
  window; both were invisible locally because the local checks re-encoded what they
  verified.
* `TestRoomCreate` asks for a room in version 2 and is answered `M_UNSUPPORTED_ROOM_VERSION`,
  because room versions 1 and 2 are deliberately not implemented (T-4013).
* The sync suites now fail on narrower things than they used to: MSC4222 `state_after`,
  filters over the state section, depth-ordered paging across a gap, and a sync token that
  points at a redaction of an unknown event (T-2009). `TestRoomState`, `TestRoomAlias`,
  `TestRoomMembers` and the M1 suites pass.

What this does *not* establish: Complement's federation suites still fail (F-015) — they drive
federation through a join, and `/make_join`, `/send_join`, `/state_ids`, `/backfill` and
`/invite` are not implemented (T-4009). An event whose predecessors are missing is refused with
the reason named rather than fetched (T-4007). EDUs are acknowledged and logged, and nothing
else happens to them (T-4008). The address policy is applied when discovery resolves a name,
not when the socket opens, so DNS rebinding is not covered (T-4011).

Follow-up in the same window, found while writing the queue's own tests:

* The federation tables hold promises, and nothing was calling the code that forgets
  expired ones. `Sender.Cleanup` now drops answered transactions past their replay window,
  key documents past their trust window and queue rows this server gave up on a month ago,
  and the worker runs it hourly (immediately on start, so a process that was down for a day
  tidies itself). A failure that is still recent keeps its row: "why did that room never
  arrive?" has to be answerable while it is still a live question.
* `SeenTransaction` took an instant and ignored it. An answer kept past its expiry is not
  idempotency — it is a stale reply to a new request that happened to reuse a transaction
  ID. The window is now part of the lookup.
* New: `tests/unit/federationqueue_test.go` asserts the queue's promises directly — a claim
  cannot be taken twice, a retry is not claimable before its time, delivered work leaves the
  table, giving up leaves a record that stops counting as owing.

`make verify` and `golangci-lint run ./...` clean: 191 integration, 211 unit tests.

## 2026-11-05 — a refusal that outlives the event it refuses (M2, T-2006a)

The authorisation rules have always said that an event citing a rejected auth event is
rejected. Nothing in this server could act on that: a refused PDU was not stored, and the
verdict went with it, so `Snapshot.Rejected` answered `false` in every real snapshot and the
two rule paths that consult it — `eventauth` rule 2.3 and the iterative check inside state
resolution — were decoration.

Now `event_rejections` holds the verdict: event ID, room, the reason, the server that
refused it. `dbSnapshot.Rejected` and `citedSnapshot.Rejected` read it, and rule 2.3 is
asked **before** the auth event is looked up. That order is the whole point. A refused
event is absent from this server either because nobody mentioned it or because this server
was told and said no, and looking it up first answers both with "cannot be resolved" —
which turns a verdict into a gap someone could still fill.

Two boundaries matter as much as the memory:

* A refusal that was only "this server could not judge yet" — unknown room, missing
  predecessors, an auth event nobody has mentioned — is **not** recorded (DEC-021). A
  handover that arrives out of order is a temporary hole; recording it as a verdict would
  permanently ban an event this server never actually judged.
* An event ID this server already holds is never recorded. Without that, a stranger
  offering forged bytes under a good event's ID would have made the good event a rejected
  one, and every successor citing it would have been refused. The lookup that decides
  "already have it" now also stops a storage error from being answered as a verdict.

Evidence: `tests/unit/rejections_test.go` (the verdict survives, a second refusal does not
rewrite the first, an unseen ID is not reported as refused, rule 2.3 fires for an absent
but refused auth event while an unmentioned one still reports a gap) and
`tests/integration/federation_test.go` (`TestFederationRefusalIsAVerdict`: two homeservers,
a forged PDU refused and remembered, an honestly sealed event citing that PDU refused with
the refusal named, an honestly sealed event citing an unmentioned ID refused and *not*
recorded).

Found while doing this, and now tracked as T-2017: **the room's current state is settled by
arrival order.** `rooms.persistEvent` sets a state key to the last state event persisted for
it; `internal/state.Resolve` implements State Resolution 2, is unit-tested, and is never
called. For one server that is usually the same answer. For two it is not, and AGENTS.md and
the specification both forbid the substitution. The resolver needs the state each forward
extremity was carrying, which is per-event state snapshots (T-3004) — so T-2017 is
sequenced after T-3004 rather than worked around. The compliance matrix now says this out
loud instead of listing SR2 as implemented.

`make verify` (fmt, vet, `-race`) and `golangci-lint run ./...` clean.

## 2026-11-05 — redaction: each server destroys its own copy (T-2009, T-2014, T-2016a)

Three endpoints and one rule, and the rule turned out to be the interesting part.

The authorisation rules deliberately say nothing about the redact level — they decide only
whether an event may be sent. Whether a redaction *destroys anything* is decided later, by
each server that receives one, on two conditions either of which is enough: the sender's
power level reaches the room's redact level, or the sender's domain matches the domain of
the sender of the event being redacted. The second is what lets a homeserver tidy up its own
users' messages without anybody gaining power over another server's events. So an event can
pass every auth rule and still never remove a byte, and `PUT /redact` now refuses a
redaction this server would never apply instead of filing one that does nothing forever.

The halves of a redaction can arrive in either order, so the link is kept on receipt and
re-checked by persistence from both directions: a redaction whose target is absent waits, and
the event that finally arrives settles it. The test hands a redaction to a second homeserver
before its target, then the target, and checks the second server ends up with a redacted copy
it applied itself.

Found on the way, and it changed the design (DEC-023): the specification strips the event
"upon receipt of a redaction event" — the receiver strips its own copy. So federation carries
the **unredacted** event plus the redaction, and the content hash is checked unconditionally;
`events.redacted_json` is a *view* for clients (`Event.Serving()`), never a replacement for
what was received. The first attempt did the opposite — forwarded redacted forms and skipped
the content-hash check for them — and `TestEventSignatureSurvivesEveryRoomVersion` failed for
every room version, which is that test doing exactly its job: a stripped copy of an event must
not pass as the event it redacts. gomatrixserverlib checks the content hash with no redaction
exception either.

Judged against the room's *current* power levels at the moment of application (DEC-022),
because the power levels a redaction was authorised against are not stored per event; T-3004
would let that question be asked properly. Recorded, not hidden.

`GET /rooms/{roomID}/event/{eventID}` came with it: an event in another room answers as not
found, the same as an event that does not exist, so the endpoint does not report which room
holds which ID. Complement's `TestRedact` passes, and `TestPowerLevels` passes too — it had
been failing only because its `SendEventSynced` helper reads events back through `/event`.

Still open, recorded as tasks: `/sync` serves the redacted form but not the
`unsigned.redacted_because` copy (T-2009b), and a redaction this server may not apply is still
listed in timelines instead of being withheld, which the specification says it should not be
(T-2009a).

Evidence: `tests/unit/redaction_rules_test.go` (target field per version, `ForClient`
placement, the redact-level and domain rule against configured and default power levels) and
`tests/integration/redaction_test.go` (emptied event with `redacted_because`, idempotent
retry, `/messages` and `/sync` agreement, unknown target and cross-room reads answered 404,
v10 top-level `redacts`, and the out-of-order handover over federation).
`make verify` (fmt, vet, `-race`) and `golangci-lint run ./...` clean; Complement filtered run
`TestJson|TestPowerLevels|TestRedact|TestEventAuth|TestRoomMessages…|TestSync` green except
`TestRoomCreate` (v2 refused by design), `TestSync/parallel` (needs the federation join
handshake and MSC4222 `state_after`) and `TestSyncTimelineGap` (depth paging).

## 2026-11-05 — the state each event was sent against (T-3004a)

`internal/state.Resolve` implements State Resolution 2 and is never called, because the
question it answers — what state was each branch carrying — is not one this server can be
asked. `room_state` holds one row per state key: *where the room is now*. A fork needs *what
each branch thought*, and that had never been recorded.

So: snapshots. A `state_groups` row is an identity for one exact set of (type, state_key) →
event ID entries; `state_group_entries` holds them; `event_state_groups` points each event at
the one it was sent against. The identity is a hash of the entries, so two branches that
happen to agree share a snapshot without anybody having to notice, and a linear room grows by
one snapshot per **state change** rather than per message — which the integration test counts
rather than asserts.

Two things the storage got wrong on the first try, both caught by existing tests: a snapshot
was unique on its hash alone, so two rooms' empty snapshots collided and one room inherited
the other's state (room creation fell over with a 500); and "this event's snapshot is
unknown" must not be answered as "the room had no state", which would authorise nonsense
against an empty room.

What is *not* done: when an event joins branches that disagree about the state, this server
records the snapshot of the most recently persisted predecessor and logs that it is
provisional. That is a known wrong answer in one narrow place — the shape of the fork is now
recorded, so the answer can be resolved instead of guessed, which is T-2017.

Evidence: `tests/unit/state_groups_test.go` (order-independent identity, one entry changed is
a different snapshot, per-room uniqueness, empty snapshot stays empty, unrecorded events are
missing rather than empty, re-attach moves the pointer) and
`tests/integration/stategroups_test.go` (the create event's snapshot holds only itself; three
messages share one snapshot; the topic event's snapshot has exactly one more entry and the
following message shares it; snapshots and current state agree; fewer snapshots than events).
`make verify` and `golangci-lint run ./...` clean.

## 2026-11-05 — the fork question, answered by an algorithm instead of an arrival order (T-2017)

`internal/state.Resolve` had been sitting unused for weeks: it implements State Resolution 2
and nothing called it, because `room_state` holds one row per state key — *where the room is
now* — and the algorithm asks a different question, *what was each branch carrying*. T-3004a
recorded the snapshots; this is the wiring.

An event that joins branches, or a room left with more than one forward extremity, is now
resolved over the extremities' snapshots and the answer written back as current state
(`internal/rooms/resolve.go`). A room with one extremity and one predecessor still takes the
cheap delta path.

The confluence test found a bug in the algorithm itself, and it is the kind that never shows
up on one server. Two homeservers were handed the same fork in opposite orders; they agreed —
so the room looked fine — but running the resolver standalone on the same events disagreed
with what the servers had decided. Cause: `state.eventTimestamp` asserted
`interface{ Int64() int64 }`, and `json.Number`'s method is `Int64() (int64, error)`. So every
event decoded from JSON looked like it happened at time 0, the mainline ordering's timestamp
comparison silently did nothing, and forks settled by event ID.

That is deterministic, which is why nothing had caught it: Lattice would agree with Lattice.
Synapse orders by `origin_server_ts`, so Lattice and Synapse would resolve the same fork to
different state — a room split, on a code path that passes its own tests. The regression test
(`TestResolveOrdersJSONDecodedEventsByTheirTimestamp`) is written to fail against the old
reading, and I checked that it does: without the fix the *older* event wins.

Evidence: `tests/integration/statresolution_test.go` — same fork, opposite orders, two
homeservers agree; the loser is still stored (resolution decides what the state *is*, it does
not delete history); both extremities stay open; and the server's answer matches the algorithm
run standalone. `tests/unit/stateresolve_test.go` — both argument orders, and the JSON case.
`make verify` and `golangci-lint` clean. Complement filtered run unchanged from before this
work: `TestRoomCreate` (v2 refused by design), `TestSync/parallel` (needs the join handshake
and MSC4222 `state_after`), `TestSyncTimelineGap` (depth paging).

Opened rather than hidden: T-3004b (rooms that predate snapshots have no groups, and this
server leaves their current state alone instead of inventing one — DEC-025) and T-3004c
(`reconcileCurrentState` runs on every persist while a room is forked — correct, unmeasured;
DEC-024 records why materialising at persist was chosen over resolving at read).

## 2026-11-05 — one answer to "what may this client see" (T-2009a, T-2009b)

The redaction work had a gap I had noted and left: `/sync` served an emptied event with no
explanation, while `/messages` attached `unsigned.redacted_because` and said who did it. Same
room, same event, two answers — and a redaction that had not been applied yet was still
listing itself in timelines, which the specification asks servers not to do until both halves
are in hand.

Both are the same decision, so they now live in one place: `redaction.Servicer` turns a stored
row into what a client may see — the emptied view, `redacts` where the room version puts it,
the copy under `unsigned`, and whether the event should be shown at all. `/event`, `/messages`,
state reads and `/sync` go through it. `internal/events` already depends on `internal/redaction`
(an event's ID is computed over its redacted form), so the servicer decodes with `matrixjson`
rather than importing the event parser back — the dependency direction is not a detail here.

A withheld redaction is kept, not deleted; only the view changes. `/event` answers it as
not-found rather than with a distinct code, because "I hold this redaction but not what it
destroys" tells the client more than the rule asks for, and a client that gets a 404 simply
asks again later, which is the behaviour we want.

Evidence: `tests/unit/serve_test.go` (the emptied form plus its reason; withholding until the
partner arrives; a chunk that skips the withheld event and keeps the rest; `redacts` placement
per room version). `tests/integration/redaction_test.go`: `TestSyncExplainsWhyAnEventItAlready
SawWentEmpty`, and `TestARedactionIsNotShownToAClientUntilItsTargetArrives` — hs2 is handed the
room and a redaction but not the message, its own user joins, and then `/messages`, `/event`
and `/sync` all decline to show the redaction while the page around them still answers; when
the message arrives, both halves show and the target comes back empty. I checked that test
bites: with the skip removed, `/messages` returns 500 instead of a page.

`make verify` and `golangci-lint` clean. Complement filtered run unchanged: `TestRedact` and
the sync tests pass as before, so the added `unsigned` copy and the withheld redactions did not
disturb what other implementations expect on the wire.

## 2026-11-05 — a remote user joins: the resident half of the handshake (T-4009a)

Federation had transactions, ingest checks, an event read and an outbound queue, but no way for
a user on another server to become a member of a room held here. This closes the resident
server's half of that: `/make_join` (v1 and v3), `/send_join` v2, `/state_ids` and `/event_auth`.

The shape of the handshake is the point of it. The resident server answers `make_join` with an
*unsigned* template carrying what the joiner cannot invent — the room's version, where the
history ends, which state authorises a join, the depth — and refuses there if its copy of the
room does not permit the user. The joiner fills in `origin`, `origin_server_ts` and `event_id`
and signs; only then does the resident accept. It checks the signed event against what the URL
claims (same room, same user, membership `join`, and an event ID it recomputes rather than
trusts), stores it, signs it with its own key, and answers with the state *before* the join plus
the auth chain that lets the joining server check the room instead of believing it.

Two decisions worth their weight: the resident signs the event it accepted, because it is the
one attesting that this join entered the room and every other server in the room should see that
attestation; and the state handed back is the pre-join state, because answering with the
post-join state asks the joining server to authorise its own join against a room that already
contains it. Federation answers with stored bytes, not the client view — the redaction servicer
is for clients, and a peer checks the content hash the origin recorded.

The first remote join ever attempted here exposed a bug in the auth rules. Rule 4.3.1 ("if the
only previous event is an `m.room.create` and the state key is its sender, allow") is an
allowing clause; our code refused when the single `prev_event` could not be resolved. Since
`prev_events` are never among the `auth_events` a receiver is handed, a server checking an
event against its cited auth events can almost never resolve them — so every remote join except
the creator's own was being refused. Fixed, with cases in the auth-rule table so it cannot
quietly come back.

Evidence: `tests/integration/federationjoins_test.go` — the handshake end to end (bea joins
alice's room; hs1 lists her as present; the room bundle is handed to hs2, which accepts it and
then shows her the room), plus refusals: invitation-only room and a ban answered 403 before any
signing, an unknown room 404, an unsigned ask 401, a tampered join refused and the user *not*
listed afterwards, a leave offered as a join refused. `TestStateAndAuthChainAreAnswered
ToServersInTheRoom` checks the reads answer 403 to an outsider and 200 afterwards, and that an
event's auth chain does not contain the event itself.

Left open honestly: `m.room.server_acl` is not enforced anywhere, and the federation handlers
call a check that allows everything and says so in its comment (T-4009c, REQ-FED-012); the
joining half (T-4009b) needs the snapshot ingest of T-4007, because a handed-over room arrives
as a bundle and this server's ingest refuses an event whose predecessors it does not hold.

`make verify` clean, `golangci-lint` clean, Complement filtered run unchanged.
