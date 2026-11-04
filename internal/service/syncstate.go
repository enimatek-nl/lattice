package service

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// SyncFilter is the subset of a sync filter that Lattice honours.
type SyncFilter struct {
	// TimelineLimit is room.timeline.limit.
	TimelineLimit int
	// TimelineLimitSet says the filter named a limit at all. The distinction matters:
	// `"limit": 0` asks for no timeline events, while an unnamed limit asks for the
	// server's default. Reading both as zero hands every client that asked for nothing the
	// room's whole history, and every client that asked for nothing at all a default.
	TimelineLimitSet bool
	// TimelineTypes and TimelineSenders are room.timeline allow-lists; an empty list
	// restricts nothing. A filter that names types says which ones the client reads, and a
	// server that ignores the list sends a payload nobody asked for into rooms the client
	// is not looking at.
	TimelineTypes   []string
	TimelineSenders []string
	// Types and Senders are the room.state allow-lists.
	Types   []string
	Senders []string
	// NotTypes and NotSenders are the exclusions of both sections.
	NotTypes   []string
	NotSenders []string
	// LazyLoadState requests lazy loaded member state (served in full; the
	// flag only affects payload size expectations).
	LazyLoadState bool
}

// timelineLimit applies the filter limit when present, and answers zero when the filter asked
// for zero events.
func (f *SyncFilter) timelineLimit(fallback int) int {
	if f != nil && f.TimelineLimitSet {
		return f.TimelineLimit
	}
	if f != nil && f.TimelineLimit > 0 {
		return f.TimelineLimit
	}
	if fallback <= 0 {
		return 10
	}
	return fallback
}

// selects reports whether a value passes an allow-list and an exclusion list. An empty
// allow-list names nothing, which is the same as naming everything.
func selects(allow, block []string, value string) bool {
	for _, banned := range block {
		if banned == value {
			return false
		}
	}
	if len(allow) == 0 {
		return true
	}
	for _, wanted := range allow {
		if wanted == value {
			return true
		}
	}
	return false
}

// allowedInTimeline reports whether a timeline event passes the filter's timeline section.
func (f *SyncFilter) allowedInTimeline(typ, sender string) bool {
	if f == nil {
		return true
	}
	return selects(f.TimelineTypes, f.NotTypes, typ) && selects(f.TimelineSenders, f.NotSenders, sender)
}

// allowedInState reports whether a state event passes the filter's state section.
func (f *SyncFilter) allowedInState(typ, sender string) bool {
	if f == nil {
		return true
	}
	return selects(f.Types, f.NotTypes, typ) && selects(f.Senders, f.NotSenders, sender)
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
	// A selection is the three lists a filter section may name. The specification lets a
	// client write them on `room` for both sections and repeat them per section, where the
	// per-section list wins.
	type selection struct {
		Types      []string `json:"types"`
		Senders    []string `json:"senders"`
		NotTypes   []string `json:"not_types"`
		NotSenders []string `json:"not_senders"`
	}
	var body struct {
		Room struct {
			selection
			State struct {
				selection
				Limit           *int `json:"limit"`
				LazyLoadOptions struct {
					LazyLoad bool `json:"lazy_load_options"`
				} `json:"lazy_load_options"`
			} `json:"state"`
			Timeline struct {
				selection
				Limit *int `json:"limit"`
			} `json:"timeline"`
		} `json:"room"`
	}
	// Filters are supplied as JSON; a malformed filter is a client error.
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil, matrix.ErrInvalidRequest("invalid filter: %v", err)
	}
	filter.NotTypes = firstList(body.Room.State.NotTypes, body.Room.NotTypes)
	filter.NotSenders = firstList(body.Room.State.NotSenders, body.Room.NotSenders)
	filter.Types = firstList(body.Room.State.Types, body.Room.Types)
	filter.Senders = firstList(body.Room.State.Senders, body.Room.Senders)
	filter.TimelineTypes = firstList(body.Room.Timeline.Types, body.Room.Types)
	filter.TimelineSenders = firstList(body.Room.Timeline.Senders, body.Room.Senders)
	// The timeline section's own exclusions beat the room-wide ones.
	if len(body.Room.Timeline.NotTypes) > 0 {
		filter.NotTypes = body.Room.Timeline.NotTypes
	}
	if len(body.Room.Timeline.NotSenders) > 0 {
		filter.NotSenders = body.Room.Timeline.NotSenders
	}
	if body.Room.Timeline.Limit != nil {
		filter.TimelineLimit = *body.Room.Timeline.Limit
		filter.TimelineLimitSet = true
	}
	if body.Room.State.LazyLoadOptions.LazyLoad {
		filter.LazyLoadState = true
	}
	return filter, nil
}

