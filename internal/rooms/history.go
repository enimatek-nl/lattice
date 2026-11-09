package rooms

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/eventfilter"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/redaction"
	"github.com/enimatek/lattice/internal/stream"
)

// Pagination limits. The default follows the Client-Server specification; the ceiling
// bounds one request, because a client asking for the whole history of a large room
// would otherwise hold a connection while the server reads it.
const (
	defaultHistoryLimit = 10
	maxHistoryLimit     = 1000
)

// HistoryRequest is one page of a room's timeline, as `/messages` asks for it.
type HistoryRequest struct {
	// From is the raw token. Empty means "from the last visible event in the direction
	// the client is walking", which is what the specification allows since v1.3.
	From string
	// To is the raw token to stop at, inclusive when it is given.
	To string
	// Dir is "f" for chronological or "b" for reverse chronological.
	Dir string
	// Limit is the maximum number of events; zero means the default.
	Limit int
	// Filter is the room event filter the client sent: the type, sender and room lists and
	// `contains_url` decide what belongs in the page, and `lazy_load_members` decides which
	// member events come with it.
	Filter eventfilter.RoomEventFilter
}

// HistoryPage is the response.
type HistoryPage struct {
	Chunk []map[string]any `json:"chunk"`
	State []map[string]any `json:"state"`
	// Start echoes the position the page began at. The specification says it is the
	// value given in `from`; when the client sent no `from`, it is the position this
	// server started from, which the client can hand straight back.
	Start string `json:"start"`
	// End is the token to continue from. It is omitted when there is nothing further this
	// requester may see, which is how a client knows to stop paginating. The specification is
	// explicit, and Complement's own pagination loop depends on it: "If no further events are
	// available (either because we have reached the start of the timeline, or because the user
	// does not have permission to see any more events), this property is omitted from the
	// response." A page trimmed by a filter still answers a token while the room holds
	// anything further back, whether the filter would keep it or not.
	End string `json:"end,omitempty"`
}

// History returns a page of a room's timeline.
//
// Three things shape the answer beyond simply reading rows:
//
//   - The requester's entitlement. A room that does not exist answers 403 rather than
//     404, matching the reference behaviour: saying 404 tells a non-member that the room
//     identifier is not in use. Beyond that, the same rule as the other room reads
//     applies — a current member reads everything, a former member reads up to the
//     moment they left, and someone who was never in the room reads only what the room
//     opened with `history_visibility: world_readable`.
//   - Direction. `dir=b` returns reverse chronological events, `dir=f` chronological, so
//     the page reads in the order the client is walking it.
//   - When to stop saying there is more. `end` is present only while further events are
//     reachable, and a requester whose horizon cut off everything ahead of them has
//     nothing reachable.
//
// Event returns one stored event to a requester entitled to read the room's history. A
// redacted event comes back in its redacted form, with the redaction that did it under
// `unsigned.redacted_because`.
func (s *Service) Event(ctx context.Context, roomID, eventID, requester string) (map[string]any, error) {
	if err := s.requireHistoryAccess(ctx, roomID, requester); err != nil {
		var refused *matrixerr.Error
		if errors.As(err, &refused) {
			// `/event/{eventID}` is the one room read where the specification folds "not
			// allowed" into "not here": it answers 404 when the event "does not exist or is not
			// visible to the requester". `/messages` keeps its 403, because there the room is
			// what is being addressed and saying what the requester would need to be to read it
			// is useful. Here it would only say that an event with that identifier exists.
			return nil, matrixerr.Newf(matrixerr.CodeNotFound, "Event %q is not known to this server.", eventID)
		}
		return nil, err
	}
	row, err := s.events.ByID(ctx, eventID)
	if err != nil {
		if notFound(err) {
			return nil, matrixerr.Newf(matrixerr.CodeNotFound, "Event %q is not known to this server.", eventID)
		}
		return nil, fmt.Errorf("rooms: cannot read the event: %w", err)
	}
	// An event in another room is not this room's to hand over, and the answer is the
	// same as for an event that does not exist.
	if row.RoomID != roomID {
		return nil, matrixerr.Newf(matrixerr.CodeNotFound, "Event %q is not known to this server.", eventID)
	}
	// An event this reader is not entitled to reads the same as an event that does not exist.
	// `/event/{eventID}` is the one room read where the specification asks for that: it says the
	// answer is 404 when the event "does not exist or is not visible to the requester", so this
	// door does not tell a caller that a room holds such an event while refusing to hand it over.
	// `/messages` keeps its 403 for a non-member, because there the room itself is what is being
	// addressed.
	allowed, err := s.history.MayRead(ctx, row, requester)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot decide whether the reader may read this event: %w", err)
	}
	if !allowed {
		return nil, matrixerr.Newf(matrixerr.CodeNotFound, "Event %q is not known to this server.", eventID)
	}
	event, err := s.serve.ServeAs(ctx, row, requester)
	if errors.Is(err, redaction.ErrWithheld) {
		// The same answer as for an event this server has never heard of. Saying "I have
		// this redaction but not yet what it destroys" would tell the client about an event
		// it is not allowed to see yet.
		return nil, matrixerr.Newf(matrixerr.CodeNotFound, "Event %q is not known to this server.", eventID)
	}
	return event, err
}

