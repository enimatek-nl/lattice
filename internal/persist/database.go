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
	// StateGroups holds the state each event was sent against. Current state is a
	// convenience; the per-event snapshots are what a fork is resolved from.
	StateGroups StateGroupRepo
	Aliases     RoomAliasRepo
	Filters     FilterRepo
	AccountData AccountDataRepo
	Presence    PresenceRepo
	// Typing is the "is typing here" indicator: one current value per (room, user) with a
	// timeout, not an event.
	Typing TypingRepo
	// Receipts holds each user's read position per room, which is what sync delivers as an
	// `m.receipt` ephemeral event and what an unread count is measured from.
	Receipts ReceiptRepo
	// Relations indexes which events relate to which, which is what makes a thread view and
	// an edit chain a lookup rather than a scan of the room.
	Relations RelationRepo
	// Pushers records where a user's devices have asked to be woken, and which session asked.
	Pushers PusherRepo
	// Forgotten records which rooms a user has asked not to be told about. It is a fact about
	// one person's relationship to one room, not about the room.
	Forgotten   ForgetRepo
	DeviceLists DeviceListRepo
	DeviceKeys  DeviceKeysRepo
	Federation  FederationRepo
	// Media holds what this server knows about content it stores or has cached, in the content
	// repository. The bytes themselves are files under the repository's directory.
	Media MediaRepo
	// UserDirectory reads who a person may be told about and under which names they appear in each
	// room. It is a read model over room state and accounts rather than a table of its own, because a
	// maintained copy of "who is in which room" would be a second answer to a question room state
	// already answers.
	UserDirectory UserDirectoryRepo
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
	d.StateGroups = NewStateGroupRepo(gdb)
	d.Aliases = NewRoomAliasRepo(gdb)
	d.Filters = NewFilterRepo(gdb)
	d.AccountData = NewAccountDataRepo(gdb)
	d.Presence = NewPresenceRepo(gdb)
	d.Typing = NewTypingRepo(gdb)
	d.Receipts = NewReceiptRepo(gdb)
	d.Relations = NewRelationRepo(gdb)
	d.Forgotten = NewForgetRepo(gdb)
	d.Pushers = NewPusherRepo(gdb)
	d.DeviceLists = NewDeviceListRepo(gdb)
	d.Federation = NewFederationRepo(gdb)
	d.DeviceKeys = NewDeviceKeysRepo(gdb)
	d.Media = NewMediaRepo(gdb)
	d.UserDirectory = NewUserDirectoryRepo(gdb)
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
		&Room{}, &Event{}, &EventEdge{}, &RoomState{}, &EventAuthContext{}, &EventTxn{}, &RoomAlias{}, &Filter{}, &AccountData{}, &Presence{}, &Receipt{}, &Relation{}, &RoomForget{}, &Pusher{}, &DeviceListChange{}, &DeviceKeyDoc{}, &OneTimeKey{}, &Typing{},
		&DeviceListStream{},
		&ServerSigningKey{}, &ServerKeyDocument{}, &FederationTransaction{}, &FederationQueueItem{},
		&MediaMetadata{},
		&EventRejection{}, &Redaction{},
		&StateGroup{}, &StateGroupEntry{}, &EventStateGroup{}); err != nil {
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

		// A user's account data is read by type, and sync reads it by position.
		// The old unique key was (user, type) and cannot hold a room-scoped value: two
		// rooms annotated with the same type by the same user are two different pieces of
		// data. Dropped by name, and only then replaced, because "IF NOT EXISTS" on the new
		// name would leave the old constraint in force on an existing database.
		"DROP INDEX IF EXISTS idx_account_data_user_type",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_account_data_user_room_type ON account_data(user_id, room_id, data_type)",
		"CREATE INDEX IF NOT EXISTS idx_account_data_user_stream ON account_data(user_id, stream_ordering)",

		// A user has one read position per room and kind of receipt, read by position for
		// the same reason presence is: a client asks what changed since its last sync.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_receipts_room_type_user_thread ON receipts(room_id, receipt_type, user_id, thread_id)",
		"CREATE INDEX IF NOT EXISTS idx_receipts_room_stream ON receipts(room_id, stream_ordering)",

		// A relation is claimed by exactly one event, and read by what it points at: the
		// pair of indexes is the endpoint `/relations/{eventID}` and its thread badge.
		// A user has one forget marker per room, and it is read per user on every sync.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_room_forgotten_user_room ON room_forgotten(user_id, room_id)",

		// A pusher is replaced under the triple the client names it by, and a password change
		// asks one question of them all: which of this user's pushers did not come from the
		// session that just proved the new password.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_pushers_user_pushkey_app ON pushers(user_id, pushkey, app_id)",
		"CREATE INDEX IF NOT EXISTS idx_pushers_user_token ON pushers(user_id, token_hash)",

		"CREATE UNIQUE INDEX IF NOT EXISTS idx_event_relations_event ON event_relations(event_id)",
		"CREATE INDEX IF NOT EXISTS idx_event_relations_room_target_stream ON event_relations(room_id, target_event_id, stream_ordering)",

		// Presence is one current row per user, read by position when sync asks what
		// changed since a token.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_presence_user ON presence(user_id)",
		"CREATE INDEX IF NOT EXISTS idx_presence_stream ON presence(stream_ordering)",

		// A sync reads one observer's unread changes, oldest first.
		"CREATE INDEX IF NOT EXISTS idx_device_list_changes_observer_stream ON device_list_changes(observer_id, stream_ordering)",

		// The outbound worker claims the items that are due, in destination order, and a
		// caller asking "has this event already been queued for that server?" needs the
		// pair.
		"CREATE INDEX IF NOT EXISTS idx_federation_queue_due ON federation_queue(dropped_at, next_attempt_at, destination, id)",
		"CREATE INDEX IF NOT EXISTS idx_federation_queue_destination_event ON federation_queue(destination, event_id)",

		// Claiming a one-time key asks for the oldest unclaimed key of one device, which
		// is what this index serves.
		"CREATE INDEX IF NOT EXISTS idx_one_time_keys_device_claimed ON one_time_keys(user_id, device_id, claimed)",

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
		// Idempotency of PUT /send/{type}/{txn}: unique per device *and room*, not global.
		// The drop is for databases written before the room was part of the key: they carry an
		// index that would let a transaction ID used in one room answer a send to another, and
		// `IF NOT EXISTS` alone would leave that index in place forever.
		"DROP INDEX IF EXISTS idx_event_txn",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_event_txn ON event_txn(device_id, room_id, txn_id)",
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
