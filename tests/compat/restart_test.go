package compat

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// TestAnAccessTokenOutlivesTheProcessThatMadeIt is about a restart. Access tokens are signed,
// and the sessions they belong to are rows in the database: if the signing secret is thrown away
// at shutdown, every token the server ever issued stops verifying while its session sits in the
// database looking perfectly current. The client is told M_UNKNOWN_TOKEN, and the way back in is
// a credential it may not have - an SSO login, a registration token, a password an operator set
// for a bot. Complement caught this as a device list test that failed after it stopped and
// started a server: the messages had arrived, and the client could not ask for them.
func TestAnAccessTokenOutlivesTheProcessThatMadeIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")

	first := NewServerWithDatabase(t, path, api.Config{})
	alice := first.Register("alice", "wonderland")
	roomID := first.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	first.Send(alice, roomID, "before", "written by the process that is about to stop")

	// A second server over the same file, with nothing else carried over: this is what starting
	// the binary again looks like from the client's side.
	second := NewServerWithDatabase(t, path, api.Config{})

	whoami := second.MustDo(http.MethodGet, "/_matrix/client/v3/account/whoami", alice.Token, nil, http.StatusOK)
	if got := whoami.String("user_id"); got != alice.UserID {
		t.Errorf("after the restart the token belongs to %q, want %q", got, alice.UserID)
	}

	// Not merely valid: usable, in the room the old process put it in.
	page := second.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=10",
		alice.Token, nil, http.StatusOK)
	events, _ := page.Body["chunk"].([]any)
	found := false
	for _, raw := range events {
		event, _ := raw.(map[string]any)
		if content, _ := event["content"].(map[string]any); content["body"] == "written by the process that is about to stop" {
			found = true
		}
	}
	if !found {
		t.Errorf("the history written before the restart is not readable with the token that wrote it")
	}

	// And the device list agrees with the process that died: the session is still there, current,
	// and named by the token the client already holds.
	listed := second.MustDo(http.MethodGet, "/_matrix/client/v3/devices", alice.Token, nil, http.StatusOK)
	devices, _ := listed.Body["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("the restarted server lists %d devices for %s, want the one already there", len(devices), alice.UserID)
	}
	device, _ := devices[0].(map[string]any)
	if device["device_id"] != alice.DeviceID {
		t.Errorf("the device listed is %v, want the token's own device %q", device["device_id"], alice.DeviceID)
	}
}

// TestAServerToldASecretUsesThatOne is the other half: the database holds a generated secret, and
// an operator who names one in the configuration gets theirs, so the token secret is a setting
// rather than an accident of the first start.
func TestAServerToldASecretUsesThatOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	first := NewServerWithDatabase(t, path, api.Config{})
	alice := first.Register("alice", "wonderland")
	second := NewServerWithDatabase(t, path, api.Config{})
	if second.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", alice.Token, nil).Status != http.StatusOK {
		t.Fatalf("the second server over the same database does not accept the token the first issued")
	}
	// The secret that ties them together is in the database, not in the memory of whichever
	// process happened to start first.
	other, err := persist.NewDatabase(path)
	if err != nil {
		t.Fatalf("opening the database again: %v", err)
	}
	defer func() { _ = other.Close() }()
	stored, err := other.MetaRepo.Get("token.secret")
	if err != nil || stored == "" {
		t.Errorf("no generated access token secret is stored in the database (err %v)", err)
	}
}

// TestTheEventStreamDoesNotStartOverAfterARestart is the other way a restart breaks a client. The
// sync token is a position in one counter shared by events, receipts, to-device messages and
// device list updates; if the counter is re-seeded from the events alone, everything the server
// wrote after its last event sits above the new starting point, and a client resuming from a token
// there is told, forever, that nothing has happened. A server that has been running for a week and
// is restarted for an upgrade shows this as every client needing a logout to catch up.
func TestTheEventStreamDoesNotStartOverAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	first := NewServerWithDatabase(t, path, api.Config{})
	alice := first.Register("alice", "wonderland")
	roomID := first.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	eventID := first.Send(alice, roomID, "one", "before the restart")
	// The last things the first process wrote are not events: a receipt and a to-device message
	// both take positions from the same counter, and they are what a naive seed misses.
	first.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/receipt/m.read/"+eventID,
		alice.Token, map[string]any{"ts": 1}, http.StatusOK)
	first.MustDo(http.MethodPut, "/_matrix/client/v3/sendToDevice/m.dummy/txn-1", alice.Token,
		map[string]any{"messages": map[string]any{alice.UserID: map[string]any{"*": map[string]any{"body": "x"}}}},
		http.StatusOK)
	token := first.MustSync(alice, "").NextBatch

	second := NewServerWithDatabase(t, path, api.Config{})
	second.Send(alice, roomID, "two", "after the restart")
	got := second.MustSync(alice, "?since="+token)
	if len(got.Rooms.Join[roomID].Timeline.Events) == 0 {
		t.Errorf("resuming from %q on the restarted server shows no new events in %s: the stream position started over", token, roomID)
	}
	if got.NextBatch == token {
		t.Errorf("the restarted server handed out %q again, the token the client already had", token)
	}
}
