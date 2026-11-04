package compat

import (
	"net/http"
	"testing"
)

// TestAKnockRestrictedRoomAdmitsItsOwnWithoutAKnock is the room that answers two ways at once. A
// knock_restricted room takes people the allow list admits straight in, and everybody else has to
// ask. Reading it as a knock room first makes the admitting half a wait: a member of the space the
// room sits in is told to knock and sit down, which is what a restricted room is not supposed to do.
// Reading it as a restricted room only would shut the door on the person who has nothing to stand on
// and nowhere else to ask.
func TestAKnockRestrictedRoomAdmitsItsOwnWithoutAKnock(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	carol := server.Register("carol", "hatter")

	allowed := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token,
		map[string]any{"preset": "public_chat", "name": "the space"}).String("room_id")
	roomID := server.Do(http.MethodPost, "/_matrix/client/v3/createRoom", alice.Token,
		map[string]any{"preset": "private_chat", "name": "the room in the space", "room_version": "11"}).String("room_id")
	if set := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules", alice.Token,
		map[string]any{
			"join_rule": "knock_restricted",
			"allow":     []map[string]any{{"type": "m.room_membership", "room_id": allowed}},
		}); set.Status != http.StatusOK {
		t.Fatalf("opening the room to its space: %d %s", set.Status, set.Raw)
	}

	// Nobody who belongs to nothing walks in. The knock is the way in for them, and the room says
	// so rather than staying silent about why it turned them away.
	if join := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, map[string]any{}); join.Status != http.StatusForbidden {
		t.Fatalf("bob joining a room he belongs to nothing in: status %d (body %s), want 403", join.Status, join.Raw)
	}
	// The same door through the state endpoint, which is the other way a client writes a
	// membership: the guard is the room's rule either way.
	if write := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+bob.UserID,
		bob.Token, map[string]any{"membership": "join"}); write.Status != http.StatusForbidden {
		t.Fatalf("bob writing his own join through the state endpoint: status %d (body %s), want 403",
			write.Status, write.Raw)
	}

	// Bob belongs to the space now. He does not knock: the room reads its allow list, sees him
	// there, and lets him in.
	if join := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+allowed, bob.Token, map[string]any{}); join.Status != http.StatusOK {
		t.Fatalf("bob joining the space: %d %s", join.Status, join.Raw)
	}
	if join := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, map[string]any{}); join.Status != http.StatusOK {
		t.Fatalf("bob joining the room through the space he belongs to: status %d (body %s), want 200", join.Status, join.Raw)
	}
	members := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/members", bob.Token, nil)
	if _, ok := members.Body["chunk"].([]any)[0].(map[string]any); !ok {
		t.Fatalf("the room's members cannot be read: %s", members.Raw)
	}
	var joined bool
	for _, item := range members.Body["chunk"].([]any) {
		event, _ := item.(map[string]any)
		if event["state_key"] == bob.UserID {
			joined = true
		}
	}
	if !joined {
		t.Errorf("bob is not among the room's members after the join the allow list admitted: %s", members.Raw)
	}

	// The other half stays open too. Carol belongs to nothing and asks.
	if knock := server.Do(http.MethodPost, "/_matrix/client/v3/knock/"+roomID, carol.Token,
		map[string]any{"reason": "i work with bob"}); knock.Status != http.StatusOK {
		t.Fatalf("carol knocking on a knock_restricted room: status %d (body %s), want 200", knock.Status, knock.Raw)
	}
	// Knocking is her way of being seen; it is not a door. The room now has her question in its
	// state and has not answered it, and a join reads the question as the answer.
	knockJoin := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, carol.Token, map[string]any{})
	if knockJoin.Status != http.StatusForbidden {
		t.Fatalf("carol joining on her unanswered knock: status %d (body %s), want 403 until the room answers",
			knockJoin.Status, knockJoin.Raw)
	}

	// The answer, and the join it permits.
	if invite := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": carol.UserID}); invite.Status != http.StatusOK {
		t.Fatalf("alice letting carol in: %d %s", invite.Status, invite.Raw)
	}
	if join := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, carol.Token, map[string]any{}); join.Status != http.StatusOK {
		t.Fatalf("carol joining after the room answered her knock: status %d (body %s), want 200", join.Status, join.Raw)
	}
}

// TestARestrictedRoomsSummaryNamesTheRoomsThatAdmit is the same rule read from the other end: a
// room describes how it is entered. A room that admits people through another room says which, so
// a client can tell a reader why the door is open to them rather than making them try it; a room
// that admits people that way does not carry the field at all, because an empty list would read as
// a room with an empty allow list rather than a room with no such rule.
func TestARestrictedRoomsSummaryNamesTheRoomsThatAdmit(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	space := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "the space", "type": "m.space"})
	room := server.CreateRoom(alice, map[string]any{
		"preset": "public_chat", "name": "the room in the space", "room_version": "8",
	})
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+room+"/state/m.room.join_rules", alice.Token,
		map[string]any{
			"join_rule": "restricted",
			"allow":     []map[string]any{{"type": "m.room_membership", "room_id": space}},
		}, http.StatusOK)

	summary := server.MustDo(http.MethodGet, "/_matrix/client/v1/room_summary/"+room, alice.Token, nil, http.StatusOK)
	if got, _ := summary.Body["join_rule"].(string); got != "restricted" {
		t.Errorf("the summary reports join_rule %q, want restricted: %s", got, summary.Raw)
	}
	allowed, _ := summary.Body["allowed_room_ids"].([]any)
	if len(allowed) != 1 || allowed[0] != space {
		t.Errorf("the summary names %v as the rooms that admit a join, want just %s: %s", allowed, space, summary.Raw)
	}

	private := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "the closed room"})
	other := server.MustDo(http.MethodGet, "/_matrix/client/v1/room_summary/"+private, alice.Token, nil, http.StatusOK)
	if _, found := other.Body["allowed_room_ids"]; found {
		t.Errorf("a room with no allow list carries allowed_room_ids anyway: %s", other.Raw)
	}
}

// TestAKnockIsNotADoor is the same rule in a plain knock room. Knocking is how an outsider asks;
// the room's answer is an invite. Accepting the join on the strength of the knock is a bug a server
// cannot see in itself: it looks like working, and the only room it is true in is this one.
func TestAKnockIsNotADoor(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "the locked room"})
	if set := server.Do(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules", alice.Token,
		map[string]any{"join_rule": "knock"}); set.Status != http.StatusOK {
		t.Fatalf("opening the room to knocking: %d %s", set.Status, set.Raw)
	}
	if knock := server.Do(http.MethodPost, "/_matrix/client/v3/knock/"+roomID, bob.Token,
		map[string]any{"reason": "let me in"}); knock.Status != http.StatusOK {
		t.Fatalf("bob knocking: %d %s", knock.Status, knock.Raw)
	}
	// The knock is in the room's state, which is what makes this a decision rather than a
	// missing fact: the room knows bob asked, and has not said yes.
	if join := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, map[string]any{}); join.Status != http.StatusForbidden {
		t.Fatalf("bob joining on his unanswered knock: status %d (body %s), want 403", join.Status, join.Raw)
	}
	if invite := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}); invite.Status != http.StatusOK {
		t.Fatalf("alice inviting bob: %d %s", invite.Status, invite.Raw)
	}
	if join := server.Do(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, map[string]any{}); join.Status != http.StatusOK {
		t.Fatalf("bob joining once invited: status %d (body %s), want 200", join.Status, join.Raw)
	}
}
