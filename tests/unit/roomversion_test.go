package unit

import (
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/roomversion"
)

// TestRoomVersionGrammar checks the appendix grammar: 1-32 characters from
// a-z, 0-9, "." and "-", non-empty.
func TestRoomVersionGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id      string
		wantErr bool
	}{
		{"1", false},
		{"11", false},
		{"1.2", false},
		{"1.2-beta", false},
		{"com.example.version", false},
		{"org.matrix.msc4291.12", false},
		{"", true},
		{"V1", true},
		{"1_2", true},
		{"1 2", true},
		{"!room:example.org", true},
		{strings.Repeat("a", 33), true},
		{strings.Repeat("a", 32), false},
	}
	for _, tc := range cases {
		err := roomversion.Validate(tc.id)
		if tc.wantErr && err == nil {
			t.Errorf("Validate(%q): expected an error", tc.id)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("Validate(%q): unexpected error %v", tc.id, err)
		}
	}
}

// TestRoomVersionTable checks the invariants that must hold for every entry, so a
// future edit cannot quietly leave a version half-specified.
func TestRoomVersionTable(t *testing.T) {
	t.Parallel()
	for _, id := range roomversion.All() {
		rules, ok := roomversion.Get(id)
		if !ok {
			t.Fatalf("All() listed %q but Get cannot find it", id)
		}
		if rules.ID != id {
			t.Errorf("version %q reports ID %q", id, rules.ID)
		}
		if rules.AuthRuleset == "" || rules.RedactionProfile == "" || rules.StateRes == "" {
			t.Errorf("version %q is missing a ruleset selector: %+v", id, rules)
		}
		switch rules.StateRes {
		case roomversion.StateResV1, roomversion.StateResV2, roomversion.StateResV2_1:
		default:
			t.Errorf("version %q has unknown state resolution %q", id, rules.StateRes)
		}
		if rules.EventIDs == roomversion.EventIDServerAssigned && id != "1" && id != "2" {
			t.Errorf("version %q must use hash-based event IDs", id)
		}
	}

	// State resolution v1 belongs to room version 1 only.
	if got := roomversion.MustGet("1").StateRes; got != roomversion.StateResV1 {
		t.Errorf("room version 1 state resolution = %q", got)
	}
	for _, id := range []string{"2", "3", "9", "10", "11"} {
		if got := roomversion.MustGet(id).StateRes; got != roomversion.StateResV2 {
			t.Errorf("room version %s state resolution = %q, want v2", id, got)
		}
	}
}

// TestRoomVersionDifferences pins the behaviour that changes between versions.
// These assertions are the guard against a room version being treated as
// "basically the same as" another one.
func TestRoomVersionDifferences(t *testing.T) {
	t.Parallel()
	v9 := roomversion.MustGet("9")
	v10 := roomversion.MustGet("10")
	v11 := roomversion.MustGet("11")
	v12 := roomversion.MustGet("12")

	if v9.EnforceIntegerPowerLevels || v9.SupportsKnockRestricted {
		t.Errorf("room version 9 must not have version 10 features: %+v", v9)
	}
	if !v10.EnforceIntegerPowerLevels || !v10.SupportsKnockRestricted {
		t.Errorf("room version 10 must enforce integer power levels and knock_restricted")
	}
	if !v10.CreatorInCreateContent || v10.RedactsInContent {
		t.Errorf("room version 10 keeps content.creator and top-level redacts")
	}
	if v10.RedactionProfile != roomversion.RedactionPreV11 {
		t.Errorf("room version 10 must use the pre-11 redaction profile")
	}
	if v11.RedactionProfile != roomversion.RedactionV11 {
		t.Errorf("room version 11 must use the version 11 redaction profile")
	}
	if v11.CreatorInCreateContent || !v11.RedactsInContent {
		t.Errorf("room version 11 removes content.creator and moves redacts into content")
	}
	if v11.StateRes != roomversion.StateResV2 || v12.StateRes != roomversion.StateResV2_1 {
		t.Errorf("room version 12 is the first version with the revised state resolution")
	}
	if !v12.DomainlessRoomIDs || !v12.PrivilegedCreators {
		t.Errorf("room version 12 must report domain-free room IDs and privileged creators")
	}
	if !v9.RedactionKeepsAuthorisedVia || roomversion.MustGet("8").RedactionKeepsAuthorisedVia {
		t.Errorf("join_authorised_via_users_server is protected from redaction starting at room version 9")
	}
	if !roomversion.MustGet("5").SpecialCaseAliases || roomversion.MustGet("6").SpecialCaseAliases {
		t.Errorf("m.room.aliases special casing ends at room version 6")
	}
	if roomversion.MustGet("4").EnforceKeyValidity || !roomversion.MustGet("5").EnforceKeyValidity {
		t.Errorf("signing key validity is enforced from room version 5")
	}
}

