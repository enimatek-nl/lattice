# Requirements

Levels follow the spec keywords (MUST / MUST NOT / SHOULD / MAY). `Status` uses the honest
vocabulary from AGENTS.md §15: `implemented+tested`, `implemented`, `partial`, `not implemented`.
Requirement coverage and Complement coverage are tracked separately (see COMPLIANCE-MATRIX.md).

## Foundation / operations

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-OPS-001 | The server is a single Go binary; configuration comes from file + environment + flags, never hardcoded | MUST | implemented+tested | `tests/unit` config table tests |
| REQ-OPS-002 | Structured logging; MUST NOT log access tokens, passwords or private key material | MUST | implemented+tested | logging redaction tests |
| REQ-OPS-003 | Graceful shutdown on SIGINT/SIGTERM; background workers have bounded lifecycle | MUST | implemented+tested | `tests/integration` shutdown test |
| REQ-OPS-004 | Client API served over plain HTTP, federation API over HTTPS on separate listeners | MUST (interop) | implemented | integration TLS listener test; Complement federation port |
| REQ-OPS-005 | Request body size limits and timeouts on all listeners | SHOULD | implemented | middleware tests |
| REQ-OPS-006 | Container image satisfies the Complement image contract | MUST (testing) | implemented | Complement `Deploy` succeeds |

## Error model and routing

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-ERR-001 | Errors returned as `{"errcode","error"}` JSON with spec codes | MUST | implemented+tested | unit + integration |
| REQ-ERR-002 | Unknown endpoints under known `_matrix/*` prefixes → 404 `M_UNRECOGNIZED` | MUST | implemented+tested | unit router tests; Complement `TestUnknownEndpoints` |
| REQ-ERR-003 | Wrong method on a known endpoint → 405 `M_UNRECOGNIZED` | MUST | implemented+tested | unit router tests; Complement `TestUnknownEndpoints` |
| REQ-ERR-004 | MUST NOT leak internal error details to clients | MUST | implemented+tested | error mapping tests |
| REQ-ERR-005 | Malformed JSON body → `M_NOT_JSON`; semantically invalid JSON → `M_BAD_JSON` | MUST | partial (M_NOT_JSON only) | unit tests; `TestJson` is an M2 target |

## Appendices: encoding, signing, identifiers

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-APP-001 | Canonical JSON: UTF-8, no whitespace, keys sorted by Unicode codepoint | MUST | implemented+tested | all spec example vectors |
| REQ-APP-002 | Canonical JSON numbers restricted to integers in `[-(2^53)+1, (2^53)-1]`; reject `-0`, fractions, exponents | MUST | implemented+tested | table tests incl. out-of-range rejection |
| REQ-APP-003 | Signing JSON: exclude `signatures` and `unsigned`, `ed25519:<keyid>`, unpadded base64 | MUST | implemented+tested | round-trip + spec example shape |
| REQ-APP-004 | Signature checking follows the 7-step procedure (unknown algs dropped, base64 decode, canonical remainder) | MUST | implemented+tested | verify tests incl. multi-signature |
| REQ-APP-005 | Validate server names per grammar; treat case-sensitively | MUST | implemented+tested | table tests |
| REQ-APP-006 | Validate user IDs: localpart charset `a-z0-9._=-/+`, non-empty, total ≤255 bytes; downcase on registration | MUST | implemented+tested | table tests + Complement registration tests |
| REQ-APP-007 | Room/alias/event ID grammar validation (≤255 bytes, sigils, per-room-version forms) | MUST | partial (grammar level; per-room-version rules land with room versions) | unit tests |
| REQ-APP-008 | Unpadded base64 for binary values; accept padded input on decode | MUST/SHOULD | implemented+tested | base64 tests |

