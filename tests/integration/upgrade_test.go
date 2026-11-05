package integration

import (
	"net/http"
	"testing"
)

// TestRoomUpgrade checks what an upgrade leaves behind in both rooms, and what it carries
// across.
func TestRoomUpgrade(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "upgradealice", "pw-upgradealice", "")
	_, alice2Token, _ := h.register(t, "upgradealice2", "pw-upgradealice2", "")

	created := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom", map[string]any{
		"preset":          "public_chat",
		"room_version":    "10",
		"name":            "Before the move",
		"topic":           "Something was discussed here",
		"room_alias_name": "moved-room",
	}, aliceToken), http.StatusOK).decoded(t)
	oldRoomID, _ := created["room_id"].(string)

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+oldRoomID, map[string]any{}, alice2Token), http.StatusOK)

	// A rule about the old room is a rule about this conversation, which is what an
	// upgrade moves.
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/pushrules/global/room/"+urlEscape(oldRoomID),
		map[string]any{"actions": []string{"dont_notify"}, "enabled": true, "default": false}, aliceToken),
		http.StatusOK)

	// The token is taken before the upgrade so that the carried rule can be looked for in
	// what a sync reports after it.
	_, aliceSince := syncNow(t, h, aliceToken, "")

	upgraded := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+oldRoomID+"/upgrade",
		map[string]any{"new_version": "11"}, aliceToken), http.StatusOK).decoded(t)
	newRoomID, _ := upgraded["replacement_room"].(string)
	if newRoomID == "" || newRoomID == oldRoomID {
		t.Fatalf("the upgrade returned %v", upgraded)
	}

	// The new room knows where it came from, with both halves the specification asks for.
	create := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+newRoomID+"/state/m.room.create/", aliceToken),
		http.StatusOK).decoded(t)
	if create["room_version"] != "11" {
		t.Errorf("the replacement room's version = %v, want 11: %v", create["room_version"], create)
	}
	predecessor, _ := create["predecessor"].(map[string]any)
	if predecessor["room_id"] != oldRoomID {
		t.Errorf("the predecessor is %v, want the room that was upgraded", create["predecessor"])
	}
	if eventID, _ := predecessor["event_id"].(string); eventID == "" {
		t.Errorf("the predecessor names no event, and the specification requires one: %v", create["predecessor"])
	}

	// The old room says it is finished, and where to go instead.
	tombstone := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+oldRoomID+"/state/m.room.tombstone/", aliceToken),
		http.StatusOK).decoded(t)
	if tombstone["replacement_room"] != newRoomID {
		t.Errorf("the tombstone is %v, want it to name %s", tombstone, newRoomID)
	}
	if body, _ := tombstone["body"].(string); body == "" {
		t.Errorf("the tombstone has no body, which the specification requires: %v", tombstone)
	}

	// The settings the conversation was run under came with it.
	for _, entry := range []struct {
		stateType string
		key       string
		want      string
	}{
		{"m.room.name", "name", "Before the move"},
		{"m.room.topic", "topic", "Something was discussed here"},
	} {
		state := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+newRoomID+"/state/"+entry.stateType+"/", aliceToken),
			http.StatusOK).decoded(t)
		if state[entry.key] != entry.want {
			t.Errorf("%s in the replacement room = %v, want %q", entry.stateType, state[entry.key], entry.want)
		}
	}
	// The room is still joinable by a member of the old one, which is what a copied
	// m.room.join_rules means in practice.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+newRoomID, map[string]any{}, alice2Token), http.StatusOK)

	// The name moved too: an alias left on the finished room would lead a client somewhere
	// its own server says is over.
	canonical := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+oldRoomID+"/state/m.room.canonical_alias/", aliceToken),
		http.StatusOK).decoded(t)
	alias, _ := canonical["alias"].(string)
	if alias == "" {
		t.Fatalf("the room created with a name does not say so: %v", canonical)
	}
	directory := h.mustStatus(t, h.get("/_matrix/client/v3/directory/room/"+urlEscape(alias), aliceToken),
		http.StatusOK).decoded(t)
	if directory["room_id"] != newRoomID {
		t.Errorf("the alias resolves to %v, want %s", directory["room_id"], newRoomID)
	}

	// And the rule about the old room is now also a rule about the new one.
	for _, roomID := range []string{oldRoomID, newRoomID} {
		if !hasRoomPushRule(t, h, aliceToken, roomID, "dont_notify") {
			t.Errorf("no carried push rule for %s", roomID)
		}
	}
	body, _ := syncNow(t, h, aliceToken, "&since="+aliceSince)
	if !accountDataMentions(body, newRoomID) {
		t.Errorf("the carried rule never reached the sync stream for the user who owns it: %v", body["account_data"])
	}
}

