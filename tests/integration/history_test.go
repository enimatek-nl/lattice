package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// TestRoomMessagesPagesInBothDirections covers the timeline paging endpoint: the direction
// the page is ordered in, the tokens that carry a client to the next page, and the point
// at which the server stops promising more.
func TestRoomMessagesPagesInBothDirections(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "history1", "pw-history1", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	// The token is taken before the messages exist, so a forward page from it holds the
	// messages and a backward page does not.
	_, since := syncNow(t, h, aliceToken, "")
	for _, body := range []string{"one", "two", "three"} {
		h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-"+body,
			`{"msgtype":"m.text","body":"`+body+`"}`, aliceToken), http.StatusOK)
	}

	// Backwards from now: the newest events first, and a token to keep going with.
	back := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=2", aliceToken), http.StatusOK).decoded(t)
	bodies := messageBodies(t, back["chunk"])
	if len(bodies) != 2 || bodies[0] != "three" || bodies[1] != "two" {
		t.Errorf("dir=b chunk = %v, want newest first: [three two]", bodies)
	}
	end, _ := back["end"].(string)
	if end == "" {
		t.Fatalf("a truncated page must carry an end token to continue from: %v", back)
	}
	if start, _ := back["start"].(string); start == "" {
		t.Errorf("the page must say where it started: %v", back)
	}
	next := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=2&from="+end, aliceToken), http.StatusOK).decoded(t)
	if got := messageBodies(t, next["chunk"]); len(got) == 0 || got[len(got)-1] != "one" {
		t.Errorf("paging back from %q = %v, want the page to reach the first message", end, got)
	}

	// Walking back to the beginning must stop promising more events: a client paginates
	// until `end` is gone.
	for cursor := end; cursor != ""; {
		page := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=1&from="+cursor, aliceToken), http.StatusOK).decoded(t)
		cursor, _ = page["end"].(string)
		if chunk, _ := page["chunk"].([]any); len(chunk) == 0 {
			if _, ok := page["end"]; ok {
				t.Fatalf("an empty page must not promise further events: %v", page)
			}
		}
	}

	// Forwards from a sync token: chronological, and only what came after it.
	forward := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&from="+since, aliceToken), http.StatusOK).decoded(t)
	forwardBodies := messageBodies(t, forward["chunk"])
	if len(forwardBodies) != 3 || forwardBodies[0] != "one" || forwardBodies[2] != "three" {
		t.Errorf("dir=f chunk = %v, want the three messages in order", forwardBodies)
	}
	// A forward page that ran out of events does not promise more.
	if _, ok := forward["end"]; ok {
		t.Errorf("a page that reached the newest event must not return an end token: %v", forward)
	}
	// `to` stops a page where the client asked, and the token it stops on is the page's
	// own end.
	first := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&from="+since+"&limit=1", aliceToken), http.StatusOK).decoded(t)
	firstEnd, _ := first["end"].(string)
	if got := messageBodies(t, first["chunk"]); len(got) != 1 || got[0] != "one" || firstEnd == "" {
		t.Errorf("dir=f limit=1 = %v (end %q), want the first message and a token to continue from", messageBodies(t, first["chunk"]), firstEnd)
	}
	limited := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&from="+since+"&to="+firstEnd, aliceToken), http.StatusOK).decoded(t)
	if got := messageBodies(t, limited["chunk"]); len(got) != 1 || got[0] != "one" {
		t.Errorf("dir=f with to=%q = %v, want only the first message", firstEnd, got)
	}
	if _, ok := limited["end"]; ok {
		t.Errorf("a page that stopped at the requested `to` must not promise more: %v", limited)
	}

	// A room this server has never heard of is not answerable, and saying 404 would
	// report which room identifiers exist.
	if res := h.get("/_matrix/client/v3/rooms/!nosuchroom:hs1/messages?dir=b", aliceToken); res.status != http.StatusForbidden {
		t.Errorf("messages for an unknown room: status %d, want 403 (body %s)", res.status, res.body)
	}
	// An unreadable direction is refused rather than guessed at.
	if res := h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=sideways", aliceToken); res.status != http.StatusBadRequest {
		t.Errorf("bad dir: status %d, want 400 (body %s)", res.status, res.body)
	}
	// A token this server did not issue is refused rather than read as the beginning of
	// the room, which is what `/sync` does with one too.
	res := h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&from=42", aliceToken)
	if res.status != http.StatusUnauthorized {
		t.Errorf("foreign token: status %d, want 401 (body %s)", res.status, res.body)
	}
	if code := res.errcode(t); code != "M_INVALID_TOKEN" {
		t.Errorf("foreign token: errcode %q, want M_INVALID_TOKEN", code)
	}
}

