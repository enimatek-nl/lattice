package integration

import (
	"net/http"
	"testing"
)

// TestPresenceEndpoints checks what a user is asked and allowed to do about their own
// presence, and what another user may read.
func TestPresenceEndpoints(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, _ := h.register(t, "presencealice", "pw-presencealice", "")

	own := h.mustStatus(t, h.get("/_matrix/client/v3/presence/"+alice+"/status", aliceToken), http.StatusOK).decoded(t)
	if own["presence"] != "online" {
		t.Errorf("a fresh account's presence = %v, want online: %v", own["presence"], own)
	}
	if own["currently_active"] != true {
		t.Errorf("a fresh account is not reported as active: %v", own)
	}
	if _, ok := own["last_active_ago"]; !ok {
		t.Errorf("the response has no last_active_ago: %v", own)
	}
	if _, present := own["status_msg"]; present {
		t.Errorf("a user who set no status message is given one: %v", own)
	}

	h.mustStatus(t, h.put(t, "/_matrix/client/v3/presence/"+alice+"/status",
		map[string]any{"presence": "online", "status_msg": "Testing something"}, aliceToken), http.StatusOK)
	after := h.mustStatus(t, h.get("/_matrix/client/v3/presence/"+alice+"/status", aliceToken), http.StatusOK).decoded(t)
	if after["status_msg"] != "Testing something" {
		t.Errorf("the status message did not come back: %v", after)
	}
	if after["presence"] != "online" {
		t.Errorf("presence = %v", after["presence"])
	}

	// Setting somebody else's presence is refused, and so is a value this server has no
	// meaning for.
	_, bobToken, _ := h.register(t, "presencebob", "pw-presencebob", "")
	forbidden := h.mustStatus(t, h.put(t, "/_matrix/client/v3/presence/"+alice+"/status",
		map[string]any{"presence": "offline"}, bobToken), http.StatusForbidden).decoded(t)
	if forbidden["errcode"] != "M_FORBIDDEN" {
		t.Errorf("setting another user's presence: %v", forbidden)
	}
	bad := h.mustStatus(t, h.put(t, "/_matrix/client/v3/presence/"+alice+"/status",
		map[string]any{"presence": "sleeping"}, aliceToken), http.StatusBadRequest).decoded(t)
	if bad["errcode"] != "M_INVALID_PARAM" {
		t.Errorf("an unknown presence value: %v", bad)
	}
	if got := h.mustStatus(t, h.get("/_matrix/client/v3/presence/"+alice+"/status", aliceToken), http.StatusOK).decoded(t); got["presence"] != "online" {
		t.Errorf("a refused change altered the stored presence: %v", got)
	}

	// A user on another server is not this server's to report.
	if res := h.get("/_matrix/client/v3/presence/@someone:elsewhere/status", aliceToken); res.status != http.StatusNotFound {
		t.Errorf("presence of a remote user: status %d, want 404 (body %s)", res.status, res.body)
	}
	// A malformed user ID is the client's mistake, not an unknown user.
	if res := h.get("/_matrix/client/v3/presence/not-a-user/status", aliceToken); res.status != http.StatusBadRequest {
		t.Errorf("a malformed user ID: status %d, want 400 (body %s)", res.status, res.body)
	}
}

// TestPresenceReachesRoomMembers checks the part that makes presence worth having: a
// change reaches the other members of the rooms they share, through `/sync`.
func TestPresenceReachesRoomMembers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, _ := h.register(t, "syncalice", "pw-syncalice", "")
	bob, bobToken, _ := h.register(t, "syncbob", "pw-syncbob", "")

	created := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "presence room"}, aliceToken), http.StatusOK).decoded(t)
	roomID, _ := created["room_id"].(string)

	// Both users have a token from before the join, so each of them can be shown the
	// other's arrival.
	_, aliceSince := syncNow(t, h, aliceToken, "")
	_, bobSince := syncNow(t, h, bobToken, "")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)

	// Joining a room makes each member's presence the other's to see.
	body, next := syncNow(t, h, bobToken, "&since="+bobSince)
	if !hasPresenceFrom(t, body, alice) {
		t.Fatalf("bob's sync after joining carries no presence for alice: %v", body["presence"])
	}
	if fromAlice, _ := syncNow(t, h, aliceToken, "&since="+aliceSince); !hasPresenceFrom(t, fromAlice, bob) {
		t.Errorf("alice's sync after bob joined carries no presence for bob: %v", fromAlice["presence"])
	}
	// And it is not repeated once it has been delivered.
	if again, _ := syncNow(t, h, bobToken, "&since="+next); hasPresenceFrom(t, again, alice) {
		t.Errorf("the same presence change was delivered twice: %v", again["presence"])
	}

	// A change made through a sync parameter reaches the other member.
	if res := h.get("/_matrix/client/v3/sync?timeout=0&set_presence=unavailable", aliceToken); res.status != http.StatusOK {
		t.Fatalf("alice's sync with set_presence: status %d (body %s)", res.status, res.body)
	}
	changed, _ := syncNow(t, h, bobToken, "&since="+next)
	if !hasPresenceWith(t, changed, alice, "unavailable") {
		t.Fatalf("alice's new presence did not reach bob: %v", changed["presence"])
	}

	// So does one made through the endpoint.
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/presence/"+alice+"/status",
		map[string]any{"presence": "online", "status_msg": "back"}, aliceToken), http.StatusOK)
	body, _ = syncNow(t, h, bobToken, "&since="+next)
	if !hasPresenceWith(t, body, alice, "online") {
		t.Fatalf("a presence set by the endpoint did not reach bob: %v", body["presence"])
	}
	if event := presenceFrom(t, body, alice); event != nil {
		content, _ := event["content"].(map[string]any)
		if content["status_msg"] != "back" {
			t.Errorf("the status message was dropped: %v", content)
		}
	}

	// The endpoint and the sync agree with each other.
	status := h.mustStatus(t, h.get("/_matrix/client/v3/presence/"+alice+"/status", bobToken), http.StatusOK).decoded(t)
	if status["presence"] != "online" || status["status_msg"] != "back" {
		t.Errorf("bob reads alice's presence as %v", status)
	}
}

// presenceFrom finds the presence event about a user in a sync response.
func presenceFrom(t *testing.T, body map[string]any, userID string) map[string]any {
	t.Helper()
	section, _ := body["presence"].(map[string]any)
	list, _ := section["events"].([]any)
	for _, item := range list {
		event, _ := item.(map[string]any)
		if event["type"] != "m.presence" {
			t.Errorf("a presence section event has type %v", event["type"])
			continue
		}
		if _, ok := event["content"]; !ok {
			t.Errorf("a presence event has no content: %v", event)
			continue
		}
		if event["sender"] == userID {
			return event
		}
	}
	return nil
}

// hasPresenceFrom reports whether a sync response carries presence for a user.
func hasPresenceFrom(t *testing.T, body map[string]any, userID string) bool {
	t.Helper()
	return presenceFrom(t, body, userID) != nil
}

// hasPresenceWith reports whether it carries that user with a given presence value.
func hasPresenceWith(t *testing.T, body map[string]any, userID, state string) bool {
	t.Helper()
	event := presenceFrom(t, body, userID)
	if event == nil {
		return false
	}
	content, _ := event["content"].(map[string]any)
	return content["presence"] == state
}
