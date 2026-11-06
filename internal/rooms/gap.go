package rooms

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

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

// EventsBetween answers "what does this other server lack between these two points in the
// room's history", which is the question POST /get_missing_events asks.
//
// The walk is a breadth-first descent through `prev_events` starting at `latest_events` — the
// events the peer says it already holds — and it does not pass through `earliest_events`: those
// are the floor the peer set, so an event at or below that floor is neither returned nor used as
// a route to something below it. `minDepth` is a second floor the peer may set. `limit` caps the
// answer, because this is a synchronous request on a path where a peer is waiting; the
// specification caps it too, and a server that walks a whole room to answer is inviting its peer
// to make it do that.
//
// Events this server has *refused* are not handed over. A soft-failed event is: it sits in the
// room's history, and the peer may already hold events that cite it — the same reasoning that
// makes `GET /event` return it.
//
// The answer is oldest first. A peer filling a hole wants to lay events down in the order they
// happened; newest-first would make it rebuild the graph backwards for no reason.
func (s *Service) EventsBetween(ctx context.Context, roomID string, earliest, latest []string, limit int, minDepth int64) ([]*persist.Event, error) {
	if limit <= 0 {
		limit = missingEventsDefaultLimit
	}
	if limit > missingEventsMaxLimit {
		limit = missingEventsMaxLimit
	}
	// A seed that has already been seen is never queued, so the floor set does double duty:
	// it is excluded from the answer and it blocks the walk underneath it.
	seen := make(map[string]bool, len(earliest)+len(latest))
	queue := make([]string, 0, len(latest))
	for _, eventID := range earliest {
		seen[eventID] = true
	}
	for _, eventID := range latest {
		if seen[eventID] {
			continue
		}
		seen[eventID] = true
		queue = append(queue, eventID)
	}
	found := make([]*persist.Event, 0, limit)
	for len(queue) > 0 && len(found) < limit {
		eventID := queue[0]
		queue = queue[1:]
		event, err := s.events.ByID(ctx, eventID)
		if err != nil {
			if notFound(err) {
				// The peer cites something this server does not hold. That is not an
				// error to report: it is the reason it is asking.
				continue
			}
			return nil, fmt.Errorf("rooms: cannot read an event while filling a peer's gap: %w", err)
		}
		// An event is only in this room's history if this server says it is. A peer that
		// names an event from another room gets nothing from it, and the walk does not go
		// into another room's graph.
		if event.RoomID != roomID {
			continue
		}
		if event.Depth < minDepth {
			continue
		}
		found = append(found, event)
		predecessors, err := s.events.References(ctx, eventID, persist.EdgePrev)
		if err != nil {
			return nil, fmt.Errorf("rooms: cannot read what an event cites while filling a peer's gap: %w", err)
		}
		for _, id := range predecessors {
			if seen[id] {
				continue
			}
			seen[id] = true
			queue = append(queue, id)
		}
	}
	if len(found) == 0 {
		return found, nil
	}
	ids := make([]string, 0, len(found))
	for _, event := range found {
		ids = append(ids, event.EventID)
	}
	refused, err := s.events.Rejections(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read which of these events this server refused: %w", err)
	}
	kept := found[:0]
	for _, event := range found {
		if _, rejected := refused[event.EventID]; rejected {
			continue
		}
		kept = append(kept, event)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].Depth != kept[j].Depth {
			return kept[i].Depth < kept[j].Depth
		}
		if kept[i].OriginServerTS != kept[j].OriginServerTS {
			return kept[i].OriginServerTS < kept[j].OriginServerTS
		}
		// Depth and timestamp can tie, and they do tie for the events a room is created
		// with: a creation batch is one depth deep and is often written inside the same
		// millisecond. The tie is broken by the order this server took them in, which is the
		// only record of which came first when the room's own events cannot say.
		return kept[i].StreamOrdering < kept[j].StreamOrdering
	})
	return kept, nil
}

// The bounds this server puts on one answer. The specification fixes the default (`limit` of 10)
// and nothing above it, so the ceiling is this server's own resource limit rather than a protocol
// figure, and it is named as such: a peer must not be able to make a server walk and sign a whole
// room's history in one synchronous request.
const (
	missingEventsDefaultLimit = 10
	missingEventsMaxLimit     = 100
)
