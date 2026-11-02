package service

import (
	"context"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// SyncParams captures the query parameters of GET /sync.
type SyncParams struct {
	UserID    string
	DeviceID  string
	Since     uint64
	Timeout   time.Duration
	Limit     int
	FilterID  string
	FilterRaw string
}

// Sync performs a (long polling) sync for a device.
//
// The call returns as soon as new data exists for the user, or after the
// requested timeout. Waiting happens on the realtime hub rather than by
// polling the database: the hub wakes every waiting sync when an event is
// appended, and each waiter unsubscribes before returning so no goroutine
// outlives the request.
func (s *Service) Sync(ctx context.Context, params SyncParams) (*matrix.SyncResponse, error) {
	filter, err := s.syncFilter(ctx, params)
	if err != nil {
		return nil, err
	}
	if params.Limit <= 0 || params.Limit > 1000 {
		params.Limit = 10
	}

	notify, cancel := s.hub.Subscribe()
	defer cancel()

	deadline := time.NewTimer(params.Timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		response, changed, err := s.buildSync(ctx, params, filter)
		if err != nil {
			return nil, err
		}
		// An initial sync always returns, and an incremental sync returns as
		// soon as it carries something new.
		if params.Since == 0 || changed || params.Timeout == 0 {
			return response, nil
		}
		select {
		case <-ctx.Done():
			return response, ctx.Err()
		case <-deadline.C:
			return response, nil
		case <-ticker.C:
			// Fallback wake-up in case a notification was dropped.
		case <-notify:
		}
	}
}

// buildSync assembles a sync response and reports whether it contains new data
// compared with the "since" position.
func (s *Service) buildSync(ctx context.Context, params SyncParams, filter *SyncFilter) (*matrix.SyncResponse, bool, error) {
	userID := params.UserID
	since := params.Since
	pos := s.StreamPos()

	joined, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipJoin)
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	invited, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipInvite)
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	left, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipLeave)
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	// A banned user is told about the room as well. The leave bucket is where "you are not
	// in this room any more" is reported, and a ban is exactly that; a sync that says
	// nothing leaves a client showing a room its user was thrown out of, and the ban event
	// - the only explanation of the change - never reaches them.
	banned, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipBan)
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	left = append(left, banned...)

	response := &matrix.SyncResponse{
		NextBatch: s.Token(pos),
		Rooms: matrix.Rooms{
			Join:   map[string]*matrix.JoinedRoom{},
			Invite: map[string]*matrix.InvitedRoom{},
			Leave:  map[string]*matrix.LeftRoom{},
		},
	}
	changed := false

	for _, roomID := range joined {
		room, roomChanged, err := s.joinedRoomSync(ctx, userID, roomID, since, params.Limit, filter)
		if err != nil {
			return nil, false, err
		}
		if room == nil {
			continue
		}
		response.Rooms.Join[roomID] = room
		changed = changed || roomChanged
	}
	for _, roomID := range invited {
		room, inviteChanged, err := s.invitedRoomSync(ctx, userID, roomID, since)
		if err != nil {
			return nil, false, err
		}
		response.Rooms.Invite[roomID] = room
		changed = changed || inviteChanged
	}
	for _, roomID := range left {
		room, roomChanged, err := s.leftRoomSync(ctx, userID, roomID, since, params.Limit)
		if err != nil {
			return nil, false, err
		}
		if room == nil {
			continue
		}
		response.Rooms.Leave[roomID] = room
		changed = changed || roomChanged
	}

	accountData, err := s.accountDataEvents(userID, since)
	if err != nil {
		return nil, false, err
	}
	if len(accountData) > 0 {
		response.AccountData = &matrix.Ephemeral{Events: accountData}
		changed = true
	}

	toDevice, err := s.toDeviceEvents(userID, params.DeviceID, since)
	if err != nil {
		return nil, false, err
	}
	if len(toDevice) > 0 {
		response.ToDevice = &matrix.Ephemeral{Events: toDevice}
		changed = true
	}

	// The key counts belong to a device rather than to a user, so they are only
	// reported when the request is authenticated with one. They are part of every
	// response: a client decides whether to upload more keys from these numbers.
	if params.DeviceID != "" {
		counts, fallback, err := s.OneTimeKeyCounts(ctx, userID, params.DeviceID)
		if err != nil {
			return nil, false, err
		}
		response.DeviceOneTimeCount = &counts
		response.UnusedFallbackKeys = fallback
	}

	// Device list changes only make sense relative to an earlier position; a
	// first sync gets the whole key set from POST /keys/query instead.
	if since > 0 {
		lists, err := s.DeviceListChanges(ctx, userID, since, pos, params.DeviceID)
		if err != nil {
			return nil, false, err
		}
		if lists != nil {
			response.DeviceLists = lists
			changed = true
		}
	} else {
		// A client that has never synced gets its push rules with the first
		// response: they decide whether the events arriving in this response are
		// notifications, so a client that has to ask /pushrules first has already
		// missed the point of them.
		rules, err := s.PushRules(ctx, userID)
		if err != nil {
			return nil, false, err
		}
		response.PushRules = &rules
	}

	receipts, err := s.receiptEventsForRooms(joined, since)
	if err != nil {
		return nil, false, err
	}
	for roomID, events := range receipts {
		room := response.Rooms.Join[roomID]
		if room == nil {
			room = &matrix.JoinedRoom{
				Timeline: matrix.Timeline{Events: []matrix.Event{}, PrevBatch: s.Token(since)},
				State:    matrix.State{Events: []matrix.Event{}},
			}
			response.Rooms.Join[roomID] = room
		}
		// The receipts are added to whatever the room already carries rather than
		// replacing it: a room's ephemeral events also include the typing indicator, and
		// a sync that reports one of them by dropping the other is a client with a
		// conversation that appears to have stopped.
		if room.Ephemeral == nil {
			room.Ephemeral = &matrix.Ephemeral{Events: events}
		} else {
			room.Ephemeral.Events = append(room.Ephemeral.Events, events...)
		}
		changed = true
	}

	if len(response.Rooms.Join) == 0 {
		response.Rooms.Join = nil
	}
	if len(response.Rooms.Invite) == 0 {
		response.Rooms.Invite = nil
	}
	if len(response.Rooms.Leave) == 0 {
		response.Rooms.Leave = nil
	}
	return response, changed, nil
}

