# Implementation Plan

Milestones are ordered by dependency. Each has an independently verifiable acceptance gate.

**Current status (2026-11-07).** M0 and M1 are complete and verified — see `ACCEPTANCE-CRITERIA.md`
(M1 verdict). M2 (rooms and events), M3 (client synchronisation), M4 (federation foundation) and part
of M5 (content repository, device keys, push rules, presence for one user) are implemented and
exercised by this project's integration tests, with large parts of M4 and M5 verified against
Complement. The measured position, from a full run on 2026-11-07: **106 Complement top-level tests
pass, 113 fail, 1 skip** (csapi 69/107, server-server 35/92, MSC packages 2/22). The honest per-feature
picture is in `docs/compliance-matrix.md` and the running record is in `PROGRESS.md`; every tally there
now comes from a run whose log and pass/failure lists are stored beside it.

Room versions 1 through 11 are offered for new rooms; version 12 and the unstable versions are
recognised and refused (T-4019), which is also what keeps MSC4289, MSC4291, MSC4297, MSC4311 and
MSC3757 red. Of the M4 handshake family, invites (both directions), the join handshake, `/state_ids`,
`/event_auth`, `/event`, `/get_missing_events`, `X-Matrix`, the durable queue, server ACLs and
restricted joins are done, as is a kick, ban or unban of a user who lives on another homeserver
(T-4062). Missing: `/make_leave`, `/send_leave`, `/make_knock`, `/send_knock`, answering `/backfill`,
federation `/profile` and `/query/directory`, and EDUs that do anything (T-4009c, T-4009b2, T-4008,
T-4064). The largest M5/M6 open items are the MSCs above, sync filtering (T-4020), relations, threads
and search, receipts and read markers, typing, cross-signing and key backup (T-3008d/e), the read
marker that would make push counts mean anything (T-3005a), depth-based paging (T-3001d) and the
address-policy re-check at dial time (T-4011). M6/M7 remain as staged.

