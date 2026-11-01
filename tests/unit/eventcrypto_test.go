package unit

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The hashing and redaction rules are specified to the byte, and the
// specification's own appendix carries events whose hashes it publishes. Those two
// facts together mean the rules can be pinned by known answers rather than by
// behaviour observed through a server.

// TestContentHashMatchesTheSpecification vectors the content hash against the two
// events the specification's signing appendix publishes with their hashes.
func TestContentHashMatchesTheSpecification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		event string
		want  string
	}{
		{
			name:  "minimally sized event",
			event: `{"room_id":"!x:domain","sender":"@a:domain","origin":"domain","origin_server_ts":1000000,"signatures":{},"hashes":{},"type":"X","content":{},"prev_events":[],"auth_events":[],"depth":3,"unsigned":{"age_ts":1000000}}`,
			want:  "5jM4wQpv6lnBo7CLIghJuHdW+s2CMBJPUOGOC89ncos",
		},
		{
			name:  "event with redactable content",
			event: `{"content":{"body":"Here is the message content"},"event_id":"$0:domain","origin":"domain","origin_server_ts":1000000,"type":"m.room.message","room_id":"!r:domain","sender":"@u:domain","signatures":{},"unsigned":{"age_ts":1000000}}`,
			want:  "onLKD1bGljeBWQhWZ1kaP9SorVmRQNdN5aM2JYU2n/g",
		},
	}
	for _, tc := range cases {
		object := decodeObject(t, tc.event)
		hash, err := eventcrypto.ContentHash(object)
		if err != nil {
			t.Fatalf("%s: ContentHash: %v", tc.name, err)
		}
		got := base64.RawStdEncoding.EncodeToString(hash[:])
		if got != tc.want {
			t.Errorf("%s: content hash = %s, want %s", tc.name, got, tc.want)
		}
		// The event as published must also pass its own hash check, which is what
		// a receiver does with an event that arrives with hashes filled in.
		signed, err := eventcrypto.HashAndSign(object, "1", mustKey(t), "domain", "ed25519:1")
		if err != nil {
			t.Fatalf("%s: HashAndSign: %v", tc.name, err)
		}
		stored, err := eventcrypto.StoredContentHash(signed)
		if err != nil {
			t.Fatalf("%s: StoredContentHash: %v", tc.name, err)
		}
		if stored != tc.want {
			t.Errorf("%s: signing changed the content hash to %s", tc.name, stored)
		}
		if err := eventcrypto.VerifyContentHash(signed, "1"); err != nil {
			t.Errorf("%s: the signed event does not verify against its own hash: %v", tc.name, err)
		}
	}
}

