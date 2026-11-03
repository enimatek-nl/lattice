package compat

import (
	"net/http"
	"testing"
)

func TestZZDebugKnockTimeline(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	david := server.Register("david", "knocker")
	roomID := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token,
		map[string]any{"preset": "private_chat", "room_version": "7"}).String("room_id")
	server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token, map[string]any{"user_id": david.UserID})
	server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, david.Token, map[string]any{})
	server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules", alice.Token, map[string]any{"join_rule": "knock"})
	server.Do(http.MethodPost, "/_matrix/client/v3/knock/"+roomID, bob.Token, map[string]any{"reason": "LET ME IN"})
	server.Do(http.MethodPost, "/_matrix/client/v3/knock/"+roomID, bob.Token, map[string]any{"reason": "knock knock"})
	for round := 0; round < 2; round++ {
		sync := server.Do(http.MethodGet, "/_matrix/client/v3/sync", alice.Token, nil)
		rooms, _ := sync.Body["rooms"].(map[string]any)
		join, _ := rooms["join"].(map[string]any)
		entry, _ := join[roomID].(map[string]any)
		tl, _ := entry["timeline"].(map[string]any)
		events, _ := tl["events"].([]any)
		t.Logf("round %d: timeline has %d events, limited=%v", round, len(events), tl["limited"])
		for i, item := range events {
			ev, _ := item.(map[string]any)
			content, _ := ev["content"].(map[string]any)
			if ev["type"] == "m.room.member" {
				t.Logf("  [%d] member %v membership=%v reason=%v", i, ev["state_key"], content["membership"], content["reason"])
			}
		}
	}
}
