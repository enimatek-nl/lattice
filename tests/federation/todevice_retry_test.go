package federation

import (
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestAToDeviceMessageWaitsForTheServerThatIsNotAnswering is the queue's whole point. A to-device
// message has no room, no state and no history: it is one device telling another device
// something, and if the server holding the second device is not there at the moment of telling,
// the only thing a homeserver can do is remember to say it later. Dropping it instead breaks the
// one thing this traffic is used for - an Olm key for an invite, which arrives after the invite or
// not at all. Complement stops the peer's container, sends a to-device message, starts the peer
// again and expects the message to arrive; a homeserver that tries once fails.
func TestAToDeviceMessageWaitsForTheServerThatIsNotAnswering(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")

	// The peer is unreachable when the message is sent, and comes back while the message is
	// still waiting. blackout rather than disconnect, because the point is a peer that returns.
	c.blackout(serverTwo)
	sendToDevice(t, one, alice, "my.test.type", "queued-1", map[string]map[string]any{
		bob.UserID: {"*": matrix.Object{"my_key": "my_value"}},
	})
	// An attempt made now fails, which is what puts the queue on a backoff and makes the test
	// about retrying rather than about not having tried yet.
	flush(t, one)
	if count, err := two.db.ToDeviceRepo.Count(); err != nil || count != 0 {
		t.Fatalf("%s received a to-device message while it was down: %d", serverTwo, count)
	}
	waiting, err := one.db.OutboxRepo.Count()
	if err != nil {
		t.Fatalf("reading the outbox of %s: %v", serverOne, err)
	}
	if waiting == 0 {
		t.Fatalf("%s dropped the to-device message instead of queueing it while %s was down", serverOne, serverTwo)
	}

	c.restore(serverTwo)
	// The queue is drained on a timer, and a destination that just failed is held back by a
	// backoff, so what is being waited for is the timer and the backoff rather than a client
	// asking again.
	deadline := time.Now().Add(40 * time.Second)
	var got matrix.SyncResponse
	for time.Now().Before(deadline) {
		flush(t, one)
		got = syncOnce(t, two, bob, "")
		if got.ToDevice != nil && len(got.ToDevice.Events) > 0 {
			break
		}
		time.Sleep(time.Second)
	}
	if got.ToDevice == nil || len(got.ToDevice.Events) == 0 {
		t.Fatalf("%s never delivered the to-device message after %s came back", serverTwo, serverTwo)
	}
	delivered := got.ToDevice.Events[0]
	if delivered.Type != "my.test.type" {
		t.Errorf("the delivered message is %s, want my.test.type", delivered.Type)
	}
	if delivered.Sender != alice.UserID {
		t.Errorf("the delivered message comes from %s, want %s", delivered.Sender, alice.UserID)
	}
	content := decodeContent(t, delivered.Content)
	if content["my_key"] != "my_value" {
		t.Errorf("the delivered message carries %v, want my_key=my_value", content)
	}

	// Delivered once. A queue that retries must not send the same message twice when a peer
	// answers late: a duplicated to-device message is a duplicated key request, and a client
	// cannot tell which it is holding.
	flush(t, one)
	if count, err := two.db.ToDeviceRepo.Count(); err != nil || count != 1 {
		t.Errorf("%s holds %d copies of the message, want the one it was sent", serverTwo, count)
	}
	remaining, err := one.db.OutboxRepo.Count()
	if err != nil {
		t.Fatalf("reading the outbox of %s again: %v", serverOne, err)
	}
	if remaining != 0 {
		t.Errorf("%s still holds %d queued transactions after delivering them", serverOne, remaining)
	}
}
