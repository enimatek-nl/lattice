# Complement — the upstream suite

How the two Complement packages are run, what a run can hide, and what each recorded failure was
waiting for. The per-group tables below are the record as it stood when each was written (2026-11-02
and 2026-11-04); the current tally and the current red list are in [TODO.md](TODO.md), and the
server-server names in it should be refreshed against a fresh run.


---

## How to run the suite when this shell's podman is broken

A long-lived shell eventually loses its rootless podman namespace: every command ends in
`invalid internal status, unable to create a new pause process: cannot re-exec process to join the
existing user namespace`, and `make complement` dies in `complement-image`. `podman system migrate`
or a fresh shell fixes the shell itself. Until then the suite runs in a transient unit, which is a
different namespace and works — with one thing to carry along, because Complement talks to the
container engine over the Docker API:

```console
systemd-run --user --unit=complement-run --property=WorkingDirectory=$PWD \
  --property=Environment=DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock \
  /usr/bin/bash -c 'make complement COMPLEMENT_RUN=TestName > run.log 2>&1'
```

Without `DOCKER_HOST` the image builds and every blueprint then fails with `permission denied while
trying to connect to the docker API at unix:///var/run/docker.sock`, which reads like a broken
engine and is only a missing variable: `/var/run/docker.sock` here is a symlink to the rootless
podman socket, and the unit does not get the environment the interactive shell had. Note that
`--scope` (rather than `--unit`) inherits the broken namespace and fails the same way as the shell.

## Triage run of 2026-11-20 — the ten "not triaged" names, each run alone

`docs/TODO.md` had entered ten names as "not triaged". Each got its own `go test -run '^TestX$'` with a
170 s timeout, on Complement `7b38a63` against an image built from `main` @ `8c98333`. **All ten are red
alone**, which retires the standing suspicion that `TestSync` was a casualty of running in parallel. The
cause per name; the measurements behind them are in [FINDINGS.md](FINDINGS.md), "Findings of 2026-11-20".

| Name | Where it dies | What it is waiting for |
|---|---|---|
| `TestSync` | `presence.events is empty` | presence streamed in `/sync` — a decision, not a gap |
| `TestMembersLocal` | `presence.events does not exist` | the same decision |
| `TestPresenceSyncDifferentRooms` | `presence.events is empty` | the same decision |
| `TestRoomSummary` | `expected one invited and one joined user, got 0 and 0` | a `summary` (`m.joined_member_count`, `m.invited_member_count`) on a joined room in `/sync` |
| `TestPushRuleRoomUpgrade` | `Key account_data.events does not exist` | a global `account_data` section in `/sync`; the upgrade carry-over itself is never reached |
| `TestSyncTimelineGap` | its subtests **pass**; the mock federation server rejects our state probes | stop asking for state the join already handed over (the chain at `internal/service/fedgap.go:415`) — three wasted round trips per join on real peers as well |
| `TestThreadsEndpoint` | `HaveInOrder: index 0 got $root want $root\|$reply` | MSC3856's bundling of `unsigned."m.relations"."m.thread".latest_event`; the ordering is already right |
| `TestThreadedReceipts`, `TestThreadReceiptsInSyncMSC4102` | `PUT /pushrules/global/postcontent/… → 400 M_INVALID_PARAM` | MSC4306's `postcontent` rule kind — and, one step before it, an unknown kind answering `404` so the suite's own "do you have this rule?" probe survives |
| `TestUploadKey` | three sub-cases of one name | `/keys/upload` answering 200 to a `device_keys` object without `algorithms`/`keys`/`signatures` (want 400 `M_BAD_JSON`) and 200 to one naming a **different user** (want 400); `/keys/query` answering `{}` where the case wants the queried user present as an empty object — there the suite is stricter than v1.19, see FINDINGS |

