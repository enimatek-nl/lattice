package integration

import (
	"fmt"
	"net/http"
	"testing"
)

// TestAReadReceiptReachesTheUsersSync moves a read position and asks what the same user's next
// sync says. A receipt is not an event — it has no ID and no place in the room's graph — so it
// has to arrive somewhere else in the response, and the place the specification gives it is the
// room's ephemeral section.
func TestAReadReceiptReachesTheUsersSync(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, token, _ := h.register(t, "reader", "pw-reader", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})
	_, since := syncNow(t, h, token, "")
	eventID := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-read",
		`{"msgtype":"m.text","body":"read me"}`, token), http.StatusOK).decoded(t)["event_id"].(string)

	res := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+eventID, map[string]any{}, token)
	if res.status != http.StatusOK {
		t.Fatalf("posting a read receipt: status %d (body %s)", res.status, res.body)
	}
	if len(res.decoded(t)) != 0 {
		t.Errorf("the receipt endpoint answered %s, want {}", res.body)
	}

	body, nextToken := syncNow(t, h, token, "&since="+since)
	receipts := ephemeralReceipts(t, h, body, roomID)
	if len(receipts) == 0 {
		t.Fatalf("the incremental sync carries no m.receipt for the room: %v",
			roomSection(t, body, "join", roomID)["ephemeral"])
	}
	for _, receipt := range receipts {
		if receipt["type"] != "m.receipt" {
			t.Errorf("the ephemeral event is %v, want type m.receipt", receipt["type"])
		}
		if _, found := receipt["room_id"]; found {
			t.Errorf("a client-facing receipt carries a room_id; the room's section already says which room: %v", receipt)
		}
		if _, found := receipt["event_id"]; found {
			t.Errorf("a receipt was given an event ID; it is not an event: %v", receipt)
		}
	}
	if !carriesReceipt(receipts, eventID, "m.read", alice) {
		t.Errorf("no receipt in %v names %s read by %s", receipts, eventID, alice)
	}

	// A first sync has no window, and the current answer is what a client needs then.
	first, _ := syncNow(t, h, token, "")
	if !carriesReceipt(ephemeralReceipts(t, h, first, roomID), eventID, "m.read", alice) {
		t.Errorf("a first sync does not carry the read position: %v",
			first["rooms"])
	}

	// A receipt that has not moved is not repeated on a later sync: an ephemeral event speaks
	// about what changed inside the window, and this window holds nothing.
	later, _ := syncNow(t, h, token, "&since="+nextToken)
	if again := ephemeralReceipts(t, h, later, roomID); len(again) != 0 {
		t.Errorf("the read position was repeated in a window that holds no change to it: %v", again)
	}
}

// TestReceiptsAreRefusedForWhatTheyAreNot checks the door: an event this server has never held,
// an event from a different room, and a receipt kind no client will look for.
func TestReceiptsAreRefusedForWhatTheyAreNot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "poster", "pw-poster", "")
	_, otherToken, _ := h.register(t, "outsider", "pw-outsider", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	otherRoom := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	eventID := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-here",
		`{"msgtype":"m.text","body":"in this room"}`, aliceToken), http.StatusOK).decoded(t)["event_id"].(string)

	for _, bad := range []struct {
		what        string
		path        string
		token       string
		wantStatus  int
		wantErrCode string
	}{
		{"an event this server has never held", "/receipt/m.read/$notthereevent00000000000000000000000000A", aliceToken, http.StatusNotFound, "M_NOT_FOUND"},
		{"an event of a different room", "/receipt/m.read/" + otherRoomEvent(t, h, otherRoom, aliceToken), aliceToken, http.StatusNotFound, "M_NOT_FOUND"},
		{"a receipt kind with no dot in it", "/receipt/readreceipt/" + eventID, aliceToken, http.StatusBadRequest, "M_INVALID_PARAM"},
		{"an event ID that is not one", "/receipt/m.read/notanevent", aliceToken, http.StatusBadRequest, "M_INVALID_PARAM"},
		{"a room the caller is not in", "/receipt/m.read/" + eventID, otherToken, http.StatusForbidden, "M_FORBIDDEN"},
	} {
		res := h.post(t, "/_matrix/client/v3/rooms/"+roomID+bad.path, map[string]any{}, bad.token)
		if res.status != bad.wantStatus {
			t.Errorf("%s: status %d, want %d (body %s)", bad.what, res.status, bad.wantStatus, res.body)
		}
		if code := res.errcode(t); code != bad.wantErrCode {
			t.Errorf("%s answered %s, want %s (body %s)", bad.what, code, bad.wantErrCode, res.body)
		}
	}

	// A custom, dotted receipt kind is the specification's to allow, and this server stores and
	// delivers it rather than refusing it.
	custom := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/receipt/com.example.seen/"+eventID,
		map[string]any{}, aliceToken)
	if custom.status != http.StatusOK {
		t.Errorf("a custom receipt kind: status %d (body %s)", custom.status, custom.body)
	}
}

