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
| this repository's own tests | **391** counted here — 219 client-API compatibility, 90 two-server federation, 82 offline unit (`go test ./tests/<pkg> -list '.*'`, all green on 2026-11-27: compat 110 s, federation 49 s). `internal/matrix` adds its own test functions, which this tally has never counted |
| Complement `tests/csapi` | **84 green of 106**, 2 skipped, in 345 s on v0.2.18 (2026-11-25; 338 s on v0.2.17, 340 s on v0.2.16). Two names less red than the 2026-11-20 tally: `TestPushSync` passes since the push rule set travels as `m.push_rules` account data, and `TestSearch` passes since `/search` speaks the specification's body and answer. The other 20 names are the same list, so the red list is stable rather than lucky. It could not be run to completion before 2026-11-18, see [COMPLEMENT.md](COMPLEMENT.md) |
| Complement server-server (`tests`, not `tests/federation`) | **78 green of 91**, 13 red — re-measured 2026-11-20 for the first time since 2026-11-04, and the 13 are name for name the names recorded then, so the number was stale but honest. 338 s of own time; run beside the client-API suite it costs nothing extra, see `make complement-both` |
| the author of smaltino's core lab tests | **72 of 89** on v0.2.27 (measured 2026-11-27 in that lab: own build of the tag, fresh database. One of the 17 that fail is a verification test that fails only inside the full round and passes 3 of 3 on its own — seen three rounds running, on v0.2.25, v0.2.26 and v0.2.27, and counted by its author as load rather than as a bug, which makes **73 of 89** the honest number. The run before it: 61 of 82 on v0.2.18, 62 on v0.2.19, 52 on v0.2.20 — the release that made a rule set readable and every notification silent, 64 on v0.2.21, 70 of 88 on v0.2.22 after three CarPlay read-aloud tests were added, 70 on v0.2.23, 71 on v0.2.24 for `kind: null` deleting, 72 of 89 on v0.2.25 for the sync loop, where the lab added an idle control of its own: a minute after a restart must be quiet, and it was three times out of three against 2.845 room-list requests on v0.2.23, and the browser page after a reload was quiet against 4.383 — with the extension connection making two requests, the floor a 30 s timeout sets; 71 on v0.2.26 for the invite knock, which is that 72 minus `pausing_in_the_middle_of_syncing_loses_nothing`, and 72 of 89 — 73 counting that flake — on v0.2.27 for the delivery fix, where the pause test passed three times on one database, each round in 22 s, and the idle control stayed green, so the sleep held). It is a client suite for web, Android and iOS written against a homeserver contract, so a feature counts only when an app can use it — which is how a pusher that was stored and never contacted hid from every measure above. Nothing in this repository produces this number, and its 17 failures name other subjects (room settings, spaces, threads, upgrades, devices, verification) — the four of this week are closed: delivery to a gateway, the invite knock, the sync loop that never slept, and delivery after a cancelled long poll |
| endpoints | **180 of 234** indexed routes implemented, 44 routes the index does not name (`make endpoints`, unchanged on 2026-11-20) |
| spec | target v1.19 (the endpoint index it is built from), room versions 1-12 understood, new rooms default to v11; `/versions` advertises v1.0-v1.11 — see queue A |
| release | **v0.2.27** tagged 2026-11-27, the tag naming `79f76f6` — `ghcr.io/enimatek-nl/lattice` publishes `0.2.27`, `0.2` and `latest` (the `image` workflow built both architectures in 2 minutes, run `45995864172`); the live server on genius runs it (image revision `79f76f6`, quadlet restarted: `NRestarts=0`, no error-severity lines in the first minutes) and `/_matrix/client/versions` answers `server_version: "Lattice/0.2.27"` with `v1.11` as its top version. What it carries over v0.2.26, in the sync subject @cybertim gave the go for: **a sliding sync answer counts as delivered when the client says so, not when this server writes it**. A long poll is answered the moment an event arrives; a phone that backgrounds itself closes the socket a breath later unread and asks again from the position it still holds — and that request was answered from the position the connection had advanced to itself, so the event fell between the two lines and was never sent on that connection again, with nothing in the answer to notice by. Measured with the lab's steps (a room past the window, the socket closed unread after 0 to 500 ms, six attempts): **4 of 6 attempts lost the event before, 0 after**, the room returning `initial: true` with `limited: true`. The rows still record what was last answered; `clampSlidingKnown` folds each room's `TimelinePos` and `TimelineCount` back to the highest position the request carried, which is the client's own statement of what it holds and already stored as `ack_pos`, and a room that had to be folded is sent as new to the connection. Two things this corrected rather than confirmed: `limited` was never missing from this server — those answers carried no room at all — and the release the lab blamed had only moved the timing, the same 4 of 6 on both binaries, which is why the old binary is measured before the new one is believed. The v0.2.25 restart probe and the quiet minute (two requests per connection per minute) still pass, so none of the sleep was traded away; new compat test `TestSlidingSyncRedeliversAnAnswerTheClientNeverAcknowledged`, verified to fail the previous code at all three assertions. The Complement tally is **not** re-measured on this release: podman cannot make a namespace in this jail, so 84 green of 106 is still the v0.2.18 number. What v0.2.26 was: **v0.2.26** tagged 2026-11-27, the tag naming `e224441` — `ghcr.io/enimatek-nl/lattice` publishes `0.2.26`, `0.2` and `latest` (the `image` workflow built both architectures in 2 minutes, run `45981059653`); the live server on genius runs it (image revision `e224441`, quadlet restarted: `NRestarts=0`, no error-severity lines in the first minutes) and `/_matrix/client/versions` answers `server_version: "Lattice/0.2.26"` with `v1.11` as its top version. What it carries over v0.2.25, in the push subject @cybertim gave the go for: **an invitation reaches the push gateway of the person it invites**. Rules were never read for a membership event, and the reason was the recipient list — "each of the local users in the room (excluding the sender)" is a list the invited person is not on, while `.m.rule.invite_for_me` is about exactly that person. A membership event now adds its own `state_key` to the names that are asked; every other membership change walks the same road and stays quiet under `.m.rule.member_event`. Two numbers moved because they were wrong before: the unread count on a room's first message reads 2 rather than 1 (an invitation is an unacknowledged notification), and the notifications endpoint lists the invitation with a notify and a sound and no highlight, which is what v1.11 gives the rule. Four delivery tests picked the payload they assert by arrival order and had silently started reading the invitation instead of the message; they pick by event id now. Replay of the lab's steps against a real binary prints the body it sends (`.agent-work/v111/push-e2e.py`, scenario three): `type`, `user_is_target: true`, `content.membership: invite`, `room_name`, `sender_display_name`, the `sound` tweak — and no knock for a join about somebody else, with the message after it still knocking. Recorded rather than fixed: on an invite that arrives over federation the room name is only as good as the state this server holds, and the disclosure that a room name now travels to a third-party gateway for a room a person has only been invited to is written down in FINDINGS. The Complement tally is **not** re-measured on this release: podman cannot make a namespace in this jail, so 84 green of 106 is still the v0.2.18 number, and the lab's own core tally for v0.2.26 was not in yet. What v0.2.25 was: **v0.2.25** tagged 2026-11-27, the tag naming `f80a797` — `ghcr.io/enimatek-nl/lattice` publishes `0.2.25`, `0.2` and `latest` (the `image` workflow built both architectures in 3 minutes, run `45966894606`); the live server on genius runs it (image revision `f80a797`, quadlet restarted: `NRestarts=0`, no error-severity lines in the first five minutes) and `/_matrix/client/versions` answers `server_version: "Lattice/0.2.25"` with `v1.11` as its top version. What it carries over v0.2.24, in the sync subject @cybertim chose: a sliding sync connection sleeps again. Both sliding tables embed `gorm.Model`, so their deletes were soft, and `ResetRooms` left every room row occupying its unique index while every read filtered it out — the next write updated the invisible row and the connection never learned that it had shown a room, so every request after a restart re-sent every room as `initial` and answered in 0.01 s instead of waiting: fifteen requests a second per device. Measured on the lab's own steps before and after: `0.01 0.02 0.01 0.01 0.02 0.02 0.01 0.01` seconds against `1.50 1.50 1.50 1.50 1.50 1.51 1.51 1.50`. And MSC4186's cancel-and-resend is honoured: a connection now accepts the position it is at *or* the highest position the client asked about, so cancelling an in-flight request to open a room no longer answers `M_UNKNOWN_POS` and forces a restart into that loop; a position one step further back is still refused. A sweep of the 37 models that combine soft delete with a unique index found no other delete like these three. What v0.2.24 was: **v0.2.24** tagged 2026-11-27, the tag naming `1aa84ce` — `ghcr.io/enimatek-nl/lattice` publishes `0.2.24`, `0.2` and `latest` (the `image` workflow built both architectures in 3 minutes, run `45962960421`); the live server on genius runs it (image revision `1aa84ce`, quadlet restarted: `NRestarts=0`, no error-severity lines in the first five minutes) and `/_matrix/client/versions` answers `server_version: "Lattice/0.2.24"` with `v1.11` as its top version. What it carries over v0.2.23: the two corrections the lab's first run against v0.2.23 found in the week's own code. `kind: null` deletes a pusher — it had been read as a string, so the Rust SDK's delete fell through to the register path and replaced a working pusher with an empty http row, answering 200 with no URL left to deliver to; and a body that names `data` while leaving `kind` out is now refused instead of guessed at. The gateway request also carries what its field table lists and its example omitted: `room_name`, `sender_display_name`, `user_is_target`, no zero counts, and device `data` minus the `url` key. Still open in this subject, with the choice outstanding rather than the work: no membership event is judged against the rules, so an invite reaches no gateway. What v0.2.23 was: **v0.2.23** tagged 2026-11-27, the tag naming `f176470` — `ghcr.io/enimatek-nl/lattice` publishes `0.2.23`, `0.2` and `latest` (the `image` workflow built both architectures in 3 minutes, run `45958367423`); the live server on genius runs it (image revision `f176470`, quadlet restarted: `NRestarts=0`, no error-severity lines in the first five minutes) and `/_matrix/client/versions` answers `server_version: "Lattice/0.2.23"` with `v1.11` as its top version. What it carries over v0.2.22: **push delivery** — this server now makes the `notify` request to the gateway a pusher names instead of storing the URL and never using it. Rules, pushers, notification rows and unread counts already existed; the request that leaves the process did not, so an app that is not open received nothing from this server on any platform — the lab's gateway counted 0 requests in 30 s. One request per recipient per gateway and format, delivered from a bounded queue by a worker so a dead gateway cannot hold up an accepted message: `event_id_only` stops at `event_id`, `room_id`, `counts`, `unread`, `prio` and `devices` (no `content`, no `sender`), a full pusher also gets `sender`, `type` and `content` with `default_payload` underneath, the pusher's own `data` travels whole, and devices that share a gateway and a format ride together. A gateway's answer is acted on: a `rejected` list or a 400 forgets the pusher, a 404 is a wrong URL and keeps it, 429/408/5xx and unreachable retry three times with doubling backoff and jitter, and giving up says so in the log. Seven compat tests, and the lab's repro replays end to end against a real binary (`.agent-work/v111/push-e2e.py`: the body and eleven checks). The Complement tally above is **not** re-measured on the last seven releases: docker was unreachable from the sessions that built them, so 84 green of 106 is still the v0.2.18 number. What v0.2.22 was: **v0.2.22** tagged 2026-11-26, the tag naming `9f6c4dd` — `ghcr.io/enimatek-nl/lattice` publishes `0.2.22`, `0.2` and `latest` (the `image` workflow built both architectures in 3 minutes, run `45852036462`); the live server on genius runs it (`:latest` pulled, image revision `9f6c4dd`, quadlet restarted: `NRestarts=0`) and `/_matrix/client/versions` answers `server_version: "Lattice/0.2.22"` with `v1.11` as its top version. What it carries over v0.2.21: the seven deviations the whole-set audit found — `.m.rule.room_one_to_one` and `.m.rule.message` naming the event type instead of a msgtype glob, `notify` without sound or count on those and `.m.rule.encrypted`, `.m.rule.roomnotif` with its `sender_notification_permission` condition and notify-plus-highlight actions, `.m.rule.tombstone` highlighting, `.m.rule.suppress_notices` and `.m.rule.master` printing the empty lists the specification gives them — plus `TestPublishedDefaultRulesAreTheSpecificationsDefinitions`, which compares all eighteen definitions at once. Read off the live server after the restart: rule set version 5, zero enabled override rules published without conditions, and the seven audited rules holding exactly what v1.11 defines (`.agent-work/v111/live-check.sh`). The Complement tally above is **not** re-measured on the last four releases: docker was unreachable from the sessions that built them, so 84 green of 106 is still the v0.2.18 number. What v0.2.21 carried over v0.2.20: the three override rules published without conditions that silenced every notification on a Rust client. What v0.2.20 carried over v0.2.19: the content rule's `pattern` (ruma refused the whole rule set without it), `pattern` off the rules that are not content rules, the sound tweak on the mention rules, the `contains_display_name` condition, a `PUT` of a rule no longer wiping the fields its body does not name, and a repair pass that reaches accounts written before the rules. What v0.2.19 carried over v0.2.18: the four default override rules of v1.7 and later that were never seeded, the three condition kinds they need, and a start-time repair that gives those rules to accounts written before them |
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
- [ ] **A timeline is ordered by when it arrived, not by where it sits in the graph.** Measured
      2026-11-28 against the live server and the deployed tag v0.2.27, in `#iaa`
      (`!hezPjQIMauLaKRxQvc:cluephone.org`), where `@tim:lattice.cybertim.net` had just rejoined.
      `EventRepoImpl.History` — which answers `/messages`, and `/relations` and `/threads` through
      it — filters, orders and paginates by `stream_pos`, the local arrival counter, and
      `EventRepoImpl.TimelineFor` picks a sync room's window from the highest `stream_pos` and
      returns it in arrival order. Arrival order is graph order only while events happen to arrive
      in graph order. In a federated room they do not, and after a join they certainly do not: a
      peer hands its batch over walking backwards from the forward extremity, so the batch arrives
      newest first. In that room 59 of the 73 events in the timeline have a lower `depth` than the
      event that arrived before them. Two visible failures follow: `?dir=b`, which must start at
      the newest and walk back, returned its first chunk starting at depth 381100
      (`$jmPUWWkwR74RfzdLFiPRHCIe9om0ONRFEQT7898iRZw`, 11-27 20:07:20) and walking *up* through
      `$8vMIxVv4jFbpWDiqKr0PSFwsl7348dhOP3rSP1moHys` and `$JvKbkaMd2V8ti_yuZqYYrQ3DSIfTztDZJjJNNG5K1c4`
      — oldest first; and the room's newest event, `$xy33eSvsN9jeJLbtAC99-3cC6c5TuERPpCmvH3wt4NI`
      (depth 381160, 11-28 14:46:22), was in no sync window at all, because the window was the
      twenty highest `stream_pos` values, depths 381100 to 381119, an afternoon that had already
      passed. A client that renders what sync gave and then paginates shows a room that looks empty
      or backwards while the newest message sits outside every window it can be handed. Shape of
      the fix: `/messages` and the first window of a room order by the graph (`depth`, then
      `origin_server_ts`, then `event_id`) the way Synapse orders by `topological_ordering`; an
      incremental sync may keep choosing *what* to send by stream position, but sends it in graph
      order. Pagination tokens carry the ordering key, so they need a second token kind, and
      sliding sync's `prev_batch` and stored `TimelinePos` read the same rows. The
      `timestamp_to_event` line below is the same mistake one endpoint further.
