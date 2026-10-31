package compat

import (
	"fmt"
	"net/http"
	"testing"
)

// fakeDeviceKey builds a device_key object shaped like a real one. The server
// never interprets it: it stores the JSON and hands the same JSON back.
func fakeDeviceKey(userID, deviceID, curve string) map[string]any {
	return map[string]any{
		"user_id":    userID,
		"device_id":  deviceID,
		"algorithms": []string{"m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"},
		"keys": map[string]string{
			"curve25519":                  curve,
			"signing.ed25519:" + deviceID: "sign-" + curve,
		},
		"signatures": map[string]any{},
	}
}

// oneTimeKeys builds a batch of one-time keys with the given prefix.
func oneTimeKeys(prefix string, count int) map[string]any {
	keys := map[string]any{}
	for i := range count {
		id := fmt.Sprintf("signed_curve25519:%s%04d", prefix, i)
		keys[id] = map[string]string{"signed_curve25519": id + "-secret"}
	}
	return keys
}

// keyCounts reads the one_time_key_counts object of an upload response.
func keyCounts(t *testing.T, resp Response) map[string]int64 {
	t.Helper()
	raw, ok := resp.Body["one_time_key_counts"].(map[string]any)
	if !ok {
		t.Fatalf("no one_time_key_counts in %s", resp.Raw)
	}
	counts := map[string]int64{}
	for algorithm, value := range raw {
		count, ok := value.(float64)
		if !ok {
			t.Fatalf("count for %s is not a number: %s", algorithm, resp.Raw)
		}
		counts[algorithm] = int64(count)
	}
	return counts
}

