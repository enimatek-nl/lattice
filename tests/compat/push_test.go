package compat

import (
	"net/http"
	"net/url"
	"testing"
)

// ruleOrder returns the rule ids of one kind in the order the server lists them.
func ruleOrder(t *testing.T, body map[string]any, kind string) []string {
	t.Helper()
	rules := scopedList(body, kind)
	out := make([]string, 0, len(rules))
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("rule %v is not an object", raw)
		}
		id, _ := rule["rule_id"].(string)
		out = append(out, id)
	}
	return out
}

// indexOfRule returns the position of a rule id in a listing, or -1.
func indexOfRule(ids []string, want string) int {
	for i, id := range ids {
		if id == want {
			return i
		}
	}
	return -1
}

// TestPushRulePlacement covers the before and after query parameters of
// PUT /pushrules/{kind}/{ruleId}, which decide where a new rule lands in the
// evaluation order.
func TestPushRulePlacement(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	ruleBody := func(pattern string) map[string]any {
		return map[string]any{
			"conditions": []any{map[string]any{"kind": "event_match", "key": "content.body", "pattern": pattern}},
			"actions":    []any{"notify"},
		}
	}

	// Without a placement hint a new rule joins the end of its kind.
	base := ruleOrder(t, server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/override",
		alice.Token, nil, http.StatusOK).Body, "override")
	server.MustDo(http.MethodPut, "/_matrix/client/v3/pushrules/override/dev.lattice.last", alice.Token,
		ruleBody("last"), http.StatusOK)
	order := ruleOrder(t, server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/override",
		alice.Token, nil, http.StatusOK).Body, "override")
	if len(order) != len(base)+1 || order[len(order)-1] != "dev.lattice.last" ||
		order[len(order)-2] != base[len(base)-1] {
		t.Fatalf("a rule without a placement should land last: %v", order)
	}

	// "after" puts the rule right behind the named rule.
	anchor := url.Values{"after": {".m.rule.member_event"}}
	server.MustDo(http.MethodPut,
		"/_matrix/client/v3/pushrules/override/dev.lattice.after?"+anchor.Encode(), alice.Token,
		ruleBody("after"), http.StatusOK)
	order = ruleOrder(t, server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/override",
		alice.Token, nil, http.StatusOK).Body, "override")
	memberAt, afterAt := indexOfRule(order, ".m.rule.member_event"), indexOfRule(order, "dev.lattice.after")
	if memberAt < 0 || afterAt != memberAt+1 {
		t.Errorf("after=.m.rule.member_event produced %v", order)
	}

	// "before" puts it right in front, which is how a rule that must be
	// evaluated ahead of the defaults is created.
	anchor = url.Values{"before": {".m.rule.master"}}
	server.MustDo(http.MethodPut,
		"/_matrix/client/v3/pushrules/override/dev.lattice.first?"+anchor.Encode(), alice.Token,
		ruleBody("first"), http.StatusOK)
	order = ruleOrder(t, server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/override",
		alice.Token, nil, http.StatusOK).Body, "override")
	if len(order) < 2 || order[0] != "dev.lattice.first" || order[1] != ".m.rule.master" {
		t.Errorf("before=.m.rule.master produced %v", order)
	}

	// Replacing a rule keeps its place.
	server.MustDo(http.MethodPut, "/_matrix/client/v3/pushrules/override/dev.lattice.after", alice.Token,
		ruleBody("after again"), http.StatusOK)
	order = ruleOrder(t, server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/override",
		alice.Token, nil, http.StatusOK).Body, "override")
	if indexOfRule(order, "dev.lattice.after") != indexOfRule(order, ".m.rule.member_event")+1 {
		t.Errorf("replacing a rule moved it: %v", order)
	}

	// A default rule can be moved the same way.
	anchor = url.Values{"after": {"dev.lattice.first"}}
	server.MustDo(http.MethodPut,
		"/_matrix/client/v3/pushrules/override/.m.rule.master?"+anchor.Encode(), alice.Token,
		map[string]any{"conditions": []any{}, "actions": []any{"dont_notify"}}, http.StatusOK)
	order = ruleOrder(t, server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/override",
		alice.Token, nil, http.StatusOK).Body, "override")
	if indexOfRule(order, ".m.rule.master") != indexOfRule(order, "dev.lattice.first")+1 {
		t.Errorf("a default rule could not be moved: %v", order)
	}

	// Bad placements are refused.
	if resp := server.Do(http.MethodPut,
		"/_matrix/client/v3/pushrules/override/dev.lattice.both?before=.m.rule.master&after=.m.rule.master",
		alice.Token, ruleBody("both")); resp.Status != http.StatusBadRequest {
		t.Errorf("both before and after: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPut,
		"/_matrix/client/v3/pushrules/override/dev.lattice.gone?after=dev.lattice.unknown",
		alice.Token, ruleBody("gone")); resp.Status != http.StatusBadRequest {
		t.Errorf("an unknown anchor: got %d %s, want 400", resp.Status, resp.Raw)
	}
	// The anchor has to live in the same kind.
	server.MustDo(http.MethodPut, "/_matrix/client/v3/pushrules/content/dev.lattice.pings", alice.Token,
		map[string]any{"pattern": "ping", "actions": []any{"notify"}}, http.StatusOK)
	if resp := server.Do(http.MethodPut,
		"/_matrix/client/v3/pushrules/override/dev.lattice.cross?after=dev.lattice.pings",
		alice.Token, ruleBody("cross")); resp.Status != http.StatusBadRequest {
		t.Errorf("an anchor of another kind: got %d %s, want 400", resp.Status, resp.Raw)
	}
	for _, id := range []string{"dev.lattice.both", "dev.lattice.gone", "dev.lattice.cross"} {
		if resp := server.Do(http.MethodGet, "/_matrix/client/v3/pushrules/override/"+id, alice.Token, nil); resp.Status != http.StatusNotFound {
			t.Errorf("%s was created despite the bad placement: %d %s", id, resp.Status, resp.Raw)
		}
	}
}

