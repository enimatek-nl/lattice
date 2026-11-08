package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/userdir"
)

// The user directory has one interesting question: whose name may a match be made on. A search that
// finds everybody by every name they have ever written somewhere is a leak with good recall — it is
// what Synapse once shipped, where Alice tells one private room she goes by "Freddy" and a stranger
// searching "Freddy" is told who Alice is. These rows fix the rule down to the person, the room and
// the name.

// world is the smallest thing the rule needs: a room the searcher shares, a room open to strangers,
// and Alice called one name in each.
//
//	@alice:example.org  profile "Alice Cooper"; "Freddy" in !shared; "Alice" in !open
//	@bob:example.org    no profile; in !shared
//	@eve:other.org      no profile; in !open
type world struct {
	joined  map[string][]string
	open    []string
	members map[string][]persist.RoomMemberName
	people  map[string]*persist.User
}

func newWorld() *world {
	return &world{
		joined: map[string][]string{
			"@alice:example.org": {"!shared:example.org", "!open:example.org"},
			"@bob:example.org":   {"!shared:example.org"},
			"@eve:other.org":     {"!open:example.org"},
		},
		open: []string{"!open:example.org"},
		members: map[string][]persist.RoomMemberName{
			"!shared:example.org": {
				{UserID: "@alice:example.org", DisplayName: "Freddy"},
				{UserID: "@bob:example.org"},
			},
			"!open:example.org": {
				{UserID: "@alice:example.org", DisplayName: "Alice"},
				{UserID: "@eve:other.org", DisplayName: "Eve"},
			},
		},
		people: map[string]*persist.User{
			"@alice:example.org": {UserID: "@alice:example.org", Displayname: "Alice Cooper"},
			"@bob:example.org":   {UserID: "@bob:example.org"},
			"@eve:other.org":     {UserID: "@eve:other.org"},
		},
	}
}

// dir implements [persist.UserDirectoryRepo] over a world. It models the read's contract rather than
// its SQL: one row per (room, user) whose member event says join, which is what the service is told
// it may rely on.
type dir struct{ f *world }

func (d dir) WithTx(*gorm.DB) persist.UserDirectoryRepo { return d }

func (d dir) JoinedRoomIDs(_ context.Context, userID string) ([]string, error) {
	return d.f.joined[userID], nil
}

func (d dir) OpenRoomIDs(_ context.Context) ([]string, error) { return d.f.open, nil }

func (d dir) JoinedNamesIn(_ context.Context, roomIDs []string) ([]persist.RoomMemberName, error) {
	var out []persist.RoomMemberName
	for _, roomID := range roomIDs {
		for _, member := range d.f.members[roomID] {
			member.RoomID = roomID
			out = append(out, member)
		}
	}
	return out, nil
}

// accounts is the one read of the account store a search performs.
type accounts struct{ f *world }

func (a accounts) ByUserIDs(_ context.Context, userIDs []string) (map[string]*persist.User, error) {
	out := map[string]*persist.User{}
	for _, userID := range userIDs {
		if user, ok := a.f.people[userID]; ok {
			out[userID] = user
		}
	}
	return out, nil
}

func directory(f *world) *userdir.Service { return userdir.NewWith(dir{f: f}, accounts{f: f}) }

