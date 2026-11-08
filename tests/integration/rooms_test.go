package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// createRoom posts a createRoom request and returns the room ID, failing the test if
// the server did not accept it.
func createRoom(t *testing.T, h *harness, token string, body map[string]any) string {
	t.Helper()
	res := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom", body, token), http.StatusOK)
	roomID, _ := res.decoded(t)["room_id"].(string)
	if roomID == "" {
		t.Fatalf("createRoom returned no room_id: %s", res.body)
	}
	return roomID
}

// send puts an event and returns the response.
func send(t *testing.T, h *harness, roomID, eventType, txnID, content string, token string) response {
	t.Helper()
	path := "/_matrix/client/v3/rooms/" + roomID + "/send/" + eventType + "/" + txnID
	return h.raw(t, http.MethodPut, path, []byte(content), token)
}

// TestCreateRoomAndSend exercises the first end-to-end write path: an authenticated
// client creates a room and posts a message into it.
func TestCreateRoomAndSend(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, token, _ := h.register(t, "alice", "pw-alice", "")
	roomID := createRoom(t, h, token, map[string]any{
		"preset": "public_chat",
		"name":   "Lounge",
		"topic":  "everything",
	})
	if !strings.HasPrefix(roomID, "!") || !strings.HasSuffix(roomID, ":hs1") {
		t.Errorf("room ID %q is not a room ID on this server", roomID)
	}

	first := send(t, h, roomID, "m.room.message", "txn-1", `{"msgtype":"m.text","body":"hello"}`, token)
	h.mustStatus(t, first, http.StatusOK)
	eventID, _ := first.decoded(t)["event_id"].(string)
	if !strings.HasPrefix(eventID, "$") {
		t.Fatalf("send returned event_id %q, want a $-prefixed ID", eventID)
	}

	// The same transaction ID from the same device must return the original event,
	// not create a second one.
	repeat := send(t, h, roomID, "m.room.message", "txn-1", `{"msgtype":"m.text","body":"hello"}`, token)
	h.mustStatus(t, repeat, http.StatusOK)
	if repeated, _ := repeat.decoded(t)["event_id"].(string); repeated != eventID {
		t.Errorf("a retried transaction produced %q, want the original %q", repeated, eventID)
	}

	// A different transaction ID is a different event.
	second := send(t, h, roomID, "m.room.message", "txn-2", `{"msgtype":"m.text","body":"again"}`, token)
	h.mustStatus(t, second, http.StatusOK)
	if other, _ := second.decoded(t)["event_id"].(string); other == eventID {
		t.Errorf("a second transaction returned the same event ID %q", other)
	}
}

// TestCreatedEventIsVerifiable checks the stored event against the same rules another
// server would apply: its event ID is the hash of what is stored, it carries this
// server's signature, and the authorisation rules accept it against the state and the
// auth context that were persisted with it.
//
// This is the test that turns "the code ran" into "the event we produced is one the
// protocol accepts".
func TestCreatedEventIsVerifiable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, token, _ := h.register(t, "carol", "pw-carol", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})
	res := send(t, h, roomID, "m.room.message", "txn-verify", `{"msgtype":"m.text","body":"signed"}`, token)
	h.mustStatus(t, res, http.StatusOK)
	eventID, _ := res.decoded(t)["event_id"].(string)

	ctx := context.Background()
	row, err := h.app.DB.Events.ByID(ctx, eventID)
	if err != nil {
		t.Fatalf("the event the server returned is not stored: %v", err)
	}
	event, err := events.Parse(row.JSON)
	if err != nil {
		t.Fatalf("the stored event does not parse: %v", err)
	}

	if hashes, ok := event["hashes"].(map[string]any); !ok || hashes["sha256"] == nil {
		t.Errorf("the stored event has no content hash: %v", event["hashes"])
	}
	signatures, ok := event["signatures"].(map[string]any)
	if !ok {
		t.Fatalf("the stored event has no signatures")
	}
	// Federation key IDs carry the algorithm prefix, which is what a peer looks for.
	if byOrigin, ok := signatures["hs1"].(map[string]any); !ok || byOrigin["ed25519:v1"] == nil {
		t.Errorf("the stored event is not signed by this server under its current key: %v", signatures)
	}

	rules := roomversion.MustGet(roomVersionOf(t, h, roomID))
	recomputed, err := events.EventID(rules, event)
	if err != nil {
		t.Fatalf("cannot recompute the event ID: %v", err)
	}
	if recomputed != eventID {
		t.Errorf("the stored event hashes to %s, but is stored as %s", recomputed, eventID)
	}

	// Authorise the event against the state that was current when it was created.
	snapshot := newStateSnapshot(t, h, roomID)
	if err := eventauth.Check(rules, event, snapshot, eventauth.Opts{}); err != nil {
		t.Errorf("the event this server created is refused by its own authorisation rules: %v", err)
	}

	authContext, err := h.app.DB.State.AuthContext(ctx, eventID)
	if err != nil {
		t.Fatalf("cannot load the authorisation context: %v", err)
	}
	if len(authContext) == 0 {
		t.Errorf("no authorisation context was stored for the event")
	}
}

