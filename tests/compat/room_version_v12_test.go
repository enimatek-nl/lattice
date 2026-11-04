package compat

import (
	"net/http"
	"strings"
	"testing"
)

// maxCanonicalJSONInt is the largest integer canonical JSON can name: 2^53-1, the point
// past which a double stops being exact. A power level beyond it is not a level, and a
// room version that implements "infinite" power by picking a bigger number still has to
// refuse it in an event.
const maxCanonicalJSONInt = int64(1<<53 - 1)

// joinRoom joins a room as the session.
func joinRoom(t *testing.T, s *Server, session *Session, roomID string) {
	t.Helper()
	s.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/join", session.Token, map[string]any{}, http.StatusOK)
}

// putState writes a state event the way a client does.
func putState(s *Server, session *Session, roomID, typ, stateKey string, content any) Response {
	return s.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/"+typ+"/"+stateKey, session.Token, content)
}

// stateSnapshot reads a room's full state as the client sees it.
func stateSnapshot(t *testing.T, s *Server, roomID, token string) []map[string]any {
	t.Helper()
	resp := s.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state", token, nil, http.StatusOK)
	events := make([]map[string]any, 0, len(resp.Array))
	for _, raw := range resp.Array {
		event, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("state entry %v is not an object", raw)
		}
		events = append(events, event)
	}
	return events
}

// stateEventOf returns the state entry of a type and key, or nil.
func stateEventOf(events []map[string]any, typ, stateKey string) map[string]any {
	for _, event := range events {
		if event["type"] == typ && event["state_key"] == stateKey {
			return event
		}
	}
	return nil
}

// stateContentOf returns the content of a state entry. GET /rooms/{roomID}/state/{type}/
// {key} answers with the content object itself, not wrapped in an event.
func stateContentOf(t *testing.T, s *Server, roomID, token, typ, stateKey string) map[string]any {
	t.Helper()
	resp := s.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/"+typ+"/"+stateKey, token, nil, http.StatusOK)
	return resp.Body
}

// TestRoomVersion12RoomIDIsTheCreateEventHash is the room version 12 change in one place:
// the room is named by a hash of the event that created it, so the id carries no server
// name, the create event carries no room id, and every other event carries that hash.
func TestRoomVersion12RoomIDIsTheCreateEventHash(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	roomID := server.CreateRoom(alice, map[string]any{
		"room_version": "12",
		"preset":       "public_chat",
		"name":         "Hashed",
	})

	if strings.Contains(roomID, ":") {
		t.Errorf("room id %s names a server; a room version 12 room is named by a hash", roomID)
	}
	if !strings.HasPrefix(roomID, "!") || len(roomID) != 44 {
		t.Errorf("room id %s is not ! plus 43 characters of URL-safe unpadded base64", roomID)
	}
	for _, c := range roomID[1:] {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			t.Errorf("room id %s holds %q, outside the URL-safe base64 alphabet", roomID, c)
		}
	}

	events := stateSnapshot(t, server, roomID, alice.Token)
	create := stateEventOf(events, "m.room.create", "")
	if create == nil {
		t.Fatalf("the room has no m.room.create event: %v", events)
	}
	createEventID, _ := create["event_id"].(string)
	if createEventID != "$"+roomID[1:] {
		t.Errorf("the create event is %s, want %s: the room id and the create event id are the same hash",
			createEventID, "$"+roomID[1:])
	}
	// The stored event has no room_id - it cannot, since the id is its own hash - but every
	// client read that is not /sync puts it back, because a client holding one room's events
	// still has to be able to say which room they are.
	if got, _ := create["room_id"].(string); got != roomID {
		t.Errorf("/state hands out the create event with room_id %q, want %q", got, roomID)
	}
	member := stateEventOf(events, "m.room.member", alice.UserID)
	if member == nil {
		t.Fatalf("the room has no membership event for its creator")
	}
	if got, _ := member["room_id"].(string); got != roomID {
		t.Errorf("an ordinary event carries room_id %q, want %q", got, roomID)
	}

	// The other reads the specification asks the same question of.
	for _, read := range []struct {
		name string
		path string
	}{
		{"event", "/_matrix/client/v3/rooms/" + roomID + "/event/" + createEventID},
		{"context", "/_matrix/client/v3/rooms/" + roomID + "/context/" + createEventID},
		{"messages", "/_matrix/client/v3/rooms/" + roomID + "/messages?dir=b&limit=50"},
	} {
		resp := server.MustDo(http.MethodGet, read.path, alice.Token, nil, http.StatusOK)
		if !strings.Contains(string(resp.Raw), roomID) {
			t.Errorf("%s does not name the room %s: %s", read.name, roomID, resp.Raw)
		}
	}

	// /sync is the one read that leaves room ids out everywhere, so the create event cannot
	// be asked to carry one there.
	sync := server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", alice.Token, nil, http.StatusOK)
	rooms, _ := sync.Body["rooms"].(map[string]any)
	join, _ := rooms["join"].(map[string]any)
	joined, _ := join[roomID].(map[string]any)
	timeline, _ := joined["timeline"].(map[string]any)
	chunk, _ := timeline["chunk"].([]any)
	seenCreate := false
	for _, raw := range chunk {
		event, _ := raw.(map[string]any)
		if event["type"] == "m.room.create" {
			seenCreate = true
			if _, has := event["room_id"]; has {
				t.Errorf("/sync hands out the create event with a room_id: %v", event)
			}
		}
	}
	if !seenCreate && len(chunk) > 0 {
		t.Errorf("/sync's timeline for the room holds %d events and none of them is the create event", len(chunk))
	}

	// A room version 11 room, beside it, still says who made its room in the id itself.
	old := server.CreateRoom(alice, map[string]any{"room_version": "11"})
	if !strings.HasSuffix(old, ":test.server") {
		t.Errorf("a room version 11 room id %s lost its server name", old)
	}
}

