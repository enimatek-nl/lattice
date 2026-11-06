# Blockers and open items

Nothing currently blocks progress. Items below are dependencies between milestones, known risks and
deliberate decisions that must not be forgotten.

## Blocking *specific* Complement tests (not the project)

| Item | Effect | Resolution |
| --- | --- | --- |
| `GET /sync` not implemented | Complement `TestLogout/Can_logout_current_device`, `TestLogout/Can_logout_all_devices`, `TestDeviceManagement/DELETE_/device/{deviceId}`, `…_requires_UI_auth_user_to_match_device_owner` cannot be verified — those tests use `/sync` as their assertion vehicle | M3 (`/sync`). Behaviour is already covered by `tests/integration/auth_test.go` |
| Media storage not implemented | Media upload/download/thumbnail suites fail with 501 | M5 media repository |
| Federation exchange not implemented | Every `federation_*`/join/backfill test fails | M4 |

## Design risks carried forward

1. **Single-writer SQLite (gormlite)** with `MaxOpenConns(1)`: correctness is fine, throughput is not.
   Recorded in `ARCHITECTURE.md`; a Postgres-capable engine option is an M7 decision, and storage
   already sits behind repository interfaces so the swap is contained.
2. **`/versions` advertises v1.1…v1.19** while most endpoints answer `M_UNRECOGNIZED`. Individual
   endpoints fail loudly, but the range overstates coverage. Decision needed before M3: keep the range
   (client behaviour is predictable) or narrow it to what is genuinely usable. Tracked in
   `docs/compliance-matrix.md`.
3. **`capabilities.m.room_versions` names a default room version that cannot be created yet.** Must be
   made truthful in M2, not left aspirational.
4. **`TestFilter` passes incidentally** (it only asserts 4xx). Do not count it as filter compliance.
5. **bcrypt cost 4 in the Complement image** is a test-only setting; the production default (10) is
   validated in config. Worth re-checking if CI ever treats the image as a deployment template.

## Environment notes

- Container runtime here is podman behind the `docker` CLI; `HEALTHCHECK` is dropped under the OCI image
  format (podman warns), so Complement relies on `/versions` polling. Real Docker honours the
  healthcheck. Not a code problem, but it explains a warning in build output.
- `COMPLEMENT_DIR` defaults to `../complement`; the local checkout is `~/complement` at
  `7b38a63c9db3337292bee5887fc8533be3a57b9f`.
- `golangci-lint` is installed; `make lint` uses it and falls back to `go vet` if absent.

## F-006 — `redacts` in room versions 3-10: specification ambiguity (open, low risk)

The per-version PDU tables list a top-level `redacts` property, but the same pages'
redaction strip list does not protect it, so the redacted form — hence the reference hash and
event ID — drops it. gomatrixserverlib drops it (`unredactableEventFieldsV1` in
`eventcrypto.go`). Synapse keeps redaction rules in Rust under `rust/src/redactions/`, which
could not be read in this session.

Decision: follow the normative strip list, matching gomatrixserverlib.
Risk: a peer that preserves `redacts` derives a different event ID for v3-v10 redaction
events.
Action: the golden test in `tests/unit/redaction_test.go` plus Complement's redaction tests
will expose it; revisit then.

## F-007 — environment: no pip, raw-source fetches blocked

`pip`/`pip3` are unavailable and `raw.githubusercontent.com` fetches were refused, so GitHub
code search via the shell helper was not usable. Reading dependencies from the local Go
module cache (`~/go/pkg/mod/...`) works well for Go implementations and is the recommended
route next time. Not blocking.

## F-008 — process note: M2 task list was stale after iteration 1

`TASKS.md` still showed T-2000/T-2001 unchecked and PROGRESS/ARCHITECTURE had no M2 section
while the code, requirements and compliance matrix were already committed. Corrected in this
iteration. Cause: a docs update script aborted partway (anchor string mismatch) and the
commit ran anyway. Countermeasure: commit docs updates separately from the script, and check
`git show --stat` before moving on.

## F-009 — rule 7 blocks rule 9.4: how the first power levels event is ever sent (open, resolved by choice)

The per-version rules reject a state event whose required level (`state_default`, 50) is
above the sender's level, and with no `m.room.power_levels` event in the room every user is
at `users_default` (0). Rule 9.4's "the first power levels event is allowed" is therefore
unreachable as written.

Evidence from other implementations: gomatrixserverlib's
`NewPowerLevelContentFromAuthEvents` gives the creator `9007199254740991` when the room has
no power levels event (citing Synapse `api/auth.py`), while its privileged-creator path uses
`CreatorPowerLevel - 1` (99). Synapse does the equivalent in its auth rules.

Decision (`internal/eventauth.powerLevelsFor`): the room creator holds 2^53-1 while the room
has no power levels event; a non-creator is still refused by rule 7 (tested).
Risk: a peer that grants 99 refuses a first event that sets a level above 99.
Action: check against Complement's power-level and room-creation tests, and against a
Dendrite room join, before treating it as settled.

## F-010 — `m.room.aliases` authorisation diverges between implementations (open, low risk)

