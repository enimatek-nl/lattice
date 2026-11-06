package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestDeviceListChangesCrossTheFederationLink checks that a device list change on one homeserver
// reaches the clients of another, and that a homeserver is only believed about its own users.
//
// Both halves matter, and they are opposite directions of the same channel.
//
// Without the first, end-to-end encryption quietly breaks across servers: a client is told which
// users' keys to re-read by its own server, and its own server only knows if the server holding
// those users says so. A key query can be answered by asking (see
// TestRemoteDeviceKeysComeFromTheirOwnServer), but nothing prompts the question, so a client keeps
// encrypting to devices it was never told about.
//
// The second is why the first is safe to accept at all. An announcement is a statement about a
// user, and the right to make it belongs to the server in that user's name. Believing a peer about
// somebody else's users would let any server push a stranger into every local client's key tracking
// — which is not a cosmetic problem, because a client that tracks a user starts trusting that user's
// keys.
func TestDeviceListChangesCrossTheFederationLink(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "dlalice", "pw-dlalice")
	bob, bobDevice := hs2.registerUserDevice(t, "dlbob", "pw-dlbob")
	bobUser := "@dlbob:" + hs2.name

	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, alice)
	roomID, _ := created["room_id"].(string)
	if roomID == "" {
		t.Fatalf("%s did not create a room: %v", hs1.name, created)
	}
	if status, answer := hs2.clientDo(t, http.MethodPost,
		"/_matrix/client/v3/join/"+roomID+"?server_name="+hs1.name, map[string]any{}, bob); status != http.StatusOK {
		t.Fatalf("%s could not join %s: %d %v", hs2.name, roomID, status, answer)
	}

	// Alice reads what she has, which includes bob's arrival. Everything asserted after this
	// point has to come from a device list change rather than from the join itself.
	_, token := deviceChanges(t, hs1, alice, "")

	// The honest case first, because it is the one a real client depends on: bob's device uploads
	// keys on its own server, and nothing alice does asks her server about it.
	keys := newTestKeys(t, bobUser, bobDevice, "A")
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/keys/upload",
		map[string]any{"device_keys": keys.DeviceKeys}, bob); status != http.StatusOK {
		t.Fatalf("uploading bob's keys on %s: %d %v", hs2.name, status, answer)
	}
	var seen []string
	if !waitFor(t, 15*time.Second, func() bool {
		seen, token = deviceChanges(t, hs1, alice, token)
		return namesUser(seen, bobUser)
	}) {
		t.Fatalf("alice never heard that bob's device list changed, although %s holds bob and shares %s with her: last sync named %v",
			hs2.name, roomID, seen)
	}

	// A peer announcing a user it does not carry is not believed. The transaction is still
	// accepted — the peer did nothing wrong by sending it, and refusing the whole transaction
	// would punish the other units in it — but the announcement does not become a fact here.
	_, token = deviceChanges(t, hs1, alice, token)
	target := "/_matrix/federation/v1/send/forged-device-list-update"
	status, answer := hs2.signedRequest(t, hs1, http.MethodPut, target,
		deviceListTransaction(t, hs2.name, map[string]any{
			"user_id":   "@mallory:hs3",
			"device_id": "EVILDEVICE",
			"stream_id": 1,
			"deleted":   false,
		}))
	if status != http.StatusOK {
		t.Fatalf("%s refused a transaction carrying an announcement: %d %s", hs1.name, status, answer)
	}
	seen, token = deviceChanges(t, hs1, alice, token)
	if namesUser(seen, "@mallory:hs3") {
		t.Errorf("%s let %s put a user nobody mentioned into a local client's key tracking: %v",
			hs1.name, hs2.name, seen)
	}

	// The same shape, naming a user the sender does carry, must be passed on. Without this the
	// check above would only be proving that announcements are ignored.
	target = "/_matrix/federation/v1/send/honest-device-list-update"
	status, answer = hs2.signedRequest(t, hs1, http.MethodPut, target,
		deviceListTransaction(t, hs2.name, map[string]any{
			"user_id":   bobUser,
			"device_id": bobDevice,
			"stream_id": 4242,
			"deleted":   false,
		}))
	if status != http.StatusOK {
		t.Fatalf("%s refused an announcement about its own user: %d %s", hs1.name, status, answer)
	}
	if !waitFor(t, 5*time.Second, func() bool {
		seen, token = deviceChanges(t, hs1, alice, token)
		return namesUser(seen, bobUser)
	}) {
		t.Errorf("an announcement %s was entitled to make about %s never reached alice: %v",
			hs2.name, bobUser, seen)
	}
}

// deviceChanges takes one sync and returns the users named in device_lists.changed, with the token
// to continue from.
func deviceChanges(t *testing.T, node *fedNode, token, since string) (users []string, next string) {
	t.Helper()
	target := "/_matrix/client/v3/sync?timeout=0"
	if since != "" {
		target += "&since=" + url.QueryEscape(since)
	}
	status, body := node.clientDo(t, http.MethodGet, target, nil, token)
	if status != http.StatusOK {
		t.Fatalf("%s refused a sync: %d %v", node.name, status, body)
	}
	next, _ = body["next_batch"].(string)
	if next == "" {
		t.Fatalf("the sync answer has no position to continue from: %v", body)
	}
	lists, _ := body["device_lists"].(map[string]any)
	changed, _ := lists["changed"].([]any)
	for _, entry := range changed {
		if userID, ok := entry.(string); ok {
			users = append(users, userID)
		}
	}
	return users, next
}

// namesUser reports whether a device list notice names a user.
func namesUser(users []string, want string) bool {
	for _, userID := range users {
		if userID == want {
			return true
		}
	}
	return false
}

// deviceListTransaction wraps one device list announcement in a federation transaction, as the
// sending server would.
func deviceListTransaction(t *testing.T, origin string, content map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"origin":           origin,
		"origin_server_ts": time.Now().UnixMilli(),
		"pdus":             []any{},
		"edus": []any{map[string]any{
			"edu_type": "m.device_list_update",
			"content":  content,
		}},
	})
	if err != nil {
		t.Fatalf("encoding the transaction: %v", err)
	}
	return body
}
