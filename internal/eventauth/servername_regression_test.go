package eventauth

import "testing"

// TestServerNameReadsServerNamesWithPorts pins the identifier grammar authorisation depends on.
//
// A Matrix server name may end in a port, and the domain of an identifier begins at its first
// colon. Splitting at the last colon reads `example.org:8448` as `8448`, and this function answers
// the rules that ask whether a membership event's target lives here, whether a room's creator
// belongs to the room's own server, and which server had to sign an authorising event — so a wrong
// split does not crash, it decides those rules wrongly for every server that federates on a port,
// which includes most of them.
func TestServerNameReadsServerNamesWithPorts(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"@alice:example.org:8448", "example.org:8448"},
		{"@alice:example.org", "example.org"},
		{"!opaque:example.org:8448", "example.org:8448"},
		{"#room:example.org:8448", "example.org:8448"},
		{"@alice", ""},
		{"", ""},
		// An event ID's opaque part may itself contain colons, and an event ID is not an
		// identifier that belongs to a server, so authorisation gets no server out of it.
		{"$something:else:example.org", ""},
		{"nonsense", ""},
	}
	for _, tc := range cases {
		if got := serverName(tc.id); got != tc.want {
			t.Errorf("serverName(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
