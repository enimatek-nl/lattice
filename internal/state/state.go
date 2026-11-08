// Package state resolves room state across forks in the event graph.
//
// Matrix rooms are a DAG, so two servers can hold different state for the same
// point in history. State Resolution 2 is what makes them converge, and it is the
// algorithm that decides who holds power after a fork — a wrong implementation does
// not fail loudly, it silently gives different servers different state, which is a
// room split. The algorithm here follows the "State resolution" section of the
// pinned room version pages (v1.19) step by step.
//
// Room versions 2 through 12 use the algorithm implemented in this file; room version
// 1 uses the older and much smaller algorithm implemented in stateresolutionv1.go.
// Version 12's revision (MSC4297) differs from version 2's in three sentences, which are
// marked in the algorithm below and are the reason a version-aware `Resolve` exists: it is
// not a rewrite of state resolution, it is state resolution with a wider conflicted set and
// a colder start.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/roomversion"
)

// ErrAlgorithmUnsupported is returned for a room version whose state resolution this
// server does not implement.
var ErrAlgorithmUnsupported = errors.New("state: state resolution for this room version is not implemented")

// State maps a state key to the event that holds it.
type State map[eventauth.Key]map[string]any

// Graph gives access to events and their auth events. State resolution walks auth
// chains, so this is deliberately narrower than a database: it can be backed by an
// in-memory set of events in tests and by stored events in the server.
type Graph interface {
	Event(id string) (map[string]any, bool)
	AuthEventIDs(id string) []string
	// Rejected reports whether an event failed the checks performed on receipt.
	// Rejected events must not be used as auth events in the iterative checks.
	Rejected(id string) bool
}

// Snapshot adapts a Graph and a State to the authorisation rules.
type Snapshot struct {
	graph Graph
	state State
}

// NewSnapshot pairs room state with the event graph so rule 2 can resolve auth events.
func NewSnapshot(graph Graph, state State) *Snapshot {
	return &Snapshot{graph: graph, state: state}
}

// State implements [eventauth.Snapshot].
func (s *Snapshot) State(key eventauth.Key) (map[string]any, bool) {
	ev, ok := s.state[key]
	return ev, ok
}

// Event implements [eventauth.Snapshot].
func (s *Snapshot) Event(id string) (map[string]any, bool) { return s.graph.Event(id) }

// Rejected implements [eventauth.Snapshot].
func (s *Snapshot) Rejected(id string) bool { return s.graph.Rejected(id) }

