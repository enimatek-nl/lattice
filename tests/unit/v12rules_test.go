package unit

import (
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Room version 12 changed two things about who owns a room, and both of them are authorisation
// rules, which is where the cost of getting them wrong actually lands.
//
// MSC4291 made the room identifier the hash of the create event, so the create event can no longer
// carry an identifier — it is the one event in the room that arrives nameless, and rule 1.2 became
// "if the event has a room_id, reject" instead of "the room ID's domain must be the sender's". Rule
// 2 of that page then makes every other event's room identifier *checkable*: it has to be the create
// event's identifier with `!` for `$`.
//
// MSC4289 made the room's creators — the sender of the create event *plus* any users named in
// `content.additional_creators` — hold a power level the room cannot lower, and refused a power
// levels event that names them at all. A server that reads the level off the power levels event, as
// every version up to 11 correctly does, hands a demoted creator their room's absence of power: the
// owner of the room can no longer change anything in it, and nothing in the room reports it.
//
// One fixture note, because it matters for reading these tests: the state events carry an
// `event_id`, which real version 12 events do not. Nothing here depends on that field except where a
// room identifier is derived, and there it is derived from the bytes, not from the label.

// v12CreateEvent builds the create event a version 12 room is made of: no prev_events, and no
// room_id, because the identifier is derived from these bytes and an event cannot contain the hash
// of itself.
func v12CreateEvent(content map[string]any) map[string]any {
	event := mkEvent("m.room.create", "$new:example.org", "@alice:example.org", content, stateKey(""))
	event["prev_events"] = []any{}
	delete(event, "room_id")
	return event
}

// v12Room is a version 12 room and, unlike every other fixture in this package, the room it returns
// does not get to choose its own name: the identifier comes out of the create event, because that is
// the rule a version 12 event is now checked against. Building the state any other way would produce
// a room this server refuses to authorise anything in, and the tests would be about the fixture.
func v12Room(t *testing.T, plContent map[string]any, additionalCreators []string, members ...string) (*eventauth.MemoryState, string) {
	t.Helper()
	rules := roomversion.MustGet("12")

	createContent := map[string]any{"room_version": "12"}
	if len(additionalCreators) > 0 {
		list := make([]any, 0, len(additionalCreators))
		for _, user := range additionalCreators {
			list = append(list, user)
		}
		createContent[eventauth.AdditionalCreatorsKey] = list
	}
	create := v12CreateEvent(createContent)

	roomID, err := events.RoomIDFromCreate(rules, create)
	if err != nil {
		t.Fatalf("the room's own create event could not name the room: %v", err)
	}

	held := []map[string]any{create}
	for _, user := range members {
		held = append(held, inRoom(roomID, mkEvent("m.room.member", "$member-"+user+":example.org", user,
			map[string]any{"membership": "join"}, stateKey(user))))
	}
	held = append(held,
		inRoom(roomID, mkEvent("m.room.join_rules", "$joinrules:example.org", "@alice:example.org",
			map[string]any{"join_rule": "public"}, stateKey(""))),
		inRoom(roomID, mkEvent("m.room.power_levels", "$pl:example.org", "@alice:example.org", plContent, stateKey(""))),
	)
	return eventauth.NewMemoryState(held...), roomID
}

// inRoom restamps an event with the room it belongs to. In a version 12 room that identifier is not
// an input to the fixture, it is a fact about the create event.
func inRoom(roomID string, event map[string]any) map[string]any {
	event["room_id"] = roomID
	return event
}

// nameEventBy is how these tests ask "may this user rename the room": an ordinary state event, which
// needs state_default (50), so the answer states the power level the server believes the user holds.
func nameEventBy(user, roomID string) func(roomversion.Rules, *eventauth.MemoryState) map[string]any {
	return func(rules roomversion.Rules, state *eventauth.MemoryState) map[string]any {
		return authorise(rules, inRoom(roomID, mkEvent("m.room.name", "$name:example.org", user,
			map[string]any{"name": "renamed"}, stateKey(""))), state)
	}
}

func lowPowerPL(users map[string]any) map[string]any {
	pl := defaultPL()
	pl["users"] = users
	return pl
}

func powerLevelsEvent(t *testing.T, users map[string]any, roomID string) map[string]any {
	t.Helper()
	content := map[string]any{}
	for key, value := range defaultPL() {
		content[key] = value
	}
	content["users"] = users
	return inRoom(roomID, mkEvent("m.room.power_levels", "$newpl:example.org", "@alice:example.org", content, stateKey("")))
}

// TestMSC4291RoomIDIsTheCreateEventHashed covers the derivation itself: what a version 12 room
// identifier is, and the two ways a create event can fail to produce one.
func TestMSC4291RoomIDIsTheCreateEventHashed(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("12")
	v11 := roomversion.MustGet("11")

	t.Run("the identifier is derived, and is the event's own with a different sigil", func(t *testing.T) {
		create := v12CreateEvent(map[string]any{"room_version": "12"})
		roomID, err := events.RoomIDFromCreate(rules, create)
		if err != nil {
			t.Fatalf("deriving the room ID: %v", err)
		}
		eventID, err := events.EventID(rules, create)
		if err != nil {
			t.Fatalf("deriving the event ID of the same event: %v", err)
		}
		if !strings.HasPrefix(roomID, "!") || strings.HasPrefix(roomID, "!$") {
			t.Errorf("room ID %q does not read as a room identifier", roomID)
		}
		if strings.HasPrefix(eventID, "$") && roomID[1:] != eventID[1:] {
			t.Errorf("room ID %q and event ID %q are not the same identifier under a different sigil", roomID, eventID)
		}
	})

	t.Run("the same bytes name the same room, twice over", func(t *testing.T) {
		first, err := events.RoomIDFromCreate(rules, v12CreateEvent(map[string]any{"room_version": "12"}))
		if err != nil {
			t.Fatal(err)
		}
		second, err := events.RoomIDFromCreate(rules, v12CreateEvent(map[string]any{"room_version": "12"}))
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Errorf("identical create events named two rooms: %q and %q", first, second)
		}
	})

	t.Run("different bytes name a different room", func(t *testing.T) {
		alice := mustRoomID(t, rules, map[string]any{"room_version": "12", "creator": "@alice:example.org"})
		bob := mustRoomID(t, rules, map[string]any{"room_version": "12", "creator": "@bob:example.org"})
		if alice == bob {
			t.Fatalf("two different create events named the same room %q", alice)
		}
	})

	t.Run("a create event that carries a room identifier names nothing", func(t *testing.T) {
		// Not a missing field to be filled in: these bytes are not the bytes the identifier is a
		// hash of, because an event cannot contain the hash of itself. This is the difference
		// between an incomplete event and a claim about a room these bytes did not create.
		create := v12CreateEvent(map[string]any{"room_version": "12"})
		create["room_id"] = "!whatever:example.org"
		if _, err := events.RoomIDFromCreate(rules, create); err == nil {
			t.Fatal("a create event carrying a room_id was accepted as the source of a room ID")
		}
	})

	t.Run("only the create event names a room", func(t *testing.T) {
		powerLevels := inRoom("!x:example.org", mkEvent("m.room.power_levels", "$pl:example.org", "@alice:example.org",
			defaultPL(), stateKey("")))
		if _, err := events.RoomIDFromCreate(rules, powerLevels); err == nil {
			t.Fatal("a power levels event was accepted as the source of a room ID")
		}
	})

	t.Run("room versions before 12 are named by their server, not by an event", func(t *testing.T) {
		create := mkEvent("m.room.create", "$create:example.org", "@alice:example.org",
			map[string]any{"room_version": "11"}, stateKey(""))
		if _, err := events.RoomIDFromCreate(v11, create); err == nil {
			t.Fatal("room version 11 was allowed to derive its room identifier from the create event")
		}
	})
}

