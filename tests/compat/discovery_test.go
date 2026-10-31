package compat

import (
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/service"
)

// TestWellKnownClient covers GET /.well-known/matrix/client, which clients use
// to turn a server name into the URL they should call. The document nests the
// URL under base_url; a bare string makes discovery fail in real clients.
func TestWellKnownClient(t *testing.T) {
	const baseURL = "https://matrix.example.org"
	server := NewServerWith(t, api.Config{AllowGuests: true}, service.WithPublicBaseURL(baseURL))

	resp := server.MustDo(http.MethodGet, "/.well-known/matrix/client", "", nil, http.StatusOK)
	if home := homeServerOf(resp.Body); home["base_url"] != baseURL {
		t.Errorf("well-known base_url = %v, want %s (%s)", home["base_url"], baseURL, resp.Raw)
	}

	// The same document comes back with the session, so a client that never
	// looked at the well-known endpoint still learns where the server lives.
	created := server.Do(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{
		"username": "dottie",
		"password": "otterburn",
		"auth":     map[string]any{"type": "m.login.dummy"},
	})
	if created.Status != http.StatusOK {
		t.Fatalf("register: got %d %s, want 200", created.Status, created.Raw)
	}
	wellKnown, _ := created.Body["well_known"].(map[string]any)
	if home := homeServerOf(wellKnown); home["base_url"] != baseURL {
		t.Errorf("register well_known base_url = %v, want %s (%s)", home["base_url"], baseURL, created.Raw)
	}
}

// homeServerOf returns the m.homeserver object of a discovery document, or nil.
func homeServerOf(body map[string]any) map[string]any {
	home, _ := body["m.homeserver"].(map[string]any)
	return home
}

// TestUnknownEndpoint checks that an endpoint Lattice does not implement answers
// with a Matrix error body. The plain "404 page not found" of net/http ends up
// in a client as an opaque "Server returned 404 error", which hides which
// endpoint is missing.
func TestUnknownEndpoint(t *testing.T) {
	server := NewServer(t)

	cases := []string{
		"/_matrix/client/v3/room_keys/version",
		"/_matrix/client/v3/keys/device_signing/upload",
		"/_matrix/client/v3/login/sso/redirect",
		"/nothing-at-all",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			resp := server.Do(http.MethodGet, path, "", nil)
			if resp.Status != http.StatusNotFound {
				t.Fatalf("got %d %s, want 404", resp.Status, resp.Raw)
			}
			if resp.ErrCode() != "M_UNRECOGNIZED" {
				t.Fatalf("errcode = %q, want M_UNRECOGNIZED (%s)", resp.ErrCode(), resp.Raw)
			}
		})
	}

	// Registered paths keep their own routing: a path that exists under another
	// method is still a 405 that names the accepted methods, not a catch-all 404.
	resp := server.Do(http.MethodGet, "/_matrix/client/v3/logout", "", nil)
	if resp.Status != http.StatusMethodNotAllowed {
		t.Errorf("GET /logout: got %d %s, want 405", resp.Status, resp.Raw)
	}
	if allow := resp.Headers.Get("Allow"); allow != http.MethodPost {
		t.Errorf("the Allow header = %q, want POST (%s)", allow, resp.Raw)
	}
}

// TestMethodMismatch covers the route-table matching that decides between a 405
// and a catch-all 404, including patterns with templated segments.
func TestMethodMismatch(t *testing.T) {
	server := NewServer(t)

	cases := []struct {
		method string
		path   string
		want   int
		allow  string
	}{
		{method: http.MethodGet, path: "/_matrix/client/v3/logout", want: http.StatusMethodNotAllowed, allow: "POST"},
		{method: http.MethodPost, path: "/_matrix/client/v3/rooms/!room:test.server/send/m.room.message/txn1",
			want: http.StatusMethodNotAllowed, allow: "PUT"},
		{method: http.MethodDelete, path: "/_matrix/client/v3/join/!room:test.server",
			want: http.StatusMethodNotAllowed, allow: "POST"},
		// A trailing slash is an empty template value, not a match: the pattern
		// /pushers/{pushkey} needs a pushkey, so this path is simply unknown.
		{method: http.MethodDelete, path: "/_matrix/client/v3/pushers/", want: http.StatusNotFound},
		{method: http.MethodPut, path: "/_matrix/client/v3/rooms/!room:test.server/state/m.room.name/",
			want: http.StatusUnauthorized},
		{method: http.MethodPost, path: "/_matrix/client/v3/rooms", want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp := server.Do(tc.method, tc.path, "", map[string]any{})
			if resp.Status != tc.want {
				t.Fatalf("got %d %s, want %d", resp.Status, resp.Raw, tc.want)
			}
			if tc.allow != "" {
				if got := resp.Headers.Get("Allow"); got != tc.allow {
					t.Errorf("the Allow header = %q, want %q", got, tc.allow)
				}
			}
			if (tc.want == http.StatusNotFound || tc.want == http.StatusMethodNotAllowed) &&
				resp.ErrCode() != "M_UNRECOGNIZED" {
				t.Errorf("errcode = %q, want M_UNRECOGNIZED (%s)", resp.ErrCode(), resp.Raw)
			}
		})
	}
}

// TestVersions covers GET /_matrix/client/versions, the liveliness check every
// client makes before it will talk to a homeserver.
func TestVersions(t *testing.T) {
	server := NewServer(t)

	resp := server.MustDo(http.MethodGet, "/_matrix/client/versions", "", nil, http.StatusOK)
	versions, ok := resp.Body["versions"].([]any)
	if !ok || len(versions) == 0 {
		t.Fatalf("versions is empty: %s", resp.Raw)
	}
	if _, ok := resp.Body["unstable_features"].(map[string]any); !ok {
		t.Errorf("unstable_features is missing: %s", resp.Raw)
	}
}
