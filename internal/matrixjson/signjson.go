package matrixjson

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"sort"
)

// Algorithm is the signing algorithm name used in signing key identifiers.
const Algorithm = "ed25519"

// KeyID builds a signing key identifier from an algorithm and a key version.
func KeyID(algorithm, version string) string {
	return algorithm + ":" + version
}

// Encoding helpers for the "unpadded Base64" used throughout Matrix.
// Decoding accepts padded input as the specification permits.

// EncodeUnpadded base64-encodes data without padding.
func EncodeUnpadded(data []byte) string {
	return base64.StdEncoding.WithPadding(base64.NoPadding).EncodeToString(data)
}

// DecodeUnpadded base64-decodes a value that may or may not carry padding.
func DecodeUnpadded(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.WithPadding(base64.NoPadding).DecodeString(s); err == nil {
		return b, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("matrixjson: invalid base64 %q: %w", s, err)
	}
	return b, nil
}

// SignJSONInPlace signs obj under the name/keyID pair and stores the signature
// in obj["signatures"][name][algorithm:keyID]. The "unsigned" member and any
// existing signatures are excluded from the signed bytes, as the specification
// requires. obj is modified in place.
func SignJSONInPlace(name, keyID string, priv ed25519.PrivateKey, obj map[string]any) error {
	sigs := signaturesOf(obj)

	unsigned, hasUnsigned := obj["unsigned"]
	delete(obj, "signatures")
	delete(obj, "unsigned")

	canonical, err := Canonical(obj)
	if err != nil {
		return fmt.Errorf("matrixjson: cannot canonicalise for signing: %w", err)
	}
	signature := EncodeUnpadded(ed25519.Sign(priv, canonical))

	sigs[name] = map[string]any{keyID: signature}
	obj["signatures"] = sigs
	if hasUnsigned {
		obj["unsigned"] = unsigned
	}
	return nil
}

// SignJSON returns obj, signed, encoded as Canonical JSON.
func SignJSON(name, keyID string, priv ed25519.PrivateKey, obj map[string]any) ([]byte, error) {
	if err := SignJSONInPlace(name, keyID, priv, obj); err != nil {
		return nil, err
	}
	return Canonical(obj)
}

// VerifyKeyFunc resolves a verification key for an origin and signing key
// identifier. Returning an error means the key is unknown or unusable.
type VerifyKeyFunc func(origin, keyID string) (ed25519.PublicKey, error)

// VerifyResult reports which origins produced a valid signature.
type VerifyResult struct {
	// Verified lists origins with at least one valid signature.
	Verified []string
	// Failures lists per-origin reasons, for diagnostics and log lines that
	// never include secret material.
	Failures map[string]string
}

// Verified reports whether every origin in want has a valid signature.
func (r VerifyResult) VerifiedAll(want []string) bool {
	for _, origin := range want {
		found := false
		for _, v := range r.Verified {
			if v == origin {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// VerifyJSON implements the "Checking for a Signature" procedure of the
// specification: signatures with unknown algorithms are dropped, the object is
// canonicalised without "signatures" and "unsigned", and each base64 signature is
// checked against the resolved verification key. An origin counts as verified if
// any of its signatures validates.
func VerifyJSON(obj map[string]any, lookup VerifyKeyFunc) (VerifyResult, error) {
	res := VerifyResult{Failures: map[string]string{}}
	if lookup == nil {
		return res, fmt.Errorf("matrixjson: no verification key lookup supplied")
	}
	sigs, ok := obj["signatures"].(map[string]any)
	if !ok || len(sigs) == 0 {
		return res, fmt.Errorf("matrixjson: object has no signatures")
	}

	remaining := make(map[string]any, len(obj))
	for k, v := range obj {
		if k == "signatures" || k == "unsigned" {
			continue
		}
		remaining[k] = v
	}
	canonical, err := Canonical(remaining)
	if err != nil {
		return res, fmt.Errorf("matrixjson: cannot canonicalise for verification: %w", err)
	}

	origins := make([]string, 0, len(sigs))
	for origin := range sigs {
		origins = append(origins, origin)
	}
	sort.Strings(origins)

	for _, origin := range origins {
		entries, ok := sigs[origin].(map[string]any)
		if !ok {
			res.Failures[origin] = "signatures entry is not an object"
			continue
		}
		keyIDs := make([]string, 0, len(entries))
		for keyID := range entries {
			keyIDs = append(keyIDs, keyID)
		}
		sort.Strings(keyIDs)

		usable := 0
		failure := "no usable signatures"
		for _, keyID := range keyIDs {
			if parseAlg(keyID) != Algorithm {
				// Step 2: drop identifiers with unknown algorithms.
				failure = "no signatures with known algorithms"
				continue
			}
			sigStr, ok := entries[keyID].(string)
			if !ok {
				failure = "signature is not a string"
				continue
			}
			sig, err := DecodeUnpadded(sigStr)
			if err != nil {
				failure = "signature is not valid base64"
				continue
			}
			key, err := lookup(origin, keyID)
			if err != nil {
				// The lookup's own words are kept. "No verification key" could mean the key was never
				// published, that the published document has expired, or that the server holding it could not
				// be reached, and those are different things for anyone working out why a room will not
				// federate.
				failure = fmt.Sprintf("no verification key for %s: %v", keyID, err)
				continue
			}
			if len(key) != ed25519.PublicKeySize {
				failure = "verification key has the wrong size"
				continue
			}
			usable++
			if ed25519.Verify(key, canonical, sig) {
				res.Verified = append(res.Verified, origin)
				failure = ""
				break
			}
			failure = "signature does not match"
		}
		if failure != "" {
			res.Failures[origin] = failure
		}
	}
	return res, nil
}

func parseAlg(keyID string) string {
	for i := 0; i < len(keyID); i++ {
		if keyID[i] == ':' {
			return keyID[:i]
		}
	}
	return keyID
}

func signaturesOf(obj map[string]any) map[string]any {
	sigs, ok := obj["signatures"].(map[string]any)
	if !ok {
		sigs = map[string]any{}
	}
	return sigs
}
