# Video calls over LiveKit — the plan

Split out of TODO.md on 2026-11-04. The MatrixRTC work (MSC4143/4195) is planned here with its own
L-numbered steps, so it does not collide with the release-plan numbering in
[STEPS-LOG.md](STEPS-LOG.md).


---

## Video calls over LiveKit — the plan (added 2026-11-03)

Element Call puts media in a LiveKit SFU, and the SFU admits nobody who cannot show a
JWT signed with its secret. On Synapse that JWT comes from a second service,
`lk-jwt-service`, because Synapse cannot be made to sign LiveKit tokens. It does not
have to be a second service any more: MSC4195 was accepted and merged, and its
endpoints are **homeserver** endpoints —
`POST /_matrix/client/v1/rtc/livekit/get_token` and a federation twin. Lattice can be
the thing that signs. Then the deployment is Lattice plus one LiveKit instance, and
the wiring that service needs from Synapse — MSC4502 application-service scopes,
MSC4512 request proxying, an MSC3861 OpenID token exchange, an admin-API hop to ask
"is this user in this room" — is not needed at all, because the access token in the
request header already says who is asking and room membership is a row in this
database.

What an operator passes is three flags:

```sh
bin/lattice -livekit-url wss://livekit.example.org:7880 \
            -livekit-key devkey -livekit-secret <64 random chars>
```

Three rather than two, because the SFU trusts a token only for the shared secret that
signed it: no configuration makes a URL and a key enough. Everything else is derived.
The URL announced to clients is exactly the one typed, never whatever the container
reports for itself — that is the whole of `lk-jwt-service` issue #161. With the three
unset the feature is off: `/rtc/transports` answers an empty list and
`/rtc/livekit/get_token` answers `M_UNRECOGNIZED`, like every other endpoint here that
is not implemented. `LATTICE_LIVEKIT_URL` and its siblings work in a container for
free, the way every other flag does.

### What is settled, and whose job it is

Verified 2026-11-03 against the proposals and LiveKit's `config-sample.yaml`. Statuses
from the MatrixRTC-in-2026 material dated 2026-10-19: MSC4140 merged, MSC4354 merged,
MSC4143 merged, MSC4195 merged, MSC4196 merged as an application.

| Piece | What it puts on the homeserver |
|---|---|
| MSC4143 MatrixRTC core | `m.rtc.slot` **state** events (`state_key` = `{application}#{slot}`, `status` `open`/`closed`, an `application` and optional `encryption` object) gate what may run; `m.rtc.member` **room** events say who is in it. A member counts as joined only while their member event is still sticky, an open slot exists, and their room membership is `join`. |
| MSC4143 discovery | `GET /_matrix/client/v1/rtc/transports` (unstable twin `org.matrix.msc4143`) is how a server announces its SFU now. The `org.matrix.msc4143.rtc_foci` key in `.well-known/matrix/client` is the older route, and Element Call ≥ 0.24.0 (2026-08-18) stopped reading it — do not add it. |
| MSC4354 sticky events | A PDU carries a **top-level** `sticky: {"duration_ms": …}`, which the client asks for through a `sticky_duration_ms` query parameter on `PUT /send/{type}/{txn}`. The server clamps the duration to 1 h, times it from `min(received_ts, origin_server_ts)`, delivers unexpired ones in a `sticky.events` sync section with `unsigned.sticky_duration_remaining_ms`, applies no history-visibility check to them, and **pushes its own unexpired sticky events to joined servers**. |
| MSC4195 LiveKit transport | The homeserver signs the JWT **and ensures the LiveKit room exists**, creating it when needed. Grants are prescribed: `sub` is the derived participant identity, `video.room` the derived room name, `roomCreate` **false**, `roomJoin` true, `canPublish` true for local users, `canSubscribe` true, `hidden` false; `nbf` is now and `exp` is short, because self-hosted LiveKit cannot revoke a token yet. Users of other servers are served by relaying to that server's federation endpoint. |
| MSC4140 delayed events | The membership deadman switch: a client schedules a delayed `m.rtc.member` leave, and MSC4195 lets it delegate keeping that event alive to the homeserver, which then watches the SFU's participant webhooks rather than the client's connection. |
| MSC4196 the `m.call` application | The application type an ordinary client uses, and its default slot: `m.call`, slot `m.call#room`. |
| MSC4502 / MSC4512 | Application-service scopes and endpoint proxying: Synapse's way of letting the external service answer those endpoints. Named here so nobody integrates `lk-jwt-service` into Lattice by mistake — with the endpoints in this process, both are moot. |

