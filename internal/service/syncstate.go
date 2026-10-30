package service

import (
	"context"
	"encoding/json"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// SyncFilter is the subset of a sync filter that Lattice honours.
type SyncFilter struct {
	// TimelineLimit is room.timeline.limit.
	TimelineLimit int
	// NotTypes are room.state.not_types exclusions.
	NotTypes []string
	// NotSenders are room.state.not_senders exclusions.
	NotSenders []string
	// LazyLoadState requests lazy loaded member state (served in full; the
	// flag only affects payload size expectations).
	LazyLoadState bool
}

// timelineLimit applies the filter limit when present.
func (f *SyncFilter) timelineLimit(fallback int) int {
	if f != nil && f.TimelineLimit > 0 {
		return f.TimelineLimit
	}
	if fallback <= 0 {
		return 10
	}
	return fallback
}

// syncFilter resolves the filter for a sync call, from the filter id or the
// inline filter definition.
func (s *Service) syncFilter(ctx context.Context, params SyncParams) (*SyncFilter, error) {
	filter := &SyncFilter{}
	raw := params.FilterRaw
	if params.FilterID != "" {
		stored, err := s.db.FilterRepo.Get(params.UserID, params.FilterID)
		if err != nil {
			if persist.IsNotFound(err) {
				return nil, matrix.ErrNotFound("no such filter %s", params.FilterID)
			}
			return nil, matrix.ErrInternal(err)
		}
		raw = stored.Body
	}
	if raw == "" {
		return filter, nil
	}
	var body struct {
		Room struct {
			State struct {
				NotTypes        []string `json:"not_types"`
				NotSenders      []string `json:"not_senders"`
				LazyLoadOptions struct {
					LazyLoad bool `json:"lazy_load_options"`
				} `json:"lazy_load_options"`
			} `json:"state"`
			Timeline struct {
				Limit int `json:"limit"`
			} `json:"timeline"`
		} `json:"room"`
	}
	// Filters are supplied as JSON; a malformed filter is a client error.
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil, matrix.ErrInvalidRequest("invalid filter: %v", err)
	}
	filter.NotTypes = body.Room.State.NotTypes
	filter.NotSenders = body.Room.State.NotSenders
	filter.TimelineLimit = body.Room.Timeline.Limit
	return filter, nil
}

// allowedByFilter reports whether a state event passes the filter exclusions.
func (f *SyncFilter) allowedByFilter(typ, sender string) bool {
	if f == nil {
		return true
	}
	for _, notType := range f.NotTypes {
		if notType == typ {
			return false
		}
	}
	for _, notSender := range f.NotSenders {
		if notSender == sender {
			return false
		}
	}
	return true
}

// stateEvents returns the full current state of a room.
func (s *Service) stateEvents(ctx context.Context, roomID string, since uint64, filter *SyncFilter) ([]matrix.Event, error) {
	rows, err := s.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	events := make([]matrix.Event, 0, len(rows))
	for _, row := range rows {
		if !filter.allowedByFilter(row.Type, row.Sender) {
			continue
		}
		event, err := s.db.EventRepo.Get(row.EventID)
		if err != nil {
			continue
		}
		events = append(events, event.Matrix())
	}
	return events, nil
}

// stateDelta returns state events of a room that changed after a position.
func (s *Service) stateDelta(roomID string, since uint64) ([]matrix.Event, error) {
	rows, err := s.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	events := make([]matrix.Event, 0)
	for _, row := range rows {
		if row.StreamPos <= since {
			continue
		}
		event, err := s.db.EventRepo.Get(row.EventID)
		if err != nil {
			continue
		}
		events = append(events, event.Matrix())
	}
	return events, nil
}

// strippedState returns the state a non-joined user may see for a room.
func (s *Service) strippedState(ctx context.Context, roomID string) []matrix.Event {
	types := []string{
		matrix.TypeCreate, matrix.TypeName, matrix.TypeTopic, matrix.TypeAvatar,
		matrix.TypeCanonicalAlias, matrix.TypeJoinRules, matrix.TypeMember,
	}
	out := make([]matrix.Event, 0, 8)
	for _, typ := range types {
		rows, err := s.db.StateRepo.List(roomID, []string{typ}, "")
		if err != nil {
			continue
		}
		for _, row := range rows {
			event, err := s.db.EventRepo.Get(row.EventID)
			if err != nil {
				continue
			}
			ev := event.Matrix()
			// Stripped state carries no origin server timestamps per spec.
			ev.OriginServerTS = 0
			out = append(out, ev)
		}
	}
	return out
}

// invitedRoomSync renders the invite section of a sync response.
func (s *Service) invitedRoomSync(ctx context.Context, roomID string) (*matrix.InvitedRoom, error) {
	return &matrix.InvitedRoom{
		InviteState: matrix.State{Events: s.strippedState(ctx, roomID)},
	}, nil
}

