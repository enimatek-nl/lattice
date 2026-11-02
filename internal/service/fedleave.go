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

// A leave is the mirror of a join, and the two differences between them are the whole
// of the difficulty: the event is made by the server that is leaving rather than by the
// room's server, and the room's server only has to decide whether it stands. So
// make_leave hands out a shape - the prev_events and auth events that attach to this
// room's graph - and send_leave takes the signed result and treats it as it would any
// other event from that server.
//
// The handshake matters most for the two cases where the room's server would otherwise
// never hear: a user rejecting an invite they were sent across the wire, and a user
// leaving a room they joined from another server. Without it the room keeps relaying
// itself to a server whose user has gone.

// ServeMakeLeave answers a peer's make_leave: the room's version and the shape of the
// leave event that would attach to this server's graph.
//
// Like make_join, this is a shape and not an authorisation. Whether this particular
// leave is allowed is settled when the signed event arrives, because until then the
// template describes no event at all.
func (s *Service) ServeMakeLeave(ctx context.Context, roomID, userID, peer string) (matrix.Object, error) {
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
		prevIDs  []string
		authIDs  []string
		depth    int64
		unsigned matrix.Object
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
		key := userID
		authIDs, err = selectAuthEvents(persist.NewStateRepo(tx), &EventDraft{
			RoomID:   roomID,
			Type:     matrix.TypeMember,
			StateKey: &key,
			Sender:   userID,
			Content:  matrix.Encode(matrix.Object{"membership": matrix.MembershipLeave}),
		}, version)
		if err != nil {
			return err
		}
		// As with make_join: the room's server knows what the leave replaces, and a
		// departure a client cannot see the difference from is a departure nobody can
		// explain - a user who left, one whose invite was withdrawn, and one who was
		// knocked out of a knock room all arrive as membership leave.
		if previous, err := persist.NewStateRepo(tx).Get(roomID, matrix.TypeMember, userID); err == nil {
			unsigned = matrix.Object{}
			var before matrix.Object
			if err := matrix.DecodeContent(matrix.RawMessage(previous.Content), &before); err == nil && len(before) > 0 {
				unsigned["prev_content"] = before
			}
			if previous.Sender != "" {
				unsigned["prev_sender"] = previous.Sender
			}
			if len(unsigned) == 0 {
				unsigned = nil
			}
		} else if !persist.IsNotFound(err) {
			return matrix.ErrInternal(err)
		}
		return nil
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
		"content":          matrix.Object{"membership": matrix.MembershipLeave},
	}
	if unsigned != nil {
		template["unsigned"] = unsigned
	}
	return matrix.Object{"room_version": version, "event": template}, nil
}

// ServeSendLeave takes a leave event a server signed for a room this server hosts, and
// decides whether the room accepts it. Unlike a join there is no state to hand back:
// the leaving server already has the room, and what it wanted was for the room's server
// to agree that its user has gone.
func (s *Service) ServeSendLeave(ctx context.Context, roomID, eventID string, event matrix.Object, peer string) (matrix.Object, error) {
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no room %s", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	version := RoomVersionOf(room)
	sender, problem := membershipForEndpoint(event, eventID, roomID, "send_leave", matrix.MembershipLeave)
	if problem != nil {
		return nil, problem
	}
	if usesDerivedEventIDs(version) {
		derived, err := eventIDOfLeave(event, version)
		if err != nil {
			return nil, matrix.ErrInvalidRequest("the event cannot be named: %v", err)
		}
		if derived != eventID {
			return nil, matrix.ErrInvalidRequest("the path names %s and the event hashes to %s", eventID, derived)
		}
	}
	judged, reason := s.judgeIncomingPDU(ctx, withEventID(event, eventID), version)
	if reason != "" {
		return nil, matrix.ErrUnauthorized("the leave sent for %s is not one this server can accept: %s", roomID, reason)
	}
	// Whether the room accepts the leave is the same question the ordinary event path
	// asks - is this event part of this room's graph, is its auth chain whole, do the
	// room's rules allow it - asked here so the server that sent it is told the answer
	// instead of having it recorded against an event it never asked about.
	outcome, reason, err := s.applyIncomingPDU(ctx, judged, version, peer, eventID)
	if err != nil {
		return nil, err
	}
	switch outcome {
	case OutcomeRejected:
		return nil, matrix.ErrForbidden("this room refuses the leave of %s: %s", sender, reason)
	case OutcomeStored, OutcomeSoftFailed:
		stored, err := s.db.EventRepo.Get(eventID)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		// The room's server speaks for the leaving user here: everyone else in the room
		// has to be told, or they keep a member who has gone.
		s.queueEventForPeersExcept(ctx, stored, version, peer)
	}
	return matrix.Object{}, nil
}

