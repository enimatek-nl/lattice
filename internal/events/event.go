// Package events implements the parts of the Matrix event (PDU) format that must
// be byte-exact: the content hash, the reference hash, the event ID derived from
// it, and the signature that keeps working after a redaction.
//
// Two rules from the specification drive the design:
//
//   - The reference hash is taken over the *redacted* event, so event IDs in room
//     versions 3 and later depend on the room version's redaction rules.
//   - The signature is taken over the redacted event as well, which is what lets
//     a redacted event stay verifiable.
//
// Events are kept as decoded JSON objects rather than Go structs. Matrix events
// are open documents: they carry arbitrary top-level and content keys, the set of
// required keys depends on the room version and event type, and the bytes that go
// into the hash must be the document's canonical form — not a struct's field
// list. Modelling an event as a struct here would silently drop exactly the
// fields that hashes cover.
package events

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/redaction"
	"github.com/enimatek/lattice/internal/roomversion"
)

// transportKeys are properties that a receiving server must not include when
// hashing or signing an event. Synapse and gomatrixserverlib strip them on
// receipt too, so keeping them would make the same event hash differently on two
// servers. `event_id` is included because in hash-ID room versions the identifier
// is derived from the event, not part of what is hashed.
var transportKeys = []string{"outlier", "destinations", "age_ts", "unsigned"}

// Signer provides the server's signing key without exposing it.
type Signer interface {
	// Sign returns the ed25519 signature of data.
	Sign(data []byte) []byte
	// KeyID is the signing key identifier, e.g. "ed25519:a1b2".
	KeyID() string
}

// Parse decodes event JSON into the representation this package uses. Numbers are
// decoded as json.Number so integers survive re-encoding: turning origin_server_ts
// into a float and writing it back is one of the classic ways to break a hash.
func Parse(data []byte) (map[string]any, error) {
	event, err := matrixjson.DecodeMap(data)
	if err != nil {
		return nil, fmt.Errorf("events: cannot parse event: %w", err)
	}
	if _, ok := event["type"]; !ok {
		return nil, errors.New("events: event has no type")
	}
	return event, nil
}

// Canonical returns the canonical JSON encoding of an event, which is what other
// servers hash and what must be stored if storage wants a stable form.
func Canonical(event map[string]any) ([]byte, error) {
	return matrixjson.Canonical(event)
}

// ContentHash returns the event's content hash in unpadded base64: the hash of the
// whole event with `hashes`, `signatures` and `unsigned` removed. Verification of
// an event always starts here, because it covers the unredacted content.
func ContentHash(rules roomversion.Rules, event map[string]any) (string, error) {
	hashable, err := matrixjson.Canonical(hashableForContent(rules, event))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(hashable)
	return matrixjson.EncodeUnpadded(sum[:]), nil
}

// ReferenceHash returns the raw reference hash of an event for a room version.
func ReferenceHash(rules roomversion.Rules, event map[string]any) ([]byte, error) {
	hashable, err := matrixjson.Canonical(hashableForReference(rules, event))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(hashable)
	return sum[:], nil
}

// EventID returns the identifier an event has in the given room version. For room
// versions 1 and 2 the identifier is assigned by the origin server, so it is read
// from the event instead of derived; deriving it there would invent an identifier
// the origin server never issued.
func EventID(rules roomversion.Rules, event map[string]any) (string, error) {
	switch rules.EventIDs {
	case roomversion.EventIDServerAssigned:
		id, ok := event["event_id"].(string)
		if !ok || id == "" {
			return "", errors.New("events: room versions 1 and 2 require an explicit event_id")
		}
		return id, nil
	default:
		sum, err := ReferenceHash(rules, event)
		if err != nil {
			return "", err
		}
		encoder := base64.RawURLEncoding
		if rules.EventIDs == roomversion.EventIDHashStdBase64 {
			encoder = base64.RawStdEncoding
		}
		return "$" + encoder.EncodeToString(sum), nil
	}
}

// Seal adds the content hash and the origin server's signature to an event in
// place, in the order the specification requires: hash first, because the hash is
// part of what gets signed, then the signature over the redacted form.
//
// The signature deliberately covers the redacted event. Signing the full event
// instead would mean that every redaction destroys the signature, and half the
// point of redaction is that hashes and signatures keep working afterwards.
func Seal(origin string, rules roomversion.Rules, event map[string]any, signer Signer) error {
	if origin == "" {
		return errors.New("events: signing an event needs the origin server name")
	}
	if signer == nil {
		return errors.New("events: signing an event needs a signer")
	}

	contentHash, err := ContentHash(rules, event)
	if err != nil {
		return fmt.Errorf("events: cannot compute content hash: %w", err)
	}
	hashes, _ := event["hashes"].(map[string]any)
	if hashes == nil {
		hashes = map[string]any{}
	}
	hashes["sha256"] = contentHash
	event["hashes"] = hashes

	signed, err := matrixjson.Canonical(hashableForSignature(rules, event))
	if err != nil {
		return fmt.Errorf("events: cannot encode event for signing: %w", err)
	}
	signature := matrixjson.EncodeUnpadded(signer.Sign(signed))

	signatureMap := signaturesMap(event)
	byOrigin, _ := signatureMap[origin].(map[string]any)
	if byOrigin == nil {
		byOrigin = map[string]any{}
	}
	byOrigin[signer.KeyID()] = signature
	signatureMap[origin] = byOrigin
	event["signatures"] = signatureMap
	return nil
}

// Redacted returns the redacted form of an event for a room version.
func Redacted(rules roomversion.Rules, event map[string]any) map[string]any {
	return redaction.Apply(rules, event)
}

// hashableForContent strips what the content hash excludes.
func hashableForContent(rules roomversion.Rules, event map[string]any) map[string]any {
	out := stripTransportKeys(rules, event)
	delete(out, "signatures")
	delete(out, "hashes")
	return out
}

// hashableForReference redacts the event, then removes what the reference hash
// excludes. The `hashes` member stays: it is precisely what makes a redacted
// event still checkable against the content hash recorded before redaction.
func hashableForReference(rules roomversion.Rules, event map[string]any) map[string]any {
	out := redaction.Apply(rules, stripTransportKeys(rules, event))
	delete(out, "signatures")
	return out
}

// hashableForSignature is the redacted form that gets signed.
func hashableForSignature(rules roomversion.Rules, event map[string]any) map[string]any {
	return hashableForReference(rules, event)
}

// stripTransportKeys removes properties that are not part of the signed/hashed
// document. event_id is only part of the document in room versions 1 and 2, where
// the identifier is assigned rather than derived; in hash-ID versions keeping it
// would make the hash depend on its own result.
func stripTransportKeys(rules roomversion.Rules, event map[string]any) map[string]any {
	out := make(map[string]any, len(event))
	for key, value := range event {
		out[key] = value
	}
	for _, key := range transportKeys {
		delete(out, key)
	}
	if rules.EventIDs != roomversion.EventIDServerAssigned {
		delete(out, "event_id")
	}
	return out
}

// signaturesMap returns the event's signatures object, creating it if needed.
func signaturesMap(event map[string]any) map[string]any {
	if existing, ok := event["signatures"].(map[string]any); ok {
		return existing
	}
	return map[string]any{}
}
