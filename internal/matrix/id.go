// Package matrix contains the Matrix specification data types, identifier
// helpers and error codes shared by the service and API layers.
package matrix

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode"
)

// Identifier alphabets used when minting new Matrix identifiers. The
// specification fixes these so clients can tell local from remote identifiers.
const (
	localpartAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789._=-/"
	opaqueAlphabet    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	alphanumeric      = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// opaqueLength is the number of random characters in minted opaque
// identifiers such as event, room and media IDs.
const opaqueLength = 22

// idCounter backs the deterministic fallback used only when crypto/rand
// misbehaves.
var idCounter uint64

// randInt returns a cryptographically secure random integer in [0, max).
func randInt(max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		idCounter++
		return int(idCounter % uint64(max))
	}
	return int(n.Int64())
}

// randomString returns n characters drawn from alphabet.
func randomString(alphabet string, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[randInt(len(alphabet))]
	}
	return string(out)
}

// NewUserLocalpart returns a random localpart suitable for a guest user ID.
func NewUserLocalpart() string {
	return randomString(localpartAlphabet, 10)
}

// NewRoomID returns a new random room identifier for the given server name,
// e.g. "!abc123:example.com".
func NewRoomID(serverName string) string {
	return "!" + randomString(opaqueAlphabet, opaqueLength) + ":" + serverName
}

// NewEventID returns a new random event identifier, e.g. "$abc123".
func NewEventID() string {
	return "$" + randomString(opaqueAlphabet, opaqueLength)
}

// NewDeviceID returns a new random device identifier.
func NewDeviceID() string {
	return randomString(alphanumeric, 10)
}

// NewFilterID returns a new random sync filter identifier.
func NewFilterID() string {
	return randomString(alphanumeric, 10)
}

// NewMediaID returns a new random media identifier.
func NewMediaID() string {
	return randomString(opaqueAlphabet, opaqueLength)
}

// NewToken returns a random opaque token used for access, refresh and
// one-time tokens.
func NewToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		idCounter++
		return fmt.Sprintf("%016x", idCounter)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// Alias represents a room alias such as "#lattice:example.com".
type Alias string

// String implements fmt.Stringer.
func (a Alias) String() string { return string(a) }

// Valid reports whether the alias is shaped like one: a "#", a local part, ":" and a
// domain. The local part is checked loosely, because an alias is normally read after
// another server made it, and a server that recognises only the aliases it would
// itself have written cannot resolve rooms that exist - a room called
// "#MissingSignatures:peer.example" is a room, and joining it is the point. The
// stricter rule for aliases this server creates is [ValidAliasLocalpart].
func (a Alias) Valid() bool {
	return aliasRe.MatchString(string(a)) && aliasLocalpartReadable(a.Localpart())
}

// Localpart returns the local part of the alias without the leading "#".
func (a Alias) Localpart() string {
	s := strings.TrimPrefix(string(a), "#")
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i]
	}
	return s
}

// Domain returns the server name part of the alias.
func (a Alias) Domain() string {
	if i := strings.Index(string(a), ":"); i >= 0 {
		return string(a)[i+1:]
	}
	return ""
}

// aliasRe is the outer shape of an alias: a "#", a local part, ":" and a domain.
// The local part itself is checked by ValidAliasLocalpart, which knows the
// Unicode range the specification allows and does not fit in an ASCII class.
var aliasRe = regexp.MustCompile(`^#[^:]+:.+$`)

// ValidLocalpart reports whether a user localpart is acceptable for this
// homeserver. Matrix reserves a small character set for user ID local parts.
func ValidLocalpart(localpart string) bool {
	if localpart == "" {
		return false
	}
	for _, r := range localpart {
		if !userLocalpartRune(r) {
			return false
		}
	}
	return true
}

