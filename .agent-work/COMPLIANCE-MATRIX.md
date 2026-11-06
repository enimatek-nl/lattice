# Compliance Matrix — Complement execution record

Complement reference: `https://github.com/matrix-org/complement`, checkout at
`7b38a63c9db3337292bee5887fc8533be3a57b9f` (2026-11-03), located at `~/complement`
(`COMPLEMENT_DIR`). Companion repo `complement-crypto` is relevant only once E2EE work starts.

## Reproducing these results

```sh
make docker                                    # builds complement-lattice:latest
scripts/complement.sh                          # M1-target filter; finds the checkout in
                                               # $COMPLEMENT_DIR, ../complement or ~/complement
# full suite:
cd ~/complement
COMPLEMENT_BASE_IMAGE=localhost/complement-lattice:latest \
  go test ./tests ./tests/csapi -count=1 -timeout 25m -v
```

Image contract satisfied (`deploy/complement/Dockerfile`, `deploy/complement/entrypoint.sh`):
`EXPOSE 8008 8448`, `SERVER_NAME` honoured, client API on plain HTTP, federation/key API on HTTPS,
certificate signed with the mounted Complement CA (`/complement/ca/ca.{crt,key}`) and that CA added
to the container trust store, `HEALTHCHECK` using the binary's own `healthcheck` subcommand
(podman reports it as ignored under OCI format; Complement then relies on `/versions`, which answers),
shared secret `complement`, storage under `/data`, entrypoint safe to run repeatedly.

## Run 1 — full `./tests` + `./tests/csapi` (2026-11-05)

```
8 top-level tests passed, 189 failed, 0 skipped
FAIL github.com/matrix-org/complement/tests        131.572s
FAIL github.com/matrix-org/complement/tests/csapi   82.260s
```

Passing:

| Test | Why it passes |
| --- | --- |
| `TestVersionStructure` | `/versions` shape is spec-shaped |
| `TestUnknownEndpoints` | 404/405 `M_UNRECOGNIZED` semantics across client, federation, key and media prefixes |
| `TestRegistration` | every subtest, including shared-secret admin registration, downcasing, `q3._=-/`, `/register/available`, and `Registration_without_a_session_fails` |
| `TestCanRegisterAdmin` | shared-secret admin registration and `whoami` |
| `TestLogin` | every subtest including uppercase login and `whoami` |
| `TestServerCapabilities` | `m.room_versions`, `m.change_password`, 401 without token |
| `TestMediaConfig` | `GET /v3/config` returns `m.upload.size` |
| `TestFilter` | **incidental**: it only asserts 4xx for invalid filters; the endpoint itself is not implemented |

Subtests that pass inside top-level failures:

- `TestDeviceManagement`: `GET /devices`, `GET/PUT /devices/{id}`, 404 for unknown devices.
  Failing subtests (`DELETE /devices/{id}`, owner check) fail because Complement performs a
  `/sync?timeout=1000` first, and `/sync` is M3 — not because the device endpoint misbehaves.
- `TestLogout`: both token-error subtests. The two behavioural subtests assert through `/sync`.

Failure cause histogram (response bodies seen by Complement):

| Cause | Count |
| --- | --- |
| 404 `M_UNRECOGNIZED` (endpoint not implemented) | 309 |
| 501 `M_UNKNOWN` (media upload stub) | 28 |
| 400 `M_INVALID_PARAM` (refresh-token refusal) | 1 |

Server health during the run: **0 panics, 0 HTTP 500 responses**. Complement's own assertions were
not modified, and no test was skipped.

## Interpretation

Every failure is a *missing feature*, not a wrong answer, except two worth naming:

1. `TestTxnIdWithRefreshToken` — refresh tokens are refused explicitly (400 `M_INVALID_PARAM`).
2. `TestMediaConfig` passes while media upload is a 501 stub: configuration is real, storage is not.

Rooms, membership, events, state, sync, E2EE key management and federation therefore remain the
long tail; their Complement coverage is the measuring stick for M2–M5.

