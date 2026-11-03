package federation

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// uploadKeyMaterial publishes a device's identity key and a batch of prekeys, and returns the
// prekeys as they were sent so that a test can name what should come back.
func uploadKeyMaterial(t *testing.T, s *server, sess *session, deviceKeys matrix.Object, count int) matrix.Object {
	t.Helper()
	oneTime := matrix.Object{}
	for i := range count {
		id := fmt.Sprintf("signed_curve25519:%s%04d", sess.DeviceID, i)
		oneTime[id] = matrix.Object{"signed_curve25519": id + "-secret"}
	}
	got := s.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/keys/upload", sess.Token,
		matrix.Object{"device_keys": deviceKeys, "one_time_keys": oneTime})
	counts := asObject(t, got.Body["one_time_key_counts"])
	if documentInt(counts, "signed_curve25519") != int64(count) {
		t.Fatalf("%s on %s holds %v prekeys after uploading %d: %s", sess.UserID, s.name, counts, count, got.Raw)
	}
	return oneTime
}

// claimCounts reads how many prekeys a claim answer offers per user.
func claimUsers(t *testing.T, answer matrix.Object) map[string]int {
	out := map[string]int{}
	section, ok := answer["one_time_keys"].(map[string]any)
	if !ok {
		return out
	}
	for userID, devices := range section {
		count := 0
		for _, keys := range asObject(t, devices) {
			count += len(asObject(t, keys))
		}
		out[userID] = count
	}
	return out
}

