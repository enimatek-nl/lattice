package integration

import (
	"context"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
	"github.com/enimatek/lattice/internal/state"
)

// Two homeservers can change the same state key without hearing about each other, and the
// room must end up with one state rather than one state per server. The specification names
// the algorithm that decides it; picking whichever event arrived last does not converge, so
// these tests hand the same fork to two homeservers in opposite orders and ask whether they
// agree afterwards.

func TestForkedRoomStateIsResolvedRatherThanAbsorbed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ca := newTestCA(t, dir)
	dial := &testDial{target: map[string]string{}}
	hs1 := newFederationNode(t, "hs1", ca, dial)
	hs2 := newFederationNode(t, "hs2", ca, dial)
	hs3 := newFederationNode(t, "hs3", ca, dial)
	ctx := context.Background()

	token := hs1.registerUser(t, "forker", "pw-forker")
	_, created := hs1.clientDo(t, "POST", "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, token)
	roomID, _ := created["room_id"].(string)
	snapshot := hs1.roomSnapshot(t, roomID, token)
	rules := roomversion.MustGet(snapshotRoomVersion(t, snapshot))
	base := byDepth(dedupe(snapshot.State))
	if len(base) == 0 {
		t.Fatalf("hs1 serves no state for %s", roomID)
	}
	sender := ""
	for _, event := range base {
		if stringAny(event, "type") == "m.room.create" {
			sender = stringAny(event, "sender")
		}
	}
	if sender == "" {
		t.Fatalf("no creator in %s's state", hs1.name)
	}

	// Both receivers start from the same point in the room's history.
	for _, to := range []*fedNode{hs2, hs3} {
		if _, answer := hs1.sendTransaction(t, to, "begin", base); anyRefusal(t, answer) {
			t.Fatalf("%s refused the room's beginning: %v", to.name, answer.PDUs)
		}
	}

	extremity := stringAny(base[len(base)-1], "event_id")
	depth := 0
	for _, event := range base {
		if at, ok := event["depth"].(float64); ok && int(at) > depth {
			depth = int(at)
		}
	}
	memory := eventauth.NewMemoryState(base...)

	// Two name events, both signed by hs1, both descending from the same event. Neither
	// knows about the other: that is the fork.
	branch := func(name string, ts int64) map[string]any {
		event := map[string]any{
			"type":             "m.room.name",
			"state_key":        "",
			"sender":           sender,
			"origin":           hs1.name,
			"origin_server_ts": ts,
			"depth":            depth + 1,
			"prev_events":      []any{extremity},
			"room_id":          roomID,
			"content":          map[string]any{"name": name},
		}
		event["auth_events"] = eventauth.SelectEventIDs(rules, event, memory)
		if err := events.Seal(hs1.name, rules, event, hs1.app.Keys); err != nil {
			t.Fatalf("sealing the %s branch: %v", name, err)
		}
		id, err := events.EventID(rules, event)
		if err != nil {
			t.Fatalf("the %s branch's event ID: %v", name, err)
		}
		event["event_id"] = id
		return event
	}
	now := time.Now().UnixMilli()
	branchA := branch("first branch", now)
	branchB := branch("second branch", now+50)

	// hs2 takes them one way round, hs3 the other.
	if _, answer := hs1.sendTransaction(t, hs2, "branch-a", []map[string]any{branchA}); anyRefusal(t, answer) {
		t.Fatalf("%s refused the first branch: %v", hs2.name, answer.PDUs)
	}
	if got := stateEventID(t, hs2, roomID, "m.room.name", ""); got != stringAny(branchA, "event_id") {
		t.Errorf("with one branch in hand %s calls the name %s, want the branch it has: %s",
			hs2.name, got, stringAny(branchA, "event_id"))
	}
	if _, answer := hs1.sendTransaction(t, hs3, "branch-b", []map[string]any{branchB}); anyRefusal(t, answer) {
		t.Fatalf("%s refused the second branch: %v", hs3.name, answer.PDUs)
	}
	for _, pair := range []struct {
		to     *fedNode
		event  map[string]any
		label  string
		txname string
	}{{hs2, branchB, "second branch", "branch-b"}, {hs3, branchA, "first branch", "branch-a"}} {
		if _, answer := hs1.sendTransaction(t, pair.to, pair.txname, []map[string]any{pair.event}); anyRefusal(t, answer) {
			t.Fatalf("%s refused the %s: %v", pair.to.name, pair.label, answer.PDUs)
		}
	}

	// Both servers now hold both branches, and both must call the room the same thing.
	nameA, nameB := stringAny(branchA, "event_id"), stringAny(branchB, "event_id")
	winner2 := stateEventID(t, hs2, roomID, "m.room.name", "")
	winner3 := stateEventID(t, hs3, roomID, "m.room.name", "")
	if winner2 != winner3 {
		t.Errorf("%s thinks the room is named by %s and %s by %s: the fork was absorbed, not resolved",
			hs2.name, winner2, hs3.name, winner3)
	}
	if winner2 != nameA && winner2 != nameB {
		t.Errorf("%s settled on %s, which is neither branch", hs2.name, winner2)
	}

	// The branch that lost is still held. Resolution decides what the room's state *is*;
	// it does not delete the history that contains the other answer.
	for _, node := range []*fedNode{hs2, hs3} {
		for _, eventID := range []string{nameA, nameB} {
			if _, err := node.app.DB.Events.ByID(ctx, eventID); err != nil {
				t.Errorf("%s lost the branch event %s while resolving: %v", node.name, eventID, err)
			}
		}
		if extremities, err := node.app.DB.Events.ForwardExtremities(ctx, roomID); err != nil || len(extremities) != 2 {
			t.Errorf("%s reports %d forward extremities (%v), want the two branches still open",
				node.name, len(extremities), err)
		}
	}

	// The answer is the one the algorithm gives, not one this server invented: resolve the
	// same two snapshots here, over the same events, and compare.
	resolved := resolveByName(t, rules, base, branchA, branchB)
	if resolved != winner2 {
		t.Errorf("%s's current name event is %s; the algorithm resolves the fork to %s",
			hs2.name, winner2, resolved)
	}
}

