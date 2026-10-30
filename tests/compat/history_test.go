package compat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// sendTyped sends an event of an arbitrary type with arbitrary content.
func (s *Server) sendTyped(session *Session, roomID, txnID, eventType string, content any) string {
	s.t.Helper()
	resp := s.MustDo(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/%s/%s", roomID, eventType, txnID),
		session.Token, content, http.StatusOK)
	return resp.String("event_id")
}

// chunkBodies lists the body of every message of a chunk, in the order the
// chunk came in. Events without a body (state, reactions) are skipped.
func chunkBodies(chunk []matrix.Event) []string {
	var out []string
	for _, event := range chunk {
		var content struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(event.Content, &content); err != nil || content.Body == "" {
			continue
		}
		out = append(out, content.Body)
	}
	return out
}

// TestEventContext covers GET /rooms/{roomId}/context/{eventId}.
func TestEventContext(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	var ids []string
	for _, body := range []string{"one", "two", "three", "four", "five"} {
		ids = append(ids, server.Send(alice, roomID, "ctx-"+body, body))
	}

	resp := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/context/"+ids[2],
		bob.Token, nil, http.StatusOK)
	server.MustFields(resp, "event", "events_before", "events_after", "start", "end")
	if event, _ := resp.Body["event"].(map[string]any); event["event_id"] != ids[2] {
		t.Errorf("the centre event = %v, want %s", event["event_id"], ids[2])
	}

	// events_before runs backwards from the centre event, newest first; the
	// room state that predates the messages is part of the window as well.
	if got := chunkBodies(eventsOf(resp.Body["events_before"])); len(got) != 2 || got[0] != "two" || got[1] != "one" {
		t.Errorf("events_before order = %v", got)
	}
	if got := chunkBodies(eventsOf(resp.Body["events_after"])); len(got) != 2 || got[0] != "four" || got[1] != "five" {
		t.Errorf("events_after order = %v", got)
	}

	// The context includes the state of the room.
	if state := eventsOf(resp.Body["state"]); len(state) == 0 {
		t.Errorf("the context carries no state: %s", resp.Raw)
	}

	// A limit narrows both sides.
	limited := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/context/"+ids[2]+"?limit=1", bob.Token, nil, http.StatusOK)
	if got := chunkBodies(eventsOf(limited.Body["events_before"])); len(got) != 1 || got[0] != "two" {
		t.Errorf("a limited context gave events_before = %v", got)
	}
	if got := chunkBodies(eventsOf(limited.Body["events_after"])); len(got) != 1 || got[0] != "four" {
		t.Errorf("a limited context gave events_after = %v", got)
	}

	// The pagination tokens of the context work on /messages.
	paginated := server.MustDo(http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/messages?from=%s&dir=b&limit=2", roomID, resp.String("end")),
		bob.Token, nil, http.StatusOK)
	if got := chunkBodies(eventsOf(paginated.Body["chunk"])); len(got) != 2 || got[0] != "five" || got[1] != "four" {
		t.Errorf("paging back from the context token gave %v: %s", got, paginated.Raw)
	}

	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/context/$nope", bob.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("context of an unknown event: got %d %s, want 404", resp.Status, resp.Raw)
	}

	carol := server.Register("carol", "kettle")
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/context/"+ids[0], carol.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("an outsider read the context: got %d %s, want 403", resp.Status, resp.Raw)
	}
}

