package compat

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestMembersAtPoint checks /members?at=: the members a room had at a point, which is the
// question a client paging a room asks when it holds a pagination token and wants to know who
// the room was made of then.
func TestMembersAtPoint(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.Send(alice, roomID, "txn-1", "hello")
	synced, resp := server.Sync(alice, "?timeout=0")
	if synced.NextBatch == "" {
		t.Fatalf("no next_batch in %s", resp.Raw)
	}
	prev := synced.Rooms.Join[roomID].Timeline.PrevBatch
	if prev == "" {
		t.Fatalf("the timeline answers no prev_batch to read the room as of: %s", resp.Raw)
	}
	bob := server.Register("bob", "mirror")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/join", bob.Token, nil, http.StatusOK)

	// The token sits at the room's own beginning. The room is not empty there: who made it and
	// that they are in it is the state the room was created with, and a client reading the room
	// as of its first moment has to be told that, or the room it is looking at has no members at
	// all.
	asOf := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/members?at="+prev, alice.Token, nil, http.StatusOK)
	if got := memberKeys(string(asOf.Raw)); got != "@alice:test.server" {
		t.Errorf("the room at %s holds %q, want only its creator: %s", prev, got, asOf.Raw)
	}
	// Somebody who joined later is not in the room at that point, however full it is now.
	if strings.Contains(string(asOf.Raw), bob.UserID) {
		t.Errorf("the room at %s already holds %s, who joined after it: %s", prev, bob.UserID, asOf.Raw)
	}
	now := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/members", alice.Token, nil, http.StatusOK)
	if got := memberKeys(string(now.Raw)); !strings.Contains(got, alice.UserID) || !strings.Contains(got, bob.UserID) {
		t.Errorf("the room now holds %q, want both members: %s", got, now.Raw)
	}
}

// memberKeys names the state keys of the member events a /members answer carries.
func memberKeys(raw string) string {
	var page struct {
		Chunk []matrix.Event `json:"chunk"`
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		return "<unparseable: " + err.Error() + ">"
	}
	keys := []string{}
	for _, event := range page.Chunk {
		if event.StateKey != nil {
			keys = append(keys, *event.StateKey)
		}
	}
	return strings.Join(keys, " ")
}
