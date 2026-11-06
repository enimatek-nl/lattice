# Progress

## 2026-11-08 — MSC4115 lands, the suite tally collapses, and one of my own ledger claims was wrong

Commit `ad8499a`. `unsigned.membership` on served events is the **reader's** membership of the
room as of the moment the event was sent, read out of the state snapshot that event was sent
against: `leave` when the snapshot holds no member entry for the reader, **no field** when this
server has no snapshot of the event at all, and a value already under `unsigned` is left alone
because that is where a peer records what *it* added around an event. `/sync` (timeline and
state), `/messages` and `/event` pass the reader; `Serve`/`ServeChunk` stay reader-free for
callers with no reader to name, and no federation path runs through this code at all.

Complement, `.agent-work/next/msc4115c.log` and `msc4115d.log`: `TestMembershipOnEvents` passes;
`TestGetRoomMembers`, `TestGetRoomMembersAtPoint`, `TestSendAndFetchMessage`,
`TestFetchMessagesFromNonExistentRoom`, `TestRoomMessagesLazyLoading(+LocalUser)`,
`TestSyncFilter` and `tests/csapi` `TestSync` pass unchanged; `tests/msc4222` `TestSync` fails its
two `state_after` subtests as it has before. Local `go test ./tests/...` green.

Found while in there: `ServeChunk` paired decoded events with stored rows by `len(out)`, so after
any withheld event every later event in the chunk got the **previous event's** `unsigned` —
someone's `prev_content` or transaction ID attached to a different event's body. Now paired by
position. Fixed without a test, which is recorded as T-4037 rather than waved away.

Then the environment, where my earlier conclusion was wrong. The deploy failures that made the
full-suite tally meaningless are `allocating lock for new **volume**: exceeded num_locks (2048)`:
**2044** anonymous volumes left behind by removed homeserver containers, most of them from one run
I interrupted with `pkill`, had eaten Podman's entire lock pool. Cleared only the ones no live
container mounts (2044 gone, 1 left, both pre-existing containers still up). That also invalidates
what B-0008 said about the rootless lock set being immovable — that error was an artefact of my
own `num_locks` edit, which is gone. Consequence for the records: `gate4.log` (59 pass/161 fail)
and `full2.log` (67/152) are noise; T-4039 is the re-run.

And a correction to the compliance matrix, because the error was mine and it was in the direction
of looking better: it claimed `TestTyping` was newly passing. It never was — the log I read for it
was a run of the transaction-ID filter, which contains no typing test. Typing is not implemented
client-side at all: `/sync` builds `ephemeral` as an empty list by construction
(`internal/sync/service.go:320-330`), and typing EDUs received over federation are never stored or
replayed. Re-run twice today it fails all three delivery subtests, consistently (T-4036). The same
sweep removed a citation to a Complement helper (`assertMembershipEventEventsPresent`) that does
not exist in this checkout. T-4034's description is also softer than the evidence:
`SendEventSynced waiting for event ID` appears 16 times in `prevbatch1.log`, so it is routine
rather than occasional, even though the tests that show it still pass.

## 2026-11-08 — the MSC4115 lookup lands, and the full-suite tally turns out not to be a tally

`persist.StateGroupRepo.StateEntryAt(eventID, type, stateKey)` (commit `64135dc`) answers what a
state key held in the snapshot an event was sent against, in one statement, and distinguishes "the
snapshot held nothing" from "this event has no recorded snapshot" — the second has to stay
distinguishable or a server starts inventing history. `go test ./tests/...` green;
`TestStateEntryAtAnswersAboutTheMomentNotTheMomentAfter` covers a message sent while bob was joined,
asked after he left: still `join`. The presentation half of T-4032 is written out as steps in
TASKS.md rather than left to be rediscovered.

Then the full-suite run, and the discovery that the run itself is the problem (B-0008, T-4035):
`RUN='.'` reports 59 top-level passes and 161 failures, but 15+ of the sampled failures are
`ContainerCreate: … exceeded num_locks (2048)` at **deploy** time, with 0.4-0.9s durations — tests
that never reached a homeserver. `TestUnknownEndpoints` and `TestRoomSummaryAllowedRoomIDs` are
among them and both passed in a filtered run 40 minutes earlier, so the number is not a compliance
measurement and is recorded as such.

Measured on the way: raising `num_locks` is refused at 4096 and above because the rootless lock set
`/libpod_rootless_lock_1000` already exists at 2048 and Podman will not resize it; that needs
`podman system reset`, which deletes images and containers this machine also uses for other work, so
it was not run and the config file was removed again (host verified back to its prior state:
`docker ps` shows the two pre-existing containers). No orphaned `conmon`, no stopped
`complement_*` containers, nothing to reap. `scripts/complement.sh` now defaults to `-p 2` and prints
it, which helps but does not make a whole-suite run possible here.

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

## 2026-11-05 — one of our users joins a room held elsewhere (T-4009b)

The resident half of the join handshake was in place; a local user asking to join a remote room
still stopped at the door. This is the joining half, and the piece it could not work without:
a room handed over by `/send_join` arrives as a *set* — the room's state, its auth chain, and the
join — whose members cite each other, and whose join cites the room's forward extremities, which
are not in the set at all. The existing ingest refused any event whose predecessors it did not
hold, which is right for a transaction and fatal for a handover: it would refuse the join of
every room that has ever held a message.

So a bundle is now taken as a snapshot (`internal/rooms/bundle.go`, DEC-028): every event is
verified on its own — nothing about a bundle makes it trusted as a whole — each is authorised
against the auth events it cites, whether those are stored here or arrived in the same bundle,
and the bundle is applied in dependency order by walking it to a fixed point. An event accepted
without its predecessors is stored with `outlier = true`: its state counts and it can authorise
later events, but this server does not know where in the history it sits, so it stays out of
client timelines and out of the room's forward extremities. The room's current state is not used
to soft-fail any of these events, because in a handover the current state is what the bundle is
building. A missing citation is a gap and is deliberately not recorded as a refusal — recording
it would make the gap permanent, so the same event could never be accepted after the missing
piece arrived. A forged event is a verdict: refused, and remembered.

