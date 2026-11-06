package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// A space is a room, its children are other rooms, and the link between them is a state event in the
// space. That makes "what is under this space" a question a client could ask by crawling links and
// peeking into each room — which is exactly what it must not have to do, because peeking needs standing
// in the room and a space is allowed to point at rooms the reader has none. The hierarchy endpoint is
// the server answering the crawl, and these tests are about the two things that make it more than a
// graph walk: a room is listed only for somebody who may see it, and a child this server does not hold
// is described by the server that does.

// mustCreateSpace creates a space whose history may be read without joining, which is what lets a
// server answer a hierarchy question about it from outside the room, and returns its room ID.
func mustCreateSpace(t *testing.T, n *fedNode, name, token string) string {
	t.Helper()
	status, answer := n.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom", map[string]any{
		"preset": "private_chat",
		"name":   name,
		"creation_content": map[string]any{
			"type": "m.space",
		},
		"initial_state": []map[string]any{{
			"type":      "m.room.history_visibility",
			"state_key": "",
			"content":   map[string]any{"history_visibility": "world_readable"},
		}},
	}, token)
	if status != http.StatusOK {
		t.Fatalf("%s could not create the space %q: %d %v", n.name, name, status, answer)
	}
	roomID, _ := answer["room_id"].(string)
	if roomID == "" {
		t.Fatalf("the space %q came back without a room ID: %v", name, answer)
	}
	return roomID
}

// linkSpace points a space at one of its children. `via` is what makes the link usable, and a link with
// no `suggested` flag is a child a client may hide by default.
func linkSpace(t *testing.T, n *fedNode, space, child, token string, order string, suggested bool) {
	t.Helper()
	content := map[string]any{"via": []any{n.name}}
	if order != "" {
		content["order"] = order
	}
	if suggested {
		content["suggested"] = true
	}
	if status, answer := n.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+space+"/state/m.space.child/"+child, content, token); status != http.StatusOK {
		t.Fatalf("%s could not link %s into %s: %d %v", n.name, child, space, status, answer)
	}
}

// hierarchyRooms asks for a space tree and returns the room IDs it listed, in the order they came.
func hierarchyRooms(t *testing.T, n *fedNode, space, token, query string) []string {
	t.Helper()
	path := "/_matrix/client/v1/rooms/" + space + "/hierarchy"
	if query != "" {
		path += "?" + query
	}
	status, answer := n.clientDo(t, http.MethodGet, path, nil, token)
	if status != http.StatusOK {
		t.Fatalf("%s could not ask for the hierarchy of %s: %d %v", n.name, space, status, answer)
	}
	var ids []string
	list, _ := answer["rooms"].([]any)
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("the hierarchy of %s carries a room entry that is not an object: %v", space, item)
		}
		roomID, _ := entry["room_id"].(string)
		if roomID == "" {
			t.Fatalf("a hierarchy entry carries no room ID: %v", entry)
		}
		ids = append(ids, roomID)
	}
	return ids
}

