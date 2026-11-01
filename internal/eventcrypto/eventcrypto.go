// Package eventcrypto implements the hashing, signing, event-id and redaction
// rules that Matrix rooms depend on. Every room version names the algorithms
// precisely, and every one of them is a claim about bytes: a hash computed over
// the wrong field set produces an event id that no other server will recognise,
// and a redaction that keeps one key too many leaves content visible that the
// room agreed to destroy.
//
// The functions here are pure. They take decoded JSON and return decoded JSON,
// which is what makes them testable as tables rather than through two running
// servers, and what lets the storage layer and the federation layer share one
// implementation of the rules.
package eventcrypto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The event properties that survive a redaction. Room version 11 removed three of
// them from the protected list - origin, membership and prev_state - because a
// redaction is meant to destroy content, not the history of who was allowed to do
// what.
var (
	// redactedKeys covers room versions 1 through 10.
	redactedKeysV1 = []string{
		"event_id", "type", "room_id", "sender", "state_key", "content",
		"hashes", "signatures", "depth", "prev_events", "prev_state",
		"auth_events", "origin", "origin_server_ts", "membership",
	}
	// redactedKeysV11 covers room version 11 and later.
	redactedKeysV11 = []string{
		"event_id", "type", "room_id", "sender", "state_key", "content",
		"hashes", "signatures", "depth", "prev_events", "auth_events",
		"origin_server_ts",
	}
)

// contentKeysV1 maps an event type to the content keys the redaction algorithm
// keeps in room versions 1 through 10. A type that is absent keeps nothing: its
// content becomes an empty object.
var contentKeysV1 = map[string][]string{
	matrix.TypeMember:      {"membership"},
	matrix.TypeCreate:      {"creator"},
	matrix.TypeJoinRules:   {"join_rule"},
	matrix.TypePowerLevels: {"ban", "events", "events_default", "kick", "redact", "state_default", "users", "users_default"},
	"m.room.aliases":       {"aliases"},
	matrix.TypeHistoryVis:  {"history_visibility"},
}

// contentKeysV11 maps an event type to the content keys the redaction algorithm
// keeps from room version 11 on. m.room.create keeps its whole content, which is
// what makes the room's version and its "m.federate" choice survive a redaction of
// the create event.
var contentKeysV11 = map[string][]string{
	matrix.TypeMember:      {"membership", "join_authorised_via_users_server"},
	matrix.TypeCreate:      nil, // nil means every key is kept
	matrix.TypeJoinRules:   {"join_rule", "allow"},
	matrix.TypePowerLevels: {"ban", "events", "events_default", "invite", "kick", "redact", "state_default", "users", "users_default"},
	matrix.TypeHistoryVis:  {"history_visibility"},
	matrix.TypeRedaction:   {"redacts"},
}

// UseRoom11Algorithms reports whether a room version uses the room version 11
// algorithms: the redaction rules, and the stricter canonical JSON. Room versions
// have no ordering, so this asks the version's own number, and a version the server
// does not know is treated as the newest one it does.
func UseRoom11Algorithms(roomVersion string) bool {
	number, err := strconv.Atoi(strings.TrimSpace(roomVersion))
	if err != nil {
		return false
	}
	return number >= 11
}

// roomVersionNumber reads the number a room version names itself by. Room versions are
// strings like "11"; nothing else is a version this server can reason about, and
// guessing at an unknown one would mean choosing hash rules for an event that may not
// follow them.
func roomVersionNumber(roomVersion string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(roomVersion))
	if err != nil {
		return 0, fmt.Errorf("room version %q is not a version this server can compute", roomVersion)
	}
	return parsed, nil
}

// withoutEventID returns a copy of the event with the event_id property left out.
//
// Room versions 3 and later define no such property: the id is what the reference hash
// comes out as, and "when events are sent over federation, the event_id field is no
// longer included" - a server that hashed a copy which already had one would name a
// different event, and every event it received would look forged.
// asPublished returns the event in the form the room version defines it in, which is
// the form every hash and signature of that event is made over. For room versions 3 and
// later that means leaving event_id out; for the versions before, the field is part of
// the event and stays.
func asPublished(event matrix.Object, roomVersion string) matrix.Object {
	number, err := roomVersionNumber(roomVersion)
	if err != nil || number < 3 {
		return event
	}
	return withoutEventID(event)
}

