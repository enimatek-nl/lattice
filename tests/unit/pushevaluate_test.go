package unit

import (
	"testing"

	"github.com/enimatek/lattice/internal/pushrules"
)

// messageEvent is the event most of these cases are checked against: a plain message
// from somebody else in a room the user is in.
// cakePattern is a content rule pattern; the rule field is a pointer so that an absent
// pattern and an empty one are different things.
var cakePattern = "cake"

func messageEvent(body string) pushrules.Event {
	return pushrules.Event{
		"sender":    "@bob:example.org",
		"type":      "m.room.message",
		"room_id":   "!room:example.org",
		"content":   map[string]any{"msgtype": "m.text", "body": body},
		"state_key": nil,
	}
}

// TestEvaluateUsesTheMostImportantRule checks the part of push rules that is easy to get
// backwards: several rules may match, and only the most important one decides.
func TestEvaluateUsesTheMostImportantRule(t *testing.T) {
	set := pushrules.Ruleset{
		Override: []pushrules.Rule{
			{RuleID: ".m.rule.master", Default: true, Enabled: false, Actions: []pushrules.Action{}},
			{RuleID: "user-override", Enabled: true,
				Conditions: []map[string]any{{"kind": "event_match", "key": "content.body", "pattern": "cake"}},
				Actions:    []pushrules.Action{{Name: "notify"}, {IsTweak: true, Tweak: "sound", Value: "alarm.wav"}}},
		},
		Content: []pushrules.Rule{{RuleID: "content-cake", Enabled: true, Pattern: &cakePattern,
			Actions: []pushrules.Action{{Name: "notify"}, {IsTweak: true, Tweak: "highlight"}}}},
		Underride: []pushrules.Rule{{RuleID: ".m.rule.message", Default: true, Enabled: true,
			Conditions: []map[string]any{{"kind": "event_match", "key": "type", "pattern": "m.room.message"}},
			Actions:    []pushrules.Action{{Name: "notify"}}}},
	}
	ctx := pushrules.Context{UserID: "@alice:example.org"}

	got := set.Evaluate(messageEvent("have some cake"), ctx)
	if got.RuleID != "user-override" {
		t.Errorf("the content rule decided, but an override rule outranks it: %+v", got)
	}
	if !got.Notify || got.Sound != "alarm.wav" || got.Highlight {
		t.Errorf("the winning rule's actions are the answer: %+v", got)
	}

	// With the master rule enabled, nothing else is consulted: it is how a user turns
	// the whole thing off.
	set.Override[0].Enabled = true
	if got := set.Evaluate(messageEvent("have some cake"), ctx); got.Notify || got.RuleID != ".m.rule.master" {
		t.Errorf("the enabled master rule should silence the room: %+v", got)
	}
	set.Override[0].Enabled = false

	// A disabled rule never matches, so the next one down decides.
	set.Override[1].Enabled = false
	if got := set.Evaluate(messageEvent("have some cake"), ctx); got.RuleID != "content-cake" || !got.Highlight {
		t.Errorf("skipping a disabled rule: %+v", got)
	}
	set.Override[1].Enabled = true

	// A rule whose actions do not contain notify suppresses the event rather than
	// notifying about it.
	suppress := pushrules.Ruleset{Room: []pushrules.Rule{{RuleID: "!room:example.org", Enabled: true, Actions: []pushrules.Action{}}},
		Underride: set.Underride}
	if got := suppress.Evaluate(messageEvent("anything"), ctx); got.Notify {
		t.Errorf("a room rule with no actions should suppress, got %+v", got)
	}

	// An event the user sent themselves is never worth a notification, whatever the
	// rules say.
	own := messageEvent("look at me")
	own["sender"] = "@alice:example.org"
	if got := set.Evaluate(own, ctx); got.Notify || got.Matched {
		t.Errorf("the user's own event notified: %+v", got)
	}
}

