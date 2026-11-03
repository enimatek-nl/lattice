package compat

import (
	"net/http"
	"testing"
)

// TestAKnockingUserIsToldWhereTheyStand is the knock read back to the person who made it.
// A knock is a question with no answer yet, so it does not arrive as an event in a timeline
// and vanish: it is a standing state, the room's state as the knocker is allowed to see it,
// reported until the room lets them in or turns them away. A client that knocked and is
// shown nothing cannot tell the user that the ask reached the room, and the knocker is left
// wondering whether they pressed the button.
func TestAKnockingUserIsToldWhereTheyStand(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	roomID := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token,
		map[string]any{"preset": "private_chat", "name": "the locked room"}).String("room_id")
	if set := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules", alice.Token,
		map[string]any{"join_rule": "knock"}); set.Status != http.StatusOK {
		t.Fatalf("opening the room to knocking: %d %s", set.Status, set.Raw)
	}
	if knock := server.Do(http.MethodPost, "/_matrix/client/v3/knock/"+roomID, bob.Token,
		map[string]any{"reason": "let me in"}); knock.Status != http.StatusOK {
		t.Fatalf("bob knocking: %d %s", knock.Status, knock.Raw)
	}

	sync := server.Do(http.MethodGet, "/_matrix/client/v3/sync", bob.Token, nil)
	rooms, _ := sync.Body["rooms"].(map[string]any)
	knocked, _ := rooms["knock"].(map[string]any)
	if knocked == nil {
		t.Fatalf("the sync of a user who knocked carries no knock section: %s", sync.Raw)
	}
	entry, _ := knocked[roomID].(map[string]any)
	if entry == nil {
		t.Fatalf("the knock section does not name the room %s: %v", roomID, knocked)
	}
	state, _ := entry["knock_state"].(map[string]any)
	events, _ := state["events"].([]any)
	var found map[string]any
	for _, item := range events {
		event, _ := item.(map[string]any)
		if event["type"] == "m.room.member" && event["state_key"] == bob.UserID {
			found = event
		}
	}
	if found == nil {
		t.Fatalf("the state shown to the knocker does not include their own membership, which is the event that says they asked: %v", events)
	}
	content, _ := found["content"].(map[string]any)
	if content["membership"] != "knock" {
		t.Errorf("the knocker's membership reads %v, want knock: %v", content["membership"], found)
	}
	if content["reason"] != "let me in" {
		t.Errorf("the room was not told why: %v", content)
	}

	// A room that lets the knocker in moves the room out of the knock section: the question
	// has been answered, and a client shown a standing knock next to an invitation cannot
	// tell which of the two is now true.
	if invite := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}); invite.Status != http.StatusOK {
		t.Fatalf("alice letting bob in: %d %s", invite.Status, invite.Raw)
	}
	after := server.Do(http.MethodGet, "/_matrix/client/v3/sync", bob.Token, nil)
	afterRooms, _ := after.Body["rooms"].(map[string]any)
	if still, _ := afterRooms["knock"].(map[string]any); still != nil && still[roomID] != nil {
		t.Errorf("the room is still offered as a knock after bob was invited: %v", afterRooms["knock"])
	}
}