// TestKeyUploadQueryClaim walks the full end-to-end key lifecycle: a device
// publishes its identity key and prekeys, another device looks them up, claims
// each prekey exactly once, and the counts follow along.
func TestKeyUploadQueryClaim(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	cases := []struct {
		name    string
		body    map[string]any
		want    int
		errCode string
		counts  map[string]int64
	}{
		{
			name:   "empty upload reports zeros",
			body:   map[string]any{"device_id": alice.DeviceID},
			want:   http.StatusOK,
			counts: map[string]int64{"curve25519": 0, "signed_curve25519": 0, "fallback_curve25519": 0},
		},
		{
			name: "identity key and two prekeys",
			body: map[string]any{
				"device_id":     alice.DeviceID,
				"device_key":    fakeDeviceKey(alice.UserID, alice.DeviceID, "curve-alice"),
				"one_time_keys": oneTimeKeys("A", 2),
			},
			want:   http.StatusOK,
			counts: map[string]int64{"signed_curve25519": 2},
		},
		{
			name: "re-uploading keeps the keys of the device",
			body: map[string]any{
				"device_id":  alice.DeviceID,
				"device_key": fakeDeviceKey(alice.UserID, alice.DeviceID, "curve-alice"),
			},
			want:   http.StatusOK,
			counts: map[string]int64{"signed_curve25519": 2},
		},
		{
			name: "device id that is not the token's",
			body: map[string]any{"device_id": "SOMETHINGELSE"},
			want: http.StatusBadRequest, errCode: "M_INVALID_PARAM",
		},
		{
			name: "key id without an algorithm",
			body: map[string]any{"one_time_keys": map[string]any{"AAAA": map[string]string{"curve25519": "x"}}},
			want: http.StatusBadRequest, errCode: "M_INVALID_PARAM",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/upload", alice.Token, tc.body)
			if resp.Status != tc.want {
				t.Fatalf("got %d %s, want %d", resp.Status, resp.Raw, tc.want)
			}
			if tc.errCode != "" && resp.ErrCode() != tc.errCode {
				t.Fatalf("errcode = %q, want %q (%s)", resp.ErrCode(), tc.errCode, resp.Raw)
			}
			if tc.counts == nil {
				return
			}
			got := keyCounts(t, resp)
			for algorithm, want := range tc.counts {
				if got[algorithm] != want {
					t.Errorf("%s count = %d, want %d (%s)", algorithm, got[algorithm], want, resp.Raw)
				}
			}
		})
	}

	// A query returns the identity key exactly as it was published.
	query := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token, map[string]any{
		"device_keys": map[string]any{alice.UserID: []string{}},
	}, http.StatusOK)
	devices, _ := query.Body["device_keys"].(map[string]any)
	aliceDevices, ok := devices[alice.UserID].(map[string]any)
	if !ok {
		t.Fatalf("no device keys for alice: %s", query.Raw)
	}
	published, ok := aliceDevices[alice.DeviceID].(map[string]any)
	if !ok {
		t.Fatalf("alice's device %s published no keys: %s", alice.DeviceID, query.Raw)
	}
	keys, _ := published["keys"].(map[string]any)
	if keys["curve25519"] != "curve-alice" {
		t.Errorf("published curve25519 = %v, want curve-alice", keys["curve25519"])
	}
	if _, ok := query.Body["failures"]; !ok {
		t.Errorf("a key query response must carry failures: %s", query.Raw)
	}

	// A query for a user with no keys returns an empty entry.
	empty := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token, map[string]any{
		"device_keys": map[string]any{"@nobody:test.server": []string{}},
	}, http.StatusOK)
	if nested, _ := empty.Body["device_keys"].(map[string]any); len(nested) != 0 {
		t.Errorf("keys were returned for an unknown user: %s", empty.Raw)
	}

	// Each prekey is handed out once, and the counts fall with it.
	claim := func() map[string]any {
		t.Helper()
		resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/claim", alice.Token, map[string]any{
			"one_time_keys": map[string]any{alice.UserID: map[string]string{alice.DeviceID: "signed_curve25519"}},
		}, http.StatusOK)
		granted, _ := resp.Body["one_time_keys"].(map[string]any)
		return granted
	}
	first := claim()
	second := claim()
	third := claim()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("two prekeys were uploaded but only %d were granted (%v, %v)", len(first)+len(second), first, second)
	}
	if len(third) != 0 {
		t.Errorf("a third claim still received a key: %v", third)
	}
	if sameKey(first, second) {
		t.Errorf("the same one-time key was handed out twice: %v and %v", first, second)
	}

	// A claim for a device that does not exist is simply unanswered.
	unknown := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/claim", alice.Token, map[string]any{
		"one_time_keys": map[string]any{alice.UserID: map[string]string{"NODEVICE": "signed_curve25519"}},
	}, http.StatusOK)
	if granted, _ := unknown.Body["one_time_keys"].(map[string]any); len(granted) != 0 {
		t.Errorf("a claim for an unknown device received keys: %s", unknown.Raw)
	}

	// A key upload requires a token.
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/upload", "", map[string]any{}); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated key upload: got %d, want 401 (%s)", resp.Status, resp.Raw)
	}
}

// TestKeyUploadIdentityFieldNames checks that the identity key of a device is
// stored under either name a client may send it under. The specification calls
// the field device_keys - which is what the Rust crypto stack Element uses
// sends - while older clients write device_key. A server that reads only one of
// the two answers the other's upload with 200 and stores nothing, which leaves
// the device out of /keys/query and every signature it makes unresolvable.
func TestKeyUploadIdentityFieldNames(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	cases := []struct {
		name  string
		body  map[string]any
		curve string
	}{
		{
			name:  "the spec name device_keys",
			body:  map[string]any{"device_keys": fakeDeviceKey(alice.UserID, alice.DeviceID, "curve-spec")},
			curve: "curve-spec",
		},
		{
			name:  "the legacy name device_key",
			body:  map[string]any{"device_key": fakeDeviceKey(alice.UserID, alice.DeviceID, "curve-legacy")},
			curve: "curve-legacy",
		},
		{
			name: "both names at once, the spec one decides",
			body: map[string]any{
				"device_keys": fakeDeviceKey(alice.UserID, alice.DeviceID, "curve-both"),
				"device_key":  fakeDeviceKey(alice.UserID, alice.DeviceID, "curve-ignored"),
			},
			curve: "curve-both",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"device_id": alice.DeviceID}
			for field, value := range tc.body {
				body[field] = value
			}
			if resp := server.Do(http.MethodPost, "/_matrix/client/v3/keys/upload", alice.Token, body); resp.Status != http.StatusOK {
				t.Fatalf("uploading an identity key: got %d %s, want 200", resp.Status, resp.Raw)
			}
			query := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", alice.Token, map[string]any{
				"device_keys": map[string]any{alice.UserID: []string{}},
			}, http.StatusOK)
			devices, _ := query.Body["device_keys"].(map[string]any)
			mine, _ := devices[alice.UserID].(map[string]any)
			published, ok := mine[alice.DeviceID].(map[string]any)
			if !ok {
				t.Fatalf("%s published no identity key after an upload under %s: %s",
					alice.DeviceID, tc.name, query.Raw)
			}
			keys, _ := published["keys"].(map[string]any)
			if keys["curve25519"] != tc.curve {
				t.Errorf("the published curve25519 key is %v, want %s (%s)", keys["curve25519"], tc.curve, query.Raw)
			}
		})
	}
}