func withoutEventID(event matrix.Object) matrix.Object {
	out := make(matrix.Object, len(event))
	for key, value := range event {
		if key == "event_id" {
			continue
		}
		out[key] = value
	}
	return out
}

// Redact returns the redacted copy of an event. The original is not modified.
//
// The redaction algorithm is the one the room version names, and it is not a
// privacy nicety: the hashes and signatures of an event cover the *unredacted*
// content precisely so that a receiver can tell that the fields it is now missing
// were removed rather than tampered with. Stripping a key the version protects, or
// keeping one it destroys, changes what every other server computes.
func Redact(event matrix.Object, roomVersion string) matrix.Object {
	keys := contentKeysV1
	protected := redactedKeysV1
	if UseRoom11Algorithms(roomVersion) {
		keys = contentKeysV11
		protected = redactedKeysV11
	}
	out := matrix.Object{}
	for _, key := range protected {
		value, found := event[key]
		if !found {
			continue
		}
		out[key] = value
	}
	// The content object is stripped too, down to the keys the type allows.
	content, ok := event["content"].(matrix.Object)
	if !ok {
		if asMap, mapOK := event["content"].(map[string]any); mapOK {
			content = asMap
		}
	}
	eventType, _ := event["type"].(string)
	allowed, typeKnown := keys[eventType]
	if content == nil {
		// An event with no content object at all gets an empty one: the field is
		// required in every room version.
		if typeKnown {
			out["content"] = matrix.Object{}
		}
		return out
	}
	if !typeKnown {
		out["content"] = matrix.Object{}
		return out
	}
	if allowed == nil {
		// Every key is kept (m.room.create from room version 11 on).
		out["content"] = content
		return out
	}
	kept := matrix.Object{}
	for _, key := range allowed {
		if value, found := content[key]; found {
			kept[key] = value
		}
	}
	// A membership event keeps its third-party invite signature, because the
	// invite is what authorises the membership and destroying it would make the
	// event unverifiable.
	if eventType == matrix.TypeMember {
		if thirdParty, ok := content["third_party_invite"].(matrix.Object); ok {
			if signed, hasSigned := thirdParty["signed"]; hasSigned {
				existing, _ := kept["third_party_invite"].(matrix.Object)
				if existing == nil {
					existing = matrix.Object{}
				}
				existing["signed"] = signed
				kept["third_party_invite"] = existing
			}
		}
	}
	out["content"] = kept
	return out
}

// ContentHash hashes the complete event, including the content that a redaction
// would destroy.
//
// unsigned, signatures and hashes are removed first: the first because any server
// may add to it in transit, the other two because they depend on the hash that is
// being computed. Everything else, including fields a redaction strips, is covered,
// which is what lets a receiver prove that a redacted event was once fuller.
func ContentHash(event matrix.Object) ([32]byte, error) {
	return hashWithout(event, "unsigned", "signatures", "hashes")
}

// ReferenceHash hashes the essential parts of an event. It is what an event id is
// derived from in room versions 3 and later, and what prev_events and auth_events
// are checked against.
//
// The event goes through the redaction algorithm first, and signatures and unsigned
// are then removed: a signature depends on the hash, and unsigned data is not
// covered by anything, so neither can be part of an identifier.
func ReferenceHash(event matrix.Object, roomVersion string) ([32]byte, error) {
	event = asPublished(event, roomVersion)
	redacted := Redact(event, roomVersion)
	return hashWithout(redacted, "signatures", "unsigned")
}

// hashWithout canonical-encodes a copy of an event with the named properties
// removed, and hashes the bytes.
func hashWithout(event matrix.Object, drop ...string) ([32]byte, error) {
	var zero [32]byte
	out := make(matrix.Object, len(event))
	for key, value := range event {
		remove := false
		for _, name := range drop {
			if key == name {
				remove = true
				break
			}
		}
		if remove {
			continue
		}
		out[key] = value
	}
	canonical, err := matrix.CanonicalJSON(out)
	if err != nil {
		return zero, fmt.Errorf("canonically encoding the event: %w", err)
	}
	return sha256.Sum256(canonical), nil
}

