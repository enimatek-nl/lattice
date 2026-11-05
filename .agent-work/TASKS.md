# Tasks

Status legend: `[ ]` pending · `[~]` in progress · `[x]` done (verification recorded in PROGRESS.md)

## M0 — Stage 0 (research/architecture)

- [x] T-0001 Read AGENTS.md + autonomous-development + backend skills; inventory installed skills → PROJECT-ASSESSMENT.md
- [x] T-0002 Pin Matrix spec version (v1.19, 2026-07-08) → SPECIFICATION-VERSION.md
- [x] T-0003 Clone & analyse Complement (layout, deployment contract, env vars, PKI, image rules)
- [x] T-0004 Read M1-relevant Complement tests as behavioural evidence (register/login/logout/versions/unknown endpoints/capabilities)
- [x] T-0005 Survey reference implementations (Synapse, Dendrite, gomatrixserverlib, conduwuit) → REFERENCE-ANALYSIS.md
- [x] T-0006 Architecture incl. event DAG, state sets, event auth, room versions, federation, storage → ARCHITECTURE.md
- [x] T-0007 Requirements + compliance tracking + milestones + acceptance criteria

## M1 — Foundation + identity layer

- [x] T-1001 `go mod init`, dependency selection (gormlite/gorm/x-crypto), Makefile, .gitignore, README
- [x] T-1002 `internal/config`: YAML+env+flags, validation, defaults — tests: `tests/unit/config_test.go`
- [x] T-1003 `internal/logging`: slog JSON, level, secret redaction — tests: `tests/unit/logging_test.go`
- [x] T-1004 `internal/matrixerr`: codes + writer + mapping of internal errors — tests
- [x] T-1005 `internal/matrixjson`: canonical JSON (spec vectors), number rules — tests
- [x] T-1006 `internal/matrixjson`: sign/verify JSON (spec 7-step, multi-sig) — tests
- [x] T-1007 `internal/matrixid`: server name, user ID, room/alias/event IDs, base64, MXID split — tests
- [x] T-1008 `internal/httpserver/router.go`: method-prefixed mux + probe mux, 404/405 `M_UNRECOGNIZED` — tests
- [x] T-1009 `internal/httpserver`: middleware chain (recover/reqid/log/limit/timeout), TLS + plain listeners, graceful shutdown — tests
- [x] T-1010 `internal/persist`: DB open (PRAGMAs), migrations, models (users, devices, access tokens, keys, uia nonces), repositories — tests
- [x] T-1011 `internal/keys`: ed25519 generate/load/persist, key id, signed server key document — tests
- [x] T-1012 `internal/users`: registration policy, bcrypt hashing, localpart downcasing, validation — tests
- [x] T-1013 `internal/auth`: token mint/verify/revoke, UIA sessions/flows, HTTP middleware — tests
- [x] T-1014 `internal/app`: wiring root; `cmd/lattice`: flags, signal handling, `healthcheck` subcommand
- [x] T-1015 CS API: `GET /versions` (+unstable_features) — tests
- [x] T-1016 CS API: `POST/GET /register` (UIA, dummy flow), `M_INVALID_USERNAME`/`M_USER_IN_USE`, `device_id` echo, `inhibit_login` — tests
- [x] T-1017 Compat: `GET/POST /_synapse/admin/v1/register` shared secret (HMAC-SHA1 nonce) — tests
- [x] T-1018 CS API: `GET/POST /login`, `POST /logout`, `POST /logout/all`, `GET /devices`, `GET/PUT/DELETE /devices/{id}`, `GET /account/whoami` — tests
- [x] T-1019 CS API: `GET /capabilities` (auth required, `m.room_versions`, `m.change_password`) — tests
- [x] T-1020 Discovery: `/.well-known/matrix/client` (+ server when configured) — tests
- [x] T-1021 Federation: `GET /_matrix/federation/v1/version`; keys: `GET /_matrix/key/v2/server`, `POST /_matrix/key/v2/query` — tests
- [x] T-1022 Media stub `POST /_matrix/media/v3/upload` (501 `M_UNKNOWN`) so 405 semantics hold
- [x] T-1023 `tests/integration`: end-to-end register→whoami→login→devices→logout over real listeners (plain + TLS)
- [x] T-1024 `deploy/complement/Dockerfile` + entrypoint (PKI, SERVER_NAME, shared secret) + `scripts/complement.sh`
- [x] T-1025 Run Complement M1 subset against the image; record results in PROGRESS/COMPLIANCE
- [x] T-1026 `make lint` (golangci-lint if available) / `go vet` clean
- [x] T-1027 Update docs (`docs/*`), compliance matrix, FINAL-REPORT.md

