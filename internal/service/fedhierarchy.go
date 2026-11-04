package service

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"strconv"
	"time"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// A space is a room like any other, so its children are rooms that may live anywhere. A
// hierarchy walk that stops at this server's own doors shows a space with holes in it and
// says nothing about where the rest went, which is worse than showing a tree with a branch
// labelled "ask over there": a client cannot join a room it was never told exists.
//
// Both directions therefore exist. A peer asking for the tree rooted at one of our rooms is
// answered the same way a client is - only the rooms it may read, the `m.space.child` links
// of each so it can continue on its own - and a walk of ours that meets a child we do not
// hold asks the servers the link names. The answer one server gives is a description of its
// own rooms, which is what makes it safe to merge into a tree this server did not build.

const (
	// remoteHierarchyAsks bounds how many times one walk dials out. A space with a
	// thousand remote children would otherwise buy a thousand round trips with one client
	// request, and the rooms past the budget are reported as an incomplete chain rather
	// than invented.
	remoteHierarchyAsks = 50
	// remoteHierarchyBudget is the wall clock one walk spends asking. It is generous
	// because a hierarchy is a read a human is waiting on, and deliberately finite because
	// somebody else's slow server is not a reason to hold a request open forever.
	remoteHierarchyBudget = 15 * time.Second
)

// HierarchyPeerAnswer is what GET /_matrix/federation/v1/hierarchy/{roomId} answers: the one room
// that was asked about, the children of that room this server can describe, and the names of the
// children it will not. It does not recurse: a space below the room belongs to whatever server
// hosts it to describe, and this answer is small enough to be worth caching.
type HierarchyPeerAnswer struct {
	Room     HierarchyRoom   `json:"room"`
	Children []HierarchyRoom `json:"children"`
	// InaccessibleChildren names the children of the requested room that are in its state but are
	// not offered to the asking server. Saying the name is not a leak - the asking server already
	// read the link - it is the difference between "nobody will tell you about this" and "I could
	// not tell you".
	InaccessibleChildren []string `json:"inaccessible_children"`
}

// ServeHierarchy answers GET /_matrix/federation/v1/hierarchy/{roomId} for a peer.
//
// The answer is about servers, not people: this endpoint is not told who is asking, and a peer may
// be told about a room when *any* of its members could get in. The peer is trusted to filter that
// down for the person it asked for, which is why a restricted room travels with the rooms its join
// rules name (`allowed_room_ids`) - without them the peer cannot tell whether its own user is the
// one the rule admits.
//
// A room that cannot be offered is answered as if it were not here, per the specification's 404:
// naming a room this server will not open is a different answer from not knowing it, and the
// specification picks the second.
func (s *Service) ServeHierarchy(ctx context.Context, roomID, peer string, params HierarchyParams) (*HierarchyPeerAnswer, error) {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	acl, err := s.roomServerACL(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	if !federation.ServerAllowed(acl, peer) || !s.offeredTo(ctx, room.ID, peer) {
		return nil, matrix.ErrNotFound("no room %s is known here", roomID)
	}

	// One room, one level: the entry carries its own child links so the asker knows where the
	// tree continues, and the children below them are asked about one at a time.
	w := walkWindow{maxDepth: 1, suggestedOnly: params.SuggestedOnly}
	entry, err := s.hierarchyEntry(ctx, room.ID, w)
	if err != nil {
		return nil, err
	}
	entry.AllowedRooms = s.roomsThatAdmit(ctx, room.ID)
	answer := &HierarchyPeerAnswer{
		Room:                 entry,
		Children:             []HierarchyRoom{},
		InaccessibleChildren: []string{},
	}
	links, err := s.spaceLinks(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		if w.suggestedOnly && !link.suggested {
			continue
		}
		here, err := s.roomExistsLocal(ctx, link.roomID)
		if err != nil {
			return nil, err
		}
		if !here {
			// This server cannot describe it either, and inventing a name for the list of
			// rooms nobody can describe would be a guess.
			continue
		}
		if !s.offeredTo(ctx, link.roomID, peer) {
			answer.InaccessibleChildren = append(answer.InaccessibleChildren, link.roomID)
			continue
		}
		child, err := s.hierarchyEntry(ctx, link.roomID, w)
		if err != nil {
			return nil, err
		}
		child.ChildrenState = nil
		child.AllowedRooms = s.roomsThatAdmit(ctx, link.roomID)
		answer.Children = append(answer.Children, child)
	}
	return answer, nil
}

// offeredTo reports whether a room may be described to a particular server. World readable is an
// open door; a server with a member in the room can read it itself; a public room or one open to
// knocking is one that member could walk into. A restricted room is the case MSC3083 added: it is
// open to that server's users when one of them belongs to a room on its allow list, and only as far
// as this server can read that other room - a copy of a room this server has left is not knowledge.
func (s *Service) offeredTo(ctx context.Context, roomID, peer string) bool {
	if s.HistoryVisibility(ctx, roomID) == matrix.HistoryWorldReadable {
		return true
	}
	if in, err := s.roomSharesServer(ctx, roomID, peer); err == nil && in {
		return true
	}
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
			members, err := s.db.MembershipRepo.Members(admitting, matrix.MembershipJoin)
			if err != nil {
				continue
			}
			for _, member := range members {
				if matrix.DomainOf(member.UserID) == peer {
					return true
				}
			}
		}
	}
	return false
}

