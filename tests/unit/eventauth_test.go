package unit

import (
	"errors"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/roomversion"
)

const authRoom = "!room:example.org"

// mkEvent builds an event. extra holds top-level members; content holds the
// content object.
func mkEvent(eventType, id, sender string, content map[string]any, extra ...map[string]any) map[string]any {
	event := map[string]any{
		"type":             eventType,
		"event_id":         id,
		"room_id":          authRoom,
		"sender":           sender,
		"origin_server_ts": 1700000000000,
		"depth":            1,
		"prev_events":      []any{"$create:example.org"},
		"auth_events":      []any{},
	}
	if content != nil {
		event["content"] = content
	} else {
		event["content"] = map[string]any{}
	}
	for _, m := range extra {
		for k, v := range m {
			event[k] = v
		}
	}
	return event
}

func stateKey(k string) map[string]any { return map[string]any{"state_key": k} }

// baseState is a room created by alice, joined by alice and bob, with bob at
// power level 0 and a public join rule unless a case replaces it.
func baseState(t *testing.T, version string, joinRule string, plContent map[string]any) *eventauth.MemoryState {
	t.Helper()
	create := mkEvent("m.room.create", "$create:example.org", "@alice:example.org",
		map[string]any{"creator": "@alice:example.org", "room_version": version}, stateKey(""))
	if version == "11" || version == "12" {
		delete(create["content"].(map[string]any), "creator")
	}
	memberAlice := mkEvent("m.room.member", "$memberalice:example.org", "@alice:example.org",
		map[string]any{"membership": "join"}, stateKey("@alice:example.org"))
	memberBob := mkEvent("m.room.member", "$memberbob:example.org", "@alice:example.org",
		map[string]any{"membership": "join"}, stateKey("@bob:example.org"))
	join := mkEvent("m.room.join_rules", "$joinrules:example.org", "@alice:example.org",
		map[string]any{"join_rule": joinRule}, stateKey(""))
	pl := mkEvent("m.room.power_levels", "$pl:example.org", "@alice:example.org", plContent, stateKey(""))
	return eventauth.NewMemoryState(create, memberAlice, memberBob, join, pl)
}

func defaultPL() map[string]any {
	return map[string]any{
		"users":          map[string]any{"@alice:example.org": 100},
		"users_default":  0,
		"events_default": 0,
		"state_default":  50,
		"invite":         0,
		"kick":           50,
		"ban":            50,
		"redact":         50,
	}
}

// authorise fills auth_events with what the selection algorithm picks, so most
// cases test the rules rather than the plumbing.
func authorise(rules roomversion.Rules, event map[string]any, state *eventauth.MemoryState) map[string]any {
	ids := eventauth.SelectEventIDs(rules, event, state)
	list := make([]any, 0, len(ids))
	for _, id := range ids {
		list = append(list, id)
	}
	event["auth_events"] = list
	return event
}

type authCase struct {
	name    string
	version string
	join    string
	pl      map[string]any
	event   func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any
	opts    eventauth.Opts
	// snapshot replaces the room state for a case that needs a different one, such
	// as a room with no power levels event yet.
	snapshot func(rules roomversion.Rules, base *eventauth.MemoryState) *eventauth.MemoryState
	wantErr  string // empty means the event must be allowed
}

func runAuthCases(t *testing.T, cases []authCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := roomversion.MustGet(tc.version)
			pl := tc.pl
			if pl == nil {
				pl = defaultPL()
			}
			join := tc.join
			if join == "" {
				join = "public"
			}
			state := baseState(t, tc.version, join, pl)
			if tc.snapshot != nil {
				state = tc.snapshot(rules, state)
			}
			event := tc.event(rules, state)
			// Cases that care about the auth events set them explicitly; the rest get
			// the set the selection algorithm asks for, so they test the rules and not
			// the plumbing.
			if ids, ok := event["auth_events"].([]any); ok && len(ids) == 0 {
				authorise(rules, event, state)
			}
			err := eventauth.Check(rules, event, state, tc.opts)
			failure, isFailure := eventauth.AsFailure(err)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected the event to be allowed, got %v (isFailure=%v)", err, isFailure)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected rejection %q, event was allowed", tc.wantErr)
			}
			if !isFailure {
				t.Fatalf("expected a rule failure, got %v", err)
			}
			if failure.Rule != tc.wantErr && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("rejected by rule %q (%s), wanted %q", failure.Rule, failure.Reason, tc.wantErr)
			}
		})
	}
}

