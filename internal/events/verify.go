package events

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"

	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/roomversion"
)

// InvalidEventError says an event is not a valid event in its room version, which is
// different from an event that is valid but not allowed. A receiver drops an invalid
// event; it rejects an unauthorised one; the difference decides whether later events
// that cite it can still be resolved.
type InvalidEventError struct {
	EventID string
	Reason  string
}

func (e InvalidEventError) Error() string {
	return fmt.Sprintf("event %s is not valid: %s", e.EventID, e.Reason)
}

// KeyLookup resolves a server's signing key by ID. It is the federation keyring's job
// to answer, and answering "no such key" is the correct response to a key this server
// has never seen or has good reason not to trust.
type KeyLookup func(origin, keyID string) (ed25519.PublicKey, error)

// Verify checks the two things that make an event somebody else's claim rather than
// this server's invention: that the event's ID is the hash its room version says it
// is, and that its origin server signed it.
//
// The ID check is not pedantry. An event ID is a hash of the event's redacted form, so
// a server that accepted an event whose ID did not match would be storing something
// that no other server can name; two servers that disagree about one ID fork that
// room's history permanently, and neither side can tell afterwards which fork the other
// is talking about.
//
// The signature check is over the same form the origin server signed, with transport
// keys and signatures stripped for the room version. A signature by a key this server
// does not know is a failure, not a warning: an unsigned event from a stranger is
// somebody's guess about what a room contained.
func Verify(rules roomversion.Rules, event map[string]any, lookup KeyLookup) error {
	// The name is settled before anything else is checked, because in the room versions whose
	// identifiers are hashes it is not the sender's claim at all: it is what this server computes
	// from the bytes in front of it. An event arriving without one is therefore not incomplete,
	// and one arriving with a label that disagrees is not a different event — see AssignEventID.
	eventID, err := AssignEventID(rules, event)
	if err != nil {
		return InvalidEventError{Reason: err.Error()}
	}

	if err := verifyContentHash(rules, event); err != nil {
		return InvalidEventError{EventID: eventID, Reason: err.Error()}
	}

	sender, _ := event["sender"].(string)
	origin, err := matrixid.UserDomain(sender)
	if err != nil {
		return InvalidEventError{EventID: eventID, Reason: fmt.Sprintf("its sender %q has no server name", sender)}
	}
	if err := verifyOriginSignature(rules, event, origin, lookup); err != nil {
		return InvalidEventError{EventID: eventID, Reason: err.Error()}
	}
	return nil
}

// verifyContentHash checks the `hashes` the origin server recorded.
//
// It is not a duplicate of the event ID check. In the room versions whose identifiers
// are hashes, the identifier is the hash of the *redacted* form, because a redaction
// must not change what an event is called; the content hash is the hash of the
// unredacted form, and it is therefore the only thing that makes the parts a redaction
// would remove — the body of a message, for instance — part of the event's identity.
// Skipping it would let a homeserver change a message's text and still present it as the
// same, signed, correctly identified event.
//
// This check is unconditional, and that is a decision rather than an oversight. The
// specification applies the redaction strip "upon receipt of a redaction event" — each
// server strips the events in its own database — so the form that crosses the wire is the
// unredacted one, together with the redaction event itself. A server that forwarded an
// already-stripped form would be refusing to say what was destroyed and by whom, and this
// server would be right to refuse it. gomatrixserverlib checks the content hash the same
// way, with no exception for redacted events.
func verifyContentHash(rules roomversion.Rules, event map[string]any) error {
	hashes, ok := event["hashes"].(map[string]any)
	if !ok || len(hashes) == 0 {
		return errors.New("it carries no content hash, so its content cannot be checked")
	}
	computed, err := ContentHash(rules, event)
	if err != nil {
		return fmt.Errorf("its content cannot be hashed: %w", err)
	}
	for algorithm, raw := range hashes {
		value, _ := raw.(string)
		if value == "" {
			return fmt.Errorf("its %s content hash is empty", algorithm)
		}
		if _, err := matrixjson.DecodeUnpadded(value); err != nil {
			return fmt.Errorf("its %s content hash is not unpadded base64", algorithm)
		}
		if algorithm != "sha256" {
			// A hash algorithm this server does not implement is not a hash it can
			// confirm; claiming otherwise would accept an event on an algorithm nobody
			// checked.
			return fmt.Errorf("its content hash uses %s, which this server does not check", algorithm)
		}
		if value != computed {
			return fmt.Errorf("its content hashes to %s, which is not the %s recorded in it", computed, algorithm)
		}
	}
	return nil
}

// verifyOriginSignature checks that the event carries a signature by a key that its own
// server published.
//
// The form signed is the room version's, which is not the event as it stands: transport
// keys are stripped, and in the room versions whose event IDs are derived the identifier
// is stripped too, because a signature must not cover a value that is itself a hash of
// what was signed. Checking with a generic JSON signer accepts or rejects the wrong
// bytes: in a room whose IDs are hashes, keeping the event ID in the signed form makes
// every honest event look forged.
func verifyOriginSignature(rules roomversion.Rules, event map[string]any, origin string, lookup KeyLookup) error {
	if origin == "" {
		return errors.New("the sender has no server name to sign it")
	}
	signatures := signaturesMap(event)
	byKey, _ := signatures[origin].(map[string]any)
	if len(byKey) == 0 {
		return fmt.Errorf("it carries no signature from %s, the server of its sender", origin)
	}
	signed, err := matrixjson.Canonical(hashableForSignature(rules, event))
	if err != nil {
		return fmt.Errorf("its signed form cannot be encoded: %w", err)
	}
	var problems []string
	verified := 0
	for keyID, raw := range byKey {
		value, _ := raw.(string)
		if value == "" {
			problems = append(problems, fmt.Sprintf("%s carries an empty signature", keyID))
			continue
		}
		signature, err := matrixjson.DecodeUnpadded(value)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", keyID, err))
			continue
		}
		key, err := lookup(origin, keyID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", keyID, err))
			continue
		}
		if len(key) != ed25519.PublicKeySize {
			problems = append(problems, fmt.Sprintf("%s: the published key is %d bytes, not %d",
				keyID, len(key), ed25519.PublicKeySize))
			continue
		}
		if !ed25519.Verify(key, signed, signature) {
			problems = append(problems, fmt.Sprintf("%s: the signature does not match", keyID))
			continue
		}
		verified++
	}
	if verified == 0 {
		if len(problems) > 0 {
			return fmt.Errorf("none of the %d signatures it carries from %s check against a key that server published: %s",
				len(byKey), origin, strings.Join(problems, "; "))
		}
		return fmt.Errorf("none of the %d signatures it carries from %s check against a key that server published",
			len(byKey), origin)
	}
	return nil
}
