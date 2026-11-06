package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The reader's membership of the room as of each event is part of the client event format, not a
// peculiarity of `/sync`: the specification defines `unsigned.membership` on the client event
// itself, as "the room membership of the user making the request, at the time of the event",
// including changes the event itself caused, and `leave` where the user had no member event yet.
// So it has to hold on the paths that hand over state and context too, where the temptation to
// read the answer off the room's *current* state is strongest — and wrong, because the state
// events on `/state` are mostly older than the moment the reader arrived.

func TestServedStateAndHistoryCarryTheReadersMembership(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "nadia", "pw-nadia", "")
	bob, bobToken, _ := h.register(t, "ostap", "pw-ostap", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{
		"preset": "private_chat",
		"name":   "Before and after",
		"invite": []string{bob},
	})

	// A message and a state change written while Bob is still only invited.
	preJoinMsg := sendText(t, h, roomID, aliceToken, "prejoin-txn", "before bob")

	// Bob joins. This is the boundary every assertion below is made against.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)

	postJoinMsg := sendText(t, h, roomID, aliceToken, "postjoin-txn", "after bob")
	topicAfterJoin := "/_matrix/client/v3/rooms/" + roomID + "/state/m.room.topic"
	h.mustStatus(t, h.put(t, topicAfterJoin, map[string]any{"topic": "written after bob"}, aliceToken), http.StatusOK)

	// `/state`: the room's current state, which is a mixture of moments. `/state` is not a
	// timeline, but every event in it was written at some point, and the reader's membership is
	// asked for *then* — so the room's creation and its power levels, both older than Bob's
	// invitation, say `leave`, while the topic written after he joined says `join`. A server that
	// answered this from the room's current state would call them all `join`, which is what makes
	// the mixture worth asserting.
	want := map[string]string{
		"m.room.create":       "leave",
		"m.room.power_levels": "leave",
		"m.room.topic":        "join",
	}
	seen := map[string]bool{}
	for _, event := range stateEvents(t, h, bobToken, roomID) {
		typ, _ := event["type"].(string)
		wantFor, checked := want[typ]
		if !checked {
			continue
		}
		seen[typ] = true
		if got := membershipInUnsigned(t, event); got != wantFor {
			t.Errorf("%s, served to Bob, says membership %q, want %s", typ, got, wantFor)
		}
	}
	for typ := range want {
		if !seen[typ] {
			t.Errorf("the room's current state carries no %s to check", typ)
		}
	}

	// `/messages`: a page of history, where the same room is read backwards and each event has to
	// speak for its own moment. Bob paginating back from now sees his join, then events from
	// before he was anyone in the room at all.
	res := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50&dir=b", bobToken), http.StatusOK)
	var page struct {
		Chunk []map[string]any `json:"chunk"`
	}
	if err := json.Unmarshal(res.body, &page); err != nil {
		t.Fatalf("decoding the messages page: %v", err)
	}
	// The page comes back newest first, so it is walked oldest first here: the question "was Bob
	// in the room when this was written" only has an answer in the direction the room happened.
	// His own membership walks leave -> invite -> join, and each step is dated by the member event
	// that caused it, so an event cannot be judged by the room's state today.
	reversed := make([]map[string]any, 0, len(page.Chunk))
	for i := len(page.Chunk) - 1; i >= 0; i-- {
		reversed = append(reversed, page.Chunk[i])
	}
	expect := "leave"
	checked := 0
	sawIDs := map[string]bool{}
	for _, event := range reversed {
		typ, _ := event["type"].(string)
		if id, _ := event["event_id"].(string); id != "" {
			sawIDs[id] = true
		}
		if typ == "m.room.member" {
			if key, _ := event["state_key"].(string); key == bob {
				// The event counts its own change: Bob's invitation says `invite` about Bob.
				content, _ := event["content"].(map[string]any)
				if next, _ := content["membership"].(string); next == "invite" || next == "join" {
					expect = next
				}
			}
		}
		if got := membershipInUnsigned(t, event); got != expect {
			t.Errorf("%s in Bob's history page says membership %q, want %s", typ, got, expect)
		}
		checked++
	}
	if checked < 4 {
		t.Fatalf("Bob's history page holds only %d events, want a page worth checking", checked)
	}
	for _, eventID := range []string{preJoinMsg, postJoinMsg} {
		if !sawIDs[eventID] {
			t.Errorf("Bob's history page does not contain the message %s", eventID)
		}
	}

}

// sendText sends a message and returns its event ID.
func sendText(t *testing.T, h *harness, roomID, token, txn, body string) string {
	t.Helper()
	path := "/_matrix/client/v3/rooms/" + roomID + "/send/m.room.message/" + txn
	res := h.mustStatus(t, h.put(t, path, map[string]any{"msgtype": "m.text", "body": body}, token), http.StatusOK)
	var out struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(res.body, &out); err != nil || out.EventID == "" {
		t.Fatalf("sending %q: no event id in %s", body, res.body)
	}
	return out.EventID
}

// stateEvents reads the room's current state over `/state` as one particular reader.
func stateEvents(t *testing.T, h *harness, token, roomID string) []map[string]any {
	t.Helper()
	res := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state", token), http.StatusOK)
	var all []map[string]any
	if err := json.Unmarshal(res.body, &all); err != nil {
		t.Fatalf("decoding the state of %s: %v (%s)", roomID, err, res.body)
	}
	return all
}

func membershipInUnsigned(t *testing.T, event map[string]any) string {
	t.Helper()
	unsigned := unsignedOf(t, event)
	membership, _ := unsigned["membership"].(string)
	return membership
}
