# TODO / working state

Hand-off document for Lattice (a minimal Matrix homeserver in Go). One file, short on purpose:
what the server is, what is measured, and what is wanted next. Everything else moved out, so
this stays readable.

| File | What lives there |
|---|---|
| **[TODO.md](TODO.md)** (this file) | the measured state, the red list, the queue of what is wanted, and the one-line history of the 26 steps |
| [STEPS-LOG.md](STEPS-LOG.md) | every step worked in full — what it changed, the `Findings worth not re-deriving` blocks, and the `Openings this leaves` lists |
| [COMPLEMENT.md](COMPLEMENT.md) | how the two upstream Complement packages are run, what a run can hide, and the per-group failure record |
| [FINDINGS.md](FINDINGS.md) | verified facts and gotchas (layout, GORM/SQLite, federation ground facts, the harness), what needs a human or a network, what is deferred on purpose, the commands, the house rules |
| [RTC-PLAN.md](RTC-PLAN.md) | the MatrixRTC / LiveKit plan, with its own L-numbered steps |
| [coverage.md](coverage.md), [coverage-comparison.md](coverage-comparison.md), [spec-coverage.md](spec-coverage.md) | coverage area by area, ours; the Rust reference's document; and a reading of the two against each other |

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


## Where the server stands (measured 2026-11-20)

| Measure | Now |
|---|---|
| `make build vet` | clean (4 s, 2026-11-20); `make lint` reports the same **7 issues** (6 staticcheck: `QF1001`×3, `QF1006`×2, `S1011`; 1 `unused`: `internal/api/federation.go:1045 eventInBody`) — pre-existing quickfix-style findings, none from the 2026-11-18 work. Note for a session started from a service unit: `golangci-lint` lives in `~/go/bin`, which is not on that PATH — and on 2026-11-26 the chat session's environment had no `golangci-lint` installed anywhere on its PATH, so the lint tally above still belongs to 2026-11-20 |
| this repository's own tests | **378** counted here — 206 client-API compatibility, 90 two-server federation, 82 offline unit (`go test ./tests/<pkg> -list '.*'`, all green on 2026-11-26: compat 97 s, federation 50 s). `internal/matrix` adds its own test functions, which this tally has never counted |
| Complement `tests/csapi` | **84 green of 106**, 2 skipped, in 345 s on v0.2.18 (2026-11-25; 338 s on v0.2.17, 340 s on v0.2.16). Two names less red than the 2026-11-20 tally: `TestPushSync` passes since the push rule set travels as `m.push_rules` account data, and `TestSearch` passes since `/search` speaks the specification's body and answer. The other 20 names are the same list, so the red list is stable rather than lucky. It could not be run to completion before 2026-11-18, see [COMPLEMENT.md](COMPLEMENT.md) |
| Complement server-server (`tests`, not `tests/federation`) | **78 green of 91**, 13 red — re-measured 2026-11-20 for the first time since 2026-11-04, and the 13 are name for name the names recorded then, so the number was stale but honest. 338 s of own time; run beside the client-API suite it costs nothing extra, see `make complement-both` |
| endpoints | **180 of 234** indexed routes implemented, 44 routes the index does not name (`make endpoints`, unchanged on 2026-11-20) |
| spec | target v1.19 (the endpoint index it is built from), room versions 1-12 understood, new rooms default to v11; `/versions` advertises v1.0-v1.11 — see queue A |
| release | **v0.2.21** tagged 2026-11-26, the tag naming `f0b7816` — `ghcr.io/enimatek-nl/lattice` publishes `0.2.21`, `0.2` and `latest` (the `image` workflow built both architectures in 3 minutes, run `45845036360`); the live server on genius runs it (`:latest` pulled, image revision `f0b7816`, quadlet restarted: `NRestarts=0`) and `/_matrix/client/versions` answers `server_version: "Lattice/0.2.21"` with `v1.11` as its top version. What it carries over v0.2.20: the three override rules that were published without conditions and therefore matched everything — `.m.rule.member_event` and `.m.rule.reaction` with the specification's empty action list, `.m.rule.invite_for_me` with `type` + `content.membership` + `state_key` of the account's own Matrix ID behind `notify` and the default sound — those two no longer matched by identifier in `Matches()`, `.m.rule.master` with the empty action list the specification gives it, and a repair that takes a default rule's conditions back always and its actions only while the row still holds exactly what this server seeded. Read off the live server after the restart: rule set version 4, zero enabled override rules published without conditions, and the invite rule carrying `@tim:lattice.cybertim.net`. The same release fixed what v0.2.20 had opened, three rules deep: the content rule's missing `pattern` (ruma refused the whole rule set on it), the `pattern` field on rules that are not content rules, the sound tweak on the mention rules, the `contains_display_name` condition, a `PUT` of a rule no longer wiping the fields its body does not name, and a repair pass that reaches accounts written before the rules. The Complement tally above is **not** re-measured on these three releases: docker was unreachable from the sessions that built them, so 84 green of 106 is still the v0.2.18 number. What v0.2.19 carried over v0.2.18: the four default override rules of v1.7 and later that were never seeded — `.m.rule.is_user_mention`, `.m.rule.is_room_mention`, `.m.rule.room.server_acl`, `.m.rule.suppress_edits` — the three condition kinds they need, and a start-time repair that gives those rules to accounts written before them. What v0.2.18 carried over v0.2.17: `POST /account/deactivate` answers `{"id_server_unbind_result": "success"}` — the required field, and the three Synapse-legacy keys are gone — while `/account/password`, `/logout` and `/logout/all` answer `{}` |
| federation | live behind `-federation` (default off); one rulebook judges what this server signs and what a peer sends (step 20); keys of a peer that cannot publish its own are asked for at the key servers named by `-key-perspectives` (`matrix.org` by default) |

The 2026-11-04 tally of "95 green of 106" was not produced by one completing run — the package
never before completed, because `/messages` kept handing back an `end` at the start of a room and
`TestMessagesOverFederation` paged until the 40-minute ceiling. Two changes closed that (the
pagination answer saying when it is finished, and the Complement image no longer rate-limiting the
suite) and events now carry `unsigned.transaction_id` for the device that sent them; the number
above is what a single run prints. `TestTxnIdWithRefreshToken`, filed against the transaction ids,
was held up by `POST /v3/refresh` reading its token from the query string instead of the JSON body,
and is green since the same day. It also found the csapi tally was 15 names redder than this file
admitted, which is what the next section is for.

## What is red, and what each one is waiting for