// TestSendRejectsNonMembers checks that the authorisation rules reach the endpoint: a
// registered user who has not joined the room cannot post into it.
func TestSendRejectsNonMembers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "dave", "pw-dave", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	_, outsiderToken, _ := h.register(t, "erin", "pw-erin", "")
	res := send(t, h, roomID, "m.room.message", "txn-out", `{"msgtype":"m.text","body":"let me in"}`, outsiderToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("a non-member posting to a room: status %d, want 403 (body %s)", res.status, res.body)
	}
	if code := res.errcode(t); code != "M_FORBIDDEN" {
		t.Errorf("errcode = %q, want M_FORBIDDEN", code)
	}
}

// TestSendWithoutMembershipEndpoints checks that membership is not reachable through
// /send, which would be a second path to a state change with weaker checks.
func TestSendWithoutMembershipEndpoints(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "frank", "pw-frank", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})

	res := send(t, h, roomID, "m.room.member", "txn-member", `{"membership":"join"}`, token)
	if res.status != http.StatusBadRequest {
		t.Fatalf("sending m.room.member through /send: status %d, want 400 (body %s)", res.status, res.body)
	}
	if code := res.errcode(t); code != "M_UNRECOGNIZED" {
		t.Errorf("errcode = %q, want M_UNRECOGNIZED", code)
	}
}

// TestRoomSendErrors pins the error codes a client acts on.
func TestRoomSendErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "grace", "pw-grace", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})

	cases := []struct {
		name      string
		method    string
		path      string
		body      string
		wantCode  string
		wantState int
	}{
		{"unknown room", http.MethodPut, "/_matrix/client/v3/rooms/!nothere:hs1/send/m.room.message/txn-x",
			`{"body":"x"}`, "M_NOT_FOUND", http.StatusNotFound},
		{"malformed room ID", http.MethodPut, "/_matrix/client/v3/rooms/notaroom/send/m.room.message/txn-x",
			`{"body":"x"}`, "M_INVALID_PARAM", http.StatusBadRequest},
		{"malformed event type", http.MethodPut, "/_matrix/client/v3/rooms/" + roomID + "/send/not%20an%20event/txn-x",
			`{"body":"x"}`, "M_INVALID_PARAM", http.StatusBadRequest},
		{"body is not an object", http.MethodPut, "/_matrix/client/v3/rooms/" + roomID + "/send/m.room.message/txn-x",
			`["not","an","object"]`, "M_BAD_JSON", http.StatusBadRequest},
		{"body is not JSON", http.MethodPut, "/_matrix/client/v3/rooms/" + roomID + "/send/m.room.message/txn-x",
			`{`, "M_NOT_JSON", http.StatusBadRequest},
		{"state path too deep", http.MethodPut, "/_matrix/client/v3/rooms/" + roomID + "/state/m.room.topic/a/b",
			`{"topic":"x"}`, "M_INVALID_PARAM", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.raw(t, tc.method, tc.path, []byte(tc.body), token)
			if res.status != tc.wantState {
				t.Fatalf("status = %d, want %d (body %s)", res.status, tc.wantState, res.body)
			}
			if tc.wantCode != "" {
				if code := res.errcode(t); code != tc.wantCode {
					t.Errorf("errcode = %q, want %q", code, tc.wantCode)
				}
			}
		})
	}
}

