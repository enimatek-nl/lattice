package compat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// joinedTimeline digs the timeline events of one room out of a sync response.
func joinedTimeline(sync Response, roomID string) []any {
	rooms, _ := sync.Body["rooms"].(map[string]any)
	join, _ := rooms["join"].(map[string]any)
	room, _ := join[roomID].(map[string]any)
	timeline, _ := room["timeline"].(map[string]any)
	events, _ := timeline["events"].([]any)
	return events
}

// TestARoomAdmitsItsMembersThroughAnotherRoom is the restricted join rule read end to end: a room
// that admits people through another room, a user who is in that other room and so gets in, the
// same user refused after leaving it, and an invitation still working whatever the rules say.
//
// The second half is what the Complement case that found this was really doing. A room's allow
// list can arrive full of things that are not rules at all - a bare string, the whole key replaced
// by a scalar - and the room cannot answer that with a locked door or a broken request: the
// statement has to land, be ignored, and leave joining needing an invite.
func TestARoomAdmitsItsMembersThroughAnotherRoom(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	allowed := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token,
		map[string]any{"name": "the allowed room", "preset": "public_chat"}).String("room_id")
	room := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token, map[string]any{
		"name":         "the restricted room",
		"preset":       "public_chat",
		"room_version": "8",
		"initial_state": []map[string]any{{
			"type": "m.room.join_rules", "state_key": "",
			"content": map[string]any{"join_rule": "public"},
		}},
	}).String("room_id")

	// The rule that admits members of the allowed room, written the way a client that did not know
	// the room id at creation time writes it.
	setRules := func(content string) string {
		resp := server.DoRaw(http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules/",
			alice.Token, "application/json", []byte(content))
		if resp.Status != http.StatusOK {
			t.Fatalf("setting the join rules %s: %d %s", content, resp.Status, resp.Raw)
		}
		return resp.String("event_id")
	}
	if id := setRules(fmt.Sprintf(`{"join_rule":"restricted","allow":[{"type":"m.room_membership","room_id":%q,"via":["test.server"]}]}`, allowed)); id == "" {
		t.Fatal("setting the restricted join rules returned no event id")
	}

	// Bob is nobody here yet.
	if joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+room, bob.Token, map[string]any{}); joined.Status != http.StatusForbidden {
		t.Fatalf("joining a restricted room without the allowed membership: %d %s, want 403", joined.Status, joined.Raw)
	}

	// Bob is let into the allowed room, and that is now enough.
	if invite := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+allowed+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}); invite.Status != http.StatusOK {
		t.Fatalf("inviting bob to the allowed room: %d %s", invite.Status, invite.Raw)
	}
	if joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+allowed, bob.Token, map[string]any{}); joined.Status != http.StatusOK {
		t.Fatalf("bob joining the allowed room: %d %s", joined.Status, joined.Raw)
	}
	if joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+room, bob.Token, map[string]any{}); joined.Status != http.StatusOK {
		t.Fatalf("bob joining the restricted room through the allowed one: %d %s", joined.Status, joined.Raw)
	}

	// A joined member rewriting their own membership through the state endpoint is not an
	// admission: the room's rules are about getting in, and a member who is already in is not
	// asking to get in again.
	if again := server.DoRaw(http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.member/"+bob.UserID,
		bob.Token, "application/json", []byte(`{"membership":"join","displayname":"Bobby"}`)); again.Status != http.StatusOK {
		t.Errorf("bob rewriting their own membership: %d %s, want 200", again.Status, again.Raw)
	}

	// A joined member rewriting their own membership in the allowed room as well, with a key the
	// room has no use for: implementations put join_authorised_via_users_server in a join, and a
	// join that is already a join ignores it.
	if again := server.DoRaw(http.MethodPut, "/_matrix/client/v3/rooms/"+allowed+"/state/m.room.member/"+bob.UserID,
		bob.Token, "application/json", []byte(`{"membership":"join","displayname":"Bobby"}`)); again.Status != http.StatusOK {
		t.Errorf("bob rewriting their own membership in the allowed room: %d %s, want 200", again.Status, again.Raw)
	}
	if again := server.DoRaw(http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.member/"+bob.UserID,
		bob.Token, "application/json",
		[]byte(`{"membership":"join","displayname":"Bobby","join_authorised_via_users_server":"unused"}`)); again.Status != http.StatusOK {
		t.Errorf("bob rewriting their own membership with an authorising user: %d %s, want 200", again.Status, again.Raw)
	}

	// Leave the allowed room and the restricted one, and the door is shut again.
	for _, id := range []string{room, allowed} {
		if left := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+id+"/leave", bob.Token, map[string]any{}); left.Status != http.StatusOK {
			t.Fatalf("bob leaving %s: %d %s", id, left.Status, left.Raw)
		}
	}
	if joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+room, bob.Token, map[string]any{}); joined.Status != http.StatusForbidden {
		t.Errorf("rejoining after leaving the allowed room: %d %s, want 403", joined.Status, joined.Raw)
	}

	// Invited, in, out again, and back into the allowed room: the membership of a room that
	// admits people through another room changes more often than its rules, and the graph has to
	// keep up.
	if invite := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+room+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}); invite.Status != http.StatusOK {
		t.Fatalf("inviting bob to the restricted room: %d %s", invite.Status, invite.Raw)
	}
	if joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+room, bob.Token, map[string]any{}); joined.Status != http.StatusOK {
		t.Fatalf("bob joining the restricted room on an invite: %d %s", joined.Status, joined.Raw)
	}
	if left := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+room+"/leave", bob.Token, map[string]any{}); left.Status != http.StatusOK {
		t.Fatalf("bob leaving the restricted room again: %d %s", left.Status, left.Raw)
	}
	if joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+allowed, bob.Token, map[string]any{}); joined.Status != http.StatusOK {
		t.Fatalf("bob rejoining the allowed room: %d %s", joined.Status, joined.Raw)
	}

	// A rule written after all that churn still has to reach the clients watching the room - the
	// timeline is where a client looks for what just happened.
	watch := server.Do(http.MethodGet, "/_matrix/client/v3/sync", alice.Token, nil)
	token := watch.String("next_batch")
	mangled := setRules(`{"join_rule":"restricted","allow":["invalid"]}`)
	resumed := server.Do(http.MethodGet, "/_matrix/client/v3/sync?since="+token, alice.Token, nil)
	var ids []string
	for _, raw := range joinedTimeline(resumed, room) {
		event, _ := raw.(map[string]any)
		id, _ := event["event_id"].(string)
		ids = append(ids, id)
	}
	if len(ids) == 0 || ids[len(ids)-1] != mangled {
		t.Errorf("the incremental sync did not put %s last in the timeline of %s: %v", mangled, room, ids)
	}

	// The unusable rules admit nobody: joining needs an invite now.
	if joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+room, bob.Token, map[string]any{}); joined.Status != http.StatusForbidden {
		t.Errorf("joining with a mangled allow list: %d %s, want 403", joined.Status, joined.Raw)
	}
	if allow := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+room+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}); allow.Status != http.StatusOK {
		t.Fatalf("inviting bob to the restricted room: %d %s", allow.Status, allow.Raw)
	}
	if joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+room, bob.Token, map[string]any{}); joined.Status != http.StatusOK {
		t.Errorf("joining with an invite while the allow list is nonsense: %d %s, want 200", joined.Status, joined.Raw)
	}

	// A join rules event whose content is not an object at all is refused as a bad request rather
	// than as a server error: the client wrote something that is not a join rules event.
	if broken := server.DoRaw(http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules/",
		alice.Token, "application/json", []byte(`"not an object"`)); broken.Status == http.StatusInternalServerError {
		t.Errorf("a join rules event that is not an object: %d %s, want a client error", broken.Status, broken.Raw)
	}

	state := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+room+"/state", alice.Token, nil)
	var current string
	for _, raw := range state.Array {
		event, _ := raw.(map[string]any)
		if event["type"] != "m.room.join_rules" {
			continue
		}
		content, _ := json.Marshal(event["content"])
		current = string(content)
	}
	if !strings.Contains(current, "invalid") {
		t.Errorf("the room's join rules are %s, which is not the mangled rule it last wrote", current)
	}
}
