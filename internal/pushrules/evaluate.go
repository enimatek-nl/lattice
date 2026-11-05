package pushrules

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Evaluating a rule set is what turns a stored preference into an answer about one
// event: is this worth telling the user about, and how should it look.
//
// Two rules of the specification decide the shape of this code.
//
// The first is that only the most important matching rule applies. Evaluation is
// therefore ordered and stops at the first match, and the master rule is checked before
// anything else: it outranks the user's own rules, which is the one place where a
// server-default rule beats a user-defined one.
//
// The second is that conditions the server does not understand must not match. They are
// kept, because the user wrote them and another version of this server may understand
// them, but they never fire — so a rule the server cannot check is a silent rule, not a
// guessing one.

// Event is the event under evaluation, in the decoded form the conditions address: the
// dot-separated paths a rule names, such as `content.body` or `state_key`, are looked up
// in it directly.
type Event map[string]any

// Context is what the conditions ask about beyond the event itself: who the user is, how
// big the room is, what the sender is allowed to do, and what the user is called in that
// room. A value the server does not know is left empty, which makes the conditions that
// need it not match rather than match on a guess.
type Context struct {
	UserID string
	// RoomMemberCount is the room's joined member count, for `room_member_count`.
	RoomMemberCount int
	// DisplayName is the user's display name in the room, for `contains_display_name`.
	DisplayName string
	// NotificationLevels maps a `sender_notification_permission` key, such as `room`,
	// to the power level the sender needs. Missing means 50, the specification's
	// default for notifications.
	NotificationLevels map[string]int
	// SenderLevel is the sender's power level in the room.
	SenderLevel int
	// SenderLevelSet distinguishes "the sender has power level 0" from "this server did
	// not look the sender up", which matters because an unchecked condition must not
	// match.
	SenderLevelSet bool
}

// Result is what evaluation says about one event.
type Result struct {
	// Matched says a rule fired at all. Without it, "no notification" and "no rule
	// matched" cannot be told apart in a log or a test.
	Matched bool
	// RuleID names the rule that fired, which is the only way a user can be told why a
	// notification arrived.
	RuleID string
	// Notify is whether the event is worth a notification: a matching rule whose
	// actions do not contain `notify` suppresses it.
	Notify bool
	// Highlight is the `highlight` tweak: true if a tweak set it, and true when a
	// `set_tweak: highlight` carries no value, as the specification defines.
	Highlight bool
	// Sound is the `sound` tweak, empty when none was set.
	Sound string
	// Tweaks carries every tweak the rule set, including ones this server does not
	// model, because a push gateway and a client may agree on others.
	Tweaks map[string]any
}

// Evaluate returns the outcome of the most important rule that matches.
//
// An event the user sent themselves never notifies: the specification says so, and a
// client that notified a user of their own message would be noisy in a way no rule can
// undo.
func (r Ruleset) Evaluate(event Event, ctx Context) Result {
	if sender, _ := event["sender"].(string); sender != "" && sender == ctx.UserID {
		return Result{}
	}
	// The master rule first: it outranks the user's own rules, and when it is enabled
	// and matches — it has no conditions, so it always matches — its actions are the
	// answer.
	if rules := r.KindRules(KindOverride); len(rules) > 0 && rules[0].RuleID == ".m.rule.master" {
		if rules[0].Enabled {
			return outcomeOf(rules[0])
		}
	}
	// The kinds in priority order, and within a kind the order they are stored in.
	for _, kind := range []Kind{KindOverride, KindContent, KindRoom, KindSender, KindUnderride} {
		for _, rule := range r.KindRules(kind) {
			if !rule.Enabled {
				continue
			}
			if rule.matches(event, kind, ctx) {
				return outcomeOf(rule)
			}
		}
	}
	return Result{}
}

// outcomeOf turns the winning rule's actions into a result.
func outcomeOf(rule Rule) Result {
	result := Result{Matched: true, RuleID: rule.RuleID, Tweaks: map[string]any{}}
	for _, action := range rule.Actions {
		if !action.IsTweak {
			if action.Name == actionNotify {
				result.Notify = true
			}
			continue
		}
		result.Tweaks[action.Tweak] = action.Value
		switch action.Tweak {
		case "highlight":
			// A highlight tweak with no value is defined to be true.
			result.Highlight = true
			if value, ok := action.Value.(bool); ok {
				result.Highlight = value
			}
		case "sound":
			if value, ok := action.Value.(string); ok {
				result.Sound = value
			}
		}
	}
	return result
}

