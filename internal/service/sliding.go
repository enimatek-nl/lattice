package service

import (
	"context"
	"time"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// SlidingSync implements POST /_matrix/client/v4/sync (MSC4186).
//
// A sliding sync is a conversation about a connection rather than about a moment in the
// stream: the client says which rooms it cares about and how much of each it wants, and the
// server answers with only what that connection has not been told. That makes the position
// token a claim about a relationship this server holds, not a number anybody can use, so the
// first job of every request is to find the connection the token was issued to and refuse any
// request whose token does not belong to the user and device making it.
//
// What a connection has been shown is never a permission. Whether the user may see a room is
// asked of the room, on every request, because a stolen or replayed position token must not be
// able to open a door.
func (s *Service) SlidingSync(ctx context.Context, userID, deviceID string,
	req *matrix.SlidingSyncRequest) (*matrix.SlidingSyncResponse, error) {
	if req == nil {
		req = &matrix.SlidingSyncRequest{}
	}
	if err := checkSlidingRequest(req); err != nil {
		return nil, err
	}
	conn, initial, err := s.openSlidingConnection(ctx, userID, deviceID, req.ConnID, req.Pos)
	if err != nil {
		return nil, err
	}

	// A timeout only makes sense once the connection exists: an initial request has
	// everything to say and answers at once, whatever the client asked for.
	var timeout time.Duration
	if req.Timeout != nil && !initial {
		timeout = time.Duration(*req.Timeout) * time.Millisecond
		if timeout < 0 {
			timeout = 0
		}
	}

	notify, cancel := s.hub.Subscribe()
	defer cancel()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		response, changed, written, err := s.buildSlidingSync(ctx, userID, deviceID, conn, req, initial)
		if err != nil {
			return nil, err
		}
		if initial || changed || timeout == 0 {
			if err := s.commitSliding(conn, written); err != nil {
				return nil, err
			}
			return response, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			if err := s.commitSliding(conn, written); err != nil {
				return nil, err
			}
			return response, nil
		case <-ticker.C:
			// A fallback wake-up, in case a notification was dropped.
		case <-notify:
		}
	}
}

// checkSlidingRequest rejects the shapes the endpoint will not act on. The client is told with
// M_INVALID_PARAM, which is the code MSC4186 names for a list or subscription count over the
// limit and for an unknown presence value.
func checkSlidingRequest(req *matrix.SlidingSyncRequest) error {
	if len(req.ConnID) > slidingMaxConnIDLen {
		return matrix.ErrInvalidParam("conn_id is longer than %d characters", slidingMaxConnIDLen)
	}
	if len(req.Lists) > slidingMaxLists {
		return matrix.ErrInvalidParam("a request names %d lists; %d is the most",
			len(req.Lists), slidingMaxLists)
	}
	if len(req.RoomSubscriptions) > slidingMaxSubscriptions {
		return matrix.ErrInvalidParam("a request names %d room subscriptions; %d is the most",
			len(req.RoomSubscriptions), slidingMaxSubscriptions)
	}
	switch req.SetPresence {
	case "", "online", "offline", "unavailable":
	default:
		return matrix.ErrInvalidParam("%q is not a presence this endpoint knows", req.SetPresence)
	}
	for name, list := range req.Lists {
		if len(name) > slidingMaxNameLen {
			return matrix.ErrInvalidParam("the list name %q is too long", name)
		}
		if list.TimelineLimit < 0 {
			return matrix.ErrInvalidParam("the list %q asks for a negative timeline limit", name)
		}
		if err := checkSlidingRange(list.Range, name); err != nil {
			return err
		}
	}
	for roomID, sub := range req.RoomSubscriptions {
		if sub.TimelineLimit < 0 {
			return matrix.ErrInvalidParam("the subscription to %s asks for a negative timeline limit", roomID)
		}
	}
	return nil
}

// checkSlidingRange checks one window. A range is inclusive at both ends and counted from 0;
// its absence means the whole list, which is a different thing from an empty one.
func checkSlidingRange(range_ []int, name string) error {
	if len(range_) == 0 {
		return nil
	}
	if len(range_) != 2 {
		return matrix.ErrInvalidParam("the range of the list %q is not a pair of numbers", name)
	}
	if range_[0] < 0 || range_[1] < range_[0] {
		return matrix.ErrInvalidParam("the range %v of the list %q names no rooms", range_, name)
	}
	return nil
}

// slidingConn is one connection of one device: its row, and what it has been told about rooms.
type slidingConn struct {
	row   *persist.SlidingSyncConnection
	known map[string]*persist.SlidingSyncRoom
	// initial marks a request that starts the connection over, which is what makes an
	// omitted room field mean "not set" rather than "unchanged".
	initial bool
	// pendingPos is the position the response last built answered with. The connection only
	// moves there when that response is the one the client is given: answering from a later
	// point than the token handed out would make the client's next request look like a
	// replay of an old one.
	pendingPos uint64
}