`tests/csapi` (**22 red, 2 skipped**, first measured 2026-11-18 in the first run of this package that
finished, and reproduced name for name on 2026-11-20: 82 green, 22 red, 2 skipped in 409 s). Every name
now has a cause — the ones carried over from 2026-11-04 keep what they were waiting for, and the ten that
were labelled "not triaged" were each run **alone** and read on 2026-11-20 (the per-name table is in
[COMPLEMENT.md](COMPLEMENT.md), the measurements behind it in [FINDINGS.md](FINDINGS.md)):

| Test | Waiting on |
|---|---|
| `TestPresence` | a decision, not a gap — presence is stored and answered per user, never streamed (see [FINDINGS.md](FINDINGS.md)) |
| `TestRegistration` | registration detail the suite checks beyond password/dummy/token |
| `TestRoomDeleteAlias` | removing a non-canonical alias is gated on the canonical-alias power level |
| `TestRoomForget` | forgetting: what is dropped, and what a rejoin must find again |
| `TestDeviceListUpdates` | device-list change delivery at suite speed. "Passes on its own" is the 2026-11-04 note and it is unverified today: the name was not among the ten run alone on 2026-11-20, so treat it as a candidate rather than a known flake |
| `TestRoomImageRoundtrip` | media: URL previews / `POST /_matrix/media/v1/report` and the roundtrip the case asserts |
| `TestPowerLevels` | power-level corner the case pushes on |
| `TestLeftRoomFixture` | **one check away** — the `/messages` chunk of a departed `joined`-visibility room (step 24) |
| `TestMessagesOverFederation` | **backfill for a late joiner**: the walk now finishes and the early history is simply not on the server — the queue's "gap filling both ways" line, one floor down |
| `TestUrlPreview` | `GET /_matrix/media/v1/preview_url` (and the report endpoint the roundtrip test also wants) |
| `TestUploadKey` | three corners, measured on a live server 2026-11-20: `/keys/upload` answers **200** to a `device_keys` object without the required `algorithms`/`keys`/`signatures` (want 400 `M_BAD_JSON`) and **200** to one whose `user_id` names a different user (want 400); and `/keys/query` answers `{"device_keys":{}}` where the case wants the queried user present as an empty object — on that sub-case the sytest-era expectation is stricter than v1.19, which says an unknown user is "missing from the `device_keys` result", so it is a choice to match it, not a protocol gap |
| `TestSync`, `TestMembersLocal`, `TestPresenceSyncDifferentRooms` | `TestPresence`'s decision seen from `/sync`: `presence.events` is empty or absent, so a checker that waits to watch someone come online never fires. All three fail **alone** (2026-11-20), which retires the 2026-11-04 note that `TestSync` passes on its own |
| `TestRoomSummary` | no `summary` on a joined room in `/sync` — the case reads `m.joined_member_count` / `m.invited_member_count` and gets an empty object (triaged 2026-11-20) |
| `TestPushRuleRoomUpgrade` | no global `account_data` section in `/sync` either, so the case never gets past reading the rules before the upgrade — the carry-over it is about is unexercised (triaged 2026-11-20) |
| `TestSyncTimelineGap` | **its subtests pass**; the `limited` flag this name stood for is closed. The parent is red because the mock federation server (`UnexpectedRequestsAreErrors=true`) is asked `GET /federation/v2/state/{roomId}/{eventId}`, then `v1/state/…`, then the `v3/state/{roomId}?event_id=` form (`internal/service/fedgap.go:415`) — after a join that already carried the state (triaged 2026-11-20) |
| `TestThreadsEndpoint` | `/threads` answers the roots in the right order but bundles nothing: no `unsigned."m.relations"."m.thread".latest_event` (MSC3856). The comparison is on that pair, so ordering is not the problem (triaged 2026-11-20) |
| `TestThreadedReceipts`, `TestThreadReceiptsInSyncMSC4102` | no longer the push rule probe (closed 2026-11-20, see the queue): each now fails on its own subject. `TestThreadedReceipts` wants **MSC3771 thread unread counts** — its own unthreaded numbers already arrive exactly as asked (`{"notification_count":6,"highlight_count":2}`, measured on the response it checks), what never arrives is `unread_thread_notifications` per thread. `TestThreadReceiptsInSyncMSC4102` wants an unthreaded and a threaded read receipt **side by side** for one event; our `/sync` carries only the threaded one, because a receipt is stored per (user, event) and the threaded write replaced the unthreaded one — the suite's own comment calls out this exact failure mode ("Non-compliant servers will typically send the last one only") |

Server-server, the `tests` package (**13 red of 91** measured alone, 14 when it runs beside
the client-API suite — `docs/COMPLEMENT.md` has the two names that move with the load; this
package also holds `TestJumpToDateEndpoint`, which is why it never showed up in the 106):

| Test | Waiting on |
|---|---|
| `TestGetMissingEventsGapFilling`, `TestOutboundFederationEventSizeGetMissingEvents`, `TestOutboundFederationIgnoresMissingEventWithBadJSONForRoomVersion6` | the gap-filling answers a peer checks shape-for-shape |
| `TestInboundFederationRejectsEventsWithRejectedAuthEvents`, `TestCorruptedAuthChain`, `TestUnrejectRejectedEvents` | rejected-event bookkeeping: what we refuse because its auth chain is refused, and what we hand back about it |
| `TestFederationRedactSendsWithoutEvent` | redacting an event the peer has never seen |
| `TestFederationRoomsInvite` | the invite handshake as the suite drives it |
| `TestACLsForEDUs`, `TestRemotePresence` | server ACLs applied to EDUs; presence across servers (presence is stored per user, never streamed) |
| `TestIsDirectFlagFederation` | the `is_direct` flag surviving a cross-server invite |
| `TestJoinFederatedRoomFromApplicationServiceBridgeUser` | application services (queue B) |
| `TestJumpToDateEndpoint` | `timestamp_to_event` walking stream positions instead of the graph |
| `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room` | a restricted join redirected to another server, in a room of the MSC3787 version — the name measured green and red in two runs of the same commit an hour apart under the parallel preset (2026-11-20), so confirm it with `make complement-fed` alone |

The state-handshake change (step 27) is federation-only and did not move this tally: the package
measured 78 green with it in.

While the csapi names above stay red they set the price of a regression check, which is why
`make complement-fast` exists: the same 82 green without the 22 waiting names, in 159 s
instead of 409. It skips by `deploy/complement/blocked-csapi.txt`, a copy of the first table
that says so in its own header — when a row closes, that file has to lose the name too.
`make complement-both` runs this package and the client-API one at once, which is safe (one
Complement namespace per package) and costs 373 s for both where running them in turn costs
their sum.

