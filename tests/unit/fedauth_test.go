package unit

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/federation"
	"github.com/enimatek/lattice/internal/matrixjson"
)

// fedSigner is a homeserver's signing key without the key file, database and
// configuration that come with one.
type fedSigner struct {
	name    string
	keyID   string
	private ed25519.PrivateKey
}

func newFedSigner(t *testing.T, name string) *fedSigner {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a signing key: %v", err)
	}
	return &fedSigner{name: name, keyID: matrixjson.KeyID("ed25519", "autotest"), private: private}
}

func (s *fedSigner) KeyID() string { return s.keyID }

func (s *fedSigner) SignJSON(obj map[string]any) error {
	return matrixjson.SignJSONInPlace(s.name, s.keyID, s.private, obj)
}

// lookup answers only for the given server, the way a keyring that has never seen a
// server answers.
func (s *fedSigner) lookup() func(origin, keyID string) (ed25519.PublicKey, error) {
	return func(origin, keyID string) (ed25519.PublicKey, error) {
		if origin != s.name || keyID != s.keyID {
			return nil, ErrNoKeyHere
		}
		return s.private.Public().(ed25519.PublicKey), nil
	}
}

// ErrNoKeyHere is what a keyring says about a server it has no keys for.
var ErrNoKeyHere = errors.New("no keys for that server")

