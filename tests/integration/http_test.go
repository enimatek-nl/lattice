package integration

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/matrixjson"
)

// TestVersionsStructure mirrors Complement's TestVersionStructure.
func TestVersionsStructure(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	res := h.get("/_matrix/client/versions", "")
	h.mustStatus(t, res, http.StatusOK)
	body := res.decoded(t)

	versions, ok := body["versions"].([]any)
	if !ok || len(versions) == 0 {
		t.Fatalf("versions missing: %s", res.body)
	}
	for _, v := range versions {
		value, ok := v.(string)
		if !ok {
			t.Fatalf("versions entry is not a string: %v", v)
		}
		if !strings.HasPrefix(value, "v") || !strings.Contains(value, ".") {
			t.Errorf("versions entry %q does not look like a global version", value)
		}
	}
	if features, ok := body["unstable_features"]; ok {
		if _, isObject := features.(map[string]any); !isObject {
			t.Errorf("unstable_features must be an object, got %T", features)
		}
	}
	// The pinned specification must be advertised.
	found := false
	for _, v := range versions {
		if v == "v1.19" {
			found = true
		}
	}
	if !found {
		t.Errorf("the pinned specification version v1.19 is not advertised: %s", res.body)
	}
}

// TestWellKnown covers both discovery documents when they are configured.
func TestWellKnown(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Default configuration publishes nothing: the endpoints answer with a Matrix
	// error rather than an HTML 404.
	client := h.get("/.well-known/matrix/client", "")
	if client.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", client.status)
	}
	if code := client.errcode(t); code != "M_NOT_FOUND" {
		t.Errorf("errcode = %s, want M_NOT_FOUND", code)
	}

	published := newHarnessWith(t, t.TempDir(), func(c *config.Config) {
		c.WellKnown.ClientBaseURL = "https://matrix.example.org"
		c.WellKnown.ServerBaseURL = "matrix.example.org:8448"
	})
	clientDoc := published.get("/.well-known/matrix/client", "")
	published.mustStatus(t, clientDoc, http.StatusOK)
	if got := clientDoc.decoded(t)["m.homeserver"]; got == nil {
		t.Errorf("m.homeserver missing: %s", clientDoc.body)
	}
	serverDoc := published.get("/.well-known/matrix/server", "")
	published.mustStatus(t, serverDoc, http.StatusOK)
	if got := serverDoc.decoded(t)["m.server"]; got != "matrix.example.org:8448" {
		t.Errorf("m.server = %v", got)
	}
}

// TestFederationVersion mirrors Complement's "PUT /_matrix/federation/v1/version
// → 405" and the version endpoint itself.
func TestFederationVersion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	res := h.federationGet(t, "/_matrix/federation/v1/version")
	h.mustStatus(t, res, http.StatusOK)
	server, ok := res.decoded(t)["server"].(map[string]any)
	if !ok {
		t.Fatalf("server member missing: %s", res.body)
	}
	if server["name"] != "lattice" {
		t.Errorf("server.name = %v", server["name"])
	}
	if version, ok := server["version"].(string); !ok || version == "" {
		t.Errorf("server.version must be a non-empty string, got %v", server["version"])
	}
}

