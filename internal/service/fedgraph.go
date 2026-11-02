package service

import (
	"context"
	"log"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// The endpoints here answer one question a peer asks constantly: what does the room this
// event belongs to look like, and what authorised it. A server that receives an event
// cannot judge it from the event alone - it needs the state the event cites, and the
// authority the event was built on. Serving those is what makes a server able to send
// anything at all into a room it does not host: a peer that cannot ask for the state
// behind an event refuses the event, however correct the event is. The refusal arrives
// worded as a complaint about the sender ("isn't divulging details about prev_events"),
// because from the refusing server's side that is exactly what happened.

// maxServedChainDepth bounds how far the auth chain of a served set is walked. A real
// chain is a handful of events deep; the bound is there so a room holding a cycle cannot
// make a read spin.
const maxServedChainDepth = 64

// maxServedMissingEvents caps how many events get_missing_events answers, which is how far
// a peer may ask this server to walk its graph for one request.
const maxServedMissingEvents = 20

// ServeStateIDs answers GET /_matrix/federation/v1/state_ids/{roomId}: the ids of the
// events that are the room's current state, and the auth chains behind them.
//
// The ids are given without the events on purpose - a peer that does not hold them asks for
// them one at a time through GET /_matrix/federation/v2/state/{roomId}/{eventId}, which
// keeps a room whose state is large from being copied whole to a server that only needed
// two of them.
func (s *Service) ServeStateIDs(ctx context.Context, roomID, peer string) (matrix.Object, error) {
	room, err := s.roomForPeer(ctx, roomID, peer)
	if err != nil {
		return nil, err
	}
	state, err := s.db.StateRepo.Events(roomID, nil)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	pduIDs := make([]string, 0, len(state))
	seeds := make([]string, 0, len(state))
	for _, event := range state {
		pduIDs = append(pduIDs, event.EventID)
		seeds = append(seeds, storedIDsOf(event.AuthEvents)...)
	}
	return matrix.Object{
		"origin":         s.ServerName(),
		"pdu_ids":        pduIDs,
		"auth_chain_ids": s.authChainIDsOf(seeds),
		"room_version":   RoomVersionOf(room),
	}, nil
}

// ServeRoomStateEvent answers GET /_matrix/federation/v1|v2/state/{roomId}/{eventId}: one
// event of the room's current state. Version 2 answers an object; version 1 answers the
// event itself, which is why the handler, not the service, decides the shape.
func (s *Service) ServeRoomStateEvent(ctx context.Context, roomID, eventID, peer string) (matrix.Object, error) {
	pdu, err := s.servedPDU(ctx, roomID, eventID, peer)
	if err != nil {
		return nil, err
	}
	return matrix.Object{"origin": s.ServerName(), "event": pdu}, nil
}

// ServeEventAuth answers GET /_matrix/federation/v1/event_auth/{roomId}/{eventId}: the
// auth chain of one event, in full. A peer asks when it holds the event but not what
// authorised it - an event relayed to it by a third server, or one that arrived inside a
// join's state.
func (s *Service) ServeEventAuth(ctx context.Context, roomID, eventID, peer string) (matrix.Object, error) {
	if _, err := s.roomForPeer(ctx, roomID, peer); err != nil {
		return nil, err
	}
	event, err := s.db.EventRepo.Get(eventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no event %s", eventID)
		}
		return nil, matrix.ErrInternal(err)
	}
	if event.RoomID != roomID {
		return nil, matrix.ErrNotFound("the event %s is not in room %s", eventID, roomID)
	}
	version, err := s.roomVersion(roomID)
	if err != nil {
		return nil, err
	}
	chain := matrix.Array{}
	for _, id := range s.authChainIDsOf(storedIDsOf(event.AuthEvents)) {
		row, err := s.db.EventRepo.Get(id)
		if err != nil {
			// A chain this server cannot complete is not served half: a peer would judge an
			// event against a missing authority and reach a wrong answer.
			log.Printf("federation: the auth chain of %s names %s, which this server cannot read: %v", eventID, id, err)
			continue
		}
		pdu, err := transportPDU(row.JSON, version)
		if err != nil {
			continue
		}
		chain = append(chain, pdu)
	}
	return matrix.Object{"auth_chain": chain}, nil
}