Room names and participant identities are pseudonymised against the SFU, and both are
one hash:

```
livekit_room_name      = base64raw( SHA256( CanonicalJSON( [ room_id, slot_id ] ) ) )
livekit_participant_id = base64raw( SHA256( CanonicalJSON( [ sender, member.id ] ) ) )
```

`matrix.CanonicalJSON` exists already and `base64.RawStdEncoding` is the unpadded
form. Two rules to keep: never map two slots to one LiveKit room, and always map the
members of one slot to the same LiveKit room — that is what keeps the number of
connections proportional to the number of servers rather than of members.

Two things are pinned here because a review had them backwards, and backwards here means
an interop failure rather than a style difference. The client asks for a token with a
flat `member_id`, not with a `member` object carrying a `device_id`: that object shape
and the three-element identity hash around it belong to the legacy OpenID/`sfu/get`
flow the standalone service still supports. And issuance must not be gated on an open
slot or a live member event, because the MSC's own sequence has the client connect to
the SFU before it publishes the member event that records the connection — the check
belongs at the SFU's participant-joined webhook, with a short `exp` covering the gap.

### What is already in place

- `PUT /rooms/{roomId}/send/{eventType}/{txnId}` and `/state/{eventType}/{stateKey}`
  accept any event type, and `AppendEvent` builds the PDU itself, so `m.rtc.slot` and
  `m.rtc.member` already flow through this server today — as ordinary events.
- To-device messages are relayed, which is what `m.rtc.encryption_key` and the
  `m.per_member` media keys of MSC4143 ride on. Encrypted media needs nothing here.
- `github.com/golang-jwt/jwt/v5` is already a direct dependency: a LiveKit access
  token is an HS256 JWT, so signing one adds no dependency.
- MSC3861 `POST /user/{userId}/openid/request_token` and MSC3288
  `GET /_matrix/client/v3/voip/turnServer` are routed. The first means a separately
  deployed `lk-jwt-service` already gets most of what it needs from Lattice; it is the
  membership check that needs an application service, which is exactly the hop this
  plan removes.
- `power.DefaultState` is 50, so `m.rtc.slot` is already gated the way MSC4143 expects.

One thing is missing structurally: `EventDraft` is (room, type, state_key, sender,
content), so **there is no place for a top-level `sticky` object** and the current
event path cannot carry MSC4354 at all. That is the largest piece of work below.

### The steps (numbered L1.. so they do not collide with the release plan above)

Work them in the order **L1, L2, L3, L5, L4, L6**: everything up to and including L5
has to be reliable with one server and one SFU before cross-server token relays and
delegated leave add a second failure domain to every one of them.

#### L1. Announce the SFU and sign tokens for local users

