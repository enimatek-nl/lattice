package integration

import (
	"net/http"
	"testing"
)

// A profile is this server's statement about its own account, and it is read in two places: the profile
// endpoints, and the member events every room holds for that user. A change therefore has to reach both,
// or the same person is presented two ways at once depending on which event a client happens to be
// rendering. These tests are about that reach, and about who is allowed to make the statement.

// memberContent reads what a room says about a user. `GET /rooms/{roomID}/state/{type}/{key}` answers
// with the event's content and nothing around it, which is why the body is read as the content.
func memberContent(t *testing.T, h *harness, roomID, userID, token string) map[string]any {
	t.Helper()
	res := h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+userID, token)
	if res.status != http.StatusOK {
		t.Fatalf("reading %s's member event in %s: status %d (body %s)", userID, roomID, res.status, res.body)
	}
	content := res.decoded(t)
	if content == nil {
		t.Fatalf("%s's member event in %s is not a JSON object: %s", userID, roomID, res.body)
	}
	return content
}

// TestProfileEndpointsAnswerAndBelong checks the shape of the profile endpoints: the reads carry no
// token, the writes belong to the account, and the fields are the two this server holds.
func TestProfileEndpointsAnswerAndBelong(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, _ := h.register(t, "profalice", "pw-profalice", "")
	_, bobToken, _ := h.register(t, "profbob", "pw-profbob", "")

	// A fresh account has a profile with nothing in it, and both fields are named: an absent key and an
	// empty one are the same fact to a client, and a response that sometimes omits one makes it guess.
	fresh := h.mustStatus(t, h.get("/_matrix/client/v3/profile/"+alice, ""), http.StatusOK).decoded(t)
	for _, key := range []string{"displayname", "avatar_url"} {
		value, present := fresh[key]
		if !present {
			t.Fatalf("a fresh profile omits %q: %v", key, fresh)
		}
		if value != "" {
			t.Errorf("a fresh profile carries %q = %v, want empty", key, value)
		}
	}

	// The write, then a read by somebody with no token at all: how a person is presented is what a
	// room's other members ask about, and they have no standing of their own to ask with.
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/profile/"+alice+"/displayname",
		map[string]any{"displayname": "Alice of the Hall"}, aliceToken), http.StatusOK)
	if got := h.mustStatus(t, h.get("/_matrix/client/v3/profile/"+alice+"/displayname", ""), http.StatusOK).decoded(t); got["displayname"] != "Alice of the Hall" {
		t.Errorf("an unauthenticated profile read: %v", got)
	}

	// The other field is untouched by a write that named only one of them.
	if both := h.mustStatus(t, h.get("/_matrix/client/v3/profile/"+alice, ""), http.StatusOK).decoded(t); both["avatar_url"] != "" {
		t.Errorf("writing the display name changed the avatar: %v", both)
	}

	// A value is stored as given. The specification puts the burden of a well-formed content URI on the
	// client and says a server MAY validate; this server declines to, and the reason is recorded next to
	// the code rather than rediscovered by a client whose avatar is a placeholder string.
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/profile/"+alice+"/avatar_url",
		map[string]any{"avatar_url": "LemurLover"}, aliceToken), http.StatusOK)
	if got := h.mustStatus(t, h.get("/_matrix/client/v3/profile/"+alice+"/avatar_url", ""), http.StatusOK).decoded(t); got["avatar_url"] != "LemurLover" {
		t.Errorf("an avatar_url that is not a content URI: %v", got)
	}

	// DELETE clears a field.
	h.mustStatus(t, h.raw(t, http.MethodDelete, "/_matrix/client/v3/profile/"+alice+"/avatar_url", nil, aliceToken), http.StatusOK)
	if got := h.mustStatus(t, h.get("/_matrix/client/v3/profile/"+alice+"/avatar_url", ""), http.StatusOK).decoded(t); got["avatar_url"] != "" {
		t.Errorf("deleting the avatar left a value behind: %v", got)
	}

	// The path names a user; the token names a user; only the token is a permission.
	refused := h.mustStatus(t, h.put(t, "/_matrix/client/v3/profile/"+alice+"/displayname",
		map[string]any{"displayname": "Someone else"}, bobToken), http.StatusForbidden).decoded(t)
	if refused["errcode"] != "M_FORBIDDEN" {
		t.Errorf("writing another user's profile: %v", refused)
	}
	if got := h.mustStatus(t, h.get("/_matrix/client/v3/profile/"+alice+"/displayname", ""), http.StatusOK).decoded(t); got["displayname"] != "Alice of the Hall" {
		t.Errorf("the refused write changed the stored name: %v", got)
	}
	if res := h.raw(t, http.MethodPut, "/_matrix/client/v3/profile/"+alice+"/displayname",
		[]byte(`{"displayname":"Anonymous"}`), ""); res.status != http.StatusUnauthorized {
		t.Errorf("an unauthenticated profile write: status %d (body %s), want 401", res.status, res.body)
	}

	// A body must name the field the path named: the target of the request is in the path, and a body
	// that points elsewhere makes the server guess.
	mismatched := h.mustStatus(t, h.put(t, "/_matrix/client/v3/profile/"+alice+"/displayname",
		map[string]any{"avatar_url": "mxc://example.com/abc"}, aliceToken), http.StatusBadRequest).decoded(t)
	if mismatched["errcode"] != "M_BAD_JSON" {
		t.Errorf("a profile body naming the other field: %v", mismatched)
	}
	if got := h.mustStatus(t, h.get("/_matrix/client/v3/profile/"+alice+"/avatar_url", ""), http.StatusOK).decoded(t); got["avatar_url"] != "" {
		t.Errorf("the mismatched body wrote the field the path did not name: %v", got)
	}

	// A field this server does not hold is refused as a field, not as an endpoint: the endpoint is
	// understood, and a client should be able to tell the two apart.
	unknown := h.mustStatus(t, h.put(t, "/_matrix/client/v3/profile/"+alice+"/org.example.field",
		map[string]any{"org.example.field": "x"}, aliceToken), http.StatusNotFound).decoded(t)
	if unknown["errcode"] != "M_NOT_FOUND" {
		t.Errorf("a profile field this server does not hold: %v", unknown)
	}
	if res := h.get("/_matrix/client/v3/profile/"+alice+"/org.example.field", ""); res.status != http.StatusNotFound {
		t.Errorf("reading a field this server does not hold: status %d (body %s)", res.status, res.body)
	}

	// An account this server does not have is not a profile with empty fields.
	if res := h.get("/_matrix/client/v3/profile/@nosuchaccount:hs1/displayname", aliceToken); res.status != http.StatusNotFound {
		t.Errorf("a profile for an account this server does not have: status %d (body %s), want 404",
			res.status, res.body)
	}

	// A user on another server is not this server's to describe. Answering from a member event it
	// happens to hold would present another server's word as if this one had checked it.
	if res := h.get("/_matrix/client/v3/profile/@someone:elsewhere/displayname", aliceToken); res.status != http.StatusNotFound {
		t.Errorf("a remote user's profile: status %d (body %s), want 404", res.status, res.body)
	}

	// A field longer than this server's own bound is refused. The bound is a decision, not a
	// specification rule, and it exists because a profile is copied into the member events of every room
	// the user stands in.
	long := make([]byte, 256)
	for i := range long {
		long[i] = 'x'
	}
	overLong := []byte(`{"displayname":"` + string(long) + `"}`)
	tooLarge := h.mustStatus(t, h.raw(t, http.MethodPut,
		"/_matrix/client/v3/profile/"+alice+"/displayname", overLong, aliceToken),
		http.StatusRequestEntityTooLarge).decoded(t)
	if tooLarge["errcode"] != "M_TOO_LARGE" {
		t.Errorf("an over-long profile field: %v", tooLarge)
	}
	if got := h.mustStatus(t, h.get("/_matrix/client/v3/profile/"+alice+"/displayname", ""), http.StatusOK).decoded(t); got["displayname"] != "Alice of the Hall" {
		t.Errorf("the refused over-long write changed the stored name: %v", got)
	}
}

