package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"gorm.io/gorm"
)

// A knock is a request to join that the room has to be able to see. Which is the whole
// of it: a knock kept on the knocking server is a note to self, and the people who can
// open the door are on other servers. So a knock travels the way a leave does - the
// asking server gets a shape from a server in the room, signs an event of its own, and
// hands it over - and the room's server answers with agreement rather than with state,
// because the knocking server has no room to fill in.
//
// A second endpoint carries the same event between servers that are not the room's own:
// a user knocking from a server that is already in the room has their server pass the
// knock on, since the server it asks may hold a copy of the room without hosting it.

// KnockFederatedRoom asks the servers of a room this server does not host to take a
// knock from a local user, and returns the room ID once one of them has.
func (s *Service) KnockFederatedRoom(ctx context.Context, roomID, userID, reason string) (string, error) {
	transport := s.federationTransport()
	if transport == nil {
		return "", matrix.ErrNotFound("this server holds no room %s, and does not speak to other servers", roomID)
	}
	// What this server already knows about the user is enough to refuse: the room's answer
	// cannot make a user who is in the room, invited, or banned into somebody who has a knock
	// to make, and asking a server for an answer this server has already been given is a round
	// trip that can only end in the same place.
	if problem := s.knownMembershipBlocksKnock(ctx, roomID, userID); problem != nil {
		return "", matrix.ErrForbidden("%v", problem)
	}
	var lastErr error
	for _, server := range s.knockServers(ctx, roomID) {
		if err := s.knockViaServer(ctx, transport, roomID, userID, reason, server); err != nil {
			lastErr = err
			log.Printf("service: knocking on %s through %s: %v", roomID, server, err)
			var peerErr *matrix.Error
			if errors.As(err, &peerErr) && peerErr.Code != "" && peerErr.Code != matrix.ErrCodeNotFound {
				// The room said no, in the room's words. Swapping that for a sentence of this
				// server's own would lose why: a person turned away from a room learns the
				// reason from the room, not from the middleman.
				return "", peerErr
			}
			continue
		}
		return roomID, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no server holds room %s to knock on", roomID)
	}
	return "", matrix.ErrForbidden("none of the servers of room %s would take the knock of %s: %v", roomID, userID, lastErr)
}

// knockServers names the servers a knock is offered to: the ones this server already
// relays the room to, and the room's own server, which is the one that can let the
// knocker in. The room's server is first, because a knock taken by a server that is in
// the room but does not host it still has to be passed on, and taking the answer from
// the horse's mouth saves a round that can only end in the same place.
func (s *Service) knockServers(ctx context.Context, roomID string) []string {
	servers, err := s.destinationsOfRoomIgnoringACL(ctx, roomID)
	if err != nil {
		servers = nil
	}
	out := []string{matrix.DomainOf(roomID)}
	for _, server := range servers {
		if server != s.serverName && server != out[0] {
			out = append(out, server)
		}
	}
	return out
}

// knockViaServer performs the make_knock/send_knock handshake with one peer and, once
// that peer has taken the knock, records it here.
func (s *Service) knockViaServer(ctx context.Context, transport *federation.Transport, roomID, userID, reason, server string) error {
	proposal, version, err := s.requestMakeKnock(ctx, transport, roomID, userID, server)
	if err != nil {
		return err
	}
	if !s.roomVersionIsSupported(version) {
		return fmt.Errorf("the room is version %s, which this server cannot speak", version)
	}
	proto, _ := proposal["event"].(matrix.Object)
	if proto == nil {
		return fmt.Errorf("%s answered make_knock with no event template", server)
	}
	content := matrix.Object{"membership": matrix.MembershipKnock}
	if reason != "" {
		content["reason"] = reason
	}
	packet, err := s.buildRemotePDU(s.remoteMemberDraft(roomID, userID, content), version, s.NowMS(),
		numberField(proto, "depth"), idsOfPDUList(proto, "prev_events"), idsOfPDUList(proto, "auth_events"),
		objectOf(proto, "unsigned"))
	if err != nil {
		return err
	}
	if err := s.SignAndIdentify(ctx, packet); err != nil {
		return err
	}
	if err := s.sendKnock(ctx, transport, server, roomID, packet); err != nil {
		return err
	}
	// The room has taken the knock. Recording it here is what makes the knocker's own
	// server agree with the room: their client shows the knock as pending, and a second
	// knock from the same user is judged against it.
	outcome, why, err := s.applyIncomingPDU(ctx, packet.object, version, server, packet.eventID)
	if err != nil {
		return err
	}
	if outcome == OutcomeRejected {
		return fmt.Errorf("this server's own copy of the room refuses the knock it just sent: %s", why)
	}
	return nil
}

