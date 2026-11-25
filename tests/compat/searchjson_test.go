package compat

import (
	"net/http"
	"net/url"
	"testing"
)

// searchSpecBody runs the request of POST /_matrix/client/v3/search exactly as
// the specification defines it — terms and filters inside the room_events
// category — and returns that category of the answer.
func searchSpecBody(t *testing.T, server *Server, token string, category map[string]any) map[string]any {
	t.Helper()
	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search", token,
		map[string]any{"search_categories": map[string]any{"room_events": category}}, http.StatusOK)
	return searchSpecCategory(t, resp.Body)
}

// searchSpecCategory picks the room_events category out of a search answer.
func searchSpecCategory(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	categories, _ := body["search_categories"].(map[string]any)
	category, _ := categories["room_events"].(map[string]any)
	if category == nil {
		t.Fatalf("the answer carries no room_events category: %v", body)
	}
	return category
}

// searchSpecIDs lists the matched event ids in the order they were answered.
func searchSpecIDs(category map[string]any) []string {
	results, _ := category["results"].([]any)
	ids := make([]string, 0, len(results))
	for _, raw := range results {
		hit, _ := raw.(map[string]any)
		result, _ := hit["result"].(map[string]any)
		if id, _ := result["event_id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// TestSearchSpecRoomWords covers the smallest search a client sends: one term in
// the search_categories body, an event to find, and the fields the Matrix Rust
// SDK reads. An earlier revision of this endpoint asked for the terms under
// "search_key" and answered "hits", so a client following the specification was
// told no search term had arrived at all.
func TestSearchSpecRoomWords(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Workshop"})
	hit := server.Send(alice, roomID, "kiln-1", "the kiln is hot")
	server.Send(alice, roomID, "kiln-2", "cold ashes elsewhere")

	category := searchSpecBody(t, server, alice.Token, map[string]any{"search_term": "kiln"})
	if count, ok := category["count"].(float64); !ok {
		t.Fatalf("the category carries no count: %v", category)
	} else if int(count) != 1 {
		t.Errorf("count = %d, want 1", int(count))
	}
	ids := searchSpecIDs(category)
	if len(ids) != 1 || ids[0] != hit {
		t.Errorf("results = %v, want just %s", ids, hit)
	}
	results, _ := category["results"].([]any)
	entry, _ := results[0].(map[string]any)
	// The result is the event a client would otherwise have to fetch again.
	event, _ := entry["result"].(map[string]any)
	if event["type"] != "m.room.message" {
		t.Errorf("the result is a %v, want the message event itself", event["type"])
	}
	if content, _ := event["content"].(map[string]any); content["body"] != "the kiln is hot" {
		t.Errorf("the result carries %v, want the message text", event["content"])
	}
	if rank, ok := entry["rank"].(float64); !ok {
		t.Errorf("a result carries no numeric rank: %v", entry)
	} else if rank <= 0 || rank > 1 {
		t.Errorf("rank = %v, want a number above zero and at most one", rank)
	}
	highlights, _ := category["highlights"].([]any)
	if len(highlights) != 1 || highlights[0] != "kiln" {
		t.Errorf("highlights = %v, want the searched word back", highlights)
	}
	if batch, ok := category["next_batch"].(string); ok && batch != "" {
		t.Errorf("a single match came with a next_batch %q", batch)
	}

	// A room name is an event as well, and a search over events finds it: Synapse
	// indexes names and topics too, so this server does the same.
	byName := searchSpecBody(t, server, alice.Token, map[string]any{"search_term": "Workshop"})
	nameIDs := searchSpecIDs(byName)
	if len(nameIDs) != 1 {
		t.Fatalf("searching the room name answered %v, want one event", nameIDs)
	}
	named := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+url.PathEscape(roomID)+"/event/"+url.PathEscape(nameIDs[0]),
		alice.Token, nil, http.StatusOK)
	if named.Body["type"] != "m.room.name" {
		t.Errorf("the name match was a %v, want the room name event", named.Body["type"])
	}
}

// TestSearchSpecFiltersAndPages covers the pieces a client leans on once the
// body is read: terms as a list, filters per room and sender, restricting the
// search to fields of the event, both orderings, and paging with next_batch.
func TestSearchSpecFiltersAndPages(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	kitchen := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Kitchen"})
	bakery := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Bakery"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+bakery, bob.Token, nil, http.StatusOK)

	// The same word in three rooms and two accounts, so a filter has to choose.
	bread := server.Send(alice, kitchen, "f1", "warm bread rises")
	server.Send(alice, bakery, "f2", "warm bread burns")
	water := server.Send(bob, bakery, "f3", "warm water cools")

	// A term list AND-s the words.
	both := searchSpecBody(t, server, alice.Token, map[string]any{"search_term": []string{"warm", "bread"}})
	if ids := searchSpecIDs(both); len(ids) != 2 {
		t.Errorf("two terms matched %v, want the two bread events", ids)
	}

	// The rooms filter keeps one room.
	oneRoom := searchSpecBody(t, server, alice.Token, map[string]any{
		"search_term": "warm",
		"filter":      map[string]any{"rooms": []string{kitchen}},
	})
	if ids := searchSpecIDs(oneRoom); len(ids) != 1 || ids[0] != bread {
		t.Errorf("room filter answered %v, want only %s", ids, bread)
	}

	// The senders filter keeps one account: bob speaks in the bakery only.
	bySender := searchSpecBody(t, server, alice.Token, map[string]any{
		"search_term": "warm",
		"filter":      map[string]any{"senders": []string{bob.UserID}},
	})
	if ids := searchSpecIDs(bySender); len(ids) != 1 || ids[0] != water {
		t.Errorf("sender filter answered %v, want only %s", ids, water)
	}

	// A key restricts matching to one field of the event.
	inBody := searchSpecBody(t, server, alice.Token, map[string]any{
		"search_term": "burns",
		"keys":        []string{"content.body"},
	})
	if count, _ := inBody["count"].(float64); int(count) != 1 {
		t.Errorf("content.body matched %d events, want 1", int(count))
	}
	inTopic := searchSpecBody(t, server, alice.Token, map[string]any{
		"search_term": "burns",
		"keys":        []string{"content.topic"},
	})
	if count, _ := inTopic["count"].(float64); int(count) != 0 {
		t.Errorf("content.topic matched %d events, want none: no topic says burns", int(count))
	}

	// Both orderings answer every match; only the sequence differs.
	recent := searchSpecBody(t, server, alice.Token, map[string]any{
		"search_term": "warm",
		"order_by":    "recent",
		"limit":       2,
	})
	recentIDs := searchSpecIDs(recent)
	if len(recentIDs) != 2 {
		t.Fatalf("the recent page answered %v, want two of three matches", recentIDs)
	}
	if recentIDs[0] == "" {
		t.Fatal("the recent page lost an event id")
	}
	// The newest match is the last one sent, and the page holds the two newest.
	if ids := searchSpecIDs(recent); ids[0] == bread {
		t.Errorf("recent ordered %v with the oldest match first", ids)
	}
	batch, _ := recent["next_batch"].(string)
	if batch == "" {
		t.Fatalf("a full page came without next_batch: %v", recent)
	}
	next := searchSpecBody(t, server, alice.Token, map[string]any{
		"search_term": "warm",
		"order_by":    "recent",
		"limit":       2,
		"next_batch":  batch,
	})
	rest := searchSpecIDs(next)
	if len(rest) != 1 {
		t.Errorf("the second page answered %v, want the one remaining match", rest)
	}
	if _, ok := next["next_batch"].(string); ok {
		t.Errorf("the last page still promised a next_batch: %v", next)
	}
	// Count covers all matches, not just this page.
	if count, _ := recent["count"].(float64); int(count) != 3 {
		t.Errorf("count = %d, want the three matches of both rooms", int(count))
	}
	ranked := searchSpecBody(t, server, alice.Token, map[string]any{"search_term": "warm", "order_by": "rank"})
	if len(searchSpecIDs(ranked)) != 3 {
		t.Errorf("rank ordering answered %v, want all three matches", searchSpecIDs(ranked))
	}
}

// TestSearchSpecWordsAndQueryBatch covers the two ways a client reaches the same
// matches: a term that is a sentence rather than a phrase, and the batch token as
// a query parameter, which is where Synapse accepts it too.
func TestSearchSpecWordsAndQueryBatch(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Numbers"})
	wanted := map[string]bool{
		server.Send(alice, roomID, "n1", "Message number 4"):  true,
		server.Send(alice, roomID, "n2", "Message number 14"): true,
	}
	server.Send(alice, roomID, "n3", "nothing of the sort")

	// One term, two words: the phrase does not occur, both messages carry the words.
	words := searchSpecBody(t, server, alice.Token, map[string]any{"search_term": "Message 4"})
	ids := searchSpecIDs(words)
	if len(ids) != 2 || !wanted[ids[0]] || !wanted[ids[1]] {
		t.Fatalf("the term \"Message 4\" matched %v, want both messages", ids)
	}
	highlights, _ := words["highlights"].([]any)
	if len(highlights) != 2 {
		t.Errorf("highlights = %v, want the two words back", highlights)
	}

	// The batch token of a page two, taken from the query string.
	limited := searchSpecBody(t, server, alice.Token, map[string]any{"search_term": "Message", "limit": 1})
	if ids := searchSpecIDs(limited); len(ids) != 1 {
		t.Fatalf("a page of one answered %v", ids)
	}
	batch, _ := limited["next_batch"].(string)
	if batch == "" {
		t.Fatalf("a page of one of two matches came without next_batch: %v", limited)
	}
	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/search?next_batch="+url.QueryEscape(batch),
		alice.Token, map[string]any{"search_categories": map[string]any{
			"room_events": map[string]any{"search_term": "Message", "limit": 1}}}, http.StatusOK)
	page2 := searchSpecIDs(searchSpecCategory(t, resp.Body))
	if len(page2) != 1 || page2[0] == ids[0] {
		t.Errorf("the query batch answered %v, want the other match than %v", page2, ids)
	}
}

// TestSearchSpecContextAroundAMatch covers event_context: a client that found a
// message asks for its neighbours in the same answer. The page size of the
// specification lives inside the filter, which is also where this request puts it.
func TestSearchSpecContextAroundAMatch(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Neighbours"})
	for _, text := range []string{"first", "second", "the match here", "fourth", "fifth"} {
		server.Send(alice, roomID, "ctx-"+text, text)
	}
	category := searchSpecBody(t, server, alice.Token, map[string]any{
		"search_term": "the match here",
		"order_by":    "recent",
		"keys":        []string{"content.body"},
		"filter":      map[string]any{"limit": 1},
		"event_context": map[string]any{
			"before_limit": 2,
			"after_limit":  2,
		},
	})
	results, _ := category["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("the search answered %d results, want 1 (%v)", len(results), category)
	}
	hit, _ := results[0].(map[string]any)
	contextBlock, _ := hit["context"].(map[string]any)
	// Each window starts next to the match and grows away from it.
	if bodies := contextBodies(contextBlock["events_before"]); len(bodies) != 2 || bodies[0] != "second" || bodies[1] != "first" {
		t.Errorf("events_before = %v, want [second first]", bodies)
	}
	if bodies := contextBodies(contextBlock["events_after"]); len(bodies) != 2 || bodies[0] != "fourth" || bodies[1] != "fifth" {
		t.Errorf("events_after = %v, want [fourth fifth]", bodies)
	}
	if count, _ := category["count"].(float64); int(count) != 1 {
		t.Errorf("count = %d, want the single match", int(count))
	}
	// A full page promises another; that is what a client paginating to the end
	// counts on, and the next request answers empty when there is nothing left.
	if batch, _ := category["next_batch"].(string); batch == "" {
		t.Errorf("a full page of one came without next_batch: %v", category)
	}
}

// contextBodies lists the message texts of a context window.
func contextBodies(raw any) []string {
	items, _ := raw.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		event, _ := item.(map[string]any)
		content, _ := event["content"].(map[string]any)
		if body, _ := content["body"].(string); body != "" {
			out = append(out, body)
		}
	}
	return out
}

