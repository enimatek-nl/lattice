package compat

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// Cross-signing keys are the root of an end-to-end encrypted identity, so they
// are the one piece of key material Lattice checks itself instead of storing
// blind: a server that accepted a key nobody signed, or a key signed by the wrong
// one, would hand out a trust that every conversation relies on.

// signingKeyObject builds one cross-signing key object the way a client does: the
// key material under its id, and a signature by issuerKeyID made over the
// canonical form of the object.
func signingKeyObject(t *testing.T, userID string, public ed25519.PublicKey, usage string,
	issuerPrivate ed25519.PrivateKey, issuerKeyID string) matrix.Object {
	t.Helper()
	keyID := matrix.Ed25519KeyID(public)
	object := matrix.Object{
		"user_id": userID,
		"usage":   []string{usage},
		"keys":    matrix.Object{keyID: base64.RawStdEncoding.EncodeToString(public)},
	}
	signature, err := matrix.SignEd25519(issuerPrivate, object)
	if err != nil {
		t.Fatalf("signing the %s key: %v", usage, err)
	}
	object["signatures"] = matrix.Object{
		userID: matrix.Object{issuerKeyID: base64.RawStdEncoding.EncodeToString(signature)},
	}
	return object
}

// crossSigningFromQuery gathers the cross-signing keys of a POST /keys/query
// response by key id. They arrive in the three maps the specification names for
// them - master_keys, self_signing_keys, user_signing_keys - and not among the
// device keys, which is a shape a client parses as a device.
func crossSigningFromQuery(resp Response) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, section := range []string{"master_keys", "self_signing_keys", "user_signing_keys"} {
		byUser, _ := resp.Body[section].(map[string]any)
		for _, object := range byUser {
			entry, _ := object.(map[string]any)
			if entry == nil {
				continue
			}
			keys, _ := entry["keys"].(map[string]any)
			for keyID := range keys {
				out[keyID] = entry
			}
		}
	}
	return out
}

// keyPair is one generated ed25519 key with the id it is published under.
type keyPair struct {
	public  ed25519.PublicKey
	private ed25519.PrivateKey
	id      string
}

// newKeyPair generates a key and its published id.
func newKeyPair(t *testing.T) keyPair {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	return keyPair{public: public, private: private, id: matrix.Ed25519KeyID(public)}
}

// passwordAuth is the User-Interactive Auth dict a client sends for a stage that
// proves it controls the account. The session id is optional: a client may start
// a fresh exchange instead of continuing one.
func passwordAuth(userID, password string, session ...string) matrix.Object {
	auth := matrix.Object{
		"type":       "m.login.password",
		"password":   password,
		"identifier": matrix.Object{"type": "m.id.user", "user": userID},
	}
	if len(session) > 0 && session[0] != "" {
		auth["session"] = session[0]
	}
	return auth
}

