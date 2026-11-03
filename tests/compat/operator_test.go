package compat

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/cli"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// The operator commands exist for what a running server cannot do for itself: an account
// has to be added to a homeserver that has closed self-service registration, and an
// account has to be removable by the person who runs the server rather than by an
// authentication flow the account can no longer answer.
//
// They are tested against a live server on a file database, the command opening that same
// file through its own handle. That is what a deployment does - `docker exec` into a
// running container - and the interesting failures only appear across that boundary: the
// second writer, the account the server has never seen, the access token that stops
// working in a process that did not do the removing.

// operatorOn runs one command with the words an operator would type and the given stdin,
// and returns what it printed.
func operatorOn(t *testing.T, stdin, name string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := cli.RunWithEnv(name, args, strings.NewReader(stdin), &out, os.LookupEnv)
	return out.String(), err
}

// operator runs a command that succeeds, with a password on stdin - which is what an
// unattended run has, since a pipe is not a terminal.
func operator(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := operatorOn(t, "w1nter-snow\n", name, args...)
	if err != nil {
		t.Fatalf("`lattice %s %s` failed: %v", name, strings.Join(args, " "), err)
	}
	return out
}

// operatorFails runs a command expected to fail and returns the sentence it answered
// with, because that sentence is the whole interface for a mistake.
func operatorFails(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := operatorOn(t, "w1nter-snow\n", name, args...)
	if err == nil {
		t.Fatalf("`lattice %s %s` was expected to fail, said: %s", name, strings.Join(args, " "), out)
	}
	return err.Error()
}

// TestAnOperatorCommandGivesAClosedServerAnAccount is the point of the commands: a server
// nobody may register on, and an account that arrives anyway.
func TestAnOperatorCommandGivesAClosedServerAnAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	server := NewServerWithDatabase(t, path, api.Config{RegistrationDisabled: true})

	// Registration closed means closed: a client gets the refusal rather than a route that
	// happens to work anyway.
	refused := server.Do(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{
		"username": "sanne",
		"password": "w1nter-snow",
		"auth":     map[string]any{"type": "m.login.dummy"},
	})
	if refused.Status != http.StatusForbidden || refused.ErrCode() != "M_FORBIDDEN" {
		t.Fatalf("a closed server answered registration with %d %s, want 403 M_FORBIDDEN", refused.Status, refused.ErrCode())
	}

	// The command opens the same file from a second handle, with the account name first -
	// the order people actually write.
	out := operator(t, cli.AddUser, "sanne", "-db", path, "-server-name", "test.server")
	if !strings.Contains(out, "@sanne:test.server created") {
		t.Fatalf("add-user said %q, want the user ID it created", out)
	}

	// And it is an account the running server acts on immediately: it signs in, and the
	// room it creates is a room.
	session := server.Login("sanne", "w1nter-snow")
	if session.UserID != "@sanne:test.server" {
		t.Fatalf("the CLI-created account signed in as %s, want @sanne:test.server", session.UserID)
	}
	whoami := server.MustDo(http.MethodGet, "/_matrix/client/v3/account/whoami", session.Token, nil, http.StatusOK)
	if whoami.String("user_id") != "@sanne:test.server" {
		t.Errorf("whoami read %s, want @sanne:test.server", whoami.String("user_id"))
	}
	roomID := server.CreateRoom(session, map[string]any{"name": "lobby"})
	if id := server.Send(session, roomID, "op-1", "added from a terminal"); id == "" {
		t.Error("the CLI-created account could not send an event")
	}

	// It is also the first account, so it is the administrator, and an administrative
	// route answers for it. That is how a server with registration closed gets somebody
	// with authority before any flag names one.
	admin := server.Do(http.MethodGet, "/_synapse/admin/v2/users/@sanne:test.server", session.Token, nil)
	if admin.Status != http.StatusOK {
		t.Errorf("the first CLI-created account cannot reach an admin route: %d %s", admin.Status, admin.ErrCode())
	}

	t.Run("the list shows what is in the file", func(t *testing.T) {
		listing := operator(t, cli.Users, "-db", path, "-server-name", "test.server")
		if !strings.Contains(listing, "@sanne:test.server") || !strings.Contains(listing, "administrator") {
			t.Fatalf("`users` said %q, want the account and what it is", listing)
		}
	})

	t.Run("removing it ends what was live", func(t *testing.T) {
		// -yes because a test has no terminal to type the confirmation into.
		result := operator(t, cli.RemoveUser, "sanne", "-db", path, "-server-name", "test.server", "-yes")
		for _, want := range []string{"@sanne:test.server is deactivated", "1 session(s) ended", "1 room(s)"} {
			if !strings.Contains(result, want) {
				t.Errorf("`remove-user` said %q, which does not mention %q", result, want)
			}
		}

		// The access token the server itself minted dies in the process that is still
		// running: sessions are rows, and the middleware checks them per request.
		late := server.Do(http.MethodGet, "/_matrix/client/v3/account/whoami", session.Token, nil)
		if late.Status != http.StatusUnauthorized || late.ErrCode() != "M_UNKNOWN_TOKEN" {
			t.Errorf("the old token still works: %d %s, want 401 M_UNKNOWN_TOKEN", late.Status, late.ErrCode())
		}
		// Signing in again is refused as a deactivated account rather than as a wrong
		// password, so an operator is not chased after a typo.
		relogin := server.Do(http.MethodPost, "/_matrix/client/v3/login", "", map[string]any{
			"type":     "m.login.password",
			"user":     "sanne",
			"password": "w1nter-snow",
		})
		if relogin.Status != http.StatusForbidden || !strings.Contains(relogin.String("error"), "deactivated") {
			t.Errorf("login after removal answered %d %q, want a refusal naming deactivation", relogin.Status, relogin.String("error"))
		}
	})

	t.Run("the name stays taken", func(t *testing.T) {
		// Deleting the row would free the name, and the events the removed account sent
		// into that room would belong to whoever took it next.
		message := operatorFails(t, cli.AddUser, "sanne", "-db", path, "-server-name", "test.server")
		if !strings.Contains(message, "already taken") {
			t.Fatalf("re-adding a removed account said %q, want a refusal that the name is taken", message)
		}
		// The list says where the account went, because a list of only the accounts that
		// can sign in leaves an operator hunting for one that is merely gone.
		listing := operator(t, cli.Users, "-db", path, "-server-name", "test.server")
		if !strings.Contains(listing, "deactivated") {
			t.Fatalf("`users` said %q, which does not account for the removed one", listing)
		}
	})
}

