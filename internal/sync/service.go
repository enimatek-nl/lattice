// Package sync implements client synchronisation: the `/sync` loop a Matrix client uses
// to learn what it has missed.
//
// The design follows the specification's own shape rather than inventing one:
//
//   - A sync token names a position in this server's global event stream
//     (`stream_ordering`). It is opaque to clients and only meaningful here: a token from
//     another server, or a made-up number, is rejected rather than silently treated as
//     "from the beginning", because a wrong-but-accepted token would hand a client a
//     history it believes is complete.
//   - A room is reported only when something in it changed since the token, except on a
//     first sync or a `full_state` sync, where every room the user is in is reported.
//   - The room sections follow the user's current membership: joined rooms under `join`,
//     invited under `invite`, knocked under `knock`, and left or banned under `leave`.
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/stream"
)

// Limits chosen to keep one sync cheap and bounded. The timeline limit default follows
// the Client-Server specification; the window cap bounds how much history a single sync
// reads when a client has been away for a long time, and it is reported honestly through
// the timeline's `limited` flag rather than hidden.
const (
	defaultTimelineLimit = 10
	maxTimelineLimit     = 1000
	windowCapMultiplier  = 10
	windowCapFloor       = 100
	windowCapCeiling     = 2000

	// maxTimeout bounds a long poll. The request middleware gives a request less time
	// than this to live, so a longer poll would be cut off by the server with a 504
	// instead of answering with the events that arrived.
	maxTimeout = 20 * time.Second

	// pollInterval is how often a waiting sync re-reads the database. A notification
	// channel would be cheaper; polling is used because it cannot miss an event written
	// by another connection, which a channel only covering this process could.
	pollInterval = 250 * time.Millisecond
)

// Token is a position in this server's event stream. The type lives in [stream] because
// `/messages` pages over the same positions, and a `prev_batch` from one endpoint is
// meant to work as `from` in the other.
type Token = stream.Position

// ParseToken reads a token a client returned.
// ParseToken reads a token issued by this server.
func ParseToken(raw string) (Token, error) { return stream.Parse(raw) }

// Request is one `/sync` call.
type Request struct {
	// Since is the raw token, and HasSince says whether the client sent one at all:
	// "from the beginning" and "from position 0" are not the same question.
	Since    string
	HasSince bool
	// Filter is the raw `filter` parameter: either a filter ID or a JSON object.
	Filter string
	// FullState asks for every room's state even when nothing changed.
	FullState bool
	// Timeout is how long the server may wait for something new.
	Timeout time.Duration
	// SetPresence is the presence this user wants reported while they sync. An empty
	// value means the client asked for nothing; an unknown one is ignored rather than
	// refused, because a client that mistypes a word should still get its events.
	SetPresence string
}

// Result is the `/sync` response.
type Result struct {
	NextBatch string `json:"next_batch"`
	Rooms     Rooms  `json:"rooms"`
	// AccountData carries the user's own values — push rules today — that changed since
	// their token. It is a section of its own because it belongs to the user rather
	// than to any room.
	AccountData Events `json:"account_data"`
	// Presence reports the users who share a room with this one and whose presence
	// changed since their token. It is a section of its own because it is not room data:
	// a client shows a dot beside a name, not beside a room.
	Presence    Events       `json:"presence"`
	ToDevice    []Event      `json:"to_device,omitempty"`
	Complete    bool         `json:"complete"`
	DeviceLists *DeviceLists `json:"device_lists,omitempty"`
}

// DeviceLists names the users whose keys a client should re-fetch, and the users whose
// keys it should drop.
//
// The section is present only when there is something to say, which is what the pointer is
// for: an empty-but-present object would read as "nothing changed", and that is a different
// answer from "there is nothing to report here".
//
// What this server does *not* do yet is distribute the keys themselves: there is no
// /keys/upload, /keys/query or /keys/claim, so a client that is told to re-fetch has
// nowhere to fetch from. That gap is recorded (T-3008b); the lists are still honest about
// which users changed.
type DeviceLists struct {
	Changed []string `json:"changed,omitempty"`
	Left    []string `json:"left,omitempty"`
}

