package federation

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A room directory says which rooms one server chose to put on its shelf, and an alias says
// what one server's name addresses. Neither is a fact another server can answer for, so both
// are asked of the server that made them - and a client pointed at the other server's shelf
// is pointed there rather than answered from here, because the alternative is a directory
// full of rooms that cannot be reached.

// addAlias gives a room an alias of this server, which is the only way an alias gets made.
func addAlias(t *testing.T, s *server, sess *session, alias, roomID string) {
	t.Helper()
	s.mustDo(http.StatusOK, http.MethodPut,
		"/_matrix/client/v3/directory/room/"+url.PathEscape(alias), sess.Token,
		matrix.Object{"room_id": roomID})
}

// directoryPage reads a room directory as a client does.
func directoryPage(t *testing.T, s *server, sess *session, query string) []matrix.Object {
	t.Helper()
	got := s.mustDo(http.StatusOK, http.MethodGet, "/_matrix/client/v3/publicRooms?"+query, sess.Token, nil)
	body, ok := decoded(t, got.Raw).(matrix.Object)
	if !ok {
		t.Fatalf("the room directory of %s is not an object: %s", s.name, got.Raw)
	}
	list, _ := body["chunk"].([]any)
	rooms := make([]matrix.Object, 0, len(list))
	for _, entry := range list {
		room, _ := entry.(matrix.Object)
		if room == nil {
			t.Fatalf("the room directory of %s holds something that is not a room: %v", s.name, entry)
		}
		rooms = append(rooms, room)
	}
	return rooms
}

// namesOf indexes a directory page by room name.
func namesOf(rooms []matrix.Object) map[string]bool {
	names := map[string]bool{}
	for _, room := range rooms {
		if name, _ := room["name"].(string); name != "" {
			names[name] = true
		}
	}
	return names
}

// TestARemoteDirectoryIsAskedOfTheServerThatPublishesIt asks one server for another's shelf.
// The answer comes from the server that publishes those rooms, with the servers that can
// serve them; and asking without naming a server still shows only this server's own rooms,
// because a directory that quietly merged in somebody else's would be a directory nobody can
// page through.
func TestARemoteDirectoryIsAskedOfTheServerThatPublishesIt(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	one.createRoom(alice, map[string]any{"preset": "public_chat", "name": "Garden"})
	two.createRoom(bob, map[string]any{"preset": "public_chat", "name": "Workshop"})

	remote := namesOf(directoryPage(t, two, bob, "server="+one.name))
	if !remote["Garden"] {
		t.Errorf("asking %s for the rooms %s publishes does not list Garden: %v", two.name, one.name, remote)
	}
	if remote["Workshop"] {
		t.Errorf("the directory of %s answered to %s includes %s's own room: %v", one.name, two.name, two.name, remote)
	}

	local := namesOf(directoryPage(t, two, bob, ""))
	if !local["Workshop"] || local["Garden"] {
		t.Errorf("the directory of %s answers with %v, which should be its own rooms only", two.name, local)
	}

	// A filter travels with the question: the searching is done by the server that holds the
	// rooms, because it is the one that knows their names and topics.
	filtered := namesOf(directoryPage(t, two, bob, "server="+one.name+"&filter=Gard"))
	if !filtered["Garden"] {
		t.Errorf("a filtered search of %s's directory does not find Garden: %v", one.name, filtered)
	}
	missed := namesOf(directoryPage(t, two, bob, "server="+one.name+"&filter=nobody-names-this"))
	if len(missed) != 0 {
		t.Errorf("a search of %s's directory for a term nothing matches returns %v", one.name, missed)
	}

	// A server that is not there is a miss rather than a fault: the client asked a question
	// that cannot be answered right now, and nothing about the request itself was wrong.
	gone := two.do(http.MethodGet, "/_matrix/client/v3/publicRooms?server=nowhere.invalid", bob.Token, nil)
	if gone.Status != http.StatusNotFound {
		t.Errorf("asking the directory of a server that is not there was answered %d, want 404: %s",
			gone.Status, gone.Raw)
	}
}

// TestAPublishedRoomHidesItselfFromTheServerItExcludes checks that a room's server ACL is
// honoured by a directory read as well as by a join. Publishing a room says "anyone may find
// this"; an ACL says "not that server". The two are not in conflict: the ACL is the room's
// own statement, and a directory entry is a way of telling somebody about a room.
func TestAPublishedRoomHidesItselfFromTheServerItExcludes(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	garden := one.createRoom(alice, map[string]any{"preset": "public_chat", "name": "Garden"})
	one.createRoom(alice, map[string]any{"preset": "public_chat", "name": "Orchard"})

	before := namesOf(directoryPage(t, two, bob, "server="+one.name))
	if !before["Garden"] {
		t.Fatalf("asking %s for the directory of %s does not list Garden: %v", two.name, one.name, before)
	}

	setRoomState(t, one, alice, garden, matrix.TypeServerACL, "", matrix.Object{
		"deny":  []any{two.name},
		"allow": []any{"*"},
	})

	after := namesOf(directoryPage(t, two, bob, "server="+one.name))
	if after["Garden"] {
		t.Errorf("%s is listed to %s after the room excluded it: %v", garden, two.name, after)
	}
	if !after["Orchard"] {
		t.Errorf("excluding %s from one room hid %s's other rooms as well: %v", two.name, one.name, after)
	}
}

