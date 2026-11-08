package unit

// State Resolution 2.1, the algorithm room version 12 names (MSC4297).
//
// These tests are about the modification that changes an answer. The other one — the conflicted state
// subgraph widening the full conflicted set — is a property of a set rather than of a resolved state
// map, and is tested against the function in internal/state, where the shape of an auth graph can be
// stated directly instead of inferred through everything else the algorithm does.
//
// What is for, in one sentence each:
//
//   - a version 12 fork resolves under version 12's rules, with the create event absent from every
//     auth chain the resolution walks, which is the same reason `eventauth.createEventOf` exists;
//   - the iterative auth checks start from an empty state, which makes an event be judged by the state
//     that its own auth events describe rather than by the state everyone agrees on *now*;
//   - and where the two algorithms disagree, the version 12 answer is the one the room keeps.
//
// A note on the fixtures, because the first draft of this file got them wrong in a way worth
// recording. Events here cite what the auth selection algorithm names, including the *sender's* member
// event: that citation is not decoration, it is how an event carries the evidence that its sender was
// in the room, and with the iterative checks starting from an empty state it is the only evidence
// there is. An event that cites nothing is refused under version 12 by a rule about power levels and
// nothing to do with the change, and a test that asserts that is a test of the fixture.

import (
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
	"github.com/enimatek/lattice/internal/state"
)

// newV12Room returns a version 12 room's opening state and its identifier.
//
// The identifier is derived from the create event rather than chosen, because rule 2 of that version's
// authorisation rules checks every event against it: a fixture that named its own room would produce a
// room this server refuses to authorise anything in. The create event is then registered in the graph
// under the event identifier its room identifier implies (`$` where the room identifier has `!`), which
// is how a rule that needs the create event finds one that no event is permitted to cite.
func newV12Room(t *testing.T, plUsers map[string]any) (roomID string, base []map[string]any) {
	t.Helper()

	create := v12CreateEvent(map[string]any{"room_version": "12"})
	derived, err := events.RoomIDFromCreate(roomversion.MustGet("12"), create)
	if err != nil {
		t.Fatalf("the room's create event could not name the room: %v", err)
	}
	create["event_id"] = "$" + derived[1:]

	member := func(id, sender, target, membership string, auth []string, ts int64) map[string]any {
		return stEvent("m.room.member", id, sender, target, map[string]any{"membership": membership}, auth, ts)
	}
	aliceJoin := member("$alicejoin", "@alice:example.org", "@alice:example.org", "join", nil, 2)
	powerLevels := stEvent("m.room.power_levels", "$pl", "@alice:example.org", "", powerContent(plUsers),
		[]string{"$alicejoin"}, 3)
	joinRules := stEvent("m.room.join_rules", "$joinrules", "@alice:example.org", "",
		map[string]any{"join_rule": "public"}, []string{"$alicejoin"}, 4)
	bobJoin := member("$bobjoin", "@bob:example.org", "@bob:example.org", "join",
		[]string{"$pl", "$joinrules"}, 5)

	base = []map[string]any{create, aliceJoin, powerLevels, joinRules, bobJoin}
	for _, event := range base {
		if event["type"] == "m.room.create" {
			continue
		}
		event["room_id"] = derived
	}
	return derived, base
}

func powerContent(users map[string]any) map[string]any {
	return map[string]any{
		"users": users, "users_default": 0, "state_default": 50,
		"ban": 50, "kick": 50, "invite": 0, "events_default": 0,
	}
}

// v12Side is one side of a fork: the room's opening state plus the events that side holds.
func v12Side(base []map[string]any, extra ...map[string]any) state.State {
	all := append(append([]map[string]any{}, base...), extra...)
	return stateOf(all...)
}

