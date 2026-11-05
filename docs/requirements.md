# Requirements

Derived from Matrix v1.19 (see `docs/specification-version.md`). Status vocabulary:

- **implemented** — code exists and is covered by the project's own tests
- **partial** — some normative behaviour is missing; the gap is named
- **not implemented** — deliberately deferred, listed so nothing is silently omitted
- **n/a** — not applicable to this server

Test paths are relative to the repository root.

## 1. Appendices: protocol primitives

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-JSON-001 | Appendices §Canonical JSON | Encode JSON as UTF-8, sorted by code point, no insignificant whitespace, objects not arrays at top level of signed payloads | MUST | implemented | `tests/unit/canonicaljson_test.go` |
| REQ-JSON-002 | Appendices §Canonical JSON numbers | Integers only, within −(2^53−1)…2^53−1, no fractional part | MUST | implemented | `tests/unit/canonicaljson_test.go` |
| REQ-JSON-003 | Appendices §Base64 | Unpadded base64 in output; accept padded input | MUST | implemented | `tests/unit/signjson_test.go` |
| REQ-SIGN-001 | Appendices §Signing JSON | Sign canonical form of the object minus `signatures` and `unsigned`; ed25519 only | MUST | implemented | `tests/unit/signjson_test.go` |
| REQ-SIGN-002 | Appendices §Verifying JSON | Follow the seven verification steps; an origin counts as verified if any of its signatures validates | MUST | implemented | `tests/unit/signjson_test.go` |
| REQ-ID-001 | Appendices §Identifier grammar | Server names (DNS or literal IP with optional port), user IDs, room IDs, aliases, event IDs, device IDs; localpart grammar; byte limits | MUST | implemented | `tests/unit/matrixid_test.go` |
| REQ-ID-002 | CS §Application Service / registration | User IDs are lowercase; server names are case-sensitive | MUST | implemented | `tests/unit/matrixid_test.go`, `tests/integration/auth_test.go` |

## 2. HTTP and error semantics

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-HTTP-001 | CS §Standard error codes | Errors are JSON objects with `errcode` and `error` | MUST | implemented | `tests/unit/httpsemantics_test.go`, `tests/integration/http_test.go` |
| REQ-HTTP-002 | Complement `TestUnknownEndpoints` | Unknown path under a Matrix prefix → 404 `M_UNRECOGNIZED` | MUST | implemented | as above |
| REQ-HTTP-003 | Complement `TestUnknownEndpoints` | Known path, wrong method → 405 `M_UNRECOGNIZED` with `Allow` | MUST | implemented | as above; method knowledge is mirrored across the two listeners so a federation path probed on the client port still answers 405 |
| REQ-HTTP-004 | CS §Versioning | Method-prefixed routing on `http.NewServeMux` | MUST | implemented | `internal/httpserver/router.go` |
| REQ-HTTP-005 | AGENTS §9 | Bound request bodies, request timeouts, panic recovery, security headers | MUST | implemented | `tests/integration/http_test.go` |
| REQ-HTTP-006 | — | Access tokens, passwords and key material never appear in logs | MUST NOT | implemented | `tests/unit/logging_test.go`, `tests/integration/lifecycle_test.go` |

