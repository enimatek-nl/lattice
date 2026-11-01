package persist

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Key backup: the encrypted room keys a client stores on the server so that a
// new device can read old messages. The session data is encrypted with a key the
// server never sees, so Lattice stores and returns it without looking inside.

// KeyBackupVersion is one backup of one user's room keys. Versions are numbered
// from 1 per user, and a client always writes to and reads from one version.
type KeyBackupVersion struct {
	UserID  string `json:"user_id" gorm:"uniqueIndex:uq_keybackup_version;size:255"`
	Version int64  `json:"version" gorm:"uniqueIndex:uq_keybackup_version"`
	// Algorithm is the backup algorithm, in practice
	// m.megolm_backup.v1.curve25519-aes-sha2.
	Algorithm string `json:"algorithm" gorm:"size:128"`
	// AuthData is the algorithm-specific object the client signs with its master
	// key. It is opaque here: the client that reads the backup checks it.
	AuthData string `json:"auth_data" gorm:"type:text"`

	gorm.Model
}

// BackupRoomKey is one encrypted megolm session of one backup version.
type BackupRoomKey struct {
	UserID    string `json:"user_id" gorm:"uniqueIndex:uq_keybackup_key;size:255"`
	Version   int64  `json:"version" gorm:"uniqueIndex:uq_keybackup_key"`
	RoomID    string `json:"room_id" gorm:"uniqueIndex:uq_keybackup_key;size:255"`
	SessionID string `json:"session_id" gorm:"uniqueIndex:uq_keybackup_key;size:255"`
	// FirstKnownIndex is the index of the first message the session can decrypt,
	// which a client uses to decide which sessions it still needs.
	FirstKnownIndex int64 `json:"first_known_index"`
	// IsVerified says whether the session came from a device whose identity the
	// owner has signed, and ForwardedCount how many times it has been handed to
	// another device. Together with the index they decide whether a newly uploaded
	// session may replace the one on file: see shouldReplaceBackup.
	IsVerified     bool   `json:"is_verified"`
	ForwardedCount int64  `json:"forwarded_count"`
	Data           string `json:"data" gorm:"type:text"`

	gorm.Model
}

// KeyBackupRepo is the contract for server-side key backup storage access.
type KeyBackupRepo interface {
	// LatestVersion returns the highest version of a user, or ErrNoBackup.
	LatestVersion(userID string) (*KeyBackupVersion, error)
	// GetVersion returns one version of a user, or ErrNoBackup.
	GetVersion(userID string, version int64) (*KeyBackupVersion, error)
	// HighestVersionNumber returns the highest stored version number of a user,
	// or 0 when it has none.
	HighestVersionNumber(userID string) (int64, error)
	CreateVersion(version *KeyBackupVersion) error
	UpdateVersion(version *KeyBackupVersion) error
	// DeleteVersion removes a backup and every session stored under it,
	// returning how many sessions went away.
	DeleteVersion(userID string, version int64) (int64, error)
	// DeleteAll removes every backup of a user, as account deactivation asks.
	DeleteAll(userID string) (int64, error)
	// StoreKeys inserts or replaces sessions, returning how many were written.
	StoreKeys(keys []*BackupRoomKey) (int64, error)
	// Keys returns the sessions of a backup. An empty roomIDs list asks for all
	// of them.
	Keys(userID string, version int64, roomIDs []string) ([]*BackupRoomKey, error)
	// GetKey returns one session, or ErrNoBackup.
	GetKey(userID string, version int64, roomID, sessionID string) (*BackupRoomKey, error)
	// CountKeys counts the sessions of a backup, or of one room of it when
	// roomID is not empty.
	CountKeys(userID string, version int64, roomID string) (int64, error)
	// DeleteKeys removes sessions: one session when both room and session are
	// given, one room when only the room is, the whole backup otherwise.
	DeleteKeys(userID string, version int64, roomID, sessionID string) (int64, error)
}

// ErrNoBackup reports that a user has no backup with the requested version.
var ErrNoBackup = errors.New("no such key backup")

// KeyBackupRepoImpl is the GORM backed KeyBackupRepo.
type KeyBackupRepoImpl struct {
	db *gorm.DB
}

// NewKeyBackupRepo builds the repository over an open connection.
func NewKeyBackupRepo(db *gorm.DB) *KeyBackupRepoImpl {
	return &KeyBackupRepoImpl{db: db}
}

// NewKeyBackupRepoTx rebinds the repository to a transaction.
func NewKeyBackupRepoTx(tx *gorm.DB) *KeyBackupRepoImpl {
	return NewKeyBackupRepo(tx)
}