// joinedRoomSync renders the join section of one room.
func (s *Service) joinedRoomSync(ctx context.Context, userID, roomID string, since uint64, limit int, filter *SyncFilter) (*matrix.JoinedRoom, bool, error) {
	events, err := s.db.EventRepo.Timeline(roomID, since, filter.timelineLimit(limit))
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	room := &matrix.JoinedRoom{
		Timeline: matrix.Timeline{
			Events:    toWire(events),
			PrevBatch: s.Token(since),
		},
		State: matrix.State{Events: []matrix.Event{}},
	}
	if room.Timeline.Events == nil {
		room.Timeline.Events = []matrix.Event{}
	}
	if err := s.annotateThreads(roomID, userID, room.Timeline.Events); err != nil {
		return nil, false, err
	}
	heroes, joinedCount, invitedCount, err := s.syncRoomSummary(roomID, userID)
	if err != nil {
		return nil, false, err
	}
	room.Heroes = heroes
	room.JoinedMemberCount = &joinedCount
	room.InvitedMemberCount = &invitedCount

	// Clients need the room state when they first see a room, and when they
	// joined after the sync token; later syncs only carry state deltas.
	if since == 0 || s.membershipAfter(roomID, userID, since) {
		state, err := s.stateEvents(ctx, roomID, since, filter)
		if err != nil {
			return nil, false, err
		}
		room.State = matrix.State{Events: state, PrevBatch: s.Token(since)}
	} else {
		state, err := s.stateDelta(roomID, since)
		if err != nil {
			return nil, false, err
		}
		room.State = matrix.State{Events: state, PrevBatch: s.Token(since)}
	}

	typing, err := s.typingEvents(roomID, userID, since)
	if err != nil {
		return nil, false, err
	}
	accountData, err := s.roomAccountDataEvents(userID, roomID, since)
	if err != nil {
		return nil, false, err
	}
	if len(typing) > 0 || len(accountData) > 0 {
		if len(typing) > 0 {
			room.Ephemeral = &matrix.Ephemeral{Events: typing}
		}
		if len(accountData) > 0 {
			room.AccountData = &matrix.Ephemeral{Events: accountData}
		}
	}

	count, highlights, err := s.unreadCount(ctx, userID, roomID, since)
	if err != nil {
		return nil, false, err
	}
	room.UnreadNotifications.NotificationCount = count
	room.UnreadNotifications.HighlightCount = highlights
	invites, err := s.db.MembershipRepo.Count(roomID, matrix.MembershipInvite)
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	room.InviteCount = invites

	limited, err := s.timelineLimited(roomID, since, len(events), filter.timelineLimit(limit))
	if err != nil {
		return nil, false, err
	}
	room.Timeline.Limited = limited
	if len(events) > 0 {
		room.Timeline.PrevBatch = s.Token(events[0].StreamPos)
	} else {
		pos, err := s.db.EventRepo.LatestRoomStreamPos(roomID)
		if err != nil {
			return nil, false, matrix.ErrInternal(err)
		}
		room.Timeline.PrevBatch = s.Token(pos)
	}
	return room, len(events) > 0 || len(typing) > 0 || len(accountData) > 0, nil
}

