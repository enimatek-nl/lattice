// Package persist contains the storage models and repositories for the
// Lattice homeserver. Every entity lives in its own file and exposes a model
// struct, a repository interface, a private implementation and a constructor
// that returns the interface.
package persist

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	sqlite "github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Database owns the raw GORM connection and one repository instance per
// entity. Handlers receive a *Database and reach the entity they need through
// the exported repository fields.
type Database struct {
	gorm *gorm.DB

	AliasRepo        AliasRepo
	AccountDataRepo  AccountDataRepo
	CrossSigningRepo CrossSigningRepo
	DeviceRepo       DeviceRepo
	DeviceKeyRepo    DeviceKeyRepo
	DeviceStreamRepo DeviceStreamRepo
	DeviceListRepo   DeviceListRepo
	EventRepo        EventRepo
	OutboxRepo       OutboxRepo
	InboundTxnRepo   InboundTxnRepo
	ReceivedPDURepo  ReceivedPDURepo
	RemoteDeviceRepo RemoteDeviceRepo
	FilterRepo       FilterRepo
	KeyBackupRepo    KeyBackupRepo
	MediaRepo        MediaRepo
	MembershipRepo   MembershipRepo
	MetaRepo         MetaRepo
	NotificationRepo NotificationRepo
	OneTimeKeyRepo   OneTimeKeyRepo
	PresenceRepo     PresenceRepo
	ProfileRepo      ProfileRepo
	ReportRepo       ReportRepo
	PusherRepo       PusherRepo
	PushRuleRepo     PushRuleRepo
	ReadMarkerRepo   ReadMarkerRepo
	ReceiptRepo      ReceiptRepo
	RoomRepo         RoomRepo
	ServerKeyRepo    ServerKeyRepo
	SessionRepo      SessionRepo
	StateRepo        StateRepo
	SlidingSyncRepo  SlidingSyncRepo
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
		&SlidingSyncConnection{},
		&SlidingSyncRoom{},
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
		&DeviceKey{},
		&OneTimeKey{},
		&DeviceListChange{},
		&ServerKey{},
		&ForwardExtremity{},
		&InboundTxn{},
		&ReceivedPDU{},
		&OutboxItem{},
		&OutboundTxn{},
		&RemoteDevice{},
		&DeviceStream{},
		&CrossSigningKey{},
		&KeyBackupVersion{},
		&BackupRoomKey{},
	}
}

