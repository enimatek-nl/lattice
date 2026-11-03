package service

import (
	"context"
	"log"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// Hierarchy defaults: a page holds at most hierarchyLimitRooms rooms and the
// walk never digs deeper than hierarchyMaxDepth.
const (
	hierarchyDefaultRooms = 50
	hierarchyLimitRooms   = 100
	hierarchyDefaultDepth = 1
	hierarchyMaxDepth     = 12
	hierarchyHardCap      = 1000
)

// HierarchyParams captures the query parameters of
// GET /_matrix/client/v1/rooms/{roomId}/hierarchy.
type HierarchyParams struct {
	RoomID string
	UserID string
	Limit  int
	// MaxDepth and MinDepth bound the distance from the requested room; the
	// room itself sits at depth 0.
	MaxDepth int
	MinDepth int
	From     string
}

// HierarchyRoom is one room of a space hierarchy.
type HierarchyRoom struct {
	RoomID           string         `json:"room_id"`
	Name             string         `json:"name,omitempty"`
	Topic            string         `json:"topic,omitempty"`
	AvatarURL        string         `json:"avatar_url,omitempty"`
	CanonicalAlias   string         `json:"canonical_alias,omitempty"`
	JoinRules        string         `json:"join_rules"`
	RoomType         string         `json:"room_type,omitempty"`
	NumJoinedMembers int64          `json:"num_joined_members"`
	WorldReadable    bool           `json:"world_readable"`
	GuestsCanJoin    bool           `json:"guests_can_join"`
	ChildrenState    []matrix.Event `json:"children_state"`
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

// RoomHierarchy walks the m.space.child edges of a room breadth first and returns a page
// of the rooms it finds. The room itself is at depth 0, and the page is bounded by the
// caller's limit, depth window and token - all of which [Service.hierarchyPage] shares with
// the federation answer, so a peer and a client see the same tree shaped the same way.
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

// walkHierarchy lists the reachable rooms breadth first, starting at the room itself.
//
// A child this server does not hold is asked about rather than dropped: the link that names
// it also names the servers that can serve it, and the tree below a remote space belongs to
// that server to describe. The depth window is translated before the question is asked, so
// the page that comes back is already cut to the window this walk is in. What cannot be
// fetched - a server that is down, a link that names nobody reachable, a walk that has spent
// its budget of questions - leaves the chain incomplete, which the answer says out loud
// instead of quietly presenting a smaller space than the one that exists.
func (s *Service) walkHierarchy(ctx context.Context, rootID string, r reader, maxDepth, minDepth int) ([]HierarchyRoom, bool, error) {
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
	queue := []node{{roomID: rootID, depth: 0}}
	out := make([]HierarchyRoom, 0, 8)
	incomplete := false
	asked := 0

	for len(queue) > 0 && len(out) < hierarchyHardCap {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= minDepth {
			entry, err := s.hierarchyEntry(ctx, current.roomID)
			if err != nil {
				return nil, false, err
			}
			out = append(out, entry)
		}
		if current.depth >= maxDepth {
			continue
		}
		children, err := s.spaceLinks(ctx, current.roomID)
		if err != nil {
			return nil, false, err
		}
		for _, link := range children {
			if visited[link.roomID] {
				continue
			}
			visited[link.roomID] = true
			if !s.LocalRoom(link.roomID) {
				if asked >= remoteHierarchyAsks {
					// The walk has asked enough. The rest of the space exists; this
					// request is not going to describe it, and the answer says so.
					incomplete = true
					continue
				}
				asked++
				remote, err := s.RemoteHierarchy(ctx, link.roomID, link.via, hierarchyDefaultRooms,
					maxDepth-current.depth-1, minDepth-current.depth-1)
				if err != nil {
					log.Printf("federation: the space below %s cannot be read: %v", link.roomID, err)
					incomplete = true
					continue
				}
				if remote.IncompleteChain {
					incomplete = true
				}
				// The page starts at the child itself and already holds everything below it
				// within the window, so its rooms are added without being queued: asking this
				// server's way down a tree another server already walked would double every
				// request and could only find less.
				for _, entry := range remote.Rooms {
					if entry.RoomID == "" || described[entry.RoomID] {
						continue
					}
					visited[entry.RoomID] = true
					described[entry.RoomID] = true
					out = append(out, entry)
				}
				continue
			}
			here, err := s.roomExistsLocal(ctx, link.roomID)
			if err != nil {
				return nil, false, err
			}
			if !here {
				incomplete = true
				continue
			}
			if !s.readableBy(ctx, link.roomID, r) {
				incomplete = true
				continue
			}
			queue = append(queue, node{roomID: link.roomID, depth: current.depth + 1})
		}
	}
	return out, incomplete, nil
}

// hierarchyEntry renders one room of the hierarchy.
func (s *Service) hierarchyEntry(ctx context.Context, roomID string) (HierarchyRoom, error) {
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
		ChildrenState:    s.hierarchyStateEvents(ctx, roomID),
	}
	return entry, nil
}

// hierarchyStateEvents returns the stripped state events the specification
// asks a hierarchy entry to describe a room with.
func (s *Service) hierarchyStateEvents(ctx context.Context, roomID string) []matrix.Event {
	rows, err := s.db.StateRepo.List(roomID, hierarchyStateTypes, "")
	if err != nil {
		return []matrix.Event{}
	}
	events, err := s.stateEventsWire(rows, nil)
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