On the joining side (`internal/federation/join.go`): the client's repeated `server_name`/`via`
parameters name who to ask, with the server the room ID names as fallback. A template is checked
before it is signed — this room, this user, membership `join`, and no `event_id`, `hashes` or
`signatures` already on it, because signing one of those means adding this server's name to an
event somebody else made. A resident server's refusal on the room's rules is not retried against
another server: that would be looking for one laxer about the room's join rules. A room ID that
carries this server's own name is answered 404 rather than asked of a stranger, which is how a
stranger would otherwise get a room invented under this server's name.

Evidence: `tests/integration/joinremote_test.go`. A user on hs2 joins a room on hs1 through the
Client-Server API; hs1 lists them as present; hs2 holds the room's foundations, its name, and
its own user's join in state; the handed-over message is *not* in hs2's timeline — which is the
outlier rule being honoured, and the assertion T-4009b1 will have to change; and a message the
joining user sends from hs2 is heard on hs1 through the relay. A join nobody answers invents no
room. A forged event in a bundle is refused and its refusal recorded.

Also fixed on the way: `App.Rooms` was declared but never assigned, so the assembled homeserver
exposed no room service to anything holding the app — the reason the bundle test needed it.

Left open and recorded: T-4009b1 (lifting the outlier flag by fetching what a handed-over room
cites, which is what will make joined history visible), T-4009c (`/make_leave`, `/send_leave`,
`/invite`, knock, and `m.room.server_acl`).

`make verify` clean, `golangci-lint` clean, Complement filtered run unchanged (TestRoomCreate,
TestSync and TestSyncTimelineGap were failing before this and still are).

## 2026-11-05 — a joined room's history is fetched, and an outlier stops being one (T-4009b1)

A room joined from elsewhere arrived with a hole under it by construction: the join cites the room's
forward extremities, and the state a resident server hands over does not include them. The events
that fill it are real, so they were stored — and kept from clients until they could be placed. This
is the pass that asks for them.

The holes are named from this room's own events, not from a guess about the room: an edge that this
server's events cite and no stored event answers (`persist.MissingPredecessors`, newest-first,
bounded). A server that was in the room before this one is asked for each, over `GET /event`, and
each answer names the events before it — so the walk goes backwards until it reaches history this
server already holds, which is where it stops rather than a hard limit. What comes back is ingested
as one bundle, ordered parents first: this server gives an event a position in its own ordering when
it stores it, and a chain fetched newest-first would otherwise be read oldest-first by a client
asking for the newest events — a client would be told that the sixth message came before the first.
Then `rooms.ResolveOutliers` clears the flag from every event whose predecessors are now held,
walking to a fixed point because a chain is usually linked from one end.

Two things were pinned deliberately. The fetch is bounded by an event budget and a pass count, and a
bounded fetch leaves the remainder *named and askable*: nothing is refused for want of budget, and
the next pass finishes the job. And a predecessor that is stored but soft-failed still counts as
held — soft-failure is a judgement about a conflict in the room's state, not about whether the event
sits in the history, and holding a perfectly good successor unlinked for it would claim something
about the graph this server has not established.

Evidence: `tests/integration/joinremote_test.go`. `TestBackfillMakesAJoinedRoomsHistoryReadable`
joins a room with a run of six messages on the other server and then reads all six, in the order the
run happened, on both servers; no event in the room is left unlinked; the join cites the room's last
message, and a message sent afterwards cites the join — so the branch continues the room's history
instead of starting a fork that someone has to merge later. `TestBackfillBudgetLeavesAGapAskable`
joins with a budget of two events: it gets the end of the history, does not show what it never
fetched, still has an outlier and a named gap, and a second pass with room in the budget makes the
whole run readable with nothing left unlinked. `tests/unit/eventstore_test.go`
(`TestOutliersAndMissingPredecessors`) checks the queries: a hole is named once whatever cites it,
newest-first, bounded, and a hole in another room is not this room's — that last one caught a
query that had no room scope at all, because edges carry no room of their own.

Left open and recorded: T-4009b2 (only a join triggers a fetch today — no worker pass, no fill when
a client pages into a gap, and we do not answer `GET /backfill` ourselves) and T-4009c (leave,
invite, knock, server ACLs).

`make verify` clean (`-race`), `golangci-lint` clean. Complement was re-run for the previous commit
and its filtered set is unchanged; this change touches no Client-Server API surface, only what a
joined room holds behind it.

## 2026-11-05 — a room can exclude a homeserver, and it is worth something at every door (T-4016, REQ-FED-012)

`m.room.server_acl` had been left as a call site that allowed everything and said so. This is the
rule behind that call site, read once into `internal/serveracl` and applied where a room is
described to a server, where a server pushes an event, and where this server decides whom to tell.

The decision is made in one place because it is asked in several: `/make_join` (v1 and v3),
`/state_ids`, `/event_auth` and `/event` answer an excluded server with 403 `M_FORBIDDEN`; each PDU
of a transaction is refused per event while the transaction itself is still answered; the room
service refuses an event whose origin the room has excluded and records the refusal; and the relay
does not queue a room's events for a server the room threw out. The room's state is what says so —
read at the moment of the request, so an exclusion takes effect and lifts again with the state that
made it.

