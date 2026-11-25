package compat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// slidingList is the list config of a sliding sync request, written as the wire object rather
// than as a Go type: these tests are about what a client sends and what the answer says, and a
// field the server invents is exactly what they are looking for.
type slidingList map[string]any

// slidingSync performs a sliding sync request and fails the test unless it was accepted.
func (s *Server) slidingSync(t *testing.T, session *Session, body map[string]any) map[string]any {
	t.Helper()
	resp := s.MustDo("POST", "/_matrix/client/v4/sync", session.Token, body, 200)
	return resp.Body
}

// slidingRooms reads the rooms section of a sliding sync answer.
func slidingRooms(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	rooms, ok := body["rooms"].(map[string]any)
	if !ok {
		t.Fatalf("the answer carries no rooms section: %v", body)
	}
	out := make(map[string]map[string]any, len(rooms))
	for roomID, raw := range rooms {
		room, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("the room %s is not an object: %v", roomID, raw)
		}
		out[roomID] = room
	}
	return out
}

// slidingListCount reads the count a list reported.
func slidingListCount(t *testing.T, body map[string]any, name string) int {
	t.Helper()
	lists, ok := body["lists"].(map[string]any)
	if !ok {
		t.Fatalf("the answer carries no lists section: %v", body)
	}
	list, ok := lists[name].(map[string]any)
	if !ok {
		t.Fatalf("the answer names no list %q", name)
	}
	count, ok := list["count"].(float64)
	if !ok {
		t.Fatalf("the list %q reports no count: %v", name, list)
	}
	return int(count)
}

// TestSlidingSyncInitialWindow looks at the endpoint at its plainest: a window over the rooms
// of an account, each carrying the state the client asked for and the last event of the room.
//
// The count is the part that is easy to get wrong and impossible to notice later: it is the
// size of the filtered list, not the number of rooms the response happened to carry, because a
// client decides how far to grow its window from it.
func TestSlidingSyncInitialWindow(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	rooms := map[string]string{}
	for _, name := range []string{"Down", "Up", "Through"} {
		roomID := server.CreateRoom(alice, map[string]any{
			"preset": "public_chat",
			"name":   name,
		})
		rooms[name] = roomID
		server.Send(alice, roomID, "said "+name, "a message in "+name)
	}

	answer := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]any{
			"all": map[string]any{
				"range":          []int{0, 1},
				"timeline_limit": 1,
				"required_state": map[string]any{
					"include": []map[string]any{{"type": "m.room.create", "state_key": ""}},
				},
			},
		},
	})

	if answer["pos"] == nil || answer["pos"] == "" {
		t.Fatalf("the answer carries no position to continue from: %v", answer)
	}
	if count := slidingListCount(t, answer, "all"); count != 3 {
		t.Errorf("the list counts %d rooms, want 3: the count ignores the range", count)
	}
	found := slidingRooms(t, answer)
	if len(found) != 2 {
		t.Fatalf("the window asked for 2 rooms and %d arrived", len(found))
	}
	for _, room := range found {
		if room["initial"] != true {
			t.Errorf("a room of a first answer is not marked initial: %v", room)
		}
		if room["membership"] != "join" {
			t.Errorf("the room reports membership %v, want join", room["membership"])
		}
		timeline, _ := room["timeline"].([]any)
		if len(timeline) != 1 {
			t.Errorf("the room carries %d timeline events, want the 1 the config asked for", len(timeline))
		}
		state, _ := room["required_state"].([]any)
		if len(state) != 1 {
			t.Fatalf("the room carries %d required state events, want the create event only", len(state))
		}
		entry, _ := state[0].(map[string]any)
		if entry["type"] != "m.room.create" {
			t.Errorf("the required state holds %v, want m.room.create", entry["type"])
		}
		if room["prev_batch"] == nil || room["prev_batch"] == "" {
			t.Errorf("the room answers no prev_batch to page backwards from: %v", room)
		}
		if room["joined_count"] == nil {
			t.Errorf("a first sight of a room does not report its member count: %v", room)
		}
		if room["bump_stamp"] == nil {
			t.Errorf("a first sight of a room does not report its activity: %v", room)
		}
	}
	// A room here because a list matched it says which list, so a client that keeps several
	// can tell where a room came from.
	for _, room := range found {
		lists, _ := room["lists"].([]any)
		if len(lists) != 1 || lists[0] != "all" {
			t.Errorf("the room does not name the list that matched it: %v", room["lists"])
		}
	}
	// The name of the room is one the client was shown, and the room has one.
	var names []string
	for _, room := range found {
		if name, ok := room["name"].(string); ok {
			names = append(names, name)
		}
	}
	if len(names) != 2 {
		t.Errorf("only %d of the 2 rooms brought a name", len(names))
	}
}

// TestSlidingSyncGrowingRange pages the room list the way a client does: a small window first,
// then a wider one. The rooms already sent are not sent again, and the ones that were outside
// the window arrive marked as new.
func TestSlidingSyncGrowingRange(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	for _, name := range []string{"Down", "Up", "Through", "Looking"} {
		roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": name})
		server.Send(alice, roomID, "said "+name, "a message in "+name)
	}

	first := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 1}, "timeline_limit": 1},
		},
	})
	seen := slidingRooms(t, first)
	if len(seen) != 2 {
		t.Fatalf("the first window sent %d rooms, want 2", len(seen))
	}
	pos, _ := first["pos"].(string)

	second := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"pos":     pos,
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 3}, "timeline_limit": 1},
		},
	})
	grown := slidingRooms(t, second)
	if len(grown) != 2 {
		t.Fatalf("widening the window sent %d rooms, want the 2 that had not been sent: %v", len(grown), grown)
	}
	for roomID, room := range grown {
		if _, already := seen[roomID]; already {
			t.Errorf("%s was sent again after the window grew", roomID)
			continue
		}
		if room["initial"] != true {
			t.Errorf("%s arrives for the first time but is not marked initial", roomID)
		}
	}
	if count := slidingListCount(t, second, "all"); count != 4 {
		t.Errorf("the widened list counts %d rooms, want 4", count)
	}
}

// TestSlidingSyncDeltaCarriesOnlyWhatMoved is the promise the endpoint exists for: a room
// that did not move is not in the answer, and the fields of a room that did are not repeated
// when they did not change.
func TestSlidingSyncDeltaCarriesOnlyWhatMoved(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	first := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Down"})
	second := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Up"})
	server.Send(alice, first, "one", "first message")
	server.Send(alice, second, "two", "second message")

	answer := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 5},
		},
	})
	rooms := slidingRooms(t, answer)
	if len(rooms) != 2 {
		t.Fatalf("the first answer sent %d rooms, want 2", len(rooms))
	}
	pos, _ := answer["pos"].(string)

	server.Send(alice, first, "three", "a message in one room only")

	next := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"pos":     pos,
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 5},
		},
	})
	changed := slidingRooms(t, next)
	if len(changed) != 1 {
		t.Fatalf("the delta carries %d rooms, want the one that moved: %v", len(changed), changed)
	}
	room, ok := changed[first]
	if !ok {
		t.Fatalf("the delta carries %v instead of the room that received a message", keysOf(changed))
	}
	if room["initial"] != nil {
		t.Errorf("a room the connection has seen is marked initial again: %v", room["initial"])
	}
	timeline, _ := room["timeline"].([]any)
	if len(timeline) != 1 {
		t.Errorf("the delta carries %d timeline events, want only the new one", len(timeline))
	}
	if live, _ := room["num_live"].(float64); int(live) != 1 {
		t.Errorf("the room reports num_live %v, want 1: the new event just happened", room["num_live"])
	}
	// The name did not change, so it is not repeated: an omitted field of a delta means
	// "still what you have".
	if _, repeated := room["name"]; repeated {
		t.Errorf("the delta repeats the room name, which did not change: %v", room["name"])
	}
	if room["prev_batch"] == nil {
		t.Errorf("the delta carries no prev_batch to page from")
	}
}

// TestSlidingSyncPosIsABatchToken checks the promise that the position of a sliding sync
// connection can be handed to /messages. It is what lets a client that only ever used sliding
// sync read a room's older events, and it is easy to break by making the token opaque in the
// wrong way.
func TestSlidingSyncPosIsABatchToken(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	for i := 0; i < 4; i++ {
		server.Send(alice, roomID, "message", "body")
	}
	answer := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 2},
		},
	})
	pos, _ := answer["pos"].(string)
	page := server.MustDo("GET", "/_matrix/client/v3/rooms/"+roomID+"/messages?from="+pos+"&dir=b&limit=10",
		alice.Token, nil, 200)
	events, _ := page.Body["chunk"].([]any)
	if len(events) == 0 {
		t.Fatalf("the sliding sync position %q pages no events from %s", pos, roomID)
	}
}

// TestSlidingSyncRefusesAPositionThatIsNotItsOwn covers the security rule of the connection
// model: holding a position token grants nothing. A position issued to another device, or to
// another user, or an older position of this same connection, is refused with M_UNKNOWN_POS.
func TestSlidingSyncRefusesAPositionThatIsNotItsOwn(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	eve := server.Register("eve", "curious")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.CreateRoom(eve, map[string]any{"preset": "public_chat"})
	server.Send(alice, roomID, "one", "body")

	aliceAnswer := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"room_subscriptions": map[string]any{
			roomID: map[string]any{"timeline_limit": 1},
		},
	})
	alicePos, _ := aliceAnswer["pos"].(string)

	// The same connection name on another device of the same user: a different connection.
	again := server.Login("alice", "wonderland")
	if resp := server.Do("POST", "/_matrix/client/v4/sync", again.Token, map[string]any{
		"conn_id": "main",
		"pos":     alicePos,
	}); resp.Status != 400 || resp.ErrCode() != "M_UNKNOWN_POS" {
		t.Errorf("a second device answering with the first device's position: got %d %s, want 400 M_UNKNOWN_POS",
			resp.Status, resp.ErrCode())
	}

	// Another user, who has no right to be told anything about alice's rooms.
	if resp := server.Do("POST", "/_matrix/client/v4/sync", eve.Token, map[string]any{
		"conn_id": "main",
		"pos":     alicePos,
	}); resp.Status != 400 || resp.ErrCode() != "M_UNKNOWN_POS" {
		t.Errorf("another user holding a position token: got %d %s, want 400 M_UNKNOWN_POS",
			resp.Status, resp.ErrCode())
	}

	// A position this connection has already moved past: the client that replays one must not
	// be answered as though nothing had happened in between.
	next := server.slidingSync(t, alice, map[string]any{"conn_id": "main", "pos": alicePos})
	nextPos, _ := next["pos"].(string)
	if nextPos == alicePos {
		t.Fatalf("the connection answered the same position twice: %q", nextPos)
	}
	if resp := server.Do("POST", "/_matrix/client/v4/sync", alice.Token, map[string]any{
		"conn_id": "main",
		"pos":     alicePos,
	}); resp.Status != 400 || resp.ErrCode() != "M_UNKNOWN_POS" {
		t.Errorf("replaying a spent position: got %d %s, want 400 M_UNKNOWN_POS",
			resp.Status, resp.ErrCode())
	}
}

