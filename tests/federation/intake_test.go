package federation

import (
	"encoding/json"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// The last step of judging an arriving event is putting it in the room. Everything
// before this tested that an event is received, named and refused when it has to be;
// these tests are about the three outcomes an event can have once it reaches the room's
// own rules, and what each of them does to what a client sees.
//
// The rooms here are still handed to the second server by hand - the join handshake that
// would do it is the next step of the plan. What is no longer faked is the event: it is
// built, hashed and signed on one server, delivered over the transport, and judged by
// the other from what it can fetch for itself.

// handTheRoomTo copies a room's state and memberships from one server to the other: the
// room row, every state event, and the membership rows. It is a join handshake written by
// hand, and it stays because the tests that use it are about what a server does with an event
// that arrives - authorisation, soft-failing, intake order - and a real handshake between
// them would add a minute of protocol to each and hide the thing under test. The scenarios
// that are about the protocol itself refuse this helper: see
// TestTheFederationScenarioTableRunsWithoutSeeds, where every room reaches the second server
// through the join.
func handTheRoomTo(t *testing.T, to, from *server, sess *session, roomID string, members ...string) {
	t.Helper()
	room, err := from.db.RoomRepo.Get(roomID)
	if err != nil {
		t.Fatalf("reading the room on %s: %v", from.name, err)
	}
	if err := to.db.RoomRepo.Create(&persist.Room{
		ID: roomID, Creator: room.Creator, RoomVersion: room.RoomVersion, JoinRule: room.JoinRule,
	}); err != nil {
		t.Fatalf("creating the room on %s: %v", to.name, err)
	}
	pos := uint64(0)
	for _, entry := range stateOf(t, from, sess, roomID) {
		event, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("the state of %s holds %v, which is not an event", roomID, entry)
		}
		typ, _ := event["type"].(string)
		key, hasKey := event["state_key"].(string)
		if !hasKey {
			continue
		}
		content, err := json.Marshal(event["content"])
		if err != nil {
			t.Fatalf("encoding the content of %s: %v", typ, err)
		}
		id, _ := event["event_id"].(string)
		sender, _ := event["sender"].(string)
		pos++
		if err := to.db.StateRepo.Upsert(&persist.RoomState{
			RoomID:    roomID,
			Type:      typ,
			StateKey:  key,
			EventID:   id,
			StreamPos: pos,
			Sender:    sender,
			Content:   string(content),
		}); err != nil {
			t.Fatalf("copying the state of %s to %s: %v", roomID, to.name, err)
		}
	}
	// The membership rows go in on both sides. The server that made the room has to
	// believe a peer's user is in it, or it will never send that server anything; the
	// server receiving has to believe it too, or it will refuse to answer for the room.
	// A real join writes both sides from opposite ends, and that is the handshake the
	// next step of the plan is about.
	for _, user := range members {
		shareRoom(t, from, roomID, user)
		shareRoom(t, to, roomID, user)
	}
}

// TestAnArrivingEventBecomesRoomHistory is the whole point of the intake work: an event
// made on another server ends up in this server's room, with its own id, its own place in
// the graph, and its content intact.
func TestAnArrivingEventBecomesRoomHistory(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")
	roomID := one.createRoom(alice, map[string]any{"name": "Both sides"})
	handTheRoomTo(t, two, one, alice, roomID, alice.UserID, "@bob:"+two.name)

	sent := one.send(alice, roomID, "across the wire")
	flush(t, one)

	receipt, err := two.db.ReceivedPDURepo.Get(sent)
	if err != nil {
		t.Fatalf("%s never received the event: %v", two.name, err)
	}
	if receipt.Outcome != service.OutcomeStored {
		t.Fatalf("%s decided %q for the event, want %q: %s",
			two.name, receipt.Outcome, service.OutcomeStored, receipt.Reason)
	}

	stored, err := two.db.EventRepo.Get(sent)
	if err != nil {
		t.Fatalf("%s did not store the event: %v", two.name, err)
	}
	if stored.RoomID != roomID || stored.Sender != alice.UserID || stored.Type != matrix.TypeMessage {
		t.Errorf("the stored event is %s by %s of type %s, want %s by %s of a message",
			stored.RoomID, stored.Sender, stored.Type, roomID, alice.UserID)
	}
	var content struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(stored.Content), &content); err != nil || content.Body != "across the wire" {
		t.Errorf("%s stored the content %s, which does not say what was sent: %v", two.name, stored.Content, err)
	}

	// A client on the receiving server reads the message in the room's history, which is
	// the property the receipt checks were worth doing all this for.
	if !visible(t, two, bob, roomID)[sent] {
		t.Errorf("%s does not show the event to a member of the room", two.name)
	}

	// The event took the place of the leaves it cited: a graph whose leaves are never
	// retired grows a fork for every event that arrives.
	extremities, err := two.db.EventRepo.ForwardExtremities(roomID, 10)
	if err != nil {
		t.Fatalf("reading the graph leaves on %s: %v", two.name, err)
	}
	if !contains(extremities, sent) {
		t.Errorf("%s holds %v as the leaves of the room, which do not include the event it accepted",
			two.name, extremities)
	}

	// The state the room carried across the wire is still the state it had: the message
	// changed nothing but the timeline.
	if got := two.stateContent(t, roomID, matrix.TypeName, ""); got != `{"name":"Both sides"}` {
		t.Errorf("%s holds the room name as %s, want it as it was on the server that made it", two.name, got)
	}
}

