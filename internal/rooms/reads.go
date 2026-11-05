package rooms

import (
	"context"
	"fmt"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
)

// CurrentState returns the room's current state events, as stored.
//
// The events are returned in the form the protocol defines them, because a client
// compares state by event content and a server cannot rewrite an event without
// invalidating its hash and signature.
func (s *Service) CurrentState(ctx context.Context, roomID, requester string) ([]map[string]any, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipAny); err != nil {
		return nil, err
	}
	rows, err := s.state.All(ctx, roomID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		event, err := s.eventJSON(ctx, row.EventID)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, nil
}

// CurrentStateEvent returns one current state entry. The boolean reports whether the
// room has state for that (type, state key) pair at all, which a handler needs in
// order to answer 404 rather than an empty object.
func (s *Service) CurrentStateEvent(ctx context.Context, roomID, eventType, stateKey, requester string) (map[string]any, bool, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipAny); err != nil {
		return nil, false, err
	}
	row, err := s.state.Get(ctx, roomID, eventType, stateKey)
	if err != nil {
		if notFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	event, err := s.eventJSON(ctx, row.EventID)
	if err != nil {
		return nil, false, err
	}
	return event, true, nil
}

// JoinedMembers returns the room's joined members, for a requester who is themselves
// joined.
//
// The stricter requirement is not decoration: "who is in this room right now" is the
// question a client asks in order to talk to them. Someone who was invited and never
// joined, or who left, has no claim to that list.
func (s *Service) JoinedMembers(ctx context.Context, roomID, requester string) ([]map[string]any, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipJoined); err != nil {
		return nil, err
	}
	return s.Members(ctx, roomID, requester, MemberFilter{Include: eventauth.MembershipJoin})
}

// MemberFilter narrows a member list. Include keeps one membership, Exclude drops
// one; the Client-Server API needs both because `not_membership` excludes while a
// "who is here" view selects.
type MemberFilter struct {
	Include string
	Exclude string
}

// Members returns the room's current m.room.member events.
func (s *Service) Members(ctx context.Context, roomID, requester string, filter MemberFilter) ([]map[string]any, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipAny); err != nil {
		return nil, err
	}
	rows, err := s.state.ByType(ctx, roomID, eventauth.TypeMember)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		event, err := s.eventJSON(ctx, row.EventID)
		if err != nil {
			return nil, err
		}
		if filter.Include != "" || filter.Exclude != "" {
			content, _ := event["content"].(map[string]any)
			membership, _ := content["membership"].(string)
			if filter.Include != "" && membership != filter.Include {
				continue
			}
			if filter.Exclude != "" && membership == filter.Exclude {
				continue
			}
		}
		out = append(out, event)
	}
	return out, nil
}

// JoinedRooms lists the rooms where the user's current membership is join.
//
// It reads the member state that names the user rather than scanning every room, which
// is the difference between work proportional to the user and work proportional to the
// server.
func (s *Service) JoinedRooms(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.state.MembershipsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		event, err := s.eventJSON(ctx, row.EventID)
		if err != nil {
			return nil, err
		}
		content, _ := event["content"].(map[string]any)
		if membership, _ := content["membership"].(string); membership == eventauth.MembershipJoin {
			out = append(out, row.RoomID)
		}
	}
	return out, nil
}

// membershipRequirement says how strict a read is about who asks.
type membershipRequirement int

const (
	// membershipAny accepts anyone with a current membership: joined, invited or
	// knocking. A banned user is not in the room for this purpose.
	membershipAny membershipRequirement = iota
	// membershipJoined accepts only a joined member, which is what a "who is here"
	// view needs.
	membershipJoined
)

// requireMembership refuses a room read for someone with no standing in the room.
//
// A room's state and member list say who is talking and what was said. Letting any
// authenticated account read any room would turn a homeserver into a public transcript
// of private rooms, which is why the room's membership, not the account's existence, is
// the gate.
func (s *Service) requireMembership(ctx context.Context, roomID, requester string, requirement membershipRequirement) error {
	if _, err := s.requireRoom(ctx, roomID); err != nil {
		return err
	}
	membership, err := s.MembershipOf(ctx, roomID, requester)
	if err != nil {
		return err
	}
	switch membership {
	case eventauth.MembershipJoin:
		return nil
	case eventauth.MembershipInvite, eventauth.MembershipKnock:
		if requirement == membershipAny {
			return nil
		}
	}
	return matrixerr.New(matrixerr.CodeForbidden,
		"You are not a member of this room, so this server will not show its state.")
}

// requireRoom turns a missing room into the protocol error, so a read of an unknown
// room cannot look like a room with empty state.
func (s *Service) requireRoom(ctx context.Context, roomID string) (*persist.Room, error) {
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return nil, matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", roomID)
		}
		return nil, fmt.Errorf("rooms: cannot load the room: %w", err)
	}
	return room, nil
}

// eventJSON reads a stored event back as the protocol object.
func (s *Service) eventJSON(ctx context.Context, eventID string) (map[string]any, error) {
	row, err := s.events.ByID(ctx, eventID)
	if err != nil {
		if notFound(err) {
			return nil, fmt.Errorf("rooms: state points at event %s, which is not stored", eventID)
		}
		return nil, err
	}
	event, err := events.Parse(row.JSON)
	if err != nil {
		return nil, fmt.Errorf("rooms: stored event %s does not parse: %w", eventID, err)
	}
	return event, nil
}
