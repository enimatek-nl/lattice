package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enimatek/lattice/internal/matrixjson"
)

// testKeys is a device's worth of key material, built the way a client builds it: signed by
// the device's own ed25519 key, because a server that accepted unsigned keys would be the
// weakest point in every conversation that passed through it.
type testKeys struct {
	DeviceKeys  map[string]any
	OneTimeKeys map[string]any
	signing     ed25519.PrivateKey
}

// newTestKeys makes a device key document and the requested number of one-time keys for a
// user and device.
func newTestKeys(t *testing.T, userID, deviceID string, serials ...string) testKeys {
	t.Helper()
	identityPub, identityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating an identity key: %v", err)
	}
	curvePub := make([]byte, 32)
	if _, err := rand.Read(curvePub); err != nil {
		t.Fatalf("generating a curve25519 key: %v", err)
	}
	doc := map[string]any{
		"user_id":    userID,
		"device_id":  deviceID,
		"algorithms": []any{"m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"},
		"keys": map[string]any{
			matrixjson.KeyID("curve25519", deviceID): matrixjson.EncodeUnpadded(curvePub),
			matrixjson.KeyID("ed25519", deviceID):    matrixjson.EncodeUnpadded(identityPub),
		},
	}
	if err := matrixjson.SignJSONInPlace(userID, matrixjson.KeyID("ed25519", deviceID), identityPriv, doc); err != nil {
		t.Fatalf("signing the device keys: %v", err)
	}
	keys := testKeys{DeviceKeys: doc, signing: identityPriv}
	for _, serial := range serials {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			t.Fatalf("generating a one-time key: %v", err)
		}
		keyID := matrixjson.KeyID("signed_curve25519", serial)
		otk := map[string]any{"key": matrixjson.EncodeUnpadded(secret)}
		if err := matrixjson.SignJSONInPlace(userID, matrixjson.KeyID("ed25519", deviceID), identityPriv, otk); err != nil {
			t.Fatalf("signing a one-time key: %v", err)
		}
		if keys.OneTimeKeys == nil {
			keys.OneTimeKeys = map[string]any{}
		}
		keys.OneTimeKeys[keyID] = otk
	}
	return keys
}

// TestDeviceKeysRoundTrip covers upload, query and claim: what a client does to let another
// of its own users start a session with it.
func TestDeviceKeysRoundTrip(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, aliceDevice := h.register(t, "keysalice", "pw-keysalice", "")
	bob, bobToken, _ := h.register(t, "keysbob", "pw-keysbob", "")
	keys := newTestKeys(t, alice, aliceDevice, "A", "B")

	upload := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{
		"device_keys":   keys.DeviceKeys,
		"one_time_keys": keys.OneTimeKeys,
	}, aliceToken), http.StatusOK).decoded(t)
	counts, _ := upload["one_time_key_counts"].(map[string]any)
	if got, _ := counts["signed_curve25519"].(float64); got != 2 {
		t.Fatalf("one_time_key_counts = %v, want two signed_curve25519 keys", upload["one_time_key_counts"])
	}

	// The document comes back as it was sent, because its signature is over exactly those
	// bytes.
	queried := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/query", map[string]any{
		"device_keys": map[string][]string{alice: {}},
	}, bobToken), http.StatusOK).decoded(t)
	returned := deviceKeyOf(t, queried, alice, aliceDevice)
	if returned == nil {
		t.Fatalf("the query returned no device keys: %v", queried)
	}
	for _, field := range []string{"user_id", "device_id", "algorithms", "keys", "signatures"} {
		if !sameJSON(returned[field], keys.DeviceKeys[field]) {
			t.Errorf("the %s of the stored document differs from what was uploaded: %v vs %v",
				field, returned[field], keys.DeviceKeys[field])
		}
	}

	// A user with no keys is still named, with nothing under them.
	quiet := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/query", map[string]any{
		"device_keys": map[string][]string{bob: {}},
	}, aliceToken), http.StatusOK).decoded(t)
	section, ok := quiet["device_keys"].(map[string]any)
	if !ok {
		t.Fatalf("the query returned no device_keys section: %v", quiet)
	}
	empty, ok := section[bob].(map[string]any)
	if !ok {
		t.Fatalf("a user with no keys is not named at all: %v", section)
	}
	if len(empty) != 0 {
		t.Errorf("a user with no keys was given some: %v", empty)
	}

	// Claiming a key retires it: a key that can start two sessions says nothing about a
	// session being new.
	claim := func() map[string]any {
		t.Helper()
		return h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/claim", map[string]any{
			"one_time_keys": map[string]any{alice: map[string]string{aliceDevice: "signed_curve25519"}},
		}, bobToken), http.StatusOK).decoded(t)
	}
	first := claim()
	handed := oneTimeKeysOf(t, first, alice, aliceDevice)
	if len(handed) != 1 {
		t.Fatalf("a claim returned %d keys, want one: %v", len(handed), first)
	}
	if _, ok := keys.OneTimeKeys[onlyKeyID(t, handed)]; !ok {
		t.Errorf("the claimed key was not one this device offered: %v", handed)
	}
	second := claim()
	if len(oneTimeKeysOf(t, second, alice, aliceDevice)) != 1 {
		t.Errorf("the second claim returned no key: %v", second)
	}
	third := claim()
	section, _ = third["one_time_keys"].(map[string]any)
	if _, present := section[alice]; present {
		t.Errorf("a third claim handed out a key that had already been used: %v", third)
	}
	counts = countsOf(t, h, aliceToken)
	if got, _ := counts["signed_curve25519"].(float64); got != 0 {
		t.Errorf("after two claims the counts are %v, want none left", counts)
	}
}

