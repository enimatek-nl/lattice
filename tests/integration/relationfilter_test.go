package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// TestEventsCanBeFilteredByWhatTheyRelateTo asks the question MSC3874 exists for: show me only
// the events that claim a relation of a certain kind. The lists arrive under their namespaced
// names, and the same document is read on both doors that take a filter — `/messages` and a
// sync's timeline — because a filter that means one thing on one door and another thing on the
// next is not a filter.
func TestEventsCanBeFilteredByWhatTheyRelateTo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "relfilter", "pw-relfilter", "")
	roomID := createRoom(t, h, token, map[string]any{"preset": "public_chat"})

	root := sendText(t, h, roomID, token, "txn-root", "the root")
	sendRelated(t, h, roomID, token, "txn-thread", "m.room.message", fmt.Sprintf(
		`{"msgtype":"m.text","body":"in the thread","m.relates_to":{"event_id":%q,"rel_type":"m.thread"}}`, root))
	sendRelated(t, h, roomID, token, "txn-reference", "m.room.message", fmt.Sprintf(
		`{"msgtype":"m.text","body":"pointing at it","m.relates_to":{"event_id":%q,"rel_type":"m.reference"}}`, root))
	// A reply written the old way carries m.relates_to with no rel_type. It is not a relation of
	// a kind, so it belongs with the events that have none: kept by not_rel_types, dropped by
	// rel_types.
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-plainreply", fmt.Sprintf(
		`{"msgtype":"m.text","body":"a plain reply","m.relates_to":{"m.in_reply_to":{"event_id":%q}}}`,
		root), token), http.StatusOK)

	for _, tc := range []struct {
		what       string
		filter     string
		wantBodies []string
		empty      bool
	}{
		{"only threads", `{"org.matrix.msc3874.rel_types":["m.thread"]}`,
			[]string{"in the thread"}, false},
		{"only references", `{"org.matrix.msc3874.rel_types":["m.reference"]}`,
			[]string{"pointing at it"}, false},
		{"threads or references", `{"org.matrix.msc3874.rel_types":["m.thread","m.reference"]}`,
			[]string{"in the thread", "pointing at it"}, false},
		// Not having a relation is not the same as having a relation that is not named: an event
		// pointing at nothing survives every not_rel_types list.
		{"not threads", `{"org.matrix.msc3874.not_rel_types":["m.thread"]}`,
			[]string{"the root", "pointing at it", "a plain reply"}, false},
		{"not references", `{"org.matrix.msc3874.not_rel_types":["m.reference"]}`,
			[]string{"the root", "in the thread", "a plain reply"}, false},
		{"neither kind", `{"org.matrix.msc3874.not_rel_types":["m.thread","m.reference"]}`,
			[]string{"the root", "a plain reply"}, false},
		// A list that names a kind the room has none of is a question with no answer, not a
		// filter that was ignored.
		{"a kind this room has none of", `{"org.matrix.msc3874.rel_types":["m.nonesuch"]}`,
			nil, true},
		// A list present and empty is the whole answer, the same way an empty `types` is.
		{"an empty list", `{"org.matrix.msc3874.rel_types":[]}`, nil, true},
	} {
		page := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+
			"/messages?dir=b&limit=50&filter="+url.QueryEscape(tc.filter), token), http.StatusOK).decoded(t)
		chunk, _ := page["chunk"].([]any)
		events := make([]map[string]any, 0, len(chunk))
		for _, entry := range chunk {
			if event, ok := entry.(map[string]any); ok {
				events = append(events, event)
			}
		}
		for _, want := range tc.wantBodies {
			if !bodiesOf(events, want) {
				t.Errorf("%s: %q lost its body; the page holds %v", tc.what, want, pageSummary(events))
			}
		}
		if tc.empty {
			if len(events) != 0 {
				t.Errorf("%s: the page holds %v, want nothing", tc.what, pageSummary(events))
			}
			continue
		}
		for _, event := range events {
			body, _ := contentOf(event)["body"].(string)
			if body == "" {
				continue // state events carry no body and are judged by the empty case above
			}
			found := false
			for _, want := range tc.wantBodies {
				if body == want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: the page kept %q, which the filter excludes", tc.what, body)
			}
		}
	}

	// The other door, the same document: a sync's timeline narrowed to one relation kind.
	body := syncBody(t, h, token, "&filter="+url.QueryEscape(
		`{"room":{"timeline":{"org.matrix.msc3874.rel_types":["m.thread"]}}}`))
	join := roomSection(t, body, "join", roomID)
	timeline, _ := join["timeline"].(map[string]any)
	entries, _ := timeline["events"].([]any)
	if len(entries) == 0 {
		t.Fatalf("a timeline filtered to m.thread came back empty; the room holds four messages")
	}
	for _, entry := range entries {
		event, _ := entry.(map[string]any)
		bodyValue, _ := contentOf(event)["body"].(string)
		if bodyValue == "" {
			continue
		}
		if bodyValue != "in the thread" {
			t.Errorf(`a sync timeline filtered to m.thread kept %q; want only "in the thread"`, bodyValue)
		}
	}

	// The lists are checked where they are read, not waved through: a string where a list of
	// relation types belongs is the client's mistake and says so.
	bad := h.get("/_matrix/client/v3/rooms/"+roomID+
		"/messages?filter="+url.QueryEscape(`{"org.matrix.msc3874.rel_types":"m.thread"}`), token)
	if bad.status != http.StatusBadRequest {
		t.Errorf("a rel_types list that is not a list answered %d (%s), want 400", bad.status, bad.body)
	}
	if code := errcodeOf(t, bad); code != "M_INVALID_PARAM" {
		t.Errorf("a rel_types list that is not a list answered %s, want M_INVALID_PARAM", code)
	}
}
