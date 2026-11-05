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

## 6. Rooms, events, state (deliberately not implemented yet)

| ID | Spec section | Requirement | Level | Status |
| --- | --- | --- | --- | --- |
| REQ-EVT-001 | Room Versions §Event authorization | Room-version-aware event authorization as a dedicated subsystem | MUST | not implemented (M2) |
| REQ-STATE-001 | Room Versions §State resolution | Resolve conflicted state with the algorithm for the room version; never last-write-wins | MUST | not implemented (M2) |
| REQ-ROOM-001 | CS §Rooms | `POST /createRoom`, membership, `PUT /send/{type}/{txn}` | MUST | not implemented (M2) |
| REQ-SYNC-001 | CS §Sync | `GET /sync`, `/messages`, filters | MUST | not implemented (M3) |
| REQ-MEDIA-001 | CS §Media repository | Upload/download/thumbnail | MUST | not implemented; `POST /_matrix/media/v3/upload` answers 501 with a Matrix error so the path still negotiates 405 correctly |
| REQ-TXN-001 | CS §Idempotency | Transaction ID semantics for `PUT /send` | MUST | not implemented (M2) |

## 7. Operational requirements

| ID | Source | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-OPS-001 | AGENTS §13 Stage 1 | Configuration from file + environment, validated at startup, no secrets required to boot locally | MUST | implemented | `tests/unit/config_test.go` |
| REQ-OPS-002 | AGENTS §13 Stage 1 | Graceful shutdown on SIGINT/SIGTERM, bounded by `shutdown_timeout` | MUST | implemented | `tests/integration/lifecycle_test.go` |
| REQ-OPS-003 | Complement image contract | `SERVER_NAME` honoured; ports 8008/8448; healthcheck; entrypoint re-runnable | MUST | implemented | `deploy/complement/*`, Complement deployment succeeds in every run above |
| REQ-OPS-004 | AGENTS §10 | Storage migrations run at startup; SQLite via gormlite; transactions for multi-row invariants | MUST | implemented | `tests/unit/storage_test.go` |
| REQ-OPS-005 | AGENTS §10 | Single-process storage: SQLite is documented as the M1 storage, not the final answer | MUST NOT (assume) | partial | one writer connection; scalability limits recorded in `ARCHITECTURE.md` |
