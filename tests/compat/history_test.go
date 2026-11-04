package compat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
		"/_matrix/client/v3/rooms/" + other + "/messages",
		"/_matrix/client/v3/rooms/" + other + "/context/" + eventID,
	} {
		if resp := server.Do(http.MethodGet, path, carol.Token, nil); resp.Status != http.StatusForbidden {
			t.Errorf("%s: got %d %s, want 403", path, resp.Status, resp.Raw)
		}
	}
	// The event endpoints refuse a stranger with 404 rather than 403: saying "you may not see
	// this one" confirms the room holds an event by that id, which is what the room's history
	// rule is meant to keep quiet.
	for _, path := range []string{
		"/_matrix/client/v3/rooms/" + aliceRoom + "/event/" + eventID,
		"/_matrix/client/v3/rooms/" + other + "/event/" + eventID,
	} {
		if resp := server.Do(http.MethodGet, path, carol.Token, nil); resp.Status != http.StatusNotFound {
			t.Errorf("%s: got %d %s, want 404", path, resp.Status, resp.Raw)
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
	// A token from before any reaction, so the tests below can describe a window
	// with both bounds.
	oldestToken := server.MustSync(alice, "").NextBatch
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
	// The default direction is "b": the newest child first, which is what the
	// relations endpoints promise ("ordered topologically most-recent first",
	// relations.yaml at spec v1.19) and what dir=b means.
	if chunk[0].ID != secondReaction || chunk[1].ID != firstReaction {
		t.Errorf("relation order = %s, %s, want newest first", chunk[0].ID, chunk[1].ID)
	}
	if chunk[0].Type != "m.reaction" || chunk[0].Sender != alice.UserID {
		t.Errorf("relation chunk = %s", list.Raw)
	}
	// dir=f walks the same window the other way round.
	forwards := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?dir=f", alice.Token, nil, http.StatusOK)
	if got := eventsOf(forwards.Body["chunk"]); len(got) != 2 ||
		got[0].ID != firstReaction || got[1].ID != secondReaction {
		t.Errorf("dir=f = %s, want oldest first", forwards.Raw)
	}
	// A bogus direction is a client error, not a silent default.
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?dir=sideways", alice.Token,
		nil); resp.Status != http.StatusBadRequest {
		t.Errorf("dir=sideways: got %d %s, want 400", resp.Status, resp.Raw)
	}
	// A page that filled up carries a token for the next one.
	page := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+target+"?limit=1", alice.Token, nil, http.StatusOK)
	first := eventsOf(page.Body["chunk"])
	if len(first) != 1 || first[0].ID != secondReaction {
		t.Errorf("the first relations page = %s", page.Raw)
	}
	token := page.String("next_batch")
	if token == "" {
		t.Fatalf("a full relations page has no next_batch: %s", page.Raw)
	}
	tail := server.MustDo(http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/relations/%s?limit=1&from=%s", roomID, target, token),
		alice.Token, nil, http.StatusOK)
	if rest := eventsOf(tail.Body["chunk"]); len(rest) != 1 || rest[0].ID != firstReaction {
		t.Errorf("the second relations page = %s", tail.Raw)
	}
	// to bounds the far end of the window. Walking backwards from the newest
	// reaction down to the token taken before any of them covers the range in
	// between.
	if bounded := server.MustDo(http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/relations/%s?from=%s&to=%s", roomID, target, token, oldestToken),
		alice.Token, nil, http.StatusOK); len(eventsOf(bounded.Body["chunk"])) != 1 {
		t.Errorf("from=%s to=%s should cover one reaction: %s", token, oldestToken, bounded.Raw)
	}
	// The specification's note about the other way round: a from token that sits
	// before the to token walks an empty range.
	if reversed := server.MustDo(http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/relations/%s?from=%s&to=%s", roomID, target, oldestToken, token),
		alice.Token, nil, http.StatusOK); len(eventsOf(reversed.Body["chunk"])) != 0 {
		t.Errorf("from=%s to=%s should be empty: %s", oldestToken, token, reversed.Raw)
	}
	// The same tokens read forwards cover the range between them.
	if rangePage := server.MustDo(http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/relations/%s?dir=f&from=%s&to=%s", roomID, target, oldestToken, token),
		alice.Token, nil, http.StatusOK); len(eventsOf(rangePage.Body["chunk"])) != 1 {
		t.Errorf("dir=f from=%s to=%s: %s", oldestToken, token, rangePage.Raw)
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

// TestRelationRedactionRules pins what redaction does to relationships. MSC2675
// (the "Redactions" part of "Event relationships" in the v1.19 client-server
// spec) says redacted relations "should not be taken into consideration in
// bundled aggregations, nor should they be returned from /relations", and that
// requesting /relations on a redacted event still returns its relations. So the
// relation is hidden and the target is not.
func TestRelationRedactionRules(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	redact := func(session *Session, target, txn string) {
		server.MustDo(http.MethodPut,
			fmt.Sprintf("/_matrix/client/v3/rooms/%s/redact/%s/%s", roomID, target, txn),
			session.Token, map[string]any{"reason": "tidying up"}, http.StatusOK)
	}

	target := server.Send(alice, roomID, "red-rel-target", "a post worth reacting to")
	// The reactions relate to the target, so they are sent after it exists.
	keep := server.sendTyped(bob, roomID, "red-rel-keep", "m.reaction", map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": target, "key": "👍"},
	})
	gone := server.sendTyped(bob, roomID, "red-rel-gone", "m.reaction", map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": target, "key": "🎉"},
	})

	redact(alice, gone, "red-rel-strip-reaction")
	listed := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/relations/"+target,
		alice.Token, nil, http.StatusOK)
	if got := eventsOf(listed.Body["chunk"]); len(got) != 1 || got[0].ID != keep {
		t.Errorf("a redacted relation is still listed: %s", listed.Raw)
	}
	// The redacted reaction is still an event in the room, emptied rather than
	// removed, and its relation survives in the store: it is only hidden.
	if raw := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/event/"+gone,
		alice.Token, nil, http.StatusOK); !isRedacted(raw.Body) {
		t.Errorf("the redacted reaction does not carry its redaction: %s", raw.Raw)
	}

	// Redacting the target changes nothing for its relations: the same MSC says
	// /relations on a redacted event keeps returning them, in line with /context
	// and /messages.
	redact(alice, target, "red-rel-strip-target")
	after := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/relations/"+target,
		alice.Token, nil, http.StatusOK)
	if got := eventsOf(after.Body["chunk"]); len(got) != 1 || got[0].ID != keep {
		t.Errorf("redacting the target hid its relations: %s", after.Raw)
	}
}

