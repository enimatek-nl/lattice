package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// relationIDs reads the event IDs a /relations page lists, in the order it lists them.
func relationIDs(t *testing.T, res response) []string {
	t.Helper()
	if res.status != http.StatusOK {
		t.Fatalf("a relations request answered %d: %s", res.status, res.body)
	}
	body := res.decoded(t)
	chunk, _ := body["chunk"].([]any)
	ids := make([]string, 0, len(chunk))
	for _, item := range chunk {
		event, _ := item.(map[string]any)
		id, _ := event["event_id"].(string)
		if id == "" {
			t.Fatalf("a relation in the page carries no event_id: %v", event)
		}
		ids = append(ids, id)
	}
	return ids
}

// sendRelated sends an event that claims to relate to another one, and returns its ID.
func sendRelated(t *testing.T, h *harness, roomID, token, txn, eventType, body string) string {
	t.Helper()
	res := h.mustStatus(t, send(t, h, roomID, eventType, txn, body, token), http.StatusOK)
	id, _ := res.decoded(t)["event_id"].(string)
	if id == "" {
		t.Fatalf("send %s returned no event_id: %s", txn, res.body)
	}
	return id
}

// TestWhatRelatesToAnEventIsListed asks the question `/relations` exists for: what points at
// this event. Three different kinds of relation are put on one root, and the two path filters
// are checked against them, because a thread view and a reaction list are the same read with
// different filters and one of them silently ignoring a filter would look like a working server.
func TestWhatRelatesToAnEventIsListed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "relator", "pw-relator", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})

	root := sendText(t, h, roomID, token, "root", "the root")
	thread := sendRelated(t, h, roomID, token, "thread-reply", "m.room.message", fmt.Sprintf(
		`{"msgtype":"m.text","body":"in the thread","m.relates_to":{"event_id":%q,"rel_type":"m.thread"}}`, root))
	dummy := sendRelated(t, h, roomID, token, "thread-dummy", "m.dummy", fmt.Sprintf(
		`{"m.relates_to":{"event_id":%q,"rel_type":"m.thread"}}`, root))
	edit := sendRelated(t, h, roomID, token, "edit", "m.room.message", fmt.Sprintf(
		`{"msgtype":"m.text","body":"* edited","m.new_content":{"msgtype":"m.text","body":"edited"},`+
			`"m.relates_to":{"event_id":%q,"rel_type":"m.replace"}}`, root))
	// A reply without rel_type is a fallback, not a relation of this kind, and an event in the
	// room that relates to nothing is not a relation either. Neither belongs in the page.
	sendRelated(t, h, roomID, token, "plain-reply", "m.room.message", fmt.Sprintf(
		`{"msgtype":"m.text","body":"plain reply","m.relates_to":{"event_id":%q}}`, root))
	sendText(t, h, roomID, token, "unrelated", "nothing to do with the root")

	base := "/_matrix/client/v1/rooms/" + roomID + "/relations/"
	got := relationIDs(t, h.get(base+root, token))
	if len(got) != 3 {
		t.Fatalf("the root has %d relations (%v), want the thread reply, the dummy and the edit", len(got), got)
	}
	for _, want := range []string{thread, dummy, edit} {
		if !hasRelationID(got, want) {
			t.Errorf("%s is not listed among the root's relations %v", want, got)
		}
	}

	// Filtered by relation type: the two thread relations, and not the edit.
	got = relationIDs(t, h.get(base+root+"/m.thread", token))
	if len(got) != 2 || !hasRelationID(got, thread) || !hasRelationID(got, dummy) {
		t.Errorf("m.thread relations of the root are %v, want %s and %s", got, thread, dummy)
	}

	// Filtered by relation type and event type: the dummy is an m.dummy, so only the reply is
	// left. A server that ignored the second segment would answer two here.
	got = relationIDs(t, h.get(base+root+"/m.thread/m.room.message", token))
	if len(got) != 1 || got[0] != thread {
		t.Errorf("m.thread relations of type m.room.message are %v, want only %s", got, thread)
	}

	// A relation type this server has never heard of is not an error and not a wildcard: it is
	// a name, and the honest answer is that nothing in the room carries it.
	if got := relationIDs(t, h.get(base+root+"/msc9999.not_a_thing", token)); len(got) != 0 {
		t.Errorf("an unknown relation type listed %v, want nothing", got)
	}

	// An event nothing relates to answers an empty page rather than a 404: saying "no such
	// event" would report which event identifiers are in use in this room.
	if got := relationIDs(t, h.get(base+dummy, token)); len(got) != 0 {
		t.Errorf("the thread reply has relations of its own: %v", got)
	}

	// An unknown event identifier in a room the requester is in is the same empty page.
	if got := relationIDs(t, h.get(base+"$nothing:hs1", token)); len(got) != 0 {
		t.Errorf("relations of an unknown event are %v, want nothing", got)
	}
}