// sameKey reports whether two claim responses granted the same key id.
func sameKey(a, b map[string]any) bool {
	for _, byUser := range a {
		for _, byDevice := range byUser.(map[string]any) {
			for keyA := range byDevice.(map[string]any) {
				for _, otherUser := range b {
					for _, otherDevice := range otherUser.(map[string]any) {
						if _, clash := otherDevice.(map[string]any)[keyA]; clash {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// TestKeyFallbackKeys checks that a fallback key keeps being handed out: it is
// the last resort for starting a session, not a single-use prekey.
func TestKeyFallbackKeys(t *testing.T) {
	server := NewServer(t)
	bob := server.Register("bob", "rubberduck")

	server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/upload", bob.Token, map[string]any{
		"device_id":     bob.DeviceID,
		"device_key":    fakeDeviceKey(bob.UserID, bob.DeviceID, "curve-bob"),
		"one_time_keys": oneTimeKeys("B", 1),
		"fallback_keys": map[string]any{
			"signed_curve25519:FALLBACK": map[string]string{"signed_curve25519": "fallback-secret"},
		},
	}, http.StatusOK)

	claim := func() (map[string]any, map[string]int64) {
		t.Helper()
		resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/claim", bob.Token, map[string]any{
			"one_time_keys": map[string]any{bob.UserID: map[string]string{bob.DeviceID: "signed_curve25519"}},
		}, http.StatusOK)
		granted, _ := resp.Body["one_time_keys"].(map[string]any)
		return granted, keyCounts(t, server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/upload", bob.Token,
			map[string]any{"device_id": bob.DeviceID}, http.StatusOK))
	}

	if granted, _ := claim(); len(granted) == 0 {
		t.Fatal("the ordinary one-time key was not granted")
	}
	first, counts := claim()
	if len(first) == 0 {
		t.Fatal("the fallback key was not granted once the device ran out of prekeys")
	}
	second, _ := claim()
	if len(second) == 0 {
		t.Fatal("the fallback key was consumed; MSC3288 keeps it available")
	}
	if !sameKey(first, second) {
		t.Errorf("the fallback key changed between claims: %v then %v", first, second)
	}
	if counts["signed_curve25519"] != 0 {
		t.Errorf("a fallback key was counted as an ordinary prekey: %v", counts)
	}

	// A sync reports the unused fallback key so the client stops uploading one.
	synced := server.MustSync(bob, "")
	if len(synced.UnusedFallbackKeys) == 0 {
		t.Errorf("sync reported no unused fallback key types: %v", synced.UnusedFallbackKeys)
	}
}

// TestKeySyncCounts checks that every sync carries the one-time key counts, and
// that a key upload wakes other devices with a device_lists section.
func TestKeySyncCounts(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	stranger := server.Register("stranger", "nobodyknows")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Keyring"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	// A device that never uploaded keys still gets the counts, at zero.
	synced := server.MustSync(bob, "")
	if synced.DeviceOneTimeCount == nil || len(*synced.DeviceOneTimeCount) == 0 {
		t.Fatalf("sync carries no device_one_time_keys_count: %v", synced.DeviceOneTimeCount)
	}

	// Alice starts a second session and publishes keys for it.
	second := server.Login("alice", "wonderland")
	_, raw := server.Sync(bob, "")
	before := raw.String("next_batch")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/upload", second.Token, map[string]any{
		"device_id":     second.DeviceID,
		"device_key":    fakeDeviceKey(alice.UserID, second.DeviceID, "curve-alice-2"),
		"one_time_keys": oneTimeKeys("C", 3),
	}, http.StatusOK)

	// Bob shares a room with alice, so his next sync names her.
	after := server.MustSync(bob, "?since="+before)
	if after.DeviceLists == nil || !contains(after.DeviceLists.Changed, alice.UserID) {
		t.Errorf("bob's sync did not report alice's device change: %+v", after.DeviceLists)
	}

	// The stranger shares no room with alice, so the change is not for them.
	foreign := server.MustSync(stranger, "?since="+before)
	if foreign.DeviceLists != nil && contains(foreign.DeviceLists.Changed, alice.UserID) {
		t.Errorf("a stranger was told about alice's devices: %+v", foreign.DeviceLists)
	}

	// The same window is what GET /keys/changes answers.
	changes := server.MustDo(http.MethodGet, "/_matrix/client/v3/keys/changes?from="+before+"&to="+after.NextBatch,
		bob.Token, nil, http.StatusOK)
	server.MustFields(changes, "changed", "left")
	changed, _ := changes.Body["changed"].([]any)
	if !containsStringOf(changed, alice.UserID) {
		t.Errorf("keys/changes did not list alice: %s", changes.Raw)
	}

	// The counts of the uploading device follow the upload.
	uploader := server.MustSync(second, "?since="+after.NextBatch)
	if uploader.DeviceOneTimeCount == nil || (*uploader.DeviceOneTimeCount)["signed_curve25519"] != 3 {
		t.Errorf("sync reported the wrong key counts for the uploader: %v", uploader.DeviceOneTimeCount)
	}

	// Both token parameters are required.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/keys/changes?from=s1", bob.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("keys/changes without to: got %d, want 400 (%s)", resp.Status, resp.Raw)
	}
}

