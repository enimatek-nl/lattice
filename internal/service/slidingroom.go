package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// slidingRoomResult builds the answer for one room and the row that records what the
// connection has then been told.
//
// Two things decide the shape. A room the user is in, or has been in, is described by its
// state, its timeline and its counts; a room they have only been invited to, or knocked on,
// or have turned down, is described by the stripped state that came with that membership -
// there is nothing else this server can honestly say about a room the user is not part of.
// The other is whether this connection has seen the room before: the first time, an omitted
// field means "not set", and every later time it means "unchanged".
func (s *Service) slidingRoomResult(ctx context.Context, userID string, fromPos, toPos uint64,
	plan slidingRoomPlan, isDM bool) (matrix.SlidingRoom, *persist.SlidingSyncRoom, error) {
	roomID := plan.entry.roomID
	known := plan.known
	initial := known == nil || !known.Sent
	room := matrix.SlidingRoom{
		Membership: plan.entry.membership,
		Lists:      plan.lists,
		Initial:    initial,
	}
	next := persist.SlidingSyncRoom{
		RoomID:        roomID,
		Sent:          true,
		TimelinePos:   plan.entry.activity,
		StatePos:      toPos,
		Config:        plan.config.key(),
		IsDM:          isDM,
		BumpStamp:     plan.entry.bump,
		TimelineCount: plan.config.timelineLimit,
	}
	if next.BumpStamp == 0 {
		next.BumpStamp = plan.entry.membershipPos
	}
	if !initial {
		next.TimelinePos = known.TimelinePos
	}
	if !initial && known.BumpStamp == next.BumpStamp {
		// An activity value the connection already has is not repeated, and the value the
		// connection keeps is the one it was last shown.
		next.BumpStamp = known.BumpStamp
	} else {
		value := int64(next.BumpStamp)
		room.BumpStamp = &value
	}

	stateRows, err := s.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		return room, nil, matrix.ErrInternal(err)
	}
	byKey := make(map[string]*persist.RoomState, len(stateRows))
	for _, row := range stateRows {
		byKey[stateKeyOf(row.Type, row.StateKey)] = row
	}

	if !plan.entry.everJoined {
		// Everything a client can be told about a room the user has never been in is in the
		// membership event that named them: the room's identity, who invited them, how it is
		// entered. The joined half - counts, timeline, state deltas - is not theirs to see.
		room.StrippedState = s.strippedState(ctx, roomID, userID)
		if isDM || initial {
			flag := isDM
			room.IsDM = &flag
		}
		return room, &next, nil
	}

	// The counts and the name come from the room's current state, which is already in hand.
	var joined, invited int64
	for _, row := range stateRows {
		if row.Type != matrix.TypeMember {
			continue
		}
		content := matrix.MustParseObject([]byte(row.Content))
		switch content["membership"] {
		case matrix.MembershipJoin:
			joined++
		case matrix.MembershipInvite:
			invited++
		}
	}
	next.JoinedCount = int(joined)
	next.InvitedCount = int(invited)
	if initial || known.JoinedCount != int(joined) {
		value := joined
		room.JoinedCount = &value
	}
	if initial || known.InvitedCount != int(invited) {
		value := invited
		room.InvitedCount = &value
	}

	// The name and the avatar have three states a string cannot hold: unchanged, set, and
	// removed. A removal has to be said out loud, because a client that stops receiving the
	// field cannot tell "still called that" from "the name is gone".
	if changed := initial || slidingStateChanged(stateRow(byKey, matrix.TypeName, ""), known.StatePos); changed {
		if row, ok := byKey[stateKeyOf(matrix.TypeName, "")]; ok {
			if name, found := stringAt(matrix.MustParseObject([]byte(row.Content)), "name"); found && name != "" {
				room.Name = jsonString(name)
			} else {
				room.Name = json.RawMessage("null")
			}
		} else {
			room.Name = json.RawMessage("null")
		}
	}
	if changed := initial || slidingStateChanged(stateRow(byKey, matrix.TypeAvatar, ""), known.StatePos); changed {
		if row, ok := byKey[stateKeyOf(matrix.TypeAvatar, "")]; ok {
			if url, found := stringAt(matrix.MustParseObject([]byte(row.Content)), "url"); found && url != "" {
				room.Avatar = jsonString(url)
			} else {
				room.Avatar = json.RawMessage("null")
			}
		} else {
			room.Avatar = json.RawMessage("null")
		}
	}
	if isDM || initial {
		flag := isDM
		room.IsDM = &flag
	}

	// The timeline: the room's most recent events, however many the config asks for. What is
	// "new" to this connection is measured against the connection, not against the room, so a
	// room that bubbled into the window carries its history without pretending it just
	// happened.
	// The timeline of a room the connection has seen before is, by default, only what has
	// happened since. Sending the whole window again on every sync would make the endpoint
	// no cheaper than the sync it replaces. The exception is the widened window: when the
	// client asks for more events than this connection holds, the older events are sent even
	// though some of them were sent before, and that is flagged.
	total, err := s.db.EventRepo.TimelineCount(roomID, 0)
	if err != nil {
		return room, nil, matrix.ErrInternal(err)
	}
	from := uint64(0)
	expansion := false
	if !initial {
		from = known.TimelinePos
		expansion = plan.config.timelineLimit > known.TimelineCount
	}
	window, err := s.slidingWindow(ctx, roomID, userID, plan.config.timelineLimit, from, expansion)
	if err != nil {
		return room, nil, err
	}
	var live int
	for _, entry := range window {
		if entry.pos > fromPos {
			live++
		}
	}
	if live > 0 {
		room.NumLive = &live
	}
	// "limited" says there is more of the room than this response carries - history behind
	// the window, or a gap inside it.
	room.Limited = total > len(window) || (!initial && total > known.TimelineCount+countAfter(window, known.TimelinePos))
	if len(window) > 0 {
		room.PrevBatch = s.Token(window[0].pos)
		next.TimelinePos = window[len(window)-1].pos
	} else {
		room.PrevBatch = s.Token(plan.entry.activity)
	}
	if expansion {
		for _, entry := range window {
			if entry.pos <= known.TimelinePos {
				room.ExpandedTimeline = true
				break
			}
		}
	}
	if !initial {
		held := known.TimelineCount + countAfter(window, known.TimelinePos)
		if held > total {
			held = total
		}
		next.TimelineCount = held
	}
	if room.ExpandedTimeline && next.TimelineCount < len(window) {
		next.TimelineCount = len(window)
	}
	events := make([]matrix.Event, 0, len(window))
	for _, entry := range window {
		events = append(events, entry.event)
	}
	room.Timeline = events

	// The state the client asked for, narrowed to what this connection has not been shown.
	state, sent, err := s.slidingState(ctx, roomID, plan.config, stateRows, byKey, window, known, initial)
	if err != nil {
		return room, nil, err
	}
	room.RequiredState = state
	next.State = strings.Join(sent, "\n")

	if len(room.Heroes) == 0 && (room.Name == nil || initial) {
		// Heroes only matter when the client has to name the room itself.
		if nameless := room.Name == nil || string(room.Name) == "null"; nameless {
			room.Heroes = slidingHeroes(stateRows, plan.entry.membership, userID)
		}
	}
	return room, &next, nil
}