## The queue — what is wanted, grouped

Order within a group is the order I would take them in; the groups are in rough order of what they
unlock. `- [x]` marks what this plan has already closed, with the step that did it.

### A. Things left half-open by the last steps (cheapest real wins)

- [ ] Decide what `/versions` may claim: the list stops at **v1.11** while the endpoint index this
      server is written against is **v1.19**. Naming a revision claims every stable addition it
      introduces, so raising it means going through v1.12 to v1.19 one revision at a time and
      marking the ones that are genuinely covered — a claim, not a number. Neither suite changes
      either way (this Complement gates nothing on `/versions`), which is exactly why nobody has
      been forced to check (2026-11-18).
- [ ] The `/messages` chunk of a departed `joined` room — `TestLeftRoomFixture` is one check from green (step 24).
- [ ] `TestMessagesOverFederation`: **backfill what a late joiner never had.** The pagination walk
      finishes since 2026-11-18 and finds the room's early history missing, which is a peer read
      this server does not make: a member who joined after the events were written is entitled to
      them under `shared`, and nothing asks a peer for them. Same family as the gap-filling line
      below, one floor down (queue C).
- [x] `/messages` says when the walk is over: `end` is omitted once there is nothing further — at
      the beginning of the room and at a `to` bound — instead of repeating `from` for as long as the
      client is willing to ask. A conforming client stops on the absence, so an always-present `end`
      was a loop, and an expensive one: it pinned the suite for 40 minutes (2026-11-18).
- [x] The federation state handshake in both directions: this server now serves the v3 query form (`GET /_matrix/federation/v3/state/{roomId}?event_id=`) as well as the v1/v2 path forms, and asks peers v2 → v1 → v3 before falling back to `state_ids` plus a fetch per event. Asking only the query form was answered with nothing by a server that has the state and not that route (step 11's machinery, closed 2026-11-04).
- [ ] `TestJumpToDateEndpoint`: `timestamp_to_event` should walk the graph, not stream positions (step 26).
- [x] `/messages?to=` is honoured: `from` is exclusive and `to` inclusive in both directions, so a window asked by its two ends returns that window and not the rest of the room (closed 2026-11-04; `TestRoomMessagesLazyLoading`, which passes `to=`, is green again).
- [ ] Token inclusivity around `from` is worth a pass: a backward `/messages` walk from a sync's `next_batch` does not return the event written *at* that token — the same class of off-by-one the last `TestLeftRoomFixture` check sits next to.
- [ ] MSC2675 is not applied to `/threads`, `/members?at=` or `/state/{eventId}` (step 23).
- [ ] `m.ignored_user_list` filters sync timelines and invites only; `/messages`, `/search` and `/relations` still carry an ignored user's words (step 25).
- [ ] Lazy loading is recomputed per response instead of remembered per device (step 25).
- [ ] As-of state reads cost one lookup per key; the complete answer is resolution over the event's ancestry, or per-event snapshots (step 26).
- [x] `TestSync`'s "times out inside a full parallel run while passing on its own" — run alone on 2026-11-20 it fails in 22 s on `presence.events is empty`, and the full package run the same day fails it too. Nothing to do with the long-poll wake-up: it is the presence decision, and it is shared with `TestMembersLocal` and `TestPresenceSyncDifferentRooms`.
- [x] An unknown push rule **kind** answered `400 M_INVALID_PARAM` where an unknown rule answers `404 M_NOT_FOUND` (closed the same day it was measured, 2026-11-20). A kind is a namespace: asking for a rule in a namespace this server does not have is the same question as asking for a rule that is absent, and a client probing "does this rule exist?" can only tolerate the second answer. Reading it also exposed two neighbours: the three mutations of a rule (`DELETE`, `…/enabled`, `…/actions`) answered **`200 {}` for a rule that is not there at all** — a gorm update matching no row reports nothing — and creating a rule *inside* an unknown kind is a bad request rather than a missing resource, so that one keeps `400`. `tests/compat/push_test.go: TestPushRuleNotFoundSaysSo`.
- [ ] `/keys/upload` validates nothing about the device key object: a `device_keys` without `algorithms`/`keys`/`signatures`, and one whose `user_id` names another user, are both stored with 200 (measured 2026-11-20). Two checks, and an unverifiable key document stops reaching the peers that query it.
- [ ] `/threads` carries no MSC3856 bundle: the roots come back in the right order without `unsigned."m.relations"."m.thread".latest_event` (and so without the per-thread `count` that comes with it).
- [ ] A read receipt is stored per (user, event), so a threaded `m.read` **replaces** the unthreaded
      one instead of joining it: `/sync` then reports one where the spec keeps both (MSC2456 receipts
      are keyed by `thread_id`, `"main"` naming the unthreaded one). Measured 2026-11-20 on
      `TestThreadReceiptsInSyncMSC4102`, whose ephemeral `m.receipt` carries only the entry with a
      `thread_id`. Fixing the key is what that name and MSC3771 below both wait on.
- [ ] MSC3771 thread unread counts: `/sync`'s `unread_thread_notifications` (a map from thread id to
      the same two counts, present only when the filter asks) is not computed. The unthreaded side is
      — with MSC3771's filter on, the numbers `TestThreadedReceipts` asks for arrive correctly, which is
      how the missing half was pinned down (2026-11-20). The wire type is right now (a map, so an empty
      one is genuinely absent); the counting is what is left.

### B. Structural gaps — what other software needs in order to attach

- [ ] **Application services** — registrations and namespaces, `as_token` auth with `user_id`/`device_id`, transaction delivery with ephemeral data, user/alias queries. Nothing bridging works without it, and it is how a bot holds an identity here.
- [ ] **Push delivery** — `POST /_matrix/push/v1/notify` as a *client* of a push gateway, with `event_id_only` and rejected pushkeys. Rules, pushers and counts already exist: this is one outbound request loop away from a phone ringing.
- [ ] **An admin surface** — user list/get/create/update/deactivate/reset, room list/details/delete, force-join, purge. Today: four routes and the CLI.
- [ ] **Authentication that is not a password** — `/login/sso/redirect`, `m.login.token` from an identity provider, MSC2965 discovery, `POST /v1/login/get_token`.

### C. Federation and interop

- [x] **Audit the federation responses for the re-marshal bug class (T-4014).** Answered 2026-11-22, and
      it found something — in the **store** path, not the response path: an inbound PDU is stored with
      `matrix.Encode` (plain `json.Marshal`, which escapes `&`, `<` and `>`), so the bytes served later are
      not the bytes the origin signed. The response side is safe by construction. Fix and test in queue H.
