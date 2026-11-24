package compat

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
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

// TestPushRuleNotFoundSaysSo pins what the push rule endpoints answer when the
// kind or the rule is not there.
//
// The difference is not cosmetic: 404 M_NOT_FOUND reads as "this rule does not
// exist", 400 M_INVALID_PARAM reads as "what you sent was malformed", and
// Complement's MSC4306 probe acts on exactly that — it asks for its thread rules
// under the kind `postcontent`, continues when the answer is 404, and treats
// anything else as a server error (client.GetPushRule followed by
// MustDisablePushRule). This server answered 400, so two thread-receipt tests
// died on a probe about a feature they explicitly opt out of.
//
// The one place where a 400 stays right is *creating* a rule inside a kind that
// does not exist: nothing is missing there, the request is.
func TestPushRuleNotFoundSaysSo(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	const absent = "dev.lattice.absent"

	// An unknown kind reads as not found — including the MSC4306 shape, with and
	// without the deprecated scope segment.
	for _, path := range []string{
		"/_matrix/client/v3/pushrules/postcontent",
		"/_matrix/client/v3/pushrules/global/postcontent",
		"/_matrix/client/v3/pushrules/global/postcontent/.io.element.msc4306.rule.subscribed_thread",
		"/_matrix/client/v3/pushrules/override/" + absent,
	} {
		if resp := server.Do(http.MethodGet, path, alice.Token, nil); resp.Status != http.StatusNotFound ||
			resp.ErrCode() != string(matrix.ErrCodeNotFound) {
			t.Errorf("GET %s: got %d %s, want 404 M_NOT_FOUND", path, resp.Status, resp.Raw)
		}
	}

	// The three mutations of a rule say the same when that rule is not there —
	// they are about it, so "not found" is the honest answer. An unknown kind
	// takes the same exit: a rule of a kind that does not exist cannot be there.
	type mutation struct {
		method, path, what string
		body               map[string]any
	}
	for _, move := range []mutation{
		{http.MethodPut, "/_matrix/client/v3/pushrules/override/" + absent + "/enabled", "enabling", map[string]any{"enabled": true}},
		{http.MethodPut, "/_matrix/client/v3/pushrules/override/" + absent + "/actions", "replacing the actions of", map[string]any{"actions": []any{"notify"}}},
		{http.MethodDelete, "/_matrix/client/v3/pushrules/override/" + absent, "deleting", nil},
		{http.MethodPut, "/_matrix/client/v3/pushrules/global/postcontent/" + absent + "/enabled", "enabling under an unknown kind", map[string]any{"enabled": true}},
	} {
		if resp := server.Do(move.method, move.path, alice.Token, move.body); resp.Status != http.StatusNotFound ||
			resp.ErrCode() != string(matrix.ErrCodeNotFound) {
			t.Errorf("%s an absent rule: got %d %s, want 404 M_NOT_FOUND", move.what, resp.Status, resp.Raw)
		}
	}

	// Creating a rule inside a kind that does not exist is a bad request, not a
	// missing resource — the two answers stay apart.
	if resp := server.Do(http.MethodPut, "/_matrix/client/v3/pushrules/postcontent/"+absent, alice.Token,
		map[string]any{"conditions": []any{}, "actions": []any{"notify"}}); resp.Status != http.StatusBadRequest ||
		resp.ErrCode() != string(matrix.ErrCodeInvalidParam) {
		t.Errorf("creating in an unknown kind: got %d %s, want 400 M_INVALID_PARAM", resp.Status, resp.Raw)
	}

	// The control: those very same three mutations on a rule that exists are
	// still 200, so the answers above are about absence and not a broken path.
	present := "/_matrix/client/v3/pushrules/override/dev.lattice.present"
	server.MustDo(http.MethodPut, present, alice.Token, map[string]any{
		"conditions": []any{map[string]any{"kind": "event_match", "key": "content.body", "pattern": "here"}},
		"actions":    []any{"notify"},
	}, http.StatusOK)
	server.MustDo(http.MethodPut, present+"/enabled", alice.Token, map[string]any{"enabled": false}, http.StatusOK)
	server.MustDo(http.MethodPut, present+"/actions", alice.Token, map[string]any{"actions": []any{"dont_notify"}}, http.StatusOK)
	if resp := server.Do(http.MethodDelete, present, alice.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("deleting a rule that exists: got %d %s, want 200", resp.Status, resp.Raw)
	}
	// And a second time it is absence again.
	if resp := server.Do(http.MethodDelete, present, alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("deleting the same rule twice: got %d %s, want 404", resp.Status, resp.Raw)
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

// TestPushRulesTravelAsAccountData reads MSC4010, the MSC that closed the hole around this
// document: a change made through /pushrules "is sent to all clients via a m.push_rules event in
// the account_data section in the next /sync", and the type is the server's to write. MSC4548
// then asks the sliding sync extension for the same row in so many words.
//
// Lattice kept the rules in their own table and the /sync answer in a `pushrules` field, so a
// second device that changed a rule was never told, and a client reading its rules where the MSC
// says they are read nothing at all.
func TestPushRulesTravelAsAccountData(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	rule := map[string]any{
		"conditions": []any{map[string]any{"kind": "event_match", "key": "content.body", "pattern": "travel"}},
		"actions":    []any{"notify"},
	}

	// A fresh account has the document already: a client must not have to wait for a change
	// before it can read the rules it is being pushed for.
	first := server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", alice.Token, nil, http.StatusOK)
	section, _ := first.Body["account_data"].(map[string]any)
	if section == nil {
		t.Fatalf("the first sync of a fresh account carries no account_data section: %s", first.Raw)
	}
	events, _ := section["events"].([]any)
	if !accountsHold(events, "m.push_rules") {
		t.Errorf("the first sync carries no m.push_rules account data: %s", first.Raw)
	}
	if !matrixHasDefaultRules(first.Raw) {
		t.Errorf("the m.push_rules account data is not the rule set a client would read from /pushrules: %s", first.Raw)
	}

	// A change through the rules API arrives as that account data on the next sync.
	server.MustDo(http.MethodPut, "/_matrix/client/v3/pushrules/global/override/dev.lattice.travel",
		alice.Token, rule, http.StatusOK)
	next := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/sync?since="+url.QueryEscape(first.String("next_batch"))+"&timeout=0",
		alice.Token, nil, http.StatusOK)
	section, _ = next.Body["account_data"].(map[string]any)
	if section == nil || !accountsHold(section["events"], "m.push_rules") {
		t.Fatalf("a rule change did not arrive as m.push_rules account data: %s", next.Raw)
	}
	if count := countAccounts(section["events"], "m.push_rules"); count != 1 {
		t.Errorf("the changed rule set arrives %d times, one entry per type is what both syncs ask for", count)
	}
	if !strings.Contains(string(next.Raw), "dev.lattice.travel") {
		t.Errorf("the account data copy does not carry the rule that was just created: %s", next.Raw)
	}

	// The sliding sync extension answers from the same row (MSC4548).
	sliding := server.slidingSync(t, alice, map[string]any{
		"conn_id":    "userdata",
		"extensions": map[string]any{"account_data": map[string]any{"enabled": true}},
	})
	extensions, _ := sliding["extensions"].(map[string]any)
	accountData, _ := extensions["account_data"].(map[string]any)
	if !accountsHold(accountData["global"], "m.push_rules") {
		t.Errorf("the account_data extension carries no m.push_rules: %v", accountData)
	}

	// The two server-owned types cannot be written by a client, global or room scoped: a stored
	// m.push_rules shadowed the real rules in every sync until MSC4010 closed that. Reading them
	// stays allowed.
	for _, path := range []string{
		"/_matrix/client/v3/user/" + url.PathEscape(alice.UserID) + "/account_data/m.push_rules",
		"/_matrix/client/v3/user/" + url.PathEscape(alice.UserID) + "/account_data/m.fully_read",
	} {
		guarded := server.Do(http.MethodPut, path, alice.Token, map[string]any{"event_id": "$one"})
		if guarded.Status != http.StatusMethodNotAllowed {
			t.Errorf("PUT %s: got %d (%s), want 405", path, guarded.Status, guarded.Raw)
		}
		if code, _ := guarded.Body["errcode"].(string); code != "M_BAD_JSON" {
			t.Errorf("PUT %s: errcode %v, want M_BAD_JSON as the spec pairs it with the 405", path, guarded.Body["errcode"])
		}
	}
	roomPath := "/_matrix/client/v3/user/" + url.PathEscape(alice.UserID) + "/rooms/!room:server/account_data/m.fully_read"
	if resp := server.Do(http.MethodPut, roomPath, alice.Token, map[string]any{"event_id": "$one"}); resp.Status != http.StatusMethodNotAllowed {
		t.Errorf("PUT %s: got %d (%s), want 405", roomPath, resp.Status, resp.Raw)
	}
	read := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/user/"+url.PathEscape(alice.UserID)+"/account_data/m.push_rules",
		alice.Token, nil, http.StatusOK)
	if rules, ok := read.Body["global"].(map[string]any); !ok || rules["override"] == nil {
		t.Errorf("GET m.push_rules should answer the rule set, got %s", read.Raw)
	}

	// The read marker still writes its own account data, through the service and not the API.
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	eventID := server.Send(alice, roomID, "one", "a message")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+url.PathEscape(eventID),
		alice.Token, map[string]any{}, http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", alice.Token,
		map[string]any{"fully_read": eventID}, http.StatusOK)
	if body := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/user/"+url.PathEscape(alice.UserID)+"/rooms/"+url.PathEscape(roomID)+"/account_data/m.fully_read",
		alice.Token, nil, http.StatusOK); body.String("event_id") != eventID {
		t.Errorf("the read marker no longer reaches m.fully_read: %s", body.Raw)
	}
}

