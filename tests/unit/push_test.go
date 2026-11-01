package unit

import (
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/push"
)

// TestPushRuleOrder pins the precedence of rule kinds. The specification
// evaluates the kinds in a fixed order and stops at the first rule that matches,
// so an override rule always beats a content rule and both beat the underride
// rules that describe "everything else".
func TestPushRuleOrder(t *testing.T) {
	in := []*persist.PushRule{
		{Kind: "underride", RuleID: ".m.rule.message"},
		{Kind: "sender", RuleID: "@alice:example.com"},
		{Kind: "underride", RuleID: ".m.rule.default"},
		{Kind: "content", RuleID: ".m.rule.contains_user_name"},
		{Kind: "override", RuleID: ".m.rule.master"},
		{Kind: "room", RuleID: "!room:example.com"},
		{Kind: "nonsense", RuleID: "first"},
		{Kind: "nonsense", RuleID: "second"},
	}
	want := []string{
		".m.rule.master", ".m.rule.contains_user_name", "!room:example.com",
		"@alice:example.com", ".m.rule.message", ".m.rule.default", "first", "second",
	}
	got := push.OrderRules(in)
	if len(got) != len(want) {
		t.Fatalf("OrderRules dropped rules: %d in, %d out", len(in), len(got))
	}
	for i, rule := range got {
		if rule.RuleID != want[i] {
			t.Errorf("rule %d = %s (%s), want %s", i, rule.RuleID, rule.Kind, want[i])
		}
	}
	// Ordering must not reorder rules inside a kind: the position a client stored
	// is what decides between two rules of the same kind.
	inside := push.OrderRules([]*persist.PushRule{
		{Kind: "override", RuleID: "b", Positions: 2},
		{Kind: "override", RuleID: "a", Positions: 1},
	})
	if inside[0].RuleID != "b" || inside[1].RuleID != "a" {
		t.Errorf("rules inside one kind were reordered: %s, %s", inside[0].RuleID, inside[1].RuleID)
	}
}

// TestGlobMatch pins the pattern language of event_match conditions. A pattern
// without a star is a whole word match, which is what stops the mention rule for
// "bob" from firing on "bobby"; a star matches any run of characters.
func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, value string
		want           bool
		comment        string
	}{
		{pattern: "", value: "anything", want: true, comment: "no pattern constrains the value"},
		{pattern: "m.text", value: "m.text", want: true},
		{pattern: "m.text", value: "m.image", want: false},
		{pattern: "bob", value: "hi bob!", want: true, comment: "a word match, case insensitive"},
		{pattern: "bob", value: "bobby", want: false, comment: "no substring mentions"},
		{pattern: "BOB", value: "Hi Bob", want: true},
		{pattern: "*ball*", value: "snap ball", want: true},
		{pattern: "*.example.com", value: "a.example.com", want: true},
		{pattern: "*.example.com", value: "example.com.evil.net", want: false},
		{pattern: "a*b*c", value: "axxbyyc", want: true, comment: "the parts must appear in order"},
		{pattern: "a*b*c", value: "axxcyy", want: false},
		{pattern: "a*", value: "a", want: true, comment: "a star may match nothing"},
		{pattern: "*a*b*", value: "zzazzbzz", want: true},
		{pattern: "m.room.*", value: "m.room.message", want: true},
		{pattern: "m.room.*", value: "m.room", want: false, comment: "the suffix has to be there"},
	}
	for _, tt := range tests {
		if got := push.GlobMatch(tt.pattern, tt.value); got != tt.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v (%s)", tt.pattern, tt.value, got, tt.want, tt.comment)
		}
	}
}

// TestWordContains pins the word boundaries of a mention. Anything that is not a
// letter, a digit or a non-ASCII byte separates words, so "bob" is mentioned by
// "bob." and "(bob)" but not by "bobby" or "12bob".
func TestWordContains(t *testing.T) {
	tests := []struct {
		haystack, needle string
		want             bool
		comment          string
	}{
		{haystack: "hi bob", needle: "bob", want: true},
		{haystack: "bob.", needle: "bob", want: true},
		{haystack: "(bob)", needle: "bob", want: true},
		{haystack: "bobby", needle: "bob", want: false},
		{haystack: "12bob", needle: "bob", want: false, comment: "a digit continues the word"},
		{haystack: "Hi Bob", needle: "bob", want: true, comment: "matching is case insensitive"},
		{haystack: "un café!", needle: "café", want: true, comment: "non-ASCII letters are word bytes"},
		{haystack: "café", needle: "caf", want: false},
		{haystack: "bob bob", needle: "bob", want: true},
		{haystack: "bobbob", needle: "bob", want: false},
		{haystack: "", needle: "bob", want: false},
		{haystack: "bob", needle: "", want: false, comment: "an empty name mentions nobody"},
		{haystack: "a b c", needle: "b c", want: true, comment: "a needle may span words"},
	}
	for _, tt := range tests {
		if got := push.WordContains(tt.haystack, tt.needle); got != tt.want {
			t.Errorf("WordContains(%q, %q) = %v, want %v (%s)", tt.haystack, tt.needle, got, tt.want, tt.comment)
		}
	}
}