- [ ] `internal/service/livekit.go`: the two derivations above, and `JoinToken`
      building the MSC4195 grant set. Pin the derivations against vectors from outside
      this repo, and they already line up: with `matrix.CanonicalJSON` and
      `base64.RawStdEncoding`, verified 2026-11-03,
      `["!roomid:example.com","slot1234"]` hashes to
      `O8437W3+jmzMVjoIP3tNwbm+XxHQk2iKpOA7aqw3qSc` (MSC4195's own example) and
      `["@alice:example.com","memberABC"]` to
      `M3VnsLXrkbxIDINXO64u8PZzFyD9ZYFiQULR2dshWYs` (the one the Rust service tests).
      A silent change to a room name puts the participants of one call in different
      LiveKit rooms, and the bug looks like a media failure.
- [ ] Take the request and response shapes from the current MSC and the current
      clients, not from an older snapshot: `room_id`, `slot_id`, `url`, `member_id`
      and an optional `server_name`, all at the top level — the flat `member_id` is
      what the JS SDK's `LivekitGetTokenRequest` and the reference service's request
      structs use. A `member` object carrying `device_id` belongs to the legacy
      OpenID/`sfu/get` flow, which is also why that service still carries a legacy
      identity built from `(mxid, device_id, member_id)` rather than the current
      `(mxid, member_id)` pair. Copying the legacy shape by mistake means a client
      sends one thing and the server hashes another.
- [ ] Validate the three flags at startup rather than at the first call: the URL must
      parse, a `ws://` URL is allowed but warned about, and an empty key with a set
      secret (or the other way round) is a start-up failure rather than a call that
      fails an hour later. The startup line that reports settings taken from the
      environment prints flag names — `livekit-secret` appears there, its value never
      does — and that is the rule to keep.
- [ ] Routes: `GET /_matrix/client/v1/rtc/transports` (plus the
      `unstable/org.matrix.msc4143/rtc/transports` twin) answering
      `{"transports":[{"type":"m.livekit","url":…}]}`, empty when unconfigured; and
      `POST /_matrix/client/v1/rtc/livekit/get_token` plus the
      `unstable/io.element.msc4195/rtc/livekit/get_token` twin that current clients
      call. Rate limit both like the other expensive writes.
- [ ] The rejections exactly as the MSC has them, and no more: requester not joined
      → 403 `M_FORBIDDEN`; `server_name` naming another server → 403 until L4 exists;
      `url` that is not the configured SFU → 400 `M_INVALID_PARAM`. Compare the URL
      after normalising scheme and host, not byte for byte. Do not invent further
      gates at issuance time: MSC4195's own sequence has the client get the token and
      **connect to the SFU before it publishes its `m.rtc.member` event**, so an
      issuance that demanded an open slot or a live member event rejects the join it
      is supposed to authorise. Slot and member validity are enforced where the MSC
      puts them — at the SFU's participant-joined webhook, in L5 — with the short
      `exp` bounding the window in between.
- [ ] Identity comes from the access token. The `member_id` in the body says which of
      the requester's own memberships to derive the participant identity for; the
      sender half of that derivation is the authenticated user, never anything in the
      request. Same rule on the federation endpoint, where `user_id` must match the
      origin server's name. A test that one user cannot obtain a token for another
      user's `member_id` is the one that keeps this true.
- [ ] `/versions`: advertise `org.matrix.msc4143` and `org.matrix.msc4195` in
      `unstable_features`, and only while configured. `org.matrix.msc4354` waits for
      L2 — advertising stickiness the server cannot yet deliver invites clients to use
      a path that silently drops their member events.
- [ ] `deploy/livekit/`: a compose file and a `livekit.yaml` whose `keys:` match the
      flags, `room.auto_create: true` for now, and the port list below. Flags without
      a runnable SFU are not easy, so this belongs in the step and not in a footnote.
- [ ] `tests/compat/livekit_test.go`: the transports body with and without
      configuration; a table over the get_token accept/reject matrix; the issued JWT
      parsed back and its grants asserted field by field; and the impersonation case
      above.
- [ ] Done when Element Call in a browser completes a two-user local call against
      Lattice and a real LiveKit.

#### L2. Sticky events (MSC4354) — what makes a call reliable

Skipping this looks harmless and then shows up as a call that looks empty to whoever
joins late: `m.rtc.member` reaches them only through the timeline window, and MSC4143
tells clients not to connect to the transports of a member whose stickiness they cannot
compute.

- [ ] `EventDraft.Sticky` carrying `duration_ms`, written into the PDU as `sticky`
      (unstable `msc4354_sticky` while that is what clients send). The hash covers it
      without a change: `hashWithout(event, "unsigned", "signatures", "hashes")`.
- [ ] `sticky_duration_ms` on the send endpoint (unstable
      `org.matrix.msc4354.sticky_duration_ms`), strictly positive, clamped to
      3 600 000; store the expiry next to the event, computed from
      `min(received_ts, origin_server_ts)` so a forged future origin timestamp cannot
      extend a membership. The expiry is a column and expiry is a query — not a map of
      Go timers — because the set of live memberships has to be reconstructable after a
      restart, a database restore, and a sync resuming from an old token, and a timer
      list is none of those things.
- [ ] Sticky lifetime is its own thing, not the timeline's: an event can be current in
      the sticky set while outside the synced timeline window, and expired from the
      sticky set while staying in history. Retention or pruning of the timeline must
      not be what decides whether a membership is live. And no dedupe by sender: the
      clients key memberships on `(sender, type, content.sticky_key)`, keeping the last
      to expire, so several devices of one user are several memberships and collapsing
      them by user ID empties a call when someone's second device joins.
- [ ] A `sticky.events` section per joined room in the sync response: deduplicated
      against the timeline, the RoomFilter's timeline filter applied **before** that
      dedupe, ignored users omitted, history visibility not applied, capped (100 is the
      suggested limit), each entry carrying
      `unsigned.sticky_duration_remaining_ms`.
- [ ] Federation: push this server's own unexpired sticky events to joined servers,
      including when a new server joins, and re-evaluate soft-failed unexpired sticky
      events when the authorising state changes. A per-sender cap (60 suggested), the
      oldest expired first, so one client cannot fill the send queue.
- [ ] Tests: expiry arithmetic and clamping as a unit table; a compat case that sends
      a member event, syncs with `timeline: {limit: 1}` and finds the event in
      `sticky.events`; cases for the dedupe and for an ignored sender; and one that
      restarts the server between the send and the sync, which is the case an
      in-memory design fails.
- [ ] Four landings rather than one commit: representation and persistence, then
      expiry and sync, then federation, then filtering and redaction with the compat
      cases. The PDU stays the source of truth throughout — an index for expiry, never
      a second copy of the event.

#### L3. Slots, and letting an ordinary member start a call

- [ ] Decide and record whether Lattice seeds an open `m.call#room` slot at room
      creation. `state_default` 50 means a member with power 0 cannot send `m.rtc.slot`
      today, and MSC4143 both keeps slots moderator-gated and recommends creating slot
      events at room creation where possible. Find out what Element Call sends in a
      room where the caller is a plain member before choosing.
- [ ] Nothing to do for room upgrades, and worth knowing before going hunting:
      `UpgradeRoom` (`internal/service/directory.go`) copies every state event except
      `m.room.create`, `m.room.member` and `m.room.tombstone`, so an open slot already
      survives an upgrade as MSC4143 recommends. A compat case that upgrades a room
      mid-call and reads the slot back in the new room would pin it.
- [ ] Check redaction of a sticky member event against what MSC4354 says: redaction
      strips `sticky_key`, so moderation by redaction works only for clients that
      remembered the event ID. A compat case, not a comment.

#### L4. Federation: tokens for users on other servers

- [ ] `POST /_matrix/federation/v1/rtc/livekit/get_token` (plus the unstable twin),
      behind the existing X-Matrix middleware: reject when `user_id` is not of the
      origin server, when this server is not in the room, or when that user is not
      joined; 400 when `url` is not one of ours. Relay a peer's 403 and 400 to the
      client, and turn anything else into 502 `M_UNKNOWN`.
- [ ] The client half: when `server_name` names someone else, sign the federation
      request, drop `server_name`, add `user_id`.
- [ ] Publishing from remote users: MSC4195 recommends allowing it, and the upstream
      service allows it by default. Allow, and name the flag here if it is ever
      narrowed.
- [ ] Worth understanding before writing it: this server's authorization can only ever
      cover this server's users and this server's SFU. A token for a foreign SFU comes
      back signed by the *other* homeserver, and that server decided the grants — the
      MSC says to accept external SFUs "with care" for exactly this reason. So there is
      no cross-server grant model to design here; there is a trust decision about which
      servers' signatures an SFU accepts, and `-livekit-url` naming one SFU keeps that
      decision small.
- [ ] Tests: a two-server case in `tests/federation`, including a banned user whose
      relayed request is refused, and a peer that answers malformed JSON or times out
      (502 `M_UNKNOWN`, nothing leaked). What goes back to a client is the peer's 403
      or 400 and nothing else: no signing material, no internal SFU address.

#### L5. Room creation, and getting rid of participants

- [ ] Create the LiveKit room before answering get_token — MSC4195 requires it to exist
      — then set `room.auto_create: false` in the example config. With auto-create on,
      any holder of any token can create rooms on the SFU, which is also why
      `roomCreate` must stay false in the grants. Make the creation idempotent: two
      users asking at once both find no room, and one of them will get an
      already-exists error back. That case continues after checking the room is the one
      expected; every other creation error is a failure, not evidence of a race.
- [ ] Remove the participant from the LiveKit room when a user leaves or is banned,
      with `revoke_token_ts` set to now. Removing a participant is not revoking their
      token: self-hosted LiveKit cannot revoke yet, so a removed participant can walk
      back in on the same unexpired JWT. That is why the enforcement point is the
      SFU's participant-joined webhook — re-check room membership on every join and
      remove again — and why `exp` stays short.
- [ ] Verify the webhook the way LiveKit signs it, not with a compare of a shared
      secret: the POST carries `Authorization: Bearer <JWT>` minted by the SFU from an
      API key, so it is parsed with the configured secret and the key checked against
      `-livekit-key` — which is exactly what the reference service's webhook receiver
      does. Handlers then have to be idempotent and safe against duplicate and
      out-of-order deliveries, and off the request-serving goroutines.
- [ ] Reconcile rather than trust: webhooks are not a durable log, so one missed
      delivery must not leave someone connected for the rest of the hour. Periodically
      list the SFU's participants and compare them against MatrixRTC state, removing
      participants whose Matrix membership or open slot no longer holds and reporting
      LiveKit rooms nobody is in. This belongs on the existing maintenance loop
      (`StartMaintenance`), not in the request path, and it is what makes a Lattice or
      LiveKit restart recover on its own.
- [ ] Tests for exactly the parts that are easy to get wrong: a participant removed
      after a leave who then reconnects with the same unexpired JWT is removed again
      (the interesting half — that `RemoveParticipant` works is the easy half); a
      webhook POST signed with the wrong key is refused; two simultaneous token
      requests for one room end with one room and two tokens; a call survives a restart
      of Lattice and a restart of the SFU.
- [ ] Decide the dependency here rather than by accident, and the answer is no: the
      official `github.com/livekit/server-sdk-go/v2` exists, and its `go.mod` requires
      moby/buildkit, containerd, pion/webrtc, cel-go, psrpc, grpc, protobuf, zap and a
      cgo `opus` binding — a few hundred modules for the two RoomService calls this step
      needs (`CreateRoom`, `RemoveParticipant`), and a C toolchain in the graph while
      the release image builds `CGO_ENABLED=0`. Hand-roll those two Twirp calls on the
      HTTP client and JWT signing already in the tree; the SDK becomes interesting only
      if the participant listing and webhook handling above start costing more than the
      dependency would.

#### M1. A local call that survives its own edges — the gate before federation

Acceptance for L1+L2+L3+L5, all of it on one server with one SFU:

- [ ] Two accounts join, publish audio and video, and see each other.
- [ ] A third account joining late sees the two already in the call — the case sticky
      events exist for.
- [ ] Leaving removes the participant; rejoining works; a ban removes the participant
      and a ban mid-call removes them while connected.
- [ ] Closing the slot ends the call for new joiners — enforced where the decision is
      made, in the participant-joined check, with the slot's open state as part of what
      that check requires — while issuance itself keeps behaving as L1 defines it.
- [ ] Restarting Lattice loses no live membership, because the expiry is in the row.
- [ ] Restarting LiveKit leaves Matrix state alone, and the reconciliation loop clears
      whatever the SFU lost.
- [ ] Killing the webhook delivery (point `webhook.urls` at a black hole) still ends
      the membership within the reconcile period rather than never.

#### L6. Delayed events, and delegating the deadman switch

- [ ] MSC4140's futures first, as their own service file: schedule a delayed event,
      restart it, trigger it. It is a general feature — scheduled messages want it too
      — and MSC4195's delegation sits on top of it.
- [ ] `POST /_matrix/client/v1/rtc/livekit/delegate_delayed_leave`, matching
      `delay_id` to an event this same user scheduled in this same room, one
      delegation per (room, slot, member, MXID), a later `delay_id` invalidating the
      earlier one.
- [ ] The same webhook handler L5 verifies and dispatches, extended to keep restarting
      the delegated event while the participant is connected and to send the leave when
      they are gone. Webhooks carry no delivery guarantee, so the reconciliation loop
      polls the SFU as well as listening to it.

### TURN, ports, and what the operator still runs

The answer to "do we have to integrate a TURN system as well" is no, and it is worth
writing down why not. LiveKit ships a TURN server and gives clients the credentials
itself, so Matrix is nowhere in that path:

- `turn: {enabled: true, domain: turn.example.org, udp_port: 3478, tls_port: 5349,
  cert_file/key_file or external_tls, relay_range_start/end,
  per_user_relay_allocation_limit, ttl_seconds: 300}` — embedded, and authenticated
  against the SFU's own signalling connection rather than against Matrix.
- A separate coturn instead: `rtc.turn_servers: [{host, port, protocol: tls, secret,
  ttl}]` on the LiveKit side, `use-auth-secret` plus `static-auth-secret` on the
  coturn side. LiveKit mints the time-limited credentials and hands them out; its
  sample config documents `secret_file`, a `ttl` defaulting to 4 h and capped at 24.
- Ports for the firewall: 7880 signalling (behind TLS), 7881 ICE/TCP, 50000-60000 UDP
  or a `udp_port: 7882-7892` mux, 3478/5349 for TURN. The URL `-livekit-url` announces
  is the TLS-wrapped 7880, not a media port.
- Lattice's `GET /_matrix/client/v3/voip/turnServer` — already routed, currently
  answering empty credentials — belongs to legacy 1:1 `m.call.invite` VoIP, which
  MSC4531 deprecates. Filling it from `-turn-*` flags is a separate optional item for
  whoever still runs those calls; it is not part of MatrixRTC and should not be
  presented as if it were.

"Provide the LiveKit instance and it all works out of the box" is honest once L1..L3
are done, with three things left on the operator's side: `keys:` matching
`-livekit-key` and `-livekit-secret`, `room.auto_create: false` from L5 on, and
`webhook.urls` from L6 on.

### Open, and needing a real deployment

- Token lifetime. The MSC wants a short `exp` because self-hosted LiveKit cannot
  revoke, while a call longer than the token needs a re-issue. Start at the hour the
  MSC's own example uses, then measure what Element Call actually asks for.
- Whether Element Call opens the slot as a plain room member (L3), and whether any
  client still reads `rtc_foci` from `.well-known` in practice: the guides disagree
  with each other, and an operator following either one gets a different answer.
- Which unstable prefixes current clients really send — `io.element.msc4195` for the
  token endpoints, `msc4354_*` for the sticky fields, `org.matrix.msc4143` for
  transports. Serve the stable path plus the twin that the client in the field calls,
  and record here when a twin is dropped.
- `make endpoints` counts "Extra routes" for anything missing from the v1.19 index, so
  these endpoints will move that number without anything being wrong. Regenerate the
  index when a spec release includes MatrixRTC.
- Multi-SFU: `transports` is an array and MSC4195 is designed around several SFUs per
  server and per room. One URL per server first; a repeatable `-livekit-url` is a
  decision for later, not a gap in these steps.
- Failure behaviour to define per step, since Matrix and LiveKit have separate state
  and separate failure domains: LiveKit down means token issuance fails and the
  homeserver keeps working; a Lattice restart recovers stickiness from the database;
  a lost webhook is healed by reconciliation; a federation peer being unreachable
  fails remote tokens and never blocks a local call. Nothing in this feature may make
  the homeserver itself unavailable when the SFU is.
- No metrics: Lattice has no metrics subsystem, and inventing one inside a feature
  step is a decision in disguise. Failures here surface as log lines naming the room,
  the slot and the pseudonymised participant — never a JWT, a secret, or a raw
  participant identity that a log could be correlated back to. If counters are ever
  wanted, they are their own step.
- Ringing and invitations for MatrixRTC sessions (MSC4075) are not merged and are not
  part of these steps.
