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
	return aliasRe.MatchString(string(a))
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

var aliasRe = regexp.MustCompile(`^#[a-zA-Z0-9._/=-]+:.+$`)

// ValidLocalpart reports whether a user or alias localpart is acceptable for
// this homeserver. Matrix reserves a small character set for localparts.
func ValidLocalpart(localpart string) bool {
	if localpart == "" {
		return false
	}
	for _, r := range localpart {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '=' || r == '/':
		default:
			return false
		}
	}
	return true
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
