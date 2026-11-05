package unit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/pushrules"
)

// TestPushRuleDefaults checks the rule set a user is given before they have configured
// anything. The rules that name the user are the part worth pinning: a placeholder left
// in their place would look right and notify them of nothing.
func TestPushRuleDefaults(t *testing.T) {
	set := pushrules.Defaults("@alice:example.org")

	for _, kind := range pushrules.Kinds() {
		if set.KindRules(kind) == nil {
			t.Errorf("the ruleset has no %s section", kind)
		}
	}
	override := set.KindRules(pushrules.KindOverride)
	if len(override) == 0 {
		t.Fatalf("there are no override rules")
	}
	if override[0].RuleID != ".m.rule.master" {
		t.Errorf("the first override rule is %s; the master rule outranks the rest, so it must come first", override[0].RuleID)
	}
	if override[0].Enabled {
		t.Errorf("the master rule ships disabled; enabling it silences the user")
	}

	want := map[pushrules.Kind][]string{
		pushrules.KindOverride: {
			".m.rule.master", ".m.rule.suppress_notices", ".m.rule.invite_for_me", ".m.rule.member_event",
			".m.rule.is_user_mention", ".m.rule.contains_display_name", ".m.rule.is_room_mention",
			".m.rule.roomnotif", ".m.rule.tombstone", ".m.rule.reaction", ".m.rule.room.server_acl",
			".m.rule.suppress_edits",
		},
		pushrules.KindContent: {".m.rule.contains_user_name"},
		pushrules.KindUnderride: {
			".m.rule.call", ".m.rule.encrypted_room_one_to_one", ".m.rule.room_one_to_one",
			".m.rule.message", ".m.rule.encrypted",
		},
	}
	for kind, ids := range want {
		rules := set.KindRules(kind)
		if len(rules) != len(ids) {
			t.Errorf("%s holds %d rules, want %d", kind, len(rules), len(ids))
			continue
		}
		for i, id := range ids {
			if rules[i].RuleID != id {
				t.Errorf("%s[%d] = %s, want %s", kind, i, rules[i].RuleID, id)
			}
			if !rules[i].Default {
				t.Errorf("%s is presented as a rule the user wrote", id)
			}
		}
	}

	// The invite rule must name this user, not a stand-in.
	for _, rule := range override {
		if rule.RuleID != ".m.rule.invite_for_me" {
			continue
		}
		var stateKey string
		for _, condition := range rule.Conditions {
			if condition["key"] == "state_key" {
				stateKey, _ = condition["pattern"].(string)
			}
		}
		if stateKey != "@alice:example.org" {
			t.Errorf("the invite rule matches an invite for %q, want @alice:example.org", stateKey)
		}
	}
	// The oldest mention rule matches the local part, not the whole ID.
	content := set.KindRules(pushrules.KindContent)[0]
	if content.Pattern == nil || *content.Pattern != "alice" {
		t.Errorf("the user-name rule pattern = %v, want alice", content.Pattern)
	}
}

// TestPushRuleActionsRoundTrip checks the union form of an action: a bare word or a
// tweak object. A client that wrote a custom sound must read it back.
func TestPushRuleActionsRoundTrip(t *testing.T) {
	raw := `["notify","dont_notify",{"set_tweak":"sound","value":"cakealarm.wav"},{"set_tweak":"highlight"}]`
	var actions []pushrules.Action
	if err := json.Unmarshal([]byte(raw), &actions); err != nil {
		t.Fatalf("decoding actions: %v", err)
	}
	if len(actions) != 4 {
		t.Fatalf("decoded %d actions, want 4", len(actions))
	}
	if !actions[0].Notify() || actions[1].Notify() {
		t.Errorf("notify asks for a notification and dont_notify does not")
	}
	if actions[2].Tweak != "sound" || actions[2].Value != "cakealarm.wav" {
		t.Errorf("the sound tweak decoded as %+v", actions[2])
	}
	if actions[3].Value != nil {
		t.Errorf("a tweak with no value must keep it absent, got %v", actions[3].Value)
	}
	out, err := json.Marshal(actions)
	if err != nil {
		t.Fatalf("encoding actions: %v", err)
	}
	if string(out) != raw {
		t.Errorf("actions changed on the way through the server:\n got %s\nwant %s", out, raw)
	}
}

