## 2026-11-09 (settle) — `TestSyncTimelineGap` is a long-standing failure, not this session's regression

Asked and answered the one contradiction left open: ran `TestSyncTimelineGap` against `405ddc1`, the
commit this session started from. **It fails there too.** So the single "newly failing" entry in
`complement-delta-final.txt` is an artefact of the pinned baseline TSV being older than the test's
regression, and nothing shipped this session broke it. No revert is owed.

That leaves the session's Complement movement at **8 newly passing, 0 genuine newly failing**, and
`TestSyncTimelineGap` confirmed as the read-order test — the same defect
`TestAFilledGapLandsInTheTimelineWhereItHappened` reproduces in half a second, and the reason
depth-ordered timeline reads are the next piece of design work rather than more hole-filling.

## 2026-11-09 (final) — T-4218 was a misdiagnosis; the real defect is read order, and it is now reproducible in 0.5s

`git log` shows a revert and a follow-up; this is what they were actually for, since the messages
describe the measurement rather than the conclusion.

T-4218 — "accept an event with a hole under it, then close the hole" — is **already implemented**: the
`/send` handler refuses with the missing events named, fetches them, and **re-runs
`AcceptRemoteEvent` on the same event** (`internal/api/federation/transactions.go:291`), which is then
stored by the ordinary path in the timeline. Fill first, accept once. Three attempts at a different
shape of this — accept as an outlier, answer the peer, fill afterwards — each regressed two of the three
hole-filling Complement tests *because they replaced that path*, and each was reverted. The task is now
tick-marked as a misdiagnosis. The lesson is written down where it can hurt again: the behaviour being
"implemented" was in the tree, and the premise was a guess that was never checked against it.

What is actually wrong, and what `TestSyncTimelineGap` needs, is the room's **read order**. A room joined
with a fetch budget too small for its history, then caught up, serves its messages as
`[five six one two three four]` — the order the events arrived, because a stream position is an insertion
order and nothing else orders a timeline. Reproduced by
`tests/integration.TestAFilledGapLandsInTheTimelineWhereItHappened`, which is committed **skipped, with
its failure output in the reason**: 0.5s locally instead of 20 minutes against Complement, and it fails
at HEAD.

