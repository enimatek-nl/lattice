package integration

import (
	"net/http"
	"testing"
	"time"
)

// TestRestrictedRoomJoinConditions is the local half of restricted rooms: a room that opens its
// door to the joined members of another room, on a single server.
//
// Three things are checked, and each fails in a different way if it is wrong. Someone in the
// allow room gets in. Someone outside it does not — and is refused rather than silently placed
// in the room, because a restricted room whose restriction leaks is the one kind of room an
// administrator cannot afford to get wrong. And the join event that lands in the room carries
// `join_authorised_via_users_server` naming a user of this server: the room version's rules
// require the join to be signed by that user's homeserver, so a join without it is an event every
// other server will reject, and a room that accepts it locally is a room that will disagree with
// everyone else about who is in it.
func TestRestrictedRoomJoinConditions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	aliceID, aliceToken, _ := h.register(t, "gatehost", "pw-gatehost", "")
	bobID, bobToken, _ := h.register(t, "gatewaymember", "pw-gatewaymember", "")
	_, carolToken, _ := h.register(t, "outsider", "pw-outsider", "")

	gateway := createRoom(t, h, aliceToken, map[string]any{
		"preset": "public_chat",
		"name":   "the gateway room",
	})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+gateway, map[string]any{}, bobToken), http.StatusOK)

	restricted := createRoom(t, h, aliceToken, restrictedRoomBody("staff only", gateway))

	res := h.post(t, "/_matrix/client/v3/join/"+restricted, map[string]any{}, bobToken)
	if res.status != http.StatusOK {
		t.Fatalf("a member of the allow room was refused the restricted room: %d %s", res.status, res.body)
	}

	// The join has to say who stood behind it, and that someone has to be this server's own
	// user: the signature the room version demands is this server's. The state endpoint answers
	// with the event's content, which is what a client reads.
	content := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+restricted+"/state/m.room.member/"+bobID,
		aliceToken), http.StatusOK).decoded(t)
	authoriser, _ := content["join_authorised_via_users_server"].(string)
	if authoriser == "" {
		t.Fatalf("the restricted join carries no authoriser; the room version requires one and every "+
			"other server will reject the event: %v", content)
	}
	if authoriser != aliceID {
		t.Errorf("the join names %q as its authoriser; this server's joined member with invite power is %q",
			authoriser, aliceID)
	}

	// Someone in no allowed room is refused, with the protocol's refusal code.
	res = h.post(t, "/_matrix/client/v3/join/"+restricted, map[string]any{}, carolToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("an outsider joined a restricted room: %d %s", res.status, res.body)
	}
	if code := errcodeOf(t, res); code != "M_FORBIDDEN" {
		t.Errorf("the refusal of a restricted join came back as %q, wanting M_FORBIDDEN: %s", code, res.body)
	}

	// A client cannot name its own authoriser. The key is a claim about who in the room vouched
	// for the joiner, and the signature that gives it meaning is the server's, not the client's.
	res = h.post(t, "/_matrix/client/v3/join/"+restricted,
		map[string]any{"join_authorised_via_users_server": aliceID}, carolToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("a client forged its way into a restricted room: %d %s", res.status, res.body)
	}
}

// TestFederatedRestrictedRoomJoin is the same room across two servers: the room and its gateway
// both live on hs1, and hs2's user asks hs1 to let them in. That is the path where the resident
// server has to decide twice — once when it offers the join template, and again when the signed
// event comes back — and where an authoriser that this server cannot sign for would produce an
// event that arrives in the room rejected.
func TestFederatedRestrictedRoomJoin(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "remotehost", "pw-remotehost")
	gateway := fedCreateRoom(t, hs1, alice, map[string]any{
		"preset": "public_chat",
		"name":   "gateway across the wire",
	})
	restricted := fedCreateRoom(t, hs1, alice, restrictedRoomBody("staff only, across the wire", gateway))

	bea := hs2.registerUser(t, "bea", "pw-bea")
	cy := hs2.registerUser(t, "cy", "pw-cy-person")
	// registerUser answers with an access token; the room's state knows the user by name.
	beaID := "@" + "bea" + ":" + hs2.name

	// bea joins the gateway first, which is what opens the other door.
	if status, answer := hs2.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/join/"+gateway+"?server_name="+hs1.name, map[string]any{}, bea); status != http.StatusOK {
		t.Fatalf("%s could not join the gateway room: %d %v", hs2.name, status, answer)
	}

	if status, answer := hs2.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/join/"+restricted+"?server_name="+hs1.name, map[string]any{}, bea); status != http.StatusOK {
		t.Fatalf("a member of the allow room was refused a restricted room over federation: %d %v",
			status, answer)
	}

	// hs1 holds the room, so it can say what the join event looks like there.
	member, ok := stateMemberEvent(hs1.roomSnapshot(t, restricted, alice), beaID)
	if !ok {
		t.Fatalf("%s has no member event in %s's state after a join that was accepted", beaID, hs1.name)
	}
	content, _ := member["content"].(map[string]any)
	if authoriser, _ := content["join_authorised_via_users_server"].(string); authoriser == "" {
		t.Errorf("the federated join landed without an authoriser; rule 4.2 of the room version needs "+
			"a signature from the named user's server: %v", content)
	}

	// cy lives on the same server as bea but joined nothing, and must not be let in.
	if status, answer := hs2.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/join/"+restricted+"?server_name="+hs1.name, map[string]any{}, cy); status != http.StatusForbidden {
		t.Fatalf("a user in no allow room joined a restricted room over federation: %d %v", status, answer)
	}
}

