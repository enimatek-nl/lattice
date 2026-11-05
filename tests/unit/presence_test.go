package unit

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/presence"
)

// newPresenceService opens a throwaway database and wires the presence service to it.
func newPresenceService(t *testing.T) (*presence.Service, *persist.Database) {
	t.Helper()
	db, err := persist.Open(filepath.Join(t.TempDir(), "presence.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return presence.New(db, "example.org", slog.New(slog.DiscardHandler)), db
}

// TestPresenceStore checks the store's two promises: a change takes a stream position,
// and being seen does not.
func TestPresenceStore(t *testing.T) {
	ctx := context.Background()
	_, db := newPresenceService(t)

	repo := db.Presence
	if _, err := repo.Get(ctx, "@nobody:example.org"); !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("reading an unknown user: %v, want ErrNotFound", err)
	}

	first, err := repo.Set(ctx, "@alice:example.org", persist.PresenceOnline, "hello", true, 1_000)
	if err != nil {
		t.Fatalf("storing presence: %v", err)
	}
	if first <= 0 {
		t.Errorf("a presence change took position %d, want a stream position", first)
	}
	row, err := repo.Get(ctx, "@alice:example.org")
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if row.State != persist.PresenceOnline || row.StatusMsg != "hello" || !row.Active {
		t.Errorf("stored presence = %+v", row)
	}

	// Being seen is not a change: the position must not move, or every request would
	// announce the user to everybody they share a room with.
	if err := repo.MarkActive(ctx, "@alice:example.org", 5_000); err != nil {
		t.Fatalf("marking the user seen: %v", err)
	}
	after, err := repo.Get(ctx, "@alice:example.org")
	if err != nil {
		t.Fatalf("reading after being seen: %v", err)
	}
	if after.StreamOrdering != first {
		t.Errorf("being seen moved the stream position from %d to %d", first, after.StreamOrdering)
	}
	if after.LastActiveTS != 5_000 || !after.Active {
		t.Errorf("being seen did not update the sighting: %+v", after)
	}

	// A change does take one, and it is ahead of anything written before it.
	second, err := repo.Set(ctx, "@alice:example.org", persist.PresenceUnavailable, "hello", false, 9_000)
	if err != nil {
		t.Fatalf("storing a second change: %v", err)
	}
	if second <= first {
		t.Errorf("positions out of order: %d then %d", first, second)
	}

	// The window a sync reads is (since, until], and it is limited to the users asked
	// for — presence for a stranger is nobody's to see.
	third, err := repo.Set(ctx, "@bob:example.org", persist.PresenceOnline, "", true, 9_500)
	if err != nil {
		t.Fatalf("storing a second user: %v", err)
	}
	changed, err := repo.ListChangedFor(ctx, []string{"@alice:example.org", "@bob:example.org"}, first, second)
	if err != nil {
		t.Fatalf("reading the window: %v", err)
	}
	if len(changed) != 1 || changed[0].UserID != "@alice:example.org" {
		t.Errorf("window = %+v, want only alice", changed)
	}
	all, err := repo.ListChangedFor(ctx, []string{"@alice:example.org", "@bob:example.org"}, first, third)
	if err != nil {
		t.Fatalf("reading the wider window: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("window = %+v, want both users", all)
	}
	none, err := repo.ListChangedFor(ctx, nil, 0, third)
	if err != nil {
		t.Fatalf("reading with no users: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("an empty user list asked for %d rows, want none: presence for a user who shares no room is not this user's to see", len(none))
	}
}

// TestPresenceStreamSharedWithEvents checks the promise a sync token rests on: an event
// may not take a position a presence change already took.
func TestPresenceStreamSharedWithEvents(t *testing.T) {
	ctx := context.Background()
	_, db := newPresenceService(t)

	if err := db.Users.Create(ctx, &persist.User{UserID: "@alice:example.org", Localpart: "alice", PasswordHash: "x"}); err != nil {
		t.Fatalf("creating an account: %v", err)
	}
	if err := db.Rooms.Create(ctx, &persist.Room{RoomID: "!r:example.org", RoomVersion: "10", Creator: "@alice:example.org"}); err != nil {
		t.Fatalf("creating a room: %v", err)
	}
	eventPosition, err := db.Events.NextStreamOrdering(ctx)
	if err != nil {
		t.Fatalf("taking an event position: %v", err)
	}
	if err := db.Events.Insert(ctx, &persist.Event{
		EventID: "$e1:example.org", RoomID: "!r:example.org", Type: "m.room.message",
		Sender: "@alice:example.org", JSON: []byte(`{}`), StreamOrdering: eventPosition,
	}); err != nil {
		t.Fatalf("inserting the event: %v", err)
	}

	presencePosition, err := db.Presence.Set(ctx, "@alice:example.org", persist.PresenceOnline, "", true, 1_000)
	if err != nil {
		t.Fatalf("storing presence: %v", err)
	}
	if presencePosition <= eventPosition {
		t.Fatalf("a presence change took position %d, which an event already had (%d)", presencePosition, eventPosition)
	}
	nextEvent, err := db.Events.NextStreamOrdering(ctx)
	if err != nil {
		t.Fatalf("taking another event position: %v", err)
	}
	if nextEvent <= presencePosition {
		t.Errorf("a later event took position %d, behind the presence change at %d", nextEvent, presencePosition)
	}
	max, err := db.StreamPosition(ctx)
	if err != nil {
		t.Fatalf("reading the stream position: %v", err)
	}
	if max != nextEvent && max != presencePosition {
		t.Errorf("the stream position is %d, want the highest of %d and %d", max, nextEvent, presencePosition)
	}
}

// TestPresenceService checks what the service adds to the store: refusing a value it has
// no meaning for, and not writing a status that did not change.
func TestPresenceService(t *testing.T) {
	ctx := context.Background()
	service, db := newPresenceService(t)

	if err := service.Set(ctx, "@alice:example.org", "sleeping", ""); err == nil {
		t.Errorf("an unknown presence value was accepted")
	}
	if err := service.Set(ctx, "@alice:example.org", persist.PresenceUnavailable, "busy"); err != nil {
		t.Fatalf("setting a known value: %v", err)
	}
	status, err := service.Status(ctx, "@alice:example.org")
	if err != nil {
		t.Fatalf("reading the status: %v", err)
	}
	if status.Presence != persist.PresenceUnavailable || status.StatusMsg != "busy" || !status.Known {
		t.Errorf("status = %+v", status)
	}
	if status.LastActiveAgo < 0 {
		t.Errorf("last active ago = %d", status.LastActiveAgo)
	}

	before, err := db.Presence.Get(ctx, "@alice:example.org")
	if err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	if err := service.SetIfChanged(ctx, "@alice:example.org", persist.PresenceUnavailable); err != nil {
		t.Fatalf("setting the same value again: %v", err)
	}
	after, err := db.Presence.Get(ctx, "@alice:example.org")
	if err != nil {
		t.Fatalf("reading the row again: %v", err)
	}
	if after.StreamOrdering != before.StreamOrdering {
		t.Errorf("an unchanged status took a new stream position (%d then %d), which would announce the user again",
			before.StreamOrdering, after.StreamOrdering)
	}
	if err := service.SetIfChanged(ctx, "@alice:example.org", persist.PresenceOnline); err != nil {
		t.Fatalf("setting a different value: %v", err)
	}
	changed, err := db.Presence.Get(ctx, "@alice:example.org")
	if err != nil {
		t.Fatalf("reading the row after a change: %v", err)
	}
	if changed.StreamOrdering <= after.StreamOrdering {
		t.Errorf("a real change did not take a new position")
	}
	if !changed.Active {
		t.Errorf("going back to online did not make the user active: %+v", changed)
	}

	// A user this server has never reported is answered as offline, not as an error.
	unknown, err := service.Status(ctx, "@stranger:example.org")
	if err != nil {
		t.Fatalf("reading an unknown user: %v", err)
	}
	if unknown.Presence != persist.PresenceOffline || unknown.Known {
		t.Errorf("unknown user = %+v", unknown)
	}
}

// TestPresenceEnsureDefaults covers accounts that existed before this code did.
func TestPresenceEnsureDefaults(t *testing.T) {
	ctx := context.Background()
	service, db := newPresenceService(t)

	for _, localpart := range []string{"first", "second"} {
		if err := db.Users.Create(ctx, &persist.User{
			UserID: "@" + localpart + ":example.org", Localpart: localpart, PasswordHash: "x",
		}); err != nil {
			t.Fatalf("creating an account: %v", err)
		}
	}
	created, err := service.EnsureDefaults(ctx)
	if err != nil {
		t.Fatalf("giving the existing accounts presence: %v", err)
	}
	if created != 2 {
		t.Errorf("created %d presence entries, want 2", created)
	}
	again, err := service.EnsureDefaults(ctx)
	if err != nil {
		t.Fatalf("running the sweep twice: %v", err)
	}
	if again != 0 {
		t.Errorf("the second sweep created %d entries, want none", again)
	}
	status, err := service.Status(ctx, "@first:example.org")
	if err != nil {
		t.Fatalf("reading the swept account: %v", err)
	}
	if status.Presence != persist.PresenceOnline || !status.Active || status.LastActive == 0 {
		t.Errorf("swept account = %+v", status)
	}
	if status.LastActive < time.Now().Add(-time.Hour).UnixMilli() {
		t.Errorf("the swept timestamp is not from this run: %d", status.LastActive)
	}
}
