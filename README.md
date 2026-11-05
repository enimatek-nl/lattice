# Lattice

An independent Matrix homeserver implemented in Go. Not a fork of Synapse, Dendrite or Complement.

**Status: early stage.** The foundation and the identity layer (registration, login, devices,
sessions, server keys, discovery) exist and are tested, including against the Complement compliance
harness. Rooms, events, state, sync and federation are **not implemented yet**. The honest, per-area
picture lives in [`docs/compliance-matrix.md`](docs/compliance-matrix.md); nothing here should be
read as a compliance claim beyond that file.

## What works today

- Client-Server API: `GET /_matrix/client/versions`, `GET /capabilities`, `POST/GET /register`
  (User-Interactive Auth), `GET /register/available`, `GET/POST /login`, `POST /logout`,
  `POST /logout/all`, `GET /account/whoami`, `GET /devices`, `GET/PUT/DELETE /devices/{id}`,
  `/.well-known/matrix/client` and `/.well-known/matrix/server`.
- Server-Server / key API: `GET /_matrix/federation/v1/version`, `GET /_matrix/key/v2/server`,
  `POST /_matrix/key/v2/query` (this server only).
- Protocol primitives implemented to the appendices: canonical JSON, signing/verifying JSON,
  identifier grammar, unpadded base64.
- Protocol error semantics: 404/405 with `M_UNRECOGNIZED`, `M_MISSING_TOKEN`/`M_UNKNOWN_TOKEN`,
  `M_INVALID_USERNAME`/`M_USER_IN_USE`/`M_WEAK_PASSWORD`, 413, and no internal details in responses.
- Synapse-compatible shared-secret registration, disabled unless a secret is configured.
- Two listeners: plain HTTP for the Client-Server API, HTTPS (TLS ≥ 1.2) for the Server-Server and
  key APIs.

## Quick start

```sh
make build                 # bin/lattice
make run                   # http://127.0.0.1:8008, https://127.0.0.1:8448, self-signed cert
make verify                # gofmt check, go vet, race-enabled tests
make complement            # Complement suites against the built container image
```

Configure with a JSON file, environment variables or flags; `make generate-config` prints a
documented starting point:

```sh
bin/lattice -config /etc/lattice/lattice.json
LATTICE_SERVER_NAME=example.com LATTICE_REGISTRATION_OPEN=false bin/lattice
```

Environment variables are `LATTICE_*`: server name, data directory, listen addresses, TLS material
(including a CA to sign the federation certificate with), registration policy, shared secret, body
limits, timeouts and logging.

## Layout

```
cmd/lattice            the binary: run, healthcheck, generate-config, version
internal/app           wiring: config → storage → services → HTTP
internal/config        configuration, defaults, validation
internal/logging       slog with secret redaction
internal/httpserver    two listeners, middleware chain, Matrix 404/405 routing semantics
internal/httpx         JSON in/out, Matrix error responses
internal/matrixjson    canonical JSON, signing and verifying JSON
internal/matrixid      identifier grammar
internal/matrixerr     error codes and writers
internal/persist       gormlite storage, migrations, repositories
internal/keys          ed25519 signing keys, server key documents
internal/auth          access tokens, UIA sessions, authentication middleware
internal/users         accounts, password policy, devices, sessions
internal/api/client    Client-Server API handlers
internal/api/federation Server-Server and key API handlers
internal/api/media     media routes (repository not implemented yet)
tests/unit             protocol primitives, storage, auth, config, logging
tests/integration      the assembled server over real HTTP and TLS listeners
deploy/complement      Containerfile and entrypoint for the Complement harness
scripts/complement.sh  build the image, run Complement from a local checkout
.agent-work/           project state: plan, tasks, progress, requirements, blockers
docs/                  specification version, requirements, compliance matrix
```

Storage is SQLite (pure-Go `gormlite`) behind repository interfaces. That is a deliberate
single-process choice for this stage, and its limits are recorded in
`.agent-work/ARCHITECTURE.md` rather than pretended away.

## Testing

```sh
make test            # unit + integration
make test-race       # the same under -race
make cover           # coverage summary
make complement      # needs Docker/Podman and a Complement checkout (COMPLEMENT_DIR)
```

Tests live in `tests/`, not beside the Go files: `tests/unit` for primitives and services,
`tests/integration` for the assembled server. Complement is treated as an external reference —
its assertions are never modified to make results look better, and unsupported tests are listed
explicitly in `docs/compliance-matrix.md`.

## Deliberate design constraints

- The Matrix specification is the contract; internal structures never reshape protocol JSON.
- Room versions, event authorization and state resolution are designed to be room-version aware
  before any of them are implemented.
- Federation authentication is `X-Matrix` request signing, not ordinary API auth.
- Access tokens are opaque and stored as digests; passwords are bcrypt; tokens, passwords and key
  material are redacted in logs.
- Unimplemented endpoints answer with protocol errors, never with fake success.
