package unit

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// Before an event can be checked for who signed it and what it claims, it has to be an
// event: the shape the room version gives it. The checks are short and unglamorous, and
// getting them wrong is expensive in both directions - refuse something valid and a
// federation partner's room silently stops growing, accept something malformed and the
// identity of the event (its hash, its id, its signature, all defined over that shape)
// is being computed over something no server agreed to.

// pduBase is a version 11 event as a peer would send it: the derived id is the real hash
// of these bytes, so the fixture is not accidentally invalid in some other way.
func pduBase(t *testing.T) matrix.Object {
	t.Helper()
	event := matrix.Object{
		"room_id":          "!room:alice.example",
		"type":             matrix.TypeMessage,
		"sender":           "@alice:alice.example",
		"origin_server_ts": int64(1_700_000_000_000),
		"depth":            int64(4),
		"prev_events":      matrix.Array{"$predecessor"},
		"auth_events":      matrix.Array{"$create"},
		"content":          matrix.Object{"msgtype": "m.text", "body": "hello"},
		"hashes":           matrix.Object{"sha256": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		"signatures":       matrix.Object{"alice.example": matrix.Object{"ed25519:a1b2c3": "AAAA"}},
	}
	id, err := eventcrypto.EventID(event, "11")
	if err != nil {
		t.Fatalf("cannot name the fixture event: %v", err)
	}
	event["event_id"] = id
	return event
}

// pduLegacy is a room version 2 event: the origin server names itself, chooses the id,
// and the event carries prev_state.
func pduLegacy() matrix.Object {
	return matrix.Object{
		"event_id":         "$one:alice.example",
		"room_id":          "!room:alice.example",
		"type":             matrix.TypeMessage,
		"sender":           "@alice:alice.example",
		"origin":           "alice.example",
		"origin_server_ts": int64(1_400_000_000_000),
		"depth":            int64(4),
		"prev_events":      matrix.Array{"$zero:alice.example"},
		"prev_state":       matrix.Array{},
		"auth_events":      matrix.Array{"$create:alice.example"},
		"content":          matrix.Object{"msgtype": "m.text", "body": "older"},
		"hashes":           matrix.Object{"sha256": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		"signatures":       matrix.Object{"alice.example": matrix.Object{"ed25519:a1b2c3": "AAAA"}},
	}
}

// TestPDUFormat walks the shape rules.
func TestPDUFormat(t *testing.T) {
	tests := []struct {
		name    string
		version string
		event   func(t *testing.T) matrix.Object
		want    string
	}{
		{
			name:    "a version 11 message",
			version: "11",
			event:   pduBase,
		},
		{
			name:    "a version 2 message",
			version: "2",
			event:   func(*testing.T) matrix.Object { return pduLegacy() },
		},
		{
			name:    "an unknown room version cannot be checked against",
			version: "nonsense",
			event:   pduBase,
			want:    "not one this server can check",
		},
		{
			name:    "a PDU without a type",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				delete(event, "type")
				return event
			},
			want: "must carry type",
		},
		{
			name:    "a sender that is not a user id",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["sender"] = "alice.example"
				return event
			},
			want: "must be a user id",
		},
		{
			name:    "a room id that is not a room id",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["room_id"] = "room:alice.example"
				return event
			},
			want: "must be a room id",
		},
		{
			name:    "an event id that is spelled for another room version",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["event_id"] = "$one:alice.example"
				return event
			},
			want: "spelled the way room version 2 spells ids",
		},
		{
			name:    "prev_events that is not an array",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["prev_events"] = "$only"
				return event
			},
			want: "must be an array of event ids",
		},
		{
			name:    "an auth event that is not an event id",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["auth_events"] = matrix.Array{"create"}
				return event
			},
			want: "is not an event id",
		},
		{
			name:    "content that is not an object",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["content"] = "hello"
				return event
			},
			want: "content of a PDU must be an object",
		},
		{
			name:    "a PDU with no content hash",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				delete(event, "hashes")
				return event
			},
			want: "must carry a content hash",
		},
		{
			name:    "a PDU with no signature",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["signatures"] = matrix.Object{}
				return event
			},
			want: "must carry at least one signature",
		},
		{
			name:    "a depth that is not a whole number",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["depth"] = "4"
				return event
			},
			want: "the depth of a PDU is missing or is not a number",
		},
		{
			name:    "a timestamp with digits after the point",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["origin_server_ts"] = 1_700_000_000.5
				return event
			},
			want: "not a whole number",
		},
		{
			name:    "a negative depth",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["depth"] = int64(-1)
				return event
			},
			want: "below the least it may be",
		},
		{
			name:    "a state key that is not a string",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["state_key"] = int64(7)
				return event
			},
			want: "state_key of a PDU must be a string",
		},
		{
			name:    "a version 11 redaction names its target in content",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["type"] = matrix.TypeRedaction
				event["content"] = matrix.Object{"redacts": "$target"}
				return event
			},
		},
		{
			name:    "a version 11 redaction may not name its target beside the content",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["type"] = matrix.TypeRedaction
				event["content"] = matrix.Object{}
				event["redacts"] = "$target"
				return event
			},
			want: "names its target in content, not beside it",
		},
		{
			name:    "a version 11 redaction with no target",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["type"] = matrix.TypeRedaction
				event["content"] = matrix.Object{}
				return event
			},
			want: "must name the event it redacts in content.redacts",
		},
		{
			name:    "a version 10 redaction names its target beside the content",
			version: "10",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["type"] = matrix.TypeRedaction
				event["content"] = matrix.Object{}
				event["redacts"] = "$target"
				return event
			},
		},
		{
			// The version 3 format has no origin, and a sender that sends one anyway has
			// still signed the event it sent. gomatrixserverlib does exactly this - its
			// event builder writes origin into every room version - so refusing it here
			// meant the state of a federated room could not be stored at all.
			name:    "a version 11 event carrying origin anyway is still the event that was signed",
			version: "11",
			event: func(t *testing.T) matrix.Object {
				event := pduBase(t)
				event["origin"] = "alice.example"
				return event
			},
		},
		{
			name:    "a version 2 event has to name its origin server",
			version: "2",
			event: func(t *testing.T) matrix.Object {
				event := pduLegacy()
				delete(event, "origin")
				return event
			},
			want: "must name the server it came from in origin",
		},
		{
			name:    "a version 2 event has to carry prev_state",
			version: "2",
			event: func(t *testing.T) matrix.Object {
				event := pduLegacy()
				delete(event, "prev_state")
				return event
			},
			want: "must carry prev_state",
		},
		{
			name:    "a version 2 event id has to name a domain",
			version: "2",
			event: func(t *testing.T) matrix.Object {
				event := pduLegacy()
				event["event_id"] = "$nomain"
				return event
			},
			want: "missing the domain room version 2 asks for",
		},
		{
			name:    "a version 2 member event with a membership that is not a string",
			version: "2",
			event: func(t *testing.T) matrix.Object {
				event := pduLegacy()
				event["type"] = matrix.TypeMember
				event["state_key"] = "@bob:bob.example"
				event["content"] = matrix.Object{"membership": true}
				return event
			},
			want: "membership of an m.room.member event must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := eventcrypto.PDUCheck(tt.event(t), tt.version)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("a valid %s event was refused: %v", tt.version, err)
			case tt.want != "" && err == nil:
				t.Fatalf("an invalid %s event was accepted (wanted %q)", tt.version, tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Fatalf("refused with %q, wanted %q", err, tt.want)
			}
		})
	}
}