// Resolve resolves a set of room states — one per forward extremity of a fork — into
// a single state, using the algorithm the room version names.
//
// authChecks carries the verifiers the iterative auth checks need. A nil verifier
// makes the matching rule reject, which can change a resolved state; callers doing
// federation work must supply them.
func Resolve(rules roomversion.Rules, graph Graph, states []State, authChecks eventauth.Opts) (State, error) {
	// State Resolution 2.1 is State Resolution 2 plus three defined modifications, so it runs
	// through this function with one flag rather than as a second implementation. That is a
	// deliberate risk trade: the two algorithms share the parts where a mistake would be silent and
	// catastrophic (the orderings, the auth difference, the iterative checks), and duplicating them
	// to keep version 2 untouched would mean two copies to keep in step, which is how two servers
	// come to disagree about who runs a room.
	twoPointOne := rules.StateRes == roomversion.StateResV2_1
	switch rules.StateRes {
	case roomversion.StateResV2, roomversion.StateResV2_1:
	case roomversion.StateResV1:
		return resolveV1(rules, graph, states, authChecks)
	default:
		return nil, fmt.Errorf("%w: room version %s uses %s", ErrAlgorithmUnsupported, rules.ID, rules.StateRes)
	}
	if len(states) == 0 {
		return State{}, nil
	}
	if len(states) == 1 {
		out := make(State, len(states[0]))
		for k, v := range states[0] {
			out[k] = v
		}
		return out, nil
	}

	unconflicted, conflicted := splitStates(states)

	// The auth difference: events that appear in some but not all auth chains. They
	// are part of the conflicted set because a fork can disagree about the history
	// that authorised the state, not only about the state itself.
	difference := authDifference(graph, states)
	full := conflicted.union(difference)
	if twoPointOne {
		// Modification 3 of the version 12 page: the full conflicted set additionally includes the
		// conflicted state subgraph — the events sitting on an auth_events path between two events the
		// fork disagrees about. Version 2 left those out, and version 12 puts them in because its
		// iterative checks start from nothing (modification 1, below) and therefore have to be able to
		// *see* the events that authorised the contested ones. Without them the checks would have to
		// reach outside the set they are given, and an algorithm whose result depends on what a lookup
		// happened to be able to find is not an algorithm two servers can run independently.
		full = full.union(conflictedStateSubgraph(graph, conflicted))
	}

	// Step 1: power events from the full conflicted set, widened by their auth
	// chains, sorted into the reverse topological power ordering.
	power := powerEventsInFullConflictedSet(graph, full)
	order := reverseTopologicalPowerOrder(rules, graph, power)

	// Step 2: iterative auth checks over those events, starting from the state
	// everyone already agrees on.
	//
	// Modification 1 of the version 12 page: that starting state is empty. Version 2 seeded the
	// checks with the unconflicted state map, which means an event could be authorised by state that
	// no branch of the fork had to authorise it; version 12 makes the power events justify themselves
	// from nothing. Step 5 still overwrites with the unconflicted map at the end, so the agreed state
	// is not lost — it simply stops being an assumption partway through.
	start := State{}
	if !twoPointOne {
		start = unconflicted
	}
	partial := iterativeAuthChecks(rules, graph, start, order, authChecks)

	// Step 4: everything else, ordered by the mainline ordering based on the power
	// levels in the partially resolved state.
	remaining := full.minus(power)
	ordered := mainlineOrder(graph, remaining, powerLevelsEventIn(partial))

	// Step 4 again: apply them.
	resolved := iterativeAuthChecks(rules, graph, partial, ordered, authChecks)

	// Step 5: the state everyone agreed on always wins.
	for key, event := range unconflicted {
		resolved[key] = event
	}
	return resolved, nil
}

// eventKey returns the (type, state_key) pair of an event.
func eventKey(event map[string]any) eventauth.Key {
	key := eventauth.Key{}
	if typ, ok := event["type"].(string); ok {
		key.Type = typ
	}
	if stateKey, ok := event["state_key"].(string); ok {
		key.StateKey = stateKey
	}
	return key
}

func eventID(event map[string]any) string {
	id, _ := event["event_id"].(string)
	return id
}

func eventSender(event map[string]any) string {
	sender, _ := event["sender"].(string)
	return sender
}

// eventTimestamp reads an event's origin_server_ts.
//
// The types matter. An event decoded from JSON carries a json.Number there — canonical JSON
// needs integers to survive re-encoding, so decoding to float64 is not an option — and
// json.Number's method is Int64() (int64, error), not the single-value method an interface
// assertion would match. Reading it as the wrong type returns zero, which silently removes
// the timestamp comparison from the mainline ordering and lets the algorithm fall through
// to comparing event IDs: a different server ordering by timestamp would then reach a
// different answer, and the room splits.
func eventTimestamp(event map[string]any) int64 {
	switch ts := event["origin_server_ts"].(type) {
	case int64:
		return ts
	case int:
		return int64(ts)
	case float64:
		return int64(ts)
	case json.Number:
		value, err := ts.Int64()
		if err != nil {
			return 0
		}
		return value
	case string:
		value, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return 0
		}
		return value
	default:
		return 0
	}
}

// eventSet is a set of events keyed by event ID, which is how the algorithm talks
// about the conflicted set and the auth difference.
type eventSet map[string]map[string]any

func (s eventSet) union(other eventSet) eventSet {
	out := make(eventSet, len(s)+len(other))
	for id, ev := range s {
		out[id] = ev
	}
	for id, ev := range other {
		out[id] = ev
	}
	return out
}

func (s eventSet) minus(other eventSet) eventSet {
	out := make(eventSet, len(s))
	for id, ev := range s {
		if _, ok := other[id]; !ok {
			out[id] = ev
		}
	}
	return out
}

