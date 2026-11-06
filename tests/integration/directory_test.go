package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestPublicRoomsListsPublishedRooms checks the room directory: what a published room
// advertises, and what a private room keeps to itself.
func TestPublicRoomsListsPublishedRooms(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "dirhost", "pw-dirhost", "")
	// A world-readable room, and the world-readability is said explicitly: the `public_chat`
	// preset makes a room joinable without an invitation and keeps its history to members, so a
	// room that is genuinely open to readers who never joined has to be made that way.
	open := createRoom(t, h, aliceToken, map[string]any{
		"visibility": "public",
		"preset":     "public_chat",
		"name":       "Open Room",
		"topic":      "everything is readable here",
		"initial_state": []map[string]any{
			{"type": "m.room.history_visibility", "state_key": "",
				"content": map[string]any{"history_visibility": "world_readable"}},
			{"type": "m.room.guest_access", "state_key": "",
				"content": map[string]any{"guest_access": "can_join"}},
		},
	})
	quiet := createRoom(t, h, aliceToken, map[string]any{"name": "Quiet Room"})
	published := createRoom(t, h, aliceToken, map[string]any{"visibility": "public", "name": "Joinable by invitation"})

	page := h.mustStatus(t, h.get("/_matrix/client/v3/publicRooms", aliceToken), http.StatusOK).decoded(t)
	entries := listedRooms(t, page)
	if len(entries) != 2 {
		t.Fatalf("the directory holds %d rooms, want the two published ones: %v", len(entries), entries)
	}
	for _, entry := range entries {
		if entry["room_id"] == quiet {
			t.Errorf("a room nobody published is listed: %v", entry)
		}
	}
	var openEntry map[string]any
	for _, entry := range entries {
		if entry["room_id"] == open {
			openEntry = entry
		}
	}
	if openEntry == nil {
		t.Fatalf("the published room is missing from the directory: %v", entries)
	}
	if openEntry["name"] != "Open Room" {
		t.Errorf("name = %v, want the room's name", openEntry["name"])
	}
	if openEntry["topic"] != "everything is readable here" {
		t.Errorf("topic = %v, want the room's topic", openEntry["topic"])
	}
	if openEntry["world_readable"] != true {
		t.Errorf("world_readable = %v, want true for a world readable room", openEntry["world_readable"])
	}
	// The other published room asked for a public room without naming a preset, which the
	// specification reads as the public_chat preset: joinable by anybody, history kept to
	// members, guests kept out. A room listed as public is not therefore a room whose history is
	// public, and a directory that says otherwise is advertising something the room refused.
	publishedEntry := entryFor(t, entries, published)
	if publishedEntry["world_readable"] != false {
		t.Errorf("world_readable = %v, want false: a public_chat room's history is shared with"+
			" members, not with the world", publishedEntry["world_readable"])
	}
	if publishedEntry["join_rule"] != "public" {
		t.Errorf("join_rule = %v, want public: a room created with visibility public but no named"+
			" preset takes the public_chat preset", publishedEntry["join_rule"])
	}
	if publishedEntry["guest_can_join"] != false {
		t.Errorf("guest_can_join = %v, want false: the public_chat preset sets m.room.guest_access"+
			" to forbidden", publishedEntry["guest_can_join"])
	}
	if openEntry["guest_can_join"] != true {
		t.Errorf("guest_can_join = %v, want true for an open room", openEntry["guest_can_join"])
	}
	if members, _ := openEntry["num_joined_members"].(float64); int(members) != 1 {
		t.Errorf("num_joined_members = %v, want 1", openEntry["num_joined_members"])
	}
	if total, _ := page["total_rooms"].(float64); int(total) != 2 {
		t.Errorf("total_rooms = %v, want 2", page["total_rooms"])
	}

	// The listing follows the room's state: a room renamed after it was published is not
	// listed under the name it stopped using.
	h.mustStatus(t, putState(t, h, open, "m.room.name", "", `{"name":"Renamed Room"}`, aliceToken), http.StatusOK)
	after := h.mustStatus(t, h.get("/_matrix/client/v3/publicRooms", aliceToken), http.StatusOK).decoded(t)
	for _, entry := range listedRooms(t, after) {
		if entry["room_id"] == open && entry["name"] != "Renamed Room" {
			t.Errorf("the listing still carries the old name: %v", entry)
		}
	}

	// Publishing is a decision, so it can be taken back — and taken again.
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/list/room/"+open,
		[]byte(`{"visibility":"private"}`), aliceToken), http.StatusOK)
	hidden := h.mustStatus(t, h.get("/_matrix/client/v3/publicRooms", aliceToken), http.StatusOK).decoded(t)
	for _, entry := range listedRooms(t, hidden) {
		if entry["room_id"] == open {
			t.Errorf("an unpublished room is still listed: %v", entry)
		}
	}
	if body := h.mustStatus(t, h.get("/_matrix/client/v3/directory/list/room/"+published, aliceToken), http.StatusOK).decoded(t); body["visibility"] != "public" {
		t.Errorf("visibility = %v, want public", body)
	}
}