- [ ] **A departed member comes back when state arrives out of order, and then their server keeps
      receiving the room.** `room_memberships` is written in arrival order, so of two member events
      for one user the one that arrives last wins, not the one that is newer in the graph. Measured
      in that same room: `@xiro:rixo.nl` (leave at 03-02, join at 03-01) is held as `join`;
      `@sander:tessera.oele.net` (leave 11-06, and a leave and a join both stamped 10-29 13:57) is
      held as `join`; `@leto:bedrageri.dk` (leave 02-23, join 02-21) is held as `join`; so is
      `@enimatek:home.enimatek.nl` (leave 07-31, join 07-28). Those rows feed
      `destinationsOfRoom`, which is right to ask the table who is in the room — so the server sends
      that room's events and EDUs to four homeservers that have no member in it, which is noise in
      the outbox (hours of retries to `rixo.nl`, whose name has no address at all, visible in the
      log as `(0 PDU(s), 3 EDU(s))`) and a disclosure: a room's traffic to a server nobody in that
      room uses. The rule the specification gives is the narrow one — a server belongs to a room
      while it has at least one member with `join`. Shape of the fix: state and membership
      projections compare events by the graph instead of taking the last write, which is the same
      comparison the item above needs.
- [ ] **Nothing says a room arrived.** An accepted inbound `/send` transaction is applied without a
      log line, and Caddy in front of this deployment writes no access log at all, so an operator
      watching a room that shows nothing can prove neither that a peer delivered it nor that nobody
      did. That cost three rounds of this investigation: the room's operator could see only their
      own failures, this server could see only its own, and the messages that had in fact arrived
      turned the diagnosis around twice. At least one line per accepted transaction, naming the room
      and how many PDUs it carried, at `info` with the sender at `debug`.
