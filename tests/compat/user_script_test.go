package compat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// advance syncs a session forward and returns the decoded response, remembering
// the token for the next step.
func (s *Server) advance(session *Session) matrix.SyncResponse {
	s.t.Helper()
	synced := s.MustSync(session, fmt.Sprintf("?since=%s&timeout=0", session.NextBatch))
	if synced.NextBatch == "" {
		s.t.Fatalf("sync returned no next_batch")
	}
	session.NextBatch = synced.NextBatch
	return synced
}

// joined returns the joined room delta of a sync response, failing when the room
// is absent.
func (s *Server) joined(t *testing.T, synced matrix.SyncResponse, roomID string) *matrix.JoinedRoom {
	t.Helper()
	room, ok := synced.Rooms.Join[roomID]
	if !ok || room == nil {
		t.Fatalf("room %s is missing from the sync: rooms=%v", roomID, synced.Rooms.Join)
	}
	return room
}

// timelineBodies returns the body of every message event of a timeline.
func timelineBodies(room *matrix.JoinedRoom) []string {
	var out []string
	for _, event := range room.Timeline.Events {
		if event.Type != matrix.TypeMessage {
			continue
		}
		var content struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(event.Content, &content); err != nil {
			continue
		}
		if content.Body != "" {
			out = append(out, content.Body)
		}
	}
	return out
}

