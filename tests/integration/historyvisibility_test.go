package integration

import (
	"net/http"
	"net/url"
	"testing"
)

// TestHistoryVisibilityIsJudgedAtTheMomentEachEventWasWritten asks the question the
// `joined` and `invited` settings exist for, and asks it of a room that changes its mind
// partway through. The answer is not a property of the room today: it is the room's
// setting when each event was written, read against who was in the room then. A server
// that reads today's setting gets this wrong in both directions — it hides history the
// reader was entitled to when it was written, and it hands over history that was fenced
// off at the time.
func TestHistoryVisibilityIsJudgedAtTheMomentEachEventWasWritten(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "visowner", "pw-visowner", "")
	bob, bobToken, _ := h.register(t, "vislate", "vislate", "")
	_, carolToken, _ := h.register(t, "visoutsider", "pw-visoutsider", "")

	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	// Three messages, and the room's setting changes between the first and the second. Each is
	// judged by the setting the room had when that message was written, which is the only
	// reading that does not contradict itself: `shared` reaches backwards to whoever is a
	// member now, and `joined` reaches only as far back as the reader's own joining.
	shared := sendText(t, h, roomID, aliceToken, "txn-shared", "written while the room was shared")
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.history_visibility/",
		map[string]any{"history_visibility": "joined"}, aliceToken), http.StatusOK)
	fenced := sendText(t, h, roomID, aliceToken, "txn-fenced", "written after the room narrowed, before the reader arrived")
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	after := sendText(t, h, roomID, aliceToken, "txn-after", "written after the reader joined")

	// The middle message is the fence: written while the room said `joined`, at a moment when
	// bob was nobody here. The first message is not, even though the room says `joined` now —
	// it was written when the room said otherwise, and `shared` does not un-happen.
	h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+after, bobToken), http.StatusOK)
	h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+shared, bobToken), http.StatusOK)
	hidden := h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+fenced, bobToken)
	if hidden.status != http.StatusNotFound {
		t.Errorf("an event written while the room was `joined`, before the reader joined, answered %d (%s), want 404",
			hidden.status, hidden.body)
	}
	if code := errcodeOf(t, hidden); code != "M_NOT_FOUND" {
		t.Errorf(`/event for an event the reader may not see answered %s, want M_NOT_FOUND: this door's `+
			"answer is \"not here\" rather than \"not yours\", because the alternative confirms the identifier exists", code)
	}

	// The same cut through a page of history: the member reads the room's older `shared`
	// stretch and everything since their own joining, and nothing from the fenced stretch in
	// between.
	page := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=50", bobToken),
		http.StatusOK).decoded(t)
	if pageHasBody(t, page["chunk"], "written after the room narrowed, before the reader arrived") {
		t.Errorf("/messages handed a member of a `joined` room an event from before their join: %v",
			pageBodies(t, page["chunk"]))
	}
	for _, want := range []string{"written while the room was shared", "written after the reader joined"} {
		if !pageHasBody(t, page["chunk"], want) {
			t.Errorf("/messages lost %q, which this reader is entitled to: %v", want, pageBodies(t, page["chunk"]))
		}
	}

	// A sync says the same thing as /messages. A room whose /messages honours the rule while
	// its sync hands over the older events is one room with two answers.
	body, _ := syncNow(t, h, bobToken, "")
	joined := roomSection(t, body, "join", roomID)
	timeline, _ := joined["timeline"].(map[string]any)
	events, _ := timeline["events"].([]any)
	for _, entry := range events {
		event, _ := entry.(map[string]any)
		content, _ := event["content"].(map[string]any)
		if got, _ := content["body"].(string); got == "written after the room narrowed, before the reader arrived" {
			t.Errorf("a sync timeline gave a member of a `joined` room an event from before their join: %v", event["event_id"])
		}
	}

	// Someone who was never in the room at all: the event door answers the same as for an
	// event that does not exist, while the room's history page keeps saying what they would
	// need to be. Those two doors address different things — one an event identifier, the
	// other the room — and this is the difference the specification draws between them.
	notMine := h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+after, carolToken)
	if notMine.status != http.StatusNotFound {
		t.Errorf("a non-member fetching an event answered %d (%s), want 404", notMine.status, notMine.body)
	}
	refused := h.get("/_matrix/client/v3/rooms/"+roomID+"/messages", carolToken)
	if refused.status != http.StatusForbidden {
		t.Errorf("a non-member reading the room's history answered %d, want 403", refused.status)
	}

	// What the room says about a member is that member's, whatever the setting. bob's own join
	// is in his page even though the room said `joined` when it was written: at that moment the
	// room was saying that he was here.
	seen := false
	for _, event := range toEvents(page["chunk"]) {
		if event["type"] == "m.room.member" && event["state_key"] == bob {
			seen = true
		}
	}
	if !seen {
		t.Errorf("the room hid the member's own join from them: %v", pageBodies(t, page["chunk"]))
	}
}