// restrictedRoomBody builds a room whose join rule names one gateway room. The room version is
// pinned rather than left to this server's preference, because restricted rooms are a feature of
// particular room versions and a test that follows the server's default would quietly stop
// testing the feature the day the default moves on.
func restrictedRoomBody(name, gateway string) map[string]any {
	return map[string]any{
		"preset":       "private_chat",
		"name":         name,
		"room_version": "11",
		"initial_state": []map[string]any{
			{
				"type":      "m.room.join_rules",
				"state_key": "",
				"content": map[string]any{
					"join_rule": "restricted",
					"allow": []map[string]any{
						{"type": "m.room_membership", "room_id": gateway},
					},
				},
			},
		},
	}
}

// errcodeOf reads the Matrix error code out of an error response, so a refusal can be told apart
// from a failure that merely happens to carry a status this test likes.
func errcodeOf(t *testing.T, res response) string {
	t.Helper()
	code, _ := res.decoded(t)["errcode"].(string)
	return code
}

// stateMemberEvent finds a user's member event in a room's state.
func stateMemberEvent(events roomEvents, userID string) (map[string]any, bool) {
	for _, event := range events.State {
		kind, _ := event["type"].(string)
		key, _ := event["state_key"].(string)
		if kind == "m.room.member" && key == userID {
			return event, true
		}
	}
	return nil, false
}

// TestLeaveViaStateEndpointInRestrictedRoom follows what a client does when it copies a member
// event out of a room and writes it back with the membership changed: the copy still carries
// `join_authorised_via_users_server`, because that is what the join event carried. The room
// version's rule 4.2 asks whether the named user's homeserver signed the event — and this server
// is about to sign the event it is being asked to write — so the write goes through. A server that
// checked that signature against the bytes it was handed rather than the ones it produced would
// refuse a room owner their own kick, in exactly the rooms that use restricted joins.
func TestLeaveViaStateEndpointInRestrictedRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	aliceID, aliceToken, _ := h.register(t, "kickhost", "pw-kickhost", "")
	_, bobToken, _ := h.register(t, "kicktarget", "pw-kicktarget", "")
	gateway := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat", "name": "gateway for the kick"})
	restricted := createRoom(t, h, aliceToken, restrictedRoomBody("kicked out of the staff room", gateway))
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+gateway, map[string]any{}, bobToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+restricted, map[string]any{}, bobToken), http.StatusOK)

	bobID := "@kicktarget:" + h.app.Cfg.ServerName
	res := h.put(t, "/_matrix/client/v3/rooms/"+restricted+"/state/m.room.member/"+bobID,
		map[string]any{"membership": "leave", "join_authorised_via_users_server": aliceID}, aliceToken)
	if res.status != http.StatusOK {
		t.Fatalf("the room owner could not kick a member over the state endpoint: %d %s", res.status, res.body)
	}
	// The room's state now says the user left. Matrix keeps a member event for a departed user
	// rather than deleting it, so what has to change is the membership it carries.
	content := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+restricted+"/state/m.room.member/"+bobID,
		aliceToken), http.StatusOK).decoded(t)
	if membership, _ := content["membership"].(string); membership != "leave" {
		t.Errorf("after the kick the member event says %q, wanting leave: %v", membership, content)
	}
	if _, stale := content["join_authorised_via_users_server"]; stale {
		t.Errorf("the leave event kept an authoriser from the join that preceded it: %v", content)
	}
}

