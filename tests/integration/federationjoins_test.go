package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// A remote user joins a room by asking a server that is already in it what a join by them
// would look like, signing the answer, and handing it back. The point of the two-step
// handshake is that neither side can decide alone: the joining server speaks for its user and
// signs, the resident server speaks for the room and accepts. These tests drive the handshake
// from the joining side by hand, because the outbound half of it is the task that follows —
// and doing it by hand is what shows what each half is responsible for.

// joinHandshake runs the resident's half of the handshake for a user on `joiner` and returns
// the signed event, its ID, and what the resident answered.
func joinHandshake(t *testing.T, joiner, resident *fedNode, roomID, userID string) (map[string]any, string, map[string]any) {
	t.Helper()

	status, body := joiner.signedRequest(t, resident, http.MethodGet,
		"/_matrix/federation/v1/make_join/"+roomID+"/"+userID, nil)
	if status != http.StatusOK {
		t.Fatalf("%s refused to template a join: %d %s", resident.name, status, body)
	}
	var offer struct {
		Event       map[string]any `json:"event"`
		RoomVersion string         `json:"room_version"`
	}
	if err := json.Unmarshal(body, &offer); err != nil {
		t.Fatalf("%s answered the join template with something unreadable: %s", resident.name, body)
	}
	template := offer.Event
	if template == nil {
		t.Fatalf("%s answered with no event template: %s", resident.name, body)
	}
	// The joining server is required to check these before signing. A resident server that
	// names a different room, or a different user, is offering somebody else's join.
	if template["type"] != "m.room.member" || template["room_id"] != roomID ||
		template["sender"] != userID || template["state_key"] != userID {
		t.Fatalf("%s offered a template that is not this user's join: %v", resident.name, template)
	}
	content, _ := template["content"].(map[string]any)
	if content["membership"] != "join" {
		t.Fatalf("%s offered a membership of %v", resident.name, content["membership"])
	}
	if _, present := template["event_id"]; present {
		t.Errorf("%s offered a template that already has an event ID: %v", resident.name, template)
	}
	if _, present := template["signatures"]; present {
		t.Errorf("%s signed a join it does not speak for: %v", resident.name, template)
	}
	if _, present := template["hashes"]; present {
		t.Errorf("%s hashed a join it does not speak for: %v", resident.name, template)
	}
	// The fields the joining server cannot invent.
	if len(anyStringSlice(template["prev_events"])) == 0 || len(anyStringSlice(template["auth_events"])) == 0 {
		t.Errorf("%s offered a template without the room's own shape: %v", resident.name, template)
	}

	rules := roomversion.MustGet(offer.RoomVersion)
	template["origin"] = joiner.name
	template["origin_server_ts"] = anyInt64(template["origin_server_ts"])
	if err := events.Seal(joiner.name, rules, template, joiner.app.Keys); err != nil {
		t.Fatalf("signing the join: %v", err)
	}
	eventID, err := events.EventID(rules, template)
	if err != nil {
		t.Fatalf("the join's event ID: %v", err)
	}

	// The joining server supplies the ID it computed. The URL carries the same one, and the
	// resident server recomputes it rather than taking the URL's word.
	template["event_id"] = eventID
	signed, err := events.Canonical(template)
	if err != nil {
		t.Fatalf("encoding the signed join: %v", err)
	}
	status, body = joiner.signedRequest(t, resident, http.MethodPut,
		"/_matrix/federation/v2/send_join/"+roomID+"/"+eventID, signed)
	if status != http.StatusOK {
		t.Fatalf("%s refused the signed join: %d %s", resident.name, status, body)
	}
	var answer map[string]any
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("%s answered the join with something unreadable: %s", resident.name, body)
	}
	return template, eventID, answer
}

