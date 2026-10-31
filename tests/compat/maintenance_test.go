package compat

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// clock is a mutable clock the handlers read while the test moves it forward.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

// current implements the clock function the service expects.
func (c *clock) current() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// advance moves the clock forward.
func (c *clock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

// newClockServer starts a homeserver whose clock the test controls, so that
// retention windows can be crossed without waiting for them.
func newClockServer(t *testing.T, c *clock) *Server {
	t.Helper()
	db, err := persist.NewDatabase(filepath.Join(t.TempDir(), "lattice.db"))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tokens := auth.NewTokenService("test-secret", auth.WithTTL(100*24*time.Hour))
	svc, err := service.New(db, "test.server", tokens, service.WithClock(c.current))
	if err != nil {
		t.Fatalf("building service: %v", err)
	}
	app := api.New(svc, auth.NewStore(tokens, db), api.Config{
		ServerName:  "test.server",
		EnableMedia: true,
		AllowGuests: true,
	})
	httpServer := httptest.NewServer(app.Handler())
	t.Cleanup(httpServer.Close)
	return &Server{
		t:     t,
		http:  httpServer,
		db:    db,
		svc:   svc,
		app:   app,
		users: map[string]*Session{},
	}
}

// TestMaintenance covers the housekeeping pass that prunes expired to-device
// messages, remembered transaction ids and stale typing indicators, both through
// the admin endpoint and through the background loop.
func TestMaintenance(t *testing.T) {
	c := &clock{now: time.Now()}
	server := newClockServer(t, c)
	// The first account on a server is its administrator.
	root := server.Register("root", "throneroom")
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Attic"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	// Alice is typing; the indicator stays reported until it expires and
	// housekeeping clears the row.
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+alice.UserID,
		alice.Token, map[string]any{"typing": true, "timeout": 60000}, http.StatusOK)
	if len(typingEvents(server.joined(t, server.advance(bob), roomID))) == 0 {
		t.Fatalf("the typing indicator was not reported before housekeeping ran")
	}

	eventID := server.Send(alice, roomID, "keep-1", "worth remembering")
	if eventID == "" {
		t.Fatal("sending the message failed")
	}
	// A media id that was promised bytes it never received. POST /create advertises
	// a deadline for the upload, so the row is expected to go away afterwards.
	reserved := server.MustDo(http.MethodPost, "/_matrix/media/v3/create", alice.Token,
		map[string]any{"name": "never-came.txt", "type": "text/plain", "size": 4}, http.StatusOK)
	reservedID := mustMediaID(t, reserved.String("content_uri"))
	if resp := server.sendToDevice(alice, "m.room_key", "keep-td", map[string]map[string]any{
		bob.UserID: {"*": map[string]any{"key": "expired"}},
	}); resp.Status != http.StatusOK {
		t.Fatalf("sendToDevice: %d %s", resp.Status, resp.Raw)
	}

	// Nothing has expired yet, so a pass has nothing to remove.
	report := server.MustDo(http.MethodPost, "/_synapse/admin/v1/maintenance", root.Token, nil, http.StatusOK)
	if count := report.Int("to_device_removed"); count != 0 {
		t.Errorf("to_device_removed = %d before the retention window passed", count)
	}
	if count := report.Int("media_reservations_dropped"); count != 0 {
		t.Errorf("media_reservations_dropped = %d before the reservation window passed", count)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/media/v3/download/test.server/"+reservedID, alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("a reservation with no bytes should not be served: %d %s", resp.Status, resp.Raw)
	}

	// A day and an hour later every piece of ephemeral data is out of date.
	c.advance(25 * time.Hour)
	report = server.MustDo(http.MethodPost, "/_synapse/admin/v1/maintenance", root.Token, nil, http.StatusOK)
	for _, field := range []string{"to_device_removed", "transactions_removed", "typing_removed"} {
		if report.Int(field) < 1 {
			t.Errorf("%s = %d, want at least one removed row in %s", field, report.Int(field), report.Raw)
		}
	}
	if count := report.Int("media_reservations_dropped"); count < 1 {
		t.Errorf("media_reservations_dropped = %d, want the abandoned reservation gone in %s",
			count, report.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/media/v3/download/test.server/"+reservedID, alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("the abandoned reservation is still served: %d %s", resp.Status, resp.Raw)
	}

	// The to-device message is gone rather than delivered late.
	synced, _ := server.Sync(bob, "")
	if events := toDeviceOf(synced); len(events) != 0 {
		t.Errorf("an expired to_device message was still delivered: %v", events)
	}

	// Typing indicators that nobody refreshed stop being reported.
	if events := typingEvents(server.joined(t, synced, roomID)); len(events) != 0 {
		t.Errorf("a stale typing indicator is still reported: %v", events)
	}

	// The transaction id is forgotten, so replaying it creates a new event
	// instead of returning the old one.
	replay := server.MustDo(http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/keep-1", alice.Token,
		map[string]any{"msgtype": "m.text", "body": "worth remembering"}, http.StatusOK)
	if replay.String("event_id") == eventID {
		t.Errorf("an expired transaction id was still replayed: %s", replay.Raw)
	}

	// The background loop does the same work on a ticker.
	c.advance(25 * time.Hour)
	if resp := server.sendToDevice(alice, "m.dummy", "loop-td", map[string]map[string]any{
		bob.UserID: {"*": map[string]any{"note": "second round"}},
	}); resp.Status != http.StatusOK {
		t.Fatalf("sendToDevice: %d %s", resp.Status, resp.Raw)
	}
	c.advance(25 * time.Hour)
	done := server.svc.StartMaintenance(t.Context(), 20*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	var removed int64
	for time.Now().Before(deadline) {
		count, err := server.svc.DB().ToDeviceRepo.Count()
		if err != nil {
			t.Fatalf("counting to_device rows: %v", err)
		}
		removed = count
		if removed == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	done()
	if removed != 0 {
		t.Errorf("the maintenance loop left %d to_device rows behind", removed)
	}

	// Permissions.
	if resp := server.Do(http.MethodPost, "/_synapse/admin/v1/maintenance", alice.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("a non-admin running maintenance: got %d %s, want 403", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_synapse/admin/v1/maintenance", "", nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated maintenance: got %d, want 401", resp.Status)
	}
}

// typingEvents returns the typing ephemeral events of a joined room.
func typingEvents(room *matrix.JoinedRoom) []matrix.Event {
	if room == nil || room.Ephemeral == nil {
		return nil
	}
	var out []matrix.Event
	for _, event := range room.Ephemeral.Events {
		if event.Type == matrix.TypeTyping {
			out = append(out, event)
		}
	}
	return out
}
