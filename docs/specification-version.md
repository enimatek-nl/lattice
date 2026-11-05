# Specification Version

## Pinned release

| Field | Value |
| --- | --- |
| Specification | Matrix (spec.matrix.org) |
| Pinned version | **v1.19** |
| Release date | 2026-07-08 |
| Pinned on / review date | 2026-11-05 |
| Next review | 2027-05-05, or when a newer release is reviewed |
| Normative source | https://spec.matrix.org/v1.19/ |
| Git tree | https://github.com/matrix-org/matrix-spec/tree/v1.19 |

`/latest` resolved to v1.19 at pin time. Newer or unstable documents are researched separately and
tracked as future work; they are never used as normative requirements (AGENTS.md §4).

Unstable proposals are only referenced where an implementation decision required one, and each such
reference is recorded in `docs/requirements.md` as non-normative context.

## Modules in scope

| Module | Status |
| --- | --- |
| Appendices: canonical JSON, signing JSON, identifier grammar, unpadded base64 | implemented and unit-tested |
| Client-Server API | partially implemented; see `docs/compliance-matrix.md` |
| Server-Server API (federation) | version and server-key endpoints only; PDUs/EDUs, joins, backfill not implemented |
| Application Service API | not implemented |
| Identity Service API | not implemented |
| Push Gateway API | not implemented |
| Room Versions | not implemented yet; v11 planned first, v10/v9 as regression fixtures, v12 behind the same abstraction |
| Olm/Megolm (E2EE) | out of scope until the E2EE milestone |

## Advertised versions

`GET /_matrix/client/versions` advertises `v1.1` … `v1.19` with an empty `unstable_features` object.
Advertising the whole range is only defensible while the endpoints a client would reach in those
versions either work or answer with protocol errors; `docs/compliance-matrix.md` lists what actually
works, and the range is narrowed if a version is ever found to mislead clients.

## Room versions and `capabilities`

`GET /capabilities` reports `m.room_versions` with a default and an `available` map. Room creation is
not implemented, so the advertised default is aspirational until the rooms milestone; this is listed
as a known inconsistency rather than silently reported as supported. `m.change_password` reports
`enabled: false` because `POST /account/password` is not implemented.

## Test reference

Complement (`https://github.com/matrix-org/complement`) is the external compliance reference. The
checkout used, the image contract and the per-test results are recorded in
`.agent-work/COMPLIANCE-MATRIX.md`; the summary lives in `docs/compliance-matrix.md`. Complement's
assertions are never modified to make results look better.
