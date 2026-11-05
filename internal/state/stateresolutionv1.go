package state

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/roomversion"
)

// resolveV1 resolves room state the way the pinned room version 1 page defines it.
//
// The algorithm is much smaller than the one room version 2 introduced, and it has its own ordering
// keys: depth, and the SHA-1 of the event ID. It works one class of event at a time — power levels
// first, because who holds power decides how everything after it is judged; then join rules; then
// memberships; then everything else, which cannot change what the authorisation rules look at and so
// only needs a choice every server makes the same way.
//
// The room version 1 page describes this version as buggy — state can reset to an older version of
// itself, users can lose their power levels, banned users can rejoin. Implementing it is not
// endorsing it: rooms in that version exist and have been running for a decade, and their state has
// to be computed the way that version defines it, or two servers holding the same room disagree about
// who is in it.
//
// The other honest fact about this algorithm is that not every server computes it this way. Synapse
// resolves version 1 rooms with a richer procedure (auth difference, power events, a reverse
// topological ordering), taken from an older and fuller description of state resolution 1. The two
// agree on a room without a fork, which is the overwhelming majority of rooms, and can disagree on a
// forked version 1 room. The pinned specification page is what this server follows, per the project's
// precedence rules; .agent-work/REQUIREMENTS.md records the divergence and what it costs.
func resolveV1(rules roomversion.Rules, graph Graph, states []State, opts eventauth.Opts) (State, error) {
	// A key is contested when the states give different events for it; anything they agree on is
	// already resolved and is never reconsidered below.
	byKey := map[eventauth.Key]map[string]map[string]any{}
	for _, held := range states {
		for key, event := range held {
			if byKey[key] == nil {
				byKey[key] = map[string]map[string]any{}
			}
			byKey[key][eventID(event)] = event
		}
	}
	resolved := State{}
	contested := map[eventauth.Key][]map[string]any{}
	for key, byID := range byKey {
		if len(byID) == 1 {
			for _, event := range byID {
				resolved[key] = event
			}
			continue
		}
		candidates := make([]map[string]any, 0, len(byID))
		for _, event := range byID {
			candidates = append(candidates, event)
		}
		contested[key] = candidates
	}

	// The three classes that affect the authorisation rules, in the order the page gives them.
	for _, kind := range []string{eventauth.TypePowerLevels, eventauth.TypeJoinRules, eventauth.TypeMember} {
		resolveClassOfConflict(rules, graph, resolved, contested, kind, opts)
	}

	// Everything else cannot change what the rules consider, so it needs only a deterministic pick:
	// the highest depth, and the lowest SHA-1 of the event ID to break a tie, among the candidates
	// that the authorisation rules allow over the state built so far.
	for key, candidates := range contested {
		ordered := append([]map[string]any{}, candidates...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if depthOf(ordered[i]) != depthOf(ordered[j]) {
				return depthOf(ordered[i]) > depthOf(ordered[j])
			}
			return sha1OfEvent(ordered[i]) < sha1OfEvent(ordered[j])
		})
		for _, candidate := range ordered {
			if allowedOver(rules, graph, resolved, candidate, opts) {
				resolved[key] = candidate
				break
			}
		}
		// If no candidate is allowed over the state built so far, the key is simply absent from the
		// result. That is what the page leaves it as: the working state was started without the
		// conflicting entries, and nothing was added back.
	}
	return resolved, nil
}

// resolveClassOfConflict resolves the contested keys of one state-event class, following the
// procedure the room version 1 page repeats three times: gather every event of that class from the
// states being resolved, order them by ascending depth and then descending SHA-1 of the event ID,
// put the first into the working state, and add each later one only while the authorisation rules
// allow it — stopping at the first one they do not, and leaving the rest of that class alone.
//
// The class is removed from `contested` as it is handled, so the later pass over the leftovers does
// not reconsider it; a class that was never contested simply does nothing here.
func resolveClassOfConflict(rules roomversion.Rules, graph Graph, resolved State, contested map[eventauth.Key][]map[string]any, kind string, opts eventauth.Opts) {
	var gathered []map[string]any
	seen := map[string]bool{}
	for key, candidates := range contested {
		if key.Type != kind {
			continue
		}
		for _, candidate := range candidates {
			if !seen[eventID(candidate)] {
				seen[eventID(candidate)] = true
				gathered = append(gathered, candidate)
			}
		}
	}
	if len(gathered) == 0 {
		return
	}
	sort.SliceStable(gathered, func(i, j int) bool {
		if depthOf(gathered[i]) != depthOf(gathered[j]) {
			return depthOf(gathered[i]) < depthOf(gathered[j])
		}
		return sha1OfEvent(gathered[i]) > sha1OfEvent(gathered[j])
	})

	// The first event goes in without being asked: it is the oldest of the class, and the state has
	// to start somewhere.
	first := gathered[0]
	resolved[eventKey(first)] = first
	for key := range contested {
		if key.Type == kind {
			delete(contested, key)
		}
	}
	for _, candidate := range gathered[1:] {
		if !allowedOver(rules, graph, resolved, candidate, opts) {
			// Stop here: the page moves on to the next class rather than trying the events that
			// come later in this one.
			return
		}
		resolved[eventKey(candidate)] = candidate
	}
}

// allowedOver asks the authorisation rules whether an event would be allowed in the state built so
// far. The event's own auth events fill any gap in that state, which is what the iterative auth
// checks do everywhere else, and a rejected auth event is not used.
func allowedOver(rules roomversion.Rules, graph Graph, resolved State, event map[string]any, opts eventauth.Opts) bool {
	// The state is used exactly as the resolution has built it, including whatever the class already
	// settled: that is what "would be allowed for a room in state R" means, and it is why an event
	// that would have been fine in the old state can be refused here.
	working := make(State, len(resolved)+8)
	for key, held := range resolved {
		working[key] = held
	}
	// What the state does not hold is filled from the event's own auth events, as the auth checks do
	// everywhere else — an event has to be judged against the state its sender said authorised it,
	// and a receiver that cannot see those events cannot judge it at all. A rejected auth event is
	// not used.
	for _, authID := range graph.AuthEventIDs(eventID(event)) {
		if graph.Rejected(authID) {
			continue
		}
		authEvent, ok := graph.Event(authID)
		if !ok {
			continue
		}
		authKey := eventKey(authEvent)
		if _, present := working[authKey]; !present {
			working[authKey] = authEvent
		}
	}
	return eventauth.Check(rules, event, NewSnapshot(graph, working), opts) == nil
}

// depthOf reads an event's depth, which is the ordering key this algorithm uses in place of the
// auth-chain comparisons the later algorithm uses.
func depthOf(event map[string]any) int64 {
	switch depth := event["depth"].(type) {
	case int64:
		return depth
	case int:
		return int64(depth)
	case float64:
		return int64(depth)
	default:
		return 0
	}
}

// sha1OfEvent is the tie-breaker the room version 1 page gives: the SHA-1 of the event ID, compared
// as a lower-case hexadecimal string. It is not a security property — the algorithm only needs every
// server to order the same way.
func sha1OfEvent(event map[string]any) string {
	sum := sha1.Sum([]byte(eventID(event)))
	return hex.EncodeToString(sum[:])
}
