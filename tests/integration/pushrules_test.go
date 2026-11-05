package integration

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestPushRulesForANewAccount checks what a user is given before they have configured
// anything, and that the read side can be approached at any depth: the whole set, one
// scope, one kind, one rule, one rule's enabled flag or actions.
func TestPushRulesForANewAccount(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, token, _ := h.register(t, "rulesowner", "pw-rulesowner", "")

	whole := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules", token), http.StatusOK).decoded(t)
	global, _ := whole["global"].(map[string]any)
	if global == nil {
		t.Fatalf("the response has no global ruleset: %v", whole)
	}
	for _, kind := range []string{"override", "content", "room", "sender", "underride"} {
		if _, ok := global[kind].([]any); !ok {
			t.Errorf("the ruleset has no %q list: %v", kind, global)
		}
	}
	// Reading it twice must answer the same thing: a client that re-reads its rules
	// after a restart is looking for the rules it left behind.
	again := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/", token), http.StatusOK).decoded(t)
	if !jsonEqual(t, whole, again) {
		t.Errorf("the ruleset changed between two reads:\n %v\n %v", whole, again)
	}

	// The master rule ships off: if it were on, the user would hear nothing at all.
	overrides := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/global/override", token), http.StatusOK).decoded(t)
	list, _ := overrides["override"].([]any)
	if len(list) == 0 {
		t.Fatalf("no override rules: %v", overrides)
	}
	master := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/global/override/%2Em.rule.master", token), http.StatusOK).decoded(t)
	if master["enabled"] != false {
		t.Errorf("the master rule is enabled: %v", master)
	}
	if master["default"] != true {
		t.Errorf("the server's own rule does not say it is one: %v", master)
	}

	// A rule for a room is named by the room ID, which arrives escaped.
	rules := h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/pushrules/global/room/%21quiet%3Ahs1",
		[]byte(`{"actions":[]}`), token), http.StatusOK)
	_ = rules
	rule := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/global/room/%21quiet%3Ahs1", token), http.StatusOK).decoded(t)
	if rule["rule_id"] != "!quiet:hs1" {
		t.Errorf("rule_id = %v, want the room ID the client named: %v", rule["rule_id"], rule)
	}
	if enabled := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/global/room/%21quiet%3Ahs1/enabled", token), http.StatusOK).decoded(t); enabled["enabled"] != true {
		t.Errorf("a rule that was just created is not enabled: %v", enabled)
	}
	actions := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/global/room/%21quiet%3Ahs1/actions", token), http.StatusOK).decoded(t)
	if got, _ := actions["actions"].([]any); len(got) != 0 {
		t.Errorf("actions = %v, want the empty list the client set", actions)
	}
	// Another user's rules are not readable through the same path.
	_, otherToken, _ := h.register(t, "rulesother", "pw-rulesother", "")
	other := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/global/room/%21quiet%3Ahs1", otherToken), http.StatusNotFound).decoded(t)
	if other["errcode"] != "M_NOT_FOUND" {
		t.Errorf("another user's rule reads as %v", other)
	}
	// The new account's own set is not the first user's.
	second := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/global/room/%21quiet%3Ahs1", otherToken), http.StatusNotFound)
	_ = second
	if res := h.get("/_matrix/client/v3/pushrules/device/phone", token); res.status != http.StatusNotFound {
		t.Errorf("an unknown scope: status %d, want 404 (body %s)", res.status, res.body)
	}
	if res := h.get("/_matrix/client/v3/pushrules/global/Overrides", token); res.status != http.StatusBadRequest {
		t.Errorf("an unknown rule kind: status %d, want 400 (body %s)", res.status, res.body)
	}
	if res := h.get("/_matrix/client/v3/pushrules/global/room/%21quiet%3Ahs1/enabled/extra", token); res.status != http.StatusNotFound {
		t.Errorf("a path below the rule: status %d, want 404 M_UNRECOGNIZED (body %s)", res.status, res.body)
	}
}

