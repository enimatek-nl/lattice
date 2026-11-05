# Specification Version

## Pinned release

| Field | Value |
| --- | --- |
| Specification | Matrix (spec.matrix.org) |
| Pinned version | **v1.19** |
| Git tree | https://github.com/matrix-org/matrix-spec/tree/v1.19 |
| Release date | 2026-07-08 |
| Pinned on / review date | 2026-11-05 |
| Next review | 2027-05-05 (or when v1.20+ is reviewed) |
| Normative source used | https://spec.matrix.org/v1.19/ |

`/latest` at pin time resolves to v1.19. Newer releases (unstable) are researched separately and
tracked as future work, never used as normative requirements (AGENTS.md §4).

## Modules in scope (v1.19)

| Module | Status in project |
| --- | --- |
| Client-Server API | target of M1/M2/M3 |
| Server-Server API (federation) | architecture in place from M1; protocol implementation from M4 |
| Application Service API | not implemented (recorded) |
| Identity Service API | not implemented (recorded) |
| Push Gateway API | not implemented (recorded) |
| Room Versions | architecture from M1; implementation from M2 |
| Appendices (canonical JSON, signing JSON, identifier grammar, base64) | **implemented and tested in M1** |
| Olm & Megolm | out of scope until E2EE work |

## Room versions

Current spec room versions: v1…v12. v1.16 made **room version 12** the default (MSC4304), with
servers encouraged to keep v11 for a while; v12 removes the domain component from room IDs and
gives creators effectively unbounded power level (MSC4289/MSC4291) plus updated state resolution
(MSC4297).

Project decision: implement v11 as the default room version first (State Resolution 2, hash-based
event IDs, domain-bearing room IDs), keep v10/v9 as regression fixtures for State Resolution 1,
and add v12 behind the same room-version abstraction later. `GET /versions` therefore reports
`room_versions` support via `capabilities`, and no room version is hardcoded in the code paths
(`internal/roomversion` is created in M2).

## Key normative references used in M1 (v1.19)

- Appendices → Canonical JSON (sorted keys by codepoint, UTF-8, integers only within
  `[-(2^53)+1, (2^53)-1]`, no `-0`, no exponents/fractions) with the spec's worked examples.
- Appendices → Signing JSON (`signatures`, `unsigned` excluded, `ed25519:<keyid>` identifiers,
  unpadded base64) and "Checking for a Signature" steps 1–7.
- Appendices → Identifier grammar: server name, user ID (`@localpart:domain`, localpart chars
  `a-z 0-9 . _ = - / +`, ≤255 bytes, homeserver downcases usernames), room ID/alias/event ID forms.
- Client-Server API → `/versions` response shape; standard error responses (`errcode`/`error`,
  `M_UNRECOGNIZED`, `M_UNKNOWN_TOKEN`, `M_MISSING_TOKEN`, `M_FORBIDDEN`, `M_INVALID_USERNAME`,
  `M_USER_IN_USE`, `M_WEAK_PASSWORD`, `M_BAD_JSON`, `M_NOT_JSON`, `M_UNKNOWN`, `M_UNAUTHORIZED`);
  registration + User-Interactive Auth (`GET /register` flows, `401` + `session`, `m.login.dummy`);
  login (`m.login.password`, `identifier`, `device_id`); logout; devices; `/account/whoami`;
  `/capabilities`.
- Server-Server API → `GET /_matrix/federation/v1/version`, `GET /_matrix/key/v2/server`,
  `POST /_matrix/key/v2/query` (endpoint existence needed for correct 405 semantics).

## Unstable / MSC material

Not used as requirements. Anything under `unstable` is recorded as future work only.
