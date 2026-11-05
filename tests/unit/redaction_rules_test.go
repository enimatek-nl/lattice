package unit

import (
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/redaction"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Where a redaction names its target is decided by the room version, and who may act on it
// is decided by the room's power levels. Both are small questions with sharp answers, and
// both are decided before anything is destroyed.

func TestRedactionTargetFollowsTheRoomVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		version string
		event   map[string]any
		want    string
	}{
		{
			name: "version 10 names it at the top level", version: "10",
			event: map[string]any{"type": "m.room.redaction", "redacts": "$target:example.org",
				"content": map[string]any{"reason": "because"}},
			want: "$target:example.org",
		},
		{
			name: "version 11 names it in content", version: "11",
			event: map[string]any{"type": "m.room.redaction",
				"content": map[string]any{"redacts": "$target:example.org"}},
			want: "$target:example.org",
		},
		{
			// Reading the top-level key as a fallback would let one signed event name
			// different targets to different readers, and the room would fork on which
			// event was destroyed.
			name: "version 11 ignores a top-level key", version: "11",
			event: map[string]any{"type": "m.room.redaction", "redacts": "$wrong:example.org",
				"content": map[string]any{"redacts": "$right:example.org"}},
			want: "$right:example.org",
		},
		{
			name: "version 10 with the field in the wrong place names nothing", version: "10",
			event: map[string]any{"type": "m.room.redaction",
				"content": map[string]any{"redacts": "$target:example.org"}},
			want: "",
		},
		{
			name: "a redaction naming nothing", version: "11",
			event: map[string]any{"type": "m.room.redaction", "content": map[string]any{"reason": "void"}},
			want:  "",
		},
	} {
		rules := roomversion.MustGet(tc.version)
		got, named := redaction.TargetOf(rules, tc.event)
		if got != tc.want {
			t.Errorf("%s: target = %q, want %q", tc.name, got, tc.want)
		}
		if named != (tc.want != "") {
			t.Errorf("%s: named = %v, want %v", tc.name, named, tc.want != "")
		}
	}
}

// A redaction event is served with the target field in both places so that a client does
// not have to know which room version moved it. The stored event is not touched: an added
// field would change its hash, and in the versions where the ID is a hash that would rename
// the event.
func TestRedactionIsServedWithItsTargetWhereClientsLook(t *testing.T) {
	t.Parallel()
	v11 := roomversion.MustGet("11")
	event := map[string]any{"type": "m.room.redaction", "content": map[string]any{"redacts": "$x:e"}}
	redaction.ForClient(v11, event)
	if event["redacts"] != "$x:e" {
		t.Errorf("a version 11 redaction is not served with a top-level redacts: %v", event)
	}

	v10 := roomversion.MustGet("10")
	older := map[string]any{"type": "m.room.redaction", "redacts": "$y:e", "content": map[string]any{}}
	redaction.ForClient(v10, older)
	content, _ := older["content"].(map[string]any)
	if content["redacts"] != "$y:e" {
		t.Errorf("a version 10 redaction is not served with content.redacts: %v", older)
	}

	// A message is not adjusted, and a redaction that names nothing gains nothing.
	message := map[string]any{"type": "m.room.message", "content": map[string]any{"body": "hi"}}
	redaction.ForClient(v11, message)
	if _, added := message["redacts"]; added {
		t.Errorf("ForClient touched a message: %v", message)
	}
	empty := map[string]any{"type": "m.room.redaction", "content": map[string]any{}}
	redaction.ForClient(v11, empty)
	if _, added := empty["redacts"]; added {
		t.Errorf("ForClient invented a target: %v", empty)
	}
}

// The authorisation rules do not consider the redact level: they decide whether an event may
// be sent. Whether the redaction is applied is a second question, asked by each server that
// receives it, and it has two answers either of which is enough.
func TestMayRedactFollowsTheRedactLevelOrTheDomain(t *testing.T) {
	t.Parallel()
	levels, err := eventauth.LoadPowerLevels(roomversion.MustGet("11"), map[string]any{
		"users":         map[string]any{"@admin:example.org": 100, "@nobody:example.org": 0},
		"users_default": 0,
		"events":        map[string]any{"m.room.redaction": 0},
		"redact":        50,
	})
	if err != nil {
		t.Fatalf("loading the power levels: %v", err)
	}

	for _, tc := range []struct {
		name   string
		sender string
		target string
		want   bool
	}{
		{name: "at the redact level may redact anybody", sender: "@admin:example.org",
			target: "@other:elsewhere.org", want: true},
		{name: "the same server may clean up its own users", sender: "@nobody:example.org",
			target: "@someone:example.org", want: true},
		{name: "another server's user without the redact level may not", sender: "@guest:elsewhere.org",
			target: "@someone:example.org", want: false},
		{name: "the redact level is not reached by an exact match alone on another server",
			sender: "@guest:elsewhere.org", target: "@admin:example.org", want: false},
		{name: "an unparseable sender has no domain to match", sender: "not-a-user",
			target: "@someone:example.org", want: false},
	} {
		if got := levels.MayRedact(tc.sender, tc.target); got != tc.want {
			t.Errorf("%s: MayRedact(%q, %q) = %v, want %v", tc.name, tc.sender, tc.target, got, tc.want)
		}
	}

	// The default room: everybody's level is the default and the redact level is 50, so a
	// stranger on another server cannot redact, and a local user can — which is the rule as
	// written, not a hole to plug here.
	unconfigured := eventauth.DefaultPowerLevels()
	if !unconfigured.MayRedact("@alice:example.org", "@bob:example.org") {
		t.Errorf("a local user may not redact a local event with the default levels")
	}
	if unconfigured.MayRedact("@mallory:elsewhere.org", "@bob:example.org") {
		t.Errorf("a user from another server may redact at the default levels")
	}
}
