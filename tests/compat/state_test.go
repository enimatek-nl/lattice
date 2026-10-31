package compat

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestStateKeysOnTheWire checks that every state event carries state_key,
// including the state events whose key is the empty string: m.room.name,
// m.room.topic, m.room.create, m.room.power_levels and friends. A client decides
// whether an event is state by looking for that field, so without it a room
// rename is read as a message, the room settings never update, and the client
// cannot find the room version in m.room.create any more.
func TestStateKeysOnTheWire(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "first name"})
	path := "/_matrix/client/v3/rooms/" + url.QueryEscape(roomID)

	first, resp := server.Sync(alice, "?timeout=0")
	joined, ok := first.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the initial sync does not have %s: %s", roomID, resp.Raw)
	}
	checkStateKeys(t, "the initial state", joined.State.Events)
	if !hasStateEvent(joined.State.Events, matrix.TypeCreate, "") {
		t.Errorf("the initial state has no m.room.create with an empty state key: %s", resp.Raw)
	}
	alice.NextBatch = first.NextBatch

	// A client renames a room with a trailing slash, because the state key is
	// empty.
	rename := server.Do(http.MethodPut, path+"/state/m.room.name/", alice.Token,
		map[string]any{"name": "second name"})
	if rename.Status != http.StatusOK {
		t.Fatalf("renaming the room: got %d %s", rename.Status, rename.Raw)
	}
	if id := rename.String("event_id"); id == "" {
		t.Errorf("the rename returned no event id: %s", rename.Raw)
	}

	second, resp := server.SyncSince(alice, 0)
	renamed, ok := second.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the rename never reached a sync: %s", resp.Raw)
	}
	checkStateKeys(t, "the state delta", renamed.State.Events)
	checkStateKeys(t, "the timeline", renamed.Timeline.Events)
	var content struct {
		Name string `json:"name"`
	}
	if len(renamed.State.Events) != 1 || renamed.State.Events[0].Type != matrix.TypeName {
		t.Fatalf("the state delta is %v, want the m.room.name alone", renamed.State.Events)
	}
	if err := json.Unmarshal(renamed.State.Events[0].Content, &content); err != nil {
		t.Fatalf("decoding the rename: %v (%s)", err, resp.Raw)
	}
	if content.Name != "second name" {
		t.Errorf("the rename carries %q, want second name", content.Name)
	}

	// The raw body is what a client parses: state_key has to be written out even
	// though it is empty.
	if !strings.Contains(string(resp.Raw), `"state_key":""`) {
		t.Errorf("the sync response omits an empty state_key: %s", resp.Raw)
	}

	// Paginated history delivers the same events.
	var page struct {
		Chunk []matrix.Event `json:"chunk"`
	}
	messages := server.MustDo(http.MethodGet, path+"/messages?dir=b&limit=20", alice.Token, nil, http.StatusOK)
	if err := json.Unmarshal(messages.Raw, &page); err != nil {
		t.Fatalf("decoding /messages: %v (%s)", err, messages.Raw)
	}
	if len(page.Chunk) == 0 {
		t.Fatalf("/messages is empty: %s", messages.Raw)
	}
	checkStateKeys(t, "/messages", page.Chunk)

	// A message is not state and must not grow a state_key.
	server.Send(alice, roomID, "typed", "an ordinary message")
	alice.NextBatch = second.NextBatch
	third, resp := server.SyncSince(alice, 0)
	msgRoom, ok := third.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the message never reached a sync: %s", resp.Raw)
	}
	var sawMessage bool
	for _, event := range msgRoom.Timeline.Events {
		if event.Type == matrix.TypeMessage && event.StateKey != nil {
			t.Errorf("the m.room.message event carries state_key %q", *event.StateKey)
		}
		if event.Type == matrix.TypeMessage {
			sawMessage = true
		}
	}
	if !sawMessage {
		t.Errorf("no m.room.message in the timeline: %s", resp.Raw)
	}

	// The room topic follows the same rule.
	server.MustDo(http.MethodPut, path+"/state/m.room.topic/", alice.Token,
		map[string]any{"topic": "what this room is for"}, http.StatusOK)
	alice.NextBatch = third.NextBatch
	fourth, resp := server.SyncSince(alice, 0)
	checkStateKeys(t, "the topic delta", fourth.Rooms.Join[roomID].State.Events)
}

// checkStateKeys fails the test for every event in the list that carries no
// state_key.
func checkStateKeys(t *testing.T, what string, events []matrix.Event) {
	t.Helper()
	for _, event := range events {
		if event.StateKey == nil {
			t.Errorf("%s: the %s event has no state_key, so a client reads it as a message",
				what, event.Type)
		}
	}
}

// hasStateEvent reports whether the list holds an event of the type and state key.
func hasStateEvent(events []matrix.Event, typ, stateKey string) bool {
	for _, event := range events {
		if event.Type == typ && event.StateKey != nil && *event.StateKey == stateKey {
			return true
		}
	}
	return false
}