// TestCrossSigning covers POST /keys/device_signing/upload: the first set of
// keys of a new account arriving without an interactive auth exchange, that
// exchange in front of changing an identity that exists, the verification of the
// keys, the replacement of a master key, and the keys showing up in POST
// /keys/query.
func TestCrossSigning(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	master := newKeyPair(t)
	selfSigning := newKeyPair(t)
	userSigning := newKeyPair(t)

	upload := func(body matrix.Object) Response {
		return server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token, body)
	}

	// A second device of the same user is watching. The upload has to reach it as
	// a device list change, which is what makes that device fetch the new keys.
	watcher := server.Login("alice", "wonderland")
	watched, _ := server.Sync(watcher, "?timeout=0")
	watcher.NextBatch = watched.NextBatch

	// The first set of keys of a new account is accepted without an interactive
	// auth exchange: the specification exempts it since v1.11, and Element relies
	// on that exemption when it sets up encryption while the account is being
	// created. Challenging that upload is what leaves the web client unable to set
	// up keys. The master key signs itself, the other two are signed by it.
	accepted := upload(matrix.Object{
		"master_key":       signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id),
		"self_signing_key": signingKeyObject(t, alice.UserID, selfSigning.public, "self-signing", master.private, master.id),
		"user_signing_key": signingKeyObject(t, alice.UserID, userSigning.public, "user-signing", master.private, master.id),
	})
	if accepted.Status != http.StatusOK {
		t.Fatalf("uploading the first cross-signing keys without auth: got %d %s, want 200",
			accepted.Status, accepted.Raw)
	}
	if len(accepted.Body) != 0 {
		t.Errorf("the upload returned a body: %s", accepted.Raw)
	}
	woken, resp := server.SyncSince(watcher, 0)
	if woken.DeviceLists == nil {
		t.Fatalf("the sync after the upload carries no device lists: %s", resp.Raw)
	}
	var listed bool
	for _, changed := range woken.DeviceLists.Changed {
		if changed == alice.UserID {
			listed = true
		}
	}
	if !listed {
		t.Errorf("the upload did not report %s as changed: %s", alice.UserID, resp.Raw)
	}

	// POST /keys/query reports the cross-signing keys in the same map as the
	// device keys, named by their key id: that is where clients read them from.
	queried := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
		map[string]any{"device_keys": map[string]any{alice.UserID: []string{}}}, http.StatusOK)
	byEntity, _ := queried.Body["device_keys"].(map[string]any)
	mine, _ := byEntity[alice.UserID].(map[string]any)
	cross := crossSigningFromQuery(queried)
	for _, keyID := range []string{master.id, selfSigning.id, userSigning.id} {
		entry, ok := cross[keyID]
		if !ok {
			t.Errorf("POST /keys/query does not report the key %s: %s", keyID, queried.Raw)
			continue
		}
		if entry["user_id"] != alice.UserID {
			t.Errorf("the key %s belongs to %v", keyID, entry["user_id"])
		}
	}
	// The cross-signing keys stay out of the device map: an entry there is read as
	// a device, and a cross-signing object has no device_id to parse.
	for keyID := range cross {
		if _, ok := mine[keyID]; ok {
			t.Errorf("the cross-signing key %s also appears as a device: %s", keyID, queried.Raw)
		}
	}

	// Re-uploading the same keys asks for nothing either: the request holds no new
	// key, which is what makes a retry after a lost response work instead of
	// landing the client on a challenge it has no reason to answer.
	again := upload(matrix.Object{
		"master_key":       signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id),
		"self_signing_key": signingKeyObject(t, alice.UserID, selfSigning.public, "self-signing", master.private, master.id),
		"user_signing_key": signingKeyObject(t, alice.UserID, userSigning.public, "user-signing", master.private, master.id),
	})
	if again.Status != http.StatusOK {
		t.Errorf("re-uploading the same cross-signing keys without auth: got %d %s", again.Status, again.Raw)
	}

	// An identity that exists is protected: without credentials a request that
	// would change it only describes how to authenticate, which is how a client
	// knows to ask for the password.
	replacement := newKeyPair(t)
	newMaster := signingKeyObject(t, alice.UserID, replacement.public, "master", replacement.private, replacement.id)
	challenge := upload(matrix.Object{"master_key": newMaster})
	if challenge.Status != http.StatusUnauthorized {
		t.Fatalf("replacing the cross-signing keys without auth: got %d %s, want 401",
			challenge.Status, challenge.Raw)
	}
	flows, _ := challenge.Body["flows"].([]any)
	if len(flows) == 0 {
		t.Fatalf("the challenge carries no flows: %s", challenge.Raw)
	}
	session, _ := challenge.Body["session"].(string)
	if session == "" {
		t.Fatalf("the challenge carries no session: %s", challenge.Raw)
	}

	// The wrong password is not fatal: the client gets the flow list back.
	wrong := upload(matrix.Object{
		"auth":       passwordAuth(alice.UserID, "wrong password", session),
		"master_key": newMaster,
	})
	if wrong.Status != http.StatusUnauthorized {
		t.Errorf("cross-signing upload with a wrong password: got %d %s", wrong.Status, wrong.Raw)
	}

	// Answering the challenge replaces the set. This is the path Element takes when
	// the user says they cannot verify their existing identity, and answering the
	// interactive auth challenge is the authorisation for it: no client can send a
	// separate reset flag, so a refusal here is a dead end for the user.
	replaced := upload(matrix.Object{
		"auth":       passwordAuth(alice.UserID, "wonderland", session),
		"master_key": newMaster,
	})
	if replaced.Status != http.StatusOK {
		t.Fatalf("replacing the cross-signing keys: got %d %s", replaced.Status, replaced.Raw)
	}
	afterReset := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
		map[string]any{"device_keys": map[string]any{alice.UserID: []string{}}}, http.StatusOK)
	cross = crossSigningFromQuery(afterReset)
	if _, ok := cross[replacement.id]; !ok {
		t.Errorf("the replacement did not store the new master key: %s", afterReset.Raw)
	}
	if _, ok := cross[master.id]; ok {
		t.Errorf("the replacement left the old master key behind: %s", afterReset.Raw)
	}
	// The keys the old master signed are gone with it: a client would otherwise
	// keep offering self-signing and user-signing keys no master key vouches for.
	for _, keyID := range []string{selfSigning.id, userSigning.id} {
		if _, ok := cross[keyID]; ok {
			t.Errorf("the replacement left the key %s of the old set behind: %s", keyID, afterReset.Raw)
		}
	}

	// A signature the replaced keys made on a device is dropped as well, because a
	// client that cannot resolve the signer shows that device as unverified for
	// good. The device is signed by the old self-signing key first, then the set is
	// replaced again.
	device := newKeyPair(t)
	deviceCurve := newKeyPair(t)
	deviceKey := matrix.Object{
		"user_id":    alice.UserID,
		"device_id":  alice.DeviceID,
		"algorithms": []string{"m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"},
		"keys": matrix.Object{
			"curve25519:" + alice.DeviceID: base64.RawStdEncoding.EncodeToString(deviceCurve.public),
			"ed25519:" + alice.DeviceID:    base64.RawStdEncoding.EncodeToString(device.public),
		},
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/upload", alice.Token,
		matrix.Object{"device_id": alice.DeviceID, "device_key": deviceKey}, http.StatusOK)
	signedDevice := matrix.Object{
		"user_id":    alice.UserID,
		"device_id":  alice.DeviceID,
		"algorithms": deviceKey["algorithms"],
		"keys":       deviceKey["keys"],
	}

	// The self-signing key of the set that is current right now signs the device.
	current := newKeyPair(t)
	if resp := upload(matrix.Object{
		"auth":             passwordAuth(alice.UserID, "wonderland"),
		"master_key":       signingKeyObject(t, alice.UserID, replacement.public, "master", replacement.private, replacement.id),
		"self_signing_key": signingKeyObject(t, alice.UserID, current.public, "self_signing", replacement.private, replacement.id),
	}); resp.Status != http.StatusOK {
		t.Fatalf("adding a self-signing key to the replaced set: got %d %s", resp.Status, resp.Raw)
	}
	signature, err := matrix.SignEd25519(current.private, signedDevice)
	if err != nil {
		t.Fatalf("signing the device with the current self-signing key: %v", err)
	}
	signedDevice["signatures"] = matrix.Object{
		alice.UserID: matrix.Object{current.id: base64.RawStdEncoding.EncodeToString(signature)},
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/signatures/upload", alice.Token,
		matrix.Object{"signed_users": map[string]any{alice.UserID: map[string]any{alice.DeviceID: signedDevice}}},
		http.StatusOK)

	// A key that was already replaced cannot sign anything: Lattice has no such key
	// for this user any more, and storing the entry would leave a signature no
	// client can resolve.
	staleSignature, err := matrix.SignEd25519(selfSigning.private, signedDevice)
	if err != nil {
		t.Fatalf("signing the device with the replaced key: %v", err)
	}
	stale := matrix.Object{
		"user_id":    alice.UserID,
		"device_id":  alice.DeviceID,
		"algorithms": deviceKey["algorithms"],
		"keys":       deviceKey["keys"],
		"signatures": matrix.Object{
			alice.UserID: matrix.Object{selfSigning.id: base64.RawStdEncoding.EncodeToString(staleSignature)},
		},
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/signatures/upload", alice.Token,
		matrix.Object{"signed_users": map[string]any{alice.UserID: map[string]any{alice.DeviceID: stale}}}); resp.Status != http.StatusBadRequest {
		t.Errorf("signing with a replaced key: got %d %s, want 400", resp.Status, resp.Raw)
	}

	second := newKeyPair(t)
	if resp := upload(matrix.Object{
		"auth":       passwordAuth(alice.UserID, "wonderland"),
		"master_key": signingKeyObject(t, alice.UserID, second.public, "master", second.private, second.id),
	}); resp.Status != http.StatusOK {
		t.Fatalf("replacing the cross-signing keys a second time: got %d %s", resp.Status, resp.Raw)
	}
	cleared := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
		map[string]any{"device_keys": map[string]any{alice.UserID: []string{alice.DeviceID}}}, http.StatusOK)
	byEntity, _ = cleared.Body["device_keys"].(map[string]any)
	mine, _ = byEntity[alice.UserID].(map[string]any)
	entry, _ := mine[alice.DeviceID].(map[string]any)
	if entry == nil {
		t.Fatalf("the device disappeared from the query response: %s", cleared.Raw)
	}
	signatures, _ := entry["signatures"].(map[string]any)
	if byUser, ok := signatures[alice.UserID].(map[string]any); ok {
		if _, stale := byUser[current.id]; stale {
			t.Errorf("a replaced key still signs the device: %v", byUser)
		}
	}
	if _, ok := crossSigningFromQuery(cleared)[second.id]; !ok {
		t.Errorf("the second replacement did not store its master key: %s", cleared.Raw)
	}
}

