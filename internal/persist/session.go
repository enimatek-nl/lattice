package persist

import (
	"time"

	"gorm.io/gorm"
)

// Session is an active access token. Deleting a session revokes the JWT that
// was minted for it because protected routes check the token against this
// table.
type Session struct {
	Token            string `json:"-" gorm:"uniqueIndex;size:255"`
	RefreshToken     string `json:"-" gorm:"index;size:255"`
	UserID           string `json:"user_id" gorm:"index;size:255"`
	DeviceID         string `json:"device_id" gorm:"size:64"`
	NextBatchToken   string `json:"next_batch_token" gorm:"size:64"`
	ValidUntilTS     int64  `json:"valid_until_ts"`
	SoftLogout       bool   `json:"soft_logout"`
	LastRefreshTS    int64  `json:"last_refresh_ts"`
	RefreshExpiredTS int64  `json:"refresh_expired_ts"`

	gorm.Model
}

// SessionRepo is the contract for access token storage access.
type SessionRepo interface {
	Create(session *Session) error
	GetByToken(token string) (*Session, error)
	GetByRefreshToken(token string) (*Session, error)
	Save(session *Session) error
	DeleteByToken(token string) error
	DeleteByUser(userID string) error
	DeleteByDevice(userID, deviceID string) error
	DeleteExceptDevice(userID, keepDeviceID string) error
	ListByUser(userID string) ([]*Session, error)
	ActiveFor(userID, deviceID string) (*Session, error)
}

// SessionRepoImpl is the GORM backed SessionRepo.
type SessionRepoImpl struct {
	db *gorm.DB
}

// Create implements [SessionRepo].
func (r *SessionRepoImpl) Create(session *Session) error {
	return r.db.Create(session).Error
}

// GetByToken implements [SessionRepo].
func (r *SessionRepoImpl) GetByToken(token string) (*Session, error) {
	var session Session
	err := r.db.Where("token = ?", token).First(&session).Error
	return &session, err
}

// GetByRefreshToken implements [SessionRepo].
func (r *SessionRepoImpl) GetByRefreshToken(token string) (*Session, error) {
	var session Session
	err := r.db.Where("refresh_token = ?", token).First(&session).Error
	return &session, err
}

// Save implements [SessionRepo].
func (r *SessionRepoImpl) Save(session *Session) error {
	return r.db.Save(session).Error
}

// DeleteByToken implements [SessionRepo]. Sessions are removed for real: a
// soft delete would keep the row around under the unique token index.
func (r *SessionRepoImpl) DeleteByToken(token string) error {
	return r.db.Unscoped().Where("token = ?", token).Delete(&Session{}).Error
}

// DeleteByUser implements [SessionRepo].
func (r *SessionRepoImpl) DeleteByUser(userID string) error {
	return r.db.Unscoped().Where("user_id = ?", userID).Delete(&Session{}).Error
}

// DeleteByDevice implements [SessionRepo].
func (r *SessionRepoImpl) DeleteByDevice(userID, deviceID string) error {
	return r.db.Unscoped().Where("user_id = ? AND device_id = ?", userID, deviceID).
		Delete(&Session{}).
		Error
}

// DeleteExceptDevice implements [SessionRepo]. An empty keepDeviceID deletes
// every session of the user.
func (r *SessionRepoImpl) DeleteExceptDevice(userID, keepDeviceID string) error {
	q := r.db.Unscoped().Where("user_id = ?", userID)
	if keepDeviceID != "" {
		q = q.Where("device_id <> ?", keepDeviceID)
	}
	return q.Delete(&Session{}).Error
}

// ListByUser implements [SessionRepo].
func (r *SessionRepoImpl) ListByUser(userID string) ([]*Session, error) {
	var sessions []*Session
	err := r.db.Where("user_id = ?", userID).Find(&sessions).Error
	return sessions, err
}

// ActiveFor implements [SessionRepo].
func (r *SessionRepoImpl) ActiveFor(userID, deviceID string) (*Session, error) {
	var session Session
	err := r.db.Where("user_id = ? AND device_id = ?", userID, deviceID).
		First(&session).Error
	return &session, err
}

// NewSessionRepo returns the SessionRepo implementation for db.
func NewSessionRepo(db *gorm.DB) SessionRepo {
	return &SessionRepoImpl{db: db}
}

// SessionTTL is how long an access token stays valid by default.
const SessionTTL = 24 * time.Hour
