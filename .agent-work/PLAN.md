# Implementation Plan

Milestones are ordered by dependency. Each has an independently verifiable acceptance gate.

**Current status (2026-11-05): M0 and M1 are complete and verified** — see `ACCEPTANCE-CRITERIA.md`
(M1 verdict) and `PROGRESS.md`. The next milestone is **M2 — rooms and events** (`TASKS.md` T-2000…).
M3+ are the staged roadmap from AGENTS.md §13 refined against the Complement inventory.

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

Not met, and named rather than glossed: two homeservers agreeing on one room's *state* after
both changed it. `rooms.persistEvent` sets a state key to the last state event persisted for
it — the last-write-wins this project must not substitute — while `internal/state.Resolve`
implements State Resolution 2, is unit-tested, and is never called. The resolver needs the
state each forward extremity was carrying, so T-2017 follows T-3004 (per-event state
snapshots). Until T-2017 lands this milestone stays open even though events move.

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
