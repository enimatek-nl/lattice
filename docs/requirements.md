# Requirements

Derived from Matrix v1.19 (see `docs/specification-version.md`). Status vocabulary:

- **implemented** — code exists and is covered by the project's own tests
- **partial** — some normative behaviour is missing; the gap is named
- **not implemented** — deliberately deferred, listed so nothing is silently omitted
- **n/a** — not applicable to this server

Test paths are relative to the repository root.

## 1. Appendices: protocol primitives

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-JSON-001 | Appendices §Canonical JSON | Encode JSON as UTF-8, sorted by code point, no insignificant whitespace, objects not arrays at top level of signed payloads | MUST | implemented | `tests/unit/canonicaljson_test.go` |
| REQ-JSON-002 | Appendices §Canonical JSON numbers | Integers only, within −(2^53−1)…2^53−1, no fractional part | MUST | implemented | `tests/unit/canonicaljson_test.go` |
| REQ-JSON-003 | Appendices §Base64 | Unpadded base64 in output; accept padded input | MUST | implemented | `tests/unit/signjson_test.go` |
| REQ-SIGN-001 | Appendices §Signing JSON | Sign canonical form of the object minus `signatures` and `unsigned`; ed25519 only | MUST | implemented | `tests/unit/signjson_test.go` |
| REQ-SIGN-002 | Appendices §Verifying JSON | Follow the seven verification steps; an origin counts as verified if any of its signatures validates | MUST | implemented | `tests/unit/signjson_test.go` |
| REQ-ID-001 | Appendices §Identifier grammar | Server names (DNS or literal IP with optional port), user IDs, room IDs, aliases, event IDs, device IDs; localpart grammar; byte limits | MUST | implemented | `tests/unit/matrixid_test.go` |
| REQ-ID-002 | CS §Application Service / registration | User IDs are lowercase; server names are case-sensitive | MUST | implemented | `tests/unit/matrixid_test.go`, `tests/integration/auth_test.go` |

## 2. HTTP and error semantics

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-HTTP-001 | CS §Standard error codes | Errors are JSON objects with `errcode` and `error` | MUST | implemented | `tests/unit/httpsemantics_test.go`, `tests/integration/http_test.go` |
| REQ-HTTP-002 | Complement `TestUnknownEndpoints` | Unknown path under a Matrix prefix → 404 `M_UNRECOGNIZED` | MUST | implemented | as above |
| REQ-HTTP-003 | Complement `TestUnknownEndpoints` | Known path, wrong method → 405 `M_UNRECOGNIZED` with `Allow` | MUST | implemented | as above; method knowledge is mirrored across the two listeners so a federation path probed on the client port still answers 405 |
| REQ-HTTP-004 | CS §Versioning | Method-prefixed routing on `http.NewServeMux` | MUST | implemented | `internal/httpserver/router.go` |
| REQ-HTTP-005 | AGENTS §9 | Bound request bodies, request timeouts, panic recovery, security headers | MUST | implemented | `tests/integration/http_test.go` |
| REQ-HTTP-006 | — | Access tokens, passwords and key material never appear in logs | MUST NOT | implemented | `tests/unit/logging_test.go`, `tests/integration/lifecycle_test.go` |
| REQ-HTTP-007 | CS §API Standards vs §GET /rooms/{roomID}/state | The standards say every endpoint answers with a JSON object; this endpoint documents its response as "Array of `ClientEvent`" and shows a top-level array. The endpoint-specific statement wins: the body is the bare array. Wrapping it in `{"state": […]}` is what an object-only reading produces, and Complement's room-state test walks the array and says such a server has no state | MUST (as the endpoint defines it) | implemented+tested | `tests/integration/membership_test.go` (`TestStateReads`), Complement `TestRoomState` |


## 3. Authentication and accounts

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-AUTH-001 | CS §Server-side discovery / UIA | `/register` performs User-Interactive Auth with `flows`, `params`, `session`, `completed` | MUST | implemented | `tests/integration/auth_test.go` |
| REQ-AUTH-002 | CS §Registration | `m.login.dummy` only completes a session this server issued | MUST | implemented | `tests/integration/auth_test.go`; Complement `Registration_without_a_session_fails` passes |
| REQ-AUTH-003 | CS §Registration | Invalid localpart → 400 `M_INVALID_USERNAME`; taken ID → 400 `M_USER_IN_USE`; both reported before the challenge | MUST | implemented | Complement `TestRegistration` |
| REQ-AUTH-004 | CS §Registration | Usernames are downcased; `q3._=-/` allowed; spaces and `,:@[]{}\|"'` and non-ASCII refused | MUST | implemented | Complement `TestRegistration` |
| REQ-AUTH-005 | CS §Login | `m.login.password` with `identifier` of type `m.id.user`; unknown user and wrong password both → 403 `M_FORBIDDEN` | MUST | implemented | Complement `TestLogin` |
| REQ-AUTH-006 | CS §Login | Login by full MXID, bare localpart, and in uppercase | MUST | implemented | Complement `Login with uppercase username…` |
| REQ-AUTH-007 | CS §Login | `device_id` in the request is honoured; `initial_device_display_name` stored | MUST | implemented | Complement `TestLogin`, `TestDeviceManagement` |
| REQ-AUTH-008 | CS §Logout | `POST /logout` invalidates only the calling device; `POST /logout/all` invalidates every session | MUST | implemented | `tests/integration/auth_test.go`; Complement `TestLogout` partially blocked on `/sync` (see compliance matrix) |
| REQ-AUTH-009 | CS §Error codes | Missing token → 401 `M_MISSING_TOKEN`; unknown/revoked token → 401 `M_UNKNOWN_TOKEN` | MUST | implemented | Complement `TestLogout` token subtests |
| REQ-AUTH-010 | CS §Devices | `GET /devices`, `GET/PUT/DELETE /devices/{id}`; unknown device → 404; delete requires UIA by the owning account | MUST | implemented | Complement `TestDeviceManagement`; UIA subtests blocked on `/sync` |
| REQ-AUTH-011 | CS §Account data / whoami | `GET /account/whoami` returns the token's user | MUST | implemented | Complement `Login with uppercase…` |
| REQ-AUTH-012 | Security | Passwords stored with bcrypt only, never plaintext; cost configurable and validated | MUST NOT | implemented | `tests/unit/storage_test.go` |
| REQ-AUTH-013 | Security | Access tokens stored as SHA-256 digests, revocable per device and per account | MUST | implemented | `tests/unit/storage_test.go` |
| REQ-AUTH-014 | CS §Registration (shared secret) | Synapse-compatible `/_synapse/admin/v1/register` with HMAC-SHA1 nonce, single-use nonce, constant-time MAC compare | MAY | implemented | Complement `TestRegistration` shared-secret subtests, `TestCanRegisterAdmin` |
| REQ-AUTH-015 | CS §Registration availability | `GET /register/available` → `{"available": true}` / 400 `M_USER_IN_USE` / 400 `M_INVALID_USERNAME` | MAY | implemented | Complement `TestRegistration` |
| REQ-AUTH-016 | CS §Deactivate account | `POST /account/deactivate` | MUST | not implemented | M2+; middleware already reports `M_USER_DEACTIVATED` for deactivated rows |
| REQ-AUTH-017 | CS §Change password | `POST /account/password` | MUST | not implemented | `capabilities` honestly reports `m.change_password.enabled = false` |
| REQ-AUTH-018 | CS §Refresh tokens | `refresh_token` request | MAY | not implemented | Refused with 400 `M_INVALID_PARAM` rather than ignored |
| REQ-AUTH-019 | CS §Login token / SSO | `m.login.token`, `m.login.application_service` | MUST (per flow) | not implemented | only the password flow is offered |