// TestSlidingSyncRefusesARidiculousRequest checks the limits the endpoint states: a hundred
// lists is a bug or an attack, and an unknown presence value is a client that mistyped one.
func TestSlidingSyncRefusesARidiculousRequest(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	lists := map[string]slidingList{}
	for i := 0; i < 101; i++ {
		lists[listName(i)] = slidingList{"timeline_limit": 1}
	}
	if resp := server.Do("POST", "/_matrix/client/v4/sync", alice.Token, map[string]any{"lists": lists}); resp.Status != 400 ||
		resp.ErrCode() != "M_INVALID_PARAM" {
		t.Errorf("101 lists: got %d %s, want 400 M_INVALID_PARAM", resp.Status, resp.ErrCode())
	}

	subscriptions := map[string]any{}
	for i := 0; i < 101; i++ {
		subscriptions["!room"+listName(i)+":example.com"] = slidingList{"timeline_limit": 1}
	}
	if resp := server.Do("POST", "/_matrix/client/v4/sync", alice.Token,
		map[string]any{"room_subscriptions": subscriptions}); resp.Status != 400 ||
		resp.ErrCode() != "M_INVALID_PARAM" {
		t.Errorf("101 subscriptions: got %d %s, want 400 M_INVALID_PARAM", resp.Status, resp.ErrCode())
	}

	if resp := server.Do("POST", "/_matrix/client/v4/sync", alice.Token,
		map[string]any{"set_presence": "asleep"}); resp.Status != 400 || resp.ErrCode() != "M_INVALID_PARAM" {
		t.Errorf("an unknown presence value: got %d %s, want 400 M_INVALID_PARAM", resp.Status, resp.ErrCode())
	}

	if resp := server.Do("POST", "/_matrix/client/v4/sync", alice.Token, map[string]any{
		"lists": map[string]slidingList{"bad": {"range": []int{5, 1}, "timeline_limit": 1}},
	}); resp.Status != 400 || resp.ErrCode() != "M_INVALID_PARAM" {
		t.Errorf("a range that ends before it starts: got %d %s, want 400 M_INVALID_PARAM",
			resp.Status, resp.ErrCode())
	}
}

// TestSlidingSyncUnstablePath checks that the unstable name of the endpoint serves the same
// thing, and that /versions says so.
func TestSlidingSyncUnstablePath(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})

	versions := server.MustDo("GET", "/_matrix/client/versions", "", nil, 200)
	features, _ := versions.Body["unstable_features"].(map[string]any)
	if features["org.matrix.simplified_msc3575"] != true {
		t.Errorf("/versions does not advertise simplified sliding sync: %v", features)
	}

	answer := server.MustDo("POST", "/_matrix/client/unstable/org.matrix.simplified_msc3575/sync",
		alice.Token, map[string]any{
			"conn_id":            "main",
			"room_subscriptions": map[string]any{roomID: map[string]any{"timeline_limit": 1}},
		}, 200)
	if rooms := slidingRooms(t, answer.Body); len(rooms) != 1 {
		t.Errorf("the unstable path returned %d rooms, want the one subscribed to", len(rooms))
	}
}

// listName makes one of the many list names a limit test needs.
func listName(i int) string {
	return "list" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + itoa(i)
}

// itoa is a local itoa so the limit tests do not import strconv for one call.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// keysOf renders a map's keys for a failure message.
func keysOf(values map[string]map[string]any) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

// TestSlidingSyncFiltersTheList asks the filters, one account holding a space, a room inside
// it, a tagged room and a direct room. A filter is not decoration on the returned rooms: it
// decides which rooms the list contains at all, and the count the list reports is the filtered
// size, which is what a client grows its window from.
func TestSlidingSyncFiltersTheList(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")

	space := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Gardens", "type": "m.space"})
	inside := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Inside"})
	outside := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Outside"})
	direct := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Direct"})
	server.MustDo("PUT", roomAccountDataPath(alice.UserID, direct, "m.tag"), alice.Token,
		map[string]any{"tags": map[string]any{"favourite": map[string]any{}}}, 200)
	server.MustDo("PUT", accountDataPath(alice.UserID, "m.direct"), alice.Token,
		map[string]any{bob.UserID: []any{direct}}, 200)
	server.MustDo("PUT", "/_matrix/client/v3/rooms/"+space+"/state/m.space.child/"+url.PathEscape(inside),
		alice.Token, map[string]any{"via": []any{"test.server"}}, 200)
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+outside+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, 200)

	for name, filter := range map[string]map[string]any{
		"m.space":       {"room_types": []any{"m.space"}},
		"not_space":     {"not_room_types": []any{"m.space"}},
		"dm":            {"is_dm": true},
		"not_dm":        {"is_dm": false},
		"favourites":    {"tags": []any{"favourite"}},
		"not_favourite": {"not_tags": []any{"favourite"}},
		"gardens":       {"spaces": []any{space}},
	} {
		answer := server.slidingSync(t, alice, map[string]any{
			"conn_id": "filter-" + name,
			"lists": map[string]slidingList{
				"all": {"timeline_limit": 0, "filters": filter},
			},
		})
		got := roomNames(slidingRooms(t, answer))
		switch name {
		case "m.space":
			wantNames(t, got, "Gardens")
		case "not_space":
			wantNames(t, got, "Direct", "Inside", "Outside")
		case "dm":
			wantNames(t, got, "Direct")
		case "not_dm":
			wantNames(t, got, "Gardens", "Inside", "Outside")
		case "favourites":
			wantNames(t, got, "Direct")
		case "not_favourite":
			wantNames(t, got, "Gardens", "Inside", "Outside")
		case "gardens":
			wantNames(t, got, "Inside")
		}
		if count := slidingListCount(t, answer, "all"); count != len(got) {
			t.Errorf("the %s list counts %d rooms and returns %d", name, count, len(got))
		}
	}

	// The invitation filter is asked of the invited user, because it is a question about the
	// person looking, not about the room.
	invited := server.slidingSync(t, bob, map[string]any{
		"conn_id": "invites",
		"lists": map[string]slidingList{
			"invites": {"timeline_limit": 0, "filters": map[string]any{"is_invited": true}},
		},
	})
	rooms := slidingRooms(t, invited)
	if len(rooms) != 1 {
		t.Fatalf("bob's invited list holds %d rooms, want the one he was invited to", len(rooms))
	}
	for _, room := range rooms {
		if room["membership"] != "invite" {
			t.Errorf("the invited list reports membership %v", room["membership"])
		}
		if room["stripped_state"] == nil {
			t.Errorf("an invitation is answered without the stripped state that names the room: %v", room)
		}
		if room["required_state"] != nil {
			t.Errorf("an invitation the user has not accepted is answered with the room's state as well")
		}
	}
}

// TestSlidingSyncSubscription follows a room the lists do not cover: the client opened it, so
// it wants it whatever its position in the activity order. A room the user may not see is
// ignored rather than answered, because the answer would be a leak.
func TestSlidingSyncSubscription(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	quiet := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Quiet"})
	loud := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Loud"})
	for i := 0; i < 3; i++ {
		server.Send(alice, loud, "loud"+itoa(i), "noise")
	}

	answer := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"top": {"range": []int{0, 0}, "timeline_limit": 1},
		},
		"room_subscriptions": map[string]any{
			quiet:                        map[string]any{"timeline_limit": 2},
			"!nobody-knows-this:example": map[string]any{"timeline_limit": 5},
		},
	})
	rooms := slidingRooms(t, answer)
	if len(rooms) != 2 {
		t.Fatalf("the answer carries %d rooms, want the list room and the subscribed one", len(rooms))
	}
	subscribed, ok := rooms[quiet]
	if !ok {
		t.Fatalf("the subscribed room is missing: %v", keysOf(rooms))
	}
	if lists, _ := subscribed["lists"].([]any); len(lists) != 0 {
		t.Errorf("a room here only because it was subscribed to names lists: %v", lists)
	}
	if timeline, _ := subscribed["timeline"].([]any); len(timeline) != 2 {
		t.Errorf("the subscription asked for 2 events and carries %d", len(timeline))
	}
	// A room alice is not in, and has never been in, is not answered at all: a subscription
	// is a request, not a permission.
	if resp := server.Do("POST", "/_matrix/client/v4/sync", bob.Token, map[string]any{
		"conn_id":            "snooping",
		"room_subscriptions": map[string]any{quiet: map[string]any{"timeline_limit": 5}},
	}); resp.Status != 200 {
		t.Fatalf("bob asking for a room he is not in: got %d %s", resp.Status, resp.ErrCode())
	} else if rooms := resp.Body["rooms"]; rooms != nil {
		t.Errorf("bob was told about a room he is not in: %v", rooms)
	}
}

// TestSlidingSyncWiderWindowIsExpanded is the config-change rule: a client that asks for more
// history than the connection has been given gets it in one go, older events included, and the
// response says so. Without the flag a client cannot tell a backfill from a room it has been
// watching all along.
func TestSlidingSyncWiderWindowIsExpanded(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Down"})
	for i := 0; i < 5; i++ {
		server.Send(alice, roomID, "msg"+itoa(i), "body "+itoa(i))
	}

	first := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 1},
		},
	})
	rooms := slidingRooms(t, first)
	room, ok := rooms[roomID]
	if !ok {
		t.Fatalf("the room is missing from the first answer")
	}
	if timeline, _ := room["timeline"].([]any); len(timeline) != 1 {
		t.Fatalf("the narrow window carries %d events, want 1", len(timeline))
	}
	pos, _ := first["pos"].(string)

	wide := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"pos":     pos,
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 6},
		},
	})
	expanded := slidingRooms(t, wide)[roomID]
	if expanded["expanded_timeline"] != true {
		t.Errorf("the widened window does not say it expanded: %v", keysOfValue(expanded))
	}
	timeline, _ := expanded["timeline"].([]any)
	if len(timeline) < 5 {
		t.Errorf("the widened window carries %d events, want the five messages backfilled in", len(timeline))
	}
	if live, ok := expanded["num_live"].(float64); ok && live != 0 {
		t.Errorf("the widened window reports %v live events, want none: nothing just happened", live)
	}
}