// TestDeviceKeysClaimOrder checks that keys are handed out in the order they were uploaded.
func TestDeviceKeysClaimOrder(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, aliceDevice := h.register(t, "orderalice", "pw-orderalice", "")
	keys := newTestKeys(t, alice, aliceDevice, "Z", "A")
	all := keys.OneTimeKeys

	// Two uploads, and the second offers the key whose ID sorts first. Ordering by key ID
	// would hand that one out first, which is not the order the client offered them in.
	firstID, secondID := "signed_curve25519:Z", "signed_curve25519:A"
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{
		"device_keys":   keys.DeviceKeys,
		"one_time_keys": map[string]any{firstID: all[firstID]},
	}, aliceToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{
		"one_time_keys": map[string]any{secondID: all[secondID]},
	}, aliceToken), http.StatusOK)

	for _, want := range []string{firstID, secondID} {
		claimed := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/claim", map[string]any{
			"one_time_keys": map[string]any{alice: map[string]string{aliceDevice: "signed_curve25519"}},
		}, aliceToken), http.StatusOK).decoded(t)
		handed := oneTimeKeysOf(t, claimed, alice, aliceDevice)
		if len(handed) != 1 {
			t.Fatalf("a claim returned %v, want exactly %s", claimed, want)
		}
		if _, ok := handed[want]; !ok {
			t.Errorf("the claim returned %v, want %s: upload order, not key ID order", handed, want)
		}
	}
}

// TestDeviceKeysIdempotent checks that offering the same keys twice is not an error: a client
// that never saw the response to its first upload will upload them again.
func TestDeviceKeysIdempotent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, aliceDevice := h.register(t, "idemalice", "pw-idemalice", "")
	keys := newTestKeys(t, alice, aliceDevice, "A", "B", "C", "D")
	body := map[string]any{"device_keys": keys.DeviceKeys, "one_time_keys": keys.OneTimeKeys}

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", body, aliceToken), http.StatusOK)
	counts := countsOf(t, h, aliceToken)
	if got, _ := counts["signed_curve25519"].(float64); got != 4 {
		t.Fatalf("counts after the first upload: %v", counts)
	}
	again := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", body, aliceToken), http.StatusOK).decoded(t)
	againCounts, _ := again["one_time_key_counts"].(map[string]any)
	if got, _ := againCounts["signed_curve25519"].(float64); got != 4 {
		t.Errorf("re-uploading the same keys changed the counts to %v, want them unchanged", againCounts)
	}
}