Two things the specification settles and two it does not. Globs are `*` and `?` over the server name
without its port, case-insensitive, and `deny` is consulted before `allow` and wins: an `allow`
entry cannot rescue a name that `deny` covers. An ACL with no allow list excludes every server,
which is the specification's own consequence and is also what a redacted ACL event leaves behind.
What the specification does not settle is a field of the wrong shape, and this server declines to
apply such an ACL rather than reading a mangled `allow` list as an absent one — every field is
optional, so a typo in one field would otherwise cut a room off from federation entirely. That is
fail-open by choice, stated in the requirement (DEC-031) and pinned by a test that keeps a room
working through a garbled ACL.

A protocol fault turned up on the way and is fixed: events this server creates did not name their
`origin` server. `SendEvent` and `newStateEvent` both set `origin_server_ts` and neither set
`origin`, and the omission survived at home because signature verification here keys off the
sender's server name rather than the origin field. A PDU without an origin cannot be traced to
whoever made it — it is what a peer checks a signature against, what a room's server exclusion is
matched against, and what some rooms' auth rules read — so both constructors name it now, and
`TestLocallyWrittenEventsNameTheirOriginServer` checks a message and a create event for it
(REQ-EVT-008, DEC-032). Two redaction tests had to be told that the unredacted form they
reconstruct carries `origin` too, since the newer room versions hide that field in the redacted form.

The ACL tests also found a gap worth recording rather than papering over: while a room excludes a
server its messages are withheld, and once the exclusion is lifted the next message is refused by
the excluded server because it descends from events it was never told about. The room is reachable
again; the hole in the middle of its history is not filled. That is T-4009b3 — asking for the
predecessors an incoming event cites, which is the specification's `/get_missing_events`, on
machinery `federation.Backfiller` already provides.

`make verify` clean (`-race`), `golangci-lint` clean. Complement: the filtered `csapi` set was run
at the previous commit and this change touches no Client-Server API surface; the server-ACL tests
are federation-path tests and Complement has no `federation_*` run against this server yet.

## 2026-11-05 — a hole under an incoming event is asked about, not judged (T-4009b3, REQ-FED-016)

An event that arrives describing itself as descending from events this server does not hold has two
bad answers and one good one. Refusing it is a verdict about an event that may be perfectly good,
judged by the state of this server's copy of the room. Storing it as an outlier without any way to
ask leaves it invisible to clients for as long as the hole stays. The good answer: name the missing
events, ask the server that just offered the event for them — it plainly has them — and offer the
event again.

`AcceptRemoteEvent` now returns `*rooms.MissingPredecessorsError` with the IDs, and the transaction
handler answers it with one bounded ask (`Backfiller.FillFrom`, seeded by exactly those events and
walking backwards from them) followed by one retry. One ask and one retry: a sender that keeps
offering unplaceable events must not turn each offer into a fetch. What comes back is ingested
parents first, which is what keeps the chased event from reading as newer than the event that
triggered the chase — the ordering rule of DEC-030 doing its job in a second situation.

The test that proves it is the server-ACL test, because an exclusion is a convenient way to make a
hole honestly: a room excludes a server, messages are withheld during the exclusion, the exclusion is
lifted, and the next message is refused for its hole. After the ask, both halves are held, the
timeline reads in the order the messages happened rather than the order they arrived, and the room is
caught up rather than permanently forked.

The specification's own endpoint for this — `GET /get_missing_events` — is still not implemented, and
this server does not answer `/backfill` either; the ask travels over `GET /event`, which reaches the
same events. Recorded under T-4009b2 and T-4012 rather than counted as done.

`make verify` clean (`-race`), `golangci-lint` clean.

## 2026-11-05 — chasing a Complement failure, and finding two real defects (DEC-034, DEC-035)

`TestSyncTimelineGap` has been in the failing set for a long time and reads like a sync bug. It is
not one. Following it produced three findings, two of which are fixed here.

The first was environmental and worth writing down anyway: in Complement's federation tests the
second homeserver is the host machine at `host.docker.internal`, which resolves to a private or
link-local address that this server's outbound policy — the SSRF protection — rightly refuses to
dial. The Complement image now names the ranges the harness lives on instead of switching the policy
off, which keeps the protection meaningful in a real deployment and the tests runnable.

The second was a real defect. The transport path already verified a peer's certificate against the
peer's *name* while dialing the address that name resolved to; the key endpoints did not, so they
verified against the IP. A homeserver with an ordinary certificate naming its domain therefore could
not have its signing keys fetched at all — and the failure surfaced as `M_UNAUTHORIZED: the request
is not signed by a server this server trusts`, which is about as far from the truth as an error
message can get. One helper now hands out name-verifying clients to both paths, and it clones the
library's default transport when the base client has none: without that the fix would have been a
no-op in exactly the deployment it was written for.

The third was a protocol judgement. `/make_join` had been refusing any ask whose `ver` differed from
the room's version. The specification reserves `M_INCOMPATIBLE_ROOM_VERSION` for a version this
server cannot speak; which version a room uses is the very thing the asker is calling to learn. A
speakable-but-different ask is now answered with the room's own version, which the response names, so
the asker signs what the room actually uses.

The test still fails, and honestly so: the join now completes its first half and stops at
`send_join`, where the harness's version 11 room arrives with no `event_id` — pseudo-IDs (T-4018) are
not implemented here. Recorded rather than papered over. The rest of the filtered Complement set is
unchanged, and `make verify` is clean.

## 2026-11-05 — three defects behind one Complement test, and it passes now (DEC-036, DEC-037)

`TestSyncTimelineGap` went from failing to passing, and getting there meant finding three things
that were genuinely wrong rather than making the harness happy.