// TestFederationRequestSignature signs a request and checks it, the way two homeservers
// do on every call.
func TestFederationRequestSignature(t *testing.T) {
	signer := newFedSigner(t, "origin.example.org")
	const destination = "destination.example.org"
	body := []byte(`{"origin":"origin.example.org","pdus":[],"origin_server_ts":1234567890}`)

	header, err := federation.AuthorizationHeader(signer.name, signer, destination,
		http.MethodPut, "/_matrix/federation/v1/send/txn7", body)
	if err != nil {
		t.Fatalf("signing a request: %v", err)
	}
	if !strings.HasPrefix(header, "X-Matrix ") {
		t.Errorf("the header starts %q, want the X-Matrix scheme", header[:10])
	}

	request, err := http.NewRequest(http.MethodPut, "https://destination.example.org/_matrix/federation/v1/send/txn7", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	request.Header.Set("Authorization", header)
	auth, signed, err := federation.VerifyRequest(request, destination, signer.lookup())
	if err != nil {
		t.Fatalf("verifying a signed request: %v", err)
	}
	if auth.Origin != signer.name || auth.Key != signer.keyID {
		t.Errorf("the credential names %s/%s, want %s/%s", auth.Origin, auth.Key, signer.name, signer.keyID)
	}
	if !bytes.Equal(signed, body) {
		t.Errorf("verification returned a different body than the request carried, so the handler would parse something else")
	}

	// A query string is part of the signed target.
	header, err = federation.AuthorizationHeader(signer.name, signer, destination,
		http.MethodGet, "/_matrix/federation/v1/state/%21room%3Aexample.org?ver=$ev", nil)
	if err != nil {
		t.Fatalf("signing a GET: %v", err)
	}
	request, err = http.NewRequest(http.MethodGet, "https://destination.example.org/_matrix/federation/v1/state/%21room%3Aexample.org?ver=$ev", nil)
	if err != nil {
		t.Fatalf("building the GET: %v", err)
	}
	request.Header.Set("Authorization", header)
	if _, _, err := federation.VerifyRequest(request, destination, signer.lookup()); err != nil {
		t.Errorf("a signed GET with a query did not verify: %v", err)
	}
	// The same signature does not cover the same path without the query.
	request.URL.RawQuery = ""
	request.Header.Set("Authorization", header)
	if _, _, err := federation.VerifyRequest(request, destination, signer.lookup()); err == nil {
		t.Errorf("a signature over a target with a query was accepted for the target without it")
	}
}

// TestFederationRequestRefused checks the ways a credential must not be believed.
func TestFederationRequestRefused(t *testing.T) {
	signer := newFedSigner(t, "origin.example.org")
	attacker := newFedSigner(t, "attacker.example.org")
	const destination = "destination.example.org"
	body := []byte(`{"pdus":[]}`)

	// signed builds a credential for the given request, so a case can change one thing
	// about the request without changing the signature that covered the original.
	signed := func(signer *fedSigner, method, target string, signedBody []byte) string {
		t.Helper()
		header, err := federation.AuthorizationHeader(signer.name, signer, destination, method, target, signedBody)
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
		return header
	}

	unknown := func(origin, keyID string) (ed25519.PublicKey, error) { return nil, ErrNoKeyHere }

	cases := []struct {
		name    string
		method  string
		target  string
		body    []byte
		header  string
		lookup  func(origin, keyID string) (ed25519.PublicKey, error)
		wantErr string
	}{
		{
			name:    "no credential at all",
			wantErr: "no X-Matrix authorization",
		},
		{
			name:    "the wrong scheme",
			header:  `Bearer something`,
			wantErr: "not X-Matrix",
		},
		{
			name:    "no signature",
			header:  `X-Matrix origin="origin.example.org",key="ed25519:autotest"`,
			wantErr: "missing origin, key or sig",
		},
		{
			name:    "signed by a key the sender does not publish",
			body:    body,
			header:  signed(attacker, http.MethodPut, "/t", body),
			lookup:  signer.lookup(),
			wantErr: "not signed by a key that server published",
		},
		{
			name:   "addressed to another homeserver",
			body:   body,
			target: "/t",
			header: func() string {
				header, err := federation.AuthorizationHeader(signer.name, signer, "elsewhere.example.org",
					http.MethodPut, "/t", body)
				if err != nil {
					t.Fatalf("signing: %v", err)
				}
				return header
			}(),
			lookup:  signer.lookup(),
			wantErr: "addressed to",
		},
		{
			name:    "the body changed on the way",
			target:  "/t",
			body:    []byte(`{"pdus":[{"room_id":"!a:example.org"}]}`),
			header:  signed(signer, http.MethodPut, "/t", body),
			lookup:  signer.lookup(),
			wantErr: "not signed by a key",
		},
		{
			name:    "a body that is not JSON",
			target:  "/t",
			body:    []byte(`not json`),
			header:  signed(signer, http.MethodPut, "/t", body),
			lookup:  signer.lookup(),
			wantErr: "not JSON",
		},
		{
			name:    "no keys known for the sender",
			target:  "/t",
			body:    body,
			header:  signed(signer, http.MethodPut, "/t", body),
			lookup:  unknown,
			wantErr: "not signed by a key",
		},
		{
			name:    "the method changed",
			target:  "/t",
			body:    body,
			method:  http.MethodPost,
			header:  signed(signer, http.MethodPut, "/t", body),
			lookup:  signer.lookup(),
			wantErr: "not signed by a key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodPut
			}
			target := tc.target
			if target == "" {
				target = "/_matrix/federation/v1/send/txn"
			}
			request, err := http.NewRequest(method, "https://destination.example.org"+target, bytes.NewReader(tc.body))
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			if tc.header != "" {
				request.Header.Set("Authorization", tc.header)
			}
			lookup := tc.lookup
			if lookup == nil {
				lookup = signer.lookup()
			}
			_, _, err = federation.VerifyRequest(request, destination, lookup)
			if err == nil {
				t.Fatalf("the credential was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the refusal says %q, want it to mention %q", err, tc.wantErr)
			}
			if len(tc.body) > 0 {
				got, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatalf("reading the body back: %v", err)
				}
				if !bytes.Equal(got, tc.body) {
					t.Errorf("the handler would see %q, want %q", got, tc.body)
				}
			}
		})
	}
}

