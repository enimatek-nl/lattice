# Compliance Matrix

Last updated: 2026-11-05.

This matrix distinguishes **implemented**, **tested by this project**, **verified against Complement**,
**partial**, **not implemented** and **not applicable**. An endpoint that merely answers is not
"compliant"; the columns exist so those states cannot be confused.

Specification: Matrix v1.19 (`docs/specification-version.md`).

## Client-Server API

| Area | Endpoint / behaviour | Status | Own tests | Complement |
| --- | --- | --- | --- | --- |
| Versioning | `GET /_matrix/client/versions` | implemented | yes | **verified** (`TestVersionStructure`) |
| Capabilities | `GET /v3/capabilities` (+`v1`) | implemented | yes | **verified** (`TestServerCapabilities`, incl. 401 when unauthenticated) |
| Registration | `POST /register` UIA, `m.login.dummy`, downcasing, invalid-character and duplicate handling, `device_id` echo, non-ASCII passwords | implemented | yes | **verified** (`TestRegistration`, all subtests) |
| Registration | `GET /register/available` | implemented | yes | **verified** (`TestRegistration`, 3 subtests) |
| Registration | UIA session is required for the credential-less `m.login.dummy` stage | implemented | yes | **verified** (`Registration_without_a_session_fails`) |
| Registration | Closed registration refused (`M_FORBIDDEN`) rather than silently allowed | implemented | yes | n/a (harness always deploys open registration) |
| Compat | `GET/POST /_synapse/admin/v1/register` shared secret (admin, downcasing, symbol rejection) | implemented | yes | **verified** (`TestRegistration`, `TestCanRegisterAdmin`) |
| Login | `GET /login` flows, `POST /login` password flow, localpart/uppercase MXID, `device_id`, wrong password and unknown user → 403 `M_FORBIDDEN` | implemented | yes | **verified** (`TestLogin`, all subtests) |
| Logout | `POST /logout`, `POST /logout/all`, `M_MISSING_TOKEN`, `M_UNKNOWN_TOKEN` | implemented | yes | **partially verified**: both token-error subtests pass; the two behavioural subtests assert through `GET /sync`, which does not exist yet (M3) |
| Devices | `GET /devices`, `GET/PUT /devices/{id}`, 404 for unknown device, `initial_device_display_name` | implemented | yes | **verified** (`TestDeviceManagement`, those subtests) |
| Devices | `DELETE /devices/{id}` with UIA (challenge, wrong password, owner check, token invalidation) | implemented | yes | **not verified by Complement** (its subtest reaches `/sync` first); verified by `tests/integration/auth_test.go` |
| Session | `GET /account/whoami` | implemented | yes | **verified** (inside `TestLogin`) |
| Errors | 404 `M_UNRECOGNIZED` for unknown paths, 405 `M_UNRECOGNIZED` + `Allow` for wrong methods, across client/federation/key/media prefixes | implemented | yes | **verified** (`TestUnknownEndpoints`, all subtests) |
| Discovery | `/.well-known/matrix/client`, `/.well-known/matrix/server` | implemented | yes | n/a |
| Media | `GET /v3/config` (upload size) | implemented | yes | **verified** (`TestMediaConfig`) |
| Media | upload / download / thumbnail / async | not implemented | stub answers 501 | `TestMediaConfig` passes; upload tests fail with 501 (recorded, not hidden) |
| Filters | `POST /filter` validation | not implemented | no | `TestFilter` "passes" only because it asserts 4xx and we answer 404 — **incidental, not compliance** |
| Rooms, events, state, members, redaction, txn ids | all | not implemented | — | fails with 404 `M_UNRECOGNIZED` (M2) |
| Sync / messages / filters | all | not implemented | — | fails with 404 (M3) |
| E2EE device keys, to-device, key backup | all | not implemented | — | fails with 404 (M5) |
| Account: deactivate, change password, 3PIDs | all | not implemented | — | fails with 404 |
| Presence, typing, receipts, read markers | all | not implemented | — | fails with 404 |
| Directory, aliases, spaces, search, user directory | all | not implemented | — | fails with 404 |
| Refresh tokens | `refresh_token` | not implemented | refusal tested | `TestTxnIdWithRefreshToken` fails on our 400 `M_INVALID_PARAM` — an explicit refusal, not a silent ignore |
| Push, notifications, application services, URL preview, SSO | all | not implemented | — | fails with 404 |

## Server-Server and key API

| Area | Status | Own tests | Complement |
| --- | --- | --- | --- |
| `GET /_matrix/federation/v1/version` | implemented | yes | **verified** (the 405 half of `TestUnknownEndpoints`) |
| `GET /_matrix/key/v2/server` (signed document, verified against its own key) | implemented | yes | not directly exercised |
| `POST /_matrix/key/v2/query` (answers for this server only) | partial — remote key notarisation and caching are M4 | yes | not directly exercised |
| TLS on the federation listener (TLS ≥ 1.2, CA-signed certificate support) | implemented | yes | every run uses it: containers start healthy and federation attempts reach the handshake |
| `X-Matrix` request authentication, PDUs, EDUs, `/send/:txn`, joins, `/state_ids`, backfill, `/event`, `/missing_events`, `/transaction` idempotency | not implemented | — | all `federation_*` tests fail (M4) |
| Server discovery (`.well-known` delegation, SRV records), key fetching | not implemented | — | M4 |

