package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// A transaction is what live activity looks like on the wire: a signed batch named by a
// transaction id, carrying events and ephemeral messages. These tests drive that door
// from one Lattice to another, because the properties that matter about it - a retry that
// must not apply twice, a peer that may only speak for its own users, a batch that must
// not be answerable as a whole - only exist between two servers.

// sendTransaction is one server handing a transaction to another over the production
// path: the transport signs the request, the receiver verifies it, and the answer is the
// per-event result.
func sendTransaction(t *testing.T, from *server, destination, txnID string, body matrix.Object) (matrix.Object, *matrix.Error) {
	t.Helper()
	answer, err := from.peer().Put(context.Background(), destination, "/_matrix/federation/v1/send/"+txnID, body)
	if err != nil {
		if failure, ok := err.(*matrix.Error); ok {
			return nil, failure
		}
		t.Fatalf("sending the transaction to %s: %v", destination, err)
	}
	return answer, nil
}

// transactionBody builds a transaction body with the shape the specification asks for.
func transactionBody(origin string, pdus, edus matrix.Array) matrix.Object {
	body := matrix.Object{
		"origin":           origin,
		"origin_server_ts": int64(1700000000000),
		"pdus":             pdus,
	}
	if edus != nil {
		body["edus"] = edus
	}
	return body
}

// syncOnce pulls a sync response from a server.
func syncOnce(t *testing.T, s *server, sess *session, since string) matrix.SyncResponse {
	t.Helper()
	path := "/_matrix/client/v3/sync?timeout=0"
	if since != "" {
		path += "&since=" + since
	}
	got := s.mustDo(http.StatusOK, http.MethodGet, path, sess.Token, nil)
	var response matrix.SyncResponse
	if err := json.Unmarshal(got.Raw, &response); err != nil {
		t.Fatalf("decoding the sync of %s: %v (%s)", s.name, err, got.Raw)
	}
	return response
}