// TestCreateRoomVersionNegotiation checks that a version this server cannot create a
// room in is refused with the protocol error, and that the advertised versions are the
// ones it can actually create.
func TestCreateRoomVersionNegotiation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "heidi", "pw-heidi", "")

	capabilities := h.mustStatus(t, h.get("/_matrix/client/v3/capabilities", token), http.StatusOK).decoded(t)
	roomVersions, _ := capabilities["capabilities"].(map[string]any)["m.room_versions"].(map[string]any)
	available, _ := roomVersions["available"].(map[string]any)
	defaultVersion, _ := roomVersions["default"].(string)
	if defaultVersion == "" || available[defaultVersion] == nil {
		t.Fatalf("the advertised default %q is not among the advertised versions %v", defaultVersion, available)
	}
	for version := range available {
		roomID := createRoom(t, h, token, map[string]any{"room_version": version})
		if got := roomVersionOf(t, h, roomID); got != version {
			t.Errorf("a room created with room_version %q is stored as %q", version, got)
		}
	}

	// A version that is nothing at all. Room version 12 is absent from this list because it is
	// implemented and offered, and it is the loop above that proves the claim: that loop creates a
	// room in every advertised version and reads the version back, and 12 is now one of them.
	// Version 12 also gets a test of its own, because what it means to be a version 12 room — an
	// identifier with no server in it, a create event that does not name its own room, a creator who
	// is not in the power levels — cannot be checked by round-tripping a version string.
	for _, rejected := range []string{"com.example.not-a-version"} {
		res := h.post(t, "/_matrix/client/v3/createRoom", map[string]any{"room_version": rejected}, token)
		if res.status != http.StatusBadRequest {
			t.Errorf("room_version %q: status %d, want 400 (body %s)", rejected, res.status, res.body)
		}
		if code := res.errcode(t); code != "M_UNSUPPORTED_ROOM_VERSION" {
			t.Errorf("room_version %q: errcode %q, want M_UNSUPPORTED_ROOM_VERSION", rejected, code)
		}
	}
}