// RemoteHierarchy asks the servers that can serve one remote room for its summary:
// GET /_matrix/federation/v1/hierarchy/{roomId}. The answer is one room and its immediate
// children; the tree below a child is asked about from wherever that child lives.
//
// The specification names one query parameter, `suggested_only`, and no pagination: the answer is
// meant to be small enough to cache, and a walk that wants more asks again.
func (s *Service) RemoteHierarchy(ctx context.Context, roomID string, via []string, suggestedOnly bool) (*HierarchyPeerAnswer, error) {
	transport := s.federationTransport()
	if transport == nil {
		return nil, matrix.ErrNotFound("this server does not federate, so the space below %s cannot be read", roomID)
	}
	ctx, cancel := context.WithTimeout(ctx, remoteHierarchyBudget)
	defer cancel()
	servers := hierarchyServers(roomID, via)
	query := url.Values{}
	if suggestedOnly {
		query.Set("suggested_only", "true")
	}
	var lastFailure string
	for _, server := range servers {
		body, err := transport.Get(ctx, server, "/_matrix/federation/v1/hierarchy/"+url.PathEscape(roomID), query)
		if err != nil {
			lastFailure = err.Error()
			continue
		}
		answer := &HierarchyPeerAnswer{}
		if err := json.Unmarshal([]byte(matrix.Encode(body)), answer); err != nil {
			lastFailure = "the answer is not a hierarchy: " + err.Error()
			continue
		}
		if answer.Room.RoomID != roomID {
			// The walk is about one room. A peer that answers with a different one is
			// describing a different space, and putting its rooms in this tree would be
			// this server's bug rather than that peer's.
			log.Printf("federation: %s answered the hierarchy of %s with %s", server, roomID, answer.Room.RoomID)
			lastFailure = server + " answered a room that is not " + roomID
			continue
		}
		return answer, nil
	}
	if lastFailure == "" {
		lastFailure = "no server was asked"
	}
	return nil, matrix.ErrNotFound("the space below %s could not be read: %s", roomID, lastFailure)
}

// hierarchyServers are the servers to ask about a remote room, in the order they are worth
// asking in: the ones the `m.space.child` link names, then the server the room id itself
// carries. A link that names nobody is still an address, and the room id says whose it is.
func hierarchyServers(roomID string, via []string) []string {
	seen := map[string]bool{}
	servers := make([]string, 0, len(via)+1)
	for _, server := range via {
		if server == "" || seen[server] || !matrix.ValidServerName(server) {
			continue
		}
		seen[server] = true
		servers = append(servers, server)
	}
	if domain := matrix.DomainOf(roomID); domain != "" && !seen[domain] && matrix.ValidServerName(domain) {
		servers = append(servers, domain)
	}
	return servers
}