## 3. Authentication and accounts

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-AUTH-001 | CS §Server-side discovery / UIA | `/register` performs User-Interactive Auth with `flows`, `params`, `session`, `completed` | MUST | implemented | `tests/integration/auth_test.go` |
| REQ-AUTH-002 | CS §Registration | `m.login.dummy` only completes a session this server issued | MUST | implemented | `tests/integration/auth_test.go`; Complement `Registration_without_a_session_fails` passes |
| REQ-AUTH-003 | CS §Registration | Invalid localpart → 400 `M_INVALID_USERNAME`; taken ID → 400 `M_USER_IN_USE`; both reported before the challenge | MUST | implemented | Complement `TestRegistration` |
| REQ-AUTH-004 | CS §Registration | Usernames are downcased; `q3._=-/` allowed; spaces and `,:@[]{}\|"'` and non-ASCII refused | MUST | implemented | Complement `TestRegistration` |
| REQ-AUTH-005 | CS §Login | `m.login.password` with `identifier` of type `m.id.user`; unknown user and wrong password both → 403 `M_FORBIDDEN` | MUST | implemented | Complement `TestLogin` |
| REQ-AUTH-006 | CS §Login | Login by full MXID, bare localpart, and in uppercase | MUST | implemented | Complement `Login with uppercase username…` |
| REQ-AUTH-007 | CS §Login | `device_id` in the request is honoured; `initial_device_display_name` stored | MUST | implemented | Complement `TestLogin`, `TestDeviceManagement` |
| REQ-AUTH-008 | CS §Logout | `POST /logout` invalidates only the calling device; `POST /logout/all` invalidates every session | MUST | implemented | `tests/integration/auth_test.go`; Complement `TestLogout` partially blocked on `/sync` (see compliance matrix) |
| REQ-AUTH-009 | CS §Error codes | Missing token → 401 `M_MISSING_TOKEN`; unknown/revoked token → 401 `M_UNKNOWN_TOKEN` | MUST | implemented | Complement `TestLogout` token subtests |
| REQ-AUTH-010 | CS §Devices | `GET /devices`, `GET/PUT/DELETE /devices/{id}`; unknown device → 404; delete requires UIA by the owning account | MUST | implemented | Complement `TestDeviceManagement`; UIA subtests blocked on `/sync` |
| REQ-AUTH-011 | CS §Account data / whoami | `GET /account/whoami` returns the token's user | MUST | implemented | Complement `Login with uppercase…` |
| REQ-AUTH-012 | Security | Passwords stored with bcrypt only, never plaintext; cost configurable and validated | MUST NOT | implemented | `tests/unit/storage_test.go` |
| REQ-AUTH-013 | Security | Access tokens stored as SHA-256 digests, revocable per device and per account | MUST | implemented | `tests/unit/storage_test.go` |
| REQ-AUTH-014 | CS §Registration (shared secret) | Synapse-compatible `/_synapse/admin/v1/register` with HMAC-SHA1 nonce, single-use nonce, constant-time MAC compare | MAY | implemented | Complement `TestRegistration` shared-secret subtests, `TestCanRegisterAdmin` |
| REQ-AUTH-015 | CS §Registration availability | `GET /register/available` → `{"available": true}` / 400 `M_USER_IN_USE` / 400 `M_INVALID_USERNAME` | MAY | implemented | Complement `TestRegistration` |
| REQ-AUTH-016 | CS §Deactivate account | `POST /account/deactivate` | MUST | not implemented | M2+; middleware already reports `M_USER_DEACTIVATED` for deactivated rows |
| REQ-AUTH-017 | CS §Change password | `POST /account/password` | MUST | not implemented | `capabilities` honestly reports `m.change_password.enabled = false` |
| REQ-AUTH-018 | CS §Refresh tokens | `refresh_token` request | MAY | not implemented | Refused with 400 `M_INVALID_PARAM` rather than ignored |
| REQ-AUTH-019 | CS §Login token / SSO | `m.login.token`, `m.login.application_service` | MUST (per flow) | not implemented | only the password flow is offered |

## 4. Discovery and versioning

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-DISC-001 | CS §Versioning | `GET /_matrix/client/versions` lists global version strings, optional object `unstable_features` | MUST | implemented | Complement `TestVersionStructure` |
| REQ-DISC-002 | CS §Client-Specification | `GET /capabilities` requires a token and exposes `m.room_versions`, `m.change_password` | MUST | implemented | Complement `TestServerCapabilities` |
| REQ-DISC-003 | CS `/.well-known/matrix/client` | Publish the client discovery document when configured | SHOULD | implemented | `tests/integration/http_test.go` |
| REQ-DISC-004 | SS `/.well-known/matrix/server` | Publish the server discovery document when configured | SHOULD | implemented | as above |
| REQ-DISC-005 | CS §Application Service | `GET /versions` for application services | n/a | n/a | no application service support yet |

## 5. Server keys and federation surface present today

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-KEY-001 | SS §Server Signing Keys | Generate and persist an ed25519 signing key; key id `ed25519:<identifier>` | MUST | implemented | `tests/unit/storage_test.go` |
| REQ-KEY-002 | SS §Server Signing Keys | `GET /_matrix/key/v2/server` returns a self-signed key document with `valid_until_ts` | MUST | implemented | `tests/integration/http_test.go` verifies the signature independently |
| REQ-KEY-003 | SS §Server Signing Keys | `POST /_matrix/key/v2/query` answers for this server; honours `minimum_valid_until_ts` | MUST | partial | answered for self only; remote key notarisation/caching is M4 |
| REQ-KEY-004 | SS §Request authentication | `X-Matrix` signing of outbound/inbound federation requests | MUST | not implemented | M4 |
| REQ-KEY-005 | SS §Transport | Federation listener serves HTTPS with TLS ≥ 1.2; CA-signed certificate supported | MUST | implemented | `tests/integration/lifecycle_test.go`, `tests/unit/tlsgen_test.go` |
| REQ-FED-001 | SS §PDUs/EDUs, joins, backfill, `/send/:txn` | Full federation exchange | MUST | not implemented | M4; tracked in `.agent-work/PLAN.md` |

