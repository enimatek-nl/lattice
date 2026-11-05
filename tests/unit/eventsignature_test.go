package unit

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// An event signature has to survive the trip it is made for: sealed here, checked
// there, after the event has been through JSON on both ends. A generic JSON signer
// checks the wrong bytes in the room versions whose event IDs are hashes — it leaves the
// identifier in the signed form, which makes every honestly signed event look forged.

func TestEventSignatureSurvivesEveryRoomVersion(t *testing.T) {
	t.Parallel()
	for _, rules := range roomversion.Supported() {
		t.Run("room version "+rules.ID, func(t *testing.T) {
			signer := newTestSigner(t)
			event := map[string]any{
				"auth_events":      []any{"$create:example.org", "$join:example.org"},
				"content":          map[string]any{"body": "carried across", "msgtype": "m.text"},
				"depth":            float64(7),
				"origin":           "example.org",
				"origin_server_ts": float64(1700000002000),
				"prev_events":      []any{"$previous:example.org"},
				"room_id":          "!room:example.org",
				"sender":           "@alice:example.org",
				"type":             "m.room.message",
			}
			if err := events.Seal("example.org", rules, event, signer); err != nil {
				t.Fatalf("sealing in room version %s: %v", rules.ID, err)
			}
			eventID, err := events.EventID(rules, event)
			if err != nil {
				t.Fatalf("computing the event ID: %v", err)
			}
			event["event_id"] = eventID

			// What a receiving server actually gets is the JSON, not the Go map.
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("encoding the event: %v", err)
			}
			var received map[string]any
			if err := json.Unmarshal(encoded, &received); err != nil {
				t.Fatalf("decoding the event: %v", err)
			}
			lookup := func(origin, keyID string) (ed25519.PublicKey, error) {
				if origin != "example.org" || keyID != signer.KeyID() {
					return nil, errors.New("this server holds no such key")
				}
				return signer.publicKey(), nil
			}
			if err := events.Verify(rules, received, lookup); err != nil {
				t.Fatalf("a honestly signed event in room version %s failed its checks: %v", rules.ID, err)
			}

			// Tampering with the content is caught, whatever the room version.
			tampered := map[string]any{}
			for key, value := range received {
				tampered[key] = value
			}
			content, _ := tampered["content"].(map[string]any)
			edited := map[string]any{}
			for key, value := range content {
				edited[key] = value
			}
			edited["body"] = "edited somewhere else"
			tampered["content"] = edited
			if err := events.Verify(rules, tampered, lookup); err == nil {
				t.Errorf("room version %s: an event whose content was changed still checked", rules.ID)
			}

			// A redacted copy keeps its signature — that is by design, so a redaction
			// does not turn an event into a forgery — but it is not what federation
			// hands over: the content hash was recorded over the unredacted form, and a
			// receiver that accepted a redacted PDU could not check the content the
			// redaction removed.
			redacted := events.Redacted(rules, received)
			if err := events.Verify(rules, redacted, lookup); err == nil {
				t.Errorf("room version %s: a redacted copy was accepted as if it were the event it redacts", rules.ID)
			} else if !strings.Contains(err.Error(), "content hash") {
				t.Errorf("room version %s: the redacted copy was refused for the wrong reason: %v", rules.ID, err)
			}

			// An event with no content hash at all cannot be checked, so it is not
			// accepted.
			behashed := map[string]any{}
			for key, value := range received {
				behashed[key] = value
			}
			delete(behashed, "hashes")
			if err := events.Verify(rules, behashed, lookup); err == nil {
				t.Errorf("room version %s: an event with no content hash was accepted", rules.ID)
			}

			// A signature by a key the sender does not publish is not a signature.
			other := newTestSigner(t)
			unknown := func(origin, keyID string) (ed25519.PublicKey, error) {
				return other.publicKey(), nil
			}
			if err := events.Verify(rules, received, unknown); err == nil {
				t.Errorf("room version %s: a signature by a key the sender never published was accepted", rules.ID)
			}
		})
	}
}

func TestEventSignatureNeedsTheSendersOwnServer(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("11")
	signer := newTestSigner(t)
	event := map[string]any{
		"auth_events":      []any{},
		"content":          map[string]any{"body": "somebody else's claim", "msgtype": "m.text"},
		"depth":            float64(2),
		"origin_server_ts": float64(1700000003000),
		"prev_events":      []any{},
		"room_id":          "!room:example.org",
		"sender":           "@alice:somewhere-else.example",
		"type":             "m.room.message",
	}
	if err := events.Seal("example.org", rules, event, signer); err != nil {
		t.Fatalf("sealing: %v", err)
	}
	eventID, err := events.EventID(rules, event)
	if err != nil {
		t.Fatalf("computing the ID: %v", err)
	}
	event["event_id"] = eventID

	// The event is signed by example.org but sent by a user of another server, and the
	// only key offered is example.org's. That is not a signature by the sender's server,
	// which is the only signature that counts.
	err = events.Verify(rules, event, func(origin, keyID string) (ed25519.PublicKey, error) {
		if origin == "example.org" {
			return signer.publicKey(), nil
		}
		return nil, errors.New("this server holds no such key")
	})
	if err == nil {
		t.Errorf("an event signed by a server other than its sender's was accepted")
	}
}

// publicKey is the verification half of the test signer, which the existing tests never
// needed because they only produced signatures.
func (s *testSigner) publicKey() ed25519.PublicKey { return s.key.Public().(ed25519.PublicKey) }
