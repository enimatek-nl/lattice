package rooms

import (
	"context"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
)

// Announcing presence when somebody joins.
//
// A room is what makes one user's presence another user's business: the Client-Server API
// reports presence for users you share a room with. So the moment a user arrives, two
// things have to happen for the room's presence to be right:
//
//   - the arrival itself becomes news, so the joiner's presence gets a new stream
//     position and the members already there are told about them;
//   - the members already there become news to the joiner, so their presence gets a new
//     position too, and the joiner's next sync carries it.
//
// Both are announcements rather than writes of a new status: what a user's status *is*
// stays theirs to set, and this only says that somebody now has a reason to see it.
//
// These calls happen after the membership event committed. The presence service writes
// through the connection pool, and a write attempted while a transaction holds the single
// writer waits for the connection that transaction is holding — a deadlock, not a delay.

// announceJoin looks up a just-persisted event and, if it is a local user joining,
// announces them and the room's other local members.
func (s *Service) announceJoin(ctx context.Context, eventID string) {
	if s.presence == nil {
		return
	}
	stored, err := s.db.Events.ByID(ctx, eventID)
	if err != nil {
		// The event committed, so this is a read failure after the fact. Presence is
		// not worth failing the join over, and it is worth a line in the log.
		s.log.Warn("rooms: cannot read a persisted event to announce presence",
			"event_id", eventID, "error", err)
		return
	}
	body, err := events.Parse(stored.JSON)
	if err != nil {
		s.log.Warn("rooms: cannot parse a persisted event to announce presence",
			"event_id", eventID, "error", err)
		return
	}
	if body["type"] != eventauth.TypeMember {
		return
	}
	content, _ := body["content"].(map[string]any)
	if content["membership"] != eventauth.MembershipJoin {
		return
	}
	stateKey, _ := body["state_key"].(string)
	roomID, _ := body["room_id"].(string)
	if stateKey == "" || roomID == "" {
		return
	}
	s.announceArrival(ctx, roomID, stateKey)
}

// announceArrival gives the joiner and the room's other local members a new presence
// position.
func (s *Service) announceArrival(ctx context.Context, roomID, joiner string) {
	if s.presence == nil {
		return
	}
	members, err := s.db.State.ByType(ctx, roomID, eventauth.TypeMember)
	if err != nil {
		s.log.Warn("rooms: cannot read the members of a room to announce presence",
			"room_id", roomID, "error", err)
		return
	}
	for _, entry := range members {
		if entry.StateKey == "" || entry.StateKey == joiner {
			continue
		}
		if err := s.presence.Announce(ctx, entry.StateKey); err != nil {
			s.log.Warn("rooms: cannot announce a member's presence",
				"room_id", roomID, "user_id", entry.StateKey, "error", err)
		}
	}
	if err := s.presence.Announce(ctx, joiner); err != nil {
		s.log.Warn("rooms: cannot announce a joiner's presence",
			"room_id", roomID, "user_id", joiner, "error", err)
	}
}