// TestCrossSigningPartialUpload checks an upload that carries only the
// self-signing key: the stored master key signs it, and has to survive it. The
// upload writes every key it holds back to storage, so a rebuilt master key
// without its object would blank the identity of the user.
func TestCrossSigningPartialUpload(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	upload := func(body matrix.Object) Response {
		return server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token, body)
	}

	master := newKeyPair(t)
	if resp := upload(matrix.Object{
		"master_key": signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id),
	}); resp.Status != http.StatusOK {
		t.Fatalf("storing the first master key: got %d %s", resp.Status, resp.Raw)
	}

	// A new self-signing key next to an existing master key is a new key, so the
	// exchange still stands in front of it.
	selfSigning := newKeyPair(t)
	selfObject := signingKeyObject(t, alice.UserID, selfSigning.public, "self-signing", master.private, master.id)
	challenge := upload(matrix.Object{"self_signing_key": selfObject})
	if challenge.Status != http.StatusUnauthorized {
		t.Fatalf("a self-signing key without auth: got %d %s, want 401", challenge.Status, challenge.Raw)
	}
	if accepted := upload(matrix.Object{
		"auth":             passwordAuth(alice.UserID, "wonderland", challenge.String("session")),
		"self_signing_key": selfObject,
	}); accepted.Status != http.StatusOK {
		t.Fatalf("a self-signing key answered by the password stage: got %d %s", accepted.Status, accepted.Raw)
	}

	// Both keys are still reported, each with the object that was uploaded: the
	// master key has to keep the material a client reads to know who to trust.
	queried := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
		matrix.Object{"device_keys": map[string][]string{alice.UserID: nil}}, http.StatusOK)
	cross := crossSigningFromQuery(queried)
	for _, keyID := range []string{master.id, selfSigning.id} {
		entry, ok := cross[keyID]
		if !ok {
			t.Errorf("POST /keys/query does not report the key %s: %s", keyID, queried.Raw)
			continue
		}
		if keys, _ := entry["keys"].(map[string]any); len(keys) == 0 {
			t.Errorf("the stored key %s lost its key material: %v", keyID, entry)
		}
	}

	// Re-uploading that same self-signing key alone needs nothing again.
	if again := upload(matrix.Object{"self_signing_key": selfObject}); again.Status != http.StatusOK {
		t.Errorf("re-uploading the self-signing key without auth: got %d %s", again.Status, again.Raw)
	}
}

