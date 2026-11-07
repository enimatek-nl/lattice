package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Leaving and knocking are the join handshake pointed at a room the asker is not in. These tests
// drive both halves from the asking side, and the thing worth watching is which server decides
// what: the resident server describes the change (the room's forward extremities, its authorising
// state, its depth — none of which a server outside the room can know) and the asker's server signs
// it, because it speaks for the user the event is about. Neither half can carry the other's
// authority, and the assertions below are written to fail if one server ever helps itself to the
// other's.
//
// The wrong-membership case is asserted as 400 rather than 403 on purpose. The endpoints are
// distinguished only by what they accept, so handing `/send_leave` a join is a request that does not
// mean what its URL says, not a permission the room withheld — and a server that answers 403 there is
// inviting the peer to look for a friendlier resident server in the same room.

// makeMembershipTemplate asks the resident server what a leave or a knock by a user on the asking
// server would look like, and returns the template with the room version the resident named.
func makeMembershipTemplate(t *testing.T, asker, resident *fedNode, roomID, userID, membership string) (map[string]any, string) {
	t.Helper()

	status, body := asker.signedRequest(t, resident, http.MethodGet,
		"/_matrix/federation/v1/make_"+membership+"/"+roomID+"/"+userID, nil)
	if status != http.StatusOK {
		t.Fatalf("%s refused to template a %s: %d %s", resident.name, membership, status, body)
	}
	var offer struct {
		Event           map[string]any `json:"event"`
		MembershipEvent map[string]any `json:"membership_event"`
		RoomVersion     string         `json:"room_version"`
	}
	if err := json.Unmarshal(body, &offer); err != nil {
		t.Fatalf("%s answered the %s template with something unreadable: %s", resident.name, membership, body)
	}
	if offer.Event == nil || offer.MembershipEvent == nil {
		t.Fatalf("%s answered a %s template under one field name only: %s", resident.name, membership, body)
	}
	// The two spellings in the answer have to be the same event, not two answers.
	signed, other := eventJSON(t, offer.Event), eventJSON(t, offer.MembershipEvent)
	if string(signed) != string(other) {
		t.Errorf("%s answered `event` and `membership_event` with different events:\n  %s\n  %s",
			resident.name, signed, other)
	}
	template := offer.Event
	if offer.RoomVersion == "" {
		t.Fatalf("%s answered a %s template without naming the room's version: %s", resident.name, membership, body)
	}
	// A template is a description of a membership change, not the change itself: it speaks for the
	// user on the asking server, and only that server's signature can make it real.
	if template["type"] != "m.room.member" || template["room_id"] != roomID ||
		template["sender"] != userID || template["state_key"] != userID {
		t.Fatalf("%s offered a %s template that is not this user's: %v", resident.name, membership, template)
	}
	content, _ := template["content"].(map[string]any)
	if content["membership"] != membership {
		t.Fatalf("%s offered a %s template with membership %v", resident.name, membership, content["membership"])
	}
	if _, present := template["signatures"]; present {
		t.Errorf("%s signed a %s it does not speak for: %v", resident.name, membership, template)
	}
	// The fields the asking server cannot invent for a room it does not hold.
	if len(events.ReferencedIDs(template, "prev_events")) == 0 {
		t.Errorf("%s offered a %s template that cites no history: %v", resident.name, membership, template)
	}
	if _, present := template["depth"]; !present {
		t.Errorf("%s offered a %s template with no depth: %v", resident.name, membership, template)
	}
	return template, offer.RoomVersion
}

// signMembership turns a template into an event the asking server has signed for its own user, and
// returns it with the event ID its content names.
func signMembership(t *testing.T, asker *fedNode, roomVersion string, template map[string]any) (map[string]any, string) {
	t.Helper()
	rules := roomversion.MustGet(roomVersion)
	template["origin"] = asker.name
	if err := events.Seal(asker.name, rules, template, asker.app.Keys); err != nil {
		t.Fatalf("signing the membership event: %v", err)
	}
	eventID, err := events.EventID(rules, template)
	if err != nil {
		t.Fatalf("the membership event's ID: %v", err)
	}
	template["event_id"] = eventID
	return template, eventID
}

