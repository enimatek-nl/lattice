// Package matrixjson implements the Matrix Canonical JSON and Signing JSON
// algorithms from the specification appendices (spec.matrix.org/v1.19/appendices).
//
// The package works on decoded value trees (map[string]any, []any, string, bool,
// nil, json.Number) rather than Go structs, because Matrix objects such as events
// are only loosely typed and must be reproduced byte-exactly.
package matrixjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxInt is the largest integer representable exactly by the canonical JSON
// grammar: (2**53)-1.
const (
	maxInt64 = int64(1)<<53 - 1
	minInt64 = -(int64(1)<<53 - 1)
)

// ErrNumberOutOfRange reports a number outside the canonical integer range.
var ErrNumberOutOfRange = errors.New("matrixjson: number outside canonical range")

// ErrNonInteger reports a non-integer number in a value that must be canonical.
var ErrNonInteger = errors.New("matrixjson: non-integer number")

// ErrInvalidUTF8 reports a string that is not valid UTF-8.
var ErrInvalidUTF8 = errors.New("matrixjson: string is not valid UTF-8")

// Decode parses JSON into a value tree, preserving numbers as json.Number so
// that canonicalisation can validate them, and rejecting trailing content.
func Decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	rest, err := io.ReadAll(dec.Buffered())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("matrixjson: trailing data after JSON value: %q", strings.TrimSpace(string(rest)))
	}
	return v, nil
}

// DecodeMap parses JSON that must be an object.
func DecodeMap(data []byte) (map[string]any, error) {
	v, err := Decode(data)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("matrixjson: expected JSON object, got %T", v)
	}
	return m, nil
}

// Canonical encodes value as Canonical JSON: the shortest UTF-8 JSON encoding
// with keys sorted lexicographically by Unicode codepoint and integers limited
// to [-(2**53)+1, (2**53)-1].
//
// Inputs that JSON permits but canonical JSON forbids (fractions, exponents that
// are not integral, values out of range) are reported as errors. Exponent and
// negative-zero forms that do denote a valid integer are normalised, as the
// specification's own examples require ({"a": -0, "b": 1e10} -> {"a":0,"b":10000000000}).
func Canonical(value any) ([]byte, error) {
	var b strings.Builder
	if err := encode(&b, value); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// CanonicalRaw decodes data and re-encodes it as Canonical JSON.
func CanonicalRaw(data []byte) ([]byte, error) {
	v, err := Decode(data)
	if err != nil {
		return nil, err
	}
	return Canonical(v)
}

func encode(b *strings.Builder, value any) error {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		return encodeString(b, v)
	case json.Number:
		s, err := canonicalNumber(v.String())
		if err != nil {
			return err
		}
		b.WriteString(s)
	case float64:
		s, err := canonicalFloat(v)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case float32:
		s, err := canonicalFloat(float64(v))
		if err != nil {
			return err
		}
		b.WriteString(s)
	case int:
		writeInt(b, int64(v))
	case int8:
		writeInt(b, int64(v))
	case int16:
		writeInt(b, int64(v))
	case int32:
		writeInt(b, int64(v))
	case int64:
		if v > maxInt64 || v < minInt64 {
			return fmt.Errorf("%w: %d", ErrNumberOutOfRange, v)
		}
		writeInt(b, v)
	case uint:
		if uint64(v) > uint64(maxInt64) {
			return fmt.Errorf("%w: %d", ErrNumberOutOfRange, v)
		}
		writeInt(b, int64(v))
	case uint8:
		writeInt(b, int64(v))
	case uint16:
		writeInt(b, int64(v))
	case uint32:
		writeInt(b, int64(v))
	case uint64:
		if v > uint64(maxInt64) {
			return fmt.Errorf("%w: %d", ErrNumberOutOfRange, v)
		}
		writeInt(b, int64(v))
	case json.RawMessage:
		vv, err := Decode(v)
		if err != nil {
			return err
		}
		return encode(b, vv)
	case []byte:
		vv, err := Decode(v)
		if err != nil {
			return err
		}
		return encode(b, vv)
	case map[string]any:
		return encodeMap(b, v)
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := encode(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		return fmt.Errorf("matrixjson: unsupported value type %T", value)
	}
	return nil
}

func encodeMap(b *strings.Builder, m map[string]any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Go's UTF-8 string ordering is byte-order ordering, which for UTF-8 equals
	// ordering by Unicode codepoint, as the canonical grammar requires.
	sort.Strings(keys)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := encodeString(b, k); err != nil {
			return err
		}
		b.WriteByte(':')
		if err := encode(b, m[k]); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

// encodeString writes a JSON string using exactly the escapes permitted by the
// canonical grammar.
func encodeString(b *strings.Builder, s string) error {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return fmt.Errorf("%w at byte %d", ErrInvalidUTF8, i)
		}
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
	return nil
}

func writeInt(b *strings.Builder, v int64) {
	if v == 0 {
		// Negative zero MUST NOT appear in canonical JSON.
		b.WriteString("0")
		return
	}
	b.WriteString(big.NewInt(v).String())
}

// canonicalNumber normalises a JSON number token to canonical integer form.
func canonicalNumber(s string) (string, error) {
	if s == "" || strings.ContainsAny(s, "_") {
		return "", fmt.Errorf("matrixjson: invalid number %q", s)
	}
	if isPlainInteger(s) {
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return "", fmt.Errorf("matrixjson: invalid number %q", s)
		}
		return ratToCanonical(r)
	}
	// Exponent or fraction form: only legal if it denotes an exact integer in range.
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", fmt.Errorf("matrixjson: invalid number %q", s)
	}
	if r.Denom().Cmp(big.NewInt(1)) != 0 {
		return "", fmt.Errorf("%w: %s", ErrNonInteger, s)
	}
	return ratToCanonical(r)
}

func canonicalFloat(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("matrixjson: NaN or Inf is not permitted")
	}
	r := new(big.Rat).SetFloat64(f)
	if r == nil {
		return "", fmt.Errorf("%w: %v", ErrNumberOutOfRange, f)
	}
	if r.Denom().Cmp(big.NewInt(1)) != 0 {
		return "", fmt.Errorf("%w: %v", ErrNonInteger, f)
	}
	return ratToCanonical(r)
}

func ratToCanonical(r *big.Rat) (string, error) {
	num := r.Num()
	den := r.Denom()
	if den.Cmp(big.NewInt(1)) != 0 {
		return "", fmt.Errorf("%w: %s", ErrNonInteger, r.RatString())
	}
	if !num.IsInt64() {
		return "", fmt.Errorf("%w: %s", ErrNumberOutOfRange, r.RatString())
	}
	v := num.Int64()
	if v > maxInt64 || v < minInt64 {
		return "", fmt.Errorf("%w: %d", ErrNumberOutOfRange, v)
	}
	return num.String(), nil
}

func isPlainInteger(s string) bool {
	body := s
	if strings.HasPrefix(body, "-") || strings.HasPrefix(body, "+") {
		body = body[1:]
	}
	if body == "" {
		return false
	}
	for i := 0; i < len(body); i++ {
		if body[i] < '0' || body[i] > '9' {
			return false
		}
	}
	return true
}
