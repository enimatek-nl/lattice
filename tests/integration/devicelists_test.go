package integration

import (
	"net/http"
	"testing"
)

// TestDeviceListsTrackMembership checks the lists a client uses to decide whose keys to
// re-fetch, and whose to forget.
//
// The point of the room in this test is that it is the only thing that decides who hears
// about a change: the member list of a room is no use for the question, because the user
// who left is no longer in it.
func TestDeviceListsTrackMembership(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	aliceID, aliceToken, _ := h.register(t, "devlistalice", "pw-devlistalice", "")
	charlie, charlieToken, _ := h.register(t, "devlistcharlie", "pw-devlistcharlie", "")
	_, bobToken, _ := h.register(t, "devlistbob", "pw-devlistbob", "")

	created := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, aliceToken), http.StatusOK).decoded(t)
	roomID, _ := created["room_id"].(string)
	// bob is only here to be somebody who shares no room with charlie.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, bobToken), http.StatusOK)

	_, aliceSince := syncNow(t, h, aliceToken, "")
	_, charlieSince := syncNow(t, h, charlieToken, "")
	_, bobSince := syncNow(t, h, bobToken, "")

	// A join is a user whose keys are worth asking for again.
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, charlieToken), http.StatusOK)
	body, next := syncNow(t, h, aliceToken, "&since="+aliceSince)
	if !inDeviceListSection(t, body, "changed", charlie) {
		t.Fatalf("charlie's join is not in alice's device_lists.changed: %v", body["device_lists"])
	}

	// The joiner is told about their own arrival, and this is the half that a single-device
	// mental model gets wrong. The device that sent the join already knows what it did; the
	// phone in the other room does not, and it has no other route to the fact that this user now
	// shares a room with new servers and that their keys need re-publishing, because a device
	// list is not an event in any timeline. Complement checks the same thing from the other side
	// (TestDeviceListsUpdateOverFederation expects a user in their own device_lists.changed).
	if mine, _ := syncNow(t, h, charlieToken, "&since="+charlieSince); !inDeviceListSection(t, mine, "changed", charlie) {
		t.Errorf("charlie was not told about their own device list: %v", mine["device_lists"])
	}

	// The leaver's own token, taken before the leave, so that his side of the notice can be
	// checked: he must be told that the people still in that room are no longer his to
	// track. A record shelved in the room could not reach him, because he has left it.
	_, charlieBeforeLeave := syncNow(t, h, charlieToken, "")
	// Leaving is a user whose keys are no longer worth keeping.
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/devlisttxn1",
		map[string]any{"msgtype": "m.text", "body": "was here"}, charlieToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, charlieToken), http.StatusOK)

	after, _ := syncNow(t, h, aliceToken, "&since="+next)
	if !inDeviceListSection(t, after, "left", charlie) {
		t.Fatalf("charlie's leave is not in alice's device_lists.left: %v", after["device_lists"])
	}
	// The same token answers the same thing: a client that retries must not be told the
	// user came back.
	repeat, _ := syncNow(t, h, aliceToken, "&since="+next)
	if !inDeviceListSection(t, repeat, "left", charlie) {
		t.Errorf("asking again from the same token lost the leave: %v", repeat["device_lists"])
	}

	if mine, _ := syncNow(t, h, charlieToken, "&since="+charlieBeforeLeave); !inDeviceListSection(t, mine, "left", aliceID) {
		t.Errorf("charlie was not told that alice is no longer his to track: %v", mine["device_lists"])
	}

	// A user who shares no room with charlie is not told about him.
	outsider, _ := syncNow(t, h, bobToken, "&since="+bobSince)
	if section, _ := outsider["device_lists"].(map[string]any); section != nil {
		if inDeviceListSection(t, outsider, "changed", charlie) || inDeviceListSection(t, outsider, "left", charlie) {
			t.Errorf("bob, who shares no room with charlie, was told about him: %v", section)
		}
	}
}

// TestDeviceListsAbsentWhenNothingChanged checks that the section is absent rather than
// empty when there is nothing to report: an empty object reads as "nothing changed", which
// is a different answer.
func TestDeviceListsAbsentWhenNothingChanged(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "quietalice", "pw-quietalice", "")
	_, bobToken, _ := h.register(t, "quietbob", "pw-quietbob", "")
	// bob registers and does nothing: registering is not a room event, and a user who
	// shares no room is not somebody's device list news.
	_ = bobToken

	_, next := syncNow(t, h, aliceToken, "")
	// Nothing happens in the room; an incremental sync says so by leaving the section out.
	second, _ := syncNow(t, h, aliceToken, "&since="+next)
	if section, present := second["device_lists"]; present {
		t.Errorf("an idle sync reported device lists anyway: %v", section)
	}
	// And no room is reported in it: the section is there because the response shape
	// always has one, and it is empty because nothing happened.
	if rooms, _ := second["rooms"].(map[string]any); len(rooms) != 0 {
		t.Errorf("an idle sync reported rooms: %v", rooms)
	}
}

// inDeviceListSection reports whether a sync response lists a user in one of the two
// device list sections.
func inDeviceListSection(t *testing.T, body map[string]any, section, userID string) bool {
	t.Helper()
	lists, _ := body["device_lists"].(map[string]any)
	values, _ := lists[section].([]any)
	for _, item := range values {
		if name, _ := item.(string); name == userID {
			return true
		}
	}
	return false
}
