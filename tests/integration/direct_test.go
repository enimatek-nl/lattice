package integration

import (
	"net/http"
	"testing"
)

// TestCreateRoomAsDirectMessage checks the two places a direct message is recorded: in the
// invitation itself, and in the inviter's own account data.
//
// Neither is a property of the room. A room can be a private conversation between two people and
// an ordinary room for somebody who joins later, so the flag belongs to the person who opened it
// and to the invitation they sent — which is why the specification puts it in the member event and
// in `m.direct`, and not in the room's state where every member would read it.
func TestCreateRoomAsDirectMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, _ := h.register(t, "quinn", "pw-quinn", "")
	bob, _, _ := h.register(t, "rosa", "pw-rosa", "")

	roomID := createRoom(t, h, aliceToken, map[string]any{
		"is_direct": true,
		"invite":    []string{bob},
		"preset":    "private_chat",
	})

	// The invitation carries the flag, because that is the copy the invited user's server sees
	// when the user lives elsewhere, and the copy their other devices sync.
	event := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+bob+"?format=event", aliceToken), http.StatusOK).decoded(t)
	content, _ := event["content"].(map[string]any)
	if content["membership"] != "invite" {
		t.Fatalf("membership of the invitation = %v, want invite", content["membership"])
	}
	if content["is_direct"] != true {
		t.Errorf("the invitation does not say it is a direct message: %v", content)
	}

	// And the inviter's account data names the room, so the conversation can be found again.
	mapping := h.mustStatus(t, h.get("/_matrix/client/v3/user/"+alice+"/account_data/m.direct", aliceToken), http.StatusOK).decoded(t)
	list, _ := mapping[bob].([]any)
	if len(list) != 1 || list[0] != roomID {
		t.Fatalf("m.direct for %s = %v, want the room created as a direct message", bob, mapping)
	}

	// A second conversation with the same person adds to the mapping rather than replacing it:
	// account data is a current value, and a server that rewrote it would lose the first
	// conversation.
	second := createRoom(t, h, aliceToken, map[string]any{"is_direct": true, "invite": []string{bob}})
	mapping = h.mustStatus(t, h.get("/_matrix/client/v3/user/"+alice+"/account_data/m.direct", aliceToken), http.StatusOK).decoded(t)
	list, _ = mapping[bob].([]any)
	if len(list) != 2 {
		t.Fatalf("m.direct for %s = %v, want both rooms", bob, mapping)
	}
	seen := map[string]bool{}
	for _, entry := range list {
		if id, _ := entry.(string); id != "" {
			seen[id] = true
		}
	}
	if !seen[roomID] || !seen[second] {
		t.Errorf("m.direct for %s = %v, want %s and %s", bob, list, roomID, second)
	}

	// A room created without the flag is not recorded as one, and an ordinary invitation carries
	// nothing that would make a client read it as private.
	ordinary := createRoom(t, h, aliceToken, map[string]any{"invite": []string{bob}})
	event = h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+ordinary+"/state/m.room.member/"+bob+"?format=event", aliceToken), http.StatusOK).decoded(t)
	content, _ = event["content"].(map[string]any)
	if _, ok := content["is_direct"]; ok {
		t.Errorf("an ordinary invitation carries is_direct: %v", content)
	}
	// The mapping still names the two conversations and nobody else: an ordinary room is not
	// filed as a direct message, and creating one must not disturb what was already recorded.
	mapping = h.mustStatus(t, h.get("/_matrix/client/v3/user/"+alice+"/account_data/m.direct", aliceToken), http.StatusOK).decoded(t)
	if len(mapping) != 1 {
		t.Errorf("m.direct = %v, want only %s", mapping, bob)
	}
	if list, _ := mapping[bob].([]any); len(list) != 2 {
		t.Errorf("m.direct for %s = %v, want the two direct rooms and nothing else", bob, list)
	}
}
