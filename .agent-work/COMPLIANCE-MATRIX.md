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

## Latest filtered run (2026-11-05, presence)

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