// TestRoomUpgradeErrors checks the answers a client gets when it cannot have what it asked
// for.
func TestRoomUpgradeErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "erralice", "pw-erralice", "")
	_, strangerToken, _ := h.register(t, "errstranger", "pw-errstranger", "")

	created := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat"}, aliceToken), http.StatusOK).decoded(t)
	roomID, _ := created["room_id"].(string)

	// A room version this server does not have is a bad request, and the answer names the
	// version so a client can offer another one.
	unsupported := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/upgrade",
		map[string]any{"new_version": "9999"}, aliceToken), http.StatusBadRequest).decoded(t)
	if unsupported["errcode"] != "M_UNSUPPORTED_ROOM_VERSION" {
		t.Errorf("an unsupported room version: %v", unsupported)
	}
	// A version this server has rules for but has not implemented is the same answer, and it
	// matters for a different reason than creation does: a room upgraded into such a version
	// would still exist, still take events, and still be a room this server cannot work out
	// the state of. Recognising a version is not the same as being able to run it.
	if got := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/upgrade",
		map[string]any{"new_version": "12"}, aliceToken), http.StatusBadRequest).decoded(t); got["errcode"] != "M_UNSUPPORTED_ROOM_VERSION" {
		t.Errorf("an upgrade into a version this server recognises but does not implement: %v", got)
	}
	// A request that does not name a version at all is missing a parameter, which is a
	// different mistake.
	if got := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/upgrade",
		map[string]any{}, aliceToken), http.StatusBadRequest).decoded(t); got["errcode"] != "M_MISSING_PARAMS" {
		t.Errorf("an upgrade with no new_version: %v", got)
	}
	// Somebody with no standing in the room cannot end it.
	forbidden := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/upgrade",
		map[string]any{"new_version": "11"}, strangerToken), http.StatusForbidden).decoded(t)
	if forbidden["errcode"] != "M_FORBIDDEN" {
		t.Errorf("an upgrade by a stranger: %v", forbidden)
	}
	// And the room is still a room: a refused upgrade must not have half-done it.
	h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.tombstone/", aliceToken), http.StatusNotFound)

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/upgrade",
		map[string]any{"new_version": "11"}, aliceToken), http.StatusOK)

	// A room that has already been upgraded has an end, and a second one would leave two
	// replacements.
	again := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/upgrade",
		map[string]any{"new_version": "11"}, aliceToken), http.StatusBadRequest).decoded(t)
	if again["errcode"] != "M_BAD_STATE" {
		t.Errorf("upgrading twice: %v", again)
	}
	// A room this server has never heard of is not a room it can upgrade.
	if got := h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/!nosuchroom:elsewhere/upgrade",
		map[string]any{"new_version": "11"}, aliceToken), http.StatusNotFound).decoded(t); got["errcode"] != "M_NOT_FOUND" {
		t.Errorf("upgrading an unknown room: %v", got)
	}
}

