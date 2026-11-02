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

// TestALeaveReachesTheServersThatStayInTheRoom puts three servers in one room, has one
// of the joining servers leave, and checks the room's own server tells the third. The
// leave is made by the server that is going, but the room's server is the one that speaks
// for it afterwards: a leave it accepts and does not relay leaves the rest of the room
// with a member who has gone, still addressing that server as if it were present.
func TestALeaveReachesTheServersThatStayInTheRoom(t *testing.T) {
	cluster := newClusterNamed(t, serverOne, serverTwo, "three.example")
	one, two, three := cluster.one(), cluster.two(), cluster.byName("three.example")
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	carol := three.register("carol", "wonderland")
	roomID := two.createRoom(bob, map[string]any{"preset": "public_chat"})

	// The room's server relays what it accepts, so its outbox has to be running.
	stop := two.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stop()

	for _, s := range []*server{one, three} {
		sess := alice
		if s == three {
			sess = carol
		}
		if got := joinVia(s, sess, roomID, two.name); got.Status != http.StatusOK {
			t.Fatalf("%s could not join %s through %s: %d %s", sess.UserID, roomID, two.name, got.Status, got.Raw)
		}
	}
	awaitMembership(t, two, roomID, alice.UserID, matrix.MembershipJoin)
	awaitMembership(t, three, roomID, alice.UserID, matrix.MembershipJoin)
	awaitMembership(t, three, roomID, carol.UserID, matrix.MembershipJoin)

	one.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/leave", alice.Token,
		matrix.Object{"reason": "done here"})

	awaitMembership(t, two, roomID, alice.UserID, matrix.MembershipLeave)
	awaitMembership(t, three, roomID, alice.UserID, matrix.MembershipLeave)
	if members := membersOf(t, three, roomID); !sameStrings(members, []string{bob.UserID, carol.UserID}) {
		t.Errorf("%s counts %v as members of %s after %s left", three.name, members, roomID, alice.UserID)
	}
}

// TestAnInvitedUserCanRejectAFederatedInvite covers the one membership a room learns from a
// server it has never been part of, answered in the other direction. The invitation is
// shipped to a stranger, and the stranger turns it down: the invited server holds the room
// only because the invite told it about the room, and the rejection has to reach the room's
// own server. Otherwise the room keeps somebody it believes is invited, and the user keeps
// seeing a room they refused.
func TestAnInvitedUserCanRejectAFederatedInvite(t *testing.T) {
	cluster := newClusterNamed(t, serverOne, serverTwo)
	one, two := cluster.one(), cluster.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "rubberduck")
	roomID := one.createRoom(alice, map[string]any{"name": "Turned down"})

	one.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		matrix.Object{"user_id": bob.UserID})
	awaitMembership(t, two, roomID, bob.UserID, matrix.MembershipInvite)

	// The rejection travels back over the leave handshake: the invited server asks the
	// room's server for the shape of a leave, signs it there, and the room's server accepts
	// it into a graph that user never appeared in.
	two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/leave", bob.Token,
		matrix.Object{})
	awaitMembership(t, two, roomID, bob.UserID, matrix.MembershipLeave)
	awaitMembership(t, one, roomID, bob.UserID, matrix.MembershipLeave)

	// The room's own member is told. A rejection the room never hears about is an invitation
	// that still stands, and a client reading the room's members would count bob among them.
	if members := membersOf(t, one, roomID); !sameStrings(members, []string{alice.UserID}) {
		t.Errorf("%s lists the members of %s as %v after %s refused the invite, want only %s",
			one.name, roomID, members, bob.UserID, alice.UserID)
	}
}

