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


## Where the server stands (measured 2026-11-04)

| Measure | Now |
|---|---|
| `make build vet lint` | clean, 0 issues |
| this repository's own tests | **326** — 167 client-API compatibility, 89 two-server federation, 70 offline unit (`go test ./...`, green) |
| Complement `tests/csapi` | **95 green of 106** |
| Complement server-server (`tests`, not `tests/federation`) | **78 green of 91** (measured 2026-11-04 evening, after the state-handshake change) |
| endpoints | **180 of 234** indexed routes implemented, 43 routes the index does not name (`make endpoints`) |
| spec | v1.19, room versions 1-12 understood, new rooms default to v11 |
| federation | live behind `-federation` (default off); one rulebook judges what this server signs and what a peer sends (step 20) |

The client-API tally moved 66 → 91 → 95 green on 2026-11-04, in the steps that put MSC2675, the
sync filters, the `leave` section, lazy loading, the ignore list and the as-of state reads right.

## What is red, and what each one is waiting for

`tests/csapi` (11):

| Test | Waiting on |
|---|---|
| `TestPresence` | a decision, not a gap — presence is stored and answered per user, never streamed (see [FINDINGS.md](FINDINGS.md)) |
| `TestRegistration` | registration detail the suite checks beyond password/dummy/token |
| `TestRoomDeleteAlias` | removing a non-canonical alias is gated on the canonical-alias power level |
| `TestRoomForget` | forgetting: what is dropped, and what a rejoin must find again |
| `TestSearch` | a real search index — this server matches substrings |
| `TestDeviceListUpdates` | device-list change delivery at suite speed (passes on its own; see the flakiness note below) |
| `TestRoomImageRoundtrip` | media: URL previews / `POST /_matrix/media/v1/report` and the roundtrip the case asserts |
| `TestMembershipOnEvents` | membership carried on events |
| `TestPowerLevels` | power-level corner the case pushes on |
| `TestPushSync` | push rules as a client sees them in sync (delivery to a gateway is a queue item below) |
| `TestLeftRoomFixture` | **one check away** — the `/messages` chunk of a departed `joined`-visibility room (step 24) |

Server-server, the `tests` package (**13 red of 91**; this package also holds
`TestJumpToDateEndpoint`, which is why it never showed up in the 106):

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

The state-handshake change (step 27) is federation-only and did not move this tally: the package
measured 78 green with it in.

## The queue — what is wanted, grouped

Order within a group is the order I would take them in; the groups are in rough order of what they
unlock. `- [x]` marks what this plan has already closed, with the step that did it.

### A. Things left half-open by the last steps (cheapest real wins)

- [ ] The `/messages` chunk of a departed `joined` room — `TestLeftRoomFixture` is one check from green (step 24).
- [x] The federation state handshake in both directions: this server now serves the v3 query form (`GET /_matrix/federation/v3/state/{roomId}?event_id=`) as well as the v1/v2 path forms, and asks peers v2 → v1 → v3 before falling back to `state_ids` plus a fetch per event. Asking only the query form was answered with nothing by a server that has the state and not that route (step 11's machinery, closed 2026-11-04).
- [ ] `TestJumpToDateEndpoint`: `timestamp_to_event` should walk the graph, not stream positions (step 26).
- [x] `/messages?to=` is honoured: `from` is exclusive and `to` inclusive in both directions, so a window asked by its two ends returns that window and not the rest of the room (closed 2026-11-04; `TestRoomMessagesLazyLoading`, which passes `to=`, is green again).
- [ ] Token inclusivity around `from` is worth a pass: a backward `/messages` walk from a sync's `next_batch` does not return the event written *at* that token — the same class of off-by-one the last `TestLeftRoomFixture` check sits next to.
- [ ] MSC2675 is not applied to `/threads`, `/members?at=` or `/state/{eventId}` (step 23).
- [ ] `m.ignored_user_list` filters sync timelines and invites only; `/messages`, `/search` and `/relations` still carry an ignored user's words (step 25).
- [ ] Lazy loading is recomputed per response instead of remembered per device (step 25).
- [ ] As-of state reads cost one lookup per key; the complete answer is resolution over the event's ancestry, or per-event snapshots (step 26).
- [ ] `TestSync` has timed out inside a full parallel run twice while passing on its own — worth a look at the long-poll wake-up.

### B. Structural gaps — what other software needs in order to attach

- [ ] **Application services** — registrations and namespaces, `as_token` auth with `user_id`/`device_id`, transaction delivery with ephemeral data, user/alias queries. Nothing bridging works without it, and it is how a bot holds an identity here.
- [ ] **Push delivery** — `POST /_matrix/push/v1/notify` as a *client* of a push gateway, with `event_id_only` and rejected pushkeys. Rules, pushers and counts already exist: this is one outbound request loop away from a phone ringing.
- [ ] **An admin surface** — user list/get/create/update/deactivate/reset, room list/details/delete, force-join, purge. Today: four routes and the CLI.
- [ ] **Authentication that is not a password** — `/login/sso/redirect`, `m.login.token` from an identity provider, MSC2965 discovery, `POST /v1/login/get_token`.

### C. Federation and interop

- [ ] Rejected-event bookkeeping (`/missing_events` and the rejected-event routes) — four Complement cases.
- [ ] Federation `POST /v1/publicRooms` at v1 (both public-room routes were registered at `/v3`).
- [ ] Verify the authorising server's signature on a restricted join, rather than trusting the claim.
- [ ] Refresh the server-server red list and re-rank what the 30 remaining cases are waiting on.
- [x] `PUT /v1|v2/invite/{roomId}/{eventId}` — served, with invite rejection and rescinding (verified 2026-11-04; the gap lists that claimed otherwise were stale).
- [x] Gap filling both ways, and closing a hole a chunked answer left by fetching the missing parents by id (steps 11, 24).
- [x] Knocking, restricted rooms and the space summary that authorises them, both directions (steps 17-19).

### D. Client surface a user feels

- [ ] `state_after` on `/sync`; the top-level `presence` and `org.matrix.msc3814.e2ee` sections filled or removed.
- [ ] `DELETE` on account data (MSC3391), `m.fully_marked` where it is still missing, `/relations` recursion.
- [ ] A real search index (full text) instead of substring matching.
- [ ] `POST /_matrix/media/v1/report`, and URL previews.
- [x] Lazy loading that is lazy on `/sync` and `/messages`, MSC3023's ignore list applied server-side, `/members?at=`, departed rooms read as of the leave, sync filters honoured, the `leave` section rebuilt, MSC2675 per-event visibility (steps 23-25).

### E. The MSC surface (where client features arrive first)

- [ ] Delayed events (MSC4140), thread subscriptions (MSC4306/4308), invite filtering (MSC4155), relation filters in `/messages` (MSC3874), poll push rules (MSC3930).
- [x] Simplified sliding sync (MSC4186) at `/v4/sync` and the `org.matrix.msc3575` path, with the five extensions (step 22).
- [x] Room version 12 understood in both directions — hashed room ids, `additional_creators`, creators-privileged rules, state resolution v2.1, stripped state as full events (step 21). Choosing v12 as the *default* is a decision, not a piece of work.

### F. Operations and quality

- [ ] Message retention (`m.room.retention`), fuzz targets, a load harness.
- [ ] Room-version upgrade that carries the state across (the reference passes it, we do not).

### G. Video calls

- [ ] The LiveKit / MatrixRTC plan and its L-steps: [RTC-PLAN.md](RTC-PLAN.md).

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
