// Package stateres resolves room state: given two or more homeservers' claims about
// what a room's state is, it answers what the state is.
//
// A room is a DAG, so two events can be created at the same time and change the same
// state key. Both are valid when they are made, and no server can be told which one
// "won" - the answer has to be computed from the events themselves, by every server
// that has them, the same way. That is what this file is: the state resolution
// algorithm for room version 2 and later, which every room version from 3 on reuses
// unchanged. It is a pure function over a graph of events and the authorization rules
// of the room version, so it can be tested with events made up on the spot, and it is
// the same code the federation endpoints use when a peer sends a state set and the
// intake uses when a room's graph has forked.
//
// The algorithm has a shape worth stating, because each part exists for a reason:
//
//   - The state sets are split into keys every set agrees on (unconflicted) and the
//     events where they disagree (conflicted). Only the disagreements need resolving.
//   - Resolving them means re-running the authorization rules, and the rules read
//     events that are in neither state set - the room's create event, the power levels
//     somebody changed three years ago. Those come from the auth chains, which is why
//     the full conflicted set adds the auth difference.
//   - The order the events are re-checked in matters: an event can only authorise what
//     follows it, so a kick has to be applied before the join it forbids. Power events
//     are sorted topologically first (an event that takes power away is considered as
//     early as its own authority allows), then everything else by mainline order, which
//     asks "which power levels event was in effect when this event was authorised?" -
//     the question a person reading a room's history answers when they decide which
//     change should have stuck.
//   - An event that fails the rules is dropped. If it was a power event, the state
//     entries it was trying to change are dropped with it, because leaving a stale
//     entry behind is how a revoked administrator keeps their power.
package stateres

import (
	"fmt"
	"sort"

	"github.com/enimatek-nl/lattice/internal/authcheck"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/power"
)

// Resolve computes the state of a room from one or more state sets, each a claim about
// the room's state - normally the state at the leaves of a forked graph. The graph has
// to hold every event those sets name plus the events their auth chains name; an event
// the graph does not have is read as unusable and left out of the fallbacks the rules
// use, which is the honest answer and lets a resolution be computed from partial
// knowledge.
//
// Rejected events - the ones the graph marks as having failed their auth checks - are
// resolved like any other, which is what the specification asks for: whether an event
// was rejected can differ between servers, and ignoring them would close no attack
// that a duplicated event would not open anyway.
func Resolve(g *authcheck.Graph, sets []authcheck.State) (authcheck.State, error) {
	if g == nil {
		return nil, fmt.Errorf("state resolution needs a graph to read")
	}
	unconflicted, conflicted := split(g, sets)
	if len(conflicted) == 0 {
		return unconflicted, nil
	}
	full := fullConflictedSet(g, sets, conflicted)

	// Step 1: the power events of the full conflicted set, widened by the parts of
	// their auth chains that are also in the set, sorted earliest to latest.
	var powerEvents []*authcheck.Event
	seen := map[string]bool{}
	for _, event := range full {
		if !isPowerEvent(event) || seen[event.ID] {
			continue
		}
		for _, candidate := range append([]*authcheck.Event{event}, lookupAll(g, authChain(g, event.ID))...) {
			if seen[candidate.ID] || !containsEvent(full, candidate) {
				continue
			}
			seen[candidate.ID] = true
			powerEvents = append(powerEvents, candidate)
		}
	}
	powerEvents = reverseTopologicalPowerOrder(g, powerEvents)

	// Step 2: apply them over the state every set agreed on.
	state := iterativeAuthChecks(g, unconflicted, powerEvents)

	// Steps 3 and 4: the rest, ordered by mainline order based on the power levels
	// event the partially resolved state now holds.
	var rest []*authcheck.Event
	for _, event := range full {
		if !seen[event.ID] {
			rest = append(rest, event)
		}
	}
	state = iterativeAuthChecks(g, state, mainlineOrder(g, state, rest))

	// Step 5: an unconflicted key is never overridden - every server agreed on it.
	for key, id := range unconflicted {
		state[key] = id
	}
	return state, nil
}

