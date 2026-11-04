package service

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// The limits MSC4186 places on a sliding sync request, and the ones this server adds to keep
// a connection's stored state bounded. A request over a spec limit is refused with
// M_INVALID_PARAM, because the client asked for something the endpoint will not do.
const (
	slidingMaxLists         = 100
	slidingMaxSubscriptions = 100
	slidingMaxConnIDLen     = 128
	slidingMaxNameLen       = 64
	// slidingMaxTimelineLimit caps how many events of one room a request may ask for. The
	// MSC lets a server cap the number, and a client that asks for ten thousand events of
	// a hundred rooms is not asking for a sync.
	slidingMaxTimelineLimit = 1000
	// slidingMaxConnections bounds the connections one device may leave standing. Each
	// costs a row for the device and a row per room it has been shown, which is otherwise
	// only bounded by how many connections a client can dream up.
	slidingMaxConnections = 8
	// slidingExpireRooms is the point at which a delta would be bigger than starting over:
	// the MSC lets a server answer such a request with M_UNKNOWN_POS so the client begins
	// again instead of receiving a response nobody can hold.
	slidingExpireRooms = 1000
)

// slidingConfig is the combined room config a room is reported under: what a client asked for
// across every rule that matched the room.
type slidingConfig struct {
	timelineLimit int
	include       []slidingStatePattern
	exclude       []slidingStatePattern
	lazyMembers   bool
}

// slidingStatePattern is one required_state entry. A half that was left out (or written as
// "*") matches everything on that axis.
type slidingStatePattern struct {
	typ      string
	stateKey string
	anyType  bool
	anyKey   bool
}

// slidingStatePatternOf reads one required_state element off the wire.
func slidingStatePatternOf(element matrix.SlidingStateElement) slidingStatePattern {
	pattern := slidingStatePattern{}
	if element.Type == nil || *element.Type == "*" {
		pattern.anyType = true
	} else {
		pattern.typ = *element.Type
	}
	if element.StateKey == nil || *element.StateKey == "*" {
		pattern.anyKey = true
	} else {
		pattern.stateKey = *element.StateKey
	}
	return pattern
}

// matches reports whether a piece of state falls under this pattern.
func (p slidingStatePattern) matches(typ, stateKey string) bool {
	if !p.anyType && p.typ != typ {
		return false
	}
	if !p.anyKey && p.stateKey != stateKey {
		return false
	}
	return true
}

// key renders the pattern as the string it is stored and deduplicated under.
func (p slidingStatePattern) key() string {
	left, right := p.typ, p.stateKey
	if p.anyType {
		left = "*"
	}
	if p.anyKey {
		right = "*"
	}
	return left + "\x1f" + right
}

// wantsAllMembers reports whether this config asks for every membership of the room. It is
// what stops a combined config from being lazy: a client that asked plainly for the room's
// memberships must not be given a subset because another list happened to be lazy.
func (c slidingConfig) wantsAllMembers() bool {
	if c.lazyMembers {
		return false
	}
	for _, pattern := range c.include {
		if (pattern.anyType || pattern.typ == matrix.TypeMember) && pattern.anyKey {
			return true
		}
	}
	return false
}

// matchesState reports whether a piece of the room's current state is to be returned.
func (c slidingConfig) matchesState(typ, stateKey string) bool {
	for _, pattern := range c.exclude {
		if pattern.matches(typ, stateKey) {
			return false
		}
	}
	for _, pattern := range c.include {
		if pattern.matches(typ, stateKey) {
			return true
		}
	}
	return false
}

// merge combines two configs into the superset the MSC asks for: the wider timeline, the
// union of the state sets, and lazy memberships only when no config wants them all.
func (c slidingConfig) merge(other slidingConfig) slidingConfig {
	combined := slidingConfig{timelineLimit: c.timelineLimit}
	if other.timelineLimit > combined.timelineLimit {
		combined.timelineLimit = other.timelineLimit
	}
	seen := map[string]bool{}
	for _, list := range [][]slidingStatePattern{c.include, other.include} {
		for _, pattern := range list {
			if seen[pattern.key()] {
				continue
			}
			seen[pattern.key()] = true
			combined.include = append(combined.include, pattern)
		}
	}
	seen = map[string]bool{}
	for _, list := range [][]slidingStatePattern{c.exclude, other.exclude} {
		for _, pattern := range list {
			if seen[pattern.key()] {
				continue
			}
			seen[pattern.key()] = true
			combined.exclude = append(combined.exclude, pattern)
		}
	}
	combined.lazyMembers = (c.lazyMembers || other.lazyMembers) &&
		!c.wantsAllMembers() && !other.wantsAllMembers()
	return combined
}

// key renders the config as the string it is remembered under, so that a later request can be
// compared with what the connection was already given.
func (c slidingConfig) key() string {
	parts := []string{
		"t=" + strconv.Itoa(c.timelineLimit),
		"lazy=" + strconv.FormatBool(c.lazyMembers),
		"in=" + patternKeys(c.include),
		"out=" + patternKeys(c.exclude),
	}
	return strings.Join(parts, "|")
}