// TestServerKeysVerifies checks GET /_matrix/key/v2/server against the procedure
// the Server-Server specification defines: the document must carry the server
// name, a verify key whose id is "ed25519:<key>", and a signature that verifies
// over the canonical form of the document without "signatures".
func TestServerKeysVerifies(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	res := h.federationGet(t, "/_matrix/key/v2/server")
	h.mustStatus(t, res, http.StatusOK)
	doc := res.decoded(t)

	if doc["server_name"] != "hs1" {
		t.Errorf("server_name = %v, want hs1", doc["server_name"])
	}
	if _, ok := doc["verify_keys"].(map[string]any); !ok {
		t.Fatalf("verify_keys missing: %s", res.body)
	}
	verifyKeys := doc["verify_keys"].(map[string]any)
	if len(verifyKeys) == 0 {
		t.Fatalf("verify_keys is empty")
	}
	var keyID string
	var encoded any
	for id, value := range verifyKeys {
		keyID, encoded = id, value
	}
	if !strings.HasPrefix(keyID, "ed25519:") {
		t.Errorf("key id %q must be of the form ed25519:<identifier>", keyID)
	}
	encodedKey, ok := encoded.(map[string]any)
	if !ok {
		t.Fatalf("verify key entry is not an object: %v", encoded)
	}
	raw, ok := encodedKey["key"].(string)
	if !ok || raw == "" {
		t.Fatalf("verify key material missing: %v", encodedKey)
	}
	keyBytes, err := matrixjson.DecodeUnpadded(raw)
	if err != nil {
		t.Fatalf("verify key is not unpadded base64: %v", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		t.Fatalf("verify key has %d bytes, want %d", len(keyBytes), ed25519.PublicKeySize)
	}
	if _, ok := doc["valid_until_ts"].(float64); !ok {
		t.Errorf("valid_until_ts missing or not a number: %v", doc["valid_until_ts"])
	}

	result, err := matrixjson.VerifyJSON(doc, func(origin, id string) (ed25519.PublicKey, error) {
		if origin != "hs1" || id != keyID {
			return nil, fmt.Errorf("unknown key %s/%s", origin, id)
		}
		return ed25519.PublicKey(keyBytes), nil
	})
	if err != nil {
		t.Fatalf("verification failed: %v", err)
	}
	if !result.VerifiedAll([]string{"hs1"}) {
		t.Errorf("the server key document does not verify with its own key: %+v", result)
	}

	// And it must verify over the bytes this server actually sent, not merely over an
	// equivalent rendering of them. A peer that takes the body, removes "signatures" and
	// checks what is left is doing the honest thing; an encoder that appends a newline,
	// escapes a character the signing rules leave alone, or writes a number differently
	// produces a document that fails that check with a perfectly good key.
	if canonical, err := matrixjson.Canonical(doc); err != nil {
		t.Fatalf("canonicalising the document: %v", err)
	} else if !bytes.Equal(res.body, canonical) {
		t.Errorf("the key document is not the canonical form its signature was made over:\n  sent:      %s\n  canonical: %s",
			res.body, canonical)
	}
	decodedBody, err := matrixjson.Decode(res.body)
	if err != nil {
		t.Fatalf("decoding the body: %v", err)
	}
	withoutSignature, ok := decodedBody.(map[string]any)
	if !ok {
		t.Fatalf("the key document is not a JSON object: %s", res.body)
	}
	signatures, _ := withoutSignature["signatures"].(map[string]any)
	byKey, _ := signatures["hs1"].(map[string]any)
	rawSignature, _ := byKey[keyID].(string)
	if rawSignature == "" {
		t.Fatalf("no signature under %s in %s", keyID, res.body)
	}
	signature, err := matrixjson.DecodeUnpadded(rawSignature)
	if err != nil {
		t.Fatalf("the signature is not unpadded base64: %v", err)
	}
	delete(withoutSignature, "signatures")
	signed, err := matrixjson.Canonical(withoutSignature)
	if err != nil {
		t.Fatalf("canonicalising the signed form: %v", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(keyBytes), signed, signature) {
		t.Errorf("the signature does not check over the document as sent")
	}
}

// TestKeyQuery checks the key query endpoint: this server answers about itself and
// omits servers it does not know.
func TestKeyQuery(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	res := h.federationPost(t, "/_matrix/key/v2/query", map[string]any{
		"server_keys": map[string]any{
			"hs1": map[string]any{
				"ed25519:v1": map[string]any{},
			},
			"unknown.example": map[string]any{
				"ed25519:v1": map[string]any{},
			},
		},
	})
	h.mustStatus(t, res, http.StatusOK)
	keys, ok := res.decoded(t)["server_keys"].([]any)
	if !ok {
		t.Fatalf("server_keys missing: %s", res.body)
	}
	if len(keys) != 1 {
		t.Fatalf("server_keys has %d entries, want exactly one for hs1", len(keys))
	}
	entry, ok := keys[0].(map[string]any)
	if !ok {
		t.Fatalf("key entry is not an object: %v", keys[0])
	}
	if entry["server_name"] != "hs1" {
		t.Errorf("server_name = %v", entry["server_name"])
	}
	if _, ok := entry["signatures"].(map[string]any); !ok {
		t.Errorf("the returned document must be signed: %v", entry)
	}
}

// TestMatrixErrorSemantics covers the 404 / 405 rules over real HTTP, including
// the prefixes this server does not serve on the client listener.
func TestMatrixErrorSemantics(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	cases := []struct {
		method  string
		path    string
		status  int
		errcode string
		allow   string
	}{
		{http.MethodGet, "/_matrix/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodGet, "/_matrix/client/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodGet, "/_matrix/client/v1/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodGet, "/_matrix/client/v3/room/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodPut, "/_matrix/client/v3/login", http.StatusMethodNotAllowed, "M_UNRECOGNIZED", "GET, POST"},
		{http.MethodGet, "/_matrix/federation/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodGet, "/_matrix/federation/v1/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodPut, "/_matrix/federation/v1/version", http.StatusMethodNotAllowed, "M_UNRECOGNIZED", "GET"},
		{http.MethodGet, "/_matrix/key/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodGet, "/_matrix/key/v2/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodPut, "/_matrix/key/v2/query", http.StatusMethodNotAllowed, "M_UNRECOGNIZED", "POST"},
		{http.MethodGet, "/_matrix/media/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodGet, "/_matrix/media/v3/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{http.MethodPatch, "/_matrix/media/v3/upload", http.StatusMethodNotAllowed, "M_UNRECOGNIZED", "POST"},
		{http.MethodGet, "/_matrix/client/v3/versions", http.StatusNotFound, "M_UNRECOGNIZED", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := h.raw(t, tc.method, tc.path, nil, "")
			if res.status != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", res.status, tc.status, res.body)
			}
			if code := res.errcode(t); code != tc.errcode {
				t.Errorf("errcode = %s, want %s", code, tc.errcode)
			}
			if tc.allow != "" {
				if got := res.header.Get("Allow"); got != tc.allow {
					t.Errorf("Allow = %q, want %q", got, tc.allow)
				}
			}
		})
	}
}

// TestMediaEndpointsAreHonest checks that an unimplemented media repository says
// so, while the paths still exist for method negotiation.
func TestMediaEndpointsAreHonest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "uploader", "sUp3rs3kr1t", "")

	upload := h.post(t, "/_matrix/media/v3/upload", []byte("binary content"), token)
	if upload.status != http.StatusNotImplemented {
		t.Errorf("upload status = %d, want 501 (body %s)", upload.status, upload.body)
	}
	if _, ok := upload.decoded(t)["errcode"]; !ok {
		t.Errorf("upload error body is not a Matrix error: %s", upload.body)
	}

	// Upload requires a token: an anonymous attempt is refused before the
	// not-implemented answer.
	anonymous := h.raw(t, http.MethodPost, "/_matrix/media/v3/upload", []byte("x"), "")
	if anonymous.status != http.StatusUnauthorized {
		t.Errorf("anonymous upload status = %d, want 401", anonymous.status)
	}

	config := h.get("/_matrix/media/v3/config", "")
	h.mustStatus(t, config, http.StatusOK)
	if size, ok := config.decoded(t)["m.upload.size"].(float64); !ok || size <= 0 {
		t.Errorf("m.upload.size = %v", config.decoded(t)["m.upload.size"])
	}
}

