# Step log — the release plan, worked one step at a time

Every step of Lattice's plan with what it found, in order. The prose here is the working record: the
`Findings worth not re-deriving` blocks and the `Openings this leaves` lists are why this file is kept
apart from [TODO.md](TODO.md), which now carries only the state and the queue. Steps 1-14 first, then
15-26 as they were appended. Nothing here has been rewritten; git holds the earlier drafts.


---

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


---

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

### 24. Reading a room you have left, and the filters a client asked for (2026-11-04)

The client-API suite was re-run to completion on 2026-11-04: **91 green of 106**, where the
table above records 40 red of 106 from 2026-11-02. Two of the three steps in this section are
the reason, and the third is the measurement that made the other two findable.

- [x] **A sync filter's allow-lists were never read.** `not_types` and `not_senders` were, and
      `types`/`senders` were not: a client that names the three event kinds it reads was sent the
      room's whole state and every membership in it, and a `limit: 0` timeline — a client asking
      for *no* events — was answered with the room's entire history, because an unset limit and a
      zero one both read as zero. `SyncFilter` now keeps the timeline and state sections apart,
      reads `types`/`senders` for each, and distinguishes an unnamed limit from a zero one.
- [x] **The `leave` section was rebuilt.** It used to be the room's timeline after the position,
      its current state, and no filter at all, so a former member was shown whatever had been
      said since they left and was shown it again on every sync afterwards. It is now: reported
      once, by the sync whose position sits before the leave; bounded at the leave; filtered like
      any other read; with its state block rebuilt as the room stood at the leave — a state key
      that moved afterwards is walked back through `StateHistory` to the version in force then,
      which is the same trick MSC2675 needed and the same absence of per-event snapshots this
      server keeps running into. `limited` is set by fetching one event more than the window asks
      for, and by a hole in this server's own copy of the room's history.
- [x] **A former member can read the room they were part of.** `canRead` let in joined users,
      invited users and strangers in a world-readable room, and refused everybody else — including
      the person who had just walked out of a public room, from `/state`, `/members` and
      `/messages`. Leaving ends what a member may see *now*; it does not unmake the past. Which
      events of that past are shown is the per-event question, and it is answered by the
      visibility gate: a room whose history opened at the join is read *as of* the leave, so
      `/state`, `/state/{type}/{key}`, `/members` and `/messages` are bounded at that position, the
      reader's own ending membership is always among them, and a stranger is still refused.
- [x] **A gap a chunked answer left open is now walked shut.** `get_missing_events` asks "what is
      between these two points" and a peer may answer with part of it; the events it did send name
      their own parents, and `GET /_matrix/federation/v1/event/{eventId}` asks for one by name.
      Following those names (`closeNamedHoles`) is what turned a room whose history arrived in
      pieces into one a client can read in order: `TestSyncTimelineGap`, fifty events deep, is
      green, and so is the flag it checks — `limited` is now true when the window reaches across a
      parent this server does not hold, which is a hole the room pointed at itself.
- [x] **`/event/{eventId}` refuses with 404, not 403** (step 23 said 403; Complement and the
      specification both say 404 and both are right). `/messages`, `/context` and the state reads
      keep the 403 that names the *room* as closed.

Openings this leaves, in the order they would be picked up:

- **`TestLeftRoomFixture` is one check from green**: the `/messages` chunk of a departed
  `joined`-visibility room. Every other check in it passes, including state as of the leave, the
  old room name, and the member list bounded to that point.
- **A bounded read keeps the room's own pagination cursors.** `/messages` on a departed room can
  answer a shorter chunk than it fetched, and `start`/`end` still name the window walked. That is
  deliberate — a client must be able to page on to a place it was told about — but it means a
  filtered page and its cursors disagree in size.
- **`/sync` and `TestSync` are load-sensitive.** Run alone, `TestSync` passes; inside a full
  parallel run it has timed out on `MustSyncUntil` more than once. Worth a look at whether our
  own long-poll wake-up is as prompt as it thinks it is.
