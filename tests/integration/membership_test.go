package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// join posts a join request for the given room.
func join(t *testing.T, h *harness, roomID, token string, body map[string]any) response {
	t.Helper()
	return h.post(t, "/_matrix/client/v3/join/"+roomID, body, token)
}

// putState sends a state event and returns the response.
func putState(t *testing.T, h *harness, roomID, eventType, stateKey, content string, token string) response {
	t.Helper()
	return h.raw(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/state/"+eventType+"/"+stateKey, []byte(content), token)
}

// currentMembership reads the room's m.room.member state for a user, which is the only
// way to see what a membership change actually did without /sync.
func currentMembership(t *testing.T, h *harness, roomID, target, token string) string {
	t.Helper()
	res := h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+target, token)
	if res.status != http.StatusOK {
		t.Fatalf("reading membership of %s: status %d (body %s)", target, res.status, res.body)
	}
	content, _ := res.decoded(t)["membership"].(string)
	return content
}

// TestInviteThenJoinIsTheOnlyWayIntoAPrivateRoom is the multi-user case that was
// impossible before these endpoints: a private room admits no one until someone with
// the invite power level asks for them.
func TestInviteThenJoinIsTheOnlyWayIntoAPrivateRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	aliceID, aliceToken, _ := h.register(t, "mallory", "pw-mallory", "")
	_, bobToken, _ := h.register(t, "oscar", "pw-oscar", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat", "name": "Inner"})

	if res := join(t, h, roomID, bobToken, map[string]any{}); res.status != http.StatusForbidden {
		t.Fatalf("joining a private room uninvited: status %d, want 403 (body %s)", res.status, res.body)
	}

	// Rule 4.4.2: an inviter must themselves be joined. Bob is not, so his attempt to
	// invite himself is refused rather than half-applied.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": bobAtHS}, bobToken), http.StatusForbidden)
	if got := currentMembership(t, h, roomID, aliceID, aliceToken); got != "join" {
		t.Errorf("the creator's own membership = %q, want join", got)
	}

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": bobAtHS}, aliceToken), http.StatusOK)
	if got := currentMembership(t, h, roomID, bobAtHS, aliceToken); got != "invite" {
		t.Fatalf("membership after invite = %q, want invite", got)
	}

	res := join(t, h, roomID, bobToken, map[string]any{})
	h.mustStatus(t, res, http.StatusOK)
	if got, _ := res.decoded(t)["room_id"].(string); got != roomID {
		t.Errorf("join returned room_id %q, want %q", got, roomID)
	}
	if got := currentMembership(t, h, roomID, bobAtHS, aliceToken); got != "join" {
		t.Fatalf("membership after join = %q, want join", got)
	}

	// The joined user can now write, which a non-member could not.
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-bob", `{"msgtype":"m.text","body":"joined"}`, bobToken), http.StatusOK)
}

const bobAtHS = "@oscar:hs1"

// TestPublicRoomJoinAndLeave covers the join rule that needs no invitation, and the
// leave path back out.
func TestPublicRoomJoinAndLeave(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "peggy", "pw-peggy", "")
	bob, bobToken, _ := h.register(t, "roman", "pw-roman", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{"via": []string{"elsewhere.example"}}), http.StatusOK)
	if got := currentMembership(t, h, roomID, bob, bobToken); got != "join" {
		t.Fatalf("membership = %q, want join", got)
	}

	joined := h.mustStatus(t, h.get("/_matrix/client/v3/joined_rooms", bobToken), http.StatusOK).decoded(t)
	list, _ := joined["joined_rooms"].([]any)
	if len(list) != 1 || list[0] != roomID {
		t.Errorf("joined_rooms = %v, want [%s]", joined["joined_rooms"], roomID)
	}

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{"reason": "done"}, bobToken), http.StatusOK)
	// The ex-member can no longer read the room, so the check is made with the
	// account that stayed.
	if got := currentMembership(t, h, roomID, bob, aliceToken); got != "leave" {
		t.Errorf("membership after leave = %q, want leave", got)
	}
	joined = h.mustStatus(t, h.get("/_matrix/client/v3/joined_rooms", bobToken), http.StatusOK).decoded(t)
	if list, _ := joined["joined_rooms"].([]any); len(list) != 0 {
		t.Errorf("joined_rooms after leaving = %v, want empty", joined["joined_rooms"])
	}
}

