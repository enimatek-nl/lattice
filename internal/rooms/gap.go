package rooms

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/roomversion"
)

// NameRemotePDUs gives events another server sent their identifiers, in the order they were given,
// empty where an event cannot be named in this room.
//
// This exists because from room version 2 a PDU does not carry its own identifier: the identifier
// is a hash of the event's contents under the room version's rules, so only a component that knows
// which room the events belong to can say what any of them is called. The room service is that
// component, and it is the only one.
//
// It matters wherever a batch of PDUs has to be handled as a set. A fetcher that keys the events it
// has just received by the `event_id` field silently collects nothing for a modern room — and
// silently means the count of what arrived is wrong too, so a peer that answered a request in full
// looks like a peer that answered nothing. Naming them first is also what makes it possible to
// order them, since the citations inside an event name hashes, and an event that has not been named
// cannot be recognised as its own predecessor.
func (s *Service) NameRemotePDUs(ctx context.Context, roomID string, pdus [][]byte) ([]string, error) {
	names := make([]string, len(pdus))
	if len(pdus) == 0 {
		return names, nil
	}
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot name events for room %q: %w", roomID, err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return nil, fmt.Errorf("rooms: room %q uses room version %s, which this server cannot name events in",
			roomID, room.RoomVersion)
	}
	for index, raw := range pdus {
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			continue
		}
		// A sender that did label an event may be trusted with its label: the hash is what this
		// server files under, and an event whose label disagrees with its contents is filed by
		// contents, which is the only answer two servers can agree on.
		id, err := events.EventID(rules, event)
		if err != nil {
			continue
		}
		names[index] = id
	}
	return names, nil
}

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

// MissingEvents names what this server would have to hold to place the event, and CitedEvent
// names the event that could not be placed. Both gap errors answer both, so a caller that can go
// and fetch has one question to ask rather than two type switches.
//
// CitedEvent matters more than it looks: from room version 2 onwards a PDU does not carry its own
// ID — the ID is a hash of the event, computed per room version, which is why only this package can
// name the event it just declined to place. A caller that needs to ask a peer for the history
// behind that event, or to name it in the per-event answer of a transaction, cannot recover the ID
// from the bytes, and getting it wrong looks like a server that never asks.
func (e *MissingPredecessorsError) MissingEvents() []string { return e.Missing }
func (e *MissingPredecessorsError) CitedEvent() string      { return e.EventID }

// MissingEventsError is what both gap errors have in common: an event this server could not
// place, and the names of the events that would let it. A caller with a server to ask matches on
// this rather than on each concrete type, because the action is the same whichever half of the
// citation was absent.
type MissingEventsError interface {
	error
	MissingEvents() []string
	// CitedEvent names the event this server could not place. It is computed here because it is
	// not in the bytes the caller has: recent room versions carry no event ID field.
	CitedEvent() string
}

// ForwardExtremities lists the events at the frontier of this server's knowledge of a room: the
// events it holds that nothing else cites as their predecessor.
//
// This is what a peer needs as `earliest_events` when this server asks for a hole to be filled:
// it says "I know everything up to here" without making the asker guess how far back this
// server's copy of the room reaches. The list is capped because the parameter is a walk instruction
// rather than a summary, and a room in the middle of a long unmerged fork can have more extremities
// than a peer should have to plan a walk around.
func (s *Service) ForwardExtremities(ctx context.Context, roomID string) ([]string, error) {
	extremities, err := s.events.ForwardExtremities(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read the frontier of a room's history: %w", err)
	}
	const cap = 10
	if len(extremities) > cap {
		sort.Strings(extremities)
		extremities = extremities[len(extremities)-cap:]
	}
	return extremities, nil
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
// happened; newest-first would make it rebuild the graph backwards for no reason. And the answer
// is what lies between: the events the peer named as its ceiling are descended from but not
// returned, because the question it asked was about their predecessors.
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
	// The events the peer named as its ceiling are where the walk starts, not part of what it
	// asked for: the question is "what are the previous events of these". Handing the ceiling
	// back is answering a question that was not asked, and a peer counting on the answer to be
	// the hole it is missing gets one event too many.
	ceiling := make(map[string]bool, len(latest))
	queue := make([]string, 0, len(latest))
	for _, eventID := range earliest {
		seen[eventID] = true
	}
	for _, eventID := range latest {
		if seen[eventID] {
			continue
		}
		seen[eventID] = true
		ceiling[eventID] = true
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
		if !ceiling[eventID] {
			found = append(found, event)
		}
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