- [ ] `Lattice never asks another server for events by ID.` `GET /_matrix/federation/v1/event/{eventId}`
      is not registered: `PUT` on the path reaches a stub that only answers `m.room.redaction`, and
      `GET` falls through to a message that offers `PUT`. Every peer that wants to fill a gap hits
      it. Complement never notices because Synapse answers a `GET` on the path with `400 M_NOT_FOUND`
      and the suite reads exactly that. It is also the read `backfill` would be built from, so the
      line above about a late joiner's history waits on it. (measured 2026-11-28 on
      `lattice.cybertim.net`, from a second Lattice pointed at it)
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
- [x] **Push delivery** — `POST /_matrix/push/v1/notify` as a *client* of a push gateway, with `event_id_only` and rejected pushkeys (built 2026-11-27 for v0.2.23; reported the same day by the lab as `a_pusher_has_the_homeserver_knock_at_the_gateway`, "the homeserver never knocked at the gateway for the message", and `a_browser_s_pusher_carries_its_subscription_and_the_names`).
      It had been true since the first pusher: the endpoint accepted the registration,
      `GET /pushers` listed it back with `active: true` and the right `data.url`, the rules decided
      that a message in a room of two was worth a notification, a notification row was written for
      `/notifications` — and no request was ever made to the URL the client supplied. Reported from
      the lab's own gateway, which counted 0 requests in 30 s. What the specification says is
      "the homeserver starts sending HTTP requests to the Push Gateway using the supplied URL", and
      "this may result in making a request to the configured push gateway".
      `internal/service/pushdeliver.go` is that loop: the request is rendered while the event is
      still being sent and put on a queue of 256, and a worker makes the call — so a gateway that
      is slow or dead cannot hold up a message that is already accepted, which is the same reason
      the federation outbox exists. One request per recipient per gateway and format, because
      `counts`, `unread` and `prio` describe the recipient and not the device, and devices that
      share both ride along in `devices[]`. `event_id_only` stops at `event_id`, `room_id`,
      `counts`, `prio` and `devices` — no `content`, no `sender`, which is what the lab's gateway
      asserts; a full pusher gets `sender`, `type` and `content`, with the pusher's
      `default_payload` underneath it. `data` travels whole, so the `account` label a client put in
      its own pusher data comes back in the payload. `prio` is high when the matched rule sets a
      sound. A gateway's answer is acted on: a `rejected` list or a 400 makes the pushers go, a 404
      is a wrong URL and says nothing about the pushkey, 429/408/5xx and unreachable are retried
      three times with doubling backoff and jitter, and giving up says so in the log — silence is
      what made "we never called it" indistinguishable from "nobody wanted us to".
      Seven tests in `tests/compat/pushdelivery_test.go`: the knock itself with every field the
      lab's gateway checks, the sender excluded from their own message, the full format carrying
      the event, both gateway answers that mean forget this device, the retry after a 503, rules
      still deciding (a switched-off recipient gets nothing), and an email pusher never becoming a
      gateway request. End to end on the lab's exact steps, including the listener on 127.0.0.2:
      `.agent-work/v111/push-e2e.py`.
      What it does not do yet: a membership event is still not judged, so an invite reaches no
      gateway — see the item below, which is now unblocked rather than blocked on a gateway to test
      against. And the URL is user input this server dials without asking anybody: see the outbound
      address policy above.
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
      Two places dial today and neither asks anybody first: the push gateway URL of a pusher
      (`internal/service/pushdeliver.go`, 2026-11-27) is user input that is posted to as soon as a
      message arrives, so an account can aim this server at a link-local metadata endpoint or a
      port on its own network; and
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
- [x] A sliding sync connection that never slept again after a restart, and the position token
      that could not be shown twice (measured 2026-11-27 by the lab on v0.2.22 through v0.2.24;
      fixed for v0.2.25, in the subject @cybertim chose).
      Two reports, one machine behind both. **The restart that forgot it had spoken:** after a
      client started a connection over, every following request answered in 0.01 s with every room
      marked `initial` again and the same `pos`, forever — fifteen requests a second per device and
      no long poll at all. The cause was not in the sliding sync code: both sliding tables embed
      `gorm.Model`, so a delete is a *soft* delete, and `ResetRooms` left every room row occupying
      its unique `(conn_key, room_id)` index while every read filtered it out. The next write hit
      its own `ON CONFLICT` clause and quietly updated a row nobody would ever read again, so the
      connection never learned that it had shown a room — and a connection that always has
      something to say never waits. Same for `TrimRooms` and `Forget`. Fixed with `Unscoped()` on
      all three, the way `PushRuleRepo.Delete` has done since the same trap was found in push rules.
      **And the position that could not be shown twice:** MSC4186 says a client may cancel an
      in-flight request and send another with the same `pos`, and that a server MUST keep its
      per-connection state correct when several requests carry the latest token. This server
      required the token to equal the position it had *last answered with*, so a client that
      cancelled a request it had been answered for — which is what opening a room does — was told
      `M_UNKNOWN_POS` and had to restart, which put it straight back into the bug above. The lab
      counted eight of them and every one followed that pattern. A connection now remembers both
      the position it is at and the highest position the client has asked about (`ack_pos`) and
      honours either: one step back is a client retrying, one step further is a client that lost
      the thread and is still answered with `M_UNKNOWN_POS`.
      Measured rather than believed, before and after, against a real binary on the lab's own steps:
      eight continuations after a restart answered in 0.01 s each and re-sent every room; after the
      fix the same eight each waited the full timeout and sent nothing, and a message that arrived
      while one of them waited was answered promptly, with a position that moved and the room not
      marked initial again (`.agent-work/v111/sliding-e2e.py`). Measured the way the lab measured
      it too: one quiet minute with a list connection and an extension-only connection, both
      long-polling at 30 s, gives **2 requests per minute per connection** — the floor the timeout
      sets, 60/30 — with no early answer and no room sent twice, against their 2845 per minute for
      the room list and 4383 in the browser on v0.2.23 (`.agent-work/v111/sliding-quiet-minute.py`).
      The extension loop sitting at the same floor matters: `to_device`, `e2ee`, typing and receipts
      do not answer of their own.
      Three compat tests, one of which is
      an older test of this repository's own that asserted the refused-retry behaviour MSC4186
      forbids — amended, with the sentence it was amended for written above it, so that it is not
      "fixed" back.
      The sweep this prompted: 37 models combine soft delete with a unique index. Beyond the two
      sliding tables, no repository deletes one of them without `Unscoped()` — read site by site,
      the single other hit being a Go method named `Delete`, not a GORM one.