// TestSlidingSyncLazyMembers asks for everything except the membership list, and for the
// memberships a client needs to render what it is being shown.
func TestSlidingSyncLazyMembers(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Down"})
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, 200)
	server.MustDo("POST", "/_matrix/client/v3/join/"+url.PathEscape(roomID), bob.Token, nil, 200)
	server.Send(bob, roomID, "bobs", "bob speaks")
	server.Send(alice, roomID, "alices", "alice speaks")

	answer := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"all": {
				"range":          []int{0, 9},
				"timeline_limit": 2,
				"required_state": map[string]any{"include": []map[string]any{{}}, "lazy_members": true},
			},
		},
	})
	room := slidingRooms(t, answer)[roomID]
	if room == nil {
		t.Fatalf("the room is missing")
	}
	state, _ := room["required_state"].([]any)
	members := map[string]bool{}
	var types []string
	for _, raw := range state {
		entry, _ := raw.(map[string]any)
		typ, _ := entry["type"].(string)
		types = append(types, typ)
		if typ == "m.room.member" {
			key, _ := entry["state_key"].(string)
			members[key] = true
		}
	}
	if len(types) == 0 {
		t.Fatalf("no required state arrived")
	}
	// The two speakers in the window, and nobody else's membership.
	if !members[alice.UserID] || !members[bob.UserID] {
		t.Errorf("the lazy members do not cover the senders of the window: %v", members)
	}
	// And a state event of another type still arrives, because "include everything" is not
	// narrowed by lazy loading.
	var sawName bool
	for _, typ := range types {
		if typ == "m.room.name" {
			sawName = true
		}
	}
	if !sawName {
		t.Errorf("lazy loading dropped the room's ordinary state: %v", types)
	}
}

// TestSlidingSyncDeletedStateArrivesAsAStub takes a state event away again. A client that is
// not told the difference between "this did not change" and "this is gone" keeps showing a room
// name that no longer exists.
func TestSlidingSyncDeletedStateArrivesAsAStub(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})

	first := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"all": {
				"range":          []int{0, 9},
				"timeline_limit": 0,
				"required_state": map[string]any{"include": []map[string]any{{"type": "m.room.name"}}},
			},
		},
	})
	// Set a name after the connection began, so the delta is the thing under test.
	nameEvent := server.MustDo("PUT", "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.name",
		alice.Token, map[string]any{"name": "Was Called Down"}, 200).String("event_id")
	pos, _ := first["pos"].(string)

	second := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"pos":     pos,
		"lists": map[string]slidingList{
			"all": {
				"range":          []int{0, 9},
				"timeline_limit": 0,
				"required_state": map[string]any{"include": []map[string]any{{"type": "m.room.name"}}},
			},
		},
	})
	room := slidingRooms(t, second)[roomID]
	if room == nil {
		t.Fatalf("the room whose name changed is not in the delta at all")
	}
	state, _ := room["required_state"].([]any)
	if len(state) != 1 {
		t.Fatalf("the delta carries %d state entries, want the new name", len(state))
	}
	entry, _ := state[0].(map[string]any)
	if entry["type"] != "m.room.name" || entry["content"] == nil {
		t.Fatalf("the delta did not carry the new name: %v", entry)
	}
	pos, _ = second["pos"].(string)

	// Redact it. The state key stays - a redacted event still holds the key - and the answer
	// has to name the disappearance rather than stay quiet about it.
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/redact/"+url.PathEscape(nameEvent),
		alice.Token, map[string]any{"reason": "took it back"}, 200)

	third := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"pos":     pos,
		"lists": map[string]slidingList{
			"all": {
				"range":          []int{0, 9},
				"timeline_limit": 0,
				"required_state": map[string]any{"include": []map[string]any{{"type": "m.room.name"}}},
			},
		},
	})
	room = slidingRooms(t, third)[roomID]
	if room == nil {
		t.Fatalf("redacting the name left the room out of the answer")
	}
	state, _ = room["required_state"].([]any)
	if len(state) != 1 {
		t.Fatalf("the answer carries %d state entries, want one stub: %v", len(state), state)
	}
	stub, _ := state[0].(map[string]any)
	if stub["type"] != "m.room.name" {
		t.Errorf("the stub names %v, want m.room.name", stub["type"])
	}
	if _, hasContent := stub["content"]; hasContent {
		t.Errorf("the stub carries content, which is what tells a client to delete the entry: %v", stub)
	}
	if _, hasKey := stub["state_key"]; !hasKey {
		t.Errorf("the stub names no state key: %v", stub)
	}
}

// TestSlidingSyncExtensions asks for the sections that are not rooms and checks each one
// carries what the classic sync carries for the same thing.
func TestSlidingSyncExtensions(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, 200)
	message := server.Send(alice, roomID, "one", "a message")
	server.MustDo("POST", "/_matrix/client/v3/join/"+url.PathEscape(roomID), bob.Token, nil, 200)

	server.MustDo("PUT", roomAccountDataPath(bob.UserID, roomID, "test.room.data"), bob.Token,
		map[string]any{"colour": "blue"}, 200)
	server.MustDo("PUT", accountDataPath(bob.UserID, "test.global.data"), bob.Token,
		map[string]any{"who": "bob"}, 200)
	server.MustDo("PUT", "/_matrix/client/v3/rooms/"+roomID+"/typing/"+bob.UserID, bob.Token,
		map[string]any{"typing": true, "timeout": 30000}, 200)
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+url.PathEscape(message),
		bob.Token, map[string]any{}, 200)
	server.MustDo("PUT", "/_matrix/client/v3/sendToDevice/test.secret/txn1", alice.Token,
		map[string]any{"messages": map[string]any{bob.UserID: map[string]any{"*": map[string]any{"hello": "bob"}}}}, 200)
	server.MustDo("POST", "/_matrix/client/v3/keys/upload", bob.Token, map[string]any{
		"one_time_keys": map[string]any{"signed_curve25519:AAAAAA": map[string]any{"key": "AAA"}},
	}, 200)

	answer := server.slidingSync(t, bob, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 5},
		},
		"extensions": map[string]any{
			"to_device":    map[string]any{"enabled": true},
			"e2ee":         map[string]any{"enabled": true},
			"account_data": map[string]any{"enabled": true},
			"receipts":     map[string]any{"enabled": true},
			"typing":       map[string]any{"enabled": true},
		},
	})
	extensions, ok := answer["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("the answer carries no extensions section: %v", answer)
	}

	toDevice, ok := extensions["to_device"].(map[string]any)
	if !ok {
		t.Fatalf("no to_device section")
	}
	// The stable path answers MSC4538, which names the list of messages `messages`; `events` is
	// the Synapse shape and belongs to the unstable URL.
	events, _ := toDevice["messages"].([]any)
	if len(events) != 1 {
		t.Errorf("to_device carries %d messages, want the one queued for this device", len(events))
	}
	if next, _ := toDevice["next_batch"].(string); next == "" {
		t.Errorf("to_device answers no next_batch for the client to acknowledge with")
	}

	e2ee, ok := extensions["e2ee"].(map[string]any)
	if !ok {
		t.Fatalf("no e2ee section")
	}
	counts, _ := e2ee["device_one_time_keys_count"].(map[string]any)
	if count, _ := counts["signed_curve25519"].(float64); count != 1 {
		t.Errorf("e2ee reports one-time key counts %v, want one signed_curve25519 key", counts)
	}

	accountData, ok := extensions["account_data"].(map[string]any)
	if !ok {
		t.Fatalf("no account_data section")
	}
	global, _ := accountData["global"].([]any)
	if !accountsHold(global, "test.global.data") {
		t.Errorf("the global account data section misses what was set: %v", global)
	}
	byRoom, _ := accountData["rooms"].(map[string]any)
	if !accountsHold(byRoom[roomID], "test.room.data") {
		t.Errorf("the room account data section misses what was set: %v", byRoom)
	}

	receipts, ok := extensions["receipts"].(map[string]any)
	if !ok {
		t.Fatalf("no receipts section")
	}
	if rooms, _ := receipts["rooms"].(map[string]any); rooms[roomID] == nil {
		t.Errorf("the receipts section carries no receipt for %s: %v", roomID, receipts)
	}

	typing, ok := extensions["typing"].(map[string]any)
	if !ok {
		t.Fatalf("no typing section")
	}
	if rooms, _ := typing["rooms"].(map[string]any); rooms[roomID] == nil {
		t.Errorf("the typing section carries nothing for %s: %v", roomID, typing)
	}
}

// TestSlidingSyncWaitsForSomethingToSay is the long poll: a request with a position and a
// timeout goes to sleep and wakes when the account moves. A client that had to ask again every
// second would use the endpoint it replaced instead.
func TestSlidingSyncWaitsForSomethingToSay(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Down"})
	server.Send(alice, roomID, "one", "first")

	first := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 5},
		},
	})
	pos, _ := first["pos"].(string)

	go func() {
		// Give the request a moment to be waiting before the room moves.
		time.Sleep(300 * time.Millisecond)
		server.Send(alice, roomID, "two", "second")
	}()
	started := time.Now()
	second := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"pos":     pos,
		"timeout": 5000,
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 5},
		},
	})
	rooms := slidingRooms(t, second)
	if len(rooms) == 0 {
		t.Fatalf("the long poll returned with nothing after the room moved")
	}
	if room := rooms[roomID]; room != nil {
		if live, _ := room["num_live"].(float64); int(live) != 1 {
			t.Errorf("the woken answer reports %v live events, want 1", room["num_live"])
		}
	}
	if started.After(time.Now()) {
		t.Errorf("the clock went backwards")
	}

	// And with nothing to report, the same request comes back when its timeout says to, with
	// an empty rooms section rather than an error.
	pos, _ = second["pos"].(string)
	waited := time.Now()
	third := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"pos":     pos,
		"timeout": 700,
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 5},
		},
	})
	if elapsed := time.Since(waited); elapsed < 600*time.Millisecond {
		t.Errorf("an idle long poll returned after %v, want it to wait for the timeout", elapsed)
	}
	if rooms := third["rooms"]; rooms != nil {
		t.Errorf("an idle long poll carried rooms: %v", rooms)
	}
	if third["pos"] == nil {
		t.Errorf("an idle long poll answers no position to continue from")
	}
}