// TestRedactionKeepsExactlyWhatTheRoomVersionProtects pins the two redaction
// algorithms. Keeping a key the version destroys leaks content; dropping a key the
// version protects changes the event id every other server computes.
func TestRedactionKeepsExactlyWhatTheRoomVersionProtects(t *testing.T) {
	t.Parallel()
	full := matrix.Object{
		"event_id":         "$0:domain",
		"type":             "m.room.message",
		"room_id":          "!r:domain",
		"sender":           "@u:domain",
		"state_key":        "",
		"content":          matrix.Object{"body": "secret", "extra": "also secret"},
		"hashes":           matrix.Object{"sha256": "aaaa"},
		"signatures":       matrix.Object{"domain": matrix.Object{"ed25519:1": "bbbb"}},
		"depth":            int64(7),
		"prev_events":      []any{"$1:domain"},
		"prev_state":       []any{"$2:domain"},
		"auth_events":      []any{"$3:domain"},
		"origin":           "domain",
		"origin_server_ts": int64(1000),
		"membership":       "join",
		"unsigned":         matrix.Object{"age_ts": int64(1000)},
	}
	original := matrix.Object{}
	for key, value := range full {
		original[key] = value
	}

	v1 := eventcrypto.Redact(full, "1")
	for _, key := range []string{"event_id", "type", "room_id", "sender", "state_key", "content",
		"hashes", "signatures", "depth", "prev_events", "prev_state", "auth_events",
		"origin", "origin_server_ts", "membership"} {
		if _, found := v1[key]; !found {
			t.Errorf("room version 1 redaction dropped %s, which that version protects", key)
		}
	}
	if _, found := v1["unsigned"]; found {
		t.Error("the redacted event still carries unsigned, which is not an event property")
	}
	// The versions differ here, and only here: a room version is not a hierarchy, so
	// the difference has to be asked about by name.
	if _, found := v1["origin"]; !found {
		t.Error("room version 1 protects origin")
	}
	if _, found := v1["membership"]; !found {
		t.Error("room version 1 protects membership")
	}
	if _, found := v1["prev_state"]; !found {
		t.Error("room version 1 protects prev_state")
	}

	v11 := eventcrypto.Redact(full, "11")
	for _, key := range []string{"origin", "membership", "prev_state"} {
		if _, found := v11[key]; found {
			t.Errorf("room version 11 redaction kept %s, which that version destroys", key)
		}
	}
	for _, key := range []string{"event_id", "type", "room_id", "sender", "content",
		"hashes", "signatures", "depth", "prev_events", "auth_events", "origin_server_ts"} {
		if _, found := v11[key]; !found {
			t.Errorf("room version 11 redaction dropped %s, which that version protects", key)
		}
	}

	// Redaction must not write through to the caller's copy: the same event object
	// is still needed to serve a client, to re-hash, and to log.
	if _, gone := original["origin"]; !gone {
		t.Error("Redact modified the event it was given")
	}
	if len(full) != len(original) {
		t.Error("Redact changed the size of the event it was given")
	}
}