## M1.5 — hardening found while closing M1

- [x] T-1101 Race between `Serve`'s listener handoff and `CloseListeners` (found by `-race` in the new PKI test) → mutex-guarded listener table; `make verify` now runs `-race`
- [x] T-1102 CA-signed federation certificates (`tlsgen.EnsureSignedByCA`, `LATTICE_TLS_CA_*`) instead of shell/openssl in the entrypoint
- [x] T-1103 `SERVER_NAME` alias, `registration_mode` startup field, forced-shutdown path (`Shutdown` timeout → `Close` + warning)
- [x] T-1104 Makefile (verify/build/test/test-race/cover/lint/docker/complement/generate-config) — `make verify` is the milestone gate
- [x] T-1105 `golangci-lint run ./...` clean

## M2 — rooms and events (next milestone)

- [x] T-2000 Read Room Versions + event authorization + event graph sections of v1.19 for
      v9/v10/v11; requirement rows recorded in `docs/requirements.md` §6 (22 rows) and the
      interpretation decisions documented there and in `BLOCKERS.md`
- [x] T-2001 `internal/roomversion`: v1-v12 registry (state resolution v1/v2/v2.1, event ID
      format, authorisation rule set id, redaction profile, join-rule support, integer power
      levels, key validity, canonical JSON strictness, `m.room.aliases` special casing,
      `content.creator` vs sender, `content.redacts` vs top level, domain-free room IDs,
      privileged creators) + grammar validation + `Supported`/`Available` —
      `tests/unit/roomversion_test.go`
- [x] T-2011 `internal/redaction`: version-specific strip lists and per-type content allow
      lists — `tests/unit/redaction_test.go`
- [x] T-2012 `internal/events`: content hash, reference hash, event ID derivation per room
      version, `Seal` (hash then sign the redacted form), transport-key stripping; goldens
      compared with gomatrixserverlib for v3/v10/v11 — `tests/unit/eventseal_test.go`
- [x] T-2002 Event storage: `rooms`, `events` (canonical JSON verbatim + indexed
      columns, NULL vs empty `state_key`, redacted/outlier/soft_failed, global
      `stream_ordering`), `event_edges` (prev/auth), `room_state`,
      `event_auth_context`, `event_txn`; forward-extremities query; dedup via unique
      event id; per-device transaction ids — `tests/unit/eventstore_test.go`
- [x] T-2003 `POST /createRoom` (presets, `room_version` negotiation, `creation_content`
      limits, initial state, name/topic/avatar shorthand, alias request refused) plus
      `/capabilities` now sourced from `roomversion.Default()`/`Available()` —
      `internal/rooms`, `tests/integration/rooms_test.go`
- [x] T-2004 `internal/eventauth`: authorisation rules 1-10 for room versions 3-11 (v1/v2
      refused), auth-events selection and its validation, version-gated knock/restricted/
      knock_restricted, v10+ integer power-level validation, the historic `m.room.aliases`
      exemption, creator power bootstrap, and `MayJoinRestricted` for the restricted-room
      allow list — `tests/unit/eventauth_test.go`
- [x] T-2005 `internal/state`: State Resolution 2 for room versions 2-11 — unconflicted/
      conflicted split, auth difference, power events widened by their auth chains, reverse
      topological power ordering, mainline ordering, iterative auth checks that refuse an
      event whose auth event was rejected; v1 and v2.1 refused rather than approximated —
      `tests/unit/stateresolve_test.go`
- [ ] T-2005a Cross-check state resolution against gomatrixserverlib's resolver with shared
      fixtures (same fork, same expected winner) before any federation work relies on it
- [x] T-2006 `PUT /rooms/{roomId}/send/{eventType}/{txn}` with per-device transaction
      deduplication, server-computed depth/prev_events/auth_events, authorisation before
      signing, sealing and persistence; plus `PUT /rooms/{roomId}/state/{type}[/{key}]`
      (empty state key supported, deeper paths refused) —
      `tests/integration/rooms_test.go` incl. `TestCreatedEventIsVerifiable`
