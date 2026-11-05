package unit

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/roomversion"
)

// testSigner is a stand-in for internal/keys.Service: it exposes signing without
// handing out the private key.
type testSigner struct {
	key   ed25519.PrivateKey
	keyID string
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("cannot generate key: %v", err)
	}
	_ = pub
	return &testSigner{key: priv, keyID: "ed25519:testk1"}
}

func (s *testSigner) Sign(data []byte) []byte { return ed25519.Sign(s.key, data) }
func (s *testSigner) KeyID() string           { return s.keyID }

// TestSealProducesHashAndSignature checks the two things an outgoing event needs,
// in the order the specification requires.
func TestSealProducesHashAndSignature(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("11")
	event, err := events.Parse([]byte(sealableEvent))
	if err != nil {
		t.Fatalf("cannot parse event: %v", err)
	}
	signer := newTestSigner(t)

	if err := events.Seal("example.org", rules, event, signer); err != nil {
		t.Fatalf("Seal failed: %v", err)
	}

	hashes, ok := event["hashes"].(map[string]any)
	if !ok {
		t.Fatalf("no hashes member after Seal: %v", event["hashes"])
	}
	wantHash, err := events.ContentHash(rules, event)
	if err != nil {
		t.Fatalf("cannot recompute content hash: %v", err)
	}
	if got, _ := hashes["sha256"].(string); got != wantHash {
		t.Errorf("hashes.sha256 = %q, want %q (the hash must cover the event as stored)", got, wantHash)
	}

	signatures, ok := event["signatures"].(map[string]any)
	if !ok {
		t.Fatalf("no signatures member after Seal")
	}
	byOrigin, ok := signatures["example.org"].(map[string]any)
	if !ok {
		t.Fatalf("signatures are not recorded under the origin server name: %v", signatures)
	}
	if _, ok := byOrigin[signer.KeyID()].(string); !ok {
		t.Errorf("signature for key %q missing", signer.KeyID())
	}
}

// TestSealSignatureSurvivesRedaction is the property that makes redaction and
// signatures compatible: the signature is taken over the redacted form, so a
// redacted event can still be verified.
func TestSealSignatureSurvivesRedaction(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"3", "10", "11"} {
		t.Run("room version "+version, func(t *testing.T) {
			rules := roomversion.MustGet(version)
			event, err := events.Parse([]byte(sealableEvent))
			if err != nil {
				t.Fatalf("cannot parse event: %v", err)
			}
			signer := newTestSigner(t)
			if err := events.Seal("example.org", rules, event, signer); err != nil {
				t.Fatalf("Seal failed: %v", err)
			}

			redacted := events.Redacted(rules, event)

			// The signature is over the redacted document without signatures or
			// transport keys, which is what a receiver re-computes.
			signed, err := matrixjson.Canonical(hashableForTest(redacted))
			if err != nil {
				t.Fatalf("cannot canonicalise redacted event: %v", err)
			}
			signature, err := matrixjson.DecodeUnpadded(signatureOf(t, event))
			if err != nil {
				t.Fatalf("cannot decode signature: %v", err)
			}
			if !ed25519.Verify(newTestSignerPublicKey(signer), signed, signature) {
				t.Errorf("the signature does not verify over the redacted event")
			}

			// And the event ID, which is the reference hash of the same document,
			// must be reproducible from the redacted form.
			fullID, err := events.EventID(rules, event)
			if err != nil {
				t.Fatalf("event ID: %v", err)
			}
			redactedID, err := events.EventID(rules, redacted)
			if err != nil {
				t.Fatalf("redacted event ID: %v", err)
			}
			if fullID != redactedID {
				t.Errorf("event ID differs between the full and redacted forms: %s vs %s", fullID, redactedID)
			}
		})
	}
}

// TestEventIDIgnoresStoredEventID covers verification of a received event: the
// identifier is part of the document a sender shows us, but it is not part of what
// the hash covers, so recomputing must ignore it.
func TestEventIDIgnoresStoredEventID(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("11")
	event, err := events.Parse([]byte(sealableEvent))
	if err != nil {
		t.Fatalf("cannot parse event: %v", err)
	}
	signer := newTestSigner(t)
	if err := events.Seal("example.org", rules, event, signer); err != nil {
		t.Fatalf("Seal failed: %v", err)
	}
	want, err := events.EventID(rules, event)
	if err != nil {
		t.Fatalf("event ID: %v", err)
	}

	withID := make(map[string]any, len(event)+1)
	for key, value := range event {
		withID[key] = value
	}
	withID["event_id"] = "$bogus:example.org" // a sender-supplied identifier is not what the hash covers
	got, err := events.EventID(rules, withID)
	if err != nil {
		t.Fatalf("event ID with stored identifier: %v", err)
	}
	if got != want {
		t.Errorf("recomputed event ID = %q, want %q", got, want)
	}
}

// TestServerAssignedEventIDs checks room versions 1 and 2, where the identifier is
// the origin server's to pick: deriving one there would invent state.
func TestServerAssignedEventIDs(t *testing.T) {
	t.Parallel()
	event, err := events.Parse([]byte(sealableEvent))
	if err != nil {
		t.Fatalf("cannot parse event: %v", err)
	}
	if _, err := events.EventID(roomversion.MustGet("1"), event); err == nil {
		t.Errorf("room version 1 needs an explicit event_id")
	}
	event["event_id"] = "$create:example.org"
	got, err := events.EventID(roomversion.MustGet("2"), event)
	if err != nil {
		t.Fatalf("room version 2 event ID: %v", err)
	}
	if got != "$create:example.org" {
		t.Errorf("room version 2 event ID = %q, want the assigned identifier", got)
	}
}

// hashableForTest reproduces the receiver's view of a redacted event: no
// signatures, no transport keys.
func hashableForTest(event map[string]any) map[string]any {
	out := make(map[string]any, len(event))
	for key, value := range event {
		if key == "signatures" || key == "unsigned" || key == "event_id" || key == "age_ts" ||
			key == "outlier" || key == "destinations" {
			continue
		}
		out[key] = value
	}
	return out
}

// signatureOf reads the single signature Seal recorded.
func signatureOf(t *testing.T, event map[string]any) string {
	t.Helper()
	signatures, ok := event["signatures"].(map[string]any)
	if !ok {
		t.Fatalf("no signatures")
	}
	byOrigin, ok := signatures["example.org"].(map[string]any)
	if !ok {
		t.Fatalf("no signatures for example.org")
	}
	for _, value := range byOrigin {
		if s, ok := value.(string); ok {
			return s
		}
	}
	t.Fatalf("no string signature in %v", byOrigin)
	return ""
}

func newTestSignerPublicKey(signer *testSigner) ed25519.PublicKey {
	return signer.key.Public().(ed25519.PublicKey)
}

// sealableEvent is an unsigned, unhashed event ready to be sealed.
const sealableEvent = `{
  "auth_events": ["$c:example.org", "$p:example.org"],
  "content": {"body": "sealed", "msgtype": "m.text"},
  "depth": 4,
  "origin": "example.org",
  "origin_server_ts": 1700000001000,
  "prev_events": ["$prev:example.org"],
  "room_id": "!room:example.org",
  "sender": "@alice:example.org",
  "type": "m.room.message"
}`
