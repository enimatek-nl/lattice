package integration

import (
	"net/http"
	"testing"
)

// A `knock_restricted` join rule mixes two rules the room may already be using: a join has to
// satisfy the room's allow list, and a knock is open to anybody who asks. Only the first is the room's
// to enforce at the door, so a knock on such a room is allowed and the join that follows it is what
// has to be authorised. Room version 10 is where the combined rule exists, so this is where the
// distinction is worth pinning down.

func TestAKnockRestrictedRoomTakesKnocksAndRestrictsJoins(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	aliceID, alice, _ := h.register(t, "host", "pw-host", "")
	_, bob, _ := h.register(t, "guest", "pw-guest", "")
	roomID := createRoom(t, h, alice, map[string]any{
		"preset": "private_chat", "room_version": "10", "name": "restricted knocking",
	})

	res := h.raw(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules/",
		[]byte(`{"join_rule":"knock_restricted","allow":[{"room_id":"!elsewhere:other.test","type":"m.room_membership"}]}`), alice)
	h.mustStatus(t, res, http.StatusOK)
	eventID, _ := res.decoded(t)["event_id"].(string)
	if eventID == "" {
		t.Fatalf("the join rule change answered without an event ID: %s", res.body)
	}

	// The room's current state, and what the member who made the change is told. A room whose join
	// rule its own moderator cannot read back is a room nobody can be expected to join correctly.
	read := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules", alice), http.StatusOK)
	if rule, _ := read.decoded(t)["join_rule"].(string); rule != "knock_restricted" {
		t.Errorf("the room's join rule reads back %q, want knock_restricted", rule)
	}
	body, _ := syncNow(t, h, alice, "")
	joined := roomSection(t, body, "join", roomID)
	if joined == nil {
		t.Fatalf("room %s is missing from alice's sync", roomID)
	}
	sawChangedRule := false
	for _, field := range []string{"timeline", "state"} {
		for _, event := range syncEvents(t, joined, field) {
			content, _ := event["content"].(map[string]any)
			if event["type"] == "m.room.join_rules" && content["join_rule"] == "knock_restricted" {
				sawChangedRule = true
			}
		}
	}
	if !sawChangedRule {
		t.Errorf("the knock_restricted join rule never appears in alice's sync of the room she set it in")
	}

	// A knock is asked, not granted: the room's allow list speaks about who may walk in, not about
	// who may ring the bell.
	knock := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/knock", map[string]any{"reason": "let me in"}, bob)
	if knock.status != http.StatusOK {
		t.Errorf("knocking on a knock_restricted room: status %d, want 200 (body %s)", knock.status, knock.body)
	}
	if got := currentMembership(t, h, roomID, bobOf(t, h, bob), bob); got != "knock" {
		t.Errorf("membership after the knock = %q, want knock", got)
	}
	// Walking in uninvited is the part the allow list decides, and the allow list names a room
	// nobody here is a member of.
	join := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/join", map[string]any{}, bob)
	if join.status != http.StatusForbidden {
		t.Errorf("joining a knock_restricted room without satisfying its allow list: status %d, want 403 (body %s)",
			join.status, join.body)
	}
	_ = aliceID
}

// bobOf asks the server which user a token was issued to.
func bobOf(t *testing.T, h *harness, token string) string {
	t.Helper()
	res := h.mustStatus(t, h.get("/_matrix/client/v3/account/whoami", token), http.StatusOK)
	user, _ := res.decoded(t)["user_id"].(string)
	if user == "" {
		t.Fatalf("whoami answered without a user ID: %s", res.body)
	}
	return user
}