// hasRelationID answers whether an ID appears in a page's list.
func hasRelationID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestRelationsPaginateInBothDirections checks the tokens a thread view is built from. The
// order matters here in a way it does not on the first test: a page of three out of ten replies
// is only right if it is the three the direction asked for, and the token has to name where this
// page stopped rather than where it started.
func TestRelationsPaginateInBothDirections(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "pager", "pw-pager", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})

	root := sendText(t, h, roomID, token, "root", "the root")
	replies := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		replies = append(replies, sendRelated(t, h, roomID, token, fmt.Sprintf("reply-%d", i),
			"m.room.message", fmt.Sprintf(
				`{"msgtype":"m.text","body":"reply %d","m.relates_to":{"event_id":%q,"rel_type":"m.thread"}}`, i, root)))
	}

	base := "/_matrix/client/v1/rooms/" + roomID + "/relations/" + root
	// Backwards is the default, and the default page is the newest of them.
	first := h.get(base+"?limit=3", token)
	got := relationIDs(t, first)
	page := first.decoded(t)
	want := []string{replies[9], replies[8], replies[7]}
	if !sameIDs(got, want) {
		t.Fatalf("the first backwards page is %v, want the three newest %v", got, want)
	}
	next, _ := page["next_batch"].(string)
	if next == "" {
		t.Fatalf("a page of three out of ten answers no next_batch: %v", page)
	}
	got = relationIDs(t, h.get(base+"?limit=3&from="+next, token))
	want = []string{replies[6], replies[5], replies[4]}
	if !sameIDs(got, want) {
		t.Errorf("the second backwards page is %v, want %v", got, want)
	}

	// Forwards from the beginning.
	forwardRes := h.get(base+"?limit=3&dir=f", token)
	got = relationIDs(t, forwardRes)
	forward := forwardRes.decoded(t)
	want = []string{replies[0], replies[1], replies[2]}
	if !sameIDs(got, want) {
		t.Errorf("the first forwards page is %v, want the three oldest %v", got, want)
	}
	next, _ = forward["next_batch"].(string)
	if next == "" {
		t.Fatalf("a forwards page of three out of ten answers no next_batch: %v", forward)
	}
	got = relationIDs(t, h.get(base+"?limit=3&dir=f&from="+next, token))
	want = []string{replies[3], replies[4], replies[5]}
	if !sameIDs(got, want) {
		t.Errorf("the second forwards page is %v, want %v", got, want)
	}

	// Walking backwards to the beginning has to stop saying there is more, or a client loops.
	last := h.get(base+"?limit=20", token).decoded(t)
	if token, more := last["next_batch"]; more && token != "" {
		t.Errorf("a page holding all ten relations still answers next_batch=%v", token)
	}
	if ids := relationIDs(t, h.get(base+"?limit=20", token)); len(ids) != 10 {
		t.Errorf("limit 20 returned %d relations, want all ten", len(ids))
	}

	// A token from a different read is still a position in this room's stream, and a nonsense
	// one is said plainly.
	bad := h.get(base+"?from=not-a-token", token)
	if bad.status != http.StatusBadRequest {
		t.Errorf("a pagination token that is not a position answered %d (%s), want 400", bad.status, bad.body)
	}
	if bad := h.get(base+"?dir=sideways", token); bad.status != http.StatusBadRequest {
		t.Errorf("an unknown direction answered %d (%s), want 400", bad.status, bad.body)
	}
}

