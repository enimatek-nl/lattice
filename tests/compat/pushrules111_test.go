package compat

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/api"
)

// v111OverrideRules is the "Default Override Rules" list of the specification as of
// v1.11, in the order the section prints them. That order is not decoration: it is
// what a client renders and, because the first rule that matches decides, what wins.
var v111OverrideRules = []string{
	".m.rule.master",
	".m.rule.suppress_notices",
	".m.rule.invite_for_me",
	".m.rule.member_event",
	".m.rule.is_user_mention",
	".m.rule.contains_display_name",
	".m.rule.is_room_mention",
	".m.rule.roomnotif",
	".m.rule.tombstone",
	".m.rule.reaction",
	".m.rule.room.server_acl",
	".m.rule.suppress_edits",
}

// pushRuleOf reads one rule of one kind.
func pushRuleOf(t *testing.T, server *Server, session *Session, kind, ruleID string) map[string]any {
	t.Helper()
	return server.MustDo(http.MethodGet,
		"/_matrix/client/v3/pushrules/global/"+kind+"/"+url.PathEscape(ruleID),
		session.Token, nil, http.StatusOK).Body
}

// ruleMissing reports whether the server answers a rule with 404 M_NOT_FOUND.
func ruleMissing(server *Server, session *Session, kind, ruleID string) bool {
	resp := server.Do(http.MethodGet,
		"/_matrix/client/v3/pushrules/global/"+kind+"/"+url.PathEscape(ruleID), session.Token, nil)
	return resp.Status == http.StatusNotFound
}

// requireCondition checks one condition of a rule: its kind first, then the parameters
// the specification names for it.
func requireCondition(t *testing.T, rule map[string]any, ruleID string, at int, want map[string]any) {
	t.Helper()
	raw, _ := rule["conditions"].([]any)
	if at >= len(raw) {
		t.Errorf("%s carries %d conditions, want one at %d: %v", ruleID, len(raw), at, raw)
		return
	}
	entry, ok := raw[at].(map[string]any)
	if !ok {
		t.Errorf("%s condition %d is not an object: %v", ruleID, at, raw[at])
		return
	}
	for key, value := range want {
		if got := fmt.Sprintf("%v", entry[key]); got != fmt.Sprintf("%v", value) {
			t.Errorf("%s condition %d: %s = %v, want %v", ruleID, at, key, got, value)
		}
	}
}