// roomNames collects the names of the rooms an answer carries, for the filter comparisons.
func roomNames(rooms map[string]map[string]any) []string {
	out := make([]string, 0, len(rooms))
	for _, room := range rooms {
		if name, ok := room["name"].(string); ok {
			out = append(out, name)
		} else {
			out = append(out, "")
		}
	}
	sort.Strings(out)
	return out
}

// wantNames fails unless the rooms an answer carries are exactly the ones wanted.
func wantNames(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the list holds %v, want %v", got, want)
	}
}

// accountsHold reports whether a list of account data entries names a type.
func accountsHold(events any, typ string) bool {
	list, ok := events.([]any)
	if !ok {
		return false
	}
	for _, raw := range list {
		entry, _ := raw.(map[string]any)
		if entry["type"] == typ {
			return true
		}
	}
	return false
}

// keysOfValue renders an object's keys for a failure message.
func keysOfValue(object map[string]any) []string {
	out := make([]string, 0, len(object))
	for key := range object {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// TestSlidingSyncTimelineCarriesThreadSummary checks that a room read through sliding sync is
// read with the same annotations as anywhere else: a thread reply arrives with its summary on
// the root, because a client that had to ask /relations for what /sync already showed would
// not use this endpoint.
func TestSlidingSyncTimelineCarriesThreadSummary(t *testing.T) {
	alice, server, roomID := threadRoom(t)
	answer := server.slidingSync(t, alice, map[string]any{
		"conn_id": "main",
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 20},
		},
	})
	room := slidingRooms(t, answer)[roomID]
	if room == nil {
		t.Fatalf("the room is missing from the answer")
	}
	timeline, _ := room["timeline"].([]any)
	var root map[string]any
	for _, raw := range timeline {
		event, _ := raw.(map[string]any)
		if event["msgbody"] == nil && event["content"] != nil {
			content, _ := event["content"].(map[string]any)
			if content["body"] == "the root message" {
				root = event
			}
		}
	}
	if root == nil {
		t.Fatalf("the root of the thread is not in the window: %d events", len(timeline))
	}
	unsigned, _ := root["unsigned"].(map[string]any)
	summary, _ := unsigned["m.thread"].(map[string]any)
	if count, _ := summary["num_events"].(float64); int(count) < 2 {
		t.Errorf("the root of a two-message thread reports %v events: %v", summary["num_events"], unsigned)
	}
}

// threadRoom makes a room with a thread of two replies in it, and returns the sender, the
// server and the room.
func threadRoom(t *testing.T) (*Session, *Server, string) {
	t.Helper()
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	root := server.Send(alice, roomID, "root", "the root message")
	for i := 0; i < 2; i++ {
		server.MustDo("PUT", "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/txn-reply"+itoa(i),
			alice.Token, map[string]any{
				"msgtype": "m.text",
				"body":    "reply " + itoa(i),
				"m.relates_to": map[string]any{
					"rel_type": "m.thread", "event_id": root, "is_falling_back": true,
				},
			}, 200)
	}
	return alice, server, roomID
}

// TestSlidingSyncExtensionsDoNotKeepThePollAwake is the same long poll with the sections that
// always answer switched on. The one-time-key counts are sent on every request - a client
// decides whether to upload more keys from them - so a server that reads "the response has an
// extensions section" as "something happened" would never let a key-watching connection wait.
func TestSlidingSyncExtensionsDoNotKeepThePollAwake(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.Send(alice, roomID, "one", "first")
	server.MustDo("POST", "/_matrix/client/v3/keys/upload", alice.Token, map[string]any{
		"one_time_keys": map[string]any{"signed_curve25519:AAAAAA": map[string]any{"key": "AAA"}},
	}, 200)

	body := func(pos string) map[string]any {
		request := map[string]any{
			"conn_id": "keys",
			"lists": map[string]slidingList{
				"all": {"range": []int{0, 9}, "timeline_limit": 5},
			},
			"extensions": map[string]any{
				"e2ee":         map[string]any{"enabled": true},
				"account_data": map[string]any{"enabled": true},
				"receipts":     map[string]any{"enabled": true},
				"typing":       map[string]any{"enabled": true},
			},
		}
		if pos != "" {
			request["pos"] = pos
			request["timeout"] = 700
		}
		return request
	}

	first := server.slidingSync(t, alice, body(""))
	extensions, _ := first["extensions"].(map[string]any)
	if extensions == nil {
		t.Fatalf("the extensions were not answered: %v", first)
	}
	if _, ok := extensions["e2ee"].(map[string]any); !ok {
		t.Fatalf("no e2ee section in %v", extensions)
	}
	pos, _ := first["pos"].(string)

	waited := time.Now()
	second := server.slidingSync(t, alice, body(pos))
	if elapsed := time.Since(waited); elapsed < 600*time.Millisecond {
		t.Errorf("a connection with the always-answered sections returned after %v, want it to wait", elapsed)
	}
	if third := second["rooms"]; third != nil {
		t.Errorf("an idle poll carried rooms: %v", third)
	}
	// The sections are still answered while waiting: a client polling for keys needs the
	// counts even when no room moved.
	if extensions, _ := second["extensions"].(map[string]any); extensions["e2ee"] == nil {
		t.Errorf("the waiting answer carries no key section: %v", second)
	}
}

// TestSlidingSyncReadsTheProxyTokens answers for the clients that learned sliding sync from the
// proxy and still write "$ME" and "$LAZY" where a state key belongs. MSC4186 defines neither
// token, and asks instead for the lazy_members flag and for naming a member you always want -
// but those clients are out there, no room can hold a state key beginning with "$", and a
// server that reads the two costs itself nothing.
func TestSlidingSyncReadsTheProxyTokens(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	carol := server.Register("carol", "cooker")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Down"})
	for _, other := range []string{bob.UserID, carol.UserID} {
		server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
			map[string]any{"user_id": other}, 200)
		joined := bob
		if other == carol.UserID {
			joined = carol
		}
		server.MustDo("POST", "/_matrix/client/v3/join/"+url.PathEscape(roomID), joined.Token, nil, 200)
	}
	// The window's only sender is bob, so a lazy answer would name bob and not alice: that is
	// what makes "$ME" observable rather than accidental.
	server.Send(bob, roomID, "one", "bob speaks")

	members := func(answer map[string]any) map[string]bool {
		out := map[string]bool{}
		room := slidingRooms(t, answer)[roomID]
		if room == nil {
			t.Fatalf("the room is missing from the answer")
		}
		state, _ := room["required_state"].([]any)
		for _, raw := range state {
			entry, _ := raw.(map[string]any)
			if entry["type"] == "m.room.member" {
				key, _ := entry["state_key"].(string)
				out[key] = true
			}
		}
		return out
	}

	// "$ME" alongside an everything else: the requester's own membership arrives, and the rest
	// of the room's members do not, because naming one member is not asking for all of them.
	named := members(server.slidingSync(t, alice, map[string]any{
		"conn_id": "me",
		"lists": map[string]slidingList{
			"all": {
				"range":          []int{0, 9},
				"timeline_limit": 1,
				"required_state": map[string]any{
					"include": []map[string]any{
						{},
						{"type": "m.room.member", "state_key": "$ME"},
					},
					"lazy_members": true,
				},
			},
		},
	}))
	if !named[alice.UserID] {
		t.Errorf("$ME did not bring alice's own membership: %v", named)
	}
	if !named[bob.UserID] {
		t.Errorf("lazy loading did not bring the membership of the window's sender: %v", named)
	}
	if named[carol.UserID] {
		t.Errorf("$ME was read as asking for every member: %v", named)
	}

	// The same config without the token, to show the token is what made the difference: alice
	// did not speak in the window, so nothing else would have brought her membership.
	without := members(server.slidingSync(t, alice, map[string]any{
		"conn_id": "without-me",
		"lists": map[string]slidingList{
			"all": {
				"range":          []int{0, 9},
				"timeline_limit": 1,
				"required_state": map[string]any{
					"include":      []map[string]any{{}},
					"lazy_members": true,
				},
			},
		},
	}))
	if without[alice.UserID] {
		t.Errorf("alice's membership arrived without being asked for, so $ME proves nothing: %v", without)
	}

	// "$LAZY" is a request for the memberships the lazy way, not for a state key that no room
	// can hold: nobody is answered with a member event keyed "$LAZY", and the membership list
	// is not dumped.
	lazy := members(server.slidingSync(t, alice, map[string]any{
		"conn_id": "lazy",
		"lists": map[string]slidingList{
			"all": {
				"range":          []int{0, 9},
				"timeline_limit": 1,
				"required_state": map[string]any{
					"include": []map[string]any{{"type": "m.room.member", "state_key": "$LAZY"}},
				},
			},
		},
	}))
	if lazy["$LAZY"] {
		t.Errorf("the room was answered with a member event keyed $LAZY: %v", lazy)
	}
	if !lazy[bob.UserID] {
		t.Errorf("$LAZY did not bring the membership of the window's sender: %v", lazy)
	}
	if lazy[alice.UserID] || lazy[carol.UserID] {
		t.Errorf("$LAZY was read as asking for every member: %v", lazy)
	}

	// And a room that a client asked about plainly is still answered plainly: the tokens turn
	// nothing on for a config that did not write them.
	plain := members(server.slidingSync(t, alice, map[string]any{
		"conn_id": "plain",
		"lists": map[string]slidingList{
			"all": {
				"range":          []int{0, 9},
				"timeline_limit": 1,
				"required_state": map[string]any{
					"include": []map[string]any{{"type": "m.room.member"}},
				},
			},
		},
	}))
	if !plain[alice.UserID] || !plain[bob.UserID] || !plain[carol.UserID] || plain["$LAZY"] {
		t.Errorf("a config asking for memberships was answered with %v, want all three", plain)
	}
}