- [x] T-2006a Persist the outcome of the on-receipt checks: `event_rejections` records the
      reason a PDU was refused, `dbSnapshot.Rejected` and `citedSnapshot.Rejected` read it,
      and rule 2.3 is asked before the lookup so "seen and refused" is not answered as
      "never heard of it". A refusal that was only a gap (missing predecessors, an auth
      event nobody has mentioned, an unknown room) is not recorded — DEC-021 — and an ID
      this server holds is never recorded, so a stranger's claim under a good event's ID
      cannot turn that event into a rejected one —
      `tests/unit/rejections_test.go`, `tests/integration/federation_test.go`
      (`TestFederationRefusalIsAVerdict`: forged PDU → refused and remembered; an event
      citing it → refused naming the refusal; a gap → refused and *not* recorded)
- [x] T-2007 membership events: join (both spellings, by room ID or local alias),
      leave, invite, kick, ban, unban, knock; the permission decision stays in the
      authorisation rules; remote-user membership refused with 501 —
      `tests/integration/membership_test.go`, Complement `TestCannotKickNonPresentUser`
- [x] T-2008 (client side) `GET /rooms/{roomID}/state`, `/state/{type}[/{key}]` with
      `format=event`, `/members` with `not_membership`, `/joined_members`,
      `/joined_rooms`, `/aliases`; reads require a current membership —
      `tests/integration/aliases_test.go`. `/state_ids` is federation-side and still M4
- [x] T-2011 room aliases: `room_alias_name`, `PUT/GET/DELETE /directory/room/{alias}`,
      join by alias, canonical-alias validation (`M_BAD_ALIAS`), Unicode aliases —
      Complement `TestRoomAlias` verified
- [x] T-2008 see above (client-side state reads done; `/state_ids` is federation, M4)
- [x] T-2009 `m.room.redaction` application: the redact-level or same-domain rule
      (`PowerLevels.MayRedact`), the redacted form stored beside the received bytes so the
      content hash stays checkable, and the link kept when the partner has not arrived yet so
      either half can arrive first — `internal/rooms/redaction.go`,
      `tests/unit/redaction_rules_test.go`, `tests/integration/redaction_test.go`;
      Complement `TestRedact/Event_content_is_redacted` now passes