// TestUserScript walks a whole client session: register, profile, room creation,
// invite, join, messaging, notifications, receipts, read markers, reactions,
// threads, media, presence, typing, history, leaving and logging out. Every
// step only uses the tokens and event ids the previous step produced, which is
// how a real client drives the API.
func TestUserScript(t *testing.T) {
	server := NewServer(t)

	// 1. Two accounts and a device each.
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	for _, session := range []*Session{alice, bob} {
		who := server.MustDo(http.MethodGet, "/_matrix/client/v3/account/whoami", session.Token, nil, http.StatusOK)
		if who.String("user_id") != session.UserID {
			t.Errorf("whoami = %s", who.Raw)
		}
	}
	if alice.UserID == bob.UserID {
		t.Fatalf("both accounts share %s", alice.UserID)
	}

	// 2. Profiles, so mentions and member events carry display names.
	server.MustDo(http.MethodPut, "/_matrix/client/v3/profile/"+bob.UserID+"/displayname", bob.Token,
		map[string]any{"displayname": "Bob"}, http.StatusOK)

	// 3. A named room with a topic and an alias.
	roomID := server.CreateRoom(alice, map[string]any{
		"preset":          "private_chat",
		"name":            "Wonderland",
		"topic":           "down the rabbit hole",
		"room_alias_name": "wonderland",
	})
	if roomID == "" {
		t.Fatal("createRoom returned no room id")
	}
	resolved := server.MustDo(http.MethodGet, "/_matrix/client/v3/directory/room/%23wonderland:test.server",
		"", nil, http.StatusOK)
	if resolved.String("room_id") != roomID {
		t.Errorf("the alias resolves to %s, want %s", resolved.Raw, roomID)
	}
	state := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state", alice.Token, nil, http.StatusOK)
	if len(state.Array) == 0 {
		t.Errorf("the room has no state: %s", state.Raw)
	}

	// 4. Alice invites Bob and he accepts by alias.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, http.StatusOK)
	bobSynced := server.advance(bob)
	if invite, ok := bobSynced.Rooms.Invite[roomID]; !ok {
		t.Errorf("bob never saw the invite: %v", bobSynced.Rooms.Invite)
	} else if len(invite.InviteState.Events) == 0 {
		t.Errorf("the invite carries no invite state")
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/%23wonderland:test.server", bob.Token, nil, http.StatusOK)
	joined := server.MustDo(http.MethodGet, "/_matrix/client/v3/joined_rooms", bob.Token, nil, http.StatusOK)
	if rooms, _ := joined.Body["joined_rooms"].([]any); len(rooms) != 1 || rooms[0] != roomID {
		t.Errorf("bob's joined rooms = %s", joined.Raw)
	}

	// 5. Both members see the room state on their first sync.
	aliceSynced := server.advance(alice)
	aliceRoom := server.joined(t, aliceSynced, roomID)
	if len(aliceRoom.State.Events) == 0 {
		t.Errorf("alice's sync carries no state for the room she created")
	}
	bobSynced = server.advance(bob)
	bobRoom := server.joined(t, bobSynced, roomID)
	if len(bobRoom.State.Events) == 0 {
		t.Errorf("bob's sync carries no state for the room he joined")
	}
	if bobRoom.Timeline.PrevBatch == "" {
		t.Errorf("bob's timeline has no prev_batch")
	}
	summary := server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/summary", alice.Token,
		nil, http.StatusOK)
	if count, _ := summary.Body["m.joined_member_count"].(float64); count != 2 {
		t.Errorf("room summary after the join = %s", summary.Raw)
	}
	// 6. A conversation. Alice speaks first, Bob answers in a thread.
	greeting := server.Send(alice, roomID, "chat-1", "hello")
	server.Send(bob, roomID, "chat-2", "hi alice")
	question := server.Send(alice, roomID, "chat-3", "anyone seen the white rabbit?")
	server.sendTyped(bob, roomID, "chat-4", matrix.TypeMessage, map[string]any{
		"msgtype": "m.text", "body": "not today",
		"m.relates_to": map[string]any{
			"rel_type": "m.thread", "event_id": question, "is_falling_back": true,
			"m.in_reply_to": map[string]any{"event_id": question},
		},
	})

	// 7. A mention highlights Bob, and his sync reports it.
	mention := server.Send(alice, roomID, "chat-5", "bob, are you there?")
	bobSynced = server.advance(bob)
	bobRoom = server.joined(t, bobSynced, roomID)
	// Every message from Alice notifies Bob, the mention also highlights him.
	if bobRoom.UnreadNotifications.NotificationCount != 3 {
		t.Errorf("bob has %d notifications, want 3", bobRoom.UnreadNotifications.NotificationCount)
	}
	if bobRoom.UnreadNotifications.HighlightCount != 1 {
		t.Errorf("the mention produced %d highlights", bobRoom.UnreadNotifications.HighlightCount)
	}
	notifications := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications", bob.Token, nil, http.StatusOK)
	notifList, _ := notifications.Body["notifications"].([]any)
	if len(notifList) != 3 {
		t.Fatalf("bob has %d notifications, want three: %s", len(notifList), notifications.Raw)
	}
	var highlighted []string
	for _, raw := range notifList {
		entry, _ := raw.(map[string]any)
		event, _ := entry["event"].(map[string]any)
		id, _ := event["event_id"].(string)
		if actionsOf(entry["actions"]).hasTweak("highlight") {
			highlighted = append(highlighted, id)
		}
	}
	if len(highlighted) != 1 || highlighted[0] != mention {
		t.Errorf("highlighted notifications = %v, want only %s", highlighted, mention)
	}

	// 8. Bob reacts to the greeting and marks the room read.
	reaction := server.sendTyped(bob, roomID, "react-1", "m.reaction", map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": greeting, "key": "👍"},
	})
	relations := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+greeting+"?rel_type=m.annotation", alice.Token,
		nil, http.StatusOK)
	if got := eventsOf(relations.Body["chunk"]); len(got) != 1 || got[0].ID != reaction {
		t.Errorf("the reaction is not listed: %s", relations.Raw)
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", bob.Token,
		map[string]any{"m.fully_read": mention, "m.read": mention}, http.StatusOK)
	markers := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/user/"+bob.UserID+"/rooms/"+roomID+"/account_data/m.fully_read", bob.Token, nil,
		http.StatusOK)
	if markers.String("event_id") != mention {
		t.Errorf("the read marker is not stored as account data: %s", markers.Raw)
	}
	bobSynced = server.advance(bob)
	bobRoom = server.joined(t, bobSynced, roomID)
	if bobRoom.UnreadNotifications.NotificationCount != 0 {
		t.Errorf("marking the room read left %d notifications", bobRoom.UnreadNotifications.NotificationCount)
	}
	if bobRoom.Ephemeral == nil || eventOfType(t, bobRoom.Ephemeral.Events, matrix.TypeReceipt) == nil {
		t.Errorf("the receipt never reached bob's sync")
	}

	// 9. Alice sees the reaction and the thread in her timeline.
	aliceSynced = server.advance(alice)
	aliceRoom = server.joined(t, aliceSynced, roomID)
	var annotated bool
	for _, event := range aliceRoom.Timeline.Events {
		if event.ID == question {
			summary, _ := event.Unsigned["m.thread"].(map[string]any)
			if summary == nil {
				t.Errorf("the thread root arrived without a summary")
				continue
			}
			if num, _ := summary["num_events"].(float64); num != 2 {
				t.Errorf("thread summary = %v", summary)
			}
			annotated = true
		}
	}
	if !annotated {
		t.Errorf("the thread root is missing from alice's timeline: %v", timelineBodies(aliceRoom))
	}
	threads := server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/threads", alice.Token,
		nil, http.StatusOK)
	if chunk, _ := threads.Body["chunk"].([]any); len(chunk) != 1 {
		t.Errorf("the room has no thread: %s", threads.Raw)
	}

	// 10. Media: Bob uploads a picture, shares it, Alice downloads it.
	uploaded := server.DoRaw(http.MethodPost, "/_matrix/media/v3/upload?filename=rabbit.png", bob.Token,
		"image/png", testPNG(t, 20, 10))
	if uploaded.Status != http.StatusOK {
		t.Fatalf("the upload failed: %d %s", uploaded.Status, uploaded.Raw)
	}
	mediaURI := uploaded.String("content_uri")
	mediaID := mustMediaID(t, mediaURI)
	server.sendTyped(bob, roomID, "media-1", matrix.TypeMessage, map[string]any{
		"msgtype": "m.image", "body": "rabbit.png", "url": mediaURI,
		"info": map[string]any{"mimetype": "image/png", "w": 1, "h": 1},
	})
	picture := server.MustDo(http.MethodGet, "/_matrix/client/v1/media/download/test.server/"+mediaID+"/rabbit.png",
		alice.Token, nil, http.StatusOK)
	if bounds := pngBounds(t, picture.Raw); bounds.Dx() != 20 || bounds.Dy() != 10 {
		t.Errorf("the downloaded picture is %v, want 20x10", bounds)
	}
	thumb := server.MustDo(http.MethodGet,
		"/_matrix/media/v3/thumbnail/test.server/"+mediaID+"?width=10&height=10&method=scale",
		alice.Token, nil, http.StatusOK)
	if bounds := pngBounds(t, thumb.Raw); bounds.Dx() != 10 || bounds.Dy() != 5 {
		t.Errorf("the thumbnail is %v, want 10x5", bounds)
	}

	// 11. Presence and typing are visible across the room.
	server.MustDo(http.MethodPut, "/_matrix/client/v3/presence/"+bob.UserID+"/status", bob.Token,
		map[string]any{"presence": "online", "status_msg": "watching the rabbit"}, http.StatusOK)
	presence := server.MustDo(http.MethodGet, "/_matrix/client/v3/presence/"+bob.UserID+"/status", alice.Token,
		nil, http.StatusOK)
	if presence.String("status_msg") != "watching the rabbit" {
		t.Errorf("presence = %s", presence.Raw)
	}
	server.MustDo(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/typing/%s", roomID, bob.UserID), bob.Token,
		map[string]any{"typing": true, "timeout": 20000}, http.StatusOK)
	aliceSynced = server.advance(alice)
	aliceRoom = server.joined(t, aliceSynced, roomID)
	if aliceRoom.Ephemeral == nil {
		t.Fatalf("typing never reached alice")
	}
	var typingUsers []string
	for _, event := range aliceRoom.Ephemeral.Events {
		if event.Type != matrix.TypeTyping {
			continue
		}
		var content struct {
			Typing  bool     `json:"typing"`
			UserIDs []string `json:"user_ids"`
		}
		if err := json.Unmarshal(event.Content, &content); err != nil {
			t.Fatalf("decoding the typing event: %v", err)
		}
		if !content.Typing {
			t.Errorf("alice was told bob stopped typing: %s", event.Content)
		}
		typingUsers = content.UserIDs
	}
	if len(typingUsers) != 1 || typingUsers[0] != bob.UserID {
		t.Errorf("typing user_ids = %v", typingUsers)
	}

	// 12. The whole conversation is paginable from the timeline token.
	earlier := server.MustDo(http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/messages?from=%s&dir=b&limit=100", roomID, bobRoom.Timeline.PrevBatch),
		bob.Token, nil, http.StatusOK)
	if len(chunkBodies(eventsOf(earlier.Body["chunk"]))) == 0 {
		t.Errorf("paging back from the timeline token gave nothing: %s", earlier.Raw)
	}
	latest := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=5",
		alice.Token, nil, http.StatusOK)
	if len(chunkBodies(eventsOf(latest.Body["chunk"]))) == 0 {
		t.Errorf("the tail of the history is empty: %s", latest.Raw)
	}
	// The message with the picture is in there somewhere.
	found := false
	for _, body := range chunkBodies(eventsOf(latest.Body["chunk"])) {
		if body == "rabbit.png" {
			found = true
		}
	}
	if !found {
		t.Errorf("the picture message is missing from the history tail: %s", latest.Raw)
	}

	// 13. Bob leaves, and both sides learn about it.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/leave", bob.Token, nil, http.StatusOK)
	aliceSynced = server.advance(alice)
	aliceRoom = server.joined(t, aliceSynced, roomID)
	var left bool
	for _, event := range aliceRoom.Timeline.Events {
		if event.Type != matrix.TypeMember || event.Sender != bob.UserID {
			continue
		}
		var content matrix.MemberContent
		if err := json.Unmarshal(event.Content, &content); err != nil {
			t.Fatalf("decoding the member event: %v", err)
		}
		if content.Membership == matrix.MembershipLeave {
			left = true
		}
	}
	if !left {
		t.Errorf("alice never saw bob leave: %v", aliceRoom.Timeline.Events)
	}
	bobSynced = server.advance(bob)
	if _, ok := bobSynced.Rooms.Leave[roomID]; !ok {
		t.Errorf("bob's sync has no leave section: %v", bobSynced.Rooms)
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/forget", bob.Token, nil, http.StatusOK)
	if rooms, _ := server.MustDo(http.MethodGet, "/_matrix/client/v3/joined_rooms", bob.Token, nil,
		http.StatusOK).Body["joined_rooms"].([]any); len(rooms) != 0 {
		t.Errorf("bob still lists the room: %v", rooms)
	}
	members := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/joined_members", alice.Token,
		nil, http.StatusOK)
	if joinedMembers, _ := members.Body["joined"].(map[string]any); len(joinedMembers) != 1 {
		t.Errorf("joined members after the leave: %s", members.Raw)
	}

	// 14. Logging out invalidates the token.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/logout", bob.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", bob.Token, nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("whoami after logout: got %d %s, want 401", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", bob.Token, nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("sync after logout: got %d %s, want 401", resp.Status, resp.Raw)
	}
	// Alice is untouched by Bob's logout.
	server.MustDo(http.MethodGet, "/_matrix/client/v3/account/whoami", alice.Token, nil, http.StatusOK)
}