// slidingSyncURL performs a sliding sync whose parameters are written in the query string, the
// way a client that learned this endpoint before MSC4186 closed sends them.
func (s *Server) slidingSyncURL(t *testing.T, session *Session, query url.Values,
	body map[string]any) Response {
	t.Helper()
	path := "/_matrix/client/v4/sync"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}
	return s.DoRaw("POST", path, session.Token, "application/json", payload)
}

// slidingSyncListConfig is the smallest list config a client sends: a window and a timeline.
func slidingSyncListConfig() map[string]any {
	return map[string]any{
		"all": slidingList{"range": []int{0, 9}, "timeline_limit": 2},
	}
}

// TestSlidingSyncReadsItsParametersFromTheURL asks the endpoint to place pos, timeout and
// set_presence where the URL put them before MSC4186 moved them into the body.
//
// The pos is the one a client feels: a server that reads only the body never learns where the
// connection was, so every poll restarts it, every room arrives marked initial again, and a
// room list a client only ever sees re-sent from scratch stops looking like it is keeping up.
func TestSlidingSyncReadsItsParametersFromTheURL(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Down"})

	first := server.slidingSyncURL(t, alice, nil, map[string]any{
		"conn_id": "url",
		"lists":   slidingSyncListConfig(),
	})
	if first.Status != 200 {
		t.Fatalf("a first request with an empty query was refused: %d %v", first.Status, first.Body)
	}
	pos, _ := first.Body["pos"].(string)
	if pos == "" {
		t.Fatalf("the answer carries no position to continue from: %v", first.Body)
	}
	if rooms := slidingRooms(t, first.Body); rooms[roomID]["initial"] != true {
		t.Fatalf("a first sight of a room is not marked initial: %v", rooms[roomID])
	}

	server.Send(alice, roomID, "second", "the room moved once")
	waited := time.Now()
	second := server.slidingSyncURL(t, alice, url.Values{
		"pos":          []string{pos},
		"timeout":      []string{"0"},
		"set_presence": []string{"offline"},
	}, map[string]any{"conn_id": "url", "lists": slidingSyncListConfig()})
	if second.Status != 200 {
		t.Fatalf("a request naming its parameters in the query was refused: %d %v",
			second.Status, second.Body)
	}
	// Nothing had to be waited for, so the timeout of 0 that arrived in the query is what
	// answered here: a connection that was told to wait would have held the request.
	if elapsed := time.Since(waited); elapsed > 2*time.Second {
		t.Errorf("a poll with timeout=0 in the query held the request for %v", elapsed)
	}
	// The room that moved is sent, and sent as a delta: the connection went on where the
	// position in the query left it instead of starting over.
	moved := slidingRooms(t, second.Body)[roomID]
	if moved == nil {
		t.Fatalf("the room that moved is missing from the answer: %v", second.Body)
	}
	if moved["initial"] == true {
		t.Errorf("a room already sent on this connection arrives marked initial again, so the "+
			"connection started over instead of continuing from the position in the query: %v", moved)
	}
	timeline, _ := moved["timeline"].([]any)
	if len(timeline) != 1 {
		t.Errorf("the room carries %d timeline events, want only the one that is new", len(timeline))
	}
	if next, _ := second.Body["pos"].(string); next == pos {
		t.Errorf("the answer repeats the position %q instead of moving the connection on", pos)
	}
}

// TestSlidingSyncPrefersTheBodyOverTheQuery checks that reading the query adds a placement
// rather than a second authority: MSC4186 asks for these parameters in the body, so a request
// that names one in both places is answered as the body says.
func TestSlidingSyncPrefersTheBodyOverTheQuery(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Down"})

	first := server.slidingSync(t, alice, map[string]any{
		"conn_id": "both",
		"lists":   slidingSyncListConfig(),
	})
	pos, _ := first["pos"].(string)
	server.Send(alice, roomID, "second", "the room moved once")

	// The body names the position the connection stands on; the query names one that was
	// never issued. Were the query the authority, this request would be refused.
	continued := server.slidingSyncURL(t, alice, url.Values{"pos": []string{"9_9_never_issued"}},
		map[string]any{"conn_id": "both", "pos": pos, "lists": slidingSyncListConfig()})
	if continued.Status != 200 {
		t.Fatalf("a request whose body names a known position was refused (%s): %v",
			continued.ErrCode(), continued.Body)
	}
	if rooms := slidingRooms(t, continued.Body); rooms[roomID]["initial"] == true {
		t.Errorf("the body's position was overridden by the query's: %v", rooms[roomID])
	}
}

// TestSlidingSyncRejectsBadQueryParameters asks what happens when a parameter in the query is
// written the wrong way: it is a parameter like any other, so the answer is the error the
// endpoint already gives for a bad one.
func TestSlidingSyncRejectsBadQueryParameters(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Down"})

	for _, tc := range []struct {
		name  string
		query url.Values
	}{
		{"a timeout that is not a number", url.Values{"timeout": []string{"soon"}}},
		{"a presence nobody takes", url.Values{"set_presence": []string{"sideways"}}},
	} {
		answer := server.slidingSyncURL(t, alice, tc.query, map[string]any{
			"conn_id": "bad",
			"lists":   slidingSyncListConfig(),
		})
		if answer.Status != 400 || answer.ErrCode() != "M_INVALID_PARAM" {
			t.Errorf("%s: got %d %s, want 400 M_INVALID_PARAM", tc.name, answer.Status, answer.ErrCode())
		}
	}
}

// TestSlidingSyncReadsTheProxyShapes asks the endpoint to read a request that writes two of its
// fields the way the sliding sync proxy wrote them, and ruma's sliding sync after it: the window
// as "ranges", the state to return as a list of pairs or of "type|state_key" strings, and the
// invitation filter under the name it had before MSC4186 renamed it.
//
// These are placements and shapes of the same questions, so the answers are the answers a client
// that writes the MSC forms gets: the state that was asked for, a window that is a window, and an
// invitation filter that filters. A client is never told it has to choose a dialect, and nothing
// about an answer changes for a client that writes the MSC forms.
func TestSlidingSyncReadsTheProxyShapes(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Down"})
	server.Send(alice, roomID, "first", "a message in Down")
	waiting := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Waiting"})
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+waiting+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, 200)

	// The pairs. A list of [type, state_key] pairs names the state to return, which is what
	// the object's include list does.
	pairs := server.slidingSync(t, alice, map[string]any{
		"conn_id": "pairs",
		"lists": map[string]any{
			"all": slidingList{
				"ranges":         [][]int{{0, 9}},
				"timeline_limit": 1,
				"required_state": [][]string{{"m.room.name", ""}, {"m.room.create", ""}},
			},
		},
	})
	room, found := slidingRooms(t, pairs)[roomID]
	if !found {
		t.Fatalf("the room is missing from an answer to a request written in pairs: %v", pairs)
	}
	if got := slidingStateTypes(t, room); len(got) != 2 || !got["m.room.name"] || !got["m.room.create"] {
		t.Errorf("the pairs named two state events and the answer carries %v", sortedKeys(got))
	}

	// The same question written as strings, the form a client that learned this endpoint from
	// the proxy's documentation writes.
	strings3 := server.slidingSync(t, alice, map[string]any{
		"conn_id": "strings",
		"lists": map[string]any{
			"all": slidingList{
				"ranges":         [][]int{{0, 9}},
				"timeline_limit": 1,
				"required_state": []string{"m.room.name|"},
			},
		},
	})
	room, found = slidingRooms(t, strings3)[roomID]
	if !found {
		t.Fatalf("the room is missing from an answer to a request written in strings: %v", strings3)
	}
	if got := slidingStateTypes(t, room); len(got) != 1 || !got["m.room.name"] {
		t.Errorf(`the "m.room.name|" string named one state event and the answer carries %v`,
			sortedKeys(got))
	}

	// The window. Two rooms asked for is two rooms, whatever the field was called, and the
	// count still reports the whole filtered list.
	narrow := server.slidingSync(t, alice, map[string]any{
		"conn_id": "window",
		"lists": map[string]any{
			"all": slidingList{"ranges": [][]int{{0, 1}}, "timeline_limit": 1},
		},
	})
	if count := slidingListCount(t, narrow, "all"); count != 2 {
		t.Errorf("the list counts %d rooms, want the 2 the account has", count)
	}
	if got := len(slidingRooms(t, narrow)); got != 2 {
		t.Errorf("a window of [0,1] written as ranges sent %d rooms, want 2", got)
	}

	// The filter under its older name answers the question it always asked, and it is still
	// asked of the person looking.
	invited := server.slidingSync(t, bob, map[string]any{
		"conn_id": "invites",
		"lists": map[string]any{
			"invites": slidingList{"timeline_limit": 0, "filters": map[string]any{"is_invite": true}},
		},
	})
	rooms := slidingRooms(t, invited)
	if len(rooms) != 1 || rooms[waiting] == nil {
		t.Fatalf("the invited list written in the older name holds %v, want only %s", rooms, waiting)
	}
	if rooms[waiting]["membership"] != "invite" {
		t.Errorf("the invited list reports membership %v", rooms[waiting]["membership"])
	}
}

// slidingStateTypes collects the event types a room answer carries in its required state.
func slidingStateTypes(t *testing.T, room map[string]any) map[string]bool {
	t.Helper()
	state, _ := room["required_state"].([]any)
	types := map[string]bool{}
	for _, raw := range state {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("a required state entry is not an object: %v", raw)
			continue
		}
		typ, _ := entry["type"].(string)
		types[typ] = true
	}
	return types
}