Ten names, six pieces of work: presence streaming (three names, one decision), `/sync` response shape (two
sections, two names — one of them hiding a third case behind itself), the `postcontent` rule kind (two
names), `/threads` bundling (one), `/keys/upload` validation (two sub-cases of one), and the redundant
state probe after a join (one, where the assertions already pass). The whole package was run from the same
tree the same day: **82 green, 22 red, 2 skipped in 409 s** — the identical tally and the identical 22
names as 2026-11-18, which is what makes a red list worth working down (`csapi-tally.txt`,
`csapi-names.txt`).

Logs and scripts: `.agent-work/complement-triage-20261120/` (`summary.txt`, one log per name,
`run-one-by-one.sh`, `run-package.sh`). Running it from a session inside the pi-courier bridge needs a
transient scope; that and the reason are written up in FINDINGS.

---

## Run of 2026-11-18 — the package finishes now, and its number is lower

Against Complement `7b38a63`, both fixes on `main` (`/messages` omitting `end`, and the image
running with `-no-rate-limit`): **`tests/csapi` completes in about six minutes — 82 green, 22 red,
2 skipped.** It could not be run to completion before: `TestMessagesOverFederation` paged a room
whose `end` token never went away, and the server answered every one of those pages at 100% CPU
until the 40-minute ceiling fell. That also means the 95-of-106 recorded below was never measured
in one run; from today the honest figure is the one a single completing run prints.

Two names left the red list with it (`TestTxnInEvent`, `TestTxnScopeOnLocalEcho`: events now carry
`unsigned.transaction_id` for the device that sent them), and one arrived as something to work on
rather than something to wait out — `TestMessagesOverFederation` walks the room and the early
history is simply not there, which is the backfill-for-late-joiners gap, named.
`TestTxnIdWithRefreshToken` went green the same day: `POST /v3/refresh` read its token from the
query string while the specification puts it in the JSON body, so the refresh was refused before the
case could reach the transaction id it is about.

Two more pairs of names went the same way the same afternoon: `TestMembershipOnEvents`, once events
carried MSC4115's `unsigned.membership` — the reader's own membership at the moment each event was
written — and `TestRoomSpecificUsernameAtJoin` with `TestRoomSpecificUsernameChange`, once the user
directory answered a list instead of `null` and started opening a private room's members to the
people in it.

The 22 names and what each is waiting for are in [TODO.md](TODO.md). The run this all comes from:
`COMPLEMENT_BASE_IMAGE=localhost/lattice-complement:latest go test -timeout=30m -count=1 -v
./tests/csapi` on Complement `7b38a63` — 79 `--- PASS`, 25 `--- FAIL`, 2 `--- SKIP`, `ok` in 365s,
`--- FAIL: github.com/matrix-org/complement/tests/csapi (365.18s)` as the closing line, 329 own
tests across three packages in the same tree.


