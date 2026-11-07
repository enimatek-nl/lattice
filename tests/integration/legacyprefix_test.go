package integration

import (
	"net/http"
	"testing"
)

// `r0` is the specification's own history, not a dialect: the endpoints under it are the same
// endpoints under `v3`. A homeserver that registers only the current prefix answers a client
// that is following older published text with `M_UNRECOGNIZED`, which reads as a missing
// feature rather than a path the server chose not to keep. The prefix is registered for the
// whole client API — including the endpoints that carry no token, because login and
// registration happen before a token exists.

func TestLegacyR0PrefixAnswersAsV3Does(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID, token, _ := h.register(t, "legacy", "pw-legacy", "")

	flows := h.mustStatus(t, h.get("/_matrix/client/r0/login", ""), http.StatusOK).decoded(t)
	if _, ok := flows["flows"].([]any); !ok {
		t.Fatalf("GET /r0/login returned no flows: %v", flows)
	}

	// A login under r0 is a login: it issues a working token for the same account.
	logged := h.mustStatus(t, h.post(t, "/_matrix/client/r0/login", map[string]any{
		"identifier": map[string]any{"type": "m.id.user", "user": userID},
		"type":       "m.login.password",
		"password":   "pw-legacy",
	}, ""), http.StatusOK).decoded(t)
	legacyToken, _ := logged["access_token"].(string)
	if legacyToken == "" {
		t.Fatalf("the r0 login returned no access token: %v", logged)
	}

	for _, check := range []struct {
		path  string
		token string
	}{
		{"/_matrix/client/r0/account/whoami", legacyToken},
		{"/_matrix/client/r0/sync?timeout=0", token},
		{"/_matrix/client/r0/devices", token},
		{"/_matrix/client/r0/pushrules", token},
		{"/_matrix/client/r0/keys/changes?from=s1&to=s2", token},
		// The room directory asks for a token in the specification, so this is not a gap.
		{"/_matrix/client/r0/publicRooms", token},
		{"/_matrix/client/r0/profile/" + userID, ""},
	} {
		res := h.get(check.path, check.token)
		if res.status != http.StatusOK {
			t.Errorf("GET %s got %d, want 200: %s", check.path, res.status, res.body)
		}
	}
	if who := h.mustStatus(t, h.get("/_matrix/client/r0/account/whoami", legacyToken), http.StatusOK).decoded(t); who["user_id"] != userID {
		t.Errorf("the r0 login and the r0 whoami disagree about who this is: %v vs %s", who["user_id"], userID)
	}

	// The prefix is a real prefix, not a catch-all: an unknown endpoint under it is still
	// unknown, and a known path with the wrong method still says which methods it takes.
	res := h.get("/_matrix/client/r0/notathing", token)
	if res.status != http.StatusNotFound || res.errcode(t) != "M_UNRECOGNIZED" {
		t.Errorf("an unknown endpoint under r0 got %d %s, want 404 M_UNRECOGNIZED", res.status, res.body)
	}
	if res := h.raw(t, http.MethodPut, "/_matrix/client/r0/login", []byte(`{}`), ""); res.status != http.StatusMethodNotAllowed {
		t.Errorf("PUT /r0/login got %d, want 405: %s", res.status, res.body)
	}
}