// TestDefaultOverrideRulesAreTheOnesTheServerAdvertises pins the override list a client
// is shown, and the rule a settings screen switches on and off.
//
// The server advertises v1.11 in /versions, and v1.11 has twelve override rules. Eight
// were seeded, so `.m.rule.is_room_mention` — the rule an Element account uses for
// "notify me when the room is mentioned" — answered 404 M_NOT_FOUND and the switch ended
// in an error message. `.m.rule.is_user_mention`, `.m.rule.room.server_acl` and
// `.m.rule.suppress_edits` were missing beside it.
func TestDefaultOverrideRulesAreTheOnesTheServerAdvertises(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	// The premise, measured rather than assumed: this is the version the rule set below
	// has to answer for.
	versions := server.MustDo(http.MethodGet, "/_matrix/client/versions", "", nil, http.StatusOK)
	listed, _ := versions.Body["versions"].([]any)
	advertises111 := false
	for _, entry := range listed {
		if entry == "v1.11" {
			advertises111 = true
		}
	}
	if !advertises111 {
		t.Fatalf("the rule set below is the one of v1.11, which /versions does not advertise: %s", versions.Raw)
	}

	order := ruleOrder(t, server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/",
		alice.Token, nil, http.StatusOK).Body, "override")
	if strings.Join(order, " ") != strings.Join(v111OverrideRules, " ") {
		t.Fatalf("the override list is\n  %v\nwant\n  %v", order, v111OverrideRules)
	}

	// The mention rule names its own recipient: a rule that is a stored row has no other
	// way to say "my Matrix ID".
	user := pushRuleOf(t, server, alice, "override", ".m.rule.is_user_mention")
	requireCondition(t, user, ".m.rule.is_user_mention", 0, map[string]any{
		"kind":  "event_property_contains",
		"key":   `content.m\.mentions.user_ids`,
		"value": alice.UserID,
	})
	if actions := actionsOf(user["actions"]); !actions.notifies() ||
		!actions.hasTweak("highlight") || !actions.hasTweak("sound") {
		t.Errorf("a user mention should notify with a sound and a highlight, actions are %v", user["actions"])
	}

	// A room mention needs both halves: the flag on the event, and the sender's power to
	// make one.
	room := pushRuleOf(t, server, alice, "override", ".m.rule.is_room_mention")
	requireCondition(t, room, ".m.rule.is_room_mention", 0, map[string]any{
		"kind": "event_property_is", "key": `content.m\.mentions.room`, "value": true,
	})
	requireCondition(t, room, ".m.rule.is_room_mention", 1, map[string]any{
		"kind": "sender_notification_permission", "key": "room",
	})
	if actions := actionsOf(room["actions"]); !actions.notifies() || !actions.hasTweak("highlight") {
		t.Errorf("a room mention should notify with a highlight, actions are %v", room["actions"])
	}

	acl := pushRuleOf(t, server, alice, "override", ".m.rule.room.server_acl")
	requireCondition(t, acl, ".m.rule.room.server_acl", 0, map[string]any{
		"kind": "event_match", "key": "type", "pattern": "m.room.server_acl",
	})
	requireCondition(t, acl, ".m.rule.room.server_acl", 1, map[string]any{
		"kind": "event_match", "key": "state_key", "pattern": "",
	})
	edited := pushRuleOf(t, server, alice, "override", ".m.rule.suppress_edits")
	requireCondition(t, edited, ".m.rule.suppress_edits", 0, map[string]any{
		"kind": "event_property_is", "key": `content.m\.relates_to.rel_type`, "value": "m.replace",
	})
	for _, rule := range []map[string]any{acl, edited} {
		if got := actionsOf(rule["actions"]); len(got) != 0 {
			t.Errorf("%s suppresses with %v, want the empty action list of the specification",
				rule["rule_id"], rule["actions"])
		}
	}

	// The request that answered 404 before: a client switching room mentions off.
	for _, enabled := range []bool{false, true} {
		server.MustDo(http.MethodPut,
			"/_matrix/client/v3/pushrules/global/override/.m.rule.is_room_mention/enabled",
			alice.Token, map[string]any{"enabled": enabled}, http.StatusOK)
		if room := pushRuleOf(t, server, alice, "override", ".m.rule.is_room_mention"); room["enabled"] != enabled {
			t.Errorf("switching the room mention rule to %v did not stick: %v", enabled, room)
		}
	}
}

// TestMentionsNotifyTheWayTheirRulesSay drives the mention rules with the events they are
// written for, and checks the two things that were wrong without them: an `m.mentions`
// mention is what highlights a user now, and an edit nobody asked to hear about stays
// quiet.
func TestMentionsNotifyTheWayTheirRulesSay(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	carol := server.Register("carol", "kettle")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	for _, account := range []*Session{bob, carol} {
		server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, account.Token, nil, http.StatusOK)
	}

	send := func(from *Session, txnID string, content map[string]any) string {
		resp := server.MustDo(http.MethodPut,
			"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/"+txnID,
			from.Token, content, http.StatusOK)
		return resp.String("event_id")
	}
	// No body carries a localpart, so the deprecated contains_display_name and
	// contains_user_name rules stay out of everything that follows.
	mentioned := send(alice, "men-1", map[string]any{
		"msgtype": "m.text", "body": "hello there",
		"m.mentions": map[string]any{"user_ids": []any{bob.UserID}},
	})
	roomMention := send(alice, "men-2", map[string]any{
		"msgtype": "m.text", "body": "hello everyone", "m.mentions": map[string]any{"room": true},
	})
	// Carol holds no power in the room and notifications.room defaults to 50, so her
	// attempt at a room mention is an ordinary message as far as the rules are concerned.
	weakMention := send(carol, "men-3", map[string]any{
		"msgtype": "m.text", "body": "hello everyone", "m.mentions": map[string]any{"room": true},
	})
	edited := send(alice, "men-4", map[string]any{
		"msgtype": "m.text", "body": "* hello there",
		"m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": mentioned},
	})
	ordinary := send(alice, "men-5", map[string]any{"msgtype": "m.text", "body": "an ordinary message"})

	// notificationsOf reads what one account was told about each event, with the actions of
	// the rule that decided it.
	notificationsOf := func(account *Session) map[string]actionList {
		listed := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications",
			account.Token, nil, http.StatusOK)
		entries, _ := listed.Body["notifications"].([]any)
		out := map[string]actionList{}
		for _, raw := range entries {
			entry, _ := raw.(map[string]any)
			event, _ := entry["event"].(map[string]any)
			id, _ := event["event_id"].(string)
			out[id] = actionsOf(entry["actions"])
		}
		return out
	}
	bobSaw, carolSaw := notificationsOf(bob), notificationsOf(carol)

	yes, no := true, false
	for _, check := range []struct {
		who     string
		seen    map[string]actionList
		event   string
		label   string
		present *bool
		highlit *bool
	}{
		{who: "bob", seen: bobSaw, event: mentioned, highlit: &yes,
			label: "a mention of bob by Matrix ID highlights him"},
		{who: "bob", seen: bobSaw, event: roomMention, highlit: &yes,
			label: "an @room mention by a room administrator highlights the room"},
		{who: "bob", seen: bobSaw, event: weakMention, highlit: &no,
			label: "an @room mention by a member without the power is no highlight"},
		{who: "bob", seen: bobSaw, event: ordinary, highlit: &no,
			label: "an ordinary message notifies without highlighting"},
		{who: "bob", seen: bobSaw, event: edited, present: &no,
			label: "an edit is silenced by .m.rule.suppress_edits"},
		{who: "carol", seen: carolSaw, event: roomMention, highlit: &yes,
			label: "the rest of the room hears an @room mention too"},
		{who: "carol", seen: carolSaw, event: mentioned, highlit: &no,
			label: "a mention of somebody else is not a highlight for carol"},
	} {
		actions, there := check.seen[check.event]
		if check.present != nil && there != *check.present {
			t.Errorf("bob's list says %s: present = %v, want %v", check.label, there, *check.present)
			continue
		}
		if check.highlit == nil || !there {
			continue
		}
		if got := actions.hasTweak("highlight"); got != *check.highlit {
			t.Errorf("%s: highlight of %s = %v, want %v (actions %v)",
				check.who, check.label, got, *check.highlit, actions)
		}
	}
}