func mustRoomID(t *testing.T, rules roomversion.Rules, content map[string]any) string {
	t.Helper()
	roomID, err := events.RoomIDFromCreate(rules, v12CreateEvent(content))
	if err != nil {
		t.Fatalf("deriving a room ID: %v", err)
	}
	return roomID
}

// TestMSC4291RoomIDIsCheckedAgainstTheCreateEvent covers rule 2: in a version 12 room, an event's
// room identifier is not a label the sender carries, it is a fact about the room's create event.
func TestMSC4291RoomIDIsCheckedAgainstTheCreateEvent(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("12")
	state, roomID := v12Room(t, defaultPL(), nil, "@alice:example.org", "@bob:example.org")

	t.Run("an event in the room this create event names is allowed", func(t *testing.T) {
		event := nameEventBy("@alice:example.org", roomID)(rules, state)
		if err := eventauth.Check(rules, event, state, eventauth.Opts{}); err != nil {
			t.Fatalf("an event carrying the room's own identifier was refused: %v", err)
		}
	})

	t.Run("an event carrying another room's identifier is refused", func(t *testing.T) {
		// The point of the rule: a version 12 room identifier is checkable rather than agreed, so a
		// server cannot put an event into a room by asserting an identifier for it.
		event := nameEventBy("@alice:example.org", "!a-room-that-is-not-this-one")(rules, state)
		err := eventauth.Check(rules, event, state, eventauth.Opts{})
		if err == nil {
			t.Fatal("an event naming a room the create event does not hash to was allowed")
		}
		if failure, ok := eventauth.AsFailure(err); !ok || failure.Rule != "2" {
			t.Fatalf("refused by %v, wanted rule 2", err)
		}
	})

	t.Run("an event in a room whose create event was edited is refused", func(t *testing.T) {
		// The other direction: the bytes of the create event decide the room's name, so a state
		// whose create event has been altered no longer contains the room its events name.
		create, _ := state.State(eventauth.Key{Type: "m.room.create"})
		tampered := map[string]any{}
		for key, value := range create {
			tampered[key] = value
		}
		tampered["sender"] = "@mallory:example.org"
		tamperedState := eventauth.NewMemoryState(append(
			[]map[string]any{tampered},
			otherStateEvents(t, state, roomID)...,
		)...)
		event := nameEventBy("@alice:example.org", roomID)(rules, tamperedState)
		err := eventauth.Check(rules, event, tamperedState, eventauth.Opts{})
		if err == nil {
			t.Fatal("an event survived a rewritten create event")
		}
		if failure, ok := eventauth.AsFailure(err); !ok || failure.Rule != "2" {
			t.Fatalf("refused by %v, wanted rule 2", err)
		}
	})

	t.Run("an event authorised against an auth chain is still authorised without the create event", func(t *testing.T) {
		// The regression this task found by running into a real homeserver. Room version 12 forbids an
		// event from citing the create event as an auth event — the room identifier names it — so an
		// event authorised against its own auth chain is authorised against a set that has no create
		// event in it. A rule 2 that demanded one there refused every event in every valid version 12
		// room, and nothing short of a federation handshake exercises that path: run 20261108v12after.
		var held []map[string]any
		held = append(held, otherStateEvents(t, state, roomID)...)
		withoutCreate := eventauth.NewMemoryState(held...)
		event := nameEventBy("@alice:example.org", roomID)(rules, withoutCreate)
		err := eventauth.Check(rules, event, withoutCreate, eventauth.Opts{})
		if failure, ok := eventauth.AsFailure(err); ok && failure.Rule == "2" {
			t.Fatalf("rule 2 refused an event for lacking a create event it is forbidden to cite: %s", failure.Reason)
		}
	})
}

