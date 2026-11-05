// Package matrixid validates and manipulates the Matrix identifier grammar
// (spec.matrix.org/v1.19/appendices/#identifier-grammar).
package matrixid

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sigils used by the common identifier format.
const (
	SigilUser  = '@'
	SigilRoom  = '!'
	SigilEvent = '$'
	SigilAlias = '#'
)

// maxIDBytes is the specification-wide limit for identifiers, including the sigil.
const maxIDBytes = 255

// ErrInvalidID reports an identifier that does not satisfy the grammar.
var ErrInvalidID = errors.New("matrixid: invalid identifier")

// UserLocalpartValid reports whether localpart is a valid user ID localpart:
// non-empty, at most 255 bytes overall, and composed only of
// "a-z", "0-9", ".", "_", "=", "-", "/" and "+".
func UserLocalpartValid(localpart string) error {
	if localpart == "" {
		return fmt.Errorf("%w: empty user ID localpart", ErrInvalidID)
	}
	if len(localpart) > maxIDBytes-2 {
		return fmt.Errorf("%w: user ID localpart too long", ErrInvalidID)
	}
	for i := 0; i < len(localpart); i++ {
		c := localpart[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '=' || c == '-' || c == '/' || c == '+':
		default:
			return fmt.Errorf("%w: user ID localpart contains %q", ErrInvalidID, string(c))
		}
	}
	return nil
}

// UsernameToLocalpart maps a user-supplied username to a user ID localpart by
// downcasing it, as the specification requires ("homeservers downcase usernames
// when creating user IDs for new users").
func UsernameToLocalpart(username string) string {
	return strings.ToLower(username)
}

// ValidateRegistrationLocalpart applies local registration policy on top of the
// grammar: the localpart must be grammatical and must not start with "_".
// The "_" restriction mirrors the character set Synapse reserves for
// appservice-managed users; it is recorded as a local policy decision.
func ValidateRegistrationLocalpart(localpart string) error {
	if err := UserLocalpartValid(localpart); err != nil {
		return err
	}
	if strings.HasPrefix(localpart, "_") {
		return fmt.Errorf("%w: user ID localpart must not start with '_'", ErrInvalidID)
	}
	return nil
}

// ServerNameValid reports whether name is a valid Matrix server name:
// hostname with an optional numeric port, where the hostname is a DNS name, an
// IPv4 literal or a bracketed IPv6 literal.
func ServerNameValid(name string) error {
	host, port, err := SplitServerName(name)
	if err != nil {
		return err
	}
	_ = port
	if len(host) == 0 {
		return fmt.Errorf("%w: empty server name", ErrInvalidID)
	}
	if len(name) > 255 {
		return fmt.Errorf("%w: server name too long", ErrInvalidID)
	}
	return nil
}

// SplitServerName separates the host and optional port of a server name.
// port is 0 when the server name does not carry an explicit port.
func SplitServerName(name string) (host string, port int, err error) {
	if name == "" {
		return "", 0, fmt.Errorf("%w: empty server name", ErrInvalidID)
	}
	host = name
	if i := strings.LastIndexByte(name, ':'); i > 0 {
		// A ':' inside a bracketed IPv6 literal is not a port separator.
		if !strings.HasPrefix(name, "[") || strings.LastIndexByte(name, ']') < i {
			portStr := name[i+1:]
			p, cerr := strconv.Atoi(portStr)
			if cerr != nil || p < 0 || p > 65535 || portStr == "" || (len(portStr) > 5) {
				return "", 0, fmt.Errorf("%w: bad port in server name %q", ErrInvalidID, name)
			}
			host = name[:i]
			port = p
		}
	}
	if err := hostValid(host); err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func hostValid(host string) error {
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") {
			return fmt.Errorf("%w: unbalanced brackets in server name %q", ErrInvalidID, host)
		}
		inner := host[1 : len(host)-1]
		if !validIPv6(inner) {
			return fmt.Errorf("%w: invalid IPv6 literal %q", ErrInvalidID, host)
		}
		return nil
	}
	if looksLikeIPv4(host) {
		return validIPv4(host)
	}
	return validDNSName(host)
}

