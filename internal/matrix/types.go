package matrix

import "encoding/json"

// RawMessage is the raw JSON of an event content block. Repositories persist
// content verbatim so unknown fields survive a round trip.
type RawMessage = json.RawMessage

// Object is an arbitrary JSON object, used for small inline payloads such as
// unsigned blocks and filters.
type Object = map[string]any

// Array is an arbitrary JSON array.
type Array = []any

// MustParseObject decodes raw into a generic object, returning an empty object
// for invalid or empty input.
func MustParseObject(raw []byte) Object {
	out := Object{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Object{}
	}
	return out
}
