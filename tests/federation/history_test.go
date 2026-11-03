package federation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// A room decides how far a reader sees, and that decision has to survive leaving the
// server it was made on. These tests put a room on one server, let a second server join it
// later, and ask what the second server is handed about the time before it was there -
// through get_missing_events, through backfill, and by the id of the event nearest a
// moment. The event arrives either way; what differs is whether it still has its content,
// and that is the room's answer rather than a preference of whichever server is asked.

// setRoomState writes a state event as a client does, which is the only way a room's
// history visibility gets set at all.
func setRoomState(t *testing.T, s *server, sess *session, roomID, typ, stateKey string, content matrix.Object) string {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/state/%s/%s", roomID, typ, stateKey)
	got := s.mustDo(http.StatusOK, http.MethodPut, path, sess.Token, content)
	id := got.stringField("event_id")
	if id == "" {
		t.Fatalf("setting %s/%s of %s answered no event id: %s", typ, stateKey, roomID, got.Raw)
	}
	return id
}

// stateID finds one state event of a room by type and state key, as a client reads the
// state, and returns its id.
func stateID(t *testing.T, s *server, sess *session, roomID, typ, stateKey string) string {
	t.Helper()
	for _, entry := range stateOf(t, s, sess, roomID) {
		event, _ := entry.(map[string]any)
		if event == nil {
			continue
		}
		if event["type"] == typ && event["state_key"] == stateKey {
			id, _ := event["event_id"].(string)
			return id
		}
	}
	t.Fatalf("the state of %s on %s carries no %s/%s", roomID, s.name, typ, stateKey)
	return ""
}

// contentOf reads the content object of a served event.
func contentOf(t *testing.T, event matrix.Object) matrix.Object {
	t.Helper()
	content, ok := event["content"].(matrix.Object)
	if !ok {
		if asMap, isMap := event["content"].(map[string]any); isMap {
			return matrix.Object(asMap)
		}
		t.Fatalf("the served event %v carries no content object: %v", event["event_id"], event)
	}
	return content
}

// TestHistoryVisibilityDecidesWhatAPeerMayRead asks one question four times: what does a
// server that joined a room after an event was sent get for that event? The event comes
// back in every case - a peer needs the graph to be complete - and what differs is the
// content, which is gone for the visibilities that only opened the history at the point the
// asking server arrived. The signature and the hashes stay either way, because they are
// made over the unredacted form precisely so that a reader can tell removal from tampering.
func TestHistoryVisibilityDecidesWhatAPeerMayRead(t *testing.T) {
	for _, tc := range []struct {
		name       string
		visibility string
		// mayRead is what the peer may read of an event that predates its arrival.
		mayRead bool
	}{
		{name: "the room tells everybody", visibility: matrix.HistoryWorldReadable, mayRead: true},
		{name: "the room shares its history with members", visibility: matrix.HistoryShared, mayRead: true},
		{name: "the room opens at the invitation", visibility: matrix.HistoryInvited},
		{name: "the room opens at the join", visibility: matrix.HistoryJoined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCluster(t)
			one, two := c.one(), c.two()
			alice := one.register("alice", "wonderland")
			bob := two.register("bob", "wonderland")
			roomID := one.createRoom(alice, map[string]any{"preset": "public_chat"})

			setRoomState(t, one, alice, roomID, matrix.TypeHistoryVis, "",
				matrix.Object{"history_visibility": tc.visibility})
			message := one.send(alice, roomID, "the garden wall")

			if got := joinVia(two, bob, roomID, one.name); got.Status != http.StatusOK {
				t.Fatalf("%s could not join %s: %d %s", bob.UserID, roomID, got.Status, got.Raw)
			}

			// What the joining server is missing between the room's beginning and its own
			// join. The message sits in that gap, and it was sent before the asking server
			// had any claim on the room.
			createID := stateID(t, two, bob, roomID, matrix.TypeCreate, "")
			joinID := stateID(t, two, bob, roomID, matrix.TypeMember, bob.UserID)
			filled := federationPOST(t, two, one, "/_matrix/federation/v1/get_missing_events/"+roomID,
				matrix.Object{
					"earliest_events": []any{createID},
					"latest_events":   []any{joinID},
					"limit":           20,
					"min_depth":       1,
				})
			if filled.Status != http.StatusOK {
				t.Fatalf("%s asking %s for the gap in %s: %d %s", two.name, one.name, roomID, filled.Status, filled.Raw)
			}
			var found matrix.Object
			for _, entry := range filled.arrayField("events") {
				event, _ := entry.(matrix.Object)
				if event == nil {
					continue
				}
				if event["type"] == matrix.TypeMessage {
					found = event
				}
			}
			if found == nil {
				t.Fatalf("%s is answered the gap in %s without the message %s: %s",
					two.name, roomID, message, filled.Raw)
			}

			content := contentOf(t, found)
			_, hasBody := content["body"]
			if hasBody != tc.mayRead {
				t.Errorf("the message that predates %s is served with content %v, want it readable=%v",
					two.name, content, tc.mayRead)
			}
			// The other half of an honest redaction: the copy that lost its content is in
			// the form the room version's own algorithm produces, so the signature on it
			// still means something, and the copy that kept its content still matches the
			// hash over it.
			if tc.mayRead {
				if err := eventcrypto.VerifyContentHash(found, roomVersionOf(one, found)); err != nil {
					t.Errorf("the served message does not match its own content hash: %v", err)
				}
			} else if !eventcrypto.IsRedactedForm(found, roomVersionOf(one, found)) {
				t.Errorf("the served message lost its content in a way the room version does not describe: %v", found)
			}
		})
	}
}