// TestReadMarkersMoveBothPositionsAndBookmarkTheRoom covers the call that moves a read receipt
// and the caller's own bookmark in one step, and where each of the two ends up.
func TestReadMarkersMoveBothPositionsAndBookmarkTheRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, token, _ := h.register(t, "marker", "pw-marker", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})
	_, since := syncNow(t, h, token, "")
	eventID := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-mark",
		`{"msgtype":"m.text","body":"bookmark me"}`, token), http.StatusOK).decoded(t)["event_id"].(string)

	res := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", map[string]any{
		"m.fully_read": eventID,
		"m.read":       eventID,
	}, token)
	if res.status != http.StatusOK {
		t.Fatalf("read_markers: status %d (body %s)", res.status, res.body)
	}

	body, _ := syncNow(t, h, token, "&since="+since)
	room := roomSection(t, body, "join", roomID)
	if !carriesReceipt(ephemeralReceipts(t, h, body, roomID), eventID, "m.read", alice) {
		t.Errorf("read_markers did not move the read receipt: %v", room["ephemeral"])
	}
	if !accountDataHas(t, room, "m.fully_read", eventID) {
		t.Errorf("read_markers did not record the bookmark in the room's account data: %v", room["account_data"])
	}

	// A call that names nothing is a bad request rather than a way to lose a marker.
	if empty := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", map[string]any{}, token); empty.status != http.StatusBadRequest {
		t.Errorf("read_markers naming nothing: status %d, want 400 (body %s)", empty.status, empty.body)
	}
	// A bookmark pointing at a room's stranger is refused.
	unknown := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", map[string]any{
		"m.fully_read": "$notthereevent00000000000000000000000000A",
	}, token)
	if unknown.status != http.StatusNotFound {
		t.Errorf("a bookmark for an unknown event: status %d, want 404 (body %s)", unknown.status, unknown.body)
	}
}

// otherRoomEvent sends and returns an event in another room, for the check that a receipt cannot
// point across rooms.
func otherRoomEvent(t *testing.T, h *harness, roomID, token string) string {
	t.Helper()
	return h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-there",
		`{"msgtype":"m.text","body":"in another room"}`, token), http.StatusOK).decoded(t)["event_id"].(string)
}

// ephemeralReceipts reads the m.receipt ephemeral events of one joined room out of a sync
// response.
func ephemeralReceipts(t *testing.T, h *harness, body map[string]any, roomID string) []map[string]any {
	t.Helper()
	room := roomSection(t, body, "join", roomID)
	ephemeral, _ := room["ephemeral"].(map[string]any)
	entries, _ := ephemeral["events"].([]any)
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		event, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("an ephemeral entry is not an object: %v", entry)
		}
		if event["type"] == "m.receipt" {
			out = append(out, event)
		}
	}
	return out
}

// carriesReceipt reports whether one of the receipts says this user read that event under that
// kind.
func carriesReceipt(receipts []map[string]any, eventID, receiptType, userID string) bool {
	for _, receipt := range receipts {
		content, _ := receipt["content"].(map[string]any)
		kinds, _ := content[eventID].(map[string]any)
		readers, _ := kinds[receiptType].(map[string]any)
		if entry, ok := readers[userID].(map[string]any); ok {
			if ts, isNumber := entry["ts"].(float64); isNumber && ts > 0 {
				return true
			}
			return true
		}
	}
	return false
}

// accountDataHas reports whether the room's account data holds a value of that type naming that
// event.
func accountDataHas(t *testing.T, room map[string]any, dataType, eventID string) bool {
	section, _ := room["account_data"].(map[string]any)
	entries, _ := section["events"].([]any)
	for _, entry := range entries {
		event, _ := entry.(map[string]any)
		if event["type"] != dataType {
			continue
		}
		content, _ := event["content"].(map[string]any)
		if got, _ := content["event_id"].(string); got == eventID {
			return fmt.Sprint(event["type"]) == dataType
		}
	}
	return false
}

// TestASecondReceiptReplacesTheFirst moves a read position twice. A receipt is one current
// statement per user per room, so the second move has to replace the first rather than stack up
// beside it — and the row being updated is one the server has to be able to find.
func TestASecondReceiptReplacesTheFirst(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "twice", "pw-twice", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})
	first := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-one",
		`{"msgtype":"m.text","body":"first"}`, token), http.StatusOK).decoded(t)["event_id"].(string)
	second := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-two",
		`{"msgtype":"m.text","body":"second"}`, token), http.StatusOK).decoded(t)["event_id"].(string)

	for _, eventID := range []string{first, second, first} {
		res := h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+eventID,
			map[string]any{}, token)
		if res.status != http.StatusOK {
			t.Fatalf("moving the read position to %s: status %d (body %s)", eventID, res.status, res.body)
		}
	}

	// A first sync answers the current position, and there is one of it: the walk back to
	// `first` replaced the move to `second` rather than adding a third receipt.
	body, _ := syncNow(t, h, token, "")
	receipts := ephemeralReceipts(t, h, body, roomID)
	if len(receipts) != 1 {
		t.Fatalf("the room carries %d receipts, want one current position: %v", len(receipts), receipts)
	}
	if !carriesReceipt(receipts, first, "m.read", "@twice:hs1") {
		t.Errorf("the current read position is not the last one set: %v", receipts)
	}
	if carriesReceipt(receipts, second, "m.read", "@twice:hs1") {
		t.Errorf("a superseded read position is still being reported: %v", receipts)
	}
}