func looksLikeIPv4(host string) bool {
	dots := 0
	for i := 0; i < len(host); i++ {
		switch {
		case host[i] == '.':
			dots++
		case host[i] < '0' || host[i] > '9':
			return false
		}
	}
	return dots == 3
}

func validIPv4(host string) error {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return fmt.Errorf("%w: invalid IPv4 literal %q", ErrInvalidID, host)
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return fmt.Errorf("%w: invalid IPv4 literal %q", ErrInvalidID, host)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return fmt.Errorf("%w: invalid IPv4 literal %q", ErrInvalidID, host)
		}
	}
	return nil
}

func validIPv6(host string) bool {
	if host == "" {
		return false
	}
	colons := 0
	for i := 0; i < len(host); i++ {
		c := host[i]
		switch {
		case c == ':':
			colons++
		case c == '.':
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	if colons == 0 || colons > 8 {
		return false
	}
	groups := strings.Split(host, ":")
	for _, g := range groups {
		if g == "" {
			continue
		}
		if len(g) > 4 {
			return false
		}
	}
	return true
}

func validDNSName(host string) error {
	if len(host) > 255 {
		return fmt.Errorf("%w: DNS name too long", ErrInvalidID)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return fmt.Errorf("%w: empty label in server name %q", ErrInvalidID, host)
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z':
			case c >= 'A' && c <= 'Z':
			case c >= '0' && c <= '9':
			case c == '-':
			default:
				return fmt.Errorf("%w: invalid character %q in server name %q", ErrInvalidID, string(c), host)
			}
		}
	}
	return nil
}

// NewUserID builds a user ID from a username and server name, downcasing the
// username as required by the specification.
func NewUserID(username, serverName string) (string, error) {
	if err := ServerNameValid(serverName); err != nil {
		return "", err
	}
	localpart := UsernameToLocalpart(username)
	if err := ValidateRegistrationLocalpart(localpart); err != nil {
		return "", err
	}
	id := string(SigilUser) + localpart + ":" + serverName
	if len(id) > maxIDBytes {
		return "", fmt.Errorf("%w: user ID exceeds %d bytes", ErrInvalidID, maxIDBytes)
	}
	return id, nil
}

// UserValid reports whether id is a syntactically valid user ID.
func UserValid(id string) error {
	localpart, domain, err := SplitID(SigilUser, id)
	if err != nil {
		return err
	}
	if err := UserLocalpartValid(localpart); err != nil {
		return err
	}
	return ServerNameValid(domain)
}

// SplitID splits an identifier of the form "<sigil>localpart:domain" (or just
// "<sigil>opaque" for identifiers without a domain component).
func SplitID(sigil byte, id string) (localpart, domain string, err error) {
	if len(id) == 0 {
		return "", "", fmt.Errorf("%w: empty identifier", ErrInvalidID)
	}
	if id[0] != sigil {
		return "", "", fmt.Errorf("%w: identifier %q must start with %q", ErrInvalidID, id, string(sigil))
	}
	if len(id) > maxIDBytes {
		return "", "", fmt.Errorf("%w: identifier exceeds %d bytes", ErrInvalidID, maxIDBytes)
	}
	body := id[1:]
	if sigil == SigilEvent {
		if body == "" {
			return "", "", fmt.Errorf("%w: event ID has no opaque part", ErrInvalidID)
		}
		return body, "", nil
	}
	// Localparts never contain ':', so the domain starts at the first colon; the
	// domain itself may carry a port ("@alice:example.org:8448").
	i := strings.IndexByte(body, ':')
	if i < 0 {
		if sigil == SigilUser || sigil == SigilAlias {
			return "", "", fmt.Errorf("%w: identifier %q must contain a domain", ErrInvalidID, id)
		}
		return body, "", nil
	}
	return body[:i], body[i+1:], nil
}