func (s *Service) History(ctx context.Context, roomID, requester string, req HistoryRequest) (HistoryPage, error) {
	if err := s.requireHistoryAccess(ctx, roomID, requester); err != nil {
		return HistoryPage{}, err
	}
	var backwards bool
	switch req.Dir {
	case "b", "":
		backwards = true
	case "f":
		backwards = false
	default:
		return HistoryPage{}, matrixerr.New(matrixerr.CodeInvalidParam,
			"The dir parameter must be \"f\" or \"b\".")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}

	start, err := s.historyStart(ctx, roomID, req.From, backwards)
	if err != nil {
		return HistoryPage{}, err
	}
	// The page is read past the requester's horizon and then cut, rather than queried up
	// to it, so that `end` can tell the truth: a shorter page means the timeline ended,
	// while a full page cut by the horizon means the requester simply may not read
	// further.
	raw, err := s.events.EventsWindow(ctx, roomID, int64(start), backwards, limit+1)
	if err != nil {
		return HistoryPage{}, err
	}
	// A page that came back short may only mean this server was never told any more. Ask before
	// deciding the room ended here; see [Service.backfillToFill].
	raw, err = s.backfillToFill(ctx, roomID, start, backwards, limit, raw)
	if err != nil {
		return HistoryPage{}, err
	}
	asOf, err := s.readHorizon(ctx, roomID, requester)
	if err != nil {
		return HistoryPage{}, err
	}
	// Which of these events the requester may read at all, judged event by event against the
	// room's history visibility *at the moment each was written*. The horizon above is one cut
	// across the whole page; this is a different question, and a page can fail it in the middle:
	// a member of a `joined` room may read everything since their own join and nothing before it,
	// which is a hole in the middle of the room's timeline rather than an edge of it.
	visible, err := s.history.Decide(ctx, raw, requester)
	if err != nil {
		return HistoryPage{}, err
	}
	var stop int64
	if req.To != "" {
		to, err := stream.Parse(req.To)
		if err != nil {
			return HistoryPage{}, err
		}
		stop = int64(to)
	}

	page := HistoryPage{
		Chunk: make([]map[string]any, 0, limit),
		State: make([]map[string]any, 0),
		Start: start.String(),
	}
	reachedStop := false
	// lastSeen is how far into the window this page's last event sits. The filter can drop
	// events the window held, so the page's length is no longer an index into it, and where
	// the page really stopped is what `end` has to name.
	lastSeen := -1
	for rawIndex := range raw {
		event := raw[rawIndex]
		if len(page.Chunk) == limit {
			break
		}
		if stop != 0 && ((backwards && event.StreamOrdering < stop) || (!backwards && event.StreamOrdering > stop)) {
			reachedStop = true
			break
		}
		// Events written after the requester's horizon are not theirs to read. Going
		// backwards this is the case where a departed member starts from a token newer
		// than their leaving; going forwards it is the case where they ask what happened
		// after it.
		if asOf > 0 && event.StreamOrdering > asOf {
			continue
		}
		if !visible[event.EventID] {
			continue
		}
		body, err := s.serve.ServeAs(ctx, event, requester)
		if err != nil {
			if errors.Is(err, redaction.ErrWithheld) {
				// A redaction whose target has not arrived is left out of the page: the
				// client would learn that something was destroyed with no way to see it.
				continue
			}
			return HistoryPage{}, err
		}
		// The filter is applied to the event in its client form, so that it sees the same
		// content the client would: a redacted event has no URL to match on, because the URL
		// is gone from what the client is allowed to read.
		if !req.Filter.AllowsIn(roomID, body) {
			continue
		}
		page.Chunk = append(page.Chunk, body)
		lastSeen = rawIndex
	}
	// `end` is only worth handing back while there is something further this requester may
	// read. A short page means the timeline ended in that direction; reaching `to` means the
	// client asked for this page and no more. The events this window held but the requester may
	// not read are asked about rather than assumed: with a room whose history visibility changed
	// partway through, or a page whose tail is withheld redactions, a page can end in the middle
	// of events that are further along and perfectly readable, and telling the client there is
	// nothing more there is its own kind of wrong.
	further := false
	for index := lastSeen + 1; index < len(raw); index++ {
		event := raw[index]
		if asOf > 0 && event.StreamOrdering > asOf {
			continue
		}
		if visible[event.EventID] {
			further = true
			break
		}
	}
	if lastSeen >= 0 && len(raw) > lastSeen+1 && !reachedStop && further {
		page.End = stream.Position(raw[lastSeen].StreamOrdering).String()
	}

	if err := s.historyState(ctx, roomID, requester, page.Chunk, req.Filter.LazyLoadMembers, &page); err != nil {
		return HistoryPage{}, err
	}
	return page, nil
}