// splitStates produces the unconflicted state map (every state set holds the same
// event for the key) and the conflicted state set (everything else).
func splitStates(states []State) (State, eventSet) {
	counts := map[eventauth.Key]map[string]int{}
	events := map[eventauth.Key]map[string]map[string]any{}
	for _, state := range states {
		for key, event := range state {
			if counts[key] == nil {
				counts[key] = map[string]int{}
				events[key] = map[string]map[string]any{}
			}
			id := eventID(event)
			counts[key][id]++
			events[key][id] = event
		}
	}
	unconflicted := State{}
	conflicted := eventSet{}
	for key, ids := range counts {
		if len(ids) == 1 && ids[onlyID(ids)] == len(states) {
			unconflicted[key] = events[key][onlyID(ids)]
			continue
		}
		for id, event := range events[key] {
			conflicted[id] = event
		}
	}
	return unconflicted, conflicted
}

func onlyID(ids map[string]int) string {
	for id := range ids {
		return id
	}
	return ""
}

// conflictedStateSubgraph returns the events lying on an auth_events path between two events in the
// conflicted set, endpoints included — the definition room version 12 adds and folds into the full
// conflicted set.
//
// Read literally the definition is about paths between pairs, and it is worth stating the equivalent
// condition this function implements because the literal reading invites two wrong implementations.
// An event X is in the subgraph when some conflicted event reaches X by walking `auth_events` edges
// (so X lies inside a conflicted event's auth chain — that is the universe below) *and* X itself
// reaches some conflicted event that way. Auth edges point backwards in time, so the second condition
// asks whether X sits *later* than a contested event, and together the two say exactly "between two
// contested events".
//
// The first wrong implementation takes each conflicted event's auth chain and keeps the conflicted
// events in it: that is the endpoints and none of what is between them, which is the part the
// definition is about. The second walks the graph once and marks whatever path it happened to take
// first — but the definition quantifies over *all* paths between *any* pair, so a first-found path is
// an answer that depends on iteration order. This walks twice instead: forward to build the universe,
// then backward from the conflicted events to find which parts of the universe lead back to one. Both
// passes are linear in the edges the resolution is already walking when it computes the auth
// difference, so the modification costs a constant factor and no new kind of lookup.
func conflictedStateSubgraph(graph Graph, conflicted eventSet) eventSet {
	universe := map[string]bool{}
	queue := make([]string, 0, len(conflicted))
	for id := range conflicted {
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if universe[current] {
			continue
		}
		universe[current] = true
		queue = append(queue, graph.AuthEventIDs(current)...)
	}

	// The reverse of the edges inside the universe, so a walk starting from the conflicted events finds
	// every event from which a conflicted event is still reachable.
	parents := map[string][]string{}
	for id := range universe {
		for _, authID := range graph.AuthEventIDs(id) {
			if universe[authID] {
				parents[authID] = append(parents[authID], id)
			}
		}
	}
	between := map[string]bool{}
	queue = queue[:0]
	for id := range conflicted {
		if universe[id] {
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if between[current] {
			continue
		}
		between[current] = true
		queue = append(queue, parents[current]...)
	}

	subgraph := eventSet{}
	for id := range between {
		event, ok := graph.Event(id)
		if !ok {
			// An event in an auth chain that cannot be read is not something a resolution can put in a
			// set, which is how the auth difference has always answered the same question: an event is
			// what its bytes say, not what a reference to it implies.
			continue
		}
		subgraph[id] = event
	}
	return subgraph
}

// authChain walks the auth events of an event, transitively.
func authChain(graph Graph, id string) map[string]bool {
	chain := map[string]bool{}
	queue := []string{id}
	for len(queue) > 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if chain[current] {
			continue
		}
		chain[current] = true
		queue = append(queue, graph.AuthEventIDs(current)...)
	}
	return chain
}

