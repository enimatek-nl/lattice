package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// A peer's invite arrives in more than one spelling. The specification writes the invited event
// under `invite_event`; gomatrixserverlib — what Dendrite and Complement's federation server send —
// writes it under `event` and rejects a body that lacks that key. This server writes both names and
// reads either, and the test below drives the reading side with the peer's spelling, because a
// request this server can send is not evidence that it can answer one.

// TestAnInviteIsCountersignedInThePeerSpelling asks hs1 to countersign an invite for one of its own
// users, written the way gomatrixserverlib writes it, and asks what hs1 made of it afterwards.
func TestAnInviteIsCountersignedInThePeerSpelling(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	cara := "@cara:" + hs1.name
	caraToken := hs1.registerUser(t, "cara", "pw-cara")
	bobaToken := hs2.registerUser(t, "boba", "pw-boba")
	boba := "@boba:" + hs2.name
	_, created := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "name": "a room on hs2"}, bobaToken)
	roomID, _ := created["room_id"].(string)
	version, _ := created["room_version"].(string)
	if roomID == "" || version == "" {
		t.Fatalf("%s could not create a room to invite from: %v", hs2.name, created)
	}

	// The room's real state, read back as signed events. They travel inside the invite so the
	// invited user's server can judge the invite at all, and they are worth reading from the room
	// rather than inventing: a state event this server made up would be refused for its signature,
	// and refusing the made-up case is not the same as accepting a peer's real one.
	status, raw := hs2.doWithStatus(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state", nil, bobaToken)
	if status != http.StatusOK {
		t.Fatalf("%s: reading the room's state: %d %s", hs2.name, status, raw)
	}
	var state []map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("%s: the room's state is unreadable: %s", hs2.name, raw)
	}
	bobaID := ""
	for _, event := range state {
		kind, _ := event["type"].(string)
		key, _ := event["state_key"].(string)
		if kind == "m.room.member" && key == boba {
			bobaID, _ = event["event_id"].(string)
		}
	}
	if bobaID == "" {
		t.Fatalf("%s's own membership event is not in the room state: %v", hs2.name, state)
	}

	invite := map[string]any{
		"type":             "m.room.member",
		"room_id":          roomID,
		"sender":           boba,
		"state_key":        cara,
		"content":          map[string]any{"membership": "invite"},
		"prev_events":      []any{bobaID},
		"depth":            float64(2),
		"origin":           hs2.name,
		"origin_server_ts": time.Now().UnixMilli(),
		"unsigned":         map[string]any{"invite_room_state": state},
	}
	auth := []any{}
	for _, entry := range state {
		kind, _ := entry["type"].(string)
		if kind == "m.room.create" || kind == "m.room.power_levels" || kind == "m.room.join_rules" {
			auth = append(auth, entry["event_id"])
		}
	}
	auth = append(auth, bobaID)
	invite["auth_events"] = auth

	rules := roomversion.MustGet(version)
	if err := events.Seal(hs2.name, rules, invite, hs2.app.Keys); err != nil {
		t.Fatalf("signing the invite: %v", err)
	}
	eventID, err := events.EventID(rules, invite)
	if err != nil {
		t.Fatalf("the invite's event ID: %v", err)
	}
	invite["event_id"] = eventID

	body, err := json.Marshal(map[string]any{"room_version": version, "event": invite})
	if err != nil {
		t.Fatalf("encoding the invite request: %v", err)
	}
	status, raw = hs2.signedRequest(t, hs1, http.MethodPut,
		"/_matrix/federation/v2/invite/"+roomID+"/"+eventID, body)
	if status != http.StatusOK {
		t.Fatalf("%s refused to countersign a peer-spelled invite: %d %s", hs1.name, status, raw)
	}
	var answer struct {
		Event map[string]any `json:"event"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil || answer.Event == nil {
		t.Fatalf("%s answered without the event it signed: %s", hs1.name, raw)
	}
	signatures, _ := answer.Event["signatures"].(map[string]any)
	for _, serverName := range []string{hs1.name, hs2.name} {
		if _, ok := signatures[serverName]; !ok {
			t.Errorf("%s's signature is missing from the countersigned invite: %v", serverName, signatures)
		}
	}

	// Countersigning is not the end of it: the invited user has to be able to see the invitation,
	// and the server that accepted it is the one that has to tell them.
	status, membership := hs1.clientDo(t, http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+cara, nil, caraToken)
	if status != http.StatusOK {
		t.Fatalf("%s: %s cannot read the invite she was given: %d %v", hs1.name, cara, status, membership)
	}
	if got, _ := membership["membership"].(string); got != "invite" {
		t.Errorf("membership of %s on %s = %q, want invite", cara, hs1.name, got)
	}
}
