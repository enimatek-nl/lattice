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

// linkSpaceSuggested adds a child link the space marks as worth showing on a short list.
func (s *Server) linkSpaceSuggested(session *Session, spaceID, childID string) {
	s.t.Helper()
	s.MustDo(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/state/m.space.child/%s", spaceID, url.PathEscape(childID)),
		session.Token, map[string]any{"via": []string{"test.server"}, "suggested": true}, http.StatusOK)
}

// unlinkSpace takes a child out of a space, which is an empty body on the same state key
// rather than a deletion: state events are not removed, they are emptied.
func (s *Server) unlinkSpace(session *Session, spaceID, childID string) {
	s.t.Helper()
	s.MustDo(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/state/m.space.child/%s", spaceID, url.PathEscape(childID)),
		session.Token, map[string]any{}, http.StatusOK)
}

// sameOrder compares two lists of ids, order included.
func sameOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// hierarchyOrder lists the room ids of a hierarchy page in the order they were given.
func hierarchyOrder(rooms []map[string]any) []string {
	ids := make([]string, 0, len(rooms))
	for _, room := range rooms {
		id, _ := room["room_id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// childrenOf lists the state keys of a hierarchy entry's children_state, which are the rooms
// the space says it contains.
func childrenOf(t *testing.T, entry map[string]any) []string {
	t.Helper()
	raw, ok := entry["children_state"].([]any)
	if !ok {
		t.Fatalf("a hierarchy entry carries no children_state: %v", entry)
	}
	keys := make([]string, 0, len(raw))
	for _, event := range raw {
		child, ok := event.(map[string]any)
		if !ok {
			t.Fatalf("children_state holds something that is not an event: %v", event)
		}
		if key, _ := child["state_key"].(string); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
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
	underneath := server.CreateRoom(alice, map[string]any{"name": "Aphid", "preset": "public_chat"})
	server.linkSpaceSuggested(alice, space, first)
	server.linkSpace(alice, space, second)
	server.linkSpace(alice, second, grandchild)
	server.linkSpace(alice, first, underneath)

	resp := server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy",
		alice.Token, nil, http.StatusOK)
	rooms := hierarchyRooms(t, resp.Body)
	// The specification names no default depth, and a caller that did not name one is
	// asking to see the space rather than a slice of it: the grandchild is part of the
	// space, so it is here. A caller who wants one level says max_depth=1.
	if len(rooms) != 5 {
		t.Fatalf("hierarchy holds %d rooms, want the whole space: %s", len(rooms), resp.Raw)
	}
	// Depth first, in the order the space names its children: a caller paging through a
	// space gets one branch finished before the next is started, which is what makes the
	// first pages of a wide space useful rather than shallow.
	want := []string{space, first, underneath, second, grandchild}
	if got := hierarchyOrder(rooms); !sameOrder(got, want) {
		t.Errorf("the walk of %s lists %v, want %v (depth first)", space, got, want)
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
	if joinRule, _ := rooms[0]["join_rule"].(string); joinRule != "public" {
		t.Errorf("join_rule = %v", rooms[0]["join_rule"])
	}
	children := childrenOf(t, rooms[0])
	if len(children) != 2 {
		t.Fatalf("children_state = %v, want the two m.space.child events", rooms[0]["children_state"])
	}
	child, _ := rooms[0]["children_state"].([]any)[0].(map[string]any)
	if child["type"] != "m.space.child" {
		t.Errorf("children_state entry type = %v", child["type"])
	}
	if _, hasStateKey := child["state_key"]; !hasStateKey {
		t.Errorf("children_state entry has no state key: %v", child)
	}
	// A room without children carries an empty list.
	for _, room := range rooms {
		id, _ := room["room_id"].(string)
		state := childrenOf(t, room)
		switch id {
		case first, second:
			if len(state) != 1 {
				t.Errorf("%s links one child, got %v", id, state)
			}
		case grandchild, underneath:
			if len(state) != 0 {
				t.Errorf("the leaf %s has children_state: %v", id, state)
			}
		}
	}
	if _, hasMore := resp.Body["next_batch"]; hasMore {
		t.Errorf("a complete walk still returned next_batch: %v", resp.Body["next_batch"])
	}

	// An explicit window is honoured exactly: one level is the space and its two children,
	// two levels reaches the grandchild.
	resp = server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?max_depth=1", alice.Token, nil, http.StatusOK)
	if got := len(hierarchyRooms(t, resp.Body)); got != 3 {
		t.Errorf("max_depth=1 found %d rooms, want 3", got)
	}
	resp = server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?max_depth=2", alice.Token, nil, http.StatusOK)
	if got := len(hierarchyRooms(t, resp.Body)); got != 5 {
		t.Errorf("max_depth=2 found %d rooms, want 5", got)
	}
	// min_depth skips the space itself, and says nothing about how deep to go: the rest of
	// the space is still the rest of the space.
	resp = server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?min_depth=1", alice.Token, nil, http.StatusOK)
	rooms = hierarchyRooms(t, resp.Body)
	if len(rooms) != 4 {
		t.Fatalf("min_depth=1 found %d rooms, want the space's four descendants", len(rooms))
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

	// A page is a run of the depth-first walk, not the shallowest rooms: the first three are
	// the space, its first child and that child's own child.
	resp = server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?limit=3", alice.Token, nil, http.StatusOK)
	if got := hierarchyOrder(hierarchyRooms(t, resp.Body)); !sameOrder(got, []string{space, first, underneath}) {
		t.Errorf("the first page of three holds %v, want %v", got, []string{space, first, underneath})
	}

	// suggested_only keeps the branches the space marks as worth showing, links included, so
	// a client asking for the short list is not handed the long one and told which to ignore.
	resp = server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy?suggested_only=true", alice.Token, nil, http.StatusOK)
	rooms = hierarchyRooms(t, resp.Body)
	// A link that is not suggested takes its whole branch out, not just the room: the short
	// list is the space with the unimportant branches removed, not the same space with some
	// of them labelled.
	if got := hierarchyOrder(rooms); !sameOrder(got, []string{space, first}) {
		t.Errorf("suggested_only lists %v, want the space and its suggested child", got)
	}
	if got := childrenOf(t, rooms[0]); !sameOrder(got, []string{first}) {
		t.Errorf("suggested_only still shows the space's other links: %v", got)
	}

	// Taking a child out of a space is an empty body on the link, not a deleted event. The
	// branch goes - both the room below it and the link itself, which would otherwise offer
	// the client a door into a room the space closed.
	server.unlinkSpace(alice, space, second)
	resp = server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy",
		alice.Token, nil, http.StatusOK)
	rooms = hierarchyRooms(t, resp.Body)
	if got := hierarchyOrder(rooms); !sameOrder(got, []string{space, first, underneath}) {
		t.Errorf("after unlinking %s the walk lists %v", second, got)
	}
	if got := childrenOf(t, rooms[0]); !sameOrder(got, []string{first}) {
		t.Errorf("the removed link is still in children_state: %v", got)
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

// TestARestrictedRoomInASpaceIsShownToTheSpace is the summary of a space whose child lets people
// in through the space. The room is not readable by an outsider - it keeps its history to its
// members - but it is joinable by a member of the space, and the summary is how a client learns a
// room is there in order to join it. So the room is shown to the people the space admits and hidden
// from the ones it does not, which is a different question from who may read it.
func TestARestrictedRoomInASpaceIsShownToTheSpace(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	space := server.CreateRoom(alice, map[string]any{
		"preset":             "public_chat",
		"name":               "The space",
		"type":               "m.space",
		"history_visibility": "world_readable",
	})
	room := server.CreateRoom(alice, map[string]any{
		"preset":       "public_chat",
		"name":         "The room in the space",
		"room_version": "8",
	})
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+url.PathEscape(room)+"/state/m.room.join_rules",
		alice.Token, map[string]any{
			"join_rule": "restricted",
			"allow":     []map[string]any{{"type": "m.room_membership", "room_id": space}},
		}, http.StatusOK)
	server.linkSpace(alice, space, room)

	listed := func(session *Session) map[string]map[string]any {
		server.t.Helper()
		resp := server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+url.PathEscape(space)+"/hierarchy",
			session.Token, nil, http.StatusOK)
		byID := map[string]map[string]any{}
		for _, entry := range hierarchyRooms(t, resp.Body) {
			if id, _ := entry["room_id"].(string); id != "" {
				byID[id] = entry
			}
		}
		return byID
	}

	// Bob belongs to nothing. The space is open to be read, so it is shown; the room is neither
	// readable by him nor joinable, so it is not.
	found := listed(bob)
	if found[space] == nil {
		t.Fatalf("the space itself is not shown to bob: %v", found)
	}
	if found[room] != nil {
		t.Fatalf("a room the space admits is shown to a person the space does not admit: %v", found[room])
	}

	// Bob joins the space, which is the fact the room's rule asks about. Nothing about the room
	// changes: it is the same state, read against a different person.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+url.PathEscape(space), bob.Token,
		map[string]any{}, http.StatusOK)
	found = listed(bob)
	entry, ok := found[room]
	if !ok {
		t.Fatalf("a member of the space is not shown the room it admits: %v", found)
	}
	if rule, _ := entry["join_rule"].(string); rule != "restricted" {
		t.Errorf("the room is described with join_rule %q, want restricted: %v", rule, entry)
	}
}