// slidingEvent is a timeline event and where it sits in the stream. The position is not part
// of what the client is sent, but it is what "live", "limited" and "expanded" are measured
// against, so the window carries it beside the event rather than hiding it in the event.
type slidingEvent struct {
	event matrix.Event
	pos   uint64
}

// slidingWindow reads the timeline events this response carries.
//
// Normally that is the events after a position, capped at the configured limit. When the
// client has widened the window, the MSC asks for the newest limit events even if some of them
// were sent before, because a client that asked for twenty events of a room it is now looking
// at wants twenty and does not want to page for the missing ones.
func (s *Service) slidingWindow(ctx context.Context, roomID, userID string, limit int,
	after uint64, whole bool) ([]slidingEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	if whole {
		rows, err := s.db.EventRepo.History(roomID, 0, "b", limit, 0)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		return s.slidingEvents(ctx, rows, roomID, userID)
	}
	// Read the newest events before the horizon and keep the ones that came after it: the
	// window has to be capped, and capping it from the wrong end would send the oldest news
	// of a busy room and drop the newest.
	rows, err := s.db.EventRepo.History(roomID, 0, "b", limit+slidingReadAhead, 0)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	kept := make([]*persist.Event, 0, limit)
	// History hands the newest first, and the cap has to fall on that end: walking from the
	// other end would keep the oldest news of a busy room and drop the newest.
	for _, row := range rows {
		if row.StreamPos <= after {
			break
		}
		kept = append(kept, row)
		if len(kept) == limit {
			break
		}
	}
	return s.slidingEvents(ctx, kept, roomID, userID)
}