// TestAuthRulesMemberEvents covers rule 4: the membership table.
func TestAuthRulesMemberEvents(t *testing.T) {
	t.Parallel()
	joinBy := func(user string) func(roomversion.Rules, *eventauth.MemoryState) map[string]any {
		return func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
			return authorise(rules, mkEvent("m.room.member", "$new:example.org", user,
				map[string]any{"membership": "join"}, stateKey(user)), state)
		}
	}
	runAuthCases(t, []authCase{
		{name: "join a public room", version: "11", event: joinBy("@carol:example.org")},
		{name: "join an invite-only room", version: "11", join: "invite", event: joinBy("@carol:example.org"), wantErr: "4.3.4"},
		{
			name: "accept an invitation", version: "11", join: "invite",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				state.Add(mkEvent("m.room.member", "$inv:example.org", "@alice:example.org",
					map[string]any{"membership": "invite"}, stateKey("@carol:example.org")))
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
					map[string]any{"membership": "join"}, stateKey("@carol:example.org")), state)
			},
		},
		{name: "join for another user", version: "11", event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
			return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
				map[string]any{"membership": "join"}, stateKey("@carol:example.org")), state)
		}, wantErr: "4.3.2"},
		{name: "banned user joins", version: "11", event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
			state.Add(mkEvent("m.room.member", "$ban:example.org", "@alice:example.org",
				map[string]any{"membership": "ban"}, stateKey("@carol:example.org")))
			return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
				map[string]any{"membership": "join"}, stateKey("@carol:example.org")), state)
		}, wantErr: "4.3.3"},
		{
			name: "the creator's first join needs no invitation", version: "11", join: "invite",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := authorise(rules, mkEvent("m.room.member", "$first:example.org", "@alice:example.org",
					map[string]any{"membership": "join"}, stateKey("@alice:example.org")), state)
				event["prev_events"] = []any{"$create:example.org"}
				return event
			},
		},
		{
			name: "a member event with no membership", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
					map[string]any{}, stateKey("@carol:example.org")), state)
			},
			wantErr: "4.1",
		},
		{
			name: "unknown membership", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
					map[string]any{"membership": "sidle"}, stateKey("@carol:example.org")), state)
			},
			wantErr: "4.8",
		},
		{
			name: "invite with sufficient power", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
					map[string]any{"membership": "invite"}, stateKey("@carol:example.org")), state)
			},
		},
		{
			name: "invite without power", version: "11", pl: map[string]any{"users": map[string]any{"@alice:example.org": 100}, "invite": 50},
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@bob:example.org",
					map[string]any{"membership": "invite"}, stateKey("@carol:example.org")), state)
			},
			wantErr: "4.4.5",
		},
		{
			name: "invite by a user who is not joined", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				state.Add(mkEvent("m.room.member", "$left:example.org", "@dave:example.org",
					map[string]any{"membership": "leave"}, stateKey("@dave:example.org")))
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@dave:example.org",
					map[string]any{"membership": "invite"}, stateKey("@carol:example.org")), state)
			},
			wantErr: "4.4.2",
		},
		{
			name: "invite a user who already joined", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
					map[string]any{"membership": "invite"}, stateKey("@bob:example.org")), state)
			},
			wantErr: "4.4.3",
		},
		{
			name: "leave yourself", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@bob:example.org",
					map[string]any{"membership": "leave"}, stateKey("@bob:example.org")), state)
			},
		},
		{
			name: "leave when already left", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
					map[string]any{"membership": "leave"}, stateKey("@carol:example.org")), state)
			},
			wantErr: "4.5.1",
		},
		{
			name: "kick a user below you", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
					map[string]any{"membership": "leave"}, stateKey("@bob:example.org")), state)
			},
		},
		{
			name: "kick a user at your level", version: "11", pl: map[string]any{"users": map[string]any{"@alice:example.org": 50, "@bob:example.org": 50}},
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
					map[string]any{"membership": "leave"}, stateKey("@bob:example.org")), state)
			},
			wantErr: "4.5.5",
		},
		{
			name: "ban with the ban level", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
					map[string]any{"membership": "ban"}, stateKey("@carol:example.org")), state)
			},
		},
		{
			name: "ban without the ban level", version: "11", pl: map[string]any{"users": map[string]any{"@alice:example.org": 40}},
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
					map[string]any{"membership": "ban"}, stateKey("@carol:example.org")), state)
			},
			wantErr: "4.6.3",
		},
	})
}