// Rooms groups the room sections by the user's membership in them.
type Rooms struct {
	Join   map[string]JoinedRoom `json:"join,omitempty"`
	Invite map[string]InviteRoom `json:"invite,omitempty"`
	Leave  map[string]LeftRoom   `json:"leave,omitempty"`
	Knock  map[string]KnockRoom  `json:"knock,omitempty"`
}

// Event is a Matrix event as it goes to a client.
type Event map[string]any

// Events is the array wrapper the specification uses everywhere. `Limited` says the
// timeline is not the whole window; `PrevBatch` is the token a client uses to page back.
type Events struct {
	Events    []Event `json:"events"`
	PrevBatch string  `json:"prev_batch,omitempty"`
	Limited   bool    `json:"limited"`
}

// JoinedRoom is a room the user is in.
type JoinedRoom struct {
	Timeline            Events              `json:"timeline"`
	State               Events              `json:"state"`
	Ephemeral           Events              `json:"ephemeral"`
	AccountData         Events              `json:"account_data"`
	UnreadNotifications UnreadNotifications `json:"unread_notifications"`
	HighlightCount      int                 `json:"highlight_count"`
	// Summary is the room summary: the counts a client needs to show a room without
	// fetching its whole member list. The keys are the specification's, dotted and
	// quoted, and they live inside `summary` rather than beside it.
	Summary map[string]any `json:"summary,omitempty"`
}

// Room summary keys, as the specification names them.
const (
	summaryJoinedMemberCount  = "m.joined_member_count"
	summaryInvitedMemberCount = "m.invited_member_count"
)

// UnreadNotifications counts what the client has not seen. Read receipts are not
// implemented, so the honest answer is zero, not a guess.
type UnreadNotifications struct {
	NotificationCount int `json:"notification_count"`
	HighlightCount    int `json:"highlight_count"`
}

// InviteRoom is a room the user has been invited to.
type InviteRoom struct {
	InviteState Events `json:"invite_state"`
}

// KnockRoom is a room the user has asked to join.
type KnockRoom struct {
	KnockState Events `json:"knock_state"`
}

// LeftRoom is a room the user has left or been removed from.
type LeftRoom struct {
	Timeline    Events `json:"timeline"`
	State       Events `json:"state"`
	Ephemeral   Events `json:"ephemeral"`
	AccountData Events `json:"account_data"`
}

// Service answers sync requests against stored events.
type Service struct {
	db       *persist.Database
	events   persist.EventRepo
	state    persist.StateRepo
	filters  persist.FilterRepo
	account  persist.AccountDataRepo
	presence persist.PresenceRepo
	devLists persist.DeviceListRepo
	log      *slog.Logger
	// now is replaced in tests: `last_active_ago` is a difference against the current
	// clock, and a test cannot assert on a difference it cannot fix.
	now func() time.Time
}

// NewService wires the synchronisation service to the storage layer.
func NewService(db *persist.Database, log *slog.Logger) *Service {
	return &Service{
		db:       db,
		events:   db.Events,
		state:    db.State,
		filters:  db.Filters,
		account:  db.AccountData,
		presence: db.Presence,
		devLists: db.DeviceLists,
		log:      log,
		now:      time.Now,
	}
}

