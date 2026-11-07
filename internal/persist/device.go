package persist

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Device is a client session belonging to a user.
type Device struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	UserID      string `gorm:"column:user_id;size:255;not null"`
	DeviceID    string `gorm:"column:device_id;size:100;not null"`
	DisplayName string `gorm:"column:display_name;size:255"`
	LastSeenIP  string `gorm:"column:last_seen_ip;size:64"`
	LastSeenTS  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// DeviceRepo is the device/session store contract.
type DeviceRepo interface {
	// WithTx returns a repository bound to an open transaction.
	WithTx(tx *gorm.DB) DeviceRepo
	Create(ctx context.Context, device *Device) error
	ByDeviceID(ctx context.Context, userID, deviceID string) (*Device, error)
	ListByUser(ctx context.Context, userID string) ([]Device, error)
	Delete(ctx context.Context, userID, deviceID string) error
	DeleteByUser(ctx context.Context, userID string) error
	SetDisplayName(ctx context.Context, userID, deviceID, displayName string) error
	Touch(ctx context.Context, userID, deviceID, ip string, ts time.Time) error
}

// DeviceRepoImpl is the GORM implementation of [DeviceRepo].
type DeviceRepoImpl struct {
	db *gorm.DB
}

// WithTx implements [DeviceRepo].
func (d *DeviceRepoImpl) WithTx(tx *gorm.DB) DeviceRepo {
	return &DeviceRepoImpl{db: tx}
}

// Create implements [DeviceRepo].
func (d *DeviceRepoImpl) Create(ctx context.Context, device *Device) error {
	if err := d.db.WithContext(ctx).Create(device).Error; err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

// ByDeviceID implements [DeviceRepo].
func (d *DeviceRepoImpl) ByDeviceID(ctx context.Context, userID, deviceID string) (*Device, error) {
	var device Device
	err := d.db.WithContext(ctx).
		Where("user_id = ? AND device_id = ?", userID, deviceID).First(&device).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &device, err
}

// ListByUser implements [DeviceRepo].
func (d *DeviceRepoImpl) ListByUser(ctx context.Context, userID string) ([]Device, error) {
	var devices []Device
	err := d.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC").Find(&devices).Error
	return devices, err
}

// Delete implements [DeviceRepo].
func (d *DeviceRepoImpl) Delete(ctx context.Context, userID, deviceID string) error {
	res := d.db.WithContext(ctx).
		Where("user_id = ? AND device_id = ?", userID, deviceID).Delete(&Device{})
	return res.Error
}

// DeleteByUser implements [DeviceRepo].
func (d *DeviceRepoImpl) DeleteByUser(ctx context.Context, userID string) error {
	return d.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&Device{}).Error
}

// SetDisplayName implements [DeviceRepo].
func (d *DeviceRepoImpl) SetDisplayName(ctx context.Context, userID, deviceID, displayName string) error {
	res := d.db.WithContext(ctx).Model(&Device{}).
		Where("user_id = ? AND device_id = ?", userID, deviceID).
		Update("display_name", displayName)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Touch implements [DeviceRepo].
func (d *DeviceRepoImpl) Touch(ctx context.Context, userID, deviceID, ip string, ts time.Time) error {
	return d.db.WithContext(ctx).Model(&Device{}).
		Where("user_id = ? AND device_id = ?", userID, deviceID).
		Updates(map[string]any{"last_seen_ip": ip, "last_seen_ts": ts}).Error
}

// NewDeviceRepo returns the GORM implementation of [DeviceRepo].
func NewDeviceRepo(db *gorm.DB) DeviceRepo {
	return &DeviceRepoImpl{db: db}
}

// AccessToken records a session token digest. The value clients present is never
// stored: only a digest of it, so a stolen dump cannot be replayed.
type AccessToken struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	TokenHash string `gorm:"column:token_hash;size:64;not null"`
	UserID    string `gorm:"column:user_id;size:255;not null"`
	DeviceID  string `gorm:"column:device_id;size:100;not null"`
	CreatedAt time.Time
}

// TokenRepo is the access token store contract.
type TokenRepo interface {
	// WithTx returns a repository bound to an open transaction.
	WithTx(tx *gorm.DB) TokenRepo
	Create(ctx context.Context, token *AccessToken) error
	ByHash(ctx context.Context, hash string) (*AccessToken, error)
	DeleteByHash(ctx context.Context, hash string) error
	DeleteByDevice(ctx context.Context, userID, deviceID string) error
	DeleteByUser(ctx context.Context, userID string) error
	// DeleteByUserExcept invalidates every token of a user except the named device's. A
	// password change that logs the other sessions out has to leave the one that asked
	// alone: revoking everything and re-minting would change the caller's token under it.
	DeleteByUserExcept(ctx context.Context, userID, keepDeviceID string) error
}

// TokenRepoImpl is the GORM implementation of [TokenRepo].
type TokenRepoImpl struct {
	db *gorm.DB
}

// WithTx implements [TokenRepo].
func (t *TokenRepoImpl) WithTx(tx *gorm.DB) TokenRepo {
	return &TokenRepoImpl{db: tx}
}

// Create implements [TokenRepo].
func (t *TokenRepoImpl) Create(ctx context.Context, token *AccessToken) error {
	if err := t.db.WithContext(ctx).Create(token).Error; err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

// ByHash implements [TokenRepo].
func (t *TokenRepoImpl) ByHash(ctx context.Context, hash string) (*AccessToken, error) {
	var token AccessToken
	err := t.db.WithContext(ctx).Where("token_hash = ?", hash).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &token, err
}

// DeleteByHash implements [TokenRepo].
func (t *TokenRepoImpl) DeleteByHash(ctx context.Context, hash string) error {
	return t.db.WithContext(ctx).Where("token_hash = ?", hash).Delete(&AccessToken{}).Error
}

// DeleteByDevice implements [TokenRepo].
func (t *TokenRepoImpl) DeleteByDevice(ctx context.Context, userID, deviceID string) error {
	return t.db.WithContext(ctx).
		Where("user_id = ? AND device_id = ?", userID, deviceID).
		Delete(&AccessToken{}).Error
}

// DeleteByUserExcept implements [TokenRepo].
func (t *TokenRepoImpl) DeleteByUserExcept(ctx context.Context, userID, keepDeviceID string) error {
	return t.db.WithContext(ctx).Where("user_id = ? AND device_id <> ?", userID, keepDeviceID).
		Delete(&AccessToken{}).Error
}

// DeleteByUser implements [TokenRepo].
func (t *TokenRepoImpl) DeleteByUser(ctx context.Context, userID string) error {
	return t.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&AccessToken{}).Error
}

// NewTokenRepo returns the GORM implementation of [TokenRepo].
func NewTokenRepo(db *gorm.DB) TokenRepo {
	return &TokenRepoImpl{db: db}
}

// isUniqueViolation detects SQLite uniqueness errors in a driver-independent way.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed") && strings.Contains(msg, "unique")
}
