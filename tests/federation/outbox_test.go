package federation

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// The other half of a transaction is the side that sends it. What these tests hold is the
// queue's contract: a local action must reach the peer that shares the room, a peer that
// is not there must be retried with the same transaction id rather than a new one, and a
// batch must not grow past what the specification lets a receiver answer.
//
// The tests seed membership rows directly where a real cross-server join would be needed,
// because the join handshake is a later step of the plan. What is under test here is the
// delivery, and the seeding says so rather than pretending the room is shared.

// mustDecodeObject reads a stored JSON document back into an object.
func mustDecodeObject(t *testing.T, document string) matrix.Object {
	t.Helper()
	decoded, err := matrix.DecodeCanonical([]byte(document))
	if err != nil {
		t.Fatalf("decoding %s: %v", document, err)
	}
	object, ok := decoded.(matrix.Object)
	if !ok {
		t.Fatalf("%s is not an object", document)
	}
	return object
}

// shareRoom records on one server that a user of the other server has joined one of its
// rooms, which is the fact the outbox uses to decide who to talk to.
func shareRoom(t *testing.T, s *server, roomID, userID string) {
	t.Helper()
	if err := s.db.MembershipRepo.Set(&persist.RoomMembership{
		RoomID:     roomID,
		UserID:     userID,
		Membership: matrix.MembershipJoin,
		EventID:    "$seed:" + s.name,
		SentTS:     time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("seeding the membership of %s on %s: %v", userID, s.name, err)
	}
}

// receipts reads what a server has been sent over federation, judged or not. The
// receipt row is the record of delivery; what the server decided about the event is a
// separate question, and answered by its outcome.
func receipts(t *testing.T, s *server) []*persist.ReceivedPDU {
	t.Helper()
	rows, err := s.db.ReceivedPDURepo.List(50)
	if err != nil {
		t.Fatalf("reading the receipts of %s: %v", s.name, err)
	}
	return rows
}

// messagesReceived lists what a server has been sent that is not a membership event. A join
// handshake puts the joiner's own event on the wire, so a count that includes it measures the
// guest list rather than the conversation.
func messagesReceived(t *testing.T, s *server) []*persist.ReceivedPDU {
	t.Helper()
	out := []*persist.ReceivedPDU{}
	for _, row := range receipts(t, s) {
		event, err := s.db.EventRepo.Get(row.EventID)
		if err != nil {
			t.Fatalf("reading the received event %s on %s: %v", row.EventID, s.name, err)
		}
		if event.Type == matrix.TypeMember {
			continue
		}
		out = append(out, row)
	}
	return out
}

// flush drains a server's outbox, which is what the background loop does on a timer.
func flush(t *testing.T, s *server) {
	t.Helper()
	if _, err := s.svc.FlushFederationOutbox(context.Background()); err != nil {
		t.Fatalf("flushing the outbox of %s: %v", s.name, err)
	}
}

// sendToDevice drives PUT /sendToDevice through the client API.
func sendToDevice(t *testing.T, s *server, sess *session, eventType, txnID string, messages map[string]map[string]any) {
	t.Helper()
	body := matrix.Object{"messages": map[string]map[string]any{}}
	byUser := map[string]map[string]any{}
	for user, byDevice := range messages {
		byUser[user] = byDevice
	}
	body["messages"] = byUser
	s.mustDo(http.StatusOK, http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/sendToDevice/%s/%s", eventType, txnID), sess.Token, body)
}

// TestAToDeviceMessageReachesARemoteDevice is the path an Olm handshake takes between two
// homeservers. Nothing else in the federation layer matters as much for whether users can
// read each other's messages: without it a local device and a remote device never open a
// session, and both sides see an unreadable room.
func TestAToDeviceMessageReachesARemoteDevice(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")

	sendToDevice(t, one, alice, matrix.TypeEncrypted, "olm-1", map[string]map[string]any{
		bob.UserID: {"*": matrix.Object{"algorithm": "m.olm.v1.curve25519-aes-sha2", "ciphertext": "opaque"}},
	})

	// Before the flush the message is this server's business only: it is queued, not
	// sent, and the local user's request has already succeeded.
	if count, err := two.db.ToDeviceRepo.Count(); err != nil || count != 0 {
		t.Errorf("%s received a to-device message before it was sent: %d", two.name, count)
	}

	flush(t, one)

	got := syncOnce(t, two, bob, "")
	if got.ToDevice == nil || len(got.ToDevice.Events) == 0 {
		t.Fatalf("%s delivered nothing to %s: %v", two.name, bob.UserID, got.ToDevice)
	}
	delivered := got.ToDevice.Events[0]
	if delivered.Type != matrix.TypeEncrypted {
		t.Errorf("the delivered message is %s, want the Olm envelope", delivered.Type)
	}
	if delivered.Sender != alice.UserID {
		t.Errorf("the delivered message comes from %s, want %s", delivered.Sender, alice.UserID)
	}
	content := decodeContent(t, delivered.Content)
	if content["ciphertext"] != "opaque" {
		t.Errorf("the delivered message carries %v, which is not the body that was sent", content)
	}

	// A retry of the same client transaction is one message, not two: the message id on
	// the wire is the client's transaction id, and the receiver keys its to-device store
	// on it.
	sendToDevice(t, one, alice, matrix.TypeEncrypted, "olm-1", map[string]map[string]any{
		bob.UserID: {"*": matrix.Object{"algorithm": "m.olm.v1.curve25519-aes-sha2", "ciphertext": "opaque"}},
	})
	flush(t, one)
	if count, err := two.db.ToDeviceRepo.Count(); err != nil || count != 1 {
		t.Errorf("a retried to-device message produced %d queued messages, want 1", count)
	}
}

// TestTypingAndReceiptsTravel checks the ephemeral side of the outbox: what somebody on
// another server is doing has to arrive, and it has to be attributed to a user of the
// server that sent it.
func TestTypingAndReceiptsTravel(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")
	roomID := shareRoomByJoin(t, one, two, alice, bob)

	message := one.send(alice, roomID, "read me")

	// Alice types, and the peer server of the room has to hear about it.
	one.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+alice.UserID,
		alice.Token, matrix.Object{"typing": true, "timeout": 20000})
	// Alice reads her own room; the receipt travels to the servers that share it, which
	// is where the person who wrote the message is.
	one.mustDo(http.StatusOK, http.MethodPost,
		"/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+message, alice.Token,
		matrix.Object{"ts": int64(1700000005000)})

	flush(t, one)

	typing, err := two.db.TypingRepo.List(roomID)
	if err != nil {
		t.Fatalf("reading the typing states on %s: %v", two.name, err)
	}
	found := false
	for _, row := range typing {
		if row.UserID == alice.UserID && row.Typing {
			found = true
		}
	}
	if !found {
		t.Errorf("%s does not know that %s is typing: %v", two.name, alice.UserID, typing)
	}

	receipt, err := two.db.ReceiptRepo.LatestForUser(alice.UserID, roomID, "m.read")
	if err != nil || receipt.EventID != message {
		t.Errorf("%s did not record the receipt for %s: %v, %v", two.name, message, receipt, err)
	}
	// A receipt says when a person read something. The clock of the server that relayed
	// it is not when they did, so the sending server's timestamp travels with it.
	local, err := one.db.ReceiptRepo.LatestForUser(alice.UserID, roomID, "m.read")
	if err != nil {
		t.Fatalf("reading the receipt on %s: %v", one.name, err)
	}
	if receipt.SentTS != local.SentTS {
		t.Errorf("%s records the receipt at %d and %s at %d: the timestamp did not survive the trip",
			one.name, local.SentTS, two.name, receipt.SentTS)
	}

	// Alice stops typing; the indicator has to go away on the other side too, rather
	// than lingering until a timeout nobody set there.
	one.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+alice.UserID,
		alice.Token, matrix.Object{"typing": false})
	flush(t, one)
	typing, err = two.db.TypingRepo.List(roomID)
	if err != nil {
		t.Fatalf("reading the typing states again: %v", err)
	}
	for _, row := range typing {
		if row.UserID == alice.UserID && row.Typing {
			t.Errorf("%s still shows %s typing after they stopped", two.name, alice.UserID)
		}
	}

	// A newer receipt replaces the older one on the peer as well: the ordering rule is
	// the local server's, and the peer only ever sees what passes it.
	newest := one.send(alice, roomID, "newest")
	one.mustDo(http.StatusOK, http.MethodPost,
		"/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+newest, alice.Token,
		matrix.Object{"ts": int64(1700000009000)})
	flush(t, one)
	if receipt, err := two.db.ReceiptRepo.LatestForUser(alice.UserID, roomID, "m.read"); err != nil || receipt.EventID != newest {
		t.Errorf("%s kept the old receipt after a newer one arrived: %v, %v", two.name, receipt, err)
	}
}