`PlaceInTimeline` (renumbering an event when it is linked into the history, opening the gap by moving the
room's older events down) was implemented properly and measured: 0/10 → **6/10**. Not a fix — the position
chosen for an event depends on which others have been positioned yet, so the walk order is still an
arrival order, merely better behaved. Reverted; the code is recoverable from this session's commits if
wanted, but it should not come back in that shape.

Durable fix, next: order a room's timeline by the depth the events already carry —
`persist.Event.Depth` is a populated column — and keep the stream position for what it honestly means,
which is what has been delivered to a client. Pagination tokens then have to name a place in that order
rather than a raw stream position; that is why this is a design change rather than a query change, and it
is what T-4206 and the remaining `/messages` work are waiting behind.

Also landed this session and standing: `/backfill` served and asked (`a2fea84`, unwired at the read path
and the reason is recorded in `app.go`), and a to-device message crossing a federation boundary is pinned
by a test (`1df6d53`, ticking T-4211, which had been implemented and untested).

## 2026-11-09 (late) — T-4218 measured and taken back out; `/backfill` built both ways and left unwired

Tried the whole of T-4218 at once — accept-with-hole, fill after answering, relaying filled history to
the rest of the room, and `PlaceInTimeline` filing late history where it happened instead of where it
arrived. Local suite green. Complement, run whole and diffed against the pinned baseline: **6 newly
passing, 3 newly failing**, and isolating the three left one real loss — `TestGetMissingEventsGapFilling`,
which was passing before and is the test the change exists for. Reverted (`git revert 6a16f1c`), with the
revert's own message carrying what each of the three pieces was doing and what to measure before putting
it back. Confirmed back at baseline: gap filling, inbound missing events and the size test pass again;
`TestSyncTimelineGap` stays the known red.

Learned and recorded, all of it measured rather than reasoned:
  - Filling a hole is not the end of it — the other servers in the room may never hear what was fetched.
    Relaying that history is probably right and is also what broke the gap-filling test, which asserts
    about what its stub reader is told.
  - Negative stream ordering (Synapse's device for `/backfill`) is the wrong tool for hole-filling: it
    files the history below a client's `since` token, so it is stored, linked, readable by `/messages`,
    and never pushed. Complement's `MustSyncUntil` timeouts were the symptom.
  - When renumbering a room's history, shift the *older* events down. Shifting newer ones up lands one on
    the position a backwards `/messages` read starts from and the room's newest event disappears from the
    first page of its own history. Three tests failed with exactly that symptom before it was understood.
  - Holding a room's create event is not holding its history: a join hands over the state, so "we have the
    beginning" is false exactly when it looks most true.
  - `TestRoomServerACLRefusesTheExcludedServer` and `TestLocalUserJoinsARemoteRoomThroughTheClientAPI`
    both distinguish "history arrived" from "history arrived in the right place". Neither should be
    silenced to make a backfill change look clean.

T-4211 (to-device across federation) turned out to be wired and untested: now pinned by
`TestToDeviceMessageCrossesTheFederation` and ticked.

T-4206 has both ends built — `GET /backfill/{roomId}` served under the room's own permission and
visibility rules, and `Client.GetBackfill`/`Backfiller.BackfillBefore` asking it — plus the read that
wants them (`Service.backfillToFill`). Not wired: connecting it regresses
`TestBackfillBudgetLeavesAGapAskable`, because a fetch taken for one reader spends a backfill budget
another reader was counting on. `TestPagingPastAJoinedRoomsHistoryAsksThePeerForMore` sits skipped at
the finish line with its number: a joined room serves 49 of 70 messages and says nothing about the
difference. That is the next task, and it is a design question about who pays, not a plumbing job.

# Progress

## 2026-11-07 — the device-list stream as an endpoint, and a prefix the specification left behind

`GET /keys/changes` answers the question the `device_lists` section of a sync answers as "since
my token", as a range instead: whose device lists did this user have to re-read between these two
positions, and whose stopped being theirs to track. It is not a new feature so much as a second
door onto rows that already exist — one row per observer per change, written at the moment the
change happened, because permission to hear about a user is room membership *then* and cannot be
worked out afterwards. The person who most needs to be told that a room's members are no longer
theirs to track is the one no longer in that room, which is why the row names its observer rather
than its room. The conversion from rows to the two lists is shared with the sync service, so the
same fact cannot get two answers depending on which door a client used.

Both bounds are required and both must be tokens this server issued: inventing a bound the server
cannot read would report changes the client then believes it has already seen. The deprecated
`user_id` parameter is checked rather than ignored, and the answer is 403 rather than an empty
list — the rows are addressed, and the addressing is the permission.

**Then a 404 that had nothing to do with keys.** Complement's `TestKeyChangesLocal` logs in
through `POST /_matrix/client/r0/login`, and this server answered 404 `M_UNRECOGNIZED`. `r0` is
the specification's own earlier prefix; the endpoints under it are the same endpoints. Most of the
room, membership, state, sync and account-data routes already answered there, but login,
registration, `whoami`, logout, `/devices`, `/keys/*`, `/pushrules` and the unauthenticated reads
were `v3`-only — a hole that only a client old enough to ask under `r0`, or a test written by one,
would find. The unauthenticated ones needed their own registration helper, because the versioned
one wraps authentication, and requiring a token at login would make the first step of using a
homeserver impossible. The prefix is a prefix and not a catch-all: an unknown path under it is
still 404 and a known path with the wrong method still answers 405.

Verification: `tests/integration/keyschanges_test.go` (the sync section and the range endpoint
agree in both directions; a user's own upload is not reported back to them; four malformed ranges
are 400; `user_id` naming another user is 403), `tests/integration/legacyprefix_test.go` (an `r0`
login issues a working token for the same account; whoami, sync, devices, push rules,
keys/changes, the room directory and a profile read answer under `r0`; unknown path 404, wrong
method 405). `go test -race ./tests/...` and `golangci-lint` green. Complement: 20 passing, 3
failing in `RUN='TestKeyChangesLocal|TestLogin|TestRegistration|TestLogout|TestDevice|TestProfile|TestPushRule|TestVersionStructure|TestUnknownEndpoints|TestCanRegisterAdmin|TestServerCapabilities|TestSync$|TestUploadKey|TestKeysQuery|TestE2E'`
(label `r0keys`) — `TestKeyChangesLocal` green for the first time, and the three failures are the
already-recorded device-list announcement and key-backup gaps (T-4064, T-4008, T-3008e), unchanged.

## 2026-11-07 — an event type nobody qualified, and a state section that stopped hiding behind the batch

**A dot is a convention.** `PUT /rooms/{roomID}/send/{type}/{txn}` answered 400 for an event type
without a dot — `please_filter_me`, say — because this server required custom types to be dotted,
a rule written down in `docs/requirements.md` so that two servers could not invent the same name.
Nothing in the protocol requires a dot: the reserved `m.` namespace is enforced and the components
have to be usable, and an unqualified name's only hazard is a clash, which belongs to whoever
invents the name. The cost of over-strictness here was invisible from the inside and glaring from
outside: the request died before the room saw the event, so the endpoints for storing it, filtering
it, paging it and reading it back were unreachable rather than missing. Complement's sync-filter
tests send unqualified types, and this server was failing them with a 400 about naming.

**What the state section may subtract.** `/sync` was dropping from a room's `state` any state event
the timeline batch carried, on the reasoning that the client is being handed the event anyway. That
reasoning holds only when the response really does hand it over, and two cases break it: a first
sync, where the client holds nothing, and any sync whose `limit` or `not_types` filter hid the
event from the batch. In a young room the second case is total — every state event is in the batch,
so the state section came back empty. The subtraction now applies only when the client has a token
and did not ask for `full_state`, which is the same condition the "changed since your token" filter
already uses. A first or full-state sync therefore carries the room's whole state, as it always
should have; shrinking that is what `lazy_load_members` is for, and that flag is honoured since the
MSC4222 work.

One existing test was corrected rather than the server: `TestPrevStateShowsWhatAStateEventReplaced`
asserted that each topic event was served exactly once, and a first sync may legitimately serve the
same state event in both the batch and the state. Its assertions are now made per event id, which
also catches the thing it was really watching — one event claiming two different predecessors.

Verification: `tests/integration/customeventtype_test.go`, `tests/integration/syncstatebatch_test.go`
and the updated `tests/unit/roomslogic_test.go`, with `go test -race ./tests/...` and
`golangci-lint` green. Complement: `TestSyncOmitsStateChangeOnFilteredEvents` green for the first
time (a state event delivered across a federation fork, a `limit=1` type-filtered batch, and the
state section still required to carry it), and with it the whole sync and room-state set —
13 top-level tests, 0 failures, in `.agent-work/next/complement-run-statebatch.log` and
`.../complement-results-statebatch.tsv`. A full suite run follows this change so the tally next to
it is measured rather than inherited.

## 2026-11-07 — a sync whose state sits where its timeline does (MSC4222 `state_after`)

A sync batch says "here are the last N events, and here is the room's state". Those two answers
sit at different points in the room. The state has always been read at the *start* of the window,
so the events that changed it since are in the timeline and their results are not in the state —
and this server used to go further, dropping from the state any event the timeline already
carried. A client holding that pair has to replay the timeline over the state to know who is in
the room, which means it has to keep every membership event it was ever shown. That is exactly the
rule a lazy-loading filter makes impossible: the filter exists to drop membership events.

MSC4222's answer, now in the stable specification, is `state_after`: the state as of the *last*
event of the batch, in place of `state`. Answering it is one lookup here rather than a replay,
because the state each event was sent against is already stored (T-3004a) — the snapshot attached
to the newest event of the batch *is* the state at the end of the batch, that event's own change
included. Rooms whose newest event has no snapshot — history that reached this server without one —
fall back to the current state, which is the same approximation the `state` section already makes,
and it is written down where it happens rather than buried.

`lazy_load_members` on the state filter is honoured now, and only there: the reference servers key
off the state filter, and a timeline is a list the client asked for, not a state section that can
be trimmed. Under it, a batch of one message sends that sender's membership and not the room's
whole roster — but the room's other state stays, because a client that cannot show a room's name or
its power levels has bought nothing with the smaller member list. Without the flag nothing is
trimmed: the specification asks for "at least" the timeline senders, and the superset sent today is
legal. Left and banned rooms keep `state`: their batch ends at the event that took the user out, so
"state at the end of the batch" is what that section already answers.

The response field is named by the flag the client used — `state_after` for `use_state_after`,
`org.matrix.msc4222.state_after` for the unstable flag — because a client that sends the
pre-stabilisation flag is a client that reads the pre-stabilisation key, and answering it under the
other name tells it the feature did not happen.

Verification: `tests/integration/stateafter_test.go` (three cases — the ordinary answer keeps
`state` and gains nothing named `state_after`; `state_after` covers every sender in the batch and
repeats the membership events the batch already carries; under lazy loading a silent member is
dropped and non-member state is not; the unstable flag names the unstable field), `go test
-race ./tests/...` and `golangci-lint` green. Complement `tests/msc4222` is green in full, both
subtests — the whole package, red in every recorded run until now — checked together with
`tests/csapi` `TestSync` and `TestRoomMessagesLazyLoading*`, which stay green, in
`.agent-work/next/complement-run-stateafter.log` with its results table
`.../complement-results-stateafter.tsv`. Two sync failures are unchanged: `TestSyncOmitsStateChangeOnFilteredEvents`
(T-4020) and `TestDeviceListUpdates` (T-4064). MSC4222's other half — `/members` as a stream with
`from`/`to` positions — is T-4068, not implemented.

## 2026-11-07 — account data a user can take back, and a run that names the package each result came from

**Room account data, and what a removal means.** `account_data` was a value per user per type. The
specification wants a value per user per room per type as well (`/rooms/{roomID}/account_data/{type}`),
and MSC3391 wants a way to take a value back — both were missing, which is why
`TestAddAccountData/Can_add_room_account_data` and all of `TestRemovingAccountData` were red. The store
gains `room_id` and `deleted`, and the unique index widens to `(user_id, room_id, data_type)` — the old
narrower index is dropped rather than kept, or one row per user per type survives the change. A removal
is not a hole: the row stays, marked removed, because the removal has to have a place in the stream for
an incremental sync to point at. Reads answer `M_NOT_FOUND`; the sync entry carries the type with `{}`
content, and only to a client that has something to forget — an initial sync (`since` absent) omits it,
because there is nothing there to erase. `PUT {}` and `DELETE` are the same act, which is what MSC3391
says and what Complement does both ways.

Two things the writing taught. First, both spellings of the room path have to be registered:
Complement's `MustSetRoomAccountData` writes through `/user/{userID}/rooms/{roomID}/account_data/{type}`
at the *stable* prefix, the MSC3391 nesting, not the shorter `/rooms/{roomID}/account_data/{type}` —
a server that registers only the short form answers a spec-following client `M_UNRECOGNIZED`, and the
404 looked like a missing store rather than a missing route. Second, a room whose only change is its
own account data was being left out of the sync response entirely, so the value was accepted and never
delivered. `m.direct` also had to learn that a removed row reads as an empty map. (GORM detail kept
because it cost a test: `Assign(struct)` uses update semantics and skips zero fields, so clearing a
tombstone left `deleted = true`; the write passes a map.)

**A list that could not tell two tests apart.** `scripts/complement.sh` cuts the pass and failure lists
from the run log, and those lists are bare top-level names. Complement defines `TestSync` twice — in
`tests/csapi` and in `tests/msc4222` — so every recorded full run carried the same string in *both*
lists, and the compliance matrix row that said "`TestSync` verified" and the row that said "still red
through sync: MSC4222 `state_after`" were both talking about that one string, with a task pointer that
belonged to neither. The runner now drives `go test -json`, writes a qualified table
(action, package, test) next to the bare lists the matrix cites, puts the package on every line of the
rendered log — which is also what makes a `-p 2` run readable — and prints the qualified pairs whenever
a name appears in both lists. It also refuses to look like a result when it is not one: a run from which
no results could be derived, because the renderer broke or `go test` died on a flag, used to print "0
passing, 0 failing"; it now says no results were derived.

Verification: `go test ./tests/...` green, `golangci-lint` 0 issues; Complement
`TestAddAccountData` 2/2, `TestRemovingAccountData` 4/4, `tests/csapi` `TestSync` 9/9, from
`RUN='TestAddAccountData|TestRemovingAccountData|TestSync$'` with label `acctdata4`
(`.agent-work/next/complement-run-acctdata4.log`, `.../complement-results-acctdata4.tsv`).
`tests/msc4222`'s `TestSync` is still red, unchanged: MSC4222 `state_after` is T-4067.

## 2026-11-07 — an image that ships, a runner that cannot misreport itself, and a room allowed to remove somebody who lives elsewhere

Three kinds of work, one honest.

**The release path.** There was no release image, no CI, no licence. `Dockerfile` now builds the
server the way the Complement image already did — two stages, `CGO_ENABLED=0`, one static binary in a
small runtime, non-root UID, `/data` as the only state, both listeners exposed — and its `HEALTHCHECK`
runs this server's own `healthcheck` subcommand instead of installing a fetcher. Verified: the image
builds, starts, answers `/versions` 200, and the healthcheck exits 0 inside it. `.github/workflows`
runs the gate the progress records already cite (gofmt, vet, pinned golangci-lint, race tests), and
publishes the image on a tag with the tag matched against `internal/app.Version`, because an image
tagged 0.2.0 that answers 0.1.0 costs an operator a week of blaming their proxy. The Makefile's
`VERSION` is read from that same constant rather than written a second time.

**A run that cannot lie about itself.** Two tallies in this project's own records had drifted from the
logs they were cut from: a test described as "newly passing" while the run's own list of the same date
still had it failing, and a device-list test marked "verified, all ten subtests" in prose that its own
failures list — and a clean re-run — contradicts. `scripts/complement.sh` now writes the raw log, cuts
the pass and failure lists from that log, prints the tally it just computed, and shouts when a package
hit its per-package timeout (everything after the test it reached never ran: that is an unfinished run,
not a smaller suite) or when deployments never started (a container problem, not a protocol one).
`COMPLEMENT_TEST_TIMEOUT` replaces the silent 10-minute default. Related: the 40-minute Complement run
that produced nothing on the abandoned `main` branch was not slow, it was broken — its image copied a
glibc-dynamic binary into a musl base, so every container died at `not found` and Complement reported
each deploy timeout as a test failure. Rebuilt statically it runs, and its csapi package still had not
finished 62 of 107 tests in 40 minutes; this repository's own two packages finish in six.

**A kick, a ban and an unban of a user who lives elsewhere.** These answered 501 for the reason
recorded in `ErrRemoteTarget`: that writing a leave or a ban about another server's user would be "this
server claiming to be somebody else's homeserver". No room version says that. In every version with
power levels, an `m.room.member` event about *another* user is authorised by the sender's kick or ban
power, and the signature that carries the act is the sender's server's — which here is this server.
Refusing it made a room unable to remove a member who joined from elsewhere, and it made a permission
refusal (403, "this user has no kick power") unreachable behind a missing-feature answer (501). So the
event is now written and signed here under the room's own rules, and the difference from a join is
stated where it belongs: a join speaks for the person doing it and cannot be signed by a stranger's
homeserver (`ErrRemoteSender`, `ErrRemoteTarget` stay, for the cases that are real).

The part that would have been missed: fan-out destinations are the servers holding a joined member, and
a ban, a kick or a rescinded invitation is usually about a server holding nobody joined here — the
invited user who never joined, the member just removed. A membership event now also goes to the server
of the user it names. Without that the room keeps two copies and only one of them knows the person is
out. The ACL filter still runs over the finished list, so a server the room threw out is not told
either.

Evidence. `tests/integration/remotemembership_test.go`: a local moderator kicks a remote member and
both copies say `leave`, the user cannot then speak from their own server; a ban of an *unjoined*
remote member (the fan-out case on its own) reaches the target's server and the unban follows it; and a
user without the kick power is answered 403 rather than 501. `go test ./tests/...` green, `-race` green
(unit 4.7s, integration 26.1s), `golangci-lint` back to 0 issues (five dead helpers removed). Against
Complement: `TestFederationRoomsInvite/Inviter user can rescind invite over federation` and
`TestUnbanViaInvite` — both red before — are green.

Recorded rather than left implicit. T-4063: Complement's *Non-invitee user cannot rescind invite over
federation* still expects the invited user's own server to keep the invitation after a kick from
another server's moderator; this server applies the kick everywhere it has the room. That is a
behavioural divergence to decide on, not a bug to hide, and the same subtest also stalls in
`SendEventSynced`, which needs its own diagnosis. T-4064: `TestDeviceListUpdates/when_joining_a_room_with_a_remote_user`
fails reproducibly — when this server's user joins a room that lives elsewhere, nobody announces the
other members' device lists to the joining server; the announcement exists for the other direction.
T-4065: the compliance matrix rows that had gone stale (media, sync, device keys, push rules, presence,
directory, room version 1/2 authorisation, server ACLs, federation invites) now say what the tree does,
because a stale "not implemented" is as misleading as a stale "done".


## 2026-11-06 — a content repository, and the federation refusal that was a database write (`505d252`, `957f106`)

T-4059 and T-4060, closed. `internal/media` plus the two doors that serve it: eleven Client-Server
endpoints (upload, create-then-upload, download, thumbnail, config — and the authenticated
`/_matrix/client/v1/media/…` forms) and the two server-server ones
(`/_matrix/federation/v1/media/download|thumbnail/{mediaId}`, behind the `X-Matrix` check).

The parts that are protocol rather than file serving, and why they are shaped as they are. A media
identifier is used as a file name, so the specification's character whitelist is applied *before* a path
is built, and the origin server in the URI is validated as a server name; that is the whole of the
path-traversal story, and the tests ask for `..`, `%2f`, backslashes and UTF-8. Content that belongs to
another server is fetched from the server named in the URI, once, and kept under a directory named for
that origin — so a second request does not put a stranger in the path of each of this server's users,
which is also what makes the origin a target for amplification if you get it wrong. This server refuses
to serve another server's content from *its own* federation endpoint: the caller was told to ask the
origin, and a server that speaks for content whose owner it cannot name is a proxy with a good reputation.
A peer's `Location` pointer is reported, not fetched (DEC-042) — following a URL a stranger named is the
SSRF shape the federation address policy exists to prevent. Thumbnails are never enlarged, a source
smaller than the request is returned untouched, and the size bound is applied before the bytes are
decoded, because decoding is where the memory goes. A JPEG stays a JPEG.

The multipart answer is in `internal/media` for **both** directions on purpose: the answer this server
gives a peer and the answer this server reads from one. A writer and a reader that disagree would pass a
test of either alone and fail every download in the field. The fetcher also falls back to the deprecated
unauthenticated endpoint when the authenticated one gives any unsuccessful answer (DEC-043) — Complement's
own fixture answers 400 for its authenticated media route, because that fixture requires an `origin` path
variable its authenticated route does not carry. Getting a file across is worth one extra request;
insisting on one error shape is how a server ends up unable to read the network.

Then the part worth more than the feature. Two of Complement's federation media tests failed *twice in
three runs* with `M_UNAUTHORIZED` — "the credential from hs2 is not signed by a key that server published
under ed25519:v1". The message was a dead end, so the refusal was made to carry the reason it refused
(`matrixjson.VerifyJSON` now keeps the lookup's words instead of collapsing them to "no verification key";
the keyring names the key IDs it actually holds). The next run said what had really happened:

    cannot record the keys fetched for hs2: sqlite3: constraint failed:
    UNIQUE constraint failed: server_signing_keys.server_name, server_signing_keys.key_id

Recording a peer's keys is a *replacement* — delete that server's rows, insert the new ones — and it was
two statements. Two requests that needed hs2's keys at the same moment both fetched them, and the two
replacements interleaved. The second insert collided, the keyring had nothing to answer with, and a
correctly signed request from a peer was refused as a forgery. Any deployment where two rooms talk to the
same peer at once was exposed to this; it is the kind of fault that reads as a security event and is a
transaction that was never written. Fixed in both places that replace rather than append
(`SaveServerKeys`, `RememberAuthContext`), regression-tested in `tests/unit/fedkeyrace_test.go`, and
verified where it was found: three consecutive Complement runs of `TestMediaFilenames` and
`TestMediaWithoutFileName` with zero credential-check refusals, after two of three had failed.

Evidence. Local: `go test ./tests/...` green (unit + integration, including a two-homeserver media fetch
that deletes the origin's file before the second request, so "answered from the copy held here" is
demonstrated rather than asserted). Complement media cluster: **51 passing / 9 failing**; the failing
names — `TestUrlPreview`, `TestFederationRoomsInvite`, `TestFederationKeyUploadQuery`,
`TestFederationRedactSendsWithoutEvent` — are already in `.agent-work/complement-failures-20261113d.txt`,
so nothing newly red. The full-suite tally after this change is recorded below the line once the run
finishes.

Open, written down rather than left implicit (T-4061): URL previews are refused honestly and not
implemented; a peer's `Location` is refused; media storage has no quarantine, no per-room quota and no
deletion endpoint. `TestUrlPreview` stays red until previews exist.


## 2026-11-11 — a profile is only useful if the rooms hear about it (`4e20c57`)

T-4057, closed. `GET /_matrix/client/v3/profile/{userId}`,
`GET`/`PUT`/`DELETE /profile/{userId}/{keyName}`, the two profile capabilities, and the restatement that
makes the endpoint mean anything in a room.

The endpoint is the easy half. What a room shows of a person is the `m.room.member` event it holds, and
that event carried a copy of the profile from the moment it was written. A profile change that stops at
the account row leaves one human being named two ways in the same room, depending on which event a client
is rendering — and the room that was told once is never told again. So the account layer asks the room
layer to restate the membership: an ordinary send per room, under that room's own rules, where a room that
will not take the event keeps what it had, one refusing does not stop the others, and an unreachable
remote does not undo the account's own change. The room layer asks the account layer back the other way so
a join writes the member event with the current profile rather than an empty one. Interfaces in both
directions, so neither layer has to know the other's model.

The rule that decided what is *not* copied: only a local user's profile. Putting another server's user's
name into an event this server signs is this server putting words in another's mouth, and profile
disagreements between servers are settled by the signature on the event.

One thing that looks like a missing check and isn't: `avatar_url` is stored as given, non-MXC values
included. The specification puts that MUST on the client, says servers MAY validate, and says clients MUST
expect values they did not ask for — and Complement stores `"LemurLover"` as an avatar while testing
something else entirely. This server's own limit is length (255 bytes, `M_TOO_LARGE`) because a profile is
amplified into every room the user joins. The identifier is parsed where it is acted on, in a media fetch;
`matrixid.SplitMXC` is in place and tested for that.

Full run: **95 / 124 / 1** (was 90 / 129 / 1). Newly passing: the four profile tests plus
`TestPushRuleRoomUpgrade`, which had been flaky. Nothing newly failing, which matters because joins now
carry profile fields.

Left recorded rather than implicit (T-4058): the federation `/profile` query so a remote user can be asked
about at all; custom profile fields, deliberately unadvertised while only the two standard ones are held;
and the user directory, which is a search index over state this server can already see and is what the two
room-specific-username tests actually stop at.

## 2026-11-11 — a flaky federation test was the network lying, not the server (`scripts/complement.sh`)

T-4056, closed, with no protocol change. `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room` failed in
about half the runs at a 5 second sync wait; `...RemoteJoinInMSC3787Room` failed in cluster runs with a
404 for a room the same deployment had just made. Neither was a Lattice decision going wrong.

Reading the container log of a failing run rather than the assertion that failed settled it: hs2 was
dialling `https://10.89.0.2:8448` for the name `hs1` and being answered by a server whose certificate
says `hs2` — hs2 dialling itself. The name had resolved to the wrong machine, two joins then returned
500, bob never stood in the restricted room, and the sync assertion timed out three steps later. The
mechanism is rootless podman: one `aardvark-dns` serves every network from a file per network under
`$XDG_RUNTIME_DIR/containers/networks/aardvark-dns/`, a removed network can leave its file behind, and
the stale records keep being answered. `getent hosts hs1` inside a live deployment answered
`10.89.12.2` while the running `hs1` container was `10.89.0.4`, and the leftover file named exactly
that address. `podman network prune` will not clear it — the network is already gone.

Two things worth keeping from this.

First, the note written about these tests on 2026-11-11 said they were "delivery, not decision", pointing
at a T-4007 line in the log. That line was real and it was irrelevant: the same log carries refusal lines
in passing during runs that pass. A log line is not a cause until something shows the step failing
because of it. The corrected entry in TASKS says so.

Second, nothing in Lattice should bend here. Dialling the resolved address while demanding a certificate
for the name is what the specification's name resolution requires, and it is exactly what caught a
federation request aimed at the wrong machine. The tempting change — accept the connection because the
address was "close enough" — would turn a detected misdelivery into a silent one.

What went into the script instead is a preflight that reports aardvark record files whose network no
longer exists, naming the commands that clear them, and leaves the cleaning to the operator, as the
volume note does. Verification: three consecutive filtered runs of the flaky test (5.0–5.8 s), the whole
`TestRestrictedRooms` cluster green except the room-version-12 policy case, and a full clean run at
**90 / 129 / 1** with nothing newly failing. Earlier tallies were measured on a dirtier network.

## 2026-11-11 — a space can be walked, as far as the reader may see (`9535c65`)

T-4051, closed. `GET /_matrix/client/v1/rooms/{roomId}/hierarchy` and its Server-Server twin
`GET /_matrix/federation/v1/hierarchy/{roomId}`. The ID had been cited in three earlier ledger entries
without ever having an entry of its own; it has one now.

A hierarchy is a graph walk plus two decisions that are not about graphs.

**Who may see a room.** A space may point at rooms the reader has no standing in, and a hierarchy entry
carries the room's name, topic and avatar — the room's existence and subject. So each room on the walk
is asked what the read endpoints ask: public, world-readable, joined, invited, or satisfying the room's
restriction. The restriction branch reuses the rule from the restricted-join work: this server may lean
on its copy of the room the restriction names only while it is still inside that room. Showing a room on
membership nobody has been telling this server about anymore leaks the room to somebody it has moved on
without.

**Who is allowed to describe a room this server does not hold.** The child's own servers, and their
answer is weighed: it must name the room that was asked about or it is set aside, and
`inaccessible_children` tells the asker not to spend another request on a room that has been answered
about. The scopes differ on purpose — the peer answers whether the *asking server* could see it, and
the asking server then answers for its own user from its own state. That is why a restricted room can be
reported by one homeserver and still withheld from a user that homeserver cannot see, and it is exactly
what `TestRestrictedRoomsSpacesSummaryFederation` checks: with the space on hs1 and the restricted room
on hs2, the room is absent until hs2 can see the space, then present for the hs1 user who is in the
space and absent for the one who is not.

The first version of the federation test passed for the wrong reason, which is the lesson worth keeping:
the room service had no way to ask a peer (`SetRemoteHierarchy` was never wired), so the remote child was
simply missing — and an assertion of "one room listed" cannot tell a refusal from an unimplemented
question. Wiring it and asserting on both servers' answers turned the same test into one of the rule.

Ordering, `suggested_only`, `max_depth`, `limit` and pagination tokens are the specification's; a token
carries the filters of the walk it came from, so a second page cannot silently come from a different
tree, and an unknown token is 400 rather than a fresh walk.

Full run: **89 / 130 / 1** (was 86 / 133 / 1). Also newly passing without being aimed at:
`TestClientSpacesSummaryJoinRules`, `TestFederatedClientSpaces`. Nothing newly failing except
`TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room`, which fails in about half the runs *on its own*,
at a 5 s sync wait for a relayed member event, with T-4007 auth-chain refusal lines in the log. That is
now T-4056 rather than a shrug.

An environment note that cost a whole run: 2043 leaked podman volumes from earlier Complement runs made
every deployment fail mid-run with `exceeded num_locks (2048)`, which looks like sixty protocol failures
and is none. `docker volume prune -f` fixes it; a block of ~0.5 s failures in a full run means check the
volume count before believing anything.

## 2026-11-11 — who is allowed to look for another server when a restricted join cannot be authorised (`3c3b2f8`)

Complement\u2019s `TestRestrictedRoomsRemoteJoinFailOver` and its MSC3787 variant now pass. The rule they
probe is one this server had backwards, and getting it right meant separating two different questions.

**May a server that cannot authorise a restricted join go asking a neighbour?** Only the client may
decide that. `M_UNABLE_TO_GRANT_JOIN` says *the user may well be entitled and this server is not the
one that can say so*, and the Client-Server API\u2019s answer is a second request with another server named.
Two over-reaches were removed: `Joiner.candidates` appended the room\u2019s origin server to the client\u2019s
named list (so a join the named servers refused could succeed somewhere else the client never named),
and `joinThroughNeighbours` fell back to every server readable out of the room\u2019s state. That second one
is now allowed only while this server still has one of its own users joined in the room — which is what
makes the list something it knows instead of something it guesses — and even then it uses the servers
from the request first. A join no named server can authorise fails, which is the answer the client
needs in order to fail over deliberately.

**When may a server rely on its copy of the room the restriction points at?** A restricted room lets in
members of another room. A server with no user of its own in that other room is not on its list of
servers to be told, so the membership it holds may be months old. Granting from that copy answers a
question it is no longer in a position to hear; refusing on it invents a verdict. `stillTracksRoom`
decides, and the stale case is `M_UNABLE_TO_GRANT_JOIN`. The auth probe reports a check it could not
carry out under its own rule name (`eventauth.RuleAllowLookup`) rather than as the room refusing,
because the caller has to know which answer it is giving.

Two three-server integration tests cover both halves (`federationTrio`, because the shape needs a
server that cannot authorise, a server that can, and a user whose home server is neither):
`TestRestrictedJoinAsksOnlyTheServersTheClientNamed` and
`TestRestrictedJoinIsNotGrantedFromAnAllowRoomThisServerHasLeft`. Both pass repeatedly, and the local
suite is green including `-race`.

Full Complement run: **86 passing / 133 failing / 1 skipped**, third consecutive full run with nothing
newly failing (80 \u2192 84 \u2192 86). What the restricted cluster still owes is written up in T-4053: the
space-summary `allowed_room_ids` (T-4051), the room-version-12 creation policy, and
`TestRestrictedRoomsRemoteJoinInMSC3787Room`, which passes alone and in the full run but fails under
the filtered cluster\u2019s load and is reported rather than smoothed over.

## 2026-11-10 — prompt delivery, and the verdict bugs it exposed (`e0caf34`)

T-4054 is done. The federation sender no longer sleeps two seconds over an empty queue: queuing work
wakes the worker. That alone made our own integration suite fail 4–7 federation tests per run, and
each failure turned out to be a real defect rather than a test that disliked speed.

1. **The queue claim was not atomic.** SELECT of the due rows and UPDATE of their attempt time were
   separate statements, so two passes could take the same rows and send the same events twice, then
   settle the same rows twice. One write transaction now; `FlushOnce` also serialises passes in a
   process so a room's later state cannot leave before the event that state sits on.
2. **A neighbour\u2019s refusal ended an event\u2019s life.** Much of what neighbours refuse is "I cannot
   judge this yet" — room unknown, auth events not held, predecessor missing — and that gap closes a
   moment later on its own. Dropping over it leaves two servers\u2019 histories different forever. Refusals
   are retried inside the existing budget; events refused on their merits are still given up on, with
   the reason kept.
3. **`InvalidEventError` lost its cause** (the reason was a formatted string), so `errors.Is` was
   blind and callers had to read prose. It carries and unwraps the failure now.
4. **"Could not obtain the keys" was reported as "the signature does not match"** and both were filed
   as refusals. `events.ErrKeysUnavailable` separates a question about this server\u2019s reach from a
   verdict about an event.
5. **A refusal is filed against a name, and a name can be pinned to tampered bytes by anybody.** When
   the genuine event turns up — signing itself, hashing to that name — `clearStaleVerdict` drops the
   verdict that was about different bytes. The poison chain this removes was the source of
   `auth rule 2.3: auth event \u2026 was rejected on receipt` on joins: one forged copy arriving first made
   the room unjoinable, and whoever lied first decided what the room meant. Verdicts about repeated
   forgeries still stand.
6. **A failed key fetch cost a minute.** The keyring stamped the attempt before making it, so a
   connection that never opened left every event from that neighbour unjudgeable for the whole
   interval \u2014 enough to fail joins outright. An answered ask earns the interval, an unanswered one is
   retried in two seconds; `TestFederationKeyringTriesAgainAfterAFailedAsk` covers both with an
   injected clock.

Measurement: 12 integration runs across the work, ending at 0 failures in five consecutive runs, and
two clean `-race` runs. Complement: **84 passing / 135 failing / 1 skipped**, the best so far
(previous 80/139/1), with **nothing newly failing** and four newly passing —
`TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11`, its knock variant,
`TestRestrictedRoomsRemoteJoin` and its MSC3787 variant. The remaining restricted-cluster failures are
recorded in T-4054: the `FailOver` pair (a resident server answering from allow-room state it has not
been told about yet) and version 12 creation, which is a policy decision.

## 2026-11-09 — who may invite is the creating server\u2019s answer; a join it cannot sign for asks a neighbour

Commit `377fb4d`. Two Complement tests moved (`TestRestrictedRoomsRemoteJoinLocalUser` and its
MSC3787 variant), and the reason they failed was in room *creation* rather than in joining: the
authorisation rules read `invite` from the power levels and default it to 0, and the event that names
it is the creating server\u2019s to write. Ours said 0 for every preset, so any member of any room we
created could invite anyone \u2014 which in a restricted room is not cosmetic, because rule 4.3.5.1 lets
an invited user in past the restriction. A public room now starts at 50, the private presets at 0.
`trusted_private_chat` also gained the preset table\u2019s last column: invitees get the creator\u2019s power
level.

Joins gained the failover `M_UNABLE_TO_GRANT_JOIN` asks for: the federation joiner keeps asking the
other servers in the room instead of returning the first refusal, and a server holding the room but
holding nobody who can vouch carries the join out over federation through its neighbours.

Two storage/correctness fixes fell out of exercising the fan-out. `ClaimQueueItems` claimed in two
separate statements, so two passes could take the same rows and deliver the same events twice; it is
one write transaction now, and `FlushOnce` serialises passes in a process. And an event whose *auth
events* are not here is not a verdict about it: rule 2.2 was being recorded as a rejection, which
poisoned the event forever and every event citing it \u2014 how a room whose events arrived out of order
becomes a room that can never be joined. It now returns `MissingAuthEventsError` and files nothing.

**Tried and reverted: prompt delivery.** The sender sleeps two seconds when the queue looks empty, so
an accepted event can sit unsent that long; waking it per enqueue and cutting the idle to 250 ms made
delivery prompt and made our own federation integration tests flaky (4\u20137 failures per run, several
with `auth rule 2.3: auth event \u2026 was rejected on receipt` in the join path). Both experiments are
reverted, the reason is written on `defaultSenderIdle`, and T-4054 carries the diagnosis forward. The
honest consequence is recorded there too: with the invitation level now 50, `TestRestrictedRoomsRemoteJoin`
passes or fails depending on whether hs2 has been told about hs1\u2019s state yet \u2014 it used to pass for
the wrong reason.

Full Complement run after the commit: **80 / 139 / 1** (previous: 81 / 138 / 1). Newly passing: the two
`RemoteJoinLocalUser` tests. Newly failing: the flaky `TestDeviceListUpdates` (passes on its own) and
the `TestRestrictedRoomsRemoteJoin` pair, for the propagation reason above. Nothing is claimed about
the restricted cluster until the ordering work in T-4054 is done.

## 2026-11-08 — restricted rooms work end to end; six Complement tests move, none break

Commit `f6f0251`. `eventauth.MayJoinRestricted` had the allow-list rule right and nothing called it.
The flow now exists: `rooms.RestrictedJoin` answers whether the joining user satisfies one of the
room's conditions (a fact about the room, the same on every server — 403 M_FORBIDDEN if not) and who
stands behind the join (rule 4.2 wants the signature of the homeserver of the user named in
`join_authorised_via_users_server`, and the specification describes that user as one of the *resident*
server's own, in the room, who could have invited the joiner — so the authoriser is chosen from this
server's joined members at or above the invite level, in sorted order, so two attempts agree). A room
whose conditions pass but which has nobody here able to invite answers 400 `M_UNABLE_TO_GRANT_JOIN`,
which the joining side now reads as "ask another server" instead of "give up".

Two ordering facts had to change, and each had been silently fatal:

* The event is signed before it is authorised. Until this server has signed there is no signature for
  4.2 to look at, so authorising first refused every restricted join this server was entitled to make.
  `/make_join` is where that check genuinely cannot be made — the template is unsigned by design — so
  `Opts.AllowUnsignedTemplate` skips 4.2 there and the signature is verified where it exists.
* The joining server stores the event the resident server handed back, not the copy it sent. The
  resident server signs the join it accepts; keeping our own copy stored an event every other server
  would reject.

Three underlying bugs came out with them. A client could name its own authoriser — Complement writes a
join→join transition carrying `"join_authorised_via_users_server": "unused"` and expects it ignored;
we returned 403 trying to verify a signature by a server called "unused". `m.room.member` was gated at
`state_default`, so an ordinary user could not write the event saying they left: the specification puts
that type at zero because the membership rules decide who may change a membership, and 50 on top of
them stopped nothing but ordinary users. And `WithTx` dropped the services attached after construction,
so a capability configured on the room service disappeared inside a transaction — a restricted join
refused in one path and allowed in the other.

**The full Complement run after it: 83 passing, 136 failing, 1 skipped** (was 77/142/1), and
`comm` against `complement-failures-20261107.txt` shows **nothing newly failing**. Newly passing:
`TestRestrictedRoomsLocalJoin`, `...LocalJoinInMSC3787Room`,
`...LocalJoinNoCreatorsUsesPowerLevelsV11`, `TestRestrictedRoomsRemoteJoin`,
`TestRestrictedRoomsRemoteJoinInMSC3787Room`, and
`TestKnockRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV11`.

One honesty note: `TestRestrictedRoomsRemoteJoinInMSC3787Room` passed in the full run but failed when
the same tests were run filtered to `TestRestrictedRooms.*`. It is flaky, not settled, and the ledger
says so rather than banking the pass.

## 2026-11-08 — the restricted-room cluster opened, and its condition was wrong at the root

Commit `93878dc`. `eventauth.MayJoinRestricted` — the function that decides whether a user satisfies a
`restricted` room's allow list — looked for entries of type `m.room.member`. The specification's allow
condition is `m.room_membership`, and it is the only condition the protocol defines. So every entry was
skipped and the rule answered "not joined to any room in the allow list" about a room its owner had
opened. This was worth finding before the flow around it: wiring a broken condition would have produced
restricted rooms that refuse everyone, and the failure would have looked like a join-flow bug.

A table now covers the condition vocabulary: satisfied, unsatisfied, one-of-several, a type that is not
a condition (refused — a condition type this server has not read is not one it may treat as satisfied),
`restricted` with no conditions (invite-only, as the specification says), a public room asking nothing,
and `knock_restricted` in the versions that carry it. The older single-case test carried the same wrong
type string, which is what had kept it agreeing with the code instead of with the protocol.

Also worth recording as a lesson about edits rather than about Matrix: while checking whether
`knock_restricted` was correctly absent from room version 9, I patched the wrong version block and
stripped the flag from version 10 — the version that introduced it. `TestRoomVersionDifferences` and
`TestAuthRulesKnockGating` both name the version each flag belongs to, so the mistake was two minutes
of work instead of a mystery. v9 was already right.

Still open, and deliberately not improvised: the flow (T-4052) — nothing calls `MayJoinRestricted`;
the join event must carry `join_authorised_via_users_server`, and room version 8's rule **4.2** demands
that event be signed by the homeserver of the user that key names, which is what decides who may be
named at all. The task says to read the Server-Server §Restricted rooms and the v8 rules first.

## 2026-11-08 — `/event` stops serving whatever it is asked for, and the baseline holds

Commit `77947ff`. `GET /_matrix/federation/v1/event/{eventId}` answered with `stored.JSON`: the event
as stored. That ignored a redaction the room had applied to that very event, and it ignored history
visibility entirely — a server with nobody entitled to an event could read it by naming it.
`pduForPeer` is now the single place federation decides what a peer is shown: the room's own redaction
first (stored, a fact about the room), then the asking server's entitlement at the moment of the event
(a fact about the reader). Both history endpoints go through it. A trimmed event keeps `prev_events`,
`auth_events` and `signatures`, because a peer has to be able to place and verify an event to know it
is missing something. `TestFederationEventReadHonoursHistoryVisibility` covers both directions between
two real servers: trimmed under `joined` visibility, body intact for a `shared` room sharing its
history backwards.

`GET /event_auth` is deliberately *not* trimmed and the reason is written where the endpoint lives: an
auth chain is not history, it is the permissions — strip their bodies and the peer cannot authorise the
event it asked about, which is worse than a peer that can check and refuse.

**The baseline was used for its purpose.** Full run again (`RUN='.*'`,
`.agent-work/next/tally-full-20261107.log`): **77 passing / 142 failing / 1 skipped**, and
`comm` against yesterday's failure list gives **empty in both directions** — no test went from green
to red and none from red to green. So the eight behaviours that moved across this session (preset
contents and order, `join_rule` in the directory, the guest-access read, the walk's ceiling, federation
visibility on two endpoints, redacted serving on `/event`) are measurably neutral against Complement,
which is a claim that could not have been made a day ago because there was nothing to diff against.

Reading the failure list for the next increment: the "join" cluster is not one bug. About ten of those
names are **restricted rooms** — `TestRestrictedRooms*` (local, remote, fail-over, MSC3787 variants)
and `TestKnockRestrictedRooms*` — where `eventauth.MayJoinRestricted` already exists but the join flow
does not carry it end to end; four more are MSC4242 (rejection cascades across a rejoin), and
`TestPartialStateJoin` is its own piece of work. That is the ordering the next increment should take:
restricted rooms first, because it is one rule with many faces and the authorisation half is already
written.

## 2026-11-08 — one Complement test walked to the end, and the presets turned out to be invented

Commits `59ad48d`, `bd169c1`. `TestInboundCanReturnMissingEvents`: 0/4 → 3/4.

**A claim in this entry was wrong the moment it was written, and is corrected here rather than in a
later entry.** It first said "the default Complement suite is green end to end", citing
`.agent-work/next/tally-20261106.log`. `scripts/complement.sh` without `RUN=` runs a **seven-test
filter** (the M1 endpoints), not the suite; what was green was that filter. The real run
(`RUN='.*' scripts/complement.sh -v`, `.agent-work/next/tally-full-20261106.log`) is
**77 top-level tests passing, 142 failing, 1 skipped** across every package, MSC packages included,
and that is the first tally at this Complement checkout worth calling a baseline. It is stored as
`complement-failures-20261106.txt` (142 names) and `complement-passes-20261106.txt` (77) so the next
change can be diffed against it instead of described.

Four causes, each found by measuring rather than reasoning:

1. **Federation visibility.** Events handed to a peer are trimmed by the room's
   `history_visibility` *as of the event*, read out of the state snapshot the event was sent against,
   with the peer's membership in that same snapshot. `invited` and `joined` went green on this.
2. **The walk returned its own ceiling.** `EventsBetween` queued the `latest_events` events as its
   starting point and then appended them to the answer too. The peer's question is "what are the
   previous events of these".
3. **The presets were invented, and wrong.** The specification gives a table for the three presets;
   this server had `public_chat` starting `world_readable`, which publishes a public room's history
   to anybody who asks on the strength of a join rule that says nothing of the kind. Reading the
   table properly also exposed two neighbours: `visibility: public` without a named preset must take
   the `public_chat` preset (we made an invite-only room and listed it as walk-in), and `GET
   /publicRooms` entries were missing the required `join_rule`.
4. **An invented event type.** The guest-access read looked for `m.room.guest_join` with a boolean
   `guest_can_join`; the protocol event is `m.room.guest_access` with a string `guest_access`. A room
   that refused guests was advertising that it admitted them.

Two honest corrections to the record. The task opened yesterday as "two `m.room.member` events for
one remote user" was a misreading: the byte array Complement prints was real evidence about *what*
was extra and no evidence about *why*, and a two-server local reproduction settled it in one run —
one member event, and both servers naming the join identically, which is what derived event IDs are
for. And the task proposing to suppress no-op state writes would have broken the `world_readable`
subtest, which *expects* a second history-visibility event when the value changes; it is closed
without being done.

One divergence kept on purpose: the specification's `public_chat` row says `guest_access: forbidden`,
Synapse emits the event only when guests may join, and Complement's expected event list is built
around Synapse. We follow the table (AGENTS §2), so the `shared` subtest is off by exactly that
event; recorded as T-4048 with the source evidence rather than worked around.

Not measured yet: the MSC-included tally, which is the number this ledger tracks. Seven behaviours
moved at once; the run that says nothing else regressed is T-4050.

## 2026-11-08 — federation visibility, and a duplicated membership event it exposed

Commit `59ad48d`. `rooms.Service.VisibilityToPeer` decides, per event and per peer, whether the
event's body may be handed over at all: `m.room.history_visibility` read from the state snapshot the
event was sent against, then the peer's membership in that same snapshot — `world_readable` to
anybody, `shared` (or absent) retroactively to members, `joined`/`invited` only for the span in which
that peer had a user here. Where a moment matters and this server has no snapshot for it, the event
goes out trimmed. The trimming goes through the room version's redaction rules so signatures and
graph fields survive; the stored event is never rewritten, because the room did not destroy anything
— this reader was never entitled to it.

`TestInboundCanReturnMissingEvents` confirms the rule bites: for `invited` and `joined`, events
written before Charlie arrived now reach his server `redacted:true` (`.agent-work/next/gme4.log`),
where they previously arrived whole.

The four subtests are still red, and the reason is now a bug of a different size, recorded as
T-4046 rather than papered over: the "extra event" the test rejects is Charlie's **own**
`m.room.member` event at depth 8, in a room where his join is also the request's `latest_events`
seed. So this server is carrying two member events for one remote user after a federated join —
decoded straight from the byte array Complement prints, not inferred from a count. That is a claim
that a user joined twice, and it lands on state resolution, `/members`, member counts and join rules
as much as on a history walk. The task says where to look and, importantly, says not to fix it by
filtering member events in the read path.

T-4047 records the rest of the shape of this work: the same decision has to sit on `GET /event`, on
backfill and on the outbound fan-out — and `handleGetEvent` currently serves `stored.JSON`
unconditionally, so a redaction applied here is not even reflected to peers on that path.

## 2026-11-08 — the first federation endpoint in a while: `/get_missing_events`

Commit `43d29c5`, and the start of the federation-first re-sequencing the plan needed.

`POST /_matrix/federation/v1/get_missing_events/{roomId}` was absent — a **required** endpoint on
the specification's list, and the mechanism that lets a server which joined late (or was handed an
event citing history it never received) fill the hole instead of parking its successors as outliers
forever. Breadth-first down `prev_events` from `latest_events`; `earliest_events` is a floor, so
those events are neither returned nor used as a route below them; `min_depth` is the peer's second
floor; only a server with somebody joined in the room is answered, and never one the room's
`m.room.server_acl` excludes. Refused events are withheld, soft-failed events are not (the
specification's rule, and the one `GET /event` already follows), and redacted events go out in their
redacted form because a redaction removes that content from the network's view too. Oldest-first,
with the local persistence order as the tie-break for a creation batch, whose events sit at one depth
and are usually written inside the same millisecond.

That ordering question moved a room-creation decision: the preset state was written join_rules,
history_visibility, power_levels, and now starts with the power levels. Those three are siblings in
the graph — the room's own events do not record an order between them — but the event that says who
may write should exist before this server writes the rest, and a peer walking the history oldest
first should meet the room's authority before the state that authority permitted. Local suite green
with the change.

`TestInboundCanReturnMissingEvents` is still red, and it matters that the reason changed: it used to
fail with `M_UNRECOGNIZED`; it now gets an answer in the order the test expects (every
"type-by-type" mismatch is gone) and fails on two things we do not yet do, each recorded as its own
task with the measured line from the log: a no-op state write creating a second
`m.room.history_visibility` event (T-4043, and it is a decision about what a client's write *means*,
not a tweak), and the absence of history-visibility redaction on events handed to a peer that was
not entitled to them when they were written (T-4044 — the machinery for the entitlement question
already exists, since MSC4115 made us read a member event out of the snapshot an event was sent
against). T-4045 asks what `public_chat` should set `history_visibility` to, because the test's
event counts assume `shared` and our documented interpretation says `world_readable`; that gets
settled from the pinned specification, not by copying another server.

## 2026-11-08 — the membership field on the state reads, and a gap Complement cannot see

Commit `740ee31`. `unsigned.membership` was reaching clients only through `/sync`, `/messages` and
`/event`; the specification puts the property on the **client event format** itself
(`ClientEventWithoutRoomID.unsigned`, added in Matrix 1.11), so `/state`, `/state/{type}/{key}`,
`/members` and the at-point roster now answer it too, and `rooms.eventJSON` has been renamed in
meaning: it is the reader-free helper for the seven places that ask an event a question for
themselves (join rule, power levels, alias bookkeeping, member counts, directory scans), and
`eventJSONFor` is the one that has a reader.

The new test is the interesting part because it forces the semantics into the open rather than
restating a field name: `/state` returns a **mixture of moments** — the room's creation and its
power levels say `leave` about a user who joined later, while a topic written after they joined says
`join` — and a history page walked oldest first walks `leave` -> `invite` -> `join`, with a user's
own member event dated by the change it causes (their invitation says `invite` about them).
Verified to fail when the field is switched off. Complement unchanged: `TestMembershipOnEvents`,
`TestGetRoomMembers(AtPoint)`, `tests/csapi` `TestSync` (`msc4115state.log`).

And one thing the 143-failure tally does not contain: `GET /rooms/{roomID}/context/{eventID}`
answers `M_UNRECOGNIZED`, and `relations` likewise — neither endpoint exists. Complement in this
checkout never calls `/context` (the string appears only in comments in
`room_timestamp_to_event_test.go`), so a missing Matrix endpoint contributes nothing to the count.
That is worth saying out loud before the tally is treated as a measure of coverage: the number
counts Complement's assertions, not the specification. Task T-4042.

## 2026-11-08 — a full-suite number that means something: 76 passing, 143 failing, zero lock errors

`.agent-work/next/gate5.log`, tree `4d163ec`, `RUN=.`, `-p 2`, volume store clean, and the log
contains 0 hits for `exceeded num_locks` — the check that the two earlier tallies failed.

Nine tests newly pass against the 09:39 baseline and **none newly fail**: the members family
(`TestGetFilteredRoomMembers`, `TestGetRoomMembersAtPoint`), MSC4115 (`TestMembershipOnEvents`),
the four transaction-ID tests, `TestIsDirectFlagLocal`, `TestWriteMDirectAccountData`. That "none
newly fail" is the sentence to care about: everything added today — the at-point roster,
`prev_batch` naming, `unsigned.membership`, the chunk pairing fix — moved nothing the other way.

143 failures is mostly absence rather than breakage: federation joins and ACLs, device keys,
media, threads, receipts, relations, knocking and restricted rooms, to-device, account data,
search, and the unstable MSC suites. It is a trajectory marker, not a compliance claim.

## 2026-11-08 — MSC4115 lands, the suite tally collapses, and one of my own ledger claims was wrong

Commit `38561f7`. `unsigned.membership` on served events is the **reader's** membership of the
room as of the moment the event was sent, read out of the state snapshot that event was sent
against: `leave` when the snapshot holds no member entry for the reader, **no field** when this
server has no snapshot of the event at all, and a value already under `unsigned` is left alone
because that is where a peer records what *it* added around an event. `/sync` (timeline and
state), `/messages` and `/event` pass the reader; `Serve`/`ServeChunk` stay reader-free for
callers with no reader to name, and no federation path runs through this code at all.

Complement, `.agent-work/next/msc4115c.log` and `msc4115d.log`: `TestMembershipOnEvents` passes;
`TestGetRoomMembers`, `TestGetRoomMembersAtPoint`, `TestSendAndFetchMessage`,
`TestFetchMessagesFromNonExistentRoom`, `TestRoomMessagesLazyLoading(+LocalUser)`,
`TestSyncFilter` and `tests/csapi` `TestSync` pass unchanged; `tests/msc4222` `TestSync` fails its
two `state_after` subtests as it has before. Local `go test ./tests/...` green.

Found while in there: `ServeChunk` paired decoded events with stored rows by `len(out)`, so after
any withheld event every later event in the chunk got the **previous event's** `unsigned` —
someone's `prev_content` or transaction ID attached to a different event's body. Now paired by
position. Fixed without a test, which is recorded as T-4041 rather than waved away.

Then the environment, where my earlier conclusion was wrong. The deploy failures that made the
full-suite tally meaningless are `allocating lock for new **volume**: exceeded num_locks (2048)`:
**2044** anonymous volumes left behind by removed homeserver containers, most of them from one run
I interrupted with `pkill`, had eaten Podman's entire lock pool. Cleared only the ones no live
container mounts (2044 gone, 1 left, both pre-existing containers still up). That also invalidates
what B-0008 said about the rootless lock set being immovable — that error was an artefact of my
own `num_locks` edit, which is gone. Consequence for the records: `gate4.log` (59 pass/161 fail)
and `full2.log` (67/152) are noise; T-4039 is the re-run.

And a correction to the compliance matrix, because the error was mine and it was in the direction
of looking better: it claimed `TestTyping` was newly passing. It never was — the log I read for it
was a run of the transaction-ID filter, which contains no typing test. Typing is not implemented
client-side at all: `/sync` builds `ephemeral` as an empty list by construction
(`internal/sync/service.go:320-330`), and typing EDUs received over federation are never stored or
replayed. Re-run twice today it fails all three delivery subtests, consistently (T-4036). The same
sweep removed a citation to a Complement helper (`assertMembershipEventEventsPresent`) that does
not exist in this checkout. T-4034's description is also softer than the evidence:
`SendEventSynced waiting for event ID` appears 16 times in `prevbatch1.log`, so it is routine
rather than occasional, even though the tests that show it still pass.

## 2026-11-08 — the MSC4115 lookup lands, and the full-suite tally turns out not to be a tally

`persist.StateGroupRepo.StateEntryAt(eventID, type, stateKey)` (commit `1e33861`) answers what a
state key held in the snapshot an event was sent against, in one statement, and distinguishes "the
snapshot held nothing" from "this event has no recorded snapshot" — the second has to stay
distinguishable or a server starts inventing history. `go test ./tests/...` green;
`TestStateEntryAtAnswersAboutTheMomentNotTheMomentAfter` covers a message sent while bob was joined,
asked after he left: still `join`. The presentation half of T-4032 is written out as steps in
TASKS.md rather than left to be rediscovered.

Then the full-suite run, and the discovery that the run itself is the problem (B-0008, T-4035):
`RUN='.'` reports 59 top-level passes and 161 failures, but 15+ of the sampled failures are
`ContainerCreate: … exceeded num_locks (2048)` at **deploy** time, with 0.4-0.9s durations — tests
that never reached a homeserver. `TestUnknownEndpoints` and `TestRoomSummaryAllowedRoomIDs` are
among them and both passed in a filtered run 40 minutes earlier, so the number is not a compliance
measurement and is recorded as such.

Measured on the way: raising `num_locks` is refused at 4096 and above because the rootless lock set
`/libpod_rootless_lock_1000` already exists at 2048 and Podman will not resize it; that needs
`podman system reset`, which deletes images and containers this machine also uses for other work, so
it was not run and the config file was removed again (host verified back to its prior state:
`docker ps` shows the two pre-existing containers). No orphaned `conmon`, no stopped
`complement_*` containers, nothing to reap. `scripts/complement.sh` now defaults to `-p 2` and prints
it, which helps but does not make a whole-suite run possible here.

Newest first. Every entry names the command that produced the evidence; nothing here is a claim
without a run behind it.

## 2026-11-05 — M1 closed (foundation + identity layer), Complement gate met

**Verification gate: `make verify`** → `gofmt` check, `go vet ./...`, `go test -race -count=1 ./tests/...`

```
ok  github.com/enimatek/lattice/tests/integration   4.613s
ok  github.com/enimatek/lattice/tests/unit          1.211s
```

`golangci-lint run ./...` → `0 issues.`

Test counts: 53 top-level tests in `tests/unit` (`canonicaljson`, `signjson`, `matrixid`, `config`,
logging redaction, `storage` — including tokens, UIA sessions, devices and login services — router
error semantics, and certificate generation); 38 top-level tests in `tests/integration`, driving the
assembled server over real HTTP and TLS listeners.

### What was built in this milestone

- Protocol primitives: canonical JSON with the spec's example vectors, sign/verify JSON (7-step
  procedure, unpadded base64), identifier grammar, ed25519 signing keys with `ed25519:<id>` key ids.
- HTTP layer: two listeners (client plain HTTP, federation/key HTTPS with TLS ≥ 1.2), method-prefixed
  `http.NewServeMux`, Matrix 404/405 `M_UNRECOGNIZED` semantics mirrored across listeners, body limits,
  request timeouts, panic recovery, security headers, graceful shutdown with a forced-close path.
- Storage: gormlite/SQLite, migrations, models and repositories for users, devices, access tokens,
  UIA sessions, one-time signing keys; bcrypt password hashing; SHA-256 token digests; single-writer
  connection setting recorded as a scalability limit.
- Identity layer: registration (User-Interactive Auth, `m.login.dummy` requires a session this server
  issued, downcasing, `M_INVALID_USERNAME`/`M_USER_IN_USE`/`M_WEAK_PASSWORD`), `/register/available`,
  Synapse-compatible shared-secret admin registration (HMAC-SHA1, single-use nonce, constant-time
  compare), login (password flow, uppercase and localpart identifiers), logout and logout-all,
  device list/get/update/delete with UIA and owner checks, `/account/whoami`, `/capabilities`,
  `/versions`, well-known client and server documents.
- Key API: `GET /_matrix/key/v2/server` (signature verified independently in tests) and
  `POST /_matrix/key/v2/query` for this server only.
- Deployment: `deploy/complement/Dockerfile` + `entrypoint.sh` (image contract, CA-signed certificate
  through `LATTICE_TLS_CA_*`), `scripts/complement.sh`, `Makefile`, `README.md`, `docs/*`.

### Complement evidence

Checkout `7b38a63c9db3337292bee5887fc8533be3a57b9f`; image `complement-lattice:latest` rebuilt from the
committed source and re-run afterwards (`scripts/complement.sh`, exit 1 as expected pre-`/sync`): the
targeted subset reproduces exactly — only the four `/sync`-asserting subtests fail.

Targeted M1 subset: `TestVersionStructure`, `TestUnknownEndpoints`, `TestRegistration`, `TestLogin`,
`TestServerCapabilities`, `TestCanRegisterAdmin`, `TestMediaConfig` pass.
`TestLogout` and `TestDeviceManagement` pass except the subtests that assert through `GET /sync`
(M3 not built yet); the equivalent behaviour is covered by `tests/integration/auth_test.go`.

Full `./tests ./tests/csapi`: 8 passed, 189 failed, 0 skipped. Failure causes: 309 × 404
`M_UNRECOGNIZED` (unimplemented endpoints), 28 × 501 (media upload stub), 1 × 400 `M_INVALID_PARAM`
(refresh-token refusal). Zero panics and zero HTTP 500 responses across the whole run. No Complement
assertion was modified. Details: `.agent-work/COMPLIANCE-MATRIX.md`, `docs/compliance-matrix.md`.

### Manual smoke against the built binary (curl only)

`registration_mode:"open"` startup line → UIA `POST {}` → session → register `Alice` →
`{"user_id":"@alice:smoke.local"}` (downcasing proven), login with `ALICE`, `whoami`, two devices in
`GET /devices`, `POST /logout` → `{}`, reuse of the token → 401 `M_UNKNOWN_TOKEN`,
`GET /_matrix/client/v3/sync` → 404 `M_UNRECOGNIZED`, `DELETE /login` → 405 with `Allow: GET, POST`,
`GET /_matrix/key/v2/server` over HTTPS returns a self-signed ed25519 document, and
`grep -c "S3cr3t\|access_token"` over the server log → `0`.

### Bugs found and fixed while closing the milestone

1. **Data race** between `httpserver.Serve` handing listeners to goroutines and `CloseListeners` from
   `App.Close` — caught by `-race` in the new PKI test. Fixed with a mutex-guarded listener table and
   an idempotent close; the race detector is now part of `make verify` so it cannot regress.
2. **Untrusted CA path**: the federation certificate is now signed by a CA supplied through
   `LATTICE_TLS_CA_CERT_FILE`/`LATTICE_TLS_CA_KEY_FILE` (Go-side, no openssl in the image), verified by
   an integration test that connects with a client trusting *only* that CA — no
   `InsecureSkipVerify` in that test.
3. **Shutdown could hang past the window** with connections still active: `Shutdown` timeout now logs,
   force-closes, and reports a forced shutdown instead of returning a bare `context.DeadlineExceeded`.
4. `golangci-lint` findings: an ineffectual assignment in the IPv6 server-name check, and a raw control
   character in a test fixture.

## 2026-11-05 — Stage 0/1 records

Architecture, requirement rows, pinned specification (v1.19) and the Complement analysis were written
to `ARCHITECTURE.md`, `REQUIREMENTS.md`, `SPECIFICATION-VERSION.md` and `REFERENCE-ANALYSIS.md`; the
Complement image contract, PKI mount points and env-var contract were confirmed against the checkout
rather than assumed.

## Historical note (context lost to compaction)

An earlier build in this session produced a container image and Complement blueprint images. Those
artefacts were rebuilt from the repository (`deploy/complement/Dockerfile`) so the recorded results
are reproducible from source; the earlier ad-hoc recipe is not used.

---

# Iteration 2 — M2 protocol primitives (T-2000, T-2001, T-2011, T-2012)

Timestamp: 2026-11-05 (UTC)

## Specification research

Fetched from https://spec.matrix.org/v1.19/ : `rooms/README` and all eleven per-version
pages, `rooms/redactions`, `rooms/authorization_rules`, `rooms/version_history`,
`server_server_api/signing_events` (content hash, reference hash, signing over the redacted
event), `appendices/canonical_json`, `client_server_api/capabilities`. Cross-checks: Synapse
`synapse/events/__init__.py` (`_RedactionV2`) and gomatrixserverlib `eventversion.go`
(version registry) and `eventcrypto.go` (`referenceOfEvent`), read from the local Go module
cache.

## Built

| Package | Contents |
| --- | --- |
| `internal/roomversion` | v1-v12 registry (state resolution, event ID format, auth rule set id, redaction profile, join rules, integer power levels, key validity, canonical JSON strictness, aliases special casing, creator placement, redacts placement, domain-free room IDs, privileged creators); grammar validation; `Supported` = {10, 11}, default v11; `Available()` for capabilities |
| `internal/redaction` | v1-v10 and v11/v12 strip lists, per-type content allow lists, v9 `join_authorised_via_users_server`, v6 aliases, v11 whole create content / `content.redacts` / `invite` / `third_party_invite.signed`, `IsRedacted` heuristic, non-mutating |
| `internal/events` | `Parse` (numbers kept exact), `Canonical`, `ContentHash`, `ReferenceHash`, `EventID`, `Seal`, transport-key stripping |

## Evidence

- `go test -count=1 ./tests/...`: integration ok, unit ok.
- `make verify` (vet + `-race`): ok. `golangci-lint run ./...`: 0 issues.
- New tests: `tests/unit/roomversion_test.go`, `tests/unit/redaction_test.go`,
  `tests/unit/eventseal_test.go`.
- Cross-implementation goldens from gomatrixserverlib
  v0.0.0-20260716140101-4fe595dc7f58 for identical input bytes: v3 and v10 give
  `$1YYoe3Q9H0V7opN6VO8xuNAlY6Gyk4uxxW9qIEi3SxE`, v11 gives
  `$F0qOSjkTdQqqMJmoiJAQY5guE-jgC6M0CMmPOzfWxOg`. The ID changes between v10 and v11
  because room version 11 changed which keys survive redaction — that is exactly what the
  test is there to catch.

## Docs

`docs/requirements.md` §6 replaced by 22 normative rows with status and tests, plus three
documented interpretation decisions. `docs/compliance-matrix.md` gained an "M2 protocol
primitives" section. `.agent-work/ARCHITECTURE.md` gained the M2 design (storage plan and
five behaviour-changing decisions). `.agent-work/TASKS.md` M2 replanned into T-2002..T-2012
with T-2000/T-2001/T-2011/T-2012 checked off. `.agent-work/BLOCKERS.md` gained F-006 (the
`redacts` ambiguity) and F-007 (environment limits on code search).

## Honest status

M2 is not endpoint-reachable: no `createRoom`, no authorisation rules, no state resolution,
no event storage. `/capabilities` still advertises a room version the server cannot create;
the registry now knows the honest set and the handler gets wired together with `createRoom`.

---

# Iteration 3 — M2 event storage (T-2002)

Timestamp: 2026-11-05 (UTC)

## Built (`internal/persist/event.go`, wiring in `database.go`)

| Table | Purpose / notable choice |
| --- | --- |
| `rooms` | room id, **room version**, creator, latest stream. The version is stored per room because every rule (auth, redaction, event ID format, state resolution) is read from it. |
| `events` | canonical JSON stored verbatim (`json`) plus lookup columns; `state_key` is NULL for non-state events and `""` for state events with an empty key (`m.room.create`); `redacted`/`redacted_json`; `outlier`/`soft_failed`; global `stream_ordering`. |
| `event_edges` | `prev` and `auth` references as rows — history is a graph walk. **No foreign key on `reference_id`**: a server legitimately holds events whose predecessors it has not fetched yet, and a constraint would reject valid Matrix state. |
| `room_state` | current state keyed by (room, type, state_key), separate from history. |
| `event_auth_context` | the state that authorised each event, so re-authorisation and federation never recompute the past. |
| `event_txn` | (device, txn) → event id, unique per device: the basis of `/send/{type}/{txn}` idempotency. |

Indexes: unique event id (dedup), (room, stream_ordering) for history and read
positions, (room, type, state_key), sender, edge reverse lookup for forward
extremities, unique room-state key, unique auth-context pair, unique (device, txn).

`ForwardExtremities` excludes soft-failed and outlier events, so new events never
attach to a branch this server has already declined.

## Evidence

`go test -count=1 ./tests/...` → integration ok, unit ok. `make verify` (`-race`) → ok.
`golangci-lint run ./...` → 0 issues.

`tests/unit/eventstore_test.go` covers: NULL-vs-empty state key preserved, event JSON
stored unchanged, duplicate event id → `ErrConflict`, forward extremities move as the
graph grows and ignore soft-failed events, links to missing events are allowed and
idempotent, batch reads tolerate gaps, stream ordering monotone and not burned by a
rolled-back transaction, room state is an upsert map including the empty state key, auth
context is replaced not accumulated, transaction ids are per device.

## Docs

`TASKS.md` T-2002 checked; `docs/compliance-matrix.md` and `docs/requirements.md`
REQ-TXN-001 updated to reflect storage-only status.

## Honest status

No endpoint can create or send an event yet. Still open in M2: authorisation rules
(T-2004), state resolution (T-2005), `createRoom` (T-2003), `send`/txn endpoint (T-2006),
membership (T-2007), state reads (T-2008), redaction application (T-2009), upgrades (T-2010).

---

# Iteration 4 — M2 event authorisation (T-2004)

Timestamp: 2026-11-05 (UTC)

## Specification basis

Rules were read from the pinned v1.19 pages rather than inferred: `rooms/v3`, `rooms/v6`,
`rooms/v9`, `rooms/v10`, `rooms/v11` (full numbered rule lists quoted for v11; rule 1
verified verbatim for v3, v6, v9, v10) and `server-server-api` §Auth events selection.
Cross-checks: gomatrixserverlib `eventauth.go` (`allowerContext.allowed`,
`userPowerLevel`, `NewPowerLevelContentFromAuthEvents`, `aliasEventAllowed`) from the local
module cache.

## Built: `internal/eventauth`

| Piece | Notes |
| --- | --- |
| `Check` | rules 1-10 in specification order for room versions 3-11; room version 1 returns `ErrRulesetUnsupported` instead of borrowing the wrong table |
| `SelectKeys` / `SelectEventIDs` | the auth events selection algorithm; the sender's and the target's member event are the same event when a user acts on their own membership, so keys are deduplicated (listing twice would make a valid event look like it has duplicate auth events) |
| rule 2 | duplicate `(type, state_key)`, set equality with the selection, unresolved auth events, rejected auth events, foreign `room_id`, missing create event |
| rule 4 | the whole membership table: join (public, invite, knock, restricted, knock_restricted), invite (including the third-party invite chain), leave (self-leave and kick), ban, knock, unknown membership |
| rule 9 | the scalar/maps/users change rules, plus the room version 10 integer validation; versions before 10 keep their lenient behaviour and the test says so |
| version gating | `knock`, `restricted`, `knock_restricted` are unknown to versions that predate them; `join_authorised_via_users_server` in a version without restricted rooms is refused |
| `powerLevelsFor` | the creator holds 2^53-1 while the room has no power levels event, otherwise rule 7 would make rule 9.4 unreachable (F-009) |
| `MayJoinRestricted` | the restricted-room allow list, applied to locally created joins and deliberately **not** inside `Check`: the auth rules are the federation contract, and being stricter than a peer splits history (F-010/REQ-AUTH-023) |
| `Snapshot` / `MemoryState` | the rules run over an interface, so the same code serves tests, iterative auth checks and (later) the database |
| `Opts` | signature verification for `join_authorised_via_users_server` and third-party invites, and the cross-room membership lookup. A nil verifier means reject, never trust |

## Evidence

`go test -count=1 ./tests/...`: integration ok, unit ok. `make verify` (`-race`): ok.
`golangci-lint run ./...`: 0 issues.

`tests/unit/eventauth_test.go`: ~45 named rule cases across membership, knock gating,
restricted joins (including an unsigned authoriser being refused), create rules and the v11
creator change, the rule 2 variants, `m.federate: false`, rules 5-9, power-level change
rules including the version 9 versus 10 integer difference, the aliases exemption and its
removal, selection algorithm outputs, and the room version 1 refusal.

Three findings became tracked items rather than quiet choices: F-009 (rule 7 blocking rule
9.4), F-010 (aliases special case divergence between gomatrixserverlib and the spec), and
REQ-AUTH-023 (restricted-room allow list not evaluated by the auth rules).

## Honest status

Still nothing endpoint-reachable: `createRoom` (T-2003), state resolution (T-2005), the send
endpoint (T-2006), membership endpoints (T-2007), state reads (T-2008) and redaction
application (T-2009) remain. Authorisation for room versions 1 and 2 is unimplemented by
decision, and the restricted-room allow list is only enforced on the local send path.

---

# Iteration 5 — M2 State Resolution 2 (T-2005)

Timestamp: 2026-11-05 (UTC)

## Built: `internal/state`

The algorithm was taken from the "State resolution" section quoted from the room version 11
page (definitions and the five numbered steps) and implemented step by step:

| Piece | Note |
| --- | --- |
| `Resolve` | refuses room versions whose algorithm is not implemented instead of running the wrong one; a single state is returned unchanged |
| `splitStates` | unconflicted map versus conflicted set |
| `authDifference` | ∪ of the auth chains minus ∩, walked from every event in every state |
| `powerEventsInFullConflictedSet` | power events (`m.room.power_levels`, `m.room.join_rules`, a leave/ban applied by someone else) widened by their auth chains |
| `reverseTopologicalPowerOrder` | Kahn's algorithm choosing the smallest available event, comparing sender power level, then `origin_server_ts`, then event ID; leftover events on a cycle are kept rather than dropped |
| `mainlineOrder` | mainline positions from the power levels event in the partially resolved state |
| `iterativeAuthChecks` | applies `eventauth.Check` over the state being built, filling missing keys from the event's own auth events and never from a rejected one |

`Snapshot` adapts a `Graph` plus a `State` to the authorisation rules, so resolution and
authorisation share one event view.

## Evidence

`go test -count=1 ./tests/...`: integration ok, unit ok. `make verify` (`-race`): ok.
`golangci-lint run ./...`: 0 issues.

`tests/unit/stateresolve_test.go`:
- a ban applied by a powerful user on one fork beats a self-join on the other, whichever
  order the forks are handed in;
- of two conflicting power levels events, the later by the ordering wins, and flipping
  `origin_server_ts` flips the result — convergence comes from the ordering, not input order;
- a second `m.room.create` reachable only through an auth chain cannot displace the create
  event every fork agrees on (step 5);
- a power levels event granting its sender power from nowhere is dropped, and a rejected
  event never enters the resolved state;
- room versions 1 and 12 are refused.

## Honest status and the next check

State resolution is implemented and tested against the specification's own wording, but it
has **not** been compared with another implementation's resolver on shared fixtures. A
divergence there is invisible locally and fatal in a federated room, so T-2005a (a
gomatrixserverlib cross-check with shared fixtures) is recorded as a required follow-up
before federation work builds on it, alongside REQ-STATE-007.

Still endpoint-inert: `createRoom` (T-2003), the send endpoint (T-2006), membership (T-2007),
state reads (T-2008), redaction application (T-2009), upgrades (T-2010).

---

# Iteration 6 — M2 room creation and event sending (T-2003, T-2006)

Timestamp: 2026-11-05 (UTC)

## Built

`internal/rooms` (service), `internal/api/client/rooms.go` (transport), and the wiring in
`internal/app`. One service owns the orchestration: room version selection → initial state
construction → auth event selection → authorisation → sealing (hash, event ID, signature) →
persistence of the event, its prev/auth edges, its state row and its auth context, all
inside one transaction.

Endpoints now reachable:
- `POST /_matrix/client/v3/createRoom` — presets, `room_version` negotiation,
  `creation_content` restrictions (the client cannot pick creator, version or predecessor),
  `initial_state`, `power_level_content_override` merged one level deep so an override
  cannot erase the creator, name/topic/avatar shorthand.
- `PUT /_matrix/client/v3/rooms/{roomID}/send/{eventType}/{txnID}` — per-device transaction
  idempotency, server-computed `depth`/`prev_events`/`auth_events`, authorisation before
  signing.
- `PUT /_matrix/client/v3/rooms/{roomID}/state/{eventType}[/{stateKey}]` — including the
  empty state key.
- `/capabilities` now reads `roomversion.Default()`/`Available()`, closing the recorded
  deviation that the server advertised versions it could not create.

## Findings worth recording

1. **`M_NOT_JSON` versus `M_BAD_JSON`.** `httpx` answered `M_BAD_JSON` for a truncated
   body because Go reports truncation as `io.ErrUnexpectedEOF`, not `*json.SyntaxError`.
   The specification separates the two and clients act on them differently.
2. **Empty state key routing.** Go's mux treats `.../state/{eventType}/` as a subtree
   pattern, so it also matches `.../state/a/b/c`. The handler counts the remaining segments
   and refuses the deeper path rather than reading it as an empty state key.
3. **`m.` event types are lowercase.** A client-supplied `m.Room.Message` would create
   state and power-level entries that no specification event matches.
4. **Signature key ID is `ed25519:v1`**, not `v1` — a test asserted the wrong form, not the
   server.
5. **The end-to-end check that matters** (`TestCreatedEventIsVerifiable`): read the stored
   event back, recompute its event ID from what is stored, confirm this server's signature,
   and run the authorisation rules against the stored state and auth context. This is what
   distinguishes "the code ran" from "the event we produced is one the protocol accepts".

## Evidence

`go test -count=1 ./tests/...`: integration ok, unit ok. `make verify` (`-race`): ok.
`golangci-lint run ./...`: 0 issues. New tests: `tests/integration/rooms_test.go` (creation,
sending, idempotency, refusal of non-members, refusal of `m.room.member` via `/send`,
version negotiation against the advertised set, error codes, state send with and without
power, stored-event verifiability) and `tests/unit/roomslogic_test.go` (event type grammar,
power-level override merge).

## Honest status

No room on this server can have a second member yet: join/invite/knock/leave are unimplemented
and `/send` refuses membership events by design, so multi-user Complement room tests still
cannot pass (F-012). The rejection outcome of on-receipt checks is not stored, so
`Snapshot.Rejected` reports false for stored events (F-011) — harmless while every event is
locally authored, must be fixed before federation ingest.

---

# Iteration 7 — membership, room aliases and state reads (T-2007, T-2008, T-2011)

Timestamp: 2026-11-05 (UTC)

## Built

`POST /join/{roomIDOrAlias}`, `POST /rooms/{roomID}/join`, `leave`, `invite`, `kick`,
`ban`, `unban`, `knock`; `PUT/GET/DELETE /directory/room/{alias}`,
`GET /rooms/{roomID}/aliases`, `room_alias_name` in `createRoom`, join by local alias,
canonical-alias validation; `GET /rooms/{roomID}/state[/{type}[/{key}]]` with
`format=event`, `/members` with `not_membership`, `/joined_members`, `/joined_rooms`.
Room aliases are a new table (`room_aliases`) with a unique index.
The offered room version range widened from {10,11} to {7,8,9,10,11}: those are the
versions whose authorisation rules and state resolution are implemented and where knock
and restricted join rules exist.

Two findings about the endpoints, not the protocol:

* The same action has two spellings — `POST /join/{roomID}` and
  `POST /rooms/{roomID}/join`. Answering the second with `M_UNRECOGNIZED` looked like a
  broken homeserver in `TestRoomMembers`, so both are served.
* Complement still calls `r0` paths. A homeserver that only serves `v3` fails
  `TestRoomState` on `r0/rooms/…/joined_members`, so every client endpoint is registered
  under both `v3` and `r0`.

## Findings worth recording

1. **A repository without `WithTx` deadlocks the server.** `RoomAliasRepo` was written
   without a transaction variant, so `WithTx` handed it the *outer* connection while a
   transaction held the only SQLite writer. The request never returned: no error, no
   timeout, just a hung PUT. Found by a test that timed out, and it would have been a
   production hang. `WithTx` is now part of the interface and the reason is commented.
2. **Kicking someone who is not in the room.** The room version rules permit a leave
   event for a user with no membership, so `eventauth` correctly allowed it; Complement
   and sytest expect 403. The rule belongs to the Client-Server endpoint, so it lives in
   `rooms.SetMembership`, and `eventauth` stays faithful to the room version rules.
3. **Room reads were unauthenticated-by-membership.** Any token could read any room's
   state and member list. Now a current membership is required, and a left member loses
   the read (Complement asserts 403 after leaving).
4. **Alias localparts are opaque Unicode.** `#老虎£яඞ:hs1` must work; a bare `%` must
   not. Both are Complement-asserted.
5. **`GET /directory/room/{alias}` must not require a token** — it is how a client finds
   a room to join.
6. **`format=event`** on a state read returns the whole event, not the content; returning
   content made `TestRoomState` report a missing `sender`.
7. **`M_BAD_ALIAS` and `M_DUPLICATE_ALIAS`** are needed for a canonical alias that names
   another room and for an alias already in use; a plain `M_UNKNOWN` would have lost the
   distinction the client acts on.

## Complement evidence (targeted run, not the full suite)

`TestRoomAlias` — including Unicode aliases and "only room members can list aliases" —
and `TestCannotKickNonPresentUser` now **pass**. `TestRoomCreate`, `TestRoomMembers`,
`TestRoomState`, `TestRoomDeleteAlias`, `TestRoomCanonicalAlias`,
`TestNotPresentUserCannotBanOthers` and `TestCannotKickLeftUser` now get through every
protocol step and fail at `GET /sync` (F-013). `TestKnocking*` refuses a remote-user
invite with 501 (F-014). No M1 regression: `TestVersionStructure`,
`TestUnknownEndpoints`, `TestRegistration`, `TestLogin`, `TestCanRegisterAdmin`,
`TestServerCapabilities`, `TestMediaConfig` still pass. Zero panics, zero HTTP 500.
No Complement assertion was changed.

## Local evidence

`go test -count=1 ./tests/...` ok; `make verify` (`-race`) ok; `golangci-lint run ./...`
0 issues. New tests: `tests/integration/membership_test.go`,
`tests/integration/aliases_test.go`, plus `tests/unit/roomslogic_test.go` additions and a
corrected `TestPick` for the widened version range.

## Honest status

Still single-server only: any membership change or alias resolution that needs another
homeserver is refused with 501. No `/sync`, so no client-observable room flow end to end.
The on-receipt rejection outcome is still not persisted (F-011).

---

## 2026-11-05 — T-3001a/T-3001b/T-3001c, T-2007 to T-2011, room versions (M2/M3)

Scope: `internal/sync` (`/sync`, filters, device data, read markers), room versions end to
end, aliases, room reads and history visibility, report and joined_rooms.

What is now true:

* A client can create a room in any version in the advertised range and use it. Complement
  accepts v3, v4, v6, v8, v9, v10, v11 and rejects v42 with `M_UNSUPPORTED_ROOM_VERSION`.
* `/sync` works incrementally with stored and inline filters, `since` position tokens,
  `limited`/`prev_batch`, unread counts, ephemeral and account data, and the `leave`
  section. Complement `TestSyncFilter`, `TestSyncSendersFilter`, `TestSyncLeaveSection`,
  `TestGappedSyncLeaveSection`, `TestOlderLeftRoomsNotInLeaveSection`,
  `TestArchivedRoomsHistory`, `TestLeaveEventVisibility`, `TestCumulativeJoinLeaveJoinSync`
  pass.
* What a room the user is out of may show is decided by the room, not by the request: the
  timeline stops at the membership event that took them out, and state is shown only where
  `m.room.history_visibility` permits it.
* Room reads split into three rules (current member / former member up to their leaving /
  outsider where the room is world readable), and `/joined_members` and `/aliases` stay
  behind membership even in a world readable room.
* A deleted alias corrects the room's `m.room.canonical_alias` event.

Complement (filter: sync, filters, room state/alias/membership/reporting, devices,
registration, login, versions, media config, room summary, left rooms):
**6 failing top level tests**, down from 15 at the start of this round.

| Failing | Cause |
| --- | --- |
| `TestRoomCreate` | v2 and v6 rooms cannot be created locally (v1-spelled invite, signed remote join) |
| `TestRoomState` | `GET /publicRooms` is not implemented (T-3003) |
| `TestRoomSummaryAllowedRoomIDs` | room summary `allowed_room_ids` needs the invite/ban state the summary does not carry |
| `TestSync` | `TestSyncTimelineGap` needs two homeservers (T-4005) |
| `TestSyncOmitsStateChangeOnFilteredEvents` | needs a federated join and lazy-loaded member state |
| `TestPushRuleCacheHealth` | push rules are read-only; `PUT`/`DELETE` missing (T-3005) |

`TestLeftRoomFixture` now fails only on `GET /rooms/{roomId}/messages` (T-3002); its state
and member reads pass.

Local evidence: `make verify` `-race` ok — 156 integration, 169 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was changed.

Health during the run: no HTTP 500, no panics.

---

## 2026-11-05 — correction to the two records above

The commit messages for `8a3afb1` and `db6e183` said `.agent-work/TASKS.md` and
`.agent-work/REQUIREMENTS.md` were updated. They were not: the edits were anchored on text
that this project's files do not contain, so they silently matched nothing. The code, the
tests and the Complement numbers in those messages are accurate; the tracking files were
left stale. `db6e183`'s successor fixes that — the requirement rows for the room, sync and
read behaviour added during M2/M3, and the task list, are written in this file pair now, in
the formats these files actually use. Recorded here because AGENTS.md §16 treats an
inaccurate record as a defect, not a detail.

## 2026-11-05 — history paging, the room directory, room summaries (M3)

Scope: `internal/stream` (pagination positions), `internal/rooms/history.go`,
`internal/rooms/directory.go`, `internal/persist/directory.go`, `internal/sync` summary
nesting, four new endpoints.

What is now true:

* `GET /rooms/{id}/messages` pages a room in both directions from a sync token. A page
  stops promising more events when the timeline ends **or** when what remains is beyond
  what the requester may read — a departed member asking what came after they left gets an
  empty chunk and no token.
* The room directory is a decision rather than a consequence: `visibility: public` on
  `/createRoom` publishes a room, `PUT /directory/list/room/{id}` changes that, and only a
  moderator of the room may do it. `GET`/`POST /publicRooms` search, page and describe the
  published rooms.
* A room created with `room_alias_name` now also names itself in `m.room.canonical_alias`.
* `GET /v1/room_summary/{roomIdOrAlias}` describes one room, including `allowed_room_ids`
  for restricted rooms.
* `/sync` puts its member counts in `summary` under `m.joined_member_count` and
  `m.invited_member_count`. They were being emitted beside the room, where no client
  would find them.
* A latent number bug: power levels and depth read numbers out of decoded event JSON, and
  the decoder that preserves canonical JSON yields `json.Number`. A plain `float64` type
  switch returned zero, which in `powerLevel` meant the creator could not publish their
  own room. Both readers now handle every form the value arrives in.

Complement (filter: room directory, messages, left/archived rooms, aliases, membership,
auth, devices, versions, sync filters, summaries): **5 failing top level tests**
(`TestLeftRoomFixture`, `TestRoomCreate`, `TestSync`, `TestSyncTimelineGap`, and the
`tests/` package `TestSync`).

| Failing | Cause |
| --- | --- |
| `TestLeftRoomFixture` | a former member's *old* state value needs per-event state snapshots (T-3004); its `/messages` window is ordered by stream position where the reference server orders by depth (T-3006) |
| `TestRoomCreate` | v2 and v6 rooms cannot be created locally (v1-spelled invite, signed remote join) — federation (T-4005) |
| `TestSync` (csapi) | MSC4222 `state_after`, an unstable extension; deliberately not implemented |
| `TestSync` (tests) | presence in `/sync` (T-3007) |
| `TestSyncTimelineGap` | needs two homeservers (T-4005) |

Newly passing since the round before: `TestPublicRooms`, `TestRoomState`,
`TestRoomSummary`, `TestRoomSummaryAllowedRoomIDs`, `TestSendAndFetchMessage`,
`TestRoomMessagesLazyLoading*`, `TestFetchMessagesFromNonExistentRoom`.

Local evidence: `make verify` `-race` ok — 170 integration, 169 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

---

## 2026-11-05 — push rules, and getting them to the client (M3)

Scope: `internal/pushrules` (new), `internal/persist/accountdata.go`,
`internal/api/client/pushrules.go`, `internal/sync/accountdata.go`, a registration hook in
`internal/users`, one index pair and one table in the migration.

What is now true:

* A user has push rules from the moment they exist. The server-default set is the
  specification's, including the rules added in v1.4, v1.7 and v1.9, with the three
  v1.7-deprecated ones still present because the specification's condition on them is
  about matching, not existence. Rules that name the user name *them*: an invite rule whose
  `state_key` pattern was a placeholder would look right and notify nothing.
* The whole API under `/pushrules` answers, at every depth, including a rule's `enabled`
  and `actions`, and writes keep the order that *is* the priority: a new rule goes at the
  head of the user's section, the server's defaults stay behind it, and `before`/`after`
  cannot refer to a default.
* The rule set reaches the client. `/sync` now has a top-level `account_data` section, and
  editing a rule bumps the stream position, so a long-polling sync wakes up instead of
  timing out.
* Defaults are created with the account (`users.Service.AfterRegister`) and swept once at
  start-up for accounts that predate the code.

Three things found on the way, all recorded because each was a trap for the next writer:

1. **A transaction inside a transaction deadlocks on a single-writer database.** The first
   version of the service opened a transaction and then called a helper that opened
   another, waiting for the connection the first was holding. The store now has one writer
   that never opens a transaction (`write`) and one wrapper for callers that hold none
   (`save`), and the reason is in the comment rather than in a commit message.
2. **A storage "not found" is not a protocol "not found".** Reading a deleted rule reached
   the client as a 500. `ruleError` maps it to `M_NOT_FOUND`, the code the push rule
   endpoints prescribe.
3. **A rule ID that starts with a dot has to be escaped, and the escape is easy to get
   wrong.** `%2E.m.rule.master` decodes to `..m.rule.master`, not `.m.rule.master`: the
   literal dot after the escape is a second dot. It is `%2Em.rule.master`. The test
   originally asserted against the wrong path, not the wrong server.

Complement (filter: push rules and push sync, room directory, summaries, `/messages`, sync
filters, logout/devices, registration/login, room creation): **no push-rule failure**;
`TestPushRuleCacheHealth` and `TestPushSync` pass, including the four "wakes an incremental
/sync" subtests. Still failing: `TestRoomCreate` (v2/v6, federation), `TestSync` (csapi:
presence, `device_lists`, a two-server join, MSC4222 `state_after`; tests package:
presence).

