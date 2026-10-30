package compat

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestInviteCoversSync covers POST /rooms/{roomId}/invite and the invite
// section of a sync.
func TestInviteCoversSync(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"name": "Invite me", "preset": "private_chat"})

	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, http.StatusOK)

	synced, resp := server.Sync(bob, "?timeout=0")
	invite, ok := synced.Rooms.Invite[roomID]
	if !ok {
		t.Fatalf("the invite is missing from sync: %s", resp.Raw)
	}
	if len(invite.InviteState.Events) == 0 {
		t.Errorf("the invite state is empty: %+v", invite)
	}

	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/join", bob.Token, nil, http.StatusOK)
	joined := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/joined_members", bob.Token, nil, http.StatusOK)
	members, _ := joined.Body["joined"].(map[string]any)
	if _, ok := members[alice.UserID]; !ok {
		t.Fatalf("joined_members after joining = %s", joined.Raw)
	}

	// Inviting a third party without permission is refused.
	carol := server.Register("carol", "kettle")
	resp = server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", carol.Token,
		map[string]any{"user_id": "@nobody:test.server"})
	if resp.Status != http.StatusForbidden {
		t.Errorf("a stranger invited someone: %d %s", resp.Status, resp.Raw)
	}
}

// TestPrivateRoomRefusesStrangers checks the invite join rule.
func TestPrivateRoomRefusesStrangers(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})

	resp := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil)
	if resp.Status != http.StatusForbidden {
		t.Fatalf("a stranger joined a private room: %d %s", resp.Status, resp.Raw)
	}
}

// TestKnockRoom covers POST /rooms/{roomId}/knock.
func TestKnockRoom(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "join_rules": "knock"})

	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/knock", bob.Token,
		map[string]any{"reason": "please let me in"}, http.StatusOK)
	if resp.String("room_id") != roomID {
		t.Fatalf("knock returned %s", resp.Raw)
	}
	members := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/members?membership=knock", alice.Token, nil, http.StatusOK)
	chunk, _ := members.Body["chunk"].([]any)
	if len(chunk) != 1 {
		t.Fatalf("knocked members = %s", members.Raw)
	}

	// A knock room refuses a plain join.
	carol := server.Register("carol", "kettle")
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, carol.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("join of a knock room: got %d %s, want 403", resp.Status, resp.Raw)
	}
}

// TestKickBanAndUnban covers the moderation endpoints.
func TestKickBanAndUnban(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "room_alias_name": "moderated"})

	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/kick", alice.Token,
		map[string]any{"user_id": bob.UserID, "reason": "noise"}, http.StatusOK)

	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil); resp.Status != http.StatusOK {
		t.Fatalf("a kicked user should be able to rejoin: %d %s", resp.Status, resp.Raw)
	}

	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/ban", alice.Token,
		map[string]any{"user_id": bob.UserID, "reason": "more noise"}, http.StatusOK)
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil); resp.Status != http.StatusForbidden {
		t.Fatalf("a banned user rejoined: %d %s", resp.Status, resp.Raw)
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/unban", alice.Token,
		map[string]any{"user_id": bob.UserID}, http.StatusOK)
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil); resp.Status != http.StatusOK {
		t.Fatalf("an unbanned user cannot join: %d %s", resp.Status, resp.Raw)
	}

	// A plain member may not moderate.
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/kick", bob.Token,
		map[string]any{"user_id": alice.UserID}); resp.Status != http.StatusForbidden {
		t.Errorf("a plain member kicked someone: %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/joined_members", bob.Token, nil); resp.Status != http.StatusOK {
		t.Fatalf("the member lost the room: %d %s", resp.Status, resp.Raw)
	}
}

// TestLeaveAndForget covers POST /rooms/{roomId}/leave and /forget.
func TestLeaveAndForget(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/leave", bob.Token, nil, http.StatusOK)

	joined := server.MustDo(http.MethodGet, "/_matrix/client/v3/joined_rooms", bob.Token, nil, http.StatusOK)
	if rooms, _ := joined.Body["joined_rooms"].([]any); len(rooms) != 0 {
		t.Errorf("the left room is still joined: %s", joined.Raw)
	}
	all := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/members?membership=*", alice.Token, nil, http.StatusOK)
	chunk, _ := all.Body["chunk"].([]any)
	if len(chunk) != 2 {
		t.Errorf("members with membership=* = %s", all.Raw)
	}
	defaults := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/members", alice.Token, nil, http.StatusOK)
	if chunk, _ := defaults.Body["chunk"].([]any); len(chunk) != 1 {
		t.Errorf("the default membership filter is not join: %s", defaults.Raw)
	}

	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/forget", bob.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("rejoining after a forget failed: %d %s", resp.Status, resp.Raw)
	}
}

// TestPowerLevels covers writing m.room.power_levels and the checks that read
// it.
func TestPowerLevels(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	carol := server.Register("carol", "kettle")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	// The specification lets any member invite by default, so raise the
	// invite level before checking the gate.
	content := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels", alice.Token, nil, http.StatusOK)
	var levels map[string]any
	if err := json.Unmarshal(content.Raw, &levels); err != nil {
		t.Fatalf("decoding power levels: %v (%s)", err, content.Raw)
	}
	levels["invite"] = 50
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels/", alice.Token, levels, http.StatusOK)

	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", bob.Token,
		map[string]any{"user_id": carol.UserID}); resp.Status != http.StatusForbidden {
		t.Fatalf("a plain member invited someone: %d %s", resp.Status, resp.Raw)
	}
	users, _ := levels["users"].(map[string]any)
	users[bob.UserID] = 50
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels/", alice.Token, levels, http.StatusOK)

	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", bob.Token,
		map[string]any{"user_id": carol.UserID}); resp.Status != http.StatusOK {
		t.Fatalf("a moderator could not invite: %d %s", resp.Status, resp.Raw)
	}

	// Downgrading the owner is refused.
	levels["users"].(map[string]any)[alice.UserID] = 0
	if resp := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels/", bob.Token, levels); resp.Status == http.StatusOK {
		t.Errorf("a moderator reset the owner's power level: %s", resp.Raw)
	}
}

// TestInviteThirdPartySigned checks that a third-party invite is refused instead
// of silently inviting nobody: Lattice does not exchange signatures with identity
// servers.
func TestInviteThirdPartySigned(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})

	resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"third_party_signed": map[string]any{"token": "identity-token", "tail": "signature"}})
	if resp.Status != http.StatusBadRequest || resp.ErrCode() != string(matrix.ErrCodeInvalidParam) {
		t.Errorf("third-party invite: got %d %s, want 400 M_INVALID_PARAM", resp.Status, resp.Raw)
	}

	// An ordinary invite keeps working.
	bob := server.Register("bob", "rubberduck")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, http.StatusOK)
}
