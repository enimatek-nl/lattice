package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// add-user: the account a closed registration door keeps the server from getting.
//
// There is deliberately no equivalent endpoint behind the API for the second and later
// accounts, because an endpoint that creates an account without the registration rules
// is a hole in whatever the operator closed with -disable-registration and
// -registration-secret. The command is not a hole: it is run by whoever already holds
// the database file, which is the strongest permission in a single-file homeserver.
func runAddUser(inv Invocation) error {
	s := &settings{}
	fs := newFlagSet(inv, AddUser, s)
	password := fs.String("password", "", "password for the new account; asked for when empty")
	displayName := fs.String("display-name", "", "profile display name of the new account")
	admin := fs.Bool("admin", false, "make the account an administrator")
	if err := parse(fs, inv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		if fs.NArg() == 0 {
			return errors.New("name the account to create, e.g. `lattice add-user alice` (`lattice add-user -h` explains the rest)")
		}
		return fmt.Errorf("one account per run, got %d names: %v", fs.NArg(), fs.Args())
	}
	// A name typed at a keyboard may carry a space; a name sent by a client has to be
	// exactly what it sent, so the trimming belongs here and not in the service.
	username := strings.TrimSpace(fs.Arg(0))
	// A localpart is stored lower case everywhere else in this server, so a name typed
	// with capitals is answered back as the name the operator has to sign in with.
	if username != strings.ToLower(username) {
		say(inv.out, "note: the account name is stored lower case, so %s becomes %s\n", username, strings.ToLower(username))
	}
	// The name is checked before a password is asked for, because the password is the one
	// thing in this exchange that cannot be corrected by typing it again: nobody should
	// have to enter a secret twice for an account name that was never going to work. The
	// service checks it again when the account is written, since that is the check that
	// actually protects the database.
	if !matrix.ValidLocalpart(strings.ToLower(username)) {
		return fmt.Errorf("%q is not a name this server can hold: an account name is lower case letters, numbers, dot, dash, underscore, equals and slash", username)
	}

	pass, err := readPassword(*password, inv, "password for "+username)
	if err != nil {
		return err
	}
	if pass == "" {
		return errors.New("an empty password would lock the account the moment it is created")
	}

	db, svc, err := open(*s)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := checkServerName(db, s, inv); err != nil {
		return err
	}

	ctx := context.Background()
	user, err := svc.CreateAccount(ctx, service.AccountParams{
		Username:    username,
		Password:    pass,
		DisplayName: strings.TrimSpace(*displayName),
		Admin:       *admin,
	})
	if err != nil {
		return err
	}
	say(inv.out, "%s created", user.ID)
	if user.Admin {
		sayText(inv.out, ", as an administrator")
	}
	sayLine(inv.out)
	if firstAccount(db) {
		// Naming the rule that made this account an administrator, rather than letting
		// the operator find out when a second one is not.
		sayLine(inv.out, "This is the first account on this database, so it is an administrator whether or not -admin is given.")
	}
	sayLine(inv.out, "Sign in with that user ID and the password you just set; a client asks for both, and this works while the homeserver is running.")
	return nil
}

// firstAccount reports whether this database holds exactly one account, which is the
// state the "first account is the administrator" rule has just produced.
func firstAccount(db *persist.Database) bool {
	total, err := db.UserRepo.Count()
	return err == nil && total == 1
}

// checkServerName refuses to write an account whose name this database has never
// served. The server name is not a label: it is the tail of every user ID, so an account
// created under the wrong one authenticates and can then join nothing, hold nothing and
// be invited nowhere - the trap main.go warns about at start-up, caught here before the
// row exists.
func checkServerName(db *persist.Database, s *settings, inv Invocation) error {
	total, err := db.UserRepo.Count()
	if err != nil {
		return fmt.Errorf("reading the accounts of %s: %w", s.dbPath, err)
	}
	if total == 0 {
		return nil
	}
	stray, err := db.UserRepo.CountOutsideDomain(s.serverName)
	if err != nil {
		return fmt.Errorf("reading the accounts of %s: %w", s.dbPath, err)
	}
	if stray == total {
		return fmt.Errorf("none of the %d accounts in %s is an account of %q, so that is not the name this server serves; pass -server-name (or set LATTICE_SERVER_NAME) to the name those accounts end with, or this command would create one that can sign in and use nothing",
			total, s.dbPath, s.serverName)
	}
	if stray > 0 {
		say(inv.out, "warning: %d of the %d accounts here do not end in %s and cannot use this server\n", stray, total, s.serverName)
	}
	return nil
}

