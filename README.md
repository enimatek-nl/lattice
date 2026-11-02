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
| **Client API** | 168 of the 234 indexed spec endpoints, plus 33 extra routes (`make endpoints`) |
| **Federation** | keys, signed requests, join / leave / invite, events, typing, receipts, device lists, state and graph reads — behind `-federation` |
| **Room versions** | 1–11 (11 is the default; room upgrades supported) |
| **Tests** | 117 client-API compatibility, 43 two-server federation, 65 unit |
| **Licence** | MIT |

---

## Try it

```sh
go build -o lattice ./cmd/server
./lattice -addr 127.0.0.1:8008 -server-name localhost -db lattice.db -jwt-secret dev
```

Point a client at `http://localhost:8008` — Element Web, Element Desktop, FluffyChat,
Hiligony, any of them. The first account you register is the administrator.

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
TLS, then get four settings right — most federation failures come from the first two:

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
  `get_missing_events/{roomId}`.
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
- **Unimplemented endpoints answer `M_UNRECOGNIZED`**, so a client can name what is
  missing instead of guessing from a bare 404.

## What is not there yet

- **Room version 12.** Rooms with domainless IDs (`!abc` with no `:host`) are refused
  with `M_INCOMPATIBLE_ROOM_VERSION`, which names the version rather than pretending the
  room is gone. v12 is the next format step: it removes the domain from room IDs, gives
  `m.room.create` creators power over the power levels, and counts event limits in bytes.
- **Federation tails**: knock handshakes, `backfill`, `timestamp_to_event`, history
  visibility redaction on what is served to peers, and asking a peer to fill a gap Lattice
  itself noticed. Application services, bridges, SSO / `m.login.token`, email recovery,
  TURN for calls.
- **Notarised key trust**: Lattice verifies a peer's own key document and re-signs it; it
  does not consult a notary such as `matrix.org`. A brand-new server whose DNS or TLS is
  half-broken therefore looks unreachable until it is not.
- **Scale**: one process, one SQLite file, in-process rate-limit buckets keyed on the
  peer address (so behind a proxy everyone shares one bucket until proxy support lands),
  and search is a `LIKE` scan rather than an index.

---

## Configuration

| Flag | Default | Meaning |
| --- | --- | --- |
| `-addr` | `:8008` | listen address |
| `-server-name` | `localhost` | the server's identity; every user and room ID ends with it. Set once; no port |
| `-base-url` | `https://<-server-name>` | URL published in `/.well-known/matrix/client` |
| `-db` | `lattice.db` | SQLite file |
| `-jwt-secret` | _(empty)_ | signs access tokens — **set it** |
| `-token-ttl` | `24h` | access token lifetime |
| `-allow-guests` | `true` | allow guest accounts |
| `-disable-registration` | `false` | close self-service registration |
| `-registration-secret` | _(empty)_ | comma separated tokens; when set, registering needs one |
| `-media` | `true` | media endpoints |
| `-max-upload-size` | `10 MiB` | upload cap |
| `-media-reservation-window` | `24h` | how long a created-but-unfilled media id survives |
| `-no-rate-limit` | `false` | turn rate limiting off |
| `-rate-limit-write-burst` / `-rate-limit-write-rate` | `100` / `30` | the expensive writes, per device |
| `-rate-limit-auth-burst` / `-rate-limit-auth-rate` | `20` / `2` | login, register, refresh, per address |
| `-rate-limit-upload-burst` / `-rate-limit-upload-rate` | `20` / `5` | uploads, per device |
| `-cors-origin` | `*` | allowed CORS origin(s) |
| `-support-email` / `-support-matrix-id` | _(empty)_ | published in `/.well-known/matrix/support` |
| `-maintenance-interval` | `service.MaintenancePeriod` | how often to prune; `0` disables |
| `-federation` | `false` | serve the server-server API and publish this server's signing key |
| `-federation-advertise` | _(empty)_ | `host:port` to publish under `/.well-known/matrix/server` |
| `-federation-peer` | _(empty)_ | `server=URL` pairs pinning where a peer is reached (tunnels, tests) |
| `-federation-flush` | `1s` | how often queued federation traffic is retried |
| `-version` | `false` | print the version and exit |

---

## Development

```sh
make build          # bin/lattice
make test           # unit + compatibility suite
make race           # the same under -race
make vet lint fmt   # go vet, golangci-lint, gofmt -s
make run            # dev server on 127.0.0.1:8008
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

### Layout

```
cmd/server          entry point: flags, listeners, graceful shutdown
internal/api        HTTP handlers and the route table (routes.go)
internal/service    business logic, one file per feature area
internal/persist    GORM + gormlite (pure-Go SQLite) models and repositories
internal/matrix     identifiers, event types, error shapes, sync structures
internal/auth       tokens and middleware            internal/realtime  sync wake-ups
internal/power      power levels                     internal/push     push rules
internal/authcheck  a room's authorization rules     internal/stateres state resolution v2
internal/eventcrypto event hashes, ids, redaction, signatures
internal/federation signing, X-Matrix auth, transport, key caches, transactions
tests/compat  tests/federation  tests/unit
```

### House rules for contributors

Handlers stay thin — decode, one `service` call, write — and errors leave through
`a.writeError`. A new endpoint means a route in `internal/api/routes.go`, a case in
`tests/compat`, and a re-run of `make endpoints`. Short lowercase package names,
table-driven tests, and `context.Context` as the first parameter of anything that does
I/O.

---

## Where the plan lives

[TODO.md](TODO.md) is the one planning document: the idea and the way of working, what
is verified and done, and — under *Complement* — what each remaining failure is waiting
for. The endpoint index taken from the specification lives beside the tool that reads it
at `internal/coverage/SPECS.md`; `make endpoints` compares it with the route table and
writes `ENDPOINTS.md`, which is generated and not committed.

Lattice is under the [MIT licence](LICENSE).
