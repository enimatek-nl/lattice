# Acceptance Criteria

Each criterion is objective and reproducible. "Verified" in PROGRESS.md means a command was run and
its output inspected; nothing here is accepted on assertion.

## AC-1 Build and static checks

| ID | Criterion | Command |
| --- | --- | --- |
| AC-1.1 | Module builds, no CGO required | `CGO_ENABLED=0 go build ./...` |
| AC-1.2 | `go vet ./...` clean | `make vet` |
| AC-1.3 | gofmt/golangci-lint clean (lint skipped with a recorded note if the tool is unavailable) | `make fmt-check lint` |

## AC-2 Automated tests

| ID | Criterion | Command |
| --- | --- | --- |
| AC-2.1 | All unit + integration tests pass with the race detector | `make test` (`go test -race ./tests/...`) |
| AC-2.2 | Protocol primitives pass the canonical-JSON example vectors published in the spec | `tests/unit/canonicaljson_test.go` |
| AC-2.3 | Signature verification follows the spec 7 steps incl. failure cases (unknown algorithm, bad base64, tampered body) | `tests/unit/signjson_test.go` |
| AC-2.4 | Identifier grammar accepts/rejects spec-derived cases incl. 255-byte limit and downcasing | `tests/unit/matrixid_test.go` |
| AC-2.5 | Server starts on ephemeral ports, serves plain client API and TLS federation API, and shuts down gracefully on context cancel | `tests/integration/lifecycle_test.go` |
| AC-2.6 | Registration + device + token creation is atomic (failure mid-way leaves no partial state) | `tests/integration/registration_atomicity_test.go` |

## AC-3 Matrix protocol behaviour (self-tested)

| ID | Criterion |
| --- | --- |
| AC-3.1 | `GET /_matrix/client/versions` → 200, `versions: [...]` (spec-version strings), `unstable_features` object |
| AC-3.2 | Unknown path under `/_matrix/...` → 404 `M_UNRECOGNIZED`; wrong method on known path → 405 `M_UNRECOGNIZED` |
| AC-3.3 | `POST /register` without `auth` → 401 with `session` + `flows[].stages`; with `m.login.dummy` → 200 `{user_id, access_token, device_id}` |
| AC-3.4 | Username downcasing → `@user-upper:<server_name>`; invalid localpart → 400 `M_INVALID_USERNAME`; duplicate → 400 `M_USER_IN_USE` |
| AC-3.5 | Shared-secret admin registration yields `@adminuser:<server_name>`; bad MAC → 403; invalid localpart → 400 `M_INVALID_USERNAME` |
| AC-3.6 | `POST /login` with `m.login.password` (MXID or localpart) → token; wrong password → 401 `M_FORBIDDEN`; unknown user → 401/403 per spec, no user enumeration difference |
| AC-3.7 | `POST /logout` invalidates only that device (subsequent authenticated call → 401 `M_UNKNOWN_TOKEN`); `POST /logout/all` invalidates all; `GET /devices` counts match |
| AC-3.8 | `GET /capabilities` requires auth; contains `m.room_versions` and `m.change_password` |
| AC-3.9 | `GET /_matrix/key/v2/server` returns a self-signed key document that verifies with the published verify key |
| AC-3.10 | No access token, password or private key appears in logs (redaction test) |

## AC-4 Complement integration (external evidence)

| ID | Criterion |
| --- | --- |
| AC-4.1 | Image satisfies the Complement contract: `EXPOSE 8008 8448`, `SERVER_NAME` respected, self-signed cert from the mounted Complement CA, CA trusted, healthy before `COMPLEMENT_SPAWN_HS_TIMEOUT_SECS`, `CMD` re-runnable |
| AC-4.2 | `complement.Deploy(t, 1)` starts the image without harness errors |
| AC-4.3 | These Complement tests pass unmodified: `TestVersionStructure`, `TestUnknownEndpoints`, `TestRegistration`, `TestLogin`, `TestLogout`, `TestServerCapabilities` |
| AC-4.4 | Runner is reproducible: `scripts/complement.sh [-run <regex>]` from a clean shell using podman's Docker API |
| AC-4.5 | No Complement file is modified (checked by `git -C <complement> status --porcelain` empty) |

## AC-5 Process / documentation

| ID | Criterion |
| --- | --- |
| AC-5.1 | `docs/specification-version.md`, `docs/requirements.md`, `docs/compliance-matrix.md` exist and match the code |
| AC-5.2 | `.agent-work/` state files updated with actual verification results |
| AC-5.3 | No endpoint is claimed implemented that is not implemented; stubs documented |
| AC-5.4 | The word "compliant" is not used for the project as a whole without evidence (compliance matrix vocabulary only) |

## Out of scope for this milestone (explicitly not claimed)

Rooms, events, state resolution, sync, federation traffic, media, E2EE device keys, appservices,
presence, receipts, admin APIs. These remain `not implemented` in the compliance matrix.

## M1 verdict (2026-11-05) — met, with named gaps

| Criterion | Evidence | Verdict |
| --- | --- | --- |
| Runnable binary, config (file/env/flags), validation | `bin/lattice version`, `generate-config`, `tests/unit/config_test.go` | met |
| Structured logging with secret redaction | startup line shows `registration_mode:"open"` and no secret values; log grep for the password/token → 0 hits; `tests/unit/logging_test.go` | met |
| Graceful shutdown bounded by timeout | SIGTERM path in `tests/integration/lifecycle_test.go`; forced-close path added when the window expires | met |
| Database initialisation + migrations | `tests/unit/storage_test.go`, startup in every Complement container | met |
| Health endpoint / healthcheck | `lattice healthcheck` subcommand; used by the image `HEALTHCHECK` | met |
| Docker deployment satisfying Complement's contract | `deploy/complement/Dockerfile`; containers deploy healthy in every run; CA-signed federation certificate | met |
| Discovery and version endpoints | Complement `TestVersionStructure`, `TestServerCapabilities`, well-known tests | met |
| Identity layer (register/login/logout/devices/whoami) | Complement `TestRegistration`, `TestLogin`, `TestCanRegisterAdmin`, `TestDeviceManagement` (except `/sync`-asserting subtests), `TestLogout` (same caveat) | met for the M1 scope |
| Protocol primitives tested | canonical JSON spec vectors, sign/verify, identifier grammar | met |
| Complement runs without weakening assertions | 8 passed / 189 failed / 0 skipped; no Complement file modified (`git status` in `~/complement` clean) | met |
| `make verify` (fmt, vet, `-race`) green | recorded in PROGRESS.md | met |
| Honest compliance documentation | `docs/compliance-matrix.md`, `.agent-work/COMPLIANCE-MATRIX.md` | met |

Explicitly **not** claimed: rooms, events, state, sync, media storage, federation exchange. Those are
M2–M5 and are listed as failing in the compliance matrix rather than hidden.