- [ ] Wire `OutboxRepo.Prune` to maintenance. The queue keeps what a peer never received, which is
      right for an hour and permanent for a month: a room holding members of servers that disappeared
      re-queues device list updates for them forever, and each of those is an attempt some live peer
      waits behind. The repo method exists and takes an age; nothing calls it.
- [x] **The queue's backoff never engaged, and that starved the peers that answer.** The jitter was
      drawn in nanoseconds and subtracted as milliseconds, putting the next attempt up to four and a
      half years in the past: five gone-away servers were due on every flush, held the oldest work, and so
      took every destination slot a message to a live server could have used. The delay is now one
      exported, tested function (`service.OutboxRetryDelay`), the pass bound covers both of
      `DueDestinations`' sources instead of one, and queued events go before ephemeral messages
      (2026-11-18, found from `retried in -15278539382ms` in a live log).
- [ ] A join has no budget of its own. A key document is fetched under ten seconds and a server that
      answered nothing is asked again only after a hold-back, but the first pass over a room whose
      state names five gone-away servers still waits for each of them one after another. The complete
      answer is one deadline for the whole verification, shared by every key it needs — what a client
      deserves is "this room cannot be verified, here is which server", not a longer wait.
- [ ] Do not ask a peer for state a join already handed over. After a `make_join`/`send_join` that carried
      the state, the v2 → v1 → v3 chain at `internal/service/fedgap.go:415` still walks the state endpoints
      — three round trips per join on a live peer, and Complement's mock counts each of them as a protocol
      error, which is the only reason `TestSyncTimelineGap` is red now that its assertions pass
      (triaged 2026-11-20).
- [ ] An outbound **address policy**: what a name may resolve to before this server dials it.
      `internal/federation/transport.go:208` is a bare `transport := &http.Transport{}` — default
      resolver, default dialer, nothing refusing loopback, link-local or RFC1918, and nothing pinning
      the address between looking a name up and connecting to it, which is the half that matters: a
      name answering differently the second time is the classic way past a check made the first. A
      peer's `.well-known`, a key perspective's answer and every federation destination go through it.
      Found auditing the abandoned `retry` branch's open tasks against this tree
      (`.agent-work/retired-retry-line-20261109/TASK-AUDIT-20261120.md`, 2026-11-20) — the one gap in
      there this line neither has nor had written down. It belongs with the first fetch of a URL this
      server did not choose, and that is URL previews: `internal/api/media.go:188-192` refuses them
      today with "URL previews need an outbound fetcher, which Lattice does not have", so the policy
      and that endpoint should arrive together (queue D).
- [ ] Verify the key server's own signature over a `keys/query` answer. What is checked today is that
      each document names the server asked about and is signed by the keys it publishes; the signature
      over the aggregate response is what tells this server that the answer came from the key server
      it named rather than from something between them (TLS covers the wire today, so this is closing
      the trust boundary rather than opening a hole).
- [ ] Answering a key query about *this* server's own name should read the local keyring. Asked by a
      peer acting as a notary, the lookup goes out over the federation port and lands on this server's
      own listener — which needs `-key-perspectives` to name a server that is not this one, and costs
      a round trip through the network for an answer held in the database.
- [ ] Rejected-event bookkeeping (`/missing_events` and the rejected-event routes) — four Complement cases.
- [ ] Federation `POST /v1/publicRooms` at v1 (both public-room routes were registered at `/v3`).
- [ ] Verify the authorising server's signature on a restricted join, rather than trusting the claim.
- [ ] Refresh the server-server red list and re-rank what the 30 remaining cases are waiting on.
- [x] `PUT /v1|v2/invite/{roomId}/{eventId}` — served, with invite rejection and rescinding (verified 2026-11-04; the gap lists that claimed otherwise were stale).
- [x] Gap filling both ways, and closing a hole a chunked answer left by fetching the missing parents by id (steps 11, 24).
- [x] **Keys of servers that can no longer publish them** — `-key-perspectives` (default `matrix.org`) asks a key server after the peer's own answer failed, holds its answer to the same discipline (names the asked server, signed by the keys it publishes), and believes an expired document only for events written before its own expiry. A key fetch now has its own ten-second budget, one flight per server, and a remembered hold-back after a failure: what a dead name costs is one short wait, not a timeout per event (2026-11-18, and the reason a room holding `rixo.nl` or `tessera.oele.net` events could not be joined at all).
- [x] Knocking, restricted rooms and the space summary that authorises them, both directions (steps 17-19).

### D. Client surface a user feels

- [ ] `/sync` response shape: a `summary` on joined rooms (`m.joined_member_count`, `m.invited_member_count`, `m.heroes`). The global `account_data` section is answered and now carries the rule set as `m.push_rules` (2026-11-24); what is left of the case it hid is the upgrade transfer in queue F.
- [ ] Decide the `/keys/query` answer for a user with no devices: the suite wants the user present as an empty object, v1.19's wording ("the corresponding user or device is missing from the `device_keys` result") reads as a defence of omitting them. Match the suite or record the refusal (measured 2026-11-20).
- [ ] `state_after` on `/sync`; the top-level `presence` and `org.matrix.msc3814.e2ee` sections filled or removed.
- [ ] `DELETE` on account data (MSC3391), `m.fully_marked` where it is still missing, `/relations` recursion.
- [ ] A real search index (full text) instead of substring matching: stemming („kilns” for „kiln”) and a rank that is not a word count. The words of a term are already AND-ed, `Complement/TestSearch` is green, and paging, context and field keys work.
- [ ] `POST /_matrix/media/v1/report`, and URL previews — and previews come second to the outbound
      address policy in queue C, because a preview is this server fetching a URL a client typed.
- [ ] Decide what an upgraded room may do afterwards. Nothing on the path that accepts an event reads a
      tombstone: `Predecessor` appears in this tree only where a room is created
      (`internal/service/directory.go:397`), so a room whose own state says "use the other room" keeps
      accepting events. Refusing them changes behaviour for clients that have not seen the tombstone, so
      it is a decision first and a patch second — and the federated half rides with it: a tombstone
      arriving over federation, and whether a user's push rules move to the new room with them. From the
      `retry` branch's open list, audited 2026-11-20.
- [x] Lazy loading that is lazy on `/sync` and `/messages`, MSC3023's ignore list applied server-side, `/members?at=`, departed rooms read as of the leave, sync filters honoured, the `leave` section rebuilt, MSC2675 per-event visibility (steps 23-25).

### E. The MSC surface (where client features arrive first)

