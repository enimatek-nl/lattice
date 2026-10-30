package service

import (
	"context"
	"strconv"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
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

// RoomHierarchy walks the m.space.child edges of a room breadth first and
// returns a page of the rooms it finds.
func (s *Service) RoomHierarchy(ctx context.Context, params HierarchyParams) (*HierarchyResult, error) {
	room, err := s.MustRoom(ctx, params.RoomID)
	if err != nil {
		return nil, err
	}
	if err := s.RequireReadable(ctx, room.ID, params.UserID); err != nil {
		return nil, err
	}
	limit := params.Limit
	if limit <= 0 || limit > hierarchyLimitRooms {
		limit = hierarchyDefaultRooms
	}
	maxDepth := params.MaxDepth
	if maxDepth <= 0 {
		maxDepth = hierarchyDefaultDepth
	}
	if maxDepth > hierarchyMaxDepth {
		maxDepth = hierarchyMaxDepth
	}
	if params.MinDepth < 0 {
		return nil, matrix.ErrInvalidParam("min_depth must not be negative")
	}
	if params.MinDepth > maxDepth {
		return nil, matrix.ErrInvalidParam("min_depth must not exceed max_depth")
	}
	offset := 0
	if params.From != "" {
		parsed, err := strconv.Atoi(params.From)
		if err != nil || parsed < 0 {
			return nil, matrix.ErrInvalidParam("invalid from token %q", params.From)
		}
		offset = parsed
	}

	rooms, incomplete, err := s.walkHierarchy(ctx, room.ID, params.UserID, maxDepth, params.MinDepth)
	if err != nil {
		return nil, err
	}
	result := &HierarchyResult{
		Rooms:           []HierarchyRoom{},
		IncompleteChain: incomplete,
	}
	if offset >= len(rooms) {
		return result, nil
	}
	end := min(offset+limit, len(rooms))
	result.Rooms = rooms[offset:end]
	if end < len(rooms) {
		result.NextBatch = strconv.Itoa(end)
	}
	return result, nil
}

// walkHierarchy lists the reachable rooms breadth first, starting at the room
// itself. Children that are remote, unknown or unreadable are left out and make
// the chain incomplete.
func (s *Service) walkHierarchy(ctx context.Context, rootID, userID string, maxDepth, minDepth int) ([]HierarchyRoom, bool, error) {
	type node struct {
		roomID string
		depth  int
	}
	visited := map[string]bool{rootID: true}
	queue := []node{{roomID: rootID, depth: 0}}
	out := make([]HierarchyRoom, 0, 8)
	incomplete := false

	for len(queue) > 0 && len(out) < hierarchyHardCap {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= minDepth {
			entry, err := s.hierarchyEntry(ctx, current.roomID, userID)
			if err != nil {
				return nil, false, err
			}
			out = append(out, entry)
		}
		if current.depth >= maxDepth {
			continue
		}
		children, err := s.spaceChildren(ctx, current.roomID)
		if err != nil {
			return nil, false, err
		}
		for _, childID := range children {
			if visited[childID] {
				continue
			}
			visited[childID] = true
			if !s.LocalRoom(childID) {
				// Remote spaces cannot be resolved without federation, which
				// makes the chain incomplete rather than wrong.
				incomplete = true
				continue
			}
			if _, err := s.db.RoomRepo.Get(childID); err != nil {
				if persist.IsNotFound(err) {
					incomplete = true
					continue
				}
				return nil, false, matrix.ErrInternal(err)
			}
			if !s.canRead(ctx, childID, userID) {
				incomplete = true
				continue
			}
			queue = append(queue, node{roomID: childID, depth: current.depth + 1})
		}
	}
	return out, incomplete, nil
}

// spaceChildren returns the child room ids of a room, taken from its
// m.space.child state events. A child whose content is an empty object was
// removed from the space.
func (s *Service) spaceChildren(ctx context.Context, roomID string) ([]string, error) {
	rows, err := s.db.StateRepo.List(roomID, []string{matrix.TypeSpaceChild}, "")
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		content := matrix.MustParseObject([]byte(row.Content))
		if len(content) == 0 {
			continue
		}
		ids = append(ids, row.StateKey)
	}
	return ids, nil
}

// hierarchyEntry renders one room of the hierarchy.
func (s *Service) hierarchyEntry(ctx context.Context, roomID, userID string) (HierarchyRoom, error) {
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
	events := make([]matrix.Event, 0, len(rows))
	for _, row := range rows {
		event, err := s.db.EventRepo.Get(row.EventID)
		if err != nil {
			continue
		}
		events = append(events, event.Matrix())
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