// TestPDUHashAndRedactedForm pins the pair of hash outcomes the intake has to tell
// apart: an event whose hash covers its content, and an event whose hash covers the
// redacted form the room version defines.
func TestPDUHashAndRedactedForm(t *testing.T) {
	event := pduBase(t)
	hash, err := eventcrypto.ContentHashOf(event, "11")
	if err != nil {
		t.Fatalf("computing the content hash: %v", err)
	}
	event["hashes"] = matrix.Object{"sha256": base64.RawStdEncoding.EncodeToString(hash[:])}
	if err := eventcrypto.VerifyContentHash(event, "11"); err != nil {
		t.Fatalf("an event with its own hash failed the hash check: %v", err)
	}

	redacted := eventcrypto.Redact(event, "11")
	if eventcrypto.IsRedactedForm(event, "11") {
		t.Fatal("an unredacted message event reads as already redacted, so a redaction would be invisible")
	}
	if !eventcrypto.IsRedactedForm(redacted, "11") {
		t.Fatal("redacting an event twice changes it again, which no redaction algorithm should do")
	}

	// The content hash covers the event as it stood when it was signed, including the
	// keys a redaction destroys. A copy that has been redacted therefore does not match
	// the hash it still carries, which is exactly why a receiver keeps the redacted copy
	// it computed itself rather than the copy a peer sent: the signature is made over the
	// redacted form and survives, and the hash is what proves which fields were there.
	if err := eventcrypto.VerifyContentHash(redacted, "11"); err == nil {
		t.Fatal("a redacted copy matched the content hash of the event it came from")
	}
	redactedHash, err := eventcrypto.ContentHashOf(redacted, "11")
	if err != nil {
		t.Fatalf("computing the redacted form's hash: %v", err)
	}
	if bytes.Equal(hash[:], redactedHash[:]) {
		t.Fatal("redacting a message event left its content hash unchanged, so a redaction would be invisible")
	}
	event["hashes"] = matrix.Object{"sha256": base64.RawStdEncoding.EncodeToString(redactedHash[:])}
	if err := eventcrypto.VerifyContentHash(event, "11"); err == nil {
		t.Fatal("a hash over the redacted form was accepted for an unredacted event")
	}
}
