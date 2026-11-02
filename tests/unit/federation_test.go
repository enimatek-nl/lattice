package unit

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The rules below decide whether a signature from another homeserver is believed.
// They are pure, and they are the part of federation that cannot be tested by
// watching two servers talk: a signature that is accepted when it should not be
// leaves no trace in either server's behaviour.

// TestServerNameParts pins the grammar of a server name. The colons of an IPv6
// literal are not port separators, and the default federation port applies only
// when no port was named, so a wrong split sends traffic somewhere else.
func TestServerNameParts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		serverName string
		host       string
		port       string
		hasPort    bool
	}{
		{serverName: "example.com", host: "example.com"},
		{serverName: "example.com:8448", host: "example.com", port: "8448", hasPort: true},
		{serverName: "example.com:443", host: "example.com", port: "443", hasPort: true},
		// An IPv6 literal in brackets, with and without a port.
		{serverName: "[::1]", host: "[::1]"},
		{serverName: "[::1]:9093", host: "[::1]", port: "9093", hasPort: true},
		{serverName: "[2001:db8::8a2e:370:7334]:8448", host: "[2001:db8::8a2e:370:7334]", port: "8448", hasPort: true},
		// An IPv6 literal without brackets: the colons belong to the address, and
		// the specification only allows a port on a bracketed literal.
		{serverName: "::1", host: "::1"},
		{serverName: "2001:db8::8a2e:370:7334", host: "2001:db8::8a2e:370:7334"},
		// Shapes that name no usable port. The name is kept whole so the caller can
		// still try it.
		{serverName: "example.com:", host: "example.com"},
		{serverName: "[::1", host: "[::1"},
		{serverName: "[::1]:", host: "[::1]"},
		{serverName: "", host: ""},
	}
	for _, tc := range cases {
		host, port, hasPort := federation.ServerNameParts(tc.serverName)
		if host != tc.host || port != tc.port || hasPort != tc.hasPort {
			t.Errorf("ServerNameParts(%q) = (%q, %q, %t), want (%q, %q, %t)",
				tc.serverName, host, port, hasPort, tc.host, tc.port, tc.hasPort)
		}
	}
}

// TestCanonicalJSONKeepsNumbersExact is the encoding half of signature
// verification. A federation timestamp that goes through float64 and comes out with
// different digits no longer matches the bytes the signature was made over, and a
// correctly signed request fails its own check.
func TestCanonicalJSONKeepsNumbersExact(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{name: "int64 timestamp", value: int64(1794137019869), want: `1794137019869`},
		{name: "int", value: 42, want: `42`},
		{name: "uint64", value: uint64(18446744073709551615), want: `18446744073709551615`},
		{name: "whole float", value: float64(1000), want: `1000`},
		{name: "fractional float", value: 1.5, want: `1.5`},
		{name: "json.Number from decoding", value: json.Number("1794137019869"), want: `1794137019869`},
		{name: "negative", value: -7, want: `-7`},
		{name: "zero", value: 0, want: `0`},
		{name: "object is sorted and compact", value: matrix.Object{"b": 1, "a": "x"}, want: `{"a":"x","b":1}`},
		{name: "array keeps its order", value: []any{"b", "a"}, want: `["b","a"]`},
	}
	for _, tc := range cases {
		got, err := matrix.CanonicalJSON(tc.value)
		if err != nil {
			t.Errorf("%s: CanonicalJSON returned %v", tc.name, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s: CanonicalJSON = %s, want %s", tc.name, got, tc.want)
		}
	}

	// A whole float that is not exactly representable must not be dressed up as an
	// integer: the sender signed those digits.
	if got, err := matrix.CanonicalJSON(float64(1794137019869.5)); err != nil || string(got) == "1794137019869" {
		t.Errorf("a fractional float was rounded to %s", got)
	}

	// The round trip a transport does: decode a peer's bytes, keep every digit, and
	// re-encode what was decoded.
	wire := `{"origin":"example.com","stream_id":1794137019869,"unsigned":{"age":1234567890123}}`
	decoded, err := matrix.DecodeCanonical([]byte(wire))
	if err != nil {
		t.Fatalf("DecodeCanonical: %v", err)
	}
	reencoded, err := matrix.CanonicalJSON(decoded)
	if err != nil {
		t.Fatalf("CanonicalJSON of a decoded document: %v", err)
	}
	if string(reencoded) != wire {
		t.Errorf("decoding and re-encoding changed the bytes:\n want %s\n  got %s", wire, reencoded)
	}
}

