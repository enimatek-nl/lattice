package integration

import (
	"net/http"
	"testing"
)

// These tests are the same shape as Complement's `TestRoomSpecificUsernameAtJoin` and
// `TestRoomSpecificUsernameChange`, and they were written against the same specification paragraph:
// a user directory MUST consider at least the users who share a room with the searcher and the users
// joined to rooms whose join rule is public or whose history visibility is world_readable. What makes
// the endpoint worth testing rather than implementing is that a display name in Matrix is written
// into a room's state, so "find Alice" and "find the person who calls themselves Freddy in that
// private room" are different questions, and only the first may be answered to a stranger.
//
// The three localparts carry a dash for the same reason Complement's do: a user ID is made of words,
// and the interesting search term is one word of an ID (`@user`) rather than a whole ID.

const alicePublicName = "Alice Cooper"
const alicePrivateName = "Freddy"

// setupDirectoryUsers builds the Complement cast, with the localparts Complement would generate.
func setupDirectoryUsers(t *testing.T) (h *harness, alice, aliceToken, bob, bobToken, eve, eveToken string) {
	t.Helper()
	h = newHarness(t)
	alice, aliceToken, _ = h.register(t, "user-1-alice", "pw-alice", "")
	bob, bobToken, _ = h.register(t, "user-2-bob", "pw-bob", "")
	eve, eveToken, _ = h.register(t, "user-3-eve", "pw-eve", "")

	// Alice's profile name — the name she goes by everywhere, and the name a stranger may match on.
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/profile/"+alice+"/displayname",
		map[string]any{"displayname": alicePublicName}, aliceToken), http.StatusOK)

	// A public room, which is what makes Alice findable by somebody she has never met: the
	// specification's second allowed set is the users joined to rooms anyone can join.
	if room := createRoom(t, h, aliceToken, map[string]any{"visibility": "public"}); room == "" {
		t.Fatal("Alice's public room was not created")
	}
	return
}

// revealPrivateName puts Alice in a private room of Bob's and writes a room-specific name there.
func revealPrivateName(t *testing.T, h *harness, alice, aliceToken, bobToken string) string {
	t.Helper()
	privateRoom := createRoom(t, h, bobToken, map[string]any{"visibility": "private"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+privateRoom+"/invite",
		map[string]any{"user_id": alice}, bobToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+privateRoom, map[string]any{}, aliceToken), http.StatusOK)
	// Alice reveals her private name to this room, which is a state write about her rather than a
	// change to her profile: her profile name stays "Alice Cooper" for everybody else.
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+privateRoom+"/state/m.room.member/"+alice,
		map[string]any{"membership": "join", "displayname": alicePrivateName}, aliceToken), http.StatusOK)
	return privateRoom
}

// searchDirectory asks the directory one person's question and returns the results it got back.
func searchDirectory(t *testing.T, h *harness, token string, body map[string]any) ([]map[string]any, bool) {
	t.Helper()
	res := h.post(t, "/_matrix/client/v3/user_directory/search", body, token)
	h.mustStatus(t, res, http.StatusOK)
	page := mustJSON(t, res)
	limited, _ := page["limited"].(bool)
	raw, _ := page["results"].([]any)
	results := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		row, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("a directory result is not an object: %v", entry)
		}
		results = append(results, row)
	}
	return results, limited
}

// wantOnly checks that exactly one person came back, and that the answer states what is expected of
// them. The name matters as much as the identity: the specification's response is "the display name
// of the user", so a match made on a room-specific name is still answered with the profile.
func wantOnly(t *testing.T, results []map[string]any, userID, displayName string) {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("the search found %d people (%v), want exactly %s", len(results), userIDsOf(results), userID)
	}
	if got, _ := results[0]["user_id"].(string); got != userID {
		t.Fatalf("the search found %q, want %q", got, userID)
	}
	if displayName != "" {
		if got, _ := results[0]["display_name"].(string); got != displayName {
			t.Errorf("the answer states %q, want the profile name %q", got, displayName)
		}
	}
}