// TestRoomVersion12Creators is the second half of MSC4289 as a client sees it: the room
// lists nobody in its power levels because its creators hold a level the event cannot
// name, and no level written by hand - not the largest one JSON can spell - outranks them.
func TestRoomVersion12Creators(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")

	roomID := server.CreateRoom(alice, map[string]any{
		"room_version": "12",
		"preset":       "public_chat",
		"invite":       []any{bob.UserID},
	})
	joinRoom(t, server, bob, roomID)

	content := stateContentOf(t, server, roomID, alice.Token, "m.room.power_levels", "")
	users, _ := content["users"].(map[string]any)
	if users == nil {
		users = map[string]any{}
	}
	if len(users) != 0 {
		t.Errorf("a room version 12 power levels event names its creators: %v", users)
	}
	events, _ := content["events"].(map[string]any)
	if got, _ := events["m.room.tombstone"].(float64); got != 150 {
		t.Errorf("m.room.tombstone needs power level %v, want 150", got)
	}

	// A creator cannot be written into the levels at all.
	resp := putState(server, alice, roomID, "m.room.power_levels", "",
		map[string]any{"users": map[string]any{alice.UserID: 100}})
	if resp.Status != http.StatusBadRequest {
		t.Errorf("naming the creator in the power levels: status %d body %s, want 400", resp.Status, resp.Raw)
	}

	// The room cannot be created with those levels either.
	bad := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token, map[string]any{
		"room_version": "12",
		"power_level_content_override": map[string]any{
			"users": map[string]any{alice.UserID: 100},
		},
	})
	if bad.Status != http.StatusBadRequest {
		t.Errorf("createRoom with the creator in the overrides: status %d body %s, want 400", bad.Status, bad.Raw)
	}

	// A level beyond canonical JSON is not a level.
	far := putState(server, alice, roomID, "m.room.power_levels", "",
		map[string]any{"users": map[string]any{bob.UserID: float64(1 << 53)}})
	if far.Status != http.StatusBadRequest {
		t.Errorf("a power level of 2^53: status %d body %s, want 400", far.Status, far.Raw)
	}

	// The creator outranks an administrator at the largest level there is, and the other way
	// round does not work.
	putState(server, alice, roomID, "m.room.power_levels", "", map[string]any{
		"users": map[string]any{bob.UserID: maxCanonicalJSONInt},
	})
	kick := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/kick", alice.Token,
		map[string]any{"user_id": bob.UserID})
	if kick.Status != http.StatusOK {
		t.Errorf("the creator kicking an administrator at %d: status %d body %s, want 200",
			maxCanonicalJSONInt, kick.Status, kick.Raw)
	}

	second := server.CreateRoom(alice, map[string]any{
		"room_version": "12",
		"preset":       "public_chat",
		"invite":       []any{bob.UserID},
	})
	joinRoom(t, server, bob, second)
	putState(server, alice, second, "m.room.power_levels", "", map[string]any{
		"users": map[string]any{bob.UserID: maxCanonicalJSONInt},
	})
	revoked := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+second+"/kick", bob.Token,
		map[string]any{"user_id": alice.UserID})
	if revoked.Status != http.StatusForbidden || revoked.ErrCode() != "M_FORBIDDEN" {
		t.Errorf("an administrator at %d kicking the creator: status %d body %s, want 403 M_FORBIDDEN",
			maxCanonicalJSONInt, revoked.Status, revoked.Raw)
	}
}

