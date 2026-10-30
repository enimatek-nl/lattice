package compat

import (
	"net/http"
	"testing"
)

// TestRegister covers POST /_matrix/client/v3/register.
func TestRegister(t *testing.T) {
	server := NewServer(t)
	server.Register("alice", "wonderland")

	cases := []struct {
		name     string
		body     map[string]any
		want     int
		errCode  string
		hasToken bool
	}{
		{name: "new account", body: map[string]any{"username": "bob", "password": "rubberduck"}, want: http.StatusOK, hasToken: true},
		{name: "taken name", body: map[string]any{"username": "alice", "password": "other"}, want: http.StatusConflict, errCode: "M_USER_IN_USE"},
		{name: "invalid name", body: map[string]any{"username": "not valid!", "password": "other"}, want: http.StatusBadRequest, errCode: "M_INVALID_USERNAME"},
		{name: "missing name", body: map[string]any{"password": "other"}, want: http.StatusBadRequest, errCode: "M_INVALID_USERNAME"},
		{name: "guest", body: map[string]any{"kind": "guest"}, want: http.StatusOK, hasToken: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := server.Do(http.MethodPost, "/_matrix/client/v3/register", "", tc.body)
			if resp.Status != tc.want {
				t.Fatalf("got %d %s, want %d", resp.Status, resp.Raw, tc.want)
			}
			if tc.errCode != "" && resp.ErrCode() != tc.errCode {
				t.Fatalf("got errcode %q, want %q (%s)", resp.ErrCode(), tc.errCode, resp.Raw)
			}
			if tc.hasToken && resp.String("access_token") == "" {
				t.Fatalf("expected an access token, got %s", resp.Raw)
			}
		})
	}
}

// TestRegisterAvailable covers GET /_matrix/client/v3/register/available.
func TestRegisterAvailable(t *testing.T) {
	server := NewServer(t)
	server.Register("carol", "password1")

	cases := []struct {
		userID string
		want   bool
	}{
		{"carol", false},
		{"dave", true},
	}
	for _, tc := range cases {
		resp := server.MustDo(http.MethodGet, "/_matrix/client/v3/register/available?user_id="+tc.userID, "", nil, http.StatusOK)
		available, _ := resp.Body["available"].(bool)
		if available != tc.want {
			t.Errorf("available(%q) = %v, want %v", tc.userID, available, tc.want)
		}
	}

	resp := server.Do(http.MethodGet, "/_matrix/client/v3/register/available?user_id=%23nope", "", nil)
	if resp.Status == http.StatusOK {
		t.Fatalf("an invalid localpart should not report availability: %s", resp.Raw)
	}
}

// TestLogin covers GET and POST /_matrix/client/v3/login.
func TestLogin(t *testing.T) {
	server := NewServer(t)
	server.Register("erin", "sunflower")

	flows := server.MustDo(http.MethodGet, "/_matrix/client/v3/login", "", nil, http.StatusOK)
	list, _ := flows.Body["flows"].([]any)
	if len(list) == 0 {
		t.Fatalf("login advertises no flows: %s", flows.Raw)
	}

	cases := []struct {
		name    string
		body    map[string]any
		want    int
		errCode string
	}{
		{name: "password", body: map[string]any{"type": "m.login.password", "user": "erin", "password": "sunflower"}, want: http.StatusOK},
		{name: "full mxid", body: map[string]any{"type": "m.login.password", "identifier": map[string]any{"type": "m.id.user", "user": "erin"}, "password": "sunflower"}, want: http.StatusOK},
		{name: "wrong password", body: map[string]any{"type": "m.login.password", "user": "erin", "password": "nope"}, want: http.StatusForbidden, errCode: "M_FORBIDDEN"},
		{name: "unknown user", body: map[string]any{"type": "m.login.password", "user": "ghost", "password": "sunflower"}, want: http.StatusForbidden, errCode: "M_FORBIDDEN"},
		{name: "unknown flow", body: map[string]any{"type": "m.login.sso", "user": "erin"}, want: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := server.Do(http.MethodPost, "/_matrix/client/v3/login", "", tc.body)
			if resp.Status != tc.want {
				t.Fatalf("got %d %s, want %d", resp.Status, resp.Raw, tc.want)
			}
			if tc.errCode != "" && resp.ErrCode() != tc.errCode {
				t.Fatalf("got errcode %q, want %q", resp.ErrCode(), tc.errCode)
			}
		})
	}

	session := server.Login("erin", "sunflower")
	if session.DeviceID == "" || session.UserID != "@erin:test.server" {
		t.Fatalf("unexpected session: %+v", session)
	}
	if session.NextBatch == "" {
		t.Errorf("login should advertise a next_batch token: %s", server.SessionFor("erin").NextBatch)
	}
}