// TestAnAccountWrittenBeforeTheRulesIsGivenThem is the other half of the fix.
//
// Default rules are rows written once, when an account is created, so learning a rule of
// the specification helps exactly the accounts that do not exist yet. An account of the
// previous release has no row for `.m.rule.is_room_mention` and stays broken on every
// server that has been running since before the rule was added — invisible to a suite that
// registers, visible on every account of a server that has users.
func TestAnAccountWrittenBeforeTheRulesIsGivenThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	server := NewServerWithDatabase(t, path, api.Config{})
	aged := server.Register("aged", "wonderland")
	current := server.Register("current", "wonderland")

	for _, id := range []string{".m.rule.is_user_mention", ".m.rule.is_room_mention",
		".m.rule.room.server_acl", ".m.rule.suppress_edits"} {
		server.MustDo(http.MethodDelete, "/_matrix/client/v3/pushrules/global/override/"+url.PathEscape(id),
			aged.Token, nil, http.StatusOK)
	}
	// What a person switched off has to stay switched off, and what a person deleted on an
	// account that already holds this rule set has to stay deleted: the repair promises the
	// set once per account, not once per start.
	server.MustDo(http.MethodPut,
		"/_matrix/client/v3/pushrules/global/override/.m.rule.contains_display_name/enabled",
		aged.Token, map[string]any{"enabled": false}, http.StatusOK)
	server.MustDo(http.MethodDelete, "/_matrix/client/v3/pushrules/global/override/.m.rule.tombstone",
		current.Token, nil, http.StatusOK)
	// Storage rather than HTTP, because no endpoint writes it: this is the row an account of
	// the previous release leaves behind, and the marker is what the repair reads.
	if err := server.db.UserRepo.SetPushRulesVersion(aged.UserID, 1); err != nil {
		t.Fatalf("writing the rule set version an older release leaves: %v", err)
	}

	restarted := NewServerWithDatabase(t, path, api.Config{})
	order := ruleOrder(t, restarted.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/",
		aged.Token, nil, http.StatusOK).Body, "override")
	if strings.Join(order, " ") != strings.Join(v111OverrideRules, " ") {
		t.Fatalf("the restarted server lists\n  %v\nfor an account written before those rules, want\n  %v",
			order, v111OverrideRules)
	}
	if room := pushRuleOf(t, restarted, aged, "override", ".m.rule.is_room_mention"); room["enabled"] != true {
		t.Errorf("a rule that arrived by repair is not the rule a new account is given: %v", room)
	}
	if name := pushRuleOf(t, restarted, aged, "override", ".m.rule.contains_display_name"); name["enabled"] != false {
		t.Errorf("the repair reset what the account had switched off: %v", name)
	}
	synced := restarted.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0",
		aged.Token, nil, http.StatusOK)
	if !strings.Contains(string(synced.Raw), "m.push_rules") ||
		!strings.Contains(string(synced.Raw), ".m.rule.is_room_mention") {
		t.Errorf("the m.push_rules account data did not follow the repair: %s", synced.Raw)
	}
	// The account that already held this rule set keeps what it deleted.
	if !ruleMissing(restarted, current, "override", ".m.rule.tombstone") {
		t.Error(".m.rule.tombstone came back on an account that was already current")
	}
}

