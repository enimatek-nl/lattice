package unit

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/federation"
	"github.com/enimatek/lattice/internal/persist"
)

// The federation tables hold promises: work this server still owes, transactions it has
// already answered, keys it decided to trust. A promise nobody keeps and nobody forgets is
// how a queue becomes a graveyard.

func TestFederationQueueClaimRetryAndDrop(t *testing.T) {
	t.Parallel()
	db := openFederationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := db.Federation.Queue(ctx, &persist.FederationQueueItem{
		Destination: "peer.example", Kind: persist.QueueKindPDU,
		Payload: `{"event_id":"$one:peer.example"}`, EventID: "$one:peer.example", NextAttemptAt: now,
	}); err != nil {
		t.Fatalf("queueing: %v", err)
	}
	depth, err := db.Federation.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("reading the queue depth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("queue depth %d, want 1", depth)
	}

	claimed, err := db.Federation.ClaimQueueItems(ctx, now, 10)
	if err != nil {
		t.Fatalf("claiming: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d items, want 1", len(claimed))
	}
	// The claim moved the attempt time forward, so a second worker cannot take the same
	// item and send it twice.
	again, err := db.Federation.ClaimQueueItems(ctx, now, 10)
	if err != nil {
		t.Fatalf("claiming again: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("the same item was claimed twice; two transactions would carry it")
	}

	// A failure puts it back with a later attempt time, keeping the reason.
	if err := db.Federation.QueueRetry(ctx, claimed[0].ID, now.Add(time.Hour), "the peer was not listening"); err != nil {
		t.Fatalf("rescheduling: %v", err)
	}
	later, err := db.Federation.ClaimQueueItems(ctx, now.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("claiming later: %v", err)
	}
	if len(later) != 0 {
		t.Errorf("an item scheduled for the future was claimed now")
	}
	if due, err := db.Federation.QueuedDestinations(ctx, now.Add(2*time.Hour)); err != nil || len(due) != 1 {
		t.Errorf("a waiting destination was not reported: %v (%v)", due, err)
	}

	// Giving up leaves the row, so the failure can still be found afterwards.
	if err := db.Federation.QueueDrop(ctx, []uint64{claimed[0].ID}, "the peer never answered"); err != nil {
		t.Fatalf("giving up: %v", err)
	}
	depth, _ = db.Federation.QueueDepth(ctx)
	if depth != 0 {
		t.Errorf("queue depth %d after giving up, want nothing left owing", depth)
	}
	count, err := db.Federation.QueueCount(ctx, "peer.example", "$one:peer.example")
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 0 {
		t.Errorf("a given-up item still counts as owing: %d", count)
	}

	// Delivered work leaves the queue entirely.
	if err := db.Federation.Queue(ctx, &persist.FederationQueueItem{
		Destination: "peer.example", Kind: persist.QueueKindPDU,
		Payload: `{"event_id":"$two:peer.example"}`, EventID: "$two:peer.example", NextAttemptAt: now,
	}); err != nil {
		t.Fatalf("queueing the second item: %v", err)
	}
	claimed, err = db.Federation.ClaimQueueItems(ctx, now, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claiming the second item: %d (%v)", len(claimed), err)
	}
	if err := db.Federation.QueueDone(ctx, []uint64{claimed[0].ID}); err != nil {
		t.Fatalf("clearing delivered work: %v", err)
	}
	depth, _ = db.Federation.QueueDepth(ctx)
	if depth != 0 {
		t.Errorf("queue depth %d after delivery, want 0", depth)
	}
}

func TestFederationCleanupForgetsWhatExpired(t *testing.T) {
	t.Parallel()
	db := openFederationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sender := federation.NewSender(db.Federation, db.Events, db.State, nil, "ours.example",
		slog.New(slog.DiscardHandler))

	// A transaction this server answered, whose replay window has passed.
	if err := db.Federation.RecordTransaction(ctx, &persist.FederationTransaction{
		Origin: "peer.example", TxnID: "old", Response: []byte(`{"pdus":{}}`),
		ReceivedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("recording the old transaction: %v", err)
	}
	// And one still inside its window, which must survive.
	if err := db.Federation.RecordTransaction(ctx, &persist.FederationTransaction{
		Origin: "peer.example", TxnID: "recent", Response: []byte(`{"pdus":{}}`),
		ReceivedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("recording the recent transaction: %v", err)
	}
	seen, err := db.Federation.SeenTransaction(ctx, "peer.example", "old", now)
	if err != nil {
		t.Fatalf("looking for the old transaction: %v", err)
	}
	if seen != nil {
		t.Errorf("an expired transaction is still answered from; its replay window has passed")
	}
	if seen, err = db.Federation.SeenTransaction(ctx, "peer.example", "recent", now); err != nil || seen == nil {
		t.Errorf("the recent transaction was not answered from: %v (%v)", seen, err)
	}

	// A key document whose trust window has run out, and the keys that came with it.
	if err := db.Federation.SaveServerKeys(ctx, &persist.ServerKeyDocument{
		ServerName: "stale.example", Document: []byte(`{"server_name":"stale.example"}`),
		ValidUntilTS: now.Add(-time.Hour).UnixMilli(), FetchedAt: now.Add(-8 * 24 * time.Hour),
	}, []persist.ServerSigningKey{{ServerName: "stale.example", KeyID: "ed25519:gone",
		Key: []byte("01234567890123456789012345678901"), ValidUntilTS: now.Add(-time.Hour).UnixMilli()}}); err != nil {
		t.Fatalf("recording the expired keys: %v", err)
	}

	// A queue row given up on long enough ago that the trail is cold, and one from
	// today, which is still somebody's question to answer.
	if err := db.Federation.Queue(ctx, &persist.FederationQueueItem{
		Destination: "cold.example", Kind: persist.QueueKindPDU, Payload: `{}`,
		EventID: "$cold:cold.example", NextAttemptAt: now,
	}); err != nil {
		t.Fatalf("queueing the cold item: %v", err)
	}
	cold, err := db.Federation.ClaimQueueItems(ctx, now, 10)
	if err != nil || len(cold) == 0 {
		t.Fatalf("claiming the cold item: %d (%v)", len(cold), err)
	}
	var ids []uint64
	for _, item := range cold {
		ids = append(ids, item.ID)
	}
	if err := db.Federation.QueueDrop(ctx, ids, "gave up"); err != nil {
		t.Fatalf("giving up on the cold item: %v", err)
	}
	if err := db.DB().Model(&persist.FederationQueueItem{}).
		Where("event_id = ?", "$cold:cold.example").
		Update("dropped_at", now.Add(-60*24*time.Hour)).Error; err != nil {
		t.Fatalf("ageing the dropped row: %v", err)
	}
	if err := db.Federation.Queue(ctx, &persist.FederationQueueItem{
		Destination: "recent.example", Kind: persist.QueueKindPDU, Payload: `{}`,
		EventID: "$fresh:recent.example", NextAttemptAt: now,
	}); err != nil {
		t.Fatalf("queueing the fresh item: %v", err)
	}
	fresh, err := db.Federation.ClaimQueueItems(ctx, now, 10)
	if err != nil || len(fresh) == 0 {
		t.Fatalf("claiming the fresh item: %d (%v)", len(fresh), err)
	}
	var freshIDs []uint64
	for _, item := range fresh {
		freshIDs = append(freshIDs, item.ID)
	}
	if err := db.Federation.QueueDrop(ctx, freshIDs, "gave up today"); err != nil {
		t.Fatalf("giving up on the fresh item: %v", err)
	}

	forgotten, err := sender.Cleanup(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if forgotten < 3 {
		t.Errorf("cleanup forgot %d records; the expired transaction, the expired keys and the cold queue row should all have gone", forgotten)
	}
	if seen, err := db.Federation.SeenTransaction(ctx, "peer.example", "recent", now); err != nil || seen == nil {
		t.Errorf("cleanup deleted a transaction that was still inside its replay window: %v (%v)", seen, err)
	}
	keys, err := db.Federation.ServerKeys(ctx, "stale.example")
	if err != nil {
		t.Fatalf("reading the keys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("%d keys outlived their trust window", len(keys))
	}
	if count, err := db.Federation.QueueCount(ctx, "recent.example", "$fresh:recent.example"); err != nil || count != 0 {
		t.Errorf("the recent failure record confused the queue: %d (%v)", count, err)
	}
	var remaining int64
	if err := db.DB().Model(&persist.FederationQueueItem{}).
		Where("event_id = ?", "$fresh:recent.example").Count(&remaining).Error; err != nil {
		t.Fatalf("counting the recent dropped row: %v", err)
	}
	if remaining != 1 {
		t.Errorf("the record of today's failure is gone; a gave-up row stays while it is still recent")
	}
}

// openFederationStore opens a store for one test.
func openFederationStore(t *testing.T) *persist.Database {
	t.Helper()
	db, err := persist.Open(filepath.Join(t.TempDir(), "federation.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