// matrixHasDefaultRules reports whether a /sync body carries a m.push_rules entry that holds the
// kind lists of the rule set, rather than an empty object standing in for it.
func matrixHasDefaultRules(raw []byte) bool {
	var probe struct {
		AccountData struct {
			Events []struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			} `json:"events"`
		} `json:"account_data"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	for _, entry := range probe.AccountData.Events {
		if entry.Type != "m.push_rules" {
			continue
		}
		var content struct {
			Global map[string]json.RawMessage `json:"global"`
		}
		if err := json.Unmarshal(entry.Content, &content); err != nil {
			return false
		}
		for _, kind := range []string{"override", "content", "room", "underride"} {
			if content.Global[kind] == nil {
				return false
			}
		}
		return true
	}
	return false
}

// countAccounts counts how often one type appears in an account data list. MSC4548 forbids more
// than one entry per type in the sliding sync list, and a duplicate would leave a client guessing
// which one won.
func countAccounts(events any, typ string) int {
	list, _ := events.([]any)
	count := 0
	for _, raw := range list {
		if entry, _ := raw.(map[string]any); entry["type"] == typ {
			count++
		}
	}
	return count
}

// TestPusherCarriesEveryRequiredField is a pusher the Matrix Rust SDK can read. The Pusher object
// names seven Required fields, and a strict reader does not treat a missing one as unset: it
// refuses the whole list with "missing field `device_display_name`", which takes a client's push
// settings down with it. Lattice never stored that field, so no answer could carry it.
func TestPusherCarriesEveryRequiredField(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", alice.Token, map[string]any{
		"pushkey":             "probe-key",
		"kind":                "http",
		"app_id":              "net.oele.probe",
		"app_display_name":    "Probe",
		"device_display_name": "Probe device",
		"lang":                "en",
		"data":                map[string]any{"url": "https://push.example.org/_matrix/push/v1/notify"},
	}, http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", alice.Token, map[string]any{
		"pushkey":          "unnamed-key",
		"kind":             "http",
		"app_id":           "net.oele.probe",
		"app_display_name": "Probe",
		"profile_tag":      "work",
		"data":             map[string]any{"url": "https://push.example.org/_matrix/push/v1/notify"},
	}, http.StatusOK)

	body := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil, http.StatusOK)
	list, _ := body.Body["pushers"].([]any)
	if len(list) != 2 {
		t.Fatalf("two pushers were set, the list carries %d: %s", len(list), body.Raw)
	}
	byKey := map[string]map[string]any{}
	for _, entry := range list {
		pusher, _ := entry.(map[string]any)
		key, _ := pusher["pushkey"].(string)
		byKey[key] = pusher
	}
	// Every one of them has to be readable on its own, so the check runs over both.
	for pushKey, pusher := range byKey {
		for _, field := range []string{"pushkey", "kind", "app_id", "app_display_name",
			"device_display_name", "lang", "data"} {
			if _, ok := pusher[field]; !ok {
				t.Errorf("the pusher %q is answered without the required field %q: %v", pushKey, field, pusher)
			}
		}
	}
	named := byKey["probe-key"]
	if named["device_display_name"] != "Probe device" {
		t.Errorf("device_display_name reads %v, want the name the client registered", named["device_display_name"])
	}
	if named["app_display_name"] != "Probe" || named["lang"] != "en" {
		t.Errorf("the pusher lost what the client named it by: %v", named)
	}
	if _, ok := named["profile_tag"]; ok {
		t.Errorf("profile_tag is optional and was not sent, yet the answer says %v", named["profile_tag"])
	}
	// A client that leaves the device name out still gets a required field, not a missing one: the
	// device says what it is called.
	unnamed := byKey["unnamed-key"]
	if unnamed["device_display_name"] != alice.DeviceID {
		t.Errorf("a pusher without a device name reads %v, want the device it belongs to (%s)",
			unnamed["device_display_name"], alice.DeviceID)
	}
	if unnamed["profile_tag"] != "work" {
		t.Errorf("profile_tag did not survive the round trip: %v", unnamed["profile_tag"])
	}
	if _, ok := unnamed["device_id"]; !ok {
		t.Errorf("the pusher of a named device answers without device_id: %v", unnamed)
	}
}
