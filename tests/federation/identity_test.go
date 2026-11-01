package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// Step 6 is the identity layer: which server am I, which key do I sign with, and
// how does a peer prove it is the server it claims to be. Everything below is what
// has to be true before a single event crosses the wire.

// TestFederationIsOffUnlessItIsAskedFor pins the cheapest promise of the whole
// step: a binary started without -federation does not serve the server-server API
// at all. The routes are absent rather than guarded, so a peer gets M_UNRECOGNIZED
// and the operator's exposure is unchanged from the single-server build.
func TestFederationIsOffUnlessItIsAskedFor(t *testing.T) {
	t.Parallel()
	s := plainServer(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/_matrix/federation/v1/version"},
		{http.MethodGet, "/_matrix/key/v2/server"},
		{http.MethodPost, "/_matrix/key/v2/query"},
		{http.MethodGet, "/_matrix/key/v2/query/" + serverOne},
		{http.MethodGet, "/_matrix/federation/v1/user/devices/@alice:plain.example"},
	}
	for _, tc := range cases {
		got := s.do(tc.method, tc.path, "", nil)
		if got.errCode() != "M_UNRECOGNIZED" {
			t.Errorf("%s %s without federation: errcode %q, want M_UNRECOGNIZED (body %s)",
				tc.method, tc.path, got.errCode(), got.Raw)
		}
		if got.Status != http.StatusNotFound {
			t.Errorf("%s %s without federation: status %d, want 404", tc.method, tc.path, got.Status)
		}
		// The message has to say why, because the person reading it is an operator
		// on the *other* side, trying to work out why their peer is refused.
		if !strings.Contains(got.stringField("error"), "server-server API") {
			t.Errorf("%s %s without federation: the error does not name the flag: %q",
				tc.method, tc.path, got.stringField("error"))
		}
	}

	// The same binary must still be a working homeserver for its clients. A flag
	// that breaks the ordinary path is not an off switch, it is a different build.
	alice := s.register("alice", "correct horse")
	roomID := s.createRoom(alice, map[string]any{"name": "still works"})
	if eventID := s.send(alice, roomID, "hello"); eventID == "" {
		t.Fatal("a server without federation could not send a message")
	}
}

