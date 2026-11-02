package service

import (
	"context"
	"log"
	"net/url"
	"strings"

	"github.com/enimatek-nl/lattice/internal/federation"
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
	// A room this server is the only member of still has to answer about itself, and a peer
	// that was invited but has not joined yet is in the room for the purposes of reading what
	// it was invited to.
	//
	// Every membership counts, not only the standing ones. A server whose user has just been
	// kicked, or is in the middle of leaving, is partway through a handshake it cannot finish
	// without the room's events: refusing the auth chain of the leave it is signing leaves the
	// room with a member it still thinks is present. Whatever a room has told a server already,
	// it cannot un-tell by refusing the next read - and a server that never had anything from
	// the room is still refused.
	former, err := s.db.MembershipRepo.Members(roomID,
		matrix.MembershipInvite, matrix.MembershipLeave, matrix.MembershipBan, matrix.MembershipKnock)
	if err != nil {
		return false, matrix.ErrInternal(err)
	}
	for _, member := range former {
		if matrix.DomainOf(member.UserID) == server {
			return true, nil
		}
	}
	return false, nil
}

// ResolveRemoteAlias asks the server an alias carries which room it points at: GET of
// /_matrix/federation/v1/query/directory.
//
// An alias this server does not host is still an address. The only way to learn what it
// addresses is to ask the server whose name it carries, and a server that skips the
// question answers "no such room" about a room that exists a hop away - which is how a
// join by alias of a room on another server fails without ever reaching that server.
func (s *Service) ResolveRemoteAlias(ctx context.Context, alias matrix.Alias) (string, error) {
	transport := s.federationTransport()
	if transport == nil {
		return "", matrix.ErrNotFound("alias %s is not hosted by this server and federation is off", alias)
	}
	body, err := transport.Get(ctx, alias.Domain(), "/_matrix/federation/v1/query/directory",
		url.Values{"room_alias": []string{string(alias)}})
	if err != nil {
		return "", err
	}
	roomID, _ := body["room_id"].(string)
	if roomID == "" {
		return "", matrix.ErrNotFound("%s answered no room for the alias %s", alias.Domain(), alias)
	}
	if !strings.HasPrefix(roomID, "!") {
		return "", matrix.ErrNotFound("%s answered the alias %s with %q, which is not a room id",
			alias.Domain(), alias, roomID)
	}
	return roomID, nil
}

// DirectoryAnswer answers a peer asking what one of this server's aliases points at:
// GET of /_matrix/federation/v1/query/directory.
//
// An alias is a public identifier - that is what makes it an address - so the answer is
// not restricted to servers already in the room. What is restricted is what a room that
// excluded the peer has to offer: an ACL is the room's own statement about who may be
// told anything about it.
func (s *Service) DirectoryAnswer(ctx context.Context, alias string, peer string) (matrix.Object, error) {
	row, err := s.db.AliasRepo.Get(alias)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server hosts no alias %s", alias)
		}
		return nil, matrix.ErrInternal(err)
	}
	acl, err := s.roomServerACL(ctx, row.RoomID)
	if err != nil {
		return nil, err
	}
	if !federation.ServerAllowed(acl, peer) {
		return nil, matrix.ErrForbidden("this room excludes %s", peer)
	}
	servers, err := s.destinationsOfRoomIgnoringACL(ctx, row.RoomID)
	if err != nil {
		return nil, err
	}
	return matrix.Object{
		"room_id": row.RoomID,
		"servers": servers,
	}, nil
}