// TestKnockRequiresAKnockJoinRule checks that knocking is a property of the room, not of
// the client asking politely: the join rule decides.
func TestKnockRequiresAKnockJoinRule(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "sid", "pw-sid", "")
	bob, bobToken, _ := h.register(t, "trin", "pw-trin", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat"})

	res := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/knock", map[string]any{"reason": "let me in"}, bobToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("knocking at an invite-only room: status %d, want 403 (body %s)", res.status, res.body)
	}

	// The room's owner switches the join rule, and the same request now works.
	h.mustStatus(t, putState(t, h, roomID, "m.room.join_rules", "", `{"join_rule":"knock"}`, aliceToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/knock", map[string]any{"reason": "let me in"}, bobToken), http.StatusOK)
	if got := currentMembership(t, h, roomID, bob, bobToken); got != "knock" {
		t.Fatalf("membership after knock = %q, want knock", got)
	}

	// An invitation converts the knock into a joinable state.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": bob}, aliceToken), http.StatusOK)
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	if got := currentMembership(t, h, roomID, bob, bobToken); got != "join" {
		t.Errorf("membership after invited join = %q, want join", got)
	}
}

// TestBanBlocksJoinUntilUnbanned checks the two halves of the ban state: a banned user
// cannot rejoin, and lifting the ban is expressed by a leave event.
func TestBanBlocksJoinUntilUnbanned(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "ursula", "pw-ursula", "")
	bob, bobToken, _ := h.register(t, "victor", "pw-victor", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/ban",
		map[string]any{"user_id": bob, "reason": "spam"}, aliceToken), http.StatusOK)
	if got := currentMembership(t, h, roomID, bob, aliceToken); got != "ban" {
		t.Fatalf("membership after ban = %q, want ban", got)
	}

	res := join(t, h, roomID, bobToken, map[string]any{})
	if res.status != http.StatusForbidden {
		t.Fatalf("a banned user joining again: status %d, want 403 (body %s)", res.status, res.body)
	}

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/unban",
		map[string]any{"user_id": bob}, aliceToken), http.StatusOK)
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	if got := currentMembership(t, h, roomID, bob, aliceToken); got != "join" {
		t.Errorf("membership after unban and join = %q, want join", got)
	}
}

// TestKickNeedsPower checks the kick half of rule 4.5: a joined member with no power
// cannot remove another member, and the room owner can.
func TestKickNeedsPower(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "wendy", "pw-wendy", "")
	bob, bobToken, _ := h.register(t, "xena", "pw-xena", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)

	res := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/kick",
		map[string]any{"user_id": "@wendy:hs1", "reason": "try it"}, bobToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("a powerless member kicking the owner: status %d, want 403 (body %s)", res.status, res.body)
	}

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/kick",
		map[string]any{"user_id": bob, "reason": "away"}, aliceToken), http.StatusOK)
	if got := currentMembership(t, h, roomID, bob, aliceToken); got != "leave" {
		t.Errorf("membership after kick = %q, want leave", got)
	}
}