// TestSearchSpecRejectsBadRequests covers the answers a client can act on: a
// missing term, an ordering this server does not know and a batch token it never
// handed out.
func TestSearchSpecRejectsBadRequests(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token,
		map[string]any{"search_categories": map[string]any{"room_events": map[string]any{}}}); resp.Status != http.StatusBadRequest {
		t.Errorf("a search without a term: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token,
		map[string]any{"search_categories": map[string]any{"room_events": map[string]any{"search_term": ""}}}); resp.Status != http.StatusBadRequest {
		t.Errorf("a search with an empty term: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token,
		map[string]any{"search_categories": map[string]any{"room_events": map[string]any{
			"search_term": "late", "order_by": "alphabetical"}}}); resp.Status != http.StatusBadRequest {
		t.Errorf("an ordering this server does not serve: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token,
		map[string]any{"search_categories": map[string]any{"room_events": map[string]any{
			"search_term": "late", "next_batch": "not-a-batch"}}}); resp.Status != http.StatusBadRequest {
		t.Errorf("a batch token this server never handed out: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token, map[string]any{}); resp.Status != http.StatusBadRequest {
		t.Errorf("a body without search_categories: got %d %s, want 400", resp.Status, resp.Raw)
	}
}

// TestSearchSpecOptionalFields covers the forgiving half: the optional fields of
// a category are accepted and change nothing, while a search that asks another
// category than room_events has no term for this server to run.
func TestSearchSpecOptionalFields(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Quiet"})
	server.Send(alice, roomID, "quiet-1", "a word nobody says twice")

	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/search", alice.Token,
		map[string]any{"search_categories": map[string]any{"groups": map[string]any{"search_term": "word"}}}); resp.Status != http.StatusBadRequest {
		t.Errorf("a search in a category this server does not serve: got %d %s, want 400",
			resp.Status, resp.Raw)
	}

	withExtra := searchSpecBody(t, server, alice.Token, map[string]any{
		"search_term":   "word",
		"escape_regex":  false,
		"groupings":     map[string]any{"group_by": []string{"eventsenders"}},
		"event_context": map[string]any{"before": 0, "after": 0},
	})
	if count, _ := withExtra["count"].(float64); int(count) != 1 {
		t.Errorf("a request with the optional fields set matched %d events, want 1", int(count))
	}
}