// TestAWorldReadableRoomIsReadableBySomeoneWhoNeverJoined checks the other end of the
// setting: a room that opens its history to the world is readable by a user with no
// membership in it at all, and the door still refuses them the room's membership list.
func TestAWorldReadableRoomIsReadableBySomeoneWhoNeverJoined(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "opener", "pw-opener", "")
	_, strangerToken, _ := h.register(t, "stranger", "pw-stranger", "")

	roomID := createRoom(t, h, aliceToken, map[string]any{
		"preset": "public_chat",
		"initial_state": []map[string]any{{
			"type": "m.room.history_visibility", "state_key": "",
			"content": map[string]any{"history_visibility": "world_readable"},
		}},
	})
	eventID := sendText(t, h, roomID, aliceToken, "txn-open", "open to the world")
	h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+eventID, strangerToken), http.StatusOK)
	page := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=50", strangerToken),
		http.StatusOK).decoded(t)
	if !pageHasBody(t, page["chunk"], "open to the world") {
		t.Errorf("a world_readable room showed a non-member no history: %v", pageBodies(t, page["chunk"]))
	}
}

// pageHasBody reports whether a /messages chunk holds an event with this body.
func pageHasBody(t *testing.T, chunk any, want string) bool {
	t.Helper()
	for _, event := range toEvents(chunk) {
		content, _ := event["content"].(map[string]any)
		if body, _ := content["body"].(string); body == want {
			return true
		}
	}
	return false
}

// toEvents turns a /messages chunk into event maps.
func toEvents(chunk any) []map[string]any {
	entries, _ := chunk.([]any)
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if event, ok := entry.(map[string]any); ok {
			out = append(out, event)
		}
	}
	return out
}

// TestALeftMemberIsShownTheStateOfASharedRoom asks the left-room question the same way. A room
// that shares its history with its members owes a member who left the room's state as it stood at
// their leaving, plus the event that took them out; emptying that section would leave the client
// holding a room it cannot name. A room that restricts its history to members answers this
// differently, and that half is pinned by TestSyncLeftRoomStateFollowsHistoryVisibility.
func TestALeftMemberIsShownTheStateOfASharedRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "archivist", "pw-archivist", "")
	bob, bobToken, _ := h.register(t, "departed", "pw-departed", "")

	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/state/a.madeup.test.state/",
		map[string]any{"my_key": "before"}, aliceToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)

	// A sync whose timeline is asked for empty still carries the state, because the state section
	// is not a copy of the timeline: it is what the room looked like when the reader left it.
	body, _ := syncNow(t, h, bobToken, "&filter="+url.QueryEscape(
		`{"room":{"timeline":{"limit":0},"include_leave":true}}`))
	room := roomSection(t, body, "leave", roomID)
	if room == nil {
		t.Fatalf("the left room is missing from the sync: %v", body["rooms"])
	}
	timeline, ok := room["timeline"].(map[string]any)
	if !ok {
		t.Fatalf("the left room carries no timeline object: %v", room)
	}
	if events, isArray := timeline["events"].([]any); !isArray || len(events) != 0 {
		t.Errorf(`a timeline asked for with limit:0 holds %v, want an empty array`, timeline["events"])
	}
	state, ok := room["state"].(map[string]any)
	if !ok {
		t.Fatalf("the left room carries no state object: %v", room)
	}
	entries, _ := state["events"].([]any)
	foundLeave, foundBefore := false, false
	for _, entry := range entries {
		event, _ := entry.(map[string]any)
		switch event["type"] {
		case "m.room.member":
			if event["state_key"] == bob {
				content, _ := event["content"].(map[string]any)
				if membership, _ := content["membership"].(string); membership != "leave" {
					t.Errorf("the state section names %q for the departed member, want their leave event", membership)
				}
				foundLeave = true
			}
		case "a.madeup.test.state":
			content, _ := event["content"].(map[string]any)
			if value, _ := content["my_key"].(string); value != "before" {
				t.Errorf("the state section carries a.madeup.test.state as %v, want the value from before the leaving", content)
			}
			foundBefore = true
		}
	}
	if !foundLeave {
		t.Errorf("the left room's state section does not carry the member's own leave: %v", eventTypes(t, room, "state"))
	}
	if !foundBefore {
		t.Errorf("the left room's state section lost the state that stood when the member left: %v", eventTypes(t, room, "state"))
	}
}
