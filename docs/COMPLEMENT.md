# Complement — the upstream suite

How the two Complement packages are run, what a run can hide, and what each recorded failure was
waiting for. The per-group tables below are the record as it stood when each was written (2026-11-02
and 2026-11-04); the current tally and the current red list are in [TODO.md](TODO.md), and the
server-server names in it should be refreshed against a fresh run.


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
