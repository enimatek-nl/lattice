<p align="center">
  <img src="assets/lattice-banner.svg" alt="Lattice — a tiny Matrix homeserver written in Go" width="100%">
</p>

# Lattice

**A small Matrix homeserver: one Go binary, one SQLite file, no external services.**

Run it, register, and you have a homeserver. Turn federation on and your users can
join rooms on other servers, and their rooms can receive yours. Encryption happens
in the clients; Lattice relays the key material, verifies the cross-signing keys
that decide what a client trusts, and stores room-key backups it cannot read.

> **Alpha.** Version `0.1.0`. This is the first release: it is complete for what it
> aims at — hosting one person's account and talking to the rest of Matrix about
> rooms up to **room version 11** — and it is honest about the rest, listed under
> [What is not there yet](#what-is-not-there-yet). Nothing about the on-disk format
> or the flags is stable promise yet.

---

## At a glance

| | |
| --- | --- |
| **You need** | a binary and a writable directory |
| **Storage** | one SQLite file — back it up by copying it |
| **Binary** | static, no cgo: ~21 MB stripped |
| **Container** | `ghcr.io/enimatek-nl/lattice` — multi-arch, published from a tag; `make image` builds it locally |
| **Client API** | 168 of the 234 indexed spec endpoints, plus 33 extra routes (`make endpoints`) |
| **Federation** | keys, signed requests, join / leave / invite, events, typing, receipts, device lists, state and graph reads, gap filling — behind `-federation` |
| **Room versions** | 1–11 (11 is the default; room upgrades supported) |
| **Tests** | 117 client-API compatibility, 43 two-server federation, 68 unit |
| **Licence** | MIT |

---

## Try it

```sh
go build -o lattice ./cmd/server
./lattice -addr 127.0.0.1:8008 -server-name localhost -db lattice.db -jwt-secret dev
```

Point a client at `http://localhost:8008` — Element Web, Element Desktop, FluffyChat,
Hiligony, any of them. The first account you register is the administrator.

Or take the image, and say the same two things as environment variables:

```sh
docker run --rm -p 8008:8008 \
  -e LATTICE_SERVER_NAME=localhost -e LATTICE_JWT_SECRET=dev \
  ghcr.io/enimatek-nl/lattice
```

Prefer the command line:

```sh
curl -X POST localhost:8008/_matrix/client/v3/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"w1nter-snow","auth":{"type":"m.login.dummy"}}'

TOKEN=$(curl -s localhost:8008/_matrix/client/v3/login -H 'Content-Type: application/json' \
  -d '{"type":"m.login.password","user":"alice","password":"w1nter-snow"}' | jq -r .access_token)

curl -s -X POST "localhost:8008/_matrix/client/v3/createRoom?access_token=$TOKEN" -d '{"name":"lobby"}'
curl -s "localhost:8008/_matrix/client/v3/sync?access_token=$TOKEN"
```

---

## Running it for real

Lattice speaks plain HTTP. Put a reverse proxy (Caddy, nginx, Traefik) in front for
TLS, then get four settings right — most federation failures come from the first two
(in a container each one is the `LATTICE_*` variable of the same name, see
[In a container](#in-a-container)):

1. **`-server-name` is the identity of your server.** It is what every user ID and room
   ID ends with. Set it once and do not change it: accounts written under an old name
   cannot use the new one. **Do not put a port here** — the port peers should dial
   belongs in `-federation-advertise`.
2. **Peers have to be able to reach you.** They look for
   `https://<server-name>/.well-known/matrix/server`, and if it answers
   `{"m.server":"example.org:443"}` they use that; if it says nothing, they knock on
   port **8448**. A peer that cannot fetch `/_matrix/key/v2/server` rejects everything
   you send, and its complaint reaches your users as a room that will not open.
   `-federation-advertise example.org:443` puts the right value in that document.
3. **`-base-url`** is what `/.well-known/matrix/client` tells clients to call. Behind a
   proxy this is not `https://<server-name>`, so set it.
4. **`-jwt-secret`** signs access tokens. Without it a fresh secret is generated per
   boot, which logs every client out on restart.

Backups: stop the server, copy the `-db` file. That is the whole backup.

### Accounts from a terminal

A server started with `-disable-registration` has no way to gain an account, and its
operator has to have one. The same binary runs the account commands instead of the
homeserver:

```bash
lattice add-user alice                        # asks for the password twice, hides what you type
lattice add-user bob -admin -display-name 'Bob B'
echo "$PASSWORD" | lattice add-user carol     # a script, or a container without a terminal

lattice users                                 # what is in the database
lattice remove-user alice                     # asks for the user ID back before it goes

lattice add-user sanne -db /var/lib/lattice/lattice.db -server-name matrix.example.org
```

They work while the homeserver is running, on the same file: the new account can sign in a
second later, and `remove-user` ends the sessions that are live right now. In a container
that is `docker exec`, where `-db` and `-server-name` come from the image's environment:

```bash
docker exec -it lattice lattice add-user alice
docker exec -i lattice sh -c 'echo "$PASSWORD" | lattice add-user bob'
```

| Command | Flags | |
|---|---|---|
| `add-user <name>` | `-password`, `-admin`, `-display-name` | Creates the account and sets its password. The first account on a fresh database becomes an administrator whether or not `-admin` is given, so the administrative endpoints have somebody to answer before any flag names them. |
| `remove-user <name>` | `-yes` | Deactivates the account: its sessions end and its tokens stop working, its device keys, cross-signing keys and room-key backups are deleted, and it leaves its rooms. `-yes` skips the confirmation, for a script. |
| `users` | `-limit`, `-all` | Lists the accounts and what they are, and says how many are deactivated and how many belong to a different server name. |
| `help` | | Lists the commands. `lattice -h` lists them next to the server's own flags. |

`-db` and `-server-name` are the same settings the server reads, with the same names and
the same precedence. A command's other flags are deliberately *not* read from the
environment: a `LATTICE_PASSWORD` left in a container would otherwise become the password
of whichever account somebody adds next.

`remove-user` deactivates rather than deletes. A deleted row would free the name, and a
Matrix user ID is written into every event its account ever sent: whoever took the name
next would own those messages in any room that still shows them. So the name stays taken,
which is also why `add-user` refuses to reuse it.

---

## Federation

Off by default, on with `-federation`. What it gives you:

- **Identity.** An ed25519 signing key is generated once and kept in the database,
  published at `/_matrix/key/v2/server`. Every request Lattice sends is signed, and
  every request it receives must be.
- **The membership handshakes.** `make_join` / `send_join`, `make_leave` / `send_leave`,
  and `invite` with the stripped state that authorises it — so a user on Lattice can
  join a room on another server, leave it, be invited into one they have never seen,
  and decline that invitation, with both servers ending up agreeing.
- **Events, and the questions around them.** An arriving event is verified (author's
  signature, content hash, then the room's own rules) before it is kept, and answered
  per event so one bad event does not stall a batch. Soft-failed events are stored and
  relayed but hidden from clients, as the specification asks.
- **Graph reads**, so a peer can check what you send: `state_ids`,
  `state/{roomId}/{eventId}`, `event_auth/{roomId}/{eventId}`, `event/{eventId}`,
  `get_missing_events/{roomId}`, `backfill/{roomId}` and `timestamp_to_event/{roomId}`.
- **History decides what a peer is told.** An event served to another server arrives with
  its signature and hashes intact and, when `m.room.history_visibility` says that server
  was not entitled to it, with its content removed — the same redaction `/redact` and
  `/event` do. The reads a peer needs in order to *judge* an event (its state, its
  authority chain) are served whole: stripping a membership's content would make "was this
  user in the room then?" unanswerable, and the answer decides the stripping.
- **Gap filling both ways.** An event citing a parent Lattice was never sent is a hole in
  the room, so Lattice asks the sender for what lies between its own newest event and the
  one it was given, applies what comes back oldest-first, and does not re-ask a peer that
  answered nothing useful. An event built on an event this room refused is refused too,
  with the parent named in the per-event answer.
- **Delivery that survives a peer being down.** Outbound traffic goes through a queue,
  retried with the same transaction id and the same bytes, backing off from five
  seconds to ten minutes. `m.room.server_acl` is honoured in both directions.

Deferred deliberately: SRV lookups, acting as a client of somebody else's notary
server, and signing-key rotation.

---

## What works

- **Everyday client API**: accounts, sessions and refresh, devices, profile, account
  data, filters, whoami, logout.
- **Rooms**: create, aliases, public directory, invite / join / knock / leave / ban /
  kick / forget, send and read messages, state, redaction, spaces and `/hierarchy`,
  threads and relations, upgrades between room versions.
- **Sync**: long-polling `/sync` with unread counts, notifications, tags, bundled
  relations, and a real `/messages` and `/context`.
- **Encryption support without reading anything**: device keys, one-time and fallback
  keys, to-device `m.room.encrypted`, cross-signing (verified, never rewritten) and
  server-side room-key backup.
- **Media**: upload, download, thumbnails, and the async create → upload → commit flow
  Element uses.
- **Receipts, typing, push rules and pushers, search, read markers.**
- **Admin and abuse**: `/_synapse/admin/*` and `/_matrix/client/v1/admin/*` for the first
  account, plus `/report`, and rate limiting on the expensive writes.
- **Account management from a terminal**: `lattice add-user`, `remove-user` and `users`,
  which work while the server runs and are how a server with registration closed gains
  its first account.
- **Unimplemented endpoints answer `M_UNRECOGNIZED`**, so a client can name what is
  missing instead of guessing from a bare 404.

## What is not there yet

- **Room version 12.** Rooms with domainless IDs (`!abc` with no `:host`) are refused
  with `M_INCOMPATIBLE_ROOM_VERSION`, which names the version rather than pretending the
  room is gone. v12 is the next format step: it removes the domain from room IDs, gives
  `m.room.create` creators power over the power levels, and counts event limits in bytes.
- **Federation tails**: knock handshakes, and cross-server end-to-end encryption —
  device list updates, to-device messaging, signing-key relays. Application services,
  bridges, SSO / `m.login.token`, email recovery, TURN for calls.
- **Notarised key trust**: Lattice verifies a peer's own key document and re-signs it; it
  does not consult a notary such as `matrix.org`. A brand-new server whose DNS or TLS is
  half-broken therefore looks unreachable until it is not.
- **Scale**: one process, one SQLite file, in-process rate-limit buckets keyed on the
  peer address (so behind a proxy everyone shares one bucket until proxy support lands),
  and search is a `LIKE` scan rather than an index.

---

## Configuration

Every setting is a flag, and every flag is also an environment variable: `LATTICE_`
plus the flag name in capitals, dashes as underscores. `-server-name` is
`LATTICE_SERVER_NAME`, `-federation-advertise` is `LATTICE_FEDERATION_ADVERTISE`. The
rule is mechanical, and `tests/unit/configenv_test.go` pins it, because a name read
differently is a server running on a generated signing secret with its database in a
directory nobody backs up.

Precedence is what it has to be: the command line wins over the environment, and the
environment wins over the default. A variable set to an empty value counts as unset —
a compose file writes `LATTICE_SERVER_NAME=` for a key left blank, and an empty server
name is a broken start rather than a choice. `-version` is the one flag never read from
the environment: it is a question rather than a setting, and an orchestrator carrying a
`LATTICE_VERSION` variable for its own bookkeeping should not turn every container
start into a printout. The startup log names whatever did arrive from the environment.

The flags that belong to one invocation rather than to a deployment are the other
exception, and they are never read from the environment either: `-password`, `-admin`,
`-display-name` for `lattice add-user`, and `-yes`, `-limit`, `-all` for the other account
commands. A `LATTICE_PASSWORD` left in a container would otherwise be the password of
whichever account somebody adds next.

| Flag | Environment | Default | Meaning |
| --- | --- | --- | --- |
| `-addr` | `LATTICE_ADDR` | `:8008` | listen address |
| `-server-name` | `LATTICE_SERVER_NAME` | `localhost` | the server's identity; every user and room ID ends with it. Set once; no port |
| `-base-url` | `LATTICE_BASE_URL` | `https://<-server-name>` | URL published in `/.well-known/matrix/client` |
| `-db` | `LATTICE_DB` | `lattice.db` | SQLite file |
| `-jwt-secret` | `LATTICE_JWT_SECRET` | _(empty)_ | signs access tokens — **set it** |
| `-token-ttl` | `LATTICE_TOKEN_TTL` | `24h` | access token lifetime |
| `-allow-guests` | `LATTICE_ALLOW_GUESTS` | `true` | allow guest accounts |
| `-disable-registration` | `LATTICE_DISABLE_REGISTRATION` | `false` | close self-service registration |
| `-registration-secret` | `LATTICE_REGISTRATION_SECRET` | _(empty)_ | comma separated tokens; when set, registering needs one |
| `-media` | `LATTICE_MEDIA` | `true` | media endpoints |
| `-max-upload-size` | `LATTICE_MAX_UPLOAD_SIZE` | `10 MiB` | upload cap |
| `-media-reservation-window` | `LATTICE_MEDIA_RESERVATION_WINDOW` | `24h` | how long a created-but-unfilled media id survives |
| `-no-rate-limit` | `LATTICE_NO_RATE_LIMIT` | `false` | turn rate limiting off |
| `-rate-limit-write-burst` / `-rate-limit-write-rate` | `LATTICE_RATE_LIMIT_WRITE_BURST` / `…_RATE` | `100` / `30` | the expensive writes, per device |
| `-rate-limit-auth-burst` / `-rate-limit-auth-rate` | `LATTICE_RATE_LIMIT_AUTH_BURST` / `…_RATE` | `20` / `2` | login, register, refresh, per address |
| `-rate-limit-upload-burst` / `-rate-limit-upload-rate` | `LATTICE_RATE_LIMIT_UPLOAD_BURST` / `…_RATE` | `20` / `5` | uploads, per device |
| `-cors-origin` | `LATTICE_CORS_ORIGIN` | `*` | allowed CORS origin(s) |
| `-support-email` / `-support-matrix-id` | `LATTICE_SUPPORT_EMAIL` / `LATTICE_SUPPORT_MATRIX_ID` | _(empty)_ | published in `/.well-known/matrix/support` |
| `-maintenance-interval` | `LATTICE_MAINTENANCE_INTERVAL` | `service.MaintenancePeriod` | how often to prune; `0` disables |
| `-tls-cert` / `-tls-key` | `LATTICE_TLS_CERT` / `LATTICE_TLS_KEY` | _(empty)_ | serve TLS directly rather than through a proxy; a certificate is how a peer believes you |
| `-tls-addr` | `LATTICE_TLS_ADDR` | _(empty)_ | a second address served over TLS — the federation port next to a plain client port |
| `-ca-cert` | `LATTICE_CA_CERT` | _(empty)_ | comma separated PEM authorities to trust when reaching peers, for a deployment on a private CA |
| `-federation` | `LATTICE_FEDERATION` | `false` | serve the server-server API and publish this server's signing key |
| `-federation-advertise` | `LATTICE_FEDERATION_ADVERTISE` | _(empty)_ | `host:port` to publish under `/.well-known/matrix/server` |
| `-federation-peer` | `LATTICE_FEDERATION_PEER` | _(empty)_ | `server=URL` pairs pinning where a peer is reached (tunnels, tests) |
| `-federation-flush` | `LATTICE_FEDERATION_FLUSH` | `1s` | how often queued federation traffic is retried |
| `-version` | — | `false` | print the version and exit |

### In a container

The image is the static binary on Alpine, running as an ordinary user, with one
directory to keep. There is no entrypoint script in it: the variables above are the
whole configuration interface, so the binary is the entry point.

```sh
docker run -d --name lattice \
  -p 8008:8008 \
  -e LATTICE_SERVER_NAME=matrix.example.org \
  -e LATTICE_JWT_SECRET="$(openssl rand -hex 32)" \
  -v lattice-data:/data \
  ghcr.io/enimatek-nl/lattice
```

What the image decides, and what it leaves to you:

- **The image sets** `LATTICE_ADDR=:8008` and `LATTICE_DB=/data/lattice.db`, because
  both are facts about where the server lives inside the container.
- **You set** `LATTICE_SERVER_NAME` and `LATTICE_JWT_SECRET`. The image will not guess
  them: a homeserver that picked its own identity is a homeserver whose accounts
  nobody can use. `LATTICE_FEDERATION=true` turns federation on, exactly as the flag
  does.
- **Flags still work** after the image name, and outrank the variables, because the
  entry point is the binary: `docker run … ghcr.io/enimatek-nl/lattice -federation-flush 5s`.
- **One volume is the whole backup.** `/data` holds `lattice.db`, and media lives
  inside that file: stop the container, copy the file.
- **The container runs as uid 10001**, and the image owns `/data`. A *bind* mount
  brings its ownership from the host, so `chown 10001:10001 ./that-directory` first —
  otherwise the first thing the container says is `permission denied` opening the
  database. A named volume needs nothing.
- **Federation wants outbound TLS**, so the image ships a trust store. Inbound it is
  the same story as the binary: peers knock on port 8448 unless
  `LATTICE_FEDERATION_ADVERTISE` says otherwise, and TLS is your proxy's job —
  `LATTICE_TLS_CERT` and `LATTICE_TLS_KEY` are there if you would rather the container
  serve it itself.
- **The health check** asks `/_matrix/client/versions` on 8008: public, needs no
  account, served by the same handler as everything else. Start the container on a
  different address and replace the check.

The tags come from the git tag: `v0.1.0` publishes `0.1.0`, `0.1` and `latest`, for
`linux/amd64` and `linux/arm64`, because the machine a homeserver runs on is as often a
Raspberry Pi as a VPS. A release candidate publishes only its own version — `v0.2.0-rc1`
moves no tag that anyone would mistake for the current one. `.github/workflows/image.yml`
runs on a tag push and refuses a tag that is not the version the binary reports, so what
an image is called and what the server says about itself cannot drift apart. `make image`
builds the same Dockerfile locally, for the architecture it runs on.

---

## Development

```sh
make build          # bin/lattice
make test           # unit + compatibility suite
make race           # the same under -race
make vet lint fmt   # go vet, golangci-lint, gofmt -s
make run            # dev server on 127.0.0.1:8008
make image          # the container image a release publishes (Dockerfile)
make endpoints      # endpoint coverage table -> ENDPOINTS.md (generated, untracked)
make complement     # the upstream Matrix suite (needs Docker/Podman and a Complement checkout)
```

Tests never sit next to the code they test: `tests/compat` is HTTP against a running
server, `tests/federation` runs two Lattice servers in one process against each other —
because a claim about federation cannot be checked from inside one server, a join bug is
a disagreement between two databases — and `tests/unit` pins the pure rules in tables.

The upstream check is [Complement](https://github.com/matrix-org/complement):
`make complement-image` builds a container from the static binary and `make complement`
runs the suite against it. The current tallies, and what each remaining failure is
waiting for, are written up under _Complement_ in [TODO.md](TODO.md).

A release is a tag. Pushing `v0.2.0` runs `.github/workflows/image.yml`, which builds
the root `Dockerfile` for `linux/amd64` and `linux/arm64` and publishes `0.2.0`, `0.2`
and `latest` to `ghcr.io/enimatek-nl/lattice`; a pull request builds the same image and
discards it, so a Dockerfile that no longer compiles is found before a release needs it.
The job refuses a tag that is not the version in `internal/service/capabilities.go`, so
the version a release is named for and the version the server reports stay one edit
rather than two. `deploy/complement/Dockerfile` is a separate image, built for the test
suite rather than for anyone to run.

### Layout

```
cmd/server          entry point: flags, listeners, graceful shutdown - or an operator command
internal/cli        the operator commands: add-user, remove-user, users
internal/config     flags, and the LATTICE_* variables that set them
internal/api        HTTP handlers and the route table (routes.go)
internal/service    business logic, one file per feature area
internal/persist    GORM + gormlite (pure-Go SQLite) models and repositories
internal/matrix     identifiers, event types, error shapes, sync structures
internal/auth       tokens and middleware            internal/realtime  sync wake-ups
internal/power      power levels                     internal/push     push rules
internal/authcheck  a room's authorization rules     internal/stateres state resolution v2
internal/eventcrypto event hashes, ids, redaction, signatures
internal/federation signing, X-Matrix auth, transport, key caches, transactions
Dockerfile          the image a release publishes    deploy/complement  the image Complement runs
tests/compat  tests/federation  tests/unit
```

### House rules for contributors

Handlers stay thin — decode, one `service` call, write — and errors leave through
`a.writeError`. A new endpoint means a route in `internal/api/routes.go`, a case in
`tests/compat`, and a re-run of `make endpoints`. A new setting means one flag in
`cmd/server/main.go`: its `LATTICE_*` variable arrives with it, by the rule in
`internal/config`, and the configuration table in README.md gains its name. Short
lowercase package names, table-driven tests, and `context.Context` as the first
parameter of anything that does I/O.

---

## Where the plan lives

[TODO.md](TODO.md) is the one planning document: the idea and the way of working, what
is verified and done, and — under *Complement* — what each remaining failure is waiting
for. The endpoint index taken from the specification lives beside the tool that reads it
at `internal/coverage/SPECS.md`; `make endpoints` compares it with the route table and
writes `ENDPOINTS.md`, which is generated and not committed.

Lattice is under the [MIT licence](LICENSE).