- **The `members` bound is applied by the client-API handler**, not by the membership layer, so a
  federation or admin read of the same room is unbounded. Correct today (those readers are
  servers, not departed members) and worth remembering if that changes.

### 25. Lazy loading that is lazy, and the people a user asked not to see (2026-11-04)

The client-API suite went 91 → **95 green of 106** the same day, on the item coverage-comparison
ranks fifth. Four tests, one theme: what a response carries.

- [x] **`lazy_load_members` is honoured, and means what it says.** The flag was parsed under one
      spelling (with the inner key mistagged, so it never fired) and ignored everywhere else.
      Now: a `/messages` page whose filter asks for lazy members answers a state block of exactly
      the memberships its own senders need - the membership the room held for each of them at the
      point their newest event on that page was written, not the one it holds today - and a sync
      whose filter asks for them carries the memberships of the people its timeline speaks for,
      plus the reader's own. The room's other state is untouched in both: lazy loading is a
      promise about the member list, which in most rooms is most of the payload, not about the
      room. All three spellings clients use are read (`lazy_load_members` flat, the
      `room_event_filter` wrapper, and MSC4222's `lazy_load_options` block).
- [x] **MSC3023 is the server's job.** `m.ignored_user_list` was stored and never consulted. The
      words of somebody on the list are now not sent in a timeline, and an invite from them is
      not handed over at all - a room the client is invited to by somebody it hid is a room it
      would have to explain. Their *membership* events are still sent: a room a user can see has
      to be able to say who joined, left or was kicked, or it renders as a room where things
      silently happened.
- [x] **`/members?at=` answers who the room was made of then.** The same position-bound read the
      departed-room work needed, now on a token the client supplies: a membership that moved
      after the token is walked back through the room's own versions of that key, and somebody
      who joined later is not in the room the client is reading.
- [x] **A room's opening belongs to it from its first moment.** The token a client gets back from
      an initial sync lands *between* the create event and the creator's membership, because this
      server writes them as separate events a position apart. Read strictly, "who was in the room
      at that token" answers nobody - and a room with no members is not a room. Synapse models the
      creator's join as part of the state the create event carries; the equivalent here is depth:
      the earliest version of a state key written at depth 1 or 2 - the room, and what the room
      says about who made it - is in force at any point inside the room's own beginning.

Openings this leaves:

- **Lazy loading is computed per response, not remembered per device.** Synapse caches which
  memberships it has already sent to a device and does not resend them; this server re-derives
  the set from the timeline of each response. Correct, and heavier than it needs to be.
- **The ignore list is applied to sync timelines and invites only.** `/messages`, `/search` and
  `/relations` still carry an ignored user's words, and the state of a room is not filtered -
  deliberately for the state, not yet for the others.
- **`/messages?to=` is parsed nowhere.** A page asked with both ends gets everything from the
  `from` token forward; Complement's lazy-loading case happens not to notice because nothing
  later was sent.
- **`TestLeftRoomFixture` is still one check away** (the same `/messages` chunk as before), and
  `TestSync` has timed out under a full parallel run twice while passing on its own - worth a
  look at the long-poll wake-up.

### 26. Reading a room as of a point, when the room's history forked (2026-11-04)

`coverage-comparison.md` carried one line about a bug rather than a gap: *"we serve both, but our
walk is wrong in a forked history"*. Two halves, both fixed.

- [x] **`/context` answered today's state around a past event.** `stateEvents` took a position and
      never used it: it listed the room's current state rows. So the context of an event written
      under one room name carried the name the room adopted afterwards, and a membership that
      moved since was shown as the context of an event that never saw it. The same lie sat in the
      state block of a backward `/messages` page, which the code comment admitted ("what is sent
      is the room as it is now"). `stateAsOf` now answers each key with the version the room was
      carrying at the point asked, and keys the room had not written by then are absent.
- [x] **"as of" was decided by arrival order.** The version of a state key chosen for a position
      was the one with the highest *stream position* at or before it — the order this server
      persisted them. Two versions of one key can sit on two branches of the graph, both in the
      room, neither "later" in any sense the room agrees with; answering by stream position makes
      the state of a past event depend on which peer pushed first. The choice now follows the
      ordering state resolution uses for competing versions of one key: greater depth, then the
      later origin timestamp, then the greater event id (`newerByVersion`). The same room and the
      same event cannot give different answers because of delivery order.

Why this rather than per-event state snapshots: Synapse answers "the state at E" with a state group
per event, so it is a lookup. This server keeps one row per state key plus a version list, and the
as-of reads are rebuilt from that. Full resolution over the ancestry of E (fetching E's graph and
running the room's conflict rules over the candidate set) is the complete fix and is what
`resolveState` already does for the room's *current* state; the ordering rule above is the bounded
piece of it — it cannot order two versions whose depths this server cannot compare against E, and
where the room's history is itself a hole (step 15's graph gaps) the honest answer is the missing
key, not a confidently chosen one.

Openings this leaves:

- **No per-event snapshots.** As-of reads cost one lookup per state key. Correct, and heavier than
  a state-group table.
- **The ordering rule is resolution's tiebreak, not resolution.** A fork whose branches both
  survive to the queried event is decided by (depth, ts, id) over the two versions rather than by
  running the room's conflict rules over the branch's ancestry — the same answer in the cases the
  rulebook reaches, different in the ones where auth rules would veto one branch first.
- **`TestJumpToDateEndpoint` is still red**, and it lives in Complement's `tests` package, not
  `tests/csapi`, so it was never inside the 106-test tally the docs quote. Picking *which* event a
  timestamp names in a forked history is the same family of bug, one level up: it walks positions
  where it should walk the graph.
- **As-of reads are used by `/context` and backward `/messages`.** A sync's state block is still
  the room's current state minus the keys its timeline already carries, which is what the sync
  semantics ask for; the distinction is now carried by two named methods instead of one
  parameter that was ignored.

---

### 27. Two half-open ends closed: the state handshake, and the far end of a page (2026-11-04)

The queue in `TODO.md` opens with what the last steps left half-done. Two of them, both about
asking a question in the form the other side actually answers.

- [x] **The federation state handshake asked the wrong door.** Synapse registers
      `GET /_matrix/federation/v1|v2/state/{roomId}/{eventId}` — the event id in the **path** — and we
      registered only `GET /v3/state/{roomId}?event_id=`. A peer asking us the specified way was
      answered with nothing, and our own `peerStateAt` asked *peers* the query form, which they answer
      with nothing, dropping us to `state_ids` plus one `/event` fetch per state event and losing the
      auth chain resolution needs to weigh two claims about one key. We now serve v1, v2 and v3, and
      ask v2 (path, full events), then v1, then v3, before the ids fallback. The answer is unchanged;
      only the door.
- [x] **`/messages` never read its far bound.** `to` was parsed nowhere, so a client asking for the
      window between two tokens got everything from `from` forward — the rest of the room. The near
      token is exclusive (it has been read), the far token inclusive (it has not), in both directions.
      Complement's `TestRoomMessagesLazyLoading` passes `to=`: making the far bound exclusive turned
      it red immediately, making it inclusive turned it green again — the suite pinning the semantics.

Openings this leaves:

- **Token inclusivity elsewhere is unexamined.** Writing the test surfaced that a backward
  `/messages` walk from a sync's `next_batch` does not return the event written *at* that token. Same
  off-by-one family; it needs its own pass rather than a guess.
- **The v1 shape on a v3 path.** The handler answers v1's bare event on the v1 route and v2's object
  elsewhere, including v3. Untested, and not worth a third case until a peer asks for one.
- **The Complement server-server package is `tests`, not `tests/federation`.** Measured after this
  change: 78 green of 91, 13 red, listed in `TODO.md`. The "61 of 91" the docs had been quoting was
  stale by a day and several steps.
- **Gap filling is where the federation reds cluster** — three of the thirteen are
  `get_missing_events` cases and three more are rejected-auth-chain bookkeeping.