- [x] A sliding sync answer the client never read no longer counts as delivered (measured
      2026-11-27 by the lab on v0.2.25 and v0.2.26 in `pausing_in_the_middle_of_syncing_loses_nothing`,
      and replayed here against both binaries: **4 of 6 attempts lost the message**, identical on
      both releases — v0.2.26 only moved the timing of the lab's pause. Fixed for v0.2.27, on the
      go @cybertim gave for this subject.
      `.agent-work/v111/sliding-abort-probe.py` is the repro: a room filled past the subscription's
      `timeline_limit` of 20, a long-poll on position P, one message, the socket closed unread
      after 0 to 500 ms, then the same request again with P. At 0 and 100 ms the long-poll was not
      answered yet and the retry delivers; at 200 ms and beyond the retry answers `{"pos":"P+1"}`
      with no room, no timeline and no `limited`, and the event never returns — while a fresh
      `conn_id` on the same device and room lists it immediately, and `/messages` from the
      `prev_batch` the lab tried also has it. So the room is fine and this connection's window
      simply stepped over it, with no flag saying so.
      The mechanism is two lines apart, both in `internal/service/sliding.go`. `fromPos` for a
      request is `conn.row.LastPos` rather than the position the request carries, and
      `commitSliding` advances `LastPos` and each room's `TimelinePos` while the response is being
      built — before anything has been written, let alone read. The lab saw exactly that in their
      proxy log: the server had sent nothing back yet at the moment the client closed. A position
      the client has never been given is then behind the connection's own line, and MSC4186 says
      it must not be: "The server cannot assume that a client has received a response until it
      receives a new request with the `pos` token set to the `pos` it returned in the response."
      What was built: the rows still record what the server last answered, and the connection
      forgets the part of that the client has not vouched for. `clampSlidingKnown` folds every
      room's `TimelinePos` and `TimelineCount` back to the highest position the request carried —
      the client's own statement of what it holds, already kept as `ack_pos` for the refusal check
      — and names the rooms it folded, which the room builder then sends as `initial` again: a
      room whose only delivery was an unacknowledged answer has not been sent to this connection.
      `BumpStamp` is left alone, because it is a timestamp and folding it would invent activity.
      The stored rows are not rewritten, so a connection that goes on to acknowledge simply finds
      its markers already right. Two consequences, both intended and both in the new test: a client
      that re-sends the latest position for its own reasons gets that window a second time, which
      MSC4186 allows and clients deduplicate by event id; and `initial` now follows
      "acknowledged on this connection" rather than "ever sent".
      Measured after: the same probe, six attempts with the socket closed at 0 to 500 ms — **0
      lost**, the room returning `initial: true` with `limited: true`. The v0.2.25 probe still
      passes (`restart` and the cancel-and-resend checks), and a quiet minute is still two requests
      per connection per minute, so nothing about the sleep was traded away. New compat test
      `TestSlidingSyncRedeliversAnAnswerTheClientNeverAcknowledged`, which reads and discards an
      answer rather than aborting one — MSC4186 lets a client "MUST NOT process any response it
      receives for it", so the endpoint is not owed a broken socket to prove the point — and
      verified to fail on the previous code at all three of its assertions.
      One claim of the report above was wrong, and the correction matters for the next reader:
      **`limited` is not missing from this server.** Measured again, a room sent with a window
      cut short of what the room holds does carry `limited: true`. What the lab's log showed was an
      answer with no room in it at all — nothing to be limited about. Now that the room is sent
      again, the flag arrives with it, and the assertion is in the new test.
      Why it matters outside a test: any client that backgrounds an app closes its long-poll
      mid-flight, which is the normal way a phone stops syncing. A message that arrives in the
      moment the server is answering goes missing from that device's timeline for good, and the
      client has no flag to notice it by.
- [ ] Soft delete is a standing trap for those 37 models, and it leaks: every soft-deleted row is
      kept forever, invisible to every read and still occupying its unique index. Worth one change
      of shape rather than one more fix — either the models behind a unique index stop embedding
      `gorm.Model`, or maintenance gains a sweep that really deletes what `deleted_at` marks.
      Found while fixing the sliding sync restart, 2026-11-27.
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
- [x] A delete that did not delete, and a gateway body written from an example instead of from
      the table (measured 2026-11-27 by the lab on v0.2.23; fixed for v0.2.24).
      `POST /_matrix/client/v3/pushers/set` with `{"app_id":…, "pushkey":…, "kind":null}` — which
      is what the Rust SDK sends for `PusherAction::Delete` — answered 200 and left an **empty
      http pusher** where a working one was: `active: true`, no `data.url`, no display names.
      `kind` was read as a string, a JSON null is not a string, and the request therefore landed
      in the register path, which overwrites the row. The specification names that field as the
      verb of the request: "`null` deletes the pusher". Now a null or empty `kind` deletes, the
      two-key short form keeps deleting because that is what it has always answered here, and a
      body that names more than the scope while leaving `kind` out is refused with
      `M_INVALID_PARAM` instead of guessed at — a wrong guess in either direction costs a person
      their notification endpoint. Tests: `TestPusherKindNullDeletes`.
      The other half was the request this server sends. The specification expects "all of the
      event-related fields in the `/notify` request", and mine had been written from that
      section's example rather than its field table: no `room_name`, no `sender_display_name` (so
      every banner on a locked screen said "Message", because a backgrounded app has no room to
      read a name out of), no `user_is_target`, `counts` printing a zero the table says to omit,
      and `devices[].data` carrying the pusher's `url`, which that table excludes in so many words
      — "minus the `url` key". Tests: `TestPushGatewayNamesTheRoomAndTheSender`,
      `TestPushGatewayCountsOmitZero`.
      One divergence deliberately kept and recorded: `tweaks` is a dictionary in the type table and
      the example, and a list of `set_tweak` objects in everything Synapse sends and Sygnal reads.
      The list stays, because a gateway that is not the example is the one that has to parse it.