// TestWhoami covers GET /_matrix/client/v3/account/whoami.
func TestWhoami(t *testing.T) {
	server := NewServer(t)
	session := server.Register("frank", "teacup")

	resp := server.MustDo(http.MethodGet, "/_matrix/client/v3/account/whoami", session.Token, nil, http.StatusOK)
	if resp.String("user_id") != "@frank:test.server" {
		t.Errorf("user_id = %q", resp.String("user_id"))
	}
	if resp.String("device_id") != session.DeviceID {
		t.Errorf("device_id = %q, want %q", resp.String("device_id"), session.DeviceID)
	}

	missing := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", "", nil)
	if missing.Status != http.StatusUnauthorized || missing.ErrCode() != "M_MISSING_TOKEN" {
		t.Errorf("without a token: got %d %s", missing.Status, missing.Raw)
	}
	invalid := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", "not-a-token", nil)
	if invalid.Status != http.StatusUnauthorized || invalid.ErrCode() != "M_UNKNOWN_TOKEN" {
		t.Errorf("with a bogus token: got %d %s", invalid.Status, invalid.Raw)
	}
}

// TestLogout covers POST /_matrix/client/v3/logout and logout/all.
func TestLogout(t *testing.T) {
	server := NewServer(t)
	session := server.Register("grace", "kite")
	other := server.Login("grace", "kite")

	server.MustDo(http.MethodPost, "/_matrix/client/v3/logout", session.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", session.Token, nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("the logged out token still works: %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", other.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("the other device was revoked too: %d %s", resp.Status, resp.Raw)
	}

	server.MustDo(http.MethodPost, "/_matrix/client/v3/logout/all", other.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", other.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("logout/all revoked the token that authenticated the request: %d %s", resp.Status, resp.Raw)
	}
	third := server.Login("grace", "kite")
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", third.Token, nil); resp.Status != http.StatusOK {
		t.Fatalf("the new device should work: %d %s", resp.Status, resp.Raw)
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/logout/all", third.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", other.Token, nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("logout/all left another device signed in: %d %s", resp.Status, resp.Raw)
	}
}

// TestRefresh covers POST /_matrix/client/v3/refresh.
func TestRefresh(t *testing.T) {
	server := NewServer(t)
	session := server.Register("heidi", "lollipop")

	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/refresh?refresh_token="+session.Refresh, "", nil, http.StatusOK)
	fresh := resp.String("access_token")
	if fresh == "" || fresh == session.Token {
		t.Fatalf("refresh returned no new access token: %s", resp.Raw)
	}
	if got := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", fresh, nil); got.Status != http.StatusOK {
		t.Errorf("the refreshed token is not accepted: %d %s", got.Status, got.Raw)
	}
	if got := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", session.Token, nil); got.Status != http.StatusUnauthorized {
		t.Errorf("the rotated token is still valid: %d %s", got.Status, got.Raw)
	}
	if bad := server.Do(http.MethodPost, "/_matrix/client/v3/refresh?refresh_token=nope", "", nil); bad.Status == http.StatusOK {
		t.Errorf("an unknown refresh token was accepted: %s", bad.Raw)
	}
}

// TestDeviceListing covers the device endpoints.
func TestDeviceListing(t *testing.T) {
	server := NewServer(t)
	session := server.Register("ivan", "trombone")
	second := server.Login("ivan", "trombone")

	list := server.MustDo(http.MethodGet, "/_matrix/client/v3/devices", session.Token, nil, http.StatusOK)
	devices, _ := list.Body["devices"].([]any)
	if len(devices) != 2 {
		t.Fatalf("expected two devices, got %s", list.Raw)
	}
	server.MustDo(http.MethodPut, "/_matrix/client/v3/devices/"+second.DeviceID, session.Token,
		map[string]any{"display_name": "phone"}, http.StatusOK)
	one := server.MustDo(http.MethodGet, "/_matrix/client/v3/devices/"+second.DeviceID, session.Token, nil, http.StatusOK)
	if one.String("display_name") != "phone" {
		t.Errorf("display_name = %q", one.String("display_name"))
	}
	server.MustDo(http.MethodDelete, "/_matrix/client/v3/devices/"+second.DeviceID, session.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", second.Token, nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("deleting a device left its session alive: %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/devices/"+second.DeviceID, session.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("deleted device still readable: %d %s", resp.Status, resp.Raw)
	}
}