Local evidence: `make verify` `-race` ok — 174 integration, 175 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

Honest gap: the rules are stored, edited and delivered, but nothing acts on them yet —
there is no pusher, no push gateway call, no `GET /notifications`, and the unread and
highlight counts in `/sync` are still zero. That is task T-3005a, not a footnote.

---

## 2026-11-05 — the rule evaluator (M3, not yet applied)

Scope: `internal/pushrules/evaluate.go`.

`Ruleset.Evaluate(event, context)` decides what one event means for one user: the most
important matching rule wins, the master rule is consulted before everything else, and the
actions of that one rule are the answer.

The conditions are implemented as the specification defines them, including the two that
are easy to get wrong: `event_match` on `content.body` matches a *substring bounded by
words* rather than the whole value, and a dot inside a property name is escaped
(`content.m\.mentions.room`), so a path split without caring about the backslash reaches
the wrong object. A condition kind this server does not know never matches — the rule is
kept, and it is silent, which is what the specification asks for.

An event the user sent themselves never notifies, whatever the rules say.

One expectation of mine was wrong while writing the tests: between the kinds, the
specification orders room rules *above* sender rules. A sender rule feels more specific to
a person reading it, and the test said so; the specification does not, and the code follows
the specification.

What is NOT done: nothing calls this yet. `/sync` still reports zero unread and highlight
counts, and there is no pusher, push gateway request or `GET /notifications`. The missing
piece for the counts is not the evaluation — it is a read marker to make "unread" mean
something (T-3005a).

