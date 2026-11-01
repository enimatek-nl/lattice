package unit

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestKeyDocumentBodyIsCanonical pins the wire form of the published key
// document: the body a peer reads is the canonical JSON of the signed document,
// with no byte added on the way out.
//
// Complement's TestInboundFederationKeys checks the document the strict way: it
// takes the served bytes, deletes the signatures member, and verifies the
// signature against those bytes without re-encoding anything. That works for any
// server whose body is already canonical, and fails for one that appends even a
// single newline - which is what the standard library's JSON encoder does.
func TestKeyDocumentBodyIsCanonical(t *testing.T) {
	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	validUntil := time.Now().Add(24 * time.Hour).UnixMilli()
	body, err := federation.KeyDocumentBody("hs1", key, validUntil)
	if err != nil {
		t.Fatalf("KeyDocumentBody: %v", err)
	}

	if trimmed := bytes.TrimSpace(body); !bytes.Equal(trimmed, body) {
		t.Fatalf("the served body carries whitespace outside the JSON: %q", body)
	}

	// A peer decodes with the canonical decoder so digits survive, and the
	// document has to read back as exactly the bytes it was sent as.
	decoded, err := matrix.DecodeCanonical(body)
	if err != nil {
		t.Fatalf("decoding the served body: %v", err)
	}
	object, ok := decoded.(matrix.Object)
	if !ok {
		t.Fatalf("the body decoded as %T", decoded)
	}
	reserialised, err := matrix.CanonicalJSON(object)
	if err != nil {
		t.Fatalf("re-encoding canonically: %v", err)
	}
	if !bytes.Equal(reserialised, body) {
		t.Fatalf("the body is not its own canonical form:\n%s\n%s", body, reserialised)
	}

	// And the signature verifies over everything except itself, which is what a
	// peer that trusts this server directly checks before it believes a single
	// event signed with this key.
	signatures, ok := object["signatures"].(matrix.Object)
	if !ok {
		t.Fatalf("the body carries no signatures: %s", body)
	}
	byKey, ok := signatures["hs1"].(matrix.Object)
	if !ok {
		t.Fatalf("the body is not signed by its own server name: %s", body)
	}
	value, ok := byKey[key.ID].(string)
	if !ok {
		t.Fatalf("no signature under %s: %s", key.ID, body)
	}
	signature, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decoding the signature: %v", err)
	}

	unsigned := matrix.Object{}
	for field, value := range object {
		if field == "signatures" {
			continue
		}
		unsigned[field] = value
	}
	signedOver, err := matrix.CanonicalJSON(unsigned)
	if err != nil {
		t.Fatalf("canonicalising the unsigned document: %v", err)
	}
	if !ed25519.Verify(key.Public, signedOver, signature) {
		t.Fatalf("the self-signature does not verify over %s", signedOver)
	}
}