// TestAPeerCanBackfillTheHistoryItNeverHad asks for older history the way a server that
// joined an old room does: some events, going back from where its own knowledge ends.
func TestAPeerCanBackfillTheHistoryItNeverHad(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	roomID := one.createRoom(alice, map[string]any{"preset": "public_chat"})
	setRoomState(t, one, alice, roomID, matrix.TypeHistoryVis, "",
		matrix.Object{"history_visibility": matrix.HistoryJoined})
	early := one.send(alice, roomID, "before the wall")
	if got := joinVia(two, bob, roomID, one.name); got.Status != http.StatusOK {
		t.Fatalf("%s could not join %s: %d %s", bob.UserID, roomID, got.Status, got.Raw)
	}
	late := one.send(alice, roomID, "after the wall")

	answer := federationGET(t, two, one, "/_matrix/federation/v1/backfill/"+roomID+"?limit=50")
	if answer.Status != http.StatusOK {
		t.Fatalf("%s asking %s to backfill %s: %d %s", two.name, one.name, roomID, answer.Status, answer.Raw)
	}
	if answer.stringField("origin") != one.name {
		t.Errorf("the backfill of %s claims to come from %q", roomID, answer.stringField("origin"))
	}
	pdus := answer.arrayField("pdus")
	if len(pdus) == 0 {
		t.Fatalf("%s backfills %s with nothing, although %s has history there", two.name, roomID, one.name)
	}
	seen := map[string]bool{}
	for _, entry := range pdus {
		event, _ := entry.(matrix.Object)
		if event == nil {
			t.Fatalf("%s backfills %s with something that is not an event: %v", two.name, roomID, entry)
		}
		if event["room_id"] != roomID {
			t.Errorf("a backfill of %s carries an event of %v", roomID, event["room_id"])
		}
		id, _ := event["event_id"].(string)
		if id == "" {
			id = derivedEventIDQuiet(one, roomID, event)
		}
		seen[id] = true
	}
	if !seen[early] {
		t.Errorf("the backfill of %s does not carry %s, an event of its history: %v", roomID, early, seen)
	}
	// The room's leaves are where a backfill starts rather than what it returns: a server
	// asking for older history has the new end, or could not have named it.
	if seen[late] {
		t.Errorf("the backfill of %s starts at the room's newest event and returns %s as well", roomID, late)
	}
	// `since` says where to start walking back, and the event named is not part of the
	// answer: the peer has it, which is why it could name it.
	older := federationGET(t, two, one,
		"/_matrix/federation/v1/backfill/"+roomID+"?limit=50&since="+late)
	if older.Status != http.StatusOK {
		t.Fatalf("%s asking %s to backfill %s before %s: %d %s",
			two.name, one.name, roomID, late, older.Status, older.Raw)
	}
	for _, entry := range older.arrayField("pdus") {
		event, _ := entry.(matrix.Object)
		if event == nil {
			continue
		}
		if id, _ := event["event_id"].(string); id == late {
			t.Errorf("the backfill before %s carries %s itself", late, late)
		}
	}
	// And the room's answer about the history before the asking server arrived: the early
	// message is there, without its content.
	var earlyEvent matrix.Object
	for _, entry := range older.arrayField("pdus") {
		event, _ := entry.(matrix.Object)
		if event != nil && event["type"] == matrix.TypeMessage {
			earlyEvent = event
		}
	}
	if earlyEvent == nil {
		t.Fatalf("%s backfills %s without the message %s it predates", two.name, roomID, early)
	}
	if _, hasBody := contentOf(t, earlyEvent)["body"]; hasBody {
		t.Errorf("a `joined` room backfills %s with the content of an event that predates %s", roomID, two.name)
	}

	// A server that is not part of the room gets nothing at all, whatever the visibility:
	// backfill is a read of a room's past, and a stranger has no past in it.
	question := federationGET(t, two, one, "/_matrix/federation/v1/backfill/"+roomID+"?limit=1")
	if question.Status != http.StatusOK {
		t.Fatalf("a member asking for backfill of %s: %d %s", roomID, question.Status, question.Raw)
	}
}