// historyStart resolves the position a page begins at.
func (s *Service) historyStart(ctx context.Context, roomID, from string, backwards bool) (stream.Position, error) {
	if from != "" {
		return stream.Parse(from)
	}
	// With no token the client is asking for the last visible event in the direction it is
	// walking. Backwards that is the room's newest event; forwards it is the room's
	// oldest, because there is nothing earlier to walk to.
	if backwards {
		room, err := s.rooms.ByRoomID(ctx, roomID)
		if err != nil {
			return 0, fmt.Errorf("rooms: cannot load the room: %w", err)
		}
		return stream.Position(room.LatestStream + 1), nil
	}
	entries, err := s.state.All(ctx, roomID)
	if err != nil {
		return 0, err
	}
	var earliest int64
	for _, entry := range entries {
		event, err := s.events.ByID(ctx, entry.EventID)
		if err != nil {
			return 0, err
		}
		if earliest == 0 || event.StreamOrdering < earliest {
			earliest = event.StreamOrdering
		}
	}
	// The start position is exclusive, so the earliest event itself has to sit just
	// behind it to be included in the page.
	if earliest > 0 {
		earliest--
	}
	return stream.Position(earliest), nil
}

// historyState fills the `state` field.
//
// With lazy loading the answer is precise: the member events for the senders appearing in
// this page, which is exactly what a client needs to render them. Without it the
// specification asks for the room's state at the start of the chunk, and this server
// keeps only the room's current state, so it answers with that — bounded by what the
// requester may read. docs/requirements.md records the difference; reconstructing the
// state as of a past position needs per-event state snapshots (T-3004).
func (s *Service) historyState(ctx context.Context, roomID, requester string, chunk []map[string]any, lazy bool, page *HistoryPage) error {
	if !lazy {
		state, err := s.CurrentState(ctx, roomID, requester)
		if err != nil {
			return err
		}
		page.State = state
		return nil
	}
	senders := map[string]bool{}
	for _, event := range chunk {
		sender, _ := event["sender"].(string)
		if sender != "" {
			senders[sender] = true
		}
	}
	userIDs := make([]string, 0, len(senders))
	for userID := range senders {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)
	asOf, err := s.readHorizon(ctx, roomID, requester)
	if err != nil {
		return err
	}
	for _, userID := range userIDs {
		entry, err := s.state.Get(ctx, roomID, eventauth.TypeMember, userID)
		if err != nil {
			if notFound(err) {
				continue
			}
			return err
		}
		stored, err := s.events.ByID(ctx, entry.EventID)
		if err != nil {
			if notFound(err) {
				continue
			}
			return err
		}
		if asOf > 0 && stored.StreamOrdering > asOf {
			continue
		}
		body, err := s.eventJSONFor(ctx, entry.EventID, requester)
		if err != nil {
			return err
		}
		page.State = append(page.State, body)
	}
	return nil
}

// requireHistoryAccess gates a timeline read. A room this server has never heard of
// answers 403 rather than 404: the identifier itself is not public information, and a
// client cannot tell an unjoined room from a nonexistent one either.
func (s *Service) requireHistoryAccess(ctx context.Context, roomID, requester string) error {
	if _, err := s.rooms.ByRoomID(ctx, roomID); err != nil {
		if notFound(err) {
			// A room this server has never heard of answers 403 rather than 404. The
			// identifier itself is not public information: 404 would tell a non-member
			// that the room identifier is not in use, which is a fact about this server's
			// rooms. The reference server behaves the same way, and a client cannot tell an
			// unjoined room from a nonexistent one over this endpoint either.
			return matrixerr.New(matrixerr.CodeForbidden,
				"This server has no room with that identifier.")
		}
		return fmt.Errorf("rooms: cannot load the room: %w", err)
	}
	return s.requireMembership(ctx, roomID, requester, membershipAny)
}