// TestAuthRulesKnockGating checks that join rules introduced by a room version are
// unknown to earlier versions, rather than silently honoured.
func TestAuthRulesKnockGating(t *testing.T) {
	t.Parallel()
	knock := func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
		return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
			map[string]any{"membership": "knock"}, stateKey("@carol:example.org")), state)
	}
	runAuthCases(t, []authCase{
		{name: "knock in version 7", version: "7", join: "knock", event: knock},
		{name: "knock in version 6 has no such join rule", version: "6", join: "knock", event: knock, wantErr: "4.7.1"},
		{name: "knock_restricted in version 10", version: "10", join: "knock_restricted", event: knock},
		{name: "knock_restricted in version 9 is unknown", version: "9", join: "knock_restricted", event: knock, wantErr: "4.7.1"},
		{name: "knock_restricted in version 7 is unknown", version: "7", join: "knock_restricted", event: knock, wantErr: "4.7.1"},
		{name: "knock by another user", version: "10", join: "knock", event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
			return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
				map[string]any{"membership": "knock"}, stateKey("@carol:example.org")), state)
		}, wantErr: "4.7.2"},
		{name: "join a knock-only room without an invitation", version: "7", join: "knock",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
					map[string]any{"membership": "join"}, stateKey("@carol:example.org")), state)
			}, wantErr: "4.3.4"},
	})
}

// TestAuthRulesRestrictedJoins covers the version 8 and version 10 restricted join
// paths, including the signature requirement.
func TestAuthRulesRestrictedJoins(t *testing.T) {
	t.Parallel()
	restrictedJoin := func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
		return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
			map[string]any{"membership": "join", "join_authorised_via_users_server": "@alice:example.org"},
			stateKey("@carol:example.org")), state)
	}
	verifying := eventauth.Opts{VerifyServerSignature: func(map[string]any, string) error { return nil }}

	runAuthCases(t, []authCase{
		{name: "restricted join in version 8", version: "8", join: "restricted", event: restrictedJoin, opts: verifying},
		{name: "restricted join in version 10", version: "10", join: "restricted", event: restrictedJoin, opts: verifying},
		{name: "restricted join in version 7 is unknown", version: "7", join: "restricted", event: restrictedJoin, opts: verifying, wantErr: "4.2"},
		{
			name: "restricted join without an authoriser", version: "10", join: "restricted",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
					map[string]any{"membership": "join"}, stateKey("@carol:example.org")), state)
			},
			wantErr: "4.3.5.2",
		},
		{
			name: "authoriser who has left", version: "10", join: "restricted",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				state.Add(mkEvent("m.room.member", "$aliceleft:example.org", "@alice:example.org",
					map[string]any{"membership": "leave"}, stateKey("@alice:example.org")))
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
					map[string]any{"membership": "join", "join_authorised_via_users_server": "@alice:example.org"},
					stateKey("@carol:example.org")), state)
			},
			opts:    verifying,
			wantErr: "4.3.5.2",
		},
		{
			name: "authoriser who cannot invite", version: "10", join: "restricted",
			pl: map[string]any{"users": map[string]any{"@alice:example.org": 100, "@bob:example.org": 10}, "invite": 50},
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
					map[string]any{"membership": "join", "join_authorised_via_users_server": "@bob:example.org"},
					stateKey("@carol:example.org")), state)
			},
			opts:    verifying,
			wantErr: "4.3.5.2",
		},
		{
			name: "unsigned authoriser is not trusted", version: "10", join: "restricted",
			event:   restrictedJoin,
			wantErr: "4.2",
		},
		{
			name: "join_authorised_via_users_server in a version that has no restricted rooms", version: "7",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@carol:example.org",
					map[string]any{"membership": "join", "join_authorised_via_users_server": "@alice:example.org"},
					stateKey("@carol:example.org")), state)
			},
			opts:    verifying,
			wantErr: "4.2",
		},
	})
}