// TestPowerLevelsGateMembership checks that the power levels really decide membership,
// and pins the specification's default: the invite level starts at 0, so any joined
// member may invite. That is not an oversight to tighten later — it is what the room
// version says, and a room owner can raise it.
func TestPowerLevelsGateMembership(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, _ := h.register(t, "yuri", "pw-yuri", "")
	bob, bobToken, _ := h.register(t, "zoe", "pw-zoe", "")
	carol, carolToken, _ := h.register(t, "adam", "pw-adam", "")
	_, daveToken, _ := h.register(t, "bill", "pw-bill", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat"})

	if res := join(t, h, roomID, bobToken, map[string]any{}); res.status != http.StatusForbidden {
		t.Fatalf("joining a private room uninvited: status %d, want 403 (body %s)", res.status, res.body)
	}
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": bob}, aliceToken), http.StatusOK)
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)

	// invite is 0 in the preset, which mirrors the specification's default.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": carol}, bobToken), http.StatusOK)
	h.mustStatus(t, join(t, h, roomID, carolToken, map[string]any{}), http.StatusOK)

	// The owner raises the invite level; a joined member at level 0 loses the ability.
	h.mustStatus(t, putState(t, h, roomID, "m.room.power_levels", "", fmt.Sprintf(
		`{"users":{"%s":100,"%s":50,"%s":0},"users_default":0,"events_default":0,"state_default":50,"ban":50,"kick":50,"invite":50,"redact":50}`,
		alice, bob, carol), aliceToken), http.StatusOK)

	res := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite", map[string]any{"user_id": "@bill:hs1"}, carolToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("inviting at level 0 with the invite level at 50: status %d, want 403 (body %s)", res.status, res.body)
	}

	h.mustStatus(t, putState(t, h, roomID, "m.room.power_levels", "", fmt.Sprintf(
		`{"users":{"%s":100,"%s":50,"%s":50},"users_default":0,"events_default":0,"state_default":50,"ban":50,"kick":50,"invite":50,"redact":50}`,
		alice, bob, carol), aliceToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": "@bill:hs1"}, carolToken), http.StatusOK)

	// Rule 9: a member may not promote themselves above the level they hold.
	res = putState(t, h, roomID, "m.room.power_levels", "", fmt.Sprintf(
		`{"users":{"%s":100,"%s":100}}`, alice, carol), carolToken)
	if res.status != http.StatusForbidden {
		t.Errorf("a level-50 member promoting themselves to 100: status %d, want 403 (body %s)", res.status, res.body)
	}
	_ = daveToken
}

// TestMemberListing checks the two membership views clients use.
func TestMemberListing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "bea", "pw-bea", "")
	bob, bobToken, _ := h.register(t, "cal", "pw-cal", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": "@dee:hs1"}, aliceToken), http.StatusNotFound) // no such account

	members := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/members", aliceToken), http.StatusOK).decoded(t)
	chunk, _ := members["chunk"].([]any)
	if len(chunk) != 2 {
		t.Fatalf("members returned %d entries, want 2 (%v)", len(chunk), members["chunk"])
	}

	joined := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/joined_members", aliceToken), http.StatusOK).decoded(t)
	list, _ := joined["joined"].(map[string]any)
	if _, ok := list[bob]; !ok {
		t.Errorf("joined_members does not list %s: %v", bob, joined["joined"])
	}
	if _, ok := list["@bea:hs1"]; !ok {
		t.Errorf("joined_members does not list the creator: %v", joined["joined"])
	}

	// A left member is still listed, and excluded on request.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)
	all := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/members", aliceToken), http.StatusOK).decoded(t)
	if chunk, _ = all["chunk"].([]any); len(chunk) != 2 {
		t.Errorf("after leaving, members returned %d entries, want 2", len(chunk))
	}
	filtered := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/members?not_membership=leave", aliceToken), http.StatusOK).decoded(t)
	if chunk, _ = filtered["chunk"].([]any); len(chunk) != 1 {
		t.Errorf("not_membership=leave returned %d entries, want 1", len(chunk))
	}
}

// TestMembershipErrors pins the answers a client acts on when a membership request is
// malformed, names an unknown account, or asks for something this server cannot do.
func TestMembershipErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "eve", "pw-eve", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	cases := []struct {
		name     string
		path     string
		body     map[string]any
		wantCode string
		wantStat int
	}{
		{"invite with no user", "/_matrix/client/v3/rooms/" + roomID + "/invite", map[string]any{}, "M_MISSING_PARAMS", http.StatusBadRequest},
		{"unknown local user", "/_matrix/client/v3/rooms/" + roomID + "/invite", map[string]any{"user_id": "@nobody:hs1"}, "M_NOT_FOUND", http.StatusNotFound},
		// An invite for a user who lives elsewhere now travels to that user's server, which
		// has to countersign it. There is no server at elsewhere.example in this harness, so
		// the honest answer is a gateway failure rather than "not implemented": this server did
		// its part and the peer did not answer. Two homeservers that do answer are covered by
		// Complement's TestFederationRejectInvite, where two homeservers do answer.
		{"remote user", "/_matrix/client/v3/rooms/" + roomID + "/invite", map[string]any{"user_id": "@someone:elsewhere.example"}, "M_UNKNOWN", http.StatusBadGateway},
		{"third-party invite", "/_matrix/client/v3/rooms/" + roomID + "/invite",
			map[string]any{"user_id": "@someone:elsewhere.example", "third_party_signed": map[string]any{"token": "t"}}, "", http.StatusNotImplemented},
		{"malformed user ID", "/_matrix/client/v3/rooms/" + roomID + "/invite", map[string]any{"user_id": "not-a-user-id"}, "M_INVALID_PARAM", http.StatusBadRequest},
		{"unknown room join", "/_matrix/client/v3/join/!unknown:hs1", map[string]any{}, "M_NOT_FOUND", http.StatusNotFound},
		{"malformed room", "/_matrix/client/v3/join/not-a-room", map[string]any{}, "M_INVALID_PARAM", http.StatusBadRequest},
		// An alias must be percent-encoded in the path: a bare # would be read as a
		// fragment and never reach the server at all. An alias this server does not
		// have is a 404, not a claim that aliases are unsupported.
		{"unknown alias join", "/_matrix/client/v3/join/%23somewhere:hs1", map[string]any{}, "M_NOT_FOUND", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.post(t, tc.path, tc.body, aliceToken)
			if res.status != tc.wantStat {
				t.Fatalf("status = %d, want %d (body %s)", res.status, tc.wantStat, res.body)
			}
			if tc.wantCode != "" {
				if code := res.errcode(t); code != tc.wantCode {
					t.Errorf("errcode = %q, want %q", code, tc.wantCode)
				}
			}
		})
	}
}