// TestRoomMessagesRespectLeaving checks that paging a room ends where the requester's
// entitlement does: a departed member can read back through what they were part of, but
// not forward into what was written after they went.
func TestRoomMessagesRespectLeaving(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "paging1", "pw-paging1", "")
	_, bobToken, _ := h.register(t, "paging2", "pw-paging2", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{
		"initial_state": []map[string]any{
			{"type": "m.room.history_visibility", "state_key": "",
				"content": map[string]any{"history_visibility": "joined"}},
		},
	})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": "@paging2:hs1"}, aliceToken), http.StatusOK)
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "before-a",
		`{"msgtype":"m.text","body":"before-a"}`, aliceToken), http.StatusOK)
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "before-b",
		`{"msgtype":"m.text","body":"before-b"}`, aliceToken), http.StatusOK)
	// The token is bob's view of the room while he is still in it: everything in the room
	// up to here is his to read either way.
	_, from := syncNow(t, h, bobToken, "")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "after-1",
		`{"msgtype":"m.text","body":"after-1"}`, aliceToken), http.StatusOK)

	back := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&from="+from+"&limit=3", bobToken), http.StatusOK).decoded(t)
	bodies := messageBodies(t, back["chunk"])
	if len(bodies) == 0 {
		t.Fatalf("dir=b for a departed member = %v, want the messages he was party to", back["chunk"])
	}
	for _, body := range bodies {
		if body == "after-1" {
			t.Errorf("a departed member was shown a message sent after he left: %v", bodies)
		}
	}
	forward := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&from="+from, bobToken), http.StatusOK).decoded(t)
	if bodies := messageBodies(t, forward["chunk"]); len(bodies) != 0 {
		t.Errorf("dir=f for a departed member = %v, want nothing after leaving", bodies)
	}
	if _, ok := forward["end"]; ok {
		t.Errorf("a departed member with nothing further to read must not be given a token to chase: %v", forward)
	}
	// A member who stayed sees what came after, and is given a token to keep going with.
	keep := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&from="+from, aliceToken), http.StatusOK).decoded(t)
	if bodies := messageBodies(t, keep["chunk"]); len(bodies) != 1 || bodies[0] != "after-1" {
		t.Errorf("dir=f for a continuing member = %v, want the message sent after bob left", bodies)
	}
}

// TestRoomMessagesLazyMembers asks for the member events of the senders in a page, which
// is how a client renders senders without downloading the room's whole state per page.
func TestRoomMessagesLazyMembers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "lazyp1", "pw-lazyp1", "")
	bob, bobToken, _ := h.register(t, "lazyp2", "pw-lazyp2", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	_, from := syncNow(t, h, aliceToken, "")
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "lazymsg",
		`{"msgtype":"m.text","body":"from bob"}`, bobToken), http.StatusOK)

	url := "/_matrix/client/v3/rooms/" + roomID + "/messages?dir=f&from=" + from +
		"&filter=" + url.QueryEscape(`{"lazy_load_members":true}`)
	page := h.mustStatus(t, h.get(url, aliceToken), http.StatusOK).decoded(t)
	if bodies := messageBodies(t, page["chunk"]); len(bodies) != 1 {
		t.Fatalf("chunk = %v, want bob's message", bodies)
	}
	state, _ := page["state"].([]any)
	if len(state) != 1 {
		t.Fatalf("lazy state = %v, want the one member event for the sender", page["state"])
	}
	event := state[0].(map[string]any)
	if event["type"] != "m.room.member" || event["state_key"] != bob {
		t.Errorf("lazy state = %s/%s, want the member event for %s", event["type"], event["state_key"], bob)
	}
	// Without the filter the room's state is returned instead.
	full := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&from="+from, aliceToken), http.StatusOK).decoded(t)
	fullState, _ := full["state"].([]any)
	if len(fullState) < 2 {
		t.Errorf("the state of a page without lazy loading = %v, want the room's state", full["state"])
	}
}

// messageBodies collects the message bodies of a chunk, in the order the page returned
// them, which is what the ordering assertions are about.
func messageBodies(t *testing.T, chunk any) []string {
	t.Helper()
	raw, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("re-encoding the chunk: %v", err)
	}
	var events []struct {
		Type    string `json:"type"`
		Content struct {
			Body string `json:"body"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("decoding the chunk: %v", err)
	}
	var bodies []string
	for _, event := range events {
		if event.Type == "m.room.message" {
			bodies = append(bodies, event.Content.Body)
		}
	}
	return bodies
}
