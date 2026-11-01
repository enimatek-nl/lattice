package compat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// sendToDevice performs PUT /sendToDevice/{eventType}/{txnId}.
func (s *Server) sendToDevice(session *Session, eventType, txnID string, messages map[string]map[string]any) Response {
	s.t.Helper()
	return s.Do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/sendToDevice/%s/%s", eventType, txnID), session.Token,
		map[string]any{"messages": messages})
}

// toDeviceOf collects the to_device events of a sync response.
func toDeviceOf(synced matrix.SyncResponse) []matrix.Event {
	if synced.ToDevice == nil {
		return nil
	}
	return synced.ToDevice.Events
}

// TestSendToDevice covers PUT /sendToDevice/{eventType}/{txnId} and delivery in
// the to_device section of a sync.
func TestSendToDevice(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	// A second device for Bob, created before anything is sent, so both
	// devices are expected to receive a message addressed to "*".
	bobOther := server.Login("bob", "rubberduck")
	if bobOther.DeviceID == bob.DeviceID {
		t.Fatalf("logging in again reused device %s", bob.DeviceID)
	}

	// Sync both devices up to the current position so only new messages show.
	server.advance(bob)
	server.advance(bobOther)

	resp := server.sendToDevice(alice, "m.room_key", "td-1", map[string]map[string]any{
		bob.UserID: {"*": map[string]any{"key": "session-1", "algorithm": "m.megolm.v1.aes-sha2"}},
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("sendToDevice: got %d %s, want 200", resp.Status, resp.Raw)
	}
	if len(resp.Body) != 0 {
		t.Errorf("sendToDevice returned a body: %s", resp.Raw)
	}

	// Both devices of Bob receive it, addressed by the sender.
	for _, device := range []*Session{bob, bobOther} {
		events := toDeviceOf(server.advance(device))
		if len(events) != 1 {
			t.Fatalf("device %s received %d to_device events, want one", device.DeviceID, len(events))
		}
		event := events[0]
		if event.Type != "m.room_key" || event.Sender != alice.UserID {
			t.Errorf("to_device event = type %s sender %s", event.Type, event.Sender)
		}
		var content map[string]any
		if err := json.Unmarshal(event.Content, &content); err != nil {
			t.Fatalf("decoding the to_device content: %v", err)
		}
		if content["key"] != "session-1" || content["algorithm"] != "m.megolm.v1.aes-sha2" {
			t.Errorf("to_device content = %v", content)
		}
		if event.OriginServerTS == 0 {
			t.Errorf("the to_device event has no origin_server_ts")
		}

		// The next sync from the new token delivers nothing again.
		if events := toDeviceOf(server.advance(device)); len(events) != 0 {
			t.Errorf("device %s received the same message twice: %v", device.DeviceID, events)
		}
	}

	// Retrying the same transaction id does not deliver the message again.
	if resp := server.sendToDevice(alice, "m.room_key", "td-1", map[string]map[string]any{
		bob.UserID: {"*": map[string]any{"key": "session-1"}},
	}); resp.Status != http.StatusOK {
		t.Fatalf("replaying a transaction: got %d %s, want 200", resp.Status, resp.Raw)
	}
	for _, device := range []*Session{bob, bobOther} {
		if events := toDeviceOf(server.advance(device)); len(events) != 0 {
			t.Errorf("the retried transaction reached device %s again: %v", device.DeviceID, events)
		}
	}

	// A new transaction delivers again, and addressing a device by id reaches
	// only that device.
	if resp := server.sendToDevice(alice, "m.dummy", "td-2", map[string]map[string]any{
		bob.UserID: {bobOther.DeviceID: map[string]any{"note": "only the other device"}},
	}); resp.Status != http.StatusOK {
		t.Fatalf("addressed sendToDevice: got %d %s, want 200", resp.Status, resp.Raw)
	}
	addressed := toDeviceOf(server.advance(bobOther))
	if len(addressed) != 1 || addressed[0].Type != "m.dummy" {
		t.Errorf("the addressed device received %v", addressed)
	}
	if events := toDeviceOf(server.advance(bob)); len(events) != 0 {
		t.Errorf("a message for one device leaked to another: %v", events)
	}

	// Unknown and remote recipients are accepted but ignored.
	for name, target := range map[string]string{
		"unknown":    "@nobody:test.server",
		"remote":     "@bob:elsewhere.example",
		"empty body": "",
	} {
		var messages map[string]map[string]any
		if target != "" {
			messages = map[string]map[string]any{target: {"*": map[string]any{"key": "x"}}}
		}
		resp := server.sendToDevice(alice, "m.room_key", "td-"+name, messages)
		if resp.Status != http.StatusOK {
			t.Errorf("%s recipient: got %d %s, want 200", name, resp.Status, resp.Raw)
		}
	}
	if events := toDeviceOf(server.advance(bob)); len(events) != 0 {
		t.Errorf("an ignored recipient still delivered to bob: %v", events)
	}

	// Room events do not belong on the to_device path.
	if resp := server.sendToDevice(alice, "m.room.encrypted", "td-3", map[string]map[string]any{
		bob.UserID: {"*": map[string]any{"ciphertext": "x"}},
	}); resp.Status != http.StatusBadRequest {
		t.Errorf("a room event type: got %d %s, want 400", resp.Status, resp.Raw)
	}

	// The endpoint requires a device of the caller.
	if resp := server.Do(http.MethodPut, "/_matrix/client/v3/sendToDevice/m.dummy/td-anon", "",
		map[string]any{"messages": map[string]map[string]any{bob.UserID: {"*": map[string]any{}}}}); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated sendToDevice: got %d %s, want 401", resp.Status, resp.Raw)
	}

	// Bob sends back to Alice: delivery works in both directions and the
	// sender never receives their own message.
	if resp := server.sendToDevice(bob, "m.room_key", "td-back", map[string]map[string]any{
		alice.UserID:              {"*": map[string]any{"key": "from-bob"}},
		alice.UserID + "-ignored": {"*": map[string]any{"key": "nope"}},
	}); resp.Status != http.StatusOK {
		t.Fatalf("replying to alice: got %d %s, want 200", resp.Status, resp.Raw)
	}
	back := toDeviceOf(server.advance(alice))
	if len(back) != 1 || back[0].Sender != bob.UserID {
		t.Errorf("alice received %v", back)
	}
	if events := toDeviceOf(server.advance(bob)); len(events) != 0 {
		t.Errorf("the sender received its own message: %v", events)
	}
}

