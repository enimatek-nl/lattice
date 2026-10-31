package compat

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// TestRegister covers POST /_matrix/client/v3/register.
func TestRegister(t *testing.T) {
	server := NewServer(t)
	server.Register("alice", "wonderland")

	dummy := map[string]any{"type": "m.login.dummy"}
	cases := []struct {
		name     string
		body     map[string]any
		want     int
		errCode  string
		hasToken bool
	}{
		{name: "new account", body: map[string]any{"username": "bob", "password": "rubberduck", "auth": dummy}, want: http.StatusOK, hasToken: true},
		{name: "taken name", body: map[string]any{"username": "alice", "password": "other", "auth": dummy}, want: http.StatusConflict, errCode: "M_USER_IN_USE"},
		{name: "invalid name", body: map[string]any{"username": "not valid!", "password": "other", "auth": dummy}, want: http.StatusBadRequest, errCode: "M_INVALID_USERNAME"},
		{name: "missing name", body: map[string]any{"password": "other", "auth": dummy}, want: http.StatusBadRequest, errCode: "M_INVALID_USERNAME"},
		{name: "guest", body: map[string]any{"kind": "guest"}, want: http.StatusOK, hasToken: true},
		// A request without an auth dict is the discovery request clients make
		// before they have anything to send, so it must be a challenge rather
		// than a complaint about the missing username.
		{name: "no auth dict", body: map[string]any{"password": "other"}, want: http.StatusUnauthorized, errCode: "M_UNAUTHORIZED"},
		{name: "empty body", body: map[string]any{}, want: http.StatusUnauthorized, errCode: "M_UNAUTHORIZED"},
		{name: "unknown stage", body: map[string]any{"username": "frank", "auth": map[string]any{"type": "m.login.email.identity"}}, want: http.StatusUnauthorized, errCode: "M_UNAUTHORIZED"},
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

// TestRegisterFlows covers the User-Interactive Auth discovery a client such as
// Element performs: an empty POST (or a GET) must answer 401 with the flows and
// a session id, and completing the advertised dummy stage with that session has
// to create the account.
func TestRegisterFlows(t *testing.T) {
	server := NewServer(t)

	challenge := func(method, path string, body any) Response {
		t.Helper()
		resp := server.Do(method, path, "", body)
		if resp.Status != http.StatusUnauthorized {
			t.Fatalf("%s %s: got %d %s, want 401", method, path, resp.Status, resp.Raw)
		}
		if resp.ErrCode() != "M_UNAUTHORIZED" {
			t.Errorf("errcode = %q, want M_UNAUTHORIZED (%s)", resp.ErrCode(), resp.Raw)
		}
		if resp.String("session") == "" {
			t.Errorf("the challenge carries no session: %s", resp.Raw)
		}
		stages := flowStages(t, resp.Body)
		if len(stages) != 1 || stages[0][0] != "m.login.dummy" {
			t.Errorf("flows = %v, want a single m.login.dummy stage", stages)
		}
		if completed, ok := resp.Body["completed"].([]any); !ok || len(completed) != 0 {
			t.Errorf("completed = %v, want an empty list", resp.Body["completed"])
		}
		if _, ok := resp.Body["params"]; !ok {
			t.Errorf("the challenge omits params: %s", resp.Raw)
		}
		return resp
	}

	empty := map[string]any{}
	post := challenge(http.MethodPost, "/_matrix/client/v3/register", empty)
	get := challenge(http.MethodGet, "/_matrix/client/v3/register", nil)

	// Continuing the exchange with the session from the challenge registers the
	// account and names the new device.
	created := server.MustDo(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{
		"username":                    "grace",
		"password":                    "hopper",
		"initial_device_display_name": "Element web",
		"auth":                        map[string]any{"type": "m.login.dummy", "session": post.String("session")},
	}, http.StatusOK)
	if created.String("access_token") == "" {
		t.Fatalf("completing the dummy stage returned no token: %s", created.Raw)
	}

	// A session that is unknown to the server is simply replaced; the client
	// still gets a usable exchange rather than a dead end.
	resp := server.Do(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{
		"username": "heidi",
		"password": "lovelace",
		"auth":     map[string]any{"type": "m.login.dummy", "session": "not-a-session"},
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("registering with an unknown session: got %d %s, want 200", resp.Status, resp.Raw)
	}

	// The device name from the request survives into the device list.
	list := server.MustDo(http.MethodGet, "/_matrix/client/v3/devices", created.String("access_token"), nil, http.StatusOK)
	if !deviceNamed(list, created.String("device_id"), "Element web") {
		t.Errorf("initial_device_display_name was not stored: %s", list.Raw)
	}

	// GET with the session of a live exchange reuses it, so a client that polls
	// the endpoint does not lose track of where it was.
	reused := server.MustDo(http.MethodGet, "/_matrix/client/v3/register?session="+get.String("session"), "", nil, http.StatusUnauthorized)
	if reused.String("session") != get.String("session") {
		t.Errorf("a repeated challenge changed the session: %s", reused.Raw)
	}
}

// TestRegisterConcurrentSessions hammers one auth session from several
// goroutines at once, which is what a client that retries does. Sharing a
// session id must stay safe and every exchange has to finish.
func TestRegisterConcurrentSessions(t *testing.T) {
	server := NewServer(t)
	challenge := server.MustDo(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{}, http.StatusUnauthorized)
	session := challenge.String("session")

	const workers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		created  int
		onchange int
	)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				// Re-reading the flows must not disturb the other exchanges.
				resp := server.Do(http.MethodGet, "/_matrix/client/v3/register?session="+session, "", nil)
				if resp.Status == http.StatusUnauthorized {
					mu.Lock()
					onchange++
					mu.Unlock()
				}
				return
			}
			resp := server.Do(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{
				"username": fmt.Sprintf("racer%d", i),
				"password": "wonderland",
				"auth":     map[string]any{"type": "m.login.dummy", "session": session},
			})
			if resp.Status == http.StatusOK && resp.String("access_token") != "" {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if want := workers / 2; created != want {
		t.Errorf("registered %d accounts concurrently, want %d", created, want)
	}
	if want := workers / 2; onchange != want {
		t.Errorf("%d challenges answered with 401, want %d", onchange, want)
	}
}

// TestRegistrationTokenFlow covers a server that only registers clients holding
// a registration token.
func TestRegistrationTokenFlow(t *testing.T) {
	server := registrationServer(t, "invite-one")

	validity := server.MustDo(http.MethodGet, "/_matrix/client/v1/register/m.login.registration_token/validity", "", nil, http.StatusOK)
	if supported, _ := validity.Body["supported"].(bool); !supported {
		t.Errorf("supported = %v, want true (%s)", validity.Body["supported"], validity.Raw)
	}

	challenge := server.MustDo(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{}, http.StatusUnauthorized)
	stages := flowStages(t, challenge.Body)
	if len(stages) != 1 || stages[0][0] != "m.login.registration_token" {
		t.Fatalf("flows = %v, want the registration token stage", stages)
	}

	cases := []struct {
		name    string
		auth    map[string]any
		want    int
		errCode string
	}{
		{name: "wrong token", auth: map[string]any{"type": "m.login.registration_token", "token": "nope"}, want: http.StatusBadRequest, errCode: "M_INVALID_PARAM"},
		{name: "no token", auth: map[string]any{"type": "m.login.registration_token"}, want: http.StatusBadRequest, errCode: "M_INVALID_PARAM"},
		{name: "dummy is closed", auth: map[string]any{"type": "m.login.dummy"}, want: http.StatusUnauthorized, errCode: "M_UNAUTHORIZED"},
		{name: "good token", auth: map[string]any{"type": "m.login.registration_token", "token": "invite-one"}, want: http.StatusOK},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := server.Do(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{
				"username": fmt.Sprintf("tokened%d", i),
				"password": "wonderland",
				"auth":     tc.auth,
			})
			if resp.Status != tc.want {
				t.Fatalf("got %d %s, want %d", resp.Status, resp.Raw, tc.want)
			}
			if tc.errCode != "" && resp.ErrCode() != tc.errCode {
				t.Fatalf("got errcode %q, want %q (%s)", resp.ErrCode(), tc.errCode, resp.Raw)
			}
		})
	}

	// An open server reports the token flow as unavailable.
	open := NewServer(t)
	resp := open.MustDo(http.MethodGet, "/_matrix/client/v1/register/m.login.registration_token/validity", "", nil, http.StatusOK)
	if supported, _ := resp.Body["supported"].(bool); supported {
		t.Errorf("an open server claims token support: %s", resp.Raw)
	}
}

// flowStages returns the stages of every flow in a User-Interactive Auth body.
func flowStages(t *testing.T, body map[string]any) [][]string {
	t.Helper()
	var out [][]string
	flows, ok := body["flows"].([]any)
	if !ok {
		t.Fatalf("the response has no flows array: %v", body)
	}
	for _, entry := range flows {
		flow, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		raw, ok := flow["stages"].([]any)
		if !ok {
			continue
		}
		stages := make([]string, 0, len(raw))
		for _, stage := range raw {
			if name, ok := stage.(string); ok {
				stages = append(stages, name)
			}
		}
		out = append(out, stages)
	}
	return out
}

// deviceNamed reports whether the device list names a device exactly.
func deviceNamed(resp Response, deviceID, name string) bool {
	devices, _ := resp.Body["devices"].([]any)
	for _, entry := range devices {
		device, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if device["device_id"] == deviceID {
			return device["display_name"] == name
		}
	}
	return false
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
