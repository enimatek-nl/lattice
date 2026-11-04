package persist

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SlidingSyncConnection is one client's sliding sync loop.
//
// A sliding sync connection is not the user and not the device: it is a single long-running
// sequence of requests, each carrying the position the previous one answered with. The
// position alone cannot answer "what has this client been told", so the server keeps the
// answer here - and keeps it per connection, because the same device may run two loops that
// have been shown different rooms.
type SlidingSyncConnection struct {
	UserID   string `json:"user_id" gorm:"uniqueIndex:uq_slidingsync_conn;size:255"`
	DeviceID string `json:"device_id" gorm:"uniqueIndex:uq_slidingsync_conn;size:64"`
	// ConnID is the client's own name for the loop; the empty string is a legal one.
	ConnID string `json:"conn_id" gorm:"uniqueIndex:uq_slidingsync_conn;size:128"`
	// LastPos is the stream position this connection has been answered up to. A request
	// that names any other position is not this connection speaking.
	LastPos uint64 `json:"last_pos" gorm:"index"`
	// ListCount and SubCount remember how much the client last asked to be tracked, which
	// is what an expiry decision is made against.
	ListCount int `json:"list_count"`
	SubCount  int `json:"sub_count"`

	gorm.Model
}

// SlidingSyncRoom is what one connection has been told about one room.
//
// This is the table that makes a sliding sync response a delta: a room is reported when it has
// not been sent before, or when something in it moved past the position recorded here. The
// timeline window and the state set are remembered per connection because they are what the
// client has - a wider window asked for later is only "new" relative to this one.
type SlidingSyncRoom struct {
	// ConnKey is the id of the [SlidingSyncConnection] row this row belongs to.
	ConnKey uint   `json:"conn_key" gorm:"uniqueIndex:uq_slidingsync_room;index"`
	RoomID  string `json:"room_id" gorm:"uniqueIndex:uq_slidingsync_room;size:255"`
	// Sent marks a room that has been sent to this connection at least once. A room the
	// user left is part of their list only if it has been: the client knows it left, and
	// resending it costs a room's worth of state for a fact the client already has.
	Sent bool `json:"sent"`
	// TimelinePos is the highest stream position of a timeline event this connection has
	// been given, and TimelineCount how many of the room's recent events it holds.
	TimelinePos   uint64 `json:"timeline_pos"`
	TimelineCount int    `json:"timeline_count"`
	// Config is the room config the connection was last shown the room under, in the
	// canonical form [RoomConfigKey] writes. A wider config asked for later is answered
	// with the wider window, not with silence.
	Config string `json:"config" gorm:"type:text"`
	// State lists the state keys already sent, one "type state_key" pair per line, so that
	// state which has gone away can be reported as a stub instead of vanishing, and
	// StatePos is the stream position the state of this room was sent as of - a state row
	// written after it is a change this connection has not been told about.
	State    string `json:"state" gorm:"type:text"`
	StatePos uint64 `json:"state_pos"`
	// BumpStamp is the activity value this connection was last shown, so that an unchanged
	// one is left out of the response rather than repeated.
	BumpStamp uint64 `json:"bump_stamp"`
	// The counts and the direct flag are remembered because the response leaves an
	// unchanged field out, and leaving a field out has to be justified.
	JoinedCount  int  `json:"joined_count"`
	InvitedCount int  `json:"invited_count"`
	IsDM         bool `json:"is_dm"`

	gorm.Model
}

// SlidingSyncRepo is the contract for sliding sync connection storage access.
type SlidingSyncRepo interface {
	// Connection returns the connection of a device under a connection id, or a not found
	// error. The lookup is what enforces that a position is only honoured for the user and
	// device it was issued to.
	Connection(userID, deviceID, connID string) (*SlidingSyncConnection, error)
	// SaveConnection inserts or updates a connection row, and fills in its id.
	SaveConnection(conn *SlidingSyncConnection) error
	// Forget drops a connection and everything recorded against it.
	Forget(connKey uint) error
	// Connections lists a device's connections, oldest recently used first, which is the
	// order they are pruned in.
	ConnectionsForDevice(userID, deviceID string, except uint) ([]*SlidingSyncConnection, error)
	// Rooms lists everything a connection has been told about rooms.
	Rooms(connKey uint) ([]*SlidingSyncRoom, error)
	// SaveRooms writes the room rows of one connection, replacing any earlier row of the
	// same room.
	SaveRooms(rows []*SlidingSyncRoom) error
	// UnsentRooms lists the rooms a connection has been told nothing about yet.
	ResetRooms(connKey uint) error
	// TrimRooms drops the room rows of a connection for rooms outside the given set, so
	// that a connection does not accumulate a row per room it will never see again.
	TrimRooms(connKey uint, keep []string) error
	// ConnectionsOlderThan lists connections whose last request predates a moment, which
	// is how a server frees the state of loops that have stopped.
	ConnectionsOlderThan(before time.Time, limit int) ([]*SlidingSyncConnection, error)
}