---

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
make complement COMPLEMENT=~/complement   # the whole csapi suite, ~7 min
make complement COMPLEMENT_RUN='TestRoomAlias|TestRoomCreate'
make complement COMPLEMENT_PKG=./tests    # the server-server suite (91 tests)
make complement COMPLEMENT_PKG=./tests COMPLEMENT_RUN='TestJoinViaRoomIDAndServerName'
make complement-fast                      # csapi without the blocked names: 82 green in 159s
make complement-both                      # csapi and server-server at once, one process per package
make complement-fed                       # server-server alone — the way to count federation
```

Two of those targets exist because of a measurement. A complete `tests/csapi` run took
409 s while its 106 tests summed to 407 s of own time: the package is entirely serial —
no top-level test calls `t.Parallel()` — and 52% of that wall clock sat in the 22 names
that are red on purpose (`docs/TODO.md`). Splitting *one package* over several processes
is the move that looks obvious and is wrong, and it does not look wrong: Complement fixes
a namespace per package (`TestMain(m, "csapi")`), names blueprint containers
`complement_csapi.<blueprint>.hsN`, and every process calls `Cleanup()` when it starts,
which deletes everything labelled `complement_pkg=csapi` — including the live homeservers
of the other lanes. Six lanes measured **63 red instead of 22**, over a summary line that
reads perfectly.

Different *packages* own different namespaces, which is what the two new targets use.
`make complement-both` measured **373 s for both suites together** — 106 client-API tests
and 91 server-server tests, 82 green and 78 green — where running them one after the other
costs the 409 s plus the 338 s of own time the server-server suite adds up to.
`make complement-fast` leaves the blocked names out and came back with the same 82 green in
**159 s**. The scripts are in `deploy/complement/`, with the mechanism written out at the top
of `run-suites.sh` so nobody has to rediscover it.

The client-API tally reproduced **name for name** across three runs that day. The
server-server tally did not: run twice against the same commit, `fed` came back 78 green and
then 77, with `TestJumpToDateEndpoint` and `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room`
trading places between the two runs (2026-11-20, both with the parallel preset). Run alone
with `make complement-fed` the same commit returned to **78 green, 13 red in 300 s** — the
recorded tally, at its own pace. That is the rule this file already states, shown in the
numbers rather than argued: a federation test waiting on a five second window moves when
another suite owns the CPU, so a federation number is only a number once it has been counted
alone.

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
| `unsigned.transaction_id` on the local echo of a `PUT /send` (`/sync`, `/event`, `/messages`) — shipped 2026-11-18, and green except where `POST /v3/refresh` stood in the way, which read its token from the query string until the same day | TestTxnInEvent, TestTxnScopeOnLocalEcho, TestTxnIdWithRefreshToken |
| History visibility on `GET /rooms/{roomID}/event/{eventID}`: refuse non-members, and events from before the join — **addressed by step 23**, unverified against the suite since; `TestLeaveEventVisibility` may still be red, because a user who has left reads nothing here (see step 23's openings) | TestFetchEventNonWorldReadable, TestFetchHistoricalJoinedEventDenied, TestFetchHistoricalInvitedEventFromBeforeInvite, TestLeaveEventVisibility |
| Sync `leave` sections and timeline gaps: a left room reappearing with its old history, `limited` + `prev_batch` on a gapped timeline, room summaries for rooms with no timeline | TestSync, TestSyncTimelineGap, TestGappedSyncLeaveSection, TestOlderLeftRoomsNotInLeaveSection, TestLeftRoomFixture, TestArchivedRoomsHistory, TestRoomSummary |
| `lazy_load_state` in `/messages` | TestRoomMessagesLazyLoading, TestRoomMessagesLazyLoadingLocalUser |
| Presence — deferred on purpose (no `m.presence`), so these are expected red | TestPresence, TestPresenceSyncDifferentRooms, TestMembersLocal |
| Device list updates: `GET /users/{id}/sync` and the `device_lists` edges they feed | TestDeviceListUpdates, TestInviteFromIgnoredUsersDoesNotAppearInSync |
| Threads and relations: `/threads`, `/relations` pagination, threaded receipts, MSC4102 | TestThreadsEndpoint, TestRelationsPagination, TestRelationsPaginationSync, TestThreadedReceipts, TestThreadReceiptsInSyncMSC4102 |
| `GET /rooms/{roomID}/members?at=<sync token>` — membership at a sync position | TestGetRoomMembersAtPoint |
| Room forget: `/forget` then reading the room, and the sync `leave` section that follows it | TestRoomForget |
| `/messages` with a filter (chunk contents) | TestRoomImageRoundtrip |
| `m.room.power_levels` writes in a privileged-creators room version (MSC4289): the creator in `users` | TestPowerLevels |
| `/search` results across a room upgrade | TestSearch |
| `/keys/upload` refusing a bad one-time key with 400, and `/keys/query` for the device behind it | TestUploadKey |
| Registration edges: `/register/available` wants 400 `M_USER_IN_USE` for a taken name (Complement's expectation, not Synapse's 200 `available: false`), and a UIA body without the `session` the server issued wants 401 | TestRegistration |
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
