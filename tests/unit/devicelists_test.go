package unit

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/enimatek/lattice/internal/persist"
)

// TestDeviceListChanges checks the two things the sync section rests on: a change takes a
// position in the shared stream, and it is addressed to the user who is allowed to hear it.
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

	// Charlie joins, so Alice — who was already there — is told to fetch his keys.
	changed, err := db.DeviceLists.Record(ctx, "!room:example.org", "@alice:example.org", "@charlie:example.org", persist.DeviceListChanged)
	if err != nil {
		t.Fatalf("recording a change: %v", err)
	}
	if changed <= eventPosition {
		t.Errorf("the change took position %d, which the membership event already had (%d)", changed, eventPosition)
	}
	// Charlie is told about the people he just met.
	if _, err := db.DeviceLists.Record(ctx, "!room:example.org", "@charlie:example.org", "@alice:example.org", persist.DeviceListChanged); err != nil {
		t.Fatalf("recording the reverse change: %v", err)
	}

	// A sync reads its own post, oldest first.
	rows, err := db.DeviceLists.ListFor(ctx, "@alice:example.org", eventPosition, changed)
	if err != nil {
		t.Fatalf("reading alice's post: %v", err)
	}
	if len(rows) != 1 || rows[0].UserID != "@charlie:example.org" || rows[0].Kind != persist.DeviceListChanged {
		t.Errorf("alice's post = %+v, want one change about charlie", rows)
	}
	// Nobody else reads it.
	elsewhere, err := db.DeviceLists.ListFor(ctx, "@nobody:example.org", 0, changed)
	if err != nil {
		t.Fatalf("reading somebody else's post: %v", err)
	}
	if len(elsewhere) != 0 {
		t.Errorf("a change was delivered to a user it was not addressed to: %+v", elsewhere)
	}
	empty, err := db.DeviceLists.ListFor(ctx, "", 0, changed)
	if err != nil {
		t.Fatalf("reading with no observer: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("an empty observer asked for %d rows, want none", len(empty))
	}

	// Charlie leaves. Both sides are told, and the notice addressed to charlie is readable
	// even though he has gone: this is what a room-shaped record cannot do, and it is the
	// reason the row names its observer.
	left, err := db.DeviceLists.Record(ctx, "!room:example.org", "@charlie:example.org", "@alice:example.org", persist.DeviceListLeft)
	if err != nil {
		t.Fatalf("recording charlie's departure: %v", err)
	}
	if _, err := db.DeviceLists.Record(ctx, "!room:example.org", "@alice:example.org", "@charlie:example.org", persist.DeviceListLeft); err != nil {
		t.Fatalf("recording alice's notice: %v", err)
	}
	// Everything charlie has not heard arrives in order: the change he caused by joining,
	// then the notice that the people he left are no longer his to track.
	gone, err := db.DeviceLists.ListFor(ctx, "@charlie:example.org", changed, left)
	if err != nil {
		t.Fatalf("reading the departed user's post: %v", err)
	}
	if len(gone) != 2 || gone[0].Kind != persist.DeviceListChanged || gone[1].Kind != persist.DeviceListLeft {
		kinds := make([]string, 0, len(gone))
		for _, row := range gone {
			kinds = append(kinds, row.Kind)
		}
		t.Errorf("charlie's post after leaving = %v, want the join change then the leave", kinds)
	}

	// The window is (since, until]: a change at the token's own position has already been
	// reported.
	again, err := db.DeviceLists.ListFor(ctx, "@charlie:example.org", left, left)
	if err != nil {
		t.Fatalf("re-reading the window: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("a change at the token's own position was reported again: %+v", again)
	}

	// Recording a change for no observer is not an error and takes no position: a user who
	// has nobody to tell has nothing to report.
	position, err := db.DeviceLists.Record(ctx, "!room:example.org", "", "@anyone:example.org", persist.DeviceListChanged)
	if err != nil {
		t.Fatalf("recording for nobody: %v", err)
	}
	if position != 0 {
		t.Errorf("a change to nobody took position %d, want no position", position)
	}
}