// TestLocalEventsTravelToThePeersOfARoom checks that a message sent here is delivered
// there in the form the room version defines, and that both servers name it the same
// thing - which is the property the whole event-graph work was for.
func TestLocalEventsTravelToThePeersOfARoom(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")
	// The join is what gives the other server its room row, its copy of the state and its
	// membership rows - including which room version the events it is sent belong to. Seeding
	// those rows would have tested the seed.
	roomID := shareRoomByJoin(t, one, two, alice, bob)

	sent := one.send(alice, roomID, "across the wire")
	flush(t, one)

	received := messagesReceived(t, two)
	if len(received) != 1 {
		t.Fatalf("%s received %d messages, want the one message", two.name, len(received))
	}
	if received[0].EventID != sent {
		t.Errorf("%s names the event %s, but the sending server named it %s: the two servers disagree about the id",
			two.name, received[0].EventID, sent)
	}
	if received[0].RoomID != roomID || received[0].Origin != one.name {
		t.Errorf("the received event is %s from %s, want %s from %s",
			received[0].RoomID, received[0].Origin, roomID, one.name)
	}
	// The event was judged as it arrived, and this server's answer is that it stands.
	// The room's state was never handed to two - no join handshake put it there - but the
	// event cites its own authority, two asks for what it does not hold, and what comes
	// back is signature-checked, hash-checked and kept as an outlier. A room whose state
	// this server has not been told about is no longer a reason to distrust an event that
	// names its authority out loud.
	if received[0].Outcome != service.OutcomeStored {
		t.Errorf("%s decided %q for the event, want %q: %s",
			two.name, received[0].Outcome, service.OutcomeStored, received[0].Reason)
	}

	// The event that arrives carries no event id of its own, because the room version
	// does not define one: an id inside the event would be part of what its own hash is
	// computed over.
	if _, found := mustDecodeObject(t, received[0].JSON)["event_id"]; found {
		t.Errorf("the delivered event carries an event id, which the room version does not define: %s", received[0].JSON)
	}

	// A second flush sends nothing: the event has been acknowledged, so a peer must not
	// be asked the same question again.
	before := len(receipts(t, two))
	flush(t, one)
	after := len(receipts(t, two))
	if before != after {
		t.Errorf("a second flush delivered the event again: %d became %d", before, after)
	}
}