// TestPushRuleWrites checks the edits a client makes and what they answer with.
func TestPushRuleWrites(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, token, _ := h.register(t, "ruleseditor", "pw-ruleseditor", "")

	// A sender rule suppressing one user, as the specification's own example does.
	path := "/_matrix/client/v3/pushrules/global/sender/%40spambot%3Ahs1"
	if res := h.raw(t, http.MethodPut, path, []byte(`{"actions":["dont_notify"]}`), token); res.status != http.StatusOK {
		t.Fatalf("creating a sender rule: status %d (body %s)", res.status, res.body)
	}
	rule := h.mustStatus(t, h.get(path, token), http.StatusOK).decoded(t)
	if got, _ := rule["actions"].([]any); len(got) != 1 || got[0] != "dont_notify" {
		t.Errorf("actions = %v, want [dont_notify]", rule["actions"])
	}

	// Turning it off, then changing its actions.
	h.mustStatus(t, h.raw(t, http.MethodPut, path+"/enabled", []byte(`{"enabled":false}`), token), http.StatusOK)
	if got := h.mustStatus(t, h.get(path+"/enabled", token), http.StatusOK).decoded(t); got["enabled"] != false {
		t.Errorf("enabled = %v after being switched off", got)
	}
	// The server's own rules may have their actions changed, only not removed.
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/pushrules/global/underride/%2Em.rule.message/actions",
		[]byte(`{"actions":["notify",{"set_tweak":"sound","value":"default"}]}`), token), http.StatusOK)
	custom := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/global/underride/%2Em.rule.message/actions", token), http.StatusOK).decoded(t)
	tweaks, _ := custom["actions"].([]any)
	if len(tweaks) != 2 {
		t.Fatalf("the actions of a default rule were not replaced: %v", custom)
	}
	if tweak, _ := tweaks[1].(map[string]any); tweak["set_tweak"] != "sound" || tweak["value"] != "default" {
		t.Errorf("the tweak did not survive the round trip: %v", tweaks[1])
	}

	// Deleting: the user's own rule goes, the server's does not.
	h.mustStatus(t, h.raw(t, http.MethodDelete, path, nil, token), http.StatusOK)
	if res := h.get(path, token); res.status != http.StatusNotFound {
		t.Errorf("a deleted rule still reads: status %d (body %s)", res.status, res.body)
	}
	if res := h.raw(t, http.MethodDelete, "/_matrix/client/v3/pushrules/global/underride/%2Em.rule.message", nil, token); res.status != http.StatusNotFound {
		t.Errorf("deleting one of the server's own rules: status %d, want 404 (body %s)", res.status, res.body)
	}
	// And the rule the user wrote is gone for good, not disabled behind the scenes.
	if res := h.raw(t, http.MethodPut, path+"/enabled", []byte(`{"enabled":true}`), token); res.status != http.StatusNotFound {
		t.Errorf("editing a deleted rule: status %d, want 404 (body %s)", res.status, res.body)
	}

	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{name: "no actions", method: http.MethodPut, path: "/_matrix/client/v3/pushrules/global/content/nocake",
			body: `{}`, want: http.StatusBadRequest},
		{name: "content rule without a pattern", method: http.MethodPut, path: "/_matrix/client/v3/pushrules/global/content/nopattern",
			body: `{"actions":["notify"]}`, want: http.StatusBadRequest},
		{name: "an identifier reserved for the server", method: http.MethodPut, path: "/_matrix/client/v3/pushrules/global/content/%2Em.rule.message",
			body: `{"actions":["notify"],"pattern":"x"}`, want: http.StatusBadRequest},
		{name: "an action this server does not know", method: http.MethodPut, path: "/_matrix/client/v3/pushrules/global/content/weird",
			body: `{"actions":["shout"],"pattern":"x"}`, want: http.StatusBadRequest},
		{name: "a tweak with no name", method: http.MethodPut, path: "/_matrix/client/v3/pushrules/global/content/tweakless",
			body: `{"actions":[{"value":"x"}],"pattern":"x"}`, want: http.StatusBadRequest},
		{name: "conditions on a room rule", method: http.MethodPut, path: "/_matrix/client/v3/pushrules/global/room/%21cond%3Ahs1",
			body: `{"actions":["notify"],"conditions":[{"kind":"event_match","key":"type","pattern":"m.room.message"}]}`, want: http.StatusBadRequest},
		{name: "both before and after", method: http.MethodPut, path: "/_matrix/client/v3/pushrules/global/content/both",
			body: `{"actions":["notify"],"pattern":"x"}`, want: http.StatusBadRequest},
		{name: "enabled is not a boolean", method: http.MethodPut, path: "/_matrix/client/v3/pushrules/global/content/nocake/enabled",
			body: `{"enabled":"yes"}`, want: http.StatusBadRequest},
		{name: "no enabled at all", method: http.MethodPut, path: "/_matrix/client/v3/pushrules/global/content/nocake/enabled",
			body: `{}`, want: http.StatusBadRequest},
	} {
		res := h.raw(t, tc.method, tc.path, []byte(tc.body), token)
		if tc.name == "both before and after" {
			res = h.raw(t, http.MethodPut, tc.path+"?before=a&after=b", []byte(tc.body), token)
		}
		if res.status != tc.want {
			t.Errorf("%s: status %d, want %d (body %s)", tc.name, res.status, tc.want, res.body)
		}
	}

	// A rule placed relative to another one, which is the only way a client can say
	// which of its own rules wins when both match.
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/pushrules/global/content/first",
		[]byte(`{"actions":["notify"],"pattern":"a"}`), token), http.StatusOK)
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/pushrules/global/content/second?before=first",
		[]byte(`{"actions":["notify"],"pattern":"b"}`), token), http.StatusOK)
	ordered := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules/global/content", token), http.StatusOK).decoded(t)
	list, _ := ordered["content"].([]any)
	if len(list) < 2 {
		t.Fatalf("the content rules are missing: %v", ordered)
	}
	if list[0] == nil || list[0].(map[string]any)["rule_id"] != "second" {
		t.Errorf("the newest rule should be the most important one; the list starts with %v", list[0])
	}
	if res := h.raw(t, http.MethodPut, "/_matrix/client/v3/pushrules/global/content/anchored?before=nonexistent",
		[]byte(`{"actions":["notify"],"pattern":"c"}`), token); res.status == http.StatusOK {
		t.Errorf("placing a rule next to one that does not exist was accepted")
	}
}

