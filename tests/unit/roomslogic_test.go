package unit

import (
	"testing"

	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/rooms"
)

// TestEventTypeValidation covers the input check on a client-supplied event type. The
// types are keys in state, power levels and push rules, so an unusable type is not
// cosmetic.
func TestEventTypeValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		eventType string
		wantOK    bool
	}{
		{"m.room.message", true},
		{"m.room.power_levels", true},
		{"m.custom_thing", true},
		{"com.example.thing", true},
		{"dev.lattice.thing", true},
		// A hyphen is not part of a name the specification reserves.
		{"m.-leading-hyphen", false},
		{"com.example-thing.v2", true},
		{"", false},
		{"m.", false},
		{"m.Room.Message", false},
		{"not an event", false},
		// Undotted custom names are accepted. The dotted namespace is a convention, and the
		// risk it manages — two servers inventing the same name — belongs to whoever
		// invents it, not to the server. Refusing one answers 400 before the room sees
		// anything, which is how Complement's filter tests came to be red here: they send
		// types like `please_filter_me`, and the reference servers take them.
		{"nodots", true},
		{"please_filter_me", true},
		{"m.room.", false},
		{".room.message", false},
		{"m..room", false},
		{"_leading", false},
	}
	for _, tc := range cases {
		err := matrixid.EventTypeValid(tc.eventType)
		if tc.wantOK && err != nil {
			t.Errorf("event type %q should be accepted: %v", tc.eventType, err)
		}
		if !tc.wantOK && err == nil {
			t.Errorf("event type %q should be refused", tc.eventType)
		}
	}
}

// TestMergePowerLevelsKeepsTheCreator pins that a client's
// power_level_content_override cannot erase the room creator or drop a level the
// preset set, because the merge is per key at one level of nesting.
func TestMergePowerLevelsKeepsTheCreator(t *testing.T) {
	t.Parallel()
	base := map[string]any{
		"users":         map[string]any{"@creator:hs1": 100},
		"state_default": 50,
		"events":        map[string]any{"m.room.message": 0},
	}
	override := map[string]any{
		"users":         map[string]any{"@guest:hs1": 5},
		"state_default": 0,
	}
	merged := rooms.MergePowerLevels(base, override)

	if users, ok := merged["users"].(map[string]any); !ok {
		t.Fatalf("users was replaced instead of merged: %v", merged["users"])
	} else if users["@creator:hs1"] != 100 {
		t.Errorf("the override erased the creator: %v", users)
	}
	if events, ok := merged["events"].(map[string]any); !ok || events["m.room.message"] != 0 {
		t.Errorf("an unmentioned nested map disappeared: %v", merged["events"])
	}
	if merged["state_default"] != 0 {
		t.Errorf("a scalar override was not applied: %v", merged["state_default"])
	}
}