// remoteMemberDraft is the draft of a membership event about the sender, which is what
// join, knock and leave are.
func (s *Service) remoteMemberDraft(roomID, userID string, content matrix.Object) *EventDraft {
	key := userID
	return &EventDraft{
		RoomID:   roomID,
		Type:     matrix.TypeMember,
		StateKey: &key,
		Sender:   userID,
		Content:  matrix.Encode(content),
	}
}

// requestMakeKnock asks a server for the knock template of a room. Version 2 is asked
// for first because it names the room version; version 1 is the endpoint older servers
// serve, and it answers with the room version as well.
func (s *Service) requestMakeKnock(ctx context.Context, transport *federation.Transport, roomID, userID, server string) (matrix.Object, string, error) {
	for _, version := range []string{"v2", "v1"} {
		path := fmt.Sprintf("/_matrix/federation/%s/make_knock/%s/%s",
			version, url.PathEscape(roomID), url.PathEscape(userID))
		body, err := transport.Get(ctx, server, path, nil)
		if err != nil {
			if isUnrecognized(err) && version == "v2" {
				continue
			}
			return nil, "", err
		}
		roomVersion, _ := body["room_version"].(string)
		if roomVersion == "" {
			return nil, "", fmt.Errorf("%s answered make_knock with no room version", server)
		}
		return body, roomVersion, nil
	}
	return nil, "", fmt.Errorf("%s serves no make_knock this server understands", server)
}