// TestEvaluateConditions walks the condition kinds. Each one has a way to be wrong that
// looks right, so the table is the point.
func TestEvaluateConditions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		conditions []map[string]any
		content    map[string]any
		event      pushrules.Event
		ctx        pushrules.Context
		wantMatch  bool
	}{
		{
			name:       "event_match on a plain property",
			conditions: []map[string]any{{"kind": "event_match", "key": "content.msgtype", "pattern": "m.text"}},
			wantMatch:  true,
		},
		{
			name:       "event_match is case insensitive",
			conditions: []map[string]any{{"kind": "event_match", "key": "content.msgtype", "pattern": "M.TEXT"}},
			wantMatch:  true,
		},
		{
			name:       "event_match must match the whole value",
			conditions: []map[string]any{{"kind": "event_match", "key": "content.msgtype", "pattern": "text"}},
			wantMatch:  false,
		},
		{
			name:       "event_match with a glob",
			conditions: []map[string]any{{"kind": "event_match", "key": "content.msgtype", "pattern": "m.*"}},
			wantMatch:  true,
		},
		{
			name:       "a star does not match a missing property",
			conditions: []map[string]any{{"kind": "event_match", "key": "content.topic", "pattern": "*"}},
			wantMatch:  false,
		},
		{
			name:       "content.body matches on word boundaries",
			conditions: []map[string]any{{"kind": "event_match", "key": "content.body", "pattern": "cake"}},
			wantMatch:  true,
		},
		{
			name:       "content.body does not match inside a word",
			conditions: []map[string]any{{"kind": "event_match", "key": "content.body", "pattern": "ake"}},
			wantMatch:  false,
		},
		{
			name:       "content.body spans words",
			conditions: []map[string]any{{"kind": "event_match", "key": "content.body", "pattern": "have*cake"}},
			wantMatch:  true,
		},
		{
			name:       "an escaped dot reaches a dotted property name",
			conditions: []map[string]any{{"kind": "event_match", "key": `content.m\.mentions.kind`, "pattern": "room"}},
			content:    map[string]any{"msgtype": "m.text", "body": "x", "m.mentions": map[string]any{"kind": "room"}},
			wantMatch:  true,
		},
		{
			name:       "event_property_is matches a boolean exactly",
			conditions: []map[string]any{{"kind": "event_property_is", "key": `content.m\.mentions.room`, "value": true}},
			content:    map[string]any{"msgtype": "m.text", "body": "x", "m.mentions": map[string]any{"room": true}},
			wantMatch:  true,
		},
		{
			name:       "event_property_is does not convert types",
			conditions: []map[string]any{{"kind": "event_property_is", "key": `content.m\.mentions.room`, "value": "true"}},
			content:    map[string]any{"msgtype": "m.text", "body": "x", "m.mentions": map[string]any{"room": true}},
			wantMatch:  false,
		},
		{
			name:       "event_property_contains finds a value in an array",
			conditions: []map[string]any{{"kind": "event_property_contains", "key": `content.m\.mentions.user_ids`, "value": "@alice:example.org"}},
			content: map[string]any{"msgtype": "m.text", "body": "x",
				"m.mentions": map[string]any{"user_ids": []any{"@carol:example.org", "@alice:example.org"}}},
			wantMatch: true,
		},
		{
			name:       "event_property_contains does not match a partial value",
			conditions: []map[string]any{{"kind": "event_property_contains", "key": `content.m\.mentions.user_ids`, "value": "alice"}},
			content: map[string]any{"msgtype": "m.text", "body": "x",
				"m.mentions": map[string]any{"user_ids": []any{"@alice:example.org"}}},
			wantMatch: false,
		},
		{
			name:       "contains_display_name matches whole words",
			conditions: []map[string]any{{"kind": "contains_display_name"}},
			content:    map[string]any{"msgtype": "m.text", "body": "Hello Alice"},
			ctx:        pushrules.Context{UserID: "@alice:example.org", DisplayName: "alice"},
			wantMatch:  true,
		},
		{
			name:       "contains_display_name does not match inside a longer name",
			conditions: []map[string]any{{"kind": "contains_display_name"}},
			content:    map[string]any{"msgtype": "m.text", "body": "Hello Alicia"},
			ctx:        pushrules.Context{UserID: "@alice:example.org", DisplayName: "Alice"},
			wantMatch:  false,
		},
		{
			name:       "contains_display_name with no known name does not match",
			conditions: []map[string]any{{"kind": "contains_display_name"}},
			content:    map[string]any{"msgtype": "m.text", "body": "Hello Alice"},
			ctx:        pushrules.Context{UserID: "@alice:example.org"},
			wantMatch:  false,
		},
		{
			name:       "room_member_count equality",
			conditions: []map[string]any{{"kind": "room_member_count", "is": "2"}},
			ctx:        pushrules.Context{UserID: "@alice:example.org", RoomMemberCount: 2},
			wantMatch:  true,
		},
		{
			name:       "room_member_count less than or equal",
			conditions: []map[string]any{{"kind": "room_member_count", "is": "<=10"}},
			ctx:        pushrules.Context{UserID: "@alice:example.org", RoomMemberCount: 3},
			wantMatch:  true,
		},
		{
			name:       "room_member_count with a bad expression does not match",
			conditions: []map[string]any{{"kind": "room_member_count", "is": "plenty"}},
			ctx:        pushrules.Context{UserID: "@alice:example.org", RoomMemberCount: 3},
			wantMatch:  false,
		},
		{
			name:       "sender_notification_permission reads the room's levels",
			conditions: []map[string]any{{"kind": "sender_notification_permission", "key": "room"}},
			ctx: pushrules.Context{UserID: "@alice:example.org", SenderLevel: 100, SenderLevelSet: true,
				NotificationLevels: map[string]int{"room": 50}},
			wantMatch: true,
		},
		{
			name:       "sender_notification_permission refuses a sender without the level",
			conditions: []map[string]any{{"kind": "sender_notification_permission", "key": "room"}},
			ctx: pushrules.Context{UserID: "@alice:example.org", SenderLevel: 0, SenderLevelSet: true,
				NotificationLevels: map[string]int{"room": 50}},
			wantMatch: false,
		},
		{
			name:       "sender_notification_permission refuses when the sender was never looked up",
			conditions: []map[string]any{{"kind": "sender_notification_permission", "key": "room"}},
			ctx:        pushrules.Context{UserID: "@alice:example.org"},
			wantMatch:  false,
		},
		{
			name:       "a condition this server does not know never matches",
			conditions: []map[string]any{{"kind": "org.example.dreaming", "key": "content.body"}},
			wantMatch:  false,
		},
		{
			name:       "a condition with no kind never matches",
			conditions: []map[string]any{{"key": "content.body", "pattern": "cake"}},
			wantMatch:  false,
		},
	} {
		set := pushrules.Ruleset{Override: []pushrules.Rule{{
			RuleID:     "probe",
			Enabled:    true,
			Conditions: tc.conditions,
			Actions:    []pushrules.Action{{Name: "notify"}},
		}}}
		event := messageEvent("have some cake")
		if tc.content != nil {
			event["content"] = tc.content
		}
		if tc.event != nil {
			event = tc.event
		}
		ctx := tc.ctx
		if ctx.UserID == "" {
			ctx.UserID = "@alice:example.org"
		}
		got := set.Evaluate(event, ctx)
		if tc.wantMatch && !got.Matched {
			t.Errorf("%s: did not match", tc.name)
		}
		if !tc.wantMatch && got.Matched {
			t.Errorf("%s: matched, want it not to", tc.name)
		}
	}
}