// TestRelationsRespectWhoMayReadTheRoom checks the entitlement on the new read. A relation is a
// fact about a room's history, so the history rule has to apply to it, and a redaction takes the
// content that made the claim — which means the index has to follow the redaction rather than
// outlive it.
func TestRelationsRespectWhoMayReadTheRoom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "owner", "pw-owner", "")
	_, outsiderToken, _ := h.register(t, "outsider", "pw-outsider", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "private_chat"})

	root := sendText(t, h, roomID, token, "root", "the root")
	reply := sendRelated(t, h, roomID, token, "reply", "m.room.message", fmt.Sprintf(
		`{"msgtype":"m.text","body":"reply","m.relates_to":{"event_id":%q,"rel_type":"m.thread"}}`, root))

	base := "/_matrix/client/v1/rooms/" + roomID + "/relations/"
	if ids := relationIDs(t, h.get(base+root, token)); !hasRelationID(ids, reply) {
		t.Fatalf("the member cannot read the room's relations: %v", ids)
	}
	// Someone who has never been in a private room does not learn what relates to what in it.
	res := h.get(base+root, outsiderToken)
	if res.status != http.StatusForbidden {
		t.Errorf("an outsider's relations request answered %d (%s), want 403", res.status, res.body)
	}

	// Redacting the reply takes its content, and the relation was content. Listing it
	// afterwards would show a client an event as a reply when the client is no longer allowed
	// to see that it is one.
	res = h.mustStatus(t, h.raw(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/redact/"+reply+"/txn-redact", []byte(`{"reason":"testing"}`), token),
		http.StatusOK)
	var redacted struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(res.body, &redacted); err != nil {
		t.Fatalf("the redaction response is not JSON: %s", res.body)
	}
	if redacted.EventID == "" {
		t.Fatalf("the redaction returned no event_id: %s", res.body)
	}
	if ids := relationIDs(t, h.get(base+root, token)); hasRelationID(ids, reply) {
		t.Errorf("a redacted reply is still listed as a relation of %s: %v", root, ids)
	}
	// The root is untouched, and so is the count of what is left.
	if ids := relationIDs(t, h.get(base+root, token)); len(ids) != 0 {
		t.Errorf("after the only reply was redacted the page holds %v", ids)
	}
}

// sameIDs compares two pages of event IDs in order.
func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// threadEntry is one thread as the list presents it: the root event itself, with the thread's
// last word and its count inside `unsigned.m.relations.m.thread`.
type threadEntry struct {
	EventID       string
	Sender        string
	LatestEventID string
	Count         float64
	Participated  bool
	raw           map[string]any
}

// threadPage reads a /threads response.
func threadPage(t *testing.T, res response) ([]threadEntry, map[string]any) {
	t.Helper()
	if res.status != http.StatusOK {
		t.Fatalf("a threads request answered %d: %s", res.status, res.body)
	}
	body := res.decoded(t)
	chunk, _ := body["chunk"].([]any)
	entries := make([]threadEntry, 0, len(chunk))
	for _, item := range chunk {
		event, _ := item.(map[string]any)
		if event == nil {
			t.Fatalf("a thread in the chunk is not an event: %v", item)
		}
		entry := threadEntry{raw: event}
		entry.EventID, _ = event["event_id"].(string)
		entry.Sender, _ = event["sender"].(string)
		unsigned, _ := event["unsigned"].(map[string]any)
		if unsigned == nil {
			t.Fatalf("a thread root carries no unsigned aggregation: %v", event)
		}
		relations, _ := unsigned["m.relations"].(map[string]any)
		thread, _ := relations["m.thread"].(map[string]any)
		if thread == nil {
			t.Fatalf("a thread root carries no unsigned.m.relations.m.thread: %v", event)
		}
		latest, _ := thread["latest_event"].(map[string]any)
		if latest == nil {
			t.Fatalf("the thread aggregation carries no latest_event: %v", thread)
		}
		entry.LatestEventID, _ = latest["event_id"].(string)
		entry.Count, _ = thread["count"].(float64)
		entry.Participated, _ = thread["current_user_participated"].(bool)
		entries = append(entries, entry)
	}
	return entries, body
}