// TestResolveV12BanBeatsJoinAcrossAFork is the security-relevant case run under the new algorithm: one
// branch has bob joined, the other has alice ban bob, and the ban has to win.
//
// It is the version 2 test case carried over deliberately. The answer must not change between the two
// algorithms, and reaching it here means the version 12 authorisation rules ran inside the iterative
// checks over a room whose create event is cited by nothing: the ban needs alice's power level and her
// membership, and in a start that is empty the only place either can come from is the ban event's own
// auth events — which is why the fixture cites alice's join rather than leaving the graph to find it.
func TestResolveV12BanBeatsJoinAcrossAFork(t *testing.T) {
	t.Parallel()
	roomID, base := newV12Room(t, map[string]any{"@alice:example.org": 100})

	joined := stEvent("m.room.member", "$bobjoinfork", "@bob:example.org", "@bob:example.org",
		map[string]any{"membership": "join"}, []string{"$pl", "$joinrules"}, 10)
	banned := stEvent("m.room.member", "$bobban", "@alice:example.org", "@bob:example.org",
		map[string]any{"membership": "ban"}, []string{"$pl", "$alicejoin", "$bobjoin"}, 11)
	for _, event := range []map[string]any{joined, banned} {
		event["room_id"] = roomID
	}

	graph := graphWith(append(append([]map[string]any{}, base...), joined, banned)...)
	left, right := v12Side(base, joined), v12Side(base, banned)

	// Both orders, because state resolution has to converge. A server that reached a different answer
	// depending on which fork it was shown first is a room split waiting to happen.
	for _, sides := range [][2]state.State{{left, right}, {right, left}} {
		resolved, err := state.Resolve(roomversion.MustGet("12"), graph, sides[:], eventauth.Opts{})
		if err != nil {
			t.Fatalf("a version 12 fork must resolve under state resolution 2.1, not be refused: %v", err)
		}
		member, ok := resolved[eventauth.Key{Type: "m.room.member", StateKey: "@bob:example.org"}]
		if !ok {
			t.Fatalf("the resolved state holds no membership for bob")
		}
		content, _ := member["content"].(map[string]any)
		if membership, _ := content["membership"].(string); membership != "ban" {
			t.Errorf("bob's resolved membership is %q, want ban: a ban must beat a join whichever fork is offered first", membership)
		}
	}
}