**The join handshake was being refused at random.** `/make_join`'s `ver` parameter is repeated once
per room version and names the versions the *asking* server can speak. This server read the first one
out of an unordered list and compared it with the room's version, so a peer listing `3`, `9`, `11`
and a proposal version was answered or refused depending on which came first. The rule now is the one
the specification describes: is the room's own version in the set? If it is, the template goes out in
the room's version; if it is not, `M_INCOMPATIBLE_ROOM_VERSION` carries the room's version, because
naming the version the asker cannot speak is the only useful thing a refusal can do. A server that
names no versions is answered rather than refused on the documented `[1]` default — holding it to
that legacy artifact would leave every room from version 3 on unjoinable by a server that simply
asks. DEC-035, recorded earlier in this window, was wrong in detail and is marked superseded by
DEC-037.

**An event without an identifier was refused, although the room version computes one.** Every room
version from 3 on defines the event ID as the reference hash and does not list `event_id` among the
required properties of the event format. So a PDU arriving without one is complete, and the receiver
names the event by hashing it — the duplicate check, the verdict and the stored copy all use that
name. A PDU carrying a label its own content contradicts is the same event wearing a wrong label: it
is filed under the hash, and the disagreement is logged rather than treated as a verdict. What is
still refused, whatever the label says, is an event whose content hash or origin signature does not
check. Room versions 1 and 2 are the opposite case and still require the sender's identifier, which
is why the unit test covers them.

**The room was left half-empty.** The combination of the two above is what a user sees: an event
refused for a missing label is a message that never arrives, in a room this server was told about.

Complement's filtered set now runs `TestSyncTimelineGap` green repeatedly; `TestRoomCreate` (room
versions 1 and 2, refused by design — T-4013) and `TestSync/parallel/Device_list_tracking` (device
lists over federation, T-4009c) are the remaining failures in that set. New tests:
`tests/unit/eventid_test.go` (every known room version),
`tests/integration/federation_test.go` (`TestEventOfferedWithoutItsLabelIsNamedByItsContent`) and
`tests/integration/federationjoins_test.go`
(`TestMakeJoinReadsEveryVersionTheAskerLists`) — the latter two were confirmed to fail against the
code before the change. `make verify` clean under `-race`, `golangci-lint` clean.

## 2026-11-08 — room versions 1 and 2 are offered, with the rules those versions actually have (T-4013, cycle 17)

Room versions 1 and 2 have been the oldest open Complement failure since cycle 4:
`TestRoomCreate/creates_a_room_with_the_given_version` asks a homeserver to make a room in version 2
and this server answered `M_UNSUPPORTED_ROOM_VERSION`. It was refused because supporting it honestly
means implementing the parts where those versions differ, and nothing less than that is worth
shipping. It now passes, along with the rest of `TestRoomCreate`.

What "the parts where they differ" turned out to be, in the order it was found:

* **The authorisation table those two pages share is not the later table early.** It is a separate
  ruleset (`eventauth.RulesV1V2`) because the clauses disagree in places that decide real events:
  a room whose `m.room.create` has no `join_rule` and no `m.room.join_rules` event admits the join
  (rule 4.4 — the later tables removed that clause, and Synapse has an equivalent accommodation
  through its `room_version → None` join-rule mapping, so this is not exotic); the membership clauses
  are keyed on the *event's own* sender-server name rather than the room's, which is also where the
  "origin must match the room" clause of later versions used to be; a redaction is allowed by the
  moderator clause **or** because the redacted event's event ID sits in the sender's domain; and the
  invite clauses care about the target's domain (version 1) or about the target having already left
  (version 2). The v1/v2 power levels rule also tolerates a top-level `users_default`, which later
  versions dropped.
* **State Resolution 1 is a different algorithm** (`internal/state/stateresolutionv1.go`): conflicts
  resolved one state class at a time — power levels, join rules, memberships, then everything else —
  with depth and the SHA-1 of the event ID as ordering keys. It is implemented because rooms in that
  version exist and their state has to be computed the way that version defines it, not because the
  algorithm is good; its documented failure modes are written in the file's comment. Noted honestly:
  Synapse resolves those rooms with a richer procedure (auth difference, power events, reverse
  topological order). The two agree on an unforked room and can disagree on a forked one; this server
  follows the pinned specification page, and the divergence is recorded in
  `docs/compliance-matrix.md` item 5 rather than discovered later by an operator.
* **The identifier is the origin server's to choose, and it is inside what gets signed.** Every
  outbound path had to mint an ID before sealing: local send (`rooms.prepare`), the join handshake
  (the joining server mints its own; the resident server's template carries one, and `send_join`
  takes the identifier from the event it was sent because that is the event the joiner signed), and
  backfill. In a room whose version names an event by its hash, an incoming event with no `event_id`
  is named by its content (DEC-036); in these versions there is nothing to compute, so a missing
  identifier is a rejected event. `events.Seal` now refuses rather than signing something that cannot
  carry the name it will be stored under.
* **References are pairs in these versions** — `[event_id, {"sha256": hash}]` — and that shape is
  inside what gets hashed, so it cannot be normalised away. `events.References`/`ReferencedIDs` are
  now room-version aware, and the state resolver, the graph walk and the auth checks read references
  through them.
* **The auth chain ends where this server stops understanding the room.** An auth event from an
  unrecognised room version is still required to resolve, so an unresolvable one means the chain is
  incomplete rather than merely short (T-4013 in the ledger). A chain that stops early would let a
  forged event be checked against state the chain never reached.
* **Power levels in those versions may be written as strings, or as floats to be truncated**
  (`"100"`, `" 00100 "`, `"+25"`, `50.57`). The loader reads them only in the versions that allow it.
  A room whose moderators were written that way is a room with moderators; treating the values as
  absent would have quietly demoted everyone in it.

Two defects found while doing this, both wider than room versions 1 and 2:

* `events.ReferencedIDs` skipped pair-form entries entirely — it only ever read bare strings. That is
  the function the graph walk uses for `prev_events` in *every* room version, so a reference written
  in the pair form was invisible: the event looked like it had no parents.
