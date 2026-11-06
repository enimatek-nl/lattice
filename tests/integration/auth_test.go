package integration

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // the shared-secret protocol specifies HMAC-SHA1
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/config"
)

// These tests mirror the expectations of the Complement csapi suites
// (apidoc_register_test.go, apidoc_login_test.go, apidoc_logout_test.go,
// apidoc_device_management_test.go, apidoc_server_capabilities_test.go) so that
// protocol regressions are caught without a Docker run.

// TestRegisterFlow mirrors "POST /register can create a user" (AC-2.1).
func TestRegisterFlow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	userID, token, deviceID := h.register(t, "alice", "s3cr3tPassphrase", "")
	if userID != "@alice:hs1" {
		t.Errorf("user_id = %q, want @alice:hs1", userID)
	}
	if len(token) < 40 {
		t.Errorf("access_token looks too short: %d chars", len(token))
	}
	if deviceID == "" {
		t.Errorf("device_id missing")
	}

	whoami := h.get("/_matrix/client/v3/account/whoami", token)
	h.mustStatus(t, whoami, http.StatusOK)
	decoded := whoami.decoded(t)
	if got := decoded["user_id"]; got != userID {
		t.Errorf("whoami user_id = %v, want %q", got, userID)
	}
	// The token belongs to a device, and the client needs to know which one: keys, push rules
	// and the device list are all per-device things.
	if got := decoded["device_id"]; got != deviceID {
		t.Errorf("whoami device_id = %v, want %q", got, deviceID)
	}
}

// TestRegisterChallengeStructure mirrors "POST {} returns a set of flows".
func TestRegisterChallengeStructure(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	res := h.raw(t, http.MethodPost, "/_matrix/client/v3/register", []byte(`{}`), "")
	h.mustStatus(t, res, http.StatusUnauthorized)
	if got := res.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	body := res.decoded(t)
	flows, ok := body["flows"].([]any)
	if !ok || len(flows) != 1 {
		t.Fatalf("flows = %v", body["flows"])
	}
	flow, ok := flows[0].(map[string]any)
	if !ok {
		t.Fatalf("flow is not an object: %v", flows[0])
	}
	stages, ok := flow["stages"].([]any)
	if !ok || len(stages) != 1 || stages[0] != "m.login.dummy" {
		t.Errorf("stages = %v, want [m.login.dummy]", flow["stages"])
	}
	if _, ok := flow["params"].(map[string]any); !ok {
		t.Errorf("flow.params must be an object, got %v", flow["params"])
	}
	for _, field := range []string{"session", "flows", "params", "completed", "errcode", "error"} {
		if _, ok := body[field]; !ok {
			t.Errorf("challenge is missing %q: %s", field, res.body)
		}
	}
	if session, ok := body["session"].(string); !ok || session == "" {
		t.Errorf("session must be a non-empty string: %v", body["session"])
	}
	if completed, ok := body["completed"].([]any); !ok || len(completed) != 0 {
		t.Errorf("completed must be an empty array, got %v", body["completed"])
	}
}

// TestRegisterDowncasesUsernames mirrors "POST /register downcases capitals in
// usernames".
func TestRegisterDowncasesUsernames(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	for _, username := range []string{"user-UPPER", "MiXeD-CaSe", "A.B_C"} {
		want := "@" + strings.ToLower(username) + ":hs1"
		got, _, _ := h.register(t, username, "s3cr3tPassphrase", "")
		if got != want {
			t.Errorf("register(%q) = %q, want %q", username, got, want)
		}
	}
}