// EventID returns the id an event has in a given room version.
//
// Room versions 1 and 2 carry an id that the origin server made up, so it is taken
// from the event itself. Room version 3 derives it from the reference hash in
// unpadded base64, and room version 4 and later use the URL-safe alphabet instead,
// which is the only difference between those two versions. Deriving an id is not
// cosmetic: two servers that compute the same hash must name the event the same
// thing, and a server that invents ids cannot be checked by anybody.
func EventID(event matrix.Object, roomVersion string) (string, error) {
	number, err := roomVersionNumber(roomVersion)
	if err != nil {
		// An unknown version is treated like the oldest one, whose ids are
		// self-declared, rather than guessed at.
		number = 1
	}
	if number < 3 {
		id, _ := event["event_id"].(string)
		if id == "" {
			return "", fmt.Errorf("room version %s requires an event_id, and this event has none", roomVersion)
		}
		return id, nil
	}
	hash, err := ReferenceHash(event, roomVersion)
	if err != nil {
		return "", err
	}
	if number == 3 {
		return "$" + base64.RawStdEncoding.EncodeToString(hash[:]), nil
	}
	return "$" + base64.RawURLEncoding.EncodeToString(hash[:]), nil
}

// HashAndSign returns the event with its content hash and its server signature
// added, following the specification's order exactly.
//
// The hash comes first, because it covers everything the event has, including the
// fields a redaction would remove. The signature is then made over the *redacted*
// copy - with the hash still inside it - so that the signature stays checkable
// after a redaction, which is the only way a receiver can tell a legitimately
// redacted event from a tampered one. The signature is copied back onto the full
// event, which is what gets sent and stored.
func HashAndSign(event matrix.Object, roomVersion string, privateKey ed25519.PrivateKey, signingName, keyID string) (matrix.Object, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("a signing key of %d bytes cannot sign events", len(privateKey))
	}
	if signingName == "" || keyID == "" {
		return nil, fmt.Errorf("signing needs a server name and a key id, got %q and %q", signingName, keyID)
	}
	// The event is first put into the form the room version defines, because that is
	// the form the hash and the signature are made over: an event that arrives with an
	// event_id on it, in a room version that does not define one, still has to come out
	// with the hash and signature its sender computed.
	out := asPublished(event, roomVersion)
	hash, err := ContentHash(out)
	if err != nil {
		return nil, err
	}
	// The hash is stored in the event before signing, because the signature covers
	// it: a signature over an event without its hash would not survive the redaction
	// the signature is meant to survive.
	out["hashes"] = matrix.Object{"sha256": base64.RawStdEncoding.EncodeToString(hash[:])}

	stripped := Redact(out, roomVersion)
	canonical, err := matrix.CanonicalJSON(withoutSignatures(stripped))
	if err != nil {
		return nil, fmt.Errorf("canonically encoding the event to sign: %w", err)
	}
	signature := ed25519.Sign(privateKey, canonical)

	signatures, _ := out["signatures"].(matrix.Object)
	if signatures == nil {
		signatures = matrix.Object{}
	}
	byKey, _ := signatures[signingName].(matrix.Object)
	if byKey == nil {
		byKey = matrix.Object{}
	}
	byKey[keyID] = base64.RawStdEncoding.EncodeToString(signature)
	signatures[signingName] = byKey
	out["signatures"] = signatures
	return out, nil
}

// withoutSignatures copies an object without the fields that are never signed. The
// event form of some room versions keeps a top-level "signatures" field only, so
// this mirrors matrix.VerifyEd25519Signature rather than inventing a third rule.
func withoutSignatures(object matrix.Object) matrix.Object {
	out := make(matrix.Object, len(object))
	for key, value := range object {
		if key == "signatures" || key == "unsigned" {
			continue
		}
		out[key] = value
	}
	return out
}

// ContentHashOf returns the content hash of an event in the room version it lives in.
//
// The difference from [ContentHash] is one property. Room versions 3 and later define
// no event_id property at all - the id is what the reference hash comes out as, and the
// id a client sees is added on the way out of the origin server - so a server re-checking
// an event it received has to leave that field out, or it hashes an event nobody signed.
// Room versions 1 and 2 carry the id, and the hash covers it.
// RedactedForm returns the event as this server would hold it after the room version's
// redaction algorithm: the published form of the event, with the properties the version
// destroys gone.
//
// A receiver keeps its own redacted copy rather than the one a peer sent, which is the
// only honest answer to a hash mismatch: the peer's copy may have had content added to
// it in transit, and the redaction is what proves which fields were there.
func RedactedForm(event matrix.Object, roomVersion string) matrix.Object {
	return Redact(asPublished(event, roomVersion), roomVersion)
}

