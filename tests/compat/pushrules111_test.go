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
