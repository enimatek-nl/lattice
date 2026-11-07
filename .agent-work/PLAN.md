# Implementation Plan

Milestones are ordered by dependency. Each has an independently verifiable acceptance gate.

**Current status (2026-11-07).** M0 and M1 are complete and verified — see `ACCEPTANCE-CRITERIA.md`
(M1 verdict). M2 (rooms and events), M3 (client synchronisation), M4 (federation foundation) and part
of M5 (content repository, device keys, push rules, presence for one user) are implemented and
exercised by this project's integration tests, with large parts of M4 and M5 verified against
Complement. The measured position, from a full run on 2026-11-07: **106 Complement top-level tests
pass, 113 fail, 1 skip** (csapi 69/107, server-server 35/92, MSC packages 2/22). The honest per-feature
picture is in `docs/compliance-matrix.md` and the running record is in `PROGRESS.md`; every tally there
now comes from a run whose log and pass/failure lists are stored beside it.

Room versions 1 through 11 are offered for new rooms; version 12 and the unstable versions are
recognised and refused (T-4019), which is also what keeps MSC4289, MSC4291, MSC4297, MSC4311 and
MSC3757 red. Of the M4 handshake family, invites (both directions), the join handshake, `/state_ids`,
`/event_auth`, `/event`, `/get_missing_events`, `X-Matrix`, the durable queue, server ACLs and
restricted joins are done, as is a kick, ban or unban of a user who lives on another homeserver
(T-4062). Missing: `/make_leave`, `/send_leave`, `/make_knock`, `/send_knock`, answering `/backfill`,
federation `/profile` and `/query/directory`, and EDUs that do anything (T-4009c, T-4009b2, T-4008,
T-4064). The largest M5/M6 open items are the MSCs above, sync filtering (T-4020), relations, threads
and search, receipts and read markers, typing, cross-signing and key backup (T-3008d/e), the read
marker that would make push counts mean anything (T-3005a), depth-based paging (T-3001d) and the
address-policy re-check at dial time (T-4011). M6/M7 remain as staged.

One decision the records now carry explicitly (`docs/compliance-matrix.md`, "A full run on the
abandoned `main` branch"): the earlier attempt in this repository's history has no commits in common
with this branch and measured 78/92 on the server-server package where this branch measures 35/92.
Porting that handshake family and the room-version-12 work — keeping this branch's image, runner,
evidence discipline and static build — is cheaper than re-deriving it, and the port must be reviewed
against the specification pages rather than accepted because it is green.

## M0 — Stage 0: research and architecture (this iteration, done)

Spec pinned (v1.19, 2026-07-08), Complement analysed and its image contract extracted,
reference implementations surveyed, requirements + compliance matrix created, architecture
designed (event DAG, state sets, event auth, room versions, federation, storage), test layout and
Complement runner designed.
**Gate:** documents exist and are internally consistent with the code that follows.

## M1 — Foundation + identity layer (this iteration)

1. Project skeleton: `go.mod`, Makefile, `.gitignore`, README, package tree.
2. Config (YAML + env + flags) and slog logging with secret redaction.
3. HTTP layer: client listener (HTTP), federation listener (HTTPS), middleware, graceful shutdown,
   Matrix 404/405 `M_UNRECOGNIZED` routing semantics, `/healthz`, `healthcheck` subcommand.
4. Protocol primitives: canonical JSON, signing JSON (+verify), identifier grammar, unpadded
   base64 — tested against the spec's example vectors.
5. Storage: GORM+gormlite, PRAGMAs, migrations, repositories for users/devices/tokens/UIA nonces;
   atomic registration.
6. Signing keys: generate/persist ed25519, `GET /_matrix/key/v2/server`, `POST /_matrix/key/v2/query`.
7. Identity: `/register` UIA + `m.login.dummy`, shared-secret admin registration, `/login`,
   `/logout`, `/logout/all`, `/devices*`, `/account/whoami`, `/versions`, `/capabilities`,
   `/.well-known/matrix/client`, federation `/version`, media upload stub.
8. Tests: `tests/unit` + `tests/integration`, race detector, `go vet`, coverage.
9. Complement image + PKI entrypoint + runner script; run the M1-relevant Complement tests.
**Gate:** Complement `TestVersionStructure`, `TestUnknownEndpoints`, `TestRegistration`,
`TestLogin`, `TestLogout`, `TestServerCapabilities` pass against the built image; `go test -race`
green; no Complement assertion weakened.

## M2 — Rooms and events (next)

Room version registry (v9/v10/v11), event representation + event ID/hash/signing per version,
event DAG storage + shortnames + snapshots, `eventauth` for v11 (+v10/v9 tables), room creation
(`POST /createRoom`), local send (`PUT /send/{type}/{txnId}`), `m.room.member` invite/join/leave,
`/state*`, `/members`, `/context`, `/event_auth`. Complement targets: `TestJson`,
`TestRoomCreation`-class tests, `tests/csapi/rooms_state_test.go`, `room_members_test.go`,
`membership_on_events_test.go`, `txnid_test.go`, `power_levels_test.go`, `redact_test.go`.

## M3 — Client synchronisation

`stream_id` sequencing, `/sync` (incremental, `since`, `timeout`, filters, `state_after` later),
`/messages`, `/initialsync` not needed, typing/read receipts later. Targets: `sync_test.go`,
`sync_filter_test.go`, `room_messages_test.go`, `account_data_test.go`.

## M4 — Federation foundation

Server discovery + SSRF guard, X-Matrix auth, key fetching/notary + cache, `/send/:txn` receiver +
durable outbound queue with backoff, PDU validation + outliers, `/make_join`/`/send_join`,
`/state_ids`, `/backfill`, `/event`, `/missing_events`. Targets: `federation_*_test.go` (join,
send, keys, get_missing_events, acl, redaction, sync, typing, presence, to_device).

Done here: keys and discovery, `X-Matrix` in and out, the `/send/{txn}` receiver and its
durable queue, PDU ingest checks in specification order, refusals recorded and cascading
(T-2006a, DEC-021), outbound fan-out, Complement's federation credential fix (DEC-020).

Corrected 2026-11-08: the blocker named here for a long time — that current state was
last-write-wins while `internal/state.Resolve` was never called — no longer holds.
`rooms.reconcileCurrentState` (`internal/rooms/resolve.go:183`–218) reads the room's forward
extremities, takes each one's recorded state snapshot, resolves them with the algorithm the room
version names, and makes the result the current state; `internal/rooms/stateat.go:91` resolves the
state an event at a fork was carrying. Unit coverage of the resolver itself predates this.

## M5 — Extended functionality

Media repository (upload/download/thumbnail/async), device keys (`/keys/upload|query|claim`,
`/keys/changes`), to-device, push rules/notifications, presence, room directory, aliases, spaces,
hierarchy, appservices, admin APIs.

## M6 — Advanced room semantics

Remaining room versions incl. v12, state resolution v2/v3 correctness suite, restricted rooms,
knocking, upgrades, thread aggregation, search.

## M7 — Compliance and hardening

Complement coverage sweep, Complement Crypto for E2EE, load/perf testing, Postgres engine option,
operational docs, security review.

## Execution rules

- One coherent change per task; tests written/updated with the code; `make test` before marking done.
- Any protocol bug fixed gets a regression test.
- Progress recorded in PROGRESS.md with the actual command output summary, never claims without runs.