func TestAUserDirectorySearchMatchesWhomTheSearcherMaySee(t *testing.T) {
	tests := []struct {
		name     string
		searcher string
		term     string
		limit    int
		want     []string
		// wantDisplayName pins the name the answer states, for the first result: the specification's
		// response is the person's display name, not whichever name the match happened to land on.
		wantDisplayName string
		wantLimited     bool
	}{
		{
			name:     "a stranger finds a member of an open room by their profile name",
			searcher: "@bob:example.org",
			term:     "Alice Cooper",
			// Bob is not in the open room, so he reaches Alice only through it — and is answered with
			// the name she goes by everywhere, which is what the response states.
			want:            []string{"@alice:example.org"},
			wantDisplayName: "Alice Cooper",
		},
		{
			name:     "a stranger cannot be matched on a name written in a room they are not in",
			searcher: "@eve:other.org",
			term:     "Freddy",
			// Eve shares no room with Alice. "Freddy" is a fact about !shared, and !shared is not hers
			// to be told about — not even that it exists.
			want: nil,
		},
		{
			name:     "the room that holds that name may be matched on it",
			searcher: "@bob:example.org",
			term:     "freddy",
			// The same term from a member of the room that says it. Case is not part of the rule.
			want: []string{"@alice:example.org"},
		},
		{
			name:     "a user ID is searchable",
			searcher: "@eve:other.org",
			term:     "@alice:example.org",
			want:     []string{"@alice:example.org"},
		},
		{
			name:     "a word of a user ID finds the person whose ID carries it",
			searcher: "@alice:example.org",
			term:     "bob",
			// Alice shares a room with Bob, so Bob is somebody she may be told about.
			want: []string{"@bob:example.org"},
		},
		{
			name:     "a term with no word in it finds nobody rather than everybody",
			searcher: "@bob:example.org",
			term:     "@:-.",
			want:     nil,
		},
		{
			name:     "a stranger in an open room is findable by the name that room holds for them",
			searcher: "@bob:example.org",
			term:     "eve",
			// Bob shares no room with Eve. She is findable because she is joined to a room whose
			// members are visible to strangers, which is the specification's second allowed set.
			want: []string{"@eve:other.org"},
		},
		{
			name:     "a term of one letter finds everybody whose words start with it, in a stable order",
			searcher: "@alice:example.org",
			term:     "e",
			limit:    1,
			// Bob and Eve both have a word starting with "e" in their user IDs, and Alice is not in her
			// own results. Of the two, Bob's ID sorts first, and the tenth is what the limit keeps.
			want:        []string{"@bob:example.org"},
			wantLimited: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := directory(newWorld()).Search(context.Background(), tc.searcher, tc.term, tc.limit)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			got := make([]string, 0, len(page.Results))
			for _, entry := range page.Results {
				got = append(got, entry.UserID)
			}
			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("search %q as %s found %v, want nobody", tc.term, tc.searcher, got)
				}
				return
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("search %q as %s found %v, want %v", tc.term, tc.searcher, got, tc.want)
			}
			if tc.wantDisplayName != "" && page.Results[0].DisplayName != tc.wantDisplayName {
				t.Errorf("the answer states %q, want the profile name %q",
					page.Results[0].DisplayName, tc.wantDisplayName)
			}
			if page.Limited != tc.wantLimited {
				t.Errorf("limited is %v, want %v", page.Limited, tc.wantLimited)
			}
		})
	}
}

func TestTheSearcherIsNotOneOfTheResults(t *testing.T) {
	// The same term, asked by two people. Bob is shown Alice; Alice, asking about herself, is shown
	// nobody — which is the same rule `TestRoomSpecificUsernameAtJoin` needs in order to expect
	// exactly one result when the term matches the searcher's own user ID as well.
	if page, err := directory(newWorld()).Search(context.Background(), "@bob:example.org", "alice", 0); err != nil {
		t.Fatalf("search: %v", err)
	} else if len(page.Results) != 1 || page.Results[0].UserID != "@alice:example.org" {
		t.Fatalf("Bob searching for Alice found %v, want just Alice", page.Results)
	}
	if page, err := directory(newWorld()).Search(context.Background(), "@alice:example.org", "alice", 0); err != nil {
		t.Fatalf("search: %v", err)
	} else if len(page.Results) != 0 {
		t.Fatalf("Alice searching for herself found %v, want nobody", page.Results)
	}
}