// TestThreadPaginationAndRedaction covers the dir and to parameters of
// GET /v1/rooms/{roomId}/threads, the thread summary on the single-event
// endpoints, and MSC2675's rule that redacted relations stay out of the
// aggregation a thread summary is built from.
func TestThreadPaginationAndRedaction(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	reply := func(session *Session, txn, root, body string) string {
		return server.sendTyped(session, roomID, txn, matrix.TypeMessage, map[string]any{
			"msgtype": "m.text", "body": body,
			"m.relates_to": map[string]any{
				"rel_type": "m.thread", "event_id": root, "is_falling_back": true,
				"m.in_reply_to": map[string]any{"event_id": root},
			},
		})
	}
	threads := func(query string) Response {
		return server.MustDo(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/threads"+query,
			alice.Token, nil, http.StatusOK)
	}
	rootIDs := func(resp Response) []string {
		chunk, _ := resp.Body["chunk"].([]any)
		out := make([]string, 0, len(chunk))
		for _, raw := range chunk {
			thread, _ := raw.(map[string]any)
			id, _ := thread["event_id"].(string)
			out = append(out, id)
		}
		return out
	}

	// Two threads: the older one answered first, the newer one last. Threads are
	// ordered by their most recent activity, so the second one leads.
	firstRoot := server.Send(alice, roomID, "page-root-1", "older thread")
	firstReply := reply(bob, "page-reply-1", firstRoot, "answer in the older thread")
	secondRoot := server.Send(alice, roomID, "page-root-2", "newer thread")
	reply(bob, "page-reply-2", secondRoot, "answer in the newer thread")

	// The token of the older thread's most recent activity, read off a forwards
	// walk that stops right there.
	firstToken := threads("?dir=f&limit=1").String("next_batch")
	if firstToken == "" {
		t.Fatalf("the first forwards thread page carries no next_batch")
	}

	backwards := threads("")
	if got := rootIDs(backwards); len(got) != 2 || got[0] != secondRoot || got[1] != firstRoot {
		t.Errorf("default thread order = %v, want newest activity first (%s, %s)", got, secondRoot, firstRoot)
	}
	forwards := threads("?dir=f")
	if got := rootIDs(forwards); len(got) != 2 || got[0] != firstRoot || got[1] != secondRoot {
		t.Errorf("dir=f = %v, want oldest activity first", got)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/rooms/"+roomID+"/threads?dir=up", alice.Token,
		nil); resp.Status != http.StatusBadRequest {
		t.Errorf("dir=up: got %d %s, want 400", resp.Status, resp.Raw)
	}

	// One thread per page, walking backwards.
	page := threads("?limit=1")
	if got := rootIDs(page); len(got) != 1 || got[0] != secondRoot {
		t.Fatalf("the first thread page = %s", page.Raw)
	}
	token := page.String("next_batch")
	if token == "" {
		t.Fatalf("a full thread page carries no next_batch: %s", page.Raw)
	}
	if next := threads("?limit=1&from=" + token); len(rootIDs(next)) != 1 || rootIDs(next)[0] != firstRoot {
		t.Errorf("the page after %s = %s, want the older thread", token, next.Raw)
	}
	// to bounds the far end of the window: the older thread sits exactly on that
	// bound, and both bounds are exclusive.
	bounded := server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+roomID+"/threads?from="+token+"&to="+firstToken, alice.Token, nil,
		http.StatusOK)
	if len(rootIDs(bounded)) != 0 {
		t.Errorf("from=%s to=%s should be empty: %s", token, firstToken, bounded.Raw)
	}
	// An empty chunk is an empty array, not null: clients read chunk as a list.
	if !strings.Contains(string(bounded.Raw), `"chunk":[]`) {
		t.Errorf("an empty thread listing should answer with an empty chunk: %s", bounded.Raw)
	}
	// relationship=latest keeps the most recently active thread whichever way the
	// listing walks.
	if latest := threads("?dir=f&relationship=latest"); len(rootIDs(latest)) != 1 || rootIDs(latest)[0] != secondRoot {
		t.Errorf("dir=f relationship=latest = %s, want %s", latest.Raw, secondRoot)
	}

	// The single-event endpoints bundle the thread summary too: MSC2675 lists
	// GET /rooms/{roomId}/event/{eventId} and GET /rooms/{roomId}/context/{eventId}
	// among the endpoints that bundle aggregations.
	event := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/event/"+firstRoot, alice.Token, nil, http.StatusOK)
	summary := threadSummaryOf(t, event.Body)
	if num, _ := summary["num_events"].(float64); num != 2 {
		t.Errorf("GET /event: num_events = %v, want 2: %s", num, event.Raw)
	}
	if latest, _ := summary["latest_event"].(map[string]any); latest["event_id"] != firstReply {
		t.Errorf("GET /event: latest_event = %v, want %s", latest["event_id"], firstReply)
	}
	around := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/context/"+firstRoot, alice.Token, nil, http.StatusOK)
	centre, _ := around.Body["event"].(map[string]any)
	centreSummary := threadSummaryOf(t, centre)
	if num, _ := centreSummary["num_events"].(float64); num != 2 {
		t.Errorf("/context centre num_events = %v, want 2: %s", num, around.Raw)
	}

	// Redacting a reply takes it out of the aggregation: the count drops, the
	// latest event moves, and the listing of relations stops showing it. The event
	// itself stays in the timeline, emptied.
	server.MustDo(http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/redact/%s/threads-redact", roomID, firstReply),
		alice.Token, map[string]any{"reason": "never mind"}, http.StatusOK)

	remaining := threads("?dir=f")
	chunk, _ := remaining.Body["chunk"].([]any)
	if len(chunk) != 1 {
		t.Fatalf("after redacting its only reply the older thread should be gone: %s", remaining.Raw)
	}
	thread, _ := chunk[0].(map[string]any)
	if thread["event_id"] != secondRoot {
		t.Errorf("remaining thread = %v, want %s", thread["event_id"], secondRoot)
	}
	if num, _ := thread["num_events"].(float64); num != 2 {
		t.Errorf("num_events = %v, want 2 (root plus one reply): %s", num, remaining.Raw)
	}
	if relations := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/relations/"+firstRoot+"?rel_type=m.thread", alice.Token, nil,
		http.StatusOK); len(eventsOf(relations.Body["chunk"])) != 0 {
		t.Errorf("a redacted thread reply is still listed: %s", relations.Raw)
	}
	if annotated := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/event/"+firstRoot, alice.Token, nil, http.StatusOK); threadSummaryOf(t, annotated.Body) != nil {
		t.Errorf("the thread summary survived the redaction of its only reply: %s", annotated.Raw)
	}
	if timeline := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/event/"+firstReply, alice.Token, nil, http.StatusOK); !isRedacted(timeline.Body) {
		t.Errorf("the redacted reply left the timeline instead of staying emptied: %s", timeline.Raw)
	}
}

