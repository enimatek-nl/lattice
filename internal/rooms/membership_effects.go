package rooms

import (
	"context"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/persist"
)

// What a membership change means beyond itself.
//
// Two things follow from a join or a leave that the membership event does not say, and a
// client cannot work out for itself:
//
//   - Presence. A room is what makes one user's presence another user's business, so an
//     arrival gives the joiner and the members already there a new presence position: the
//     members are told about the newcomer, and the newcomer is told about them.
//   - Device lists. A user joining is a user whose keys are worth fetching, and a user
//     leaving is a user whose keys are no longer worth keeping. A client learns this from
//     `device_lists.changed` and `device_lists.left` in its sync, so the change has to be
//     recorded with a position in the same stream.
//
// Both happen after the membership event committed. The presence service and these writes
// go through the connection pool, and a write attempted while a transaction holds the
// single writer waits for the connection that transaction is holding: a deadlock, not a
// delay.
//
// Neither can fail the join. A user whose presence was not announced, or whose keys will be
// re-fetched a moment later by a client that notices on its own, is a smaller fault than a
// join that worked and reported failure.

// announceMembership looks up a just-persisted event and applies the side effects of a
// membership change.
func (s *Service) announceMembership(ctx context.Context, eventID string) {
	if s.presence == nil {
		return
	}
	stored, err := s.db.Events.ByID(ctx, eventID)
	if err != nil {
		// The event committed, so this is a read failure after the fact. Not worth
		// failing the join over, and worth a line in the log.
		s.log.Warn("rooms: cannot read a persisted event to announce a membership change",
			"event_id", eventID, "error", err)
		return
	}
	body, err := events.Parse(stored.JSON)
	if err != nil {
		s.log.Warn("rooms: cannot parse a persisted event to announce a membership change",
			"event_id", eventID, "error", err)
		return
	}
	if body["type"] != eventauth.TypeMember {
		return
	}
	content, _ := body["content"].(map[string]any)
	membership, _ := content["membership"].(string)
	stateKey, _ := body["state_key"].(string)
	roomID, _ := body["room_id"].(string)
	if stateKey == "" || roomID == "" {
		return
	}

	switch membership {
	case eventauth.MembershipJoin:
		s.announceArrival(ctx, roomID, stateKey)
		s.recordDeviceListChange(ctx, roomID, stateKey, persist.DeviceListChanged)
	case eventauth.MembershipLeave, eventauth.MembershipBan:
		s.recordDeviceListChange(ctx, roomID, stateKey, persist.DeviceListLeft)
	}
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

// recordDeviceListChange notes that a user's device list should be re-read, or that it is no
// longer worth keeping.
func (s *Service) recordDeviceListChange(ctx context.Context, roomID, userID, kind string) {
	if _, err := s.db.DeviceLists.Record(ctx, roomID, userID, kind); err != nil {
		s.log.Warn("rooms: cannot record a device list change",
			"room_id", roomID, "user_id", userID, "kind", kind, "error", err)
	}
}