// authDifference returns the events that appear in some auth chains but not all.
func authDifference(graph Graph, states []State) eventSet {
	union := map[string]bool{}
	intersection := map[string]bool{}
	for i, state := range states {
		chain := map[string]bool{}
		for _, event := range state {
			for id := range authChain(graph, eventID(event)) {
				chain[id] = true
			}
		}
		for id := range chain {
			union[id] = true
		}
		if i == 0 {
			for id := range chain {
				intersection[id] = true
			}
		} else {
			for id := range intersection {
				if !chain[id] {
					delete(intersection, id)
				}
			}
		}
	}
	difference := eventSet{}
	for id := range union {
		if intersection[id] {
			continue
		}
		if event, ok := graph.Event(id); ok {
			difference[id] = event
		}
	}
	return difference
}

// isPowerEvent reports whether an event can take someone's ability to do something
// away: power levels, join rules, and a leave or ban applied by someone else.
func isPowerEvent(event map[string]any) bool {
	switch typ, _ := event["type"].(string); typ {
	case eventauth.TypePowerLevels, eventauth.TypeJoinRules:
		return true
	case eventauth.TypeMember:
		membership := ""
		if content, ok := event["content"].(map[string]any); ok {
			membership, _ = content["membership"].(string)
		}
		if membership != eventauth.MembershipLeave && membership != eventauth.MembershipBan {
			return false
		}
		key := eventKey(event)
		return key.StateKey != "" && eventSender(event) != key.StateKey
	default:
		return false
	}
}

// powerEventsInFullConflictedSet implements step 1's widening: a power event brings
// in the events in its auth chain that are also in the full conflicted set.
func powerEventsInFullConflictedSet(graph Graph, full eventSet) eventSet {
	power := eventSet{}
	for id, event := range full {
		if isPowerEvent(event) {
			power[id] = event
		}
	}
	widened := eventSet{}
	for id := range power {
		for chainID := range authChain(graph, id) {
			if event, ok := full[chainID]; ok {
				widened[chainID] = event
			}
		}
	}
	return widened
}

// powerLevelsFromAuthEvents reads the power levels an event was authorised against,
// which is what the topological comparison uses.
func powerLevelsFromAuthEvents(rules roomversion.Rules, graph Graph, id string) *eventauth.PowerLevels {
	for _, authID := range graph.AuthEventIDs(id) {
		event, ok := graph.Event(authID)
		if !ok {
			continue
		}
		if typ, _ := event["type"].(string); typ != eventauth.TypePowerLevels {
			continue
		}
		content, _ := event["content"].(map[string]any)
		pl, err := eventauth.LoadPowerLevels(rules, content)
		if err == nil {
			return pl
		}
	}
	return eventauth.DefaultPowerLevels()
}

// reverseTopologicalPowerOrder sorts events from earliest to latest using the
// smallest event first, where "smallest" compares the sender's power level (higher
// sorts first), then origin_server_ts, then the event ID, following the DAG formed
// by auth events (Kahn's algorithm).
func reverseTopologicalPowerOrder(rules roomversion.Rules, graph Graph, set eventSet) []map[string]any {
	indegree := map[string]int{}
	children := map[string][]string{}
	for id := range set {
		indegree[id] = 0
	}
	for id := range set {
		for _, authID := range graph.AuthEventIDs(id) {
			if _, ok := set[authID]; !ok {
				continue
			}
			// An event comes after its auth events.
			indegree[id]++
			children[authID] = append(children[authID], id)
		}
	}

	less := func(a, b string) bool {
		eventA, eventB := set[a], set[b]
		levelA := powerLevelsFromAuthEvents(rules, graph, a).User(eventSender(eventA))
		levelB := powerLevelsFromAuthEvents(rules, graph, b).User(eventSender(eventB))
		if levelA != levelB {
			return levelA > levelB
		}
		if tsA, tsB := eventTimestamp(eventA), eventTimestamp(eventB); tsA != tsB {
			return tsA < tsB
		}
		return a < b
	}

	available := make([]string, 0, len(set))
	for id, degree := range indegree {
		if degree == 0 {
			available = append(available, id)
		}
	}
	sort.Slice(available, func(i, j int) bool { return less(available[i], available[j]) })

	var ordered []map[string]any
	for len(available) > 0 {
		next := available[0]
		available = available[1:]
		ordered = append(ordered, set[next])
		for _, child := range children[next] {
			indegree[child]--
			if indegree[child] == 0 {
				available = append(available, child)
			}
		}
		sort.Slice(available, func(i, j int) bool { return less(available[i], available[j]) })
	}
	if len(ordered) != len(set) {
		// A cycle in auth events should not exist; if one does, keep the events that
		// were left rather than dropping them silently.
		for id := range set {
			found := false
			for _, event := range ordered {
				if eventID(event) == id {
					found = true
					break
				}
			}
			if !found {
				ordered = append(ordered, set[id])
			}
		}
	}
	return ordered
}