// sortedKeys lists the keys of a set in order, so a failure reads as a sentence.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestSlidingSyncStrippedStateCarriesAStateKey checks a field that is easy to lose to a
// serialiser: a stripped state event's `state_key` is required, and for the three types that
// name a room — create, name, join_rules — its value is the empty string. Omit the field when
// it is empty and a strict parser does not read "no state key", it reads "not an event" and
// drops it. The room then arrives in the room list with no name, on every path.
func TestSlidingSyncStrippedStateCarriesAStateKey(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Blueprints"})
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, 200)

	// want shrinks as each type is seen, so the test cannot pass on a stripped state that
	// carries only the events whose state key happens to be non-empty.
	want := map[string]bool{"m.room.create": true, "m.room.name": true, "m.room.join_rules": true}

	check := func(where string, events []any) {
		t.Helper()
		for _, raw := range events {
			event, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s: a stripped state event is not an object: %v", where, raw)
			}
			key, _ := event["type"].(string)
			value, present := event["state_key"]
			if !present {
				t.Errorf("%s: the %s event is written without a state_key field: %v", where, key, event)
				continue
			}
			// Only the types this test came for are checked by value: an
			// m.room.member event's state key is the member, and it is right to say so.
			if _, tracked := want[key]; tracked {
				if text, _ := value.(string); text != "" {
					t.Errorf("%s: the %s event carries state key %q, want the empty string", where, key, text)
				}
				delete(want, key)
			}
		}
		for key := range want {
			t.Errorf("%s: no %s event to check, so this says nothing about it", where, key)
			delete(want, key)
		}
	}

	// The classic /sync invitation.
	sync := server.MustDo("GET", "/_matrix/client/v3/sync", bob.Token, nil, 200)
	rooms, _ := sync.Body["rooms"].(map[string]any)
	invited, _ := rooms["invite"].(map[string]any)
	ourInvite, _ := invited[roomID].(map[string]any)
	state, _ := ourInvite["invite_state"].(map[string]any)
	events, _ := state["events"].([]any)
	check("/sync invite_state", events)

	// Sliding sync, on both of its paths.
	for _, path := range []string{
		"/_matrix/client/v4/sync",
		"/_matrix/client/unstable/org.matrix.simplified_msc3575/sync",
	} {
		answer := server.MustDo("POST", path, bob.Token, map[string]any{
			"conn_id": "invite-" + path,
			"lists": map[string]slidingList{
				"invites": {"timeline_limit": 0, "required_state": []any{[]any{"m.room.name", ""}}},
			},
		}, 200)
		room, ok := slidingRooms(t, answer.Body)[roomID]
		if !ok {
			t.Fatalf("%s: bob's invitation is not in the answer: %v", path, answer.Body)
		}
		stripped, ok := room["stripped_state"].([]any)
		if !ok {
			t.Fatalf("%s: the invitation carries no stripped state: %v", path, room)
		}
		check(path+" stripped_state", stripped)
	}
}

// rawExtensionField reads one field of one extension section as it stands on the wire. Decoded
// into a Go map an empty list, a null and an absent field all read back as nil, and telling
// those three apart is exactly what these checks are about.
func rawExtensionField(t *testing.T, raw []byte, extension, field string) (json.RawMessage, bool) {
	t.Helper()
	var probe struct {
		Extensions map[string]map[string]json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("the answer is not JSON: %v (%s)", err, raw)
	}
	section, ok := probe.Extensions[extension]
	if !ok {
		return nil, false
	}
	value, ok := section[field]
	return value, ok
}

// slidingEncryptionRequest is the request the fault was reported with: an encryption connection
// that wants its keys and its to-device messages, and nothing else.
func slidingEncryptionRequest(connID string) map[string]any {
	return map[string]any{
		"conn_id": connID,
		"extensions": map[string]any{
			"to_device": map[string]any{"enabled": true},
			"e2ee":      map[string]any{"enabled": true},
		},
	}
}

// TestSlidingSyncEncryptionFieldsAreNeverNull reads MSC4543 literally. Its table marks
// device_unused_fallback_key_types Required, type [string], and writes that "an empty list means
// the device has no unused fallback key" — so the answer is [] when there is none, never null
// and never absent. The Go pattern behind it is the one that erased state_key: a nil slice
// marshals as null.
//
// A strict client does not shrug this off. matrix-rust-sdk parses the whole e2ee section as one
// structure, so the null takes the key counts and the to-device messages down with it, and the
// device stops receiving keys.
func TestSlidingSyncEncryptionFieldsAreNeverNull(t *testing.T) {
	server := NewServer(t)
	bob := server.Register("bob", "builders")

	// A device that never uploaded a fallback key is the reported case: the list has nothing
	// to carry, which is exactly when the encoder is tempted to write null.
	for _, path := range []string{
		"/_matrix/client/v4/sync",
		"/_matrix/client/unstable/org.matrix.simplified_msc3575/sync",
	} {
		answer := server.MustDo("POST", path, bob.Token, slidingEncryptionRequest("encryption"), 200)
		fallback, ok := rawExtensionField(t, answer.Raw, "e2ee", "device_unused_fallback_key_types")
		if !ok {
			t.Errorf("%s: the e2ee section carries no device_unused_fallback_key_types: %s", path, answer.Raw)
		} else if got := strings.TrimSpace(string(fallback)); got != "[]" {
			t.Errorf("%s: device_unused_fallback_key_types is %s, want an empty list", path, got)
		}
		counts, ok := rawExtensionField(t, answer.Raw, "e2ee", "device_one_time_keys_count")
		if !ok {
			t.Errorf("%s: the e2ee section carries no device_one_time_keys_count: %s", path, answer.Raw)
		} else if got := strings.TrimSpace(string(counts)); !strings.HasPrefix(got, "{") {
			t.Errorf("%s: device_one_time_keys_count is %s, want an object", path, got)
		}
	}

	// The classic /sync carries the same field, and MSC2732 asks for it there always — Synapse
	// reverted a release that stripped the empty one over exactly this argument. Same encoder,
	// same trap: omitempty erases an empty list.
	classic := server.Do("GET", "/_matrix/client/v3/sync?timeout=0", bob.Token, nil)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(classic.Raw, &top); err != nil {
		t.Fatalf("the classic sync is not JSON: %v (%s)", err, classic.Raw)
	}
	value, ok := top["device_unused_fallback_key_types"]
	if !ok {
		t.Errorf("the classic sync leaves device_unused_fallback_key_types out altogether: %s", classic.Raw)
	} else if got := string(value); !strings.HasPrefix(got, "[") {
		t.Errorf("the classic sync answers device_unused_fallback_key_types as %s, want an empty list", got)
	}

	// The list is not a hard-wired empty array either: name the algorithm of a key that exists.
	server.MustDo("POST", "/_matrix/client/v3/keys/upload", bob.Token, map[string]any{
		"fallback_keys": map[string]any{"fallback_curve25519:AAAAAA": map[string]any{"key": "AAA"}},
	}, 200)
	answer := server.MustDo("POST", "/_matrix/client/v4/sync", bob.Token,
		slidingEncryptionRequest("encryption-two"), 200)
	fallback, ok := rawExtensionField(t, answer.Raw, "e2ee", "device_unused_fallback_key_types")
	if !ok {
		t.Fatalf("the answer after the upload carries no device_unused_fallback_key_types: %s", answer.Raw)
	}
	if !strings.Contains(string(fallback), "fallback_curve25519") {
		t.Errorf("a device with a fallback key should name it, the answer says %s", fallback)
	}
}