// eventsOf decodes a JSON array of events.
func eventsOf(raw any) []matrix.Event {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []matrix.Event
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

// TestEventPermissions covers GET /rooms/{roomId}/event/{eventId}.
func TestEventPermissions(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	aliceRoom := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Secret"})
	other := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Other"})
	eventID := server.Send(alice, aliceRoom, "perm-1", "for the room")

	got := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+aliceRoom+"/event/"+eventID,
		alice.Token, nil, http.StatusOK)
	if got.String("event_id") != eventID || got.String("room_id") != aliceRoom {
		t.Errorf("fetched event = %s", got.Raw)
	}

	carol := server.Register("carol", "kettle")
	for _, path := range []string{
		"/_matrix/client/v3/rooms/" + aliceRoom + "/event/" + eventID,
		"/_matrix/client/v3/rooms/" + other + "/event/" + eventID,
		"/_matrix/client/v3/rooms/" + other + "/messages",
		"/_matrix/client/v3/rooms/" + other + "/context/" + eventID,
	} {
		if resp := server.Do(http.MethodGet, path, carol.Token, nil); resp.Status != http.StatusForbidden {
			t.Errorf("%s: got %d %s, want 403", path, resp.Status, resp.Raw)
		}
	}

	// An event id of another room is not readable through this room either.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+aliceRoom+"/event/$unknown",
		alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("an unknown event id: got %d %s, want 404", resp.Status, resp.Raw)
	}

	// Redacted events keep their id but lose their content.
	secret := server.Send(alice, aliceRoom, "perm-2", "secret")
	server.MustDo(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/redact/%s/txn-perm", aliceRoom, secret), alice.Token,
		map[string]any{"reason": "because"}, http.StatusOK)
	redacted := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+aliceRoom+"/event/"+secret,
		alice.Token, nil, http.StatusOK)
	if redacted.String("type") != matrix.TypeMessage {
		t.Errorf("a redacted event changed type: %s", redacted.Raw)
	}
	if content, ok := redacted.Body["content"].(map[string]any); !ok || len(content) != 0 {
		t.Errorf("a redacted event still has content: %s", redacted.Raw)
	}
	if _, ok := redacted.Body["prev_content"]; ok {
		t.Errorf("prev_content leaked into a redacted event: %s", redacted.Raw)
	}
}

// TestRelations covers GET /rooms/{roomId}/relations/{eventId}.
func TestRelations(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	target := server.Send(alice, roomID, "rel-target", "post")
	react := func(session *Session, txn, key, relType string) string {
		return server.sendTyped(session, roomID, txn, "m.reaction", map[string]any{
			"m.relates_to": map[string]any{"rel_type": relType, "event_id": target, "key": key},
		})
	}
	firstReaction := react(bob, "rel-r1", "👍", "m.annotation")
	secondReaction := react(alice, "rel-r2", "🎉", "m.annotation")
	unknown := server.sendTyped(bob, roomID, "rel-nested", "m.reaction", map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": "$not-here", "key": "😼"},
	})

	list := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/relations/"+target,
		bob.Token, nil, http.StatusOK)
	chunk := eventsOf(list.Body["chunk"])
	if len(chunk) != 2 {
		t.Fatalf("relations = %s", list.Raw)
	}
	// Relations come back oldest first.
	if chunk[0].ID != firstReaction || chunk[1].ID != secondReaction {
		t.Errorf("relation order = %s, %s", chunk[0].ID, chunk[1].ID)
	}
	if chunk[0].Type != "m.reaction" || chunk[0].Sender != bob.UserID {
		t.Errorf("relation chunk = %s", list.Raw)
	}
	// A page that filled up carries a token for the next one.
	page := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?limit=1", alice.Token, nil, http.StatusOK)
	first := eventsOf(page.Body["chunk"])
	if len(first) != 1 || first[0].ID != firstReaction {
		t.Errorf("the first relations page = %s", page.Raw)
	}
	token := page.String("next_batch")
	if token == "" {
		t.Fatalf("a full relations page has no next_batch: %s", page.Raw)
	}
	tail := server.MustDo(http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/relations/%s?limit=1&from=%s", roomID, target, token),
		alice.Token, nil, http.StatusOK)
	if rest := eventsOf(tail.Body["chunk"]); len(rest) != 1 || rest[0].ID != secondReaction {
		t.Errorf("the second relations page = %s", tail.Raw)
	}

	// A relation of an event nobody knows about is still listed under its own id.
	if solo := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/relations/$not-here",
		alice.Token, nil, http.StatusOK); len(eventsOf(solo.Body["chunk"])) != 1 {
		t.Errorf("relations of an unknown event = %s", solo.Raw)
	}

	// Filtering by relation type and event type.
	annotations := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?rel_type=m.annotation", alice.Token,
		nil, http.StatusOK)
	if len(eventsOf(annotations.Body["chunk"])) != 2 {
		t.Errorf("rel_type=m.annotation: %s", annotations.Raw)
	}
	if threads := server.Do(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?rel_type=m.thread", alice.Token, nil); threads.Status != http.StatusOK ||
		len(eventsOf(threads.Body["chunk"])) != 0 {
		t.Errorf("rel_type=m.thread should be empty: %d %s", threads.Status, threads.Raw)
	}
	byType := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?event_type=m.reaction", alice.Token,
		nil, http.StatusOK)
	if len(eventsOf(byType.Body["chunk"])) != 2 {
		t.Errorf("event_type=m.reaction: %s", byType.Raw)
	}
	if none := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?event_type=m.sticker", alice.Token,
		nil, http.StatusOK); len(eventsOf(none.Body["chunk"])) != 0 {
		t.Errorf("event_type=m.sticker should be empty: %s", none.Raw)
	}
	// Both filters at once.
	if both := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?rel_type=m.annotation&event_type=m.reaction",
		alice.Token, nil, http.StatusOK); len(eventsOf(both.Body["chunk"])) != 2 {
		t.Errorf("combined filters: %s", both.Raw)
	}
	// A filter that cannot match gives an empty chunk, not an error.
	if nomatch := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?rel_type=m.annotation&event_type=m.room.message",
		alice.Token, nil, http.StatusOK); len(eventsOf(nomatch.Body["chunk"])) != 0 {
		t.Errorf("impossible filters: %s", nomatch.Raw)
	}
	_ = unknown

	// A member may list relations, an outsider may not.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/relations/"+target,
		server.Register("carol", "kettle").Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("an outsider listed relations: got %d %s, want 403", resp.Status, resp.Raw)
	}

	// A reaction with an unknown target is accepted as a normal event.
	if resp := server.Do(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.reaction/rel-unknown", roomID), bob.Token,
		map[string]any{"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": "$missing", "key": "🙂"}}); resp.Status != http.StatusOK {
		t.Errorf("reacting to an unknown event: got %d %s, want 200", resp.Status, resp.Raw)
	}
}