## Identity, auth and security

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-AUTH-001 | `POST /register` performs User-Interactive Auth; unauthenticated attempt returns 401 with `session` and `flows[].stages` | MUST | implemented+tested | integration tests; Complement `TestRegistration` |
| REQ-AUTH-002 | Open registration offers `m.login.dummy`; shared-secret admin registration supported with secret from config | MUST/MAY | implemented+tested | Complement shared-secret subtests |
| REQ-AUTH-003 | Invalid localpart → 400 `M_INVALID_USERNAME`; taken localpart → 400 `M_USER_IN_USE` | MUST | implemented+tested | unit + Complement |
| REQ-AUTH-004 | Usernames downcased when allocating user IDs | MUST | implemented+tested | unit + Complement |
| REQ-AUTH-005 | Passwords MUST NOT be stored in plaintext | MUST | implemented+tested (bcrypt) | unit; verify hash round-trip |
| REQ-AUTH-006 | Weak passwords rejected (`M_WEAK_PASSWORD`) | MUST (policy) | implemented+tested | unit |
| REQ-AUTH-007 | Access tokens are high-entropy, stored hashed (not plaintext), revocable per device | MUST | implemented+tested | unit + logout tests |
| REQ-AUTH-008 | `POST /login` supports `m.login.password` with `identifier` (MXID or localpart) and honours `device_id` | MUST | implemented+tested | Complement `TestLogin` |
| REQ-AUTH-009 | Missing token → 401 `M_MISSING_TOKEN`; invalid/expired → 401 `M_UNKNOWN_TOKEN` (+`org.matrix.labels: ["org.matrix.msc2168.unverified"]` not required) | MUST | implemented+tested | unit + Complement logout/sync 401 |
| REQ-AUTH-010 | `POST /logout` revokes only the current device; `POST /logout/all` revokes all | MUST | implemented+tested | Complement `TestLogout` |
| REQ-AUTH-011 | `GET /account/whoami` returns authenticated user | MUST | implemented+tested | integration |
| REQ-AUTH-012 | Device registry (`GET /devices`, `GET/DELETE /devices/{id}`) reflects sessions | MUST | implemented (update of device display name pending) | Complement `TestLogout` |
| REQ-AUTH-013 | Server signing key: ed25519, persisted, single active key id `ed25519:v1`, published via `/_matrix/key/v2/server` | MUST | implemented+tested | unit + integration |
| REQ-AUTH-014 | Refresh tokens / short-lived access tokens | MAY | not implemented (documented) | n/a |
| REQ-AUTH-015 | OAuth 2.0 auth (MSC3861 family, v1.15+) | MAY (legacy still permitted) | not implemented | recorded |

## Client-Server API surface (M1 subset)

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-CS-001 | `GET /_matrix/client/versions` lists spec versions + `unstable_features` object | MUST | implemented+tested | Complement `TestVersionStructure` |
| REQ-CS-002 | `GET /_matrix/client/v1/capabilities` requires auth; includes `m.room_versions`, `m.change_password` | MUST | implemented+tested | Complement `TestServerCapabilities` |
| REQ-CS-003 | `/.well-known/matrix/client` discovery document when configured | SHOULD/MAY | implemented+tested | unit |
| REQ-CS-004 | Rooms/events/state/sync/messages | MUST (protocol core) | not implemented (M2/M3) | — |
| REQ-CS-005 | Media endpoints | MUST (media module) | not implemented (stub returns 501 `M_UNKNOWN` so method semantics hold) | — |

## Server-Server API surface (M1 subset)

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-FED-001 | Federation endpoints served over HTTPS with a valid certificate on the federation port | MUST | implemented | Complement PKI entrypoint + integration TLS test |
| REQ-FED-002 | `GET /_matrix/federation/v1/version` present (any method → 405 `M_UNRECOGNIZED`) | MUST/SHOULD | implemented+tested | Complement `TestUnknownEndpoints` |
| REQ-FED-003 | `GET /_matrix/key/v2/server` returns signed server key document; `POST /_matrix/key/v2/query` present | MUST | implemented+tested (self-query) | unit + integration |
| REQ-FED-004 | Federation request authentication (signed X-Matrix `origin`/`key`/`mac`) | MUST | not implemented (M4) | — |
| REQ-FED-005 | Transactions, PDU validation, EDUs, joins, backfill, missing events, state exchange | MUST | not implemented (M4+) | — |
| REQ-FED-006 | Federation failures must not block unrelated local operations | MUST | not implemented (M4, design in ARCHITECTURE) | — |

