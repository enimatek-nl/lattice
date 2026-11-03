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

// ServeHierarchy answers GET /_matrix/federation/v1/hierarchy/{roomId} for a peer: the
// tree below this room as far as this server can describe it, and only the rooms the
// asking server may read.
//
// A room that is not world readable is not described to a server that is not in it: a space
// is a room, and the rule that governs reading a room governs reading its shape. What a
// refused branch of the tree costs the asker is said plainly in `incomplete_chain`, because
// "there is more, and you cannot see it" is a different answer from "there is nothing
// there".
func (s *Service) ServeHierarchy(ctx context.Context, roomID, peer string, params HierarchyParams) (*HierarchyResult, error) {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	acl, err := s.roomServerACL(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	if !federation.ServerAllowed(acl, peer) {
		return nil, matrix.ErrForbidden("this room excludes %s", peer)
	}
	r := reader{server: peer}
	if !s.readableBy(ctx, room.ID, r) {
		return nil, matrix.ErrForbidden("%s is not part of %s, and the room is not open to be read", peer, room.ID)
	}
	result, err := s.hierarchyPage(ctx, room.ID, r, params)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RemoteHierarchy asks the servers that can serve one remote room for the tree below it:
// GET /_matrix/federation/v1/hierarchy/{roomId}.
//
// The depth limits are translated before the question is sent: the servers are asked for
// what lies below *their* room, so the page they return is already filtered for the window
// this walk is in and this server does not have to guess how deep each entry sat.
func (s *Service) RemoteHierarchy(ctx context.Context, roomID string, via []string, limit, maxDepth, minDepth int) (*HierarchyResult, error) {
	transport := s.federationTransport()
	if transport == nil {
		return nil, matrix.ErrNotFound("this server does not federate, so the space below %s cannot be read", roomID)
	}
	ctx, cancel := context.WithTimeout(ctx, remoteHierarchyBudget)
	defer cancel()
	if maxDepth < 0 {
		return nil, matrix.ErrNotFound("the space below %s is deeper than this walk reaches", roomID)
	}
	servers := hierarchyServers(roomID, via)
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	query.Set("max_depth", strconv.Itoa(maxDepth))
	if minDepth > 0 {
		query.Set("min_depth", strconv.Itoa(minDepth))
	}
	var lastFailure string
	for _, server := range servers {
		body, err := transport.Get(ctx, server, "/_matrix/federation/v1/hierarchy/"+url.PathEscape(roomID), query)
		if err != nil {
			lastFailure = err.Error()
			continue
		}
		answer := &HierarchyResult{}
		if err := json.Unmarshal([]byte(matrix.Encode(body)), answer); err != nil {
			lastFailure = "the answer is not a hierarchy: " + err.Error()
			continue
		}
		if len(answer.Rooms) == 0 {
			lastFailure = server + " answered with no rooms"
			continue
		}
		if answer.Rooms[0].RoomID != roomID {
			// The walk is about one room. A peer that answers with a different one is
			// describing a different space, and putting its rooms in this tree would be
			// this server's bug rather than that peer's.
			log.Printf("federation: %s answered the hierarchy of %s with %s first", server, roomID, answer.Rooms[0].RoomID)
			lastFailure = server + " answered a tree that does not start at " + roomID
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

	rooms, incomplete, err := s.walkHierarchy(ctx, rootID, r, maxDepth, params.MinDepth)
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
		link := spaceLink{roomID: row.StateKey}
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