// TestRoomVersion12AdditionalCreators is the part of MSC4289 that lets more than one
// person make a room: the array is checked when the room is made, and everybody it names
// holds the same unnameable level as the sender of the create event.
func TestRoomVersion12AdditionalCreators(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	charlie := server.Register("charlie", "wheels")

	roomID := server.CreateRoom(alice, map[string]any{
		"room_version": "12",
		"preset":       "public_chat",
		"creation_content": map[string]any{
			"additional_creators": []any{bob.UserID},
		},
	})
	if got := stateContentOf(t, server, roomID, alice.Token, "m.room.create", ""); len(got) == 0 {
		t.Fatalf("the create event has no content")
	} else {
		list, _ := got["additional_creators"].([]any)
		if len(list) != 1 || list[0] != bob.UserID {
			t.Errorf("additional_creators = %v, want [%s]", list, bob.UserID)
		}
	}
	joinRoom(t, server, bob, roomID)

	// A creator does the privileged things without being given a level.
	name := putState(server, bob, roomID, "m.room.name", "", map[string]any{"name": "Bob named it"})
	if name.Status != http.StatusOK {
		t.Errorf("an additional creator setting the room name: status %d body %s, want 200", name.Status, name.Raw)
	}
	// And a creator cannot be kicked by another creator, or listed in the levels.
	kick := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/kick", alice.Token,
		map[string]any{"user_id": bob.UserID})
	if kick.Status != http.StatusForbidden || kick.ErrCode() != "M_FORBIDDEN" {
		t.Errorf("one creator kicking another: status %d body %s, want 403 M_FORBIDDEN", kick.Status, kick.Raw)
	}
	levels := putState(server, alice, roomID, "m.room.power_levels", "",
		map[string]any{"users": map[string]any{bob.UserID: 100}})
	if levels.Status != http.StatusBadRequest {
		t.Errorf("naming an additional creator in the power levels: status %d body %s, want 400",
			levels.Status, levels.Raw)
	}

	// The array is checked where it is written.
	for _, bad := range []any{
		"not-an-array",
		[]any{"@foo:example.com", 42},
		[]any{"@foo:example.com", "not-a-user-id"},
		[]any{"@invalid:dom$ain$.com"},
	} {
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token, map[string]any{
			"room_version":     "12",
			"preset":           "public_chat",
			"creation_content": map[string]any{"additional_creators": bad},
		})
		if resp.Status != http.StatusBadRequest {
			t.Errorf("additional_creators %v: status %d body %s, want 400", bad, resp.Status, resp.Raw)
		}
	}
	good := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token, map[string]any{
		"room_version":     "12",
		"preset":           "public_chat",
		"creation_content": map[string]any{"additional_creators": []any{"@foo:example.com", "@bar:baz.code"}},
	})
	if good.Status != http.StatusOK {
		t.Errorf("a well spelled additional_creators: status %d body %s, want 200", good.Status, good.Raw)
	}

	// The people a createRoom call invites are makers of the room too: an invited user must
	// be able to administer the room they were invited into without waiting for a level.
	dm := server.CreateRoom(alice, map[string]any{
		"room_version": "12",
		"preset":       "trusted_private_chat",
		"is_direct":    true,
		"invite":       []any{bob.UserID},
		"creation_content": map[string]any{
			"additional_creators": []any{charlie.UserID},
		},
	})
	created := stateContentOf(t, server, dm, alice.Token, "m.room.create", "")
	list, _ := created["additional_creators"].([]any)
	seen := map[string]bool{}
	for _, entry := range list {
		if user, ok := entry.(string); ok {
			seen[user] = true
		}
	}
	for _, want := range []string{bob.UserID, charlie.UserID} {
		if !seen[want] {
			t.Errorf("additional_creators %v is missing the invited %s", list, want)
		}
	}
}