// otherStateEvents returns the room's state apart from its create event, each event stamped
// for the room it belongs to.
func otherStateEvents(t *testing.T, state *eventauth.MemoryState, roomID string) []map[string]any {
	t.Helper()
	var held []map[string]any
	for _, key := range []eventauth.Key{
		{Type: "m.room.member", StateKey: "@alice:example.org"},
		{Type: "m.room.member", StateKey: "@bob:example.org"},
		{Type: "m.room.join_rules"},
		{Type: "m.room.power_levels"},
	} {
		if ev, ok := state.State(key); ok {
			held = append(held, inRoom(roomID, ev))
		}
	}
	return held
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
		state, roomID := v12Room(t, lowered, nil, "@alice:example.org", "@bob:example.org")
		event := nameEventBy("@alice:example.org", roomID)(rules, state)
		if err := eventauth.Check(rules, event, state, eventauth.Opts{}); err != nil {
			t.Fatalf("the room's creator was refused an ordinary state event: %v", err)
		}
	})

	t.Run("the same power levels event in version 11 does lower them", func(t *testing.T) {
		rules := roomversion.MustGet("11")
		state := baseState(t, "11", "public", lowered)
		event := nameEventBy("@alice:example.org", authRoom)(rules, state)
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
		state, roomID := v12Room(t, lowPowerPL(map[string]any{"@alice:example.org": 100}),
			[]string{"@carol:example.org"}, "@alice:example.org", "@carol:example.org")
		event := nameEventBy("@carol:example.org", roomID)(rules, state)
		if err := eventauth.Check(rules, event, state, eventauth.Opts{}); err != nil {
			t.Fatalf("an additional creator named in the create event was refused: %v", err)
		}
	})

	t.Run("infinite power does not extend to everybody", func(t *testing.T) {
		// The guard against the obvious over-correction: this is a rule about creators, and a
		// room where a plain member can rename it at power level 0 is a broken room.
		rules := roomversion.MustGet("12")
		state, roomID := v12Room(t, lowPowerPL(map[string]any{"@alice:example.org": 100}),
			[]string{"@carol:example.org"}, "@alice:example.org", "@bob:example.org")
		event := nameEventBy("@bob:example.org", roomID)(rules, state)
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

	t.Run("naming the creator is refused, at any level", func(t *testing.T) {
		rules := roomversion.MustGet("12")
		state, roomID := v12Room(t, defaultPL(), nil, "@alice:example.org")
		for _, level := range []int{0, 5, 100} {
			event := authorise(rules, powerLevelsEvent(t, map[string]any{
				"@alice:example.org": level, "@bob:example.org": 50,
			}, roomID), state)
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
		state, roomID := v12Room(t, defaultPL(), []string{"@carol:example.org"},
			"@alice:example.org", "@carol:example.org")
		event := authorise(rules, powerLevelsEvent(t, map[string]any{
			"@alice:example.org": 100, "@carol:example.org": 40,
		}, roomID), state)
		err := eventauth.Check(rules, event, state, eventauth.Opts{})
		if failure, ok := eventauth.AsFailure(err); err == nil || !ok || failure.Rule != "10.4" {
			t.Fatalf("refused by %v, wanted rule 10.4", err)
		}
	})

	t.Run("the first power levels event of a room is not exempt", func(t *testing.T) {
		// Room version 11 lets the first power levels event say whatever it likes, since somebody
		// has to bootstrap power levels. The specification orders the creators clause ahead of that
		// clause, so a version 12 room cannot be bootstrapped by naming its creator. State
		// Resolution 2.1 starts its iterative checks from an empty state map, which is the same
		// situation one step inside the algorithm.
		rules := roomversion.MustGet("12")
		_, roomID := v12Room(t, defaultPL(), nil, "@alice:example.org")
		state, freshRoomID := v12Room(t, defaultPL(), nil, "@alice:example.org")
		if freshRoomID != roomID {
			t.Fatalf("the same room was named two identifiers: %q and %q", roomID, freshRoomID)
		}
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
		nopl := eventauth.NewMemoryState(held...)
		event := authorise(rules, powerLevelsEvent(t, map[string]any{"@alice:example.org": 100}, roomID), nopl)
		err := eventauth.Check(rules, event, nopl, eventauth.Opts{})
		if failure, ok := eventauth.AsFailure(err); err == nil || !ok || failure.Rule != "10.4" {
			t.Fatalf("refused by %v, wanted rule 10.4 before the first-event clause", err)
		}
	})

	t.Run("a power levels event that leaves the creators alone is allowed", func(t *testing.T) {
		rules := roomversion.MustGet("12")
		state, roomID := v12Room(t, defaultPL(), nil, "@alice:example.org", "@bob:example.org")
		event := authorise(rules, powerLevelsEvent(t, map[string]any{"@bob:example.org": 50}, roomID), state)
		if err := eventauth.Check(rules, event, state, eventauth.Opts{}); err != nil {
			t.Fatalf("a power levels event naming only a plain member was refused: %v", err)
		}
	})

	t.Run("version 11 lets a creator be listed", func(t *testing.T) {
		// Regression guard the other way: the clause is a version 12 change, and a room in every
		// earlier version states its creator's level in this event.
		rules := roomversion.MustGet("11")
		state := baseState(t, "11", "public", defaultPL())
		event := authorise(rules, powerLevelsEvent(t, map[string]any{
			"@alice:example.org": 100, "@bob:example.org": 50,
		}, authRoom), state)
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

	for _, version := range []string{"3", "10", "11"} {
		rules := roomversion.MustGet(version)
		state := baseState(t, version, "public", defaultPL())
		keys := eventauth.SelectKeys(rules, mkEvent("m.room.name", "$n:example.org", "@bob:example.org",
			map[string]any{"name": "x"}, stateKey("")), state)
		if !hasKey(keys, eventauth.Key{Type: "m.room.create"}) {
			t.Errorf("room version %s: the create event stopped being selected, and only version 12 dropped it", version)
		}
	}

	rules := roomversion.MustGet("12")
	state, roomID := v12Room(t, defaultPL(), nil, "@alice:example.org", "@bob:example.org")
	keys := eventauth.SelectKeys(rules, inRoom(roomID, mkEvent("m.room.name", "$n:example.org", "@bob:example.org",
		map[string]any{"name": "x"}, stateKey(""))), state)
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