// TestAuthRulesCreateEvent covers rule 1, including how it changed in version 11.
func TestAuthRulesCreateEvent(t *testing.T) {
	t.Parallel()
	runAuthCases(t, []authCase{
		{
			name: "create in version 10", version: "10",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := mkEvent("m.room.create", "$new:example.org", "@alice:example.org",
					map[string]any{"creator": "@alice:example.org", "room_version": "10"}, stateKey(""))
				event["prev_events"] = []any{}
				return event
			},
		},
		{
			name: "create without a creator in version 10", version: "10",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := mkEvent("m.room.create", "$new:example.org", "@alice:example.org",
					map[string]any{"room_version": "10"}, stateKey(""))
				event["prev_events"] = []any{}
				return event
			},
			wantErr: "1.4",
		},
		{
			name: "create without a creator in version 11", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := mkEvent("m.room.create", "$new:example.org", "@alice:example.org",
					map[string]any{"room_version": "11"}, stateKey(""))
				event["prev_events"] = []any{}
				return event
			},
		},
		{
			name: "create with prev_events", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return mkEvent("m.room.create", "$new:example.org", "@alice:example.org",
					map[string]any{"room_version": "11"}, stateKey(""))
			},
			wantErr: "1.1",
		},
		{
			name: "a create event whose room belongs to another server is that server's business", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := mkEvent("m.room.create", "$new:example.org", "@alice:evil.example",
					map[string]any{"room_version": "11"}, stateKey(""))
				event["prev_events"] = []any{}
				event["room_id"] = "!room:evil.example"
				return event
			},
		},
		{
			name: "create naming an unknown room version", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := mkEvent("m.room.create", "$new:example.org", "@alice:example.org",
					map[string]any{"room_version": "com.example.not-a-room-version"}, stateKey(""))
				event["prev_events"] = []any{}
				return event
			},
			wantErr: "1.3",
		},
	})
}

