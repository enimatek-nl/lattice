package compat

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
