// Command endpoints reports which endpoints from the specification index in
// SPECS.md the server implements. It builds the API layer, enumerates the
// registered route patterns and diffs them against the index table.
//
// Usage:
//
//	go run ./cmd/endpoints [-out ENDPOINTS.md] [-spec SPECS.md] [-all]
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// specRow matches one row of the endpoint index table in SPECS.md.
var specRow = regexp.MustCompile("(?m)^\\s+`(/[^`]*)`\\s+(GET|POST|PUT|DELETE)\\s+(.*?)\\s*$")

func main() {
	var (
		specPath = flag.String("spec", "SPECS.md", "path to the endpoint index")
		outPath  = flag.String("out", "", "write a markdown report to this path")
		showAll  = flag.Bool("all", false, "list every endpoint instead of only the summary")
	)
	flag.Parse()

	implemented, err := registeredRoutes()
	if err != nil {
		log.Fatalf("building the API: %v", err)
	}
	spec, err := parseSpec(*specPath)
	if err != nil {
		log.Fatalf("reading %s: %v", *specPath, err)
	}

	done, missing, extra := compare(spec, implemented)
	summary := fmt.Sprintf("%d/%d endpoints from the specification index are implemented (%d extra routes)",
		len(done), len(done)+len(missing), len(extra))

	if *outPath != "" {
		report := report(done, missing, extra, spec)
		if err := os.WriteFile(*outPath, []byte(report), 0o644); err != nil {
			log.Fatalf("writing %s: %v", *outPath, err)
		}
	}
	fmt.Println(summary)
	if *showAll {
		for _, row := range done {
			fmt.Printf("OK   %s %s\n", row.Method, row.Path)
		}
		for _, row := range missing {
			fmt.Printf("TODO %s %s (%s)\n", row.Method, row.Path, row.Deprecation)
		}
		for _, row := range extra {
			fmt.Printf("EXTRA %s\n", row)
		}
	}
}

// endpoint is one method and path pair.
type endpoint struct {
	Method      string
	Path        string
	Deprecation string
	Implemented bool
}

// registeredRoutes builds the API application and returns its route patterns.
func registeredRoutes() ([]endpoint, error) {
	db, err := persist.NewDatabase(":memory:")
	if err != nil {
		return nil, err
	}
	tokens := auth.NewTokenService("report-only-secret")
	svc, err := service.New(db, "example.com", tokens)
	if err != nil {
		return nil, err
	}
	app := api.New(svc, auth.NewStore(tokens, db), api.Config{
		ServerName:  "example.com",
		EnableMedia: true,
		AllowGuests: true,
	})
	var routes []endpoint
	for _, pattern := range app.Registered() {
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			method, path = "", pattern
		}
		routes = append(routes, endpoint{Method: method, Path: path})
	}
	return routes, nil
}

// parseSpec reads the endpoint index table out of SPECS.md.
func parseSpec(path string) ([]endpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []endpoint
	for _, match := range specRow.FindAllStringSubmatch(string(data), -1) {
		rows = append(rows, endpoint{
			Path:        normalizePath(match[1]),
			Method:      match[2],
			Deprecation: strings.Join(strings.Fields(match[3]), " "),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Path == rows[j].Path {
			return rows[i].Method < rows[j].Method
		}
		return rows[i].Path < rows[j].Path
	})
	return rows, nil
}

// normalizePath strips the trailing slash of endpoints whose last path segment
// may be empty, so the comparison is segment based.
func normalizePath(path string) string {
	return strings.TrimSuffix(path, "/")
}

// segments splits a path template into segments, treating every {placeholder}
// as a wildcard that matches any segment.
func segments(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "{") {
			parts[i] = "*"
		}
	}
	return parts
}

// matches reports whether a route pattern serves a specification endpoint.
func matches(route, want endpoint) bool {
	if route.Method != "" && route.Method != want.Method {
		return false
	}
	if !versionEquivalent(route.Path, want.Path) {
		return false
	}
	routeParts, wantParts := segments(route.Path), segments(want.Path)
	// A pattern registered with a trailing slash serves a whole subtree, so it
	// also answers endpoints with more path segments.
	subtree := strings.HasSuffix(route.Path, "/")
	if !subtree && len(routeParts) != len(wantParts) {
		return false
	}
	if len(wantParts) < len(routeParts) {
		return false
	}
	for i := range routeParts {
		if routeParts[i] == wantParts[i] || routeParts[i] == "*" || wantParts[i] == "*" {
			continue
		}
		return false
	}
	return true
}

// versionEquivalent allows v1, v2 and v3 prefixes to satisfy each other, which
// mirrors how homeservers expose the same handler under several API revisions.
func versionEquivalent(a, b string) bool {
	return versionPrefix(a) == versionPrefix(b)
}

// versionPrefix returns the revision segment of a client API path.
func versionPrefix(path string) string {
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, "v") && len(part) > 1 && part[1] >= '0' && part[1] <= '9' {
			return part
		}
	}
	return ""
}

// compare splits the specification index into implemented and missing rows and
// lists the routes that the index does not mention.
func compare(spec, implemented []endpoint) (done, missing []endpoint, extra []string) {
	for _, want := range spec {
		found := false
		for _, route := range implemented {
			if matches(route, want) {
				found = true
				break
			}
		}
		if found {
			done = append(done, want)
		} else {
			missing = append(missing, want)
		}
	}
	for _, route := range implemented {
		covered := false
		for _, want := range spec {
			if matches(route, want) {
				covered = true
				break
			}
		}
		if !covered {
			extra = append(extra, strings.TrimSpace(route.Method+" "+route.Path))
		}
	}
	return done, missing, extra
}

// report renders the markdown coverage report.
func report(done, missing []endpoint, extra []string, spec []endpoint) string {
	var b strings.Builder
	b.WriteString("# Endpoint coverage\n\n")
	b.WriteString("Generated by `go run ./cmd/endpoints -out ENDPOINTS.md` from the endpoint\n")
	b.WriteString("index in [SPECS.md](SPECS.md).\n\n")
	b.WriteString(fmt.Sprintf("Implemented: **%d** of **%d** indexed endpoints. Extra routes: **%d**.\n\n",
		len(done), len(done)+len(missing), len(extra)))
	b.WriteString("| Endpoint | Method | Status | Deprecation |\n| --- | --- | --- | --- |\n")
	all := append([]endpoint{}, done...)
	for i := range all {
		all[i].Implemented = true
	}
	for i := range missing {
		all = append(all, missing[i])
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Path == all[j].Path {
			return all[i].Method < all[j].Method
		}
		return all[i].Path < all[j].Path
	})
	for _, row := range all {
		status := "missing"
		if row.Implemented {
			status = "implemented"
		}
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s |\n", row.Path, row.Method, status, row.Deprecation))
	}
	if len(extra) > 0 {
		b.WriteString("\n## Routes outside the index\n\n")
		b.WriteString("These handlers exist but the specification index has no matching entry,\n")
		b.WriteString("usually because they are administrative or a legacy alias.\n\n")
		for _, route := range extra {
			b.WriteString(fmt.Sprintf("- `%s`\n", route))
		}
	}
	return b.String()
}
