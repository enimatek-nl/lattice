package integration

import (
	"net/http"
	"testing"
)

// TestLoginIntoAnExistingDeviceReusesIt checks what happens when a client asks for a device ID
// its account already has.
//
// A device ID names a device, not a session. Signing into the same device again is the same
// device signing in — so it keeps its identity, and with it the transaction IDs it has already
// used, its display name and the other access tokens it holds. Handing out a second device under
// one name would either collide in the store or split one device's history in two, and a client
// that re-logs in to recover a session would find its idempotency gone.
func TestLoginIntoAnExistingDeviceReusesIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	userID, firstToken, deviceID := h.register(t, "wren", "pw-wren", "")
	if deviceID == "" {
		t.Fatal("registration returned no device ID")
	}

	second := h.mustStatus(t, h.post(t, "/_matrix/client/v3/login", map[string]any{
		"type":      "m.login.password",
		"user":      userID,
		"password":  "pw-wren",
		"device_id": deviceID,
	}, ""), http.StatusOK).decoded(t)
	if second["device_id"] != deviceID {
		t.Fatalf("login returned device_id %v, want the device asked for (%s)", second["device_id"], deviceID)
	}
	secondToken, _ := second["access_token"].(string)
	if secondToken == "" {
		t.Fatal("login returned no access token")
	}

	// Both tokens belong to that device, so both are still usable: re-logging in is not a
	// logout of the sessions the device already has.
	for _, token := range []string{firstToken, secondToken} {
		who := h.mustStatus(t, h.get("/_matrix/client/v3/account/whoami", token), http.StatusOK).decoded(t)
		if who["user_id"] != userID || who["device_id"] != deviceID {
			t.Errorf("whoami = %v, want %s on %s", who, userID, deviceID)
		}
	}

	// And the account still has one device, not two under one name.
	devices := h.mustStatus(t, h.get("/_matrix/client/v3/devices", firstToken), http.StatusOK).decoded(t)
	list, _ := devices["devices"].([]any)
	if len(list) != 1 {
		t.Fatalf("the account has %d devices, want one: %v", len(list), list)
	}

	// A device name belongs to an account, not to the whole server: another account may use the
	// same name, and doing so gives it a device of its own.
	otherID, otherToken, otherDevice := h.register(t, "vic", "pw-vic", "")
	if otherDevice == "" || otherDevice == deviceID {
		t.Fatalf("unexpected device IDs: %q and %q", deviceID, otherDevice)
	}
	mine := h.mustStatus(t, h.post(t, "/_matrix/client/v3/login", map[string]any{
		"type":      "m.login.password",
		"user":      userID,
		"password":  "pw-wren",
		"device_id": otherDevice,
	}, ""), http.StatusOK).decoded(t)
	if mine["device_id"] != otherDevice || mine["user_id"] != userID {
		t.Fatalf("login with a name another account uses = %v, want %s on %s", mine, userID, otherDevice)
	}
	// Neither account sees the other's device: the name is the same, the devices are not.
	who := h.mustStatus(t, h.get("/_matrix/client/v3/account/whoami", otherToken), http.StatusOK).decoded(t)
	if who["user_id"] != otherID {
		t.Errorf("%s's session now reports %v", otherID, who)
	}
	otherList := h.mustStatus(t, h.get("/_matrix/client/v3/devices", otherToken), http.StatusOK).decoded(t)
	if entries, _ := otherList["devices"].([]any); len(entries) != 1 {
		t.Errorf("%s has %v devices, want its own one", otherID, entries)
	}

	// The device the name refers to is what transaction IDs are scoped to, so a send from the
	// second session with a transaction ID the first session used answers with the first event.
	roomID := createRoom(t, h, firstToken, map[string]any{})
	first := h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/txnscope", map[string]any{
		"msgtype": "m.text", "body": "one",
	}, firstToken), http.StatusOK).decoded(t)
	again := h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/txnscope", map[string]any{
		"msgtype": "m.text", "body": "two",
	}, secondToken), http.StatusOK).decoded(t)
	if first["event_id"] != again["event_id"] {
		t.Errorf("one device across two sessions: %v and %v want the same event", first, again)
	}
}