// TestAuthorizationHeaderRoundTrip pins the header a peer has to be able to read.
func TestAuthorizationHeaderRoundTrip(t *testing.T) {
	t.Parallel()
	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	fields, err := federation.SignAuthorization(key, "GET", "/_matrix/federation/v1/version?origin=origin.example",
		"origin.example", "destination.example", matrix.Object{})
	if err != nil {
		t.Fatalf("SignAuthorization: %v", err)
	}
	header := fields.AuthorizationHeader()
	if !strings.HasPrefix(header, "X-Matrix ") {
		t.Errorf("the header does not start with X-Matrix: %q", header)
	}
	for _, want := range []string{`origin="origin.example"`, `destination="destination.example"`,
		`key="` + key.ID + `"`, "sig="} {
		if !strings.Contains(header, want) {
			t.Errorf("the header %q lacks %s", header, want)
		}
	}

	parsed, err := federation.ParseAuthorization(header)
	if err != nil {
		t.Fatalf("ParseAuthorization(%q): %v", header, err)
	}
	if parsed.Origin != fields.Origin || parsed.Destination != fields.Destination ||
		parsed.KeyID != fields.KeyID || parsed.Signature != fields.Signature {
		t.Errorf("parsing the header lost information: %+v, want %+v", parsed, fields)
	}

	// The specification writes the parameters quoted; Synapse has been seen reading
	// them bare. Both have to parse, because a server that refuses a well-formed
	// request from an older peer just looks broken.
	bare := "X-Matrix origin=origin.example,destination=destination.example,key=" + key.ID + ",sig=" + fields.Signature
	if got, err := federation.ParseAuthorization(bare); err != nil {
		t.Errorf("a bare parameter list failed to parse: %v", err)
	} else if got.Origin != "origin.example" || got.KeyID != key.ID {
		t.Errorf("a bare parameter list parsed as %+v", got)
	}

	bad := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "another scheme", value: "Bearer abc"},
		{name: "no parameters", value: "X-Matrix "},
		{name: "no signature", value: `X-Matrix origin="a",destination="b",key="ed25519:x"`},
		{name: "no origin", value: `X-Matrix destination="b",key="ed25519:x",sig="y"`},
		{name: "no key", value: `X-Matrix origin="a",destination="b",sig="y"`},
		{name: "unterminated quote", value: `X-Matrix origin="a,destination="b",key="ed25519:x",sig="y"`},
	}
	for _, tc := range bad {
		if _, err := federation.ParseAuthorization(tc.value); err == nil {
			t.Errorf("%s: ParseAuthorization(%q) accepted it", tc.name, tc.value)
		}
	}
}

// TestAuthorizationVerification covers the signature check itself, including the
// shapes that must fail. A federation request is authenticated by exactly this
// check, so a false accept is the whole security model failing at once.
func TestAuthorizationVerification(t *testing.T) {
	t.Parallel()
	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	content := matrix.Object{"edus": []any{}, "pdus": []any{}}
	const uri = "/_matrix/federation/v1/send/1700000000000/transaction"

	signed, err := federation.SignAuthorization(key, "PUT", uri, "origin.example", "destination.example", content)
	if err != nil {
		t.Fatalf("SignAuthorization: %v", err)
	}
	if err := federation.VerifyAuthorization(signed, "PUT", uri, content, keyPairPublic(t, key)); err != nil {
		t.Fatalf("a correctly signed request failed its own signature: %v", err)
	}

	other, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}

	cases := []struct {
		name      string
		fields    federation.AuthFields
		method    string
		uri       string
		content   matrix.Object
		publicKey ed25519.PublicKey
	}{
		{
			name:      "a different method",
			fields:    signed,
			method:    "GET",
			uri:       uri,
			content:   content,
			publicKey: keyPairPublic(t, key),
		},
		{
			name:      "a different request target",
			fields:    signed,
			method:    "PUT",
			uri:       uri + "/later",
			content:   content,
			publicKey: keyPairPublic(t, key),
		},
		{
			name:      "a different query string",
			fields:    signed,
			method:    "PUT",
			uri:       uri + "?foo=bar",
			content:   content,
			publicKey: keyPairPublic(t, key),
		},
		{
			name:      "content that was not signed",
			fields:    signed,
			method:    "PUT",
			uri:       uri,
			content:   matrix.Object{"edus": []any{}, "pdus": []any{"forged"}},
			publicKey: keyPairPublic(t, key),
		},
		{
			name:      "a claimed origin nobody signed for",
			fields:    withOrigin(signed, "victim.example"),
			method:    "PUT",
			uri:       uri,
			content:   content,
			publicKey: keyPairPublic(t, key),
		},
		{
			name:      "a claimed destination nobody signed for",
			fields:    withDestination(signed, "victim.example"),
			method:    "PUT",
			uri:       uri,
			content:   content,
			publicKey: keyPairPublic(t, key),
		},
		{
			name:      "the signature of another key",
			fields:    withSignature(signed, other),
			method:    "PUT",
			uri:       uri,
			content:   content,
			publicKey: keyPairPublic(t, key),
		},
		{
			name:      "verified with the wrong key",
			fields:    signed,
			method:    "PUT",
			uri:       uri,
			content:   content,
			publicKey: keyPairPublic(t, other),
		},
		{
			name:      "a truncated signature",
			fields:    withSignatureValue(signed, signed.Signature[:20]),
			method:    "PUT",
			uri:       uri,
			content:   content,
			publicKey: keyPairPublic(t, key),
		},
		{
			name:      "no signature at all",
			fields:    withSignatureValue(signed, ""),
			method:    "PUT",
			uri:       uri,
			content:   content,
			publicKey: keyPairPublic(t, key),
		},
	}
	for _, tc := range cases {
		if err := federation.VerifyAuthorization(tc.fields, tc.method, tc.uri, tc.content, tc.publicKey); err == nil {
			t.Errorf("%s: the request was accepted", tc.name)
		}
	}
}