// patternKeys renders a list of patterns in a stable order, so that two requests that ask for
// the same state in a different order compare equal.
func patternKeys(patterns []slidingStatePattern) string {
	keys := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		keys = append(keys, pattern.key())
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// configOf builds the room config of one list or subscription.
func configOf(timelineLimit int, required matrix.SlidingRequiredState) slidingConfig {
	if timelineLimit < 0 {
		timelineLimit = 0
	}
	if timelineLimit > slidingMaxTimelineLimit {
		timelineLimit = slidingMaxTimelineLimit
	}
	config := slidingConfig{timelineLimit: timelineLimit, lazyMembers: required.LazyMembers}
	for _, element := range required.Include {
		config.include = append(config.include, slidingStatePatternOf(element))
	}
	for _, element := range required.Exclude {
		config.exclude = append(config.exclude, slidingStatePatternOf(element))
	}
	return config
}

// slidingRoomEntry is one room in the user's list, as the endpoint sees it.
type slidingRoomEntry struct {
	roomID     string
	membership string
	// everJoined says the user has been in the room, which outlives the membership row that
	// said so; forced says somebody else ended their membership. Together they answer the
	// question the list rules ask: is this a room the user walked out of, or one they were
	// thrown out of?
	everJoined bool
	forced     bool
	// activity is when the room last moved on this server, which is what orders the list.
	activity uint64
	// bump is the last piece of real activity in the room, which is what the client sorts
	// its own list by.
	bump uint64
	// membershipPos is where the user's own membership happened, which stands in for
	// activity in a room they are no longer part of.
	membershipPos uint64
}

// slidingRooms builds the user's room list: the rooms the endpoint may report at all, in the
// order a list indexes into them.
//
// The MSC's rule for a room the user left is that it counts only if this connection has been
// shown it before - the client knows the user left, and resending it buys nothing. A room the
// user was kicked or banned out of always counts, because otherwise the one fact the client
// cannot guess - that somebody else closed the door - never arrives. A ban that landed on
// somebody who was never in the room is not their room list's business at all.
func (s *Service) slidingRooms(ctx context.Context, userID string, seen map[string]bool) ([]slidingRoomEntry, error) {
	rows, err := s.db.MembershipRepo.RowsFor(userID,
		matrix.MembershipJoin, matrix.MembershipInvite, matrix.MembershipKnock,
		matrix.MembershipLeave, matrix.MembershipBan)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	// Whose hand moved the membership out of the room is written in the membership event, so
	// the events are fetched in one go rather than one room at a time.
	eventIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Membership == matrix.MembershipLeave || row.Membership == matrix.MembershipBan {
			eventIDs = append(eventIDs, row.EventID)
		}
	}
	events, err := s.db.EventRepo.GetByIDs(eventIDs)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	roomIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		roomIDs = append(roomIDs, row.RoomID)
	}
	activity, err := s.db.EventRepo.LatestPosForRooms(roomIDs)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	bumps, err := s.db.EventRepo.LatestBumpPos(roomIDs)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}

	out := make([]slidingRoomEntry, 0, len(rows))
	for _, row := range rows {
		entry := slidingRoomEntry{
			roomID:        row.RoomID,
			membership:    row.Membership,
			everJoined:    row.WasJoined,
			activity:      activity[row.RoomID],
			bump:          bumps[row.RoomID],
			membershipPos: activity[row.RoomID],
		}
		if event, ok := events[row.EventID]; ok {
			entry.forced = event.Sender != "" && event.Sender != userID
			if event.StreamPos > entry.membershipPos {
				entry.membershipPos = event.StreamPos
			}
		}
		switch row.Membership {
		case matrix.MembershipLeave, matrix.MembershipBan:
			if !seen[row.RoomID] && !(entry.forced && entry.everJoined) {
				continue
			}
		}
		// A room the user is not in cannot move on for them: their list places it by the
		// membership that ended, not by what its remaining members are doing.
		if entry.membership != matrix.MembershipJoin && entry.membershipPos > entry.activity {
			entry.activity = entry.membershipPos
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].activity != out[j].activity {
			return out[i].activity > out[j].activity
		}
		return out[i].roomID < out[j].roomID
	})
	return out, nil
}

// slidingFacts holds what the filters ask about a room. Each block is fetched only if some
// list asks the question, and fetched for every candidate room at once rather than room by
// room: a filter runs over the whole list, because the count a list reports is the number of
// rooms matching the filter and not the number the range asked for.
type slidingFacts struct {
	direct    map[string]bool
	encrypted map[string]bool
	create    map[string]string
	tags      map[string][]string
	children  map[string]bool
}

