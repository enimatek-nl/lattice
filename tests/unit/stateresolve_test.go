package unit

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
	"github.com/enimatek/lattice/internal/state"
)

const stateRoom = "!sroom:example.org"

// memGraph is an event store that can answer what state resolution asks: the event
// itself, the auth events it cites, and whether it was rejected on receipt.
type memGraph struct {
	events   map[string]map[string]any
	auth     map[string][]string
	rejected map[string]bool
}

func newGraph() *memGraph {
	return &memGraph{events: map[string]map[string]any{}, auth: map[string][]string{}, rejected: map[string]bool{}}
}

func (g *memGraph) add(event map[string]any) {
	id := event["event_id"].(string)
	g.events[id] = event
	// Room versions 1 and 2 write each auth event as [event_id, {"sha256": hash}], so a graph that
	// only understands bare IDs cannot walk the auth chain of an event from those rooms.
	ids := events.ReferencedIDs(event, "auth_events")
	g.auth[id] = ids
}

func (g *memGraph) reject(id string) { g.rejected[id] = true }

func (g *memGraph) Event(id string) (map[string]any, bool) {
	event, ok := g.events[id]
	return event, ok
}

func (g *memGraph) AuthEventIDs(id string) []string { return g.auth[id] }

func (g *memGraph) Rejected(id string) bool { return g.rejected[id] }

// stEvent builds a state event for these tests.
func stEvent(eventType, id, sender, stateKey string, content map[string]any, authIDs []string, ts int64) map[string]any {
	anyIDs := make([]any, 0, len(authIDs))
	for _, id := range authIDs {
		anyIDs = append(anyIDs, id)
	}
	event := map[string]any{
		"type":             eventType,
		"event_id":         id,
		"room_id":          stateRoom,
		"sender":           sender,
		"origin_server_ts": ts,
		"auth_events":      anyIDs,
		"content":          content,
	}
	if stateKey != "" || eventType != "m.room.create" {
		event["state_key"] = stateKey
	}
	return event
}

func stateOf(events ...map[string]any) state.State {
	out := state.State{}
	for _, event := range events {
		key := eventauth.Key{Type: event["type"].(string)}
		if stateKey, ok := event["state_key"].(string); ok {
			key.StateKey = stateKey
		}
		out[key] = event
	}
	return out
}

// forkBase is the state both sides of a fork agree on: a room created by alice,
// alice joined, alice at power level 100, public join rule.
func forkBase() []map[string]any {
	create := stEvent("m.room.create", "$create:example.org", "@alice:example.org", "",
		map[string]any{"creator": "@alice:example.org", "room_version": "10"}, nil, 1)
	aliceJoin := stEvent("m.room.member", "$alicejoin:example.org", "@alice:example.org", "@alice:example.org",
		map[string]any{"membership": "join"}, []string{"$create:example.org"}, 2)
	joinRules := stEvent("m.room.join_rules", "$joinrules:example.org", "@alice:example.org", "",
		map[string]any{"join_rule": "public"}, []string{"$create:example.org", "$alicejoin:example.org"}, 3)
	pl := stEvent("m.room.power_levels", "$pl0:example.org", "@alice:example.org", "",
		map[string]any{"users": map[string]any{"@alice:example.org": 100}, "ban": 50, "kick": 50, "state_default": 50},
		[]string{"$create:example.org", "$alicejoin:example.org"}, 4)
	return []map[string]any{create, aliceJoin, joinRules, pl}
}

func graphWith(events ...map[string]any) *memGraph {
	graph := newGraph()
	for _, event := range events {
		graph.add(event)
	}
	return graph
}