// TestThreads covers GET /v1/rooms/{roomId}/threads.
func TestThreads(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	root := server.Send(alice, roomID, "thread-root", "starting a thread")
	// A plain message is not a thread.
	server.Send(bob, roomID, "plain-reply", "not in the thread")
	inThread := func(session *Session, txn, body string) {
		server.sendTyped(session, roomID, txn, matrix.TypeMessage, map[string]any{
			"msgtype": "m.text", "body": body,
			"m.relates_to": map[string]any{
				"rel_type": "m.thread", "event_id": root, "is_falling_back": true,
				"m.in_reply_to": map[string]any{"event_id": root},
			},
		})
	}
	inThread(bob, "thread-1", "first answer")
	latest := server.sendTyped(bob, roomID, "thread-2", matrix.TypeMessage, map[string]any{
		"msgtype": "m.text", "body": "second answer",
		"m.relates_to": map[string]any{
			"rel_type": "m.thread", "event_id": root, "is_falling_back": true,
			"m.in_reply_to": map[string]any{"event_id": root},
		},
	})

	list := server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/threads", alice.Token,
		nil, http.StatusOK)
	chunk, _ := list.Body["chunk"].([]any)
	if len(chunk) != 1 {
		t.Fatalf("threads = %s", list.Raw)
	}
	thread, _ := chunk[0].(map[string]any)
	if thread["event_id"] != root {
		t.Errorf("thread root = %v, want %s", thread["event_id"], root)
	}
	if count, _ := thread["count"].(float64); count != 2 {
		t.Errorf("thread count = %v, want 2 replies: %s", thread["count"], list.Raw)
	}
	if num, _ := thread["num_events"].(float64); num != 3 {
		t.Errorf("thread num_events = %v, want 3 (root plus two replies): %s", thread["num_events"], list.Raw)
	}
	synced := server.MustSync(alice, "?timeout=0")
	room := synced.Rooms.Join[roomID]
	var seenRoot bool
	for _, event := range room.Timeline.Events {
		if event.ID != root {
			continue
		}
		seenRoot = true
		summary, _ := event.Unsigned["m.thread"].(map[string]any)
		if summary == nil {
			t.Errorf("the thread root carries no thread summary: %s", event.Type)
			break
		}
		if num, _ := summary["num_events"].(float64); num != 3 {
			t.Errorf("thread summary num_events = %v: %v", summary["num_events"], event.Unsigned)
		}
		if latestEvent, _ := summary["latest_event"].(map[string]any); latestEvent["event_id"] != latest {
			t.Errorf("thread summary latest_event = %v, want %s", summary["latest_event"], latest)
		}
	}
	if !seenRoot {
		t.Errorf("the thread root is not in the timeline: %s", list.Raw)
	}

	// Relations of the thread root list the replies.
	relations := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+root+"?rel_type=m.thread", bob.Token, nil, http.StatusOK)
	if got := eventsOf(relations.Body["chunk"]); len(got) != 2 {
		t.Errorf("thread relations = %s", relations.Raw)
	}

	// A non-member may not list threads.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/threads",
		server.Register("dave", "tea").Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("an outsider listed threads: got %d %s, want 403", resp.Status, resp.Raw)
	}
	// A thread the requester took part in shows up under include=mine.
	mine := server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/threads?include=mine",
		bob.Token, nil, http.StatusOK)
	if chunk, _ := mine.Body["chunk"].([]any); len(chunk) != 1 {
		t.Errorf("include=mine for bob: %s", mine.Raw)
	}
	// Bogus filter values are rejected instead of ignored.
	for _, query := range []string{"?include=somebody-elses", "?relationship=oldest"} {
		if resp := server.Do(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/threads"+query,
			alice.Token, nil); resp.Status != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", query, resp.Status, resp.Raw)
		}
	}
}

