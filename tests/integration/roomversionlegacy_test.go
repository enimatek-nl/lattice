package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// TestRoomsInRoomVersionsOneAndTwoCarryTheirOwnEventShape checks the two things a room in those
// versions gets from its version rather than from this server's preference: an event identifier the
// origin server chose, and references written as [event_id, {"sha256": hash}] pairs whose hash is the
// referenced event's real reference hash.
//
// Both are easy to get wrong in a way that only shows up when another server reads the room back. A
// hash-derived identifier in a version 1 room is an identifier no other server will recognise as that
// event's; references written as bare IDs are references a reader of that version cannot parse, which
// leaves the room looking as though its events have no parents and no authorisation events at all.
// So the events are fetched over federation from a second homeserver, in the form a peer actually
// receives, and examined there.
func TestRoomsInRoomVersionsOneAndTwoCarryTheirOwnEventShape(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"1", "2"} {
		version := version
		t.Run("room version "+version, func(t *testing.T) {
			t.Parallel()
			hs1, hs2 := federationPair(t)

			alice := hs1.registerUser(t, "legacy"+version, "pw-legacy"+version)
			status, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
				map[string]any{"room_version": version, "preset": "public_chat", "name": "an old room"}, alice)
			if status != http.StatusOK {
				t.Fatalf("creating a room version %s room: %v", version, created)
			}
			roomID, _ := created["room_id"].(string)
			if got := createEventContent(t, hs1, roomID, alice)["room_version"]; got != version {
				t.Fatalf("the room was created as version %v, wanting %s", got, version)
			}

			status, sent := hs1.clientDo(t, http.MethodPut,
				"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/legacy-"+version,
				map[string]any{"msgtype": "m.text", "body": "written in room version " + version}, alice)
			if status != http.StatusOK {
				t.Fatalf("sending the message: %v", sent)
			}
			messageID, _ := sent["event_id"].(string)

			// The identifier is the origin server's to choose in these versions: an opaque local
			// part inside the sending server's namespace. It is not the hash of anything.
			if !strings.HasPrefix(messageID, "$") || strings.Count(messageID, ":") != 1 {
				t.Errorf("the event ID %q is not of the $opaque_id:domain shape room version %s defines",
					messageID, version)
			}
			if domain := messageID[strings.Index(messageID, ":")+1:]; domain != hs1.name {
				t.Errorf("the event ID %q is named by %s, wanting the server that made it (%s)",
					messageID, domain, hs1.name)
			}

			// Hand the room to the second homeserver in causal order, then ask it for the message in
			// the form a peer receives: the stored PDU, not the trimmed form a client is given.
			snapshot := hs1.roomSnapshot(t, roomID, alice)
			handover := byDepth(dedupe(snapshot.State, snapshot.Messages))
			status, answer := hs1.sendTransaction(t, hs2, "legacy-"+version, handover)
			if status != http.StatusOK {
				t.Fatalf("the transaction was refused as a whole: %d %v", status, answer)
			}
			for eventID, result := range answer.PDUs {
				if result.Error != "" {
					t.Fatalf("%s refused %s: %s", hs2.name, eventID, result.Error)
				}
			}

			pdu := fetchedPDU(t, hs1, hs2, messageID)
			rules := roomversion.MustGet(version)

			// Every reference is a pair, and the hash in it is the referenced event's actual
			// reference hash — recomputed here from the event the peer hands over, not copied from
			// what the reference claims.
			for _, field := range []string{"prev_events", "auth_events"} {
				list, _ := pdu[field].([]any)
				if len(list) == 0 {
					t.Errorf("the %s of the message are empty, so the room graph this server received has no shape to it", field)
					continue
				}
				for _, entry := range list {
					pair, ok := entry.([]any)
					if !ok || len(pair) != 2 {
						t.Fatalf("%s is written %v, wanting [event_id, {\"sha256\": hash}] in room version %s",
							field, entry, version)
					}
					referencedID, _ := pair[0].(string)
					body, ok := pair[1].(map[string]any)
					if !ok {
						t.Fatalf("the %s entry for %s carries %v, wanting an object with a sha256 hash",
							field, referencedID, pair[1])
					}
					claimed, _ := body["sha256"].(string)
					if claimed == "" {
						t.Fatalf("the %s entry for %s carries no hash", field, referencedID)
					}
					reference := fetchedPDU(t, hs1, hs2, referencedID)
					computed, err := events.ReferenceHashOf(rules, reference)
					if err != nil {
						t.Fatalf("computing the reference hash of %s: %v", referencedID, err)
					}
					if computed != claimed {
						t.Errorf("the %s entry for %s claims %q, but that event's reference hash is %q",
							field, referencedID, claimed, computed)
					}
				}
			}
		})
	}
}

