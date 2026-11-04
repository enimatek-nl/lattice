# TODO / working state

Hand-off document for Lattice (minimal Matrix homeserver in Go) - the only
planning document in the repository. [README.md](README.md) is what the server
does today for a stranger; the endpoint index the specification asks for is
[internal/coverage/SPECS.md](internal/coverage/SPECS.md) and `make endpoints`
writes the generated implemented/missing table to `ENDPOINTS.md` (generated, not
committed). [docs/coverage.md](coverage.md) states what the server covers area by area,
and [docs/coverage-comparison.md](coverage-comparison.md) reads it against the coverage
document of the Rust reference server ([docs/spec-coverage.md](spec-coverage.md)). This file is
now a **release plan: work the steps in order, one at a time**. Steps 1-10 are
done, so the next one is **step 11**; federation is steps 6-12. The history that
used to fill this file lives in git; only facts a future session must not
re-derive were kept (below the plan).

## The idea, and the way of working

The goal has not changed since the first commit: a drop-in implementation of
https://spec.matrix.org/latest/ in Go on SQLite, small enough that one binary and one
file are a homeserver. The method is what makes it checkable: **implement an endpoint,
add its functional test, see whether it behaves, move on** - tests never sit next to the
code they test, so "which endpoint is already covered?" has an answer (`make endpoints`).
A holistic user-script test - account, room, message, upload - was added later and lives
in `tests/compat/user_script_test.go`. Phases (bare POC, everyday endpoints, the E2EE key
layer, federation) are complete or described by the steps below; the old PLAN.md is gone
because it said nothing these steps do not.

## Current state (verified 2026-11-04)

- A working homeserver with its **federation transport in production shape**:
  `make build vet lint` clean (`make lint` = golangci-lint, 0 issues),
  `go test ./...` green — **139** compatibility tests (~62 s here, ~5½ min
  with `-race` on this machine), **70** offline unit tests in `tests/unit`
  (milliseconds), and **84** two-server tests in `tests/federation` (~40 s,
  ~56 s with `-race`). Coverage: **168 of the 234** indexed endpoints, 33 extra
  routes (`make endpoints`). Steps 1–10 are closed: rate limiting, the
  client-API correctness tails, batched reads, offline unit tests, the
  federation signing/transport ground, the **event DAG** (events hashed,
  signed, identified by reference hash, stored with `prev_events`,
  `auth_events` and their exact signed JSON), **transactions** — inbound
  `PUT /send` with a replay-safe transaction log, EDU sinks for typing,
  receipts, to-device, device lists and cross-signing, an outbound per
  destination queue with retries, and `m.room.server_acl` enforced in both
  directions — and the **cross-server membership handshake**: `make_join`/`send_join`
  and `make_leave`/`send_leave` served and sent, the state a peer hands over persisted
  and usable, remote aliases resolved through `/query/directory`, and a room's decision
  about a user (ban, unban, invite, kick, leave) reaching the server that user lives on.
  Steps 9 to 13 are done and **step 14** wrapped the release: the two-server scenario
  table runs through the join handshake rather than through rows a test wrote, and both
  Complement packages have a recorded tally.
- Against [Complement](https://github.com/matrix-org/complement), the upstream
  Matrix suite, **40 of 107** client-API tests fail (`make complement`) and **51 of
  91** server-server tests fail — 40 green, up from 4 green before step 10 and 22 green
  before step 11. Green now: the membership handshakes and their guards, the graph and
  state reads a peer needs to judge what it is sent, gap filling both ways, profiles and
  the room directory, spaces and `/hierarchy`, OpenID exchange, device lists, key
  queries and claims, and `TestContentMediaV1`.
  See _Complement_ below for how they run, what a run can hide, and what each remaining
  failure is waiting for. A room's rules are read once, by one list, for what this server
  writes and what a peer sends (step 20). The transport itself is not among the reasons:
  knocking, restricted rooms and the space summary that authorises them are green (steps 17-19);
  what is left is room versions past v11, application services, presence, the federation
  `PUT /invite` endpoint, and redaction of an event the peer has never seen. The
  The federation
  media cases are green; see step 16.
- The three bugs the federation suite found were not federation bugs: access tokens died
  at every restart (the signing secret was per process, the sessions are in the
  database), the event stream counter was re-seeded from the events table alone (so a
  restarted server re-issued positions and clients holding a later token saw nothing),
  and a device list change made by one of a user's other devices never reached that
  user's own sync.
- Federation is live behind `-federation` (default off): a persisted ed25519
  signing key, `/_matrix/key/v2/*`, `/_matrix/federation/v1/version`,
  `/_matrix/federation/v1/user/devices/{userId}`,
  `PUT /_matrix/federation/v1/send/{txnId}`, `GET /_matrix/federation/v1/event/{eventID}`,
  `GET /_matrix/federation/v1|v2/make_join/{roomId}/{userId}`,
  `PUT /_matrix/federation/v1|v2/send_join/{roomId}/{eventId}`,
  `GET /_matrix/federation/v1/query/directory`,
  `GET /_matrix/federation/v1|v2/make_leave/{roomId}/{userId}`,
  `PUT /_matrix/federation/v1|v2/send_leave/{roomId}/{eventId}`,
  `PUT /_matrix/federation/v1/invite/{roomId}/{eventId}` and its v2 counterpart, which
  carry `invite_room_state` so a server that has never been in the room can still tell its
  user they have been invited,
  an outbound client that signs, fetches an auth chain it is missing, and retries a
  queue, and inbound middleware that refuses anything it cannot attribute to a peer.
  Still deferred on purpose: SRV lookups, acting as a
  *client* of someone else's notary, key rotation, and per-peer budgets for the
  volume routes that arrive with the read-side queries of step 11.
- What federation does **not** do yet, so a two-server test is not mistaken for a
  working join: no room can be joined across servers (step 10), so the rooms of every
  two-server test are shared by hand. What an arriving event does is no longer faked:
  it is judged — shape, signature, content hash, then the room's rules against its own
  auth chain and against this server's state — and the outcome decides what happens:
  accepted moves the state, the graph and the timeline; soft-failed is stored, relayed
  and hidden; refused is kept only as a receipt with its reason. Two Lattice servers
  already exchange messages, typing, receipts, room keys and device list updates for
  rooms that both are in.
- The E2EE trust layer is done end-to-end: device keys, cross-signing
  (verified server-side over canonical JSON), key signatures, room key
  backup. The Element "unable to set up keys" thread is **closed** — pinned
  by `TestElementKeySetupChain`, `TestKeySignaturesRequestShapes`,
  `TestCrossSigningPartialUpload`, `TestKeyUploadIdentityFieldNames`,
  `TestCrossSigningAuthShapes`. Do not re-open it; the only thing left there
  is clicking through a real Element (see _Open_ below).
- Second Element thread, closed 2026-11-01 (with its follow-up the same day): an
  Element Web account spammed
  `POST /keys/upload` (48 `signed_curve25519` keys each time, every sync) while
  messages from another user would not decrypt. Two server bugs, both fixed and
  pinned: `/keys/upload` **replaced** the one-time key pool instead of adding to
  it (so the client's count never reached its target of 100 and it topped up
  forever, destroying keys other devices were about to claim), and the stored key
  object was **normalised**, which dropped `signatures` — a claimed key could not
  be verified, so no Olm session opened and the room key never arrived. See the
  gotcha under _Device one-time keys_. Long-polling was checked and is fine: an
  idle sync waits out its timeout (`TestSyncIdleWaitsForTheTimeout`). The follow-up
  was the next request in that same chain: `PUT /sendToDevice/m.room.encrypted`
  refused as a room event, so the Megolm key never reached the other device — see
  the _PUT /sendToDevice_ entry under _Behaviour already handled_.
- Federation decision (2026-11-01): **in scope for the first release**,
  gated behind a `-federation` boolean flag (default off → server-server routes
  unmounted, `M_UNRECOGNIZED` on those paths). The work is the steps 6–14 of this
  plan. Target: spec v1.19 server-server API. One part of it is deliberately **not**
  behind the flag since step 7: events are hashed, signed and given derived ids
  whether or not federation is on, because those are properties of the stored event
  and a room created unsigned can never be federated afterwards.

## The gaps, ranked (measured 2026-11-05)

Two coverage documents sit in this directory: [coverage.md](coverage.md) is ours, area by area,
with an endpoint tally from `cmd/endpoints` (**180 of 234** indexed routes);
[spec-coverage.md](spec-coverage.md) is the same shape of document for a Rust homeserver, and
[coverage-comparison.md](coverage-comparison.md) reads them against each other. Nine things come
out of that reading - things that server does, and this one does not. In order of what each
unlocks:

1. **Room version 12** — room ids as hashes, room creators with infinite power, state resolution
   v2.1, stripped state as full events. **Nineteen** Complement tests carry those names, and v12
   is what the specification now recommends as the default. → **step 21**.
2. **Application services** — no `/_matrix/app/v1/*` at all, no registration files or
   namespaces, no `m.login.application_service`. No bridge of any kind can attach, and a bot
   cannot hold an identity on this server.
3. **Simplified Sliding Sync (MSC4186)** — `POST /_matrix/client/v4/sync` with lists, ranges,
   filters and subscriptions. **Done 2026-11-04** (step 22): both paths are served, and the holes
   left are listed there.
4. **Push delivery** — rules, pushers and unread counts are here; nothing is POSTed to a push
   gateway (`POST /_matrix/push/v1/notify` as a *client* of one, `event_id_only`, rejected
   pushkeys). Mobile push does not work, and the missing half is one outbound request loop.
5. **The federation paths peers actually ask for** — `PUT /v1|v2/invite/{roomId}/{eventId}` (so:
   no invite rejection or rescinding over federation), the specified
   `GET /v1|v2/state/{roomId}?event_id=` (this server registers the event id as a **path**
   segment, so a peer asking the specified way is answered with nothing), federation
   `POST /v1/publicRooms` (both public-room routes were registered at `/v3`), and the
   rejected-event bookkeeping that four Complement tests measure.
6. **Authentication that is not a password** — `/login/sso/redirect`, `m.login.token` issued by
   an identity provider, MSC2965 discovery, `POST /v1/login/get_token`. `/auth_metadata` answers
   a reasoned 404 today.
7. **Sync and timeline details a client feels** — `lazy_load` accepted and state served in full,
   `m.ignored_user_list` not applied to a timeline, no `state_after`, top-level `presence` and
   `org.matrix.msc3814.e2ee` sections declared and never filled, no `DELETE` on account data, no
   `m.fully_marked`, `/relations` without `recurse`, and the `timestamp_to_event` walk returning
   an event *after* the asked moment (`TestJumpToDateEndpoint`).
8. **An admin surface** — the reference has a Synapse-compatible subset (users list/get/create/
   deactivate/reset, rooms list/details/delete/force-join, purge, server notices). This server
   has four admin routes, `/_synapse/admin/v2/users/{id}`, and the CLI.
9. **The MSC surface and the quality of life around it** — delayed events (MSC4140), thread
   subscriptions (MSC4306), invite filtering (MSC4155), poll push rules (MSC3930), relation
   filters in `/messages` (MSC3874), account-data deletion (MSC3391); a full-text search index
   instead of substring matching; `m.room.retention`; fuzz targets; a load harness.

## Release plan — pick one step at a time, top to bottom

### 1. Housekeeping — done (2026-11-01)

- [x] Deleted the four verifiably dead functions: `api.rawQueryObject`,
      `service.sessionTTL`, `service.userSummary`,
      `service.roomMembersForSync`. Kept on purpose: `UserSummary` (the user
      directory returns it), `displayName`/`avatarURL` (live callers) and
      `StateRepo.MemberEvents`, now without callers — it stays as repo
      contract because federation state blocks want it.
- [x] `Makefile` with `help build test race vet fmt lint endpoints run swagger
      clean` (`help` is the default goal; `ADDR`/`SERVER_NAME`/`DB`/
      `JWT_SECRET`/`SPEC`/`ENDPOINTS` are overridable variables). `make lint`
      runs golangci-lint 2.15.0 and reports **0 issues** — its first run
      reported seven, all fixed here: a dead store in `service.newSession`
      (the device row was never read when the device already existed),
      `WriteString(fmt.Sprintf)` → `fmt.Fprintf` and two append spreads in
      `cmd/endpoints`, an append spread in `api.handleMembers`, and the
      unchecked `defer resp.Body.Close()` in the test harness.
- [x] `make swagger` works and is honest about being generated: swag v1.16.4
      is fetched with `go run` (not a module dependency), general API info
      annotations live on the `package api` comment, `createRoomBody` became a
      real type (it was an inline anonymous struct the annotation named), and
      output is `swagger.json`/`swagger.yaml` in the gitignored
      `docs/swagger/`. JSON/YAML only on purpose: swag's generated `docs.go`
      imports the swaggo runtime and would break `go build ./...`. 78 paths
      come out; there is no Swagger UI route and none is planned yet.
- [x] `make endpoints` re-run: still **149/234** endpoints, 26 extra routes,
      `ENDPOINTS.md` unchanged.

Standing duty for the steps below: `make endpoints` after each one (also in
the house rules).

### 2. Rate limiting — done (2026-11-01)

- [x] `internal/api/ratelimit.go`: lazily refilled token buckets in three
      kinds — `limitWrite` (`/send`, `/createRoom`, both `/join` forms,
      `/invite`), `limitUpload` (`POST`/`PUT /_matrix/media/v3/upload`) and
      `limitAuth` (`POST /login`, `/refresh`, `/register`). Registration
      helpers: `a.limited(pattern, kind, handler)` (auth first, then the
      bucket) and `a.anonymousLimited` for the endpoints without a token.
      Subject = `auth.GetDeviceID`, or the peer address for the auth bucket;
      `clientAddress` strips the port and reads `RemoteAddress` because there
      is no proxy support to trust a header.
- [x] The 429 body is `matrix.ErrLimitExceeded(retryAfterMS)` — already written,
      never called until now — so the answer is
      `{errcode: M_LIMIT_EXCEEDED, error, retry_after_ms}`.
- [x] Configuration: `api.Config.RateLimits` + `api.DefaultRateLimits()`
      (write 100 burst / 30 per second, auth 20 / 2, upload 20 / 5 — generous
      on purpose: a single-user server never notices them) and the flags
      `-no-rate-limit`, `-rate-limit-{write,auth,upload}-burst`,
      `-rate-limit-{write,auth,upload}-rate`. A zero value falls back to the
      default; `-no-rate-limit` is the off switch.
- [x] Lifecycle: no goroutine. Buckets are pruned on the request path, at most
      once a minute, dropping anything idle for 30 minutes; rejections are
      logged once per bucket plus every hundredth.
- [x] Compat tests `tests/compat/ratelimit_test.go`: 429 shape with a positive
      `retry_after_ms`, the window recovers after the stated wait, buckets are
      per device (a second login of the same user is unaffected), login/register
      share one per-address budget and even the correct password waits,
      uploads are a separate budget, and `Disabled` answers everything. The
      suite's defaults never trip the limiter: 100 compat tests green.

### 3. Client-API correctness tails — done (2026-11-01)

- [x] `GET /notifications` ordering decided and pinned: stored in arrival
      order, paged forward from a row-id token, so **oldest first** and `from`
      always continues downwards. `TestNotificationsOrderAndRead` pins it, and
      adds the mandatory `read` flag (an m.read or m.read.private receipt, or
      the fully read marker, reached the notification) and the `only=highlight`
      filter, which the specification defines and Lattice did not implement.
      `only` with another value is a 400.
- [x] `/relations` and redaction: the TODO said "hide relations whose target
      was redacted" — that is **upside down**. MSC2675 (the merged aggregations
      MSC; "Redactions" under "Event relationships", spec v1.19) reads:
      "Redacted relations should not be taken into consideration in bundled
      aggregations, nor should they be returned from `/relations`. Requesting
      `/relations` on a redacted event should still return any existing
      relation events." So the *relation* is hidden and the *target* is not:
      `EventRepo.Relations`, `Threads` and `ThreadsFor` all filter
      `redacted = false`, which also takes redacted replies out of thread
      counts and `unsigned.m.thread`. Pinned by `TestRelationRedactionRules`
      and `TestThreadPaginationAndRedaction`.
- [x] `/relations` honours `dir` and `to` (`persist.RelationQuery` carries the
      window). **The default direction changed**: relations.yaml (v1.19) orders
      the chunk "topologically most-recent first" and `dir` defaults to `b`, so
      a call without tokens now answers newest first — `TestRelations` used to
      pin oldest-first and was updated to the spec.
- [x] `/threads` honours `dir` and `to`; both bounds are exclusive, ordering is
      by the thread's most recent activity, and `relationship=latest` keeps the
      newest thread whichever way the listing walks. `parseDirection` is now the
      single place that reads `dir` (also used by `/messages` and
      `timestamp_to_event`, whose default stays `f`).
- [x] `GET /rooms/{roomId}/event/{eventId}` and the centre event of `/context`
      carry `unsigned.m.thread` (MSC2675 lists both among the bundling
      endpoints); `annotateThreads` takes a slice, so both feed it a one-element
      one.
- [x] `m.heroes` now sorts by recent activity: `EventRepo.LatestActivity(roomID)`
      (max stream position per sender) feeds `pickHeroes`, ties break on the
      membership timestamp then the user id, and the sync summary and
      `GET /summary` share the picker. The activity query is skipped for rooms
      with six members or fewer (all of them are heroes anyway), which keeps it
      off the DM hot path. Pinned by `TestHeroesOrderByActivity`.

### 4. Batch reads — done (2026-11-01)

- [x] `EventRepo.GetByIDs(ids) []map[string]*Event` and the hydration loops are
      gone: `syncstate.stateEvents`, `syncstate.stateDelta`,
      `hierarchy.hierarchyStateEvents`, `search.searchStateEvents`,
      `markers.Notifications` and `membership.Members` each render their rows
      from one event read. `stateEventsWire(rows, keep)` is the shared renderer
      (sync state block, hierarchy, search) — filter first, then one read, then
      render in row order.
- [x] `strippedState` (the invite state of a sync) listed its types one query at
      a time and then read an event per row: now one `StateRepo.List` for all
      eight types plus the members, grouped by type so the block keeps its
      order, and `strippedEvents` renders it in one event read. The fallback for
      a state row whose event is gone is unchanged.
- [x] Profiles: `ProfileRepo.FieldsFor(userIDs, keys...)` and
      `service.profileSummaries` replace the per-member `displayName`/`avatarURL`
      calls in `Members`, `JoinedMembers`, `SearchUserDirectory` and
      `notifyParticipants` (a message to a 100-member room was 200 profile
      reads). The localpart fallback of `displayName` is preserved, so a user
      with no profile row still gets a name. `SetProfileKey` reads the one
      profile once instead of once per joined room.
- [x] Threads: `EventRepo.ThreadStats(roomID, userID)` aggregates
      `COUNT(*)`, `MAX(stream_pos)` and "how many replies did the requester send"
      per root in one grouped query — so `include=mine` no longer needs every
      reply loaded — and `EventRepo.ByPositions(roomID, positions)` fetches the
      latest reply of every listed thread in one read. Backing it, `events` now
      carries `idx_event_room_rel (room_id, rel_type, relates_to)` next to the
      existing `idx_event_relates`. `/threads` cost grows with the number of
      threads, not the number of replies. An empty listing now answers
      `"chunk": []` rather than `null` (the slice was nil), pinned in
      `TestThreadPaginationAndRedaction`.
- [x] Still one-query-per-room by design: the per-room state and timeline reads
      in sync. That is inherent to a per-room sync response, and federation will
      want those per-room reads anyway.

### 5. Unit tests in `tests/unit/` — done (2026-11-01)

- [x] `tests/unit/` (package `unit`, table-driven, no server, no database, no
      network): **31** tests over identifiers (`Alias` validity,
      `ValidLocalpart`, `UserMxid`, `DomainOf`/`LocalpartOf`, the shape of minted
      IDs), error mapping (constructor → errcode + status table, JSON body shape
      including the User-Interactive Auth `Extra` keys and the rule that an
      internal cause never reaches the client, `matrix.ErrAs` conversion,
      `auth.WriteError` through `httptest`), stream tokens, power levels, push
      rules, and the endpoint matcher.
- [x] Three pure packages were cut out of the service layer and the coverage
      command so they can be tested offline — this is the offline test bed step 7
      asked for: `internal/push` (rule precedence, `Matches`, glob / word /
      count matching, action tweaks — was `service/pushnotify.go`),
      `internal/power` (`Defaults`, `ApplyDefaults`, `Level`, `EventRequired`,
      `ActionRequired`, `ChangeRequired` — was `service/power.go` and
      `stateguards.go`), and `internal/coverage` (the internal/coverage/SPECS.md matcher and report
      — was `cmd/endpoints/main.go`, which is now a thin `main`). The service
      keeps the storage plumbing and aliases the constants
      (`PushRuleKinds = push.Kinds`, `PowerAdmin = power.Admin`), and
      `ENDPOINTS.md` came out byte-identical after the move.
- [x] Three behaviours the tables forced into the open, decided rather than left
      fuzzy:
      - **API revisions are not interchangeable in the coverage matcher**: a v1
        index entry is not answered by a v3 route — `VersionEquivalent` compares
        the revision for equality. Its comment claimed the opposite; the comment
        now says what the code does. Making revisions interchangeable would
        report endpoints as implemented that the older client cannot reach, so
        this stays strict.
      - A `room_member_count` `is` expression tolerates padding around the
        operator (`" >= 3 "`), like Synapse's regex does; `CompareCount` trims
        before looking for the operator.
      - A display-name mention has to appear as it is written: a multi-word
        display name matches as a phrase, not one word at a time.

### 6. Federation I — identity, keys, signed transport — done (2026-11-02)

- [x] `-federation` in `cmd/server` (+ `api.Config.Federation`). Off is the
      default, and off is an **absence** rather than a guard:
      `registerRoutes` does not call `registerFederationRoutes` at all, so
      those paths answer `M_UNRECOGNIZED` and no key row is written —
      `service.WithFederation(true)` is what makes `service.New` mint the
      signing key. Pinned by `TestFederationIsOffUnlessItIsAskedFor`, which
      also checks the ordinary client path still works; the other direction is
      `TestTheClientAPIIsUnchangedWithFederationOn`.
- [x] `persist.ServerKey` (`internal/persist/serverkeys.go`) and
      `ServerKeyRepo` (`Current`, `List`, `Store` with
      `OnConflict{key_id, DoNothing}`): one ed25519 pair per server, id
      `ed25519:<version>`, the private seed stored beside it. Minted on first
      use and cached behind a mutex in `Service.SigningKey` — two racing first
      requests must not each mint a pair, or peers see two signers for one
      name. `TestSigningKeySurvivesARestart` reopens the database file and
      demands the same key id back. Rotation stays deferred.
- [x] `internal/federation`: `keys.go` (`KeyPair`, `KeyDocument`,
      `VerifyKeyDocument` — server_name must match the name that was asked
      for, key ids must be `ed25519:<version>` with a spec-shaped version, the
      document must be signed by a key it publishes, `valid_until_ts` must be
      ahead and is capped at 7 days by `KeySet.Expires`), `authorization.go`
      (sign/parse/verify the X-Matrix header, quoted **and** bare parameters),
      `transport.go` (peer client + caches), `middleware.go` (inbound
      verification, and the peer's name on the context via `federation.Peer`).
- [x] Endpoints: `GET /_matrix/key/v2/server`; `GET
      /_matrix/key/v2/server/{keyID}` (extra route — the whole document for a
      key id we publish, `M_NOT_FOUND` for one we do not); `POST
      /_matrix/key/v2/query` and `GET /_matrix/key/v2/query/{serverName}`
      (fetch → verify → re-sign with our key; a peer that does not answer is a
      `failures` entry, never an error, and the array stays `[]` not `null`);
      `GET /_matrix/federation/v1/version` (unauthenticated on purpose — an
      operator comparing two servers must be able to ask); and `GET
      /_matrix/federation/v1/user/devices/{userId}`, step 11's read side
      pulled forward because it is the smallest signed-and-authenticated GET
      available. Coverage is now **154 of 234** endpoints, 27 extra routes.
- [x] Name resolution: a `PeerBaseURLs` pin first (tests and tunnels;
      `-federation-peer "name=http://127.0.0.1:PORT"`), then
      `/.well-known/matrix/server`'s `m.server` (24 h cache, 1 h for a
      failure), then the name itself with `:8448` when it names no port. SRV
      is still skipped as planned. `federation.ServerNameParts` does the split
      — bracketed IPv6 literals are the trap.
- [x] Verify-key cache: an entry lives until the document's own expiry capped
      at 7 days. An unknown key id forces one refetch (a peer that rotated
      must not be locked out forever) but never more than one per 30 s
      (`keyRefetchGap`) — both forcing reasons are peer-provocable, so an
      attacker cannot turn bad requests into outbound traffic. A peer that is
      briefly unreachable does **not** invalidate keys still inside their
      window; pinned by the last block of `TestKeyQueryNotarisesAPeersKeys`.
