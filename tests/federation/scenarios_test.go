package federation

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// TestTheFederationScenarioTableRunsWithoutSeeds is the wrap of the federation work: one pass
// over every scenario the two-server suite is supposed to hold, with the rooms put on both
// servers by the join handshake rather than by writing rows into a database.
//
// The distinction is the reason this file exists. A test that seeds `Room` and `RoomMembership`
// rows asks "does the code work given that both servers already know everything" - which is true
// of the seed and says nothing about the protocol. Each scenario below starts from two servers
// that have never heard of each other's users, and the facts they end up sharing are facts one of
// them sent.
//
// Knocking is deliberately not here. A knock arrives at a server the room does not know yet, so
// it needs the join machinery to carry an invite that has not been accepted and an allow rule
// that names a stranger; that is the knocking gap named in TODO, not a scenario this suite can
// honestly claim.
func TestTheFederationScenarioTableRunsWithoutSeeds(t *testing.T) {
	t.Parallel()
	scenarios := []struct {
		name string
		run  func(t *testing.T, one, two *server, alice, bob *session)
	}{
		{
			name: "join",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				roomID := shareRoomByJoin(t, one, two, alice, bob)
				// The host server learned about the guest from the handshake, not from a row a
				// test wrote.
				members, err := one.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
				if err != nil {
					t.Fatalf("reading the members on %s: %v", one.name, err)
				}
				if len(members) != 2 {
					t.Errorf("%s holds %d joined members, want the host and the guest it was told about", one.name, len(members))
				}
				if _, err := two.db.StateRepo.Get(roomID, matrix.TypeCreate, ""); err != nil {
					t.Errorf("%s joined %s without being given the room's state: %v", two.name, roomID, err)
				}
			},
		},
		{
			name: "invite then join",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				roomID := one.createRoom(alice, map[string]any{"preset": "private_chat"})
				one.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite",
					alice.Token, matrix.Object{"user_id": bob.UserID})
				two.join(bob, roomID)
				if _, err := two.db.MembershipRepo.Get(roomID, bob.UserID); err != nil {
					t.Fatalf("%s did not end up in %s after an invite and a join: %v", bob.UserID, roomID, err)
				}
			},
		},
		{
			name: "messages both ways",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				roomID := shareRoomByJoin(t, one, two, alice, bob)
				there := one.send(alice, roomID, "host to guest")
				back := two.send(bob, roomID, "guest to host")
				flush(t, one)
				flush(t, two)
				for _, pair := range []struct {
					on      *server
					eventID string
					text    string
				}{{two, there, "host to guest"}, {one, back, "guest to host"}} {
					event, err := pair.on.db.EventRepo.Get(pair.eventID)
					if err != nil {
						t.Fatalf("%s never received %q: %v", pair.on.name, pair.text, err)
					}
					if object := decodeContent(t, matrix.RawMessage(event.Content)); stringField(object, "body") != pair.text {
						t.Errorf("%s holds %q, want the message %q", pair.on.name, object["body"], pair.text)
					}
				}
			},
		},
		{
			name: "typing",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				roomID := shareRoomByJoin(t, one, two, alice, bob)
				two.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+bob.UserID,
					bob.Token, matrix.Object{"typing": true, "timeout": 30000})
				flush(t, two)
				if !typedAs(t, one, roomID, bob.UserID) {
					t.Errorf("%s never heard that %s is typing in %s", one.name, bob.UserID, roomID)
				}
			},
		},
		{
			name: "receipts",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				roomID := shareRoomByJoin(t, one, two, alice, bob)
				message := one.send(alice, roomID, "read me")
				flush(t, one)
				two.mustDo(http.StatusOK, http.MethodPost,
					"/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+message,
					bob.Token, matrix.Object{"ts": 1})
				flush(t, two)
				if !readReceiptFor(t, one, roomID, message) {
					t.Errorf("%s never learned that %s read %s", one.name, bob.UserID, message)
				}
			},
		},
		{
			name: "to-device",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				shareRoomByJoin(t, one, two, alice, bob)
				sendToDevice(t, one, alice, matrix.TypeEncrypted, "scenarios-olm", map[string]map[string]any{
					bob.UserID: {bob.DeviceID: matrix.Object{"algorithm": "m.olm.v1.curve25519-aes-sha2", "ciphertext": "opaque"}},
				})
				flush(t, one)
				got := syncOnce(t, two, bob, "")
				if got.ToDevice == nil || len(got.ToDevice.Events) == 0 {
					t.Fatalf("%s delivered no to-device message to %s", two.name, bob.UserID)
				}
				if got.ToDevice.Events[0].Sender != alice.UserID {
					t.Errorf("the to-device message is from %s, want %s", got.ToDevice.Events[0].Sender, alice.UserID)
				}
			},
		},
		{
			name: "remote profile",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				shareRoomByJoin(t, one, two, alice, bob)
				one.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/profile/"+alice.UserID+"/displayname",
					alice.Token, matrix.Object{"displayname": "Alice of hs1"})
				got := two.mustDo(http.StatusOK, http.MethodGet, "/_matrix/client/v3/profile/"+alice.UserID,
					bob.Token, nil)
				if stringField(got.Body, "displayname") != "Alice of hs1" {
					t.Errorf("%s answered %v for %s's profile, want the name %s holds: %s",
						two.name, got.Body["displayname"], alice.UserID, one.name, got.Raw)
				}
			},
		},
		{
			name: "remote device keys",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				shareRoomByJoin(t, one, two, alice, bob)
				keys := signedDeviceKeys(t, alice)
				one.uploadDeviceKeys(alice, keys)
				got := two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/keys/query", bob.Token,
					matrix.Object{"device_keys": matrix.Object{alice.UserID: matrix.Array{}}})
				device := asObject(t, asObject(t, got.Body["device_keys"])[alice.UserID])
				if err := keyObjectMatches(t, keys, device[alice.DeviceID]); err != nil {
					t.Errorf("%s answered a device key for %s that is not the one published: %v",
						two.name, alice.UserID, err)
				}
			},
		},
		{
			name: "leave",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				roomID := shareRoomByJoin(t, one, two, alice, bob)
				two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/leave",
					bob.Token, map[string]any{})
				flush(t, two)
				row, err := one.db.MembershipRepo.Get(roomID, bob.UserID)
				if err != nil {
					t.Fatalf("%s never recorded that %s left: %v", one.name, bob.UserID, err)
				}
				if row.Membership != matrix.MembershipLeave {
					t.Errorf("%s thinks %s is %s after the leave, want leave", one.name, bob.UserID, row.Membership)
				}
			},
		},
		{
			name: "backfill",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				roomID := one.createRoom(alice, map[string]any{"preset": "private_chat", "invite": []any{bob.UserID}})
				// The room tells its history to whoever is in it, which is what makes the gap
				// fetchable at all: under the default `joined` visibility the events before a
				// guest arrived are the guest's not to read, and a server that asks for them
				// should be refused.
				one.mustDo(http.StatusOK, http.MethodPut,
					"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.history_visibility", alice.Token,
					matrix.Object{"history_visibility": "shared"})
				history := one.send(alice, roomID, "before the guest arrived")
				two.join(bob, roomID)
				if _, err := two.db.EventRepo.Get(history); err == nil {
					t.Fatalf("%s already holds %s before asking for anything: the scenario proves nothing",
						two.name, history)
				}
				// The guest server was not there for the message, so it asks the server that was.
				// This is the federation endpoint rather than a client's /messages: a client
				// reaching across a gap in its own history is not wired up yet, and pretending
				// otherwise here would hide that.
				_, raw, err := two.peer().GetRaw(t.Context(), serverOne,
					"/_matrix/federation/v1/backfill/"+roomID,
					map[string][]string{"limit": {"10"}}, 1<<20)
				if err != nil {
					t.Fatalf("%s asking %s for the history of %s: %v", two.name, one.name, roomID, err)
				}
				answer := map[string]any{}
				if err := json.Unmarshal(raw, &answer); err != nil {
					t.Fatalf("the backfill answer of %s is not JSON: %v (%s)", one.name, err, raw)
				}
				// A backfill PDU carries no event id - the id is what a receiver computes from the
				// bytes - so what is being looked for is the message itself.
				for _, raw := range asArray(t, answer["pdus"]) {
					event, _ := raw.(map[string]any)
					content, _ := event["content"].(map[string]any)
					if event["type"] == matrix.TypeMessage && content["body"] == "before the guest arrived" {
						return
					}
				}
				t.Errorf("%s backfilled %s without the message %s sent before the guest arrived",
					two.name, roomID, alice.UserID)
			},
		},
		{
			name: "soft-fail after a ban",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				roomID := shareRoomByJoin(t, one, two, alice, bob)
				one.mustDo(http.StatusOK, http.MethodPut,
					"/_matrix/client/v3/rooms/"+roomID+"/state/"+matrix.TypeMember+"/"+bob.UserID,
					alice.Token, matrix.Object{"membership": matrix.MembershipBan})
				// The ban has not been sent yet, and that is the scenario: Bob's server still
				// believes he is in the room, so it lets him speak, and the event arrives built on
				// state that was true when he wrote it. It is kept and marked, not dropped.
				late := two.send(bob, roomID, "unbanned yet")
				flush(t, two)
				receipt, err := one.db.ReceivedPDURepo.Get(late)
				if err != nil {
					t.Fatalf("%s never received the banned user's event: %v", one.name, err)
				}
				if receipt.Outcome != service.OutcomeSoftFailed {
					t.Errorf("%s decided %q about a banned user's event, want %q (%s)",
						one.name, receipt.Outcome, service.OutcomeSoftFailed, receipt.Reason)
				}
			},
		},
		{
			name: "a retried transaction is applied once",
			run: func(t *testing.T, one, two *server, alice, bob *session) {
				roomID := shareRoomByJoin(t, one, two, alice, bob)
				message := one.send(alice, roomID, "exactly once")
				flush(t, one)
				flush(t, one)
				flush(t, one)
				applied := 0
				for _, row := range receipts(t, two) {
					if row.EventID == message {
						applied++
					}
				}
				if applied != 1 {
					t.Errorf("%s recorded %d receipts for one event, want the one its transaction carried", two.name, applied)
				}
				if count, err := two.db.EventRepo.Count(); err == nil && count < 1 {
					t.Errorf("%s holds %d events, want at least the one message", two.name, count)
				}
			},
		},
	}

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			c := newCluster(t)
			one, two := c.one(), c.two()
			alice := one.register("alice", "wonderland")
			bob := two.register("bob", "rubberduck")
			scenario.run(t, one, two, alice, bob)
		})
	}
}

// typedAs reports whether a server has been told that a user is typing in a room.
func typedAs(t *testing.T, s *server, roomID, userID string) bool {
	t.Helper()
	states, err := s.db.TypingRepo.List(roomID)
	if err != nil {
		t.Fatalf("reading the typing state of %s on %s: %v", roomID, s.name, err)
	}
	for _, entry := range states {
		if entry.UserID == userID {
			return true
		}
	}
	return false
}

// readReceiptFor reports whether a server holds a read receipt for an event.
func readReceiptFor(t *testing.T, s *server, roomID, eventID string) bool {
	t.Helper()
	listed, err := s.db.ReceiptRepo.ForRoom(roomID)
	if err != nil {
		t.Fatalf("reading the receipts of %s on %s: %v", eventID, s.name, err)
	}
	for _, entry := range listed {
		if entry.EventID == eventID {
			return true
		}
	}
	return false
}

// readReceiptFor needs the room because receipts are stored per room: an event id alone does not
// say where to look.
