package integration

import (
	"net/http"
	"testing"
)

// Account data the client keeps about one room, and what a removal means.
//
// Room account data is not room state: a tag, a DM flag, an annotation — the user's own note
// about one room, which no other member reads and no server agrees. What makes it worth its
// own endpoints is that a client has to be able to read it back without syncing, and then be
// told about changes by sync, like everything else.
//
// MSC3391 is the part with a decision in it. A PUT whose content is an empty object is a
// removal, and a removal is an answer rather than an absence: a client that already holds the
// value has to be told to forget it, which it can only do if the incremental sync carries the
// type with an empty content object. An initial sync omits the type entirely, because there is
// nothing there to forget. Deleting the row instead of recording the deletion makes the first
// case impossible, and a client that had read the value keeps it forever.

func TestRoomAccountDataRoundTrip(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "annotator", "pw-annotator", "")

	first := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat"})
	second := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat"})

	roomPath := func(room string) string {
		return "/_matrix/client/v3/rooms/" + room + "/account_data/site.note"
	}

	h.mustStatus(t, h.put(t, roomPath(first), map[string]any{"value": "room first"}, aliceToken), http.StatusOK)
	// The same type name in another room is another piece of data. The room is part of the
	// identity of the value, not a folder it happens to sit in.
	h.mustStatus(t, h.put(t, roomPath(second), map[string]any{"value": "other room"}, aliceToken), http.StatusOK)

	if got := h.mustStatus(t, h.get(roomPath(first), aliceToken), http.StatusOK).decoded(t); got["value"] != "room first" {
		t.Errorf("room account data read back as %v, wanting the value written to this room", got)
	}
	if got := h.mustStatus(t, h.get(roomPath(second), aliceToken), http.StatusOK).decoded(t); got["value"] != "other room" {
		t.Errorf("the second room's %q was overwritten by the first's: %v", "site.note", got)
	}

	// Writing the same type in the same room replaces it; it is a current value, not a history.
	h.mustStatus(t, h.put(t, roomPath(first), map[string]any{"value": "room second"}, aliceToken), http.StatusOK)
	if got := h.mustStatus(t, h.get(roomPath(first), aliceToken), http.StatusOK).decoded(t); got["value"] != "room second" {
		t.Errorf("room account data after a second write = %v, wanting the newest value", got)
	}

	// And it arrives down /sync, in the room it belongs to.
	since := syncToken(t, h, aliceToken, "")
	h.mustStatus(t, h.put(t, roomPath(first), map[string]any{"value": "synced"}, aliceToken), http.StatusOK)
	events := roomAccountDataFromSync(t, h, aliceToken, since, first)
	if len(events) != 1 || eventString(events[0], "type") != "site.note" {
		t.Fatalf("the room's account_data section after a write: %v", events)
	}
	if content, _ := events[0]["content"].(map[string]any); content["value"] != "synced" {
		t.Errorf("the sync carried the wrong room account data: %v", events[0])
	}
}

