# Lattice spec coverage

Target: **Matrix spec v1.19**, default room version **11** (the specification recommends 12;
creating stays on 11 until the creators-privileged semantics are the default people expect),
supported room versions **1–12**, including the room id that is the hash of a room version 12
room's create event. The endpoint index
lives in [`internal/coverage/SPECS.md`](../internal/coverage/SPECS.md) and the tally is
regenerated with `go run ./cmd/endpoints -out ENDPOINTS.md`:
**180 of 234** indexed endpoints implemented, **41** routes the index does not name.

Two gates. This repository's own suites - **139** client-API tests (`tests/compat`), **84**
two-server tests (`tests/federation`), **70** offline unit tests (`tests/unit`). And Complement's
`tests` package (91 top-level tests): the last complete run, 2026-11-04, is
**61 green, 30 red** (it needs `-parallel 2`; see _Complement_ in
[docs/TODO.md](TODO.md)). A run of the join/leave/knock/restricted/room-version/v12-MSC group
after room version 12 landed has two red: the application-service bridge, and a restricted
join that is expected to fail over in a draft-MSC3787 room.

| Area | Status |
|---|---|
| Client-Server: versions / well-known / capabilities | done: `/versions` (with `unstable_features`), `/.well-known/matrix/{client,server,support}`, `/capabilities` (`msc3026` blank user ids, `msc3440` sync without filter, `msc3890` `?user_id=` on the membership endpoints, `msc4222` `/joined_members`). `GET /v1/auth_metadata` is routed and answers a reasoned `M_NOT_FOUND`: this server does not use OAuth. |
| Client-Server: registration / login / UIA / devices / profile | done for what this server authenticates with: UIA (with flow discovery on `GET /register`), `m.login.password`, `m.login.dummy`, `m.login.registration_token` plus its validity endpoint, `m.login.token`, `GET /register/available`, password change, deactivation, refresh tokens and `logout/all`, sessions bounded by expiry; devices (list/get/rename/delete, `delete_devices`), profile with arbitrary keys. Not here: SSO/OIDC (`/login/sso/redirect*`, `/login/get_token`), third-party identifiers (email/phone, `requestToken` flows, 3PID binding and unbinding, password reset through them) and appservice login, which refuses with a reason. The account lock/suspend and whois routes are this server's own extensions. |
| Client-Server: rooms / membership / events | done: create (presets, `creation_content`, power-level overrides, initial state, alias), join by id or alias with `server_name`, knock, leave, invite, kick, ban, unban, forget, send with transactions, state (including a membership written through the state endpoint, which the room's rules judge like any other membership), redact (`/redact`, the `PUT /send` form, and stripping of what the redaction covers), `/messages` (filters, bounds, history visibility), `/context`, `/event/{id}`, `/members`, `/joined_members`, `/joined_rooms`, `/mutual_rooms`, aliases, room directory (`/publicRooms` GET and POST, visibility per room), upgrades, reports (room, event, user), room summary (`/v1/room_summary/{idOrAlias}` and `/rooms/{id}/summary`), spaces through `/hierarchy`. Client `timestamp_to_event` is served; the walk picks the wrong event in a forked federated history (`TestJumpToDateEndpoint` red). The legacy `/events` stream and `initialSync` are not served. |
| Client-Server: relations / threads / search / spaces | partial: `/relations` (relation type and event type selection, `from`/`to`/`dir`, no `recurse`), `/threads`, bundled aggregations in `/sync` and `/messages` (`m.relates_to`, `reply_to`, latest edit and replacement resolution, reaction counts), `/search` over room events (substring match on the body, room/sender/type filters, four orderings, paginated) - no full-text or stemmed index. Spaces are `/hierarchy` plus `m.space.child` and the join rules that read them; there is no `/spaces` endpoint, and the specification never standardised one. |
| Client-Server: `/sync` (classic) | done with four holes: joined/invited/left/knocked rooms, state, timelines with `limited`/`prev_batch`, account data and room account data, tags, receipts (threaded ones carried through), typing, device lists, `to_device`, unread and per-thread notification counts, filters (`room`/`state`, limits, `not_types`/`not_senders`/`senders`), since-token expiry handled by the room-range rule. Holes: `lazy_load` is accepted and the member state is served in full, `m.ignored_user_list` does not filter a timeline, there is no `state_after`, and the top-level `presence` and `org.matrix.msc3814.e2ee` sections are declared and never filled (presence is stored and answered per user by `GET /presence/{userId}/status`, not streamed). |
| Client-Server: Simplified Sliding Sync (MSC4186) | not implemented. No endpoint is served under an `org.matrix.msc*` path, and `/sync` is the only sync. |
| Client-Server: media | done: authenticated `v1` download/thumbnail (with the filename variants), the legacy `v3` paths, `v1/config`, `preview_url`, uploads both synchronous and async (`v1/create` + `commit`), size and rate limits, remote media over federation. Not here: `POST /_matrix/media/v1/report`. |
| Client-Server: E2EE | done: `/keys/upload` (device keys, one-time keys, fallback keys), `/keys/query`, `/keys/claim`, `/keys/device_signing/upload`, `/keys/signatures/upload`, `/keys/changes`, key backup versions and keys (with the delete endpoints), to-device, and the matching federation `user/keys/query`, `user/keys/claim`, `m.device_list_update` and `m.signing_key_update` EDUs. |
| Client-Server: push | partial: push rules (get, set, delete, defaults), pushers (set/list/delete, `http` and `email` kinds, pushkey validation), `/notifications`, rules evaluated when an event is appended and recorded as unread and highlight counts. Not here: the **Push Gateway API** - no `POST /_matrix/push/v1/notify`, nothing is delivered to a push gateway, so a pusher is a row and a count rather than a notification reaching a phone. |
| Server-Server: keys / signing | done: `GET /_matrix/key/v2/server` (with `{keyID}`), notary `POST /_matrix/key/v2/query` and the per-server form, key caching and rotation, `X-Matrix` authentication on every inbound request, `GET /_matrix/federation/v1/version`. |
| Server-Server: join / leave / invite / knock | done: `make_join`/`send_join` v1 and v2, `make_leave`/`send_leave` v1 and v2, `make_knock`/`send_knock` v1 and v2 plus the knock relay between servers that hold a copy, `/event_auth`, restricted joins with `join_authorised_via_users_server` (the authoriser's membership and invite power are checked; **the authoriser's server signature is not** - see the open item in TODO), `/hierarchy` in both directions, `/state_ids`, `/event`, `/event_auth`. Not here: the dedicated invite endpoint `PUT /v1|v2/invite/{roomId}/{eventId}` (so no invite rejection or rescinding over federation), `exchange_third_party_invite`, `3pid/onbind`. A peer's `GET /v1/state/{roomId}?event_id=` **is** asked - it is how a gap in the history is settled (see the state-resolution bullet) - while this server still answers that question at its own, nonstandard path (see deviations). |
| Server-Server: transactions (`/send`) | done: v1 and v2 `send`, EDUs for typing, receipts, to-device, device lists and signing keys; presence is accepted and dropped by decision (this server keeps presence local); auth-chain gathering with fetches of events this server lacks, rejected-event bookkeeping, server ACLs applied to what arrives and to what is sent. The rejection bookkeeping is incomplete: `TestCorruptedAuthChain`, `TestInboundFederationRejectsEventsWithRejectedAuthEvents`, `TestUnrejectRejectedEvents` and the v6 bad-JSON case are red. |
| Server-Server: backfill / missing events | done both directions: `backfill`, `get_missing_events`, `timestamp_to_event`, `/event_auth`, history-visibility rules applied to what is handed over. |
| Server-Server: media | done: `media/download` and `media/thumbnail` (with the size variants), remote media cached and served. |
| Appservice API | **not implemented**: no `/_matrix/app/v1/*` (ping, transactions, user and room queries, third-party lookups), no registration files or namespaces, no `m.login.application_service`, no `directory/list/appservice`. Bridges cannot be attached; `TestJoinFederatedRoomFromApplicationServiceBridgeUser` is the Complement case. |
| Admin API | this server's own small set, not a Synapse-compatible subset: account flag get/put (lock, suspend), `/v1/admin/reports`, `/_synapse/admin/v2/users/{userId}`, `/_synapse/admin/v1/whois/{userId}`, `/_synapse/admin/v1/maintenance`, plus the CLI (`lattice user`, `-admin`). No user listing or creation over HTTP, no room administration, no purge. |
| Auth: MSC3861 delegation | not implemented: no OIDC, no delegation, no MAS management API. Login and registration are local. |
| Unstable MSCs | none served under MSC paths. Four capability flags are advertised (above); MSC3771's `unread_thread_notifications`, MSC3390's threaded receipts, MSC3083's restricted joins and MSC2965-style discovery are in the stable shape they reached. Not here: delayed events (MSC4140), thread subscriptions (MSC4306), invite filtering (MSC4155), faster joins (MSC3902), owned state (MSC3757), account-data deletion (`DELETE` on account data is not routed), `m.fully_marked`. |
| Calls: MatrixRTC (MSC4143, MSC4195) | not implemented: no `/rtc/*`, no SFU configuration. The pieces a legacy `lk-jwt-service` needs - `openid/request_token` and federation `openid/userinfo` - are served. |
| Operations | rate limiting (per-address and per-token buckets with `M_LIMIT_EXCEEDED`), session and refresh-token expiry, media and URL-preview limits, OpenAPI generation from the handler annotations (`make swagger`), Docker and compose assets under `deploy/`, the CLI, the endpoint coverage tool (`cmd/endpoints`). Not here: message retention (`m.room.retention`), fuzz targets, a load-test harness. |

## Deviations and known wrongness

- **A room version 12 room is understood, and its state is resolved v2.1.** A peer's claim about
  the state at an event is settled against this server's own view by the resolution algorithm;
  the outcome is written for a key this server does not hold and refused for one it does - so a
  peer that has simply never been told about a change cannot undo it by answering a question.
  The open half is that there is still no per-event state snapshot, which the last bullet below
  covers.
- **Federation room state is served at the wrong path.** This server registers
  `GET /_matrix/federation/v1/state/{roomID}/{eventID}`; the specification asks
  `GET /_matrix/federation/v1/state/{roomId}?event_id=`. A peer that asks the specified way gets
  nothing. Same for v2.
- **Federation public rooms.** `GET /_matrix/federation/v1/publicRooms` is served; `POST` at v1
  is not, and the extra routes are registered at `/v3/publicRooms`, which the index does not name.
- **`timestamp_to_event` on the client side** returns an event later than the asked moment, or
  nothing, in a history that forked (`TestJumpToDateEndpoint`).
- **Lazy loading is not lazy** and **ignored users do not filter a timeline**, both accepted and
  answered in full, which costs payload size and shows blocked users.
- **Presence is local by design**, not by omission: an inbound `m.presence` EDU is accepted and
  dropped, because the specification gives no way to say "unsupported", and nothing is sent out;
  the `/sync` `presence` section is never filled either. `TestRemotePresence` is red for that reason.
- The **authorising user's server signature** on a restricted join is not verified, and there is
  no per-event state snapshot, so "the state at an event" is judged from the events cited plus
  this server's current state (both open items, listed in [docs/TODO.md](TODO.md)).

## The same document for the reference implementation

The comparable document for the Rust homeserver this project is measured against is
[spec-coverage.md](spec-coverage.md); the side-by-side reading of the two is in
[coverage-comparison.md](coverage-comparison.md).
