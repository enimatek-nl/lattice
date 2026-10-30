package compat

// This file holds the search tests; TestSearch lives in search_test.go and the
// JSON shaped request is covered here.

import (
	"net/http"
	"testing"
)

// jsonHits returns the hits of the room_events category of a JSON search.
func jsonHits(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	categories, ok := body["search_categories"].(map[string]any)
	if !ok {
		t.Fatalf("no search_categories in %v", body)
	}
	category, ok := categories["room_events"].(map[string]any)
	if !ok {
		t.Fatalf("no room_events category in %v", categories)
	}
	raw, ok := category["hits"].([]any)
	if !ok {
		t.Fatalf("no hits in the room_events category: %v", category)
	}
	hits := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		hit, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("hit %v is not an object", entry)
		}
		hits = append(hits, hit)
	}
	return hits
}

// TestSearchJSON covers the search_categories request form, which carries
// interactive filters and returns the matches grouped per conversation.
func TestSearchJSON(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Chatter"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	root := server.Send(alice, roomID, "j1", "the white rabbit is late")
	server.Send(bob, roomID, "j2", "late for the tea party")

	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_categories": map[string]any{"room_events": map[string]any{
			"search_key": map[string]any{"term": []string{"late"}},
			"limits":     map[string]any{"limit": 5},
		}},
	}, http.StatusOK)
	hits := jsonHits(t, resp.Body)
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2 in %s", len(hits), resp.Raw)
	}
	for _, hit := range hits {
		id, _ := hit["event_id"].(string)
		if id == "" {
			t.Fatalf("hit without an event id: %v", hit)
		}
		groups, ok := hit["results_per_group"].([]any)
		if !ok || len(groups) != 1 {
			t.Fatalf("hit %s has no single group: %v", id, hit["results_per_group"])
		}
		group, _ := groups[0].(map[string]any)
		if groupID, _ := group["group_id"].(string); groupID != roomID {
			t.Errorf("group_id = %v, want the room %s", group["group_id"], roomID)
		}
		events, ok := group["events"].([]any)
		if !ok || len(events) != 1 {
			t.Fatalf("group of %s holds %v", id, group["events"])
		}
		event, _ := events[0].(map[string]any)
		if event["event_id"] == nil || event["content"] == nil {
			t.Errorf("the grouped event is incomplete: %v", event)
		}
	}

	// The category echoes the settings that were applied.
	categories, _ := resp.Body["search_categories"].(map[string]any)
	category, _ := categories["room_events"].(map[string]any)
	if orderBy, _ := category["order_by"].(string); orderBy != "recent" {
		t.Errorf("order_by = %v", category["order_by"])
	}
	groupings, ok := category["groupings"].([]any)
	if !ok || len(groupings) != 1 {
		t.Fatalf("groupings = %v", category["groupings"])
	}
	first, _ := groupings[0].(map[string]any)
	if key, _ := first["key"].(string); key != "session_id" {
		t.Errorf("default grouping key = %v", first["key"])
	}
	limits, ok := category["limits"].(map[string]any)
	if !ok || limits["max_size"] == nil {
		t.Errorf("limits = %v", category["limits"])
	}

	// A thread is one conversation, so a reply groups under its root.
	threadTxn := "j3"
	body := map[string]any{
		"msgtype": "m.text",
		"body":    "late as always",
		"m.relates_to": map[string]any{
			"rel_type": "m.thread",
			"event_id": root,
		},
	}
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/"+threadTxn,
		alice.Token, body, http.StatusOK)
	resp = server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_categories": map[string]any{"room_events": map[string]any{
			"search_key": map[string]any{"term": "late"},
			"groupings":  []any{map[string]any{"key": "session_id"}},
		}},
	}, http.StatusOK)
	groups := map[string]int{}
	for _, hit := range jsonHits(t, resp.Body) {
		entries, _ := hit["results_per_group"].([]any)
		for _, raw := range entries {
			group, _ := raw.(map[string]any)
			id, _ := group["group_id"].(string)
			events, _ := group["events"].([]any)
			groups[id] += len(events)
		}
	}
	if groups[root] == 0 {
		t.Errorf("the reply to %s did not group under the root: %v", root, groups)
	}

	// Grouping by room gives one group per room, and a menu selection narrows
	// the search the same way a filter does.
	resp = server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_categories": map[string]any{"room_events": map[string]any{
			"search_key": map[string]any{"term": "late"},
			"groupings":  []any{map[string]any{"key": "room_id"}},
			"menu":       map[string]any{"event_types": []string{"m.room.message"}},
		}},
	}, http.StatusOK)
	if len(jsonHits(t, resp.Body)) != 3 {
		t.Errorf("menu filtered hits: %s", resp.Raw)
	}
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_categories": map[string]any{"room_events": map[string]any{
			"search_key": map[string]any{"term": "late"},
			"menu":       map[string]any{"event_types": []string{"m.room.topic"}},
		}},
	}, http.StatusOK); len(jsonHits(t, resp.Body)) != 0 {
		t.Errorf("a menu selection that matches nothing: %s", resp.Raw)
	}

	// The search_key may also carry a filter and a nested full text query.
	if resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{
		"search_categories": map[string]any{"room_events": map[string]any{
			"search_key": map[string]any{
				"json": map[string]any{
					"query":  "late",
					"filter": map[string]any{"rooms": []string{roomID}, "senders": []string{bob.UserID}},
				},
			},
		}},
	}, http.StatusOK); len(jsonHits(t, resp.Body)) != 1 {
		t.Errorf("the nested json search key: %s", resp.Raw)
	}

	// Bad requests.
	for name, body := range map[string]any{
		"no categories":  map[string]any{},
		"unknown kind":   map[string]any{"search_categories": map[string]any{"people": map[string]any{}}},
		"no terms":       map[string]any{"search_categories": map[string]any{"room_events": map[string]any{}}},
		"bad category":   map[string]any{"search_categories": map[string]any{"room_events": "nope"}},
		"bad search key": map[string]any{"search_categories": map[string]any{"room_events": map[string]any{"search_key": "nope"}}},
		"bad groupings":  map[string]any{"search_categories": map[string]any{"room_events": map[string]any{"search_key": map[string]any{"term": "late"}, "groupings": "nope"}}},
		"unknown group":  map[string]any{"search_categories": map[string]any{"room_events": map[string]any{"search_key": map[string]any{"term": "late"}, "groupings": []any{"day"}}}},
		"bad order":      map[string]any{"search_categories": map[string]any{"room_events": map[string]any{"search_key": map[string]any{"term": "late"}, "order_by": "hottest"}}},
	} {
		if resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token, body); resp.Status != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", name, resp.Status, resp.Raw)
		}
	}
}