// TestProfileChangeRestatesTheRooms checks the part a profile endpoint cannot do by itself: a room shows
// a person through the member event it holds, so changing a profile has to restate those events, in every
// room the user stands in, without touching anybody else's.
func TestProfileChangeRestatesTheRooms(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, _ := h.register(t, "restatealice", "pw-restatealice", "")
	bob, bobToken, _ := h.register(t, "restatebob", "pw-restatebob", "")

	first := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "first room"}, aliceToken), http.StatusOK).decoded(t)
	second := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "second room"}, bobToken), http.StatusOK).decoded(t)
	third := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "third room"}, bobToken), http.StatusOK).decoded(t)
	firstRoom, _ := first["room_id"].(string)
	secondRoom, _ := second["room_id"].(string)
	thirdRoom, _ := third["room_id"].(string)

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+secondRoom, map[string]any{}, aliceToken), http.StatusOK)

	// bob sees alice in the first room; his sync token is taken before the change so the restatement is
	// something his sync can be asked about.
	_, bobSince := syncNow(t, h, bobToken, "")

	h.mustStatus(t, h.put(t, "/_matrix/client/v3/profile/"+alice+"/displayname",
		map[string]any{"displayname": "Alice Renamed"}, aliceToken), http.StatusOK)

	for _, roomID := range []string{firstRoom, secondRoom} {
		content := memberContent(t, h, roomID, alice, aliceToken)
		if content["membership"] != "join" {
			t.Fatalf("the restatement in %s changed the membership: %v", roomID, content)
		}
		if content["displayname"] != "Alice Renamed" {
			t.Errorf("the member event in %s still says %v about %s", roomID, content["displayname"], alice)
		}
	}

	// The change reached bob as an event, not only as state: a client watching a room learns of it the
	// same way it learns of any member event. The room asked about is the one bob stands in — the other
	// one is alice's alone, so bob's sync would legitimately say nothing about it.
	body, _ := syncNow(t, h, bobToken, "&since="+bobSince)
	if !syncHasMemberWith(t, body, alice, secondRoom, "displayname", "Alice Renamed") {
		t.Fatalf("bob's sync after alice renamed herself carries no restated member event: %v", body)
	}

	// Nobody else's member event moved. The room asked about is the one both users stand in.
	if content := memberContent(t, h, secondRoom, bob, aliceToken); content["displayname"] == "Alice Renamed" {
		t.Errorf("alice's rename reached %s's member event: %v", bob, content)
	}

	// A room joined after the change carries the new name in the member event written at the join, so a
	// restatement is not the only path that has the current profile.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+thirdRoom, map[string]any{}, aliceToken), http.StatusOK)
	if content := memberContent(t, h, thirdRoom, alice, aliceToken); content["displayname"] != "Alice Renamed" {
		t.Errorf("the member event written at a later join says %v, want the current name: %v",
			content["displayname"], content)
	}

	// Clearing a field restates the rooms the same way, so a client stops showing a name rather than
	// holding on to one nobody is claiming.
	h.mustStatus(t, h.raw(t, http.MethodDelete, "/_matrix/client/v3/profile/"+alice+"/displayname", nil, aliceToken), http.StatusOK)
	if content := memberContent(t, h, firstRoom, alice, aliceToken); content["displayname"] != "" {
		t.Errorf("deleting the display name left %v in the member event: %v", content["displayname"], content)
	}
}

// syncHasMemberWith says whether a sync response carries a member event for a user in a room whose
// content names a value.
func syncHasMemberWith(t *testing.T, body map[string]any, userID, roomID, key, want string) bool {
	t.Helper()
	join := pathValue(t, body, "rooms", "join", roomID)
	for _, bucket := range []string{"timeline", "state"} {
		list, _ := pathValue(t, join, bucket, "events").([]any)
		for _, item := range list {
			event, _ := item.(map[string]any)
			if event["type"] != "m.room.member" || event["state_key"] != userID {
				continue
			}
			content, _ := event["content"].(map[string]any)
			if content != nil && content[key] == want {
				return true
			}
		}
	}
	return false
}

// pathValue walks a decoded JSON response without failing a test over a shape that simply is not there.
func pathValue(t *testing.T, root any, keys ...string) any {
	t.Helper()
	current := root
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return map[string]any{}
		}
		current = object[key]
	}
	if current == nil {
		return map[string]any{}
	}
	return current
}
