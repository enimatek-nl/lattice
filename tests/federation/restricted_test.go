package federation

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A restricted room admits people through another room, which means the admission rests on
// somebody's word. The two tests below are about who says that word and who has to believe it:
// the room's own server can only vouch for its own users, and a joining server that is given
// nobody to name cannot make the join up out of its own head.

// TestARestrictedRoomNamesWhoLetTheJoinerIn is the shape of a restricted join across servers. The
// room's server names the user who vouches for the joiner, because it is the only server that can
// say who that user is; the joining server carries that name in the signed event; and the room
// keeps it, so the room's members can later see who let a stranger in.
func TestARestrictedRoomNamesWhoLetTheJoinerIn(t *testing.T) {
	cluster := newCluster(t)
	hs1, hs2 := cluster.one(), cluster.two()
	alice := hs1.register("alice", "wonderland")
	bob := hs2.register("bob", "rubberduck")

	allowed := hs1.createRoom(alice, map[string]any{"preset": "public_chat", "name": "the allowed room"})
	room := hs1.createRoom(alice, map[string]any{"preset": "public_chat", "name": "the restricted room", "room_version": "8"})
	hs1.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules", alice.Token,
		map[string]any{
			"join_rule": "restricted",
			"allow":     []map[string]any{{"type": "m.space", "room_id": allowed}},
		})

	// Bob is nobody here, and the room says so.
	if got := joinVia(hs2, bob, room, serverOne); got.Status != http.StatusForbidden {
		t.Fatalf("bob joining a restricted room he has no part in: status %d (body %s), want 403", got.Status, got.Raw)
	}

	// Membership of the other room is the key.
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/join/"+allowed+"?server_name="+serverOne, bob.Token, map[string]any{})
	if got := joinVia(hs2, bob, room, serverOne); got.Status != http.StatusOK {
		t.Fatalf("bob joining the restricted room through the allowed one: status %d (body %s), want 200", got.Status, got.Raw)
	}
	awaitMembership(t, hs1, room, bob.UserID, "join")
	awaitMembership(t, hs2, room, bob.UserID, "join")

	content := membershipContent(t, hs1, alice, room, bob.UserID)
	if authorised, _ := content["join_authorised_via_users_server"].(string); authorised != alice.UserID {
		t.Errorf("the room records the join as authorised by %q, want %q: %v", authorised, alice.UserID, content)
	}
	// The leave has to travel back. A leave cites the join that put the user in the room, and a
	// join whose witness is missing from its auth events is a join the room's rules refuse the
	// second time they are read - so the leave of a restricted join is the test that the witness
	// was cited properly.
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+room+"/leave", bob.Token, map[string]any{})
	awaitMembership(t, hs1, room, bob.UserID, "leave")

}

// TestARestrictedRoomAdmitsThroughARoomTheRoomsOwnServerHosts is the restricted join seen from the
// server that is in the room but not in charge of it. Both the restricted room and the room it
// admits people through belong to the peer; the joiner lives here, and this server is a member of
// the restricted room already. It could ask the peer to make the join, but it does not have to:
// it holds the graph and the state, so it can build the join and offer it - and the peer's job is
// to decide whether the joiner belongs to the room it admits people through, which is the half
// only the peer can answer.
func TestARestrictedRoomAdmitsThroughARoomTheRoomsOwnServerHosts(t *testing.T) {
	cluster := newCluster(t)
	hs1, hs2 := cluster.one(), cluster.two()
	stop := hs1.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stop()
	charlie := hs2.register("charlie", "teatime")

	allowed := hs2.createRoom(charlie, map[string]any{"preset": "public_chat", "name": "the space"})
	room := hs2.createRoom(charlie, map[string]any{"preset": "public_chat", "name": "the restricted room", "room_version": "8"})
	hs2.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules", charlie.Token,
		map[string]any{
			"join_rule": "restricted",
			"allow":     []map[string]any{{"type": "m.space", "room_id": allowed}},
		})

	// alice lives here and is in the restricted room, so this server is one of the servers the
	// room counts.
	alice := hs1.register("alice", "wonderland")
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+room+"/invite", charlie.Token, map[string]any{"user_id": alice.UserID})
	if got := joinVia(hs1, alice, room, serverTwo); got.Status != http.StatusOK {
		t.Fatalf("alice accepting her invite to %s: status %d (body %s), want 200", room, got.Status, got.Raw)
	}

	bob := hs1.register("bob", "rubberduck")
	if got := joinVia(hs1, bob, allowed, serverTwo); got.Status != http.StatusOK {
		t.Fatalf("bob joining the admitting room %s through its own server: status %d (body %s), want 200", allowed, got.Status, got.Raw)
	}
	if got := joinVia(hs1, bob, room, serverOne); got.Status != http.StatusOK {
		t.Fatalf("bob joining %s through %s, which is in the room: status %d (body %s), want 200", room, serverOne, got.Status, got.Raw)
	}
	awaitMembership(t, hs2, room, bob.UserID, matrix.MembershipJoin)
	awaitMembership(t, hs1, room, bob.UserID, matrix.MembershipJoin)
}