// sendKnock hands the signed knock to the server that proposed it. The body wraps the
// event, unlike send_leave: a knock has no other shape to be.
func (s *Service) sendKnock(ctx context.Context, transport *federation.Transport, server, roomID string, packet *pdu) error {
	for _, version := range []string{"v2", "v1"} {
		path := fmt.Sprintf("/_matrix/federation/%s/send_knock/%s/%s",
			version, url.PathEscape(roomID), url.PathEscape(packet.eventID))
		if _, err := transport.Put(ctx, server, path, matrix.Object{"event": packet.object}); err != nil {
			if isUnrecognized(err) && version == "v2" {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("%s serves no send_knock this server understands", server)
}

// ServeMakeKnock answers a peer's make_knock: the room's version and the shape of the
// knock that would attach to this server's graph. As with make_leave, a shape is not an
// authorisation - the room decides when the signed event arrives.
func (s *Service) ServeMakeKnock(ctx context.Context, roomID, userID, peer string) (matrix.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no room %s", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	if acl, err := s.roomServerACL(ctx, roomID); err != nil {
		return nil, err
	} else if !federation.ServerAllowed(acl, peer) {
		return nil, matrix.ErrForbidden("this room excludes %s", peer)
	}
	if !matrix.IsUserID(userID) {
		return nil, matrix.ErrInvalidRequest("%s is not a user this server can name", userID)
	}
	version := RoomVersionOf(room)
	var (
		prevIDs []string
		authIDs []string
		depth   int64
	)
	err = s.db.Tx(func(tx *gorm.DB) error {
		events := persist.NewEventRepo(tx)
		leaves, err := events.ForwardExtremities(roomID, maxPrevEvents)
		if err != nil {
			return matrix.ErrInternal(err)
		}
		prevIDs = leaves
		depth, err = depthAfter(events, roomID, prevIDs)
		if err != nil {
			return err
		}
		authIDs, err = selectAuthEvents(persist.NewStateRepo(tx), &EventDraft{
			RoomID:   roomID,
			Type:     matrix.TypeMember,
			StateKey: &userID,
			Sender:   userID,
			Content:  matrix.Encode(matrix.Object{"membership": matrix.MembershipKnock}),
		}, version)
		return err
	})
	if err != nil {
		return nil, err
	}
	template := matrix.Object{
		"room_id":          roomID,
		"type":             matrix.TypeMember,
		"sender":           userID,
		"state_key":        userID,
		"origin_server_ts": s.NowMS(),
		"depth":            depth,
		"prev_events":      pduIDList(prevIDs),
		"auth_events":      pduIDList(authIDs),
		"content":          matrix.Object{"membership": matrix.MembershipKnock},
	}
	return matrix.Object{"room_version": version, "event": template}, nil
}

// ServeSendKnock takes a knock event a server signed for a room this server hosts, and
// decides whether the room accepts it. The answer is agreement, not state; the rest of
// the room is told, because a knock nobody can see is a request nobody can answer.
func (s *Service) ServeSendKnock(ctx context.Context, roomID, eventID string, event matrix.Object, peer string) (matrix.Object, error) {
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no room %s", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	version := RoomVersionOf(room)
	sender, problem := membershipForEndpoint(event, eventID, roomID, "send_knock", matrix.MembershipKnock)
	if problem != nil {
		return nil, problem
	}
	if usesDerivedEventIDs(version) {
		derived, err := eventIDOfKnock(event, version)
		if err != nil {
			return nil, matrix.ErrInvalidRequest("the event cannot be named: %v", err)
		}
		if derived != eventID {
			return nil, matrix.ErrInvalidRequest("the path names %s and the event hashes to %s", eventID, derived)
		}
	}
	if problem := s.knockRoomProblem(ctx, roomID, sender); problem != nil {
		return nil, matrix.ErrForbidden("this room refuses the knock of %s: %v", sender, problem)
	}
	return s.acceptKnock(ctx, roomID, eventID, event, version, peer)
}

// ServeKnock takes a knock that a server forwards for a room it does not host: the
// knocking server's own server is in the room, so it asks a server in the room rather
// than the room's server, and the server it asks has to carry the knock the rest of the
// way. Answering 404 here would mean a user of a server that is already in a room cannot
// knock on it.
func (s *Service) ServeKnock(ctx context.Context, roomID, eventID string, event matrix.Object, peer string) (matrix.Object, error) {
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no room %s", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	version := RoomVersionOf(room)
	sender, problem := membershipForEndpoint(event, eventID, roomID, "knock", matrix.MembershipKnock)
	if problem != nil {
		return nil, problem
	}
	if s.LocalRoom(roomID) {
		if problem := s.knockRoomProblem(ctx, roomID, sender); problem != nil {
			return nil, matrix.ErrForbidden("this room refuses the knock of %s: %v", sender, problem)
		}
		return s.acceptKnock(ctx, roomID, eventID, event, version, peer)
	}
	if problem := s.knockRoomProblem(ctx, roomID, sender); problem != nil {
		return nil, matrix.ErrForbidden("this room refuses the knock of %s: %v", sender, problem)
	}
	transport := s.federationTransport()
	if transport == nil {
		return nil, matrix.ErrNotFound("this server does not speak to other servers")
	}
	packet := &pdu{object: withEventID(event, eventID), eventID: eventID}
	var lastErr error
	for _, server := range s.knockServers(ctx, roomID) {
		if server == peer || server == matrix.DomainOf(sender) {
			continue
		}
		if err := s.sendKnock(ctx, transport, server, roomID, packet); err != nil {
			lastErr = err
			continue
		}
		return matrix.Object{}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no other server holds room %s", roomID)
	}
	return nil, fmt.Errorf("this server holds a copy of %s but could not carry the knock on: %w", roomID, lastErr)
}

// acceptKnock stores a knock the room has decided to take and tells the rest of the room
// about it.
func (s *Service) acceptKnock(ctx context.Context, roomID, eventID string, event matrix.Object, version, peer string) (matrix.Object, error) {
	judged, reason := s.judgeIncomingPDU(ctx, withEventID(event, eventID), version)
	if reason != "" {
		return nil, matrix.ErrUnauthorized("the knock sent for %s is not one this server can accept: %s", roomID, reason)
	}
	outcome, reason, err := s.applyIncomingPDU(ctx, judged, version, peer, eventID)
	if err != nil {
		return nil, err
	}
	switch outcome {
	case OutcomeRejected:
		return nil, matrix.ErrForbidden("this room refuses the knock: %s", reason)
	case OutcomeStored, OutcomeSoftFailed:
		stored, err := s.db.EventRepo.Get(eventID)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		// The knock has to reach the servers that can answer it. A room whose members
		// cannot see a knock cannot reject it or let the knocker in, and the knocker is
		// left waiting in a room that never heard them.
		s.queueEventForPeersExcept(ctx, stored, version, peer)
	}
	return matrix.Object{}, nil
}

// knownMembershipBlocksKnock refuses a knock this server can already refuse without
// asking anybody: a user it knows to be in the room, invited, or banned has no knock to
// make. The room's join rule is deliberately not consulted - a server that holds a copy of
// a room may hold a stale or partial copy of its rules, and "I cannot see a knock rule" is
// not the same question as "the room has none".
func (s *Service) knownMembershipBlocksKnock(ctx context.Context, roomID, userID string) error {
	switch s.MembershipOf(ctx, roomID, userID) {
	case matrix.MembershipJoin, matrix.MembershipInvite, matrix.MembershipBan:
		return fmt.Errorf("you already have a membership in this room")
	}
	return nil
}

// knockRoomProblem answers whether a room admits a knock from a user at all, read from
// the side that is asked to accept it. The room has to be one that can be knocked on,
// and a user the room has already answered - in the room, invited, or banned - has no
// knock to make.
func (s *Service) knockRoomProblem(ctx context.Context, roomID, userID string) error {
	return s.knockProblemWith(ctx, s.joinRuleOf(ctx, roomID), s.MembershipOf(ctx, roomID, userID))
}

// eventIDOfKnock is the id a knock of a room version with derived ids is named by.
func eventIDOfKnock(event matrix.Object, version string) (string, error) {
	if !usesDerivedEventIDs(version) {
		if id, ok := event["event_id"].(string); ok {
			return id, nil
		}
		return "", fmt.Errorf("a room version %s event has to name itself", version)
	}
	return eventcrypto.EventID(event, version)
}
