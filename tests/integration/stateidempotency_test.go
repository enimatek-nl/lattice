package integration

import (
	"net/http"
	"testing"
)

// TestStateThatDoesNotChangeTheRoomIsNotWrittenAgain pins the rule that a state event is a
// statement about a map slot, so repeating the statement already in that slot is not a new event:
// the caller is given the ID of the event that already says it. Two neighbours of the rule are
// pinned in the same place because they are where the rule would do damage if it were drawn too
// widely: a redaction emptied the slot, so writing the words back *is* a change; and a repeated
// *message* is not a state statement at all, so two identical messages stay two events.
func TestStateThatDoesNotChangeTheRoomIsNotWrittenAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "statemod", "pw-statemod", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})

	first := setState(t, h, roomID, "m.room.topic", "", map[string]any{"topic": "the same words"}, token)
	second := setState(t, h, roomID, "m.room.topic", "", map[string]any{"topic": "the same words"}, token)
	if first != second {
		t.Errorf("the same state sent twice made two events: %s then %s", first, second)
	}

	// Different words are a different statement, and make a new event.
	third := setState(t, h, roomID, "m.room.topic", "", map[string]any{"topic": "other words"}, token)
	if third == first {
		t.Errorf("changing the topic returned the previous event %s; the room's state did change", first)
	}

	// One topic event in the room's timeline, not three: the repeats were not written.
	if got := timelineTypes(t, h, roomID, token, "m.room.topic"); got != 2 {
		t.Errorf("the timeline holds %d m.room.topic events, want 2 (the two different topics)", got)
	}

	// A message repeated is not a state statement. Two identical messages are two events, and the
	// rule that deduplicates state must not reach them.
	one := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-repeat-a",
		`{"msgtype":"m.text","body":"twice"}`, token), http.StatusOK).decoded(t)["event_id"]
	two := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-repeat-b",
		`{"msgtype":"m.text","body":"twice"}`, token), http.StatusOK).decoded(t)["event_id"]
	if one == two {
		t.Errorf("two identical messages were collapsed into %v; a message is not a statement about a map slot", one)
	}

	// A redaction emptied the slot, so writing the same words back is a change.
	redacted := h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/redact/"+third+"/txn-redact-topic",
		map[string]any{"reason": "testing"}, token), http.StatusOK)
	_ = redacted
	restored := setState(t, h, roomID, "m.room.topic", "", map[string]any{"topic": "other words"}, token)
	if restored == third {
		t.Errorf("the same content was written back over a redaction and returned the redacted event %s; the content coming back is a change", third)
	}
}

// TestRepeatingAStateWriteYouHaveLostTheRightToMakeIsRefused pins where the no-change rule sits:
// authorisation is asked first, so a member who has been stripped of the power to set the topic is
// refused even when what they are trying to set is what the topic already says. Reading the rule in
// the other order would let a powerless member's pointless write answer 200 while the same write
// with one different word answers 403, which is a rule nobody can reason about.
func TestRepeatingAStateWriteYouHaveLostTheRightToMakeIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, ownerToken, _ := h.register(t, "levelsowner", "pw-levelsowner", "")
	member, memberToken, _ := h.register(t, "levelsmember", "pw-levelsmember", "")
	roomID := createRoom(t, h, ownerToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, memberToken), http.StatusOK)

	// The member can set state to start with.
	lifted := h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels", map[string]any{
		"users":          map[string]any{"@levelsowner:" + h.app.Cfg.ServerName: 100, member: 50},
		"users_default":  0,
		"events_default": 0,
		"state_default":  50,
		"invite":         50,
		"kick":           50,
		"ban":            50,
		"redact":         50,
	}, ownerToken)
	if lifted.status != http.StatusOK {
		t.Fatalf("the room owner could not give the member state power: %d %s", lifted.status, lifted.body)
	}
	topic := setState(t, h, roomID, "m.room.topic", "", map[string]any{"topic": "theirs to write"}, memberToken)

	// And cannot any more.
	lowered := h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels", map[string]any{
		"users":          map[string]any{"@levelsowner:" + h.app.Cfg.ServerName: 100, member: 0},
		"users_default":  0,
		"events_default": 0,
		"state_default":  50,
		"invite":         50,
		"kick":           50,
		"ban":            50,
		"redact":         50,
	}, ownerToken)
	if lowered.status != http.StatusOK {
		t.Fatalf("the room owner could not take the member's state power away: %d %s", lowered.status, lowered.body)
	}

	repeat := h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic",
		map[string]any{"topic": "theirs to write"}, memberToken)
	if repeat.status != http.StatusForbidden {
		t.Errorf("a member with no state power re-setting the topic to what it already is: status %d (body %s), want 403. The event the rule would return is %s",
			repeat.status, repeat.body, topic)
	}
}