// TestTheClientAPIIsUnchangedWithFederationOn keeps the flag honest. Turning on the
// server-server API must not change what a client sees: the same registration, the
// same room, the same sync, the same message history, and the same M_UNRECOGNIZED
// for a path that does not exist.
func TestTheClientAPIIsUnchangedWithFederationOn(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	s := c.one()

	alice := s.register("alice", "correct horse")
	bob := s.register("bob", "battery staple")
	roomID := s.createRoom(alice, map[string]any{"name": "ordinary", "preset": "public_chat"})
	eventID := s.send(alice, roomID, "nothing changed here")

	got := s.mustDo(http.StatusOK, http.MethodGet, "/_matrix/client/v3/sync?timeout=0", alice.Token, nil)
	if got.stringField("next_batch") == "" {
		t.Fatalf("sync returned no next_batch: %s", got.Raw)
	}
	rooms := got.objectField("rooms")
	joined, _ := rooms["join"].(map[string]any)
	if joined == nil {
		t.Fatalf("sync carries no joined rooms: %s", got.Raw)
	}
	if _, ok := joined[roomID]; !ok {
		t.Errorf("sync does not list the room %s that was just created", roomID)
	}

	// A second local user joins and reads the history: the path a federation route
	// would have to shadow to break this is the one clients use most.
	s.join(bob, roomID)
	history := s.mustDo(http.StatusOK,
		http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=10", bob.Token, nil)
	found := false
	for _, item := range history.arrayField("chunk") {
		event := asObject(t, item)
		if event["event_id"] == eventID {
			found = true
		}
	}
	if !found {
		t.Errorf("the message %s is missing from the room history: %s", eventID, history.Raw)
	}

	if unknown := s.do(http.MethodGet, "/_matrix/client/v3/whoosh", alice.Token, nil); unknown.errCode() != "M_UNRECOGNIZED" {
		t.Errorf("an unknown client path answered %q, want M_UNRECOGNIZED", unknown.errCode())
	}
}

// TestServerKeysArePublishedAndSelfSigned checks the document a peer fetches before
// it believes anything else this server says.
func TestServerKeysArePublishedAndSelfSigned(t *testing.T) {
	t.Parallel()
	c := newCluster(t)

	for _, name := range []string{serverOne, serverTwo} {
		got := c.doAs(name, http.MethodGet, "/_matrix/key/v2/server", "", nil)
		if got.Status != http.StatusOK {
			t.Fatalf("%s: GET /_matrix/key/v2/server: status %d, want 200 (body %s)", name, got.Status, got.Raw)
		}
		document := matrix.Object(got.Body)
		if document["server_name"] != name {
			t.Errorf("%s: the published document says server_name %v", name, document["server_name"])
		}
		if _, ok := document["old_verify_keys"]; !ok {
			t.Errorf("%s: the published document has no old_verify_keys; peers read a missing key as an unknown answer", name)
		}

		nowMS := time.Now().UnixMilli()
		set, err := federation.VerifyKeyDocument(document, name, nowMS)
		if err != nil {
			t.Fatalf("%s: the published document does not verify: %v", name, err)
		}
		if len(set.KeyIDs()) != 1 {
			t.Errorf("%s: the document publishes %d keys, want the one signing key it has", name, len(set.KeyIDs()))
		}
		keyID := set.KeyIDs()[0]
		if _, ok := set.Key(keyID); !ok {
			t.Fatalf("%s: the verified set does not carry %s", name, keyID)
		}
		if until := documentInt(document, "valid_until_ts"); until <= nowMS {
			t.Errorf("%s: the document already expired at %d, now %d", name, until, nowMS)
		} else if until > nowMS+federation.MaxKeyValidity.Milliseconds() {
			t.Errorf("%s: the document claims validity past the seven day cap: %d ms ahead", name, until-nowMS)
		}

		// The key id variant answers about a key this server publishes, and says
		// there is nothing to know about one it does not.
		byID := c.doAs(name, http.MethodGet, "/_matrix/key/v2/server/"+keyID, "", nil)
		if byID.Status != http.StatusOK {
			t.Errorf("%s: GET /_matrix/key/v2/server/%s: status %d, want 200", name, keyID, byID.Status)
		}
		unknown := c.doAs(name, http.MethodGet, "/_matrix/key/v2/server/ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "", nil)
		if unknown.errCode() != "M_NOT_FOUND" {
			t.Errorf("%s: an unknown key id answered %q, want M_NOT_FOUND", name, unknown.errCode())
		}
	}
}

// TestSigningKeySurvivesARestart pins the property that decides whether a room can
// remember who signed what: the key a server signs with is the one it generated
// first, and a restart must not mint a new one.
func TestSigningKeySurvivesARestart(t *testing.T) {
	t.Parallel()
	c := newCluster(t)

	first := c.one().mustDo(http.StatusOK, http.MethodGet, "/_matrix/key/v2/server", "", nil)
	firstKeyID := onlyVerifyKeyID(t, first.Body)

	// Reopen the same database through a fresh service, which is what a restart is.
	path := c.dbPathOf(serverOne)
	if err := c.one().db.Close(); err != nil {
		t.Fatalf("closing the first database: %v", err)
	}
	db, err := persist.NewDatabase(path)
	if err != nil {
		t.Fatalf("reopening %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	svc, err := service.New(db, serverOne, authTokens(serverOne), service.WithFederation(true))
	if err != nil {
		t.Fatalf("building a service on the reopened database: %v", err)
	}
	document, err := svc.ServerKeysDocument(t.Context())
	if err != nil {
		t.Fatalf("asking the reopened service for its keys: %v", err)
	}
	secondKeyID := onlyVerifyKeyID(t, document)
	if firstKeyID != secondKeyID {
		t.Errorf("the signing key changed across a restart: %s then %s; every event signed before the restart becomes unverifiable",
			firstKeyID, secondKeyID)
	}
}

// TestKeyQueryNotarisesAPeersKeys covers the notary endpoints, which are how a
// server that cannot verify a peer directly still gets an answer it can rely on.
func TestKeyQueryNotarisesAPeersKeys(t *testing.T) {
	t.Parallel()
	// down.example is part of the cluster so its name is pinned like the others, and
	// its listener is closed before anything asks for it: the failure then arrives
	// as a refused connection rather than as a DNS or connect timeout.
	const downServer = "down.example"
	c := newClusterNamed(t, serverOne, serverTwo, downServer)

	got := c.doAs(serverOne, http.MethodPost, "/_matrix/key/v2/query", "", matrix.Object{
		"server_keys": matrix.Object{serverTwo: matrix.Object{}},
	})
	if got.Status != http.StatusOK {
		t.Fatalf("POST /_matrix/key/v2/query: status %d, want 200 (body %s)", got.Status, got.Raw)
	}
	documents := got.arrayField("server_keys")
	if len(documents) != 1 {
		t.Fatalf("the notary returned %d documents, want 1 (body %s)", len(documents), got.Raw)
	}
	notarised := asObject(t, documents[0])
	if notarised["server_name"] != serverTwo {
		t.Errorf("the notarised document names %v, want %s", notarised["server_name"], serverTwo)
	}
	if failures := got.objectField("failures"); len(failures) != 0 {
		t.Errorf("the notary reported failures for a peer that answered: %v", failures)
	}

	// Both signatures must hold: the peer's own, and this server's. A notary that
	// signs without the peer's signature, or a peer document that was never
	// verified, would let a man in the middle introduce a key.
	nowMS := time.Now().UnixMilli()
	set, err := federation.VerifyKeyDocument(notarised, serverTwo, nowMS)
	if err != nil {
		t.Fatalf("the notarised document no longer verifies as %s's: %v", serverTwo, err)
	}
	if err := matrix.VerifyEd25519Signature(notarised, serverTwo, set.KeyIDs()[0], mustKey(t, set, set.KeyIDs()[0])); err != nil {
		t.Errorf("the peer's own signature does not hold on the notarised document: %v", err)
	}

	own := c.one().mustDo(http.StatusOK, http.MethodGet, "/_matrix/key/v2/server", "", nil)
	ownSet, err := federation.VerifyKeyDocument(matrix.Object(own.Body), serverOne, nowMS)
	if err != nil {
		t.Fatalf("this server's own document does not verify: %v", err)
	}
	ownKeyID := ownSet.KeyIDs()[0]
	if err := matrix.VerifyEd25519Signature(notarised, serverOne, ownKeyID, mustKey(t, ownSet, ownKeyID)); err != nil {
		t.Errorf("the notary signature is missing or wrong: %v", err)
	}

	// A second signature must not replace the first, and a document must not gain
	// keys it did not have.
	if signatures, ok := notarised["signatures"].(map[string]any); ok {
		if _, ok := signatures[serverOne].(map[string]any); !ok {
			t.Errorf("the notarised document carries no signature entry for %s", serverOne)
		}
		if _, ok := signatures[serverTwo].(map[string]any); !ok {
			t.Errorf("the notarised document lost the signature entry of %s", serverTwo)
		}
	}

	// The single-server spelling of the same service.
	one := c.doAs(serverOne, http.MethodGet, "/_matrix/key/v2/query/"+serverTwo, "", nil)
	if one.Status != http.StatusOK || len(one.arrayField("server_keys")) != 1 {
		t.Errorf("GET /_matrix/key/v2/query/%s: status %d, %d documents; body %s",
			serverTwo, one.Status, len(one.arrayField("server_keys")), one.Raw)
	}

	// A name that cannot be reached is a failure entry, not a failed request: the
	// caller asked about a server, and the honest answer is "I could not reach it".
	// The array stays empty rather than null, because every client reads it as a
	// list.
	c.disconnect(downServer)
	unreachable := c.doAs(serverOne, http.MethodPost, "/_matrix/key/v2/query", "", matrix.Object{
		"server_keys": matrix.Object{downServer: matrix.Object{}},
	})
	if unreachable.Status != http.StatusOK {
		t.Errorf("a key query for an unreachable server answered %d, want 200 with a failure entry", unreachable.Status)
	}
	if documents := unreachable.Body["server_keys"]; len(asArray(t, documents)) != 0 {
		t.Errorf("a key query for an unreachable server returned documents: %s", unreachable.Raw)
	}
	if failures := unreachable.objectField("failures"); len(stringField(failures, downServer)) == 0 {
		t.Errorf("a key query for an unreachable server reported no failure: %s", unreachable.Raw)
	}

	// A peer that goes quiet does not invalidate the keys already inside their
	// validity window. Signatures of older events have to stay checkable while a
	// peer is briefly down, and re-fetching on every request is how a homeserver
	// turns its peer's outage into its own.
	c.disconnect(serverTwo)
	cached := c.doAs(serverOne, http.MethodPost, "/_matrix/key/v2/query", "", matrix.Object{
		"server_keys": matrix.Object{serverTwo: matrix.Object{}},
	})
	if len(cached.arrayField("server_keys")) != 1 {
		t.Errorf("a key query while the peer is down returned %d documents, want the one still-valid cached document: %s",
			len(cached.arrayField("server_keys")), cached.Raw)
	}
	if failures := cached.objectField("failures"); len(failures) != 0 {
		t.Errorf("a key query answered from the cache reported failures: %v", failures)
	}
}

// TestDeviceKeysCrossTheWireUnchanged is the reason federation and encryption work
// together at all: a peer asks for a user's devices, and the key objects it gets
// back have to be the bytes the user's devices signed.
func TestDeviceKeysCrossTheWireUnchanged(t *testing.T) {
	t.Parallel()
	c := newCluster(t)

	alice := c.one().register("alice", "correct horse")
	uploaded := signedDeviceKeys(t, alice)
	c.one().uploadDeviceKeys(alice, uploaded)

	// The peer asks over its own signed transport, which is how a real homeserver
	// asks: no client token is involved, only the server's signature.
	document, err := c.two().peer().Get(t.Context(), serverOne,
		"/_matrix/federation/v1/user/devices/"+alice.UserID, nil)
	if err != nil {
		t.Fatalf("%s asking %s for %s's devices: %v", serverTwo, serverOne, alice.UserID, err)
	}
	if document["user_id"] != alice.UserID {
		t.Errorf("the device list names %v, want %s", document["user_id"], alice.UserID)
	}
	devices, ok := document["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("the device list carries %v, want one device", document["devices"])
	}
	entry := asObject(t, devices[0])
	if entry["device_id"] != alice.DeviceID {
		t.Errorf("the device list names device %v, want %s", entry["device_id"], alice.DeviceID)
	}
	gotKeys := asObject(t, entry["keys"])
	if canonicalWant, err := matrix.CanonicalJSON(uploaded); err != nil {
		t.Fatalf("encoding the uploaded keys: %v", err)
	} else if canonicalGot, err := matrix.CanonicalJSON(gotKeys); err != nil {
		t.Fatalf("encoding the served keys: %v", err)
	} else if string(canonicalWant) != string(canonicalGot) {
		t.Errorf("the served device keys differ from what the device uploaded:\n want %s\n  got %s", canonicalWant, canonicalGot)
	}
	if documentInt(document, "stream_id") <= 0 {
		t.Errorf("the device list has no stream_id: %v", document["stream_id"])
	}
	// A user with no cross-signing keys gets no master_key at all. An empty object
	// would tell the peer something else: that there is a key which says nothing.
	if _, found := document["master_key"]; found {
		t.Errorf("the device list of a user with no cross-signing keys carries master_key: %v", document["master_key"])
	}

	// A user that belongs to the other server is not this server's business. The
	// peer is told so with an error it can act on, and the transport keeps the
	// errcode rather than flattening the answer into a string.
	_, err = c.one().peer().Get(t.Context(), serverTwo, "/_matrix/federation/v1/user/devices/@alice:"+serverOne, nil)
	var merr *matrix.Error
	if !errors.As(err, &merr) || merr.Code != matrix.ErrCodeNotFound {
		t.Errorf("asking %s for a user of %s: got %v, want M_NOT_FOUND", serverTwo, serverOne, err)
	}
}

// TestRequestsAreRefusedUnlessTheyAreSigned covers the shapes of a request that a
// server must not act on. Each one carries a plausible-looking header; the
// difference between accepting and refusing them is the whole security model of
// federation.
func TestRequestsAreRefusedUnlessTheyAreSigned(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	// The path is real, so a refusal can only be about the signature. A request for
	// an unknown user still reaches the handler when it is signed, which would turn
	// a 401 into a 404 and hide the point of the case.
	devicePath := "/_matrix/federation/v1/user/devices/@alice:" + serverOne

	signed := func(t *testing.T, key federation.KeyPair, origin, destination, uri string, content matrix.Object) string {
		t.Helper()
		fields, err := federation.SignAuthorization(key, http.MethodGet, uri, origin, destination, content)
		if err != nil {
			t.Fatalf("signing a request: %v", err)
		}
		return fields.AuthorizationHeader()
	}

	ours, err := c.one().svc.SigningKey(t.Context())
	if err != nil {
		t.Fatalf("this server's signing key: %v", err)
	}
	stranger, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("making an unrelated keypair: %v", err)
	}

	cases := []struct {
		name   string
		uri    string
		header string
	}{
		{
			name:   "no authorization header at all",
			uri:    devicePath,
			header: "",
		},
		{
			name:   "an authorization header that is not X-Matrix",
			uri:    devicePath,
			header: "Bearer sometoken",
		},
		{
			name:   "addressed to somebody else",
			uri:    devicePath,
			header: signed(t, ours, serverOne, "elsewhere.example", devicePath, matrix.Object{}),
		},
		{
			name:   "signed with a key the origin does not publish",
			uri:    devicePath,
			header: signed(t, stranger, serverOne, serverOne, devicePath, matrix.Object{}),
		},
		{
			name:   "signed for a different path",
			uri:    devicePath,
			header: signed(t, ours, serverOne, serverOne, "/_matrix/federation/v1/user/devices/@someone:else", matrix.Object{}),
		},
		{
			name:   "signed with a different method",
			uri:    devicePath,
			header: signedWithMethod(t, ours, http.MethodDelete, serverOne, serverOne, devicePath, matrix.Object{}),
		},
		{
			name:   "an origin that does not exist",
			uri:    devicePath,
			header: signed(t, ours, "nowhere.example", serverOne, devicePath, matrix.Object{}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rawAuth(t, c.one().url(), http.MethodGet, tc.uri, tc.header)
			if got.errCode() != "M_UNAUTHORIZED" {
				t.Errorf("refused with %q, want M_UNAUTHORIZED (status %d, body %s)",
					got.errCode(), got.Status, got.Raw)
			}
			if got.Status != http.StatusUnauthorized {
				t.Errorf("refused with status %d, want 401", got.Status)
			}
		})
	}

	// A GET whose query string disagrees with the signed origin is the same class
	// of problem: the signature is about one request, and this is another one.
	header := signed(t, ours, serverTwo, serverOne, devicePath+"?origin="+serverTwo, matrix.Object{})
	got := rawAuth(t, c.one().url(), http.MethodGet, devicePath+"?origin="+serverOne, header)
	if got.errCode() != "M_UNAUTHORIZED" {
		t.Errorf("a mismatched query origin was answered %q, want M_UNAUTHORIZED", got.errCode())
	}
}

// TestSignedGETReachesAPeer is the positive case of the same machinery: the same
// path, signed correctly, is answered.
func TestSignedGETReachesAPeer(t *testing.T) {
	t.Parallel()
	c := newCluster(t)

	alice := c.one().register("alice", "correct horse")
	document, err := c.two().peer().Get(t.Context(), serverOne,
		"/_matrix/federation/v1/user/devices/"+alice.UserID, nil)
	if err != nil {
		t.Fatalf("a correctly signed request was refused: %v", err)
	}
	if document["user_id"] != alice.UserID {
		t.Errorf("the answer names %v, want %s", document["user_id"], alice.UserID)
	}
	devices, ok := document["devices"].([]any)
	if !ok {
		t.Fatalf("devices is not an array: %v", document["devices"])
	}
	if len(devices) != 0 {
		t.Errorf("a user who uploaded no keys reported %d devices", len(devices))
	}
}

// TestFederationVersionIsPublic keeps the operator's diagnostic reachable: knowing
// what a server runs must not require having already authenticated to it.
func TestFederationVersionIsPublic(t *testing.T) {
	t.Parallel()
	c := newCluster(t)

	for _, name := range []string{serverOne, serverTwo} {
		got := c.doAs(name, http.MethodGet, "/_matrix/federation/v1/version", "", nil)
		if got.Status != http.StatusOK {
			t.Fatalf("GET /_matrix/federation/v1/version on %s: status %d, want 200", name, got.Status)
		}
		server := got.objectField("server")
		if server["name"] != "Lattice" {
			t.Errorf("the version document claims to be %v, want Lattice", server["name"])
		}
		if version, ok := server["version"].(string); !ok || version == "" {
			t.Errorf("the version document carries no version: %s", got.Raw)
		}
	}
}

// helpers

// plainServer starts a homeserver with the federation flag off.
func plainServer(t *testing.T) *server {
	t.Helper()
	return startPlain(t, "plain.example")
}

// signedDeviceKeys builds the identity keys of a device and signs them the way a
// real device does: canonical JSON without the signatures, with the device's own
// ed25519 key.
func signedDeviceKeys(t *testing.T, sess *session) matrix.Object {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a device key: %v", err)
	}
	object := matrix.Object{
		"user_id":    sess.UserID,
		"device_id":  sess.DeviceID,
		"algorithms": []any{"m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"},
		"keys": matrix.Object{
			// The curve25519 half is random bytes rather than a real key: no
			// Olm session is opened in this test, and a homeserver never looks
			// inside the key material it stores.
			"curve25519:" + sess.DeviceID: randomBase64(t),
			"ed25519:" + sess.DeviceID:    base64.RawStdEncoding.EncodeToString(public),
		},
	}
	signature, err := matrix.SignEd25519(private, object)
	if err != nil {
		t.Fatalf("signing the device keys: %v", err)
	}
	object["signatures"] = matrix.Object{
		sess.UserID: matrix.Object{
			"ed25519:" + sess.DeviceID: base64.RawStdEncoding.EncodeToString(signature),
		},
	}
	return object
}

// randomBase64 is a plausible looking key for a field no server reads.
func randomBase64(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generating random bytes: %v", err)
	}
	return base64.RawStdEncoding.EncodeToString(buf)
}

// rawAuth sends a request with an Authorization header a test built by hand, which
// is the only way to send a signature that is wrong in a specific way.
func rawAuth(t *testing.T, baseURL, method, target, authorization string) response {
	t.Helper()
	request, err := http.NewRequest(method, baseURL+target, nil)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, target, err)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return roundTrip(t, request)
}