// TestCreateRoomInRoomVersionTwelve checks the things that make a room version 12 room a version 12
// room, none of which a version string round-trip can see.
//
// A room is named after its create event here, so the identifier this server returns has to be the hash
// of the event it also stores — and not the hash of anything else it might have stored, since the same
// bytes under a different name would be a different room. The create event may not carry a room
// identifier (rule 1.2 refuses one), so what names the room has to be recoverable from the event alone.
// And the room's first power levels event may not name its creator (rule 10.4), so the defaults have to
// be written without them — which means the creator's authority in a version 12 room is never a fact
// the room states, and every one of those rules has to hold of the events this server signs itself.
func TestCreateRoomInRoomVersionTwelve(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "v12", "pw-v12-pass", "")

	roomID := createRoom(t, h, token, map[string]any{"room_version": "12"})
	if strings.ContainsRune(roomID, ':') {
		t.Errorf("a room version 12 identifier names no server, got %q", roomID)
	}
	if got := roomVersionOf(t, h, roomID); got != "12" {
		t.Fatalf("the room is stored as version %q", got)
	}

	rules := roomversion.MustGet("12")
	snapshot := newStateSnapshot(t, h, roomID)
	key := eventauth.Key{Type: eventauth.TypeCreate, StateKey: ""}
	create, ok := snapshot.byKey[key]
	if !ok {
		t.Fatalf("the room has no create event in its state")
	}
	if carried, present := create["room_id"].(string); present && carried != "" {
		t.Errorf("the create event of a room version 12 room carries room_id %q; rule 1.2 refuses an event that does", carried)
	}
	if legacy, present := create["content"].(map[string]any)["creator"]; present {
		t.Errorf("the create event carries the legacy creator property (%v), which room versions 11 and later leave to the sender", legacy)
	}
	if version, _ := create["content"].(map[string]any)["room_version"].(string); version != "12" {
		t.Errorf("the create event names room version %v", version)
	}
	createEventID, _ := create["event_id"].(string)
	if want := "$" + roomID[1:]; createEventID != want {
		t.Errorf("the room is named %q but its create event is %q: the room identifier is that event's hash with a different sigil", roomID, createEventID)
	}

	// The room's own events, checked the way a receiving server checks them: this server built and
	// signed them, which is the reason to be least suspicious and the least good reason to be.
	//
	// The distinction in the middle of that loop is the one that matters, and it is not specific to this
	// room version. An event is authorised against the state that authorised it — the auth events it
	// carries — and not against the room as it stands afterwards. Checking the room's creator's join
	// against the finished state asks it to have cited a power levels event that did not exist when it
	// was made, and every server in the room would then be refusing the room's own history. So the
	// state below comes from each event's own citations, while identifiers still resolve across the
	// whole room, because a receiver holding the room can read the create event its identifier names.
	for stateKey, event := range snapshot.byKey {
		eventID := mustEventID(t, event)
		stored := storedEvent(t, h, eventID)
		recomputed, err := events.EventID(rules, stored)
		if err != nil {
			t.Fatalf("cannot recompute the identifier of %s: %v", stateKey, err)
		}
		if recomputed != eventID {
			t.Errorf("%s is stored as %s and hashes to %s", stateKey, eventID, recomputed)
		}
		received := &testSnapshot{byKey: map[eventauth.Key]map[string]any{}, byID: snapshot.byID, h: h}
		for _, cited := range events.ReferencedIDs(stored, "auth_events") {
			body := storedEvent(t, h, cited)
			received.byID[cited] = body
			stateKey, isState := body["state_key"].(string)
			if isState {
				received.byKey[eventauth.Key{Type: stringOrEvent(body["type"]), StateKey: stateKey}] = body
			}
		}
		if err := eventauth.Check(rules, stored, received, eventauth.Opts{}); err != nil {
			t.Errorf("the %s event this server created for a room version 12 room is refused by that version's rules: %v", stateKey, err)
		}
	}

	// The power levels of a room whose creator cannot be listed: nobody at all, and a room upgrade
	// above what an administrator can be given.
	powerLevels, ok := snapshot.byKey[eventauth.Key{Type: eventauth.TypePowerLevels, StateKey: ""}]
	if !ok {
		t.Fatalf("the room has no power levels event")
	}
	content, _ := powerLevels["content"].(map[string]any)
	users, _ := content["users"].(map[string]any)
	if len(users) != 0 {
		t.Errorf("a room version 12 room lists %v in its power levels; a creator may not be listed there", users)
	}
	if events_, _ := content["events"].(map[string]any); events_["m.room.tombstone"] == nil {
		t.Errorf("a room version 12 room leaves m.room.tombstone at the default; the room's upgrade has to sit above what an administrator can reach")
	} else if tombstone, _ := events_["m.room.tombstone"].(json.Number); tombstone != "150" {
		t.Errorf("m.room.tombstone requires %v, want 150", events_["m.room.tombstone"])
	}

	// And it is a room: an event goes in and comes back out.
	if res := send(t, h, roomID, "m.room.message", "v12-first", `{"msgtype":"m.text","body":"hello from a hashed room"}`, token); res.status != http.StatusOK {
		t.Fatalf("sending a message to the version 12 room: %d %s", res.status, res.body)
	}
	messages := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+url.PathEscape(roomID)+"/messages?dir=b&limit=10", token), http.StatusOK).decoded(t)
	if chunk, _ := messages["chunk"].([]any); len(chunk) == 0 {
		t.Fatalf("the version 12 room has no history: %v", messages)
	}
}

func stringOrEvent(value any) string {
	text, _ := value.(string)
	return text
}

func mustEventID(t *testing.T, event map[string]any) string {
	t.Helper()
	eventID, _ := event["event_id"].(string)
	if eventID == "" {
		t.Fatalf("a stored state event carries no identifier: %v", event["type"])
	}
	return eventID
}

// TestCreateRoomRequiresAuth keeps room creation behind a token: an unauthenticated
// client must not be able to make rooms on this server.
func TestCreateRoomRequiresAuth(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	res := h.post(t, "/_matrix/client/v3/createRoom", map[string]any{}, "")
	if res.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated createRoom: status %d, want 401", res.status)
	}
	if code := res.errcode(t); code != "M_MISSING_TOKEN" {
		t.Errorf("errcode = %q, want M_MISSING_TOKEN", code)
	}
}