// TestTimestampToEvent covers GET /v1/rooms/{roomId}/timestamp_to_event.
func TestTimestampToEvent(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})

	first := server.Send(alice, roomID, "ts-1", "one")
	middle := server.Send(alice, roomID, "ts-2", "two")
	last := server.Send(alice, roomID, "ts-3", "three")
	ts := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/event/"+middle,
		alice.Token, nil, http.StatusOK).Int("origin_server_ts")
	oldest := oldestEvent(t, server, alice, roomID)
	_ = first

	for _, tc := range []struct {
		name, query, want string
	}{
		{"forward from the event itself", fmt.Sprintf("?ts=%d&dir=f", ts), middle},
		{"backward from the event itself", fmt.Sprintf("?ts=%d&dir=b", ts), middle},
		{"forward from before everything", "?ts=1&dir=f", oldest},
		{"backward from after everything", fmt.Sprintf("?ts=%d&dir=b", time.Now().UnixMilli()+60000), last},
		{"forward direction is the default", fmt.Sprintf("?ts=%d", ts), middle},
	} {
		resp := server.MustDo(http.MethodGet,
			"/_matrix/client/v1/rooms/"+roomID+"/timestamp_to_event"+tc.query, alice.Token, nil, http.StatusOK)
		if resp.String("event_id") != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, resp.Raw, tc.want)
		}
		if resp.Int("origin_server_ts") == 0 {
			t.Errorf("%s: no origin_server_ts in %s", tc.name, resp.Raw)
		}
	}

	// Nothing at or after the end of the room.
	if resp := server.Do(http.MethodGet,
		fmt.Sprintf("/_matrix/client/v1/rooms/%s/timestamp_to_event?ts=%d&dir=f", roomID, time.Now().UnixMilli()+60000),
		alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("past the end of the room: got %d %s, want 404", resp.Status, resp.Raw)
	}
	// Nothing before the start of the room.
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v1/rooms/"+roomID+"/timestamp_to_event?ts=1&dir=b", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("before the start of the room: got %d %s, want 404", resp.Status, resp.Raw)
	}
	// A room without messages still has its create event to jump to.
	empty := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	if resp := server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+empty+"/timestamp_to_event?ts=1&dir=f", alice.Token, nil,
		http.StatusOK); resp.String("event_id") == "" {
		t.Errorf("a room without messages: %s", resp.Raw)
	}
	for _, query := range []string{"?dir=f", "?ts=not-a-number"} {
		if resp := server.Do(http.MethodGet,
			"/_matrix/client/v1/rooms/"+roomID+"/timestamp_to_event"+query, alice.Token, nil); resp.Status != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", query, resp.Status, resp.Raw)
		}
	}
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v1/rooms/"+roomID+"/timestamp_to_event?ts=1&dir=sideways", alice.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("an unknown direction: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v1/rooms/"+roomID+"/timestamp_to_event?ts=1&dir=f",
		server.Register("carol", "kettle").Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("an outsider searched by timestamp: got %d %s, want 403", resp.Status, resp.Raw)
	}
}