// TestTransactionIDScopesToTheRoom checks that a transaction ID is a device's name for one
// attempt in one room, not a global sequence of its sends.
//
// A client that puts the same name on a send to each of two rooms is making two requests. A
// server that answers the second from the first has dropped a message and told the client it
// sent one.
func TestTransactionIDScopesToTheRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, token, _ := h.register(t, "uma", "pw-uma", "")
	firstRoom := createRoom(t, h, token, map[string]any{})
	secondRoom := createRoom(t, h, token, map[string]any{})

	inFirst := h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+firstRoom+"/send/m.room.message/shared", map[string]any{
		"msgtype": "m.text", "body": "in the first room",
	}, token), http.StatusOK).decoded(t)
	inSecond := h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+secondRoom+"/send/m.room.message/shared", map[string]any{
		"msgtype": "m.text", "body": "in the second room",
	}, token), http.StatusOK).decoded(t)
	if inFirst["event_id"] == inSecond["event_id"] {
		t.Fatalf("one transaction ID answered two rooms with one event: %v / %v", inFirst, inSecond)
	}

	// Each room keeps its own answer for that name, and a repeat inside a room still returns the
	// event it already produced.
	repeat := h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+secondRoom+"/send/m.room.message/shared", map[string]any{
		"msgtype": "m.text", "body": "a different body",
	}, token), http.StatusOK).decoded(t)
	if repeat["event_id"] != inSecond["event_id"] {
		t.Errorf("a repeat in the same room produced a new event: %v vs %v", repeat, inSecond)
	}
	history := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+firstRoom+"/messages?limit=10", token), http.StatusOK).decoded(t)
	chunk, _ := history["chunk"].([]any)
	var bodies []string
	for _, entry := range chunk {
		event, _ := entry.(map[string]any)
		content, _ := event["content"].(map[string]any)
		if body, _ := content["body"].(string); body != "" {
			bodies = append(bodies, body)
		}
	}
	if len(bodies) != 1 || bodies[0] != "in the first room" {
		t.Errorf("the first room holds %v, only the message sent to it", bodies)
	}
}

// TestSentEventCarriesItsTransactionID checks that a client can recognise its own send later.
//
// A transaction ID is the name a device gave a request. It is how a client reading a timeline
// spots the event it was waiting for, and on a device with several sessions how it tells which of
// them made the request. It is not part of what the event's signature covers, and it does not
// travel to other homeservers: a name chosen here says nothing about what happened elsewhere.
func TestSentEventCarriesItsTransactionID(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, token, _ := h.register(t, "nyx", "pw-nyx", "")
	roomID := createRoom(t, h, token, map[string]any{})

	sent := h.mustStatus(t, send(t, h, roomID, "m.room.message", "note-1",
		`{"msgtype":"m.text","body":"mine"}`, token), http.StatusOK).decoded(t)
	eventID, _ := sent["event_id"].(string)
	if eventID == "" {
		t.Fatalf("send returned no event ID: %v", sent)
	}

	served := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+eventID, token), http.StatusOK).decoded(t)
	unsigned, _ := served["unsigned"].(map[string]any)
	if unsigned["transaction_id"] != "note-1" {
		t.Errorf("GET /event reports unsigned %#v, want transaction_id note-1", unsigned)
	}

	body, _ := syncNow(t, h, token, "")
	room := roomSection(t, body, "join", roomID)
	found := false
	for _, entry := range eventsOfType(t, room, "m.room.message") {
		if entry["event_id"] != eventID {
			continue
		}
		found = true
		synced, _ := entry["unsigned"].(map[string]any)
		if synced["transaction_id"] != "note-1" {
			t.Errorf("/sync reports unsigned %#v, want transaction_id note-1", synced)
		}
	}
	if !found {
		t.Fatalf("/sync does not include the event %s: %v", eventID, room)
	}

	// An event that did not arrive through a named request has no such name. The room's own
	// creation is the obvious one, and an empty field there would be a value invented to fill
	// space.
	create := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.create?format=event", token), http.StatusOK).decoded(t)
	if unsigned, ok := create["unsigned"].(map[string]any); ok {
		if _, present := unsigned["transaction_id"]; present {
			t.Errorf("m.room.create carries a transaction ID: %v", create)
		}
	}
}