// TestTheOutboxRetriesTheSameTransaction is the specification's delivery rule: a sender
// retries the same transaction until it gets an answer, and does not start a second
// transaction to the same server while one is outstanding. A receiver can only answer a
// retry with the answer it gave before if the retry is recognisably the same request.
func TestTheOutboxRetriesTheSameTransaction(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")

	// The peer is unreachable before anything is delivered.
	sendToDevice(t, one, alice, matrix.TypeRoomKey, "lost-1", map[string]map[string]any{
		bob.UserID: {bob.DeviceID: matrix.Object{"session_id": "sess-lost"}},
	})
	c.blackout(two.name)

	flush(t, one)
	first, err := one.db.OutboxRepo.Outstanding(two.name)
	if err != nil {
		t.Fatalf("%s has no transaction in flight after a failed attempt: %v", one.name, err)
	}
	if first.TxnID == "" {
		t.Fatal("the transaction in flight has no id, so a retry could not be the same request")
	}

	// Every further attempt re-sends that id; it never opens a second transaction to the
	// same server while one is outstanding.
	for i := 0; i < 3; i++ {
		// The backoff has to be stepped over for the retry to be due again, which is
		// what the retry clock is for.
		if err := one.db.OutboxRepo.Retry(two.name, first.TxnID, time.Now().UnixMilli()-1000, "carry on"); err != nil {
			t.Fatalf("stepping the retry clock: %v", err)
		}
		flush(t, one)
	}
	again, err := one.db.OutboxRepo.Outstanding(two.name)
	if err != nil {
		t.Fatalf("reading the transaction in flight: %v", err)
	}
	if again.TxnID != first.TxnID {
		t.Errorf("the retry started a new transaction %s instead of retrying %s", again.TxnID, first.TxnID)
	}
	if again.Tries < 3 {
		t.Errorf("the transaction has been attempted %d times after four flushes, want at least 3", again.Tries)
	}
	if again.LastErr == "" {
		t.Error("the failed attempts recorded no reason, which leaves an operator guessing")
	}

	// The queue behind it waits: nothing else is packed while one transaction is
	// outstanding for that destination.
	if count, err := one.db.OutboxRepo.Count(); err != nil || count < 1 {
		t.Errorf("the queued item vanished from %s's outbox while the transaction was in flight: %d", one.name, count)
	}

	// When the peer comes back the same transaction finally lands, and the queue empties.
	c.restore(two.name)
	if err := one.db.OutboxRepo.Retry(two.name, first.TxnID, time.Now().UnixMilli()-1000, "carry on"); err != nil {
		t.Fatalf("clearing the backoff: %v", err)
	}
	flush(t, one)
	if _, err := one.db.OutboxRepo.Outstanding(two.name); err == nil {
		t.Errorf("%s still holds a transaction in flight after the peer came back", one.name)
	}
	if count, err := two.db.ToDeviceRepo.Count(); err != nil || count != 1 {
		t.Errorf("after the retry the peer holds %d to-device messages, want the one that was queued", count)
	}
}