// TestJoiningARoomYouAreAlreadyInDoesNotWriteAnotherMemberEvent is the membership half of the same
// rule: a join is a state write, the room's current member event already says `join` for this user,
// so nothing is written and the member event the room holds keeps its ID.
func TestJoiningARoomYouAreAlreadyInDoesNotWriteAnotherMemberEvent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, ownerToken, _ := h.register(t, "repeatowner", "pw-repeatowner", "")
	bob, bobToken, _ := h.register(t, "repeatbob", "pw-repeatbob", "")
	roomID := createRoom(t, h, ownerToken, map[string]any{"preset": "public_chat"})

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)
	first := memberEventID(t, h, roomID, bob, ownerToken)

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)
	second := memberEventID(t, h, roomID, bob, ownerToken)
	if first != second {
		t.Errorf("joining a room twice replaced its member event: %s then %s", first, second)
	}
	if got := timelineJoinEvents(t, h, roomID, bob, ownerToken); got != 1 {
		t.Errorf("the room's timeline holds %d join events for %s, want 1", got, bob)
	}

	// Leaving and joining again *is* a change, and writes a new member event.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)
	third := memberEventID(t, h, roomID, bob, ownerToken)
	if third == second {
		t.Errorf("leaving and rejoining kept the same member event %s; the room's state did change", second)
	}
}

// setState writes one state event and returns the ID it was given.
func setState(t *testing.T, h *harness, roomID, eventType, stateKey string, content map[string]any, token string) string {
	t.Helper()
	path := "/_matrix/client/v3/rooms/" + roomID + "/state/" + eventType
	if stateKey != "" {
		path += "/" + stateKey
	}
	res := h.mustStatus(t, h.put(t, path, content, token), http.StatusOK)
	id, _ := res.decoded(t)["event_id"].(string)
	if id == "" {
		t.Fatalf("writing state %s answered no event_id: %s", eventType, res.body)
	}
	return id
}

// memberEventID reads the event ID of the member event a room currently holds for a user. The
// single-state endpoint is not usable for this: it answers with the event's *content*, and a content
// object carries no ID. /messages gives whole events, newest first, so the first `join` member
// event naming this user is the room's current one.
func memberEventID(t *testing.T, h *harness, roomID, userID, token string) string {
	t.Helper()
	res := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?limit=100", token), http.StatusOK)
	chunk, _ := res.decoded(t)["chunk"].([]any)
	for _, entry := range chunk {
		event, _ := entry.(map[string]any)
		kind, _ := event["type"].(string)
		key, _ := event["state_key"].(string)
		content, _ := event["content"].(map[string]any)
		membership, _ := content["membership"].(string)
		if kind == "m.room.member" && key == userID && membership == "join" {
			id, _ := event["event_id"].(string)
			if id != "" {
				return id
			}
		}
	}
	t.Fatalf("no join member event for %s in the room's messages: %v", userID, res.body)
	return ""
}

// timelineTypes counts the events of one type in a room's timeline, read through /messages.
func timelineTypes(t *testing.T, h *harness, roomID, token, eventType string) int {
	t.Helper()
	res := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?limit=100", token), http.StatusOK)
	chunk, _ := res.decoded(t)["chunk"].([]any)
	count := 0
	for _, entry := range chunk {
		event, _ := entry.(map[string]any)
		if kind, _ := event["type"].(string); kind == eventType {
			count++
		}
	}
	return count
}

// timelineJoinEvents counts the member events naming this user as joining, read through /messages.
func timelineJoinEvents(t *testing.T, h *harness, roomID, userID, token string) int {
	t.Helper()
	res := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?limit=100", token), http.StatusOK)
	chunk, _ := res.decoded(t)["chunk"].([]any)
	count := 0
	for _, entry := range chunk {
		event, _ := entry.(map[string]any)
		kind, _ := event["type"].(string)
		key, _ := event["state_key"].(string)
		content, _ := event["content"].(map[string]any)
		membership, _ := content["membership"].(string)
		if kind == "m.room.member" && key == userID && membership == "join" {
			count++
		}
	}
	return count
}
