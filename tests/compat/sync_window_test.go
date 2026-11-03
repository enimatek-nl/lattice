package compat

import (
	"fmt"
	"net/http"
	"testing"
)

// TestAFreshSyncShowsTheEndOfABusyRoom is about which end of a room a client is given when it has
// seen nothing. A timeline limit means "the last N", because a client that is starting up wants the
// conversation as it stands now and a token to walk back from; handing it the room's first events
// and calling the rest "limited" makes a busy room look like it stopped moving long ago.
func TestAFreshSyncShowsTheEndOfABusyRoom(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token,
		map[string]any{"preset": "public_chat"}).String("room_id")

	var firstID, lastID string
	for i := 0; i < 12; i++ {
		sent := server.Do(http.MethodPut,
			fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/t%d", roomID, i), alice.Token,
			map[string]any{"msgtype": "m.text", "body": fmt.Sprintf("message %d", i)})
		if sent.Status != http.StatusOK {
			t.Fatalf("sending message %d: %d %s", i, sent.Status, sent.Raw)
		}
		if i == 0 {
			firstID = sent.String("event_id")
		}
		lastID = sent.String("event_id")
	}

	fresh := server.Do(http.MethodGet, "/_matrix/client/v3/sync", alice.Token, nil)
	timeline := timelineIDs(joinedTimeline(fresh, roomID))
	if !has(timeline, lastID) {
		t.Fatalf("a fresh sync did not carry the newest event of the room (%d events returned), want %s",
			len(timeline), lastID)
	}
	if has(timeline, firstID) {
		t.Errorf("a fresh sync carried the room's oldest event %s although its timeline was limited: %v", firstID, timeline)
	}
	room := timelineRoom(fresh, roomID)
	if limited, _ := room["timeline"].(map[string]any)["limited"].(bool); !limited {
		t.Errorf("the timeline of a room with more events than the limit is not marked limited")
	}
	if prev, _ := room["timeline"].(map[string]any)["prev_batch"].(string); prev == "" {
		t.Errorf("a limited timeline carries no prev_batch, so the client cannot walk back to the rest")
	}

	// Reading on from the token still gives only what came after it.
	token := fresh.String("next_batch")
	next := server.Do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/late", roomID), alice.Token,
		map[string]any{"msgtype": "m.text", "body": "late one"})
	resumed := server.Do(http.MethodGet, "/_matrix/client/v3/sync?since="+token, alice.Token, nil)
	after := timelineIDs(joinedTimeline(resumed, roomID))
	if !has(after, next.String("event_id")) {
		t.Errorf("the sync after the token did not carry the event written since it: %v", after)
	}
	if has(after, lastID) {
		t.Errorf("the sync after the token repeated %s, which the token already covered: %v", lastID, after)
	}
}

// timelineIDs lists the event ids a timeline carries, rather than every id that appears inside it:
// auth_events and prev_events name other events, and a substring match would read them as the
// events themselves.
func timelineIDs(events []any) []string {
	ids := make([]string, 0, len(events))
	for _, raw := range events {
		event, _ := raw.(map[string]any)
		id, _ := event["event_id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// has reports whether a list holds a value.
func has(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// timelineRoom digs one room's join block out of a sync response.
func timelineRoom(sync Response, roomID string) map[string]any {
	rooms, _ := sync.Body["rooms"].(map[string]any)
	join, _ := rooms["join"].(map[string]any)
	room, _ := join[roomID].(map[string]any)
	return room
}
