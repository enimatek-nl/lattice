// Package serveracl decides whether a room will deal with a homeserver.
//
// A Matrix room can exclude servers with an `m.room.server_acl` state event. The decision has to be
// made in one place, because it is made in several: before answering a join, before handing over a
// room's state, before accepting an event, and before sending one. A room's exclusion is worth
// nothing if each of those paths works out the rule for itself.
//
// The rule (SS §Server access control lists, CS §m.room.server_acl):
//
//   - No ACL event in the room's state: every server is allowed.
//   - The server's name is an IP literal and the ACL does not allow IP literals: denied.
//   - The host matches a pattern in `deny`: denied.
//   - Otherwise the host matches a pattern in `allow`: allowed.
//   - The host matches a pattern in `allow`: allowed.
//   - Anything else: denied. An ACL with an empty `allow` list therefore excludes every server,
//     which is what the specification says an absent `allow` means, and what makes an ACL event
//     with no content at all — including a redacted one — a room-wide exclusion.
//
// Patterns are glob-style against the server name with its port removed, so `evil.com`,
// `evil.com:8448` and `evil.com:1234` are the same host to an ACL: `*` matches any number of
// characters, `?` matches exactly one, and matching is case-insensitive. There is no escape
// mechanism, so a `*` or `?` in a pattern is always a wildcard.
package serveracl

import (
	"net/netip"
	"strings"
)

// Type is the state event type that carries a room's server ACL.
const Type = "m.room.server_acl"

// ACL is a room's exclusion of servers, read from its state event.
type ACL struct {
	// AllowIPs corresponds to `allow_ip_literals`, which is true when the field is absent or is
	// not a boolean.
	AllowIPs bool
	// Allow and Deny are the glob patterns, in the order the room gave them.
	Allow []string
	Deny  []string
}

// Parse reads an ACL from the content of an `m.room.server_acl` event.
//
// The second answer says whether the content was understood. The specification makes every field
// optional, so a missing field is not damage: `allow_ip_literals` defaults to true and both lists
// default to empty. What is not in the specification is a field of the wrong shape — `allow` given
// as a string, a list holding a number. This server does not apply such an ACL, and says so,
// because mapping a mangled allow list onto "no allow list" would cut a room off from every server
// on a typo: the exclusion the room meant to describe is not the one it wrote, and the safe reading
// of a sentence nobody can parse is that it was not said. A well-formed ACL with an empty allow
// list is applied as written, because excluding every server is a thing rooms legitimately do.
func Parse(content map[string]any) (ACL, bool) {
	acl := ACL{AllowIPs: true}
	if content == nil {
		return acl, true
	}
	if raw, present := content["allow_ip_literals"]; present {
		allow, ok := raw.(bool)
		if !ok {
			return ACL{}, false
		}
		acl.AllowIPs = allow
	}
	var understood bool
	acl.Allow, understood = patterns(content["allow"])
	if !understood {
		return ACL{}, false
	}
	acl.Deny, understood = patterns(content["deny"])
	if !understood {
		return ACL{}, false
	}
	return acl, true
}

// patterns reads a list of glob patterns and says whether it understood what it found. An absent
// field is an empty list; a field that is not a list of strings is not understood.
func patterns(value any) ([]string, bool) {
	if value == nil {
		return nil, true
	}
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		pattern, ok := entry.(string)
		if !ok {
			return nil, false
		}
		out = append(out, pattern)
	}
	return out, true
}

// Allows says whether this room will deal with the named server.
func (a ACL) Allows(serverName string) bool {
	host := Host(serverName)
	if !a.AllowIPs && IsIPLiteral(host) {
		return false
	}
	for _, pattern := range a.Deny {
		if Match(pattern, host) {
			return false
		}
	}
	for _, pattern := range a.Allow {
		if Match(pattern, host) {
			return true
		}
	}
	return false
}

// Host strips the port from a server name. An IPv6 literal keeps its brackets, which are part of
// how the name is written and what a pattern is matched against.
func Host(serverName string) string {
	if strings.HasPrefix(serverName, "[") {
		if end := strings.Index(serverName, "]"); end > 0 {
			return serverName[:end+1]
		}
	}
	if before, _, found := strings.Cut(serverName, ":"); found {
		return before
	}
	return serverName
}

// IsIPLiteral says whether a host is written as an address rather than a name.
func IsIPLiteral(host string) bool {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	_, err := netip.ParseAddr(host)
	return err == nil
}

// Match reports whether a host matches a glob pattern: `*` for any number of characters, `?` for
// exactly one, everything else literal, and case-insensitive throughout.
//
// `*` is not anchored at dots, so `*.matrix.org` matches `server.matrix.org` and also
// `a.b.matrix.org`; and it matches nothing at all, so `*.matrix.org` does not match `matrix.org`.
func Match(pattern, host string) bool {
	pattern, host = strings.ToLower(pattern), strings.ToLower(host)
	var star, resume = -1, 0
	position, taken := 0, 0
	for taken < len(host) {
		switch {
		case position < len(pattern) && (pattern[position] == '?' || pattern[position] == host[taken]):
			position++
			taken++
		case position < len(pattern) && pattern[position] == '*':
			star, resume = position, taken
			position++
		case star >= 0:
			position = star + 1
			resume++
			taken = resume
		default:
			return false
		}
	}
	for position < len(pattern) && pattern[position] == '*' {
		position++
	}
	return position == len(pattern)
}
