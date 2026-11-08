package unit

import (
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Room version 12 changed two things about who owns a room, and both of them are authorisation
// rules, which is where the cost of getting them wrong actually lands.
//
// MSC4291 made the room identifier the hash of the create event, so the create event can no longer
// carry an identifier — it is the one event in the room that arrives nameless, and rule 1.2 became
// "if the event has a room_id, reject" instead of "the room ID's domain must be the sender's".
//
// MSC4289 made the room's creators — the sender of the create event *plus* any users named in
// `content.additional_creators` — hold a power level the room cannot lower, and refused a power
// levels event that names them at all. A server that reads the level off the power levels event,
// as every version up to 11 correctly does, hands a demoted creator their room's absence of power:
// the owner of the room can no longer change anything in it, and nothing in the room reports it.
//
// The fixtures here give state events an `event_id`, which real version 12 events do not carry —
// their identifiers are computed. That is MSC4291's remaining half (T-4019), and these tests are
// about the rules, not about the naming.

// v12CreateEvent builds the create event a version 12 room is made of: no prev_events, and no
// room_id, because the identifier is derived from these bytes.
func v12CreateEvent(content map[string]any) map[string]any {
	event := mkEvent("m.room.create", "$new:example.org", "@alice:example.org", content, stateKey(""))
	event["prev_events"] = []any{}
	delete(event, "room_id")
	return event
}

// roomStateForV12 is baseState for a version 12 room: the create event carries no room_id and may
// name additional creators, and the users listed are joined. The power levels event is whatever the
// caller hands over, including one that wrongly names a creator — which is the state this server
// has to be robust against, since it can arrive from a server that did not check it.
func roomStateForV12(t *testing.T, plContent map[string]any, additionalCreators []string, members ...string) *eventauth.MemoryState {
	t.Helper()
	createContent := map[string]any{"room_version": "12"}
	if len(additionalCreators) > 0 {
		list := make([]any, 0, len(additionalCreators))
		for _, user := range additionalCreators {
			list = append(list, user)
		}
		createContent[eventauth.AdditionalCreatorsKey] = list
	}
	create := mkEvent("m.room.create", "$create:example.org", "@alice:example.org", createContent, stateKey(""))
	delete(create, "room_id")

	events := []map[string]any{create}
	for _, user := range members {
		events = append(events, mkEvent("m.room.member", "$member-"+user+":example.org", user,
			map[string]any{"membership": "join"}, stateKey(user)))
	}
	events = append(events,
		mkEvent("m.room.join_rules", "$joinrules:example.org", "@alice:example.org",
			map[string]any{"join_rule": "public"}, stateKey("")),
		mkEvent("m.room.power_levels", "$pl:example.org", "@alice:example.org", plContent, stateKey("")),
	)
	return eventauth.NewMemoryState(events...)
}

// sendersPLIsHowYouLearnTheRule: an ordinary state event needs state_default, 50, so whether the
// sender may write one says exactly what power level the server believes they hold.
func stateEventBy(user string) func(roomversion.Rules, *eventauth.MemoryState) map[string]any {
	return func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
		return authorise(rules, mkEvent("m.room.name", "$name:example.org", user,
			map[string]any{"name": "renamed"}, stateKey("")), state)
	}
}

func lowPowerPL(users map[string]any) map[string]any {
	pl := defaultPL()
	pl["users"] = users
	return pl
}

func TestMSC4291CreateEventCarriesNoRoomID(t *testing.T) {
	t.Parallel()
	runAuthCases(t, []authCase{
		{
			name:    "a version 12 create event arrives without a room identifier",
			version: "12",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return v12CreateEvent(map[string]any{"room_version": "12"})
			},
		},
		{
			name:    "a version 12 create event that brings its own room identifier names a room these bytes do not make",
			version: "12",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := v12CreateEvent(map[string]any{"room_version": "12"})
				event["room_id"] = authRoom
				return event
			},
			wantErr: "1.2",
		},
		{
			name:    "the same create event in version 11 still carries its room identifier",
			version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := mkEvent("m.room.create", "$new:example.org", "@alice:example.org",
					map[string]any{"room_version": "11"}, stateKey(""))
				event["prev_events"] = []any{}
				return event
			},
		},
		{
			name:    "additional creators that are user IDs are allowed",
			version: "12",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return v12CreateEvent(map[string]any{
					"room_version":                  "12",
					eventauth.AdditionalCreatorsKey: []any{"@carol:example.org"},
				})
			},
		},
		{
			name:    "additional creators that is not an array",
			version: "12",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return v12CreateEvent(map[string]any{
					"room_version":                  "12",
					eventauth.AdditionalCreatorsKey: "@carol:example.org",
				})
			},
			wantErr: "1.4",
		},
		{
			name:    "additional creators holding something that is not a string",
			version: "12",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return v12CreateEvent(map[string]any{
					"room_version":                  "12",
					eventauth.AdditionalCreatorsKey: []any{7},
				})
			},
			wantErr: "1.4",
		},
		{
			// Each of these users holds infinite power in the room, so a value that fails the
			// user ID grammar is not a malformed optional field.
			name:    "additional creators holding a user ID the grammar does not accept",
			version: "12",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				return v12CreateEvent(map[string]any{
					"room_version":                  "12",
					eventauth.AdditionalCreatorsKey: []any{"carol:example.org"},
				})
			},
			wantErr: "1.4",
		},
		{
			name:    "version 11 does not know additional creators and is not made stricter by them",
			version: "11",
			event: func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
				event := mkEvent("m.room.create", "$new:example.org", "@alice:example.org",
					map[string]any{
						"room_version":                  "11",
						eventauth.AdditionalCreatorsKey: "not even an array",
					}, stateKey(""))
				event["prev_events"] = []any{}
				return event
			},
		},
	})
}