// TestPublicRoomsSearchAndPages covers finding a published room by name or topic and
// walking the list a page at a time.
func TestPublicRoomsSearchAndPages(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "dirsearch", "pw-dirsearch", "")
	wombles := createRoom(t, h, aliceToken, map[string]any{
		"visibility": "public",
		"name":       "Kitchen",
		"topic":      "Something about WOMBLES here",
	})
	createRoom(t, h, aliceToken, map[string]any{"visibility": "public", "name": "Lounge", "topic": "nothing of interest"})

	// The search is a person looking for a room, so it is not case sensitive.
	res := h.mustStatus(t, h.raw(t, http.MethodPost, "/_matrix/client/v3/publicRooms",
		[]byte(`{"filter":{"generic_search_term":"wombles"}}`), aliceToken), http.StatusOK)
	var searched struct {
		Chunk      []map[string]any `json:"chunk"`
		TotalRooms int              `json:"total_rooms"`
	}
	if err := json.Unmarshal(res.body, &searched); err != nil {
		t.Fatalf("decoding the search result: %v (body %s)", err, res.body)
	}
	if searched.TotalRooms != 1 || len(searched.Chunk) != 1 || searched.Chunk[0]["room_id"] != wombles {
		t.Errorf("search results = %v, want only the room whose topic mentions the term", searched.Chunk)
	}

	// Paging: each token gives the following rooms, and the last page stops promising.
	first := h.mustStatus(t, h.raw(t, http.MethodPost, "/_matrix/client/v3/publicRooms",
		[]byte(`{"limit":1}`), aliceToken), http.StatusOK).decoded(t)
	if len(listedRooms(t, first)) != 1 {
		t.Fatalf("limit=1 returned %v", first["chunk"])
	}
	token, _ := first["next_batch"].(string)
	if token == "" {
		t.Fatalf("a page with more rooms behind it must give a token: %v", first)
	}
	seen := map[string]bool{}
	for _, entry := range listedRooms(t, first) {
		seen[entry["room_id"].(string)] = true
	}
	for cursor := token; cursor != ""; {
		page := h.mustStatus(t, h.post(t, "/_matrix/client/v3/publicRooms",
			map[string]any{"limit": 1, "since": cursor}, aliceToken), http.StatusOK).decoded(t)
		entries := listedRooms(t, page)
		if len(entries) == 0 {
			t.Fatalf("a page after %q was empty while more pages were promised", cursor)
		}
		for _, entry := range entries {
			roomID := entry["room_id"].(string)
			if seen[roomID] {
				t.Errorf("the directory listed %s twice across pages", roomID)
			}
			seen[roomID] = true
		}
		cursor, _ = page["next_batch"].(string)
	}
	if len(seen) != 2 {
		t.Errorf("paging found %d rooms, want both published rooms", len(seen))
	}

	// A token this server did not issue is refused, not read as the first page.
	res = h.raw(t, http.MethodPost, "/_matrix/client/v3/publicRooms", []byte(`{"since":"s42"}`), aliceToken)
	if res.status != http.StatusBadRequest {
		t.Errorf("foreign directory token: status %d, want 400 (body %s)", res.status, res.body)
	}
}

// TestRoomDirectoryAuthority checks who may put a room in the public directory. A member
// with no particular standing cannot advertise a room to the world.
func TestRoomDirectoryAuthority(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "dirmod", "pw-dirmod", "")
	_, bobToken, _ := h.register(t, "dirmember", "pw-dirmember", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": "@dirmember:hs1"}, aliceToken), http.StatusOK)
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)

	res := h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/list/room/"+roomID,
		[]byte(`{"visibility":"public"}`), bobToken)
	if res.status != http.StatusForbidden {
		t.Errorf("publishing by an ordinary member: status %d, want 403 (body %s)", res.status, res.body)
	}
	// The creator can, and the listing changes as a result.
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/list/room/"+roomID,
		[]byte(`{"visibility":"public"}`), aliceToken), http.StatusOK)
	page := h.mustStatus(t, h.get("/_matrix/client/v3/publicRooms", bobToken), http.StatusOK).decoded(t)
	var found bool
	for _, entry := range listedRooms(t, page) {
		if entry["room_id"] == roomID {
			found = true
		}
	}
	if !found {
		t.Errorf("the room was not listed after its creator published it: %v", page["chunk"])
	}
	// Somebody outside the room has no say in it.
	_, carolToken, _ := h.register(t, "dirout", "pw-dirout", "")
	if res := h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/list/room/"+roomID,
		[]byte(`{"visibility":"private"}`), carolToken); res.status != http.StatusForbidden {
		t.Errorf("unpublishing by an outsider: status %d, want 403 (body %s)", res.status, res.body)
	}
}