// TestKeyDeviceRemoval checks that key material goes away with its device, so
// other clients stop encrypting for a session that no longer exists.
func TestKeyDeviceRemoval(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	other := server.Register("carol", "sunflower")

	upload := func(session *Session, curve string) {
		server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/upload", session.Token, map[string]any{
			"device_id":     session.DeviceID,
			"device_key":    fakeDeviceKey(session.UserID, session.DeviceID, curve),
			"one_time_keys": oneTimeKeys("D", 2),
		}, http.StatusOK)
	}
	upload(alice, "curve-alice")
	second := server.Login("alice", "wonderland")
	upload(second, "curve-alice-2")

	query := func() map[string]any {
		resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", other.Token, map[string]any{
			"device_keys": map[string]any{alice.UserID: []string{}},
		}, http.StatusOK)
		devices, _ := resp.Body["device_keys"].(map[string]any)
		own, _ := devices[alice.UserID].(map[string]any)
		return own
	}
	if len(query()) != 2 {
		t.Fatalf("expected two devices with keys, got %v", query())
	}

	// A device filter narrows the query to the named devices.
	filtered := server.MustDo(http.MethodPost, "/_matrix/client/v3/keys/query", other.Token, map[string]any{
		"device_keys": map[string]any{alice.UserID: []string{second.DeviceID}},
	}, http.StatusOK)
	devices, _ := filtered.Body["device_keys"].(map[string]any)
	own, _ := devices[alice.UserID].(map[string]any)
	if len(own) != 1 || own[second.DeviceID] == nil {
		t.Errorf("the device filter did not narrow the query: %s", filtered.Raw)
	}

	server.MustDo(http.MethodDelete, "/_matrix/client/v3/devices/"+second.DeviceID, alice.Token, nil, http.StatusOK)
	remaining := query()
	if len(remaining) != 1 || remaining[alice.DeviceID] == nil {
		t.Errorf("the keys of the deleted device survived: %v", remaining)
	}

	// Deactivating the account takes the rest with it.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/account/deactivate", alice.Token, map[string]any{
		"auth": map[string]any{"type": "m.login.password", "user": "alice", "password": "wonderland"},
	}, http.StatusOK)
	if left := query(); len(left) != 0 {
		t.Errorf("a deactivated account still publishes keys: %v", left)
	}
}