// TestSlidingSyncToDeviceFollowsItsEndpoint reads MSC4538. Its table names the messages of the
// to_device section `messages`, Required, and its changelog counts returning them under `events`
// as the Synapse v1.151 shape — which the unstable prefix section leaves in place on the
// unstable endpoint only. A ToDeviceMessage carries type, sender and content: no event id, no
// origin_server_ts.
func TestSlidingSyncToDeviceFollowsItsEndpoint(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	server.MustDo("PUT", accountDataPath(bob.UserID, "test.queue.marker"), bob.Token,
		map[string]any{"who": "bob"}, 200)
	server.MustDo("PUT", "/_matrix/client/v3/sendToDevice/test.secret/queued", alice.Token,
		map[string]any{"messages": map[string]any{bob.UserID: map[string]any{"*": map[string]any{"hello": "bob"}}}}, 200)

	// The stable endpoint follows the MSC: a required `messages` list naming no event ids.
	stable := server.MustDo("POST", "/_matrix/client/v4/sync", bob.Token,
		slidingEncryptionRequest("queue"), 200)
	messages, ok := rawExtensionField(t, stable.Raw, "to_device", "messages")
	if !ok {
		t.Fatalf("%s: the to_device section carries no messages: %s", "/_matrix/client/v4/sync", stable.Raw)
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(messages, &list); err != nil {
		t.Fatalf("%s: messages is %s, want a list: %v", "/_matrix/client/v4/sync", messages, err)
	}
	if len(list) != 1 {
		t.Fatalf("%s: messages carries %d entries, want the one queued for this device: %s",
			"/_matrix/client/v4/sync", len(list), stable.Raw)
	}
	for _, field := range []string{"event_id", "origin_server_ts"} {
		if _, ok := list[0][field]; ok {
			t.Errorf("%s: a ToDeviceMessage names %s, MSC4538 lists type, sender and content: %s",
				"/_matrix/client/v4/sync", field, messages)
		}
	}
	if _, ok := list[0]["type"]; !ok {
		t.Errorf("the message carries no type: %s", messages)
	}
	if _, ok := list[0]["sender"]; !ok {
		t.Errorf("the message carries no sender: %s", messages)
	}
	if next, ok := rawExtensionField(t, stable.Raw, "to_device", "next_batch"); !ok || string(next) == "null" {
		t.Errorf("%s: no next_batch to acknowledge with: %s", "/_matrix/client/v4/sync", stable.Raw)
	}

	// MSC4538 changelog point 1: a limit that is not a positive integer is refused with
	// M_BAD_JSON. Synapse accepted a zero and answered an empty section whose next_batch
	// acknowledged everything queued, so the messages disappeared unread.
	bad := server.Do("POST", "/_matrix/client/v4/sync", bob.Token, map[string]any{
		"conn_id": "queue-limit",
		"extensions": map[string]any{
			"to_device": map[string]any{"enabled": true, "limit": 0},
		},
	})
	if bad.Status != 400 {
		t.Errorf("a limit of zero was answered %d (%s), want 400", bad.Status, bad.Raw)
	}
	if code, _ := bad.Body["errcode"].(string); code != "M_BAD_JSON" {
		t.Errorf("a limit of zero answered errcode %v, want M_BAD_JSON", bad.Body["errcode"])
	}

	// The unstable endpoint keeps the shape its own clients were written against.
	legacy := server.MustDo("PUT", "/_matrix/client/v3/sendToDevice/test.secret/queued2", alice.Token,
		map[string]any{"messages": map[string]any{bob.UserID: map[string]any{"*": map[string]any{"again": "bob"}}}}, 200)
	if legacy.Status != 200 {
		t.Fatalf("queueing the second message failed: %s", legacy.Raw)
	}
	unstable := server.MustDo("POST", "/_matrix/client/unstable/org.matrix.simplified_msc3575/sync",
		bob.Token, slidingEncryptionRequest("queue-legacy"), 200)
	events, ok := rawExtensionField(t, unstable.Raw, "to_device", "events")
	if !ok {
		t.Fatalf("%s: the to_device section carries no events: %s",
			"/_matrix/client/unstable/org.matrix.simplified_msc3575/sync", unstable.Raw)
	}
	var legacyList []map[string]json.RawMessage
	if err := json.Unmarshal(events, &legacyList); err != nil || len(legacyList) == 0 {
		t.Errorf("%s: events is %s, want the message still queued: %v",
			"/_matrix/client/unstable/org.matrix.simplified_msc3575/sync", events, err)
	}
}

// TestSlidingSyncEphemeralSectionsFollowTheirEndpoint is the same decision one more time, asked
// of MSC4551 and MSC4508: a room inside the receipts section is the bare content of an
// m.receipt event, and a room inside the typing section is the bare {"user_ids": […]}. Both MSCs
// say the wrapper adds nothing, because the section can only carry that one thing, and both hand
// the wrapped event form to the unstable URL, whose clients read it.
//
// That wrapping is not decoration. matrix-rust-sdk expects the bare value on the stable path, so
// an event there reads as an empty update and read markers and typing indicators simply do not
// arrive.
func TestSlidingSyncEphemeralSectionsFollowTheirEndpoint(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, 200)
	server.MustDo("POST", "/_matrix/client/v3/join/"+url.PathEscape(roomID), bob.Token, nil, 200)
	server.MustDo("POST", "/_matrix/client/v3/join/"+url.PathEscape(roomID), alice.Token, nil, 200)
	eventID := server.Send(alice, roomID, "one", "a message to read")
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+url.PathEscape(eventID),
		alice.Token, map[string]any{}, 200)
	server.MustDo("PUT", "/_matrix/client/v3/rooms/"+roomID+"/typing/"+alice.UserID, alice.Token,
		map[string]any{"typing": true, "timeout": 30000}, 200)

	request := func(connID string) map[string]any {
		return map[string]any{
			"conn_id": connID,
			"lists":   map[string]slidingList{"all": {"range": []int{0, 9}, "timeline_limit": 5}},
			"extensions": map[string]any{
				"receipts": map[string]any{"enabled": true},
				"typing":   map[string]any{"enabled": true},
			},
		}
	}
	// sectionRoom reads one room out of one extension section of a raw answer.
	sectionRoom := func(t *testing.T, raw []byte, extension string) map[string]json.RawMessage {
		t.Helper()
		var probe struct {
			Extensions map[string]struct {
				Rooms map[string]map[string]json.RawMessage `json:"rooms"`
			} `json:"extensions"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("the answer is not JSON: %v (%s)", err, raw)
		}
		room, ok := probe.Extensions[extension].Rooms[roomID]
		if !ok {
			t.Fatalf("the %s section carries nothing for %s: %s", extension, roomID, raw)
		}
		return room
	}

	stable := server.MustDo("POST", "/_matrix/client/v4/sync", bob.Token, request("bare"), 200)
	typing := sectionRoom(t, stable.Raw, "typing")
	if _, wrapped := typing["type"]; wrapped {
		t.Errorf("the stable typing section wraps the room in an m.typing event: %v", typing)
	}
	if _, ok := typing["user_ids"]; !ok {
		t.Errorf("the stable typing section names no user_ids: %v", typing)
	}
	receipts := sectionRoom(t, stable.Raw, "receipts")
	if _, wrapped := receipts["type"]; wrapped {
		t.Errorf("the stable receipts section wraps the room in an m.receipt event: %v", receipts)
	}
	if _, ok := receipts["content"]; ok {
		t.Errorf("the stable receipts section answers a content wrapper: %v", receipts)
	}
	// The keys of the bare content are event ids, exactly as in an m.receipt event.
	if named := eventKeys(receipts); len(named) == 0 {
		t.Errorf("the stable receipts section names no event id for %s: %v", eventID, receipts)
	}

	// The unstable URL keeps what its clients parse: a full ephemeral event per room.
	unstable := server.MustDo("POST", "/_matrix/client/unstable/org.matrix.simplified_msc3575/sync",
		bob.Token, request("wrapped"), 200)
	for name, wantType := range map[string]string{"typing": "m.typing", "receipts": "m.receipt"} {
		room := sectionRoom(t, unstable.Raw, name)
		if got, want := string(room["type"]), `"`+wantType+`"`; got != want {
			t.Errorf("the unstable %s section answers type %s, want the %s event it always did",
				name, got, wantType)
		}
		if _, ok := room["content"]; !ok {
			t.Errorf("the unstable %s section carries no content wrapper: %v", name, room)
		}
	}
}

// eventKeys returns the keys of a bare m.receipt content, which are event ids.
func eventKeys(content map[string]json.RawMessage) []string {
	var out []string
	for key := range content {
		if strings.HasPrefix(key, "$") {
			out = append(out, key)
		}
	}
	return out
}

// TestSlidingSyncEphemeralSectionsWakeTheLongPoll is the fault that made typing, read markers and
// room tags unreachable: the ephemeral sections covered only the rooms this response happened to
// carry, and a room with nothing else to say has no entry in a response. Nothing to report, so
// nothing ever could be - and the long poll, told there was nothing, waited out its timeout.
//
// MSC4508 says the scope differently in so many words: "a room being in scope does _not_ require
// the room to have an entry in the `rooms` section of the _response_". The scope is the
// connection's window and its subscriptions, and a change in it is an update to wake for.
func TestSlidingSyncEphemeralSectionsWakeTheLongPoll(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, 200)
	server.MustDo("POST", "/_matrix/client/v3/join/"+url.PathEscape(roomID), bob.Token, nil, 200)
	eventID := server.Send(alice, roomID, "one", "a message to read")

	// The shape the reporter's client sends: a list, the three ephemeral extensions, and the
	// star that every pre-MSC implementation accepted for "every room".
	request := func(connID string, pos string, timeout int) map[string]any {
		body := map[string]any{
			"conn_id": connID,
			"lists": map[string]slidingList{
				"all_rooms": {"range": []int{0, 9}, "timeline_limit": 1},
			},
			"extensions": map[string]any{
				"typing":       map[string]any{"enabled": true, "lists": []string{"all_rooms"}, "rooms": []string{"*"}},
				"receipts":     map[string]any{"enabled": true, "lists": []string{"all_rooms"}, "rooms": []string{"*"}},
				"account_data": map[string]any{"enabled": true, "lists": []string{"all_rooms"}, "rooms": []string{"*"}},
			},
		}
		if pos != "" {
			body["pos"] = pos
		}
		if timeout > 0 {
			body["timeout"] = timeout
		}
		return body
	}

	initial := server.MustDo("POST", "/_matrix/client/v4/sync", bob.Token, request("room-list", "", 0), 200)
	pos, _ := initial.Body["pos"].(string)
	if pos == "" {
		t.Fatalf("the connection began without a position: %s", initial.Raw)
	}

	// Two changes in a room the next response has no reason to mention.
	server.MustDo("PUT", "/_matrix/client/v3/rooms/"+roomID+"/typing/"+alice.UserID, alice.Token,
		map[string]any{"typing": true, "timeout": 30000}, 200)
	server.MustDo("POST", "/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+url.PathEscape(eventID),
		alice.Token, map[string]any{}, 200)

	began := time.Now()
	woken := server.MustDo("POST", "/_matrix/client/v4/sync", bob.Token, request("room-list", pos, 5000), 200)
	elapsed := time.Since(began)
	if elapsed > 2*time.Second {
		t.Errorf("the long poll waited %s for a typing notification and a receipt, MSC4508 wants it back at once",
			elapsed.Round(time.Millisecond))
	}
	extensions, _ := woken.Body["extensions"].(map[string]any)
	typing, _ := extensions["typing"].(map[string]any)
	rooms, _ := typing["rooms"].(map[string]any)
	entry, _ := rooms[roomID].(map[string]any)
	if entry == nil {
		t.Fatalf("the typing section carries nothing for %s: %s", roomID, woken.Raw)
	}
	if users, _ := entry["user_ids"].([]any); len(users) != 1 || users[0] != alice.UserID {
		t.Errorf("the typing section says %v, want %s typing", entry["user_ids"], alice.UserID)
	}
	receipts, _ := extensions["receipts"].(map[string]any)
	byRoom, _ := receipts["rooms"].(map[string]any)
	room, _ := byRoom[roomID].(map[string]any)
	if room == nil {
		t.Fatalf("the receipts section carries nothing for %s: %s", roomID, woken.Raw)
	}
	if _, ok := room[eventID]; !ok {
		t.Errorf("the receipts section misses the receipt on %s: %v", eventID, room)
	}

	// Room account data is per user, so the one who set the tag has to be the one asking.
	own := map[string]any{
		"conn_id":    "alice-list",
		"lists":      map[string]slidingList{"all_rooms": {"range": []int{0, 9}, "timeline_limit": 1}},
		"extensions": map[string]any{"account_data": map[string]any{"enabled": true}},
	}
	firstAlice := server.MustDo("POST", "/_matrix/client/v4/sync", alice.Token, own, 200)
	alicePos, _ := firstAlice.Body["pos"].(string)
	server.MustDo("PUT", "/_matrix/client/v3/user/"+url.PathEscape(alice.UserID)+"/rooms/"+roomID+"/tags/m.favourite",
		alice.Token, map[string]any{"order": 0.5}, 200)
	own["pos"] = alicePos
	own["timeout"] = 5000
	began = time.Now()
	aliceWoken := server.MustDo("POST", "/_matrix/client/v4/sync", alice.Token, own, 200)
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Errorf("a room tag waited %s for a client that was waiting for it", elapsed.Round(time.Millisecond))
	}
	aliceExtensions, _ := aliceWoken.Body["extensions"].(map[string]any)
	accountData, _ := aliceExtensions["account_data"].(map[string]any)
	if accountData == nil {
		t.Fatalf("the account_data section disappeared: %s", aliceWoken.Raw)
	}
	if tagged := accountData["rooms"]; !accountsHold(tagged.(map[string]any)[roomID], "m.tag") {
		t.Errorf("the room tag did not arrive as room account data: %v", tagged)
	}
}

// TestSlidingSyncLimitedMeansMissedEvents keeps "limited" a statement about what the client is
// missing. It used to be computed from the room's size as well, so any room with more history than
// the window said "limited" forever - and a client that believes the field throws its timeline away
// and goes paging every time it is told there is news. MSC4186 asks about the events "since the
// previous sync" that did not fit, plus the first sight of a room, where history behind the window
// is exactly what the client is being told to page for.
func TestSlidingSyncLimitedMeansMissedEvents(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "deep room"})
	for i := 0; i < 4; i++ {
		server.Send(alice, roomID, fmt.Sprintf("h%d", i), "history")
	}
	lists := map[string]any{"all": map[string]slidingList{"all": {"range": []int{0, 9}, "timeline_limit": 3}}}
	request := func(pos string, extra map[string]any) map[string]any {
		body := map[string]any{"conn_id": "deep", "lists": lists["all"]}
		if pos != "" {
			body["pos"] = pos
		}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}

	first := server.MustDo("POST", "/_matrix/client/v4/sync", alice.Token, request("", nil), 200)
	room := slidingRoomOf(t, first.Body, roomID)
	if room["limited"] != true {
		t.Errorf("a room deeper than its window should be offered as limited, got %v", room["limited"])
	}
	if events, _ := room["timeline"].([]any); len(events) != 3 {
		t.Fatalf("the window is %d events, want the newest 3", len(events))
	}
	pos, _ := first.Body["pos"].(string)

	// One message, delivered. Nothing was left out, so nothing is limited.
	server.Send(alice, roomID, "new", "the one new message")
	update := server.MustDo("POST", "/_matrix/client/v4/sync", alice.Token,
		request(pos, map[string]any{"timeout": 0}), 200)
	room = slidingRoomOf(t, update.Body, roomID)
	if _, ok := room["limited"]; ok {
		t.Errorf("one new message, sent in full, still says limited: %v", room["limited"])
	}
	if events, _ := room["timeline"].([]any); len(events) != 1 {
		t.Errorf("the update carries %d events, want the one new message", len(events))
	}
	pos, _ = update.Body["pos"].(string)

	// More news than the window carries is the case the field exists for.
	for i := 0; i < 5; i++ {
		server.Send(alice, roomID, fmt.Sprintf("f%d", i), "flood")
	}
	flood := server.MustDo("POST", "/_matrix/client/v4/sync", alice.Token,
		request(pos, map[string]any{"timeout": 0}), 200)
	room = slidingRoomOf(t, flood.Body, roomID)
	if room["limited"] != true {
		t.Errorf("five messages into a window of three must be called limited, got %v", room["limited"])
	}
	if events, _ := room["timeline"].([]any); len(events) != 3 {
		t.Errorf("the flooded window carries %d events, want three", len(events))
	}
}

// TestSlidingSyncSubscriptionSendsTheStateItJustStartedWants is the other half of a first look at a
// room: a subscription that asks for state the connection has never been shown must actually get it.
// The state delta skipped everything older than the connection's position, which is right for state
// the client holds and wrong for state it was only this round asked for - the room's name, topic and
// create event are typically older than the connection itself. MSC4186 sends the widened
// `required_state` "if the client has previously seen that state" it may leave it out, and this
// client has seen none of it.
func TestSlidingSyncSubscriptionSendsTheStateItJustStartedWants(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "named room"})
	server.MustDo("PUT", "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic", alice.Token,
		map[string]any{"topic": "what the room is about"}, 200)

	narrow := map[string]any{
		"conn_id": "widening",
		"lists": map[string]slidingList{
			"all": {"range": []int{0, 9}, "timeline_limit": 1,
				"required_state": map[string]any{"include": []any{
					map[string]string{"type": "m.room.name", "state_key": ""}}}},
		},
	}
	first := server.MustDo("POST", "/_matrix/client/v4/sync", alice.Token, narrow, 200)
	room := slidingRoomOf(t, first.Body, roomID)
	if got := sortedKeys(slidingStateTypes(t, room)); len(got) != 1 || got[0] != "m.room.name" {
		t.Fatalf("the list asked for the name alone, got %v", got)
	}
	pos, _ := first.Body["pos"].(string)

	// The same room, now also subscribed to under a wider config. Name is known, topic and create
	// are not, and both predate the connection.
	narrow["pos"] = pos
	narrow["room_subscriptions"] = map[string]any{
		roomID: map[string]any{"timeline_limit": 1, "required_state": map[string]any{
			"include": []any{map[string]string{"type": "m.room.name", "state_key": ""},
				map[string]string{"type": "m.room.topic", "state_key": ""},
				map[string]string{"type": "m.room.create", "state_key": ""}}}},
	}
	widened := server.MustDo("POST", "/_matrix/client/v4/sync", alice.Token, narrow, 200)
	room = slidingRoomOf(t, widened.Body, roomID)
	got := sortedKeys(slidingStateTypes(t, room))
	if strings.Join(got, ",") != "m.room.create,m.room.topic" {
		t.Errorf("the widened config sent %v, want the two types the connection has never seen", got)
	}
	pos, _ = widened.Body["pos"].(string)

	// Nothing moved, so nothing is repeated: the record of what the connection holds has to survive
	// the round that added nothing to it, or the next round sends the same state again.
	narrow["pos"] = pos
	quiet := server.MustDo("POST", "/_matrix/client/v4/sync", alice.Token, narrow, 200)
	rooms, _ := quiet.Body["rooms"].(map[string]any)
	if room, _ := rooms[roomID].(map[string]any); room != nil {
		t.Errorf("a room with nothing new is still reported: %v", room["required_state"])
	}
}

// slidingRoomOf picks one room out of a sliding sync response, failing the test when it is absent.
func slidingRoomOf(t *testing.T, body map[string]any, roomID string) map[string]any {
	t.Helper()
	rooms, _ := body["rooms"].(map[string]any)
	room, _ := rooms[roomID].(map[string]any)
	if room == nil {
		t.Fatalf("the response carries nothing for %s: %v", roomID, body)
	}
	return room
}

// TestSyncAnswersFromAStoredPosition covers the position a sync answers from. The
// stream counter runs ahead of the store — a write takes its position before the
// row that carries it lands — so a server that answers from the counter can hand
// out a position it never saw the change of, and every token after it walks past
// that change for good. A write that fails after taking its position makes the
// difference visible without waiting for a race: the counter moves, nothing is
// stored, and no answer may name the new position.
func TestSyncAnswersFromAStoredPosition(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Positions"})
	server.Send(alice, roomID, "stored-1", "written before the token")

	stored, err := server.db.LatestStreamPos()
	if err != nil {
		t.Fatalf("asking the database for its highest position: %v", err)
	}
	// A room this server does not have: the position is taken, the event never lands.
	if _, err := server.svc.AppendEvent(context.Background(), &service.EventDraft{
		Kind: "room_event", RoomID: "!absent:test.server", Type: matrix.TypeMessage,
		Sender: alice.UserID, Content: matrix.Encode(map[string]any{"msgtype": "m.text", "body": "burned"}),
	}); err == nil {
		t.Fatal("appending an event to a room that does not exist was accepted")
	}
	if position := server.svc.StreamPos(); position <= stored {
		t.Fatalf("the counter is at %d after a write took a position, want above the stored %d", position, stored)
	}

	sync := server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", alice.Token, nil, http.StatusOK)
	token, _ := sync.Body["next_batch"].(string)
	assertPositionIsStored(t, server, alice, "classic /sync", token)

	for _, path := range []string{
		"/_matrix/client/v4/sync",
		"/_matrix/client/unstable/org.matrix.simplified_msc3575/sync",
	} {
		resp := server.MustDo(http.MethodPost, path, alice.Token, map[string]any{
			"conn_id": "positions",
			"lists": map[string]any{
				"all": map[string]any{"range": []int{0, 9}, "timeline_limit": 1},
			},
			"extensions": map[string]any{"account_data": map[string]any{"enabled": true}},
		}, http.StatusOK)
		pos, _ := resp.Body["pos"].(string)
		assertPositionIsStored(t, server, alice, path, pos)
	}
}

// assertPositionIsStored checks that a batch token names a change the database
// holds, rather than a position the counter only reached.
func assertPositionIsStored(t *testing.T, server *Server, session *Session, where, token string) {
	t.Helper()
	if token == "" {
		t.Fatalf("%s answered without a position", where)
	}
	pos, err := server.svc.ParseToken(token)
	if err != nil {
		t.Fatalf("%s answered with the position %q: %v", where, token, err)
	}
	stored, err := server.db.LatestStreamPos()
	if err != nil {
		t.Fatalf("asking the database for its highest position: %v", err)
	}
	if pos > stored {
		t.Errorf("%s answered from position %d, while the highest stored change is %d", where, pos, stored)
	}
}

// TestSlidingSyncAccountDataWakesAWaitingPoll covers the symptom the position
// mistake produces: a device marks a change while another device waits for it, and
// the waiting answer has to carry it.
func TestSlidingSyncAccountDataWakesAWaitingPoll(t *testing.T) {
	server := NewServer(t)
	phone := server.Register("alice", "wonderland")
	laptop := server.Login("alice", "wonderland")
	body := map[string]any{
		"conn_id": "room-list",
		"lists": map[string]any{
			"all": map[string]any{"range": []int{0, 19}, "timeline_limit": 1},
		},
		"extensions": map[string]any{"account_data": map[string]any{"enabled": true}},
	}
	start := server.MustDo(http.MethodPost, "/_matrix/client/v4/sync?timeout=0", laptop.Token, body, http.StatusOK)
	pos, _ := start.Body["pos"].(string)
	if pos == "" {
		t.Fatalf("the connection started without a position: %s", start.Raw)
	}

	answered := make(chan string, 1)
	go func() {
		resp := server.MustDo(http.MethodPost, "/_matrix/client/v4/sync?timeout=8000&pos="+url.QueryEscape(pos),
			laptop.Token, body, http.StatusOK)
		pos, _ := resp.Body["pos"].(string)
		answered <- pos
	}()
	if err := server.svc.SetAccountData(context.Background(), phone.UserID, phone.UserID, "",
		"io.element.recent_emoji", matrix.RawMessage(`{"recent_emoji":[["party",1]]}`)); err != nil {
		t.Fatalf("writing account data: %v", err)
	}
	select {
	case <-answered:
	case <-time.After(9 * time.Second):
		t.Fatal("the waiting poll did not answer the account data change")
	}
}
