<p align="center">
  <img src="assets/lattice-banner.svg" alt="Lattice — a tiny Matrix homeserver written in Go" width="100%">
</p>

# Lattice

A minimal [Matrix](https://spec.matrix.org/latest/) homeserver written in Go:
the client-server HTTP API, a business layer, and a single SQLite file for
storage. The goal is a drop-in homeserver for clients that speak the standard
client API, plus the server-server API behind a flag (`-federation`, off by
default) — no external services. End-to-end encryption happens
in the clients; Lattice relays the key material, verifies the cross-signing keys
that decide what a client trusts, and stores encrypted room-key backups without
being able to read any of it.

**Status:** working homeserver whose federation moves membership across servers: a user
on one Lattice gets into a room on another, both hold the room, a room's decision about a
user reaches the server that user lives on, and a leave reaches the server that hosts the
room. The end-to-end cryptography stays on
the clients. **163 of
the 234** endpoints indexed in [SPECS.md](SPECS.md) are implemented, plus 31
extra routes (see [ENDPOINTS.md](ENDPOINTS.md)). 113 black-box compatibility
tests, 65 offline unit tests and 38 two-server federation tests are green.
`make build vet lint` clean (golangci-lint reports 0 issues).
Implementation version `0.1.0`
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
| `-no-rate-limit`                       | `false`                     | turn off request rate limiting entirely                                                                                             |
| `-rate-limit-write-burst`, `-rate-limit-write-rate`     | `100`, `30`               | requests per device before `M_LIMIT_EXCEEDED` on `/send`, `/createRoom`, `/join`, `/invite`, and the sustained requests per second    |
| `-rate-limit-auth-burst`, `-rate-limit-auth-rate`       | `20`, `2`                 | the same for `POST /login`, `/register` and `/refresh`, counted per address                                                          |
| `-rate-limit-upload-burst`, `-rate-limit-upload-rate`   | `20`, `5`                 | the same for media uploads, counted per device                                                                                       |
| `-media-reservation-window`            | `24h`                       | how long a media id from `POST /_matrix/media/v3/create` waits for its bytes before maintenance drops it                           |
| `-allow-guests`                        | `true`                      | allow guest account creation                                                                                                       |
| `-disable-registration`                | `false`                     | turn off self-service registration                                                                                                 |
| `-registration-secret`                 | _(empty)_                   | comma separated registration tokens; when set, registering requires one and the open `m.login.dummy` flow disappears               |
| `-cors-origin`                         | `*`                         | allowed CORS origin, or a comma separated list                                                                                     |
| `-support-email`, `-support-matrix-id` | _(empty)_                   | published in `/.well-known/matrix/support`                                                                                         |
| `-media`                               | `true`                      | enable the media endpoints                                                                                                         |
| `-maintenance-interval`                | `service.MaintenancePeriod` | how often to prune to-device messages, transaction ids and typing rows; `0` disables                                               |
| `-federation`                          | `false`                     | serve the server-server API: mount `/_matrix/federation/*` and `/_matrix/key/v2/*`, and generate this server's signing key         |
| `-federation-peer`                     | _(empty)_                   | comma separated `server=URL` pairs that pin where a peer is reached, bypassing well-known delegation and TLS; for tunnels and tests  |
| `-federation-flush`                    | `1s`                        | how often federation traffic waiting for a peer is retried; `0` delivers only when something asks for a flush                         |
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

With `-federation`, the server-server API is served too: `GET
/_matrix/key/v2/server` (and its `{keyID}` variant) publishes an ed25519 key
that is generated once and kept, `POST /_matrix/key/v2/query` and `GET
/_matrix/key/v2/query/{serverName}` fetch another server's keys, verify them and
re-sign them with this server's key, `GET /_matrix/federation/v1/version` says what
this server runs without requiring a signature, `GET
/_matrix/federation/v1/user/devices/{userId}` serves another server the device keys
of a local user, byte for byte as their own devices signed them, and `PUT
/_matrix/federation/v1/send/{txnId}` takes the transactions a peer delivers — up to
50 events and 100 ephemeral messages, answered per event, with `(origin, txnId)` and
its answer kept so a retry is answered rather than re-executed. Every request on
the authenticated paths carries an `Authorization: X-Matrix` signature over the
method, the exact request target, both server names and the body, and a request
that cannot be attributed to a known peer is refused with `401 M_UNAUTHORIZED`
before a handler sees it. Outbound requests are signed the same way, and a peer's
published keys are only trusted after checking that the document names the server
that was asked, is signed by a key it publishes, and has not expired — capped at
seven days however long the document claims to live.

Traffic leaves through a queue rather than through the request that produced it:
a message, a typing indicator, a receipt, a room key or a device list change is
stored in an outbox per destination and put on the wire by a loop
(`-federation-flush`). A peer that is down is retried with the same transaction id
and the same bytes, backing off from five seconds to ten minutes, and one
destination never has two transactions in flight — which is what makes the
receiver's transaction log meaningful. A room's `m.room.server_acl` is enforced in
both directions: the excluded server is not sent the room's events, and its events
are dropped on arrival.

An event that arrives is judged before it is kept, in the order the specification
gives: it has to be a valid event for the room's version, then the server that made it
has to have signed it with a key that server currently publishes (a peer delivering an
event is not its author and is never asked to sign it), then its content hash has to
cover the content that arrived — and when it does not, this server keeps the redacted
copy it computed itself rather than the copy it was sent. A peer that cannot prove one
of those gets a reason in that event's slot of the transaction answer, not a refused
transaction: the good events in the same batch should not be retried forever because
one event was broken. What is not done yet is deciding whether the event was *allowed*
in the room, and putting it there — see the next section.

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
  keys, over and over. The same trap sits under one-time keys, from the other side:
  a client counts the keys the server holds (in the upload response and in every
  sync) and uploads enough to reach its target of 100, so an upload **adds** to the
  pool instead of replacing it — replacing it keeps the count below the target
  forever, so the client uploads on every sync, and the keys other devices were
  about to claim disappear. A device keeps at most `MaxOneTimeKeys` (100) available
  keys, and a claimed key stays claimed even if the same key id is uploaded again.
  A key object is stored and handed to the claimer exactly as it arrived, because
  its signature is made by the uploading device over that very object and verified
  by whoever claims it: renaming its field or dropping its signatures looks
  harmless on the server and leaves the other side unable to decrypt forever. A
  database written before that fix is repaired when it is opened: unclaimed signed
  keys with no signature are dropped, because the only thing a client can do with
  one is fail to verify it, and the device uploads a proper key on its next sync.
  And the to-device path accepts `m.room.encrypted`, which is not a room event
  there but the Olm envelope: it is how every client hands the Megolm key to
  another device once an Olm session is open, with the type in the path and the
  envelope itself as the per-device value. Refusing it spends the claimed key and
  leaves the other side locked out of every message for good. Other room event
  types on that path are still refused.
  What that still
  leaves open: a device key signature is only checked when Lattice holds the key
  that made it, and none of this has been run against a real
  client test suite.
- **Federation judges and applies what arrives, but cannot join yet, and only behind
  `-federation`.** Two Lattice servers can exchange room events, typing, read
  receipts, room keys and device list updates for rooms both are in, and each
  refuses what the other cannot prove — an arriving event is checked against the room
  version's event format, against the author's signing key and against its own content
  hash, and then against the room's authorization rules: twice, once against the events
  the sender cites as its auth events and once against this server's own state of the
  room. Those two readings have different outcomes, and the difference is the point. A
  refusal keeps the event out of the room entirely, with the reason recorded beside the
  receipt for an operator to read. A *soft failure* — the sender's chain supports the
  event and this server's state does not — stores it, relays it onward, and hides it from
  every client read and from the room's state. An accepted event moves the state, the
  graph and the timeline, exactly as a local one does; missing auth events are fetched
  from the peer over `GET /_matrix/federation/v1/event/{eventID}`, which this server also
  serves. What no server can do yet is *join* a room across servers, so a user still
  cannot read a room that lives somewhere else.
  Deferred by decision rather than by omission: SRV lookups (a name is resolved by
  its well-known delegation or on port 8448), acting as a client of somebody
  else's notary server, and signing key rotation.
- **No calls** (`/voip/turnServer` publishes no TURN relay), **no SSO** or
  `m.login.token`, **no 3PID / email recovery**, **no application services**,
  **no bridges** (`/thirdparty/protocols` is empty).
- Rate limiting is in-process only: a token bucket per device on the expensive
  writes (`/send`, `/createRoom`, `/join`, `/invite`), per device on uploads and
  per address on `POST /login`, `/register` and `/refresh`, answered with
  `M_LIMIT_EXCEEDED` and `retry_after_ms`. Buckets live in memory, so they reset
  on restart, and they are keyed on the peer address — behind a reverse proxy
  every client shares one bucket until proxy support lands. Tune the budgets with
  `-rate-limit-write-burst` and friends, or switch the whole thing off with
  `-no-rate-limit`.
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
internal/coverage route-to-spec matcher the ENDPOINTS.md numbers come from
internal/power    power level arithmetic (which level a user has, what an action asks for)
internal/authcheck  the authorization rules of a room version: may this event be in this
                   room, given the state these rules read
internal/stateres  state resolution v2: two servers' claims about one room, resolved to
                   one state
internal/push     push rule engine: rule precedence, pattern and count matching
internal/federation  signing identity, X-Matrix auth, peer transport, key caches,
                   transaction envelopes, server ACLs
internal/eventcrypto  the event's own rules: redaction, hashes, derived ids, signatures
internal/service  business logic (one file per feature area)
internal/api      HTTP handlers and the route table (routes.go)
tests/compat      black-box compatibility suite: HTTP against a running server
tests/federation  two Lattice servers in one process, talking to each other
tests/unit        offline unit tests: pure functions, tables, no server
```

Functional tests live in `tests/` only, never next to the implementation — that
is what makes "which endpoint is already covered?" answerable. The pure rules
federation will reuse (identifiers, errors, power levels, push rules, tokens,
endpoint matching, canonical JSON and signatures) are additionally pinned in
`tests/unit`, where a wrong answer is a readable table diff rather than a
confusing HTTP response. `tests/federation` exists because a claim about
federation cannot be checked from inside one server: a bug in a join is a
disagreement between two databases.

## Commands

```sh
make help                                # list the targets, one line each
make build                               # bin/lattice
make test                                # unit + compatibility suite
make race                                # the same under -race
make vet lint fmt                        # go vet, golangci-lint, gofmt -s
make endpoints                           # regenerate ENDPOINTS.md
make run                                 # dev server on 127.0.0.1:8008
make swagger                             # OpenAPI docs, docs/swagger/ (not committed)
make clean                               # drop bin/ and docs/swagger/
make complement-image                    # the container image Complement drives
make complement                          # the upstream Matrix suite, ~5 min

go build ./... && go vet ./...
go test ./tests/unit/                      # the offline unit tables, milliseconds
go test ./tests/federation/                # two servers talking to each other, under a second
go test ./tests/compat/ -race              # ~5 min because of the long-poll tests
go test ./tests/compat/                    # ~50 s
go run ./cmd/endpoints                     # coverage summary
go run ./cmd/endpoints -all                # every implemented / missing route
go run ./cmd/endpoints -out ENDPOINTS.md   # regenerate the coverage report
```

`ENDPOINTS.md` is generated — regenerate it after adding a route instead of
editing it. `docs/swagger/` is generated too and gitignored: `make swagger`
fetched swag on demand, so the repository carries the annotations but not the
OpenAPI document, and no Swagger UI route is mounted.

`make complement` runs [Complement](https://github.com/matrix-org/complement),
the upstream Matrix test suite, against a container built from `bin/lattice` by
`deploy/complement/`. It is deliberately not part of `make test`: it needs Docker
(or Podman — the target only calls `docker`), and a checkout of Complement
itself, which is not a module dependency because its own `go.mod` owns the test
binaries. Point the target at that checkout (`COMPLEMENT=~/complement make
complement`), narrow it with `COMPLEMENT_RUN='TestRoomAlias|TestRoomCreate'`, and
use `COMPLEMENT_PKG=./tests` for the server-server suite — 91 tests, 72 of them
red, 19 green: the membership handshake and what hangs off it is green, and the rest
waits on the handshakes Lattice does not serve yet — knock, invite, and the
graph-reading queries of TODO step 11. Where
Lattice stands, and what each remaining failure is missing, is written up under
_Complement_ in [TODO.md](TODO.md).

## Where development stands

[PLAN.md](PLAN.md) holds the goal and the phases, [TODO.md](TODO.md) is the
working hand-off document (verified facts, done list, in-flight notes, next
chunk), [SPECS.md](SPECS.md) is the full endpoint index from the spec, and
[ENDPOINTS.md](ENDPOINTS.md) is the generated implemented/missing table.

Phase 1 (bare POC) and phase 2 (the everyday client endpoints) are done, and so
is the E2EE key layer: device keys, cross-signing and server-side key backup. The
release plan in [TODO.md](TODO.md) works step by step: rate limiting, the client
API correctness tails, batch reads, unit tests in `tests/unit/`, then federation
(steps 6–12) behind a `-federation` flag — the decision and the full plan are
recorded there.

Note: `SPECS.md` references a spec corpus that is not in this repo, so anything
depending on exact spec wording (the default push rule set, `/threads` ordering,
key backup formats) needs the corpus fetched first.

The external check is Complement: `make complement` runs the upstream Matrix
test suite against a container built from this binary. It is a different yardstick
from the in-repo compatibility suite — it is written against the specification by
people who did not write this server — and the current tally, plus what each
remaining failure is waiting for, is written up under _Complement_ in
[TODO.md](TODO.md).

## House rules

- Handlers stay thin: decode → `service` call → `writeJSON`; errors go through
  `a.writeError` (which uses `matrix.ErrAs`).
- New endpoint: add the route in `internal/api/routes.go`, add a case in
  `tests/compat`, re-run the coverage tool.
- Package names short and lowercase; tests are table-driven; context is the
  first parameter of anything that does I/O.