// slidingReadAhead is how far past the limit the window reads so that a gap of already-seen
// events does not eat the events that are actually new.
const slidingReadAhead = 100

// slidingEvents turns stored events into the window's oldest-first order, with the bundled
// aggregations /v3/sync attaches: a client reading a room through sliding sync sees reactions,
// edits and thread counts the same way it sees them anywhere else.
func (s *Service) slidingEvents(ctx context.Context, rows []*persist.Event, roomID, userID string) ([]slidingEvent, error) {
	// MSC2675: a room whose history opened when the user joined does not show them the
	// events it held before that, and a window filled from a peer is exactly how those
	// events arrive. The cursors of the window are unaffected: they name positions in the
	// room, not events the client was shown.
	gate, err := s.historyGateFor(ctx, roomID, userID)
	if err != nil {
		return nil, err
	}
	rows, _ = gate.keepReadable(rows)
	out := make([]slidingEvent, 0, len(rows))
	wire := make([]matrix.Event, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, slidingEvent{event: rows[i].Matrix(), pos: rows[i].StreamPos})
		wire = append(wire, out[len(out)-1].event)
	}
	if err := s.annotateThreads(roomID, userID, wire); err != nil {
		return nil, err
	}
	return out, nil
}

// countAfter counts the window events past a position.
func countAfter(window []slidingEvent, after uint64) int {
	var count int
	for _, entry := range window {
		if entry.pos > after {
			count++
		}
	}
	return count
}