// waitsForHierarchyRooms pages through a whole space tree until it holds the expected number of rooms,
// because a server learns that a space gained a child when it is told, and a test of a rule must not be
// a test of how fast two servers happened to talk.
func waitsForHierarchyRooms(t *testing.T, n *fedNode, space, token string, want int) []string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var ids []string
	for {
		ids = nil
		next := ""
		for {
			query := "limit=100"
			if next != "" {
				query += "&from=" + next
			}
			page := hierarchyRooms(t, n, space, token, query)
			ids = append(ids, page...)
			status, answer := n.clientDo(t, http.MethodGet,
				"/_matrix/client/v1/rooms/"+space+"/hierarchy?"+query, nil, token)
			if status != http.StatusOK {
				t.Fatalf("%s could not page through the hierarchy of %s: %d %v", n.name, space, status, answer)
			}
			next, _ = answer["next_batch"].(string)
			if next == "" {
				break
			}
		}
		if len(ids) == want {
			return ids
		}
		if time.Now().After(deadline) {
			return ids
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestHierarchyHidesARestrictedChildFromAUserOutsideTheSpace is the visibility half of the endpoint.
//
// A space that points at a restricted room tells everybody where the room is; the room itself decides
// who may be in it. Listing the room for a user the room would not let in hands that user its name,
// topic and avatar, which is the room's to give and not the space's. So the room appears for alice,
// who is in the space, and not for bob, who is not — and the same room, asked about directly, is
// refused rather than answered with an empty tree.
func TestHierarchyHidesARestrictedChildFromAUserOutsideTheSpace(t *testing.T) {
	t.Parallel()
	hs1, _ := federationPair(t)

	alice := hs1.registerUser(t, "halie", "pw-halie")
	space := fedCreateRoom(t, hs1, alice, map[string]any{
		"preset": "private_chat",
		"name":   "the hall",
		"creation_content": map[string]any{
			"type": "m.space",
		},
		"initial_state": []map[string]any{{
			"type":      "m.room.history_visibility",
			"state_key": "",
			"content":   map[string]any{"history_visibility": "world_readable"},
		}},
	})
	gateway := fedCreateRoom(t, hs1, alice, map[string]any{
		"preset": "public_chat",
		"name":   "the gateway",
	})
	restricted := fedCreateRoom(t, hs1, alice, restrictedRoomBody("the restricted hall", gateway))
	linkSpace(t, hs1, space, restricted, alice, "", true)

	bob := hs1.registerUser(t, "halbob", "pw-halbob")
	if status, answer := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+space+"?server_name="+hs1.name,
		map[string]any{}, alice); status != http.StatusOK {
		t.Fatalf("%s could not join the space: %d %v", alice, status, answer)
	}

	if ids := waitsForHierarchyRooms(t, hs1, space, alice, 2); len(ids) != 2 {
		t.Fatalf("the space listed %d rooms for %s, want 2 (the space and its child): %v", len(ids), alice, ids)
	}
	if ids := hierarchyRooms(t, hs1, space, bob, "limit=100"); len(ids) != 1 || ids[0] != space {
		t.Fatalf("the space listed %v for %s, who is not in it; want only the space %s — a restricted "+
			"room's summary is the room's to give, not the space's", ids, bob, space)
	}

	// The room's own summary, asked for directly, is refused rather than answered with an empty tree:
	// an empty answer would leave the client guessing whether the room is empty or the reader is
	// outside it.
	if status, answer := hs1.clientDo(t, http.MethodGet,
		"/_matrix/client/v1/rooms/"+restricted+"/hierarchy", nil, bob); status != http.StatusForbidden {
		t.Fatalf("asking for the hierarchy of a room %s is not in answered %d %v, want 403",
			bob, status, answer)
	}

	// The restricted child says which rooms its join rule points at, because a client cannot offer
	// "join this through the space" without knowing that the space is what opens it.
	status, answer := hs1.clientDo(t, http.MethodGet, "/_matrix/client/v1/rooms/"+space+"/hierarchy?limit=100", nil, alice)
	if status != http.StatusOK {
		t.Fatalf("%s could not ask for the hierarchy of %s: %d %v", alice, space, status, answer)
	}
	list, _ := answer["rooms"].([]any)
	for _, item := range list {
		entry, _ := item.(map[string]any)
		if entry["room_id"] != restricted {
			continue
		}
		allowed, _ := entry["allowed_room_ids"].([]any)
		if len(allowed) != 1 || allowed[0] != gateway {
			t.Errorf("the restricted room's summary names allowed_room_ids %v, want [%s]", allowed, gateway)
		}
		if rule, _ := entry["join_rule"].(string); rule != "restricted" {
			t.Errorf("the restricted room's summary names join_rule %q, want restricted", rule)
		}
		return
	}
	t.Errorf("the hierarchy for %s never listed the restricted room %s", alice, restricted)
}

// TestHierarchyWalksARoomHeldByAnotherHomeserver is the other half: a child that lives elsewhere is
// described by the server that holds it, and the description is weighed rather than believed.
//
// The space is on hs1 and the restricted room on hs2, so hs1 cannot answer for the child. Initially hs2
// has never seen the space, so it cannot know that a hs1 user is in it and withholds the room — the
// correct answer, and the one that shows this server is not inventing the child's join rule for itself.
// Once a hs2 user joins the space, hs2 can answer, and the room appears for the hs1 user who is in the
// space and not for the one who is not: hs2 answered about the servers, and hs1 answered about its own
// users.
func TestHierarchyWalksARoomHeldByAnotherHomeserver(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "ralice", "pw-ralice")
	space := fedCreateRoom(t, hs1, alice, map[string]any{
		"preset": "public_chat",
		"name":   "remote space",
		"creation_content": map[string]any{
			"type": "m.space",
		},
		"initial_state": []map[string]any{{
			"type":      "m.room.history_visibility",
			"state_key": "",
			"content":   map[string]any{"history_visibility": "world_readable"},
		}},
	})
	charlie := hs2.registerUser(t, "rcharlie", "pw-rcharlie")
	charlieID := "@rcharlie:hs2"
	// The room hs2 holds restricts joining to the members of the space — the space on the other
	// server. That is the shape this test is about: the room that could let the user in is held by the
	// server that was not asked.
	restricted := fedCreateRoom(t, hs2, charlie, restrictedRoomBody("remote restricted", space))
	linkSpace(t, hs1, space, restricted, alice, "", true)

	bob := hs1.registerUser(t, "rbob", "pw-rbob")
	if status, answer := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+space+"?server_name="+hs1.name,
		map[string]any{}, alice); status != http.StatusOK {
		t.Fatalf("%s could not join the space: %d %v", alice, status, answer)
	}

	// hs2 holds the restricted room but not the space it points at, so it cannot say who that room
	// would let in. hs1 walks the space and gets nothing for that child — not a guess made from hs1's
	// own copy of the space.
	if got := len(hierarchyRooms(t, hs1, space, alice, "limit=100")); got != 1 {
		t.Fatalf("hs1 listed %d rooms for %s before hs2 could see the space, want 1: hs2 holds the child "+
			"and hs1 must not describe it on hs2's behalf", got, alice)
	}

	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+space+"?server_name="+hs1.name,
		map[string]any{}, charlie); status != http.StatusOK {
		t.Fatalf("%s could not join the space: %d %v", charlieID, status, answer)
	}
	if !waitsForMembership(t, hs2, space, charlie, charlieID, "join") {
		t.Fatalf("%s never saw %s join the space", hs2.name, charlieID)
	}

	if got := waitsForHierarchyRooms(t, hs1, space, alice, 2); len(got) != 2 {
		t.Fatalf("hs1 listed %d rooms for %s after %s joined the space, want 2: %v", len(got), alice, charlieID, got)
	}
	// Both servers have to get there: hs1 by asking, hs2 by being able to answer.
	if got := len(hierarchyRooms(t, hs1, space, alice, "limit=100")); got != 2 {
		t.Fatalf("hs1 listed %d rooms for %s once hs2 could see the space, want 2", got, alice)
	}
	if got := len(hierarchyRooms(t, hs2, space, charlie, "limit=100")); got != 2 {
		t.Fatalf("hs2 listed %d rooms for %s, want 2", got, charlieID)
	}
	// And hs1 is still the one that answers about its own users. bob never joined the space, and the
	// room hs2 holds opens only to the space's members — hs2's "the asking server could see this" is
	// not hs1's "this user may see it".
	if got := len(hierarchyRooms(t, hs1, space, bob, "limit=100")); got != 1 {
		t.Fatalf("hs1 listed %d rooms for %s, want 1: %s is not in the space the restriction names", got, bob, bob)
	}
}

