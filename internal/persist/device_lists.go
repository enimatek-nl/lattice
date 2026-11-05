package persist

import (
	"context"

	"gorm.io/gorm"
)

// DeviceListChange says to one user that another user's device list is worth re-reading, or
// that it is no longer worth keeping.
//
// The observer is named because "who may hear about this" is a question about room
// membership at the moment the change happened, and it cannot be worked out afterwards. A
// row that only named the room would be unreadable by exactly the person who most needs it:
// somebody who has just left a room is told that the people still in it are no longer theirs
// to track, and they are no longer in that room to read a message left there.
//
// The room is still recorded, not for the read but for the explanation: it says why the
// change happened, which is what makes a log line about a device list change worth reading.
type DeviceListChange struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`
	// ObserverID is the user being told. A sync reads the rows that name it.
	ObserverID string `gorm:"column:observer_id;type:text;not null;index:idx_device_list_changes_observer_stream,priority:1"`
	// UserID is the user whose keys are being talked about.
	UserID         string `gorm:"column:user_id;type:text;not null"`
	RoomID         string `gorm:"column:room_id;type:text;not null"`
	Kind           string `gorm:"column:kind;type:text;not null"`
	StreamOrdering int64  `gorm:"column:stream_ordering;not null;default:0;index:idx_device_list_changes_observer_stream,priority:2"`
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
	// Record notes a change for one observer and returns the stream position it took. It
	// does not deduplicate: two joins are two reasons to re-read a key list, and a client
	// that syncs between them is entitled to be told about both.
	Record(ctx context.Context, roomID, observerID, userID, kind string) (int64, error)
	// ListFor returns the changes an observer has not heard yet, in (since, until], oldest
	// first.
	ListFor(ctx context.Context, observerID string, since, until int64) ([]*DeviceListChange, error)
}

// NewDeviceListRepo returns the device list repository bound to db.
func NewDeviceListRepo(db *gorm.DB) DeviceListRepo { return &deviceListRepo{db: db} }

type deviceListRepo struct{ db *gorm.DB }

// WithTx implements [DeviceListRepo].
func (r *deviceListRepo) WithTx(tx *gorm.DB) DeviceListRepo { return &deviceListRepo{db: tx} }

// Record implements [DeviceListRepo].
func (r *deviceListRepo) Record(ctx context.Context, roomID, observerID, userID, kind string) (int64, error) {
	if observerID == "" || userID == "" {
		return 0, nil
	}
	next, err := nextStreamPosition(ctx, r.db)
	if err != nil {
		return 0, err
	}
	if err := r.db.WithContext(ctx).Create(&DeviceListChange{
		ObserverID:     observerID,
		UserID:         userID,
		RoomID:         roomID,
		Kind:           kind,
		StreamOrdering: next,
	}).Error; err != nil {
		return 0, err
	}
	return next, nil
}

// ListFor implements [DeviceListRepo].
func (r *deviceListRepo) ListFor(ctx context.Context, observerID string, since, until int64) ([]*DeviceListChange, error) {
	if observerID == "" {
		return nil, nil
	}
	var rows []*DeviceListChange
	err := r.db.WithContext(ctx).
		Where("observer_id = ? AND stream_ordering > ? AND stream_ordering <= ?", observerID, since, until).
		Order("stream_ordering ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