- [ ] Delayed events (MSC4140), thread subscriptions (MSC4306/4308) — the `postcontent` push rule kind is
      still wanted (it is what those rules live under), but it is no longer what stops
      `TestThreadedReceipts` and `TestThreadReceiptsInSyncMSC4102`: since 2026-11-20 their setup
      probe is satisfied and they fail further along, on thread unread counts in `/sync` — see
      `TestThreadsEndpoint`'s row above. Invite filtering (MSC4155), relation filters in
      `/messages` (MSC3874), poll push rules (MSC3930).
- [x] Simplified sliding sync (MSC4186) at `/v4/sync` and the `org.matrix.msc3575` path, with the five extensions (step 22).
      Since 2026-11-22 `pos`, `timeout` and `set_presence` are also read from the query string where the body leaves
      them out, which is what a matrix-rust-sdk client sends — see the finding of that afternoon.
- [x] Sliding sync read the way a proxy-era client writes it (2026-11-22): `required_state` as `[type, state_key]`
      pairs or `"type|state_key"` strings beside the MSC object, `ranges` beside `range`, the `is_invite` filter name
      beside `is_invited`, and the three parameters in the query string beside the body. Measured against ruma 0.25.2
      and asked for by smaltino's author. Read from the request only — an answer is written in one shape, the MSC's.
- [x] Stripped state always carries `state_key` (2026-11-23): it is **Required** by the spec and `omitempty`
      erased it for `m.room.create` / `m.room.name` / `m.room.join_rules`, whose state key is `""` — ruma dropped the
      events, so an invitation arrived nameless on every path. `/versions` also advertises
      `org.matrix.simplified_msc3575.stable` beside the unstable flag, which is how matrix-rust-sdk chooses
      `POST /v4/sync` over the unstable URL. Both measured, `TestSlidingSyncStrippedStateCarriesAStateKey`.
- [x] Sliding sync delivery bugs from smaltino's lab, 2026-11-24, three in a row: the ephemeral extensions now cover
      the connection's window rather than only the rooms the response happened to report (typing, read markers and room
      tags were unreachable and the long poll timed out); `limited` reads what the client missed since the last sync
      instead of the room's depth (one new message no longer resets a client's timeline); and a widened
      `required_state` reaches the client, with the connection's record of shown state made cumulative so sending old
      state once does not mean sending it again every round. `TestSlidingSyncEphemeralSectionsWakeTheLongPoll`,
      `TestSlidingSyncLimitedMeansMissedEvents`, `TestSlidingSyncSubscriptionSendsTheStateItJustStartedWants`.
- [x] A private read receipt (`m.read.private`) is delivered only to the user who sent it (2026-11-24). It had been
      stored right and delivered to the whole room in every sync shape; the viewer is now a parameter of the one
      function that builds `m.receipt` ephemeral events, and federation was measured to have been clean already.
      `TestPrivateReadReceiptReachesOnlyItsOwner`.
- [x] Two answers fixed 2026-11-24, both from the same lab: presence of a user who does not exist is
      `404 M_NOT_FOUND` rather than an idle-looking `200` with `last_active_ago: 0`, and `GET
      /room_keys/keys/{roomId}` answers `200 {"sessions": {}}` for a room with nothing backed up, leaving the 404
      for a backup version that does not exist — the request a restoring client makes for every room it is invited to.
- [x] `POST /search` speaks the specification (2026-11-25). It had been built on a draft this repository
      invented — terms read from `search_key`, answers shaped as `hits`/`limits`/`groupings` — so every client
      following the published request was told no search term had arrived. Terms, the filter with its `limit`,
      `keys` (through `json_extract`), both orderings, `next_batch` from body or query, and `event_context`
      windows now follow it, and one result carries the event itself. A term is a list of words, MSC2675 applies
      to search the way it does to `/messages`, and the invented body is gone. `Complement/TestSearch` green; six
      search tests rewritten.
- [x] Batch tokens name a stored position, never the counter that hands positions out (2026-11-25). The counter
      advances before the row carrying the position is stored, so a sync reading in that window answered from a
      position it had not seen the change of and every later token skipped it — measured as one bound reading no
      changes and, a round later, one. `TestSyncAnswersFromAStoredPosition`; lab-confirmed the same day: the
      reporter's repro lost 0 of 36 accounts against 2 of 12 on the previous release, and their core suite went
      from 53 to 55 of 78 with the two load-dependent failures passing.
- [x] The four default override rules that v1.11 defines and this server never seeded
      (reported 2026-11-26 by the author of smaltino, fixed the same day; v0.2.19).
      `GET /pushrules` published eight override rules where v1.11 defines twelve: `.m.rule.is_user_mention`
      and `.m.rule.is_room_mention` (v1.7), `.m.rule.room.server_acl` (v1.4) and
      `.m.rule.suppress_edits` (v1.9). What a person felt was the switch an Element account uses
      for "notify me when the room is mentioned" answering `404 M_NOT_FOUND`, and their core suite
      failing on a mention that arrived without its highlight. Four things had to move together:
      1. `internal/push` grew `event_property_is`, `event_property_contains` and
         `sender_notification_permission`, and a property-path reader that honours the escaped dots
         of the appendices — these rules read `content.m\.mentions.room`, one property called
         `m.mentions`. `Matches` takes a `push.Context` now, because a condition can name six things
         about a room and a seventh positional parameter reads as a mistake at every call site.
      2. The four are seeded where the specification puts them, with their conditions **stored**.
         `.m.rule.is_user_mention` carries the recipient's own Matrix ID inside its condition: a
         rule that is a row has no other way to say "me".
      3. Accounts older than a rule are repaired at start, because defaults are rows written once at
         registration and a suite that registers cannot see what that costs a running server.
         `User.PushRulesVersion` names the set an account holds and `RepairDefaultPushRules` adds
         what is missing, once per account per set — which is also what keeps a rule a person
         deleted deleted. The `m.push_rules` account data is republished for the accounts it touched.
      4. Measured over two binaries with one script (`.agent-work/v111/probe.py`, its output in
         `before.txt` and `after.txt` beside it): eight rules and a 404 become twelve and a 200; a
         mention of a user's Matrix ID highlights instead of merely notifying; an `@room` mention
         from a room administrator highlights; and an edit (`m.relates_to.rel_type` of `m.replace`)
         stops notifying at all.
      `TestDefaultOverrideRulesAreTheOnesTheServerAdvertises`, `TestMentionsNotifyTheWayTheirRulesSay`,
      `TestAnAccountWrittenBeforeTheRulesIsGivenThem`, and `TestPropertyValuePaths` beside the new
      condition kinds in `TestRuleMatches`. Complement was **not** run for this change: docker is not
      reachable from the session that wrote it, so the 84-of-106 tally above predates it.