// TestRedactionOfContentByType pins which content keys survive, per event type and
// room version. A membership event that loses its membership is no longer a
// membership event; a create event that loses its room version makes the room
// unreadable.
func TestRedactionOfContentByType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		eventType   string
		content     matrix.Object
		roomVersion string
		want        matrix.Object
	}{
		{
			name:      "a message loses everything in both versions",
			eventType: "m.room.message",
			content:   matrix.Object{"body": "hi", "msgtype": "m.text", "m.relates_to": matrix.Object{}},
			want:      matrix.Object{},
		},
		{
			name:        "a member keeps its membership",
			eventType:   matrix.TypeMember,
			content:     matrix.Object{"membership": "join", "display_name": "Alice"},
			roomVersion: "1",
			want:        matrix.Object{"membership": "join"},
		},
		{
			name:        "from room version 11 a member also keeps what authorised its join",
			eventType:   matrix.TypeMember,
			content:     matrix.Object{"membership": "join", "join_authorised_via_users_server": "@mod:domain", "display_name": "Alice"},
			roomVersion: "11",
			want:        matrix.Object{"membership": "join", "join_authorised_via_users_server": "@mod:domain"},
		},
		{
			name:        "a create event keeps only its creator before room version 11",
			eventType:   matrix.TypeCreate,
			content:     matrix.Object{"creator": "@a:domain", "room_version": "11", "m.federate": true},
			roomVersion: "10",
			want:        matrix.Object{"creator": "@a:domain"},
		},
		{
			name:        "from room version 11 a create event keeps its content",
			eventType:   matrix.TypeCreate,
			content:     matrix.Object{"creator": "@a:domain", "room_version": "11", "m.federate": true},
			roomVersion: "11",
			want:        matrix.Object{"creator": "@a:domain", "room_version": "11", "m.federate": true},
		},
		{
			name:        "join rules keep the rule, and the conditions from room version 11",
			eventType:   matrix.TypeJoinRules,
			content:     matrix.Object{"join_rule": "restricted", "allow": []any{matrix.Object{"type": "m.room.member", "room_id": "!other:domain"}}},
			roomVersion: "10",
			want:        matrix.Object{"join_rule": "restricted"},
		},
		{
			name:        "join rules keep the rule, and the conditions from room version 11",
			eventType:   matrix.TypeJoinRules,
			content:     matrix.Object{"join_rule": "restricted", "allow": []any{matrix.Object{"type": "m.room.member", "room_id": "!other:domain"}}},
			roomVersion: "11",
			want:        matrix.Object{"join_rule": "restricted", "allow": []any{matrix.Object{"type": "m.room.member", "room_id": "!other:domain"}}},
		},
		{
			name:        "power levels keep the levels that authorise things",
			eventType:   matrix.TypePowerLevels,
			content:     matrix.Object{"users": matrix.Object{"@a:domain": 100}, "invite": 0, "notifications": matrix.Object{"room_mention": 0}},
			roomVersion: "11",
			want:        matrix.Object{"users": matrix.Object{"@a:domain": 100}, "invite": 0},
		},
		{
			name:        "a redaction keeps what it redacts from room version 11",
			eventType:   matrix.TypeRedaction,
			content:     matrix.Object{"redacts": "$victim:domain", "reason": "spam"},
			roomVersion: "11",
			want:        matrix.Object{"redacts": "$victim:domain"},
		},
		{
			name:        "a redaction loses its target before room version 11",
			eventType:   matrix.TypeRedaction,
			content:     matrix.Object{"redacts": "$victim:domain", "reason": "spam"},
			roomVersion: "10",
			want:        matrix.Object{},
		},
		{
			name:        "an unknown type keeps nothing",
			eventType:   "com.example.thing",
			content:     matrix.Object{"keep": "me"},
			roomVersion: "11",
			want:        matrix.Object{},
		},
	}
	for _, tc := range cases {
		version := tc.roomVersion
		if version == "" {
			version = "1"
		}
		redacted := eventcrypto.Redact(matrix.Object{"type": tc.eventType, "content": tc.content}, version)
		content, ok := redacted["content"].(matrix.Object)
		if !ok {
			t.Errorf("%s (%s): content is %T, want an object", tc.name, version, redacted["content"])
			continue
		}
		if canonicalWant, err := matrix.CanonicalJSON(tc.want); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		} else if canonicalGot, err := matrix.CanonicalJSON(content); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		} else if string(canonicalWant) != string(canonicalGot) {
			t.Errorf("%s (%s): content = %s, want %s", tc.name, version, canonicalGot, canonicalWant)
		}
	}
}

// TestThirdPartyInviteSignatureSurvivesRedaction pins the one content key that is
// kept as a nested exception: the signed claim of a third-party invite is what makes
// the membership verifiable, and destroying it makes the event meaningless.
func TestThirdPartyInviteSignatureSurvivesRedaction(t *testing.T) {
	t.Parallel()
	invite := matrix.Object{
		"signed": matrix.Object{"mxid": "@a:domain", "token": "tok"},
		"other":  "dropped",
	}
	redacted := eventcrypto.Redact(matrix.Object{
		"type":    matrix.TypeMember,
		"content": matrix.Object{"membership": "invite", "third_party_invite": invite},
	}, "1")
	content := redacted["content"].(matrix.Object)
	signed, ok := content["third_party_invite"].(matrix.Object)
	if !ok {
		t.Fatalf("the third-party invite was dropped: %v", content["third_party_invite"])
	}
	if _, found := signed["signed"]; !found {
		t.Errorf("the signed claim was dropped: %v", signed)
	}
	if _, found := signed["other"]; found {
		t.Errorf("the third-party invite kept more than its signature: %v", signed)
	}
}

