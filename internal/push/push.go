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

// ConditionContainsDisplayName is the condition kind the specification gives
// `.m.rule.contains_display_name`. It names no key and no pattern: the name it
// looks for is the recipient's own, which is not a property of the event.
const ConditionContainsDisplayName = "contains_display_name"

// DefaultNotificationLevel is the level a sender_notification_permission condition
// asks for when the room's power levels name no entry for its key. The power levels
// schema defaults the one notification key the specification defines — `room`, the
// level required to trigger an `@room` notification — to 50.
const DefaultNotificationLevel = int64(50)

// Context is everything a rule needs besides the event: the words of the recipient
// whose rule set is being evaluated, the size of the room the event landed in, and
// the power the event's sender holds there.
//
// It is a struct because a rule condition can name six of these things and a
// seventh positional parameter reads as a mistake at every call site.
type Context struct {
	Event   *persist.Event
	Content matrix.Object
	Body    string
	// DisplayName and Localpart are what the recipient is called in this room and in
	// their own ID: the deprecated mention rules match one of them as a whole word.
	DisplayName string
	Localpart   string
	MemberCount int
	// SenderLevel is the power level the sender of the event holds in the room, and
	// Notifications is the room's `notifications` object. A
	// sender_notification_permission condition compares the two, so they arrive
	// together rather than being looked up per condition.
	SenderLevel   int64
	Notifications map[string]int64
}

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

// Matches reports whether a rule fires for an event. The recipient's own words —
// the message body and what the recipient is called — are what the mention rules
// look for, and the room the event landed in supplies the member count and the
// sender's power.
func Matches(rule *persist.PushRule, ctx *Context) bool {
	switch rule.RuleID {
	case RuleMaster:
		// The master rule is an off switch, and this server reads it as one that is
		// never thrown: it never matches, whatever its enabled flag says. The
		// specification instead has it match every event with an empty action list,
		// so enabling it is what silences an account - see the recorded divergence in
		// docs/TODO.md.
		return false
	case RuleContainsDisplayName, RuleContainsUserName:
		// The user name rule matches the localpart of the recipient, the
		// display name rule matches their profile name. Both are seeded with no
		// conditions of their own, which is why they are recognised by identifier.
		needle := strings.ToLower(ctx.DisplayName)
		if rule.RuleID == RuleContainsUserName || needle == "" {
			needle = strings.ToLower(ctx.Localpart)
		}
		return needle != "" && WordContains(ctx.Body, needle)
	case RuleMemberEvent, RuleReaction, RuleNotice:
		// Membership changes and reactions are not worth waking a client for, and this
		// server says so by never matching. The specification says it by matching them
		// with an empty action list, which amounts to the same thing here: neither a
		// membership change nor a reaction reaches the notification path at all, which
		// reads m.room.message, m.room.encrypted and m.sticker. `.m.rule.notice` is not
		// in the specification's ruleset and rides along with the same answer.
		return false
	}
	if rule.Kind == "room" {
		return rule.RuleID == ctx.Event.RoomID
	}
	if rule.Kind == "sender" {
		return rule.RuleID == ctx.Event.Sender
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
			value, _ := PropertyValue(ctx.Event, ctx.Content, key)
			text, ok := value.(string)
			if !ok {
				// event_match compares strings; anything else does not match.
				return false
			}
			if !GlobMatch(pattern, text) {
				return false
			}
		case "event_property_is":
			value, ok := propertyOf(ctx, condition)
			if !ok {
				return false
			}
			if !sameValue(value, condition["value"]) {
				return false
			}
		case "event_property_contains":
			value, ok := propertyOf(ctx, condition)
			if !ok {
				return false
			}
			list, isList := value.([]any)
			if !isList || !containsValue(list, condition["value"]) {
				return false
			}
		case "room_member_count":
			is, _ := condition["is"].(string)
			if !CompareCount(is, ctx.MemberCount) {
				return false
			}
		case ConditionContainsDisplayName:
			if !WordContains(ctx.Body, strings.ToLower(ctx.DisplayName)) {
				return false
			}
		case "sender_notification_permission":
			key, _ := condition["key"].(string)
			required, ok := ctx.Notifications[key]
			if !ok {
				required = DefaultNotificationLevel
			}
			if ctx.SenderLevel < required {
				return false
			}
		default:
			// Unknown condition kinds make the rule not match, per spec.
			return false
		}
	}
	return true
}

