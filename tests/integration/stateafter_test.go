package integration

import (
	"net/url"
	"testing"
)

// MSC4222 moves the state a sync sends from the start of the timeline batch to its end, and
// calls it `state_after`. The reason is not tidiness: a server that trims membership events
// (lazy loading) can only do that if it knows which members the batch speaks about, and the
// batch's own events are the only place that is written down. A client told about a join in
// the timeline and given a state section that stops before it has to keep every membership it
// was ever shown, forever, to know who is in the room.

// memberStateKeysIn lists the user whose member event a state section carries.
func memberStateKeysIn(t *testing.T, room map[string]any, field string) []string {
	t.Helper()
	list, _ := room[field].(map[string]any)
	raw, _ := list["events"].([]any)
	out := []string{}
	for _, entry := range raw {
		event, _ := entry.(map[string]any)
		if event["type"] != "m.room.member" {
			continue
		}
		if key, ok := event["state_key"].(string); ok {
			out = append(out, key)
		}
	}
	return out
}

// eventIDsIn lists the event IDs a section carries, so a test can ask whether the same event
// appears twice — which is the point of the section, not an accident.
func eventIDsIn(t *testing.T, room map[string]any, field string) []string {
	t.Helper()
	list, _ := room[field].(map[string]any)
	raw, _ := list["events"].([]any)
	out := []string{}
	for _, entry := range raw {
		event, _ := entry.(map[string]any)
		if id, ok := event["event_id"].(string); ok {
			out = append(out, id)
		}
	}
	return out
}

// timelineSenderIDs lists who spoke in the timeline batch, which is the set MSC4222 says the
// trimmed state has to cover.
func timelineSenderIDs(t *testing.T, room map[string]any) []string {
	t.Helper()
	list, _ := room["timeline"].(map[string]any)
	raw, _ := list["events"].([]any)
	out := []string{}
	for _, entry := range raw {
		event, _ := entry.(map[string]any)
		if sender, ok := event["sender"].(string); ok && !containsString(out, sender) {
			out = append(out, sender)
		}
	}
	return out
}

