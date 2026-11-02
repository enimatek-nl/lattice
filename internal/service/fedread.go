package service

import (
	"context"
	"log"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// FederatedEvent answers a peer asking for one event by id: GET of
// /_matrix/federation/v1/event/{eventID}.
//
// The answer is the event in the form its room version defines, in the array the
// specification calls pdus, beside the name of the server that is answering. A peer
// asks this when it has an event that cites this one - an auth event it was never sent,
// a prev event it never received - and it has to read the event to judge what it was
// sent. That is why this endpoint is not optional for a server that judges events: an
// auth chain this server cannot complete is an auth chain it can only refuse.
//
// The peer has to be in the room. A server that shares no membership with the room has
// no claim on its history, and a server the room excluded with an ACL has even less.
func (s *Service) FederatedEvent(ctx context.Context, eventID, peer string) (matrix.Object, error) {
	if eventID == "" {
		return nil, matrix.ErrNotFound("no event with an empty id")
	}
	event, err := s.db.EventRepo.Get(eventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no event %s", eventID)
		}
		return nil, matrix.ErrInternal(err)
	}
	room, err := s.db.RoomRepo.Get(event.RoomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no event %s", eventID)
		}
		return nil, matrix.ErrInternal(err)
	}
	shares, err := s.roomSharesServer(ctx, event.RoomID, peer)
	if err != nil {
		return nil, err
	}
	if !shares {
		return nil, matrix.ErrForbidden("the server %s is not part of room %s", peer, event.RoomID)
	}
	pdu, err := transportPDU(event.JSON, RoomVersionOf(room))
	if err != nil {
		log.Printf("federation: the event %s cannot be prepared for %s: %v", eventID, peer, err)
		return nil, matrix.ErrInternal(err)
	}
	return matrix.Object{
		"origin": s.ServerName(),
		"pdus":   []any{pdu},
	}, nil
}

// roomSharesServer reports whether a server is part of a room: some user of it is
// joined, or was invited and has not left. The ACL is read the same way the outbox
// reads it, so a server the room excluded cannot fetch its way back in through an
// event id.
func (s *Service) roomSharesServer(ctx context.Context, roomID, server string) (bool, error) {
	if server == "" {
		return false, nil
	}
	servers, err := s.destinationsOfRoom(ctx, roomID)
	if err != nil {
		return false, matrix.ErrInternal(err)
	}
	for _, name := range servers {
		if name == server {
			return true, nil
		}
	}
	// A room this server is the only member of still has to answer about itself, and a
	// peer that was invited but has not joined yet is in the room for the purposes of
	// reading what it was invited to.
	invited, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipInvite)
	if err != nil {
		return false, matrix.ErrInternal(err)
	}
	for _, member := range invited {
		if matrix.DomainOf(member.UserID) == server {
			return true, nil
		}
	}
	return false, nil
}
