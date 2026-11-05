package unit

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	"github.com/enimatek/lattice/internal/matrixjson"
)

// newTestKeys generates an ed25519 key pair for signing tests.
func newTestKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("cannot generate key: %v", err)
	}
	return pub, priv
}

// TestSignJSONRoundTrip signs an object and verifies it with the published
// verification key, checking the excluded members rule.
func TestSignJSONRoundTrip(t *testing.T) {
	t.Parallel()
	pub, priv := newTestKeys(t)

	obj := map[string]any{
		"name": "example.org",
		"signing_keys": map[string]any{
			"ed25519:1": matrixjson.EncodeUnpadded(pub),
		},
		"unsigned": map[string]any{"age_ts": int64(922834800000)},
	}
	if err := matrixjson.SignJSONInPlace("example.org", "ed25519:1", priv, obj); err != nil {
		t.Fatalf("SignJSONInPlace failed: %v", err)
	}

	sigs, ok := obj["signatures"].(map[string]any)
	if !ok {
		t.Fatalf("signatures member missing or wrong type: %T", obj["signatures"])
	}
	origin, ok := sigs["example.org"].(map[string]any)
	if !ok {
		t.Fatalf("signature for origin missing: %v", sigs)
	}
	if _, ok := origin["ed25519:1"].(string); !ok {
		t.Fatalf("signature for key id missing: %v", origin)
	}
	// The unsigned member must survive signing.
	if _, ok := obj["unsigned"].(map[string]any); !ok {
		t.Fatalf("unsigned member was dropped by signing")
	}

	res, err := matrixjson.VerifyJSON(obj, func(originName, keyID string) (ed25519.PublicKey, error) {
		if originName != "example.org" || keyID != "ed25519:1" {
			return nil, errNoKey
		}
		return pub, nil
	})
	if err != nil {
		t.Fatalf("VerifyJSON failed: %v", err)
	}
	if !res.VerifiedAll([]string{"example.org"}) {
		t.Fatalf("expected origin to verify, got %+v", res)
	}
}

// TestSignJSONExcludesSignaturesAndUnsigned proves the signature covers the
// object without "signatures" and "unsigned", as the specification requires:
// changing "unsigned" must not invalidate the signature.
func TestSignJSONExcludesSignaturesAndUnsigned(t *testing.T) {
	t.Parallel()
	pub, priv := newTestKeys(t)
	obj := map[string]any{
		"content":    map[string]any{"body": "hello"},
		"unsigned":   map[string]any{"age_ts": int64(1)},
		"signatures": map[string]any{},
	}
	if err := matrixjson.SignJSONInPlace("hs", "ed25519:v1", priv, obj); err != nil {
		t.Fatalf("sign failed: %v", err)
	}
	obj["unsigned"] = map[string]any{"age_ts": int64(999), "extra": "added later"}

	res, err := matrixjson.VerifyJSON(obj, func(string, string) (ed25519.PublicKey, error) { return pub, nil })
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if !res.VerifiedAll([]string{"hs"}) {
		t.Fatalf("signature should still be valid after changing unsigned: %+v", res)
	}
}

// TestVerifyJSONFailures covers the rejection paths of the seven-step procedure.
func TestVerifyJSONFailures(t *testing.T) {
	t.Parallel()
	pub, priv := newTestKeys(t)
	otherPub, _ := newTestKeys(t)

	cases := []struct {
		name   string
		mutate func(obj map[string]any)
		lookup matrixjson.VerifyKeyFunc
	}{
		{
			name:   "tampered content",
			mutate: func(obj map[string]any) { obj["content"] = map[string]any{"body": "changed"} },
			lookup: func(string, string) (ed25519.PublicKey, error) { return pub, nil },
		},
		{
			name:   "unknown algorithm only",
			mutate: func(obj map[string]any) { sigs(obj)["hs"] = map[string]any{"rsa:1": "AAAA"} },
			lookup: func(string, string) (ed25519.PublicKey, error) { return pub, nil },
		},
		{
			name:   "wrong verification key",
			mutate: func(map[string]any) {},
			lookup: func(string, string) (ed25519.PublicKey, error) { return otherPub, nil },
		},
		{
			name:   "signature not base64",
			mutate: func(obj map[string]any) { sigs(obj)["hs"] = map[string]any{"ed25519:v1": "not base64!!"} },
			lookup: func(string, string) (ed25519.PublicKey, error) { return pub, nil },
		},
		{
			name:   "no key available",
			mutate: func(map[string]any) {},
			lookup: func(string, string) (ed25519.PublicKey, error) { return nil, errNoKey },
		},
		{
			name:   "no signatures member",
			mutate: func(obj map[string]any) { delete(obj, "signatures") },
			lookup: func(string, string) (ed25519.PublicKey, error) { return pub, nil },
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obj := map[string]any{
				"content":    map[string]any{"body": "hello"},
				"signatures": map[string]any{},
			}
			if err := matrixjson.SignJSONInPlace("hs", "ed25519:v1", priv, obj); err != nil {
				t.Fatalf("sign failed: %v", err)
			}
			tc.mutate(obj)
			res, err := matrixjson.VerifyJSON(obj, tc.lookup)
			if err != nil {
				// A refusal to even attempt verification is also a rejection.
				return
			}
			if res.VerifiedAll([]string{"hs"}) {
				t.Fatalf("expected verification to fail, but it succeeded: %+v", res)
			}
		})
	}
}