## Rooms, events, state (architecture requirements of M1; implementation M2+)

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-EVT-001 | Events modelled per room version (id/hash rules, `auth_events`, `prev_events`, depth, hashes, signatures, redaction) | MUST | not implemented (schema+design documented) | — |
| REQ-EVT-002 | History stored as an event DAG with forward extremities, not a linear list | MUST | design documented | ARCHITECTURE §5 |
| REQ-EVT-003 | Event authorization as a dedicated, room-version-aware subsystem | MUST | design documented | ARCHITECTURE §7 |
| REQ-STATE-001 | State resolution per room version (v1/v2/v3), never last-write-wins | MUST NOT substitute | design documented | ARCHITECTURE §6 |
| REQ-RVER-001 | Room versions handled through one abstraction; no hardcoded version | MUST | design documented (M2) | — |

## Persistence

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-DB-001 | Storage supports users, devices, access tokens, signing keys | MUST | implemented+tested | repository tests |
| REQ-DB-002 | Storage layer reachable only through repository interfaces (swappable engine) | MUST | implemented+tested | — |
| REQ-DB-003 | Atomic operations use DB transactions | MUST | implemented+tested (register+device+token atomicity) | integration test on rollback |
| REQ-DB-004 | Indexes for the common lookups (token hash, user localpart, room/event id) | MUST | partial (users/devices/tokens indexed; event tables arrive in M2) | schema review |
| REQ-DB-005 | Event/state/membership/sync/transaction tables | MUST | not implemented (M2/M4) | — |
| REQ-DB-006 | SQLite is not assumed sufficient for every deployment; limits documented and boundary kept | MUST | documented | ARCHITECTURE §9 |

## Testing and process

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-TEST-001 | Unit + integration tests, table-driven, race detector on | MUST | implemented | `make test` |
| REQ-TEST-002 | Tests live in a dedicated `tests/` tree, not next to implementation files | MUST (project) | implemented | repo layout |
| REQ-TEST-003 | Complement runs against a reproducible image; results recorded; assertions never weakened | MUST | implemented | `scripts/complement.sh`, PROGRESS.md |
| REQ-TEST-004 | Requirement + compliance tracking maintained | MUST | implemented | this file, COMPLIANCE-MATRIX.md |
| REQ-TEST-005 | Every protocol bug fixed gets a regression test | MUST | policy adopted | per-fix in PROGRESS.md |

## Client API requirements added during M2/M3