// membershipAfter reports whether the stored membership event of a user in a
// room was written after a position.
func (s *Service) membershipAfter(roomID, userID string, since uint64) bool {
	row, err := s.db.MembershipRepo.Get(roomID, userID)
	if err != nil {
		return true
	}
	event, err := s.db.EventRepo.Get(row.EventID)
	if err != nil {
		return true
	}
	return event.StreamPos > since
}

// timelineLimited reports whether more events exist than were returned.
func (s *Service) timelineLimited(roomID string, since uint64, returned, limit int) (bool, error) {
	if limit == 0 {
		return false, nil
	}
	rest, err := s.db.EventRepo.Timeline(roomID, since, limit+1)
	if err != nil {
		return false, matrix.ErrInternal(err)
	}
	return len(rest) > returned, nil
}

// annotateThreads attaches the m.thread summary of a thread root to the
// timeline events that carry one, which is how clients render threads without
// extra requests.
func (s *Service) annotateThreads(roomID, userID string, events []matrix.Event) error {
	if len(events) == 0 {
		return nil
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	replies, err := s.db.EventRepo.ThreadsFor(roomID, ids)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	if len(replies) == 0 {
		return nil
	}
	type digest struct {
		count        int64
		latest       *persist.Event
		participated bool
	}
	byRoot := map[string]*digest{}
	for _, reply := range replies {
		entry, ok := byRoot[reply.RelatesTo]
		if !ok {
			entry = &digest{}
			byRoot[reply.RelatesTo] = entry
		}
		entry.count++
		if reply.Sender == userID {
			entry.participated = true
		}
		if entry.latest == nil || reply.StreamPos > entry.latest.StreamPos {
			entry.latest = reply
		}
	}
	for i := range events {
		entry, ok := byRoot[events[i].ID]
		if !ok || entry.latest == nil {
			continue
		}
		if events[i].Unsigned == nil {
			events[i].Unsigned = matrix.Object{}
		}
		events[i].Unsigned["m.thread"] = matrix.Object{
			"num_events":                entry.count + 1,
			"latest_event":              entry.latest.Matrix(),
			"current_user_participated": entry.participated || events[i].Sender == userID,
		}
	}
	return nil
}

// toWire converts stored events into their wire representation.
func toWire(events []*persist.Event) []matrix.Event {
	out := make([]matrix.Event, 0, len(events))
	for _, event := range events {
		out = append(out, event.Matrix())
	}
	return out
}