func TestMSC4289CreatorsHoldInfinitePower(t *testing.T) {
	t.Parallel()
	// The same room state, the same event, two room versions: the difference is the answer.
	lowered := lowPowerPL(map[string]any{"@alice:example.org": 5})

	t.Run("a creator listed at a low level is not lowered", func(t *testing.T) {
		rules := roomversion.MustGet("12")
		state := roomStateForV12(t, lowered, nil, "@alice:example.org", "@bob:example.org")
		event := stateEventBy("@alice:example.org")(rules, state)
		if err := eventauth.Check(rules, event, state, eventauth.Opts{}); err != nil {
			t.Fatalf("the room's creator was refused an ordinary state event: %v", err)
		}
	})

	t.Run("the same power levels event in version 11 does lower them", func(t *testing.T) {
		rules := roomversion.MustGet("11")
		state := baseState(t, "11", "public", lowered)
		event := stateEventBy("@alice:example.org")(rules, state)
		err := eventauth.Check(rules, event, state, eventauth.Opts{})
		if err == nil {
			t.Fatal("a version 11 creator listed at power level 5 was treated as holding infinite power")
		}
		if failure, ok := eventauth.AsFailure(err); !ok || failure.Rule != "7" {
			t.Fatalf("refused by %v, wanted rule 7 (the event needs more power than the sender holds)", err)
		}
	})

	t.Run("an additional creator holds it too", func(t *testing.T) {
		rules := roomversion.MustGet("12")
		state := roomStateForV12(t, lowPowerPL(map[string]any{"@alice:example.org": 100}),
			[]string{"@carol:example.org"}, "@alice:example.org", "@carol:example.org")
		event := stateEventBy("@carol:example.org")(rules, state)
		if err := eventauth.Check(rules, event, state, eventauth.Opts{}); err != nil {
			t.Fatalf("an additional creator named in the create event was refused: %v", err)
		}
	})

	t.Run("infinite power does not extend to everybody", func(t *testing.T) {
		// The guard against the obvious over-correction: this is a rule about creators, and a
		// room where a plain member can rename it at power level 0 is a broken room.
		rules := roomversion.MustGet("12")
		state := roomStateForV12(t, lowPowerPL(map[string]any{"@alice:example.org": 100}),
			[]string{"@carol:example.org"}, "@alice:example.org", "@bob:example.org")
		event := stateEventBy("@bob:example.org")(rules, state)
		err := eventauth.Check(rules, event, state, eventauth.Opts{})
		if err == nil {
			t.Fatal("a user who is nobody in this room was allowed to rename it")
		}
		if failure, ok := eventauth.AsFailure(err); !ok || failure.Rule != "7" {
			t.Fatalf("refused by %v, wanted rule 7", err)
		}
	})
}