// TestGETSignsNoContentMember pins the shape of a signed GET, because the two
// implementations that matter sign it by leaving the content member out: Synapse
// writes the member only for non-GETs, and gomatrixserverlib marshals the field
// with "omitempty". A homeserver that invents an empty object there verifies a
// different document from the one the peer signed, so it refuses every signed GET
// that peer sends - make_join among them, which is where a federated join starts -
// and gets its own signed GETs refused in return.
func TestGETSignsNoContentMember(t *testing.T) {
	t.Parallel()
	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	const uri = "/_matrix/federation/v1/make_join/%21room%3Aorigin.example/%40user%3Adestination.example?ver=10&ver=11"

	// The document as a peer that omits empty fields signs it: no content member.
	peer := matrix.Object{
		"method":      "GET",
		"uri":         uri,
		"origin":      "origin.example",
		"destination": "destination.example",
	}
	sig, err := key.Sign(peer)
	if err != nil {
		t.Fatalf("signing the peer's document: %v", err)
	}
	fields := federation.AuthFields{
		Origin:      "origin.example",
		Destination: "destination.example",
		KeyID:       key.ID,
		Signature:   sig,
	}
	if err := federation.VerifyAuthorization(fields, "GET", uri, nil, keyPairPublic(t, key)); err != nil {
		t.Errorf("a peer's GET, which carries no content member, did not verify: %v", err)
	}
	// Content passed for a GET is ignored rather than invented into the signed
	// document, so a caller that hands over a parsed body cannot break the check.
	if err := federation.VerifyAuthorization(fields, "GET", uri, matrix.Object{}, keyPairPublic(t, key)); err != nil {
		t.Errorf("a GET signed with no content did not verify against an empty object: %v", err)
	}

	// Lattice's own signed GET has to be the same document, not one with an empty
	// object added.
	mine, err := federation.SignAuthorization(key, "GET", uri, "origin.example", "destination.example", nil)
	if err != nil {
		t.Fatalf("SignAuthorization: %v", err)
	}
	if mine.Signature != sig {
		t.Error("a signed GET is not the document a peer signs: Lattice adds something to it")
	}

	// A non-GET with nothing to send signs no member either, and must not be
	// verified against an object that was never sent.
	bodyless, err := federation.SignAuthorization(key, "PUT", "/_matrix/federation/v1/send_join/%21room%3Aorigin.example/$event",
		"origin.example", "destination.example", nil)
	if err != nil {
		t.Fatalf("SignAuthorization: %v", err)
	}
	if err := federation.VerifyAuthorization(bodyless, "PUT", "/_matrix/federation/v1/send_join/%21room%3Aorigin.example/$event",
		matrix.Object{}, keyPairPublic(t, key)); err == nil {
		t.Error("a PUT with no body verified against a body that was never sent")
	}
}

