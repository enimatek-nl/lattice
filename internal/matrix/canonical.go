package matrix

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Canonical JSON is the encoding the Matrix signing specification defines for
// the bytes a signature is made over: UTF-8, object keys sorted by code point,
// no insignificant whitespace, and only the mandatory escapes. Two
// implementations that disagree about any of that cannot verify each other's
// signatures, so Lattice builds the encoding itself instead of trusting the
// layout a client happened to send.

// DecodeCanonical decodes a JSON document into Go values, keeping numbers
// exactly as they were written. Re-encoding a document is only faithful while
// the numbers survive the round trip, which encoding/json does not promise for
// float64.
func DecodeCanonical(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}
	return value, nil
}

// CanonicalJSON encodes a decoded JSON value the way the signing specification
// asks for.
func CanonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := encodeCanonical(&out, value, 0); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// canonicalDepthLimit bounds the recursion so a crafted document cannot exhaust
// the stack. The specification's own nesting is shallow.
const canonicalDepthLimit = 100

func encodeCanonical(out *bytes.Buffer, value any, depth int) error {
	if depth > canonicalDepthLimit {
		return fmt.Errorf("JSON value nested deeper than %d levels", canonicalDepthLimit)
	}
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case json.Number:
		if typed == "" {
			return fmt.Errorf("empty number in JSON value")
		}
		out.WriteString(typed.String())
	case int:
		out.WriteString(strconv.Itoa(typed))
	case int64:
		out.WriteString(strconv.FormatInt(typed, 10))
	case uint64:
		out.WriteString(strconv.FormatUint(typed, 10))
	case float64:
		// Canonical JSON writes a number with no exponent and no meaningless
		// fraction. Whole numbers inside the range int64 models exactly go back as
		// integers, because that is how a sender wrote a timestamp or a power level
		// and writing 1600000000000 as "1600000000000.0" would change the bytes a
		// signature was made over.
		//
		// A fraction is not refused: a homeserver has to be able to re-encode an
		// object it received without touching its digits, and an event content
		// holding 1.5 is legal JSON even though no Matrix field requires it. The
		// shortest form that round-trips is written without an exponent, which is
		// the only spelling canonical JSON allows.
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return fmt.Errorf("cannot canonically encode %v", typed)
		}
		if typed == math.Trunc(typed) && typed <= 1<<53 && typed >= -(1<<53) {
			out.WriteString(strconv.FormatInt(int64(typed), 10))
			break
		}
		out.WriteString(strconv.FormatFloat(typed, 'f', -1, 64))
	case string:
		encodeCanonicalString(out, typed)
	case map[string]any:
		return encodeCanonicalMap(out, typed, depth)
	case []any:
		out.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := encodeCanonical(out, item, depth+1); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case []string:
		out.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				out.WriteByte(',')
			}
			encodeCanonicalString(out, item)
		}
		out.WriteByte(']')
	default:
		return fmt.Errorf("cannot canonically encode a %T", value)
	}
	return nil
}

func encodeCanonicalMap(out *bytes.Buffer, value map[string]any, depth int) error {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	// Sorting Go strings compares their bytes, which for UTF-8 is the same order
	// as comparing code points.
	sort.Strings(keys)
	out.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		encodeCanonicalString(out, key)
		out.WriteByte(':')
		if err := encodeCanonical(out, value[key], depth+1); err != nil {
			return err
		}
	}
	out.WriteByte('}')
	return nil
}

// encodeCanonicalString writes a string with the smallest set of escapes the
// specification allows: the quote and the backslash, and the control characters
// below U+0020. Everything else, including non-ASCII text, is written as it is.
func encodeCanonicalString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
}

// EncodeCanonicalString escapes a string the way canonical JSON does. It is
// exported for the tests of that escaping.
func EncodeCanonicalString(value string) string {
	var out bytes.Buffer
	encodeCanonicalString(&out, value)
	return out.String()
}

// SignatureKeyID separates a signature algorithm from the key it names, as in
// "ed25519:twQPtGZfBd".
func SignatureKeyID(algorithm, keyID string) string {
	return algorithm + ":" + keyID
}

// Ed25519KeyID returns the id an ed25519 public key is published under.
func Ed25519KeyID(publicKey ed25519.PublicKey) string {
	return SignatureKeyID("ed25519", base64.RawStdEncoding.EncodeToString(publicKey))
}

// DecodeEd25519Key reads the base64 form of an ed25519 key. Clients write the
// unpadded form; a padded one is accepted too, because plenty of JSON libraries
// produce it.
func DecodeEd25519Key(value string) (ed25519.PublicKey, error) {
	if value == "" {
		return nil, fmt.Errorf("no key to decode")
	}
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("decoding base64: %w", err)
		}
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("expected %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// DecodeSignature reads a base64 signature.
func DecodeSignature(value string) ([]byte, error) {
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("decoding base64: %w", err)
		}
	}
	if len(raw) != ed25519.SignatureSize {
		return nil, fmt.Errorf("expected %d bytes, got %d", ed25519.SignatureSize, len(raw))
	}
	return raw, nil
}

// SignEd25519 signs the canonical form of an object, which is what a test needs
// to build the key material a real client would send.
func SignEd25519(privateKey ed25519.PrivateKey, object Object) ([]byte, error) {
	canonical, err := CanonicalJSON(objectWithoutSignatures(object))
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(privateKey, canonical), nil
}

// VerifyEd25519Signature checks that the object carries a valid signature by
// signer under keyID, made over the canonical form of the object with the
// signatures and unsigned sections removed.
func VerifyEd25519Signature(object Object, signer, keyID string, publicKey ed25519.PublicKey) error {
	signatures, ok := object["signatures"].(Object)
	if !ok {
		return fmt.Errorf("the object has no signatures")
	}
	byKey, ok := signatures[signer].(Object)
	if !ok {
		return fmt.Errorf("the object carries no signature by %s", signer)
	}
	value, ok := byKey[keyID].(string)
	if !ok {
		return fmt.Errorf("the object carries no signature under %s from %s", keyID, signer)
	}
	signature, err := DecodeSignature(value)
	if err != nil {
		return err
	}
	canonical, err := CanonicalJSON(objectWithoutSignatures(object))
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return fmt.Errorf("the signature under %s from %s does not match its key", keyID, signer)
	}
	return nil
}

// objectWithoutSignatures returns a copy of the object without the fields that
// are never signed.
func objectWithoutSignatures(object Object) Object {
	out := make(Object, len(object))
	for key, value := range object {
		if key == "signatures" || key == "unsigned" {
			continue
		}
		out[key] = value
	}
	return out
}