// TestARelayedInviteReachesAServerNewToTheRoom invites a user whose server has never
// been in the room, after the room has moved on without them. The invite reaches that
// server as an ordinary event citing prev and auth events it has never been told about,
// and it still has to end up holding the invite: a user who is not told they have been
// invited cannot answer, and a room that invites into the void has no invite at all.
func TestARelayedInviteReachesAServerNewToTheRoom(t *testing.T) {
	cluster := newClusterNamed(t, serverOne, serverTwo, "three.example")
	two, three := cluster.two(), cluster.byName("three.example")
	alice := two.register("alice", "wonderland")
	bob := three.register("bob", "wonderland")
	roomID := two.createRoom(alice, map[string]any{"preset": "private_chat"})

	stop := two.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)
	defer stop()

	// The room moves on before the invite is sent, so the invite the invited server is
	// handed builds on events that server has never been given. It will ask for them, and
	// the asking is part of the test.
	for i := 0; i < 3; i++ {
		two.mustDo(http.StatusOK, http.MethodPut,
			fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/%s", roomID, matrix.NewEventID()),
			alice.Token, matrix.Object{"msgtype": "m.text", "body": "before you arrived"})
	}

	two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		matrix.Object{"user_id": bob.UserID})

	awaitMembership(t, three, roomID, bob.UserID, matrix.MembershipInvite)

	// The invited server holds the room's identity, not just the membership: the invite
	// shipped the state it is authorised by, and a client has to be able to tell its user
	// which room they have been invited to. The power levels are deliberately not among it:
	// the specification's stripped state names the room, how it is entered, and the two
	// memberships the invitation is between, and a room's whole rule book is not something a
	// stranger is handed before they have done anything.
	state := stateOf(t, three, bob, roomID)
	types := map[string]bool{}
	for _, raw := range state {
		event, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		types[fmt.Sprint(event["type"])] = true
	}
	for _, want := range []string{matrix.TypeCreate, matrix.TypeJoinRules, matrix.TypeMember} {
		if !types[want] {
			t.Errorf("%s holds no %s for %s, which the invite shipped: %v", three.name, want, roomID, types)
		}
	}

	// And the user is told. An invite that lives in a database is an invite nobody can
	// accept, and the sync bucket it belongs in is invite rather than join.
	deadline := time.Now().Add(5 * time.Second)
	for {
		synced := three.mustDo(http.StatusOK, http.MethodGet, "/_matrix/client/v3/sync", bob.Token, nil)
		if invited, _ := roomsContainMembership(synced.Body, "invite", roomID, bob.UserID, matrix.MembershipInvite); invited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never synced the invite of %s to %s: %s", three.name, bob.UserID, roomID, synced.Raw)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Bob accepts. The join is built on the shape two.example hands over, and that shape
	// carries what the join replaces: a client shown a membership of "join" cannot tell an
	// accepted invitation from a knock that was let in, and only the server holding the room
	// knows which it was.
	joinVia(three, bob, roomID, two.name)
	awaitMembership(t, two, roomID, bob.UserID, matrix.MembershipJoin)
	// Each side is read by one of its own users: a stranger asking for another server's
	// history is refused, and that refusal is correct rather than a bug to route around.
	for _, side := range []struct {
		s    *server
		sess *session
	}{{two, alice}, {three, bob}} {
		server, sess := side.s, side.sess
		page := server.mustDo(http.StatusOK, http.MethodGet,
			"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=50", sess.Token, nil)
		chunk, _ := page.Body["chunk"].([]any)
		found := false
		for _, entry := range chunk {
			event, _ := entry.(map[string]any)
			if event["type"] != matrix.TypeMember || event["state_key"] != bob.UserID {
				continue
			}
			content, _ := event["content"].(map[string]any)
			if content["membership"] != matrix.MembershipJoin {
				continue
			}
			unsigned, _ := event["unsigned"].(map[string]any)
			previous, _ := unsigned["prev_content"].(map[string]any)
			if previous["membership"] != matrix.MembershipInvite {
				t.Errorf("%s records %s's join without the invite it replaced: %v", server.name, bob.UserID, unsigned)
			}
			found = true
			break
		}
		if !found {
			t.Errorf("%s holds no join event for %s in the history of %s", server.name, bob.UserID, roomID)
		}
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
	for _, section := range []string{"state", "timeline", "invite_state"} {
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