// NewDatabase opens (creating it when needed) the SQLite database at path and
// migrates all models. Use ":memory:" for an ephemeral database.
func NewDatabase(path string) (*Database, error) {
	var dsn string
	inMemory := path == ":memory:" || strings.HasPrefix(path, "file::memory")
	if inMemory {
		// Each fresh connection to ":memory:" would get its own empty database,
		// so the shared cache keeps one schema across the connection pool.
		dsn = "file::memory:?cache=shared"
	} else {
		// A file database is the one a real deployment uses, and a real deployment
		// writes from several requests at once: a sync, a send and a receipt can all
		// want the write lock in the same millisecond. Three settings make that a
		// queue rather than a 500 - and the compatibility suite never noticed, because
		// every test there runs against an in-memory database with one connection.
		//
		//   busy_timeout  wait for the lock instead of failing at once; the write is
		//                 late, not lost.
		//   journal_mode  WAL, so readers never block the writer and the writer never
		//                 blocks readers - a long-polling sync holds a read open.
		//   txlock        BEGIN IMMEDIATE: a transaction that means to write takes the
		//                 write lock at once. Without it SQLite upgrades a read
		//                 transaction mid-flight, and that upgrade cannot be waited
		//                 for - it returns SQLITE_BUSY no matter what busy_timeout is
		//                 set to.
		dsn = withPragmas(path)
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
	if err := backfillStateFlags(gdb); err != nil {
		return nil, fmt.Errorf("backfill state event markers: %w", err)
	}
	if err := sweepUnsignedOneTimeKeys(gdb); err != nil {
		return nil, fmt.Errorf("drop unverifiable one-time keys: %w", err)
	}
	if err := seedForwardExtremities(gdb); err != nil {
		return nil, fmt.Errorf("seed the graph leaves of existing rooms: %w", err)
	}
	return NewFromGORM(gdb), nil
}

// seedForwardExtremities gives every room that has events but no recorded leaves one
// leaf: its newest event.
//
// Rooms written before events carried prev_events have no graph to recover - the
// links were never stored - so the only honest answer is that the newest event is
// where the next one attaches. The repair only touches rooms with no rows of their
// own, which after the first run means no rooms at all, so it costs one indexed
// lookup per open.
func seedForwardExtremities(gdb *gorm.DB) error {
	var roomIDs []string
	if err := gdb.Model(&ForwardExtremity{}).Distinct().Pluck("room_id", &roomIDs).Error; err != nil {
		return err
	}
	known := make(map[string]bool, len(roomIDs))
	for _, roomID := range roomIDs {
		known[roomID] = true
	}
	var latest []Event
	if err := gdb.Model(&Event{}).
		Select("room_id, max(stream_pos) as stream_pos").
		Group("room_id").Scan(&latest).Error; err != nil {
		return err
	}
	for _, row := range latest {
		if known[row.RoomID] {
			continue
		}
		var newest Event
		if err := gdb.Where("room_id = ? AND stream_pos = ?", row.RoomID, row.StreamPos).
			First(&newest).Error; err != nil {
			if IsNotFound(err) {
				continue
			}
			return err
		}
		if err := gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(&ForwardExtremity{
			RoomID:         newest.RoomID,
			EventID:        newest.EventID,
			Depth:          newest.Depth,
			OriginServerTS: newest.OriginServerTS,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// sweepUnsignedOneTimeKeys is a data repair, run on every open like the state
// backfill, because there is no schema version to hang it on and it is cheap:
// the pool of unclaimed keys is bounded by MaxOneTimeKeys per device.
//
// Lattice used to rewrite an uploaded one-time key object into
// {"signed_curve25519": "..."}, which threw away the signature the device made
// over that object. Whoever claims the key verifies that signature, so every key
// stored in that shape was useless: no Olm session, and "unable to decrypt" on
// the other side. Deleting the rows is what lets an existing database recover -
// the owning device uploads a fresh, properly signed key on its next sync.
func sweepUnsignedOneTimeKeys(gdb *gorm.DB) error {
	_, err := NewOneTimeKeyRepo(gdb).SweepUnsignedOneTimeKeys()
	return err
}

// backfillStateFlags marks stored events as state events.
//
// Event.IsState exists because the state key of m.room.name, m.room.topic,
// m.room.create and the other empty-key state events is the empty string, which
// is the same value a timeline event has: without the marker those events reach a
// client without state_key, and a client reads them as messages. Databases written
// before the column existed carry no marker, so every event the current state of a
// room points at is marked, as is every event with a non-empty state key. A
// superseded empty-key state event cannot be recognised any more; it is delivered
// as a plain timeline event, which is how it always was.

func backfillStateFlags(gdb *gorm.DB) error {
	if err := gdb.Exec(`UPDATE events SET is_state = 1 WHERE is_state = 0 AND state_key <> ''`).Error; err != nil {
		return err
	}
	return gdb.Exec(`UPDATE events SET is_state = 1
		WHERE is_state = 0 AND event_id IN (SELECT event_id FROM room_states WHERE event_id <> '')`).Error
}

// NewFromGORM wires the repositories around an already opened and migrated
// GORM connection.
func NewFromGORM(gdb *gorm.DB) *Database {
	return &Database{
		gorm:             gdb,
		AliasRepo:        NewAliasRepo(gdb),
		AccountDataRepo:  NewAccountDataRepo(gdb),
		CrossSigningRepo: NewCrossSigningRepo(gdb),
		DeviceRepo:       NewDeviceRepo(gdb),
		DeviceKeyRepo:    NewDeviceKeyRepo(gdb),
		DeviceStreamRepo: NewDeviceStreamRepo(gdb),
		DeviceListRepo:   NewDeviceListRepo(gdb),
		EventRepo:        NewEventRepo(gdb),
		OutboxRepo:       NewOutboxRepo(gdb),
		InboundTxnRepo:   NewInboundTxnRepo(gdb),
		ReceivedPDURepo:  NewReceivedPDURepo(gdb),
		RemoteDeviceRepo: NewRemoteDeviceRepo(gdb),
		FilterRepo:       NewFilterRepo(gdb),
		SlidingSyncRepo:  NewSlidingSyncRepo(gdb),
		KeyBackupRepo:    NewKeyBackupRepo(gdb),
		MediaRepo:        NewMediaRepo(gdb),
		MembershipRepo:   NewMembershipRepo(gdb),
		MetaRepo:         NewMetaRepo(gdb),
		NotificationRepo: NewNotificationRepo(gdb),
		OneTimeKeyRepo:   NewOneTimeKeyRepo(gdb),
		PresenceRepo:     NewPresenceRepo(gdb),
		ProfileRepo:      NewProfileRepo(gdb),
		ReportRepo:       NewReportRepo(gdb),
		PusherRepo:       NewPusherRepo(gdb),
		PushRuleRepo:     NewPushRuleRepo(gdb),
		ReadMarkerRepo:   NewReadMarkerRepo(gdb),
		ReceiptRepo:      NewReceiptRepo(gdb),
		RoomRepo:         NewRoomRepo(gdb),
		ServerKeyRepo:    NewServerKeyRepo(gdb),
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

// databasePragmas are the settings a file database is opened with. They are only
// applied when the caller did not name the same pragma themselves, so an operator
// who wants a different durability or timeout can ask for it in the path.
var databasePragmas = []string{
	"busy_timeout(10000)",
	"journal_mode(WAL)",
}

// withPragmas renders the path as the file: URL the SQLite driver reads, with the
// pragmas and the transaction lock mode this server needs. A path that already
// carries a query string is kept as it is, because that is how an operator passes
// a pragma we have not thought of.
func withPragmas(path string) string {
	base := path
	query := url.Values{}
	if head, tail, found := strings.Cut(path, "?"); found {
		base = head
		if parsed, err := url.ParseQuery(tail); err == nil {
			query = parsed
		}
	}
	if !strings.HasPrefix(base, "file:") {
		base = "file:" + base
	}
	for _, pragma := range databasePragmas {
		name, _, _ := strings.Cut(pragma, "(")
		if hasPragma(query, name) {
			continue
		}
		query.Add("_pragma", pragma)
	}
	if !query.Has("_txlock") {
		query.Set("_txlock", "immediate")
	}
	return base + "?" + query.Encode()
}

// hasPragma reports whether a query already names a pragma.
func hasPragma(query url.Values, name string) bool {
	for _, pragma := range query["_pragma"] {
		if pragma == name || strings.HasPrefix(pragma, name+"(") || strings.HasPrefix(pragma, name+"=") {
			return true
		}
	}
	return false
}
