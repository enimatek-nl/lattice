package unit

import (
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/matrixid"
)

func TestServerNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value string
		valid bool
	}{
		{"dns", "matrix.org", true},
		{"dns with port", "matrix.org:8888", true},
		{"subdomain with hyphen", "matrix-internal.example.co.uk", true},
		{"uppercase is legal but distinct", "MATRIX.ORG", true},
		{"single label", "localhost", true},
		{"ipv4", "1.2.3.4", true},
		{"ipv4 with port", "1.2.3.4:1234", true},
		{"ipv6 literal", "[1234:5678::abcd]", true},
		{"ipv6 literal with port", "[1234:5678::abcd]:5678", true},
		{"empty", "", false},
		{"empty label", "example..org", false},
		{"bad port", "example.org:99999", false},
		{"non numeric port", "example.org:http", false},
		{"bad ipv4 octet", "1.2.3.256", false},
		{"unbalanced brackets", "[::1", false},
		{"invalid character", "exa_mple.org", false},
		{"trailing dot label", "example.org.", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := matrixid.ServerNameValid(tc.value)
			if tc.valid && err != nil {
				t.Errorf("ServerNameValid(%q) = %v, want valid", tc.value, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("ServerNameValid(%q) succeeded, want invalid", tc.value)
			}
		})
	}
}

func TestSplitServerName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		value  string
		host   string
		port   int
		wantOK bool
	}{
		{"no port", "example.org", "example.org", 0, true},
		{"with port", "example.org:8448", "example.org", 8448, true},
		{"ipv6 keeps brackets", "[::1]:8448", "[::1]", 8448, true},
		{"bad", "example.org:0x10", "", 0, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host, port, err := matrixid.SplitServerName(tc.value)
			if tc.wantOK != (err == nil) {
				t.Fatalf("SplitServerName(%q) error = %v, want ok=%v", tc.value, err, tc.wantOK)
			}
			if err == nil && (host != tc.host || port != tc.port) {
				t.Errorf("SplitServerName(%q) = (%q,%d), want (%q,%d)", tc.value, host, port, tc.host, tc.port)
			}
		})
	}
}

func TestUserLocalpartGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		localpart string
		grammarOK bool
		regOK     bool
	}{
		{"simple", "alice", true, true},
		{"dots underscore equals dash slash plus", "a.b_c=-/+9", true, true},
		{"digits", "0123456789", true, true},
		{"empty", "", false, false},
		{"uppercase forbidden", "Alice", false, false},
		{"space forbidden", "a b", false, false},
		{"colon forbidden", "a:b", false, false},
		{"at forbidden", "a@b", false, false},
		{"hash forbidden", "a#b", false, false},
		{"non ascii forbidden", "é", false, false},
		{"leading underscore grammar ok but policy rejects", "_synapse_bot", true, false},
		{"too long", strings.Repeat("a", 300), false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			grammarErr := matrixid.UserLocalpartValid(tc.localpart)
			if tc.grammarOK != (grammarErr == nil) {
				t.Errorf("UserLocalpartValid(%q) = %v, want ok=%v", tc.localpart, grammarErr, tc.grammarOK)
			}
			regErr := matrixid.ValidateRegistrationLocalpart(tc.localpart)
			if tc.regOK != (regErr == nil) {
				t.Errorf("ValidateRegistrationLocalpart(%q) = %v, want ok=%v", tc.localpart, regErr, tc.regOK)
			}
		})
	}
}

func TestNewUserIDDowncases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		username string
		want     string
		wantErr  bool
	}{
		{"mixed case", "user-UPPER", "@user-upper:hs1", false},
		{"already lower", "alice", "@alice:hs1", false},
		{"with symbols", "us,er", "", true},
		{"with space", "us er", "", true},
		{"with non ascii", "üs", "", true},
		{"leading underscore", "_bot", "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := matrixid.NewUserID(tc.username, "hs1")
			if tc.wantErr != (err != nil) {
				t.Fatalf("NewUserID(%q) error = %v, want error=%v", tc.username, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("NewUserID(%q) = %q, want %q", tc.username, got, tc.want)
			}
		})
	}
}

func TestUserValid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		id    string
		valid bool
	}{
		{"valid", "@alice:example.org", true},
		{"valid with port", "@alice:example.org:8448", true},
		{"no domain", "@alice", false},
		{"empty localpart", "@:example.org", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := matrixid.UserValid(tc.id)
			if tc.valid != (err == nil) {
				t.Errorf("UserValid(%q) = %v, want ok=%v", tc.id, err, tc.valid)
			}
		})
	}
}

func TestSplitIDAndParts(t *testing.T) {
	t.Parallel()
	userID := "@alice:example.org"
	localpart, err := matrixid.UserLocalpart(userID)
	if err != nil || localpart != "alice" {
		t.Fatalf("UserLocalpart(%q) = (%q,%v)", userID, localpart, err)
	}
	domain, err := matrixid.UserDomain(userID)
	if err != nil || domain != "example.org" {
		t.Fatalf("UserDomain(%q) = (%q,%v)", userID, domain, err)
	}
	if !matrixid.IsLocal("example.org", "example.org") {
		t.Errorf("IsLocal should be true")
	}
	if matrixid.IsLocal("EXAMPLE.ORG", "example.org") {
		t.Errorf("IsLocal must be case sensitive")
	}
	roomLocal, roomDomain, err := matrixid.SplitID(matrixid.SigilRoom, "!opaque:example.org")
	if err != nil || roomLocal != "opaque" || roomDomain != "example.org" {
		t.Fatalf("SplitID room = (%q,%q,%v)", roomLocal, roomDomain, err)
	}
	if _, _, err := matrixid.SplitID(matrixid.SigilRoom, "!opaque"); err != nil {
		t.Errorf("domain-less room ID should parse: %v", err)
	}
}