## Latest filtered run (2026-11-05, device and one-time keys)

Filters: `TestUploadKey`, `TestKeyClaimOrdering`, `TestUploadKeyIdempotency`,
`TestUploadKeyIdempotencyOverlap`, `TestDeviceListUpdates`, `TestDeviceListUpdates`,
`TestPushRuleRoomUpgrade`, `TestPresence`.

Passing: all four key-upload/claim tests, `TestPresence`, and — of the ten subtests of
`TestDeviceListUpdates` — the five that need one homeserver: a local user joining, a local
user leaving, a user rejoining, joining a room with a local user, and leaving one.

Still failing, all of them for the same reason: the subtests that put one of the two users on
a second homeserver. They stop at `POST /join/{roomID}?server_name=hs2` with `M_NOT_FOUND`,
because this server cannot join a room it has never heard of. `TestPushRuleRoomUpgrade`'s two
remaining subtests and the remaining `TestSync` failure are the same missing capability. That
is T-4005 (federation), and it is now the largest single cause of failures in this suite.

## Earlier run (2026-11-05, room upgrade)

Filter: `TestPushRuleRoomUpgrade`. The two subtests that need no federation now pass —
`upgrading a room carries over existing push rules for local users` and the manual variant of
it. What is left failing is the pair that joins a room on a second homeserver
(`joining a remote upgraded room ...`, `joining a remote manually upgraded room ...`), which
fails at `POST /join/{roomID}?server_name=hs1` with `M_NOT_FOUND`: this server cannot join a
room it has never heard of. That is T-4005 (federation), not the upgrade.

## Earlier filtered run (2026-11-05, room upgrade and push rules)

Filter: `TestPushRuleRoomUpgrade`. All four subtests fail, for two different reasons:

  * `upgrading a room carries over existing push rules for local users` and
    `manually upgrading a room ... for local users` — there is no
    `POST /rooms/{roomId}/upgrade` on this server (T-2010). These two need no federation.
  * `joining a remote upgraded room ...` and `joining a remote manually upgraded room ...` —
    they join through a second homeserver, so they are T-4005.

This is the reason T-2010 is next in the queue rather than left as a roadmap item: it has
black-box coverage that fails today without needing federation first.

## Latest filtered run (2026-11-05, device list tracking)

Filter: presence, push rules and push sync, room directory and summaries, `/messages`,
logout and devices, registration and login.
`TestSync/parallel/Device_list_tracking` now passes, including the "even if she makes the
request twice" case. What is left failing in `TestSync` is two things, both known:

  * `sync_should_succeed_even_if_the_sync_token_points_to_a_redaction_of_an_unknown_event`
    — the test needs a second homeserver to create the event, so it is T-4005 (federation).
  * the two MSC4222 `state_after` subtests, and the whole `tests/msc4222` package, which is
    an unstable specification extension this server does not implement.

## Full suite, clean environment (2026-11-06, tree `4502f1e`)

`RUN='.'` over all 17 of Complement's packages, `-p 2`, anonymous-volume store clean, and
`grep -c "exceeded num_locks"` over the log is **0** — so unlike the two runs below it, this one
measures the server and not the host. `.agent-work/next/gate5.log`.

**76 top-level tests pass, 143 fail.** Every package contains at least one failure; that is what
an unimplemented-surface picture looks like, not a server in pieces. Against the earlier run of
2026-11-06 09:39 (`full2.log`, 67/152 — itself contaminated by deploy failures), **nine tests
newly pass and none newly fail**: `TestGetFilteredRoomMembers`, `TestGetRoomMembersAtPoint`,
`TestMembershipOnEvents` (MSC4115), `TestTxnInEvent`, `TestTxnIdempotency`,
`TestTxnIdempotencyScopedToDevice`, `TestTxnScopeOnLocalEcho`, `TestIsDirectFlagLocal`,
`TestWriteMDirectAccountData`.

