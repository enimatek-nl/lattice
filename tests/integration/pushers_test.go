package integration

import (
	"net/http"
	"testing"
)

// pusherBody is one device asking to be woken at a URL.
func pusherBody(pushkey, appID string) map[string]any {
	return map[string]any{
		"pushkey":             pushkey,
		"kind":                "http",
		"app_id":              appID,
		"app_display_name":    "an app",
		"device_display_name": "a device",
		"lang":                "en",
		"data":                map[string]any{"url": "https://push.example.invalid/_matrix/push/v1/notify"},
	}
}

// pushersOf reads what GET /pushers answers for a session.
func pushersOf(t *testing.T, h *harness, token string) []map[string]any {
	t.Helper()
	body := h.mustStatus(t, h.get("/_matrix/client/v3/pushers", token), http.StatusOK).decoded(t)
	list, _ := body["pushers"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, entry := range list {
		if pusher, ok := entry.(map[string]any); ok {
			out = append(out, pusher)
		}
	}
	return out
}

// TestAPusherIsTheAddressOfTheSessionThatWroteIt asks the question a pusher exists for, and the
// question a password change asks afterwards. A pusher is not a preference in the way account
// data is: it is one device saying "wake me here", written by one session. So it is readable by
// the user's other sessions, it is replaced under the (pushkey, app_id) the client names it by,
// and when the session that wrote it stops working the address stops being kept — while the
// session that just proved the new password keeps its own.
func TestAPusherIsTheAddressOfTheSessionThatWroteIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, token, _ := h.register(t, "pusheruser", "pw-one", "")
	second := loginWith(t, h, alice, "pw-one")

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/pushers/set", pusherBody("push-key-one", "app.one"), second.token),
		http.StatusOK)

	// Both sessions of the user see the pusher, because it belongs to the user's account and
	// not to the one session that happened to write it.
	for _, token := range []string{token, second.token} {
		list := pushersOf(t, h, token)
		if len(list) != 1 {
			t.Fatalf("a session of the user sees %d pushers, want 1: %v", len(list), list)
		}
		if got, _ := list[0]["pushkey"].(string); got != "push-key-one" {
			t.Errorf("the pusher lists pushkey %q, want push-key-one", got)
		}
		data, _ := list[0]["data"].(map[string]any)
		if url, _ := data["url"].(string); url == "" {
			t.Errorf("the pusher lost the data it was written with: %v", list[0])
		}
		if got, _ := list[0]["device_display_name"].(string); got != "a device" {
			t.Errorf("the pusher lists device_display_name %q, want a device", got)
		}
	}

	// Setting the same (pushkey, app_id) again replaces it rather than adding a second path: a
	// pusher is named by that pair, and a second entry would be a second notification path the
	// client never asked for.
	secondPusher := pusherBody("push-key-one", "app.one")
	secondPusher["device_display_name"] = "the same device, again"
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/pushers/set", secondPusher, token), http.StatusOK)
	if list := pushersOf(t, h, token); len(list) != 1 {
		t.Errorf("setting the same pusher again left %d entries, want 1: %v", len(list), list)
	} else if got, _ := list[0]["device_display_name"].(string); got != "the same device, again" {
		t.Errorf("the replaced pusher says %q, want the newest device display name", got)
	}

	// A pusher that has just been written by another session belongs to that session now. This
	// is not an accident of the store: the address is whoever last said "wake me here", and a
	// pusher left owned by a session that has since been replaced would outlive the device that
	// asked for it. So the pusher rewritten above survives the password change below, while the
	// one written by the second session and never touched again goes with that session.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/pushers/set", pusherBody("push-key-two", "app.two"), second.token),
		http.StatusOK)
	if list := pushersOf(t, h, token); len(list) != 2 {
		t.Fatalf("the user should now have two pushers, sees %v", list)
	}

	// A password change by the first session ends the other session, and the address that
	// session wrote goes with it.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/account/password",
		passwordChangeBody(alice, "pw-one", "pw-two", nil), token), http.StatusOK)
	list := pushersOf(t, h, token)
	if len(list) != 1 {
		t.Fatalf("after a password change the user has %d pushers, want only the one this session wrote: %v",
			len(list), list)
	}
	if got, _ := list[0]["pushkey"].(string); got != "push-key-one" {
		t.Errorf("the password change kept %q and dropped the session's own pusher %q",
			got, "push-key-two")
	}

	// The session that changed the password keeps what it wrote, because it is still here.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/pushers/set", pusherBody("push-key-three", "app.three"), token),
		http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/account/password",
		passwordChangeBody(alice, "pw-two", "pw-three", nil), token), http.StatusOK)
	if list := pushersOf(t, h, token); len(list) != 2 {
		t.Errorf("the session that proved the new password lost its own pushers: %v", list)
	}

	// Logging a session out takes its address with it.
	third := loginWith(t, h, alice, "pw-three")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/pushers/set", pusherBody("push-key-four", "app.four"), third.token),
		http.StatusOK)
	if list := pushersOf(t, h, token); len(list) != 3 {
		t.Fatalf("the user should now have three pushers, sees %v", list)
	}
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/logout", map[string]any{}, third.token), http.StatusOK)
	if list := pushersOf(t, h, token); len(list) != 2 {
		t.Errorf("logging out a session left %d pushers; its own should be gone: %v", len(list), list)
	}

	// `kind: null` is how a client says "stop waking me here".
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/pushers/set", map[string]any{
		"pushkey": "push-key-three",
		"app_id":  "app.three",
		"kind":    nil,
	}, token), http.StatusOK)
	if list := pushersOf(t, h, token); len(list) != 1 {
		t.Errorf("a pusher set with kind:null left %d entries, want the one that was not named: %v",
			len(list), list)
	}
}