// TestHierarchyOrdersPagesAndFiltersItsChildren checks the parts of the answer a client draws from:
// children arrive in the order the space's own links say, a page is a page, and a request for only the
// suggested children is not answered with the whole set.
func TestHierarchyOrdersPagesAndFiltersItsChildren(t *testing.T) {
	t.Parallel()
	hs1, _ := federationPair(t)

	alice := hs1.registerUser(t, "oalice", "pw-oalice")
	space := mustCreateSpace(t, hs1, "ordered space", alice)
	if status, answer := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+space+"?server_name="+hs1.name,
		map[string]any{}, alice); status != http.StatusOK {
		t.Fatalf("%s could not join the space: %d %v", alice, status, answer)
	}

	// Four children, ordered "a", "b" and "c", and one whose link carries no order at all, which sorts
	// after the ones that have one. The third is also the one the space does not suggest. Room IDs do
	// not matter to the order; the links decide it.
	first := fedCreateRoom(t, hs1, alice, map[string]any{"preset": "private_chat", "name": "first"})
	second := fedCreateRoom(t, hs1, alice, map[string]any{"preset": "private_chat", "name": "second"})
	hidden := fedCreateRoom(t, hs1, alice, map[string]any{"preset": "private_chat", "name": "unsuggested"})
	unordered := fedCreateRoom(t, hs1, alice, map[string]any{"preset": "private_chat", "name": "unordered"})
	linkSpace(t, hs1, space, unordered, alice, "", true)
	linkSpace(t, hs1, space, second, alice, "b", true)
	linkSpace(t, hs1, space, first, alice, "a", true)
	linkSpace(t, hs1, space, hidden, alice, "c", false)

	want := []string{space, first, second, hidden, unordered}
	got := waitsForHierarchyRooms(t, hs1, space, alice, len(want))
	if len(got) != len(want) {
		t.Fatalf("the space listed %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("the space listed %v, want %v: children come in the order their links put them, "+
				"and a link with no order sorts after the ones that have one", got, want)
		}
	}

	// suggested_only is the space's own filter, not the reader's: the unsuggested child is a room the
	// reader is in, and it is left out because the space said it is not the one to show.
	if got := hierarchyRooms(t, hs1, space, alice, "suggested_only=true&limit=100"); len(got) != 4 || containsRoom(got, hidden) {
		t.Fatalf("suggested_only listed %v, want the space and the three suggested children", got)
	}

	// max_depth 0 is the requested room alone.
	if got := hierarchyRooms(t, hs1, space, alice, "max_depth=0"); len(got) != 1 || got[0] != space {
		t.Fatalf("max_depth=0 listed %v, want only %s", got, space)
	}

	// A page is a page: two rooms, then the token, then the rest, and no room twice.
	status, answer := hs1.clientDo(t, http.MethodGet,
		"/_matrix/client/v1/rooms/"+space+"/hierarchy?limit=2", nil, alice)
	if status != http.StatusOK {
		t.Fatalf("the first page failed: %d %v", status, answer)
	}
	firstPage, _ := answer["rooms"].([]any)
	if len(firstPage) != 2 {
		t.Fatalf("limit=2 answered with %d rooms, want 2", len(firstPage))
	}
	token, _ := answer["next_batch"].(string)
	if token == "" {
		t.Fatalf("limit=2 over five rooms offered no next_batch: %v", answer)
	}
	status, answer = hs1.clientDo(t, http.MethodGet,
		"/_matrix/client/v1/rooms/"+space+"/hierarchy?limit=2&from="+token, nil, alice)
	if status != http.StatusOK {
		t.Fatalf("the second page failed: %d %v", status, answer)
	}
	secondPage, _ := answer["rooms"].([]any)
	if len(secondPage) != 2 {
		t.Fatalf("the second page holds %d rooms, want 2", len(secondPage))
	}
	// A token carried into a differently shaped walk points into a different tree, and the walk it
	// came from is the only one it can be used in.
	if status, answer := hs1.clientDo(t, http.MethodGet,
		"/_matrix/client/v1/rooms/"+space+"/hierarchy?limit=2&from="+token+"&max_depth=1", nil, alice); status != http.StatusBadRequest {
		t.Fatalf("paging with a changed max_depth answered %d %v, want 400", status, answer)
	}
	if status, answer := hs1.clientDo(t, http.MethodGet,
		"/_matrix/client/v1/rooms/"+space+"/hierarchy?from=not-a-token", nil, alice); status != http.StatusBadRequest {
		t.Fatalf("an unknown pagination token answered %d %v, want 400", status, answer)
	}
}

