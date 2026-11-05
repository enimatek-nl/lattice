package unit

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/persist"
)

func openEventStore(t *testing.T) *persist.Database {
	t.Helper()
	db, err := persist.Open(filepath.Join(t.TempDir(), "events.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("cannot open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func strPtr(s string) *string { return &s }

func storeEvent(t *testing.T, db *persist.Database, eventID, roomID, eventType string, stateKey *string, depth int64) *persist.Event {
	t.Helper()
	stream, err := db.Events.NextStreamOrdering(context.Background())
	if err != nil {
		t.Fatalf("stream ordering: %v", err)
	}
	event := &persist.Event{
		EventID:        eventID,
		RoomID:         roomID,
		Type:           eventType,
		StateKey:       stateKey,
		Sender:         "@alice:example.org",
		Depth:          depth,
		OriginServerTS: 1700000000000 + depth,
		ReceivedTS:     1700000001000 + depth,
		JSON:           []byte(`{"event_id":"` + eventID + `"}`),
		StreamOrdering: stream,
	}
	if err := db.Events.Insert(context.Background(), event); err != nil {
		t.Fatalf("insert %s: %v", eventID, err)
	}
	return event
}

// TestEventRoundTripPreservesStateKeySemantics checks the NULL versus empty-string
// distinction: m.room.create is a state event whose state key is the empty string,
// and an m.room.message has no state key at all.
func TestEventRoundTripPreservesStateKeySemantics(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	ctx := context.Background()

	storeEvent(t, db, "$create:example.org", "!room:example.org", "m.room.create", strPtr(""), 1)
	storeEvent(t, db, "$msg:example.org", "!room:example.org", "m.room.message", nil, 2)

	created, err := db.Events.ByID(ctx, "$create:example.org")
	if err != nil {
		t.Fatalf("read create event: %v", err)
	}
	if !created.IsState() || created.StateKey == nil || *created.StateKey != "" {
		t.Errorf("create event lost its empty state key: state_key=%v", created.StateKey)
	}

	message, err := db.Events.ByID(ctx, "$msg:example.org")
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	if message.IsState() {
		t.Errorf("a message must not look like a state event: state_key=%q", deref(message.StateKey))
	}
	if string(message.JSON) != `{"event_id":"$msg:example.org"}` {
		t.Errorf("stored JSON changed: %s", message.JSON)
	}
}

// TestDuplicateEventIsConflict covers event deduplication: the same event arriving
// twice must not create a second row.
func TestDuplicateEventIsConflict(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	storeEvent(t, db, "$dup:example.org", "!room:example.org", "m.room.message", nil, 1)

	err := db.Events.Insert(context.Background(), &persist.Event{
		EventID: "$dup:example.org", RoomID: "!room:example.org", Type: "m.room.message",
		Sender: "@bob:example.org", JSON: []byte(`{}`),
	})
	if !errors.Is(err, persist.ErrConflict) {
		t.Errorf("duplicate insert = %v, want ErrConflict", err)
	}
}

// TestForwardExtremities checks the event-graph query new events attach to.
func TestForwardExtremities(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	ctx := context.Background()
	room := "!graph:example.org"

	storeEvent(t, db, "$a:example.org", room, "m.room.message", nil, 1)
	extremities, err := db.Events.ForwardExtremities(ctx, room)
	if err != nil {
		t.Fatalf("forward extremities: %v", err)
	}
	if len(extremities) != 1 || extremities[0] != "$a:example.org" {
		t.Fatalf("extremities after first event = %v", extremities)
	}

	storeEvent(t, db, "$b:example.org", room, "m.room.message", nil, 2)
	if err := db.Events.Link(ctx, "$b:example.org", "$a:example.org", persist.EdgePrev); err != nil {
		t.Fatalf("link: %v", err)
	}
	extremities, err = db.Events.ForwardExtremities(ctx, room)
	if err != nil {
		t.Fatalf("forward extremities: %v", err)
	}
	if len(extremities) != 1 || extremities[0] != "$b:example.org" {
		t.Errorf("extremities after linking = %v, want only $b", extremities)
	}

	// A soft-failed event is kept but is not part of accepted history, so it must not
	// become a leaf new events attach to.
	soft := &persist.Event{
		EventID: "$soft:example.org", RoomID: room, Type: "m.room.message",
		Sender: "@mallory:example.org", JSON: []byte(`{}`), SoftFailed: true,
		StreamOrdering: mustStream(t, db),
	}
	if err := db.Events.Insert(ctx, soft); err != nil {
		t.Fatalf("insert soft-failed: %v", err)
	}
	extremities, err = db.Events.ForwardExtremities(ctx, room)
	if err != nil {
		t.Fatalf("forward extremities: %v", err)
	}
	for _, id := range extremities {
		if id == "$soft:example.org" {
			t.Errorf("a soft-failed event was offered as a forward extremity: %v", extremities)
		}
	}
}

// TestEdgesAreNotConstrained records an intentional choice: Matrix servers hold
// events whose predecessors or auth events they have not fetched yet, so a reference
// to a missing event is valid state, not corruption.
func TestEdgesAreNotConstrained(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	ctx := context.Background()

	storeEvent(t, db, "$child:example.org", "!partial:example.org", "m.room.message", nil, 5)
	if err := db.Events.Link(ctx, "$child:example.org", "$missing:example.org", persist.EdgePrev); err != nil {
		t.Fatalf("linking to a missing event should be allowed: %v", err)
	}
	refs, err := db.Events.References(ctx, "$child:example.org", persist.EdgePrev)
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if len(refs) != 1 || refs[0] != "$missing:example.org" {
		t.Errorf("references = %v", refs)
	}
	back, err := db.Events.ReferencedBy(ctx, "$missing:example.org", persist.EdgePrev)
	if err != nil {
		t.Fatalf("referenced by: %v", err)
	}
	if len(back) != 1 || back[0] != "$child:example.org" {
		t.Errorf("referenced by = %v", back)
	}

	// Linking the same pair twice is a no-op rather than an error or a duplicate row.
	if err := db.Events.Link(ctx, "$child:example.org", "$missing:example.org", persist.EdgePrev); err != nil {
		t.Errorf("repeat link: %v", err)
	}
	refs, _ = db.Events.References(ctx, "$child:example.org", persist.EdgePrev)
	if len(refs) != 1 {
		t.Errorf("repeat link created duplicates: %v", refs)
	}
}

// TestMissingEventsAreNotAnError checks that a batch read tolerates gaps, which
// auth chains and backfills routinely have.
func TestMissingEventsAreNotAnError(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	storeEvent(t, db, "$here:example.org", "!gap:example.org", "m.room.message", nil, 1)

	events, err := db.Events.ByIDs(context.Background(), []string{"$here:example.org", "$gone:example.org"})
	if err != nil {
		t.Fatalf("ByIDs should tolerate missing IDs: %v", err)
	}
	if len(events) != 1 || events[0].EventID != "$here:example.org" {
		t.Errorf("ByIDs = %d events, want 1", len(events))
	}
	if events, err := db.Events.ByIDs(context.Background(), nil); err != nil || len(events) != 0 {
		t.Errorf("ByIDs(nil) = %v, %v", events, err)
	}
}

// TestStreamOrderingIsMonotonicWithinATransaction covers the ordering read
// positions and history pagination depend on.
func TestStreamOrderingIsMonotonicWithinATransaction(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	ctx := context.Background()

	first, err := db.Events.NextStreamOrdering(ctx)
	if err != nil {
		t.Fatalf("next stream: %v", err)
	}
	storeEvent(t, db, "$one:example.org", "!order:example.org", "m.room.message", nil, 1)
	second, err := db.Events.NextStreamOrdering(ctx)
	if err != nil {
		t.Fatalf("next stream: %v", err)
	}
	if second <= first {
		t.Errorf("stream ordering did not advance: %d then %d", first, second)
	}

	// A rolled-back transaction must not burn an ordering number: the events it would
	// have written do not exist, so the next event reuses the position.
	if err := db.Tx(ctx, func(tx *gorm.DB) error {
		return errors.New("rollback")
	}); err == nil {
		t.Fatalf("expected the transaction to fail")
	}
	afterRollback, err := db.Events.NextStreamOrdering(ctx)
	if err != nil {
		t.Fatalf("next stream after rollback: %v", err)
	}
	if afterRollback != second {
		t.Errorf("stream ordering advanced past a rolled back transaction: %d then %d", second, afterRollback)
	}
}

// TestRoomStateIsAMap checks the upsert semantics of current room state.
func TestRoomStateIsAMap(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	ctx := context.Background()
	room := "!state:example.org"

	if err := db.State.Set(ctx, &persist.RoomState{RoomID: room, Type: "m.room.member", StateKey: "@alice:example.org", EventID: "$join1:example.org"}); err != nil {
		t.Fatalf("set state: %v", err)
	}
	if err := db.State.Set(ctx, &persist.RoomState{RoomID: room, Type: "m.room.member", StateKey: "@alice:example.org", EventID: "$leave1:example.org"}); err != nil {
		t.Fatalf("set state again: %v", err)
	}
	current, err := db.State.Get(ctx, room, "m.room.member", "@alice:example.org")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if current.EventID != "$leave1:example.org" {
		t.Errorf("state = %s, want the newest event", current.EventID)
	}
	all, err := db.State.All(ctx, room)
	if err != nil {
		t.Fatalf("all state: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("state rows = %d, want one entry per (type, state_key)", len(all))
	}
	// Empty state keys (m.room.create, m.room.join_rules) are a real key.
	if err := db.State.Set(ctx, &persist.RoomState{RoomID: room, Type: "m.room.create", StateKey: "", EventID: "$create:example.org"}); err != nil {
		t.Fatalf("set empty state key: %v", err)
	}
	if got, err := db.State.Get(ctx, room, "m.room.create", ""); err != nil || got.EventID != "$create:example.org" {
		t.Errorf("empty state key lookup = %v, %v", got, err)
	}
	if _, err := db.State.Get(ctx, room, "m.room.topic", ""); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("missing state = %v, want ErrNotFound", err)
	}
}

// TestAuthContextIsPerEvent checks that an event's stored auth snapshot is replaced
// rather than accumulated.
func TestAuthContextIsPerEvent(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	ctx := context.Background()

	if err := db.State.RememberAuthContext(ctx, "$ev:example.org", []string{"$create:example.org", "$pl:example.org"}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	ids, err := db.State.AuthContext(ctx, "$ev:example.org")
	if err != nil {
		t.Fatalf("auth context: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("auth context = %v", ids)
	}
	if err := db.State.RememberAuthContext(ctx, "$ev:example.org", []string{"$create:example.org"}); err != nil {
		t.Fatalf("remember again: %v", err)
	}
	ids, _ = db.State.AuthContext(ctx, "$ev:example.org")
	if len(ids) != 1 {
		t.Errorf("auth context accumulated instead of being replaced: %v", ids)
	}
}

// TestTransactionIDsArePerDevice covers the idempotency rule for
// PUT /rooms/{roomId}/send/{type}/{txn}: the same transaction ID on a different
// device is a different request.
func TestTransactionIDsArePerDevice(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	ctx := context.Background()

	if _, err := db.Events.TxnLookup(ctx, "DEVICEA", "txn1"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("lookup before save = %v, want ErrNotFound", err)
	}
	if err := db.Events.TxnSave(ctx, &persist.EventTxn{DeviceID: "DEVICEA", TxnID: "txn1", EventID: "$ev:example.org"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	found, err := db.Events.TxnLookup(ctx, "DEVICEA", "txn1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if found.EventID != "$ev:example.org" {
		t.Errorf("lookup returned %q", found.EventID)
	}
	if err := db.Events.TxnSave(ctx, &persist.EventTxn{DeviceID: "DEVICEA", TxnID: "txn1", EventID: "$other:example.org"}); !errors.Is(err, persist.ErrConflict) {
		t.Errorf("repeat transaction id = %v, want ErrConflict", err)
	}
	if err := db.Events.TxnSave(ctx, &persist.EventTxn{DeviceID: "DEVICEB", TxnID: "txn1", EventID: "$other:example.org"}); err != nil {
		t.Errorf("same transaction id on another device should be accepted: %v", err)
	}
}

// TestRoomStorePersistsVersion checks that the room version is recorded: it selects
// the authorisation rules, redaction profile and event ID format for the room.
func TestRoomStorePersistsVersion(t *testing.T) {
	t.Parallel()
	db := openEventStore(t)
	ctx := context.Background()

	if err := db.Rooms.Create(ctx, &persist.Room{RoomID: "!new:example.org", RoomVersion: "11", Creator: "@alice:example.org", CreatedTS: 1700000000000}); err != nil {
		t.Fatalf("create room: %v", err)
	}
	room, err := db.Rooms.ByRoomID(ctx, "!new:example.org")
	if err != nil {
		t.Fatalf("read room: %v", err)
	}
	if room.RoomVersion != "11" || room.Creator != "@alice:example.org" {
		t.Errorf("room = %+v", room)
	}
	if err := db.Rooms.SetLatestStream(ctx, "!new:example.org", 42); err != nil {
		t.Fatalf("set stream: %v", err)
	}
	room, _ = db.Rooms.ByRoomID(ctx, "!new:example.org")
	if room.LatestStream != 42 {
		t.Errorf("latest stream = %d, want 42", room.LatestStream)
	}
	if _, err := db.Rooms.ByRoomID(ctx, "!absent:example.org"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("missing room = %v, want ErrNotFound", err)
	}
}

func mustStream(t *testing.T, db *persist.Database) int64 {
	t.Helper()
	stream, err := db.Events.NextStreamOrdering(context.Background())
	if err != nil {
		t.Fatalf("stream ordering: %v", err)
	}
	return stream
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return "'" + *s + "'"
}