// mainlineOrder sorts events by the mainline of the given power levels event.
func mainlineOrder(graph Graph, set eventSet, base map[string]any) []map[string]any {
	if base == nil {
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		ordered := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			ordered = append(ordered, set[id])
		}
		return ordered
	}

	mainline := mainlineOf(graph, eventID(base))
	position := func(id string) int {
		// Walk this event's own power levels chain and find where it first meets the
		// mainline. Not meeting it at all means "infinitely far".
		current := id
		for hops := 0; hops < len(set)+len(mainline)+16; hops++ {
			next := powerLevelsEventID(graph, current)
			if next == "" {
				return len(mainline) + 1
			}
			if index, ok := mainline[next]; ok {
				return index
			}
			current = next
		}
		return len(mainline) + 1
	}

	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		pi, pj := position(ids[i]), position(ids[j])
		if pi != pj {
			// A larger mainline position sorts first: its auth chain rests on an
			// earlier point in the mainline.
			return pi > pj
		}
		eventA, eventB := set[ids[i]], set[ids[j]]
		if tsA, tsB := eventTimestamp(eventA), eventTimestamp(eventB); tsA != tsB {
			return tsA < tsB
		}
		return ids[i] < ids[j]
	})
	ordered := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, set[id])
	}
	return ordered
}

// mainlineOf walks the power levels chain of an event, oldest first.
func mainlineOf(graph Graph, id string) map[string]int {
	mainline := map[string]int{}
	index := 0
	for current := id; current != ""; current = powerLevelsEventID(graph, current) {
		if _, seen := mainline[current]; seen {
			break
		}
		mainline[current] = index
		index++
	}
	return mainline
}

// powerLevelsEventID returns the m.room.power_levels event cited by an event.
func powerLevelsEventID(graph Graph, id string) string {
	for _, authID := range graph.AuthEventIDs(id) {
		event, ok := graph.Event(authID)
		if !ok {
			continue
		}
		if typ, _ := event["type"].(string); typ == eventauth.TypePowerLevels {
			return authID
		}
	}
	return ""
}

// powerLevelsEventIn returns the power levels event held by a resolved state.
func powerLevelsEventIn(state State) map[string]any {
	event, _ := state[eventauth.Key{Type: eventauth.TypePowerLevels}]
	return event
}

// iterativeAuthChecks applies events in order, keeping only those the authorisation
// rules allow over the state built so far. A rejected event is skipped rather than
// failing the resolution: that is how a fork that one server refused converges.
//
// Where the state is missing something the rules need, the event's own auth events
// supply it, as the specification requires — but a rejected auth event is not used.
func iterativeAuthChecks(rules roomversion.Rules, graph Graph, start State, ordered []map[string]any, opts eventauth.Opts) State {
	current := make(State, len(start))
	for key, event := range start {
		current[key] = event
	}

	for _, event := range ordered {
		working := make(State, len(current)+8)
		for key, held := range current {
			working[key] = held
		}
		// Fill gaps from the event's own auth events.
		for _, authID := range graph.AuthEventIDs(eventID(event)) {
			if graph.Rejected(authID) {
				continue
			}
			authEvent, ok := graph.Event(authID)
			if !ok {
				continue
			}
			key := eventKey(authEvent)
			if _, present := working[key]; !present {
				working[key] = authEvent
			}
		}
		if err := eventauth.Check(rules, event, NewSnapshot(graph, working), opts); err != nil {
			continue
		}
		if _, isState := event["state_key"]; isState {
			current[eventKey(event)] = event
		}
	}
	return current
}