// TestAuthRulesAuthEvents covers rule 2: the shape of the authorisation chain.
func TestAuthRulesAuthEvents(t *testing.T) {
	t.Parallel()
	runAuthCases(t, []authCase{
		{
			name: "no create event among the auth events", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := authorise(rules, mkEvent("m.room.message", "$new:example.org", "@alice:example.org",
					map[string]any{"body": "hi"}), state)
				ids := event["auth_events"].([]any)
				event["auth_events"] = ids[1:] // drop the create event
				return event
			},
			// Rule 2.2 fires before 2.4 here: the selection algorithm asks for the
			// create event whenever the state holds one, so a missing create event is
			// already a mismatch by the time 2.4 would be reached.
			wantErr: "2.2",
		},
		{
			name: "duplicate auth events", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := authorise(rules, mkEvent("m.room.message", "$new:example.org", "@alice:example.org",
					map[string]any{"body": "hi"}), state)
				event["auth_events"] = append(event["auth_events"].([]any), "$create:example.org")
				return event
			},
			wantErr: "2.1",
		},
		{
			name: "auth events that are not the selected set", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return mkEvent("m.room.message", "$new:example.org", "@alice:example.org",
					map[string]any{"body": "hi"}, map[string]any{"auth_events": []any{"$create:example.org"}})
			},
			wantErr: "2.2",
		},
		{
			name: "auth event from another room", version: "11",
			snapshot: func(rules roomversion.Rules, base *eventauth.MemoryState) *eventauth.MemoryState {
				// The room's own power levels event, but stamped with a different
				// room_id: the selected set still matches, so only rule 2.5 can catch it.
				foreign := eventauth.NewMemoryState()
				for _, key := range []eventauth.Key{
					{Type: "m.room.create"}, {Type: "m.room.member", StateKey: "@alice:example.org"},
					{Type: "m.room.join_rules"}, {Type: "m.room.power_levels"},
				} {
					if ev, ok := base.State(key); ok {
						foreign.Add(ev)
					}
				}
				if pl, ok := foreign.State(eventauth.Key{Type: "m.room.power_levels"}); ok {
					pl["room_id"] = "!somewhereelse:example.org"
				}
				return foreign
			},
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.message", "$new:example.org", "@alice:example.org",
					map[string]any{"body": "hi"}), state)
			},
			wantErr: "2.5",
		},
		{
			name: "an auth event that was rejected on receipt", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := authorise(rules, mkEvent("m.room.message", "$new:example.org", "@alice:example.org",
					map[string]any{"body": "hi"}), state)
				state.Reject("$pl:example.org")
				return event
			},
			wantErr: "2.3",
		},
		{
			name: "an auth event that cannot be resolved", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return mkEvent("m.room.message", "$new:example.org", "@alice:example.org",
					map[string]any{"body": "hi"},
					map[string]any{"auth_events": []any{"$create:example.org", "$pl:example.org", "$unknown:example.org"}})
			},
			wantErr: "2.2",
		},
	})
}

// TestAuthRulesFederateFalse covers rule 3.
func TestAuthRulesFederateFalse(t *testing.T) {
	t.Parallel()
	runAuthCases(t, []authCase{
		{
			name: "a foreign sender in a non-federated room", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				create := mkEvent("m.room.create", "$create:example.org", "@alice:example.org",
					map[string]any{"m.federate": false}, stateKey(""))
				state.Add(create)
				state.Add(mkEvent("m.room.member", "$memberforeign:example.org", "@dave:evil.example",
					map[string]any{"membership": "join"}, stateKey("@dave:evil.example")))
				return authorise(rules, mkEvent("m.room.message", "$new:example.org", "@dave:evil.example",
					map[string]any{"body": "hi"}), state)
			},
			wantErr: "3",
		},
	})
}

// TestAuthRulesPowerLevelRequirements covers rules 5 through 8.
func TestAuthRulesPowerLevelRequirements(t *testing.T) {
	t.Parallel()
	runAuthCases(t, []authCase{
		{
			name: "a non-member sends a message", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.message", "$new:example.org", "@carol:example.org",
					map[string]any{"body": "hi"}), state)
			},
			wantErr: "5",
		},
		{
			name: "a state event without the state default level", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.topic", "$new:example.org", "@bob:example.org",
					map[string]any{"topic": "hi"}, stateKey("")), state)
			},
			wantErr: "7",
		},
		{
			name: "a member event naming another user is a kick, judged by rule 4", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.member", "$new:example.org", "@alice:example.org",
					map[string]any{"membership": "leave"}, stateKey("@carol:example.org")), state)
			},
			// Rule 4 decides membership events before rule 8 looks at state keys, so
			// this is a kick and alice is allowed it.
			wantErr: "",
		},
		{
			name: "a custom state event named for another user", version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("com.example.thing", "$new:example.org", "@alice:example.org",
					map[string]any{}, stateKey("@bob:example.org")), state)
			},
			wantErr: "8",
		},
		{
			name: "an event type raised above the sender", version: "11",
			pl: map[string]any{"users": map[string]any{"@alice:example.org": 100}, "events": map[string]any{"m.room.message": 50}},
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.message", "$new:example.org", "@bob:example.org",
					map[string]any{"body": "hi"}), state)
			},
			wantErr: "7",
		},
		{
			name: "a third party invite event without invite power", version: "11",
			pl: map[string]any{"users": map[string]any{"@alice:example.org": 100}, "invite": 50},
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.third_party_invite", "$new:example.org", "@bob:example.org",
					map[string]any{"signed": map[string]any{}}, stateKey("token")), state)
			},
			wantErr: "6",
		},
	})
}