// TestRemoteUserJoinsARoomInRoomVersionTwo runs the join handshake against a room in the older
// version. The handshake is where two homeservers exchange the most event detail — a template, a
// signed join event, the room's state and its auth chain — and in room version 2 all of it has to be
// written in that version's shape, including the join event's identifier, which the joining server
// chooses rather than derives.
func TestRemoteUserJoinsARoomInRoomVersionTwo(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "legacyhost", "pw-legacyhost")
	status, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"room_version": "2", "preset": "public_chat", "name": "old open doors"}, alice)
	if status != http.StatusOK {
		t.Fatalf("creating the room: %v", created)
	}
	roomID, _ := created["room_id"].(string)

	bea := "@bea:" + hs2.name
	hs2.registerUser(t, "bea-legacy", "pw-bea-legacy")

	template, eventID, answer := joinHandshake(t, hs2, hs1, roomID, bea)

	// The resident server's template points at the room's forward extremities in the room's
	// own shape; a joining server that received bare IDs could not sign anything another server
	// in that room would accept.
	if list, _ := template["prev_events"].([]any); len(list) == 0 {
		t.Fatalf("the join template names no previous events: %v", template)
	} else {
		for _, entry := range list {
			if pair, ok := entry.([]any); !ok || len(pair) != 2 {
				t.Errorf("the join template's prev_events entry %v is not a room version 2 reference", entry)
			}
		}
	}

	// The join event carries an identifier a server chose, not one derived from the event: in
	// this version the name cannot be recomputed from the content, so it is written down before
	// signing and survives both signatures unchanged. The room's resident server offers one in
	// its template; whichever server's namespace it sits in, it is opaque and it is what both
	// servers store the event under.
	if !strings.HasPrefix(eventID, "$") || strings.Count(eventID, ":") != 1 {
		t.Errorf("the join event %q is not of the $opaque_id:domain shape room version 2 defines", eventID)
	}
	if domain := eventID[strings.Index(eventID, ":")+1:]; domain != hs1.name && domain != hs2.name {
		t.Errorf("the join event %q is named by %s, which is neither server in this handshake", eventID, domain)
	}
	if strings.Contains(eventID, "=") {
		t.Errorf("the join event %q looks content-derived, which is how later versions name events, not this one", eventID)
	}

	// And the join was accepted into the room: the resident server answered with the state and
	// its authorisation chain, and signed the event it accepted.
	state := pduList(t, answer["state"])
	if !hasType(t, state, "m.room.create") {
		t.Errorf("%s's state answer is missing the room's create event: %v", hs1.name, typeList(state))
	}
	signed := eventMap(t, answer["event"])
	if signed == nil {
		t.Fatalf("%s did not answer with the accepted membership event: %v", hs1.name, answer["event"])
	}
	signatures, _ := signed["signatures"].(map[string]any)
	if _, ok := signatures[hs1.name]; !ok {
		t.Errorf("%s did not sign a join it accepted into its room: %v", hs1.name, signatures)
	}
}

// createEventContent reads a room's m.room.create content as a client would, which is where the
// room's version is recorded.
func createEventContent(t *testing.T, node *fedNode, roomID, token string) map[string]any {
	t.Helper()
	status, content := node.clientDo(t, http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.create/", nil, token)
	if status != http.StatusOK {
		t.Fatalf("reading the create event of %s: %d %v", roomID, status, content)
	}
	return content
}

// fetchedPDU asks one homeserver of another for a single event, in the form the Server-Server API
// answers it, and returns the event object.
func fetchedPDU(t *testing.T, asker, holder *fedNode, eventID string) map[string]any {
	t.Helper()
	status, payload := asker.signedRequest(t, holder, http.MethodGet,
		"/_matrix/federation/v1/event/"+eventID, nil)
	if status != http.StatusOK {
		t.Fatalf("%s asking %s for %s: %d %s", asker.name, holder.name, eventID, status, payload)
	}
	var answer struct {
		PDUs []json.RawMessage `json:"pdus"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		t.Fatalf("decoding the event answer from %s: %s", holder.name, payload)
	}
	if len(answer.PDUs) != 1 {
		t.Fatalf("%s answered with %d events for %s, wanting one", holder.name, len(answer.PDUs), eventID)
	}
	var pdu map[string]any
	if err := json.Unmarshal(answer.PDUs[0], &pdu); err != nil {
		t.Fatalf("decoding the PDU for %s: %v", eventID, err)
	}
	return pdu
}