// Sync answers one synchronisation request for a user.
//
// The long poll is a loop over the same read, not a notification: each pass re-reads the
// stream position. That costs a query per interval, and it is the honest choice because
// this server can write events from a connection that has no way to reach a channel held
// by this request.
func (s *Service) Sync(ctx context.Context, userID string, req Request) (*Result, error) {
	var since Token
	if req.HasSince {
		token, err := ParseToken(req.Since)
		if err != nil {
			return nil, err
		}
		since = token
	}
	filter, err := s.filter(ctx, userID, req.Filter)
	if err != nil {
		return nil, err
	}

	timeout := req.Timeout
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	deadline := time.Now().Add(timeout)

	for {
		result, changed, err := s.build(ctx, userID, since, filter, req)
		if err != nil {
			return nil, err
		}
		// A first sync, a full-state sync, or a sync that found something answers at
		// once. Anything else waits, unless the client asked for no wait at all.
		if !req.HasSince || req.FullState || changed || timeout <= 0 {
			return result, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return result, nil
		}
		wait := pollInterval
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			// The client hung up. That is normal for a long poll, so it is not an
			// error to report as a 500; the handler answers nothing.
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// build reads one snapshot. `changed` says whether any room section was filled, which is
// what tells a long poll whether to return.
func (s *Service) build(ctx context.Context, userID string, since Token, filter Filter, req Request) (*Result, bool, error) {
	// The position covers every table that takes part in the stream, not events alone.
	// A token that stopped at the last event would be a token that had not seen the
	// account data or presence written after it, and a client holding it would be told
	// the same thing again on every sync — or, if those writes had taken positions below
	// a later event's, never told at all.
	position, err := s.db.StreamPosition(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("sync: cannot read the stream position: %w", err)
	}
	memberships, err := s.state.MembershipEventsFor(ctx, userID)
	if err != nil {
		return nil, false, fmt.Errorf("sync: cannot read the user's memberships: %w", err)
	}

	result := &Result{NextBatch: Token(position).String(), Complete: true}
	// The users whose presence this one is allowed to see, and the rooms that decide it.
	// Presence is not room data, but the room is what decides who may see it — and for
	// device list changes the room is the only way to answer, because the member list of
	// a room no longer contains the user who left.
	var fellowMembers []string
	for _, membership := range memberships {
		membershipValue := membershipValueOf(membership)
		// A room the user is out of is only reported up to the point where they left:
		// events after that are not theirs to see, and their membership event is the
		// boundary.
		until := int64(0)
		switch membershipValue {
		case eventauth.MembershipLeave, eventauth.MembershipBan:
			until = membership.StreamOrdering
		}
		room, err := s.room(ctx, membership.RoomID, since, until, position, filter, req)
		if err != nil {
			return nil, false, err
		}
		switch membershipValue {
		case eventauth.MembershipJoin:
			joined, invited, members, err := s.memberCounts(ctx, membership.RoomID)
			if err != nil {
				return nil, false, err
			}
			fellowMembers = append(fellowMembers, members...)
			// A joined room is interesting when something happened in it. If nothing
			// did, reporting it again would make a client re-read a room it already has
			// and would make "unchanged rooms are omitted" untrue.
			if !included(room, req) {
				continue
			}
			if result.Rooms.Join == nil {
				result.Rooms.Join = map[string]JoinedRoom{}
			}
			result.Rooms.Join[membership.RoomID] = JoinedRoom{
				Timeline: room.timeline,
				State:    room.state,
				// Ephemeral events (receipts, typing) and account data are not stored
				// yet, so the honest answer is an empty list, not a missing field.
				Ephemeral:           Events{Events: []Event{}},
				AccountData:         Events{Events: []Event{}},
				UnreadNotifications: UnreadNotifications{},
				HighlightCount:      0,
				Summary: map[string]any{
					summaryJoinedMemberCount:  joined,
					summaryInvitedMemberCount: invited,
				},
			}
		case eventauth.MembershipInvite:

			if result.Rooms.Invite == nil {
				result.Rooms.Invite = map[string]InviteRoom{}
			}
			result.Rooms.Invite[membership.RoomID] = InviteRoom{
				InviteState: s.strippedState(ctx, membership.RoomID, userID),
			}
		case eventauth.MembershipKnock:

			if result.Rooms.Knock == nil {
				result.Rooms.Knock = map[string]KnockRoom{}
			}
			result.Rooms.Knock[membership.RoomID] = KnockRoom{
				KnockState: s.strippedState(ctx, membership.RoomID, userID),
			}
		case eventauth.MembershipLeave, eventauth.MembershipBan:
			// Someone who is out of a room is told about the event that put them out.
			// Later events in that room are not theirs to see, so an older membership
			// event means this room has nothing left to report.
			if req.HasSince && !req.FullState && membership.StreamOrdering <= int64(since) {
				continue
			}
			if result.Rooms.Leave == nil {
				result.Rooms.Leave = map[string]LeftRoom{}
			}
			// A room that made its history world readable may still show its state to
			// someone who left. Any other visibility is the room saying that only its
			// members may see that, and a former member is not one. The timeline keeps the
			// leave event either way: it is the event that says why the room is here at all.
			state := room.state
			if stateVisible, err := s.stateVisibleToOutsider(ctx, membership.RoomID); err == nil && !stateVisible {
				state = Events{Events: []Event{}, PrevBatch: room.timeline.PrevBatch}
			}
			result.Rooms.Leave[membership.RoomID] = LeftRoom{
				Timeline:    room.timeline,
				State:       state,
				Ephemeral:   Events{Events: []Event{}},
				AccountData: Events{Events: []Event{}},
			}
		default:
			// An unrecognised membership value in our own storage is a storage fault,
			// not a protocol case: say so rather than inventing a section.
			return nil, false, fmt.Errorf("sync: room %s stores membership %q for %s, which this server does not understand",
				membership.RoomID, membershipValue, userID)
		}
	}

	// The user's own data counts as a change: a client waiting for something new is
	// waiting for news about itself too, and a ruleset the user just edited is news.
	accountData, err := s.accountEvents(ctx, userID, int64(since))
	if err != nil {
		return nil, false, err
	}
	result.AccountData = Events{Events: accountData}

	// Presence counts as a change: a client long-polling for news about a room is also
	// waiting to hear that somebody it shares a room with came back.
	presenceEvents, err := s.presenceEvents(ctx, userID, fellowMembers, int64(since), int64(position))
	if err != nil {
		return nil, false, err
	}
	result.Presence = Events{Events: presenceEvents}

	if lists := s.deviceListsFor(ctx, userID, int64(since), int64(position)); lists != nil {
		result.DeviceLists = lists
	}

	changed := result.Rooms.Join != nil || result.Rooms.Invite != nil ||
		result.Rooms.Leave != nil || result.Rooms.Knock != nil ||
		len(accountData) > 0 || len(presenceEvents) > 0
	return result, changed, nil
}

// stateVisibleToOutsider reports whether a room's state may be shown to someone who is
// no longer in it.
func (s *Service) stateVisibleToOutsider(ctx context.Context, roomID string) (bool, error) {
	entries, err := s.state.All(ctx, roomID)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Type != "m.room.history_visibility" {
			continue
		}
		stored, err := s.events.ByID(ctx, entry.EventID)
		if err != nil {
			return false, err
		}
		body, err := events.Parse(stored.Serving())
		if err != nil {
			return false, err
		}
		content, _ := body["content"].(map[string]any)
		visibility, _ := content["history_visibility"].(string)
		return visibility == "world_readable", nil
	}
	return false, nil
}

// memberCounts counts the room's members by membership for the room summary.
// memberCounts counts the room's members by membership for the room summary, and returns
// the user IDs of the joined members. The IDs come from the same read: a member event is
// both the count and the answer to "who is in here".
func (s *Service) memberCounts(ctx context.Context, roomID string) (joined, invited int, members []string, err error) {
	entries, err := s.state.ByType(ctx, roomID, eventauth.TypeMember)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("sync: cannot count the members of %s: %w", roomID, err)
	}
	eventIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		eventIDs = append(eventIDs, entry.EventID)
	}
	if len(eventIDs) == 0 {
		return 0, 0, nil, nil
	}
	stored, err := s.events.ByIDs(ctx, eventIDs)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("sync: cannot read the member events of %s: %w", roomID, err)
	}
	for _, event := range stored {
		userID := ""
		if event.StateKey != nil {
			userID = *event.StateKey
		}
		switch membershipValueOf(event) {
		case eventauth.MembershipJoin:
			joined++
			if userID != "" {
				members = append(members, userID)
			}
		case eventauth.MembershipInvite:
			invited++
		}
	}
	return joined, invited, members, nil
}