// TestValidateActions checks what a client may ask for. The two legacy actions are
// accepted because the specification says they are to be ignored rather than rejected.
func TestValidateActions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		actions []pushrules.Action
		wantErr bool
	}{
		{name: "notify", actions: []pushrules.Action{{Name: "notify"}}},
		{name: "legacy dont_notify", actions: []pushrules.Action{{Name: "dont_notify"}}},
		{name: "legacy coalesce", actions: []pushrules.Action{{Name: "coalesce"}}},
		{name: "no actions means silence", actions: []pushrules.Action{}},
		{
			name:    "unknown action",
			actions: []pushrules.Action{{Name: "shout_loudly"}},
			wantErr: true,
		},
		{
			name:    "empty action",
			actions: []pushrules.Action{{Name: ""}},
			wantErr: true,
		},
		{
			name:    "tweak without a name",
			actions: []pushrules.Action{{IsTweak: true}},
			wantErr: true,
		},
	} {
		err := pushrules.ValidateActions(tc.actions)
		if tc.wantErr && err == nil {
			t.Errorf("%s: accepted, want it refused", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: refused: %v", tc.name, err)
		}
	}
}

// TestValidateRuleID checks the identifiers a client may write. A leading dot marks a
// rule the server provided, and letting a client claim one would let it overwrite the
// default set with something that still looks like ours.
func TestValidateRuleID(t *testing.T) {
	for _, tc := range []struct {
		ruleID  string
		wantErr bool
	}{
		{ruleID: "nocake"},
		{ruleID: "SSByZWFsbHkgbGlrZSBjYWtl"},
		{ruleID: ".m.rule.message", wantErr: true},
		{ruleID: "", wantErr: true},
		{ruleID: "one/two", wantErr: true},
		{ruleID: `one\two`, wantErr: true},
	} {
		err := pushrules.ValidateRuleID(tc.ruleID)
		if tc.wantErr && err == nil {
			t.Errorf("%q: accepted, want it refused", tc.ruleID)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%q: refused: %v", tc.ruleID, err)
		}
	}
}

// TestRulesetFromAccountData checks the read side of stored rules: a stored value must
// come back unchanged, and a stored value that is not a ruleset must not be quietly
// replaced by the defaults.
func TestRulesetFromAccountData(t *testing.T) {
	const stored = `{"global":{"override":[{"rule_id":"mine","default":false,"enabled":true,"actions":["notify"],"conditions":[{"kind":"event_match","key":"content.body","pattern":"cake"}]}],"content":[],"room":[],"sender":[],"underride":[]}}`
	set, err := pushrules.RulesetFromAccountData(stored)
	if err != nil {
		t.Fatalf("reading stored rules: %v", err)
	}
	rules := set.KindRules(pushrules.KindOverride)
	if len(rules) != 1 || rules[0].RuleID != "mine" || len(rules[0].Conditions) != 1 {
		t.Fatalf("the user's rule did not survive: %+v", rules)
	}
	if rules[0].Conditions[0]["pattern"] != "cake" {
		t.Errorf("the condition parameters changed: %v", rules[0].Conditions[0])
	}

	// A ruleset written before this server knew about a kind still answers with all five.
	partial, err := pushrules.RulesetFromAccountData(`{"global":{"override":[],"content":[]}}`)
	if err != nil {
		t.Fatalf("reading a partial ruleset: %v", err)
	}
	for _, kind := range pushrules.Kinds() {
		if partial.KindRules(kind) == nil {
			t.Errorf("%s came back missing rather than empty", kind)
		}
	}

	for _, bad := range []string{``, `not json`, `{"global":[]}`} {
		if _, err := pushrules.RulesetFromAccountData(bad); err == nil {
			t.Errorf("%q was read as a ruleset; it should be refused", strings.TrimSpace(bad))
		}
	}
}

// TestParseKind checks the rule kinds a path may name.
func TestParseKind(t *testing.T) {
	for _, name := range []string{"override", "content", "room", "sender", "underride"} {
		if _, err := pushrules.ParseKind(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"", "global", "Overrides", "room;drop"} {
		if _, err := pushrules.ParseKind(name); err == nil {
			t.Errorf("%q was accepted as a rule kind", name)
		}
	}
}
