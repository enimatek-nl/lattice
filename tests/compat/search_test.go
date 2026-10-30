package compat

import (
	"net/http"
	"testing"
)

// searchCount digs the "count" of the room_events category out of a search
// response.
func searchCount(t *testing.T, body map[string]any) int {
	t.Helper()
	categories, ok := body["categories"].(map[string]any)
	if !ok {
		t.Fatalf("no categories in %v", body)
	}
	category, ok := categories["room_events"].(map[string]any)
	if !ok {
		t.Fatalf("no room_events category in %v", categories)
	}
	count, ok := category["count"].(float64)
	if !ok {
		t.Fatalf("no count in the room_events category: %v", category)
	}
	return int(count)
}

// searchResults returns the event ids of the room_events result groups.
func searchResults(t *testing.T, body map[string]any) []string {
	t.Helper()
	results, ok := body["results"].(map[string]any)
	if !ok {
		t.Fatalf("no results in %v", body)
	}
	var ids []string
	for _, group := range results {
		room, ok := group.(map[string]any)
		if !ok {
			continue
		}
		chunk, ok := room["results_events"].(map[string]any)
		if !ok {
			continue
		}
		events, ok := chunk["chunk"].([]any)
		if !ok {
			continue
		}
		for _, raw := range events {
			event, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if id, ok := event["event_id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// TestSearch covers POST /_matrix/client/v3/search with the flat body of the
// original specification.
func TestSearch(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Chatter"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	first := server.Send(alice, roomID, "s1", "the white rabbit is late")
	second := server.Send(bob, roomID, "s2", "late for the tea party")
	server.Send(alice, roomID, "s3", "nothing to see here")

	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token,
		map[string]any{"search_term": "late"}, http.StatusOK)
	if got := searchCount(t, resp.Body); got != 2 {
		t.Errorf("count = %d, want 2 in %s", got, resp.Raw)
	}
	found := searchResults(t, resp.Body)
	for _, want := range []string{first, second} {
		if !containsID(found, want) {
			t.Errorf("%s is missing from the results %v", want, found)
		}
	}
	echo, ok := resp.Body["search_categories"].(map[string]any)
	if !ok {
		t.Fatalf("no search_categories echo in %s", resp.Raw)
	}
	roomEvents, ok := echo["room_events"].(map[string]any)
	if !ok {
		t.Fatalf("no room_events echo: %v", echo)
	}
	if orderBy, _ := roomEvents["order_by"].(string); orderBy != "recent" {
		t.Errorf("order_by = %v, want recent", roomEvents["order_by"])
	}

	// Case does not matter, and a term nobody used returns an empty set.
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", bob.Token,
		map[string]any{"search_term": "LATE FOR"}, http.StatusOK); searchCount(t, resp.Body) != 1 {
		t.Errorf("case insensitive search: %s", resp.Raw)
	}
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token,
		map[string]any{"search_term": "dormouse"}, http.StatusOK); searchCount(t, resp.Body) != 0 {
		t.Errorf("an unknown term should find nothing: %s", resp.Raw)
	}

	// Filters narrow the rooms, the senders and the event types.
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_term": "late",
		"filter":      map[string]any{"senders": []string{alice.UserID}},
	}, http.StatusOK); searchCount(t, resp.Body) != 1 {
		t.Errorf("filter by sender: %s", resp.Raw)
	}
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_term": "late",
		"filter":      map[string]any{"not_rooms": []string{roomID}},
	}, http.StatusOK); searchCount(t, resp.Body) != 0 {
		t.Errorf("filter excluding the only room: %s", resp.Raw)
	}
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_term": "late",
		"filter":      map[string]any{"types": []string{"m.room.message"}},
	}, http.StatusOK); searchCount(t, resp.Body) != 2 {
		t.Errorf("filter by event type: %s", resp.Raw)
	}
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_term": "late",
		"filter":      map[string]any{"types": []string{"m.room.name"}},
	}, http.StatusOK); searchCount(t, resp.Body) != 0 {
		t.Errorf("filter by a type without matches: %s", resp.Raw)
	}

	// A limit truncates the result and says so in the category echo.
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token,
		map[string]any{"search_term": "late", "limit": 1}, http.StatusOK); searchCount(t, resp.Body) != 1 {
		t.Errorf("limit 1: %s", resp.Raw)
	}

	// event_fields trims the returned events.
	resp = server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_term":  "late",
		"event_fields": []string{"type"},
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("event_fields search: got %d %s", resp.Status, resp.Raw)
	}
	for _, id := range searchResults(t, resp.Body) {
		event := searchEvent(t, resp.Body, id)
		if _, hasContent := event["content"]; hasContent {
			t.Errorf("event %s still carries its content: %v", id, event)
		}
		if _, hasType := event["type"]; !hasType {
			t.Errorf("event %s lost its type: %v", id, event)
		}
		if event["event_id"] == nil {
			t.Errorf("event %s lost its event_id: %v", id, event)
		}
	}

	// include_state attaches the state of the matching rooms.
	resp = server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_term":   "late",
		"include_state": true,
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("include_state search: got %d %s", resp.Status, resp.Raw)
	}
	results, ok := resp.Body["results"].(map[string]any)
	if !ok {
		t.Fatalf("no results in %s", resp.Raw)
	}
	group, ok := results[roomID].(map[string]any)
	if !ok {
		t.Fatalf("no result group for %s in %v", roomID, results)
	}
	state, ok := group["state"].([]any)
	if !ok || len(state) == 0 {
		t.Errorf("include_state returned no state events: %v", group)
	}

	// A room the requester cannot read is an error, not a silent gap.
	private := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", bob.Token, map[string]any{
		"search_term": "late",
		"filter":      map[string]any{"rooms": []string{private}},
	}); resp.Status != http.StatusForbidden {
		t.Errorf("searching an unreadable room: got %d %s, want 403", resp.Status, resp.Raw)
	}

	// Bad requests.
	for name, body := range map[string]any{
		"no term":      map[string]any{},
		"empty term":   map[string]any{"search_term": ""},
		"bad order":    map[string]any{"search_term": "late", "order_by": "relevance"},
		"bad filter":   map[string]any{"search_term": "late", "filter": map[string]any{"rooms": "nope"}},
		"terms as map": map[string]any{"search_term": map[string]any{"nope": true}},
	} {
		resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token, body)
		if resp.Status != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", name, resp.Status, resp.Raw)
		}
	}

	// Search needs an access token.
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", "",
		map[string]any{"search_term": "late"}); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated search: got %d, want 401", resp.Status)
	}
}

// searchEvent returns the raw event map of one result.
func searchEvent(t *testing.T, body map[string]any, eventID string) map[string]any {
	t.Helper()
	results, ok := body["results"].(map[string]any)
	if !ok {
		t.Fatalf("no results in %v", body)
	}
	for _, group := range results {
		room, ok := group.(map[string]any)
		if !ok {
			continue
		}
		chunk, ok := room["results_events"].(map[string]any)
		if !ok {
			continue
		}
		events, _ := chunk["chunk"].([]any)
		for _, raw := range events {
			event, ok := raw.(map[string]any)
			if ok && event["event_id"] == eventID {
				return event
			}
		}
	}
	t.Fatalf("event %s is not in the results", eventID)
	return nil
}

// containsID reports whether a list of event ids holds the wanted id.
func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
