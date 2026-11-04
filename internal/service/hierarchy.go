package service

import (
	"context"
	"log"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// Hierarchy bounds: a page holds at most hierarchyLimitRooms rooms, a walk never lists
// more than hierarchyHardCap of them, and it never digs deeper than hierarchyMaxDepth.
//
// The specification names no default depth for this endpoint, and the two readings that
// follow from that differ a lot: one level, or the whole space. A whole space is what is
// walked when the caller does not say, because a client drawing a space out of one level of
// it draws a box with no children in it, and every client that draws spaces asks without
// saying. hierarchyMaxDepth is not a policy about what a caller deserves; it is what stops a
// space that links itself - which any pair of rooms can be made to do - from spinning here
// and in the requests this walk sends.
const (
	hierarchyDefaultRooms = 50
	hierarchyLimitRooms   = 100
	hierarchyMaxDepth     = 12
	hierarchyHardCap      = 1000
	// hierarchyUnbounded is what the API layers pass when the caller did not name a depth
	// window, as distinct from the caller naming zero - which is a real answer, meaning the
	// room itself and nothing below it.
	HierarchyUnbounded = -1
)

// HierarchyParams captures the query parameters of
// GET /_matrix/client/v1/rooms/{roomId}/hierarchy.
type HierarchyParams struct {
	RoomID string
	UserID string
	Limit  int
	// MaxDepth and MinDepth bound the distance from the requested room; the room itself
	// sits at depth 0. MaxDepth of HierarchyUnbounded means the caller named no window and
	// the space is walked to the bound the walk keeps for itself.
	MaxDepth int
	MinDepth int
	From     string
	// SuggestedOnly keeps only the branches a space marks as worth showing.
	SuggestedOnly bool
}

// HierarchyRoom is one room of a space hierarchy.
type HierarchyRoom struct {
	RoomID           string `json:"room_id"`
	Name             string `json:"name,omitempty"`
	Topic            string `json:"topic,omitempty"`
	AvatarURL        string `json:"avatar_url,omitempty"`
	CanonicalAlias   string `json:"canonical_alias,omitempty"`
	JoinRules        string `json:"join_rule"`
	RoomType         string `json:"room_type,omitempty"`
	NumJoinedMembers int64  `json:"num_joined_members"`
	WorldReadable    bool   `json:"world_readable"`
	GuestsCanJoin    bool   `json:"guest_can_join"`
	// ChildrenState are the m.space.child links this entry carries so that whoever reads it knows
	// where the tree continues. It is a pointer so that the two answers can differ: a client is
	// always shown the key, empty for a room that is not a space, while a child described to a
	// peer omits it - the links of that room are asked about with the room, not carried along.
	ChildrenState *[]matrix.Event `json:"children_state,omitempty"`
	// AllowedRooms are the rooms whose membership admits a joiner, for a restricted room. The
	// client endpoint does not need them - this server has already decided for its own user - but
	// the federation answer carries them, because the peer answering for a room it hosts cannot
	// know which of the asking server's users the rule admits.
	AllowedRooms []string `json:"allowed_room_ids,omitempty"`
}

// HierarchyResult is a page of the room hierarchy.
type HierarchyResult struct {
	Rooms []HierarchyRoom `json:"rooms"`
	// NextBatch continues the walk where this page stopped.
	NextBatch string `json:"next_batch,omitempty"`
	// IncompleteChain reports that the walk could not resolve every child, so
	// the tree below this page is only part of the truth.
	IncompleteChain bool `json:"incomplete_chain,omitempty"`
}

// hierarchyStateTypes are the events the specification asks a hierarchy entry
// to carry in children_state: the m.space.child links of that room, which tell
// a client where the tree continues.
var hierarchyStateTypes = []string{matrix.TypeSpaceChild}

// RoomHierarchy walks the m.space.child edges of a room depth first and returns a page of
// the rooms it finds. The room itself is at depth 0, and the page is bounded by the caller's
// limit, depth window and token - all of which [Service.hierarchyPage] shares with the
// federation answer, so a peer and a client see the same tree shaped the same way.
//
// Depth first is not a detail: the caller asks for a page at a time, and a page of a
// breadth-first walk holds the top of the space and none of its branches, so the first two
// pages of a wide space are its two shallowest corridors and the room a client is looking for
// is on page seven.
func (s *Service) RoomHierarchy(ctx context.Context, params HierarchyParams) (*HierarchyResult, error) {
	room, err := s.MustRoom(ctx, params.RoomID)
	if err != nil {
		return nil, err
	}
	if err := s.RequireReadable(ctx, room.ID, params.UserID); err != nil {
		return nil, err
	}
	return s.hierarchyPage(ctx, room.ID, reader{userID: params.UserID}, params)
}

// walkWindow is how far down a hierarchy walk goes and which of its branches it takes.
type walkWindow struct {
	maxDepth int
	minDepth int
	// suggestedOnly keeps the branches a space marks as worth showing - the `suggested`
	// flag on an m.space.child link - and drops the rest, links included, so that a client
	// asking for the short list is not shown the long one with a note.
	suggestedOnly bool
}

// walkHierarchy lists the reachable rooms depth first, starting at the room itself.
//
// A child this server does not hold is asked about rather than dropped: the link that names
// it also names the servers that can serve it, and the tree below a remote space belongs to
// that server to describe. The depth window is translated before the question is asked, so
// the page that comes back is already cut to the window this walk is in. What cannot be
// fetched - a server that is down, a link that names nobody reachable, a walk that has spent
// its budget of questions - leaves the chain incomplete, which the answer says out loud
// instead of quietly presenting a smaller space than the one that exists.
func (s *Service) walkHierarchy(ctx context.Context, rootID string, r reader, w walkWindow) ([]HierarchyRoom, bool, error) {
	type node struct {
		roomID string
		depth  int
	}
	// Two sets, because they answer two questions. `visited` is about the walk: this room
	// has been queued or asked about, so a second edge pointing at it costs nothing.
	// `described` is about the answer: this room has been put in the page. A remote child is
	// in the first set before its own description arrives - that is what stops a second
	// question about it - and folding the two together is how a room with two parents ends up
	// listed nowhere at all.
	visited := map[string]bool{rootID: true}
	described := map[string]bool{rootID: true}
	stack := []node{{roomID: rootID, depth: 0}}
	out := make([]HierarchyRoom, 0, 8)
	incomplete := false
	asked := 0

	for len(stack) > 0 && len(out) < hierarchyHardCap {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.depth >= w.minDepth {
			entry, err := s.hierarchyEntry(ctx, current.roomID, w)
			if err != nil {
				return nil, false, err
			}
			out = append(out, entry)
		}
		if current.depth >= w.maxDepth {
			continue
		}
		children, err := s.spaceLinks(ctx, current.roomID)
		if err != nil {
			return nil, false, err
		}
		// Last in, first out: the links are pushed back to front so the first child a space
		// names is the first one walked, which is the order the space was written in.
		for i := len(children) - 1; i >= 0; i-- {
			link := children[i]
			if w.suggestedOnly && !link.suggested {
				continue
			}
			if visited[link.roomID] {
				continue
			}
			visited[link.roomID] = true
			// A room this server holds is described from its own copy, whichever server the
			// room id says owns it: local knowledge beats a peer's description, and the spec
			// says the same. A room it does not hold is asked about from the servers the link
			// names, one room per question.
			here, err := s.roomExistsLocal(ctx, link.roomID)
			if err != nil {
				return nil, false, err
			}
			if !here {
				if asked >= remoteHierarchyAsks {
					// The walk has asked enough. The rest of the space exists; this
					// request is not going to describe it, and the answer says so.
					incomplete = true
					continue
				}
				asked++
				answer, err := s.RemoteHierarchy(ctx, link.roomID, link.via, w.suggestedOnly)
				if err != nil {
					log.Printf("federation: the space below %s cannot be read: %v", link.roomID, err)
					incomplete = true
					continue
				}
				// A peer that names a child as one nobody can reach has answered the question
				// once for everybody: the room is not asked about again.
				for _, room := range answer.InaccessibleChildren {
					visited[room] = true
				}
				if answer.Room.RoomID == "" || described[answer.Room.RoomID] ||
					!s.remoteRoomShows(ctx, answer.Room, r) {
					continue
				}
				described[answer.Room.RoomID] = true
				out = append(out, answer.Room)
				// The child links the answer carries say where the tree goes next. They are
				// queued rather than folded in here, so each room below is asked of the server
				// that actually holds it.
				if current.depth+1 < w.maxDepth {
					for _, child := range entrySpaceLinks(answer.Room) {
						if visited[child.roomID] {
							continue
						}
						visited[child.roomID] = true
						stack = append(stack, node{roomID: child.roomID, depth: current.depth + 2})
					}
				}
				continue
			}
			if !s.hierarchyShows(ctx, link.roomID, r) {
				incomplete = true
				continue
			}
			if !s.hierarchyShows(ctx, link.roomID, r) {
				incomplete = true
				continue
			}
			stack = append(stack, node{roomID: link.roomID, depth: current.depth + 1})
		}
	}
	return out, incomplete, nil
}

// hierarchyShows decides whether a room of a space goes into the tree for a reader.
//
// The endpoint lists rooms the reader has joined *or could join*. Reading is not the test: a
// public room that keeps its history to its members is unreadable to an outsider and perfectly
// joinable, and a space that hides it cannot be joined out of the summary, which is what the
// summary is for. What stays out is a room the reader cannot get into: an invite-only room they
// were not invited to, a restricted room whose allow list names no room they belong to, or a
// sub-space that is itself closed, whose whole branch then stays closed.
//
// A server asking over federation is answered by [Service.readableBy] instead: it is not asking
// for one person, and what it is given has to be filtered by the person it asked for.
func (s *Service) hierarchyShows(ctx context.Context, roomID string, r reader) bool {
	if r.userID == "" {
		return s.readableBy(ctx, roomID, r)
	}
	if s.canRead(ctx, roomID, r.userID) {
		return true
	}
	return s.joinableBy(ctx, roomID, r.userID)
}

// remoteRoomShows decides whether a room described by another server goes into the tree for the
// user this walk is answering. The peer answered for servers, not people: it was told nobody was
// asking, and it included the room because *some* user of this server could get in. Deciding for
// the one person who asked is this server's job, and the fields the peer sent - the join rule,
// whether the room is world readable, and the rooms its allow list names - are what makes that
// decision possible. Where this server holds one of those rooms it knows the answer; where it does
// not, it cannot confirm the condition, and a room whose condition cannot be confirmed is a room
// this user cannot be shown.
func (s *Service) remoteRoomShows(ctx context.Context, entry HierarchyRoom, r reader) bool {
	if r.userID == "" {
		return true
	}
	if entry.WorldReadable {
		return true
	}
	switch s.MembershipOf(ctx, entry.RoomID, r.userID) {
	case matrix.MembershipJoin, matrix.MembershipInvite:
		return true
	}
	switch entry.JoinRules {
	case matrix.JoinRulesPublic, matrix.JoinRulesKnock:
		return true
	case matrix.JoinRulesRestricted, matrix.JoinRulesKnockRestricted:
		for _, admitting := range entry.AllowedRooms {
			if s.MembershipOf(ctx, admitting, r.userID) == matrix.MembershipJoin {
				return true
			}
		}
	}
	return false
}

// entrySpaceLinks reads the child links a hierarchy entry carries, in the shape
// [Service.spaceLinks] reads out of local state. A link with an empty body was removed from the
// space and is not an edge.
func entrySpaceLinks(entry HierarchyRoom) []spaceLink {
	events := []matrix.Event{}
	if entry.ChildrenState != nil {
		events = *entry.ChildrenState
	}
	links := make([]spaceLink, 0, len(events))
	for _, event := range events {
		if event.StateKey == nil || event.Type != matrix.TypeSpaceChild {
			continue
		}
		content := matrix.MustParseObject([]byte(event.Content))
		if len(content) == 0 {
			continue
		}
		link := spaceLink{roomID: *event.StateKey, suggested: content["suggested"] == true}
		if listed, ok := content["via"].([]any); ok {
			for _, entry := range listed {
				if server, isText := entry.(string); isText {
					link.via = append(link.via, server)
				}
			}
		}
		links = append(links, link)
	}
	return links
}

// joinableBy reports whether the room's own join rule lets this user in as things stand. A public
// room, or one open to knocking, asks nothing of them. A restricted one asks membership of a room
// on its allow list, and only of a room this server can still read: a copy of an admitting room
// this server has left says nothing about anybody. An invite-only room asks an invite, which
// [Service.canRead] has already refused.
func (s *Service) joinableBy(ctx context.Context, roomID, userID string) bool {
	rule, err := s.db.StateRepo.JoinRule(roomID)
	if err != nil {
		return false
	}
	switch rule {
	case matrix.JoinRulesPublic, matrix.JoinRulesKnock:
		return true
	case matrix.JoinRulesRestricted, matrix.JoinRulesKnockRestricted:
		for _, admitting := range s.roomsThatAdmit(ctx, roomID) {
			if !s.canReadMembershipOf(admitting) {
				continue
			}
			if s.MembershipOf(ctx, admitting, userID) == matrix.MembershipJoin {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// hierarchyEntry renders one room of the hierarchy.
func (s *Service) hierarchyEntry(ctx context.Context, roomID string, w walkWindow) (HierarchyRoom, error) {
	joinRule, err := s.db.StateRepo.JoinRule(roomID)
	if err != nil {
		joinRule = matrix.JoinRulesInvite
	}
	joined, err := s.db.MembershipRepo.Count(roomID, matrix.MembershipJoin)
	if err != nil {
		return HierarchyRoom{}, matrix.ErrInternal(err)
	}
	entry := HierarchyRoom{
		RoomID:           roomID,
		Name:             s.roomName(ctx, roomID),
		Topic:            s.roomTopic(ctx, roomID),
		AvatarURL:        s.roomAvatarURL(ctx, roomID),
		CanonicalAlias:   s.roomAlias(ctx, roomID),
		JoinRules:        joinRule,
		RoomType:         s.roomType(ctx, roomID),
		NumJoinedMembers: joined,
		WorldReadable:    s.HistoryVisibility(ctx, roomID) == matrix.HistoryWorldReadable,
		GuestsCanJoin:    s.guestsCanConnect(ctx, roomID),
	}
	events := s.hierarchyStateEvents(ctx, roomID, w)
	entry.ChildrenState = &events
	return entry, nil
}

// hierarchyStateEvents returns the state events a hierarchy entry carries so that whoever
// reads the entry knows where the tree continues.
//
// A link with an empty body is not shown. That is what a removed child looks like - the room
// stays in the space's state with its content gone, because state events are not deleted - and
// showing it would tell a client to walk into a branch the space took away, and offer the same
// room twice: once as a child of this space, once as a child of whatever still lists it.
func (s *Service) hierarchyStateEvents(ctx context.Context, roomID string, w walkWindow) []matrix.Event {
	rows, err := s.db.StateRepo.List(roomID, hierarchyStateTypes, "")
	if err != nil {
		return []matrix.Event{}
	}
	kept := make([]*persist.RoomState, 0, len(rows))
	for _, row := range rows {
		content := matrix.MustParseObject([]byte(row.Content))
		if len(content) == 0 {
			continue
		}
		if w.suggestedOnly && content["suggested"] != true {
			continue
		}
		kept = append(kept, row)
	}
	events, err := s.stateEventsWire(kept, nil)
	if err != nil {
		return []matrix.Event{}
	}
	return events
}

// roomType returns the room type recorded in the m.room.create event, which is
// "m.space" for spaces.
func (s *Service) roomType(ctx context.Context, roomID string) string {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeCreate, "")
	if err != nil {
		return ""
	}
	var content matrix.CreateContent
	if err := matrix.DecodeContent(raw, &content); err != nil {
		return ""
	}
	return content.Type
}
