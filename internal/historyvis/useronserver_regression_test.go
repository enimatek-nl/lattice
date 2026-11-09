package historyvis

import "testing"

// TestUserOnServerAcceptsServerNamesWithPorts is a regression test for a split on the wrong colon:
// a server name is allowed to end in a port, and comparing the part after the last colon against a
// full server name matches nothing, which reads as "this peer is in no room" for every room the
// peer is actually in.
func TestUserOnServerAcceptsServerNamesWithPorts(t *testing.T) {
	cases := []struct {
		userID, serverName string
		want               bool
	}{
		{"@charlie:host.docker.internal:1024", "host.docker.internal:1024", true},
		{"@alice:example.org:8448", "example.org:8448", true},
		{"@alice:example.org:8448", "example.org", false},
		{"@alice:example.org", "example.org:8448", false},
		{"@alice:example.org", "example.org", true},
		{"charlie:no-sigil", "no-sigil", false},
		{"", "somewhere", false},
	}
	for _, tc := range cases {
		if got := userOnServer(tc.userID, tc.serverName); got != tc.want {
			t.Errorf("userOnServer(%q, %q) = %v, want %v", tc.userID, tc.serverName, got, tc.want)
		}
	}
}
