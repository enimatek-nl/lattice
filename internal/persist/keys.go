package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeviceKey stores the identity key material a device uploaded through
// POST /keys/upload. The homeserver keeps it as opaque JSON: the signatures are
// verified by the clients that read the keys, which is what lets Lattice serve
// the end-to-end-encryption endpoints without doing any cryptography itself.
type DeviceKey struct {
	UserID   string `json:"user_id" gorm:"uniqueIndex:uq_devicekey_user_device;size:255;index:idx_devicekey_user"`
	DeviceID string `json:"device_id" gorm:"uniqueIndex:uq_devicekey_user_device;size:64"`
	// KeyJSON is the device_key object exactly as the client sent it.
	KeyJSON   string `json:"-" gorm:"type:text"`
	StreamPos int64  `json:"stream_pos" gorm:"index"`

	gorm.Model
}

// DeviceKeyRepo is the contract for device identity key storage access.
type DeviceKeyRepo interface {
	// Store inserts or replaces the identity key of a device.
	Store(key *DeviceKey) error
	Get(userID, deviceID string) (*DeviceKey, error)
	// List returns the keys of the given users (every device of every user when
	// no user is named).
	List(userIDs ...string) ([]*DeviceKey, error)
	// Delete removes the keys of the given devices of one user.
	Delete(userID string, deviceIDs ...string) error
	// Count reports how many devices of a user have published keys.
	Count(userID string) (int64, error)
}

// DeviceKeyRepoImpl is the GORM backed DeviceKeyRepo.
type DeviceKeyRepoImpl struct {
	db *gorm.DB
}

// Store implements [DeviceKeyRepo]. A device re-uploads its identity key when
// it restarts, so the row is replaced rather than duplicated.
func (r *DeviceKeyRepoImpl) Store(key *DeviceKey) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "device_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"key_json", "stream_pos", "updated_at"}),
	}).Create(key).Error
}

// Get implements [DeviceKeyRepo].
func (r *DeviceKeyRepoImpl) Get(userID, deviceID string) (*DeviceKey, error) {
	var key DeviceKey
	err := r.db.Where("user_id = ? AND device_id = ?", userID, deviceID).First(&key).Error
	return &key, err
}

// List implements [DeviceKeyRepo].
func (r *DeviceKeyRepoImpl) List(userIDs ...string) ([]*DeviceKey, error) {
	var keys []*DeviceKey
	q := r.db.Model(&DeviceKey{})
	if len(userIDs) > 0 {
		q = q.Where("user_id IN ?", userIDs)
	}
	err := q.Order("id asc").Find(&keys).Error
	return keys, err
}

// Delete implements [DeviceKeyRepo]. Keys go away with their device, so a
// deleted device cannot be talked to any more.
func (r *DeviceKeyRepoImpl) Delete(userID string, deviceIDs ...string) error {
	q := r.db.Unscoped().Where("user_id = ?", userID)
	if len(deviceIDs) > 0 {
		q = q.Where("device_id IN ?", deviceIDs)
	}
	return q.Delete(&DeviceKey{}).Error
}

// Count implements [DeviceKeyRepo].
func (r *DeviceKeyRepoImpl) Count(userID string) (int64, error) {
	var n int64
	err := r.db.Model(&DeviceKey{}).Where("user_id = ?", userID).Count(&n).Error
	return n, err
}

// OneTimeKey is a prekey a client uploaded so other devices can start an
// end-to-end session with it. Like the identity keys these are opaque JSON
// blobs; the server only hands them out once.
type OneTimeKey struct {
	UserID    string `json:"user_id" gorm:"uniqueIndex:uq_otk_user_device_key;size:255;index:idx_otk_user_device"`
	DeviceID  string `json:"device_id" gorm:"uniqueIndex:uq_otk_user_device_key;size:64"`
	KeyID     string `json:"key_id" gorm:"uniqueIndex:uq_otk_user_device_key;size:128"`
	Algorithm string `json:"algorithm" gorm:"size:64"`
	// Fallback keys (MSC3288) are not consumed: a device keeps handing out its
	// last fallback key until the client replaces it.
	Fallback bool `json:"fallback"`
	// KeyJSON is the key exactly as the client sent it, key id included.
	KeyJSON string `json:"-" gorm:"type:text"`
	// StoredTS is when the key was uploaded, which drives the retention window.
	StoredTS int64 `json:"stored_ts" gorm:"index"`
	// ClaimedTS is zero while a key is still available.
	ClaimedTS int64 `json:"claimed_ts" gorm:"index"`

	gorm.Model
}

