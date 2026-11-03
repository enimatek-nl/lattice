// Package cli is the operator's half of the binary: the commands that work on a
// homeserver's database without going through its HTTP API.
//
// They exist because the HTTP API is not always reachable or open. A server started
// with -disable-registration has closed the door a client knocks on, and still needs
// the first account, and the tenth; an account whose owner has gone has to be removable
// by the person who runs the server rather than by a User-Interactive Auth flow that
// the account can no longer answer. Those are not administrative endpoints bolted onto
// a server that may itself be broken: they are one binary, run with the flags or the
// environment variables it already understands.
//
// Each command opens the database the way the server does - the pure-Go SQLite driver,
// WAL, and the busy timeout that lets a second writer in while the homeserver is
// running - so the commands work against a live server, from a shell in the container,
// without stopping anything.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/config"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// The operator commands, in the order they are listed in the usage message.
const (
	AddUser    = "add-user"
	RemoveUser = "remove-user"
	Users      = "users"
	Help       = "help"
)

// The three helpers below are the package's only way of writing to an operator's
// terminal. They drop the error fmt returns, deliberately: an output stream that fails -
// a pipe closed by `| head`, a terminal that went away - changes nothing about what was
// written to the database, and the exit status and the error sentence are what report the
// outcome. Checking it at every call site would only add messages that tell an operator
// nothing they can act on.
func say(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

func sayLine(out io.Writer, args ...any) {
	_, _ = fmt.Fprintln(out, args...)
}

func sayText(out io.Writer, args ...any) {
	_, _ = fmt.Fprint(out, args...)
}

// Invocation is everything a command is given: the words after its name, the streams it
// reads and writes, and how to read the environment. Passing the environment in rather
// than reading os.Getenv is what lets a test hand it a table.
type Invocation struct {
	args  []string
	stdin io.Reader
	out   io.Writer
	env   func(string) (string, bool)
}

// Out returns the stream a command writes its result to.
func (i Invocation) Out() io.Writer { return i.out }

// settings are the flags every command shares with the server: the same names, the same
// LATTICE_* variables, the same precedence. Inside a container both of them live in the
// environment, and a command that ignored that would open the wrong file or write an
// account under a name this server does not serve.
type settings struct {
	dbPath     string
	serverName string
}

// command is one operator utility.
type command struct {
	name    string
	summary string
	// usage explains the command, its flags and an example, for the help output and
	// for the message the flag set prints when it is asked.
	usage func(out io.Writer)
	run   func(inv Invocation) error
}

// commands lists the utilities.
func commands() []command {
	return []command{
		{name: AddUser, summary: "create an account and set its password", usage: usageAddUser, run: runAddUser},
		{name: RemoveUser, summary: "deactivate an account, ending its sessions and keys", usage: usageRemoveUser, run: runRemoveUser},
		{name: Users, summary: "list the accounts in a database", usage: usageUsers, run: runUsers},
		{name: Help, summary: "list these commands", usage: Usage, run: runHelp},
	}
}

// IsCommand reports whether the first word after the binary names an operator command
// rather than a server flag. Only a bare word can be one, which is what keeps every
// existing invocation - `lattice -addr :8008`, `lattice -server-name example.org`,
// `lattice -version` - running the homeserver exactly as before.
func IsCommand(name string) bool {
	if name == "" || name[0] == '-' {
		return false
	}
	for _, c := range commands() {
		if c.name == name {
			return true
		}
	}
	return false
}

// Run executes one operator command. The error it returns is already phrased for a
// person at a keyboard: a homeserver error such as M_USER_IN_USE arrives as its message
// rather than as an error code and an HTTP status.
func Run(name string, args []string, stdin io.Reader, out io.Writer) error {
	return RunWithEnv(name, args, stdin, out, os.LookupEnv)
}

// RunWithEnv is Run with the environment supplied, so the same precedence the server
// follows - command line, environment, default - can be exercised without a process.
func RunWithEnv(name string, args []string, stdin io.Reader, out io.Writer, env func(string) (string, bool)) error {
	var found *command
	for _, c := range commands() {
		if c.name == name {
			found = &c
			break
		}
	}
	if found == nil {
		return fmt.Errorf("%q is not a command; run `lattice %s` for the list", name, Help)
	}
	inv := Invocation{args: args, stdin: stdin, out: out, env: env}
	err := found.run(inv)
	if errors.Is(err, flag.ErrHelp) {
		// -h asked for the usage, and the flag set has already printed it.
		return nil
	}
	return friendly(err)
}

// Usage prints the command list, for `lattice help` and for the server's own -h output.
func Usage(out io.Writer) {
	sayLine(out, "Operator commands, run in place of the homeserver:")
	width := 0
	for _, c := range commands() {
		if len(c.name) > width {
			width = len(c.name)
		}
	}
	for _, c := range commands() {
		say(out, "%s  %s\n", pad(c.name, width), c.summary)
	}
	sayLine(out, "\nEvery command takes the same -db and -server-name as the server,")
	sayLine(out, "and reads them from LATTICE_DB and LATTICE_SERVER_NAME the same way.")
}

// runHelp prints the command list, with each command's own usage underneath.
func runHelp(inv Invocation) error {
	Usage(inv.out)
	return nil
}

// newFlagSet starts the flag set of one command with the settings it shares with the
// server, and points its usage at the command's own explanation so that `-h` prints
// something a person can act on.
func newFlagSet(inv Invocation, name string, s *settings) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(inv.out)
	fs.StringVar(&s.dbPath, "db", "lattice.db", "SQLite database file")
	fs.StringVar(&s.serverName, "server-name", "localhost", "homeserver name; the account ID ends with it")
	fs.Usage = func() { commandUsage(inv.out, name) }
	return fs
}

