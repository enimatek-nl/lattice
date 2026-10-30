package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Presence is the last known presence state of a user.
type Presence struct {
	UserID          string `json:"user_id" gorm:"uniqueIndex;size:255"`
	Presence        string `json:"presence" gorm:"size:16"`
	StatusMsg       string `json:"status_msg" gorm:"size:255"`
	LastActiveAgo   int64  `json:"last_active_ago"`
	CurrentlyActive bool   `json:"currently_active"`
	SentTS          int64  `json:"-"`

	gorm.Model
}

// PresenceRepo is the contract for presence storage access.
type PresenceRepo interface {
	Set(presence *Presence) error
	Get(userID string) (*Presence, error)
}

// PresenceRepoImpl is the GORM backed PresenceRepo.
type PresenceRepoImpl struct {
	db *gorm.DB
}

// Set implements [PresenceRepo]. A record for the user is replaced outright, so
// presence updates never accumulate.
func (r *PresenceRepoImpl) Set(presence *Presence) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"presence", "status_msg", "last_active_ago", "currently_active", "sent_ts", "updated_at",
		}),
	}).Create(presence).Error
}

// Get implements [PresenceRepo].
func (r *PresenceRepoImpl) Get(userID string) (*Presence, error) {
	var presence Presence
	err := r.db.Where("user_id = ?", userID).First(&presence).Error
	return &presence, err
}

// NewPresenceRepo returns the PresenceRepo implementation for db.
func NewPresenceRepo(db *gorm.DB) PresenceRepo {
	return &PresenceRepoImpl{db: db}
}
