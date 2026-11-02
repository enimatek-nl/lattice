// Package unit holds offline unit tests: pure functions tested directly, with no
// server, no database and no network. The black-box suite in tests/compat covers
// the same behaviour through HTTP; these tests exist so the rules that federation
// will build on (identifiers, errors, power levels, push rules, tokens) fail with
// a readable table diff instead of a confusing HTTP response.
package unit

import (
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestAliases pins the room alias helpers. Aliases are the only Matrix identifier
// whose localpart may contain a slash, which is how clients nest aliases such as
// "#lattice/dev:example.com".
func TestAliases(t *testing.T) {
	tests := []struct {
		alias   matrix.Alias
		valid   bool
		name    string
		domain  string
		comment string
	}{
		{alias: "#lattice:example.com", valid: true, name: "lattice", domain: "example.com"},
		{alias: "#lattice/dev:example.com", valid: true, name: "lattice/dev", domain: "example.com",
			comment: "a slash nests an alias, which the specification allows"},
		{alias: "#room-1_2=3:example.com", valid: true, name: "room-1_2=3", domain: "example.com"},
		{alias: "#MissingSignatures:peer.example", valid: true, name: "MissingSignatures", domain: "peer.example",
			comment: "another server named this room; recognising it is not this server's to refuse"},
		{alias: "#", valid: false, name: "", domain: "", comment: "no localpart and no domain"},
		{alias: "lattice:example.com", valid: false, name: "lattice", domain: "example.com",
			comment: "invalid, but readable: the sigil is what makes an alias an alias"},
		{alias: "#lattice", valid: false, name: "lattice", domain: "",
			comment: "an alias without a server name cannot be resolved"},
		{alias: "#lat tice:example.com", valid: false, name: "lat tice", domain: "example.com",
			comment: "spaces are not in the localpart character set"},
		{alias: "#lattice:", valid: false, name: "lattice", domain: "",
			comment: "the domain may not be empty"},
	}
	for _, tt := range tests {
		if got := tt.alias.Valid(); got != tt.valid {
			t.Errorf("%s: Valid() = %v, want %v", tt.alias, got, tt.valid)
		}
		if got := tt.alias.Localpart(); got != tt.name {
			t.Errorf("%s: Localpart() = %q, want %q", tt.alias, got, tt.name)
		}
		if got := tt.alias.Domain(); got != tt.domain {
			t.Errorf("%s: Domain() = %q, want %q", tt.alias, got, tt.domain)
		}
		if got := tt.alias.String(); got != string(tt.alias) {
			t.Errorf("%s: String() = %q", tt.alias, got)
		}
	}
}

// TestValidLocalpart pins which localparts the homeserver accepts. The
// specification reserves a small character set for user and alias localparts; a
// server that accepts more cannot round-trip those IDs over federation.
func TestValidLocalpart(t *testing.T) {
	tests := []struct {
		localpart string
		accepted  bool
		comment   string
	}{
		{localpart: "alice", accepted: true},
		{localpart: "a0.-_/=", accepted: true,
			comment: "digits and the six punctuation marks the specification lists"},
		{localpart: "", accepted: false, comment: "an empty localpart names nobody"},
		{localpart: "Alice", accepted: false, comment: "user IDs are case sensitive lowercase"},
		{localpart: "über", accepted: false, comment: "non-ASCII is not in the reserved set"},
		{localpart: "a b", accepted: false},
		{localpart: "a:b", accepted: false, comment: "the separator of an ID is not part of a localpart"},
		{localpart: "@alice", accepted: false, comment: "the sigil is added by the caller"},
		{localpart: "café", accepted: false},
	}
	for _, tt := range tests {
		if got := matrix.ValidLocalpart(tt.localpart); got != tt.accepted {
			t.Errorf("ValidLocalpart(%q) = %v, want %v", tt.localpart, got, tt.accepted)
		}
	}
}

// TestUserMxid pins how a user ID is assembled. An ID already carrying a domain
// is left alone: a client that registers against example.com but sends
// "@alice:other.com" must not silently end up with a local user.
func TestUserMxid(t *testing.T) {
	tests := []struct {
		localpart, server, want string
		comment                 string
	}{
		{localpart: "alice", server: "example.com", want: "@alice:example.com"},
		{localpart: "@alice", server: "example.com", want: "@alice:example.com",
			comment: "a leading sigil is not repeated"},
		{localpart: "@alice:other.com", server: "example.com", want: "@alice:other.com",
			comment: "an explicit domain wins: remote IDs stay remote"},
		{localpart: "alice:other.com", server: "example.com", want: "@alice:other.com",
			comment: "the sigil is added even when the domain is already there"},
	}
	for _, tt := range tests {
		if got := matrix.UserMxid(tt.localpart, tt.server); got != tt.want {
			t.Errorf("UserMxid(%q, %q) = %q, want %q", tt.localpart, tt.server, got, tt.want)
		}
	}
}

// TestIdentifierParts pins the splitting of identifiers into localpart and
// domain. The domain is everything after the first colon, so a domain carrying a
// port stays in one piece.
func TestIdentifierParts(t *testing.T) {
	tests := []struct {
		id        string
		localpart string
		domain    string
		comment   string
	}{
		{id: "@alice:example.com", localpart: "alice", domain: "example.com"},
		{id: "!room:example.com", localpart: "!room", domain: "example.com",
			comment: "LocalpartOf only strips the user sigil; room IDs keep theirs"},
		{id: "$event:example.com:1875", localpart: "$event", domain: "example.com:1875",
			comment: "a host:port domain is one domain"},
		{id: "no-colon", localpart: "no-colon", domain: "",
			comment: "an identifier without a domain has no server to send it to"},
		{id: "", localpart: "", domain: ""},
	}
	for _, tt := range tests {
		if got := matrix.DomainOf(tt.id); got != tt.domain {
			t.Errorf("DomainOf(%q) = %q, want %q", tt.id, got, tt.domain)
		}
		if got := matrix.LocalpartOf(tt.id); got != tt.localpart {
			t.Errorf("LocalpartOf(%q) = %q, want %q", tt.id, got, tt.localpart)
		}
	}
}

// TestGeneratedIDs pins the shape of the identifiers the server hands out. They
// carry the sigil the specification requires, and two calls never return the same
// event ID: a transaction that reuses an event ID overwrites the first event.
func TestGeneratedIDs(t *testing.T) {
	if got := matrix.NewRoomID("example.com"); got == "" || got[0] != '!' || matrix.DomainOf(got) != "example.com" {
		t.Errorf("NewRoomID = %q, want !...:example.com", got)
	}
	for _, tt := range []struct {
		id    string
		name  string
		begin byte
	}{
		{id: matrix.NewEventID(), name: "NewEventID", begin: '$'},
	} {
		if tt.id == "" || tt.id[0] != tt.begin {
			t.Errorf("%s = %q, want it to start with %q", tt.name, tt.id, string(tt.begin))
		}
	}
	// Media IDs are opaque strings without a sigil: the mxc:// URL supplies the
	// scheme, so a media ID only has to be unique and URL safe.
	if media := matrix.NewMediaID(); media == "" || strings.ContainsAny(media, "/:") {
		t.Errorf("NewMediaID = %q, want a non-empty opaque id without a separator", media)
	}
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := matrix.NewEventID()
		if seen[id] {
			t.Fatalf("NewEventID returned %s twice", id)
		}
		seen[id] = true
	}
	if got := matrix.NewDeviceID(); len(got) != 10 {
		t.Errorf("NewDeviceID = %q, want 10 characters", got)
	}
	if got := matrix.NewUserLocalpart(); !matrix.ValidLocalpart(got) {
		t.Errorf("NewUserLocalpart = %q, which the server would reject", got)
	}
}
