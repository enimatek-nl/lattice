# Architecture

Modular monolith (AGENTS.md §5). One binary, one process, explicit internal boundaries. It is not
distributed on purpose: no microservices until a measured requirement appears.

```
                      ┌───────────────────────────────────────────────────────┐
  client (HTTP/8008)  │  listener: client API      listener: federation/8448  │  remote HS (HTTPS)
        │             │        │                          │                   │
        ▼             │  middleware chain           middleware chain          │
   ┌─────────┐        │  (recover, reqid, log,      (recover, reqid, log,     │
   │ client  │        │   limit, timeout, tls)      x-matrix auth [M4])       │
   │  API    │        │        │                          │                   │
   └────┬────┘        ▼        ▼                          ▼                   │
        │        ┌────────────────────────────────────────────────┐           │
        └───────►│            domain services                     │◄──────────┘
                 │ users · auth(uia,tokens) · rooms · events ·    │
                 │ eventauth · state · roomversion · federation · │
                 │ keys · sync · media · workers                  │
                 └───────────────────────┬────────────────────────┘
                                         ▼
                          repository interfaces (internal/persist)
                                         ▼
                          GORM + gormlite (SQLite, pure Go)  →  Postgres later
```

## 1. Package map

Implemented now (M0/M1) and marked; later packages are listed with the milestone that creates them
so the boundaries exist in the design before the code does.

| Package | Responsibility | Milestone |
| --- | --- | --- |
| `cmd/lattice` | flags (`-config`, `-port`…), config load, wiring, two listeners, signal handling, `healthcheck` subcommand | M1 |
| `internal/app` | application root: builds config, db, services, routers; `Run(ctx)`; used by `main` **and** integration tests | M1 |
| `internal/config` | YAML/env/flag config, validation, defaults | M1 |
| `internal/logging` | `log/slog` JSON logging, level, redaction of secrets | M1 |
| `internal/httpserver` | server lifecycle (client HTTP + federation HTTPS), graceful shutdown, middleware, Matrix routing semantics (404/405 `M_UNRECOGNIZED`) | M1 |
| `internal/matrixerr` | spec error codes, `MatrixError`, response writer | M1 |
| `internal/matrixjson` | canonical JSON, signing JSON, verify | M1 |
| `internal/matrixid` | identifier grammar: server name, user/room/alias/event IDs, MXID split, unpadded base64 | M1 |
| `internal/keys` | ed25519 signing key lifecycle (`ed25519:v1`), persistence, signed `/_matrix/key/v2/server` document, remote-key cache [M4] | M1 |
| `internal/persist` | `Database` (GORM+gormlite), migrations, PRAGMAs, repositories | M1 (+M2/M4 tables) |
| `internal/users` | registration policy, password hashing, profile | M1 |
| `internal/auth` | access tokens, UIA sessions/flows, HTTP auth middleware | M1 |
| `internal/api/client` | CS API handlers, one file per endpoint group | M1 (+M2/M3) |
| `internal/api/federation` | S-S API handlers | M1 subset (+M4) |
| `internal/api/media` | media repository endpoints | M1 stub (+M5) |
| `internal/roomversion` | room version registry: ID/room-ID rules, auth ruleset, state-res algorithm id, default | M2 |
| `internal/events` | PDU/event representation, parse, serialise, hash, sign, redaction, event ID generation, DAG helpers | M2 |
| `internal/eventauth` | room-version-aware event authorization rules | M2 |
| `internal/state` | state sets, shortnames, state resolution v1/v2/v3, snapshots | M2/M3 |
| `internal/rooms` | room lifecycle, membership, aliases | M2 |
| `internal/sync` | sync positions (`stream_id`), timelines, filters | M3 |
| `internal/federation` | server discovery (`/.well-known`, SRV), outbound client, X-Matrix signing, transactions/EDUs, join protocol, backfill, federation sender worker | M4 |
| `internal/devices` (folded into `users`/`auth` until it needs more) | device registry | M1 |
| `internal/workers` | background worker lifecycle (federation send, key notary, cleanup) with `errgroup` + bounded retries | M4 |

Tests live only under `tests/` (project override of the backend skill layout):

```
tests/unit/         # package-level behaviour, no network: JSON, signing, ids, config, errors,
                    # routing semantics, repos (temp sqlite), keys, auth, users
tests/integration/  # package `integration`: full app wiring on httptest/TLS listeners
```

## 2. HTTP layer and Matrix routing semantics

- `http.NewServeMux` with method-prefixed patterns (backend skill), one mux per API surface.
- **Matrix semantics needed**: unknown path → `404 {"errcode":"M_UNRECOGNIZED"}`; wrong method on a
  *known* path → `405 M_UNRECOGNIZED`. Go's mux answers 405 with an empty body, and its
  `NotFoundHandler` cannot tell "unknown path" from "known path, wrong method".
  Solution (`internal/httpserver/router.go`): every route is registered twice — method-prefixed in
  the serving mux, and method-agnostic in a *probe* mux that records which methods the path accepts.
  `NotFoundHandler` runs the probe: matched ⇒ 405 `M_UNRECOGNIZED` (+`Allow`), else 404
  `M_UNRECOGNIZED`. Keeps stdlib routing and satisfies the protocol.