// TestPublicRoomsOfAnotherServer checks that a directory question about another server is
// answered honestly: this server cannot ask anyone else yet, and it does not pretend that
// its own list is the answer.
func TestPublicRoomsOfAnotherServer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "dirsrv", "pw-dirsrv", "")
	res := h.get("/_matrix/client/v3/publicRooms/elsewhere.example", aliceToken)
	if res.status != http.StatusNotImplemented {
		t.Errorf("another server's directory: status %d, want 501 (body %s)", res.status, res.body)
	}
	if !strings.Contains(strings.ToLower(string(res.body)), "federation") {
		t.Errorf("the refusal should say why it cannot answer: %s", res.body)
	}
	// This server's own name is answerable.
	h.mustStatus(t, h.get("/_matrix/client/v3/publicRooms/hs1", aliceToken), http.StatusOK)
}

// listedRooms decodes the chunk of a directory response.
func listedRooms(t *testing.T, page map[string]any) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(page["chunk"])
	if err != nil {
		t.Fatalf("re-encoding the directory chunk: %v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decoding the directory chunk: %v", err)
	}
	return entries
}

// TestRoomSummaryReportsTheRoom checks the per-room summary: what a room is called, how
// full it is, and which rooms a restricted room lets in.
func TestRoomSummaryReportsTheRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "summaryhost", "pw-summaryhost", "")
	_, bobToken, _ := h.register(t, "summaryguest", "pw-summaryguest", "")
	space := createRoom(t, h, aliceToken, map[string]any{
		"creation_content": map[string]any{"type": "m.space"},
		"name":             "The Space",
	})
	restricted := createRoom(t, h, aliceToken, map[string]any{
		"room_version": "8",
		"name":         "Members of the space only",
		"topic":        "by invitation of the space",
		"initial_state": []map[string]any{
			{"type": "m.room.join_rules", "state_key": "", "content": map[string]any{
				"join_rule": "restricted",
				"allow": []map[string]any{
					{"type": "m.room_membership", "room_id": space},
				},
			}},
		},
	})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+space+"/invite",
		map[string]any{"user_id": "@summaryguest:hs1"}, aliceToken), http.StatusOK)
	h.mustStatus(t, join(t, h, space, bobToken, map[string]any{}), http.StatusOK)

	summary := h.mustStatus(t, h.get("/_matrix/client/v1/room_summary/"+restricted, aliceToken), http.StatusOK).decoded(t)
	if summary["room_id"] != restricted {
		t.Errorf("room_id = %v, want %s", summary["room_id"], restricted)
	}
	if summary["name"] != "Members of the space only" {
		t.Errorf("name = %v", summary["name"])
	}
	if summary["topic"] != "by invitation of the space" {
		t.Errorf("topic = %v", summary["topic"])
	}
	if summary["join_rule"] != "restricted" {
		t.Errorf("join_rule = %v, want restricted", summary["join_rule"])
	}
	if summary["room_version"] != "8" {
		t.Errorf("room_version = %v, want 8", summary["room_version"])
	}
	if value, ok := summary["room_type"]; ok && value != "" {
		t.Errorf("a plain room should report no room type, got %v", value)
	}
	allowed, _ := summary["allowed_room_ids"].([]any)
	if len(allowed) != 1 || allowed[0] != space {
		t.Errorf("allowed_room_ids = %v, want [%s]", summary["allowed_room_ids"], space)
	}
	if members, _ := summary["num_joined_members"].(float64); int(members) != 1 {
		t.Errorf("num_joined_members = %v, want 1", summary["num_joined_members"])
	}

	// A room with no restriction says nothing about allowed rooms: an empty list would
	// read as a restriction with no rooms behind it.
	plain := h.mustStatus(t, h.get("/_matrix/client/v1/room_summary/"+space, aliceToken), http.StatusOK).decoded(t)
	if _, ok := plain["allowed_room_ids"]; ok {
		t.Errorf("an unrestricted room should omit allowed_room_ids: %v", plain)
	}
	if plain["room_type"] != "m.space" {
		t.Errorf("room_type = %v, want m.space", plain["room_type"])
	}

	// A stranger cannot ask about a private room, and is not told whether it exists.
	_, carolToken, _ := h.register(t, "summarystranger", "pw-summarystranger", "")
	if res := h.get("/_matrix/client/v1/room_summary/"+space, carolToken); res.status != http.StatusForbidden {
		t.Errorf("summary for an outsider: status %d, want 403 (body %s)", res.status, res.body)
	}
	// An alias names the same room.
	alias := "#summarised:hs1"
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(alias),
		[]byte(`{"room_id":"`+space+`"}`), aliceToken), http.StatusOK)
	byAlias := h.mustStatus(t, h.get("/_matrix/client/v1/room_summary/"+escAlias(alias), aliceToken), http.StatusOK).decoded(t)
	if byAlias["room_id"] != space {
		t.Errorf("the summary by alias names %v, want %s", byAlias["room_id"], space)
	}
}

// entryFor picks one room's entry out of a directory page.
func entryFor(t *testing.T, entries []map[string]any, roomID string) map[string]any {
	t.Helper()
	for _, entry := range entries {
		if entry["room_id"] == roomID {
			return entry
		}
	}
	t.Fatalf("the directory page does not list %s: %v", roomID, entries)
	return nil
}
