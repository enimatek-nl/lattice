package unit

import (
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/coverage"
)

// TestCoverageSegments pins how a path template is read. Every {placeholder} is a
// wildcard segment, and a trailing slash means "this subtree", which is how the
// media and well-known routes cover a family of paths.
func TestCoverageSegments(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{path: "/_matrix/client/v3/rooms/{roomId}/send/{eventType}/{txnId}",
			want: []string{"_matrix", "client", "v3", "rooms", "*", "send", "*", "*"}},
		{path: "/_matrix/media/v3/download/{serverName}/{mediaId}",
			want: []string{"_matrix", "media", "v3", "download", "*", "*"}},
		{path: "/.well-known/matrix/client", want: []string{".well-known", "matrix", "client"}},
		{path: "", want: []string{""}},
	}
	for _, tt := range tests {
		got := coverage.Segments(tt.path)
		if strings.Join(got, "/") != strings.Join(tt.want, "/") {
			t.Errorf("Segments(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
	if got := coverage.NormalizePath("/_matrix/client/v1/createRoom/"); got != "/_matrix/client/v1/createRoom" {
		t.Errorf("NormalizePath = %q, want the trailing slash stripped", got)
	}
}

// TestCoverageVersion pins which API revisions satisfy each other. The same
// handler is mounted under v3 and v1 on purpose, so the report must not count a
// v1-only entry as missing when a v3 route answers it - but a federation path is
// not a client path and never matches one.
func TestCoverageVersion(t *testing.T) {
	tests := []struct {
		path    string
		want    string
		comment string
	}{
		{path: "/_matrix/client/v3/rooms", want: "v3"},
		{path: "/_matrix/client/v1/rooms", want: "v1"},
		{path: "/_matrix/client/unstable/rooms", want: ""},
		{path: "/_matrix/federation/v1/send", want: "v1"},
		{path: "/_matrix/client/versions", want: ""},
		{path: "/_matrix/client/v3beta/rooms", want: "v3beta", comment: "a pre-release suffix is part of the revision"},
	}
	for _, tt := range tests {
		if got := coverage.VersionPrefix(tt.path); got != tt.want {
			t.Errorf("VersionPrefix(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
	equivalent := []struct {
		a, b    string
		want    bool
		comment string
	}{
		{a: "/_matrix/client/v3/rooms", b: "/_matrix/client/v1/rooms", want: false,
			comment: "a v3 route is not an implementation of a v1 entry: the client that asks for the older revision is refused"},
		{a: "/_matrix/client/v3/rooms", b: "/_matrix/client/v3/rooms", want: true},
		{a: "/_matrix/client/versions", b: "/_matrix/client/versions", want: true,
			comment: "an unversioned path matches an unversioned path"},
		{a: "/_matrix/client/v3/rooms", b: "/_matrix/media/v3/download/x", want: true,
			comment: "the revision is the only thing compared; the path decides the rest"},
		{a: "/_matrix/client/v3/rooms", b: "/_matrix/client/rooms", want: false,
			comment: "an unversioned path never satisfies a versioned one"},
	}
	for _, tt := range equivalent {
		if got := coverage.VersionEquivalent(tt.a, tt.b); got != tt.want {
			t.Errorf("VersionEquivalent(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestCoverageMatches pins the route-to-endpoint matcher. This is the rule the
// coverage number is built from, so a silent change here moves the reported
// progress of the whole project without any code changing.
func TestCoverageMatches(t *testing.T) {
	tests := []struct {
		route   coverage.Endpoint
		want    coverage.Endpoint
		match   bool
		comment string
	}{
		{
			route: coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/messages"},
			want:  coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/messages"},
			match: true,
		},
		{
			route:   coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/messages"},
			want:    coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v1/rooms/{roomId}/messages"},
			match:   false,
			comment: "the index lists the revision an endpoint is documented under; a route at another revision does not answer it",
		},
		{
			route: coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/messages"},
			want:  coverage.Endpoint{Method: "POST", Path: "/_matrix/client/v3/rooms/{roomId}/messages"},
			match: false, comment: "a GET route does not answer a POST endpoint",
		},
		{
			route: coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/messages"},
			want:  coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/threads"},
			match: false,
		},
		{
			route: coverage.Endpoint{Method: "GET", Path: "/_matrix/media/v3/download/"},
			want:  coverage.Endpoint{Method: "GET", Path: "/_matrix/media/v3/download/{serverName}/{mediaId}"},
			match: true, comment: "a subtree pattern covers deeper paths",
		},
		{
			route: coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/join/{roomIdOrAlias}"},
			want:  coverage.Endpoint{Method: "POST", Path: "/_matrix/client/v3/join/{roomId}"},
			match: false, comment: "the method is checked first: join is a POST endpoint",
		},
		{
			route: coverage.Endpoint{Method: "POST", Path: "/_matrix/client/v3/join/{roomIdOrAlias}"},
			want:  coverage.Endpoint{Method: "POST", Path: "/_matrix/client/v3/join/{roomId}/leave"},
			match: false, comment: "the index asks for a path this route does not serve",
		},
		{
			route:   coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/state/{eventType}/{stateKey}"},
			want:    coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/state/m.room.name"},
			match:   false,
			comment: "an index entry whose trailing slash was normalised away describes one segment fewer than the route",
		},
		{
			route: coverage.Endpoint{Path: "/_matrix/client/v3/rooms/{roomId}/send/{eventType}/{txnId}"},
			want:  coverage.Endpoint{Method: "PUT", Path: "/_matrix/client/v3/rooms/{roomId}/send/{eventType}/{txnId}"},
			match: true, comment: "a route registered without a method serves every method",
		},
		{
			route: coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/state"},
			want:  coverage.Endpoint{Method: "GET", Path: "/_matrix/client/v3/rooms/{roomId}/state/{eventType}/{stateKey}"},
			match: false, comment: "a shorter route does not cover a longer endpoint",
		},
	}
	for _, tt := range tests {
		if got := coverage.Matches(tt.route, tt.want); got != tt.match {
			t.Errorf("Matches(%s %s, %s %s) = %v, want %v (%s)",
				tt.route.Method, tt.route.Path, tt.want.Method, tt.want.Path, got, tt.match, tt.comment)
		}
	}
}

// TestCoverageCompare runs the whole diff over a small index so the three lists
// it returns - implemented, missing, extra - are pinned together.
func TestCoverageCompare(t *testing.T) {
	spec := coverage.ParseSpecRows(`
  ` + "`/rooms/{roomId}/messages`" + `  GET Not marked
  ` + "`/rooms/{roomId}/threads`" + `   GET Linked to v1 only
  ` + "`/rooms/{roomId}/hierarchy`" + ` GET     not    started
`)
	if len(spec) != 3 {
		t.Fatalf("ParseSpecRows found %d rows: %+v", len(spec), spec)
	}
	if spec[0].Path != "/rooms/{roomId}/hierarchy" || spec[1].Path != "/rooms/{roomId}/messages" {
		t.Errorf("the rows are not sorted by path: %s, %s", spec[0].Path, spec[1].Path)
	}
	if spec[0].Method != "GET" {
		t.Errorf("the method of a row = %q, want GET", spec[0].Method)
	}
	if spec[0].Deprecation != "not started" {
		t.Errorf("the note of a row = %q, want its padding collapsed to single spaces", spec[0].Deprecation)
	}

	routes := coverage.Routes([]string{
		"GET /rooms/{roomId}/messages",
		"POST /rooms/{roomId}/knock",
	})
	done, missing, extra := coverage.Compare(spec, routes)
	if len(done) != 1 || done[0].Path != "/rooms/{roomId}/messages" {
		t.Errorf("implemented = %+v, want only /rooms/{roomId}/messages", done)
	}
	if len(missing) != 2 {
		t.Errorf("missing = %+v, want the thread and hierarchy endpoints", missing)
	}
	if len(extra) != 1 || extra[0] != "POST /rooms/{roomId}/knock" {
		t.Errorf("extra = %v, want the knock route the index does not mention", extra)
	}
	summary := coverage.Summary(done, missing, extra)
	if summary != "1/3 endpoints from the specification index are implemented (1 extra routes)" {
		t.Errorf("summary = %q", summary)
	}
}

// TestCoverageReport checks the parts of ENDPOINTS.md the numbers come from. The
// file is generated, so a change here is a change in the project's public
// progress report.
func TestCoverageReport(t *testing.T) {
	done := []coverage.Endpoint{{Method: "GET", Path: "/rooms/{roomId}/messages"}}
	missing := []coverage.Endpoint{{Method: "GET", Path: "/rooms/{roomId}/threads", Deprecation: "v1 only"}}
	report := coverage.Report(done, missing, []string{"POST /admin/shutdown"})
	for _, want := range []string{
		"Implemented: **1** of **2** indexed endpoints. Extra routes: **1**.",
		"| `/rooms/{roomId}/messages` | GET | implemented |  |",
		"| `/rooms/{roomId}/threads` | GET | missing | v1 only |",
		"- `POST /admin/shutdown`",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report is missing %q:\n%s", want, report)
		}
	}
	// A done row is reported as implemented even though the caller built it from
	// the index, where the flag is not set.
	if strings.Count(report, "| implemented |") != 1 {
		t.Errorf("exactly one endpoint should be reported as implemented:\n%s", report)
	}
	if strings.Contains(report, "Routes outside the index") && len(missing) == 0 {
		t.Errorf("the extra routes section appeared with no extra routes")
	}
	empty := coverage.Report(nil, nil, nil)
	if !strings.Contains(empty, "Implemented: **0** of **0**") {
		t.Errorf("an empty report should still render: %s", empty)
	}
	if strings.Contains(empty, "Routes outside the index") {
		t.Errorf("an empty report should not list extra routes")
	}
}

// TestCoverageRoutes pins how registered patterns are read: the API registers
// "METHOD /path", and a bare path is a route that answers every method.
func TestCoverageRoutes(t *testing.T) {
	routes := coverage.Routes([]string{"GET /a", "/b", "POST /c/{id}"})
	if len(routes) != 3 {
		t.Fatalf("Routes returned %d endpoints", len(routes))
	}
	if routes[0].Method != "GET" || routes[0].Path != "/a" {
		t.Errorf("first route = %+v", routes[0])
	}
	if routes[1].Method != "" || routes[1].Path != "/b" {
		t.Errorf("a pattern without a method = %+v, want an empty method", routes[1])
	}
	if routes[2].String() != "POST /c/{id}" {
		t.Errorf("String() = %q", routes[2].String())
	}
}