- Middleware order (outer→inner): `recover` → `requestID` → `accessLog` → `maxBody` →
  `timeout` → `auth` (per sub-mux) → handler.
- Two listeners: client API on `:8008` (plain HTTP behind TLS-terminating proxy in real
  deployments; plain in Complement), federation + keys API on `:8448` (HTTPS from configured
  cert). Started together under one `errgroup`, shut down together on context cancellation with a
  `ShutdownTimeout`.

## 3. Identity and authentication

- `users`: localpart unique per server; IDs allocated as `@<downcased localpart>:<server_name>`;
  policy: charset per grammar, no leading `_` (Synapse-compat), password ≥ 6 chars
  (`M_WEAK_PASSWORD`), bcrypt cost 10 (configurable) — passwords never plaintext, never logged.
- `devices`: one row per session; `device_id` either client-supplied (validated: grammar
  `[A-Za-z0-9._-]{1,24}`-ish, must be unique per user) or generated (10 chars unpadded base64).
- `access tokens`: 32 random bytes → unpadded base64url (43 chars) presented to clients, stored as
  SHA-256(token) with `user_id`, `device_id`, `created_at`. Lookup by hash index. Revocation =
  delete rows (per device or per user).
- UIA (`/register`, later `/account/password`, `/admin/*`): sessions held in a TTL-bounded,
  size-bounded in-memory store (documented as single-process state; DB-backed variant when workers
  arrive). Flows offered: `m.login.dummy` (open registration), `m.login.password` (for
  authenticated flows). `401` responses include `session`, `flows`, `params`, `completed: []`.
- Shared-secret admin registration (`/_synapse/admin/v1/register`): Synapse-compatible HMAC-SHA1
  nonce exchange, nonces in the same TTL store, constant-time MAC compare, `admin` flag honoured.
  Enabled only when a shared secret is configured (DEC-006).

## 4. Cryptography

- `matrixjson.CanonicalJSON`: `map[string]any`/ordered-value encoder producing shortest UTF-8 JSON,
  keys sorted by Unicode codepoint, integers only within `±(2^53-1)`, no `+0/-0`, no exponents,
  escaping exactly per the grammar (`\b \t \n \f \r \" \\ \u00XX` for other control chars).
  Decoding uses `json.Decoder` with `UseNumber()` so large/float numbers are detectable and
  rejected with `M_BAD_JSON` (feeds Complement `TestJson` in M2).
- `matrixjson.SignJSON/VerifyJSON`: drop `signatures` + `unsigned`, canonicalise, ed25519 sign,
  unpadded base64 into `signatures[<name>]["ed25519:<keyid>"]`, put `unsigned` back. Verification
  implements the spec's 7 steps, tolerant of multiple signatures per entity.
- `keys`: ed25519 keypair generated at first boot, persisted (PEM in data dir / DB), stable key id
  `v1`, `verify_keys`, `signatures`, `valid_until_ts` (+7d), min `valid_until_ts` refresh. Server
  key document signed per Appendices. Private keys never serialised to logs.

## 5. Event graph (design; implemented M2)

Room history is a DAG. Planned schema (`internal/persist`, M2):

| Table | Purpose | Key columns |
| --- | --- | --- |
| `rooms` | room registry | `room_id` (unique), `room_version`, `resolved_state_snapshot_id`, `latest_stream_id`, `has_auth_chain_index` |
| `events` | every persisted event, PDU form as canonical JSON blob | `event_id` (unique), `room_id`, `type`, `state_key` (NULL if not state), `sender`, `origin_server_ts`, `depth`, `origin`, `json` (raw), `rejected_reason` (NULL if accepted), `stream_id` (monotonic per room), `added_at` |
| `event_refs` | DAG edges: `prev_event`/`auth_event`/`third_party` | `event_id`, `ref_event_id`, `ref_type` |
| `event_shortnames` | event_id ⇄ `bigint` shortname for set algebra | `room_id`, `shortname`, `event_id` |
| `event_state_snapshot` / `…_set` | immutable state sets (Dendrite-style dedup) | `snapshot_id`, `set_id`, `shortname` |
| `room_current_state` | current state per room | `room_id`, `shortname` |
| `room_forward_extremities` | DAG tips | `room_id`, `event_id` |
| `event_auth_chain` / `…_links` | auth chain closure for authorization + `/event_auth` | `event_id`, `auth_chain_id` |
| `event_relations`, `event_threads` | aggregation (M3) | |
| `redactions` | redaction bookkeeping | |
| `event_visibility` | history-visibility decisions | |

