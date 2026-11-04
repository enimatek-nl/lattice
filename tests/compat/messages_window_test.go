package compat

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestMessagesWindow checks that /messages honours `to` as well as `from`. The two tokens name a
// window; a page that ignores the far end hands the client the rest of the room as well, and a
// client that asked for one stretch of history to render is given the room instead.
//
// The far bound pairs with the cursor the page itself reports: a page's `end` names the boundary it
// consumed, so asking for a window up to that boundary yields that page and nothing beyond it.
func TestMessagesWindow(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	first := server.Send(alice, roomID, "txn-1", "first")
	second := server.Send(alice, roomID, "txn-2", "second")
	third := server.Send(alice, roomID, "txn-3", "third")

	// A page of two, walked backwards from the room's end: it names both of its own bounds.
	next := server.MustSync(alice, "?timeout=0").NextBatch
	page := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&from="+next+"&limit=2",
		alice.Token, nil, http.StatusOK)
	var window struct {
		Start string `json:"start"`
		End   string `json:"end"`
		Chunk []struct {
			EventID string `json:"event_id"`
		} `json:"chunk"`
	}
	_ = json.Unmarshal([]byte(page.Raw), &window)
	if window.Start == "" || window.End == "" || len(window.Chunk) < 2 {
		t.Fatalf("a page of two does not name its bounds: %s", page.Raw)
	}

	// The same window asked for by name stops at the same bound: the newest two events, and the
	// older one the page did not reach stays out.
	same := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&from="+next+"&to="+window.End+"&limit=50",
		alice.Token, nil, http.StatusOK)
	ids := chunkIDs(string(same.Raw))
	for _, want := range []string{first, second} {
		if !containsID(ids, want) {
			t.Errorf("the window %s..%s is missing %s: %s", window.Start, window.End, want, same.Raw)
		}
	}
	if containsID(ids, third) {
		t.Errorf("the window ran past its far bound and returned %s: %s", third, same.Raw)
	}

	// Without the far bound the same walk keeps going past it.
	open := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&from="+next+"&limit=50",
		alice.Token, nil, http.StatusOK)
	if len(chunkIDs(string(open.Raw))) <= len(chunkIDs(string(same.Raw))) {
		t.Errorf("the bounded page is not shorter than the unbounded one: %s", open.Raw)
	}
}

func chunkIDs(raw string) []string {
	var page struct {
		Chunk []struct {
			EventID string `json:"event_id"`
		} `json:"chunk"`
	}
	_ = json.Unmarshal([]byte(raw), &page)
	ids := []string{}
	for _, event := range page.Chunk {
		ids = append(ids, event.EventID)
	}
	return ids
}
