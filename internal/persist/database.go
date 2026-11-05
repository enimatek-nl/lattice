// Package persist implements storage. Services reach the database only through
// the repository interfaces declared here, so the engine can be replaced without
// touching protocol code (AGENTS.md §10).
package persist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ErrNotFound is returned by repositories when no row matches.
var ErrNotFound = errors.New("persist: not found")

// ErrConflict is returned when a uniqueness constraint blocks a write.
var ErrConflict = errors.New("persist: conflict")

// Database bundles the connection and every repository.
type Database struct {
	db *gorm.DB

	Users       UserRepo
	Devices     DeviceRepo
	Tokens      TokenRepo
	SigningKeys SigningKeyRepo
	Rooms       RoomRepo
	Events      EventRepo
	State       StateRepo
	Aliases     RoomAliasRepo
}

// Open opens (creating if needed) the SQLite database at path.
func Open(path string, log *slog.Logger) (*Database, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("persist: cannot create data directory: %w", err)
		}
	}
	gdb, err := gorm.Open(gormlite.Open(path), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
		// SQLite rejects concurrent writers; serialize them in the pool.
		DisableAutomaticPing: false,
	})
	if err != nil {
		return nil, fmt.Errorf("persist: cannot open database: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("persist: cannot access database handle: %w", err)
	}
	if err := applyPragmas(gdb); err != nil {
		return nil, err
	}
	// A single writer keeps SQLite consistent; readers share the WAL snapshot.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(time.Hour)

	d := &Database{db: gdb}
	if err := d.migrate(log); err != nil {
		return nil, err
	}
	d.Users = NewUserRepo(gdb)
	d.Devices = NewDeviceRepo(gdb)
	d.Tokens = NewTokenRepo(gdb)
	d.SigningKeys = NewSigningKeyRepo(gdb)
	d.Rooms = NewRoomRepo(gdb)
	d.Events = NewEventRepo(gdb)
	d.State = NewStateRepo(gdb)
	d.Aliases = NewRoomAliasRepo(gdb)
	return d, nil
}

func applyPragmas(gdb *gorm.DB) error {
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA wal_autocheckpoint=1000",
	} {
		if err := gdb.Exec(p).Error; err != nil {
			return fmt.Errorf("persist: %s failed: %w", p, err)
		}
	}
	return nil
}

// migrate creates or updates the schema. Model migration keeps the schema tied
// to the Go types; the explicit index statements document the indexes that
// matter for Matrix lookups and are not derived from struct tags.
func (d *Database) migrate(log *slog.Logger) error {
	if err := d.db.AutoMigrate(&User{}, &Device{}, &AccessToken{}, &SigningKey{},
		&Room{}, &Event{}, &EventEdge{}, &RoomState{}, &EventAuthContext{}, &EventTxn{}, &RoomAlias{}); err != nil {
		return fmt.Errorf("persist: migration failed: %w", err)
	}
	statements := []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_users_user_id ON users(user_id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_users_localpart ON users(localpart)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_user_device ON devices(user_id, device_id)",
		"CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_tokens_hash ON access_tokens(token_hash)",
		"CREATE INDEX IF NOT EXISTS idx_tokens_user_device ON access_tokens(user_id, device_id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_signing_keys_key_id ON signing_keys(key_id)",

		// Room and event lookups. An event ID is globally unique, which is what
		// turns a repeated event into ErrConflict instead of a second row.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_rooms_room_id ON rooms(room_id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_events_event_id ON events(event_id)",
		// History and read positions walk a room in stream order.
		"CREATE INDEX IF NOT EXISTS idx_events_room_stream ON events(room_id, stream_ordering)",
		// State lookups by (room, type, state_key) and membership by sender.
		"CREATE INDEX IF NOT EXISTS idx_events_room_type_state ON events(room_id, type, state_key)",
		"CREATE INDEX IF NOT EXISTS idx_events_sender ON events(sender)",
		// The event graph: forward extremities need the reverse direction, and
		// auth chain walks need the forward one.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_event_edges_unique ON event_edges(event_id, reference_id, kind)",
		"CREATE INDEX IF NOT EXISTS idx_event_edges_reference ON event_edges(reference_id, kind)",
		// Current room state is a map keyed by (room, type, state_key).
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_room_state_key ON room_state(room_id, type, state_key)",
		"CREATE INDEX IF NOT EXISTS idx_room_state_event ON room_state(event_id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_context_unique ON event_auth_context(event_id, state_event_id)",
		"CREATE INDEX IF NOT EXISTS idx_auth_context_event ON event_auth_context(event_id)",
		// Idempotency of PUT /send/{type}/{txn}: unique per device, not global.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_event_txn ON event_txn(device_id, txn_id)",
		// An alias resolves to exactly one room, and a room's aliases are listed
		// together.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_room_aliases_alias ON room_aliases(alias)",
		"CREATE INDEX IF NOT EXISTS idx_room_aliases_room ON room_aliases(room_id)",
	}
	for _, stmt := range statements {
		if err := d.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("persist: index statement failed: %w", err)
		}
	}
	if log != nil {
		log.Debug("database schema ready")
	}
	return nil
}

// DB exposes the underlying handle for transactions owned by services.
func (d *Database) DB() *gorm.DB { return d.db }

// Tx runs fn inside a transaction, rolling back on error.
func (d *Database) Tx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return d.db.WithContext(ctx).Transaction(fn)
}

// Close releases the underlying connection pool.
func (d *Database) Close() error {
	sqlDB, err := d.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