// matches reports whether every condition of a rule holds. A rule with no conditions
// always matches, which is what makes the room, sender and content kinds work: their
// condition is their identifier or pattern, not a list.
func (rule Rule) matches(event Event, kind Kind, ctx Context) bool {
	switch kind {
	case KindRoom:
		roomID, _ := event["room_id"].(string)
		return roomID != "" && roomID == rule.RuleID
	case KindSender:
		sender, _ := event["sender"].(string)
		return sender != "" && sender == rule.RuleID
	case KindContent:
		if rule.Pattern == nil {
			return false
		}
		body, _ := event["content"].(map[string]any)
		text, ok := body["body"].(string)
		if !ok {
			return false
		}
		return globMatchWordBoundary(*rule.Pattern, text)
	}
	for _, condition := range rule.Conditions {
		if !conditionHolds(condition, event, ctx) {
			return false
		}
	}
	return true
}

// conditionHolds checks one condition. An unrecognised kind never matches.
func conditionHolds(condition map[string]any, event Event, ctx Context) bool {
	kind, _ := condition["kind"].(string)
	switch kind {
	case "event_match":
		key, _ := condition["key"].(string)
		pattern, ok := condition["pattern"].(string)
		if key == "" || !ok {
			return false
		}
		value, found := lookup(event, key)
		if !found {
			return false
		}
		text, ok := value.(string)
		if !ok {
			// An absent or non-string property does not match, even against `*`.
			return false
		}
		if key == "content.body" {
			return globMatchWordBoundary(pattern, text)
		}
		return globMatchEntire(pattern, text)
	case "event_property_is":
		key, _ := condition["key"].(string)
		value, found := lookup(event, key)
		if !found {
			return false
		}
		return exactlyEqual(condition["value"], value)
	case "event_property_contains":
		key, _ := condition["key"].(string)
		value, found := lookup(event, key)
		if !found {
			return false
		}
		list, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range list {
			if exactlyEqual(condition["value"], item) {
				return true
			}
		}
		return false
	case "contains_display_name":
		if strings.TrimSpace(ctx.DisplayName) == "" {
			return false
		}
		content, _ := event["content"].(map[string]any)
		text, ok := content["body"].(string)
		if !ok {
			return false
		}
		// The display name is matched as words, so "Ann" does not fire on "Anne".
		return containsWord(text, ctx.DisplayName)
	case "room_member_count":
		is, ok := condition["is"].(string)
		if !ok {
			return false
		}
		return compareCount(is, ctx.RoomMemberCount)
	case "sender_notification_permission":
		key, _ := condition["key"].(string)
		if key == "" || !ctx.SenderLevelSet {
			return false
		}
		required, ok := ctx.NotificationLevels[key]
		if !ok {
			required = 50
		}
		return ctx.SenderLevel >= required
	default:
		return false
	}
}

// lookup walks a dot-separated property path. A backslash escapes a dot that is part of
// a name, which is how `content.m\.mentions.room` reaches the `m.mentions` object.
func lookup(event Event, path string) (any, bool) {
	var current any = map[string]any(event)
	for _, part := range splitPath(path) {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok := object[part]
		if !ok {
			return nil, false
		}
		current = value
	}
	return current, true
}

// splitPath breaks a property path on unescaped dots.
func splitPath(path string) []string {
	var parts []string
	var current strings.Builder
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '\\':
			if i+1 < len(path) && path[i+1] == '.' {
				current.WriteByte('.')
				i++
				continue
			}
			current.WriteByte(path[i])
		case '.':
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteByte(path[i])
		}
	}
	parts = append(parts, current.String())
	return parts
}

// exactlyEqual compares two JSON values for a condition that matches exactly. Only the
// non-compound canonical JSON types are comparable this way, and no type conversion is
// attempted: `true` does not match "true", and 1 does not match "1".
func exactlyEqual(want, got any) bool {
	switch expected := want.(type) {
	case string:
		actual, ok := got.(string)
		return ok && actual == expected
	case bool:
		actual, ok := got.(bool)
		return ok && actual == expected
	case int:
		return numericEqual(got, float64(expected))
	case int64:
		return numericEqual(got, float64(expected))
	case float64:
		return numericEqual(got, expected)
	case nil:
		return got == nil
	default:
		return false
	}
}

// numericEqual compares a number that may have arrived as a JSON number or a Go number.
func numericEqual(value any, want float64) bool {
	switch number := value.(type) {
	case float64:
		return number == want
	case int:
		return float64(number) == want
	case int64:
		return float64(number) == want
	case json.Number:
		parsed, err := number.Float64()
		return err == nil && parsed == want
	default:
		return false
	}
}