// waitForDeviceListChange polls a client's sync until the named user appears among the device
// lists that changed, flushing the sending servers on the way because these tests drive the
// queue rather than wait for a timer. The token to resume from is returned, so a test can wait
// for several changes in a row without seeing the same one twice.
func waitForDeviceListChange(t *testing.T, s *server, sess *session, since, want string, senders ...*server) string {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; {
		for _, sender := range senders {
			flush(t, sender)
		}
		got := syncOnce(t, s, sess, since)
		if got.DeviceLists != nil {
			for _, userID := range got.DeviceLists.Changed {
				if userID == want {
					return got.NextBatch
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never told %s that the devices of %s changed (syncing from %q): %#v",
				s.name, sess.UserID, want, since, got.DeviceLists)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// loginAsNewDevice signs an account in again on a fresh device, which is what adding a laptop
// looks like to a homeserver.
func loginAsNewDevice(t *testing.T, s *server, sess *session, password string) *session {
	t.Helper()
	got := s.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/login", "", matrix.Object{
		"type":                        "m.login.password",
		"identifier":                  matrix.Object{"user": sess.UserID},
		"password":                    password,
		"initial_device_display_name": "second laptop",
	})
	second := &session{
		UserID:   got.stringField("user_id"),
		DeviceID: got.stringField("device_id"),
		Token:    got.stringField("access_token"),
	}
	if second.DeviceID == "" || second.DeviceID == sess.DeviceID {
		t.Fatalf("logging %s in again gave device %q, want a new one: %s", sess.UserID, second.DeviceID, got.Raw)
	}
	return second
}

// keyObjectMatches checks a served device key against what the device published, ignoring the
// unsigned block: a server labels devices for its clients, and the label is not part of what the
// device signed.
func keyObjectMatches(t *testing.T, want, got any) error {
	t.Helper()
	served := asObject(t, got)
	for _, field := range []string{"user_id", "device_id", "algorithms", "keys", "signatures"} {
		if !canonicalEqual(t, asObject(t, want)[field], served[field]) {
			return fmt.Errorf("%s is %v, want %v", field, served[field], asObject(t, want)[field])
		}
	}
	return nil
}

// canonicalEqual compares two key objects the way a homeserver has to: by their canonical
// form, because a device key is only as good as the bytes its owner signed.
func canonicalEqual(t *testing.T, want, got any) bool {
	t.Helper()
	left, err := matrix.CanonicalJSON(want)
	if err != nil {
		t.Fatalf("encoding the wanted key object: %v", err)
	}
	right, err := matrix.CanonicalJSON(got)
	if err != nil {
		t.Fatalf("encoding the received key object: %v", err)
	}
	return string(left) == string(right)
}

// TestAPeerQueriesAndClaimsTheKeysOfOurUsers is the serving half of cross-server encryption:
// another homeserver asks what our devices are and asks for a prekey to start a session with
// one of them.
func TestAPeerQueriesAndClaimsTheKeysOfOurUsers(t *testing.T) {
	t.Parallel()
	c := newCluster(t)

	alice := c.one().register("alice", "correct horse")
	uploaded := signedDeviceKeys(t, alice)
	prekeys := uploadKeyMaterial(t, c.one(), alice, uploaded, 3)

	answer, err := c.two().peer().Post(t.Context(), serverOne, "/_matrix/federation/v1/user/keys/query",
		matrix.Object{"device_keys": matrix.Object{alice.UserID: matrix.Array{}}})
	if err != nil {
		t.Fatalf("%s asking %s for %s's keys: %v", serverTwo, serverOne, alice.UserID, err)
	}
	devices := asObject(t, asObject(t, answer["device_keys"])[alice.UserID])
	if len(devices) != 1 {
		t.Fatalf("the answer holds %d devices of %s: %v", len(devices), alice.UserID, answer["device_keys"])
	}
	if err := keyObjectMatches(t, uploaded, devices[alice.DeviceID]); err != nil {
		t.Errorf("the served device keys are not the bytes the device signed: %v", err)
	}
	// The label of the device travels with it. It is not signed - it is this server's description
	// of a device it hosts - and a peer that caches the list gets it, so a client on the far side
	// can show the user what their own laptop is called.
	if name := asObject(t, asObject(t, devices[alice.DeviceID])["unsigned"])["device_display_name"]; name != "test device" {
		t.Errorf("the served device carries the label %v, want test device", name)
	}
	if documentInt(answer, "stream_id") <= 0 {
		t.Errorf("the answer carries no device list position: %v", answer["stream_id"])
	}
	// A user who is not ours is not answered for. The peer asked the wrong server, and the
	// honest answer is that this server has nothing to say about that name.
	other, err := c.two().peer().Post(t.Context(), serverOne, "/_matrix/federation/v1/user/keys/query",
		matrix.Object{"device_keys": matrix.Object{"@bob:" + serverTwo: matrix.Array{}}})
	if err != nil {
		t.Fatalf("%s asking %s about a user of its own: %v", serverTwo, serverOne, err)
	}
	if named := asObject(t, other["device_keys"]); len(named) != 0 {
		t.Errorf("%s answered for a user it does not hold: %v", serverOne, named)
	}

	claimed, err := c.two().peer().Post(t.Context(), serverOne, "/_matrix/federation/v1/user/keys/claim",
		matrix.Object{"one_time_keys": matrix.Object{alice.UserID: matrix.Object{alice.DeviceID: "signed_curve25519"}}})
	if err != nil {
		t.Fatalf("%s claiming a prekey of %s: %v", serverTwo, alice.UserID, err)
	}
	if got := claimUsers(t, claimed)[alice.UserID]; got != 1 {
		t.Fatalf("the claim offered %d prekeys, want 1: %v", got, claimed["one_time_keys"])
	}
	// The key handed over is one of the ones the device offered, and it is spent: a claim is
	// the act of taking a prekey out of circulation, and only the server holding it can do
	// that. A second claim for the same device must come back empty.
	offered := asObject(t, asObject(t, asObject(t, claimed["one_time_keys"])[alice.UserID])[alice.DeviceID])
	for keyID, key := range offered {
		if _, known := prekeys[keyID]; !known {
			t.Errorf("the claim offered %s, which the device never uploaded", keyID)
		}
		if !canonicalEqual(t, prekeys[keyID], key) {
			t.Errorf("the prekey %s came back changed: want %v, got %v", keyID, prekeys[keyID], key)
		}
	}
	// Every claim takes a different prekey, and the fourth one comes back empty: the keys are
	// spent by the handover, which only the server holding them can do. A server that answered a
	// claim from a copy would hand the same key out forever, and the two ends would open
	// sessions with a prekey its owner had already used up.
	seen := map[string]bool{}
	// Two left after the first claim, so two more come and the fourth ask is told there is
	// nothing.
	for claim := range 2 {
		next, err := c.two().peer().Post(t.Context(), serverOne, "/_matrix/federation/v1/user/keys/claim",
			matrix.Object{"one_time_keys": matrix.Object{alice.UserID: matrix.Object{alice.DeviceID: "signed_curve25519"}}})
		if err != nil {
			t.Fatalf("%s claiming again: %v", serverTwo, err)
		}
		offered := asObject(t, asObject(t, asObject(t, next["one_time_keys"])[alice.UserID])[alice.DeviceID])
		if len(offered) != 1 {
			t.Fatalf("claim %d offered %d prekeys, want 1: %v", claim+1, len(offered), next["one_time_keys"])
		}
		for keyID := range offered {
			if seen[keyID] {
				t.Errorf("claim %d offered %s twice", claim+1, keyID)
			}
			seen[keyID] = true
		}
	}
	if empty, err := c.two().peer().Post(t.Context(), serverOne, "/_matrix/federation/v1/user/keys/claim",
		matrix.Object{"one_time_keys": matrix.Object{alice.UserID: matrix.Object{alice.DeviceID: "signed_curve25519"}}}); err != nil {
		t.Fatalf("%s claiming the last prekey: %v", serverTwo, err)
	} else if got := claimUsers(t, empty)[alice.UserID]; got != 0 {
		t.Errorf("a claim with nothing left offered %d prekeys, want none: %v", got, empty["one_time_keys"])
	}

	// Three keys were offered and all three were taken: the counts a client reads back have to
	// agree with what its server handed to the peer, or a client keeps uploading prekeys to a
	// device that has none.
	counts := asObject(t, c.one().mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/keys/upload",
		alice.Token, matrix.Object{"device_id": alice.DeviceID}).Body["one_time_key_counts"])
	if documentInt(counts, "signed_curve25519") != 0 {
		t.Errorf("after three claims %s holds %v prekeys, want none", alice.UserID, counts)
	}
}

// TestAKeyQuestionAboutARemoteUserIsAskedOfTheServerThatHoldsThem is the asking half: a client
// of this server wants to encrypt to somebody who lives elsewhere, and this server has to go
// and ask - and get the answer as if the person lived next door.
func TestAKeyQuestionAboutARemoteUserIsAskedOfTheServerThatHoldsThem(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	one, two := c.one(), c.two()

	alice := one.register("alice", "correct horse")
	bob := two.register("bob", "rubberduck")
	roomID := one.createRoom(alice, map[string]any{"preset": "private_chat", "invite": []any{bob.UserID}})
	two.join(bob, roomID)

	uploaded := signedDeviceKeys(t, alice)
	prekeys := uploadKeyMaterial(t, one, alice, uploaded, 2)

	queried := two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/keys/query", bob.Token,
		matrix.Object{"device_keys": matrix.Object{alice.UserID: matrix.Array{}}})
	devices := asObject(t, asObject(t, queried.Body["device_keys"])[alice.UserID])
	if len(devices) != 1 {
		t.Fatalf("%s asked about %s and got %d devices: %s", two.name, alice.UserID, len(devices), queried.Raw)
	}
	// The key object arrives as the device signed it - the unsigned block apart, which is where
	// the name of the device lives and which is nobody's signature's business.
	if err := keyObjectMatches(t, uploaded, devices[alice.DeviceID]); err != nil {
		t.Errorf("the device keys of a remote user came back changed: %v", err)
	}
	if name := asObject(t, asObject(t, devices[alice.DeviceID])["unsigned"])["device_display_name"]; name != "test device" {
		t.Errorf("the remote device is labelled %v, want the name its server knows", name)
	}

	// A prekey is claimed from the server that holds it, so it stops being available there. The
	// second claim is the test: answering locally from a copy would happily hand the same key
	// out twice, and the client on the other end would open two sessions with one key.
	claim := func() response {
		return two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/keys/claim", bob.Token,
			matrix.Object{"one_time_keys": matrix.Object{alice.UserID: matrix.Object{alice.DeviceID: "signed_curve25519"}}})
	}
	first := claim()
	offered := asObject(t, asObject(t, asObject(t, first.Body["one_time_keys"])[alice.UserID])[alice.DeviceID])
	if len(offered) != 1 {
		t.Fatalf("the claim offered %d prekeys, want 1: %s", len(offered), first.Raw)
	}
	for keyID, key := range offered {
		if _, known := prekeys[keyID]; !known {
			t.Errorf("the claim offered %s, which %s never uploaded", keyID, alice.UserID)
		}
		if !canonicalEqual(t, prekeys[keyID], key) {
			t.Errorf("the prekey %s came back changed: want %v, got %v", keyID, prekeys[keyID], key)
		}
	}
	if again := claim(); len(asObject(t, again.Body["one_time_keys"])) != 1 {
		// One user still appears, because the answer lists what was asked for; what must not
		// appear is a second key for that device.
		if nested := asObject(t, asObject(t, again.Body["one_time_keys"])[alice.UserID]); len(nested) != 0 {
			t.Errorf("a second claim offered prekeys for %s: %s", alice.UserID, again.Raw)
		}
	}

	// The same question about a server that does not answer is a failure entry rather than a
	// broken request: a client encrypting for a room needs the keys it can get, and needs to be
	// told which people it did not get them for.
	c.disconnect(serverOne)
	ghost := two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/keys/query", bob.Token,
		matrix.Object{"device_keys": matrix.Object{"@carol:" + serverOne: matrix.Array{}}})
	if failures := asObject(t, ghost.Body["failures"]); len(failures) == 0 {
		t.Errorf("asking about a user on a server that does not answer reported no failure: %s", ghost.Raw)
	} else if _, named := failures[serverOne]; !named {
		t.Errorf("the failure entry does not name %s: %v", serverOne, failures)
	}
	c.restore(serverOne)
}