// TestTheOutboxRunsByItself checks the loop a real deployment depends on: nobody has to
// ask for a flush, and a message does not wait for a request to trigger one.
func TestTheOutboxRunsByItself(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")

	stop := one.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stop()

	sendToDevice(t, one, alice, matrix.TypeDummy, "loop-1", map[string]map[string]any{
		bob.UserID: {bob.DeviceID: matrix.Object{}},
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		count, err := two.db.ToDeviceRepo.Count()
		if err != nil {
			t.Fatalf("counting the delivered messages: %v", err)
		}
		if count == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("%s never received what %s queued: the background loop did not deliver it", two.name, one.name)
}

// TestTheQueueRespectsTheTransactionLimits checks that a backlog is delivered in
// transactions a receiver is allowed to answer, rather than in one enormous request that
// the peer refuses as a whole.
func TestTheQueueRespectsTheTransactionLimits(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	bob := two.register("bob", "rubberduck")

	// The items are queued directly because building 60 legitimate to-device messages
	// through the client API would test the client API again rather than the packing.
	items := make([]*persist.OutboxItem, 0, 130)
	for i := 0; i < 130; i++ {
		items = append(items, &persist.OutboxItem{
			Destination: two.name,
			Kind:        "edu",
			EdgeID:      fmt.Sprintf("bulk-%d:%s", i, two.name),
			Payload: `{"edu_type":"m.direct_to_device","content":{"message_id":"bulk-` +
				fmt.Sprint(i) + `","sender":"@carol:` + one.name + `","type":"m.dummy","messages":{"` +
				bob.UserID + `":{"` + bob.DeviceID + `":{}}}}}`,
		})
	}
	if added, err := one.db.OutboxRepo.Append(items...); err != nil || added != 130 {
		t.Fatalf("queueing the backlog: %d added, %v", added, err)
	}

	flush(t, one)
	// One pass carries at most the EDU limit, so the rest stays queued for the next.
	remaining, err := one.db.OutboxRepo.Count()
	if err != nil {
		t.Fatalf("counting what is left: %v", err)
	}
	if remaining != 30 {
		t.Errorf("after one flush %d of 130 items are still queued, want the 30 that did not fit in one transaction", remaining)
	}
	if delivered, err := two.db.ToDeviceRepo.Count(); err != nil || delivered != 100 {
		t.Errorf("%s received %d messages in one transaction, want the 100 a transaction may carry", two.name, delivered)
	}

	// The rest arrive on the next pass, in order, and nothing is lost in between.
	flush(t, one)
	if remaining, err := one.db.OutboxRepo.Count(); err != nil || remaining != 0 {
		t.Errorf("after the second flush %d items are still queued, want none", remaining)
	}
	if delivered, err := two.db.ToDeviceRepo.Count(); err != nil || delivered != 130 {
		t.Errorf("%s holds %d messages at the end, want all 130", two.name, delivered)
	}
}

// TestARoomCanExcludeAServer checks that an exclusion written in a client is enforced by
// the servers, in both directions. An ACL that only one server obeys is a rule the
// excluded server does not know about, and the room has no way to take it back once it is
// posted - the excluded server's own software decides whether the room's decision means
// anything.
func TestARoomCanExcludeAServer(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")
	roomID := shareRoomByJoin(t, one, two, alice, bob)

	// The message sent before the exclusion travels, because at the time it was sent the
	// room allowed the server.
	before := one.send(alice, roomID, "before the door closed")
	flush(t, one)
	if got := messagesReceived(t, two); len(got) != 1 {
		t.Fatalf("the peer received %d messages before the exclusion, want 1", len(got))
	}

	one.mustDo(http.StatusOK, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/state/"+matrix.TypeServerACL, alice.Token,
		matrix.Object{"deny": []any{two.name}})
	flush(t, one)

	// The exclusion itself arrives. A ban that the banned server never hears about leaves
	// that server relaying the room to everyone else forever.
	if kept, err := two.db.ReceivedPDURepo.Get(before); err != nil || kept == nil {
		t.Errorf("%s forgot the event it had already accepted: %v", two.name, err)
	}
	pending := receipts(t, two)
	sawACL := false
	for _, row := range pending {
		if strings.Contains(row.JSON, `"m.room.server_acl"`) {
			sawACL = true
		}
	}
	if !sawACL {
		t.Error("the exclusion never reached the server it excludes")
	}

	// What comes after it does not travel.
	closed := one.send(alice, roomID, "after the door closed")
	flush(t, one)
	if count, err := one.db.OutboxRepo.Count(); err != nil || count != 0 {
		t.Errorf("%s still has %d items queued for a server the room excluded", one.name, count)
	}
	for _, row := range receipts(t, two) {
		if row.EventID == closed {
			t.Errorf("%s accepted an event the room sent after it excluded %s", two.name, two.name)
		}
	}
	// What is there is what was sent before the door closed, plus the exclusion itself: the
	// membership events of the join are counted as what they are, since the handshake put them
	// on the wire before any rule about the server existed.
	if got := messagesReceived(t, two); len(got) != 2 {
		t.Errorf("%s holds %d messages, want the one sent before the exclusion and the exclusion itself", two.name, len(got))
	}

	// The other direction: a server the room excludes cannot push events into it. The
	// event is signed by the excluded server, so this is not a question of whether the
	// signature checks - it does.
	key, err := two.svc.SigningKey(context.Background())
	if err != nil {
		t.Fatalf("the excluded server's signing key: %v", err)
	}
	pushed := matrix.Object{
		"auth_events":      []any{},
		"content":          matrix.Object{"body": "let me back in"},
		"depth":            int64(2),
		"origin_server_ts": int64(time.Now().UnixMilli()),
		"prev_events":      []any{},
		"room_id":          roomID,
		"sender":           "@mole:" + two.name,
		"type":             "m.room.message",
	}
	signed, err := eventcrypto.HashAndSign(pushed, "11", key.Private, two.name, key.ID)
	if err != nil {
		t.Fatalf("signing the pushed event: %v", err)
	}
	eventID, err := eventcrypto.EventID(signed, "11")
	if err != nil {
		t.Fatalf("naming the pushed event: %v", err)
	}
	results, apiError := sendTransaction(t, two, one.name, "intrude-1", matrix.Object{
		"origin":           two.name,
		"origin_server_ts": int64(time.Now().UnixMilli()),
		"pdus":             []any{signed},
	})
	if apiError != nil {
		t.Fatalf("the excluded server was refused: %s", apiError.Message)
	}
	byID, _ := results["pdus"].(map[string]any)
	if _, named := byID[eventID]; !named {
		t.Errorf("the pushed event is answered under a different name: %v", results)
	}
	// It is answered without an error - a refusal would be retried, and a retry is how an
	// exclusion becomes a flood - but it is not queued.
	if stored, err := one.db.ReceivedPDURepo.Get(eventID); err == nil && stored != nil {
		t.Errorf("%s accepted an event from a server the room excluded", one.name)
	}
	if got := receipts(t, one); len(got) != 0 {
		t.Errorf("%s took in %d events from the server it excludes", one.name, len(got))
	}
}

// TestARoomNobodyHereIsIn checks the two answers a server can give an event for a room it
// has never heard of. A message is refused, because the room version - and with it the
// event's whole identity - cannot be checked. An invitation is taken in, because an
// invite is the only way a server ever learns of a room: refusing it would make the first
// step of joining impossible.
func TestARoomNobodyHereIsIn(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	roomID := one.createRoom(alice, map[string]any{"name": "Unknown to two"})

	// Server two has never heard of the room, and nothing is told to it.
	message := one.send(alice, roomID, "into the void")
	flush(t, one)
	if got := receipts(t, two); len(got) != 0 {
		t.Errorf("%s took in %d events for a room it is not in", two.name, len(got))
	}
	// The sending server is not told the transaction failed: the events are answered
	// per event, and the sending server's queue is empty either way.
	if waiting, err := one.db.OutboxRepo.Count(); err != nil || waiting != 0 {
		t.Errorf("%s is still retrying a peer that answered: %d", one.name, waiting)
	}
	_ = message

	// An invitation naming a user of the receiving server is taken in, before the room is
	// known. The event is built by the sending server's own signing key, which is what a
	// peer has to go on.
	key, err := one.svc.SigningKey(context.Background())
	if err != nil {
		t.Fatalf("the signing key of the sending server: %v", err)
	}
	invite := matrix.Object{
		"auth_events":      []any{},
		"content":          matrix.Object{"membership": "invite"},
		"depth":            int64(1),
		"origin_server_ts": int64(time.Now().UnixMilli()),
		"prev_events":      []any{},
		"room_id":          roomID,
		"sender":           alice.UserID,
		"state_key":        "@carol:" + two.name,
		"type":             matrix.TypeMember,
	}
	signed, err := eventcrypto.HashAndSign(invite, "11", key.Private, one.name, key.ID)
	if err != nil {
		t.Fatalf("signing the invitation: %v", err)
	}
	eventID, err := eventcrypto.EventID(signed, "11")
	if err != nil {
		t.Fatalf("naming the invitation: %v", err)
	}
	results, apiError := sendTransaction(t, one, two.name, "invite-1", matrix.Object{
		"origin":           one.name,
		"origin_server_ts": int64(time.Now().UnixMilli()),
		"pdus":             []any{signed},
	})
	if apiError != nil {
		t.Fatalf("the invitation was refused: %s", apiError.Message)
	}
	byID, _ := results["pdus"].(map[string]any)
	if entry, named := byID[eventID]; !named {
		t.Errorf("the invitation is answered under a different name: %v", results)
	} else if refused, ok := entry.(map[string]any); ok && len(refused) > 0 {
		t.Errorf("the invitation was refused: %v", refused)
	}
	stored, err := two.db.ReceivedPDURepo.Get(eventID)
	if err != nil || stored == nil {
		t.Fatalf("%s did not keep the invitation for one of its users: %v", two.name, err)
	}
	if stored.RoomVersion == "" {
		t.Errorf("%s kept the invitation without recording the room version its id was named under", two.name)
	}
	if stored.RoomID != roomID || stored.Origin != one.name {
		t.Errorf("the kept invitation names %s from %s", stored.RoomID, stored.Origin)
	}

	// An invitation for somebody else's user is not an invitation this server may act on.
	other := matrix.Object{
		"auth_events":      []any{},
		"content":          matrix.Object{"membership": "invite"},
		"depth":            int64(1),
		"origin_server_ts": int64(time.Now().UnixMilli()),
		"prev_events":      []any{},
		"room_id":          roomID,
		"sender":           alice.UserID,
		"state_key":        "@dave:" + one.name,
		"type":             matrix.TypeMember,
	}
	signedOther, err := eventcrypto.HashAndSign(other, "11", key.Private, one.name, key.ID)
	if err != nil {
		t.Fatalf("signing the second invitation: %v", err)
	}
	otherID, err := eventcrypto.EventID(signedOther, "11")
	if err != nil {
		t.Fatalf("naming the second invitation: %v", err)
	}
	if _, apiError := sendTransaction(t, one, two.name, "invite-2", matrix.Object{
		"origin":           one.name,
		"origin_server_ts": int64(time.Now().UnixMilli()),
		"pdus":             []any{signedOther},
	}); apiError != nil {
		t.Fatalf("the second transaction was refused at the door: %v", apiError)
	}
	if stored, err := two.db.ReceivedPDURepo.Get(otherID); err == nil && stored != nil {
		t.Errorf("%s kept an invitation for a user of another server", two.name)
	}
}

// TestTwoSendersSharingATransactionIDBothGetTheirMessage is about who a queued to-device message
// belongs to. A client's transaction id is unique within one client session, not across a
// homeserver: every device starts counting at 0, and a server that keys its queue on the id alone
// lets one user's Olm handshake cancel another's. Complement's own to-device test uses the id "1"
// in each of its subtests against one deployment, which is how this was found.
func TestTwoSendersSharingATransactionIDBothGetTheirMessage(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	carol := one.register("carol", "wonderland")
	bob := two.register("bob", "rubberduck")

	sendToDevice(t, one, alice, matrix.TypeEncrypted, "1", map[string]map[string]any{
		bob.UserID: {"*": matrix.Object{"ciphertext": "from-alice"}},
	})
	sendToDevice(t, one, carol, matrix.TypeEncrypted, "1", map[string]map[string]any{
		bob.UserID: {"*": matrix.Object{"ciphertext": "from-carol"}},
	})
	flush(t, one)

	count, err := two.db.ToDeviceRepo.Count()
	if err != nil {
		t.Fatalf("reading the to-device queue of %s: %v", two.name, err)
	}
	if count != 2 {
		t.Fatalf("%s holds %d to-device messages, want the two that were sent (%v)", two.name, count, err)
	}

	// The same message, offered twice by the same client, is still one message: the
	// de-duplication is per sender and id, not switched off.
	sendToDevice(t, one, alice, matrix.TypeEncrypted, "1", map[string]map[string]any{
		bob.UserID: {"*": matrix.Object{"ciphertext": "from-alice"}},
	})
	flush(t, one)
	if again, err := two.db.ToDeviceRepo.Count(); err != nil || again != 2 {
		t.Errorf("%s holds %d to-device messages after a repeat, want the two it was given (%v)", two.name, again, err)
	}
}