## 4. Discovery and versioning

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-DISC-001 | CS §Versioning | `GET /_matrix/client/versions` lists global version strings, optional object `unstable_features` | MUST | implemented | Complement `TestVersionStructure` |
| REQ-DISC-002 | CS §Client-Specification | `GET /capabilities` requires a token and exposes `m.room_versions`, `m.change_password` | MUST | implemented | Complement `TestServerCapabilities` |
| REQ-DISC-003 | CS `/.well-known/matrix/client` | Publish the client discovery document when configured | SHOULD | implemented | `tests/integration/http_test.go` |
| REQ-DISC-004 | SS `/.well-known/matrix/server` | Publish the server discovery document when configured | SHOULD | implemented | as above |
| REQ-DISC-005 | CS §Application Service | `GET /versions` for application services | n/a | n/a | no application service support yet |

## 5. Server keys and federation surface present today

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-KEY-001 | SS §Server Signing Keys | Generate and persist an ed25519 signing key; key id `ed25519:<identifier>` | MUST | implemented | `tests/unit/storage_test.go` |
| REQ-KEY-002 | SS §Server Signing Keys | `GET /_matrix/key/v2/server` returns a self-signed key document with `valid_until_ts` | MUST | implemented | `tests/integration/http_test.go` verifies the signature independently |
| REQ-KEY-003 | SS §Server Signing Keys | `POST /_matrix/key/v2/query` answers for this server; honours `minimum_valid_until_ts` | MUST | partial | answered for self only; the *notary* half (asking another server about a stranger's keys) is implemented in the keyring and tested, the answering half for other servers' keys is not |
| REQ-KEY-004 | SS §Request authentication | `X-Matrix` signing of outbound/inbound federation requests | MUST | implemented+tested | `tests/unit/fedauth_test.go`, `tests/integration/federation_test.go`: signature, wrong destination, tampered body, unknown key ID, no credential |
| REQ-KEY-005 | SS §Transport | Federation listener serves HTTPS with TLS ≥ 1.2; CA-signed certificate supported | MUST | implemented | `tests/integration/lifecycle_test.go`, `tests/unit/tlsgen_test.go` |
| REQ-KEY-006 | SS §Server Signing Keys | A remote server's key document is trusted only when that server signed a key it publishes; the trust window is clamped; retired keys stay usable until their own `expired_ts`; expiry is enforced per room version | MUST | implemented+tested | `tests/unit/federationkeys_test.go` |
| REQ-KEY-009 | SS §EDUs (`m.device_list_update`), CS §device lists | A device list change for a local user is announced to the homeservers that hold a joined room with that user, and an announcement received from a peer is passed on to the local users who share a room with the user it names. A peer is only believed about users whose IDs sit in its own name. The update number is per user and strictly increasing, so a peer can tell a newer announcement from one it has already applied | MUST | implemented+tested: announcements are sent on a key upload and when a local user joins a room held elsewhere; received announcements about another server's users are dropped while the transaction is still accepted | `tests/integration/federationdevicelists_test.go`, `tests/unit/devicestreams_test.go` |
| REQ-KEY-008 | SS §POST /user/keys/query, CS §POST /keys/query | A key query for a user who lives on another homeserver is answered by asking that homeserver, because it holds the key documents that user's devices uploaded; the answer is kept only for the users that were asked about, and only when their user IDs sit in the namespace of the server that answered. A homeserver answering a peer's question answers only for its own users, and the key documents are handed over exactly as uploaded, because the device signed them in that shape and the client is the one that checks | MUST | implemented+tested: a peer that answers for somebody else's users is not believed, and one unreachable peer leaves that user's entry empty rather than failing the whole query | `tests/integration/federationdevicekeys_test.go` (`TestRemoteDeviceKeysComeFromTheirOwnServer`), `tests/integration/keys_test.go` |
| REQ-KEY-007 | SS §Server Signing Keys | A certificate obtained by dialing a discovered address is verified against the *name*, not the address | MUST | implemented | `internal/federation/client.go` (`clientFor`); exercised end to end in `tests/integration/federation_test.go` |
| REQ-FED-000 | SS §Server Signing Keys | A signed key document is served in the canonical form its signature was made over. A peer that checks the bytes it received — which is what Complement does — reports a re-encoded document as a bad signature, however good the key is | MUST | implemented+tested (the endpoint served Go-encoded JSON, with a trailing newline and Go's escaping; Complement `TestInboundFederationKeys` failed and now passes) | `tests/integration/http_test.go` (`TestServerKeysVerifies` asserts the body is its own canonical form and verifies over it), Complement `TestInboundFederationKeys` |
| REQ-FED-001 | SS §PDUs/EDUs, joins, backfill, `/send/:txn` | Full federation exchange | MUST | partial | transactions, PDU ingest checks, event read, the outbound queue, and the resident half of the join handshake are implemented and tested two-servers-in-one-process; the outbound half of a join, the missing-event fetch that fills a joined room's history, leave/invite and answering `/backfill` are the remaining parts |
| REQ-FED-002 | SS §Server name resolution | `.well-known/matrix/server` delegation, SRV records, IP literals and explicit ports, certificate name rules, cached answers. The certificate name rule is applied wherever this server reaches a named server, including the key endpoints: the connection goes to the resolved address while the certificate and TLS server name are checked against the name. A service record that cannot be read at all (SERVFAIL, a timeout, a resolver that does not serve SRV) falls through to the name itself on the federation port rather than ending the attempt, and a name that could not be looked up at all is held briefly rather than for the well-known hour, so one bad moment does not make a healthy peer uncontactable so a peer whose certificate names only its domain is accepted and one named for somebody else is not | MUST | implemented+tested | `tests/unit/federationdiscovery_test.go` |
| REQ-FED-003 | SS §Server name resolution (security) | Refuse to dial addresses that discovery was not entitled to produce (loopback, link-local metadata, private ranges) unless configured | MUST | implemented+tested | `tests/unit/federationdiscovery_test.go`, `tests/unit/config_test.go`. Known limit: the check happens at resolution, not at dial (DNS rebinding) |
| REQ-FED-004 | SS §PDUs | A received PDU is stored only if it is valid in its room version, its ID is the hash its content makes, its content hash matches, its origin signed it, and its cited auth events authorise it | MUST | implemented+tested | `tests/integration/federation_test.go`, `tests/unit/eventsignature_test.go` |
| REQ-FED-005 | SS §PDUs | An event authorised by its own branch but refused by the room's current state is stored and soft-failed, not rejected | MUST | implemented | `internal/rooms/remote.go`; soft-failed events are excluded from relay and forward extremities. No two-server assertion yet that a client never sees one |
| REQ-FED-006 | SS §Transactions | A transaction is processed once; a replay gets the same answer; each PDU is answered by ID with its own outcome; the PDU/EDU caps are enforced | MUST | implemented+tested | `tests/integration/federation_test.go` |
| REQ-FED-007 | SS §EDUs | EDUs are received and acknowledged | MUST | partial | received, counted against the cap, acknowledged and logged; none of them do anything yet (T-4008) |
| REQ-FED-008 | Architecture requirement in AGENTS.md §8 | One unreachable homeserver must not block unrelated local operations | MUST | implemented+tested | delivery is queued and retried with a bounded schedule; a local send does not wait for a remote. `tests/integration/federation_test.go` (`TestFederationSenderQueue`) |
| REQ-FED-009 | SS §Access and permission rules for state | Federation reads are limited to servers the room's own state says are present | MUST | implemented+tested | `tests/integration/federation_test.go` (`TestFederationEventAccess`), `tests/integration/federationjoins_test.go` (`TestStateAndAuthChainAreAnsweredToServersInTheRoom`) |
| REQ-FED-010 | SS §GET /make_join, §PUT /send_join | A resident server hands an unsigned join template in the room's own version. `ver` names the room versions the *asker* can speak, repeated once per version, and is answered by asking whether the room's version is in that set: if it is, the template in the room's version; if not, `M_INCOMPATIBLE_ROOM_VERSION` carrying the room's version, which is the fact the asker needs; an asker that names no versions is answered rather than refused on the legacy default (DEC-035, DEC-037), and only for a user its copy of the room permits; it then checks the signed event against what the URL claims, accepts it into the graph, signs it itself, answers with the state *before* the join plus its auth chain, and relays it to the room's other servers | MUST | implemented+tested on both sides: the resident's endpoints and the handshake this server runs when one of its users joins a room held elsewhere | `tests/integration/federationjoins_test.go` (`TestRemoteUserJoinsARoomHeldElsewhere`, `TestMakeJoinRefusesAUserTheRoomDoesNotPermit`, `TestSendJoinRefusesAnEventThatIsNotWhatItClaims`) |
| REQ-FED-011 | SS §GET /state_ids, §GET /event_auth | A room's state and an event's auth chain are answered by ID to servers the room has a seat for, from what is stored rather than from what this server currently believes | MUST | implemented+tested | `tests/integration/federationjoins_test.go` (`TestStateAndAuthChainAreAnsweredToServersInTheRoom`) |
| REQ-FED-013 | SS §Checks performed on receipt of a PDU, applied to a bundle | A set of events handed over at once (the answer to `/send_join`) is taken as a snapshot: each event is verified on its own, authorised against the auth events it cites whether those are stored here or in the same bundle, and applied in dependency order by walking to a fixed point. A missing citation is a gap and is never recorded as a refusal; a forged event is refused and the refusal is remembered | MUST | implemented+tested | `internal/rooms/bundle.go`, `tests/integration/joinremote_test.go` (`TestHandedOverBundleGapsAreNotVerdicts`, `TestForgedEventInABundleIsRefused`) |
| REQ-FED-014 | SS §PDU persistence (outliers) | An event accepted without its predecessors is stored as an outlier: its state counts and it can authorise later events, but it is kept out of client timelines and out of the room's forward extremities until the gap is filled | MUST | implemented+tested: the exclusion, and the flag being lifted once the predecessors arrive | `internal/persist/event.go` (`MarkOutlier`), `internal/persist/history.go`, `internal/persist/sync.go`, `tests/integration/joinremote_test.go` (`TestLocalUserJoinsARemoteRoomThroughTheClientAPI`) |
| REQ-FED-015 | SS §Join restrictions | A room this server does not hold is not invented by a join: a room ID carrying this server's own name is answered 404 rather than asked of a stranger, and a room is only adopted from a create event | MUST | implemented+tested | `internal/rooms/joinremote.go`, `internal/rooms/bundle.go`, `tests/integration/joinremote_test.go` (`TestRemoteJoinRefusesWithoutInventingARoom`) |
| REQ-FED-016 | SS §GET /event (as a client), §PDU persistence | A room this server holds only partly is caught up by asking a server that was in the room first: the holes are named from this room's own events, fetched newest-first, walked backwards until they meet what this server holds, stored parents-first so this server's own ordering follows causality rather than arrival, and bounded by a budget that leaves the rest of the history askable. The same ask answers an event that arrives describing itself as descending from events this server does not hold: the room service names the missing predecessors, the federation layer asks the server that offered the event for them, and the event is offered once more | MUST | implemented+tested (triggered by a join and by an incoming event with missing predecessors; no periodic pass, and this server does not answer `GET /backfill` or `GET /get_missing_events` yet — it asks for missing events with `GET /event`) | `internal/federation/backfill.go`, `internal/rooms/gap.go`, `internal/persist/event.go` (`MissingPredecessors`, `Outliers`), `tests/integration/joinremote_test.go` (`TestBackfillMakesAJoinedRoomsHistoryReadable`, `TestBackfillBudgetLeavesAGapAskable`), `tests/unit/eventstore_test.go` |
| REQ-FED-012 | SS §Server access control lists, CS §m.room.server_acl | `m.room.server_acl` excludes homeservers from a room. The exclusion is read from the room's current state and applied before the room is described to a server (`/make_join` v1 and v3, `/state_ids`, `/event_auth`, `/event`), per PDU inside a transaction, and on the outbound side: a room's events are not relayed to a server the room excluded. Patterns are globs (`*`, `?`) matched case-insensitively against the server name without its port; `deny` is consulted before `allow` and wins; `allow_ip_literals: false` denies servers named by an address; an ACL with no allow list excludes every server | MUST | implemented+tested. Not yet applied where the endpoint itself is missing (`/make_leave`, `/send_leave`, `/backfill`, `/get_missing_events`), and an ACL this server cannot parse is not applied (DEC-031) | `internal/serveracl/serveracl.go`, `internal/rooms/acl.go`, `internal/api/federation/acl.go`, `internal/federation/sender.go`, `tests/unit/serveracl_test.go`, `tests/integration/serveracl_test.go` |

## 6. Rooms, events and state (M2 in progress)

Protocol primitives first: none of this is reachable from an endpoint yet, but these are
the parts that must be byte-exact before any room is created.

| ID | Spec section | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-VER-001 | Room Versions §grammar | Room version strings are opaque, ≤ 32 characters, `[a-z0-9.-]` | MUST | implemented | `tests/unit/roomversion_test.go` |
| REQ-VER-002 | Room Versions §feature matrix | Per-version behaviour is selected from a registry: state resolution, event ID format, authorisation rule set, redaction profile, join-rule support, integer power levels, whether the event must be canonical JSON, `content.creator`, `content.redacts`, aliases as special-case state, domain-free room IDs | MUST | implemented (v1–v12 registered; versions 1 through 11 offered for new rooms; version 12 and the unstable versions recognised and refused) | `tests/unit/roomversion_test.go` |
| REQ-VER-003 | CS §Capabilities | Only versions this server can create rooms in are advertised in `m.room_versions.available` | MUST | implemented: the advertised set and the default both come from the registry, so a version whose rules are unimplemented cannot be advertised | `tests/integration/aliases_test.go` (`TestRoomVersionRangeIsWiderThanTheDefault`), `tests/integration/rooms_test.go` (`TestCreateRoomVersionNegotiation`), `tests/unit/roomversion_test.go` |
| REQ-VER-004 | Room Versions §v1, §v2, §v1–v5 "Deprecated event content schemas" | Room versions 1 and 2 are implemented as their own pages state, not as earlier spellings of the later rules: the authorisation table those two versions share (a room with no `m.room.join_rules` allows the join; membership rules keyed on the event's own domain rather than the room's; a redaction allowed by the moderator clause **or** by the redacted event's sender domain; an invite allowed for a moderator of the target's domain, and in version 2 by the target's own `leave`), State Resolution 1 in version 1, identifiers the origin server chooses, references written as `[event_id, {"sha256": hash}]` pairs, and power levels that may be written as strings or as floats to be truncated | MUST | implemented+tested | `tests/unit/authorisation_legacy_test.go`, `tests/unit/stateresolvev1_test.go`, `tests/unit/powerlevels_legacy_test.go`, `tests/unit/eventid_test.go`, `tests/unit/eventsignature_test.go`, `tests/integration/roomversionlegacy_test.go` |
| REQ-EVT-001 | SS §Signing Events (content hash) | Content hash = SHA-256 over canonical JSON with `hashes`, `signatures`, `unsigned` removed; stored unpadded base64 under `hashes.sha256` | MUST | implemented | `tests/unit/eventseal_test.go` |
| REQ-EVT-002 | SS §Signing Events (reference hash) | Reference hash = the event put through the redaction algorithm, then `signatures`/`unsigned` removed, then canonical JSON, then SHA-256 | MUST | implemented | `tests/unit/redaction_test.go` (goldens taken from gomatrixserverlib) |
| REQ-EVT-003 | Room Versions §Event IDs | Event ID = `$` + reference hash, standard unpadded base64 in v3 and URL-safe unpadded base64 in v4+; assigned by the origin server in v1–v2. Where the identifier is the hash, the receiving server names the event: an incoming event with no `event_id` is not incomplete, and one carrying a label its own content contradicts is not a second event, so the computed hash is used and recorded (DEC-036) | MUST | implemented+tested | `tests/unit/redaction_test.go`, `tests/unit/eventid_test.go`, `tests/integration/federation_test.go` (`TestEventOfferedWithoutItsLabelIsNamedByItsContent`) |
| REQ-EVT-004 | SS §Signing Events (signing) | Sign the **redacted** event so signatures survive redaction; record under `signatures.<origin>.<key id>` | MUST | implemented | `tests/unit/eventseal_test.go` verifies over the redacted form and checks event-ID stability |
| REQ-EVT-005 | SS §Signing Events (receipt) | `event_id`, `outlier`, `destinations`, `age_ts`, `unsigned` are not part of what is hashed; recomputing an identifier ignores the supplied `event_id` | MUST | implemented | `tests/unit/eventseal_test.go` |
| REQ-RED-001 | Room Versions §Redactions (v1–v10) | Strip every top-level key except `event_id`, `type`, `room_id`, `sender`, `state_key`, `content`, `hashes`, `signatures`, `depth`, `prev_events`, `prev_state`, `auth_events`, `origin`, `origin_server_ts`, `membership` | MUST | implemented | `tests/unit/redaction_test.go` |
| REQ-RED-002 | Room Versions §Redactions (v11+) | `origin`, `membership` and `prev_state` are no longer protected; `m.room.create` keeps its whole content; `m.room.redaction` keeps `content.redacts`; `m.room.power_levels` also keeps `invite`; a redacted `m.room.member` keeps `third_party_invite.signed` | MUST | implemented | `tests/unit/redaction_test.go` |
| REQ-RED-003 | Room Versions §Redactions (v9) | `content.join_authorised_via_users_server` is protected in v9+ but not in v8 | MUST | implemented | `tests/unit/redaction_test.go` |
| REQ-RED-004 | Room Versions §Redactions (v6) | `m.room.aliases` loses its special redaction/auth handling in v6 | MUST | implemented | `tests/unit/redaction_test.go` |
| REQ-RED-005 | Room Versions §Handling redactions | Apply a redaction only if the sender's power level is at least the redact level **or** the sender's domain matches the redacted event's sender domain | MUST | implemented+tested; the redact level is *not* part of the authorisation rules, and this server refuses at `PUT /redact` a redaction it would never apply | `tests/unit/redaction_rules_test.go`, `tests/integration/redaction_test.go` |
| REQ-RED-007 | Room Versions §Redaction algorithm, §Handling redactions | "Upon receipt of a redaction event, the server must strip off any keys not in the following list…" — each server strips its own copy, so federation carries the unredacted event plus the redaction; a redaction whose partner is absent is kept and re-checked when the partner arrives | MUST | implemented+tested: `events.Verify` checks the content hash unconditionally (a forwarded redacted form would be uncheckable and is refused), and a redaction is recorded as a link that persistence re-checks from either direction | `internal/events/verify.go`, `internal/rooms/redaction.go`, `tests/integration/redaction_test.go` |
| REQ-RED-008 | CS §Redactions | Serve a redacted event with a copy of its redaction under `unsigned.redacted_because`, and place the `redacts` field where the client's room version does not put it (`should`) | SHOULD | implemented+tested. One servicer (`internal/redaction/serve.go`) turns a stored row into what a client may see, and `/event`, `/messages`, state reads and `/sync` all go through it — they previously did not, which is how `/sync` came to serve an emptied event with no explanation while `/messages` explained it | `tests/unit/serve_test.go`, `tests/integration/redaction_test.go` (`TestSyncExplainsWhyAnEventItAlreadySawWentEmpty`) |
| REQ-RED-009 | CS §Redactions | Withhold from clients a redaction that has not been applied, until both it and its partner can be validated (`should`) | SHOULD | implemented+tested: an unapplied redaction is kept by the server and left out of what clients read — timeline pages, `/event` and `/sync`. `/event` answers it as not-found rather than admitting the event exists with its partner missing | `tests/unit/serve_test.go`, `tests/integration/redaction_test.go` (`TestARedactionIsNotShownToAClientUntilItsTargetArrives`) |
| REQ-RED-006 | Room version 12 §Redactions | Additional protection of relation information | MUST | not implemented — v12 currently reuses the v11 profile and is not offered | recorded, M6 |
| REQ-EVT-008 | SS §PDUs (required fields) | A PDU this server creates names its origin server in `origin` and its creation time in `origin_server_ts`. The name is what a peer matches signatures and a room's server exclusion against, and some rooms' auth rules read it; an event without it cannot be traced to whoever made it | MUST | implemented+tested. Locally written events were missing `origin` until this was recorded; the omission survived because this server verifies signatures against the sender's server name rather than the origin field | `internal/rooms/service.go` (`send`, `newStateEvent`), `internal/rooms/join.go`, `tests/integration/federation_test.go` (`TestLocallyWrittenEventsNameTheirOriginServer`) |
| REQ-EVT-006 | Room Versions §Event format | `auth_events` ≤ 10, `prev_events` ≤ 20, `depth` < 2^53−1 and equal to max(prev depths)+1 | MUST | not implemented | M2 event validation |
| REQ-EVT-007 | Appendices §Canonical JSON | In v6+ non-integer numbers and out-of-range integers make an event invalid (`M_BAD_JSON` on CS endpoints, discard over federation) | MUST | partial — canonical encoding is enforced, the per-version rejection rule is not wired up | M2 |
| REQ-CS-016 | CS §Events | `GET /rooms/{roomID}/event/{eventID}` returns one event to a requester entitled to read the room's history; an event in another room is answered as not found | MUST | implemented+tested | `tests/integration/redaction_test.go`, Complement `TestPowerLevels` |
| REQ-CS-017 | CS §Redactions | `PUT /rooms/{roomID}/redact/{eventID}/{txnID}` creates an `m.room.redaction`, places the target field by room version, and is idempotent per device transaction | MUST | implemented+tested | `tests/integration/redaction_test.go`, Complement `TestRedact` |
| REQ-STATE-001 | Room Versions §State resolution | State Resolution 1 for v1, State Resolution 2 for v2–v11, the revised algorithm for v12 | MUST | implemented+tested for v2–v11, **and consulted by the live path**: the state each event was sent against is recorded (T-3004a), and a room whose event graph has more than one forward extremity has its current state written from the resolved answer rather than from arrival order. v1 and v12 algorithms remain unimplemented (rooms of those versions are refused) | `tests/unit/stateresolve_test.go`, `tests/integration/statresolution_test.go` |
| REQ-STATE-003 | Room Versions §State resolution (confluence) | Servers holding the same fork must reach the same state; the answer may not depend on the order the branches were received in | MUST | implemented+tested: two homeservers receive the same fork in opposite orders and agree; the resolver is also checked against both argument orders directly | `tests/integration/statresolution_test.go`, `tests/unit/stateresolve_test.go` (`TestResolveNameForkIgnoresTheOrderTheStatesAreGiven`) |
| REQ-STATE-004 | Room Versions §State resolution (mainline ordering) | Order the conflicted non-power events by mainline position, then `origin_server_ts`, then event ID | MUST | implemented+tested, including the case that broke it: a decoded event's `origin_server_ts` is a `json.Number`, and reading it as another type yields 0 for every event, which silently replaces timestamp ordering with event-ID ordering | `tests/unit/stateresolve_test.go` (`TestResolveOrdersJSONDecodedEventsByTheirTimestamp`) |
| REQ-STATE-002 | Room Versions §State resolution / §Authorisation rules | An event whose `auth_events` cite an event this server rejected is itself rejected — the verdict about the first event has to outlive the event, which was not stored | MUST | implemented+tested; a gap (an auth event nobody has mentioned) is kept distinct from a verdict, and only the verdict is recorded | `tests/unit/rejections_test.go`, `tests/integration/federation_test.go` (`TestFederationRefusalIsAVerdict`) |
| REQ-AUTH-020 | Room Versions §Authorisation rules | Room-version-specific event authorisation as a subsystem separate from handlers (rules 1-10, room versions 3-11; room version 1 refused rather than approximated) | MUST | implemented | `tests/unit/eventauth_test.go` |
| REQ-AUTH-021 | SS §Auth events selection | `auth_events` must match the selection algorithm; duplicates, foreign-room events and rejected auth events invalidate the event | MUST | implemented | `tests/unit/eventauth_test.go` (`TestSelectAuthEvents`, rule 2 cases) |
| REQ-AUTH-023 | Rooms §Restricted rooms | A restricted join is only legitimate when the user is joined to a room named in `join_rules.allow` | MUST | partial — enforced for joins this server creates (`eventauth.MayJoinRestricted`), deliberately not inside `Check`, because the per-version auth rules do not evaluate the allow list and rejecting an event a peer's rules accept would split the room's history | `tests/unit/eventauth_test.go` (`TestMayJoinRestricted`) |
| REQ-AUTH-024 | Room Versions §Authorisation rules (aliases) | The historic `m.room.aliases` exemption applies only where the room version still has it | MUST | implemented (versions up to 5; ordinary state rules from version 6) | `tests/unit/eventauth_test.go` (`TestAuthRulesAliases`) |
| REQ-AUTH-025 | Room Versions §Authorisation rules | Knock, `restricted` and `knock_restricted` are unknown to room versions that predate them | MUST | implemented | `tests/unit/eventauth_test.go` (`TestAuthRulesKnockGating`, `TestAuthRulesRestrictedJoins`) |
| REQ-AUTH-022 | Room Versions §Authorisation rule 9 (v10+) | Integer-only power-level content validation | MUST | implemented (room version 9 and earlier keep their lenient behaviour, which the tests pin as a decision) | `tests/unit/eventauth_test.go` |
| REQ-TXN-001 | CS §Idempotency | Transaction IDs on `PUT /send/{type}/{txn}` are per-device and replay yields the same event | MUST | implemented, through the endpoint | `tests/unit/eventstore_test.go`, `tests/integration/rooms_test.go` |
| REQ-ROOM-001 | CS §Rooms | `POST /createRoom` with version negotiation, presets, initial state, `creation_content` restrictions, `room_alias_name` | MUST | implemented | `tests/integration/rooms_test.go`, `tests/integration/aliases_test.go` |
| REQ-ROOM-003 | CS §Sending events | `PUT /rooms/{roomID}/send/{type}/{txn}`: authorisation before signing, per-device idempotency, server-computed depth and references | MUST | implemented | `tests/integration/rooms_test.go` |
| REQ-ROOM-004 | SS §Signing events | An event this server produced re-verifies from storage: its ID is the hash of what is stored, it carries this server's `ed25519:v1` signature, and the authorisation rules accept it against the stored state and auth context | MUST | implemented | `tests/integration/rooms_test.go` (`TestCreatedEventIsVerifiable`) |
| REQ-ROOM-005 | CS §Joining, leaving, inviting, knocking | `POST /join/{roomIDOrAlias}`, `POST /rooms/{roomID}/join`, `leave`, `invite`, `kick`, `ban`, `unban`, `knock`; permission comes from the authorisation rules, not a per-endpoint check | MUST | implemented | `tests/integration/membership_test.go`; Complement `TestCannotKickNonPresentUser` **verified** |
| REQ-ROOM-006 | CS §Room aliases | `room_alias_name` creates the alias with the room in one transaction; `PUT/GET/DELETE /directory/room/{alias}`; `GET /rooms/{roomID}/aliases`; join by alias | MUST | implemented | `tests/integration/aliases_test.go`; Complement `TestRoomAlias` **verified** |
| REQ-ROOM-007 | CS §Room aliases | `m.room.canonical_alias` may only name aliases this server resolves to this room: bad syntax → 400 `M_INVALID_PARAM`; unresolvable or another room → 400 `M_BAD_ALIAS` | MUST | implemented | `tests/integration/aliases_test.go` (`TestCanonicalAliasValidation`) |
| REQ-ROOM-008 | Security | Reading a room's state, members, joined members or aliases requires a current membership; a left member loses the read | MUST | implemented | `tests/integration/aliases_test.go` (`TestRoomReadsNeedMembership`) |
| REQ-ROOM-009 | CS §Room versions | The versions offered for creation are those whose rules are implemented (7-11); registered-but-unoffered versions are refused, not substituted | MUST | implemented | `tests/unit/roomversion_test.go`, `tests/integration/aliases_test.go` |
| REQ-ROOM-010 | CS §State events | `PUT /rooms/{roomID}/state/{type}[/{key}]` including the empty state key; `GET` returns content, or the whole event with `format=event` | MUST | implemented | `tests/integration/rooms_test.go`, `tests/integration/aliases_test.go` |
| REQ-ROOM-011 | CS §Versioning | The legacy `r0` prefix is served alongside `v3` for the endpoints this server implements | SHOULD | implemented | `tests/integration/aliases_test.go` (`TestLegacyR0Paths`) |
| REQ-ROOM-012 | SS §Invites and alias resolution | Membership change for a user on another homeserver, and resolution of an alias owned by another server, are refused with 501 rather than pretended | MUST NOT (fake success) | implemented as an explicit refusal | `tests/integration/membership_test.go` (`TestMembershipErrors`) |
| REQ-ROOM-013 | CS §Room directory, upgrades | `GET /publicRooms`, `POST /rooms/{roomID}/upgrade`, `GET /rooms/{roomID}/event/{eventID}` | MUST | not implemented | M3 |
| REQ-ROOM-002 | Room Versions §room IDs | v12's domain-free room IDs and privileged creators | MUST | represented in the registry, not implemented | M6 |

### Interpretation decisions where the specification and implementations need reconciling

4. **Bootstrapping power levels (rule 7 versus rule 9.4).** Rule 9.4 allows the first
   `m.room.power_levels` event unconditionally, but rule 7 already demands
   `state_default` (50) for a state event, and with no power levels event in the room
   every user sits at `users_default` (0) — so nobody could ever send it. Synapse and
   gomatrixserverlib both resolve this by giving the room creator effectively unlimited
   power while the room has no power levels event (gomatrixserverlib uses 2^53−1 in
   `NewPowerLevelContentFromAuthEvents` and 99 in its privileged-creator path). This
   implementation gives the creator 2^53−1 in that situation and tests that a
   non-creator is still refused. Falsifiable: a peer that grants 99 instead would refuse
   a first event that sets a level above 99.
5. **`M_NOT_JSON` versus `M_BAD_JSON`.** A body that stops in the middle of a JSON
   value is not valid JSON and must be `M_NOT_JSON`; `M_BAD_JSON` is reserved for JSON
   that parsed but had the wrong shape. Go's decoder reports truncation as
   `io.ErrUnexpectedEOF`, which is not a `*json.SyntaxError`, so a naive check answered
   `M_BAD_JSON` for `{`. Tested in `TestRoomSendErrors`.
6. **Room preset contents.** The specification gives a table for the three presets, and it is
   normative: `private_chat` → `join_rule: invite`, `history_visibility: shared`,
   `guest_access: can_join`; `trusted_private_chat` → the same, with every invitee given the
   creator's power level; `public_chat` → `join_rule: public`, `history_visibility: shared`,
   `guest_access: forbidden`. This server writes that table, plus a power-level layout giving
   the creator 100 and `state_default` 50.
   **Corrected 2026-11-06.** An earlier entry here claimed the preset contents were
   implementation-defined and had `public_chat` start `world_readable`. That was wrong twice
   over: the table is in the specification, and its public row says `shared`. A public room is
   joinable without an invitation, which is not a statement that its history is readable by
   people who never joined; the old preset published the history on the strength of the join
   rule. Complement's `TestInboundCanReturnMissingEvents` counts the history-visibility events
   of a `public_chat` room and is right to. The same correction applies to guest access: the
   event is `m.room.guest_access` with a string `guest_access` key, and the directory read here
   had invented `m.room.guest_join` with a boolean.
7. **Empty state key routing.** `/state/{eventType}/` is the empty state key. Go's mux
   treats a pattern ending in `/` after a wildcard as a subtree, so that pattern also
   matches `/state/a/b/c`; the handler counts the remaining segments and refuses the
   longer paths with `M_INVALID_PARAM` instead of quietly treating them as an empty key.
9. **Kicking requires a present member.** The room version authorisation rules allow a
   `m.room.member` leave event for a user who has no membership, because the event is not
   invalid on its own. The Client-Server endpoint is where "you cannot kick someone who is
   not here" lives; sytest, Complement and Synapse all expect 403. It is enforced in
   `rooms.SetMembership` so `internal/eventauth` stays faithful to the room version rules.
10. **Alias resolution is unauthenticated.** `GET /directory/room/{alias}` answers without
    a token: it is how a client finds a room to join, and requiring a token would make the
    first join impossible. Creating and deleting an alias require a membership and, for a
    deletion by someone other than the alias creator, the room's kick power level.
11. **Membership changes for remote users are refused with 501.** Returning 403 would
    report a permission problem where the missing thing is federation, and returning 200
    would record state that no remote server was ever told about.
12. **Room aliases may hold opaque Unicode.** `#老虎£яඞ:hs1` is a legal alias; a bare `%`
    in the local part is refused because percent is the escape character. Complement tests
    both.
13. **`m.` event types are lowercase.** Client-supplied event types are validated: an
   `m.`-prefixed type may hold only lowercase letters, digits and underscores per
   component. Accepting `m.Room.Message` would create state and power-level entries no
   specification event ever matches. Custom types must be dotted, so two servers cannot
   invent the same name.
9. **`m.room.aliases` special case.** The specification removes it in room version 6 and
   Synapse gates it by version; gomatrixserverlib applies it to every room version. This
   implementation follows the specification, so an aliases event in a version 6+ room needs
   the sender to be joined and to hold the state power level. A peer applying the historic
   exemption would accept aliases events this server rejects — the kind of divergence that
   shows up as a missing alias rather than a forked room, and it is recorded here so it is
   not mistaken for a bug in the room-state code.

1. **`redacts` in room versions 3–10.** The per-version PDU tables list a top-level
   `redacts` property for redaction events, but the same pages' redaction strip list does
   not protect it, so the redacted form — hence the reference hash and event ID — drops
   it. gomatrixserverlib drops it; Synapse's Rust redaction implementation could not be
   read at the time of writing. This project follows the normative strip list, which also
   matches gomatrixserverlib. If a peer preserves the key instead, our event ID for a
   v3–v10 redaction event would differ from theirs; that is a falsifiable choice, checked
   as soon as redaction runs against Complement. Tracked in `.agent-work/BLOCKERS.md`.
2. **`format_version` in room versions 1–2.** Historic events carry it; the strip list
   does not protect it, so neither do we. v1/v2 rooms are not offered for creation.
3. **Transport keys on receipt.** `event_id`, `outlier`, `destinations` and `age_ts` are
   removed before hashing and signing, as both gomatrixserverlib and Synapse do; the
   specification states this in the receiving behaviour rather than in the hash
   definition.

### 6.5 Profiles (added during M3)

| ID | Source | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-PROFILE-001 | CS v1.19 `GET`/`PUT`/`DELETE /profile/{userId}[/{keyName}]` | Public reads; a write needs a token for that user; the body carries exactly one property, named as the path names; unknown `keyName`s refused when `m.profile_fields` is not advertised. | MUST | implemented+tested | `tests/integration/profile_test.go`, Complement `TestProfileDisplayName`, `TestProfileAvatarURL` |
| REQ-PROFILE-002 | Implementation convention, tested by Complement | After a profile change the homeserver restates the user's `m.room.member` event in every room they are joined to. | Convention | implemented+tested | `tests/integration/profile_test.go`, Complement `TestDisplayNameUpdate`, `TestAvatarUrlUpdate` |

`avatar_url` is stored as given: the specification makes the client responsible for a content URI, marks
server-side validation MAY, and Complement stores a bare string as an avatar while testing something else.
The identifier is parsed where it is acted on (a media fetch). The length bound is this server's own and is
refused `M_TOO_LARGE`. Full wording in `.agent-work/REQUIREMENTS.md`.

### 6.4 Spaces: a room pointing at rooms (added during M3)

| ID | Source | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-SPACE-001 | CS v1.11 v1 `/rooms/{roomId}/hierarchy` | The requested room followed by its descendants, depth-first; children ordered by the `m.space.child` link's `order` (well formed: printable ASCII, ≤50, no `/`), then the link's `origin_server_ts`, then room ID; `suggested_only`, `max_depth`, `limit`, `from` honoured; `next_batch` while rooms remain. | MUST | implemented+tested | `tests/integration/hierarchy_test.go`, Complement `TestClientSpacesSummaryJoinRules`, `TestFederatedClientSpaces` |
| REQ-SPACE-002 | CS v1.11 v1 `/rooms/{roomId}/hierarchy` | A room is listed only for a user who may see it; a restricted room names `allowed_room_ids`; the room the request is about is refused rather than answered with an empty list. | MUST | implemented+tested | `tests/integration/hierarchy_test.go`, Complement `TestRoomSummaryAllowedRoomIDs`, `TestRestrictedRoomsSpacesSummaryLocal` |
| REQ-SPACE-003 | FED v1.11 v1 `/hierarchy/{roomId}` | Answer another server with the room, a summary of each child **the asking server** could see, and `inaccessible_children` for those it could not. | MUST | implemented+tested | `tests/integration/hierarchy_test.go`, Complement `TestRestrictedRoomsSpacesSummaryFederation` |

The visibility decision is the same one the read endpoints make (public, world-readable, joined,
invited, or satisfying the room's restriction), including the rule that this server leans on its copy
of a room a restriction names only while it is still inside that room. A child held by another server is
described by that server and weighed, not believed: an answer that does not name the room asked about is
set aside, and the asking server still answers for its own user out of its own state. The full record,
including what the first attempt got wrong, is in `.agent-work/TASKS.md` (T-4051) and
`.agent-work/PROGRESS.md`.

## 7. Operational requirements

| ID | Source | Requirement | Level | Status | Tests |
| --- | --- | --- | --- | --- | --- |
| REQ-OPS-001 | AGENTS §13 Stage 1 | Configuration from file + environment, validated at startup, no secrets required to boot locally | MUST | implemented | `tests/unit/config_test.go` |
| REQ-OPS-002 | AGENTS §13 Stage 1 | Graceful shutdown on SIGINT/SIGTERM, bounded by `shutdown_timeout` | MUST | implemented | `tests/integration/lifecycle_test.go` |
| REQ-OPS-003 | Complement image contract | `SERVER_NAME` honoured; ports 8008/8448; healthcheck; entrypoint re-runnable | MUST | implemented | `deploy/complement/*`, Complement deployment succeeds in every run above |
| REQ-OPS-004 | AGENTS §10 | Storage migrations run at startup; SQLite via gormlite; transactions for multi-row invariants | MUST | implemented | `tests/unit/storage_test.go` |
| REQ-OPS-005 | AGENTS §10 | Single-process storage: SQLite is documented as the M1 storage, not the final answer | MUST NOT (assume) | partial | one writer connection; scalability limits recorded in `ARCHITECTURE.md` |