// firstList answers the first list that names anything, which is how a per-section filter
// overrides a room-wide one without losing it when the section is silent.
func firstList(primary, fallback []string) []string {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}

// stateEventsWire renders state rows as events. The events a room's state points
// at are read in one query: rendering row by row turns a state block into a
// query per state key, which is the hot path of an initial sync.
func (s *Service) stateEventsWire(rows []*persist.RoomState, keep func(*persist.RoomState) bool) ([]matrix.Event, error) {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if keep != nil && !keep(row) {
			continue
		}
		ids = append(ids, row.EventID)
	}
	if len(ids) == 0 {
		return []matrix.Event{}, nil
	}
	events, err := s.db.EventRepo.GetByIDs(ids)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	out := make([]matrix.Event, 0, len(ids))
	for _, row := range rows {
		// A row whose event is gone (or that the filter dropped) has no entry.
		if event, ok := events[row.EventID]; ok {
			out = append(out, event.Matrix())
		}
	}
	return out, nil
}

// stateKeyOf names a state event by the pair that identifies it in a room.
func stateKeyOf(typ, stateKey string) string { return typ + "\x1f" + stateKey }

// stateKeysIn lists the state keys a timeline already carries. The state block of a sync is
// meant to describe the room around the events in it, and a room whose state block and
// timeline both name m.room.member/@bob with different content is a room that says two
// things about itself: a client that applies the state after the timeline ends up with an
// older membership than the one it was just shown, and one that applies it before sees the
// older one vanish. Each state key is therefore reported once, in whichever of the two
// places its current version sits.
func stateKeysIn(events []*persist.Event) map[string]bool {
	if len(events) == 0 {
		return nil
	}
	out := make(map[string]bool, len(events))
	for _, event := range events {
		if event.IsState {
			out[stateKeyOf(event.Type, event.StateKey)] = true
		}
	}
	return out
}

// stateEvents returns the full current state of a room.
func (s *Service) stateEvents(ctx context.Context, roomID string, since uint64, filter *SyncFilter, skip map[string]bool) ([]matrix.Event, error) {
	rows, err := s.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return s.stateEventsWire(rows, func(row *persist.RoomState) bool {
		if skip[stateKeyOf(row.Type, row.StateKey)] {
			return false
		}
		return filter.allowedInState(row.Type, row.Sender)
	})
}

// stateDelta returns state events of a room that changed after a position.
func (s *Service) stateDelta(roomID string, since uint64, skip map[string]bool) ([]matrix.Event, error) {
	rows, err := s.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return s.stateEventsWire(rows, func(row *persist.RoomState) bool {
		return row.StreamPos > since && !skip[stateKeyOf(row.Type, row.StateKey)]
	})
}

// strippedState returns the state a non-joined user may see for a room. The
// m.room.create event is part of it because it carries the room version: a
// client that cannot find it assumes version 1 and warns about an upgrade.
//
// Membership is limited to the two people the invitation is between. The
// specification lists the m.room.member events of the inviter and the invitee;
// carrying every member of the room hands a stranger the full guest list before
// they have done anything but receive an invitation.
func (s *Service) strippedState(ctx context.Context, roomID, invitee string) []matrix.StrippedStateEvent {
	out, err := s.strippedEvents(ctx, s.inviteStateRows(ctx, roomID, invitee))
	if err != nil {
		return nil
	}
	return out
}