// propertyOf resolves the key an event_property_is or event_property_contains
// condition names. A condition without a value to compare against answers the way
// a property that is not there does: the condition does not hold.
func propertyOf(ctx *Context, condition matrix.Object) (any, bool) {
	if _, named := condition["value"]; !named {
		return nil, false
	}
	key, _ := condition["key"].(string)
	return PropertyValue(ctx.Event, ctx.Content, key)
}

// containsValue reports whether an array property holds a value, matched exactly.
// Values that are not of the types the specification allows in an array are
// skipped rather than treated as equal to nothing.
func containsValue(list []any, want any) bool {
	for _, entry := range list {
		if sameValue(entry, want) {
			return true
		}
	}
	return false
}

// sameValue compares two JSON values the way event_property_is and
// event_property_contains ask for: exactly, on the non-compound canonical JSON
// types only. Numbers reach here as float64 from encoding/json, and canonical JSON
// confines every integer it allows to a range a float64 represents exactly, so a
// number never matches a string that spells it and a bool never matches a 1.
func sameValue(value, want any) bool {
	switch concrete := value.(type) {
	case nil:
		return want == nil
	case string:
		text, ok := want.(string)
		return ok && concrete == text
	case bool:
		flag, ok := want.(bool)
		return ok && concrete == flag
	case float64:
		number, ok := want.(float64)
		return ok && concrete == number
	}
	return false
}

// PropertyValue resolves a dot-separated property path against an event and hands
// back the raw JSON value it names, as the appendices define the paths: a dot
// separates two property names and a backslash escapes the dot or backslash that
// follows it, which is how the default rules name `content.m\.mentions.room` for
// the property called `m.mentions`. Other escape sequences are left alone, so a
// `\x` is a backslash and an x.
//
// The names of the event itself — type, sender, state_key and the rest — are the
// only ones above content, and they are read from the stored row rather than from
// the content, which is all a client-supplied key can name.
func PropertyValue(event *persist.Event, content matrix.Object, key string) (any, bool) {
	parts := splitPropertyPath(key)
	if len(parts) == 0 {
		return nil, false
	}
	var current any
	switch parts[0] {
	case "type":
		current = event.Type
	case "sender":
		current = event.Sender
	case "room_id":
		current = event.RoomID
	case "event_id":
		current = event.EventID
	case "state_key":
		current = event.StateKey
	case "content":
		current = content
	default:
		return nil, false
	}
	for _, name := range parts[1:] {
		switch level := current.(type) {
		case map[string]any:
			value, ok := level[name]
			if !ok {
				return nil, false
			}
			current = value
		default:
			return nil, false
		}
	}
	return current, true
}

// splitPropertyPath cuts a key into property names on the dots that are not
// escaped, and removes the escaping.
func splitPropertyPath(key string) []string {
	if key == "" {
		return nil
	}
	var (
		parts []string
		name  strings.Builder
	)
	for i := 0; i < len(key); i++ {
		switch key[i] {
		case '\\':
			// \. and \\ carry their own rule; anything else an escape is written in
			// stays as it came, per the appendices.
			if i+1 < len(key) && (key[i+1] == '.' || key[i+1] == '\\') {
				i++
				name.WriteByte(key[i])
				continue
			}
			name.WriteByte('\\')
		case '.':
			parts = append(parts, name.String())
			name.Reset()
		default:
			name.WriteByte(key[i])
		}
	}
	return append(parts, name.String())
}

// EventFieldValue resolves a dotted key such as content.body against an event.
// Only string values match: the specification's event_match conditions compare
// against strings.
func EventFieldValue(event *persist.Event, content matrix.Object, key string) string {
	value, ok := PropertyValue(event, content, key)
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
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