// TestStateEventSend covers the state endpoint, including the empty state key that
// addresses room-wide state such as the topic.
func TestStateEventSend(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "ivan", "pw-ivan", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})

	res := h.raw(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic/",
		[]byte(`{"topic":"changed"}`), token)
	h.mustStatus(t, res, http.StatusOK)
	eventID, _ := res.decoded(t)["event_id"].(string)
	if eventID == "" {
		t.Fatalf("state send returned no event_id: %s", res.body)
	}

	// A user with no power in the room cannot change room state.
	_, outsider, _ := h.register(t, "judy", "pw-judy", "")
	blocked := h.raw(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic/",
		[]byte(`{"topic":"mine now"}`), outsider)
	if blocked.status != http.StatusForbidden {
		t.Errorf("an outsider setting state: status %d, want 403 (body %s)", blocked.status, blocked.body)
	}
}

// roomVersionOf reads the stored room version. It exists so a test asserts on what the
// server recorded, not on what it was asked for.
func roomVersionOf(t *testing.T, h *harness, roomID string) string {
	t.Helper()
	room, err := h.app.DB.Rooms.ByRoomID(context.Background(), roomID)
	if err != nil {
		t.Fatalf("the room the server created is not stored: %v", err)
	}
	return room.RoomVersion
}

// stateSnapshot builds the authorisation view of a room's stored state, so a test can
// re-authorise a produced event the way a receiving server would.
func newStateSnapshot(t *testing.T, h *harness, roomID string) *testSnapshot {
	t.Helper()
	rows, err := h.app.DB.State.All(context.Background(), roomID)
	if err != nil {
		t.Fatalf("cannot load room state: %v", err)
	}
	snapshot := &testSnapshot{byKey: map[eventauth.Key]map[string]any{}, byID: map[string]map[string]any{}, h: h}
	for _, row := range rows {
		event := storedEvent(t, h, row.EventID)
		snapshot.byKey[eventauth.Key{Type: row.Type, StateKey: row.StateKey}] = event
		snapshot.byID[row.EventID] = event
	}
	return snapshot
}

func storedEvent(t *testing.T, h *harness, eventID string) map[string]any {
	t.Helper()
	row, err := h.app.DB.Events.ByID(context.Background(), eventID)
	if err != nil {
		t.Fatalf("event %q is not stored: %v", eventID, err)
	}
	event, err := events.Parse(row.JSON)
	if err != nil {
		t.Fatalf("stored event %q does not parse: %v", eventID, err)
	}
	return event
}

type testSnapshot struct {
	byKey map[eventauth.Key]map[string]any
	byID  map[string]map[string]any
	h     *harness
}

func (s *testSnapshot) State(key eventauth.Key) (map[string]any, bool) {
	event, ok := s.byKey[key]
	return event, ok
}

func (s *testSnapshot) Event(id string) (map[string]any, bool) {
	if event, ok := s.byID[id]; ok {
		return event, true
	}
	row, err := s.h.app.DB.Events.ByID(context.Background(), id)
	if err != nil {
		return nil, false
	}
	event, err := events.Parse(row.JSON)
	if err != nil {
		return nil, false
	}
	s.byID[id] = event
	return event, true
}

func (s *testSnapshot) Rejected(string) bool { return false }

// TestCreateRoomShorthandOverridesInitialState pins the order the Client-Server API
// prescribes: preset defaults, then initial_state, then the shorthand name/topic/avatar
// parameters. A server that appends the shorthand first silently ignores the room name
// the client asked for.
func TestCreateRoomShorthandOverridesInitialState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "mon", "pw-mon", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{
		"preset": "public_chat",
		"topic":  "Test Room",
		"initial_state": []map[string]any{
			{"type": "m.room.topic", "state_key": "", "content": map[string]any{"topic": "Shenanigans"}},
		},
	})

	topic := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic/", aliceToken), http.StatusOK).decoded(t)
	if got, _ := topic["topic"].(string); got != "Test Room" {
		t.Errorf("topic = %q, want the shorthand value the client asked for", got)
	}
	// The specification duplicates the plain text into the structured field so a client
	// can render a topic without guessing whether it is formatted.
	rich, ok := topic["m.topic"].(map[string]any)
	if !ok {
		t.Fatalf("m.room.topic has no structured m.topic field: %v", topic)
	}
	entries, ok := rich["m.text"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("m.topic.m.text is not a one-entry array: %v", rich)
	}
	entry, _ := entries[0].(map[string]any)
	if got, _ := entry["body"].(string); got != "Test Room" {
		t.Errorf("m.topic.m.text[0].body = %q, want Test Room", got)
	}
	if mimetype, present := entry["mimetype"]; present && mimetype != "text/plain" {
		t.Errorf("mimetype = %v, want text/plain when set", mimetype)
	}

	// A client that sends its own m.room.topic event, with no shorthand, keeps exactly
	// the content it wrote: the server does not rewrite a client's event.
	other := createRoom(t, h, aliceToken, map[string]any{
		"preset":        "public_chat",
		"initial_state": []map[string]any{{"type": "m.room.topic", "content": map[string]any{"topic": "Only this"}}},
	})
	plain := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+other+"/state/m.room.topic/", aliceToken), http.StatusOK).decoded(t)
	if _, present := plain["m.topic"]; present {
		t.Errorf("the server added m.topic to a client's own state event: %v", plain)
	}
}

