package unit

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/enimatek/lattice/internal/persist"
)

// TestDeviceListChanges checks the two things the sync section rests on: a change takes a
// position in the shared stream, and a read is limited to the rooms that decide who may
// hear about it.
func TestDeviceListChanges(t *testing.T) {
	ctx := context.Background()
	db, err := persist.Open(filepath.Join(t.TempDir(), "devicelists.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	eventPosition, err := db.Events.NextStreamOrdering(ctx)
	if err != nil {
		t.Fatalf("taking an event position: %v", err)
	}
	if err := db.Events.Insert(ctx, &persist.Event{
		EventID: "$join:example.org", RoomID: "!room:example.org", Type: "m.room.member",
		Sender: "@charlie:example.org", JSON: []byte(`{}`), StreamOrdering: eventPosition,
	}); err != nil {
		t.Fatalf("inserting the membership event: %v", err)
	}

	changed, err := db.DeviceLists.Record(ctx, "!room:example.org", "@charlie:example.org", persist.DeviceListChanged)
	if err != nil {
		t.Fatalf("recording a change: %v", err)
	}
	if changed <= eventPosition {
		t.Errorf("the change took position %d, which the membership event already had (%d)", changed, eventPosition)
	}
	left, err := db.DeviceLists.Record(ctx, "!room:example.org", "@charlie:example.org", persist.DeviceListLeft)
	if err != nil {
		t.Fatalf("recording a leave: %v", err)
	}
	if left <= changed {
		t.Errorf("positions out of order: %d then %d", changed, left)
	}

	// The window is (since, until], oldest first, and only the rooms asked about.
	rows, err := db.DeviceLists.ListInRooms(ctx, []string{"!room:example.org"}, eventPosition, changed)
	if err != nil {
		t.Fatalf("reading the window: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != persist.DeviceListChanged {
		t.Errorf("window = %+v, want only the change", rows)
	}
	both, err := db.DeviceLists.ListInRooms(ctx, []string{"!room:example.org"}, eventPosition, left)
	if err != nil {
		t.Fatalf("reading the wider window: %v", err)
	}
	if len(both) != 2 || both[0].Kind != persist.DeviceListChanged || both[1].Kind != persist.DeviceListLeft {
		t.Errorf("window = %+v, want the change then the leave", both)
	}
	elsewhere, err := db.DeviceLists.ListInRooms(ctx, []string{"!another:example.org"}, 0, left)
	if err != nil {
		t.Fatalf("reading another room: %v", err)
	}
	if len(elsewhere) != 0 {
		t.Errorf("a change in another room was returned: %+v", elsewhere)
	}
	none, err := db.DeviceLists.ListInRooms(ctx, nil, 0, left)
	if err != nil {
		t.Fatalf("reading with no rooms: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("an empty room list asked for %d rows, want none", len(none))
	}

	// A leave is still readable after the user is gone from the room, which is why the
	// change carries the room it happened in.
	after, err := db.DeviceLists.ListInRooms(ctx, []string{"!room:example.org"}, changed, left)
	if err != nil {
		t.Fatalf("reading the leave alone: %v", err)
	}
	if len(after) != 1 || after[0].Kind != persist.DeviceListLeft {
		t.Errorf("window = %+v, want only the leave", after)
	}
}
