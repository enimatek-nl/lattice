package integration

import (
	"net/http"
	"testing"
)

// TestChangingAPasswordEndsTheOldOne walks the whole of what a password change is for: the old
// password stops opening the door, the new one opens it, the session that asked stays open, and
// the sessions that did not are shut — unless the client asked otherwise.

func TestChangingAPasswordEndsTheOldOne(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID, token, _ := h.register(t, "pwchange", "the-first-password", "")
	other := loginWith(t, h, userID, "the-first-password")
	otherToken, otherDevice := other.token, other.deviceID

	// Both sessions work before.
	h.mustStatus(t, h.get("/_matrix/client/v3/account/whoami", token), http.StatusOK)
	h.mustStatus(t, h.get("/_matrix/client/v3/account/whoami", otherToken), http.StatusOK)

	// With no auth at all the endpoint answers a User-Interactive Auth challenge naming the
	// stage it wants, so a client can find the flow rather than be told to guess.
	challenge := h.mustStatus(t, h.post(t, "/_matrix/client/v3/account/password", map[string]any{}, token),
		http.StatusUnauthorized).decoded(t)
	if code := challenge["errcode"]; code != "M_FORBIDDEN" {
		t.Errorf("the challenge answered %v, want M_FORBIDDEN", code)
	}
	if !offersPasswordStage(challenge) {
		t.Errorf("the challenge offers no m.login.password flow: %v", challenge["flows"])
	}

	// The wrong password is refused, and the account is not left in a half state: the first
	// password still works.
	bad := passwordChangeBody(userID, "not-the-password", "the-second-password", nil)
	res := h.post(t, "/_matrix/client/v3/account/password", bad, token)
	if res.status != http.StatusUnauthorized || res.errcode(t) != "M_FORBIDDEN" {
		t.Fatalf("a password change with the wrong password: status %d %s, want 401 M_FORBIDDEN (body %s)",
			res.status, res.errcode(t), res.body)
	}
	if refused := h.post(t, "/_matrix/client/v3/login", map[string]any{
		"identifier": map[string]any{"type": "m.id.user", "user": userID},
		"type":       "m.login.password",
		"password":   "the-first-password",
	}, ""); refused.status != http.StatusOK {
		// A refused change must not have touched anything: the password that was
		// offered at the door is still the one that opens it.
		t.Errorf("the first password stopped working after a refused change: status %d", refused.status)
	}

	// A change with no new password is a bad request, not a way to lock oneself out.
	noNew := h.post(t, "/_matrix/client/v3/account/password",
		passwordChangeBody(userID, "the-first-password", "", nil), token)
	if noNew.status != http.StatusBadRequest || noNew.errcode(t) != "M_INVALID_PARAM" {
		t.Errorf("a change with no new password: status %d %s, want 400 M_INVALID_PARAM (body %s)",
			noNew.status, noNew.errcode(t), noNew.body)
	}

	// The real change.
	changed := h.mustStatus(t, h.post(t, "/_matrix/client/v3/account/password",
		passwordChangeBody(userID, "the-first-password", "the-second-password", nil), token),
		http.StatusOK)
	if len(changed.decoded(t)) != 0 {
		t.Errorf("a successful password change answered %s, want {}", changed.body)
	}

	// The old password is gone.
	res = h.post(t, "/_matrix/client/v3/login", map[string]any{
		"identifier": map[string]any{"type": "m.id.user", "user": userID},
		"type":       "m.login.password",
		"password":   "the-first-password",
	}, "")
	if res.status != http.StatusForbidden || res.errcode(t) != "M_FORBIDDEN" {
		t.Errorf("login with the old password: status %d %s, want 403 M_FORBIDDEN (body %s)",
			res.status, res.errcode(t), res.body)
	}

	// The new one opens the door, and names the same account.
	relogged := h.mustStatus(t, h.post(t, "/_matrix/client/v3/login", map[string]any{
		"identifier": map[string]any{"type": "m.id.user", "user": userID},
		"type":       "m.login.password",
		"password":   "the-second-password",
	}, ""), http.StatusOK).decoded(t)
	if loggedAs, _ := relogged["user_id"].(string); loggedAs != userID {
		t.Errorf("login with the new password returned user_id %q, want %q", loggedAs, userID)
	}

	// The session that changed the password keeps working; the one that did not is over.
	h.mustStatus(t, h.get("/_matrix/client/v3/account/whoami", token), http.StatusOK)
	revoked := h.get("/_matrix/client/v3/account/whoami", otherToken)
	if revoked.status != http.StatusUnauthorized {
		t.Errorf("the session that did not ask: status %d, want 401 (body %s)", revoked.status, revoked.body)
	}
	if code := revoked.errcode(t); code != "M_UNKNOWN_TOKEN" {
		t.Errorf("the revoked session was refused with %s, want M_UNKNOWN_TOKEN", code)
	}
	// The device still exists; it is the session that ended. Complement and every client
	// distinguish "this token is dead" from "this device never was".
	if device := h.mustStatus(t, h.get("/_matrix/client/v3/devices/"+otherDevice, token),
		http.StatusOK).decoded(t); device["device_id"] != otherDevice {
		t.Errorf("the other device disappeared from the device list: %v", device)
	}
}

