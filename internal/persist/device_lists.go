package persist

import (
	"context"

	"gorm.io/gorm"
)

// DeviceListChange records that somebody's device list is worth re-reading, and why the
// server knows that.
//
// `/sync` reports these as `device_lists.changed` and `device_lists.left`: lists of user
// IDs. A client that sees one goes and asks for that user's keys again. The room is stored
// with the change because the room is what says who is allowed to hear about it: a change
// that happened in a room this user is not in is not theirs to see — and the member list of
// a room is no use for the question, because the user who left is no longer in it.
type DeviceListChange struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement"`
	RoomID string `gorm:"column:room_id;type:text;not null"`
	UserID string `gorm:"column:user_id;type:text;not null"`
	// Kind is one of the two sections a sync response has for these.
	Kind           string `gorm:"column:kind;type:text;not null"`
	StreamOrdering int64  `gorm:"column:stream_ordering;not null;default:0"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (DeviceListChange) TableName() string { return "device_list_changes" }

// The two sections of the sync response, named as the specification names them.
const (
	DeviceListChanged = "changed"
	DeviceListLeft    = "left"
)

// DeviceListRepo is the store contract for device list changes.
type DeviceListRepo interface {
	// WithTx returns the repository working inside an open transaction.
	WithTx(tx *gorm.DB) DeviceListRepo
	// Record notes a change and returns the stream position it took. It does not
	// deduplicate: two joins are two reasons to re-read a key list, and a client that
	// syncs between them is entitled to be told about both.
	Record(ctx context.Context, roomID, userID, kind string) (int64, error)
	// ListInRooms returns the changes in the given rooms whose position is in
	// (since, until], oldest first.
	ListInRooms(ctx context.Context, roomIDs []string, since, until int64) ([]*DeviceListChange, error)
}

// NewDeviceListRepo returns the device list repository bound to db.
func NewDeviceListRepo(db *gorm.DB) DeviceListRepo { return &deviceListRepo{db: db} }

type deviceListRepo struct{ db *gorm.DB }

// WithTx implements [DeviceListRepo].
func (r *deviceListRepo) WithTx(tx *gorm.DB) DeviceListRepo { return &deviceListRepo{db: tx} }

// Record implements [DeviceListRepo].
func (r *deviceListRepo) Record(ctx context.Context, roomID, userID, kind string) (int64, error) {
	next, err := nextStreamPosition(ctx, r.db)
	if err != nil {
		return 0, err
	}
	if err := r.db.WithContext(ctx).Create(&DeviceListChange{
		RoomID:         roomID,
		UserID:         userID,
		Kind:           kind,
		StreamOrdering: next,
	}).Error; err != nil {
		return 0, err
	}
	return next, nil
}

// ListInRooms implements [DeviceListRepo].
func (r *deviceListRepo) ListInRooms(ctx context.Context, roomIDs []string, since, until int64) ([]*DeviceListChange, error) {
	if len(roomIDs) == 0 {
		return nil, nil
	}
	var rows []*DeviceListChange
	err := r.db.WithContext(ctx).
		Where("room_id IN ? AND stream_ordering > ? AND stream_ordering <= ?", roomIDs, since, until).
		Order("stream_ordering ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