// TestADeviceListChangeTravelsToTheServerThatSharesARoom is the notification half of the
// arrangement: without it a client keeps encrypting to the devices it knew about the day it
// joined, and a user who added a third laptop is locked out of their own conversation.
func TestADeviceListChangeTravelsToTheServerThatSharesARoom(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	one, two := c.one(), c.two()

	alice := one.register("alice", "correct horse")
	bob := two.register("bob", "rubberduck")
	roomID := one.createRoom(alice, map[string]any{"preset": "private_chat", "invite": []any{bob.UserID}})
	two.join(bob, roomID)
	uploaded := signedDeviceKeys(t, alice)
	uploadKeyMaterial(t, one, alice, uploaded, 1)

	// Sync both clients to a known point, so what follows is a change and not the state of the
	// world at login.
	aliceSince := syncOnce(t, one, alice, "").NextBatch
	bobSince := syncOnce(t, two, bob, "").NextBatch

	// A device's human-readable name is part of its device list, and the specification names a
	// change to it as a reason to tell the other servers in the rooms this user is in.
	one.mustDo(http.StatusOK, http.MethodPut, "/_matrix/client/v3/devices/"+alice.DeviceID, alice.Token,
		matrix.Object{"display_name": "Alice's laptop"})

	waitForDeviceListChange(t, two, bob, bobSince, alice.UserID, one)
	// A second laptop is the case a client actually notices: the change came from a different
	// device, so the device reading its own sync is told about it rather than told about itself.
	second := loginAsNewDevice(t, one, alice, "correct horse")
	uploadKeyMaterial(t, one, second, signedDeviceKeys(t, second), 1)
	waitForDeviceListChange(t, one, alice, aliceSince, alice.UserID, one)

	// The update did not only notify. It carried the key object, so this server can answer the
	// same question with its peer out to lunch.
	c.disconnect(serverOne)
	answered := two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/keys/query", bob.Token,
		matrix.Object{"device_keys": matrix.Object{alice.UserID: matrix.Array{}}})
	devices := asObject(t, asObject(t, answered.Body["device_keys"])[alice.UserID])
	if len(devices) != 2 {
		t.Fatalf("with %s unreachable %s answered %d devices of %s, want the two it was told about: %s",
			serverOne, serverTwo, len(devices), alice.UserID, answered.Raw)
	}
	if err := keyObjectMatches(t, uploaded, devices[alice.DeviceID]); err != nil {
		t.Errorf("the cached device keys are not the bytes the device signed: %v", err)
	}
	c.restore(serverOne)
}

