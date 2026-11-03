package federation

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A space is a room, so its children may live anywhere; and a hierarchy walk that stops at
// this server's own doors shows a space with holes in it and says nothing about where the
// rest went. These tests put a tree across two servers and ask for it from both ends: a
// client of one server walking a space whose branches hang over to the other, and a peer
// asking for the part of a space that starts here.

// linkSpace adds an `m.space.child` edge to a space, naming the servers that can serve the
// child. That `via` list is not decoration: it is how the space says who to ask.
func linkSpace(t *testing.T, s *server, sess *session, spaceID, childID string, via ...string) {
	t.Helper()
	list := make([]any, 0, len(via))
	for _, server := range via {
		list = append(list, server)
	}
	setRoomState(t, s, sess, spaceID, matrix.TypeSpaceChild, url.PathEscape(childID),
		matrix.Object{"via": list})
}

// worldReadable opens a room's history to anyone, which is what makes a room describable to
// a server that is not in it. Complement's own federated-space test does the same thing for
// the same reason.
func worldReadable(t *testing.T, s *server, sess *session, roomID string) {
	t.Helper()
	setRoomState(t, s, sess, roomID, matrix.TypeHistoryVis, "",
		matrix.Object{"history_visibility": matrix.HistoryWorldReadable})
}

// clientHierarchy reads a hierarchy as a client does and returns its rooms with the flag
// that says whether the walk had to leave something out.
func clientHierarchy(t *testing.T, s *server, sess *session, roomID string, maxDepth int, query ...string) ([]matrix.Object, bool) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v1/rooms/%s/hierarchy?max_depth=%d", roomID, maxDepth)
	for _, extra := range query {
		path += "&" + extra
	}
	got := s.mustDo(http.StatusOK, http.MethodGet, path, sess.Token, nil)
	body, ok := decoded(t, got.Raw).(matrix.Object)
	if !ok {
		t.Fatalf("the hierarchy of %s is not an object: %s", roomID, got.Raw)
	}
	list, _ := body["rooms"].([]any)
	rooms := make([]matrix.Object, 0, len(list))
	for _, entry := range list {
		room, _ := entry.(matrix.Object)
		if room == nil {
			t.Fatalf("the hierarchy of %s holds something that is not a room: %v", roomID, entry)
		}
		rooms = append(rooms, room)
	}
	incomplete, _ := body["incomplete_chain"].(bool)
	return rooms, incomplete
}

// roomsOf indexes a hierarchy page by room id.
func roomsOf(rooms []matrix.Object) map[string]matrix.Object {
	byID := map[string]matrix.Object{}
	for _, room := range rooms {
		if id, _ := room["room_id"].(string); id != "" {
			byID[id] = room
		}
	}
	return byID
}

// TestASpaceThatSpansTwoServersIsWalkedFromOneSide is the outbound half of the hierarchy:
// a client asks this server for a space, some of whose children are not this server's
// rooms, and the answer holds the rooms on the other side described by that other server.
// The rooms are world readable because a space that is not is not describable to a stranger,
// which is the rule the next test checks from the other direction.
func TestASpaceThatSpansTwoServersIsWalkedFromOneSide(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")

	root := one.createRoom(alice, map[string]any{
		"name":             "Everything",
		"creation_content": map[string]any{"type": "m.space"},
	})
	farRoom := two.createRoom(bob, map[string]any{"preset": "public_chat", "name": "The far room"})
	farSpace := two.createRoom(bob, map[string]any{
		"name":             "The far space",
		"creation_content": map[string]any{"type": "m.space"},
	})
	for _, room := range []string{farRoom, farSpace} {
		worldReadable(t, two, bob, room)
	}
	linkSpace(t, two, bob, farSpace, farRoom, two.name)
	linkSpace(t, one, alice, root, farRoom, two.name)
	linkSpace(t, one, alice, root, farSpace, two.name)

	// One level down: the space's children, including the ones this server has never held.
	shallow, incomplete := clientHierarchy(t, one, alice, root, 1)
	if incomplete {
		t.Errorf("the hierarchy of %s reports an incomplete chain, although %s can be asked for both children: %v",
			root, two.name, shallow)
	}
	found := roomsOf(shallow)
	if _, ok := found[root]; !ok {
		t.Errorf("the hierarchy of %s does not include the space itself: %v", root, shallow)
	}
	for _, want := range []struct {
		roomID, name string
	}{
		{farRoom, "The far room"},
		{farSpace, "The far space"},
	} {
		room, ok := found[want.roomID]
		if !ok {
			t.Errorf("the hierarchy of %s leaves out %s, a child of it on %s: %v", root, want.roomID, two.name, shallow)
			continue
		}
		if room["name"] != want.name {
			t.Errorf("%s is described as %v in the hierarchy of %s, want the name its own server gives it (%q)",
				want.roomID, room["name"], root, want.name)
		}
	}

	// Two levels down: the walk follows the remote space into its own child, and a room is
	// still listed once even though two edges reach it.
	deep, incomplete := clientHierarchy(t, one, alice, root, 2)
	if incomplete {
		t.Errorf("a walk two levels into %s reports an incomplete chain: %v", root, deep)
	}
	seen := map[string]int{}
	for _, room := range deep {
		if id, _ := room["room_id"].(string); id != "" {
			seen[id]++
		}
	}
	if seen[farRoom] != 1 {
		t.Errorf("%s is listed %d times in the hierarchy of %s, where two edges point at it",
			farRoom, seen[farRoom], root)
	}
	if seen[farSpace] != 1 {
		t.Errorf("%s is listed %d times in the hierarchy of %s", farSpace, seen[farSpace], root)
	}
}

