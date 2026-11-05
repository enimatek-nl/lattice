package unit

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// TestEventIsNamedByItsContentNotItsLabel is about what an event is called.
//
// From room version 3 on, the event format of the specification does not list an event_id among
// the required properties, because the identifier there is the reference hash: every receiver
// computes it from the bytes in front of it. So an event arriving without that field is not
// incomplete, and an event arriving with a field that contradicts its own content is not a second
// event — it carries a wrong label. A server that demands the label refuses well-formed events,
// and one that believes the label files an event under a name no other server will use. Room
// versions 1 and 2 are the opposite case: the origin server chose the identifier, so a missing one
// cannot be reconstructed and asking for it is correct.
func TestEventIsNamedByItsContentNotItsLabel(t *testing.T) {
	t.Parallel()
	// Room versions 1 and 2 are known to this server but not offered for new rooms, so they are
	// named here rather than left to Supported(); their identifier rules are the ones this test
	// is most particular about.
	versions := append(roomversion.Supported(), roomversion.MustGet("1"), roomversion.MustGet("2"))
	for _, rules := range versions {
		t.Run("room version "+rules.ID, func(t *testing.T) {
			signer := newTestSigner(t)
			event := map[string]any{
				"auth_events":      []any{"$create:example.org", "$join:example.org"},
				"content":          map[string]any{"body": "who decides the name", "msgtype": "m.text"},
				"depth":            float64(4),
				"origin":           "example.org",
				"origin_server_ts": float64(1700000004000),
				"prev_events":      []any{"$previous:example.org"},
				"room_id":          "!room:example.org",
				"sender":           "@alice:example.org",
				"type":             "m.room.message",
			}
			// In the room versions where the origin server picks the identifier it has to be
			// present before signing, because the signature covers it there.
			chosen := ""
			if rules.EventIDs == roomversion.EventIDServerAssigned {
				chosen = "$origin-server-chose-this:example.org"
				event["event_id"] = chosen
			}
			if err := events.Seal("example.org", rules, event, signer); err != nil {
				t.Fatalf("sealing in room version %s: %v", rules.ID, err)
			}
			derived, err := events.EventID(rules, event)
			if err != nil {
				t.Fatalf("computing the event ID in room version %s: %v", rules.ID, err)
			}
			lookup := func(origin, keyID string) (ed25519.PublicKey, error) {
				if origin != "example.org" || keyID != signer.KeyID() {
					return nil, errors.New("this server holds no such key")
				}
				return signer.publicKey(), nil
			}

			if rules.EventIDs == roomversion.EventIDServerAssigned {
				if derived != chosen {
					t.Fatalf("room version %s: the event was named %s, but this room version "+
						"leaves the name to the origin server, which chose %s", rules.ID, derived, chosen)
				}
				// The name is a fact about what the origin server said, so an event without it
				// is an event this server cannot name, and saying so is the only honest answer.
				unnamed := copyEventMap(event)
				delete(unnamed, "event_id")
				if _, err := events.AssignEventID(rules, unnamed); err == nil {
					t.Errorf("room version %s: an event with no event_id was given a name anyway", rules.ID)
				} else if !strings.Contains(err.Error(), "event_id") {
					t.Errorf("room version %s: refused for an unrelated reason: %v", rules.ID, err)
				}
				if err := events.Verify(rules, unnamed, lookup); err == nil {
					t.Errorf("room version %s: an event with no event_id passed its checks", rules.ID)
				}
				return
			}

			// The identifier is the hash, so it can be left out and the event is still the same
			// event: the name is not the sender's to supply.
			unnamed := copyEventMap(event)
			delete(unnamed, "event_id")
			id, err := events.AssignEventID(rules, unnamed)
			if err != nil {
				t.Fatalf("room version %s: naming an event that carries no event_id: %v", rules.ID, err)
			}
			if id != derived {
				t.Errorf("room version %s: an event without a label was named %s, not %s",
					rules.ID, id, derived)
			}
			if stored, _ := unnamed["event_id"].(string); stored != derived {
				t.Errorf("room version %s: the named event carries %q, not the name it was given",
					rules.ID, stored)
			}
			if err := events.Verify(rules, unnamed, lookup); err != nil {
				t.Errorf("room version %s: an honestly signed event with no event_id failed its "+
					"checks: %v", rules.ID, err)
			}

			// A label that contradicts the content does not make a different event, and refusing
			// one would throw away an event every other server can name. The hash wins.
			mislabelled := copyEventMap(event)
			mislabelled["event_id"] = "$someone-else-chose-this:example.org"
			id, err = events.AssignEventID(rules, mislabelled)
			if err != nil {
				t.Fatalf("room version %s: naming an event whose label is wrong: %v", rules.ID, err)
			}
			if id != derived {
				t.Errorf("room version %s: a wrongly labelled event was named %s, not %s",
					rules.ID, id, derived)
			}
			if err := events.Verify(rules, mislabelled, lookup); err != nil {
				t.Errorf("room version %s: an honestly signed event with a wrong label failed its "+
					"checks: %v", rules.ID, err)
			}

			// What is not tolerated is a changed event: the hash of the content and the
			// signature are checked whatever the label says.
			tampered := copyEventMap(unnamed)
			content, _ := tampered["content"].(map[string]any)
			edited := map[string]any{}
			for key, value := range content {
				edited[key] = value
			}
			edited["body"] = "changed in transit"
			tampered["content"] = edited
			if err := events.Verify(rules, tampered, lookup); err == nil {
				t.Errorf("room version %s: an event whose content was changed still checked", rules.ID)
			}
		})
	}
}

func copyEventMap(event map[string]any) map[string]any {
	out := make(map[string]any, len(event))
	for key, value := range event {
		out[key] = value
	}
	return out
}