// hierarchyPage is one page of a walk: the rooms between the walk's start and the page
// limit, with the token that continues it. Both the client endpoint and the federation one
// answer with this, which is why the same walk serves both.
func (s *Service) hierarchyPage(ctx context.Context, rootID string, r reader, params HierarchyParams) (*HierarchyResult, error) {
	limit := params.Limit
	if limit <= 0 || limit > hierarchyLimitRooms {
		limit = hierarchyDefaultRooms
	}
	w := walkWindow{minDepth: params.MinDepth, suggestedOnly: params.SuggestedOnly}
	maxDepth := params.MaxDepth
	if maxDepth == HierarchyUnbounded {
		// The caller named no window, so the space is walked. The ceiling that still
		// applies is the one this server keeps against a graph that links itself, not a
		// decision about how much of somebody's space they get to see.
		maxDepth = hierarchyMaxDepth
	}
	if maxDepth > hierarchyMaxDepth {
		maxDepth = hierarchyMaxDepth
	}
	if w.minDepth < 0 {
		return nil, matrix.ErrInvalidParam("min_depth must not be negative")
	}
	if w.minDepth > maxDepth {
		return nil, matrix.ErrInvalidParam("min_depth must not exceed max_depth")
	}
	w.maxDepth = maxDepth
	offset := 0
	if params.From != "" {
		parsed, err := strconv.Atoi(params.From)
		if err != nil || parsed < 0 {
			return nil, matrix.ErrInvalidParam("invalid from token %q", params.From)
		}
		offset = parsed
	}

	rooms, incomplete, err := s.walkHierarchy(ctx, rootID, r, w)
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

// reader is whoever a hierarchy is being walked for: a user of this server, or a server
// that asked. They are different in one way that matters - a user may read a room this
// server has been told about, a peer may read a room of its own or one it is part of - and
// identical in every other way, which is why there is one walk rather than two.
type reader struct {
	userID string
	server string
}

// readableBy is the visibility rule for a reader of either kind.
func (s *Service) readableBy(ctx context.Context, roomID string, r reader) bool {
	if r.userID != "" {
		return s.canRead(ctx, roomID, r.userID)
	}
	if r.server == "" {
		return false
	}
	// A world readable room is described to anybody, which is what a public space is for.
	// Anything else is described only to a server that is in it: it can read those rooms
	// itself, and the tree it builds out of them is its own business.
	if s.HistoryVisibility(ctx, roomID) == matrix.HistoryWorldReadable {
		return true
	}
	in, err := s.roomSharesServer(ctx, roomID, r.server)
	if err != nil {
		log.Printf("federation: whether %s may read %s is unknown: %v", r.server, roomID, err)
		return false
	}
	return in
}

// spaceLink is one `m.space.child` edge: the room it points at, and the servers the link
// names as able to serve it. The second half is what makes a remote child reachable - a
// room id says which server a room belongs to, but a room can be served by several, and the
// one that made the link knows which it could reach.
type spaceLink struct {
	roomID string
	via    []string
	// suggested is the space saying this branch is worth showing on a short list. A space
	// full of rooms that are merely filed is a space whose important children get lost.
	suggested bool
}

// spaceLinks returns the child edges of a room. A child whose content is an empty object
// was removed from the space and is not an edge at all.
func (s *Service) spaceLinks(ctx context.Context, roomID string) ([]spaceLink, error) {
	rows, err := s.db.StateRepo.List(roomID, []string{matrix.TypeSpaceChild}, "")
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	links := make([]spaceLink, 0, len(rows))
	for _, row := range rows {
		content := matrix.MustParseObject([]byte(row.Content))
		if len(content) == 0 {
			continue
		}
		link := spaceLink{roomID: row.StateKey, suggested: content["suggested"] == true}
		if listed, ok := content["via"].([]any); ok {
			for _, entry := range listed {
				if server, isText := entry.(string); isText {
					link.via = append(link.via, server)
				}
			}
		}
		links = append(links, link)
	}
	return links, nil
}

// roomExistsLocal reports whether this server holds this room.
func (s *Service) roomExistsLocal(ctx context.Context, roomID string) (bool, error) {
	if _, err := s.db.RoomRepo.Get(roomID); err != nil {
		if persist.IsNotFound(err) {
			return false, nil
		}
		return false, matrix.ErrInternal(err)
	}
	return true, nil
}
