package unit

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/persist"
	"golang.org/x/sync/errgroup"
)

// TestSaveServerKeysUnderConcurrentFetches is a regression test for a federation failure that looked like an
// authentication problem.
//
// When two requests need a peer's signing keys at the same moment, the keyring asks that peer twice and both
// answers are recorded. Recording is a replacement — delete the server's key rows, insert the new ones — and
// two replacements that interleave collide on the unique index over (server_name, key_id). The caller then
// sees "cannot record the keys", the lookup has nothing to answer with, and a correctly signed request from a
// peer is refused as unauthenticated. On 2026-11-13 this is what Complement's media-over-federation test saw
// against this server, and it would happen in any deployment where two rooms talk to the same peer at once.
func TestSaveServerKeysUnderConcurrentFetches(t *testing.T) {
	t.Parallel()
	db, err := persist.Open(filepath.Join(t.TempDir(), "keys.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("cannot open the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	now := time.Now()
	document := func(fetched time.Time) *persist.ServerKeyDocument {
		return &persist.ServerKeyDocument{
			ServerName:   "peer.example",
			Document:     []byte(`{"server_name":"peer.example"}`),
			ValidUntilTS: fetched.Add(24 * time.Hour).UnixMilli(),
			FetchedAt:    fetched,
		}
	}
	keysFor := func(fetched time.Time) []persist.ServerSigningKey {
		return []persist.ServerSigningKey{{
			ServerName:   "peer.example",
			KeyID:        "ed25519:v1",
			Key:          []byte("0123456789abcdef0123456789abcdef"),
			ValidUntilTS: fetched.Add(24 * time.Hour).UnixMilli(),
			FetchedAt:    fetched,
		}}
	}

	group, groupCtx := errgroup.WithContext(ctx)
	var failures []error
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		group.Go(func() error {
			if err := db.Federation.SaveServerKeys(groupCtx, document(now), keysFor(now)); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatalf("a concurrent save failed: %v", err)
	}
	for _, failure := range failures {
		if errors.Is(failure, persist.ErrConflict) {
			continue
		}
		t.Errorf("saving the same server's keys concurrently failed: %v", failure)
	}

	rows, err := db.Federation.ServerKeys(ctx, "peer.example")
	if err != nil {
		t.Fatalf("cannot read back the keys: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("%d key rows are recorded for one server that published one key: %v", len(rows), rows)
	}
}

// TestRememberAuthContextUnderConcurrentWrites is the same race on the other replace-as-you-write table: an
// event handed to this server by two peers at once has its auth context snapshot replaced twice.
func TestRememberAuthContextUnderConcurrentWrites(t *testing.T) {
	t.Parallel()
	db, err := persist.Open(filepath.Join(t.TempDir(), "auth.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("cannot open the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	ids := []string{"$one:peer.example", "$two:peer.example", "$three:peer.example"}

	group := new(errgroup.Group)
	var mu sync.Mutex
	var failures []error
	for i := 0; i < 8; i++ {
		group.Go(func() error {
			if err := db.State.RememberAuthContext(ctx, "$event:peer.example", ids); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatalf("a concurrent write failed: %v", err)
	}
	for _, failure := range failures {
		if errors.Is(failure, persist.ErrConflict) {
			continue
		}
		t.Errorf("writing the same auth context concurrently failed: %v", failure)
	}

	stored, err := db.State.AuthContext(ctx, "$event:peer.example")
	if err != nil {
		t.Fatalf("cannot read back the auth context: %v", err)
	}
	if len(stored) != len(ids) {
		t.Errorf("%d auth context rows are recorded, want %d: %v", len(stored), len(ids), stored)
	}
}