// pushKindOf reads the rules of one kind the way the endpoint answers them.
func pushKindOf(t *testing.T, server *Server, session *Session, kind string) []map[string]any {
	t.Helper()
	answer := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/global/"+kind,
		session.Token, nil, http.StatusOK).Body
	global, _ := answer["global"].(map[string]any)
	raw, _ := global[kind].([]any)
	rules := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if rule, ok := entry.(map[string]any); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

// actionTweak returns the value a set_tweak of that name carries. The second answer says
// whether the tweak is there at all, which is what the specification's highlight action
// amounts to: it is written without a value, and no value means true.
func actionTweak(actions any, tweak string) (any, bool) {
	list, _ := actions.([]any)
	for _, entry := range list {
		tweaked, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := tweaked["set_tweak"].(string); name == tweak {
			return tweaked["value"], true
		}
	}
	return nil, false
}

// TestContentRulesCarryThePatternTheirKindRequires is the rule set a Rust client can read.
//
// `.m.rule.contains_user_name` is the only content rule this server seeds, and it was
// seeded with no condition at all — while the wire form reads a content rule's pattern out
// of a stored content.body condition. The answer therefore had no `pattern`, and ruma,
// which is the Rust SDK behind smaltino and Element X, refused the whole rule set with
// `missing field 'pattern'` and fell back to its own defaults: a second device of an
// account stopped showing what the first one changed, even though the account data and both
// syncs carried the change. A content rule always carries a pattern, so it is seeded with
// the condition the pattern is: the account's own local part, which is what the
// specification's "[the local part of the user's Matrix ID]" asks a server to substitute.
func TestContentRulesCarryThePatternTheirKindRequires(t *testing.T) {
	server := NewServer(t)
	patterned := server.Register("patterned", "wonderland")

	rules := pushKindOf(t, server, patterned, "content")
	if len(rules) != 1 {
		t.Fatalf("the content kind holds %d rules, want the one the specification defines: %v",
			len(rules), rules)
	}
	rule := rules[0]
	if rule["rule_id"] != ".m.rule.contains_user_name" {
		t.Errorf("the content kind holds %v, want .m.rule.contains_user_name", rule["rule_id"])
	}
	if pattern, named := rule["pattern"].(string); !named {
		t.Errorf("a content rule answered with no pattern at all, which costs a strict client "+
			"the whole rule set: %v", rule)
	} else if pattern != "patterned" {
		t.Errorf("pattern = %q, want the local part of the account", pattern)
	}
	actions, _ := rule["actions"].([]any)
	if len(actions) == 0 || actions[0] != "notify" {
		t.Errorf("the rule's actions do not start with notify: %v", actions)
	}
	if value, tweaked := actionTweak(actions, "sound"); !tweaked || value != "default" {
		t.Errorf("the specification gives this rule a sound of default: %v", actions)
	}
	if _, tweaked := actionTweak(actions, "highlight"); !tweaked {
		t.Errorf("a mention of the local part has to highlight: %v", actions)
	}

	// The field belongs to content rules and to nothing else. Reading a stored
	// content.body condition was enough to put one on an override rule too, which is what
	// `.m.rule.roomnotif` published beside its conditions.
	for _, override := range pushKindOf(t, server, patterned, "override") {
		if _, named := override["pattern"]; named {
			t.Errorf("%s is an override rule and carries a pattern of its own: %v",
				override["rule_id"], override)
		}
	}

	// Beside it: the deprecated display name rule is an override rule, and v1.11 gives it
	// one condition that names no key and no pattern, plus the same sound.
	display := pushRuleOf(t, server, patterned, "override", ".m.rule.contains_display_name")
	if raw, _ := display["conditions"].([]any); len(raw) != 1 {
		t.Errorf(".m.rule.contains_display_name carries %d conditions, want the one the "+
			"specification defines: %v", len(raw), display["conditions"])
	}
	requireCondition(t, display, ".m.rule.contains_display_name", 0,
		map[string]any{"kind": "contains_display_name"})
	if value, tweaked := actionTweak(display["actions"], "sound"); !tweaked || value != "default" {
		t.Errorf("the specification gives this rule a sound of default: %v", display["actions"])
	}

	// A rule a person writes themselves has to answer the same way, because one PUT
	// without a pattern would otherwise cost every Rust device on the account its rules.
	server.MustDo(http.MethodPut,
		"/_matrix/client/v3/pushrules/global/content/"+url.PathEscape("cake*lie"),
		patterned.Token, map[string]any{"actions": []any{"notify"}}, http.StatusOK)
	for _, own := range pushKindOf(t, server, patterned, "content") {
		if _, named := own["pattern"].(string); !named {
			t.Errorf("a content rule answered with no pattern: %v", own)
		}
	}
}