// TestRestrictedJoinUsesThisServersOwnAuthoriser is the preference a restricted room has to keep when
// two homeservers could each stand behind a join. This server holds the room and holds a member with
// the invite power; a neighbour holds the room's creator as well. Relaying the join to the neighbour
// would work — the user gets in — but the join event would then name and carry the signature of
// another server's user, and the room's own members would be able to do nothing about who vouched for
// it. The specification's description of the authoriser is of a user of the resident server, and the
// join made here is what the room's power levels are actually deciding.
func TestRestrictedJoinUsesThisServersOwnAuthoriser(t *testing.T) {
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "nalice", "pw-nalice")
	gateway := fedCreateRoom(t, hs1, alice, map[string]any{"preset": "public_chat", "name": "gateway nc"})
	restricted := fedCreateRoom(t, hs1, alice, restrictedRoomBody("restricted nc", gateway))

	bob := hs2.registerUser(t, "nbob", "pw-nbob")
	charlie := hs2.registerUser(t, "ncharlie", "pw-ncharlie")
	bobID := "@nbob:" + hs2.name
	aliceID := "@nalice:" + hs1.name

	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+gateway+"?server_name="+hs1.name, map[string]any{}, bob); status != http.StatusOK {
		t.Fatalf("bob could not join the gateway: %d %v", status, answer)
	}
	// bob's join of the restricted room has to go remotely, authorised by alice.
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+restricted+"?server_name="+hs1.name, map[string]any{}, bob); status != http.StatusOK {
		t.Fatalf("bob could not join the restricted room: %d %v", status, answer)
	}
	// alice restricts the invite level to moderators and promotes bob.
	if status, answer := hs1.clientDo(t, http.MethodPut, "/_matrix/client/v3/rooms/"+restricted+"/state/m.room.power_levels",
		map[string]any{"invite": 50, "users": map[string]any{aliceID: 100, bobID: 50}}, alice); status != http.StatusOK {
		t.Fatalf("alice could not set the power levels: %d %v", status, answer)
	}
	// hs2 has to learn the new power levels before it can be expected to act on them, and it
	// learns them when the fan-out tells it. The test asks for that state rather than sleeping:
	// until “%s” appears in the levels hs2 holds, a choice between two authorisers is not a choice
	// hs2 is in a position to make.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if holdsInvitePower(t, hs2, restricted, bob, bobID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("“%s” never learned that “%s” holds the invite power in “%s”", hs2.name, bobID, restricted)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+gateway+"?server_name="+hs1.name, map[string]any{}, charlie); status != http.StatusOK {
		t.Fatalf("charlie could not join the gateway: %d %v", status, answer)
	}
	// charlie joins the restricted room through hs2, which holds bob with invite power: the join
	// should be made here rather than relayed to hs1.
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+restricted+"?server_name="+hs2.name, map[string]any{}, charlie); status != http.StatusOK {
		t.Fatalf("charlie could not join the restricted room via %s: %d %v", hs2.name, status, answer)
	}
	var member map[string]any
	var ok bool
	for attempt := 0; attempt < 40; attempt++ {
		member, ok = stateMemberEvent(hs1.roomSnapshot(t, restricted, alice), "@ncharlie:"+hs2.name)
		if ok {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("charlie's join never reached %s's state", hs1.name)
	}
	content, _ := member["content"].(map[string]any)
	if authoriser, _ := content["join_authorised_via_users_server"].(string); authoriser != bobID {
		t.Errorf("charlie's join was authorised by %q, wanting %q (hs2 holds a local user with invite power)", authoriser, bobID)
	}
}

// holdsInvitePower says whether the state a node holds for a room already gives the user the
// room's invitation level.
func holdsInvitePower(t *testing.T, node *fedNode, roomID, token, userID string) bool {
	t.Helper()
	for _, event := range node.roomSnapshot(t, roomID, token).State {
		if event["type"] != "m.room.power_levels" {
			continue
		}
		content, _ := event["content"].(map[string]any)
		users, _ := content["users"].(map[string]any)
		if level, ok := users[userID]; ok {
			switch value := level.(type) {
			case int64:
				return value >= 50
			case float64:
				return value >= 50
			}
		}
	}
	return false
}