func ContentHashOf(event matrix.Object, roomVersion string) ([32]byte, error) {
	return ContentHash(asPublished(event, roomVersion))
}

// VerifyContentHash compares the content hash an event carries with the hash of the
// event as it stands. A mismatch is reported plainly; what it means is the
// caller's decision, and [IsRedactedForm] is the tool that makes that decision
// honestly.
func VerifyContentHash(event matrix.Object, roomVersion string) error {
	encoded, err := StoredContentHash(event)
	if err != nil {
		return err
	}
	want, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		want, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("the content hash %q is not base64", encoded)
		}
	}
	hash, err := ContentHashOf(event, roomVersion)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(want, hash[:]) != 1 {
		return fmt.Errorf("the event's content hash does not match its content")
	}
	return nil
}

// IsRedactedForm reports whether an event is already in the form the room version's
// redaction algorithm would leave it in.
//
// Together with [VerifyContentHash] this answers the awkward case of receiving an
// event that was redacted in transit: its content hash covers content this server
// was never given, so the check fails although nothing is wrong. A failing hash on
// an event that is already in its redacted form is therefore tolerated - the
// sender's signature, which is made over the redacted form precisely so that it
// survives, is what proves the event genuine. Anything else is a mismatch.
func IsRedactedForm(event matrix.Object, roomVersion string) bool {
	redacted := Redact(event, roomVersion)
	if len(redacted) != len(event) {
		return false
	}
	for key, value := range redacted {
		if !sameValue(event[key], value) {
			return false
		}
	}
	return true
}

// sameValue compares two decoded JSON values by their canonical encoding, which is
// the only comparison that does not depend on how a sender spelled a number or
// ordered a key.
func sameValue(a, b any) bool {
	canonicalA, errA := matrix.CanonicalJSON(a)
	canonicalB, errB := matrix.CanonicalJSON(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(canonicalA, canonicalB)
}

// StoredContentHash reads the sha256 content hash an event carries.
func StoredContentHash(event matrix.Object) (string, error) {
	hashes, ok := event["hashes"].(matrix.Object)
	if !ok {
		if asMap, mapOK := event["hashes"].(map[string]any); mapOK {
			hashes = asMap
		} else {
			return "", fmt.Errorf("the event carries no hashes object")
		}
	}
	encoded, ok := hashes["sha256"].(string)
	if !ok || encoded == "" {
		return "", fmt.Errorf("the event carries no sha256 content hash")
	}
	return encoded, nil
}

// EventSignature checks that an event carries a valid signature by signingName
// under keyID.
//
// The signature is verified over the redacted form of the event with the signature
// fields removed, which is the form the sender signed: a server that verified over
// the event as received would reject every event that had been redacted and every
// event that another server had annotated with unsigned data.
func EventSignature(event matrix.Object, roomVersion, signingName, keyID string, publicKey ed25519.PublicKey) error {
	signatures, ok := event["signatures"].(matrix.Object)
	if !ok {
		if asMap, mapOK := event["signatures"].(map[string]any); mapOK {
			signatures = asMap
		} else {
			return fmt.Errorf("the event carries no signatures")
		}
	}
	byKey, ok := signatures[signingName].(matrix.Object)
	if !ok {
		if asMap, mapOK := signatures[signingName].(map[string]any); mapOK {
			byKey = asMap
		} else {
			return fmt.Errorf("the event carries no signature by %s", signingName)
		}
	}
	encoded, ok := byKey[keyID].(string)
	if !ok || encoded == "" {
		return fmt.Errorf("the event carries no signature under %s from %s", keyID, signingName)
	}
	signature, err := matrix.DecodeSignature(encoded)
	if err != nil {
		return err
	}
	// The signature is verified against the form the room version defines, which is
	// the form it was made over: see [HashAndSign].
	redacted := Redact(asPublished(event, roomVersion), roomVersion)
	canonical, err := matrix.CanonicalJSON(withoutSignatures(redacted))
	if err != nil {
		return fmt.Errorf("canonically encoding the event to verify: %w", err)
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return fmt.Errorf("the signature under %s from %s does not match its key", keyID, signingName)
	}
	return nil
}