// TestEvaluateRoomAndSenderRules checks the two kinds whose condition is their identifier.
func TestEvaluateRoomAndSenderRules(t *testing.T) {
	ctx := pushrules.Context{UserID: "@alice:example.org"}
	set := pushrules.Ruleset{
		Room:      []pushrules.Rule{{RuleID: "!room:example.org", Enabled: true, Actions: []pushrules.Action{{Name: "notify"}, {IsTweak: true, Tweak: "highlight"}}}},
		Sender:    []pushrules.Rule{{RuleID: "@bob:example.org", Enabled: true, Actions: []pushrules.Action{}}},
		Underride: []pushrules.Rule{{RuleID: ".m.rule.message", Default: true, Enabled: true, Conditions: []map[string]any{{"kind": "event_match", "key": "type", "pattern": "m.room.message"}}, Actions: []pushrules.Action{{Name: "notify"}}}},
	}
	// The specification's order between the kinds is override, content, room, sender,
	// underride — so a room rule decides before a sender rule is consulted, even though
	// a sender rule feels more specific to a person reading it.
	got := set.Evaluate(messageEvent("hi"), ctx)
	if got.RuleID != "!room:example.org" || !got.Notify || !got.Highlight {
		t.Errorf("the room rule should outrank the sender rule: %+v", got)
	}
	// Take the room rule away and the sender rule, which suppresses, decides.
	withoutRoom := pushrules.Ruleset{Sender: set.Sender, Underride: set.Underride}
	if got := withoutRoom.Evaluate(messageEvent("hi"), ctx); got.RuleID != "@bob:example.org" || got.Notify {
		t.Errorf("the sender rule should outrank the underride rule: %+v", got)
	}
	// A rule for a different room or sender is not about this event.
	other := pushrules.Ruleset{Room: []pushrules.Rule{{RuleID: "!elsewhere:example.org", Enabled: true, Actions: []pushrules.Action{{Name: "notify"}}}},
		Sender: []pushrules.Rule{{RuleID: "@carol:example.org", Enabled: true, Actions: []pushrules.Action{{Name: "notify"}}}}}
	if got := other.Evaluate(messageEvent("hi"), ctx); got.Matched {
		t.Errorf("a rule for another room or sender fired: %+v", got)
	}
}