func TestMSC4289PowerLevelsMayNotNameACreator(t *testing.T) {
	t.Parallel()
	plEvent := func(users map[string]any, extra ...map[string]any) map[string]any {
		content := map[string]any{}
		for key, value := range defaultPL() {
			content[key] = value
		}
		content["users"] = users
		event := mkEvent("m.room.power_levels", "$newpl:example.org", "@alice:example.org", content, stateKey(""))
		for _, m := range extra {
			for k, v := range m {
				event[k] = v
			}
		}
		return event
	}

	t.Run("naming the creator is refused, at any level", func(t *testing.T) {
		rules := roomversion.MustGet("12")
		state := roomStateForV12(t, defaultPL(), nil, "@alice:example.org")
		for _, level := range []int{0, 5, 100} {
			event := authorise(rules, plEvent(map[string]any{
				"@alice:example.org": level, "@bob:example.org": 50,
			}), state)
			err := eventauth.Check(rules, event, state, eventauth.Opts{})
			if err == nil {
				t.Fatalf("a power levels event naming the creator at %d was allowed", level)
			}
			if failure, ok := eventauth.AsFailure(err); !ok || failure.Rule != "10.4" {
				t.Fatalf("refused by %v, wanted rule 10.4", err)
			}
		}
	})

	t.Run("naming an additional creator is refused too", func(t *testing.T) {
		rules := roomversion.MustGet("12")
		state := roomStateForV12(t, defaultPL(), []string{"@carol:example.org"},
			"@alice:example.org", "@carol:example.org")
		event := authorise(rules, plEvent(map[string]any{
			"@alice:example.org": 100, "@carol:example.org": 40,
		}), state)
		err := eventauth.Check(rules, event, state, eventauth.Opts{})
		if failure, ok := eventauth.AsFailure(err); err == nil || !ok || failure.Rule != "10.4" {
			t.Fatalf("refused by %v, wanted rule 10.4", err)
		}
	})

	t.Run("the first power levels event of a room is not exempt", func(t *testing.T) {
		// Room version 11 lets the first power levels event say whatever it likes, since somebody
		// has to bootstrap power levels. The specification orders the creators clause ahead of
		// that clause, so a version 12 room cannot be bootstrapped by naming its creator.
		rules := roomversion.MustGet("12")
		state := roomStateForV12(t, defaultPL(), nil, "@alice:example.org")
		var held []map[string]any
		for _, key := range []eventauth.Key{
			{Type: "m.room.create"},
			{Type: "m.room.member", StateKey: "@alice:example.org"},
			{Type: "m.room.join_rules"},
		} {
			if ev, ok := state.State(key); ok {
				held = append(held, ev)
			}
		}
		fresh := eventauth.NewMemoryState(held...)
		event := authorise(rules, plEvent(map[string]any{"@alice:example.org": 100}), fresh)
		err := eventauth.Check(rules, event, fresh, eventauth.Opts{})
		if failure, ok := eventauth.AsFailure(err); err == nil || !ok || failure.Rule != "10.4" {
			t.Fatalf("refused by %v, wanted rule 10.4 before the first-event clause", err)
		}
	})

	t.Run("a power levels event that leaves the creators alone is allowed", func(t *testing.T) {
		rules := roomversion.MustGet("12")
		state := roomStateForV12(t, defaultPL(), nil, "@alice:example.org", "@bob:example.org")
		event := authorise(rules, plEvent(map[string]any{"@bob:example.org": 50}), state)
		if err := eventauth.Check(rules, event, state, eventauth.Opts{}); err != nil {
			t.Fatalf("a power levels event naming only a plain member was refused: %v", err)
		}
	})

	t.Run("version 11 lets a creator be listed", func(t *testing.T) {
		// Regression guard the other way: the clause is a version 12 change, and a room in every
		// earlier version states its creator's level in this event.
		rules := roomversion.MustGet("11")
		state := baseState(t, "11", "public", defaultPL())
		event := authorise(rules, plEvent(map[string]any{
			"@alice:example.org": 100, "@bob:example.org": 50,
		}), state)
		if err := eventauth.Check(rules, event, state, eventauth.Opts{}); err != nil {
			t.Fatalf("a version 11 power levels event naming its creator was refused: %v", err)
		}
	})
}

// TestMSC4291CreateEventIsNotAnAuthEvent covers the other half of MSC4291's auth change: the create
// event is no longer selected into `auth_events`, because the room ID that every event in a version
// 12 room carries is that event's own identifier, so citing it would be saying the same thing twice
// — and rule 3.2 refuses an event whose auth_events do not match the selection.
func TestMSC4291CreateEventIsNotAnAuthEvent(t *testing.T) {
	t.Parallel()
	name := mkEvent("m.room.name", "$n:example.org", "@bob:example.org",
		map[string]any{"name": "x"}, stateKey(""))

	for _, version := range []string{"3", "10", "11"} {
		rules := roomversion.MustGet(version)
		state := baseState(t, version, "public", defaultPL())
		keys := eventauth.SelectKeys(rules, name, state)
		if !hasKey(keys, eventauth.Key{Type: "m.room.create"}) {
			t.Errorf("room version %s: the create event stopped being selected, and only version 12 dropped it", version)
		}
	}

	rules := roomversion.MustGet("12")
	v12 := roomStateForV12(t, defaultPL(), nil, "@alice:example.org", "@bob:example.org")
	keys := eventauth.SelectKeys(rules, name, v12)
	if hasKey(keys, eventauth.Key{Type: "m.room.create"}) {
		t.Error("room version 12: the create event is still selected into auth_events")
	}
	if !hasKey(keys, eventauth.Key{Type: "m.room.power_levels"}) {
		t.Error("room version 12: dropping the create event also dropped the power levels event")
	}
	if !hasKey(keys, eventauth.Key{Type: "m.room.member", StateKey: "@bob:example.org"}) {
		t.Error("room version 12: the sender's membership stopped being selected")
	}
}

func hasKey(keys []eventauth.Key, want eventauth.Key) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}