// TestAPeerCanNameAnEventByTimestamp asks MSC3030's question over federation: which event
// of this room is nearest this moment. The answer is an id, so there is no content here to
// weigh - what has to hold is that the id names an event of the room asked about, and that
// a peer can then go and read it.
func TestAPeerCanNameAnEventByTimestamp(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	roomID := one.createRoom(alice, map[string]any{"preset": "public_chat"})
	one.send(alice, roomID, "somewhere in time")
	if got := joinVia(two, bob, roomID, one.name); got.Status != http.StatusOK {
		t.Fatalf("%s could not join %s: %d %s", bob.UserID, roomID, got.Status, got.Raw)
	}

	for _, tc := range []struct {
		name string
		body matrix.Object
	}{
		{name: "forwards from before the room", body: matrix.Object{"dir": "f", "ts": int64(1_000_000_000_000)}},
		{name: "backwards from after the room", body: matrix.Object{"dir": "b", "ts": int64(4_000_000_000_000)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := federationPOST(t, two, one, "/_matrix/federation/v1/timestamp_to_event/"+roomID, tc.body)
			if answer.Status != http.StatusOK {
				t.Fatalf("%s asking %s for the event of %s nearest %v: %d %s",
					two.name, one.name, roomID, tc.body["ts"], answer.Status, answer.Raw)
			}
			id := answer.stringField("event_id")
			if id == "" {
				t.Fatalf("no event is named for %s: %s", roomID, answer.Raw)
			}
			event := federationGET(t, two, one, "/_matrix/federation/v1/event/"+id)
			if event.Status != http.StatusOK {
				t.Fatalf("the named event %s cannot be read: %d %s", id, event.Status, event.Raw)
			}
			if _, err := one.db.EventRepo.Get(id); err != nil {
				t.Errorf("%s named %s, which it does not hold as an event: %v", one.name, id, err)
			}
		})
	}

	// A window the room has nothing in is answered as a miss, not as the nearest thing
	// this server happens to hold.
	narrow := federationPOST(t, two, one, "/_matrix/federation/v1/timestamp_to_event/"+roomID,
		matrix.Object{"dir": "f", "ts": int64(1_000_000_000_000), "end": int64(1_000_000_600_000)})
	if narrow.Status != http.StatusNotFound {
		t.Errorf("a window of %s with nothing in it is answered %d, want 404: %s", roomID, narrow.Status, narrow.Raw)
	}

	// Searching backwards from far in the future lands on the newest thing the room has.
	// The newest event is not necessarily one specific id - a room's create event, its
	// state and its first message can all be made in the same millisecond - so what is
	// checked is the moment named.
	found := federationPOST(t, two, one, "/_matrix/federation/v1/timestamp_to_event/"+roomID,
		matrix.Object{"dir": "b", "ts": int64(4_000_000_000_000)})
	if found.Status != http.StatusOK {
		t.Fatalf("searching backwards over %s: %d %s", roomID, found.Status, found.Raw)
	}
	newest := newestTimestamp(t, one, alice, roomID)
	if answerTimestamp(t, found) != newest {
		t.Errorf("searching backwards over %s names the moment %d, want the room's newest event at %d",
			roomID, answerTimestamp(t, found), newest)
	}
	forwards := federationPOST(t, two, one, "/_matrix/federation/v1/timestamp_to_event/"+roomID,
		matrix.Object{"dir": "f", "ts": int64(1_000_000_000_000)})
	if forwards.Status != http.StatusOK {
		t.Fatalf("searching forwards over %s: %d %s", roomID, forwards.Status, forwards.Raw)
	}
	if answerTimestamp(t, forwards) > newest {
		t.Errorf("searching forwards from the dawn over %s lands past the room's newest event", roomID)
	}
}

