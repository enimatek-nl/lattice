package persist

import (
	"strconv"

	"gorm.io/gorm"
)

// Filter is a stored sync filter definition.
type Filter struct {
	UserID   string `json:"user_id" gorm:"uniqueIndex:uq_filter_user_id;size:255"`
	FilterID string `json:"filter_id" gorm:"uniqueIndex:uq_filter_user_id;size:64"`
	Body     string `json:"-" gorm:"type:text"`

	gorm.Model
}

// FilterRepo is the contract for sync filter storage access.
type FilterRepo interface {
	Create(filter *Filter) error
	Get(userID, filterID string) (*Filter, error)
	NextFilterID() (string, error)
}

// FilterRepoImpl is the GORM backed FilterRepo.
type FilterRepoImpl struct {
	db *gorm.DB
}

// Create implements [FilterRepo].
func (r *FilterRepoImpl) Create(filter *Filter) error {
	return r.db.Create(filter).Error
}

// Get implements [FilterRepo].
func (r *FilterRepoImpl) Get(userID, filterID string) (*Filter, error) {
	var filter Filter
	err := r.db.Where("user_id = ? AND filter_id = ?", userID, filterID).
		First(&filter).Error
	return &filter, err
}

// NextFilterID implements [FilterRepo] using an auto incremented numeric id.
func (r *FilterRepoImpl) NextFilterID() (string, error) {
	var maxID int64
	if err := r.db.Model(&Filter{}).Select("coalesce(max(id), 0)").Scan(&maxID).Error; err != nil {
		return "", err
	}
	return strconv.FormatInt(maxID+1, 10), nil
}

// NewFilterRepo returns the FilterRepo implementation for db.
func NewFilterRepo(db *gorm.DB) FilterRepo {
	return &FilterRepoImpl{db: db}
}