// TestARemoteAliasIsResolvedByTheServerThatHostsIt is the join path of the first item of this
// step: a client names an address on another server, and this server asks that server what it
// addresses rather than answering "no such room". The alias is not ASCII, because an alias is
// written by people and the interesting question is whether the name survives the trip - a
// room that cannot be addressed in the language its users speak is a room with no name.
func TestARemoteAliasIsResolvedByTheServerThatHostsIt(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")

	roomID := one.createRoom(alice, map[string]any{"preset": "public_chat", "name": "Тайный сад"})
	alias := "#тот-самый:" + one.name
	addAlias(t, one, alice, alias, roomID)

	// The alias is addressable at all: any server can read it.
	resolved := federationGET(t, two, one,
		"/_matrix/federation/v1/query/directory?room_alias="+url.QueryEscape(alias))
	if resolved.Status != http.StatusOK {
		t.Fatalf("%s asking %s about the alias %s: %d %s", two.name, one.name, alias, resolved.Status, resolved.Raw)
	}
	if resolved.stringField("room_id") != roomID {
		t.Errorf("the alias %s is answered with %q, which is not the room it was made for (%s)",
			alias, resolved.stringField("room_id"), roomID)
	}
	servers, _ := resolved.Body["servers"].([]any)
	if len(servers) == 0 {
		t.Errorf("the answer about %s names no server that can serve %s: %s", alias, roomID, resolved.Raw)
	}

	// And a client of the other server can join by that name: the alias carries a server,
	// that server says what it addresses, and the join goes on from there.
	joined := two.mustDo(http.StatusOK, http.MethodPost,
		"/_matrix/client/v3/join/"+url.PathEscape(alias), bob.Token, matrix.Object{})
	if joined.stringField("room_id") != roomID {
		t.Errorf("%s joining by the alias %s ended in %q, want %s: %s",
			bob.UserID, alias, joined.stringField("room_id"), roomID, joined.Raw)
	}
	if stateID(t, one, alice, roomID, matrix.TypeMember, bob.UserID) == "" {
		t.Errorf("%s joined %s by alias, but %s does not record the membership", bob.UserID, roomID, one.name)
	}

	// An alias that does not exist is a miss on both ends, and the miss says so rather than
	// inventing a room.
	missing := federationGET(t, two, one,
		"/_matrix/federation/v1/query/directory?room_alias="+url.QueryEscape("#no-such-alias:"+one.name))
	if missing.Status != http.StatusNotFound {
		t.Errorf("an alias that was never made is answered %d, want 404: %s", missing.Status, missing.Raw)
	}
	notHere := two.do(http.MethodPost, "/_matrix/client/v3/join/"+url.PathEscape("#no-such-alias:"+one.name),
		bob.Token, matrix.Object{})
	if notHere.Status == http.StatusOK {
		t.Errorf("%s joined a room through an alias that does not exist: %s", bob.UserID, notHere.Raw)
	}
}

// TestASpaceServesItsOwnSideToAPeer is the shape check on the federation hierarchy answer: the one
// room that was asked about, the children of it this server can describe, and the child links that
// say where the tree continues. It is not the client answer: a peer is not a person, and the tree
// below a child belongs to whichever server holds that child to describe.
func TestASpaceServesItsOwnSideToAPeer(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")

	space := one.createRoom(alice, map[string]any{
		"name":             "Described space",
		"creation_content": map[string]any{"type": "m.space"},
	})
	child := one.createRoom(alice, map[string]any{"preset": "public_chat", "name": "Described room"})
	worldReadable(t, one, alice, space)
	worldReadable(t, one, alice, child)
	linkSpace(t, one, alice, space, child, one.name)

	answer := federationGET(t, two, one, "/_matrix/federation/v1/hierarchy/"+space)
	if answer.Status != http.StatusOK {
		t.Fatalf("%s asking for the hierarchy of %s: %d %s", two.name, space, answer.Status, answer.Raw)
	}
	room, _ := answer.Body["room"].(matrix.Object)
	if room == nil || room["room_id"] != space {
		t.Fatalf("the hierarchy answer of %s does not describe the room that was asked for: %s", space, answer.Raw)
	}
	// The links are what let a walk continue. A peer that is told about a space and not about
	// its children still knows where to ask next.
	links, _ := room["children_state"].([]any)
	if len(links) == 0 {
		t.Fatalf("the hierarchy answer of %s carries no children_state, so a walk cannot continue from it: %s",
			space, answer.Raw)
	}
	children, _ := answer.Body["children"].([]any)
	var entry matrix.Object
	for _, item := range children {
		described, ok := item.(matrix.Object)
		if !ok {
			continue
		}
		if id, _ := described["room_id"].(string); id == child {
			entry = described
		}
	}
	if entry == nil || entry["room_id"] != child {
		t.Fatalf("the hierarchy of %s does not describe its child %s: %s", space, child, answer.Raw)
	}
	if entry["name"] != "Described room" {
		t.Errorf("the child of %s is described as %v, want the name its own server gives it", space, entry["name"])
	}
	if _, found := entry["children_state"]; found {
		t.Errorf("the hierarchy of %s describes %s with children_state, which is the shape that invites a peer to skip asking the server that holds it",
			space, child)
	}
}
