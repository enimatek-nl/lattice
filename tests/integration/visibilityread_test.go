package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestFederationEventReadHonoursHistoryVisibility asks one homeserver for a single event by name,
// the way a peer does with GET /_matrix/federation/v1/event/{eventID}, and checks that the answer
// carries what the asking server was entitled to and no more.
//
// Naming an event is not a credential for reading it. A room whose history visibility at the moment
// of the event was `joined` (or `invited`) hid that event from the asking server's users, so the
// event goes over with its body taken out — and it still goes over, with its predecessors, its auth
// events and its signatures intact, because the peer has to be able to place and verify the event in
// order to know that it is missing something. The second room is the other half of the rule: a
// `shared` room shares its history backwards, so the same request for an event written before the
// asker arrived returns the body, and a server that trimmed everything out of caution would break
// the rooms that were opened.

func TestFederationEventReadHonoursHistoryVisibility(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "hostess", "pw-hostess")
	hiddenRoom := fedCreateRoom(t, hs1, alice, map[string]any{
		"preset": "public_chat",
		"name":   "history hidden from latecomers",
		"initial_state": []map[string]any{
			{"type": "m.room.history_visibility", "state_key": "",
				"content": map[string]any{"history_visibility": "joined"}},
		},
	})
	openRoom := fedCreateRoom(t, hs1, alice, map[string]any{
		"preset": "public_chat",
		"name":   "history shared with latecomers",
	})

	hiddenMessage := fedSendMessage(t, hs1, alice, hiddenRoom, "written before the join")
	openMessage := fedSendMessage(t, hs1, alice, openRoom, "written before the join")

	beaToken := hs2.registerUser(t, "bea", "pw-bea")
	for _, roomID := range []string{hiddenRoom, openRoom} {
		if status, answer := hs2.clientDo(t, http.MethodPost,
			"/_matrix/client/v3/join/"+roomID+"?server_name="+hs1.name, map[string]any{}, beaToken); status != http.StatusOK {
			t.Fatalf("%s could not join %s on %s: %d %v", hs2.name, roomID, hs1.name, status, answer)
		}
	}

	for _, tc := range []struct {
		room     string
		event    string
		wantBody bool
		why      string
	}{
		{hiddenRoom, hiddenMessage, false, "a room whose history was `joined` at the moment of the event"},
		{openRoom, openMessage, true, "a `shared` room shares history backwards with new members"},
	} {
		status, payload := hs2.signedRequest(t, hs1, http.MethodGet,
			"/_matrix/federation/v1/event/"+tc.event, nil)
		if status != http.StatusOK {
			t.Fatalf("%s asking %s for %s: %d %s", hs2.name, hs1.name, tc.event, status, payload)
		}
		pdu := onlyPDU(t, payload)
		content, _ := pdu["content"].(map[string]any)
		_, hasBody := content["body"]
		if hasBody != tc.wantBody {
			t.Errorf("%s: %s got content %v, want the body %s: %s",
				tc.room, tc.event, content, map[bool]string{true: "present", false: "absent"}[tc.wantBody], tc.why)
		}
		// A trimmed event is still an event: without the graph fields and the signature a peer
		// cannot place it, verify it, or work out that it is missing the rest.
		for _, key := range []string{"prev_events", "auth_events", "signatures", "event_id", "room_id", "sender"} {
			if _, ok := pdu[key]; !ok {
				t.Errorf("%s: the answer for %s has no %s, which a peer needs to place and verify the event",
					tc.room, tc.event, key)
			}
		}
	}
}

// onlyPDU reads the single PDU a federation read endpoint answered with.
func onlyPDU(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var answer struct {
		PDUs []map[string]any `json:"pdus"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		t.Fatalf("a federation event answer is not JSON: %v (%s)", err, payload)
	}
	if len(answer.PDUs) != 1 {
		t.Fatalf("the answer carries %d PDUs, wanting 1: %s", len(answer.PDUs), payload)
	}
	return answer.PDUs[0]
}

// fedCreateRoom and fedSendMessage are the two client calls a federated pair needs constantly
// enough to be worth naming.
func fedCreateRoom(t *testing.T, n *fedNode, token string, body map[string]any) string {
	t.Helper()
	status, answer := n.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom", body, token)
	if status != http.StatusOK {
		t.Fatalf("%s could not create a room: %d %v", n.name, status, answer)
	}
	roomID, _ := answer["room_id"].(string)
	if roomID == "" {
		t.Fatalf("%s created a room without a room ID: %v", n.name, answer)
	}
	return roomID
}

func fedSendMessage(t *testing.T, n *fedNode, token, roomID, body string) string {
	t.Helper()
	status, answer := n.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/"+n.name+"-"+body,
		map[string]any{"msgtype": "m.text", "body": body}, token)
	if status != http.StatusOK {
		t.Fatalf("%s could not send a message in %s: %d %v", n.name, roomID, status, answer)
	}
	eventID, _ := answer["event_id"].(string)
	if eventID == "" {
		t.Fatalf("%s got no event ID for a message in %s: %v", n.name, roomID, answer)
	}
	return eventID
}
