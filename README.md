# Lattice

An independent Matrix homeserver implemented in Go. Not a fork of Synapse, Dendrite or Complement.

**Status: a homeserver with its core in place and its federation handshake half-built.** The
identity layer, rooms and events (creation, sending, state, membership, redaction, aliases, room
directory, upgrades, spaces), synchronisation (`/sync`, `/messages`, `/members`), the content
repository, device keys, push rules and a first federation layer — discovery, `X-Matrix` signing,
`/send/{txnId}`, the join handshake, `/state_ids`, `/event`, `/event_auth`, `/get_missing_events`,
backfill, federation media, server ACLs and restricted rooms — are implemented and tested, the last
several of them against the Complement compliance harness with two homeservers in one deployment.
Leaves, invites over federation, knocking, threads and relations, receipts and read markers, typing,
cross-signing and key backup, search, room version 12 and the MSCs built on it are **not** implemented,
and they answer with a protocol error rather than a fake success.

The measured position, from a full run on 2026-11-07: **104 Complement top-level tests pass, 115 fail,
1 skipped** (csapi 68 of 107, server-server 34 of 92). That is the honest number, not a compliance
claim. The per-area picture — implemented, tested here, verified by Complement, partial, absent — is in
[`docs/compliance-matrix.md`](docs/compliance-matrix.md), and the raw log and the pass/failure lists
behind every tally quoted there live under `.agent-work/`, which is working state kept out of
the repository — a clone will not have them, and re-running the command reproduces them.

## What works today

- Client-Server API: `GET /_matrix/client/versions`, `GET /capabilities`, `POST/GET /register`
  (User-Interactive Auth), `GET /register/available`, `GET/POST /login`, `POST /logout`,
  `POST /logout/all`, `GET /account/whoami`, `GET /devices`, `GET/PUT/DELETE /devices/{id}`,
  `/.well-known/matrix/client` and `/.well-known/matrix/server`, `/_synapse/admin/v1/register`
  (shared secret), the profile endpoints and the membership restatement that follows a profile change.
- Rooms: `POST /createRoom` (presets, version negotiation, initial state), `PUT /send/{type}/{txn}`
  with per-device idempotency, `PUT /state/{type}[/{key}]`, join / invite / knock / leave / ban / kick /
  unban, `GET /state*`, `/members`, `/messages`, `/event/{id}`, `PUT /redact/{id}/{txn}`, `/aliases`,
  `/directory/room/{alias}`, `/publicRooms`, `/upgrade`, `/joined_rooms`, `/joined_members`,
  `/hierarchy`, `/room_summary/{id}`.
- Synchronisation: `GET /sync` (incremental, `since`, long-poll, `device_lists`, `to_device` absent,
  account data), `GET/PUT /user/{id}/filter`, user `account_data`, presence for one user.
- Device keys and encryption support: `POST /keys/upload`, `/keys/query` (including asking a peer for a
  remote user), `/keys/claim`, federation `user/keys/query`, device-list change announcements.
- Push rules: the whole read/write `/pushrules` API and delivery through `account_data` in `/sync`;
  the per-event evaluation that would make unread and highlight counts mean anything is not done.
- Content repository: upload (sync and async), download (including the authenticated `v1` forms),
  thumbnail, `config`, remote content fetched once from the origin and served from the copy held here,
  and the two federation `media/download|thumbnail` routes.
- Server-Server / key API: `GET /_matrix/federation/v1/version`, `GET /_matrix/key/v2/server`,
  `POST /_matrix/key/v2/query` (this server only), server discovery (`.well-known`, SRV, IP literals,
  address policy), peer key fetching with notary fallback, `PUT /send/{txnId}` in both directions,
  `make_join`/`send_join`, `state_ids`, `event_auth`, `event`, `get_missing_events`, `backfill` (asking),
  `hierarchy`, `invite` v1/v2, restricted joins with an authoriser this server chooses.
- Protocol primitives implemented to the appendices: canonical JSON, signing/verifying JSON,
  identifier grammar, unpadded base64 — with redaction and event-ID derivation cross-checked against
  gomatrixserverlib by golden test, and room version 1-11 authorisation and State Resolution 2 on the
  live path (each event's state snapshot is recorded; a fork is resolved, not last-write-wins).
- Protocol error semantics: 404/405 with `M_UNRECOGNIZED`, `M_MISSING_TOKEN`/`M_UNKNOWN_TOKEN`,
  `M_INVALID_USERNAME`/`M_USER_IN_USE`/`M_WEAK_PASSWORD`, 413, and no internal details in responses.
- Two listeners: plain HTTP for the Client-Server API, HTTPS (TLS ≥ 1.2) for the Server-Server and
  key APIs.

## What does not work yet

`/context`, `/event_auth`, `/timestamp`, `/relations`, `/threads`, `/search`, `/keys/changes`,
cross-signing and key backup, to-device send, receipts and read markers, typing, account
deactivation and password change, room-scoped account data, pushers and the push gateway, URL
previews, appservices, refresh tokens, room version 12 (recognised and refused, not invented), and
the federation halves of leave, invite, knock and `/backfill`. Each is a task in
[`.agent-work/TASKS.md`](.agent-work/TASKS.md), not a gap in the documentation.

## Quick start

```sh
make build                 # bin/lattice
make run                   # http://127.0.0.1:8008, https://127.0.0.1:8448, self-signed cert
make verify                # gofmt check, go vet, race-enabled tests
make complement            # Complement suites against the built container image
make complement-all        # the whole suite, writing its log and tally lists under .agent-work/
make image                 # the release image (static binary, no shell, no compiler)
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