// ServeMissingEvents answers POST /_matrix/federation/v1/get_missing_events/{roomId}: the
// events this server holds that lie between two points in the graph. It is what a peer uses
// to fill a gap it noticed - an event whose prev_events it was never sent - instead of
// asking for the room's whole history.
func (s *Service) ServeMissingEvents(ctx context.Context, roomID string, peer string, body matrix.Object) (matrix.Object, error) {
	if _, err := s.roomForPeer(ctx, roomID, peer); err != nil {
		return nil, err
	}
	latest := stringSlice(body["latest_events"])
	earliest := stringSlice(body["earliest_events"])
	if len(latest) == 0 || len(earliest) == 0 {
		return nil, matrix.ErrInvalidParam("get_missing_events needs latest_events and earliest_events")
	}
	limit := maxServedMissingEvents
	if wanted := numberField(body, "limit"); wanted > 0 && int(wanted) < limit {
		limit = int(wanted)
	}
	version, err := s.roomVersion(roomID)
	if err != nil {
		return nil, err
	}

	// The walk is backwards from the events the peer already has, stopping at the events it
	// says it is missing: anything past those is older than the gap it asked about.
	wanted := map[string]bool{}
	for _, id := range earliest {
		wanted[id] = true
	}
	exclude := map[string]bool{}
	for _, id := range latest {
		exclude[id] = true
	}
	seen := map[string]bool{}
	collected := make([]string, 0, limit)
	frontier := append([]string(nil), latest...)
	for depth := 0; depth < maxServedMissingEvents && len(collected) < limit; depth++ {
		next := make([]string, 0, len(frontier))
		for _, id := range frontier {
			if seen[id] {
				continue
			}
			seen[id] = true
			event, err := s.db.EventRepo.Get(id)
			if err != nil {
				continue
			}
			if event.RoomID != roomID {
				continue
			}
			for _, prev := range storedIDsOf(event.PrevEvents) {
				if seen[prev] || wanted[prev] {
					continue
				}
				if !exclude[prev] {
					collected = append(collected, prev)
					if len(collected) >= limit {
						break
					}
				}
				next = append(next, prev)
			}
			if len(collected) >= limit {
				break
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}

	events := matrix.Array{}
	for _, id := range collected {
		row, err := s.db.EventRepo.Get(id)
		if err != nil {
			continue
		}
		pdu, err := transportPDU(row.JSON, version)
		if err != nil {
			continue
		}
		events = append(events, pdu)
	}
	return matrix.Object{"events": events}, nil
}

// roomForPeer returns the room a graph read is asked for, when the asking server is part
// of it. The room's own state is not this server's to publish to a stranger: a server that
// shares no membership has no claim on it, and a server the room excluded with an ACL has
// less than none.
func (s *Service) roomForPeer(ctx context.Context, roomID, peer string) (*persist.Room, error) {
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no room %s", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	shares, err := s.roomSharesServer(ctx, roomID, peer)
	if err != nil {
		return nil, err
	}
	if !shares {
		return nil, matrix.ErrForbidden("the server %s is not part of room %s", peer, roomID)
	}
	return room, nil
}

// servedPDU reads one event of a room for a peer, checking the room the event belongs to is
// the room that was asked about. An event id is an opaque string to a peer, so the check is
// what stops one room being used to read another.
func (s *Service) servedPDU(ctx context.Context, roomID, eventID, peer string) (matrix.Object, error) {
	room, err := s.roomForPeer(ctx, roomID, peer)
	if err != nil {
		return nil, err
	}
	event, err := s.db.EventRepo.Get(eventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no event %s", eventID)
		}
		return nil, matrix.ErrInternal(err)
	}
	if event.RoomID != roomID {
		return nil, matrix.ErrNotFound("the event %s is not in room %s", eventID, roomID)
	}
	pdu, err := transportPDU(event.JSON, RoomVersionOf(room))
	if err != nil {
		log.Printf("federation: the event %s cannot be prepared for %s: %v", eventID, peer, err)
		return nil, matrix.ErrInternal(err)
	}
	return pdu, nil
}

// authChainIDsOf walks the authority of a set of events, as far as this server can read it.
// The result is a set rather than an order: a peer is asking which events it is missing, not
// which order to read them in.
func (s *Service) authChainIDsOf(seeds []string) []string {
	chain := map[string]bool{}
	frontier := append([]string(nil), seeds...)
	for depth := 0; depth < maxServedChainDepth && len(frontier) > 0; depth++ {
		next := make([]string, 0, len(frontier))
		for _, id := range frontier {
			if id == "" || chain[id] {
				continue
			}
			chain[id] = true
			event, err := s.db.EventRepo.Get(id)
			if err != nil {
				continue
			}
			next = append(next, storedIDsOf(event.AuthEvents)...)
		}
		frontier = next
	}
	ids := make([]string, 0, len(chain))
	for id := range chain {
		ids = append(ids, id)
	}
	return ids
}

// roomVersion reads the room version a room is held in, for preparing PDUs of that version.
func (s *Service) roomVersion(roomID string) (string, error) {
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return "", matrix.ErrNotFound("this server holds no room %s", roomID)
		}
		return "", matrix.ErrInternal(err)
	}
	return RoomVersionOf(room), nil
}
