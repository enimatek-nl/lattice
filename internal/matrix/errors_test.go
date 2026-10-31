package matrix

import (
	"encoding/json"
	"testing"
)

// TestErrorMarshal checks the body a client receives for an error: the standard
// errcode and error keys, the optional retry_after_ms, and the Extra keys an
// endpoint adds for a User-Interactive Auth challenge.
func TestErrorMarshal(t *testing.T) {
	cases := []struct {
		name    string
		err     *Error
		want    map[string]any
		wantKey string
		wantAny any
	}{
		{
			name: "plain forbidden",
			err:  ErrForbidden("registration is disabled on this homeserver"),
			want: map[string]any{"errcode": "M_FORBIDDEN", "error": "registration is disabled on this homeserver"},
		},
		{
			name: "rate limited",
			err:  ErrLimitExceeded(250),
			want: map[string]any{"errcode": "M_LIMIT_EXCEEDED", "error": "Too many requests", "retry_after_ms": float64(250)},
		},
		{
			name: "auth challenge",
			err: ErrUnauthorized("additional authentication is required").WithExtra(Object{
				"flows":     []any{Object{"stages": []string{"m.login.dummy"}}},
				"session":   "abc",
				"completed": []string{},
				"params":    Object{},
			}),
			want:    map[string]any{"errcode": "M_UNAUTHORIZED", "error": "additional authentication is required", "session": "abc"},
			wantKey: "flows",
			wantAny: []any{map[string]any{"stages": []any{"m.login.dummy"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.err)
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decoding %s: %v", raw, err)
			}
			for key, value := range tc.want {
				if body[key] != value {
					t.Errorf("%s = %v, want %v (%s)", key, body[key], value, raw)
				}
			}
			if tc.wantKey != "" {
				got, err := json.Marshal(body[tc.wantKey])
				if err != nil {
					t.Fatalf("encoding %s: %v", tc.wantKey, err)
				}
				want, err := json.Marshal(tc.wantAny)
				if err != nil {
					t.Fatalf("encoding the expected %s: %v", tc.wantKey, err)
				}
				if string(got) != string(want) {
					t.Errorf("%s = %s, want %s", tc.wantKey, got, want)
				}
			}
		})
	}
}

// TestMarshalOnValue documents that the Extra merge happens on the pointer
// receiver: the API layer always marshals a *Error.
func TestMarshalOnValue(t *testing.T) {
	err := ErrNotFound("no such room")
	err.Extra = Object{"room_id": "!room:server"}
	body, merr := json.Marshal(err)
	if merr != nil {
		t.Fatalf("marshalling: %v", merr)
	}
	var decoded map[string]any
	if uerr := json.Unmarshal(body, &decoded); uerr != nil {
		t.Fatalf("decoding %s: %v", body, uerr)
	}
	if decoded["room_id"] != "!room:server" {
		t.Errorf("the Extra keys were dropped: %s", body)
	}
}
