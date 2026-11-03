package federation

import (
	"net/http"
	"net/url"
	"testing"
)

// An OpenID token is a promise one server makes about one of its own users: it is minted
// here, and only here can it be turned back into the user it stands for. These tests check
// both that the minting server answers, and that no other server can be made to answer - a
// token that worked anywhere would be a user identity invented by whoever held it.

// TestAnOpenIDTokenIsWorthWhatItsServerSays walks the whole pair of endpoints: a user mints
// a token, and the server that issued it is asked what the token means.
func TestAnOpenIDTokenIsWorthWhatItsServerSays(t *testing.T) {
	c := newCluster(t)
	one, two := c.one(), c.two()
	alice := one.register("alice", "wonderland")
	two.register("bob", "wonderland")

	minted := one.mustDo(http.StatusOK, http.MethodPost,
		"/_matrix/client/v3/user/"+url.PathEscape(alice.UserID)+"/openid/request_token", alice.Token, nil)
	token := minted.stringField("access_token")
	if token == "" {
		t.Fatalf("minting an OpenID token for %s returned no access token: %s", alice.UserID, minted.Raw)
	}
	if minted.stringField("matrix_server_name") != one.name {
		t.Errorf("the OpenID token of %s is offered by %q, which is not the server that minted it (%s): %s",
			alice.UserID, minted.stringField("matrix_server_name"), one.name, minted.Raw)
	}

	// The issuing server answers, and answers about its own user.
	answered := one.do(http.MethodGet, "/_matrix/federation/v1/openid/userinfo?access_token="+url.QueryEscape(token), "", nil)
	if answered.Status != http.StatusOK {
		t.Fatalf("asking %s what its own OpenID token stands for: %d %s",
			one.name, answered.Status, answered.Raw)
	}
	if answered.stringField("sub") != alice.UserID {
		t.Errorf("the OpenID token of %s is answered with %q", alice.UserID, answered.stringField("sub"))
	}

	// A stranger's server cannot be asked about it. It did not mint the token, it cannot
	// know what it means, and an answer here would be a claim about somebody else's user.
	elsewhere := two.do(http.MethodGet, "/_matrix/federation/v1/openid/userinfo?access_token="+url.QueryEscape(token), "", nil)
	if elsewhere.Status == http.StatusOK {
		t.Errorf("%s answered what an OpenID token of %s stands for: %s", two.name, one.name, elsewhere.Raw)
	}
	if elsewhere.Status != http.StatusUnauthorized {
		t.Errorf("%s was asked about a token it did not mint and answered %d, want %d: %s",
			two.name, elsewhere.Status, http.StatusUnauthorized, elsewhere.Raw)
	}

	// A token that was never minted is refused in the same words as one that has expired or
	// was never ours: the asker cannot act on the difference, and the difference is not
	// information this server needs to volunteer.
	for name, path := range map[string]string{
		"a token nobody issued": "/_matrix/federation/v1/openid/userinfo?access_token=not-a-token",
		"no token at all":       "/_matrix/federation/v1/openid/userinfo",
	} {
		got := one.do(http.MethodGet, path, "", nil)
		if got.Status != http.StatusUnauthorized {
			t.Errorf("asking %s about %s was answered %d, want %d: %s",
				one.name, name, got.Status, http.StatusUnauthorized, got.Raw)
		}
		if sub := got.stringField("sub"); sub != "" {
			t.Errorf("asking %s about %s still named a user (%s): %s", one.name, name, sub, got.Raw)
		}
	}

	// The token is a credential of one server, so the user of the other server has nothing
	// to exchange: a token minted for one user is not a way to speak for another.
	other := two.do(http.MethodGet, "/_matrix/federation/v1/openid/userinfo?access_token="+url.QueryEscape(token), "", nil)
	if other.stringField("sub") != "" {
		t.Errorf("%s was named by an OpenID token minted by %s: %s", other.stringField("sub"), one.name, other.Raw)
	}
}

// TestAnOpenIDTokenIsNotAKey checks the boundary that matters while this endpoint exists: it
// identifies a user, it does not act as one. Everything it can say is one name.
func TestAnOpenIDTokenIsNotAKey(t *testing.T) {
	c := newCluster(t)
	one := c.one()
	alice := one.register("alice", "wonderland")

	minted := one.mustDo(http.StatusOK, http.MethodPost,
		"/_matrix/client/v3/user/"+url.PathEscape(alice.UserID)+"/openid/request_token", alice.Token, nil)
	token := minted.stringField("access_token")

	// The answer is a name and nothing that reads like a session.
	answered := one.do(http.MethodGet,
		"/_matrix/federation/v1/openid/userinfo?access_token="+url.QueryEscape(token), "", nil)
	if answered.Status != http.StatusOK {
		t.Fatalf("asking about an OpenID token: %d %s", answered.Status, answered.Raw)
	}
	if answered.stringField("sub") != alice.UserID {
		t.Errorf("the answer about an OpenID token names %q, want %s", answered.stringField("sub"), alice.UserID)
	}
	for _, key := range []string{"access_token", "refresh_token", "device_id", "token"} {
		if value, found := answered.Body[key]; found {
			t.Errorf("the answer about an OpenID token includes %s=%v, which is more than a name", key, value)
		}
	}

	// And the token does not work as the user's credential on the client API.
	used := one.do(http.MethodGet, "/_matrix/client/v3/whoami", token, nil)
	if used.Status == http.StatusOK {
		t.Errorf("an OpenID token was accepted as a client access token: %s", used.Raw)
	}
}