// compareCount reads a `room_member_count` comparison: an optional operator and a
// decimal integer. With no operator it means equality.
func compareCount(expression string, count int) bool {
	expression = strings.TrimSpace(expression)
	operator := "=="
	number := expression
	for _, candidate := range []string{">=", "<=", "==", ">", "<"} {
		if strings.HasPrefix(expression, candidate) {
			operator, number = candidate, strings.TrimSpace(expression[len(candidate):])
			break
		}
	}
	want, err := strconv.Atoi(number)
	if err != nil {
		return false
	}
	switch operator {
	case "==":
		return count == want
	case "<":
		return count < want
	case ">":
		return count > want
	case "<=":
		return count <= want
	case ">=":
		return count >= want
	default:
		return false
	}
}

// globMatchEntire reports whether the whole value matches the glob, case-insensitively.
func globMatchEntire(pattern, value string) bool {
	return globCache.get(anchored, pattern).MatchString(value)
}

// globMatchWordBoundary reports whether any substring of value that starts and ends at a
// word boundary matches the glob, case-insensitively. A word boundary is the start or end
// of the value, or a character outside [A-Za-z0-9_] — which is what a Go regexp word
// boundary means, so the assertion can be written directly.
func globMatchWordBoundary(pattern, value string) bool {
	return globCache.get(wordBounded, pattern).MatchString(value)
}

// containsWord reports whether text contains phrase as whole words, ignoring case. It is
// written out rather than expressed as a regexp because a phrase may begin or end with a
// character that a word boundary cannot assert against, and that case has to be answered
// by the same rule the specification gives: the neighbouring character must not be a word
// character.
func containsWord(text, phrase string) bool {
	haystack, needle := strings.ToLower(text), strings.ToLower(strings.TrimSpace(phrase))
	if needle == "" {
		return false
	}
	for start := 0; ; {
		at := strings.Index(haystack[start:], needle)
		if at < 0 {
			return false
		}
		at += start
		end := at + len(needle)
		// Both sides must be word boundaries: the neighbouring character, if any, must
		// not be a word character.
		if isWordByte(before(haystack, at)) || isWordByte(atWith(haystack, end)) {
			start = at + 1
			continue
		}
		return true
	}
}

// before returns the byte just before i, or 0 at the start of the string.
func before(text string, i int) byte {
	if i == 0 {
		return 0
	}
	return text[i-1]
}

// atWith returns the byte at i, or 0 past the end.
func atWith(text string, i int) byte {
	if i >= len(text) {
		return 0
	}
	return text[i]
}

// isWordByte reports membership in the specification's word set: letters, digits and
// underscore. Bytes of a multi-byte character are outside it, which is the answer the
// rule gives anyway.
func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// The two ways a glob is anchored. They compile the same pattern differently, so they are
// part of the cache key.
const (
	anchored    = "entire:"
	wordBounded = "words:"
)

// globMatcher compiles a specification glob into a case-insensitive regexp: `*` is any
// run of characters, `?` is one character, and a backslash escapes the next character.
// Patterns are cached because they are compiled per event, and a rule set is evaluated
// against every message.
var globCache = newGlobCache(256)

type globCacheT struct {
	mu    sync.RWMutex
	limit int
	order []string
	items map[string]*regexp.Regexp
}

func newGlobCache(limit int) *globCacheT {
	return &globCacheT{limit: limit, items: map[string]*regexp.Regexp{}}
}

func (c *globCacheT) get(kind, pattern string) *regexp.Regexp {
	key := kind + pattern
	c.mu.RLock()
	re, ok := c.items[key]
	c.mu.RUnlock()
	if ok {
		return re
	}
	body := globToRegexp(pattern)
	if kind == anchored {
		re = regexp.MustCompile(`(?i)\A(?:` + body + `)\z`)
	} else {
		re = regexp.MustCompile(`(?i)\b(?:` + body + `)\b`)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.limit && len(c.order) > 0 {
		evicted := c.order[0]
		c.order = c.order[1:]
		if evicted != key {
			delete(c.items, evicted)
		}
	}
	if _, exists := c.items[key]; !exists {
		c.order = append(c.order, key)
	}
	c.items[key] = re
	return re
}

// globToRegexp translates the glob body. Word-boundary matching is done by the callers
// wrapping the body, so the body itself is not anchored here.
func globToRegexp(pattern string) string {
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			out.WriteString(".*")
		case '?':
			out.WriteString(".")
		case '\\':
			if i+1 < len(pattern) {
				i++
				out.WriteString(regexp.QuoteMeta(string(pattern[i])))
			}
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	return out.String()
}
