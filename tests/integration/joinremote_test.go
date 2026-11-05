package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/rooms"
	"github.com/enimatek/lattice/internal/roomversion"
)

// A local user joining a room held by another homeserver is the other half of the handshake the
// resident server answers in federationjoins_test.go. Here the client asks in the ordinary way —
// POST /join — and this server has to become a joining server: ask someone who holds the room
// for the shape a join by its own user would take, sign that as its own, hand it back, and keep
// the room it is handed in return.

// TestLocalUserJoinsARemoteRoomThroughTheClientAPI joins a room on hs2 the way a client would.
func TestLocalUserJoinsARemoteRoomThroughTheClientAPI(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "hostess", "pw-hostess")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "open to hs2"}, alice)
	roomID, _ := created["room_id"].(string)
	// A room with a message in it. The join will be offered the room's forward extremities as
	// its predecessors, and a message is what an extremity usually is — which is exactly the
	// case a handed-over room cannot resolve on its own.
	if status, answer := hs1.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/host-txn",
		map[string]any{"msgtype": "m.text", "body": "come in"}, alice); status != http.StatusOK {
		t.Fatalf("a message on %s: %v", hs1.name, answer)
	}

	bea := "@bea:" + hs2.name
	beaToken := hs2.registerUser(t, "bea", "pw-bea")

	// The client names the server to ask. A room ID names the server that created the room,
	// which is not always one that still holds it, so the ask is the client's to make.
	status, joined := hs2.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/join/"+roomID+"?server_name="+hs1.name, map[string]any{}, beaToken)
	if status != http.StatusOK {
		t.Fatalf("%s could not join %s on %s: %d %v", hs2.name, roomID, hs1.name, status, joined)
	}
	if got, _ := joined["room_id"].(string); got != roomID {
		t.Errorf("%s answered the join with room %q, wanting %q", hs2.name, got, roomID)
	}

	// The resident server has the user in its room. That is the handshake's whole purpose, and
	// nothing else here proves it.
	_, members := hs1.clientDo(t, http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/joined_members", nil, alice)
	if !membershipOf(t, members, bea) {
		t.Fatalf("%s does not list %s as present after the join: %v", hs1.name, bea, members)
	}

	// And this server now holds the room it joined: it appears in the user's joined rooms, and
	// the room's state is here — the room's own account of its rules, not a copy this server
	// invented.
	_, rooms := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/joined_rooms", nil, beaToken)
	if list, _ := rooms["joined_rooms"].([]any); !containsAny(list, roomID) {
		t.Errorf("%s does not list the room its own user just joined: %v", hs2.name, rooms)
	}
	state := hs2.clientDoList(t, "/_matrix/client/v3/rooms/"+roomID+"/state", beaToken)
	if !hasType(t, state, "m.room.create") || !hasType(t, state, "m.room.power_levels") ||
		!hasType(t, state, "m.room.join_rules") {
		t.Errorf("%s joined a room without keeping its foundations: %v", hs2.name, typeList(state))
	}
	if !hasType(t, state, "m.room.name") {
		t.Errorf("%s's copy of the room has lost its name: %v", hs2.name, typeList(state))
	}
	if !hasMembership(t, state, bea, "join") {
		t.Errorf("%s does not hold its own user's join in the room state: %v", hs2.name, typeList(state))
	}

	// The history of the room is readable from here. The join cited the room's forward
	// extremities, and the state handed over does not include them, so the room arrived with a
	// hole under it and the message sent before the join could not be placed. Asking the resident
	// server for what is missing is what closes it — and a room a user cannot read is not much
	// use to them.
	if !waitFor(t, 15*time.Second, func() bool {
		_, page := hs2.clientDo(t, http.MethodGet,
			"/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, beaToken)
		return chunkHasBody(t, page, "come in")
	}) {
		_, page := hs2.clientDo(t, http.MethodGet,
			"/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, beaToken)
		t.Errorf("%s still cannot read the history of the room %s joined: %v", hs2.name, bea, page)
	}
	if outliers, err := hs2.app.DB.Events.Outliers(context.Background(), roomID); err != nil {
		t.Fatalf("reading what %s holds unlinked: %v", hs2.name, err)
	} else if len(outliers) > 0 {
		t.Errorf("%s filled the gap but left %d events unlinked in a room it claims to hold",
			hs2.name, len(outliers))
	}

	// The room is usable from here, which is what joining has to buy. A message sent by the
	// local user has to reach the server that holds the room.
	if status, answer := hs2.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/bea-txn",
		map[string]any{"msgtype": "m.text", "body": "hello from hs2"}, beaToken); status != http.StatusOK {
		t.Fatalf("%s cannot speak in the room it joined: %d %v", hs2.name, status, answer)
	}
	if !waitFor(t, 15*time.Second, func() bool {
		_, page := hs1.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, alice)
		return chunkHasBody(t, page, "hello from hs2")
	}) {
		t.Errorf("%s never heard the message %s sent from the room they share", hs1.name, bea)
	}
}