// slidingGatherFacts reads what the request's filters need to know.
func (s *Service) slidingGatherFacts(ctx context.Context, userID string, rooms []slidingRoomEntry, lists map[string]matrix.SlidingSyncList) (*slidingFacts, error) {
	roomIDs := make([]string, 0, len(rooms))
	for _, entry := range rooms {
		roomIDs = append(roomIDs, entry.roomID)
	}
	facts := &slidingFacts{}
	var spaces []string
	for _, list := range lists {
		filter := list.Filters
		if filter == nil {
			continue
		}
		if filter.IsDM != nil && facts.direct == nil {
			facts.direct = s.directRoomSet(ctx, userID)
		}
		if filter.IsEncrypted != nil && facts.encrypted == nil {
			rows, err := s.db.StateRepo.ForRooms(roomIDs, matrix.TypeEncryption, "")
			if err != nil {
				return nil, matrix.ErrInternal(err)
			}
			facts.encrypted = map[string]bool{}
			for roomID := range rows {
				facts.encrypted[roomID] = true
			}
		}
		if (len(filter.RoomTypes) > 0 || len(filter.NotRoomTypes) > 0) && facts.create == nil {
			rows, err := s.db.StateRepo.ForRooms(roomIDs, matrix.TypeCreate, "")
			if err != nil {
				return nil, matrix.ErrInternal(err)
			}
			facts.create = map[string]string{}
			for roomID, row := range rows {
				content := matrix.MustParseObject([]byte(row.Content))
				if typ, ok := content["type"].(string); ok {
					facts.create[roomID] = typ
				}
			}
		}
		if (len(filter.Tags) > 0 || len(filter.NotTags) > 0) && facts.tags == nil {
			contents, err := s.db.AccountDataRepo.ContentsForRooms(userID, roomIDs, matrix.TypeTag)
			if err != nil {
				return nil, matrix.ErrInternal(err)
			}
			facts.tags = map[string][]string{}
			for roomID, content := range contents {
				body := matrix.MustParseObject([]byte(content))
				tagged, ok := body["tags"].(map[string]any)
				if !ok {
					continue
				}
				names := make([]string, 0, len(tagged))
				for name := range tagged {
					names = append(names, name)
				}
				facts.tags[roomID] = names
			}
		}
		spaces = append(spaces, filter.Spaces...)
	}
	if len(spaces) > 0 {
		// Only the spaces the user has joined are looked at, and only for the rooms
		// directly hung under them: the MSC forbids walking into subspaces.
		joined := map[string]bool{}
		for _, space := range spaces {
			row, err := s.db.MembershipRepo.Get(space, userID)
			if err != nil || row.Membership != matrix.MembershipJoin {
				continue
			}
			joined[space] = true
		}
		names := make([]string, 0, len(joined))
		for space := range joined {
			names = append(names, space)
		}
		children, err := s.db.StateRepo.SpaceChildren(names)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		facts.children = children
	}
	return facts, nil
}

// slidingRoomMatches asks one room the questions one list's filter asks.
func slidingRoomMatches(entry slidingRoomEntry, filter *matrix.SlidingRoomFilter, facts *slidingFacts) bool {
	if filter == nil {
		return true
	}
	if filter.IsDM != nil && facts.direct[entry.roomID] != *filter.IsDM {
		return false
	}
	if filter.IsInvited != nil && (entry.membership == matrix.MembershipInvite) != *filter.IsInvited {
		return false
	}
	if filter.IsEncrypted != nil && facts.encrypted[entry.roomID] != *filter.IsEncrypted {
		return false
	}
	if len(filter.RoomTypes) > 0 || len(filter.NotRoomTypes) > 0 {
		typ := facts.create[entry.roomID]
		if inTypeList(filter.NotRoomTypes, typ) {
			return false
		}
		if len(filter.RoomTypes) > 0 && !inTypeList(filter.RoomTypes, typ) {
			return false
		}
	}
	tags := facts.tags[entry.roomID]
	if len(filter.Tags) > 0 && !anyTag(tags, filter.Tags) {
		return false
	}
	if anyTag(tags, filter.NotTags) {
		return false
	}
	if len(filter.Spaces) > 0 && !facts.children[entry.roomID] {
		return false
	}
	return true
}

// inTypeList answers whether a room type is named by a filter list. An empty entry in the
// list - the null the MSC spells for "a room with no type" - matches a room that has none.
func inTypeList(list []*string, typ string) bool {
	for _, entry := range list {
		if entry == nil {
			if typ == "" {
				return true
			}
			continue
		}
		if *entry == typ {
			return true
		}
	}
	return false
}

// anyTag reports whether a room carries any of the named tags.
func anyTag(tags, wanted []string) bool {
	for _, tag := range tags {
		for _, want := range wanted {
			if tag == want {
				return true
			}
		}
	}
	return false
}

// slidingRoomSet is the set of rooms a connection has been told about, which is what decides
// whether a room the user left still belongs in their list.
func slidingKnownRooms(rows []*persist.SlidingSyncRoom) map[string]bool {
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.Sent {
			seen[row.RoomID] = true
		}
	}
	return seen
}