// openSlidingConnection finds the connection a request speaks for, or begins a new one.
//
// A request that carries no position starts a connection: whatever was recorded under that
// connection id before is dropped, because the client has said it is starting over. A request
// that carries one must carry one this server gave to this user on this device, and it must be
// the position the connection is at now - the MSC allows a server to answer anything else with
// M_UNKNOWN_POS, and a connection whose client has lost the thread is better restarted than
// guessed at.
func (s *Service) openSlidingConnection(ctx context.Context, userID, deviceID, connID, pos string) (*slidingConn, bool, error) {
	if pos == "" {
		row, err := s.db.SlidingSyncRepo.Connection(userID, deviceID, connID)
		if err != nil {
			if !persist.IsNotFound(err) {
				return nil, false, matrix.ErrInternal(err)
			}
			row = &persist.SlidingSyncConnection{UserID: userID, DeviceID: deviceID, ConnID: connID}
		} else if err := s.db.SlidingSyncRepo.ResetRooms(row.ID); err != nil {
			return nil, false, matrix.ErrInternal(err)
		}
		row.LastPos = s.StreamPos()
		if err := s.db.SlidingSyncRepo.SaveConnection(row); err != nil {
			return nil, false, matrix.ErrInternal(err)
		}
		if err := s.pruneSlidingConnections(userID, deviceID, row.ID); err != nil {
			return nil, false, err
		}
		return &slidingConn{row: row, known: map[string]*persist.SlidingSyncRoom{}, initial: true}, true, nil
	}

	tokenConn, tokenPos, err := auth.ParseSlidingSyncPos(pos)
	if err != nil {
		return nil, false, matrix.ErrUnknownPos("the position %q was not issued to this connection", pos)
	}
	if connID != "" && connID != tokenConn {
		return nil, false, matrix.ErrUnknownPos(
			"the position %q belongs to the connection %q, not to %q", pos, tokenConn, connID)
	}
	row, err := s.db.SlidingSyncRepo.Connection(userID, deviceID, tokenConn)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, false, matrix.ErrUnknownPos(
				"this user and device have no connection %q", tokenConn)
		}
		return nil, false, matrix.ErrInternal(err)
	}
	if tokenPos != row.LastPos {
		return nil, false, matrix.ErrUnknownPos(
			"the connection %q is at position %d, not %d; start it again", tokenConn, row.LastPos, tokenPos)
	}
	rows, err := s.db.SlidingSyncRepo.Rooms(row.ID)
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	known := make(map[string]*persist.SlidingSyncRoom, len(rows))
	for _, entry := range rows {
		known[entry.RoomID] = entry
	}
	return &slidingConn{row: row, known: known}, false, nil
}

// pruneSlidingConnections bounds how many connections one device may leave standing, which
// protects the server from a client that opens a connection per retry and never closes one.
func (s *Service) pruneSlidingConnections(userID, deviceID string, keep uint) error {
	rows, err := s.db.SlidingSyncRepo.ConnectionsForDevice(userID, deviceID, keep)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	for len(rows) >= slidingMaxConnections {
		oldest := rows[0]
		rows = rows[1:]
		if err := s.db.SlidingSyncRepo.Forget(oldest.ID); err != nil {
			return matrix.ErrInternal(err)
		}
	}
	return nil
}

// slidingRoomPlan is one room a rule matched, with the config it is reported under and what
// the connection already knows of it.
type slidingRoomPlan struct {
	entry  slidingRoomEntry
	lists  []string
	config slidingConfig
	known  *persist.SlidingSyncRoom
}