// TestCrossSigningRejectsBadKeys checks the ways a key object can be wrong, and
// that every one of them is refused rather than stored.
func TestCrossSigningRejectsBadKeys(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	master := newKeyPair(t)
	selfSigning := newKeyPair(t)
	other := newKeyPair(t)

	t.Run("a master key that does not sign itself", func(t *testing.T) {
		object := signingKeyObject(t, alice.UserID, master.public, "master", selfSigning.private, selfSigning.id)
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
			matrix.Object{"auth": passwordAuth(alice.UserID, "wonderland"), "master_key": object})
		if resp.Status != http.StatusBadRequest || resp.ErrCode() != "M_INVALID_PARAM" {
			t.Errorf("got %d %s, want 400 M_INVALID_PARAM", resp.Status, resp.Raw)
		}
	})
	t.Run("a self-signing key the master key did not sign", func(t *testing.T) {
		good := signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id)
		bad := signingKeyObject(t, alice.UserID, selfSigning.public, "self-signing", other.private, other.id)
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
			matrix.Object{"auth": passwordAuth(alice.UserID, "wonderland"), "master_key": good, "self_signing_key": bad})
		if resp.Status != http.StatusBadRequest || resp.ErrCode() != "M_INVALID_PARAM" {
			t.Errorf("got %d %s, want 400 M_INVALID_PARAM", resp.Status, resp.Raw)
		}
	})
	t.Run("a key id that does not match its key material", func(t *testing.T) {
		object := matrix.Object{
			"user_id": alice.UserID,
			"usage":   []string{"master"},
			"keys":    matrix.Object{master.id: base64.RawStdEncoding.EncodeToString(other.public)},
		}
		signature, err := matrix.SignEd25519(master.private, object)
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
		object["signatures"] = matrix.Object{
			alice.UserID: matrix.Object{master.id: base64.RawStdEncoding.EncodeToString(signature)},
		}
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
			matrix.Object{"auth": passwordAuth(alice.UserID, "wonderland"), "master_key": object})
		if resp.Status != http.StatusBadRequest {
			t.Errorf("got %d %s, want 400", resp.Status, resp.Raw)
		}
	})
	t.Run("a key for another account", func(t *testing.T) {
		object := signingKeyObject(t, bob.UserID, master.public, "master", master.private, master.id)
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
			matrix.Object{"auth": passwordAuth(alice.UserID, "wonderland"), "master_key": object})
		if resp.Status != http.StatusBadRequest {
			t.Errorf("got %d %s, want 400", resp.Status, resp.Raw)
		}
	})
	t.Run("a key that names the wrong usage", func(t *testing.T) {
		object := signingKeyObject(t, alice.UserID, master.public, "user-signing", master.private, master.id)
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
			matrix.Object{"auth": passwordAuth(alice.UserID, "wonderland"), "master_key": object})
		if resp.Status != http.StatusBadRequest {
			t.Errorf("got %d %s, want 400", resp.Status, resp.Raw)
		}
	})
	t.Run("a stage this endpoint does not offer", func(t *testing.T) {
		// The exchange only stands in front of a request that would change an
		// existing identity, so the user first gets a key set the way a new account
		// does it, and then meets the challenge when the replacement names a stage
		// this endpoint has never offered.
		carol := server.Register("carol", "dustbunny")
		first := newKeyPair(t)
		if resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", carol.Token,
			matrix.Object{"master_key": signingKeyObject(t, carol.UserID, first.public, "master", first.private, first.id)}); resp.Status != http.StatusOK {
			t.Fatalf("storing the first key set: got %d %s", resp.Status, resp.Raw)
		}
		next := newKeyPair(t)
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", carol.Token,
			matrix.Object{
				"auth":       matrix.Object{"type": "m.login.dummy"},
				"master_key": signingKeyObject(t, carol.UserID, next.public, "master", next.private, next.id),
			})
		if resp.Status != http.StatusUnauthorized {
			t.Errorf("got %d %s, want 401", resp.Status, resp.Raw)
		}
	})
	t.Run("nothing at all", func(t *testing.T) {
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", "", nil)
		if resp.Status != http.StatusUnauthorized {
			t.Errorf("unauthenticated upload: got %d, want 401", resp.Status)
		}
	})

	// Nothing was stored by any of those attempts.
	queried := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
		map[string]any{"device_keys": map[string]any{alice.UserID: []string{}}}, http.StatusOK)
	if byEntity, ok := queried.Body["device_keys"].(map[string]any); ok {
		if entry, ok := byEntity[alice.UserID].(map[string]any); ok && len(entry) > 0 {
			t.Errorf("a rejected key was stored anyway: %s", queried.Raw)
		}
	}
}

