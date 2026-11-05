package unit

import (
	"errors"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// TestReferencesAreReadInWhicheverShapeTheRoomVersionUses covers the one place that turns an
// event's prev_events and auth_events into event IDs.
//
// Room versions 3 and later list IDs. Room versions 1 and 2 list [id, {"sha256": hash}] pairs, and
// a reader written for the first shape returns nothing at all for the second: the event then looks
// like it has no parents and no auth events, which does not crash anything — it quietly resolves
// state from an empty graph, authorises against no auth chain and pages through no history.
func TestReferencesAreReadInWhicheverShapeTheRoomVersionUses(t *testing.T) {
	t.Parallel()

	pairs := map[string]any{
		"auth_events": []any{
			[]any{"$create:example.org", map[string]any{"sha256": "aaa"}},
			[]any{"$join:example.org", map[string]any{"sha256": "bbb"}},
		},
		"prev_events": []any{
			[]any{"$parent:example.org", map[string]any{"sha256": "ccc"}},
		},
	}
	got := events.ReferencedIDs(pairs, "auth_events")
	if strings.Join(got, ",") != "$create:example.org,$join:example.org" {
		t.Errorf("a room version 1/2 reference list read as %q, wanting both event IDs", strings.Join(got, ","))
	}
	if got := events.ReferencedIDs(pairs, "prev_events"); len(got) != 1 || got[0] != "$parent:example.org" {
		t.Errorf("the parent of a pair-form reference came out as %q", got)
	}

	plain := map[string]any{
		"prev_events": []any{"$one:example.org", "$two:example.org"},
	}
	if got := events.ReferencedIDs(plain, "prev_events"); len(got) != 2 {
		t.Errorf("a plain reference list read as %q, wanting both event IDs", got)
	}

	// Some paths in this server hold the already-narrowed shape rather than decoded JSON.
	already := map[string]any{"auth_events": []string{"$a:example.org", "$b:example.org"}}
	if got := events.ReferencedIDs(already, "auth_events"); len(got) != 2 {
		t.Errorf("a []string reference list read as %q, wanting both", got)
	}

	// A missing field, a malformed entry and an empty pair all read as no references, not as a
	// crash and not as a reference to the empty string.
	for name, event := range map[string]map[string]any{
		"absent":   {},
		"garbage":  {"prev_events": "not a list"},
		"empty":    {"prev_events": []any{}},
		"nopair":   {"prev_events": []any{[]any{}}},
		"noidpair": {"prev_events": []any{[]any{map[string]any{"sha256": "x"}}}},
	} {
		if got := events.ReferencedIDs(event, "prev_events"); len(got) != 0 {
			t.Errorf("%s: read as %q, wanting no references", name, got)
		}
	}
}

// TestReferencesAreWrittenInTheRoomsShape is the other half: this server must describe the events
// it creates in the form the room's version defines, because the reference hashes it publishes are
// part of what every other server hashes and signs around.
func TestReferencesAreWrittenInTheRoomsShape(t *testing.T) {
	t.Parallel()

	hashes := map[string]string{"$a:example.org": "hash-a", "$b:example.org": "hash-b"}
	hashOf := func(id string) (string, error) {
		hash, ok := hashes[id]
		if !ok {
			return "", errors.New("this server has never seen that event")
		}
		return hash, nil
	}

	plain, err := events.References(roomversion.MustGet("11"), []string{"$a:example.org", "$b:example.org"}, hashOf)
	if err != nil {
		t.Fatalf("plain references: %v", err)
	}
	for i, want := range []string{"$a:example.org", "$b:example.org"} {
		if got, ok := plain[i].(string); !ok || got != want {
			t.Errorf("room version 11 reference %d is %#v, wanting the bare ID", i, plain[i])
		}
	}

	for _, id := range []string{"1", "2"} {
		pairedList, err := events.References(roomversion.MustGet(id), []string{"$a:example.org", "$b:example.org"}, hashOf)
		if err != nil {
			t.Fatalf("room version %s references: %v", id, err)
		}
		for i, want := range []struct{ id, hash string }{{"$a:example.org", "hash-a"}, {"$b:example.org", "hash-b"}} {
			pair, ok := pairedList[i].([]any)
			if !ok || len(pair) != 2 {
				t.Fatalf("room version %s reference %d is %#v, wanting [event_id, {sha256}]", id, i, pairedList[i])
			}
			if pair[0] != want.id {
				t.Errorf("room version %s reference %d names %v, wanting %s", id, i, pair[0], want.id)
			}
			body, ok := pair[1].(map[string]any)
			if !ok || body["sha256"] != want.hash {
				t.Errorf("room version %s reference %d carries %#v, wanting the referenced event's reference hash",
					id, i, pair[1])
			}
		}
	}

	// A reference whose hash cannot be computed is not rendered as a half-filled pair.
	if _, err := events.References(roomversion.MustGet("1"), []string{"$unknown:example.org"}, hashOf); err == nil {
		t.Errorf("a reference to an event this server does not have was accepted")
	}
	// Nor is it silently rendered as a bare ID, which would be a pair-shaped room holding
	// version-3-style references.
	if _, err := events.References(roomversion.MustGet("2"), []string{"$a:example.org"}, nil); err == nil {
		t.Errorf("room version 2 references were built with no way to hash the referenced events")
	}
}
