package rooms

import (
	"context"
	"fmt"
	"github.com/enimatek/lattice/internal/events"

	"github.com/enimatek/lattice/internal/persist"
)

// recordStateAt notes which state an event was sent against.
//
// The rule is simple in the ordinary case and awkward in the one that matters:
//
//   - a state event changes one key, so its snapshot is its predecessor's snapshot with
//     that key replaced. Rooms that are not forked grow by one snapshot per state change,
//     not per message;
//   - a message keeps its predecessor's snapshot;
//   - an event with several predecessors, or predecessors that disagree about the state,
//     stands at a join of branches. There is no such thing as "the" state there until the
//     branches are resolved, so the predecessors' snapshots are handed to the algorithm the
//     room version names and the answer becomes the snapshot this event descends from.
func (s *Service) recordStateAt(ctx context.Context, event map[string]any, row *persist.Event) error {
	roomID, eventID := row.RoomID, row.EventID
	if roomID == "" || eventID == "" {
		return nil
	}

	base, err := s.baseStateGroup(ctx, roomID, event)
	if err != nil {
		return err
	}
	if row.StateKey == nil {
		return s.groups.Attach(ctx, eventID, base)
	}
	key := *row.StateKey
	entries, err := s.groups.Entries(ctx, base)
	if err != nil {
		return fmt.Errorf("rooms: cannot read the state an event descends from: %w", err)
	}
	updated := make([]persist.StateGroupEntry, 0, len(entries)+1)
	replaced := false
	for _, entry := range entries {
		if entry.Type == row.Type && entry.StateKey == key {
			entry.EventID = eventID
			replaced = true
		}
		entry.ID = 0
		updated = append(updated, entry)
	}
	if !replaced {
		updated = append(updated, persist.StateGroupEntry{
			Type: row.Type, StateKey: key, EventID: eventID,
		})
	}
	group, err := s.groups.EnsureGroup(ctx, roomID, updated)
	if err != nil {
		return fmt.Errorf("rooms: cannot record a state snapshot: %w", err)
	}
	return s.groups.Attach(ctx, eventID, group)
}

// baseStateGroup returns the snapshot a new event descends from: its predecessors' shared
// snapshot, or an empty one for a create event.
func (s *Service) baseStateGroup(ctx context.Context, roomID string, event map[string]any) (int64, error) {
	prevs := events.ReferencedIDs(event, "prev_events")
	if len(prevs) == 0 {
		return s.groups.EmptyGroup(ctx, roomID)
	}
	var known []int64
	for _, prev := range prevs {
		groupID, err := s.groups.GroupOf(ctx, prev)
		if err != nil {
			if notFound(err) {
				// A predecessor whose snapshot was never recorded: an event that arrived
				// before this feature existed, or one that reached us out of order. Falling
				// back to an empty snapshot would claim the room had no state, which is
				// worse than not claiming one at all.
				continue
			}
			return 0, fmt.Errorf("rooms: cannot read a predecessor's state snapshot: %w", err)
		}
		known = append(known, groupID)
	}
	if len(known) == 0 {
		return s.groups.EmptyGroup(ctx, roomID)
	}
	if len(uniqueGroups(known)) == 1 {
		return known[0], nil
	}
	return s.resolveGroups(ctx, roomID, known)
}
