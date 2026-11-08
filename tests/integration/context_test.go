package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// `/context` is the read a client reaches for after it has been pointed at one event — by a
// notification, by a permalink, by `timestamp_to_event` — and wants to know what was happening
// around it. Two things make it more than `/messages` with a filter, and both are tested here:
//
//   - the two tokens, which have to be the same tokens `/messages` takes and have to continue the
//     timeline rather than repeat the window the client has just been given; and
//   - the limit, which the specification applies to the two sides *together*, and which may legally
//     be zero — a caller that wants the event and its neighbourhood's address and nothing else.
//
// The window itself is produced by `/messages` underneath (internal/rooms/context.go says why: a
// second implementation of "what may this reader see" would drift), so these tests also check that
// the two endpoints cannot disagree about the same event.

// contextPage is the response of GET /rooms/{roomID}/context/{eventID}.
type contextPage struct {
	Event        map[string]any   `json:"event"`
	EventsBefore []map[string]any `json:"events_before"`
	EventsAfter  []map[string]any `json:"events_after"`
	State        []map[string]any `json:"state"`
	Start        string           `json:"start"`
	End          string           `json:"end"`
}

func readContext(t *testing.T, h *harness, token, roomID, eventID, query string) (contextPage, response) {
	t.Helper()
	path := "/_matrix/client/v1/rooms/" + url.PathEscape(roomID) + "/context/" + url.PathEscape(eventID)
	if query != "" {
		path += "?" + query
	}
	res := h.get(path, token)
	var page contextPage
	if res.status == http.StatusOK {
		if err := json.Unmarshal(res.body, &page); err != nil {
			t.Fatalf("decoding the context of %s: %v (%s)", eventID, err, res.body)
		}
	}
	return page, res
}

func eventIDsOf(t *testing.T, events []map[string]any) []string {
	t.Helper()
	out := make([]string, 0, len(events))
	for _, event := range events {
		id, _ := event["event_id"].(string)
		out = append(out, id)
	}
	return out
}

// fiveMessageRoom makes a room whose middle is worth asking about: create, A, B, C, then the
// state-heavy messages at the end, with B as the event the tests centre on.
func fiveMessageRoom(t *testing.T, h *harness, token, roomID string) (a, b, c string) {
	t.Helper()
	a = sendText(t, h, roomID, token, "ctx-a", "A")
	b = sendText(t, h, roomID, token, "ctx-b", "B")
	c = sendText(t, h, roomID, token, "ctx-c", "C")
	return
}