// inviteStateRows picks the pieces of a room's state that travel to somebody who is not in
// it: the room's identity (its create event, name, topic, avatar, alias), how it is entered
// (join rule, encryption), and the two memberships the invitation is between - the invitee's
// own and the inviter's, which is how a client shows who invited them.
func (s *Service) inviteStateRows(ctx context.Context, roomID, invitee string) []*persist.RoomState {
	types := []string{
		matrix.TypeCreate, matrix.TypeName, matrix.TypeTopic, matrix.TypeAvatar,
		matrix.TypeCanonicalAlias, matrix.TypeJoinRules, matrix.TypeEncryption,
	}
	// One state read covers the whole block: listing a type at a time was a query
	// per type. The fetched rows are grouped by type so the block keeps the order
	// clients have come to expect, create first.
	rows, err := s.db.StateRepo.List(roomID, append(append([]string{}, types...), matrix.TypeMember), "")
	if err != nil {
		return nil
	}
	byType := map[string][]*persist.RoomState{}
	for _, row := range rows {
		byType[row.Type] = append(byType[row.Type], row)
	}
	keep := make([]*persist.RoomState, 0, len(types)+2)
	for _, typ := range types {
		keep = append(keep, byType[typ]...)
	}
	// The invitation event names the inviter in its sender; that is the only other
	// membership a client needs, to show who invited the user.
	inviter := ""
	for _, row := range byType[matrix.TypeMember] {
		if row.StateKey == invitee {
			inviter = row.Sender
			break
		}
	}
	for _, row := range byType[matrix.TypeMember] {
		if row.StateKey == invitee || (inviter != "" && row.StateKey == inviter) {
			keep = append(keep, row)
		}
	}
	return keep
}

// strippedEvents renders state rows for the client API's stripped state, which per the
// specification is the four fields that describe a piece of state and nothing else: no
// origin server timestamp, no hashes, no signatures. MSC4311 keeps that shape on the client
// side even though the same block is a set of complete events between servers, because a
// client is not being asked to check the room - it is being shown what it was invited to.
// A row whose event has gone is rendered from the row itself, as the specification allows.
func (s *Service) strippedEvents(ctx context.Context, rows []*persist.RoomState) ([]matrix.StrippedStateEvent, error) {
	if len(rows) == 0 {
		return []matrix.StrippedStateEvent{}, nil
	}
	out := make([]matrix.StrippedStateEvent, 0, len(rows))
	for _, row := range rows {
		// The four fields of a stripped state event, read from the state row rather than
		// copied out of the stored event: a stored event carries where it came from, when it
		// arrived and how it was signed, and all of that belongs to the room, not to the
		// notice a stranger is shown about it.
		out = append(out, matrix.StrippedStateEvent{
			Type:     row.Type,
			StateKey: row.StateKey,
			Sender:   row.Sender,
			Content:  matrix.RawMessage(row.Content),
		})
	}
	return out, nil
}

// invitedRoomSync renders the invite section of a sync response.
//
// An invitation is membership state rather than an event stream, so it is
// reported on every sync until it is accepted or withdrawn. Only an invitation
// that arrived after the token counts as new data: treating every outstanding
// invitation as new makes the long poll return at once, forever, which a client
// reads as an endless stream of updates and answers with a request per sync.
func (s *Service) invitedRoomSync(ctx context.Context, userID, roomID string,
	since uint64) (*matrix.InvitedRoom, bool, error) {
	return &matrix.InvitedRoom{
		InviteState: matrix.StrippedState{Events: s.strippedState(ctx, roomID, userID)},
	}, s.membershipAfter(roomID, userID, since), nil
}

// knockedRoomSync renders the knock section of a sync response: the state of a room a
// user has knocked on and is waiting to be let into. Like an invitation, a knock is a
// standing state rather than an event stream, so it is reported until the room answers it;
// only a knock made after the token counts as new, or the long poll would return at once
// forever. The knocker's own membership is part of the state shown, because it is the
// event that says the knock was made.
func (s *Service) knockedRoomSync(ctx context.Context, userID, roomID string,
	since uint64) (*matrix.KnockedRoom, bool, error) {
	return &matrix.KnockedRoom{
		KnockState: matrix.StrippedState{Events: s.strippedState(ctx, roomID, userID)},
	}, s.membershipAfter(roomID, userID, since), nil
}