// TestKeyDocumentVerification pins what a peer's published keys have to prove
// before a signature made with them is believed.
func TestKeyDocumentVerification(t *testing.T) {
	t.Parallel()
	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	nowMS := time.Now().UnixMilli()
	valid, err := federation.KeyDocument("origin.example", key, nowMS+24*60*60*1000)
	if err != nil {
		t.Fatalf("KeyDocument: %v", err)
	}

	t.Run("a well formed document verifies", func(t *testing.T) {
		set, err := federation.VerifyKeyDocument(valid, "origin.example", nowMS)
		if err != nil {
			t.Fatalf("VerifyKeyDocument: %v", err)
		}
		if set.ServerName != "origin.example" {
			t.Errorf("the verified set names %q", set.ServerName)
		}
		if len(set.KeyIDs()) != 1 {
			t.Errorf("the verified set carries %d keys, want 1", len(set.KeyIDs()))
		}
	})

	cases := []struct {
		name     string
		document matrix.Object
		askedFor string
	}{
		{
			name:     "the document is for somebody else",
			document: valid,
			askedFor: "victim.example",
		},
		{
			name:     "the document names no server",
			document: without(valid, "server_name"),
			askedFor: "origin.example",
		},
		{
			name:     "the document has expired",
			document: withField(valid, "valid_until_ts", int64(nowMS)-1),
			askedFor: "origin.example",
		},
		{
			name:     "the document carries no signature",
			document: without(valid, "signatures"),
			askedFor: "origin.example",
		},
		{
			name:     "the signature is by somebody else",
			document: withField(valid, "signatures", matrix.Object{"victim.example": matrix.Object{key.ID: "AAAA"}}),
			askedFor: "origin.example",
		},
		{
			name:     "the signature does not match the published key",
			document: withField(valid, "signatures", matrix.Object{"origin.example": matrix.Object{key.ID: signatureOfAnotherKey(t)}}),
			askedFor: "origin.example",
		},
		{
			name:     "no keys are published",
			document: withField(valid, "verify_keys", matrix.Object{}),
			askedFor: "origin.example",
		},
		{
			name:     "the keys field is not an object",
			document: withField(valid, "verify_keys", "ed25519:aaaa"),
			askedFor: "origin.example",
		},
		{
			name:     "a key entry has no key",
			document: withField(valid, "verify_keys", matrix.Object{key.ID: matrix.Object{}}),
			askedFor: "origin.example",
		},
		{
			name:     "a key id names another algorithm",
			document: withField(valid, "verify_keys", matrix.Object{"rsa:aaaa": matrix.Object{"key": "AAAA"}}),
			askedFor: "origin.example",
		},
		{
			name:     "a key id has no algorithm prefix",
			document: withField(valid, "verify_keys", matrix.Object{"aaaa": matrix.Object{"key": "AAAA"}}),
			askedFor: "origin.example",
		},
		{
			name:     "a key version uses a character the specification forbids",
			document: withField(valid, "verify_keys", matrix.Object{"ed25519:aa+bb": matrix.Object{"key": "AAAA"}}),
			askedFor: "origin.example",
		},
		{
			name:     "the published key is not an ed25519 key",
			document: withField(valid, "verify_keys", matrix.Object{"ed25519:aaaa": matrix.Object{"key": "AAAA"}}),
			askedFor: "origin.example",
		},
		{
			name:     "valid_until_ts is missing",
			document: without(valid, "valid_until_ts"),
			askedFor: "origin.example",
		},
	}
	for _, tc := range cases {
		if _, err := federation.VerifyKeyDocument(tc.document, tc.askedFor, nowMS); err == nil {
			t.Errorf("%s: the document was accepted", tc.name)
		}
	}
}

// TestKeyValidityIsCapped pins the seven day rule. A peer may publish a key that
// claims validity for a year; trusting that claim is what lets a stolen key keep
// signing for months.
func TestKeyValidityIsCapped(t *testing.T) {
	t.Parallel()
	nowMS := time.Now().UnixMilli()
	cases := []struct {
		name         string
		validUntilMS int64
		wantMS       int64
	}{
		{
			name:         "a document that expires sooner is trusted for less",
			validUntilMS: nowMS + 60_000,
			wantMS:       nowMS + 60_000,
		},
		{
			name:         "a document that claims a year is capped at seven days",
			validUntilMS: nowMS + 365*24*60*60*1000,
			wantMS:       nowMS + federation.MaxKeyValidity.Milliseconds(),
		},
	}
	for _, tc := range cases {
		set := &federation.KeySet{ValidUntilMS: tc.validUntilMS}
		if got := set.Expires(nowMS); got != tc.wantMS {
			t.Errorf("%s: Expires = %d, want %d", tc.name, got, tc.wantMS)
		}
	}
}

