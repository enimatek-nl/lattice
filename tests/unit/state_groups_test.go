package unit

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/enimatek/lattice/internal/persist"
)

// Current state says where a room is now. A fork can only be resolved against what each
// branch thought the state was, so a snapshot has to exist per event — stored as groups, so
// a linear room grows by one snapshot per state change rather than one per message.

func TestStateGroupsAreSharedNotCopied(t *testing.T) {
	t.Parallel()
	db := openStateGroupStore(t)
	ctx := context.Background()

	first, err := db.StateGroups.EnsureGroup(ctx, "!room:example.org", []persist.StateGroupEntry{
		{Type: "m.room.create", StateKey: "", EventID: "$create:e"},
		{Type: "m.room.member", StateKey: "@alice:e", EventID: "$join:e"},
	})
	if err != nil {
		t.Fatalf("creating the first snapshot: %v", err)
	}
	// The same set given in another order is the same snapshot: the identity is of the
	// state, not of the order it happened to be written in.
	again, err := db.StateGroups.EnsureGroup(ctx, "!room:example.org", []persist.StateGroupEntry{
		{Type: "m.room.member", StateKey: "@alice:e", EventID: "$join:e"},
		{Type: "m.room.create", StateKey: "", EventID: "$create:e"},
	})
	if err != nil {
		t.Fatalf("creating the same snapshot again: %v", err)
	}
	if again != first {
		t.Errorf("the same state produced two snapshots (%d and %d); a fork would then be invented where there is none", first, again)
	}

	// One entry changed: a different snapshot.
	changed, err := db.StateGroups.EnsureGroup(ctx, "!room:example.org", []persist.StateGroupEntry{
		{Type: "m.room.create", StateKey: "", EventID: "$create:e"},
		{Type: "m.room.member", StateKey: "@alice:e", EventID: "$rejoin:e"},
	})
	if err != nil {
		t.Fatalf("creating the changed snapshot: %v", err)
	}
	if changed == first {
		t.Errorf("a different state reused the first snapshot; the room would be read as unchanged")
	}

	entries, err := db.StateGroups.Entries(ctx, changed)
	if err != nil {
		t.Fatalf("reading the snapshot back: %v", err)
	}
	if len(entries) != 2 || entries[0].Type != "m.room.create" || entries[1].EventID != "$rejoin:e" {
		t.Errorf("the snapshot came back as %v", entries)
	}

	// Two rooms may hold the same state — the empty state of two new rooms, for
	// instance — without becoming each other's snapshot.
	left, err := db.StateGroups.EmptyGroup(ctx, "!left:example.org")
	if err != nil {
		t.Fatalf("the first empty snapshot: %v", err)
	}
	right, err := db.StateGroups.EmptyGroup(ctx, "!right:example.org")
	if err != nil {
		t.Fatalf("the second empty snapshot: %v (a snapshot is unique within its room, not across all rooms)", err)
	}
	if left == right {
		t.Errorf("two rooms share one snapshot row: %d", left)
	}
	leftEntries, err := db.StateGroups.Entries(ctx, left)
	if err != nil || len(leftEntries) != 0 {
		t.Errorf("the empty snapshot holds %v entries (%v)", len(leftEntries), err)
	}
}

func TestEventsRecordWhichSnapshotTheyWereSentAgainst(t *testing.T) {
	t.Parallel()
	db := openStateGroupStore(t)
	ctx := context.Background()

	group, err := db.StateGroups.EnsureGroup(ctx, "!room:example.org", []persist.StateGroupEntry{
		{Type: "m.room.create", StateKey: "", EventID: "$create:e"},
	})
	if err != nil {
		t.Fatalf("creating the snapshot: %v", err)
	}
	if err := db.StateGroups.Attach(ctx, "$message:e", group); err != nil {
		t.Fatalf("attaching the event: %v", err)
	}
	got, err := db.StateGroups.GroupOf(ctx, "$message:e")
	if err != nil {
		t.Fatalf("reading which snapshot an event was sent against: %v", err)
	}
	if got != group {
		t.Errorf("the event points at snapshot %d, want %d", got, group)
	}

	// An event that was never recorded is reported as missing, not as pointing at the
	// empty state: "unknown" and "the room had no state" are different answers, and the
	// second one would authorise nonsense.
	if id, err := db.StateGroups.GroupOf(ctx, "$unknown:e"); !errorsIsNotFound(err) {
		t.Errorf("an unrecorded event returned snapshot %d, err %v", id, err)
	}

	// Re-attaching (the same event persisted again after a retry) moves the pointer rather
	// than failing or leaving two rows.
	other, err := db.StateGroups.EnsureGroup(ctx, "!room:example.org", []persist.StateGroupEntry{
		{Type: "m.room.create", StateKey: "", EventID: "$create:e"},
		{Type: "m.room.topic", StateKey: "", EventID: "$topic:e"},
	})
	if err != nil {
		t.Fatalf("creating the second snapshot: %v", err)
	}
	if err := db.StateGroups.Attach(ctx, "$message:e", other); err != nil {
		t.Fatalf("re-attaching the event: %v", err)
	}
	if got, err := db.StateGroups.GroupOf(ctx, "$message:e"); err != nil || got != other {
		t.Errorf("after re-attaching the event points at %d (%v), want %d", got, err, other)
	}
}

func errorsIsNotFound(err error) bool { return errors.Is(err, persist.ErrNotFound) }

func openStateGroupStore(t *testing.T) *persist.Database {
	t.Helper()
	db, err := persist.Open(filepath.Join(t.TempDir(), "stategroups.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