// TestRemoteUserLeavesARoomHeldElsewhere is the leave handshake end to end: the user's own server
// asks, signs, and hands back; the server holding the room accepts the departure into its copy of
// the history.
func TestRemoteUserLeavesARoomHeldElsewhere(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "host", "pw-host")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "open doors"}, alice)
	roomID, _ := created["room_id"].(string)
	bea := "@bea:" + hs2.name
	hs2.registerUser(t, "bea", "pw-bea")

	joinHandshake(t, hs2, hs1, roomID, bea)
	if got := stateMembershipOf(t, hs1, roomID, bea, alice); got != "join" {
		t.Fatalf("membership after the join handshake = %q, want join", got)
	}

	template, roomVersion := makeMembershipTemplate(t, hs2, hs1, roomID, bea, "leave")
	event, eventID := signMembership(t, hs2, roomVersion, template)

	status, body := hs2.signedRequest(t, hs1, http.MethodPut,
		"/_matrix/federation/v2/send_leave/"+roomID+"/"+eventID, eventJSON(t, event))
	if status != http.StatusOK {
		t.Fatalf("%s refused the signed leave: %d %s", hs1.name, status, body)
	}
	if got := stateMembershipOf(t, hs1, roomID, bea, alice); got != "leave" {
		t.Fatalf("membership after the leave handshake = %q, want leave", got)
	}

	// What is *not* checked here is hs2's own copy of the room. This handshake was driven by hand
	// from the asking side, so hs2 asked and signed but never stored what it was told: the outbound
	// path that does that has its own tests (TestJoiningARoomHeldElsewhere). Mixing the two would
	// make a failure here indistinguishable from a failure of the handshake being tested.
}

// TestRemoteUserRejectsAnInviteByLeaving covers the case where the asking server holds no copy of
// the room at all — which is the reason `/make_leave` cannot be skipped.
func TestRemoteUserRejectsAnInviteByLeaving(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "host", "pw-host")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "name": "closed doors"}, alice)
	roomID, _ := created["room_id"].(string)
	bea := "@bea:" + hs2.name
	hs2.registerUser(t, "bea", "pw-bea")

	if status, body := hs1.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/rooms/"+roomID+"/invite", map[string]any{"user_id": bea}, alice); status != http.StatusOK {
		t.Fatalf("%s could not invite %s: %d %s", hs1.name, bea, status, body)
	}

	// hs2 has never seen this room, so it cannot know what the rejection should cite.
	template, roomVersion := makeMembershipTemplate(t, hs2, hs1, roomID, bea, "leave")
	event, eventID := signMembership(t, hs2, roomVersion, template)
	status, body := hs2.signedRequest(t, hs1, http.MethodPut,
		"/_matrix/federation/v1/send_leave/"+roomID+"/"+eventID, eventJSON(t, event))
	if status != http.StatusOK {
		t.Fatalf("%s refused the v1 send_leave: %d %s", hs1.name, status, body)
	}
	if got := stateMembershipOf(t, hs1, roomID, bea, alice); got != "leave" {
		t.Fatalf("membership after rejecting the invite = %q, want leave", got)
	}
}