// TestRoomVersion12CannotSendACreateEvent: a room already made cannot be re-made. Room
// version 12 says so twice over - the event would have to name a room it is defining, and
// the room's name comes from the hash of the one event that is already there.
func TestRoomVersion12CannotSendACreateEvent(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	for _, version := range []string{"11", "12"} {
		roomID := server.CreateRoom(alice, map[string]any{"room_version": version})
		resp := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.create",
			alice.Token, map[string]any{"room_version": version, "entropy": 100})
		if resp.Status != http.StatusBadRequest {
			t.Errorf("sending m.room.create in a version %s room: status %d body %s, want 400",
				version, resp.Status, resp.Raw)
		}
	}
}

// TestRoomVersion12Upgrade covers the successor room: it is named by a hash of its own
// create event, its create event points back at the old room without claiming what the old
// room's last event was, and the levels it inherits say nothing about its creators.
func TestRoomVersion12Upgrade(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	charlie := server.Register("charlie", "wheels")

	old := server.CreateRoom(alice, map[string]any{"room_version": "11", "preset": "public_chat", "name": "Before"})
	joinRoom(t, server, bob, old)
	putState(server, alice, old, "m.room.power_levels", "", map[string]any{
		"users": map[string]any{bob.UserID: 100, charlie.UserID: 50},
	})

	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+old+"/upgrade", bob.Token,
		map[string]any{
			"new_version":         "12",
			"additional_creators": []any{charlie.UserID},
		}, http.StatusOK)
	next := resp.String("replacement_room")
	if strings.Contains(next, ":") || len(next) != 44 {
		t.Errorf("the successor room %s is not named by a hash", next)
	}
	events := stateSnapshot(t, server, next, bob.Token)
	create := stateEventOf(events, "m.room.create", "")
	if create == nil {
		t.Fatalf("the successor room has no create event")
	}
	if id, _ := create["event_id"].(string); id != "$"+next[1:] {
		t.Errorf("the successor's create event is %s, want the hash the room is named by", id)
	}
	content, _ := create["content"].(map[string]any)
	list, _ := content["additional_creators"].([]any)
	if len(list) != 1 || list[0] != charlie.UserID {
		t.Errorf("additional_creators = %v, want [%s]", list, charlie.UserID)
	}
	predecessor, _ := content["predecessor"].(map[string]any)
	if predecessor == nil {
		t.Fatalf("the successor's create event names no predecessor: %v", content)
	}
	if predecessor["room_id"] != old {
		t.Errorf("predecessor.room_id = %v, want %s", predecessor["room_id"], old)
	}
	if _, has := predecessor["event_id"]; has {
		t.Errorf("predecessor.event_id is set to %v; a room version 12 room does not inherit a claimed last event",
			predecessor["event_id"])
	}

	levels := stateContentOf(t, server, next, bob.Token, "m.room.power_levels", "")
	users, _ := levels["users"].(map[string]any)
	for _, creator := range []string{bob.UserID, charlie.UserID} {
		if _, named := users[creator]; named {
			t.Errorf("the successor's power levels name %s, who is a creator of it: %v", creator, users)
		}
	}
	if got, _ := users[charlie.UserID]; got != nil {
		t.Errorf("charlie kept a level in a room that made them a creator")
	}
}
