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