| ID | Requirement | Level | Status | Verification |
| --- | --- | --- | --- | --- |
| REQ-ROOM-016 | Server-Server API v1.5: versioning — rooms v1 through v11 are advertised, and a client can create and use a room in each of them; v2 and v6 follow their own rules. | Implementation choice; no normative statement about which versions a server must support. | implemented (partially verified) | `tests/unit/roomversions_test.go`, `tests/integration/roomversions_test.go` (v3, v4, v6, v8, v9, v10, v11 created and used), Complement `TestRoomCreate/unsupported room version` (v42 refused, v3-v11 accepted) | v2 and v6 cannot be created by a local client (v2 needs the v1 spelling `join_rule: ban`; v6 needs a join signed by the remote server), so their rules stay unit-tested until federation exercises them (T-4005). |
| REQ-ROOM-017 | Client-Server API v1.11: `POST /rooms/{roomId}/report/{eventId}`. | MUST exist; the specification defines no effect beyond acknowledgement. | implemented | `tests/integration/reporting_test.go` | The report is acknowledged and not stored: no content, retention or effect is specified, so there is nothing specified to store. |
| REQ-ROOM-018 | Client-Server API v1.11: `GET /joined_rooms`. | MUST list the rooms where the user's membership is join. | implemented | `tests/integration/aliases_test.go` (`TestJoinedRooms`) | |
| REQ-ROOM-019 | Client-Server API v1.11: `GET /rooms/{roomId}/messages` with `from`, `to`, `dir`, `limit`, `filter`; response `chunk`, `start`, `end`, `state`. `end` is omitted when no further events are available. | MUST. | implemented (with limits) | `tests/integration/history_test.go`, Complement `TestSendAndFetchMessage`, `TestFetchMessagesFromNonExistentRoom`, `TestRoomMessagesLazyLoading*` | Pages are ordered by `stream_ordering`; the reference server orders by depth then stream position, and its tokens carry both (T-3006). `state` is the room's current state bounded by the requester's entitlement, or the lazy-loaded member events — the state as of the start of the chunk needs per-event snapshots (T-3004). A stored filter ID in `filter` is refused rather than guessed at. |
| REQ-ROOM-020 | Client-Server API v1.11: `GET`/`POST /publicRooms`, `GET /publicRooms/{serverName}`, `GET`/`PUT /directory/list/room/{roomId}`. | MUST list the rooms the server publishes; publishing is a decision by someone with standing in the room. | implemented (partially) | `tests/integration/directory_test.go`, Complement `TestPublicRooms`, `TestRoomState` | Another server's directory answers 501 (federation, T-4005). `filter.roomsOnly` and network filtering unimplemented. Member counts are computed per room per page; a denormalised count is a scaling follow-up. |
| REQ-ROOM-021 | Client-Server API v1.15: `GET /room_summary/{roomIdOrAlias}`, including `join_rule`, `room_version` and `allowed_room_ids` for restricted rooms. | MUST describe a room to someone entitled to know about it. | implemented (partially) | `tests/integration/directory_test.go` (`TestRoomSummaryReportsTheRoom`), Complement `TestRoomSummaryAllowedRoomIDs` | The section is from a later specification release than the pinned v1.11; recorded as DEC-013. `prelude`, `successors` and `predecessor` are not implemented. |
| REQ-SYNC-006 | Client-Server API v1.11: a joined room's `summary` carries `m.joined_member_count` and `m.invited_member_count`. | MUST. | implemented (partially) | `tests/integration/sync_test.go` (`TestSyncRoomSummary`), Complement `TestRoomSummary` | `m.heroes` and `m.marker` are not implemented. |
| REQ-SYNC-007 | Client-Server API v1.11: the events shown for a room the user is out of stop at the membership event that took them out; state is shown only where `m.room.history_visibility` allows it. | MUST NOT show a user events they are no longer entitled to. | implemented | `tests/integration/sync_test.go` (`TestSyncLeftRoomTimelineStopsAtLeaving`, `TestSyncLeftRoomStateFollowsHistoryVisibility`), Complement `TestSyncLeaveSection`, `TestGappedSyncLeaveSection`, `TestOlderLeftRoomsNotInLeaveSection`, `TestArchivedRoomsHistory`, `TestLeaveEventVisibility`, `TestCumulativeJoinLeaveJoinSync` | |
| REQ-READ-001 | Client-Server API v1.11: room reads (`/state`, `/state/{type}/{key}`, `/members`, `/joined_members`, `/aliases`) follow membership and history visibility. | MUST restrict reads to who the room's state allows. | implemented (partially) | `tests/integration/aliases_test.go` (`TestRoomReadsNeedMembership`, `TestFormerMemberReadsStopAtLeaving`), Complement `TestRoomState`, `TestLeftRoomFixture` (its state and member reads), `TestRoomAlias` | A former member's read stops at the moment they left; a key rewritten after that reads as absent rather than as its old value, because the server keeps no per-event state snapshots (T-3004). |

