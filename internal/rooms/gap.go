package rooms

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/enimatek/lattice/internal/persist"
)

// MissingPredecessorsError says that an event offered to this server cites predecessors this
// server does not hold. It is deliberately not a refusal: the event may be perfectly good, and this
// server's inability to place it is a fact about this server's copy of the room. A caller with a
// server to ask — the one that just offered the event, usually — can go and get what is missing and
// offer it again.
type MissingPredecessorsError struct {
	EventID string
	RoomID  string
	// Missing names the cited predecessors that are not held here.
	Missing []string
}

// Error implements error, in the words a federation sender is answered with.
func (e *MissingPredecessorsError) Error() string {
	return fmt.Sprintf("its predecessors are not stored here: %d of the events it cites are missing",
		len(e.Missing))
}

// MissingPredecessors names the holes in a room's history: events this room's own events cite as
// predecessors, and which this server does not hold. They are not damage. A room that was joined
// rather than created here has holes by construction — a join cites the room's forward
// extremities, and the state a resident server hands over does not include them — and a room
// being told about through a partial relay can have them too.
//
// Events this server has already refused are left out. A refusal is a verdict about bytes that
// will not change, so chasing a refused event would be asking a remote server for something this
// server has already decided to reject, on every pass, forever.
func (s *Service) MissingPredecessors(ctx context.Context, roomID string, limit int) ([]string, error) {
	ids, err := s.events.MissingPredecessors(ctx, roomID, limit)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read the gaps in a room's history: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	refused, err := s.events.Rejections(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read what this server refused in a room's history: %w", err)
	}
	if len(refused) == 0 {
		return ids, nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, verdict := refused[id]; verdict {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

// ResolveOutliers clears the outlier flag from the events this server can now place in a room's
// history, and returns how many it cleared.
//
// An event stops being an outlier when every event it cites as a predecessor is held here: its
// place relative to them is known, which is all the flag ever claimed to be missing. The walk is
// repeated until it stops making progress, because a chain of unlinked events is usually linked
// from one end — filling the oldest hole is what lets the newer one be placed, and that in turn
// places the one after it.
//
// A predecessor that this server holds but has soft-failed still counts as held. Soft-failure is
// a judgement about a conflict in the room's state, not about whether the event sits in the
// history; keeping a perfectly good successor unlinked for it would be making a claim about the
// graph that this server has not established.
func (s *Service) ResolveOutliers(ctx context.Context, roomID string) (int, error) {
	cleared := 0
	for {
		outliers, err := s.events.Outliers(ctx, roomID)
		if err != nil {
			return cleared, fmt.Errorf("rooms: cannot read the unlinked events of a room: %w", err)
		}
		progress := 0
		for _, eventID := range outliers {
			predecessors, err := s.events.References(ctx, eventID, persist.EdgePrev)
			if err != nil {
				return cleared, fmt.Errorf("rooms: cannot read what an unlinked event cites: %w", err)
			}
			if len(predecessors) == 0 {
				// Nothing to link it to: this is an event with no predecessors, and there was
				// never a hole under it.
				if err := s.events.MarkOutlier(ctx, eventID, false); err != nil {
					return cleared, fmt.Errorf("rooms: cannot link an event this server can place: %w", err)
				}
				cleared++
				progress++
				continue
			}
			held, err := s.events.ByIDs(ctx, predecessors)
			if err != nil {
				return cleared, fmt.Errorf("rooms: cannot read the predecessors of an unlinked event: %w", err)
			}
			if len(held) < len(predecessors) {
				continue
			}
			if err := s.events.MarkOutlier(ctx, eventID, false); err != nil {
				return cleared, fmt.Errorf("rooms: cannot link an event this server can place: %w", err)
			}
			cleared++
			progress++
		}
		if progress == 0 {
			if cleared > 0 {
				s.log.Debug("rooms: more of a room's history is linked now",
					slog.String("room_id", roomID), slog.Int("linked", cleared))
			}
			return cleared, nil
		}
	}
}