// TestResolveBanBeatsJoin is the security-relevant case: two forks disagree about a
// user's membership, and the ban applied by someone with power must win.
func TestResolveBanBeatsJoin(t *testing.T) {
	t.Parallel()
	base := forkBase()

	bobJoin := stEvent("m.room.member", "$bobjoin:example.org", "@bob:example.org", "@bob:example.org",
		map[string]any{"membership": "join"},
		[]string{"$create:example.org", "$pl0:example.org", "$joinrules:example.org"}, 10)
	bobBan := stEvent("m.room.member", "$bobban:example.org", "@alice:example.org", "@bob:example.org",
		map[string]any{"membership": "ban"},
		[]string{"$create:example.org", "$pl0:example.org", "$alicejoin:example.org"}, 11)

	rules := roomversion.MustGet("11")

	left := append(append([]map[string]any{}, base...), bobJoin)
	right := append(append([]map[string]any{}, base...), bobBan)

	for _, swap := range []bool{false, true} {
		graph := graphWith(append(append([]map[string]any{}, left...), right...)...)
		states := []state.State{stateOf(left...), stateOf(right...)}
		if swap {
			states = []state.State{stateOf(right...), stateOf(left...)}
		}
		resolved, err := state.Resolve(rules, graph, states, eventauth.Opts{})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		current := resolved[eventauth.Key{Type: "m.room.member", StateKey: "@bob:example.org"}]
		if current == nil || current["event_id"] != "$bobban:example.org" {
			t.Errorf("swap=%v: resolved membership = %v, want the ban", swap, idOf(current))
		}
	}
}

func idOf(event map[string]any) any {
	if event == nil {
		return nil
	}
	return event["event_id"]
}