// UserLocalpart returns the localpart of a user ID.
func UserLocalpart(userID string) (string, error) {
	localpart, _, err := SplitID(SigilUser, userID)
	return localpart, err
}

// UserDomain returns the domain part of a user ID.
func UserDomain(userID string) (string, error) {
	_, domain, err := SplitID(SigilUser, userID)
	return domain, err
}

// IsLocal reports whether domain belongs to this server. Server names are
// case-sensitive in Matrix, so the comparison is exact.
func IsLocal(domain, serverName string) bool {
	return domain == serverName
}

// RoomIDValid reports whether id is a valid room ID for a room version that
// carries a domain component. Version-specific rules live in the room version
// registry.
func RoomIDValid(id string) error {
	localpart, domain, err := SplitID(SigilRoom, id)
	if err != nil {
		return err
	}
	if err := opaqueUnicodeValid(localpart); err != nil {
		return fmt.Errorf("%w: room ID: %v", ErrInvalidID, err)
	}
	if domain != "" {
		return ServerNameValid(domain)
	}
	return nil
}

// EventIDValid reports whether id is a syntactically valid event ID.
func EventIDValid(id string) error {
	if len(id) == 0 || id[0] != SigilEvent {
		return fmt.Errorf("%w: event ID %q must start with '$'", ErrInvalidID, id)
	}
	if len(id) > maxIDBytes {
		return fmt.Errorf("%w: event ID exceeds %d bytes", ErrInvalidID, maxIDBytes)
	}
	if id[1:] == "" {
		return fmt.Errorf("%w: event ID has no opaque part", ErrInvalidID)
	}
	return nil
}

// RoomAliasValid reports whether alias is a valid room alias.
func RoomAliasValid(alias string) error {
	localpart, domain, err := SplitID(SigilAlias, alias)
	if err != nil {
		return err
	}
	if err := opaqueUnicodeValid(localpart); err != nil {
		return fmt.Errorf("%w: room alias: %v", ErrInvalidID, err)
	}
	if domain == "" {
		return fmt.Errorf("%w: room alias %q must contain a domain", ErrInvalidID, alias)
	}
	return ServerNameValid(domain)
}

// opaqueUnicodeValid accepts any valid non-surrogate Unicode code point except
// ':' and NUL, which is the rule the specification gives for opaque localparts.
func opaqueUnicodeValid(s string) error {
	if s == "" {
		return fmt.Errorf("empty localpart")
	}
	for i, r := range s {
		if r == utf8.RuneError {
			return fmt.Errorf("invalid UTF-8 at byte %d", i)
		}
		if r == ':' || r == 0 {
			return fmt.Errorf("character %q is not allowed", r)
		}
		if utf16Surrogate(r) {
			return fmt.Errorf("surrogate code point U+%04X is not allowed", r)
		}
		if unicode.Is(unicode.Cs, r) {
			return fmt.Errorf("surrogate code point U+%04X is not allowed", r)
		}
	}
	return nil
}

func utf16Surrogate(r rune) bool {
	return r >= 0xD800 && r <= 0xDFFF
}

// DeviceIDValid applies the local device ID policy: the character set Synapse
// uses ([A-Za-z0-9._=/-]) with a length of 1..100 bytes.
func DeviceIDValid(deviceID string) error {
	if deviceID == "" {
		return fmt.Errorf("%w: empty device ID", ErrInvalidID)
	}
	if len(deviceID) > 100 {
		return fmt.Errorf("%w: device ID too long", ErrInvalidID)
	}
	for i := 0; i < len(deviceID); i++ {
		c := deviceID[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '=' || c == '-' || c == '/' || c == '+':
		default:
			return fmt.Errorf("%w: device ID contains %q", ErrInvalidID, string(c))
		}
	}
	return nil
}

// GenerateRandomString returns n bytes of cryptographic randomness encoded as
// unpadded URL-safe base64. Used for tokens, nonces and device IDs.
func GenerateRandomString(random io.Reader, n int) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", fmt.Errorf("matrixid: cannot read randomness: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
