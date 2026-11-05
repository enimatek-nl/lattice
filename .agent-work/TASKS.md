# Tasks

Status legend: `[ ]` pending · `[~]` in progress · `[x]` done (verification recorded in PROGRESS.md)

## M0 — Stage 0 (research/architecture)

- [x] T-0001 Read AGENTS.md + autonomous-development + backend skills; inventory installed skills → PROJECT-ASSESSMENT.md
- [x] T-0002 Pin Matrix spec version (v1.19, 2026-07-08) → SPECIFICATION-VERSION.md
- [x] T-0003 Clone & analyse Complement (layout, deployment contract, env vars, PKI, image rules)
- [x] T-0004 Read M1-relevant Complement tests as behavioural evidence (register/login/logout/versions/unknown endpoints/capabilities)
- [x] T-0005 Survey reference implementations (Synapse, Dendrite, gomatrixserverlib, conduwuit) → REFERENCE-ANALYSIS.md
- [x] T-0006 Architecture incl. event DAG, state sets, event auth, room versions, federation, storage → ARCHITECTURE.md
- [x] T-0007 Requirements + compliance tracking + milestones + acceptance criteria

## M1 — Foundation + identity layer

- [x] T-1001 `go mod init`, dependency selection (gormlite/gorm/x-crypto), Makefile, .gitignore, README
- [x] T-1002 `internal/config`: YAML+env+flags, validation, defaults — tests: `tests/unit/config_test.go`
- [x] T-1003 `internal/logging`: slog JSON, level, secret redaction — tests: `tests/unit/logging_test.go`
- [x] T-1004 `internal/matrixerr`: codes + writer + mapping of internal errors — tests
- [x] T-1005 `internal/matrixjson`: canonical JSON (spec vectors), number rules — tests
- [x] T-1006 `internal/matrixjson`: sign/verify JSON (spec 7-step, multi-sig) — tests
- [x] T-1007 `internal/matrixid`: server name, user ID, room/alias/event IDs, base64, MXID split — tests
- [x] T-1008 `internal/httpserver/router.go`: method-prefixed mux + probe mux, 404/405 `M_UNRECOGNIZED` — tests
- [x] T-1009 `internal/httpserver`: middleware chain (recover/reqid/log/limit/timeout), TLS + plain listeners, graceful shutdown — tests
- [x] T-1010 `internal/persist`: DB open (PRAGMAs), migrations, models (users, devices, access tokens, keys, uia nonces), repositories — tests
- [x] T-1011 `internal/keys`: ed25519 generate/load/persist, key id, signed server key document — tests
- [x] T-1012 `internal/users`: registration policy, bcrypt hashing, localpart downcasing, validation — tests
- [x] T-1013 `internal/auth`: token mint/verify/revoke, UIA sessions/flows, HTTP middleware — tests
- [x] T-1014 `internal/app`: wiring root; `cmd/lattice`: flags, signal handling, `healthcheck` subcommand
- [x] T-1015 CS API: `GET /versions` (+unstable_features) — tests
- [x] T-1016 CS API: `POST/GET /register` (UIA, dummy flow), `M_INVALID_USERNAME`/`M_USER_IN_USE`, `device_id` echo, `inhibit_login` — tests
- [x] T-1017 Compat: `GET/POST /_synapse/admin/v1/register` shared secret (HMAC-SHA1 nonce) — tests
- [x] T-1018 CS API: `GET/POST /login`, `POST /logout`, `POST /logout/all`, `GET /devices`, `GET/PUT/DELETE /devices/{id}`, `GET /account/whoami` — tests
- [x] T-1019 CS API: `GET /capabilities` (auth required, `m.room_versions`, `m.change_password`) — tests
- [x] T-1020 Discovery: `/.well-known/matrix/client` (+ server when configured) — tests
- [x] T-1021 Federation: `GET /_matrix/federation/v1/version`; keys: `GET /_matrix/key/v2/server`, `POST /_matrix/key/v2/query` — tests
- [x] T-1022 Media stub `POST /_matrix/media/v3/upload` (501 `M_UNKNOWN`) so 405 semantics hold
- [x] T-1023 `tests/integration`: end-to-end register→whoami→login→devices→logout over real listeners (plain + TLS)
- [x] T-1024 `deploy/complement/Dockerfile` + entrypoint (PKI, SERVER_NAME, shared secret) + `scripts/complement.sh`
- [x] T-1025 Run Complement M1 subset against the image; record results in PROGRESS/COMPLIANCE
- [x] T-1026 `make lint` (golangci-lint if available) / `go vet` clean
- [x] T-1027 Update docs (`docs/*`), compliance matrix, FINAL-REPORT.md

## M1.5 — hardening found while closing M1