// ParseHistoryFilter reads the `filter` parameter of a `/messages` request, which the
// specification defines as a room event filter written inline: here it is the whole parameter,
// where in a sync it is the `room.timeline` part of a larger document.
func ParseHistoryFilter(raw string) (eventfilter.RoomEventFilter, error) {
	return eventfilter.ParseRoomEventFilter(raw)
}

// historyBackfillMaxAsks bounds how many times one read will ask another server for history. Each ask
// is a round trip taken while a client waits, and a peer with nothing to give answers with an empty
// list, which ends the loop by itself. The bound is for the peer that answers just enough each time to
// keep the loop going.
const historyBackfillMaxAsks = 3

// backfillToFill asks a server that holds this room for history this server was never told about, so
// that a client paging past the end of this server's knowledge gets the history rather than the end.
//
// A short page is the trouble. It is the answer a room gives when it has run out, and the protocol
// gives a client no other word for "the room began here" — so a server that has simply never been told
// any more answers in exactly the language of a room that has no more, and no client can tell the two
// apart afterwards. Asking costs one round trip to a peer and is bounded to a few goes per read,
// because a page is served on somebody's patience and a peer that is having trouble is allowed to be
// having trouble.
//
// The asking stops when the page is full, when the peer hands over nothing further, or when this server
// already holds the room's create event — that last being the honest end of the question, and the
// reason a room this server holds in full does not go and ask its peers for the week before it began.
//
// A room with no peer to ask, or a server not wired to ask, is left as it was read. This is a read that
// tried to be more useful, not one that can now fail for a reason the client could have avoided.
func (s *Service) backfillToFill(ctx context.Context, roomID string, start stream.Position,
	backwards bool, limit int, raw []*persist.Event,
) ([]*persist.Event, error) {
	if s.backfill == nil || !backwards || limit <= 0 {
		return raw, nil
	}
	for asks := 0; asks < historyBackfillMaxAsks; asks++ {
		if len(raw) > limit {
			break
		}
		// Which event to ask about, and whether to ask at all. A room whose history is complete down
		// to its create event has no event that cites something this server lacks, so the answer is
		// nothing to ask and the room really is out of past. A room joined from a peer holds a run of
		// events whose oldest end points at an event nobody sent, which is both the question and the
		// reason to believe the page came short for a good one.
		//
		// The room's earliest stored event is the wrong thing to ask about, and holding the create
		// event is the wrong reason to stop: a join hands over the room's state, so this server holds
		// the beginning of a room it has never been told the middle of, and "we have the create event"
		// would end the search at exactly the moment it should begin.
		seed, err := s.historyBackfillSeed(ctx, roomID, raw)
		if err != nil || seed == "" {
			break
		}
		fetched, err := s.backfill.FetchHistoryBefore(ctx, roomID, seed)
		if err != nil || fetched <= 0 {
			break
		}
		// Read the page again from where the client asked, which may now reach further into the past
		// than the first read could. The start does not move: the client asked about one place in the
		// history, and filling in behind it must not move the ground under that question.
		if raw, err = s.events.EventsWindow(ctx, roomID, int64(start), backwards, limit+1); err != nil {
			return raw, err
		}
	}
	return raw, nil
}

// historyBackfillSeed chooses the event to ask a peer about: one whose predecessors this server does
// not hold, and which this server nevertheless holds itself.
//
// Both halves matter and they pull against each other. The peer is asked for the events between what
// this server holds and what it lacks, so an event this server does not hold cannot be asked about —
// it has to be named as *missing* by something the peer can see this server has. And an event whose
// predecessors are all present cannot be the mouth of a hole at all: walking back from it arrives at
// the same place walking back from its predecessors does, so asking about it asks for nothing.
//
// The events this server holds but could not place — outliers, kept out of client timelines precisely
// because their place in the history is unknown — are the clean case: each one is a held event with a
// named gap under it. Failing those, the oldest event on the page the client could not fill is the
// best guess available, and the honest one: it is the point where the reader stopped, and the reason
// they stopped there.
func (s *Service) historyBackfillSeed(ctx context.Context, roomID string, raw []*persist.Event) (string, error) {
	if outliers, err := s.events.Outliers(ctx, roomID); err == nil && len(outliers) > 0 {
		return outliers[0], nil
	}
	if len(raw) > 0 {
		return raw[len(raw)-1].EventID, nil
	}
	return "", nil
}
