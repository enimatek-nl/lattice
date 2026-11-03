// Package config is how an operator's settings reach the server. Flags are the
// primary interface, because they are what a person at a shell types and what a
// failing start can be read from; the environment is the second one, because a
// container is configured with variables instead of a command line. Both name the
// same settings, and this package is the one place that mapping is decided.
package config

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

// EnvPrefix is prepended to a flag name - upper cased, with dashes turned into
// underscores - to name the environment variable that may set it: -server-name is
// LATTICE_SERVER_NAME, -federation-advertise is LATTICE_FEDERATION_ADVERTISE. The
// rule is mechanical on purpose, so a flag that exists can be configured without
// anyone having to remember a second name for it.
const EnvPrefix = "LATTICE_"

// notSettings names the flags that are never read from the environment, because they are
// not settings of a deployment:
//
//   - version prints a string and exits. An orchestrator that carries a LATTICE_VERSION
//     variable for its own bookkeeping would otherwise turn every container start into a
//     printout of a version and a clean exit.
//   - password, admin, display-name, yes, limit and all belong to one invocation of one
//     operator command. A LATTICE_PASSWORD left in a container's environment would
//     otherwise be the password of whichever account happens to be added next, and a
//     LATTICE_ADMIN would quietly make it an administrator. -limit and -all decide how much
//     of a list one answer holds, which is a question about the question.
var notSettings = map[string]bool{
	"version":      true,
	"password":     true,
	"admin":        true,
	"display-name": true,
	"yes":          true,
	"limit":        true,
	"all":          true,
}

// EnvName returns the environment variable that can set the named flag.
func EnvName(flagName string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// ApplyEnvDefaults gives every flag in fs the value the environment holds for it,
// unless the command line already set that flag. It runs after parsing and before
// any value is read, which is what keeps the precedence an operator expects:
// command line first, environment second, the flag's own default last.
//
// A variable set to an empty or whitespace-only value counts as unset, because a
// compose file writes LATTICE_SERVER_NAME= for a key the author left blank, and an
// empty server name or database path is a broken start rather than a choice.
//
// lookup is how the environment is read: os.LookupEnv in the server, a table in a
// test. The names of the flags taken from the environment are returned so the
// caller can say where a setting came from; an error is returned for a variable
// whose value the flag cannot hold.
func ApplyEnvDefaults(fs *flag.FlagSet, lookup func(string) (string, bool)) ([]string, error) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	var applied []string
	var errs []error
	fs.VisitAll(func(f *flag.Flag) {
		if notSettings[f.Name] || given[f.Name] {
			return
		}
		value, found := lookup(EnvName(f.Name))
		if !found || strings.TrimSpace(value) == "" {
			return
		}
		if err := fs.Set(f.Name, value); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvName(f.Name), err))
			return
		}
		applied = append(applied, f.Name)
	})
	return applied, errors.Join(errs...)
}