// TestARestrictedRoomRefusesWhenNobodyCanVouch is the same join with nobody to vouch for it. The
// room admits people through a room none of this server's users belongs to, so the room's server
// has no name to give, and a join that cannot be authorised has to fail rather than arrive as an
// event the room's own rules refuse.
func TestARestrictedRoomRefusesWhenNobodyCanVouch(t *testing.T) {
	cluster := newCluster(t)
	hs1, hs2 := cluster.one(), cluster.two()
	alice := hs1.register("alice", "wonderland")
	bob := hs2.register("bob", "rubberduck")

	room := hs1.createRoom(alice, map[string]any{"preset": "public_chat", "name": "the closed room", "room_version": "8"})
	hs1.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules", alice.Token,
		map[string]any{
			"join_rule": "restricted",
			"allow":     []map[string]any{{"type": "m.space", "room_id": "!notin:elsewhere.example"}},
		})
	if got := joinVia(hs2, bob, room, serverOne); got.Status == http.StatusOK {
		t.Fatalf("bob joined a restricted room that nobody could authorise (body %s)", got.Raw)
	}
}

// membershipContent reads the content of a user's membership event as the room's server holds it.
func membershipContent(t *testing.T, s *server, sess *session, roomID, userID string) map[string]any {
	t.Helper()
	for _, raw := range stateOf(t, s, sess, roomID) {
		event, _ := raw.(map[string]any)
		if event["type"] != "m.room.member" || event["state_key"] != userID {
			continue
		}
		content, _ := event["content"].(map[string]any)
		return content
	}
	t.Fatalf("%s holds no membership event for %s in %s", s.name, userID, roomID)
	return nil
}

// TestARestrictedRoomAdmitsItsOwnMembersOnItsOwnWord is the half of a restricted join that needs no
// ask across servers. This server holds the room, has a member who may invite and belongs to the
// room that admits people, and the joiner visibly belongs to that room too: the admission is this
// server's to make, and asking the room's own server would make one server the gatekeeper for a
// room its members share.
func TestARestrictedRoomAdmitsItsOwnMembersOnItsOwnWord(t *testing.T) {
	cluster := newCluster(t)
	hs1, hs2 := cluster.one(), cluster.two()
	stop := hs2.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stop()
	alice := hs1.register("alice", "wonderland")
	bob := hs2.register("bob", "rubberduck")
	charlie := hs2.register("charlie", "hatter")

	room, allowed := restrictedPair(t, hs1, alice)

	// Bob is let in the ordinary way, and then given the room's invite level.
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/join/"+allowed+"?server_name="+serverOne, bob.Token, map[string]any{})
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/join/"+room+"?server_name="+serverOne, bob.Token, map[string]any{})
	hs1.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.power_levels", alice.Token,
		map[string]any{
			"invite": 100,
			"users":  map[string]any{alice.UserID: 100, bob.UserID: 100},
		})

	// Charlie reaches the room through hs2 alone: hs2 has somebody who can vouch for the join, so
	// it does not have to ask hs1.
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/join/"+allowed+"?server_name="+serverOne, charlie.Token, map[string]any{})
	if got := joinVia(hs2, charlie, room, serverTwo); got.Status != http.StatusOK {
		t.Fatalf("charlie joining the restricted room through hs2, which has a witness: status %d (body %s), want 200", got.Status, got.Raw)
	}
	awaitMembership(t, hs2, room, charlie.UserID, "join")
	awaitMembership(t, hs1, room, charlie.UserID, "join")

	content := membershipContent(t, hs1, alice, room, charlie.UserID)
	if authorised, _ := content["join_authorised_via_users_server"].(string); authorised != bob.UserID {
		t.Errorf("the join is authorised by %q, want the local witness %q: %v", authorised, bob.UserID, content)
	}
}