// TestKeySignatures covers POST /keys/signatures/upload: signing your own device
// with your master key, which is what turns the shield in a client from red to
// grey, and the checks that come with it.
func TestKeySignatures(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	// A device publishes its identity key, which is the object that gets signed.
	deviceSigning := newKeyPair(t)
	deviceKey := matrix.Object{
		"user_id":    alice.UserID,
		"device_id":  alice.DeviceID,
		"algorithms": []string{"m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"},
		"keys": matrix.Object{
			"curve25519:" + alice.DeviceID:      base64.RawStdEncoding.EncodeToString(deviceSigning.public),
			"signing.ed25519:" + alice.DeviceID: base64.RawStdEncoding.EncodeToString(deviceSigning.public),
		},
		"signatures": matrix.Object{},
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/upload", alice.Token,
		matrix.Object{"device_id": alice.DeviceID, "device_key": deviceKey}, http.StatusOK)

	master := newKeyPair(t)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{
			"auth":       passwordAuth(alice.UserID, "wonderland"),
			"master_key": signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id),
		}, http.StatusOK)

	// The device key signed by the master key.
	signed := matrix.Object{
		"user_id":    alice.UserID,
		"device_id":  alice.DeviceID,
		"algorithms": deviceKey["algorithms"],
		"keys":       deviceKey["keys"],
	}
	signature, err := matrix.SignEd25519(master.private, signed)
	if err != nil {
		t.Fatalf("signing the device key: %v", err)
	}
	signed["signatures"] = matrix.Object{
		alice.UserID: matrix.Object{master.id: base64.RawStdEncoding.EncodeToString(signature)},
	}
	// The signature arrives in the specification's own shape: a bare map of user
	// ID to key ID to object, which is what ruma and matrix-js-sdk send.
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/signatures/upload", alice.Token,
		matrix.Object{alice.UserID: matrix.Object{alice.DeviceID: signed}},
		http.StatusOK); len(resp.Body) != 0 {
		t.Errorf("the signature upload returned a body: %s", resp.Raw)
	}

	queried := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
		map[string]any{"device_keys": map[string]any{alice.UserID: []string{alice.DeviceID}}}, http.StatusOK)
	byEntity, _ := queried.Body["device_keys"].(map[string]any)
	mine, _ := byEntity[alice.UserID].(map[string]any)
	stored, _ := mine[alice.DeviceID].(map[string]any)
	signatures, _ := stored["signatures"].(map[string]any)
	byKey, _ := signatures[alice.UserID].(map[string]any)
	if byKey[master.id] == nil {
		t.Errorf("the signature never reached the stored device key: %s", queried.Raw)
	}

	t.Run("a signature that does not match the master key", func(t *testing.T) {
		forged := newKeyPair(t)
		bad := matrix.Object{
			"user_id":    alice.UserID,
			"device_id":  alice.DeviceID,
			"algorithms": deviceKey["algorithms"],
			"keys":       deviceKey["keys"],
		}
		badSignature, err := matrix.SignEd25519(forged.private, bad)
		if err != nil {
			t.Fatalf("signing with the wrong key: %v", err)
		}
		bad["signatures"] = matrix.Object{
			// The signature claims to come from the master key but was made
			// with another one.
			alice.UserID: matrix.Object{master.id: base64.RawStdEncoding.EncodeToString(badSignature)},
		}
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/signatures/upload", alice.Token,
			matrix.Object{"signed_users": map[string]any{alice.UserID: map[string]any{alice.DeviceID: bad}}})
		if resp.Status != http.StatusBadRequest || resp.ErrCode() != "M_INVALID_PARAM" {
			t.Errorf("a forged signature: got %d %s", resp.Status, resp.Raw)
		}
	})

	t.Run("a signature for something nobody has", func(t *testing.T) {
		// A signature for a device that is gone is not an error: the client simply
		// has nothing to attach it to.
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/signatures/upload", alice.Token,
			matrix.Object{"signed_users": map[string]any{
				alice.UserID: map[string]any{"deleteddevice": matrix.Object{
					"user_id":   alice.UserID,
					"device_id": "deleteddevice",
					"signatures": matrix.Object{alice.UserID: matrix.Object{
						master.id: "aWdub3JhYmxl",
					}},
				}},
			}})
		if resp.Status != http.StatusOK {
			t.Errorf("signing an unknown device: got %d %s, want 200", resp.Status, resp.Raw)
		}
	})

	// The cross-signing keys of a deactivated account go away, so that a deleted
	// user cannot keep vouching for anything.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/account/deactivate", alice.Token,
		matrix.Object{"auth": passwordAuth(alice.UserID, "wonderland")}, http.StatusOK)
	response := server.Do(http.MethodPost, "/_matrix/client/v3/keys/query", bobToken(server),
		map[string]any{"device_keys": map[string]any{alice.UserID: []string{}}})
	if response.Status != http.StatusOK {
		t.Fatalf("querying the keys of a deactivated account: %d %s", response.Status, response.Raw)
	}
	var emptied struct {
		DeviceKeys map[string]map[string]matrix.Object `json:"device_keys"`
	}
	if err := json.Unmarshal(response.Raw, &emptied); err != nil {
		t.Fatalf("decoding the query: %v (%s)", err, response.Raw)
	}
	if len(emptied.DeviceKeys[alice.UserID]) != 0 {
		t.Errorf("a deactivated account still has keys: %s", response.Raw)
	}
}