// TestADefaultRuleKeepsTheConditionsOnlyTheServerWrites covers the two ways a default rule
// used to lose what makes it that rule.
//
// One: a settings screen changing a rule's actions sends actions and nothing else, and the
// endpoint read that silence for "no conditions" — so switching a notice rule's actions
// turned `.m.rule.suppress_notices` into a rule matching every event. Two: rules are rows
// written once, so an account of the previous build keeps a content rule with no condition
// to read a pattern from, and no endpoint can put it back. The conditions of a default rule
// are the one part of it a person cannot write, which is what lets the repair pass take the
// server's own copy of them without undoing anything anybody asked for.
func TestADefaultRuleKeepsTheConditionsOnlyTheServerWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	server := NewServerWithDatabase(t, path, api.Config{})
	aged := server.Register("longstanding", "wonderland")

	server.MustDo(http.MethodPut,
		"/_matrix/client/v3/pushrules/global/override/"+url.PathEscape(".m.rule.suppress_notices"),
		aged.Token, map[string]any{"actions": []any{"dont_notify"}}, http.StatusOK)
	stillThere := pushRuleOf(t, server, aged, "override", ".m.rule.suppress_notices")
	requireCondition(t, stillThere, ".m.rule.suppress_notices", 0, map[string]any{
		"kind": "event_match", "key": "content.msgtype", "pattern": "m.notice",
	})
	if got := fmt.Sprintf("%v", stillThere["actions"]); got != "[dont_notify]" {
		t.Errorf("the actions a PUT brought did not arrive: %v", stillThere["actions"])
	}
	if stillThere["default"] != true {
		t.Errorf("changing the actions of a default rule made it a rule of the person: %v", stillThere)
	}

	// Storage rather than HTTP, because no endpoint writes this: it is the row the previous
	// build left behind — a rule with the right name and no condition of its own.
	stored, err := server.db.PushRuleRepo.Get(aged.UserID, "content", ".m.rule.contains_user_name")
	if err != nil {
		t.Fatalf("reading the content rule of a fresh account: %v", err)
	}
	stored.Conditions = ""
	if err := server.db.PushRuleRepo.Set(stored); err != nil {
		t.Fatalf("storing the rule as an older build writes it: %v", err)
	}
	// What a person switched off has to stay switched off through the repair that follows.
	server.MustDo(http.MethodPut,
		"/_matrix/client/v3/pushrules/global/content/"+url.PathEscape(".m.rule.contains_user_name")+"/enabled",
		aged.Token, map[string]any{"enabled": false}, http.StatusOK)
	if err := server.db.UserRepo.SetPushRulesVersion(aged.UserID, 2); err != nil {
		t.Fatalf("writing the rule set version an older release leaves: %v", err)
	}

	restarted := NewServerWithDatabase(t, path, api.Config{})
	repaired := pushRuleOf(t, restarted, aged, "content", ".m.rule.contains_user_name")
	if pattern, _ := repaired["pattern"].(string); pattern != "longstanding" {
		t.Errorf("an account written before the pattern keeps reading no pattern at all: %v", repaired)
	}
	if repaired["enabled"] != false {
		t.Errorf("the repair reset what the account had switched off: %v", repaired)
	}
	// Clients read the rules out of the m.push_rules account data too, and that is the copy
	// the Rust SDK parses — which is where the reported symptom lived.
	synced := restarted.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0",
		aged.Token, nil, http.StatusOK)
	if !strings.Contains(string(synced.Raw), `"pattern":"longstanding"`) {
		t.Errorf("the m.push_rules account data did not follow the repair: %s", synced.Raw)
	}
}