- [x] The content rule a Rust client cannot read (reported 2026-11-26 by the author of
      smaltino from the v0.2.19 lab run, fixed the same day; v0.2.20). `.m.rule.contains_user_name`
      is this server's only content rule and it was seeded **without conditions**: `pushRuleWire`
      reads a content rule's `pattern` back out of a stored `content.body` condition, so the answer
      for the content kind carried no `pattern` at all. Ruma — the Rust SDK behind smaltino and
      Element X — stops parsing the **whole** rule set on `missing field 'pattern'` and falls back
      to its own defaults, which is the entire reason a second device of an account stopped showing
      what the first one changed: the account data and both syncs carried the change, and the reader
      threw it away.
      1. The rule is seeded with the condition its pattern is — `content.body` matched against the
         account's own local part, which is what the specification's
         `"[the local part of the user's Matrix ID]"` asks a server to substitute — and both mention
         rules now carry the sound tweak of `default` that v1.11 defines beside the highlight.
         `.m.rule.contains_display_name` is seeded with the one condition v1.11 gives it,
         `{"kind": "contains_display_name"}`, with no key and no pattern. That kind was already
         implemented in the matcher, which is what a person copying the published condition into a
         rule of their own depends on.
      2. `pattern` is published for content rules and for nothing else, and for every content rule:
         one with no pattern to show publishes an empty one, because presence is what the strict
         reader asks for. `.m.rule.roomnotif` therefore no longer publishes a `pattern` beside its
         conditions — the old shorthand looked at the condition and never at the kind.
      3. Existing accounts are repaired. The conditions of a default rule are the one part of it no
         endpoint writes, so `RepairDefaultPushRules` takes the server's own copy back for rules
         flagged `default`, with `defaultRulesVersion` at 3. Actions and the enabled flag are not
         touched, so what a person switched off stays switched off.
      4. A `PUT` of a rule that exists keeps the three things its body never says: the place in the
         rule order, the `default` flag, and — for a default rule — the conditions. Writing the test
         found two bugs the report did not: changing a default rule's actions used to wipe its
         conditions, which turns `.m.rule.suppress_notices` into a rule matching every event, and
         used to clear the flag, which makes a rule of the server a rule of the person in every
         client that asks.
      Refusing a content rule without a pattern was considered and dropped: the request schema of
      `PUT` requires `actions` and nothing else, so a client is allowed to send a content rule with
      conditions and no pattern, and a 400 there would be this server's own rule. What it can
      promise is that its own answer always parses, which is what two and three do.
      Measured against the release the report was made on — same test file, a `v0.2.19` worktree:
      twelve assertions fail there and pass here. No pattern, no sound tweak, a pattern on an
      override rule, a default rule with no conditions left after a `PUT` of actions, `default`
      cleared to false, an account written before the change still reading no pattern, and the
      `m.push_rules` account data without it either.
      `TestContentRulesCarryThePatternTheirKindRequires` and
      `TestADefaultRuleKeepsTheConditionsOnlyTheServerWrites`; `.agent-work/v111/wire.py` prints the
      answer of a fresh account. Complement and `make lint` were unavailable to this session too.
- [x] The three override rules that silenced every notification on a Rust client (reported
      2026-11-26 from the v0.2.20 lab run, fixed the same day; v0.2.21). This one is the cost of the
      item above: as soon as ruma could read the rule set it began to obey it, and the rules it read
      were three override rules published **without conditions** — `.m.rule.member_event`,
      `.m.rule.reaction`, and `.m.rule.invite_for_me` with a `member_count` condition the
      specification does not define. The specification is blunt: "A rule with no conditions always
      matches". An override rule with an empty action list, third in a list of twelve, is an off
      switch; every event a Rust client looked at landed on it.
      Measured by evaluating the published answer the way a client does
      (`.agent-work/v111/rules-eval.py`, the specification's algorithm: override, content, room,
      sender, underride; first match wins, last underride wins):

      | event | on v0.2.20 | on v0.2.21 |
      |---|---|---|
      | a plain message | `.m.rule.member_event`, `dont_notify` | `.m.rule.message`, notify + sound `default` |
      | an encrypted message | `.m.rule.member_event`, `dont_notify` | `.m.rule.encrypted`, notify + `count` |
      | an invite for the recipient | `.m.rule.member_event`, `dont_notify` | `.m.rule.invite_for_me`, notify + sound `default` |
      | an invite for somebody else | `.m.rule.member_event`, `dont_notify` | `.m.rule.member_event`, `[]` |
      | a reaction | `.m.rule.member_event`, `dont_notify` | `.m.rule.reaction`, `[]` |

      What it took:
      1. Store the conditions v1.11 defines, naming the recipient where the specification names the
         recipient: `type` = `m.room.member` for the member rule, `type` = `m.reaction` for the
         reaction rule, and `type` + `content.membership` = `invite` + `state_key` = the account's own
         Matrix ID for the invite rule. The two silence rules get the specification's empty action
         list, the invite rule `notify` + sound `default`. `member_count` as a condition of its own is
         not in the specification and is gone.
      2. Stop matching those two by identifier in `Matches()`. They are conditions now, and an empty
         action list is what keeps a client asleep — which a client evaluating the published rules
         itself has to be able to reach.
      3. Repair the accounts again, with `defaultRulesVersion` at 4. A default rule's conditions are
         the server's and are taken back always; its actions are the person's and are taken back only
         while the row still holds exactly what this server seeded for it (`seededWrongActions`),
         which is the one case in which the server can tell nobody chose them. That is what gives the
         invite rule its sound and the silence rules their empty lists without undoing a settings
         screen.
      4. `.m.rule.master` publishes the empty action list of the specification instead of
         `dont_notify`. It is the one rule the specification defines without conditions, and it is
         switched off by default for the reason this item is about.
      `TestTheRulesThatSilencedEverythingCarryConditions` ends on the invariant, the check that would
      have caught this before a client did: **no enabled override rule is published without
      conditions.** `TestAnAccountWrittenBeforeTheConditionsIsGivenThem` covers the same rules on an
      account written by the previous release, including the rule whose actions that account had
      changed and keeps. Six rows in `TestRuleMatches`, one of which pins the divergence that hid all
      of this: this server reads a rule without conditions as matching only underride rules, where
      the specification has it match everything — so this server never saw the silence it published.
      Complement and `make lint` were unavailable to this session too.
