package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A room directory is a server's own list of what it publishes, and - like an alias - it is
// only ever authoritative for itself. Both directions of that exist here. A peer asking what
// this server publishes is answered with the rooms this server publishes and the servers that
// can serve them, which is the piece a directory entry from one's own server cannot carry: a
// client that is handed a room it does not host still has to know who to go to. And a client
// asking about somebody else's directory is sent to that somebody else rather than answered
// from our own list, because a directory is not a fact about rooms, it is a statement about
// which rooms a particular server chose to put on a shelf.

// PublicRoomForPeer is one entry of a room directory as one server tells another: the
// description the client API gives, plus the servers that can serve the room.
type PublicRoomForPeer struct {
	PublicRoom
	Servers []string `json:"servers"`
}

// PublicRoomsForPeer is a page of the directory in the shape a peer is answered with. The
// pagination tokens are the same offsets this server gives its own clients, which is honest
// for a peer that keeps asking this server the same question and meaningless to one that
// does not - and every continuation of a page is asked of the server that made the token.
type PublicRoomsForPeer struct {
	Chunk      []PublicRoomForPeer `json:"chunk"`
	NextBatch  string              `json:"next_batch,omitempty"`
	PrevBatch  string              `json:"prev_batch,omitempty"`
	TotalRooms int64               `json:"total_rooms"`
}

// ServePublicRooms answers a peer asking what this server publishes: GET of
// /_matrix/federation/v1/publicRooms and v3 of the same.
//
// A room that excludes the asking server is left out rather than described and refused
// later: an ACL is the room's statement about who may be told anything about it, and a
// directory entry is a way of telling. `total_rooms` still counts everything this server
// publishes, because that count is a fact about this server's shelf rather than a promise
// about what any one peer will find on it.
func (s *Service) ServePublicRooms(ctx context.Context, peer string, filter string, limit int, since string) (*PublicRoomsForPeer, error) {
	page, err := s.PublicRooms(ctx, filter, limit, since)
	if err != nil {
		return nil, err
	}
	answer := &PublicRoomsForPeer{
		Chunk:      []PublicRoomForPeer{},
		NextBatch:  page.NextBatch,
		PrevBatch:  page.PrevBatch,
		TotalRooms: page.TotalRooms,
	}
	for _, room := range page.Chunk {
		acl, err := s.roomServerACL(ctx, room.RoomID)
		if err != nil {
			return nil, err
		}
		if !federation.ServerAllowed(acl, peer) {
			continue
		}
		servers, err := s.destinationsOfRoom(ctx, room.RoomID)
		if err != nil {
			return nil, err
		}
		// This server publishes the room, so this server can serve it - which is the one
		// thing the list is guaranteed to contain.
		entry := PublicRoomForPeer{PublicRoom: room, Servers: append([]string{s.serverName}, servers...)}
		answer.Chunk = append(answer.Chunk, entry)
	}
	return answer, nil
}

// RemotePublicRooms asks another server what it publishes, and answers a client of this one
// with that. v3 is asked for first: it is the form that carries a room's type and the
// filter as an object, and a server that only knows v1 is answered with v1 rather than with
// a failure.
func (s *Service) RemotePublicRooms(ctx context.Context, server, filter string, limit int, since string) (*PublicRoomsResult, error) {
	transport := s.federationTransport()
	if transport == nil {
		return nil, matrix.ErrNotFound("this server does not federate, so the rooms %s publishes cannot be listed", server)
	}
	if !matrix.ValidServerName(server) {
		return nil, matrix.ErrInvalidParam("%q is not a server name, so nobody there publishes rooms", server)
	}
	var lastFailure string
	if page, err := s.publicRoomsV3(ctx, transport, server, filter, limit, since); err == nil {
		return page, nil
	} else {
		lastFailure = err.Error()
		if !isUnrecognized(err) {
			// The other server's directory is not reachable, which is a fact about the
			// question rather than a failure of the one who asked it: a client pointed at
			// somebody else's shelf is told the shelf is not there, and not shown our own.
			return nil, matrix.ErrNotFound("the rooms %s publishes could not be listed: %v", server, err)
		}
	}
	page, err := s.publicRoomsV1(ctx, transport, server, filter, limit, since)
	if err != nil {
		return nil, matrix.ErrNotFound("the rooms %s publishes could not be listed: %s", server, fmt.Sprintf("%s; v1: %s", lastFailure, err))
	}
	return page, nil
}

func (s *Service) publicRoomsV3(ctx context.Context, transport *federation.Transport, server, filter string, limit int, since string) (*PublicRoomsResult, error) {
	body := matrix.Object{"limit": limit}
	if filter != "" {
		body["filter"] = matrix.Object{"generic_search_term": filter}
	}
	if since != "" {
		body["since"] = since
	}
	answer, err := transport.Post(ctx, server, "/_matrix/federation/v3/publicRooms", body)
	if err != nil {
		return nil, err
	}
	return decodePublicRooms(answer)
}

func (s *Service) publicRoomsV1(ctx context.Context, transport *federation.Transport, server, filter string, limit int, since string) (*PublicRoomsResult, error) {
	query := url.Values{"limit": []string{strconv.Itoa(limit)}}
	if filter != "" {
		query.Set("filter_generic_search_terms", filter)
	}
	if since != "" {
		query.Set("since", since)
	}
	answer, err := transport.Get(ctx, server, "/_matrix/federation/v1/publicRooms", query)
	if err != nil {
		return nil, err
	}
	return decodePublicRooms(answer)
}

// decodePublicRooms reads a directory page a peer answered with. The federation form carries
// an extra `servers` list per room, which a client of this server is not shown as part of a
// directory entry it asked for locally - so it is decoded and dropped rather than ignored,
// which is what keeps the two shapes from being quietly conflated somewhere else.
func decodePublicRooms(answer matrix.Object) (*PublicRoomsResult, error) {
	type entry struct {
		PublicRoom
		Servers []string `json:"servers"`
	}
	var page struct {
		Chunk      []entry `json:"chunk"`
		NextBatch  string  `json:"next_batch"`
		PrevBatch  string  `json:"prev_batch"`
		TotalRooms int64   `json:"total_rooms"`
	}
	if err := json.Unmarshal([]byte(matrix.Encode(answer)), &page); err != nil {
		return nil, fmt.Errorf("the directory answer cannot be read: %w", err)
	}
	result := &PublicRoomsResult{
		Chunk:      make([]PublicRoom, 0, len(page.Chunk)),
		NextBatch:  page.NextBatch,
		PrevBatch:  page.PrevBatch,
		TotalRooms: page.TotalRooms,
	}
	for _, room := range page.Chunk {
		result.Chunk = append(result.Chunk, room.PublicRoom)
	}
	return result, nil
}