func TestRoomAliasAndEventIDs(t *testing.T) {
	t.Parallel()
	aliasCases := []struct {
		alias string
		valid bool
	}{
		{"#room:example.org", true},
		{"#weird ünïcode:example.org", true},
		{"#room", false},
		{"room:example.org", false},
		{"#room:bad_host", false},
	}
	for _, tc := range aliasCases {
		tc := tc
		t.Run("alias "+tc.alias, func(t *testing.T) {
			t.Parallel()
			err := matrixid.RoomAliasValid(tc.alias)
			if tc.valid != (err == nil) {
				t.Errorf("RoomAliasValid(%q) = %v, want ok=%v", tc.alias, err, tc.valid)
			}
		})
	}

	eventCases := []struct {
		eventID string
		valid   bool
	}{
		{"$Rqnc-F-dvnEYJTyHq_iKxU2bZ1CI92-kuZq3a5lr5Zg", true},
		{"$opaque:example.org", true},
		{"", false},
		{"Rqnc-no-sigil", false},
		{"$", false},
		{"$" + strings.Repeat("a", 300), false},
	}
	for _, tc := range eventCases {
		tc := tc
		t.Run("event "+safeName(tc.eventID), func(t *testing.T) {
			t.Parallel()
			err := matrixid.EventIDValid(tc.eventID)
			if tc.valid != (err == nil) {
				t.Errorf("EventIDValid(%q) = %v, want ok=%v", tc.eventID, err, tc.valid)
			}
		})
	}
}

func TestRoomIDValid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id    string
		valid bool
	}{
		{"!opaqueid:example.org", true},
		{"!opaqueid", true},
		{"!bad:id:with:colons", false},
		{"opaque:example.org", false},
		{"!nul\x00:example.org", false},
		{"!with\x02control:example.org", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("room "+safeName(tc.id), func(t *testing.T) {
			t.Parallel()
			err := matrixid.RoomIDValid(tc.id)
			if tc.valid != (err == nil) {
				t.Errorf("RoomIDValid(%q) = %v, want ok=%v", tc.id, err, tc.valid)
			}
		})
	}
}

func TestDeviceIDPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id    string
		valid bool
	}{
		{"my_device_id", true},
		{"xyzzy", true},
		{"Gibberish", true},
		{"", false},
		{"has space", false},
		{strings.Repeat("d", 101), false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("device "+safeName(tc.id), func(t *testing.T) {
			t.Parallel()
			err := matrixid.DeviceIDValid(tc.id)
			if tc.valid != (err == nil) {
				t.Errorf("DeviceIDValid(%q) = %v, want ok=%v", tc.id, err, tc.valid)
			}
		})
	}
}

func TestGenerateRandomString(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		value, err := matrixid.GenerateRandomString(nil, 32)
		if err != nil {
			t.Fatalf("GenerateRandomString failed: %v", err)
		}
		if len(value) != 43 {
			t.Fatalf("expected 43 unpadded url-safe base64 chars, got %d (%q)", len(value), value)
		}
		if seen[value] {
			t.Fatalf("collision generated twice: %q", value)
		}
		seen[value] = true
	}
}

// safeName renders an identifier as a legal subtest name.
func safeName(s string) string {
	if s == "" {
		return "<empty>"
	}
	r := []rune(s)
	if len(r) > 24 {
		r = r[:24]
	}
	return strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			return c
		default:
			return '_'
		}
	}, string(r))
}

// TestSplitMXC covers the content identifier a profile, a media URL and a room's image state all
// carry: `mxc://<server-name>/<media-id>`.
func TestSplitMXC(t *testing.T) {
	t.Parallel()
	valid := []struct {
		id, server, media string
	}{
		{"mxc://example.com/SEsfnsuifSDFSSEF", "example.com", "SEsfnsuifSDFSSEF"},
		{"mxc://example.org:8448/abc-DEF_123", "example.org:8448", "abc-DEF_123"},
		{"mxc://example.com/a.b!c=d+e", "example.com", "a.b!c=d+e"},
	}
	for _, tc := range valid {
		server, media, err := matrixid.SplitMXC(tc.id)
		if err != nil {
			t.Errorf("SplitMXC(%q) = %v, want %q and %q", tc.id, err, tc.server, tc.media)
			continue
		}
		if server != tc.server || media != tc.media {
			t.Errorf("SplitMXC(%q) = %q, %q, want %q, %q", tc.id, server, media, tc.server, tc.media)
		}
	}

	invalid := []string{
		"",
		"https://example.com/abc",
		"mxc://",
		"mxc://example.com",
		"mxc:///abc",
		"mxc://example.com/",
		"mxc://not a host/abc",
		"mxc://example.com/has space",
		"mxc://example.com/que?stion",
	}
	for _, id := range invalid {
		if server, media, err := matrixid.SplitMXC(id); err == nil {
			t.Errorf("SplitMXC(%q) = %q, %q, want an error", id, server, media)
		}
	}
}
