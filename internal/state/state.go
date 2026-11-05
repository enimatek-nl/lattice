// Package state resolves room state across forks in the event graph.
//
// Matrix rooms are a DAG, so two servers can hold different state for the same
// point in history. State Resolution 2 is what makes them converge, and it is the
// algorithm that decides who holds power after a fork — a wrong implementation does
// not fail loudly, it silently gives different servers different state, which is a
// room split. The algorithm here follows the "State resolution" section of the
// pinned room version pages (v1.19) step by step.
//
// Room versions 2 through 11 use the algorithm implemented here. Room version 1 uses
// the older algorithm and version 12 a revised one; both are refused rather than
// approximated.
package state

import (
	"errors"
	"fmt"
	"sort"

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
	switch rules.StateRes {
	case roomversion.StateResV2:
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

	// Step 1: power events from the full conflicted set, widened by their auth
	// chains, sorted into the reverse topological power ordering.
	power := powerEventsInFullConflictedSet(graph, full)
	order := reverseTopologicalPowerOrder(rules, graph, power)

	// Step 2: iterative auth checks over those events, starting from the state
	// everyone already agrees on.
	partial := iterativeAuthChecks(rules, graph, unconflicted, order, authChecks)

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

func eventTimestamp(event map[string]any) int64 {
	switch ts := event["origin_server_ts"].(type) {
	case int64:
		return ts
	case int:
		return int64(ts)
	case float64:
		return int64(ts)
	default:
		if n, ok := ts.(interface{ Int64() int64 }); ok {
			return n.Int64()
		}
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
