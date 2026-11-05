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
- [ ] T-2006a Persist the outcome of the on-receipt checks so `Snapshot.Rejected` is real;
      until then an event that failed on receipt could be used as an auth event
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
- [ ] T-2009 `m.room.redaction` application: power level or same server name rule, redacted
      form stored, hashes/signatures stay valid
- [ ] T-2010 room upgrade path (`m.room.create.predecessor`, `POST /rooms/{roomId}/upgrade`)

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
- [ ] T-3001c `presence` in `/sync` and `PUT /presence/{userID}/status` (Complement
      `TestSync/parallel/Newly_joined_room_includes_presence_in_incremental_sync`)
- [ ] T-3001d depth-ordered history paging: order by depth then stream position and widen
      the token to carry both, so a room whose events arrive out of depth order pages the
      way the reference server pages it (Complement `TestLeftRoomFixture`)
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
- [ ] T-3008 `device_lists` in `/sync`: whose device lists changed. Complement
      `TestSync/parallel/Device_list_tracking` asks for it; it depends on device keys
- [ ] T-3001 (leftover numbering, same work as T-3001a-d and T-4005 above): what still
      fails at a `/sync` step is presence, `device_lists`, MSC4222 `state_after`, and the
      two-server cases that need federation
- [x] T-3002 the sync-dependent assertions in `TestLogout` and `TestDeviceManagement` pass
