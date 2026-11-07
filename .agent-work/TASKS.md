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
- [x] T-4022 device-list changes over federation: send `m.device_list_update` EDUs to the other
      servers in the rooms a local user shares when that user's devices or keys change, accept them
      from a peer, and surface what they say into `/sync` for the local users watching. The
      transaction transport already carries EDUs and the per-observer device-list record exists
      (DEC-017), so this is the two ends and the trigger, not a new subsystem.
      This is now the single remaining cause of the device-list Complement failures:
      `TestDeviceListUpdates`'s five two-server subtests and
      `TestDeviceListsUpdateOverFederationOnRoomJoin` all reach a `/sync` wait that nothing answers.
      The piece in front of them — fetching a remote user's device keys from their own server — was
      implemented on 2026-11-08 and those subtests moved from failing in 0.4s to reaching the sync
      step 5s later. `GET /client/v3/keys/changes` (the other way a client can learn) is also absent;
      if the EDU work covers the tests, that stays a client-convenience gap rather than a blocker.
      Done 2026-11-08: announcements go out on a key upload (the trigger already recorded the local
      half) and when a local user joins a room held elsewhere, and received announcements are passed
      to the local observers who share a room with the user named — only when the sender is entitled
      to speak for that user. `TestDeviceListUpdates` (all ten subtests) and
      `TestDeviceListsUpdateOverFederationOnRoomJoin` pass; the remaining device-list Complement
      failure, `TestDeviceListsUpdateOverFederation`, stops at `createRoom` because it invites a
      remote user, which is the missing outbound invite (T-4009c), not a device-list gap.
- [x] T-4023 what is still missing around device lists: announcing a *deleted* device
      (`deleted: true` — a logout removes a device and its keys, and a peer's clients keep tracking
      it until told otherwise), cross-signing key changes once cross-signing exists, and
      `GET /client/v3/keys/changes`, which is how a client can ask directly rather than waiting to
      be told. Also unanswered: `master_keys`/`self_signing_keys` in the federation key answer,
      because this server does not store cross-signing keys at all.
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
- [x] T-4021 `PUT /upgrade` accepted any version this server *knows* (`roomversion.Get`) rather than
      any it can actually run: a room upgraded into version 12 or an unstable version would exist,
      take events, and be a room this server cannot work out the state of. `roomversion.IsOffered`
      now answers the question in one place and the upgrade path uses it, so a recognised-but-
      unimplemented version is refused `M_UNSUPPORTED_ROOM_VERSION` exactly as creation refuses it.
      tests: `tests/integration/upgrade_test.go` (`TestRoomUpgradeErrors`: `new_version: "12"`),
      `tests/unit/roomversion_test.go` (known ≠ offered). Found while offering room versions 1 and 2,
      which is what widened the hole: the offered set grew, and the upgrade doorway had never been
      limited to it.
- [ ] T-4014 audit the remaining federation responses for the same mistake as T-4006a: any
      object that carries a signature must be written as the bytes that signature covers.
      `pdus` are already carried as raw stored bytes; the transaction answer and the error
      bodies are unsigned, so they are unaffected today
- [ ] T-4012 `Sender.MaxAge`/`MaxAttempts` and the queue depth are configured but not exposed
      to an operator: no admin endpoint and no metric reports a queue that will never drain

## Opened by the invite handshake (2026-11-08)

T-4009c is split rather than closed: the invite half is done (see PROGRESS 2026-11-08, commit
`fb11648`), the other halves below are not.

- [ ] T-4023 `unsigned.prev_content` and `unsigned.prev_sender` on state events. A client answering
      an invitation needs to know what it was invited *as*, and a room that has changed hands needs
      to show the change: Complement's `TestFederationRoomsInvite` ("Invited user has 'is_direct'
      flag in prev_content after joining") reads `unsigned.prev_content.membership` and
      `unsigned.prev_sender`. Design: the previous content is a fact about persistence, so compute it
      when a state event is written (the current state row for the same type and state_key, before
      the new one lands), store it beside the event, and inject it into `unsigned` when the event is
      served to a client. It must not enter the stored bytes a signature covers, and it is not a
      federation field.
- [x] T-4024 `is_direct` on invites and the `m.direct` account data. `createRoom` ignores the flag
      today, so a room created as a direct message is not recorded as one anywhere. Needed by the
      same Complement case, and by any client that groups DMs.
- [ ] T-4025 Ban, kick and leave for a user who lives on another homeserver. Still refused with 501
      (`rooms.ErrRemoteTarget`). This is the rest of T-4009c: the event has to be authored by the
      server that holds the room and carried to the other side, or authored by the other side and
      brought here, depending on the change. Blocks `TestUnbanViaInvite` and the kick cases in
      `TestFederationRoomsInvite`.
- [ ] T-4026 Find out why a peer refused to re-verify our `invite_room_state`. Before invites were
      let through, hs2 answered `M_INVALID_PARAM: A state event sent along with the invite does not
      carry the signature its origin owes`. Those events are signed by our own server in a room whose
      keys the peer had just fetched, so the likely causes are the ref-pair form of `prev_events` and
      `auth_events` in a re-served PDU, or redaction handling in `events.Verify`. The invite path now
      skips such events instead of refusing, which keeps interoperability but hides the fault; the
      log line names each event that fails.

- [x] T-4023, T-4024 are done (PROGRESS 2026-11-08, commit `fcc4dae`). Two gaps opened by that
      work:
- [x] T-4027, T-4028, T-4029 are done together (PROGRESS 2026-11-07, the commit that adds
      `tests/integration/accountdata_test.go`), because a removal that does not reach `/sync` and a
      room-scoped value nobody can read are the same store problem: account data needs a scope
      (user, or user+room) and a removal that is itself a position in the stream.
      Store: `account_data` gains `room_id` (empty means the user's own scope) and `deleted`, and the
      unique index becomes `(user_id, room_id, data_type)` — the old `(user_id, data_type)` index is
      dropped, because keeping it would let one row per user per type survive the widening. Reads of
      a removed row answer `M_NOT_FOUND`; a removal writes a row rather than deleting one, so it
      carries a stream position an incremental sync can point at. `internal/users/accountdata.go`
      decides removal from the request: an object with no members *is* the removal (MSC3391), so
      `PUT {}` and `DELETE` both tombstone and both answer `{}`.
      Sync: one read of both scopes per sync, split into the top-level list and a list per room; a
      removed type is carried to a client that has something to forget (`since != 0`) as an entry
      with `{}` content and omitted for an initial sync, where there is nothing to forget. A room
      whose only change is its room account data is now included in the response — otherwise the
      value is written and never delivered.
      Paths: the short form `/rooms/{roomID}/account_data/{type}` and the MSC3391 nesting
      `/user/{userID}/rooms/{roomID}/account_data/{type}`, both under `v3` and `r0`, plus the
      unstable `/_matrix/client/unstable/org.matrix.msc3391` spellings. Both spellings of the room
      form are not decoration: Complement's `MustSetRoomAccountData` writes through the *user*-nested
      path at the stable prefix, so a server that registers only the short form answers a spec-writing
      client `M_UNRECOGNIZED`. Where the path names the user the caller must be that user; where it
      does not, the access token names them.
      Detail worth keeping: `m.direct` (which is account data, and which room DM clients depend on)
      now reads a removed row as an empty map rather than as a value that was deleted from nowhere.
      GORM detail that cost a test: `Assign(struct)` applies update semantics and skips zero fields, so
      clearing a tombstone left `deleted = true`; the write passes a map instead.
      Verification: `tests/integration/accountdata_test.go` (round trip through the room scope, and
      six removal dialects — empty PUT, stable DELETE, unstable MSC3391 DELETE, user and room scopes —
      each asserted as GET 404, absent from an initial sync, present with `{}` in an incremental one);
      Complement `TestAddAccountData` (both subtests), `TestRemovingAccountData` (all four subtests)
      green, both red before (`.agent-work/next/complement-run-acctdata4.log`).

## M2/M3 gate run (2026-11-08, `.agent-work/next/gate.log`: 19 pass, 13 fail)

Ordered cheapest-verified-win first. Evidence lines are from Complement sources at the checkout in
`~/complement`.

- [x] T-4030 `/login` accepts a client-supplied `device_id`. Complement's txnid tests log in with a
      chosen device ID and we answer `M_INVALID_PARAM "Device ID …"` (`txnid_test.go:147`, `:233`).
      Fails `TestTxnScopeOnLocalEcho`, `TestTxnIdWithRefreshToken`,
      `TestTxnIdempotencyScopedToDevice` at the first step. Validate the shape, reuse the device if
      it already belongs to that user, refuse one that belongs to somebody else.
- [x] T-4031 A reused transaction ID with a *different* body is a new event.
      `TestTxnIdempotency` (`txnid_test.go:211`): the fourth send re-used the third's txn ID with
      different content and got the cached event back. Synapse's uniqueness is
      (user, device, txn, body); ours is (device, txn). Same test class: 5 tests.
- [x] T-4035 the full-suite contamination was leaked Podman **volumes**, not an immovable lock
      ceiling (B-0008, 2026-11-06). 2044 anonymous volumes from removed containers had eaten the
      whole 2048-lock pool; clearing the ones no live container mounts restored Complement to
      working, and `TestMembershipOnEvents` then ran in 3.6s. `scripts/complement.sh` now reports
      the anonymous-volume count and names the cleanup instead of running it, and defaults to
      `-p 2`. Until a full suite is re-run on a clean store, `gate4.log` (59/161) and `full2.log`
      (67/152) are both to be treated as noise, not tallies.
- [x] T-4039 re-run the full suite with a clean volume store and record the tally as a number with
      a date, replacing the two contaminated ones. Expect it to take tens of minutes; expect
      `msc3902` (`TestPartialStateJoin`) to fail rather than hang, and separate unimplemented MSCs
      from real regressions by family, not by count.
      Do not read `.agent-work/next/gate4.log` (59 pass / 161 fail) as a compliance picture: 15+ of
      those failures are `ContainerCreate … exceeded num_locks (2048)` at deploy time, on tests that
      pass in filtered runs. Reliable until then: filtered family runs. Possible fixes, none tried
      blind: a non-destructive way to grow the rootless lock set; running the suite package by
      package with `podman container prune -f` between packages; or a Docker-backed host. Do not run
      `podman system reset` on this machine without asking — it has unrelated containers on it.
- [x] T-4032 (done 2026-11-06, commit `ad8499a`). `unsigned.membership` is the **reader's**
      membership of the room as of the moment each event was sent, read from the snapshot that
      event was sent against; `leave` when the snapshot holds no member entry, no field when this
      server has no snapshot for the event at all, and a value already under `unsigned` is left
      alone because that is where a peer records what it added. `redaction.Servicer` gained the
      state-group repository and the reader-aware entry points `ServeAs`/`ServeChunkAs`; `/sync`
      timeline and state, `/messages` and `/event` pass the reader, and the reader-free
      `Serve`/`ServeChunk` remain for callers with no reader to name. Complement
      `TestMembershipOnEvents` passes (`.agent-work/next/msc4115c.log`), with
      `TestGetRoomMembers`, `TestGetRoomMembersAtPoint`, `TestSendAndFetchMessage`,
      `TestRoomMessagesLazyLoading(+LocalUser)`, `TestFetchMessagesFromNonExistentRoom`,
      `TestSyncFilter` and `tests/csapi` `TestSync` unchanged and green
      (`.agent-work/next/msc4115d.log`).
      Two things it left behind, both tasks rather than footnotes: `ServeChunk` had been pairing
      events with rows by `len(out)`, so after any withheld event every later event in the chunk
      carried the previous event's `unsigned` — fixed by pairing by position, **not yet covered by
      a test** (T-4041); and `/state`-style reads do not carry the field, which MSC4115's scope
      does not clearly reach (T-4038).
      The steps below are kept because they record why the lookup is shaped the way it is.
- [x] T-4033 (half). `handleMembers` read only `not_membership`; `membership=` was ignored. Fixed
      in the commit after `7157a39`: `TestGetFilteredRoomMembers` passes all three subtests
      (`not_membership`, `membership/leave`, `membership/join`).
- [x] T-4033a `/members?at=<sync token>` — done in commit `066bb6c`. The roster is read from the
      state snapshot the newest event at or before the token was sent against
      (`events.EventsWindow` → `groups.GroupOf` → `groups.Entries`), which is the same snapshot the
      authorisation rules judged that event by. Empty by that token gives an empty roster; a token
      whose snapshot was never recorded is refused rather than answered from today's state.
      The other half of the fix was `prev_batch` (commit `2dcda9a`): `/sync` named the **oldest**
      event of the batch, so a client asking about the point its own batch began was told a roster
      from before the room had any members. It now names the newest end, which is what Synapse
      issues and what Complement expects. The Client-Server API does not settle this —
      matrix-org/matrix-spec#595 has been open on it since 2020 — so the decision and its cost (a
      client may meet an event twice paging back) are written into the comment at
      `internal/sync/service.go`. Complement `TestGetRoomMembersAtPoint` passes, and the sync and
      message families re-run unchanged.
      Follow-on worth its own look, not assumed: the `at` resolution and MSC4115's
      `unsigned.membership` (T-4032) need the same "state at a stream position" mechanism; the
      snapshot lookup now exists and should be reused rather than rewritten.
