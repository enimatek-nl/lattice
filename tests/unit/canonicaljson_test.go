package unit

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/matrixjson"
)

// TestCanonicalJSONSpecExamples uses the vectors published in the specification
// appendices (spec.matrix.org/v1.19/appendices/#canonical-json-examples).
func TestCanonicalJSONSpecExamples(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty object", `{}`, `{}`},
		{"two members", "{\"one\": 1,\n \"two\": \"Two\"}", `{"one":1,"two":"Two"}`},
		{"sorted members", "{\n \"b\": \"2\",\n \"a\": \"1\"\n}", `{"a":"1","b":"2"}`},
		{"already sorted", `{"b":"2","a":"1"}`, `{"a":"1","b":"2"}`},
		{
			"nested object",
			`{
    "auth": {
        "success": true,
        "mxid": "@john.doe:example.com",
        "profile": {
            "display_name": "John Doe",
            "three_pids": [
                {
                    "medium": "email",
                    "address": "john.doe@example.org"
                },
                {
                    "medium": "msisdn",
                    "address": "123456789"
                }
            ]
        }
    }
}`,
			`{"auth":{"mxid":"@john.doe:example.com","profile":{"display_name":"John Doe","three_pids":[{"address":"john.doe@example.org","medium":"email"},{"address":"123456789","medium":"msisdn"}]},"success":true}}`,
		},
		{"non-ascii passthrough", `{"a": "日本語"}`, `{"a":"日本語"}`},
		{"codepoint ordering", "{\n \"本\": 2,\n \"日\": 1\n}", `{"日":1,"本":2}`},
		{"unicode escape decoded", `{"a": "\u65E5"}`, `{"a":"日"}`},
		{"null member", `{"a": null}`, `{"a":null}`},
		{"negative zero and exponent", `{"a": -0, "b": 1e10}`, `{"a":0,"b":10000000000}`},
		{"array ordering preserved", `["b","a"]`, `["b","a"]`},
		{"escapes normalised", `{"a":"line\nbreak\ttab"}`, `{"a":"line\nbreak\ttab"}`},
		{"control character escape", `{"a":"\u0018\u001f"}`, `{"a":"\u0018\u001f"}`},
		{"line separator is left unescaped", `{"a":"\u2028"}`, "{\"a\":\" \"}"},
		{"quotes and backslash", `{"a":"say \"hi\" \\ ok"}`, `{"a":"say \"hi\" \\ ok"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := matrixjson.CanonicalRaw([]byte(tc.input))
			if err != nil {
				t.Fatalf("CanonicalRaw(%q) failed: %v", tc.input, err)
			}
			if string(got) != tc.want {
				t.Errorf("CanonicalRaw(%q) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

// TestCanonicalJSONIdempotent checks that canonical output re-canonicalises to
// itself, which is what signature verification depends on.
// A signed response has to be canonicalised, and callers build objects with the Go types
// they have, not the two the decoder produces. An encoder that refuses []string or
// []map[string]any turns a signed response into a 500 at runtime.
func TestCanonicalJSONAcceptsGoContainerTypes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"strings", []string{"b", "a"}, `["b","a"]`},
		{"hand built objects", []map[string]any{{"b": 1, "a": 2}}, `[{"a":2,"b":1}]`},
		{"string keyed map", map[string]any{"z": "1", "a": []any{1, 2}}, `{"a":[1,2],"z":"1"}`},
	}
	for _, tc := range cases {
		if tc.want == "" {
			continue
		}
		got, err := matrixjson.Canonical(tc.value)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}

	// A struct is still refused: which of its fields belong in a signed document is a
	// decision, and `json:"..."` tags are not the place that decision is made.
	if _, err := matrixjson.Canonical(struct {
		A int `json:"a"`
	}{A: 1}); err == nil {
		t.Errorf("a struct was canonicalised; the signed form of a struct is not implied by its tags")
	}
}

func TestCanonicalJSONIdempotent(t *testing.T) {
	t.Parallel()
	const input = `{"a":1,"b":[1,2,3],"c":{"d":"é"},"e":null}`
	first, err := matrixjson.CanonicalRaw([]byte(input))
	if err != nil {
		t.Fatalf("first pass failed: %v", err)
	}
	second, err := matrixjson.CanonicalRaw(first)
	if err != nil {
		t.Fatalf("second pass failed: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("not idempotent: %s then %s", first, second)
	}
}

// TestCanonicalJSONNumberRules covers the integer range the canonical grammar
// imposes, which is also what the Complement JSON validity tests assert.
func TestCanonicalJSONNumberRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"max int", `{"n": 9007199254740991}`, `{"n":9007199254740991}`, nil},
		{"min int", `{"n": -9007199254740991}`, `{"n":-9007199254740991}`, nil},
		{"above max", `{"n": 9007199254740992}`, "", matrixjson.ErrNumberOutOfRange},
		{"below min", `{"n": -9007199254740992}`, "", matrixjson.ErrNumberOutOfRange},
		{"fraction", `{"n": 1.1}`, "", matrixjson.ErrNonInteger},
		{"integral float", `{"n": 3.0}`, `{"n":3}`, nil},
		{"exponent integral", `{"n": 2e3}`, `{"n":2000}`, nil},
		{"huge exponent out of range", `{"n": 1e30}`, "", matrixjson.ErrNumberOutOfRange},
		{"negative zero", `{"n": -0}`, `{"n":0}`, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := matrixjson.CanonicalRaw([]byte(tc.input))
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected %v, got %s", tc.wantErr, got)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestCanonicalJSONRejectsInvalidInput checks the failure modes a homeserver can
// be fed over the network.
func TestCanonicalJSONRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"trailing data", `{"a":1} {"b":2}`},
		{"truncated", `{"a":`},

		{"bare NaN", `{"a": NaN}`},
		{"bare Infinity", `{"a": Infinity}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := matrixjson.CanonicalRaw([]byte(tc.input)); err == nil {
				t.Errorf("expected an error for %q", tc.input)
			}
		})
	}
}

