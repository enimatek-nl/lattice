# Compliance Matrix

Last updated: 2026-11-07. (Rows below the first two tables are dated where they were written; a
row that contradicts a later dated section is stale, and the dated sections and the run lists under
`.agent-work/` are the authority. A 2026-11-07 pass over this file corrected the rows that had drifted
— media, sync, device keys, push rules, room version 1/2 authorisation, server ACLs, federation
invites — because a stale "not implemented" line is as misleading as a stale "done" one.)

This matrix distinguishes **implemented**, **tested by this project**, **verified against Complement**,
**partial**, **not implemented** and **not applicable**. An endpoint that merely answers is not
"compliant"; the columns exist so those states cannot be confused.

Specification: Matrix v1.19 (`docs/specification-version.md`).

## Client-Server API

| Area | Endpoint / behaviour | Status | Own tests | Complement |
| --- | --- | --- | --- | --- |
| Versioning | `GET /_matrix/client/versions`; the legacy `r0` prefix registered for the whole client API alongside `v3`, including the endpoints that carry no token (T-4073) | implemented | `tests/integration/http_test.go`, `tests/integration/legacyprefix_test.go` | **verified** (`TestVersionStructure`, `TestUnknownEndpoints`) |
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
| Session | `GET /account/whoami` (names the user and the device the token belongs to — keys, push rules and the device list are per-device things) | implemented | yes | **verified** (inside `TestLogin`) |
| Errors | 404 `M_UNRECOGNIZED` for unknown paths, 405 `M_UNRECOGNIZED` + `Allow` for wrong methods, across client/federation/key/media prefixes | implemented | yes | **verified** (`TestUnknownEndpoints`, all subtests) |
| Discovery | `/.well-known/matrix/client`, `/.well-known/matrix/server` | implemented | yes | n/a |
| Media | `GET /v3/config` (upload size) | implemented | yes | **verified** (`TestMediaConfig`) |
| Media | upload (sync and async `create`/`commit`), download including the authenticated `/client/v1/media/...` forms, thumbnail, `config`, remote content fetched once from the origin and served from the copy held here | implemented | `tests/integration/media_test.go`, `tests/unit/media_multipart_test.go` | **verified**: `TestMediaConfig`, `TestContentMediaV1`, `TestContentCSAPIMediaV1`, `TestMediaFilenames`, `TestMediaWithoutFileName`, `TestAsyncUpload`, `TestFederationThumbnail`, `TestLocalPngThumbnail`, `TestRemotePngThumbnail`. `TestRoomImageRoundtrip` and `TestUrlPreview` stay red: previews and the media report endpoint are not implemented |
| Filters | `POST`/`GET`/`DELETE /user/{id}/filter[/{filterID}]`: filters are stored, validated and read back, and a sync and a `/messages` page consult them. MSC3874's relation lists (`org.matrix.msc3874.rel_types`, `not_rel_types`) narrow both doors, read from the event's own `content.m.relates_to.rel_type` claim rather than from the relation index | partial — the sections and limits a filter names are not all applied (T-4020) | `tests/integration/sync_test.go`, `tests/integration/messagesfilter_test.go`, `tests/integration/relationfilter_test.go` | **verified**: `TestFilterMessagesByRelType` (all six of its filter documents, run 20261108y-relfilter, T-4088), `TestSyncFilter`. `TestFilter` and `TestSyncOmitsStateChangeOnFilteredEvents` are red (T-4020) |
| Rooms | `POST /v3/createRoom`: presets, `room_version` negotiation (`M_UNSUPPORTED_ROOM_VERSION`), `creation_content` limits, initial state, name/topic/avatar shorthand | implemented | yes | not verified (Complement room tests also need membership endpoints) |
| Events | `PUT /v3/rooms/{roomID}/send/{eventType}/{txnID}`: authorisation, hashing, signing, storage, per-device transaction idempotency; event types outside the reserved `m.` namespace need no dot, since the dotted namespace is a convention and refusing an unqualified type hides the endpoints that would carry it (T-4069) | implemented | `tests/integration/customeventtype_test.go`, `tests/unit/roomslogic_test.go` | not verified by name; the undotted types Complement sends in its filter tests were refused here until T-4069 |
| Events | `PUT /send/m.room.member/...` refused with 400 `M_UNRECOGNIZED` (membership has its own endpoints) | implemented | yes | not verified |
| State | `PUT /v3/rooms/{roomID}/state/{eventType}[/{stateKey}]`, including the empty state key and rejection of deeper paths | implemented | yes | not verified |
| Rooms | join / invite / knock / leave, `GET /state`, `/members`, `/messages`, `/event/{eventID}`, redaction (`PUT /redact/{eventID}/{txn}`), aliases, directory, upgrades | implemented | `tests/integration/redaction_test.go`, `tests/integration/history_test.go`, Complement `TestRedact`/`TestPowerLevels` | still missing: `GET /rooms/{roomID}/context/{eventID}` (T-2015), `GET /rooms/{roomID}/event_auth/{eventID}` (T-2016), `GET /_matrix/federation/v1/state_ids` |
| Sync / messages | `GET /sync` (incremental, `since`, long-poll, joined/invited/left sections, `device_lists`, account data in both scopes, limited timelines, `prev_batch`, MSC4222 `state_after` with `lazy_load_members`), `GET /rooms/{id}/messages` | implemented — MSC4222's `GET /rooms/{id}/members` stream is not (T-4068) | `tests/integration/sync_test.go`, `tests/integration/history_test.go`, `tests/integration/statresolution_test.go`, `tests/integration/stateafter_test.go` | **verified**: `TestSync` (`tests/csapi`), `TestSync` (`tests/msc4222`, both subtests — a different test with the same name; only the qualified results table tells them apart, T-4066), `TestSyncLeaveSection`, `TestGappedSyncLeaveSection`, `TestSyncTimelineGap`, `TestCumulativeJoinLeaveJoinSync`, `TestOlderLeftRoomsNotInLeaveSection`, `TestRoomMessagesLazyLoading`, `TestRoomMessagesLazyLoadingLocalUser`, `TestSyncOmitsStateChangeOnFilteredEvents` (T-4070: a state event a filtered batch hides must still be in the state section). Still red through sync: the presence section (T-3001e), the filter fields that narrow by sender, URL and event fields rather than type (T-4020), and the two-server cases that need the handshake pieces below |
| Relations, threads | `GET /rooms/{roomID}/relations/{eventID}[/{relType}[/{eventType}]]` and `GET /rooms/{roomID}/threads`, both under `v3` and `v1`: what relates to an event, filtered by relation and event type, walked in either direction with tokens; and the room's conversations, ordered by each thread's newest event. The relation claim stays in the event's content — `event_relations` is an index over it, written where the event is stored and dropped where a redaction takes the content | implemented | yes | **verified** (`TestRelations`, `TestRelationsPagination`, `TestRelationsPaginationSync`, `TestThreadsEndpoint`, run 20261107r-threads) |
| Pushers | `POST /pushers/set`, `GET /pushers`: a device asking to be woken at a URL, kept as one row per (pushkey, app_id) with the hash of the session that wrote it. A password change drops the pushers of the sessions it ended and keeps the one that proved the new password; logging a session out drops its own, logging out all drops every one. Delivering to them (the push gateway, `GET /notifications`, evaluating rules into notifications) is not implemented (T-3005a) | implemented (storage and the endpoints) | `tests/integration/pushers_test.go` | **verified**: `TestChangePasswordPushers` (both subtests, run 20261108z-pushers) |
| Leaving and forgetting | `POST /rooms/{roomID}/forget`: the room stops being shown to the user who forgot it, and stops being readable to them, until a join or an invite again clears it. The marker is per user and per room, and is judged per sync window, so a sync asked from before the leaving still answers the leaving. A member who has left is owed the state and roster **as of** their leaving, read from the state group the leaving event was persisted with | implemented | `tests/integration/forget_test.go`, `tests/integration/aliases_test.go` | **verified**: `TestRoomForget` (all six subtests), `TestLeftRoomFixture` (all five subtests), run 20261108u-forget |
| E2EE device keys | `POST /keys/upload`, `POST /keys/query` (asking a peer for a user who lives elsewhere), `POST /keys/claim`, `GET /keys/changes` (the same addressed rows the sync `device_lists` section reads, asked as a token range), federation `user/keys/query` | implemented — cross-signing and key backup are not (T-3008e) | `tests/integration/keys_test.go`, `tests/integration/federationdevicekeys_test.go`, `tests/integration/keyschanges_test.go` | **verified**: `TestUploadKey`, `TestUploadKeyIdempotency`, `TestUploadKeyIdempotencyOverlap`, `TestKeyClaimOrdering`, `TestKeysQueryWithDeviceIDAsObjectFails`, `TestKeyChangesLocal` (T-4072). Red: `TestFederationKeyUploadQuery` (T-4008's device-list EDUs), `TestE2EKeyBackupReplaceRoomKeyRules` (key backup, T-3008e) |
| Account | whoami, devices, sessions, profile; `account_data` in both scopes (user, and user-per-room) with write, read and MSC3391 removal reaching `/sync` | partial — no `POST /account/password`, no `POST /account/deactivate` | `tests/integration/auth_test.go`, `tests/integration/direct_test.go`, `tests/integration/pushrules_test.go`, `tests/integration/accountdata_test.go` | **verified**: `TestAddAccountData` (both subtests), `TestRemovingAccountData` (all four subtests — DELETE and empty PUT, user and room scope) from the targeted run `.agent-work/next/complement-run-acctdata4.log`, not from a full suite run. Still red: `TestChangePassword`, `TestDeactivateAccount` (no `/account/password`, no `/account/deactivate`) |
| Presence | `GET`/`PUT /presence/{userID}/status` for one user, and the `presence` section of `/sync` for users who share a room | partial — there is no idle timeout, so a user who stops asking stays online (T-3001e) | `tests/integration/presence_test.go` | `TestPresence` and `TestPresenceSyncDifferentRooms` pass; `TestRemotePresence` is red (no `m.presence` EDU, T-4008) |
| Directory, aliases, spaces | `/publicRooms` (both listeners), `/directory/room/{alias}`, `/rooms/{id}/aliases`, `/hierarchy` (both directions), `/room_summary/{id}` | implemented | `tests/integration/aliases_test.go`, `tests/integration/hierarchy_test.go`, `tests/integration/directory_test.go` | **verified**: `TestRoomAlias`, `TestRoomCanonicalAlias`, `TestRoomDeleteAlias`, `TestPublicRooms`, `TestRoomSummary`, `TestClientSpacesSummaryJoinRules`, `TestFederatedClientSpaces`. Red: `TestRemoteAliasRequestsUnderstandUnicode` (no federation `/query/directory`), the user directory (`TestRoomSpecificUsername*`, T-4058), and `/search`, which is not implemented at all (`/relations` and `/threads` are — see the row above; `TestRelations*` and `TestThreadsEndpoint` went green in run 20261107r-threads) |
| Refresh tokens | `refresh_token` | not implemented | refusal tested | `TestTxnIdWithRefreshToken` fails on our 400 `M_INVALID_PARAM` — an explicit refusal, not a silent ignore |
| Push rules | the whole read/write `/pushrules` API, the default rule set, delivery to the client through `account_data` in `/sync`, and an evaluator for every condition kind in the specification | partial — the evaluator exists but nothing evaluates rules as events are persisted, so the unread and highlight counts a client is told are zero (T-3005a) | `tests/unit/pushrules_test.go`, `tests/integration/pushrules_test.go` | **verified**: `TestPushRuleCacheHealth`, `TestPushSync`, `TestPushRuleRoomUpgrade` (flaky: red in some runs). Red: `TestPollsLocalPushRules`, and pushers, the push gateway and `GET /notifications` are absent |

## Server-Server and key API

| Area | Status | Own tests | Complement |
| --- | --- | --- | --- |
| `GET /_matrix/federation/v1/version` | implemented | yes | **verified** (the 405 half of `TestUnknownEndpoints`) |
| `GET /_matrix/key/v2/server` (signed document, served as the canonical bytes its signature covers) | implemented | yes (`TestServerKeysVerifies` checks the body is canonical and verifies the signature over it) | **verified** (`TestInboundFederationKeys`; it failed while the endpoint re-encoded the document) |
| `POST /_matrix/key/v2/query` (answers for this server only) | partial — *asking* a notary is implemented and tested; answering about other servers is not | yes | not directly exercised |
| Server discovery: `.well-known/matrix/server` delegation, `_matrix._tcp`/`_matrix-federation._tcp` SRV records, IP literals, explicit ports, certificate-name rules, caching, refusal of a broken delegation | implemented | yes (`tests/unit/federationdiscovery_test.go`) | not verified: Complement gives every container a fixed `SERVER_NAME` and no DNS |
| Address policy for dialed addresses (loopback, link-local metadata, CGNAT, multicast, private ranges denied by default; configurable allow/deny) | implemented | yes (`tests/unit/federationdiscovery_test.go`, `tests/unit/config_test.go`) | n/a. Known limit: enforced at resolution, not at dial time |
| Remote signing keys: fetch, self-signature requirement, one-week trust clamp, retired keys until `expired_ts`, expiry enforced per room version, notary fallback, cache reuse | implemented | yes (`tests/unit/federationkeys_test.go`) | not verified |
| `POST /_matrix/federation/v1/user/keys/query` (answering for this server's own users) and the outbound half of `POST /_matrix/client/v3/keys/query` for users who live elsewhere | implemented: the answer carries only the users that were asked about and only those in the answering server's namespace, key documents are passed through byte-for-byte because the device signed them, and a peer that does not answer leaves that user's entry empty instead of failing the query. Cross-signing (`master_keys`, `self_signing_keys`) is not stored here, so it is not reported | yes (`tests/integration/federationdevicekeys_test.go`) | **verified** as part of `TestDeviceListUpdates`, which queries a remote user's keys after being told they changed |
| Device-list changes over federation (`m.device_list_update` EDUs out when a local user's devices change or that user joins a room elsewhere, in when a peer sends them) | implemented: the announcement goes to the servers that hold a joined room with the user, a peer is only believed about users in its own name, and what arrives is passed to the local users who share a room with that user. A deletion announcement (`deleted: true`) and cross-signing key changes are not announced yet (T-4023) | yes (`tests/integration/federationdevicelists_test.go`, `tests/unit/devicestreams_test.go`) | **verified for nine of the ten `TestDeviceListUpdates` subtests**, plus `TestDeviceListsUpdateOverFederationOnRoomJoin` and `TestUserAppearsInChangedDeviceListOnJoinOverFederation`. The tenth, *when joining a room with a remote user*, fails on a clean re-run (2026-11-07, twice in a row, and it is in the project's own failures list): when this server's user joins a room that lives elsewhere, nobody announces the other members' device lists to the joining server (T-4064) |
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
| Invites over federation: `PUT /_matrix/federation/v1|v2/invite/{roomID}/{eventID}` answered for this server's own users (the event is checked against what the URL claims, countersigned here, stored as an invite), and asked of a peer when a local user invites somebody who lives elsewhere | implemented | `tests/integration/membership_test.go`, `tests/integration/remotemembership_test.go` | **verified** in `TestFederationRoomsInvite` (reject, rescind, metadata, rejoin subtests) and `TestUnbanViaInvite`; `TestFederationRoomsInvite` as a whole is still red for one subtest — see T-4063 |
| Leaves and knocks over federation (`/make_leave`, `/send_leave`, `/make_knock`, `/send_knock`), answering `/backfill`, `/hooda`, third-party invites, federation `/profile`, federation `/query/directory` for remote aliases | not implemented | — | recorded as open tasks (T-4009c, T-4009b2, T-4058) rather than a silent gap; a local user who wants to leave a room held elsewhere writes their own leave and fans it out, which is why the leave endpoint tests pass for local rooms |
| A kick, ban or unban of a user who lives on another homeserver: this server holds the room, so the member event is written and signed here under the room's own rules, and fan-out takes it to the target's server as well as to the servers holding a joined member | implemented+tested | `tests/integration/remotemembership_test.go` | **verified** by `TestFederationRoomsInvite/Inviter user can rescind invite over federation` and `TestUnbanViaInvite`, both red before this work; one Complement case disagrees — see T-4063 |
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
| Room version registry v1-v12 (state resolution, event ID format, auth rule set, redaction profile, join rules, integer power levels, canonical-JSON strictness, creator placement, redacts placement, aliases as special-case state, domain-free room IDs) | implemented | `tests/unit/roomversion_test.go` |
| Room versions 1 and 2 as their own pages define them: the authorisation table those two versions share, State Resolution 1, identifiers the origin server chooses, references as `[event_id, {"sha256": hash}]` pairs, and the pre-canonical-JSON power level forms | implemented+tested | `tests/unit/authorisation_legacy_test.go`, `tests/unit/stateresolvev1_test.go`, `tests/unit/powerlevels_legacy_test.go`, `tests/unit/eventid_test.go`, `tests/unit/eventsignature_test.go`, `tests/integration/roomversionlegacy_test.go` (a created room, its events named `$opaque:hs1`, and every fetched reference checked to carry the referenced event's real reference hash) |
| Room version 12 and the unstable versions | recognised and refused rather than invented: their rules are registered so a request is answered honestly, but the revised state resolution and the pseudo-identifier event format are not written | `tests/unit/roomversion_test.go`, `tests/integration/rooms_test.go` |
| Room version grammar validation | implemented | `tests/unit/roomversion_test.go` |
| Only supported versions offered/advertised (`roomversion.Supported`/`Available`) | implemented and now wired into `/capabilities`, so the advertised set and the set rooms can be created in are one table | `tests/integration/rooms_test.go` |
| Redaction algorithm, per room version | implemented | `tests/unit/redaction_test.go` |
| Content hash, reference hash, event ID derivation | implemented | `tests/unit/redaction_test.go`, `tests/unit/eventseal_test.go` |
| Hashing + signing an outgoing event (signature over the redacted form) | implemented | `tests/unit/eventseal_test.go` |
| Cross-implementation agreement on redaction and event IDs | verified against gomatrixserverlib v0.0.0-20260716140101-4fe595dc7f58 by golden test | `TestRedactionAndEventIDsAgainstReferenceImplementation` |
| Event authorisation rules (rules 1-10) for room versions 3-11, with version-gated join rules and power-level validation | implemented | `tests/unit/eventauth_test.go` |
| Auth events selection and its validation (duplicates, foreign rooms, rejected auth events) | implemented | `tests/unit/eventauth_test.go` |
| Restricted-room allow list evaluation | partial: enforced for locally created joins only | `tests/unit/eventauth_test.go` |
| Event authorisation for room versions 1 and 2 | implemented as the table those two versions share (`eventauth.RulesV1V2`), not the later versions' rules applied early | `tests/unit/authorisation_legacy_test.go` |
| State Resolution 2 (room versions 2-11): auth difference, power events, reverse topological power ordering, mainline ordering, iterative auth checks — and the live path consults it: each event's state snapshot is recorded (`state_groups`), and a room with more than one forward extremity has its current state written from the resolved answer, not from the order events arrived | implemented+tested | `tests/unit/stateresolve_test.go` (the algorithm, both argument orders, and the timestamp ordering over JSON-decoded events), `tests/integration/statresolution_test.go` (the same fork handed to two homeservers in opposite orders; both agree, both keep the losing branch, and the answer matches the algorithm run standalone) |
| State resolution for room version 1 | implemented as the algorithm the pinned version 1 page describes; see the divergence note below about Synapse's richer procedure | `tests/unit/stateresolvev1_test.go`, `tests/integration/roomversionlegacy_test.go` |
| State resolution for room version 12 (v2.1: conflicted subgraph, empty start set) | not implemented (refused with `ErrAlgorithmUnsupported`); version 12 is recognised and refused rather than invented | `tests/unit/stateresolve_test.go`, T-4019 |
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
| `m.room.server_acl`: a room excluding a homeserver, applied to the join template, the room's state, its authorisation chain, a single event, any PDU it pushes, and the fan-out that would relay events to it | implemented+tested | `tests/integration/serveracl_test.go`, `tests/unit/serveracl_test.go`. Still red: `TestACLsForEDUs` and the ACL check on the endpoints that do not exist yet (T-4012) |
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

**Where the evidence is.** Every path below under `.agent-work/` — the run logs, the pass and
failure lists, the package-qualified result tables — is a file in the working directory of
whoever ran the suite, and that directory is deliberately not committed (see `.gitignore`). A
clone of this repository will not contain them. Nothing here should be read as citable from the
repository alone: the run log and the two lists behind a tally are held locally and can be
produced by re-running the command named with that run, or asked for from whoever ran it. A
tally in this document without its log and its lists beside it is a claim, not evidence, and the
same rule about that applies to whoever reads it.

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
5. Room version 1 state resolution follows the algorithm the pinned specification page for that
   version describes. Synapse resolves those rooms with a richer procedure inherited from an older
   and fuller description of state resolution 1 (auth difference, power events, a reverse
   topological sort). The two agree on a room that has not forked, which is nearly all of them, and
   can disagree about which of two conflicting power levels events stands on a forked version 1
   room. This server follows the specification page rather than either implementation, because the
   pages are what the project's precedence rules make normative; the divergence is recorded here so
   that a version 1 room that disagrees between servers is investigated rather than assumed away.
   Interoperability against Synapse on such a room is not tested.

## Targeted run 20261108aa-todayset — every endpoint added today, in one clean run

`RUN='TestRoomForget|TestLeftRoomFixture|TestRelations|TestRelationsPagination|
TestRelationsPaginationSync|TestThreadsEndpoint|TestFilterMessagesByRelType|
TestChangePasswordPushers|TestChangePassword$|TestLogout|TestRoomReceipts|TestRoomReadMarkers|^TestSync$'`,
PARALLEL=1, against the tree at `5e20fe5`. Log
`.agent-work/next/complement-run-20261108aa-todayset.log`, lists
`.agent-work/complement-passes-20261108aa-todayset.txt` (14 names) and
`-failures-20261108aa-todayset.txt` (empty).

    == tally: 14 passing, 0 failing, 0 skipped (top-level tests)
    == no federation fingerprints in this run: no certificate naming another server,
       no refused handshake, no 401 on a federation template.

| passing | what it pins |
|---|---|
| `TestRelations`, `TestRelationsPagination`, `TestRelationsPaginationSync` | what relates to an event, paged both ways, obeying who may read the room |
| `TestThreadsEndpoint` | the room's conversations, ordered by each thread's newest event, in the shape clients read |
| `TestRoomForget` (6 subtests), `TestLeftRoomFixture` (5 subtests) | forgetting, and the state a departed member is owed as of their leaving |
| `TestFilterMessagesByRelType` | MSC3874's `rel_types` / `not_rel_types` on `/messages` |
| `TestChangePasswordPushers` (2 subtests), `TestChangePassword` (5), `TestLogout` | pushers belonging to the session that wrote them, and a password change taking the dead sessions' with it |
| `TestRoomReceipts`, `TestRoomReadMarkers`, `TestSync` | receipts and read markers, and the sync that carries them |

Every one of these was red or unattempted when this stretch of work began. They are gathered into
one run because the interesting question is not only whether each new thing works but whether the
new things and the older neighbours of theirs still work together — sync, receipts, logout,
password changes and reads by departed members are all neighbours of the forget and thread work.

The run still printed the stale-DNS warning before and after it (the aardvark-dns record named
below is untouched), and it still reported no fingerprints: these tests speak to one server, and
the fault that has been reddening the two-server tests all day cannot reach them.

## Targeted run 20261108z-pushers — pushers, and what takes them back

`RUN='TestChangePasswordPushers|TestChangePassword$|TestLogout|TestLoginLogout|TestDeactivateAccount|TestPushRules?'`,
PARALLEL=1, against the tree that became `5e20fe5`. Log
`.agent-work/next/complement-run-20261108z-pushers.log`, lists
`.agent-work/complement-passes-20261108z-pushers.txt` / `-failures-…`.

    == tally: 5 passing, 1 failing, 0 skipped
    --- PASS: TestChangePasswordPushers (1.94s)
        --- PASS: …/Pushers created with a different access token are deleted on password change
        --- PASS: …/Pushers created with the same access token are not deleted on password change
    --- PASS: TestChangePassword (0.72s)      — all five subtests
    --- PASS: TestDeactivateAccount          — password flow, wrong password, deactivation
    --- PASS: TestLogout
    --- PASS: TestPushRuleCacheHealth

The one failure is `TestPushRuleRoomUpgrade`, and this run still carries the container network's
fingerprints — 4 certificates naming another server and 4 refused handshakes — so it is the same
environment fault as `20261108x-pushupcheck`, not a consequence of the pusher work: its two local
subtests pass and its two remote-join subtests fail on `certificate is valid for hs2, localhost,
not hs1`. Nothing that was green before this change went red here (`TestChangePassword`,
`TestDeactivateAccount`, `TestLogout` were already green and stay green).

`TestChangePasswordPushers` had been red because there was nothing to be red about: `/pushers/set`
and `/pushers` did not exist. Implemented as one row per (user, pushkey, app_id) carrying the hash
of the token that wrote it, with a password change dropping the pushers of the sessions it ended.
`tests/integration/pushers_test.go` asks what the Complement test does not: that an email pusher
is refused `M_INVALID_EMAIL_PARAM` rather than stored, that an http pusher whose `data.url` is not
https is refused (a notification carries the event's content, so plain http would put it on the
open network), that `kind: null` removes rather than adds, and that replacing a pusher from a
second session hands its ownership to that session — which is why a later password change keeps
that one and drops the pusher nobody re-wrote.

## Full run 20261108w — the tally moved, and the container network moved with it

PARALLEL=2, filter `.*`, at tree `4dc3628`. Log
`.agent-work/next/complement-run-20261108w.log`, table
`.agent-work/next/complement-results-20261108w.tsv`, lists
`.agent-work/complement-passes-20261108w.txt` / `-failures-20261108w.txt`.

    == tally: 131 passing, 88 failing, 1 skipped (top-level tests)
    == federation fingerprints in this run, counted from its own log:
          11  a certificate naming another server (certificate is valid for X, not Y)
          12  a TLS handshake refusing what a peer presented (tls: bad certificate)
           0  a peer refusing a request as unsigned (not signed by a server this server trusts)
           0  a federation template answered 401, which a room may legitimately do

**This run is contaminated, and the number is quoted with that said rather than smoothed over.**
Against the last clean run (20261107s, 130/89/1, fingerprints 0/0/0) the lists differ by three
names, no more:

| change | name | reading |
|---|---|---|
| green now, red then | `TestRoomForget`, `TestLeftRoomFixture` | the forget work (T-4077); both were also green alone at the same tree in 20261108u-forget, so this half is real |
| red now, green then | `TestPushRuleRoomUpgrade` | not attributed to this server — see below |

129 tests are green in both runs, and 87 are red in both.

`TestPushRuleRoomUpgrade` deploys two servers, and its remote halves fail in this run with the
network's own signature, from the run's log:

    federation: a server could not carry a join  destination=hs1
      error=Get "https://10.89.0.2:8448/_matrix/federation/v1/make_join/…":
            tls: failed to verify certificate: x509: certificate is valid for hs2, localhost, not hs1

This server asked where `hs1` was and was taken to an address holding **hs2's** certificate. The
subtests that need no peer — `upgrading_a_room_carries_over_existing_push_rules_for_local_users`
and its `manually_upgrading…` twin — pass in the same run.

## Targeted run 20261108x-pushupcheck — the flipped test, alone, still contaminated

`RUN='TestPushRuleRoomUpgrade'`, PARALLEL=1, at `4dc3628`, run right after the full one:

    == tally: 0 passing, 1 failing, 0 skipped
           4  a certificate naming another server   4  a TLS handshake refusing what a peer presented

    --- FAIL: TestPushRuleRoomUpgrade/parallel/joining_a_remote_upgraded_room_carries_over_existing_push_rules
    --- FAIL: TestPushRuleRoomUpgrade/parallel/joining_a_remote_manually_upgraded_room_carries_over_existing_push_rules
    --- PASS: TestPushRuleRoomUpgrade/parallel/manually_upgrading_a_room_carries_over_existing_push_rules_for_local_users
    --- PASS: TestPushRuleRoomUpgrade/parallel/upgrading_a_room_carries_over_existing_push_rules_for_local_users

Running it alone did not clear the contamination, which settles two things at once: this test's
status says nothing about this server until the stale network records are gone, and T-4071's
"flaky in both directions" description was this environment all along. The stale record is
`complement_msc3902_1_servers` in `${XDG_RUNTIME_DIR}/containers/networks/aardvark-dns` — checked
after the run, still present, its network already deleted. Clearing it is an operator action (the
two-command loop is in `scripts/complement.sh` next to the preflight warning); it was not run here
because that directory also holds other projects' containers.

## Targeted run 20261108y-relfilter — MSC3874 relation filters

`RUN='TestFilterMessagesByRelType'`, PARALLEL=1, against the tree that became `8bd7bc7`. Log
`.agent-work/next/complement-run-20261108y-relfilter.log`.

    == tally: 1 passing, 0 failing, 0 skipped
    tests/msc3874 | --- PASS: TestFilterMessagesByRelType (1.67s)

All six filter documents in it pass: `rel_types` with one type, with another, with both; and
`not_rel_types` with one, with two. Our own test `TestEventsCanBeFilteredByWhatTheyRelateTo` in
`tests/integration/relationfilter_test.go` adds the cases the Complement test does not ask:
`not_rel_types` keeping an event that has no relation at all, an empty list meaning nothing
rather than everything, an unknown relation type meaning nothing, a bad shape answered
`M_INVALID_PARAM`, and the same document obeyed on the sync door as on `/messages`.

## Targeted run 20261108u-forget — forgetting a room, and the state owed to a member who left

`RUN='TestRoomForget|TestLeftRoomFixture|TestRoomLeave|TestPeekingViaPublicRoomsAndThenJoin'`,
PARALLEL=1, at tree `4dc3628`. Log `.agent-work/next/complement-run-20261108u-forget.log`,
table `.agent-work/next/complement-results-20261108u-forget.tsv`,
lists `.agent-work/complement-passes-20261108u-forget.txt` / `-failures-…` (the failure list is empty).

    == tally: 2 passing, 0 failing, 0 skipped (top-level tests)

Every subtest of both, listed, because both were unattempted before and each subtest pins a
separate half of the behaviour:

| subtest | now |
|---|---|
| `TestRoomForget/Parallel/Forgotten room messages cannot be paginated` | PASS |
| `TestRoomForget/Parallel/Can forget room you've been kicked from` | PASS |
| `TestRoomForget/Parallel/Forgetting room does not show up in v2 initial /sync` | PASS |
| `TestRoomForget/Parallel/Can re-join room if re-invited` | PASS |
| `TestRoomForget/Parallel/Leave for forgotten room shows up in v2 incremental /sync` | PASS |
| `TestLeftRoomFixture/Can get rooms/{roomId}/state for a departed room` | PASS |
| `TestLeftRoomFixture/Can get rooms/{roomId}/members for a departed room` | PASS |
| `TestLeftRoomFixture/Can get rooms/{roomId}/messages for a departed room` | PASS |
| `TestLeftRoomFixture/Can get 'm.room.name' state for a departed room` | PASS |
| `TestLeftRoomFixture/Getting messages going forward is limited for a departed room` | PASS |

`TestRoomLeave` and `TestPeekingViaPublicRoomsAndThenJoin` matched no test in this Complement
checkout, so the tally counts two tests, not four — said rather than quietly dropped.

The four things this needed, each with the test that kept me honest:

* **A `/forget` marker of our own** (`persist.ForgetRepo`, table `room_forgotten`). Forgetting is
  said by the user, not done to them: a room the user has left, been kicked from, or has never
  joined can be forgotten (200 `{}`), while a room they are still in is refused with 400
  `M_UNKNOWN` (`rooms.ErrStillInRoom` says why in its own words).
* **Sync must keep answering later** — `TestRoomForget/…/Leave for forgotten room shows up in v2
  incremental /sync` asks for a sync from *before* the leaving, and the leaving has to come back.
  So the marker is not absolute: the sync service drops a forgotten room **unless** the requested
  window still holds that membership event. My first attempt hid the join as well, and that test
  caught it.
* **A forgotten room must refuse to be read** — `/messages` answers 403 `M_FORBIDDEN` for the
  forgetter while the other member still reads it.
* **Re-joining has to clear the marker**, done at the membership write (a join or an invite) rather
  than at the sync read, because `/messages` does not go through sync.

`TestLeftRoomFixture` never needed `/forget` at all. What it asked for is that a member who has
left is still owed the state of the room **as it was when they left**: `/state` and
`/state/m.room.name` and the roster answered what they knew at that moment, and the history
stays readable forwards with a bounded window. We were answering with today's state, which the
reader was never told and was never allowed to see.

    internal/rooms/reads.go   stateFor(ctx, roomID, requester) []persist.StateGroupEntry
    internal/persist          state_groups, state_group_entries, event_state_groups
                              (GroupOf, Entries, StateEntryAt)

`stateFor` resolves the member's read horizon to the event they left at, asks which state group
that event was persisted with, and returns that group's entries. Only a room never seen that way
falls back to cutting today's state, and the fallback says so in the code rather than pretending.

**One thing to flag: our own tests had pinned the weaker answer.**
`TestFormerMemberReadsStopAtLeaving` in `tests/integration/aliases_test.go` asserted that state
written *after* the reader left is hidden as a **404**, and that the departed member's roster
contains no trace of a member who joined later. Both assertions described the behaviour we had,
not the behaviour the room owes. They are corrected — the value as of the leaving comes back, and
a later member may appear only as an invite the reader knew about, never as a join — and the
reason is written next to them. The Complement assertions were not touched.

## Latest full run (2026-11-07s — the cleanest on record, and the relations work measured in it)

`RUN='.*' COMPLEMENT_LABEL=20261107s COMPLEMENT_TEST_TIMEOUT=50m PARALLEL=2 scripts/complement.sh -v`
at tree `2c31072`: **130 passing, 89 failing, 1 skipped** out of 220 top-level tests.

| Field | Value |
| --- | --- |
| Federation fingerprints | **zero of every kind** — no wrong-name certificate, no refused TLS handshake, no peer refusing a request as unsigned, no federation template answered 401, no unbuilt deployment image. This is the first full run that reports that about itself; 20261107h counted 0/1/0 and said nothing, 20261107m counted 8/6/7, 20261107o counted 4/3/4 |
| Log readability | every one of the 20,567 log lines was one JSON event; nothing was dropped, so the tally needs no caveat |
| Newly passing against 20261107h | `TestChangePassword`, `TestDeactivateAccount`, `TestRoomReceipts`, `TestRoomReadMarkers` (the account and receipt work), `TestRelations`, `TestRelationsPagination`, `TestRelationsPaginationSync`, `TestThreadsEndpoint` (T-4086, four tests never attempted before), `TestPushRuleRoomUpgrade` (the T-4071 flake, green again) |
| Newly failing against 20261107h | none |
| Newly failing against 20261107o | none — the four two-server tests that went red there (`TestDeviceListsUpdateOverFederationOnRoomJoin`, `TestFederatedClientSpaces`, `TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11`, `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room`) are green here |
| What that says about 20261107o | those four are flaky, not broken: they were green in 20261107h, red in 20261107o with contamination beside them, green alone in 20261107p except `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room`, and green again here. That last one is red in isolation and green in two parallel runs, which makes it the flakiest thing in the suite rather than a confirmed server fault — the read of T-4071 it seemed to prove is not established |
| The 98 remaining failures | unchanged in character: threads and pushers, search and the user directory, `/forget`, MSC3874 relation-type filters, typing and to-device EDUs, backfill gap filling, DNS-rebinding dial policy, room version 12, and T-4071 |
| Complement assertions changed | none |

Lists: `.agent-work/complement-{passes,failures}-20261107s.txt`; table
`.agent-work/next/complement-results-20261107s.tsv`; log `.agent-work/next/complement-run-20261107s.log`.

## Full run 2026-11-07o — the tally was lost and recovered, and the runner was fixed for it

`RUN='.*' COMPLEMENT_LABEL=20261107o COMPLEMENT_TEST_TIMEOUT=50m PARALLEL=2 scripts/complement.sh -v`
at tree `c2e0015`. The run finished — 104,803 lines of log, every package reported — and the script
printed **no tally at all**. `go test -json` had glued two events onto one line at offset 24232, the
`jq` reading the log stopped there, and with `set -euo pipefail` that took the rest of the script
down with it. This is worth recording as a runner fault rather than a result: a suite that ran
completely and reported nothing looks, from the outside, exactly like a suite that was never run.

Re-deriving the results from the same log, after teaching the runner to split glued lines and to
skip anything still unparseable instead of stopping: **123 passing, 98 failing, 1 skipped** (222
qualified results; 2 of 104,803 log lines could not be read as events and were dropped). Against
20261107h: `TestChangePassword`, `TestDeactivateAccount`, `TestRoomReadMarkers`, `TestRoomReceipts`
newly green — the two features those runs were about — and four two-server tests newly red.

| Newly red | What the log says | Isolated (run 20261107p-relationscheck, `PARALLEL=1`) |
| --- | --- | --- |
| `TestDeviceListsUpdateOverFederationOnRoomJoin` | `Deploy: No images have been built for blueprint 1_servers` — the test never reached a homeserver | passes |
| `TestFederatedClientSpaces` | the room's own two servers; the peer's name resolved into another deployment | passes |
| `TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11` | `Get "https://10.89.2.13…"` refused and the join answered 500 (`M_UNKNOWN`, still T-4071) | passes |
| `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room` | `tls: bad certificate` on the way to `hs1`, and the same 500 | **fails alone too** — the one of the four that is this server's to fix |

Fingerprints counted from that log: 4 wrong-name certificates, 3 refused TLS handshakes, 4 peers
refusing a request as unsigned. 20261107h, the cleanest full run on record, counts 0, 1 and 0. So
the parallel run is still talking over itself, less than 20261107m did and more than 20261107h.

Lists: `.agent-work/complement-{passes,failures}-20261107o.txt`; table
`.agent-work/next/complement-results-20261107o.tsv`; log `.agent-work/next/complement-run-20261107o.log`.

## Targeted run 2026-11-07p-relationscheck — the four suspects alone, and the first look at `/relations`

`RUN='TestRelations.*|TestThreadsEndpoint|TestDeviceListsUpdateOverFederationOnRoomJoin|TestFederatedClientSpaces|TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11|TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room'`
at tree `3f731a5`, `PARALLEL=1`: **6 passing, 2 failing.**

- Newly green at the first attempt: `TestRelations`, `TestRelationsPagination`,
  `TestRelationsPaginationSync` — the relation endpoint, its two path filters and its tokens.
- `TestThreadsEndpoint` red: the response was a `{event, latest_event, total}` triple, which is what
  MSC3440's draft spells, while Complement reads `unsigned.m.relations.m.thread.latest_event`
  inside the root event, which is what every implementation serves. Fixed in `2c31072`, not by
  touching the assertion.
- `TestDeviceListsUpdateOverFederationOnRoomJoin`, `TestFederatedClientSpaces` and
  `TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11` pass alone, which is the evidence that
  their failure in 20261107o is the deployment and not the protocol.
- `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room` fails alone: `hs1` is contacted at an
  address, the handshake is refused, and this server answers the join with 500 `M_UNKNOWN`. That is
  T-4071 (a retryable federation failure answered as a server fault) with a container-network cause
  underneath it.

## Targeted run 2026-11-07r-threads — relations and threads together

Same filter as above narrowed to the four relation tests, tree `2c31072`, `PARALLEL=1`:
**4 passing, 0 failing** — `TestRelations`, `TestRelationsPagination`, `TestRelationsPaginationSync`
and `TestThreadsEndpoint`. Four tests green that were never attempted before, with no assertion
changed and no test skipped. A thread as an endpoint does not on its own help
`TestThreadReceiptsInSyncMSC4102`: that one wants a *receipt* to carry a thread position, which is
the half of T-4083 that MSC2028 leaves open.

Runner changes along the way, all of them about reading a run honestly rather than running one:

- `json_events` splits the events `go test -json` glues onto one line and drops what is still not
  JSON, and the script says how many lines it dropped. Without it a finished run reports nothing.
- Federation fingerprints are counted from the log and printed next to the tally, kept apart
  because they mean different things: a certificate naming another server, a TLS handshake refused,
  a peer refusing a request as unsigned (those three are the container network), a federation
  template answered 401 (which a room may legitimately do), and a deployment whose image was never
  built (which is the runtime declining to start).

## Latest full run (2026-11-07m — after the volume cleanup; contaminated by a stale podman DNS record)

`RUN='.*' COMPLEMENT_LABEL=20261107m COMPLEMENT_TEST_TIMEOUT=40m PARALLEL=2 scripts/complement.sh -v`
at tree `24ae2b0` — the first full run after the anonymous-volume store was cleared (0 volumes at the
start, `exceeded num_locks` appears 0 times). **117 passing, 102 failing, 1 skipped.** It is *not* a
cleaner measurement than 20261107h: five tests the tree fixed went green, and nine two-server tests
went red on the container network rather than on the protocol.

| Field | Value |
| --- | --- |
| Newly passing | `TestChangePassword`, `TestDeactivateAccount` (T-4081), `TestRoomReceipts`, `TestRoomReadMarkers` (T-4083), `TestPushRuleRoomUpgrade` (the T-4071 flake, green in this run and red in 20261107h — neither result closes that task) |
| Newly failing | `TestFederatedClientSpaces`, `TestKnockRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11`, `TestMediaFilenames`, `TestMediaWithoutFileNameCSMediaV1`, `TestRemotePngThumbnail`, `TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11`, `TestRestrictedRoomsRemoteJoin`, `TestRestrictedRoomsRemoteJoinLocalUser`, `TestRestrictedRoomsSpacesSummaryFederation` — every one of them two-server |
| What their server logs say | 32 × `answered the join template with 401`, 10 × the knock template, 2 × the leave template, 5 × `hs1 answered M_UNAUTHORIZED for media: The request is not signed by a server this server trusts`, and 8 × `certificate is valid for hs2, localhost, not hs1`. 20261107h and 20261107c contain none of the certificate lines and 2 and 0 of the 401-template lines respectively |
| Why that is the network and not the tree | `$XDG_RUNTIME_DIR/containers/networks/aardvark-dns/complement_msc3902_1_servers` is still present although `podman network exists complement_msc3902_1_servers` says otherwise, and it maps `hs1` to **10.89.0.2, 10.89.1.2 and 10.89.2.2** — the addresses fresh deployments take now that the store has been emptied. A peer asked for `hs1` reaches another deployment's `hs2`, whose certificate says so, and everything the real peer signed is then refused |
| Isolation re-run | `20261107n-regresscheck` (same nine names, `PARALLEL=1`): **9 passing, 0 failing** — every one of them green alone. A test that passes alone and fails under parallel deployments is a name-collision measurement, not a protocol one |
| The measured position | 20261107h's **121 / 98 / 1** remains the last clean full-suite tally. T-4081 and T-4083 carry targeted runs (`20261107i-account`, `20261107k-receipts`) and are confirmed in this run too |
| Runner | `scripts/complement.sh` now counts those contamination lines next to the tally (it prints the count and says the two-server results should be re-run at `PARALLEL=1`), and refuses to start above 1300 anonymous volumes. Verified against the stored logs: 2 for 20261107h, 51 for 20261107m |
| Complement assertions changed | none |

Lists: `.agent-work/complement-{passes,failures}-20261107m.txt`; table
`.agent-work/next/complement-results-20261107m.tsv`; log `.agent-work/next/complement-run-20261107m.log`.
Isolation run: `.agent-work/complement-{passes,failures}-20261107n-regresscheck.txt`,
`.agent-work/next/complement-run-20261107n-regresscheck.log`.

## Latest full run (2026-11-07h — the three targeted fixes of 20261107d/e/g measured together; T-4071 seen again as a 500)

`RUN='.*' COMPLEMENT_LABEL=20261107h COMPLEMENT_TEST_TIMEOUT=40m PARALLEL=2 scripts/complement.sh -v`, Complement
`7b38a63c9db3`, image built at tree `6f3390f` (the size ceiling, the UTF-8 rule, the filter shape and
the filter applied at both doors). The `/account/password` and `/account/deactivate` endpoints came
after this run and are not in it.

| Field | Value |
| --- | --- |
| Tally | **121 passing, 98 failing, 1 skipped** (previously 118 / 101 / 1, the same 220-test universe) |
| Newly passing | `TestEvent` (413 `M_TOO_LARGE` at the storage funnel, T-4078), `TestRequestEncodingFails` (400 `M_NOT_JSON` for a body that is not UTF-8, T-4078), `TestFilter` (a filter whose fields are the wrong shape is refused, T-4079), `TestRoomImageRoundtrip` (`contains_url` honoured on `/messages`, T-4080) |
| Newly failing | `TestPushRuleRoomUpgrade`, which passed in 20261107c. It failed on `POST /join/{roomID}?server_name=hs1` returning **500** (`room_upgrade_test.go:193`), which is the defect T-4071 records — a retryable federation failure answered 500 `M_UNKNOWN` — so this is the task's substance being reproduced, not a new problem |
| Split | not re-cut per package for this run; the pass and failure lists are the qualified table's |
| Environment | the runner reported 1696 anonymous volumes and one stale `aardvark-dns` record (`complement_msc3902_1_servers`) before it started; no `exceeded num_locks` in the log, no package hit its 40-minute budget, and no name appears in both lists |
| Complement assertions changed | none |

Lists: `.agent-work/complement-passes-20261107h.txt`, `.agent-work/complement-failures-20261107h.txt`;
qualified table `.agent-work/next/complement-results-20261107h.tsv`; log
`.agent-work/next/complement-run-20261107h.log`.

## Run 2026-11-07l is not a tally: the podman volume store exhausted its locks

`RUN='.*' COMPLEMENT_LABEL=20261107l COMPLEMENT_TEST_TIMEOUT=40m PARALLEL=2 scripts/complement.sh -v`
was started at tree `df8f6fc` to re-measure the whole suite after the account and receipt work. It
finished with **3 passing, 217 failing, 0 skipped** and the runner's own warning beside it: *some
deployments never started*. It measures the container runtime, not this server, and it is recorded
here so that nobody reads 217 as a protocol result.

| Evidence | Value |
| --- | --- |
| `exceeded num_locks` lines in the log | 279; the first is `allocating lock for new volume: allocation failed; exceeded num_locks (2048)` |
| Deploy failures the runner counted | 63 (`failed to deployBaseImage` / `timed out checking for homeserver to be up`) |
| Anonymous volumes in the store when it started | 2038 — the runner warned before the first test ran |
| Top-level results | 3 tests passed (`TestMembersLocal`, `TestIsDirectFlagLocal`, `TestWriteMDirectAccountData`); the other 217 never reached an assertion |
| The measured position | unchanged: **20261107h, 121 passing / 98 failing / 1 skipped**, with the account (T-4081) and receipt (T-4083) work carrying targeted runs only |

Clearing it is an operator's call, not a script's — it deletes volumes and DNS files on a machine
that may be running other projects' containers. The two snippets are in `scripts/complement.sh`;
they are:

```
keep=$(for c in $(podman ps -aq); do podman inspect "$c" --format '{{range .Mounts}}{{.Name}} {{end}}'; done)
for v in $(podman volume ls -q); do case "$v" in *[!0-9a-f]*|"") continue;; esac
  [ ${#v} -eq 64 ] || continue; case "$keep" in *"$v"*) continue;; esac
  podman volume rm "$v" >/dev/null; done

dns="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/containers/networks/aardvark-dns"
for f in "$dns"/*; do n=${f##*/}; [ "$n" = aardvark.pid ] && continue
  podman network exists "$n" 2>/dev/null || rm -f "$f"; done
kill "$(cat "$dns/aardvark.pid")" 2>/dev/null || true
```

Lists and log: `.agent-work/complement-{passes,failures}-20261107l.txt`,
`.agent-work/next/complement-run-20261107l.log`.

## Targeted runs (2026-11-07j and 2026-11-07k — T-4083 read receipts, closed)

Same filter both times, so the second is a re-measurement and not a different question:
`RUN='^TestRoomReceipts$|^TestRoomReadMarkers$|^TestSync.*$|^TestRoomMessages.*$|^TestThreadReceipts.*$' COMPLEMENT_TEST_TIMEOUT=25m scripts/complement.sh -v`
with `COMPLEMENT_LABEL=20261107j-receipts` and `20261107k-receipts`. The family is wider than the
two new endpoints on purpose: a joined room is now included in a sync when it has a receipt to
report, and that rule had to be measured against every sync test in the suite.

| Field | 20261107j-receipts | 20261107k-receipts (after the fix) |
| --- | --- | --- |
| Tally | **9 passing, 2 failing** | **10 passing, 1 failing, 0 skipped** |
| `TestRoomReadMarkers` | green — the receipt and the `m.fully_read` bookmark both reach the caller's sync | green |
| `TestRoomReceipts` | **red**: the second receipt of the room `returned non-2xx code: 500 Internal Server Error` (`apidoc_room_receipts_test.go:61`) — an update of an existing receipt row had no key to update it by | green |
| `TestThreadReceiptsInSyncMSC4102` | red, as in 20261107h — receipts inside a thread, and threads are not implemented | red, unchanged |
| Sync family | `TestSync` (csapi and msc4222), `TestSyncFilter`, `TestSyncLeaveSection`, `TestSyncTimelineGap`, `TestSyncOmitsStateChangeOnFilteredEvents`, both lazy-loading `/messages` tests | same, all green — the receipt-carrying room does not disturb the rooms a sync chooses to include |
| Complement assertions changed | none | none |

Lists: `.agent-work/complement-{passes,failures}-20261107j-receipts.txt` and
`…-20261107k-receipts.txt`; tables
`.agent-work/next/complement-results-20261107{j,k}-receipts.tsv`; logs
`.agent-work/next/complement-run-20261107{j,k}-receipts.log`. The full-suite tally above is still
20261107h's 121 / 98 / 1; the account endpoints (T-4081) and the receipts (T-4083) are in the tree
after it and have only targeted runs beside them.

## Targeted run (2026-11-07i-account — T-4081 closed)

`RUN='^TestChangePassword$|^TestChangePasswordPushers$|^TestDeactivateAccount$|^TestLogin$|^TestLogout$|^TestDeviceManagement$|^TestRegistration$|^TestAccountData$|^TestWhoami$' COMPLEMENT_LABEL=20261107i-account COMPLEMENT_TEST_TIMEOUT=20m scripts/complement.sh -v`
— the two new account endpoints and the account family they could disturb. `TestWhoami` and
`TestAccountData` are not names in this Complement checkout, so seven of the nine ran.

| Field | Value |
| --- | --- |
| Tally | **6 passing, 1 failing, 0 skipped** |
| Newly passing | `TestChangePassword` (`POST /account/password`: UIA password stage, the old password stops opening the door, the new one does, the session that asked survives and the others are revoked), `TestDeactivateAccount` (`POST /account/deactivate`: the flow is offered, the wrong password is refused 401 `M_FORBIDDEN`, the account ends, and `/login` afterwards says 403) |
| Still failing | `TestChangePasswordPushers`, which needs `POST /pushers/set`; the endpoint is a 404 and push delivery is not implemented at all — opened as T-4082 |
| Regression check | `TestLogin`, `TestLogout`, `TestRegistration`, `TestDeviceManagement` green, as in 20261107h: the login door now names a deactivated account before it tests the password, and nothing else moved |
| Complement assertions changed | none |

Lists: `.agent-work/complement-passes-20261107i-account.txt`,
`.agent-work/complement-failures-20261107i-account.txt`; table
`.agent-work/next/complement-results-20261107i-account.tsv`; log
`.agent-work/next/complement-run-20261107i-account.log`.

## Targeted run (2026-11-07g-msgfilter — T-4080 closed)

`RUN='^TestRoomImageRoundtrip$|^TestSendAndFetchMessage$|^TestFetchMessagesFromNonExistentRoom$|^TestSendMessageWithTxn$|^TestRoomMessagesLazyLoading$|^TestRoomMessagesLazyLoadingLocalUser$|^TestMessagesOverFederation$|^TestFilter$|^TestSyncFilter$|^TestSync$|^TestFilterMessagesByRelType$' COMPLEMENT_LABEL=20261107g-msgfilter COMPLEMENT_TEST_TIMEOUT=25m scripts/complement.sh -v`
— the `/messages` and filter families, including the federation pagination test that is the reason
`end` is still omitted at the end of a timeline.

| Field | Value |
| --- | --- |
| Tally | **10 passing, 2 failing, 0 skipped** (twelve names, thirteen results: `TestSync` in two packages) |
| Newly passing | `TestRoomImageRoundtrip` — `filter={"contains_url":true}` selects the media event, which is what makes its `chunk` of 1 and its `end` token both true; `contains_url` was read nowhere in this server before |
| Unchanged | `TestFilter`, `TestSyncFilter`, `TestSync` (csapi and msc4222), `TestRoomMessagesLazyLoading`, `TestRoomMessagesLazyLoadingLocalUser`, `TestSendAndFetchMessage`, `TestSendMessageWithTxn`, `TestFetchMessagesFromNonExistentRoom` |
| Still failing, same reason as in 20261107c | `TestFilterMessagesByRelType` (MSC3874 `rel_types` on `/messages`), `TestMessagesOverFederation` (its backfill subtest waits on `SendEventSynced` for events that never arrive — the `/get_missing_events` and backfill gap, T-4009b2/T-4007) |
| A change that was made and taken back out | every page answering `end`: the reference servers do, and one Complement test asserts the key, but the specification omits it when nothing further is available and `TestMessagesOverFederation` paginates "until no `end` property is returned". It would have traded one test for the other; the filter was the gap. The quotation is recorded in `internal/rooms/history.go` |
| Complement assertions changed | none |

Lists: `.agent-work/complement-passes-20261107g-msgfilter.txt`,
`.agent-work/complement-failures-20261107g-msgfilter.txt`; table
`.agent-work/next/complement-results-20261107g-msgfilter.tsv`; log
`.agent-work/next/complement-run-20261107g-msgfilter.log`.

## Targeted run (2026-11-07e-filters — T-4079 closed)

`RUN='^TestFilter$|^TestSyncFilter$|^TestRoomMessagesLazyLoading$|^TestRoomMessagesLazyLoadingLocalUser$|^TestSync$|^TestSearch$|^TestPublicRooms$|^TestFilterMessagesByRelType$|^TestRoomImageRoundtrip$|^TestMediaConfig$|^TestSendAndFetchMessage$|^TestSendMessageWithTxn$' COMPLEMENT_LABEL=20261107e-filters COMPLEMENT_TEST_TIMEOUT=25m scripts/complement.sh -v`
— every Complement test that puts a `filter` in a request, run together so the new validation could be
checked against its own blast radius rather than against a hopeful subset. `TestSync` is defined in two
packages and is listed twice, so thirteen results for twelve names.

| Field | Value |
| --- | --- |
| Tally | **10 passing, 3 failing, 0 skipped** |
| Newly passing | `TestFilter` — twenty-one shapes a client can write, all answered 400 `M_INVALID_PARAM` naming the field, where 20261107c showed 200 and a stored filter |
| Still failing, unchanged from 20261107c | `TestSearch` (search is not implemented), `TestFilterMessagesByRelType` (MSC3874 `rel_types` on `/messages`), `TestRoomImageRoundtrip` (`MatchResponse key 'end' missing` — this server omits `end` when it thinks there is nothing further) |
| Regression check | `TestSync` green in both `tests/csapi` and `tests/msc4222`; `TestSyncFilter`, `TestRoomMessagesLazyLoading`, `TestRoomMessagesLazyLoadingLocalUser`, `TestPublicRooms`, `TestMediaConfig`, `TestRoomImageRoundtrip`'s neighbours all behave as in 20261107c |
| Complement assertions changed | none |

Lists: `.agent-work/complement-passes-20261107e-filters.txt`,
`.agent-work/complement-failures-20261107e-filters.txt`; table
`.agent-work/next/complement-results-20261107e-filters.tsv`; log
`.agent-work/next/complement-run-20261107e-filters.log`. The full-suite tally above remains 20261107c's.

## Targeted run (2026-11-07d-sizelimit — T-4078 closed)

`RUN='^TestEvent$|^TestRequestEncodingFails$' COMPLEMENT_LABEL=20261107d-sizelimit scripts/complement.sh -v`,
Complement `7b38a63c9db3`, image built at the tree carrying `maxPDUSize` in `persistEvent` and the
UTF-8 check in `httpx.DecodeJSON`. Every other package reported "no tests to run", so this is a
two-test run and nothing else in it is measured.

| Field | Value |
| --- | --- |
| Tally | **2 passing, 0 failing, 0 skipped** (top-level; the run's whole universe was these two tests) |
| Passing | `TestEvent` — `Large_Event` and `Large_State_Event` now answer 413 `M_TOO_LARGE`, where 20261107c showed 200 for both; `TestRequestEncodingFails` — `POST_rejects_invalid_utf-8_in_JSON` answers 400 `M_NOT_JSON`, where 20261107c showed 401 `M_NO_AUTH` because Go's decoder had swallowed the invalid byte and the request reached the auth question |
| Newly failing | not measurable from a two-test run |
| Full-suite tally | unchanged: 20261107c's 118 / 101 / 1, above. These two tests were failing there and are green in this run; a full re-run has not been done since |
| Complement assertions changed | none |

Lists: `.agent-work/complement-passes-20261107d-sizelimit.txt` (2 lines),
`.agent-work/complement-failures-20261107d-sizelimit.txt` (empty);
table `.agent-work/next/complement-results-20261107d-sizelimit.tsv`;
log `.agent-work/next/complement-run-20261107d-sizelimit.log`.

## Latest full run (2026-11-07c — T-4074 the membership handshakes closed, T-4075/T-4076/T-4077 opened)

`RUN='.*' COMPLEMENT_LABEL=20261107c COMPLEMENT_TEST_TIMEOUT=40m scripts/complement.sh -v`, Complement
`7b38a63c9db3`, image built at tree `f18742b` (leave/knock handshakes, the invite spelling fix and the
room-version/knock-restricted tests in that commit). No name appears in both lists; no `exceeded
num_locks`; no package hit its 40-minute budget.

| Field | Value |
| --- | --- |
| Tally | **118 passing, 101 failing, 1 skipped** (previously 109 / 110 / 1, the same 220-test universe) |
| Newly passing | `TestCannotSendNonJoinViaSendJoinV1`, `TestCannotSendNonLeaveViaSendLeaveV1`, `TestCannotSendNonLeaveViaSendLeaveV2`, `TestCannotSendNonKnockViaSendKnock`, `TestCannotSendKnockViaSendKnockInMSC3787Room` (the `send_*` endpoints answer 400 for a membership the endpoint is not for, T-4074); `TestEventAuth` and `TestIsDirectFlagFederation`, both of which drive a Complement server rejecting an invite, i.e. `/make_leave` + `/send_leave` reaching this server (T-4074); `TestKeyChangesLocal` (`GET /keys/changes`, T-4072, and the `r0` prefix, T-4073 — green in the earlier `r0keys` run and in this one); `TestPushRuleRoomUpgrade`, which is the flaky test of T-4071 and passing does not close it — see below |
| Newly failing | none |
| Split | csapi 71/106, `tests` (server-server) 43/91 with the run's one skip, MSC packages 4/23 |
| Still red in the cluster this run worked on | `TestKnocking` and `TestKnockingInMSC3787Room`. Their local halves went from 3 passing of 13 subtests to 12 of 13 in the targeted run `membshk3`; what is left there is one question about what a first sync's `state` section is (T-4076) and the fact that a knocked-on remote room is invisible to the user who knocked on it (T-4075). `TestRoomForget` and `TestLeftRoomFixture` are leaving-versus-forgetting, T-4077 |
| `TestPushRuleRoomUpgrade`, stated honestly | green in this run and green in the solo re-run `pushup4` (`RUN=TestPushRuleRoomUpgrade`, 1 passing, 0 failing); it failed twice in the 2026-11-07b window, once on a cached negative answer of "no keys recorded" and once on a peer name resolving to a deleted deployment (`certificate is valid for hs2, not hs1`). T-4071 stays open: the 500 `M_UNKNOWN` a client gets for a retryable federation failure is still wrong regardless of the flake |
| Environment checks | 1355 anonymous volumes and one stale `aardvark-dns` record (`complement_msc3902_1_servers`) reported by the runner before it started; neither produced a deploy failure in this run's log, but both are still there for the next one, and clearing them is an operator's call (the commands are in `scripts/complement.sh`) |
| Complement assertions changed | none |

Lists: `.agent-work/complement-passes-20261107c.txt`, `.agent-work/complement-failures-20261107c.txt`;
qualified table: `.agent-work/next/complement-results-20261107c.tsv`;
log: `.agent-work/next/complement-run-20261107c.log`.
Targeted run quoted above: `.agent-work/next/complement-run-membshk3.log` with
`.agent-work/next/complement-results-membshk3.tsv` (2 passing, 4 failing in that subset);
the `pushup4` re-run: `.agent-work/next/complement-run-pushup4.log`.

## Latest full run (2026-11-07b — T-4067 MSC4222 closed, T-4069 and T-4070 closed)

`RUN='.*' COMPLEMENT_LABEL=20261107b COMPLEMENT_TEST_TIMEOUT=40m scripts/complement.sh -v`, Complement
`7b38a63c9db3`, image from `deploy/complement/Dockerfile` at this commit. This is the first run whose
pass/failure lists come from the package-qualified results table, so a test name shared by two
packages can no longer be counted once (T-4066): `TestSync` is green in both `tests/csapi` and
`tests/msc4222`, and no name appears in both lists.

| Field | Value |
| --- | --- |
| Tally | **109 passing, 110 failing, 1 skipped** (previously 106 / 113 / 1, the same 220-test universe) |
| Newly passing | `TestAddAccountData`, `TestRemovingAccountData` (room-scoped account data and MSC3391 removals reaching `/sync`); `TestSync` in `tests/msc4222`, both subtests — the whole MSC4222 package, green for the first time (T-4067); `TestSyncOmitsStateChangeOnFilteredEvents` (a state event a filtered batch hides must still be in the state section, T-4070) |
| Newly failing | `TestPushRuleRoomUpgrade`, which is flaky and not a regression: three runs of it after this one gave fail (a federation key fetch answered 401 because the peer "has no keys recorded and was asked for too recently to ask again"), fail (a peer name resolving to the wrong container, so the TLS check saw `certificate is valid for hs2, not hs1`) and **pass**. Both failure modes are environment/timing at container start, and the second is the stale-DNS condition the runner warns about |
| Split | csapi 69/106, `tests` (server-server) 36/91 with the run's one skip, MSC packages 4/23 |
| Environment checks | 960 anonymous volumes and one stale `aardvark-dns` record (`complement_msc3902_1_servers`) reported by the runner before it started; no `exceeded num_locks`; no package hit its 40-minute budget; no deploy-timeout failures |
| Complement assertions changed | none |

Lists: `.agent-work/complement-passes-20261107b.txt`, `.agent-work/complement-failures-20261107b.txt`;
qualified table: `.agent-work/next/complement-results-20261107b.tsv`;
log: `.agent-work/next/complement-run-20261107b.log`.

## Latest full run (2026-11-07, remote membership changes — T-4062 closed, T-4063/T-4064 opened)

`RUN='.' PARALLEL=2 COMPLEMENT_LABEL=20261107a scripts/complement.sh -v`, Complement `7b38a63c9db3`,
image from `deploy/complement/Dockerfile` at this commit. The runner now writes its own log and cuts
its own pass/failure lists, so this tally and the lists under `.agent-work/` are one artefact.

| Field | Value |
| --- | --- |
| Tally | **106 passing, 113 failing, 1 skipped** (previously 104 / 115 / 1, the same 220-test universe) |
| Newly failing | none |
| Newly passing | `TestUnbanViaInvite` and `TestFederationRoomsInvite/Inviter user can rescind invite over federation` (the kick, ban and unban of a remote user, T-4062); `TestAsyncUpload`, which the 2026-11-06 records described as turned green by the `M_CANNOT_OVERWRITE_MEDIA` correction while that run's own list still had it failing — it is green here, in a run whose log says so |
| Split | csapi 69/107 (was 68: `TestRoomDeleteAlias`, flaky), server-server 35/92 (was 34: `TestUnbanViaInvite`), MSC packages 2/22 |
| Red in the same area | `TestFederationRoomsInvite` remains red as a whole for one subtest, which expects the invited user's own server to keep holding the invitation after a moderator on another server kicks that user (T-4063); inside that subtest a `SendEventSynced` step also stalls, which needs its own diagnosis |
| Reproducibly red, newly recorded | `TestDeviceListUpdates/when_joining_a_room_with_a_remote_user` — nine of that test's ten subtests pass; the matrix row claiming all ten was wrong and is corrected (T-4064) |
| Environment checks | no leaked-volume or stale-DNS warning; no `exceeded num_locks`; 0 panics |
| Complement assertions changed | none |

Lists: `.agent-work/complement-passes-20261107a.txt`, `.agent-work/complement-failures-20261107a.txt`;
log: `.agent-work/next/complement-run-20261107a.log`.

## A full run on the abandoned `main` branch, measured for comparison (2026-11-07)

Not this branch's compliance position — recorded because `retry` and `main` share no history, and the
comparison decides what to port rather than re-derive. Same Complement checkout, same machine, same
runner.

| Field | Value |
| --- | --- |
| Image as checked out | **does not start**: `deploy/complement/Dockerfile` copies a host-built, glibc-dynamic binary into an Alpine (musl) runtime, so every container dies at `lattice: not found` and every test fails as a deploy timeout. Built with `CGO_ENABLED=0` it starts |
| Server-server (`tests`) | **78 of 92 green** — matches what `main`'s own `docs/TODO.md` claimed |
| `tests/csapi` | did **not** finish in 40 minutes; of the 62 that ran before the timeout, 51 were green |
| Against this branch | `main` passes 45 server-server tests this branch fails (knocking, `/make_leave`/`/send_leave`, MSC4289/4291/4297/4311, `get_missing_events`, typing, federation profile, `TestNetworkPartitionOrdering`); this branch passes one that `main` fails (`TestUnrejectRejectedEvents`) |
| Decision this puts on the record | port the missing handshake family and the room-version-12 MSC work from `main` rather than re-deriving it, and keep this branch's image, runner, evidence discipline and static build |

## Latest full run (2026-11-11, profiles — T-4057 closed, T-4058 opened)

`RUN='.*' COMPLEMENT_LOCAL_MIGRATIONS=true scripts/complement.sh -v`, commit `4e20c57`:
**95 pass, 124 fail, 1 skip** (was 90 / 129 / 1). Newly passing: `TestProfileDisplayName`,
`TestProfileAvatarURL`, `TestDisplayNameUpdate`, `TestAvatarUrlUpdate` (the profile endpoints and the
member-event restatement that follows a profile change) and `TestPushRuleRoomUpgrade`, which had been
flaky. Nothing newly failing, which matters because join events now carry `displayname`/`avatar_url`.

Still red in that area for reasons already recorded: the federation `/profile` query (a remote user cannot
be asked about except through rooms already shared) and the user directory, where the two
room-specific-username tests stop (T-4058).

Two environment conditions can invalidate a run without touching the protocol, and both are reported by
`scripts/complement.sh` before it starts: leaked anonymous volumes reaching podman's 2048 volume-lock
limit, and `aardvark-dns` record files left behind by networks that no longer exist (T-4056). A block of
~0.5 second failures means check the volume count; federation tests disagreeing about who is who means
check the DNS records.

The per-run logs and failure lists live in `.agent-work/COMPLIANCE-MATRIX.md` and
`.agent-work/complement-failures-*.txt`.

## Media cluster after the content repository (2026-11-06, T-4059/T-4060 closed)

| Field | Value |
| --- | --- |
| Filter | `RUN='TestFederation\|TestUpload\|TestDownload\|TestThumbnail\|TestMedia\|TestUrlPreview\|TestConfig'`, packages `-p 2`, Complement at `7b38a63c9db3` |
| Result | **51 passing, 9 failing** (top-level plus subtests) |
| Failing names | `TestUrlPreview` (URL previews not implemented), `TestFederationRoomsInvite`, `TestFederationKeyUploadQuery`, `TestFederationRedactSendsWithoutEvent` — all four already in `.agent-work/complement-failures-20261113d.txt`, so none newly red |
| Newly passing because of this work | the `TestMediaFilenames`, `TestMediaWithoutFileName`, `TestThumbnail*`, `TestUpload*`, `TestDownload*` families, and the federation media subtests within them. A follow-up correction — `409 M_CANNOT_OVERWRITE_MEDIA` for an upload over an address that already has content, which this server was answering `M_FORBIDDEN` — turned `TestAsyncUpload` green too |
| Bug fixed on the way | federation credential refusals caused by two concurrent `SaveServerKeys` replacements colliding on `(server_name, key_id)`; see T-4060, DEC-045 |
| Verification of that fix | `TestMediaFilenames\|TestMediaWithoutFileName`: 2 of 3 runs red before (each with one credential-check refusal), 3 of 3 runs green after, 0 refusals |
| Server faults observed | 0 panics; the only 500s in the cluster were the media fetch failing while the bug above was present |
| Complement assertions changed | none |

## Latest full run (content repository and the keyring race — T-4059, T-4060 closed, T-4061 opened)

`RUN='.' PARALLEL=2 scripts/complement.sh -v`, Complement `7b38a63c9db3`, image from
`deploy/complement/Dockerfile` at `505d252`.

| Field | Value |
| --- | --- |
| Tally | **104 passing, 115 failing, 1 skipped** (previously 95 / 124 / 1; the same 220-test universe) |
| Newly failing | none |
| Newly passing (media) | `TestContentMediaV1`, `TestContentCSAPIMediaV1`, `TestMediaFilenames`, `TestMediaWithoutFileName`, `TestMediaWithoutFileNameCSMediaV1`, `TestFederationThumbnail`, `TestLocalPngThumbnail`, `TestRemotePngThumbnail`, `TestMediaConfig`, `TestAsyncUpload` |
| Newly passing (federation, from the same fix) | `TestFetchEvent`, `TestFetchEventWorldReadable`, `TestFetchHistoricalSharedEvent`, `TestFetchHistoricalInvitedEventFromBetweenInvite`, `TestDeviceListsUpdateOverFederationOnRoomJoin`, `TestCumulativeJoinLeaveJoinSync`, `TestGappedSyncLeaveSection` |
| Cause of those federation failures | two concurrent fetches of the same peer's signing keys each replaced that server's key rows as separate statements, collided on `(server_name, key_id)`, left the keyring with nothing to answer with, and a correctly signed request was refused as unauthenticated |
| Still red in this area | `TestUrlPreview` (URL previews not implemented — answered `M_UNRECOGNIZED`); `TestFederationKeyUploadQuery`, `TestFederationRoomsInvite`, `TestFederationRedactSendsWithoutEvent` (unrelated, already red) |
| Flaky, recorded not fixed | `TestFederationRejectInvite` green in this run, red in another; same intermittent send-to-own-sync observation as T-4034 |
| Environment checks | `exceeded num_locks` 0 occurrences; no leaked-volume or stale-DNS warning |
| Complement assertions changed | none |

Lists: `.agent-work/complement-passes-20261113e.txt`, `.agent-work/complement-failures-20261113e.txt`.