// oldestEvent returns the first event of a room according to /messages.
func oldestEvent(t *testing.T, server *Server, session *Session, roomID string) string {
	t.Helper()
	page := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=100", session.Token, nil, http.StatusOK)
	chunk := eventsOf(page.Body["chunk"])
	if len(chunk) == 0 {
		t.Fatalf("the room has no history: %s", page.Raw)
	}
	return chunk[len(chunk)-1].ID
}

// TestReportEvent covers POST /rooms/{roomId}/report/{eventId}.
func TestReportEvent(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	eventID := server.Send(alice, roomID, "report-target", "spam")

	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report/"+eventID,
		bob.Token, map[string]any{"reason": "spammy", "score": -50}, http.StatusOK)
	if len(resp.Body) != 0 {
		t.Errorf("a report returned a body: %s", resp.Raw)
	}
	// A report may be filed without a score, and twice for the same event.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report/"+eventID,
		alice.Token, map[string]any{"reason": "self report"}, http.StatusOK)

	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report/$missing",
		bob.Token, map[string]any{"reason": "gone"}); resp.Status != http.StatusNotFound {
		t.Errorf("reporting an unknown event: got %d %s, want 404", resp.Status, resp.Raw)
	}
	// Scores must be negative, more severe being more negative.
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report/"+eventID,
		bob.Token, map[string]any{"score": 50}); resp.Status != http.StatusBadRequest {
		t.Errorf("a positive score: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report/"+eventID,
		bob.Token, map[string]any{"reason": 42}); resp.Status != http.StatusBadRequest {
		t.Errorf("a non-string reason: got %d %s, want 400", resp.Status, resp.Raw)
	}
	// Only members of the room may report its events.
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report/"+eventID,
		server.Register("carol", "kettle").Token, map[string]any{"reason": "outsider"}); resp.Status != http.StatusForbidden {
		t.Errorf("an outsider reported an event: got %d %s, want 403", resp.Status, resp.Raw)
	}
}

// TestTyping covers PUT /rooms/{roomId}/typing/{userId} arriving in the sync
// of another member.
func TestTyping(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	setTyping := func(session *Session, typing bool, timeout int) {
		server.MustDo(http.MethodPut,
			fmt.Sprintf("/_matrix/client/v3/rooms/%s/typing/%s", roomID, session.UserID), session.Token,
			map[string]any{"typing": typing, "timeout": timeout}, http.StatusOK)
	}

	// Typing is only visible to other members.
	setTyping(alice, true, 30000)
	synced := server.MustSync(bob, "?timeout=0")
	room := synced.Rooms.Join[roomID]
	if room.Ephemeral == nil {
		t.Fatalf("no ephemeral events for a typing member: %v", room)
	}
	var typingEvent *matrix.Event
	for i, event := range room.Ephemeral.Events {
		if event.Type == "m.typing" {
			typingEvent = &room.Ephemeral.Events[i]
		}
	}
	if typingEvent == nil {
		t.Fatalf("no m.typing event: %v", room.Ephemeral.Events)
	}
	var content struct {
		Typing  bool     `json:"typing"`
		UserIDs []string `json:"user_ids"`
	}
	if err := json.Unmarshal(typingEvent.Content, &content); err != nil {
		t.Fatalf("decoding the typing event: %v", err)
	}
	if !content.Typing || len(content.UserIDs) != 1 || content.UserIDs[0] != alice.UserID {
		t.Errorf("typing content = %s", typingEvent.Content)
	}

	// The typing state of the requesting user is never listed back to them.
	own := server.MustSync(alice, "?timeout=0")
	if ephemeral := own.Rooms.Join[roomID].Ephemeral; ephemeral != nil {
		for _, event := range ephemeral.Events {
			if event.Type != "m.typing" {
				continue
			}
			var ownContent struct {
				UserIDs []string `json:"user_ids"`
			}
			if err := json.Unmarshal(event.Content, &ownContent); err != nil {
				t.Fatalf("decoding alice's typing event: %v", err)
			}
			for _, id := range ownContent.UserIDs {
				if id == alice.UserID {
					t.Errorf("sync echoed the typing state of the requesting user back to them: %s", event.Content)
				}
			}
		}
	}

	// Stopping typing is announced too.
	setTyping(alice, false, 0)
	stopped := server.MustSync(bob, "?timeout=0")
	found := false
	stoppedRoom := stopped.Rooms.Join[roomID]
	if stoppedRoom.Ephemeral != nil {
		for _, event := range stoppedRoom.Ephemeral.Events {
			if event.Type != "m.typing" {
				continue
			}
			found = true
			if err := json.Unmarshal(event.Content, &content); err != nil {
				t.Fatalf("decoding the stopped typing event: %v", err)
			}
			if content.Typing || len(content.UserIDs) != 0 {
				t.Errorf("stopped typing content = %s", event.Content)
			}
		}
	}
	if !found {
		t.Errorf("stopping typing produced no m.typing event: %v", stoppedRoom.Ephemeral)
	}

	// The timeout expires on its own.
	setTyping(alice, true, 200)
	time.Sleep(400 * time.Millisecond)
	expired := server.MustSync(bob, "?timeout=0")
	expiredRoom := expired.Rooms.Join[roomID]
	if expiredRoom.Ephemeral != nil {
		for _, event := range expiredRoom.Ephemeral.Events {
			if event.Type != "m.typing" {
				continue
			}
			if err := json.Unmarshal(event.Content, &content); err != nil {
				t.Fatalf("decoding the expired typing event: %v", err)
			}
			if content.Typing || len(content.UserIDs) != 0 {
				t.Errorf("an expired typing state is still active: %s", event.Content)
			}
		}
	}
}