// leftRoomSync renders the leave section of a sync response. A room is only
// included when it has events after the token, so leaving does not reappear on
// every subsequent sync.
// leftRoomSync renders the leave section of a room.
//
// A left room is reported once: the sync that carries the leave. A client that has been told a
// room is gone does not need to be told again on every sync afterwards, and a room the user
// walked out of years ago is not news on an incremental sync. The bound that follows from that
// is the important one for correctness: the room's history in the leave section stops at the
// leave, because what a former member is shown is what they saw while they were there, not
// whatever has been said since.
func (s *Service) leftRoomSync(ctx context.Context, userID, roomID string, since uint64,
	limit int, filter *SyncFilter) (*matrix.LeftRoom, bool, error) {
	left, err := s.membershipPos(roomID, userID)
	if err != nil {
		return nil, false, err
	}
	// since=0 is a first sync, where a room the user once left is reported with the history
	// they were part of; afterwards the room is reported only by the sync whose position sits
	// before the leave.
	if since > 0 && left <= since {
		return nil, false, nil
	}
	want := filter.timelineLimit(limit)
	window := []*persist.Event{}
	if want > 0 {
		window, err = s.db.EventRepo.TimelineUntil(roomID, since, left, want+1)
		if err != nil {
			return nil, false, matrix.ErrInternal(err)
		}
	}
	// MSC2675 applies to a former member as much as to a current one, with the room's own
	// decision about when its history opened deciding what of it they are shown.
	gate, err := s.historyGateFor(ctx, roomID, userID)
	if err != nil {
		return nil, false, err
	}
	kept := make([]*persist.Event, 0, len(window))
	for _, event := range window {
		if !filter.allowedInTimeline(event.Type, event.Sender) || !gate.allows(event) {
			continue
		}
		kept = append(kept, event)
	}
	// The extra event fetched above is what says whether the window was cut: a leave section
	// that ran out of room is limited in the same sense a joined timeline is.
	limited := len(window) > want
	if len(window) > want {
		window = window[:len(window)-1]
	}
	if len(window) > len(kept) {
		limited = true
	}
	state, err := s.stateUntil(ctx, roomID, since, left, filter, stateKeysIn(kept))
	if err != nil {
		return nil, false, err
	}
	accountData, err := s.roomAccountDataEvents(userID, roomID, since)
	if err != nil {
		return nil, false, err
	}
	room := &matrix.LeftRoom{
		Timeline: matrix.Timeline{
			Events:  toWire(kept),
			Limited: limited,
		},
		State: matrix.State{Events: state, PrevBatch: s.Token(since)},
	}
	if len(window) > 0 {
		room.Timeline.PrevBatch = s.Token(window[0].StreamPos)
	} else {
		room.Timeline.PrevBatch = s.Token(left)
	}
	if len(accountData) > 0 {
		room.AccountData = &matrix.Ephemeral{Events: accountData}
	}
	return room, len(kept) > 0 || len(state) > 0, nil
}

// membershipPos is where a user's current membership of a room was written. A membership row
// points at the event that says it, so the position of that event is the position of the fact.
func (s *Service) membershipPos(roomID, userID string) (uint64, error) {
	row, err := s.db.MembershipRepo.Get(roomID, userID)
	if err != nil {
		if persist.IsNotFound(err) {
			return 0, nil
		}
		return 0, matrix.ErrInternal(err)
	}
	event, err := s.db.EventRepo.Get(row.EventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return 0, nil
		}
		return 0, matrix.ErrInternal(err)
	}
	return event.StreamPos, nil
}