// deviceListsFor reads the device list changes addressed to this user since their token.
//
// The rows are addressed rather than shelved in rooms, and that is the point: a change a user
// needs to hear about because they left a room cannot be left in that room.
func (s *Service) deviceListsFor(ctx context.Context, userID string, since, until int64) *DeviceLists {
	rows, err := s.devLists.ListFor(ctx, userID, since, until)
	if err != nil {
		// A sync is not worth failing over a bookkeeping table, and the client will ask
		// again. The gap is logged rather than hidden.
		s.log.Warn("sync: cannot read the device list changes", "user_id", userID, "error", err)
		return nil
	}
	var changed, left []string
	seenChanged, seenLeft := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		// A user's own changes are not reported back to them.
		if row.UserID == userID || row.UserID == "" {
			continue
		}
		switch row.Kind {
		case persist.DeviceListChanged:
			if !seenChanged[row.UserID] {
				seenChanged[row.UserID] = true
				changed = append(changed, row.UserID)
			}
		case persist.DeviceListLeft:
			if !seenLeft[row.UserID] {
				seenLeft[row.UserID] = true
				left = append(left, row.UserID)
			}
		default:
			// A kind this server did not write is a storage fault worth knowing about.
			s.log.Warn("sync: unknown device list change kind",
				"room_id", row.RoomID, "user_id", row.UserID, "kind", row.Kind)
		}
	}
	if len(changed) == 0 && len(left) == 0 {
		return nil
	}
	sort.Strings(changed)
	sort.Strings(left)
	return &DeviceLists{Changed: changed, Left: left}
}

