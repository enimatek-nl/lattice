package unit

import (
	"testing"

	"github.com/enimatek-nl/lattice/internal/authcheck"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/stateres"
)

// A room is a DAG, so two homeservers can make two events at the same moment that
// change the same state key. Nothing in either event says which one won, and no
// server gets to decide: the answer has to be computed from the events, by everybody,
// the same way. The cases below pin what that computation produces, because every one
// of them is a room two servers would otherwise see differently.

const (
	resAlice = "@alice:alice.example"
	resBob   = "@bob:bob.example"
	resCarol = "@carol:carol.example"
)

// resEvent is a short name for a state event in a resolution fixture. The auth events
// it cites matter twice over: the resolver fills the state keys a check needs from
// them, and the mainline ordering reads the power levels event among them.
func resEvent(id, typ, stateKey, sender string, ts int64, content matrix.Object, auth ...string) *authcheck.Event {
	key := stateKey
	return &authcheck.Event{
		ID:             id,
		Type:           typ,
		StateKey:       &key,
		Sender:         sender,
		OriginServerTS: ts,
		Content:        content,
		AuthIDs:        auth,
	}
}

func resMember(id, target, membership, sender string, ts int64, auth ...string) *authcheck.Event {
	return resEvent(id, matrix.TypeMember, target, sender, ts,
		matrix.Object{"membership": membership}, auth...)
}

func resLevels(id, sender string, ts int64, users matrix.Object, auth ...string) *authcheck.Event {
	return resEvent(id, matrix.TypePowerLevels, "", sender, ts, matrix.Object{"users": users}, auth...)
}

// resRoom is the room every case starts from: alice's, public, with alice and bob
// joined. It returns the graph and the state a server that has seen nothing since
// would hold.
func resRoom(t *testing.T) (*authcheck.Graph, authcheck.State) {
	t.Helper()
	g := authcheck.NewGraph("11")
	create := resEvent("$create", matrix.TypeCreate, "", resAlice, 1,
		matrix.Object{"creator": resAlice})
	pl := resLevels("$pl", resAlice, 2, matrix.Object{resAlice: int64(100), resBob: int64(0)},
		"$create")
	rules := resEvent("$jr", matrix.TypeJoinRules, "", resAlice, 2,
		matrix.Object{"join_rule": matrix.JoinRulesPublic}, "$create", "$pl")
	ma := resMember("$ma", resAlice, matrix.MembershipJoin, resAlice, 2, "$create", "$pl", "$jr")
	mb := resMember("$mb", resBob, matrix.MembershipJoin, resBob, 3, "$create", "$pl", "$jr")
	for _, event := range []*authcheck.Event{create, pl, rules, ma, mb} {
		g.Add(event)
	}
	state := authcheck.State{
		{Type: matrix.TypeCreate}:                     "$create",
		{Type: matrix.TypePowerLevels}:                "$pl",
		{Type: matrix.TypeJoinRules}:                  "$jr",
		{Type: matrix.TypeMember, StateKey: resAlice}: "$ma",
		{Type: matrix.TypeMember, StateKey: resBob}:   "$mb",
	}
	return g, state
}

// TestStateResolutionAgreement covers the cases where there is nothing to resolve: a
// resolution of one state, and of several states that say the same thing.
func TestStateResolutionAgreement(t *testing.T) {
	g, base := resRoom(t)
	other := authcheck.State{}
	for key, id := range base {
		other[key] = id
	}
	got, err := stateres.Resolve(g, []authcheck.State{base, other})
	if err != nil {
		t.Fatalf("resolution of two identical states failed: %v", err)
	}
	if len(got) != len(base) {
		t.Fatalf("resolving identical states changed the state: %d entries, want %d", len(got), len(base))
	}
	for key, id := range base {
		if got[key] != id {
			t.Errorf("resolving identical states replaced %s: got %s, want %s", key, got[key], id)
		}
	}
}