| ID | Decision |
| --- | --- |
| REQ-PUSH-001 | Client-Server API v1.11: v3 — *Push notifications*: `GET /pushrules[/{scope}[/{kind}[/{ruleId}[/enabled\|/actions]]]]`, `PUT /pushrules/{scope}/{kind}/{ruleId}`, `PUT …/enabled`, `PUT …/actions`, `DELETE /pushrules/{scope}/{kind}/{ruleId}`; server-default rules with the specified rule IDs; a rule ID may not start with `.`; creating a rule leaves it enabled. | MUST. | implemented (partially) | `tests/unit/pushrules_test.go`, `tests/unit/pushrulessvc_test.go`, `tests/integration/pushrules_test.go`, Complement `TestPushRuleCacheHealth`, `TestPushSync` | Only the `global` scope exists. Rules are evaluated by nothing yet: no pusher, no push gateway, no `GET /notifications`, and the sync `unread_notifications`/`highlight_count` are still zero (T-3005a). A rule ID containing an encoded slash is read as two path segments. |
| REQ-SYNC-008 | Client-Server API v1.11: v3 — *Sync*: the top-level `account_data` section carries the user's own values (push rules today) when they changed since the token. | MUST. | implemented (partially) | `tests/integration/pushrules_test.go` (`TestPushRulesReachTheClientThroughSync`), Complement `TestPushSync` | The `account` filter's type list is not applied to this section; every changed value is sent. |
| DEC-015 | A user's push rules are stored as one `m.push_rules` account data value per user, not as one row per rule. The client-visible form is a single ruleset and the order within a kind is the meaning; storing it whole keeps that order exact. The cost is that every write rewrites the whole set and rule evaluation reads a JSON blob — acceptable at this scale, recorded as a scaling follow-up. |
| DEC-013 | `GET /v1/room_summary/{roomIdOrAlias}` is implemented although its specification section belongs to a later release than the pinned v1.11: the shape is stable, Complement's helpers use it, and a client's room preview asks for it. Recorded here rather than passed off as pinned-spec work. |
| DEC-014 | Pagination tokens are single `stream_ordering` positions (`s{n}`), shared by `/sync` and `/messages`. The reference server's history tokens carry a `(topological, stream)` pair. Sharing one token type is what lets a sync `prev_batch` be used as a history `from`; depth-ordered paging (T-3006) will widen the token rather than fork it. |


## Decisions for ambiguities (documented, reversible)

| ID | Decision |
| --- | --- |
| DEC-001 | Module path `github.com/enimatek/lattice`. |
| DEC-002 | Storage engine for M1 = gormlite (pure-Go SQLite) via GORM, behind repository interfaces; Postgres later. Compatibility/perf verified with tests (AGENTS.md §6 caveat satisfied). |
| DEC-003 | No dependency on gomatrixserverlib; canonical JSON/signing/identifier layers implemented in-tree. |
| DEC-004 | Access tokens: opaque 256-bit random strings, stored as SHA-256 hashes; no JWT (Matrix semantics preserved). |
| DEC-005 | No refresh tokens in M1 (MAY); access tokens do not expire, revocable. |
| DEC-006 | `/_synapse/admin/v1/register` implemented because Complement requires it; it is a compatibility endpoint, not spec, and is disabled unless a shared secret is configured. |
| DEC-007 | Unimplemented-but-required endpoints return 501 `M_UNKNOWN` rather than pretending to work; endpoints not implemented at all return 404 `M_UNRECOGNIZED`. Media upload is registered as a stub so that the spec-required 405 method semantics hold. |
| DEC-008 | Default room version target = v11 (v12 later behind the same abstraction). |
| DEC-009 | `GET /register` also implements `GET /register/{sid}` and UIA `flows` for `/login` (`m.login.password`). |
| DEC-010 | Federation HTTPS uses a generated self-signed cert by default; Complement PKI is used inside the Complement image. |
| DEC-011 | Specialist skills listed as examples in AGENTS.md §2 are not installed; their concerns (security, DB, test, crypto, devops) are handled directly against the spec and recorded here. |
| DEC-012 | Open registration is enabled by configuration (`registration.open: true`); the Complement image enables it because Complement's harness depends on it. |