- [x] An invite now reaches the push gateway of the person it invites (measured 2026-11-26;
      re-measured 2026-11-27 against v0.2.23, where the lab named the assertions it waits for;
      the go came from @cybertim on 2026-11-27; fixed for v0.2.26). `notifyParticipants` had
      returned for every event that was not `m.room.message`, `m.room.encrypted` or `m.sticker`,
      so a membership change was never judged against the rules of anybody — which left the
      `.m.rule.invite_for_me` that v0.2.22 finally gave its conditions unread by this server.
      What the fix had to answer first was not the payload but the list: "homeservers process
      push rules for each of the local users in the room (excluding the sender)", and the person
      an invitation is for is not in the room — the invitation is what puts them in it. So a
      membership event adds its own `state_key` to the names that are asked, and every other
      membership change travels the same road and stays quiet, because `.m.rule.member_event`
      matches it with an empty action list. Two consequences worth naming: an invitation is a
      notification row now, so `counts.unread` on a room's first message reads 2 rather than 1
      ("the number of unacknowledged notifications" counts the invitation, which the app has
      indeed not acknowledged); and `GET /notifications` lists it, without a highlight, because
      v1.11 gives `.m.rule.invite_for_me` a notify and a sound and no highlight tweak.
      The disclosure asked about before building: a push body carries `room_name`, so judging an
      invite means this server tells that person's gateway — and through it a push provider — the
      name of a room they are only invited to. Synapse does this, the lab's test asserts it, and
      the alternative is an invitation that arrives as a silent badge. Said here rather than
      found later. What arrives, from the lab's own steps against a real binary
      (`.agent-work/v111/push-e2e.py`, scenario three): `type: "m.room.member"`,
      `user_is_target: true`, `content.membership: "invite"`, `room_name: "Kiln"`,
      `sender_display_name`, and the `sound` tweak. Measured on the same run: a join about
      somebody else knocks at nobody, and the message that follows still knocks. Three compat
      tests added (`TestPushGatewayIsKnockedAtForAnInvite`,
      `TestPushGatewayIsQuietForAMembershipAboutSomebodyElse`, and the invitation now expected in
      `TestUserScript`), and the four delivery tests that read "the first knock that arrives"
      were changed to read the knock of the event they are about — an invitation now arrives
      before a room's first message, so "first" had quietly stopped meaning "the message".
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
- [x] The whole default rule set, compared with the definitions the specification prints
      (measured 2026-11-26, after the third client report in a row was about rule data rather than
      about behaviour; fixed the same day; v0.2.22). Three releases had been opened one rule at a
      time by a Rust client, so this time the published answer was compared with the specification
      as a whole: `.agent-work/v111/spec-audit.py` reads the JSON definitions out of the
      "Predefined Rules" section and diffs them, conditions and actions, against `GET /pushrules`.
      Eight deviations came back, six of them never reported:

      | rule | published here | v1.11 defines |
      |---|---|---|
      | `.m.rule.room_one_to_one` | member count 2 | member count 2 **and** `type` = `m.room.message` |
      | `.m.rule.message` | `content.msgtype` `m.*`, notify + sound `default` | `type` = `m.room.message`, `notify` |
      | `.m.rule.encrypted` | notify + `count` | `notify` |
      | `.m.rule.roomnotif` | one `@room` condition, `dont_notify` | that condition **and** `sender_notification_permission`, notify + highlight |
      | `.m.rule.tombstone` | `dont_notify` | notify + highlight |
      | `.m.rule.suppress_notices` | `dont_notify` | `[]` |
      | `.m.rule.master` | no `conditions` field | `"conditions": []` |

      The first row is what the room reported: an underride rule that speaks of a message and names
      no type matches a name change, a topic change and somebody else's call in a room of two —
      seven "Room updated" announcements in one test. Measured by walking the published answer with
      the specification's algorithm (`.agent-work/v111/rules-eval.py`), in a room of two from an
      ordinary member: a name change and a topic change went from `.m.rule.room_one_to_one` with
      notify and a sound to no rule at all, and an encrypted message from that same rule to
      `.m.rule.encrypted_room_one_to_one`, which is the rule written for it.

      Two of the seven change what this server itself does, and both are the specification's doing:
      a plain message in a group room now carries no sound tweak (`.m.rule.message` is `notify`, and
      the sound of a room is the person's choice), and an `@room` in a body from a moderator now
      highlights the way `m.mentions.room` does.

      `TestPublishedDefaultRulesAreTheSpecificationsDefinitions` is that audit inside the suite: a
      table of all eighteen definitions, emitted from the specification's own JSON by
      `.agent-work/v111/gen-rules-table.py`, compared field for field, with the two rules this
      server added of its own named as the only permitted extras. It fails on a single reverted
      tweak, which is the point: four reports and three releases went into rules that a comparison
      of the whole set catches in one go. The repair pass runs again at `defaultRulesVersion` 5,
      taking actions back only where a row still holds what this server seeded (`seededWrongActions`
      grew to the nine rules touched since), so nobody's settings screen is undone.

      Read beside it: `OrderRules` and `Matches` pick the **first** matching rule of a kind, where
      Synapse keeps the **last** for underride rules. The specification only says rules of a kind
      carry an ordering priority and the highest wins, and never says which end of the list that
      is. ruma agrees with this server, which is how the room's own evaluation came out the same
      way; a gateway written to Synapse's reading would pick `.m.rule.message` where ruma and here
      pick `.m.rule.room_one_to_one`. Recorded rather than changed.

- [x] A call for somebody else rang, and the reason was not the call rule (measured 2026-11-26;
      resolved the same day by v0.2.22; corrected the same day by the report). `woken_for_a_ring`
      was failing, its name says a call, and I concluded from the name that it sent an
      `m.call.invite` whose `party_id` named another user — which would have been unfixable in rule
      data, because v1.11's `.m.rule.call` matches `type` = `m.call.invite` and nothing on that event
      says whom the call is for. smaltino corrected it: the test sends
      `org.matrix.msc4075.rtc.notification`, and that event **does** carry an `m.mentions` property,
      so the mention rules read it fine. What made it fail was `.m.rule.room_one_to_one` without its
      `type` condition — the first row of the audit above — and the test passes on v0.2.22. Nothing
      to change on either side, and `.m.rule.call` stays exactly as the specification defines it.
      The lesson is the one about sampling: a test's name is not its request, and a conclusion drawn
      from the name is worth as much as the name.
- [ ] The rest of what v1.17 says the default rules are (split off from the v1.11 item above
      on 2026-11-26; not built). What is left is what a newer specification *changes* rather than adds,
      and the report agrees v1.11 may keep it: `.m.rule.contains_display_name`, `.m.rule.roomnotif`
      and `.m.rule.contains_user_name` are deprecated but still defined, while `.m.rule.default` and
      `.m.rule.notice` are seeded here and are in no ruleset at all. Read off the rules as this
      server publishes them: `.m.rule.notice` is still hard-coded in `Matches()`, so it publishes a
      condition it does not obey — the cleaner reason to delete it is that it is in no ruleset of the
      specification at all, which the audit of
      2026-11-26 now prints on every run of the compatibility suite; `.m.rule.master` is read as
      "never matches" where the specification has it match every event with an empty action list,
      which is exactly what makes switching it on the way to
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
