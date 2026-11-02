package compat

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"gorm.io/gorm"
)

// TestAFileDatabaseCarriesOnUnderConcurrentWriters is about a failure that only a
// second process can see. Every test in this suite runs against an in-memory
// database with one connection, which makes write contention impossible - and a
// deployment runs against a file, where a send, a receipt and a device list update
// can ask for the write lock in the same millisecond. The homeserver answered with
// "500 M_UNKNOWN: insert event: sqlite3: database is locked", which is a message
// lost rather than a message delayed.
//
// The fix is in how the database is opened, so the test checks both the settings
// and the behaviour they are meant to buy.
func TestAFileDatabaseCarriesOnUnderConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	db, err := persist.NewDatabase(path)
	if err != nil {
		t.Fatalf("opening the file database: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Read the settings off the connection rather than the path: what matters is what
	// the database agreed to, not what was asked.
	if mode := pragma(t, db, "journal_mode"); mode != "wal" {
		t.Errorf("the file database uses journal mode %q, want wal: readers would block the writer", mode)
	}
	if timeout := pragmaInt(t, db, "busy_timeout"); timeout < 5000 {
		t.Errorf("the file database waits %d ms for the write lock, want at least 5000", timeout)
	}

	const writers = 12
	const perWriter = 5
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		problems []string
		written  int
	)
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for n := 0; n < perWriter; n++ {
				// This is the shape a real request makes: a transaction that reads and
				// then writes. It is the case waiting cannot rescue, because SQLite takes
				// the write lock when the transaction upgrades mid-flight and that
				// upgrade is refused outright - unless the transaction begins in write
				// mode, which is what the database is opened to do.
				err := db.Tx(func(tx *gorm.DB) error {
					var count int64
					if err := tx.Model(&persist.Event{}).Count(&count).Error; err != nil {
						return err
					}
					return tx.Create(&persist.Event{
						EventID:        fmt.Sprintf("$contended-%d-%d", writer, n),
						RoomID:         "!contended:test.server",
						Type:           matrix.TypeMessage,
						Sender:         "@nobody:test.server",
						StreamPos:      uint64(writer*perWriter + n + 1),
						Content:        `{"body":"contention"}`,
						OriginServerTS: time.Now().UnixMilli(),
					}).Error
				})
				if err != nil {
					mu.Lock()
					if len(problems) < 3 {
						problems = append(problems, err.Error())
					}
					mu.Unlock()
					continue
				}
				mu.Lock()
				written++
				mu.Unlock()
			}
		}(writer)
	}
	wg.Wait()

	if len(problems) > 0 {
		t.Errorf("%d of %d concurrent writes failed: %v", writers*perWriter-written, writers*perWriter, problems)
	}
	var stored int64
	if err := db.GORM().Model(&persist.Event{}).Count(&stored).Error; err != nil {
		t.Fatalf("counting the stored events: %v", err)
	}
	if stored != writers*perWriter {
		t.Errorf("the database holds %d events, want all %d that were written", stored, writers*perWriter)
	}
}

// pragma reads a single scalar pragma from the connection.
func pragma(t *testing.T, db *persist.Database, name string) string {
	t.Helper()
	var value string
	if err := db.GORM().Raw("PRAGMA " + name).Scan(&value).Error; err != nil {
		t.Fatalf("reading PRAGMA %s: %v", name, err)
	}
	return value
}

// pragmaInt reads a numeric pragma.
func pragmaInt(t *testing.T, db *persist.Database, name string) int {
	t.Helper()
	var value int
	if err := db.GORM().Raw("PRAGMA " + name).Scan(&value).Error; err != nil {
		t.Fatalf("reading PRAGMA %s: %v", name, err)
	}
	return value
}

// TestAnAccountWrittenForAnotherServerNameIsNamed covers what an operator sees when
// -server-name changes under a database that already holds accounts. The sessions outlive
// the name they were minted under, because they are rows in this file: the client keeps
// authenticating, keeps looking logged in, and then every room join fails. A message that
// says "you must have an account to join a room" sends the operator off looking for a
// missing account, when the account is there and the server's own name has moved.
func TestAnAccountWrittenForAnotherServerNameIsNamed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")

	first := NewServerWithDatabase(t, path, api.Config{AllowGuests: true, Federation: true})
	alice := first.Register("alice", "otterburn")
	roomID := first.CreateRoom(alice, map[string]any{"name": "renamed"})

	// The control: the same account, the same room, the same request, answered by the
	// server whose name the account was written for.
	joined := first.Do(http.MethodPost, "/_matrix/client/v3/join/"+url.PathEscape(roomID),
		alice.Token, map[string]any{})
	if joined.Status != http.StatusOK {
		t.Fatalf("join by the account's own server: got %d, want 200 (%s)", joined.Status, joined.Raw)
	}
	if got := joined.String("room_id"); got != roomID {
		t.Errorf("join by the account's own server: room_id = %q, want %s (%s)", got, roomID, joined.Raw)
	}

	renamed := NewServerWithDatabase(t, path, api.Config{AllowGuests: true, Federation: true, ServerName: "second.server"})
	refused := renamed.Do(http.MethodPost, "/_matrix/client/v3/join/"+url.PathEscape(roomID),
		alice.Token, map[string]any{})
	if refused.Status != http.StatusForbidden || refused.String("errcode") != string(matrix.ErrCodeForbidden) {
		t.Fatalf("renamed server: got %d %s, want 403 M_FORBIDDEN (%s)",
			refused.Status, refused.String("errcode"), refused.Raw)
	}
	for _, want := range []string{alice.UserID, "second.server"} {
		if !strings.Contains(string(refused.Raw), want) {
			t.Errorf("the refusal %q does not name %s", refused.Raw, want)
		}
	}
}

// TestAccountsOutsideTheServersNameAreCounted pins the query the startup warning is built
// from. A user ID carries the server name it was created for, so a database can hold
// accounts the running server cannot act for - and the count has to be exact, which means
// a name containing a LIKE wildcard must not reach a wider set of accounts than it owns.
func TestAccountsOutsideTheServersNameAreCounted(t *testing.T) {
	db, err := persist.NewDatabase(filepath.Join(t.TempDir(), "counted.db"))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	accounts := []struct{ id, localpart string }{
		{"@alice:example.org", "alice"},
		{"@bob:example.org", "bob"},
		{"@carol:other.example", "carol"},
		{"@dave:exampleXorg", "dave"},
		{"@eve:exa_mple.org", "eve"},
		{"@frank:exaXmple.org", "frank"},
	}
	for _, account := range accounts {
		if err := db.UserRepo.Create(&persist.User{ID: account.id, Localpart: account.localpart}); err != nil {
			t.Fatalf("recording %s: %v", account.id, err)
		}
	}

	for _, want := range []struct {
		domain  string
		outside int64
	}{
		// alice and bob belong; carol is another server, dave's name only looks alike
		// because a dot is not a wildcard, and the underscore pair is decided by escaping.
		{domain: "example.org", outside: 4},
		{domain: "exa_mple.org", outside: 5},
		{domain: "nowhere.example", outside: 6},
	} {
		got, err := db.UserRepo.CountOutsideDomain(want.domain)
		if err != nil {
			t.Fatalf("counting accounts outside %s: %v", want.domain, err)
		}
		if got != want.outside {
			t.Errorf("accounts outside %s = %d, want %d", want.domain, got, want.outside)
		}
	}
}
