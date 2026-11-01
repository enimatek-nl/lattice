<p align="center">
  <img src="assets/lattice-banner.svg" alt="Lattice — a tiny Matrix homeserver written in Go" width="100%">
</p>

# Lattice

A minimal [Matrix](https://spec.matrix.org/latest/) homeserver written in Go:
the client-server HTTP API, a business layer, and a single SQLite file for
storage. The goal is a drop-in homeserver for clients that speak the standard
client API — no federation, no external services. End-to-end encryption happens
in the clients; Lattice relays the key material, verifies the cross-signing keys
that decide what a client trusts, and stores encrypted room-key backups without
being able to read any of it.

**Status:** working single-server homeserver; the cryptography stays on the
clients. **141 of
the 234** endpoints indexed in [SPECS.md](SPECS.md) are implemented, plus 24
extra routes (see [ENDPOINTS.md](ENDPOINTS.md)). 88 black-box compatibility tests
are green. `go build ./... && go vet ./...` clean. Implementation version `0.1.0`
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
server through `/.well-known/matrix/client`. When the server sits behind a proxy
or a tunnel, publish the URL clients should use with `-base-url`; the default is
`https://<-server-name>`, which is rarely right for a development server.

### Registration is User-Interactive Auth

Registration sits behind the User-Interactive Auth exchange the specification
defines, because that is how clients discover it:

```sh
# a request with no auth dict (POST with {} or GET) answers 401 with the flows
curl -s -X POST localhost:8008/_matrix/client/v3/register -d '{}' -H 'Content-Type: application/json'
# {"errcode":"M_UNAUTHORIZED","flows":[{"stages":["m.login.dummy"]}],"params":{},
#  "session":"…","completed":[]}
```

The client then repeats the request with the stage it completed and the `session`
it was given. `m.login.dummy` is the flow on an open server; `-registration-secret`
replaces it with `m.login.registration_token`, and
`GET /_matrix/client/v1/register/m.login.registration_token/validity` reports which
one is in force. `m.login.password` and the Lattice-specific
`org.matrix.lattice.admin` (an administrator vouching for the request) are also
accepted as stages. Guest accounts skip the exchange — a client asking for a
throwaway session has nothing to authenticate with. Sessions live in memory and
are pruned by the housekeeping loop, so a server restart abandons unfinished
registrations.

### Flags

| Flag                                   | Default                     | Meaning                                                                                                                            |
| -------------------------------------- | --------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `-addr`                                | `:8008`                     | listen address                                                                                                                     |
| `-server-name`                         | `localhost`                 | homeserver name; user and room IDs end with it                                                                                     |
| `-base-url`                            | `https://<-server-name>`    | URL published in `/.well-known/matrix/client`; set it when a proxy or tunnel fronts the server                                     |
| `-db`                                  | `lattice.db`                | SQLite database file                                                                                                               |
| `-jwt-secret`                          | _(empty)_                   | secret used to sign access tokens; **set it** — an empty value generates a fresh one per boot and logs every client out on restart |
| `-token-ttl`                           | `24h`                       | access token lifetime                                                                                                              |
| `-max-upload-size`                     | `10 MiB`                    | maximum media upload size                                                                                                          |
| `-media-reservation-window`            | `24h`                       | how long a media id from `POST /_matrix/media/v3/create` waits for its bytes before maintenance drops it                           |
| `-allow-guests`                        | `true`                      | allow guest account creation                                                                                                       |
| `-disable-registration`                | `false`                     | turn off self-service registration                                                                                                 |
| `-registration-secret`                 | _(empty)_                   | comma separated registration tokens; when set, registering requires one and the open `m.login.dummy` flow disappears               |
| `-cors-origin`                         | `*`                         | allowed CORS origin, or a comma separated list                                                                                     |
| `-support-email`, `-support-matrix-id` | _(empty)_                   | published in `/.well-known/matrix/support`                                                                                         |
| `-media`                               | `true`                      | enable the media endpoints                                                                                                         |
| `-maintenance-interval`                | `service.MaintenancePeriod` | how often to prune to-device messages, transaction ids and typing rows; `0` disables                                               |
| `-version`                             | `false`                     | print the version and exit                                                                                                         |

## What works today

`versions`, register / login / refresh / logout / `whoami`, room creation,
aliases and the public room directory, invite / join / knock / leave / ban /
kick / forget, sending and reading events (`/send`, `/messages`, `/context`,
state), long-polling sync with unread and notification counts, receipts and
read markers, typing, push rules and pushers, search (both request forms),
spaces and `/hierarchy`, threads and relations, room versions 1-11 (11 by
default, all advertised as stable in `/v1/capabilities`, `POST /rooms/{id}/upgrade`
included, which requires a version, copies the transferable state, locks the power
levels of the old room and closes it to further events), media upload/download/thumbnail
including the async create → upload → commit flow Element uses, the profile and
third-party-identifier endpoints (the 3PID listing is real and empty; linking an
address refuses with a reason, because no verification mail or text is ever sent),
profiles, filters, account data, redaction, device management (`/devices`),
device keys (`/keys/upload` - whose identity key is read under either name a
client writes it, the specification's `device_keys` that the Rust crypto stack
Element ships sends and the older `device_key` - and `/keys/query`, which answers
cross-signing keys in the `master_keys`, `self_signing_keys` and
`user_signing_keys` maps the specification names, never among the device entries
a client parses as a device -
`/keys/claim`, `/keys/changes` and
the key sections of `/sync`, including unused fallback keys named under the
algorithm they were uploaded under), cross-signing (`/keys/device_signing/upload`
behind the interactive auth exchange only when a request would change an
identity that already exists - the first set of keys of a new account, and a
repeat of the stored set, are accepted without one, as the specification has
required since v1.11 and as Element relies on while it creates a user; whose
only extra spec-defined auth type is `m.oauth` for OAuth homeservers;
`/keys/signatures/upload`, whose body is read in all three shapes a client
may send it in - the specification's own bare map of user ID to key ID to
signed object (what ruma, the Rust crypto stack Element ships, and
matrix-js-sdk send), and the wrapped `signed_users` and `signed_keys` forms
Lattice has taken from the start) and
server-side room key backup (`/room_keys/*`, versions created with `PUT` and named
as strings, deleted for real so a recreated backup cannot collide with the number
of the one that went away), the admin routes, abuse reports, and
`/.well-known/matrix/{client,server,support}`. Endpoints that are not implemented
answer `404 M_UNRECOGNIZED` rather than a bare HTTP 404, so a client can name the
endpoint that is missing.

## What does not work yet

- **End-to-end encryption is client-side, and the server does not read it.**
  Device keys, one-time keys, fallback keys and backed up room keys are stored as
  the opaque JSON a client uploaded, so two clients can establish Olm sessions,
  exchange `m.room.encrypted` events, verify each other and restore old messages
  from a backup. The cross-signing keys are the exception: those are verified
  here, because everything else in the trust layer rests on them - and verified
  without being rewritten, including the `usage` field, which is accepted under
  either spelling in use (the specification writes `self-signing`, the Rust crypto
  stack Element ships writes `self_signing`, and refusing a correctly signed key
  over a hyphen leaves a user unable to set up encryption at all). A device is also
  never told about a device list change it caused itself: uploading an identity key
  records one so the user's other devices and the people in their rooms re-query,
  and echoed back to the uploader that same record makes a client fetch its own
  keys, over and over. What that still
  leaves open: a device key signature is only checked when Lattice holds the key
  that made it, and none of this has been run against a real
  client test suite.
- **No federation** — nothing under `/_matrix/federation/v1/*`. Users can only
  reach users on this server.
- **No calls** (`/voip/turnServer` publishes no TURN relay), **no SSO** or
  `m.login.token`, **no 3PID / email recovery**, **no application services**,
  **no bridges** (`/thirdparty/protocols` is empty).
- **No rate limiting** — `M_LIMIT_EXCEEDED` and `retry_after_ms` exist in the
  error types but nothing raises them.
- Search is a `LIKE` scan over serialised event content: no word index, no
  stemming, no relevance ranking.

## Operational notes

- The server speaks plain HTTP; terminate TLS in a reverse proxy. Publish the
  public URL with `-base-url`, otherwise `/.well-known/matrix/client` points
  clients at `https://<-server-name>` and discovery from a hosted client (for
  example app.element.io) resolves to the wrong address.
- A client that is handed a server name it has no URL for goes looking for one at
  `https://<server-name>/.well-known/matrix/client`. With `-server-name localhost`
  that is a connection to a local HTTPS server that does not exist, which shows up
  in the browser console as a failed request even though the session works. Set
  `-server-name` to the name clients should dial and `-base-url` to the URL they
  should call; a client that signed in through that URL stops probing.
- A long poll the client gives up on is not treated as an error. Element restarts
  its sync loop whenever crypto setup or a filter changes; for those requests
  Lattice writes no body and logs nothing, so the faults worth reading stay
  visible instead of burying the log in `context canceled`.
- Every User-Interactive Auth exchange leaves two lines in the log: the challenge
  (what it was for, the session id, the stages already done and the ones on
  offer) and the outcome of the stage the client sent. That is how to tell the
  difference between a client that never came back with credentials, one that
  came back with the wrong ones, and one whose request was accepted - which is
  the question a client-side "unable to set up keys" leaves open. (The
  cross-signing challenge below only stands in front of a request that would
  change an identity the account already has; publishing the first set of keys
  needs no exchange at all):

    ```text
    interactive auth: asking for additional authentication is required to upload
      cross-signing keys (session 4hQx…, completed [], stages [m.login.password])
    interactive auth: @tim:localhost completed the m.login.password stage
    ```

- Everything — events, state, accounts, and uploaded media — lives in one SQLite
  file. Backing up means copying that file; `-max-upload-size` bounds its growth.
- Always pass `-jwt-secret`. Sessions are stored server-side, so deleting a
  session revokes its token; logout really stops a token from working.
- The **first** registered account is the administrator (`/_synapse/admin/*` and
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

Phase 1 (bare POC) and phase 2 (the everyday client endpoints) are done, and so
is the E2EE key layer: device keys, cross-signing and server-side key backup. The
next chunks queued in [TODO.md](TODO.md) are `account/3pid/*`, SSO, appservice
APIs, unit tests in `tests/unit/`, a Makefile and OpenAPI generation. Federation
is decided in scope as phase 3, gated behind a `-federation` flag — the decision
and the full plan are recorded in [TODO.md](TODO.md).

Note: `SPECS.md` references a spec corpus that is not in this repo, so anything
depending on exact spec wording (the default push rule set, `/threads` ordering,
key backup formats) needs the corpus fetched first.

## House rules

- Handlers stay thin: decode → `service` call → `writeJSON`; errors go through
  `a.writeError` (which uses `matrix.ErrAs`).
- New endpoint: add the route in `internal/api/routes.go`, add a case in
  `tests/compat`, re-run the coverage tool.
- Package names short and lowercase; tests are table-driven; context is the
  first parameter of anything that does I/O.
