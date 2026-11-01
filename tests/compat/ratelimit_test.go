package compat

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/api"
)

// limitedServer starts a homeserver whose buckets are small enough for a test
// to empty in a couple of requests. The refill rates are high, so a test can
// watch a bucket recover without sleeping for a second.
func limitedServer(t *testing.T, limits api.RateLimits) *Server {
	t.Helper()
	return NewServerWith(t, api.Config{AllowGuests: true, RateLimits: limits})
}

// TestRateLimitShape pins the error a limited request answers with: the 429
// status, the M_LIMIT_EXCEEDED code and a retry_after_ms the client can obey.
// The same bucket then refills, which is what makes the token a wait and not a
// punishment.
func TestRateLimitShape(t *testing.T) {
	server := limitedServer(t, api.RateLimits{WriteBurst: 2, WriteRate: 5})
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})

	path := "/_matrix/client/v3/rooms/" + roomID + "/send/m.room.message/"
	send := func(txn string) Response {
		return server.Do(http.MethodPut, path+txn, alice.Token,
			map[string]any{"msgtype": "m.text", "body": "hello"})
	}

	// createRoom took one write token, so one send fits and the next does not.
	if resp := send("shape-1"); resp.Status != http.StatusOK {
		t.Fatalf("the first send after creating the room = %d %s, want 200", resp.Status, resp.Raw)
	}
	resp := send("shape-2")
	if resp.Status != http.StatusTooManyRequests {
		t.Fatalf("an emptied bucket answered %d %s, want 429", resp.Status, resp.Raw)
	}
	if resp.ErrCode() != "M_LIMIT_EXCEEDED" {
		t.Errorf("errcode = %q, want M_LIMIT_EXCEEDED (%s)", resp.ErrCode(), resp.Raw)
	}
	if resp.String("error") == "" {
		t.Errorf("the 429 carries no human readable error: %s", resp.Raw)
	}
	wait := resp.Int("retry_after_ms")
	if wait <= 0 {
		t.Fatalf("retry_after_ms = %d, want a positive wait (%s)", wait, resp.Raw)
	}
	if wait > 1000 {
		t.Errorf("retry_after_ms = %d at a refill rate of 5/s, want a wait under a second", wait)
	}

	// Waiting out the stated pause makes room for the next event; the same
	// transaction id is used so the retry is the client's retry, not new traffic.
	time.Sleep(time.Duration(wait) * time.Millisecond)
	if again := send("shape-2"); again.Status != http.StatusOK {
		t.Fatalf("after retry_after_ms the send = %d %s, want 200", again.Status, again.Raw)
	}
}

