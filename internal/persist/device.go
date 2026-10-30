package persist

import "gorm.io/gorm"

// Device is a client session belonging to a user. Every access token is bound
// to a device.
type Device struct {
	// DeviceID is opaque and only unique per user.
	DeviceID string `json:"device_id" gorm:"uniqueIndex:uq_device_user_id;size:64"`
	UserID   string `json:"user_id" gorm:"uniqueIndex:uq_device_user_id;size:255"`

	DisplayName  string `json:"display_name" gorm:"size:255"`
	LastActiveTS int64  `json:"last_active_ts"`
	LastActiveIP string `json:"last_active_ip" gorm:"size:64"`

	gorm.Model
}

// DeviceRepo is the contract for device storage access.
type DeviceRepo interface {
	Create(device *Device) error
	Get(userID, deviceID string) (*Device, error)
	List(userID string) ([]*Device, error)
	Save(device *Device) error
	Delete(userID, deviceID string) error
	DeleteAll(userID string, exceptDeviceID string) error
	Count(userID string) (int64, error)
}

// DeviceRepoImpl is the GORM backed DeviceRepo.
type DeviceRepoImpl struct {
	db *gorm.DB
}

// Create implements [DeviceRepo].
func (r *DeviceRepoImpl) Create(device *Device) error {
	return r.db.Create(device).Error
}

// Get implements [DeviceRepo].
func (r *DeviceRepoImpl) Get(userID, deviceID string) (*Device, error) {
	var device Device
	err := r.db.Where("user_id = ? AND device_id = ?", userID, deviceID).
		First(&device).Error
	return &device, err
}

// List implements [DeviceRepo].
func (r *DeviceRepoImpl) List(userID string) ([]*Device, error) {
	var devices []*Device
	err := r.db.Where("user_id = ?", userID).Order("id asc").Find(&devices).Error
	return devices, err
}

// Save implements [DeviceRepo].
func (r *DeviceRepoImpl) Save(device *Device) error {
	return r.db.Save(device).Error
}

// Delete implements [DeviceRepo]. Rows are dropped for real, so a later login
// can claim the same device id again.
func (r *DeviceRepoImpl) Delete(userID, deviceID string) error {
	return r.db.Unscoped().Where("user_id = ? AND device_id = ?", userID, deviceID).
		Delete(&Device{}).
		Error
}

// DeleteAll implements [DeviceRepo]. An empty exceptDeviceID deletes all
// devices of the user.
func (r *DeviceRepoImpl) DeleteAll(userID string, exceptDeviceID string) error {
	q := r.db.Unscoped().Where("user_id = ?", userID)
	if exceptDeviceID != "" {
		q = q.Where("device_id <> ?", exceptDeviceID)
	}
	return q.Delete(&Device{}).Error
}

// Count implements [DeviceRepo].
func (r *DeviceRepoImpl) Count(userID string) (int64, error) {
	var n int64
	err := r.db.Model(&Device{}).Where("user_id = ?", userID).Count(&n).Error
	return n, err
}

// NewDeviceRepo returns the DeviceRepo implementation for db.
func NewDeviceRepo(db *gorm.DB) DeviceRepo {
	return &DeviceRepoImpl{db: db}
}