// stateUntil is the state block of a room as it stood at a position - for a leave section, as
// it stood when the user left.
//
// This server keeps the state a room holds, not a snapshot at every event, so the state at a
// past position is rebuilt: a state key whose current event predates the position is already
// the answer, and one that moved afterwards is walked back through the room's own versions of
// that key to the one in force then. A key first written after the position has no answer, and
// contributes nothing.
func (s *Service) stateUntil(ctx context.Context, roomID string, since, until uint64,
	filter *SyncFilter, skip map[string]bool) ([]matrix.Event, error) {
	rows, err := s.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	current := make([]*persist.Event, 0, len(rows))
	for _, row := range rows {
		if skip[stateKeyOf(row.Type, row.StateKey)] {
			continue
		}
		if !filter.allowedInState(row.Type, row.Sender) {
			continue
		}
		event, err := s.db.EventRepo.Get(row.EventID)
		if err != nil {
			continue
		}
		if event.StreamPos <= until && (since == 0 || event.StreamPos > since) {
			current = append(current, event)
			continue
		}
		// The key moved after the position. What was in force at the position is the last
		// earlier version of it, if the room ever wrote one.
		versions, err := s.db.EventRepo.StateHistory(roomID, row.Type, row.StateKey)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		var best *persist.Event
		for _, version := range versions {
			if version.StreamPos > until {
				break
			}
			best = version
		}
		if best != nil && (since == 0 || best.StreamPos > since) {
			current = append(current, best)
		}
	}
	out := make([]matrix.Event, 0, len(current))
	for _, event := range current {
		out = append(out, event.Matrix())
	}
	return out, nil
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

// receiptEventsForRooms groups the receipts of a set of rooms by room, producing
// m.receipt ephemeral events.
//
// Every receipt of the room is reported, not only the reader's own: seeing that the
// person you are talking to has read your message is the point of the event, and it is
// also how a receipt that arrived from another homeserver reaches a client.
func (s *Service) receiptEventsForRooms(roomIDs []string, since uint64) (map[string][]matrix.Event, error) {
	rows, err := s.db.ReceiptRepo.ListForRooms(roomIDs, since)
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
		reader := matrix.Object{"ts": row.SentTS}
		if row.Data != "" {
			if data := matrix.MustParseObject([]byte(row.Data)); len(data) > 0 {
				for key, value := range data {
					reader[key] = value
				}
			}
		}
		eventBody[row.ReceiptType] = matrix.Object{row.UserID: reader}
	}
	out := make(map[string][]matrix.Event, len(byRoom))
	for roomID, content := range byRoom {
		out[roomID] = []matrix.Event{{
			// As with typing: the room is where the event sits, not a field of it.
			Type:    matrix.TypeReceipt,
			Content: matrix.Encode(content),
		}}
	}
	return out, nil
}

// syncHeroCount is how many member ids the sync room summary names.
const syncHeroCount = 5

// pickHeroes names the members a room summary should show. The specification
// leaves the choice to the server and only fixes the maximum, so Lattice orders
// by recent activity in the room: whoever spoke most recently is the person the
// other members are most likely to be looking for. Members that never sent an
// event fall back to their membership row, so a freshly invited crowd still
// shows up in a stable order, and equal positions break by user id so two syncs
// of the same room agree.
func pickHeroes(members []*persist.RoomMembership, requester string, activity map[string]uint64, limit int) []string {
	candidates := make([]*persist.RoomMembership, 0, len(members))
	for _, member := range members {
		if member.UserID != requester {
			candidates = append(candidates, member)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		posI, posJ := activity[candidates[i].UserID], activity[candidates[j].UserID]
		if posI != posJ {
			return posI > posJ
		}
		if candidates[i].SentTS != candidates[j].SentTS {
			return candidates[i].SentTS > candidates[j].SentTS
		}
		return candidates[i].UserID < candidates[j].UserID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	heroes := make([]string, 0, len(candidates))
	for _, member := range candidates {
		heroes = append(heroes, member.UserID)
	}
	return heroes
}

// heroActivity returns the recency map pickHeroes needs, but only after checking
// that the room is big enough to need one. Small rooms and DMs show every member
// anyway, and skipping the scan keeps that query off the sync hot path.
func (s *Service) heroActivity(roomID string, members []*persist.RoomMembership, limit int) map[string]uint64 {
	if len(members) <= limit+1 {
		return nil
	}
	activity, err := s.db.EventRepo.LatestActivity(roomID)
	if err != nil {
		// Falling back to the membership timestamps is better than failing a sync
		// over a summary nicety.
		return nil
	}
	return activity
}

// syncRoomSummary returns the summary fields every member sees in a joined
// room: up to five relevant members (excluding the requester) and the member
// counts. It mirrors the room summary endpoint but stays cheap on purpose.
func (s *Service) syncRoomSummary(roomID, userID string) (heroes []string, joined, invited int64, err error) {
	members, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return nil, 0, 0, matrix.ErrInternal(err)
	}
	joined = int64(len(members))
	heroes = pickHeroes(members, userID, s.heroActivity(roomID, members, syncHeroCount), syncHeroCount)
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
		// No room_id on the event. The room is the key the ephemeral events are
		// grouped under in a sync, and the specification's example of this event names
		// only content and type; a second copy of the room id is a second answer to a
		// question already answered, and clients that read the wrong one disagree.
		Type: matrix.TypeTyping,
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
