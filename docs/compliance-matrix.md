# Compliance Matrix

Last updated: 2026-11-05.

This matrix distinguishes **implemented**, **tested by this project**, **verified against Complement**,
**partial**, **not implemented** and **not applicable**. An endpoint that merely answers is not
"compliant"; the columns exist so those states cannot be confused.

Specification: Matrix v1.19 (`docs/specification-version.md`).

## Client-Server API

| Area | Endpoint / behaviour | Status | Own tests | Complement |
| --- | --- | --- | --- | --- |
| Versioning | `GET /_matrix/client/versions` | implemented | yes | **verified** (`TestVersionStructure`) |
| Capabilities | `GET /v3/capabilities` (+`v1`) | implemented | yes | **verified** (`TestServerCapabilities`, incl. 401 when unauthenticated) |
| Registration | `POST /register` UIA, `m.login.dummy`, downcasing, invalid-character and duplicate handling, `device_id` echo, non-ASCII passwords | implemented | yes | **verified** (`TestRegistration`, all subtests) |
| Registration | `GET /register/available` | implemented | yes | **verified** (`TestRegistration`, 3 subtests) |
| Registration | UIA session is required for the credential-less `m.login.dummy` stage | implemented | yes | **verified** (`Registration_without_a_session_fails`) |
| Registration | Closed registration refused (`M_FORBIDDEN`) rather than silently allowed | implemented | yes | n/a (harness always deploys open registration) |
| Compat | `GET/POST /_synapse/admin/v1/register` shared secret (admin, downcasing, symbol rejection) | implemented | yes | **verified** (`TestRegistration`, `TestCanRegisterAdmin`) |
| Login | `GET /login` flows, `POST /login` password flow, localpart/uppercase MXID, `device_id`, wrong password and unknown user → 403 `M_FORBIDDEN` | implemented | yes | **verified** (`TestLogin`, all subtests) |
| Logout | `POST /logout`, `POST /logout/all`, `M_MISSING_TOKEN`, `M_UNKNOWN_TOKEN` | implemented | yes | **partially verified**: both token-error subtests pass; the two behavioural subtests assert through `GET /sync`, which does not exist yet (M3) |
| Devices | `GET /devices`, `GET/PUT /devices/{id}`, 404 for unknown device, `initial_device_display_name` | implemented | yes | **verified** (`TestDeviceManagement`, those subtests) |
| Devices | `DELETE /devices/{id}` with UIA (challenge, wrong password, owner check, token invalidation) | implemented | yes | **not verified by Complement** (its subtest reaches `/sync` first); verified by `tests/integration/auth_test.go` |
| Session | `GET /account/whoami` | implemented | yes | **verified** (inside `TestLogin`) |
| Errors | 404 `M_UNRECOGNIZED` for unknown paths, 405 `M_UNRECOGNIZED` + `Allow` for wrong methods, across client/federation/key/media prefixes | implemented | yes | **verified** (`TestUnknownEndpoints`, all subtests) |
| Discovery | `/.well-known/matrix/client`, `/.well-known/matrix/server` | implemented | yes | n/a |
| Media | `GET /v3/config` (upload size) | implemented | yes | **verified** (`TestMediaConfig`) |
| Media | upload / download / thumbnail / async | not implemented | stub answers 501 | `TestMediaConfig` passes; upload tests fail with 501 (recorded, not hidden) |
| Filters | `POST /filter` validation | not implemented | no | `TestFilter` "passes" only because it asserts 4xx and we answer 404 — **incidental, not compliance** |
| Rooms | `POST /v3/createRoom`: presets, `room_version` negotiation (`M_UNSUPPORTED_ROOM_VERSION`), `creation_content` limits, initial state, name/topic/avatar shorthand | implemented | yes | not verified (Complement room tests also need membership endpoints) |
| Events | `PUT /v3/rooms/{roomID}/send/{eventType}/{txnID}`: authorisation, hashing, signing, storage, per-device transaction idempotency | implemented | yes | not verified |
| Events | `PUT /send/m.room.member/...` refused with 400 `M_UNRECOGNIZED` (membership has its own endpoints) | implemented | yes | not verified |
| State | `PUT /v3/rooms/{roomID}/state/{eventType}[/{stateKey}]`, including the empty state key and rejection of deeper paths | implemented | yes | not verified |
| Rooms | join / invite / knock / leave, `GET /state`, `/members`, `/messages`, `/event/{eventID}`, redaction (`PUT /redact/{eventID}/{txn}`), aliases, directory, upgrades | implemented | `tests/integration/redaction_test.go`, `tests/integration/history_test.go`, Complement `TestRedact`/`TestPowerLevels` | still missing: `GET /rooms/{roomID}/context/{eventID}` (T-2015), `GET /rooms/{roomID}/event_auth/{eventID}` (T-2016), `GET /_matrix/federation/v1/state_ids` |
| Sync / messages / filters | all | not implemented | — | fails with 404 (M3) |
| E2EE device keys, to-device, key backup | all | not implemented | — | fails with 404 (M5) |
| Account: deactivate, change password, 3PIDs | all | not implemented | — | fails with 404 |
| Presence, typing, receipts, read markers | all | not implemented | — | fails with 404 |
| Directory, aliases, spaces, search, user directory | all | not implemented | — | fails with 404 |
| Refresh tokens | `refresh_token` | not implemented | refusal tested | `TestTxnIdWithRefreshToken` fails on our 400 `M_INVALID_PARAM` — an explicit refusal, not a silent ignore |
| Push, notifications, application services, URL preview, SSO | all | not implemented | — | fails with 404 |