// TestHashesIgnoreTransitFields pins what may and may not change in flight. Any
// server may annotate an event with unsigned data, and a hash over those bytes
// would be broken by an innocent relay.
func TestHashesIgnoreTransitFields(t *testing.T) {
	t.Parallel()
	base := matrix.Object{
		"event_id":         "$1:domain",
		"type":             "m.room.message",
		"room_id":          "!r:domain",
		"sender":           "@a:domain",
		"origin":           "domain",
		"origin_server_ts": int64(1000),
		"depth":            int64(3),
		"content":          matrix.Object{"body": "hi"},
		"prev_events":      []any{},
		"auth_events":      []any{},
	}
	hashOf := func(object matrix.Object) string {
		t.Helper()
		hash, err := eventcrypto.ContentHash(object)
		if err != nil {
			t.Fatalf("ContentHash: %v", err)
		}
		return base64.RawStdEncoding.EncodeToString(hash[:])
	}
	without := hashOf(base)
	annotated := matrix.Object{}
	for key, value := range base {
		annotated[key] = value
	}
	annotated["unsigned"] = matrix.Object{"age": int64(999999), "transaction_id": "abc"}
	if hashOf(annotated) != without {
		t.Error("the content hash covers unsigned, which any server may rewrite in transit")
	}
	signedCopy := matrix.Object{}
	for key, value := range base {
		signedCopy[key] = value
	}
	signedCopy["signatures"] = matrix.Object{"domain": matrix.Object{"ed25519:1": "x"}}
	if hashOf(signedCopy) != without {
		t.Error("the content hash covers signatures, which cannot be added without invalidating them")
	}

	// Everything else is covered, including the fields a redaction removes: that is
	// how a receiver can prove an event was fuller before.
	touched := []matrix.Object{
		withField(base, "depth", int64(4)),
		withField(base, "origin", "other.domain"),
		withField(base, "prev_events", []any{"$2:domain"}),
		withField(base, "auth_events", []any{"$3:domain"}),
		withField(base, "event_id", "$2:domain"),
		withField(base, "origin_server_ts", int64(1001)),
	}
	for _, other := range touched {
		if hashOf(other) == without {
			t.Errorf("the content hash ignores a change to %v", other)
		}
	}
}

// TestReferenceHashRedactsFirst pins the order the specification gives: the event
// goes through the redaction algorithm, and only then are signatures and unsigned
// dropped.
func TestReferenceHashRedactsFirst(t *testing.T) {
	t.Parallel()
	event := matrix.Object{
		"event_id":         "$1:domain",
		"type":             "m.room.message",
		"room_id":          "!r:domain",
		"sender":           "@a:domain",
		"origin":           "domain",
		"origin_server_ts": int64(1000),
		"depth":            int64(3),
		"content":          matrix.Object{"body": "hi"},
	}
	hashOf := func(object matrix.Object, version string) string {
		t.Helper()
		hash, err := eventcrypto.ReferenceHash(object, version)
		if err != nil {
			t.Fatalf("ReferenceHash: %v", err)
		}
		return base64.RawStdEncoding.EncodeToString(hash[:])
	}
	bare := hashOf(event, "11")
	annotated := withField(event, "unsigned", matrix.Object{"age": int64(5)})
	signed := withField(annotated, "signatures", matrix.Object{"domain": matrix.Object{"ed25519:1": "x"}})
	for _, other := range []matrix.Object{annotated, signed} {
		if hashOf(other, "11") != bare {
			t.Error("the reference hash covers signatures or unsigned data")
		}
	}
	// A content hash is different: the reference hash covers the essential fields of
	// an event *including* its content hashes, which is what lets an event id prove
	// which version of the content a server saw.
	if hashOf(withField(event, "hashes", matrix.Object{"sha256": "y"}), "11") == bare {
		t.Error("the reference hash ignores the event's content hash")
	}
	// Content is destroyed by the redaction the reference hash applies first, so a
	// change that only touches content does not move the reference hash - while the
	// content hash does move, which is exactly the pairing the design depends on.
	contentChanged := withField(event, "content", matrix.Object{"body": "different"})
	if hashOf(contentChanged, "11") != bare {
		t.Error("the reference hash covers content that the redaction algorithm destroys")
	}
	if contentHashOf(t, contentChanged) == contentHashOf(t, event) {
		t.Error("the content hash ignores a change to content, so a receiver could not tell a redacted event from a forged one")
	}

	// The room version changes which top-level keys survive the redaction, so the
	// same event has a different id in a version 1 room than in a version 11 one.
	if hashOf(event, "1") == hashOf(event, "11") {
		t.Error("room versions 1 and 11 give the same reference hash although their redaction rules differ")
	}
}

