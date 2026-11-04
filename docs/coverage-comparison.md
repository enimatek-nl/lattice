# Coverage compared: Lattice and the Rust reference

Two documents, one yardstick. [spec-coverage.md](spec-coverage.md) is the coverage document of a
Rust homeserver (spec v1.19, default room version 12, Complement as its conformance gate:
88/90 federation and 105/107 client-server). [coverage.md](coverage.md) is ours (spec v1.19,
default room version 11, Complement 61/91, plus this repository's own 293 tests). Their claims are
theirs - this comparison reads what each document says and what our own runs measure, not their
code.

Read together, the two are the same shape of server - rooms, membership, federation with
restricted joins, media, E2EE, sync - with a difference in emphasis. The Rust server has spent its
recent effort on the *surface*: sliding sync, relations and threads, appservices, the admin API,
SSO delegation through MAS, MatrixRTC, and a batch of MSCs. Lattice has spent it on the *depth*:
federation correctness (auth chains, state resolution reads, gap filling, soft-fail, the one
rulebook for authorization), the space summary and restricted joins both directions, and a
two-server test suite of its own. That shows in the two Complement tallies, which are not
comparable head-to-head: theirs is 193 of 197 across both packages; ours is 61 of 91 in one.

## Area by area

| Area | Reference | Lattice | Reading |
|---|---|---|---|
| Versions, well-known, capabilities | done | done | parity |
| Registration, login, UIA, devices, profile | done, plus shared-secret admin registration, appservice device masquerading | password/dummy/token flows, UIA, devices, profile, refresh tokens | **behind on how a user gets authenticated elsewhere**: no SSO, no OIDC, no MAS, no 3PID. Their "no 3PID" is our "no 3PID" too. |
| Rooms, membership, events | done | done | parity; they list `timestamp_to_event` and `/context`, we serve both, but our walk is wrong in a forked history |
| Relations, threads, search, spaces | done: recursion, `/threads`, bundled aggregations, Postgres full-text search, `/hierarchy` with remote children | `/relations` without `recurse`, `/threads`, bundled aggregations, `/hierarchy` both directions, substring search | **behind**: relation recursion and a real search index |
| `/sync` (classic) | done, incl. `state_after`, lazy loading, threaded receipts, per-thread counts, ignored users | same shape, three holes | **behind**: `lazy_load` not lazy, `m.ignored_user_list` not applied, no `state_after`; the MSC3814 `e2ee` section is declared and never filled |
| Simplified Sliding Sync (MSC4186) | done, with the list/subscription/delta machinery | not implemented | **the largest single missing feature.** Modern clients (Element X) speak sliding sync; they cannot use this server |
| Media | done, incl. URL previews, async uploads, remote media | done, same shape | parity; `POST /_matrix/media/v1/report` missing on our side |
| E2EE | done, incl. key backups, cross-signing, to-device | done, same shape | parity |
| Push | done: rules evaluated *and delivered* to HTTP pushers | rules, pushers, counts; no Push Gateway | **behind**: nothing reaches a phone. The evaluation half exists, so this is one outbound client away |
| Server-server keys, signing | done | done | parity |
| Server-server join/leave/invite/knock | done, incl. invite rejection and rescinding, server ACLs, hierarchy | knock and restricted joins both directions, ACLs, hierarchy | **near parity**; they have the dedicated `PUT /invite` handshake, we do not; we do not verify the authoriser's server signature, they do not say |
| Server-server transactions and EDUs | done: PDU pipeline, missing events, rejected events, EDUs | same, presence deliberately local | **behind on rejected-event bookkeeping** (four Complement tests); presence is a decision, not a gap |
| Backfill, missing events, state reads | done, with history-visibility redaction | done both ways | parity; our `/state/{roomId}` is served at a path the specification does not use, which is a bug rather than a gap |
| Appservices | done: YAML registrations, namespaces, `as_token` auth with `user_id`/`device_id`, ephemeral forwarding, user/alias queries, third-party lookups | not implemented | **behind, and the most consequential structural gap**: no bridges (matrix-bot, hookshot, mautrix-\*), no application-service login, no `receive_ephemeral` |
| Admin API | Synapse-compatible subset: users, rooms, force-join, delete, purge, server notices | a small custom set plus the CLI | **behind**: no operators' surface for user or room administration |
| MSC3861 / MAS delegation | done, tested against MAS 1.27 | not implemented | **behind** |
| Unstable MSCs | seven groups, all Complement-passing: MSC3391, MSC3874, MSC3930, MSC3967, MSC3890, MSC4155, MSC4140, MSC4306/4308 | none under MSC paths; four capability flags | **behind**, and it is where client features arrive first: delayed events, thread subscriptions, invite filtering |
| Search | full-text (`word`), substring (`pg_trgm`) or both | substring only | **behind** |
| MatrixRTC (MSC4143/4195) | done over LiveKit, incl. federation `get_token` and legacy JWT endpoints | not implemented | **behind**; the OpenID pieces they need are already here |
| Operations | rate limiting, retention, fuzz targets, load-test swarm, deployment assets | rate limiting, session expiry, deployment, CLI, endpoint coverage tool | partly behind: no retention, no fuzzing, no load harness; the coverage tool is something they do not have |
| Room versions | 1-12, default 12 | 1-11, default 11 | **behind**: this is 14 of our 30 Complement failures |
| Tests | Complement as the gate | Complement **and** 139 client-API, 84 two-server, 70 unit tests in-repo | different, and in our favour: a two-server suite in the repository is what found the restricted-join and knock findings |

## Where Lattice lacks, ranked by what it unlocks

1. **Room version 12** - the auth rules of v12 (`additional_creators`, the room-id-as-hash
   rule), then state resolution v2. 14 Complement tests, and it is the precondition for
   anything that ships in a v12 room.
2. **Application services** - registrations, namespaces, `as_token` auth with `user_id` and
   `device_id`, transaction delivery with ephemeral data, user/alias queries. Nothing
   bridging works without it, and it is also the route by which a bot holds an identity.
3. **Simplified Sliding Sync (MSC4186)** - the sync modern clients ask for. The classic `/sync`
   is complete enough that this is a large project rather than a fix, but it decides whether
   Element X-class clients can connect.
4. **Push delivery** - `POST /_matrix/push/v1/notify` as a *client* of a push gateway, with
   `event_id_only` and rejected pushkeys. The rules, pushers and counts already exist.
5. **Federation odds that peers actually ask for** - `PUT /v1|v2/invite` (invite rejection and
   rescinding), the specified `GET /v1/state/{roomId}?event_id=` path, federation
   `publicRooms` POST at v1, rejected-event bookkeeping. Two of these are bugs rather than
   missing features.
6. **Auth that is not a password** - SSO (`/login/sso/redirect`), `m.login.token` issuance from an
   identity provider, MSC2965 discovery, and the `/login/get_token` endpoint appservices and
   bridges need.
7. **Sync and timeline correctness for clients** - real lazy loading, `m.ignored_user_list`
   applied, `state_after`, `m.fully_marked`, the MSC3814 `e2ee` section filled or removed,
   account-data deletion (`DELETE` on account data), the `timestamp_to_event` walk.
8. **Admin surface** - user list/get/create/update/deactivate/reset, room list/details/delete,
   force-join, purge. Today an operator has a CLI and four routes.
9. **Relations recursion, thread subscriptions, delayed events, invite filtering** - the MSC
   surface where client features arrive first.
10. **Search index, message retention, fuzzing, a load harness** - quality of life at scale.

## Where Lattice is not behind

- **Knocking and restricted joins, both directions.** MSC3202 and MSC3083 including the
  `restricted`/`knock_restricted` space summary (`allowed_room_ids` over federation), and
  Complement's whole restricted/knock block is green except the v12 variants.
- **One rulebook.** The room's authorization rules are the same code for an event this server
  signs and an event a peer sends; the two had drifted before, and the drift is what the
  knock, administrator and rank findings were.
- **A two-server test suite in the repository** (84 tests) that runs on every `go test ./...`,
  with the shape checks written as prose in the assertions.
- **The endpoint coverage tool** (`cmd/endpoints` diffing routes against a v1.19 index), which is
  why this document can state an endpoint tally; the reference document states none.
- Room reports, room summary, mutual rooms, refresh tokens, account locking and suspension,
  registration availability, and a deliberate answer for every endpoint that is not implemented
  (`M_UNKNOWN` with a reason rather than an empty 404).
