package unit

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/enimatek/lattice/internal/persist"
)

// TestDeviceListStreamsIncreasePerUser pins the numbering a peer uses to decide whether a device
// list update is newer than one it already applied.
//
// It has to be per user and strictly increasing. Both halves have failed in designs that looked
// reasonable: a count of the user's devices goes backwards the moment a device is deleted, and a
// single global position shared with the sync stream would tie a user's device list to unrelated
// traffic in other tables — and would let a second server's numbers mean nothing when they arrive
// carrying the same name.
func TestDeviceListStreamsIncreasePerUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devicestreams.db")
	db, err := persist.Open(path, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	first, err := db.DeviceLists.NextDeviceStream(ctx, "@first:localhost")
	if err != nil {
		t.Fatalf("taking the first number: %v", err)
	}
	if first != 1 {
		t.Errorf("the first number for a user is %d, wanting 1", first)
	}
	second, err := db.DeviceLists.NextDeviceStream(ctx, "@first:localhost")
	if err != nil {
		t.Fatalf("taking the second number: %v", err)
	}
	if second <= first {
		t.Errorf("the second number is %d, wanting more than the first (%d)", second, first)
	}

	other, err := db.DeviceLists.NextDeviceStream(ctx, "@second:localhost")
	if err != nil {
		t.Fatalf("taking another user's number: %v", err)
	}
	if other != 1 {
		t.Errorf("a different user started at %d, wanting 1: the counter is per user, so that one user's traffic cannot make another's announcements look stale", other)
	}

	// An empty user ID is not a user, and inventing a counter row for it would be a row nothing
	// can ever read back.
	none, err := db.DeviceLists.NextDeviceStream(ctx, "")
	if err != nil {
		t.Fatalf("asking for the number of an empty user ID: %v", err)
	}
	if none != 0 {
		t.Errorf("an empty user ID was given %d", none)
	}

	// The counter has to survive the database being reopened: a number that restarts after a
	// restart makes this server's later announcements look older than the ones a peer already
	// applied, which is precisely the case the number exists to settle.
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	reopened, err := persist.Open(path, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	after, err := reopened.DeviceLists.NextDeviceStream(ctx, "@first:localhost")
	if err != nil {
		t.Fatalf("taking a number after reopening: %v", err)
	}
	if after <= second {
		t.Errorf("after a restart the number is %d, wanting more than %d", after, second)
	}
}