// TestAnEventTheRoomDoesNotSupportIsKeptQuietly covers the soft failure. The sender's own
// chain says the sender is joined; this server's state says the sender was banned. Both
// are honest about the room they have seen, so the event is not refused: it is stored, it
// goes on to other servers, and no client is told about it, because this server's state
// does not support it.
func TestAnEventTheRoomDoesNotSupportIsKeptQuietly(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")
	roomID := one.createRoom(alice, map[string]any{"name": "Both sides"})
	handTheRoomTo(t, two, one, alice, roomID, alice.UserID, "@bob:"+two.name)

	// The room on two bans alice, and one never hears about it: the ban is written into
	// two's state by hand, which is what a ban that has not reached the other server yet
	// looks like from here.
	ban := map[string]any{
		"type":        matrix.TypeMember,
		"state_key":   alice.UserID,
		"event_id":    "$ban:two.example",
		"sender":      "@bob:" + two.name,
		"content":     map[string]any{"membership": matrix.MembershipBan},
		"origin":      two.name,
		"depth":       int64(4),
		"prev_events": []any{},
	}
	encoded, err := json.Marshal(ban["content"])
	if err != nil {
		t.Fatalf("encoding the ban: %v", err)
	}
	if err := two.db.StateRepo.Upsert(&persist.RoomState{
		RoomID: roomID, Type: matrix.TypeMember, StateKey: alice.UserID,
		EventID: "$ban:two.example", StreamPos: 900,
		Sender: "@bob:" + two.name, Content: string(encoded),
	}); err != nil {
		t.Fatalf("banning %s on %s: %v", alice.UserID, two.name, err)
	}
	if err := two.db.MembershipRepo.Set(&persist.RoomMembership{
		RoomID: roomID, UserID: alice.UserID, Membership: matrix.MembershipBan,
		EventID: "$ban:two.example", SentTS: 1700000002000,
	}); err != nil {
		t.Fatalf("recording the ban on %s: %v", two.name, err)
	}

	// Alice, who has not been told, keeps talking. The event is authorised by everything
	// the sender's chain knows, and unsupported by everything this server knows.
	sent := one.send(alice, roomID, "unbanned yet")
	flush(t, one)

	receipt, err := two.db.ReceivedPDURepo.Get(sent)
	if err != nil {
		t.Fatalf("%s never received the event: %v", two.name, err)
	}
	if receipt.Outcome != service.OutcomeSoftFailed {
		t.Fatalf("%s decided %q, want %q: %s",
			two.name, receipt.Outcome, service.OutcomeSoftFailed, receipt.Reason)
	}

	stored, err := two.db.EventRepo.Get(sent)
	if err != nil {
		t.Fatalf("a soft-failed event was not kept: %v", err)
	}
	if !stored.SoftFailed {
		t.Errorf("%s kept the event without recording that it does not apply it", two.name)
	}
	if visible(t, two, bob, roomID)[sent] {
		t.Errorf("%s shows a client an event its own state does not support", two.name)
	}
	// It is not a leaf of the graph either. An event this server will not build on must
	// not be the tip it offers, and a local message sent after it must not cite it.
	extremities, err := two.db.EventRepo.ForwardExtremities(roomID, 10)
	if err != nil {
		t.Fatalf("reading the graph leaves on %s: %v", two.name, err)
	}
	if contains(extremities, sent) {
		t.Errorf("%s made an event it does not apply a leaf of the room: %v", two.name, extremities)
	}
	// The state did not move: the room's membership is still the ban, not the message.
	if got := two.stateContent(t, roomID, matrix.TypeMember, alice.UserID); got != `{"membership":"ban"}` {
		t.Errorf("%s holds %s as the membership of %s, want the ban it decided",
			two.name, got, alice.UserID)
	}
	// A soft-failed event is not a local one's parent: the next message from bob is
	// accepted and is the leaf this server offers.
	next := one.send(alice, roomID, "still talking")
	flush(t, one)
	if _, err := two.db.EventRepo.Get(next); err == nil {
		if visible(t, two, bob, roomID)[next] {
			t.Errorf("%s applied a second event from a banned user", two.name)
		}
	}
}

