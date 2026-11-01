package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/push"
)

// notifyParticipants evaluates the push rules of every joined participant and
// records notifications. It runs after an event is committed: failing to
// notify must never fail the send itself, so errors are logged by the caller.
//
// The matching itself is pure and lives in internal/push; this function only
// gathers what the rules need - the rules of each recipient, the body of the
// event, the display names of the room and its size.
func (s *Service) notifyParticipants(ctx context.Context, roomID string, event *persist.Event) error {
	switch event.Type {
	case matrix.TypeMessage, matrix.TypeEncrypted, matrix.TypeSticker:
	default:
		return nil
	}
	members, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return err
	}
	memberCount := len(members)
	content := matrix.MustParseObject([]byte(event.Content))
	body, _ := content["body"].(string)
	// Rules match against the recipient's display name, so the names of the whole
	// room are read once instead of one profile per member.
	recipients := make([]string, 0, len(members))
	for _, member := range members {
		recipients = append(recipients, member.UserID)
	}
	profiles, err := s.profileSummaries(ctx, recipients)
	if err != nil {
		return err
	}

	for _, member := range members {
		if member.UserID == event.Sender {
			continue
		}
		rules, err := s.db.PushRuleRepo.List(member.UserID, "")
		if err != nil {
			return err
		}
		rules = push.OrderRules(rules)
		displayName := profiles[member.UserID].DisplayName
		localpart := matrix.LocalpartOf(member.UserID)
		var matched *persist.PushRule
		for _, rule := range rules {
			if !rule.Enabled {
				continue
			}
			if push.Matches(rule, event, content, body, displayName, localpart, memberCount) {
				matched = rule
				break
			}
		}
		if matched == nil {
			continue
		}
		actions := toAnySlice(matched.Actions)
		if !push.Notifies(actions) {
			continue
		}
		notification := &persist.Notification{
			UserID:      member.UserID,
			RoomID:      roomID,
			EventID:     event.EventID,
			TS:          event.OriginServerTS,
			StreamPos:   event.StreamPos,
			Actions:     matched.Actions,
			Highlighted: push.HasTweak(actions, "highlight"),
			Notified:    true,
		}
		if push.HasTweak(actions, "count") {
			notification.CountTweak = "count"
		}
		if err := s.db.NotificationRepo.Create(notification); err != nil {
			return err
		}
	}
	return nil
}
