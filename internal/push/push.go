// Package push evaluates Matrix push rules: the precedence of rule kinds,
// whether a rule fires for an event, and the pattern, word and count
// expressions the conditions of a rule are built from.
//
// Everything here is pure - no storage, no clock, no server state. The rules of
// a room decide both whether a message notifies and whether it highlights, so
// the matching rules of the specification (the "Push Rule Event" section and
// the algorithm in "Notifications") live in one place that a unit test can drive
// without a database, and that federation can reuse when remote events arrive.
package push

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// Kinds lists the push rule kinds in the order the specification gives them the
// right to decide: an override rule wins over everything, an underride rule over
// nothing.
var Kinds = []string{"override", "content", "room", "sender", "underride"}

// Default rule identifiers of the specification, named because matching
// behaviour is defined per identifier rather than per condition list.
const (
	RuleMaster              = ".m.rule.master"
	RuleContainsDisplayName = ".m.rule.contains_display_name"
	RuleContainsUserName    = ".m.rule.contains_user_name"
	RuleMemberEvent         = ".m.rule.member_event"
	RuleReaction            = ".m.rule.reaction"
	RuleNotice              = ".m.rule.notice"
)

// OrderRules sorts rules by kind precedence and then by their stored position.
// Rules of unknown kinds keep their relative order at the end.
func OrderRules(rules []*persist.PushRule) []*persist.PushRule {
	rank := map[string]int{}
	for i, kind := range Kinds {
		rank[kind] = i
	}
	out := make([]*persist.PushRule, 0, len(rules))
	for _, kind := range Kinds {
		for _, rule := range rules {
			if rule.Kind == kind {
				out = append(out, rule)
			}
		}
	}
	for _, rule := range rules {
		if _, ok := rank[rule.Kind]; !ok {
			out = append(out, rule)
		}
	}
	return out
}

// Matches reports whether a rule fires for an event. body, displayName and
// localpart are the recipient's own words: the message body and what the
// recipient is called, which is what the mention rules look for. memberCount is
// the size of the room the event landed in.
func Matches(rule *persist.PushRule, event *persist.Event, content matrix.Object,
	body, displayName, localpart string, memberCount int) bool {
	switch rule.RuleID {
	case RuleMaster:
		// The master rule is an off switch: the specification defines it as never
		// matching, and disabling notifications means removing it.
		return false
	case RuleContainsDisplayName, RuleContainsUserName:
		// The user name rule matches the localpart of the recipient, the
		// display name rule matches their profile name.
		needle := strings.ToLower(displayName)
		if rule.RuleID == RuleContainsUserName || needle == "" {
			needle = strings.ToLower(localpart)
		}
		return needle != "" && WordContains(body, needle)
	case RuleMemberEvent, RuleReaction, RuleNotice:
		// Membership changes, reactions and notices are not worth waking a client
		// for; the specification defines these defaults as not matching.
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
			value := EventFieldValue(event, content, key)
			if !GlobMatch(pattern, value) {
				return false
			}
		case "room_member_count":
			is, _ := condition["is"].(string)
			if !CompareCount(is, memberCount) {
				return false
			}
		case "contains_display_name":
			if !WordContains(body, strings.ToLower(displayName)) {
				return false
			}
		default:
			// Unknown condition kinds make the rule not match, per spec.
			return false
		}
	}
	return true
}

// EventFieldValue resolves a dotted key such as content.body against an event.
// Only string values match: the specification's event_match conditions compare
// against strings.
func EventFieldValue(event *persist.Event, content matrix.Object, key string) string {
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

// GlobMatch implements the simple * glob of push rule patterns. A pattern
// without a star is a word match, which is why "bob" does not match "bobby":
// the specification asks for mentions, not substrings.
func GlobMatch(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	pattern = strings.ToLower(pattern)
	value = strings.ToLower(value)
	if !strings.Contains(pattern, "*") {
		return WordContains(value, pattern)
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

// WordContains reports whether needle appears in haystack as a whole word.
func WordContains(haystack, needle string) bool {
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

// CompareCount evaluates the "is" expression of a room_member_count condition,
// which the specification writes as an inequality such as ">=1000" or "==2".
func CompareCount(is string, count int) bool {
	// Servers and clients write the expression with padding now and then, so the
	// operator is looked for after the whitespace rather than at the first byte.
	is = strings.TrimSpace(is)
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

// Notifies reports whether an action list notifies.
func Notifies(actions matrix.Array) bool {
	for _, action := range actions {
		if entry, ok := action.(string); ok && entry == "notify" {
			return true
		}
	}
	return false
}

// HasTweak reports whether an action list sets a tweak, such as "highlight" or
// "sound".
func HasTweak(actions matrix.Array, tweak string) bool {
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