// presenceEvents reads the presence of the given users that changed in (since, until].
//
// A presence event in a sync response has no event ID and no timestamp: it is not an event
// in a room's graph, it is the current answer about a person, delivered at the position
// where it changed. The specification says so, and inventing an ID would invite a client
// to try to page back to it.
func (s *Service) presenceEvents(ctx context.Context, userID string, fellow []string, since, until int64) ([]Event, error) {
	var others []string
	seen := map[string]bool{}
	for _, candidate := range fellow {
		if candidate == userID || seen[candidate] {
			continue
		}
		seen[candidate] = true
		others = append(others, candidate)
	}
	rows, err := s.presence.ListChangedFor(ctx, others, since, until)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot read the presence of this user's fellow members: %w", err)
	}
	out := make([]Event, 0, len(rows))
	now := s.now().UnixMilli()
	for _, row := range rows {
		ago := now - row.LastActiveTS
		if ago < 0 {
			// A timestamp from the future means the clock moved backwards. Saying the
			// user was seen zero milliseconds ago is the honest floor; a negative age
			// is a number no client has a way to show.
			ago = 0
		}
		content := map[string]any{
			"presence":         row.State,
			"last_active_ago":  ago,
			"currently_active": row.Active,
		}
		if row.StatusMsg != "" {
			content["status_msg"] = row.StatusMsg
		}
		out = append(out, Event{"type": "m.presence", "sender": row.UserID, "content": content})
	}
	return out, nil
}

// included says whether a room has anything to report in this response.
//
// A membership event is itself an event in the room, so "something happened here" also
// covers the join, invite or knock that made the room interesting in the first place.
func included(room roomUpdate, req Request) bool {
	return !req.HasSince || req.FullState || room.total > 0
}

