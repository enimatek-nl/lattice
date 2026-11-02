// Package coverage diffs the routes an API layer registers against the endpoint
// index in internal/coverage/SPECS.md, which is how the project keeps its coverage report honest.
//
// Matching is segment based and treats every {placeholder} as a wildcard,
// because a route pattern such as
// "PUT /_matrix/client/v3/rooms/{roomId}/send/{eventType}/{txnId}" describes a
// whole family of specification endpoints. The comparison rules are kept here,
// apart from the command, so they can be tested with a table instead of a
// running server: an index that silently stops matching would report a
// disappearing endpoint as "missing" and nobody would notice.
package coverage

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

// SpecRow matches one row of the endpoint index table in internal/coverage/SPECS.md: a path in
// backticks, a method, and whatever note follows.
var SpecRow = regexp.MustCompile("(?m)^\\s+`(/[^`]*)`\\s+(GET|POST|PUT|DELETE)\\s+(.*?)\\s*$")

// Endpoint is one method and path pair, from the index or from the server.
type Endpoint struct {
	Method      string
	Path        string
	Deprecation string
	Implemented bool
}

// String renders the endpoint the way the report lists unmatched routes.
func (e Endpoint) String() string { return strings.TrimSpace(e.Method + " " + e.Path) }

// Routes turns registered route patterns ("GET /_matrix/client/v3/...") into
// endpoints. A pattern without a method is a route that serves every method.
func Routes(patterns []string) []Endpoint {
	routes := make([]Endpoint, 0, len(patterns))
	for _, pattern := range patterns {
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			method, path = "", pattern
		}
		routes = append(routes, Endpoint{Method: method, Path: path})
	}
	return routes
}

// ParseSpec reads the endpoint index table out of internal/coverage/SPECS.md.
func ParseSpec(path string) ([]Endpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseSpecRows(string(data)), nil
}

// ParseSpecRows reads the index table out of the text of the endpoint index.
func ParseSpecRows(text string) []Endpoint {
	var rows []Endpoint
	for _, match := range SpecRow.FindAllStringSubmatch(text, -1) {
		rows = append(rows, Endpoint{
			Path:        NormalizePath(match[1]),
			Method:      match[2],
			Deprecation: strings.Join(strings.Fields(match[3]), " "),
		})
	}
	Sort(rows)
	return rows
}

// Sort orders endpoints by path, then method, which is the order the report
// prints them in.
func Sort(rows []Endpoint) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Path == rows[j].Path {
			return rows[i].Method < rows[j].Method
		}
		return rows[i].Path < rows[j].Path
	})
}

// NormalizePath strips the trailing slash of endpoints whose last path segment
// may be empty, so the comparison is segment based. A path that differs from a
// route pattern only by that empty final segment therefore does not match it.
func NormalizePath(path string) string {
	return strings.TrimSuffix(path, "/")
}

// Segments splits a path template into segments, treating every {placeholder}
// as a wildcard that matches any segment.
func Segments(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "{") {
			parts[i] = "*"
		}
	}
	return parts
}
