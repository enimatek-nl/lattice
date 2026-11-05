# Report — Milestone M1 (foundation + identity layer)

Date: 2026-11-05. This is a milestone report, **not** a project completion report: Matrix compliance
is far from reached and `docs/compliance-matrix.md` is the authority on what does and does not work.

## What was delivered

A runnable, modular Go homeserver skeleton with a working identity layer, protocol primitives built to
the specification appendices, and a reproducible Complement test environment.

```
cmd/lattice                 run | healthcheck | generate-config | version
internal/app                composition root (config → storage → services → HTTP)
internal/config  logging    env/file/flag config with validation; slog with secret redaction
internal/httpserver         two listeners, middleware chain, Matrix 404/405 routing semantics
internal/httpx  matrixerr   JSON in/out, protocol error bodies
internal/matrixjson         canonical JSON, signing/verifying JSON
internal/matrixid           identifier grammar (server names, MXIDs, room/alias/event/device IDs)
internal/persist            gormlite storage, migrations, repositories
internal/keys  auth  users  ed25519 signing keys, tokens, UIA sessions, accounts, devices
internal/api/{client,federation,media}
tests/unit  tests/integration
deploy/complement  scripts/complement.sh  Makefile  docs/
```

Endpoints that work: `/_matrix/client/versions`, `/capabilities`, `/.well-known/matrix/{client,server}`,
`/register` (UIA) and `/register/available`, `/_synapse/admin/v1/register` (shared secret), `/login`,
`/logout`, `/logout/all`, `/account/whoami`, `/devices` and `/devices/{id}` (GET/PUT/DELETE),
`/_matrix/federation/v1/version`, `/_matrix/key/v2/server`, `/_matrix/key/v2/query` (self only),
`/_matrix/media/v3/config`.

Everything else answers with a protocol error rather than a fake success — that is a deliberate
property, and Complement's `TestUnknownEndpoints` verifies it.

## Verification evidence

| Check | Result |
| --- | --- |
| `make verify` (gofmt, `go vet`, `go test -race`) | pass — `tests/integration` 4.6s, `tests/unit` 1.2s |
| `golangci-lint run ./...` | `0 issues.` |
| Own tests | 53 unit + 38 integration top-level tests, all passing |
| Complement M1 subset | `TestVersionStructure`, `TestUnknownEndpoints`, `TestRegistration`, `TestLogin`, `TestServerCapabilities`, `TestCanRegisterAdmin`, `TestMediaConfig` pass |
| Complement full `./tests ./tests/csapi` | 8 passed, 189 failed, 0 skipped; failures are unimplemented features (309 × 404 `M_UNRECOGNIZED`, 28 × 501 media, 1 × 400 refresh-token refusal) |
| Server health during the full run | 0 panics, 0 HTTP 500 responses |
| Manual curl smoke | UIA registration → `@alice:smoke.local`, uppercase login, whoami, device list, logout then `M_UNKNOWN_TOKEN`, 405 with `Allow`, signed key document, no secrets in logs |

No Complement file was modified; no assertion was weakened; nothing was skipped.

## Deviations from the generic backend skill, and why

1. **Tests in `tests/unit` and `tests/integration`, not beside the Go files** — an explicit instruction,
   and it keeps protocol fixtures away from production code.
2. **No JWT.** Matrix authentication is opaque access tokens bound to devices, stored as digests and
   revocable. A JWT would break `logout/all`, token rotation and federation semantics.
3. **GORM + gormlite** as the skill requires, but with `MaxOpenConns(1)` and explicit transactions:
   event/auth-chain invariants need serialisable writes, and the limit is recorded as a scalability
   constraint rather than a design goal.
4. **Canonical JSON is never "normalised" into Go structs.** Events stay as ordered raw documents so
   what is signed is what is stored.
5. **Federation auth is `X-Matrix` signing**, planned as its own subsystem, not middleware-style API auth.

## Defects found and fixed during the milestone

- A data race between listener handoff in `Serve` and `CloseListeners` in `Close` (found by `-race`,
  fixed with a mutex-guarded listener table; `-race` is now part of the milestone gate).
- Shutdown could return a bare `context.DeadlineExceeded` when connections outlived the graceful
  window; it now logs, force-closes and reports a forced shutdown.
- Federation certificates could not be verified by peers in test environments; the server now signs its
  certificate with a configured CA (Complement's mounted CA in tests, any private CA in deployment),
  proven by an integration test that trusts only that CA and performs no verification bypass.
- Two `golangci-lint` findings (ineffectual assignment, raw control character in a fixture).

## What M2 must do next

Rooms and events are the foundation everything else stands on: room-version registry (v9/v10/v11),
event DAG storage with shortnames and state snapshots, event authorization as its own subsystem,
state resolution v1/v2 (never last-write-wins), `POST /createRoom`, `PUT /send/{type}/{txn}` with
transaction idempotency, membership and `/state`. Task breakdown: `.agent-work/TASKS.md` (T-2000…T-2010).

## Compliance position

`docs/compliance-matrix.md`: implemented / tested / Complement-verified / partial / not implemented are
kept separate. The project is **not** Matrix-compliant and does not claim to be; 8 Complement top-level
tests pass today, and the honest gap is the remaining protocol.