// parse parses a command's flags and then applies the environment to the ones the
// command line left unset, which is the rule [config] states for the server: a flag
// typed here beats a variable, and a variable beats the default.
func parse(fs *flag.FlagSet, inv Invocation) error {
	if err := fs.Parse(inOrder(fs, inv.args)); err != nil {
		return err
	}
	if _, err := config.ApplyEnvDefaults(fs, inv.env); err != nil {
		return err
	}
	return nil
}

// inOrder moves a command's flags in front of its positional arguments. Go's flag
// package stops reading flags at the first word that is not one, so `lattice add-user
// alice -db /data/lattice.db` would otherwise read -db, its path and everything after
// them as more account names - and an operator writes the name first, because that is
// the order every other command in the world uses.
//
// A flag set knows which of its flags are switches and which take a value, so the split
// is exact rather than a guess: a switch consumes no following word, and everything from
// a bare -- onwards is an argument, dashes or not.
func inOrder(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	terminated := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			terminated = true
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case len(arg) < 2 || arg[0] != '-':
			positional = append(positional, arg)
		default:
			flags = append(flags, arg)
			name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
			if hasValue {
				continue
			}
			definition := fs.Lookup(name)
			if definition == nil {
				// Not a flag of this command: leave it where it is and let the flag
				// package say so, rather than inventing a rule for a word that has no
				// meaning here.
				continue
			}
			if !isSwitch(definition) {
				if i+1 < len(args) {
					flags = append(flags, args[i+1])
					i++
				}
			}
		}
	}
	// The positional arguments go last, where the flag package stops looking for flags.
	// A -- the operator typed is put back in front of them, so that a name which does
	// look like a flag stays a name rather than becoming an unknown flag error.
	if terminated {
		return append(flags, append([]string{"--"}, positional...)...)
	}
	return append(flags, positional...)
}

// isSwitch reports whether a flag can be given without a value.
func isSwitch(f *flag.Flag) bool {
	switcher, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && switcher.IsBoolFlag()
}

// open opens the database and builds the service around it, with the same settings the
// server builds itself from. That includes the signing key the server mints on first
// start, because an operator command run on a fresh file must not leave it in a state
// the server then has to repair.
//
// The token service is built on a random secret, since these commands neither mint nor
// check an access token and a constant would be a thing to forge from should a path
// ever grow that minted one.
func open(s settings) (*persist.Database, *service.Service, error) {
	db, err := persist.NewDatabase(s.dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", s.dbPath, err)
	}
	svc, err := service.New(db, s.serverName, auth.NewTokenService(matrix.NewToken()))
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("starting service on %s: %w", s.dbPath, err)
	}
	return db, svc, nil
}

// friendly turns a homeserver error into a sentence: the M_* code and the HTTP status
// are what a client branches on, and noise to someone at a terminal.
func friendly(err error) error {
	var merr *matrix.Error
	if errors.As(err, &merr) {
		return errors.New(merr.Message)
	}
	return err
}

// pad left-aligns text in a column of the given width.
func pad(text string, width int) string {
	for len(text) < width {
		text += " "
	}
	return text
}