// TestVerifyJSONMultipleSignatures checks that an origin counts as verified when
// any of its signatures validates, which is how the specification resolves
// multiple signatures (v1.19 clarification).
func TestVerifyJSONMultipleSignatures(t *testing.T) {
	t.Parallel()
	pub, priv := newTestKeys(t)
	_, otherPriv := newTestKeys(t)

	obj := map[string]any{"content": map[string]any{"body": "x"}, "signatures": map[string]any{}}
	if err := matrixjson.SignJSONInPlace("hs", "ed25519:v2", otherPriv, obj); err != nil {
		t.Fatalf("sign with old key failed: %v", err)
	}
	if err := matrixjson.SignJSONInPlace("hs", "ed25519:v1", priv, obj); err != nil {
		t.Fatalf("sign with current key failed: %v", err)
	}

	res, err := matrixjson.VerifyJSON(obj, func(_, keyID string) (ed25519.PublicKey, error) {
		if keyID == "ed25519:v1" {
			return pub, nil
		}
		return nil, errNoKey
	})
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if !res.VerifiedAll([]string{"hs"}) {
		t.Fatalf("expected the valid signature to be accepted: %+v", res)
	}
}

// TestUnpaddedBase64 checks the encoding rules from the appendices: output has no
// padding, input may carry it.
func TestUnpaddedBase64(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input []byte
		want  string
	}{
		{"empty", []byte(""), ""},
		{"f", []byte("f"), "Zg"},
		{"fo", []byte("fo"), "Zm8"},
		{"foo", []byte("foo"), "Zm9v"},
		{"foob", []byte("foob"), "Zm9vYg"},
		{"fooba", []byte("fooba"), "Zm9vYmE"},
		{"foobar", []byte("foobar"), "Zm9vYmFy"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := matrixjson.EncodeUnpadded(tc.input); got != tc.want {
				t.Errorf("EncodeUnpadded(%q) = %q, want %q", tc.input, got, tc.want)
			}
			decoded, err := matrixjson.DecodeUnpadded(tc.want)
			if err != nil {
				t.Fatalf("DecodeUnpadded(%q) failed: %v", tc.want, err)
			}
			if string(decoded) != string(tc.input) {
				t.Errorf("DecodeUnpadded(%q) = %q, want %q", tc.want, decoded, tc.input)
			}
			padded := base64.StdEncoding.EncodeToString(tc.input)
			decodedPadded, err := matrixjson.DecodeUnpadded(padded)
			if err != nil {
				t.Fatalf("DecodeUnpadded of padded %q failed: %v", padded, err)
			}
			if string(decodedPadded) != string(tc.input) {
				t.Errorf("padded decode mismatch: %q", decodedPadded)
			}
		})
	}
}

// TestSignJSONCanonicalOutput checks that SignJSON returns canonical bytes: keys
// sorted and no whitespace.
func TestSignJSONCanonicalOutput(t *testing.T) {
	t.Parallel()
	_, priv := newTestKeys(t)
	obj := map[string]any{"b": int64(2), "a": "one"}
	out, err := matrixjson.SignJSON("hs", "ed25519:v1", priv, obj)
	if err != nil {
		t.Fatalf("SignJSON failed: %v", err)
	}
	if want := "{\"a\":\"one\",\"b\":2,\"signatures\":{\"hs\":{\"ed25519:v1\":\""; string(out)[:len(want)] != want {
		t.Errorf("output is not canonical: %s", out)
	}
}

func sigs(obj map[string]any) map[string]any {
	m, _ := obj["signatures"].(map[string]any)
	if m == nil {
		m = map[string]any{}
		obj["signatures"] = m
	}
	return m
}

var errNoKey = errNoKeySentinel{}

type errNoKeySentinel struct{}

func (errNoKeySentinel) Error() string { return "no key" }