- [x] T-1101 Race between `Serve`'s listener handoff and `CloseListeners` (found by `-race` in the new PKI test) → mutex-guarded listener table; `make verify` now runs `-race`
- [x] T-1102 CA-signed federation certificates (`tlsgen.EnsureSignedByCA`, `LATTICE_TLS_CA_*`) instead of shell/openssl in the entrypoint
- [x] T-1103 `SERVER_NAME` alias, `registration_mode` startup field, forced-shutdown path (`Shutdown` timeout → `Close` + warning)
- [x] T-1104 Makefile (verify/build/test/test-race/cover/lint/docker/complement/generate-config) — `make verify` is the milestone gate
- [x] T-1105 `golangci-lint run ./...` clean

## M2 — rooms and events (next milestone)

- [x] T-2000 Read Room Versions + event authorization + event graph sections of v1.19 for
      v9/v10/v11; requirement rows recorded in `docs/requirements.md` §6 (22 rows) and the
      interpretation decisions documented there and in `BLOCKERS.md`
- [x] T-2001 `internal/roomversion`: v1-v12 registry (state resolution v1/v2/v2.1, event ID
      format, authorisation rule set id, redaction profile, join-rule support, integer power
      levels, key validity, canonical JSON strictness, `m.room.aliases` special casing,
      `content.creator` vs sender, `content.redacts` vs top level, domain-free room IDs,
      privileged creators) + grammar validation + `Supported`/`Available` —
      `tests/unit/roomversion_test.go`
- [x] T-2011 `internal/redaction`: version-specific strip lists and per-type content allow
      lists — `tests/unit/redaction_test.go`
- [x] T-2012 `internal/events`: content hash, reference hash, event ID derivation per room
      version, `Seal` (hash then sign the redacted form), transport-key stripping; goldens
      compared with gomatrixserverlib for v3/v10/v11 — `tests/unit/eventseal_test.go`
- [x] T-2002 Event storage: `rooms`, `events` (canonical JSON verbatim + indexed
      columns, NULL vs empty `state_key`, redacted/outlier/soft_failed, global
      `stream_ordering`), `event_edges` (prev/auth), `room_state`,
      `event_auth_context`, `event_txn`; forward-extremities query; dedup via unique
      event id; per-device transaction ids — `tests/unit/eventstore_test.go`
- [x] T-2003 `POST /createRoom` (presets, `room_version` negotiation, `creation_content`
      limits, initial state, name/topic/avatar shorthand, alias request refused) plus
      `/capabilities` now sourced from `roomversion.Default()`/`Available()` —
      `internal/rooms`, `tests/integration/rooms_test.go`
- [x] T-2004 `internal/eventauth`: authorisation rules 1-10 for room versions 3-11 (v1/v2
      refused), auth-events selection and its validation, version-gated knock/restricted/
      knock_restricted, v10+ integer power-level validation, the historic `m.room.aliases`
      exemption, creator power bootstrap, and `MayJoinRestricted` for the restricted-room
      allow list — `tests/unit/eventauth_test.go`
- [x] T-2005 `internal/state`: State Resolution 2 for room versions 2-11 — unconflicted/
      conflicted split, auth difference, power events widened by their auth chains, reverse
      topological power ordering, mainline ordering, iterative auth checks that refuse an
      event whose auth event was rejected; v1 and v2.1 refused rather than approximated —
      `tests/unit/stateresolve_test.go`
- [ ] T-2005a Cross-check state resolution against gomatrixserverlib's resolver with shared
      fixtures (same fork, same expected winner) before any federation work relies on it
- [x] T-2006 `PUT /rooms/{roomId}/send/{eventType}/{txn}` with per-device transaction
      deduplication, server-computed depth/prev_events/auth_events, authorisation before
      signing, sealing and persistence; plus `PUT /rooms/{roomId}/state/{type}[/{key}]`
      (empty state key supported, deeper paths refused) —
      `tests/integration/rooms_test.go` incl. `TestCreatedEventIsVerifiable`
- [ ] T-2006a Persist the outcome of the on-receipt checks so `Snapshot.Rejected` is real;
      until then an event that failed on receipt could be used as an auth event
- [ ] T-2007 membership events: join/leave/invite/ban/kick (local), member state reads,
      `knock` gated on room version >= 7
- [ ] T-2008 `GET /rooms/{roomId}/state` and `/{type}/{state_key}`
- [ ] T-2009 `m.room.redaction` application: power level or same server name rule, redacted
      form stored, hashes/signatures stay valid
- [ ] T-2010 room upgrade path (`m.room.create.predecessor`, `POST /rooms/{roomId}/upgrade`)

- [ ] T-3001 `GET /sync` (timeline, state, invite rooms, device list changes), `/messages` pagination, `since`/`next_batch` tokens, filter storage
- [ ] T-3002 Unblocks Complement `TestLogout` behavioural subtests and `TestDeviceManagement/DELETE…` (they assert through `/sync`)
