package persist

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Filter is a stored sync filter. The body is kept as the JSON the client sent rather
// than as columns: a filter is an opaque document whose meaning belongs to the client
// synchronisation layer, and reshaping it into columns would make the server's stored
// filters diverge from what the client asked for.
type Filter struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	UserID    string `gorm:"column:user_id;size:255;not null"`
	Body      string `gorm:"column:body;type:text;not null"`
	CreatedAt time.Time
}

// TableName pins the table name.
func (Filter) TableName() string { return "filters" }

// FilterRepo stores and retrieves filters.
type FilterRepo interface {
	WithTx(tx *gorm.DB) FilterRepo

	// Save stores a filter and fills in its ID.
	Save(ctx context.Context, filter *Filter) error

	// ByID returns a user's filter. A filter belongs to the user who created it, so a
	// lookup is always scoped to that user: another user's filter ID must not resolve.
	ByID(ctx context.Context, userID string, id int64) (*Filter, error)
}

type filterRepo struct{ db *gorm.DB }

// NewFilterRepo returns the filter repository.
func NewFilterRepo(db *gorm.DB) FilterRepo { return &filterRepo{db: db} }

// WithTx implements [FilterRepo]. Required whenever a write happens inside a
// transaction; see RoomAliasRepo for why leaving it out deadlocks a single-writer
// database.
func (r *filterRepo) WithTx(tx *gorm.DB) FilterRepo { return &filterRepo{db: tx} }

// Save implements [FilterRepo].
func (r *filterRepo) Save(ctx context.Context, filter *Filter) error {
	if err := r.db.WithContext(ctx).Create(filter).Error; err != nil {
		return asConflict(err)
	}
	return nil
}

// ByID implements [FilterRepo].
func (r *filterRepo) ByID(ctx context.Context, userID string, id int64) (*Filter, error) {
	var filter Filter
	err := r.db.WithContext(ctx).Where("user_id = ? AND id = ?", userID, id).First(&filter).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &filter, nil
}
