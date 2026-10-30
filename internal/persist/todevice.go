package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ToDevice is an event addressed to another user's devices instead of to a
// room. Clients receive these events in the to_device section of a sync; they
// are what end-to-end key exchange travels on.
type ToDevice struct {
	SenderID     string `json:"sender_id" gorm:"uniqueIndex:uq_todevice_txn;size:255"`
	SenderDevice string `json:"sender_device" gorm:"size:255"`
	// TxnID scopes the client transaction of PUT /sendToDevice, which makes a
	// retried request idempotent.
	TxnID string `json:"txn_id" gorm:"uniqueIndex:uq_todevice_txn;size:255"`
	// TargetID is the recipient user, TargetDevice the recipient device or "*"
	// for every device of that user.
	TargetID     string `json:"target_id" gorm:"uniqueIndex:uq_todevice_txn;size:255;index:idx_todevice_target"`
	TargetDevice string `json:"target_device" gorm:"uniqueIndex:uq_todevice_txn;size:255"`
	Type         string `json:"type" gorm:"size:255"`
	Content      string `json:"content"`
	TS           int64  `json:"ts"`
	StreamPos    uint64 `json:"stream_pos" gorm:"index"`

	gorm.Model
}

// ToDeviceRepo is the contract for send-to-device storage access.
type ToDeviceRepo interface {
	// Set stores one addressed message, replacing an earlier row of the same
	// sender, transaction and target.
	Set(message *ToDevice) error
	// Pending lists the messages of a user's device after a stream position.
	Pending(userID, deviceID string, after uint64, limit int) ([]*ToDevice, error)
	// Expire deletes messages sent before the given timestamp and returns how
	// many rows went away. Delivery is positional, so a message only has to
	// survive long enough for a client to pick it up: Lattice keeps the 24 hour
	// window Synapse applies so the table stays bounded.
	Expire(beforeTS int64) (int64, error)
	// Count reports how many to_device messages are still queued.
	Count() (int64, error)
}

// ToDeviceRepoImpl is the GORM backed ToDeviceRepo.
type ToDeviceRepoImpl struct {
	db *gorm.DB
}

// Set implements [ToDeviceRepo].
func (r *ToDeviceRepoImpl) Set(message *ToDevice) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "sender_id"}, {Name: "txn_id"}, {Name: "target_id"}, {Name: "target_device"},
		},
		// A retried transaction carries the same message, so the original
		// stream position is kept: devices that already received it must not
		// see it a second time.
		DoUpdates: clause.AssignmentColumns([]string{"content", "type", "updated_at"}),
	}).Create(message).Error
}

// Pending implements [ToDeviceRepo].
func (r *ToDeviceRepoImpl) Pending(userID, deviceID string, after uint64, limit int) ([]*ToDevice, error) {
	q := r.db.Where("target_id = ? AND (target_device = ? OR target_device = ?) AND stream_pos > ?",
		userID, deviceID, "*", after).Order("stream_pos asc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []*ToDevice
	err := q.Find(&rows).Error
	return rows, err
}

// Expire implements [ToDeviceRepo]. The rows are dropped for real: the models
// carry gorm's soft delete column, but expired to_device traffic has no
// forensic value.
func (r *ToDeviceRepoImpl) Expire(beforeTS int64) (int64, error) {
	result := r.db.Unscoped().Where("ts < ?", beforeTS).Delete(&ToDevice{})
	return result.RowsAffected, result.Error
}

// Count implements [ToDeviceRepo].
func (r *ToDeviceRepoImpl) Count() (int64, error) {
	var n int64
	err := r.db.Model(&ToDevice{}).Count(&n).Error
	return n, err
}

// NewToDeviceRepo returns the ToDeviceRepo implementation for db.
func NewToDeviceRepo(db *gorm.DB) ToDeviceRepo {
	return &ToDeviceRepoImpl{db: db}
}