// TestRemoteJoinRefusesWithoutInventingARoom: a join of a room nobody answers is a failure, and
// the failure must not leave this server holding a room it has been told nothing about.
func TestRemoteJoinRefusesWithoutInventingARoom(t *testing.T) {
	t.Parallel()
	_, hs2 := federationPair(t)

	beaToken := hs2.registerUser(t, "wanderer", "pw-wanderer")
	fictional := "!nowhereknown:hs9"
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+fictional,
		map[string]any{}, beaToken); status == http.StatusOK {
		t.Errorf("%s claimed to join a room no server answered for: %v", hs2.name, answer)
	}
	if status, answer := hs2.clientDo(t, http.MethodGet,
		"/_matrix/client/v3/rooms/"+fictional+"/state", nil, beaToken); status == http.StatusOK {
		t.Errorf("%s holds a room nobody handed over: %v", hs2.name, answer)
	}

	// A room ID carrying this server's own name is not somebody else's room. Asking a stranger
	// to hand one over would let that stranger invent a room under this server's name.
	own := "!butfake:" + hs2.name
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+own,
		map[string]any{}, beaToken); status != http.StatusNotFound {
		t.Errorf("a room on this server's own name that it does not hold was answered %d (%v), wanting 404",
			status, answer)
	}
}

// containsAny reads a JSON list of strings for a value.
func containsAny(list []any, want string) bool {
	for _, entry := range list {
		if text, ok := entry.(string); ok && text == want {
			return true
		}
	}
	return false
}

// hasMembership looks for a member event with the membership a room state should hold.
func hasMembership(t *testing.T, state []map[string]any, userID, membership string) bool {
	t.Helper()
	for _, event := range state {
		if event["type"] != "m.room.member" || event["state_key"] != userID {
			continue
		}
		content, _ := event["content"].(map[string]any)
		if got, _ := content["membership"].(string); got == membership {
			return true
		}
	}
	return false
}

// chunkHasBody says whether a `/messages` page carries an event with this body.
func chunkHasBody(t *testing.T, answer map[string]any, body string) bool {
	t.Helper()
	chunk, _ := answer["chunk"].([]any)
	for _, entry := range chunk {
		event, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		content, _ := event["content"].(map[string]any)
		if got, _ := content["body"].(string); got == body {
			return true
		}
	}
	return false
}

