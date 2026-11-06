package integration

import (
	"context"
	"net/http"
	"testing"
)

// TestFederatedJoinLeavesOneMemberEventForTheRemoteUser is a regression test for T-4046: the room
// graph must not end up holding two `m.room.member` events for the same remote user when that user
// joined once.
//
// The shape that exposed it is the resident half of a federated join. The joining server creates
// and signs the join event; the resident server signs that same event as well — it is the server
// attesting that the join was accepted into the room — and stores it. If those two servers end up
// naming the event differently, or if the handshake writes the membership twice, the room carries
// two claims that one user joined, and that lands on state resolution, `/members`, member counts,
// join rules and every history walk a peer asks for. Complement caught it from the history walk
// (`TestInboundCanReturnMissingEvents` finding the joiner's own member event in the answer, at a
// depth that says it was written late rather than at creation).

func TestFederatedJoinLeavesOneMemberEventForTheRemoteUser(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "hostess", "pw-hostess")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat", "name": "one join only"}, alice)
	roomID, _ := created["room_id"].(string)
	if status, answer := hs1.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/count-txn",
		map[string]any{"msgtype": "m.text", "body": "before the join"}, alice); status != http.StatusOK {
		t.Fatalf("a message on %s: %v", hs1.name, answer)
	}

	bea := "@bea:" + hs2.name
	beaToken := hs2.registerUser(t, "bea", "pw-bea")
	if status, joined := hs2.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/join/"+roomID+"?server_name="+hs1.name, map[string]any{}, beaToken); status != http.StatusOK {
		t.Fatalf("%s could not join %s on %s: %d %v", hs2.name, roomID, hs1.name, status, joined)
	}

	// Every event the resident server holds for the room, and every one of them that names Bea.
	db := hs1.app.DB
	rows, err := db.Events.EventsAfter(context.Background(), roomID, 0, 200)
	if err != nil {
		t.Fatalf("reading %s's events for %s: %v", hs1.name, roomID, err)
	}
	var forBea []string
	t.Logf("events %s holds for %s:", hs1.name, roomID)
	for _, row := range rows {
		stateKey := ""
		if row.StateKey != nil {
			stateKey = *row.StateKey
		}
		t.Logf("  %-24s state_key=%-28s depth=%d id=%s", row.Type, stateKey, row.Depth, row.EventID)
		if row.Type == "m.room.member" && stateKey == bea {
			forBea = append(forBea, row.EventID)
		}
	}
	if len(forBea) > 1 {
		t.Errorf("%s holds %d member events for %s after one join, wanting 1: %v",
			hs1.name, len(forBea), bea, forBea)
	}

	// The ceiling of the walk is not part of the answer. The peer named this join event as the
	// point it is looking back from, so handing it back is answering a different question, and a
	// peer that treats the answer as "the hole I am missing" now has one event too many.
	if len(forBea) == 1 {
		_, createdState := hs1.clientDo(t, http.MethodGet,
			"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.create/", nil, alice)
		createID, _ := createdState["event_id"].(string)
		answer, err := hs1.app.Rooms.EventsBetween(context.Background(), roomID,
			[]string{createID}, forBea, 10, 1)
		if err != nil {
			t.Fatalf("filling the gap between the room's creation and %s's join: %v", bea, err)
		}
		for _, row := range answer {
			if row.EventID == forBea[0] {
				t.Errorf("the walk between the room's creation and %s's join returned the join itself"+
					", which was the ceiling of the walk, not part of the answer", bea)
			}
		}
		if len(answer) == 0 {
			t.Errorf("the walk between the room's creation and %s's join answered nothing,"+
				" wanting the room's history before that join", bea)
		}
	}

	// The two servers have to call the join event the same thing. A derived event ID is the point
	// of a derived event ID: two servers agree on which event is which without asking each other,
	// and a peer that names an event this server does not have under that name cannot fill a hole
	// in its history, ask about the event, or refer to it at all.
	// The full state list, because it is the list that carries event IDs: the per-key form of
	// that endpoint answers with the event's content, which is what the specification documents
	// for it, and content has no identifier in it.
	remoteState := hs2.clientDoList(t, "/_matrix/client/v3/rooms/"+roomID+"/state", beaToken)
	remoteID := ""
	for _, event := range remoteState {
		if event["type"] != "m.room.member" || event["state_key"] != bea {
			continue
		}
		remoteID, _ = event["event_id"].(string)
	}
	if remoteID == "" {
		t.Fatalf("%s cannot find its own user's membership event in %s: %v", hs2.name, roomID, remoteState)
	}
	if len(forBea) == 1 && forBea[0] != remoteID {
		t.Errorf("the two servers name %s's join differently: %s calls it %s, %s calls it %s",
			bea, hs2.name, remoteID, hs1.name, forBea[0])
	}
}