func TestTheContextOfAnEventIsTheWindowAroundIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "ctx-alice", "pw-ctx", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	a, b, c := fiveMessageRoom(t, h, aliceToken, roomID)

	page, res := readContext(t, h, aliceToken, roomID, b, "")
	h.mustStatus(t, res, http.StatusOK)

	if got, _ := page.Event["event_id"].(string); got != b {
		t.Fatalf("the window is centred on %q, want %q", got, b)
	}
	// `events_before` is reverse-chronological and `events_after` chronological, which the
	// specification states and a client rendering a window depends on: one array reads backwards
	// from the centre and the other forwards.
	before := eventIDsOf(t, page.EventsBefore)
	after := eventIDsOf(t, page.EventsAfter)
	if len(before) < 1 || before[0] != a {
		t.Errorf("events_before = %v, want the newest first with %q at the front", before, a)
	}
	if len(after) < 1 || after[0] != c {
		t.Errorf("events_after = %v, want %q first (chronological)", after, c)
	}
	for _, id := range append(append([]string{}, before...), after...) {
		if id == b {
			t.Errorf("the requested event appears in a side array as well as in `event`")
		}
	}
	if len(page.State) == 0 {
		t.Errorf("the window came with no state, so a client cannot render it")
	}

	t.Run("the tokens continue the timeline rather than repeat the window", func(t *testing.T) {
		// `start` is for walking backwards and `end` for walking forwards, with `/messages`. The
		// failure this is aimed at is a client that jumps, gets five events, paginates, and is
		// shown the same five again — or is shown nothing and concludes the room is empty.
		back := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&from="+url.QueryEscape(page.Start), aliceToken),
			http.StatusOK).decoded(t)
		chunk, _ := back["chunk"].([]any)
		if len(chunk) == 0 {
			t.Fatalf("paginating backwards from the window's `start` gives nothing")
		}
		for _, entry := range chunk {
			event, _ := entry.(map[string]any)
			if id, _ := event["event_id"].(string); id == b {
				t.Errorf("walking back from `start` returns the event the window was about again")
			}
		}

		forward := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=f&from="+url.QueryEscape(page.End), aliceToken),
			http.StatusOK).decoded(t)
		forwardChunk, _ := forward["chunk"].([]any)
		for _, entry := range forwardChunk {
			event, _ := entry.(map[string]any)
			if id, _ := event["event_id"].(string); id == b {
				t.Errorf("walking forward from `end` returns the event the window was about again")
			}
		}
		// `end` must keep naming something further along while the room has events after the
		// window; a window at the very end of a room is the case where it may not.
		latest, _ := readContext(t, h, aliceToken, roomID, c, "")
		if latest.End == "" {
			t.Errorf("the last event's window omits `end`; a client reads that as nothing further")
		}
	})

	t.Run("the limit covers both sides together", func(t *testing.T) {
		// The specification applies `limit` to the sum of the two arrays. Read as one limit each,
		// a client asking for ten events gets twenty, which matters most to the client that asked
		// for a small window precisely because it is drawing a small thing.
		page, _ := readContext(t, h, aliceToken, roomID, b, "limit=1")
		total := len(page.EventsBefore) + len(page.EventsAfter)
		if total > 1 {
			t.Errorf("limit 1 returned %d context events (%v before, %v after)",
				total, eventIDsOf(t, page.EventsBefore), eventIDsOf(t, page.EventsAfter))
		}
		if len(page.EventsBefore)+len(page.EventsAfter) == 0 {
			t.Errorf("limit 1 returned no window at all")
		}
		if got, _ := page.Event["event_id"].(string); got != b {
			t.Errorf("the requested event must always be returned, got %q", got)
		}
	})

	t.Run("limit 0 asks for the event and its address only", func(t *testing.T) {
		// Complement's own use of this endpoint: jump to an event, take a token, and go fetch the
		// history with `/messages`. An empty `start` or `end` here strands that client.
		page, res := readContext(t, h, aliceToken, roomID, b, "limit=0")
		h.mustStatus(t, res, http.StatusOK)
		if got, _ := page.Event["event_id"].(string); got != b {
			t.Fatalf("a zero limit must still return the event, got %q", got)
		}
		if len(page.EventsBefore) != 0 || len(page.EventsAfter) != 0 {
			t.Errorf("a zero limit returned a window: %v before, %v after",
				eventIDsOf(t, page.EventsBefore), eventIDsOf(t, page.EventsAfter))
		}
		if page.Start == "" || page.End == "" {
			t.Fatalf("a zero limit left a token out: start=%q end=%q", page.Start, page.End)
		}
		// The two tokens differ by design: one names the position the event sits at and the other
		// just past it, so that walking either way from them cannot lose the event itself.
		if page.Start == page.End {
			t.Errorf("start and end are the same token (%q), so one direction must skip the event", page.Start)
		}
		// Backwards from `start` must show the event that precedes the centre, and backwards from
		// `end` must show the centre event too — the two assertions Complement makes when it uses
		// this endpoint as a source of pagination tokens.
		fromStart := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&from="+url.QueryEscape(page.Start), aliceToken),
			http.StatusOK).decoded(t)
		if !chunkHas(t, fromStart, a) {
			t.Errorf("walking back from `start` does not reach %q: %v", a, chunkIDs(t, fromStart))
		}
		if chunkHas(t, fromStart, b) {
			t.Errorf("walking back from `start` includes the centre event %q", b)
		}
		fromEnd := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&from="+url.QueryEscape(page.End), aliceToken),
			http.StatusOK).decoded(t)
		if !chunkHas(t, fromEnd, b) || !chunkHas(t, fromEnd, a) {
			t.Errorf("walking back from `end` should reach both %q and %q: %v", b, a, chunkIDs(t, fromEnd))
		}
	})

	t.Run("the filter shapes the window and never the event asked for", func(t *testing.T) {
		// A caller filtering a window is asking "what else was going on that matches"; it is not
		// asking whether the event it already has matches.
		sendText(t, h, roomID, aliceToken, "ctx-media", "with a link")
		page, _ := readContext(t, h, aliceToken, roomID, b, `filter={"not_types":["m.room.message"]}`)
		if got, _ := page.Event["event_id"].(string); got != b {
			t.Errorf("the filter removed the requested event: %v", page.Event["event_id"])
		}
		for _, event := range append(append([]map[string]any{}, page.EventsBefore...), page.EventsAfter...) {
			if typ, _ := event["type"].(string); typ == "m.room.message" {
				t.Errorf("the filter let %q into the window", event["event_id"])
			}
		}
	})
}