// anyStringSlice and anyInt64 read JSON values without the ceremony.
func anyStringSlice(value any) []string {
	list, _ := value.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func anyInt64(value any) int64 {
	number, _ := value.(json.Number)
	if number == "" {
		if text, ok := value.(string); ok {
			number = json.Number(text)
		}
	}
	out, _ := number.Int64()
	return out
}

// TestRemoteUserJoinsARoomHeldElsewhere is the handshake end to end.
func TestRemoteUserJoinsARoomHeldElsewhere(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "host", "pw-host")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "open doors"}, alice)
	roomID, _ := created["room_id"].(string)
	bea := "@bea:" + hs2.name
	beaToken := hs2.registerUser(t, "bea", "pw-bea")

	template, eventID, answer := joinHandshake(t, hs2, hs1, roomID, bea)

	// The resident server answers with the room the joining server now has to catch up on:
	// the state as it was before the join, and what authorises it.
	state := pduList(t, answer["state"])
	if len(state) == 0 {
		t.Fatalf("%s answered with no state: %v", hs1.name, answer)
	}
	if !hasType(t, state, "m.room.create") || !hasType(t, state, "m.room.power_levels") {
		t.Errorf("%s's state answer is missing the room's foundations: %v", hs1.name, typeList(state))
	}
	chain := pduList(t, answer["auth_chain"])
	if len(chain) == 0 {
		t.Errorf("%s answered with no auth chain, so the join cannot be checked: %v", hs1.name, answer)
	}
	if servers := anyStringSlice(answer["servers_in_room"]); len(servers) != 1 || servers[0] != hs1.name {
		t.Errorf("%s named %v as the servers in the room, wanting itself only", hs1.name, servers)
	}

	// The resident server signs the event too. It is the one attesting that this join was
	// accepted into the room, and the other servers in the room see that signature.
	signed := eventMap(t, answer["event"])
	if signed == nil || signed["event_id"] != eventID {
		t.Fatalf("%s did not answer with the accepted membership event: %v", hs1.name, answer["event"])
	}
	signatures, _ := signed["signatures"].(map[string]any)
	if _, ok := signatures[hs2.name]; !ok {
		t.Errorf("%s's answer is not signed by the server that speaks for the user: %v", hs2.name, signatures)
	}
	if _, ok := signatures[hs1.name]; !ok {
		t.Errorf("%s did not sign a join it accepted into its room: %v", hs1.name, signatures)
	}
	// Signing is the only thing the resident server adds. Where the join sits in the graph
	// came from the template, and quietly re-picking it would rename the event in front of
	// every server that already heard about it.
	if signed["depth"] != template["depth"] ||
		len(anyStringSlice(signed["prev_events"])) != len(anyStringSlice(template["prev_events"])) ||
		len(anyStringSlice(signed["auth_events"])) != len(anyStringSlice(template["auth_events"])) {
		t.Errorf("%s changed the join between the template and the accepted event:\n%v\n%v",
			hs1.name, template, signed)
	}

	// The joining server is not in the room yet, so it has to be brought up to date. This
	// hands the bundle over in the order the events depend on, which is what the missing-event
	// fetch of T-4007 will do on its own.
	bundle := append(pduList(t, answer["auth_chain"]), pduList(t, answer["state"])...)
	bundle = append(bundle, signed)
	if _, results := hs1.sendTransaction(t, hs2, "catch-up-after-join", byDepth(dedupe(bundle))); anyRefusal(t, results) {
		t.Fatalf("%s refused the room it was handed after a join: %v", hs2.name, results.PDUs)
	}

	// On the resident's side the user is now a member, which is the whole point of the
	// handshake.
	_, members := hs1.clientDo(t, http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/joined_members", nil, alice)
	joined, _ := members["joined"].(map[string]any)
	if _, ok := joined[bea]; !ok {
		t.Fatalf("%s accepted the join but does not list %s as present: %v", hs1.name, bea, members)
	}

	// And on the joining side the user can see the room they joined.
	status, page := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, beaToken)
	if status != http.StatusOK {
		t.Errorf("%s will not show its own user the room they just joined: %v", hs2.name, page)
	} else if len(chunkList(t, page["chunk"])) == 0 {
		t.Errorf("%s shows its own user an empty room after the join", hs2.name)
	}
}

// pduList reads a list of PDUs from a federation response.
func pduList(t *testing.T, value any) []map[string]any {
	t.Helper()
	raw, _ := value.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if event, ok := entry.(map[string]any); ok {
			out = append(out, event)
		}
	}
	return out
}

func eventMap(t *testing.T, value any) map[string]any {
	t.Helper()
	event, _ := value.(map[string]any)
	return event
}

func hasType(t *testing.T, events []map[string]any, kind string) bool {
	t.Helper()
	for _, event := range events {
		if event["type"] == kind {
			return true
		}
	}
	return false
}

func typeList(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, stringAny(event, "type"))
	}
	return out
}

