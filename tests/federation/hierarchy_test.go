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
	// A negative depth means the caller did not name one, which is a different request from
	// max_depth=0 and the one that matters for the whole-space reading.
	path := fmt.Sprintf("/_matrix/client/v1/rooms/%s/hierarchy", roomID)
	start := true
	appendParam := func(param string) {
		if start {
			path += "?" + param
			start = false
			return
		}
		path += "&" + param
	}
	if maxDepth >= 0 {
		appendParam(fmt.Sprintf("max_depth=%d", maxDepth))
	}
	for _, extra := range query {
		appendParam(extra)
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

// TestASpaceWalkedWithoutSayingHowDeep reaches the far side of a space and back. The
// specification names no default depth for this endpoint, and a caller that did not name one
// is asking to see the space, so the walk goes the length of it - including a branch that
// leaves this server, hangs under somebody else's room, and comes back to a room of our own
// that we only learn about because the other server's answer named it and then asked us about
// it. Naming a window is honoured exactly: the same tree at max_depth=1 stops at the doors.
func TestASpaceWalkedWithoutSayingHowDeep(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")

	root := one.createRoom(alice, map[string]any{
		"name":             "The root",
		"creation_content": map[string]any{"type": "m.space"},
	})
	farSpace := two.createRoom(bob, map[string]any{
		"name":             "A space over there",
		"creation_content": map[string]any{"type": "m.space"},
	})
	near := one.createRoom(alice, map[string]any{"preset": "public_chat", "name": "A room back home"})
	worldReadable(t, two, bob, farSpace)
	for _, room := range []string{root, near} {
		worldReadable(t, one, alice, room)
	}
	linkSpace(t, one, alice, root, farSpace, two.name)
	linkSpace(t, two, bob, farSpace, near, one.name)

	asked, _ := clientHierarchy(t, one, alice, root, 1, "")
	if _, found := roomsOf(asked)[near]; found {
		t.Errorf("max_depth=1 lists %s, which is three levels down from %s: %v", near, root, asked)
	}

	walked, incomplete := clientHierarchy(t, one, alice, root, -1, "")
	if incomplete {
		t.Errorf("a walk that named no depth reports an incomplete chain for a space both servers can describe: %v", walked)
	}
	found := roomsOf(walked)
	for _, want := range []string{root, farSpace, near} {
		if _, ok := found[want]; !ok {
			t.Errorf("a walk of %s that named no depth leaves out %s: %v", root, want, walked)
		}
	}
}

// TestAPeerWalksTheSpaceRootedHere is the inbound half: another server asks about one of our
// rooms, and is answered the room plus the children it may read - which is not every room we
// hold, and not the whole tree either.
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

	answer := federationGET(t, two, one, "/_matrix/federation/v1/hierarchy/"+open)
	if answer.Status != http.StatusOK {
		t.Fatalf("%s asking %s for the hierarchy of %s: %d %s",
			two.name, one.name, open, answer.Status, answer.Raw)
	}
	asked, _ := answer.Body["room"].(matrix.Object)
	if asked == nil || asked["room_id"] != open {
		t.Fatalf("the hierarchy answer for %s does not describe the room asked for: %s", open, answer.Raw)
	}
	children, _ := answer.Body["children"].([]any)
	if len(children) == 0 {
		t.Fatalf("the hierarchy of %s is answered with no children, which leaves out %s: %s", open, child, answer.Raw)
	}
	names := map[string]bool{}
	for _, item := range children {
		entry, _ := item.(matrix.Object)
		if entry == nil {
			continue
		}
		if id, _ := entry["room_id"].(string); id != "" {
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
	closed := federationGET(t, two, one, "/_matrix/federation/v1/hierarchy/"+quiet)
	if closed.Status != http.StatusNotFound {
		t.Errorf("%s asking for the hierarchy of the space %s was answered %d, want %d: %s",
			two.name, quiet, closed.Status, http.StatusNotFound, closed.Raw)
	}
	if _, found := closed.Body["room"]; found {
		t.Errorf("a refused hierarchy of %s still answers with a room: %s", quiet, closed.Raw)
	}
}

// TestARestrictedSpaceChildIsOfferedToTheServerItAdmits is a restricted room seen over the
// federation hierarchy endpoint. The peer is not told who is asking, so it answers for servers:
// the room is offered when some user of the asking server could get in, and it travels with the
// rooms its join rules name so the asking server can decide for the one person who asked. Until
// this server has somebody in the room that admits the join, it cannot make that offer at all -
// it does not know the asking server has anybody there.
func TestARestrictedSpaceChildIsOfferedToTheServerItAdmits(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := one.register("bob", "rubberduck")
	charlie := two.register("charlie", "hatter")

	space := one.createRoom(alice, map[string]any{
		"preset":           "public_chat",
		"name":             "The space",
		"creation_content": map[string]any{"type": "m.space"},
	})
	worldReadable(t, one, alice, space)
	room := two.createRoom(charlie, map[string]any{
		"preset":       "public_chat",
		"name":         "The room in the space",
		"room_version": "8",
	})
	two.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules", charlie.Token,
		map[string]any{
			"join_rule": "restricted",
			"allow":     []map[string]any{{"type": "m.room_membership", "room_id": space}},
		})
	linkSpace(t, one, alice, space, room, two.name)

	seen := func(sess *session) map[string]matrix.Object {
		t.Helper()
		rooms, _ := clientHierarchy(t, one, sess, space, -1)
		return roomsOf(rooms)
	}

	// Nobody on either server is in the space as far as two can see, so two cannot say that
	// anybody on one may join: the branch stays closed rather than being described on a guess.
	if found := seen(alice); found[room] != nil {
		t.Errorf("two describes the restricted room %s before anybody on one is known to belong to the space: %v", room, found)
	}

	// charlie joins the space. two now holds the space's membership and can see that one has a
	// member there: alice. The room is offered, and named with the room that admits the join so
	// that one can decide for its own users.
	if got := joinVia(two, charlie, space, serverOne); got.Status != http.StatusOK {
		t.Fatalf("charlie joining the space: %d %s", got.Status, got.Raw)
	}
	found := seen(alice)
	entry, ok := found[room]
	if !ok {
		t.Fatalf("alice, a member of the space, is not shown the room it admits: %v", found)
	}
	allowed, _ := entry["allowed_room_ids"].([]any)
	if len(allowed) == 0 {
		t.Errorf("the restricted room is described without the rooms that admit a join, so this server cannot tell whose membership the rule asks about: %v", entry)
	}

	// Bob is on the same server and is not in the space. The peer answered for the server, and
	// this server is the one that has to answer for the person.
	if found := seen(bob); found[room] != nil {
		t.Errorf("bob, who belongs to nothing, is shown a room the space admits: %v", found)
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
