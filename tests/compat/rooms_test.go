package compat

import (
	"net/http"
	"testing"
)

// TestCreateRoom covers POST /_matrix/client/v3/createRoom and the reads that
// follow from it.
func TestCreateRoom(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	roomID := server.CreateRoom(alice, map[string]any{
		"name":            "Lobby",
		"topic":           "General chatter",
		"preset":          "public_chat",
		"room_alias_name": "lobby",
	})

	joined := server.MustDo(http.MethodGet, "/_matrix/client/v3/joined_rooms", alice.Token, nil, http.StatusOK)
	rooms, _ := joined.Body["joined_rooms"].([]any)
	if len(rooms) != 1 || rooms[0] != roomID {
		t.Fatalf("joined_rooms = %s", joined.Raw)
	}

	alias := server.MustDo(http.MethodGet, "/_matrix/client/v3/directory/room/%23lobby:test.server", "", nil, http.StatusOK)
	if alias.String("room_id") != roomID {
		t.Errorf("alias resolves to %q, want %q", alias.String("room_id"), roomID)
	}

	state := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state", alice.Token, nil, http.StatusOK)
	events, _ := state.Body["state"].([]any)
	types := map[string]bool{}
	for _, raw := range events {
		event, _ := raw.(map[string]any)
		typ, _ := event["type"].(string)
		types[typ] = true
	}
	for _, want := range []string{"m.room.create", "m.room.member", "m.room.power_levels", "m.room.join_rules", "m.room.history_visibility", "m.room.guest_access", "m.room.name", "m.room.topic"} {
		if !types[want] {
			t.Errorf("the initial state is missing %s (got %v)", want, keys(types))
		}
	}

	public := server.MustDo(http.MethodGet, "/_matrix/client/v3/publicRooms", alice.Token, nil, http.StatusOK)
	chunk, _ := public.Body["chunk"].([]any)
	if len(chunk) != 1 {
		t.Fatalf("publicRooms returned %s", public.Raw)
	}
	entry, _ := chunk[0].(map[string]any)
	if entry["name"] != "Lobby" || entry["room_id"] != roomID {
		t.Errorf("public room entry = %v", entry)
	}

	summary := server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/summary", alice.Token, nil, http.StatusOK)
	if count, _ := summary.Body["m.joined_member_count"].(float64); count != 1 {
		t.Errorf("m.joined_member_count = %v (%s)", count, summary.Raw)
	}

	aliases := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/aliases", alice.Token, nil, http.StatusOK)
	list, _ := aliases.Body["aliases"].([]any)
	if len(list) != 1 || list[0] != "#lobby:test.server" {
		t.Errorf("aliases = %s", aliases.Raw)
	}
}

// TestCreateRoomPrivate checks that a private room is not listed publicly.
func TestCreateRoomPrivate(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	server.CreateRoom(alice, map[string]any{"name": "Secret", "preset": "private_chat"})

	public := server.MustDo(http.MethodGet, "/_matrix/client/v3/publicRooms", alice.Token, nil, http.StatusOK)
	if chunk, _ := public.Body["chunk"].([]any); len(chunk) != 0 {
		t.Fatalf("a private room appeared in the directory: %s", public.Raw)
	}
	total, _ := public.Body["total_rooms"].(float64)
	if total != 0 {
		t.Errorf("total_rooms = %v", total)
	}
}

// TestAliasManagement covers PUT, GET and DELETE /_matrix/client/v3/directory/room/{roomAlias}.
func TestAliasManagement(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"name": "Plain"})

	server.MustDo(http.MethodPut, "/_matrix/client/v3/directory/room/%23side:test.server", alice.Token,
		map[string]any{"room_id": roomID}, http.StatusOK)
	got := server.MustDo(http.MethodGet, "/_matrix/client/v3/directory/room/%23side:test.server", "", nil, http.StatusOK)
	if got.String("room_id") != roomID {
		t.Errorf("alias points at %q", got.String("room_id"))
	}

	duplicate := server.Do(http.MethodPut, "/_matrix/client/v3/directory/room/%23side:test.server", alice.Token,
		map[string]any{"room_id": roomID})
	if duplicate.Status == http.StatusOK {
		t.Errorf("creating an existing alias should fail: %s", duplicate.Raw)
	}

	foreign := server.Do(http.MethodPut, "/_matrix/client/v3/directory/room/%23other:test.server", bob.Token,
		map[string]any{"room_id": roomID})
	if foreign.Status == http.StatusOK {
		t.Errorf("a non-member created an alias for a room they are not in: %s", foreign.Raw)
	}

	server.MustDo(http.MethodDelete, "/_matrix/client/v3/directory/room/%23side:test.server", alice.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/directory/room/%23side:test.server", "", nil); resp.Status != http.StatusNotFound {
		t.Errorf("the deleted alias still resolves: %d %s", resp.Status, resp.Raw)
	}
}

// TestJoinByAlias covers POST /_matrix/client/v3/join/{roomIdOrAlias}.
func TestJoinByAlias(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "room_alias_name": "square"})

	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/join/%23square:test.server", bob.Token, nil, http.StatusOK)
	if resp.String("room_id") != roomID {
		t.Fatalf("join returned %s, want %s", resp.Raw, roomID)
	}

	members := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/members", alice.Token, nil, http.StatusOK)
	chunk, _ := members.Body["chunk"].([]any)
	if len(chunk) != 2 {
		t.Fatalf("members = %s", members.Raw)
	}

	joined := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/joined_members", alice.Token, nil, http.StatusOK)
	members2, _ := joined.Body["joined"].(map[string]any)
	if _, ok := members2["@bob:test.server"]; !ok {
		t.Errorf("joined_members = %s", joined.Raw)
	}

	unknown := server.Do(http.MethodPost, "/_matrix/client/v3/join/%23nobody:test.server", bob.Token, nil)
	if unknown.Status != http.StatusNotFound {
		t.Errorf("joining an unknown alias: got %d %s", unknown.Status, unknown.Raw)
	}
}

// keys returns the keys of a set, for failure messages.
func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}