## 6. Rooms, events and state (M2 in progress)

Protocol primitives first: none of this is reachable from an endpoint yet, but these are
the parts that must be byte-exact before any room is created.

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-VER-001 | Room Versions §grammar | Room version strings are opaque, ≤ 32 characters, `[a-z0-9.-]` | MUST | implemented | `tests/unit/roomversion_test.go` |
| REQ-VER-002 | Room Versions §feature matrix | Per-version behaviour is selected from a registry: state resolution, event ID format, authorisation rule set, redaction profile, join-rule support, integer power levels, `content.creator`, `content.redacts`, domain-free room IDs | MUST | implemented (v1–v12 registered; v10 and v11 offered for new rooms) | `tests/unit/roomversion_test.go` |
| REQ-VER-003 | CS §Capabilities | Only versions this server can create rooms in are advertised in `m.room_versions.available` | MUST | implemented in the registry; the handler still reports an aspirational default — see `docs/compliance-matrix.md` | as above |
| REQ-EVT-001 | SS §Signing Events (content hash) | Content hash = SHA-256 over canonical JSON with `hashes`, `signatures`, `unsigned` removed; stored unpadded base64 under `hashes.sha256` | MUST | implemented | `tests/unit/eventseal_test.go` |
| REQ-EVT-002 | SS §Signing Events (reference hash) | Reference hash = the event put through the redaction algorithm, then `signatures`/`unsigned` removed, then canonical JSON, then SHA-256 | MUST | implemented | `tests/unit/redaction_test.go` (goldens taken from gomatrixserverlib) |
| REQ-EVT-003 | Room Versions §Event IDs | Event ID = `$` + reference hash, standard unpadded base64 in v3 and URL-safe unpadded base64 in v4+; assigned by the origin server in v1–v2 | MUST | implemented | `tests/unit/redaction_test.go` |
| REQ-EVT-004 | SS §Signing Events (signing) | Sign the **redacted** event so signatures survive redaction; record under `signatures.<origin>.<key id>` | MUST | implemented | `tests/unit/eventseal_test.go` verifies over the redacted form and checks event-ID stability |
| REQ-EVT-005 | SS §Signing Events (receipt) | `event_id`, `outlier`, `destinations`, `age_ts`, `unsigned` are not part of what is hashed; recomputing an identifier ignores the supplied `event_id` | MUST | implemented | `tests/unit/eventseal_test.go` |
| REQ-RED-001 | Room Versions §Redactions (v1–v10) | Strip every top-level key except `event_id`, `type`, `room_id`, `sender`, `state_key`, `content`, `hashes`, `signatures`, `depth`, `prev_events`, `prev_state`, `auth_events`, `origin`, `origin_server_ts`, `membership` | MUST | implemented | `tests/unit/redaction_test.go` |
| REQ-RED-002 | Room Versions §Redactions (v11+) | `origin`, `membership` and `prev_state` are no longer protected; `m.room.create` keeps its whole content; `m.room.redaction` keeps `content.redacts`; `m.room.power_levels` also keeps `invite`; a redacted `m.room.member` keeps `third_party_invite.signed` | MUST | implemented | `tests/unit/redaction_test.go` |
| REQ-RED-003 | Room Versions §Redactions (v9) | `content.join_authorised_via_users_server` is protected in v9+ but not in v8 | MUST | implemented | `tests/unit/redaction_test.go` |
| REQ-RED-004 | Room Versions §Redactions (v6) | `m.room.aliases` loses its special redaction/auth handling in v6 | MUST | implemented | `tests/unit/redaction_test.go` |
| REQ-RED-005 | Room Versions §Handling redactions | Apply a redaction only if the sender's power level is at least the redact level **or** the sender's domain matches the redacted event's sender domain | MUST | not implemented | M2 (needs power levels and event lookup) |
| REQ-RED-006 | Room version 12 §Redactions | Additional protection of relation information | MUST | not implemented — v12 currently reuses the v11 profile and is not offered | recorded, M6 |
| REQ-EVT-006 | Room Versions §Event format | `auth_events` ≤ 10, `prev_events` ≤ 20, `depth` < 2^53−1 and equal to max(prev depths)+1 | MUST | not implemented | M2 event validation |
| REQ-EVT-007 | Appendices §Canonical JSON | In v6+ non-integer numbers and out-of-range integers make an event invalid (`M_BAD_JSON` on CS endpoints, discard over federation) | MUST | partial — canonical encoding is enforced, the per-version rejection rule is not wired up | M2 |
| REQ-STATE-001 | Room Versions §State resolution | State Resolution 1 for v1, State Resolution 2 for v2–v11, the revised algorithm for v12 | MUST | not implemented; the registry selects the algorithm | M2 |
| REQ-AUTH-020 | Room Versions §Authorisation rules | Room-version-specific event authorisation as a subsystem separate from handlers | MUST | not implemented | M2 (`internal/eventauth`) |
| REQ-AUTH-021 | SS §Auth events selection | `auth_events` must match the selection algorithm; duplicates, foreign-room events and rejected auth events invalidate the event | MUST | not implemented | M2 |
| REQ-AUTH-022 | Room Versions §Authorisation rule 9 (v10+) | Integer-only power-level content validation | MUST | not implemented | M2 |
| REQ-TXN-001 | CS §Idempotency | Transaction IDs on `PUT /send/{type}/{txn}` are per-device and replay yields the same event | MUST | partial — the storage layer enforces per-device uniqueness and returns the prior event; no endpoint yet | `tests/unit/eventstore_test.go` (endpoint coverage pending, M2) |
| REQ-ROOM-001 | CS §Rooms | `POST /createRoom` with version negotiation, initial state, presets | MUST | not implemented | M2 |
| REQ-ROOM-002 | Room Versions §room IDs | v12's domain-free room IDs and privileged creators | MUST | represented in the registry, not implemented | M6 |