- [x] Tests: `tests/federation/` is the two-server suite (9 tests). Its
      `lazyHandler` exists because peer URLs are part of `api.Config`, so both
      listeners must exist before either app is built. Requests are pinned to
      loopback URLs — two servers on one machine cannot present certificates the
      other trusts, and the pin is the same mechanism an operator names on the
      command line, so the code under test is the production path.
      `tests/unit/federation_test.go` adds 8 offline tables for the signature,
      canonical-number and server-name grammar rules: a signature accepted when
      it should not be leaves no trace in either server's behaviour, so it has
      to be pinned where it is decided.
- Gotchas to know before step 7:
  - The signed uri is the **raw request target** (`r.RequestURI`). The
    transport signs `path + "?" + url.Values.Encode()` and puts exactly that
    string into the URL it sends, so the target that is verified is the target
    that was signed. Any re-encode on either side breaks the signature.
  - A GET signs an empty object as `content`; `contentToVerify` treats an
    absent body as that same empty object rather than refusing it.
  - `destination=` may legitimately be **absent** (older peers). The rule is
    "present ⇒ must name us", and the query `origin=` on a GET must agree with
    the signed origin when both are there.
  - Peer answers are decoded with `matrix.DecodeCanonical` (`json.Number`), and
    `matrix.CanonicalJSON` gained exact `int`/`int64`/`uint64`/`json.Number`
    and float handling. A fractional float used to be **refused**, which would
    have made a peer event carrying `1.5` unverifiable; whole floats still go
    back as integers so a timestamp does not gain a `.0`.
  - `matrix.Object` is an **alias** of `map[string]any`: a type switch must not
    carry both `matrix.Object` and `map[string]any` cases (it will not
    compile), and a `.(matrix.Object)` assertion on decoded JSON does succeed.
  - Peers are dialed over **https** unless pinned. A live two-server test on
    one machine needs `-federation-peer` with `http://` URLs.
  - Key queries are charged to the per-address `auth` bucket because they dial
    out on a stranger's behalf; publishing our own key is not charged, since
    peers fetch it constantly and the work is local.

### 7. Federation II — the event DAG — done (2026-11-02)

- [x] Storage: `prev_events`, `auth_events` and the **exact signed PDU JSON**
      (`persist.Event.JSON`, canonical-encoded), plus `SoftFailed` and
      `RejectedReason` for the steps that need them, and a `ForwardExtremity`
      table — one row per leaf of a room's graph, with depth and timestamp so
      leaves can be ordered the way the specification asks.
- [x] `AppendEvent` fills `prev_events` (the stored leaves, deepest first, capped
      at 20 and logged when a room exceeds the cap) and `auth_events` per the
      spec's selection: create, power_levels, sender member, target member,
      join_rules for join/invite/knock, `m.room.third_party_invite` by token,
      and the `join_authorised_via_users_server` member.
- [x] Event ids are reference-hash based: `$` + unpadded base64 for room version
      3, `$` + URL-safe unpadded base64 for 4 and later, invented ids only for
      versions 1–2 (which also carry `origin`, which v3+ does not).
- [x] Content hash, the two redaction algorithms, and hash-and-sign in the spec's
      order, in a pure package `internal/eventcrypto`; outgoing events carry
      `hashes` + `signatures` from the signing key **before** persisting.
- [x] The client is served the **stored** event (`matrix.Event.MarshalJSON`), with
      `event_id` and this server's `unsigned` properties added.
