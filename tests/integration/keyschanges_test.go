package integration

import (
	"net/http"
	"net/url"
	"testing"
)

// `GET /keys/changes` and the `device_lists` section of a sync are the same fact asked two
// ways: whose device lists did this user need to re-read, and whose stopped being theirs to
// track. They are answered from the same addressed rows, so they must agree — and the rows are
// addressed at the moment the change happened, because permission to hear about a user is room
// membership then, which cannot be worked out afterwards.

func stringList(t *testing.T, body map[string]any, field string) []string {
	t.Helper()
	raw, ok := body[field]
	if !ok {
		t.Fatalf("the response has no %s key: %v", field, body)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s is not an array: %v", field, raw)
	}
	out := []string{}
	for _, entry := range list {
		if text, ok := entry.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func TestKeysChangesAgreesWithTheSyncSection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, aliceDevice := h.register(t, "watcher", "pw-watcher", "")
	bob, bobToken, bobDevice := h.register(t, "noisy", "pw-noisy", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	join(t, h, roomID, bobToken, map[string]any{})

	// A position before anything else happens, so every range below has a known start.
	_, from := syncNow(t, h, aliceToken, "")

	keys := newTestKeys(t, bob, bobDevice, "A")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{
		"device_keys": keys.DeviceKeys, "one_time_keys": keys.OneTimeKeys,
	}, bobToken), http.StatusOK)

	synced, to := syncNow(t, h, aliceToken, "&since="+url.QueryEscape(from))
	devices, _ := synced["device_lists"].(map[string]any)
	if devices == nil {
		t.Fatalf("the sync carries no device_lists section: %v", synced)
	}
	saidChanged := stringList(t, devices, "changed")
	if !containsString(saidChanged, bob) {
		t.Fatalf("the sync did not tell alice that bob's device list moved: %v", devices)
	}

	changes := h.mustStatus(t, h.get("/_matrix/client/v3/keys/changes?from="+
		url.QueryEscape(from)+"&to="+url.QueryEscape(to), aliceToken), http.StatusOK).decoded(t)
	if got := stringList(t, changes, "changed"); !sameList(got, saidChanged) {
		t.Errorf("/keys/changes says %v and the sync says %v; they are the same fact", got, saidChanged)
	}
	if left := stringList(t, changes, "left"); len(left) != 0 {
		t.Errorf("/keys/changes reports nobody left, got %v", left)
	}

	// A departure is the other list: alice is told bob is no longer hers to track, and the
	// range answer agrees.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)
	left, leftTo := syncNow(t, h, aliceToken, "&since="+url.QueryEscape(to))
	devices, _ = left["device_lists"].(map[string]any)
	if devices == nil {
		t.Fatalf("the sync after the leave carries no device_lists section: %v", left)
	}
	saidLeft := stringList(t, devices, "left")
	if !containsString(saidLeft, bob) {
		t.Fatalf("the sync did not tell alice that bob left: %v", devices)
	}
	changes = h.mustStatus(t, h.get("/_matrix/client/v3/keys/changes?from="+
		url.QueryEscape(to)+"&to="+url.QueryEscape(leftTo), aliceToken), http.StatusOK).decoded(t)
	if got := stringList(t, changes, "left"); !sameList(got, saidLeft) {
		t.Errorf("/keys/changes says %v and the sync says %v for the same range", got, saidLeft)
	}

	// A user's own changes are not reported back to them: alice uploading her own keys is not
	// news she needs to be told about.
	ownKeys := newTestKeys(t, alice, aliceDevice, "Z")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{
		"device_keys": ownKeys.DeviceKeys, "one_time_keys": ownKeys.OneTimeKeys,
	}, aliceToken), http.StatusOK)
	_, ownTo := syncNow(t, h, aliceToken, "&since="+url.QueryEscape(leftTo))
	changes = h.mustStatus(t, h.get("/_matrix/client/v3/keys/changes?from="+
		url.QueryEscape(leftTo)+"&to="+url.QueryEscape(ownTo), aliceToken), http.StatusOK).decoded(t)
	if got := stringList(t, changes, "changed"); containsString(got, alice) {
		t.Errorf("alice is told her own device list moved: %v", got)
	}
}

// sameList compares two lists the server produced, in the order the server produced them:
// these come from the same read, so an ordering difference is a difference.
func sameList(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestKeysChangesRefusesToGuessAToken covers the two ways a client can ask this question
// badly: a bound that is missing, and a bound that this server never issued. Treating an
// unreadable token as "from the beginning" would report changes the client then believes it
// has already seen.
func TestKeysChangesRefusesToGuessAToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "precise", "pw-precise", "")
	other, otherToken, _ := h.register(t, "someoneelse", "pw-someoneelse", "")
	_, token := syncNow(t, h, aliceToken, "")

	for _, query := range []string{
		"",
		"?from=" + url.QueryEscape(token),
		"?to=" + url.QueryEscape(token),
		"?from=snotatoken&to=" + url.QueryEscape(token),
		"?from=" + url.QueryEscape(token) + "&to=42",
	} {
		res := h.get("/_matrix/client/v3/keys/changes"+query, aliceToken)
		if res.status != http.StatusBadRequest {
			t.Errorf("query %q got %d, want 400: %s", query, res.status, res.body)
		}
	}

	// The deprecated user_id parameter is checked, not honoured: the rows are addressed to
	// the caller, and naming somebody else does not make their list the caller's.
	res := h.get("/_matrix/client/v3/keys/changes?from=s1&to=s2&user_id="+
		url.QueryEscape(other), aliceToken)
	if res.status != http.StatusForbidden {
		t.Errorf("asking for another user's device list changes got %d, want 403: %s", res.status, res.body)
	}
	// Naming the caller is allowed, and answers the caller's own list.
	own := h.get("/_matrix/client/v3/keys/changes?from=s1&to=s2&user_id="+
		url.QueryEscape(other), otherToken)
	if own.status != http.StatusOK {
		t.Errorf("a caller naming themselves got %d, want 200: %s", own.status, own.body)
	}
	_ = aliceToken
}