// TestDeviceKeysRefused checks what this server will not store.
func TestDeviceKeysRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, aliceDevice := h.register(t, "refusealice", "pw-refusealice", "")
	bobID, bobToken, bobDevice := h.register(t, "refusebob", "pw-refusebob", "")
	keys := newTestKeys(t, alice, aliceDevice, "A")

	// A document with no algorithms, no keys or no signatures cannot be checked or used.
	for _, missing := range []string{"algorithms", "keys", "signatures"} {
		broken := map[string]any{}
		for field, value := range keys.DeviceKeys {
			if field == missing {
				continue
			}
			broken[field] = value
		}
		got := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload",
			map[string]any{"device_keys": broken}, aliceToken), http.StatusBadRequest).decoded(t)
		if got["errcode"] != "M_BAD_JSON" {
			t.Errorf("device keys with no %s: %v", missing, got)
		}
	}

	// Keys naming another user are refused whoever sent them.
	impersonated := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload",
		map[string]any{"device_keys": keys.DeviceKeys}, bobToken), http.StatusBadRequest).decoded(t)
	if impersonated["errcode"] != "M_INVALID_PARAM" {
		t.Errorf("keys for another user: %v", impersonated)
	}

	// A signature from a key the document does not publish is not a signature.
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	unsigned := map[string]any{}
	for field, value := range keys.DeviceKeys {
		if field == "signatures" {
			continue
		}
		unsigned[field] = value
	}
	// The published ed25519 key is the honest one; the signature is by a different key that
	// happens to carry the same name.
	unsigned["keys"] = keys.DeviceKeys["keys"]
	if err := matrixjson.SignJSONInPlace(alice, matrixjson.KeyID("ed25519", aliceDevice), otherPriv, unsigned); err != nil {
		t.Fatalf("signing with the wrong key: %v", err)
	}
	// and the device_id is another device's, so the key names are that device's too.
	unsigned["device_id"] = "OTHERDEV"
	bad := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload",
		map[string]any{"device_keys": unsigned}, aliceToken), http.StatusBadRequest).decoded(t)
	if bad["errcode"] == "" {
		t.Errorf("a device key signed by the wrong key was accepted: %v", bad)
	}

	// An unsigned one-time key is refused, and so is one signed by a key this device never
	// published.
	unsignedOTK := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{
		"one_time_keys": map[string]any{"signed_curve25519:X": map[string]any{"key": "aaaa"}},
	}, aliceToken), http.StatusBadRequest).decoded(t)
	if unsignedOTK["errcode"] == "" {
		t.Errorf("an unsigned one-time key was accepted: %v", unsignedOTK)
	}
	otherKeys := newTestKeys(t, bobID, bobDevice, "A")
	mismatched := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{
		"one_time_keys": otherKeys.OneTimeKeys,
	}, aliceToken), http.StatusBadRequest).decoded(t)
	if mismatched["errcode"] == "" {
		t.Errorf("a one-time key signed by another device was accepted: %v", mismatched)
	}

	// Fallback keys are refused rather than quietly stored as something else.
	fallback := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{
		"fallback_keys": map[string]any{"signed_curve25519:": map[string]any{"key": "aaaa"}},
	}, aliceToken), http.StatusBadRequest).decoded(t)
	if fallback["errcode"] != "M_INVALID_PARAM" {
		t.Errorf("a fallback key: %v", fallback)
	}
}

