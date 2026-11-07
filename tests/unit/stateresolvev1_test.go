package unit

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/roomversion"
	"github.com/enimatek/lattice/internal/state"
)

// The state resolution algorithm of room version 1 is a different algorithm from the one every later
// version uses, with its own ordering keys: depth, and the SHA-1 of the event ID. It is tested here
// on its own terms rather than assumed to behave like the later one, because the places they differ
// are exactly the places a room splits in two: which power levels event stands, which join rule
// stands, and who is in the room afterwards.

// v1Event is a state event with the depth this algorithm orders by.
func v1Event(eventType, id, sender, stateKey string, content map[string]any, authIDs []string, depth int64) map[string]any {
	event := stEvent(eventType, id, sender, stateKey, content, authIDs, 1700000000000+depth)
	event["depth"] = depth
	return event
}

// v1Base is what both sides of a fork agree on in a room version 1 room: alice created the room,
// alice has joined, alice holds power level 100, and the room is public.
func v1Base() []map[string]any {
	create := v1Event("m.room.create", "$create:example.org", "@alice:example.org", "",
		map[string]any{"creator": "@alice:example.org", "room_version": "1"}, nil, 1)
	aliceJoin := v1Event("m.room.member", "$alicejoin:example.org", "@alice:example.org", "@alice:example.org",
		map[string]any{"membership": "join"}, []string{"$create:example.org"}, 2)
	joinRules := v1Event("m.room.join_rules", "$joinrules:example.org", "@alice:example.org", "",
		map[string]any{"join_rule": "public"}, []string{"$create:example.org", "$alicejoin:example.org"}, 3)
	levels := v1Event("m.room.power_levels", "$pl0:example.org", "@alice:example.org", "",
		map[string]any{
			"users":         map[string]any{"@alice:example.org": 100},
			"state_default": 50,
			"kick":          50,
			"ban":           50,
		}, []string{"$create:example.org", "$alicejoin:example.org"}, 4)
	return []map[string]any{create, aliceJoin, joinRules, levels}
}

// TestResolveV1PreservesAgreedState is the least the algorithm has to get right: with nothing
// contested, the state comes back as it went in.
func TestResolveV1PreservesAgreedState(t *testing.T) {
	t.Parallel()
	base := v1Base()
	graph := graphWith(base...)
	resolved, err := state.Resolve(roomversion.MustGet("1"), graph,
		[]state.State{stateOf(base...), stateOf(base...)}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved) != len(base) {
		t.Errorf("resolving two copies of the same state gave %d entries, wanting %d", len(resolved), len(base))
	}
	for key, event := range stateOf(base...) {
		if idOf(resolved[key]) != idOf(event) {
			t.Errorf("%s resolved to %v, wanting %v", key, idOf(resolved[key]), idOf(event))
		}
	}
}

// TestResolveV1PowerLevelsAreResolvedFirstAndTheEarliestIsNotJudged covers the shape of the
// algorithm: the power levels of a fork are settled before anything else, the first of them in
// depth order goes in without being asked, and each later one is kept only while the authorisation
// rules allow it over what has been built so far.
//
// The case where that matters is a fork in which the later event grants power that the earlier one
// has already taken away. Judged against a state that still held the old levels it would pass; judged
// as the algorithm judges it, after the earlier event has been applied, it does not — and the
// earlier event's answer is the one every server reaches.
func TestResolveV1PowerLevelsAreResolvedFirstAndTheEarliestIsNotJudged(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		earliestUsers map[string]any
		laterUsers    map[string]any
		wantLevels    string
	}{
		{
			// The earlier event leaves alice at 100, so granting bob 50 is within her reach:
			// the later event stands.
			name:          "a grant the sender can still make",
			earliestUsers: map[string]any{"@alice:example.org": 100},
			laterUsers:    map[string]any{"@alice:example.org": 100, "@bob:example.org": 50},
			wantLevels:    "$plater:example.org",
		},
		{
			// The earlier event left alice at 0 — it was added to the state without being
			// judged, being the first of its class. The later event grants bob 100, which a
			// user at 0 cannot do, so it is refused and the earlier event's levels are what
			// the room has.
			name:          "a grant the earlier event made impossible",
			earliestUsers: map[string]any{"@alice:example.org": 0},
			laterUsers:    map[string]any{"@alice:example.org": 0, "@bob:example.org": 100},
			wantLevels:    "$plearliest:example.org",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := v1Base()
			earliest := v1Event("m.room.power_levels", "$plearliest:example.org", "@alice:example.org", "",
				map[string]any{"users": tc.earliestUsers, "state_default": 50},
				[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 5)
			later := v1Event("m.room.power_levels", "$plater:example.org", "@alice:example.org", "",
				map[string]any{"users": tc.laterUsers, "state_default": 50},
				[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 6)

			// Two forks that disagree only about the power levels event.
			left := append(append([]map[string]any{}, base...), earliest)
			right := append(append([]map[string]any{}, base...), later)
			graph := graphWith(append(left, right...)...)
			resolved, err := state.Resolve(roomversion.MustGet("1"), graph,
				[]state.State{stateOf(left...), stateOf(right...)}, eventauth.Opts{})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			got := resolved[eventauth.Key{Type: "m.room.power_levels"}]
			if idOf(got) != tc.wantLevels {
				t.Errorf("the room's power levels resolved to %v, wanting %s", idOf(got), tc.wantLevels)
			}
		})
	}
}