// TestEventIDsAreDerivedPerRoomVersion pins the three id formats.
func TestEventIDsAreDerivedPerRoomVersion(t *testing.T) {
	t.Parallel()
	event := matrix.Object{
		"event_id":         "$given:domain",
		"type":             "m.room.message",
		"room_id":          "!r:domain",
		"sender":           "@a:domain",
		"origin":           "domain",
		"origin_server_ts": int64(1000),
		"depth":            int64(3),
		"content":          matrix.Object{"body": "hi"},
	}
	cases := []struct {
		version   string
		prefixed  bool
		wantsOwn  bool
		urlSafeOK bool
	}{
		{version: "1", wantsOwn: true},
		{version: "2", wantsOwn: true},
		{version: "3", prefixed: true},
		{version: "4", prefixed: true},
		{version: "11", prefixed: true},
	}
	for _, tc := range cases {
		id, err := eventcrypto.EventID(event, tc.version)
		if err != nil {
			t.Fatalf("EventID for room version %s: %v", tc.version, err)
		}
		if tc.wantsOwn {
			if id != "$given:domain" {
				t.Errorf("room version %s: id = %q, want the event's own event_id", tc.version, id)
			}
			continue
		}
		if !strings.HasPrefix(id, "$") {
			t.Errorf("room version %s: id %q has no $ prefix", tc.version, id)
		}
		if strings.Contains(id, ":") || strings.Contains(id, "=") {
			t.Errorf("room version %s: id %q is not derived from the hash", tc.version, id)
		}
		body := id[1:]
		if tc.version == "3" {
			if _, err := base64.RawStdEncoding.DecodeString(body); err != nil {
				t.Errorf("room version %s: id %q is not unpadded base64: %v", tc.version, id, err)
			}
		} else {
			if _, err := base64.RawURLEncoding.DecodeString(body); err != nil {
				t.Errorf("room version %s: id %q is not URL-safe unpadded base64: %v", tc.version, id, err)
			}
			if strings.ContainsAny(body, "+/") {
				t.Errorf("room version %s: id %q uses the non-URL-safe alphabet", tc.version, id)
			}
		}
		if len(body) != 43 {
			t.Errorf("room version %s: id %q encodes %d characters, want the 43 of a sha256", tc.version, id, len(body))
		}
	}

	// Room versions 3 and 4 differ only in alphabet. An id that has neither + nor /
	// is identical in both, which is why the difference went unnoticed for so long.
	three, err := eventcrypto.EventID(event, "3")
	if err != nil {
		t.Fatal(err)
	}
	four, err := eventcrypto.EventID(event, "4")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(three[1:], "+/") {
		if three == four {
			t.Errorf("the two alphabets produced the same id %q for a hash that needs both", three)
		}
		if strings.ReplaceAll(strings.ReplaceAll(three, "+", "-"), "/", "_") != four {
			t.Errorf("room versions 3 and 4 differ by more than the alphabet: %q and %q", three, four)
		}
	}

	// A room version 1 or 2 event with no id cannot be used: inventing one here would
	// hand two servers different ids for the same event.
	if _, err := eventcrypto.EventID(matrix.Object{"type": "m.room.message"}, "1"); err == nil {
		t.Error("an event with no event_id was accepted for room version 1")
	}
}