// TestMalformedJSON checks that unparsable bodies yield a Matrix error rather than
// a panic or a bare HTTP error.
func TestMalformedJSON(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	cases := [][]byte{
		[]byte(`{`),
		[]byte(`[]`),
		[]byte(`{"username": }`),
		[]byte("not json at all"),
		[]byte(`{"username": "` + invalidUTF8 + `"}`),
	}
	for _, body := range cases {
		res := h.raw(t, http.MethodPost, "/_matrix/client/v3/register", body, "")
		if res.status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (body %s)", res.status, res.body)
		}
		var probe map[string]any
		if err := json.Unmarshal(res.body, &probe); err != nil {
			t.Errorf("error body is not JSON: %s", res.body)
			continue
		}
		if probe["errcode"] == nil || probe["error"] == nil {
			t.Errorf("error body lacks errcode/error: %s", res.body)
		}
	}
}

const invalidUTF8 = "\xff\xfe"

// TestBodySizeLimit checks the request size guard rails.
func TestBodySizeLimit(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, t.TempDir(), func(c *config.Config) {
		c.HTTP.MaxBodyBytes = 1024
	})

	oversized := make([]byte, 4096)
	for i := range oversized {
		oversized[i] = 'a'
	}
	res := h.raw(t, http.MethodPost, "/_matrix/client/v3/register", oversized, "")
	if res.status != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413 (body %s)", res.status, res.body)
	}
	var probe map[string]any
	if err := json.Unmarshal(res.body, &probe); err != nil {
		t.Fatalf("413 body is not a Matrix error: %s", res.body)
	}
	if probe["errcode"] != "M_TOO_LARGE" {
		t.Errorf("errcode = %v, want M_TOO_LARGE", probe["errcode"])
	}
}

// TestSecurityHeaders checks the response headers the server sets on every API
// response.
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	res := h.get("/_matrix/client/versions", "")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := res.header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := res.header.Get("Content-Security-Policy"); csp == "" {
		t.Errorf("Content-Security-Policy missing")
	}
	if id := res.header.Get("X-Lattice-Request-Id"); id == "" {
		t.Errorf("request id header missing")
	}
}

// TestHealthEndpoint checks the operational endpoint.
func TestHealthEndpoint(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	res := h.get("/healthz", "")
	h.mustStatus(t, res, http.StatusOK)
	if got := res.decoded(t)["status"]; got != "ok" {
		t.Errorf("status = %v", got)
	}
}
