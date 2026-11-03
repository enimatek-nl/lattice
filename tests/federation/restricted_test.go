package federation

import (
	"net/http"
	"testing"
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