// TestSigningKeyRoundTrip pins that a stored keypair reads back as the same key.
// The private half is stored, so this is the difference between a server that can
// still sign after a restart and one that has lost its identity.
func TestSigningKeyRoundTrip(t *testing.T) {
	t.Parallel()
	original, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	if !strings.HasPrefix(original.ID, "ed25519:") {
		t.Errorf("the key id %q does not name the algorithm", original.ID)
	}
	if len(original.Version) == 0 {
		t.Error("the key has no version part")
	}

	restored, err := federation.KeyPairFromStorage(original.ID, original.PublicKey(), original.Seed())
	if err != nil {
		t.Fatalf("KeyPairFromStorage: %v", err)
	}
	if restored.ID != original.ID {
		t.Errorf("the restored key has id %s, want %s", restored.ID, original.ID)
	}
	object := matrix.Object{"server_name": "origin.example", "valid_until_ts": int64(1)}
	first, err := original.Sign(object)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	second, err := restored.Sign(object)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if first != second {
		t.Errorf("the restored key signs differently: %s then %s", first, second)
	}

	bad := []struct {
		name      string
		keyID     string
		publicKey string
		seed      string
	}{
		{name: "no key id", keyID: "", publicKey: "AAAA", seed: "AAAA"},
		{name: "a key id without an algorithm", keyID: "aaaa", publicKey: "AAAA", seed: "AAAA"},
		{name: "another algorithm", keyID: "rsa:aaaa", publicKey: "AAAA", seed: "AAAA"},
		{name: "a key that does not match its id", keyID: "ed25519:aaaa", publicKey: "AAAA", seed: "AAAA"},
	}
	for _, tc := range bad {
		if _, err := federation.KeyPairFromStorage(tc.keyID, tc.publicKey, tc.seed); err == nil {
			t.Errorf("%s: KeyPairFromStorage accepted it", tc.name)
		}
	}
}

// helpers

// keyPairPublic reads the public half of a federation keypair.
func keyPairPublic(t *testing.T, key federation.KeyPair) ed25519.PublicKey {
	t.Helper()
	publicKey, err := matrix.DecodeEd25519Key(key.PublicKey())
	if err != nil {
		t.Fatalf("decoding the public key of %s: %v", key.ID, err)
	}
	return publicKey
}

// withOrigin returns the fields with a different origin claimed afterwards.
func withOrigin(fields federation.AuthFields, origin string) federation.AuthFields {
	fields.Origin = origin
	return fields
}

// withDestination returns the fields with a different destination claimed
// afterwards.
func withDestination(fields federation.AuthFields, destination string) federation.AuthFields {
	fields.Destination = destination
	return fields
}

// withSignature returns the fields with a signature made by another key.
func withSignature(fields federation.AuthFields, other federation.KeyPair) federation.AuthFields {
	fields.Signature = signatureOfKey(other)
	return fields
}

// withSignatureValue returns the fields with a literal signature value.
func withSignatureValue(fields federation.AuthFields, signature string) federation.AuthFields {
	fields.Signature = signature
	return fields
}

// signatureOfKey signs a fixed object with a keypair, which is enough to produce a
// well formed but unrelated signature.
func signatureOfKey(key federation.KeyPair) string {
	signature, err := key.Sign(matrix.Object{"purpose": "an unrelated object"})
	if err != nil {
		panic("signing with a generated key: " + err.Error())
	}
	return signature
}

// signatureOfAnotherKey produces a base64 signature from a key that is not part of
// the document.
func signatureOfAnotherKey(t *testing.T) string {
	t.Helper()
	other, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	raw, err := base64.RawStdEncoding.DecodeString(signatureOfKey(other))
	if err != nil {
		t.Fatalf("a generated signature is not base64: %v", err)
	}
	return base64.RawStdEncoding.EncodeToString(raw)
}

// without returns a copy of an object with a field removed.
func without(object matrix.Object, field string) matrix.Object {
	out := matrix.Object{}
	for key, value := range object {
		if key == field {
			continue
		}
		out[key] = value
	}
	return out
}

