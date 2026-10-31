package matrix

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

// TestCanonicalJSON checks the encoding rules the signing specification spells
// out: keys sorted, whitespace dropped, escapes limited.
func TestCanonicalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty object", `{}`, `{}`},
		{"empty array", `[ ]`, `[]`},
		{"sorted keys", `{ "b": [ ] , "a": { } }`, `{"a":{},"b":[]}`},
		{"last key wins on duplicates", `{ "b": "a", "b": "c" }`, `{"b":"c"}`},
		{"spaces inside keys are data", `{ " b " : "a"}`, `{" b ":"a"}`},
		{"nested objects", `{"a":{"c":1,"b":[1,2,{"z":null,"y":true}]}}`, `{"a":{"b":[1,2,{"y":true,"z":null}],"c":1}}`},
		{"numbers keep their spelling", `{"n":1.5000000000000000001,"m":-0.0000000000001}`,
			`{"m":-0.0000000000001,"n":1.5000000000000000001}`},
		{"integer and exponent forms", `{"a":1e3,"b":1000}`, `{"a":1e3,"b":1000}`},
		{"quotes and backslashes", `{"a":"\"x\\y"}`, `{"a":"\"x\\y"}`},
		{"non-ascii is written as it is", `{"é":"日🌍"}`, `{"é":"日🌍"}`},
		{"forward slashes need no escape", `{"a":"http://matrix.org"}`, `{"a":"http://matrix.org"}`},
		{"and the ampersand", `{"a":"<b>&c"}`, `{"a":"<b>&c"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value, err := DecodeCanonical([]byte(tc.in))
			if err != nil {
				t.Fatalf("decoding %s: %v", tc.in, err)
			}
			got, err := CanonicalJSON(value)
			if err != nil {
				t.Fatalf("encoding %s: %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Errorf("canonical form of %s = %s, want %s", tc.in, got, tc.want)
			}
			// The encoding is stable: encoding it again changes nothing.
			again, err := DecodeCanonical(got)
			if err != nil {
				t.Fatalf("decoding the canonical form: %v", err)
			}
			second, err := CanonicalJSON(again)
			if err != nil {
				t.Fatalf("re-encoding: %v", err)
			}
			if string(second) != string(got) {
				t.Errorf("the canonical form is not stable: %s then %s", got, second)
			}
		})
	}
}

// TestCanonicalEscaping checks the small escape set, including the control
// characters that have to come back as \u00xx.
func TestCanonicalEscaping(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"plain", `"plain"`},
		{"tab", `"tab\tmore"`},
		{"newline", `"a\nb"`},
		{"\x00 and \x1f", "\"\x00\x1f\""},
		{`"quote"`, `"\"quote\""`},
		{`back\slash`, `"back\\slash"`},
		{"café", `"café"`},
	}
	for _, tc := range tests {
		got := EncodeCanonicalString(tc.in)
		// The escaping has to stay decodable.
		if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
			t.Errorf("escaped %q = %s", tc.in, got)
		}
		for _, r := range tc.in {
			if r >= 0x20 && r != '"' && r != '\\' && !strings.Contains(got, string(r)) {
				t.Errorf("escaped %q lost %q: %s", tc.in, r, got)
			}
		}
	}
	if got := EncodeCanonicalString("\x00\x1f"); got != "\"\\u0000\\u001f\"" {
		t.Errorf("control characters escaped as %s", got)
	}
}

// TestCanonicalIgnoresGoTypes checks that a value assembled in Go and the same
// value decoded from JSON encode to the same bytes. A signature is only
// verifiable if the encoder does not care how the value arrived: the client builds
// its objects in memory and the server reads them back from JSON.
func TestCanonicalIgnoresGoTypes(t *testing.T) {
	pairs := []struct {
		name    string
		built   any
		decoded string
	}{
		{"list of strings", []string{"m.megolm.v1.aes-sha2", "olm"}, `["m.megolm.v1.aes-sha2","olm"]`},
		{"empty list", []string{}, `[]`},
		{"object with a list", Object{"b": "2", "a": []string{"x"}}, `{"a":["x"],"b":"2"}`},
		{"empty object", Object{}, `{}`},
	}
	for _, tc := range pairs {
		built, err := CanonicalJSON(tc.built)
		if err != nil {
			t.Fatalf("encoding the built %s: %v", tc.name, err)
		}
		same, err := DecodeCanonical([]byte(tc.decoded))
		if err != nil {
			t.Fatalf("decoding %s: %v", tc.decoded, err)
		}
		fromJSON, err := CanonicalJSON(same)
		if err != nil {
			t.Fatalf("encoding the decoded %s: %v", tc.name, err)
		}
		if string(built) != string(fromJSON) {
			t.Errorf("the %s encoded as %s from Go but %s from JSON", tc.name, built, fromJSON)
		}
	}
}

// TestCanonicalRejectsRubbish checks the errors rather than the output.
func TestCanonicalRejectsRubbish(t *testing.T) {
	if _, err := DecodeCanonical([]byte(`{"a":`)); err == nil {
		t.Error("truncated JSON decoded without an error")
	}
	deep := strings.Repeat("[", canonicalDepthLimit+2) + strings.Repeat("]", canonicalDepthLimit+2)
	value, err := DecodeCanonical([]byte(deep))
	if err != nil {
		t.Fatalf("decoding the deep document: %v", err)
	}
	if _, err := CanonicalJSON(value); err == nil {
		t.Error("a deeply nested document encoded without an error")
	}
	if _, err := CanonicalJSON(make(chan int)); err == nil {
		t.Error("a channel encoded without an error")
	}
}

// TestEd25519Signatures round trips a signature through the same code path a
// client takes: sign the canonical form, then verify it.
func TestEd25519Signatures(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	user := "@alice:test.server"
	keyID := Ed25519KeyID(public)
	if !strings.HasPrefix(keyID, "ed25519:") {
		t.Errorf("key id = %s", keyID)
	}

	object := Object{
		"user_id": user,
		"usage":   []string{"master"},
		"keys":    Object{keyID: base64.RawStdEncoding.EncodeToString(public)},
	}
	signature, err := SignEd25519(private, object)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	object["signatures"] = Object{user: Object{keyID: base64.RawStdEncoding.EncodeToString(signature)}}
	// The unsigned section is never signed, so its presence cannot break the
	// signature.
	object["unsigned"] = Object{"anything": "at all"}

	if err := VerifyEd25519Signature(object, user, keyID, public); err != nil {
		t.Fatalf("verifying a fresh signature: %v", err)
	}

	t.Run("a changed field breaks it", func(t *testing.T) {
		tampered := Object{}
		for key, value := range object {
			tampered[key] = value
		}
		tampered["usage"] = []string{"self-signing"}
		if err := VerifyEd25519Signature(tampered, user, keyID, public); err == nil {
			t.Error("a tampered object verified")
		}
	})
	t.Run("the wrong key breaks it", func(t *testing.T) {
		other, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generating the other key: %v", err)
		}
		if err := VerifyEd25519Signature(object, user, Ed25519KeyID(other), other); err == nil {
			t.Error("a signature verified against a key it was not made for")
		}
	})
	t.Run("a missing signature is reported", func(t *testing.T) {
		unsigned := Object{"user_id": user}
		if err := VerifyEd25519Signature(unsigned, user, keyID, public); err == nil {
			t.Error("an object without signatures verified")
		}
	})
}

// TestDecodeEd25519Key covers the two base64 spellings clients use.
func TestDecodeEd25519Key(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	decoded, err := DecodeEd25519Key(base64.RawStdEncoding.EncodeToString(public))
	if err != nil {
		t.Fatalf("decoding the unpadded key: %v", err)
	}
	if !decoded.Equal(public) {
		t.Error("the unpadded key decoded to something else")
	}
	padded := base64.StdEncoding.EncodeToString(public)
	if _, err := DecodeEd25519Key(padded); err != nil {
		t.Errorf("decoding the padded key %s: %v", padded, err)
	}
	for _, bad := range []string{"", "not base64 !!", base64.RawStdEncoding.EncodeToString([]byte("short"))} {
		if _, err := DecodeEd25519Key(bad); err == nil {
			t.Errorf("decoding %q should fail", bad)
		}
	}
}