// TestResolveLaterPowerLevelsWinsByOrdering shows the ordering deciding which of two
// conflicting power levels events survives, which is what makes servers converge.
func TestResolveLaterPowerLevelsWinsByOrdering(t *testing.T) {
	t.Parallel()
	base := forkBase()

	plA := stEvent("m.room.power_levels", "$pla:example.org", "@alice:example.org", "",
		map[string]any{"users": map[string]any{"@alice:example.org": 100}, "state_default": 10},
		[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 20)
	plB := stEvent("m.room.power_levels", "$plb:example.org", "@alice:example.org", "",
		map[string]any{"users": map[string]any{"@alice:example.org": 100}, "state_default": 30},
		[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 21)

	rules := roomversion.MustGet("11")
	graph := graphWith(append(append([]map[string]any{}, base...), plA, plB)...)
	resolved, err := state.Resolve(rules, graph, []state.State{
		stateOf(append(append([]map[string]any{}, base...), plA)...),
		stateOf(append(append([]map[string]any{}, base...), plB)...),
	}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	current := resolved[eventauth.Key{Type: "m.room.power_levels"}]
	if current == nil || current["event_id"] != "$plb:example.org" {
		t.Fatalf("resolved power levels = %v, want the later one", idOf(current))
	}

	// Reverse the timestamps and the other event wins: the ordering is what decides,
	// not the order the forks were handed in.
	plA["origin_server_ts"] = int64(30)
	graph2 := graphWith(append(append([]map[string]any{}, base...), plA, plB)...)
	resolved2, err := state.Resolve(rules, graph2, []state.State{
		stateOf(append(append([]map[string]any{}, base...), plA)...),
		stateOf(append(append([]map[string]any{}, base...), plB)...),
	}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolve with swapped timestamps: %v", err)
	}
	if current := resolved2[eventauth.Key{Type: "m.room.power_levels"}]; current == nil || current["event_id"] != "$pla:example.org" {
		t.Errorf("after swapping timestamps, resolved power levels = %v, want $pla", idOf(current))
	}
}

// TestResolveUnconflictedStateWins checks step 5: an event every fork agrees on is
// not displaced by a conflicting event that only appears in an auth chain.
func TestResolveUnconflictedStateWins(t *testing.T) {
	t.Parallel()
	base := forkBase()

	// A second create event, reachable only through an auth chain: it belongs to the
	// conflicted set, but the room's real create event is in every state.
	secondCreate := stEvent("m.room.create", "$create2:example.org", "@alice:example.org", "",
		map[string]any{"creator": "@alice:example.org", "room_version": "10"}, nil, 0)
	topicA := stEvent("m.room.topic", "$topicA:example.org", "@alice:example.org", "",
		map[string]any{"topic": "left"},
		[]string{"$create2:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 30)
	topicB := stEvent("m.room.topic", "$topicB:example.org", "@alice:example.org", "",
		map[string]any{"topic": "right"},
		[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 31)

	graph := graphWith(append(append([]map[string]any{}, base...), secondCreate, topicA, topicB)...)
	rules := roomversion.MustGet("11")
	resolved, err := state.Resolve(rules, graph, []state.State{
		stateOf(append(append([]map[string]any{}, base...), topicA)...),
		stateOf(append(append([]map[string]any{}, base...), topicB)...),
	}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resolved[eventauth.Key{Type: "m.room.create"}]; got == nil || got["event_id"] != "$create:example.org" {
		t.Errorf("the create event was replaced by one that is only in an auth chain: %v", idOf(got))
	}
}

// TestResolveSkipsRejectedEvents records that an event which fails auth against the
// state being built is dropped, and that a rejected auth event is never used to
// rescue it.
func TestResolveSkipsRejectedEvents(t *testing.T) {
	t.Parallel()
	base := forkBase()

	// bob tries to raise his own level on one fork. Rule 9 refuses it: he has no power.
	bobPL := stEvent("m.room.power_levels", "$bobpl:example.org", "@bob:example.org", "",
		map[string]any{"users": map[string]any{"@alice:example.org": 100, "@bob:example.org": 100}},
		[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 40)
	// bob's own member event exists only on this fork, and is rejected on receipt.
	bobJoin := stEvent("m.room.member", "$bobjoin:example.org", "@bob:example.org", "@bob:example.org",
		map[string]any{"membership": "join"},
		[]string{"$create:example.org", "$pl0:example.org", "$joinrules:example.org"}, 39)

	graph := graphWith(append(append([]map[string]any{}, base...), bobJoin, bobPL)...)
	graph.reject("$bobjoin:example.org")

	rules := roomversion.MustGet("11")
	resolved, err := state.Resolve(rules, graph, []state.State{
		stateOf(append(append([]map[string]any{}, base...), bobPL)...),
		stateOf(base...),
	}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resolved[eventauth.Key{Type: "m.room.power_levels"}]; got == nil || got["event_id"] != "$pl0:example.org" {
		t.Errorf("a power levels event that grants its sender power from nowhere was accepted: %v", idOf(got))
	}
	if got := resolved[eventauth.Key{Type: "m.room.member", StateKey: "@bob:example.org"}]; got != nil {
		t.Errorf("a rejected event entered the resolved state: %v", idOf(got))
	}
}

// TestResolveSingleStateIsIdentity checks the fast path does not quietly rewrite state.
func TestResolveSingleStateIsIdentity(t *testing.T) {
	t.Parallel()
	base := forkBase()
	graph := graphWith(base...)
	resolved, err := state.Resolve(roomversion.MustGet("11"), graph, []state.State{stateOf(base...)}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved) != len(base) {
		t.Errorf("resolved %d entries from %d", len(resolved), len(base))
	}
}

// TestResolveRefusesOtherAlgorithms checks that a room version whose state resolution this server has
// not written is refused, not resolved with the wrong one. Room version 1 has its own algorithm and is
// covered in stateresolutionv1_test.go; versions 2 through 12 are all implemented, so the list of
// versions that must be refused is now empty and the interesting half of this test runs the other way:
// version 12 must resolve. The synthetic version at the end is what keeps the refusal honest — with
// every real version implemented, an error path that nothing asks for is an error path that rots, and
// applying version 2's algorithm to a version that names something else is exactly the mistake it
// exists to catch.
func TestResolveRefusesOtherAlgorithms(t *testing.T) {
	t.Parallel()
	base := forkBase()
	graph := graphWith(base...)
	states := []state.State{stateOf(base...), stateOf(base...)}

	if _, err := state.Resolve(roomversion.MustGet("12"), graph, states, eventauth.Opts{}); err != nil {
		t.Errorf("room version 12 resolution = %v, want it to resolve: state resolution 2.1 is implemented, and refusing here sends every version 12 room into the error path rather than the algorithm", err)
	}

	v3 := roomversion.MustGet("11")
	v3.StateRes = roomversion.StateRes("v3")
	if _, err := state.Resolve(v3, graph, states, eventauth.Opts{}); !errors.Is(err, state.ErrAlgorithmUnsupported) {
		t.Errorf("a room version naming an unimplemented algorithm resolved with %v, want ErrAlgorithmUnsupported", err)
	}
}

// TestResolveNameForkIgnoresTheOrderTheStatesAreGiven is confluence, which is the whole
// point of state resolution: two homeservers that receive the same fork in different
// orders must end up with the same room state. If the answer depended on which side is
// listed first, the room would split along the order its servers happened to hear about
// it in.
func TestResolveNameForkIgnoresTheOrderTheStatesAreGiven(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("11")
	base := forkBase()
	auth := []string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}
	older := stEvent("m.room.name", "$name_older:example.org", "@alice:example.org", "",
		map[string]any{"name": "older"}, auth, 1_000)
	newer := stEvent("m.room.name", "$name_newer:example.org", "@alice:example.org", "",
		map[string]any{"name": "newer"}, auth, 2_000)

	events := append(append([]map[string]any{}, base...), older, newer)
	graph := graphWith(events...)
	olderState := stateOf(append(append([]map[string]any{}, base...), older)...)
	newerState := stateOf(append(append([]map[string]any{}, base...), newer)...)

	nameOf := func(resolved state.State) string {
		event, ok := resolved[eventauth.Key{Type: "m.room.name", StateKey: ""}]
		if !ok {
			return "<no name>"
		}
		id, _ := event["event_id"].(string)
		return id
	}

	for _, attempt := range []struct {
		label  string
		states []state.State
	}{
		{"older first", []state.State{olderState, newerState}},
		{"newer first", []state.State{newerState, olderState}},
	} {
		resolved, err := state.Resolve(rules, graph, attempt.states, eventauth.Opts{})
		if err != nil {
			t.Fatalf("resolving with %s: %v", attempt.label, err)
		}
		if got := nameOf(resolved); got != "$name_newer:example.org" {
			t.Errorf("with %s the room is named by %s, want the event the mainline ordering puts last: %s",
				attempt.label, got, "$name_newer:example.org")
		}
	}
}

// TestResolveOrdersJSONDecodedEventsByTheirTimestamp is a regression test for a silent
// failure. The events a server resolves are decoded from JSON, where a number arrives as a
// json.Number; reading it as a plain int64 or float64 yields zero for every event. A
// resolution that cannot see timestamps drops the timestamp comparison from the mainline
// ordering and falls through to event IDs — deterministic, so this server would agree with
// itself, but a server that orders by timestamp (as the specification says) would reach a
// different answer for the same fork, and the room splits.
func TestResolveOrdersJSONDecodedEventsByTheirTimestamp(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("11")
	base := forkBase()
	auth := []string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}

	fromJSON := func(eventID, name string, ts int64) map[string]any {
		body := map[string]any{
			"type":             "m.room.name",
			"event_id":         eventID,
			"room_id":          stateRoom,
			"state_key":        "",
			"sender":           "@alice:example.org",
			"origin":           "example.org",
			"origin_server_ts": ts,
			"depth":            5,
			"prev_events":      []string{"$alicejoin:example.org"},
			"auth_events":      auth,
			"content":          map[string]any{"name": name},
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding %s: %v", eventID, err)
		}
		parsed, err := events.Parse(encoded)
		if err != nil {
			t.Fatalf("parsing %s: %v", eventID, err)
		}
		if _, ok := parsed["origin_server_ts"].(json.Number); !ok {
			t.Fatalf("a decoded event carries its timestamp as %T, not json.Number — this test has stopped testing the thing it names",
				parsed["origin_server_ts"])
		}
		return parsed
	}
	older := fromJSON("$name_older:example.org", "older", 1_000)
	newer := fromJSON("$name_newer:example.org", "newer", 2_000)

	events := append(append([]map[string]any{}, base...), older, newer)
	graph := graphWith(events...)
	olderState := stateOf(append(append([]map[string]any{}, base...), older)...)
	newerState := stateOf(append(append([]map[string]any{}, base...), newer)...)

	for _, attempt := range []struct {
		label  string
		states []state.State
	}{
		{"older first", []state.State{olderState, newerState}},
		{"newer first", []state.State{newerState, olderState}},
	} {
		resolved, err := state.Resolve(rules, graph, attempt.states, eventauth.Opts{})
		if err != nil {
			t.Fatalf("resolving with %s: %v", attempt.label, err)
		}
		event, ok := resolved[eventauth.Key{Type: "m.room.name", StateKey: ""}]
		if !ok {
			t.Fatalf("with %s the resolved state has no name", attempt.label)
		}
		if got := event["event_id"]; got != "$name_newer:example.org" {
			t.Errorf("with %s the room is named by %v; the mainline ordering compares origin_server_ts, and the newer event must win",
				attempt.label, got)
		}
	}
}