func userIDsOf(results []map[string]any) []string {
	out := make([]string, 0, len(results))
	for _, row := range results {
		id, _ := row["user_id"].(string)
		out = append(out, id)
	}
	return out
}

func TestTheUserDirectoryFindsPeopleWithoutRevealingPrivateNames(t *testing.T) {
	t.Parallel()
	h, alice, aliceToken, _, bobToken, _, eveToken := setupDirectoryUsers(t)
	revealPrivateName(t, h, alice, aliceToken, bobToken)

	// Eve shares no room with Alice. She was never told Alice exists, and the only reason she may be
	// told now is that Alice is joined to a room anybody can join.
	t.Run("Eve finds Alice by her profile name", func(t *testing.T) {
		results, _ := searchDirectory(t, h, eveToken, map[string]any{"search_term": alicePublicName})
		wantOnly(t, results, alice, alicePublicName)
	})
	t.Run("Eve finds Alice by one word of her user ID", func(t *testing.T) {
		// `@user` is a prefix of all three localparts, so this is really two rules at once: the term
		// must match on words of the ID, and Bob and Eve must then be dropped because Alice's open
		// room is the only visible one and they are not in it (Eve is also herself, which is no
		// result at all). Complement's version of this test expects exactly one row for the same
		// reason.
		results, _ := searchDirectory(t, h, eveToken, map[string]any{"search_term": "@user"})
		wantOnly(t, results, alice, alicePublicName)
	})
	t.Run("Eve cannot find Alice by the name she wrote in a room Eve is not in", func(t *testing.T) {
		results, limited := searchDirectory(t, h, eveToken, map[string]any{"search_term": alicePrivateName})
		if len(results) != 0 {
			t.Fatalf("a private room's name for Alice was matched by a stranger: %v", userIDsOf(results))
		}
		if limited {
			t.Errorf("an empty result is not a truncated one")
		}
	})

	// Bob is in the private room. Everything Alice wrote there is his to match on — and the answer he
	// gets still states the name she goes by everywhere, because that is the response shape.
	t.Run("Bob finds Alice by her profile name", func(t *testing.T) {
		results, _ := searchDirectory(t, h, bobToken, map[string]any{"search_term": alicePublicName})
		wantOnly(t, results, alice, alicePublicName)
	})
	t.Run("Bob finds Alice by the room-specific name", func(t *testing.T) {
		results, _ := searchDirectory(t, h, bobToken, map[string]any{"search_term": alicePrivateName})
		wantOnly(t, results, alice, alicePublicName)
	})
	t.Run("Bob is not shown himself for a word of his own user ID", func(t *testing.T) {
		results, _ := searchDirectory(t, h, bobToken, map[string]any{"search_term": "@user"})
		wantOnly(t, results, alice, alicePublicName)
	})

	// And the room's own member list still says what Alice wrote in it. The directory filtering names
	// is not the room changing them: `GET /members` is where a room's names belong.
	t.Run("the private room still holds the name written in it", func(t *testing.T) {
		roomID := revealPrivateName(t, h, alice, aliceToken, bobToken)
		res := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+alice, bobToken),
			http.StatusOK).decoded(t)
		if got, _ := res["displayname"].(string); got != alicePrivateName {
			t.Errorf("the room's member event says %q, want the name written into it (%q)", got, alicePrivateName)
		}
	})
}