// TestStateReads checks the state views, including that an unset state key answers 404
// rather than an empty object, which is how a client tells the two apart.
func TestStateReads(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "gwen", "pw-gwen", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat", "topic": "the topic"})

	state := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state", aliceToken), http.StatusOK)
	// The body is the bare array the endpoint's own schema describes. The specification
	// says elsewhere that every endpoint answers with a JSON object, and this is one of
	// the places where the two statements disagree; the endpoint-specific one is what
	// clients act on. Recorded here so nobody "fixes" it into an envelope again.
	var events []any
	if err := json.Unmarshal(state.body, &events); err != nil {
		t.Fatalf("GET /state did not return an array: %v (%s)", err, state.body)
	}
	types := map[string]bool{}
	for _, entry := range events {
		event, _ := entry.(map[string]any)
		types[event["type"].(string)] = true
	}
	for _, want := range []string{"m.room.create", "m.room.member", "m.room.power_levels", "m.room.join_rules", "m.room.history_visibility", "m.room.topic"} {
		if !types[want] {
			t.Errorf("the initial state has no %s event (types: %v)", want, types)
		}
	}

	topic := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic/", aliceToken), http.StatusOK)
	if got, _ := topic.decoded(t)["topic"].(string); got != "the topic" {
		t.Errorf("topic content = %q, want %q", got, "the topic")
	}

	missing := h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.name/", aliceToken)
	if missing.status != http.StatusNotFound {
		t.Errorf("reading unset state: status %d, want 404 (body %s)", missing.status, missing.body)
	}
	if code := missing.errcode(t); code != "M_NOT_FOUND" {
		t.Errorf("errcode = %q, want M_NOT_FOUND", code)
	}

	deep := h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic/a/b", aliceToken)
	if deep.status != http.StatusBadRequest {
		t.Errorf("a state path with too many segments: status %d, want 400 (body %s)", deep.status, deep.body)
	}

	unknownRoom := h.get("/_matrix/client/v3/rooms/!nothere:hs1/state", aliceToken)
	if unknownRoom.status != http.StatusNotFound {
		t.Errorf("state of an unknown room: status %d, want 404", unknownRoom.status)
	}
}

// TestKickNeedsTheTargetToBePresent checks the Client-Server side of kicking: a room
// owner may remove a member, but not someone who never joined and not someone who has
// already left. The event authorisation rules do not forbid the second case, so this is
// where the rule lives, and it matches the sytest and Complement expectations.
func TestKickNeedsTheTargetToBePresent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "kim", "pw-kim", "")
	bob, bobToken, _ := h.register(t, "lee", "pw-lee", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	res := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/kick",
		map[string]any{"user_id": bob, "reason": "never here"}, aliceToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("kicking a user who never joined: status %d, want 403 (body %s)", res.status, res.body)
	}

	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/kick",
		map[string]any{"user_id": bob, "reason": "out"}, aliceToken), http.StatusOK)

	res = h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/kick",
		map[string]any{"user_id": bob, "reason": "already gone"}, aliceToken)
	if res.status != http.StatusForbidden {
		t.Errorf("kicking a user who already left: status %d, want 403 (body %s)", res.status, res.body)
	}
}