// TestAuthRulesAliases covers the historic m.room.aliases exemption and its removal
// in room version 6.
func TestAuthRulesAliases(t *testing.T) {
	t.Parallel()
	aliases := func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
		return authorise(rules, mkEvent("m.room.aliases", "$new:example.org", "@carol:example.org",
			map[string]any{"aliases": []any{"#room:example.org"}}, stateKey("example.org")), state)
	}
	runAuthCases(t, []authCase{
		// carol is not a member of the room, yet the aliases event is allowed: the
		// exemption exists so a server can maintain its own alias list.
		{name: "aliases in version 5 are exempt from membership", version: "5", event: aliases},
		{
			name: "aliases in version 5 for another server", version: "5",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.aliases", "$new:example.org", "@carol:example.org",
					map[string]any{"aliases": []any{}}, stateKey("other.example")), state)
			},
			wantErr: "aliases.2",
		},
		{
			name: "aliases in version 6 are ordinary state events", version: "6",
			event:   aliases,
			wantErr: "5",
		},
	})
}

// TestAuthRulesPowerLevelChanges covers rule 9, including the room version 10
// integer requirement.
func TestAuthRulesPowerLevelChanges(t *testing.T) {
	t.Parallel()
	plEvent := func(content map[string]any) func(roomversion.Rules, *eventauth.MemoryState) map[string]any {
		return func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
			return authorise(rules, mkEvent("m.room.power_levels", "$new:example.org", "@alice:example.org",
				content, stateKey("")), state)
		}
	}
	runAuthCases(t, []authCase{
		{
			name: "someone who is not the creator cannot bootstrap power levels", version: "11",
			snapshot: func(rules roomversion.Rules, base *eventauth.MemoryState) *eventauth.MemoryState {
				// A room whose state has no m.room.power_levels event yet.
				fresh := eventauth.NewMemoryState()
				for _, key := range []eventauth.Key{
					{Type: "m.room.create"}, {Type: "m.room.member", StateKey: "@bob:example.org"},
				} {
					if ev, ok := base.State(key); ok {
						fresh.Add(ev)
					}
				}
				return fresh
			},
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return authorise(rules, mkEvent("m.room.power_levels", "$newpl:example.org", "@bob:example.org",
					map[string]any{"users": map[string]any{"@bob:example.org": 100}}, stateKey("")), state)
			},
			wantErr: "7",
		},
		{
			name: "grant a level below your own", version: "11",
			event: plEvent(map[string]any{
				"users": map[string]any{"@alice:example.org": 100, "@bob:example.org": 50},
			}),
		},
		{
			name: "grant a level above your own", version: "11",
			event: plEvent(map[string]any{
				"users": map[string]any{"@alice:example.org": 100, "@bob:example.org": 150},
			}),
			wantErr: "9.9.1",
		},
		{
			name: "lower someone who is not below you", version: "11",
			pl:      map[string]any{"users": map[string]any{"@alice:example.org": 100, "@bob:example.org": 100}},
			event:   plEvent(map[string]any{"users": map[string]any{"@alice:example.org": 100, "@bob:example.org": 10}}),
			wantErr: "9.8.1",
		},
		{
			name: "raise state_default above your level", version: "11",
			pl:      map[string]any{"users": map[string]any{"@alice:example.org": 100, "@bob:example.org": 10}},
			event:   plEvent(map[string]any{"users": map[string]any{"@alice:example.org": 100}, "state_default": 200}),
			wantErr: "9.5.2",
		},
		{
			name: "remove a level above your own", version: "11",
			pl:      map[string]any{"users": map[string]any{"@alice:example.org": 100}, "redact": 150},
			event:   plEvent(map[string]any{"users": map[string]any{"@alice:example.org": 100}}),
			wantErr: "9.5.1",
		},
		{
			name: "raise an event type level above your own", version: "11",
			event: plEvent(map[string]any{
				"users":  map[string]any{"@alice:example.org": 100},
				"events": map[string]any{"m.room.message": 200},
			}),
			wantErr: "9.7.1",
		},
		{
			name: "non-integer power level in version 10", version: "10",
			event:   plEvent(map[string]any{"users": map[string]any{"@alice:example.org": 100}, "ban": 1.5}),
			wantErr: "9.1",
		},
		{
			name: "non-integer users entry in version 10", version: "10",
			event:   plEvent(map[string]any{"users": map[string]any{"@bob:example.org": 12.5}}),
			wantErr: "9.1",
		},
		{
			// Room versions before 10 have no integer requirement in the rules, so
			// the event is not rejected by rule 9. Recorded here so the difference is
			// a decision, not an oversight.
			name: "non-integer power level in version 9", version: "9",
			event: plEvent(map[string]any{"users": map[string]any{"@alice:example.org": 100}, "ban": 1.5}),
		},
		{
			name: "the first power levels event in a room is unconditional", version: "11",
			snapshot: func(rules roomversion.Rules, base *eventauth.MemoryState) *eventauth.MemoryState {
				// Room state with no m.room.power_levels event yet: rule 9.4 lets the
				// first one say whatever it likes, because someone has to bootstrap
				// power levels in a room.
				fresh := eventauth.NewMemoryState()
				for _, key := range []eventauth.Key{{Type: "m.room.create"}, {Type: "m.room.member", StateKey: "@alice:example.org"}} {
					if ev, ok := base.State(key); ok {
						fresh.Add(ev)
					}
				}
				return fresh
			},
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := mkEvent("m.room.power_levels", "$newpl:example.org", "@alice:example.org",
					map[string]any{"users": map[string]any{"@alice:example.org": 100, "@bob:example.org": 100}, "state_default": 100},
					stateKey(""))
				return authorise(rules, event, state)
			},
		},
	})
}

