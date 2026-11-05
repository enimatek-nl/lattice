package events

import (
	"fmt"

	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/roomversion"
)

// ReferencedIDs returns the event IDs named by one of an event's reference fields, normally
// "prev_events" or "auth_events", in whichever shape that room version's event format uses.
//
// Room versions 3 and later list plain event IDs. Room versions 1 and 2 list pairs: an event ID
// and the reference hash of the event being pointed at. Reading a pair list as a list of strings
// yields nothing at all, and a room whose references all read as empty is a room whose graph has
// quietly come apart — the state resolved from it, the auth chains walked through it and the
// history paged along it would each be wrong, and nothing would say so. So the shape is handled
// here, once, rather than at every place that walks a reference.
//
// The hash carried in a pair is not checked here. This server does not treat a disagreement about
// it as a verdict either: what establishes an event is its own content hash and its origin server's
// signature, and the embedded hash only restates a fact a receiver can compute for itself. Events
// in those versions were written by servers that disagree about what a reference hash covers, so
// refusing on the embedded value would shut this server out of rooms that already exist. The
// referenced event's own validity is checked where that event is handled, which is the place that
// can actually judge it.
func ReferencedIDs(event map[string]any, field string) []string {
	// A value that has already been narrowed to []string is a list of plain IDs: nothing in this
	// server builds that shape for a room version that uses pairs, so there is nothing to unpack.
	if plain, ok := event[field].([]string); ok {
		out := make([]string, 0, len(plain))
		for _, id := range plain {
			if id != "" {
				out = append(out, id)
			}
		}
		return out
	}
	list, _ := event[field].([]any)
	ids := make([]string, 0, len(list))
	for _, entry := range list {
		switch reference := entry.(type) {
		case string:
			if reference != "" {
				ids = append(ids, reference)
			}
		case []any:
			if len(reference) == 0 {
				continue
			}
			if id, ok := reference[0].(string); ok && id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// References renders a list of event IDs in the shape the room version's event format expects:
// plain IDs from room version 3 on, and [event_id, {"sha256": hash}] pairs in room versions 1 and
// 2. hashOf is asked for the reference hash of each referenced event, unpadded base64; an error
// there means this server cannot describe an event it is about to point at, which is a fact worth
// failing on rather than papering over with a half-filled reference.
func References(rules roomversion.Rules, ids []string, hashOf func(id string) (string, error)) ([]any, error) {
	out := make([]any, 0, len(ids))
	if rules.EventIDs != roomversion.EventIDServerAssigned {
		for _, id := range ids {
			out = append(out, id)
		}
		return out, nil
	}
	if hashOf == nil {
		return nil, fmt.Errorf("events: room versions 1 and 2 need the reference hash of each referenced event")
	}
	for _, id := range ids {
		hash, err := hashOf(id)
		if err != nil {
			return nil, fmt.Errorf("events: no reference hash for %s: %w", id, err)
		}
		out = append(out, []any{id, map[string]any{"sha256": hash}})
	}
	return out, nil
}

// ReferenceHashOf returns an event's reference hash in the encoding a reference pair carries.
func ReferenceHashOf(rules roomversion.Rules, event map[string]any) (string, error) {
	sum, err := ReferenceHash(rules, event)
	if err != nil {
		return "", err
	}
	return matrixjson.EncodeUnpadded(sum), nil
}
