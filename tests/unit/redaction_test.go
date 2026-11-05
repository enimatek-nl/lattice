package unit

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/redaction"
	"github.com/enimatek/lattice/internal/roomversion"
)

// vectorEvent is the event used by the cross-implementation goldens below. It
// deliberately carries properties that only some room versions protect, plus the
// transport-only keys a receiving server must not hash.
const vectorEvent = `{
  "auth_events": ["$c:example.org", "$p:example.org"],
  "content": {"body": "hello", "msgtype": "m.text", "hidden": "gone"},
  "depth": 7,
  "origin": "example.org",
  "origin_server_ts": 1700000000000,
  "prev_events": ["$prev:example.org"],
  "room_id": "!room:example.org",
  "sender": "@alice:example.org",
  "type": "m.room.message",
  "membership": "join",
  "prev_state": "kept",
  "redacts": "$target:example.org",
  "outlier": true,
  "age_ts": 1
}`

// goldens below were produced by gomatrixserverlib
// v0.0.0-20260716140101-4fe595dc7f58 (the library Dendrite builds on) from the
// same input bytes. They are here on purpose: redaction and reference hashing are
// where two implementations silently drift apart, and once event IDs disagree the
// room is forked. If a golden stops matching, either this implementation or the
// library changed, and that needs to be a deliberate decision.
var goldens = []struct {
	roomVersion string
	redacted    string
	eventID     string
}{
	{
		roomVersion: "3",
		redacted:    `{"auth_events":["$c:example.org","$p:example.org"],"content":{},"depth":7,"membership":"join","origin":"example.org","origin_server_ts":1700000000000,"prev_events":["$prev:example.org"],"prev_state":"kept","room_id":"!room:example.org","sender":"@alice:example.org","type":"m.room.message"}`,
		eventID:     "$1YYoe3Q9H0V7opN6VO8xuNAlY6Gyk4uxxW9qIEi3SxE",
	},
	{
		roomVersion: "10",
		redacted:    `{"auth_events":["$c:example.org","$p:example.org"],"content":{},"depth":7,"membership":"join","origin":"example.org","origin_server_ts":1700000000000,"prev_events":["$prev:example.org"],"prev_state":"kept","room_id":"!room:example.org","sender":"@alice:example.org","type":"m.room.message"}`,
		eventID:     "$1YYoe3Q9H0V7opN6VO8xuNAlY6Gyk4uxxW9qIEi3SxE",
	},
	{
		roomVersion: "11",
		redacted:    `{"auth_events":["$c:example.org","$p:example.org"],"content":{},"depth":7,"origin_server_ts":1700000000000,"prev_events":["$prev:example.org"],"room_id":"!room:example.org","sender":"@alice:example.org","type":"m.room.message"}`,
		eventID:     "$F0qOSjkTdQqqMJmoiJAQY5guE-jgC6M0CMmPOzfWxOg",
	},
}

// TestRedactionAndEventIDsAgainstReferenceImplementation compares both the
// redacted document and the derived event ID with gomatrixserverlib.
func TestRedactionAndEventIDsAgainstReferenceImplementation(t *testing.T) {
	t.Parallel()
	event, err := events.Parse([]byte(vectorEvent))
	if err != nil {
		t.Fatalf("cannot parse vector event: %v", err)
	}

	for _, golden := range goldens {
		rules := roomversion.MustGet(golden.roomVersion)

		gotRedacted, err := matrixjson.Canonical(events.Redacted(rules, event))
		if err != nil {
			t.Fatalf("room version %s: cannot canonicalise redacted event: %v", golden.roomVersion, err)
		}
		if string(gotRedacted) != golden.redacted {
			t.Errorf("room version %s redacted form mismatch:\n got %s\nwant %s", golden.roomVersion, gotRedacted, golden.redacted)
		}

		gotID, err := events.EventID(rules, event)
		if err != nil {
			t.Fatalf("room version %s: cannot compute event ID: %v", golden.roomVersion, err)
		}
		if gotID != golden.eventID {
			t.Errorf("room version %s event ID = %s, want %s", golden.roomVersion, gotID, golden.eventID)
		}
	}
}

// TestEventIDsDifferByRedactionRules is the reason the reference hash is
// room-version aware at all: the same bytes get a different identifier once the
// redaction rules change.
func TestEventIDsDifferByRedactionRules(t *testing.T) {
	t.Parallel()
	event, err := events.Parse([]byte(vectorEvent))
	if err != nil {
		t.Fatalf("cannot parse vector event: %v", err)
	}
	v10, err := events.EventID(roomversion.MustGet("10"), event)
	if err != nil {
		t.Fatalf("v10 event ID: %v", err)
	}
	v11, err := events.EventID(roomversion.MustGet("11"), event)
	if err != nil {
		t.Fatalf("v11 event ID: %v", err)
	}
	if v10 == v11 {
		t.Errorf("room version 11 changed the redaction rules, so the identifier must change too")
	}
}