// TestCompareCount pins the "is" expression of a room_member_count condition,
// which the specification writes as an inequality.
func TestCompareCount(t *testing.T) {
	tests := []struct {
		is      string
		count   int
		want    bool
		comment string
	}{
		{is: "==2", count: 2, want: true},
		{is: "=2", count: 2, want: true, comment: "a single equals sign is the same thing"},
		{is: "2", count: 2, want: true, comment: "a bare number means equality"},
		{is: ">=2", count: 2, want: true},
		{is: ">=2", count: 1, want: false},
		{is: ">2", count: 2, want: false},
		{is: "<=3", count: 3, want: true},
		{is: "<3", count: 3, want: false},
		{is: " >= 3 ", count: 4, want: true, comment: "spaces around the number are tolerated"},
		{is: "", count: 0, want: false, comment: "no expression matches nothing"},
		{is: "abc", count: 0, want: false},
		{is: ">=abc", count: 0, want: false},
	}
	for _, tt := range tests {
		if got := push.CompareCount(tt.is, tt.count); got != tt.want {
			t.Errorf("CompareCount(%q, %d) = %v, want %v", tt.is, tt.count, got, tt.want)
		}
	}
}

// TestPushActions pins the two reads of a matched rule's actions: whether the
// rule notifies at all, and which tweaks it sets.
func TestPushActions(t *testing.T) {
	notify := matrix.Array{"notify", matrix.Object{"set_tweak": "sound", "value": "default"}}
	dontNotify := matrix.Array{"dont_notify"}
	highlight := matrix.Array{"notify", matrix.Object{"set_tweak": "highlight", "value": true}}
	count := matrix.Array{"notify", matrix.Object{"set_tweak": "count"}}

	tests := []struct {
		actions   matrix.Array
		notifies  bool
		highlight bool
		sound     bool
		comment   string
	}{
		{actions: notify, notifies: true, sound: true},
		{actions: dontNotify, notifies: false},
		{actions: highlight, notifies: true, highlight: true},
		{actions: count, notifies: true},
		{actions: matrix.Array{}, notifies: false},
		{actions: matrix.Array{matrix.Object{"set_tweak": "highlight"}}, notifies: false, highlight: true,
			comment: "a tweak without a notify action sets the tweak but does not notify"},
	}
	for _, tt := range tests {
		if got := push.Notifies(tt.actions); got != tt.notifies {
			t.Errorf("Notifies(%v) = %v, want %v", tt.actions, got, tt.notifies)
		}
		if got := push.HasTweak(tt.actions, "highlight"); got != tt.highlight {
			t.Errorf("HasTweak(%v, highlight) = %v, want %v", tt.actions, got, tt.highlight)
		}
		if got := push.HasTweak(tt.actions, "sound"); got != tt.sound {
			t.Errorf("HasTweak(%v, sound) = %v, want %v", tt.actions, got, tt.sound)
		}
	}
}

// TestEventFieldValue pins the dotted keys an event_match condition may name.
func TestEventFieldValue(t *testing.T) {
	event := &persist.Event{
		EventID: "$event", RoomID: "!room:example.com", Type: matrix.TypeMessage, Sender: "@alice:example.com",
	}
	content := matrix.Object{"msgtype": "m.text", "body": "hello", "emote": true}
	tests := []struct {
		key, want string
		comment   string
	}{
		{key: "type", want: matrix.TypeMessage},
		{key: "sender", want: "@alice:example.com"},
		{key: "room_id", want: "!room:example.com"},
		{key: "event_id", want: "$event"},
		{key: "content.msgtype", want: "m.text"},
		{key: "content.body", want: "hello"},
		{key: "content.emote", want: "", comment: "only string values can match"},
		{key: "content.missing", want: ""},
		{key: "content", want: "", comment: "a bare content key names no field"},
		{key: "origin_server_ts", want: "", comment: "the specification defines no comparison for it"},
	}
	for _, tt := range tests {
		if got := push.EventFieldValue(event, content, tt.key); got != tt.want {
			t.Errorf("EventFieldValue(%q) = %q, want %q (%s)", tt.key, got, tt.want, tt.comment)
		}
	}
}