// ephemeralOfType returns the ephemeral events of one type from a joined room.
func ephemeralOfType(room *matrix.JoinedRoom, eventType string) []matrix.Event {
	if room == nil || room.Ephemeral == nil {
		return nil
	}
	var out []matrix.Event
	for _, event := range room.Ephemeral.Events {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

// TestSendToDeviceCrossesTheWire is the E2EE case that decides whether a federation
// setup works at all: a room key addressed to a device on another server. Refusing the
// message, or losing it, locks both sides out of the conversation with nothing on either
// side to show for it.
func TestSendToDeviceCrossesTheWire(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	bob := two.register("bob", "rubberduck")

	edus := matrix.Array{matrix.Object{
		"edu_type": "m.direct_to_device",
		"content": matrix.Object{
			"message_id": "olm-handshake-1",
			"type":       matrix.TypeRoomKey,
			"sender":     "@alice:" + one.name,
			"messages": matrix.Object{
				bob.UserID: matrix.Object{
					bob.DeviceID: matrix.Object{"algorithm": "m.megolm.v1.aes-sha2", "session_id": "sess-1"},
					// The wildcard targets every device of the user, which is how a
					// key request reaches a device the sender has never met.
					"*": matrix.Object{"algorithm": "m.forwarded_room_key", "session_id": "sess-2"},
				},
			},
		},
	}}
	answer, failure := sendTransaction(t, one, two.name, "todevice-1", transactionBody(one.name, matrix.Array{}, edus))
	if failure != nil {
		t.Fatalf("%s refused the transaction: %d %s %s", two.name, failure.Status, failure.Code, failure.Message)
	}
	if pdus, ok := answer["pdus"].(matrix.Object); !ok || len(pdus) != 0 {
		t.Errorf("the answer to an empty transaction is %v, which claims something went wrong", answer["pdus"])
	}

	got := syncOnce(t, two, bob, "")
	if got.ToDevice == nil || len(got.ToDevice.Events) == 0 {
		t.Fatalf("%s delivered nothing to %s: %v", two.name, bob.UserID, got.ToDevice)
	}
	bySession := map[string]matrix.Event{}
	for _, event := range got.ToDevice.Events {
		if event.Type != matrix.TypeRoomKey {
			continue
		}
		content := decodeContent(t, event.Content)
		if session, ok := content["session_id"].(string); ok {
			bySession[session] = event
		}
		if event.Sender != "@alice:"+one.name {
			t.Errorf("the message arrives from %s, which is not who sent it", event.Sender)
		}
	}
	if len(bySession) != 2 {
		t.Errorf("%s delivered %d messages to %s, want the direct one and the wildcard one",
			two.name, len(bySession), bob.UserID)
	}
}

// TestARetriedTransactionIsAppliedOnce is the reason the transaction id exists. A sender
// retries until it gets a 200; a receiver that processes the batch again applies every
// message in it twice, and there is no way to take back a duplicated event in a room.
func TestARetriedTransactionIsAppliedOnce(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	bob := two.register("bob", "rubberduck")

	edu := func(messageID string) matrix.Array {
		return matrix.Array{matrix.Object{
			"edu_type": "m.direct_to_device",
			"content": matrix.Object{
				"message_id": messageID,
				"type":       matrix.TypeDummy,
				"sender":     "@alice:" + one.name,
				"messages":   matrix.Object{bob.UserID: matrix.Object{bob.DeviceID: matrix.Object{}}},
			},
		}}
	}
	first, failure := sendTransaction(t, one, two.name, "retry-me", transactionBody(one.name, matrix.Array{}, edu("first-message")))
	if failure != nil {
		t.Fatalf("the first attempt failed: %v", failure)
	}
	// The same transaction id, sent again, with different content inside: the sender is
	// retrying, so what it says the second time does not count.
	second, failure := sendTransaction(t, one, two.name, "retry-me", transactionBody(one.name, matrix.Array{}, edu("second-message")))
	if failure != nil {
		t.Fatalf("the retry failed: %v", failure)
	}
	if !jsonRoundTripEqual(first, second) {
		t.Errorf("the retry was answered with %v, but the first attempt was answered with %v", second, first)
	}

	got := syncOnce(t, two, bob, "")
	delivered := 0
	if got.ToDevice != nil {
		for _, event := range got.ToDevice.Events {
			if event.Type == matrix.TypeDummy {
				delivered++
			}
		}
	}
	if delivered != 1 {
		t.Errorf("%s delivered %d copies of the retry: the transaction was processed twice", two.name, delivered)
	}

	// A different transaction id is a different transaction, and is applied.
	if _, failure := sendTransaction(t, one, two.name, "retry-me-too", transactionBody(one.name, matrix.Array{}, edu("third-message"))); failure != nil {
		t.Fatalf("the follow-up transaction failed: %v", failure)
	}
	again := syncOnce(t, two, bob, got.NextBatch)
	if again.ToDevice == nil || len(again.ToDevice.Events) != 1 {
		t.Errorf("a new transaction id delivered %v, want the new message", again.ToDevice)
	}
}

// TestTypingAndReceiptsFromAPeerAreRecorded checks the two ephemeral sinks that reach a
// client: somebody on another server typing, and having read something.
func TestTypingAndReceiptsFromAPeerAreRecorded(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	bob := two.register("bob", "rubberduck")
	roomID := two.createRoom(bob, map[string]any{"name": "Shared"})

	alice := "@alice:" + one.name
	edus := matrix.Array{
		matrix.Object{
			"edu_type": "m.typing",
			"content":  matrix.Object{"room_id": roomID, "user_id": alice, "typing": true},
		},
		matrix.Object{
			"edu_type": "m.receipt",
			"content": matrix.Object{roomID: matrix.Object{
				"m.read": matrix.Object{alice: matrix.Object{
					"data":      matrix.Object{"ts": int64(1700000001000)},
					"event_ids": []any{"$read:example.org"},
				}},
			}},
		},
	}
	if _, failure := sendTransaction(t, one, two.name, "ephemeral-1", transactionBody(one.name, matrix.Array{}, edus)); failure != nil {
		t.Fatalf("the ephemeral transaction was refused: %d %s", failure.Status, failure.Message)
	}

	joined := syncOnce(t, two, bob, "").Rooms.Join
	room, ok := joined[roomID]
	if !ok {
		t.Fatalf("%s lost the room %s from sync", two.name, roomID)
	}
	typing := ephemeralOfType(room, matrix.TypeTyping)
	if len(typing) != 1 {
		t.Fatalf("%s reports %d typing events, want the one from %s", two.name, len(typing), alice)
	}
	content := decodeContent(t, typing[0].Content)
	users, _ := content["user_ids"].([]any)
	if len(users) == 0 || users[0] != alice {
		t.Errorf("the typing event names %v, want %s", content["user_ids"], alice)
	}
	receipts := ephemeralOfType(room, matrix.TypeReceipt)
	if len(receipts) != 1 {
		t.Fatalf("%s reports %d receipt events, want the one from %s", two.name, len(receipts), alice)
	}
	body := decodeContent(t, receipts[0].Content)
	// A client reads receipts keyed by the event that was read - the room is already
	// known, because the event arrived in a sync of that room.
	perEvent, ok := body["$read:example.org"].(map[string]any)
	if !ok {
		t.Fatalf("the receipt event covers %v, want the event that was read", body)
	}
	public, _ := perEvent["m.read"].(map[string]any)
	if _, ok := public[alice]; !ok {
		t.Errorf("the receipt of %s is not in the sync event: %v", alice, perEvent)
	}

	// The receipt is also visible through the receipt listing, which is where a client
	// that missed the ephemeral event looks.
	listing := two.mustDo(http.StatusOK, http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/receipts", bob.Token, nil)
	if !strings.Contains(string(listing.Raw), alice) {
		t.Errorf("the receipt listing of %s does not mention %s: %s", roomID, alice, listing.Raw)
	}
}

// TestAPeerCannotSpeakForAnotherServersUsers pins a rule that is easy to skip and
// expensive to skip: a server reports for its own users. Accepting a typing indicator, a
// receipt or a device list for somebody else's user turns any peer into a forger of
// another server's activity.
func TestAPeerCannotSpeakForAnotherServersUsers(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	bob := two.register("bob", "rubberduck")
	roomID := two.createRoom(bob, map[string]any{"name": "Watched"})

	edus := matrix.Array{
		matrix.Object{"edu_type": "m.typing", "content": matrix.Object{
			"room_id": roomID, "user_id": "@mallory:three.example", "typing": true,
		}},
		matrix.Object{"edu_type": "m.receipt", "content": matrix.Object{roomID: matrix.Object{
			"m.read": matrix.Object{"@mallory:three.example": matrix.Object{
				"data": matrix.Object{"ts": int64(1700000002000)}, "event_ids": []any{"$x:example.org"},
			}},
		}}},
		matrix.Object{"edu_type": "m.device_list_update", "content": matrix.Object{
			"user_id": "@mallory:three.example", "device_id": "MALLORY", "stream_id": int64(1),
		}},
	}
	// The transaction is accepted: a peer that says one thing it should not is not
	// arguing about the connection, and refusing the whole batch would drop the
	// legitimate messages beside it.
	if _, failure := sendTransaction(t, one, two.name, "forgery-1", transactionBody(one.name, matrix.Array{}, edus)); failure != nil {
		t.Fatalf("the transaction was refused: %d %s", failure.Status, failure.Message)
	}

	room := syncOnce(t, two, bob, "").Rooms.Join[roomID]
	if got := ephemeralOfType(room, matrix.TypeTyping); len(got) != 0 {
		t.Errorf("%s records typing by a user %s does not speak for: %v", two.name, one.name, got)
	}
	if got := ephemeralOfType(room, matrix.TypeReceipt); len(got) != 0 {
		t.Errorf("%s records a receipt by a user %s does not speak for: %v", two.name, one.name, got)
	}
	devices, err := two.db.RemoteDeviceRepo.ForUser("@mallory:three.example")
	if err != nil {
		t.Fatalf("reading the device cache: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("%s cached devices announced by a server that does not own the user: %v", two.name, devices)
	}
}

// TestATransactionClaimingAnotherOriginIsRefused checks the one thing the signature is
// for: the transaction may not name a sender other than the server whose key signed it.
func TestATransactionClaimingAnotherOriginIsRefused(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()

	_, failure := sendTransaction(t, one, two.name, "lies-1",
		transactionBody("three.example", matrix.Array{}, nil))
	if failure == nil {
		t.Fatalf("%s accepted a transaction signed by %s but claiming to be three.example", two.name, one.name)
	}
	if failure.Status != http.StatusUnauthorized || failure.Code != matrix.ErrCodeUnauthorized {
		t.Errorf("claiming another origin: got %d %s, want 401 %s", failure.Status, failure.Code, matrix.ErrCodeUnauthorized)
	}
}

// TestOversizedTransactionsAreRefused pins the envelope limits at the boundary: the
// specification counts PDUs and EDUs per transaction, and a receiver that quietly accepts
// more has agreed to re-send and re-process a bigger batch because of one bad event.
func TestOversizedTransactionsAreRefused(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()

	pdus := make(matrix.Array, federationMaxPDUs+1)
	for i := range pdus {
		pdus[i] = matrix.Object{
			"event_id": fmt.Sprintf("$event%d:example.org", i),
			"room_id":  "!somewhere:example.org",
			"type":     matrix.TypeMessage,
			"sender":   "@someone:example.org",
			"content":  matrix.Object{},
		}
	}
	_, failure := sendTransaction(t, one, two.name, "too-many-pdus", transactionBody(one.name, pdus, nil))
	if failure == nil || failure.Code != matrix.ErrCodeInvalidParam {
		t.Errorf("%d PDUs in one transaction: got %v, want M_INVALID_PARAM", len(pdus), failure)
	}

	big := matrix.Array{matrix.Object{
		"event_id": "$big:example.org",
		"room_id":  "!somewhere:example.org",
		"type":     matrix.TypeMessage,
		"sender":   "@someone:example.org",
		"content":  matrix.Object{"body": strings.Repeat("x", 70000)},
	}}
	_, failure = sendTransaction(t, one, two.name, "oversized-pdu", transactionBody(one.name, big, nil))
	if failure == nil || failure.Code != matrix.ErrCodeInvalidParam {
		t.Errorf("an oversized PDU: got %v, want M_INVALID_PARAM", failure)
	}

	// A transaction at the limit is not refused: a limit that bites early is a bug
	// nobody reports until a room with real traffic starts losing events.
	_, failure = sendTransaction(t, one, two.name, "at-the-limit",
		transactionBody(one.name, pdus[:federationMaxPDUs], nil))
	if failure != nil {
		t.Errorf("%d PDUs in one transaction was refused: %d %s", federationMaxPDUs, failure.Status, failure.Message)
	}
}

// TestUnknownEDUsAreIgnored checks that an ephemeral message this server does not
// understand is dropped rather than fatal: the specification offers no way to tell a peer
// "I do not implement that", and a server that refuses the batch around it breaks its
// peers whenever they learn something new.
func TestUnknownEDUsAreIgnored(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	bob := two.register("bob", "rubberduck")

	edus := matrix.Array{
		matrix.Object{"edu_type": "m.bogus_experimental", "content": matrix.Object{"whatever": true}},
		matrix.Object{"edu_type": "m.typing", "content": "not an object at all"},
		matrix.Object{"edu_type": "m.presence", "content": matrix.Object{"push": matrix.Array{}}},
	}
	if _, failure := sendTransaction(t, one, two.name, "unknown-edus", transactionBody(one.name, matrix.Array{}, edus)); failure != nil {
		t.Fatalf("a transaction with unknown EDUs was refused: %d %s", failure.Status, failure.Message)
	}
	// The server is still itself afterwards.
	if got := two.mustDo(http.StatusOK, http.MethodGet, "/_matrix/federation/v1/version", "", nil); got.Body["server"] == nil {
		t.Errorf("%s stopped answering after the unknown EDUs: %s", two.name, got.Raw)
	}
	// And its client API is unaffected.
	syncOnce(t, two, bob, "")
}

// TestEventsAreQueuedAndAnswered checks the events side of the envelope: a PDU is
// answered per event, kept in the form it arrived in, and an event that arrives twice is
// stored once. Deciding whether it belongs in the room is the next step of the plan; what
// this step guarantees is that nothing is lost between the ack and the decision.
func TestEventsAreQueuedAndAnswered(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	bob := two.register("bob", "rubberduck")
	roomID := two.createRoom(bob, map[string]any{"name": "Their room"})

	pdu := matrix.Object{
		"room_id":          roomID,
		"type":             matrix.TypeMessage,
		"sender":           "@alice:" + one.name,
		"origin_server_ts": int64(1700000003000),
		"depth":            int64(9),
		"prev_events":      matrix.Array{},
		"auth_events":      matrix.Array{},
		"content":          matrix.Object{"msgtype": "m.text", "body": "from the other side"},
		"hashes":           matrix.Object{"sha256": "not-a-real-hash"},
		"signatures":       matrix.Object{one.name: matrix.Object{"ed25519:key": "not-a-real-signature"}},
	}
	first, failure := sendTransaction(t, one, two.name, "events-1", transactionBody(one.name, matrix.Array{pdu}, nil))
	if failure != nil {
		t.Fatalf("the event transaction was refused: %d %s %s", failure.Status, failure.Code, failure.Message)
	}
	pdus, ok := first["pdus"].(matrix.Object)
	if !ok || len(pdus) != 1 {
		t.Fatalf("the answer to one event is %v, which names no event", first["pdus"])
	}
	var eventID string
	for id, entry := range pdus {
		eventID = id
		if object, ok := entry.(matrix.Object); !ok || len(object) != 0 {
			t.Errorf("the event %s was answered with %v, which claims a problem", id, entry)
		}
	}
	if !strings.HasPrefix(eventID, "$") {
		t.Errorf("the event is answered under %q, which is not an event id", eventID)
	}
	// Room version 11 does not carry an event id, so the receiver derived it - and the
	// derivation is reproducible from the bytes that were sent.
	derived, err := derivedEventID(t, two, roomID, pdu)
	if err != nil {
		t.Errorf("the event id cannot be recomputed from the bytes that were sent: %v", err)
	} else if derived != eventID {
		t.Errorf("the event is answered under %s, but the bytes hash to %s", eventID, derived)
	}

	queued, err := two.db.ReceivedPDURepo.Count()
	if err != nil {
		t.Fatalf("counting the queued events: %v", err)
	}
	if queued != 1 {
		t.Fatalf("%s queued %d events, want the one that was sent", two.name, queued)
	}
	stored, err := two.db.ReceivedPDURepo.Get(eventID)
	if err != nil {
		t.Fatalf("reading the queued event: %v", err)
	}
	if stored.RoomID != roomID || stored.Origin != one.name || stored.TxnID != "events-1" {
		t.Errorf("the queued event is %s from %s in transaction %s, want %s from %s in events-1",
			stored.RoomID, stored.Origin, stored.TxnID, roomID, one.name)
	}
	if stored.Processed {
		t.Errorf("the queued event is marked processed with outcome %q, which has not been decided yet", stored.Outcome)
	}

	// The same event by another transaction id is the same event: two peers, or one
	// peer that lost its answer, will send it again, and a room that holds it twice has
	// two copies of one statement.
	if _, failure := sendTransaction(t, one, two.name, "events-2", transactionBody(one.name, matrix.Array{pdu}, nil)); failure != nil {
		t.Fatalf("the second delivery was refused: %d %s", failure.Status, failure.Message)
	}
	if count, err := two.db.ReceivedPDURepo.Count(); err != nil || count != 1 {
		t.Errorf("after the same event arrived twice, %d events are queued, want 1", count)
	}

	// An event that cannot be named - no id, and a room this server has never heard of,
	// so its room version is unknown - is answered with a problem rather than accepted
	// silently, because the answer is keyed by event id.
	unknown := matrix.Object{
		"room_id": "!unknown:elsewhere.example",
		"type":    matrix.TypeMessage,
		"sender":  "@someone:elsewhere.example",
		"content": matrix.Object{},
	}
	answer, failure := sendTransaction(t, one, two.name, "events-3", transactionBody(one.name, matrix.Array{unknown}, nil))
	if failure != nil {
		t.Fatalf("the unnamed event was refused at the envelope: %d %s", failure.Status, failure.Message)
	}
	entry, ok := answer["pdus"].(matrix.Object)[""]
	if !ok {
		t.Errorf("the unnamed event is answered with %v, which hides that it was refused", answer["pdus"])
		return
	}
	if result, ok := entry.(matrix.Object); !ok || result["error"] == nil {
		t.Errorf("the unnamed event is answered with %v, which claims it was handled", entry)
	}
}

// TestADeviceListUpdateIsCached checks the device list sink: a peer's announcement of a
// new device has to land in a cache this server can address an encrypted message to, and
// a gap in the sequence of updates has to be recorded rather than papered over.
func TestADeviceListUpdateIsCached(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := "@alice:" + one.name

	update := matrix.Object{
		"edu_type": "m.device_list_update",
		"content": matrix.Object{
			"user_id":             alice,
			"device_id":           "DEVICEA",
			"stream_id":           int64(5),
			"device_display_name": "Alice's phone",
			"keys": matrix.Object{
				"algorithms": []any{"m.megolm.v1.aes-sha2"},
				"device_id":  "DEVICEA",
				"keys": matrix.Object{
					"curve25519:DEVICEA": "3C5BFWi2Y8MaVvjM8M22DBmh24PmgR0nPvJOIArzgyI",
					"ed25519:DEVICEA":    "lEuiRJBit0IG6nUf5pUzWTUEsRVVe/HJkoKuEww9ULI",
				},
				"signatures": matrix.Object{alice: matrix.Object{"ed25519:DEVICEA": "a-signature"}},
				"user_id":    alice,
			},
		},
	}
	if _, failure := sendTransaction(t, one, two.name, "devices-1", transactionBody(one.name, matrix.Array{}, matrix.Array{update})); failure != nil {
		t.Fatalf("the device list update was refused: %d %s %s", failure.Status, failure.Code, failure.Message)
	}
	devices, err := two.db.RemoteDeviceRepo.ForUser(alice)
	if err != nil {
		t.Fatalf("reading the device cache: %v", err)
	}
	if len(devices) != 1 || devices[0].DeviceID != "DEVICEA" {
		t.Fatalf("%s cached %v for %s, want the one device that was announced", two.name, devices, alice)
	}
	// The key object is kept exactly as the peer published it: its signatures are the
	// user's own, and a cache that re-encodes them cannot be used to encrypt to.
	if !strings.Contains(devices[0].KeyJSON, `"ed25519:DEVICEA"`) {
		t.Errorf("the cached key object is %s", devices[0].KeyJSON)
	}
	if devices[0].DisplayName != "Alice's phone" {
		t.Errorf("the cached device is named %q", devices[0].DisplayName)
	}
	stream, err := two.db.DeviceStreamRepo.Get(alice)
	if err != nil {
		t.Fatalf("reading the device list position: %v", err)
	}
	if stream.StreamID != 5 || stream.GapSeen {
		t.Errorf("%s thinks %s's device list is at %d with a gap: %v", two.name, alice, stream.StreamID, stream)
	}

	// An update that builds on an id this server never received means an update was
	// lost. The cache is no longer trustworthy, and the row has to say so: an encrypted
	// message sent to a stale device list is a message that never arrives.
	next := update
	next["content"] = matrix.Object{
		"user_id":   alice,
		"device_id": "DEVICEB",
		"stream_id": int64(9),
		"prev_id":   []any{int64(7)},
	}
	if _, failure := sendTransaction(t, one, two.name, "devices-2", transactionBody(one.name, matrix.Array{}, matrix.Array{next})); failure != nil {
		t.Fatalf("the follow-up device list update was refused: %d %s", failure.Status, failure.Message)
	}
	stream, err = two.db.DeviceStreamRepo.Get(alice)
	if err != nil {
		t.Fatalf("reading the device list position again: %v", err)
	}
	if !stream.GapSeen {
		t.Errorf("%s did not record the gap in %s's device list", two.name, alice)
	}

	// A deletion takes the device out of the cache, so a message is not addressed to a
	// device that no longer exists.
	deletion := matrix.Object{
		"edu_type": "m.device_list_update",
		"content": matrix.Object{
			"user_id": alice, "device_id": "DEVICEA", "stream_id": int64(10), "deleted": true,
		},
	}
	if _, failure := sendTransaction(t, one, two.name, "devices-3", transactionBody(one.name, matrix.Array{}, matrix.Array{deletion})); failure != nil {
		t.Fatalf("the device deletion was refused: %d %s", failure.Status, failure.Message)
	}
	devices, err = two.db.RemoteDeviceRepo.ForUser(alice)
	if err != nil {
		t.Fatalf("reading the cache after the deletion: %v", err)
	}
	for _, device := range devices {
		if device.DeviceID == "DEVICEA" {
			t.Errorf("%s still holds the deleted device %s: %v", two.name, alice, devices)
		}
	}
}

// TestASigningKeyUpdateIsRecorded checks that cross-signing keys announced by a peer are
// stored as they arrived. Whether to trust them is the user's decision, made on their own
// devices; this server's job is to keep the evidence unchanged.
func TestASigningKeyUpdateIsRecorded(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := "@alice:" + one.name

	edu := matrix.Object{
		"edu_type": "m.signing_key_update",
		"content": matrix.Object{
			"user_id": alice,
			"master_key": matrix.Object{
				"usage":   []any{"master"},
				"user_id": alice,
				"keys":    matrix.Object{"ed25519:masterkey": "themasterpublickey"},
			},
			"self_signing_key": matrix.Object{
				"usage":      []any{"self_signing"},
				"user_id":    alice,
				"keys":       matrix.Object{"ed25519:selfkey": "theselfpublickey"},
				"signatures": matrix.Object{alice: matrix.Object{"ed25519:masterkey": "a-signature"}},
			},
		},
	}
	if _, failure := sendTransaction(t, one, two.name, "signing-1", transactionBody(one.name, matrix.Array{}, matrix.Array{edu})); failure != nil {
		t.Fatalf("the signing key update was refused: %d %s %s", failure.Status, failure.Code, failure.Message)
	}
	keys, err := two.db.CrossSigningRepo.List(alice)
	if err != nil {
		t.Fatalf("reading the cross-signing keys of %s: %v", alice, err)
	}
	if len(keys) != 2 {
		t.Fatalf("%s recorded %d cross-signing keys for %s, want master and self-signing", two.name, len(keys), alice)
	}
	for _, key := range keys {
		if key.UserID != alice || key.Data == "" {
			t.Errorf("the recorded key %s of %s is incomplete: %+v", key.KeyID, alice, key)
		}
	}
}

// decodeContent reads the content object of a sync event.
func decodeContent(t *testing.T, raw matrix.RawMessage) matrix.Object {
	t.Helper()
	object := matrix.Object{}
	if len(raw) == 0 {
		return object
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decoding the content %s: %v", raw, err)
	}
	return object
}

// federationMaxPDUs repeats the specification's limit rather than importing it, so a
// change to the constant has to be noticed here as well as in the unit suite.
const federationMaxPDUs = 50

// jsonRoundTripEqual compares two decoded answers by their encoded bytes.
func jsonRoundTripEqual(a, b matrix.Object) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

// derivedEventID recomputes the id of an event the way a receiver in this room's version
// has to, which is how a test can tell that a server kept the bytes it was sent.
func derivedEventID(t *testing.T, s *server, roomID string, event matrix.Object) (string, error) {
	t.Helper()
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		return "", fmt.Errorf("reading the room: %w", err)
	}
	return eventReference(room, event)
}

// eventReference derives the id of an event in a room's version: room version 3 and
// later name an event by the hash of the event without its id.
func eventReference(room *persist.Room, event matrix.Object) (string, error) {
	version := room.RoomVersion
	if version == "" {
		version = "11"
	}
	return eventcrypto.EventID(event, version)
}
