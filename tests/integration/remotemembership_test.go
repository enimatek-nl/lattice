package integration

import (
	"net/http"
	"testing"
	"time"
)

// A room whose moderator lives here has to be able to remove a member who lives elsewhere.
//
// The room's own authorisation rules decide that: in every room version that has power levels,
// a member event about *another* user is judged by the sender's kick or ban power, and the
// signature that carries the act is the sender's server's. So the event is written here and
// signed here, and what the other server is owed is the event itself. That is the whole of the
// difference between this and a join: a join speaks for the person doing it and cannot be signed
// by a stranger's homeserver, and a kick speaks for the room.
//
// The fan-out has one subtlety the membership rules create. The servers an event reaches are the
// servers holding a joined member, and a ban or a rescinded invitation is usually about a server
// that holds nobody joined here — so a membership event must also go to the server of the user it
// names, or the room keeps two copies and only one of them knows the person is out.

// TestLocalModeratorKicksARemoteMember: the kick answers 200 rather than 501, both copies of the
// room agree the user is out, and the user can no longer speak in the room from their own server.
func TestLocalModeratorKicksARemoteMember(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "moderator", "pw-moderator")
	bea := "@bea:" + hs2.name
	beaToken := hs2.registerUser(t, "bea", "pw-bea")

	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "name": "a room with a stranger in it",
			"invite": []any{bea}}, alice)
	roomID, _ := created["room_id"].(string)
	if roomID == "" {
		t.Fatalf("the room was not created: %v", created)
	}
	if status, joined := hs2.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/join/"+roomID+"?server_name="+hs1.name, map[string]any{}, beaToken); status != http.StatusOK {
		t.Fatalf("%s could not join %s on %s: %d %v", hs2.name, roomID, hs1.name, status, joined)
	}

	if status, answer := hs1.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/rooms/"+roomID+"/kick", map[string]any{"user_id": bea}, alice); status != http.StatusOK {
		t.Fatalf("a local moderator kicking %s on %s: %d %v", bea, hs1.name, status, answer)
	}

	// The resident server says so.
	if got := membershipContent(t, hs1, roomID, bea, alice); got != "leave" {
		t.Errorf("%s holds %s as %q after the kick, wanting leave", hs1.name, bea, got)
	}
	// And the kicked user's server was told, rather than holding an invite or a join that the
	// room no longer believes in.
	if !waitFor(t, 15*time.Second, func() bool {
		return membershipContent(t, hs2, roomID, bea, beaToken) == "leave"
	}) {
		t.Errorf("%s never learned that %s was kicked out of %s; it holds them as %q",
			hs2.name, bea, roomID, membershipContent(t, hs2, roomID, bea, beaToken))
	}

	// The room is closed to them from their own side as well, which is the point of the kick.
	if status, answer := hs2.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/after-kick",
		map[string]any{"msgtype": "m.text", "body": "still here"}, beaToken); status == http.StatusOK {
		t.Errorf("%s accepted a message from a kicked user: %v", hs2.name, answer)
	}
}

// TestLocalModeratorBansAnUnjoinedRemoteMember covers the fan-out half on its own: the user was
// invited and never joined, so the target's homeserver holds no joined member of this room and
// would never be reached by the ordinary membership rule. A ban nobody else hears about is a ban
// the target's own server will happily undo by letting them join again.
func TestLocalModeratorBansAnUnjoinedRemoteMember(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "moderator", "pw-ban-mod")
	bea := "@bea:" + hs2.name
	beaToken := hs2.registerUser(t, "bea", "pw-ban-bea")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "name": "a door that closes", "invite": []any{bea}}, alice)
	roomID, _ := created["room_id"].(string)
	if roomID == "" {
		t.Fatalf("the room was not created: %v", created)
	}

	if status, answer := hs1.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/rooms/"+roomID+"/ban", map[string]any{"user_id": bea, "reason": "noise"}, alice); status != http.StatusOK {
		t.Fatalf("a local moderator banning %s: %d %v", bea, status, answer)
	}
	if got := membershipContent(t, hs1, roomID, bea, alice); got != "ban" {
		t.Errorf("%s holds %s as %q after the ban, wanting ban", hs1.name, bea, got)
	}
	if !waitFor(t, 15*time.Second, func() bool {
		return membershipContent(t, hs2, roomID, bea, beaToken) == "ban"
	}) {
		t.Errorf("%s was never told about the ban on %s: it holds them as %q",
			hs2.name, bea, membershipContent(t, hs2, roomID, bea, beaToken))
	}

	// Un-banning is a leave event for that user, and it has to reach the same server.
	if status, answer := hs1.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/rooms/"+roomID+"/unban", map[string]any{"user_id": bea}, alice); status != http.StatusOK {
		t.Fatalf("a local moderator unbanning %s: %d %v", bea, status, answer)
	}
	if !waitFor(t, 15*time.Second, func() bool {
		return membershipContent(t, hs2, roomID, bea, beaToken) == "leave"
	}) {
		t.Errorf("%s still holds the ban on %s after the unban: %q",
			hs2.name, bea, membershipContent(t, hs2, roomID, bea, beaToken))
	}
}

// TestKickWithoutThePowerIsARefusalNotAMissingFeature: the answer to a kick this server would have
// no right to write is 403 from the room's rules. Before this work a remote target stopped the
// endpoint early and answered 501, which told an administrator that the feature was missing when
// the real answer was that the room said no.
func TestKickWithoutThePowerIsARefusalNotAMissingFeature(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "owner", "pw-owner")
	caroToken := hs1.registerUser(t, "caro", "pw-caro")
	caro := "@caro:" + hs1.name
	bea := "@bea:" + hs2.name
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "name": "levels matter", "invite": []any{bea, caro}}, alice)
	roomID, _ := created["room_id"].(string)
	if roomID == "" {
		t.Fatalf("the room was not created: %v", created)
	}

	if status, answer := hs1.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/rooms/"+roomID+"/kick", map[string]any{"user_id": bea}, caroToken); status == http.StatusNotImplemented {
		t.Fatalf("a user without the kick power was told the endpoint does not exist: %d %v", status, answer)
	} else if status != http.StatusForbidden {
		t.Errorf("kicking without the power answered %d, wanting 403: %v", status, answer)
	}
}

// membershipContent reads the membership this server currently holds for one user, as the state
// endpoint documents it: the content of the member event. A server that has never heard of the
// user answers with an empty string.
func membershipContent(t *testing.T, node *fedNode, roomID, userID, token string) string {
	t.Helper()
	status, body := node.clientDo(t, http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+userID, nil, token)
	if status != http.StatusOK {
		return ""
	}
	membership, _ := body["membership"].(string)
	return membership
}
