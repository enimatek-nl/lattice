package compat

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestUserDirectorySeesTheRoomsItIsAllowedTo covers what the user directory may say and to whom:
// the people in a public room are findable by anybody, the people of a private room only by those
// who are in it, and a name someone chose for one room is not information for a person who is only
// in another one. The answer is also required to be a list: `results` is a required property, and a
// server that answers `null` when nothing matches leaves a client counting a list it was not given.
func TestUserDirectorySeesTheRoomsItIsAllowedTo(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "loblaw")
	carol := server.Register("carol", "dance")
	eve := server.Register("eve", "dicts")

	server.MustDo(http.MethodPut, "/_matrix/client/v3/profile/"+alice.UserID+"/displayname", alice.Token,
		map[string]any{"displayname": "Alice Cooper"}, http.StatusOK)

	publicRoom := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "visibility": "public"})
	privateRoom := server.CreateRoom(bob, map[string]any{"preset": "private_chat"})
	for _, guest := range []*Session{alice, carol} {
		server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+privateRoom+"/invite", bob.Token,
			map[string]any{"user_id": guest.UserID}, http.StatusOK)
		server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+privateRoom, guest.Token, nil, http.StatusOK)
	}
	// A name for one room is not a name for everybody: alice tells the private room who she is.
	server.MustDo(http.MethodPut,
		"/_matrix/client/v3/rooms/"+privateRoom+"/state/m.room.member/"+alice.UserID, alice.Token,
		map[string]any{"membership": "join", "displayname": "Freddy"}, http.StatusOK)

	// Eve shares no room with carol, who is in no public room, so carol does not exist to her.
	results := searchDirectory(t, server, eve, "carol")
	if len(results) != 0 {
		t.Errorf("a stranger finds a private room's member: %v", results)
	}
	// Bob is in that room, and the directory answers with the room it found her through.
	results = searchDirectory(t, server, bob, "carol")
	if len(results) != 1 || results[0].UserID != carol.UserID {
		t.Fatalf("the person sharing a private room does not find her: %v", results)
	}
	if !hasString(results[0].Rooms, privateRoom) {
		t.Errorf("the answer does not name the room carol can be contacted in: %v", results[0].Rooms)
	}

	// A public room opens its members to anybody, with the name the user chose for their profile.
	for _, searcher := range []*Session{eve, bob} {
		results = searchDirectory(t, server, searcher, "Alice Cooper")
		if len(results) != 1 || results[0].UserID != alice.UserID {
			t.Fatalf("%s does not find the member of a public room by profile name: %v", searcher.UserID, results)
		}
		if results[0].DisplayName != "Alice Cooper" {
			t.Errorf("the directory returns %q, want the profile name %q", results[0].DisplayName, "Alice Cooper")
		}
		if !hasString(results[0].Rooms, publicRoom) {
			t.Errorf("the answer does not name the public room alice is in: %v", results[0].Rooms)
		}
	}

	// The room-specific name stays inside the room: eve must not learn that a person she can see is
	// called something else somewhere else.
	if results = searchDirectory(t, server, eve, "Freddy"); len(results) != 0 {
		t.Errorf("a room's own name for a member leaks to a stranger: %v", results)
	}
}

// directoryEntry is one line of a user directory answer.
type directoryEntry struct {
	UserID      string   `json:"user_id"`
	DisplayName string   `json:"display_name"`
	AvatarURL   string   `json:"avatar_url"`
	Rooms       []string `json:"rooms"`
}

// searchDirectory asks the question and insists on the shape: `results` is a list, empty or not.
func searchDirectory(t *testing.T, server *Server, session *Session, term string) []directoryEntry {
	t.Helper()
	resp := server.MustDo(http.MethodPost, "/_matrix/client/v3/user_directory/search", session.Token,
		map[string]any{"search_term": term}, http.StatusOK)
	var envelope map[string]any
	if err := json.Unmarshal(resp.Raw, &envelope); err != nil {
		t.Fatalf("the user directory does not answer with an object: %v\n%s", err, resp.Raw)
	}
	list, ok := envelope["results"].([]any)
	if !ok {
		t.Fatalf("results is not an array: %s", resp.Raw)
	}
	var out struct {
		Results []directoryEntry `json:"results"`
	}
	if err := json.Unmarshal(resp.Raw, &out); err != nil {
		t.Fatalf("the user directory answers %d entries this test cannot read: %v\n%s", len(list), err, resp.Raw)
	}
	return out.Results
}

func hasString(haystack []string, needle string) bool {
	for _, value := range haystack {
		if value == needle {
			return true
		}
	}
	return false
}