// TestEvaluateDefaults checks the rules the server ships with, because those are the ones
// that decide what a user who has changed nothing hears.
func TestEvaluateDefaults(t *testing.T) {
	const user = "@alice:example.org"
	set := pushrules.Defaults(user)
	ctx := func(levels ...int) pushrules.Context {
		c := pushrules.Context{UserID: user, RoomMemberCount: 5}
		if len(levels) > 0 {
			c.SenderLevel, c.SenderLevelSet = levels[0], true
		}
		return c
	}

	if got := set.Evaluate(messageEvent("plain message"), ctx()); !got.Notify || got.Highlight {
		t.Errorf("a plain message should notify without highlighting: %+v", got)
	}

	notice := messageEvent("bot says nothing")
	notice["content"] = map[string]any{"msgtype": "m.notice", "body": "bot says nothing"}
	if got := set.Evaluate(notice, ctx()); got.Notify {
		t.Errorf("a notice should be suppressed: %+v", got)
	}

	reaction := pushrules.Event{"sender": "@bob:example.org", "type": "m.reaction", "room_id": "!room:example.org",
		"content": map[string]any{"m.relates_to": map[string]any{}}}
	if got := set.Evaluate(reaction, ctx()); got.Notify {
		t.Errorf("a reaction should be suppressed: %+v", got)
	}

	mention := messageEvent("hey")
	mention["content"] = map[string]any{"msgtype": "m.text", "body": "hey",
		"m.mentions": map[string]any{"user_ids": []any{user}}}
	if got := set.Evaluate(mention, ctx()); !got.Notify || !got.Highlight {
		t.Errorf("a direct mention should notify and highlight: %+v", got)
	}

	// A room-wide mention needs the sender to hold the room notification level, which is
	// what keeps an ordinary member from lighting up the whole room.
	everyone := messageEvent("@room fire")
	everyone["content"] = map[string]any{"msgtype": "m.text", "body": "@room fire",
		"m.mentions": map[string]any{"room": true}}
	if got := set.Evaluate(everyone, ctx(0)); got.Highlight {
		t.Errorf("a room mention from a member without the level should not highlight: %+v", got)
	}
	if got := set.Evaluate(everyone, ctx(100)); !got.Highlight {
		t.Errorf("a room mention from someone with the level should highlight: %+v", got)
	}

	// The user-name content rule fires on the local part as a word.
	if got := set.Evaluate(messageEvent("alice, are you there"), ctx()); !got.Highlight {
		t.Errorf("the user-name rule should highlight: %+v", got)
	}
	if got := set.Evaluate(messageEvent("we aliased the room"), ctx()); got.Highlight {
		t.Errorf("%q is not a word match on the local part: %+v", "aliased", got)
	}

	// A one-to-one room gets a sound rather than a bare notification.
	direct := messageEvent("are you coming?")
	if got := set.Evaluate(direct, pushrules.Context{UserID: user, RoomMemberCount: 2}); got.Sound != "default" {
		t.Errorf("a message in a two-member room should set a sound: %+v", got)
	}
}

// TestEvaluateTweaks checks that the tweaks a rule sets reach the caller, including the
// ones this server does not model.
func TestEvaluateTweaks(t *testing.T) {
	set := pushrules.Ruleset{Override: []pushrules.Rule{{RuleID: "tweaks", Enabled: true, Actions: []pushrules.Action{
		{Name: "notify"},
		{IsTweak: true, Tweak: "highlight"},
		{IsTweak: true, Tweak: "sound", Value: "ring"},
		{IsTweak: true, Tweak: "org.example.flash", Value: true},
	}}}}
	got := set.Evaluate(messageEvent("x"), pushrules.Context{UserID: "@a:example.org"})
	if !got.Highlight {
		t.Errorf("a highlight tweak with no value is defined to be true: %+v", got)
	}
	if got.Sound != "ring" {
		t.Errorf("sound = %q", got.Sound)
	}
	if got.Tweaks["org.example.flash"] != true {
		t.Errorf("an unmodelled tweak was dropped: %v", got.Tweaks)
	}

	// A highlight tweak that says false says false.
	set.Override[0].Actions = []pushrules.Action{{Name: "notify"}, {IsTweak: true, Tweak: "highlight", Value: false}}
	if got := set.Evaluate(messageEvent("x"), pushrules.Context{UserID: "@a:example.org"}); got.Highlight {
		t.Errorf("highlight false was read as true: %+v", got)
	}
}
