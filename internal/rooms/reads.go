package rooms

import (
	"context"
	"fmt"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/stream"
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
	asOf, err := s.readHorizon(ctx, roomID, requester)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if asOf > 0 {
			stored, err := s.events.ByID(ctx, row.EventID)
			if err != nil {
				return nil, err
			}
			if stored.StreamOrdering > asOf {
				continue
			}
		}
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
	asOf, err := s.readHorizon(ctx, roomID, requester)
	if err != nil {
		return nil, false, err
	}
	if asOf > 0 {
		stored, err := s.events.ByID(ctx, row.EventID)
		if err != nil {
			return nil, false, err
		}
		if stored.StreamOrdering > asOf {
			// The room has state under this key, but not state this requester is
			// entitled to: it was written after they left.
			return nil, false, nil
		}
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

// topicContent builds an m.room.topic content object from a plain topic. The
// specification duplicates the plain text into the structured `m.topic` field so that a
// client can render a topic without guessing whether it is formatted. Only the shorthand
// parameter does this: a client that sends its own m.room.topic event in initial_state
// keeps exactly the content it wrote, because rewriting a client's own event would be a
// server editing what the client signed.
func topicContent(topic string) map[string]any {
	return map[string]any{
		"topic": topic,
		"m.topic": map[string]any{
			"m.text": []any{map[string]any{
				"body":     topic,
				"mimetype": "text/plain",
			}},
		},
	}
}

// deduplicateState keeps one entry per state key, with the last one written winning, so
// that creating a room does not write the same key twice and leave the earlier version
// as a pointless step in the room's history.
func deduplicateState(list []StateContent) []StateContent {
	positions := make(map[string]int, len(list))
	out := make([]StateContent, 0, len(list))
	for _, entry := range list {
		key := entry.Type + "\x00" + entry.StateKey
		if at, seen := positions[key]; seen {
			out[at] = entry
			continue
		}
		positions[key] = len(out)
		out = append(out, entry)
	}
	return out
}

// MemberFilter narrows a member list. Include keeps one membership, Exclude drops
// one; the Client-Server API needs both because `not_membership` excludes while a
// "who is here" view selects.
//
// At is a sync token: the roster is then the one this server's history held at that
// position rather than the current one.
type MemberFilter struct {
	Include string
	Exclude string
	At      string
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
	asOf, err := s.readHorizon(ctx, roomID, requester)
	if err != nil {
		return nil, err
	}
	// `at` narrows the list to what had happened by a token the client already holds. The
	// membership read here is the current one cut at that position, which answers the question
	// clients actually ask with it — who had joined by then — and gets the interesting case
	// wrong in one direction only: a member who joined before the position and left after it is
	// missing, because this server's current state no longer holds them. Reconstructing the
	// roster as it was needs the state snapshot the event at that position was sent against,
	// which is recorded per event and is the same lookup `/context` and the membership-on-events
	// question need; it is deliberately not approximated here.
	capped := asOf > 0
	if filter.At != "" {
		position, err := stream.Parse(filter.At)
		if err != nil {
			return nil, matrixerr.New(matrixerr.CodeInvalidParam,
				"at is not a synchronisation token this server issued.")
		}
		if !capped || int64(position) < asOf {
			asOf = int64(position)
		}
		capped = true
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if capped {
			stored, err := s.events.ByID(ctx, row.EventID)
			if err != nil {
				return nil, err
			}
			// A member who joined after the requester left is not part of the roster the
			// requester ever saw.
			if stored.StreamOrdering > asOf {
				continue
			}
		}
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
	// membershipAny accepts anyone with a stake in the room: a current member, a former
	// member reading the history they were part of, or any user when the room made its
	// history world readable.
	membershipAny membershipRequirement = iota
	// membershipJoined accepts only a joined member, which is what a "who is here right
	// now" view needs.
	membershipJoined
	// membershipStrict accepts only a current member, with no history-visibility
	// exception. A room's names say that it exists and what it is called; making its
	// history world readable is a decision about its history, not an announcement of its
	// names.
	membershipStrict
)

// readHorizon returns the stream position a requester's view of a room stops at, or zero
// when their view is current.
//
// A former member's entitlement ends when their membership does. This server keeps only
// the room's current state, not a snapshot per event, so it cannot reconstruct the value
// a key had at that moment. What it can do is withhold every entry that changed
// afterwards, which is the part a former member has no claim to. That is a partial
// answer — a key that was replaced after the leave reads as absent rather than as its old
// value — and it is recorded as such; the alternative, answering with the current value,
// would hand over state written after the requester left.
func (s *Service) readHorizon(ctx context.Context, roomID, requester string) (int64, error) {
	membership, err := s.MembershipOf(ctx, roomID, requester)
	if err != nil {
		return 0, err
	}
	switch membership {
	case eventauth.MembershipLeave, eventauth.MembershipBan:
		row, err := s.state.Get(ctx, roomID, eventauth.TypeMember, requester)
		if err != nil {
			if notFound(err) {
				return 0, nil
			}
			return 0, err
		}
		event, err := s.events.ByID(ctx, row.EventID)
		if err != nil {
			if notFound(err) {
				return 0, nil
			}
			return 0, err
		}
		return event.StreamOrdering, nil
	default:
		return 0, nil
	}
}

// requireMembership refuses a room read for someone with no standing in the room.
//
// A room's state and member list say who is talking and what was said, so the gate is the
// room's membership rather than the mere existence of an account. Three cases follow from
// that and from what the specification's history visibility means:
//
//   - a current member (joined, invited or knocking) may read: an invited user cannot
//     accept an invitation without seeing what they are accepting;
//   - a former member may read the state and member list of a room they left or were
//     banned from — the history they took part in stays theirs, and this is the behaviour
//     Complement pins as SPEC-216 — but not the live roster;
//   - someone who was never in the room may read only what the room itself opened up by
//     setting `history_visibility: world_readable`.
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
		if requirement != membershipJoined {
			return nil
		}
	case eventauth.MembershipLeave, eventauth.MembershipBan:
		if requirement == membershipAny {
			return nil
		}
	default:
		// No membership at all: only a room that opened its history may be read.
		if requirement == membershipAny {
			visibility, err := s.historyVisibility(ctx, roomID)
			if err == nil && visibility == "world_readable" {
				return nil
			}
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

// eventJSON reads a stored event back as the protocol object, in the form a client is
// entitled to see: the redacted form when a redaction was applied, the `redacts` field in
// the place the client's room version expects, and — for a redacted event — a copy of the
// redaction that did it under `unsigned.redacted_because`, which is how a client learns
// why an event it once read is now empty.
func (s *Service) eventJSON(ctx context.Context, eventID string) (map[string]any, error) {
	row, err := s.events.ByID(ctx, eventID)
	if err != nil {
		if notFound(err) {
			return nil, fmt.Errorf("rooms: state points at event %s, which is not stored", eventID)
		}
		return nil, err
	}
	return s.serveEvent(ctx, row)
}

// serveEvent is eventJSON over a row already in hand. The decision about what a client may
// see is the redaction servicer's, not this package's: `/messages`, `/event` and `/sync` all
// go through the same one.
func (s *Service) serveEvent(ctx context.Context, row *persist.Event) (map[string]any, error) {
	return s.serve.Serve(ctx, row)
}
