package integration

import (
	"net/http"
	"net/url"
	"testing"
)

// TestMessagesHonoursItsFilter asks /messages for a page shaped by a room event filter. The
// filter arrives here as the whole `filter` parameter — the same document that sits under
// `room.timeline` in a sync — and it used to be read for one flag, so everything else in it
// quietly did nothing and the client got the whole timeline it had asked to have trimmed.
func TestMessagesHonoursItsFilter(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, aliceToken, _ := h.register(t, "filer", "pw-filer", "")
	bob, bobToken, _ := h.register(t, "other", "pw-other", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)

	h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-text",
		`{"msgtype":"m.text","body":"a sentence"}`, aliceToken), http.StatusOK)
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-file",
		`{"msgtype":"m.file","body":"picture.png","url":"mxc://hs1/abcdef"}`, aliceToken), http.StatusOK)
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-bob",
		`{"msgtype":"m.text","body":"from bob"}`, bobToken), http.StatusOK)
	h.mustStatus(t, send(t, h, roomID, "m.room.topic", "txn-topic",
		`{"topic":"filtered"}`, aliceToken), http.StatusOK)

	// Each case says what a page must look like rather than listing every event, because the
	// room holds state events around the four that were sent and the page is bounded by the
	// default limit; what the filter decides is which events may be in it at all.
	for _, tc := range []struct {
		what   string
		filter string
		check  func(t *testing.T, events []map[string]any)
	}{
		{"contains_url", `{"contains_url":true}`, func(t *testing.T, events []map[string]any) {
			must := bodiesOf(events, "picture.png")
			if !must {
				t.Errorf(`contains_url:true did not keep the file message: %v`, pageSummary(events))
			}
			for _, event := range events {
				if !hasURLContent(event) {
					t.Errorf(`contains_url:true kept %v, which has no URL`, contentOf(event))
				}
			}
		}},
		{"contains_url false", `{"contains_url":false}`, func(t *testing.T, events []map[string]any) {
			if bodiesOf(events, "picture.png") {
				t.Errorf(`contains_url:false kept the file message: %v`, pageSummary(events))
			}
			for _, want := range []string{"from bob", "a sentence"} {
				if !bodiesOf(events, want) {
					t.Errorf(`contains_url:false lost the %q message: %v`, want, pageSummary(events))
				}
			}
		}},
		{"one sender", `{"senders":["` + alice + `"]}`, func(t *testing.T, events []map[string]any) {
			for _, event := range events {
				if sender, _ := event["sender"].(string); sender != alice {
					t.Errorf(`senders:[alice] kept an event from %q`, sender)
				}
			}
			if !bodiesOf(events, "a sentence") {
				t.Errorf(`senders:[alice] lost alice's message: %v`, pageSummary(events))
			}
			if bodiesOf(events, "from bob") {
				t.Errorf(`senders:[alice] kept bob's message: %v`, pageSummary(events))
			}
		}},
		{"not one sender", `{"not_senders":["` + alice + `"]}`, func(t *testing.T, events []map[string]any) {
			for _, event := range events {
				if sender, _ := event["sender"].(string); sender == alice {
					t.Errorf(`not_senders:[alice] kept an event from %q`, sender)
				}
			}
			if !bodiesOf(events, "from bob") {
				t.Errorf(`not_senders:[alice] lost bob's message: %v`, pageSummary(events))
			}
		}},
		{"only bobs messages", `{"senders":["` + bob + `"]}`, func(t *testing.T, events []map[string]any) {
			if !bodiesOf(events, "from bob") {
				t.Errorf(`senders:[bob] lost bob's message: %v`, pageSummary(events))
			}
			for _, event := range events {
				if sender, _ := event["sender"].(string); sender != bob {
					t.Errorf(`senders:[bob] kept an event from %q`, sender)
				}
			}
		}},
		// `m.file` is a msgtype, not an event type: a filter on the type is about the event,
		// and the file message is an m.room.message.
		{"only events of a type the room has none of", `{"types":["m.file"]}`,
			func(t *testing.T, events []map[string]any) {
				if len(events) != 0 {
					t.Errorf(`types:["m.file"] returned %v, want nothing`, pageSummary(events))
				}
			}},
		{"a room this filter is not for", `{"rooms":["!somewhereelse:hs1"]}`,
			func(t *testing.T, events []map[string]any) {
				if len(events) != 0 {
					t.Errorf("rooms:[another room] returned %d events from this room", len(events))
				}
			}},
	} {
		page := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+
			"/messages?dir=b&filter="+url.QueryEscape(tc.filter), aliceToken), http.StatusOK).decoded(t)
		chunk, _ := page["chunk"].([]any)
		events := make([]map[string]any, 0, len(chunk))
		for _, entry := range chunk {
			if event, ok := entry.(map[string]any); ok {
				events = append(events, event)
			}
		}
		if len(events) != len(chunk) {
			t.Fatalf("%s: a chunk entry is not an object", tc.what)
		}
		tc.check(t, events)
	}

	// A filtered page still hands back the token to continue from, because events the
	// client did not ask for are still further back in the room; and following that token
	// cannot return what the filter had already excluded.
	page := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+
		"/messages?dir=b&filter="+url.QueryEscape(`{"contains_url":true}`), aliceToken), http.StatusOK).decoded(t)
	end, _ := page["end"].(string)
	if end == "" {
		t.Fatalf("a page whose filter left unasked-for events behind should still say where to continue: %v", page)
	}
	next := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+
		"/messages?dir=b&from="+end+"&filter="+url.QueryEscape(`{"contains_url":true}`), aliceToken),
		http.StatusOK).decoded(t)
	if bodies := pageBodies(t, next["chunk"]); len(bodies) != 0 {
		t.Errorf("following the filtered page's end token returned %v, want nothing further", bodies)
	}
}