// TestHierarchyQueryAnswersOnlyWhatTheAskingServerCouldSee is the Server-Server half. A peer asking
// about a room is answered about what that peer could see, not about the user the peer happens to have
// in mind — the peer does the filtering for its own users, and this server has no standing to answer
// about them.
func TestHierarchyQueryAnswersOnlyWhatTheAskingServerCouldSee(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "qalice", "pw-qalice")
	open := fedCreateRoom(t, hs1, alice, map[string]any{"preset": "public_chat", "name": "open space"})
	// A room with nobody from hs2 in it, and history closed to non-members: hs2 cannot see it, and
	// telling hs2 that it exists is not nothing.
	kept := fedCreateRoom(t, hs1, alice, map[string]any{"preset": "private_chat", "name": "kept"})
	linkSpace(t, hs1, open, kept, alice, "", true)

	status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/hierarchy/"+urlEscape(open)+"?suggested_only=false", nil)
	if status != http.StatusOK {
		t.Fatalf("%s asked for the hierarchy of %s and got %d: %s", hs2.name, open, status, body)
	}
	var answer map[string]any
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("the hierarchy answer is not a JSON object: %v (%s)", err, body)
	}
	room, _ := answer["room"].(map[string]any)
	if room["room_id"] != open {
		t.Errorf("the answer describes %v, want the room that was asked about (%s)", room["room_id"], open)
	}
	if _, ok := answer["children"]; !ok {
		t.Errorf("the answer carries no children list at all: %v", answer)
	}
	// The child hs2 cannot see is named as inaccessible rather than left out in silence. That list is
	// what stops the asking server from spending another request on a room this server has already
	// answered about.
	if blocked, _ := answer["inaccessible_children"].([]any); len(blocked) != 1 || blocked[0] != kept {
		t.Errorf("the answer named inaccessible_children %v, want [%s]", answer["inaccessible_children"], kept)
	}

	// hs2 asks for the room it cannot see. The answer is the one it gives for a room it has never
	// heard of: a hierarchy summary carries a room's name, and a server that is keeping the room has
	// not agreed to hand that over.
	if status, body = hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/hierarchy/"+urlEscape(kept), nil); status != http.StatusNotFound {
		t.Fatalf("%s asked for the hierarchy of a room it is not in and got %d: %s", hs2.name, status, body)
	}
	// And a request that arrives unauthenticated gets the answer the Server-Server API gives to an
	// unsigned request, not a room summary.
	if status, body = hs1.rawRequest(t, hs2, http.MethodGet, "/_matrix/federation/v1/hierarchy/"+urlEscape(open), "", nil); status != http.StatusUnauthorized {
		t.Fatalf("an unsigned hierarchy query got %d: %s", status, body)
	}
}

// containsRoom says whether a list of room IDs holds one.
func containsRoom(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
