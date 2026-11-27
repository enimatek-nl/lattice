package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/push"
)

// notifyParticipants evaluates the push rules of everybody an event may wake and records
// notifications. It runs after an event is committed: failing to notify must never fail the
// send itself, so errors are logged by the caller.
//
// The matching itself is pure and lives in internal/push; this function only gathers what the
// rules need - the rules of each recipient, the body of the event, the display names of the
// room and its size, and the power the sender holds in it.
//
// Who is asked: "When receiving a new event homeservers process push rules for each of the
// local users in the room (excluding the sender)". A membership event adds one name to that
// list, because `.m.rule.invite_for_me` "matches any invites to a new room for this user" and
// the person it invites is not in the room yet - the invitation is what puts them in it. A
// server that asks only the joined members therefore has no rule left to read for an invite,
// and an invited user with a pusher sits at a device that never wakes.
func (s *Service) notifyParticipants(ctx context.Context, roomID string, event *persist.Event) error {
	switch event.Type {
	case matrix.TypeMessage, matrix.TypeEncrypted, matrix.TypeSticker, matrix.TypeMember:
	default:
		return nil
	}
	joined, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return err
	}
	// The members whose own event this is are not asked about it; see the loop below.
	recipients := make([]string, 0, len(joined)+1)
	asked := make(map[string]bool, len(joined)+1)
	for _, member := range joined {
		if member.UserID == event.Sender || asked[member.UserID] {
			continue
		}
		asked[member.UserID] = true
		recipients = append(recipients, member.UserID)
	}
	if event.Type == matrix.TypeMember && event.StateKey != "" {
		// The subject of a membership event is a recipient even when this server holds no
		// membership row for them in this room, which is exactly the state of an invitee.
		// Joins, leaves, knocks and bans travel the same road and stay quiet: the default
		// rules match them with `.m.rule.member_event`, whose action list is empty.
		if target := event.StateKey; target != event.Sender && s.LocalUser(target) && !asked[target] {
			recipients = append(recipients, target)
		}
	}
	if len(recipients) == 0 {
		return nil
	}
	// The size a `room_member_count` condition compares against is the room's, joined
	// members only, whether or not this event is about one of them.
	memberCount := len(joined)
	content := matrix.MustParseObject([]byte(event.Content))
	body, _ := content["body"].(string)
	// Rules match against the recipient's display name, so the names of the whole
	// room are read once instead of one profile per member.
	profiles, err := s.profileSummaries(ctx, recipients)
	if err != nil {
		return err
	}
	// An `@room` mention only notifies when the sender may make one, so the sender's
	// level and the room's notification levels are read once for the whole room.
	senderLevel, notificationLevels := s.senderPower(ctx, roomID, event.Sender)
	// Who this event may wake, with the actions that said so. The gateway requests are
	// rendered at the end, once the notification rows exist - so the unread counts the
	// body carries include what was just written.
	var notified []pushRecipient

	for _, recipient := range recipients {
		rules, err := s.db.PushRuleRepo.List(recipient, "")
		if err != nil {
			return err
		}
		rules = push.OrderRules(rules)
		ruleContext := &push.Context{
			Event:         event,
			Content:       content,
			Body:          body,
			DisplayName:   profiles[recipient].DisplayName,
			Localpart:     matrix.LocalpartOf(recipient),
			MemberCount:   memberCount,
			SenderLevel:   senderLevel,
			Notifications: notificationLevels,
		}
		var matched *persist.PushRule
		for _, rule := range rules {
			if !rule.Enabled {
				continue
			}
			if push.Matches(rule, ruleContext) {
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
			UserID:      recipient,
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
		notified = append(notified, pushRecipient{UserID: recipient, Actions: actions})
	}
	// Registered endpoints come last and never fail the send: a push gateway that is
	// down is not a reason for a message to go unsent.
	s.queuePushesForEvent(ctx, roomID, event, notified)
	return nil
}