// TestMessagesRefusesAFilterItCannotRead covers the same shape check the sync filter door runs,
// at the door where the filter is the whole parameter.
func TestMessagesRefusesAFilterItCannotRead(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, token, _ := h.register(t, "checker", "pw-checker", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})

	for _, bad := range []struct{ what, filter string }{
		{"types is not a list", `{"types":"m.room.message"}`},
		{"types holds numbers", `{"types":[7]}`},
		{"senders holds a string that is not a user ID", `{"senders":["not_a_sender_id"]}`},
		{"rooms holds a string that is not a room ID", `{"rooms":["not_a_room_id"]}`},
		{"contains_url is not a boolean", `{"contains_url":"yes"}`},
		{"limit is not a number", `{"limit":"five"}`},
		{"the filter is not an object", `"m.room.message"`},
	} {
		res := h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&filter="+
			url.QueryEscape(bad.filter), token)
		if res.status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (body %s)", bad.what, res.status, res.body)
		}
		if code := res.errcode(t); code != "M_INVALID_PARAM" {
			t.Errorf("%s answered %s, want M_INVALID_PARAM (body %s)", bad.what, code, res.body)
		}
	}
	// A filter in words this server reads is applied, not refused: `alice` wrote none of the
	// state in this room after creating it, so filtering to her alone still leaves the room's
	// messages.
	page := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&filter="+
		url.QueryEscape(`{"senders":["`+alice+`"],"contains_url":false}`), token), http.StatusOK)
	if _, ok := page.decoded(t)["chunk"]; !ok {
		t.Errorf("a readable filter answered without a chunk: %s", page.body)
	}
}

// contentOf, bodies and bodiesOf read a page in the words a test can compare.
func contentOf(event map[string]any) map[string]any {
	content, _ := event["content"].(map[string]any)
	if content == nil {
		return map[string]any{}
	}
	return content
}

func pageSummary(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		eventType, _ := event["type"].(string)
		body, _ := contentOf(event)["body"].(string)
		out = append(out, eventType+" "+body)
	}
	return out
}

func bodiesOf(events []map[string]any, want string) bool {
	for _, event := range events {
		if body, _ := contentOf(event)["body"].(string); body == want {
			return true
		}
	}
	return false
}

func hasURLContent(event map[string]any) bool {
	urlValue, _ := contentOf(event)["url"].(string)
	return urlValue != ""
}

// pageBodies reads the `body` of each event in a page, which is the plainest way to see which
// events arrived.
func pageBodies(t *testing.T, chunk any) []string {
	t.Helper()
	entries, _ := chunk.([]any)
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		event, _ := entry.(map[string]any)
		content, _ := event["content"].(map[string]any)
		body, _ := content["body"].(string)
		out = append(out, body)
	}
	return out
}

// TestSyncFilterKeepsTheEventsWithAURL runs the same flag through the other door, where it sits
// inside a larger document. A client that asks for a room's media in a sync and is sent its
// conversation instead has been told nothing about why the answer is bigger than the question.
func TestSyncFilterKeepsTheEventsWithAURL(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "syncfilter", "pw-syncfilter", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})
	_, since := syncNow(t, h, token, "")

	h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-say",
		`{"msgtype":"m.text","body":"a sentence"}`, token), http.StatusOK)
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-show",
		`{"msgtype":"m.file","body":"picture.png","url":"mxc://hs1/ghijkl"}`, token), http.StatusOK)

	body, _ := syncNow(t, h, token, "&since="+since+"&filter="+url.QueryEscape(
		`{"room":{"timeline":{"contains_url":true},"state":{"types":[]}}}`))
	room := roomSection(t, body, "join", roomID)
	timeline, _ := room["timeline"].(map[string]any)
	entries, _ := timeline["events"].([]any)
	events := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if event, ok := entry.(map[string]any); ok {
			events = append(events, event)
		}
	}
	if len(events) != 1 {
		t.Fatalf("the filtered timeline holds %v, want only the file message", pageSummary(events))
	}
	if sent, _ := contentOf(events[0])["body"].(string); sent != "picture.png" {
		t.Errorf("the filtered timeline returned %q, want the file message", sent)
	}
	if state, _ := room["state"].(map[string]any); state != nil {
		if entries, _ := state["events"].([]any); len(entries) != 0 {
			t.Errorf(`a state filter of "types":[] was answered with %d state events`, len(entries))
		}
	}
	// The same sync without the filter still sees the conversation, so the difference above
	// is the filter working and not a timeline that went missing.
	unfiltered, _ := syncNow(t, h, token, "&since="+since)
	unfilteredRoom := roomSection(t, unfiltered, "join", roomID)
	unfilteredTimeline, _ := unfilteredRoom["timeline"].(map[string]any)
	plainEntries, _ := unfilteredTimeline["events"].([]any)
	if len(plainEntries) != 2 {
		t.Fatalf("the unfiltered timeline holds %d events, want the two messages", len(plainEntries))
	}
}