// lookupAll resolves a list of event ids against the graph, dropping ids the graph has
// no event for.
func lookupAll(g *authcheck.Graph, ids []string) []*authcheck.Event {
	out := make([]*authcheck.Event, 0, len(ids))
	for _, id := range ids {
		if event := g.Lookup(id); event != nil {
			out = append(out, event)
		}
	}
	return out
}

// split divides the state sets into keys every set holds with the same value, and every
// event that is not such a value. A key that only some of the sets hold is a
// disagreement even when those sets agree: the set that omits it says something.
func split(g *authcheck.Graph, sets []authcheck.State) (authcheck.State, []*authcheck.Event) {
	unconflicted := authcheck.State{}
	if len(sets) == 0 {
		return unconflicted, nil
	}
	values := map[authcheck.Key]map[string]bool{}
	present := map[authcheck.Key]int{}
	for _, set := range sets {
		for key, id := range set {
			if values[key] == nil {
				values[key] = map[string]bool{}
			}
			values[key][id] = true
			present[key]++
		}
	}
	conflictedIDs := map[string]bool{}
	for key, held := range values {
		if present[key] == len(sets) && len(held) == 1 {
			for id := range held {
				unconflicted[key] = id
			}
			continue
		}
		for id := range held {
			conflictedIDs[id] = true
		}
	}
	out := make([]*authcheck.Event, 0, len(conflictedIDs))
	for id := range conflictedIDs {
		if event := g.Lookup(id); event != nil {
			out = append(out, event)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return unconflicted, out
}

// fullConflictedSet is the conflicted events plus the auth difference: the auth chain
// events that do not appear in every state set's chain. Those events are what makes two
// servers disagree about who was allowed to do what, and the rules need them even
// though neither state set holds them.
func fullConflictedSet(g *authcheck.Graph, sets []authcheck.State, conflicted []*authcheck.Event) []*authcheck.Event {
	out := append([]*authcheck.Event{}, conflicted...)
	if len(sets) >= 2 {
		chains := make([]map[string]bool, 0, len(sets))
		for _, set := range sets {
			chain := map[string]bool{}
			for _, id := range set {
				chain[id] = true
				for _, ancestor := range authChain(g, id) {
					chain[ancestor] = true
				}
			}
			chains = append(chains, chain)
		}
		for id := range chains[0] {
			inEvery := true
			for _, chain := range chains[1:] {
				if !chain[id] {
					inEvery = false
					break
				}
			}
			if inEvery {
				continue
			}
			if event := g.Lookup(id); event != nil {
				out = append(out, event)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return dedupeEvents(out)
}

// dedupeEvents collapses a list of events to one entry per id, keeping the order.
func dedupeEvents(events []*authcheck.Event) []*authcheck.Event {
	seen := map[string]bool{}
	out := make([]*authcheck.Event, 0, len(events))
	for _, event := range events {
		if event == nil || event.ID == "" || seen[event.ID] {
			continue
		}
		seen[event.ID] = true
		out = append(out, event)
	}
	return out
}

// containsEvent reports whether a list holds an event.
func containsEvent(events []*authcheck.Event, want *authcheck.Event) bool {
	if want == nil {
		return false
	}
	for _, event := range events {
		if event != nil && event.ID == want.ID {
			return true
		}
	}
	return false
}

// authChain walks an event's auth_events, and their auth_events, and so on. The event
// itself is not part of its own chain.
func authChain(g *authcheck.Graph, eventID string) []string {
	var out []string
	seen := map[string]bool{eventID: true}
	queue := []string{eventID}
	for len(queue) > 0 {
		current := g.Lookup(queue[0])
		queue = queue[1:]
		if current == nil {
			continue
		}
		for _, id := range current.AuthIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
			queue = append(queue, id)
		}
	}
	return out
}

// isPowerEvent reports whether an event can take somebody's ability to act away: a
// power levels event, a join rules event, or a member event that leaves or bans
// somebody and was written by somebody else. Those are resolved first, because an event
// that loses power must not be able to hand out more of it afterwards.
func isPowerEvent(event *authcheck.Event) bool {
	if event == nil || !event.IsState() {
		return false
	}
	switch event.Type {
	case matrix.TypePowerLevels, matrix.TypeJoinRules:
		return true
	case matrix.TypeMember:
		membership := event.Membership()
		if membership != matrix.MembershipLeave && membership != matrix.MembershipBan {
			return false
		}
		return event.Sender != authcheck.StateKeyOfString(event.StateKey)
	default:
		return false
	}
}

// reverseTopologicalPowerOrder sorts events from earliest to latest over the DAG their
// auth_events form, taking the smallest available event at each step. "Smallest" asks
// who held the power: an event whose sender held more power comes first, so that a
// change of power is weighed before what somebody with less power did under the old
// one. Ties fall to origin_server_ts and then to the event id, which is what makes the
// ordering the same on every server.
func reverseTopologicalPowerOrder(g *authcheck.Graph, events []*authcheck.Event) []*authcheck.Event {
	events = dedupeEvents(events)
	inSet := map[string]bool{}
	byID := map[string]*authcheck.Event{}
	for _, event := range events {
		inSet[event.ID] = true
		byID[event.ID] = event
	}
	// incoming counts, for each event, how many events of the set it was authorised
	// against: those have to be weighed first.
	incoming := map[string]int{}
	dependants := map[string][]string{}
	for _, event := range events {
		count := 0
		for _, id := range event.AuthIDs {
			if !inSet[id] || id == event.ID {
				continue
			}
			count++
			dependants[id] = append(dependants[id], event.ID)
		}
		incoming[event.ID] = count
	}
	var ready []*authcheck.Event
	for _, event := range events {
		if incoming[event.ID] == 0 {
			ready = append(ready, event)
		}
	}
	out := make([]*authcheck.Event, 0, len(events))
	for len(ready) > 0 {
		sort.SliceStable(ready, func(i, j int) bool { return smaller(g, ready[i], ready[j]) })
		next := ready[0]
		ready = ready[1:]
		out = append(out, next)
		for _, id := range dependants[next.ID] {
			incoming[id]--
			if incoming[id] == 0 {
				ready = append(ready, byID[id])
			}
		}
	}
	if len(out) < len(events) {
		// The auth links do not form a DAG: an event cites something that cites it
		// back. That cannot happen in a room built honestly, but a peer can send it,
		// and the specification says to carry on as if the cycle were not there by
		// picking the smallest event that is left over.
		left := map[string]bool{}
		for _, event := range events {
			if !containsEvent(out, event) {
				left[event.ID] = true
			}
		}
		for len(left) > 0 {
			var candidates []*authcheck.Event
			for id := range left {
				candidates = append(candidates, byID[id])
			}
			sort.SliceStable(candidates, func(i, j int) bool { return smaller(g, candidates[i], candidates[j]) })
			out = append(out, candidates[0])
			delete(left, candidates[0].ID)
		}
	}
	return out
}

// smaller compares two events by the relation the reverse topological power ordering
// uses: sender power level (higher first), then origin_server_ts, then event id. Each
// sender's level is read from the power levels event that event was itself authorised
// against, which is why two events can be compared by different tables of levels.
func smaller(g *authcheck.Graph, x, y *authcheck.Event) bool {
	lx, ly := senderLevel(g, x), senderLevel(g, y)
	if lx != ly {
		return lx > ly
	}
	if x.OriginServerTS != y.OriginServerTS {
		return x.OriginServerTS < y.OriginServerTS
	}
	return x.ID < y.ID
}

// senderLevel reads the power level an event's sender held, from the power levels event
// in that event's own auth events.
func senderLevel(g *authcheck.Graph, event *authcheck.Event) int64 {
	creator := ""
	levels := authcheck.PowerLevelsOf(g, nil)
	for _, id := range event.AuthIDs {
		auth := g.Lookup(id)
		if auth == nil {
			continue
		}
		switch auth.Type {
		case matrix.TypeCreate:
			creator = auth.Sender
		case matrix.TypePowerLevels:
			levels = authcheck.PowerLevelsOf(g, auth)
		}
	}
	return power.Level(levels, event.Sender, creator)
}

// mainlineOrder sorts events by their position on the mainline of a power levels event:
// how far back in the chain of power changes an event was authorised. An event
// authorised under a more recent power levels event sorts later, which is what makes a
// change made under the newer table of levels beat one made under the older one.
func mainlineOrder(g *authcheck.Graph, state authcheck.State, events []*authcheck.Event) []*authcheck.Event {
	base := mainlineOf(g, state)
	sorted := dedupeEvents(events)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := mainlinePosition(g, sorted[i], base), mainlinePosition(g, sorted[j], base)
		if pi != pj {
			// A larger position means the event's authority reaches back further, and
			// the ordering puts that event first.
			return pi > pj
		}
		if sorted[i].OriginServerTS != sorted[j].OriginServerTS {
			return sorted[i].OriginServerTS < sorted[j].OriginServerTS
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

// mainlineOf returns the mainline of the power levels event the state holds: that event
// and the power levels events its own auth events cite, walking back to the room's
// creation.
func mainlineOf(g *authcheck.Graph, state authcheck.State) []string {
	id, ok := state[authcheck.Key{Type: matrix.TypePowerLevels}]
	if !ok {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		out = append(out, id)
		id = ""
		event := g.Lookup(out[len(out)-1])
		if event == nil {
			break
		}
		for _, authID := range event.AuthIDs {
			if auth := g.Lookup(authID); auth != nil && auth.Type == matrix.TypePowerLevels {
				id = authID
				break
			}
		}
	}
	return out
}

// mainlinePosition returns how far back into a mainline an event's authority reaches.
// An event that reaches none of it is placed past the end, which is the specification's
// sentinel for "this event is not based on any of these power changes".
func mainlinePosition(g *authcheck.Graph, event *authcheck.Event, mainline []string) int {
	position := len(mainline)
	for _, id := range chainOfPowerEvents(g, event) {
		for i, candidate := range mainline {
			if id == candidate && i < position {
				position = i
			}
		}
	}
	return position
}

// chainOfPowerEvents walks an event's auth events looking for power levels events, and
// walks those in turn: the same walk the event's own mainline makes, starting one step
// below the event itself.
func chainOfPowerEvents(g *authcheck.Graph, event *authcheck.Event) []string {
	var out []string
	seen := map[string]bool{event.ID: true}
	queue := []string{event.ID}
	for len(queue) > 0 {
		current := g.Lookup(queue[0])
		queue = queue[1:]
		if current == nil {
			continue
		}
		for _, id := range current.AuthIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			auth := g.Lookup(id)
			if auth == nil || auth.Type != matrix.TypePowerLevels {
				continue
			}
			out = append(out, id)
			queue = append(queue, id)
		}
	}
	return out
}

// iterativeAuthChecks walks a list of events oldest first, applying each to the state
// when the authorization rules allow it in the state built so far. An event that fails
// is left out, and the state built so far carries on.
func iterativeAuthChecks(g *authcheck.Graph, start authcheck.State, events []*authcheck.Event) authcheck.State {
	state := authcheck.State{}
	for key, id := range start {
		state[key] = id
	}
	for _, event := range events {
		if !event.IsState() {
			continue
		}
		candidate := authcheck.State{}
		for key, id := range state {
			candidate[key] = id
		}
		// The rules read state the state built so far may not hold yet. The event's own
		// auth events are where those came from, so they fill the missing keys - unless
		// the graph says that event was rejected, in which case it says nothing.
		for _, id := range event.AuthIDs {
			auth := g.Lookup(id)
			if auth == nil || !auth.IsState() {
				continue
			}
			if _, ok := candidate[auth.Key()]; ok {
				continue
			}
			if _, rejected := g.Rejected[auth.ID]; rejected {
				continue
			}
			candidate[auth.Key()] = auth.ID
		}
		// Room version 2 ignores an event that does not pass the rules. Room version 1
		// also dropped the state entry a failed *power* event was aiming at, on the
		// theory that a revoked administrator should not keep their entry; version 2
		// gets the same protection from weighing power events first, and removing the
		// entry here would lose a change that the other fork made honestly.
		if err := authcheck.Rules(g, candidate, event); err == nil {
			state[event.Key()] = event.ID
		}
	}
	return state
}