The specification drops the aliases special case in room version 6; Synapse gates it by
version (`special_case_aliases_auth`), gomatrixserverlib applies it for all room versions
(`aliasEventAllowed`, state key must equal the sender's server name, exempt from membership
and power-level checks).

Decision: follow the specification and Synapse — `rules.SpecialCaseAliases` only for versions
up to 5.
Risk: in a version 6+ room this server rejects `m.room.aliases` events a peer accepts, so the
alias list can differ between servers (a missing alias, not a forked room).
Action: revisit if Complement or an interop test shows alias disagreement.

## F-011 — the on-receipt rejection outcome is not stored (open, blocks federation correctness)

`eventauth.Snapshot` asks whether an event was rejected when it arrived, because the
authorisation rules and both state-resolution iterative checks must not use a rejected
event as an auth event. The event table has no such column, so
`internal/rooms.dbSnapshot.Rejected` always reports false.

Why it does not bite yet: every event in a room this server created passed the checks
before it was written, so there is nothing to report. It bites the moment federated
events are accepted, where a peer can send an event that failed here.

Action: add the column and write the outcome during ingest (T-2006a), before
`internal/federation` starts accepting PDUs.

## F-012 — no membership endpoints, so rooms are single-member (resolved)

Closed in iteration 7: join (both spellings), leave, invite, kick, ban, unban and knock
are implemented and tested, and Complement's `TestCannotKickNonPresentUser` passes. The
refusal of `m.room.member` on `/send` remains deliberate.

## F-013 — `/sync` is missing, and it is now the gate on most room tests (open, M3)

With creation, sending, membership and aliases working, the Complement suites listed in
T-3001 proceed through every protocol step and then stop at `GET /sync` returning 404.
This is not a protocol disagreement: the tests use sync to observe the state they have
just written, which is also how real clients work. Until sync exists, no multi-step room
flow can be demonstrated end to end from a client's point of view.

Action: T-3001 (sync tokens, timeline, state, invite rooms).

## F-014 — anything that needs another homeserver answers 501 (open, M4)

Inviting, banning or kicking a user on another server, and resolving an alias owned by
another server, are refused with 501 and an explicit message. Consequence: Complement's
multi-server suites (knocking with a remote invite, restricted-room remote joins, all
federation suites) cannot pass. The refusals are recorded rather than hidden, and 403 or
200 would both be lies.

## F-015 — federation reaches the point where a room has to exist on both sides (open, M4)

The transport, the credential, the key trust chain, the transaction handling and the
receiving checks work between two homeservers in one process (`TestFederationHandover`).
They do not yet let a *user* on one server be in a room on another: `/make_join`,
`/send_join`, `/state_ids`, `/backfill`, `/invite` and `/event_auth` are not implemented
(T-4009), and an event whose predecessors or auth events are missing is refused rather
than fetched (T-4007). Consequence: Complement's federation suites still fail here even
though the parts they test first now exist, because Complement drives them through a join.

Action: T-4009 (the join/leave/invite exchange), with T-4007 (missing-event fetch) needed
for any handover that does not arrive in causal order.

## B-0008 — full Complement suite cannot run to completion on this host: Podman lock exhaustion

Symptom, measured on 2026-11-06: a full-suite run (`RUN='.'`, 17 packages) reports 59 top-level
passes and 161 failures, and the failures are not test failures. Their bodies read

    Deploy: Deploy returned error … ContainerCreate: Error response from daemon: container create:
    allocating lock for new container: allocation failed; exceeded num_locks (2048)

`TestUnknownEndpoints` and `TestRoomSummaryAllowedRoomIDs` are among them; both passed in filtered
runs 40 minutes earlier, and their durations in the full run are 0.4-0.9s — too short to have
talked to a homeserver. So the full-suite tally is contaminated and must not be read as a
compliance measurement. Filtered runs of a handful of tests are unaffected.

What was tried, and why it did not work:

  * Raising `num_locks` in `~/.config/containers/containers.conf`. 32768 and 16384 are refused —
    `failed to open N locks in /libpod_rootless_lock_1000: numerical result out of range` — and so
    is 4096, because the rootless lock set already exists at 2048 and Podman will not resize it in
    place. Resizing means deleting that set, which is what `podman system reset` does, and that
    also deletes the user's images and stopped containers. **Not run: it destroys other people's
    work.** The config file was removed again, so the host is back to its previous state (checked:
    `docker ps` lists the pre-existing containers).
  * Reaping leaks: no orphaned `conmon` processes (2 running, both for live containers), no stopped
    `complement_*` containers, no stale files under the storage lock directories that exist. So the
    locks are not held by leftovers that can be reaped without a reset.

What works instead: keep Complement runs to a bounded number of deployments. `scripts/complement.sh`
now passes `-p 2` by default (override with `PARALLEL=n` or a `-p` on the command line) and says so
in its output. That is not enough on its own for the whole suite — the suite creates hundreds of
containers in one process — so the full tally still cannot be produced here.

For whoever reads this next: the question to answer is whether the 2048 rootless lock set can be
recreated at a larger size *without* `podman system reset` (which is destructive). If it cannot,
then a full-suite run needs either a host with Docker rather than rootless Podman, or the suite run
package-by-package with a lock-releasing step between packages. Ask the user before touching the
storage of a machine that has other containers on it.
