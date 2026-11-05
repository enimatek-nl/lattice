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

## Latest filtered run (2026-11-05, push notifications)

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
