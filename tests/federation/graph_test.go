package federation

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The room is a graph before it is a list, and the fields that make it one are the
// fields a client sees: which events an event builds on, which events authorise it,
// and the hash and signature that let anybody check the result against the server that
// claims to have made it. These tests read those fields back through the client API,
// because that is where they stop being internal bookkeeping: an event that does not
// verify here does not verify on the next homeserver either.

// decoded decodes a JSON body keeping numbers exactly as written. A signature is made
// over specific digits, so a body that goes through float64 on the way to a re-encode is
// a different object by the time it is hashed.
func decoded(t *testing.T, raw []byte) any {
	t.Helper()
	value, err := matrix.DecodeCanonical(raw)
	if err != nil {
		t.Fatalf("decoding: %v (%s)", err, raw)
	}
	return value
}

// servedEvent decodes an event exactly as it was served. The canonical decoder is used
// rather than the test's usual map decoding because a signature is made over specific
// digits: a number that passes through float64 on the way to a re-encode is a different
// event by the time it is hashed.
func servedEvent(t *testing.T, s *server, sess *session, roomID, eventID string) matrix.Object {
	t.Helper()
	got := s.mustDo(http.StatusOK, http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/event/%s", roomID, eventID), sess.Token, nil)
	event, ok := decoded(t, got.Raw).(matrix.Object)
	if !ok {
		t.Fatalf("the served event is not an object: %s", got.Raw)
	}
	if event["event_id"] != eventID {
		t.Fatalf("the room answered with %v for the event %s", event["event_id"], eventID)
	}
	return event
}

// servedState reads a state event out of the room's full state. The single-state
// endpoint answers with the content of an event and nothing else, which is what the
// client API promises but not what an event's hash and signature are made over; the
// full state listing is where a state event can be found whole.
func servedState(t *testing.T, s *server, sess *session, roomID, typ, stateKey string) matrix.Object {
	t.Helper()
	entries := stateEvents(t, s, sess, roomID)
	for _, entry := range entries {
		state, ok := entry.(matrix.Object)
		if !ok {
			continue
		}
		if state["type"] == typ && state["state_key"] == stateKey {
			return state
		}
	}
	t.Fatalf("the state of %s has no %s/%s", roomID, typ, stateKey)
	return nil
}

// stateEvents lists the room's state as full events, which is where a client that did
// not create the room finds the events it is asked to trust.
func stateEvents(t *testing.T, s *server, sess *session, roomID string) []any {
	t.Helper()
	got := s.mustDo(http.StatusOK, http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/state", roomID), sess.Token, nil)
	body, ok := decoded(t, got.Raw).([]any)
	if !ok {
		t.Fatalf("the state of %s is not an array of events: %s", roomID, got.Raw)
	}
	return body
}

// idsOf reads an array of event ids out of a served event.
func idsOf(t *testing.T, event matrix.Object, field string) []string {
	t.Helper()
	list, ok := event[field].([]any)
	if !ok {
		t.Fatalf("%s is missing from the event: %v", field, event[field])
	}
	ids := make([]string, 0, len(list))
	for _, entry := range list {
		id, ok := entry.(string)
		if !ok {
			t.Fatalf("%s holds %v, which is not an event id", field, entry)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestEventsKnowWhereTheyAttach follows a message trail through a room and checks the
// graph from the leaf back to the root: an id the room version derives rather than one
// the server made up, prev_events that name the event before it, auth_events that carry
// what authorises the event, a depth that counts the chain, and a hash and signature
// that hold up against the bytes this server hands out.
func TestEventsKnowWhereTheyAttach(t *testing.T) {
	c := newCluster(t)
	one := c.one()
	alice := one.register("alice", "wonderland")
	roomID := one.createRoom(alice, map[string]any{"name": "Garden"})
	first := one.send(alice, roomID, "down the rabbit hole")
	second := one.send(alice, roomID, "and the pool")

	create := servedState(t, one, alice, roomID, matrix.TypeCreate, "")
	member := servedState(t, one, alice, roomID, matrix.TypeMember, alice.UserID)
	power := servedState(t, one, alice, roomID, matrix.TypePowerLevels, "")
	createID, _ := create["event_id"].(string)
	memberID, _ := member["event_id"].(string)
	powerID, _ := power["event_id"].(string)
	if createID == "" || memberID == "" || powerID == "" {
		t.Fatalf("the room's own state events have no ids: %v %v %v", create, member, power)
	}

	events := map[string]matrix.Object{
		"create": create,
		"first":  servedEvent(t, one, alice, roomID, first),
		"second": servedEvent(t, one, alice, roomID, second),
	}

	// An id this server cannot be asked to have invented: room version 11 derives it
	// from the event, and the derivation is reproducible from what the client was
	// given.
	for name, id := range map[string]string{"create": createID, "first": first, "second": second} {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "$"))
		if err != nil || len(raw) != 32 {
			t.Errorf("%s has the id %s, which is not a reference hash", name, id)
		}
		recomputed, err := eventcrypto.EventID(events[name], "11")
		if err != nil {
			t.Fatalf("recomputing the id of %s: %v", name, err)
		}
		if recomputed != id {
			t.Errorf("%s is served as %s, but the event hashes to %s", name, id, recomputed)
		}
	}

	// The create event is the root: it builds on nothing.
	if prev := idsOf(t, create, "prev_events"); len(prev) != 0 {
		t.Errorf("the create event builds on %v", prev)
	}
	// The first message builds on one event - the leaf the room was left with after
	// its creation - and the second on the first message. The graph is a chain rather
	// than a fan because each event retires the leaf it cited; a room whose leaves were
	// never retired would fork on every send.
	stateIDs := map[string]bool{}
	for _, entry := range stateEvents(t, one, alice, roomID) {
		if state, ok := entry.(matrix.Object); ok {
			if id, ok := state["event_id"].(string); ok {
				stateIDs[id] = true
			}
		}
	}
	prev := idsOf(t, events["first"], "prev_events")
	if len(prev) != 1 {
		t.Errorf("the first message builds on %v, want exactly one event", prev)
	} else if !stateIDs[prev[0]] {
		t.Errorf("the first message builds on %s, which is not one of the room's state events", prev[0])
	}
	if prev := idsOf(t, events["second"], "prev_events"); len(prev) != 1 || prev[0] != first {
		t.Errorf("the second message builds on %v, want just %s", prev, first)
	}

	// The auth events are the ones the authorisation rules read: the room's creation,
	// the power levels, and the sender's own membership.
	for name, event := range map[string]matrix.Object{"first": events["first"], "second": events["second"]} {
		auth := idsOf(t, event, "auth_events")
		for _, want := range []string{createID, powerID, memberID} {
			if !strings.Contains(strings.Join(auth, " "), want) {
				t.Errorf("the auth events of %s are %v, which leaves out %s", name, auth, want)
			}
		}
	}

	// Depth counts the chain rather than the clock.
	depths := map[string]int64{}
	for name, event := range events {
		depth, ok := event["depth"].(json.Number)
		if !ok {
			t.Fatalf("%s carries a depth of %v", name, event["depth"])
		}
		value, err := depth.Int64()
		if err != nil {
			t.Fatalf("reading the depth of %s: %v", name, err)
		}
		depths[name] = value
	}
	if depths["create"] != 1 {
		t.Errorf("the create event has depth %d; the root of a room has no events behind it", depths["create"])
	}
	if depths["first"] <= depths["create"] {
		t.Errorf("the first message has depth %d, which does not build on the room's state at depth %d",
			depths["first"], depths["create"])
	}
	if depths["second"] != depths["first"]+1 {
		t.Errorf("the second message has depth %d while the first has %d: depth counts the chain, not the clock",
			depths["second"], depths["first"])
	}
}

// TestServedEventsVerifyAgainstThePublishedKey checks an event the way the next
// homeserver would: fetch this server's signing key from the endpoint that publishes it,
// then check the hash and the signature of an event this server made.
//
// This is the test that fails if the server re-serialises events on the way out. A
// signature is made over one particular spelling of an event; code that rebuilds the
// event from columns, reorders keys, or lets a number pass through float64 changes what
// the signature covers while leaving the signature itself looking plausible.
func TestServedEventsVerifyAgainstThePublishedKey(t *testing.T) {
	c := newCluster(t)
	one := c.one()
	alice := one.register("alice", "wonderland")
	roomID := one.createRoom(alice, map[string]any{"name": "Garden"})
	sent := one.send(alice, roomID, "down the rabbit hole")

	published := one.mustDo(http.StatusOK, http.MethodGet, "/_matrix/key/v2/server", "", nil).objectField("verify_keys")
	if len(published) == 0 {
		t.Fatalf("the server publishes no signing keys")
	}
	event := servedEvent(t, one, alice, roomID, sent)
	if err := eventcrypto.VerifyContentHash(event, "11"); err != nil {
		t.Errorf("the event does not match its own content hash: %v", err)
	}
	signatures, ok := event["signatures"].(matrix.Object)
	if !ok {
		t.Fatalf("the event carries no signatures: %v", event["signatures"])
	}
	for serverName, byKeyAny := range signatures {
		byKey, ok := byKeyAny.(matrix.Object)
		if !ok {
			t.Fatalf("the signatures of %s are not an object: %v", serverName, byKeyAny)
		}
		for keyID := range byKey {
			entry, ok := published[keyID].(matrix.Object)
			if !ok {
				t.Errorf("the event is signed under %s, which this server does not publish", keyID)
				continue
			}
			encoded, _ := entry["key"].(string)
			publicKey, err := matrix.DecodeEd25519Key(encoded)
			if err != nil {
				t.Errorf("the published key %s cannot be read: %v", keyID, err)
				continue
			}
			if err := eventcrypto.EventSignature(event, "11", serverName, keyID, publicKey); err != nil {
				t.Errorf("the event does not carry a valid signature from %s: %v", serverName, err)
			}
		}
	}
	if _, found := signatures[one.name]; !found {
		t.Errorf("no event signature is made by %s, which is the server that made the event", one.name)
	}
}

// TestAStateEventEntersTheGraph checks that a state change becomes part of the graph
// like any other event: the next message builds on it, and the state event's own id is
// derived like every other one.
func TestAStateEventEntersTheGraph(t *testing.T) {
	c := newCluster(t)
	one := c.one()
	alice := one.register("alice", "wonderland")
	roomID := one.createRoom(alice, map[string]any{"name": "Garden"})
	renamed := one.mustDo(http.StatusOK, http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/state/m.room.name/", roomID), alice.Token,
		matrix.Object{"name": "The Queen's Garden"}).stringField("event_id")
	if renamed == "" {
		t.Fatal("renaming the room returned no event id")
	}
	after := one.send(alice, roomID, "whose garden is it")

	event := servedEvent(t, one, alice, roomID, after)
	prev := idsOf(t, event, "prev_events")
	if len(prev) != 1 || prev[0] != renamed {
		t.Errorf("the message builds on %v, want the rename %s", prev, renamed)
	}
	// The old name event is still in the room's history, but it is no longer a leaf:
	// the graph has one leaf again, not two, so a later event cannot fork off it.
	if strings.Contains(strings.Join(prev, " "), renamed) == false {
		t.Errorf("the graph still points at %v", prev)
	}
}

// TestTheWireFormOfAnEventCarriesTheGraph checks the fields through /messages, which is
// how history reaches a client that did not watch the room live. The graph fields have
// to survive the trip, because a client that wants to check an event needs the event as
// it was signed, not a summary of it.
func TestTheWireFormOfAnEventCarriesTheGraph(t *testing.T) {
	c := newCluster(t)
	one := c.one()
	alice := one.register("alice", "wonderland")
	roomID := one.createRoom(alice, map[string]any{"name": "Garden"})
	one.send(alice, roomID, "down the rabbit hole")

	got := one.mustDo(http.StatusOK, http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/messages?dir=b&limit=10", roomID), alice.Token, nil)
	body, ok := decoded(t, got.Raw).(matrix.Object)
	if !ok {
		t.Fatalf("the /messages body is not an object: %s", got.Raw)
	}
	chunk, ok := body["chunk"].([]any)
	if !ok || len(chunk) == 0 {
		t.Fatalf("/messages returned no events: %s", got.Raw)
	}
	for _, entry := range chunk {
		event, ok := entry.(matrix.Object)
		if !ok {
			t.Fatalf("an event in the chunk is not an object: %v", entry)
		}
		for _, field := range []string{"prev_events", "auth_events", "depth", "hashes", "signatures", "origin_server_ts", "sender", "room_id"} {
			if _, found := event[field]; !found {
				t.Errorf("the event served for %v has no %s", event["event_id"], field)
			}
		}
		if err := eventcrypto.VerifyContentHash(event, "11"); err != nil {
			t.Errorf("the served event does not match its content hash: %v", err)
		}
	}
}