// OneTimeKeyRepo is the contract for one-time key storage access.
type OneTimeKeyRepo interface {
	// Replace drops the unclaimed keys of a device (for the given fallback
	// class) and stores the new batch. Claimed keys are kept until they fall
	// out of the retention window so a claim can never be replayed.
	Replace(userID, deviceID string, fallback bool, keys []*OneTimeKey) error
	// Counts returns how many available keys a device has per algorithm. Fallback
	// keys are excluded: they are never consumed, so counting them would keep a
	// device permanently "full". Clients learn about them from the unused
	// fallback key types of a sync.
	Counts(userID, deviceID string) (map[string]int64, error)
	// Available lists up to limit available keys of one device and algorithm.
	Available(userID, deviceID, algorithm string, fallback bool, limit int) ([]*OneTimeKey, error)
	// Claim marks a specific key as taken and reports whether it was still
	// available, which is how two concurrent claims cannot receive the same key.
	Claim(id uint, nowTS int64) (bool, error)
	// Expire deletes keys uploaded before the given timestamp and returns how
	// many rows went away. Claimed keys live in the same window, so a replayed
	// claim can never hand out the same key twice.
	Expire(beforeTS int64) (int64, error)
	// Delete removes the keys of the given devices of one user.
	Delete(userID string, deviceIDs ...string) error
	// Count reports how many keys a device still has available.
	Count(userID, deviceID string) (int64, error)
}

// OneTimeKeyRepoImpl is the GORM backed OneTimeKeyRepo.
type OneTimeKeyRepoImpl struct {
	db *gorm.DB
}

// Replace implements [OneTimeKeyRepo].
func (r *OneTimeKeyRepoImpl) Replace(userID, deviceID string, fallback bool, keys []*OneTimeKey) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("user_id = ? AND device_id = ? AND fallback = ? AND claimed_ts = 0",
			userID, deviceID, fallback).Delete(&OneTimeKey{}).Error; err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "device_id"}, {Name: "key_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"algorithm", "fallback", "key_json", "claimed_ts", "updated_at"}),
		}).Create(&keys).Error
	})
}

// Counts implements [OneTimeKeyRepo].
func (r *OneTimeKeyRepoImpl) Counts(userID, deviceID string) (map[string]int64, error) {
	type row struct {
		Algorithm string
		Total     int64
	}
	var rows []row
	err := r.db.Model(&OneTimeKey{}).
		Select("algorithm, count(*) as total").
		Where("user_id = ? AND device_id = ? AND claimed_ts = 0 AND fallback = ?", userID, deviceID, false).
		Group("algorithm").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, entry := range rows {
		counts[entry.Algorithm] = entry.Total
	}
	return counts, nil
}

// Available implements [OneTimeKeyRepo]. Keys are handed out oldest first, like
// Synapse does, so a client that re-uploads batches cannot starve a claimer.
func (r *OneTimeKeyRepoImpl) Available(userID, deviceID, algorithm string, fallback bool, limit int) ([]*OneTimeKey, error) {
	if limit <= 0 {
		limit = 1
	}
	var keys []*OneTimeKey
	err := r.db.Where("user_id = ? AND device_id = ? AND algorithm = ? AND fallback = ? AND claimed_ts = 0",
		userID, deviceID, algorithm, fallback).
		Order("id asc").Limit(limit).Find(&keys).Error
	return keys, err
}

// Claim implements [OneTimeKeyRepo]. The update is conditional on the key still
// being free, so the first claimer wins even when two devices ask at once.
func (r *OneTimeKeyRepoImpl) Claim(id uint, nowTS int64) (bool, error) {
	result := r.db.Model(&OneTimeKey{}).Where("id = ? AND claimed_ts = 0", id).
		UpdateColumn("claimed_ts", nowTS)
	return result.RowsAffected == 1, result.Error
}

