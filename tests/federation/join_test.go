package federation

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// A join is the first thing in federation that has to be true on two servers at once.
// Until it works, every other federation test has to seed the room it is about, and a
// seeded room says nothing about whether a server can learn one for itself. These tests
// are the ones that replace that seeding: the room lives on one server, a user of the
// other joins it by naming a server, and both servers are then asked what they know.
//
// The ban test is the second half. A join teaches a server the room's past; the room has
// to be able to change its mind about a user afterwards, and the change has to travel
// back. A ban that only the hosting server knows is a ban the other server keeps
// relaying events for.

// joinVia asks a server to join one of its users to a room hosted elsewhere, naming the
// servers to ask. It returns the response so a test can say what went wrong.
func joinVia(s *server, sess *session, roomID string, via ...string) response {
	viaQuery := ""
	for _, server := range via {
		viaQuery += "server_name=" + server + "&"
	}
	return s.do(http.MethodPost, "/_matrix/client/v3/join/"+roomID+"?"+viaQuery, sess.Token, map[string]any{})
}

// awaitMembership waits for a membership row to say what it should, and fails the test
// with the reason it did not. Delivery across servers is asynchronous, so the wait is
// bounded and the failure says what was last seen.
func awaitMembership(t *testing.T, s *server, roomID, userID, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last string
	for {
		members, err := s.db.MembershipRepo.Get(roomID, userID)
		if err == nil {
			if members.Membership == want {
				return
			}
			last = fmt.Sprintf("membership is %q", members.Membership)
		} else if persist.IsNotFound(err) {
			last = "no membership row"
		} else {
			t.Fatalf("reading the membership of %s in %s on %s: %v", userID, roomID, s.name, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never recorded %s for %s in %s (%s)", s.name, want, userID, roomID, last)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAFederatedJoinTeachesBothServersTheRoom joins a user of one server to a room the
// other hosts, and checks the two halves of the result: the host took the join, and the
// joining server can describe the room it joined.
func TestAFederatedJoinTeachesBothServersTheRoom(t *testing.T) {
	cluster := newCluster(t)
	one, two := cluster.one(), cluster.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	roomID := two.createRoom(bob, map[string]any{"preset": "public_chat"})

	got := joinVia(one, alice, roomID, two.name)
	if got.Status != http.StatusOK {
		t.Fatalf("%s could not join %s through %s: %d %s", alice.UserID, roomID, two.name, got.Status, got.Raw)
	}

	// The host accepted the join, so the room it hosts now holds a member from the
	// other server.
	awaitMembership(t, two, roomID, alice.UserID, matrix.MembershipJoin)

	// And the joining server holds the room's state. This is the part a join is for:
	// without it the server has a membership it cannot act on, and its user cannot be
	// told what room they are in.
	state := stateOf(t, one, alice, roomID)
	if len(state) < 5 {
		t.Errorf("%s holds %d state events for %s after joining; the state it was handed did not arrive",
			one.name, len(state), roomID)
	}
	history := one.mustDo(http.StatusOK, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages", alice.Token, nil)
	events, _ := history.Body["chunk"].([]any)
	for _, entry := range events {
		event, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if event["type"] == matrix.TypeCreate {
			t.Errorf("the create event of %s appeared in /messages on %s, although it arrived as state", roomID, one.name)
		}
	}

	// Both servers agree on who is in the room, which is what the rest of federation
	// decides who to talk to.
	if !sameStrings(membersOf(t, one, roomID), membersOf(t, two, roomID)) {
		t.Errorf("the two servers disagree about the members of %s: %v against %v",
			roomID, membersOf(t, one, roomID), membersOf(t, two, roomID))
	}
}

// TestARoomCanBanAUserThatJoinedFromAnotherServer checks that a room's decision about a
// user reaches the server that user lives on, and that the server acts on it.
func TestARoomCanBanAUserThatJoinedFromAnotherServer(t *testing.T) {
	cluster := newCluster(t)
	one, two := cluster.one(), cluster.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	roomID := two.createRoom(bob, map[string]any{"preset": "public_chat"})

	// The room's server is the one that sends the ban, so its outbox has to be running.
	stop := two.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stop()

	got := joinVia(one, alice, roomID, two.name)
	if got.Status != http.StatusOK {
		t.Fatalf("%s could not join %s through %s: %d %s", alice.UserID, roomID, two.name, got.Status, got.Raw)
	}
	awaitMembership(t, two, roomID, alice.UserID, matrix.MembershipJoin)

	// Where the user's client had got to before the room threw them out, so the sync that
	// follows is the delta a real client would see.
	synced := one.mustDo(http.StatusOK, http.MethodGet, "/_matrix/client/v3/sync", alice.Token, nil)
	since, _ := synced.Body["next_batch"].(string)

	two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/ban", bob.Token,
		matrix.Object{"user_id": alice.UserID, "reason": "the room does not want you"})

	// The ban has to arrive on the other server: a ban only one server knows leaves the
	// other one relaying this user's events into the room.
	awaitMembership(t, one, roomID, alice.UserID, matrix.MembershipBan)

	// And the ban has to be acted on: the banned user cannot speak in the room any more,
	// on either server.
	if resp := one.do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/%s", roomID, matrix.NewEventID()),
		alice.Token, matrix.Object{"msgtype": "m.text", "body": "let me back in"}); resp.Status == http.StatusOK {
		t.Errorf("the banned user spoke in the room through %s: %s", one.name, resp.Raw)
	}

	// The user's own client has to be told. A database that knows about the ban and a sync
	// that says nothing are a client still showing a room its user was thrown out of, and
	// the ban event itself is the only answer to "why is this room gone".
	awaitBannedInSync(t, one, alice, roomID, since)

	// A ban is not permanent unless the room says so: an unban and an invite have to reach
	// the other server too, and the user has to be able to come back. This is the half that
	// catches a server refusing a second join because it still holds the room from the first.
	two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/unban", bob.Token,
		matrix.Object{"user_id": alice.UserID})
	awaitMembership(t, one, roomID, alice.UserID, matrix.MembershipLeave)
	two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", bob.Token,
		matrix.Object{"user_id": alice.UserID})
	awaitMembership(t, one, roomID, alice.UserID, matrix.MembershipInvite)

	got = joinVia(one, alice, roomID, two.name)
	if got.Status != http.StatusOK {
		t.Fatalf("%s could not join %s again after the ban: %d %s", alice.UserID, roomID, got.Status, got.Raw)
	}
	awaitMembership(t, one, roomID, alice.UserID, matrix.MembershipJoin)
	awaitMembership(t, two, roomID, alice.UserID, matrix.MembershipJoin)
	if resp := one.do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/%s", roomID, matrix.NewEventID()),
		alice.Token, matrix.Object{"msgtype": "m.text", "body": "let me back in"}); resp.Status != http.StatusOK {
		t.Errorf("the rejoined user could not speak in %s through %s: %d %s", roomID, one.name, resp.Status, resp.Raw)
	}
}

