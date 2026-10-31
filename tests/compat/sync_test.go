package compat

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestInitialSync checks the shape of a first sync.
func TestInitialSync(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Sync room"})
	server.Send(alice, roomID, "txn-1", "first")

	synced, resp := server.Sync(alice, "?timeout=0")
	if synced.NextBatch == "" {
		t.Fatalf("no next_batch in %s", resp.Raw)
	}
	room, ok := synced.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from the join section: %s", resp.Raw)
	}
	if len(room.State.Events) == 0 {
		t.Errorf("the initial sync should carry the room state: %s", resp.Raw)
	}
	if len(room.Timeline.Events) == 0 {
		t.Fatalf("the timeline is empty: %s", resp.Raw)
	}
	if room.Timeline.PrevBatch == "" {
		t.Errorf("the timeline has no prev_batch token: %+v", room.Timeline)
	}
}

// TestIncrementalSync checks that a sync with since only returns new events.
func TestIncrementalSync(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})

	first, _ := server.Sync(alice, "?timeout=0")
	alice.NextBatch = first.NextBatch

	server.Send(alice, roomID, "txn-a", "alpha")
	server.Send(alice, roomID, "txn-b", "beta")

	second, resp := server.SyncSince(alice, 0)
	if second.NextBatch == alice.NextBatch {
		t.Fatalf("next_batch did not move: %s", resp.Raw)
	}
	room, ok := second.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from the incremental sync: %s", resp.Raw)
	}
	bodies := messageBodies(t, room.Timeline.Events)
	if len(bodies) != 2 || bodies[0] != "alpha" || bodies[1] != "beta" {
		t.Fatalf("timeline = %v, want [alpha beta]", bodies)
	}
	if len(room.State.Events) != 0 {
		t.Errorf("an incremental sync should not resend state: %v", room.State.Events)
	}
	alice.NextBatch = second.NextBatch

	quiet, _ := server.SyncSince(alice, 0)
	if room, ok := quiet.Rooms.Join[roomID]; ok && len(room.Timeline.Events) != 0 {
		t.Errorf("a sync with nothing new returned events: %v", room.Timeline.Events)
	}
	if quiet.NextBatch != alice.NextBatch {
		t.Errorf("next_batch moved without new events: %q then %q", alice.NextBatch, quiet.NextBatch)
	}
}

// TestLongPollSyncWakes checks that a long polling sync returns as soon as an
// event arrives.
func TestLongPollSyncWakes(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	bob.NextBatch = server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", bob.Token, nil, http.StatusOK).String("next_batch")

	done := make(chan matrix.SyncResponse, 1)
	go func() {
		synced, _ := server.SyncSince(bob, 8000)
		done <- synced
	}()

	// Give the long poll a moment to register before waking it up.
	time.Sleep(150 * time.Millisecond)
	start := time.Now()
	server.Send(alice, roomID, "txn-wake", "wake up")

	select {
	case synced := <-done:
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("the long poll took %v to notice the event", elapsed)
		}
		room, ok := synced.Rooms.Join[roomID]
		if !ok {
			t.Fatalf("the woken sync lost the room: %+v", synced.Rooms)
		}
		if bodies := messageBodies(t, room.Timeline.Events); len(bodies) != 1 || bodies[0] != "wake up" {
			t.Errorf("woken timeline = %v", bodies)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("the long polling sync never returned")
	}
}

