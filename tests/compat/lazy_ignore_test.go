package compat

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestLazyLoadedMessages checks the state block of a lazy loaded /messages page: the membership
// events of the people whose events the page carries, and nothing else. A client paging through a
// room it is not looking at said it does not want the room's member list, and the member list is
// most of what a state block is made of.
func TestLazyLoadedMessages(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "mirror")
	carol := server.Register("carol", "hat")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	for _, guest := range []*Session{bob, carol} {
		server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/join", guest.Token, nil, http.StatusOK)
	}
	server.Send(bob, roomID, "txn-bob", "from bob")
	next := server.MustSync(alice, "?timeout=0").NextBatch

	page := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=3&from="+next+
			"&filter="+url.QueryEscape(`{"lazy_load_members": true}`),
		alice.Token, nil, http.StatusOK)
	events := chunkOf(string(page.Raw))
	if len(events) == 0 {
		t.Fatalf("the page carries nothing: %s", page.Raw)
	}
	// The state block is exactly the memberships the page needs: one per sender, and the room's
	// other state - which the client paging a room it is not looking at said it does not want -
	// is not there at all.
	spoken := map[string]bool{}
	for _, event := range events {
		spoken[event.Sender] = true
	}
	state := stateOf(string(page.Raw))
	if len(state) != len(spoken) {
		t.Errorf("a lazy loaded page carries %d state events for %d senders, want one each: %s", len(state), len(spoken), page.Raw)
	}
	for _, event := range state {
		if event.Type != matrix.TypeMember {
			t.Errorf("a lazy loaded state block carries %s, which is not what lazy loading asked to drop: %s", event.Type, page.Raw)
		}
		if event.StateKey == nil || !spoken[*event.StateKey] {
			t.Errorf("a lazy loaded state block carries a membership for somebody whose words this page does not carry: %s", page.Raw)
		}
	}
	// The same page without the request is the room's whole state, which is what the flag exists
	// to avoid: carol joined, said nothing, and is in it.
	plain := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=3&from="+next,
		alice.Token, nil, http.StatusOK)
	found := false
	for _, event := range stateOf(string(plain.Raw)) {
		if event.Type == matrix.TypeMember && event.StateKey != nil && *event.StateKey == carol.UserID {
			found = true
		}
	}
	if !found {
		t.Errorf("without lazy loading the page is expected to carry the room's state, carol's membership included: %s", plain.Raw)
	}
}

// TestLazyLoadedSync checks the same promise on a sync: a filter that asks for lazy loaded
// members gets the memberships of the people the timeline speaks for and the reader's own, and
// the room's other state arrives untouched.
func TestLazyLoadedSync(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "mirror")
	carol := server.Register("carol", "hat")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	for _, guest := range []*Session{bob, carol} {
		server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/join", guest.Token, nil, http.StatusOK)
	}
	server.Send(bob, roomID, "txn-bob", "from bob")

	filter := `{"room":{"timeline":{"limit":1},"state":{"lazy_load_members":true}}}`
	synced, resp := server.Sync(alice, "?timeout=0&filter="+url.QueryEscape(filter))
	room, ok := synced.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from the join section: %s", resp.Raw)
	}
	if len(room.Timeline.Events) != 1 || room.Timeline.Events[0].Sender != bob.UserID {
		t.Fatalf("the filter asked for one timeline event and got: %s", resp.Raw)
	}
	for _, event := range room.State.Events {
		if event.Type != matrix.TypeMember {
			continue
		}
		if event.StateKey != nil && *event.StateKey == carol.UserID {
			t.Errorf("a lazy loaded state block carries the membership of %s, who says nothing in this response: %s", carol.UserID, resp.Raw)
		}
	}
	// The room's other state is not what lazy loading is about: a client that cannot see the
	// room's join rules or power levels cannot show the room at all.
	kinds := map[string]bool{}
	for _, event := range room.State.Events {
		kinds[event.Type] = true
	}
	for _, want := range []string{matrix.TypeCreate, matrix.TypePowerLevels, matrix.TypeJoinRules} {
		if !kinds[want] {
			t.Errorf("the state block of a lazy loaded sync has no %s: %s", want, resp.Raw)
		}
	}
	// And the same sync with the flag off shows carol, which is the payload the flag bought back.
	plain := server.MustSync(alice, "?timeout=0&filter="+url.QueryEscape(`{"room":{"timeline":{"limit":1}}}`))
	shown := false
	for _, event := range plain.Rooms.Join[roomID].State.Events {
		if event.Type == matrix.TypeMember && event.StateKey != nil && *event.StateKey == carol.UserID {
			shown = true
		}
	}
	if !shown {
		t.Errorf("without lazy loading the sync is expected to carry the room's member state: %+v", plain.Rooms.Join[roomID].State)
	}
}