// TestAccountDataRemovalIsAnAnswer covers MSC3391 in both dialects and both scopes: an empty
// PUT and a DELETE both remove, the read then answers 404, an incremental sync carries the
// type with an empty body, and an initial sync does not mention it at all.
func TestAccountDataRemovalIsAnAnswer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, _ := h.register(t, "editor", "pw-editor", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat"})

	userPath := "/_matrix/client/v3/user/" + alice + "/account_data/site.preference"
	roomPath := "/_matrix/client/v3/rooms/" + roomID + "/account_data/site.note"

	for _, tc := range []struct {
		name   string
		scope  string
		remove func()
	}{
		{"empty PUT of user account data", "user", func() {
			h.mustStatus(t, h.put(t, userPath, map[string]any{}, aliceToken), http.StatusOK)
		}},
		{"DELETE of user account data", "user", func() {
			h.mustStatus(t, h.raw(t, http.MethodDelete, userPath, nil, aliceToken), http.StatusOK)
		}},
		{"empty PUT of room account data", "room", func() {
			h.mustStatus(t, h.put(t, roomPath, map[string]any{}, aliceToken), http.StatusOK)
		}},
		{"DELETE of room account data", "room", func() {
			h.mustStatus(t, h.raw(t, http.MethodDelete, roomPath, nil, aliceToken), http.StatusOK)
		}},
		{"DELETE under the MSC3391 unstable path", "user", func() {
			unstable := "/_matrix/client/unstable/org.matrix.msc3391/user/" + alice + "/account_data/site.preference"
			h.mustStatus(t, h.raw(t, http.MethodDelete, unstable, nil, aliceToken), http.StatusOK)
		}},
		{"DELETE of room data under the MSC3391 unstable path", "room", func() {
			unstable := "/_matrix/client/unstable/org.matrix.msc3391/user/" + alice +
				"/rooms/" + roomID + "/account_data/site.note"
			h.mustStatus(t, h.raw(t, http.MethodDelete, unstable, nil, aliceToken), http.StatusOK)
		}},
	} {
		// Write it, read it back, take a token from here, then remove it.
		path := userPath
		if tc.scope == "room" {
			path = roomPath
		}
		h.mustStatus(t, h.put(t, path, map[string]any{"value": "set"}, aliceToken), http.StatusOK)
		h.mustStatus(t, h.get(path, aliceToken), http.StatusOK)

		since := syncToken(t, h, aliceToken, "")
		tc.remove()

		// The read says it is gone.
		res := h.get(path, aliceToken)
		h.mustStatus(t, res, http.StatusNotFound)

		// A client that follows up with an initial sync has nothing to forget, so the type
		// must not appear in it at all.
		initial := h.mustStatus(t, h.get("/_matrix/client/v3/sync", aliceToken), http.StatusOK).decoded(t)
		if accountDataTypePresent(initial, "account_data", "", "site.preference") && tc.scope == "user" {
			t.Errorf("%s: the removed type is still in an initial sync", tc.name)
		}
		if tc.scope == "room" && accountDataTypePresent(initial, "account_data", roomID, "site.note") {
			t.Errorf("%s: the removed room type is still in an initial sync", tc.name)
		}

		// And the incremental sync says so, with an empty body: that is what makes a client
		// drop a value it already holds.
		delta := h.mustStatus(t, h.get("/_matrix/client/v3/sync?since="+since, aliceToken), http.StatusOK).decoded(t)
		var events []map[string]any
		if tc.scope == "user" {
			events = accountDataEvents(delta, "account_data", "")
		} else {
			events = accountDataEvents(delta, "account_data", roomID)
		}
		found := false
		for _, event := range events {
			if eventString(event, "type") != accountTypeName(tc.scope) {
				continue
			}
			content, ok := event["content"].(map[string]any)
			if !ok || len(content) != 0 {
				t.Errorf("%s: the incremental sync carries the removal with %v, wanting an empty object", tc.name, event["content"])
			}
			found = true
		}
		if !found {
			t.Errorf("%s: the incremental sync never mentioned the removal: %v", tc.name, events)
		}
	}
}

func accountTypeName(scope string) string {
	if scope == "room" {
		return "site.note"
	}
	return "site.preference"
}

// syncToken runs one sync and returns its next_batch.
func syncToken(t *testing.T, h *harness, token, since string) string {
	t.Helper()
	path := "/_matrix/client/v3/sync"
	if since != "" {
		path += "?since=" + since
	}
	body := h.mustStatus(t, h.get(path, token), http.StatusOK).decoded(t)
	next, _ := body["next_batch"].(string)
	if next == "" {
		t.Fatalf("the sync returned no next_batch: %v", body)
	}
	return next
}

// roomAccountDataFromSync reads one joined room's account_data section out of a sync made after
// `since`.
func roomAccountDataFromSync(t *testing.T, h *harness, token, since, roomID string) []map[string]any {
	t.Helper()
	body := h.mustStatus(t, h.get("/_matrix/client/v3/sync?since="+since, token), http.StatusOK).decoded(t)
	return accountDataEvents(body, "account_data", roomID)
}

// accountDataEvents digs out the account_data events of one scope: an empty roomID is the top
// level of the response, any other is that room inside rooms.join.
func accountDataEvents(body map[string]any, field, roomID string) []map[string]any {
	scope := body
	if roomID != "" {
		rooms, _ := body["rooms"].(map[string]any)
		join, _ := rooms["join"].(map[string]any)
		one, _ := join[roomID].(map[string]any)
		if one == nil {
			return nil
		}
		scope = one
	}
	section, _ := scope[field].(map[string]any)
	if section == nil {
		return nil
	}
	raw, _ := section["events"].([]any)
	events := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if event, ok := item.(map[string]any); ok {
			events = append(events, event)
		}
	}
	return events
}

func accountDataTypePresent(body map[string]any, field, roomID, dataType string) bool {
	for _, event := range accountDataEvents(body, field, roomID) {
		if eventString(event, "type") == dataType {
			return true
		}
	}
	return false
}

func eventString(event map[string]any, key string) string {
	value, _ := event[key].(string)
	return value
}