- [ ] T-4036 typing is not implemented on the client-facing side, and the ledger claimed
      otherwise until 2026-11-06. `/sync` builds `ephemeral` as an empty list by construction
      (`internal/sync/service.go:320-330`); typing EDUs arrive over federation
      (`internal/api/federation/transactions.go`) but nothing stores or replays them. Complement
      `TestTyping` fails all three delivery subtests, consistently, twice on 2026-11-06
      (`msc4115d.log`, `typing1.log`): `MustSyncUntil: timed out after 5.0s`. The work is a
      per-room ephemeral store with an expiry (a typing notification is a claim that is true for
      ~30s, not history), feeding `/sync` `ephemeral.events`, `PUT /rooms/{roomID}/typing`, and
      forwarding to joined servers. Receipts (T-?) share that store.
- [x] T-4041 the `ServeChunk` row/event misalignment is covered (2026-11-06).
      `TestServeChunkKeepsEachEventWithItsOwnUnsigned` in `tests/unit/serve_test.go` serves a chunk
      of a message with a transaction ID, a redaction whose target is not stored (withheld), and a
      topic with `PrevContent`; it asserts the surviving events carry their own `unsigned` and only
      their own. Verified to fail against the old pairing — the topic event comes back with no
      `prev_content` because it got the message's row — and to pass against the fixed one.
      Worth knowing rather than pretending otherwise: `ServeChunk`/`ServeChunkAs` have **no
      production callers** — every read path serves events one at a time — so the bug was latent,
      and the test pins an API that is not yet on any live path. That is T-4040.
- [ ] T-4040 put the read paths on the chunk API, and take the cost of MSC4115 down with it.
      `/sync` (timeline per room, state per room) and `/messages` serve events one at a time
      through `Serve`/`ServeAs`, so each event pays for its own lookups: the redaction-link read,
      and now two more per event for `unsigned.membership` (the snapshot entry, then the member
      event itself). `ServeChunkAs` exists precisely so those are batched — one
      `RedactionLinks` for the chunk instead of one per event — and it is currently called by
      nothing outside its own tests. Batching the membership lookups needs either one query per
      chunk over the event IDs or a per-chunk cache keyed by (snapshot, state_key); measure the
      sync path before and after rather than assuming it is slow, and note that a chunk is where
      the withholding decision is *also* better made (a client seeing half of a redaction pair in
      one timeline is the failure `Withheld` exists to prevent).
- [x] T-4038 (done 2026-11-06, commit `134b25c`) `unsigned.membership` on the remaining
      client-facing read paths. Settled by reading the specification rather than another server: the
      property is defined on the **client event format** — `ClientEventWithoutRoomID.unsigned.membership`,
      added in Matrix 1.11, matrix-org/matrix-spec#1847 — so it is not an endpoint list. The value is
      the requesting user's `m.room.member` state at the point of the event "including any changes
      caused by the event", and `leave` where they had none; the one documented place it is not
      populated is events pushed to an application service, where there is no requesting user.
      `rooms.Service.eventJSON` is now explicitly the reader-free helper for calls that ask an event
      a question for themselves (join rule, power levels, canonical-alias bookkeeping, member counts,
      the room directory — no reader, and a per-event snapshot lookup there would be wasted work);
      `/state`, `/state/{type}/{key}`, `/members` and the at-point roster go through `eventJSONFor`.
      `TestServedStateAndHistoryCarryTheReadersMembership` covers the shape — `/state` answering
      `leave` for the room's creation and `join` for a topic written after the reader joined, a
      history page walking `leave` -> `invite` -> `join` — and fails when the field is switched off.
      Complement unchanged by the threading: `TestMembershipOnEvents`, `TestGetRoomMembers(AtPoint)`
      and `tests/csapi` `TestSync` (`msc4115state.log`).
- [x] T-4043 (closed without doing it, 2026-11-06) does an identical state write make a new event?
      It does here, and it should: making it not so would have broken the case it was opened for.
      The event this task was opened on was not a no-op write at all — it was the walk's own
      `latest_events` ceiling being returned as part of the answer (T-4046), and Complement's
      `world_readable` subtest, which now passes, *expects* a second `m.room.history_visibility`
      event when the value actually changes. Leaving this alone is the correct outcome; the question
      is closed rather than parked. If no-op state writes ever need suppressing, it is a decision
      about what a client's `PUT` means and needs its own task, not a side-effect of this one.
- [ ] T-4048 the `public_chat` preset writes `m.room.guest_access: forbidden`; Complement expects
      no such event. The specification's preset table for `createRoom` gives
      `public_chat` → join_rule `public`, history_visibility `shared`, guest_access `forbidden`, and
      a room with no guest-access event defaults to `can_join`, so the table can only be honoured by
      writing the event. Synapse does not: its preset carries `guest_can_join`, and
      `_send_events_for_new_room` emits `m.room.guest_access` **only when that is true** — so a
      Synapse `public_chat` room has no guest-access event at all, and Complement's
      `TestInboundCanReturnMissingEvents` counts its expected events around that absence (measured in
      `synapse/handlers/room.py`, develop, 2026-11-06). The consequence today: that test's four
      subtests are three green and one off by exactly this event. Kept because the specification
      outranks another server's preset, per AGENTS §2. Two follow-ups: check what Complement's guest
      access tests expect of a `public_chat` room when guest support exists here (T-4049), and
      revisit if evidence shows the table's `guest_access` column is descriptive rather than
      normative.
- [ ] T-4049 guest access is not implemented as a module. The state event is now written and read
      for the directory's `guest_can_join`, but there is no `m.guest` registration, no guest
      access-token handling, and no "a guest may read a `world_readable` room without joining" path —
      which is also what the room-preview and `/initialSync`-style flows depend on. Task it when the
      Client-Server surface it belongs to is next.
- [x] T-4046 (done 2026-11-06, commit `5a381a4`, and the diagnosis in its title was **wrong**) what
      `TestInboundCanReturnMissingEvents` called an extra event was `get_missing_events` answering
      with its own ceiling. The record here first read the byte array Complement prints — a second
      `m.room.member` for the joiner, at depth 8 — as proof that a federated join stored the
      membership twice. It did not. `TestFederatedJoinLeavesOneMemberEventForTheRemoteUser`
      reproduces the same handshake between two local servers and finds exactly one member event for
      the remote user, and both servers name the join event identically, which is the point of
      content-derived event IDs. The actual bug was in `EventsBetween`: an event named in
      `latest_events` was queued as the starting point and then appended to the answer as well. The
      question a peer asks is "what are the previous events of these", so the ceiling is descended
      from and not returned. Worth keeping the lesson next to the correction: the byte array was
      real evidence about *what* was extra and no evidence at all about *why*, and the two-server
      reproduction settled it in one run where reading the log had sent an investigation off to the
      join handshake.