// roomUpdate is what one room contributes to a response.
type roomUpdate struct {
	timeline Events
	state    Events
	// total counts every event in the window, before filtering and truncation. It is
	// what makes the `limited` flag mean something.
	total int64
}

// room reads the window a client has not seen yet.
// room reads one room's window. `until` bounds the newest event the requester may see
// (zero means "as new as it gets"), which is how a room the user has left stops at the
// point where they left.
func (s *Service) room(ctx context.Context, roomID string, since Token, until int64, position int64, filter Filter, req Request) (roomUpdate, error) {
	limit := filter.TimelineLimit()

	total, err := s.events.CountAfter(ctx, roomID, int64(since))
	if err != nil {
		return roomUpdate{}, fmt.Errorf("sync: cannot count events in %s: %w", roomID, err)
	}

	// The window is read newest-first and then cut, so a client that has been away
	// gets the events it is missing rather than the oldest ones it already had.
	window, err := s.events.LatestEvents(ctx, roomID, windowCap(limit))
	if err != nil {
		return roomUpdate{}, fmt.Errorf("sync: cannot read events in %s: %w", roomID, err)
	}

	seen := make([]*persist.Event, 0, len(window))
	for _, event := range window {
		if since > 0 && event.StreamOrdering <= int64(since) {
			continue
		}
		// The boundary is inclusive: the membership event that took the user out of the
		// room is the one event about that room they are entitled to see, and it is how a
		// client learns the room left its joined list.
		if until > 0 && event.StreamOrdering > until {
			continue
		}
		seen = append(seen, event)
	}

	// `full_state` still limits the timeline by `since`; only the state is widened.
	// On a first sync there is no window, so the timeline is the most recent events.
	accepted := make([]Event, 0, len(seen))
	for _, event := range seen {
		if !filter.AllowsTimeline(event.Type) {
			continue
		}
		body, err := clientEvent(event)
		if err != nil {
			return roomUpdate{}, err
		}
		accepted = append(accepted, body)
	}
	if len(accepted) > limit {
		accepted = accepted[len(accepted)-limit:]
	}

	timeline := Events{Events: accepted, Limited: total > int64(len(accepted))}
	if len(accepted) > 0 {
		// prev_batch names the position just before the oldest event returned, which is
		// where pagination continues from.
		oldest := seen[len(seen)-len(accepted)]
		timeline.PrevBatch = Token(oldest.StreamOrdering).String()
	} else {
		timeline.PrevBatch = Token(position).String()
	}

	stateEvents, err := s.stateEvents(ctx, roomID, since, until, position, filter, req, accepted)
	if err != nil {
		return roomUpdate{}, err
	}
	return roomUpdate{timeline: timeline, state: stateEvents, total: total}, nil
}