// SlidingSyncRepoImpl is the GORM backed [SlidingSyncRepo].
type SlidingSyncRepoImpl struct {
	db *gorm.DB
}

// Connection implements [SlidingSyncRepo].
func (r *SlidingSyncRepoImpl) Connection(userID, deviceID, connID string) (*SlidingSyncConnection, error) {
	var conn SlidingSyncConnection
	err := r.db.Where("user_id = ? AND device_id = ? AND conn_id = ?", userID, deviceID, connID).
		First(&conn).Error
	return &conn, err
}

// SaveConnection implements [SlidingSyncRepo].
func (r *SlidingSyncRepoImpl) SaveConnection(conn *SlidingSyncConnection) error {
	if conn.ID == 0 {
		return r.db.Create(conn).Error
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "device_id"}, {Name: "conn_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_pos", "list_count", "sub_count", "updated_at"}),
	}).Save(conn).Error
}

// Forget implements [SlidingSyncRepo].
func (r *SlidingSyncRepoImpl) Forget(connKey uint) error {
	if err := r.db.Where("conn_key = ?", connKey).Delete(&SlidingSyncRoom{}).Error; err != nil {
		return err
	}
	return r.db.Delete(&SlidingSyncConnection{}, connKey).Error
}

// ConnectionsForDevice implements [SlidingSyncRepo].
func (r *SlidingSyncRepoImpl) ConnectionsForDevice(userID, deviceID string, except uint) ([]*SlidingSyncConnection, error) {
	var rows []*SlidingSyncConnection
	err := r.db.Where("user_id = ? AND device_id = ? AND id <> ?", userID, deviceID, except).
		Order("updated_at asc").Find(&rows).Error
	return rows, err
}

// Rooms implements [SlidingSyncRepo].
func (r *SlidingSyncRepoImpl) Rooms(connKey uint) ([]*SlidingSyncRoom, error) {
	var rows []*SlidingSyncRoom
	err := r.db.Where("conn_key = ?", connKey).Find(&rows).Error
	return rows, err
}

// SaveRooms implements [SlidingSyncRepo].
func (r *SlidingSyncRepoImpl) SaveRooms(rows []*SlidingSyncRoom) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "conn_key"}, {Name: "room_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"sent", "timeline_pos", "timeline_count", "config", "state", "state_pos", "bump_stamp",
			"joined_count", "invited_count", "is_dm", "updated_at",
		}),
	}).CreateInBatches(rows, 200).Error
}

// ResetRooms implements [SlidingSyncRepo].
func (r *SlidingSyncRepoImpl) ResetRooms(connKey uint) error {
	return r.db.Where("conn_key = ?", connKey).Delete(&SlidingSyncRoom{}).Error
}

// TrimRooms implements [SlidingSyncRepo].
func (r *SlidingSyncRepoImpl) TrimRooms(connKey uint, keep []string) error {
	query := r.db.Where("conn_key = ?", connKey)
	if len(keep) > 0 {
		query = query.Where("room_id NOT IN ?", keep)
	}
	return query.Delete(&SlidingSyncRoom{}).Error
}

// ConnectionsOlderThan implements [SlidingSyncRepo].
func (r *SlidingSyncRepoImpl) ConnectionsOlderThan(before time.Time, limit int) ([]*SlidingSyncConnection, error) {
	var rows []*SlidingSyncConnection
	err := r.db.Where("updated_at < ?", before).Order("updated_at asc").Limit(limit).Find(&rows).Error
	return rows, err
}

// NewSlidingSyncRepo returns the [SlidingSyncRepo] implementation for db.
func NewSlidingSyncRepo(db *gorm.DB) SlidingSyncRepo {
	return &SlidingSyncRepoImpl{db: db}
}