// LatestVersion implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) LatestVersion(userID string) (*KeyBackupVersion, error) {
	var version KeyBackupVersion
	err := r.db.Where("user_id = ?", userID).Order("version desc").First(&version).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoBackup
		}
		return nil, err
	}
	return &version, nil
}

// GetVersion implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) GetVersion(userID string, version int64) (*KeyBackupVersion, error) {
	var row KeyBackupVersion
	if err := r.db.Where("user_id = ? AND version = ?", userID, version).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoBackup
		}
		return nil, err
	}
	return &row, nil
}

// HighestVersionNumber implements [KeyBackupRepo]. The search covers rows that
// an older version of Lattice marked as deleted without removing them, because
// the unique index over (user_id, version) still counts those rows: picking a
// number they already hold fails with a duplicate key.
func (r *KeyBackupRepoImpl) HighestVersionNumber(userID string) (int64, error) {
	var version KeyBackupVersion
	err := r.db.Unscoped().Where("user_id = ?", userID).Order("version desc").First(&version).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return version.Version, nil
}

// CreateVersion implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) CreateVersion(version *KeyBackupVersion) error {
	return r.db.Create(version).Error
}

// UpdateVersion implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) UpdateVersion(version *KeyBackupVersion) error {
	return r.db.Model(&KeyBackupVersion{}).
		Where("user_id = ? AND version = ?", version.UserID, version.Version).
		Updates(map[string]any{"algorithm": version.Algorithm, "auth_data": version.AuthData}).Error
}

// DeleteVersion implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) DeleteVersion(userID string, version int64) (int64, error) {
	// The rows are removed for real. A backup version that is only marked as
	// deleted still occupies its slot in the unique index over (user_id, version),
	// so the next backup would be handed a number that cannot be inserted, and a
	// client recreating its backup would be told the version already exists.
	if err := r.db.Unscoped().Where("user_id = ? AND version = ?", userID, version).
		Delete(&KeyBackupVersion{}).Error; err != nil {
		return 0, err
	}
	result := r.db.Unscoped().Where("user_id = ? AND version = ?", userID, version).Delete(&BackupRoomKey{})
	return result.RowsAffected, result.Error
}

// DeleteAll implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) DeleteAll(userID string) (int64, error) {
	if err := r.db.Unscoped().Where("user_id = ?", userID).Delete(&KeyBackupVersion{}).Error; err != nil {
		return 0, err
	}
	result := r.db.Unscoped().Where("user_id = ?", userID).Delete(&BackupRoomKey{})
	return result.RowsAffected, result.Error
}

// StoreKeys implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) StoreKeys(keys []*BackupRoomKey) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	err := r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "version"},
			{Name: "room_id"}, {Name: "session_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"first_known_index", "is_verified",
			"forwarded_count", "data"}),
	}).CreateInBatches(keys, 200).Error
	if err != nil {
		return 0, err
	}
	return int64(len(keys)), nil
}

// Keys implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) Keys(userID string, version int64, roomIDs []string) ([]*BackupRoomKey, error) {
	query := r.db.Where("user_id = ? AND version = ?", userID, version)
	if len(roomIDs) > 0 {
		query = query.Where("room_id IN ?", roomIDs)
	}
	var rows []*BackupRoomKey
	err := query.Order("room_id asc, session_id asc").Find(&rows).Error
	return rows, err
}

// GetKey implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) GetKey(userID string, version int64, roomID, sessionID string) (*BackupRoomKey, error) {
	var row BackupRoomKey
	err := r.db.Where("user_id = ? AND version = ? AND room_id = ? AND session_id = ?",
		userID, version, roomID, sessionID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoBackup
		}
		return nil, err
	}
	return &row, nil
}

// CountKeys implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) CountKeys(userID string, version int64, roomID string) (int64, error) {
	query := r.db.Model(&BackupRoomKey{}).Where("user_id = ? AND version = ?", userID, version)
	if roomID != "" {
		query = query.Where("room_id = ?", roomID)
	}
	var count int64
	return count, query.Count(&count).Error
}

// DeleteKeys implements [KeyBackupRepo].
func (r *KeyBackupRepoImpl) DeleteKeys(userID string, version int64, roomID, sessionID string) (int64, error) {
	query := r.db.Unscoped().Where("user_id = ? AND version = ?", userID, version)
	if roomID != "" {
		query = query.Where("room_id = ?", roomID)
	}
	if sessionID != "" {
		query = query.Where("session_id = ?", sessionID)
	}
	result := query.Delete(&BackupRoomKey{})
	return result.RowsAffected, result.Error
}