// TestAPusherRequestIsRefusedInWordsTheClientCanActOn covers the refusals. Each is a mistake a
// client can actually make, and an empty list is not an answer to any of them.
func TestAPusherRequestIsRefusedInWordsTheClientCanActOn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "pusherrefusals", "pw-refusals", "")

	for _, tc := range []struct {
		what string
		body map[string]any
		code string
	}{
		{"an email pusher", map[string]any{
			"pushkey": "someone@example.invalid", "kind": "email", "app_id": "app.email",
			"data": map[string]any{"email": "someone@example.invalid"},
		}, "M_INVALID_EMAIL_PARAM"},
		{"no push key", map[string]any{
			"kind": "http", "app_id": "app.one", "device_display_name": "d",
			"data": map[string]any{"url": "https://push.example.invalid/x"},
		}, "M_INVALID_PARAM"},
		{"no app ID", map[string]any{
			"pushkey": "k", "kind": "http", "device_display_name": "d",
			"data": map[string]any{"url": "https://push.example.invalid/x"},
		}, "M_INVALID_PARAM"},
		{"no device display name", map[string]any{
			"pushkey": "k", "kind": "http", "app_id": "app.one",
			"data": map[string]any{"url": "https://push.example.invalid/x"},
		}, "M_INVALID_PARAM"},
		{"a kind this server does not deliver", map[string]any{
			"pushkey": "k", "kind": "carrier-pigeon", "app_id": "app.one", "device_display_name": "d",
			"data": map[string]any{"url": "https://push.example.invalid/x"},
		}, "M_INVALID_PARAM"},
		{"an http pusher with no url", map[string]any{
			"pushkey": "k", "kind": "http", "app_id": "app.one", "device_display_name": "d",
			"data": map[string]any{},
		}, "M_INVALID_PARAM"},
		// The scheme is checked because a notification carries the content of the event that
		// triggered it; plain http would put that on the open network.
		{"a url that is not https", map[string]any{
			"pushkey": "k", "kind": "http", "app_id": "app.one", "device_display_name": "d",
			"data": map[string]any{"url": "http://push.example.invalid/x"},
		}, "M_INVALID_PARAM"},
		{"no kind at all", map[string]any{
			"pushkey": "k", "app_id": "app.one", "device_display_name": "d",
			"data": map[string]any{"url": "https://push.example.invalid/x"},
		}, "M_INVALID_PARAM"},
	} {
		res := h.post(t, "/_matrix/client/v3/pushers/set", tc.body, token)
		if res.status != http.StatusBadRequest {
			t.Errorf("%s: answered %d (%s), want 400", tc.what, res.status, res.body)
			continue
		}
		if code := res.errcode(t); code != tc.code {
			t.Errorf("%s: answered %s, want %s", tc.what, code, tc.code)
		}
	}

	// And nothing was stored by the requests that were refused.
	if list := pushersOf(t, h, token); len(list) != 0 {
		t.Errorf("refused pusher requests stored %v", list)
	}
}