// aliasLocalpartReadable rejects only what would make an alias unreadable rather
// than what this server would not have created: the characters the sigil and the
// separator are made of, and anything a transport cannot carry.
func aliasLocalpartReadable(localpart string) bool {
	if localpart == "" {
		return false
	}
	for _, r := range localpart {
		if r == ' ' || r == '#' || r == ':' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidAliasLocalpart reports whether an alias local part is acceptable. Room
// aliases are display text as much as identifiers: the specification lets them
// hold any character from U+0080 upwards, which is how a room called
// "#salle de réunion:example.com" or one named in Japanese is addressable.
// User IDs stay restricted to the ASCII set above.
func ValidAliasLocalpart(localpart string) bool {
	if localpart == "" {
		return false
	}
	for _, r := range localpart {
		switch {
		case userLocalpartRune(r):
		case r >= '\u0080' && !unicode.IsControl(r):
		default:
			return false
		}
	}
	return true
}

// userLocalpartRune reports whether r belongs to the small ASCII set Matrix
// allows in an identifier local part.
func userLocalpartRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
	case r >= '0' && r <= '9':
	case r == '.' || r == '_' || r == '-' || r == '=' || r == '/':
	default:
		return false
	}
	return true
}

// IsUserID reports whether id is written as "@localpart:domain" and holds a
// local part this server could accept.
func IsUserID(id string) bool {
	localpart, _, ok := cutIdentifier(id, "@")
	return ok && ValidLocalpart(localpart)
}

// ValidUserID reports whether a string is spelled as a user ID all the way through: the
// sigil, a local part this server could accept, and a server name that could address a
// server. [IsUserID] asks only the first two, because the identifier in already-stored
// state was accepted once and is not re-examined for how its server name is spelled; a
// value arriving from a client is checked all the way, before it is written down as
// somebody's identity.
func ValidUserID(id string) bool {
	localpart, domain, ok := cutIdentifier(id, "@")
	return ok && ValidLocalpart(localpart) && ValidServerName(domain)
}

// IsRoomID reports whether id is written as a room ID: either "!localpart:domain", or
// the domain-less form room version 12 mints - "!" followed by a URL-safe unpadded
// base64 hash, which names no server at all because the room is named by its create
// event rather than by the server that made it.
//
// Room ID local parts are opaque: a server mints them and nothing else reads them, so
// only the shape is checked.
func IsRoomID(id string) bool {
	if !strings.HasPrefix(id, "!") {
		return false
	}
	rest := id[1:]
	if local, _, found := strings.Cut(rest, ":"); found {
		return local != ""
	}
	return isHashedRoomIDLocal(rest)
}

// hashedRoomIDLength is the length of a URL-safe unpadded base64 SHA-256: 43
// characters, because 32 bytes need 43 characters once the padding is dropped.
const hashedRoomIDLength = 43

