package persist

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Device keys and one-time keys, as a client uploads them.
//
// A device key document is stored exactly as it arrived. It is a signed object: re-writing
// any part of it, however harmless that rewrite looks, makes the signature meaningless. The
// same rule applies to a one-time key.
//
// A one-time key's primary key is its insertion order, and that is not incidental — a key is
// claimed in the order it was uploaded, so a table that reordered them by key ID would hand
// out a session built from keys in an order the client never offered.
type DeviceKeyDoc struct {
	// UserID and DeviceID identify the device. They are taken from the document, which is
	// checked against the authenticated user before anything is written.
	UserID         string `gorm:"column:user_id;type:text;not null;uniqueIndex:idx_device_keys_identity"`
	DeviceID       string `gorm:"column:device_id;type:text;not null;uniqueIndex:idx_device_keys_identity"`
	Document       string `gorm:"column:document;type:text;not null"`
	StreamOrdering int64  `gorm:"column:stream_ordering;not null;default:0"`
	UpdatedAt      int64  `gorm:"column:updated_at;not null;default:0"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (DeviceKeyDoc) TableName() string { return "device_keys" }

// OneTimeKey is one key a client offers for somebody else to start a session with.
type OneTimeKey struct {
	// ID is the insertion order, which is the claim order.
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	UserID     string `gorm:"column:user_id;type:text;not null;uniqueIndex:idx_one_time_keys_identity"`
	DeviceID   string `gorm:"column:device_id;type:text;not null;uniqueIndex:idx_one_time_keys_identity"`
	KeyID      string `gorm:"column:key_id;type:text;not null;uniqueIndex:idx_one_time_keys_identity"`
	Algorithm  string `gorm:"column:algorithm;type:text;not null"`
	Document   string `gorm:"column:document;type:text;not null"`
	Claimed    bool   `gorm:"column:claimed;not null;default:false"`
	UploadedAt int64  `gorm:"column:uploaded_at;not null;default:0"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (OneTimeKey) TableName() string { return "one_time_keys" }

// DeviceKeysRepo is the store contract for both kinds of key.
type DeviceKeysRepo interface {
	// WithTx returns the repository working inside an open transaction.
	WithTx(tx *gorm.DB) DeviceKeysRepo

	// PutDeviceKey stores a device's document, replacing the previous one for the same
	// device: a device has one current set of identity keys, and an update replaces it.
	PutDeviceKey(ctx context.Context, doc *DeviceKeyDoc) error
	// DeviceKeys returns the documents for a user, limited to the named devices when the
	// list is not empty. A non-zero `since` asks for the devices that changed after that
	// stream position, which is what a client holding a token wants.
	DeviceKeys(ctx context.Context, userID string, deviceIDs []string, since int64) ([]*DeviceKeyDoc, error)

	// PutOneTimeKeys stores keys that are not already held, and reports how many were
	// new. Re-offering a key a client already offered is not an error: a client that lost
	// the response to an upload will upload it again.
	PutOneTimeKeys(ctx context.Context, keys []*OneTimeKey) (int, error)
	// UnclaimedOneTimeKeys returns up to `limit` unclaimed keys for one device, oldest
	// upload first, restricted to the named algorithms when that list is not empty.
	UnclaimedOneTimeKeys(ctx context.Context, userID, deviceID string, algorithms []string, limit int) ([]*OneTimeKey, error)
	// MarkClaimed retires keys so that they cannot be handed out twice.
	MarkClaimed(ctx context.Context, ids []uint64) error
	// NextStreamOrdering takes a position from the stream that every part of a sync token
	// is measured against, so that a device key update can be compared with a token a
	// client already holds.
	NextStreamOrdering(ctx context.Context) (int64, error)
	// OneTimeKeyCounts counts the unclaimed keys of one device by algorithm, which is what
	// an upload answers with, and what a client uses to decide whether to top up.
	OneTimeKeyCounts(ctx context.Context, userID, deviceID string) (map[string]int, error)
}

// NewDeviceKeysRepo returns the device key repository bound to db.
func NewDeviceKeysRepo(db *gorm.DB) DeviceKeysRepo { return &deviceKeysRepo{db: db} }

type deviceKeysRepo struct{ db *gorm.DB }

// WithTx implements [DeviceKeysRepo]. Skipping this would send the write to a separate
// connection while a transaction holds the only writer, which deadlocks.
func (r *deviceKeysRepo) WithTx(tx *gorm.DB) DeviceKeysRepo { return &deviceKeysRepo{db: tx} }

// NextStreamOrdering implements [DeviceKeysRepo].
func (r *deviceKeysRepo) NextStreamOrdering(ctx context.Context) (int64, error) {
	return nextStreamPosition(ctx, r.db)
}

// PutDeviceKey implements [DeviceKeysRepo].
func (r *deviceKeysRepo) PutDeviceKey(ctx context.Context, doc *DeviceKeyDoc) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "device_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"document", "stream_ordering", "updated_at"}),
	}).Create(doc).Error
}

// DeviceKeys implements [DeviceKeysRepo].
func (r *deviceKeysRepo) DeviceKeys(ctx context.Context, userID string, deviceIDs []string, since int64) ([]*DeviceKeyDoc, error) {
	query := r.db.WithContext(ctx).Where("user_id = ?", userID)
	if len(deviceIDs) > 0 {
		query = query.Where("device_id IN ?", deviceIDs)
	}
	if since > 0 {
		query = query.Where("stream_ordering > ?", since)
	}
	var docs []*DeviceKeyDoc
	if err := query.Order("device_id ASC").Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

// PutOneTimeKeys implements [DeviceKeysRepo].
func (r *deviceKeysRepo) PutOneTimeKeys(ctx context.Context, keys []*OneTimeKey) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "device_id"}, {Name: "key_id"}},
		DoNothing: true,
	}).Create(&keys)
	if result.Error != nil {
		return 0, result.Error
	}
	return int(result.RowsAffected), nil
}

// UnclaimedOneTimeKeys implements [DeviceKeysRepo].
func (r *deviceKeysRepo) UnclaimedOneTimeKeys(ctx context.Context, userID, deviceID string, algorithms []string, limit int) ([]*OneTimeKey, error) {
	if limit <= 0 {
		return nil, nil
	}
	query := r.db.WithContext(ctx).
		Where("user_id = ? AND device_id = ? AND claimed = ?", userID, deviceID, false)
	if len(algorithms) > 0 {
		query = query.Where("algorithm IN ?", algorithms)
	}
	var keys []*OneTimeKey
	if err := query.Order("id ASC").Limit(limit).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// MarkClaimed implements [DeviceKeysRepo].
func (r *deviceKeysRepo) MarkClaimed(ctx context.Context, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&OneTimeKey{}).
		Where("id IN ?", ids).
		Update("claimed", true).Error
}

// OneTimeKeyCounts implements [DeviceKeysRepo].
func (r *deviceKeysRepo) OneTimeKeyCounts(ctx context.Context, userID, deviceID string) (map[string]int, error) {
	type row struct {
		Algorithm string
		Count     int
	}
	var rows []row
	err := r.db.WithContext(ctx).Model(&OneTimeKey{}).
		Select("algorithm, COUNT(*) AS count").
		Where("user_id = ? AND device_id = ? AND claimed = ?", userID, deviceID, false).
		Group("algorithm").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, entry := range rows {
		counts[entry.Algorithm] = entry.Count
	}
	return counts, nil
}