// withField returns a copy of an object with a field replaced or added.
func withField(object matrix.Object, field string, value any) matrix.Object {
	out := matrix.Object{}
	for key, existing := range object {
		out[key] = existing
	}
	out[field] = value
	return out
}

// fixedIdentity is a server that always signs with one key: enough to drive the
// transport, which only needs a name and a keypair.
type fixedIdentity struct {
	name string
	key  federation.KeyPair
}

func (f fixedIdentity) ServerName() string { return f.name }

func (f fixedIdentity) SigningKey(context.Context) (federation.KeyPair, error) { return f.key, nil }

// TestTransportMergesAPathQuery pins that a request whose path already carries a
// query goes out with one query string, not two. The transport adds its own origin
// parameter, and appending it to a path that had a query glued it onto the last
// value: a make_join offering room version 11 arrived offering "11?origin=", and the
// peer answered that no room version was shared - which reads like a version
// disagreement and is a malformed request.
func TestTransportMergesAPathQuery(t *testing.T) {
	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	identity := fixedIdentity{name: "origin.example", key: key}

	var target string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target = r.URL.RequestURI()
		fields, err := federation.ParseAuthorization(r.Header.Get("Authorization"))
		if err != nil {
			t.Errorf("the transport sent an unreadable authorization header: %v", err)
		}
		// The peer verifies over the target it received, so the transport has to
		// have signed exactly this string.
		if err := federation.VerifyAuthorization(fields, r.Method, target, nil, key.Public); err != nil {
			t.Errorf("the transport's own signature does not verify over %s: %v", target, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"room_version":"11","event":{}}`))
	}))
	defer server.Close()

	transport := federation.NewTransport(identity, federation.Config{
		PeerBaseURLs: map[string]string{"peer.example": server.URL},
	})
	query := url.Values{}
	path := "/_matrix/federation/v1/make_join/%21room%3Apeer.example/%40user%3Aorigin.example?"
	for _, version := range []string{"1", "11"} {
		query.Add("ver", version)
		path += "ver=" + version + "&"
	}
	if _, err := transport.Get(context.Background(), "peer.example", strings.TrimSuffix(path, "&"), nil); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got := strings.Count(target, "?"); got != 1 {
		t.Fatalf("the request target carries %d query separators: %s", got, target)
	}
	parsed, err := url.ParseRequestURI(target)
	if err != nil {
		t.Fatalf("parsing the request target %s: %v", target, err)
	}
	if got := parsed.Query()["ver"]; len(got) != 2 || got[0] != "1" || got[1] != "11" {
		t.Errorf("the room versions arrived as %q, wanted [1 11]", got)
	}
	if got := parsed.Query().Get("origin"); got != "origin.example" {
		t.Errorf("the query named origin %q, wanted the sending server", got)
	}
}

// TestPeerAuthRefusalIsNotOurOwn covers what a client is told when another homeserver
// refuses to believe this server's signature. The peer answers M_UNAUTHORIZED, meaning
// "I cannot identify you"; a client handed that code unchanged reads it as "your access
// token is no good" and acts on it - logging the user out over a room that would not open.
// The refusal is about this server, so it is answered as a bad gateway that names the peer.
func TestPeerAuthRefusalIsNotOurOwn(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errcode":"M_UNAUTHORIZED","error":"no signing keys available for origin.example"}`))
	}))
	defer peer.Close()

	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	transport := federation.NewTransport(fixedIdentity{name: "origin.example", key: key}, federation.Config{
		PeerBaseURLs: map[string]string{"peer.example": peer.URL},
	})
	if _, err := transport.Get(context.Background(), "peer.example",
		"/_matrix/federation/v1/query/directory", nil); err == nil {
		t.Fatal("the transport reported no error for a refusal from the peer")
	} else if matrix.HasCode(err, matrix.ErrCodeUnauthorized) {
		t.Errorf("a peer's M_UNAUTHORIZED was passed on as this server's: %v", err)
	} else if peerErr, ok := err.(*matrix.Error); !ok {
		t.Errorf("the transport returned %T, want a *matrix.Error", err)
	} else {
		if peerErr.Status != http.StatusBadGateway {
			t.Errorf("the client is answered with %d, want %d: %s",
				peerErr.Status, http.StatusBadGateway, peerErr.Message)
		}
		if !strings.Contains(peerErr.Message, "peer.example") {
			t.Errorf("the answer %q does not name the server that refused", peerErr.Message)
		}
	}
}