// TestReportRoomAndUser covers POST /rooms/{roomId}/report and
// POST /users/{userId}/report.
func TestReportRoomAndUser(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report", bob.Token,
		map[string]any{"reason": "this room is spam"}, http.StatusOK)
	if len(resp.Body) != 0 {
		t.Errorf("reporting a room returned a body: %s", resp.Raw)
	}

	carol := server.Register("carol", "kettle")
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report", carol.Token,
		map[string]any{"reason": "outsider"}); resp.Status != http.StatusForbidden {
		t.Errorf("an outsider reported a private room: got %d %s, want 403", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/!nosuchroom:test.server/report",
		bob.Token, map[string]any{"reason": "gone"}); resp.Status != http.StatusNotFound {
		t.Errorf("reporting an unknown room: got %d %s, want 404", resp.Status, resp.Raw)
	}

	// Users of this homeserver can be reported.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/users/"+alice.UserID+"/report", bob.Token,
		map[string]any{"reason": "rude"}, http.StatusOK)
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/users/@nobody:test.server/report",
		bob.Token, map[string]any{"reason": "ghost"}); resp.Status != http.StatusNotFound {
		t.Errorf("reporting an unknown user: got %d %s, want 404", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/users/"+bob.UserID+"/report", bob.Token,
		map[string]any{"reason": "myself"}); resp.Status != http.StatusBadRequest {
		t.Errorf("reporting yourself: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/users/"+alice.UserID+"/report", bob.Token,
		map[string]any{"reason": 7}); resp.Status != http.StatusBadRequest {
		t.Errorf("a non-string reason: got %d %s, want 400", resp.Status, resp.Raw)
	}
}

// storedReport mirrors the administrator-facing report record.
type storedReport struct {
	ID           uint           `json:"id"`
	Kind         string         `json:"kind"`
	RoomID       string         `json:"room_id"`
	EventID      string         `json:"event_id"`
	UserID       string         `json:"user_id"`
	Reporter     string         `json:"reporter"`
	Reason       string         `json:"reason"`
	Score        int64          `json:"score"`
	OriginServer int64          `json:"origin_server_ts"`
	EventContent map[string]any `json:"event_content"`
}

// decodeReports unpacks the body of GET /v1/admin/reports.
func decodeReports(t *testing.T, resp Response) []storedReport {
	t.Helper()
	var body struct {
		Reports []storedReport `json:"reports"`
	}
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatalf("decoding reports: %v (%s)", err, resp.Raw)
	}
	return body.Reports
}

