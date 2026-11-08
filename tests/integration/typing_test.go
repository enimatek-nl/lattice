package integration

import (
	"net/http"
	"testing"
	"time"
)

// TestATypingIndicatorReachesTheOtherMembersSync raises a hand in a room and asks what the other
// member's sync says about it. A typing indicator is not an event: it has no ID, no place in the
// room's graph, and a lifetime the client itself set, so it has to arrive in the room's ephemeral
// section — and it has to arrive *without* a `room_id`, because the section already says which
// room this is and the field exists only over federation.
func TestATypingIndicatorReachesTheOtherMembersSync(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, _ := h.register(t, "typer", "pw-typer", "")
	_, bobToken, _ := h.register(t, "watcher", "pw-watcher", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)

	_, seen := syncNow(t, h, bobToken, "")

	setTyping(t, h, roomID, alice, aliceToken, true, 10000)
	body, afterStart := syncNow(t, h, bobToken, "&since="+seen)
	got := typingUsers(t, h, body, roomID)
	if len(got) != 1 || got[0] != alice {
		t.Fatalf("the room's typing list is %v, want just %s", got, alice)
	}
	for _, event := range typingEvents(t, h, body, roomID) {
		if _, found := event["room_id"]; found {
			t.Errorf("a client-facing typing event carries a room_id; the room's section already says which room: %v", event)
		}
		if _, found := event["event_id"]; found {
			t.Errorf("a typing indicator was given an event ID; it is not an event: %v", event)
		}
	}

	// The same indicator set again is still one indicator, and the current list is what a client
	// is shown — not a history of the room's keystrokes.
	setTyping(t, h, roomID, alice, aliceToken, true, 10000)
	again, _ := syncNow(t, h, bobToken, "&since="+seen)
	if got := typingUsers(t, h, again, roomID); len(got) != 1 || got[0] != alice {
		t.Errorf("typing again changed the room's typing list to %v, want just %s", got, alice)
	}

	// Stopping is an answer, not an absence: the list goes empty rather than the event going
	// missing, which is the only way a client learns to unshow the indicator.
	setTyping(t, h, roomID, alice, aliceToken, false, 0)

	// A window that holds no change to the room's typing is not sent the same list again. The
	// window has to start past the stop, which is the change the previous sync reported: asking
	// with the older token again would be asking for that change a second time, and the answer is
	// legitimately the same.
	stoppedBody, stoppedToken := syncNow(t, h, bobToken, "&since="+afterStart)
	if got := typingUsers(t, h, stoppedBody, roomID); got != nil {
		t.Errorf("the typing list after stopping is %v, want empty", got)
	}
	quiet, _ := syncNow(t, h, bobToken, "&since="+stoppedToken)
	if again := typingEvents(t, h, quiet, roomID); len(again) != 0 {
		t.Errorf("a window with no change to typing still carries %v", again)
	}
}

// TestATypingIndicatorExpiresOnItsOwnClientSetsIt checks that an indicator the client set a
// timeout on does not outlive that timeout. The client is not asked to take it back, and a hand
// left raised on another member's screen forever is the failure this is about.
func TestATypingIndicatorExpiresOnItsOwnClientSetsIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, _ := h.register(t, "brief", "pw-brief", "")
	_, bobToken, _ := h.register(t, "patient", "pw-patient", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)

	_, seen := syncNow(t, h, bobToken, "")
	setTyping(t, h, roomID, alice, aliceToken, true, 250)
	body, _ := syncNow(t, h, bobToken, "&since="+seen)
	if got := typingUsers(t, h, body, roomID); len(got) != 1 || got[0] != alice {
		t.Fatalf("the indicator did not arrive at all: %v", got)
	}

	// Past the timeout. The indicator is not merely absent from the answer: the room is told the
	// list is now empty, because the client that was shown the hand has to be shown it lowered.
	time.Sleep(400 * time.Millisecond)
	expired, _ := syncNow(t, h, bobToken, "&since="+seen)
	if got := typingUsers(t, h, expired, roomID); got != nil {
		t.Errorf("%s is still shown typing after the timeout it set itself: %v", alice, got)
	}
}

// TestTypingIsNotShownToSomebodyOutsideTheRoom is the leak test: a user who is not in the room is
// not shown the room, and so is not shown who is typing in it.
func TestTypingIsNotShownToSomebodyOutsideTheRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, _ := h.register(t, "insider", "pw-insider", "")
	_, charlieToken, _ := h.register(t, "outsider", "pw-outsider", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat"})

	_, charlieSeen := syncNow(t, h, charlieToken, "")
	setTyping(t, h, roomID, alice, aliceToken, true, 10000)

	body, _ := syncNow(t, h, charlieToken, "&since="+charlieSeen)
	if room := roomSection(t, body, "join", roomID); room != nil {
		t.Fatalf("a user outside the room was shown the room at all: %v", room)
	}
}