// TestResolveV12JudgesAnEventByTheStateThatAuthorisedIt is the observable half of modification 1: the
// iterative auth checks start from an empty state map instead of the unconflicted state map.
//
// The fixture is the scenario the change was written for, and the direction matters. Bob once had the
// power to set a join rule; the room later agreed a power levels event taking it away, and that later
// event is what both sides of the fork agree on. A join rule event of bob's from the earlier time —
// which cites the power levels event that authorised it, as it must — arrives on one side of a fork,
// and alice's own join rule event arrives on the other.
//
// State Resolution 2 seeds the checks with what everyone agrees on, so bob's event is judged against a
// power levels event that postdates it, refuses it, and alice's join rule wins. State Resolution 2.1
// seeds with nothing, so bob's event is judged against the state it brings with it, passes, and — being
// the later of the two power events, which the reverse topological power ordering puts last — takes the
// key.
//
// That is the difference the MSC was arguing for rather than an accident of implementation: the state
// everyone agrees on is the state *now*, and an event that was authorised years ago does not become
// unauthorised because the room changed underneath it. Complement drives the same scenario over
// federation in TestMSC4297StateResolutionV2_1_starts_from_empty_set, where the room's replayed join
// rule must survive; this is that assertion where a fork can be built in a millisecond instead of
// reconstructed by lying about /state_ids.
func TestResolveV12JudgesAnEventByTheStateThatAuthorisedIt(t *testing.T) {
	t.Parallel()
	key := eventauth.Key{Type: "m.room.join_rules", StateKey: ""}

	// The version 12 case. `plOld` is history: it is in the graph, because bob's event cites it and
	// the resolution has to read it, and it is in neither side's state map, because the room no longer
	// holds it.
	roomID, base := newV12Room(t, map[string]any{"@alice:example.org": 100, "@bob:example.org": 0})
	plOld := stEvent("m.room.power_levels", "$plold", "@alice:example.org", "",
		powerContent(map[string]any{"@alice:example.org": 100, "@bob:example.org": 100}),
		[]string{"$alicejoin"}, 15)
	plOld["room_id"] = roomID
	byBob := stEvent("m.room.join_rules", "$bybob", "@bob:example.org", "",
		map[string]any{"join_rule": "knock"}, []string{"$plold", "$bobjoin"}, 30)
	byAlice := stEvent("m.room.join_rules", "$byalice", "@alice:example.org", "",
		map[string]any{"join_rule": "invite"}, []string{"$pl", "$alicejoin"}, 20)
	for _, event := range []map[string]any{byBob, byAlice} {
		event["room_id"] = roomID
	}

	graph := graphWith(append(append([]map[string]any{}, base...), plOld, byBob, byAlice)...)
	resolved, err := state.Resolve(roomversion.MustGet("12"), graph,
		[]state.State{v12Side(base, byBob), v12Side(base, byAlice)}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("version 12 resolution: %v", err)
	}
	if held, ok := resolved[key]; !ok {
		t.Fatalf("version 12 left the room with no join rule at all")
	} else if held["event_id"] != "$bybob" {
		t.Errorf("version 12 resolved the join rule to %v, want $bybob: bob's event is authorised by the power levels event it cites, and the room's later demotion of him is not a verdict on an event that predates it",
			held["event_id"])
	}

	// The version 2 answer over the same shape of fork: the demotion is handed to the checks, bob's
	// event is refused by it, and alice keeps the join rule. Recorded because it is the difference
	// rather than its absence, and because a reader of the version 12 code should be able to see that
	// the older algorithm produced the other answer for a reason.
	v11Base := forkBase()
	v11BobJoin := stEvent("m.room.member", "$bobjoin:example.org", "@bob:example.org", "@bob:example.org",
		map[string]any{"membership": "join"}, []string{"$create:example.org", "$pl0:example.org"}, 5)
	v11DemoteBob := stEvent("m.room.power_levels", "$plnow:example.org", "@alice:example.org", "",
		powerContent(map[string]any{"@alice:example.org": 100, "@bob:example.org": 0}),
		[]string{"$create:example.org", "$alicejoin:example.org"}, 6)
	v11RaiseBob := stEvent("m.room.power_levels", "$plold:example.org", "@alice:example.org", "",
		powerContent(map[string]any{"@alice:example.org": 100, "@bob:example.org": 100}),
		[]string{"$create:example.org", "$alicejoin:example.org"}, 7)
	v11ByBob := stEvent("m.room.join_rules", "$v11bybob", "@bob:example.org", "",
		map[string]any{"join_rule": "knock"},
		[]string{"$create:example.org", "$plold:example.org", "$bobjoin:example.org"}, 30)
	v11ByAlice := stEvent("m.room.join_rules", "$v11byalice", "@alice:example.org", "",
		map[string]any{"join_rule": "invite"},
		[]string{"$create:example.org", "$plnow:example.org", "$alicejoin:example.org"}, 20)
	v11Base = append(v11Base, v11BobJoin, v11DemoteBob)
	v11Graph := graphWith(append(append([]map[string]any{}, v11Base...), v11RaiseBob, v11ByBob, v11ByAlice)...)

	v11Resolved, err := state.Resolve(roomversion.MustGet("11"), v11Graph,
		[]state.State{v12Side(v11Base, v11ByBob), v12Side(v11Base, v11ByAlice)}, eventauth.Opts{})
	if err != nil {
		t.Fatalf("version 11 resolution: %v", err)
	}
	held, ok := v11Resolved[key]
	if !ok {
		t.Fatalf("version 2 left the room with no join rule, which is its own bug: it should refuse bob's event and keep alice's")
	}
	if held["event_id"] != "$v11byalice" {
		t.Errorf("version 2 resolved the join rule to %v, want alice's event: seeded with the agreed demotion of bob, his own earlier event cannot authorise",
			held["event_id"])
	}
}
