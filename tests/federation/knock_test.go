package federation

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestAKnockOnARemoteRoomReachesTheRoom is the point of knocking: the request is made on
// one server and answered on another. A knock kept on the knocking server is a note to
// self - the people who can let somebody in are in the room, and the room is elsewhere.
func TestAKnockOnARemoteRoomReachesTheRoom(t *testing.T) {
	cluster := newCluster(t)
	hs1, hs2 := cluster.one(), cluster.two()
	stop := hs2.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stop()

	alice := hs1.register("alice", "wonderland")
	charlie := hs2.register("charlie", "hatter")

	room := hs1.createRoom(alice, map[string]any{"preset": "private_chat", "name": "the locked room", "room_version": "7"})
	hs1.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules", alice.Token,
		map[string]any{"join_rule": "knock"})

	got := hs2.do(http.MethodPost, "/_matrix/client/v3/knock/"+room, charlie.Token,
		map[string]any{"reason": "let me in"})
	if got.Status != http.StatusOK {
		t.Fatalf("charlie knocking on a room held by the other server: status %d (body %s), want 200", got.Status, got.Raw)
	}

	awaitMembership(t, hs1, room, charlie.UserID, "knock")
	content := membershipContent(t, hs1, alice, room, charlie.UserID)
	if reason, _ := content["reason"].(string); reason != "let me in" {
		t.Errorf("the room was told %q, want the reason charlie gave: %v", reason, content)
	}
	// The knocking server keeps its own copy: the client of a user who knocked shows the
	// knock as pending, and a second knock is judged against it.
	awaitMembership(t, hs2, room, charlie.UserID, "knock")

	// Knocking again is allowed: a room that has not answered is a room that has not
	// refused, and a knock is how the ask is repeated.
	again := hs2.do(http.MethodPost, "/_matrix/client/v3/knock/"+room, charlie.Token,
		map[string]any{"reason": "please, let me in"})
	if again.Status != http.StatusOK {
		t.Fatalf("charlie knocking a second time: status %d (body %s), want 200", again.Status, again.Raw)
	}
	awaitMembership(t, hs1, room, charlie.UserID, "knock")
	if content = membershipContent(t, hs1, alice, room, charlie.UserID); content["reason"] != "please, let me in" {
		t.Errorf("the second knock left %v in the room's view: %v", content["reason"], content)
	}
}

// TestAKnockOnARoomThatDoesNotAdmitKnocking is the same request in a room that has not
// said it may be knocked on. The room's rule is the room's, and the server asked is not
// allowed to be more welcoming than the room.
func TestAKnockOnARoomThatDoesNotAdmitKnocking(t *testing.T) {
	cluster := newCluster(t)
	hs1, hs2 := cluster.one(), cluster.two()
	alice := hs1.register("alice", "wonderland")
	charlie := hs2.register("charlie", "hatter")

	room := hs1.createRoom(alice, map[string]any{"preset": "private_chat", "name": "an invite-only room", "room_version": "7"})

	got := hs2.do(http.MethodPost, "/_matrix/client/v3/knock/"+room, charlie.Token, map[string]any{"reason": "let me in"})
	if got.Status != http.StatusForbidden {
		t.Fatalf("charlie knocking on a room that is entered by invite only: status %d (body %s), want 403", got.Status, got.Raw)
	}
	members := hs1.do(http.MethodGet, "/_matrix/client/v3/rooms/"+room+"/members", alice.Token, nil)
	if members.Status != http.StatusOK {
		t.Fatalf("reading the room's members: status %d (body %s)", members.Status, members.Raw)
	}
	chunk, _ := members.Body["chunk"].([]any)
	for _, item := range chunk {
		event, _ := item.(map[string]any)
		if event["state_key"] == charlie.UserID {
			t.Fatalf("a refused knock still left a membership event for %s in the room: %v", charlie.UserID, event)
		}
	}
}

// TestAKnockerIsSeenByTheRoomBeforeItIsAnswered keeps the two halves of the flow in the
// order the people in the room need: a knock has to be visible before a moderator can
// accept it, and accepting it is an invite, which the knocker's server has to receive for
// a room it is not otherwise in.
func TestAKnockerIsSeenByTheRoomBeforeItIsAnswered(t *testing.T) {
	cluster := newCluster(t)
	hs1, hs2 := cluster.one(), cluster.two()
	// Both servers send: hs1 relays the invite and the knock it accepted, hs2 relays the
	// knock it signed and the join that follows the invite.
	stopOne := hs1.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stopOne()
	stopTwo := hs2.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stopTwo()

	alice := hs1.register("alice", "wonderland")
	charlie := hs2.register("charlie", "hatter")

	room := hs1.createRoom(alice, map[string]any{"preset": "private_chat", "name": "the locked room", "room_version": "7"})
	hs1.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules", alice.Token,
		map[string]any{"join_rule": "knock"})
	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/knock/"+room, charlie.Token, map[string]any{"reason": "hello"})

	// Alice sees the knock and lets charlie in.
	hs1.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+room+"/invite", alice.Token,
		map[string]any{"user_id": charlie.UserID})
	awaitMembership(t, hs2, room, charlie.UserID, "invite")

	hs2.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/join/"+room, charlie.Token, map[string]any{})
	awaitMembership(t, hs1, room, charlie.UserID, "join")

	// In the room already: the room answered this user, and a knock is a question that has
	// been answered. The refusal is this server's to make - it holds the membership - and it
	// has to reach the client as the room's answer rather than as a failure of the wiring.
	after := hs2.do(http.MethodPost, "/_matrix/client/v3/knock/"+room, charlie.Token, map[string]any{"reason": "let me in again"})
	if after.Status != http.StatusForbidden {
		t.Fatalf("charlie knocking on a room they are in: status %d (body %s), want 403", after.Status, after.Raw)
	}
}