// remove-user: an account taken away by the person who owns the file, because the flow
// that ends an account over HTTP asks that account for its password.
func runRemoveUser(inv Invocation) error {
	s := &settings{}
	fs := newFlagSet(inv, RemoveUser, s)
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	if err := parse(fs, inv); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		if fs.NArg() == 0 {
			return errors.New("name the account to remove, e.g. `lattice remove-user alice`")
		}
		return fmt.Errorf("one account per run, got %d names: %v", fs.NArg(), fs.Args())
	}

	db, svc, err := open(*s)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	name := strings.ToLower(strings.TrimSpace(fs.Arg(0)))
	account, err := findAccount(db, name)
	if err != nil {
		return err
	}
	if account.Deactivated {
		say(inv.out, "%s is already deactivated; nothing was changed\n", account.ID)
		return nil
	}

	sessions, err := db.SessionRepo.ListByUser(account.ID)
	if err != nil {
		return fmt.Errorf("reading the sessions of %s: %w", account.ID, err)
	}
	rooms, err := db.MembershipRepo.RoomsFor(account.ID)
	if err != nil {
		return fmt.Errorf("reading the rooms of %s: %w", account.ID, err)
	}

	// The user ID typed back is the confirmation, because it is the one thing that
	// cannot be hit by accident, and the action has no undo: the sessions and the key
	// material of an account are what its other devices and its peers are holding.
	if !*yes && isTerminal(inv.stdin) {
		say(inv.out, "Removing %s ends %d session(s), drops its end-to-end keys and key backups, and takes it out of %d room(s).\nType the user ID to confirm: ",
			account.ID, len(sessions), len(rooms))
		typed, err := readLine(inv)
		if err != nil {
			return err
		}
		if typed != account.ID && typed != account.Localpart {
			return errors.New("that is not the user ID, so nothing was done")
		}
	}

	ctx := context.Background()
	if err := svc.DeactivateLocalAccount(ctx, account.ID); err != nil {
		return err
	}
	say(inv.out, "%s is deactivated: %d session(s) ended, its device keys, cross-signing keys and room-key backups removed, and it left %d room(s).\n",
		account.ID, len(sessions), len(rooms))
	say(inv.out, "The name stays taken. Its user ID is written into every event it ever sent, so a new account under it would inherit those messages in rooms that still show them as its own.\n")
	return nil
}

// findAccount resolves what the operator typed - a localpart or a whole user ID - into
// an account, and says plainly when there is no such account.
func findAccount(db *persist.Database, name string) (*persist.User, error) {
	localpart := matrix.LocalpartOf(strings.TrimPrefix(name, "@"))
	account, err := db.UserRepo.GetByLocalpart(localpart)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, fmt.Errorf("no account named %s in this database; `lattice users` lists what is in it", localpart)
		}
		return nil, fmt.Errorf("reading the account %s: %w", localpart, err)
	}
	return account, nil
}

// users: the list every other command is answered from.
func runUsers(inv Invocation) error {
	s := &settings{}
	fs := newFlagSet(inv, Users, s)
	limit := fs.Int("limit", 100, "how many accounts to list")
	all := fs.Bool("all", false, "list every account")
	if err := parse(fs, inv); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("this command takes no names, got %v", fs.Args())
	}

	db, _, err := open(*s)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	total, err := db.UserRepo.Count()
	if err != nil {
		return fmt.Errorf("reading the accounts of %s: %w", s.dbPath, err)
	}
	shown := *limit
	if *all {
		shown = int(total)
	}
	if shown < 1 {
		// A limit of nothing is not an empty list and not "everything" either: it reads as
		// a database with no accounts in it, which is the last thing an operator checking
		// up on a server should be told. One row is the smallest honest answer.
		shown = 1
	}
	accounts, err := db.UserRepo.List(shown, 0)
	if err != nil {
		return fmt.Errorf("reading the accounts of %s: %w", s.dbPath, err)
	}
	say(inv.out, "%s holds %d account(s) of %s\n", s.dbPath, total, s.serverName)
	for _, account := range accounts {
		say(inv.out, "  %s%s\n", account.ID, marks(account))
	}
	if int64(len(accounts)) < total {
		say(inv.out, "  ... %d more; -all lists them\n", total-int64(len(accounts)))
	}
	// Deactivated accounts are invisible to List, which is right for a count of who
	// can sign in and wrong for an operator wondering where an account went.
	if strays, err := deactivatedCount(db); err == nil && strays > 0 {
		say(inv.out, "%d account(s) here are deactivated and cannot sign in\n", strays)
	}
	if strays, err := db.UserRepo.CountOutsideDomain(s.serverName); err == nil && strays > 0 {
		say(inv.out, "%d of them do not end in %s and cannot use this server; -server-name has to be the name they were created for\n", strays, s.serverName)
	}
	return nil
}