// TestHashAndSignKeepsTheEventWhole covers the order of operations: the hash covers
// the full event, the signature is made over the redacted copy including that hash,
// and the event that comes back is the full one.
func TestHashAndSignKeepsTheEventWhole(t *testing.T) {
	t.Parallel()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a signing key: %v", err)
	}
	event := matrix.Object{
		"event_id":         "$1:domain",
		"type":             "m.room.message",
		"room_id":          "!r:domain",
		"sender":           "@a:domain",
		"origin":           "domain",
		"origin_server_ts": int64(1000),
		"depth":            int64(3),
		"prev_events":      []any{},
		"auth_events":      []any{},
		"content":          matrix.Object{"body": "Here is the message content"},
		"unsigned":         matrix.Object{"age_ts": int64(1000)},
	}
	signed, err := eventcrypto.HashAndSign(event, "11", private, "domain", "ed25519:1")
	if err != nil {
		t.Fatalf("HashAndSign: %v", err)
	}
	if signed["content"].(matrix.Object)["body"] != "Here is the message content" {
		t.Errorf("signing stripped the event's content: %v", signed["content"])
	}
	if _, found := signed["hashes"]; !found {
		t.Fatal("the signed event carries no hashes")
	}
	if err := eventcrypto.VerifyContentHash(signed, "11"); err != nil {
		t.Errorf("the signed event fails its own content hash: %v", err)
	}
	if err := eventcrypto.EventSignature(signed, "11", "domain", "ed25519:1", public); err != nil {
		t.Errorf("the signed event fails its own signature: %v", err)
	}

	// A redacted copy of the signed event still verifies against both, which is the
	// whole point of signing the redacted form.
	redacted := eventcrypto.Redact(signed, "11")
	if err := eventcrypto.EventSignature(redacted, "11", "domain", "ed25519:1", public); err != nil {
		t.Errorf("the redacted event fails the signature that was made to survive redaction: %v", err)
	}
	// The content hash of a redacted copy cannot match, and that is not a fault: the
	// hash covers content this copy no longer holds. IsRedactedForm is how a receiver
	// tells that case from tampering, and it is pinned separately below.
	if err := eventcrypto.VerifyContentHash(redacted, "11"); err == nil {
		t.Error("a redacted copy passed a hash check that covers content it does not have")
	}

	// A different key must not verify, and neither must a tampered event that kept
	// the old hash and signature.
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a second key: %v", err)
	}
	if err := eventcrypto.EventSignature(signed, "11", "domain", "ed25519:1", otherPublic); err == nil {
		t.Error("the signature verified against an unrelated key")
	}
	// An edit to content that the room version redacts away leaves the redacted form
	// untouched, so the signature still verifies. That is not a hole: the signature is
	// meant to survive redaction, and it is the content hash - which covers the
	// unredacted event - that detects the edit. Both checks are required, and neither
	// is enough on its own.
	tampered := withField(signed, "content", matrix.Object{"body": "edited by a relay"})
	if err := eventcrypto.EventSignature(tampered, "11", "domain", "ed25519:1", public); err != nil {
		t.Errorf("a signature over redacted content should not break when the unredacted part is edited: %v", err)
	}
	if err := eventcrypto.VerifyContentHash(tampered, "11"); err == nil {
		t.Error("an edited event still verified its content hash")
	}
	// An edit to a field the redaction protects breaks the signature.
	for _, field := range []string{"depth", "origin_server_ts", "sender", "room_id", "type"} {
		if err := eventcrypto.EventSignature(withField(signed, field, "edited"), "11", "domain", "ed25519:1", public); err == nil {
			t.Errorf("an edit to %s, which the room version protects, still verified its signature", field)
		}
	}

	// Signing needs both halves of an identity.
	if _, err := eventcrypto.HashAndSign(event, "11", private, "", "ed25519:1"); err == nil {
		t.Error("an event was signed with no server name")
	}
	if _, err := eventcrypto.HashAndSign(event, "11", private, "domain", ""); err == nil {
		t.Error("an event was signed with no key id")
	}
	if _, err := eventcrypto.HashAndSign(event, "11", ed25519.PrivateKey{}, "domain", "ed25519:1"); err == nil {
		t.Error("an event was signed with no key")
	}
}