// TestRedactionKeepsPerVersionKeys checks the allow lists that the goldens above
// do not exercise.
func TestRedactionKeepsPerVersionKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		roomVersion string
		event       map[string]any
		wantContent map[string]any
	}{
		{
			name:        "create keeps whole content in v11",
			roomVersion: "11",
			event: map[string]any{
				"type":    "m.room.create",
				"content": map[string]any{"room_version": "11", "predecessor": map[string]any{"room_id": "!old:example.org"}, "extra": "kept"},
			},
			wantContent: map[string]any{"room_version": "11", "predecessor": map[string]any{"room_id": "!old:example.org"}, "extra": "kept"},
		},
		{
			name:        "create keeps only creator before v11",
			roomVersion: "10",
			event: map[string]any{
				"type":    "m.room.create",
				"content": map[string]any{"creator": "@alice:example.org", "room_version": "10", "extra": "gone"},
			},
			wantContent: map[string]any{"creator": "@alice:example.org"},
		},
		{
			name:        "redaction target survives in v11",
			roomVersion: "11",
			event: map[string]any{
				"type":    "m.room.redaction",
				"content": map[string]any{"redacts": "$target:example.org", "reason": "gone"},
			},
			wantContent: map[string]any{"redacts": "$target:example.org"},
		},
		{
			name:        "power levels keep the invite level in v11 only",
			roomVersion: "11",
			event: map[string]any{
				"type":    "m.room.power_levels",
				"content": map[string]any{"invite": 0, "users": map[string]any{"@alice:example.org": 100}, "comment": "gone"},
			},
			wantContent: map[string]any{"invite": 0, "users": map[string]any{"@alice:example.org": 100}},
		},
		{
			name:        "join authorised via survives in v9",
			roomVersion: "9",
			event: map[string]any{
				"type":       "m.room.member",
				"content":    map[string]any{"membership": "join", "join_authorised_via_users_server": "@mod:example.org", "displayname": "gone"},
				"state_key":  "@alice:example.org",
				"origin":     "example.org",
				"membership": "join",
			},
			wantContent: map[string]any{"membership": "join", "join_authorised_via_users_server": "@mod:example.org"},
		},
		{
			name:        "join authorised via is not protected in v8",
			roomVersion: "8",
			event: map[string]any{
				"type":      "m.room.member",
				"content":   map[string]any{"membership": "join", "join_authorised_via_users_server": "@mod:example.org"},
				"state_key": "@alice:example.org",
			},
			wantContent: map[string]any{"membership": "join"},
		},
		{
			name:        "third party invite signature survives in v11",
			roomVersion: "11",
			event: map[string]any{
				"type": "m.room.member",
				"content": map[string]any{
					"membership":         "invite",
					"third_party_invite": map[string]any{"display_name": "gone", "signed": map[string]any{"mxid": "@alice:example.org"}},
				},
				"state_key": "@alice:example.org",
			},
			wantContent: map[string]any{
				"membership":         "invite",
				"third_party_invite": map[string]any{"signed": map[string]any{"mxid": "@alice:example.org"}},
			},
		},
		{
			name:        "aliases keep their list only in old versions",
			roomVersion: "5",
			event: map[string]any{
				"type":      "m.room.aliases",
				"content":   map[string]any{"aliases": []any{"#room:example.org"}},
				"state_key": "example.org",
			},
			wantContent: map[string]any{"aliases": []any{"#room:example.org"}},
		},
		{
			name:        "aliases lose their special casing in v6",
			roomVersion: "6",
			event: map[string]any{
				"type":      "m.room.aliases",
				"content":   map[string]any{"aliases": []any{"#room:example.org"}},
				"state_key": "example.org",
			},
			wantContent: map[string]any{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := roomversion.MustGet(tc.roomVersion)
			redacted := redaction.Apply(rules, tc.event)
			content, ok := redacted["content"].(map[string]any)
			if !ok {
				t.Fatalf("redacted event has no content object: %v", redacted)
			}
			if !reflect.DeepEqual(content, tc.wantContent) {
				t.Errorf("content = %#v, want %#v", content, tc.wantContent)
			}
		})
	}
}