// TestMakeJoinRefusesAUserTheRoomDoesNotPermit: the resident server decides who may join its
// copy of the room, and it says so before the joining server has done any signing.
func TestMakeJoinRefusesAUserTheRoomDoesNotPermit(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "gatekeeper", "pw-gatekeeper")
	_, closed := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "name": "closed"}, alice)
	closedRoom, _ := closed["room_id"].(string)
	_, open := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "shut to one"}, alice)
	openRoom, _ := open["room_id"].(string)

	carol := "@carol:" + hs2.name
	hs2.registerUser(t, "carol", "pw-carol")

	status, answer := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/make_join/"+closedRoom+"/"+carol, nil)
	if status != http.StatusForbidden {
		t.Errorf("an invitation-only room answered a join with %d (%s), wanting 403", status, answer)
	}

	// A ban is a refusal by the room, not by the join rule, and it holds in a public room.
	if status, answer := hs1.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+openRoom+"/state/m.room.member/"+carol,
		map[string]any{"membership": "ban"}, alice); status != http.StatusOK {
		t.Fatalf("%s would not ban %s: %v", hs1.name, carol, answer)
	}
	status, answer = hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/make_join/"+openRoom+"/"+carol, nil)
	if status != http.StatusForbidden {
		t.Errorf("a banned user was answered with %d (%s), wanting 403", status, answer)
	}

	// A room this server has never heard of is not a room it can template a join for.
	status, answer = hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/make_join/"+unknownRoomID+"/"+carol, nil)
	if status != http.StatusNotFound {
		t.Errorf("an unknown room was answered with %d (%s), wanting 404", status, answer)
	}

	// And nobody gets the room's shape without signing for the ask.
	status, answer = hs2.rawRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/make_join/"+openRoom+"/"+carol, "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("an unsigned make_join was answered with %d (%s), wanting 401", status, answer)
	}
}

const unknownRoomID = "!nowhere:hs9"

// TestSendJoinRefusesAnEventThatIsNotWhatItClaims pins the path checks. The URL names a room
// and an event ID; the body has to be that event, or the two halves of the handshake are
// talking about different things and the room is being fed an event nobody offered.
func TestSendJoinRefusesAnEventThatIsNotWhatItClaims(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "witness", "pw-witness")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, alice)
	roomID, _ := created["room_id"].(string)
	dave := "@dave:" + hs2.name
	hs2.registerUser(t, "dave", "pw-dave")

	status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/make_join/"+roomID+"/"+dave, nil)
	if status != http.StatusOK {
		t.Fatalf("templating a join: %d %s", status, body)
	}
	var offer struct {
		Event       map[string]any `json:"event"`
		RoomVersion string         `json:"room_version"`
	}
	if err := json.Unmarshal(body, &offer); err != nil {
		t.Fatalf("the join template: %v", err)
	}
	rules := roomversion.MustGet(offer.RoomVersion)
	offer.Event["origin"] = hs2.name
	if err := events.Seal(hs2.name, rules, offer.Event, hs2.app.Keys); err != nil {
		t.Fatalf("signing: %v", err)
	}
	eventID, err := events.EventID(rules, offer.Event)
	if err != nil {
		t.Fatalf("the join's ID: %v", err)
	}
	offer.Event["event_id"] = eventID
	// One word changed after the ID was computed. The bytes still verify — they are signed —
	// but they are no longer the event the ID names.
	content, _ := offer.Event["content"].(map[string]any)
	content["reason"] = "not what was offered"
	edited, err := events.Canonical(offer.Event)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	// Which check catches it is this server's choice: the path check notices that the ID in
	// the URL is not the ID this event makes, and the receiving checks notice that the content
	// no longer matches the hash the origin recorded. What matters is that the event is
	// refused rather than stored, so the room does not gain a member nobody offered.
	if status, answer := hs2.signedRequest(t, hs1, http.MethodPut,
		"/_matrix/federation/v2/send_join/"+roomID+"/"+eventID, edited); status == http.StatusOK {
		t.Errorf("%s accepted an event that is not the join its ID names: %v", hs1.name, answer)
	} else if status != http.StatusBadRequest && status != http.StatusForbidden {
		t.Errorf("a tampered join was answered with %d (%s), wanting a refusal", status, answer)
	}
	if _, members := hs1.clientDo(t, http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/joined_members", nil, alice); membershipOf(t, members, dave) {
		t.Errorf("%s lists %s as present after refusing the join", hs1.name, dave)
	}

	// The membership has to be a join. Somebody else's membership event under a join's URL is
	// a different claim about the room.
	other := map[string]any{
		"type": "m.room.member", "room_id": roomID, "sender": dave, "state_key": dave,
		"content": map[string]any{"membership": "leave"},
	}
	other["auth_events"] = offer.Event["auth_events"]
	other["prev_events"] = offer.Event["prev_events"]
	other["depth"] = offer.Event["depth"]
	other["origin"] = hs2.name
	other["origin_server_ts"] = offer.Event["origin_server_ts"]
	if err := events.Seal(hs2.name, rules, other, hs2.app.Keys); err != nil {
		t.Fatalf("signing the leave: %v", err)
	}
	leaveID, err := events.EventID(rules, other)
	if err != nil {
		t.Fatalf("the leave's ID: %v", err)
	}
	other["event_id"] = leaveID
	encoded, _ := events.Canonical(other)
	if status, answer := hs2.signedRequest(t, hs1, http.MethodPut,
		"/_matrix/federation/v2/send_join/"+roomID+"/"+leaveID, encoded); status != http.StatusBadRequest {
		t.Errorf("a leave offered as a join was answered with %d (%s), wanting 400", status, answer)
	}
}

