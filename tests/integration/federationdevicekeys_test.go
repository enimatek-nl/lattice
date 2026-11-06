package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRemoteDeviceKeysComeFromTheirOwnServer checks that a key query for a user who lives on
// another homeserver is answered by that homeserver rather than with silence.
//
// This is the point where end-to-end encryption stops being a local feature. A client that starts a
// session with a remote user needs that user's device keys, and this server has never seen them:
// they were uploaded by a device on the other server, and signed by that device. Answering "no keys"
// is not a neutral failure either — the client reads it as "this user has no devices", which is
// exactly the misunderstanding a key distribution system must not allow.
func TestRemoteDeviceKeysComeFromTheirOwnServer(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	alice := hs1.registerUser(t, "fedkeyalice", "pw-fedkeyalice")
	bob, bobID := hs2.registerUserDevice(t, "fedkeybob", "pw-fedkeybob")
	bobUser := "@fedkeybob:" + hs2.name

	keys := newTestKeys(t, bobUser, bobID, "A", "B")
	status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/keys/upload", map[string]any{
		"device_keys":   keys.DeviceKeys,
		"one_time_keys": keys.OneTimeKeys,
	}, bob)
	if status != http.StatusOK {
		t.Fatalf("uploading the device keys on %s: %d %v", hs2.name, status, answer)
	}

	// Alice asks her own server about a user she has never met, on a server she has never
	// spoken to. The answer has to come from that server.
	status, query := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/keys/query", map[string]any{
		"device_keys": map[string][]string{bobUser: {}},
	}, alice)
	if status != http.StatusOK {
		t.Fatalf("querying a remote user's keys: %d %v", status, query)
	}
	devices, _ := query["device_keys"].(map[string]any)
	ofBob, _ := devices[bobUser].(map[string]any)
	if len(ofBob) == 0 {
		t.Fatalf("a query for %s answered with no devices at all: %v — the remote server was never asked, or its answer was dropped",
			bobUser, query)
	}
	reported, ok := ofBob[bobID].(map[string]any)
	if !ok {
		t.Fatalf("the query did not report the device %s: %v", bobID, ofBob)
	}
	if reported["user_id"] != bobUser || reported["device_id"] != bobID {
		t.Errorf("the key document names %v/%v, wanting %s/%s",
			reported["user_id"], reported["device_id"], bobUser, bobID)
	}
	// The document has to arrive as it was uploaded, because the device signed it in that
	// shape. A server that tidied it on the way through would hand out an unverifiable key.
	uploaded := keys.DeviceKeys
	if string(eventJSON(t, reported["keys"])) != string(eventJSON(t, uploaded["keys"])) {
		t.Errorf("the key document arrived changed: %v against the uploaded %v", reported["keys"], uploaded["keys"])
	}

	// The other direction is the one that could leak: a server must not be willing to speak
	// about users who do not live on it. Asked directly, over federation, for a user belonging
	// to somebody else, it answers with nothing rather than with a lookup.
	status, payload := hs2.signedRequest(t, hs1, http.MethodPost, "/_matrix/federation/v1/user/keys/query",
		eventJSON(t, map[string]any{"device_keys": map[string][]string{bobUser: {}}}))
	if status != http.StatusOK {
		t.Fatalf("%s refused a device key query: %d %s", hs1.name, status, payload)
	}
	var elsewhere struct {
		DeviceKeys map[string]map[string]any `json:"device_keys"`
	}
	if err := json.Unmarshal(payload, &elsewhere); err != nil {
		t.Fatalf("decoding %s's answer: %s", hs1.name, payload)
	}
	if len(elsewhere.DeviceKeys[bobUser]) != 0 {
		t.Errorf("%s answered for %s, a user it does not carry: %v", hs1.name, bobUser, elsewhere.DeviceKeys[bobUser])
	}
}