// waitFor keeps asking until the answer is yes or the deadline passes. Federation delivery is
// queued and retried, so an event arriving is a matter of when, not whether.
func waitFor(t *testing.T, budget time.Duration, ok func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if ok() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestHandedOverBundleGapsAreNotVerdicts drives the bundle ingest directly, because what it has
// to pin is the difference between "this event is bad" and "this event cannot be judged yet".
// A handed-over room is a set of events that cite each other; treating a missing citation as a
// verdict would refuse a room permanently because one of its events arrived in a different pass.
func TestHandedOverBundleGapsAreNotVerdicts(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "archivist", "pw-archivist")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "the stacks"}, alice)
	roomID, _ := created["room_id"].(string)
	_, sent := hs1.clientDo(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/archive-txn",
		map[string]any{"msgtype": "m.text", "body": "shelved"}, alice)
	messageID, _ := sent["event_id"].(string)

	// The keys of the server the events came from are fetched as they are needed, which is what
	// the checks below exercise: a server this server has never spoken to cannot be trusted on
	// the strength of its own name.
	snapshot := hs1.roomSnapshot(t, roomID, alice)
	ctx := context.Background()
	verify := func(event map[string]any, rules roomversion.Rules) error {
		return events.Verify(rules, event, hs2.app.Keyring.LookupForRoom(ctx, rules))
	}
	pdu := func(event map[string]any) []byte {
		t.Helper()
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encoding an event: %v", err)
		}
		return encoded
	}
	// The message and the state it needs — minus one of the events that authorise its sender.
	// Nothing here is forged: the bundle is simply incomplete, which is the normal shape of a
	// room being told about in pieces. Which event to withhold is read from the message itself,
	// so the gap is real whatever the auth event selection happens to pick.
	message := snapshot.Messages[0]
	cited := anyStringSlice(message["auth_events"])
	withhold := cited[len(cited)-1]
	for _, event := range snapshot.State {
		if stringAny(event, "type") == "m.room.member" && containsString(cited, stringAny(event, "event_id")) {
			withhold = stringAny(event, "event_id")
			break
		}
	}
	incomplete := make([]map[string]any, 0, len(snapshot.State))
	for _, event := range snapshot.State {
		if stringAny(event, "event_id") == withhold {
			continue
		}
		incomplete = append(incomplete, event)
	}
	incomplete = append(incomplete, message)

	bundle := rooms.RemoteBundle{}
	for _, event := range incomplete {
		bundle.PDUs = append(bundle.PDUs, pdu(event))
	}

	outcome, err := hs2.app.Rooms.AcceptRemoteBundle(ctx, bundle, verify)
	if err != nil {
		t.Fatalf("%s could not take a handed-over bundle: %v", hs2.name, err)
	}
	if len(outcome.Unresolved) == 0 {
		t.Fatalf("an event whose authorisation is missing was judged anyway: refused=%v accepted=%v",
			outcome.Refused, outcome.Accepted)
	}
	if _, refused := outcome.Refused[stringAny(message, "event_id")]; refused {
		t.Errorf("%s refused a message for a reason that is only a gap: %v", hs2.name, outcome.Refused)
	}
	if held, err := hs2.app.DB.Events.ByID(ctx, messageID); err == nil && held != nil {
		t.Errorf("%s stored a message it could not authorise", hs2.name)
	}
	// A gap is not a verdict, so nothing is recorded against the event. Recording it would
	// make the gap permanent: the event would stay refused even after the missing citation
	// arrived.
	if rejections, err := hs2.app.DB.Events.Rejections(ctx, []string{messageID}); err != nil || len(rejections) > 0 {
		t.Errorf("%s recorded a refusal for an event it never judged: %v (%v)", hs2.name, rejections, err)
	}

	// The room itself is here: the create event in the bundle is the room's own account of its
	// version, which is the one thing a bundle is allowed to introduce.
	if _, err := hs2.app.DB.Rooms.ByRoomID(ctx, roomID); err != nil {
		t.Errorf("%s did not adopt the room a bundle announced: %v", hs2.name, err)
	}

	// Fill the gap and offer the same events again. The message is the one that could not be
	// judged; now it can.
	full := rooms.RemoteBundle{}
	for _, event := range append(snapshot.State, snapshot.Messages...) {
		full.PDUs = append(full.PDUs, pdu(event))
	}
	outcome, err = hs2.app.Rooms.AcceptRemoteBundle(ctx, full, verify)
	if err != nil {
		t.Fatalf("%s could not take the completed bundle: %v", hs2.name, err)
	}
	if len(outcome.Unresolved) > 0 {
		t.Errorf("%s still cannot judge events it has been given: %v", hs2.name, outcome.Unresolved)
	}
	if len(outcome.Refused) > 0 {
		t.Errorf("%s refused events from a complete bundle: %v", hs2.name, outcome.Refused)
	}
	held, err := hs2.app.DB.Events.ByID(ctx, messageID)
	if err != nil || held == nil {
		t.Fatalf("%s did not store the message once its authorisation arrived: %v", hs2.name, err)
	}
	if !held.Outlier {
		t.Errorf("%s stored a message whose predecessors it does not hold as though it could place it", hs2.name)
	}

}

