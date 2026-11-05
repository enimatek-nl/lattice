package unit

import (
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/roomversion"
)

// TestPowerLevelsReadTheLegacyFormsInOlderVersions covers the two relaxations the pages for room
// versions 1 through 5 document, and which those versions' rooms depend on: an integer-valued power
// level may be written as a string, and a number may carry a fractional part and be truncated.
//
// This is not a compatibility nicety. A room whose power levels were written that way has moderators
// and a ban level; reading the values as absent would leave the server believing nobody has power in
// a room where several people do, which is a wrong answer about who controls a room rather than a
// detail of JSON parsing.
func TestPowerLevelsReadTheLegacyFormsInOlderVersions(t *testing.T) {
	t.Parallel()

	content := map[string]any{
		"ban":           "77",
		"state_default": " 040 ",
		"users": map[string]any{
			"@padded:example.org":     " 000100 ",
			"@signed:example.org":     "+25",
			"@fractional:example.org": 50.57,
			"@plain:example.org":      10,
		},
	}

	for _, version := range []string{"1", "2", "3", "4", "5"} {
		rules := roomversion.MustGet(version)
		levels, err := eventauth.LoadPowerLevels(rules, content)
		if err != nil {
			t.Fatalf("room version %s: %v", version, err)
		}
		if levels.Ban != 77 {
			t.Errorf("room version %s: the ban level read as %d, wanting 77 from the string \"77\"", version, levels.Ban)
		}
		if levels.StateDefault != 40 {
			t.Errorf("room version %s: state_default read as %d, wanting 40 from \" 040 \"", version, levels.StateDefault)
		}
		for userID, want := range map[string]int{
			"@padded:example.org":     100,
			"@signed:example.org":     25,
			"@fractional:example.org": 50, // truncated at the decimal point, as the page says
			"@plain:example.org":      10,
		} {
			if got := levels.User(userID); got != want {
				t.Errorf("room version %s: %s holds %d, wanting %d", version, userID, got, want)
			}
		}
	}

	// Room version 6 required canonical JSON, and a power level there is an integer or nothing. Room
	// versions 6 through 9 say nothing about the value, so an unreadable one leaves the default in
	// place; from version 10 on the event is invalid outright, which the loader reports.
	quiet, err := eventauth.LoadPowerLevels(roomversion.MustGet("7"), content)
	if err != nil {
		t.Fatalf("room version 7: %v", err)
	}
	if quiet.Ban == 77 || quiet.StateDefault == 40 {
		t.Errorf("room version 7 read the legacy forms: ban %d, state_default %d", quiet.Ban, quiet.StateDefault)
	}
	if got := quiet.User("@padded:example.org"); got != 0 {
		t.Errorf("room version 7 gave a power level of %d for a user named with a string", got)
	}
	if _, err := eventauth.LoadPowerLevels(roomversion.MustGet("11"), content); err == nil {
		t.Errorf("room version 11 accepted power levels that are not integers")
	}

	// A string with a decimal point is not an integer in any version, permissive ones included: the
	// page allows a bare base-10 integer, optionally signed and padded, and nothing else.
	decimalOnly := map[string]any{"users": map[string]any{"@decimal:example.org": "50.5"}}
	permissive, err := eventauth.LoadPowerLevels(roomversion.MustGet("2"), decimalOnly)
	if err != nil {
		t.Fatalf("room version 2: %v", err)
	}
	if got := permissive.User("@decimal:example.org"); got != 0 {
		t.Errorf("a power level written as \"50.5\" read as %d, wanting it to be no power level at all", got)
	}
}