// TestStateAndAuthChainAreAnsweredToServersInTheRoom: a room's state and its auth chain are
// what a server needs to catch up, and also everything a room contains. Asking is
// authenticated, and the answer is limited to servers the room already has a seat for.
func TestStateAndAuthChainAreAnsweredToServersInTheRoom(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "librarian", "pw-librarian")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "the stacks"}, alice)
	roomID, _ := created["room_id"].(string)
	erin := "@erin:" + hs2.name
	hs2.registerUser(t, "erin", "pw-erin")

	if status, answer := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/state_ids/"+roomID+"?event_id=$nothing:hs1", nil); status != http.StatusForbidden {
		t.Errorf("a server outside the room was answered state_ids with %d (%s), wanting 403", status, answer)
	}

	_, eventID, answer := joinHandshake(t, hs2, hs1, roomID, erin)
	state := pduList(t, answer["state"])
	var createID string
	for _, event := range state {
		if event["type"] == "m.room.create" {
			createID = stringAny(event, "event_id")
		}
	}
	if createID == "" {
		t.Fatalf("no create event in the state answer: %v", typeList(state))
	}

	status, body := hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/state_ids/"+roomID+"?event_id="+createID, nil)
	if status != http.StatusOK {
		t.Fatalf("a server the room now has is answered state_ids with %d: %s", status, body)
	}
	var ids struct {
		PDUIDs       []string `json:"pdu_ids"`
		AuthChainIDs []string `json:"auth_chain_ids"`
	}
	if err := json.Unmarshal(body, &ids); err != nil {
		t.Fatalf("the state_ids answer: %s", body)
	}
	// The state answer was the room before the join; state_ids is the room now, which has one
	// more member in it.
	if len(ids.PDUIDs) < len(state) {
		t.Errorf("state_ids names %d events, fewer than the %d the join answer handed over",
			len(ids.PDUIDs), len(state))
	}
	if !containsString(ids.PDUIDs, eventID) {
		t.Errorf("state_ids does not name the join that just happened: %v", ids.PDUIDs)
	}
	if !containsString(ids.AuthChainIDs, createID) {
		t.Errorf("the auth chain ids do not include the room's create event: %v", ids.AuthChainIDs)
	}

	status, body = hs2.signedRequest(t, hs1, http.MethodGet,
		"/_matrix/federation/v1/event_auth/"+roomID+"/"+eventID, nil)
	if status != http.StatusOK {
		t.Fatalf("the auth chain of the join was answered with %d: %s", status, body)
	}
	var chain struct {
		AuthChain []map[string]any `json:"auth_chain"`
	}
	if err := json.Unmarshal(body, &chain); err != nil {
		t.Fatalf("the event_auth answer: %s", body)
	}
	if len(chain.AuthChain) == 0 {
		t.Fatalf("the join's auth chain is empty")
	}
	found := false
	for _, event := range chain.AuthChain {
		if event["type"] == "m.room.create" {
			found = true
		}
		if event["type"] == "m.room.member" && event["event_id"] == eventID {
			t.Errorf("an event's auth chain contains the event itself: %v", event)
		}
	}
	if !found {
		t.Errorf("the join's auth chain does not reach the room's create event: %v", typeList(chain.AuthChain))
	}
}

// membershipOf reads a `/joined_members` answer for a user's presence.
func membershipOf(t *testing.T, answer map[string]any, userID string) bool {
	t.Helper()
	joined, _ := answer["joined"].(map[string]any)
	_, ok := joined[userID]
	return ok
}
