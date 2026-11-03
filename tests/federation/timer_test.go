package federation

import (
	"context"
	"testing"
	"time"
)

// TestTheOutboxTimerDeliversWithoutAnyoneAsking runs the queue the way a running server does: on
// a timer, with no test calling flush. The distinction matters because a queue that is only
// drained when something else happens looks identical in a test that drains it by hand, and
// identical right up until a to-device message sits in a database while both servers are healthy.
func TestTheOutboxTimerDeliversWithoutAnyoneAsking(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := one.svc.StartFederationOutbox(ctx, 200*time.Millisecond)
	defer stop()

	// Out first, while the peer is not answering, so the message has to wait for the backoff and
	// the timer rather than for a hand.
	c.blackout(serverTwo)
	sendToDevice(t, one, alice, "my.test.type", "timer-1", map[string]map[string]any{
		bob.UserID: {"*": map[string]any{"my_key": "my_value"}},
	})
	time.Sleep(300 * time.Millisecond)
	if count, err := two.db.ToDeviceRepo.Count(); err != nil || count != 0 {
		t.Fatalf("%s received a to-device message while it was down: %d", serverTwo, count)
	}
	c.restore(serverTwo)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if count, err := two.db.ToDeviceRepo.Count(); err != nil {
			t.Fatalf("reading %s: %v", serverTwo, err)
		} else if count > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("the outbox timer never delivered the queued to-device message to %s", serverTwo)
}