// isRedacted reports whether a decoded event body carries unsigned.redacted_because.
func isRedacted(body map[string]any) bool {
	unsigned, _ := body["unsigned"].(map[string]any)
	_, ok := unsigned["redacted_because"].(map[string]any)
	return ok
}

// threadSummaryOf returns the unsigned.m.thread block of a decoded event body,
// or nil when the event carries none.
func threadSummaryOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	unsigned, _ := body["unsigned"].(map[string]any)
	summary, _ := unsigned["m.thread"].(map[string]any)
	return summary
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

// TestHistoryVisibilityOpensTheRoomWhenTheUserArrived is MSC2675's second half. A room-level
// permission check answers "may this user read the room"; the room also decides *when its
// history opened*, and a room that chose `joined` or `invited` said that a member sees the
// events from the moment they arrived, not the ones that were there before. Without this half,
// a member who joined last week can ask for the room's first event by id and be answered - and
// a batch of those events can arrive unasked in a timeline filled from a peer.
func TestHistoryVisibilityOpensTheRoomWhenTheUserArrived(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "builders")
	carol := server.Register("carol", "kettle")

	setVisibility := func(roomID, visibility string) {
		server.MustDo(http.MethodPut,
			"/_matrix/client/v3/rooms/"+url.PathEscape(roomID)+"/state/m.room.history_visibility",
			alice.Token, map[string]any{"history_visibility": visibility}, http.StatusOK)
	}
	chunk := func(session *Session, roomID string) []map[string]any {
		resp := server.MustDo(http.MethodGet,
			"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=50", session.Token, nil, http.StatusOK)
		raw, _ := resp.Body["chunk"].([]any)
		events := make([]map[string]any, 0, len(raw))
		for _, entry := range raw {
			event, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			events = append(events, event)
		}
		return events
	}
	holds := func(events []map[string]any, eventID string) bool {
		for _, event := range events {
			if id, _ := event["event_id"].(string); id == eventID {
				return true
			}
		}
		return false
	}
	// summaryOf renders a page legibly, because an event id says nothing about what was shown.
	summaryOf := func(events []map[string]any) string {
		parts := make([]string, 0, len(events))
		for _, event := range events {
			text := ""
			if content, ok := event["content"].(map[string]any); ok {
				if body, ok := content["body"].(string); ok {
					text = `"` + body + `"`
				}
			}
			if text == "" {
				if key, ok := event["state_key"].(string); ok {
					text = "key " + key
				}
			}
			typ, _ := event["type"].(string)
			parts = append(parts, typ+" "+text)
		}
		return strings.Join(parts, ", ")
	}
	membershipOf := func(events []map[string]any, stateKey string) bool {
		for _, event := range events {
			key, _ := event["state_key"].(string)
			if event["type"] == matrix.TypeMember && key == stateKey {
				return true
			}
		}
		return false
	}

	// A room whose history opens at the join.
	joined := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Joined"})
	setVisibility(joined, matrix.HistoryJoined)
	before := server.Send(alice, joined, "hv-before", "spoken before bob arrived")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+joined+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+url.PathEscape(joined), bob.Token, nil, http.StatusOK)
	after := server.Send(alice, joined, "hv-after", "spoken after bob arrived")

	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+joined+"/event/"+before, bob.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("a joined-visibility room answered bob with a code that admits an event exists before his join: got %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+joined+"/event/"+after, bob.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("bob was refused an event from after his join: got %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+joined+"/event/"+before, alice.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("the creator was refused the room's own history: got %d %s", resp.Status, resp.Raw)
	}

	page := chunk(bob, joined)
	if holds(page, before) {
		t.Errorf("/messages showed bob an event from before his join: %s", summaryOf(page))
	}
	if !holds(page, after) {
		t.Errorf("/messages hid bob an event from after his join: %s", summaryOf(page))
	}
	// His own membership events stay his whatever the room decided: a client that cannot see
	// the event which ended or began a membership cannot explain the room to the user.
	if !membershipOf(page, bob.UserID) {
		t.Errorf("bob's own membership was hidden from him: %s", summaryOf(page))
	}

	// The same rule across a timeline that arrived in one batch: bob's first sync must not
	// carry the older event, which is how a room filled from a peer would otherwise hand it over.
	synced, _ := server.Sync(bob, "?timeout=0")
	if room, ok := synced.Rooms.Join[joined]; ok {
		for _, event := range room.Timeline.Events {
			if event.ID == before {
				t.Errorf("/sync carried an event from before bob's join: %s", event.ID)
			}
		}
	} else {
		t.Errorf("bob's sync has no join section for %s", joined)
	}

	// A room whose history opens at the invite: the invited user may read what is said to them
	// while they have not yet joined, and not what was said before.
	knocking := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Invited"})
	setVisibility(knocking, matrix.HistoryInvited)
	preInvite := server.Send(alice, knocking, "hv-pre", "spoken before the invite")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+knocking+"/invite", alice.Token,
		map[string]any{"user_id": bob.UserID}, http.StatusOK)
	duringInvite := server.Send(alice, knocking, "hv-during", "spoken while bob was only invited")

	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+knocking+"/event/"+preInvite, bob.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("an invited-visibility room answered bob with a code that admits an event exists before his invite: got %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+knocking+"/event/"+duringInvite, bob.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("an invited user was refused an event sent after the invite: got %d %s", resp.Status, resp.Raw)
	}
	if holds(chunk(bob, knocking), preInvite) {
		t.Errorf("/messages of an invited-visibility room showed bob a pre-invite event")
	}

	// A room open to the world answers a stranger; a room that said nothing (`shared`, the
	// default) does not. The difference is the room-level half, and it belongs to both.
	open := server.CreateRoom(alice, map[string]any{"preset": "private_chat", "name": "Open"})
	setVisibility(open, matrix.HistoryWorldReadable)
	public := server.Send(alice, open, "hv-open", "open to whoever asks")
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+open+"/event/"+public, carol.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("a world-readable room refused a stranger: got %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+joined+"/event/"+after, carol.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("a shared room answered a stranger with anything but not-found: got %d %s", resp.Status, resp.Raw)
	}

	// Search is a read of the same history: a match in a room that hides the event does not
	// entitle anybody to its text.
	found := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", bob.Token,
		map[string]any{"search_categories": map[string]any{
			"room_events": map[string]any{"search_key": map[string]any{"term": []string{"spoken before"}}},
		}}, http.StatusOK)
	{
		categories, _ := found.Body["categories"].(map[string]any)
		events, _ := categories["room_events"].(map[string]any)
		results, _ := events["results"].([]any)
		for _, raw := range results {
			hit, _ := raw.(map[string]any)
			event, _ := hit["event"].(map[string]any)
			if id, _ := event["event_id"].(string); id == before {
				t.Errorf("/search answered with an event hidden by the room's visibility: %s", found.Raw)
			}
		}
	}
}
