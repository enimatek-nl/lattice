package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// notifyParticipants evaluates the push rules of every joined participant and
// records notifications. It runs after an event is committed: failing to
// notify must never fail the send itself, so errors are logged by the caller.
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

	for _, member := range members {
		if member.UserID == event.Sender {
			continue
		}
		rules, err := s.db.PushRuleRepo.List(member.UserID, "")
		if err != nil {
			return err
		}
		rules = orderRules(rules)
		displayName := s.displayName(ctx, member.UserID)
		localpart := matrix.LocalpartOf(member.UserID)
		var matched *persist.PushRule
		for _, rule := range rules {
			if !rule.Enabled {
				continue
			}
			if s.ruleMatches(rule, event, content, body, displayName, localpart, memberCount) {
				matched = rule
				break
			}
		}
		if matched == nil {
			continue
		}
		actions := toAnySlice(matched.Actions)
		if !notifies(actions) {
			continue
		}
		notification := &persist.Notification{
			UserID:      member.UserID,
			RoomID:      roomID,
			EventID:     event.EventID,
			TS:          event.OriginServerTS,
			StreamPos:   event.StreamPos,
			Actions:     matched.Actions,
			Highlighted: hasTweak(actions, "highlight"),
			Notified:    true,
		}
		if hasTweak(actions, "count") {
			notification.CountTweak = "count"
		}
		if err := s.db.NotificationRepo.Create(notification); err != nil {
			return err
		}
	}
	return nil
}

// orderRules sorts rules by kind precedence and then by their stored position.
func orderRules(rules []*persist.PushRule) []*persist.PushRule {
	rank := map[string]int{}
	for i, kind := range PushRuleKinds {
		rank[kind] = i
	}
	out := make([]*persist.PushRule, 0, len(rules))
	for _, kind := range PushRuleKinds {
		for _, rule := range rules {
			if rule.Kind == kind {
				out = append(out, rule)
			}
		}
	}
	// Rules of unknown kinds keep their relative order at the end.
	for _, rule := range rules {
		if _, ok := rank[rule.Kind]; !ok {
			out = append(out, rule)
		}
	}
	return out
}

// ruleMatches reports whether a rule fires for an event.
func (s *Service) ruleMatches(rule *persist.PushRule, event *persist.Event, content matrix.Object, body, displayName, localpart string, memberCount int) bool {
	switch rule.RuleID {
	case ".m.rule.master":
		return false
	case ".m.rule.contains_display_name", ".m.rule.contains_user_name":
		// The user name rule matches the localpart of the recipient, the
		// display name rule matches their profile name.
		needle := strings.ToLower(displayName)
		if rule.RuleID == ".m.rule.contains_user_name" || needle == "" {
			needle = strings.ToLower(localpart)
		}
		return needle != "" && wordContains(body, needle)
	case ".m.rule.member_event", ".m.rule.reaction", ".m.rule.notice":
		return false
	}
	if rule.Kind == "room" {
		return rule.RuleID == event.RoomID
	}
	if rule.Kind == "sender" {
		return rule.RuleID == event.Sender
	}
	if rule.Conditions == "" {
		// Rules without conditions match everything, which is what the default
		// underride rule expresses.
		return rule.Kind == "underride"
	}
	var conditions []matrix.Object
	if err := json.Unmarshal([]byte(rule.Conditions), &conditions); err != nil {
		return false
	}
	for _, condition := range conditions {
		kind, _ := condition["kind"].(string)
		switch kind {
		case "event_match":
			key, _ := condition["key"].(string)
			pattern, _ := condition["pattern"].(string)
			value := eventFieldValue(event, content, key)
			if !globMatch(pattern, value) {
				return false
			}
		case "room_member_count":
			is, _ := condition["is"].(string)
			if !compareCount(is, memberCount) {
				return false
			}
		case "contains_display_name":
			if !wordContains(body, strings.ToLower(displayName)) {
				return false
			}
		default:
			// Unknown condition kinds make the rule not match, per spec.
			return false
		}
	}
	return true
}

// eventFieldValue resolves a dotted key such as content.body against an event.
func eventFieldValue(event *persist.Event, content matrix.Object, key string) string {
	switch key {
	case "type":
		return event.Type
	case "sender":
		return event.Sender
	case "room_id":
		return event.RoomID
	case "event_id":
		return event.EventID
	}
	if !strings.HasPrefix(key, "content.") {
		return ""
	}
	value, ok := content[strings.TrimPrefix(key, "content.")].(string)
	if !ok {
		return ""
	}
	return value
}

// globMatch implements the simple * glob used by push rule patterns.
func globMatch(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	pattern = strings.ToLower(pattern)
	value = strings.ToLower(value)
	if !strings.Contains(pattern, "*") {
		return wordContains(value, pattern)
	}
	parts := strings.Split(pattern, "*")
	if parts[0] != "" && !strings.HasPrefix(value, parts[0]) {
		return false
	}
	last := parts[len(parts)-1]
	if last != "" && !strings.HasSuffix(value, last) {
		return false
	}
	cursor := 0
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		idx := strings.Index(value[cursor:], part)
		if idx < 0 {
			return false
		}
		cursor += idx + len(part)
	}
	return true
}

// wordContains reports whether needle appears in haystack as a whole word.
func wordContains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	haystack = strings.ToLower(haystack)
	for i := 0; ; {
		j := strings.Index(haystack[i:], needle)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(needle)
		beforeOK := start == 0 || !isWordByte(haystack[start-1])
		afterOK := end == len(haystack) || !isWordByte(haystack[end])
		if beforeOK && afterOK {
			return true
		}
		i = end
	}
}

// isWordByte reports whether a byte continues a word.
func isWordByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
	case b >= 'A' && b <= 'Z':
	case b >= '0' && b <= '9':
	case b >= 0x80:
	default:
		return false
	}
	return true
}

// compareCount evaluates the "is" expression of a room_member_count condition.
func compareCount(is string, count int) bool {
	if is == "" {
		return false
	}
	op := "=="
	value := is
	for _, candidate := range []string{"==", ">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(is, candidate) {
			op = candidate
			value = strings.TrimPrefix(is, candidate)
			break
		}
	}
	target, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	switch op {
	case "==", "=":
		return count == target
	case ">":
		return count > target
	case ">=":
		return count >= target
	case "<":
		return count < target
	case "<=":
		return count <= target
	}
	return false
}

// notifies reports whether an action list notifies.
func notifies(actions matrix.Array) bool {
	for _, action := range actions {
		if entry, ok := action.(string); ok && entry == "notify" {
			return true
		}
	}
	return false
}

// hasTweak reports whether an action list sets a tweak.
func hasTweak(actions matrix.Array, tweak string) bool {
	for _, action := range actions {
		entry, ok := action.(matrix.Object)
		if !ok {
			continue
		}
		if name, _ := entry["set_tweak"].(string); name == tweak {
			return true
		}
	}
	return false
}
