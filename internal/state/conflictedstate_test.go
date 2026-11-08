package state

import (
	"testing"
)

// Tests for the conflicted state subgraph, the definition room version 12 (MSC4297) adds to state
// resolution and folds into the full conflicted set.
//
// They live here rather than in tests/unit because the set is the algorithm's own intermediate and
// its definition is about the shape of the auth graph, which is easier to state against the function
// than to infer through a resolved state map — where its effect arrives mixed together with the
// orderings. The behavioural half of the modification is covered in
// tests/unit/stateresolvev21_test.go.
//
// The graph these tests walk is a small one, drawn here because two of the wrong answers are only
// visible when it is:
//
//	A → X → B        A cites X and Y; X and Y each cite B; Y also cites Z, which cites nothing.
//	A → Y → B
//	      ↘ Z
//
// With A and B conflicted, the events "between a pair of conflicted events" are X and Y. Z is
// reachable from a conflicted event and is precisely not between two of them.

func testEvent(id string, auth ...string) map[string]any {
	return map[string]any{"event_id": id, "type": "m.room.test", "state_key": id}
}

type testGraph struct {
	auth map[string][]string
}

func (g *testGraph) Event(id string) (map[string]any, bool) {
	if _, ok := g.auth[id]; !ok {
		return nil, false
	}
	return testEvent(id), true
}

func (g *testGraph) AuthEventIDs(id string) []string { return g.auth[id] }

func (g *testGraph) Rejected(string) bool { return false }

func diamond() *testGraph {
	return &testGraph{auth: map[string][]string{
		"$A": {"$X", "$Y"},
		"$X": {"$B"},
		"$Y": {"$B", "$Z"},
		"$B": nil,
		"$Z": nil,
	}}
}

// TestConflictedStateSubgraphIsPathsNotChains is the diamond above read three ways, each of which is
// a mistake the definition invites.
func TestConflictedStateSubgraphIsPathsNotChains(t *testing.T) {
	graph := diamond()

	got := conflictedStateSubgraph(graph, eventSet{"$A": testEvent("$A"), "$B": testEvent("$B")})

	// The endpoints are part of the set — the definition says "including endpoints", and step 1 of
	// the algorithm depends on it by expecting to find the contested power events in the full set.
	for _, id := range []string{"$A", "$B", "$X", "$Y"} {
		if _, ok := got[id]; !ok {
			t.Errorf("the subgraph is missing %s; got %v", id, ids(got))
		}
	}
	// Z is reachable from a conflicted event and reaches none. An implementation that took each
	// conflicted event's auth chain would sweep it in, and the full conflicted set would then carry
	// an event no branch contested — which the iterative checks would be free to apply.
	if _, ok := got["$Z"]; ok {
		t.Errorf("the subgraph reached past the endpoints to $Z: %v", ids(got))
	}
	// Both X and Y, not just whichever the walk reached first. The definition quantifies over every
	// path between every pair, so an implementation that marks one path is an implementation whose
	// answer depends on map iteration order — the definition of a state resolution that two servers
	// get different answers from.
	if len(got) != 4 {
		t.Errorf("the subgraph is %v, want exactly A, B, X, Y", ids(got))
	}
}

// TestConflictedStateSubgraphOfASingleEventIsItself holds the line that keeps the previous test's
// discipline honest: with nothing else contested, an event's auth chain contributes nothing.
func TestConflictedStateSubgraphOfASingleEventIsItself(t *testing.T) {
	graph := diamond()

	got := conflictedStateSubgraph(graph, eventSet{"$A": testEvent("$A")})

	if len(got) != 1 {
		t.Fatalf("the subgraph of one contested event is %v, want only that event", ids(got))
	}
	if _, ok := got["$A"]; !ok {
		t.Errorf("the contested event is missing from its own subgraph: %v", ids(got))
	}
}

// TestConflictedStateSubgraphIgnoresUnreadableEvents keeps the set to events that exist. An auth
// chain can name an event this server cannot read; the resolution cannot put bytes it does not have
// into a set, and pretending otherwise would make the resolved state depend on a reference rather
// than on an event.
func TestConflictedStateSubgraphIgnoresUnreadableEvents(t *testing.T) {
	graph := diamond()
	delete(graph.auth, "$X")

	got := conflictedStateSubgraph(graph, eventSet{"$A": testEvent("$A"), "$B": testEvent("$B")})

	if _, ok := got["$X"]; ok {
		t.Errorf("an event that cannot be read is in the subgraph: %v", ids(got))
	}
	for _, id := range []string{"$A", "$B", "$Y"} {
		if _, ok := got[id]; !ok {
			t.Errorf("removing one unreadable event lost %s as well: %v", id, ids(got))
		}
	}
}

func ids(set eventSet) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}