// resolveByName runs the room version's state resolution over the two branch states and
// returns the event ID that holds m.room.name afterwards.
func resolveByName(t *testing.T, rules roomversion.Rules, base []map[string]any, branchA, branchB map[string]any) string {
	t.Helper()
	graph := &mapGraph{events: map[string]map[string]any{}}
	branchState := func(branch map[string]any) state.State {
		out := state.State{}
		for _, event := range base {
			graph.events[stringAny(event, "event_id")] = event
			out[eventauth.Key{Type: stringAny(event, "type"), StateKey: stringAny(event, "state_key")}] = event
		}
		graph.events[stringAny(branch, "event_id")] = branch
		out[eventauth.Key{Type: stringAny(branch, "type"), StateKey: stringAny(branch, "state_key")}] = branch
		return out
	}
	states := []state.State{branchState(branchA), branchState(branchB)}
	resolved, err := state.Resolve(rules, graph, states, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolving the fork directly: %v", err)
	}
	event, ok := resolved[eventauth.Key{Type: "m.room.name", StateKey: ""}]
	if !ok {
		t.Fatalf("the resolved state has no name: %v", resolved)
	}
	return stringAny(event, "event_id")
}

// mapGraph is the event graph over a known set of events.
type mapGraph struct {
	events map[string]map[string]any
}

func (g *mapGraph) Event(id string) (map[string]any, bool) {
	event, ok := g.events[id]
	return event, ok
}

func (g *mapGraph) AuthEventIDs(id string) []string {
	event, ok := g.events[id]
	if !ok {
		return nil
	}
	var ids []string
	for _, value := range []any{} {
		_ = value
	}
	if listed, ok := event["auth_events"].([]any); ok {
		for _, value := range listed {
			if id, ok := value.(string); ok {
				ids = append(ids, id)
			}
		}
	} else if listed, ok := event["auth_events"].([]string); ok {
		ids = append(ids, listed...)
	}
	return ids
}

func (g *mapGraph) Rejected(string) bool { return false }

func stateEventID(t *testing.T, node *fedNode, roomID, eventType, stateKey string) string {
	t.Helper()
	row, err := node.app.DB.State.All(context.Background(), roomID)
	if err != nil {
		t.Fatalf("reading %s's state: %v", node.name, err)
	}
	for _, entry := range row {
		if entry.Type == eventType && entry.StateKey == stateKey {
			return entry.EventID
		}
	}
	return ""
}
