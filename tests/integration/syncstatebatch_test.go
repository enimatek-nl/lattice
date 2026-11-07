package integration

import (
	"net/url"
	"testing"
)

// A first sync has nothing the client already holds, so its state section is the room's whole
// state — including the state events the timeline batch happens to show. Subtracting the batch
// from the state is an optimisation for incremental syncs, where the client is being handed
// those events in the same response, and it is wrong the moment the batch does not actually
// carry them: a limited or type-filtered timeline hides an event from the batch, and a client
// that then finds it missing from the state too has been shown a room with a hole in it.

func TestSyncFirstBatchKeepsStateTheBatchShows(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, _ := h.register(t, "marta", "pw-marta", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"name": "the room", "topic": "first topic"})

	// The newest event in this room is a state event, and the batch below is one event
	// wide, so the batch and the state overlap completely on it.
	filter := `{"room":{"timeline":{"limit":1}}}`
	room := roomSection(t, syncBody(t, h, aliceToken, "&filter="+url.QueryEscape(filter)), "join", roomID)
	timeline := eventIDsIn(t, room, "timeline")
	if len(timeline) != 1 {
		t.Fatalf("the batch is not the one event asked for: %v", timeline)
	}
	state := eventIDsIn(t, room, "state")
	for _, id := range timeline {
		if !containsString(state, id) {
			t.Errorf("the state section omits %s, which this first sync's batch shows; state has %v", id, state)
		}
	}
	// The rest of the room's state is there too: a limited batch is not a limited room.
	if !containsString(eventTypes(t, room, "state"), "m.room.create") {
		t.Errorf("the state section is missing the room's creation: %v", eventTypes(t, room, "state"))
	}

	// A filter that hides the event from the batch must not cost the client its state entry.
	hidden := `{"room":{"timeline":{"limit":1,"not_types":["m.room.topic"]}}}`
	room = roomSection(t, syncBody(t, h, aliceToken, "&filter="+url.QueryEscape(hidden)), "join", roomID)
	if containsString(eventTypes(t, room, "timeline"), "m.room.topic") {
		t.Errorf("the timeline filter did not hide the topic event: %v", eventTypes(t, room, "timeline"))
	}
	if !containsString(eventTypes(t, room, "state"), "m.room.topic") {
		t.Errorf("hiding the topic from the batch also lost it from the room's state: %v", eventTypes(t, room, "state"))
	}

	// An incremental sync may still subtract what it is handing over in the same response.
	_, next := syncNow(t, h, aliceToken, "")
	sendText(t, h, roomID, aliceToken, "batch-txn", "a message")
	room = roomSection(t, syncBody(t, h, aliceToken, "&since="+url.QueryEscape(next)), "join", roomID)
	for _, id := range eventIDsIn(t, room, "timeline") {
		if containsString(eventIDsIn(t, room, "state"), id) {
			t.Errorf("the incremental sync serves %s twice, in the batch and in the state", id)
		}
	}
	_ = alice
}
