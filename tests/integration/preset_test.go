package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRoomPresetsFollowTheSpecificationTable pins the state a room starts with for each of the
// three named presets, plus the rule that a client which asks for a public room without naming a
// preset gets the public preset.
//
// The specification gives this as a table of join rule, history visibility and guest access, and
// the row that is easy to get wrong is the public one: `public_chat` makes a room joinable without
// an invitation and keeps its history to members, with guests kept out. A server that starts a
// public room `world_readable` has published that room's history to anybody who asks, on the
// strength of a join rule that says nothing of the kind — and a server that creates an invite-only
// room for a client that said `visibility: public` has published a listing for a room the reader
// cannot enter.

func TestRoomPresetsFollowTheSpecificationTable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "presetter", "pw-presetter", "")

	for _, tc := range []struct {
		preset    string
		joinRule  string
		history   string
		guestRead string
	}{
		{"private_chat", "invite", "shared", "can_join"},
		{"trusted_private_chat", "invite", "shared", "can_join"},
		{"public_chat", "public", "shared", "forbidden"},
	} {
		roomID := createRoom(t, h, token, map[string]any{"preset": tc.preset})
		state := presetStateOf(t, h, roomID, token)
		wantStateContent(t, state, "m.room.join_rules", "join_rule", tc.joinRule, tc.preset)
		wantStateContent(t, state, "m.room.history_visibility", "history_visibility", tc.history, tc.preset)
		wantGuestAccess(t, state, tc.guestRead, tc.preset)
	}

	// No preset named, but the room asked for in public: the specification reads the visibility as
	// choosing the preset, so this is the public row again.
	roomID := createRoom(t, h, token, map[string]any{"visibility": "public"})
	state := presetStateOf(t, h, roomID, token)
	wantStateContent(t, state, "m.room.join_rules", "join_rule", "public", "visibility=public with no preset")
	wantStateContent(t, state, "m.room.history_visibility", "history_visibility", "shared", "visibility=public with no preset")
	wantGuestAccess(t, state, "forbidden", "visibility=public with no preset")
}

// presetStateOf reads a room's current state as events, not as the per-key content the state
// endpoint answers with, because the preset has to be checked by event type.
func presetStateOf(t *testing.T, h *harness, roomID, token string) []map[string]any {
	t.Helper()
	res := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state", token), http.StatusOK)
	var events []map[string]any
	if err := json.Unmarshal(res.body, &events); err != nil {
		t.Fatalf("a room's state is not a JSON array: %v (%s)", err, res.body)
	}
	return events
}

// wantStateContent asserts what one state event in the room says.
// wantGuestAccess asserts what the room says about guests, which is not the same as asserting that a
// particular event exists. The event's schema makes absence mean `forbidden`, and this server writes
// the event only where it grants something — so a room whose preset keeps guests out is required to
// behave as `forbidden`, by event or by silence, and either is correct.
func wantGuestAccess(t *testing.T, state []map[string]any, want, why string) {
	t.Helper()
	access := "forbidden"
	for _, event := range state {
		if event["type"] != "m.room.guest_access" || event["state_key"] != "" {
			continue
		}
		content, _ := event["content"].(map[string]any)
		access, _ = content["guest_access"].(string)
	}
	if access != want {
		t.Errorf("guest access of the room created for %s = %q, want %q (absence of m.room.guest_access means %q)",
			why, access, want, "forbidden")
	}
}

func wantStateContent(t *testing.T, state []map[string]any, eventType, key, want, why string) {
	t.Helper()
	for _, event := range state {
		if event["type"] != eventType || event["state_key"] != "" {
			continue
		}
		content, _ := event["content"].(map[string]any)
		if got, _ := content[key].(string); got != want {
			t.Errorf("%s in the room created for %s: %s[%s] = %q, want %q",
				eventType, why, eventType, key, got, want)
		}
		return
	}
	t.Errorf("the room created for %s has no %s event, which its preset is required to create", why, eventType)
}
