package unit

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// What the federation queue promises about a peer that is not there is "later", and
// "later" is the whole promise. Two quiet failures break it without losing a byte: a
// next attempt computed in the past, which is an attempt made again at once, and a flush
// that takes a fixed number of destinations ordered by oldest work, so that servers
// which can never answer occupy every slot a live peer's message could have used.
//
// Both were real. A jitter drawn in nanoseconds was subtracted as if it were
// milliseconds, which moved the next attempt up to four years back; five dead servers in
// a room's member list then kept the queue busy on servers that would never read what it
// sent while a room's message waited behind them.

// The bounds are spelled out here because README.md promises them to an operator: "a
// queue, retried with the same transaction id and the same bytes, backing off from five
// seconds to ten minutes".
const (
	outboxRetryFloor   = 5 * time.Second
	outboxRetryCeiling = 10 * time.Minute
)

// TestOutboxRetryDelayStaysInsideItsOwnBackoff holds the shape of the wait: doubling,
// bounded, and always a wait. The jitter may shave a quarter off, and nothing more -
// which is the difference between "we come back sooner than the ceiling" and "we never
// back off at all".
func TestOutboxRetryDelayStaysInsideItsOwnBackoff(t *testing.T) {
	t.Parallel()
	previous := time.Duration(0)
	for tries := 0; tries <= 12; tries++ {
		base := outboxRetryFloor << tries
		if base > outboxRetryCeiling {
			base = outboxRetryCeiling
		}
		if previous > 0 && previous < outboxRetryCeiling && base*3/4 <= previous {
			t.Fatalf("%d failed attempts waits no longer than %d did: %s is not past %s", tries, tries-1, base*3/4, previous)
		}
		for attempt := 0; attempt < 200; attempt++ {
			delay := service.OutboxRetryDelay(tries)
			switch {
			case delay <= 0:
				t.Fatalf("%d failed attempts produced a wait of %s: a next attempt in the past is a next attempt right now, every flush, forever", tries, delay)
			case delay > base:
				t.Fatalf("%d failed attempts waited %s, longer than the %s this step promises", tries, delay, base)
			case delay < base-base/4:
				t.Fatalf("%d failed attempts waited %s, more than a quarter under the %s this step promises", tries, delay, base)
			}
		}
		previous = base
	}
}

// TestAFailedPeerWaitsOutItsBackoffAndAMessageGoesFirst holds the two properties the
// queue's fairness is built from: a destination that is backing off is not asked again,
// and when a flush has one slot and two destinations waiting, the slot goes to the one
// holding an event rather than to the older ephemeral message for a peer that is gone.
func TestAFailedPeerWaitsOutItsBackoffAndAMessageGoesFirst(t *testing.T) {
	db, err := persist.NewDatabase(filepath.Join(t.TempDir(), "lattice.db"))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	now := time.Now().UnixMilli()
	// Gone is a server that stopped answering a week ago and is owed three device list
	// updates; Present is a peer a room's message is waiting for, queued afterwards.
	mustQueueOutboxItem(t, db, persist.OutboxItem{
		Destination: "gone.example",
		Kind:        "edu",
		EdgeID:      "edu:gone.example:m.device_list_update:@someone:gone.example:1",
		Payload:     `{"edu_type":"m.device_list_update","content":{}}`,
		CreatedTS:   now - 60_000,
	})
	mustQueueOutboxItem(t, db, persist.OutboxItem{
		Destination: "present.example",
		Kind:        "pdu",
		RoomID:      "!room:present.example",
		EventID:     "$event:present.example",
		EdgeID:      "pdu:present.example:$event:present.example",
		Payload:     `{"type":"m.room.message"}`,
		CreatedTS:   now - 1000,
	})
	if err := db.OutboxRepo.Pack(&persist.OutboundTxn{
		Destination: "gone.example",
		TxnID:       "l-1-gone",
		Payload:     `{"origin":"lattice.example","origin_server_ts":1,"pdus":[],"edus":[]}`,
		EDUs:        1,
		Tries:       1,
		NextTry:     now + service.OutboxRetryDelay(1).Milliseconds(),
		Created:     now - 60_000,
	}); err != nil {
		t.Fatalf("packing the transaction in flight to gone.example: %v", err)
	}

	if due := dueDestinations(t, db, now); len(due) != 1 || due[0] != "present.example" {
		t.Fatalf("the destinations due are %v, want only present.example: gone.example is backing off and must be left alone", due)
	}

	// One slot for two destinations that are both due: the message goes first, although
	// its item is the younger of the two.
	if err := db.OutboxRepo.Retry("gone.example", "l-1-gone", now-service.OutboxRetryDelay(2).Milliseconds(), "no route to host"); err != nil {
		t.Fatalf("recording a failed attempt for gone.example: %v", err)
	}
	if due, err := db.OutboxRepo.DueDestinations(now, 1); err != nil {
		t.Fatalf("reading the due destinations: %v", err)
	} else if len(due) != 1 || due[0] != "present.example" {
		t.Fatalf("with one slot the flush takes %v, want present.example: a device list update for a server that is gone is not a reason for a message to wait", due)
	}
	if due := dueDestinations(t, db, now); len(due) != 2 {
		t.Fatalf("the destinations due are %v, want both once the backoff has run out", due)
	}
}

// mustQueueOutboxItem queues one item, and fails the test when the queue says otherwise.
func mustQueueOutboxItem(t *testing.T, db *persist.Database, item persist.OutboxItem) {
	t.Helper()
	added, err := db.OutboxRepo.Append(&item)
	if err != nil {
		t.Fatalf("queueing an item for %s: %v", item.Destination, err)
	}
	if added != 1 {
		t.Fatalf("queueing an item for %s added %d rows, want 1", item.Destination, added)
	}
}

// dueDestinations reads what a flush would take, without limiting it.
func dueDestinations(t *testing.T, db *persist.Database, now int64) []string {
	t.Helper()
	destinations, err := db.OutboxRepo.DueDestinations(now, 0)
	if err != nil {
		t.Fatalf("reading the due destinations: %v", err)
	}
	return destinations
}
