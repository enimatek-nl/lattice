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
	events := state.Array
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

// TestRoomUpgrade covers POST /rooms/{roomId}/upgrade: the old room ends up with
// a tombstone that names its successor, and the successor carries the
// predecessor and the requested room version.
func TestRoomUpgrade(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{
		"room_version": "1",
		"name":         "the old one",
		"invite":       []string{bob.UserID},
	})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, map[string]any{}, http.StatusOK)

	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/upgrade", alice.Token,
		map[string]any{"new_version": "11", "reason": "modernise"}, http.StatusOK)
	successor, _ := resp.Body["replacement_room"].(string)
	if successor == "" || successor == roomID {
		t.Fatalf("replacement_room = %q (%s)", successor, resp.Raw)
	}

	tombstone := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.tombstone",
		alice.Token, nil, http.StatusOK)
	if tombstone.String("replacement_room") != successor {
		t.Errorf("the tombstone names %v as its replacement, want %s (%s)",
			tombstone.Body["replacement_room"], successor, tombstone.Raw)
	}
	if tombstone.String("reason") != "modernise" {
		t.Errorf("tombstone reason = %q, want modernise", tombstone.String("reason"))
	}

	create := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+successor+"/state/m.room.create",
		alice.Token, nil, http.StatusOK)
	if create.String("room_version") != "11" {
		t.Errorf("the successor is room version %q, want 11 (%s)", create.String("room_version"), create.Raw)
	}
	predecessor, _ := create.Body["predecessor"].(map[string]any)
	if predecessor == nil || predecessor["room_id"] != roomID {
		t.Errorf("the successor has no predecessor pointing at %s: %s", roomID, create.Raw)
	}

	// Ordinary state is carried over. Membership is not: the client invites the
	// members of the old room into the new one.
	name := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+successor+"/state/m.room.name",
		alice.Token, nil, http.StatusOK)
	if name.String("name") != "the old one" {
		t.Errorf("the successor is named %q", name.String("name"))
	}
	members := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+successor+"/members", alice.Token,
		nil, http.StatusOK)
	chunk, _ := members.Body["chunk"].([]any)
	if len(chunk) != 1 {
		t.Errorf("the successor starts with %d members, want only the creator: %s", len(chunk), members.Raw)
	}

	// A version this server does not know is refused.
	bad := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/upgrade", alice.Token,
		map[string]any{"new_version": "com.example.version"})
	if bad.Status != http.StatusBadRequest || bad.ErrCode() != "M_INCOMPATIBLE_ROOM_VERSION" {
		t.Errorf("upgrading to an unknown version: got %d %s", bad.Status, bad.Raw)
	}

	// A plain member may not upgrade a room they do not manage.
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/upgrade", bob.Token,
		map[string]any{"new_version": "11"}); resp.Status != http.StatusForbidden {
		t.Errorf("bob upgraded a room he does not manage: %d %s", resp.Status, resp.Raw)
	}

	// The new version is required. Defaulting it to the room's own version would
	// quietly make a copy of the room under a name nobody asked for.
	missing := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+successor+"/upgrade", alice.Token,
		map[string]any{})
	if missing.Status != http.StatusBadRequest || missing.ErrCode() != "M_INVALID_PARAM" {
		t.Errorf("upgrading without a version: got %d %s, want 400 M_INVALID_PARAM",
			missing.Status, missing.Raw)
	}
	// The same room cannot be replaced twice.
	double := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/upgrade", alice.Token,
		map[string]any{"new_version": "11"})
	if double.Status != http.StatusForbidden {
		t.Errorf("upgrading an already replaced room: got %d %s, want 403", double.Status, double.Raw)
	}

	// A replaced room is closed. The specification asks the server to raise the
	// power levels of the old room as part of the upgrade, so the lock is visible
	// in the state and survives into whatever reads it next.
	levels := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels",
		alice.Token, nil, http.StatusOK)
	if numberValue(levels.Body["events_default"]) < 50 || numberValue(levels.Body["invite"]) < 50 {
		t.Errorf("the old room still lets members send and invite: %s", levels.Raw)
	}

	// Events are refused even for the room's creator: the tombstone says the room
	// is not intended to be used any further.
	closed := server.Do(http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/after-close", alice.Token,
		map[string]any{"msgtype": "m.text", "body": "still talking here?"})
	if closed.Status != http.StatusForbidden || closed.ErrCode() != "M_FORBIDDEN" {
		t.Errorf("sending to a replaced room: got %d %s, want 403 M_FORBIDDEN", closed.Status, closed.Raw)
	}
	state := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic/",
		alice.Token, map[string]any{"topic": "renamed after the upgrade"})
	if state.Status != http.StatusForbidden {
		t.Errorf("changing state in a replaced room: got %d %s, want 403", state.Status, state.Raw)
	}
	// Membership is a different path, so people can still leave a room that has
	// been closed.
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/leave", bob.Token,
		map[string]any{}, http.StatusOK); resp.Status != http.StatusOK {
		t.Errorf("leaving a replaced room: %d %s", resp.Status, resp.Raw)
	}
	// The replacement room is open as normal.
	if id := server.Send(alice, successor, "in-the-new-room", "talking here now"); id == "" {
		t.Errorf("the replacement room refuses messages")
	}
}

// numberValue reads a JSON number that arrived as any.
func numberValue(value any) float64 {
	if number, ok := value.(float64); ok {
		return number
	}
	return 0
}

// keys returns the keys of a set, for failure messages.
func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}