func TestAUserSearchSaysWhatItCannotAnswer(t *testing.T) {
	t.Parallel()
	h, alice, aliceToken, _, bobToken, _, eveToken := setupDirectoryUsers(t)
	// The private room has to exist before the last subtest can ask whether it makes a difference.
	revealPrivateName(t, h, alice, aliceToken, bobToken)

	t.Run("a search without a term is refused rather than answered with everybody", func(t *testing.T) {
		res := h.post(t, "/_matrix/client/v3/user_directory/search", map[string]any{}, bobToken)
		h.mustStatus(t, res, http.StatusBadRequest)
		if code := res.errcode(t); code != "M_MISSING_PARAM" {
			t.Errorf("a missing search_term answers %q, want M_MISSING_PARAM", code)
		}
	})
	t.Run("a negative limit is a bad parameter", func(t *testing.T) {
		res := h.post(t, "/_matrix/client/v3/user_directory/search",
			map[string]any{"search_term": "alice", "limit": -1}, bobToken)
		h.mustStatus(t, res, http.StatusBadRequest)
	})
	t.Run("an empty term finds nobody", func(t *testing.T) {
		// Not an error: the specification asks for a term, and this one was supplied. It simply has
		// no word in it, and a directory search with no word in it is not a request for the whole
		// account list.
		results, _ := searchDirectory(t, h, bobToken, map[string]any{"search_term": "   "})
		if len(results) != 0 {
			t.Fatalf("an empty term found %v", userIDsOf(results))
		}
	})
	t.Run("the endpoint belongs to a person, so it asks who", func(t *testing.T) {
		res := h.post(t, "/_matrix/client/v3/user_directory/search", map[string]any{"search_term": "alice"}, "")
		h.mustStatus(t, res, http.StatusUnauthorized)
	})
	t.Run("the answer is per searcher, not per server", func(t *testing.T) {
		// The same question, from the private room and from outside it. If the directory were a
		// server-wide list, both would answer the same way and one of them would be a leak.
		inRoom, _ := searchDirectory(t, h, bobToken, map[string]any{"search_term": alicePrivateName})
		outside, _ := searchDirectory(t, h, eveToken, map[string]any{"search_term": alicePrivateName})
		if len(inRoom) == 0 {
			t.Fatalf("the room member was not shown the name written in their room")
		}
		if len(outside) != 0 {
			t.Fatalf("the stranger was shown the name written in a room they are not in: %v", userIDsOf(outside))
		}
	})
}

func TestAUserSearchStopsWhereTheCallerSaid(t *testing.T) {
	t.Parallel()
	h, _, aliceToken, _, bobToken, _, _ := setupDirectoryUsers(t)

	// Two more people in Alice's public room, so a term that matches all three has to truncate.
	carol, carolToken, _ := h.register(t, "user-4-carol", "pw-carol", "")
	dave, daveToken, _ := h.register(t, "user-5-dave", "pw-dave", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"visibility": "public"})
	for _, joiner := range []struct {
		token string
		user  string
	}{{carolToken, carol}, {daveToken, dave}} {
		h.mustStatus(t, h.post(t, "/_matrix/client/v3/join/"+roomID, map[string]any{}, joiner.token),
			http.StatusOK)
	}

	// Alice is joined to two public rooms by now; everyone in one is visible to Bob. The term "user"
	// matches every localpart, so the result set is Alice, Carol and Dave.
	all, limited := searchDirectory(t, h, bobToken, map[string]any{"search_term": "user"})
	if len(all) != 3 {
		t.Fatalf("a term matching three people found %v, want three of them", userIDsOf(all))
	}
	if limited {
		t.Errorf("three results under the default limit of ten are not a truncated page")
	}

	page, limited := searchDirectory(t, h, bobToken, map[string]any{"search_term": "user", "limit": 2})
	if len(page) != 2 {
		t.Fatalf("limit 2 returned %v", userIDsOf(page))
	}
	if !limited {
		t.Errorf("two of three results must say the list was truncated")
	}
	// The order is stable: a caller that pages or retries the same search should not be shown a
	// reshuffled list and be unable to tell which half it has already seen.
	firstAgain, _ := searchDirectory(t, h, bobToken, map[string]any{"search_term": "user", "limit": 2})
	if userIDsOf(firstAgain)[0] != userIDsOf(page)[0] {
		t.Errorf("the same search answered in a different order: %v then %v", userIDsOf(page), userIDsOf(firstAgain))
	}
}
