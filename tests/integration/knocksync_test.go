package integration

import (
	"net/http"
	"testing"
)

// A knock replaced by a later knock is still an event that happened. The room's current state keeps
// only the newest one — that is what "current state" means — but the timeline is the room's history,
// and the members reading the room have to be able to see that the first knock was made, why it was
// made, and that it was made again. A sync that carries only the surviving state event leaves the
// room reading a history in which the earlier knock never occurred, and a knock's reason is the part
// a moderator decides with.

// TestSupersededKnocksStayInTheTimeline knocks twice with different reasons and reads what an in-room
// user's first sync carries.
func TestSupersededKnocksStayInTheTimeline(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	aliceID, alice, _ := h.register(t, "host", "pw-host", "")
	bobID, bob, _ := h.register(t, "guest", "pw-guest", "")
	roomID := createRoom(t, h, alice, map[string]any{"preset": "private_chat", "room_version": "10"})
	h.mustStatus(t, h.raw(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules/",
		[]byte(`{"join_rule":"knock"}`), alice), http.StatusOK)
	// Alice created the room, so she is in it and can read it. Bob is not: knocking is how he asks to
	// be let in, and an invitation would make the knock refused for a different reason entirely.
	_ = aliceID

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/knock/"+roomID,
		map[string]any{"reason": "let me in"}, bob), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/knock",
		map[string]any{"reason": "please, again"}, bob), http.StatusOK)

	body, _ := syncNow(t, h, alice, "")
	joined := roomSection(t, body, "join", roomID)
	if joined == nil {
		t.Fatalf("room %s is missing from alice's sync: %v", roomID, body["rooms"])
	}
	var reasons []string
	for _, event := range syncEvents(t, joined, "timeline") {
		if event["type"] != "m.room.member" || event["state_key"] != bobID {
			continue
		}
		content, _ := event["content"].(map[string]any)
		if content["membership"] != "knock" {
			continue
		}
		reason, _ := content["reason"].(string)
		reasons = append(reasons, reason)
	}
	if len(reasons) != 2 || reasons[0] != "let me in" || reasons[1] != "please, again" {
		t.Errorf("knock events in the timeline = %v, want both knocks in the order they happened", reasons)
	}

	// The state section is the other half of the answer, and it is allowed to carry only the newest
	// member event: two current memberships for one user would be the room contradicting itself.
	stateCount := 0
	for _, event := range syncEvents(t, joined, "state") {
		if event["type"] == "m.room.member" && event["state_key"] == bobID {
			stateCount++
		}
	}
	if stateCount > 1 {
		t.Errorf("the state section carries %d member events for %s; current state holds one",
			stateCount, bobID)
	}
}

// syncEvents reads a room's timeline or state events out of a sync response.
func syncEvents(t *testing.T, room map[string]any, field string) []map[string]any {
	t.Helper()
	section, _ := room[field].(map[string]any)
	raw, _ := section["events"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if event, ok := entry.(map[string]any); ok {
			out = append(out, event)
		}
	}
	return out
}