// slidingState decides which of a room's current state a connection is shown.
//
// A first sight of a room is the whole set the config asks for. Afterwards only the state that
// has moved since the room was last sent arrives, plus a stub for each piece the connection
// was shown and the room no longer has - because a client that never hears the difference
// keeps displaying a room name somebody deleted.
func (s *Service) slidingState(ctx context.Context, roomID string, config slidingConfig,
	rows []*persist.RoomState, byKey map[string]*persist.RoomState, window []slidingEvent,
	known *persist.SlidingSyncRoom, initial bool) ([]any, []string, error) {
	from := uint64(0)
	if !initial && known != nil {
		from = known.StatePos
	}
	lazy := config.lazyMembers
	wanted := map[string]bool{}
	if lazy {
		// Whoever spoke in the window, and whoever a membership event in the window is
		// about: without them a client cannot render what it is being shown.
		for _, entry := range window {
			wanted[entry.event.Sender] = true
			if entry.event.Type == matrix.TypeMember && entry.event.StateKey != nil {
				wanted[*entry.event.StateKey] = true
			}
		}
		if !initial && known != nil && known.TimelineCount >= 0 {
			// A non-gappy sync also carries every membership change since the connection last
			// saw the room, which is how a client keeps a member list without asking for one.
			for _, row := range rows {
				if row.Type == matrix.TypeMember && row.StreamPos > from {
					wanted[row.StateKey] = true
				}
			}
		}
	}

	// A redaction inside the window is state this room no longer stands behind. The state row
	// stays - a redacted event is still the event that holds the key - so the way a client
	// learns is a stub naming the key, which is what the MSC asks for. Membership is the
	// exception: redacting a membership event does not end the membership, and a client told
	// to delete it would show a member who is still in the room.
	redacted, err := s.slidingRedactions(roomID, from)
	if err != nil {
		return nil, nil, err
	}

	out := make([]any, 0, len(rows))
	sent := make([]string, 0, len(rows))
	for _, row := range rows {
		keep := false
		if row.Type == matrix.TypeMember {
			keep = slidingKeepMember(config, wanted, row)
		} else {
			keep = config.matchesState(row.Type, row.StateKey)
		}
		if !keep {
			continue
		}
		key := stateKeyOf(row.Type, row.StateKey)
		if redacted[key] {
			// A connection that was never shown this state has nothing to delete.
			if !initial && slidingWasSent(known, key) {
				out = append(out, matrix.StateStub{Type: row.Type, StateKey: row.StateKey})
			}
			continue
		}
		if !initial && row.StreamPos <= from {
			continue
		}
		event, err := s.slidingStateEvent(row)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, event)
		sent = append(sent, key)
	}

	// State that has gone away is named by a stub. Only state the connection was actually
	// shown, and still would be under the current config, is worth the client's deletion.
	if !initial && known != nil && known.State != "" {
		for _, key := range strings.Split(known.State, "\n") {
			if key == "" {
				continue
			}
			if _, still := byKey[key]; still {
				continue
			}
			typ, stateKey := splitStateKey(key)
			if !config.matchesState(typ, stateKey) {
				continue
			}
			out = append(out, matrix.StateStub{Type: typ, StateKey: stateKey})
		}
	}
	if len(out) == 0 {
		return nil, sent, nil
	}
	return out, sent, nil
}

// slidingKeepMember decides whether one membership event belongs in the response. Lazy
// loading replaces the ordinary filtering for memberships - the MSC is explicit that the
// exclusion list does not apply to them - except where the client asked for a named member.
func slidingKeepMember(config slidingConfig, wanted map[string]bool, row *persist.RoomState) bool {
	if !config.lazyMembers {
		return config.matchesState(row.Type, row.StateKey)
	}
	if wanted[row.StateKey] {
		return true
	}
	for _, pattern := range config.include {
		if pattern.anyKey {
			continue
		}
		if (pattern.anyType || pattern.typ == matrix.TypeMember) && pattern.stateKey == row.StateKey {
			return config.matchesState(row.Type, row.StateKey)
		}
	}
	return false
}

// slidingStateEvent renders one state row as an event of the room.
func (s *Service) slidingStateEvent(row *persist.RoomState) (matrix.Event, error) {
	event, err := s.db.EventRepo.Get(row.EventID)
	if err != nil {
		// The state row is the record of what the room says about itself; an event that has
		// been purged from under it does not make the room forget it. Render the row.
		return matrix.Event{
			Type:     row.Type,
			StateKey: &row.StateKey,
			Sender:   row.Sender,
			Content:  matrix.RawMessage(row.Content),
		}, nil
	}
	return event.Matrix(), nil
}

// slidingRedactions names the pieces of a room's state that a redaction has covered over since
// a position. A redaction is a separate event from the state it works on, so finding them is a
// look at the room's recent events of its own: a client that asked for no timeline still has to
// be told that state it holds is gone.
func (s *Service) slidingRedactions(roomID string, from uint64) (map[string]bool, error) {
	rows, err := s.db.EventRepo.History(roomID, 0, "b", slidingReadAhead, 0)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	out := map[string]bool{}
	for _, row := range rows {
		if row.StreamPos <= from {
			break
		}
		if row.Type != matrix.TypeRedaction {
			continue
		}
		target, err := s.db.EventRepo.GetInRoom(roomID, row.Matrix().Redacts)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		if target == nil || !target.IsState {
			continue
		}
		if target.Type == matrix.TypeMember {
			// Redacting a membership does not end it, and a client told to delete the entry
			// would drop a member who is still in the room.
			continue
		}
		out[stateKeyOf(target.Type, target.StateKey)] = true
	}
	return out, nil
}