// isHashedRoomIDLocal reports whether a room ID local part is the hash form room
// version 12 uses: URL-safe unpadded base64, and nothing else. The length is part of
// the shape on purpose - a shorter string in that alphabet would be a room ID from an
// older version that lost its domain on the way here, and treating that as a valid
// identifier would let a truncated id address a room nobody asked for.
func isHashedRoomIDLocal(local string) bool {
	if len(local) != hashedRoomIDLength {
		return false
	}
	for i := 0; i < len(local); i++ {
		c := local[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// IsHashedRoomID reports whether a room ID is the domain-less hash form. It is the
// question to ask before treating a room ID as addressable by its domain.
func IsHashedRoomID(id string) bool {
	return strings.HasPrefix(id, "!") && !strings.Contains(id, ":") && isHashedRoomIDLocal(id[1:])
}

// RoomIDFromEventID returns the room ID a room version 12 room takes from the event
// that created it: the create event's id with ! in place of $, because both are the
// same hash over the same event and the sigil is the only thing that differs.
func RoomIDFromEventID(eventID string) (string, error) {
	if !strings.HasPrefix(eventID, "$") {
		return "", fmt.Errorf("the create event id %q is not a hashed event id", eventID)
	}
	roomID := "!" + eventID[1:]
	if !IsHashedRoomID(roomID) {
		return "", fmt.Errorf("the create event id %q does not hash to a room ID this server can name", eventID)
	}
	return roomID, nil
}

// cutIdentifier splits "<sigil>localpart:domain" into its parts.
func cutIdentifier(id, sigil string) (localpart, domain string, ok bool) {
	if !strings.HasPrefix(id, sigil) {
		return "", "", false
	}
	localpart, domain, found := strings.Cut(strings.TrimPrefix(id, sigil), ":")
	if !found || domain == "" {
		return "", "", false
	}
	return localpart, domain, true
}

// ValidServerName reports whether name is written the way a server name is written: a
// host, optionally followed by a colon and a port. It is not a DNS check - a name can be
// perfectly spelled and unreachable - it is the check that separates a name that could
// address a server from one that cannot, which is what matters before a string from a
// stranger is used as an address or embedded in an identifier.
//
// The port is the part that catches the common malformed identifier: `localhost:http` is
// what a URI that should have been split looks like, and a server name that carries one
// was never a server name.
func ValidServerName(name string) bool {
	host, port, hasPort := name, "", false
	if name != "" && name[0] == '[' {
		// A bracketed IPv6 literal keeps its own colons; the port is what follows the
		// closing bracket, and nothing else may.
		end := strings.Index(name, "]")
		if end < 0 {
			return false
		}
		host = name[:end+1]
		rest := name[end+1:]
		switch {
		case rest == "":
		case strings.HasPrefix(rest, ":"):
			hasPort, port = true, rest[1:]
		default:
			return false
		}
	} else if local, after, found := strings.Cut(name, ":"); found {
		host, hasPort, port = local, true, after
	}
	if hasPort {
		if port == "" || len(port) > 5 {
			return false
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	if host == "" {
		return false
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == ':' || r == '[' || r == ']':
		default:
			return false
		}
	}
	// A name made only of punctuation is not a host. This is the same "does it carry
	// something addressable" question the port check answers, one level in.
	if strings.Trim(host, ".-[]:") == "" {
		return false
	}
	return true
}

// ValidRoomID reports whether the string is shaped like a room ID: an exclamation
// mark, a local part, a colon and a server name. A room ID is the one identifier in
// Matrix that a server mints for itself and never reuses, so its shape is checked
// before it is used as a key - an allow rule that names "invalid" is not a room that
// anybody can be a member of, and treating it as one would let a typo decide who may
// join.
func ValidRoomID(id string) bool {
	if !strings.HasPrefix(id, "!") {
		return false
	}
	local, server, found := strings.Cut(id[1:], ":")
	if local == "" {
		return false
	}
	if !found {
		// Room version 12: the id is a hash of the room's create event and names no
		// server, so there is no server name to check.
		return isHashedRoomIDLocal(local)
	}
	return ValidServerName(server)
}

// UserMxid builds a user ID from a localpart and server name.
func UserMxid(localpart, serverName string) string {
	if !strings.HasPrefix(localpart, "@") {
		localpart = "@" + localpart
	}
	if !strings.Contains(localpart, ":") {
		localpart += ":" + serverName
	}
	return localpart
}

// LocalpartOf returns the localpart of a user ID.
func LocalpartOf(userID string) string {
	s := strings.TrimPrefix(userID, "@")
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i]
	}
	return s
}

// DomainOf returns the server name part of a Matrix identifier, or the empty string
// when the identifier names none.
//
// A room ID from room version 12 names none on purpose: such a room is named by a hash
// of its create event, and the server that made it is not written into the identifier.
// A caller that asked for the domain of a room ID to decide where the room lives, which
// server to send to, or whether the room is local has to ask something else instead -
// the room's own state, or this server's database. A caller that asked about a user ID,
// an alias or a server name is unaffected: those keep their domain in every room
// version, aliases included.
func DomainOf(id string) string {
	if i := strings.Index(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return ""
}
