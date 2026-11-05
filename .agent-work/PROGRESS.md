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