// bobToken logs in a second account, for the query that comes after Alice
// deactivated herself.
func bobToken(server *Server) string {
	return server.Register("bob", "rubberduck").Token
}

// TestKeySignaturesRequestShapes checks that a signature is stored whichever
// shape the request arrives in. The specification's body is a bare map of user
// ID to key ID to signed object - what ruma, the types behind the Rust crypto
// stack Element ships, and matrix-js-sdk both send - while "signed_users" and
// "signed_keys" are the wrapped shapes Lattice has taken from the start. A
// server that reads only the wrappers answers a real client's upload with 200
// and stores nothing, and the session that upload was meant to verify stays
// unverified for good: this is how Element ended up asking a brand-new account
// to verify itself forever. Each case signs its own device, so no earlier case
// can mask a later one.
func TestKeySignaturesRequestShapes(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	// One master key signs the devices of every case; the first set of keys of a
	// new account asks for no interactive auth exchange.
	master := newKeyPair(t)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{"master_key": signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id)},
		http.StatusOK)

	cases := []struct {
		name  string
		shape func(deviceID string, signed matrix.Object) matrix.Object
	}{
		{
			name: "the specification's bare map",
			shape: func(deviceID string, signed matrix.Object) matrix.Object {
				return matrix.Object{alice.UserID: matrix.Object{deviceID: signed}}
			},
		},
		{
			name: "the signed_users wrapper",
			shape: func(deviceID string, signed matrix.Object) matrix.Object {
				return matrix.Object{"signed_users": map[string]any{alice.UserID: map[string]any{deviceID: signed}}}
			},
		},
		{
			name: "the signed_keys list",
			shape: func(_ string, signed matrix.Object) matrix.Object {
				return matrix.Object{"signed_keys": []any{signed}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh device signs in, publishes its identity key, and is signed
			// with the master key through this case's shape.
			session := server.Login("alice", "wonderland")
			signing := newKeyPair(t)
			deviceKey := matrix.Object{
				"user_id":    alice.UserID,
				"device_id":  session.DeviceID,
				"algorithms": []string{"m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"},
				"keys": matrix.Object{
					"curve25519:" + session.DeviceID: base64.RawStdEncoding.EncodeToString(signing.public),
					"ed25519:" + session.DeviceID:    base64.RawStdEncoding.EncodeToString(signing.public),
				},
			}
			server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/upload", session.Token,
				matrix.Object{"device_id": session.DeviceID, "device_keys": deviceKey}, http.StatusOK)

			signed := matrix.Object{
				"user_id":    alice.UserID,
				"device_id":  session.DeviceID,
				"algorithms": deviceKey["algorithms"],
				"keys":       deviceKey["keys"],
			}
			signature, err := matrix.SignEd25519(master.private, signed)
			if err != nil {
				t.Fatalf("signing the device key: %v", err)
			}
			signed["signatures"] = matrix.Object{
				alice.UserID: matrix.Object{master.id: base64.RawStdEncoding.EncodeToString(signature)},
			}

			if resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/signatures/upload", alice.Token,
				tc.shape(session.DeviceID, signed)); resp.Status != http.StatusOK {
				t.Fatalf("uploading the signature: got %d %s, want 200", resp.Status, resp.Raw)
			}
			queried := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
				matrix.Object{"device_keys": map[string][]string{alice.UserID: nil}}, http.StatusOK)
			byEntity, _ := queried.Body["device_keys"].(map[string]any)
			mine, _ := byEntity[alice.UserID].(map[string]any)
			stored, ok := mine[session.DeviceID].(map[string]any)
			if !ok {
				t.Fatalf("the device published no keys: %s", queried.Raw)
			}
			signatures, _ := stored["signatures"].(map[string]any)
			byKey, _ := signatures[alice.UserID].(map[string]any)
			if byKey[master.id] == nil {
				t.Errorf("the signature sent as %s never reached the stored device key: %s", tc.name, queried.Raw)
			}
		})
	}
}

