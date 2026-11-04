# Coverage compared: Lattice and the Rust reference

Two documents, one yardstick. [spec-coverage.md](spec-coverage.md) is the coverage document of a
Rust homeserver (spec v1.19, default room version 12, Complement as its conformance gate:
88/90 federation and 105/107 client-server). [coverage.md](coverage.md) is ours (spec v1.19,
default room version 11, Complement **61 green of 91** server-server as of 2026-11-04 and
**66 green of 106** client-API as of 2026-11-02, plus this repository's own 317 tests: 160
client-API, 88 two-server, 69 unit). Their claims are theirs - this comparison reads what each
document says and what our own runs measure, not their code. The client-API tally has not been
re-measured since 2026-11-02: the run attempted on 2026-11-04 did not finish inside its budget
(see the Complement section of [TODO.md](TODO.md)), so treat that half of our tally as two
federation rounds old.

Read together, the two are the same shape of server - rooms, membership, federation with
restricted joins, media, E2EE, sync in both its forms. What separates them is where each has
spent its effort. The Rust server has spent it on the *surface*: applicationservices, the admin
API, SSO delegation through MAS, MatrixRTC, a batch of MSCs, push delivery. Lattice has spent it
on the *depth*: federation correctness (auth chains, state resolution reads, gap filling,
soft-fail, the one rulebook for authorization), the space summary and restricted joins both
directions, and a two-server test suite of its own. Two items that this document once listed as
their advantages have since closed - **room version 12** and **simplified sliding sync** - so the
sliding-sync row is now a comparison of details rather than of presence.

The Complement tallies are still not comparable head-to-head: theirs is 193 of 197 across both
packages, ours is 61 of 91 in one and 66 of 106 in the other, and the two packages test very
different amounts of this server.

## Area by area

| Area | Reference | Lattice | Reading |
|---|---|---|---|
| Versions, well-known, capabilities | done | done | parity |
| Registration, login, UIA, devices, profile | done, plus shared-secret admin registration, appservice device masquerading | password/dummy/token flows, UIA, devices, profile, refresh tokens | **behind on how a user gets authenticated elsewhere**: no SSO, no OIDC, no MAS, no 3PID. Their "no 3PID" is our "no 3PID" too. |
| Rooms, membership, events | done | done | parity; they list `timestamp_to_event` and `/context`, we serve both, but our walk is wrong in a forked history |
| Relations, threads, search, spaces | done: recursion, `/threads`, bundled aggregations, Postgres full-text search, `/hierarchy` with remote children | `/relations` without `recurse`, `/threads`, bundled aggregations, `/hierarchy` both directions, substring search | **behind**: relation recursion and a real search index |
| `/sync` (classic) | done, incl. `state_after`, lazy loading, threaded receipts, per-thread counts, ignored users | same shape, three holes | **behind**: `lazy_load` not lazy, `m.ignored_user_list` not applied, no `state_after`; the MSC3814 `e2ee` section is declared and never filled |
| Simplified Sliding Sync (MSC4186) | done: lists with ranges and filters, subscriptions, per-connection deltas with position retry, `required_state` wildcards (`*`, `$ME`, `$LAZY`), and the to-device, e2ee, account-data, receipts and typing extensions | done: the same five extensions at `/v4/sync` and the `org.matrix.simplified_msc3575` path — ranges with a `count` that ignores them, the eight filters, subscriptions, per-connection deltas with `initial`/`expanded_timeline`/state stubs, and a `pos` refused `M_UNKNOWN_POS` unless it was issued to this device, which still pages `/messages` | **near parity, and the five extensions match**, including the state-key tokens: their document's `*`, `$ME` and `$LAZY` are answered here too, which is worth saying because MSC4186 defines only the wildcard - the other two come from the proxy, and the clients that used it are still in the wild. What we do not have is `topic`, `truncate` or per-list `bump_event_types`. Their document contradicts itself on the MSC4308 thread-subscription extension (the sliding-sync row says it is missing, the MSC row says it passes Complement); we do not have MSC4306 at all, so we do not have it either |
| Media | done, incl. URL previews, async uploads, remote media | done, same shape | parity; `POST /_matrix/media/v1/report` missing on our side |
| E2EE | done, incl. key backups, cross-signing, to-device | done, same shape | parity |
| Push | done: rules evaluated *and delivered* to HTTP pushers | rules, pushers, counts; no Push Gateway | **behind**: nothing reaches a phone. The evaluation half exists, so this is one outbound client away |
| Server-server keys, signing | done | done | parity |
| Server-server join/leave/invite/knock | done, incl. invite rejection and rescinding, server ACLs, hierarchy | knock and restricted joins both directions, ACLs, hierarchy, a v12 invite carried as full stripped state | **near parity**; they have the dedicated `PUT /invite` handshake, we do not; we do not verify the authoriser's server signature, they do not say |
| Server-server transactions and EDUs | done: PDU pipeline, missing events, rejected events, EDUs | same, presence deliberately local | **behind on rejected-event bookkeeping** (four Complement tests); presence is a decision, not a gap |
| Backfill, missing events, state reads | done, with history-visibility redaction | done both ways, and a gap in our view is settled by asking the peer for the events we lack in the order the room was built (`get_missing_events` with `min_depth`) and by judging a peer's claimed state against our own resolution | parity, with one bug of ours: our federation `/state/{roomId}` is registered at a path the specification does not use, so a peer asking the specified way is answered with nothing |
| Appservices | done: YAML registrations, namespaces, `as_token` auth with `user_id`/`device_id`, ephemeral forwarding, user/alias queries, third-party lookups | not implemented | **behind, and the most consequential structural gap**: no bridges (matrix-bot, hookshot, mautrix-\*), no application-service login, no `receive_ephemeral` |
| Admin API | Synapse-compatible subset: users, rooms, force-join, delete, purge, server notices | a small custom set plus the CLI | **behind**: no operators' surface for user or room administration |
| MSC3861 / MAS delegation | done, tested against MAS 1.27 | not implemented | **behind** |
| Unstable MSCs | seven groups, all Complement-passing: MSC3391, MSC3874, MSC3930, MSC3967, MSC3890, MSC4155, MSC4140, MSC4306/4308 | one unstable path (sliding sync under `org.matrix.simplified_msc3575`) and five capability flags | **behind**, and it is where client features arrive first: delayed events, thread subscriptions, invite filtering, account-data deletion |
| Search | full-text (`word`), substring (`pg_trgm`) or both | substring only | **behind** |
| MatrixRTC (MSC4143/4195) | done over LiveKit, incl. federation `get_token` and legacy JWT endpoints | not implemented (planned: the plan is in [TODO.md](TODO.md)) | **behind**; the OpenID pieces they need are already here |
| Operations | rate limiting, retention, fuzz targets, load-test swarm, deployment assets | rate limiting, session expiry, deployment, CLI, endpoint coverage tool | partly behind: no retention, no fuzzing, no load harness; the coverage tool is something they do not have |
| Room versions | 1-12, default 12 | 1-12 understood, default 11 | **near parity**. v12's hashed room ids, `additional_creators`, the creator rules and state resolution v2.1 are in, in both directions; what is left is the default a new room gets and the two v12 knock/restricted Complement cases |
| Tests | Complement as the gate | Complement **and** 160 client-API, 88 two-server, 69 unit tests in-repo | different, and in our favour: a two-server suite in the repository is what found the restricted-join, knock and state-resolution findings |

