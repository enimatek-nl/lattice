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

// Valid reports whether the alias has the shape "#localpart:domain".
func (a Alias) Valid() bool {
	return aliasRe.MatchString(string(a)) && ValidAliasLocalpart(a.Localpart())
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

// IsRoomID reports whether id is written as "!localpart:domain". Room ID local
// parts are opaque: a server mints them and nothing else reads them, so only the
// shape is checked.
func IsRoomID(id string) bool {
	localpart, _, ok := cutIdentifier(id, "!")
	return ok && localpart != ""
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

// DomainOf returns the server name part of a Matrix identifier.
func DomainOf(id string) string {
	if i := strings.Index(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return ""
}
