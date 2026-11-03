package federation

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A user ID names the server behind a user, and that server is the only one that can say
// what the user is called. These tests ask the question from both ends: a peer reading a
// name over federation, and a client of this server reading a name that lives a hop away.
// The two directions are the same two sentences - describe the users whose names you hold,
// and ask the server whose names you do not - and a server that gets either one wrong
// shows its member lists with half the people missing.

// setProfile writes one profile key of a user as a client does.
func setProfile(t *testing.T, s *server, sess *session, key, value string) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/profile/%s/%s", sess.UserID, key)
	s.mustDo(http.StatusOK, http.MethodPut, path, sess.Token, matrix.Object{key: value})
}

// TestAPeerCanAskWhatAUserIsCalled reads a local user's profile over federation: the field
// asked for, the field only, and both when no field is named.
func TestAPeerCanAskWhatAUserIsCalled(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	setProfile(t, one, alice, "displayname", "Alice Cooper")
	setProfile(t, one, alice, "avatar_url", "mxc://one.example/alice")

	for _, tc := range []struct {
		name  string
		field string
		want  matrix.Object
		empty []string
	}{
		{
			name:  "one field at a time",
			field: "displayname",
			want:  matrix.Object{"displayname": "Alice Cooper"},
		},
		{
			name:  "the other field",
			field: "avatar_url",
			want:  matrix.Object{"avatar_url": "mxc://one.example/alice"},
		},
		{
			name: "everything, when nothing was asked for",
			want: matrix.Object{"displayname": "Alice Cooper", "avatar_url": "mxc://one.example/alice"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := "/_matrix/federation/v1/query/profile?user_id=" + alice.UserID
			if tc.field != "" {
				target += "&field=" + tc.field
			}
			answer := federationGET(t, two, one, target)
			if answer.Status != http.StatusOK {
				t.Fatalf("%s asking %s for the profile of %s: %d %s",
					two.name, one.name, alice.UserID, answer.Status, answer.Raw)
			}
			for key, want := range tc.want {
				if answer.stringField(key) != want {
					t.Errorf("the profile of %s answers %s as %q, want %q: %s",
						alice.UserID, key, answer.stringField(key), want, answer.Raw)
				}
			}
			// A field the peer did not ask for is not handed over either: the question
			// names what is wanted, and answering more is not the same as answering right.
			if tc.field != "" {
				for _, key := range []string{"displayname", "avatar_url"} {
					if key == tc.field {
						continue
					}
					if _, found := answer.Body[key]; found {
						t.Errorf("a request for the %s of %s also answers %s: %s",
							tc.field, alice.UserID, key, answer.Raw)
					}
				}
			}
		})
	}

	// A user who never chose a name has no name to report. Answering with the empty string
	// would be a claim about what the user wrote rather than about what this server holds,
	// and it would leave the asking server no way to tell the two apart.
	carol := one.register("carol", "wonderland")
	quiet := federationGET(t, two, one, "/_matrix/federation/v1/query/profile?user_id="+carol.UserID)
	if quiet.Status != http.StatusOK {
		t.Fatalf("the profile of a user with nothing set: %d %s", quiet.Status, quiet.Raw)
	}
	for _, key := range []string{"displayname", "avatar_url"} {
		if value, found := quiet.Body[key]; found {
			t.Errorf("the profile of %s answers %s with %v, although it was never set", carol.UserID, key, value)
		}
	}
}

// TestAProfileQuestionThisServerCannotAnswer covers the two ways a profile question goes
// wrong: a name that belongs to somebody else, and a name that could not belong to anyone.
// The first is a miss, because this server has no idea what that user is called and an
// answer phrased like an authority is how a wrong name gets copied around a federation.
// The second is a bad request, because the asker wrote something that is not an address.
func TestAProfileQuestionThisServerCannotAnswer(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	bob := two.register("bob", "wonderland")
	setProfile(t, two, bob, "displayname", "Bob Ludd")

	for _, tc := range []struct {
		name   string
		userID string
		want   int
	}{
		{name: "a user of the asking server", userID: bob.UserID, want: http.StatusNotFound},
		{name: "a name that cannot address a server", userID: "@user1:localhost:http", want: http.StatusBadRequest},
		{name: "a name that is not a user id", userID: "alice", want: http.StatusBadRequest},
		{name: "a user this server never had", userID: "@nobody:" + one.name, want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := federationGET(t, two, one,
				"/_matrix/federation/v1/query/profile?user_id="+tc.userID+"&field=displayname")
			if answer.Status != tc.want {
				t.Errorf("asking %s about %s was answered %d, want %d: %s",
					one.name, tc.userID, answer.Status, tc.want, answer.Raw)
			}
			if answer.Status == http.StatusOK {
				t.Errorf("asking %s about %s answered %s, which says this server knows them",
					one.name, tc.userID, answer.Raw)
			}
		})
	}

	// The name that is held somewhere else is held somewhere: the asking server is not
	// being told the user does not exist, only that this server is not the place to learn
	// what they are called.
	about := federationGET(t, one, two, "/_matrix/federation/v1/query/profile?user_id="+bob.UserID)
	if about.Status != http.StatusOK || about.stringField("displayname") != "Bob Ludd" {
		t.Errorf("asking the server that holds %s about them: %d %s", bob.UserID, about.Status, about.Raw)
	}
}

// TestARemoteUserProfileComesFromItsOwnServer is the outbound half: a client of this server
// reads the name of a user who lives elsewhere, and this server asks rather than shrugs.
// The client is unauthenticated - a member list is read by people who have not signed in
// yet too - so the asking cannot depend on who is asking, and cannot let a stranger's
// outage turn into a failure of the read.
func TestARemoteUserProfileComesFromItsOwnServer(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	bob := two.register("bob", "wonderland")
	setProfile(t, two, bob, "displayname", "Bob Ludd")
	setProfile(t, two, bob, "avatar_url", "mxc://two.example/bob")

	whole := one.mustDo(http.StatusOK, http.MethodGet, "/_matrix/client/v3/profile/"+bob.UserID, "", nil)
	for key, want := range map[string]string{
		"displayname": "Bob Ludd",
		"avatar_url":  "mxc://two.example/bob",
	} {
		if got := whole.stringField(key); got != want {
			t.Errorf("a client of %s reads %s of %s as %q, want %q: %s", one.name, key, bob.UserID, got, want, whole.Raw)
		}
	}

	key := one.mustDo(http.StatusOK, http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/profile/%s/displayname", bob.UserID), "", nil)
	if key.stringField("displayname") != "Bob Ludd" {
		t.Errorf("one key of %s is answered %q: %s", bob.UserID, key.Raw, key.Raw)
	}

	// A key the remote user never set is a miss on this side too, rather than a key with
	// nothing in it.
	missing := one.mustDo(http.StatusNotFound, http.MethodGet,
		fmt.Sprintf("/_matrix/client/v3/profile/%s/unknown_key", bob.UserID), "", nil)
	if missing.errCode() != "M_NOT_FOUND" {
		t.Errorf("a key nobody set is answered %s: %s", missing.errCode(), missing.Raw)
	}

	// And a user whose own server cannot be reached: the read says it cannot tell, and the
	// client is not blamed for the internet's state.
	absent := one.mustDo(http.StatusNotFound, http.MethodGet,
		"/_matrix/client/v3/profile/@ghost:"+serverStranger, "", nil)
	if absent.errCode() != "M_NOT_FOUND" {
		t.Errorf("a user on a server that is not there is answered %s: %s", absent.errCode(), absent.Raw)
	}
}