// TestOperatorCommandsRefuseWhatTheyCannotHonor pins the sentences an operator reads when
// a command cannot be done. Each case is a mistake that would otherwise end with a
// half-made account in a file nobody expected.
func TestOperatorCommandsRefuseWhatTheyCannotHonor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	// A server is running on this file for the whole table, because that is when an
	// account created under the wrong name does its damage.
	NewServerWithDatabase(t, path, api.Config{RegistrationDisabled: true})

	if out := operator(t, cli.AddUser, "alice", "-db", path, "-server-name", "test.server"); !strings.Contains(out, "@alice:test.server created") {
		t.Fatalf("setting up the first account said %q", out)
	}

	cases := []struct {
		name string
		cmd  string
		// args are the words after the command name.
		args []string
		// stdin is what the command can read, empty for an unattended run with nothing
		// offered.
		stdin string
		// want is a phrase the answer has to contain.
		want string
	}{
		{
			name: "no name at all",
			cmd:  cli.AddUser,
			args: []string{"-db", path, "-server-name", "test.server"},
			want: "name the account to create",
		},
		{
			name: "two names in one run",
			cmd:  cli.AddUser,
			args: []string{"dave", "erin", "-db", path, "-server-name", "test.server"},
			want: "one account per run",
		},
		{
			name: "a name this server could never hold",
			cmd:  cli.AddUser,
			args: []string{"a name with spaces", "-db", path, "-server-name", "test.server"},
			want: "not a name this server can hold",
		},
		{
			name:  "no password offered by anyone",
			cmd:   cli.AddUser,
			args:  []string{"dave", "-db", path, "-server-name", "test.server"},
			stdin: "",
			want:  "no password",
		},
		{
			name: "a server name this file has never served",
			cmd:  cli.AddUser,
			args: []string{"carol", "-password", "x", "-db", path, "-server-name", "wrong.example"},
			want: "not the name this server serves",
		},
		{
			name: "an account that is not in the file",
			cmd:  cli.RemoveUser,
			args: []string{"nobody", "-db", path, "-server-name", "test.server", "-yes"},
			want: "no account named nobody",
		},
		{
			name: "a command that takes no names was given one",
			cmd:  cli.Users,
			args: []string{"alice", "-db", path, "-server-name", "test.server"},
			want: "takes no names",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdin := tc.stdin + "w1nter-snow\n"
			if tc.stdin == "" {
				stdin = ""
			}
			out, err := operatorOn(t, stdin, tc.cmd, tc.args...)
			if err == nil {
				t.Fatalf("`lattice %s %s` was expected to fail, said: %s", tc.cmd, strings.Join(tc.args, " "), out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("answer was %q, want it to say %q", err, tc.want)
			}
		})
	}

	// None of that left anything behind: the file holds the one account it held before.
	db, err := persist.NewDatabase(path)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer func() { _ = db.Close() }()
	total, err := db.UserRepo.Count()
	if err != nil {
		t.Fatalf("counting accounts: %v", err)
	}
	if total != 1 {
		t.Errorf("the failed commands left %d accounts, want the 1 made first", total)
	}
}

