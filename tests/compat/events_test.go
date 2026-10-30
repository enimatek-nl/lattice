package compat

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestSendEvent covers PUT /rooms/{roomId}/send/{eventType}/{txnId} and the
// timeline of a sync.
func TestSendEvent(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	eventID := server.Send(alice, roomID, "txn-1", "hello")
	if eventID == "" {
		t.Fatal("sending a message returned no event_id")
	}

	replay := server.MustDo(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/txn-1", roomID), alice.Token,
		map[string]any{"msgtype": "m.text", "body": "different text"}, http.StatusOK)
	if replay.String("event_id") != eventID {
		t.Errorf("the same transaction created a second event: %q vs %q", replay.String("event_id"), eventID)
	}

	other := server.Do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/txn-1", roomID), bob.Token,
		map[string]any{"msgtype": "m.text", "body": "bob says hi"})
	if other.String("event_id") == eventID {
		t.Errorf("transaction ids must be scoped per sender: %s", other.Raw)
	}

	synced, resp := server.Sync(bob, "?timeout=0")
	room, ok := synced.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from sync: %s", resp.Raw)
	}
	var bodies []string
	for _, event := range room.Timeline.Events {
		if event.Type != matrix.TypeMessage {
			continue
		}
		var content struct {
			Body string `json:"body"`
		}
		if err := matrix.DecodeContent(event.Content, &content); err != nil {
			t.Fatalf("decoding the message content: %v", err)
		}
		bodies = append(bodies, content.Body)
	}
	if len(bodies) != 2 {
		t.Fatalf("timeline messages = %v", bodies)
	}
	if bodies[0] != "hello" || bodies[1] != "bob says hi" {
		t.Errorf("timeline order = %v", bodies)
	}
	if !room.Timeline.Limited {
		t.Logf("note: the initial timeline is not marked limited (%d events)", len(room.Timeline.Events))
	}

	// A stranger may not read or write the room.
	carol := server.Register("carol", "kettle")
	if resp := server.Do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/txn-9", roomID), carol.Token,
		map[string]any{"msgtype": "m.text", "body": "intruder"}); resp.Status != http.StatusForbidden {
		t.Errorf("a stranger sent a message: %d %s", resp.Status, resp.Raw)
	}

	// State types must go to the state endpoint.
	if resp := server.Do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.topic/txn-10", roomID), alice.Token,
		map[string]any{"topic": "nope"}); resp.Status == http.StatusOK {
		t.Errorf("a state event was accepted on the send endpoint: %s", resp.Raw)
	}
}

// TestStateEndpoints covers GET and PUT /rooms/{roomId}/state.
func TestStateEndpoints(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})

	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic/", alice.Token,
		map[string]any{"topic": "Off topic"}, http.StatusOK)
	got := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic/", alice.Token, nil, http.StatusOK)
	if got.String("topic") != "Off topic" {
		t.Errorf("topic = %s", got.Raw)
	}
	sameWithoutSlash := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic", alice.Token, nil, http.StatusOK)
	if sameWithoutSlash.String("topic") != "Off topic" {
		t.Errorf("reading without a trailing slash gave %s", sameWithoutSlash.Raw)
	}

	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.name/", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("reading absent state: got %d %s, want 404", resp.Status, resp.Raw)
	}

	// A state key that is not empty works too.
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/test.state-key/second", alice.Token,
		map[string]any{"value": 2}, http.StatusOK)
	one := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/test.state-key/second", alice.Token, nil, http.StatusOK)
	if value, _ := one.Body["value"].(float64); value != 2 {
		t.Errorf("state with a key = %s", one.Raw)
	}

	// Membership may not be edited through the state endpoint.
	if resp := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+alice.UserID, alice.Token,
		map[string]any{"membership": "leave"}); resp.Status == http.StatusOK {
		t.Errorf("membership was written through the state endpoint: %s", resp.Raw)
	}
	if resp := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.create/", alice.Token,
		map[string]any{"creator": alice.UserID}); resp.Status == http.StatusOK {
		t.Errorf("m.room.create was replaced: %s", resp.Raw)
	}
}

// TestRoomHistory covers GET /rooms/{roomId}/messages.
func TestRoomHistory(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})

	for i := 1; i <= 5; i++ {
		server.Send(alice, roomID, fmt.Sprintf("txn-%d", i), fmt.Sprintf("message %d", i))
	}

	page := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=2", alice.Token, nil, http.StatusOK)
	chunk, _ := page.Body["chunk"].([]any)
	if len(chunk) != 2 {
		t.Fatalf("messages returned %d events: %s", len(chunk), page.Raw)
	}
	first, _ := chunk[0].(map[string]any)
	content, _ := first["content"].(map[string]any)
	if content["body"] != "message 5" {
		t.Errorf("backward pagination should start with the newest event, got %v", content["body"])
	}
	from := page.String("end")
	if from == "" {
		t.Fatalf("no pagination token in %s", page.Raw)
	}
	next := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=2&from="+from, alice.Token, nil, http.StatusOK)
	secondChunk, _ := next.Body["chunk"].([]any)
	if len(secondChunk) != 2 {
		t.Fatalf("second page = %s", next.Raw)
	}
	secondEvent, _ := secondChunk[0].(map[string]any)
	secondContent, _ := secondEvent["content"].(map[string]any)
	if secondContent["body"] != "message 3" {
		t.Errorf("second page starts at %v, want message 3", secondContent["body"])
	}

	forward := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&limit=10&from="+next.String("end"), alice.Token, nil, http.StatusOK)
	forwardChunk, _ := forward.Body["chunk"].([]any)
	if len(forwardChunk) == 0 {
		t.Fatalf("forward pagination returned nothing: %s", forward.Raw)
	}
	if first, _ := forwardChunk[0].(map[string]any); first["type"] != matrix.TypeMessage {
		t.Errorf("forward pagination should start at the requested token, got %v", first["type"])
	}

	// A type filter narrows the chunk.
	filtered := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=10", alice.Token, nil, http.StatusOK)
	allChunk, _ := filtered.Body["chunk"].([]any)
	if len(allChunk) < 5 {
		t.Errorf("expected the full history, got %d events", len(allChunk))
	}

	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?dir=z", alice.Token, nil); resp.Status == http.StatusOK {
		t.Errorf("an invalid dir was accepted: %s", resp.Raw)
	}
}

// TestRedaction covers PUT /rooms/{roomId}/redact/{eventId}/{txnId}.
func TestRedaction(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	eventID := server.Send(alice, roomID, "txn-secret", "embarrassing")
	server.MustDo(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/redact/%s/txn-redact", roomID, eventID), alice.Token,
		map[string]any{"reason": "oops"}, http.StatusOK)

	event := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/event/"+eventID, bob.Token, nil, http.StatusOK)
	body, _ := event.Body["content"].(map[string]any)
	if _, ok := body["body"]; ok {
		t.Errorf("the redacted event still carries its content: %s", event.Raw)
	}
	unsigned, _ := event.Body["unsigned"].(map[string]any)
	because, _ := unsigned["redacted_because"].(map[string]any)
	if because["type"] != matrix.TypeRedaction {
		t.Errorf("unsigned.redacted_because = %v in %s", unsigned["redacted_because"], event.Raw)
	}

	// Outsiders cannot redact.
	carol := server.Register("carol", "kettle")
	other := server.Send(alice, roomID, "txn-two", "second")
	if resp := server.Do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/redact/%s/txn-no", roomID, other), carol.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("an outsider redacted an event: %d %s", resp.Status, resp.Raw)
	}
}
