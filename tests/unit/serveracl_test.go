package unit

import (
	"testing"

	"github.com/enimatek/lattice/internal/serveracl"
)

// TestServerACLMatchesServerNamesTheWayRoomsWriteThem pins the glob semantics of an
// `m.room.server_acl`: wildcards, case, and the port that is not part of the comparison.
func TestServerACLMatchesServerNamesTheWayRoomsWriteThem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		host    string
		want    bool
		why     string
	}{
		{"*", "anything.test", true, "the whole world"},
		{"evil.com", "evil.com", true, "an exact name"},
		{"evil.com", "notevil.com", false, "a name that merely ends with it"},
		{"*.matrix.org", "server.matrix.org", true, "one label under a domain"},
		{"*.matrix.org", "a.b.matrix.org", true, "a wildcard is not anchored at dots"},
		{"*.matrix.org", "matrix.org", false, "a wildcard needs something before the dot"},
		{"matrix??.org", "matrix02.org", true, "? takes exactly one character"},
		{"matrix??.org", "matrix1.org", false, "? does not take two"},
		{"EVIL.com", "evil.COM", true, "matching ignores case"},
		{"[2001:db8:1234::1]", "[2001:db8:1234::2]", false, "one IPv6 literal is not another"},
		{"[2001:db8:1234::1]", "[2001:db8:1234::1]", true, "the brackets are part of the name"},
		{"1.2.3.4", "1.2.3.4", true, "an IPv4 literal"},
	}
	for _, tc := range cases {
		if got := serveracl.Match(tc.pattern, tc.host); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v (%s)", tc.pattern, tc.host, got, tc.want, tc.why)
		}
	}
}

// TestServerACLComparesTheHostNotTheEndpoint: a room excludes a homeserver, not a TCP endpoint, so
// the port a server happens to listen on must not be a way round the exclusion.
func TestServerACLComparesTheHostNotTheEndpoint(t *testing.T) {
	t.Parallel()
	acl, understood := serveracl.Parse(map[string]any{"allow": []any{"*"}, "deny": []any{"evil.com"}})
	if !understood {
		t.Fatal("a plain ACL was not understood")
	}
	for _, name := range []string{"evil.com", "evil.com:8448", "evil.com:1234"} {
		if acl.Allows(name) {
			t.Errorf("%s was allowed by an ACL that denies evil.com", name)
		}
	}
	if !acl.Allows("good.com:9999") {
		t.Errorf("an allowed server was refused because of the port it listens on")
	}
	if host := serveracl.Host("[2001:db8::1]:8448"); host != "[2001:db8::1]" {
		t.Errorf("the host of a bracketed IPv6 server name is %q", host)
	}
}

// TestServerACLDecidesInTheOrderTheSpecificationGives covers the precedence between IP literals,
// deny and allow — including the case that surprises people: an ACL with no allow list excludes
// every server, which is also what a redacted ACL event amounts to.
func TestServerACLDecidesInTheOrderTheSpecificationGives(t *testing.T) {
	t.Parallel()

	// A malformed ACL is not applied. `allow` written as a string rather than a list is a mistake
	// of shape, and reading it as "no allow list" would exclude every server in the room.
	if _, understood := serveracl.Parse(map[string]any{"allow": "evil.com"}); understood {
		t.Error("an ACL whose allow list is not a list was read as if it made sense")
	}
	if _, understood := serveracl.Parse(map[string]any{"deny": []any{42}}); understood {
		t.Error("an ACL holding a non-string pattern was read as if it made sense")
	}
	if _, understood := serveracl.Parse(map[string]any{"allow_ip_literals": "yes please"}); understood {
		t.Error("an ACL whose IP-literal flag is not a boolean was read as if it made sense")
	}

	// Well-formed but empty: the specification's own consequence is that nobody is allowed.
	empty, understood := serveracl.Parse(map[string]any{})
	if !understood {
		t.Fatal("an ACL with no fields should be understood; the fields are optional")
	}
	if empty.Allows("anyone.test") {
		t.Error("an ACL with no allow list admitted a server, although an absent allow list " +
			"excludes every server — including what a redacted ACL event leaves behind")
	}

	// Deny is consulted first and wins. A name repeated in `allow` does not rescue a server that
	// `deny` covers — the two lists are not a general allow-over-deny override, and a room that
	// wants to except one server from a broad deny has to write the deny narrowly.
	acl, _ := serveracl.Parse(map[string]any{
		"allow": []any{"*.matrix.org", "sub.matrix.org"},
		"deny":  []any{"*"},
	})
	if acl.Allows("elsewhere.test") {
		t.Error("a server outside the allow list was admitted by an ACL that denies *")
	}
	if acl.Allows("sub.matrix.org") {
		t.Error("a server covered by deny was admitted because it was named in allow as well")
	}
	if !serveracl.Match("sub.matrix.org", "sub.matrix.org") {
		t.Error("the pattern itself does not match; the test is measuring the wrong thing")
	}

	// IP literals are refused when the room says so, whatever the lists say about them.
	literal, _ := serveracl.Parse(map[string]any{
		"allow_ip_literals": false,
		"allow":             []any{"*"},
	})
	if literal.Allows("198.51.100.7") || literal.Allows("[2001:db8::1]:8448") {
		t.Error("a room that does not allow IP literals accepted a server named by an address")
	}
	if !literal.Allows("named.server") {
		t.Error("refusing IP literals spilled over onto named servers")
	}
	opened, _ := serveracl.Parse(map[string]any{"allow": []any{"*"}})
	if !opened.Allows("198.51.100.7") {
		t.Error("the default for allow_ip_literals is to allow them")
	}
}
