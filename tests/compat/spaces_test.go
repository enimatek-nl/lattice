package compat

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// linkSpace adds an m.space.child state event to a space.
func (s *Server) linkSpace(session *Session, spaceID, childID string) {
	s.t.Helper()
	s.MustDo(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/state/m.space.child/%s", spaceID, url.PathEscape(childID)),
		session.Token, map[string]any{"via": []string{"test.server"}}, http.StatusOK)
}

// hierarchyRooms extracts the rooms array of a hierarchy response.
func hierarchyRooms(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["rooms"].([]any)
	if !ok {
		t.Fatalf("no rooms array in %v", body)
	}
	rooms := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		room, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("room entry %v is not an object", entry)
		}
		rooms = append(rooms, room)
	}
	return rooms
}

// TestSpaceHierarchy covers GET /v1/rooms/{roomId}/hierarchy and the
// m.space.child walk it performs.
func TestSpaceHierarchy(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	space := server.CreateRoom(alice, map[string]any{"type": "m.space", "name": "Garden", "preset": "public_chat"})
	first := server.CreateRoom(alice, map[string]any{"name": "Tomatoes", "preset": "public_chat"})
	second := server.CreateRoom(alice, map[string]any{"name": "Herbs", "preset": "public_chat"})
	grandchild := server.CreateRoom(alice, map[string]any{"name": "Basil", "preset": "public_chat"})
	server.linkSpace(alice, space, first)
	server.linkSpace(alice, space, second)
	server.linkSpace(alice, second, grandchild)

	resp := server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy",
		alice.Token, nil, http.StatusOK)
	rooms := hierarchyRooms(t, resp.Body)
	if len(rooms) != 3 {
		t.Fatalf("hierarchy holds %d rooms, want the space and its two children: %s", len(rooms), resp.Raw)
	}
	if id, _ := rooms[0]["room_id"].(string); id != space {
		t.Errorf("the first room of the walk = %v, want the space itself", rooms[0]["room_id"])
	}
	if roomType, _ := rooms[0]["room_type"].(string); roomType != "m.space" {
		t.Errorf("room_type = %v, want m.space", rooms[0]["room_type"])
	}
	if name, _ := rooms[0]["name"].(string); name != "Garden" {
		t.Errorf("name = %v", rooms[0]["name"])
	}
	if members, _ := rooms[0]["num_joined_members"].(float64); members != 1 {
		t.Errorf("num_joined_members = %v", rooms[0]["num_joined_members"])
	}
	if joinRules, _ := rooms[0]["join_rules"].(string); joinRules != "public" {
		t.Errorf("join_rules = %v", rooms[0]["join_rules"])
	}
	children, ok := rooms[0]["children_state"].([]any)
	if !ok || len(children) != 2 {
		t.Fatalf("children_state = %v, want the two m.space.child events", rooms[0]["children_state"])
	}
	child, _ := children[0].(map[string]any)
	if child["type"] != "m.space.child" {
		t.Errorf("children_state entry type = %v", child["type"])
	}
	if _, hasStateKey := child["state_key"]; !hasStateKey {
		t.Errorf("children_state entry has no state key: %v", child)
	}
	if id, _ := rooms[2]["room_id"].(string); id != second && id != first {
		t.Errorf("the third room of the walk = %v", rooms[2]["room_id"])
	}
	// A room without children carries an empty list.
	for _, room := range rooms {
		id, _ := room["room_id"].(string)
		state, _ := room["children_state"].([]any)
		if id == first && len(state) != 0 {
			t.Errorf("the leaf %s has children_state: %v", id, state)
		}
		if id == second && len(state) != 1 {
			t.Errorf("%s links one child, got %v", id, state)
		}
	}
	if _, hasMore := resp.Body["next_batch"]; hasMore {
		t.Errorf("a complete walk still returned next_batch: %v", resp.Body["next_batch"])
	}

	// max_depth reaches the grandchild, min_depth skips the space itself.
	resp = server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?max_depth=2", alice.Token, nil, http.StatusOK)
	if got := len(hierarchyRooms(t, resp.Body)); got != 4 {
		t.Errorf("max_depth=2 found %d rooms, want 4", got)
	}
	resp = server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?min_depth=1", alice.Token, nil, http.StatusOK)
	rooms = hierarchyRooms(t, resp.Body)
	if len(rooms) != 2 {
		t.Fatalf("min_depth=1 found %d rooms, want 2", len(rooms))
	}
	if id, _ := rooms[0]["room_id"].(string); id == space {
		t.Errorf("min_depth=1 still includes the space itself")
	}

	// A page holds at most "limit" rooms and hands out a token for the rest.
	resp = server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?limit=1", alice.Token, nil, http.StatusOK)
	rooms = hierarchyRooms(t, resp.Body)
	if len(rooms) != 1 {
		t.Fatalf("limit=1 returned %d rooms", len(rooms))
	}
	token, _ := resp.Body["next_batch"].(string)
	if token == "" {
		t.Fatalf("limit=1 gave no next_batch: %s", resp.Raw)
	}
	next := server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?limit=1&from="+token,
		alice.Token, nil, http.StatusOK)
	if got := len(hierarchyRooms(t, next.Body)); got != 1 {
		t.Errorf("the second page holds %d rooms, want 1", got)
	}
	if id, _ := hierarchyRooms(t, next.Body)[0]["room_id"].(string); id == rooms[0]["room_id"] {
		t.Errorf("paging returned the same room twice: %s", id)
	}

	// A child that does not exist makes the chain incomplete instead of
	// silently dropping the rest of the walk.
	server.linkSpace(alice, space, "!nonsense:test.server")
	resp = server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy",
		alice.Token, nil, http.StatusOK)
	if incomplete, _ := resp.Body["incomplete_chain"].(bool); !incomplete {
		t.Errorf("an unresolvable child did not set incomplete_chain: %s", resp.Raw)
	}
	if _, ok := resp.Body["rooms"]; !ok {
		t.Errorf("an unresolvable child broke the walk: %s", resp.Raw)
	}

	// Bad requests and permissions.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/rooms/!nope:test.server/hierarchy",
		alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("unknown space: got %d %s, want 404", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?min_depth=3&max_depth=1",
		alice.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("min_depth above max_depth: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?from=now",
		alice.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("a nonsense from token: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy",
		"", nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated hierarchy: got %d, want 401", resp.Status)
	}
	outsider := server.Register("carol", "kettle")
	secret := server.CreateRoom(alice, map[string]any{"name": "Shed", "preset": "private_chat"})
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/rooms/"+url.PathEscape(secret)+"/hierarchy",
		outsider.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("hierarchy of a private room: got %d %s, want 403", resp.Status, resp.Raw)
	}
}
