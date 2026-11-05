package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRoomServerACLRefusesTheExcludedServer: a room can decide it will not deal with a homeserver,
// and that decision has to be worth something at every door.
//
// The room here is federated before it is closed — the exclusion is a change made to a room the
// other server is already in, which is the shape that matters. Then each of the paths the
// specification names is asked: the join handshake, the room's state, an event pushed in a
// transaction, and the relay that decides which servers to tell about a new message. A room that
// refuses a server at one door and hands it the room at another has not excluded the server; it has
// expressed a preference.
func TestRoomServerACLRefusesTheExcludedServer(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "aclhost", "pw-aclhost")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "a room that closes its door"}, alice)
	roomID, _ := created["room_id"].(string)
	if roomID == "" {
		t.Fatal("the room has no ID")
	}
	bea := hs2.registerUser(t, "aclguest", "pw-aclguest")
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+roomID,
		map[string]any{}, bea); status != http.StatusOK {
		t.Fatalf("%s could not join %s before the exclusion: %d %v", hs2.name, roomID, status, answer)
	}

	// The room allows its own server and nobody else. This is the shape a room uses when it
	// decides that the servers it knows are the only ones it will deal with.
	setRoomACL(t, hs1, alice, roomID, map[string]any{"allow": []any{hs1.name}, "deny": []any{}})

	// The join handshake: the room will not even tell the excluded server what a join by one of
	// its users would look like.
	visitor := "@newcomer:" + hs2.name
	if status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/make_join/"+roomID+"/"+visitor, nil); status != http.StatusForbidden ||
		!strings.Contains(string(body), "M_FORBIDDEN") {
		t.Errorf("%s was answered make_join by the room that excludes it with %d (%s), wanting 403 M_FORBIDDEN",
			hs2.name, status, body)
	}
	if status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v3/make_join/"+roomID+"/"+visitor, nil); status != http.StatusForbidden {
		t.Errorf("%s was answered a version 3 make_join with %d (%s), wanting 403", hs2.name, status, body)
	}

	// The room's shape: state and authorisation are what a server needs to take part, so they are
	// the first things an exclusion has to withhold.
	if status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/state_ids/"+roomID+"?event_id=$nothing:"+hs1.name, nil); status != http.StatusForbidden {
		t.Errorf("%s was answered state_ids by the room that excludes it with %d (%s), wanting 403",
			hs2.name, status, body)
	}
	memberID := ""
	for _, event := range hs2.clientDoList(t, "/_matrix/client/v3/rooms/"+roomID+"/state", bea) {
		if stringAny(event, "type") == "m.room.member" && stringAny(event, "state_key") == "@aclguest:"+hs2.name {
			memberID = stringAny(event, "event_id")
		}
	}
	if memberID == "" {
		t.Fatalf("%s has no member event for its own user in the room", hs2.name)
	}
	if status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/event_auth/"+roomID+"/"+memberID, nil); status != http.StatusForbidden {
		t.Errorf("%s was answered event_auth by the room that excludes it with %d (%s), wanting 403",
			hs2.name, status, body)
	}
	// A single event, which is not on the specification's protected list but is a door into the
	// room all the same.
	if status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/event/"+memberID, nil); status != http.StatusForbidden {
		t.Errorf("%s was answered a read of one event by the room that excludes it with %d (%s), wanting 403",
			hs2.name, status, body)
	}

	// An event pushed by the excluded server is refused per event, and the transaction that
	// carried it is still answered: a room's decision about one server is not a reason to drop the
	// delivery channel.
	_, sent := hs2.clientDo(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/acl-from-hs2",
		map[string]any{"msgtype": "m.text", "body": "written after the exclusion"}, bea)
	pduID, _ := sent["event_id"].(string)
	if pduID == "" {
		t.Fatalf("%s could not write in the room from its own side", hs2.name)
	}
	if status, answer := hs2.sendTransaction(t, hs1, "acl-pushed-pdu",
		[]map[string]any{storedPDU(t, hs2, pduID)}); status != http.StatusOK {
		t.Fatalf("%s answered a transaction from the excluded server with %d, wanting an answer per event",
			hs1.name, status)
	} else if result, known := answer.PDUs[pduID]; !known {
		t.Errorf("%s did not answer the pushed event at all: %v", hs1.name, answer.PDUs)
	} else if result.Error == "" {
		t.Errorf("%s accepted an event from a server the room excludes", hs1.name)
	} else if !strings.Contains(result.Error, "excludes") {
		t.Errorf("%s refused the pushed event with %q, wanting the refusal to name the room's exclusion",
			hs1.name, result.Error)
	}

	// A message written on the resident server afterwards is not passed to the excluded server.
	// The exclusion says the room will not deal with that server; relaying to it is dealing with it.
	_, late := hs1.clientDo(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/acl-late",
		map[string]any{"msgtype": "m.text", "body": "not for the excluded server"}, alice)
	lateID, _ := late["event_id"].(string)
	if lateID == "" {
		t.Fatal("the resident server's own message has no ID")
	}
	time.Sleep(1500 * time.Millisecond)
	if row, err := hs2.app.DB.Events.ByID(context.Background(), lateID); err == nil && row != nil {
		t.Errorf("%s was handed a message of the room that excludes it; the relay is not honouring the exclusion",
			hs2.name)
	}

	// And the exclusion can be lifted: the room's decision is about now, not a verdict that
	// outlives the state that made it. The doors are checked again on the next request.
	setRoomACL(t, hs1, alice, roomID, map[string]any{"allow": []any{"*"}, "deny": []any{}})
	if status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/make_join/"+roomID+"/"+visitor, nil); status != http.StatusOK {
		t.Errorf("%s was still refused a make_join after the room lifted its exclusion: %d (%s)",
			hs2.name, status, body)
	}
	if status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/state_ids/"+roomID+"?event_id="+memberID, nil); status != http.StatusOK {
		t.Errorf("%s was still refused state_ids after the room lifted its exclusion: %d (%s)",
			hs2.name, status, body)
	}
	// What the exclusion withheld stays missing, and is not silently forgiven: the messages sent
	// while the door was shut are not in this server's copy of the room, and a message that
	// follows them cites events this server has never been told about. Catching up on them is the
	// bounded missing-event ask of T-4009b2; what this test pins is that the exclusion neither
	// leaks during nor hardens after.
	if row, err := hs2.app.DB.Events.ByID(context.Background(), lateID); err == nil && row != nil {
		t.Errorf("%s received the message that was withheld during the exclusion", hs2.name)
	}
}

// setRoomACL writes a room's server ACL as one of its own users would.
func setRoomACL(t *testing.T, node *fedNode, token, roomID string, content map[string]any) {
	t.Helper()
	status, answer := node.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.server_acl", content, token)
	if status != http.StatusOK {
		t.Fatalf("%s could not set a server ACL on %s: %d %v", node.name, roomID, status, answer)
	}
}

// storedPDU returns the bytes a server holds for an event, in the form its signatures cover.
func storedPDU(t *testing.T, node *fedNode, eventID string) map[string]any {
	t.Helper()
	row, err := node.app.DB.Events.ByID(context.Background(), eventID)
	if err != nil || row == nil {
		t.Fatalf("%s does not hold the event %s it is meant to send", node.name, eventID)
	}
	var event map[string]any
	if err := json.Unmarshal(row.JSON, &event); err != nil {
		t.Fatalf("%s's copy of %s is not readable: %v", node.name, eventID, err)
	}
	return event
}