// marks renders what an account is, beside its ID.
func marks(account *persist.User) string {
	var flags []string
	if account.Admin {
		flags = append(flags, "administrator")
	}
	if account.Guest {
		flags = append(flags, "guest")
	}
	if account.Locked {
		flags = append(flags, "locked")
	}
	if account.Suspended {
		flags = append(flags, "suspended")
	}
	if len(flags) == 0 {
		return ""
	}
	return "  (" + strings.Join(flags, ", ") + ")"
}

// deactivatedCount counts the accounts that can no longer sign in. The user repository
// keeps them out of every listing, so the only way to see them is to ask for them.
func deactivatedCount(db *persist.Database) (int64, error) {
	var count int64
	err := db.GORM().Model(&persist.User{}).Where("deactivated = ?", true).Count(&count).Error
	return count, err
}

// usageAddUser and friends: the whole explanation of one command, printed by `help`
// and by the command's own -h.
func usageAddUser(out io.Writer) {
	sayText(out, `lattice `+AddUser+` <name> [-password P] [-admin] [-display-name D] [-db PATH] [-server-name NAME]

Create a local account and set its password. This is how an account is added to a
server started with -disable-registration: that flag closes the registration endpoint
clients use, not the way in for the person who owns the database.

The first account on a fresh database becomes an administrator, so the administrative
endpoints have somebody to answer before any flag names them. -admin makes a later one
an administrator too.

  lattice `+AddUser+` alice
  echo "$PASSWORD" | lattice `+AddUser+` alice
  docker exec -it lattice lattice `+AddUser+` alice        # registration is closed, the container holds the database

The password is asked for when -password is not given: typed without echo in a
terminal, read as one line from a pipe. It is stored as a bcrypt hash and printed
nowhere.

The account can sign in as soon as it exists, including while the homeserver is
running on the same file.
`)
}

func usageRemoveUser(out io.Writer) {
	sayText(out, `lattice `+RemoveUser+` <name> [-yes] [-db PATH] [-server-name NAME]

Deactivate an account: its access tokens stop working, its devices' end-to-end keys,
cross-signing keys and room-key backups are dropped, and it leaves every room it was
in. The account's password is cleared and it can no longer sign in or be invited.

The row is not deleted, and the name stays taken. A Matrix user ID is written into
every event that account ever sent, so deleting the row would let a new account be
created under the same ID and own the messages of the one that left.

In a terminal the user ID has to be typed back before anything is written; -yes skips
that, which is what a script wants.

  lattice `+RemoveUser+` alice
  docker exec lattice lattice `+RemoveUser+` alice -yes
`)
}

func usageUsers(out io.Writer) {
	sayText(out, `lattice users [-limit N] [-all] [-db PATH] [-server-name NAME]

List the accounts in a database, with what each one is: administrator, guest, locked,
suspended. Deactivated accounts are counted separately, because they can no longer sign
in but are still in the file so that their names stay taken.

  lattice users
  docker exec lattice lattice users -all
`)
}

// commandUsage prints the explanation of one command.
func commandUsage(out io.Writer, name string) {
	for _, c := range commands() {
		if c.name == name {
			c.usage(out)
			return
		}
	}
}

// isTerminal reports whether the operator can be asked something, which decides whether
// a password is prompted for and whether a removal is confirmed.
func isTerminal(in io.Reader) bool {
	file, ok := in.(*os.File)
	return ok && termIsTerminal(file.Fd())
}
