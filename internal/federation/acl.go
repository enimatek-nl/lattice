package federation

import (
	"net"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A room decides which servers may take part in it. The rule is enforced on both sides of
// a connection because a room cannot uninvite a server that is already relaying its
// events: the only way an ACL takes effect is that every server stops carrying what the
// ACL forbids. An ACL this server ignores is an ACL its peers' users will see broken -
// the denied server keeps talking, and the room has no way to make it stop.

// ServerAllowed reports whether a server may carry events for a room whose
// m.room.server_acl content is content. A nil or empty content means no ACL is in the
// room state, which allows every server.
//
// The order of the checks is the specification's, and it is not the order a reader would
// guess: a deny entry is consulted before the allow list, so an allow list cannot rescue
// a name that a deny entry names. Getting that order wrong is how a moderation tool
// "blocks" a server that keeps posting.
func ServerAllowed(content matrix.Object, serverName string) bool {
	if len(content) == 0 {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(serverName))
	if name == "" {
		// A name this server cannot read is not a server it should be talking to.
		return false
	}
	// The port is not part of the identity an ACL names: an ACL entry matches a server,
	// and a server is a host, which may be reached on more than one port.
	host, _, _ := ServerNameParts(name)
	// A bracketed IPv6 literal is written with brackets only together with a port; the
	// ACL compares the address itself.
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return false
	}

	if !literalsAllowed(content) && net.ParseIP(host) != nil {
		return false
	}
	for _, pattern := range stringList(content["deny"]) {
		if matchesACL(pattern, host) {
			return false
		}
	}
	allow := stringList(content["allow"])
	if len(allow) == 0 {
		return true
	}
	for _, pattern := range allow {
		if matchesACL(pattern, host) {
			return true
		}
	}
	return false
}

// literalsAllowed reports whether an ACL lets IP addresses take part. The specification's
// default is true; an ACL that says otherwise is the one an operator writes after an
// abusive server turned up with a bare IP as its name.
func literalsAllowed(content matrix.Object) bool {
	if value, ok := content["allow_ip_literals"].(bool); ok {
		return value
	}
	return true
}

// stringList reads a list of strings, ignoring entries that are not strings: an ACL
// written by a buggy client cannot be allowed to widen or narrow what it matches.
func stringList(value any) []string {
	switch typed := value.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if text, ok := entry.(string); ok {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return typed
	}
	return nil
}

// matchesACL reports whether a server name matches one ACL pattern. The pattern language
// is deliberately small: "*" stands for any run of characters, "?" for exactly one, and
// every other character means itself. A pattern is not a regular expression, and treating
// it as one would let a moderator's "*.<evil>" become a pattern that matches half the
// internet.
func matchesACL(pattern, name string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "*" {
		return true
	}
	if !strings.ContainsAny(p, "*?") {
		return p == name
	}
	// A two pointer walk with backtracking over the last star: linear for the patterns
	// ACLs actually contain, and no allocation.
	star, mark := -1, 0
	i, j := 0, 0
	for j < len(name) {
		switch {
		case i < len(p) && (p[i] == '?' || p[i] == name[j]):
			i++
			j++
		case i < len(p) && p[i] == '*':
			star, mark = i, j
			i++
		case star >= 0:
			i = star + 1
			mark++
			j = mark
		default:
			return false
		}
	}
	for i < len(p) && p[i] == '*' {
		i++
	}
	return i == len(p)
}