// TestAnOperatorCommandKeepsThePasswordOutOfEverythingItPrints is about the one value
// these commands have to get right twice: hashed rather than stored, and printed nowhere.
func TestAnOperatorCommandKeepsThePasswordOutOfEverythingItPrints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	const secret = "hand-typed-and-not-to-be-seen"

	out, err := operatorOn(t, secret+"\n", cli.AddUser, "hash", "-db", path, "-server-name", "test.server", "-admin")
	if err != nil {
		t.Fatalf("add-user failed: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the password appears in what add-user printed: %q", out)
	}

	db, err := persist.NewDatabase(path)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	account, err := db.UserRepo.GetByLocalpart("hash")
	if err != nil {
		t.Fatalf("the account is not in the database: %v", err)
	}
	if account.PasswordHash == secret {
		t.Error("the password is stored in plain text")
	}
	if !strings.HasPrefix(account.PasswordHash, "$2") {
		t.Errorf("the stored hash is %q, which is not a bcrypt hash", account.PasswordHash)
	}
	if !account.Admin {
		t.Error("-admin did not make this account an administrator")
	}
	if display, err := db.ProfileRepo.Get(account.ID, service.ProfileDisplayName); err == nil && display != "" {
		t.Errorf("an account without -display-name has a display name: %q", display)
	}
	// The account arrives with the push rules every account has, so a client is not left
	// with defaults it expects to already be there.
	rules, err := db.PushRuleRepo.List(account.ID, "override")
	if err != nil {
		t.Fatalf("reading the push rules of the new account: %v", err)
	}
	if len(rules) == 0 {
		t.Error("the new account has no default push rules")
	}

	// The second account is not an administrator unless the flag says so: the first-account
	// rule has to be a rule about the first, not about every account made this way.
	if _, err := operatorOn(t, "second-password\n", cli.AddUser, "plain", "-db", path, "-server-name", "test.server"); err != nil {
		t.Fatalf("add-user for a second account failed: %v", err)
	}
	second, err := db.UserRepo.GetByLocalpart("plain")
	if err != nil {
		t.Fatalf("the second account is not in the database: %v", err)
	}
	if second.Admin {
		t.Error("a second account became an administrator on its own")
	}
}

