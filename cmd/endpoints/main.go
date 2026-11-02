// Command endpoints reports which endpoints from the specification index in
// the endpoint index in internal/coverage/SPECS.md the server implements. It builds the API layer, enumerates the
// registered route patterns and hands both to internal/coverage, which holds the
// matching rules and the report.
//
// Usage:
//
//	go run ./cmd/endpoints [-out ENDPOINTS.md] [-spec internal/coverage/SPECS.md] [-all]
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/coverage"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

func main() {
	var (
		specPath = flag.String("spec", "internal/coverage/SPECS.md", "path to the endpoint index")
		outPath  = flag.String("out", "", "write a markdown report to this path")
		showAll  = flag.Bool("all", false, "list every endpoint instead of only the summary")
	)
	flag.Parse()

	implemented, err := registeredRoutes()
	if err != nil {
		log.Fatalf("building the API: %v", err)
	}
	spec, err := coverage.ParseSpec(*specPath)
	if err != nil {
		log.Fatalf("reading %s: %v", *specPath, err)
	}

	done, missing, extra := coverage.Compare(spec, implemented)
	fmt.Println(coverage.Summary(done, missing, extra))

	if *outPath != "" {
		if err := os.WriteFile(*outPath, []byte(coverage.Report(done, missing, extra)), 0o644); err != nil {
			log.Fatalf("writing %s: %v", *outPath, err)
		}
	}
	if *showAll {
		for _, row := range done {
			fmt.Printf("OK   %s %s\n", row.Method, row.Path)
		}
		for _, row := range missing {
			fmt.Printf("TODO %s %s (%s)\n", row.Method, row.Path, row.Deprecation)
		}
		for _, route := range extra {
			fmt.Printf("EXTRA %s\n", route)
		}
	}
}

// registeredRoutes builds the API application and returns its route patterns.
// The database is an empty in-memory one: the routes are registered while the
// application is built, so no data is ever read.
func registeredRoutes() ([]coverage.Endpoint, error) {
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
		// The report counts what the binary is able to serve. The server-server
		// routes exist behind -federation, and leaving them out would report the
		// key endpoints as unimplemented and hide the extra routes they add.
		Federation: true,
	})
	return coverage.Routes(app.Registered()), nil
}