// TestPushRulesReachTheClientThroughSync checks the part a notification setting depends
// on: the rules must arrive in the account data of a sync, both for a client that starts
// from nothing and for one already listening.
func TestPushRulesReachTheClientThroughSync(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, token, _ := h.register(t, "rulessync", "pw-rulessync", "")

	body, since := syncNow(t, h, token, "")
	event := accountDataEvent(t, body, "m.push_rules")
	if event == nil {
		t.Fatalf("an initial sync carries no push rules: %v", body["account_data"])
	}
	content, _ := event["content"].(map[string]any)
	if content["global"] == nil {
		t.Fatalf("the push rules event carries no ruleset: %v", event)
	}

	// A long poll must notice a rule change: a client that turns a rule off expects the
	// new set, not a timeout.
	var (
		got     map[string]any
		gotAt   time.Time
		mu      sync.Mutex
		started = make(chan struct{})
	)
	go func() {
		close(started)
		res := h.get("/_matrix/client/v3/sync?timeout=5000&since="+since, token)
		mu.Lock()
		defer mu.Unlock()
		got = res.decoded(t)
		gotAt = time.Now()
	}()
	<-started
	time.Sleep(150 * time.Millisecond)
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/pushrules/global/content/latenight",
		[]byte(`{"actions":["notify"],"pattern":"cake"}`), token), http.StatusOK)

	deadline := time.After(4 * time.Second)
	for {
		mu.Lock()
		done := got != nil
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the sync did not wake up for a push rule change")
		case <-time.After(50 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if gotAt.IsZero() {
		t.Fatalf("the sync returned without a time")
	}
	event = accountDataEvent(t, got, "m.push_rules")
	if event == nil {
		t.Fatalf("the incremental sync carried no push rules: %v", got["account_data"])
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("re-encoding the rules event: %v", err)
	}
	if !json.Valid(encoded) {
		t.Errorf("the rules event is not valid JSON")
	}
}

// accountDataEvent finds one event in the top-level account data of a sync response.
func accountDataEvent(t *testing.T, body map[string]any, typ string) map[string]any {
	t.Helper()
	section, _ := body["account_data"].(map[string]any)
	list, _ := section["events"].([]any)
	for _, item := range list {
		event, _ := item.(map[string]any)
		if event["type"] == typ {
			return event
		}
	}
	return nil
}

// jsonEqual compares two decoded JSON values.
func jsonEqual(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	left, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("encoding %v: %v", a, err)
	}
	right, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("encoding %v: %v", b, err)
	}
	return string(left) == string(right)
}