* The server ACL check on a join template did not run for a room in version 1 or 2, because their
  templates carry no `event_id` and the ACL code uses the event ID domain to decide whose room is
  being templated. The rule reads the *room ID* domain in that case; a banned server can no longer be
  handed a join template for a version 1 room it was excluded from.

Verification: `make verify` clean, including `-race` and `-count=2` (`tests/unit` 1136s of test
work, `tests/integration` green); `go vet ./...` clean; `gofmt -l` clean. New tests:
`tests/unit/authorisation_legacy_test.go` (3), `tests/unit/stateresolvev1_test.go` (6 plus
subtests), `tests/unit/powerlevels_legacy_test.go`, `tests/unit/eventid_test.go` (2),
`tests/unit/chain_test.go` (+2 cases), `tests/unit/eventauth_test.go` (+1), and
`tests/integration/roomversionlegacy_test.go` (2): rooms created in versions 1 and 2, their events
named `$opaque:hs1`, every `prev_events`/`auth_events` entry fetched over federation checked to be a
pair whose hash recomputes to the referenced event's real reference hash, and a full federation join
handshake into a version 2 room. `tests/unit/eventsignature_test.go` now seals and verifies in every
offered version, writing each version's references in that version's shape and minting the identifier
where that version requires it first.

Complement: the **full** `./tests` + `./tests/csapi` suite was run for the first time since the
earliest record in this ledger, as a baseline rather than a filter: **60 top-level tests passing,
136 failing** (Run 1, five cycles ago: 8 and 189). `TestRoomCreate` is green including
`POST_/createRoom_creates_a_room_with_the_given_version`; `TestRedact`, `TestServerCapabilities`,
`TestVersionStructure`, `TestCannotSendNonJoinViaSendJoinV2` and `TestCumulativeJoinLeaveJoinSync`
are green. The room-version tests that stay red are the ones about work not done: the MSC4297 state
resolution v2.1 pair (room version 12), `TestMSC4289PrivilegedRoomCreators_Upgrades` and
`TestMSC4291RoomIDAsHashOfCreateEvent_UpgradedRooms` (version 12 formats), and
`TestOutboundFederationIgnoresMissingEventWithBadJSONForRoomVersion6`. No Complement assertion was
modified.

## 2026-11-08 — an upgrade into a version this server only recognises is refused (T-4021)

Offering room versions 1 and 2 made the offered set wider than it had been, and looking at what else
reads that set turned up a doorway that had never been limited to it: `PUT /upgrade` checked
`roomversion.Get` — "do we have rules for this version?" — rather than "can we run a room in it?".
Room version 12 has registered rules and an unimplemented revised state resolution, so a room could
be upgraded into it: it would exist, keep accepting events, and be a room this server could not work
out the state of, while every other server in it computed something else. `roomversion.IsOffered`
now answers that question once, the upgrade path uses it, and the refusal is the same
`M_UNSUPPORTED_ROOM_VERSION` creation already gives.

`make verify` clean. Tests: `TestRoomUpgradeErrors` asks for `new_version: "12"`; `roomversion_test.go`
asserts that the advertised set and `IsOffered` agree, and that a known version is not automatically
an offered one.

## 2026-11-08 — a homeserver answers for its own users' keys, and discovery stops treating a broken resolver as an absent server

Three things, all found by following Complement failures instead of guessing at them.

**The Complement image was refusing to dial the peer container.** The federation address policy
denies private ranges on purpose — that is the SSRF protection — and the image's exception list named
`127.0.0.0/8, 172.16.0.0/12, 169.254.0.0/16`. On this machine the runtime is podman, whose created
networks are `10.89.0.0/16`, so every hs-to-hs dial in the suite was refused with "no allowed network
covers", and the failure looked like a protocol bug. Naming every runtime's pool would break the next
time somebody runs the suite somewhere else, so the entrypoint now allows the networks the container
is *attached to*, which is exactly where a peer homeserver in these tests can be, and nothing else
private.