func TestAUserDirectorySkipsWhoeverIsNoLongerToBeFound(t *testing.T) {
	t.Run("an account that has been closed is not findable", func(t *testing.T) {
		// Closing an account leaves its member events where they are — history keeps the names it was
		// written under — but a closed account is not a person to be put in front of somebody.
		f := newWorld()
		f.people["@alice:example.org"] = &persist.User{
			UserID:      "@alice:example.org",
			Displayname: "Alice Cooper",
			Deactivated: true,
		}
		page, err := directory(f).Search(context.Background(), "@eve:other.org", "Alice Cooper", 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(page.Results) != 0 {
			t.Fatalf("a closed account is still findable: %v", page.Results)
		}
	})

	t.Run("a name from a room the searcher does not share is not matched even when both see her", func(t *testing.T) {
		// Alice has left the shared room. Bob may still find her through the open room, and may
		// still not search her by the name she wrote in the room he used to share with her, because
		// the read that answers the directory returns rooms whose member events say join.
		f := newWorld()
		delete(f.members, "!shared:example.org")
		f.joined["@alice:example.org"] = []string{"!open:example.org"}

		page, err := directory(f).Search(context.Background(), "@bob:example.org", "Freddy", 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(page.Results) != 0 {
			t.Fatalf("a name from a room nobody holds any more matched: %v", page.Results)
		}
		// Her profile name still finds her, because she is joined to a room whose members are public.
		page, err = directory(f).Search(context.Background(), "@bob:example.org", "Alice Cooper", 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(page.Results) != 1 || page.Results[0].UserID != "@alice:example.org" {
			t.Fatalf("an open room's member stopped being findable by profile name: %v", page.Results)
		}
	})

	t.Run("a searcher in no room at all is told about nobody", func(t *testing.T) {
		// The specification's minimum set for somebody in no room is empty, and answering with the
		// whole server would be answering a different question.
		f := newWorld()
		f.joined["@bob:example.org"] = nil
		f.open = nil
		page, err := directory(f).Search(context.Background(), "@bob:example.org", "alice", 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(page.Results) != 0 {
			t.Fatalf("a searcher with no rooms was told about people: %v", page.Results)
		}
	})
}

func TestATokenisedSearchFindsWordsRatherThanStrings(t *testing.T) {
	// Matching is on words, because the terms people type are words and because a Matrix user ID is
	// built from words separated by characters a localpart cannot carry. A single string comparison
	// would find neither "Cooper" inside "Alice Cooper" nor "user" inside "@user-1-alice:hs1", which
	// is how Complement looks a user up by "the MXID up to the first dash".
	f := newWorld()
	for _, tc := range []struct {
		term string
		want int
	}{
		{"alice", 1},
		{"cooper", 1},
		{"Coop", 1},
		{"alice cooper", 1},
		{"cooper alice", 1},
		{"alice freddy", 0},
		{"zzz", 0},
		{"  alice  ", 1},
		{"@eve:other.org", 1},
		{"example", 1}, // one word of the user ID: the server part, which is a word like any other
	} {
		t.Run(strings.TrimSpace(tc.term), func(t *testing.T) {
			page, err := directory(f).Search(context.Background(), "@bob:example.org", tc.term, 0)
			if err != nil {
				t.Fatalf("search %q: %v", tc.term, err)
			}
			if len(page.Results) != tc.want {
				t.Fatalf("search %q found %d, want %d: %v", tc.term, len(page.Results), tc.want, page.Results)
			}
		})
	}
}

// The reads underneath the directory own two rules of their own, both of them about a member event
// that is not about anybody who is currently there: an event whose membership says leave, invite,
// ban or knock still names a person and still carries the name they were going by at the time. The
// service cannot see the difference — the read has already answered — so the rules are tested here,
// against the storage they are implemented in.
func TestTheDirectoryReadsSayWhoIsActuallyInTheRoom(t *testing.T) {
	db := openEventStore(t)
	ctx := context.Background()
	const openRoom = "!open:example.org"
	const closedRoom = "!private:example.org"
	const noRulesRoom = "!unwritten:example.org"

	stateEvent := func(t *testing.T, eventID, roomID, eventType, stateKey, contentJSON string) {
		t.Helper()
		body := fmt.Sprintf(`{"type":%q,"room_id":%q,"event_id":%q,"sender":"@alice:example.org","content":%s}`,
			eventType, roomID, eventID, contentJSON)
		storeRawEvent(t, db, eventID, roomID, eventType, strPtr(stateKey), body)
	}

	stateEvent(t, "$open-rules:example.org", openRoom, "m.room.join_rules", "", `{"join_rule":"public"}`)
	stateEvent(t, "$closed-rules:example.org", closedRoom, "m.room.join_rules", "", `{"join_rule":"invite"}`)
	// noRulesRoom is given no join rule at all: a room nobody opened is not a room somebody opened.
	stateEvent(t, "$open-hv:example.org", openRoom, "m.room.history_visibility", "", `{"history_visibility":"joined"}`)
	stateEvent(t, "$closed-hv:example.org", closedRoom, "m.room.history_visibility", "", `{"history_visibility":"world_readable"}`)

	stateEvent(t, "$joined:example.org", openRoom, "m.room.member", "@alice:example.org",
		`{"membership":"join","displayname":"Freddy","avatar_url":"mxc://example.org/abc"}`)
	stateEvent(t, "$invited:example.org", openRoom, "m.room.member", "@invited:example.org",
		`{"membership":"invite","displayname":"Invited Person"}`)
	stateEvent(t, "$left:example.org", openRoom, "m.room.member", "@left:example.org",
		`{"membership":"leave","displayname":"Left Person"}`)
	stateEvent(t, "$private-join:example.org", closedRoom, "m.room.member", "@bob:example.org",
		`{"membership":"join","displayname":"Bob"}`)
	stateEvent(t, "$knock:example.org", closedRoom, "m.room.member", "@knocker:example.org",
		`{"membership":"knock","displayname":"Knocker"}`)
	// Alice is in both rooms; Bob only in the closed one.
	stateEvent(t, "$alice-closed:example.org", closedRoom, "m.room.member", "@alice:example.org",
		`{"membership":"join","displayname":"Freds"}`)

	dirRepo := db.UserDirectory

	t.Run("an open room is the join rule or the history visibility, not the absence of either", func(t *testing.T) {
		got, err := dirRepo.OpenRoomIDs(ctx)
		if err != nil {
			t.Fatalf("open rooms: %v", err)
		}
		// Both rooms qualify — one by its join rule, one by its history visibility — and the room
		// whose rules were never written does not.
		want := []string{closedRoom, openRoom}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("open rooms = %v, want %v", got, want)
		}
		if slices.Contains(got, noRulesRoom) {
			t.Errorf("a room whose join rule was never written counts as open")
		}
	})

	t.Run("the names come back for members who are there and not for anybody else", func(t *testing.T) {
		got, err := dirRepo.JoinedNamesIn(ctx, []string{openRoom, closedRoom})
		if err != nil {
			t.Fatalf("joined names: %v", err)
		}
		byUser := map[string]persist.RoomMemberName{}
		for _, member := range got {
			byUser[member.UserID+"/"+member.RoomID] = member
		}
		if len(got) != 3 { // alice in two rooms, bob in one
			t.Fatalf("names returned: %v", got)
		}
		alice := byUser["@alice:example.org/"+openRoom]
		if alice.DisplayName != "Freddy" || alice.AvatarURL != "mxc://example.org/abc" {
			t.Errorf("the open room's name for Alice is %q/%q, want Freddy/mxc://example.org/abc",
				alice.DisplayName, alice.AvatarURL)
		}
		// A different room holds a different name for the same person, and that is the whole reason
		// the read returns one row per room rather than one row per person.
		if elsewhere := byUser["@alice:example.org/"+closedRoom]; elsewhere.DisplayName != "Freds" {
			t.Errorf("the closed room's name for Alice is %q, want Freds", elsewhere.DisplayName)
		}
		for _, absent := range []string{"@invited:example.org", "@left:example.org", "@knocker:example.org"} {
			if _, found := byUser[absent+"/"+openRoom]; found {
				t.Errorf("%s was returned as a member of the open room", absent)
			}
			if _, found := byUser[absent+"/"+closedRoom]; found {
				t.Errorf("%s was returned as a member of the closed room", absent)
			}
		}
	})

	t.Run("a room is only joined where the member event says so", func(t *testing.T) {
		joined, err := dirRepo.JoinedRoomIDs(ctx, "@alice:example.org")
		if err != nil {
			t.Fatalf("joined rooms: %v", err)
		}
		want := []string{closedRoom, openRoom}
		slices.Sort(want)
		if !slices.Equal(joined, want) {
			t.Fatalf("Alice's rooms = %v, want %v", joined, want)
		}
		// Somebody invited to a room is not in it, which is what keeps an invite from making the
		// inviter's other rooms visible to the person holding the invitation.
		invited, err := dirRepo.JoinedRoomIDs(ctx, "@invited:example.org")
		if err != nil {
			t.Fatalf("joined rooms: %v", err)
		}
		if len(invited) != 0 {
			t.Fatalf("an invited user is joined to %v", invited)
		}
	})
}

// storeRawEvent writes a state event whose bytes the caller chose, because what these reads answer
// depends on what the content says and nothing else.
func storeRawEvent(t *testing.T, db *persist.Database, eventID, roomID, eventType string, stateKey *string, body string) {
	t.Helper()
	ctx := context.Background()
	if !json.Valid([]byte(body)) {
		t.Fatalf("the test wrote an event that is not JSON: %s", body)
	}
	stream, err := db.Events.NextStreamOrdering(ctx)
	if err != nil {
		t.Fatalf("stream ordering: %v", err)
	}
	event := &persist.Event{
		EventID:        eventID,
		RoomID:         roomID,
		Type:           eventType,
		StateKey:       stateKey,
		Sender:         "@alice:example.org",
		Depth:          1,
		OriginServerTS: 1700000000000,
		ReceivedTS:     1700000001000,
		JSON:           []byte(body),
		StreamOrdering: stream,
	}
	if err := db.Events.Insert(ctx, event); err != nil {
		t.Fatalf("insert %s: %v", eventID, err)
	}
	if err := db.State.Set(ctx, &persist.RoomState{RoomID: roomID, Type: eventType, StateKey: *stateKey, EventID: eventID}); err != nil {
		t.Fatalf("state: %v", err)
	}
}