// TestAnEventWithTheWrongRulesIsRefused covers the rejection. Every event the sender cites
// is fetchable and signed, so nothing about the delivery is wrong - but the set of rules
// the sender chose for itself is not the set the room's selection algorithm names, and a
// server that accepted such an event would be accepting the sender's choice of rules.
func TestAnEventWithTheWrongRulesIsRefused(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")
	roomID := one.createRoom(alice, map[string]any{"name": "Both sides"})
	handTheRoomTo(t, two, one, alice, roomID, alice.UserID, "@bob:"+two.name)

	// The room's create event, which is real, signed and fetchable - and the only rule
	// the forged event bothers to cite. A room with the creator's power levels and an
	// invite-only join rule is not what that set describes.
	createID := ""
	for _, entry := range stateOf(t, one, alice, roomID) {
		event, _ := entry.(map[string]any)
		if event["type"] == matrix.TypeCreate {
			createID, _ = event["event_id"].(string)
		}
	}
	if createID == "" {
		t.Fatalf("the room on %s carries no create event", one.name)
	}

	pdu := signedPDU(t, one, matrix.Object{
		"room_id":          roomID,
		"type":             matrix.TypeMessage,
		"sender":           alice.UserID,
		"origin_server_ts": int64(1700000003000),
		"depth":            int64(7),
		"prev_events":      matrix.Array{createID},
		"auth_events":      matrix.Array{createID},
		"content":          matrix.Object{"msgtype": "m.text", "body": "by my own rules"},
	})
	answer, failure := sendTransaction(t, one, two.name, "wrong-rules", transactionBody(one.name, matrix.Array{pdu}, nil))
	if failure != nil {
		t.Fatalf("the event was refused at the envelope: %d %s", failure.Status, failure.Message)
	}
	id, err := derivedEventID(t, two, roomID, pdu)
	if err != nil {
		t.Fatalf("the forged event cannot be named: %v", err)
	}
	if entry, ok := answer["pdus"].(matrix.Object)[id]; ok {
		if result, isObject := entry.(matrix.Object); !isObject || len(result) != 0 {
			t.Errorf("the event was answered with %v, which claims a problem a refusal does not have", entry)
		}
	}

	receipt, err := two.db.ReceivedPDURepo.Get(id)
	if err != nil {
		t.Fatalf("%s never received the event: %v", two.name, err)
	}
	if receipt.Outcome != service.OutcomeRejected {
		t.Fatalf("%s decided %q, want %q: %s",
			two.name, receipt.Outcome, service.OutcomeRejected, receipt.Reason)
	}
	if receipt.Reason == "" {
		t.Error("the refusal records no reason, so an operator reading the receipt learns nothing")
	}
	// The refusal is not a partial acceptance: no event row, no state, no leaf, and no
	// copy of the event anywhere a client can reach.
	if _, err := two.db.EventRepo.Get(id); !persist.IsNotFound(err) {
		t.Errorf("%s kept a refused event as a room event: %v", two.name, err)
	}
	if visible(t, two, bob, roomID)[id] {
		t.Errorf("%s shows a client an event it refused", two.name)
	}
	extremities, err := two.db.EventRepo.ForwardExtremities(roomID, 10)
	if err != nil {
		t.Fatalf("reading the graph leaves on %s: %v", two.name, err)
	}
	if contains(extremities, id) {
		t.Errorf("%s made a refused event a leaf of the room: %v", two.name, extremities)
	}
	// The events it cited were fetched for the judgement, which is what made the
	// judgement possible at all.
	if _, err := two.db.EventRepo.Get(createID); err == nil {
		t.Errorf("%s stored an auth event it only fetched to judge an event", two.name)
	}
}

// contains reports whether a list holds a value.
func contains(list []string, value string) bool {
	for _, entry := range list {
		if entry == value {
			return true
		}
	}
	return false
}

// stateContent reads the content of one state event from a server's own database, which
// is what the room's rules read and what the tests must not take on trust.
func (s *server) stateContent(t *testing.T, roomID, typ, stateKey string) string {
	t.Helper()
	row, err := s.db.StateRepo.Get(roomID, typ, stateKey)
	if err != nil {
		t.Fatalf("reading the state of %s (%s, %s) on %s: %v", roomID, typ, stateKey, s.name, err)
	}
	return row.Content
}