// TestCanonicalJSONRejectsInvalidUTF8 documents that Go strings carrying raw
// invalid UTF-8 are refused instead of silently mangled.
func TestCanonicalJSONRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()
	bad := "keep" + string([]byte{0xff, 0xfe})
	_, err := matrixjson.Canonical(map[string]any{"k": bad})
	if !errors.Is(err, matrixjson.ErrInvalidUTF8) {
		t.Fatalf("expected ErrInvalidUTF8, got %v", err)
	}
}

// TestCanonicalJSONKeyOrderIsCodepointOrder checks the sorting rule for keys that
// mix ASCII and non-ASCII code points.
func TestCanonicalJSONKeyOrderIsCodepointOrder(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"b":  1,
		"A":  2,
		"é":  3,
		"日":  4,
		"~":  5,
		"":   6,
		"a1": 7,
	}
	out, err := matrixjson.Canonical(value)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}
	want := `{"":6,"A":2,"a1":7,"b":1,"~":5,"é":3,"日":4}`
	if string(out) != want {
		t.Errorf("got %s, want %s", out, want)
	}
}

// TestDecodePreservesNumbers checks that decoding keeps the exact numeric token,
// so range validation is not lost to float conversion.
func TestDecodePreservesNumbers(t *testing.T) {
	t.Parallel()
	v, err := matrixjson.Decode([]byte(`{"n":9007199254740991}`))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("decoded value is %T", v)
	}
	num, ok := m["n"].(json.Number)
	if !ok {
		t.Fatalf("number decoded as %T", m["n"])
	}
	if num.String() != "9007199254740991" {
		t.Errorf("number = %q", num.String())
	}
	if !strings.Contains(num.String(), "991") {
		t.Errorf("unexpected number %q", num.String())
	}
}