// TestRuleMatches walks the rule shapes the notification path meets. The expected
// results come from the "Push Rule Event" section and the default ruleset: the
// master rule is an off switch, membership events and reactions never wake a
// client, and conditions of one rule are ANDed.
func TestRuleMatches(t *testing.T) {
	event := &persist.Event{
		EventID: "$event", RoomID: "!room:example.com", Type: matrix.TypeMessage, Sender: "@alice:example.com",
	}
	content := matrix.Object{"msgtype": "m.text", "body": "hello alice"}
	body := "hello alice"

	tests := []struct {
		rule        *persist.PushRule
		body        string
		displayName string
		localpart   string
		memberCount int
		want        bool
		comment     string
	}{
		{
			rule:    &persist.PushRule{Kind: "override", RuleID: ".m.rule.master", Enabled: true},
			want:    false,
			comment: "the master rule is the off switch: it never matches",
		},
		{
			rule:        &persist.PushRule{Kind: "override", RuleID: ".m.rule.contains_display_name"},
			body:        "nice work Alice",
			displayName: "Alice",
			localpart:   "bob",
			want:        true,
		},
		{
			rule:        &persist.PushRule{Kind: "override", RuleID: ".m.rule.contains_display_name"},
			body:        "alice said wonder",
			displayName: "Alice Wonder",
			localpart:   "bob",
			want:        false,
			comment:     "a display name has to appear as it is written, not one word at a time",
		},
		{
			rule:        &persist.PushRule{Kind: "override", RuleID: ".m.rule.contains_display_name"},
			body:        "nice work Alicia",
			displayName: "Alice",
			localpart:   "bob",
			want:        false,
			comment:     "a mention is a whole word",
		},
		{
			rule:        &persist.PushRule{Kind: "content", RuleID: ".m.rule.contains_user_name"},
			body:        "bob, over here",
			displayName: "",
			localpart:   "bob",
			want:        true,
			comment:     "without a display name the user name rule uses the localpart",
		},
		{
			rule:    &persist.PushRule{Kind: "override", RuleID: ".m.rule.member_event"},
			want:    false,
			comment: "a member joined message does not wake a client",
		},
		{
			rule: &persist.PushRule{Kind: "override", RuleID: ".m.rule.reaction"},
			want: false,
		},
		{
			rule:    &persist.PushRule{Kind: "underride", RuleID: ".m.rule.notice"},
			want:    false,
			comment: "notices come from bots and are silenced by default",
		},
		{
			rule:    &persist.PushRule{Kind: "room", RuleID: "!room:example.com"},
			want:    true,
			comment: "a room rule is keyed by the room id",
		},
		{
			rule: &persist.PushRule{Kind: "room", RuleID: "!other:example.com"},
			want: false,
		},
		{
			rule:    &persist.PushRule{Kind: "sender", RuleID: "@alice:example.com"},
			want:    true,
			comment: "a sender rule is keyed by the sender",
		},
		{
			rule:    &persist.PushRule{Kind: "underride", RuleID: ".m.rule.default"},
			want:    true,
			comment: "the default underride rule has no conditions and matches every message",
		},
		{
			rule:    &persist.PushRule{Kind: "override", RuleID: "no-conditions"},
			want:    false,
			comment: "a conditionless override rule does not swallow everything",
		},
		{
			rule: &persist.PushRule{Kind: "underride", RuleID: "msgtype",
				Conditions: `[{"kind":"event_match","key":"content.msgtype","pattern":"m.text"}]`},
			body:    body,
			want:    true,
			comment: "the default ruleset keys the message rules on content.msgtype",
		},
		{
			rule: &persist.PushRule{Kind: "underride", RuleID: "msgtype",
				Conditions: `[{"kind":"event_match","key":"content.msgtype","pattern":"m.notice"}]`},
			body: body,
			want: false,
		},
		{
			rule: &persist.PushRule{Kind: "override", RuleID: "type",
				Conditions: `[{"kind":"event_match","key":"type","pattern":"m.room.message"}]`},
			body: body,
			want: true,
		},
		{
			rule: &persist.PushRule{Kind: "underride", RuleID: "dm",
				Conditions: `[{"kind":"room_member_count","is":"==2"}]`},
			memberCount: 2,
			want:        true,
			comment:     "the one-to-one rules fire in a room of two",
		},
		{
			rule: &persist.PushRule{Kind: "underride", RuleID: "dm",
				Conditions: `[{"kind":"room_member_count","is":"==2"}]`},
			memberCount: 5,
			want:        false,
		},
		{
			rule: &persist.PushRule{Kind: "override", RuleID: "mention",
				Conditions: `[{"kind":"contains_display_name"}]`},
			body:        "hello alice",
			displayName: "Alice",
			want:        true,
		},
		{
			rule: &persist.PushRule{Kind: "override", RuleID: "and",
				Conditions: `[{"kind":"event_match","key":"content.msgtype","pattern":"m.text"},` +
					`{"kind":"room_member_count","is":">=1000"}]`},
			memberCount: 3,
			want:        false,
			comment:     "the conditions of a rule are ANDed: the whole body rule stays quiet in a small room",
		},
		{
			rule: &persist.PushRule{Kind: "override", RuleID: "unknown",
				Conditions: `[{"kind":"event_property_is","key":"content.msgtype","value":"m.text"}]`},
			want:    false,
			comment: "an unknown condition kind makes the rule not match",
		},
		{
			rule:    &persist.PushRule{Kind: "override", RuleID: "broken", Conditions: "not json"},
			want:    false,
			comment: "a stored rule that cannot be read matches nothing rather than everything",
		},
	}
	for _, tt := range tests {
		got := push.Matches(tt.rule, event, content, tt.body, tt.displayName, tt.localpart, tt.memberCount)
		if got != tt.want {
			t.Errorf("Matches(%s/%s) = %v, want %v (%s)", tt.rule.Kind, tt.rule.RuleID, got, tt.want, tt.comment)
		}
	}
}