- [x] T-2009a withhold from clients a redaction this server has not applied (the
      specification's `should`: "they should not be sent to clients until both the redaction
      event and the event the redaction affects have been received, and can be validated").
      The links existed; now the read paths consult them. An unapplied redaction is left out
      of `/messages` pages and the sync timeline, and `/event` answers it as not found
- [x] T-2009b attach `unsigned.redacted_because`, and place `redacts` for the client's room
      version, in `/sync` as well. Both halves landed in one place — `redaction.Servicer` —
      because the drift was the bug: `/messages` explained an emptied event and `/sync` did
      not. Any new read path goes through the servicer, including `/context` when T-2015
      builds it
- [x] T-2010 room upgrade path: `POST /rooms/{roomId}/upgrade` and a client-supplied
      `predecessor` in `creation_content`, with the transferable state carried, the aliases
      moved, the tombstone sent, and each local user's room rules carried across —
      Complement `TestPushRuleRoomUpgrade`'s two local subtests
- [ ] T-2010b decide what an upgraded room is allowed to do afterwards: it still accepts
      events today, although its own state says it is not intended to be used any further.
      Refusing them is a behaviour change with consequences for clients that have not seen
      the tombstone, so it is a decision rather than a default
- [ ] T-2010c upgrade awareness for rooms this server is not in: a tombstone arriving over
      federation should carry the local users' rules too. The hook already fires for any
      tombstone this server persists, so the work is the federation path (T-4005), not the
      hook,
      including carrying the user's push rules over to the new room — Complement
      `TestPushRuleRoomUpgrade` has two local subtests that fail for nothing else: the
      endpoint does not exist. The other two subtests of that test need federation

- [x] T-3001 `GET /sync` — done, including stored and inline filters, position tokens,
      `limited`/`prev_batch`, ephemeral and account data, the `leave`
      section bounded at the membership event that took the user out; `unread_notifications`
      and `highlight_count` are reported as zero until the push rules are evaluated (T-3005a).
      Complement `TestSyncFilter`, `TestSyncSendersFilter`, `TestSyncLeaveSection`,
      `TestGappedSyncLeaveSection`, `TestOlderLeftRoomsNotInLeaveSection`,
      `TestArchivedRoomsHistory`, `TestLeaveEventVisibility` and
      `TestCumulativeJoinLeaveJoinSync` pass. An earlier line here claimed `/sync` was the
      single largest blocker with a list of tests that has since changed; that line is
      replaced rather than kept beside the current one

- [x] T-3001a history paging: `GET /rooms/{roomID}/messages` in both directions, `end`
      omitted when nothing further is readable, lazy-loaded member state —
      Complement `TestSendAndFetchMessage`, `TestRoomMessagesLazyLoading*`
- [x] T-3001b the room directory: `GET`/`POST /publicRooms` with search and paging,
      `GET`/`PUT /directory/list/room/{roomID}`, `visibility` on `/createRoom`, and
      `GET /v1/room_summary/{roomIdOrAlias}` — Complement `TestPublicRooms`,
      `TestRoomState`, `TestRoomSummaryAllowedRoomIDs`
- [x] T-3001c presence: `GET`/`PUT /presence/{userID}/status`, `set_presence` on `/sync`,
      the `presence` section, and the join that makes one user's presence another user's to
      see — Complement `TestPresence` and both presence subtests of `TestSync`
- [ ] T-3001e presence that goes quiet on its own: an idle timeout, so a user who stops
      asking does not stay `online` forever. This needs a policy decision (how long, and
      whether a sync alone counts as activity), so it is a task rather than a guess
- [ ] T-3001f the `presence` section of a sync filter, which is not applied to the section
      this server sends
- [ ] T-3001d depth-ordered history paging: order by depth then stream position and widen
      the token to carry both, so a room whose events arrive out of depth order pages the
      way the reference server pages it (Complement `TestLeftRoomFixture`)
- [x] T-3004a per-event state snapshots: `state_groups` / `state_group_entries` /
      `event_state_groups`, identified by a hash of their entries so a linear room stores one
      snapshot per state change and a fork shows up as two groups with a common ancestor —
      `internal/persist/stategroups.go`, `internal/rooms/stateat.go`,
      `tests/unit/state_groups_test.go`, `tests/integration/stategroups_test.go`.
      A merge of disagreeing snapshots is resolved by the algorithm (T-2017)
- [ ] T-3004 per-event state snapshots, so a read can be answered as of a past position:
      a former member's old state value, `state_at_event`, and the state a joining user is
      given
- [x] T-3005 push rules: the ruleset and its defaults, the whole read/write API under
      `/pushrules`, and delivery to the client through `account_data` in `/sync` —
      Complement `TestPushRuleCacheHealth`, `TestPushSync`
- [x] T-3005i rule evaluation: `pushrules.Evaluate` — kind priority order, the master rule
      first, every condition kind in the specification including the word-boundary and
      escaped-dot cases, unrecognised conditions never matching, and the user's own events
      never notifying. Unit-tested; not wired into anything yet
- [ ] T-3005a make the rules mean something: evaluate them when an event is persisted so
      `unread_notifications` and `highlight_count` in `/sync` are real, then pushers, the
      push gateway POST and `GET /notifications`. Until this lands the counts a client is
      told are zero, which is recorded rather than asserted. The evaluator exists
      (T-3005i); what is missing is the read marker that makes "unread" mean something,
      then the per-event lookup each condition needs (display name, member count, power
      levels) and the storage of the outcome
- [x] T-3008 `device_lists` in `/sync`: `changed` on a join, `left` on a leave or ban,
      recorded with the room it happened in — Complement
      `TestSync/parallel/Device_list_tracking` (including the retry case)
- [x] T-3008b the keys themselves: `POST`/`PUT /keys/upload`, `POST /keys/query` (with
      `since`), `POST /keys/claim` (once only, in upload order) — Complement `TestUploadKey`,
      `TestKeyClaimOrdering`, `TestUploadKeyIdempotency`, `TestUploadKeyIdempotencyOverlap`
      and the local subtests of `TestDeviceListUpdates`
- [ ] T-3008d `GET /keys/changes?from=&to=`, which answers the same question a
      `device_lists` section answers, for a window a client names
- [ ] T-3008e cross-signing and key backup: `POST /keys/device_signing/upload`,
      `POST /keys/signatures/upload`, `/room_keys/*`. Without them a user cannot verify a
      device or recover their history on a new one
- [ ] T-3008f fallback keys (MSC3288): refused today with a reason. Accepting them means
      storing a key that outlives the session it started, which needs a decision about how
      long the server keeps what cannot be single-use
- [ ] T-3008c device list changes triggered by a key upload, a new device or a
      cross-signing update, once those exist; only membership triggers one today
- [ ] T-3001 (leftover numbering, same work as T-3001a-d and T-4005 above): what still
      fails at a `/sync` step is presence, `device_lists`, MSC4222 `state_after`, and the
      two-server cases that need federation
- [x] T-3002 the sync-dependent assertions in `TestLogout` and `TestDeviceManagement` pass

## M4 — federation foundation (started)

- [x] T-4001 `internal/federation/auth.go`: the X-Matrix credential — build and check.
      `origin`/`key`/`mac`, the MAC over method+uri+body signed as signing JSON, the
      destination check, the body limit before any parsing, and the rule that a signature
      by a key the sender does not publish is no signature at all —
      `tests/unit/fedauth_test.go`
- [x] T-4002 `internal/federation/target.go`: server name → address. `.well-known/matrix/server`
      delegation, `_matrix._tcp` and `_matrix-federation._tcp` SRV records, IP literals and
      explicit-port names skipping delegation, the certificate name staying the name that
      was asked for (except for an IP delegate), caching with a TTL, and a broken delegation
      treated as no delegation rather than as permission —
      `tests/unit/federationdiscovery_test.go`
- [x] T-4003 `internal/federation/ippolicy.go`: which addresses this server will dial.
      Deny-by-default over loopback, the unspecified address, link-local (instance metadata),
      CGNAT, multicast and the private ranges; a configured allow list narrows further and a
      deny entry still wins inside it; a rule that cannot be parsed stops startup —
      `tests/unit/federationdiscovery_test.go`, `tests/unit/config_test.go`
- [x] T-4004 `internal/federation/keyring.go`: other servers' signing keys. Fetch direct,
      then notaries; a document is believed only when the server it names signed a key it
      publishes; the trust window is clamped to a week whatever the document claims; retired
      keys are usable until their own `expired_ts`; expiry is enforced only for the room
      versions that enforce it; a cached answer is reused instead of refetched —
      `tests/unit/federationkeys_test.go`
- [x] T-4005 `internal/federation/client.go` + `sender.go`: the outbound half. Signed
      transactions, an on-disk queue with per-destination batching and a claim that cannot
      double-send, exponential retries bounded by age and attempts, a remote's per-PDU
      refusal recorded as a refusal rather than retried forever, and a worker whose lifetime
      is the process's — `tests/integration/federation_test.go`
- [x] T-4006 `PUT /_matrix/federation/v1/send/{txnId}`: the inbound half. Authenticated
      before a byte of the body is read, the body's `origin` required to agree with the
      credential, the PDU and EDU caps enforced, transactions remembered and answered
      identically when replayed, and each PDU answered by event ID with its own reason —
      `tests/integration/federation_test.go` incl. the forged, tampered, wrong-destination,
      unknown-key, origin-mismatch, oversize and replay cases
- [x] T-4007a `rooms.Service.AcceptRemoteEvent`: the receiving checks in the specification's
      order — event validity, event ID against the hash of its own content, the content hash
      it carries, the origin server's signature, authorisation against the auth events the
      sender cited; a room introduced by a remote `m.room.create` is adopted with the room
      version that create event asked for — `tests/integration/federation_test.go`
- [x] T-4007b `GET /_matrix/federation/v1/event/{eventId}`: hands an event to a server the
      room's own state says is present, and to nobody else —
      `tests/integration/federation_test.go`
- [x] T-4007c soft-failure recorded on ingest: an event its own branch authorises but the
      room's current state does not is stored, marked, kept out of what this server relays
      and out of the forward extremities, and still answered to peers over `/event` —
      `internal/rooms/remote.go`, `internal/persist/event.go`
- [ ] T-4007 missing-event and backfill fetch: an event whose predecessors or auth events
      this server does not have is refused today with the reason named, instead of the gap
      being fetched from the sender. Until it exists a handover must arrive in causal order
- [ ] T-4008 EDUs are received, acknowledged and logged, and nothing else happens to them:
      presence, typing, receipts and device-list units over federation are their own work,
      and the outbound `EnqueueEDU` path has no caller yet
- [x] T-4009a the resident half of the join handshake: `/make_join` (v1 and v3) hands over an
      unsigned template with the room's version and refuses a user the room does not permit;
      `/send_join` v2 checks the signed event against what its URL claims, accepts it, signs it
      as well, and answers with the pre-join state and its auth chain; `/state_ids` and
      `/event_auth` answer servers the room has a seat for —
      `tests/integration/federationjoins_test.go`. Found on the way: auth rule 4.3.1 refused a
      join whose single `prev_event` could not be resolved, and `prev_events` are never among
      the auth events a receiver is handed, so every remote join but the creator's own was
      refused (`tests/unit/eventauth_test.go`)
- [x] T-4009b the joining half: `POST /join/{roomID}` of a room held elsewhere now asks a named
      server for the join template, refuses to sign anything that is not its own user's join in
      that room, seals and names the event itself, hands it to `/send_join`, and stores what
      comes back as a snapshot — each event verified on its own, the bundle applied in dependency
      order, an event accepted without its predecessors kept as an outlier, and a missing citation
      treated as a gap rather than a verdict (`internal/rooms/bundle.go`, DEC-028). The client's
      repeated `server_name`/`via` parameters are read and used as the servers to ask.
      `tests/integration/joinremote_test.go`: a user on hs2 joins, is listed by hs1, holds the
      room's state on hs2, speaks in it, and is heard on hs1; a join nobody answers invents no
      room; a forged event in a bundle is refused and remembered. Found on the way: `App.Rooms`
      was never assigned, so the assembled app exposed no room service at all
- [x] T-4009b1 fetch what a handed-over room cites and lift the outlier flag: the holes are named
      from the room's own events (`persist.MissingPredecessors`), asked of a server that was in the
      room first, newest-first, walked back event by event until they meet what this server holds,
      ingested as one bundle ordered parents-first so this server's own ordering follows causality
      and not arrival, and re-linked by `rooms.ResolveOutliers`. Bounded by a per-call event budget
      and pass count; a bounded fetch leaves the rest named and askable, which
      `TestBackfillBudgetLeavesAGapAskable` pins (DEC-030). A joined room's history is now readable,
      and a message sent after the join cites the join, which cites the room's last message, so the
      branch is connected rather than forked
- [ ] T-4009b2 the other triggers and the answering side: a bounded backfill pass in the federation
      worker for rooms with gaps, filling on demand when a client pages a timeline that stops at a
      gap, and answering `GET /backfill` ourselves (REQ-FED-001). Only a join triggers a fetch today
- [x] T-4009b3 ask for the predecessors an incoming event cites when this server does not hold
      them. The room service now returns a `rooms.MissingPredecessorsError` naming the events, which
      is not a verdict but a request for the caller to go and get them; the transaction handler asks
      the server that just offered the event (it plainly has them), ingests what comes back parents
      first, and re-offers the event once. Evidence is the server-ACL test: messages withheld during
      an exclusion are still missing when the exclusion is lifted, the next message is refused for
      its hole, and after the ask both halves are held here and read in the order they happened.
      `GET /get_missing_events` is not implemented — the ask goes over `GET /event`, which is a
      legitimate route to the same events (REQ-FED-016, DEC-033)
- [x] T-4016 `m.room.server_acl` is enforced (REQ-FED-012, DEC-031): the join template (v1 and v3),
      `/state_ids`, `/event_auth`, `/event`, and each PDU of a transaction answer an excluded server
      with 403 `M_FORBIDDEN`; a room's events are not relayed to it; events from an excluded server
      are refused where they are accepted, and the refusal is recorded. A malformed ACL is not
      applied, and a test says so rather than leaving the choice to be rediscovered
- [ ] T-4012 apply the room's server ACL on the endpoints this server does not answer yet —
      `/make_leave`, `/send_leave`, `/backfill`, `/get_missing_events` — as each of them is built
      (REQ-FED-012). A check that is written once in the room service and called by each endpoint is
      what keeps the last of them from being forgotten
- [ ] T-4009c the rest of the handshake family: `/make_leave`, `/send_leave`, `/invite` (v1/v2),
      `/make_knock`, `/send_knock`, `/event_auth` as a *client* of another server, and applying
      `m.room.server_acl` to the join, state, auth and per-PDU paths (REQ-FED-012)
- [x] T-4010 `Sender.Cleanup` and the worker's hourly pass: answered transactions past their
      replay window, key documents past their trust window and queue rows given up on for a
      month are forgotten, while everything still current survives —
      `tests/unit/federationqueue_test.go` (which also caught that `SeenTransaction` accepted
      an instant and ignored it, so an answer kept past its expiry was replayed as if current)
- [x] T-4006a signed responses are answered in the form their signatures cover: the server
      key document and the key-query response are encoded as canonical JSON, and
      `TestServerKeysVerifies` asserts the body is its own canonical form and that the
      signature checks over the bytes sent — Complement `TestInboundFederationKeys` passes
      (it failed while the endpoint re-encoded through Go's JSON encoder)
- [x] T-2017 make the room's current state the result of the state resolution algorithm
      rather than of arrival order. An event that joins branches, or a room left with more
      than one forward extremity, is now resolved with the algorithm the room version names
      and the answer written back as current state (`internal/rooms/resolve.go`,
      `internal/rooms/stateat.go`). Found on the way: `state.eventTimestamp` could not read a
      `json.Number`, so every decoded event looked like it happened at time 0 — the
      mainline ordering silently dropped its timestamp comparison and settled forks by event
      ID, which is not what a timestamp-ordering server would conclude.
      `tests/integration/statresolution_test.go` hands the same fork to two homeservers in
      opposite orders; `tests/unit/stateresolve_test.go` pins the ordering and its independence
      of argument order
- [ ] T-3004b state snapshots for rooms that predate T-3004a: their events have no group, so a
      fork in such a room cannot be resolved from history — this server logs and leaves the
      current state alone rather than rebuilding it from a guess. A backfill pass (or a
      documented upgrade note) is what closes it; fresh rooms are unaffected
- [ ] T-3004c measure what resolution costs on a forked room: `reconcileCurrentState` runs
      over the forward extremities on every persist while a room is forked, which is correct
      and unmeasured. Before any deployment with real forks this needs a benchmark and, if it
      is too slow, the state-at-read-time design (Synapse's state groups plus a lazily
      computed current state)

- [x] T-2014 `GET /rooms/{roomID}/event/{eventID}`: one event by ID through the same history
      access rule as `/messages`; an event that lives in another room is answered as not
      found, so the endpoint does not report which room holds an ID. Complement's
      `SendEventSynced` uses it, which is why `TestPowerLevels` now passes as well —
      `tests/integration/redaction_test.go`
- [ ] T-2015 `GET /rooms/{roomID}/context/{eventID}` (`event`, `events_before`, `events_after`,
      `state`), which `GET /event` and `/messages` already have the parts of
- [x] T-2016a redaction as a client action: `PUT /rooms/{roomID}/redact/{eventID}/{txnID}`
      (the verb the specification and Complement use), which places the target field where the
      room version puts it — top level up to v10, in `content` from v11 — and refuses a
      redaction this server would never apply rather than storing one that destroys nothing:
      `tests/integration/redaction_test.go`, Complement `TestRedact`
- [ ] T-2016b `GET /rooms/{roomID}/event_auth/{eventID}` and
      `GET /rooms/{roomID}/timestamp`: still absent; `/timestamp` also wants depth-based
      paging (T-3001d)
- [x] T-4013 room versions 1 and 2 are offered for new rooms, with the rules those versions
      actually have rather than the later versions' rules applied early: their shared authorisation
      table (`eventauth.RulesV1V2` — join rule ignored for a room with no `m.room.join_rules`, the
      membership clauses keyed on the event's own domain, the redaction moderator-or-same-domain
      clause, the invite clauses for the target's domain and for a target already gone), State
      Resolution 1 in version 1 (`internal/state/stateresolutionv1.go`), identifiers the origin
      server chooses (`roomversion.EventIDServerAssigned`, `events.MintEventID`, and minting in the
      outbound join template and in `rooms.prepare`), references written as
      `[event_id, {"sha256": hash}]` pairs (`events.References`/`ReferencedIDs` are now
      version-aware, and the graph walk and auth-chain walk read through them), the redaction
      profile those versions use, and the power level forms the pre-canonical-JSON versions allow
      (`LoadPowerLevels` reads `"100"`, `" 00100 ",` `"+25"` and `50.57`). `Supported()` now returns
      versions 1–11, with 1 and 2 last; version 12 and the unstable versions stay recognised and
      refused.
      tests: `tests/unit/authorisation_legacy_test.go`, `tests/unit/stateresolvev1_test.go`,
      `tests/unit/powerlevels_legacy_test.go`, `tests/unit/eventid_test.go`,
      `tests/unit/eventsignature_test.go` (now signs and verifies in every offered version, with
      each version's reference shape and its identifier order), `tests/unit/roomversion_test.go`,
      `tests/unit/chain_test.go`, `tests/unit/eventauth_test.go`,
      `tests/integration/roomversionlegacy_test.go` (rooms created in versions 1 and 2, their events
      named `$opaque:hs1`, every fetched reference a pair whose hash recomputes to the referenced
      event's real reference hash, and a federation join handshake into a version 2 room),
      `tests/integration/aliases_test.go`, `tests/integration/rooms_test.go`.
      outstanding: room version 12's revised state resolution and the unstable versions'
      pseudo-identifier format (T-4019); the Synapse divergence on forked version 1 rooms is
      recorded in `docs/compliance-matrix.md` item 5 and is not interoperability-tested
- [ ] T-4011 the address policy is applied when discovery resolves a name, not when the
      socket is opened. A name whose answer changes between the two (DNS rebinding) is not
      covered; the fix is a dialer that re-checks the address it is about to connect to
- [x] T-4018 an event offered over federation is named by the server that receives it, not by the
      offer (DEC-036). Opened as "pseudo-IDs", and the diagnosis was wrong: the event format of every
      room version from 3 on does not list `event_id` among the required properties, because the
      identifier there is the reference hash — so a PDU without one is complete, and a PDU carrying a
      label its content contradicts is the same event wearing a wrong label. `events.AssignEventID`
      computes the name; the ingest paths use it for the duplicate check, the verdicts and the stored
      copy; `Verify` no longer demands a field the room version does not require. Evidence:
      `tests/unit/eventid_test.go` (every known room version, including 1 and 2 where the identifier
      really is the sender's to supply) and `tests/integration/federation_test.go`
      (`TestEventOfferedWithoutItsLabelIsNamedByItsContent`: an unlabelled event and a mislabelled
      one, each stored under the ID its content makes and readable by it). Complement's
      `TestSyncTimelineGap` passes now
- [~] T-4017 the Complement failures that remain in the sets this project has been running.
      The full `./tests` + `./tests/csapi` suite was run on 2026-11-08 as a baseline: 60 top-level
      passing, 136 failing. In the rooms/sync/federation area the picture is: `TestRoomCreate`
      (including `creates_a_room_with_the_given_version`, the room version 2 ask), `TestSync`,
      `TestSyncTimelineGap`, `TestRoomState`, `TestRedact`, `TestPresence` and
      `TestServerCapabilities` all pass. What still fails there is the device-list tracking case —
      `TestDeviceListUpdates` and `TestSync/parallel/Device_list_tracking` — which needs device-list
      EDUs over federation (T-4009c), and `TestSyncOmitsStateChangeOnFilteredEvents`, which needs
      sync filtering (T-4020).
- [ ] T-4019 room version 12 and the unstable versions: the revised state resolution (v2.1 — the
      conflicted-subgraph and empty-start cases Complement's MSC4297 tests exercise) and the
      pseudo-identifier event format. The rules are registered, so these versions are recognised and
      refused honestly today; implementing them is what turns the refusal into support.
- [ ] T-4020 sync filtering (`/filter`, filter IDs, the lazy-loading and event-field limits), which
      is what `TestSyncOmitsStateChangeOnFilteredEvents` asks for and what several clients assume.
- [ ] T-4021 `PUT /upgrade` accepts any version this server *knows* (`roomversion.Get`) rather than
      any it can actually run (`roomversion.Supported`): a room upgraded into version 12 or an
      unstable version lands in a state this server cannot resolve. Offer only the supported set,
      and refuse the rest with `M_UNSUPPORTED_ROOM_VERSION` as creation already does.
- [ ] T-4014 audit the remaining federation responses for the same mistake as T-4006a: any
      object that carries a signature must be written as the bytes that signature covers.
      `pdus` are already carried as raw stored bytes; the transaction answer and the error
      bodies are unsigned, so they are unaffected today
- [ ] T-4012 `Sender.MaxAge`/`MaxAttempts` and the queue depth are configured but not exposed
      to an operator: no admin endpoint and no metric reports a queue that will never drain