// leftRoomSync renders the leave section of a sync response. A room is only
// included when it has events after the token, so leaving does not reappear on
// every subsequent sync.
func (s *Service) leftRoomSync(ctx context.Context, userID, roomID string, since uint64, limit int) (*matrix.LeftRoom, bool, error) {
	events, err := s.db.EventRepo.Timeline(roomID, since, limit)
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	if len(events) == 0 {
		return nil, false, nil
	}
	state, err := s.stateEvents(ctx, roomID, since, nil)
	if err != nil {
		return nil, false, err
	}
	accountData, err := s.roomAccountDataEvents(userID, roomID, since)
	if err != nil {
		return nil, false, err
	}
	room := &matrix.LeftRoom{
		Timeline: matrix.Timeline{
			Events:    toWire(events),
			Limited:   false,
			PrevBatch: s.Token(events[0].StreamPos),
		},
		State: matrix.State{Events: state, PrevBatch: s.Token(since)},
	}
	if len(accountData) > 0 {
		room.AccountData = &matrix.Ephemeral{Events: accountData}
	}
	return room, true, nil
}

// accountDataEvents returns the global account data events of a user.
func (s *Service) accountDataEvents(userID string, since uint64) ([]matrix.Event, error) {
	rows, err := s.db.AccountDataRepo.ListGlobal(userID, since)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return accountDataWire(rows), nil
}

// roomAccountDataEvents returns the room scoped account data of a user.
func (s *Service) roomAccountDataEvents(userID, roomID string, since uint64) ([]matrix.Event, error) {
	rows, err := s.db.AccountDataRepo.List(userID, roomID, since)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return accountDataWire(rows), nil
}

// accountDataWire converts account data rows into events.
func accountDataWire(rows []*persist.AccountData) []matrix.Event {
	out := make([]matrix.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, matrix.Event{
			Type:    row.Type,
			Content: matrix.RawMessage(row.Content),
		})
	}
	return out
}

// receiptEventsForUser groups the read receipts of a user by room, producing
// m.receipt ephemeral events.
func (s *Service) receiptEventsForUser(userID string, since uint64) (map[string][]matrix.Event, error) {
	rows, err := s.db.ReceiptRepo.ListForUser(userID, since)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	byRoom := map[string]matrix.Object{}
	for _, row := range rows {
		content, ok := byRoom[row.RoomID]
		if !ok {
			content = matrix.Object{}
			byRoom[row.RoomID] = content
		}
		eventBody, ok := content[row.EventID].(matrix.Object)
		if !ok {
			eventBody = matrix.Object{}
			content[row.EventID] = eventBody
		}
		eventBody[row.ReceiptType] = matrix.Object{
			userID: matrix.Object{
				"ts": row.SentTS,
			},
		}
	}
	out := make(map[string][]matrix.Event, len(byRoom))
	for roomID, content := range byRoom {
		out[roomID] = []matrix.Event{{
			Type:    matrix.TypeReceipt,
			RoomID:  roomID,
			Content: matrix.Encode(content),
		}}
	}
	return out, nil
}

// syncHeroCount is how many member ids the sync room summary names.
const syncHeroCount = 5

// syncRoomSummary returns the summary fields every member sees in a joined
// room: up to five relevant members (excluding the requester) and the member
// counts. It mirrors the room summary endpoint but stays cheap on purpose.
func (s *Service) syncRoomSummary(roomID, userID string) (heroes []string, joined, invited int64, err error) {
	members, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return nil, 0, 0, matrix.ErrInternal(err)
	}
	joined = int64(len(members))
	heroes = make([]string, 0, syncHeroCount)
	for _, member := range members {
		if member.UserID == userID {
			continue
		}
		if len(heroes) >= syncHeroCount {
			break
		}
		heroes = append(heroes, member.UserID)
	}
	invitedRows, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipInvite)
	if err != nil {
		return nil, 0, 0, matrix.ErrInternal(err)
	}
	invited = int64(len(invitedRows))
	return heroes, joined, invited, nil
}

// typingEvents returns the m.typing ephemeral event of a room when its typing
// indicators changed after a position.
func (s *Service) typingEvents(roomID, userID string, since uint64) ([]matrix.Event, error) {
	rows, err := s.db.TypingRepo.Recent(roomID, since)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	active, err := s.db.TypingRepo.ActiveUserIDs(roomID, userID, s.NowMS())
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	userIDs := make([]any, 0, len(active))
	for _, id := range active {
		userIDs = append(userIDs, id)
	}
	return []matrix.Event{{
		Type:   matrix.TypeTyping,
		RoomID: roomID,
		Content: matrix.Encode(matrix.Object{
			"typing":   len(userIDs) > 0,
			"user_ids": userIDs,
		}),
	}}, nil
}

// unreadCount counts the notifications of a user since their last read marker,
// returning the total and the highlighted share of it.
func (s *Service) unreadCount(ctx context.Context, userID, roomID string, since uint64) (int64, int64, error) {
	after := since
	if marker, err := s.db.ReadMarkerRepo.Get(userID, roomID); err == nil && marker.FullyReadEventID != "" {
		if event, err := s.db.EventRepo.GetInRoom(roomID, marker.FullyReadEventID); err == nil &&
			event.StreamPos > after {
			after = event.StreamPos
		}
	}
	count, err := s.db.NotificationRepo.CountAfter(userID, roomID, after)
	if err != nil {
		return 0, 0, matrix.ErrInternal(err)
	}
	highlights, err := s.db.NotificationRepo.CountHighlightedAfter(userID, roomID, after)
	if err != nil {
		return 0, 0, matrix.ErrInternal(err)
	}
	return count, highlights, nil
}
