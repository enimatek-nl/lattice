package compat

import (
	"net/http"
	"testing"
)

// The tests here pin what changed when the room's rules began to be read once, by the same list,
// for events this server writes and events a peer sends it (see [Service.judgeLocalEvent] in the
// service). Each one is a case where the endpoint's own guard and the room's rules had drifted
// apart, and the drift was only ever visible from outside: this server accepted what it wrote, and
// every peer holding the same room refused the identical event.

// TestAModeratorCannotBanOrKickSomebodyWhoOutranksThem is the rank rule the specification states
// beside the power levels: a ban needs the ban level *and* a target below the sender. A moderator
// holding 50 could ban a member holding 100, because the guard asked only about the level and not
// about the rank. An event of that kind is refused by the room's other servers, so the ban was
// real only on this one.
func TestAModeratorCannotBanOrKickSomebodyWhoOutranksThem(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	carol := server.Register("carol", "hatter")

	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "the ranked room"})
	for _, who := range []*Session{bob, carol} {
		server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, who.Token, map[string]any{}, http.StatusOK)
	}
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels", alice.Token,
		map[string]any{"users": map[string]any{alice.UserID: 100, bob.UserID: 50, carol.UserID: 100}},
		http.StatusOK)

	ban := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/ban", bob.Token,
		map[string]any{"user_id": carol.UserID, "reason": "out"})
	if ban.Status != http.StatusForbidden {
		t.Fatalf("a moderator with 50 banned a member with 100: status %d (body %s), want 403",
			ban.Status, ban.Raw)
	}
	kick := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/kick", bob.Token,
		map[string]any{"user_id": carol.UserID, "reason": "out"})
	if kick.Status != http.StatusForbidden {
		t.Fatalf("the same moderator kicked that member: status %d (body %s), want 403", kick.Status, kick.Raw)
	}
	// The rank rule asks about the room's levels, not about the endpoint: a moderator may still
	// act on somebody who sits below them. dave joined at level 20.
	dave := server.Register("dave", "card")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, dave.Token, map[string]any{}, http.StatusOK)
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels", alice.Token,
		map[string]any{"users": map[string]any{alice.UserID: 100, bob.UserID: 50, carol.UserID: 100, dave.UserID: 20}},
		http.StatusOK)
	kicked := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/kick", bob.Token,
		map[string]any{"user_id": dave.UserID, "reason": "out"})
	if kicked.Status != http.StatusOK {
		t.Fatalf("the moderator kicking a member who sits below them: status %d (body %s), want 200",
			kicked.Status, kicked.Raw)
	}
}

// TestAServerAdministratorIsNotAboveARoomsJoinRule records an extension this server used to have
// and no longer does. An administrator of the *server* could join a room whose join rule is invite
// without an invite, because the guard beside the join endpoint asked whether the user ran the
// server. The room's rules do not know about server administrators, and neither do the room's other
// servers: the membership event this server signed for that join is one every other server in the
// room refuses, so the membership existed on one server only. Administrators can still read rooms
// they are not in, which is a question this server answers for itself; becoming a member of a room
// that excludes them is not.
func TestAServerAdministratorIsNotAboveARoomsJoinRule(t *testing.T) {
	server := NewServer(t)
	// The first account created on a server is its administrator.
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	roomID := server.CreateRoom(bob, map[string]any{"preset": "private_chat", "name": "bob's room"})
	joined := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, alice.Token, map[string]any{})
	if joined.Status != http.StatusForbidden {
		t.Fatalf("the server's administrator joined a room nobody invited them to: status %d (body %s), want 403",
			joined.Status, joined.Raw)
	}
	// The invitation, which is the answer the room gives, works for the same user.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", bob.Token,
		map[string]any{"user_id": alice.UserID}, http.StatusOK)
	if again := server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, alice.Token,
		map[string]any{}, http.StatusOK); again.String("room_id") != roomID {
		t.Errorf("the administrator joining once invited: %s", again.Raw)
	}
}

// TestARoomsRulesDecideAMembershipWrittenThroughTheStateEndpoint puts a membership through the state
// endpoint, which is the door this server used to guard by hand. A room's moderator can write a
// membership there - it is how some clients kick and how some rooms are set up - but writing
// somebody else's join is not a moderator's act in any room, and the refusal comes from the rule
// rather than from the shape of the request.
func TestARoomsRulesDecideAMembershipWrittenThroughTheStateEndpoint(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "open room"})
	walked := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+bob.UserID,
		alice.Token, map[string]any{"membership": "join"})
	if walked.Status != http.StatusForbidden {
		t.Fatalf("the moderator wrote bob's join for him: status %d (body %s), want 403", walked.Status, walked.Raw)
	}
	// Bob's own join of the same room is admitted by the same rules read the same way.
	if join := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, map[string]any{}); join.Status != http.StatusOK {
		t.Fatalf("bob joining the public room himself: status %d (body %s), want 200", join.Status, join.Raw)
	}
	// And a moderator may still write a membership the room hands them: an invite for bob's
	// substitute, sent by a hand that holds the invite level.
	carol := server.Register("carol", "hatter")
	if invite := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+carol.UserID,
		alice.Token, map[string]any{"membership": "invite"}); invite.Status != http.StatusOK {
		t.Fatalf("the moderator inviting carol through the state endpoint: status %d (body %s), want 200",
			invite.Status, invite.Raw)
	}
}

// TestAMemberWithoutPowerCanRedactTheirOwnMessage marks the line the other way. A room's rules say
// who may send an event of a type; the redaction level is this server's knob for letting a member
// take back what they wrote, and it is not part of the rules a peer checks. So the level applies to
// somebody else's message and not to their own, and the difference is kept here rather than in the
// rule list, because a peer refusing a member's redaction of their own message would be wrong.
func TestAMemberWithoutPowerCanRedactTheirOwnMessage(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "open room"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, map[string]any{}, http.StatusOK)

	own := server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/bob-1",
		bob.Token, map[string]any{"msgtype": "m.text", "body": "written by a member with no power"}, http.StatusOK)
	if redacted := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/redact/"+
		own.String("event_id"), bob.Token, map[string]any{"reason": "mine"}); redacted.Status != http.StatusOK {
		t.Errorf("bob redacting his own message: status %d (body %s), want 200", redacted.Status, redacted.Raw)
	}
	someone := server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/alice-1",
		alice.Token, map[string]any{"msgtype": "m.text", "body": "written by the creator"}, http.StatusOK)
	if refused := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/redact/"+
		someone.String("event_id"), bob.Token, map[string]any{"reason": "not mine"}); refused.Status != http.StatusForbidden {
		t.Errorf("bob redacting the creator's message: status %d (body %s), want 403", refused.Status, refused.Raw)
	}
}
