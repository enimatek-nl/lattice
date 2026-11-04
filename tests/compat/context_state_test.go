package compat

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestContextStateAtEvent checks that the state block of /context is the state the room was
// carrying when the centre event was written, not the state it carries now. A room that renamed
// itself afterwards is the easy case of a question that a forked history makes sharp: the event
// the client is looking at never saw the new name, and showing it there puts words in the room's
// past that it never said.
func TestContextStateAtEvent(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "the first name"})
	eventID := server.Send(alice, roomID, "txn-1", "said under the old name")
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.name",
		alice.Token, map[string]any{"name": "the second name"}, http.StatusOK)

	page := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/context/"+eventID+"?limit=5",
		alice.Token, nil, http.StatusOK)
	// The state of the room as the centre event knew it: the name it was written under.
	names := nameOfState(string(page.Raw))
	if names == "" {
		t.Fatalf("the context of %s carries no room name at all: %s", eventID, page.Raw)
	}
	if names != "the first name" {
		t.Errorf("the context of an event written under %q carries %q: %s", "the first name", names, page.Raw)
	}
	// And the room today still answers the current name, so the fix is a question about the
	// point asked, not a stale cache.
	now := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.name/", alice.Token, nil, http.StatusOK)
	var current map[string]any
	_ = json.Unmarshal([]byte(now.Raw), &current)
	if current["name"] != "the second name" {
		t.Errorf("the room's current name went wrong: %s", now.Raw)
	}
}

// nameOfState answers the room name a /context state block carries.
func nameOfState(raw string) string {
	var page struct {
		State []struct {
			Type     string         `json:"type"`
			Content  map[string]any `json:"content"`
			StateKey *string        `json:"state_key"`
		} `json:"state"`
	}
	_ = json.Unmarshal([]byte(raw), &page)
	for _, event := range page.State {
		if event.Type == matrix.TypeName {
			if name, ok := event.Content["name"].(string); ok {
				return name
			}
		}
	}
	return ""
}