// TestStateResolutionForks pins what happens when two servers fork a room.
func TestStateResolutionForks(t *testing.T) {
	tests := []struct {
		name string
		// build adds the events of each fork to the graph and returns the two state
		// sets that claim them.
		build func(t *testing.T, g *authcheck.Graph, base authcheck.State) []authcheck.State
		// want and absent name the state entries the resolved state must and must not
		// hold.
		want   map[authcheck.Key]string
		absent []authcheck.Key
	}{
		{
			// Two administrators rename the room at the same moment. Neither event is
			// wrong, and the resolution cannot ask which one the room "really" wanted;
			// the tie falls to the timestamp, and both servers land on the same name.
			name: "two renames, the later timestamp wins",
			build: func(t *testing.T, g *authcheck.Graph, base authcheck.State) []authcheck.State {
				t.Helper()
				first := resEvent("$name1", matrix.TypeName, "", resAlice, 10,
					matrix.Object{"name": "first"}, "$create", "$pl", "$ma")
				second := resEvent("$name2", matrix.TypeName, "", resAlice, 11,
					matrix.Object{"name": "second"}, "$create", "$pl", "$ma")
				g.Add(first)
				g.Add(second)
				left := cloneState(base)
				left[authcheck.Key{Type: matrix.TypeName}] = "$name1"
				right := cloneState(base)
				right[authcheck.Key{Type: matrix.TypeName}] = "$name2"
				return []authcheck.State{left, right}
			},
			want: map[authcheck.Key]string{{Type: matrix.TypeName}: "$name2"},
		},
		{
			// The room's most important fork: one server kicks a user, the other keeps
			// acting as though they are in the room. The kick is a power event, so it is
			// weighed first, and what the kicked user did afterwards cannot pass the
			// rules. Without that ordering, a kicked user's state would survive the kick.
			name: "a kick beats what the kicked user did on the other fork",
			build: func(t *testing.T, g *authcheck.Graph, base authcheck.State) []authcheck.State {
				t.Helper()
				kick := resMember("$kick", resBob, matrix.MembershipLeave, resAlice, 10,
					"$create", "$pl", "$jr", "$ma", "$mb")
				name := resEvent("$name", matrix.TypeName, "", resBob, 11,
					matrix.Object{"name": "Bob was here"}, "$create", "$pl", "$jr", "$mb")
				g.Add(kick)
				g.Add(name)
				left := cloneState(base)
				left[authcheck.Key{Type: matrix.TypeMember, StateKey: resBob}] = "$kick"
				right := cloneState(base)
				right[authcheck.Key{Type: matrix.TypeName}] = "$name"
				return []authcheck.State{left, right}
			},
			want:   map[authcheck.Key]string{{Type: matrix.TypeMember, StateKey: resBob}: "$kick"},
			absent: []authcheck.Key{{Type: matrix.TypeName}},
		},
		{
			// Two power levels events, one authorised by the other. The one built on the
			// newer levels has to be weighed after it, which it is: the auth links say
			// so, and the topological sort follows them.
			name: "a power change built on another power change outlives it",
			build: func(t *testing.T, g *authcheck.Graph, base authcheck.State) []authcheck.State {
				t.Helper()
				grant := resLevels("$grant", resAlice, 10, matrix.Object{
					resAlice: int64(100), resBob: int64(100),
				}, "$create", "$pl", "$ma")
				// bob writes a second set of levels, and the levels he read were the ones
				// that made him an administrator: the auth link is what says so.
				extend := resLevels("$extend", resBob, 11, matrix.Object{
					resAlice: int64(100), resBob: int64(100), resCarol: int64(50),
				}, "$create", "$grant", "$ma")
				g.Add(grant)
				g.Add(extend)
				left := cloneState(base)
				left[authcheck.Key{Type: matrix.TypePowerLevels}] = "$grant"
				right := cloneState(base)
				right[authcheck.Key{Type: matrix.TypePowerLevels}] = "$extend"
				return []authcheck.State{left, right}
			},
			want: map[authcheck.Key]string{{Type: matrix.TypePowerLevels}: "$extend"},
		},
		{
			// The same pair, but the second change was not authorised by the first: bob
			// wrote it while he still had no power. The rules refuse it, and the grant
			// stands. A resolution that only compared timestamps would land on the
			// forgery.
			name: "a power change that was never authorised is dropped",
			build: func(t *testing.T, g *authcheck.Graph, base authcheck.State) []authcheck.State {
				t.Helper()
				grant := resLevels("$grant", resAlice, 10, matrix.Object{
					resAlice: int64(100), resBob: int64(100),
				}, "$create", "$pl", "$ma")
				forgery := resLevels("$forgery", resBob, 11, matrix.Object{
					resAlice: int64(0), resBob: int64(100),
				}, "$create", "$pl", "$mb")
				g.Add(grant)
				g.Add(forgery)
				left := cloneState(base)
				left[authcheck.Key{Type: matrix.TypePowerLevels}] = "$grant"
				right := cloneState(base)
				right[authcheck.Key{Type: matrix.TypePowerLevels}] = "$forgery"
				return []authcheck.State{left, right}
			},
			want: map[authcheck.Key]string{{Type: matrix.TypePowerLevels}: "$grant"},
		},
		{
			// Two renames at the very same millisecond. The timestamps cannot separate
			// them, so the event id does: the relation has to be total, or two servers
			// holding the same events resolve to different rooms.
			name: "two renames at the same timestamp, the larger event id wins",
			build: func(t *testing.T, g *authcheck.Graph, base authcheck.State) []authcheck.State {
				t.Helper()
				small := resEvent("$nameA", matrix.TypeName, "", resAlice, 10,
					matrix.Object{"name": "alpha"}, "$create", "$pl", "$ma")
				large := resEvent("$nameB", matrix.TypeName, "", resAlice, 10,
					matrix.Object{"name": "bravo"}, "$create", "$pl", "$ma")
				g.Add(small)
				g.Add(large)
				left := cloneState(base)
				left[authcheck.Key{Type: matrix.TypeName}] = "$nameA"
				right := cloneState(base)
				right[authcheck.Key{Type: matrix.TypeName}] = "$nameB"
				return []authcheck.State{left, right}
			},
			want: map[authcheck.Key]string{{Type: matrix.TypeName}: "$nameB"},
		},
		{
			// A join rule that one fork relaxed and the other did not. The relaxed rule
			// was written by an administrator, so both survive their own checks and the
			// ordering decides - by timestamp, since neither is built on the other.
			name: "the later join rule change stands",
			build: func(t *testing.T, g *authcheck.Graph, base authcheck.State) []authcheck.State {
				t.Helper()
				invite := resEvent("$jr2", matrix.TypeJoinRules, "", resAlice, 12,
					matrix.Object{"join_rule": matrix.JoinRulesInvite}, "$create", "$pl", "$ma")
				g.Add(invite)
				right := cloneState(base)
				right[authcheck.Key{Type: matrix.TypeJoinRules}] = "$jr2"
				return []authcheck.State{base, right}
			},
			want: map[authcheck.Key]string{{Type: matrix.TypeJoinRules}: "$jr2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, base := resRoom(t)
			sets := tt.build(t, g, base)
			got, err := stateres.Resolve(g, sets)
			if err != nil {
				t.Fatalf("resolution failed: %v", err)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Errorf("%s resolved to %q, want %q", key, got[key], want)
				}
			}
			for _, key := range tt.absent {
				if id, ok := got[key]; ok {
					t.Errorf("%s resolved to %q, want it left out", key, id)
				}
			}
			// The room's beginning survives every resolution: a resolution that lost the
			// create event or the join rules would leave a room that cannot authorise
			// anything at all.
			for _, key := range []authcheck.Key{
				{Type: matrix.TypeCreate}, {Type: matrix.TypeJoinRules},
				{Type: matrix.TypeMember, StateKey: resAlice},
			} {
				if _, ok := got[key]; !ok {
					t.Errorf("the resolved state lost %s", key)
				}
			}
		})
	}
}