// TestRateLimitPerDevice pins whose budget a request draws from: each device of
// a user gets its own, so a second session is never punished for the first one.
func TestRateLimitPerDevice(t *testing.T) {
	server := limitedServer(t, api.RateLimits{WriteBurst: 2, WriteRate: 0.001})
	alice := server.Register("alice", "wonderland")
	second := server.Login("alice", "wonderland")
	if second.DeviceID == alice.DeviceID {
		t.Fatalf("logging in reused device %s", second.DeviceID)
	}
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})

	path := "/_matrix/client/v3/rooms/" + roomID + "/send/m.room.message/"
	// The room creation plus one send empty the first device's bucket.
	if resp := server.Do(http.MethodPut, path+"device-a", alice.Token,
		map[string]any{"msgtype": "m.text", "body": "one"}); resp.Status != http.StatusOK {
		t.Fatalf("the first send = %d %s, want 200", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPut, path+"device-a-2", alice.Token,
		map[string]any{"msgtype": "m.text", "body": "two"}); resp.Status != http.StatusTooManyRequests {
		t.Fatalf("the second send on an emptied bucket = %d %s, want 429", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPut, path+"device-b", second.Token,
		map[string]any{"msgtype": "m.text", "body": "other device"}); resp.Status != http.StatusOK {
		t.Errorf("the other device was limited: %d %s, want 200", resp.Status, resp.Raw)
	}
}

// TestRateLimitAuthentication covers the brute-force budget: login, refresh and
// registration share one bucket per address, because an attempt has no device
// to key on yet.
func TestRateLimitAuthentication(t *testing.T) {
	server := limitedServer(t, api.RateLimits{AuthBurst: 3, AuthRate: 0.001})
	alice := server.Register("alice", "wonderland")

	// Registering took one token of the address bucket; the two guesses that
	// follow fit, and the third does not.
	for i := 0; i < 2; i++ {
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/login", "", map[string]any{
			"type": "m.login.password", "user": "alice", "password": "wrong",
		})
		if resp.Status != http.StatusForbidden {
			t.Fatalf("guessing a password %d: got %d %s, want 403", i, resp.Status, resp.Raw)
		}
	}
	resp := server.Do(http.MethodPost, "/_matrix/client/v3/login", "", map[string]any{
		"type": "m.login.password", "user": "alice", "password": "wonderland",
	})
	if resp.Status != http.StatusTooManyRequests {
		t.Fatalf("the fourth attempt = %d %s, want 429", resp.Status, resp.Raw)
	}
	if resp.ErrCode() != "M_LIMIT_EXCEEDED" || resp.Int("retry_after_ms") <= 0 {
		t.Errorf("the login 429 is missing its wait: %s", resp.Raw)
	}
	// The correct password does not buy its way past the wait: the point of the
	// budget is the number of attempts, not whether they succeed.
	if correct := server.Do(http.MethodPost, "/_matrix/client/v3/login", "", map[string]any{
		"type": "m.login.password", "user": "alice", "password": "wonderland",
	}); correct.Status != http.StatusTooManyRequests {
		t.Errorf("a correct password while limited = %d %s, want 429", correct.Status, correct.Raw)
	}
	// Registering draws from the same address bucket.
	if guest := server.Do(http.MethodPost, "/_matrix/client/v3/register", "",
		map[string]any{"kind": "guest"}); guest.Status != http.StatusTooManyRequests {
		t.Errorf("registering while the address bucket is empty = %d %s, want 429",
			guest.Status, guest.Raw)
	}
	// An endpoint that is not limited still works for an already authenticated
	// client: the budget guards work, not reading one's own state.
	if whoami := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", alice.Token, nil); whoami.Status != http.StatusOK {
		t.Errorf("whoami while the address bucket is empty = %d %s, want 200", whoami.Status, whoami.Raw)
	}
}

// TestRateLimitUploads covers the media upload bucket, which is separate so a
// client syncing a gallery cannot starve its own room sends.
func TestRateLimitUploads(t *testing.T) {
	server := limitedServer(t, api.RateLimits{UploadBurst: 1, UploadRate: 0.001, WriteBurst: 50, WriteRate: 50})
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})

	first := server.DoRaw(http.MethodPost, "/_matrix/media/v3/upload?filename=first.png",
		alice.Token, "image/png", testPNG(t, 8, 8))
	if first.Status != http.StatusOK {
		t.Fatalf("the first upload = %d %s, want 200", first.Status, first.Raw)
	}
	if mustMediaID(t, first.String("content_uri")) == "" {
		t.Fatal("the first upload returned no media id")
	}
	second := server.DoRaw(http.MethodPost, "/_matrix/media/v3/upload?filename=second.png",
		alice.Token, "image/png", testPNG(t, 8, 8))
	if second.Status != http.StatusTooManyRequests {
		t.Fatalf("the second upload = %d %s, want 429", second.Status, second.Raw)
	}
	if second.ErrCode() != "M_LIMIT_EXCEEDED" {
		t.Errorf("upload errcode = %q (%s)", second.ErrCode(), second.Raw)
	}
	// Sends are a different budget, so the exhausted upload bucket does not
	// reach into the room.
	if resp := server.Do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/upload-room", roomID),
		alice.Token, map[string]any{"msgtype": "m.text", "body": "still allowed"}); resp.Status != http.StatusOK {
		t.Errorf("a room send while uploads are limited = %d %s, want 200", resp.Status, resp.Raw)
	}
}

// TestRateLimitDisabled covers the switch that turns the whole thing off: a
// server operator or a test that must not be throttled sets Disabled.
func TestRateLimitDisabled(t *testing.T) {
	server := limitedServer(t, api.RateLimits{Disabled: true, WriteBurst: 1, WriteRate: 0.001})
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	path := "/_matrix/client/v3/rooms/" + roomID + "/send/m.room.message/"
	for i := 0; i < 30; i++ {
		resp := server.Do(http.MethodPut, fmt.Sprintf("%sdisabled-%d", path, i), alice.Token,
			map[string]any{"msgtype": "m.text", "body": fmt.Sprintf("message %d", i)})
		if resp.Status != http.StatusOK {
			t.Fatalf("send %d on an unlimited server = %d %s, want 200", i, resp.Status, resp.Raw)
		}
	}
}