// TestPusherDeletion covers DELETE /pushers/{pushkey}/{appEncodedPushkey}.
func TestPusherDeletion(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	pusher := func(pushKey, appID string) map[string]any {
		return map[string]any{
			"pushkey":          pushKey,
			"app_id":           appID,
			"app_display_name": "Lattice Test",
			"device_id":        alice.DeviceID,
			"kind":             "http",
			"lang":             "en",
			"data":             map[string]any{"url": "https://push.example/lattice"},
		}
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", alice.Token,
		pusher("push-key-one", "dev.lattice.app"), http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", alice.Token,
		pusher("push-key-two", "dev.lattice.app"), http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", bob.Token,
		pusher("push-key-one", "dev.lattice.other"), http.StatusOK)
	if got := lenPushers(server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil,
		http.StatusOK)); got != 2 {
		t.Fatalf("alice should have two pushers, has %d", got)
	}

	// A pushkey alone is not enough: the app id identifies the pusher.
	if resp := server.Do(http.MethodDelete, "/_matrix/client/v3/pushers/push-key-one", alice.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("deleting without an app id: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodDelete, "/_matrix/client/v3/pushers/push-key-one/dev.lattice.wrong",
		alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("deleting an unknown app id: got %d %s, want 404", resp.Status, resp.Raw)
	}
	// Bob may not remove the pusher of Alice, even with the same pushkey.
	if resp := server.Do(http.MethodDelete, "/_matrix/client/v3/pushers/push-key-one/dev.lattice.app",
		bob.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("bob deleted a pusher of alice: got %d %s, want 404", resp.Status, resp.Raw)
	}
	if got := lenPushers(server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil,
		http.StatusOK)); got != 2 {
		t.Errorf("a refused delete removed a pusher: %d", got)
	}

	// The real thing: the app id is url encoded in the path.
	path := "/_matrix/client/v3/pushers/push-key-one/" + url.PathEscape("dev.lattice.app")
	server.MustDo(http.MethodDelete, path, alice.Token, nil, http.StatusOK)
	if got := lenPushers(server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil,
		http.StatusOK)); got != 1 {
		t.Errorf("one pusher should be left, got %d", got)
	}
	// Deleting it a second time is not found, and Bob still has his own.
	if resp := server.Do(http.MethodDelete, path, alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("deleting twice: got %d %s, want 404", resp.Status, resp.Raw)
	}
	if got := lenPushers(server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", bob.Token, nil,
		http.StatusOK)); got != 1 {
		t.Errorf("the delete touched the pusher of bob: %d", got)
	}

	// The short form works with an app_id query parameter, and a pushkey can be
	// registered again after it was removed.
	server.MustDo(http.MethodDelete, "/_matrix/client/v3/pushers/push-key-two?app_id=dev.lattice.app",
		alice.Token, nil, http.StatusOK)
	if got := lenPushers(server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil,
		http.StatusOK)); got != 0 {
		t.Errorf("the query form removed nothing: %d", got)
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", alice.Token,
		pusher("push-key-one", "dev.lattice.app"), http.StatusOK)
	resp := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil, http.StatusOK)
	if got := lenPushers(resp); got != 1 {
		t.Fatalf("re-registering a deleted pusher: got %d pushers: %s", got, resp.Raw)
	}
	if resp := server.Do(http.MethodDelete, "/_matrix/client/v3/pushers/", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("deleting an empty pushkey: got %d, want 404", resp.Status)
	}
	if resp := server.Do(http.MethodDelete, path, "", nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated pusher delete: got %d, want 401", resp.Status)
	}
}