// TestFederationHeaderParsing checks the header grammar, which older servers write in
// more than one way.
func TestFederationHeaderParsing(t *testing.T) {
	const want = "ed25519:abc"
	for _, header := range []string{
		`X-Matrix origin="example.org",destination="other.org",key="ed25519:abc",sig="SIG"`,
		`X-MATRIX origin="example.org", destination="other.org", key="ed25519:abc", sig="SIG"`,
		`X-Matrix origin=example.org,destination=other.org,key=ed25519:abc,sig=SIG,extra="ignored"`,
		`X-Matrix origin="example.org",key="ed25519:abc",signature="SIG"`,
		`X-Matrix origin="exa\"mple.org",key="ed25519:abc",sig="SIG"`,
	} {
		auth, err := federation.ParseAuthorization(header)
		if err != nil {
			t.Errorf("%s: %v", header, err)
			continue
		}
		if auth.Key != want {
			t.Errorf("%s: the key is %q, want %q", header, auth.Key, want)
		}
		if auth.Signature != "SIG" {
			t.Errorf("%s: the signature is %q, want SIG", header, auth.Signature)
		}
	}
	// The escaped name is unescaped, as RFC 9110 requires.
	auth, err := federation.ParseAuthorization(`X-Matrix origin="exa\"mple.org",key="ed25519:abc",sig="SIG"`)
	if err != nil {
		t.Fatalf("parsing an escaped value: %v", err)
	}
	if auth.Origin != `exa"mple.org` {
		t.Errorf("the origin is %q, want the escaped quote resolved", auth.Origin)
	}
	// An unterminated quote is a malformed value, not a value with a quote in it.
	if _, err := federation.ParseAuthorization(`X-Matrix origin="example.org,key="ed25519:abc",sig="SIG"`); err == nil {
		t.Errorf("an unterminated quote was accepted")
	}
	// A credential that cannot be checked at all says so.
	for _, header := range []string{
		`X-Matrix origin="example.org",key="ed25519:abc"`,
		`X-Matrix key="ed25519:abc",sig="SIG"`,
		`X-Matrix origin="example.org",sig="SIG"`,
	} {
		if _, err := federation.ParseAuthorization(header); err == nil {
			t.Errorf("%s was accepted", header)
		}
	}
}

// TestFederationSignatureCoversNumbers checks that a body whose numbers and members are
// spelled in an unusual but legal way still verifies, because the signature is over the
// canonical form of the parsed body, not the bytes. Canonical JSON allows only integers,
// which is what makes that possible at all.
func TestFederationSignatureCoversNumbers(t *testing.T) {
	signer := newFedSigner(t, "origin.example.org")
	const destination = "destination.example.org"
	// The same JSON, spelled differently: a receiver that re-encodes the parsed body
	// canonically signs and verifies the same thing either way.
	for _, spelling := range []string{
		`{"origin_server_ts":1234567890,"pdus":[],"depth":3}`,
		`{"pdus":[],"depth":3,"origin_server_ts":  1234567890 }`,
	} {
		header, err := federation.AuthorizationHeader(signer.name, signer, destination, http.MethodPut, "/t", []byte(spelling))
		if err != nil {
			t.Fatalf("signing %s: %v", spelling, err)
		}
		request, _ := http.NewRequest(http.MethodPut, "https://destination.example.org/t", bytes.NewReader([]byte(spelling)))
		request.Header.Set("Authorization", header)
		if _, _, err := federation.VerifyRequest(request, destination, signer.lookup()); err != nil {
			t.Errorf("a signature over %s did not verify: %v", spelling, err)
		}
	}
	// A number that changes when re-encoded is a different signed object.
	var decoded any
	if err := json.Unmarshal([]byte(`{"sort":1e2}`), &decoded); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	canonical, err := matrixjson.Canonical(decoded)
	if err != nil {
		t.Fatalf("canonicalising: %v", err)
	}
	if string(canonical) == `{"sort":1e2}` {
		t.Errorf("canonical encoding left the exponent in, so the signed form is not what the sender hashed")
	}
}
