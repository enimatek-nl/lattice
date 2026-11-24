package unit

import (
	"path/filepath"
	"testing"

	sqlite "github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/enimatek-nl/lattice/internal/persist"
)

// legacyPusher is a pusher row as it was written before a pusher had an owner in its unique scope:
// the index named the endpoint - pushkey, kind and app id - and nothing else. The table name is the
// one the real model uses, because this is the same table with an older index over it.
type legacyPusher struct {
	PushKey  string `gorm:"uniqueIndex:uq_pusher_scope;size:255"`
	Kind     string `gorm:"uniqueIndex:uq_pusher_scope;size:32"`
	AppID    string `gorm:"uniqueIndex:uq_pusher_scope;size:255"`
	UserID   string `gorm:"index;size:255"`
	DeviceID string `gorm:"size:64"`

	gorm.Model
}

// TableName puts the legacy row on the table the current model uses.
func (legacyPusher) TableName() string { return "pushers" }

// TestPusherScopeOfAnOldDatabase repairs is about a database that already exists. AutoMigrate adds
// a missing index and never rewrites one that exists, so the model asking for an owner in the scope
// changes nothing on disk: registering the endpoint of another account stays an update of that
// account's row, which is a pusher changing hands. The repair is what closes a database written
// before the model asked, and this test writes one.
func TestPusherScopeOfAnOldDatabaseRepairs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.db")
	gdb, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open the legacy database: %v", err)
	}
	if err := gdb.AutoMigrate(&legacyPusher{}); err != nil {
		t.Fatalf("write the legacy pushers table: %v", err)
	}
	seed := &legacyPusher{PushKey: "shared-key", Kind: "http", AppID: "dev.lattice.app",
		UserID: "@alice:test.server", DeviceID: "AAA"}
	if err := gdb.Create(seed).Error; err != nil {
		t.Fatalf("seed a pusher of the old shape: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("handle of the legacy database: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close the legacy database: %v", err)
	}

	// Opening it as it is found in the field: AutoMigrate and the repairs run on the existing file.
	db, err := persist.NewDatabase(path)
	if err != nil {
		t.Fatalf("open the database for the current server: %v", err)
	}
	set := func(userID, deviceID, name string) {
		pusher := &persist.Pusher{PushKey: "shared-key", Kind: "http", AppID: "dev.lattice.app",
			UserID: userID, DeviceID: deviceID, DeviceName: name, Active: true}
		if err := db.PusherRepo.Set(pusher); err != nil {
			t.Fatalf("register a pusher for %s: %v", userID, err)
		}
	}
	// Alice re-registers the row she already has, and Bob registers the same endpoint for himself.
	set("@alice:test.server", "AAA", "Alice phone")
	set("@bob:test.server", "BBB", "Bob tablet")

	alice, err := db.PusherRepo.List("@alice:test.server")
	if err != nil {
		t.Fatalf("list Alice's pushers: %v", err)
	}
	bob, err := db.PusherRepo.List("@bob:test.server")
	if err != nil {
		t.Fatalf("list Bob's pushers: %v", err)
	}
	if len(alice) != 1 || len(bob) != 1 {
		t.Errorf("the old scope is still in force: Alice has %d pushers and Bob %d, want one each",
			len(alice), len(bob))
		return
	}
	if alice[0].DeviceName != "Alice phone" {
		t.Errorf("the row that predates the name column still has none: %q", alice[0].DeviceName)
	}
	if bob[0].DeviceID != "BBB" {
		t.Errorf("Bob's pusher reads %s, want his own device", bob[0].DeviceID)
	}
}
