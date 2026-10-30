// Package persist contains the storage models and repositories for the
// Lattice homeserver. Every entity lives in its own file and exposes a model
// struct, a repository interface, a private implementation and a constructor
// that returns the interface.
package persist

import (
	"errors"
	"fmt"
	"strings"

	sqlite "github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Database owns the raw GORM connection and one repository instance per
// entity. Handlers receive a *Database and reach the entity they need through
// the exported repository fields.
type Database struct {
	gorm *gorm.DB

	AliasRepo        AliasRepo
	AccountDataRepo  AccountDataRepo
	DeviceRepo       DeviceRepo
	EventRepo        EventRepo
	FilterRepo       FilterRepo
	MediaRepo        MediaRepo
	MembershipRepo   MembershipRepo
	MetaRepo         MetaRepo
	NotificationRepo NotificationRepo
	PresenceRepo     PresenceRepo
	ProfileRepo      ProfileRepo
	ReportRepo       ReportRepo
	PusherRepo       PusherRepo
	PushRuleRepo     PushRuleRepo
	ReadMarkerRepo   ReadMarkerRepo
	ReceiptRepo      ReceiptRepo
	RoomRepo         RoomRepo
	SessionRepo      SessionRepo
	StateRepo        StateRepo
	ToDeviceRepo     ToDeviceRepo
	TxnRepo          TxnRepo
	TypingRepo       TypingRepo
	UserRepo         UserRepo
	VisibilityRepo   VisibilityRepo
}

// models lists every model that participates in AutoMigrate. New entities must
// be appended here and get a matching repository field above.
func models() []any {
	return []any{
		&User{},
		&Session{},
		&Device{},
		&Room{},
		&RoomAlias{},
		&RoomVisibility{},
		&Event{},
		&RoomState{},
		&RoomMembership{},
		&AccountData{},
		&Filter{},
		&Receipt{},
		&ReadMarker{},
		&PushRule{},
		&Pusher{},
		&Presence{},
		&Notification{},
		&Report{},
		&Media{},
		&Meta{},
		&ProfileField{},
		&Typing{},
		&ToDevice{},
		&Txn{},
	}
}

// NewDatabase opens (creating it when needed) the SQLite database at path and
// migrates all models. Use ":memory:" for an ephemeral database.
func NewDatabase(path string) (*Database, error) {
	dsn := path
	inMemory := path == ":memory:" || strings.HasPrefix(path, "file::memory")
	if inMemory {
		// Each fresh connection to ":memory:" would get its own empty database,
		// so the shared cache keeps one schema across the connection pool.
		dsn = "file::memory:?cache=shared"
	}
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}
	if inMemory {
		sqlDB, err := gdb.DB()
		if err != nil {
			return nil, fmt.Errorf("pool for %q: %w", path, err)
		}
		// A single connection keeps the ephemeral database alive for the whole
		// process and avoids write contention.
		sqlDB.SetMaxOpenConns(1)
	}
	if err := gdb.AutoMigrate(models()...); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return NewFromGORM(gdb), nil
}

// NewFromGORM wires the repositories around an already opened and migrated
// GORM connection.
func NewFromGORM(gdb *gorm.DB) *Database {
	return &Database{
		gorm:             gdb,
		AliasRepo:        NewAliasRepo(gdb),
		AccountDataRepo:  NewAccountDataRepo(gdb),
		DeviceRepo:       NewDeviceRepo(gdb),
		EventRepo:        NewEventRepo(gdb),
		FilterRepo:       NewFilterRepo(gdb),
		MediaRepo:        NewMediaRepo(gdb),
		MembershipRepo:   NewMembershipRepo(gdb),
		MetaRepo:         NewMetaRepo(gdb),
		NotificationRepo: NewNotificationRepo(gdb),
		PresenceRepo:     NewPresenceRepo(gdb),
		ProfileRepo:      NewProfileRepo(gdb),
		ReportRepo:       NewReportRepo(gdb),
		PusherRepo:       NewPusherRepo(gdb),
		PushRuleRepo:     NewPushRuleRepo(gdb),
		ReadMarkerRepo:   NewReadMarkerRepo(gdb),
		ReceiptRepo:      NewReceiptRepo(gdb),
		RoomRepo:         NewRoomRepo(gdb),
		SessionRepo:      NewSessionRepo(gdb),
		StateRepo:        NewStateRepo(gdb),
		ToDeviceRepo:     NewToDeviceRepo(gdb),
		TxnRepo:          NewTxnRepo(gdb),
		TypingRepo:       NewTypingRepo(gdb),
		UserRepo:         NewUserRepo(gdb),
		VisibilityRepo:   NewVisibilityRepo(gdb),
	}
}

// GORM returns the underlying connection. Prefer the repositories; this exists
// for the service layer, which needs transactions that span repositories.
func (d *Database) GORM() *gorm.DB { return d.gorm }

// Tx runs fn inside a database transaction, rolling back when fn returns an
// error.
func (d *Database) Tx(fn func(tx *gorm.DB) error) error {
	return d.gorm.Transaction(fn)
}

// Close releases the underlying database handle.
func (d *Database) Close() error {
	sqlDB, err := d.gorm.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// IsNotFound reports whether err means "no such record" at the storage layer.
func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// IsDuplicate reports whether err means a unique constraint violation.
func IsDuplicate(err error) bool {
	if err == nil {
		return false
	}
	// gormlite surfaces SQLITE_CONSTRAINT through gorm's ErrDuplicatedKey when
	// TranslateError is enabled, and as a raw message otherwise.
	return errors.Is(err, gorm.ErrDuplicatedKey) ||
		strings.Contains(err.Error(), "constraint failed")
}