// TestRegisterRejectsSpecialCharacters mirrors "POST /register rejects usernames
// with special characters". Complement expects the rejection on the *first*
// request, because servers validate the request body before the auth challenge.
func TestRegisterRejectsSpecialCharacters(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	specialChars := []string{"!", `"`, ":", "?", `\`, "@", "[", "]", "{", "|", "}", "£", "é", "\n", "'"}
	for _, ch := range specialChars {
		username := "user-" + ch + "-reject-please"
		res := h.post(t, "/_matrix/client/v3/register", map[string]any{
			"username": username,
			"password": "sUp3rs3kr1t",
		}, "")
		if res.status != http.StatusBadRequest {
			t.Errorf("register(%q): status = %d, want 400 (body %s)", username, res.status, res.body)
			continue
		}
		if code := res.errcode(t); code != "M_INVALID_USERNAME" {
			t.Errorf("register(%q): errcode = %s, want M_INVALID_USERNAME", username, code)
		}
	}
}

// TestRegisterAllowsPunctuationUsernames mirrors "POST /register allows
// registration of usernames with q3._=-/".
func TestRegisterAllowsPunctuationUsernames(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	for _, ch := range []string{"q", "3", ".", "_", "=", "-", "/"} {
		localpart := "chrtestuser" + ch
		userID, _, _ := h.register(t, localpart, "sUp3rs3kr1t", "")
		if userID != "@"+localpart+":hs1" {
			t.Errorf("register(%q) = %q", localpart, userID)
		}
	}
}

// TestRegisterRejectsDuplicate mirrors "POST /register rejects if user already
// exists", which re-sends the request with the auth member removed.
func TestRegisterRejectsDuplicate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	body := map[string]any{"username": "post-can-create-a-user-once", "password": "sUp3rs3kr1t"}
	challenge := h.post(t, "/_matrix/client/v3/register", body, "")
	h.mustStatus(t, challenge, http.StatusUnauthorized)
	session, _ := challenge.decoded(t)["session"].(string)
	body["auth"] = map[string]any{"session": session, "type": "m.login.dummy"}
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/register", body, ""), http.StatusOK)

	delete(body, "auth")
	res := h.post(t, "/_matrix/client/v3/register", body, "")
	h.mustStatus(t, res, http.StatusBadRequest)
	if code := res.errcode(t); code != "M_USER_IN_USE" {
		t.Errorf("errcode = %s, want M_USER_IN_USE", code)
	}
}

// TestRegisterWeakPassword checks the advertised password policy error.
func TestRegisterWeakPassword(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	res := h.raw(t, http.MethodPost, "/_matrix/client/v3/register",
		[]byte(`{"username":"shorty","password":"abc","auth":{"type":"m.login.dummy"}}`), "")
	h.mustStatus(t, res, http.StatusBadRequest)
	if code := res.errcode(t); code != "M_WEAK_PASSWORD" {
		t.Errorf("errcode = %s, want M_WEAK_PASSWORD", code)
	}
}

// TestRegisterDummyRequiresSession mirrors "Registration without a session
// fails": once the server issues a session, an m.login.dummy attempt that omits
// it must not be waved through.
func TestRegisterDummyRequiresSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	body := map[string]any{"username": "auth-requires-session", "password": "sUp3rs3kr1t"}
	challenge := h.post(t, "/_matrix/client/v3/register", body, "")
	h.mustStatus(t, challenge, http.StatusUnauthorized)
	session, _ := challenge.decoded(t)["session"].(string)
	if session == "" {
		t.Skip("server does not issue UIA sessions")
	}
	body["auth"] = map[string]any{"type": "m.login.dummy"}
	res := h.post(t, "/_matrix/client/v3/register", body, "")
	if res.status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (body %s)", res.status, res.body)
	}

	// With the session, the same request succeeds.
	body["auth"] = map[string]any{"type": "m.login.dummy", "session": session}
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/register", body, ""), http.StatusOK)
}

// TestRegisterDeviceID mirrors "POST /register returns the same device_id as that
// in the request" and stores the initial device display name.
func TestRegisterDeviceID(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	challenge := h.post(t, "/_matrix/client/v3/register", map[string]any{
		"username":                    "user-device",
		"password":                    "sUp3rs3kr1t",
		"device_id":                   "my_device_id",
		"initial_device_display_name": "display_name",
	}, "")
	session, _ := challenge.decoded(t)["session"].(string)
	res := h.post(t, "/_matrix/client/v3/register", map[string]any{
		"username":                    "user-device",
		"password":                    "sUp3rs3kr1t",
		"device_id":                   "my_device_id",
		"initial_device_display_name": "display_name",
		"auth":                        map[string]any{"session": session, "type": "m.login.dummy"},
	}, "")
	h.mustStatus(t, res, http.StatusOK)
	if got := res.decoded(t)["device_id"]; got != "my_device_id" {
		t.Errorf("device_id = %v, want my_device_id", got)
	}

	detail := h.get("/_matrix/client/v3/devices/my_device_id", tokenOf(t, res))
	h.mustStatus(t, detail, http.StatusOK)
	if got := detail.decoded(t)["display_name"]; got != "display_name" {
		t.Errorf("display_name = %v, want display_name", got)
	}
}

// TestRegisterAvailable mirrors the three /register/available cases in
// Complement.
func TestRegisterAvailable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	res := h.get("/_matrix/client/v3/register/available?username=username_should_be_available", "")
	h.mustStatus(t, res, http.StatusOK)
	if got := res.decoded(t)["available"]; got != true {
		t.Errorf("available = %v, want true", got)
	}

	h.register(t, "username_not_available", "sUp3rs3kr1t", "")
	taken := h.get("/_matrix/client/v3/register/available?username=username_not_available", "")
	h.mustStatus(t, taken, http.StatusBadRequest)
	if code := taken.errcode(t); code != "M_USER_IN_USE" {
		t.Errorf("errcode = %s, want M_USER_IN_USE", code)
	}
	if _, ok := taken.decoded(t)["error"]; !ok {
		t.Errorf("error member missing")
	}

	invalid := h.get("/_matrix/client/v3/register/available?username=username,should_not_be_valid", "")
	h.mustStatus(t, invalid, http.StatusBadRequest)
	if code := invalid.errcode(t); code != "M_INVALID_USERNAME" {
		t.Errorf("errcode = %s, want M_INVALID_USERNAME", code)
	}
}

// TestRegisterClosed checks that a closed server refuses registration instead of
// silently allowing it.
func TestRegisterClosed(t *testing.T) {
	t.Parallel()
	closed := newHarnessWith(t, t.TempDir(), func(c *config.Config) {
		c.Registration.Open = false
		c.Registration.SharedSecret = ""
	})

	body := map[string]any{"username": "nope", "password": "sUp3rs3kr1t"}
	challenge := closed.post(t, "/_matrix/client/v3/register", body, "")
	if challenge.status != http.StatusUnauthorized && challenge.status != http.StatusForbidden {
		t.Fatalf("status = %d, want 401 or 403 (body %s)", challenge.status, challenge.body)
	}
	decoded := challenge.decoded(t)
	if session, ok := decoded["session"].(string); ok {
		body["auth"] = map[string]any{"session": session, "type": "m.login.dummy"}
	} else {
		body["auth"] = map[string]any{"type": "m.login.dummy"}
	}
	res := closed.post(t, "/_matrix/client/v3/register", body, "")
	if res.status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", res.status, res.body)
	}
	if code := res.errcode(t); code != "M_FORBIDDEN" {
		t.Errorf("errcode = %s, want M_FORBIDDEN", code)
	}
}

// TestSharedSecretRegistration mirrors registerSharedSecret in Complement,
// including the admin, downcasing and symbol-rejection variants.
func TestSharedSecretRegistration(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	cases := []struct {
		name       string
		username   string
		admin      bool
		wantStatus int
		wantUserID string
		errcode    string
	}{
		{"admin", "adminuser", true, http.StatusOK, "@adminuser:hs1", ""},
		{"plain", "user-shared-secret", false, http.StatusOK, "@user-shared-secret:hs1", ""},
		{"downcases capitals", "user-UPPER-shared-SECRET", false, http.StatusOK, "@user-upper-shared-secret:hs1", ""},
		{"disallows symbols", "us,er", false, http.StatusBadRequest, "", "M_INVALID_USERNAME"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			nonceRes := h.get("/_synapse/admin/v1/register", "")
			h.mustStatus(t, nonceRes, http.StatusOK)
			nonce, _ := nonceRes.decoded(t)["nonce"].(string)
			if nonce == "" {
				t.Fatalf("no nonce issued: %s", nonceRes.body)
			}
			const password = "sUp3rs3kr1t"
			res := h.post(t, "/_synapse/admin/v1/register", map[string]any{
				"nonce":    nonce,
				"username": tc.username,
				"password": password,
				"mac":      sharedSecretMAC("complement", nonce, tc.username, password, tc.admin),
				"admin":    tc.admin,
			}, "")
			if res.status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", res.status, tc.wantStatus, res.body)
			}
			if tc.wantUserID != "" {
				if got := res.decoded(t)["user_id"]; got != tc.wantUserID {
					t.Errorf("user_id = %v, want %q", got, tc.wantUserID)
				}
				if res.decoded(t)["access_token"] == nil {
					t.Errorf("access_token missing")
				}
			}
			if tc.errcode != "" {
				if code := res.errcode(t); code != tc.errcode {
					t.Errorf("errcode = %s, want %s", code, tc.errcode)
				}
			}
		})
	}
}

// TestSharedSecretRejectsBadMAC and nonce reuse: the admin endpoint must not be a
// bypass for authentication.
func TestSharedSecretRejectsBadMACAndReplay(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	nonceRes := h.get("/_synapse/admin/v1/register", "")
	nonce, _ := nonceRes.decoded(t)["nonce"].(string)
	const password = "sUp3rs3kr1t"
	mac := sharedSecretMAC("complement", nonce, "replay-user", password, false)

	h.mustStatus(t, h.post(t, "/_synapse/admin/v1/register", map[string]any{
		"nonce": nonce, "username": "replay-user", "password": password, "mac": mac,
	}, ""), http.StatusOK)

	replay := h.post(t, "/_synapse/admin/v1/register", map[string]any{
		"nonce": nonce, "username": "second", "password": password, "mac": mac,
	}, "")
	if replay.status == http.StatusOK {
		t.Errorf("nonce replay was accepted")
	}

	fresh := h.get("/_synapse/admin/v1/register", "")
	freshNonce, _ := fresh.decoded(t)["nonce"].(string)
	bad := h.post(t, "/_synapse/admin/v1/register", map[string]any{
		"nonce": freshNonce, "username": "third", "password": password,
		"mac": strings.Repeat("0", 40),
	}, "")
	if bad.status == http.StatusOK {
		t.Errorf("a bogus MAC was accepted")
	}

	noSecret := h.post(t, "/_synapse/admin/v1/register", map[string]any{
		"nonce": freshNonce, "username": "fourth", "password": password,
	}, "")
	if noSecret.status == http.StatusOK {
		t.Errorf("a request without a MAC was accepted")
	}
}

// TestSharedSecretDisabledWithoutSecret checks that the admin endpoint is absent
// when no secret is configured.
func TestSharedSecretDisabledWithoutSecret(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, t.TempDir(), func(c *config.Config) { c.Registration.SharedSecret = "" })
	res := h.get("/_synapse/admin/v1/register", "")
	h.mustStatus(t, res, http.StatusNotFound)
	if code := res.errcode(t); code != "M_UNRECOGNIZED" {
		t.Errorf("errcode = %s, want M_UNRECOGNIZED", code)
	}
}

// TestLoginFlow mirrors apidoc_login_test.go (AC-2.3).
func TestLoginFlow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	userID, _, _ := h.register(t, "login-user", "superuser", "")

	cases := []struct {
		name       string
		user       string
		wantStatus int
	}{
		{"full mxid", userID, http.StatusOK},
		{"localpart", "login-user", http.StatusOK},
		{"uppercase mxid", "@LOGIN-USER:hs1", http.StatusOK},
		{"uppercase localpart", "LOGIN-USER", http.StatusOK},
		{"non-existing user", "i-dont-exist", http.StatusForbidden},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			res := h.post(t, "/_matrix/client/v3/login", map[string]any{
				"type":       "m.login.password",
				"identifier": map[string]any{"type": "m.id.user", "user": tc.user},
				"password":   "superuser",
			}, "")
			h.mustStatus(t, res, tc.wantStatus)
			if tc.wantStatus != http.StatusOK {
				return
			}
			body := res.decoded(t)
			if body["user_id"] != userID {
				t.Errorf("user_id = %v, want %q", body["user_id"], userID)
			}
			token := tokenOf(t, res)
			if whoami := h.get("/_matrix/client/v3/account/whoami", token); whoami.status != http.StatusOK {
				t.Errorf("the issued token does not authenticate: %s", whoami.body)
			}
		})
	}

	t.Run("wrong password", func(t *testing.T) {
		res := h.post(t, "/_matrix/client/v3/login", map[string]any{
			"type":       "m.login.password",
			"identifier": map[string]any{"type": "m.id.user", "user": userID},
			"password":   "wrong_password",
		}, "")
		h.mustStatus(t, res, http.StatusForbidden)
		if code := res.errcode(t); code != "M_FORBIDDEN" {
			t.Errorf("errcode = %s, want M_FORBIDDEN", code)
		}
	})

	t.Run("unknown login type", func(t *testing.T) {
		res := h.post(t, "/_matrix/client/v3/login", map[string]any{
			"type":       "m.login.magic",
			"identifier": map[string]any{"type": "m.id.user", "user": userID},
			"password":   "superuser",
		}, "")
		if res.status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (body %s)", res.status, res.body)
		}
	})

	t.Run("device id and display name", func(t *testing.T) {
		res := h.post(t, "/_matrix/client/v3/login", map[string]any{
			"type":                        "m.login.password",
			"identifier":                  map[string]any{"type": "m.id.user", "user": userID},
			"password":                    "superuser",
			"device_id":                   "test_device_id",
			"initial_device_display_name": "device display",
		}, "")
		h.mustStatus(t, res, http.StatusOK)
		if got := res.decoded(t)["device_id"]; got != "test_device_id" {
			t.Errorf("device_id = %v, want test_device_id", got)
		}
		token := tokenOf(t, res)
		detail := h.get("/_matrix/client/v3/devices/test_device_id", token)
		h.mustStatus(t, detail, http.StatusOK)
		if got := detail.decoded(t)["display_name"]; got != "device display" {
			t.Errorf("display_name = %v, want 'device display'", got)
		}
		if got := detail.decoded(t)["device_id"]; got != "test_device_id" {
			t.Errorf("device_id = %v", got)
		}
	})

	t.Run("GET /login yields a set of flows", func(t *testing.T) {
		res := h.raw(t, http.MethodGet, "/_matrix/client/v3/login", []byte(`{}`), "")
		h.mustStatus(t, res, http.StatusOK)
		if got := res.header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		flows, ok := res.decoded(t)["flows"].([]any)
		if !ok || len(flows) == 0 {
			t.Fatalf("flows missing: %s", res.body)
		}
		for _, flow := range flows {
			object, ok := flow.(map[string]any)
			if !ok {
				t.Fatalf("flow is not an object: %v", flow)
			}
			_, hasStages := object["stages"].([]any)
			hasType, _ := object["type"].(string)
			if !hasStages && hasType == "" {
				t.Errorf("flow needs 'stages' or 'type': %v", object)
			}
		}
	})
}

// TestDeviceManagement mirrors apidoc_device_management_test.go (AC-2.4).
func TestDeviceManagement(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, initialDevice := h.register(t, "managed", "superuser", "")
	second := h.post(t, "/_matrix/client/v3/login", map[string]any{
		"type":                        "m.login.password",
		"identifier":                  map[string]any{"type": "m.id.user", "user": "@managed:hs1"},
		"password":                    "superuser",
		"device_id":                   "login_device",
		"initial_device_display_name": "device display",
	}, "")
	h.mustStatus(t, second, http.StatusOK)

	list := h.get("/_matrix/client/v3/devices", aliceToken)
	h.mustStatus(t, list, http.StatusOK)
	devices, _ := list.decoded(t)["devices"].([]any)
	if len(devices) != 2 {
		t.Fatalf("devices = %s, want two", list.body)
	}
	seen := map[string]bool{}
	for _, device := range devices {
		object, ok := device.(map[string]any)
		if !ok {
			t.Fatalf("device entry is not an object: %v", device)
		}
		id, _ := object["device_id"].(string)
		seen[id] = true
	}
	for _, want := range []string{initialDevice, "login_device"} {
		if !seen[want] {
			t.Errorf("GET /devices did not list %q", want)
		}
	}

	detail := h.get("/_matrix/client/v3/devices/login_device", aliceToken)
	h.mustStatus(t, detail, http.StatusOK)
	if got := detail.decoded(t)["display_name"]; got != "device display" {
		t.Errorf("display_name = %v, want 'device display'", got)
	}

	unknown := h.get("/_matrix/client/v3/devices/unknown_device", aliceToken)
	h.mustStatus(t, unknown, http.StatusNotFound)

	updated := h.do(t, http.MethodPut, h.clientURL+"/_matrix/client/v3/devices/login_device",
		map[string]any{"display_name": "new device display"}, aliceToken, h.client)
	h.mustStatus(t, updated, http.StatusOK)
	after := h.get("/_matrix/client/v3/devices/login_device", aliceToken)
	if got := after.decoded(t)["display_name"]; got != "new device display" {
		t.Errorf("display_name after PUT = %v", got)
	}
	unknownPut := h.do(t, http.MethodPut, h.clientURL+"/_matrix/client/v3/devices/unknown_device",
		map[string]any{"display_name": "nope"}, aliceToken, h.client)
	h.mustStatus(t, unknownPut, http.StatusNotFound)

	// DELETE without auth body yields a UIA challenge.
	noAuth := h.do(t, http.MethodDelete, h.clientURL+"/_matrix/client/v3/devices/login_device", nil, aliceToken, h.client)
	h.mustStatus(t, noAuth, http.StatusUnauthorized)
	challenge := noAuth.decoded(t)
	for _, field := range []string{"session", "flows", "params"} {
		if _, ok := challenge[field]; !ok {
			t.Errorf("UIA challenge missing %q: %s", field, noAuth.body)
		}
	}

	wrongPassword := h.do(t, http.MethodDelete, h.clientURL+"/_matrix/client/v3/devices/login_device", map[string]any{
		"auth": map[string]any{
			"identifier": map[string]any{"type": "m.id.user", "user": "@managed:hs1"},
			"type":       "m.login.password",
			"password":   "super-wrong-password",
		},
	}, aliceToken, h.client)
	h.mustStatus(t, wrongPassword, http.StatusUnauthorized)
	failed := wrongPassword.decoded(t)
	for _, field := range []string{"flows", "params", "session", "error", "errcode"} {
		if _, ok := failed[field]; !ok {
			t.Errorf("failed UIA response missing %q: %s", field, wrongPassword.body)
		}
	}
	if code := wrongPassword.errcode(t); code != "M_FORBIDDEN" {
		t.Errorf("errcode = %s, want M_FORBIDDEN", code)
	}
	if still := h.get("/_matrix/client/v3/devices/login_device", aliceToken); still.status != http.StatusOK {
		t.Errorf("device survived a failed UIA delete")
	}

	// Credentials from another account must not be able to delete the device.
	bobID, _, _ := h.register(t, "bob", "bobspassword", "")
	_ = bobID
	foreign := h.do(t, http.MethodDelete, h.clientURL+"/_matrix/client/v3/devices/login_device", map[string]any{
		"auth": map[string]any{
			"identifier": map[string]any{"type": "m.id.user", "user": "@bob:hs1"},
			"type":       "m.login.password",
			"password":   "bobspassword",
		},
	}, aliceToken, h.client)
	h.mustStatus(t, foreign, http.StatusForbidden)
	if still := h.get("/_matrix/client/v3/devices/login_device", aliceToken); still.status != http.StatusOK {
		t.Errorf("device survived an authorised-but-wrong-user delete")
	}

	// Correct credentials delete the device and invalidate its token.
	secondToken := tokenOf(t, second)
	okDelete := h.do(t, http.MethodDelete, h.clientURL+"/_matrix/client/v3/devices/login_device", map[string]any{
		"auth": map[string]any{
			"identifier": map[string]any{"type": "m.id.user", "user": "@managed:hs1"},
			"type":       "m.login.password",
			"password":   "superuser",
		},
	}, aliceToken, h.client)
	h.mustStatus(t, okDelete, http.StatusOK)
	if gone := h.get("/_matrix/client/v3/devices/login_device", aliceToken); gone.status != http.StatusNotFound {
		t.Errorf("deleted device still readable: %d", gone.status)
	}
	if whoami := h.get("/_matrix/client/v3/account/whoami", secondToken); whoami.status != http.StatusUnauthorized {
		t.Errorf("the deleted device's token is still valid: %d", whoami.status)
	}
	if whoami := h.get("/_matrix/client/v3/account/whoami", aliceToken); whoami.status != http.StatusOK {
		t.Errorf("the calling session must survive: %d", whoami.status)
	}
}

// TestLogout mirrors apidoc_logout_test.go, including the token error codes.
func TestLogout(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, verifyToken, _ := h.register(t, "verify", "superuser", "")

	createSession := func() (string, string) {
		res := h.post(t, "/_matrix/client/v3/login", map[string]any{
			"type":       "m.login.password",
			"identifier": map[string]any{"type": "m.id.user", "user": "@verify:hs1"},
			"password":   "superuser",
		}, "")
		h.mustStatus(t, res, http.StatusOK)
		return res.decoded(t)["device_id"].(string), tokenOf(t, res)
	}

	// Can logout current device.
	deviceID, tokenToLogout := createSession()
	list := h.get("/_matrix/client/v3/devices", verifyToken)
	if devices, _ := list.decoded(t)["devices"].([]any); len(devices) != 2 {
		t.Fatalf("devices = %s, want two", list.body)
	}
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/logout", map[string]any{}, tokenToLogout), http.StatusOK)
	if whoami := h.get("/_matrix/client/v3/account/whoami", tokenToLogout); whoami.status != http.StatusUnauthorized {
		t.Errorf("the logged-out token still works: %d", whoami.status)
	}
	afterList := h.get("/_matrix/client/v3/devices", verifyToken)
	devices, _ := afterList.decoded(t)["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("devices after logout = %s, want one", afterList.body)
	}
	entry, _ := devices[0].(map[string]any)
	if entry["device_id"] == deviceID {
		t.Errorf("the second device still exists")
	}

	// Can logout all devices.
	_, tokenToLogoutAll := createSession()
	before, _ := h.get("/_matrix/client/v3/devices", verifyToken).decoded(t)["devices"].([]any)
	if len(before) != 2 {
		t.Fatalf("devices = %d, want two before logout/all", len(before))
	}
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/logout/all", map[string]any{}, tokenToLogoutAll), http.StatusOK)
	for _, token := range []string{tokenToLogoutAll, verifyToken} {
		if whoami := h.get("/_matrix/client/v3/account/whoami", token); whoami.status != http.StatusUnauthorized {
			t.Errorf("logout/all left a session alive: %d", whoami.status)
		}
	}

	// Invalid access token.
	invalid := h.post(t, "/_matrix/client/v3/logout", map[string]any{}, "invalidAccessToken")
	h.mustStatus(t, invalid, http.StatusUnauthorized)
	if code := invalid.errcode(t); code != "M_UNKNOWN_TOKEN" {
		t.Errorf("errcode = %s, want M_UNKNOWN_TOKEN", code)
	}

	// Missing access token.
	missing := h.post(t, "/_matrix/client/v3/logout", map[string]any{}, "")
	h.mustStatus(t, missing, http.StatusUnauthorized)
	if code := missing.errcode(t); code != "M_MISSING_TOKEN" {
		t.Errorf("errcode = %s, want M_MISSING_TOKEN", code)
	}
}

// TestServerCapabilities mirrors apidoc_server_capabilities_test.go.
func TestServerCapabilities(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "capable", "sUp3rs3kr1t", "")

	unauthed := h.get("/_matrix/client/v3/capabilities", "")
	h.mustStatus(t, unauthed, http.StatusUnauthorized)

	res := h.get("/_matrix/client/v3/capabilities", token)
	h.mustStatus(t, res, http.StatusOK)
	body := res.decoded(t)
	capabilities, ok := body["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities is not an object: %v", body["capabilities"])
	}
	for _, key := range []string{"m.room_versions", "m.change_password"} {
		if _, ok := capabilities[key]; !ok {
			t.Errorf("capabilities is missing %q: %s", key, res.body)
		}
	}
	roomVersions, ok := capabilities["m.room_versions"].(map[string]any)
	if !ok {
		t.Errorf("m.room_versions is not an object: %v", capabilities["m.room_versions"])
	} else {
		if _, ok := roomVersions["default"].(string); !ok {
			t.Errorf("m.room_versions.default missing")
		}
		if _, ok := roomVersions["available"].(map[string]any); !ok {
			t.Errorf("m.room_versions.available missing")
		}
	}
}

// TestAccessTokenErrorCodes checks the two token error codes the specification
// distinguishes.
func TestAccessTokenErrorCodes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	paths := []string{
		"/_matrix/client/v3/account/whoami",
		"/_matrix/client/v3/devices",
		"/_matrix/client/v3/capabilities",
	}
	for _, path := range paths {
		res := h.get(path, "")
		if res.status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, res.status)
		}
		if code := res.errcode(t); code != "M_MISSING_TOKEN" {
			t.Errorf("%s: errcode = %s, want M_MISSING_TOKEN", path, code)
		}
		var probe map[string]any
		if err := json.Unmarshal(res.body, &probe); err != nil {
			t.Errorf("%s: body is not JSON: %s", path, res.body)
		}
	}

	for _, path := range append(paths, "/_matrix/client/v3/logout", "/_matrix/client/v3/logout/all") {
		var res response
		if strings.HasSuffix(path, "logout") || strings.HasSuffix(path, "logout/all") {
			res = h.post(t, path, map[string]any{}, "000000000000000000000000bogus")
			if code := res.errcode(t); code != "M_UNKNOWN_TOKEN" {
				t.Errorf("%s: errcode = %s, want M_UNKNOWN_TOKEN", path, code)
			}
			continue
		}
		res = h.get(path, "000000000000000000000000bogus")
		if res.status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, res.status)
		}
		if code := res.errcode(t); code != "M_UNKNOWN_TOKEN" {
			t.Errorf("%s: errcode = %s, want M_UNKNOWN_TOKEN", path, code)
		}
	}
}

// TestTokenViaQueryAndBearer checks both accepted credential transports.
func TestTokenViaQueryAndBearer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "transport", "sUp3rs3kr1t", "")

	bearer := h.get("/_matrix/client/v3/account/whoami", token)
	h.mustStatus(t, bearer, http.StatusOK)

	query := h.get("/_matrix/client/v3/account/whoami?access_token="+token, "")
	h.mustStatus(t, query, http.StatusOK)

	// A malformed header must not be treated as a credential.
	bad := h.raw(t, http.MethodGet, "/_matrix/client/v3/account/whoami", nil, "")
	h.mustStatus(t, bad, http.StatusUnauthorized)
}

// tokenOf extracts access_token from a response, failing the test if absent.
func tokenOf(t *testing.T, res response) string {
	t.Helper()
	token, _ := res.decoded(t)["access_token"].(string)
	if token == "" {
		t.Fatalf("no access_token in response: %s", res.body)
	}
	return token
}

// sharedSecretMAC computes the Synapse shared-secret MAC: HMAC-SHA1 over the
// NUL-separated nonce, username, password and admin marker.
func sharedSecretMAC(secret, nonce, username, password string, admin bool) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(nonce))
	mac.Write([]byte{0x00})
	mac.Write([]byte(username))
	mac.Write([]byte{0x00})
	mac.Write([]byte(password))
	mac.Write([]byte{0x00})
	if admin {
		mac.Write([]byte("admin"))
	} else {
		mac.Write([]byte("notadmin"))
	}
	return hex.EncodeToString(mac.Sum(nil))
}