// TestForgedEventInABundleIsRefused: a bundle is a set of events, and each one still has to
// stand up on its own. An event whose content no longer matches what its origin signed is a
// verdict, not a gap, so it is refused and the refusal is remembered — which is what stops the
// same forgery being offered again, or an event citing it being accepted.
//
// This uses a room neither server has seen, because a refusal is never recorded against an event
// this server already holds: an ID that was accepted once is not refused by a second, worse copy
// of the bytes.
func TestForgedEventInABundleIsRefused(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "witness", "pw-witness")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "signed once"}, alice)
	roomID, _ := created["room_id"].(string)
	snapshot := hs1.roomSnapshot(t, roomID, alice)

	ctx := context.Background()
	verify := func(event map[string]any, rules roomversion.Rules) error {
		return events.Verify(rules, event, hs2.app.Keyring.LookupForRoom(ctx, rules))
	}
	forged := byType(t, snapshot.State, "m.room.name")[0]
	content, _ := forged["content"].(map[string]any)
	content["name"] = "rewritten after the fact"
	encoded, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("encoding the event: %v", err)
	}
	bundle := rooms.RemoteBundle{PDUs: [][]byte{
		encoded,
		mustMarshal(t, byType(t, snapshot.State, "m.room.create")[0]),
	}}
	if _, err := hs2.app.Rooms.AcceptRemoteBundle(ctx, bundle, verify); err != nil {
		t.Fatalf("the bundle call should not fail on one bad event: %v", err)
	}
	forgedID := stringAny(byType(t, snapshot.State, "m.room.name")[0], "event_id")
	if held, err := hs2.app.DB.Events.ByID(ctx, forgedID); err == nil && held != nil {
		t.Errorf("%s stored an event whose content does not match its signature", hs2.name)
	}
	rejections, err := hs2.app.DB.Events.Rejections(ctx, []string{forgedID})
	if err != nil {
		t.Fatalf("reading refusals: %v", err)
	}
	if len(rejections) == 0 {
		t.Errorf("%s did not record a refusal for a forged event, so it can be offered again", hs2.name)
	}
}

// byType picks the events of one type out of a list.
func byType(t *testing.T, events []map[string]any, kind string) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		if event["type"] == kind {
			out = append(out, event)
		}
	}
	return out
}

// mustMarshal encodes an event back into the bytes a bundle carries.
func mustMarshal(t *testing.T, event map[string]any) []byte {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encoding an event: %v", err)
	}
	return encoded
}

