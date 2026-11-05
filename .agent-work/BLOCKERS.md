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

## F-012 — no membership endpoints, so rooms are single-member (open, M2)

Join, invite, knock and leave are not implemented, and `/send` deliberately refuses
`m.room.member`. Consequence, stated plainly: no room on this server can currently have a
second member, so the multi-user behaviour that Complement's room tests exercise cannot
pass yet, even though creation and sending work.

Action: T-2007.