// TestPick covers room version negotiation: an unset preference takes the default,
// an unsupported-but-valid version is refused rather than swapped, and a malformed
// version is refused as malformed.
func TestPick(t *testing.T) {
	t.Parallel()
	chosen, usedDefault, err := roomversion.Pick("")
	if err != nil || !usedDefault || chosen.ID != roomversion.Default().ID {
		t.Errorf("Pick(\"\") = %+v, %v, %v", chosen, usedDefault, err)
	}
	chosen, usedDefault, err = roomversion.Pick("10")
	if err != nil || usedDefault || chosen.ID != "10" {
		t.Errorf("Pick(\"10\") = %+v, %v, %v", chosen, usedDefault, err)
	}
	for _, offered := range []string{"3", "4", "5", "6", "7", "8", "9", "10", "11"} {
		if chosen, usedDefault, err := roomversion.Pick(offered); err != nil || usedDefault || chosen.ID != offered {
			t.Errorf("Pick(%q) = %+v, %v, %v; this version is offered", offered, chosen, usedDefault, err)
		}
	}
	if chosen, usedDefault, err := roomversion.Pick("7"); err != nil || usedDefault || chosen.ID != "7" {
		t.Errorf("Pick(\"7\") = %+v, %v, %v; version 7 is offered because its rules are implemented", chosen, usedDefault, err)
	}
	// Room versions 1 and 2 are offered, because the rules that make them different are
	// implemented: their own authorisation table, State Resolution 1 in the version that
	// needs it, identifiers the origin server chooses, and references written as pairs.
	for _, legacy := range []string{"1", "2"} {
		if chosen, usedDefault, err := roomversion.Pick(legacy); err != nil || usedDefault || chosen.ID != legacy {
			t.Errorf("Pick(%q) = %+v, %v, %v; room version %s is offered", legacy, chosen, usedDefault, err, legacy)
		}
	}
	// A version this server has rules for but has not implemented is refused rather than
	// quietly substituted: the client asked for particular semantics, and handing it different
	// ones without saying so is worse than saying no.
	if _, _, err := roomversion.Pick("12"); err == nil {
		t.Errorf("Pick(\"12\") should refuse a registered but unimplemented version instead of substituting one")
	}
	if _, _, err := roomversion.Pick("Not A Version"); err == nil {
		t.Errorf("Pick should reject a malformed version")
	}
}

// TestAvailable checks that only versions this server can create rooms in are
// advertised, while the full registry still covers rooms it may be sent.
func TestAvailable(t *testing.T) {
	t.Parallel()
	available := roomversion.Available()
	if len(available) != len(roomversion.Supported()) {
		t.Errorf("available has %d entries, supported has %d", len(available), len(roomversion.Supported()))
	}
	for _, rules := range roomversion.Supported() {
		if _, ok := available[rules.ID]; !ok {
			t.Errorf("supported version %s is missing from the capability map", rules.ID)
		}
	}
	if _, ok := available["12"]; ok {
		t.Errorf("room version 12 must not be advertised while it cannot be created")
	}
	if !roomversion.IsKnown("1") {
		t.Errorf("room version 1 must still be known: events in such rooms need its rules")
	}
	// The upgrade path asks the same question by a different route, so the two answers have to
	// agree: a version can be recognised and still not be one this server can run a room in.
	for _, version := range roomversion.Supported() {
		if !roomversion.IsOffered(version.ID) {
			t.Errorf("room version %s is offered for creation but IsOffered says it is not", version.ID)
		}
	}
	for _, version := range []string{"12", "org.matrix.msc4458", "not a version"} {
		if roomversion.IsOffered(version) {
			t.Errorf("IsOffered claims this server runs room version %q", version)
		}
	}
}