// TestRedactionDropsTransportAndUnprotectedKeys checks the top-level strip list,
// including the keys that room version 11 stopped protecting.
func TestRedactionDropsTransportAndUnprotectedKeys(t *testing.T) {
	t.Parallel()
	event := map[string]any{
		"type":              "m.room.message",
		"content":           map[string]any{"body": "hi"},
		"origin":            "example.org",
		"membership":        "join",
		"prev_state":        "kept",
		"age_ts":            1,
		"outlier":           true,
		"destinations":      []any{"example.org"},
		"origin_server_ts":  1700000000000,
		"unsigned":          map[string]any{"age": 1},
		"hashes":            map[string]any{"sha256": "abc"},
		"signatures":        map[string]any{"example.org": map[string]any{"ed25519:a": "sig"}},
		"some_custom_thing": "gone",
	}

	v10 := redaction.Apply(roomversion.MustGet("10"), event)
	for _, key := range []string{"origin", "membership", "prev_state", "hashes", "signatures", "origin_server_ts"} {
		if _, ok := v10[key]; !ok {
			t.Errorf("room version 10 redaction dropped %q, which it must protect", key)
		}
	}
	for _, key := range []string{"age_ts", "outlier", "destinations", "unsigned", "some_custom_thing"} {
		if _, ok := v10[key]; ok {
			t.Errorf("room version 10 redaction kept %q, which it must drop", key)
		}
	}

	v11 := redaction.Apply(roomversion.MustGet("11"), event)
	for _, key := range []string{"origin", "membership", "prev_state"} {
		if _, ok := v11[key]; ok {
			t.Errorf("room version 11 redaction kept %q, which it no longer protects", key)
		}
	}
	if _, ok := v11["hashes"]; !ok {
		t.Errorf("hashes must survive redaction, otherwise a redacted event cannot be verified")
	}

	// The input must not be modified: verification needs the original bytes.
	if _, ok := event["some_custom_thing"]; !ok {
		t.Errorf("Apply modified the event it was given")
	}
	if len(event) != 13 {
		t.Errorf("the input event changed size: %d entries", len(event))
	}
}

// TestIsRedacted covers the heuristic used when a server claims an event is
// redacted.
func TestIsRedacted(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("11")
	full := map[string]any{"type": "m.room.message", "content": map[string]any{"body": "hi"}}
	if redaction.IsRedacted(rules, full) {
		t.Errorf("an event with content looks redacted")
	}
	redacted := map[string]any{"type": "m.room.message", "content": map[string]any{}}
	if !redaction.IsRedacted(rules, redacted) {
		t.Errorf("an event with empty content should look redacted")
	}
	create := map[string]any{"type": "m.room.create", "content": map[string]any{"room_version": "11"}}
	if redaction.IsRedacted(rules, create) {
		t.Errorf("a version 11 create event keeps its content, so empty-looking state is not redaction")
	}
}

// TestCanonicalFormIsStableAcrossRedaction checks the property the design relies
// on: hashing the redacted form of an already redacted event gives the same bytes.
func TestCanonicalFormIsStableAcrossRedaction(t *testing.T) {
	t.Parallel()
	event, err := events.Parse([]byte(vectorEvent))
	if err != nil {
		t.Fatalf("cannot parse vector event: %v", err)
	}
	for _, id := range []string{"3", "10", "11"} {
		rules := roomversion.MustGet(id)
		once := events.Redacted(rules, event)
		twice := events.Redacted(rules, once)

		first, err := matrixjson.Canonical(once)
		if err != nil {
			t.Fatalf("room version %s: %v", id, err)
		}
		second, err := matrixjson.Canonical(twice)
		if err != nil {
			t.Fatalf("room version %s: %v", id, err)
		}
		if string(first) != string(second) {
			t.Errorf("room version %s: redaction is not idempotent:\n%s\n%s", id, first, second)
		}
	}
}

// TestIntegersSurviveRoundTrip guards the classic hash-breaking mistake of turning
// an integer timestamp into a float when re-encoding.
func TestIntegersSurviveRoundTrip(t *testing.T) {
	t.Parallel()
	event, err := events.Parse([]byte(vectorEvent))
	if err != nil {
		t.Fatalf("cannot parse vector event: %v", err)
	}
	encoded, err := matrixjson.Canonical(event)
	if err != nil {
		t.Fatalf("cannot canonicalise: %v", err)
	}
	encodedStr := string(encoded)
	if !strings.Contains(encodedStr, `"origin_server_ts":1700000000000`) || !strings.Contains(encodedStr, `"depth":7`) {
		t.Errorf("integers were re-encoded as floats: %s", encodedStr)
	}
	// Numbers must stay exactly as typed, so a value that is not an integer in
	// the input is still rejected by the canonical JSON rules rather than coerced.
	if _, err := matrixjson.Canonical(map[string]any{"depth": json.Number("1.5")}); err == nil {
		t.Errorf("a fractional depth should not encode as canonical JSON")
	}
}