func TestTheContextOfAnEventNobodyMaySee(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "ctx-owner", "pw-owner", "")
	_, strangerToken, _ := h.register(t, "ctx-stranger", "pw-stranger", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "private_chat"})
	secret := sendText(t, h, roomID, aliceToken, "ctx-secret", "not for the stranger")

	t.Run("a stranger to the room is refused the window", func(t *testing.T) {
		_, res := readContext(t, h, strangerToken, roomID, secret, "")
		if res.status != http.StatusForbidden {
			t.Errorf("status %d, want 403 (the room is not theirs to look around in)", res.status)
		}
	})
	t.Run("an event that is not there answers 404", func(t *testing.T) {
		_, res := readContext(t, h, aliceToken, roomID, "$nope:example.org", "")
		if res.status != http.StatusNotFound {
			t.Errorf("status %d, want 404", res.status)
		}
	})
	t.Run("a room that is not there answers 403, not 404", func(t *testing.T) {
		// The same rule `/messages` follows: a 404 on the room would tell a caller which room
		// identifiers this server has never heard of.
		_, res := readContext(t, h, aliceToken, "!nosuchroom:localhost", "$nope:example.org", "")
		if res.status != http.StatusForbidden {
			t.Errorf("status %d, want 403", res.status)
		}
	})
	t.Run("a malformed event ID or limit is a bad request", func(t *testing.T) {
		if _, res := readContext(t, h, aliceToken, roomID, "not-an-event-id", ""); res.status != http.StatusBadRequest {
			t.Errorf("an invalid event ID answered %d, want 400", res.status)
		}
		if _, res := readContext(t, h, aliceToken, roomID, secret, "limit=-3"); res.status != http.StatusBadRequest {
			t.Errorf("a negative limit answered %d, want 400", res.status)
		}
	})
	t.Run("the window belongs to a person, so it asks who", func(t *testing.T) {
		if _, res := readContext(t, h, "", roomID, secret, ""); res.status != http.StatusUnauthorized {
			t.Errorf("no token answered %d, want 401", res.status)
		}
	})
	t.Run("the window and /messages cannot disagree about the same event", func(t *testing.T) {
		// The window is built from `/messages` on purpose. This is the check that keeps it that
		// way: an event the reader may see one place must not be hidden in the other.
		page, _ := readContext(t, h, aliceToken, roomID, secret, "limit=50")
		if len(page.EventsBefore) == 0 {
			t.Fatalf("no window at all: %v", page)
		}
		first := page.EventsBefore[0]
		firstID, _ := first["event_id"].(string)
		direct := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+firstID, aliceToken), http.StatusOK).decoded(t)
		if direct["event_id"] != firstID {
			t.Errorf("/context showed %q and /event answered %v", firstID, direct["event_id"])
		}
	})
}

func chunkHas(t *testing.T, page map[string]any, eventID string) bool {
	t.Helper()
	for _, entry := range chunkIDs(t, page) {
		if entry == eventID {
			return true
		}
	}
	return false
}

func chunkIDs(t *testing.T, page map[string]any) []string {
	t.Helper()
	raw, _ := page["chunk"].([]any)
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		event, _ := entry.(map[string]any)
		id, _ := event["event_id"].(string)
		out = append(out, id)
	}
	return out
}