This is a count of Complement tests, not a statement of Matrix compliance. The failures are
dominated by whole areas that are absent rather than broken — federation room joins and ACLs,
device keys and key claiming, media and thumbnails, threads, receipts, relations, knocking and
restricted rooms, to-device, account data, search, URL previews, MSC suites that are unstable
specifications (msc4242, msc4222, msc4429, msc4291, msc4289, msc4311). A number this size is only
useful as a trajectory, which is why the per-test comparison above is the part worth keeping.

## Latest filtered run (2026-11-06, sending, typing, membership)

Filters: `.agent-work/next/txn1.log`, `txn2.log`, `prevbatch1.log` (Complement unchanged; no
assertion weakened). Newly passing, all verified in this session:

  * `TestTxnIDDeduplication` — a transaction ID is an idempotency key, not a lock; a same-body
    resend returns the same event ID instead of `M_UNKNOWN`.
  * `TestClientNoEncryption` — the whole package, including `TestSendEventSynced`, which had been
    failing on a `PUT /send` whose body could not be decoded at all: `msgtype`/`body` were looked
    for at the top level instead of inside `content`.
  * `TestMembershipOnEvents` — MSC4115. Every event a client is served now carries
    `unsigned.membership`: that reader's own membership of the room as of the moment the event
    was sent, read from the state snapshot the event was sent against. Absence of a member entry
    in that snapshot is `leave`; an event this server has no snapshot of gets no field, because
    "nobody was there" and "we were not there to see it" are different answers.
  * `TestGetFilteredRoomMembers` — all three subtests. `handleMembers` read only `not_membership`,
    so `membership=` was ignored.
  * `TestGetRoomMembers`, `TestGetRoomMembersAtPoint` — the roster, and the roster as it was at a
    synchronisation token. `at` is answered from the state snapshot the event at that position was
    sent against, not from today's state cut by position; `/sync` now issues `timeline.prev_batch`
    as the newest end of the batch, which is the half that made the at-point question answerable at
    all (the Client-Server API does not decide which end it names — matrix-org/matrix-spec#595).
  * `TestSendAndFetchMessage`, `TestRoomMessagesLazyLoading`, `TestRoomMessagesLazyLoadingLocalUser`,
    `TestSyncFilter`, `TestRoomSummaryAllowedRoomIDs` and `TestSync` in `tests/csapi` — unchanged
    from before these changes, i.e. no regression.
  * The M1 gate set (`gate2.log`): every package `ok`.

Correction to this section as first written: it listed `TestTyping` as newly passing. It is not,
and it was never observed passing in this session — the log I read for it was a run of the
transaction-ID filter, which contains no typing test. Typing is not implemented on the
client-facing side: `/sync` builds `ephemeral` as an empty list by construction
(`internal/sync/service.go:320-330`), and typing EDUs received over federation are not stored or
served. Re-running it twice on 2026-11-06 (`.agent-work/next/msc4115d.log`,
`typing1.log`) fails all three of its delivery subtests with `MustSyncUntil: timed out after
5.0s`, consistently. Task T-4036.

What remains outstanding in this family: the same lookup on the read paths that still serve
events without a reader — `/context`, `/relations`, `/state`, the room directory and alias reads.
That is in scope, not a grey area: in the merged specification the property is defined on the
**client event format** itself (`ClientEventWithoutRoomID.unsigned.membership`,
`x-addedInMatrixVersion: "1.11"`), not endpoint by endpoint, so it applies wherever this server
hands a client an event. Task T-4038.

Two sentences from that definition settle questions this session had been guessing at, so they are
quoted rather than paraphrased: the value is the requesting user's `m.room.member` state "at the
point of the event, **including any changes caused by the event**" — which is why a user's own
join event carries `join`, which is what Complement's `TestMembershipOnEvents` checks — and "if
the user had yet to join the room at the time of the event (i.e, they have no `m.room.member`
state), this property is set to `leave`". The property is a **SHOULD**, with the exemption written
out: servers "MAY omit it if necessary (for example, if calculating the value is expensive)"; the
one documented place it is *not* populated is events pushed to application services, where there
is no requesting user. So this server's choice to answer `leave` for a witnessed snapshot that
held no member entry, and to answer nothing for an event it never witnessed, is within the
specification and not a hedge — but T-4040's batching is worth doing all the same, because a
SHOULD is not a licence to be slow. And
`SendEventSynced waiting for event ID …` appears in some runs of `room_members_test.go:74`/`:88`
and not others, which is T-4034 and recorded as intermittent rather than explained.

## Earlier filtered run (2026-11-05, presence)

Filter: presence, push rules and push sync, room directory and summaries, `/messages`,
logout and devices, registration and login.
`TestPresence` passes, including "Presence can be set from sync" and the two "reported to
local room members" subtests, and both presence subtests of `TestSync`
(`Newly_joined_room_includes_presence_in_incremental_sync`,
`Get_presence_for_newly_joined_members_in_incremental_sync`) pass.

`TestSync` in `tests/csapi` now fails on three things only: `Device_list_tracking`
(T-3008, needs device keys), the sync token across a redaction of an unknown event (needs
federation to create the event, T-4005), and MSC4222 `state_after` (an unstable extension
deliberately not implemented). Presence is no longer among them.

## Earlier filtered run (2026-11-05, push notifications)

Filter: push rules and push sync, room directory, room summaries, `/messages`, sync
filters, logout and devices, registration and login, room creation.
**No push-rule test fails**: `TestPushRuleCacheHealth` and `TestPushSync` (including its
"adding/changing a rule wakes an incremental `/sync`" subtests) pass. Still failing in this
filter: `TestRoomCreate` (v2/v6 — federation), `TestSync` in both packages.

`TestSync` in `tests/csapi` fails on four things, and the reasons are now separated:
presence in `/sync` (2 subtests, T-3001c), `device_lists` (1 subtest, T-3008), a two-server
join that needs federation (1 subtest, T-4005), and MSC4222 `state_after` (2 subtests, an
unstable extension deliberately not implemented).

## Earlier filtered run (2026-11-05, history paging and the room directory)

Filter: room directory and public rooms, `/messages`, left and archived rooms, aliases,
membership, registration/login/devices, versions and capabilities, sync filters, room
summaries. **5 failing top level tests**; the causes and task IDs are in
`.agent-work/PROGRESS.md`. Newly passing: `TestPublicRooms`, `TestRoomState`,
`TestRoomSummary`, `TestRoomSummaryAllowedRoomIDs`, `TestSendAndFetchMessage`,
`TestRoomMessagesLazyLoading`, `TestRoomMessagesLazyLoadingLocalUser`,
`TestFetchMessagesFromNonExistentRoom`. No Complement assertion was modified.

## Earlier filtered run (2026-11-05, M2/M3 sync work)

Filter: sync and sync filters, room state/alias/membership/reporting, devices, logout,
registration, login, versions, media config, room summary, left/archived rooms.
**6 failing top level tests** (`TestRoomCreate`, `TestRoomState`,
`TestRoomSummaryAllowedRoomIDs`, `TestSync`, `TestSyncOmitsStateChangeOnFilteredEvents`,
`TestPushRuleCacheHealth`); 15 at the start of the round. Causes and the follow-up task IDs
are in `.agent-work/PROGRESS.md`. No Complement assertion was modified.

## Progress tracking rule

Each milestone records: the Complement filter run, the pass count, and the newly failing/passing
tests attributable to that milestone. A test that starts failing without an explanatory code change
is treated as a regression and blocks the milestone.

## Latest full run (2026-11-08, room versions 1 and 2 — T-4013)

```sh
cd ~/complement
COMPLEMENT_BASE_IMAGE=complement-lattice:latest COMPLEMENT_LOCAL_MIGRATIONS=true \
  go test ./tests ./tests/csapi -count=1 -timeout 25m -v
```

**60 top-level tests pass, 136 fail** across `./tests` and `./tests/csapi`. This is a full-suite
number, the first since Run 1 ("8 passed, 189 failed"), so it is the baseline later cycles compare
against rather than a filtered comparison.

Room versions: `TestRoomCreate` passes in full, including
`POST_/createRoom_creates_a_room_with_the_given_version` — the subtest that had asked for a room
version 2 room and been refused since cycle 4. `TestRedact` passes. Still red, for work not done
rather than for this change: the MSC4297 state-resolution-v2.1 tests (room version 12),
`TestMSC4289PrivilegedRoomCreators_Upgrades` and
`TestMSC4291RoomIDAsHashOfCreateEvent_UpgradedRooms` (version 12 formats), and
`TestOutboundFederationIgnoresMissingEventWithBadJSONForRoomVersion6`.

A room version 1 or 2 room is otherwise covered by this project's own tests rather than by
Complement: `tests/integration/roomversionlegacy_test.go` creates such rooms, checks the
origin-server identifier on the wire, fetches the events over federation and recomputes every
reference hash, and runs a join handshake into a version 2 room.

## Latest full run (2026-11-08, federation key query and discovery robustness — T-4022 opened)

Same command as the previous section. **62 top-level tests pass, 134 fail** (was 60 and 136).

Newly passing, with no newly failing test:

* `TestJoinViaRoomIDAndServerName` — a local user joining a room held by the other homeserver in
  the suite. It had two causes. The Complement image's address policy did not cover the network the
  peer container actually lives on (podman puts created networks in `10.89.0.0/16`, Docker in a
  172.x pool), so the dial was refused as though it were SSRF; the entrypoint now allows the
  networks the container is attached to. And an SRV lookup that fails outright — a resolver that
  does not serve the record type answers SERVFAIL or goes quiet — ended resolution instead of
  falling through to the name on the federation port.
* `TestUserAppearsInChangedDeviceListOnJoinOverFederation` — the same join being the precondition.

Still failing, with the cause narrowed rather than assumed: `TestDeviceListUpdates` (the five
subtests that need two homeservers) and `TestDeviceListsUpdateOverFederationOnRoomJoin`. The device
key query those subtests used to stop at now succeeds — `GET /client/v3/keys/query` for a remote
user is answered by asking that user's server — and they stop later, at a `/sync` wait for a
device-list change that no `m.device_list_update` EDU ever delivers. That is T-4022.

No Complement assertion was modified.

## Latest full run (2026-11-08, device-list announcements over federation — T-4022 closed, T-4023 opened)

Same command. **64 top-level tests pass, 132 fail** (was 62 and 134).

Newly passing, none newly failing:

* `TestDeviceListUpdates` — all ten subtests, including the five that need two homeservers. A key
  upload on one server now reaches the other server's clients: the announcement goes out as an
  `m.device_list_update` ephemeral unit, the receiving server passes it to the local users who share
  a room with that user, and the client is told to re-read the key list.
* `TestDeviceListsUpdateOverFederationOnRoomJoin` — the Complement stub receives an announcement for
  a user who joined *from* this server into a room this server does not hold. That needed a separate
  trigger: an adopted bundle does not run the side effects of a locally accepted event, and the
  joining user is the one fact the other servers have not been told.

Still failing in this area, with the cause checked rather than assumed:
`TestDeviceListsUpdateOverFederation` gets past `POST /createRoom` now that an invite for a user on
another homeserver is countersigned by that server (T-4009c); it stops later, at the device list
assertions. It is not a
device-list gap.

No Complement assertion was modified.

## Latest full run (2026-11-11, room hierarchy — T-4051 closed, T-4056 opened)

`RUN='.*' COMPLEMENT_LOCAL_MIGRATIONS=true scripts/complement.sh -v`, commit `9535c64`.
**89 top-level tests pass, 130 fail, 1 skipped** (was 86 / 133 / 1 in run 2026-11-11 earlier).
Logs: `.agent-work/next/tally-full-20261113b.log`, failure list
`.agent-work/complement-failures-20261113b.txt`.

Newly passing, nothing newly failing except the pair noted below:

* `TestRestrictedRoomsSpacesSummaryLocal` and `TestRestrictedRoomsSpacesSummaryFederation` — the
  space tree, local and federated. The federation one is the reason the Server-Server endpoint exists:
  the space is on hs1, the restricted room on hs2, and hs1 must not describe a room it does not hold.
  hs2 withholds it while it cannot see the space, and lists it once one of its own users joins the
  space; hs1 then answers for its own users, which is why the room stays hidden from the hs1 user who
  never joined the space.
* `TestClientSpacesSummaryJoinRules`, `TestFederatedClientSpaces` — both were waiting on the same
  endpoint.

Newly failing, and checked rather than assumed:

* `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room` — flaky, not a regression: it fails in roughly
  half the runs including alone, at a 5 s `MustSyncUntil` on a relayed member event, with T-4007
  refusal lines in the log. Recorded as T-4056. Its sibling `...FailOver` passed in the same run.
* `TestRestrictedRoomsRemoteJoinInMSC3787Room` — recorded as flaky earlier (2026-11-11) and passed in
  this run.

One run of this tally was thrown away, and the cause is worth recording because it will recur:
podman had accumulated **2043 unnamed volumes** from earlier Complement runs, and mid-run every
deployment began failing with `creating named volume …: allocation failed; exceeded num_locks (2048)`,
which reads as 60 protocol failures that are not failures at all. `docker volume prune -f` clears it;
if a full run suddenly fails in a block at ~0.5 s each, check `docker volume ls -q | wc -l` before
believing the numbers.

No Complement assertion was modified.

## Full run, clean environment (2026-11-11, after T-4056)

`RUN='.*' COMPLEMENT_LOCAL_MIGRATIONS=true scripts/complement.sh -v`, same commit as the run above
(`9535c64`) plus the preflight in `scripts/complement.sh`. **90 pass, 129 fail, 1 skip** (was 89/130/1
on a polluted environment). Logs: `.agent-work/next/tally-clean-20261113.log`, list
`.agent-work/complement-failures-20261113c.txt`. Nothing newly failing; the one difference is
`TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room`, which stopped being flaky once the environment
was fixed — the reason is T-4056, in short: a removed podman network left its `aardvark-dns` record
file behind, and `hs1` kept resolving to an address from that deleted deployment, so one homeserver
dialled another container and was refused for presenting the wrong certificate. Both earlier tallies in
this file were measured on a dirtier network than this one.

## Full run (2026-11-11, profiles — T-4057 closed, T-4058 opened)

`RUN='.*' COMPLEMENT_LOCAL_MIGRATIONS=true scripts/complement.sh -v`, commit `192c3dc`.
**95 pass, 124 fail, 1 skip** (was 90 / 129 / 1). Logs `.agent-work/next/tally-profile-20261113.log`,
list `.agent-work/complement-failures-20261113d.txt`.

Newly passing, nothing newly failing: `TestProfileDisplayName`, `TestProfileAvatarURL`,
`TestDisplayNameUpdate`, `TestAvatarUrlUpdate`, and `TestPushRuleRoomUpgrade` (which had been flaky across
earlier runs and passed here).

The change that could have broken something else is the member event: joins now carry `displayname` and
`avatar_url`, and a profile change appends a restated member event to every room the user stands in.
Nothing in the suite objected — the tests that compare member content compare particular keys, not the
whole object. Still failing in this area, and not because of the profile work:
`TestInboundFederationProfile` and `TestOutboundFederationProfile` (the federation `/profile` query,
T-4058a), and `TestRoomSpecificUsernameAtJoin`/`...Change`, which stop at the user directory
(`POST /user_directory/search` unimplemented, T-4058c).

No Complement assertion was modified.