// buildSlidingSync assembles one response. It writes nothing: a request that is going to be
// asked again - because the client cancelled it, or because the long poll has another round -
// must not have already spent the state it is about to report.
func (s *Service) buildSlidingSync(ctx context.Context, userID, deviceID string,
	conn *slidingConn, req *matrix.SlidingSyncRequest, initial bool) (*matrix.SlidingSyncResponse, bool, []*persist.SlidingSyncRoom, error) {
	toPos := s.StreamPos()
	fromPos := conn.row.LastPos
	conn.pendingPos = toPos

	entries, err := s.slidingRooms(ctx, userID, slidingKnownRooms(conn.knownToRows()))
	if err != nil {
		return nil, false, nil, err
	}
	facts, err := s.slidingGatherFacts(ctx, userID, entries, req.Lists)
	if err != nil {
		return nil, false, nil, err
	}
	direct := s.directRoomSet(ctx, userID)

	plans := map[string]*slidingRoomPlan{}
	// Each list is filtered once. The count a list reports is the size of that filtered
	// sequence whatever the range asked for, so the same answer serves both halves.
	matched := make(map[string][]slidingRoomEntry, len(req.Lists))
	for name, list := range req.Lists {
		var rooms []slidingRoomEntry
		for _, entry := range entries {
			if slidingRoomMatches(entry, list.Filters, facts) {
				rooms = append(rooms, entry)
			}
		}
		matched[name] = rooms
		config := configOf(list.TimelineLimit, list.RequiredState, userID)
		from, to := slidingRangeBounds(list.Range, len(rooms))
		for i := from; i <= to; i++ {
			addSlidingPlan(plans, rooms[i], name, config, conn.known[rooms[i].roomID])
		}
	}
	for roomID, sub := range req.RoomSubscriptions {
		for _, entry := range entries {
			if entry.roomID != roomID {
				continue
			}
			addSlidingPlan(plans, entry, "",
				configOf(sub.TimelineLimit, sub.RequiredState, userID), conn.known[roomID])
			break
		}
		// A subscription to a room the user may not see, or that does not exist, is
		// ignored rather than answered: the MSC is explicit that no information about such
		// a room is returned, and an error would say more than a silence.
	}

	if len(plans) > slidingExpireRooms {
		// The delta would be larger than starting over. The MSC lets a server say so, and
		// saying it means forgetting the connection so that the client's next request,
		// which it must make without a position, begins cleanly.
		if err := s.db.SlidingSyncRepo.Forget(conn.row.ID); err != nil {
			return nil, false, nil, matrix.ErrInternal(err)
		}
		return nil, false, nil, matrix.ErrUnknownPos(
			"this connection would be sent %d rooms at once; start it again", len(plans))
	}

	listResults := make(map[string]matrix.SlidingListResult, len(req.Lists))
	for name := range req.Lists {
		listResults[name] = matrix.SlidingListResult{Count: len(matched[name])}
	}

	response := &matrix.SlidingSyncResponse{
		Pos:   auth.SlidingSyncPos(conn.row.ConnID, toPos),
		Lists: listResults,
		Rooms: map[string]matrix.SlidingRoom{},
	}
	written := make([]*persist.SlidingSyncRoom, 0, len(plans))
	for roomID, plan := range plans {
		if !slidingShouldReport(plan.entry, plan.known, plan.config) {
			continue
		}
		room, row, err := s.slidingRoomResult(ctx, userID, fromPos, toPos, *plan, direct[roomID])
		if err != nil {
			return nil, false, nil, err
		}
		row.ConnKey = conn.row.ID
		response.Rooms[roomID] = room
		written = append(written, row)
	}
	if len(response.Rooms) == 0 {
		response.Rooms = nil
	}

	extensions, moved, err := s.slidingExtensions(ctx, userID, deviceID, conn, req, response, initial)
	if err != nil {
		return nil, false, nil, err
	}
	if len(extensions) > 0 {
		response.Extensions = extensions
	}

	if err := s.trimSlidingRooms(conn, plans, entries); err != nil {
		return nil, false, nil, err
	}
	return response, len(response.Rooms) > 0 || moved, written, nil
}

// knownToRows returns the connection's stored room rows, which the list rules read to know
// which rooms this client has been shown.
func (c *slidingConn) knownToRows() []*persist.SlidingSyncRoom {
	out := make([]*persist.SlidingSyncRoom, 0, len(c.known))
	for _, row := range c.known {
		out = append(out, row)
	}
	return out
}

// addSlidingPlan records that a room matched a rule, widening its config if it matched
// another one too.
func addSlidingPlan(plans map[string]*slidingRoomPlan, entry slidingRoomEntry, list string,
	config slidingConfig, known *persist.SlidingSyncRoom) {
	plan, ok := plans[entry.roomID]
	if !ok {
		belong := []string{}
		if list != "" {
			belong = append(belong, list)
		}
		plans[entry.roomID] = &slidingRoomPlan{entry: entry, lists: belong, config: config, known: known}
		return
	}
	if list != "" {
		plan.lists = append(plan.lists, list)
	}
	plan.config = plan.config.merge(config)
}

// slidingRangeBounds turns a list's range into indexes that cannot run off the end of it.
func slidingRangeBounds(range_ []int, total int) (int, int) {
	if total == 0 {
		return 0, -1
	}
	if len(range_) == 0 {
		return 0, total - 1
	}
	return min(range_[0], total-1), min(range_[1], total-1)
}

// trimSlidingRooms forgets what the connection was told about rooms it can no longer be shown,
// which is otherwise a slow accumulation of rows for rooms the user has forgotten.
func (s *Service) trimSlidingRooms(conn *slidingConn, plans map[string]*slidingRoomPlan,
	entries []slidingRoomEntry) error {
	if len(conn.known) == 0 {
		return nil
	}
	keep := make([]string, 0, len(entries)+len(plans))
	for _, entry := range entries {
		keep = append(keep, entry.roomID)
	}
	for roomID := range plans {
		keep = append(keep, roomID)
	}
	if err := s.db.SlidingSyncRepo.TrimRooms(conn.row.ID, keep); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// commitSliding writes what the response told the client, and moves the connection to the
// position the response answered with.
func (s *Service) commitSliding(conn *slidingConn, written []*persist.SlidingSyncRoom) error {
	conn.row.LastPos = conn.pendingPos
	if err := s.db.SlidingSyncRepo.SaveConnection(conn.row); err != nil {
		return matrix.ErrInternal(err)
	}
	for _, row := range written {
		if existing, ok := conn.known[row.RoomID]; ok {
			*existing = *row
			continue
		}
		conn.known[row.RoomID] = row
	}
	if err := s.db.SlidingSyncRepo.SaveRooms(written); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}