// TestDeviceKeyUploadAnnouncesChange checks the other half of a key upload: the clients that
// share a room with this device are told to ask for its keys again.
func TestDeviceKeyUploadAnnouncesChange(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, aliceDevice := h.register(t, "announcealice", "pw-announcealice", "")
	_, bobToken, _ := h.register(t, "announcebob", "pw-announcebob", "")

	created := h.mustStatus(t, h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, aliceToken), http.StatusOK).decoded(t)
	roomID, _ := created["room_id"].(string)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)

	_, bobSince := syncNow(t, h, bobToken, "")
	keys := newTestKeys(t, alice, aliceDevice, "A")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{
		"device_keys": keys.DeviceKeys,
	}, aliceToken), http.StatusOK)

	body, _ := syncNow(t, h, bobToken, "&since="+bobSince)
	if !inDeviceListSection(t, body, "changed", alice) {
		t.Errorf("alice's key upload never reached bob's sync: %v", body["device_lists"])
	}
}

// TestDeviceKeyQuerySince checks that a query with a token reports the devices that changed
// since it, which is what makes a client's key cache worth keeping.
func TestDeviceKeyQuerySince(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, aliceDevice := h.register(t, "sincealice", "pw-sincealice", "")
	_, bobToken, _ := h.register(t, "sincebob", "pw-sincebob", "")

	keys := newTestKeys(t, alice, aliceDevice)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload",
		map[string]any{"device_keys": keys.DeviceKeys}, aliceToken), http.StatusOK)
	_, token := syncNow(t, h, aliceToken, "")

	if deviceKeyOf(t, h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/query",
		map[string]any{"device_keys": map[string][]string{alice: {}}, "since": token},
		bobToken), http.StatusOK).decoded(t), alice, aliceDevice) != nil {
		t.Errorf("a query since a token that covers the upload still reported the device")
	}

	updated := newTestKeys(t, alice, aliceDevice)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload",
		map[string]any{"device_keys": updated.DeviceKeys}, aliceToken), http.StatusOK)
	if deviceKeyOf(t, h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/query",
		map[string]any{"device_keys": map[string][]string{alice: {}}, "since": token},
		bobToken), http.StatusOK).decoded(t), alice, aliceDevice) == nil {
		t.Errorf("a device whose keys changed after the token was not reported")
	}

	// A token this server did not issue is the client's mistake.
	if got := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/query",
		map[string]any{"device_keys": map[string][]string{alice: {}}, "since": "not-a-token"},
		bobToken), http.StatusBadRequest).decoded(t); got["errcode"] != "M_INVALID_PARAM" {
		t.Errorf("a query with a bad token: %v", got)
	}
}

// countsOf reads this user's one-time key counts by uploading nothing.
func countsOf(t *testing.T, h *harness, token string) map[string]any {
	t.Helper()
	body := h.mustStatus(t, h.post(t, "/_matrix/client/v3/keys/upload", map[string]any{}, token), http.StatusOK).decoded(t)
	counts, _ := body["one_time_key_counts"].(map[string]any)
	return counts
}

// deviceKeyOf finds one device's document in a query response.
func deviceKeyOf(t *testing.T, response map[string]any, userID, deviceID string) map[string]any {
	t.Helper()
	section, _ := response["device_keys"].(map[string]any)
	user, _ := section[userID].(map[string]any)
	device, _ := user[deviceID].(map[string]any)
	return device
}

// oneTimeKeysOf finds one device's claimed keys in a claim response.
func oneTimeKeysOf(t *testing.T, response map[string]any, userID, deviceID string) map[string]any {
	t.Helper()
	section, _ := response["one_time_keys"].(map[string]any)
	user, _ := section[userID].(map[string]any)
	device, _ := user[deviceID].(map[string]any)
	return device
}

// onlyKeyID fails unless the map holds exactly one key, and names it.
func onlyKeyID(t *testing.T, keys map[string]any) string {
	t.Helper()
	if len(keys) != 1 {
		t.Fatalf("%v does not hold exactly one key", keys)
	}
	for keyID := range keys {
		return keyID
	}
	return ""
}

// sameJSON compares two decoded JSON values by their encoding, which is enough for a test
// that wants to know whether a document came back unchanged.
func sameJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}