Local evidence: `make verify` `-race` ok — 174 integration, 199 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement test changed, because none of this is
reachable from the API yet.

---

## 2026-11-05 — presence, and one stream instead of three (M3)

Scope: `internal/presence` (new), `internal/persist/presence.go`,
`internal/persist/stream.go` (new), `internal/api/client/presence.go`, the presence section
in `internal/sync`, a join hook in `internal/rooms`.

What is now true:

* `GET`/`PUT /presence/{userID}/status` answer. A user may only set their own; an unknown
  presence value is refused; a user on another server is answered as not known rather than
  guessed; a user this server has never reported is `offline`, because "are they around"
  has that answer before it has "I don't know them".
* `/sync` has a `presence` section. Presence is not room data, but the room is what decides
  whose presence you may see, so a join announces the arrival to the members already there
  and their presence to the joiner.
* `set_presence` is honoured. It defaults to `online`, and an unchanged status is *not*
  written again: a client polls, and writing every poll would announce the user to everyone
  they share a room with over and over. `MarkActive` exists for the same split — being seen
  refreshes `last_active_ago` and deliberately takes no stream position.

A real defect was found on the way, and it was in code shipped two days ago:

* Positions were taken as `MAX(events.stream_ordering) + 1` by every writer — events *and*
  account data. So the position `/sync` reported as `next_batch` (the events maximum) could
  sit below positions account data had already taken, and a later event could be handed a
  position an account data write had taken. A sync token is supposed to mean "everything up
  to here"; under that scheme it meant "every event up to here, and possibly some things
  after it, and a client holding it may be told the same thing again on every poll".
  Positions now come from one counter — the maximum across `events`, `account_data` and
  `presence` — and `next_batch` is that same maximum. `TestPresenceStreamSharedWithEvents`
  pins the cross-table ordering, because that is the invariant, not the implementation.
* The join hook runs *after* the membership transaction commits. Calling the presence
  service inside it would have reproduced the deadlock already recorded in the push rule
  service: the presence write goes through the pool, and the pool has one writer which the
  open transaction is holding.

Honest gaps: nothing makes a user go quiet, so `online` sticks until the user or a room
event says otherwise (T-3001e — it needs a policy decision, so it is a task, not a guess);
no federation presence EDUs, so remote users are not known (M4); the filter's `presence`
section is not applied (T-3001f).

Complement: `TestPresence` passes with all its subtests, and both presence subtests of
`TestSync` pass. `TestSync` now fails on three things: `Device_list_tracking` (device keys,
T-3008), a sync token across a redaction of an unknown event (needs federation to create
the event, T-4005), and MSC4222 `state_after` (unstable, deliberately out).

Local evidence: `make verify` `-race` ok — 176 integration, 203 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

---

## 2026-11-05 — device list tracking in `/sync` (M3)

Scope: `internal/persist/device_lists.go` (new), `internal/rooms/membership_effects.go`
(renamed from `presence.go`), the `device_lists` section in `internal/sync`.

`device_lists.changed` names a user whose keys are worth asking for again;
`device_lists.left` names one whose keys are no longer worth keeping. Both take a position
in the shared stream, and both record the room the change happened in — the member list of a
room cannot answer "who may hear about this", because the user who left is no longer in it.

The section is absent rather than empty when there is nothing to say: an empty object reads
as "nothing changed", which is a different answer from "there is nothing to report".

Membership is the only trigger today. Key uploads, new devices and cross-signing updates are
the other triggers in the specification, and none of those endpoints exist on this server
yet, so the requirement row says "partially" and the task list carries T-3008b rather than
letting a passing Complement test imply that encryption works. It does not: a client told to
re-fetch keys has nowhere to ask.

Complement: `TestSync/parallel/Device_list_tracking` passes, including the retry case.
`TestSync` now fails on two things only — a redaction of an unknown event that needs a
second homeserver to create (T-4005), and MSC4222 `state_after` (unstable, deliberately out,
along with the whole `tests/msc4222` package).

Local evidence: `make verify` `-race` ok — 178 integration, 204 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

---

## 2026-11-05 — room upgrades (M3)

Scope: `internal/rooms/upgrade.go` (new), `internal/persist/alias.go` (`MoveRoom`), the
tombstone branch of `internal/rooms/membership_effects.go`, `pushrules.Service.RoomUpgraded`,
`POST /rooms/{roomID}/upgrade`.

Both ways an upgrade happens are supported, because clients do both: the endpoint, and a
client that creates the new room with a `predecessor` in `creation_content` and then closes
the old room by hand. What the server does:

* The replacement's `m.room.create` carries `predecessor` with **both** fields the
  specification requires. A client that names only the old room is not wrong — filling in the
  event is the server's job, because it is the one that knows the room.
* The `type` and `m.federate` of the old room's create event are copied. A space that
  upgraded into a plain room would stop being a space, and nobody asked for that.
* The nine state types the specification recommends are carried; membership is not, because
  signing a join for a user on another server would be forging their arrival.
* The aliases move. A name left on a room whose own state says it is finished leads clients
  somewhere dead.
* A tombstone anywhere — from the endpoint or from a client — carries each local user's
  room-scoped push rules across. That behaviour is not in the specification's upgrade
  section, so it is recorded as an implementation convention rather than as a requirement.

One bug found while testing it, in code written an hour earlier: `pushrules.load` reports
whether the ruleset had to be *created*, and the new copy carried the case backwards — every
user with real rules was skipped, and only a user with none was processed. The integration
test caught it; the Complement test would have caught it too but only after a federation
join that never gets that far today.