**A resolver that cannot read SRV records is not an absent server.** `Resolve` treated any SRV lookup
error as fatal. Container DNS answers `_matrix-fed._tcp.hs1` with a timeout, while the name itself
resolves and the server is listening on 8448 — so a perfectly reachable peer was uncontactable. The
specification's own order is service records, then the name on the federation port, and an unreadable
record now leads to the second step. Separately, a name that could not be looked up at all was cached
for `wellKnownErrorTTL` — an hour, which is the specification's cache time for a *well-known
delegation document*, a thing a server publishes deliberately. A failed lookup is not that kind of
fact: one resolver hiccup made a healthy peer unreachable for an hour, with an error message ("was
resolved recently and could not be found") that reads like a durable fact about the other server. The
lookup failure now has its own 30-second window: long enough that a burst of traffic to one bad name
does not hammer DNS, short enough that the next attempt happens in the same shift.

**A homeserver answers for the key documents of its own users, and asks its peers for the rest.**
`POST /_matrix/federation/v1/user/keys/query` is served, and `POST /client/v3/keys/query` for a user
who lives elsewhere now reaches out instead of answering "no keys" — which is not a neutral answer,
because a client reads it as "this user has no devices". What is checked is the shape of the answer:
only the users that were asked about, only those whose IDs sit in the namespace of the server that
answered, and the documents themselves untouched, because the device signed them in that shape and
the client is the one entitled to check that. One unreachable peer leaves that user's entry empty
rather than failing the whole query, per the rule that a dead remote must not block local work. The
inbound half refuses to answer for users that are not its own, which the new test checks directly by
asking a server for somebody else's user. While at it, `GET /account/whoami` now names the device the
token belongs to: keys, push rules and the device list are per-device things, and a client that
cannot tell which of its own sessions it is in cannot manage them.

Evidence: `make verify` clean. New tests — `TestFederationDiscoveryRetriesALookupFailureSoon` and
`TestFederationDiscoverySurvivesAnUnreadableSRVRecord` (unit),
`TestRemoteDeviceKeysComeFromTheirOwnServer` (integration, two homeservers: the key document arrives
byte-for-byte, and a server asked for another server's user answers with nothing), and the existing
`whoami` assertion extended to the device identifier. Complement's full `./tests` + `./tests/csapi`
suite: **62 top-level passing, 134 failing**, up from 60 and 136 earlier the same day, with two tests
newly green (`TestJoinViaRoomIDAndServerName`,
`TestUserAppearsInChangedDeviceListOnJoinOverFederation`) and none newly failing.

The device-list tests still fail, and the cause is now measured rather than assumed: they used to stop
at the key query (0.4s in) and now stop at a `/sync` wait for a device-list change that nothing
delivers (5s in). That is `m.device_list_update` EDUs in both directions, opened as T-4022.

## 2026-11-08 — a device list change on one server reaches the clients of another (T-4022), and a timeout middleware stops racing with the handler it timed out

**The channel.** `m.device_list_update` ephemeral units are now sent and acted on. Out: when a local
user's devices change (the key-upload path already recorded the local half of that, so it was one
call away) and when a local user joins a room held elsewhere. In: a received announcement is recorded
for the local users who share a room with the user named, which is the same question the local
announcement answers, so the same recording is used. The transport was already there — the sender
queue carries EDUs — so this is the two ends and the trigger, not a new subsystem, which is what the
architecture note in DEC-017 anticipated.

**The rule that makes receiving it safe.** A peer is believed only about users whose IDs sit in that
peer's own name. A transaction carrying an announcement for `@mallory:hs3` from `hs2` is still
accepted — the peer did nothing wrong by sending it, and refusing the transaction would punish the
other units inside it — but the announcement does not become a fact here. Without that rule any
server could push a stranger into every local client's key tracking, and a client that tracks a user
starts trusting that user's keys.

**What is not stored, and why.** The key document inside the announcement is not kept, and the update
number is not compared against a stored one. Numbers and copies guard state this server does not
keep: a query about a remote user is answered by asking that user's server, which holds the signed
original. A duplicate announcement therefore costs a client a key query and nothing else, while a
stale local copy would be a place for a signed fact to go wrong. The per-user update *number* this
server announces is kept, in its own table, because a number that restarts with a device count going
backwards makes later announcements look stale to a peer that already applied a newer one.

**A separate trigger the first run exposed.** Joining a room that another server holds stores the
join as part of the bundle that server hands over, and an adopted bundle deliberately does not run the
side effects of a locally accepted event — running a peer's whole handover through the room's effects
would echo it back into the room. But the user who just joined is exactly the fact the other servers
have not been told about, so the join handshake announces it directly.

**A race that had been waiting.** `make verify` under `-race` began failing, and the report was not
in the new code: `httpserver.Timeout` writes a Gateway Timeout from another goroutine when the
deadline passes, and the deadline also passes when a client hangs up. The handler kept a promoted
`Header()` pointing at the same response that the timeout goroutine was writing — a data race on a
map. The guard now keeps the handler's headers private and commits them to the real response at the
first write, so a handler still working on a request nobody is waiting for cannot write a response
that has already been sent. This was latent, not new: the new EDU work only made it likely enough to
be caught.

Evidence: `make verify` clean, with zero `DATA RACE` reports (it had four). New tests:
`TestDeviceListChangesCrossTheFederationLink` (two homeservers: an upload on one shows up in the
other's `/sync`; a forged announcement for another server's user does not; the same shape naming the
sender's own user does — otherwise the forged case would only prove announcements are ignored) and
`TestDeviceListStreamsIncreasePerUser` (per user, strictly increasing, survives a restart).
Complement full `./tests` + `./tests/csapi`: **64 passing, 132 failing**, up from 62 and 134, with
`TestDeviceListUpdates` (all ten subtests) and
`TestDeviceListsUpdateOverFederationOnRoomJoin` newly green and none newly failing.

## 2026-11-08 — an invite for a user who lives elsewhere is countersigned by that user's server (T-4009c)

Commit `fb11648`.

An invite named a user on another homeserver used to be refused with 501. The handshake now
exists: the inviting server builds the event, the invited user's server signs it as well, and only
the twice-signed event is stored anywhere.

Sending (`internal/rooms/invite.go`, `internal/federation/invite.go`).
`Service.inviteRemote` builds the member event with the room's own prev events, the auth events the
room's rules select, the depth and origin the room expects, and refuses before asking if the room's
power levels would not allow the invite in the first place. `unsigned.invite_room_state` carries the
state a client needs to recognise the room — create, join rules, name, avatar, canonical alias,
encryption, power levels — plus the member events, because the inviter's own membership is what
authorises the invite and the invited user's previous membership is what an invite may be lifting
(a ban, for instance). `federation.Inviter.SendInvite` asks `PUT /_matrix/federation/v2/invite/
{roomId}/{eventId}` and falls back to v1 only for a room version 1 or 2 room and only when the
answer says the endpoint is unknown: a 403 that gives a reason is a verdict, and asking twice would
only ask the same question twice. The event that comes back is compared with the one that was sent —
type, membership, sender, state key, room — because only signatures may differ, and a peer that
returns a different event is not countersigning this invite. Nothing is stored unless the answer is
right, and a peer's refusal is answered to the client as a gateway failure (`matrixerr` grew a
`Status` override for exactly this: an `M_UNKNOWN` on its own reads as this homeserver's fault, and
the operator would look in the wrong place).

Receiving (`internal/api/federation/invites.go`, v1 and v2). The checks are about what this server
would be promising: an `m.room.member` invite, about a user who lives here, whose sender lives on the
server that sent it, in the room the path names, with the event ID the path names. v2 names the room
version; v1 has nowhere to put it, so a v1 invite for a room this server holds is judged by its own
record and one for a room it does not hold is taken as a version 1 room rather than a guess at a
newer one. The inviting server's signature is verified *before* this server seals the event, because
afterwards a verifier cannot tell whose word is whose.

The room an invite names may be new here. It is created with the version the request states, and the
state that arrived inside the invite is kept as events and current state — no event graph, no forward
extremities, because this server holds no history of that room and pretending otherwise invites
pagination into a room it knows one event of. Without it the invited user could read the invitation
but not answer it: the join or rejection that follows has to be authorisable, and the events it cites
have to be here. A bundled state event is used only when its own origin's signature checks; one that
does not is skipped and logged rather than treated as a verdict, because a peer whose older state this
server cannot re-verify (rotated keys, a redaction whose rules are applied differently) would
otherwise be unable to invite here at all. The invitation itself is signed by the server that holds
the room, and that is the promise worth checking.

Room creation keeps its shape. A room created "with Bob in it", where Bob lives elsewhere, is
created; the invitation is attempted afterwards and its failure is logged rather than returned. An
unreachable Bob should not destroy a room that now exists, and a client that is told the room exists
can retry `/invite`, where the answer is a status it can act on. `tests/integration/rooms_test.go`
now asserts that: the room is created, and no remote invite is stored for a user whose server never
countersigned it.

Complement evidence.
`TestFederationRejectInvite` passes: alice@hs1 invites charlie@hs2, delia (a Complement stub
already in the room) sees the invite arrive as a PDU, charlie rejects it, and the rejection reaches
delia too. That is the whole loop — build, countersign, store, relay — with a third party checking
the relay.
`TestFederationRoomsInvite` and `TestUnbanViaInvite` still fail, on two named gaps that are not
invites: `unsigned.prev_content` is not written on membership changes, and a ban or a kick for a
user who lives elsewhere is still refused with 501.
Full `./tests` + `./tests/csapi` counts are recorded in the next section.

Two local test expectations changed, deliberately, because the behaviour they pinned is gone:
inviting `@someone:elsewhere.example` is now a gateway failure rather than 501, and creating a room
with a remote invitee is now a created room rather than 501.

Complement counts after the invite handshake, for the record: `RUN=. scripts/complement.sh -v`
over Complement's whole `./tests/...` tree — every package, including the MSC-specific ones — is
**67 passing, 152 failing** (`.agent-work/next/full2.log`). The earlier 64/132 figure was a
narrower scope (`./tests` and `./tests/csapi` only), so the totals are not comparable; what is
comparable is that nothing previously green went red, and `TestFederationRejectInvite` joined the
passing set.

## 2026-11-08 — a state event shows what it replaced; a direct message is recorded where the specification puts it (T-4023, T-4024)

Commit `fcc4dae`.

**`unsigned.prev_content` / `unsigned.prev_sender` (T-4023).** The previous content of a state
event is a fact about the moment it was written, so it is computed at persistence
(`persist.eventRepo.recordPrevState`, called from `Insert` for state events): the current
`room_state` row for the same `(room, type, state_key)` is read *before* the caller records the
new state, its content — in the redacted form when a redaction was applied, because that is the
content a client compares against — is stored on the new row with the sender of that event. Two
deliberate omissions: a state event that repeats the content already in place records nothing
("the same content again" has no previous content, and reporting one would show a change that did
not happen), and a state event whose predecessor cannot read records nothing, which leaves it
looking like the first of its kind rather than inventing a history. Presentation is
`redaction.showPrevState`, in the same servicer that adds `unsigned.redacted_because`; the fields
never enter the stored bytes, so the signature still covers what it covered, and they do not
travel to other homeservers — a claim about our own state is not something a peer agreed to.

**`is_direct` (T-4024).** `createRoom` accepted the key and dropped it. Invitations now carry
`is_direct: true` in their content — local ones and the remote ones, where the countersigning
server has no other way to learn the flag — and the creator's `m.direct` account data gains the
room against each invited user, extending the existing mapping because account data is a current
value and overwriting it would lose the user's other conversations. It is not room state: a room
is a direct message for the people it was opened between, not for everyone who may join later.

**The account data endpoints.** `GET`/`PUT /_matrix/client/v3/user/{userID}/account_data/{type}`
were missing entirely: the server stored account data and synced it, but no client could read or
write it, so the `m.direct` mapping the server had just recorded was invisible to the user who
owns it. `internal/users.AccountDataService` and `internal/api/client/accountdata.go`; the
document is kept and returned as the bytes the client sent, because decoding it through this
server's structs would drop keys this server has no opinion about.

Complement evidence from the run after this change (`.agent-work/next/inv5.log`):
`TestWriteMDirectAccountData` passes (newly green), `TestFederationRejectInvite` still passes, and
`TestAddAccountData` now reaches room-scoped account data instead of failing on the user-scoped
half. `TestFederationRoomsInvite` no longer fails on `prev_content` or on the invitation: what is
left in it, and in `TestUnbanViaInvite`, is the remote ban/kick gap (T-4025) — `POST /kick` and
`POST /ban` for a user who lives elsewhere are still refused with 501.

Local tests: `tests/integration/prev_state_test.go` (three topic writes in the order second,
third, second; only per-event recording can report both predecessors, and the room's first topic
reports no predecessor), `tests/integration/direct_test.go` (the flag on the invitation, the
mapping extended rather than replaced, and an ordinary room filed nowhere).

## 2026-11-08 — devices can be signed into again; a transaction ID names one send in one room (T-4030, T-4031)

Commit `7157a39`.

Run of the M2 gate family (`txnid_test.go`, `.agent-work/next/txn2.log`): **4 of 5 pass** —
`TestTxnInEvent`, `TestTxnScopeOnLocalEcho`, `TestTxnIdempotencyScopedToDevice`,
`TestTxnIdempotency`. Only `TestTxnIdWithRefreshToken` remains, and it stops at the first request
because this server refuses `refresh_token: true` (T-4037).

Three separate causes, only two of them predicted by the diagnosis:

1. `POST /login` with a `device_id` the account already had created a *second* device under that
   name, and the unique `(user_id, device_id)` index answered `M_INVALID_PARAM "Device ID is
   already in use"`. A device ID names a device, not a session, so a login that names one the
   account holds now reuses it — display name kept when the client does not offer one, last seen
   moved, a new access token minted, and the sessions that device already had left alone. Device
   names stay per account: another account naming the same device gets a device of its own, which
   `TestLoginIntoAnExistingDeviceReusesIt` now asserts in both directions.
2. A transaction ID was unique per device, so the same name on a send to two rooms answered both
   with one event and lost a message. The key is `(device_id, room_id, txn_id)`; the migration
   drops the old index before creating the new one, because `CREATE UNIQUE INDEX IF NOT EXISTS`
   would otherwise leave a database written before this change constrained by the room-blind index
   forever.
3. `unsigned.transaction_id` was never served. It is now stored on the event row at the moment the
   send persists it and added by the same servicer that adds `prev_content` and
   `redacted_because`: never in the signed bytes, never to a peer homeserver. `event_txn` remains
   the uniqueness guarantee that makes a retry idempotent — the column is the copy a client is
   shown, and the two are deliberately separate.

Correction to the task list, from reading the test instead of its name: MSC4115's
`unsigned.membership` (T-4032) is the **reader's** membership as of each event, not the sender's —
see T-4032 for the evidence. It cannot be a stored column, and it belongs with T-4033's
membership-history lookup.

Local: `tests/integration/devicelogin_test.go` (three tests) and
`TestTransactionIDsAreScopedToDeviceAndRoom` in `tests/unit/eventstore_test.go`;
`go vet ./...` clean, `go test -race -count=1 ./tests/...` green.

## 2026-11-08 — the member roster a client asks for, at the point it asks for it (T-4033, T-4033a)

Commits `9abc815`, `066bb6c`, `2dcda9a`.

Complement, filter `TestGetRoomMembers|TestGetRoomMembersAtPoint|TestGetFilteredRoomMembers`
plus the sync and message families (`.agent-work/next/prevbatch1.log`):

- `TestGetFilteredRoomMembers` — all three subtests pass. `handleMembers` had read only
  `not_membership`, so `membership=join` and `membership=leave` were ignored.
- `TestGetRoomMembersAtPoint` — passes.
- `TestGetRoomMembers`, `TestSendAndFetchMessage`, `TestRoomMessagesLazyLoading`,
  `TestRoomMessagesLazyLoadingLocalUser`, `TestSyncFilter`, `TestSync` (csapi),
  `TestRoomSummaryAllowedRoomIDs` — pass, unchanged from before the change.
- `tests/msc4222` `TestSync` still fails its two `state_after` subtests; it failed the same way in
  the previous gate run, so it is not a regression from anything here.

Two things had to change, and the second only became visible once the first was right:

1. `GET /members?at=<token>` was reading today's state and cutting it by stream position. For the
   case `at` exists for, that has nothing to cut: a member whose join this server has replaced with
   a leave is not in the current state at all. It now reads the state snapshot the newest event at
   or before the token was sent against (`events.EventsWindow` → `groups.GroupOf` →
   `groups.Entries`) — the same snapshot the authorisation rules judged that event by, which is why
   the roster shown to a client cannot drift away from the room's own view of itself at that
   moment. A token whose snapshot was never recorded is refused; nothing had happened by another
   token gives an empty roster. The requester's own horizon still applies.
2. `/sync` was issuing `timeline.prev_batch` as the position of the **oldest** event in the batch,
   so a client asking who was in the room "at the point my batch began" was naming the create event
   and was told nobody — in a room it had just been shown the joins of. It now names the **newest**
   end, which is what Synapse issues and what Complement expects. The Client-Server API does not
   decide this (matrix-org/matrix-spec#595, open since 2020), so the reasoning and the cost — a
   client may meet an event twice paging backwards — are in the comment at
   `internal/sync/service.go:634-646`, not only in a commit message.

`tests/integration/members_filter_test.go` covers the three filters and three points in the
history, including the one the old code could not express: who was in the room while they were
there, asked after they have left.

Open observation, recorded rather than theorised: `SendEventSynced waiting for event ID …` appears
in some runs of `room_members_test.go:74`/`:88` and not others with the same blueprint, so a sent
event sometimes does not reach the sender's own `/sync` in time. That is intermittent, and it is
T-4034.

### In flight, for the next session to read

A full Complement suite run (`RUN='.'`, all packages) is writing to
`.agent-work/next/gate3.log`, started 10:37 UTC on 2026-11-06 with the tree at `96576d2`.
Read it when it ends for the honest tally; do not read the whole file — the useful commands are
`grep -E '^(ok|FAIL)\s+github'` for packages and `grep -E '^--- FAIL'` for tests.

Already visible in it, and worth its own task rather than a footnote: the `msc3902` package hangs
with its homeserver container `Up 14 minutes` and no further log output (the log went quiet at
11:44 local). A Complement test that never returns is a server bug of a particular kind — the test
is waiting for something the server will never send (a sync that never carries an event, a
missing-event fetch that is never answered, or a request handler that never replies). It is worse
than a failure, because a failure reports itself. `msc4242`, `msc4306` and `msc4429` report package
failures, which is expected for unimplemented MSCs and needs separating from hangs when the tally is
taken.
