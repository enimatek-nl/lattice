package compat

import (
	"net/http"
	"testing"
)

// TestARoomSaysWhoMayInvite is about a permission a room states rather than assumes. The
// specification's two chapters disagree about the level inviting asks for when the room has not
// said: the client-server chapter reads 0, the authorisation chapter - the one that decides whether
// an event may be sent at all - reads invite alongside kick, ban and redact, all 50. The
// implementations enforce 50. A room that creates its levels event without the key therefore means
// one thing to a client and another to the servers that judge its events, so the room states the
// level it actually enforces, and a member with no level in it cannot bring strangers in.
func TestARoomSaysWhoMayInvite(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	carol := server.Register("carol", "dormouse")

	roomID := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token,
		map[string]any{"preset": "private_chat", "name": "the room"}).String("room_id")

	levels := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels", alice.Token, nil)
	if levels.Status != http.StatusOK {
		t.Fatalf("reading the room's power levels: %d %s", levels.Status, levels.Raw)
	}
	if got, _ := levels.Body["invite"].(float64); got != 50 {
		t.Errorf("the room says inviting needs level %v, want 50: %s", levels.Body["invite"], levels.Raw)
	}

	if invite := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}); invite.Status != http.StatusOK {
		t.Fatalf("the creator inviting bob: %d %s", invite.Status, invite.Raw)
	}
	if joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, map[string]any{}); joined.Status != http.StatusOK {
		t.Fatalf("bob joining: %d %s", joined.Status, joined.Raw)
	}

	if invite := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", bob.Token,
		map[string]any{"user_id": carol.UserID}); invite.Status != http.StatusForbidden {
		t.Errorf("bob, who holds no level in the room, inviting carol: %d %s, want 403", invite.Status, invite.Raw)
	}

	// The room can still open the door on purpose: a moderator setting the level to 0 says
	// inviting is nobody's special job, and that is honoured.
	if raised := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels/",
		alice.Token, map[string]any{"invite": 0, "users": map[string]any{alice.UserID: 100, bob.UserID: 0}}); raised.Status != http.StatusOK {
		t.Fatalf("alice opening invites: %d %s", raised.Status, raised.Raw)
	}
	if invite := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", bob.Token,
		map[string]any{"user_id": carol.UserID}); invite.Status != http.StatusOK {
		t.Errorf("bob inviting carol after the room opened invites: %d %s, want 200", invite.Status, invite.Raw)
	}
}