// TestBackfillMakesAJoinedRoomsHistoryReadable catches up on a room with more than one event
// missing underneath it.
//
// One missing event is the easy case: the join cites it, it is fetched, and the join is placed. A
// room with a history is harder — the event fetched names another event this server does not hold,
// which names another, and the walk has to stop where this server's own history begins rather than
// run to the room's birth on a room that might be years old. What a client should then see is the
// history in the order it happened, because this server gives events an ordering of its own and a
// history fetched backwards would otherwise be read backwards.
func TestBackfillMakesAJoinedRoomsHistoryReadable(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "chronicler", "pw-chronicler")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "a run of messages"}, alice)
	roomID, _ := created["room_id"].(string)

	// A run of messages, one per turn, so that the history under the join is a chain and not a
	// single event. The last one is the extremity the join will be made against.
	turns := []string{"first", "second", "third", "fourth", "fifth", "sixth"}
	var lastMessageID string
	for i, body := range turns {
		_, sent := hs1.clientDo(t, http.MethodPut,
			"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/run-"+body,
			map[string]any{"msgtype": "m.text", "body": body}, alice)
		lastMessageID, _ = sent["event_id"].(string)
		if i == len(turns)-1 && lastMessageID == "" {
			t.Fatal("the last message of the run has no event ID")
		}
	}

	beaToken := hs2.registerUser(t, "reader", "pw-reader")
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+roomID,
		map[string]any{}, beaToken); status != http.StatusOK {
		t.Fatalf("%s could not join %s: %d %v", hs2.name, roomID, status, answer)
	}

	// All of it is readable, not merely stored: an event this server cannot place is kept from
	// clients, so what appears in the timeline is the proof that the chain was linked.
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, page := hs2.clientDo(t, http.MethodGet,
			"/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, beaToken)
		if seen := bodies(t, page); containsAll(seen, turns) || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	_, page := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, beaToken)
	seen := bodies(t, page)
	if !containsAll(seen, turns) {
		t.Fatalf("%s reads only part of the history of the room it joined: %v", hs2.name, seen)
	}
	if outliers, err := hs2.app.DB.Events.Outliers(context.Background(), roomID); err != nil || len(outliers) > 0 {
		t.Errorf("%s holds %d events of a room it has filled in without a place for them: %v (%v)",
			hs2.name, len(outliers), outliers, err)
	}

	// The order is the order it happened. `/messages` pages by this server's own ordering, newest
	// first, and that ordering is assigned when an event is stored — so a chain fetched
	// newest-first would be read oldest-first by a client asking for the newest events, and the
	// sixth message would look like it came before the first. Both servers are asked here, because
	// the second one is the server that had to earn the ordering.
	recent := reverse(turns)
	_, home := hs1.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, alice)
	if order := bodies(t, home); !sameOrder(order, recent) {
		t.Errorf("%s wrote this history and now reads it as %v, wanting the newest first (%v)",
			hs1.name, order, recent)
	}
	if !sameOrder(seen, recent) {
		t.Errorf("%s reads the history of the room it joined as %v, wanting the newest first (%v)",
			hs2.name, seen, recent)
	}
	if !sameOrder(bodies(t, page), bodies(t, home)) {
		t.Errorf("%s and %s tell the history of the same room in different orders: %v against %v",
			hs2.name, hs1.name, bodies(t, page), bodies(t, home))
	}

	// Speaking in the room now builds on the history that arrived rather than on the state events
	// that stood in for it while the gap was open. The tip of this server's branch should be the
	// join, and the join should sit on the last message of the room's own history — which is what
	// a message citing them continues, instead of starting a fork the room has to merge later.
	memberID, joinPrevs := "", []string{}
	for _, event := range hs2.clientDoList(t, "/_matrix/client/v3/rooms/"+roomID+"/state", beaToken) {
		if stringAny(event, "type") != "m.room.member" || stringAny(event, "state_key") != "@"+"reader:"+hs2.name {
			continue
		}
		memberID = stringAny(event, "event_id")
		joinPrevs = anyStringSlice(event["prev_events"])
	}
	if !containsString(joinPrevs, lastMessageID) {
		t.Errorf("the join this server made cites %v as its predecessors; the room's last message was %s, "+
			"so the history fetched under the join is not the history the join was made against", joinPrevs, lastMessageID)
	}
	_, sent := hs2.clientDo(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/from-reader",
		map[string]any{"msgtype": "m.text", "body": "read you all"}, beaToken)
	_, fresh := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, beaToken)
	var prevs []string
	for _, event := range bodiesWith(t, fresh, stringAny(sent, "event_id")) {
		prevs = anyStringSlice(event["prev_events"])
	}
	if !containsString(prevs, memberID) {
		t.Errorf("a message sent on %s after the history arrived cites %v, wanting the join (%s) that closes that history",
			hs2.name, prevs, memberID)
	}
}

// bodies lists the body text of the message events a page carries, in the order the page gives
// them.
func bodies(t *testing.T, answer map[string]any) []string {
	t.Helper()
	out := make([]string, 0, 8)
	for _, event := range bodiesWith(t, answer, "") {
		content, _ := event["content"].(map[string]any)
		if body, ok := content["body"].(string); ok && body != "" {
			out = append(out, body)
		}
	}
	return out
}