// TestAFederatedLeaveReachesTheRoomsServer checks that a user leaving from the server
// they live on tells the server that hosts the room. A leave that stays on the leaver's
// own server leaves the room holding a member who has gone: the room's server keeps
// relaying itself to a server that stopped caring, and the room's own idea of who is in it
// - which is who federation is sent to next - is wrong.
func TestAFederatedLeaveReachesTheRoomsServer(t *testing.T) {
	cluster := newCluster(t)
	one, two := cluster.one(), cluster.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	roomID := two.createRoom(bob, map[string]any{"preset": "public_chat"})

	if got := joinVia(one, alice, roomID, two.name); got.Status != http.StatusOK {
		t.Fatalf("%s could not join %s through %s: %d %s", alice.UserID, roomID, two.name, got.Status, got.Raw)
	}
	awaitMembership(t, two, roomID, alice.UserID, matrix.MembershipJoin)

	one.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/leave", alice.Token,
		matrix.Object{"reason": "done here"})

	// The room's own server holds the leave, and so does the leaver's: the leave is one
	// event both have to agree on, and the handshake is what makes the room's server sign
	// off on it rather than learn about it later, if at all.
	awaitMembership(t, two, roomID, alice.UserID, matrix.MembershipLeave)
	awaitMembership(t, one, roomID, alice.UserID, matrix.MembershipLeave)
	if members := membersOf(t, two, roomID); !sameStrings(members, []string{bob.UserID}) {
		t.Errorf("%s counts %v as members of %s after %s left", two.name, members, roomID, alice.UserID)
	}
}

// awaitBannedInSync waits for a user's sync to report the room as left, with the ban
// event among the events shown.
func awaitBannedInSync(t *testing.T, s *server, u *session, roomID, since string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		got := s.mustDo(http.StatusOK, http.MethodGet, "/_matrix/client/v3/sync?since="+since, u.Token, nil)
		if banned, _ := roomsContainMembership(got.Body, "leave", roomID, u.UserID, matrix.MembershipBan); banned {
			return
		}
		if next, ok := got.Body["next_batch"].(string); ok {
			since = next
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never synced the ban of %s in %s: %s", s.name, u.UserID, roomID, got.Raw)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// roomsContainMembership looks in one bucket of a sync response for a membership event
// about a user.
func roomsContainMembership(body matrix.Object, bucket, roomID, userID, membership string) (bool, bool) {
	rooms, _ := body["rooms"].(map[string]any)
	entry, _ := rooms[bucket].(map[string]any)
	if entry == nil {
		return false, false
	}
	raw, ok := entry[roomID]
	if !ok {
		return false, false
	}
	room, _ := raw.(map[string]any)
	for _, section := range []string{"state", "timeline"} {
		block, _ := room[section].(map[string]any)
		events, _ := block["events"].([]any)
		for _, entry := range events {
			event, _ := entry.(map[string]any)
			if event["type"] != matrix.TypeMember || event["state_key"] != userID {
				continue
			}
			content, _ := event["content"].(map[string]any)
			if content["membership"] == membership {
				return true, true
			}
		}
	}
	return false, true
}

// membersOf lists the users a server holds as joined in a room.
func membersOf(t *testing.T, s *server, roomID string) []string {
	t.Helper()
	joined, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		t.Fatalf("reading the members of %s on %s: %v", roomID, s.name, err)
	}
	var out []string
	for _, member := range joined {
		out = append(out, member.UserID)
	}
	return out
}

// sameStrings compares two lists of names, which are read out of a database and so are
// not in a defined order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}