- [x] T-4050 (done 2026-11-06) the full-suite tally, measured. `RUN='.*' scripts/complement.sh -v`
      at this Complement checkout: **77 top-level passing, 142 failing, 1 skipped**
      (`TestSendJoinPartialStateResponse`), every package including the MSC ones
      (`.agent-work/next/tally-full-20261106.log`). The names are in
      `complement-failures-20261106.txt` and `complement-passes-20261106.txt`, which is what future
      runs should be diffed against — the earlier numbers in this ledger (a run reported as "136
      failing" several cycles ago) are not comparable, because the Complement checkout and the
      package set have moved. Note also what tripped the first attempt: `scripts/complement.sh` with
      no `RUN=` is a **seven-test filter**, so a green run without `RUN=` means seven tests passed,
      not the suite. T-4051 is the follow-up that matters: work the failure list down, largest
      common cause first (federation outbound and join flows dominate it), and diff against this
      baseline each time.

- [x] T-4052 (2026-11-08, commit `dd2b096`) the restricted-room flow. `rooms.RestrictedJoin` decides
      the allow condition and picks the authoriser from this server's own joined members at or above
      the invite level; `Opts.AllowUnsignedTemplate` covers `/make_join`, where the template cannot
      carry a signature; `/send_join` re-checks the conditions and that the named authoriser is one
      this server can sign for, and answers 400 `M_UNABLE_TO_GRANT_JOIN` when it cannot; the joining
      server stores the event the resident server handed back rather than its own copy, which is what
      carries the resident signature rule 4.2 needs. Three underlying bugs went with it: a client
      could name its own authoriser; `m.room.member` was gated at `state_default`, locking ordinary
      users out of their own membership events; and `WithTx` dropped the services attached after
      construction. Evidence: `TestRestrictedRoomsLocalJoin`, `...LocalJoinInMSC3787Room`,
      `...LocalJoinNoCreatorsUsesPowerLevelsV11`, `TestRestrictedRoomsRemoteJoin`,
      `TestRestrictedRoomsRemoteJoinInMSC3787Room` and
      `TestKnockRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11` moved from failing to passing,
      with no test moving the other way.

- [ ] T-4053 what the restricted-room cluster still owes. After `cc7ec65` the cluster is
      `LocalJoin`, both MSC3787 variants, `LocalJoinNoCreatorsUsesPowerLevelsV11`, `RemoteJoin`
      (+MSC3787), `RemoteJoinLocalUser` (+MSC3787) and `RemoteJoinFailOver` (+MSC3787) passing.
      What is left, in the order worth taking it:
      * ~~`TestRestrictedRoomsSpacesSummaryLocal` / `...Federation`~~ — done in `9535c65`; the room
        hierarchy is implemented on both listeners and both tests pass. That was T-4051.
      * `TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV12` — refused at `createRoom` with
        `M_UNSUPPORTED_ROOM_VERSION`. Room version 12 is in the registry but not offered for
        creation. That is a policy decision this project has not made: read
        `roomversion.supported`, the pinned specification's room-version section and what v12 changes
        about room identifiers before deciding, and record the decision rather than flipping a flag.
      * ~~The two MSC3787 restricted-join variants flip between runs~~ — not a Lattice fault, and the
        cause written here first ("delivery, not decision … the T-4007 gap") was **wrong**: it read a
        log line that was present in passing as the reason, without checking it. The real cause is the
        test environment handing out a stale address for a peer server name; see T-4056. With the
        environment cleaned, the whole `TestRestrictedRooms*` cluster passes except the room version 12
        case above.
      Note: an earlier version of this task claimed MSC3787 needed the invite power of *the allow
      room* rather than the restricted room to decide an invitation. That was never checked against
      the specification, and the tests it was meant to explain (`RemoteJoinLocalUser` and its MSC3787
      variant) pass on the restricted room's own invite level. Do not implement the claim.

- [x] T-4054 fan-out ordering, then prompt delivery. Done in `9ce20d5`; see PROGRESS for what was
      found. The sender wakes when work is queued; the queue claim is atomic; a neighbour's refusal
      is retried instead of ending an event's journey; a refusal filed against an event ID is dropped
      when the genuine event arrives; unobtainable signing keys are a question, not a verdict; a
      failed key fetch is retried in two seconds rather than waited out for a minute. Full run after
      it: **84 / 135 / 1**, nothing newly failing, and four restricted-room tests newly passing
      (`NoCreatorsUsesPowerLevelsV11`, its knock variant, `RemoteJoin`, its MSC3787 variant).
      Still failing in that cluster, in order of what they need: `TestRestrictedRoomsRemoteJoinFailOver`
      and its MSC3787 variant (hs1 grants a restricted join for a user who has just left the allow
      room on another server — hs1 has not been told the leave yet, so the answer is stale rather
      than wrong; the fix is asking the neighbour for the allow room's current state when the
      membership it needs is absent, or refusing on a known gap, not weakening the failover),
      `TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV12` (we refuse to create version 12
      rooms: a policy decision that has not been taken), and the two `SpacesSummary` tests (T-4051). Three findings, from today's runs:
      (a) The federation sender polls the queue and sleeps two seconds when it finds nothing, so an
      event this server accepts can sit unsent for two seconds. Complement's restricted-room tests act
      seconds later and read the neighbour's stale state: `TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11`
      asks hs2 to authorise a join with power levels hs2 has not been told about yet.
      (b) Cutting that idle to 250 ms, and waking the sender on every enqueue, made delivery prompt and
      made **our own** integration suite flaky: four to seven federation tests fail in a run, with
      `auth rule 2.3: auth event … was rejected on receipt` in the join path. Both were reverted; the
      note is on `defaultSenderIdle`. So eager fan-out currently hands a neighbour events out of order
      — most likely a room's state pushed at a server that is mid-join, or a later event in one
      transaction and its predecessor in another. Find the actual ordering failure before shortening the
      idle again; `TestFederationSenderQueue`, `TestRemoteUserJoinsARoomHeldElsewhere` and
      `TestDeviceListChangesCrossTheFederationLink` are the ones that flip.
      (c) `TestRestrictedRoomsRemoteJoin` and its MSC3787 variant pass with a two-second fan-out only
      because hs2 authorises the join locally. Once the invitation level is 50 (T-4053's preset fix),
      hs2 has no local authoriser and fails over to hs1 — and hs1 still thinks a user who left the
      allow room is in it, so a join that must fail succeeds. This is (a) with a consequence: the
      restricted-room pair is now flaky rather than passing by accident. Do not fix it by weakening the
      failover.

- [x] T-4055 what a full run looked like on 2026-11-09 after `3a0cba4`: **80 passing, 139
      failing, 1 skipped**, superseded by the 2026-11-10 run above (84 / 135 / 1). Against the previous full run, `TestRestrictedRoomsRemoteJoinLocalUser`
      and `TestRestrictedRoomsRemoteJoinLocalUserInMSC3787Room` newly pass, and
      `TestRestrictedRoomsRemoteJoin`, its MSC3787 variant and `TestDeviceListUpdates` newly fail — the
      restricted pair for the reason in T-4054(c), and `TestDeviceListUpdates` because it is load-flaky
      (it passes when run on its own). Treat `TestRestrictedRooms*` results as unstable until T-4054 is
      done, and re-tally after it.

- [x] T-4051 (done 2026-11-11, commit `9535c65`) the room hierarchy, on both listeners. The task ID had
      been referenced in three earlier entries without an entry of its own; it is recorded here, closed,
      so the references point at something.
      `GET /_matrix/client/v1/rooms/{roomId}/hierarchy` walks a space depth-first in the order its own
      `m.space.child` links give (`order` when well formed, then the link's timestamp, then room ID),
      lists a room only for a reader who may see it, honours `suggested_only`, `max_depth`, `limit` and
      `from`, and refuses the room the request is about with 403 rather than answering an empty tree.
      `GET /_matrix/federation/v1/hierarchy/{roomId}` answers a peer about what that *peer* could see:
      the room, a summary per visible child, and `inaccessible_children` so the peer does not ask again.
      The room service's `Hierarchy`/`HierarchyQuery` share one summary builder and the same visibility
      rules used by the read endpoints, including the allow-room currency rule from the restricted-join
      work: this server leans on its copy of a room the restriction names only while it is inside it.
      Remote answers are weighed, not believed — an answer that does not name the room asked about is
      dropped. Tests: four in `tests/integration/hierarchy_test.go`. Complement:
      `TestRestrictedRoomsSpacesSummaryLocal`, `TestRestrictedRoomsSpacesSummaryFederation`,
      `TestClientSpacesSummaryJoinRules` and `TestFederatedClientSpaces` newly passing.
      What was wrong in the first attempt is worth keeping: the room service had no way to ask a peer at
      all (`SetRemoteHierarchy` unwired), so the remote child was simply absent and a `want 1` assertion
      could not tell absence from a refusal. A hierarchy test that only counts rooms cannot fail that.

- [x] T-4056 (done 2026-11-11, `scripts/complement.sh`) the two MSC3787 restricted-join Complement
      tests flip — **the cause is the test environment, not this server**, and no protocol behaviour was
      changed for it.
      What was observed: `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room` failed in about half the
      runs, alone or in a cluster, always at `bob.MustSyncUntil(SyncJoinedTo(@user-3:hs3, room))` with
      bob's sync showing `rooms: {}`; `...RemoteJoinInMSC3787Room` failed in filtered cluster runs with
      404 "room is not known to this server" for rooms the same deployment had made minutes earlier.
      What the log of a failing run showed, in hs2's own container: `certificate is valid for hs2, not
      hs1` while dialing `https://10.89.0.2:8448` for `hs1` — that is hs2 dialing **itself** and
      correctly refusing hs2's certificate when it was asked for hs1. Two joins then returned 500
      ("no server in the room would take the join"), so bob never stood in the restricted room, so his
      sync never showed charlie in it: the sync timeout was the last symptom of a name that resolved to
      the wrong machine.
      The mechanism, checked rather than assumed: rootless podman serves every network from one
      `aardvark-dns` process driven by one file per network under
      `$XDG_RUNTIME_DIR/containers/networks/aardvark-dns/`. A deployment's network was removed and its
      file stayed; the live probe showed `getent hosts hs1` answering **10.89.12.2** while the running
      hs1 container was **10.89.0.4**, and the leftover file
      `complement_msc3902_1_servers` named 10.89.12.2 for `hs1`. So a homeserver container could be
      pointed at an address from a deleted deployment — some other test's hs2, or nothing. `podman
      network prune` does not clear it: the network is already gone, so there is nothing to prune.
      Why nothing was changed in Lattice: the two rules that look implicated are both right as
      implemented. Dialling the resolved address while requiring a certificate for the *name* is what
      the Server-Server name-resolution section says, and it is what caught the wrong machine — a
      permissive behaviour here would have made a genuinely misdelivered federation request look
      successful. Trying every address a name resolves to instead of the first allowed one is a real
      hardening idea for the future, but it would not have helped here: DNS returned exactly one
      address, and it was wrong.
      What was changed instead: `scripts/complement.sh` now reports, before running anything, any
      aardvark-dns record file whose network no longer exists, with the commands that clear it in a
      comment. Deliberately reported and not cleaned automatically, for the same reason the leaked
      volumes are reported and not deleted: it is another project's container state on somebody else's
      machine.
      Verification: with the orphan removed and aardvark restarted, `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room`
      passed three consecutive filtered runs (5.0–5.8 s, never the 17–18 s failure shape), the whole
      `TestRestrictedRooms` cluster passed except the room version 12 case, and a full clean run gave
      **90 passing, 129 failing, 1 skipped** with nothing newly failing. The earlier 89/130 tally was
      taken while the stale record was live, so numbers before this entry sit on a dirtier environment
      than numbers after it.

- [x] T-4057 (done 2026-11-11, commit `192c3dc`) the profile endpoints, and the member events that have
      to hear about a change. `GET /_matrix/client/v3/profile/{userId}` and
      `GET`/`PUT`/`DELETE /_matrix/client/v3/profile/{userId}/{keyName}` (with the `r0` prefixes), the
      `m.set_displayname` / `m.set_avatar_url` capabilities, and `persist.User` columns for the two fields.
      The endpoint is the small half. A room presents a person through the `m.room.member` event it holds,
      which carried a copy of the profile at the moment it was written, so a change that only touches the
      account row leaves one human named two ways in one room — and a room that was told once is never
      told again. `users.Service.SetProfile`/`ClearProfile` therefore call
      `rooms.Service.RefreshProfile` through a `ProfileBroadcaster` interface, which sends the membership
      again in every room the user stands in as an ordinary event under that room's own rules: a room that
      will not take it keeps what it had, one refusing does not stop the others, and the account's own
      change is not undone because somebody else's server was unreachable. The room layer reads the
      account layer back through `ProfileReader` (`users.Service.ProfileFields`) so a join writes the
      member event with the current profile instead of an empty one. Interfaces, not types, in both
      directions.
      Only a local user's profile is copied. A remote user's member event belongs to that user's server;
      writing their name into an event this server signs is this server putting words in another's mouth,
      and the disagreement is settled by the signature.
      A decision, because it reads like a missing check: `avatar_url` is stored as given even when it is
      not a Matrix content identifier. The specification puts that MUST on the client, says servers MAY
      validate, and says clients MUST expect values they did not ask for; Complement stores "LemurLover"
      as an avatar and expects success. Refusing would enforce an optional rule and fail a test that is not
      about that rule. `matrixid.SplitMXC` exists and is tested for the place the identifier is acted on —
      a media fetch. The one bound kept is this server's own: 255 bytes, refused `M_TOO_LARGE`, because a
      profile is amplified into every room the user joins (AGENTS §9, resource limits). Recorded under
      "Decisions" in REQUIREMENTS.md.
      Full run after: **95 passing, 124 failing, 1 skipped**, nothing newly failing — the member-event
      content changed for joins, which is the kind of change that breaks tests comparing member content
      exactly, and it did not. Newly passing: the four profile tests and `TestPushRuleRoomUpgrade`, which
      had been flaky.

- [ ] T-4058 the profile gaps T-4057 left, recorded rather than left implicit.
      (a) `GET /_matrix/client/v3/profile/{userId}` for a user on **another** server is answered 404 here.
      Synapse asks the other server over federation
      (`GET /_matrix/federation/v1/profile/{userID}`, an unauthenticated federation GET) and caches. Until
      that exists, Complement's `TestInboundFederationProfile` and `TestOutboundFederationProfile` stay
      red, and a client looking at a remote user has no way to ask how that user is presented except
      through the member events of a room it is already in. Read the Server-Server section on `/profile`,
      check whether the pinned specification marks that endpoint stable (the Complement test names suggest
      MSC4032/MSC4039 work: verify against the spec pages, not the test names), and note that an
      unauthenticated federation endpoint is a directory of this server's users unless it is scoped.
      (b) Custom profile fields (`keyName` of any name, `m.profile_fields` in `/versions` and in
      `/capabilities`). Not implemented and deliberately not advertised; this server holds the two standard
      fields and refuses any other `keyName` with `M_NOT_FOUND`, which is the answer the specification
      expects from a server that does not advertise the extension. Do not advertise it by storing arbitrary
      JSON in the account row without deciding how a field is propagated: a profile field is copied into
      member events in every room the user stands in, which turns one account into a fan-out per room per
      change.
      (c) `POST /user_directory/search` and `GET /user_directory` are not implemented
      (`TestRoomSpecificUsernameAtJoin`, `TestRoomSpecificUsernameChange`,
      `TestUserDirectorySearchLimited`…). The room-specific display name they check is *already* carried by
      the member events this server writes, so the user directory is a search index over state this server
      can already see, not a new kind of data — worth taking after T-4057 rather than before.

- [x] T-4059 the content repository: storing files, serving them, and asking a stranger for its own.
      Eleven endpoints on two doors. The Client-Server door: `POST /media/v3/upload` (+ `v1`, `r0`),
      `POST /media/v1/create`, `PUT /media/v3/upload/{serverName}/{mediaId}` (+ `v1`), `GET
      /media/v3/download/{serverName}/{mediaId}` (+ `v1`, `r0`, and the `/download/…/{fileName}` form
      that lets a browser write the name it was given), the same three for `/thumbnail`, and
      `GET /media/v3/config`. The authenticated door the specification added later: `GET
      /client/v1/media/download`, `/download/…/{fileName}`, `/thumbnail`. The server-server door: `GET
      /_matrix/federation/v1/media/download/{mediaId}` and `/thumbnail/{mediaId}`, both behind the
      `X-Matrix` credential check.
      Why it is a module and not a handler: three different things have to agree — what is on disk, what
      the database says it is, and what the two doors tell different kinds of caller. `internal/media`
      owns the rules (identifier whitelist, origin validation, size bounds applied *before* the bytes are
      read or decoded, the pending-reservation state machine and its sweep, thumbnails, and the multipart
      answer in both directions); `internal/persist/media.go` holds the metadata; `internal/api/media` and
      `internal/api/federation` are transport; `internal/federation/media.go` is the outbound half.
      The decisions that were not obvious, written up as DEC-040…DEC-044: the identifier whitelist is
      applied before any path is built; remote content is fetched from its origin once and kept under a
      directory named for that origin, and this server refuses to serve *another server's* content from
      its own federation endpoint; a peer's `Location` pointer is not followed; a thumbnail of remote
      content is made here from the bytes here; the fallback to the deprecated endpoint is taken on any
      unsuccessful answer (DEC-043, because Complement's own fixture answers 400 for its authenticated
      media route).
      Correction found by Complement after the first commit: uploading over an address that already has
      content is answered `409 M_CANNOT_OVERWRITE_MEDIA`, which is the code the specification names for it;
      this server was answering `M_FORBIDDEN`, which describes a different fact. `TestAsyncUpload` is green.
      Evidence: `tests/unit/media_test.go`, `tests/unit/media_multipart_test.go`,
      `tests/integration/media_test.go` (including a two-homeserver fetch that proves the second request
      is answered from the copy held here — the origin's file is deleted before it). Complement media
      cluster (`RUN='TestFederation|TestUpload|TestDownload|TestThumbnail|TestMedia|TestUrlPreview|TestConfig'`):
      **51 passing, 9 failing**; the failing names — `TestUrlPreview`, `TestFederationRoomsInvite`,
      `TestFederationKeyUploadQuery`, `TestFederationRedactSendsWithoutEvent` — are all in
      `.agent-work/complement-failures-20261113d.txt`, i.e. already red before this work. Full suite after:
      **104 passing, 115 failing, 1 skipped** (was 95/124/1), nothing newly failing, and the names that moved
      green include the media families (`TestContentMediaV1`, `TestContentCSAPIMediaV1`, `TestMediaFilenames`,
      `TestMediaWithoutFileName`, `TestMediaWithoutFileNameCSMediaV1`, `TestFederationThumbnail`,
      `TestLocalPngThumbnail`, `TestRemotePngThumbnail`, `TestMediaConfig`) plus federation tests that the
      T-4060 race was reaching: `TestFetchEvent`, `TestFetchEventWorldReadable`, `TestFetchHistoricalSharedEvent`,
      `TestDeviceListsUpdateOverFederationOnRoomJoin`, `TestCumulativeJoinLeaveJoinSync`,
      `TestFederationRejectInvite` (red again in a later run — it is flaky, see T-4034's observation).
      Lists: `.agent-work/complement-failures-20261113e.txt`,
      `.agent-work/complement-passes-20261113e.txt`.
      Found on the way, and the reason two of those media tests were red: T-4060.

- [x] T-4060 a federation request refused as unauthenticated because a *database write* failed.
      Symptom in Complement, twice in three runs: `GET /_matrix/federation/v1/media/download/…` refused
      with "the credential from hs2 is not signed by a key that server published under ed25519:v1", and
      the caller's own log saying `no verification key for ed25519:v1`. Cause, once the refusal carried
      the lookup's own words (DEC-046): `cannot record the keys fetched for hs2: UNIQUE constraint failed:
      server_signing_keys.server_name, server_signing_keys.key_id`. Two requests needed hs2's keys at the
      same moment, both fetched, and each ran a *replacement* of the key set as separate statements:
      delete, then insert. Interleaved, the second insert collided; the keyring then had nothing to answer
      with, and a correctly signed request looked like a forgery.
      Fixed by making replacement one transaction in both places that replace rather than append
      (`SaveServerKeys`, `RememberAuthContext` — an event handed over by two peers at once hits the same
      shape). The diagnostics are kept, because they are what turned a two-day hunt into a one-line read.
      Regression test: `tests/unit/fedkeyrace_test.go`. Verified against Complement: three consecutive
      clean runs of `TestMediaFilenames|TestMediaWithoutFileName` with zero credential-check refusals,
      after two of three runs had failed before the fix.

- [ ] T-4061 the media gaps T-4059 left, recorded rather than left implicit.
      (a) URL previews (`GET /media/v3/preview_url`, `GET /client/v1/media/preview_url`): answered
      `M_UNRECOGNIZED`, not implemented. Complement's `TestUrlPreview` is red. This is a fetch of a
      client-supplied URL, so it needs the resolver/address policy federation already has, a text/html
      parser that does not execute anything, an image-only `thumbnail_url`, and a cache with a bound —
      do not implement it by handing a caller's URL to `http.Get`.
      (b) A peer's `Location` part (DEC-042) is refused. If it is ever followed, it needs its own
      resolution with the federation IP policy applied, a same-origin check against the peer it came from,
      and the same size bound. Until then a server that answers with a pointer is reported, not served.
      (c) `TestFederationKeyUploadQuery`, `TestFederationRoomsInvite`,
      `TestFederationRedactSendsWithoutEvent` and the flaky `TestFederationRejectInvite` remain red; none is a
      media
      problem. Key query/claim across federation and invite rescission over federation keep their own
      tasks in the federation sections above.
      (d) Media storage is the filesystem plus the metadata row. It has no quarantine directory, no
      per-room or per-user quota (only the pending-upload budget and the upload size limit), and no
      deletion endpoint — which the specification does not offer either. Record before any deployment
      claim, per AGENTS §10.

## 2026-11-07 — the release path, the run's own evidence, and remote membership changes

- [x] T-4062 a room whose moderator lives here can kick, ban or unban a member who lives
      elsewhere. The endpoints answered 501 behind `ErrRemoteTarget` on the reasoning that a leave
      or a ban about another server's user would be "this server claiming to be somebody else's
      homeserver". No room version says that: an `m.room.member` event about *another* user is
      authorised by the sender's kick/ban/unban power, and the signature is the sender's server's,
      which here is this one. Consequence of the refusal: a room could never remove somebody who
      joined from elsewhere, and a real permission refusal (403, sender without the power) was
      unreachable behind a missing-feature answer (501). Now written and signed here under the
      room's rules; `ErrRemoteSender` names the case that is real (an event this server would sign
      for another server's user), `ErrRemoteTarget` stays for join and knock.
      Fan-out detail worth recording: the destinations for an event are the servers holding a joined
      member, and a ban, a kick or a rescinded invitation is usually about a server holding nobody
      joined here — so a `m.room.member` event is now also queued to the domain of the user it names,
      after which the room's ACL filter still applies.
      Verification: `tests/integration/remotemembership_test.go` (three cases: kick visible on both
      servers and the user silenced on their own; ban of an *unjoined* remote member reaching the
      target's server, then the unban; a sender without the power answered 403, not 501);
      `go test ./tests/...` and `-race` green; Complement `TestFederationRoomsInvite/Inviter user
      can rescind invite over federation` and `TestUnbanViaInvite` green, both red before.

- [ ] T-4063 what T-4062 leaves red: `TestFederationRoomsInvite/Non-invitee user cannot rescind
      invite over federation`. The case expects the *invited user's own server* to go on holding the
      invitation after a moderator on another server kicks that user, and to keep answering
      `SyncInvitedTo` for it. This server applies the kick to every copy of the room it holds, so the
      invitation stops being current here. Which is right is a decision, not an accident: the room's
      state is one answer, but an invite is also a claim a server keeps about its own user, and the
      two are settled differently depending on which server writes what. Also inside that subtest: it
      stalls in `SendEventSynced` for a message in a *different* room, which needs its own diagnosis
      before the behavioural question can even be seen — a sync that does not deliver a local user's
      own event is a bug whatever the invite rule turns out to be.

- [ ] T-4064 a device list nobody is told about. `TestDeviceListUpdates/when_joining_a_room_with_a_remote_user`
      fails reproducibly (2026-11-07, twice in a row; and it is in `.agent-work/complement-failures-20261113e.txt`,
      which is why the compliance matrix row claiming "all ten subtests" is corrected in T-4065).
      The trigger is missing, not the announcement: when a server *joins* a room that lives elsewhere,
      the resident server has to announce the device lists of the members already there to the server
      that just arrived, and today announcements go only to servers that already hold a joined room
      with the user. Needs the announcement to be asked for as part of the join's aftermath, plus an
      answer on the joining side that feeds it into the per-user device streams. Related: T-3008c
      (key upload, new device and cross-signing as triggers) and T-4023 (deletions).

- [x] T-4065 the records had drifted, and drift in an honest-looking table is worse than a gap.
      `docs/compliance-matrix.md`: media, sync/messages, device keys, push rules, presence, directory
      and spaces, account data and filters still said "not implemented" long after they were written;
      the primitives table contradicted the federation table about room version 1/2 authorisation and
      about `m.room.server_acl`; the federation table said invites were missing when both halves of
      the invite path exist; and the device-list row overclaimed. Each row now says what the tree
      does, with the Complement names that prove it and the ones that still fail. `README.md`
      described rooms, events, sync and federation as unimplemented; it carries the measured position
      instead. `docs/compliance-matrix.md`'s "Last updated" line now says how to read the dated
      sections below it.

- [x] T-4066 a pass/failure list that cannot tell two tests apart. The lists
      `scripts/complement.sh` cuts are bare top-level names, and Complement defines the same name in
      more than one package: `TestSync` exists in `tests/csapi` and in `tests/msc4222`. In every
      recorded full run one of them passed and the other failed, so the pair appeared in *both* lists
      and the prose had no way to say which server behaviour each one was evidence about — the
      compliance matrix cited "`TestSync` verified" and "still red through sync: MSC4222
      `state_after`" in adjacent columns of one row, both about the same string. The runner now asks
      `go test -json`, writes `.agent-work/next/complement-results-<label>.tsv` — action, package,
      test — beside the bare lists the matrix cites, prints the log with the package on every line
      (which is what makes a `-p 2` run readable at all), and prints the qualified pairs whenever a
      name lands in both lists. `jq` is used when present; without it the script says out loud that
      the lists are not package-qualified. Also: a run from which nothing could be derived (a broken
      renderer, a `go test` that died at a flag) used to print "0 passing, 0 failing" as if that were
      a result; it now says no results were derived. Found while reading this run's own list: the
      `TestSync` failure in `.agent-work/complement-failures-20261113e.txt` is `tests/msc4222`'s, not
      the stable sync suite's — which is why the matrix row that said "MSC4222 `state_after` (T-3001f)"
      pointed at a task about sync filters' presence section. It points at T-4067 now.

- [x] T-4067 MSC4222 `state_after` is answered, and MSC4222's other half is a different feature.
      Done: `/sync` with `use_state_after=true` (or the unstable `org.matrix.msc4222.use_state_after`)
      replaces a joined room's `state` with `state_after` — the state as of the *last* event of the
      timeline batch, that event's own change included — and the field carries the name the client
      asked with, because a client that sends the pre-stabilisation flag reads the pre-stabilisation
      key. This server already records the state each event was sent against (T-3004a), so the answer
      is one lookup: the snapshot attached to the newest event of the batch. Rooms whose newest event
      has no snapshot (history that arrived without one) fall back to the current state, which is the
      same approximation the `state` section already makes, recorded where it happens.
      `lazy_load_members` on the state filter is now honoured, and only on the state filter: the
      reference servers key off it there, and a timeline is a list the client asked for, not a state
      section that can be trimmed. Under it the members sent are the ones the batch speaks about, so
      a batch of one message sends that sender's membership and not the whole roster — while
      non-member state stays, because a client that cannot show a room's name or power levels has
      gained nothing. What is deliberately *not* trimmed is `state` without the flag: the
      specification asks for "at least" the timeline senders, and today's superset is legal.
      Left and banned rooms keep `state`: their batch ends at the event that took the user out, so
      "state at the end of the batch" is what that section already answers.
      Still open, its own task: MSC4222's `GET /rooms/{roomID}/members` as a stream (`from`/`to`
      positions, so a client can follow a room's membership instead of re-reading it).
      Verification: `tests/integration/stateafter_test.go` (three cases: the ordinary answer keeps
      `state` and gains nothing named `state_after`; `state_after` covers every timeline sender and
      repeats the membership events already in the batch; under lazy loading a silent member is
      dropped and non-member state is not; the unstable flag names the unstable field); Complement
      `tests/msc4222` green, both subtests, from
      `RUN='TestSync$|TestSyncOmitsStateChangeOnFilteredEvents|TestRoomMessagesLazyLoading|TestSyncLazyLoadingRoomMembers|TestDeviceListUpdates|TestAddAccountData'`
      with label `stateafter` (`.agent-work/next/complement-run-stateafter.log`,
      `.../complement-results-stateafter.tsv`) — that run also re-checks the `tests/csapi` `TestSync`
      and `TestRoomMessagesLazyLoading*`, which stay green, and leaves `TestSyncOmitsStateChangeOnFilteredEvents`
      (T-4020) and `TestDeviceListUpdates` (T-4064) red as before.

- [ ] T-4068 MSC4222's other half: `GET /rooms/{roomID}/members` as a stream. `/members` answers the
      roster now, with `membership`/`not_membership` filters and `at` for the past (T-4058's
      neighbour), but not with `from`/`to` positions that let a client follow membership changes
      instead of re-reading the whole list. Nothing in the Complement suites at this checkout asks
      for it; it is recorded so "MSC4222 done" is not claimed from the sync half alone.


- [x] T-4069 an event type without a dot is not malformed, and refusing it hid an endpoint.
      `PUT /rooms/{roomID}/send/{type}/{txn}` answered 400 for types like `please_filter_me`
      because `matrixid.EventTypeValid` required at least two dot-separated components, a rule
      this project wrote for itself ("custom types must be dotted, so two servers cannot invent
      the same name" — `docs/requirements.md` DEC line 13). Nothing in the protocol says that:
      the reserved `m.` namespace is enforced, and a name without a dot is unqualified, whose
      only risk is a clash — a problem belonging to whoever invents the name, not to the server
      carrying it. The cost of the rule was invisible in the local suite and obvious in
      Complement's: a client could not store, filter, paginate or read back an event of that
      type, because the request died before the room saw it. Now accepted: components still have
      to be usable (no empty component, no space, no leading dot, length bound), and `m.` still
      has to look like the specification's own names.
      Verification: `tests/integration/customeventtype_test.go` (an undotted type is stored,
      delivered in a sync, excluded by a `not_types` filter that names it, and still readable at
      `/event/{eventID}` afterwards; the reserved shapes `m.Room.Message`, `m.`, `m..room` and
      `bad type` are still 400); `tests/unit/roomslogic_test.go` updated; Complement
      `TestSyncOmitsStateChangeOnFilteredEvents` moves from the 400 to a later assertion, which
      T-4070 finished.

- [x] T-4070 the state section may only subtract what the client is being shown in this response.
      `stateEvents` dropped every state event that the timeline batch carried. That is right for
      an incremental sync — the client is handed the event in the same response — and wrong in
      the two cases the subtraction cannot see: a *first* sync, where the client holds nothing,
      and any sync whose filter or `limit` hid the event from the batch. In a young room the
      second case is total: every state event is the batch, so the state section answered empty.
      Now the subtraction applies only when `since` is present and `full_state` is not, which is
      the same condition the "changed since the token" filter already uses. Cost is unchanged for
      a linear room (the incremental case is the one that has a token); the first sync and a
      `full_state` sync carry the room's whole state, which is what they were always meant to
      carry and what `lazy_load_members` exists to shrink.
      Complement: `TestSyncOmitsStateChangeOnFilteredEvents` (a two-server room, a state event
      `m.room.name` delivered over the federation `/send` path across a fork, then a sync with
      `timeline.limit=1` and `not_types:["please_filter_me"]` — the event is in the batch and has
      to be in the state too), red in every recorded run before this and green with it, together
      with the sync and room-state family that could have caught a regression: 13 top-level
      tests, 0 failures, `.agent-work/next/complement-run-statebatch.log` and
      `.../complement-results-statebatch.tsv`.
      Own tests: `tests/integration/syncstatebatch_test.go` (a limited batch whose single event
      is a state event: the state section carries it and the rest of the room's state; a filter
      that hides the topic from the batch must not hide it from the state; an incremental sync
      still does not serve the same event twice). One existing test needed correcting rather
      than the server: `TestPrevStateShowsWhatAStateEventReplaced` counted served topic events
      exactly, and a first sync may now serve the same event in both sections, so its assertions
      are made per event id — including the case that mattered, one event claiming two different
      things that it replaced.

- [ ] T-4071 a two-server test that fails twice for different reasons and passes the third time.
      `TestPushRuleRoomUpgrade` (csapi) moved from passing in `.agent-work/complement-passes-20261113e.txt`
      to failing in the full run `20261107b`, and its two local-upgrade subtests pass every time: the
      two *remote*-join subtests are the flaky ones. Three re-runs in a row gave: fail with
      `federation: hs2 has no keys recorded and was asked for too recently to ask again` — a join
      refused at the X-Matrix credential check because the peer's signing keys were fetched, not
      found, and the negative answer is cached with a backoff, so the first join after a deployment
      can be refused and reach the client as a 500 on `/join`; fail with
      `certificate is valid for hs2, localhost, not hs1` — a request for the name `hs1` went to a
      container holding `hs2`'s certificate, which is the stale `aardvark-dns` record condition the
      runner warns about, not a protocol fault; and then pass. Neither is caused by the sync, account
      data or event-type work in this window, and the third run is the proof.
      Worth fixing on its own: the negative key cache should not be able to answer "this server has
      no keys" to a join that arrived *because* the peer exists, and the answer a client gets here is
      `M_UNKNOWN` 500 for what is, at worst, a retryable federation failure. Related: T-4001 (the
      credential check), T-4005 (key fetching and its trust clamp), T-4056 (the environment warning).

- [ ] T-4072 `GET /keys/changes`: the device-list stream as an endpoint. `device_lists.changed` and
      `device_lists.left` already reach `/sync`, and the rows behind them are addressed per observer
      with a stream position — so the range question a client asks ("whose devices moved between these
      two tokens?") is a read of the same table, not a new feature. Complement: `TestKeyChangesLocal`
      (csapi), which is red on the missing endpoint. Record the deprecated `user_id` parameter's
      handling explicitly: if it names someone other than the caller, the answer is not that
      someone's list.

- [x] T-4072 is done, and it came with a route the client API was missing. `GET
      /_matrix/client/v3/keys/changes` answers the range question the `device_lists` section of a
      sync answers as "since my token": whose device lists did this user have to re-read, and
      whose stopped being theirs to track. It reads the same addressed rows, so the two cannot
      drift — the shared conversion is `e2ekeys.DeviceListSections`, used by the sync service as
      well, which also keeps the one diagnostic the sync path had (a change row with a kind this
      server never wrote is a storage fault, and the helper has no logger).
      Bounds: both are required and both must be tokens this server issued. Filling in a bound
      the server cannot read — a bare number, a token from another homeserver — would report
      changes the client then believes it has already seen. The deprecated `user_id` parameter is
      checked rather than ignored: naming another user does not make their addressed rows yours,
      and the answer is 403 rather than a politely empty list.
      Verification: `tests/integration/keyschanges_test.go` (the sync section and the range
      endpoint agree in both directions, changed and left; a user's own upload is not reported
      back to them; four malformed ranges are 400; `user_id` naming another user is 403 and
      naming themselves is answered); Complement `TestKeyChangesLocal` green.
      That test then exposed the second thing: it logs in through
      `POST /_matrix/client/r0/login`, which this server answered 404 `M_UNRECOGNIZED`.

- [x] T-4073 the legacy `r0` prefix is registered for the whole client API, not for the handful of
      endpoints a Complement test happened to use. `r0` is the specification's own history — the
      same endpoints published under an older prefix — and a server that answers it with
      `M_UNRECOGNIZED` tells a client that follows the older text that the feature is missing.
      Most of the room, membership, state, sync and account-data routes already had it; login,
      registration, `whoami`, logout, `/devices`, `/keys/*`, `/pushrules` and the unauthenticated
      reads (`/directory/room/{alias}`, `/profile/...`) were v3-only. The unauthenticated ones need
      their own helper because the versioned one wraps authentication, and requiring a token at
      login would make the first step of using a homeserver impossible.
      The prefix is a prefix, not a catch-all: `GET /r0/notathing` is still 404 `M_UNRECOGNIZED`
      and `PUT /r0/login` is still 405 with an `Allow` header.
      Verification: `tests/integration/legacyprefix_test.go` (login under r0 issues a working
      token for the same account; whoami, sync, devices, pushrules, keys/changes, publicRooms and
      a profile read all answer under r0; unknown path 404, wrong method 405); Complement
      `TestLogin`, `TestRegistration`, `TestLogout`, `TestDeviceManagement`, `TestProfile*`,
      `TestPushRule*`, `TestVersionStructure`, `TestUnknownEndpoints`, `TestServerCapabilities`,
      `TestUploadKey*`, `TestKeysQueryWithDeviceIDAsObjectFails`, `TestKeyChangesLocal` and both
      `TestSync`s green in `.agent-work/next/complement-run-r0keys.log`
      (`.agent-work/next/complement-results-r0keys.tsv`): 20 passing, 3 failing. Those three
      (`TestDeviceListUpdates`, `TestDeviceListsUpdateOverFederation`,
      `TestE2EKeyBackupReplaceRoomKeyRules`) are the already-recorded device-list announcement and
      key-backup gaps (T-4064, T-4008, T-3008e), unchanged by this work.

- [x] T-4074 the membership handshakes a room holds and this server does not: `/make_leave`,
      `/send_leave` (v1 and v2), `/make_knock`, `/send_knock` (v1 and v2), the outbound halves, and
      `POST /knock/{roomIDOrAlias}`. Six Complement tests and a cluster of leave/forget tests were
      red for the missing endpoints.

      The three handshakes are one shape, so the room service writes one template —
      `MembershipTemplate(ctx, room, user, membership)` — and `JoinTemplate` is that asked for a
      join. The differences are the room's business rather than the endpoint's: a restricted room
      needs its join authoriser named before the event is named, so that step runs only for a join,
      and a refusal says which thing the room did not let the user do (`verbOf`).

      On the endpoints: `make_leave` answers under both `event` and `membership_event`, because the
      specification and gomatrixserverlib — what Dendrite and Complement's own federation server
      decode with — disagree, and an answer under one name reads to the other reader as "not
      implemented". `send_leave` and `send_knock` take the bare event or the v1 `event` envelope,
      check the path against the event (room ID, sender/state_key, the membership claimed, the
      origin, and the event ID recomputed from the content), and answer **400 `M_INVALID_PARAM`, not
      403**, when the event claims a membership the endpoint is not for: that is a request which does
      not mean its URL, not a permission the room withheld, and 403 invites a peer to go find a more
      obliging resident server in the same room. `send_join` now answers the v1 URL too (v1 body
      wrapped, v1 answer without `event`/`auth_chain`/`servers_in_room`) and shares those path
      checks, which are the same function with the membership as a parameter. A knock's second half
      is `knock_room_state`: full PDUs so the knocker can check rather than believe, minus the
      room's membership events, which is precisely what knocking does not grant.

      Also in this window, because the knock tests stopped at the invite in front of them: an invite
      names its event under both `invite_event` (specification) and `event` (gomatrixserverlib, which
      rejects a v2 body without that key outright). Lattice sent only the first, so to a large part
      of the federation its invites carried no event at all. Inbound reads either spelling now; the
      invite endpoint had no test of a peer's request in it, and has one.

      Verification: `tests/integration/federationmembership_test.go` (a leave end to end, an invite
      rejected by a server that has never held the room, a knock with `knock_room_state` checked for
      `m.room.create`, for full PDUs and for the absence of membership, a knock template refused at
      an invite-only room, and seven wrong-membership requests across `send_leave`/`send_knock`/
      `send_join` v1 and v2 — all 400), `tests/integration/federationinvite_test.go`
      (countersigning a peer-spelled invite, with the room's real signed state bundled in
      `unsigned.invite_room_state`, and the invited user then able to read the invitation),
      `tests/integration/knockrestricted_test.go`, and the whole Go suite green.
      Targeted Complement `.agent-work/next/complement-run-membshk3.log`
      (`.agent-work/next/complement-results-membshk3.tsv`): 2 passing, 4 failing in that subset, of
      which the four `TestCannotSend*Via*` validation tests and `TestFederationRejectInvite` and
      `TestRoomsInvite` are green, and `TestKnocking`'s local half went from 3 passing of 13
      subtests to 12 of 13.

- [ ] T-4075 a knocked-on room is invisible to the user who knocked on it. `POST /knock/{roomID}`
      against a room held elsewhere now reaches the room — the resident server records the knock and
      the room's other members see the membership — but the knocking user's own server keeps nothing:
      her `/sync` has no `rooms.knock` entry for the room, so the knock she just made cannot be seen,
      rescinded or followed up from her side.

      The tempting fix is wrong and was tried. Storing what `/send_knock` hands back as a join's
      bundle cannot work: `knock_room_state` deliberately carries no `m.room.member` events, and in
      every room version with knock the member event of whoever wrote a state event is one of that
      event's auth events. Given the full PDUs, nothing in the set can be authorised — including this
      server's own knock event — so all seven events sit unresolved forever. The log records what
      arrived (`internal/federation/membership.go`, at the return that does not store it).

      What is needed is a knock's own record — the shape an invite-hanging room is for an invitee:
      the room row, this server's signed knock event, and the stripped state as an unverified picture
      of the room, never as a history. Then `/sync`'s `rooms.knock.<room>.knock_state` (which
      Complement reads through `client.SyncKnockedOn`) has something to answer with.
      Complement: `TestKnocking/Users_in_the_room_see_a_user's_membership_update_when_they_knock#01`
      and the four subtests after it in
      `.agent-work/next/complement-run-membshk3.log`.

- [ ] T-4076 what a first sync's `state` section is. Complement's knock test reads the room's
      current member events out of `rooms.join.<room>.state` and expects the reason of the **first**
      knock, which only holds if `state` is the state *at the start of the returned timeline* rather
      than the state as it is now. The specification's wording is "state events relevant to the room,
      sent up to the start of the timeline", and MSC4222 (`state_after`) exists precisely because
      servers got these two the wrong way round; this server answers `state` as current state (see
      the conditional subtraction in `internal/sync/service.go` and the MSC4222 read in
      `internal/sync/stateafter.go`).
      This is not a free flip: `TestSyncOmitsStateChangeOnFilteredEvents` was reddened by an earlier
      shape of the same question, and it is green now. Decide it against both tests, in that order of
      evidence, and note that a server that gets this wrong is still interoperable — which is why the
      fix has to be argued and not guessed.
      Complement: `TestKnocking/Users_in_the_room_see_a_user's_membership_update_when_they_knock`
      (`content.reason` of the wrong knock) in `.agent-work/next/complement-run-membshk3.log`.

- [ ] T-4077 `POST /rooms/{roomID}/forget`, and the difference between leaving and forgetting.
      Lattice has no `/forget` at all: the endpoint answers 404 `M_UNRECOGNIZED`. Behind it is a
      policy this server has not decided — a *left* room stays readable to the departed member
      (state, `/members`, `/messages`: SPEC-216), and *forgetting* is what takes that away. Today a
      left room is not readable, which is the automatic-forget behaviour, and the two Complement
      tests that pin the distinction are red together:
      `TestRoomForget` (400 `M_UNKNOWN` while still joined; 200 after leaving; `/messages` then 403;
      the room gone from the room lists; MSC4323's forget-while-joined gated on the capability) and
      `TestLeftRoomFixture` (a departed member reading `m.room.name`, a state event that no longer
      exists, `/members` showing herself as `leave` and not the member who joined later, and
      `/messages` from her old sync token).
      Start from the persistence side: forgetting is a per-user fact about a room, so it needs a row
      that `/sync`, the room lists and the history reads all consult — not a deletion of the room.
      Related: T-2015 (`/context`), T-3001 (history visibility), T-4074 (a leave that reaches a room
      held elsewhere, now done).

- [x] T-4078 two ceilings a request hits before anything has happened: an event over `MAX_PDU_SIZE`,
      and a body that is not valid UTF-8. Both were in the 2026-11-07c failing list with a 200 or a
      wrong-code answer where the specification asks for a refusal:
      `TestEvent/Parallel/Large_Event` and `TestEvent/Parallel/Large_State_Event`
      (`invalid_test.go:215`, `:229`: "MatchResponse got status 200 want 413"), and
      `TestRequestEncodingFails/POST_rejects_invalid_utf-8_in_JSON` (`apidoc_request_encoding_test.go:22`:
      "MatchResponse got status 401 want 400", the answer being `M_NO_AUTH` — the body's invalid byte
      had been swallowed and the request had gone on to the auth question).

      The size ceiling is checked at the storage funnel rather than at the client door
      (`maxPDUSize` in `internal/rooms/service.go`, measured on the canonical form inside
      `persistEvent`), so the client's `/send` and `/state`, a peer's `/send_join`, `/send_leave`,
      `/send_knock` and a transaction's bundle ingest all answer the same question the same way —
      which is the point: the limit is a property of an event, and a server that enforced it only on
      one door would store events its peers would refuse. 413 `M_TOO_LARGE`, with the two numbers in
      the message so a sender can tell an over-long body from a near-limit room ID.

      The UTF-8 ceiling is in `httpx.DecodeJSON`: Go's decoder does not fail on an invalid byte, it
      substitutes U+FFFD and carries on, so an unvalidated body becomes a stored display name,
      message and index entry, with a 200 for the sender. JSON text is UTF-8 by definition, so the
      body is rejected as `M_NOT_JSON` (400) before the decoder sees it. Valid UTF-8 of any width is
      ordinary JSON: the test sets a name with an em dash and Japanese in it afterwards.

      Verification: `tests/integration/eventlimits_test.go`. The size test **bisects** for the
      largest body this server accepts and asserts that the boundary sits just under 65,536 bytes;
      hand-picking an oversized body cannot tell a correct ceiling from one that fires far too early,
      and the first version of this test proved that by picking a body that was already over. The
      refused events are also checked not to be in the room's timeline — a refused event is not a
      happened event. `go test ./tests/...` green. Targeted Complement run `20261107d-sizelimit`
      (`.agent-work/next/complement-run-20261107d-sizelimit.log`, table
      `.agent-work/next/complement-results-20261107d-sizelimit.tsv`): 2 passing, 0 failing
      top-level — `TestEvent` (with `Large_Event` and `Large_State_Event`) and
      `TestRequestEncodingFails` — lists `.agent-work/complement-{passes,failures}-20261107d-sizelimit.txt`.
      These two were not re-run in a full suite; the full tally above them is still 20261107c.

- [x] T-4079 a filter is not accepted whatever is written in it. `TestFilter` (csapi) was red on
      fourteen assertions all of the same shape, in the 20261107c log:
      "Expected 4XX status code, got 200 for testing filter map[presence:not_an_object]" and thirteen
      more. `POST /user/{userID}/filter` stored any JSON and answered 200, so a filter with a typo in
      it became a permanent fact: every later `/sync` through it returned fewer events than the client
      meant to ask for, and nothing said so.

      Fixed at the two doors a filter arrives at — `SaveFilter` (the create door) and `Service.filter`
      (a filter given inline to `/sync`), which is why the second one matters: a client told its filter
      is wrong once and not again has been trained to distrust the answer it did get. The checks are
      the ones the filter object states in the specification — which fields are objects, which are
      lists of strings, which are lists of **room IDs** and **user IDs** (`map[room:map[timeline:
      map[rooms:[not_a_room_id]]]]` and `[not_a_sender_id]` are the two that no amount of type checking
      would catch) — and answered 400 `M_INVALID_PARAM` naming the field.

      What is deliberately not checked is written in the code: unknown field names are left alone. The
      filter object is where MSCs put their switches (a threaded-sync filter carries a namespaced name
      today), and refusing a name this server has not read makes this server the reason a client cannot
      try a newer spec.

      Verification: `tests/integration/filtervalidation_test.go`, twenty-one bad documents and the
      good one read back byte for byte, plus the inline `/sync` door. `go test ./tests/...` green.
      Targeted Complement run `20261107e-filters`
      (`.agent-work/next/complement-run-20261107e-filters.log`, table
      `.agent-work/next/complement-results-20261107e-filters.tsv`): **10 passing, 3 failing** across
      the twelve filter-touching tests in the suite (`TestFilter` green; `TestSync` green in both
      packages that define it, csapi and msc4222; `TestSyncFilter`, `TestRoomMessagesLazyLoading`,
      `TestRoomMessagesLazyLoadingLocalUser`, `TestSearch`…). The three failures — `TestSearch`,
      `TestRoomImageRoundtrip`, `TestFilterMessagesByRelType` — are all in
      `.agent-work/complement-failures-20261107c.txt` too, so they are not caused by this change.
      `TestFilterMessagesByRelType` is MSC3874 `rel_types` on `/messages`, unimplemented;
      `TestRoomImageRoundtrip` fails on `key 'end' missing`, which is next.

- [x] T-4080 a filter given to `/rooms/{room}/messages` was read for one flag and thrown away, and
      `contains_url` was not read anywhere at all. `TestRoomImageRoundtrip` asks for
      `/messages?filter={"contains_url":true}&dir=b` and asserts the chunk holds the one media
      event; this server answered the room's whole timeline, and because nothing was left *behind*
      it also answered no `end` token, which is what Complement's assertion pointed at first.

      The filter is one document with two doors: `/sync` nests it under `room.timeline` and
      `room.state`, and `/messages` takes a **room event filter as the whole parameter**. The type
      lists, the sender lists, the room lists and `contains_url` now live in one package,
      `internal/eventfilter`, which both doors and the shape checks from T-4079 use, so a filter
      cannot mean one thing on one door and another on the next. Applying it to a `/messages` page
      also moved where the page's `end` is measured: with a filter the chunk is no longer an index
      into the window that was read, so `end` names the position the page really stopped at.

      A detour worth recording because it was nearly taken: `end` was going to be answered on every
      page, since one Complement test asserted the key exists and the reference servers do answer
      it. It is not, and the specification is explicit — "If no further events are available
      (either because we have reached the start of the timeline, or because the user does not have
      permission to see any more events), this property is omitted from the response" — and
      Complement's own federation pagination loop says *"Keep paginating until the start of the room
      (no `end` property is returned)"*. Making `end` always present would have broken
      `TestMessagesOverFederation` to satisfy `TestRoomImageRoundtrip`; the real gap was the filter.
      The quotation is now in `internal/rooms/history.go` where the next reader will look.

      Verification: `tests/integration/messagesfilter_test.go` (the `/messages` filter's four kinds
      of list and `contains_url` both ways, a filtered page's `end` token followed to an empty page,
      the same shape checks answered 400 `M_INVALID_PARAM` at this door, and the flag honoured
      through a `/sync` filter with an unfiltered control sync beside it). `go test ./tests/...`
      green. Targeted Complement run `20261107g-msgfilter`
      (`.agent-work/next/complement-run-20261107g-msgfilter.log`, table
      `.agent-work/next/complement-results-20261107g-msgfilter.tsv`): **10 passing, 2 failing** —
      `TestRoomImageRoundtrip` newly green; `TestFilter`, `TestSyncFilter`, `TestSync` (both
      packages), the two lazy-loading `/messages` tests and the send/fetch family unchanged; the two
      failures are `TestFilterMessagesByRelType` (MSC3874 `rel_types`, unimplemented) and
      `TestMessagesOverFederation`, which fails in this run exactly as it does in 20261107c —
      `SendEventSynced waiting for event ID …` in the backfill subtest, T-4009b2/T-4007 — so it is
      not a consequence of applying filters.

- [x] T-4081 an account can change its password, and an account can end. `POST /account/password`
      and `POST /account/deactivate` were 404 `M_UNRECOGNIZED`, which is why `TestChangePassword` and
      `TestDeactivateAccount` were in every failure list.

      Both are guarded by User-Interactive Auth with an `m.login.password` stage, and the guard is
      the point: the session that is asking is the thing that may have been stolen. A password change
      takes the old password away (the hash it was checked against is gone), keeps the session that
      asked, and ends every other token unless the client passed `logout_devices: false`. "End every
      other token" is why the token store gained a revoke-except: revoking everything and minting a
      fresh token would change the caller's token under its feet, and the client would be sent to the
      login door by the call that just proved its password.

      Deactivation drops the password hash and marks the account. The account row stays, because this
      server's rooms, membership events and keys name it: what the specification promises is that the
      account cannot be used again, not that the server forgets it ever existed. Login then answers
      **403 M_USER_DEACTIVATED** rather than "wrong password" — the order of those two checks was
      reversed, because after a deactivation there is no hash to compare and the caller needs the
      truth about the account, not about the password.

      Verification: `tests/integration/passworddeactivate_test.go` (the challenge offers the stage;
      the wrong password leaves the first password working; a change with no new password is 400; the
      old password is 403 and the new one 200 afterwards; the asking session still works and the
      other is `M_UNKNOWN_TOKEN` while its *device* remains listed; `logout_devices:false` keeps it;
      deactivation ends login and the token that did it). Targeted Complement run `20261107i-account`
      (`.agent-work/next/complement-run-20261107i-account.log`, table
      `.agent-work/next/complement-results-20261107i-account.tsv`): **6 passing, 1 failing** —
      `TestChangePassword` and `TestDeactivateAccount` newly green, `TestLogin`/`TestLogout`/
      `TestRegistration`/`TestDeviceManagement` unchanged. `TestChangePasswordPushers` stays red:
      `POST /pushers/set` is a 404, which is T-4082.

- [ ] T-4082 pushers: `POST /pushers/set`, `GET /pushers`, and what a pusher is for.
      `TestChangePasswordPushers` (`tests/csapi/account_change_password_pushers_test.go`) fails at
      "CSAPI.MustDo POST …/pushers/set returned non-2xx code: 404" — this server has no pushers at
      all, so it cannot record that a device wants its events POSTed to a push gateway, cannot list
      them, and cannot expire them. Related and equally absent: `GET /notifications` (the badge a
      mobile app reads) and the `org.matrix.msc3902.push`/`push_rules` additive rules that ride on
      the same machinery, plus the rule that a password change expires the pushers of the sessions it
      ends (`TestChangePasswordPushers`'s third assertion). Start with the storage: a pusher is a
      per-device document with a pushkey, an `url` and `data`, and a `kind`; the reference behaviour
      replaces a pusher with the same `(pushkey, app_id, user, device)` and gives it a token that a
      gateway can use to ask for its removal.
      Complement: `TestChangePasswordPushers` in
      `.agent-work/next/complement-run-20261107i-account.log`.

- [x] Full run 20261107h is recorded in `docs/compliance-matrix.md`: **121 passing, 98 failing,
      1 skipped**, with `TestEvent`, `TestFilter`, `TestRequestEncodingFails` and
      `TestRoomImageRoundtrip` newly green and `TestPushRuleRoomUpgrade` newly red on the 500 that
      T-4071 is about.

- [x] T-4083 read receipts and read markers. `POST /rooms/{room}/receipt/{type}/{eventID}` and
      `POST /rooms/{room}/read_markers` were 404 `M_UNRECOGNIZED`, so `TestRoomReceipts` and
      `TestRoomReadMarkers` had nothing to fail on but the door.

      A receipt is not an event: it has no ID, no authorization rules and no place in the room's
      graph. It is a current value about a person — like presence, which this server already
      synchronises — so it is stored as one row per (room, kind of receipt, user, thread) that
      *moves*, and the row takes a position in the same stream events take. That is what makes an
      incremental sync able to answer "which receipts moved inside your window", while a first sync
      has no window and is given the current answer instead. `m.fully_read` is the reader's own
      bookmark, so it goes into room-scoped account data rather than becoming something the rest of
      the room hears about.

      Three things the stream needed to be told about, each of which was a failure before it was a
      sentence: the `receipts` table joins the union the stream position is computed from (without
      it a `next_batch` is behind the receipt it is supposed to cover, and the receipt is filtered
      out of the very window that was asked for); the repository has to be bound to the transaction
      that takes the position (SQLite waits on itself otherwise, which read as a request that hung
      for ten seconds); and a row that is being updated needs a key to be found by — the **second**
      receipt one user posted answered 500 `M_UNKNOWN` until the row had a primary key.

      Verification: `tests/integration/receipts_test.go` — a receipt reaches the same user's next
      incremental sync as `m.receipt` with no `room_id` and no event ID of its own, a first sync
      carries the current position, a window that contains no move repeats nothing, a receipt
      posted twice leaves one position rather than two, and the refusals: an event this server has
      never held and an event from a different room are 404, a receipt kind with no dot in it and a
      non-event ID are 400, a room the caller is not in is 403, and a custom dotted kind is
      accepted.

      Targeted Complement runs, both in the sync and `/messages` family so the changed include-rule
      for joined rooms was measured against its blast radius:
      `20261107j-receipts` (**9 passing, 2 failing**) had `TestRoomReadMarkers` green and
      `TestRoomReceipts` still red — `room_receipts_test.go:61`, the second receipt of the room
      `returned non-2xx code: 500 Internal Server Error`, which is the primary-key finding above —
      and after the fix `20261107k-receipts` is **10 passing, 1 failing**: `TestRoomReceipts` and
      `TestRoomReadMarkers` green, `TestSync` green in both packages that define it, and
      `TestSyncFilter`, `TestSyncLeaveSection`, `TestSyncTimelineGap`,
      `TestSyncOmitsStateChangeOnFilteredEvents` and the two lazy-loading `/messages` tests
      unchanged from 20261107h. The one failure is `TestThreadReceiptsInSyncMSC4102`, red in
      20261107h as well: it receipts *within a thread*, and threads are not implemented (MSC4102,
      T-4020/T-4024 cluster).
      Unread counts are still zero on purpose: MSC784's counting rules and MSC2860's read marker
      are T-3005a, and the rows behind this are now the raw material for it.