- [ ] A rule without conditions always matches, and this server disagrees (spun off from the item
      above on 2026-11-26; not built). `Matches()` reads an empty condition list as matching only for
      underride rules; the specification has it match every event. The consequence is two-sided, and
      both sides were measured on 2026-11-26: a person writing an override rule without conditions to
      mute everything gets silence on a Rust client and a server that keeps notifying, and the rules
      this server published without conditions silenced every event on that client while nothing here
      noticed — which is the incident above, and why `TestRuleMatches` now pins the behaviour and
      `TestTheRulesThatSilencedEverythingCarryConditions` enforces the invariant instead. Following the
      specification means a rule that is switched on and names no condition matches everything, so the
      change wants a Complement run it cannot get from a session without docker, and it wants the
      default set read once more first: `.m.rule.master` is the specification's own condition-less
      rule, and this server also reads it as an off switch that is never thrown.
- [ ] The rest of what v1.17 says the default rules are (split off from the v1.11 item above
      on 2026-11-26; not built). What is left is what a newer specification *changes* rather than adds,
      and the report agrees v1.11 may keep it: `.m.rule.contains_display_name`, `.m.rule.roomnotif`
      and `.m.rule.contains_user_name` are deprecated but still defined, while `.m.rule.default` and
      `.m.rule.notice` are seeded here and are in no ruleset at all. Read off the rules as this
      server publishes them: `.m.rule.notice` is still hard-coded in `Matches()`, so it publishes a
      condition it does not obey — the cleaner reason to delete it is that it is in no ruleset of the
      specification at all; `.m.rule.roomnotif`
      is seeded as `dont_notify` on one condition where v1.11 gives it `notify` plus `highlight` and a
      `sender_notification_permission` beside the `@room` word; `.m.rule.invite_for_me` names a
      condition kind that does not exist — and membership events are never evaluated anyway, because
      the notification path reads `m.room.message`, `m.room.encrypted` and `m.sticker` and nothing
      else; and `.m.rule.master` is read as "never matches" where the specification has it match
      every event with an empty action list, which is exactly what makes switching it on the way to
      turn an account quiet.
- [x] `POST /account/deactivate` answers with the body the specification has (2026-11-25). It did the
      work and answered three Synapse-legacy keys (`id_server`, `3pids_disconnected_json`,
      `unbind_credentials`) while leaving out the **required** `id_server_unbind_result`, so the Rust SDK
      could not read the answer and a client reported failure for an account that was deleted. The value is
      `success`: `account_deactivation.yaml` says it "must be `success` if the homeserver has no identifiers
      to unbind for the user", and Lattice stores no identifiers at all — `no-support` is for an identifier
      that failed to unbind. The legacy keys are gone, and `POST /account/password`, `/logout` and
      `/logout/all` now answer `{}` as their schemas name no fields.
      `TestAccountAnswersCarryWhatTheSchemaNames`.
- [x] `POST /account/password` answers a request without credentials with the interactive auth
      challenge (2026-11-25). It answered `403 M_FORBIDDEN "the old password was incorrect"`, and a
      client that follows the specification sends that bare request first and only asks the person
      for the old password after a `401` — so the change stopped there, blamed the client's own
      password and never asked for it. Every refusal of the endpoint is a challenge now
      (`authorizePasswordStage`, cut out of the cross-signing exchange so both bodies match),
      including a wrong `auth` and a wrong deprecated `old_password`. `TestChangePasswordInteractiveAuth`.
- [x] `timestamp_to_event` takes the epoch as an answer to search from, not as bad input
      (2026-11-25). `ts <= 0` was the guard on both the client side and the MSC3030 federation
      side, so `ts=0` — the epoch, before everything in the room, and what a client asks for
      when it wants the first event — came back `400 M_INVALID_PARAM` where `ts=1` answered
      normally. Absence and zero are different questions now: `numberFieldOK` reports whether a
      number was there, `numberField` keeps answering 0 when it was not.
      `TestTimestampToEvent`, `TestAPeerCanNameAnEventByTimestamp`.
- [ ] Sliding sync answered the way a stock-0.19 client reads it: `invite_state` where MSC4186 writes `stripped_state`,
      and the `notification_count`/`highlight_count` the MSC deleted. Not built, because that puts a superseded field in
      every answer of the endpoint rather than tolerating one request. The fix belongs in ruma. Revisit if ruma does not
      move and a real user needs it — then: unstable path only, behind a config name, the dialect a connection arrived
      in logged once. Narrowed 2026-11-23: smaltino no longer needs it (own SDK copy, reads `/versions`, takes
      `POST /v4/sync` on the stable flag); the remaining audience is Element X and anything else on unpatched
      matrix-rust-sdk 0.19.
- [x] Room version 12 understood in both directions — hashed room ids, `additional_creators`, creators-privileged rules, state resolution v2.1, stripped state as full events (step 21). Choosing v12 as the *default* is a decision, not a piece of work.

### F. Operations and quality

- [ ] MSC4508 remembers scope, Lattice does not. A room that (re-)enters a list's range owes the client its **current**
      typing state, and a room that drops out owes nothing further - which needs the connection to remember what it has
      put in scope. Lattice answers changes since `pos` for whatever is in the window now, so a typing state that was
      already true when a room slid into range can go unsent until the next change. Same hole for re-enabled extensions.
      The scope itself is right since 2026-11-24; this is the bookkeeping on top of it.
- [ ] MSC4551's "Which receipts" rules. A room entering scope owes the client its *initial* receipts — the receipts on
      the events this response's timeline carries, plus the requesting user's own receipts of every type and thread,
      always — and the timeline receipts again whenever the room's entry says `expanded_timeline`. Lattice answers only
      what changed since the request's `pos`, so a client learns other members' markers but never its own read position
      on events outside the window: unread counters and cross-device read markers are the parts that quietly stay wrong.
      The same MSC also wants `M_UNKNOWN_POS` rather than a silent partial subset when a room has re-entered scope.
