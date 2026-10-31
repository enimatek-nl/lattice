package compat

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestElementKeySetupChain walks the exact sequence Element web performs when it
// sets up encryption for a freshly registered account, with the payload shapes
// that client sends rather than the ones Lattice's other tests happen to use:
// the device key signing itself, the interactive auth answered with the legacy
// "user" field instead of an "identifier", the key backup created with PUT, and
// the sessions stored one room at a time. A chain test catches the in-between
// problems that per-endpoint tests miss - a stage that works on its own but not
// in sequence, a response shape a client parses strictly.
func TestElementKeySetupChain(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	password := "wonderland"

	// 1. The device publishes its identity keys, self-signed the way libolm and
	// the Rust crypto do it.
	identity := newKeyPair(t)
	signing := newKeyPair(t)
	deviceKey := matrix.Object{
		"user_id":    alice.UserID,
		"device_id":  alice.DeviceID,
		"algorithms": []string{"m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"},
		"keys": matrix.Object{
			"curve25519:" + alice.DeviceID: base64.RawStdEncoding.EncodeToString(identity.public),
			"ed25519:" + alice.DeviceID:    base64.RawStdEncoding.EncodeToString(signing.public),
		},
	}
	selfSignature, err := matrix.SignEd25519(signing.private, deviceKey)
	if err != nil {
		t.Fatalf("signing the device key: %v", err)
	}
	deviceKey["signatures"] = matrix.Object{
		alice.UserID: matrix.Object{
			"ed25519:" + alice.DeviceID: base64.RawStdEncoding.EncodeToString(selfSignature),
		},
	}
	uploaded := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/upload", alice.Token,
		matrix.Object{
			"device_id":  alice.DeviceID,
			"device_key": deviceKey,
			// The Rust crypto keeps one fallback key around and reuses it, so the
			// upload carries it separately from the one-time keys.
			"fallback_keys": matrix.Object{
				"fallback_curve25519:AAAAAQ": matrix.Object{
					"key": base64.RawStdEncoding.EncodeToString(identity.public),
				},
			},
			"one_time_keys": oneTimeKeys("CHAIN", 5),
		}, http.StatusOK)
	counts, _ := uploaded.Body["one_time_key_counts"].(map[string]any)
	if counts == nil || counts["signed_curve25519"] == nil {
		t.Fatalf("the upload response has no one-time key counts: %s", uploaded.Raw)
	}

	// 2. Cross-signing needs a second authentication. Element answers the first
	// attempt with the interactive auth dialog, whose password entry sends the
	// account name in the legacy "user" field with no "identifier" beside it.
	master := newKeyPair(t)
	selfSigningKey := newKeyPair(t)
	userSigningKey := newKeyPair(t)
	attempt := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{"master_key": signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id)})
	if attempt.Status != http.StatusUnauthorized {
		t.Fatalf("the first cross-signing upload: got %d %s, want 401", attempt.Status, attempt.Raw)
	}
	session := attempt.String("session")
	if session == "" {
		t.Fatalf("the challenge carries no session: %s", attempt.Raw)
	}
	challenge := attempt.Body["flows"]
	if challenge == nil {
		t.Fatalf("the challenge carries no flows: %s", attempt.Raw)
	}
	accepted := server.Do(http.MethodPost, "/_matrix/client/v3/keys/device_signing/upload", alice.Token,
		matrix.Object{
			"auth": matrix.Object{
				"type":     "m.login.password",
				"user":     alice.UserID,
				"password": password,
				"session":  session,
			},
			"master_key":       signingKeyObject(t, alice.UserID, master.public, "master", master.private, master.id),
			"self_signing_key": signingKeyObject(t, alice.UserID, selfSigningKey.public, "self_signing", master.private, master.id),
			"user_signing_key": signingKeyObject(t, alice.UserID, userSigningKey.public, "user_signing", master.private, master.id),
		})
	if accepted.Status != http.StatusOK {
		t.Fatalf("the cross-signing upload with the legacy auth shape: got %d %s",
			accepted.Status, accepted.Raw)
	}

	// 3. The self-signing key signs the device, which is what marks the device as
	// belonging to the user.
	signedDevice := matrix.Object{
		"user_id":    alice.UserID,
		"device_id":  alice.DeviceID,
		"algorithms": deviceKey["algorithms"],
		"keys":       deviceKey["keys"],
	}
	deviceSignature, err := matrix.SignEd25519(selfSigningKey.private, signedDevice)
	if err != nil {
		t.Fatalf("signing the device with the self-signing key: %v", err)
	}
	signedDevice["signatures"] = matrix.Object{
		alice.UserID: matrix.Object{
			selfSigningKey.id: base64.RawStdEncoding.EncodeToString(deviceSignature),
		},
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/signatures/upload", alice.Token,
		matrix.Object{"signed_keys": []any{signedDevice}}); resp.Status != http.StatusOK {
		t.Fatalf("uploading the device signature: got %d %s", resp.Status, resp.Raw)
	}

	// 4. Key backup: Element creates the version with PUT and stores sessions
	// room by room.
	authData := matrix.Object{"public_key": base64.RawStdEncoding.EncodeToString(identity.public),
		"signatures": matrix.Object{}}
	created := server.Do(http.MethodPut, "/_matrix/client/v3/room_keys/version", alice.Token,
		matrix.Object{"algorithm": "m.megolm_backup.v1.curve25519-aes-sha2", "auth_data": authData})
	if created.Status != http.StatusOK {
		t.Fatalf("creating the key backup with PUT: got %d %s", created.Status, created.Raw)
	}
	version := created.String("version")
	if version == "" {
		t.Fatalf("the backup version response has no version string: %s", created.Raw)
	}
	stored := server.Do(http.MethodPut, "/_matrix/client/v3/room_keys/keys?version="+version, alice.Token,
		matrix.Object{"rooms": map[string]any{"!room:test.server": map[string]any{
			"sessions": map[string]any{
				"sessionOne": map[string]any{
					"first_message_index": 0,
					"forwarded_count":     0,
					"is_verified":         false,
					"session_data":        map[string]any{"ephemeral": "opaque", "mac": "opaque", "ciphertext": "opaque"},
				},
			},
		}}})
	if stored.Status != http.StatusOK {
		t.Fatalf("storing a backup session: got %d %s", stored.Status, stored.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/room_keys/keys", alice.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("reading the backed up sessions: got %d %s", resp.Status, resp.Raw)
	}

	// 5. What the next device sees: the cross-signing keys next to the device keys,
	// each with the key material and the signatures the client has to verify.
	queried := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token,
		matrix.Object{"device_keys": map[string][]string{alice.UserID: nil}}, http.StatusOK)
	byEntity, _ := queried.Body["device_keys"].(map[string]any)
	mine, _ := byEntity[alice.UserID].(map[string]any)
	for _, key := range []keyPair{master, selfSigningKey, userSigningKey} {
		stored, ok := mine[key.id].(map[string]any)
		if !ok {
			t.Errorf("the query response has no cross-signing key %s: %s", key.id, queried.Raw)
			continue
		}
		if stored["user_id"] != alice.UserID {
			t.Errorf("the stored cross-signing key %s has user_id %v", key.id, stored["user_id"])
		}
		if _, ok := stored["keys"].(map[string]any); !ok {
			t.Errorf("the stored cross-signing key %s has no keys object: %v", key.id, stored["keys"])
		}
		signatures, _ := stored["signatures"].(map[string]any)
		if _, ok := signatures[alice.UserID].(map[string]any); !ok {
			t.Errorf("the stored cross-signing key %s has no signature by its own user: %v", key.id, stored["signatures"])
		}
		usage, ok := stored["usage"].([]any)
		if !ok || len(usage) == 0 || usage[0] == "" {
			t.Errorf("the stored cross-signing key %s has no usage: %v", key.id, stored["usage"])
		}
	}
	// The device signature survived, which is what makes the device trusted.
	device, _ := mine["ed25519:"+alice.DeviceID].(map[string]any)
	if device == nil {
		device, _ = mine[alice.DeviceID].(map[string]any)
	}
	if device == nil {
		t.Fatalf("the query response has no device entry for %s: %s", alice.DeviceID, queried.Raw)
	}
	signatures, _ := device["signatures"].(map[string]any)
	if byUser, ok := signatures[alice.UserID].(map[string]any); !ok {
		t.Errorf("the device lost its signatures: %v", device["signatures"])
	} else if _, ok := byUser[selfSigningKey.id]; !ok {
		t.Errorf("the device is not signed by the self-signing key any more: %v", byUser)
	}

	// 6. A fresh sync carries the key sections the crypto stack reads to decide
	// whether it has work to do.
	synced := server.MustSync(alice, "?timeout=0")
	if synced.DeviceOneTimeCount == nil {
		t.Errorf("the sync has no device_one_time_keys_count")
	} else if (*synced.DeviceOneTimeCount)["signed_curve25519"] <= 0 {
		t.Errorf("the sync reports %v one-time keys, want the ones just uploaded",
			(*synced.DeviceOneTimeCount)["signed_curve25519"])
	}
	if len(synced.UnusedFallbackKeys) == 0 {
		t.Errorf("the sync reports no unused fallback key types, want fallback_curve25519: %v",
			synced.UnusedFallbackKeys)
	}
}