// slidingWasSent reports whether a connection was previously shown a piece of state, which is
// what decides whether naming its disappearance tells the client anything.
func slidingWasSent(known *persist.SlidingSyncRoom, key string) bool {
	if known == nil || known.State == "" {
		return false
	}
	for _, entry := range strings.Split(known.State, "\n") {
		if entry == key {
			return true
		}
	}
	return false
}

// stateRow finds one piece of the room's state in a map keyed by the composite key, and
// returns nil when the room does not have it.
func stateRow(byKey map[string]*persist.RoomState, typ, stateKey string) *persist.RoomState {
	return byKey[stateKeyOf(typ, stateKey)]
}

// slidingStateChanged reports whether a state row was written after a position.
func slidingStateChanged(row *persist.RoomState, from uint64) bool {
	return row != nil && row.StreamPos > from
}

// slidingHeroes names the members a client can call a room by: the ones most recently seen in
// it, joined first, then invited, then left. The requester is not a hero of its own room.
func slidingHeroes(stateRows []*persist.RoomState, membership, requester string) []matrix.Hero {
	type candidate struct {
		userID   string
		position uint64
		members  int
	}
	order := map[string]int{matrix.MembershipJoin: 0, matrix.MembershipInvite: 1}
	candidates := make([]candidate, 0, len(stateRows))
	for _, row := range stateRows {
		if row.Type != matrix.TypeMember || row.StateKey == requester {
			continue
		}
		content := matrix.MustParseObject([]byte(row.Content))
		memberType, _ := content["membership"].(string)
		rank, ok := order[memberType]
		if !ok {
			rank = 2
		}
		candidates = append(candidates, candidate{userID: row.StateKey, position: row.StreamPos, members: rank})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].members != candidates[j].members {
			return candidates[i].members < candidates[j].members
		}
		if candidates[i].position != candidates[j].position {
			return candidates[i].position > candidates[j].position
		}
		return candidates[i].userID < candidates[j].userID
	})
	if len(candidates) > syncHeroCount {
		candidates = candidates[:syncHeroCount]
	}
	heroes := make([]matrix.Hero, 0, len(candidates))
	for _, entry := range candidates {
		hero := matrix.Hero{UserID: entry.userID}
		for _, row := range stateRows {
			if row.Type != matrix.TypeMember || row.StateKey != entry.userID {
				continue
			}
			content := matrix.MustParseObject([]byte(row.Content))
			hero.DisplayName, _ = content["displayname"].(string)
			hero.AvatarURL, _ = content["avatar_url"].(string)
			break
		}
		heroes = append(heroes, hero)
	}
	return heroes
}

// slidingShouldReport decides whether a room belongs in this response at all: it has never
// been sent to the connection, or something in it has moved since.
func slidingShouldReport(entry slidingRoomEntry, known *persist.SlidingSyncRoom, config slidingConfig) bool {
	if known == nil || !known.Sent {
		return true
	}
	if entry.activity > known.TimelinePos {
		return true
	}
	if entry.membershipPos > known.StatePos {
		return true
	}
	if known.BumpStamp != entry.bump {
		return true
	}
	// A wider config is worth answering at once: the MSC asks that a room whose window has
	// grown be sent now rather than at the room's next event.
	return known.Config != config.key()
}

// jsonString renders a value as a JSON string, for the fields that have to be able to say
// null as well.
func jsonString(value string) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}

// stringAt reads a string out of an object, reporting whether it was there and a string.
func stringAt(object matrix.Object, key string) (string, bool) {
	value, ok := object[key].(string)
	return value, ok
}

// splitStateKey undoes [stateKeyOf].
func splitStateKey(key string) (typ, stateKey string) {
	if index := strings.IndexByte(key, '\x1f'); index >= 0 {
		return key[:index], key[index+1:]
	}
	return key, ""
}