// TestAnOperatorCommandTakesItsSettingsFromTheEnvironment mirrors how the container uses
// them: the image holds LATTICE_DB and LATTICE_SERVER_NAME, and a command run with
// `docker exec` has to find that file and that name without being told either.
func TestAnOperatorCommandTakesItsSettingsFromTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	env := func(name string) (string, bool) {
		switch name {
		case "LATTICE_DB":
			return path, true
		case "LATTICE_SERVER_NAME":
			return "from.env", true
		case "LATTICE_PASSWORD":
			// A password left in an environment is not a setting of the deployment: it
			// would become the password of whichever account is added next.
			return "a-variable-nobody-asked-for", true
		case "LATTICE_ADMIN":
			return "true", true
		}
		return "", false
	}

	var out bytes.Buffer
	// The password comes from stdin, because -password was not given and LATTICE_PASSWORD
	// deliberately does not fill it.
	if err := cli.RunWithEnv(cli.AddUser, []string{"pieter"}, strings.NewReader("hand typed\n"), &out, env); err != nil {
		t.Fatalf("add-user with only the environment to go on failed: %v", err)
	}
	if !strings.Contains(out.String(), "@pieter:from.env created") {
		t.Fatalf("add-user said %q, want an account of the server name in the environment", out.String())
	}

	db, err := persist.NewDatabase(path)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	account, err := db.UserRepo.GetByLocalpart("pieter")
	if err != nil {
		t.Fatalf("the account is not in the database: %v", err)
	}
	if account.PasswordHash == "a-variable-nobody-asked-for" {
		t.Error("LATTICE_PASSWORD became the password of a new account")
	}

	// A flag typed by hand still outranks the variable: the same precedence the server
	// follows for its own flags. A fresh file, because a name that does not match the
	// accounts already in the first one is refused before anything is written.
	flagged := filepath.Join(t.TempDir(), "flagged.db")
	var second bytes.Buffer
	flags := func(name string) (string, bool) {
		switch name {
		case "LATTICE_DB":
			return path, true
		case "LATTICE_SERVER_NAME":
			return "from.env", true
		}
		return "", false
	}
	if err := cli.RunWithEnv(cli.AddUser, []string{"klamoudis", "-db", flagged, "-server-name", "from.the.flag", "-password", "typed-by-hand"}, strings.NewReader(""), &second, flags); err != nil {
		t.Fatalf("add-user with a flag and a variable failed: %v", err)
	}
	if !strings.Contains(second.String(), "@klamoudis:from.the.flag created") {
		t.Fatalf("add-user said %q, want the flag's server name to win", second.String())
	}
}

// TestAnOperatorCommandRemovesWhatAnAccountHeld checks that the effect of removal is the
// whole effect, not just a flag on the row: sessions, keys and rooms all go the same way
// they do when the account deactivates itself.
func TestAnOperatorCommandRemovesWhatAnAccountHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	server := NewServerWithDatabase(t, path, api.Config{RegistrationDisabled: true})

	if out := operator(t, cli.AddUser, "gone", "-db", path, "-server-name", "test.server"); !strings.Contains(out, "created") {
		t.Fatalf("setting up the account said %q", out)
	}
	session := server.Login("gone", "w1nter-snow")
	roomID := server.CreateRoom(session, map[string]any{"name": "to leave"})

	db, err := persist.NewDatabase(path)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	if sessions, err := db.SessionRepo.ListByUser(session.UserID); err != nil || len(sessions) == 0 {
		t.Fatalf("before removal the account holds %d sessions (err %v), want at least one", len(sessions), err)
	}
	if rooms, err := db.MembershipRepo.RoomsFor(session.UserID); err != nil || len(rooms) != 1 || rooms[0] != roomID {
		t.Fatalf("before removal the account is in %v (err %v), want just %s", rooms, err, roomID)
	}

	result := operator(t, cli.RemoveUser, "gone", "-db", path, "-server-name", "test.server", "-yes")
	if !strings.Contains(result, "is deactivated") {
		t.Fatalf("remove-user said %q", result)
	}

	account, err := db.UserRepo.GetByLocalpart("gone")
	if err != nil {
		t.Fatalf("reading the account: %v", err)
	}
	if !account.Deactivated {
		t.Error("the account was not deactivated")
	}
	if account.PasswordHash != "" {
		t.Error("a deactivated account kept its password hash")
	}
	if sessions, err := db.SessionRepo.ListByUser(session.UserID); err != nil || len(sessions) != 0 {
		t.Errorf("after removal the account still has %d sessions (err %v)", len(sessions), err)
	}
	if rooms, err := db.MembershipRepo.RoomsFor(session.UserID); err != nil || len(rooms) != 0 {
		t.Errorf("after removal the account is still in %d rooms (err %v)", len(rooms), err)
	}

	// Removal is a state, not an event: asking twice says so instead of failing.
	again := operator(t, cli.RemoveUser, "gone", "-db", path, "-server-name", "test.server", "-yes")
	if !strings.Contains(again, "already deactivated") {
		t.Errorf("removing an already removed account said %q", again)
	}
}
