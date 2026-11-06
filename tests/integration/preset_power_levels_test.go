package integration

import (
	"net/http"
	"testing"
)

// TestInvitationLevelFollowsThePreset pins what a new room says about inviting, because the
// authorisation rules do not decide it: the rule reads `invite` from the power levels and defaults it
// to zero, and the power levels are the creating server's to write. A public room has to be gated at
// 50, since an invitation is a door key in a restricted room (rule 4.3.5.1 admits an invited user past
// the join restriction) and a public room whose every member may invite is a public room whose
// restriction every member can lift. A private room is the opposite shape on purpose: a room made to
// gather a handful of people is a room where those people may bring whoever they came for.
func TestInvitationLevelFollowsThePreset(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, ownerToken, _ := h.register(t, "plowner", "pw-plowner", "")
	memberID, memberToken, _ := h.register(t, "plmember", "pw-plmember", "")
	watcherID, _, _ := h.register(t, "plwatcher", "pw-plwatcher", "")

	public := createRoom(t, h, ownerToken, map[string]any{"preset": "public_chat", "name": "a room anyone may join"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+public+"/invite", map[string]any{"user_id": memberID}, ownerToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+public, map[string]any{}, memberToken), http.StatusOK)

	private := createRoom(t, h, ownerToken, map[string]any{"preset": "private_chat", "name": "a room we are gathering people into"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+private+"/invite", map[string]any{"user_id": memberID}, ownerToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+private, map[string]any{}, memberToken), http.StatusOK)

	// The member has no power in either room, and the room says what that means for inviting.
	res := h.post(t, "/_matrix/client/v3/rooms/"+public+"/invite", map[string]any{"user_id": watcherID}, memberToken)
	if res.status != http.StatusForbidden {
		t.Errorf("in the public room a member with no power invited somebody: %d %s", res.status, res.body)
	}
	res = h.post(t, "/_matrix/client/v3/rooms/"+private+"/invite", map[string]any{"user_id": watcherID}, memberToken)
	if res.status != http.StatusOK {
		t.Errorf("in the private room a member could not invite: %d %s", res.status, res.body)
	}

	// The refusal has to be the room's rules refusing it, not an accident of the endpoint.
	lowered := h.put(t, "/_matrix/client/v3/rooms/"+private+"/state/m.room.power_levels", map[string]any{
		"users":          map[string]any{"@plowner:" + h.app.Cfg.ServerName: 100, memberID: 0},
		"users_default":  0,
		"events_default": 0,
		"state_default":  50,
		"invite":         50,
		"kick":           50,
		"ban":            50,
		"redact":         50,
	}, ownerToken)
	if lowered.status != http.StatusOK {
		t.Fatalf("the room owner could not raise the invitation level: %d %s", lowered.status, lowered.body)
	}
	latecomerID, _, _ := h.register(t, "pllatecomer", "pw-pllatecomer", "")
	res = h.post(t, "/_matrix/client/v3/rooms/"+private+"/invite", map[string]any{"user_id": latecomerID}, memberToken)
	if res.status != http.StatusForbidden {
		t.Errorf("raising the invitation level to 50 did not stop the member inviting: %d %s", res.status, res.body)
	}
}

// TestTrustedPrivateChatInviteesRunTheRoom is the last column of the specification's preset table,
// the one that is not a join rule and so is easy to leave out: in `trusted_private_chat` every invitee
// gets the creator's power level. A room created as a trusted private chat with three people in it is
// a room those three run, not a room where one of them can undo what the other two decided.
func TestTrustedPrivateChatInviteesRunTheRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, ownerToken, _ := h.register(t, "topwner", "pw-topwner", "")
	friendID, friendToken, _ := h.register(t, "topfriend", "pw-topfriend", "")
	strangerID, _, _ := h.register(t, "topstranger", "pw-topstranger", "")

	room := createRoom(t, h, ownerToken, map[string]any{
		"preset": "trusted_private_chat",
		"name":   "three of us, all of us in charge",
		"invite": []string{friendID},
	})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+room, map[string]any{}, friendToken), http.StatusOK)

	// The friend can change the room's name, which needs the state level of 50 that an ordinary
	// member does not have.
	res := h.put(t, "/_matrix/client/v3/rooms/"+room+"/state/m.room.name", map[string]any{"name": "renamed by a friend"}, friendToken)
	if res.status != http.StatusOK {
		t.Fatalf("an invitee of a trusted private chat could not rename it: %d %s", res.status, res.body)
	}

	// The same user, invited into an ordinary private room, cannot: the difference is the preset.
	plain := createRoom(t, h, ownerToken, map[string]any{"preset": "private_chat", "name": "one of us in charge"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+plain+"/invite", map[string]any{"user_id": friendID}, ownerToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+plain, map[string]any{}, friendToken), http.StatusOK)
	res = h.put(t, "/_matrix/client/v3/rooms/"+plain+"/state/m.room.name", map[string]any{"name": "renamed by a friend"}, friendToken)
	if res.status == http.StatusOK {
		t.Errorf("a member of an ordinary private room renamed it, which the preset does not permit: %s", res.body)
	}
	_ = strangerID
}