// TestResolveV1JoinRulesThenMemberships is the ordering between the classes: the join rule of a fork
// is settled after the power levels and before the memberships, and a later join rule that the room's
// own power levels allow is the one that stands.
func TestResolveV1JoinRulesThenMemberships(t *testing.T) {
	t.Parallel()
	base := v1Base()
	publicFirst := v1Event("m.room.join_rules", "$jrpublic:example.org", "@alice:example.org", "",
		map[string]any{"join_rule": "public"},
		[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 5)
	inviteLater := v1Event("m.room.join_rules", "$jrinvite:example.org", "@alice:example.org", "",
		map[string]any{"join_rule": "invite"},
		[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 6)

	left := append(append([]map[string]any{}, base...), publicFirst)
	right := append(append([]map[string]any{}, base...), inviteLater)
	graph := graphWith(append(left, right...)...)
	resolved, err := state.Resolve(roomversion.MustGet("1"), graph,
		[]state.State{stateOf(left...), stateOf(right...)}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resolved[eventauth.Key{Type: "m.room.join_rules"}]; idOf(got) != "$jrinvite:example.org" {
		t.Errorf("the join rule resolved to %v, wanting the later one, which the room's power levels allow",
			idOf(got))
	}
}

// TestResolveV1OtherConflictsUseDepthThenTheEventID covers the last step: events that cannot change
// what the rules consider are chosen by the highest depth, with the SHA-1 of the event ID breaking a
// tie. The tie-break is what makes two servers that hold the same fork name the same room name; it is
// not a preference.
func TestResolveV1OtherConflictsUseDepthThenTheEventID(t *testing.T) {
	t.Parallel()

	t.Run("the deeper event stands", func(t *testing.T) {
		base := v1Base()
		shallow := v1Event("m.room.name", "$name-shallow:example.org", "@alice:example.org", "",
			map[string]any{"name": "older"},
			[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 5)
		deep := v1Event("m.room.name", "$name-deep:example.org", "@alice:example.org", "",
			map[string]any{"name": "newer"},
			[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 7)
		left := append(append([]map[string]any{}, base...), shallow)
		right := append(append([]map[string]any{}, base...), deep)
		graph := graphWith(append(left, right...)...)
		resolved, err := state.Resolve(roomversion.MustGet("1"), graph,
			[]state.State{stateOf(left...), stateOf(right...)}, eventauth.Opts{})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got := resolved[eventauth.Key{Type: "m.room.name"}]; idOf(got) != "$name-deep:example.org" {
			t.Errorf("the room name resolved to %v, wanting the deeper event", idOf(got))
		}
	})

	t.Run("equal depth is settled by the hash of the event ID", func(t *testing.T) {
		base := v1Base()
		first := v1Event("m.room.name", "$aaa-name:example.org", "@alice:example.org", "",
			map[string]any{"name": "first"},
			[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 5)
		second := v1Event("m.room.name", "$zzz-name:example.org", "@alice:example.org", "",
			map[string]any{"name": "second"},
			[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 5)
		left := append(append([]map[string]any{}, base...), first)
		right := append(append([]map[string]any{}, base...), second)
		graph := graphWith(append(left, right...)...)
		resolved, err := state.Resolve(roomversion.MustGet("1"), graph,
			[]state.State{stateOf(left...), stateOf(right...)}, eventauth.Opts{})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		want := "$zzz-name:example.org"
		if hashOfEventID("$aaa-name:example.org") < hashOfEventID("$zzz-name:example.org") {
			want = "$aaa-name:example.org"
		}
		if got := resolved[eventauth.Key{Type: "m.room.name"}]; idOf(got) != want {
			t.Errorf("a tie on depth resolved to %v, wanting the lower SHA-1 of the event ID (%s)",
				idOf(got), want)
		}
	})
}

// TestResolveV1LeavesAKeyAbsentWhenNothingIsAllowed covers the case the algorithm leaves empty: an
// ordinary state event is judged by the rules like anything else, so when every candidate needs a
// power level its sender does not have, the room ends up with no value for that key at all rather
// than with whichever event happened to arrive first.
func TestResolveV1LeavesAKeyAbsentWhenNothingIsAllowed(t *testing.T) {
	t.Parallel()
	base := v1Base()
	// bob has no power level in this room, and a state event costs 50.
	// bob holds no power level here, and a state event costs 50. He has no member event in the
	// room either, which is what the selection algorithm reflects.
	first := v1Event("m.room.name", "$bobname-a:example.org", "@bob:example.org", "",
		map[string]any{"name": "bob one"},
		[]string{"$create:example.org", "$pl0:example.org"}, 5)
	second := v1Event("m.room.name", "$bobname-b:example.org", "@bob:example.org", "",
		map[string]any{"name": "bob two"},
		[]string{"$create:example.org", "$pl0:example.org"}, 6)
	left := append(append([]map[string]any{}, base...), first)
	right := append(append([]map[string]any{}, base...), second)
	graph := graphWith(append(left, right...)...)
	resolved, err := state.Resolve(roomversion.MustGet("1"), graph,
		[]state.State{stateOf(left...), stateOf(right...)}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resolved[eventauth.Key{Type: "m.room.name"}]; got != nil {
		t.Errorf("a name nobody is allowed to set resolved to %v, wanting the key to be absent", idOf(got))
	}
}

// TestResolveV1WalksPairFormAuthEvents keeps the resolution honest about the event format of these
// versions. In room versions 1 and 2 an auth event is written as [event_id, {"sha256": hash}], so a
// resolution that fills gaps from an event's own auth events sees nothing at all unless it reads that
// shape — and without those events, an otherwise allowed event is judged against state that is missing
// the power levels or the create event, and is refused.
func TestResolveV1WalksPairFormAuthEvents(t *testing.T) {
	t.Parallel()
	base := v1Base()
	for _, event := range base {
		event["auth_events"] = pairReferences(eventsOf(base, event))
	}
	earliest := v1Event("m.room.power_levels", "$plearliest:example.org", "@alice:example.org", "",
		map[string]any{"users": map[string]any{"@alice:example.org": 100}, "state_default": 50},
		[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 5)
	later := v1Event("m.room.power_levels", "$plater:example.org", "@alice:example.org", "",
		map[string]any{"users": map[string]any{"@alice:example.org": 100, "@bob:example.org": 50}, "state_default": 50},
		[]string{"$create:example.org", "$alicejoin:example.org", "$pl0:example.org"}, 6)
	earliest["auth_events"] = pairReferences([]map[string]any{base[0], base[1], base[3]})
	later["auth_events"] = pairReferences([]map[string]any{base[0], base[1], base[3]})

	left := append(append([]map[string]any{}, base...), earliest)
	right := append(append([]map[string]any{}, base...), later)
	graph := graphWith(append(left, right...)...)
	resolved, err := state.Resolve(roomversion.MustGet("1"), graph,
		[]state.State{stateOf(left...), stateOf(right...)}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := resolved[eventauth.Key{Type: "m.room.power_levels"}]
	if idOf(got) != "$plater:example.org" {
		t.Errorf("with pair-form auth events the levels resolved to %v, wanting the later event, "+
			"which is allowed once the auth chain can actually be walked", idOf(got))
	}
}

// pairReferences writes a list of events the way room versions 1 and 2 write a reference: the event
// ID and a hash of it, as a pair.
func pairReferences(referenced []map[string]any) []any {
	out := make([]any, 0, len(referenced))
	for _, event := range referenced {
		sum := sha1.Sum([]byte(event["event_id"].(string)))
		out = append(out, []any{event["event_id"], map[string]any{"sha256": hex.EncodeToString(sum[:])}})
	}
	return out
}

// eventsOf returns the events a given event cites by ID, which is how these tests attach the same
// references in pair form.
func eventsOf(all []map[string]any, event map[string]any) []map[string]any {
	var found []map[string]any
	for _, id := range eventAuthIDs(event) {
		for _, candidate := range all {
			if candidate["event_id"] == id {
				found = append(found, candidate)
			}
		}
	}
	return found
}

func eventAuthIDs(event map[string]any) []string {
	var ids []string
	for _, entry := range event["auth_events"].([]any) {
		if text, ok := entry.(string); ok {
			ids = append(ids, text)
		}
	}
	return ids
}

// hashOfEventID is the ordering key the room version 1 algorithm uses, computed here rather than read
// from the implementation so the test states the rule instead of restating the code.
func hashOfEventID(eventID string) string {
	sum := sha1.Sum([]byte(eventID))
	return hex.EncodeToString(sum[:])
}
