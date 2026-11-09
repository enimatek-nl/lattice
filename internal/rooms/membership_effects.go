package rooms

import (
	"context"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixid"
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

// applySideEffects looks up a just-persisted event and applies what follows from it that
// the event itself does not say.
func (s *Service) applySideEffects(ctx context.Context, eventID string) {
	stored, err := s.db.Events.ByID(ctx, eventID)
	if err != nil {
		// The event committed, so this is a read failure after the fact. Not worth
		// failing the join over, and worth a line in the log.
		s.log.Warn("rooms: cannot read a persisted event to announce its side effects",
			"event_id", eventID, "error", err)
		return
	}
	body, err := events.Parse(stored.Serving())
	if err != nil {
		s.log.Warn("rooms: cannot parse a persisted event to announce its side effects",
			"event_id", eventID, "error", err)
		return
	}
	eventType, _ := body["type"].(string)
	content, _ := body["content"].(map[string]any)
	roomID, _ := body["room_id"].(string)
	if roomID == "" {
		return
	}

	// Every event this server accepts is offered to the other servers in the room. A
	// soft-failed or outlier event is not: the first is one this server has decided not
	// to relay, and the second has no resolved place in the history to relay it from.
	if s.relayer != nil && !stored.SoftFailed && !stored.Outlier {
		if err := s.relayer.Relay(ctx, roomID, eventID); err != nil {
			s.log.Warn("rooms: cannot queue an event for the other servers in the room",
				"room_id", roomID, "event_id", eventID, "error", err)
		}
	}

	// A tombstone has an empty state key, so the member check below must not run first.
	if eventType == tombstoneEventType {
		if stateKey, _ := body["state_key"].(string); stateKey == "" {
			s.announceUpgrade(ctx, roomID, content)
		}
		return
	}

	stateKey, _ := body["state_key"].(string)
	if stateKey == "" {
		return
	}
	switch content["membership"] {
	case eventauth.MembershipJoin:
		s.announceArrival(ctx, roomID, stateKey)
	case eventauth.MembershipLeave, eventauth.MembershipBan:
		s.announceDeparture(ctx, roomID, stateKey)
	}
}

// announceDeparture tells a room that a member is gone, and tells the member that the room
// is gone from their view.
//
// The second half is the one a room-shaped model misses. Somebody who leaves must be told
// that the people still in that room are no longer theirs to track — otherwise their client
// keeps fetching keys for a stranger — and they are no longer in the room to read a notice
// left there. So the change is written for each observer separately.
func (s *Service) announceDeparture(ctx context.Context, roomID, gone string) {
	members, err := s.db.State.CurrentMembers(ctx, roomID)
	if err != nil {
		s.log.Warn("rooms: cannot read the members of a room to announce a departure",
			"room_id", roomID, "error", err)
		return
	}
	for _, member := range members {
		if member == gone {
			continue
		}
		s.recordDeviceListChange(ctx, roomID, member, gone, persist.DeviceListLeft)
		s.recordDeviceListChange(ctx, roomID, gone, member, persist.DeviceListLeft)
	}
}

// announceArrival tells a room about a newcomer, and the newcomer about the room.
//
// Both directions matter, and it is easy to do only one. The members are told that somebody
// new arrived; the joiner is told about the people already there, whose presence they can
// now see and whose keys they should now fetch. A server that announces only the joiner
// leaves the newcomer knowing nobody is there.
func (s *Service) announceArrival(ctx context.Context, roomID, joiner string) {
	members, err := s.db.State.CurrentMembers(ctx, roomID)
	if err != nil {
		s.log.Warn("rooms: cannot read the members of a room to announce an arrival",
			"room_id", roomID, "error", err)
		return
	}
	for _, member := range members {
		if member == joiner {
			continue
		}
		// The member's presence and keys are the joiner's to know about...
		s.announceUser(ctx, roomID, member)
		// ...and the joiner's are the members'.
		s.recordDeviceListChange(ctx, roomID, member, joiner, persist.DeviceListChanged)
	}
	// The joiner's own other devices are told too, and they are not in the loop above, which
	// skips the joiner. From their point of view this user just became somebody who shares a
	// room with new servers, which is one of the two things that makes a device list worth
	// re-publishing; a client on a phone and a client on a laptop are separate verifiers of each
	// other, and neither learns about the other from a timeline.
	if domain, err := matrixid.UserDomain(joiner); err == nil && matrixid.IsLocal(domain, s.name) {
		s.recordDeviceListChange(ctx, roomID, joiner, joiner, persist.DeviceListChanged)
	}
	s.announceUser(ctx, roomID, joiner)
}

// announceUser gives one user a presence position, and tells the room that this user's keys
// are worth re-reading.
func (s *Service) announceUser(ctx context.Context, roomID, userID string) {
	if s.presence != nil {
		if err := s.presence.Announce(ctx, userID); err != nil {
			s.log.Warn("rooms: cannot announce a user's presence",
				"room_id", roomID, "user_id", userID, "error", err)
		}
	}
	members, err := s.db.State.CurrentMembers(ctx, roomID)
	if err != nil {
		s.log.Warn("rooms: cannot read the members of a room to announce a device list change",
			"room_id", roomID, "error", err)
		return
	}
	for _, member := range members {
		if member == userID {
			// Somebody's arrival is not news to the person who arrived, and this is not the
			// self-notification that a device list needs: the exception for a joiner is written
			// once, deliberately, in [Service.announceArrival]. Written here it would fire for
			// every member of the room on every join, and a user would find their own name in
			// `device_lists.changed` each time somebody else walked in.
			continue
		}
		s.recordDeviceListChange(ctx, roomID, member, userID, persist.DeviceListChanged)
	}

	// A user who has just joined from here is a new fact for the other servers in the room:
	// their clients will be told about this user's devices by us, and nothing else prompts
	// that. The local recording above says nothing to a server on the other side of the room.
	if domain, err := matrixid.UserDomain(userID); s.deviceListAnnouncer != nil && err == nil &&
		matrixid.IsLocal(domain, s.name) {
		s.deviceListAnnouncer.AnnounceDeviceListsToFederation(ctx, userID)
	}
}

// recordDeviceListChange notes, for one observer, that another user's device list should be
// re-read or is no longer worth keeping.
func (s *Service) recordDeviceListChange(ctx context.Context, roomID, observerID, userID, kind string) {
	if observerID == userID && kind != persist.DeviceListChanged {
		// Someone who left is not told, in a room they are no longer in, about the people they
		// can no longer see. A change to a user's *own* device list, on the other hand, is
		// precisely the news that user's remaining devices need: a second device that appears
		// is a device the first one will otherwise never encrypt to, and a client cannot be told
		// to re-read its own user's keys by any other route, because the change is not in any
		// room's timeline. This is the rule the specification gives for `device_lists.changed`,
		// and the reason a user appears in their own list after they join a room.
		return
	}
	if _, err := s.db.DeviceLists.Record(ctx, roomID, observerID, userID, kind); err != nil {
		s.log.Warn("rooms: cannot record a device list change",
			"room_id", roomID, "observer_id", observerID, "user_id", userID, "kind", kind, "error", err)
	}
}