// Expire implements [OneTimeKeyRepo].
func (r *OneTimeKeyRepoImpl) Expire(beforeTS int64) (int64, error) {
	result := r.db.Unscoped().Where("stored_ts < ?", beforeTS).Delete(&OneTimeKey{})
	return result.RowsAffected, result.Error
}

// Delete implements [OneTimeKeyRepo].
func (r *OneTimeKeyRepoImpl) Delete(userID string, deviceIDs ...string) error {
	q := r.db.Unscoped().Where("user_id = ?", userID)
	if len(deviceIDs) > 0 {
		q = q.Where("device_id IN ?", deviceIDs)
	}
	return q.Delete(&OneTimeKey{}).Error
}

// Count implements [OneTimeKeyRepo].
func (r *OneTimeKeyRepoImpl) Count(userID, deviceID string) (int64, error) {
	var n int64
	err := r.db.Model(&OneTimeKey{}).
		Where("user_id = ? AND device_id = ? AND claimed_ts = 0", userID, deviceID).
		Count(&n).Error
	return n, err
}

// DeviceListChange records that the devices of a user changed at a position in
// the event stream. GET /keys/changes and the device_lists section of /sync read
// this log; without it a server cannot say which devices came and went between
// two sync tokens.
type DeviceListChange struct {
	UserID    string `json:"user_id" gorm:"index:idx_devicelist_user_pos;size:255"`
	DeviceID  string `json:"device_id" gorm:"size:64"`
	Kind      string `json:"kind" gorm:"size:16"`
	StreamPos int64  `json:"stream_pos" gorm:"index:idx_devicelist_pos"`
	TS        int64  `json:"ts" gorm:"index"`

	gorm.Model
}

// Device list change kinds.
const (
	DeviceListChanged = "change"
	DeviceListLeft    = "leave"
)

// DeviceListRepo is the contract for device list change storage access.
type DeviceListRepo interface {
	// Append records one change.
	Append(change *DeviceListChange) error
	// Since lists the changes after fromPos up to and including toPos, oldest
	// first, capped at limit rows.
	Since(fromPos, toPos int64, limit int) ([]*DeviceListChange, error)
	// Expire deletes changes recorded before the given timestamp and returns how
	// many rows went away.
	Expire(beforeTS int64) (int64, error)
	// Count reports how many changes are still recorded.
	Count() (int64, error)
}

// DeviceListRepoImpl is the GORM backed DeviceListRepo.
type DeviceListRepoImpl struct {
	db *gorm.DB
}

// Append implements [DeviceListRepo].
func (r *DeviceListRepoImpl) Append(change *DeviceListChange) error {
	return r.db.Create(change).Error
}

// Since implements [DeviceListRepo].
func (r *DeviceListRepoImpl) Since(fromPos, toPos int64, limit int) ([]*DeviceListChange, error) {
	if limit <= 0 {
		limit = 100
	}
	var changes []*DeviceListChange
	err := r.db.Where("stream_pos > ? AND stream_pos <= ?", fromPos, toPos).
		Order("id asc").Limit(limit).Find(&changes).Error
	return changes, err
}

// Expire implements [DeviceListRepo].
func (r *DeviceListRepoImpl) Expire(beforeTS int64) (int64, error) {
	result := r.db.Unscoped().Where("ts < ?", beforeTS).Delete(&DeviceListChange{})
	return result.RowsAffected, result.Error
}

// Count implements [DeviceListRepo].
func (r *DeviceListRepoImpl) Count() (int64, error) {
	var n int64
	err := r.db.Model(&DeviceListChange{}).Count(&n).Error
	return n, err
}

// NewDeviceKeyRepo returns the interface type.
func NewDeviceKeyRepo(db *gorm.DB) DeviceKeyRepo {
	return &DeviceKeyRepoImpl{db: db}
}

// NewOneTimeKeyRepo returns the interface type.
func NewOneTimeKeyRepo(db *gorm.DB) OneTimeKeyRepo {
	return &OneTimeKeyRepoImpl{db: db}
}

// NewDeviceListRepo returns the interface type.
func NewDeviceListRepo(db *gorm.DB) DeviceListRepo {
	return &DeviceListRepoImpl{db: db}
}
