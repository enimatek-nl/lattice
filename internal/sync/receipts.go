package sync

import (
	"context"
	"fmt"

	"github.com/enimatek/lattice/internal/persist"
)

// Receipts reach a client as ephemeral events: `m.receipt` objects in the room's ephemeral
// section. They are not part of the room's graph and have no event ID, because a receipt is a
// statement about a person's reading rather than something the room agreed to.
//
// The window works the way presence's does. A receipt carries the stream position its last move
// took, so an incremental sync sends the receipts that moved inside it. A first sync has no
// window, and the client needs the current answer, so it is sent: the newest receipt of each
// kind from each user this room has.

// receiptScanLimit bounds how many receipts one room contributes to one response. A receipt is
// small, but a room with ten thousand members is a hundred thousand objects a client did not
// ask for on a first sync.
const receiptScanLimit = 1000

// receiptEvents reads a room's receipts for this window and returns them as ephemeral events.
func (s *Service) receiptEvents(ctx context.Context, roomID string, since, until int64) ([]Event, error) {
	rows, err := s.receiptsForRoom(ctx, roomID, since, until)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	// The shape the specification gives: the event that was read, then the kind of receipt,
	// then who read it and when.
	byEvent := map[string]map[string]map[string]any{}
	order := []string{}
	for _, row := range rows {
		kinds, seen := byEvent[row.EventID]
		if !seen {
			kinds = map[string]map[string]any{}
			byEvent[row.EventID] = kinds
			order = append(order, row.EventID)
		}
		readers, ok := kinds[row.ReceiptType]
		if !ok {
			readers = map[string]any{}
			kinds[row.ReceiptType] = readers
		}
		position := map[string]any{"ts": row.TS}
		if row.ThreadScoped {
			threadID := row.ThreadID
			if threadID == "" {
				threadID = "m.main"
			}
			position["thread_id"] = threadID
		}
		readers[row.UserID] = position
	}
	out := make([]Event, 0, len(order))
	for _, eventID := range order {
		// A receipt event has no `room_id` on the client-Server API: the room's own section
		// says which room it belongs to. The field exists over federation, and putting it
		// here would let a client believe it had to strip it out.
		out = append(out, Event{
			"type": "m.receipt",
			// The content is keyed by the event that was read; the kind of receipt and
			// who read it sit inside that.
			"content": map[string]any{eventID: byEvent[eventID]},
		})
	}
	return out, nil
}

// receiptsForRoom reads the rows, oldest first.
func (s *Service) receiptsForRoom(ctx context.Context, roomID string, since, until int64) ([]*persist.Receipt, error) {
	rows, err := s.db.Receipts.ListForRoom(ctx, roomID, since, until, receiptScanLimit)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot read the receipts of %s: %w", roomID, err)
	}
	out := make([]*persist.Receipt, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out, nil
}
