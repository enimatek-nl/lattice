package rooms

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
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
	// LazyMembers asks for the member events of the senders in this page instead of the
	// room's state, which is how a client fills in display names without downloading the
	// whole state at every page.
	LazyMembers bool
}

// HistoryPage is the response.
type HistoryPage struct {
	Chunk []map[string]any `json:"chunk"`
	State []map[string]any `json:"state"`
	// Start echoes the position the page began at. The specification says it is the
	// value given in `from`; when the client sent no `from`, it is the position this
	// server started from, which the client can hand straight back.
	Start string `json:"start"`
	// End is the token to continue from. It is omitted when there is nothing further
	// this requester may see, which is how a client knows to stop paginating.
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
	asOf, err := s.readHorizon(ctx, roomID, requester)
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
	hiddenByHorizon := false
	for _, event := range raw {
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
			hiddenByHorizon = true
			continue
		}
		body, err := s.eventJSON(ctx, event.EventID)
		if err != nil {
			return HistoryPage{}, err
		}
		page.Chunk = append(page.Chunk, body)
	}
	// `end` is only worth handing back while there is something further this requester
	// may read: a short page means the timeline ended in that direction, reaching `to`
	// means the client asked for this page and no more, and a page whose remaining
	// events were withheld means the requester has reached their limit.
	if len(raw) > len(page.Chunk) && !reachedStop && !hiddenByHorizon {
		page.End = stream.Position(raw[len(page.Chunk)-1].StreamOrdering).String()
	}

	if err := s.historyState(ctx, roomID, requester, page.Chunk, req.LazyMembers, &page); err != nil {
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
		body, err := s.eventJSON(ctx, entry.EventID)
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

// ParseHistoryFilter reads the `filter` parameter of a `/messages` request. It accepts an
// inline JSON object, which is how clients send it here, and ignores everything except
// `lazy_load_members`, which is the part of a room event filter this endpoint acts on.
func ParseHistoryFilter(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	var filter struct {
		LazyLoadMembers *bool `json:"lazy_load_members"`
	}
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return false, matrixerr.New(matrixerr.CodeInvalidParam,
			"The filter parameter is not a JSON object.")
	}
	return filter.LazyLoadMembers != nil && *filter.LazyLoadMembers, nil
}