// TestAPasswordChangeCanKeepTheOtherSessions covers the opt-out, which is what a user who merely
// mistyped their own password and wants to fix it needs: the change should not sign them out of
// the phone in the other room.
func TestAPasswordChangeCanKeepTheOtherSessions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID, token, _ := h.register(t, "pwkeep", "keep-first", "")
	otherToken := loginWith(t, h, userID, "keep-first").token

	keep := false
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/account/password",
		passwordChangeBody(userID, "keep-first", "keep-second", &keep), token), http.StatusOK)

	h.mustStatus(t, h.get("/_matrix/client/v3/account/whoami", otherToken), http.StatusOK)
	loginWith(t, h, userID, "keep-second")
}

// TestADeactivatedAccountCannotProveItself ends an account and asks what is left: the password
// door, the tokens it holds, and the challenge that guards the door.
func TestADeactivatedAccountCannotProveItself(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID, token, _ := h.register(t, "goodbye", "bye-now", "")

	// The flow is offered before the deed is done.
	challenge := h.mustStatus(t, h.post(t, "/_matrix/client/v3/account/deactivate", map[string]any{}, token),
		http.StatusUnauthorized).decoded(t)
	if !offersPasswordStage(challenge) {
		t.Errorf("the deactivation challenge offers no m.login.password flow: %v", challenge["flows"])
	}

	// The wrong password refuses it.
	wrong := h.post(t, "/_matrix/client/v3/account/deactivate",
		deactivateBody(userID, "not-the-password"), token)
	if wrong.status != http.StatusUnauthorized || wrong.errcode(t) != "M_FORBIDDEN" {
		t.Fatalf("deactivation with the wrong password: status %d %s, want 401 M_FORBIDDEN (body %s)",
			wrong.status, wrong.errcode(t), wrong.body)
	}

	done := h.mustStatus(t, h.post(t, "/_matrix/client/v3/account/deactivate",
		deactivateBody(userID, "bye-now"), token), http.StatusOK).decoded(t)
	if result, _ := done["id_server_unbind_result"].(string); result != "no-support" {
		t.Errorf("deactivation answered %v, want id_server_unbind_result no-support: %v", result, done)
	}

	// The password no longer opens anything, and what it says is that the account has ended
	// rather than that the password was wrong.
	res := h.post(t, "/_matrix/client/v3/login", map[string]any{
		"identifier": map[string]any{"type": "m.id.user", "user": userID},
		"type":       "m.login.password",
		"password":   "bye-now",
	}, "")
	if res.status != http.StatusForbidden {
		t.Fatalf("login after deactivation: status %d, want 403 (body %s)", res.status, res.body)
	}
	if code := res.errcode(t); code != "M_USER_DEACTIVATED" {
		t.Errorf("login after deactivation answered %s, want M_USER_DEACTIVATED (body %s)", code, res.body)
	}

	// And the token that did it is finished too.
	after := h.get("/_matrix/client/v3/account/whoami", token)
	if after.status != http.StatusUnauthorized {
		t.Errorf("the token of a deactivated account: status %d, want 401 (body %s)", after.status, after.body)
	}

	// The account is unusable rather than erased: this server keeps the row, because its
	// rooms, membership events and keys name it. What the specification promises is that it
	// cannot be used again, and that is what the checks above measured.
}

// loginSession is a second session obtained through /login.
type loginSession struct {
	token    string
	deviceID string
}

// loginWith opens a session with a password, failing the test if the door stays shut.
func loginWith(t *testing.T, h *harness, userID, password string) loginSession {
	t.Helper()
	res := h.mustStatus(t, h.post(t, "/_matrix/client/v3/login", map[string]any{
		"identifier": map[string]any{"type": "m.id.user", "user": userID},
		"type":       "m.login.password",
		"password":   password,
	}, ""), http.StatusOK).decoded(t)
	token, _ := res["access_token"].(string)
	device, _ := res["device_id"].(string)
	if token == "" || device == "" {
		t.Fatalf("login answered without a token or a device: %v", res)
	}
	return loginSession{token: token, deviceID: device}
}

// passwordChangeBody is the POST /account/password body: the credentials and the new password.
func passwordChangeBody(userID, current, next string, logoutDevices *bool) map[string]any {
	body := map[string]any{
		"auth":         passwordCredential(userID, current),
		"new_password": next,
	}
	if logoutDevices != nil {
		body["logout_devices"] = *logoutDevices
	}
	return body
}

// deactivateBody is the POST /account/deactivate body.
func deactivateBody(userID, current string) map[string]any {
	return map[string]any{"auth": passwordCredential(userID, current)}
}

// passwordCredential is an m.login.password credential as a client writes it.
func passwordCredential(userID, password string) map[string]any {
	return map[string]any{
		"type":       "m.login.password",
		"identifier": map[string]any{"type": "m.id.user", "user": userID},
		"password":   password,
	}
}

// offersPasswordStage reports whether a User-Interactive Auth challenge offers a flow whose
// stages include m.login.password.
func offersPasswordStage(challenge map[string]any) bool {
	flows, _ := challenge["flows"].([]any)
	for _, entry := range flows {
		flow, _ := entry.(map[string]any)
		stages, _ := flow["stages"].([]any)
		for _, stage := range stages {
			if name, _ := stage.(string); name == "m.login.password" {
				return true
			}
		}
	}
	return false
}
