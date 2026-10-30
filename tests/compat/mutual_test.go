package compat

import (
	"net/http"
	"net/url"
	"testing"
)

// roomIDList reads the "rooms" array of a mutual or direct room listing.
func roomIDList(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["rooms"].([]any)
	if !ok {
		t.Fatalf("no rooms array in %v", body)
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		id, ok := entry.(string)
		if !ok {
			t.Fatalf("room entry %v is not a room id", entry)
		}
		out = append(out, id)
	}
	return out
}

// TestMutualRooms covers GET /v1/mutual_rooms, the MSC2666 listing of rooms two
// users share.
func TestMutualRooms(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	carol := server.Register("carol", "kettle")

	shared := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Shared"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+url.PathEscape(shared), bob.Token, nil, http.StatusOK)
	other := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Also shared"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+url.PathEscape(other), bob.Token, nil, http.StatusOK)
	for _, roomID := range []string{shared, other} {
		server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+url.PathEscape(roomID), carol.Token, nil, http.StatusOK)
	}
	solo := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Solo"})
	// A direct message between the two users stays out of the listing.
	dm := server.CreateRoom(alice, map[string]any{"invite": []string{bob.UserID}, "is_direct": true})

	urls := url.Values{"user_id": {bob.UserID}, "room_id": {shared}}
	resp := server.MustDo(http.MethodGet, "/_matrix/client/v1/mutual_rooms?"+urls.Encode(),
		alice.Token, nil, http.StatusOK)
	rooms := roomIDList(t, resp.Body)
	if len(rooms) != 2 {
		t.Fatalf("mutual rooms = %v, want the two rooms they share", rooms)
	}
	for _, want := range []string{shared, other} {
		if !containsRoom(rooms, want) {
			t.Errorf("%s is missing from %v", want, rooms)
		}
	}
	if containsRoom(rooms, dm) || containsRoom(rooms, solo) {
		t.Errorf("the listing leaks a private room: %v", rooms)
	}

	// The anchor room is used for authorization: the requester must be able to
	// read it and the target must be part of it.
	anchor := url.Values{"user_id": {bob.UserID}, "room_id": {solo}}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/mutual_rooms?"+anchor.Encode(),
		alice.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("bob is not in the anchor room: got %d %s, want 403", resp.Status, resp.Raw)
	}
	outsider := url.Values{"user_id": {alice.UserID}, "room_id": {solo}}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/mutual_rooms?"+outsider.Encode(),
		carol.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("carol may not use the anchor room: got %d %s, want 403", resp.Status, resp.Raw)
	}

	// Without an anchor room the listing simply compares the two memberships.
	bare := url.Values{"user_id": {carol.UserID}}
	resp = server.MustDo(http.MethodGet, "/_matrix/client/v1/mutual_rooms?"+bare.Encode(),
		alice.Token, nil, http.StatusOK)
	if len(roomIDList(t, resp.Body)) != 2 {
		t.Errorf("alice and carol share the two public rooms: %s", resp.Raw)
	}
	// Bob and Carol share the two public rooms.
	if resp := server.MustDo(http.MethodGet, "/_matrix/client/v1/mutual_rooms?"+bare.Encode(),
		bob.Token, nil, http.StatusOK); len(roomIDList(t, resp.Body)) != 2 {
		t.Errorf("bob and carol: %s", resp.Raw)
	}

	// Bad requests.
	for name, values := range map[string]url.Values{
		"no target":   {},
		"himself":     {"user_id": {alice.UserID}},
		"unknown":     {"user_id": {"@nobody:test.server"}},
		"remote user": {"user_id": {"@bob:elsewhere.example"}},
	} {
		if resp := server.Do(http.MethodGet, "/_matrix/client/v1/mutual_rooms?"+values.Encode(),
			alice.Token, nil); resp.Status != http.StatusBadRequest && resp.Status != http.StatusNotFound {
			t.Errorf("%s: got %d %s, want 400 or 404", name, resp.Status, resp.Raw)
		}
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/mutual_rooms?"+bare.Encode(), "", nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated mutual rooms: got %d, want 401", resp.Status)
	}
}

// TestDirectRooms covers GET /v1/users/{userId}/rooms, the direct message
// listing derived from the m.direct account data.
func TestDirectRooms(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	dm := server.CreateRoom(alice, map[string]any{"invite": []string{bob.UserID}, "is_direct": true})
	server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Not a DM"})

	path := "/_matrix/client/v1/users/" + url.PathEscape(alice.UserID) + "/rooms"
	resp := server.MustDo(http.MethodGet, path, alice.Token, nil, http.StatusOK)
	rooms := roomIDList(t, resp.Body)
	if len(rooms) != 1 || rooms[0] != dm {
		t.Fatalf("direct rooms of alice = %v, want only %s", rooms, dm)
	}

	// The m.direct data belongs to the user who started the conversation, so
	// the other side only lists rooms it marked itself.
	other := server.MustDo(http.MethodGet, "/_matrix/client/v1/users/"+url.PathEscape(bob.UserID)+"/rooms",
		bob.Token, nil, http.StatusOK)
	if rooms := roomIDList(t, other.Body); len(rooms) != 0 {
		t.Errorf("direct rooms of bob = %v, want none because alice started the room", rooms)
	}
	second := server.CreateRoom(bob, map[string]any{"invite": []string{alice.UserID}, "is_direct": true})
	other = server.MustDo(http.MethodGet, "/_matrix/client/v1/users/"+url.PathEscape(bob.UserID)+"/rooms",
		bob.Token, nil, http.StatusOK)
	if rooms := roomIDList(t, other.Body); len(rooms) != 1 || rooms[0] != second {
		t.Errorf("direct rooms of bob = %v, want %s", rooms, second)
	}

	// Nobody else may read the listing.
	if resp := server.Do(http.MethodGet, path, bob.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("bob listing the rooms of alice: got %d %s, want 403", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v1/users/"+url.PathEscape("@nobody:test.server")+"/rooms",
		alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("an unknown user: got %d %s, want 404", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, path, "", nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated direct rooms: got %d, want 401", resp.Status)
	}
}

// containsRoom reports whether a listing holds the wanted room id.
func containsRoom(rooms []string, want string) bool {
	for _, room := range rooms {
		if room == want {
			return true
		}
	}
	return false
}