// membershipForEndpoint checks the shape a membership handshake endpoint insists on: an
// m.room.member event of the membership the endpoint is named for, in the room the path
// names, under the id the path names. It answers with the state key, the user the event is
// about.
//
// Join, leave and knock are about the sender: a kick is not sent this way - a kick is an
// ordinary event from the kicking server - so a state key that is not the sender's is
// answered as the misuse it is. An invitation is the exception, and the other way round: the
// inviter signs an event whose state key is the user being invited, and an invitation to the
// inviter themselves is the misuse there.
func membershipForEndpoint(event matrix.Object, eventID, roomID, endpoint, want string) (string, error) {
	if federation.EventField(event, "type") != matrix.TypeMember {
		return "", matrix.ErrInvalidRequest("%s takes an m.room.member event, not %s",
			endpoint, federation.EventField(event, "type"))
	}
	key, hasKey := event["state_key"].(string)
	sender := federation.EventField(event, "sender")
	if !hasKey {
		return "", matrix.ErrInvalidRequest("the m.room.member event sent to %s has no state key", endpoint)
	}
	if want == matrix.MembershipInvite {
		if key == sender {
			return key, matrix.ErrInvalidRequest("%s used %s to invite themselves to the room", sender, endpoint)
		}
	} else if key != sender {
		return key, matrix.ErrInvalidRequest("%s cannot use %s to change the membership of %s", sender, endpoint, key)
	}
	if got := federation.EventField(event, "room_id"); got != roomID {
		return key, matrix.ErrInvalidRequest("the event is for room %s, not %s", got, roomID)
	}
	content := objectOf(event, "content")
	if membership, _ := content["membership"].(string); membership != want {
		return key, matrix.ErrInvalidRequest("%s takes a %s, not a %s", endpoint, want, membership)
	}
	if got := federation.EventField(event, "event_id"); got != "" && got != eventID {
		return key, matrix.ErrInvalidRequest("the path names %s and the event names %s", eventID, got)
	}
	return key, nil
}