// TestCrossSigningUsageSpellings checks that a cross-signing key whose usage
// field spells the kind with an underscore is accepted. The specification writes
// self-signing and user-signing with a hyphen; the Rust crypto stack Element
// ships writes them with an underscore, and refusing a correctly signed key over
// that difference leaves the user unable to set up encryption at all.
func TestCrossSigningUsageSpellings(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	master := newKeyPair(t)
	selfSigning := newKeyPair(t)

	upload := func(body matrix.Object) Response {
		return server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token, body)
	}
	// A new account publishes its first set without the interactive auth
	// exchange, so the spellings are all this test has to answer for.
	accepted := upload(matrix.Object{
		"master_key":       signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id),
		"self_signing_key": signingKeyObject(t, alice.UserID, selfSigning.public, "self_signing", master.private, master.id),
	})
	if accepted.Status != http.StatusOK {
		t.Fatalf("uploading keys with the underscore spellings: got %d %s", accepted.Status, accepted.Raw)
	}

	// The object comes back exactly as it arrived: rewriting the usage field would
	// change what its own signature covers, and the client verifies that signature.
	queried := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
		matrix.Object{"device_keys": map[string][]string{alice.UserID: nil}}, http.StatusOK)
	stored := crossSigningFromQuery(queried)[selfSigning.id]
	usage, _ := stored["usage"].([]any)
	if len(usage) != 1 || usage[0] != "self_signing" {
		t.Errorf("the stored key lost the spelling it arrived with: %v (body %s)", stored["usage"], queried.Raw)
	}
}