// TestSelectAuthEvents pins the auth events selection algorithm.
func TestSelectAuthEvents(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("11")
	state := baseState(t, "11", "invite", defaultPL())

	message := mkEvent("m.room.message", "$msg:example.org", "@alice:example.org", map[string]any{"body": "hi"})
	got := eventauth.SelectEventIDs(rules, message, state)
	want := []string{"$create:example.org", "$memberalice:example.org", "$pl:example.org"}
	if !sameStrings(got, want) {
		t.Errorf("message auth events = %v, want %v", got, want)
	}

	invite := mkEvent("m.room.member", "$inv:example.org", "@alice:example.org",
		map[string]any{"membership": "invite"}, stateKey("@carol:example.org"))
	got = eventauth.SelectEventIDs(rules, invite, state)
	want = []string{"$create:example.org", "$joinrules:example.org", "$memberalice:example.org", "$pl:example.org"}
	if !sameStrings(got, want) {
		t.Errorf("invite auth events = %v, want %v (no member event exists for the target yet)", got, want)
	}

	withThirdParty := mkEvent("m.room.member", "$inv2:example.org", "@alice:example.org",
		map[string]any{"membership": "invite", "third_party_invite": map[string]any{
			"signed": map[string]any{"mxid": "@carol:example.org", "token": "tok"}},
		}, stateKey("@carol:example.org"))
	state.Add(mkEvent("m.room.third_party_invite", "$tpi:example.org", "@alice:example.org",
		map[string]any{"public_key": "key"}, stateKey("tok")))
	got = eventauth.SelectEventIDs(rules, withThirdParty, state)
	want = []string{"$create:example.org", "$joinrules:example.org", "$memberalice:example.org", "$pl:example.org", "$tpi:example.org"}
	if !sameStrings(got, want) {
		t.Errorf("third-party invite auth events = %v, want %v", got, want)
	}

	// A restricted join also selects the authorising user's member event.
	restricted := roomversion.MustGet("10")
	state10 := baseState(t, "10", "restricted", defaultPL())
	join := mkEvent("m.room.member", "$join:example.org", "@carol:example.org",
		map[string]any{"membership": "join", "join_authorised_via_users_server": "@alice:example.org"},
		stateKey("@carol:example.org"))
	got = eventauth.SelectEventIDs(restricted, join, state10)
	want = []string{"$create:example.org", "$joinrules:example.org", "$memberalice:example.org", "$pl:example.org"}
	if !sameStrings(got, want) {
		t.Errorf("restricted join auth events = %v, want %v", got, want)
	}

	// The create event itself selects nothing.
	create := mkEvent("m.room.create", "$new:example.org", "@alice:example.org", map[string]any{}, stateKey(""))
	if got := eventauth.SelectEventIDs(rules, create, state); len(got) != 0 {
		t.Errorf("a create event should not be built from auth events, got %v", got)
	}
}