// TestARestrictedRoomAskedAloneRefusesWhenItCannotVouch is the join that no amount of naming
// servers can fix. The client asks a server that holds the room but has no member who belongs to the
// admitting room, and gets a refusal; naming the room's own server as well changes nothing, because
// the room's rule reads the joiner's own membership and the joiner is not in that room either. What
// fixes it is the fact the rule asks about: once the joiner belongs to the admitting room, the same
// client and the same server name finish the join.
func TestARestrictedRoomAskedAloneRefusesWhenItCannotVouch(t *testing.T) {
	cluster := newCluster(t)
	hs1, hs2 := cluster.one(), cluster.two()
	stop := hs2.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stop()
	alice := hs1.register("alice", "wonderland")
	bob := hs2.register("bob", "rubberduck")
	charlie := hs2.register("charlie", "hatter")

	room, allowed := restrictedPair(t, hs1, alice)
	hs1.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.power_levels", alice.Token,
		map[string]any{"invite": 100, "users": map[string]any{alice.UserID: 100}})

	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/join/"+allowed+"?server_name="+serverOne, bob.Token, map[string]any{})
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/join/"+room+"?server_name="+serverOne, bob.Token, map[string]any{})

	// charlie belongs to nothing. Naming one server, then both, is refused both times: a second
	// server in the list is another server to ask, not a looser rule to read.
	if got := joinVia(hs2, charlie, room, serverTwo); got.Status == http.StatusOK {
		t.Fatalf("charlie joined a restricted room he belongs to nothing in (body %s)", got.Raw)
	}
	if got := joinVia(hs2, charlie, room, serverTwo, serverOne); got.Status == http.StatusOK {
		t.Fatalf("charlie joined a restricted room through the room's own server without belonging to the admitting room (body %s)", got.Raw)
	}

	// The fact the rule asks about is charlie's own membership of the admitting room. Once he has
	// it, the same join through the same server goes through.
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/join/"+allowed+"?server_name="+serverOne, charlie.Token, map[string]any{})
	if got := joinVia(hs2, charlie, room, serverTwo); got.Status != http.StatusOK {
		t.Fatalf("charlie joining the restricted room once he belongs to the admitting room: status %d (body %s), want 200", got.Status, got.Raw)
	}
	awaitMembership(t, hs1, room, charlie.UserID, "join")
}

// restrictedPair makes a restricted room and the room its rule reads, both hosted by host and
// created by creator, and returns them in that order.
func restrictedPair(t *testing.T, host *server, creator *session) (room, allowed string) {
	t.Helper()
	allowed = host.createRoom(creator, map[string]any{"preset": "public_chat", "name": "the allowed room"})
	room = host.createRoom(creator, map[string]any{"preset": "public_chat", "name": "the restricted room", "room_version": "8"})
	host.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules", creator.Token,
		map[string]any{"join_rule": "restricted", "allow": []map[string]any{{"type": "m.space", "room_id": allowed}}})
	return room, allowed
}