// TestCrossSigningAuthShapes answers the interactive auth challenge the way
// Element does, so a difference in field names cannot break a real client: the
// password stage is retried in the same session, the account is named in an
// "identifier" object of type m.id.user, and a first attempt with the wrong
// password only asks again rather than ending the exchange.
func TestCrossSigningAuthShapes(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	// The exchange stands in front of changing an identity, so one has to exist:
	// the first key set of a new account arrives without it.
	first := newKeyPair(t)
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{"master_key": signingKeyObject(t, alice.UserID, first.public, "master", first.private, first.id)}); resp.Status != http.StatusOK {
		t.Fatalf("storing the first master key: got %d %s", resp.Status, resp.Raw)
	}

	master := newKeyPair(t)
	object := signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id)

	// The Rust crypto also signs the master key with the device that uploaded it,
	// so a real request carries two signatures on that one object. Both have to
	// survive: dropping the device signature would lose the link between the
	// identity and the device that published it.
	deviceKey := matrix.Object{
		"user_id":    alice.UserID,
		"device_id":  alice.DeviceID,
		"algorithms": []string{"m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"},
		"keys": matrix.Object{
			"curve25519:" + alice.DeviceID: base64.RawStdEncoding.EncodeToString(master.public),
			"ed25519:" + alice.DeviceID:    base64.RawStdEncoding.EncodeToString(master.public),
		},
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/upload", alice.Token,
		matrix.Object{"device_id": alice.DeviceID, "device_key": deviceKey}, http.StatusOK)
	unsigned := matrix.Object{
		"user_id": object["user_id"],
		"usage":   object["usage"],
		"keys":    object["keys"],
	}
	deviceSig, err := matrix.SignEd25519(master.private, unsigned)
	if err != nil {
		t.Fatalf("signing the master key with the device: %v", err)
	}
	signatures, _ := object["signatures"].(matrix.Object)
	byUser, _ := signatures[alice.UserID].(matrix.Object)
	byUser["ed25519:"+alice.DeviceID] = base64.RawStdEncoding.EncodeToString(deviceSig)

	challenge := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{"master_key": object})
	if challenge.Status != http.StatusUnauthorized {
		t.Fatalf("the first attempt: got %d %s, want 401", challenge.Status, challenge.Raw)
	}
	session := challenge.String("session")
	if session == "" {
		t.Fatalf("the challenge carries no session: %s", challenge.Raw)
	}

	// A wrong password keeps the same session and the same flow list, which is
	// what lets the client show the field again instead of giving up.
	wrong := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{
			"master_key": object,
			"auth": matrix.Object{
				"type":       "m.login.password",
				"identifier": matrix.Object{"type": "m.id.user", "user": alice.UserID},
				"password":   "not-it",
				"session":    session,
			},
		})
	if wrong.Status != http.StatusUnauthorized {
		t.Fatalf("a wrong password: got %d %s, want 401", wrong.Status, wrong.Raw)
	}
	if again := wrong.String("session"); again != session {
		t.Errorf("a refused stage changed the session from %q to %q", session, again)
	}
	if completed, ok := wrong.Body["completed"].([]any); ok && len(completed) != 0 {
		t.Errorf("a refused stage was marked completed: %v", wrong.Body["completed"])
	}

	// Element sends no legacy "user" field and no client_secret; the identifier
	// object alone has to identify the account.
	accepted := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{
			"master_key": object,
			"auth": matrix.Object{
				"type":       "m.login.password",
				"identifier": matrix.Object{"type": "m.id.user", "user": alice.UserID},
				"password":   "wonderland",
				"session":    session,
			},
		})
	if accepted.Status != http.StatusOK {
		t.Fatalf("the retry with an identifier object: got %d %s", accepted.Status, accepted.Raw)
	}
	// The stored object keeps both signatures: the one the master key made over
	// itself, and the one the uploading device made, which is the link between an
	// identity and the device that published it.
	queried := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
		matrix.Object{"device_keys": map[string][]string{alice.UserID: nil}}, http.StatusOK)
	stored := crossSigningFromQuery(queried)[master.id]
	if stored == nil {
		t.Fatalf("the accepted upload stored nothing: %s", queried.Raw)
	}
	kept, _ := stored["signatures"].(map[string]any)
	bySigner, _ := kept[alice.UserID].(map[string]any)
	for _, keyID := range []string{master.id, "ed25519:" + alice.DeviceID} {
		if _, ok := bySigner[keyID]; !ok {
			t.Errorf("the stored master key lost the signature under %s: %v", keyID, bySigner)
		}
	}

	// A spent session id does not lock the client out: the session is a handle for
	// the exchange, not a capability. Knowing the password is the gate, so a client
	// that retries with a stale id and valid credentials still gets through - and
	// one without them does not. Both attempts carry a fresh master key, because
	// only a request that would change the stored identity runs the exchange.
	next := newKeyPair(t)
	nextMaster := signingKeyObject(t, alice.UserID, next.public, "master", next.private, next.id)
	guessed := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{
			"master_key": nextMaster,
			"auth": matrix.Object{
				"type":       "m.login.password",
				"identifier": matrix.Object{"type": "m.id.user", "user": alice.UserID},
				"password":   "letmein",
				"session":    "not-a-session-any-more",
			},
		})
	if guessed.Status == http.StatusOK {
		t.Errorf("a guessed password through a stale session id was accepted: %s", guessed.Raw)
	}
	stale := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{
			"master_key": nextMaster,
			"auth": matrix.Object{
				"type":       "m.login.password",
				"identifier": matrix.Object{"type": "m.id.user", "user": alice.UserID},
				"password":   "wonderland",
				"session":    "not-a-session-any-more",
			},
		})
	if stale.Status != http.StatusOK {
		t.Errorf("a retry with a stale session id: got %d %s, want 200", stale.Status, stale.Raw)
	}
}
