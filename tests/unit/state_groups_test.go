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

// A snapshot is only useful if it can be asked about on its own. The question that matters in
// the protocol is narrow — what did this room think about this key at the moment this event was
// sent — and answering it from the room's current state answers a different question: a member
// who has since left is not in the current state at all, so there is nothing left to look up.
func TestStateEntryAtAnswersAboutTheMomentNotTheMomentAfter(t *testing.T) {
	t.Parallel()
	db := openStateGroupStore(t)
	ctx := context.Background()
	room := "!room:example.org"

	created, err := db.StateGroups.EnsureGroup(ctx, room, []persist.StateGroupEntry{
		{Type: "m.room.create", StateKey: "", EventID: "$create:e"},
	})
	if err != nil {
		t.Fatalf("recording the state the create event carries: %v", err)
	}
	withAlice, err := db.StateGroups.EnsureGroup(ctx, room, []persist.StateGroupEntry{
		{Type: "m.room.create", StateKey: "", EventID: "$create:e"},
		{Type: "m.room.member", StateKey: "@alice:e", EventID: "$alice-join:e"},
	})
	if err != nil {
		t.Fatalf("recording the state alice's join carries: %v", err)
	}
	withBob, err := db.StateGroups.EnsureGroup(ctx, room, []persist.StateGroupEntry{
		{Type: "m.room.create", StateKey: "", EventID: "$create:e"},
		{Type: "m.room.member", StateKey: "@alice:e", EventID: "$alice-join:e"},
		{Type: "m.room.member", StateKey: "@bob:e", EventID: "$bob-join:e"},
	})
	if err != nil {
		t.Fatalf("recording the state bob's join carries: %v", err)
	}
	afterBobLeft, err := db.StateGroups.EnsureGroup(ctx, room, []persist.StateGroupEntry{
		{Type: "m.room.create", StateKey: "", EventID: "$create:e"},
		{Type: "m.room.member", StateKey: "@alice:e", EventID: "$alice-join:e"},
		{Type: "m.room.member", StateKey: "@bob:e", EventID: "$bob-leave:e"},
	})
	if err != nil {
		t.Fatalf("recording the state bob's leave carries: %v", err)
	}
	for _, attach := range []struct {
		eventID string
		groupID int64
	}{
		{"$create:e", created},
		{"$alice-join:e", withAlice},
		{"$bob-join:e", withBob},
		{"$message:e", withBob},
		{"$bob-leave:e", afterBobLeft},
	} {
		if err := db.StateGroups.Attach(ctx, attach.eventID, attach.groupID); err != nil {
			t.Fatalf("attaching %s to its state: %v", attach.eventID, err)
		}
	}

	cases := []struct {
		eventID  string
		stateKey string
		want     string
		missing  bool
	}{
		// The message was sent while bob was joined, and says so even though he left later.
		{"$message:e", "@bob:e", "$bob-join:e", false},
		// And the leave does not rewrite that: asked about the same message again, the answer
		// is still the join that stood when it was sent.
		{"$message:e", "@alice:e", "$alice-join:e", false},
		// The leave event's own snapshot holds bob as gone.
		{"$bob-leave:e", "@bob:e", "$bob-leave:e", false},
		// The create event was sent before alice existed in the room: no entry, which the
		// caller must not fill in with the member event that came after.
		{"$create:e", "@alice:e", "", true},
		// A key nobody ever set.
		{"$message:e", "@carol:e", "", true},
		// An event whose snapshot was never recorded. Inventing an answer from what is in place
		// now is what this method exists to make impossible.
		{"$unrecorded:e", "@alice:e", "", true},
	}
	for _, tc := range cases {
		got, err := db.StateGroups.StateEntryAt(ctx, tc.eventID, "m.room.member", tc.stateKey)
		if tc.missing {
			if !errors.Is(err, persist.ErrNotFound) {
				t.Errorf("StateEntryAt(%s, %s) = %q, %v; want ErrNotFound", tc.eventID, tc.stateKey, got, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("StateEntryAt(%s, %s): %v", tc.eventID, tc.stateKey, err)
			continue
		}
		if got != tc.want {
			t.Errorf("StateEntryAt(%s, %s) = %q, want %q", tc.eventID, tc.stateKey, got, tc.want)
		}
	}
}