// TestSyncRoomSummary covers the m.heroes, m.joined_member_count and
// m.invited_member_count fields of a joined room in a sync.
func TestSyncRoomSummary(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Small room"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	room := server.joined(t, server.advance(alice), roomID)
	if room.JoinedMemberCount == nil || *room.JoinedMemberCount != 2 {
		t.Errorf("joined_member_count = %v", room.JoinedMemberCount)
	}
	if room.InvitedMemberCount == nil || *room.InvitedMemberCount != 0 {
		t.Errorf("invited_member_count = %v", room.InvitedMemberCount)
	}
	if len(room.Heroes) != 1 || room.Heroes[0] != bob.UserID {
		t.Errorf("m.heroes = %v, want only %s", room.Heroes, bob.UserID)
	}

	// The other member sees themselves excluded from the heroes too.
	other := server.joined(t, server.advance(bob), roomID)
	if len(other.Heroes) != 1 || other.Heroes[0] != alice.UserID {
		t.Errorf("bob's m.heroes = %v", other.Heroes)
	}

	// An invite raises the invited count for the room's members.
	carol := server.Register("carol", "kettle")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": carol.UserID}, http.StatusOK)
	invited := server.joined(t, server.advance(alice), roomID)
	if invited.InvitedMemberCount == nil || *invited.InvitedMemberCount != 1 {
		t.Errorf("invited_member_count after the invite = %v", invited.InvitedMemberCount)
	}

	// A large room names at most five heroes and never the requester.
	witnesses := make([]*Session, 0, 6)
	for _, localpart := range []string{"w1", "w2", "w3", "w4", "w5", "w6"} {
		witness := server.Register(localpart, "torch")
		server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, witness.Token, nil, http.StatusOK)
		witnesses = append(witnesses, witness)
	}
	crowd := server.joined(t, server.advance(alice), roomID)
	if len(crowd.Heroes) != 5 {
		t.Errorf("m.heroes = %v, want five names", crowd.Heroes)
	}
	for _, id := range crowd.Heroes {
		if id == alice.UserID {
			t.Errorf("the requester is one of her own heroes: %v", crowd.Heroes)
		}
	}
	if crowd.JoinedMemberCount == nil || *crowd.JoinedMemberCount != int64(2+len(witnesses)) {
		t.Errorf("joined_member_count = %v", crowd.JoinedMemberCount)
	}
}

// TestHeroesOrderByActivity pins how the room summaries choose their heroes. The
// specification leaves the choice to the server and only caps the number, so
// Lattice names the members that were most recently active in the room: that is
// what a room list wants to show. Sync and the room summary endpoint answer with
// the same list.
func TestHeroesOrderByActivity(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})

	witnesses := map[string]*Session{}
	for _, localpart := range []string{"w1", "w2", "w3", "w4", "w5", "w6"} {
		witness := server.Register(localpart, "torch")
		server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, witness.Token, nil, http.StatusOK)
		witnesses[localpart] = witness
	}
	// Three of them speak up, in this order, so the last one is the freshest.
	server.Send(witnesses["w4"], roomID, "hero-said-4", "w4 spoke")
	server.Send(witnesses["w5"], roomID, "hero-said-5", "w5 spoke")
	server.Send(witnesses["w6"], roomID, "hero-said-6", "w6 spoke")

	room := server.joined(t, server.advance(alice), roomID)
	want := []string{"w6", "w5", "w4", "w3", "w2"}
	got := make([]string, 0, len(room.Heroes))
	for _, id := range room.Heroes {
		got = append(got, matrix.LocalpartOf(id))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("m.heroes = %v, want %v (most recently active first)", got, want)
	}
	for _, id := range room.Heroes {
		if id == alice.UserID {
			t.Errorf("the requester is one of her own heroes: %v", room.Heroes)
		}
	}

	summary := server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/summary", alice.Token,
		nil, http.StatusOK)
	rawHeroes, _ := summary.Body["m.heroes"].([]any)
	if len(rawHeroes) != len(want) {
		t.Fatalf("m.heroes = %s", summary.Raw)
	}
	for i, raw := range rawHeroes {
		id, _ := raw.(string)
		if matrix.LocalpartOf(id) != want[i] {
			t.Errorf("summary m.heroes[%d] = %v, want %s (%s)", i, id, want[i], summary.Raw)
		}
	}
}
