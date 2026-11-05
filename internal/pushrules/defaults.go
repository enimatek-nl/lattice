package pushrules

import (
	"encoding/json"
	"strings"
)

// Defaults returns the server-default rules for a user, in the order the specification
// lists them.
//
// Three of them name the user: an invite rule matches the user's Matrix ID, a mention
// rule matches it in the `m.mentions` property, and the oldest mention rule matches the
// local part of it. That is why the defaults are built per user instead of stored once:
// the rule set the user can see and edit is theirs, and a placeholder left in its place
// would notify them of nothing.
//
// The set follows the pinned specification release. The rules the specification added in
// v1.4, v1.7 and v1.9 are included, and the three it deprecated in v1.7
// (`contains_display_name`, `roomnotif`, `contains_user_name`) are still enabled — the
// specification says they should apply when an event carries no `m.mentions` property,
// which is a rule about matching, not about whether the rule exists.
func Defaults(userID string) Ruleset {
	localpart := userID
	if at := strings.IndexByte(localpart, ':'); at >= 0 {
		localpart = strings.TrimPrefix(localpart[1:at], "@")
	}
	set := Ruleset{
		Override: []Rule{
			// The master rule outranks every other rule, user-defined ones
			// included, so it is checked before any of them and it is kept first
			// in the list rather than sorted with the rest.
			{RuleID: ".m.rule.master", Default: true, Enabled: false, Actions: []Action{}},
			{RuleID: ".m.rule.suppress_notices", Default: true, Enabled: true, Actions: []Action{},
				Conditions: []map[string]any{eventMatch("content.msgtype", "m.notice")}},
			{RuleID: ".m.rule.invite_for_me", Default: true, Enabled: true,
				Actions: []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "sound", Value: "default"}},
				Conditions: []map[string]any{
					eventMatch("type", "m.room.member"),
					eventMatch("content.membership", "invite"),
					eventMatch("state_key", userID),
				}},
			{RuleID: ".m.rule.member_event", Default: true, Enabled: true, Actions: []Action{},
				Conditions: []map[string]any{eventMatch("type", "m.room.member")}},
			{RuleID: ".m.rule.is_user_mention", Default: true, Enabled: true,
				Actions: []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "sound", Value: "default"}, {IsTweak: true, Tweak: "highlight"}},
				Conditions: []map[string]any{{
					"kind": "event_property_contains",
					// The dot in the property name is escaped, as the
					// specification's dot-separated paths require.
					"key":   `content.m\.mentions.user_ids`,
					"value": userID,
				}}},
			{RuleID: ".m.rule.contains_display_name", Default: true, Enabled: true,
				Actions:    []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "sound", Value: "default"}, {IsTweak: true, Tweak: "highlight"}},
				Conditions: []map[string]any{{"kind": "contains_display_name"}}},
			{RuleID: ".m.rule.is_room_mention", Default: true, Enabled: true,
				Actions: []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "highlight"}},
				Conditions: []map[string]any{{
					"kind":  "event_property_is",
					"key":   `content.m\.mentions.room`,
					"value": true,
				}, {
					"kind": "sender_notification_permission",
					"key":  "room",
				}}},
			{RuleID: ".m.rule.roomnotif", Default: true, Enabled: true,
				Actions: []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "highlight"}},
				Conditions: []map[string]any{
					eventMatch("content.body", "@room"),
					{"kind": "sender_notification_permission", "key": "room"},
				}},
			{RuleID: ".m.rule.tombstone", Default: true, Enabled: true,
				Actions:    []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "highlight"}},
				Conditions: []map[string]any{eventMatch("type", "m.room.tombstone"), eventMatch("state_key", "")}},
			{RuleID: ".m.rule.reaction", Default: true, Enabled: true, Actions: []Action{},
				Conditions: []map[string]any{eventMatch("type", "m.reaction")}},
			{RuleID: ".m.rule.room.server_acl", Default: true, Enabled: true, Actions: []Action{},
				Conditions: []map[string]any{eventMatch("type", "m.room.server_acl"), eventMatch("state_key", "")}},
			{RuleID: ".m.rule.suppress_edits", Default: true, Enabled: true, Actions: []Action{},
				Conditions: []map[string]any{{
					"kind":  "event_property_is",
					"key":   `content.m\.relates_to.rel_type`,
					"value": "m.replace",
				}}},
		},
		Content: []Rule{{
			RuleID:  ".m.rule.contains_user_name",
			Default: true, Enabled: true,
			Pattern: &localpart,
			Actions: []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "sound", Value: "default"}, {IsTweak: true, Tweak: "highlight"}},
		}},
		Room:   []Rule{},
		Sender: []Rule{},
		Underride: []Rule{
			{RuleID: ".m.rule.call", Default: true, Enabled: true,
				Actions:    []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "sound", Value: "ring"}},
				Conditions: []map[string]any{eventMatch("type", "m.call.invite")}},
			{RuleID: ".m.rule.encrypted_room_one_to_one", Default: true, Enabled: true,
				Actions:    []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "sound", Value: "default"}},
				Conditions: []map[string]any{{"kind": "room_member_count", "is": "2"}, eventMatch("type", "m.room.encrypted")}},
			{RuleID: ".m.rule.room_one_to_one", Default: true, Enabled: true,
				Actions:    []Action{{Name: actionNotify}, {IsTweak: true, Tweak: "sound", Value: "default"}},
				Conditions: []map[string]any{{"kind": "room_member_count", "is": "2"}, eventMatch("type", "m.room.message")}},
			{RuleID: ".m.rule.message", Default: true, Enabled: true, Actions: []Action{{Name: actionNotify}},
				Conditions: []map[string]any{eventMatch("type", "m.room.message")}},
			{RuleID: ".m.rule.encrypted", Default: true, Enabled: true, Actions: []Action{{Name: actionNotify}},
				Conditions: []map[string]any{eventMatch("type", "m.room.encrypted")}},
		},
	}
	return set
}

// eventMatch builds an `event_match` condition.
func eventMatch(key, pattern string) map[string]any {
	return map[string]any{"kind": "event_match", "key": key, "pattern": pattern}
}

// AccountDataType is the account data type a user's rules are synchronised under.
const AccountDataType = "m.push_rules"

// ScopeName is the key a ruleset is stored and served under.
const ScopeName = "global"

// RulesetFromAccountData reads a stored ruleset. A stored value that is not a ruleset is
// an error rather than an empty set: silently replacing the user's rules with defaults
// because one row went wrong would be worse than refusing the read.
func RulesetFromAccountData(raw string) (Ruleset, error) {
	var body struct {
		Global Ruleset `json:"global"`
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&body); err != nil {
		return Ruleset{}, err
	}
	set := body.Global
	// A ruleset that predates a kind this server now lists, or that was written by an
	// older version, may omit it. The response must always carry all five.
	if set.Override == nil {
		set.Override = []Rule{}
	}
	if set.Content == nil {
		set.Content = []Rule{}
	}
	if set.Room == nil {
		set.Room = []Rule{}
	}
	if set.Sender == nil {
		set.Sender = []Rule{}
	}
	if set.Underride == nil {
		set.Underride = []Rule{}
	}
	return set, nil
}