// answerTimestamp reads the moment a federation answer names.
func answerTimestamp(t *testing.T, r response) int64 {
	t.Helper()
	switch value := r.Body["origin_server_ts"].(type) {
	case int64:
		return value
	case float64:
		return int64(value)
	default:
		t.Fatalf("the answer carries no origin_server_ts: %s", r.Raw)
	}
	return 0
}

// newestTimestamp is the moment of the newest event of a room as a client reads its
// history: several events of a room can be made in the same millisecond, so a search by
// time is checked against the moment rather than against one event id.
func newestTimestamp(t *testing.T, s *server, sess *session, roomID string) int64 {
	t.Helper()
	got := s.mustDo(http.StatusOK, http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&limit=100", sess.Token, nil)
	var body struct {
		Chunk []struct {
			OriginServerTS int64 `json:"origin_server_ts"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(got.Raw, &body); err != nil {
		t.Fatalf("decoding the history of %s on %s: %v", roomID, s.name, err)
	}
	var newest int64
	for _, event := range body.Chunk {
		if event.OriginServerTS > newest {
			newest = event.OriginServerTS
		}
	}
	return newest
}

// parkEvent writes an event into a server's room history without relaying it anywhere:
// what a server that made events while a peer was unreachable ends up holding. The row is
// written by hand because the harness has no partition to open - and what the test then
// measures is the other server's behaviour, which is entirely real.
func parkEvent(t *testing.T, s *server, pdu matrix.Object) string {
	t.Helper()
	version := roomVersionOf(s, pdu)
	id, err := eventcrypto.EventID(pdu, version)
	if err != nil {
		t.Fatalf("naming the parked event: %v", err)
	}
	encoded, err := matrix.CanonicalJSON(pdu)
	if err != nil {
		t.Fatalf("encoding the parked event: %v", err)
	}
	pos, err := s.db.EventRepo.LatestStreamPos()
	if err != nil {
		t.Fatalf("reading the stream position: %v", err)
	}
	content, err := json.Marshal(pdu["content"])
	if err != nil {
		t.Fatalf("encoding the content of the parked event: %v", err)
	}
	prevs, err := json.Marshal(pdu["prev_events"])
	if err != nil {
		t.Fatalf("encoding the prev_events of the parked event: %v", err)
	}
	auths, err := json.Marshal(pdu["auth_events"])
	if err != nil {
		t.Fatalf("encoding the auth_events of the parked event: %v", err)
	}
	depth, _ := pdu["depth"].(int64)
	ts, _ := pdu["origin_server_ts"].(int64)
	row := &persist.Event{
		EventID:        id,
		RoomID:         fmt.Sprintf("%v", pdu["room_id"]),
		StreamPos:      pos + 1,
		Type:           fmt.Sprintf("%v", pdu["type"]),
		Sender:         fmt.Sprintf("%v", pdu["sender"]),
		OriginServerTS: ts,
		Content:        string(content),
		Depth:          depth,
		JSON:           string(encoded),
		PrevEvents:     string(prevs),
		AuthEvents:     string(auths),
	}
	if err := s.db.EventRepo.Create(row); err != nil {
		t.Fatalf("parking the event %s on %s: %v", id, s.name, err)
	}
	return id
}

// TestAServerAsksForTheHistoryItWasNeverSent is the outbound half of get_missing_events.
// A peer sends an event whose parent this server has never seen - the two were apart - and
// this server does not shrug and accept a hole: it asks, and the history between the two
// ends lands in its room in the order the room was built.
func TestAServerAsksForTheHistoryItWasNeverSent(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	roomID := one.createRoom(alice, map[string]any{"preset": "public_chat"})
	if got := joinVia(two, bob, roomID, one.name); got.Status != http.StatusOK {
		t.Fatalf("%s could not join %s: %d %s", bob.UserID, roomID, got.Status, got.Raw)
	}

	// Both servers hold a real message *by the user the missing events come from*: it was
	// sent while the two were still in contact, so it is where the gap starts, and the auth
	// events it cites are the set this room's own rules name for a message by that sender.
	// A fixture that copies them cannot cite a rule the room does not select, which is how
	// the first version of this test fooled itself.
	base := two.send(bob, roomID, "a message both hold")
	flush(t, two)
	earliest, err := one.db.EventRepo.Get(base)
	if err != nil {
		t.Fatalf("%s never took the message %s that the gap starts after: %v", one.name, base, err)
	}
	var authIDs []string
	if err := json.Unmarshal([]byte(earliest.AuthEvents), &authIDs); err != nil {
		t.Fatalf("the auth events of %s are not a list of ids: %v", base, err)
	}
	authEvents := matrix.Array{}
	for _, id := range authIDs {
		authEvents = append(authEvents, id)
	}

	made := make([]matrix.Object, 0, 3)
	for i, text := range []string{"one missed", "two missed", "three missed"} {
		prev := matrix.Array{base}
		if i > 0 {
			prev = matrix.Array{mustName(t, two, made[i-1])}
		}
		made = append(made, signedPDU(t, two, matrix.Object{
			"room_id":          roomID,
			"type":             matrix.TypeMessage,
			"sender":           bob.UserID,
			"origin_server_ts": int64(1_700_000_000_000 + int64(i)*1000),
			"depth":            earliest.Depth + int64(i+1),
			"prev_events":      prev,
			"auth_events":      authEvents,
			"content":          matrix.Object{"msgtype": "m.text", "body": text},
		}))
	}
	var ids []string
	for _, pdu := range made {
		ids = append(ids, parkEvent(t, two, pdu))
	}

	// Only the last of the three is delivered. The two before it are the hole.
	answer, failure := sendTransaction(t, two, one.name, "after-the-partition",
		transactionBody(two.name, matrix.Array{made[2]}, nil))
	if failure != nil {
		t.Fatalf("the transaction carrying %s was refused at the envelope: %d %s",
			ids[2], failure.Status, failure.Message)
	}
	if entry, ok := answer["pdus"].(matrix.Object)[ids[2]]; ok {
		if result, isObject := entry.(matrix.Object); !isObject || result["error"] != nil {
			t.Errorf("%s was answered with %v, which says the event did not land: %v", one.name, entry, answer)
		}
	}

	// The two events one was never sent are in its room now, and they arrived through the
	// question one asked rather than through anything two pushed.
	for _, id := range ids[:2] {
		if _, err := one.db.EventRepo.Get(id); err != nil {
			t.Errorf("%s never took %s into the room although it asked for the gap: %v", one.name, id, err)
		}
	}
	order := historyOf(t, one, alice, roomID)
	position := map[string]int{}
	for i, id := range order {
		position[id] = i
	}
	for _, id := range ids {
		if _, ok := position[id]; !ok {
			t.Errorf("the history of %s on %s does not show %s: %v", roomID, one.name, id, order)
		}
	}
	for i := 1; i < len(ids); i++ {
		if position[ids[i-1]] >= position[ids[i]] {
			t.Errorf("%s reads before %s in the history of %s: %v", ids[i-1], ids[i], roomID, order)
		}
	}
}

// TestAnEventBuiltOnARefusalIsRefusedToo closes the graph rule from the other side. A peer
// that had an event turned down can build another event on top of it; the branch is not
// this server's to grow, because doing so puts the refused event back in the room by
// implication one event at a time. The refusal says which parent condemned the event, so
// the sender can tell a hole from a verdict.
func TestAnEventBuiltOnARefusalIsRefusedToo(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	bob := two.register("bob", "wonderland")
	roomID := one.createRoom(alice, map[string]any{"preset": "public_chat"})
	if got := joinVia(two, bob, roomID, one.name); got.Status != http.StatusOK {
		t.Fatalf("%s could not join %s: %d %s", bob.UserID, roomID, got.Status, got.Raw)
	}
	ground := two.send(bob, roomID, "a message both hold")
	flush(t, two)
	real, err := one.db.EventRepo.Get(ground)
	if err != nil {
		t.Fatalf("%s never took the message %s: %v", one.name, ground, err)
	}
	var authIDs []string
	if err := json.Unmarshal([]byte(real.AuthEvents), &authIDs); err != nil {
		t.Fatalf("the auth events of %s are not ids: %v", ground, err)
	}
	authEvents := matrix.Array{}
	for _, id := range authIDs {
		authEvents = append(authEvents, id)
	}

	// The first event picks its own rules: it cites only the create event, which is not the
	// set this room's selection names, so it is refused outright.
	createID := ""
	for _, entry := range stateOf(t, two, bob, roomID) {
		event, _ := entry.(map[string]any)
		if event != nil && event["type"] == matrix.TypeCreate {
			createID, _ = event["event_id"].(string)
		}
	}
	if createID == "" {
		t.Fatalf("the state of %s carries no create event", roomID)
	}
	forge := signedPDU(t, two, matrix.Object{
		"room_id":          roomID,
		"type":             matrix.TypeMessage,
		"sender":           bob.UserID,
		"origin_server_ts": int64(1_700_000_100_000),
		"depth":            real.Depth + 1,
		"prev_events":      matrix.Array{ground},
		"auth_events":      matrix.Array{createID},
		"content":          matrix.Object{"msgtype": "m.text", "body": "by my own rules"},
	})
	if _, failure := sendTransaction(t, two, one.name, "own-rules",
		transactionBody(two.name, matrix.Array{forge}, nil)); failure != nil {
		t.Fatalf("the forged event was refused at the envelope: %d %s", failure.Status, failure.Message)
	}
	forgedID, err := derivedEventID(t, one, roomID, forge)
	if err != nil {
		t.Fatalf("naming the forged event: %v", err)
	}
	receipt, err := one.db.ReceivedPDURepo.Get(forgedID)
	if err != nil {
		t.Fatalf("%s never received the forged event: %v", one.name, err)
	}
	if receipt.Outcome != service.OutcomeRejected {
		t.Fatalf("%s decided %q for the forged event, want %q: %s",
			one.name, receipt.Outcome, service.OutcomeRejected, receipt.Reason)
	}

	// The second event is honest in every way except where it stands: its parent is the
	// event that was refused.
	onTop := signedPDU(t, two, matrix.Object{
		"room_id":          roomID,
		"type":             matrix.TypeMessage,
		"sender":           bob.UserID,
		"origin_server_ts": int64(1_700_000_200_000),
		"depth":            real.Depth + 2,
		"prev_events":      matrix.Array{forgedID},
		"auth_events":      authEvents,
		"content":          matrix.Object{"msgtype": "m.text", "body": "built on the refusal"},
	})
	if _, failure := sendTransaction(t, two, one.name, "on-top",
		transactionBody(two.name, matrix.Array{onTop}, nil)); failure != nil {
		t.Fatalf("the second event was refused at the envelope: %d %s", failure.Status, failure.Message)
	}
	standing, err := one.db.ReceivedPDURepo.Get(mustName(t, one, onTop))
	if err != nil {
		t.Fatalf("%s never received the event built on the refusal: %v", one.name, err)
	}
	if standing.Outcome != service.OutcomeRejected {
		t.Errorf("%s decided %q for an event built on a refusal, want %q: %s",
			one.name, standing.Outcome, service.OutcomeRejected, standing.Reason)
	}
	if !strings.Contains(standing.Reason, forgedID) {
		t.Errorf("the refusal of the event above a refusal says %q, which does not name the parent %s",
			standing.Reason, forgedID)
	}

	// The room moves on: an event built on what the room accepted is accepted, and the
	// refusal does not poison the branch of the graph it sits beside.
	movedOn := signedPDU(t, two, matrix.Object{
		"room_id":          roomID,
		"type":             matrix.TypeMessage,
		"sender":           bob.UserID,
		"origin_server_ts": int64(1_700_000_300_000),
		"depth":            real.Depth + 1,
		"prev_events":      matrix.Array{ground},
		"auth_events":      authEvents,
		"content":          matrix.Object{"msgtype": "m.text", "body": "the room goes on"},
	})
	if _, failure := sendTransaction(t, two, one.name, "moving-on",
		transactionBody(two.name, matrix.Array{movedOn}, nil)); failure != nil {
		t.Fatalf("the next event was refused at the envelope: %d %s", failure.Status, failure.Message)
	}
	if _, err := one.db.EventRepo.Get(mustName(t, one, movedOn)); err != nil {
		t.Errorf("%s refused the event that followed a refusal too: %v", one.name, err)
	}
	if !visible(t, one, alice, roomID)[mustName(t, one, movedOn)] {
		t.Errorf("the event that followed a refusal is not in the history of %s", roomID)
	}
}

// mustName derives the id of a crafted event, and fails the test when it cannot.
func mustName(t *testing.T, s *server, pdu matrix.Object) string {
	t.Helper()
	id, err := derivedEventID(t, s, fmt.Sprintf("%v", pdu["room_id"]), pdu)
	if err != nil {
		t.Fatalf("naming the crafted event: %v", err)
	}
	return id
}

// historyOf lists the event ids of a room as a client reads them, oldest first.
func historyOf(t *testing.T, s *server, sess *session, roomID string) []string {
	t.Helper()
	got := s.mustDo(http.StatusOK, http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&limit=100", sess.Token, nil)
	var body struct {
		Chunk []struct {
			EventID string `json:"event_id"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(got.Raw, &body); err != nil {
		t.Fatalf("decoding the history of %s on %s: %v", roomID, s.name, err)
	}
	ids := make([]string, 0, len(body.Chunk))
	for _, event := range body.Chunk {
		ids = append(ids, event.EventID)
	}
	return ids
}