// TestStateResolutionUsesAuthChain checks the half of the full conflicted set that is
// not in either state: the events the auth chains hold. A room's rules read the power
// levels event even when neither fork's state still points at it, and an event the
// server has marked rejected may not be used through the back door.
func TestStateResolutionUsesAuthChain(t *testing.T) {
	g, base := resRoom(t)
	// One fork replaced the room's levels; the other kept them and let bob rename the
	// room, citing the levels event that granted him the power.
	grant := resLevels("$grant", resAlice, 10, matrix.Object{
		resAlice: int64(100), resBob: int64(50),
	}, "$create", "$pl", "$ma")
	name := resEvent("$name", matrix.TypeName, "", resBob, 11,
		matrix.Object{"name": "Bob can name things"}, "$create", "$grant", "$jr", "$mb")
	g.Add(grant)
	g.Add(name)

	left := cloneState(base)
	left[authcheck.Key{Type: matrix.TypePowerLevels}] = "$grant"
	right := cloneState(base)
	right[authcheck.Key{Type: matrix.TypeName}] = "$name"

	got, err := stateres.Resolve(g, []authcheck.State{left, right})
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if got[authcheck.Key{Type: matrix.TypeName}] != "$name" {
		t.Fatalf("the rename was dropped although the grant that allows it is in the auth chain: %v", got)
	}

	// The same rename, with the grant marked rejected. An event that this server
	// refused says nothing, whichever chain it arrived through.
	g2, base2 := resRoom(t)
	g2.Add(grant)
	g2.Add(name)
	g2.Reject("$grant", "the levels event was written by somebody without the power to write it")
	left2 := cloneState(base2)
	left2[authcheck.Key{Type: matrix.TypePowerLevels}] = "$pl"
	right2 := cloneState(base2)
	right2[authcheck.Key{Type: matrix.TypeName}] = "$name"
	got2, err := stateres.Resolve(g2, []authcheck.State{left2, right2})
	if err != nil {
		t.Fatalf("resolution over a rejected auth event failed: %v", err)
	}
	if _, ok := got2[authcheck.Key{Type: matrix.TypeName}]; ok {
		t.Errorf("a rename authorised only by a rejected event was resolved into the state: %v", got2)
	}
}

// cloneState copies a state map, so a case can fork it.
func cloneState(state authcheck.State) authcheck.State {
	out := authcheck.State{}
	for key, id := range state {
		out[key] = id
	}
	return out
}