// bodiesWith lists the events a page carries, optionally narrowed to one event ID.
func bodiesWith(t *testing.T, answer map[string]any, eventID string) []map[string]any {
	t.Helper()
	chunk, _ := answer["chunk"].([]any)
	out := make([]map[string]any, 0, len(chunk))
	for _, entry := range chunk {
		event, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if eventID != "" && stringAny(event, "event_id") != eventID {
			continue
		}
		out = append(out, event)
	}
	return out
}

// reverse copies a list in the opposite order.
func reverse(list []string) []string {
	out := make([]string, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	return out
}

// containsAll says whether a list holds every one of the wanted values.
func containsAll(list []string, wanted []string) bool {
	for _, want := range wanted {
		if !containsString(list, want) {
			return false
		}
	}
	return true
}

// sameOrder says whether both lists carry the same values in the same order.
func sameOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestBackfillBudgetLeavesAGapAskable: catching up on a room's history is bounded, and the bound
// must not turn into a claim that the rest does not exist.
//
// A homeserver that fetches a slice of a long history has to keep the remainder fetchable. If a
// partial fetch marked what it could not place as refused, the room would be permanently unreadable
// past the slice — and a room being unreadable because a server ran out of budget is a bug dressed
// up as a fact about the room.
func TestBackfillBudgetLeavesAGapAskable(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "longwinded", "pw-longwinded")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "a long one"}, alice)
	roomID, _ := created["room_id"].(string)
	for _, body := range []string{"one", "two", "three", "four", "five", "six"} {
		if status, answer := hs1.clientDo(t, http.MethodPut,
			"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/long-"+body,
			map[string]any{"msgtype": "m.text", "body": body}, alice); status != http.StatusOK {
			t.Fatalf("a message on %s: %d %v", hs1.name, status, answer)
		}
	}

	beaToken := hs2.registerUser(t, "starter", "pw-starter")
	// A join that may fetch two events. The history under the join is five messages long.
	hs2.app.Backfill.MaxEvents = 2
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+roomID,
		map[string]any{}, beaToken); status != http.StatusOK {
		t.Fatalf("%s could not join %s: %d %v", hs2.name, roomID, status, answer)
	}

	_, page := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, beaToken)
	if !chunkHasBody(t, page, "six") {
		t.Errorf("%s did not fetch the end of the history it joined, which is the part the budget should have bought: %v",
			hs2.name, bodies(t, page))
	}
	if chunkHasBody(t, page, "one") {
		t.Errorf("%s shows a message it never fetched; the budget is not being honoured", hs2.name)
	}
	outliers, err := hs2.app.DB.Events.Outliers(context.Background(), roomID)
	if err != nil {
		t.Fatalf("reading what %s holds unlinked: %v", hs2.name, err)
	}
	if len(outliers) == 0 {
		t.Fatalf("%s claims a partly fetched history is fully placed", hs2.name)
	}

	// The gap survives as a question. Asking again with room in the budget finishes the job — no
	// event had to be forgiven, and nothing was refused on the way.
	gaps, err := hs2.app.Rooms.MissingPredecessors(context.Background(), roomID, 10)
	if err != nil {
		t.Fatalf("reading the gaps: %v", err)
	}
	if len(gaps) == 0 {
		t.Fatalf("%s names no gap in a history it stopped fetching", hs2.name)
	}
	hs2.app.Backfill.MaxEvents = 0
	if _, err := hs2.app.Backfill.Fill(context.Background(), roomID, []string{hs1.name}); err != nil {
		t.Fatalf("%s could not finish catching up on %s: %v", hs2.name, roomID, err)
	}
	if !waitFor(t, 15*time.Second, func() bool {
		_, page := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, beaToken)
		return chunkHasBody(t, page, "one") && chunkHasBody(t, page, "four")
	}) {
		_, page := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, beaToken)
		t.Errorf("%s never read the history it had stopped short of: %v", hs2.name, bodies(t, page))
	}
	if outliers, err := hs2.app.DB.Events.Outliers(context.Background(), roomID); err != nil || len(outliers) > 0 {
		t.Errorf("%s finished fetching but still has %d events with no place: %v (%v)",
			hs2.name, len(outliers), outliers, err)
	}
}