// TestRulesetForRoomVersionOne records that room version 1 is refused rather than
// authorised with the wrong table.
func TestRulesetForRoomVersionOne(t *testing.T) {
	t.Parallel()
	state := baseState(t, "1", "public", defaultPL())
	event := mkEvent("m.room.message", "$msg:example.org", "@alice:example.org", map[string]any{"body": "hi"})
	err := eventauth.Check(roomversion.MustGet("1"), event, state, eventauth.Opts{})
	if !errors.Is(err, eventauth.ErrRulesetUnsupported) {
		t.Errorf("room version 1 authorisation = %v, want ErrRulesetUnsupported", err)
	}
}

// TestMayJoinRestricted covers the restricted-room allow list, which is applied to
// locally created joins and deliberately not inside the federation auth rules.
func TestMayJoinRestricted(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("10")
	state := baseState(t, "10", "restricted", defaultPL())
	state.Add(mkEvent("m.room.join_rules", "$joinrules:example.org", "@alice:example.org",
		map[string]any{"join_rule": "restricted", "allow": []any{
			map[string]any{"type": "m.room.member", "room_id": "!gateway:example.org"},
		}}, stateKey("")))

	join := mkEvent("m.room.member", "$join:example.org", "@carol:example.org",
		map[string]any{"membership": "join", "join_authorised_via_users_server": "@alice:example.org"},
		stateKey("@carol:example.org"))

	if err := eventauth.MayJoinRestricted(rules, join, state, eventauth.Opts{}); err == nil {
		t.Errorf("an allow list needs a membership lookup; refusing without one is the safe behaviour")
	}
	joined := eventauth.Opts{JoinedInRoom: func(roomID, userID string) (bool, error) {
		return roomID == "!gateway:example.org" && userID == "@carol:example.org", nil
	}}
	if err := eventauth.MayJoinRestricted(rules, join, state, joined); err != nil {
		t.Errorf("allow list should accept a member of the gateway room: %v", err)
	}
	notJoined := eventauth.Opts{JoinedInRoom: func(string, string) (bool, error) { return false, nil }}
	if err := eventauth.MayJoinRestricted(rules, join, state, notJoined); err == nil {
		t.Errorf("a user in no allowed room must not be able to join a restricted room")
	}

	public := baseState(t, "10", "public", defaultPL())
	if err := eventauth.MayJoinRestricted(rules, join, public, eventauth.Opts{}); err != nil {
		t.Errorf("the allow list does not apply to a public room: %v", err)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if !set[s] {
			return false
		}
	}
	return true
}