// TestRemoteUserKnocksOnARoomHeldElsewhere is the knock handshake: the room's answer is not only
// acceptance but a description of the room, so the knocker knows what it is waiting on.
func TestRemoteUserKnocksOnARoomHeldElsewhere(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "host", "pw-host")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "name": "doors ajar", "room_version": "10"}, alice)
	roomID, _ := created["room_id"].(string)
	version, _ := created["room_version"].(string)
	if version != "10" {
		t.Fatalf("%s created a room version %q; this test needs one that permits knocking", hs1.name, version)
	}
	if status, body := hs1.doWithStatus(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules",
		map[string]any{"join_rule": "knock"}, alice); status != http.StatusOK {
		t.Fatalf("%s could not set the join rule to knock: %d %s", hs1.name, status, body)
	}
	bea := "@bea:" + hs2.name
	_ = hs2.registerUser(t, "bea", "pw-bea")

	template, roomVersion := makeMembershipTemplate(t, hs2, hs1, roomID, bea, "knock")
	if roomVersion != "10" {
		t.Errorf("%s answered a knock template for room version %s; the room is version 10", hs1.name, roomVersion)
	}
	event, eventID := signMembership(t, hs2, roomVersion, template)

	status, body := hs2.signedRequest(t, hs1, http.MethodPut,
		"/_matrix/federation/v1/send_knock/"+roomID+"/"+eventID, eventJSON(t, event))
	if status != http.StatusOK {
		t.Fatalf("%s refused the signed knock: %d %s", hs1.name, status, body)
	}

	// `knock_room_state` is the part of the room a knock is allowed to see. It has to identify the
	// room — the create event is what a knocker recognises a room by — and it must not be the room's
	// membership list, which is precisely what knocking does not grant.
	var answer struct {
		KnockRoomState []map[string]any `json:"knock_room_state"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("%s answered the knock with something unreadable: %s", hs1.name, body)
	}
	if len(answer.KnockRoomState) == 0 {
		t.Fatalf("%s answered a knock with no room state: %s", hs1.name, body)
	}
	sawCreate, sawName, sawMember := false, false, false
	for _, pdu := range answer.KnockRoomState {
		switch pdu["type"] {
		case "m.room.create":
			sawCreate = true
		case "m.room.name":
			sawName = true
		case "m.room.member":
			sawMember = true
		}
		if _, present := pdu["origin_server_ts"]; !present {
			t.Errorf("%s handed over a stripped state event, which cannot be checked: %v", hs1.name, pdu["type"])
		}
	}
	if !sawCreate || !sawName {
		t.Errorf("%s's knock_room_state is missing m.room.create or m.room.name: create=%v name=%v",
			hs1.name, sawCreate, sawName)
	}
	if sawMember {
		t.Errorf("%s answered a knock with the room's membership events", hs1.name)
	}

	if got := stateMembershipOf(t, hs1, roomID, bea, alice); got != "knock" {
		t.Fatalf("membership after the knock handshake = %q, want knock", got)
	}

	// What is *not* checked here is hs2's copy of the room. The handshake was driven by hand from
	// the asking side, so hs2 asked and signed but never stored what it was handed back; the outbound
	// path that does that is a separate subject with its own tests (TestJoiningARoomHeldElsewhere),
	// and pulling it in here would blur the failure of one with the failure of the other.
}

// TestAKnockIsRefusedWhereTheRoomDoesNotTakeKnocks checks that the first half of the handshake does
// not hand out a template for a membership the room forbids. A server that gets one anyway has done
// the signing for nothing, and a server that hands one out is describing a membership change it
// would refuse a moment later.
func TestAKnockIsRefusedWhereTheRoomDoesNotTakeKnocks(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "host", "pw-host")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "room_version": "10"}, alice)
	roomID, _ := created["room_id"].(string)
	bea := "@bea:" + hs2.name
	hs2.registerUser(t, "bea", "pw-bea")

	status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/make_knock/"+roomID+"/"+bea, nil)
	if status != http.StatusForbidden {
		t.Fatalf("make_knock at an invite-only room: status %d, want 403 (body %s)", status, body)
	}
	if code := errCode(body); code != "M_FORBIDDEN" {
		t.Errorf("make_knock at an invite-only room answered %s, want M_FORBIDDEN (body %s)", code, body)
	}
}

// TestSendMembershipEndpointsRefuseTheWrongMembership is the check that the endpoints only accept
// what they are for. Every event below is signed by the server that speaks for its user, so nothing
// except the membership the event claims is wrong about it — which is what makes the answer have to
// be 400 rather than a permission failure.
func TestSendMembershipEndpointsRefuseTheWrongMembership(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "host", "pw-host")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, alice)
	roomID, _ := created["room_id"].(string)
	bea := "@bea:" + hs2.name
	hs2.registerUser(t, "bea", "pw-bea")
	// A leave template only exists for a user the room holds, so the events below are built by a
	// user who is genuinely in the room. Then the only thing wrong with each of them is the
	// membership it claims.
	joinHandshake(t, hs2, hs1, roomID, bea)

	// Two events, both properly signed by hs2 for its own user, differing only in the membership
	// they claim.
	template, version := makeMembershipTemplate(t, hs2, hs1, roomID, bea, "leave")
	leave, leaveID := signMembership(t, hs2, version, template)

	// The same event with its membership rewritten to `join`: exactly what a server would have to
	// forge to put its user into a room through an endpoint that never authorised a join there.
	forged := roundTripEvent(t, leave)
	forgedContent, _ := forged["content"].(map[string]any)
	forgedContent["membership"] = "join"
	rules := roomversion.MustGet(version)
	if err := events.Seal(hs2.name, rules, forged, hs2.app.Keys); err != nil {
		t.Fatalf("re-signing the forged event: %v", err)
	}
	forgedID, err := events.EventID(rules, forged)
	if err != nil {
		t.Fatalf("the forged event's ID: %v", err)
	}
	if forgedID == leaveID {
		t.Fatalf("rewriting the membership did not change the event's name: %s", forgedID)
	}

	cases := []struct {
		path    string
		event   map[string]any
		eventID string
	}{
		{"/_matrix/federation/v1/send_leave/" + roomID + "/" + forgedID, forged, forgedID},
		{"/_matrix/federation/v2/send_leave/" + roomID + "/" + forgedID, forged, forgedID},
		{"/_matrix/federation/v1/send_knock/" + roomID + "/" + leaveID, leave, leaveID},
		{"/_matrix/federation/v2/send_knock/" + roomID + "/" + leaveID, leave, leaveID},
		{"/_matrix/federation/v1/send_knock/" + roomID + "/" + forgedID, forged, forgedID},
		{"/_matrix/federation/v1/send_join/" + roomID + "/" + leaveID, leave, leaveID},
		{"/_matrix/federation/v2/send_join/" + roomID + "/" + leaveID, leave, leaveID},
	}
	for _, tc := range cases {
		status, body := hs2.signedRequest(t, hs1, http.MethodPut, tc.path, eventJSON(t, tc.event))
		if status != http.StatusBadRequest {
			t.Errorf("PUT %s with the wrong membership: status %d, want 400 (body %s)", tc.path, status, body)
		}
		if code := errCode(body); code != "M_INVALID_PARAM" && code != "M_BAD_JSON" && code != "M_NOT_JSON" {
			t.Errorf("PUT %s answered %s; a request that does not mean its URL is a bad request (body %s)",
				tc.path, code, body)
		}
	}
}

// roundTripEvent copies an event through its JSON form, so changing the copy cannot change the
// original. A shallow copy would let a forged membership leak back into the honest event and make
// the test's two events one event.
func roundTripEvent(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("copying an event: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("copying an event: %v", err)
	}
	return out
}

// stateMembershipOf reads a room's m.room.member state for a user on a given node. It is the plainest
// way to ask "what did that handshake do", because a membership change is exactly a change to this
// one state event.
func stateMembershipOf(t *testing.T, node *fedNode, roomID, target, token string) string {
	t.Helper()
	status, content := node.clientDo(t, http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+target, nil, token)
	if status != http.StatusOK {
		t.Fatalf("%s: reading the membership of %s: %d %v", node.name, target, status, content)
	}
	membership, _ := content["membership"].(string)
	return membership
}