// TestCreateRoomInviteList checks that a room created with an invite list really invites
// those users, in the same transaction that created the room.
func TestCreateRoomInviteList(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "nina", "pw-nina", "")
	bob, bobToken, _ := h.register(t, "oleg", "pw-oleg", "")

	roomID := createRoom(t, h, aliceToken, map[string]any{"invite": []string{bob}})
	members := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/members", aliceToken), http.StatusOK).decoded(t)
	chunk, _ := members["chunk"].([]any)
	if len(chunk) != 2 {
		t.Fatalf("members = %v, want two entries", members["chunk"])
	}
	var invited string
	for _, entry := range chunk {
		event, _ := entry.(map[string]any)
		content, _ := event["content"].(map[string]any)
		if content["membership"] == "invite" {
			invited, _ = event["state_key"].(string)
		}
	}
	if invited != bob {
		t.Errorf("invited %q, want %s", invited, bob)
	}

	// The invitation reaches the invited user's sync, which is the only way a client
	// finds out it was invited at all.
	body := h.mustStatus(t, h.get("/_matrix/client/v3/sync?timeout=0", bobToken), http.StatusOK).decoded(t)
	if roomSection(t, body, "invite", roomID) == nil {
		t.Errorf("the invitation is not in bob's sync: %v", body["rooms"])
	}

	// A room created with a user who lives elsewhere is created. The room is this server's to
	// make; what it cannot do on its own is invite that user, because the invited user's own
	// homeserver has to countersign the invite. There is no server at elsewhere.example in this
	// harness, so the invitation does not land — and it is not written down either, because an
	// invite only this server signed is not something the room will ever believe. The client is
	// left a room it can invite through, rather than an error that would have destroyed a room
	// that now exists. Two homeservers that do answer each other are covered by
	// Complement's TestFederationRejectInvite, where two homeservers do answer.
	remote := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"invite": []string{"@someone:elsewhere.example"}}, aliceToken), http.StatusOK)
	remoteRoom, _ := remote.decoded(t)["room_id"].(string)
	if remoteRoom == "" {
		t.Fatalf("no room was created: %v", remote.decoded(t))
	}
	members = h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+remoteRoom+"/members", aliceToken), http.StatusOK).decoded(t)
	chunk, _ = members["chunk"].([]any)
	for _, entry := range chunk {
		event, _ := entry.(map[string]any)
		content, _ := event["content"].(map[string]any)
		if content["membership"] == "invite" {
			t.Errorf("a remote invite was stored although the other server never countersigned it: %v", event)
		}
	}
}

// TestJoinCopiesExtraKeys checks that a client's extra join keys land in the member event,
// which is how a client attaches its own information to the fact that it joined.
func TestJoinCopiesExtraKeys(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "pam", "pw-pam", "")
	bob, bobToken, _ := h.register(t, "rita2", "pw-rita2", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	res := h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{"foo": "bar", "reason": "why not"}, bobToken)
	h.mustStatus(t, res, http.StatusOK)

	event := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+bob+"?format=event", aliceToken), http.StatusOK).decoded(t)
	content, _ := event["content"].(map[string]any)
	if content["membership"] != "join" {
		t.Errorf("membership = %v, want join", content["membership"])
	}
	if content["foo"] != "bar" {
		t.Errorf("the extra key was not copied into the member event: %v", content)
	}
	if content["reason"] != "why not" {
		t.Errorf("reason = %v, want the reason the client gave", content["reason"])
	}
}