## Server-Server and key API

| Area | Status | Own tests | Complement |
| --- | --- | --- | --- |
| `GET /_matrix/federation/v1/version` | implemented | yes | **verified** (the 405 half of `TestUnknownEndpoints`) |
| `GET /_matrix/key/v2/server` (signed document, served as the canonical bytes its signature covers) | implemented | yes (`TestServerKeysVerifies` checks the body is canonical and verifies the signature over it) | **verified** (`TestInboundFederationKeys`; it failed while the endpoint re-encoded the document) |
| `POST /_matrix/key/v2/query` (answers for this server only) | partial — *asking* a notary is implemented and tested; answering about other servers is not | yes | not directly exercised |
| Server discovery: `.well-known/matrix/server` delegation, `_matrix._tcp`/`_matrix-federation._tcp` SRV records, IP literals, explicit ports, certificate-name rules, caching, refusal of a broken delegation | implemented | yes (`tests/unit/federationdiscovery_test.go`) | not verified: Complement gives every container a fixed `SERVER_NAME` and no DNS |
| Address policy for dialed addresses (loopback, link-local metadata, CGNAT, multicast, private ranges denied by default; configurable allow/deny) | implemented | yes (`tests/unit/federationdiscovery_test.go`, `tests/unit/config_test.go`) | n/a. Known limit: enforced at resolution, not at dial time |
| Remote signing keys: fetch, self-signature requirement, one-week trust clamp, retired keys until `expired_ts`, expiry enforced per room version, notary fallback, cache reuse | implemented | yes (`tests/unit/federationkeys_test.go`) | not verified |
| `X-Matrix` request authentication (in and out) | implemented | yes (`tests/unit/fedauth_test.go`, `tests/integration/federation_test.go`) | not verified: needs two homeservers in one Complement run |
| `PUT /_matrix/federation/v1/send/{txnId}`: authentication, origin agreement, PDU/EDU caps, per-PDU answers, transaction replay answered identically | implemented | yes (`tests/integration/federation_test.go`) | not verified (needs two servers) |
| A refusal is a verdict this server keeps: `event_rejections` records why an event was refused (bad signature, wrong hash, unauthorised), an event citing a refused auth event is refused under rule 2.3 naming that refusal, and a refusal that was only a gap — missing predecessors, an auth event nobody has mentioned — is deliberately **not** recorded, because the gap may still be filled | implemented+tested | `tests/unit/rejections_test.go`, `tests/integration/federation_test.go` (`TestFederationRefusalIsAVerdict`) |
| PDU ingest checks in specification order: validity in room version, event ID = reference hash, content hash, origin signature, authorisation against cited auth events; remote `m.room.create` adopted with its own room version | implemented+tested | `tests/integration/federation_test.go`, `tests/unit/eventsignature_test.go` |
| Soft-failure: stored, marked, not relayed, not a forward extremity, still answered over `/event` | implemented, partially tested | the refusal path is asserted end to end; the "client never sees it" half is not | not verified |
| `GET /_matrix/federation/v1/event/{eventId}` with the room-membership restriction | implemented | yes (`TestFederationEventAccess`) | not verified |
| Outbound queue: per-destination batching, non-double-claiming, bounded retries, remote refusals recorded rather than retried, worker tied to process lifetime | implemented | yes (`TestFederationSenderQueue`) | not verified |
| EDUs: received, capped, acknowledged, logged — and nothing else | partial | receipt path only | not verified |
| `m.room.server_acl` is enforced: an excluded server is refused the join template, the room's state, its authorisation chain, a single event, and any PDU it pushes; a room's events are not relayed to it | implemented+tested | `tests/integration/serveracl_test.go`, `tests/unit/serveracl_test.go` |
| Join handshake (`/make_join`, `/send_join`, both halves), `/state_ids`, `/event_auth` as a client, and the missing-event read (`GET /event` as a client, to fill a room's history) | implemented+tested between two homeservers in one process | `tests/integration/federationjoins_test.go`, `tests/integration/joinremote_test.go` | not verified: no `federation_*` Complement test runs against this server yet |
| Leaves, invites, knocks (`/make_leave`, `/send_leave`, `/invite`, `/make_knock`, `/send_knock`), answering `/backfill`, `/hooda`, third-party invites | not implemented | — | recorded as open tasks (T-4009c) rather than a silent gap |
| TLS on the federation listener (TLS ≥ 1.2, CA-signed certificate support) | implemented | yes | every run uses it: containers start healthy and federation attempts reach the handshake |

## Protocol primitives (appendices)

| Area | Status | Tests |
| --- | --- | --- |
| Canonical JSON (spec example vectors, integer range, escaping, UTF-8) | implemented | `tests/unit/canonicaljson_test.go` |
| Signing / verifying JSON (7-step procedure, multiple signatures, unpadded base64) | implemented | `tests/unit/signjson_test.go` |
| Identifier grammar (server names, user/room/alias/event/device IDs, downcasing) | implemented | `tests/unit/matrixid_test.go` |
| ed25519 signing key generation, persistence, key id format | implemented | `tests/unit/storage_test.go` |

### Protocol primitives (M2, in progress — not reachable from any endpoint yet)

| Capability | Status | Evidence |
| --- | --- | --- |
| Room version registry v1-v12 (state res, event ID format, auth rule set, redaction profile, join rules, integer power levels, creator placement, redacts placement, domain-free room IDs) | implemented | `tests/unit/roomversion_test.go` |
| Room version grammar validation | implemented | `tests/unit/roomversion_test.go` |
| Only supported versions offered/advertised (`roomversion.Supported`/`Available`) | implemented and now wired into `/capabilities`, so the advertised set and the set rooms can be created in are one table | `tests/integration/rooms_test.go` |
| Redaction algorithm, per room version | implemented | `tests/unit/redaction_test.go` |
| Content hash, reference hash, event ID derivation | implemented | `tests/unit/redaction_test.go`, `tests/unit/eventseal_test.go` |
| Hashing + signing an outgoing event (signature over the redacted form) | implemented | `tests/unit/eventseal_test.go` |
| Cross-implementation agreement on redaction and event IDs | verified against gomatrixserverlib v0.0.0-20260716140101-4fe595dc7f58 by golden test | `TestRedactionAndEventIDsAgainstReferenceImplementation` |
| Event authorisation rules (rules 1-10) for room versions 3-11, with version-gated join rules and power-level validation | implemented | `tests/unit/eventauth_test.go` |
| Auth events selection and its validation (duplicates, foreign rooms, rejected auth events) | implemented | `tests/unit/eventauth_test.go` |
| Restricted-room allow list evaluation | partial: enforced for locally created joins only | `tests/unit/eventauth_test.go` |
| Event authorisation for room versions 1 and 2 | not implemented (refused with `ErrRulesetUnsupported`) | `tests/unit/eventauth_test.go` |
| State Resolution 2 (room versions 2-11): auth difference, power events, reverse topological power ordering, mainline ordering, iterative auth checks — and the live path consults it: each event's state snapshot is recorded (`state_groups`), and a room with more than one forward extremity has its current state written from the resolved answer, not from the order events arrived | implemented+tested | `tests/unit/stateresolve_test.go` (the algorithm, both argument orders, and the timestamp ordering over JSON-decoded events), `tests/integration/statresolution_test.go` (the same fork handed to two homeservers in opposite orders; both agree, both keep the losing branch, and the answer matches the algorithm run standalone) |
| State resolution for room versions 1 and 12 | not implemented (refused with `ErrAlgorithmUnsupported`) | `tests/unit/stateresolve_test.go` |
| State resolution cross-checked against another implementation | not done yet | follow-up task T-2005a |
| Event graph storage (events, prev/auth edges, per-event auth context) and the forward-extremities query | implemented | `tests/unit/eventstore_test.go` |
| Transaction ID store for `/send/{type}/{txn}` idempotency (unique per device) | implemented and exercised through the endpoint | `tests/unit/eventstore_test.go`, `tests/integration/rooms_test.go` |
| `depth` validation against `prev_events` | not implemented | M2 |
| Redaction application rules: redact level, or the sender's domain matching the domain of the event's sender; a redaction whose partner has not arrived is kept and re-checked when it does | implemented+tested | `tests/unit/redaction_rules_test.go`, `tests/integration/redaction_test.go` (`TestRedactionArrivingBeforeItsTargetIsAppliedWhenTheTargetTurnsUp`) |
| Join handshake, resident half: an unsigned join template with the room's version, refused before signing for a user the room does not permit; the signed event checked against what its URL claims, accepted into the graph, signed by this server too, and answered with the pre-join state and its auth chain | implemented+tested (two homeservers in one process) | `internal/api/federation/joins.go`, `internal/rooms/join.go`, `tests/integration/federationjoins_test.go` |
| Join handshake, joining half: `POST /join` of a room held elsewhere asks a named server for the template, refuses to sign anything that is not its own user's join in that room, seals and names the event itself, and keeps the room it is handed back | implemented+tested — a user on hs2 joins, speaks and is heard in a room on hs1 | `internal/federation/join.go`, `internal/rooms/joinremote.go`, `tests/integration/joinremote_test.go` |
| A handed-over room is stored as a snapshot: every event verified on its own, applied in dependency order, a missing citation kept as a gap rather than a verdict, a forgery refused and remembered | implemented+tested | `internal/rooms/bundle.go`, `tests/integration/joinremote_test.go` |
| An event accepted without its predecessors is an outlier: its state counts, and it is kept out of timelines and forward extremities | implemented+tested | `internal/persist/event.go`, `internal/persist/history.go`, `internal/persist/sync.go` |
| A room's history is caught up by asking a server that was there first: holes are named from this room's own events, fetched newest-first, walked back until they meet what this server holds, stored parents-first so this server's own ordering follows causality, and bounded by a budget that leaves the rest askable | implemented+tested (triggered by a join; no worker pass or on-demand trigger yet) | `internal/federation/backfill.go`, `internal/rooms/gap.go`, `tests/integration/joinremote_test.go`, `tests/unit/eventstore_test.go` |
| `/state_ids` and `/event_auth`: a room's state and an event's auth chain, by ID, to servers the room has a seat for | implemented+tested | `tests/integration/federationjoins_test.go` |
| `m.room.server_acl`: a room excluding a homeserver, applied to the join/state/auth endpoints and per PDU | not implemented | open task; the federation handlers name where the rule belongs |
| Redaction on the wire: a homeserver forwards the unredacted event plus the redaction, and each receiver strips its own copy; the content hash is therefore checked unconditionally | implemented+tested | `internal/events/verify.go`, `tests/integration/redaction_test.go`, cross-checked against gomatrixserverlib's unconditional content-hash check |
| What a client is shown for a redacted event: the emptied form, the redaction under `unsigned.redacted_because`, and `redacts` where its room version looks — the same on `/event`, `/messages`, state reads and `/sync`; and a redaction whose partner has not arrived is kept but not shown | implemented+tested | `internal/redaction/serve.go`, `tests/unit/serve_test.go`, `tests/integration/redaction_test.go` (`TestSyncExplainsWhyAnEventItAlreadySawWentEmpty`, `TestARedactionIsNotShownToAClientUntilItsTargetArrives`) |
| `unsigned.redacted_because` on a redacted event served to a client, and `redacts` placed for the client's room version | implemented (client-server reads: `/event`, `/messages`, `/context`, state) — **not yet** in `/sync`, which serves the redacted form but not the `unsigned` copy | `tests/integration/redaction_test.go`, `tests/unit/redaction_rules_test.go` |

### Evidence run after the redaction work (2026-11-05)

| Item | Value |
| --- | --- |
| Suites run | `./tests/csapi` with `RUN='TestJson\|TestPowerLevels\|TestRedact\|TestEventAuth\|TestRoomMessagesLazyLoading\|TestFetchMessagesFromNonExistentRoom\|TestArchivedRoomsHistory\|TestRoomCreate\|TestRoomMembers\|TestRoomState\|TestCannotKick\|TestNotPresentUserCannotBanOthers\|TestSync'` |
| Passed | `TestRedact` (first time: redaction is applied and read back through `GET /event`), `TestPowerLevels` (was failing only because `/event/{eventID}` was missing), `TestJson`, `TestRoomState`, `TestRoomMembers`, `TestRoomMessagesLazyLoading`, `TestRoomMessagesLazyLoadingLocalUser`, `TestArchivedRoomsHistory`, `TestFetchMessagesFromNonExistentRoom`, `TestCannotKickLeftUser`, `TestCannotKickNonPresentUser`, `TestNotPresentUserCannotBanOthers`, `TestSyncFilter`, `TestSyncLeaveSection` |
| Failed and why | `TestRoomCreate`: room version 2 refused (v1/v2 deliberately unimplemented). `TestSync/parallel/sync_should_succeed_even_if_the_sync_token_points_to_a_redaction_of_an_unknown_event`: the test builds the redaction on a federation peer, which needs the join/`/state_ids` handshake this server does not answer yet. `TestSync/parallel` state-section subtests: MSC4222 `state_after` and sync filters (T-3001f). `TestSyncTimelineGap`: history pages by stream position, not depth (T-3001d) |
| Not changed by this work | the `redacted_because` copy is not attached in `/sync` (only on `/event`, `/messages`, `/context` and state reads), and a redaction this server may not apply is still shown to clients rather than withheld — both recorded as open tasks |

### M2 evidence run (2026-11-05, same checkout and image)

Run with `RUN='TestJson|TestPowerLevels|TestRedact|TestEventAuth|TestRoomMessagesLazyLoading|TestFetchMessagesFromNonExistentRoom|TestArchivedRoomsHistory|TestRoomCreate|TestRoomMembers|TestRoomState|TestCannotKick|TestNotPresentUserCannotBanOthers|TestJumpToDateEndpoint'`.

| Result | Tests | Why |
| --- | --- | --- |
| Passed | `TestJson`, `TestRoomState`, `TestRoomMembers`, `TestRoomMessagesLazyLoading`, `TestFetchMessagesFromNonExistentRoom`, `TestArchivedRoomsHistory`, `TestCannotKickNonPresentUser`, `TestNotPresentUserCannotBanOthers` | the rooms/events core behaves as the specification describes for a single server |
| Failed | `TestRedact/Event_content_is_redacted` | `m.room.redaction` is stored but never applied (T-2009) |
| Failed | `TestPowerLevels/PUT …/m.room.power_levels can set levels` | the test's `SendEventSynced` reads the event back through `GET /rooms/{roomID}/event/{eventID}`, which this server does not implement (404 `M_UNRECOGNIZED`); the power-level step itself answered 200 and the permission step answered 403 (T-2014) |
| Failed | `TestRoomCreate/creates_a_room_with_the_given_version` | asked for room version 2; refused with `M_UNSUPPORTED_ROOM_VERSION` by design (T-4013) |
| Failed | `TestJumpToDateEndpoint` | `/messages` pages by stream position, not depth, and `/timestamp` does not exist (T-3001d) |
| Failed | `TestEventAuth` | needs federation `/make_join` (T-4009) |

Client endpoints the M2 scope named and this server still does not have (`GET /rooms/{roomID}/event/{eventID}` and `PUT /rooms/{roomID}/redact/{eventID}/{txnID}` landed since — both Complement's `TestRedact` and `TestPowerLevels` now pass): `GET /rooms/{roomID}/event/{eventID}`, `GET /rooms/{roomID}/context/{eventID}`, `POST /rooms/{roomID}/redact/{eventID}/{txn}`, `GET /rooms/{roomID}/event_auth/{eventID}`, `GET /rooms/{roomID}/timestamp`, and (federation-side) `GET /rooms/{roomID}/state_ids`.

### Targeted run after the first federation work (2026-11-05)

| Field | Value |
| --- | --- |
| Suites run | `./tests`, `./tests/csapi`, `./tests/msc4222` with `RUN='TestRoomState\|TestRoomCreate\|TestRoomMembers\|TestSync\|TestRoomAlias\|TestRoomSummary\|TestVersionStructure\|TestUnknownEndpoints\|TestRegistration\|TestLogin'` (and a broader pass over `./tests/...` recorded separately) |
| Passed | `TestRoomState` (now, and only after the response shape was corrected), `TestRoomAlias`, `TestRoomMembers`, `TestRoomSummaryAllowedRoomIDs`, `TestVersionStructure`, `TestUnknownEndpoints`, `TestRegistration`, `TestLogin`, and the M1 regression suites |
| Failed and why | `TestRoomCreate/creates_a_room_with_the_given_version`: asked for room version **2**, which this server refuses with `M_UNSUPPORTED_ROOM_VERSION` — room versions 1 and 2 are deliberately not implemented (their authorisation rules and state resolution v1 are not written). `TestSyncTimelineGap`: history paging is by stream position, not depth (T-3001d). `TestSync/sync_should_succeed_even_if_the_sync_token_points_to_a_redaction_of_an_unknown_event`: redaction application (T-2009). `TestSync/parallel/*state_after*`: MSC4222 `state_after` is not implemented (T-3001f). `TestSyncOmitsStateChangeOnFilteredEvents`: a sync filter's state section is not applied (T-3001f) |
| What the run caught that the local suite did not | `GET /rooms/{roomID}/state` had been changed in this window from a bare array to `{"state": […]}`, on a reading of the API-standards sentence that every endpoint answers with a JSON object. Complement's `TestRoomState` walks the top-level array, the endpoint's own schema says "Array of `ClientEvent`", and Synapse answers with an array. The change was reverted the same day; the local suite had been written around the wrong shape and could not see it |
| Server faults observed | 0 panics, 0 HTTP 500 |
| Complement assertions changed | none |

### Targeted run after the join-handshake and event-identifier work (2026-11-05)

| Field | Value |
| --- | --- |
| Suites run | `./tests/csapi` with `RUN='TestJson\|TestPowerLevels\|TestRedact\|TestRoomMessagesLazyLoading\|TestFetchMessagesFromNonExistentRoom\|TestArchivedRoomsHistory\|TestRoomCreate\|TestRoomMembers\|TestRoomState\|TestSync'` |
| Passed | the set above except the two failures below, and `TestSyncTimelineGap` — which had been failing for the whole of this project and now passes repeatedly, alone and inside the set |
| Failed and why | `TestRoomCreate/creates_a_room_with_the_given_version`: asked for room version **2**, refused with `M_UNSUPPORTED_ROOM_VERSION` — room versions 1 and 2 are deliberately unimplemented (T-4013). It is the only failure left in this set: `TestSync` passes, subtests included, and the device-list case turned out not to need EDUs over federation |
| What the run caught that the local suite did not | three things, all real. (1) `/make_join` read one `ver` parameter out of an unordered list and compared it with the room's version, so a server that lists the room's version among others was refused at random (DEC-037). (2) A PDU with no `event_id` was refused, although the event format of every room version from 3 on does not list that property and the identifier is the reference hash the receiver computes (DEC-036). (3) The federation key endpoints verified a peer's certificate against the resolved address instead of the server's name, so a peer with an ordinary certificate could not have its signing keys fetched at all; the failure surfaced as an authentication refusal (DEC-034, fixed earlier in this window) |
| Interim note on flakiness | before the `ver` fix, `TestSyncTimelineGap` passed or failed depending on the order of the harness's version list. That is why an earlier record in this file calls it a history-paging failure: the diagnosis was made against one draw of the lottery |
| Server faults observed | 0 panics, 0 HTTP 500 |
| Complement assertions changed | none |

## Complement run record

Full inventory, commands and the per-test list: `.agent-work/COMPLIANCE-MATRIX.md`.

| Field | Value |
| --- | --- |
| Complement checkout | `7b38a63c9db3337292bee5887fc8533be3a57b9f` (2026-11-03) |
| Image | `complement-lattice:latest`, built from `deploy/complement/Dockerfile` |
| Suites | `./tests` and `./tests/csapi` |
| Top-level results | 8 passed, 189 failed, 0 skipped |
| M1-target subset | `TestVersionStructure`, `TestUnknownEndpoints`, `TestRegistration`, `TestLogin`, `TestServerCapabilities`, `TestCanRegisterAdmin`, `TestMediaConfig` pass; `TestLogout` and `TestDeviceManagement` pass except the subtests that assert through `/sync` |
| Failure causes | 404 `M_UNRECOGNIZED` on unimplemented endpoints (309 responses), 501 on media upload (28), one 400 `M_INVALID_PARAM` refresh-token refusal |
| Server faults observed | 0 panics, 0 HTTP 500 responses |
| Complement assertions changed | none |

### Targeted run after the M2 rooms, membership and alias work (2026-11-05)

The full suite has not been re-run for this iteration; the run below is the rooms,
membership, alias and M1-regression subset, and it is reported as such.

| Field | Value |
| --- | --- |
| Suites run | `./tests`, `./tests/csapi` with `RUN='TestRoomAlias\|TestRoomDeleteAlias\|TestRoomCanonicalAlias\|TestCannotKick*\|TestNotPresentUserCannotBanOthers\|TestRoomCreate\|TestRoomMembers\|TestRoomState\|TestKnocking*\|TestCannotSendNonKnockViaSendKnock\|TestRoomHierarchy\|TestUnknownEndpoints\|TestVersionStructure\|TestServerCapabilities\|TestRegistration\|TestLogin\|TestCanRegisterAdmin\|TestMediaConfig'` |
| Passed | `TestRoomAlias` (all subtests), `TestCannotKickNonPresentUser`, and every M1 regression suite listed above |
| Failed and why | `TestRoomCreate`, `TestRoomMembers`, `TestRoomState`, `TestRoomDeleteAlias`, `TestRoomCanonicalAlias`, `TestNotPresentUserCannotBanOthers`, `TestCannotKickLeftUser`: every protocol step before the assertion succeeded, then failed at `GET /sync` (not implemented, T-3001). `TestKnocking`, `TestKnockingInMSC3787Room`: refuse a remote-user invitation with 501 (federation, M4). `TestCannotSendNonKnockViaSendKnock`, `TestRoomHierarchy`: version or endpoint gaps recorded in the tables above |
| Newly unblocked by this iteration | the alias family and the kick/ban family no longer fail on their own endpoints; they fail on `/sync` |
| Server faults observed | 0 panics, 0 HTTP 500 |
| Complement assertions changed | none |

## Known inconsistencies recorded honestly

1. `GET /versions` advertises v1.1…v1.19 while most endpoints are missing. Clients are not misled
   about individual endpoints (they answer `M_UNRECOGNIZED`), but the range overstates coverage; a
   narrowing decision is tracked as an open item.
2. ~~`capabilities.m.room_versions` advertises a version while no room can be created yet.~~ **Resolved:** `/capabilities` now reads `roomversion.Default()` and `roomversion.Available()`, and a test creates a room in every advertised version, so the advertisement cannot outrun the implementation. Original note: the registry exposes the honest set (`Supported`/`Available` return v10 and v11 only); wiring `/capabilities` to it happens together with `createRoom`, because advertising versions without the endpoints that use them would be equally misleading.
3. `TestFilter` passing is incidental; the filter endpoint is not implemented.
4. `TestLogout`/`TestDeviceManagement` subtests fail only because the assertion vehicle (`/sync`)
   does not exist; the logout/device behaviour itself is covered by this project's integration tests.