// signedWithMethod signs a request for a method other than the one it is sent with.
func signedWithMethod(t *testing.T, key federation.KeyPair, method, origin, destination, uri string, content matrix.Object) string {
	t.Helper()
	fields, err := federation.SignAuthorization(key, method, uri, origin, destination, content)
	if err != nil {
		t.Fatalf("signing a request: %v", err)
	}
	return fields.AuthorizationHeader()
}

// onlyVerifyKeyID returns the single key id a document publishes.
func onlyVerifyKeyID(t *testing.T, document map[string]any) string {
	t.Helper()
	keys, ok := document["verify_keys"].(map[string]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("the document publishes %v, want exactly one key", document["verify_keys"])
	}
	for keyID := range keys {
		return keyID
	}
	return ""
}

// mustKey reads a public key out of a verified set.
func mustKey(t *testing.T, set *federation.KeySet, keyID string) ed25519.PublicKey {
	t.Helper()
	publicKey, ok := set.Key(keyID)
	if !ok {
		t.Fatalf("the verified set does not carry %s", keyID)
	}
	return publicKey
}

// asArray narrows a decoded JSON value, failing the test when it is not an array.
func asArray(t *testing.T, value any) []any {
	t.Helper()
	array, ok := value.([]any)
	if !ok {
		t.Fatalf("expected a JSON array, got %T (%v)", value, value)
	}
	return array
}

// asObject narrows a decoded JSON value, failing the test when it is not an object.
func asObject(t *testing.T, value any) matrix.Object {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON object, got %T (%v)", value, value)
	}
	return object
}

// stringField reads a string field out of a decoded object.
func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

// documentInt reads an integer field of a decoded document. Test bodies are decoded
// with encoding/json, so whole numbers arrive as float64.
func documentInt(document map[string]any, field string) int64 {
	switch value := document[field].(type) {
	case float64:
		return int64(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0
		}
		return parsed
	case int64:
		return value
	default:
		return 0
	}
}
