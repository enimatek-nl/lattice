package rooms

import (
	"context"
	"fmt"

	"github.com/enimatek/lattice/internal/eventfilter"
	"github.com/enimatek/lattice/internal/stream"
)

// ContextRequest is the window around one event, as `/context` asks for it.
type ContextRequest struct {
	// Limit is the ceiling on the *sum* of the two sides, which is what the specification
	// says and not two limits of `limit` each. It is a pointer because this endpoint has three
	// states where the others have two: absent (the specification's default of ten), zero (an
	// explicit empty window — the event and its tokens only, which is how a client uses this
	// endpoint as a source of pagination tokens), and a number. A plain int cannot tell the
	// first two apart, and getting it wrong either returns ten events to a caller that asked for
	// none or returns none to a caller that asked for the default.
	Limit *int
	// Filter applies to `events_before`, `events_after` and `state`, and deliberately not to
	// the requested event: a client that asked for the context of this event is looking at
	// this event.
	Filter eventfilter.RoomEventFilter
}

// ContextPage is the response. The two tokens are the point of the endpoint for a client:
// they are the same tokens `/messages` takes, so a jump to one event turns into a walk in
// either direction without a second round trip to find out where the event sits.
type ContextPage struct {
	Event        map[string]any   `json:"event"`
	EventsBefore []map[string]any `json:"events_before"`
	EventsAfter  []map[string]any `json:"events_after"`
	State        []map[string]any `json:"state"`
	Start        string           `json:"start"`
	End          string           `json:"end"`
}

// Context returns the events around one event.
//
// It is built from `/messages` rather than beside it, and that is the whole design: the two
// reads have to agree about what the requester may see (a member reads the whole timeline, a
// former member reads up to the moment they left, a stranger reads only what the room opened
// with `world_readable`), about which of those events a redaction has taken content from, and
// about what a pagination token means. A second implementation of that would drift, and the
// drift would show up as a client being told one thing about an event by `/context` and
// another by `/messages` a second later.
//
// The tokens name the edges of what was actually returned, so that paginating from them
// continues rather than repeats: `start` is where the oldest returned `events_before` entry
// sits, `end` just after the newest `events_after` entry. When a side is empty — a window at
// the end of the room, or `limit: 0` — the token names the requested event's own position
// instead, which is the nearest true thing and always usable: walking backwards from it gives
// what precedes the event, and forwards from one past it gives what follows.
func (s *Service) Context(ctx context.Context, roomID, eventID, requester string, req ContextRequest) (ContextPage, error) {
	// Whose room this is, asked before the event is read. [Service.Event] folds "you may not see
	// this" into 404, which is right for `/event/{eventID}` and wrong here: `/context` addresses
	// the room, and a caller who is not in it is told the same thing `/messages` tells them. With
	// that settled first, the read below can only 404 for an event that is genuinely not there or
	// genuinely not readable by someone who *is* entitled to be asking about the room.
	if err := s.requireHistoryAccess(ctx, roomID, requester); err != nil {
		return ContextPage{}, err
	}
	// The requested event, with the errors of `GET /event` for everything else: an event this
	// server does not hold, or one this reader may not read, answers 404.
	event, err := s.Event(ctx, roomID, eventID, requester)
	if err != nil {
		return ContextPage{}, err
	}
	row, err := s.events.ByID(ctx, eventID)
	if err != nil {
		// [Service.Event] has just read this event, so this is a storage fault and not a
		// missing row.
		return ContextPage{}, fmt.Errorf("rooms: cannot locate the event whose context was asked for: %w", err)
	}
	position := stream.Position(row.StreamOrdering)

	// The two budgets add up to the limit rather than each being the limit. The extra unit of an
	// odd limit goes to the events after, because a client scrolling from a jump is usually
	// walking forwards and the more useful half should not be the one that loses the remainder.
	limit := defaultHistoryLimit
	if req.Limit != nil {
		limit = *req.Limit
	}
	if limit < 0 {
		limit = 0
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	beforeBudget := limit / 2
	afterBudget := limit - beforeBudget

	// Both walks start at the event's own position, which is exclusive in either direction — so
	// the event is neither counted twice nor missed. A side with no budget is not walked at all:
	// [Service.History] reads a limit of zero as "the default of ten", and handing it the empty
	// half of an odd limit would turn a window of one into a window of eleven.
	var before, after HistoryPage
	if beforeBudget > 0 {
		var err error
		before, err = s.History(ctx, roomID, requester, HistoryRequest{
			From: position.String(), Dir: "b", Limit: beforeBudget, Filter: req.Filter,
		})
		if err != nil {
			return ContextPage{}, err
		}
	}
	if afterBudget > 0 {
		var err error
		after, err = s.History(ctx, roomID, requester, HistoryRequest{
			From: position.String(), Dir: "f", Limit: afterBudget, Filter: req.Filter,
		})
		if err != nil {
			return ContextPage{}, err
		}
	}

	page := ContextPage{
		Event:        event,
		EventsBefore: before.Chunk,
		EventsAfter:  after.Chunk,
		Start:        position.String(),
		End:          stream.Position(row.StreamOrdering + 1).String(),
	}
	if page.EventsBefore == nil {
		page.EventsBefore = []map[string]any{}
	}
	if page.EventsAfter == nil {
		page.EventsAfter = []map[string]any{}
	}
	// Where the returned window actually reached replaces the event's own position as the token,
	// because a client that has been shown five events before the one it asked about should
	// continue from the fifth and not be shown them again.
	if before.End != "" {
		page.Start = before.End
	}
	if after.End != "" {
		page.End = after.End
	}

	// The state is the state that goes with this window, which is the same answer `/messages`
	// gives for a chunk: the room's state as far as this requester may read it, or — under lazy
	// loading — the member events of the senders appearing in it.
	window := HistoryPage{Chunk: make([]map[string]any, 0, len(page.EventsBefore)+len(page.EventsAfter)+1), State: []map[string]any{}}
	window.Chunk = append(window.Chunk, page.EventsBefore...)
	window.Chunk = append(window.Chunk, event)
	window.Chunk = append(window.Chunk, page.EventsAfter...)
	if err := s.historyState(ctx, roomID, requester, window.Chunk, req.Filter.LazyLoadMembers, &window); err != nil {
		return ContextPage{}, err
	}
	page.State = window.State
	return page, nil
}