// TestIgnoredUsers checks MSC3023 on the server rather than the client: the words of somebody a
// user has ignored are not sent to them, an invite from them is not handed over at all, and their
// membership events still are, because a room a user can see has to be able to say who joined.
func TestIgnoredUsers(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "mirror")
	shared := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+shared+"/join", bob.Token, nil, http.StatusOK)

	server.MustDo(http.MethodPut, "/_matrix/client/v3/user/"+alice.UserID+"/account_data/"+matrix.TypeIgnoredUserList,
		alice.Token, map[string]any{"ignored_users": map[string]any{bob.UserID: map[string]any{}}}, http.StatusOK)

	server.Send(bob, shared, "txn-ignored", "you will not read this")
	other := server.CreateRoom(bob, map[string]any{"preset": "private_chat", "invite": []string{alice.UserID}})

	synced, resp := server.Sync(alice, "?timeout=0")
	if _, invited := synced.Rooms.Invite[other]; invited {
		t.Errorf("an invite from somebody on the ignore list was handed over: %s", resp.Raw)
	}
	for _, event := range synced.Rooms.Join[shared].Timeline.Events {
		if event.Sender == bob.UserID && event.Type != matrix.TypeMember {
			t.Errorf("the words of the ignored %s were sent to %s: %s", bob.UserID, alice.UserID, resp.Raw)
		}
	}
	joined := false
	for _, event := range synced.Rooms.Join[shared].Timeline.Events {
		if event.Type == matrix.TypeMember && event.StateKey != nil && *event.StateKey == bob.UserID {
			joined = true
		}
	}
	if !joined && !strings.Contains(string(resp.Raw), bob.UserID) {
		t.Errorf("the room cannot say that the ignored user is in it: %s", resp.Raw)
	}
	// Somebody not on the list is unaffected.
	carol := server.Register("carol", "hat")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+shared+"/join", carol.Token, nil, http.StatusOK)
	server.Send(carol, shared, "txn-carol", "still readable")
	synced, resp = server.Sync(alice, "?timeout=0&since="+synced.NextBatch)
	spoken := false
	for _, event := range synced.Rooms.Join[shared].Timeline.Events {
		if event.Sender == carol.UserID {
			spoken = true
		}
	}
	if !spoken {
		t.Errorf("carol's message did not reach %s: %s", alice.UserID, resp.Raw)
	}
}

// chunkOf and stateOf read the two arrays of a /messages answer. The wire form of an event is not
// the shape the server holds it in, so the test reads the three keys it asks about.
func chunkOf(raw string) []wireEvent {
	var page struct {
		Chunk []wireEvent `json:"chunk"`
	}
	_ = json.Unmarshal([]byte(raw), &page)
	return page.Chunk
}

func stateOf(raw string) []wireEvent {
	var page struct {
		State []wireEvent `json:"state"`
	}
	_ = json.Unmarshal([]byte(raw), &page)
	return page.State
}

type wireEvent struct {
	Type     string  `json:"type"`
	StateKey *string `json:"state_key"`
	Sender   string  `json:"sender"`
}

// events of the wire form, read for the three keys the assertions ask about.
