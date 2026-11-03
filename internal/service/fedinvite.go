package service

import (
	"context"
	"fmt"
	"log"
	"net/url"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"gorm.io/gorm"
)

// An invite is the one membership a server receives without having asked. Every other
// handshake starts with the invited server knocking - make_join, make_leave - because it
// needs the room's graph to build an event. An invite goes the other way: the inviting
// server already has the room, and it ships the invite together with the piece of the
// room's state the invite is authorised by, so a server that has never heard of the room
// can still tell its user they have been invited.
//
// That is also why the endpoint exists rather than an ordinary event relay: a relay
// delivers an invite citing events the invited server has never been told about, and the
// invited server spends a round trip per event it lacks before it can say anything to its
// user.

// ServeInvite takes an invite a peer has made for a user this server hosts. The body
// carries the invite and the stripped state that authorises it; the answer is the same
// invite with this server's signature added, which is what lets the inviting server - and
// later anybody else - show that the invited server agreed to be part of the room.
func (s *Service) ServeInvite(ctx context.Context, roomID, eventID string, body matrix.Object, peer string) (matrix.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, hasEvent := body["event"]
	if !hasEvent {
		return nil, matrix.ErrInvalidRequest("the invite carries no event")
	}
	event, ok := raw.(matrix.Object)
	if !ok {
		event = objectOf(body, "event")
	}
	if federation.EventField(event, "room_id") != roomID {
		return nil, matrix.ErrInvalidRequest("the invite is for room %s, not %s",
			federation.EventField(event, "room_id"), roomID)
	}
	invitee, problem := membershipForEndpoint(event, eventID, roomID, "invite", matrix.MembershipInvite)
	if problem != nil {
		return nil, problem
	}
	if !s.LocalUser(invitee) {
		// The endpoint is addressed to the server of the invited user. Taking an invite for
		// somebody else would be this server promising an invitation it cannot deliver.
		return nil, matrix.ErrForbidden("%s is not a user this server hosts", invitee)
	}
	state := stateListOf(body, "invite_room_state")
	version, err := s.roomVersionOfInvite(ctx, event, eventID, state)
	if err != nil {
		return nil, err
	}
	if !s.roomVersionIsSupported(version) {
		return nil, matrix.ErrForbidden("the room is version %s, which this server cannot speak", version)
	}
	if problem := s.inviteIsBackedByTheRoom(event, state, roomID); problem != "" {
		return nil, matrix.ErrForbidden("%s", problem)
	}
	judged, reason := s.judgeIncomingPDU(ctx, withEventID(event, eventID), version)
	if reason != "" {
		return nil, matrix.ErrUnauthorized("the invite sent for %s is not one this server can accept: %s", roomID, reason)
	}
	if err := s.storeInvitedRoom(ctx, roomID, version, state, judged, eventID); err != nil {
		return nil, err
	}
	// This server signs the invitation it accepted. The inviter's signature stays on the
	// event - a signature is made over the event, not over the copy somebody else made of
	// it - and the copy that goes back carries both.
	key, err := s.SigningKey(ctx)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	signed, err := eventcrypto.HashAndSign(judged, version, key.Private, s.serverName, key.ID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return matrix.Object{"event": signed}, nil
}

// inviteIsBackedByTheRoom reports why the state shipped with an invite does not support
// it, or "" when it does. The check is deliberately small: an invited server holds no
// graph, so it cannot run the room's rules, and pretending otherwise means refusing honest
// invites over a detail the room's own server never claimed. What it can insist on is that
// the room is named (its create event is there) and that the inviter is described as
// somebody in it - an invite from a server that will not say the inviter joined is a claim
// about a room nobody can recognise.
func (s *Service) inviteIsBackedByTheRoom(event matrix.Object, state []matrix.Object, roomID string) string {
	sender := federation.EventField(event, "sender")
	create := false
	joined := false
	for _, ev := range state {
		switch federation.EventField(ev, "type") {
		case matrix.TypeCreate:
			create = true
		case matrix.TypeMember:
			if federation.EventField(ev, "state_key") != sender {
				continue
			}
			if membership, _ := objectOf(ev, "content")["membership"].(string); membership == matrix.MembershipJoin {
				joined = true
			}
		}
	}
	if !create {
		return fmt.Sprintf("the invite for %s ships no m.room.create, so it names no room", roomID)
	}
	if !joined {
		return fmt.Sprintf("the invite for %s does not describe %s as joined to the room", roomID, sender)
	}
	return ""
}

// roomVersionOfInvite works out which room version an invite belongs to. The room row
// answers when this server holds the room; otherwise the event is hashed under each
// version this server speaks and the one that reproduces the id the peer named wins,
// because in those versions the id is the hash and a wrong version cannot reproduce it.
// Only when neither works does this server fall back to its default, which is a guess it
// will have to confirm at the join handshake.
func (s *Service) roomVersionOfInvite(ctx context.Context, event matrix.Object, eventID string, state []matrix.Object) (string, error) {
	roomID := federation.EventField(event, "room_id")
	if room, err := s.db.RoomRepo.Get(roomID); err == nil {
		return RoomVersionOf(room), nil
	} else if !persist.IsNotFound(err) {
		return "", matrix.ErrInternal(err)
	}
	if eventID == "" {
		return DefaultRoomVersion, nil
	}
	matches := 0
	version := ""
	for _, candidate := range s.roomVersions {
		derived, err := eventcrypto.EventID(withEventID(event, eventID), candidate)
		if err != nil || derived != eventID {
			continue
		}
		matches++
		version = candidate
	}
	if matches == 1 {
		return version, nil
	}
	// An id that no version reproduces is not a reason to refuse an invitation: an older
	// room version this server does not speak, or an id the peer invented, both look the
	// same from here. The default is kept, and the join handshake is where a wrong guess
	// surfaces - there, this server asks for the room version and the state together.
	if state != nil {
		log.Printf("service: the invite of %s in %s names an id no room version this server speaks reproduces",
			federation.EventField(event, "sender"), roomID)
	}
	return DefaultRoomVersion, nil
}

// storeInvitedRoom keeps what an invite brought: the room, the state that authorises the
// invite, and the invite itself. The state is kept as another server's description of the
// room rather than as this server's history - it is not in the timeline - and the invite is
// applied like any other membership, because a user who is not told they have been invited
// cannot answer.
func (s *Service) storeInvitedRoom(ctx context.Context, roomID, version string, state []matrix.Object, invite matrix.Object, eventID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	joinRule := matrix.JoinRulesInvite
	for _, ev := range state {
		if federation.EventField(ev, "type") != matrix.TypeJoinRules {
			continue
		}
		if rule, ok := objectOf(ev, "content")["join_rule"].(string); ok && rule != "" {
			joinRule = rule
		}
	}
	if err := s.db.Tx(func(tx *gorm.DB) error {
		return persist.NewRoomRepo(tx).Create(&persist.Room{
			ID:          roomID,
			RoomVersion: version,
			JoinRule:    joinRule,
		})
	}); err != nil && !persist.IsDuplicate(err) {
		return matrix.ErrInternal(err)
	}
	for _, ev := range state {
		id := federation.EventField(ev, "event_id")
		if id == "" {
			// Stripped state is not required to carry ids, and a state event of a room
			// version from 3 onwards does not carry one inside itself. Without an id this
			// server can only keep the piece of state, which it does under the type and
			// state key the room's own rules name it by.
			continue
		}
		if _, err := s.keepCitedState(ev, id, version); err != nil {
			return err
		}
	}
	outcome, reason, err := s.applyIncomingPDU(ctx, invite, version, federation.EventField(invite, "sender"), eventID)
	if err != nil {
		return err
	}
	if outcome == OutcomeRejected {
		return matrix.ErrForbidden("this server refuses the invite of %s: %s",
			federation.EventField(invite, "state_key"), reason)
	}
	// The chain of an invite into a room this server has just learned about cites state the
	// peer shipped rather than events it can name, so a soft failure here is expected: the
	// user has been invited, and the room's graph will be filled in by the join handshake.
	if outcome == OutcomeSoftFailed {
		log.Printf("service: the invite of %s in %s is kept without the room's full graph: %s",
			federation.EventField(invite, "state_key"), roomID, reason)
	}
	return nil
}

// InviteRemoteServer hands an invite to the server of a user this room has invited, for
// the case the ordinary relay cannot reach: a server that is not in the room has nothing to
// relay to it from, and an invite is the one event a room sends before the receiving
// server is part of it.
func (s *Service) InviteRemoteServer(ctx context.Context, roomID, invitee string) error {
	transport := s.federationTransport()
	if transport == nil {
		return fmt.Errorf("this server does not speak to other servers")
	}
	row, err := s.db.StateRepo.Get(roomID, matrix.TypeMember, invitee)
	if err != nil {
		if persist.IsNotFound(err) {
			return fmt.Errorf("this server holds no invite of %s in %s", invitee, roomID)
		}
		return matrix.ErrInternal(err)
	}
	stored, err := s.db.EventRepo.Get(row.EventID)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	event := withEventID(matrix.MustParseObject([]byte(stored.JSON)), row.EventID)
	state := s.strippedStateObjects(ctx, roomID, invitee)
	body := matrix.Object{
		"event":             event,
		"invite_room_state": state,
	}
	// The room version travels with the invite. A v2 invite event is a v3-format event, and
	// which format the bytes are in is a property of the room, not of the endpoint: a receiving
	// server that is not yet in the room has no copy of the room to read the version from, and
	// without it it cannot say whether the event it is being handed is signed over the old keys
	// or the new ones.
	if room, err := s.db.RoomRepo.Get(roomID); err == nil {
		body["room_version"] = RoomVersionOf(room)
	}
	server := matrix.DomainOf(invitee)
	path := fmt.Sprintf("/_matrix/federation/v2/invite/%s/%s",
		url.PathEscape(roomID), url.PathEscape(row.EventID))
	if _, err := transport.Put(ctx, server, path, body); err != nil {
		path = fmt.Sprintf("/_matrix/federation/v1/invite/%s/%s",
			url.PathEscape(roomID), url.PathEscape(row.EventID))
		if _, fallbackErr := transport.Put(ctx, server, path, body); fallbackErr != nil {
			return fmt.Errorf("%s would not take the invite of %s: %w", server, invitee, fallbackErr)
		}
	}
	return nil
}

// strippedStateObjects renders the state an invite is authorised by in the object form the
// federation endpoint carries. It is the same block a client is shown for a room it has
// not joined: the room's identity, how it is entered, and the two memberships the
// invitation is between.
func (s *Service) strippedStateObjects(ctx context.Context, roomID, invitee string) []matrix.Object {
	events := s.strippedState(ctx, roomID, invitee)
	out := make([]matrix.Object, 0, len(events))
	for _, ev := range events {
		out = append(out, matrix.MustParseObject(matrix.Encode(ev)))
	}
	return out
}

// stateListOf reads a list of events out of a federation body, tolerating the empty
// member as well as a missing one.
func stateListOf(body matrix.Object, field string) []matrix.Object {
	raw, ok := body[field]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]matrix.Object, 0, len(list))
	for _, entry := range list {
		if ev, ok := entry.(matrix.Object); ok {
			out = append(out, ev)
			continue
		}
		if encoded, ok := entry.(map[string]any); ok {
			out = append(out, matrix.MustParseObject(matrix.Encode(encoded)))
		}
	}
	return out
}