## Protocol primitives (appendices)

| Area | Status | Tests |
| --- | --- | --- |
| Canonical JSON (spec example vectors, integer range, escaping, UTF-8) | implemented | `tests/unit/canonicaljson_test.go` |
| Signing / verifying JSON (7-step procedure, multiple signatures, unpadded base64) | implemented | `tests/unit/signjson_test.go` |
| Identifier grammar (server names, user/room/alias/event/device IDs, downcasing) | implemented | `tests/unit/matrixid_test.go` |
| ed25519 signing key generation, persistence, key id format | implemented | `tests/unit/storage_test.go` |

### Protocol primitives (M2, in progress — not reachable from any endpoint yet)

| Capability | Status | Evidence |
| --- | --- | --- |
| Room version registry v1-v12 (state res, event ID format, auth rule set, redaction profile, join rules, integer power levels, creator placement, redacts placement, domain-free room IDs) | implemented | `tests/unit/roomversion_test.go` |
| Room version grammar validation | implemented | `tests/unit/roomversion_test.go` |
| Only supported versions offered/advertised (`roomversion.Supported`/`Available`) | implemented (registry); the `/capabilities` handler does not use it yet | see deviation 2 below |
| Redaction algorithm, per room version | implemented | `tests/unit/redaction_test.go` |
| Content hash, reference hash, event ID derivation | implemented | `tests/unit/redaction_test.go`, `tests/unit/eventseal_test.go` |
| Hashing + signing an outgoing event (signature over the redacted form) | implemented | `tests/unit/eventseal_test.go` |
| Cross-implementation agreement on redaction and event IDs | verified against gomatrixserverlib v0.0.0-20260716140101-4fe595dc7f58 by golden test | `TestRedactionAndEventIDsAgainstReferenceImplementation` |
| Event authorisation rules (rules 1-10) for room versions 3-11, with version-gated join rules and power-level validation | implemented | `tests/unit/eventauth_test.go` |
| Auth events selection and its validation (duplicates, foreign rooms, rejected auth events) | implemented | `tests/unit/eventauth_test.go` |
| Restricted-room allow list evaluation | partial: enforced for locally created joins only | `tests/unit/eventauth_test.go` |
| Event authorisation for room versions 1 and 2 | not implemented (refused with `ErrRulesetUnsupported`) | `tests/unit/eventauth_test.go` |
| State resolution (v1 / v2 / v2.1) | not implemented | M2 |
| Event graph storage (events, prev/auth edges, per-event auth context) and the forward-extremities query | implemented | `tests/unit/eventstore_test.go` |
| Transaction ID store for `/send/{type}/{txn}` idempotency (unique per device) | implemented (storage only; the endpoint does not exist yet) | `tests/unit/eventstore_test.go` |
| `depth` validation against `prev_events` | not implemented | M2 |
| Redaction application rules (power level or same domain) | not implemented; the redacted form has a column to live in | M2 |

## Complement run record

Full inventory, commands and the per-test list: `.agent-work/COMPLIANCE-MATRIX.md`.

| Field | Value |
| --- | --- |
| Complement checkout | `7b38a63c9db3337292bee5887fc8533be3a57b9f` (2026-11-03) |
| Image | `complement-lattice:latest`, built from `deploy/complement/Dockerfile` |
| Suites | `./tests` and `./tests/csapi` |
| Top-level results | 8 passed, 189 failed, 0 skipped |
| M1-target subset | `TestVersionStructure`, `TestUnknownEndpoints`, `TestRegistration`, `TestLogin`, `TestServerCapabilities`, `TestCanRegisterAdmin`, `TestMediaConfig` pass; `TestLogout` and `TestDeviceManagement` pass except the subtests that assert through `/sync` |
| Failure causes | 404 `M_UNRECOGNIZED` on unimplemented endpoints (309 responses), 501 on media upload (28), one 400 `M_INVALID_PARAM` refresh-token refusal |
| Server faults observed | 0 panics, 0 HTTP 500 responses |
| Complement assertions changed | none |

## Known inconsistencies recorded honestly

1. `GET /versions` advertises v1.1…v1.19 while most endpoints are missing. Clients are not misled
   about individual endpoints (they answer `M_UNRECOGNIZED`), but the range overstates coverage; a
   narrowing decision is tracked as an open item.
2. `capabilities.m.room_versions` advertises `11` as default and v1-v11 as available, while no room can be created yet. The registry now exposes the honest set (`Supported`/`Available` return v10 and v11 only); wiring `/capabilities` to it happens together with `createRoom`, because advertising versions without the endpoints that use them would be equally misleading.
3. `TestFilter` passing is incidental; the filter endpoint is not implemented.
4. `TestLogout`/`TestDeviceManagement` subtests fail only because the assertion vehicle (`/sync`)
   does not exist; the logout/device behaviour itself is covered by this project's integration tests.