Invariants: monotonic `stream_id` for sync ordering (never confused with topological order);
forward extremities updated on accept; unaccepted/outlier events stored with `rejected_reason` and
never used as state; events immutable once persisted (redaction changes *presentation*, not bytes).

## 6. Room state and state resolution (M2)

- State sets computed as `bigint` shortname sets (fast, set-deduplicated) and materialised as
  immutable snapshots; per-room current state kept in `room_current_state`.
- `internal/roomversion` registry maps `room_version` → {event ID algorithm, room ID algorithm,
  auth ruleset, state-res algorithm ∈ {v1, v2, v3}}. `v1` = room versions 1–2,
  `v2` = 3–10, `v3` (Hydra) = 12, per spec. The resolver is implemented from the spec's
  "State resolution algorithm" (auth conflict map + power/resolutions/priorities); **no
  last-write-wins shortcut** (AGENTS.md §7).
- Every state calculation is exercised with cross-version fixtures, including known
  state-reset/regression cases from the spec text.

## 7. Event authorization (M2)

`eventauth.Rules` interface per room version: `Check` + `CheckAfterAuth` taking the event, its
auth events and the state needed (`m.room.create`, `m.room.member`, `m.room.power_levels`,
`m.room.join_rules`, `m.room.history_visibility`, plus version-specific: restricted join rules,
server ACLs). Rules tables derived directly from the spec's per-version auth rules; the differences
between v1…v12 are handled by small, explicit rule sets instead of conditionals scattered in
handlers. Authorization is never performed in HTTP handlers.

## 8. Federation (M4)

- `internal/federation`: server name resolution (well-known delegation + SRV + direct), pinned TLS
  config, X-Matrix `origin`/`key`/`mac` signing for outbound and verification for inbound
  (`/send/:txnId`, `/event/:id`, `/state/:roomId`, `/make_*`, `/send_*`, `/backfill`, `/query/*`,
  `/make_join`/`/send_join`, `/transport/keys/…`), EDUs (`m.presence`, `m.receipt`,
  `m.typing`, device list updates, to-device), transaction dedup (`txn_id` per destination +
  `edu_stream_id`), PDUs as outliers with later authorization (state-conflict-safe), and a
  **federation sender queue** in the DB with exponential backoff and per-destination isolation so
  one dead remote cannot block local progress (AGENTS.md §8).
- Outbound worker: `errgroup` + context, bounded retries, per-destination single-flight, and
  timeouts; the queue is durable so restarts resume.

## 9. Persistence decisions

- Engine now: GORM + `github.com/ncruces/go-sqlite3/gormlite` (pure Go, no CGO, static binary,
  works in containers) — verified in tests.
- PRAGMAs: `journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=5000`,
  `wal_autocheckpoint`, single `*sql.DB` with `SetMaxOpenConns(1)` for writes under SQLite (documented
  scalability limit, AGENTS.md §10 "never assume SQLite is sufficient").
- Every aggregate write uses `db.Transaction`; repositories are interfaces so a Postgres engine can
  be swapped without touching services.
- Migrations: explicit `AutoMigrate` of models + hand-written index/`CREATE UNIQUE` statements kept
  in one place with an ordered migration list (no implicit schema drift).

## 10. Security posture (per AGENTS.md §9)

bcrypt passwords; hashed tokens; ed25519 keys file mode 0600; constant-time comparisons for
tokens/MACs; body size caps (1 MiB CS API, 20 MiB federation, configurable); per-request timeouts;
strict JSON decoding (unknown numbers rejected); path handling only through mux patterns (no manual
file paths in M1); SSRF guard required before M4 outbound federation (private-IP policy for
well-known/SRV resolution); no secrets in logs (redaction in `logging`); admin endpoints off unless
configured; `X-Forwarded-For` trusted only when a proxy CIDR is configured.

## 11. Observability

slog JSON with `request_id`, `user_id` (never token), route pattern, status, duration. `/healthz`
(process up) + `lattice healthcheck` subcommand (container healthcheck without extra binaries).
Metrics deliberately deferred; hooks left at middleware level.

## 12. Deployment for Complement

`deploy/complement/Dockerfile`: multi-stage (`golang:1.26` build → small runtime with
`ca-certificates`+`openssl`), `EXPOSE 8008 8448`, `HEALTHCHECK CMD /app/lattice healthcheck`,
`ENTRYPOINT` script that (idempotently) signs a `$SERVER_NAME` cert with `/complement/ca/ca.key`,
installs the CA into the trust store, renders config from env (`SERVER_NAME`,
`LATTICE_REGISTRATION_SHARED_SECRET=complement`, open registration on) and execs the server.
`scripts/complement.sh` builds the image with podman (`--format docker`, needed for HEALTHCHECK)
and runs Complement with `DOCKER_HOST` + `COMPLEMENT_HOSTNAME_RUNNING_COMPLEMENT=host.containers.internal`.
