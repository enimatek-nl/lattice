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