// TestManualRoomUpgrade checks the other way an upgrade happens: a client creates the new
// room itself, naming a predecessor, and then closes the old room. The server still owes the
// user the rules that moved with the conversation.
func TestManualRoomUpgrade(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "manualalice", "pw-manualalice", "")

	created := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "room_version": "10"}, aliceToken), http.StatusOK).decoded(t)
	oldRoomID, _ := created["room_id"].(string)
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/pushrules/global/room/"+urlEscape(oldRoomID),
		map[string]any{"actions": []string{"dont_notify"}, "enabled": true, "default": false}, aliceToken),
		http.StatusOK)

	// The client names only the old room. The event the specification requires in a
	// predecessor is this server's to fill in, because it is the one that knows the room.
	newer := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom", map[string]any{
		"preset":       "public_chat",
		"room_version": "11",
		"creation_content": map[string]any{
			"predecessor": map[string]any{"room_id": oldRoomID},
		},
	}, aliceToken), http.StatusOK).decoded(t)
	newRoomID, _ := newer["room_id"].(string)

	create := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+newRoomID+"/state/m.room.create/", aliceToken),
		http.StatusOK).decoded(t)
	predecessor, _ := create["predecessor"].(map[string]any)
	if predecessor["room_id"] != oldRoomID {
		t.Fatalf("the predecessor is %v", create["predecessor"])
	}
	if eventID, _ := predecessor["event_id"].(string); eventID == "" {
		t.Errorf("the predecessor has no event_id: %v", predecessor)
	}

	// Nothing has been carried yet: the old room is still a room.
	if hasRoomPushRule(t, h, aliceToken, newRoomID, "dont_notify") {
		t.Errorf("rules were carried before the old room was closed")
	}

	// Closing the old room is what tells the server the conversation moved.
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+oldRoomID+"/state/m.room.tombstone/",
		map[string]any{"body": "This room has been replaced", "replacement_room": newRoomID}, aliceToken),
		http.StatusOK)
	if !hasRoomPushRule(t, h, aliceToken, newRoomID, "dont_notify") {
		t.Errorf("the tombstone closed the room but carried nothing with it")
	}
	// The rule for the old room is kept: the history is still there, and a client shows both
	// rooms.
	if !hasRoomPushRule(t, h, aliceToken, oldRoomID, "dont_notify") {
		t.Errorf("the rule for the old room was removed")
	}

	// A predecessor that names a room this server does not have is somebody else's claim,
	// and an unparseable one is the client's mistake.
	if got := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom", map[string]any{
		"creation_content": map[string]any{"predecessor": "not-an-object"},
	}, aliceToken), http.StatusBadRequest).decoded(t); got["errcode"] != "M_INVALID_PARAM" {
		t.Errorf("a predecessor that is not an object: %v", got)
	}
	if got := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom", map[string]any{
		"creation_content": map[string]any{"predecessor": map[string]any{"room_id": "not-a-room"}},
	}, aliceToken), http.StatusBadRequest).decoded(t); got["errcode"] != "M_INVALID_PARAM" {
		t.Errorf("a predecessor naming something that is not a room ID: %v", got)
	}
}

// hasRoomPushRule reports whether the user has a room rule with those actions.
func hasRoomPushRule(t *testing.T, h *harness, token, roomID, action string) bool {
	t.Helper()
	set := h.mustStatus(t, h.get("/_matrix/client/v3/pushrules", token), http.StatusOK).decoded(t)
	global, _ := set["global"].(map[string]any)
	rules, _ := global["room"].([]any)
	for _, item := range rules {
		rule, _ := item.(map[string]any)
		if rule["rule_id"] != roomID {
			continue
		}
		actions, _ := rule["actions"].([]any)
		for _, entry := range actions {
			if name, _ := entry.(string); name == action {
				return true
			}
		}
	}
	return false
}

// accountDataMentions reports whether a sync response's global account data contains a
// string anywhere in it.
func accountDataMentions(body map[string]any, needle string) bool {
	account, _ := body["account_data"].(map[string]any)
	events, _ := account["events"].([]any)
	for _, item := range events {
		if jsonContainsString(item, needle) {
			return true
		}
	}
	return false
}

// containsString walks a decoded JSON value looking for a string.
func jsonContainsString(value any, needle string) bool {
	switch typed := value.(type) {
	case string:
		return typed == needle
	case map[string]any:
		for _, item := range typed {
			if jsonContainsString(item, needle) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if jsonContainsString(item, needle) {
				return true
			}
		}
	}
	return false
}

// urlEscape makes an ID safe to put in a path. Room IDs begin with "!", aliases begin with
// "#", and both are legal path characters once percent-encoded.
func urlEscape(id string) string {
	const upper = "0123456789abcdef"
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c == '!' || c == '#' || c == '/' {
			out = append(out, '%', upper[c>>4], upper[c&0xf])
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