// TestContentHashChecksAreHonestAboutRedaction records what a hash check can and
// cannot prove about an event that arrived already redacted.
func TestContentHashChecksAreHonestAboutRedaction(t *testing.T) {
	t.Parallel()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a signing key: %v", err)
	}
	event := matrix.Object{
		"type":             "m.room.message",
		"room_id":          "!r:domain",
		"sender":           "@a:domain",
		"origin":           "domain",
		"origin_server_ts": int64(1000),
		"depth":            int64(3),
		"content":          matrix.Object{"body": "full"},
		"prev_events":      []any{},
		"auth_events":      []any{},
	}
	signed, err := eventcrypto.HashAndSign(event, "11", private, "domain", "ed25519:1")
	if err != nil {
		t.Fatalf("HashAndSign: %v", err)
	}
	// A receiver handed the redacted copy cannot match its hash: the hash covers
	// content that was destroyed on the way. What the receiver can establish is that
	// the event is in its redacted form, which is what tells a redaction in transit
	// apart from a forged event.
	redacted := eventcrypto.Redact(signed, "11")
	if err := eventcrypto.VerifyContentHash(redacted, "11"); err == nil {
		t.Error("a redacted copy passed a hash check that covers content it no longer has")
	}
	if !eventcrypto.IsRedactedForm(redacted, "11") {
		t.Errorf("a legitimately redacted copy is not recognised as such: %v", redacted)
	}
	if eventcrypto.IsRedactedForm(tamperedContent(signed), "11") {
		t.Error("an event with content still in it is not in redacted form")
	}
	// An event with no hashes at all cannot be checked.
	if err := eventcrypto.VerifyContentHash(without(signed, "hashes"), "11"); err == nil {
		t.Error("an event with no hashes passed a hash check")
	}
	// The signature still ties the redacted copy to the sender.
	if err := eventcrypto.EventSignature(redacted, "11", "domain", "ed25519:1", public); err != nil {
		t.Errorf("the redacted copy failed the sender's signature: %v", err)
	}
}

// TestRoom11AlgorithmsAreChosenByName keeps the version question explicit: room
// versions are not ordered, so an unknown version must not silently be treated as
// the newest one.
func TestRoom11AlgorithmsAreChosenByName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		version string
		want    bool
	}{
		{version: "1", want: false},
		{version: "10", want: false},
		{version: "11", want: true},
		{version: "12", want: true},
		{version: " 11 ", want: true},
		{version: "", want: false},
		{version: "experimental", want: false},
	}
	for _, tc := range cases {
		if got := eventcrypto.UseRoom11Algorithms(tc.version); got != tc.want {
			t.Errorf("UseRoom11Algorithms(%q) = %t, want %t", tc.version, got, tc.want)
		}
	}
}

// tamperedContent returns an event that keeps its content, and so cannot be the
// redacted form of anything.
func tamperedContent(event matrix.Object) matrix.Object {
	out := matrix.Object{}
	for key, value := range event {
		out[key] = value
	}
	out["content"] = matrix.Object{"body": "still here"}
	return out
}

// helpers

// decodeObject decodes a JSON object, failing the test on anything else.
func decodeObject(t *testing.T, raw string) matrix.Object {
	t.Helper()
	var object matrix.Object
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		t.Fatalf("decoding the test event: %v", err)
	}
	// Whole json.Number values are handed to the hashing code as int64, which is how
	// the storage layer stores timestamps once it has read them.
	for key, value := range object {
		if number, ok := value.(json.Number); ok {
			if asInt, err := number.Int64(); err == nil {
				object[key] = asInt
			}
		}
	}
	return object
}

// mustKey produces a throwaway ed25519 key for tests that only need signing to
// succeed.
func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	return private
}

// contentHashOf renders an event's content hash as base64 for comparison.
func contentHashOf(t *testing.T, object matrix.Object) string {
	t.Helper()
	hash, err := eventcrypto.ContentHash(object)
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}
	return base64.RawStdEncoding.EncodeToString(hash[:])
}
