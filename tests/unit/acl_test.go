package unit

import (
	"testing"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// An ACL is written by a moderator in a client, and enforced by every server in the room.
// The gap between those two is where the damage happens: a pattern that means something
// else to the server blocks either nothing or everybody, and a room cannot take a wrong
// ACL back - the denied server's own software decides whether to obey.

func TestServerACLs(t *testing.T) {
	t.Run("no ACL allows anyone", func(t *testing.T) {
		for _, content := range []matrix.Object{nil, {}} {
			if !federation.ServerAllowed(content, "anything.example") {
				t.Errorf("an empty ACL denies %s", "anything.example")
			}
		}
	})

	cases := []struct {
		name    string
		content matrix.Object
		server  string
		allowed bool
	}{
		{
			name:    "a denied name is denied",
			content: matrix.Object{"deny": []any{"evil.example"}},
			server:  "evil.example",
			allowed: false,
		},
		{
			name:    "an unrelated server is unaffected",
			content: matrix.Object{"deny": []any{"evil.example"}},
			server:  "good.example",
			allowed: true,
		},
		{
			name:    "deny is consulted before allow",
			content: matrix.Object{"allow": []any{"*"}, "deny": []any{"evil.example"}},
			server:  "evil.example",
			allowed: false,
		},
		{
			name:    "an allow list excludes the rest",
			content: matrix.Object{"allow": []any{"example.com", "example.org"}},
			server:  "somewhere.else",
			allowed: false,
		},
		{
			name:    "a star spans dots, so a subdomain is covered",
			content: matrix.Object{"deny": []any{"*.evil.example"}},
			server:  "cdn.evil.example",
			allowed: false,
		},
		{
			name:    "a star does not reach backwards",
			content: matrix.Object{"deny": []any{"*.evil.example"}},
			server:  "evil.example",
			allowed: true,
		},
		{
			name:    "a question mark matches one character",
			content: matrix.Object{"deny": []any{"evil?.example"}},
			server:  "evil1.example",
			allowed: false,
		},
		{
			name:    "a question mark is not a star",
			content: matrix.Object{"deny": []any{"evil?.example"}},
			server:  "evil12.example",
			allowed: true,
		},
		{
			name:    "matching ignores case",
			content: matrix.Object{"deny": []any{"Evil.Example"}},
			server:  "evil.EXAMPLE",
			allowed: false,
		},
		{
			name:    "the port is not part of the name an ACL matches",
			content: matrix.Object{"deny": []any{"evil.example"}},
			server:  "evil.example:8448",
			allowed: false,
		},
		{
			name:    "an allow entry matches a name with a port",
			content: matrix.Object{"allow": []any{"good.example"}},
			server:  "good.example:443",
			allowed: true,
		},
		{
			name:    "an IP literal is allowed unless the ACL says otherwise",
			content: matrix.Object{"deny": []any{"evil.example"}},
			server:  "198.51.100.7",
			allowed: true,
		},
		{
			name:    "an IP literal is denied when the ACL forbids literals",
			content: matrix.Object{"allow_ip_literals": false, "allow": []any{"*"}},
			server:  "198.51.100.7",
			allowed: false,
		},
		{
			name:    "an IPv6 literal is denied with the same rule",
			content: matrix.Object{"allow_ip_literals": false, "allow": []any{"*"}},
			server:  "2001:db8::1",
			allowed: false,
		},
		{
			name:    "a bracketed IPv6 literal with a port is read as the address",
			content: matrix.Object{"allow_ip_literals": false, "allow": []any{"*"}},
			server:  "[2001:db8::1]:8448",
			allowed: false,
		},
		{
			name:    "a named server is not an IP literal",
			content: matrix.Object{"allow_ip_literals": false, "allow": []any{"good.example"}},
			server:  "good.example",
			allowed: true,
		},
		{
			name:    "a wildcard allow admits anyone",
			content: matrix.Object{"allow": []any{"*"}},
			server:  "anyone.example",
			allowed: true,
		},
		{
			name:    "a pattern is not a regular expression",
			content: matrix.Object{"deny": []any{"evil(+)?\\.example"}},
			server:  "evil.example",
			allowed: true,
		},
		{
			name:    "a name that cannot be read is not allowed",
			content: matrix.Object{"allow": []any{"*"}},
			server:  "  ",
			allowed: false,
		},
		{
			name:    "entries that are not strings are ignored",
			content: matrix.Object{"deny": []any{42, nil, matrix.Object{}}},
			server:  "anyone.example",
			allowed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := federation.ServerAllowed(tc.content, tc.server); got != tc.allowed {
				t.Errorf("the ACL %v allows %s: %t, want %t", tc.content, tc.server, got, tc.allowed)
			}
		})
	}
}
