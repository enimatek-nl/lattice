# Lattice

A minimal [Matrix](https://spec.matrix.org/latest/) homeserver written in Go:
the client-server HTTP API, a business layer, and a single SQLite file for
storage. The goal is a drop-in homeserver for clients that speak the standard
client API — no federation, no end-to-end encryption, no external services.

**Status:** working single-server, unencrypted homeserver. **113 of the 234**
endpoints indexed in [SPECS.md](SPECS.md) are implemented, plus 23 extra routes
(see [ENDPOINTS.md](ENDPOINTS.md)). 61 black-box compatibility tests are green.
`go build ./... && go vet ./...` clean. Implementation version `0.1.0`
(reported by `GET /_matrix/client/versions` as `Lattice/0.1.0`).

## Quick start

```sh
go build -o lattice ./cmd/server
./lattice --addr 127.0.0.1:8008 --server-name localhost --db lattice.db --jwt-secret dev
```

Then talk to it like a Matrix client:

```sh
# register — the first account on a fresh server becomes the administrator
curl -X POST localhost:8008/_matrix/client/v3/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"w1nter-snow","auth":{"type":"m.login.dummy"}}'

# log in and keep the access token
TOKEN=$(curl -s localhost:8008/_matrix/client/v3/login \
  -H 'Content-Type: application/json' \
  -d '{"type":"m.login.password","identifier":{"type":"m.id.user","user":"alice"},"password":"w1nter-snow"}' \
  | jq -r .access_token)

# create a room, send a message, sync
curl -s -X POST "localhost:8008/_matrix/client/v3/createRoom?access_token=$TOKEN" \
  -d '{"name":"lobby","visibility":"private"}'
curl -s "localhost:8008/_matrix/client/v3/sync?access_token=$TOKEN"
```

Point a desktop or web client at `http://localhost:8008` and it will discover the
server through `/.well-known/matrix/client`.

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-addr` | `:8008` | listen address |
| `-server-name` | `localhost` | homeserver name; user and room IDs end with it |
| `-db` | `lattice.db` | SQLite database file |
| `-jwt-secret` | *(empty)* | secret used to sign access tokens; **set it** — an empty value generates a fresh one per boot and logs every client out on restart |
| `-token-ttl` | `24h` | access token lifetime |
| `-max-upload-size` | `10 MiB` | maximum media upload size |
| `-allow-guests` | `true` | allow guest account creation |
| `-disable-registration` | `false` | turn off self-service registration |
| `-cors-origin` | `*` | allowed CORS origin, or a comma separated list |
| `-support-email`, `-support-matrix-id` | *(empty)* | published in `/.well-known/matrix/support` |
| `-media` | `true` | enable the media endpoints |
| `-maintenance-interval` | `service.MaintenancePeriod` | how often to prune to-device messages, transaction ids and typing rows; `0` disables |
| `-version` | `false` | print the version and exit |

## What works today

`versions`, register / login / refresh / logout / `whoami`, room creation,
aliases and the public room directory, invite / join / knock / leave / ban /
kick / forget, sending and reading events (`/send`, `/messages`, `/context`,
state), long-polling sync with unread and notification counts, receipts and
read markers, typing, push rules and pushers, search (both request forms),
spaces and `/hierarchy`, threads and relations, media upload/download/thumbnail,
profiles, filters, account data, redaction, device management (`/devices`),
the admin routes, abuse reports, and `/.well-known/matrix/{client,server,support}`.

## What does not work yet

* **No end-to-end encryption** — nothing under `/_matrix/client/v3/keys/*` or
  `/room_keys/*`. There are no device keys to exchange, so encrypted rooms,
  interactive verification and key backup cannot work.
* **No federation** — nothing under `/_matrix/federation/v1/*`. Users can only
  reach users on this server.
* **No calls** (`/voip/turnServer`), **no SSO** or `m.login.token`, **no 3PID /
  email recovery**, **no application services**.
* **No rate limiting** — `M_LIMIT_EXCEEDED` and `retry_after_ms` exist in the
  error types but nothing raises them.
* Search is a `LIKE` scan over serialised event content: no word index, no
  stemming, no relevance ranking.

## Operational notes

* The server speaks plain HTTP; terminate TLS in a reverse proxy.
* Everything — events, state, accounts, and uploaded media — lives in one SQLite
  file. Backing up means copying that file; `-max-upload-size` bounds its growth.
* Always pass `-jwt-secret`. Sessions are stored server-side, so deleting a
  session revokes its token; logout really stops a token from working.
* The **first** registered account is the administrator (`/_synapse/admin/*` and
  `/_matrix/client/v1/admin/*` require it).

## Layout

```
cmd/server        server entry point (flags, graceful shutdown)
cmd/endpoints     coverage report tool (reads SPECS.md, writes ENDPOINTS.md)
internal/matrix   Matrix identifiers, event types, error shapes, sync structs
internal/persist  GORM + gormlite (pure-Go SQLite) models and repositories
internal/auth     JWT tokens, auth middleware, context getters
internal/realtime hub that wakes long-polling syncs
internal/service  business logic (one file per feature area)
internal/api      HTTP handlers and the route table (routes.go)
tests/compat      black-box compatibility suite: HTTP against a running server
```

Functional tests live in `tests/` only, never next to the implementation — that
is what makes "which endpoint is already covered?" answerable.

## Commands

```sh
go build ./... && go vet ./...
go test ./tests/compat/ -race              # ~3 min because of the long-poll tests
go test ./tests/compat/                    # ~27 s
go run ./cmd/endpoints                     # coverage summary
go run ./cmd/endpoints -all                # every implemented / missing route
go run ./cmd/endpoints -out ENDPOINTS.md   # regenerate the coverage report
```

`ENDPOINTS.md` is generated — regenerate it after adding a route instead of
editing it.

## Where development stands

[PLAN.md](PLAN.md) holds the goal and the phases, [TODO.md](TODO.md) is the
working hand-off document (verified facts, done list, in-flight notes, next
chunk), [SPECS.md](SPECS.md) is the full endpoint index from the spec, and
[ENDPOINTS.md](ENDPOINTS.md) is the generated implemented/missing table.

Phase 1 (bare POC) and phase 2 (the everyday client endpoints) are done. The
next chunks queued in [TODO.md](TODO.md) are the device key / E2EE APIs,
server-side key backup, `account/3pid/*`, SSO, appservice APIs, unit tests in
`tests/unit/`, a Makefile and OpenAPI generation. Federation is probably out of
scope — that decision is not recorded yet.

Note: `SPECS.md` references a spec corpus that is not in this repo, so anything
depending on exact spec wording (the default push rule set, `/threads` ordering,
key backup formats) needs the corpus fetched first.

## House rules

* Handlers stay thin: decode → `service` call → `writeJSON`; errors go through
  `a.writeError` (which uses `matrix.ErrAs`).
* New endpoint: add the route in `internal/api/routes.go`, add a case in
  `tests/compat`, re-run the coverage tool.
* Package names short and lowercase; tests are table-driven; context is the
  first parameter of anything that does I/O.