// TestAJoinNamesTheDeviceListsThePeerShouldExpect covers the trigger that is easy to miss: a
// device list update is also owed when a user joins a room that contains servers not already
// being told about that user. Nobody can ask about a device list they have never heard exists.
func TestAJoinNamesTheDeviceListsThePeerShouldExpect(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	one, two := c.one(), c.two()

	alice := one.register("alice", "correct horse")
	uploaded := signedDeviceKeys(t, alice)
	uploadKeyMaterial(t, one, alice, uploaded, 1)

	bob := two.register("bob", "rubberduck")
	roomID := one.createRoom(alice, map[string]any{"preset": "private_chat", "invite": []any{bob.UserID}})
	bobSince := syncOnce(t, two, bob, "").NextBatch

	// Bob's join reaches Alice's server, which now shares a room with a server it has never
	// sent Alice's device list to.
	two.join(bob, roomID)

	waitForDeviceListChange(t, two, bob, bobSince, alice.UserID, one)

	// And the copy that arrived is the whole device list, not just a hint that one exists: the
	// peer is asked for nothing further, as this check shows with the owner unreachable.
	c.disconnect(serverOne)
	answered := two.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/keys/query", bob.Token,
		matrix.Object{"device_keys": matrix.Object{alice.UserID: matrix.Array{}}})
	devices := asObject(t, asObject(t, answered.Body["device_keys"])[alice.UserID])
	if len(devices) != 1 {
		t.Fatalf("%s knows %d devices of %s after the join, want 1: %s",
			serverTwo, len(devices), alice.UserID, answered.Raw)
	}
	if err := keyObjectMatches(t, uploaded, devices[alice.DeviceID]); err != nil {
		t.Errorf("the device list that arrived with the join is not what the device published: %v", err)
	}
	c.restore(serverOne)
}