## Where Lattice lacks, ranked by what it unlocks

Two of the ten items this list carried have closed: **room version 12** (step 21) and
**simplified sliding sync** (step 22). They are left out of the numbering rather than marked
done, because the list is a work queue.

1. **Application services** - registrations, namespaces, `as_token` auth with `user_id` and
   `device_id`, transaction delivery with ephemeral data, user/alias queries. Nothing
   bridging works without it, and it is also the route by which a bot holds an identity.
2. **Push delivery** - `POST /_matrix/push/v1/notify` as a *client* of a push gateway, with
   `event_id_only` and rejected pushkeys. The rules, pushers and counts already exist.
3. **Federation odds that peers actually ask for** - `PUT /v1|v2/invite` (invite rejection and
   rescinding), the specified `GET /v1/state/{roomId}?event_id=` path, federation
   `publicRooms` POST at v1, rejected-event bookkeeping. Two of these are bugs rather than
   missing features.
4. **Auth that is not a password** - SSO (`/login/sso/redirect`), `m.login.token` issuance from an
   identity provider, MSC2965 discovery, and the `/login/get_token` endpoint appservices and
   bridges need.
5. **Sync and timeline correctness for clients** - real lazy loading, `m.ignored_user_list`
   applied, `state_after`, `m.fully_marked`, the MSC3814 `e2ee` section filled or removed,
   account-data deletion (`DELETE` on account data), the `timestamp_to_event` walk. Sliding
   sync took the *size* of the problem - a client that only wants six rooms no longer pays for
   the account - but not these.
6. **Admin surface** - user list/get/create/update/deactivate/reset, room list/details/delete,
   force-join, purge. Today an operator has a CLI and four routes.
7. **The MSC surface** - relations recursion, thread subscriptions (MSC4306/4308), delayed
   events (MSC4140), invite filtering (MSC4155), account-data deletion (MSC3391). This is where
   client features arrive first, and MSC4306 is now on the critical path twice: it is a client
   feature of its own, and it is what Complement's only sliding-sync case asks for.
8. **Search index, message retention, fuzzing, a load harness** - quality of life at scale.
9. **Room version 12 as the default** - understanding v12 is done; choosing it for new rooms is
   a decision about the creators-privileged semantics, not a piece of work.

## Where Lattice is not behind

- **Knocking and restricted joins, both directions.** MSC3202 and MSC3083 including the
  `restricted`/`knock_restricted` space summary (`allowed_room_ids` over federation), and
  Complement's whole restricted/knock block is green except the v12 variants.
- **Room version 12, understood rather than accepted.** The room id that is the hash of the
  create event, `additional_creators`, the rule that a creator cannot be demoted, stripped
  state as full events, and state resolution v2.1 applied to a peer's claim about the state at
  an event. The default for new rooms is deliberately still 11.
- **Simplified sliding sync.** Same five extensions as the reference, and a position token that
  is refused with `M_UNKNOWN_POS` unless this user and device were given it - and that still
  works as a batch token in `/messages`, which is the property that lets a sliding-sync-only
  client page a room at all.
- **One rulebook.** The room's authorization rules are the same code for an event this server
  signs and an event a peer sends; the two had drifted before, and the drift is what the
  knock, administrator and rank findings were.
- **A gap in the history is settled, not worked around.** `get_missing_events` answers in the
  order the room was built and honours `min_depth`, and a peer's claim about the state at an
  event is judged against this server's own resolution rather than accepted or refused on the
  letter of what the peer cited.
- **A two-server test suite in the repository** (88 tests) that runs on every `go test ./...`,
  with the shape checks written as prose in the assertions.
- **The endpoint coverage tool** (`cmd/endpoints` diffing routes against a v1.19 index), which is
  why this document can state an endpoint tally - 180 of 234 indexed endpoints, 43 routes the
  index does not name; the reference document states none.
- Room reports, room summary, mutual rooms, refresh tokens, account locking and suspension,
  registration availability, and a deliberate answer for every endpoint that is not implemented
  (`M_UNKNOWN` with a reason rather than an empty 404).