// TestTypingIsRefusedForWhatItIsNot checks the door rather than the indicator: setting somebody
// else's typing, typing in a room the caller is not in, a request that does not say what it means,
// and a timeout that is not a length of time.
func TestTypingIsRefusedForWhatItIsNot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, _ := h.register(t, "owner", "pw-owner", "")
	mallory, malloryToken, _ := h.register(t, "impostor", "pw-impostor", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	// A path naming another user is not that user's request to make.
	res := h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+alice,
		map[string]any{"typing": true, "timeout": 10000}, malloryToken)
	if res.status != http.StatusForbidden {
		t.Errorf("setting another user's typing: status %d (body %s), want 403", res.status, res.body)
	}

	// A non-member's typing is not that room's to show.
	res = h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+mallory,
		map[string]any{"typing": true, "timeout": 10000}, malloryToken)
	if res.status != http.StatusForbidden {
		t.Errorf("typing in a room the caller is not in: status %d (body %s), want 403", res.status, res.body)
	}

	// `typing` is required: a request that does not say which way it means cannot be read as
	// "stopped", which is what a missing boolean would otherwise become.
	res = h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+alice,
		map[string]any{"timeout": 10000}, aliceToken)
	if res.status != http.StatusBadRequest {
		t.Errorf("a typing notification with no `typing` key: status %d (body %s), want 400", res.status, res.body)
	}

	res = h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+alice,
		map[string]any{"typing": true, "timeout": -1}, aliceToken)
	if res.status != http.StatusBadRequest {
		t.Errorf("a negative typing timeout: status %d (body %s), want 400", res.status, res.body)
	}
}

// TestTypingTakesPartInTheStream checks the thing every synchronisable value has to do: the change
// has to be inside the window a client asks about, and the token the client is handed has to be at
// least as new as the change it was just told about. A typing write that took no position would be
// invisible to a client syncing from a token, and one that took a position above `next_batch` would
// be told twice forever.
func TestTypingTakesPartInTheStream(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, _ := h.register(t, "streamer", "pw-streamer", "")
	_, bobToken, _ := h.register(t, "reader", "pw-reader", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)

	_, seen := syncNow(t, h, bobToken, "")
	setTyping(t, h, roomID, alice, aliceToken, true, 10000)
	first, afterFirst := syncNow(t, h, bobToken, "&since="+seen)
	if got := typingUsers(t, h, first, roomID); len(got) != 1 {
		t.Fatalf("the typing change was outside the window the client asked for: %v", got)
	}
	if afterFirst == seen {
		t.Fatalf("a typing change did not move the stream: the token before and after are both %s", seen)
	}

	// And the next window starts past it: the same unchanged list is not delivered again.
	second, _ := syncNow(t, h, bobToken, "&since="+afterFirst)
	if again := typingEvents(t, h, second, roomID); len(again) != 0 {
		t.Errorf("the unchanged typing list was repeated in the window after it: %v", again)
	}
}

// setTyping is PUT /rooms/{roomID}/typing/{userID}, and insists on the 200 the specification
// promises.
func setTyping(t *testing.T, h *harness, roomID, userID, token string, typing bool, timeoutMillis int) {
	t.Helper()
	res := h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+userID,
		map[string]any{"typing": typing, "timeout": timeoutMillis}, token)
	if res.status != http.StatusOK {
		t.Fatalf("setting typing to %v: status %d (body %s)", typing, res.status, res.body)
	}
	if len(res.decoded(t)) != 0 {
		t.Errorf("the typing endpoint answered %s, want {}", res.body)
	}
}

// typingEvents reads a joined room's m.typing ephemeral events out of a sync response.
func typingEvents(t *testing.T, h *harness, body map[string]any, roomID string) []map[string]any {
	t.Helper()
	room := roomSection(t, body, "join", roomID)
	if room == nil {
		return nil
	}
	ephemeral, _ := room["ephemeral"].(map[string]any)
	entries, _ := ephemeral["events"].([]any)
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		event, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("an ephemeral entry is not an object: %v", entry)
		}
		if event["type"] == "m.typing" {
			out = append(out, event)
		}
	}
	return out
}

// typingUsers is the room's typing list as one sync reported it, or nil when the sync said the
// room's typing list is empty. A nil and a missing section are the same answer here, which is why
// the callers assert with `!= nil` for "nobody typing" and with a length for "somebody is".
func typingUsers(t *testing.T, h *harness, body map[string]any, roomID string) []string {
	t.Helper()
	events := typingEvents(t, h, body, roomID)
	if len(events) == 0 {
		return nil
	}
	last := events[len(events)-1]
	content, _ := last["content"].(map[string]any)
	raw, _ := content["user_ids"].([]any)
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		user, ok := entry.(string)
		if !ok {
			t.Fatalf("a user ID in a typing list is not a string: %v", entry)
		}
		out = append(out, user)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