// TestThirdPartyAndTurn covers the endpoints that ask for services Lattice does
// not run: an empty protocol list and an empty TURN configuration answer them
// without the 404 that makes clients show an error.
func TestThirdPartyAndTurn(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	protocols := server.MustDo(http.MethodGet, "/_matrix/client/v3/thirdparty/protocols", "", nil, http.StatusOK)
	if len(protocols.Body) != 0 {
		t.Errorf("the protocol list should be empty: %s", protocols.Raw)
	}

	cases := []struct {
		method string
		path   string
		token  string
		want   int
	}{
		{method: http.MethodGet, path: "/_matrix/client/v3/thirdparty/protocol/matrix", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/_matrix/client/v3/thirdparty/user", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/_matrix/client/v3/thirdparty/user/matrix", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/_matrix/client/v3/thirdparty/location", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/_matrix/client/v3/thirdparty/location/matrix", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/_matrix/client/v3/voip/turnServer", want: http.StatusUnauthorized},
		{method: http.MethodGet, path: "/_matrix/client/v3/voip/turnServer", token: alice.Token, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp := server.Do(tc.method, tc.path, tc.token, nil)
			if resp.Status != tc.want {
				t.Fatalf("got %d %s, want %d", resp.Status, resp.Raw, tc.want)
			}
		})
	}

	turn := server.MustDo(http.MethodGet, "/_matrix/client/v3/voip/turnServer", alice.Token, nil, http.StatusOK)
	uris, ok := turn.Body["uris"].([]any)
	if !ok || len(uris) != 0 {
		t.Errorf("the TURN document has no empty uris list: %s", turn.Raw)
	}
	if _, ok := turn.Body["ttl"]; !ok {
		t.Errorf("the TURN document has no ttl: %s", turn.Raw)
	}
}

// contains reports whether needle is in the list.
func contains(list []string, needle string) bool {
	for _, item := range list {
		if item == needle {
			return true
		}
	}
	return false
}

// containsStringOf reports whether needle is in a decoded JSON array.
func containsStringOf(list []any, needle string) bool {
	for _, item := range list {
		if item == needle {
			return true
		}
	}
	return false
}

// TestKeyDeviceListEcho checks that a device is not told about the device list
// changes it caused itself. Every key upload records such a change so the user's
// other devices learn about it, but echoed back to the device that made it the
// client fetches its own keys, and a client that re-uploads keys after fetching
// them queries keys over and over.
func TestKeyDeviceListEcho(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"invite": []string{bob.UserID}})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/join", bob.Token, map[string]any{}, http.StatusOK)

	// A second device of the same user, and the other user in the room, both exist
	// before the upload and both start from a token that already covers the change
	// which made the second device appear.
	watcher := server.Login("alice", "wonderland")
	for _, session := range []*Session{alice, watcher, bob} {
		started, _ := server.Sync(session, "?timeout=0")
		session.NextBatch = started.NextBatch
	}

	uploaded := server.Do(http.MethodPost, "/_matrix/client/v3/keys/upload", alice.Token, map[string]any{
		"device_id":     alice.DeviceID,
		"device_key":    fakeDeviceKey(alice.UserID, alice.DeviceID, "curve-alice"),
		"one_time_keys": oneTimeKeys("ECHO", 1),
	})
	if uploaded.Status != http.StatusOK {
		t.Fatalf("uploading keys: %d %s", uploaded.Status, uploaded.Raw)
	}

	own, _ := server.SyncSince(alice, 0)
	if own.DeviceLists != nil && contains(own.DeviceLists.Changed, alice.UserID) {
		t.Errorf("alice was told about her own key upload: %v", own.DeviceLists.Changed)
	}
	watchSync, _ := server.SyncSince(watcher, 0)
	if watchSync.DeviceLists == nil || !contains(watchSync.DeviceLists.Changed, alice.UserID) {
		t.Errorf("alice's other device was not told about the upload: %+v", watchSync.DeviceLists)
	}
	bobSync, _ := server.SyncSince(bob, 0)
	if bobSync.DeviceLists == nil || !contains(bobSync.DeviceLists.Changed, alice.UserID) {
		t.Errorf("the other user was not told about the upload: %+v", bobSync.DeviceLists)
	}
}
