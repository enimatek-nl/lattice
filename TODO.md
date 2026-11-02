# TODO / working state

Hand-off document for Lattice (minimal Matrix homeserver in Go). Read
[README.md](README.md) for what the server does today, [PLAN.md](PLAN.md) for
the goal, [ENDPOINTS.md](ENDPOINTS.md) for generated coverage. This file is
now a **release plan: work the steps in order, one at a time**. Steps 1-5 are
done, so the next one is **step 6**; federation is steps 6-12. The history that
used to fill this file lives in git; only facts a future session must not
re-derive were kept (below the plan).

## Current state (verified 2026-11-02)

- A working homeserver with its **federation transport in production shape**:
  `make build vet lint` clean (`make lint` = golangci-lint, 0 issues),
  `go test ./...` green — **113** compatibility tests (~52 s here, ~5½ min
  with `-race` on this machine), **65** offline unit tests in `tests/unit`
  (milliseconds), and **38** two-server tests in `tests/federation` (~12 s,
  ~56 s with `-race`). Coverage: **163 of the 234** indexed endpoints, 31 extra
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
  The plan continues at **step 11**; steps 11 to 13 are driven
  test-by-test against Complement's server-server suite, each naming the tests it
  is expected to turn green, and step 14 wraps the release.
- Against [Complement](https://github.com/matrix-org/complement), the upstream
  Matrix suite, **40 of 106** client-API tests fail (`make complement`) and **72 of
  91** server-server tests fail (`make complement COMPLEMENT_PKG=./tests`) — up from
  4 green before step 10, all of them the membership handshake and what hangs off it.
  See _Complement_ below for how they run, what a run can hide, and what each remaining
  failure is waiting for. The transport itself is not among the reasons: most of
  what is left is a peer answering `M_UNRECOGNIZED` to a handshake Lattice does not
  serve yet — leave, knock, invite, and the graph-reading queries of step 11.
- Federation is live behind `-federation` (default off): a persisted ed25519
  signing key, `/_matrix/key/v2/*`, `/_matrix/federation/v1/version`,
  `/_matrix/federation/v1/user/devices/{userId}`,
  `PUT /_matrix/federation/v1/send/{txnId}`, `GET /_matrix/federation/v1/event/{eventID}`,
  `GET /_matrix/federation/v1|v2/make_join/{roomId}/{userId}`,
  `PUT /_matrix/federation/v1|v2/send_join/{roomId}/{eventId}`,
  `GET /_matrix/federation/v1/query/directory`,
  `GET /_matrix/federation/v1|v2/make_leave/{roomId}/{userId}`,
  `PUT /_matrix/federation/v1|v2/send_leave/{roomId}/{eventId}`,
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
      `stateguards.go`), and `internal/coverage` (the SPECS.md matcher and report
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
- [ ] Knock (`make_knock`/`send_knock` v1/v2; the client-side knock already exists),
      which is what `TestKnocking`, `TestKnockingInMSC3787Room`,
      `TestCannotSendNonKnockViaSendKnock` and
      `TestCannotSendKnockViaSendKnockInMSC3787Room` wait on.
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
- **An invite relayed as an ordinary event dies on an auth chain this server cannot
  complete - and it should soft-fail instead.** `TestFederationRejectInvite` shows the
  shape: hs1 hosts the room, hs2 has never been in it, and the invite arrives over `/send`
  citing auth events hs2 does not hold. hs2 asks hs1 for them, hs1 answers, and the whole
  exchange lands on a rejection because the chain could not be closed before the test ran
  out. A room this server has never been in cannot have the authority to *reject* the
  events of; the honest outcome is a soft failure - keep the event as an outlier, tell the
  user they have been invited, and close the gap later - while a chain that cites an event
  this server holds and has judged rejected stays a refusal. That distinction is the next
  chunk, and it is what stands between the current membership handshakes and
  `TestFederationRoomsInvite`/`TestFederationRejectInvite`.
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

### 11. Federation VI — the read side of the graph: auth chains and gap filling

Two servers that were apart have to agree afterwards, which means each has to be
able to name the events the other is missing and to prove an event's authority.

- [ ] Persist and read the auth chain as a query (the arrays are stored per event
      since step 7; the walk is what is missing).
- [ ] Serve `GET /event_auth/{roomId}/{eventId}`, `GET /event/{eventId}`,
      `GET /state_ids/{roomId}` (including the partial-state form `send_join`
      uses) and `GET /state/{roomId}/{eventId}` for one event.
- [ ] Serve `POST /get_missing_events/{roomId}` (bounded by `limit`, only events
      the requester may see) **and** request it: on a gap, ask, receive the events
      through `/send`, apply them through step 9. Gap filling is also what makes
      soft-fail and rejected events observable in practice.
- [ ] Serve `GET /backfill/{roomId}` (older history with its state, depth-bounded)
      and `POST /timestamp_to_event/{roomId}` (MSC3030).
- [ ] Events whose auth chain is broken, or that hang off a rejected event:
      refused, and reported in the per-PDU answer rather than retried forever.
      Unverifiable events in room versions that tolerate them are a separate rule,
      and `TestJoinFederatedRoomWithUnverifiableEvents` checks it.

Complement turns green: `TestEventAuth`, `TestGetMissingEventsGapFilling`,
`TestInboundCanReturnMissingEvents`,
`TestOutboundFederationIgnoresMissingEventWithBadJSONForRoomVersion6`,
`TestOutboundFederationEventSizeGetMissingEvents`, `TestCorruptedAuthChain`,
`TestUnrejectRejectedEvents`, `TestInboundFederationRejectsEventsWithRejectedAuthEvents`,
`TestJoinFederatedRoomWithUnverifiableEvents`, `TestFederationRedactSendsWithoutEvent`,
`TestNetworkPartitionOrdering`.

### 12. Federation VII — names, profiles and spaces

- [ ] `GET /_matrix/federation/v1/query/directory`, so a remote server can resolve
      a local alias (Unicode included), and the client-side path that then joins
      through it.
- [ ] `GET /_matrix/federation/v1/query/profile` — display name and avatar of a
      local user, refused for a user this server does not have.
- [ ] `GET`/`POST /_matrix/federation/v1/publicRooms` for another server's room
      directory, and the client-side `/publicRooms?server=` that reaches it.
- [ ] `GET /_matrix/federation/v1/hierarchy/{roomId}` to serve our side of a space,
      and `walkHierarchy` following remote spaces through it instead of skipping
      them.
- [ ] Decide what happens to `GET /_matrix/federation/v1/openid/userinfo`: it is
      indexed and unimplemented, and it exists for third-party invites, which are
      themselves deferred. Record the decision instead of leaving it unlisted.

Complement turns green: `TestRemoteAliasRequestsUnderstandUnicode`,
`TestInboundFederationProfile`, `TestOutboundFederationProfile`,
`TestFederatedClientSpaces`, `TestClientSpacesSummary`,
`TestClientSpacesSummaryJoinRules`.

### 13. Federation VIII — cross-server E2EE, device lists, to-device and media

The sinks for all of this landed in step 8; this is the producing and asking half.

- [ ] **Emit** `m.device_list_update` when a device uploads or deletes keys, with
      the per-user `stream_id`/`prev_id` chain on the counter `FederatedDevices`
      already counts, to the servers that hold that user in a room; and
      `m.signing_key_update` on the cross-signing changes the local paths already
      record.
- [ ] `POST /client/v3/keys/query` and `/keys/claim` for **remote** users: ask the
      owning server (`GET /federation/v1/user/devices/{userId}` is already served),
      verify its signature, cache, answer as if local. A `device_id` that is not
      there is asked for again, never invented.
- [ ] To-device to a remote user: queue for the destination server (inbound and the
      `*` expansion already work).
- [ ] Federation media: `GET /_matrix/federation/v1/media/download/{mediaId}` and
      `/thumbnail/{mediaId}/{width}/{height}` answered as `multipart/mixed` with
      **exactly two parts**, and the client-side `/download` and `/thumbnail`
      fetching from the owning server for an `mxc://` this server does not have,
      caching the result as a Media row.

Complement turns green: `TestDeviceListsUpdateOverFederation`,
`TestDeviceListsUpdateOverFederationOnRoomJoin`, `TestFederationKeyUploadQuery`,
`TestToDeviceMessagesOverFederation`, `TestContentMediaV1`.

### 14. Federation IX — a two-server suite that does not cheat, and the wrap

- [ ] Rewrite the in-repo two-server cases that seed `Room`/`RoomMembership` rows
      so the membership is the result of step 10's handshake; keep the seeded form
      where it tests storage rather than flow. The scenario table stays: join,
      invite→join, messages both ways, typing, receipts, to-device, remote
      profile/keys, leave, knock, backfill, soft-fail (ban, then an event
      referencing pre-ban state), transaction retry idempotence.
- [ ] Run both Complement packages and record the tallies here and in README:
      `make complement` and `make complement COMPLEMENT_PKG=./tests`.
- [ ] Regenerate `ENDPOINTS.md`: the federation count should move from 3/31 to
      near-complete, with the media-admin endpoints, the CS-protocol proxying, and
      whatever the deferred list above names still open.
- [ ] README's federation section: what `-federation` turns on, TLS and the
      certificate, `-federation-address`, `-federation-peer`, `-insecure`, and the
      fact that rooms federate by default with `-no-federation` to opt out.
- [ ] Decide `0.2.0` (client API complete) / `1.0.0` (with federation) tagging.
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

## Complement — the upstream suite (running since 2026-11-01)

Tally, complete run of 2026-11-02 against Complement `e3fcff1`: **40 of 106**
client-API tests fail (44 deployments, no panics), and **72 of 91** server-server
tests fail (108 deployments, no panics) — 19 server-server tests green, where the
run before the membership handshake had 4. The rest of this section is how to run
it, what the run can hide, and what every remaining failure is waiting for.

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
What the remaining 72 wait for, by group: `make_knock`/`send_knock` (knock and its
guards), the invite endpoint `PUT /v1|v2/invite` (`TestFederationRoomsInvite`,
`TestFederationRejectInvite`), the graph-reading queries `event_auth`, `state_ids`,
`state`, `get_missing_events`, `backfill`, `timestamp_to_event`
(`TestEventAuth`, `TestGetMissingEventsGapFilling`, `TestInboundCanReturnMissingEvents`,
`TestJumpToDateEndpoint`, `TestCorruptedAuthChain`), spaces and the hierarchy route
(`TestFederatedClientSpaces`, `TestClientSpacesSummary*`), profile and device-list
queries (`TestInboundFederationProfile`, `TestDeviceListsUpdateOverFederation*`),
media federation (`TestContentMediaV1`, `TestFederationThumbnail`), room versions
this server does not speak (`TestComplementCanCreateValidV12Rooms`), and the ACL tests
(`TestACLs`, `TestACLsForEDUs`) which need a room two servers share — which now works —
and the EDU paths that check it.

The 40, grouped by what is actually missing — each group is a work item, and the
suite's own skips are not hiding any of them:

| Missing | Tests |
| --- | --- |
| `unsigned.transaction_id` on the local echo of a `PUT /send` (`/sync`, `/event`, `/messages`) — the transaction log already stores it | TestTxnInEvent, TestTxnScopeOnLocalEcho, TestTxnIdWithRefreshToken |
| History visibility on `GET /rooms/{roomID}/event/{eventID}`: refuse non-members, and events from before the join | TestFetchEventNonWorldReadable, TestFetchHistoricalJoinedEventDenied, TestFetchHistoricalInvitedEventFromBeforeInvite, TestLeaveEventVisibility |
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
- `SPECS.md` references a spec corpus that is **not** in this workspace;
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

## Commands

```sh
make build vet lint                       # compiles, vet, golangci-lint
make test                                 # unit + compat suites (~45 s here)
make race                                 # same with -race
make endpoints                            # coverage report -> ENDPOINTS.md
make run                                  # dev server, 127.0.0.1:8008, /tmp/lattice.db
make swagger                              # docs/swagger/{swagger.json,swagger.yaml}
go test ./tests/compat/ -race
bin/lattice --addr 127.0.0.1:8008 --server-name localhost:8008 \
    --db /tmp/lattice.db --jwt-secret dev      # what `make run` executes
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