// TestTheRoomThreadListFollowsTheNewestReply checks the question `/threads` answers: which
// conversations are live. The ordering is the point — a thread is listed where its newest event
// sits, not where it started — along with the two things a client shows next to it: which event
// is the last word, and how many there are.
func TestTheRoomThreadListFollowsTheNewestReply(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "threader", "pw-threader", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})

	threadOne := sendText(t, h, roomID, token, "root-1", "thread one root")
	threadTwo := sendText(t, h, roomID, token, "root-2", "thread two root")
	sendRelated(t, h, roomID, token, "reply-1", "m.room.message", fmt.Sprintf(
		`{"msgtype":"m.text","body":"in one","m.relates_to":{"event_id":%q,"rel_type":"m.thread"}}`, threadOne))
	replyTwo := sendRelated(t, h, roomID, token, "reply-2", "m.room.message", fmt.Sprintf(
		`{"msgtype":"m.text","body":"in two","m.relates_to":{"event_id":%q,"rel_type":"m.thread"}}`, threadTwo))
	// A root nobody has replied to is not a thread yet: nothing points at it, so there is no
	// newest event to order it by.
	sendText(t, h, roomID, token, "root-alone", "never a thread")

	entries, _ := threadPage(t, h.get("/_matrix/client/v1/rooms/"+roomID+"/threads", token))
	if len(entries) != 2 {
		t.Fatalf("the room lists %d threads, want the two that have replies", len(entries))
	}
	if entries[0].EventID != threadTwo {
		t.Errorf("the newest thread is %s, want the one replied to last (%s)", entries[0].EventID, threadTwo)
	}
	if entries[0].LatestEventID != replyTwo {
		t.Errorf("thread two's last word is %s, want %s", entries[0].LatestEventID, replyTwo)
	}
	if entries[0].Count != 1 {
		t.Errorf("thread two reports a count of %v, want 1", entries[0].Count)
	}
	if entries[1].EventID != threadOne {
		t.Errorf("the older thread is %s, want %s", entries[1].EventID, threadOne)
	}

	// Replying to the older thread moves it to the top, and its total with it.
	replyThree := sendRelated(t, h, roomID, token, "reply-3", "m.room.message", fmt.Sprintf(
		`{"msgtype":"m.text","body":"back in one","m.relates_to":{"event_id":%q,"rel_type":"m.thread"}}`, threadOne))
	entries, _ = threadPage(t, h.get("/_matrix/client/v1/rooms/"+roomID+"/threads", token))
	if entries[0].EventID != threadOne {
		t.Fatalf("after a reply to thread one it is not at the top: %s", entries[0].EventID)
	}
	if entries[0].LatestEventID != replyThree {
		t.Errorf("thread one's last word is %s, want %s", entries[0].LatestEventID, replyThree)
	}
	if entries[0].Count != 2 {
		t.Errorf("thread one reports a count of %v, want 2", entries[0].Count)
	}

	// Whether the reader has taken part in a thread is the other thing a client highlights, and
	// it is asked per reader: alice started both threads, so both are hers; someone who only
	// joined and never spoke in them sees neither.
	entries, _ = threadPage(t, h.get("/_matrix/client/v1/rooms/"+roomID+"/threads", token))
	for _, entry := range entries {
		if !entry.Participated {
			t.Errorf("the reader who started and replied in %s is told they did not take part", entry.EventID)
		}
	}
	bob, bobToken, _ := h.register(t, "lurker", "pw-lurker", "")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bobToken), http.StatusOK)
	entries, _ = threadPage(t, h.get("/_matrix/client/v1/rooms/"+roomID+"/threads", bobToken))
	if len(entries) != 2 {
		t.Fatalf("%s sees %d threads, want both", bob, len(entries))
	}
	for _, entry := range entries {
		if entry.Participated {
			t.Errorf("a reader who never spoke in %s is told they did", entry.EventID)
		}
	}

	// The MSC3440 type lists choose which events count as part of a thread, so they decide
	// which threads there are at all.
	entries, _ = threadPage(t, h.get("/_matrix/client/v1/rooms/"+roomID+"/threads?include=m.dummy", token))
	if len(entries) != 0 {
		t.Errorf("including only m.dummy lists %d threads, want none", len(entries))
	}
	entries, _ = threadPage(t, h.get("/_matrix/client/v1/rooms/"+roomID+"/threads?exclude=m.room.message", token))
	if len(entries) != 0 {
		t.Errorf("excluding m.room.message lists %d threads, want none", len(entries))
	}

	// Paging walks the list in order and stops promising more at the end.
	entries, body := threadPage(t, h.get("/_matrix/client/v1/rooms/"+roomID+"/threads?limit=1", token))
	if len(entries) != 1 {
		t.Fatalf("limit=1 returned %d threads", len(entries))
	}
	next, _ := body["next_batch"].(string)
	if next == "" {
		t.Fatalf("one thread out of two answers no next_batch: %v", body)
	}
	entries, body = threadPage(t, h.get("/_matrix/client/v1/rooms/"+roomID+"/threads?limit=1&from="+next, token))
	if len(entries) != 1 {
		t.Fatalf("the second page holds %d threads, want the other one", len(entries))
	}
	if entries[0].EventID != threadTwo {
		t.Errorf("the second page lists %s, want %s", entries[0].EventID, threadTwo)
	}
	if more, ok := body["next_batch"]; ok && more != "" {
		t.Errorf("the last page still promises next_batch=%v", more)
	}

	// A nonsense token and a nonsense type list are said plainly.
	if res := h.get("/_matrix/client/v1/rooms/"+roomID+"/threads?from=not-a-token", token); res.status != http.StatusBadRequest {
		t.Errorf("a bad pagination token answered %d (%s), want 400", res.status, res.body)
	}
	// A type that is well formed but unknown is not an error: it is a name this room happens
	// not to use, and the answer is an empty list. A type that is not a name at all is the
	// client's mistake, and says so.
	if res := h.get("/_matrix/client/v1/rooms/"+roomID+"/threads?include=m.room.messege", token); res.status != http.StatusOK {
		t.Errorf("an unknown but well formed include answered %d (%s), want 200 with an empty list", res.status, res.body)
	}
	if res := h.get("/_matrix/client/v1/rooms/"+roomID+"/threads?include=m.", token); res.status != http.StatusBadRequest {
		t.Errorf("an m. event type with no name answered %d (%s), want 400", res.status, res.body)
	}
	// MSC3440's own names are not type names and are not refused: ".*" asks for every event in
	// a thread, and ".*non-timeline" asks about events this server's thread index never holds.
	if entries, _ := threadPage(t, h.get("/_matrix/client/v1/rooms/"+roomID+"/threads?include=.*", token)); len(entries) != 2 {
		t.Errorf("include=.* lists %d threads, want both", len(entries))
	}
	res := h.get("/_matrix/client/v1/rooms/"+roomID+"/threads?exclude=.*non-timeline", token)
	if res.status != http.StatusOK {
		t.Errorf("exclude=.*non-timeline answered %d (%s), want 200", res.status, res.body)
	}
}