// TestAPeerWalksTheSpaceRootedHere is the inbound half: another server asks for the tree
// that starts at one of our rooms, and is answered the rooms it may read - which is not
// every room we hold.
func TestAPeerWalksTheSpaceRootedHere(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")

	open := one.createRoom(alice, map[string]any{
		"name":             "Open space",
		"creation_content": map[string]any{"type": "m.space"},
	})
	child := one.createRoom(alice, map[string]any{"preset": "public_chat", "name": "Open room"})
	worldReadable(t, one, alice, open)
	worldReadable(t, one, alice, child)
	linkSpace(t, one, alice, open, child, one.name)

	answer := federationGET(t, two, one, "/_matrix/federation/v1/hierarchy/"+open+"?max_depth=1")
	if answer.Status != http.StatusOK {
		t.Fatalf("%s asking %s for the hierarchy of %s: %d %s",
			two.name, one.name, open, answer.Status, answer.Raw)
	}
	rooms, _ := answer.Body["rooms"].([]any)
	if len(rooms) < 2 {
		t.Fatalf("the hierarchy of %s is answered with %d rooms, which leaves out its child: %s",
			open, len(rooms), answer.Raw)
	}
	names := map[string]bool{}
	for _, entry := range rooms {
		room, _ := entry.(matrix.Object)
		if room == nil {
			continue
		}
		if id, _ := room["room_id"].(string); id != "" {
			names[id] = true
		}
	}
	if !names[child] {
		t.Errorf("the hierarchy of %s served to %s does not name the child %s: %s", open, two.name, child, answer.Raw)
	}

	// The same question about a space that is not open to be read. A space is a room, and
	// the rule about who may read a room is the rule about who may be told its shape: the
	// answer is a refusal, not an empty tree, because "there is nothing here" is a claim
	// this server is not entitled to make about a room that has plenty in it.
	quiet := one.createRoom(alice, map[string]any{
		"name":             "Quiet space",
		"creation_content": map[string]any{"type": "m.space"},
	})
	linkSpace(t, one, alice, quiet, child, one.name)
	closed := federationGET(t, two, one, "/_matrix/federation/v1/hierarchy/"+quiet+"?max_depth=1")
	if closed.Status != http.StatusForbidden {
		t.Errorf("%s asking for the hierarchy of the space %s was answered %d, want %d: %s",
			two.name, quiet, closed.Status, http.StatusForbidden, closed.Raw)
	}
	if _, found := closed.Body["rooms"]; found {
		t.Errorf("a refused hierarchy of %s still answers with rooms: %s", quiet, closed.Raw)
	}
}

// TestASpaceChildThatCannotBeAsked keeps the honest answer available: when a child cannot be
// described by anyone, the chain is reported incomplete. A tree with a hole in it shown as
// complete is how a space quietly loses rooms.
func TestASpaceChildThatCannotBeAskedLeavesTheChainIncomplete(t *testing.T) {
	c := newCluster(t)
	one := c.one()
	alice := one.register("alice", "wonderland")

	root := one.createRoom(alice, map[string]any{
		"name":             "A space with a missing branch",
		"creation_content": map[string]any{"type": "m.space"},
	})
	worldReadable(t, one, alice, root)
	unreachable := "!nowhere-to-be-found:" + serverStranger
	linkSpace(t, one, alice, root, unreachable, serverStranger)

	rooms, incomplete := clientHierarchy(t, one, alice, root, 1)
	if !incomplete {
		t.Errorf("the hierarchy of %s claims a complete chain although %s was never reachable: %v",
			root, unreachable, rooms)
	}
	if _, found := roomsOf(rooms)[unreachable]; found {
		t.Errorf("the hierarchy of %s invents the room %s, which nobody described", root, unreachable)
	}
}