// TestSyncLongPollTimeout checks that an incremental sync with nothing new holds
// the connection open for the whole timeout, and that timeout=0 opts out.
func TestSyncLongPollTimeout(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.Send(alice, roomID, "txn-old", "already seen")

	first, resp := server.Sync(alice, "?timeout=0")
	if first.NextBatch == "" {
		t.Fatalf("no next_batch in %s", resp.Raw)
	}
	alice.NextBatch = first.NextBatch

	// Nothing happened after the token, so the request has to stay open.
	start := time.Now()
	idle, resp := server.Sync(alice, "?since="+first.NextBatch+"&timeout=2500")
	elapsed := time.Since(start)
	if resp.Status != http.StatusOK {
		t.Fatalf("idle sync: %d %s", resp.Status, resp.Raw)
	}
	if elapsed < 2*time.Second {
		t.Errorf("an idle sync answered after %v; it should wait for the 2.5s timeout", elapsed)
	}
	if room := idle.Rooms.Join[roomID]; room != nil && len(room.Timeline.Events) != 0 {
		t.Errorf("an idle sync reported %d events", len(room.Timeline.Events))
	}
	if idle.NextBatch == "" {
		t.Errorf("the idle sync has no next_batch: %s", resp.Raw)
	}

	// timeout=0 answers immediately.
	start = time.Now()
	if _, resp := server.Sync(alice, "?since="+first.NextBatch+"&timeout=0"); resp.Status != http.StatusOK {
		t.Fatalf("timeout=0 sync: %d %s", resp.Status, resp.Raw)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("timeout=0 took %v; it should not wait", elapsed)
	}

	// Account data counts as new data and wakes a waiting sync.
	done := make(chan Response, 1)
	go func() {
		_, resp := server.SyncSince(alice, 8000)
		done <- resp
	}()
	time.Sleep(150 * time.Millisecond)
	start = time.Now()
	server.MustDo(http.MethodPut, "/_matrix/client/v3/user/"+alice.UserID+"/account_data/tags",
		alice.Token, map[string]any{"tags": map[string]any{"favorite": map[string]any{}}}, http.StatusOK)

	select {
	case resp := <-done:
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("account data took %v to wake the sync", elapsed)
		}
		var woken matrix.SyncResponse
		if err := json.Unmarshal(resp.Raw, &woken); err != nil {
			t.Fatalf("decoding the woken sync: %v (%s)", err, resp.Raw)
		}
		if woken.AccountData == nil || len(woken.AccountData.Events) == 0 {
			t.Errorf("the woken sync carries no account data: %s", resp.Raw)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("the long polling sync never returned after an account data change")
	}
}

// TestSyncInviteIdle checks that an outstanding invitation does not keep waking
// the long poll. An invitation is membership state, so it is reported on every
// sync until it is dealt with, but only one that arrived after the token is new
// data. A client that is handed an update every few milliseconds loops: it
// re-processes the room and, for an invited room, re-downloads the device keys
// of its members on every pass.
func TestSyncInviteIdle(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{
		"preset": "trusted_private_chat",
		"name":   "a DM",
		"invite": []string{bob.UserID},
	})

	first, resp := server.Sync(bob, "?timeout=0")
	invited, ok := first.Rooms.Invite[roomID]
	if !ok {
		t.Fatalf("bob sees no invitation: %s", resp.Raw)
	}
	// The stripped state has to carry m.room.create: a client that cannot find
	// the room_version in it assumes version 1 and warns about an upgrade.
	var version any
	for _, event := range invited.InviteState.Events {
		if event.Type == "m.room.create" {
			var content map[string]any
			if err := json.Unmarshal(event.Content, &content); err != nil {
				t.Fatalf("decoding the stripped m.room.create: %v", err)
			}
			version = content["room_version"]
		}
	}
	if version == nil || version == "" {
		t.Errorf("the stripped state of %s carries no room_version: %s", roomID, resp.Raw)
	}

	bob.NextBatch = first.NextBatch
	start := time.Now()
	idle, resp := server.SyncSince(bob, 2500)
	elapsed := time.Since(start)
	if resp.Status != http.StatusOK {
		t.Fatalf("idle sync: %d %s", resp.Status, resp.Raw)
	}
	if elapsed < 2*time.Second {
		t.Errorf("an idle sync with an outstanding invitation answered after %v; it should wait for the timeout", elapsed)
	}
	if _, ok := idle.Rooms.Invite[roomID]; !ok {
		t.Errorf("the invitation vanished from the idle sync: %s", resp.Raw)
	}

	// A second invitation is new data and does wake the sync.
	done := make(chan Response, 1)
	go func() {
		_, resp := server.SyncSince(bob, 8000)
		done <- resp
	}()
	time.Sleep(150 * time.Millisecond)
	start = time.Now()
	other := server.CreateRoom(alice, map[string]any{"name": "another DM", "invite": []string{bob.UserID}})

	select {
	case resp := <-done:
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("the invitation took %v to wake the sync", elapsed)
		}
		var woken matrix.SyncResponse
		if err := json.Unmarshal(resp.Raw, &woken); err != nil {
			t.Fatalf("decoding the woken sync: %v (%s)", err, resp.Raw)
		}
		if _, ok := woken.Rooms.Invite[other]; !ok {
			t.Errorf("the woken sync does not invite bob to %s: %s", other, resp.Raw)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("a new invitation never woke the sync")
	}
}