- [ ] MSC4548's two remaining rules, then its flag. Enabling the extension for the first time on a connection must
      return **all** global account data (its changelog point 2 counts Synapse's "changed since `pos`" as the deviation),
      and a room that enters scope for the first time owes all of *its* account data rather than the changes since
      `pos`. Lattice does neither, so `org.matrix.msc4548.stable` is deliberately not advertised while the other four
      extension flags are. The shape itself is right: `{type, content}` per entry, one entry per type, and
      `m.push_rules` in the list since 2026-11-24.
- [ ] Transfer push rules across a room upgrade (MSC2313). Upgrading a room must carry the `room`-kind rule of the old
      room id over to the new one; `TestPushRuleRoomUpgrade` now reads the `m.push_rules` account data and fails on
      exactly this, waiting for a rule naming the new room id. Measured 2026-11-24, log kept in
      `.agent-work/pushacct/run2.log`.
- [ ] Audit the answers for the nil-collection class once more, by construction rather than by report: `grep -n "omitempty"
      internal/matrix/*.go` next to the spec tables, plus every `map[string]any` an extension builds. Two of the three
      found so far (`state_key`, `device_unused_fallback_key_types`) came from clients, not from a reading of the code.
      A helper that fails a `Required` list/map field that arrived nil would catch the third in the existing tests.
- [ ] Message retention (`m.room.retention`), fuzz targets, a load harness.
- [ ] Room-version upgrade that carries the state across (the reference passes it, we do not).

### G. Video calls

- [ ] The LiveKit / MatrixRTC plan and its L-steps: [RTC-PLAN.md](RTC-PLAN.md).

### H. Questions the `retry` audit left open

Eight of its 53 open items were neither closed here nor queued, because they are questions rather
than tasks. Each is listed with the probe that settles it. Full reasoning:
`.agent-work/retired-retry-line-20261109/TASK-AUDIT-20261120.md` (scratch, not in git — this list is
the copy that survives).

- [x] **T-4014 — the re-marshal bug class.** Audited 2026-11-22. `retry`'s version cannot happen here:
      `matrix.Event.MarshalJSON` serves the stored bytes whenever `Stored` is set, and exactly one loader
      fills it. The named endpoints answer through that loader, so they are fine. What the audit found
      instead is the store path: `internal/service/fedapply.go:328` and `:546` write the event's `JSON`
      column with `matrix.Encode` — `json.Marshal`, HTML-escaping `&`, `<` and `>` — where the create path
      uses `matrix.CanonicalJSON`. Measured: canonical reproduces the bytes it is given, `Encode` does not.
- [x] **The fix for T-4014, and the test that makes it stick.** Landed 2026-11-22: `signedForm()` in
      `internal/service/fedapply.go` writes both call sites with `matrix.CanonicalJSON`, and
      `TestAnArrivingEventIsKeptInTheFormItWasSignedIn` (`tests/federation/intake_test.go`) delivers an event
      reading `Tom & Jerry <3> a<b` across the two-server cluster and compares what each server kept. Reverted
      to `matrix.Encode` it fails on both of its byte checks, so it is the test and not a certificate. Rows
      stored before this cannot be repaired — the original bytes were never kept.
- [ ] **T-4068 — does `/members` stream?** MSC4222 wants membership as a stream with `from`/`to`;
      `internal/api/routes.go:87` serves a roster. One request against a live server with a `from`/`to`
      pair settles whether it streams at all. Feeds the MSC4222 entry in queue B.
- [ ] **T-4105 — can a room be upgraded *into* version 12?** `POST /upgrade` with
      `room_version=12` on a room carrying `additional_creators`, then check the new room's auth
      events. We claim v12 is understood both directions; this path has never been exercised. Neighbour
      of the upgraded-room decision above.
- [ ] **T-2005a — is our state resolution compared to another implementation's answers?**
      `tests/unit/stateres_test.go` holds our own cases only. A shared-fixture run against
      `gomatrixserverlib`'s resolver is the evidence RTC-356 wants. The heaviest of the eight.
- [ ] **T-4090 — is left-room `state` gated per room or per event?** `stateVisibleToOutsider` decides
      per room. Overlaps `TestLeftRoomFixture`, called "one check away", and history visibility.
- [ ] **T-4102 — can the single-event accept path authorise against a room's create event when v12
      forbids citing it?** A v12 room whose creator's first event arrives over `/send` answers it.
- [ ] **T-4026 — why did a peer refuse to re-verify our `invite_room_state`?** Needs a second real
      homeserver rather than the Complement mock, and the peer's refusal reason.
- [ ] **T-4040, T-4096b — what do the read paths and the user directory cost?** The performance queue
      already opens with "measure before optimising"; these are two named benchmarks for that baseline.

## How this got here — the 26 steps, one line each

Full record, including what each left open: [STEPS-LOG.md](STEPS-LOG.md).

| Step | What it did |
|---|---|
| 1-5 (2026-11-01) | housekeeping; rate limiting; the client-API correctness tails; batched reads (the N+1 queries); the offline unit suite |
| 6 (11-02) | Federation I — identity, keys, the signed transport |
| 7 (11-02) | Federation II — the event DAG: hashes, signatures, derived ids, `prev_events`/`auth_events`, exact signed JSON |
| 8 (11-02) | Federation III — transactions and EDUs: replay-safe log, EDU sinks, per-destination outbound queue, `m.room.server_acl` both ways |
| 9 (11-02) | Federation IV — judging the event that arrived: shape, signature, hash, then the room's rules; soft-fail |
| 10 (11-02) | Federation V — the first honest cross-server join (`make_join`/`send_join`, `make_leave`/`send_leave`) |
| 11 (11-03) | Federation VI — the read side: auth chains, gap filling with `get_missing_events` and `min_depth` |
| 12-13 (11-03) | Federation VII-VIII — names, profiles, the directory, spaces/`/hierarchy`; cross-server E2EE, device lists, to-device, media |
| 14 (11-03) | Federation IX — a two-server suite that does not cheat; the release wrap |
| 15 (11-03) | Federation X — the queue's memory: to-device across a peer outage |
| 16 (11-03) | Federation XI — media: which door, and what the file is called |
| 17 (11-04) | Federation XII — knocking and restricted rooms |
| 18 (11-04) | Federation XIII — the space summary (`allowed_room_ids` over federation) |
| 19 (11-04) | A knock is a question, not a door |
| 20 (11-04) | Authorization: one rulebook for what we sign and what a peer sends |
| 21 (11-04) | Room version 12 — ids as hashes, privileged creators, state resolution v2.1 |
| 22 (11-04) | Simplified sliding sync (MSC4186) |
| 23 (11-04) | MSC2675 — which events of a room a member may see |
| 24 (11-04) | Reading a room you have left; the filters a client asked for; holes closed by fetching named parents by id |
| 25 (11-04) | Lazy loading that is lazy; the people a user asked not to see |
| 26 (11-04) | Reading a room as of a point when its history forked |

## House rules

They live with the other operational knowledge: [FINDINGS.md](FINDINGS.md) (house rules, commands,
the harness gotchas, and what is deferred on purpose). The rules that outlived being written down
once: one endpoint per step with its functional test, tests never beside the code they test, every
deliberate absence answered with a reasoned `M_UNKNOWN`, and a finding recorded where a future
session would otherwise re-derive it.