- [x] Pinned in `tests/unit/eventcrypto_test.go` (spec known-answer hashes and
      the redaction key sets), `tests/federation/graph_test.go` (a message trail
      whose ids, prev/auth events, depths, hashes and signatures are checked
      against the server's own published key), and
      `TestALegacyRoomGetsALeaf` in `tests/compat/maintenance_test.go` (a
      database from an older Lattice: its rooms get a leaf at open time, and the
      next event in one attaches to it and is authorised from the state that old
      server recorded).
- [ ] State resolution **v2** is *not* here: it is a pure function over loaded
      state and belongs with step 9/10, where conflicts from two servers exist
      to resolve. Nothing local has forked yet.

### Findings worth not re-deriving (step 7)

- **A room version 11 PDU has no `event_id` and no `origin`.** The v11 table lists
  `auth_events`, `content`, `depth`, `hashes`, `origin_server_ts`, `prev_events`,
  `room_id`, `sender`, `signatures`, `type` as required. `origin` belongs to the
  pre-v3 event. Lattice builds the PDU that way and adds `event_id` only on the way
  to a client.
- **The id is computed after the hash and after signing.** The reference hash covers
  the *redacted* copy *including* the content hash. An id computed before the hash was
  in the event, or a signature made before the hash was added, yields an event every
  other server refuses. `eventcrypto.HashAndSign` follows that order, with a test.
- **A receiver must strip `event_id` before recomputing anything** in room versions 3
  and later: `eventcrypto.asPublished` does it for the content hash, the reference hash
  and the signature check. Without it every received event looks forged, because the
  copy in hand has an id the sender never hashed.
- **Clients have to be served the stored event.** Serving the reconstructed struct
  passed every pre-existing test and failed the new ones: the fields matched, the bytes
  did not, and a hash is about bytes. The stored PDU's `origin_server_ts` and the
  column's must also come from **one** read of the clock — two `NowMS()` calls in the
  append path produced an event whose served copy was a millisecond newer than its own
  signature.
- **A redacted event is served from its columns, never from its stored copy**, which
  still holds the content the redaction destroyed. `persist.Event.Matrix()` leaves
  `Stored` unset when the row is redacted.
- **Room versions 1 and 2 still work and are not silently upgraded**: invented
  `event_id`, `origin` present, and the hash covers both.
- **The signing key is no longer gated on `-federation`.** It used to be, which would
  have left a Lattice started without the flag holding rooms it could never federate —
  the hash and signature are part of what those events are and cannot be retrofitted.
  The flag now only decides whether the server-server routes are mounted; with it off
  the cost is one row in the database.
- **Old rooms get one leaf each** (their newest event) seeded at open time, like the
  other idempotent repairs: their events never recorded `prev_events`, so there is no
  graph to recover, and naming the newest event is the only honest answer.
- **`prev_events`/`auth_events` are decided once.** They are stored, not recomputed: in
  v3+ the id is a hash over them, so recomputing against a graph that moved on describes
  a different event. Reads that need the graph (step 11's `/event_auth`, `/backfill`)
  must read the stored arrays.

### 8. Federation III — transactions and EDUs — done (2026-11-02)

- [x] Inbound `PUT /_matrix/federation/v1/send/{txnId}`
      (`internal/service/fedrecv.go`, route in `internal/api/federation.go`).
      `persist.InboundTxn` keys `(origin, txn_id)` → the stored answer, so a
      retry is answered from the row and never re-executed; a replay is logged
      and reported by a header. Caps enforced before any work:
      **50 PDUs / 100 EDUs** per transaction, **65536 bytes** per PDU, txn id
      ≤ 255 characters, body ≤ 10 MiB.
- [x] PDU intake answers **per event** (`{"pdus": {"$id": {}}}`). Until step 9
      judges an event, intake verifies the *transaction* (signed, origin matches
      the authenticated peer, a peer speaks only for its own users' EDUs) and
      keeps the event byte-for-byte in `persist.ReceivedPDU` with its derived
      event id, origin and room version.
- [x] EDU sinks: `m.typing` → typing rows + hub, `m.receipt` → receipts (the
      sender's `data.ts` is kept — a receipt says when a person read something,
      not when a server relayed it), `m.direct_to_device` → to-device store,
      `m.device_list_update` → `RemoteDevice` cache with gap detection
      (`prev_id` vs the recorded stream position → `DeviceStream.GapSeen`, which
      step 13 turns into a re-query), `m.signing_key_update` → cross-signing
      store plus a device list change; `m.presence` accepted and ignored
      (presence stays local); an unknown EDU type is logged and ignored, per spec.
- [x] Outbound (`internal/service/fedsend.go`): `persist.OutboxItem` +
      `persist.OutboundTxn`. Producers queue work instead of sending inline —
      events (`AppendEvent`), typing, receipts, to-device (including `*`
      device fan-out on the receiving side), device list and cross-signing
      changes. One destination at a time has **one outstanding transaction**
      (`sync.Mutex` per destination in-process + the `OutboundTxn` row across
      restarts); a retry re-sends the **same bytes under the same txn id**,
      which is what lets the receiver's transaction log do its job. Exponential
      backoff 5 s → 10 min with a random slice subtracted (servers that lose the
      same peer come back in step without it). Destinations are worked on
      concurrently (`sync.WaitGroup`; no new dependency), `StartFederationOutbox`
      is the loop (`-federation-flush`, default 1 s), `FlushFederationOutbox` is
      the on-the-spot version.
- [x] `m.room.server_acl`: `federation.ServerAllowed` (`internal/federation/acl.go`)
      implements the spec's order — no ACL allows, IP literal denied when
      `allow_ip_literals` is false, then `deny`, then `allow` (an empty allow
      list allows); globs are `*`/`?` only, case-insensitive, port stripped.
      Enforced **both ways**: `destinationsOfRoom` filters the queue, and
      `queuePDU` drops events from a server the room excludes. A refused
      inbound event is answered as accepted — a refusal is retried, and a retry
      is how an exclusion turns into a flood. The ACL event itself still travels
      to excluded servers, because a ban the banned server never hears about is
      not a ban.
- [x] Tests: `tests/unit/transaction_test.go` (envelope, caps, results,
      `NewTxnID`), `tests/unit/acl_test.go` (21 ACL cases),
      `tests/federation/transaction_test.go` (inbound: signed transport, replay
      idempotence, EDU sinks, forged users refused, oversized refused, unknown
      EDU ignored, PDUs queued and answered, device list gaps),
      `tests/federation/outbox_test.go` (outbound: room keys cross the wire and
      reach the device, typing/receipts travel with the sender's timestamp, PDUs
      arrive named identically on both sides, retry keeps the txn id and lands
      when the peer returns, the loop delivers without help, 130 items arrive as
      100 + 30, ACLs bite both ways, unknown rooms refuse messages but keep
      invites).
- [x] Client-API correctness found on the way and fixed: read receipts are
      room-wide in `/sync` (the old user-only read hid a receipt from the person
      the message was for — pinned by `TestReceiptsReachTheOtherSide`), and
      receipt events no longer replace typing in the same ephemeral block.
- [x] `make endpoints`: **155/234** (was 154) — `PUT
      /_matrix/federation/v1/send/{txnId}`. 27 extra routes unchanged.

### Findings worth not re-deriving (step 8)

- **A per-PDU error is not a transport failure.** The peer understood the
  transaction and said what it did with each event. Retrying such a transaction
  gets the same answer forever, so `attemptTransaction` logs the rejections
  (`PDUResults.Rejections`) and acknowledges. Only a signature, connection or
  decode failure puts a transaction back on the retry clock.
- **A retry keeps its id and its bytes.** `OutboxRepo.Retry` updates the
  `OutboundTxn` row in place (tries, next try, last error) and leaves the row
  outstanding; only `Acknowledge` deletes it. `DueDestinations` therefore unions
  "has unpacked items due" with "has an outstanding transaction due" — a
  destination whose only work is a failed transaction is still due, and a
  version of `Retry` that un-packed the items made every attempt a new
  transaction id (found by `TestTheOutboxRetriesTheSameTransaction`).
- **`origin` in the transaction body is this server's name**, not the
  destination's. The destination is the value at hand when the queue row is
  written, which is how this bug happened; the receiver compares the body's
  origin with the origin the signature came from and refuses a mismatch.
- **Rooms the receiver is not in.** A non-membership PDU for an unknown room is
  refused per event (`this server is not in the room the event belongs to`) —
  without the room version its id cannot be derived at all, since the id is the
  hash. A `m.room.member` event naming one of *this* server's users is taken in:
  an invite is the only way a server ever learns of a room. Those rows record
  the room version their id was named under (`DefaultRoomVersion`, i.e. the
  version this server creates rooms with). Divergence on purpose: room version 3
  spells event ids differently, so an invite into a v3 room is named wrong until
  the join replaces the guess — v3 rooms are a decade old.
- **`matrix.CanonicalJSON` does not encode typed maps.** `map[string]map[string]any`
  fails with "cannot canonically encode". Build nested `matrix.Object` instead of
  nested Go maps, or the EDU is silently never queued (found by
  `TestAToDeviceMessageReachesARemoteDevice`).
- **A `*` to-device device id is expanded by the receiver**, from the devices it
  knows; the sender has no visibility into a remote user's devices. The local
  send-to-device store keys rows by device id and sync serves only that device's
  rows, so storing `*` would make the message undeliverable.
- **The EDU cap is a packing target.** 100 EDUs is the maximum a receiver must
  accept, so a backlog is delivered 100 at a time (PDUs: 50). A test that expects
  50 EDUs per transaction is testing the wrong number.
- **`Event.Content` holds the content object**, not an envelope: unmarshalling
  the ACL column into `struct{ Content matrix.Object }` silently produced an
  empty ACL and let everything through (found by `TestARoomCanExcludeAServer`).
- **Typing has no identity.** A typing EDU is queued with an empty edge, i.e. no
  de-duplication: "still typing" repeated is the same statement, and "stopped
  typing" has to be able to arrive again. Receipts are keyed by
  `event_id[:thread]`, to-device by the client's transaction id, device list
  updates by `device:stream_id`.
- **`m.device_list_update` carries `stream_id` from the same counter as the
  device-list sync position** (`DeviceListChange.StreamPos`, echoed by
  `FederatedDevices`), so a peer's view and this server's own `changed` list
  cannot disagree.
- **A signed document has to be served as the bytes its signature was made
  over.** `GET /_matrix/key/v2/server` built a correct document and re-verified it
  in-process, and Complement still rejected it: the standard library's JSON encoder
  appends a newline to every body it writes, and a peer that checks the signature
  against the bytes it received counts that byte as part of what was signed. The
  document is now published by `federation.KeyDocumentBody` in canonical form and
  written by `writeRawJSON`, which no response body goes through with whitespace
  this server added. `tests/unit/keydocument_test.go` pins it: the served bytes are
  their own canonical form, and the signature verifies over everything except
  itself.

### The loop steps 9 to 13 are driven by

The client API was built against Complement's `tests/csapi`: pick a red test, name
the reason it is red, write the Lattice test that reproduces that reason, fix,
re-run. Steps 9 to 13 do the same against `COMPLEMENT_PKG=./tests` — 91 tests, 87
red (the Complement section has the command, the harness facts and the baseline).
**Each step names the tests it is expected to turn green**, so "federation got
better" is a measurement rather than a feeling. Iterate with `COMPLEMENT_RUN`: a
full server-server run is around 40 minutes, because most of those tests start two
containers and wait for them.

One ordering rule falls out of that baseline. The failures are the *peer* answering
`M_UNRECOGNIZED` to `make_join` and friends: signed requests reach the other
container over TLS on 8448, and come back with "no such route". So the handshake
endpoints, not the transport, are what block everything — and the read-side graph
endpoints (`/event_auth`, `/state_ids`, `/get_missing_events`, `/backfill`) come
**after** the first join rather than before it, because they are easy to write
against a room that does not exist locally and impossible to test honestly
otherwise.

### 9. Federation IV — judge the event that arrived (no Complement win yet)

This step buys no green test: Complement cannot get as far as judging an event
until step 10 lets a second server into a room. It is a prerequisite for every step
after it, so its tests are in-repo and its definition of done is local.

Half of it is in, on 2026-11-02.

- [x] **The rules themselves**: `internal/authcheck` answers "may this event be in
      this room, given the state these rules read?" — the numbered list of the room
      version, over a `Graph` of events and a `State` snapshot. Pure, no database, no
      network, so every clause is pinned by a table in `tests/unit/authrules_test.go`
      (~70 cases across versions 2, 5, 7, 9, 10, 11). Two entry points, and the split
      matters: `Check` is the whole list including the rule that reads the sender's own
      choice of `auth_events`, and `Rules` is the list without it — which is what state
      resolution needs, because resolution is there to re-derive that choice.
- [x] **State resolution v2**: `internal/stateres`, the specification's algorithm —
      unconflicted map, conflicted set, auth difference, power events sorted by reverse
      topological power ordering, iterative auth checks, then the rest by mainline
      order, then the unconflicted keys restored. `tests/unit/stateres_test.go` pins
      five forks, including the one that matters most: a kick on one fork beats what the
      kicked user did on the other.
- [x] **The first three receipt checks**, in the spec's order, in
      `Service.judgeIncomingPDU` on the `PUT /send` path: `eventcrypto.PDUCheck` (shape
      for the room version — see `tests/unit/pduformat_test.go`), then the author's
      signature with a key fetched and verified over federation, then the content hash.
      A hash mismatch keeps this server's own redacted copy rather than the peer's, and
      `TestEventsAreQueuedAndAnswered` now ships a genuinely signed PDU over the real
      transport and shows all three outcomes.

The rest closed on 2026-11-02, and with it the step:

- [x] **The remaining receipt checks.** `Service.applyIncomingPDU` reads the room's
      rules twice: against the events the arriving event cites as its `auth_events`
      (which includes the rule that the set it cites is exactly the set the selection
      algorithm names — the one that catches a sender picking friendlier rules for
      itself), and against this server's current state of the room. The first refusal is
      a **rejection**; the second is a **soft failure**.
- [x] **The auth chain is fetched when it is not held.** `gatherAuthChain` walks the
      chain, reads what this server has, and asks the delivering peer for the rest over
      `GET /_matrix/federation/v1/event/{eventID}` — served for the first time in the
      same change (`Service.FederatedEvent`), because a chain this server cannot complete
      is a chain it can only refuse. A fetched event goes through the same intake checks
      as a pushed one, and its id is recomputed and compared: a peer asked for one event
      cannot answer with another. Every event in the chain is then put through the rules
      against its own chain, so an arriving event cannot be judged by a power levels
      event nobody ever authorised.
- [x] **Both outcomes have their semantics.** Rejected: no event row, no state, no leaf,
      no client ever sees it — the receipt row keeps it with the reason, which is where
      "why is this event not in my room" is answered. Soft-failed: stored, relayed to the
      other servers in the room, kept out of the state, out of the forward extremities and
      out of every client read — `History`, `Timeline`, `ByPositions`, `Relations`,
      `Search`, `HistoryByTimestamp` and `GET /rooms/{roomID}/event/{eventID}` all filter
      it. A local send after a soft-failed event does not cite it.
- [x] **The queue is consumed**, inline as each event arrives, and that is a decision
      rather than a shortcut: the transaction answer a peer waits for is the answer to
      "what did you decide about this event", a retry has to get the same answer, and the
      state of a room must not depend on when this server gets round to its backlog.
      `ReceivedPDURepo.Pending`/`Count` remain for an operator asking what is waiting, and
      `List` was added for what has arrived at all.
- [x] **`tests/federation/intake_test.go`**: three cases, no fake events. An accepted
      message from the other server appears in the receiver's `/messages`, advances the
      forward extremities and leaves the state alone; a message from a user this server
      has banned and the sender's server has not is stored, hidden, and not a leaf; a
      message that cites a set of rules of its own choosing is refused with a reason, and
      is nowhere a client can reach.

Still open from step 9, deliberately:

- [ ] The **authorising user's server signature** on a restricted join
      (`join_authorised_via_users_server` has to be signed by that user's homeserver, or
      "my friend let me in" is a claim anybody can make). `authcheck` checks everything
      about it that a state snapshot can carry; the signature needs the join handshake of
      step 10 to have anything to check. The third-party invite signature needs no such
      thing — its keys travel in the room's own `m.room.third_party_invite` event — and
      `authcheck` checks it.
- [ ] **There is no per-event state snapshot**, so "the state at the event" is judged as
      the events the sender cites plus this server's own state, rather than as a state
      this server computed for that point in the graph. That is enough to enforce the
      rules, and not enough to *re-derive* the auth event set for an event whose state
      this server never saw. Snapshots come with the read side of the graph (step 11:
      `state_ids`, `get_missing_events`, backfill), which is where they are needed for a
      reason other than this one.

- [x] ~~**Locally originated events are not judged by `authcheck`.**~~ Done in step 20: the
      rules are read once, of `authcheck`, for what this server writes and what a peer
      sends. Two rules that had drifted apart were found doing it - a server administrator
      above a room's join rule, and a ban without the rank rule beside it.

Done when: an event created on server B appears on server A with A's own decision
recorded — done, and pinned by `intake_test.go`. The other half of the sentence, "and A
and B resolve the same state after a deliberate fork", waits for a fork two servers can
actually build, which is step 10's join plus step 11's graph reads; `internal/stateres`
makes it checkable in the meantime.

### Findings worth not re-deriving (step 9)

- **TODO.md was wrong that state resolution existed.** It named `internal/stateres` and
  `eventcrypto.PDUCheck` as done work; neither file was in the tree, and the intake
  accepted any PDU with any hash and any signature. Trust the tree over the plan.
- **The `auth_events` selection is written twice** — `selectAuthEvents` in
  `internal/service/dag.go` picks them when an event is made, `authcheck.Selection`
  re-derives them when one is judged. Both are the spec's list; they have to stay the
  same list, because a sender that chose otherwise has changed which rules apply to its
  own event. Rule 2 is what catches the divergence, and it refuses an event whose auth
  events are *also* more than the list, so a lax selection is as fatal as a mean one.
- **Room version 2 does not drop a failed power event's state entry.** Room version 1
  removed all events with the same `(type, state_key)` when a *power event* failed auth;
  the v2 text says only that the event is ignored, and v2 gets that protection from
  weighing power events first instead. Implementing v1's removal in the v2 loop silently
  deletes the honest change the other fork made.
- **The auth-event-set rule must not run inside resolution.** Resolution assembles the
  state an event is checked against, and the event's `auth_events` are what it is there
  to re-derive; running rule 2 there refuses nearly every event under resolution. Hence
  `authcheck.Check` versus `authcheck.Rules`.
- **A `state_default` of 0 means 0.** `power.ApplyDefaults` folds an explicit 0 back to
  50 because a decoded int64 cannot tell 0 from absent, and the client-facing checks
  accept that. The authorization rules read the raw content, where presence *is*
  visible, so `authcheck.powerLevels` restores defaults only for keys the room did not
  write — and `power.EventRequired` now takes levels that already have defaults restored
  rather than guessing again.
- **Refusing more than the room's rules refuse is not safety.** An authorising user
  named on a join to a *public* room is decoration: the room is open either way, and a
  server that refuses such a join becomes the only one that could not read the room. The
  authoriser is checked where its rules apply — under `restricted` and
  `knock_restricted` — and ignored elsewhere.
- **The content hash covers the unredacted event in every room version** (the v11 change
  is about which keys redaction strips, not about hashing). So a redacted copy never
  matches its own hash, and the receiver's rule is "use the redacted copy I computed",
  not "reject".
- **The author of a PDU in room version 3 and later is the sender's server.** Versions 1
  and 2 name it twice (`origin`, and the domain of the id the origin chose) and both
  have to have signed; later versions name nothing, and the spec's list of expected
  signatures says "the sender's server". The *delivering* peer never signs a PDU — that
  is the point of signing PDUs at all.
- **A test fixture with `"hashes": {"sha256": "not-a-real-hash"}` tests the queue, not
  the checks.** `TestEventsAreQueuedAndAnswered` had one, and passed for months; with
  the receipt checks wired in it had to sign the event with the server's own key, which
  is what makes it also able to show a forged signature and a tampered body.
- **The server-server tally is unchanged by the intake checks: still 87 of 91.** A
  re-run after this half of step 9 (`./tests`, 126 deployments, no panics) fails exactly
  the set it did before, minus `TestInboundFederationKeys` which went green in step 8.
  Step 9 turns no test green by design — Complement cannot reach event judgement until a
  join exists — so a run that stays flat while the intake gets stricter is the result
  that says nothing else broke.

- **A state summary in the graph can shadow the real event.** `readCurrentState` first
  added a room's state entries to the graph from the state rows, which carry no
  `prev_events` or `auth_events`; the chain walk then believed the chain was complete,
  and the transitive check refused every event in the room ("cites no auth events of its
  own"). State entries are now read as events where this server has them, and the graph
  tracks which entries are held in full: a summary is enough to read a rule against and
  not enough to check a chain with.
- **A fetched auth-chain event is deliberately not stored.** Synapse and Dendrite persist
  the auth chain they fetch. Lattice's `/messages` reads event rows, so persisting a
  fetched auth event would put an event into a room's timeline that is not part of the
  room's state — which is the opposite of what the state rules just decided. The chain is
  re-fetched per judgement instead; if that becomes too slow, the fix is a table for auth
  chains, not the events table.
- **Redaction strips the graph.** In room version 11 the keys a redaction keeps do not
  include `auth_events` or `prev_events`, so an event kept as its redacted copy (a content
  hash that did not match) cannot be used as a link in an auth chain. The intake keeps the
  redacted copy because the spec says to, and the chain then has to be fetched again.
- **"Hidden from clients" is a property of every read, not of one.** A soft-failed event
  is in the events table, so `/messages`, `/sync`, `/context`, relations, search,
  timestamp-to-event and `GET /rooms/{roomID}/event/{eventID}` each had to learn to filter
  it. Any new read path over that table has to decide the same question again.
- **Answering `GET /event` is what made judging possible.** Two Lattice servers could not
  exchange an accepted event at all before it: each cited auth events the other had never
  been sent. It is served only to a server that shares the room by membership, ACL-read
  the same way the outbox reads it, so an excluded server cannot fetch its way back in.
- **Fetched events are verified against the id that was asked for.** Room versions from 3
  on carry no id inside the event, so the only way to know a peer answered the question
  asked is to hash the answer and compare it with the id requested.

### 10. Federation V — the first honest cross-server join — done (2026-11-02)

The step the rest of federation waits for: a user on one Lattice gets into a room
on another, and both hold the room afterwards. That now works in both directions,
and it is the first step to turn Complement server-server tests green: **17 of the
91** pass, up from 4.

- [x] Outbound: `#alias:remote` resolved through `GET /v1/query/directory` →
      `GET /v1/make_join/{roomId}/{userId}` (falling back to v2) →
      `PUT /v2/send_join/{roomId}/{eventId}` → the state and auth chain it answers
      with are persisted, marked `not_in_timeline` because the peer's answer is a
      description of the room and not traffic this server saw.
- [x] Inbound: `make_join` v1/v2 answer with the room version and a join template
      whose `prev_events` and `auth_events` come from this server's graph, judged
      against the room's join rules and ACL; `send_join` v1/v2 verify the event,
      apply it, and answer with the state — v2 with `servers_in_room` and, when
      `omit_members=true` is asked for, the partial-state answer with
      `members_omitted: true`.
- [x] Guards Complement checks and a naive implementation fails: a `send_join`
      carrying anything but a **join** is refused, a banned user's `send_join` is
      refused, and a `send_join` whose event id does not match the path (or whose
      hash does not match the id in a v3+ room) is refused.
- [x] The room-creation default flipped: `m.federate` defaults to **true**, with
      `-no-federation` the server-wide opt-out. While the default was false every
      room this server made was silently un-federated.
- [x] A membership change made by the room's server reaches the server of the user
      it is about — a ban whose destination list is built from the membership the
      ban already changed is a ban the banned user's own server never hears about.
- [x] The same handshake for leave: `make_leave` v1/v2 and `PUT /v1|v2/send_leave`, a
      leave made and signed by the leaving server, refused unless it is a **leave** by
      the sender of themselves (a kick is an ordinary event from the kicking server, not
      a `send_leave`), and applied and relayed to the rest of the room when accepted. A
      local user leaving a room this server does not host goes through that server,
      which is the only way the room's own list of members stops being wrong.
- [x] Knock over federation: `GET /v1|v2/make_knock/{roomID}/{userID}`,
      `PUT /v1|v2/send_knock/{roomID}/{eventID}` and `POST /v1|v2/knock/{roomID}/{eventID}`
      (the last carries a knock between servers that hold a copy of the room without hosting
      it). The room's server answers with agreement, not state, and queues the knock for the
      rest of the room; a knock may be made again, and only a user the room has already
      answered - joined, invited, banned - is refused. `TestKnocking`,
      `TestKnockingInMSC3787Room`, `TestKnockRoomsInPublicRoomsDirectory` and
      `TestCannotSendNonKnockViaSendKnock` are green.
- [ ] Invites over the dedicated endpoint: `PUT /v1|v2/invite/{roomId}/{eventId}`,
      whose `invite_room_state` must carry `m.room.create`. A local `/invite` of a
      remote user already reaches them today by the ordinary event relay, which is
      enough for `TestUnbanViaInvite` (a room both servers are already in) but not
      for `TestFederationRoomsInvite`, where the invited server is not in the room
      yet and the invite endpoint is the only road. Also open: refusing an invite
      for a local user who does not exist while allowing a remote name, which is
      what `TestFederationRejectInvite` checks from the other side.

`TestOutboundFederationSend` is green (steps 9 and 10 together), and so is
`TestRemoteTyping`: a typing EDU is only delivered to a server that holds a
membership for the user.

Complement is green now: `TestJoinViaRoomIDAndServerName`,
`TestJoinFederatedRoomFailOver`, `TestSendJoinPartialStateResponse`,
`TestCannotSendNonJoinViaSendJoinV1`, `TestCannotSendNonJoinViaSendJoinV2`,
`TestCannotSendNonLeaveViaSendLeaveV1`, `TestCannotSendNonLeaveViaSendLeaveV2`,
`TestBannedUserCannotSendJoin`, `TestUnbanViaInvite`,
`TestJoinFederatedRoomWithUnverifiableEvents`, `TestOutboundFederationSend`,
`TestRemoteTyping`, `TestRemoteAliasRequestsUnderstandUnicode`,
`TestInboundFederationRejectsEventsWithRejectedAuthEvents`.
Still red from this list, and who owns them: `TestFederationRoomsInvite` and
`TestFederationRejectInvite` (the invite endpoint `PUT /v1|v2/invite`, the only road to a
server that is not in the room yet), `TestKnocking*` and the knock guards (the knock
handshake), `TestUserAppearsInChangedDeviceListOnJoinOverFederation`
(step 13: the device-list EDU on join), `TestMessagesOverFederation` and
`TestPushRuleRoomUpgrade` on the csapi side (they need a room two servers share,
which now works, plus the pieces named above).

### Findings worth not re-deriving (step 10)

- **A stored event of a v3+ room carries no id inside itself, and a graph keyed by
  id cannot hold it.** `authcheck.Graph.Add` drops an unnamed event, and the graph
  then falls back to the state *summary* of the same event — which has no
  `prev_events` and no `auth_events`. The chain walk stops early and the transitive
  check reports an honest event as "cites no auth events of its own". Every
  `graph.Add` of a stored event has to be `pduEvent(withEventID(json, row.EventID))`.
- **`selectAuthEvents` reads the membership out of the draft's content.** The
  `make_join` template is built from a draft with no content, so its membership is
  the empty string, `m.room.join_rules` is left out of the template's auth events,
  and the joining server's own rules then refuse the join it was handed. A template
  for a join has to carry `"membership": "join"` in its draft.
- **A room this server holds is not a room this server hosts.** Joining a room id
  whose domain is another server has to go through that server again even when this
  server already holds the room from an earlier join, a ban or an invite — which is
  the second half of every leave-and-rejoin (`TestUnbanViaInvite`).
- **Membership destinations.** `destinationsOfRoom` answers "who is in this room",
  read after the event was applied. For a membership event that is the wrong
  question: a ban, kick or leave removes its own target from the list, so the
  server of the user it is about is never told. `membershipEventServers` adds the
  target's and the sender's servers back, subject to the room's ACL.
- **A banned user's rooms belong in `rooms.leave`.** `buildSync` queried
  `MembershipLeave` and never `MembershipBan`, so a ban that arrived over
  federation updated the database and said nothing to the client — the sync had no
  room at all, and the ban event, the only explanation, never reached anyone.
- **What a room cites, a server keeps.** An auth-chain fetch used to be consulted and
  thrown away: the events were added to the in-memory graph, used for one judgement, and
  forgotten. That left a server unable to read the rules of a room it was never handed - an
  invite relayed to a server that is not in the room cites the inviter's membership, and
  without it the invite reads as one made by somebody who is not in the room, so it is
  soft-failed and the user is never told. Fetched events are kept now, as outliers: event
  row, state row where the room had none for that key, membership side effects - signature
  and hash checked on the way in, never overwriting state this server already resolved,
  never advancing the graph's leaves. The room row is created too, because a server can now
  hear of a room from an event somebody cites as authority, and a state event whose room
  does not exist fails its own side effects with "record not found".
- **A delivery is not unmade by the server that made it impatient.** Applying an event can
  mean asking the peer that delivered it for the events the chain cites; when that asking
  ran on the inbound request's context, a sender that gave up canceled the fetch halfway
  through and the event was refused for it. The same holds for key verification: a key
  document a peer hands over is worth caching even if the peer has stopped waiting, or the
  retry that follows starts another fetch the same impatience cancels. Both run on their
  own bounded clocks now - 20 s to apply an event, 10 s to verify a signature.
- **An invite still costs one round trip per event the invited server lacks.** With the
  above landed, `TestFederationRoomsInvite` and `TestFederationRejectInvite` get further
  and still fail: the chain of an invite in a room that has been talking cites several
  events the invited server has never been told about, and each one asks for its own HTTPS
  round trip - each of which first needs the peer's key. That is what the invite endpoint
  is *for*: `PUT /v2/invite/{roomId}/{eventId}` carries `invite_room_state`, the stripped
  state the invite is authorised by, in a single request, and an invited server that is
  given it needs no fetches at all. Shipping that state, on both sides of the wire, is the
  next chunk.
- **The invite endpoint is in, and the two invite tests fail on other things.** A room that
  invites a user whose server is not in the room now ships the invite and its stripped
  state over `PUT /v2/invite/{roomId}/{eventId}` (v1 as a fallback), and the invited server
  keeps the room row, the state it was handed, and the membership - answered back signed by
  both servers, since a signature is made over the event and not over somebody's copy of
  it. A room version from 3 onwards carries no id inside the event, so the invited server
  works the version out by hashing the invite under each version it speaks and keeping the
  one that reproduces the id it was given. Locally, a three-server test shows the invited
  server holding create, join rules and the memberships the invitation is between, and its
  user syncing the room in the `invite` bucket. Both tests then failed on further gaps, and
  those are now closed: `unsigned.prev_content` and `unsigned.prev_sender` exist (see the
  next finding), and kicking reaches invited and knocking users rather than only joined ones.
  `TestFederationRejectInvite` is green.
- **A chain event re-judged against the room's present state refuses an honest event.**
  `TestFederationRoomsInvite` is the one invite test still red, and its refusal reads
  "the auth chain carries $..., which the rules refuse: @user cannot leave a room it is not
  in (membership: none)". The chain event is a leave the room had already soft-failed, and
  the transitive check reads the room's membership as it stands *now* rather than as the
  state that event's own auth events describe. An auth event is a statement about a moment,
  not about the room today: a leave cites the membership that stood when it was signed, and
  judging it against a later state says something the room never claimed. Reading each chain
  event against the state its own auth set names - which is what `stateOfAuthIDs` was meant
  to do - is the next thing to get right, and it gates more than this test.
- **The invite's room-version trick works locally and not in Complement.** An invited server
  with no room row hashes the invite under each version it speaks and keeps the one that
  reproduces the id it was given. Locally that matches; against Complement's servers every
  invite logs "names an id no room version this server speaks reproduces" and falls back to
  the default, which happens to be right there. The fallback is safe and the log is honest,
  but the mismatch means the derivation is not doing its job, and a room of an older version
  would be guessed wrong until the join handshake asks.
- **A state event that does not say what it replaced is a change with no before.**
  `unsigned.prev_content` and `unsigned.prev_sender` did not exist in Lattice at all: a
  client shown a membership of "join" cannot tell an accepted invitation from a knock that
  was let in, and a rename without the name it replaced is a rename nobody can explain. The
  field is unsigned, so it is written when the event is built without touching what is
  hashed or signed - which also lets the room's own server annotate a `make_join` and
  `make_leave` template with what the membership there replaces, since only that server
  knows. Three places had to cooperate for it to arrive: the joining server had to carry the
  template's annotation instead of rebuilding the event without it, the client rendering
  path had to merge its derived `age` into the stored `unsigned` instead of replacing it, and
  `createRoom`'s `is_direct` had to reach the invitations it makes - a client decides a room
  is a direct conversation by reading that key off the membership event, and the other
  server only ever sees that event.
- **An event kept as an outlier was left out of the room's own history.** A join hands this
  server its own join event inside the room's state, so that event arrives marked as another
  server's history; the join then stores the same event itself, the write is a duplicate, and
  the duplicate was ignored - leaving a room with a hole where its own join belongs, a
  history read that cannot find the event that made the server part of the room, and a client
  that never sees it. A duplicate arriving as something that happened here now promotes the
  event into the timeline at this moment: the content of the copy on disk stands, because it
  is the same event, but the timeline flag and the position are not somebody else's to keep.
- **A changed `-server-name` locks every existing account out of federation, quietly.**
  Accounts are keyed by their full user ID, which carries the name they were created for.
  Set the name to something else over a database that already has accounts - a real case:
  an operator adding the `:443` that federation needed put it on `-server-name` as well as
  in the well-known document - and the sessions keep authenticating, because sessions are
  rows in the same file, while every join answers
  `403 M_FORBIDDEN: you must have an account to join a room`. The message is the problem as
  much as the mistake: it sends the operator looking for an account that is sitting in the
  database. The refusal now names the account and the name this server answers to; the
  account is checked before the federation transport is blamed, so a session the server
  cannot act for is not reported as a room it cannot reach; and the startup log counts the
  accounts that belong to another name.
- **A peer refuses an event it cannot check, and blames the sender.** Sending into a room
  hosted elsewhere stopped with `cluephone.org refused an event from this server: Your server
  isn't divulging details about prev_events referenced in this event`, next to a fetch of
  `/_matrix/federation/v1/state_ids/...` that never arrived. Both halves are the same thing:
  a receiver does not accept an event on trust - it asks for the room's current state, the
  state behind it, and the authority chain of the event - and a server that serves none of
  those is refused for events that are perfectly correct. `state_ids`, `state/{roomId}/{eventId}`
  (v1 and v2), `event_auth/{roomId}/{eventId}` and `get_missing_events/{roomId}` are served now,
  to peers that are part of the room only, and a refusal to relay is logged with the sender of
  the event named, so a relay of somebody else's event is not mistaken for a fault of this
  server. Two things remain in this area: `backfill` and `timestamp_to_event`, and peers that
  check an event's `origin` against its sender - room versions 11 and 12 have no `origin` in
  the event, so a strict peer rejects a correctly relayed event and nothing on the sending
  side can satisfy it without changing the event's hash.
  The two gaps the same tests name: history visibility has to be respected on what is served
  (under `joined` or `invited`, events that predate the asking server's presence go out
  redacted, and `m.room.history_visibility` is currently read only to decide who may read a
  room at all), and a gap this server notices in its own timeline has to be asked about - the
  outbound half of `get_missing_events`.


- **A room version this server does not speak is refused by name, not as a missing room.**
  A real join of a room on another homeserver failed with the peer answering
  `M_INCOMPATIBLE_ROOM_VERSION`, and Lattice reported
  `404 no server in the list could complete the join` - a sentence that sends an operator to
  compare two healthy servers. The version now travels: a peer's refusal carries
  `room_version`, this server keeps it and answers the client with
  `M_INCOMPATIBLE_ROOM_VERSION` plus that version, and its own `make_join` answers the same
  way when asked for versions none of which it holds. The room in the field was version 12,
  which Lattice does not speak: **room version 12 is the next room-format step**, and it is
  more than adding a number to the list. It drops the domain from room IDs (the room in the
  field was `!HgwqGE1...` with no `:host`, which several paths in this server read as a
  malformed ID), it gives `m.room.create.content.creators` power over the power levels, and
  it measures event limits in bytes. The list of versions a server speaks is configuration
  now (`service.WithRoomVersions`), which is what made the refusal testable: a cluster where
  one server speaks only version 1, joining a version 11 room.

- **A peer that cannot fetch this server's key refuses everything, and the client sees an
  authentication error.** A real deployment behind a reverse proxy on 443 answers peers on
  443, but the well-known document published `m.server: <server name>` - a name with no port,
  which means 8448. Every peer therefore knocked on a closed port, could not fetch
  `/_matrix/key/v2/server`, and refused to believe a signed request from this server. The
  room looked unjoinable and the client was handed
  `401 M_UNAUTHORIZED: no signing keys available for <this server>` - words from the peer,
  and a code a client reads as "your access token is no good", which costs a user their
  session. Two answers: `-federation-advertise host:port` for the well-known document, and a
  peer's `M_UNAUTHORIZED` translated into a `502 M_UNKNOWN` that names the peer, with the
  reason and the address peers use logged where an operator will see it - including at
  startup, before a single request has failed.
- **An event must not cite the same auth event twice, and a membership event about
  oneself does.** `selectAuthEvents` adds the sender's membership and the target's
  membership, which are one and the same stored event when the sender equals the state
  key. Locally created events got away with the duplicate, because this server does not
  run a receiver's checks on its own events; the room's server refused a federated leave
  for it. Auth event selection de-duplicates now.
- **Partial state is the room before the join.** The state and the server list of a
  `send_join` answer are read *before* the join is applied, and this server names
  itself in `servers_in_room` because hosting the room is being in it. Following
  Synapse: every non-member event, the joiner's membership as the room already had
  it, and the heroes' memberships **only when the room has no name and no canonical
  alias** — heroes are what a client shows for a nameless room. `members_omitted`
  reports that memberships were left out of the answer, which is true whenever
  partial state was asked for, even if every member of a small room is a name that
  stayed.
- **Room presets and `m.room.guest_access`.** Lattice wrote a guest-access event
  into every room with the pre-spec value `can_connect`. The presets the ecosystem
  agrees on give a `public_chat` room **no** guest-access event at all, and that
  shows up over federation: a partial-state answer whose state carries an event no
  one else's public rooms have fails a shape check written against everyone else.
  `can_join`/`forbidden` are the spec values; `can_connect` is still read.
- **`make_join` is asked for at v1 first.** v2 adds nothing v1 cannot carry (both
  send `ver=`), and servers that have not implemented v2 answer `M_UNRECOGNIZED`,
  which some deployments log as an unexpected request rather than a miss.
- **Federation refusals have to be logged.** A rejected PDU's reason is stored in a
  column nothing reads; the sender is told and the operator is not. Rejections and
  soft failures now log the reason, which is how "why is this event not in my room"
  gets answered without a database query.

`TestOutboundFederationSend` (a message sent locally reaches the peer — it needs
steps 9 and 10 together), and `TestACLs`/`TestACLsForEDUs`, whose rules step 8
implemented but which could never run until two servers shared a room.

Complement turns green: `TestJoinViaRoomIDAndServerName`,
`TestJoinFederatedRoomFailOver`, `TestSendJoinPartialStateResponse`,
`TestCannotSendNonJoinViaSendJoinV1`, `TestCannotSendNonJoinViaSendJoinV2`,
`TestCannotSendNonLeaveViaSendLeaveV1`, `TestCannotSendNonLeaveViaSendLeaveV2`,
`TestBannedUserCannotSendJoin`, `TestUnbanViaInvite`, `TestFederationRoomsInvite`,
`TestFederationRejectInvite`, `TestRemoteTyping` (a typing EDU needs a membership
on the peer), `TestUserAppearsInChangedDeviceListOnJoinOverFederation` (the join
plus the device-list EDU), and on the csapi side `TestMessagesOverFederation` and
`TestPushRuleRoomUpgrade` once steps 9 and 10 combine.

### 11. Federation VI — the read side of the graph: auth chains and gap filling — **done (2026-11-03)**

Two servers that were apart have to agree afterwards, which means each has to be
able to name the events the other is missing and to prove an event's authority.

- [x] Persist and read the auth chain as a query (the arrays are stored per event
      since step 7; the walk is what is missing).
- [x] Serve `GET /event_auth/{roomId}/{eventId}`, `GET /event/{eventId}`,
      `GET /state_ids/{roomId}` (including the partial-state form `send_join`
      uses) and `GET /state/{roomId}/{eventId}` for one event.
- [x] Serve `POST /get_missing_events/{roomId}` (bounded by `limit`, only events
      the requester may see) **and** request it: on a gap, ask, receive the events
      through `/send`, apply them through step 9. Gap filling is also what makes
      soft-fail and rejected events observable in practice.
- [x] Serve `GET /backfill/{roomId}` (older history with its state, depth-bounded)
      and `POST /timestamp_to_event/{roomId}` (MSC3030).
- [x] Events whose auth chain is broken, or that hang off a rejected event:
      refused, and reported in the per-PDU answer rather than retried forever.
      Unverifiable events in room versions that tolerate them are a separate rule,
      and `TestJoinFederatedRoomWithUnverifiableEvents` checks it.

What the step settled on, beyond the list:

- History visibility is enforced on the *read* side, not only on `/send`.
  `/event`, `get_missing_events` and `backfill` hand a server an event with its
  signature and hashes intact and, when the room says the asking server was not
  entitled to it, with its content gone — the same `Redact` step 5 built for
  `/redact` and `/event`, with the event's own auth events as the state the
  decision is made against. The membership half is read as of the event; the
  visibility half is read from the room as it stands, because this server keeps
  no state snapshot per event and a room that never changed its visibility —
  almost every room — has the one answer either way.
- Three reads stay whole: `state_ids`, `state` and `event_auth`. A peer cannot
  judge an event without the rules and memberships it cites as authority, and a
  membership with its content stripped cannot say whether the asking server's
  user was in the room — the question the redaction itself rests on. Stripping
  those produces refusals of honest events, which is how Synapse's
  `TestEventAuth` ends up asserting that an *invite* is answered unredacted.
- The gap is asked about before the event is judged, and the asking is bounded:
  three rounds per arriving event, twenty events per round, ten seconds. A round
  that answers and lands nothing stops the asking rather than repeating it — the
  peer would answer the same way — and the reason is logged. Gap-filled events
  are applied oldest-first (a parent before the event that cites it) and relayed
  to nobody: every other server in the room can ask the same question now that
  this one holds the answer.
- A refusal travels one step and no further. An event whose parent this server
  decided about and refused is refused too, with the parent named in the answer;
  a parent that is merely unknown is a gap, not a verdict, and is not held
  against the event above it — which is the difference between "this room said
  no" and "nobody has told me yet", and the distinction
  `TestUnrejectRejectedEvents` is about.

Complement turns green: `TestEventAuth` (done - it is green as of the graph reads).
The outbound half of `TestGetMissingEventsGapFilling` and the visibility redaction of
`TestInboundCanReturnMissingEvents` are the two this step was waiting on;
`TestOutboundFederationEventSizeGetMissingEvents` also pins `earliest_events` to the
sending server's own newest event, which is what `fillGraphGap` names.
Still unread as a whole: `TestOutboundFederationIgnoresMissingEventWithBadJSONForRoomVersion6`,
`TestCorruptedAuthChain`, `TestUnrejectRejectedEvents`,
`TestInboundFederationRejectsEventsWithRejectedAuthEvents`,
`TestJoinFederatedRoomWithUnverifiableEvents`, `TestFederationRedactSendsWithoutEvent`,
`TestNetworkPartitionOrdering`.

### 12. Federation VII — names, profiles and spaces

- [x] `GET /_matrix/federation/v1/query/directory`, so a remote server can resolve
      a local alias (Unicode included), and the client-side path that then joins
      through it. *(Was already served; the gap was the answer's `servers` list,
      which named only the other members and left a room hosted nowhere else —
      including here — unaddressable. `DirectoryAnswer` now puts this server first.
      `TestARemoteAliasIsResolvedByTheServerThatHostsIt` covers a non-ASCII alias
      resolved over federation and then used by a client of the other server to
      join.)*
- [x] `GET /_matrix/federation/v1/query/profile` — display name and avatar of a
      local user, refused for a user this server does not have. *(Served: one field
      at a time or both, an empty answer for a field this server does not hold, and
      `M_NOT_FOUND` for a user that is not ours rather than an empty object, because
      "no display name" and "no such user" are different answers a client cannot
      recover from once they look the same. The outbound half is
      `RemoteUserProfile`, which a client's `GET /profile/{userId}` now uses for a
      remote user instead of answering 404 — and takes the peer's word only for the
      two fields it asked about, never for whether the user exists.)*
- [x] `GET`/`POST /_matrix/federation/v1/publicRooms` for another server's room
      directory, and the client-side `/publicRooms?server=` that reaches it.
      *(v1 GET and v3 GET/POST served; `RemotePublicRooms` asks v3 first and falls
      back to v1 when the peer does not know it. A room whose `m.room.server_acl`
      excludes the asker is left out of the page: publishing says "anyone may find
      this", an ACL says "not that server", and the ACL is the room's own word.
      `destinationsOfRoom` excludes this server because it exists to choose where to
      send, so the directory answer prepends it — otherwise a room nobody else has
      joined is listed with nowhere to go.)*
- [x] `GET /_matrix/federation/v1/hierarchy/{roomId}` to serve our side of a space,
      and `walkHierarchy` following remote spaces through it instead of skipping
      them. *(One walk serves both endpoints, with a `reader` that is either a user
      of this server or a peer: a peer sees a space when it is world readable or the
      peer's server is in it, and a space that is not open to be read is refused
      rather than answered as an empty tree — "there is nothing here" is not this
      server's claim to make about a room it is keeping private. The walk asks the
      servers the `m.space.child` link names, translates the depth window before
      asking so the peer's page arrives already cut to it, and bounds itself at 50
      outbound asks and 15 seconds; whatever falls out of that is `incomplete_chain`.
      Two sets drive the walk — `visited` for rooms already queued or asked about,
      `described` for rooms in the page — because a remote child is in the first
      before its own description arrives, and collapsing them is how a room with two
      parents ends up listed nowhere at all.)*
- [x] Decide what happens to `GET /_matrix/federation/v1/openid/userinfo`: it is
      indexed and unimplemented, and it exists for third-party invites, which are
      themselves deferred. Record the decision instead of leaving it unlisted.
      *(Implemented, because the minting half already exists: `openid/request_token`
      hands a user a credential to show a stranger, and a server that will not say
      what its own tokens mean has sold the user something with no way to spend it.
      Third-party invites stay deferred; this endpoint is what they will need, and it
      is one name. It is registered unsigned on purpose — the asker is usually an
      identity server with no homeserver key, and the token is the credential. Two
      tests: only the minting server answers, a stranger's server refuses, and the
      token is not a session (`/whoami` refuses it).)*

Complement turns green — all six, verified against the image built from this commit:
`TestRemoteAliasRequestsUnderstandUnicode`, `TestInboundFederationProfile` (both
subtests, including the one that rejects a server name whose port is not a number,
which is what `matrix.ValidServerName` is for), `TestOutboundFederationProfile`,
`TestFederatedClientSpaces`, `TestClientSpacesSummary` (all five subtests) and
`TestClientSpacesSummaryJoinRules`.

Serving the federation half of the hierarchy exposed three things the client half had
been getting quietly wrong, and Complement is precise about them:

- **No default depth.** The specification names none, and Synapse walks the whole space
  when the caller does not say. A client that asked for a space and was handed one level
  of it was being shown a room with no children, so an unnamed window now means the whole
  space, bounded at twelve levels and a thousand rooms to stop a space that links itself.
  `max_depth=0` is still an answer: the room and nothing below it.
- **Depth first, not breadth.** The endpoint is specified as paginating the tree depth
  first, and the pages prove it: `limit=4` over Complement's space is Root, R1, SS1, SS2 —
  one branch finished — where a breadth-first walk hands over the shallowest rooms and the
  room a client is looking for lands on page seven. The walk is now a stack, with each
  room's links pushed back to front so the space's own ordering survives.
- **`suggested_only` and removed links.** A link without `suggested: true` takes its whole
  branch out when the short list is asked for — links in `children_state` included, since
  showing a door and refusing to walk through it is worse than no door. A link that has
  been emptied (how a child is taken out of a space, because state events are not deleted)
  is not shown at all: it is a branch the space closed, and offering it lists the same room
  twice whenever another space still names it.

### 13. Federation VIII — cross-server E2EE, device lists, to-device and media

The sinks for all of this landed in step 8; this is the producing and asking half.

- [x] **Emit** `m.device_list_update` when a device uploads, is renamed or loses its
      keys, with the per-user `stream_id`/`prev_id` chain, to the servers that hold
      that user in a room; and on a join, in both directions — the joining user's
      list to the servers already in the room, and every local list in the room to
      the server that just joined. *(The receiving half applied these since step 8 and
      never had a sender. A snapshot is sent per **device**, not per key upload: a
      device that has not published keys yet is still a device, and a peer told about
      nothing has no reason to ask. A join delivers its updates inside the join
      request rather than on the outbox timer, because the peer's client starts
      syncing a few milliseconds after the join returns — an update that leaves on
      the next tick puts the room in one sync response and the users in it in a later
      one, which is what `TestDeviceListsUpdateOverFederation` checks and what a
      client working out whom to encrypt for has to hold apart.
      `m.signing_key_update` is queued on the cross-signing paths already.)*
- [x] `POST /client/v3/keys/query` and `/keys/claim` for **remote** users: ask the
      owning server over `POST /federation/v1/user/keys/query` and
      `/user/keys/claim` (the older `GET /user/devices/{userId}` is kept as a
      fallback for a peer that does not know them), check each device's signature by
      the key inside it, cache, answer as if local. A `device_id` that is not there
      is asked for again, never invented. `GET /user/devices` is served too, since
      the older query form needs it. *(A device's `unsigned.device_display_name` is
      filled in on the way out — the label is this server's description, not part of
      what the device signed — and a cached remote device keeps the label rather than
      letting a later list un-name it.)*
- [x] To-device to a remote user: queue for the destination server, and retry the
      queued transaction until the peer answers. *(Inbound and the `*` expansion were
      there; the sending half was the whole of it. `TestAToDeviceMessageWaitsForTheServerThatIsNotAnswering`
      and `TestTheOutboxTimerDeliversWithoutAnyoneAsking` cover down-peer → queued →
      back-up → delivered once. One attempt at a transaction is bounded at 10s rather
      than the transport's 30s, so a peer that is silent rather than refusing costs a
      backoff slot and not the queue.)*
- [x] Federation media: `GET /_matrix/federation/v1/media/download/{mediaId}` and
      `thumbnail` — the box in the query that current servers send and the
      `/{width}/{height}` path older ones still do — answered as `multipart/mixed`
      with **exactly two parts**, and the client-side `/media/download` and
      `/media/thumbnail` fetching from the owning server for an `mxc://` this server
      does not hold, caching the bytes as a Media row. *(The copy is served from the
      local row afterwards without a round trip, so a peer going away does not take
      the content with it; a thumbnail of a copy already here is scaled here. A peer
      that cannot be asked is `M_NOT_FOUND`, and no empty row is left behind.)*

Three bugs that were not federation, found by the federation suite:

- Access tokens died on restart. The secret that signs them was generated per
  process while the sessions it authorised live in the database, so every restart
  was a mass logout that the database looked innocent about. A generated secret is
  now stored in `Meta` next to the sessions it made (`auth.TokenSecret`).
- The event stream counter was re-seeded from the **events** table at start-up.
  Receipts, to-device messages, device list changes and read markers take positions
  from the same counter, so a restarted server handed out positions it had already
  issued and every client holding a token past that point was told, forever, that
  nothing had happened. `Database.LatestStreamPos` takes the maximum across every
  table that carries one.
- A device list change made by one of a user's *other* devices was not reported to
  that user's own sync, so a client never learned to re-query when a second laptop
  appeared.

Complement (server-server package): `TestDeviceListsUpdateOverFederation` (all three
connectivity cases), `TestDeviceListsUpdateOverFederationOnRoomJoin`,
`TestUserAppearsInChangedDeviceListOnJoinOverFederation`, `TestFederationKeyUploadQuery`
and `TestContentMediaV1` are green. `TestToDeviceMessagesOverFederation` is green for
good connectivity; its `interrupted_connectivity` and `stopped_server` cases still
fail: the message is queued, and after Complement restarts the peer nothing in the
restarted sender's log shows an attempt being made, which the in-repo timer and
backoff tests (`tests/federation/timer_test.go`) do not reproduce.

### 14. Federation IX — a two-server suite that does not cheat, and the wrap

- [x] Rewrite the in-repo two-server cases that seed `Room`/`RoomMembership` rows so the
      membership is the result of step 10's handshake; keep the seeded form where it tests
      storage rather than flow. *(The harness gained `shareRoomByJoin`: the room invites
      the peer's user when it is made and that user's server joins, and the helper then
      fails the test if either server holds no joined membership or the joiner holds no
      room row — so the facts a later assertion leans on are checked rather than assumed.
      The outbox tests that send, type, receipt and exclude now start that way, which also
      exposed a counting mistake those tests had been making happily: a join handshake puts
      the joiner's own event on the wire, so "how many events did the peer receive" was
      measuring the guest list. `TestTheFederationScenarioTableRunsWithoutSeeds` is the
      table — join, invite→join, messages both ways, typing, receipts, to-device, remote
      profile, remote device keys, leave, backfill, soft-fail after a ban, a retried
      transaction applied once — twelve subtests, each on two servers that had never heard
      of each other's users. Two rows of the original list are named rather than faked:
      **knock** cannot cross a server boundary yet, and the **backfill** row goes over
      `GET /federation/v1/backfill` because a client's `/messages` still does not reach
      across a gap in its own history. The intake tests keep their hand-written join and
      say why: they are about what a server does with an event that arrives — authorisation,
      soft-failing, intake order — and a real handshake in the middle of each would add a
      minute of protocol and hide the thing under test.)*
- [x] Run both Complement packages and record the tallies here and in README. Measured
      2026-11-03 against Complement v1.19 at `/tmp/complement`, image
      `localhost/lattice-complement:latest`, and re-measured after the outbox fix:
      * `tests/csapi` — **40 red of 107** (67 green).
      * `tests` (server-server) — **51 red of 91** (40 green), against 4 green before
        step 10 and 22 green before step 11. Green includes the join/leave/invite
        handshakes, the graph and state reads, `backfill` and `timestamp_to_event`,
        profiles, the room directory and `publicRooms`, `/hierarchy`, OpenID exchange,
        `TestDeviceListsUpdateOverFederation` (all three connectivity cases),
        `TestFederationKeyUploadQuery`, `TestToDeviceMessagesOverFederation` with good
        connectivity, and `TestContentMediaV1`, and after step 16 the whole media set. The
        51 red are dominated by knocking and
        restricted rooms (~17), room versions past v11 (~13), application services and
        presence; the ones that are Lattice's own federation bugs are
        `TestACLsForEDUs` and the media thumbnail cases.
- [x] Regenerate `ENDPOINTS.md`: **177 of 234** indexed endpoints implemented, 37 extra
      routes (the admin aliases, `/_synapse/admin/*`, the refresh and device flows that
      the index lists under other names). Federation is near-complete; what is left in
      the index is the media-admin set, the CS-protocol proxying for rooms this server
      does not host, and the deferred list above.
- [x] README's federation section: what `-federation` turns on — including the
      cross-server encryption plumbing and federated media step 13 built — the TLS
      settings **as the flags actually are** (`-tls-addr`, `-tls-cert`, `-tls-key`,
      `-federation-advertise`, `-federation-peer`; there is no `-federation-address`,
      `-insecure` or `-no-federation`, and the plan's names for them are retired), and
      the fact that rooms are federated unless the creator says otherwise in
      `creation_content`. Also corrected: a generated `-jwt-secret` now survives a
      restart, which is what the step 13 bug fixed.
- [x] Decide `0.2.0` / `1.0.0`. **0.2.0**, bumped in `service.LatticeVersion` and the
      Swagger version. The reasoning is that "1.0.0 with federation" would promise
      federation as a settled default, and it is not: it is off until switched on,
      knocking does not cross a server boundary, restricted-room `join_authorised_via_users_server`
      is missing, and a peer's key is trusted only by its own signature rather than a
      notary's. Those are the four things that would make it 1.0.0. The release
      machinery (`Dockerfile`, `.github/workflows/image.yml`) publishes the multi-arch
      image on a pushed `vX.Y.Z` tag that must equal `LatticeVersion`, so the release
      itself is `git tag v0.2.0 && git push --tags` — left to the operator, since
      pushing a tag publishes an image.
### What else the server-server package holds, and who owns it

Steps 9 to 13 name 37 of the 87 red. The other 50 sit in that package because
Complement groups by **deployment**, not by protocol: most of them start a single
container.

- **Knocking and restricted rooms** (17: `TestKnocking*`, `TestKnockRestricted*`,
  `TestRestrictedRooms*`, including the MSC3787 variants) — join-rule `allow` lists
  naming users and spaces, `join_authorised_via_users_server` validated against a
  space the authoriser is in, and the spaces summary that supplies that
  authorisation. Client-side join rules, not federation; it fits after step 10
  because the space half wants step 12's hierarchy, and `TestKnockRoomsInPublicRoomsDirectory*`
  wants step 12's room directory too.
- **Room versions past v11** (13: `TestComplementCanCreateValidV12Rooms`, the five
  MSC4291 cases, the six MSC4289 privileged-creators cases) — a track of its own:
  `im.n.affoliat.matrix.v2`, an event id derived from the create event, and
  privileged creators listed in `m.room.power_levels.users` (the same gap csapi
  reports as `TestPowerLevels`). Not federation; a room-version step.
- **Stripped state** (`TestMSC4311StrippedStateClientAPI`,
  `TestMSC4311FullEventsOnStrippedStateFederation`,
  `TestMSC4311RejectInvalidStrippedStateFederation`) — invite and hierarchy state
  carrying whole events with the room version, and invalid stripped state refused.
  Lands with the invite and hierarchy work of steps 10 and 12.
- **State resolution v2 as a black box** (`TestMSC4297StateResolutionV2_1_starts_from_empty_set`,
  `..._includes_conflicted_subgraph`) — these drive Lattice from Complement's *own*
  federation server (`federation.NewServer` with `HandleMakeSendJoinRequests` and
  `HandleTransactionRequests`), so they exercise our client side and the spec's
  letter rather than our serving side. They go green with steps 9 and 10 together,
  and they are the first tests that tell us where our outbound client disagrees with
  an implementation that is not ours.
- **Media** (`TestMediaFilenames`, `TestMediaWithoutFileName`,
  `TestMediaWithoutFileNameCSMediaV1`, `TestRemotePngThumbnail`,
  `TestFederationThumbnail`, `TestContentMediaV1`) — `Content-Disposition` filenames,
  the client v1 media paths, and fetching from the owning server: step 13.
- **Client-side strays** (`TestSyncOmitsStateChangeOnFilteredEvents`,
  `TestRoomSummaryAllowedRoomIDs`, `TestIsDirectFlagLocal`, `TestIsDirectFlagFederation`,
  `TestJumpToDateEndpoint`) — the same gaps the csapi table lists, seen from a
  package that starts one container anyway. `TestJumpToDateEndpoint` is
  `/timestamp_to_event`, which step 11 serves for peers and the client API needs too.

**Deliberately left red** — these are decisions, not gaps, so do not "fix" them by
accident: `TestRemotePresence`, because presence is deferred past the first release
and remote presence stays red until that decision changes;
`TestJoinFederatedRoomFromApplicationServiceBridgeUser`, because application
services are out of scope; and anything that needs this server to be a **notary of
record** or to follow **SRV delegation**, both deferred.

## Deferred past the first release (decisions recorded 2026-11-01)

- **3PID / identity** (`account/3pid/*` changes, `requestToken`): stays
  routed-but-refuses-with-a-reason; no identity server integration.
- **SSO, `m.login.token`, OAuth**: password + registration-token auth is
  enough for a first release.
- **Application services**, **bridges**: out; `/thirdparty/protocols`
  stays empty.
- **Presence** (client push model stays as-is; no `m.presence` EDUs).
- **Policy Servers** (`/_matrix/policy/v1/sign`, spec v1.18): rare in the
  wild; revisit after federation proves out.
- **Deprecated** `initialSync` / `GET /events` / `PUT
/state/{type}/{key}/{txnId}` (Go's mux path-cleaning makes the empty
  state key form unroutable — decided, not laziness).
- **Moderation actions** on reports (dismiss/delete, per-report GET),
  **event retention** (`max_events`/`max_age` in maintenance), **search
  word-index/ranking**, **push-rule seed migration** (fresh installs make
  it moot), **per-device to-device watermark** (`ToDeviceRepo.Prune` still
  unused), **signing-key rotation** (`old_verify_keys`), **SRV delegation**
  and being a notary of record, **`/logout/all`-style extras**.
- **Video calls**: were "out"; they are planned now, in the section below.

## Video calls over LiveKit — the plan (added 2026-11-03)

Element Call puts media in a LiveKit SFU, and the SFU admits nobody who cannot show a
JWT signed with its secret. On Synapse that JWT comes from a second service,
`lk-jwt-service`, because Synapse cannot be made to sign LiveKit tokens. It does not
have to be a second service any more: MSC4195 was accepted and merged, and its
endpoints are **homeserver** endpoints —
`POST /_matrix/client/v1/rtc/livekit/get_token` and a federation twin. Lattice can be
the thing that signs. Then the deployment is Lattice plus one LiveKit instance, and
the wiring that service needs from Synapse — MSC4502 application-service scopes,
MSC4512 request proxying, an MSC3861 OpenID token exchange, an admin-API hop to ask
"is this user in this room" — is not needed at all, because the access token in the
request header already says who is asking and room membership is a row in this
database.

What an operator passes is three flags:

```sh
bin/lattice -livekit-url wss://livekit.example.org:7880 \
            -livekit-key devkey -livekit-secret <64 random chars>
```

Three rather than two, because the SFU trusts a token only for the shared secret that
signed it: no configuration makes a URL and a key enough. Everything else is derived.
The URL announced to clients is exactly the one typed, never whatever the container
reports for itself — that is the whole of `lk-jwt-service` issue #161. With the three
unset the feature is off: `/rtc/transports` answers an empty list and
`/rtc/livekit/get_token` answers `M_UNRECOGNIZED`, like every other endpoint here that
is not implemented. `LATTICE_LIVEKIT_URL` and its siblings work in a container for
free, the way every other flag does.

### What is settled, and whose job it is

Verified 2026-11-03 against the proposals and LiveKit's `config-sample.yaml`. Statuses
from the MatrixRTC-in-2026 material dated 2026-10-19: MSC4140 merged, MSC4354 merged,
MSC4143 merged, MSC4195 merged, MSC4196 merged as an application.

| Piece | What it puts on the homeserver |
|---|---|
| MSC4143 MatrixRTC core | `m.rtc.slot` **state** events (`state_key` = `{application}#{slot}`, `status` `open`/`closed`, an `application` and optional `encryption` object) gate what may run; `m.rtc.member` **room** events say who is in it. A member counts as joined only while their member event is still sticky, an open slot exists, and their room membership is `join`. |
| MSC4143 discovery | `GET /_matrix/client/v1/rtc/transports` (unstable twin `org.matrix.msc4143`) is how a server announces its SFU now. The `org.matrix.msc4143.rtc_foci` key in `.well-known/matrix/client` is the older route, and Element Call ≥ 0.24.0 (2026-08-18) stopped reading it — do not add it. |
| MSC4354 sticky events | A PDU carries a **top-level** `sticky: {"duration_ms": …}`, which the client asks for through a `sticky_duration_ms` query parameter on `PUT /send/{type}/{txn}`. The server clamps the duration to 1 h, times it from `min(received_ts, origin_server_ts)`, delivers unexpired ones in a `sticky.events` sync section with `unsigned.sticky_duration_remaining_ms`, applies no history-visibility check to them, and **pushes its own unexpired sticky events to joined servers**. |
| MSC4195 LiveKit transport | The homeserver signs the JWT **and ensures the LiveKit room exists**, creating it when needed. Grants are prescribed: `sub` is the derived participant identity, `video.room` the derived room name, `roomCreate` **false**, `roomJoin` true, `canPublish` true for local users, `canSubscribe` true, `hidden` false; `nbf` is now and `exp` is short, because self-hosted LiveKit cannot revoke a token yet. Users of other servers are served by relaying to that server's federation endpoint. |
| MSC4140 delayed events | The membership deadman switch: a client schedules a delayed `m.rtc.member` leave, and MSC4195 lets it delegate keeping that event alive to the homeserver, which then watches the SFU's participant webhooks rather than the client's connection. |
| MSC4196 the `m.call` application | The application type an ordinary client uses, and its default slot: `m.call`, slot `m.call#room`. |
| MSC4502 / MSC4512 | Application-service scopes and endpoint proxying: Synapse's way of letting the external service answer those endpoints. Named here so nobody integrates `lk-jwt-service` into Lattice by mistake — with the endpoints in this process, both are moot. |

Room names and participant identities are pseudonymised against the SFU, and both are
one hash:

```
livekit_room_name      = base64raw( SHA256( CanonicalJSON( [ room_id, slot_id ] ) ) )
livekit_participant_id = base64raw( SHA256( CanonicalJSON( [ sender, member.id ] ) ) )
```

`matrix.CanonicalJSON` exists already and `base64.RawStdEncoding` is the unpadded
form. Two rules to keep: never map two slots to one LiveKit room, and always map the
members of one slot to the same LiveKit room — that is what keeps the number of
connections proportional to the number of servers rather than of members.

Two things are pinned here because a review had them backwards, and backwards here means
an interop failure rather than a style difference. The client asks for a token with a
flat `member_id`, not with a `member` object carrying a `device_id`: that object shape
and the three-element identity hash around it belong to the legacy OpenID/`sfu/get`
flow the standalone service still supports. And issuance must not be gated on an open
slot or a live member event, because the MSC's own sequence has the client connect to
the SFU before it publishes the member event that records the connection — the check
belongs at the SFU's participant-joined webhook, with a short `exp` covering the gap.

### What is already in place

- `PUT /rooms/{roomId}/send/{eventType}/{txnId}` and `/state/{eventType}/{stateKey}`
  accept any event type, and `AppendEvent` builds the PDU itself, so `m.rtc.slot` and
  `m.rtc.member` already flow through this server today — as ordinary events.
- To-device messages are relayed, which is what `m.rtc.encryption_key` and the
  `m.per_member` media keys of MSC4143 ride on. Encrypted media needs nothing here.
- `github.com/golang-jwt/jwt/v5` is already a direct dependency: a LiveKit access
  token is an HS256 JWT, so signing one adds no dependency.
- MSC3861 `POST /user/{userId}/openid/request_token` and MSC3288
  `GET /_matrix/client/v3/voip/turnServer` are routed. The first means a separately
  deployed `lk-jwt-service` already gets most of what it needs from Lattice; it is the
  membership check that needs an application service, which is exactly the hop this
  plan removes.
- `power.DefaultState` is 50, so `m.rtc.slot` is already gated the way MSC4143 expects.

One thing is missing structurally: `EventDraft` is (room, type, state_key, sender,
content), so **there is no place for a top-level `sticky` object** and the current
event path cannot carry MSC4354 at all. That is the largest piece of work below.

### The steps (numbered L1.. so they do not collide with the release plan above)

Work them in the order **L1, L2, L3, L5, L4, L6**: everything up to and including L5
has to be reliable with one server and one SFU before cross-server token relays and
delegated leave add a second failure domain to every one of them.

#### L1. Announce the SFU and sign tokens for local users

- [ ] `internal/service/livekit.go`: the two derivations above, and `JoinToken`
      building the MSC4195 grant set. Pin the derivations against vectors from outside
      this repo, and they already line up: with `matrix.CanonicalJSON` and
      `base64.RawStdEncoding`, verified 2026-11-03,
      `["!roomid:example.com","slot1234"]` hashes to
      `O8437W3+jmzMVjoIP3tNwbm+XxHQk2iKpOA7aqw3qSc` (MSC4195's own example) and
      `["@alice:example.com","memberABC"]` to
      `M3VnsLXrkbxIDINXO64u8PZzFyD9ZYFiQULR2dshWYs` (the one the Rust service tests).
      A silent change to a room name puts the participants of one call in different
      LiveKit rooms, and the bug looks like a media failure.
- [ ] Take the request and response shapes from the current MSC and the current
      clients, not from an older snapshot: `room_id`, `slot_id`, `url`, `member_id`
      and an optional `server_name`, all at the top level — the flat `member_id` is
      what the JS SDK's `LivekitGetTokenRequest` and the reference service's request
      structs use. A `member` object carrying `device_id` belongs to the legacy
      OpenID/`sfu/get` flow, which is also why that service still carries a legacy
      identity built from `(mxid, device_id, member_id)` rather than the current
      `(mxid, member_id)` pair. Copying the legacy shape by mistake means a client
      sends one thing and the server hashes another.
- [ ] Validate the three flags at startup rather than at the first call: the URL must
      parse, a `ws://` URL is allowed but warned about, and an empty key with a set
      secret (or the other way round) is a start-up failure rather than a call that
      fails an hour later. The startup line that reports settings taken from the
      environment prints flag names — `livekit-secret` appears there, its value never
      does — and that is the rule to keep.
- [ ] Routes: `GET /_matrix/client/v1/rtc/transports` (plus the
      `unstable/org.matrix.msc4143/rtc/transports` twin) answering
      `{"transports":[{"type":"m.livekit","url":…}]}`, empty when unconfigured; and
      `POST /_matrix/client/v1/rtc/livekit/get_token` plus the
      `unstable/io.element.msc4195/rtc/livekit/get_token` twin that current clients
      call. Rate limit both like the other expensive writes.
- [ ] The rejections exactly as the MSC has them, and no more: requester not joined
      → 403 `M_FORBIDDEN`; `server_name` naming another server → 403 until L4 exists;
      `url` that is not the configured SFU → 400 `M_INVALID_PARAM`. Compare the URL
      after normalising scheme and host, not byte for byte. Do not invent further
      gates at issuance time: MSC4195's own sequence has the client get the token and
      **connect to the SFU before it publishes its `m.rtc.member` event**, so an
      issuance that demanded an open slot or a live member event rejects the join it
      is supposed to authorise. Slot and member validity are enforced where the MSC
      puts them — at the SFU's participant-joined webhook, in L5 — with the short
      `exp` bounding the window in between.
- [ ] Identity comes from the access token. The `member_id` in the body says which of
      the requester's own memberships to derive the participant identity for; the
      sender half of that derivation is the authenticated user, never anything in the
      request. Same rule on the federation endpoint, where `user_id` must match the
      origin server's name. A test that one user cannot obtain a token for another
      user's `member_id` is the one that keeps this true.
- [ ] `/versions`: advertise `org.matrix.msc4143` and `org.matrix.msc4195` in
      `unstable_features`, and only while configured. `org.matrix.msc4354` waits for
      L2 — advertising stickiness the server cannot yet deliver invites clients to use
      a path that silently drops their member events.
- [ ] `deploy/livekit/`: a compose file and a `livekit.yaml` whose `keys:` match the
      flags, `room.auto_create: true` for now, and the port list below. Flags without
      a runnable SFU are not easy, so this belongs in the step and not in a footnote.
- [ ] `tests/compat/livekit_test.go`: the transports body with and without
      configuration; a table over the get_token accept/reject matrix; the issued JWT
      parsed back and its grants asserted field by field; and the impersonation case
      above.
- [ ] Done when Element Call in a browser completes a two-user local call against
      Lattice and a real LiveKit.

#### L2. Sticky events (MSC4354) — what makes a call reliable

Skipping this looks harmless and then shows up as a call that looks empty to whoever
joins late: `m.rtc.member` reaches them only through the timeline window, and MSC4143
tells clients not to connect to the transports of a member whose stickiness they cannot
compute.

- [ ] `EventDraft.Sticky` carrying `duration_ms`, written into the PDU as `sticky`
      (unstable `msc4354_sticky` while that is what clients send). The hash covers it
      without a change: `hashWithout(event, "unsigned", "signatures", "hashes")`.
- [ ] `sticky_duration_ms` on the send endpoint (unstable
      `org.matrix.msc4354.sticky_duration_ms`), strictly positive, clamped to
      3 600 000; store the expiry next to the event, computed from
      `min(received_ts, origin_server_ts)` so a forged future origin timestamp cannot
      extend a membership. The expiry is a column and expiry is a query — not a map of
      Go timers — because the set of live memberships has to be reconstructable after a
      restart, a database restore, and a sync resuming from an old token, and a timer
      list is none of those things.
- [ ] Sticky lifetime is its own thing, not the timeline's: an event can be current in
      the sticky set while outside the synced timeline window, and expired from the
      sticky set while staying in history. Retention or pruning of the timeline must
      not be what decides whether a membership is live. And no dedupe by sender: the
      clients key memberships on `(sender, type, content.sticky_key)`, keeping the last
      to expire, so several devices of one user are several memberships and collapsing
      them by user ID empties a call when someone's second device joins.
- [ ] A `sticky.events` section per joined room in the sync response: deduplicated
      against the timeline, the RoomFilter's timeline filter applied **before** that
      dedupe, ignored users omitted, history visibility not applied, capped (100 is the
      suggested limit), each entry carrying
      `unsigned.sticky_duration_remaining_ms`.
- [ ] Federation: push this server's own unexpired sticky events to joined servers,
      including when a new server joins, and re-evaluate soft-failed unexpired sticky
      events when the authorising state changes. A per-sender cap (60 suggested), the
      oldest expired first, so one client cannot fill the send queue.
- [ ] Tests: expiry arithmetic and clamping as a unit table; a compat case that sends
      a member event, syncs with `timeline: {limit: 1}` and finds the event in
      `sticky.events`; cases for the dedupe and for an ignored sender; and one that
      restarts the server between the send and the sync, which is the case an
      in-memory design fails.
- [ ] Four landings rather than one commit: representation and persistence, then
      expiry and sync, then federation, then filtering and redaction with the compat
      cases. The PDU stays the source of truth throughout — an index for expiry, never
      a second copy of the event.

#### L3. Slots, and letting an ordinary member start a call

- [ ] Decide and record whether Lattice seeds an open `m.call#room` slot at room
      creation. `state_default` 50 means a member with power 0 cannot send `m.rtc.slot`
      today, and MSC4143 both keeps slots moderator-gated and recommends creating slot
      events at room creation where possible. Find out what Element Call sends in a
      room where the caller is a plain member before choosing.
- [ ] Nothing to do for room upgrades, and worth knowing before going hunting:
      `UpgradeRoom` (`internal/service/directory.go`) copies every state event except
      `m.room.create`, `m.room.member` and `m.room.tombstone`, so an open slot already
      survives an upgrade as MSC4143 recommends. A compat case that upgrades a room
      mid-call and reads the slot back in the new room would pin it.
- [ ] Check redaction of a sticky member event against what MSC4354 says: redaction
      strips `sticky_key`, so moderation by redaction works only for clients that
      remembered the event ID. A compat case, not a comment.

#### L4. Federation: tokens for users on other servers

- [ ] `POST /_matrix/federation/v1/rtc/livekit/get_token` (plus the unstable twin),
      behind the existing X-Matrix middleware: reject when `user_id` is not of the
      origin server, when this server is not in the room, or when that user is not
      joined; 400 when `url` is not one of ours. Relay a peer's 403 and 400 to the
      client, and turn anything else into 502 `M_UNKNOWN`.
- [ ] The client half: when `server_name` names someone else, sign the federation
      request, drop `server_name`, add `user_id`.
- [ ] Publishing from remote users: MSC4195 recommends allowing it, and the upstream
      service allows it by default. Allow, and name the flag here if it is ever
      narrowed.
- [ ] Worth understanding before writing it: this server's authorization can only ever
      cover this server's users and this server's SFU. A token for a foreign SFU comes
      back signed by the *other* homeserver, and that server decided the grants — the
      MSC says to accept external SFUs "with care" for exactly this reason. So there is
      no cross-server grant model to design here; there is a trust decision about which
      servers' signatures an SFU accepts, and `-livekit-url` naming one SFU keeps that
      decision small.
- [ ] Tests: a two-server case in `tests/federation`, including a banned user whose
      relayed request is refused, and a peer that answers malformed JSON or times out
      (502 `M_UNKNOWN`, nothing leaked). What goes back to a client is the peer's 403
      or 400 and nothing else: no signing material, no internal SFU address.

#### L5. Room creation, and getting rid of participants

- [ ] Create the LiveKit room before answering get_token — MSC4195 requires it to exist
      — then set `room.auto_create: false` in the example config. With auto-create on,
      any holder of any token can create rooms on the SFU, which is also why
      `roomCreate` must stay false in the grants. Make the creation idempotent: two
      users asking at once both find no room, and one of them will get an
      already-exists error back. That case continues after checking the room is the one
      expected; every other creation error is a failure, not evidence of a race.
- [ ] Remove the participant from the LiveKit room when a user leaves or is banned,
      with `revoke_token_ts` set to now. Removing a participant is not revoking their
      token: self-hosted LiveKit cannot revoke yet, so a removed participant can walk
      back in on the same unexpired JWT. That is why the enforcement point is the
      SFU's participant-joined webhook — re-check room membership on every join and
      remove again — and why `exp` stays short.
- [ ] Verify the webhook the way LiveKit signs it, not with a compare of a shared
      secret: the POST carries `Authorization: Bearer <JWT>` minted by the SFU from an
      API key, so it is parsed with the configured secret and the key checked against
      `-livekit-key` — which is exactly what the reference service's webhook receiver
      does. Handlers then have to be idempotent and safe against duplicate and
      out-of-order deliveries, and off the request-serving goroutines.
- [ ] Reconcile rather than trust: webhooks are not a durable log, so one missed
      delivery must not leave someone connected for the rest of the hour. Periodically
      list the SFU's participants and compare them against MatrixRTC state, removing
      participants whose Matrix membership or open slot no longer holds and reporting
      LiveKit rooms nobody is in. This belongs on the existing maintenance loop
      (`StartMaintenance`), not in the request path, and it is what makes a Lattice or
      LiveKit restart recover on its own.
- [ ] Tests for exactly the parts that are easy to get wrong: a participant removed
      after a leave who then reconnects with the same unexpired JWT is removed again
      (the interesting half — that `RemoveParticipant` works is the easy half); a
      webhook POST signed with the wrong key is refused; two simultaneous token
      requests for one room end with one room and two tokens; a call survives a restart
      of Lattice and a restart of the SFU.
- [ ] Decide the dependency here rather than by accident, and the answer is no: the
      official `github.com/livekit/server-sdk-go/v2` exists, and its `go.mod` requires
      moby/buildkit, containerd, pion/webrtc, cel-go, psrpc, grpc, protobuf, zap and a
      cgo `opus` binding — a few hundred modules for the two RoomService calls this step
      needs (`CreateRoom`, `RemoveParticipant`), and a C toolchain in the graph while
      the release image builds `CGO_ENABLED=0`. Hand-roll those two Twirp calls on the
      HTTP client and JWT signing already in the tree; the SDK becomes interesting only
      if the participant listing and webhook handling above start costing more than the
      dependency would.

#### M1. A local call that survives its own edges — the gate before federation

Acceptance for L1+L2+L3+L5, all of it on one server with one SFU:

- [ ] Two accounts join, publish audio and video, and see each other.
- [ ] A third account joining late sees the two already in the call — the case sticky
      events exist for.
- [ ] Leaving removes the participant; rejoining works; a ban removes the participant
      and a ban mid-call removes them while connected.
- [ ] Closing the slot ends the call for new joiners — enforced where the decision is
      made, in the participant-joined check, with the slot's open state as part of what
      that check requires — while issuance itself keeps behaving as L1 defines it.
- [ ] Restarting Lattice loses no live membership, because the expiry is in the row.
- [ ] Restarting LiveKit leaves Matrix state alone, and the reconciliation loop clears
      whatever the SFU lost.
- [ ] Killing the webhook delivery (point `webhook.urls` at a black hole) still ends
      the membership within the reconcile period rather than never.

#### L6. Delayed events, and delegating the deadman switch

- [ ] MSC4140's futures first, as their own service file: schedule a delayed event,
      restart it, trigger it. It is a general feature — scheduled messages want it too
      — and MSC4195's delegation sits on top of it.
- [ ] `POST /_matrix/client/v1/rtc/livekit/delegate_delayed_leave`, matching
      `delay_id` to an event this same user scheduled in this same room, one
      delegation per (room, slot, member, MXID), a later `delay_id` invalidating the
      earlier one.
- [ ] The same webhook handler L5 verifies and dispatches, extended to keep restarting
      the delegated event while the participant is connected and to send the leave when
      they are gone. Webhooks carry no delivery guarantee, so the reconciliation loop
      polls the SFU as well as listening to it.

### TURN, ports, and what the operator still runs

The answer to "do we have to integrate a TURN system as well" is no, and it is worth
writing down why not. LiveKit ships a TURN server and gives clients the credentials
itself, so Matrix is nowhere in that path:

- `turn: {enabled: true, domain: turn.example.org, udp_port: 3478, tls_port: 5349,
  cert_file/key_file or external_tls, relay_range_start/end,
  per_user_relay_allocation_limit, ttl_seconds: 300}` — embedded, and authenticated
  against the SFU's own signalling connection rather than against Matrix.
- A separate coturn instead: `rtc.turn_servers: [{host, port, protocol: tls, secret,
  ttl}]` on the LiveKit side, `use-auth-secret` plus `static-auth-secret` on the
  coturn side. LiveKit mints the time-limited credentials and hands them out; its
  sample config documents `secret_file`, a `ttl` defaulting to 4 h and capped at 24.
- Ports for the firewall: 7880 signalling (behind TLS), 7881 ICE/TCP, 50000-60000 UDP
  or a `udp_port: 7882-7892` mux, 3478/5349 for TURN. The URL `-livekit-url` announces
  is the TLS-wrapped 7880, not a media port.
- Lattice's `GET /_matrix/client/v3/voip/turnServer` — already routed, currently
  answering empty credentials — belongs to legacy 1:1 `m.call.invite` VoIP, which
  MSC4531 deprecates. Filling it from `-turn-*` flags is a separate optional item for
  whoever still runs those calls; it is not part of MatrixRTC and should not be
  presented as if it were.

"Provide the LiveKit instance and it all works out of the box" is honest once L1..L3
are done, with three things left on the operator's side: `keys:` matching
`-livekit-key` and `-livekit-secret`, `room.auto_create: false` from L5 on, and
`webhook.urls` from L6 on.

### Open, and needing a real deployment

- Token lifetime. The MSC wants a short `exp` because self-hosted LiveKit cannot
  revoke, while a call longer than the token needs a re-issue. Start at the hour the
  MSC's own example uses, then measure what Element Call actually asks for.
- Whether Element Call opens the slot as a plain room member (L3), and whether any
  client still reads `rtc_foci` from `.well-known` in practice: the guides disagree
  with each other, and an operator following either one gets a different answer.
- Which unstable prefixes current clients really send — `io.element.msc4195` for the
  token endpoints, `msc4354_*` for the sticky fields, `org.matrix.msc4143` for
  transports. Serve the stable path plus the twin that the client in the field calls,
  and record here when a twin is dropped.
- `make endpoints` counts "Extra routes" for anything missing from the v1.19 index, so
  these endpoints will move that number without anything being wrong. Regenerate the
  index when a spec release includes MatrixRTC.
- Multi-SFU: `transports` is an array and MSC4195 is designed around several SFUs per
  server and per room. One URL per server first; a repeatable `-livekit-url` is a
  decision for later, not a gap in these steps.
- Failure behaviour to define per step, since Matrix and LiveKit have separate state
  and separate failure domains: LiveKit down means token issuance fails and the
  homeserver keeps working; a Lattice restart recovers stickiness from the database;
  a lost webhook is healed by reconciliation; a federation peer being unreachable
  fails remote tokens and never blocks a local call. Nothing in this feature may make
  the homeserver itself unavailable when the SFU is.
- No metrics: Lattice has no metrics subsystem, and inventing one inside a feature
  step is a decision in disguise. Failures here surface as log lines naming the room,
  the slot and the pseudonymised participant — never a JWT, a secret, or a raw
  participant identity that a log could be correlated back to. If counters are ever
  wanted, they are their own step.
- Ringing and invitations for MatrixRTC sessions (MSC4075) are not merged and are not
  part of these steps.

## Complement — the upstream suite (running since 2026-11-01)

Tally, complete run of 2026-11-02 against Complement `e3fcff1`: **40 of 106**
client-API tests fail (the graph reads change nothing there), and **68 of 91**
server-server tests fail — **23 green**, where the run before the membership handshake
had 4 and the run before the graph-reading endpoints had 22. `TestEventAuth` is the new
one, and it is precisely the read a Synapse peer makes before it accepts an event.
`TestInboundCanReturnMissingEvents` is still red as a whole, but its `world_readable`
and `shared` subtests pass now that `get_missing_events` answers in the order the room
was built and honours `min_depth`; `joined` and `invited` wait on redacting what predates
the asking server. The rest of this section is how to run it, what the run can hide, and
what every remaining failure is waiting for.

A second run, 2026-11-04, against a freshly cloned Complement main (no `msc4186` package
exists upstream, so sliding sync itself is not measured there; the one upstream case that
reaches `POST /unstable/org.matrix.simplified_msc3575/sync` is `tests/msc4306`'s thread
subscription, which stays red because it asks for a section that needs MSC4306). The
whole `tests/csapi` package did **not finish in its 40 minute budget**: one of the two
servers of a two-server deployment sat at 90-100% CPU for the better part of an hour, which
is worth chasing for its own sake — it is a spin inside this server under one of the
federation tests, not a Complement problem. A focused group did run to completion:
`TestServerCapabilities`, `TestVersionStructure`, `TestFilter`, `TestSyncLeaveSection` and
`TestSyncFilter` green; `TestSync` red on presence and on state events appearing in a
`m.room.message` timeline, and `TestSyncTimelineGap` red on a ten-event federated batch
delivered without `limited` — all four known and unchanged by the sliding-sync work, which
adds two routes, one `/versions` flag and five repository reads and touches no existing
handler.

[Complement](https://github.com/matrix-org/complement) starts homeservers in
containers and drives them over the client-server API. It is a second opinion on
the client API: written by people who did not write this server, against the
specification, with the assertions the specification's wording implies — several
response shapes below were wrong here while every in-repo test stayed green.

```sh
make complement-image                     # bin/lattice with CGO off, then the image
make complement COMPLEMENT=~/complement   # the whole csapi suite, ~5 min
make complement COMPLEMENT_RUN='TestRoomAlias|TestRoomCreate'
make complement COMPLEMENT_PKG=./tests    # the server-server suite (91 tests)
make complement COMPLEMENT_PKG=./tests COMPLEMENT_RUN='TestJoinViaRoomIDAndServerName'
```

`COMPLEMENT` points at a Complement checkout; it is not a module dependency,
because Complement's own `go.mod` owns the test binaries. The image is
`localhost/lattice-complement:latest` (`COMPLEMENT_IMAGE`), built from
`deploy/complement/`: `Dockerfile` (alpine plus the binary) and `entrypoint.sh`
(signs a certificate for `SERVER_NAME` with the CA Complement mounts at
`/complement/ca`, trusts that CA, then serves 8008 for clients and 8448 for
federation).

Two harness facts that cost an hour each:

- **`CGO_ENABLED=0` is load-bearing.** The image is Alpine (musl); a dynamically
  linked binary inside it dies as `/usr/local/bin/lattice: not found` with exit
  code 127, and then every test times out against a server that never started.
  `make complement-image` therefore builds its own binary instead of trusting
  `make build`.
- **A Complement run can abort and still hand back a failure list.** Some runs
  panic inside Complement itself (`rooms_state_test.go:128` dereferences a nil
  response), `go test` stops, and every test after it simply never reports — so
  the `--- FAIL` count understates the truth. Count the `: Server logs:` blocks
  (one per deployment) before believing a tally: two runs in a row were read as
  "25 then 44 failures, improving" when both had aborted.
- **A fresh Complement checkout changes the tally on its own.** The checkout is
  not pinned — it is a clone, and its own tests move. The current runs use
  `e3fcff1`, which added a subtest (`Setting state twice is idempotent`, from
  sytest parity) that made a previously green test fail for a reason that had
  nothing to do with the last Lattice change. Diff the *names*, not the counts,
  and keep the checkout's commit next to the numbers.
- **Rootless podman fails a deployment now and then.** `Deploy: ... ContainerStart:
  Error response from daemon: rootlessport conflict`, and the test fails without
  Lattice ever being asked anything. One such failure per long run has been seen;
  re-run the named test before writing it down as a Lattice failure.

Tally, complete run of 2026-11-01 (`tests/csapi`, 106 top-level tests): **41
failing**. The complete run before the current round of fixes failed 43; that
round turned `TestGetRoomMembers`, `TestGetFilteredRoomMembers`, `TestRoomMembers`,
`TestRoomState`, `TestLogout`, `TestChangePasswordPushers` and `TestPublicRooms`
green and regressed nothing, and the round before that (the `r0` prefix rewrite,
body validation, filter validation, canonical-alias guards, Unicode aliases,
media reservation states, key-backup replace rules) took the aborted pair from 50
to 44.

The run of 2026-11-02 failed **40**. Two names left the list: `TestRoomCreationReports-
EventsToMyself`, which needed state writes to be idempotent — writing the state a room
already holds now answers with the event it holds rather than appending a copy of one
decision (`Service.unchangedState`, pinned by `TestWritingTheSameStateTwiceIsOneEvent`;
membership is excluded, because re-joining has to be a new event even when its content
matches) — and `TestProfileAvatarURL`, which was a rootless-podman port conflict and
went green on its own.

The server-server run of 2026-11-02, after step 10, is green for
`TestJoinViaRoomIDAndServerName`, `TestJoinFederatedRoomFailOver`,
`TestJoinFederatedRoomWithUnverifiableEvents`, `TestSendJoinPartialStateResponse`,
`TestCannotSendNonJoinViaSendJoinV1`, `TestCannotSendNonJoinViaSendJoinV2`,
`TestCannotSendNonLeaveViaSendLeaveV1`, `TestCannotSendNonLeaveViaSendLeaveV2`,
`TestBannedUserCannotSendJoin`, `TestUnbanViaInvite`, `TestOutboundFederationSend`,
`TestRemoteTyping`, `TestRemoteAliasRequestsUnderstandUnicode`,
`TestInboundFederationKeys`, `TestInboundFederationRejectsEventsWithRejectedAuthEvents`,
`TestUnknownEndpoints`, `TestWriteMDirectAccountData`, `TestLocalPngThumbnail` and
`TestSyncOmitsStateChangeOnFilteredEvents`. The client-API tally did **not** move when
the room presets did (a `public_chat` room no longer gets an `m.room.guest_access`
event), which is the check that the preset change was a fix rather than a regression.
One deployment failed in that run with a podman limit - `allocating lock for new volume:
allocation failed; exceeded num_locks (2048)`, from roughly two thousand anonymous
volumes left behind by earlier runs - so the named test was re-run alone and failed for
its real reason. `podman volume prune -f` between long runs is now part of the routine.

The named federation runs of 2026-11-03, after step 12, added six to the green side:
`TestRemoteAliasRequestsUnderstandUnicode`, `TestInboundFederationProfile` (including
the subtest that rejects a server name whose port is not a number),
`TestOutboundFederationProfile`, `TestFederatedClientSpaces`, `TestClientSpacesSummary`
(all five subtests: whole graph, `max_depth`, `suggested_only`, pagination, redacted
link) and `TestClientSpacesSummaryJoinRules`. The client-API package was run in full
alongside them and stayed at the same 40 failures, which is the check that the change
to the hierarchy's default depth and walk order was a fix and not a swing.

The runs of 2026-11-03 (step 17, knocking and restricted rooms) took the restricted group
whole: `TestRestrictedRoomsLocalJoin`, `TestRestrictedRoomsRemoteJoin`,
`TestRestrictedRoomsRemoteJoinLocalUser`, `TestRestrictedRoomsRemoteJoinFailOver` and the four
`...InMSC3787Room` twins, next to the knock group that went green in the same step. Came with them:
`TestFederationRejectInvite`, `TestFederationThumbnail`, `TestJoinFederatedRoomWithUnverifiableEvents`.
What is left in that neighbourhood is the space summary a restricted join is authorised through
(`TestRestrictedRoomsSpacesSummaryLocal/Federation`), room version 12
(`TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV12`,
`TestKnockRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV12`,
`TestComplementCanCreateValidV12Rooms`), and `TestFederationRoomsInvite`.
What the remaining federation failures wait for, by group: `make_knock`/`send_knock` (knock and its
guards), the invite endpoint `PUT /v1|v2/invite` (`TestFederationRoomsInvite`,
`TestFederationRejectInvite`), the graph-reading queries `event_auth`, `state_ids`,
`state`, `get_missing_events`, `backfill`, `timestamp_to_event`
(`TestEventAuth`, `TestGetMissingEventsGapFilling`, `TestInboundCanReturnMissingEvents`,
`TestJumpToDateEndpoint`, `TestCorruptedAuthChain`), device-list
queries (`TestDeviceListsUpdateOverFederation*`),
media federation (`TestContentMediaV1`, `TestFederationThumbnail`), room versions
this server does not speak (`TestComplementCanCreateValidV12Rooms`), and the ACL tests
(`TestACLs`, `TestACLsForEDUs`) which need a room two servers share — which now works —
and the EDU paths that check it. Spaces, the hierarchy route and the profile queries came
off that list with step 12; the count is not restated here until the whole federation
package is run end to end again, which is step 14's first task.

The 40, grouped by what is actually missing — each group is a work item, and the
suite's own skips are not hiding any of them:

| Missing | Tests |
| --- | --- |
| `unsigned.transaction_id` on the local echo of a `PUT /send` (`/sync`, `/event`, `/messages`) — the transaction log already stores it | TestTxnInEvent, TestTxnScopeOnLocalEcho, TestTxnIdWithRefreshToken |
| History visibility on `GET /rooms/{roomID}/event/{eventID}`: refuse non-members, and events from before the join — **addressed by step 23**, unverified against the suite since; `TestLeaveEventVisibility` may still be red, because a user who has left reads nothing here (see step 23's openings) | TestFetchEventNonWorldReadable, TestFetchHistoricalJoinedEventDenied, TestFetchHistoricalInvitedEventFromBeforeInvite, TestLeaveEventVisibility |
| Sync `leave` sections and timeline gaps: a left room reappearing with its old history, `limited` + `prev_batch` on a gapped timeline, room summaries for rooms with no timeline | TestSync, TestSyncTimelineGap, TestGappedSyncLeaveSection, TestOlderLeftRoomsNotInLeaveSection, TestLeftRoomFixture, TestArchivedRoomsHistory, TestRoomSummary |
| `lazy_load_state` in `/messages` | TestRoomMessagesLazyLoading, TestRoomMessagesLazyLoadingLocalUser |
| Presence — deferred on purpose (no `m.presence`), so these are expected red | TestPresence, TestPresenceSyncDifferentRooms, TestMembersLocal |
| Device list updates: `GET /users/{id}/sync` and the `device_lists` edges they feed | TestDeviceListUpdates, TestInviteFromIgnoredUsersDoesNotAppearInSync |
| Threads and relations: `/threads`, `/relations` pagination, threaded receipts, MSC4102 | TestThreadsEndpoint, TestRelationsPagination, TestRelationsPaginationSync, TestThreadedReceipts, TestThreadReceiptsInSyncMSC4102 |
| Room-specific member display names: writing your own `m.room.member` through the state endpoint, and the user directory and room summary that read it | TestRoomSpecificUsernameAtJoin, TestRoomSpecificUsernameChange |
| `GET /rooms/{roomID}/members?at=<sync token>` — membership at a sync position | TestGetRoomMembersAtPoint |
| Push rules in the first sync as **`m.push_rules` account data** (the `pushrules` key is served now), and a rule change waking an incremental sync | TestPushSync |
| Room forget: `/forget` then reading the room, and the sync `leave` section that follows it | TestRoomForget |
| `/messages` with a filter (chunk contents) | TestRoomImageRoundtrip |
| `m.room.power_levels` writes in a privileged-creators room version (MSC4289): the creator in `users` | TestPowerLevels |
| `/search` results across a room upgrade | TestSearch |
| `/keys/upload` refusing a bad one-time key with 400, and `/keys/query` for the device behind it | TestUploadKey |
| Registration edges: `/register/available` wants 400 `M_USER_IN_USE` for a taken name (Complement's expectation, not Synapse's 200 `available: false`), and a UIA body without the `session` the server issued wants 401 | TestRegistration |
| MSC4115 `unsigned.membership` on served events | TestMembershipOnEvents |
| URL previews (`GET /_matrix/media/v3/preview_url`) — unimplemented, and this test does not skip on `M_UNRECOGNIZED` | TestUrlPreview |
| Two-server behaviour, which needs inbound PDU processing and joins (steps 9–10) | TestMessagesOverFederation, TestPushRuleRoomUpgrade |

The cheapest three, in order: `unsigned.transaction_id` (the transaction log
already has the value, the event just has to carry it into `unsigned`), history
visibility on `/event/{eventID}` (the visibility helper exists, the endpoint does
not consult it), and `lazy_load_state` in `/messages`.

**The server-server suite is a different package and a different yardstick.** The
package is `./tests` — package `tests`, **91 tests**: the `federation_*` tables
plus knock, restricted rooms, room hierarchy, media and v12. There is no
`./tests/federation`.

Tally, first complete run of 2026-11-01: **88 of 91 fail**, and **87 after the
key-document fix** recorded under step 8 (`TestInboundFederationKeys` went green).
The three that passed before that: `TestUnknownEndpoints` (an unknown federation
path answers 404 `M_UNRECOGNIZED`, which is the right answer where there is no
route), `TestLocalPngThumbnail`, `TestWriteMDirectAccountData`.

Two facts from that run, which decide the order of steps 9 to 13:

- **The transport is not the problem.** The failures are `make_join` and friends
  answered `M_UNRECOGNIZED` by the *peer* Lattice — signed requests reach the
  other container over TLS on 8448 and come back with "no such route". Discovery
  (`/.well-known/matrix/server`, then `https://<name>:8448`, SRV skipped), the CA
  the entrypoint installs, and key fetching already work.
- **Nothing is seeded.** The in-repo `tests/federation` cluster injects peer URLs
  and inserts `Room` and `RoomMembership` rows directly, which is why it proves
  the transport and the DAG plumbing but has never proved that a room can be
  *joined* across two servers. Complement starts two containers of this image and
  reaches a client-visible assertion only if the protocol actually ran — so a
  federation test going green is evidence, and an in-repo one is not.

The entrypoint starts Lattice with `-federation` and a key file, and signs a
certificate naming `SERVER_NAME`, so federation is on in every deployment and a
federation failure reads as a protocol failure rather than a certificate one.

## Open (need a human or network, not a code step)

- A real **Element click-through** against a running Lattice (the whole key
  setup chain is chain-tested, but "does the browser stop complaining" is
  unconfirmed; watch the interactive-auth log lines README describes).
- `internal/coverage/SPECS.md` references a spec corpus that is **not** in this workspace;
  anything needing exact spec wording should quote the matrix-spec tag it
  was read from (e.g. `cross_signing.yaml` @ v1.19) or fetch the corpus.

## Verified facts and gotchas (condensed — do not re-derive)

### Layout and symbols

- Module `github.com/enimatek-nl/lattice`, Go 1.26. `matrix.Event` field is
  **`ID`**; `persist.Room` field is **`ID`**; `persist.Event.Matrix()`
  returns a **value**. `persist.Event` has `Depth` but no
  `prev_events`/`auth_events`/raw-JSON columns (step 7 adds them); it does
  denormalise `RelatesTo`/`RelType` from `m.relates_to` via
  `service.relationOf` (with `m.in_reply_to` fallback → `m.in_reply_to`).
- Repos are `db.XRepo` fields; key ones: `EventRepo` (`Relations`,
  `ThreadStats`, `ThreadsFor`, `ByPositions`, `GetByIDs`, `LatestActivity`,
  `Search`, `HistoryByTimestamp`), `DeviceKeyRepo`,
  `OneTimeKeyRepo` (conditional `Claim` → a prekey is handed out once),
  `DeviceListRepo`, `ToDeviceRepo` (`Pending(userID, deviceID, since, 100)`,
  unused `Prune`), `TxnRepo` (see `replayTxn` in `service/events.go`),
  `TypingRepo.ActiveUserIDs(roomID, except, nowMS)`.
- `internal/matrix/canonical.go` has the full JSON-signing layer:
  `CanonicalJSON`, `DecodeCanonical`, `SignEd25519`,
  `VerifyEd25519Signature`, `Ed25519KeyID`, `SignatureKeyID`, unpadded-base64
  helpers. Go's `json.Marshal` is **not** canonical (escapes `<>&`, floats
  numbers).
- `internal/realtime` hub wakes long-polling syncs; `service.Sync` keeps a
  500 ms fallback tick. `api.New(svc, authStore, api.Config{…})`,
  `app.Registered()` feeds the coverage tool; a catch-all answers
  404 `M_UNRECOGNIZED` and restores 405+`Allow` by matching
  `app.Registered()`.
- Pure rule packages cut out of the service layer so `tests/unit` can drive
  them (and federation can reuse them): `internal/power` (level arithmetic —
  `service.powerLevels` only reads the event and calls `power.ApplyDefaults`, and
  `power.EventRequired` expects levels that already have the defaults restored),
  `internal/authcheck` (the room version's authorization rules, and the auth-event
  selection: `Check` is the whole list, `Rules` is the list without the rule that
  reads the sender's own `auth_events`, which is what `stateres` needs),
  `internal/stateres` (state resolution v2, over an `authcheck.Graph` and state
  snapshots), `internal/push` (rule precedence and matching — `service.PushRuleKinds`
  is `push.Kinds`, `notifyParticipants` only gathers inputs), `internal/coverage`
  (the SPECS.md matcher and report — `cmd/endpoints` is a thin `main`).
  `.gitignore` used to carry `coverage.*`, which silently matched
  `internal/coverage/coverage.go` and would have kept the package out of the
  repository: the rule is now `coverage.out` / `coverage.html`, do not widen it
  back. There is no file named `coverage.go` in that package for the same
  reason.
- Auth: JWT HS256 with per-device `jti`; `auth.Require`/`Optional`/
  `RequireAdmin`, `auth.GetUserID/GetDeviceID/GetToken/IsAdmin`,
  `api.requireSelf(w, r)` gates personal-data writes.

### Behaviour already handled — do not "fix" again

- `GET /members` defaults `membership=join` (`*` = all); default invite PL
  is 0; PL changes need sender ≥ max(old,new) per user; `m.room.create` and
  `m.room.member` writes are guarded (`stateguards.go`); `m.room.create`
  and `m.room.encryption` are fixed once written; txn replay runs **before**
  these guards.
- Tombstoned rooms reject sends (even the creator), cannot upgrade twice,
  and the upgrade raises `events_default`/`invite` to `max(50,
users_default+1)`; tombstone carries `replacement_room`, successor's
  create carries `predecessor`; upgrade requires `new_version`.
- Sync: `device_one_time_keys_count` on every authenticated sync;
  `device_lists` only incremental, filtered to shared rooms, `left` always
  empty; **an invitation that already exists must not wake a long poll**
  (only an event written after the token may); a device list change is
  never echoed to the device that caused it; an abandoned long poll writes
  **no body and no log line**; **an idle sync waits out the timeout and answers
  with the token it was given** (`TestSyncIdleWaitsForTheTimeout` — answering at
  once turns every client into a busy loop, which is what an upload storm looks
  like in an access log); `Event.IsState` carries empty-state-key state
  explicitly (backfilled on migrate) and every state event serialises
  `state_key` even when it is `""`.
- Cross-signing: `usage` accepted as `self-signing` AND `self_signing`
  (rust crypto), objects stored byte-for-byte; replacement only after the
  `m.login.password` UIA stage; first upload and repeat-of-stored need no
  exchange; replacing deletes the old ids **and** their device signatures;
  `/keys/query` answers cross-signing keys in `master_keys`/
  `self_signing_keys`/`user_signing_keys`, never as devices; `POST
/keys/upload` reads `device_keys` then `device_key`; `/keys/signatures/
upload` accepts the bare-map, `signed_users` and `signed_keys` shapes;
  key backup versions are **strings**, PUT creates (POST 405), wrong version
  400 `M_WRONG_KEY_VERSION`, deletes are hard deletes.
- `PUT /sendToDevice/{eventType}/{txnId}`: the per-device value **is the event
  content** — there is no `type`/`content` wrapper, the type comes from the path.
  `m.room.encrypted` is therefore **accepted**: it is the Olm envelope, and that
  request is how every client hands a Megolm key to another device after claiming
  its one-time key (`refusesOnTheToDevicePath` still bans the other `m.room.*`
  types). The content is stored and delivered byte-for-byte, `ciphertext` and the
  client's `org.matrix.msgid` included — Lattice never reads it. Third Element
  thread, 2026-11-01: refusing it spent the claimed key and left the recipient with
  "the sender's device has not sent us the keys for this message" forever.
  `TestSendToDeviceOlmEncrypted`, `TestSendToDevice`.
  **Known divergence, not fixed:** a wildcard send to one's own user is echoed back
  to the sending device (`Pending` matches `target_device IN (device, "*")` with no
  sender exclusion), while Synapse drops the sender's own device. Delivery to the
  *other* devices of the same user — what multi-device key sharing needs — works.
- Device one-time keys are opaque and claimed once; fallback keys are
  repeat-claimed, never counted, filed under their arriving key id's
  algorithm (`fallback_curve25519`). **An upload adds to the pool**: a client
  counts the keys on offer (upload response + every sync's
  `device_one_time_keys_count`) and tops up to 100, so replacing the pool makes
  Element Web POST `/keys/upload` after every sync and throw away keys another
  device was about to claim (`OneTimeKeyRepo.Add`; `MaxOneTimeKeys` = 100 bounds
  it; re-uploading a claimed key id never revives it — `claimed_ts` is not in the
  conflict update). Synapse is the reference here: the count in its upload response
  covers the keys "previously persisted", a repeated upload is a no-op
  (matrix-org/synapse#2053), and the key object is stored and returned as uploaded
  with `fallback` and `signatures` intact. **Store the key object byte-for-byte**: the claimer verifies
  the signature the uploading device made over that object, so normalising
  `{"key":…, "signatures":…}` into `{"signed_curve25519":…}` hands out an
  unverifiable key — no Olm session, "Unable to decrypt" on the other side. Those
  two bugs together were the Element Web upload storm plus undecryptable messages
  (2026-11-01). Pinned by `TestOneTimeKeyTopUpKeepsThePool`,
  `TestOneTimeKeysComeBackAsUploaded`, `TestClaimedOneTimeKeysSurviveTopUp`, and
  the open-time repair of databases written before the fix by
  `TestUpgradeDropsUnverifiableOneTimeKeys`.
- Media: **two flows, both complete** (`create`→`PUT upload`→`commit`, with
  `-media-reservation-window`; reservations are unserved placeholder rows,
  404 not empty 200); inline in SQLite; PNG thumbnails via stdlib.
- Routed-but-refuse policy: unrouted paths are worse than honest refusals —
  3pid reads answer real empty lists, mutations answer `M_FORBIDDEN` with a
  reason; `POST /invite` refuses `third_party_signed`; `GET /login`
  advertises `m.login.password` only; `GET /v1/auth_metadata` 404 on
  purpose; `GET /room_keys/version` 404 until a backup exists;
  `POST /logout/all` keeps the calling token valid.
- `POST /register` (and GET) with no auth → 401 with `flows`/`session`
  (`m.login.dummy`, or `m.login.registration_token` with
  `-registration-secret`); guests skip; the challenge payload rides in
  `matrix.Error.Extra`. `/.well-known/matrix/client` nests
  `{"m.homeserver":{"base_url":…}}`.
- `/v1/capabilities` advertises room versions 1–11 as `stable`/`unstable`
  words, default 11; one set of state/redaction rules for all versions
  (until federation forces per-version rules — see step 7).
- Stripped state = inviter + invitee memberships only, plus create, join
  rules, aesthetics, encryption; **must include `m.room.create`**.
- `m.room_versions` default room version and `m.federate: false` currently
  in `writeInitialState` — flips with `-federation` in step 10.
- Shapes the upstream suite pinned (2026-11-01), all of them spec answers
  rather than Complement inventions: the `GET /rooms/{roomID}/members` chunk
  is the **member events** in client format (each carries `type`, `state_key`,
  `content`, `room_id`), not a wrapper of `user_id`/`membership` keys; `GET
/rooms/{roomID}/state` answers with a **bare JSON array** (both compatibility
  harnesses now keep an array body in `Response.Array`); `m.room.canonical_alias`
  stores the **full alias**, so `createRoom`'s `room_alias_name` is normalised
  before the event is written; an `m.room.member` event keeps the client's own
  content keys (`MemberContent.Extra`), which is how a join with custom content
  survives; the first `/_matrix/client/versions` answer names its
  `default_room_version`; the first `/sync` carries `pushrules`.
- Session end: `POST /logout` ends the device behind the token (its sessions and
  its device keys go with it), and `POST /logout/all` ends **everything**, the
  calling token included — a device list full of signed-out devices is what the
  other reading produces. A password change deletes the pushers **of the other
  devices**, so a pusher records the device that registered it (the body's
  `device_id`, else the device of the access token that made the request).

### Federation ground facts (verified against spec v1.19, 2026-11-01)

- PDUs/EDUs ride Transactions over `PUT /_matrix/federation/v1/send/{txnId}`
  — max 50 PDUs / 100 EDUs; no new txn to a destination until the previous
  got 200. Requests carry `Authorization: X-Matrix
origin=,destination=,key=,sig=` signing `{method, uri, origin,
destination, content}`; `uri` = full path + query, never the host; server
  names in the signature ignore delegation.
- Keys: self-signed `GET /_matrix/key/v2/server` (`valid_until_ts` capped
  at 7 days), notary `POST /_matrix/key/v2/query` /
  `GET /_matrix/key/v2/query/{serverName}`. Discovery:
  `/.well-known/matrix/server` (`m.server`) → port 8448. Unknown federation
  endpoints answer 404 `M_UNRECOGNIZED` (already Lattice's contract).
- Sinks for EDUs and primitives for signing all exist locally (see facts
  above); what is missing is the DAG storage, server signing key, and
  reference-hash event ids (steps 6–7).

### Testing harness (tests keep getting these wrong)

- Paths: room account data is
  `/user/{userId}/rooms/{roomId}/account_data/{type}` (no
  `/rooms/…/account_data` shortcut); room summary is **v1**
  (`GET /_matrix/client/v1/rooms/{roomId}/summary`); media: unauth
  `/_matrix/media/v3/…`, auth `/_matrix/client/v1/media/…` (only that one
  takes a trailing `/{fileName}`); aliases in test paths must be escaped
  (`%23wonderland:test.server`).
- Helpers: `sendTyped`, `eventsOf`, `chunkBodies`, `oldestEvent`,
  `eventOfType`, `testPNG`, `pngBounds`, `mustMediaID`,
  `actionsOf(...).notifies()/.hasTweak()`, `lenList`, `lenPushers`,
  `toDeviceOf`, `typingEvents`, `decodeReports`, `server.DoRaw`,
  `server.MustSync`, `Response.Int/String/ErrCode`. Keys: `fakeDeviceKey`,
  `oneTimeKeys` (algorithm-named shape), `elementOneTimeKeys` (`key` +
  `signatures` shape), `keyCounts`, `claimKeys`, `keyIDOf`, `keyObjectOf`,
  `keyIDs`. `server.Sync`/
  `SyncSince` do **not** update `session.NextBatch` — assign it by hand;
  `Response` has no `Sync()` helper (`json.Unmarshal(resp.Raw,
&matrix.SyncResponse{})`); time travel via `newClockServer(t, clock)` +
  the mutex clock in `maintenance_test.go` (give the token service a long
  TTL there); logs silenced unless `LATTICE_TEST_LOGS=1`.
- SQLite `:memory:` gives per-connection DBs — `NewDatabase` rewrites to
  `file::memory:?cache=shared` + pins one connection; the harness uses a
  file in `t.TempDir()` instead.
- **There is no schema version.** `NewDatabase` (in `internal/persist/persist.go`,
  not `db.go`) runs `AutoMigrate` plus the open-time repairs —
  `backfillStateFlags`, `sweepUnsignedOneTimeKeys` — on **every** open, so a
  repair has to be idempotent and cheap. That also makes an upgrade testable
  without a fake old binary: open a file, write the old-shaped rows through a
  repo, `Close()`, `NewDatabase` the same path, assert (`TestUpgradeDropsUnverifiableOneTimeKeys`).
  `db.GORM()` is the escape hatch for rows a repo will not show.

### GORM / mux / SQLite gotchas

- Every `Count` chain needs `db.Model(&X{})`. `clause.OnConflict` names
  **snake_cased columns** (`push_key`, `app_name`, `sent_ts`,
  `fully_read_event_id`); the `Where().Assign().FirstOrCreate()` form is
  banned (struct leaks into WHERE). `PushRuleRepo.Get`/`PusherRepo.Get`
  return a **non-nil zero row** + `ErrRecordNotFound` — branch on
  `persist.IsNotFound(err)`, never `x == nil`. `ReadMarkerRepo.Get` hands
  back a zero row; `service.SetReadMarker` sets the ids itself and also
  writes `m.fully_read` room account data.
- Deletes that free a unique index must be `Unscoped()` hard deletes: push
  rules, pushers, memberships (forget), aliases, profile fields, devices,
  sessions, key backups + sessions, cross-signing keys.
- mux: empty state keys arrive as a trailing `/` (both patterns
  registered); push rules served by one subtree handler per method skipping
  the deprecated `global` scope; wildcard names must match at the same
  position.
- Receipts never move backwards (`PostReceipt` keeps the newer position).
  Read markers accept the spec spellings `m.fully_read`/`m.read` plus the
  legacy ones.
- Push rules: new accounts seeded with `.m.rule.message` +
  `.m.rule.encrypted_room_one_to_one`; every message notifies, only
  mentions highlight; `.m.rule.room_one_to_one` fires first in 2-member
  rooms. Rule placement uses `PushRulePlacement{Before, After}`, moves
  renumber from 0.
- To-device: unique `(sender_id, txn_id, target_id, target_device)`; the
  on-conflict update **excludes `stream_pos` and `ts`** (a retry must not
  jump a device's timeline); delivery is positional after the sync token;
  `m.room.*` types → 400; unknown recipients skipped silently.
- Sync room summary: `matrix.JoinedRoom` carries `Heroes`/`
joined_member_count`/`invited_member_count` (`*int64`), built by
  `service.syncRoomSummary` (cap 5).
- Search = escaped `LIKE` over serialised content, newest first, joined +
  invited rooms only; `event_fields` projection returns `matrix.Object`
  (a struct would emit `null`s); `filter.rooms` checks room by room (403,
  not a silent gap).
- Hierarchy walk: breadth-first `m.space.child`, default depth 1,
  `next_batch` = offset, unreadable/unknown child sets `incomplete_chain`;
  `children_state` holds the space's own child events.
- Reports persist (`persist.Report`, copies the event's content at report
  time — redaction cannot hide it), newest first; admin listing clamps
  limit to 100 (max 1000); all three report endpoints exist.
- Housekeeping (`RunMaintenance`): prunes to-device (24h), txn ids (24h),
  stale typing (30s grace); `StartMaintenance` returns one stop fn that
  cancels **and** waits; admin trigger: `POST /_synapse/admin/v1/
maintenance`. First registered account = administrator.
- Typing rows carry `ExpiresTS`; `ActiveUserIDs` filters expired rows,
  `Recent` deliberately does not (expired state still emits a "nobody is
  typing" `m.typing`); `handleTyping` uses the token user but 403s when the
  path `{userId}` differs.

### Packaging, the image, and the release tag (added 2026-11-03)

- **Every flag is an environment variable already**: `LATTICE_` + flag name,
  upper-cased, dashes to underscores (`internal/config/env.go`). Nothing registers a
  name per setting, so a new flag in `cmd/server/main.go` is configurable in a
  container the moment it exists. Precedence is **command line > environment >
  default**; a variable set to blank counts as unset (compose writes `KEY=` for a key
  left blank), and `-version` is excluded — it is a question, not a setting, and an
  orchestrator carrying `LATTICE_VERSION` for its own bookkeeping would otherwise turn
  every start into a printout. What arrived from the environment is named at startup.
  `tests/unit/configenv_test.go` pins all of it; note that the flag package leaves a
  numeric or duration value at **zero** after a failed `Set`, which is why a bad
  variable stops the start rather than being logged and carried on.
- **Two images, deliberately.** `Dockerfile` (root) is what a release publishes:
  built inside the image, no entrypoint script, configuration through the
  environment. `deploy/complement/Dockerfile` is the test image: binary built outside
  (that image is rebuilt for every suite run) and a shell entrypoint that mints the CA
  certificate. `.dockerignore` keeps `bin/` and `deploy/` in the context so
  `make complement-image` keeps working.
- **The image runs as uid 10001** and owns `/data`. A *bind* mount keeps the host's
  ownership, so an unprivileged container answers `permission denied` opening the
  database unless the host directory is `chown 10001:10001`'d; named volumes need
  nothing. Verified end to end: register + whoami against a named volume, WAL files
  and media included, health route answering.
- **Cross-architecture**: the build stage is `--platform=$BUILDPLATFORM` and reads
  `TARGETOS`/`TARGETARCH`, so the Go compile stays native even for an arm64 image
  (checked: static aarch64 binary built on amd64). Only the Alpine runtime stage needs
  emulation, for `apk` and `adduser`; `docker/setup-qemu-action` registers it in CI.
  Rootless podman without binfmt cannot build that stage for a foreign platform —
  local `make image` builds for the host's architecture, which is the point.
- **A tag is the version**: the release job compares `GITHUB_REF_NAME` with
  `LatticeVersion` in `internal/service/capabilities.go` (a `-rc` suffix is allowed),
  because an image tagged `0.2.0` that answers `0.1.0` on `/_matrix/client/versions`
  costs a week of debugging aimed at the proxy. `v0.1.0` publishes `0.1.0`, `0.1` and
  `latest`, for `linux/amd64` and `linux/arm64`. Do **not** add an explicit
  `type=raw,value=latest` to the metadata step: `latest` comes from that action's auto
  flavour, which is what keeps it off a prerelease — `v0.2.0-rc1` publishes only
  `0.2.0-rc1` and moves nothing (verified against the action's own test table).

### Account management from the terminal (added 2026-11-03)

- **One binary, two halves.** `cmd/server/main.go` looks at `os.Args[1]`: a flag starts
  the homeserver (every existing start form — README, Makefile, container entrypoint —
  begins with a flag), a known word runs an operator command from `internal/cli`. A word
  that is *not* a known command is refused with the list, **not** treated as a plain
  `lattice`: `lattice adduser` typoed would otherwise open a listening port and write a
  fresh `lattice.db` into whatever directory the operator was standing in. `cli.IsCommand`
  is the single classifier; `tests/unit/cli_test.go` pins which words are commands.
- **The service layer has one account path each way**, and both halves use it:
  `service.CreateAccount(ctx, AccountParams)` (`internal/service/account.go`) is what the
  HTTP `Register` calls *and* what `add-user` calls, so a CLI account gets the same bcrypt
  cost, the same default push rules, the same guest and first-account-admin rules. On the
  way out, `service.DeactivateLocalAccount` (`internal/service/devices.go`) is the
  unauthenticated operator path; `DeactivateAccount` is the user-interactive-auth endpoint
  wrapper around it. Do not add a second place that creates or tears down an account.
- **Go's `flag` stops parsing at the first positional**, so `add-user alice -db path` read
  `-db`, the path and everything after them as more account names. `inOrder` in
  `internal/cli/cli.go` moves a command's flags ahead of its names, using the flag set's
  own knowledge (`IsBoolFlag`) to know whether a word after a flag belongs to it, and puts
  a typed `--` back in front of the names so a name that looks like a flag stays a name.
  It is unexported on purpose: pin the behaviour through `cli.RunWithEnv` with the name
  first, which is what every test does.
- **A password is asked for, never read from the environment.** `-password`, `-admin`,
  `-display-name`, `-yes`, `-limit`, `-all` are excluded from the `LATTICE_*` mapping in
  `internal/config/env.go`: a `LATTICE_PASSWORD` in a container's environment would become
  the password of whichever account somebody adds next, and `LATTICE_ADMIN` would make it
  an administrator. The exclusion is a table case in `configenv_test.go`. On a terminal the
  password is read twice with the echo off (`golang.org/x/term`); in a pipe it is one line,
  and an empty line is refused with the three ways to supply it. A blank `-password ""`
  means "not given" (same blank-is-unset rule as everywhere else).
- **Validate before asking for a secret**: `add-user` checks the name is one this server
  can hold *before* prompting, so nobody types a password twice for a name that was never
  going to work. It also warns when a capitalised name will be stored lower case, and
  trims spaces (a keyboard allows both; a client's JSON body does not).
- **Deactivation, not deletion.** `remove-user` clears sessions, device keys,
  cross-signing keys, key backups and room-key backups, forgets the memberships, and marks
  the row: the ID stays taken on purpose, because it is written into every event that
  account ever sent and a freed name would inherit them. `add-user` refuses to reuse it,
  and says so. Deactivated accounts drop out of `UserRepo.List`, so `users` counts them
  with a separate query (`deactivatedCount`, through `db.GORM()`); a list that showed only
  sign-in-able accounts would leave an operator hunting for one that is merely gone.
- **Confirmation is for a terminal, not for a pipe.** `remove-user` asks for the user ID
  back; with no terminal on stdin nothing is asked and the command proceeds, because
  `echo x | lattice remove-user` and `docker exec` without `-t` have no way to answer. `-yes`
  is the explicit script form. Getting this backwards either blocks every pipeline or skips
  the only check in front of a destructive command.
- **Both commands work against a running server, on the same file**, and that is the point
  (`tests/compat/operator_test.go` runs a live server and a second `persist.Database`
  handle side by side). Users and sessions are rows the middleware reads per request, so an
  account created by another process signs in immediately and `remove-user` kills a token a
  live server had already accepted — no restart, no shared state. SQLite WAL plus the busy
  timeout in `persist.NewDatabase` is what makes the second writer safe.
- **In a container the commands need no flags**: the image sets `LATTICE_DB` and
  `LATTICE_SERVER_NAME`, so `docker exec -it lattice lattice add-user alice` finds the
  right file and the right name. Verified by hand: closed registration, account added with
  `docker exec`, login from the host as `@sanne:matrix.example.org`, removal, refusal to
  reuse the name.
- **Output plumbing**: `say`, `sayLine`, `sayText` in `internal/cli` are the only writers
  and they drop fmt's error deliberately (a pipe closed by `| head` changes nothing about
  what reached the database); that keeps errcheck quiet without `_ =` noise at each site.
  Error paths print one sentence, not the whole usage block — the sentence is the
  interface, and `-h` prints the block.

## Commands

```sh
make build vet lint                       # compiles, vet, golangci-lint
make test                                 # unit + compat suites (~45 s here)
make race                                 # same with -race
make endpoints                            # coverage report -> ENDPOINTS.md (generated, untracked)
make run                                  # dev server, 127.0.0.1:8008, /tmp/lattice.db
make image                                # the release image (Dockerfile) -> lattice:latest
make complement-image                     # bin/lattice with CGO off, then the Complement image
make swagger                              # docs/swagger/{swagger.json,swagger.yaml}
go test ./tests/compat/ -race
go test ./tests/compat/ -run 'TestAnOperator|TestOperator'   # the account commands alone
bin/lattice --addr 127.0.0.1:8008 --server-name localhost:8008 \
    --db /tmp/lattice.db --jwt-secret dev      # what `make run` executes
bin/lattice add-user alice -db /tmp/lattice.db -server-name localhost    # operator command
bin/lattice users -db /tmp/lattice.db -server-name localhost
bin/lattice remove-user alice -db /tmp/lattice.db -yes
```

Note: the compat (and future federation) suites bind loopback ports, so they
need to run outside any network sandbox.

## House rules for this repo

- Handlers stay thin: decode → `service` call → `writeJSON`; errors go
  through `a.writeError` (which uses `matrix.ErrAs`).
- New endpoint: add the route in `internal/api/routes.go`, add a case in
  `tests/compat` (or `tests/federation` once steps 6+ land), then re-run
  `make endpoints`.
- Tests live in `tests/` only — the user asked for functional tests outside
  the implementation tree. `tests/compat` is black-box HTTP against a running
  server; `tests/unit` (`package unit`) drives pure functions with tables and no
  database. When a rule deserves a unit table but lives unexported in a service
  file, cut it into a small pure package (`internal/power`, `internal/push`,
  `internal/coverage`) instead of exporting it from `service`; the service keeps
  the storage plumbing.
- Federation code mounts only under `-federation`; with the flag off the
  binary must behave exactly as a single-server Lattice.
- New repo convention (unchanged): model + interface + Impl + `NewXxxRepo`
  in `internal/persist/`, wire into `Database`, add to `AutoMigrate`,
  remember the hard-delete rule for unique indexes.

### 15. Federation X — the queue's memory (the to-device outage cases)

`TestToDeviceMessagesOverFederation` passed with good connectivity and failed its
`interrupted_connectivity` and `stopped_server` cases — and, tellingly, **passed when run
on its own**. That is the shape of a bug about what a server remembers, so the queue was
read rather than guessed at:

- Outbox items are idempotent per `EdgeID` under a unique index, which is right: an event
  sent twice to one server is one item, a room key retried by a client is one key.
- Every model embeds `gorm.Model`, so `Delete` is a **soft delete**: the row disappears
  from reads and keeps its index. `Acknowledge` retired an item by marking it, so its edge
  stayed burned forever — and `Append`'s `ON CONFLICT DO NOTHING` silently dropped the next
  message that reused it. Complement's three subtests share one deployment and each PUTs
  the client transaction id `1`, so the third subtest's message was never queued at all.
  The log said so — "1 of 1 queued item(s) were already waiting" — which is why the retry
  never appeared in the sender's log.
- The edge for a to-device message was `destination : edu_type : client-txn-id`. A client's
  transaction id is unique **per client session**, not per server: every device counts from
  0. Two users of one homeserver both using `1` to the same peer cancelled each other out.

Fixes: the outbox (and every other table addressed by a unique index — sessions, media ids,
inbound transaction ids, device keys, one-time keys, account data, key-backup rows, remote
devices, received receipts of a room) is deleted with `Unscoped()`, so a retired row stops
holding its identity hostage; and the to-device edge carries the sender. Two log lines were
added because this bug was invisible from the outside: a failed attempt names the
destination, the transaction and the wait, and a start-up with a backlog says how much is
waiting.

Tests: `TestAnAcknowledgedOutboxEdgeCanBeUsedAgain` (`tests/compat/database_test.go`; fails
against the soft delete) and `TestTwoSendersSharingATransactionIDBothGetTheirMessage`
(`tests/federation/outbox_test.go`; delivers one message where the sender-less edge is used).
Complement: all three `TestToDeviceMessagesOverFederation` cases green, and the server-server
package moved from 37 to 38 green with nothing newly red. `TestMediaFilenames` /
`TestMediaWithoutFileName` are the same class of bug in a different table — a retired media
row keeping its `(server_name, media_id)` — and are the next thing to look at.

### 16. Federation XI — media: which door, and what the file is called

The two media cases left red by step 15 turned out to be two unrelated bugs, and neither was
in the media *content* path.

**Which door is answered on.** `TestMediaWithoutFileName` failed with
`asking host.docker.internal:1054 for the media PlainTextFile: M_UNKNOWN: answered 400`.
Lattice asked only `/_matrix/federation/v1/media/download/{mediaId}` — where the
specification puts media now, and where no origin is named because the mxc URI the client is
holding already says whose content it is. A server that predates that change has no such
route. Complement is not being obscure: it is a peer that only answers the legacy
unauthenticated path, `/_matrix/media/v3/download/{serverName}/{mediaId}`, which carries the
origin in the URL. A user on such a peer would have seen a broken avatar until the *other*
server caught up, which is not their problem to fix. `RemoteMedia` and `RemoteThumbnail` now
walk both paths, newest first, and try the next one after any failure — a peer without the
authenticated endpoint reports it as M_UNRECOGNIZED, a 404, or an error page, and one extra
request to a server that genuinely has no such content is cheaper than content that never
arrives.

**What the file is called.** `TestMediaFilenames` failed as `filename did not match, expected
'☕', got '🐔'`, and on two subtests with `M_UNRECOGNIZED`. The route with a trailing
`{fileName}` was registered on the authenticated endpoint only, and even there the handler
labelled the response with the name the *uploader* had given the content, ignoring the name
that was asked for. The suffix is the requester's suggestion for how the download should be
saved; it never decides which bytes are served. Both endpoints now take the suffix
(`/_matrix/media/v3/download/{serverName}/{mediaId}/{fileName}` and the
`/_matrix/client/v1/media` form), `downloadName` prefers the requested name and flattens path
separators in it — the name comes from whoever is asking, and a careless client turns
whatever it is handed into a path.

Tests: `TestMediaUploadAndDownload` (`tests/compat/userdata_test.go`) now asserts the
`Content-Disposition` name on three shapes of download — no suffix (the uploader's name), an
unauthenticated suffix, an authenticated suffix with a space and a semicolon in it — and fails
against the handler that used the stored name. Complement is the only place the two-door
problem shows up, so that is where it is covered.

Re-measured: `tests` (server-server) **51 red of 91**, 40 green; every media and thumbnail
case in that package is now green (`TestMediaFilenames`, `TestMediaWithoutFileName`,
`TestMediaWithoutFileNameCSMediaV1`, `TestContentMediaV1`, `TestLocalPngThumbnail`,
`TestRemotePngThumbnail`, `TestFederationThumbnail`) and nothing is newly red. The red list is
knocking and restricted rooms, room versions past v11, application services, presence, and
`TestACLsForEDUs`.

### 17. Federation XII — knocking and restricted rooms (in progress)
One rule that was not about knocking and showed up there: **a state key is described once per
sync response.** The state block used to carry the room's current state while the timeline carried
the events, so a room knocked on twice arrived with two versions of `m.room.member/@bob` in one
answer - and a check, or a client, that reads the state first never reaches the older membership in
the timeline. The state block now describes the room around the timeline instead of repeating it.
Anything that reads sync responses should expect that: a state key appears in `state` or in
`timeline`, not both.


Aimed at the knock/restricted block of the Complement red list, and explicitly **not** at
MSC3787 (`...InMSC3787Room` — restricted rooms back-ported to room versions 1-3), MSC4289
privileged creators, room versions past v11, application services or presence: the first two
are drafts the implementations treat as experimental, the rest is a different feature.

Done so far:

- **Join rules read leniently.** `m.room.join_rules` is decoded by hand: an `allow` entry that
  is not an object, or names something that is not a room, is dropped; a whole `allow` key that
  is a scalar is dropped. The room keeps working and joining falls back to needing an invite,
  which is what the other implementations do and what `Join should fail with mangled join
  rules` checks. Both spellings of the room-shaped rule are accepted: the spec's `m.space` and
  the older `m.room_membership` the implementations grew up with.
- **A membership written through the state endpoint is judged by its membership.** The spec
  gives `m.room.member` a default power level of 50, which is right for inviting somebody else
  and wrong for joining oneself, so the generic state-level check now stands aside for
  `validateMemberStateWrite`, and that checks a self join against the room's join rule (the
  state endpoint must not be a door with no guard).
- **A limited timeline is the end of the room.** `TimelineFor` ordered ascending with a LIMIT,
  so a fresh `/sync` of a room with more than the limit returned its *oldest* events, marked the
  rest limited, and pointed `prev_batch` at the start of the room. Fixed to the newest window;
  `limited` is now decided by counting. This was the cause of a state event never reaching a
  syncing client in `TestRestrictedRoomsLocalJoin`.
- `TestRestrictedRoomsLocalJoin` is green in Complement (all five subtests).

Green now (also): `TestRestrictedRoomsRemoteJoinFailOver` — which needed two things.
`JoinFederatedRoom` tried a **local witness** first (`joinThroughLocalWitness`, `joinRoomLocally`:
write the join against the copy of the graph this server keeps, witness in the content, let
`AppendEvent` relay it) and answered "not my call" when it had no witness or could not see the
joiner in an admitting room. That local write was taken out again on 2026-11-03, for the reason in
the block at the end of this step. The attempt list stopped adding the room's own server behind the
client's back (`joinCandidates`), and asking ourselves is skipped. `ServeSendJoin` now queues the
join it accepted for the room's other servers — a join answered and not relayed leaves the room's
other servers with a member they have never heard of, wrong member counts and a fan-out that
excludes a server holding a copy of the room.

Also: rooms this server creates state `invite: 50` rather than 0. The specification's two chapters
disagree (matrix-spec#1187) and the implementations enforce 50, so a room that says 0 and is judged
by 50 means different things to its members. A written `invite: 0` is still honoured as written —
rooms open invites on purpose — so `ApplyDefaults` leaves invite alone.

Lead worth following: `TestRestrictedRoomsRemoteJoinLocalUser` now reaches the sync check and
fails because a non-hosting server that was **invited** into a restricted room answers a later join
with "room X needs an invite to join" — `joinAllowed` reads `invite` from its state where the room
says `restricted`. That points at the state bundle our invite path stores on the invited server:
the join rules of the room are not in it, or are the wrong version of it.

Green now: `TestRestrictedRoomsLocalJoin`, `TestRestrictedRoomsRemoteJoin`,
`TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11`. The remote half needed the
authorising user: `ServeMakeJoin` names one of its own users who is joined, holds the room's
invite level and belongs to one of the allowed rooms; `joinViaServer` copies that name out of the
template into the signed event; `ServeSendJoin` judges the pair - the joiner must belong to the
allowed rooms this server hosts (an invitation is its own admission), and the witness must be a
user of this server or of the server in front of it, and a member of one of those rooms. The
witness also has to be part of the join's **auth events**, which is why make_join chooses it
before selecting them: a chain built from `{"membership": "join"}` alone is refused the next time
the room reads it, which showed up as a leave being rejected. A failed federated join now carries
the refusing server's reason instead of a bare M_NOT_FOUND.

Green since, and what each needed:

- Knocking (`TestKnocking`, `TestKnockingInMSC3787Room`, `TestKnockRoomsInPublicRoomsDirectory`
  both variants, `TestCannotSendNonKnockViaSendKnock`): `make_knock`/`send_knock`/`knock` in v1
  and v2, inbound and outbound; the knock queued for the rest of the room once the room took it;
  a knock allowed to be made again; the `knock` section of `/sync` filled in (it was declared and
  never written); the room directory saying each room's `join_rule`; and the state rule below.
- `TestRestrictedRoomsRemoteJoinFailOver` (both variants): the attempt list no longer adds the
  room's own server behind the client's back; and `ServeSendJoin` queues the join it accepted for
  the rest of the room, which is the half that was missing for the server asked first. (It also
  named a local-witness path, since removed: see the last block.)
- An outbound invite now carries `room_version` — a v2 invite is a v3-format event, and the
  invited server has no copy of the room to learn the version from. Complement's stub refused the
  invite with "unsupported room version ''" until it did.

Still red, and what each needs:

- `TestRestrictedRoomsSpacesSummaryLocal/Federation`: the space summary a restricted join is
  authorised through, for a space this server does not host.
- `TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV12` and
  `TestKnockRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV12`: `createRoom` with
  `room_version: "12"` answers `M_UNSUPPORTED_ROOM_VERSION`. Everything else in that test is
  reached by its v11 twin, which is green. This wants a decision about room version 12, not a fix
  to the join rules.

Green as of 2026-11-03, the whole restricted block except the two above:
`TestRestrictedRoomsLocalJoin`, `TestRestrictedRoomsRemoteJoin`,
`TestRestrictedRoomsRemoteJoinLocalUser`, `TestRestrictedRoomsRemoteJoinFailOver`, and the four
`...InMSC3787Room` variants of the same. MSC3787 turned out not to be a new room version to
support: it is `knock_restricted`, and the tests run it on room version 10, which this server
already speaks.

What it took, in the order the discoveries came:

- **Do not pre-judge a room this server does not host.** `joinAllowed` fell back to `invite` when
  it held no join rule, and "I hold no rule here" was read as "the rule is invite". Every first
  join of a remote room was then refused before it was asked about. What this server holds is what
  it may judge; the room's own server answers for the rest.
- **The room version comes from the server that knows it.** A room entered by invitation was
  recorded under this server's default version whenever the inviter's event id could not be
  reproduced. The version decides how every event id in that room is made, so from then on this
  server signed and hashed the room wrongly: the invite was fine, the join was fine, and the first
  event this server made there was refused by every peer as a bad signature. The invite body and
  `m.room.create` both name the version, and that is believed; the join handshake corrects the row
  when the room's own server states it.
- **A server in the room may finish a join there.** `ServeSendJoin`, which already holds the graph,
  the state and the state-before-the-event, builds the join template for its own user as well
  (`joinThroughThisServer`), and the finished event is handed to another server in the room to be
  accepted. `joinTemplate` is one function used by both sides, so answering `make_join` and
  finishing a join here cannot drift.
- **A server may not write a join into its own copy of a room it does not host.** That was the
  local-witness path, and Complement caught what it does: it answered 200 out of a copy that was
  stale, and the room never got to say no. A server in the room builds the join and names the
  witness its own state points at; the server the room counts judges it, and its refusal is the
  client's answer.
- **Membership of another room is a fact only while somebody is still there.** `canReadMembershipOf`
  counts a room as readable when this server hosts it or still has a member in it. A copy of a room
  the last member walked out of is frozen, and a restricted rule read from it answers for the day
  the member left. This is what makes `TestRestrictedRoomsRemoteJoinFailOver` answer with a refusal
  instead of a guess.
- **The rule reads the joiner, not the witness.** A restricted room admits a joiner who belongs to
  one of the rooms on its allow list. It does not admit a stranger because somebody who belongs
  there signed the join: Synapse refuses that, and a server that accepts it holds two answers about
  one join. `authcheck` can check the half that a state snapshot can answer - that the allow list
  names at least one readable room, so a malformed list admits nothing and a self-claim has nothing
  to stand on - and the caller checks the joiner's membership of the other room.
- **`knock_restricted` answers two ways at once.** Whoever the allow list admits joins without
  knocking; the rest knock and wait. Asking for the knock first made the admitting half a wait.

A hole found on the way, and closed in step 19: **a user who had knocked could join without an
invite**, because the join check read the asker's own knock as the room's answer. It is recorded
here because of how it was found - a probe, not a failure - and because of what it says about the
two authorisers this server has (step 19).
- New from the wider sweep: `TestJoinFederatedRoomFromApplicationServiceBridgeUser` — an
  appservice virtual user asking to join a remote room is answered `401 M_UNKNOWN`: the request
  authenticates as the appservice, and the user it names has no device here. Application services
  and user-identified requests are their own piece of work.

### 18. Federation XIII — the space summary (2026-11-04)

Green: `TestRestrictedRoomsSpacesSummaryLocal`, `TestRestrictedRoomsSpacesSummaryFederation`,
`TestClientSpacesSummary` (all subtests), `TestClientSpacesSummaryJoinRules`,
`TestFederatedClientSpaces`, and `TestRoomSummaryAllowedRoomIDs` - which had never been run
against this server before, and was red on the first try.

What each turned out to be:

- **The summary lists rooms the reader can reach, not the rooms the reader can read.** A
  restricted room keeps its history to its members, so an outsider cannot read it, and the walk
  used reading as its only gate - which hides the room from exactly the person its own join rule
  admits. `hierarchyShows` now asks `joinableBy` as well: membership of a room on the restricted
  room's allow list, and only of an admitting room this server can still read.
- **A public room the reader is not in stays out.** MSC2946's phrase is "joined or potentially
  joinable", and read literally that lists every public room a space links. The implementations
  do not do that, and a summary of a space is not a directory of everything reachable from it.
  Recorded here because the phrase invites the wrong reading.
- **The federation hierarchy answer is shaped for servers, not people.** It was answered in the
  client shape (`rooms`, recursed, paginated) and parsed the same way, which is fine between two
  copies of this server and wrong for everybody else. The specification's answer is `room` (the
  one room asked about, with its `children_state`), `children` (its immediate children, without
  links), and `inaccessible_children` (the children that will not be described). One room, no
  recursion, no pagination: the walk asks room by room. `RemoteHierarchy` asks and parses that,
  and the walk recurses by following the links the answer carries.
- **`allowed_room_ids` is what makes that answer decidable.** The peer is not told who is asking,
  so it answers for the asking *server*: the room is offered when any member of that server could
  get in, and it travels with the rooms its allow list names so the asker can decide for its own
  user (`remoteRoomShows`). Until the peer has somebody in an admitting room it cannot make the
  offer at all, which is the whole point of `TestRestrictedRoomsSpacesSummaryFederation`.
- **A room that cannot be offered is answered as unknown**, `M_NOT_FOUND`, not `M_FORBIDDEN`, on
  this endpoint. A local copy of a room wins over a peer's description: a child hosted elsewhere
  but held here is described from what this server knows rather than asked about.
- **Two field names were wrong**: `join_rule`, not `join_rules`, and `guest_can_join`, not
  `guests_can_join`. The room summary (`GET /v1/room_summary/{roomIDOrAlias}`) carried the first
  mistake too, and needed `allowed_room_ids` for restricted rooms and no key at all for the rest.
  Our own compat test had been asserting the wrong name, which is what a test written from the
  implementation rather than the specification buys you.

`HierarchyRoom.ChildrenState` became a pointer so the two answers can differ: a client is always
shown the key, empty for a room that is not a space; a child described to a peer omits it.

A run over the neighbourhood of these routes (join, invite, federation, members, sync, the room
directory, state, redaction, leave/ban/kick, knock, restricted, hierarchy, event auth and gap
filling): **35 green, 5 red**, and the five are the known ones - room version 12 twice, the
application-service bridge user, `TestFederationRoomsInvite`, and
`TestFederationRedactSendsWithoutEvent`. Nothing that was green went red.


### 19. Knocking: a knock is a question, not a door (2026-11-04)

`POST /join` on a `knock` room accepted a user whose membership was `knock`, and a
`knock_restricted` room accepted it too when the allow list did not admit them. A knock
puts the asker in the room's state so the room can answer; reading that state as the
answer lets the asker in on their own question. The rule asks for an invite, and
retracting a knock stays a leave.

The reason this was worth a step of its own is the shape of the mistake. This server has
**two authorisers**:

- `authcheck` - the room rules as the specification writes them - runs only on events
  **arriving over federation** (`fedapply.go`), and its `knock` branch has always refused
  this: `the room's join rule is knock and X has not been invited (membership: knock)`.
- Locally originated events go `appendMember` → `AppendEvent` and are judged by the
  service's own guards (`joinRuleProblem`, power levels, the state guards). Nothing there
  consults `authcheck`.

So the same join was accepted at home and refused by every peer holding the same room, and
nothing in the room could see the disagreement from inside. Fixing the rule was four lines;
the open question behind it is whether local writes should be judged by `authcheck` as
well, with the service guards reduced to validating the request rather than deciding the
room's rules. That is a piece of work of its own, and until it is done, every rule has to
be written down twice and can drift in exactly this way.

Complement does not cover it: its subtest *"Attempting to join a room with join rule
'knock' without an invite should fail"* (`tests/knocking_test.go:126`) runs **before** the
knock in that test, so joining after knocking is never tried. Nothing in this server's own
suite relied on the hole either - every join after a knock in the tests is preceded by an
invite - which is why it survived as long as it did.

Now asserted: `TestAKnockIsNotADoor` (knock, 403, invite, 200) and the MSC3787 pair test,
which also writes a membership through the state endpoint to pin the second door. The
`tests/compat/zz_debug_test.go` probe that had been committed in place of assertions -
`TestZZDebugKnockTimeline`, no assertions, only `t.Logf`, and counted in the compat totals
- is deleted.

The knock and restricted group after the change - `TestKnocking` and its MSC3787 twin, the
public-room-directory knocks, the restricted join and fail-over set, the space summaries and
the room summary - is **21 tests, 19 green**; the two red are the room version 12 power-level
cases that were red before it.


### 20. Authorization: one rulebook (2026-11-04)

A room's rules are written down in `internal/authcheck` - the specification's list, read
event kind by event kind, with the room version deciding which entries of it apply. That
list ran only on events **arriving from a peer** (`fedapply.go`). Everything this server
originated was judged by guards written beside the endpoints that asked for the events, and
the two had drifted. The drift is invisible from inside: this server accepts what it wrote,
and every peer holding the same room refuses the identical event, which is how the knock
that opened a door (step 19) survived as long as it did.

Now the same list is asked about a locally written event inside the append transaction -
after the event is built and identified, before a row is written - so the state it reads is
the state the event was built against and an event the room refuses leaves nothing behind:
no stream position, no transaction record, no membership row. [authcheck.Rules] and not
[authcheck.Check]: the extra check in `Check` reads an event's own choice of auth events,
and exists to catch a sender picking friendlier rules for itself. This server picked them,
with the same selection the rule would recompute, so the rule would only be checking this
server's arithmetic.

Two rules turned out to be enforced wrong at home, found by asking the rulebook instead of
the guard. Both are recorded because they were probes, not failures:

- **A server administrator was above a room's join rule.** `joinRuleProblem` let an
  administrator join an invite-only room with no invite, because the guard asked whether the
  user ran the server. The room's rules know no server administrators; the membership event
  signed for that join is refused by every other server in the room, so the membership
  existed on one server. Administrators still read rooms they are not in, which is a
  question this server answers for itself. Before: `200`. Now: `403`, in the rulebook's
  words.
- **A moderator could ban somebody who outranks them.** The ban guard asked for the ban
  level and not for the rank beside it, so a member holding 50 banned a member holding 100 -
  while the *kick* guard refused the same pair. One rule held in one of its two places.
  Before: `200`. Now: `403` for both.

What the guards kept is what no room rule says: that a membership is not written for another
user by their own hand (`you cannot invite yourself`, `you cannot ban yourself this way`),
that a canonical alias has to resolve to this room, that a room's encryption is set once,
that an `m.room.create` is not replaced. The **redaction level** stays on the service side
deliberately: a member with no power redacting their own message is this server's policy and
not a rule a peer checks, so putting it in the rule list would make this server refuse what
every other server accepts.

The graph gains two fields for the difference between asking a peer and deciding at home.
`Originating` says this judgement is about an event the server holding the graph is about to
sign; `Admits` names the rooms the caller has confirmed the joiner belongs to, for a
restricted join - a fact that is not in this room and no snapshot of it can carry. The rule
still reads the room's allow list, so a room named there admits nothing unless the room
admits people through it. `readCurrentState` and the new local read share one state reader
(`stateForAuth`), which is also where a locally judged event's state comes from the append
transaction rather than from outside it.

A run of the guards' side of this - `knock`, `restricted`, `spaces`, `room summary`, `join`,
`invite`, `redact`, `ban`, `kick`, `leave` - is **23 tests, 20 green**, the three red being the
pair of room version 12 power-level cases and the application-service bridge user. Nothing that
was green went red, which is the point of the exercise: a room's rules, read by one list, refuse
the same events at both doors.

The first complete run of Complement's `./tests` package (all **91** tests) since this landed:
**61 green, 30 red**, against the **40 green, 51 red** measured at the start of this stretch of
federation work. The 30 sit in these places - the MSC4289 privileged-creators group (6),
MSC4291 room-ids-as-hash (5), MSC4297 state resolution v2 (2), MSC4311 stripped state (3), room
version 12 (3), the auth-chain rejection set (`TestCorruptedAuthChain`,
`TestInboundFederationRejectsEventsWithRejectedAuthEvents`, `TestUnrejectRejectedEvents`,
`TestOutboundFederationIgnoresMissingEventWithBadJSONForRoomVersion6`), the federation odds
already listed (`TestFederationRoomsInvite`, `TestFederationRedactSendsWithoutEvent`,
`TestIsDirectFlagFederation`, `TestACLsForEDUs`, the application-service bridge user), presence,
and `/jump_to_date`. That run is also the one that found the volume-lock limit of the other note arriving in
bulk rather than once: without `-parallel 2`, **30** tests failed with
`exceeded num_locks (2048)` deploy errors and nothing was measured. A complete run of this
package wants `-parallel 2`, and a `podman volume prune -f` before it.

### 21. Room version 12 — room ids as hashes, privileged creators, state resolution v2.1

Target: the specification's own page for it, [rooms/v12](https://spec.matrix.org/v1.19/rooms/v12/),
which is room version 11 plus four things. The gate is Complement's `v12_test.go` — **nineteen**
tests, all red today:

- `TestMSC4291RoomIDAsHashOfCreateEvent` and its four sub-tests (`_AuthEventsOmitsCreateEvent`,
  `_UpgradedRooms`, `_CannotSendCreateEvent`, `_RoomIDIsOnCreateEvent`), plus
  `TestComplementCanCreateValidV12Rooms`.
- `TestMSC4289PrivilegedRoomCreators` and five more (`_Additional`, `_InvitedAreCreators`,
  `_AdditionalCreatorsAndInvited`, `_AdditionalValidation`, `_Upgrades`).
- `TestMSC4297StateResolutionV2_1_starts_from_empty_set` and `_includes_conflicted_subgraph`.
- `TestMSC4311StrippedStateClientAPI`, `_FullEventsOnStrippedStateFederation`,
  `_RejectInvalidStrippedStateFederation`.
- `TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV12` and the `KnockRestricted` twin —
  red only because no v12 room can be created.

Work, in the order that keeps the tree green:

- [x] **The version plumbing.** `service.SupportedRoomVersions` gains `"12"`;
      `DefaultRoomVersion` stays **"11"** on purpose: the specification recommends 12, but
      `TestIsDirectFlagFederation` sends a v12 invite whose stripped state lacks `m.room.create`,
      which a correct server rejects (MSC4311, v1.16) — so defaulting to 12 turns that test red
      for being right. Say so in the code, next to the constant.
- [x] **Room ids without a domain.** A v12 room id is `!` plus URL-safe unpadded base64 of the
      create event's reference hash: no `:example.com` behind it. Every place that asks a room id
      for its host to decide whether the room is local has to stop doing that — locality is
      "this server has the room", not "the id names this server". The audit has a name:
      `internal/matrix/id.go` holds the grammar — `NewRoomID` (random, always `!local:domain`),
      `IsRoomID` and `ValidRoomID` (both require the colon) — and `DomainOf` is called in 42
      places. `IsRoomID`/`ValidRoomID` must accept `!` plus URL-safe unpadded base64 with no
      colon, and each caller of `DomainOf` on a room id gets read once and classified: it means
      where the room lives (→ ask the database), or it is about an alias or a user (→ unchanged;
      aliases keep their domain in every room version), or it is a room id being used as a
      federation hint (→ the id says nothing; ask the state which servers hold the room). Record
      that classification as a comment on `DomainOf` once the first caller is fixed.
- [x] **The event format.** `m.room.create` carries **no** `room_id` (auth rule 1.2 rejects one
      that does); every other event carries the room id. Event ids are the URL-safe unpadded
      base64 reference hash, as in v4+; the room id is that same hash with `!` instead of `$`, so
      creating a room means: build the create event without a room id, hash it, and the room's id
      falls out of the hash. Canonical JSON, hashing and the stored-event marshalling for the
      client API all have to agree that the create event of a v12 room has no `room_id` key.
- [x] **`m.room.create` is not an auth event** in v12 (rule 3.2 note): the auth-event *selection*
      must not cite it, and the rule must reject an event that does cite it. The rule that
      verifies a peer's choice of auth events therefore gets stricter, not looser.
- [x] **Room creators, formally.** Creators are the create event's `sender` plus
      `content.additional_creators`. Their power level is **infinite**: `authcheck` must answer
      infinite for them wherever it reads a level, and rule 10.4 rejects any
      `m.room.power_levels` whose `users` names a creator. Rule 1.4 validates
      `additional_creators` as an array of user ids under the same grammar as `sender`.
- [x] **Rule 2, the room id check.** An event is rejected unless its `room_id` is the event id of
      an accepted `m.room.create` with the sigil `!`: the check needs the create event of the room
      in hand, which the graph already has.
- [x] **State resolution v2.1** (`internal/stateres`): the v2 algorithm with three changes —
      step 2's iterative auth checks start from an **empty** state map instead of the unconflicted
      one; a **conflicted state subgraph** (paths along `auth_events` between two conflicted
      events, endpoints included) is defined; and the **full conflicted set** additionally
      includes that subgraph. Two tests exercise exactly this, and both check the resolved state
      rather than the code path, so a wrong subgraph shows up as a wrong state map.
- [x] **Stripped state as full events (MSC4311).** The `invite_room_state` this server sends is a
      list of **complete** PDUs (with `room_id`, `hashes`, `signatures`, `depth`, `prev_events`,
      `origin_server_ts`), not the trimmed tuples it sends today; for v12 it must include
      `m.room.create`, because nothing else in the invite says what the room id is. Inbound: an
      invite whose stripped state does not validate (a tuple, a missing create in v12, a state
      event whose `room_id` is not this room's) is rejected, with the reason in the log.
- [x] **Upgrades.** Read `TestMSC4289PrivilegedRoomCreators_Upgrades` and the MSC4289 upgrade
      text before writing the code: what carries a creator into the new room (a `predecessor`
      plus `additional_creators`, or a power-levels content carry-over) is exactly what that test
      asserts, and guessing it wastes an afternoon.
- [x] **Wiring state resolution in.** When an arriving event cites a parent this server was
      never sent, the history walk is only half of the question: this server keeps the state a
      room *holds*, not a snapshot per event, so a hole in the history is also a hole in its
      picture of the room. Having walked `get_missing_events`, it asks the peer what its state
      was at that event (`/state`, falling back to `/state_ids` and `/event`), judges every
      event the peer hands over, and runs `stateres` over both views. What the resolution names
      for a key this server does not hold is written down; what it names for a key this server
      already answers is **not** - a snapshot a peer hands over is evidence about the hole, not
      a verdict on a piece of state this server resolved from its own graph, and taking it would
      let a server that has simply not been told about something undo the decision. The
      disagreement is logged rather than buried.

      Two things this exposed in the receipt path, both worth knowing about separately. The
      transitive auth-chain check read a chain event against the state it cites, and when the
      chain walk stopped at its bounded depth that snapshot was missing memberships - so a deep
      chain refused an honest event with "nobody holds one" because of how far down this server
      chose to look; those events are left unjudged and counted in a log line. And `send_join`,
      `send_leave`: version 2 of those endpoints wraps the event in `{"event": ...}`, and the
      server only ever read the bare body it sends itself - a spec-conformant peer's join was
      refused for naming no event type.

A run of Complement's v12 gates after this stretch: **MSC4289 (6), MSC4291 (5), MSC4311 (3),
MSC4297 (2), `TestComplementCanCreateValidV12Rooms`, `TestRoomVersionSupport`, and the two v12
restricted/knock cases - green**. Five things the run found that were worth fixing on the way:
a room version 12 `m.room.create` names no room, so every place that read a PDU's `room_id` to
decide where to keep an event was keeping the room's first event under the empty room
(`roomOfPDU` now reads the room out of the event's id when the event cannot name it); the empty
auth set that a v12 room's earliest events legitimately carry was being read as a forged chain;
a knock from a server outside the room had nowhere to ask when the room id named no server,
which is what the endpoint's `via` list is for; a peer's `send_knock` body is the bare event in
the wild and the wrapped one in the spec, so both are read; and MSC4297 needed the two fixes to
the receipt path above, without which a 250-event chain was unusable.

- [x] **Own tests**, because Complement is a guest here: a v12 room created, hashed, joined,
      posted to, knocked on and upgraded in `tests/compat` (the id has no domain, the create
      event has no `room_id`, the creator cannot be demoted, `users` naming the creator is
      refused, `additional_creators` holds at rule 1.4); and in `tests/federation` a v12 invite
      with full stripped state accepted, a v12 invite with a tuple or without the create event
      refused, and a v12 join through `make_join`/`send_join`.

Done means: those nineteen Complement tests green, the two v12 knock/restricted ones green, and
nothing that was green before goes red.

### 22. Simplified Sliding Sync (MSC4186) — the sync modern clients speak — done (2026-11-04)

Target: [MSC4186](https://github.com/matrix-org/matrix-spec-proposals/blob/main/proposals/4186-simplified-sliding-sync.md).
Served at `POST /_matrix/client/v4/sync` and, while the revision that carries it is still
unstable, at `POST /_matrix/client/unstable/org.matrix.simplified_msc3575/sync`, with
`org.matrix.simplified_msc3575: true` in `/versions`. As predicted, the pinned Complement
checkout has no `msc4186` package: the gate was this repository's own suite, **17** tests in
`tests/compat/sliding_sync_test.go`. The one upstream case that reaches the endpoint is
`tests/msc4306`'s sliding-sync thread subscription, and it stays red because it asks for the
`io.element.msc4308.thread_subscriptions` extension, which needs MSC4306 (gap 9), not this one.

The shape of it: `internal/matrix/sliding.go` (the wire types), `internal/service/sliding.go`
(the connection, the poll loop, the list assembly), `internal/service/slidinglist.go` (ordering
and filters), `internal/service/slidingroom.go` (one room's result), `internal/service/slidingext.go`
(the sections that are not rooms), `internal/persist/slidingsync.go` (two tables),
`internal/api/sliding.go` (the handler).

- [x] **The connection model.** `sliding_sync_connections` holds one row per
      `(user_id, device_id, conn_id)`, and `sliding_sync_rooms` holds what that connection has
      been shown of each room: the timeline position, the state position, the config it was sent
      under, how much of the room's recent history the client holds, and the room's current name,
      avatar, membership and activity value. A connection belongs to the **device**, not to the
      token, because a connection outlives the request that carries it and a refreshed token
      should not orphan it.
      `pos` is `ss` + base64 of `connKey\0position` (`auth.SlidingSyncPos`): opaque to the client,
      but carrying which connection it was issued to, because the specification requires that a
      position this user and device were never given be refused and a bare number holds no memory
      of that. A token that decodes to another connection, or that names a position other than the
      one the connection is at, is answered 400 `M_UNKNOWN_POS` (`matrix.ErrUnknownPos`), and the
      client's recovery — begin again without a `pos` — is the one the MSC prescribes.
      What a connection has been seen is never a permission: membership, stripped state and
      counts are re-read from the rooms on every request.
      The MSC lets a server answer at most one request per connection; this server does not
      serialise them. A second concurrent request builds from the same knowledge and its commit
      moves the connection forward, so the first request's answer still arrives but the `pos` it
      hands out is refused afterwards. That is the documented recovery path rather than a hole.
- [x] **`pos` usable in `/messages` and `/relations`.** `auth.ParseStreamPos` understands the
      `ss` form and returns the stream position inside it, so a client that has only ever used
      sliding sync can page a room. `TestSlidingSyncPosIsABatchToken` pins it.
      The same reasoning made `prev_batch` a plain stream token.
- [x] **Lists.** The per-user sequence is assembled in `slidinglist.go`: joined, invited, knocked
      and left-but-previously-sent rooms, sorted by the stream position of the newest event of the
      kinds the MSC counts as activity (`bump_stamp` exposes exactly that number, and it is
      deliberately the same value the ordering uses). `range` is inclusive and 0-indexed; absent
      means the whole list, bounded by `slidingExpireRooms` (1000) which expires the connection
      rather than answering with a list the size of the account. `count` is the size of the
      filtered sequence whatever the range asked for — one filtering pass per list serves both.
      More than 100 lists or 100 subscriptions is 400 `M_INVALID_PARAM`, as are a `range` whose end
      precedes its start and an unknown `set_presence`.
- [x] **Filters**: `is_dm` (from `m.direct`), `spaces` (rooms directly named by an `m.space.child`
      of a *joined* space the caller names; subspaces are the client's to name), `is_encrypted`,
      `is_invited`, `room_types`/`not_room_types` (a null entry means "no type"; the negation
      wins), `tags`/`not_tags` (the negation wins). MSC4186 names no sender or event-type filter,
      and none was invented.
- [x] **`required_state`**: `include`/`exclude` of `{type?, state_key?}` with an omitted half as a
      wildcard, and `lazy_members`, which sends the members of the timeline's senders, the targets
      of membership events in the window, and the whole membership list when the window is not
      `limited`. `exclude` does not narrow lazy memberships, as the MSC asks. The proxy-era
      state-key tokens are read too, though MSC4186 defines only the wildcard: `$ME` resolves to
      the requesting user, which makes it an ordinary pattern that survives lazy loading (naming
      one member is not asking for all of them), and `$LAZY` turns lazy loading on rather than
      looking for a state key no room can hold. Deleted state is a
      `StateStub`: `{"type":…,"state_key":…}` with no `content`. On this server state rows are not
      removed, so the way state *goes away* is a redaction — a redaction inside the room's recent
      events names the key that has been covered over, and the stub is sent only to a connection
      that was previously shown that state. Membership is excluded from the rule: redacting a
      membership does not end it, and a client told to delete the entry would drop a member who is
      still in the room.
- [x] **Config combination and expansion.** A room matched by several lists (or a list and a
      subscription) is sent once, under the union: the largest `timeline_limit`, the union of
      `required_state`, lazy only if every config asking for memberships is lazy, and the lists it
      matched named in `lists`. `sliding_sync_rooms.config` stores that combination so the next
      request can tell a widened window from an unchanged one; `timeline_limit` beyond what the
      connection holds resends the newest window with `expanded_timeline: true`, and such rooms are
      reported immediately rather than waited for.
- [x] **Subscriptions**: `room_subscriptions` with `timeline_limit` and `required_state`; a room
      the user is not in and never was is silently left out of the answer, and a room here only
      because it was subscribed to carries no `lists`.
- [x] **The room result**: `initial`, `name`/`avatar` as raw JSON so *removed* can be told from
      *unchanged* (`null` versus absent), `heroes` only when the room has no name of its own,
      `required_state`, `timeline` (with the thread summary `/sync` attaches), `prev_batch`,
      `limited`, `num_live`, `joined_count`, `invited_count`, `bump_stamp`, `membership`, `lists`,
      `is_dm`; and for an invite, knock or rejection, `stripped_state` instead of the joined half.
      Counts and heroes are the same computation `/sync` already does. Not sent: `topic` — the MSC
      names it, the summary here is name, avatar and heroes.
- [x] **Extensions**: `to_device` (`events`, `next_batch`), `e2ee` (`device_one_time_keys_count`,
      `device_unused_fallback_key_types`, and `device_lists` deltas on a non-initial request),
      `account_data` (`global`, `rooms`), `receipts` (`rooms`), `typing` (`rooms`). They answer
      from the same data the classic `/sync` assembles. The reserved `lists`/`rooms` keys narrow
      which rooms the ephemeral sections cover. A section the client asks for by a name this
      server does not know is left out of the answer rather than failed: a client that asked for
      presence still needs its rooms. Not implemented: the `presence` extension and
      `io.element.msc4308.thread_subscriptions`.
- [x] **Long-polling**: `timeout` in milliseconds, ignored on the initial request; a request with a
      `pos` and nothing to say waits on the same hub `/v3/sync` waits on, and answers an empty
      rooms section when the timeout expires. `set_presence` is validated; presence on this server
      is local (see the presence decision above), so it has nothing to write.
- [x] **Own tests** in `tests/compat/sliding_sync_test.go`: an initial window with a range and a
      `count` that ignores it; a grown range sending only the rooms not yet sent, marked `initial`;
      deltas that carry only the room that moved and do not repeat an unchanged name; `pos` used as
      a `/messages` batch token; a foreign, a cross-device and a replayed `pos` refused with
      `M_UNKNOWN_POS`; the limits (`101` lists, `101` subscriptions, a backwards range, an unknown
      presence); the unstable path and its `/versions` flag; the seven filters and the invited
      list's stripped state; a subscription outside every list and one into a stranger's room;
      a widened window arriving as `expanded_timeline`; lazy members covering the window's senders;
      a redacted state event arriving as a stub; the five extension sections; the long poll waking
      on a message and an idle long poll waiting for its timeout; the thread summary on a window's
      root.

Openings this leaves, in the order they would be picked up:

- **The extension room scoping reads the first section that names `lists` or `rooms`.** A client
  that scopes `receipts` one way and `account_data` another gets one scope for both.
- **`timeline_count` is an estimate.** It is how many of the room's recent events the connection is
  believed to hold, and it decides only whether a widened window has to resend. A client that
  changes `timeline_limit` back and forth may be resent history it already has.
- **No `truncate`, no per-list `bump_event_types`.** The event kinds that count as activity are one
  fixed set for the server, which is also what the room list is sorted by.
- **No `presence` extension and no thread-subscription extension**, the latter because MSC4306 is
  not implemented at all.
- **Eight connections per device, 1000 rooms per connection**, both pruned rather than refused:
  the oldest connections of a device are forgotten, and a room list past the bound expires the
  connection so the client begins again instead of being served a query that grows without end.

### 23. MSC2675 — which events of a room a member may see (2026-11-04)

The specification answers two different questions and this server had answered one of them.
`RequireReadable` answers *may this user read the room*: joined and invited users can, banned
users cannot, strangers only if the room is world readable. MSC2675 adds the second: *which of
its events*, decided by the room's `m.room.history_visibility` and by when the user arrived. A
room that chose `joined` said that a member sees the events from the moment they joined, and
`invited` that the room opens at the invite. Nothing consulted that on the client side, so a
member who joined last week could ask for the room's first event by id and be answered — and a
batch of those events could arrive unasked, in a timeline filled from a peer.

- [x] **`historyGate`** in `internal/service/history.go` answers the *when* half for one room and
      one user. It reads the room's own `m.room.member` events for that user once, oldest first,
      and answers what the membership was at the position of the event being considered: a
      `join` is readable in both strict modes, an `invite` only in `invited`, because that room
      chose the invite as the opening and the other chose the join. A room that named `shared`,
      named nothing, or named a visibility this server does not know needs no query at all — the
      room-level answer is the whole answer, which is what every read on this server has always
      done, so the common case is free.
- [x] **A user's own membership events are theirs whatever the room decided.** A client that
      cannot see the event which opened or closed a membership cannot tell the user why a room
      appeared or went away. This is a deliberate widening of the strict rule, and it is said out
      loud in the code rather than left to be discovered.
- [x] **Applied where history is read**: `GET /rooms/{id}/event/{eventId}` refuses with 403 as
      MSC2675 asks (not the 404 an event that does not exist earns), `/messages` and `/context`
      filter their chunks with the centre event refused outright, `/relations` and `/search`
      drop matches, and the timelines of `/sync` and of a sliding-sync window are filtered as
      they are rendered. Pagination cursors are taken *before* the filter, because a cursor names
      a position in the room and not an event the client was shown: a filtered page still pages
      on to the right place.
- [x] **`StateHistory`** in `internal/persist/event.go` — every version of one state key of a
      room, oldest applied first. Not `hiddenFromTimeline`: a state event that never appeared in
      a timeline window still happened, and a membership that arrived over federation as state is
      exactly what the caller is reconstructing. Only an event this server's state does not
      support is left out.
- [x] **Own test** `TestHistoryVisibilityOpensTheRoomWhenTheUserArrived`: a `joined` room refuses
      its pre-join event by id to the late joiner and answers it to the creator, `/messages` and
      the first `/sync` timeline leave the pre-join event out for the joiner while keeping his own
      membership, an `invited` room hides what was said before the invite and answers what was
      said while he was only invited, a `world_readable` room answers a stranger and a `shared`
      one does not, and `/search` does not hand over the text of a hidden event. Verified against
      a build with the gate forced open, which fails five of its assertions.

Openings this leaves, in the order they would be picked up:

- **`/threads`, `/members?at=` and `/state/{eventId}` are not filtered.** `/threads` would have to
  decide what a thread means when its latest event is hidden; `/members?at=` and the state at an
  event want the per-event state snapshot that is already an open item under room version 12.
- **A user who has left reads nothing** except in a world-readable room, because that is what
  `RequireReadable` has always said. MSC2675 does not settle what a former member may read, and
  MSC2666 ("recently joined") is not implemented, so a client that leaves a room and pages its
  history is refused where Synapse would often answer.
- **The visibility applied is the room's current decision.** MSC2675 is read that way — the
  setting in force when the question is asked — but a room that changed its visibility mid-history
  is judged as it is now, not as it was when the event was sent.
- **The count in `/search` is of what was shown.** Filtering after the match means `limited` and
  `count` describe the filtered batch; a client may be told a page was short when the room was
  hiding matches rather than running out of them.
- **`limited` in a timeline counts what the room holds**, filtered or not. That is what the
  specification means the flag to describe — the gap in the history — but it means a client can be
  told a timeline is limited by events it was never allowed to see.
