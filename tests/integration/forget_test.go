package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// TestAForgottenRoomStopsBeingShownToTheUserWhoForgotIt walks what `/forget` is for. The word is
// about one person's view: the room, its history and everyone else in it are untouched, and what
// stops is that user being told about it.
func TestAForgottenRoomStopsBeingShownToTheUserWhoForgotIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, _ := h.register(t, "forgetful", "pw-forgetful", "")
	_, bobToken, _ := h.register(t, "witness", "pw-witness", "")

	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": "@witness:hs1"}, aliceToken), http.StatusOK)
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	messageID := sendText(t, h, roomID, aliceToken, "before-leave", "written while she was here")

	// Still in the room: refused. Leaving is a step with its own endpoint, its own authorisation
	// and an event the room has to be told about; forget cannot do it secretly.
	res := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/forget", map[string]any{}, aliceToken)
	if res.status != http.StatusBadRequest {
		t.Fatalf("forgetting a room she is still in: status %d, want 400 (body %s)", res.status, res.body)
	}
	if code := errcodeOf(t, res); code != "M_UNKNOWN" {
		t.Errorf("forgetting a room she is still in answered errcode %q, want M_UNKNOWN (body %s)", code, res.body)
	}

	// A token taken before the leaving, so the leaving itself is still ahead of it.
	_, beforeLeave := syncNow(t, h, aliceToken, "")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, aliceToken), http.StatusOK)
	if res := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/forget", map[string]any{}, aliceToken), http.StatusOK); len(res.decoded(t)) != 0 {
		t.Errorf("the forget endpoint answered %s, want {}", res.body)
	}

	// A former member may normally read the room they left. Having asked not to be told about it,
	// they no longer can — until they come back.
	if res := h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b", aliceToken); res.status != http.StatusForbidden {
		t.Errorf("paging a forgotten room: status %d (%s), want 403", res.status, res.body)
	}
	// Someone who is still in the room reads it normally: the forget is one person's request.
	if res := h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b", bobToken); res.status != http.StatusOK {
		t.Errorf("the remaining member's /messages: status %d (%s), want 200", res.status, res.body)
	}

	// A first sync asks where the user is now, and the answer for a forgotten room is nowhere —
	// including in `leave`, and including with a filter that asks for left rooms.
	includeLeave := `{"room":{"include_leave":true}}`
	first, _ := syncNow(t, h, aliceToken, "&filter="+url.QueryEscape(includeLeave))
	for _, section := range []string{"join", "invite", "leave", "archived"} {
		if room := roomSection(t, first, section, roomID); room != nil {
			t.Errorf("a forgotten room appears in a first sync under rooms.%s: %v", section, room)
		}
	}

	// A sync from before the leaving still reports it. The client has not been told that alice
	// left, and dropping that now would lose the leaving from the client's record for good: the
	// forget hides the room going forward, it does not rewrite a window the client has not read.
	incremental, _ := syncNow(t, h, aliceToken, "&since="+beforeLeave+"&filter="+url.QueryEscape(includeLeave))
	if room := roomSection(t, incremental, "leave", roomID); room == nil {
		t.Errorf("the leaving is missing from the incremental sync after the forget: %v", incremental["rooms"])
	}

	// Coming back undoes the request. Re-invited and rejoined, alice reads the history she had
	// forgotten, including the message she left behind.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": alice}, bobToken), http.StatusOK)
	h.mustStatus(t, join(t, h, roomID, aliceToken, map[string]any{}), http.StatusOK)
	page := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=50", aliceToken), http.StatusOK)
	if !chunkHasEventID(page, messageID) {
		t.Errorf("after rejoining, the forgotten message %s is not in %s", messageID, page.body)
	}
	afterRejoin, _ := syncNow(t, h, aliceToken, "")
	if room := roomSection(t, afterRejoin, "join", roomID); room == nil {
		t.Errorf("the room is not back in %s's sync after rejoining: %v", "alice", afterRejoin["rooms"])
	}
}

// TestAForgottenRoomAfterBeingKicked covers the two cases where the requester never had to ask to
// leave: they were thrown out, or they refused an invitation. Either way the room is out of their
// membership and the forget is a request about their own view, so it is accepted.
func TestAForgottenRoomAfterBeingKicked(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "kicker", "pw-kicker", "")
	bob, bobToken, _ := h.register(t, "kicked", "pw-kicked", "")
	carol, carolToken, _ := h.register(t, "refuser", "pw-refuser", "")

	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/kick",
		map[string]any{"user_id": bob}, aliceToken), http.StatusOK)
	if res := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/forget", map[string]any{}, bobToken), http.StatusOK); len(res.decoded(t)) != 0 {
		t.Errorf("a kicked member forgetting the room: %s, want {}", res.body)
	}

	inviteRoom := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+inviteRoom+"/invite",
		map[string]any{"user_id": carol}, aliceToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+inviteRoom+"/leave", map[string]any{}, carolToken), http.StatusOK)
	if res := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+inviteRoom+"/forget", map[string]any{}, carolToken), http.StatusOK); len(res.decoded(t)) != 0 {
		t.Errorf("forgetting a room whose invitation was refused: %s, want {}", res.body)
	}
	// The invitation is gone from carol's sync, which is what the forget was for.
	syncBody, _ := syncNow(t, h, carolToken, "")
	rooms, _ := syncBody["rooms"].(map[string]any)
	for section, value := range rooms {
		sectionMap, _ := value.(map[string]any)
		if _, seen := sectionMap[inviteRoom]; seen {
			t.Errorf("the refused invitation is still in carol's first sync under %s", section)
		}
	}
}

// chunkHasEventID answers whether a /messages page holds an event.
func chunkHasEventID(res response, eventID string) bool {
	var page struct {
		Chunk []struct {
			EventID string `json:"event_id"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(res.body, &page); err != nil {
		return false
	}
	for _, event := range page.Chunk {
		if event.EventID == eventID {
			return true
		}
	}
	return false
}