// TestSyncFilters covers inline and stored sync filters.
func TestSyncFilters(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	for i, text := range []string{"one", "two", "three", "four"} {
		server.Send(alice, roomID, text, text)
		_ = i
	}

	inline := `{"room":{"timeline":{"limit":2}}}`
	synced, resp := server.Sync(alice, "?filter="+url.QueryEscape(inline))
	room, ok := synced.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("room missing with an inline filter: %s", resp.Raw)
	}
	messages := 0
	for _, event := range room.Timeline.Events {
		if event.Type == matrix.TypeMessage {
			messages++
		}
	}
	if messages > 2 {
		t.Errorf("the inline timeline limit was ignored: %d messages in %s", messages, resp.Raw)
	}

	created := server.MustDo(http.MethodPost, "/_matrix/client/v3/user/"+alice.UserID+"/filter", alice.Token,
		json.RawMessage(`{"room":{"timeline":{"limit":1,"not_types":["m.room.member"]}}}`), http.StatusOK)
	filterID := created.String("filter_id")
	if filterID == "" {
		t.Fatalf("no filter_id in %s", created.Raw)
	}
	fetched := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/user/"+alice.UserID+"/filter/"+filterID, alice.Token, nil, http.StatusOK)
	if _, ok := fetched.Body["room"]; !ok {
		t.Errorf("the stored filter lost its room section: %s", fetched.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v2/user/"+alice.UserID+"/filter/"+filterID, alice.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("the v2 filter read failed: %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v3/user/"+alice.UserID+"/filter/"+filterID+"/room", alice.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("reading a single filter section failed: %d %s", resp.Status, resp.Raw)
	}

	filtered, resp := server.Sync(alice, "?filter="+filterID)
	room, ok = filtered.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("room missing with a stored filter: %s", resp.Raw)
	}
	for _, event := range room.Timeline.Events {
		if event.Type == matrix.TypeMember {
			t.Errorf("not_types was ignored: %s", resp.Raw)
		}
	}

	// Filters are private to their owner.
	bob := server.Register("bob", "rubberduck")
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v3/user/"+alice.UserID+"/filter/"+filterID, bob.Token, nil); resp.Status == http.StatusOK {
		t.Errorf("bob read alice's filter: %s", resp.Raw)
	}
}

// TestSyncRoomLeave covers the leave section of a sync.
func TestSyncRoomLeave(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	bob.NextBatch = server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", bob.Token, nil, http.StatusOK).String("next_batch")

	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/kick", alice.Token,
		map[string]any{"user_id": bob.UserID}, http.StatusOK)

	synced, resp := server.SyncSince(bob, 0)
	if _, ok := synced.Rooms.Leave[roomID]; !ok {
		t.Fatalf("the kicked room is not in the leave section: %s", resp.Raw)
	}
}

// messageBodies returns the body of every m.room.message event.
func messageBodies(t *testing.T, events []matrix.Event) []string {
	t.Helper()
	var bodies []string
	for _, event := range events {
		if event.Type != matrix.TypeMessage {
			continue
		}
		var content struct {
			Body string `json:"body"`
		}
		if err := matrix.DecodeContent(event.Content, &content); err != nil {
			t.Fatalf("decoding message content: %v", err)
		}
		bodies = append(bodies, content.Body)
	}
	return bodies
}