// stateEvents returns the room's state as the client needs it: the state that applies at
// the start of the timeline, minus the state events already in the timeline.
//
// The exact rule the specification gives is "state at the start of the window", which
// would need a stored state snapshot per event. This server has no such snapshot yet, so
// it answers with the room's current state, keeping only entries that changed after the
// token (or all of them on a first or full-state sync), and dropping any that the
// timeline already carries. That is a documented approximation: it can name state that
// changed after the window, never state that has since been superseded.
func (s *Service) stateEvents(ctx context.Context, roomID string, since Token, until int64, position int64, filter Filter, req Request, timeline []Event) (Events, error) {
	entries, err := s.state.All(ctx, roomID)
	if err != nil {
		return Events{}, fmt.Errorf("sync: cannot read the state of %s: %w", roomID, err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.EventID)
	}
	if len(ids) == 0 {
		return Events{Events: []Event{}}, nil
	}
	stored, err := s.events.ByIDs(ctx, ids)
	if err != nil {
		return Events{}, fmt.Errorf("sync: cannot read state events of %s: %w", roomID, err)
	}
	inTimeline := make(map[string]bool, len(timeline))
	for _, event := range timeline {
		if id, ok := event["event_id"].(string); ok {
			inTimeline[id] = true
		}
	}

	out := make([]Event, 0, len(stored))
	for _, event := range stored {
		if inTimeline[event.EventID] {
			continue
		}
		if !filter.AllowsState(event.Type) {
			continue
		}
		if until > 0 {
			// Someone who left is shown the room's state as of the moment they left,
			// which is what their timeline window ends at. Current state would include
			// changes made after they went.
			if event.StreamOrdering > until {
				continue
			}
		} else if req.HasSince && !req.FullState && event.StreamOrdering <= int64(since) {
			continue
		}
		body, err := clientEvent(event)
		if err != nil {
			return Events{}, err
		}
		out = append(out, body)
	}
	sort.Slice(out, func(i, j int) bool {
		left, _ := out[i]["type"].(string)
		right, _ := out[j]["type"].(string)
		if left != right {
			return left < right
		}
		leftKey, _ := out[i]["state_key"].(string)
		rightKey, _ := out[j]["state_key"].(string)
		return leftKey < rightKey
	})
	return Events{Events: out, PrevBatch: Token(position).String()}, nil
}

// strippedState builds the `invite_state` / `knock_state` view: enough of the room for a
// client to show what it is being invited to, without letting the invitation read the
// room's full membership.
func (s *Service) strippedState(ctx context.Context, roomID, userID string) Events {
	entries, err := s.state.All(ctx, roomID)
	if err != nil {
		s.log.Warn("sync: cannot read room state for an invitation",
			slog.String("room_id", roomID), slog.String("error", err.Error()))
		return Events{Events: []Event{}}
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		// Someone being invited is told who else is invited, not who is in the room, so
		// member events other than their own stay out.
		if entry.Type == eventauth.TypeMember && entry.StateKey != userID {
			continue
		}
		ids = append(ids, entry.EventID)
	}
	if len(ids) == 0 {
		return Events{Events: []Event{}}
	}
	stored, err := s.events.ByIDs(ctx, ids)
	if err != nil {
		s.log.Warn("sync: cannot read state events for an invitation",
			slog.String("room_id", roomID), slog.String("error", err.Error()))
		return Events{Events: []Event{}}
	}
	out := make([]Event, 0, len(stored))
	for _, event := range stored {
		out = append(out, Event(stripForInvite(event.JSON)))
	}
	return Events{Events: out}
}

// clientEvent renders a stored event as it goes to a client. A redacted event is sent in
// its redacted form so the server does not hand out content it has agreed to remove.
func clientEvent(event *persist.Event) (Event, error) {
	raw := event.JSON
	if event.Redacted && len(event.RedactedJSON) > 0 {
		raw = event.RedactedJSON
	}
	body, err := events.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("sync: stored event %s does not parse: %w", event.EventID, err)
	}
	return Event(body), nil
}

// stripForInvite removes the fields that only matter between servers. The specification
// asks for this for stripped state: an invitation crosses a trust boundary, and the
// event's internal graph position and signatures say nothing a client should rely on.
func stripForInvite(raw []byte) map[string]any {
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return map[string]any{}
	}
	for _, key := range []string{"auth_events", "depth", "prev_events", "origin", "hashes", "signatures", "dest"} {
		delete(event, key)
	}
	return event
}

// membershipValueOf reads the membership a stored m.room.member event asserts.
func membershipValueOf(event *persist.Event) string {
	body, err := events.Parse(event.JSON)
	if err != nil {
		return ""
	}
	content, _ := body["content"].(map[string]any)
	value, _ := content["membership"].(string)
	return value
}

// windowCap bounds how much history one sync reads.
func windowCap(limit int) int {
	size := limit * windowCapMultiplier
	if size < windowCapFloor {
		size = windowCapFloor
	}
	if size > windowCapCeiling {
		size = windowCapCeiling
	}
	return size
}
