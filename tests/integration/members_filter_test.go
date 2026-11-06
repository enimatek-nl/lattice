package integration

import (
	"net/http"
	"testing"
)

// memberStateKeys reads the chunk of a /members response into "state_key:membership" names, which
// is the pair the endpoint is really about: who, and what they were.
func memberStateKeys(t *testing.T, body map[string]any) []string {
	t.Helper()
	chunk, _ := body["chunk"].([]any)
	out := make([]string, 0, len(chunk))
	for _, entry := range chunk {
		event, _ := entry.(map[string]any)
		stateKey, _ := event["state_key"].(string)
		content, _ := event["content"].(map[string]any)
		membership, _ := content["membership"].(string)
		out = append(out, stateKey+":"+membership)
	}
	return out
}

func equalNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestMembersFiltersAndAtPoint checks the three ways a client narrows a member list, including the
// one that asks about the past.
//
// `membership=` and `not_membership=` decide which memberships the answer may contain. `at` asks a
// different question — who was in the room at a point in the history the client already holds — and
// it cannot be answered from the roster on hand: a member whose join this server has since replaced
// with a leave is not in the current state at all, so cutting the current roster by position has
// nothing left to cut. The answer has to come from the state the room carried at that moment.
func TestMembersFiltersAndAtPoint(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	aliceID, aliceToken, _ := h.register(t, "iris", "pw-iris", "")
	bobID, bobToken, _ := h.register(t, "oberon", "pw-oberon", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	// Where the room stood before anyone else arrived.
	_, whileAlone := syncNow(t, h, aliceToken, "")

	join(t, h, roomID, bobToken, map[string]any{})
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/roster-1", map[string]any{
		"msgtype": "m.text", "body": "hello from bob",
	}, bobToken), http.StatusOK)
	// And where it stood with both of them in it.
	_, withBob := syncNow(t, h, aliceToken, "")

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)

	rooms := "/_matrix/client/v3/rooms/" + roomID + "/members"
	alice := aliceID + ":join"
	bobJoin := bobID + ":join"
	bobLeave := bobID + ":leave"

	cases := []struct {
		name string
		path string
		want []string
	}{
		// Today's roster holds each member once, as whatever they are now.
		{"current", rooms, []string{alice, bobLeave}},
		{"membership=join", rooms + "?membership=join", []string{alice}},
		{"membership=leave", rooms + "?membership=leave", []string{bobLeave}},
		{"not_membership=leave", rooms + "?not_membership=leave", []string{alice}},
		// The past roster holds bob as he was then: joined, and not the leave that came after.
		{"at=while bob was here", rooms + "?at=" + withBob, []string{alice, bobJoin}},
		{"at=while bob was here, joined only", rooms + "?at=" + withBob + "&membership=join", []string{alice, bobJoin}},
		{"at=before bob arrived", rooms + "?at=" + whileAlone, []string{alice}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := h.mustStatus(t, h.get(tc.path, aliceToken), http.StatusOK).decoded(t)
			got := memberStateKeys(t, body)
			if !equalNames(got, tc.want) {
				t.Errorf("%s = %v, want %v", tc.path, got, tc.want)
			}
		})
	}

	// A token this server never issued is a bad request, not an empty answer or a guess built
	// from today's roster.
	bad := h.get(rooms+"?at=notatoken", aliceToken)
	if bad.status != http.StatusBadRequest {
		t.Errorf("at=notatoken status = %d, want 400 (body: %s)", bad.status, bad.body)
	}
}
