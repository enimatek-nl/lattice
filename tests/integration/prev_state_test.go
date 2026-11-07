package integration

import (
	"net/http"
	"testing"
)

// eventsOfType collects the events of one type out of a sync section, looking at both the
// room's current state and its timeline, because which of the two an event lands in depends on
// how fresh it is.
func eventsOfType(t *testing.T, room map[string]any, eventType string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, field := range []string{"state", "timeline"} {
		list, _ := room[field].(map[string]any)
		raw, _ := list["events"].([]any)
		for _, entry := range raw {
			event, _ := entry.(map[string]any)
			kind, _ := event["type"].(string)
			if kind == eventType {
				out = append(out, event)
			}
		}
	}
	return out
}

// unsignedOf reads an event's unsigned object.
func unsignedOf(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	unsigned, _ := event["unsigned"].(map[string]any)
	return unsigned
}

// TestPrevStateShowsWhatAStateEventReplaced checks that a state event carries, under
// `unsigned`, the content it replaced and who wrote that one.
//
// This is the client's view of a change rather than the change itself: a room whose topic moved
// from one thing to another is only legible to a client that can see both sides, and an
// invitation is only answerable when the user can see what they were invited as. The values are
// something only the server that wrote the event knows — the state as it stood at that moment —
// so they belong to the presentation layer and not to the event's own bytes.
func TestPrevStateShowsWhatAStateEventReplaced(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, _ := h.register(t, "ines", "pw-ines", "")
	bob, bobToken, _ := h.register(t, "josip", "pw-josip", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{
		"preset":    "private_chat",
		"topic":     "first topic",
		"name":      "Prev state",
		"invite":    []string{bob},
		"direction": "",
	})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)

	// One change, then the same content offered again. Whether the second attempt is stored at
	// all is this server's choice; either way only the first of them can honestly say what it
	// replaced.
	for _, topic := range []string{"second topic", "third topic", "second topic"} {
		h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic",
			map[string]any{"topic": topic}, aliceToken), http.StatusOK)
	}

	body, _ := syncNow(t, h, bobToken, "")
	room := roomSection(t, body, "join", roomID)
	if room == nil {
		t.Fatalf("bob's sync does not show the room: %v", body["rooms"])
	}

	// The topic that moved: it says what it replaced, and who wrote the thing it replaced.
	// Each topic reports the one it replaced, if any. Three writes in the order second, third,
	// second mean that the answer cannot be read off the room's current state: the first and the
	// last of them are the same content, and only the first of them replaced something. Whether
	// this server stores the repeated write at all is its own business, so the assertions are
	// made about the topic events it does serve.
	// What each event reports, by the content it carries: the room's first topic replaced
	// nothing, the move to "third topic" replaced "second topic", and the two writes of
	// "second topic" replaced "first topic" and "third topic" in that order. Reading the answer
	// off the room's current state could not produce both of those.
	type claim struct {
		topic  string
		before string
	}
	var claims []claim
	// One event may be served twice in a response — a state event that is both in the
	// timeline batch and in the room's state — and that is legal. What is not legal is the
	// same event reporting two different things it replaced, so the assertions below are
	// made per event id.
	seen := map[string]claim{}
	for _, event := range eventsOfType(t, room, "m.room.topic") {
		content, _ := event["content"].(map[string]any)
		topic, _ := content["topic"].(string)
		unsigned := unsignedOf(t, event)
		previous, _ := unsigned["prev_content"].(map[string]any)
		before, _ := previous["topic"].(string)
		id, _ := event["event_id"].(string)
		current := claim{topic: topic, before: before}
		if already, ok := seen[id]; ok {
			if already != current {
				t.Errorf("the topic event %s is served as replacing %q and as replacing %q", id, already.before, current.before)
			}
			continue
		}
		seen[id] = current
		claims = append(claims, current)
		if previous != nil && topic != "first topic" {
			if sender, _ := unsigned["prev_sender"].(string); sender != alice {
				t.Errorf("prev_sender of the topic change to %q = %q, want %s", topic, sender, alice)
			}
		}
	}
	count := func(want claim) int {
		n := 0
		for _, got := range claims {
			if got == want {
				n++
			}
		}
		return n
	}
	for _, want := range []claim{
		{topic: "first topic", before: ""},
		{topic: "second topic", before: "first topic"},
		{topic: "third topic", before: "second topic"},
		{topic: "second topic", before: "third topic"},
	} {
		if count(want) != 1 {
			t.Errorf("%d served topic events read %q as replacing %q, want exactly 1; served: %v",
				count(want), want.topic, want.before, claims)
		}
	}
	for _, got := range claims {
		if got.topic == "first topic" && got.before != "" {
			t.Errorf("the room's first topic reports it replaced %q", got.before)
		}
	}

	// A member event answering an invitation carries the invitation it answers. This is how a
	// client knows the room it joined was offered to it as an invitation, and by whom.
	var joined map[string]any
	for _, event := range eventsOfType(t, room, "m.room.member") {
		stateKey, _ := event["state_key"].(string)
		content, _ := event["content"].(map[string]any)
		if stateKey == bob && content["membership"] == "join" {
			joined = event
		}
	}
	if joined == nil {
		t.Fatalf("bob's join event is not in his sync: %v", room)
	}
	unsigned := unsignedOf(t, joined)
	previous, _ := unsigned["prev_content"].(map[string]any)
	if previous == nil {
		t.Fatalf("the join event carries no prev_content, although it replaced an invitation: %v", joined)
	}
	if previous["membership"] != "invite" {
		t.Errorf("prev_content of the join = %v, want the invitation it answers", previous)
	}
	if sender, _ := unsigned["prev_sender"].(string); sender != alice {
		t.Errorf("prev_sender of the join = %q, want %s", sender, alice)
	}

	// The first event of a kind has nothing to compare itself with, and saying otherwise would
	// invent a history the room does not have.
	for _, event := range eventsOfType(t, room, "m.room.create") {
		unsigned := unsignedOf(t, event)
		if _, ok := unsigned["prev_content"]; ok {
			t.Errorf("the create event claims a previous content: %v", unsigned)
		}
		if _, ok := unsigned["prev_sender"]; ok {
			t.Errorf("the create event claims a previous sender: %v", unsigned)
		}
	}
}
