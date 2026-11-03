package federation

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestAUserWithoutInvitePowerCannotInviteThroughAnotherServer is the same rule read from the
// weaker side. A room restricts who may invite; a user with no power in it asks a server that
// holds a copy of the room but does not host it to invite somebody. The room's rule is the room's,
// not the hosting server's, and a second server holding the room is not a second door.
func TestAUserWithoutInvitePowerCannotInviteThroughAnotherServer(t *testing.T) {
	cluster := newCluster(t)
	hs1, hs2 := cluster.one(), cluster.two()
	stop := hs2.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stop()

	alice := hs1.register("alice", "wonderland")
	bob := hs2.register("bob", "rubberduck")
	charlie := hs2.register("charlie", "hatter")

	room := hs1.createRoom(alice, map[string]any{"preset": "private_chat", "name": "the room"})
	hs1.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.power_levels", alice.Token,
		map[string]any{"invite": 100, "users": map[string]any{alice.UserID: 100, bob.UserID: 10}})
	hs1.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+room+"/invite", alice.Token, map[string]any{"user_id": bob.UserID})
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/join/"+room+"?server_name="+serverOne, bob.Token, map[string]any{})

	got := hs2.do(http.MethodPost, "/_matrix/client/v3/rooms/"+room+"/invite", bob.Token, map[string]any{"user_id": charlie.UserID})
	if got.Status != http.StatusForbidden {
		t.Fatalf("bob inviting charlie to a room that restricts invites: status %d (body %s), want 403", got.Status, got.Raw)
	}
}