One decision the records now carry explicitly (`docs/compliance-matrix.md`, "A full run on the
abandoned `main` branch"): the earlier attempt in this repository's history has no commits in common
with this branch and measured 78/92 on the server-server package where this branch measures 35/92.
Porting that handshake family and the room-version-12 work — keeping this branch's image, runner,
evidence discipline and static build — is cheaper than re-deriving it, and the port must be reviewed
against the specification pages rather than accepted because it is green.

## M0 — Stage 0: research and architecture (this iteration, done)

Spec pinned (v1.19, 2026-07-08), Complement analysed and its image contract extracted,
reference implementations surveyed, requirements + compliance matrix created, architecture
designed (event DAG, state sets, event auth, room versions, federation, storage), test layout and
Complement runner designed.
**Gate:** documents exist and are internally consistent with the code that follows.

## M1 — Foundation + identity layer (this iteration)

1. Project skeleton: `go.mod`, Makefile, `.gitignore`, README, package tree.
2. Config (YAML + env + flags) and slog logging with secret redaction.
3. HTTP layer: client listener (HTTP), federation listener (HTTPS), middleware, graceful shutdown,
   Matrix 404/405 `M_UNRECOGNIZED` routing semantics, `/healthz`, `healthcheck` subcommand.
4. Protocol primitives: canonical JSON, signing JSON (+verify), identifier grammar, unpadded
   base64 — tested against the spec's example vectors.
5. Storage: GORM+gormlite, PRAGMAs, migrations, repositories for users/devices/tokens/UIA nonces;
   atomic registration.
6. Signing keys: generate/persist ed25519, `GET /_matrix/key/v2/server`, `POST /_matrix/key/v2/query`.
7. Identity: `/register` UIA + `m.login.dummy`, shared-secret admin registration, `/login`,
   `/logout`, `/logout/all`, `/devices*`, `/account/whoami`, `/versions`, `/capabilities`,
   `/.well-known/matrix/client`, federation `/version`, media upload stub.
8. Tests: `tests/unit` + `tests/integration`, race detector, `go vet`, coverage.
9. Complement image + PKI entrypoint + runner script; run the M1-relevant Complement tests.
**Gate:** Complement `TestVersionStructure`, `TestUnknownEndpoints`, `TestRegistration`,
`TestLogin`, `TestLogout`, `TestServerCapabilities` pass against the built image; `go test -race`
green; no Complement assertion weakened.

## M2 — Rooms and events (next)

Room version registry (v9/v10/v11), event representation + event ID/hash/signing per version,
event DAG storage + shortnames + snapshots, `eventauth` for v11 (+v10/v9 tables), room creation
(`POST /createRoom`), local send (`PUT /send/{type}/{txnId}`), `m.room.member` invite/join/leave,
`/state*`, `/members`, `/context`, `/event_auth`. Complement targets: `TestJson`,
`TestRoomCreation`-class tests, `tests/csapi/rooms_state_test.go`, `room_members_test.go`,
`membership_on_events_test.go`, `txnid_test.go`, `power_levels_test.go`, `redact_test.go`.

## M3 — Client synchronisation

`stream_id` sequencing, `/sync` (incremental, `since`, `timeout`, filters, `state_after` later),
`/messages`, `/initialsync` not needed, typing/read receipts later. Targets: `sync_test.go`,
`sync_filter_test.go`, `room_messages_test.go`, `account_data_test.go`.

## M4 — Federation foundation

Server discovery + SSRF guard, X-Matrix auth, key fetching/notary + cache, `/send/:txn` receiver +
durable outbound queue with backoff, PDU validation + outliers, `/make_join`/`/send_join`,
`/state_ids`, `/backfill`, `/event`, `/missing_events`. Targets: `federation_*_test.go` (join,
send, keys, get_missing_events, acl, redaction, sync, typing, presence, to_device).

Done here: keys and discovery, `X-Matrix` in and out, the `/send/{txn}` receiver and its
durable queue, PDU ingest checks in specification order, refusals recorded and cascading
(T-2006a, DEC-021), outbound fan-out, Complement's federation credential fix (DEC-020).

Corrected 2026-11-08: the blocker named here for a long time — that current state was
last-write-wins while `internal/state.Resolve` was never called — no longer holds.
`rooms.reconcileCurrentState` (`internal/rooms/resolve.go:183`–218) reads the room's forward
extremities, takes each one's recorded state snapshot, resolves them with the algorithm the room
version names, and makes the result the current state; `internal/rooms/stateat.go:91` resolves the
state an event at a fork was carrying. Unit coverage of the resolver itself predates this.

## M5 — Extended functionality

Media repository (upload/download/thumbnail/async), device keys (`/keys/upload|query|claim`,
`/keys/changes`), to-device, push rules/notifications, presence, room directory, aliases, spaces,
hierarchy, appservices, admin APIs.

## M6 — Advanced room semantics

Remaining room versions incl. v12, state resolution v2/v3 correctness suite, restricted rooms,
knocking, upgrades, thread aggregation, search.

## M7 — Compliance and hardening

Complement coverage sweep, Complement Crypto for E2EE, load/perf testing, Postgres engine option,
operational docs, security review.

## Execution rules

- One coherent change per task; tests written/updated with the code; `make test` before marking done.
- Any protocol bug fixed gets a regression test.
- Progress recorded in PROGRESS.md with the actual command output summary, never claims without runs.

# Milestone plan for a first working version (agreed with the user on 2026-11-09)

The user will verify against a real client after M3. Baseline: Complement full serial
`20261109full-v12` — 151 passing / 68 failing / 1 skipped, image `def259a` clean, fingerprints zero.

Diagnosis for the gate below was read out of that run's log, not guessed at.

## M1 — it chats across two servers

Gate (all four green in one Complement run, fingerprints zero): `TestOutboundFederationSend`,
`TestMessagesOverFederation`, `TestFederationRoomsInvite`, `TestInviteFiltering`.

What the log actually says, which changes the shape of the work: **backfill is not the problem**.
`TestMessagesOverFederation` logs `federation: a room's history was filled in … fetched 50, stored 50,
linked 3, refused 0` and *then* fails `SendEventSynced waiting for event ID …` and
"Expected events ('?' = missing expected items)". So bob's server can pull alice's history on join, and
cannot see the events alice sends **after** he joined. Together with `TestOutboundFederationSend` red,
that is one root cause: an event accepted locally is not pushed to the remote servers resident in the room
as a federation transaction. Joins, `/state_ids`, server keys, device-list EDUs and invitations all
already cross the wire, so the machinery exists for membership and is simply not on the message path.

Tasks, in order (each: reproduce → root cause → smallest fix → regression test → commit):
- T-4201 reproduce `TestOutboundFederationSend` alone against two local deployments and read the hs1 log:
  does this server try a `/send/transactable/{txnId}` at all, and what does it do with the answer?
- T-4202 push locally-originated PDUs to resident remote servers on the send path (the room's remote
  member list, deduplicated by server), in a transaction with the destination's own retry budget and
  timeout. One slow peer must not block another room's traffic, and the local send must not fail because
  a remote server is down: the event is in this server's timeline either way (AGENTS.md §8).
- T-4203 device-list / PD-EDU ordering after T-4202: an event and the EDUs that accompany it in the same
  transaction (`TestUserAppearsInChangedDeviceListOnJoinOverFederation` already passes on the join path,
  so follow that precedent rather than inventing a new queue).
- T-4204 `TestFederationRoomsInvite` — `Non-invitee user cannot rescind invite over federation` times out
  in sync after 151 responses: an invite-state change made on hs2 is not relayed to hs1. Read it after
  T-4202; it may be the same missing push on a different path.
- T-4205 `TestInviteFiltering`, then re-run the whole M1 gate plus the room-version tests touched earlier.

## M2 — it keeps secrets

Gate: `TestToDeviceMessages`, `TestToDeviceMessagesOverFederation` (all three subtests),
`TestDeviceListUpdates` (all ten subtests), `TestDeviceListsUpdateOverFederation`, and the key-upload /
query family staying green.

Tasks: T-4210 `PUT /send_to_device` local delivery into `/sync`; T-4211 the same over federation with the
transaction retry semantics M1 established; T-4212 device-list `stream_id` deltas in `/sync` plus the
federation EDU (all ten `TestDeviceListUpdates` subtests fail on register-time sync checks, so read one
before writing code); T-4213 key notary query (`TestFederationKeyUploadQuery`).

## M3 — it is a home server someone uses

Gate, in this order: read receipts (`TestReadReceipts…`), threading (`TestThreadedReceipts`,
`TestThreadSubscriptions`, `TestThreadReceiptsInSyncMSC4102`), URL preview, knocking
(`TestKnocking`), `TestBannedUserCannotSendJoin`, `TestFederatedEventRelationships`, media preview,
`TestServerNotices`.

Each of M1/M2/M3 ends with a full serial Complement run and a summary file; the tally in
`docs/compliance-matrix.md` moves only with the log to point at. If a gate cannot be met, it is reported
as partial with the failing names — not softened.

---

# Heading toward a product: M3 triage and what M4 has to inherit (2026-11-09)

Written at `1d487ae`, after the first trustworthy full run of the milestone work (**155 passing / 64
failing / 1 skipped**, serial, fingerprints zero). It is deliberately short: its job is to make the next
session's first hour productive, not to restate the milestone definitions above.

## Where the milestones actually stand

| Milestone | State | What "done" still means |
|---|---|---|
| **M1** two servers talk | **1 of 4 gate tests** (`TestOutboundFederationSend`), 3 remaining | T-4207 `/messages` paginating from a remote server, T-4206 the backfill loop that actually terminates, the invite relay behind `TestRoomsInvite` / `TestFederationRejectInvite` / `TestInboundFederationInviteOverFederationRoomV11` |
| **M2** keeps secrets | local ✓ (`TestToDeviceMessages`), federated happy path ✓, `stopped_server` ✓ | T-4212 `interrupted_connectivity` (a 3-second outage costs the message; backoff curve measured at 2s/4s/8s), T-4204 `/send/{txnId}` EDU queueing |
| **M3** the daily-driver features | barely started | see the triage below |
| **M4** | not started | its scope is defined in the milestone plan above; read it before scheduling, it was written before any of this code existed |

**Two things gate everything else and they come first:**

- **T-4213 `TestPushRuleRoomUpgrade`, a regression from this session.** A remote join to an upgraded room
  returns 500 because a transient key-fetch failure was still remembered when a credential needed
  verifying. Until this is green the tree is not "155 and improving", it is "154 and one of mine".
- **T-4212 `interrupted_connectivity`.** The last piece of M2's gate.

## M3 triage, ranked by what a person notices

The 64 failures are not 64 problems; they are a handful of families. Read
`.agent-work/complement-failures-20261109-m1m2-clean.txt` (the trustworthy list) and group it before
choosing anything. Ranked by user-visible consequence, cheapest first:

1. **Room membership edge cases** (`TestUnbanViaInvite`, `TestCannotSendNonLeaveViaSendLeaveV1`,
   `TestFederationRejectInvite`, `TestJoinViaRoomIDAndServerName`). These are small rules with big
   consequences — a ban that a re-invite silently lifts is a safety bug, not a UI bug. Probably a day.
2. **Device-list announcements over federation** (`TestDeviceListsUpdateOverFederationOnRoomJoin`,
   `TestUserAppearsInChangedDeviceListOnJoinOverFederation`). The EDU exists and the hook exists; what is
   missing is the *trigger* — a join must announce the joining user's device lists. Without these,
   cross-server encrypted rooms keep using stale keys, which is how "it works locally and breaks
   federally" reaches a user as "Element won't decrypt".
3. **`/messages` history depth** (shared with M1: T-4206/T-4207). The one users name: join a room, see
   the last dozen messages, and be unable to scroll. It is also the reason an encrypted room shows red
   decrypt-error bubbles for history.
4. **Media URL preview** (`TestMediaURLPreview*`) and **push rules/knock room versions** — real but
   isolatable; nothing else depends on them.
5. **Threads** (`TestRoomMessages*` relation filtering) — last, because it depends on relation indexing
   decisions that deserve their own design note.

## What M4 inherits, so that it can be scheduled honestly

Three debts from M1–M3 that M4 should not discover by accident:

- **Federation observability.** Today "the queue is holding your message" and "the message is gone" look
  identical to an operator. The queue already stores `attempts` and `last_error`; nothing shows them. A
  `/_lattice/federation/queue` admin endpoint and one log line per state change would have shortened this
  session by hours.
- **Error honesty across the federation boundary.** `M_UNKNOWN` with a 200-shaped code on paths where the
  fault is an upstream server (see T-4213's fourth point) and where the spec wants `M_RESOURCE_LIMIT_EXCEEDED`
  or an `errcode` that clients can act on.
- **`/state` and `/event` for a remote server** — 501 today, and the reason the backfill work (T-4206)
  cannot be finished by tuning the client side alone.