// TestAdminReports checks that filed abuse reports are stored and can be listed
// and filtered by an administrator.
func TestAdminReports(t *testing.T) {
	server := NewServer(t)
	// The first account on a server is its administrator.
	root := server.Register("root", "throneroom")
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	eventID := server.Send(alice, roomID, "spam-1", "buy my stuff")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report/"+eventID, bob.Token,
		map[string]any{"reason": "spam", "score": -100}, http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/report", bob.Token,
		map[string]any{"reason": "the whole room is spam"}, http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/users/"+alice.UserID+"/report", bob.Token,
		map[string]any{"reason": "she spams"}, http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/users/"+bob.UserID+"/report", alice.Token,
		map[string]any{"reason": "he reports everything"}, http.StatusOK)

	listed := server.MustDo(http.MethodGet, "/_matrix/client/v1/admin/reports", root.Token, nil, http.StatusOK)
	reports := decodeReports(t, listed)
	if got := listed.Int("total"); got != 4 {
		t.Errorf("total = %d, want 4 in %s", got, listed.Raw)
	}
	if len(reports) != 4 {
		t.Fatalf("listed %d reports, want 4: %s", len(reports), listed.Raw)
	}
	// Newest first, so the user report about bob is the first one.
	if reports[0].Kind != "user" || reports[0].UserID != bob.UserID || reports[0].Reporter != alice.UserID {
		t.Errorf("newest report = %+v", reports[0])
	}
	if reports[1].Kind != "user" || reports[1].UserID != alice.UserID {
		t.Errorf("second report = %+v", reports[1])
	}
	if reports[2].Kind != "room" || reports[2].RoomID != roomID {
		t.Errorf("room report = %+v", reports[2])
	}
	event := reports[3]
	if event.Kind != "event" || event.EventID != eventID || event.RoomID != roomID {
		t.Errorf("event report = %+v", event)
	}
	if event.Reason != "spam" || event.Score != -100 || event.Reporter != bob.UserID {
		t.Errorf("event report details = %+v", event)
	}
	if event.OriginServer == 0 {
		t.Errorf("the event report has no timestamp: %+v", event)
	}
	// The reported content is copied when the report arrives, so that redacting
	// the event later does not hide what was reported.
	if event.EventContent["body"] != "buy my stuff" {
		t.Errorf("reported content = %v", event.EventContent)
	}
	server.MustDo(http.MethodPut, fmt.Sprintf("/_matrix/client/v3/rooms/%s/redact/%s/txn-redact-report", roomID, eventID),
		alice.Token, map[string]any{"reason": "cleaning up"}, http.StatusOK)
	after := decodeReports(t, server.MustDo(http.MethodGet, "/_matrix/client/v1/admin/reports", root.Token, nil, http.StatusOK))
	if after[3].EventContent["body"] != "buy my stuff" {
		t.Errorf("the report lost its content after redaction: %v", after[3].EventContent)
	}

	// Filters narrow the listing; total counts what matches, not what is shown.
	byKind := server.MustDo(http.MethodGet, "/_matrix/client/v1/admin/reports?kind=event", root.Token, nil, http.StatusOK)
	if got := decodeReports(t, byKind); len(got) != 1 || got[0].EventID != eventID {
		t.Errorf("kind=event = %+v", got)
	}
	if got := byKind.Int("total"); got != 1 {
		t.Errorf("total for kind=event = %d", got)
	}
	byReporter := server.MustDo(http.MethodGet,
		"/_matrix/client/v1/admin/reports?reporter="+url.QueryEscape(alice.UserID), root.Token, nil, http.StatusOK)
	if got := decodeReports(t, byReporter); len(got) != 1 || got[0].Reporter != alice.UserID {
		t.Errorf("reporter filter = %+v", got)
	}
	byRoom := server.MustDo(http.MethodGet, "/_matrix/client/v1/admin/reports?room_id="+url.QueryEscape(roomID),
		root.Token, nil, http.StatusOK)
	if got := decodeReports(t, byRoom); len(got) != 2 {
		t.Errorf("room filter = %+v", got)
	}
	paged := server.MustDo(http.MethodGet, "/_matrix/client/v1/admin/reports?limit=1&offset=1", root.Token, nil, http.StatusOK)
	if got := decodeReports(t, paged); len(got) != 1 {
		t.Errorf("limit=1&offset=1 returned %+v", got)
	}
	if got := paged.Int("total"); got != 4 {
		t.Errorf("total with a limit = %d, want the unfiltered 4", got)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/admin/reports?kind=bogus", root.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("an unknown report kind: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/admin/reports", alice.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("a non-admin listing reports: got %d %s, want 403", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/admin/reports", "", nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated report listing: got %d, want 401", resp.Status)
	}
}