### Interpretation decisions where the specification and implementations need reconciling

1. **`redacts` in room versions 3–10.** The per-version PDU tables list a top-level
   `redacts` property for redaction events, but the same pages' redaction strip list does
   not protect it, so the redacted form — hence the reference hash and event ID — drops
   it. gomatrixserverlib drops it; Synapse's Rust redaction implementation could not be
   read at the time of writing. This project follows the normative strip list, which also
   matches gomatrixserverlib. If a peer preserves the key instead, our event ID for a
   v3–v10 redaction event would differ from theirs; that is a falsifiable choice, checked
   as soon as redaction runs against Complement. Tracked in `.agent-work/BLOCKERS.md`.
2. **`format_version` in room versions 1–2.** Historic events carry it; the strip list
   does not protect it, so neither do we. v1/v2 rooms are not offered for creation.
3. **Transport keys on receipt.** `event_id`, `outlier`, `destinations` and `age_ts` are
   removed before hashing and signing, as both gomatrixserverlib and Synapse do; the
   specification states this in the receiving behaviour rather than in the hash
   definition.

## 7. Operational requirements

| ID | Source | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-OPS-001 | AGENTS §13 Stage 1 | Configuration from file + environment, validated at startup, no secrets required to boot locally | MUST | implemented | `tests/unit/config_test.go` |
| REQ-OPS-002 | AGENTS §13 Stage 1 | Graceful shutdown on SIGINT/SIGTERM, bounded by `shutdown_timeout` | MUST | implemented | `tests/integration/lifecycle_test.go` |
| REQ-OPS-003 | Complement image contract | `SERVER_NAME` honoured; ports 8008/8448; healthcheck; entrypoint re-runnable | MUST | implemented | `deploy/complement/*`, Complement deployment succeeds in every run above |
| REQ-OPS-004 | AGENTS §10 | Storage migrations run at startup; SQLite via gormlite; transactions for multi-row invariants | MUST | implemented | `tests/unit/storage_test.go` |
| REQ-OPS-005 | AGENTS §10 | Single-process storage: SQLite is documented as the M1 storage, not the final answer | MUST NOT (assume) | partial | one writer connection; scalability limits recorded in `ARCHITECTURE.md` |