// LeaveFederatedRoom tells the room's own server that a local user has left, for a room
// this server does not host. The leave is asked for at a server the room is already
// talked to, signed here, and handed to send_leave; the room's answer is not state but
// agreement, so the event is applied locally as any other accepted event is.
func (s *Service) LeaveFederatedRoom(ctx context.Context, roomID, userID, reason string) error {
	transport := s.federationTransport()
	if transport == nil {
		return matrix.ErrNotFound("this server holds no room %s, and does not speak to other servers", roomID)
	}
	var lastErr error
	for _, server := range s.leaveServers(ctx, roomID) {
		if err := s.leaveViaServer(ctx, transport, roomID, userID, reason, server); err != nil {
			lastErr = err
			log.Printf("service: leaving %s through %s: %v", roomID, server, err)
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no server this server talks to holds room %s", roomID)
	}
	return fmt.Errorf("none of the servers in room %s would take the leave of %s: %w", roomID, userID, lastErr)
}

// leaveServers names the servers a leave has to be offered to: the room's own server
// first, since it is authoritative, then the servers the room is currently relayed to,
// since they are the ones with a copy to update.
func (s *Service) leaveServers(ctx context.Context, roomID string) []string {
	servers, err := s.destinationsOfRoomIgnoringACL(ctx, roomID)
	if err != nil {
		servers = nil
	}
	return joinServers(roomID, servers)
}

// leaveViaServer performs the make_leave/send_leave handshake with one peer.
func (s *Service) leaveViaServer(ctx context.Context, transport *federation.Transport, roomID, userID, reason, server string) error {
	proposal, version, err := s.requestMakeLeave(ctx, transport, roomID, userID, server)
	if err != nil {
		return err
	}
	if !s.roomVersionIsSupported(version) {
		return fmt.Errorf("the room is version %s, which this server cannot speak", version)
	}
	proto, _ := proposal["event"].(matrix.Object)
	if proto == nil {
		return fmt.Errorf("%s answered make_leave with no event template", server)
	}
	depth := numberField(proto, "depth")
	prevIDs := idsOfPDUList(proto, "prev_events")
	authIDs := idsOfPDUList(proto, "auth_events")
	content := matrix.Object{"membership": matrix.MembershipLeave}
	if reason != "" {
		content["reason"] = reason
	}
	key := userID
	draft := &EventDraft{
		RoomID:   roomID,
		Type:     matrix.TypeMember,
		StateKey: &key,
		Sender:   userID,
		Content:  matrix.Encode(content),
	}
	packet, err := s.buildRemotePDU(draft, version, s.NowMS(), depth, prevIDs, authIDs,
		objectOf(proto, "unsigned"))
	if err != nil {
		return err
	}
	if err := s.SignAndIdentify(ctx, packet); err != nil {
		return err
	}
	if err := s.sendLeave(ctx, transport, server, roomID, packet); err != nil {
		return err
	}
	// The room's server has agreed. Applying the event here is what makes the leave
	// count: membership, the graph, and every other peer's copy all move because of it,
	// and an acknowledgement that stopped at the database row would leave a room whose
	// members are half informed.
	outcome, why, err := s.applyIncomingPDU(ctx, packet.object, version, server, packet.eventID)
	if err != nil {
		return err
	}
	if outcome == OutcomeRejected {
		return fmt.Errorf("this server's own room refuses the leave it just sent: %s", why)
	}
	return nil
}

// requestMakeLeave asks a server for the leave template of a room. Version 1 is the
// endpoint every server serves; version 2 only adds the room version, which version 1
// answers with anyway.
func (s *Service) requestMakeLeave(ctx context.Context, transport *federation.Transport, roomID, userID, server string) (matrix.Object, string, error) {
	path := fmt.Sprintf("/_matrix/federation/v1/make_leave/%s/%s",
		url.PathEscape(roomID), url.PathEscape(userID))
	body, err := transport.Get(ctx, server, path, nil)
	if err != nil {
		return nil, "", err
	}
	version, _ := body["room_version"].(string)
	if version == "" {
		return nil, "", fmt.Errorf("%s answered make_leave with no room version", server)
	}
	return body, version, nil
}

// sendLeave hands the signed leave to the server that proposed it. Version 2 is tried
// first; version 1, which some servers still answer with a two element array, is the
// fallback.
func (s *Service) sendLeave(ctx context.Context, transport *federation.Transport, server, roomID string, packet *pdu) error {
	for _, version := range []string{"v2", "v1"} {
		path := fmt.Sprintf("/_matrix/federation/%s/send_leave/%s/%s",
			version, url.PathEscape(roomID), url.PathEscape(packet.eventID))
		if _, err := transport.Put(ctx, server, path, packet.object); err != nil {
			if isUnrecognized(err) && version == "v2" {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("%s serves no send_leave this server understands", server)
}

// eventIDOfLeave is the id a leave of a room version with derived ids is named by, which
// send_leave has to be told about in its path.
func eventIDOfLeave(event matrix.Object, version string) (string, error) {
	if !usesDerivedEventIDs(version) {
		if id, ok := event["event_id"].(string); ok {
			return id, nil
		}
		return "", fmt.Errorf("a room version %s event has to name itself", version)
	}
	return eventcrypto.EventID(event, version)
}