Decisions recorded rather than defaulted: an upgraded room still accepts events (T-2010b —
refusing them affects clients that have not seen the tombstone, so it is a decision, not a
default), and `m.room.canonical_alias` is not copied even though the alias itself moves
(the person upgrading did not choose the new room's name).

Complement: `TestPushRuleRoomUpgrade`'s two local subtests pass. The two remote ones fail at
`POST /join` for a room on another homeserver — T-4005.

Local evidence: `make verify` `-race` ok — 181 integration, 204 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues. No Complement assertion was modified.

---

## 2026-11-05 — device keys, one-time keys, and who may hear about them (M3)

Scope: `internal/e2ekeys` (new), `internal/persist/devicekeys.go` (new),
`internal/persist/device_lists.go` (observer-addressed), `internal/api/client/keys.go`,
`persist.CurrentMembers`.

`POST /keys/upload` (and PUT, for clients written against older text), `POST /keys/query`
with `since`, `POST /keys/claim`. A key document is stored as the bytes that arrived, because
its signature is over exactly those contents. A one-time key is only accepted signed, and its
signature is checked against the identity keys the device published — not against anything in
the key's own document, because a key that vouches for itself vouches for nothing. Keys are
claimed once, in upload order: a claim that reordered them would hand out sessions in an order
the client never offered.

Two behaviours were worth the bug hunt:

* A key upload must tell the other clients of the rooms this user is in to fetch the keys
  again. `TestDeviceListUpdates` checks that, and it also checks the case a room-shaped model
  cannot serve: a user who leaves must be told that the people still in the room are no longer
  theirs to track. A notice shelved in a room cannot reach the person who just left it, so a
  device list change is now addressed to its observer at write time (DEC-017).
* Joining announces both directions. The members are told about the newcomer *and* the
  newcomer is told about the members — presence already did that; device lists did only half.

One deliberate relaxation, recorded rather than hidden (DEC-016): an offered device-key
signature is verified and a bad one refused, but a document that offers no signature at all is
stored. The specification says the device signs its identity keys, and it also says the client
that is about to encrypt to them is the one that verifies them, and that a client must not rely
on the server being well behaved. Complement's device list tests upload documents signed by
nobody — Synapse and Dendrite store them — and the behaviour those tests describe is worth more
here than a claim this server is not entitled to make. One-time keys are not relaxed: their
format requires a signature, and it is checked.

Local evidence: `make verify` `-race` ok — 186 integration, 205 unit tests; `go vet` and
`golangci-lint run ./...` 0 issues.

Complement: the four key-upload/claim tests pass; five of the ten `TestDeviceListUpdates`
subtests pass — all the ones that need a single homeserver. The other five, and the remaining
failures in `TestPushRuleRoomUpgrade` and `TestSync`, all stop at the same place:
`POST /join` for a room this server has never heard of. Federation (T-4005) is now the largest
single cause of Complement failures here.

## 2026-11-05 — federation begins: two homeservers, one process (M4)

Federation stopped being a placeholder. A homeserver can now authenticate another
homeserver, decide what its keys are worth, accept its events after checking everything
the specification says a receiver checks, answer for the ones it took, and keep trying to
deliver the ones it owes — without any of that being able to block a local user.

What was built:

* `internal/federation`: the X-Matrix credential in both directions (`auth.go`); server
  name to address, with `.well-known` delegation, both SRV service names, IP literals and
  explicit ports, and answers cached under a TTL (`target.go`); the addresses this server
  is willing to dial, denied by default for loopback, link-local — where the instance
  metadata services live — CGNAT, multicast and the private ranges (`ippolicy.go`); other
  servers' signing keys, fetched direct or through a notary, trusted only when that server
  signed a key it publishes, with the trust window clamped to a week whatever the document
  claims and retired keys honoured until their own `expired_ts` (`keyring.go`); the
  outbound client and the queue that retries with a bounded schedule and remembers a
  remote's refusal instead of retrying it forever (`client.go`, `sender.go`).
* `PUT /_matrix/federation/v1/send/{txnId}`: authenticated before a byte of the body is
  read, the body's `origin` required to agree with the credential, the PDU and EDU caps
  enforced, a replayed transaction answered identically, and every PDU answered by its own
  event ID with its own reason. `GET /_matrix/federation/v1/event/{eventId}` hands an event
  to a server the room's own state says is present, and to nobody else.
* `rooms.Service.AcceptRemoteEvent`: the receiving checks in the specification's order —
  valid in its room version, its ID equal to the hash its content makes, its content hash
  matching, its origin server's signature checking, its cited auth events authorising it —
  and a room introduced by a remote `m.room.create` adopted at the room version that create
  event asked for, not this server's default. An event its branch authorises but the room's
  current state does not is stored and soft-failed (DEC-018): it is kept out of what this
  server relays and out of the forward extremities, and still shown to peers over `/event`,
  because the specification says a soft-failed event is part of the history.
* Federation configuration: `federation.enabled`, `notary_servers`, `ip_allow`, `ip_deny`,
  `timeout`, `queue_max_age`, `queue_max_attempts`, with the environment overrides
  (`LATTICE_FEDERATION_ENABLED`, `..._IP_ALLOW`, `..._IP_DENY`, `..._NOTARIES`), and
  malformed CIDRs stopping startup rather than vanishing into an open door.

Four defects the work exposed, each fixed with a test that used to pass:

* I "fixed" `GET /rooms/{roomID}/state` from a bare array to `{"state": […]}`, on the
  strength of the API-standards sentence that every endpoint answers with a JSON object, and
  rewrote two of this project's tests to assert the new shape. That was wrong, and the
  Complement run in this window is what said so: the endpoint's own schema documents
  "Array of `ClientEvent`" and shows a top-level array, `TestRoomState` walks that array, and
  Synapse answers with one. Reverted; the array is the contract (REQ-HTTP-007 now records the
  contradiction rather than a rule that does not exist). The lesson is recorded too: the local
  suite had been written around the wrong shape, so it defended the bug.
* Event signature verification used a generic JSON signer. In the room versions whose event
  IDs are hashes, the identifier is not part of the signed document, so keeping it makes every
  honestly signed event look forged: this server would have refused real events from every
  other server on the network, and had no test that could notice (DEC reference: the seal
  tests predate any receiver).
* Verification never checked the `hashes` an origin server records. In room versions 3 and up
  the event ID is the hash of the *redacted* form, so the body of a message is not in it — the
  content hash is the only thing that puts the body into the event's identity. Without that
  check, a homeserver in the middle could rewrite a message's text and present the same,
  correctly identified, correctly signed event (REQ-FED-010).
* `federation_queue.dropped_at` was a plain `time.Time`, so every queued row carried the zero
  date instead of NULL, and every "is this still owing?" query — including the claim that
  feeds the sender — answered no. The queue silently emptied itself and undelivered events
  were lost. Nullable columns in the storage layer are pointers now, where NULL is the answer.

Local evidence: full suite green — `go vet ./...` clean, `golangci-lint run ./...` 0 issues,
`tests/unit` and `tests/integration` pass under `-race` (`make verify`). New: `tests/unit/federationdiscovery_test.go`,
`tests/unit/federationkeys_test.go`, `tests/unit/eventsignature_test.go` (all supported room
versions), and `tests/integration/federation_test.go`, which runs two homeservers on real
listeners with real TLS certificates from a test CA and real `X-Matrix` signatures, moves a
room's beginning from one to the other, reads an event back and compares every field that a
hash depends on, and then checks the refusals: no credential, an unknown key ID, a body changed
after signing, a credential addressed to somebody else, an origin that disagrees with its
credential, a forged event signature, an event in an unknown room, a transaction of 51 PDUs,
and a replay.

A Complement run in the same window, and what it said:

* It caught the `/state` shape error above. The local suite could not: its assertions had
  been written around the wrong shape in the same window.
* `TestInboundFederationKeys` failed, and the reason was worth having: `GET
  /_matrix/key/v2/server` signed a document and then served it through Go's encoder. The
  bytes on the wire were not the bytes the signature covers — a trailing newline, Go's
  escaping, Go's number formatting. Complement verifies the body it received with the
  `signatures` member removed, and correctly called our own key invalid. The endpoint now
  answers with the canonical form (REQ-FED-000), and the integration test asserts both that
  the body is its own canonical form and that the signature checks over it.
  `TestInboundFederationKeys` passes. This is the second Complement-caught defect in this
  window; both were invisible locally because the local checks re-encoded what they
  verified.
* `TestRoomCreate` asks for a room in version 2 and is answered `M_UNSUPPORTED_ROOM_VERSION`,
  because room versions 1 and 2 are deliberately not implemented (T-4013).
* The sync suites now fail on narrower things than they used to: MSC4222 `state_after`,
  filters over the state section, depth-ordered paging across a gap, and a sync token that
  points at a redaction of an unknown event (T-2009). `TestRoomState`, `TestRoomAlias`,
  `TestRoomMembers` and the M1 suites pass.

What this does *not* establish: Complement's federation suites still fail (F-015) — they drive
federation through a join, and `/make_join`, `/send_join`, `/state_ids`, `/backfill` and
`/invite` are not implemented (T-4009). An event whose predecessors are missing is refused with
the reason named rather than fetched (T-4007). EDUs are acknowledged and logged, and nothing
else happens to them (T-4008). The address policy is applied when discovery resolves a name,
not when the socket opens, so DNS rebinding is not covered (T-4011).

Follow-up in the same window, found while writing the queue's own tests:

* The federation tables hold promises, and nothing was calling the code that forgets
  expired ones. `Sender.Cleanup` now drops answered transactions past their replay window,
  key documents past their trust window and queue rows this server gave up on a month ago,
  and the worker runs it hourly (immediately on start, so a process that was down for a day
  tidies itself). A failure that is still recent keeps its row: "why did that room never
  arrive?" has to be answerable while it is still a live question.
* `SeenTransaction` took an instant and ignored it. An answer kept past its expiry is not
  idempotency — it is a stale reply to a new request that happened to reuse a transaction
  ID. The window is now part of the lookup.
* New: `tests/unit/federationqueue_test.go` asserts the queue's promises directly — a claim
  cannot be taken twice, a retry is not claimable before its time, delivered work leaves the
  table, giving up leaves a record that stops counting as owing.

`make verify` and `golangci-lint run ./...` clean: 191 integration, 211 unit tests.

## 2026-11-05 — a refusal that outlives the event it refuses (M2, T-2006a)

The authorisation rules have always said that an event citing a rejected auth event is
rejected. Nothing in this server could act on that: a refused PDU was not stored, and the
verdict went with it, so `Snapshot.Rejected` answered `false` in every real snapshot and the
two rule paths that consult it — `eventauth` rule 2.3 and the iterative check inside state
resolution — were decoration.

Now `event_rejections` holds the verdict: event ID, room, the reason, the server that
refused it. `dbSnapshot.Rejected` and `citedSnapshot.Rejected` read it, and rule 2.3 is
asked **before** the auth event is looked up. That order is the whole point. A refused
event is absent from this server either because nobody mentioned it or because this server
was told and said no, and looking it up first answers both with "cannot be resolved" —
which turns a verdict into a gap someone could still fill.

Two boundaries matter as much as the memory:

* A refusal that was only "this server could not judge yet" — unknown room, missing
  predecessors, an auth event nobody has mentioned — is **not** recorded (DEC-021). A
  handover that arrives out of order is a temporary hole; recording it as a verdict would
  permanently ban an event this server never actually judged.
* An event ID this server already holds is never recorded. Without that, a stranger
  offering forged bytes under a good event's ID would have made the good event a rejected
  one, and every successor citing it would have been refused. The lookup that decides
  "already have it" now also stops a storage error from being answered as a verdict.

Evidence: `tests/unit/rejections_test.go` (the verdict survives, a second refusal does not
rewrite the first, an unseen ID is not reported as refused, rule 2.3 fires for an absent
but refused auth event while an unmentioned one still reports a gap) and
`tests/integration/federation_test.go` (`TestFederationRefusalIsAVerdict`: two homeservers,
a forged PDU refused and remembered, an honestly sealed event citing that PDU refused with
the refusal named, an honestly sealed event citing an unmentioned ID refused and *not*
recorded).

Found while doing this, and now tracked as T-2017: **the room's current state is settled by
arrival order.** `rooms.persistEvent` sets a state key to the last state event persisted for
it; `internal/state.Resolve` implements State Resolution 2, is unit-tested, and is never
called. For one server that is usually the same answer. For two it is not, and AGENTS.md and
the specification both forbid the substitution. The resolver needs the state each forward
extremity was carrying, which is per-event state snapshots (T-3004) — so T-2017 is
sequenced after T-3004 rather than worked around. The compliance matrix now says this out
loud instead of listing SR2 as implemented.

`make verify` (fmt, vet, `-race`) and `golangci-lint run ./...` clean.

## 2026-11-05 — redaction: each server destroys its own copy (T-2009, T-2014, T-2016a)

Three endpoints and one rule, and the rule turned out to be the interesting part.

The authorisation rules deliberately say nothing about the redact level — they decide only
whether an event may be sent. Whether a redaction *destroys anything* is decided later, by
each server that receives one, on two conditions either of which is enough: the sender's
power level reaches the room's redact level, or the sender's domain matches the domain of
the sender of the event being redacted. The second is what lets a homeserver tidy up its own
users' messages without anybody gaining power over another server's events. So an event can
pass every auth rule and still never remove a byte, and `PUT /redact` now refuses a
redaction this server would never apply instead of filing one that does nothing forever.

The halves of a redaction can arrive in either order, so the link is kept on receipt and
re-checked by persistence from both directions: a redaction whose target is absent waits, and
the event that finally arrives settles it. The test hands a redaction to a second homeserver
before its target, then the target, and checks the second server ends up with a redacted copy
it applied itself.

Found on the way, and it changed the design (DEC-023): the specification strips the event
"upon receipt of a redaction event" — the receiver strips its own copy. So federation carries
the **unredacted** event plus the redaction, and the content hash is checked unconditionally;
`events.redacted_json` is a *view* for clients (`Event.Serving()`), never a replacement for
what was received. The first attempt did the opposite — forwarded redacted forms and skipped
the content-hash check for them — and `TestEventSignatureSurvivesEveryRoomVersion` failed for
every room version, which is that test doing exactly its job: a stripped copy of an event must
not pass as the event it redacts. gomatrixserverlib checks the content hash with no redaction
exception either.

Judged against the room's *current* power levels at the moment of application (DEC-022),
because the power levels a redaction was authorised against are not stored per event; T-3004
would let that question be asked properly. Recorded, not hidden.

`GET /rooms/{roomID}/event/{eventID}` came with it: an event in another room answers as not
found, the same as an event that does not exist, so the endpoint does not report which room
holds which ID. Complement's `TestRedact` passes, and `TestPowerLevels` passes too — it had
been failing only because its `SendEventSynced` helper reads events back through `/event`.

Still open, recorded as tasks: `/sync` serves the redacted form but not the
`unsigned.redacted_because` copy (T-2009b), and a redaction this server may not apply is still
listed in timelines instead of being withheld, which the specification says it should not be
(T-2009a).

Evidence: `tests/unit/redaction_rules_test.go` (target field per version, `ForClient`
placement, the redact-level and domain rule against configured and default power levels) and
`tests/integration/redaction_test.go` (emptied event with `redacted_because`, idempotent
retry, `/messages` and `/sync` agreement, unknown target and cross-room reads answered 404,
v10 top-level `redacts`, and the out-of-order handover over federation).
`make verify` (fmt, vet, `-race`) and `golangci-lint run ./...` clean; Complement filtered run
`TestJson|TestPowerLevels|TestRedact|TestEventAuth|TestRoomMessages…|TestSync` green except
`TestRoomCreate` (v2 refused by design), `TestSync/parallel` (needs the federation join
handshake and MSC4222 `state_after`) and `TestSyncTimelineGap` (depth paging).

## 2026-11-05 — the state each event was sent against (T-3004a)

`internal/state.Resolve` implements State Resolution 2 and is never called, because the
question it answers — what state was each branch carrying — is not one this server can be
asked. `room_state` holds one row per state key: *where the room is now*. A fork needs *what
each branch thought*, and that had never been recorded.

So: snapshots. A `state_groups` row is an identity for one exact set of (type, state_key) →
event ID entries; `state_group_entries` holds them; `event_state_groups` points each event at
the one it was sent against. The identity is a hash of the entries, so two branches that
happen to agree share a snapshot without anybody having to notice, and a linear room grows by
one snapshot per **state change** rather than per message — which the integration test counts
rather than asserts.

Two things the storage got wrong on the first try, both caught by existing tests: a snapshot
was unique on its hash alone, so two rooms' empty snapshots collided and one room inherited
the other's state (room creation fell over with a 500); and "this event's snapshot is
unknown" must not be answered as "the room had no state", which would authorise nonsense
against an empty room.

What is *not* done: when an event joins branches that disagree about the state, this server
records the snapshot of the most recently persisted predecessor and logs that it is
provisional. That is a known wrong answer in one narrow place — the shape of the fork is now
recorded, so the answer can be resolved instead of guessed, which is T-2017.

Evidence: `tests/unit/state_groups_test.go` (order-independent identity, one entry changed is
a different snapshot, per-room uniqueness, empty snapshot stays empty, unrecorded events are
missing rather than empty, re-attach moves the pointer) and
`tests/integration/stategroups_test.go` (the create event's snapshot holds only itself; three
messages share one snapshot; the topic event's snapshot has exactly one more entry and the
following message shares it; snapshots and current state agree; fewer snapshots than events).
`make verify` and `golangci-lint run ./...` clean.

## 2026-11-05 — the fork question, answered by an algorithm instead of an arrival order (T-2017)

`internal/state.Resolve` had been sitting unused for weeks: it implements State Resolution 2
and nothing called it, because `room_state` holds one row per state key — *where the room is
now* — and the algorithm asks a different question, *what was each branch carrying*. T-3004a
recorded the snapshots; this is the wiring.

An event that joins branches, or a room left with more than one forward extremity, is now
resolved over the extremities' snapshots and the answer written back as current state
(`internal/rooms/resolve.go`). A room with one extremity and one predecessor still takes the
cheap delta path.

The confluence test found a bug in the algorithm itself, and it is the kind that never shows
up on one server. Two homeservers were handed the same fork in opposite orders; they agreed —
so the room looked fine — but running the resolver standalone on the same events disagreed
with what the servers had decided. Cause: `state.eventTimestamp` asserted
`interface{ Int64() int64 }`, and `json.Number`'s method is `Int64() (int64, error)`. So every
event decoded from JSON looked like it happened at time 0, the mainline ordering's timestamp
comparison silently did nothing, and forks settled by event ID.

That is deterministic, which is why nothing had caught it: Lattice would agree with Lattice.
Synapse orders by `origin_server_ts`, so Lattice and Synapse would resolve the same fork to
different state — a room split, on a code path that passes its own tests. The regression test
(`TestResolveOrdersJSONDecodedEventsByTheirTimestamp`) is written to fail against the old
reading, and I checked that it does: without the fix the *older* event wins.

Evidence: `tests/integration/statresolution_test.go` — same fork, opposite orders, two
homeservers agree; the loser is still stored (resolution decides what the state *is*, it does
not delete history); both extremities stay open; and the server's answer matches the algorithm
run standalone. `tests/unit/stateresolve_test.go` — both argument orders, and the JSON case.
`make verify` and `golangci-lint` clean. Complement filtered run unchanged from before this
work: `TestRoomCreate` (v2 refused by design), `TestSync/parallel` (needs the join handshake
and MSC4222 `state_after`), `TestSyncTimelineGap` (depth paging).

Opened rather than hidden: T-3004b (rooms that predate snapshots have no groups, and this
server leaves their current state alone instead of inventing one — DEC-025) and T-3004c
(`reconcileCurrentState` runs on every persist while a room is forked — correct, unmeasured;
DEC-024 records why materialising at persist was chosen over resolving at read).

## 2026-11-05 — one answer to "what may this client see" (T-2009a, T-2009b)

The redaction work had a gap I had noted and left: `/sync` served an emptied event with no
explanation, while `/messages` attached `unsigned.redacted_because` and said who did it. Same
room, same event, two answers — and a redaction that had not been applied yet was still
listing itself in timelines, which the specification asks servers not to do until both halves
are in hand.

Both are the same decision, so they now live in one place: `redaction.Servicer` turns a stored
row into what a client may see — the emptied view, `redacts` where the room version puts it,
the copy under `unsigned`, and whether the event should be shown at all. `/event`, `/messages`,
state reads and `/sync` go through it. `internal/events` already depends on `internal/redaction`
(an event's ID is computed over its redacted form), so the servicer decodes with `matrixjson`
rather than importing the event parser back — the dependency direction is not a detail here.

A withheld redaction is kept, not deleted; only the view changes. `/event` answers it as
not-found rather than with a distinct code, because "I hold this redaction but not what it
destroys" tells the client more than the rule asks for, and a client that gets a 404 simply
asks again later, which is the behaviour we want.

Evidence: `tests/unit/serve_test.go` (the emptied form plus its reason; withholding until the
partner arrives; a chunk that skips the withheld event and keeps the rest; `redacts` placement
per room version). `tests/integration/redaction_test.go`: `TestSyncExplainsWhyAnEventItAlready
SawWentEmpty`, and `TestARedactionIsNotShownToAClientUntilItsTargetArrives` — hs2 is handed the
room and a redaction but not the message, its own user joins, and then `/messages`, `/event`
and `/sync` all decline to show the redaction while the page around them still answers; when
the message arrives, both halves show and the target comes back empty. I checked that test
bites: with the skip removed, `/messages` returns 500 instead of a page.

`make verify` and `golangci-lint` clean. Complement filtered run unchanged: `TestRedact` and
the sync tests pass as before, so the added `unsigned` copy and the withheld redactions did not
disturb what other implementations expect on the wire.

## 2026-11-05 — a remote user joins: the resident half of the handshake (T-4009a)

Federation had transactions, ingest checks, an event read and an outbound queue, but no way for
a user on another server to become a member of a room held here. This closes the resident
server's half of that: `/make_join` (v1 and v3), `/send_join` v2, `/state_ids` and `/event_auth`.

The shape of the handshake is the point of it. The resident server answers `make_join` with an
*unsigned* template carrying what the joiner cannot invent — the room's version, where the
history ends, which state authorises a join, the depth — and refuses there if its copy of the
room does not permit the user. The joiner fills in `origin`, `origin_server_ts` and `event_id`
and signs; only then does the resident accept. It checks the signed event against what the URL
claims (same room, same user, membership `join`, and an event ID it recomputes rather than
trusts), stores it, signs it with its own key, and answers with the state *before* the join plus
the auth chain that lets the joining server check the room instead of believing it.

Two decisions worth their weight: the resident signs the event it accepted, because it is the
one attesting that this join entered the room and every other server in the room should see that
attestation; and the state handed back is the pre-join state, because answering with the
post-join state asks the joining server to authorise its own join against a room that already
contains it. Federation answers with stored bytes, not the client view — the redaction servicer
is for clients, and a peer checks the content hash the origin recorded.

The first remote join ever attempted here exposed a bug in the auth rules. Rule 4.3.1 ("if the
only previous event is an `m.room.create` and the state key is its sender, allow") is an
allowing clause; our code refused when the single `prev_event` could not be resolved. Since
`prev_events` are never among the `auth_events` a receiver is handed, a server checking an
event against its cited auth events can almost never resolve them — so every remote join except
the creator's own was being refused. Fixed, with cases in the auth-rule table so it cannot
quietly come back.

Evidence: `tests/integration/federationjoins_test.go` — the handshake end to end (bea joins
alice's room; hs1 lists her as present; the room bundle is handed to hs2, which accepts it and
then shows her the room), plus refusals: invitation-only room and a ban answered 403 before any
signing, an unknown room 404, an unsigned ask 401, a tampered join refused and the user *not*
listed afterwards, a leave offered as a join refused. `TestStateAndAuthChainAreAnswered
ToServersInTheRoom` checks the reads answer 403 to an outsider and 200 afterwards, and that an
event's auth chain does not contain the event itself.

Left open honestly: `m.room.server_acl` is not enforced anywhere, and the federation handlers
call a check that allows everything and says so in its comment (T-4009c, REQ-FED-012); the
joining half (T-4009b) needs the snapshot ingest of T-4007, because a handed-over room arrives
as a bundle and this server's ingest refuses an event whose predecessors it does not hold.

`make verify` clean, `golangci-lint` clean, Complement filtered run unchanged.

## 2026-11-05 — one of our users joins a room held elsewhere (T-4009b)

The resident half of the join handshake was in place; a local user asking to join a remote room
still stopped at the door. This is the joining half, and the piece it could not work without:
a room handed over by `/send_join` arrives as a *set* — the room's state, its auth chain, and the
join — whose members cite each other, and whose join cites the room's forward extremities, which
are not in the set at all. The existing ingest refused any event whose predecessors it did not
hold, which is right for a transaction and fatal for a handover: it would refuse the join of
every room that has ever held a message.

So a bundle is now taken as a snapshot (`internal/rooms/bundle.go`, DEC-028): every event is
verified on its own — nothing about a bundle makes it trusted as a whole — each is authorised
against the auth events it cites, whether those are stored here or arrived in the same bundle,
and the bundle is applied in dependency order by walking it to a fixed point. An event accepted
without its predecessors is stored with `outlier = true`: its state counts and it can authorise
later events, but this server does not know where in the history it sits, so it stays out of
client timelines and out of the room's forward extremities. The room's current state is not used
to soft-fail any of these events, because in a handover the current state is what the bundle is
building. A missing citation is a gap and is deliberately not recorded as a refusal — recording
it would make the gap permanent, so the same event could never be accepted after the missing
piece arrived. A forged event is a verdict: refused, and remembered.

On the joining side (`internal/federation/join.go`): the client's repeated `server_name`/`via`
parameters name who to ask, with the server the room ID names as fallback. A template is checked
before it is signed — this room, this user, membership `join`, and no `event_id`, `hashes` or
`signatures` already on it, because signing one of those means adding this server's name to an
event somebody else made. A resident server's refusal on the room's rules is not retried against
another server: that would be looking for one laxer about the room's join rules. A room ID that
carries this server's own name is answered 404 rather than asked of a stranger, which is how a
stranger would otherwise get a room invented under this server's name.

Evidence: `tests/integration/joinremote_test.go`. A user on hs2 joins a room on hs1 through the
Client-Server API; hs1 lists them as present; hs2 holds the room's foundations, its name, and
its own user's join in state; the handed-over message is *not* in hs2's timeline — which is the
outlier rule being honoured, and the assertion T-4009b1 will have to change; and a message the
joining user sends from hs2 is heard on hs1 through the relay. A join nobody answers invents no
room. A forged event in a bundle is refused and its refusal recorded.

Also fixed on the way: `App.Rooms` was declared but never assigned, so the assembled homeserver
exposed no room service to anything holding the app — the reason the bundle test needed it.

Left open and recorded: T-4009b1 (lifting the outlier flag by fetching what a handed-over room
cites, which is what will make joined history visible), T-4009c (`/make_leave`, `/send_leave`,
`/invite`, knock, and `m.room.server_acl`).

`make verify` clean, `golangci-lint` clean, Complement filtered run unchanged (TestRoomCreate,
TestSync and TestSyncTimelineGap were failing before this and still are).

## 2026-11-05 — a joined room's history is fetched, and an outlier stops being one (T-4009b1)

A room joined from elsewhere arrived with a hole under it by construction: the join cites the room's
forward extremities, and the state a resident server hands over does not include them. The events
that fill it are real, so they were stored — and kept from clients until they could be placed. This
is the pass that asks for them.

The holes are named from this room's own events, not from a guess about the room: an edge that this
server's events cite and no stored event answers (`persist.MissingPredecessors`, newest-first,
bounded). A server that was in the room before this one is asked for each, over `GET /event`, and
each answer names the events before it — so the walk goes backwards until it reaches history this
server already holds, which is where it stops rather than a hard limit. What comes back is ingested
as one bundle, ordered parents first: this server gives an event a position in its own ordering when
it stores it, and a chain fetched newest-first would otherwise be read oldest-first by a client
asking for the newest events — a client would be told that the sixth message came before the first.
Then `rooms.ResolveOutliers` clears the flag from every event whose predecessors are now held,
walking to a fixed point because a chain is usually linked from one end.

Two things were pinned deliberately. The fetch is bounded by an event budget and a pass count, and a
bounded fetch leaves the remainder *named and askable*: nothing is refused for want of budget, and
the next pass finishes the job. And a predecessor that is stored but soft-failed still counts as
held — soft-failure is a judgement about a conflict in the room's state, not about whether the event
sits in the history, and holding a perfectly good successor unlinked for it would claim something
about the graph this server has not established.

Evidence: `tests/integration/joinremote_test.go`. `TestBackfillMakesAJoinedRoomsHistoryReadable`
joins a room with a run of six messages on the other server and then reads all six, in the order the
run happened, on both servers; no event in the room is left unlinked; the join cites the room's last
message, and a message sent afterwards cites the join — so the branch continues the room's history
instead of starting a fork that someone has to merge later. `TestBackfillBudgetLeavesAGapAskable`
joins with a budget of two events: it gets the end of the history, does not show what it never
fetched, still has an outlier and a named gap, and a second pass with room in the budget makes the
whole run readable with nothing left unlinked. `tests/unit/eventstore_test.go`
(`TestOutliersAndMissingPredecessors`) checks the queries: a hole is named once whatever cites it,
newest-first, bounded, and a hole in another room is not this room's — that last one caught a
query that had no room scope at all, because edges carry no room of their own.

Left open and recorded: T-4009b2 (only a join triggers a fetch today — no worker pass, no fill when
a client pages into a gap, and we do not answer `GET /backfill` ourselves) and T-4009c (leave,
invite, knock, server ACLs).

`make verify` clean (`-race`), `golangci-lint` clean. Complement was re-run for the previous commit
and its filtered set is unchanged; this change touches no Client-Server API surface, only what a
joined room holds behind it.

## 2026-11-05 — a room can exclude a homeserver, and it is worth something at every door (T-4016, REQ-FED-012)

`m.room.server_acl` had been left as a call site that allowed everything and said so. This is the
rule behind that call site, read once into `internal/serveracl` and applied where a room is
described to a server, where a server pushes an event, and where this server decides whom to tell.

The decision is made in one place because it is asked in several: `/make_join` (v1 and v3),
`/state_ids`, `/event_auth` and `/event` answer an excluded server with 403 `M_FORBIDDEN`; each PDU
of a transaction is refused per event while the transaction itself is still answered; the room
service refuses an event whose origin the room has excluded and records the refusal; and the relay
does not queue a room's events for a server the room threw out. The room's state is what says so —
read at the moment of the request, so an exclusion takes effect and lifts again with the state that
made it.

Two things the specification settles and two it does not. Globs are `*` and `?` over the server name
without its port, case-insensitive, and `deny` is consulted before `allow` and wins: an `allow`
entry cannot rescue a name that `deny` covers. An ACL with no allow list excludes every server,
which is the specification's own consequence and is also what a redacted ACL event leaves behind.
What the specification does not settle is a field of the wrong shape, and this server declines to
apply such an ACL rather than reading a mangled `allow` list as an absent one — every field is
optional, so a typo in one field would otherwise cut a room off from federation entirely. That is
fail-open by choice, stated in the requirement (DEC-031) and pinned by a test that keeps a room
working through a garbled ACL.

A protocol fault turned up on the way and is fixed: events this server creates did not name their
`origin` server. `SendEvent` and `newStateEvent` both set `origin_server_ts` and neither set
`origin`, and the omission survived at home because signature verification here keys off the
sender's server name rather than the origin field. A PDU without an origin cannot be traced to
whoever made it — it is what a peer checks a signature against, what a room's server exclusion is
matched against, and what some rooms' auth rules read — so both constructors name it now, and
`TestLocallyWrittenEventsNameTheirOriginServer` checks a message and a create event for it
(REQ-EVT-008, DEC-032). Two redaction tests had to be told that the unredacted form they
reconstruct carries `origin` too, since the newer room versions hide that field in the redacted form.

The ACL tests also found a gap worth recording rather than papering over: while a room excludes a
server its messages are withheld, and once the exclusion is lifted the next message is refused by
the excluded server because it descends from events it was never told about. The room is reachable
again; the hole in the middle of its history is not filled. That is T-4009b3 — asking for the
predecessors an incoming event cites, which is the specification's `/get_missing_events`, on
machinery `federation.Backfiller` already provides.

`make verify` clean (`-race`), `golangci-lint` clean. Complement: the filtered `csapi` set was run
at the previous commit and this change touches no Client-Server API surface; the server-ACL tests
are federation-path tests and Complement has no `federation_*` run against this server yet.

## 2026-11-05 — a hole under an incoming event is asked about, not judged (T-4009b3, REQ-FED-016)

An event that arrives describing itself as descending from events this server does not hold has two
bad answers and one good one. Refusing it is a verdict about an event that may be perfectly good,
judged by the state of this server's copy of the room. Storing it as an outlier without any way to
ask leaves it invisible to clients for as long as the hole stays. The good answer: name the missing
events, ask the server that just offered the event for them — it plainly has them — and offer the
event again.

`AcceptRemoteEvent` now returns `*rooms.MissingPredecessorsError` with the IDs, and the transaction
handler answers it with one bounded ask (`Backfiller.FillFrom`, seeded by exactly those events and
walking backwards from them) followed by one retry. One ask and one retry: a sender that keeps
offering unplaceable events must not turn each offer into a fetch. What comes back is ingested
parents first, which is what keeps the chased event from reading as newer than the event that
triggered the chase — the ordering rule of DEC-030 doing its job in a second situation.

The test that proves it is the server-ACL test, because an exclusion is a convenient way to make a
hole honestly: a room excludes a server, messages are withheld during the exclusion, the exclusion is
lifted, and the next message is refused for its hole. After the ask, both halves are held, the
timeline reads in the order the messages happened rather than the order they arrived, and the room is
caught up rather than permanently forked.

The specification's own endpoint for this — `GET /get_missing_events` — is still not implemented, and
this server does not answer `/backfill` either; the ask travels over `GET /event`, which reaches the
same events. Recorded under T-4009b2 and T-4012 rather than counted as done.

`make verify` clean (`-race`), `golangci-lint` clean.

## 2026-11-05 — chasing a Complement failure, and finding two real defects (DEC-034, DEC-035)

`TestSyncTimelineGap` has been in the failing set for a long time and reads like a sync bug. It is
not one. Following it produced three findings, two of which are fixed here.

The first was environmental and worth writing down anyway: in Complement's federation tests the
second homeserver is the host machine at `host.docker.internal`, which resolves to a private or
link-local address that this server's outbound policy — the SSRF protection — rightly refuses to
dial. The Complement image now names the ranges the harness lives on instead of switching the policy
off, which keeps the protection meaningful in a real deployment and the tests runnable.

The second was a real defect. The transport path already verified a peer's certificate against the
peer's *name* while dialing the address that name resolved to; the key endpoints did not, so they
verified against the IP. A homeserver with an ordinary certificate naming its domain therefore could
not have its signing keys fetched at all — and the failure surfaced as `M_UNAUTHORIZED: the request
is not signed by a server this server trusts`, which is about as far from the truth as an error
message can get. One helper now hands out name-verifying clients to both paths, and it clones the
library's default transport when the base client has none: without that the fix would have been a
no-op in exactly the deployment it was written for.

The third was a protocol judgement. `/make_join` had been refusing any ask whose `ver` differed from
the room's version. The specification reserves `M_INCOMPATIBLE_ROOM_VERSION` for a version this
server cannot speak; which version a room uses is the very thing the asker is calling to learn. A
speakable-but-different ask is now answered with the room's own version, which the response names, so
the asker signs what the room actually uses.

The test still fails, and honestly so: the join now completes its first half and stops at
`send_join`, where the harness's version 11 room arrives with no `event_id` — pseudo-IDs (T-4018) are
not implemented here. Recorded rather than papered over. The rest of the filtered Complement set is
unchanged, and `make verify` is clean.

## 2026-11-05 — three defects behind one Complement test, and it passes now (DEC-036, DEC-037)

`TestSyncTimelineGap` went from failing to passing, and getting there meant finding three things
that were genuinely wrong rather than making the harness happy.

**The join handshake was being refused at random.** `/make_join`'s `ver` parameter is repeated once
per room version and names the versions the *asking* server can speak. This server read the first one
out of an unordered list and compared it with the room's version, so a peer listing `3`, `9`, `11`
and a proposal version was answered or refused depending on which came first. The rule now is the one
the specification describes: is the room's own version in the set? If it is, the template goes out in
the room's version; if it is not, `M_INCOMPATIBLE_ROOM_VERSION` carries the room's version, because
naming the version the asker cannot speak is the only useful thing a refusal can do. A server that
names no versions is answered rather than refused on the documented `[1]` default — holding it to
that legacy artifact would leave every room from version 3 on unjoinable by a server that simply
asks. DEC-035, recorded earlier in this window, was wrong in detail and is marked superseded by
DEC-037.

**An event without an identifier was refused, although the room version computes one.** Every room
version from 3 on defines the event ID as the reference hash and does not list `event_id` among the
required properties of the event format. So a PDU arriving without one is complete, and the receiver
names the event by hashing it — the duplicate check, the verdict and the stored copy all use that
name. A PDU carrying a label its own content contradicts is the same event wearing a wrong label: it
is filed under the hash, and the disagreement is logged rather than treated as a verdict. What is
still refused, whatever the label says, is an event whose content hash or origin signature does not
check. Room versions 1 and 2 are the opposite case and still require the sender's identifier, which
is why the unit test covers them.

**The room was left half-empty.** The combination of the two above is what a user sees: an event
refused for a missing label is a message that never arrives, in a room this server was told about.

Complement's filtered set now runs `TestSyncTimelineGap` green repeatedly; `TestRoomCreate` (room
versions 1 and 2, refused by design — T-4013) and `TestSync/parallel/Device_list_tracking` (device
lists over federation, T-4009c) are the remaining failures in that set. New tests:
`tests/unit/eventid_test.go` (every known room version),
`tests/integration/federation_test.go` (`TestEventOfferedWithoutItsLabelIsNamedByItsContent`) and
`tests/integration/federationjoins_test.go`
(`TestMakeJoinReadsEveryVersionTheAskerLists`) — the latter two were confirmed to fail against the
code before the change. `make verify` clean under `-race`, `golangci-lint` clean.

## 2026-11-08 — room versions 1 and 2 are offered, with the rules those versions actually have (T-4013, cycle 17)

Room versions 1 and 2 have been the oldest open Complement failure since cycle 4:
`TestRoomCreate/creates_a_room_with_the_given_version` asks a homeserver to make a room in version 2
and this server answered `M_UNSUPPORTED_ROOM_VERSION`. It was refused because supporting it honestly
means implementing the parts where those versions differ, and nothing less than that is worth
shipping. It now passes, along with the rest of `TestRoomCreate`.

What "the parts where they differ" turned out to be, in the order it was found:

* **The authorisation table those two pages share is not the later table early.** It is a separate
  ruleset (`eventauth.RulesV1V2`) because the clauses disagree in places that decide real events:
  a room whose `m.room.create` has no `join_rule` and no `m.room.join_rules` event admits the join
  (rule 4.4 — the later tables removed that clause, and Synapse has an equivalent accommodation
  through its `room_version → None` join-rule mapping, so this is not exotic); the membership clauses
  are keyed on the *event's own* sender-server name rather than the room's, which is also where the
  "origin must match the room" clause of later versions used to be; a redaction is allowed by the
  moderator clause **or** because the redacted event's event ID sits in the sender's domain; and the
  invite clauses care about the target's domain (version 1) or about the target having already left
  (version 2). The v1/v2 power levels rule also tolerates a top-level `users_default`, which later
  versions dropped.
* **State Resolution 1 is a different algorithm** (`internal/state/stateresolutionv1.go`): conflicts
  resolved one state class at a time — power levels, join rules, memberships, then everything else —
  with depth and the SHA-1 of the event ID as ordering keys. It is implemented because rooms in that
  version exist and their state has to be computed the way that version defines it, not because the
  algorithm is good; its documented failure modes are written in the file's comment. Noted honestly:
  Synapse resolves those rooms with a richer procedure (auth difference, power events, reverse
  topological order). The two agree on an unforked room and can disagree on a forked one; this server
  follows the pinned specification page, and the divergence is recorded in
  `docs/compliance-matrix.md` item 5 rather than discovered later by an operator.
* **The identifier is the origin server's to choose, and it is inside what gets signed.** Every
  outbound path had to mint an ID before sealing: local send (`rooms.prepare`), the join handshake
  (the joining server mints its own; the resident server's template carries one, and `send_join`
  takes the identifier from the event it was sent because that is the event the joiner signed), and
  backfill. In a room whose version names an event by its hash, an incoming event with no `event_id`
  is named by its content (DEC-036); in these versions there is nothing to compute, so a missing
  identifier is a rejected event. `events.Seal` now refuses rather than signing something that cannot
  carry the name it will be stored under.
* **References are pairs in these versions** — `[event_id, {"sha256": hash}]` — and that shape is
  inside what gets hashed, so it cannot be normalised away. `events.References`/`ReferencedIDs` are
  now room-version aware, and the state resolver, the graph walk and the auth checks read references
  through them.
* **The auth chain ends where this server stops understanding the room.** An auth event from an
  unrecognised room version is still required to resolve, so an unresolvable one means the chain is
  incomplete rather than merely short (T-4013 in the ledger). A chain that stops early would let a
  forged event be checked against state the chain never reached.
* **Power levels in those versions may be written as strings, or as floats to be truncated**
  (`"100"`, `" 00100 "`, `"+25"`, `50.57`). The loader reads them only in the versions that allow it.
  A room whose moderators were written that way is a room with moderators; treating the values as
  absent would have quietly demoted everyone in it.

Two defects found while doing this, both wider than room versions 1 and 2:

* `events.ReferencedIDs` skipped pair-form entries entirely — it only ever read bare strings. That is
  the function the graph walk uses for `prev_events` in *every* room version, so a reference written
  in the pair form was invisible: the event looked like it had no parents.
* The server ACL check on a join template did not run for a room in version 1 or 2, because their
  templates carry no `event_id` and the ACL code uses the event ID domain to decide whose room is
  being templated. The rule reads the *room ID* domain in that case; a banned server can no longer be
  handed a join template for a version 1 room it was excluded from.

Verification: `make verify` clean, including `-race` and `-count=2` (`tests/unit` 1136s of test
work, `tests/integration` green); `go vet ./...` clean; `gofmt -l` clean. New tests:
`tests/unit/authorisation_legacy_test.go` (3), `tests/unit/stateresolvev1_test.go` (6 plus
subtests), `tests/unit/powerlevels_legacy_test.go`, `tests/unit/eventid_test.go` (2),
`tests/unit/chain_test.go` (+2 cases), `tests/unit/eventauth_test.go` (+1), and
`tests/integration/roomversionlegacy_test.go` (2): rooms created in versions 1 and 2, their events
named `$opaque:hs1`, every `prev_events`/`auth_events` entry fetched over federation checked to be a
pair whose hash recomputes to the referenced event's real reference hash, and a full federation join
handshake into a version 2 room. `tests/unit/eventsignature_test.go` now seals and verifies in every
offered version, writing each version's references in that version's shape and minting the identifier
where that version requires it first.

Complement: the **full** `./tests` + `./tests/csapi` suite was run for the first time since the
earliest record in this ledger, as a baseline rather than a filter: **60 top-level tests passing,
136 failing** (Run 1, five cycles ago: 8 and 189). `TestRoomCreate` is green including
`POST_/createRoom_creates_a_room_with_the_given_version`; `TestRedact`, `TestServerCapabilities`,
`TestVersionStructure`, `TestCannotSendNonJoinViaSendJoinV2` and `TestCumulativeJoinLeaveJoinSync`
are green. The room-version tests that stay red are the ones about work not done: the MSC4297 state
resolution v2.1 pair (room version 12), `TestMSC4289PrivilegedRoomCreators_Upgrades` and
`TestMSC4291RoomIDAsHashOfCreateEvent_UpgradedRooms` (version 12 formats), and
`TestOutboundFederationIgnoresMissingEventWithBadJSONForRoomVersion6`. No Complement assertion was
modified.

## 2026-11-08 — an upgrade into a version this server only recognises is refused (T-4021)

Offering room versions 1 and 2 made the offered set wider than it had been, and looking at what else
reads that set turned up a doorway that had never been limited to it: `PUT /upgrade` checked
`roomversion.Get` — "do we have rules for this version?" — rather than "can we run a room in it?".
Room version 12 has registered rules and an unimplemented revised state resolution, so a room could
be upgraded into it: it would exist, keep accepting events, and be a room this server could not work
out the state of, while every other server in it computed something else. `roomversion.IsOffered`
now answers that question once, the upgrade path uses it, and the refusal is the same
`M_UNSUPPORTED_ROOM_VERSION` creation already gives.

`make verify` clean. Tests: `TestRoomUpgradeErrors` asks for `new_version: "12"`; `roomversion_test.go`
asserts that the advertised set and `IsOffered` agree, and that a known version is not automatically
an offered one.

## 2026-11-08 — a homeserver answers for its own users' keys, and discovery stops treating a broken resolver as an absent server

Three things, all found by following Complement failures instead of guessing at them.

**The Complement image was refusing to dial the peer container.** The federation address policy
denies private ranges on purpose — that is the SSRF protection — and the image's exception list named
`127.0.0.0/8, 172.16.0.0/12, 169.254.0.0/16`. On this machine the runtime is podman, whose created
networks are `10.89.0.0/16`, so every hs-to-hs dial in the suite was refused with "no allowed network
covers", and the failure looked like a protocol bug. Naming every runtime's pool would break the next
time somebody runs the suite somewhere else, so the entrypoint now allows the networks the container
is *attached to*, which is exactly where a peer homeserver in these tests can be, and nothing else
private.

**A resolver that cannot read SRV records is not an absent server.** `Resolve` treated any SRV lookup
error as fatal. Container DNS answers `_matrix-fed._tcp.hs1` with a timeout, while the name itself
resolves and the server is listening on 8448 — so a perfectly reachable peer was uncontactable. The
specification's own order is service records, then the name on the federation port, and an unreadable
record now leads to the second step. Separately, a name that could not be looked up at all was cached
for `wellKnownErrorTTL` — an hour, which is the specification's cache time for a *well-known
delegation document*, a thing a server publishes deliberately. A failed lookup is not that kind of
fact: one resolver hiccup made a healthy peer unreachable for an hour, with an error message ("was
resolved recently and could not be found") that reads like a durable fact about the other server. The
lookup failure now has its own 30-second window: long enough that a burst of traffic to one bad name
does not hammer DNS, short enough that the next attempt happens in the same shift.

**A homeserver answers for the key documents of its own users, and asks its peers for the rest.**
`POST /_matrix/federation/v1/user/keys/query` is served, and `POST /client/v3/keys/query` for a user
who lives elsewhere now reaches out instead of answering "no keys" — which is not a neutral answer,
because a client reads it as "this user has no devices". What is checked is the shape of the answer:
only the users that were asked about, only those whose IDs sit in the namespace of the server that
answered, and the documents themselves untouched, because the device signed them in that shape and
the client is the one entitled to check that. One unreachable peer leaves that user's entry empty
rather than failing the whole query, per the rule that a dead remote must not block local work. The
inbound half refuses to answer for users that are not its own, which the new test checks directly by
asking a server for somebody else's user. While at it, `GET /account/whoami` now names the device the
token belongs to: keys, push rules and the device list are per-device things, and a client that
cannot tell which of its own sessions it is in cannot manage them.

Evidence: `make verify` clean. New tests — `TestFederationDiscoveryRetriesALookupFailureSoon` and
`TestFederationDiscoverySurvivesAnUnreadableSRVRecord` (unit),
`TestRemoteDeviceKeysComeFromTheirOwnServer` (integration, two homeservers: the key document arrives
byte-for-byte, and a server asked for another server's user answers with nothing), and the existing
`whoami` assertion extended to the device identifier. Complement's full `./tests` + `./tests/csapi`
suite: **62 top-level passing, 134 failing**, up from 60 and 136 earlier the same day, with two tests
newly green (`TestJoinViaRoomIDAndServerName`,
`TestUserAppearsInChangedDeviceListOnJoinOverFederation`) and none newly failing.

The device-list tests still fail, and the cause is now measured rather than assumed: they used to stop
at the key query (0.4s in) and now stop at a `/sync` wait for a device-list change that nothing
delivers (5s in). That is `m.device_list_update` EDUs in both directions, opened as T-4022.

## 2026-11-08 — a device list change on one server reaches the clients of another (T-4022), and a timeout middleware stops racing with the handler it timed out

**The channel.** `m.device_list_update` ephemeral units are now sent and acted on. Out: when a local
user's devices change (the key-upload path already recorded the local half of that, so it was one
call away) and when a local user joins a room held elsewhere. In: a received announcement is recorded
for the local users who share a room with the user named, which is the same question the local
announcement answers, so the same recording is used. The transport was already there — the sender
queue carries EDUs — so this is the two ends and the trigger, not a new subsystem, which is what the
architecture note in DEC-017 anticipated.

**The rule that makes receiving it safe.** A peer is believed only about users whose IDs sit in that
peer's own name. A transaction carrying an announcement for `@mallory:hs3` from `hs2` is still
accepted — the peer did nothing wrong by sending it, and refusing the transaction would punish the
other units inside it — but the announcement does not become a fact here. Without that rule any
server could push a stranger into every local client's key tracking, and a client that tracks a user
starts trusting that user's keys.

**What is not stored, and why.** The key document inside the announcement is not kept, and the update
number is not compared against a stored one. Numbers and copies guard state this server does not
keep: a query about a remote user is answered by asking that user's server, which holds the signed
original. A duplicate announcement therefore costs a client a key query and nothing else, while a
stale local copy would be a place for a signed fact to go wrong. The per-user update *number* this
server announces is kept, in its own table, because a number that restarts with a device count going
backwards makes later announcements look stale to a peer that already applied a newer one.

**A separate trigger the first run exposed.** Joining a room that another server holds stores the
join as part of the bundle that server hands over, and an adopted bundle deliberately does not run the
side effects of a locally accepted event — running a peer's whole handover through the room's effects
would echo it back into the room. But the user who just joined is exactly the fact the other servers
have not been told about, so the join handshake announces it directly.

**A race that had been waiting.** `make verify` under `-race` began failing, and the report was not
in the new code: `httpserver.Timeout` writes a Gateway Timeout from another goroutine when the
deadline passes, and the deadline also passes when a client hangs up. The handler kept a promoted
`Header()` pointing at the same response that the timeout goroutine was writing — a data race on a
map. The guard now keeps the handler's headers private and commits them to the real response at the
first write, so a handler still working on a request nobody is waiting for cannot write a response
that has already been sent. This was latent, not new: the new EDU work only made it likely enough to
be caught.

Evidence: `make verify` clean, with zero `DATA RACE` reports (it had four). New tests:
`TestDeviceListChangesCrossTheFederationLink` (two homeservers: an upload on one shows up in the
other's `/sync`; a forged announcement for another server's user does not; the same shape naming the
sender's own user does — otherwise the forged case would only prove announcements are ignored) and
`TestDeviceListStreamsIncreasePerUser` (per user, strictly increasing, survives a restart).
Complement full `./tests` + `./tests/csapi`: **64 passing, 132 failing**, up from 62 and 134, with
`TestDeviceListUpdates` (all ten subtests) and
`TestDeviceListsUpdateOverFederationOnRoomJoin` newly green and none newly failing.

## 2026-11-08 — an invite for a user who lives elsewhere is countersigned by that user's server (T-4009c)

Commit `adf188a`.

An invite named a user on another homeserver used to be refused with 501. The handshake now
exists: the inviting server builds the event, the invited user's server signs it as well, and only
the twice-signed event is stored anywhere.

Sending (`internal/rooms/invite.go`, `internal/federation/invite.go`).
`Service.inviteRemote` builds the member event with the room's own prev events, the auth events the
room's rules select, the depth and origin the room expects, and refuses before asking if the room's
power levels would not allow the invite in the first place. `unsigned.invite_room_state` carries the
state a client needs to recognise the room — create, join rules, name, avatar, canonical alias,
encryption, power levels — plus the member events, because the inviter's own membership is what
authorises the invite and the invited user's previous membership is what an invite may be lifting
(a ban, for instance). `federation.Inviter.SendInvite` asks `PUT /_matrix/federation/v2/invite/
{roomId}/{eventId}` and falls back to v1 only for a room version 1 or 2 room and only when the
answer says the endpoint is unknown: a 403 that gives a reason is a verdict, and asking twice would
only ask the same question twice. The event that comes back is compared with the one that was sent —
type, membership, sender, state key, room — because only signatures may differ, and a peer that
returns a different event is not countersigning this invite. Nothing is stored unless the answer is
right, and a peer's refusal is answered to the client as a gateway failure (`matrixerr` grew a
`Status` override for exactly this: an `M_UNKNOWN` on its own reads as this homeserver's fault, and
the operator would look in the wrong place).

Receiving (`internal/api/federation/invites.go`, v1 and v2). The checks are about what this server
would be promising: an `m.room.member` invite, about a user who lives here, whose sender lives on the
server that sent it, in the room the path names, with the event ID the path names. v2 names the room
version; v1 has nowhere to put it, so a v1 invite for a room this server holds is judged by its own
record and one for a room it does not hold is taken as a version 1 room rather than a guess at a
newer one. The inviting server's signature is verified *before* this server seals the event, because
afterwards a verifier cannot tell whose word is whose.

The room an invite names may be new here. It is created with the version the request states, and the
state that arrived inside the invite is kept as events and current state — no event graph, no forward
extremities, because this server holds no history of that room and pretending otherwise invites
pagination into a room it knows one event of. Without it the invited user could read the invitation
but not answer it: the join or rejection that follows has to be authorisable, and the events it cites
have to be here. A bundled state event is used only when its own origin's signature checks; one that
does not is skipped and logged rather than treated as a verdict, because a peer whose older state this
server cannot re-verify (rotated keys, a redaction whose rules are applied differently) would
otherwise be unable to invite here at all. The invitation itself is signed by the server that holds
the room, and that is the promise worth checking.

Room creation keeps its shape. A room created "with Bob in it", where Bob lives elsewhere, is
created; the invitation is attempted afterwards and its failure is logged rather than returned. An
unreachable Bob should not destroy a room that now exists, and a client that is told the room exists
can retry `/invite`, where the answer is a status it can act on. `tests/integration/rooms_test.go`
now asserts that: the room is created, and no remote invite is stored for a user whose server never
countersigned it.

Complement evidence.
`TestFederationRejectInvite` passes: alice@hs1 invites charlie@hs2, delia (a Complement stub
already in the room) sees the invite arrive as a PDU, charlie rejects it, and the rejection reaches
delia too. That is the whole loop — build, countersign, store, relay — with a third party checking
the relay.
`TestFederationRoomsInvite` and `TestUnbanViaInvite` still fail, on two named gaps that are not
invites: `unsigned.prev_content` is not written on membership changes, and a ban or a kick for a
user who lives elsewhere is still refused with 501.
Full `./tests` + `./tests/csapi` counts are recorded in the next section.

Two local test expectations changed, deliberately, because the behaviour they pinned is gone:
inviting `@someone:elsewhere.example` is now a gateway failure rather than 501, and creating a room
with a remote invitee is now a created room rather than 501.

Complement counts after the invite handshake, for the record: `RUN=. scripts/complement.sh -v`
over Complement's whole `./tests/...` tree — every package, including the MSC-specific ones — is
**67 passing, 152 failing** (`.agent-work/next/full2.log`). The earlier 64/132 figure was a
narrower scope (`./tests` and `./tests/csapi` only), so the totals are not comparable; what is
comparable is that nothing previously green went red, and `TestFederationRejectInvite` joined the
passing set.

## 2026-11-08 — a state event shows what it replaced; a direct message is recorded where the specification puts it (T-4023, T-4024)

Commit `afe9b0b`.

**`unsigned.prev_content` / `unsigned.prev_sender` (T-4023).** The previous content of a state
event is a fact about the moment it was written, so it is computed at persistence
(`persist.eventRepo.recordPrevState`, called from `Insert` for state events): the current
`room_state` row for the same `(room, type, state_key)` is read *before* the caller records the
new state, its content — in the redacted form when a redaction was applied, because that is the
content a client compares against — is stored on the new row with the sender of that event. Two
deliberate omissions: a state event that repeats the content already in place records nothing
("the same content again" has no previous content, and reporting one would show a change that did
not happen), and a state event whose predecessor cannot read records nothing, which leaves it
looking like the first of its kind rather than inventing a history. Presentation is
`redaction.showPrevState`, in the same servicer that adds `unsigned.redacted_because`; the fields
never enter the stored bytes, so the signature still covers what it covered, and they do not
travel to other homeservers — a claim about our own state is not something a peer agreed to.

**`is_direct` (T-4024).** `createRoom` accepted the key and dropped it. Invitations now carry
`is_direct: true` in their content — local ones and the remote ones, where the countersigning
server has no other way to learn the flag — and the creator's `m.direct` account data gains the
room against each invited user, extending the existing mapping because account data is a current
value and overwriting it would lose the user's other conversations. It is not room state: a room
is a direct message for the people it was opened between, not for everyone who may join later.

**The account data endpoints.** `GET`/`PUT /_matrix/client/v3/user/{userID}/account_data/{type}`
were missing entirely: the server stored account data and synced it, but no client could read or
write it, so the `m.direct` mapping the server had just recorded was invisible to the user who
owns it. `internal/users.AccountDataService` and `internal/api/client/accountdata.go`; the
document is kept and returned as the bytes the client sent, because decoding it through this
server's structs would drop keys this server has no opinion about.

Complement evidence from the run after this change (`.agent-work/next/inv5.log`):
`TestWriteMDirectAccountData` passes (newly green), `TestFederationRejectInvite` still passes, and
`TestAddAccountData` now reaches room-scoped account data instead of failing on the user-scoped
half. `TestFederationRoomsInvite` no longer fails on `prev_content` or on the invitation: what is
left in it, and in `TestUnbanViaInvite`, is the remote ban/kick gap (T-4025) — `POST /kick` and
`POST /ban` for a user who lives elsewhere are still refused with 501.

Local tests: `tests/integration/prev_state_test.go` (three topic writes in the order second,
third, second; only per-event recording can report both predecessors, and the room's first topic
reports no predecessor), `tests/integration/direct_test.go` (the flag on the invitation, the
mapping extended rather than replaced, and an ordinary room filed nowhere).

## 2026-11-08 — devices can be signed into again; a transaction ID names one send in one room (T-4030, T-4031)

Commit `27c3341`.

Run of the M2 gate family (`txnid_test.go`, `.agent-work/next/txn2.log`): **4 of 5 pass** —
`TestTxnInEvent`, `TestTxnScopeOnLocalEcho`, `TestTxnIdempotencyScopedToDevice`,
`TestTxnIdempotency`. Only `TestTxnIdWithRefreshToken` remains, and it stops at the first request
because this server refuses `refresh_token: true` (T-4037).

Three separate causes, only two of them predicted by the diagnosis:

1. `POST /login` with a `device_id` the account already had created a *second* device under that
   name, and the unique `(user_id, device_id)` index answered `M_INVALID_PARAM "Device ID is
   already in use"`. A device ID names a device, not a session, so a login that names one the
   account holds now reuses it — display name kept when the client does not offer one, last seen
   moved, a new access token minted, and the sessions that device already had left alone. Device
   names stay per account: another account naming the same device gets a device of its own, which
   `TestLoginIntoAnExistingDeviceReusesIt` now asserts in both directions.
2. A transaction ID was unique per device, so the same name on a send to two rooms answered both
   with one event and lost a message. The key is `(device_id, room_id, txn_id)`; the migration
   drops the old index before creating the new one, because `CREATE UNIQUE INDEX IF NOT EXISTS`
   would otherwise leave a database written before this change constrained by the room-blind index
   forever.
3. `unsigned.transaction_id` was never served. It is now stored on the event row at the moment the
   send persists it and added by the same servicer that adds `prev_content` and
   `redacted_because`: never in the signed bytes, never to a peer homeserver. `event_txn` remains
   the uniqueness guarantee that makes a retry idempotent — the column is the copy a client is
   shown, and the two are deliberately separate.

Correction to the task list, from reading the test instead of its name: MSC4115's
`unsigned.membership` (T-4032) is the **reader's** membership as of each event, not the sender's —
see T-4032 for the evidence. It cannot be a stored column, and it belongs with T-4033's
membership-history lookup.

Local: `tests/integration/devicelogin_test.go` (three tests) and
`TestTransactionIDsAreScopedToDeviceAndRoom` in `tests/unit/eventstore_test.go`;
`go vet ./...` clean, `go test -race -count=1 ./tests/...` green.

## 2026-11-08 — the member roster a client asks for, at the point it asks for it (T-4033, T-4033a)

Commits `7f54a7d`, `f44cbcf`, `c6d42c8`.

Complement, filter `TestGetRoomMembers|TestGetRoomMembersAtPoint|TestGetFilteredRoomMembers`
plus the sync and message families (`.agent-work/next/prevbatch1.log`):

- `TestGetFilteredRoomMembers` — all three subtests pass. `handleMembers` had read only
  `not_membership`, so `membership=join` and `membership=leave` were ignored.
- `TestGetRoomMembersAtPoint` — passes.
- `TestGetRoomMembers`, `TestSendAndFetchMessage`, `TestRoomMessagesLazyLoading`,
  `TestRoomMessagesLazyLoadingLocalUser`, `TestSyncFilter`, `TestSync` (csapi),
  `TestRoomSummaryAllowedRoomIDs` — pass, unchanged from before the change.
- `tests/msc4222` `TestSync` still fails its two `state_after` subtests; it failed the same way in
  the previous gate run, so it is not a regression from anything here.

Two things had to change, and the second only became visible once the first was right:

1. `GET /members?at=<token>` was reading today's state and cutting it by stream position. For the
   case `at` exists for, that has nothing to cut: a member whose join this server has replaced with
   a leave is not in the current state at all. It now reads the state snapshot the newest event at
   or before the token was sent against (`events.EventsWindow` → `groups.GroupOf` →
   `groups.Entries`) — the same snapshot the authorisation rules judged that event by, which is why
   the roster shown to a client cannot drift away from the room's own view of itself at that
   moment. A token whose snapshot was never recorded is refused; nothing had happened by another
   token gives an empty roster. The requester's own horizon still applies.
2. `/sync` was issuing `timeline.prev_batch` as the position of the **oldest** event in the batch,
   so a client asking who was in the room "at the point my batch began" was naming the create event
   and was told nobody — in a room it had just been shown the joins of. It now names the **newest**
   end, which is what Synapse issues and what Complement expects. The Client-Server API does not
   decide this (matrix-org/matrix-spec#595, open since 2020), so the reasoning and the cost — a
   client may meet an event twice paging backwards — are in the comment at
   `internal/sync/service.go:634-646`, not only in a commit message.

`tests/integration/members_filter_test.go` covers the three filters and three points in the
history, including the one the old code could not express: who was in the room while they were
there, asked after they have left.

Open observation, recorded rather than theorised: `SendEventSynced waiting for event ID …` appears
in some runs of `room_members_test.go:74`/`:88` and not others with the same blueprint, so a sent
event sometimes does not reach the sender's own `/sync` in time. That is intermittent, and it is
T-4034.

### In flight, for the next session to read

A full Complement suite run (`RUN='.'`, all packages) is writing to
`.agent-work/next/gate3.log`, started 10:37 UTC on 2026-11-06 with the tree at `608f98d`.
Read it when it ends for the honest tally; do not read the whole file — the useful commands are
`grep -E '^(ok|FAIL)\s+github'` for packages and `grep -E '^--- FAIL'` for tests.

Already visible in it, and worth its own task rather than a footnote: the `msc3902` package hangs
with its homeserver container `Up 14 minutes` and no further log output (the log went quiet at
11:44 local). A Complement test that never returns is a server bug of a particular kind — the test
is waiting for something the server will never send (a sync that never carries an event, a
missing-event fetch that is never answered, or a request handler that never replies). It is worse
than a failure, because a failure reports itself. `msc4242`, `msc4306` and `msc4429` report package
failures, which is expected for unimplemented MSCs and needs separating from hangs when the tally is
taken.

### In flight: the first trustworthy full-suite tally (T-4039)

`RUN='.'` started 12:44 local (11:44 UTC) on 2026-11-06 at tree `4d163ec`, writing to
`.agent-work/next/gate5.log`, with the anonymous-volume store clean (17, not 2044) and the script
at its `-p 2` default. The two earlier tallies (`gate4.log` 59/161, `full2.log` 67/152) are
contaminated and should be replaced by this one in the compliance matrix — read the log with
`grep -E '^(ok|FAIL)\s+github'` for packages and `grep -E '^--- FAIL'` for top-level tests, and
check it did not die of `num_locks` before believing any number:
`grep -c 'exceeded num_locks' gate5.log` should be 0.

## 2026-11-07 (late) — the membership handshakes a room holds and this server does not (T-4074)

Commits `6462091`, `aac2374`, `12be510`, `f18742b`. Four Complement tests were in the failing list
for a handshake this server simply did not serve — `TestCannotSendNonLeaveViaSendLeaveV1/V2`,
`TestCannotSendNonKnockViaSendKnock`, `TestCannotSendKnockViaSendKnockInMSC3787Room` — and the two
big knock tests stopped before their first assertion.

**What was built.** One template in the room service for all three memberships
(`MembershipTemplate(ctx, room, user, membership)`), with `JoinTemplate` now that asked for a join;
the differences are the room's, not the endpoint's — a restricted room's authoriser is chosen only
for a join, because it has to be in the event before the event is named. Inbound
`/make_leave`, `/send_leave` (v1+v2), `/make_knock`, `/send_knock` (v1+v2), and `/v1/send_join`
which gmsl clients fall back to and Complement probes directly; a wrong membership there is
answered **400, not 403**, because the endpoints differ only in what they accept and a 403 tells the
peer to go ask a friendlier resident server about the same room. Outbound: the same two halves run
from this side for a knock or a leave, and `POST /knock/{roomIDOrAlias}` exists so a client can ask
for them. `roomversion`'s refusal rule and the path-vs-event checks are now shared functions rather
than three copies with different words in them.

**Two interoperability facts, recorded where the code does the compromise.** The event field of a
v2 `/invite` body is `invite_event` in the specification and `event` in gomatrixserverlib, which
rejects a body without the latter outright — so lattice writes both names and reads either, and
until this window nothing in this repo tested a peer's invite request at all. The same disagreement
exists for `/make_leave` (`membership_event` vs `event`), and both names go out.

**One thing tried and reverted, with the reason kept next to the code.** Handing a knock's
`knock_room_state` to the bundle ingester cannot work: the stripped state deliberately carries no
`m.room.member` events, and in every room version with knock the writer's member event is one of the
auth events of the state events around it. Measured, not theorised — all seven events in the bundle,
including this server's own signed knock, came back unresolved. The log records the handover; the
missing piece is a knock's own record (an invite-hanging shape for a knock), and that is T-4075.

**Verification.** `tests/integration/federationmembership_test.go`,
`federationinvite_test.go`, `knocksync_test.go`, `knockrestricted_test.go`; `go test ./tests/...`
green. Targeted Complement run `membshk3`
(`.agent-work/next/complement-run-membshk3.log`, table
`.agent-work/next/complement-results-membshk3.tsv`): all six `TestCannotSend*` validation tests and
`TestFederationRejectInvite`/`TestRoomsInvite` green, and `TestKnocking`'s local half went from 3 of
13 subtests to 12 of 13. Its remaining subtest, and the federated half's first failure, are separate
and recorded: T-4076 (what a first sync's `state` section is — current state vs the state at the
start of the returned timeline, which is exactly the ambiguity MSC4222 exists for) and T-4075.
`TestRoomForget` and `TestLeftRoomFixture` are red for a policy this server has not decided —
leaving versus forgetting a room — and that is T-4077; `/forget` answers 404 `M_UNRECOGNIZED` today.

## 2026-11-07 (evening) — two ceilings: 413 `M_TOO_LARGE` and `M_NOT_JSON` (T-4078)

Both came out of the 20261107c failure list rather than out of the plan, and both were the cheapest
kind of red: a server answering **200** where the specification asks for a refusal.

```
invalid_test.go:215: MatchResponse got status 200 want 413 - .../send/m.room.message/1
apidoc_request_encoding_test.go:22: MatchResponse got status 401 want 400 - .../register
                                  => {"errcode":"M_NO_AUTH", ...}
```

**Where the ceiling belongs.** `MAX_PDU_SIZE` is now enforced at the storage funnel — `persistEvent`
measures the canonical form — not at the client door. That placement is the finding, not the number:
the limit is a property of an event, so `/send`, `/state`, a peer's `/send_join`, `/send_leave`,
`/send_knock` and a transaction's bundle ingest must all answer it identically, and a server enforcing
it on one door only will happily store events its peers will refuse. Over the limit is 413
`M_TOO_LARGE` naming both byte counts.

**A decoder that does not fail is a correctness hole, not a tolerance.** Go's `encoding/json`
replaces an invalid UTF-8 byte with U+FFFD and continues, so the request in
`TestRequestEncodingFails` was parsed, went on to the auth question, and answered 401 for a body that
was never valid JSON text. `httpx.DecodeJSON` now rejects a body that is not UTF-8 with 400
`M_NOT_JSON` before the decoder is called. Valid UTF-8 of any width remains ordinary JSON, and the
test says so by setting a display name containing an em dash and Japanese.

**What the test taught.** The first version picked a 72 KB body as "just under the limit" and failed —
the body alone was over it. It now **bisects** for the largest accepted body and asserts that the
boundary lands just under 65,536 bytes, which is the only way to tell a correct ceiling from one that
fires far too early; a test that hand-picks an oversized body proves the refusal exists and nothing
about where it is. It also checks that every event the room kept is inside the boundary: a refused
event is not a happened event.

**Verification.** `tests/integration/eventlimits_test.go`; `go test ./tests/...` green. Targeted
Complement run `20261107d-sizelimit`: **2 passing, 0 failing** top-level — `TestEvent` (including
`Large_Event` and `Large_State_Event`, both of which were the 200s above) and
`TestRequestEncodingFails`. Single targeted run, all packages filtered out except these two; the
full-suite tally remains 20261107c's 118/101/1 until the next full run says otherwise.

**Next cheap win found in the same log, not taken yet:** `TestFilter`'s fourteen
"Expected 4XX status code, got 200 for testing filter map[...]" assertions — this server stores any
JSON as a filter. Opened as T-4079.

## 2026-11-07 (evening) — a filter with a typo in it is refused, not stored (T-4079)

`TestFilter` was fourteen lines in the 20261107c log that all read the same way:

```
invalid_test.go:188: Expected 4XX status code, got 200 for testing filter map[presence:not_an_object]
invalid_test.go:188: Expected 4XX status code, got 200 for testing filter map[room:map[timeline:map[rooms:[not_a_room_id]]]]
```

A filter is the client's document, and this server stored any document and answered 200. The harm is
not the 200; it is that the filter then shaped every later `/sync` of that user's, returning fewer
events than the client meant to ask for, with no error anywhere to explain the missing ones.

**Two doors, not one.** The obvious place was `POST /user/{user}/filter`. The one that is easy to miss
is `/sync?filter={…}`, where the same document arrives inline: a client told its filter is wrong once
and not again has been trained to distrust the answer it did get.

**What is not checked, on purpose.** Field *names* this server has not read are left alone. The filter
object is where MSCs park their switches — a threaded-sync filter carries a namespaced name today — and
refusing an unknown name makes this server the reason a client cannot try a newer spec. Field *shapes*
this server does read must match the specification, including the two that no type checking catches:
`rooms` must hold room IDs and `senders` must hold user IDs, so `[not_a_sender_id]` is a bug at the
door rather than a filter that quietly matches nothing.

**Verification.** `tests/integration/filtervalidation_test.go` (twenty-one bad documents, the good one
read back byte for byte, and the inline door). `go test ./tests/...` green. Targeted Complement run
`20261107e-filters`: **10 passing, 3 failing** over the twelve filter-touching tests in the suite, with
`TestFilter` green and `TestSync` green in both packages that define it (`csapi`, `msc4222`). The three
failures (`TestSearch`, `TestRoomImageRoundtrip`, `TestFilterMessagesByRelType`) are all in the
20261107c failure list as well, so nothing here caused them. Full-suite tally is still 20261107c's
118 / 101 / 1 until the next full run.

**Picked up from the same log, not yet done:** `TestRoomImageRoundtrip` fails on
`MatchResponse key 'end' missing` — this page of history answers `start` and omits `end` when it
believes there is nothing further, where the reference servers always answer a token to continue from.
That is a deliberate behaviour with four assertions in `tests/integration/history_test.go` behind it,
so changing it means changing the local story about how a client knows to stop paginating, and saying
which of the two this project is following.

## 2026-11-07 (evening) — the filter on `/messages` is a filter again (T-4080)

`TestRoomImageRoundtrip` asks one thing of `/messages`:

```
GET /rooms/{room}/messages?filter={"contains_url":true}&dir=b
  → chunk of 1, `start` a string, `end` a string
```

This server returned the room's whole timeline, because `ParseHistoryFilter` read exactly one flag
out of the parameter — `lazy_load_members` — and ignored the rest. `contains_url` was not read
anywhere in the server, not even on the `/sync` door where the filter is defined.

**One document, two doors.** `/sync` nests the filter under `room.timeline` and `room.state`;
`/messages` takes a *room event filter as the whole parameter*. The type lists, sender lists, room
lists and `contains_url` now live in `internal/eventfilter`, used by both doors and by the shape
checks added in T-4079, so a filter cannot come to mean one thing on one door and something else on
the next. Applying it to a page also moved where `end` is measured: once a filter drops events, the
chunk's length is no longer an index into the window that was read, so `end` names the position the
page really stopped at.

**The detour, recorded because it was nearly taken.** The failing assertion was about `end`, so the
first patch made every page answer an `end` token — the reference servers do. That patch is gone, and
the reason is in the specification and in Complement's own words:

> "If no further events are available (either because we have reached the start of the timeline, or
> because the user does not have permission to see any more events), this property is omitted from
> the response."
>
> `// Keep paginating until we start of the room (no end property is returned).`
> — `tests/csapi/room_messages_test.go`, `TestMessagesOverFederation`

Always answering `end` would have traded a federation pagination test for a media one. The real gap
was the filter all along, and the quotation now sits next to the `End` field where the next reader
will form the same hypothesis.

**Verification.** `tests/integration/messagesfilter_test.go`: the four kinds of list and
`contains_url` both ways on `/messages`, a filtered page's `end` followed to an empty page, the same
shape checks answered 400 `M_INVALID_PARAM` at this door, and the flag honoured through a sync filter
with an unfiltered control sync beside it. `go test ./tests/...` green. Targeted Complement run
`20261107g-msgfilter`: **10 passing, 2 failing**, `TestRoomImageRoundtrip` newly green; the two
failures (`TestFilterMessagesByRelType`, `TestMessagesOverFederation`) fail identically in 20261107c.
Full-suite tally is still 20261107c's 118 / 101 / 1 — three targeted runs' worth of fixes are in the
tree, and the next full run is what will re-measure them.

## 2026-11-07 (night) — the three fixes measured at once, and an account that can end (20261107h, T-4081)

**The full run.** `RUN='.*' COMPLEMENT_LABEL=20261107h COMPLEMENT_TEST_TIMEOUT=40m` at tree `6f3390f`:
**121 passing, 98 failing, 1 skipped** — from 118 / 101 / 1. The four newly green are exactly the four
the three targeted runs had claimed: `TestEvent`, `TestRequestEncodingFails`, `TestFilter`,
`TestRoomImageRoundtrip`. Nothing else moved except one test, and that one moved the wrong way:

```
room_upgrade_test.go:193: CSAPI.Must: POST /_matrix/client/v3/join/%21ap5U…:hs1?server_name=hs1
                          returned non-2xx code: 500 Internal Server Error
```

That is `TestPushRuleRoomUpgrade`, green in 20261107c, and the 500 is the defect T-4071 has been
carrying: a retryable federation failure answered 500 `M_UNKNOWN`. It is reported as newly failing,
because a test that fails on a known defect still failed. The environment warnings were there too
(1696 anonymous volumes, the stale `complement_msc3902_1_servers` DNS record) and neither produced a
deploy failure in this log — clearing both remains an operator's call, the commands are in the runner.

**An account can change its password and end itself** (`8f1e884`, T-4081): `POST /account/password`
and `POST /account/deactivate`, both behind a User-Interactive Auth `m.login.password` stage. Two
things in that are worth more than the endpoints:

- a password change ends every *other* session and keeps the one that asked, which needed a
  revoke-except in the token store — revoking all and re-minting would swap the caller's token under
  it and send it to the login door with the password it had just proved;
- deactivation clears the password hash, so login's two checks had to change order: a caller who
  tries a deactivated account's real password needs `M_USER_DEACTIVATED`, not "wrong password".

Targeted run `20261107i-account`: **6 passing, 1 failing** — `TestChangePassword` and
`TestDeactivateAccount` newly green, the login/logout/registration/device families unchanged, and
`TestChangePasswordPushers` still red because `POST /pushers/set` does not exist here at all. That is
T-4082, the next door.

## 2026-11-07 (night) — read receipts, and what a non-event has to do with the stream (T-4083)

`POST /rooms/{room}/receipt/{type}/{eventID}` and `POST /rooms/{room}/read_markers` did not exist,
so `TestRoomReceipts` and `TestRoomReadMarkers` were 404s all the way down. Implementing them looked
like an endpoint and a table; it turned into three sentences about the stream, each bought with a
failure:

1. **A receipt is not an event, but it still has to be synchronisable.** It has no ID, no auth
   rules and no place in the graph — it is a current value about a person, exactly like presence,
   which this server already delivers. So it is stored as one row per (room, kind, user, thread)
   that *moves*, and the move takes a position in the same stream events use. An incremental sync
   then answers "which receipts moved inside your window"; a first sync has no window and gets the
   current answer.
2. **The stream has to know the table exists.** Positions are `MAX(stream_ordering)+1` over a union
   of the tables that participate. `receipts` was not in it, so a receipt sat at a position the
   stream did not know about, `next_batch` fell behind it, and the receipt was filtered out of the
   window that had been asked for. Added to `streamTables`, and the test went from "no receipt" to
   "the receipt".
3. **Two failures that were storage, not protocol.** The repository has to be bound to the
   transaction that takes the position: SQLite waited on itself and the request hung for ten
   seconds, which reads as a deadlock bug in the sync loop and is a missing `WithTx`. And the
   second receipt one user posted answered **500 `M_UNKNOWN`** — an update of an existing row needs
   a key to find it by, and the row had no primary key. `20261107j-receipts` records that 500 with
   its line (`room_receipts_test.go:61`); `20261107k-receipts`, after the fix, is the run that says
   it works.

**Where the two halves live.** `m.read` is the receipt, sent through the same service call the
receipt endpoint uses so the two doors cannot disagree about what a read position means.
`m.fully_read` is the reader's own bookmark: room-scoped account data, delivered in the same sync
under `account_data`, because nothing else in the room needs to know it.

**Verification.** `tests/integration/receipts_test.go` — the ephemeral shape (no `room_id`, no event
ID of its own), a first sync carrying the current position, a quiet window repeating nothing, a
position moved twice leaving one receipt, and the refusals (unknown event and cross-room event 404,
bad receipt kind and bad event ID 400, a room the caller is not in 403, a custom dotted kind
accepted). Targeted run `20261107k-receipts`: **10 passing, 1 failing** — `TestRoomReceipts` and
`TestRoomReadMarkers` green, `TestSync` green in both packages, the `/sync` and `/messages` family
unchanged. The remaining failure, `TestThreadReceiptsInSyncMSC4102`, was failing before this work
too: it receipts inside a thread, and threads do not exist here yet.

**Still honest about:** unread counts, which remain zero. The rows are there now; MSC784's counting
rules and MSC2860's marker are T-3005a.

## 2026-11-07 (later still) — a full run that measured the runtime, not the server (20261107l)

The second full run of the day, started at `df8f6fc` to re-measure everything after the account and
receipt work, ended **3 passing, 217 failing**. It is not a protocol result and is recorded as a
broken run rather than as a tally: 279 lines of `allocating lock for new volume: allocation failed;
exceeded num_locks (2048)`, 63 deployments that never started, and the runner's own warning printed
before the first test — 2038 anonymous volumes in the podman store, up from 1355 at the start of the
day and 594 at the start of the week.

Nothing about the tree is being blamed, and nothing about the 217 is being believed. The measured
position stays **20261107h: 121 / 98 / 1**, with the account endpoints (T-4081, `TestChangePassword`
and `TestDeactivateAccount` green) and the receipts (T-4083, `TestRoomReceipts` and
`TestRoomReadMarkers` green) carrying targeted runs of their own.

What the run *did* establish is that the leak has a doubling time and will eat any run from now on:
one full suite adds roughly 700 volumes, and the limit is 2048 locks. The fix is two shell loops
that delete other people's state, which is exactly why the runner names them instead of running
them; they are in `scripts/complement.sh` and quoted in `docs/compliance-matrix.md`. That is the
next thing a human has to do before another full run means anything (T-4084).

**And one change to the runner, because the failure was foreseeable:** `scripts/complement.sh` now
refuses to start when the anonymous-volume count is past 1300 — naming the count, podman's 2048
lock ceiling, and this run — instead of running 220 tests and reporting the runtime's limits as
failures. Checked against the actual state: with 2045 volumes it exits 1 before deploying anything.
`COMPLEMENT_FORCE=1` keeps the door open for anyone who wants an unfinished result on purpose.
Clearing the store is still an operator's two loops, unchanged, because they delete state that may
belong to other projects.

## 2026-11-07 (later) — a clean volume store, a dirty DNS record (20261107m, 20261107n, T-4085)

The volume store was cleared, and the full suite ran again at tree `24ae2b0`: **117 passing,
102 failing, 1 skipped**, with `exceeded num_locks` appearing zero times. Five tests moved the right
way — `TestChangePassword`, `TestDeactivateAccount` (T-4081), `TestRoomReceipts`,
`TestRoomReadMarkers` (T-4083), and the T-4071 flake `TestPushRuleRoomUpgrade` — and nine two-server
tests moved the wrong way. Nine is a lot, so the failures were chased rather than explained away:

```
32 × federation: hs1 answered the join template with 401
10 × …the knock template with 401          2 × …the leave template with 401
 5 × hs1 answered M_UNAUTHORIZED for media: The request is not signed by a server this server trusts
 8 × certificate is valid for hs2, localhost, not hs1        (20261107h: 2, 0, 0, 0, 0)
```

That is a name-resolution collision, and the file that causes it is still sitting in
`$XDG_RUNTIME_DIR/containers/networks/aardvark-dns/complement_msc3902_1_servers`: the network is gone,
the file remains, and it answers `hs1` with **10.89.0.2, 10.89.1.2 and 10.89.2.2** — exactly the
addresses fresh deployments take once the store has been emptied. Clearing the volumes therefore
*moved* the problem rather than removing it: hs2 asks for hs1, reaches somebody else's hs2, and then
refuses every request the real hs1 signed.

The discriminator that settles it: the same nine tests, alone, at `PARALLEL=1`
(`20261107n-regresscheck`) — **9 passing, 0 failing**.

So the honest sentence in the matrix is that **20261107h's 121 / 98 / 1 remains the last clean
full-suite tally**, that T-4081 and T-4083 are confirmed by this run as well as by their targeted
runs, and that 20261107m's federation half is contaminated. The runner now counts the contamination
lines and prints them beside the tally (2 against 20261107h's log, 51 against 20261107m's), so the
next reader cannot mistake a namespace for a protocol. Clearing the stale record is an operator's
loop, still not run here.


## 2026-11-07 (later still) — a run that reported nothing, and four tests nobody had asked for yet (T-4086, T-4087)

The full suite at tree `c2e0015` ran all 220 tests and printed no tally at all. The cause was not the
server and not even the container runtime: `go test -json` glued two events onto one line, `jq`
stopped reading at it, `set -euo pipefail` stopped the script, and a completed run looked identical
to one that never happened. Re-deriving the results over the same log once the runner could split the
join gives **123 passing, 98 failing, 1 skipped** for that run — four newly green against 20261107h
(the account pair, the receipt pair) and four two-server tests newly red, three of which pass when
run alone (`20261107p-relationscheck`) and one of which does not:
`TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room`, which is T-4071 in the flesh — a peer contacted
at an address, a TLS handshake refused, and this server answering the join with 500 `M_UNKNOWN`.

The fingerprints of the cross-talk are now counted and printed beside the tally, kept apart by what
they mean: 20261107h 0 wrong-name certificates / 1 refused handshake / 0 refusals as unsigned,
20261107m 8/6/7, 20261107o 4/3/4. Clearing the volume store helped; it did not make the namespace
trustworthy, and now the runs say so themselves.

Then the relations work (T-4086), which is four Complement tests this server had never attempted:

```
20261107p-relationscheck   6 passing, 2 failing   (the three /relations tests green at the first attempt)
20261107r-threads          4 passing, 0 failing   (TestRelations, TestRelationsPagination,
                                                   TestRelationsPaginationSync, TestThreadsEndpoint)
```

The relation claim itself stays inside the event's signed content; `event_relations` is only an index
over it, written at the same funnel that stores the event and deleted where a redaction takes the
content. That last part is not decoration: a redacted reply left in the index would be listed under
its root as a reply, to a client that can no longer see it is one.

`/threads` taught something about the difference between a proposal and a protocol. MSC3440's draft
answers with sibling `event`, `latest_event` and `total` fields, so that is what was built, and
Complement refused it: every implementation and every client reads the root *event* with the thread
hanging off it under `unsigned.m.relations.m.thread`. The response was changed to what the ecosystem
converged on; the assertion was left where it was. Ordering is by each thread's newest event rather
than by its root, and `count` is computed under the reader's horizon, because a count that includes
events someone cannot see is a way of telling them those events exist.


## 2026-11-07 (night) — the suite measured clean: 130 passing, 89 failing, 1 skipped

Run `20261107s`, tree `2c31072`, full universe, `PARALLEL=2`. It is the first full run that can say
out loud that its container network did not interfere: zero wrong-name certificates, zero refused
TLS handshakes, zero refusals as unsigned, zero federation templates answered 401, zero deployments
that never started, and every one of its 20,567 log lines readable as one event. Against the last
clean run (20261107h's 121/98/1) it is nine tests better and nothing is newly failing; against
20261107o it is seven better and nothing is newly failing.

Four of the nine are the relations and threads work (T-4086): `TestRelations`, `TestRelationsPagination`,
`TestRelationsPaginationSync`, `TestThreadsEndpoint`. The other five are the account and receipt pairs
carried over from the earlier targeted runs, plus `TestDeviceListsUpdateOverFederationOnRoomJoin` and
the T-4071 flake `TestPushRuleRoomUpgrade`.

And the counters settled an argument rather than confirming one. `TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room`
was red in 20261107h, red with contamination in 20261107o, red when run on its own in 20261107p — and
green here. Four data points, two environments, no pattern. The `500 M_UNKNOWN` we attributed to
T-4071 in those logs is real and worth fixing, but this suite does not reproduce it on demand, and
saying otherwise would be the same mistake as reading 20261107m's nine regressions as regressions.

## Forgetting a room, and the state a departed member is owed (2026-11-08, T-4077)

`TestRoomForget` and `TestLeftRoomFixture` had been red since M4 and were, as T-4077 said, one
question wearing two coats: forgetting is a thing a user does for themselves, leaving is a thing
that happened to them, and the room answers differently.

**What was added**

    internal/persist/forget.go   RoomForget ("room_forgotten"), ForgetRepo: Mark/Clear/Of/ListForUser
    internal/rooms/forget.go     Forget, forgottenRoom, rememberRoomAgain, ErrStillInRoom
    internal/api/client/forget.go  POST /rooms/{roomID}/forget: 400 M_UNKNOWN while still in it
    internal/rooms/reads.go      requireMembership refuses a forgotten room; stateFor for a leaver
    internal/sync/service.go     a forgotten room is skipped unless the window reaches past the mark

The first shape was wrong in a way only a test could catch: I made the marker absolute, so
`TestRoomForget/…/Leave for forgotten room shows up in v2 incremental /sync` failed — a sync asked
from *before* the leaving still has to tell the leaving. The marker is per (user, room) and judged
per window, not absolutely.

**The part that was not about `/forget`.** `TestLeftRoomFixture` wants a member who left to be
owed the room as it was at the leaving: `m.room.name`, a state event that no longer exists, the
roster with herself as `leave`. We were answering with today's state, which showed her changes she
was not party to and hid values she was entitled to. There was already a snapshot to answer from —
every event is persisted with a state group (`event_state_groups` → `state_groups` →
`state_group_entries`) — it was simply not consulted on the read side. `stateFor` walks the
reader's horizon to the leaving event, takes that group's entries, and falls back to cutting
today's state only for a room never persisted that way, saying so in the code.

**The correction that matters for how much to trust a green suite.** Two of our *own* assertions
in `TestFormerMemberReadsStopAtLeaving` pinned the weaker answer: a 404 for state rewritten after
the reader left, and no trace of a later member in her roster. They were green, and they were
describing the old behaviour. Both were corrected to what the room owes, with the reason beside
them, and no Complement assertion was touched.

**Evidence.** `go build ./...`, `go vet ./internal/...` clean; `go test ./internal/...
./tests/integration/` green including `tests/integration/forget_test.go` (a forgotten room cannot
be paginated by the forgetter but still can by a member; re-invitation and rejoining restore it; a
kicked-from room can be forgotten). Complement
`RUN='TestRoomForget|TestLeftRoomFixture|TestRoomLeave|TestPeekingViaPublicRoomsAndThenJoin'`,
PARALLEL=1, at `4dc3628`: **2 passing, 0 failing, 0 skipped** — all six and all five subtests
listed in `docs/compliance-matrix.md` ("Targeted run 20261108u-forget"); the two patterns that
matched no test in this checkout are named there rather than quietly dropped. A full-suite run at
this tree follows below, because nothing else in the tally can be quoted against a tree that
changes.

## Relation filters, and a full run that could not be believed whole (2026-11-08, T-4088, T-4077's run)

**The full suite at the forget tree.** PARALLEL=2, at `4dc3628`:

    == tally: 131 passing, 88 failing, 1 skipped (top-level tests)
          11  a certificate naming another server   12  a TLS handshake refusing what a peer presented

The count went up by one and the failure list by one less than the forget work predicted, so the
lists were diffed against the last clean run (20261107s, 130/89/1, fingerprints 0/0/0) instead of
the numbers being compared: `TestRoomForget` and `TestLeftRoomFixture` went green as the targeted
run had already shown; 129 names are green in both and 87 red in both; one name moved the wrong
way, `TestPushRuleRoomUpgrade`.

**That one is the environment's, and it is provable rather than excused.** Re-run alone
(`20261108x-pushupcheck`, PARALLEL=1) it is still red — and still carrying 4 certificate
mismatches and 4 refused handshakes, which is how the contamination is known not to be a
consequence of running many packages at once. Its two remote-join subtests fail on the peer's
identity:

    federation: a server could not carry a join  destination=hs1
      error=Get "https://10.89.0.2:8448/…/make_join/…":
            x509: certificate is valid for hs2, localhost, not hs1

while its two local subtests pass in the same run. The stale aardvark-dns record
`complement_msc3902_1_servers` was checked after the run and is still there, its network already
deleted. Clearing it is the operator's two commands (in `scripts/complement.sh`); it was not run
here, because that directory belongs to every project on this machine, not just to this room.
Until it is cleared, **any two-server Complement result from this machine is contaminated**, which
also retires T-4071's "flaky in both directions" reading: what looked like flakiness in
`TestRestrictedRoomsRemoteJoinFailOverInMSC3787Room` and now here is the same DNS fault.

**MSC3874's relation lists (T-4088).** `TestFilterMessagesByRelType` had been red in every full
run for one reason: `org.matrix.msc3874.rel_types` was a field this server had never read. It went
green in `20261108y-relfilter` (`1 passing, 0 failing`) after adding both lists to
`internal/eventfilter`, where the filter document lives — so `/messages` and a sync's timeline now
obey the same document the same way.

The interesting part is *where* the decision reads from. `event_relations` exists now, and the
temptation was a join. But a filter asks what the event claims, and the claim is in
`content.m.relates_to.rel_type`; the index is this server's note about it, and a filter that read
the note would answer differently depending on what had been indexed — and differently again after
a redaction. So:

    rel_types      keep only events claiming one of these rel_types
    not_rel_types  drop the events claiming one of these

with three readings written down rather than left to intuition: an event that claims no relation is
never named by `rel_types` and never excluded by `not_rel_types` (not having a relation is not
having a relation that is not named); `m.relates_to` without `rel_type` is a reply fallback and
belongs with the relationless; a list present but empty is the whole answer, as an empty `types`
already was. A string where a list belongs is refused `M_INVALID_PARAM` rather than quietly
matching nothing.

**A mistake of mine, kept in the record.** The new test first failed with "a timeline filtered to
m.thread came back empty", and the easy reading was that sync ignored the new filter. It did not:
I had read `timeline.chunk` where the specification says `timeline.events` — `/messages` uses
`chunk`, sync uses `events`. The debug that found it was two lines; the assumption that saved them
would have shipped a wrong test.

**MSC honesty.** MSC3874 is a proposal, not in the published specification (`rel_types` appears
nowhere in spec.matrix.org v1.11 through v1.19), and its file could not be fetched here —
raw.githubusercontent returned 404 for every plausible filename and the GitHub API refused
unauthenticated search. So the MSC was not read, and the entry says so: what is implemented is
what the Complement test pins plus the cases above, and the proposal's treatment of a literal
`null` inside the list is not implemented and not claimed.

## Pushers: a device saying where to wake it (2026-11-08, T-4082)

`TestChangePasswordPushers` had been red since it first appeared in a run because there was
nothing there to be red about: `/pushers/set` and `/pushers` were 404. Now:

    --- PASS: TestChangePasswordPushers (1.94s)
        --- PASS: …/Pushers created with a different access token are deleted on password change
        --- PASS: …/Pushers created with the same access token are not deleted on password change

in run `20261108z-pushers` (**5 passing, 1 failing, 0 skipped**), where the one failure is
`TestPushRuleRoomUpgrade` again — the two-server test the stale aardvark-dns record has been
failing all day, carrying 4 more certificate mismatches in the same run.

**What a pusher is, decided rather than assumed.** It is not account data. Account data is what a
user wants; a pusher is one app on one device saying "wake me here", and notification content —
including the body of the event that triggered it — gets POSTed to that address. So the row carries
the hash of the token that wrote it, and the places where sessions end take the pushers with them:
a password change drops those of the sessions it ended, logging out drops a session's own, logging
out everywhere drops all of them. Anything else means the next holder of an account inherits an
address somebody else's app set up.

Device-level rather than token-level would have been the lazier rule and would be wrong: with
`logout_devices:false` the sessions survive, so "delete by device" deletes live pushers, and the
`logout_devices:true` case is only correct by accident. The token hash is what `auth` already
holds; `auth.TokenHash(r)` was added so no handler has to carry a raw token around.

**A hand-over I found with a test rather than in prose.** A pusher is replaced under
(pushkey, app_id) — that is the identity the client names it by. Which means: if session B set a
pusher and session A then sets the same one, the row now belongs to A, and A's password change
keeps it while dropping the one B wrote and nobody touched. That is the coherent rule (whoever
last said "wake me here" owns the address), but my first version of the test tripped over it as
though it were a bug. It is pinned explicitly in `tests/integration/pushers_test.go` with the
reason beside it, so nobody rediscovers it as a surprise.

**Refusals say what is wrong.** An email pusher is `M_INVALID_EMAIL_PARAM` — a code the
specification has separately precisely because a client holding an email pusher needs to know
this is not a matter of retrying — and this server grew that code in `internal/matrixerr` rather
than answering `M_INVALID_PARAM` and leaving the client to guess. `data.url` has to be https: a
notification POST carries the event's content, and plain http would put a private message on the
open network for anybody between here and the gateway to read.

**Not claimed.** Delivering to a pusher at all is still T-3005a: there is no push gateway call, no
`GET /notifications`, no rule evaluation producing a notification. The rows and the doors exist;
the thing they exist for does not. Writing that in the same breath as the green test matters,
because "pushers implemented" would read as if notifications worked.

## Everything added today, in one run that could be believed (2026-11-08, run 20261108aa-todayset)

`RUN` naming the fourteen Complement tests that cover what this stretch of work added, PARALLEL=1:

    == tally: 14 passing, 0 failing, 0 skipped (top-level tests)
    == no federation fingerprints in this run

Relations, thread lists, `/forget`, the departed member's state, MSC3874's relation filters,
pushers and their expiry, plus the neighbours that could plausibly have been broken by any of it —
`TestSync`, `TestRoomReceipts`, `TestRoomReadMarkers`, `TestLogout`, `TestChangePassword`. All
green, none of them touched to get there.

Two things about the runner came out of doing this honestly:

* **A clean run used to be quiet about being clean.** The fingerprint block only printed when
  there was something to warn about, so "no section" and "no problem" looked the same in the log.
  It now prints `== no federation fingerprints in this run …` when there is nothing to report.
  The counters were replayed over three stored logs to check the change did not move them:
  20261108w 11/12, 20261108x-pushupcheck 4/4, 20261108aa 0 — the same numbers the runs printed.
* **The stale-DNS check now runs after the suite as well as before it** (previous section), and
  the first use shows it: 20261108aa names `complement_msc3902_1_servers` at both ends of the run.
  It has been reported to the operator twice now and remains unfixed, because clearing it deletes
  state that belongs to whatever else is running on this machine.

**Where the tally stands.** The last uncontaminated full run remains 20261107s at `2c31072`:
**130 passing, 89 failing, 1 skipped**. The full run since then (20261108w) printed 131/88/1 but
carried 11 certificates naming another server and 12 refused handshakes, so its two-server half is
not evidence — and its one regression is `TestPushRuleRoomUpgrade`, which is red under those
conditions and green when the network cooperates. A full run worth believing is owed once the
stale records are cleared: the four new green tests above (`TestRoomForget`,
`TestLeftRoomFixture`, `TestFilterMessagesByRelType`, `TestChangePasswordPushers`) and whatever
the pusher and filter work changed elsewhere are not yet reflected in any clean full number.


## Reading events through the moment they were written (2026-11-08, `c915aac`, runs 20261108cc/dd/ee/ff)

Four Complement tests had been red for the whole life of this branch for the same reason: the
history-visibility rule existed in this server only where it hands an event to **another server**.
Every door a client reads through — `/event`, `/messages`, `/relations`, `/threads`, `/sync` —
handed out whatever the room contained, and a `joined` room's entire past was on the wire for
somebody who had joined five seconds ago.

The rule is now one package, `internal/historyvis`, and every one of those doors asks it. The part
worth keeping in the code as well as in the commit message is *when the question is about*. The
specification (CS v1.11, Room Events) says:

> These options are applied at the point an event is sent. Checks are performed with the state of
> the `m.room.history_visibility` event when the event in question is added to the DAG. This means
> clients cannot retrospectively choose to show or hide history to new users if the setting at that
> time was more restrictive.

So the answer is not "what does this room allow today" — it is "what did this room allow when this
event was written, and where was this reader in the room at that moment". Both halves come out of
the state snapshot the event was persisted with, which the state tables already keep; the storage
decision that made state resolution cheap is what makes this a lookup rather than a replay. The
test room changes its setting in the middle and pins both directions at once: the stretch written
while it was `shared` stays readable, the stretch written after it became `joined` is 404, and the
stretch after the reader joined is readable again.

Two things the rule got wrong in ways only our own tests caught:

* **A reader's own membership belongs to them.** Filtering the sync timeline hid the member's own
  join event from that member, and their own leave from a left member. The fix is in the rule —
  a `m.room.member` event whose state key *is* the reader is always readable by that reader — and
  `TestSyncLeftRoomStateFollowsHistoryVisibility` broke first and was right to. Not the test: the
  rule.
* **The left room's state section was being emptied for `shared` rooms.** `stateVisibleToOutsider`
  said "unless `world_readable`, hide the state", and `shared` — the protocol's default — is not
  `world_readable`, so a departed member got a room they could not name. Now `shared` and an absent
  visibility event show the state as it stood at the leaving, and `world_readable` shows what has
  happened since. `TestArchivedRoomsHistory` went from red in `cc` and `dd` to green in `ee`/`ff`
  off that fix.

Two doors answering the same refusal differently is deliberate and recorded in `docs/requirements.md`
(REQ-VIS-003): `/event` 404 `M_NOT_FOUND`, `/messages` 403 `M_FORBIDDEN` for a non-member, because
the specification lists no 403 for `/event` and answering 403 there tells the asker that an event
exists.

**What is not claimed.** For a *left* room the state section is still gated once for the room
instead of once per event, so a `joined` or `invited` room hides from a former member state it wrote
while they were a member — which the specification says they may see. T-4090, with the reason it is
left open (it errs towards hiding, and re-running that evidence properly is part of the fix, not
something to re-assert). MSC3765's `knock` visibility value is not implemented; a value outside the
pinned four is treated as `shared` and that is labelled in the code as a decision about malformed
input, not as a reading of the spec.

**The evidence, and a change to how evidence is kept.** `go build`, `go vet` clean and
`go test ./internal/... ./tests/...` exit 0 at `c915aac`. Complement, PARALLEL=1, counted from the
stored `.tsv` tables: `cc-histvis2` 8/3, `dd-readpaths` 24/2, `ee-histvisdone` **21/0/0**, and
`ff-histvisverify` **21/0/0** — the same filter re-run at the same tree with an identical pass list.
The two failures in the earlier runs were `TestArchivedRoomsHistory` (real, fixed) and
`TestMessagesOverFederation` (two-server, and that log carries 6 wrong-name certificates and 4
refused handshakes: the stale `complement_msc3902_1_servers` aardvark-dns record, reported a third
time, still standing, still making two-server results from this machine unusable).

The `ee`/`ff` number is small and deliberately so — it is the leave/visibility neighbourhood, not
the suite. What it does mean is four Complement tests newly green: `TestFetchHistoricalJoinedEventDenied`,
`TestFetchHistoricalInvitedEventFromBeforeInvite`, `TestFetchEventNonWorldReadable`,
`TestArchivedRoomsHistory`.

One housekeeping change fell out of writing this section: the runner's `== tally:` line was printed
to a terminal while the stored log was the raw `go test -json` stream, so no targeted tally quoted
anywhere in these documents had a file behind it. `scripts/complement.sh` now writes
`complement-summary-<label>.txt` beside the log — label, tree, filter, parallelism, timeout, exit
code, tally, fingerprint counts — derived by the same commands from the same log, with
`20261108ff-histvisverify` as its first output (T-4091). The last uncontaminated full run remains
**20261107s: 130 passing, 89 failing, 1 skipped**, and it does not contain any of the last five
days' newly green tests; a full serialised run after the stale DNS records are cleared is still the
thing that would move that number honestly.

## Who is typing: the third value that is about a person, not a room (2026-11-08, `41f78a0`, runs 20261108gg/hh)

`TestTyping` and `TestLeakyTyping` had been red since the first run this branch ever made, and the
reason was in a comment: the sync service's `roomUpdate.ephemeral` field said "read receipts today,
typing when EDUs carry anything". There was nowhere to put a typing indicator, so there was no
indicator.

`PUT /rooms/{roomID}/typing/{userID}`, and the indicator arriving in the room's ephemeral section as
`m.typing`. Presence, read receipts, this: three things this server stores about a person rather than
about a room, and typing is the one that comes with its own expiry. The client says "I am typing for
N milliseconds"; nobody says anything when those milliseconds pass. Two consequences shaped the code,
and both are easy to get wrong in a way that only shows up on someone else's screen:

* **Every change takes a position in the stream every synchronisable value uses.** `typing` joined
  `persist.streamTables`, which is the one place a table declares that a sync token speaks of it.
  Forget that and a client holding a token is told "nothing changed" about a hand that went up.
* **An indicator going stale is a change, and it is recorded as one.** The sweep in
  `typing.Service.Sweep` turns an expired row into a fresh stream position, so the room's members are
  told the list is now empty instead of being left with a hand raised forever. It runs where the
  indicator is read, not on a timer — this server has no background loop for it, and a reader asking
  "who is typing here" is the natural moment to notice the answer has gone stale.

That second one is why the store's rows are **emptied rather than deleted**, the same reason
MSC3391's account-data removal is a tombstone write: a deleted row leaves no position behind, and a
stream whose maximum can drop hands a client a `next_batch` older than the token it already holds.
The table therefore holds one small row per (room, user) who has ever typed.

The refusals follow decisions this server already made elsewhere rather than inventing new ones: a
path naming another user is 403 (the presence endpoint already says "a user may only set their own
presence"), a non-member's typing is not that room's to show (403), and a body with no `typing` key
is 400 — `typing` is decoded as a pointer because the specification makes it required, and
`"typing": false` is a different request from a body that does not say. The five-minute ceiling on a
client's timeout is **ours**, written as such next to the constant: the specification sets no
ceiling, and an unbounded one is a stale indicator outliving a crashed client.

**One test failed first, and it was the test's fault.** `TestATypingIndicatorReachesTheOtherMembersSync`
asserted that a later sync would not repeat an unchanged typing list, while asking with the token
from *before* the stop — a window that really does contain the change, so re-delivering the same
empty list is right. The window was moved past the change; the rule did not move.

**Evidence.** `tests/integration/typing_test.go`: five tests — the indicator arriving as `m.typing`
with no `room_id` and no event ID, expiry reported as an empty list once the client's own timeout
passes, nothing shown to a user outside the room, the four refusals, and the token arithmetic (a
typing write moves the stream; the window after it does not repeat the list). All five pass;
`go build`, `go vet`, `go test ./internal/... ./tests/...` exit 0. Complement, PARALLEL=1,
`20261108hh-typingcommit` at the clean tree `41f78a0`: **8 passing, 0 failing, 0 skipped**, pass list
`TestTyping`, `TestLeakyTyping`, `TestSync` (csapi and msc4222), `TestSyncFilter`, `TestRoomReceipts`,
`TestRoomReadMarkers`, `TestSendAndFetchMessage`; failure list empty; fingerprints 0/0/0/0/0. The run
before the commit (`20261108gg-typing`) was the same eight names — and that is how the runner's new
summary file earned its keep: `gg`'s summary said `tree: 089248a`, which was the commit *before* the
typing code existed, because the image had been built from a dirty tree. The summary now says when
the tree does not match its commit.

**Not claimed.** Typing over federation is T-4093: `TestRemoteTyping` is not green, `EnqueueEDU` still
has exactly one caller (device-list changes), and the inbound transaction handler still acts only on
`m.device_list_update`. Read receipts over federation are the same shape of task and are also not
done, with no Complement test to make the gap visible.

## A state event that changes nothing is not an event (2026-11-08, `345f110`, run 20261108ii)

`TestRoomCreationReportsEventsToMyself` has four behaviours inside it and two of them had been red
since the first run this branch ever made: sending the same state twice must answer with the *same*
event ID, and joining a room you are already in must not put a second member event in it. They read
as two endpoint quirks, which is presumably why they stayed red: a "retry idempotency" fix here, a
"don't re-join" special case there. They are one fact. State is a map — `(type, state_key)` → the
event that last spoke for it — so a write that repeats what the slot already says, by the same
sender, with the same content, is not a new fact about the room.

So `send()` asks, after authorising and before naming the event, whether the event would change the
room; if not it stores nothing and answers with the ID of the event already in the slot. Membership
comes along for free, because the membership endpoints are state writes like any other. Nothing else
had to change, which is the part of this worth trusting: no test was edited, and the whole go suite
was green the first time it was run against the rule.

What counts as no change is the whole of the care. The comparison is of **what would be written**,
not of what was asked for — by that point the content carries the authoriser this server chose for a
restricted join, the display name it copied from the profile, the reason a client typed — and three
answers of "unchanged" are wrong: over a redaction (the content coming back *is* a change), from a
different sender (a different statement, and `prev_sender` plus the authorisation rules care who said
it), and for an entry that is an outlier or soft-failed. Authorisation is asked **first**, so a member
who has lost the power to set a room's topic is refused whether or not the topic already says what
they are typing; `TestRepeatingAStateWriteYouHaveLostTheRightToMakeIsRefused` pins that ordering
because the other order would answer 200 for a pointless forbidden write and 403 for the same write
with one different word.

Two behaviours the rule must not swallow are pinned next to the ones it fixes: two identical
*messages* stay two events (a message is not a statement about a map slot), and leave-then-rejoin
writes a new member event (the slot said `leave`, so `join` is a change).

**Evidence.** Complement `20261108ii-idempotentstate` at `345f110`, PARALLEL=1: **11 passing,
2 failing, 0 skipped**, fingerprints 0/0/0/0/0 (stored summary carries the tree, the exit code and
the same tally). Newly green: `TestRoomCreationReportsEventsToMyself`. The two failures —
`TestRoomSpecificUsernameAtJoin`, `TestRoomSpecificUsernameChange` — were red in the last full run
too and need `POST /user_directory/search`; recorded as T-4095 rather than left to look like fallout
from this change.

**One loose end in the documents, fixed on the way.** DEC numbers are cited by `docs/requirements.md`
and the compliance matrix, but the decision log itself lived only in `.agent-work/`, which is now out
of the repository — so a clone was sent to read a file that is not there. The decisions the tracked
documents cite (DEC-031, 034–037, 040–047) are now a section of `docs/requirements.md` itself, and
this decision is DEC-047 there.

## The measuring instrument was the bottleneck, so it got fixed first (2026-11-08, `0b5eb07`, runs 20261108kk/ll/mm)

The honest summary of the last four days is a number that is uncomfortable to write: of 157 commits,
**82 touched Go code and 75 touched none of it**, and 44 of the 181 task entries in `TASKS.md` are
about the runner, the logs and the recording rather than about Matrix. That is not laziness — the
evidence discipline in AGENTS.md is the reason this branch's numbers can be checked at all — but it
does say where the hours went, and the four days' worth of Complement runs (50 of them) say the rest:
every one of those runs paid for an image it did not need to rebuild from scratch, and four of them
produced a number that had to be thrown away.

So this entry is about the instrument, and about one protocol feature built while the instrument was
busy.

**The image (T-4096).** `scripts/complement.sh` compiled the module inside the build. That is tidy for
a release and wrong for an inner loop: the layer cache cannot survive a source change, so every run
paid ~45s of wall clock and ~3min of CPU (measured twice: 47.9s/2m50s, 42.4s/2m49s) for a binary the
host builds in ~2s. The compile moved to the host in `scripts/complement-image.sh`, and
`deploy/complement/Dockerfile.prebuilt` only assembles: **8.7s** for compile, image and provenance
check. The reproducible path stayed (`BUILD_MODE=container`, `make docker-container`), because an
image nobody can build from the repository alone would make a green run meaningless.

**Provenance, because a tree in a summary was a lie waiting to happen.** Run 20261108gg printed the
commit *before* the code it claimed to test; the image had been built from a dirty tree and nothing
knew. Now the tree identity is written into the image at `/REVISION`, the image is tagged with the
revision, and the script asks the *image* what it contains — non-zero exit if the answer is not the
tree it was pointed at. The feature paid for itself the same hour: the full baseline below could say
`IMAGE_TREE=clean` for what was under test while its own working tree had gone dirty mid-run, which is
exactly the distinction the old single `tree:` line blurred.

**The volume wall, at its root instead of at its symptoms.** T-4084 cleared podman's anonymous volumes
by hand because the loop reaches other projects' state. The `VOLUME ["/data"]` line in the Complement
image is what *made* them: one per homeserver container, ~700 a suite, lock table at 2048, and
20261107l's "3 passing, 217 failing" measuring the runtime. Take the line away and there is nothing to
clean — a smoke container reports 0 mounts, and the serial full run in progress has created **0
volumes** where its predecessor would have created hundreds by now. The documented loop survives as
`CLEAN=volumes`.

**The DNS wall, in the part that is safe to automate.** aardvark-dns keeps answering names for a
deleted network out of the file that network left behind, which is how a homeserver asking where `hs1`
is reaches a stranger's `hs2` and every signature afterwards looks forged. T-4085 counted that; it is
now *cleared* before each run, scoped to the `complement_*` names Complement gives its own networks
and only where `podman network exists` confirms the network is gone. First run removed
`complement_msc3902_1_servers`, the specific file behind several runs of that failure mode.

**A tally is now a set difference, not an impression.** `BASELINE` names a qualified
(package, test) table and the runner prints newly passing / newly failing / unchanged / did-not-run,
with `UPDATE_BASELINE=1` to move the reference. The delta did the work immediately: the user-directory
run reported "2 newly passing, 0 newly failing" without anyone comparing two lists by eye.

**Two full runs, and neither number is quotable yet (T-4097).** PARALLEL=2 on a clean tree:
**141 passing, 78 failing, 1 skipped** of 220 — up from the 130/89/1 the README still carries, which
is what `/forget`, MSC3874's relation filters, pushers, history visibility, typing and idempotent
state are worth. It is a floor and the run says why: **11 certificates naming another server, 12
refused handshakes**. Understanding why also corrected something I had recommended earlier: sharding
the suite across processes would not have helped, because Complement names a network after the
*package* (`internal/docker/builder.go:458`), so tests within a package already share `hs1`, and podman
reuses address ranges across a run's own live and deleted networks. Clearing records before a run
cannot fix records the run creates — this one started clean and finished with `complement_msc3902_1_
servers` on disk again. Serialisation is the knob that demonstrably works (every PARALLEL=1 run on
record counted 0 fingerprints), so `20261108mm-serial` is running the suite that way now and will
become the baseline the documents are generated from. Those documents currently disagree with each
other about one server — README 130/89/1, `BLOCKERS.md` "remains 121/98/1", this run 141/78/1 — which
is the thing worth ending first.

## Whom a person may be told about, and under which names (2026-11-08, `32793af`, run 20261108ll)

`POST /_matrix/client/v3/user_directory/search` — T-4095, the two Complement tests
`TestRoomSpecificUsernameAtJoin` and `TestRoomSpecificUsernameChange`, red since this branch's first
run and red for nothing else.

The endpoint is four lines of HTTP around one rule, and the rule is the reason it is not a user list.
A display name in Matrix lives in a room's member event as well as in a profile, so Alice can be
"Alice Cooper" to the world and "Freddy" to one private room. Those are two facts; the second belongs
to that room; and a directory that indexes both and matches both tells a stranger what Alice revealed
to one person. That is the bug the two Complement tests were written to reproduce (synapse#5677), and
it is why "find Alice" and "find the person who calls themselves Freddy in that room" are different
questions.

`internal/userdir` answers them per searcher, over the three sets the specification makes mandatory —
users sharing a room with the searcher, users joined to rooms whose join rule is `public`, users joined
to rooms whose history visibility is `world_readable`. A name written in a room counts towards a match
only for a searcher in that room; the profile name and the user ID count for anybody allowed to see
the person at all; and the name in the answer is always the profile one, because the specification's
response asks for the display name *of the user*, not for whichever name the match landed on. The
searcher is excluded — Complement needs it, because the term it searches "by MXID" is `@user`, a
prefix shared by all three of its test users, and the assertion is exactly one result. Deactivated
accounts are not findable: their member events stay, because those are history, but a closed account
is not a person to put in front of somebody.

Three decisions the task note left open, settled here. Room-specific names weigh *below* the profile
name, which weighs below the user ID, with whole-word beats prefix, then (as the spec says) whether
profile information exists, then the user ID — so an unchanged server answers twice in the same order.
The directory is **computed** from room state rather than maintained: room state is the one answer
this server gives to "who is in which room", and a table beside it would be a second answer to keep in
step with every join, leave, kick, profile change and room this server has never heard of; the cost is
state reads proportional to the server's open rooms, recorded as a ceiling (T-4096b) instead of hidden.
And terms match on **words** — lower-cased, split on anything that is not a letter or digit, every word
of the term having to land on one field — which is what turns `@user` into the word `user`, finds
"Coop" inside "Alice Cooper", and finds "@user-1-alice" by the part of it Complement can type.

Two honest gaps, said rather than skipped: no `Accept-Language` collation (spec says "preferably"; it
would be a dependency for accents that sort differently in two languages, not for whether a search
finds a person), and `*` is not a wildcard — the specification does not define one and Synapse reads it
as "everybody", so here it is a term with no word in it and finds nobody.

**A test found the bug first.** `JoinedRoomIDs` filtered `state_key = ?` without selecting
`state_key`, so the guard against an event about nobody rejected every row and the directory answered
"nobody is in any room". That is a directory empty in production and invisible to any test that did not
go through the database, and the row that caught it is in the same commit as the code it caught.

**Evidence.** `make verify` (fmt-check, `go vet`, `go test -race ./tests/...`) exit 0 —
tests/integration 25.8s, tests/unit 5.5s. 20 unit rows over the service + 3 over the reads against
real storage (an invited, a left and a knocked member carry no name; a room whose join rule was never
written is not open) + 13 HTTP tests in three groups. Complement `20261108ll-userdir` at the clean tree
`32793af`, PARALLEL=1, on the same twelve names as `20261108jj-idempotentclean`: **13 passing, 0
failing, 0 skipped**, fingerprints 0/0/0/0/0, its own delta against the baseline **2 newly passing,
0 newly failing**.

## `/context`, and what a per-package tally says about where the work actually is (2026-11-08, `52e3d4a`)

**The endpoint.** `GET /_matrix/client/v1/rooms/{roomID}/context/{eventID}` — T-2015. It is built
*from* `/messages` rather than beside it: the two reads must agree about what a requester may see,
about which events a withheld redaction has emptied, and about what a pagination token means, and a
second implementation of those three rules would drift into the state where a client is told one
thing about an event by `/context` and another by `/messages` a second later. One test asserts the
non-disagreement directly.

The tokens are the interesting part and they are anchored on the window that was actually returned:
`start` at the oldest `events_before` event, `end` just past the newest `events_after` one, so
paginating continues instead of repeating. When a side is empty — the end of a room, or `limit: 0` —
the token falls back to the requested event's own position, which is what makes `limit: 0` return two
usable tokens; that is not a nicety, it is how Complement uses this endpoint (jump to an event, take
a token, go read with `/messages`).

**Two bugs, and they are the same bug wearing different hats.** `limit` applies to the *sum* of the
two sides, so `limit: 1` splits into 0 before and 1 after — and 0 handed to `History`, whose
documented reading of zero is "the caller did not say, use ten", answered a window of eleven. Then
the read inherited `Service.Event`'s deliberate folding of "you may not see this" into 404, which is
right for `/event/{eventID}` (it declines to confirm that an event ID exists) and wrong for an
endpoint that addresses a *room* and owes a non-member the 403 that `/messages` gives. Both were
caught by the new tests, neither reached a commit. The general shape — *a helpful default where the
caller had been specific* — is worth recognising in review; `limit` is a pointer now, because absent
(the default of ten), zero (an explicit empty window) and a number are three requests, not two.

**And the measurement tool that changes the plan.** `scripts/complement-tally.sh` regenerates the
tallies the documents quote from a run's own results table, which is aimed at the fact that three
documents quoted one server three different ways (README 130/89/1, `BLOCKERS.md` "remains 121/98/1",
run 20261108kk 141/78/1). Run against the 20261108kk table with `--by-package`, it also produced the
thing the task list has been missing, which is *where* the 78 failures live:

| package | pass | fail |
| --- | --- | --- |
| `csapi` | **93** | **13** |
| `tests` (federation and room semantics) | **43** | **47** |
| the 15 `msc*` packages | 5 | 18 |

Read that against the open task list and the shape of the remaining work changes. The Client-Server
API is not the frontier — it is 93 of 106 green, and the residue is `/search`, guests, to-device and
cross-signing. The frontier is `tests`, where 47 failures sit almost entirely in federation: the
leave/knock handshake, EDUs that do nothing, `/backfill` unanswered, room version 12 and the MSCs
built on it. That is T-4009c, T-4008, T-4009b2 and T-4019, and it is why they are the next tasks
rather than another client endpoint. It is also the honest answer to "how long until done": the
remaining work is not a pile of small endpoints, it is the distributed half of the protocol, and it
should be budgeted like that.

**Evidence.** `make verify` exit 0 (tests/integration 23.4s, tests/unit 5.3s).
`tests/integration/context_test.go`: 12 tests, two groups — the window (centre event, ordering of
each side, state present, tokens continuing in both directions, the sum-limit, `limit: 0` with both
tokens exercised through `/messages`, filter applied to the window and never to the requested event)
and the refusals (403 non-member, 404 unknown event, 403 unknown room, 400 bad event ID or negative
limit, 401 without a token).

## Session close: a baseline worth diffing, two endpoints landed, and the next lever named (2026-11-08)

Four commits, and the useful output is not the endpoints — it is that the project now has a number it
can diff against.

| commit | what |
| --- | --- |
| `0b5eb07` | the image under test declares its own source (`/REVISION`), builds in 8.7s instead of ~45s, and no longer mounts `/data` as a volume — the anonymous-volume leak that faked 217 failures in 20261107l was a line in the Dockerfile |
| `32793af` | the user directory (T-4095): `POST /user_directory/search`, computed from room state with no table to fall out of step |
| `52e3d4a` | `/context` (T-2015): the window around an event, built from `/messages` so the two cannot disagree |
| `d924b11` | full serial run 20261108mm as the stored baseline; `scripts/complement-tally.sh`; three documents forced to one number |

**The number.** 143 passing, 76 failing, 1 skipped of 220, `PARALLEL=1`, image built clean at
`32793af`, fingerprints `cert-naming-another-server=0 tls-handshake-refused=0 unsigned-peer=0`.
Stored at `.agent-work/next/complement-baseline.tsv`; every future run prints its delta against it.
Not in this image: `/context`, which landed mid-run — so 143 does not include it, and any test that
would exercise it is still red here.

**What the measurement retired.** My own earlier reading, in this record twice now. `20261108kk`
(PARALLEL=2, one commit earlier) carried 23 contaminated log lines, and I wrote that its 141 was
therefore a floor. Diff the two runs and they differ by exactly the two user-directory tests — the
whole difference between their images. Contamination in a log is not automatically contamination of a
tally: 20261107l really did fake 217 failures, this faked none. Read the fingerprint line, then ask
whether the contamination could have moved the specific test it surfaced in. `BLOCKERS.md` risk 1 says
this now, so the next session does not re-derive it or re-inflate the estimate because of it.

**Where the remaining work is, and this is the part that changes the plan.** `tests/csapi` is
95 pass / 11 fail. `tests` is **43 / 47**. The Client-Server API is not the unfinished half of this
project; federation and room semantics are. Ranked by how many tests hang off one task, from
`complement-results-20261108mm-serial.tsv`:

1. **T-4019 — room version 12 and state resolution v2.1: ~17 tests.** MSC4289 (6), MSC4291 (5),
   MSC4297 (2), MSC4311 (2), `TestComplementCanCreateValidV12Rooms`, `TestMSC3757OwnedState`. Nothing
   else in the suite is worth this much at once, and `TestComplementCanCreateValidV12Rooms` failing
   means the room version this ecosystem is moving to cannot be created here at all.
2. **F-011 + MSC4242 — the on-rejection outcome that is not stored: 4 tests** (`…RejectedStateDAGEvents`,
   `…RejectionCascadesOnRejoin`, `…RejoinMergesOldAndNewStateDAGBranches`, `JoinLeaveRejoinPublicRoom`).
   This is the architectural hole BLOCKERS.md has carried as "blocks federation correctness", and
   doing it before more handshake work is cheaper than doing it after.
3. **T-4009c — the leave/knock handshake: 3 tests** (`TestKnocking`, `TestKnockingInMSC3787Room`,
   `TestBannedUserCannotSendJoin` is adjacent) and the device-list EDU blocker for
   `TestDeviceListUpdates`.
4. **T-4008 / T-4064 — EDUs that arrive and do nothing** (typing federation, receipts federation,
   `TestACLsForEDUs`).

Do not start T-4019 in a hurry: it is one task with four MSCs inside it, and MSC4291 changes how room
identifiers are derived, which reaches event IDs, joins and upgrades. Split it in the plan before
writing code — my suggestion is v12 room creation + SR v2.1 (MSC4297) first because it is self-contained
and unlocks `TestComplementCanCreateValidV12Rooms`, then MSC4311 (full events instead of stripped state,
contained in the join reply), then MSC4289, then MSC4291 last with its own design note.

**One probe before closing, and it redirected the top task.** I ran `TestComplementCanCreateValidV12Rooms`
alone (run 20261108v12probe, 8.7s image build, ~4s test) to make sure the next session starts from the
real reason rather than an assumed one. The assumption was wrong: it is not "cannot create a v12 room".
hs2 creates the version-12 room, hs1 joins it, hs2 answers `/make_join` and `/send_join` and **accepts the
join**, and hs1 then fails on its own side — `this server could not store it: room "!lLE3fSa…" is not
known to this server` — reported to the client as a 500 `M_UNKNOWN`. The thing to fix first is the join
store path for a room whose create event arrives only inside the `/send_join` state, which may have nothing
to do with room version 12 at all. Recorded on T-4019 with the split order (v12+MSC4297, MSC4311, MSC4289,
MSC4291 last).

**Standing conditions, unchanged and still true.** Complement is the arbiter and its assertions are
not edited; every claim here has a stored run behind it; `.agent-work/` stays out of the repository;
`PARALLEL=1` for a quotable full run; `make verify` before every commit.

## Room version 12: the room that could not be adopted, and the rules that own a room (`a995368`)

**The probe was worth more than the guess.** The note I left for this session said the top task was
room version 12 and that `TestComplementCanCreateValidV12Rooms` failed while *storing* a join. The
inference sitting behind that note was "probably state resolution, or the v12 event format". Both
were wrong, and the run that took about twenty seconds said so: the bundle hs1 was handed contains
`m.room.power_levels`, `m.room.join_rules`, two `m.room.member` events and — for the version-12 room —
**no `m.room.create` event**, because `AcceptRemoteBundle` refuses every event that carries no
`room_id`, and in room version 12 the create event legitimately has none. The specification says it
plainly in the PDU table: `room_id` is Required, "**Omitted from `m.room.create` events**". So the one
event whose job is to introduce a room was discarded for introducing itself correctly, and every
later event in that bundle then failed with the sentence this code emits when a room does not exist:
*room … is not known to this server*.

The lesson is the same one the measurement work taught, one level down: an error message describing
the *symptom* of a missing thing is not a description of the missing thing. hs1 was not failing to
store a join. It had never learned the room existed.

**What landed** is the authorisation half of MSC4289 and MSC4291, gated on the two room-version flags
(`DomainlessRoomIDs`, `PrivilegedCreators`) that the registry has carried since the rules were
sketched and nothing had ever read. Reading them is the interesting part of this commit, because
three of the rules contradict rules that work today:

- `Check` refused any event with no `room_id` **before any rule could read it**, which in version 12
  is the create event — so a version 12 room could not be authorised in either direction, not merely
  not created.
- Rule 2.4 requires the create event among an event's auth events. Room version 12 *forbids* it,
  twice: the selection algorithm leaves it out, and rule 3.2 refuses an event whose auth events do
  not match the selection. Fixing `SelectKeys` without inverting 2.4 gives a server that refuses every
  version 12 event for citing an event it is forbidden to cite — the test that caught that was the
  second one written, which is a margin I would rather not have needed.
- A room creator's power level is infinite and not the room's to take away, so it is asserted when
  power levels are loaded rather than read from the power levels event: rule 10.4 refuses such an
  event naming a creator, but this server also has to survive a room whose event came from a server
  that did not. A creator quietly reduced to zero is an owner who can do nothing in their own room,
  with no error anywhere to say so. And rule 10.4 is checked *before* "no previous power levels
  event, allow", because the specification orders it there and the difference is real — the first
  power levels event of a room is otherwise unconditional, and in version 12 it still may not list
  the room's creator.

**Why the order of the remaining pieces is a safety property, not a preference.** `internal/state`
refuses `StateResV2_1` by name, and that refusal is currently the only thing keeping version 12 rooms
out of a server whose version 12 authorisation rules were incomplete until this commit. Implementing
SRv2.1 first would have opened the door to rooms resolved with a correct algorithm and authorised with
rules missing the clauses above — which is how a server produces a confidently wrong room, the one
failure mode this project cannot detect after the fact. So: T-4099 (derive and verify the room ID;
compute identifiers for a format that carries none), then T-4100 (SRv2.1), then — and only then —
`supported` and `capabilities.m.room_versions` gain "12". Nothing turns green in Complement at the end
of this commit, and none is claimed: the intermediate state is a v12 room that joins as far as state
resolution and then says so honestly.

**Seen and not chased, in the open.** `TestRemoteUserJoinsARoomHeldElsewhere` failed once in eight
`-race` runs of the full integration package, with `a remote server refused an event: room … is not
known to this server` — the resident server pushing a PDU at a joining server before the room landed.
Four runs after it passed with these changes, three before them, and four in isolation, so it is
recorded as T-4101 rather than waved away as flaky or blamed on this commit without evidence. It is the
same family as F-011: an outcome that is not stored, then referenced as though it had been.

## A room version 12 room can be joined (`2543104`): four causes, and three were invisible until their
## predecessors were fixed

`TestComplementCanCreateValidV12Rooms` passes — the first movement in the room version 12 cluster,
measured as **1 newly passing, 0 newly failing** across a 42-test selection (V12, MSC4291, MSC4289,
MSC4297, MSC4311, MSC3757, the federation joins), `PARALLEL=1`, fingerprints `0/0/0/0/0`. One green
test is a small headline, so the value here is the sequence underneath it.

Read in order, the four causes are a lesson in why a protocol milestone has to be driven against
something that talks back:

1. **The handover was never told which room it was a handover of**, so a create event carrying no
   `room_id` — legal, *required*, in version 12 — was discarded and the room never existed here. This
   is the bug the whole session began by looking for, and the previous session's note about it had the
   symptom right and the diagnosis wrong (it blamed state resolution).
2. **My own rule 2 then refused every event in every valid version 12 room**, because it demanded the
   create event in a state that version 12 forbids an event from citing. Run 20261108v12after. A rule
   written from the specification page, correct against the page, impossible against the protocol —
   the page lists rule 2 among the authorisation rules without mentioning that the same page removes
   the create event from the set those rules are evaluated over.
3. **A rule that needs the create event cannot reach it any more — and that is a class, not a typo.**
   The next root cause was `m.room.power_levels needs power level 50, sender has 0` on a room's *first*
   power levels event, sent by the room's creator: every rule reaching for the creator was silently
   finding nobody, because up to version 11 the create event is in the auth chain and in version 12 it
   is not. `createEventOf` now does what the specification's own parenthetical says — the room ID,
   "being the `m.room.create` event's ID" — and four rules use it. Note the shape of the failure: not
   an error about create events, but a creator with the power of a stranger.
4. **4.3.1 could not resolve the prev event, so the room's creator was refused their own room for want
   of an invitation** — and everything citing that join was refused one cascade later for citing a
   rejected auth event. The fix distinguishes two permissions that had never needed separating: the
   bundle's events are now resolvable *by identifier*, and are deliberately not added to the state an
   event is authorised against.

**What is still refused, said as a boundary rather than an apology.** State Resolution 2.1 is
unimplemented and `internal/state` refuses it by name, so this room joins, sends and reads because it
has never forked — fork it and the answer is a loud error, not a guess. Room version 12 is still absent
from `supported`, so no client can ask this server to create one, and `capabilities.m.room_versions`
stays truthful. `AcceptRemoteEvent` still cannot reach an uncited create event outside a bundle
(T-4102). And one log line (`cannot record the latest stream position of a handed-over room: persist:
not found`) appeared in all four probe runs and is T-4104 rather than a shrug, because T-4103 will walk
down that store path for the first time locally.

**The order held, and it mattered.** Auth rules before the paths that reach them, room identity before
state resolution, and `supported` last of all — so that no intermediate commit ever admitted a version
12 room to a server that could not authorise one. The temptation during cause 3 was to make state
resolution work next because it is the biggest red cluster; the reason not to is that SRv2.1 running
over rules that find no creator would produce a room two servers agree on and both get wrong.

**Evidence.** Run 20261108v12set, log/delta/TSV under `.agent-work/next/`. `make verify` exit 0
(integration 29.1s, unit 5.4s, `-race`). `tests/unit/v12rules_test.go`: 33 passing tests, now including
the guard for cause 2 — an event authorised against an auth chain with no create event must not be
refused by rule 2, because that is the shape every valid version 12 event arrives in.

## Room version 12 end to end: state resolution 2.1 (`2d0dddf`) and creation (`def259a`), and eight
## tests that had been immovable

Two commits, seven Complement tests newly passing and none broken in the version 12 selection, and a
baseline that moved from 143 to 144 with the join fix (run 20261108nn, full serial, `PARALLEL=1`, image
self-declared `2543104` clean, fingerprints zero: **144 passing, 75 failing, 1 skipped of 220**).

**State Resolution 2.1 is three sentences of difference, not a new algorithm.** The implementation runs
through the version 2 function behind one flag — deliberately: the parts where a mistake is silent and
catastrophic (orderings, auth difference, iterative checks) are shared, and two copies to keep in step is
how two servers end up disagreeing about who runs a room. The conflicted state subgraph is the one
piece of new graph work, and its definition invites two wrong implementations that are named in the code
so the next reader recognises them: keeping only the conflicted events from each conflicted event's auth
chain gives you the endpoints and none of what is between them; walking once and marking the first path
found makes the resolved state depend on map iteration order. It walks forward to build the universe and
backward from the contested events instead, which matches how gomatrixserverlib does it with path
book-keeping on a DFS.

**The empty-start modification is not "events must cite more"; it is *which state judges an event*.** The
fixture that shows it is the one the MSC was written for: bob set a join rule while he had the power, the
room later agreed a power levels event demoting him, and that later event is what both forks agree on.
Version 2 hands bob's event the demotion to be judged by and refuses an event that predates it; 2.1 reads
the power levels event the event cites. Asserting this from both directions — version 2 answers one way,
version 12 the other — is what keeps the difference from silently disappearing later.

**The first draft of those tests was wrong about the fixture, and inverting the assertion would have
frozen a protocol error into the suite.** It built alice banning bob while citing nothing about her own
membership and asserted the ban wins; it does not, because with an empty start the ban's own auth events
are the only place her membership can come from — which is exactly why the auth selection algorithm names
the *sender's* member event. Rewritten to cite what the selection names, the ban wins and the parity
claim with version 2 means something.

**Then version 12 rooms could be created, and three facts about identifiers arrived together.**
The room's name is a hash of its create event, so: it has to be **sealed first** (the reference hash
covers `hashes`; gomatrixserverlib deletes only `signatures` and `unsigned`), which the check comparing
the create event's own hash against the name the room is filed under caught immediately — checks like
that earn their keep by being wrong once. The create event then carries **no room identifier**, so
`persistEvent` reading it out of the event filed the room's birth under `""`, which is the whole of
T-4104's mystery: `SetLatestStream("")` and "persist: not found" was the database being right. And a
creator's power has to be **one above the ceiling a room can express**, because MSC4289 says infinite in
the same breath as letting an admin be given 2^53−1 — at the ceiling they tie, and every strict comparison
(kick, ban, unban, rule 10 over `users`) then refuses the creator their own room. Complement tests that
exact value on purpose.

**Two status codes were wrong in a way that matters to clients.** A client writing a create event, or
naming a creator in a power levels event, gets 400: 403 tells a client that more power would fix it, and
for both of those requests nothing would. Upgrading *into* version 12 stays a 400 with a sentence saying
what is missing (T-4105: carry the old creators into `additional_creators`, which changes the bytes the new
identifier hashes to, and strike them from the copied power levels); before that gate existed it was a 500
caused by rule 10.4 firing correctly.

**Measured, not assumed.** Run 20261109a (`v12create2`), the version 12 selection, `PARALLEL=1`,
fingerprints zero: **7 newly passing, 0 newly failing**. The MSC4297 pair now fails inside federation
rather than at `createRoom` — further along, which is the expected order. `make verify` exit 0.

**Open, written down rather than left as a shrug:** T-4105 (upgrades into 12), T-4106 (the client API must
put the room identifier *back* on create events in `/state`, `/messages`, `/event`, `/context`; the stored
bytes must stay identifier-free or the event stops hashing to its own name), T-4107 (the restricted-join
path offers a creator power level 100, which is a second place making a claim that `eventauth` already
makes properly), and T-4102 (the single-event receipt path cannot reach an uncited create event).

## The presentation boundary, and one clean refactor that was wrong (`f064ccf`, `25969be`)

Two tasks closed on measured behaviour rather than on argument, and one lesson worth more than either.

**MSC4291 looks like a contradiction and is two claims about two things.** Room version 12 rule 1.2 refuses
a create event carrying `room_id` — an event cannot contain the hash of itself — and yet the Client-Server
API must include the room identifier on the create event. So the identifier is added on the way out
(`internal/api/client/presentation.go`, five handlers) and never on the way in, where it would rename every
version 12 room this server made. `/sync` is asserted as the exception *including that it shows the event
at all*: an exception nobody can see being taken is indistinguishable from a rule nobody ever applied.
Verified by turning the stamp off and counting the failures it should cause.

**The restricted-join path powered a version 12 creator at zero.** The decision read the power levels
event, and a version 12 creator is deliberately not in it (rule 10.4), so the room's own creator could not
invite anyone into it while `eventauth` held that same user infinite. One question, two answers.

**And the obvious refactor was wrong.** Routing the answer through a single exported
`eventauth.PowerLevelsFor` fed by `Service.loadState` — the tidy thing, the thing this project's own notes
ask for — broke `TestRestrictedJoinIsNotGrantedFromAnAllowRoomThisServerHasLeft` in six seconds. Cause:
`loadState` skips state rows whose events this server does not hold, so on a room joined from elsewhere the
power levels event can be absent from the snapshot, and the *defaults* then answer an invitation question
with a fifty the room never set. Single-source-of-truth is right about the *rule* and wrong about the *view*:
the creator claim now comes from `eventauth.CreatorsOf`, while the state it is applied to stays as narrow as
it has always been. Both halves are recorded in the helper's comment, because the tidy version will be
attempted again by someone who has not read this.

Both changes were proved by inversion: each new test fails without its fix (with the message naming what a
version 12 room owes its creator) and passes with it. A negative control in the second test — a guest
listed at power zero — caught a bug in the test itself: `h.register` returns `(userID, accessToken,
deviceID)` and the first draft asserted about device IDs, one of which agreed with the buggy code. That is
what a control is for.

Complement measurements for these two are deliberately *not* recorded: the full serial run for `def259a` was
still holding the podman state, and a second deployment suite beside it produces failures that are evidence
about nothing. `TestMSC4291RoomIDAsHashOfCreateEvent_RoomIDIsOnCreateEvent`,
`TestRestrictedRoomsLocalJoinNoCreatorsUsesPowerLevelsV12` and its knock sibling are the criteria, named in
the task entries, to be read off the next run.

## M1 and M2, first blood: aliases, and the only route an encrypted room has (`427b44e`, `e6e521b`)

Two milestones started, two Complement tests moved from red to green, and one wrong diagnosis caught before
it became code.

**`TestOutboundFederationSend` never tested outbound send.** It failed at `POST /join/#flibble:other-server`,
which this server answered `501 ... that requires federation`, so there was no room, no remote member and
no event to relay. The plan had a task to build a relay that already existed and worked. The fix was
therefore in the front door: `GET /_matrix/federation/v1/query/directory` inbound, `Service.AliasTarget` as
one source of the answer, and client-side resolution for `/join`, `/knock` and `GET /directory/room/{alias}`.
`GET /directory/room/{alias}` had been answering 404 for remote aliases, which is indistinguishable to a
user from "this room was deleted"; now it asks the server that owns the name.

**To-device was the whole of encryption's carrier and none of it existed** — no endpoint, no store, a
`to_device` field in the sync response that nothing ever wrote. Three small pieces later, a client on this
server can hand a Megolm session to another client's device.

**The mistake worth more than the feature.** Complement's to-device test failed on the *second* recipient
of one send. The transaction-idempotency index keyed on `txn_id` alone, which is a genuine design flaw —
a transaction deduplicates a *request*, a request is a set of messages, and the second recipient looked
like a retry of the first — so fixing it felt like confirmation. It wasn't the cause: the local test passed
with the buggy index too, because the second row had been in the database the whole time. The actual bug was
one line in another package: `device_messages` was missing from `streamTables`, the union that defines the
sync clock, so its rows sat at positions no token could reach and `/sync` answered "nothing changed" about
a key that had just arrived. The lesson is about the shape of the error, not its size: **a diagnosis that
fits the symptom exactly is the one most likely to be accepted without being checked**, and the check cost
two lines of a debug test.

Evidence: `m1alias2` (2 pass / 0 fail), `m1family` (3 newly passing, 0 newly failing across the join,
invite, message and knock families), `m2td3` (`TestToDeviceMessages` passes). Local: alias-target and
signature tests, and `TestToDeviceMessagesReachOneDevice` covering fan-out, per-device cursors, retried
transactions, `*` expansion and one-send-two-recipients. `gofmt`, `go vet`, the full non-race suite and
`-race` over the touched packages clean. A full serial run (`20261109-m1m2`) is in flight, because putting
a table into the stream union changes positions in a way only a broad suite can check.

Known and deliberate: a to-device send addressed to a device on another server is logged and dropped until
T-4211 gives it an EDU.

## 2026-11-09 — Batch 1 (federation event graph), part 2 — tally 157/220

Run: `complement-summary-full8.txt` (tree `bb10be8`, parallel 4, 50m, fingerprints zero).
**157 passing / 62 failing / 1 skipped** — reference `complement-results-20261109-m1m2-clean.tsv` was 155.

Newly passing (3): `TestGetMissingEventsGapFilling`, `TestInboundCanReturnMissingEvents` (all four
visibility subtests), `TestOutboundFederationEventSizeGetMissingEvents`.
Newly failing (1): `TestSyncTimelineGap` — understood, not dismissed; see T-4218. The inline hole-closing
on `/send` is what fixes the first and breaks the second, and the same inline fill is load-bearing for the
local `TestRoomServerACLRefusesTheExcludedServer`, so it cannot simply come out either. The resolution is
accept-as-outlier plus an asynchronous fill plus `limited` on truncation, and the two Complement tests are
the pair that defines it.

Work landed this session (4 commits on `retry`, all with `go build`, `go vet`, full local suite clean):
1. `27772d9` names the events a peer sends for a hole (`GapStore.NameRemotePDUs`), turns one fill into a
   bounded loop over `BundleOutcome.Unresolved`, and logs an answered `/get_missing_events`.
2. `4d1d11d` stops public rooms writing a `guest_access` event that only repeats the default — and removes
   the event that was hiding a real bug: absence of `m.room.guest_access` was read as *guests may join*,
   against the event's own schema, advertising guest access in the room directory and space summaries.
3. `bb10be8` fixes two identifier-grammar sites that split at the **last** colon, so any server name with a
   port (`example.org:8448`, and Complement's stub on an ephemeral port) matched nothing: history
   visibility decided a joined peer was in no room, and event authorisation compared names that were not
   server names. Both now use `internal/matrixid`; two regression tests pin it, including the legacy
   event-ID case the naive split was written for.
4. `405ddc1` makes both gap refusals name the events they lack, and pins the detection while the filling is
   disconnected from `/send`.

Remaining in Batch 1, each needing its own measurement cycle: T-4218 (accept-with-hole + async fill +
`limited`), T-4206/T-4217 (`/backfill` both directions, and the negative stream ordering it forces),
outbound `/event_auth` fetching for `TestCorruptedAuthChain`, the MSC4242 rejoin-after-rejection family,
`PartialStateJoin`, `DelayedEvents`.