// stateAfterRoom sets up a room with three members — two of whom speak — and returns it with
// the tokens the test needs.
func stateAfterRoom(t *testing.T) (*harness, string, string, string, string, string) {
	t.Helper()
	h := newHarness(t)
	aliceID, aliceToken, _ := h.register(t, "callum", "pw-callum", "")
	bobID, bobToken, _ := h.register(t, "dot", "pw-dot", "")
	carolID, carolToken, _ := h.register(t, "eliot", "pw-eliot", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	join(t, h, roomID, bobToken, map[string]any{})
	join(t, h, roomID, carolToken, map[string]any{})
	// Only two of the three speak. The silent one is what a trimmed state is allowed to drop.
	sendText(t, h, roomID, aliceToken, "sa-alice", "alice speaks")
	sendText(t, h, roomID, bobToken, "sa-bob", "bob speaks")
	return h, roomID, aliceToken, aliceID, bobID, carolID
}

// TestSyncStateAfterAnswersAtTheEndOfTheBatch checks the shape and the promise: the client that
// asked gets `state_after` instead of `state`, and that section covers every sender in the
// batch it came with — including the membership events that are themselves in the batch, which
// is what the old "state minus the timeline" rule used to drop.
func TestSyncStateAfterAnswersAtTheEndOfTheBatch(t *testing.T) {
	t.Parallel()
	h, roomID, aliceToken, aliceID, bobID, carolID := stateAfterRoom(t)

	// The ordinary answer first, so the difference below cannot be blamed on a missing
	// section: no flag means `state`, and nothing named `state_after`.
	plain := roomSection(t, syncBody(t, h, aliceToken, ""), "join", roomID)
	if _, ok := plain["state"].(map[string]any); !ok {
		t.Fatalf("a sync with no flag lost its state section: %v", plain)
	}
	for _, field := range []string{"state_after", "org.matrix.msc4222.state_after"} {
		if _, ok := plain[field].(map[string]any); ok {
			t.Errorf("a sync with no flag carries %s: %v", field, plain)
		}
	}

	query := "&use_state_after=true&filter=" + url.QueryEscape(`{"room":{"state":{"lazy_load_members":true}}}`)
	room := roomSection(t, syncBody(t, h, aliceToken, query), "join", roomID)
	if room == nil {
		t.Fatalf("the joined room is missing from the state_after sync")
	}
	if _, ok := room["state"].(map[string]any); ok {
		t.Errorf("a state_after sync still carries state: %v", sectionKeys(room))
	}
	after, ok := room["state_after"].(map[string]any)
	if !ok {
		t.Fatalf("no state_after section: %v", sectionKeys(room))
	}
	if _, ok := after["events"].([]any); !ok {
		t.Fatalf("state_after has no events array: %v", after)
	}

	// Every sender in the batch must have a member event in the state section.
	keys := memberStateKeysIn(t, room, "state_after")
	for _, sender := range timelineSenderIDs(t, room) {
		if !containsString(keys, sender) {
			t.Errorf("state_after has no member event for the timeline sender %s; it has %v", sender, keys)
		}
	}
	// A member event that is in the timeline is in the state section too. This is the
	// MSC4222 rule stated from the other side: the state is not "the room minus what you
	// were just shown", it is "the room as of what you were just shown".
	bobJoin := ""
	for _, id := range eventIDsIn(t, room, "timeline") {
		if containsString(eventIDsIn(t, room, "state_after"), id) && id != "" {
			bobJoin = id
			break
		}
	}
	if bobJoin == "" {
		t.Errorf("no event appears in both the timeline and state_after; state_after is not the state at the end of the batch")
	}

	// Without lazy loading the section is the whole state, so the silent member is in it.
	full := roomSection(t, syncBody(t, h, aliceToken, "&use_state_after=true"), "join", roomID)
	fullKeys := memberStateKeysIn(t, full, "state_after")
	for _, want := range []string{aliceID, bobID, carolID} {
		if !containsString(fullKeys, want) {
			t.Errorf("state_after without lazy loading is missing the member %s; it has %v", want, fullKeys)
		}
	}
}

// TestSyncStateAfterTrimsSilentMembersUnderLazyLoad checks the half of the feature that costs
// something to get right: the state section is allowed to be small, but only as small as the
// batch it travels with, and a member nobody spoke about in this batch is what may be dropped.
func TestSyncStateAfterTrimsSilentMembersUnderLazyLoad(t *testing.T) {
	t.Parallel()
	h, roomID, aliceToken, aliceID, bobID, carolID := stateAfterRoom(t)

	// A batch of one event: Alice's message. She is the only sender in it.
	filter := `{"room":{"timeline":{"limit":1},"state":{"lazy_load_members":true}}}`
	query := "&use_state_after=true&filter=" + url.QueryEscape(filter)
	room := roomSection(t, syncBody(t, h, aliceToken, query), "join", roomID)
	if room == nil {
		t.Fatalf("the joined room is missing from the state_after sync")
	}
	senders := timelineSenderIDs(t, room)
	if len(senders) != 1 {
		t.Fatalf("the timeline batch is not the one event this test asked for: %v", senders)
	}
	spoken := senders[0]
	keys := memberStateKeysIn(t, room, "state_after")
	if !containsString(keys, spoken) {
		t.Errorf("state_after is missing the member event of %s, the one sender in this batch: %v", spoken, keys)
	}
	for _, member := range []string{aliceID, bobID, carolID} {
		if member == spoken {
			continue
		}
		if containsString(keys, member) {
			t.Errorf("state_after carries the member event of %s, who is not spoken about in this batch: %v", member, keys)
		}
	}
	// Trimming membership is not trimming state: the room's other state is still there, and
	// a client that cannot show a room's name or its power levels has gained nothing.
	if kinds := eventTypes(t, room, "state_after"); !containsString(kinds, "m.room.create") {
		t.Errorf("state_after dropped non-member state: %v", kinds)
	}
}

// TestSyncStateAfterUnderTheUnstableFlag checks that the field is named by the flag. A client
// that asks with the pre-stabilisation spelling looks for the pre-stabilisation name; answering
// it only under the stable name reads as the feature not having happened.
func TestSyncStateAfterUnderTheUnstableFlag(t *testing.T) {
	t.Parallel()
	h, roomID, aliceToken, _, _, _ := stateAfterRoom(t)

	room := roomSection(t, syncBody(t, h, aliceToken, "&org.matrix.msc4222.use_state_after=true"), "join", roomID)
	if _, ok := room["org.matrix.msc4222.state_after"].(map[string]any); !ok {
		t.Fatalf("no org.matrix.msc4222.state_after section: %v", sectionKeys(room))
	}
	if _, ok := room["state"].(map[string]any); ok {
		t.Errorf("a state_after sync still carries state: %v", sectionKeys(room))
	}
}

// syncBody syncs with no timeout and returns the decoded body.
func syncBody(t *testing.T, h *harness, token, query string) map[string]any {
	t.Helper()
	body, _ := syncNow(t, h, token, query)
	return body
}

// sectionKeys names the sections of a room for a failure message, so a missing field can be told
// apart from a misnamed one.
func sectionKeys(room map[string]any) []string {
	out := []string{}
	for key := range room {
		out = append(out, key)
	}
	return out
}
