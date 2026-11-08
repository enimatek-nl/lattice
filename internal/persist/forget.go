package persist

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// RoomForget records that a user has asked this server to stop telling them about a room.
//
// Forgetting is not a membership. The membership event stays exactly where it is — it is part of
// the room's history, signed by whoever sent it, and no other member's view of the room changes
// because one person stopped wanting to see it. What changes is this user's: the room stops
// appearing in their sync, and they stop being able to read it, until they come back to it. That
// is a fact about one person's relationship to one room, so it is stored as one row rather than
// as an edit to anything the room holds.
type RoomForget struct {
	// ID is this row's key; the identity of a forget is the (user, room) pair, indexed below.
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	UserID      string `gorm:"column:user_id;type:text;not null"`
	RoomID      string `gorm:"column:room_id;type:text;not null"`
	ForgottenAt int64  `gorm:"column:forgotten_at;not null;default:0"`
}

// TableName pins the table name; "room_forgets" is not a word.
func (RoomForget) TableName() string { return "room_forgotten" }

// ForgetRepo records which rooms a user has forgotten.
type ForgetRepo interface {
	WithTx(tx *gorm.DB) ForgetRepo

	// Mark records the forget and returns the stream position it was recorded at, which is
	// what lets a synchronisation decide whether the forget happened before or after the
	// window it is reporting.
	Mark(ctx context.Context, forget *RoomForget) (int64, error)
	// Clear forgets the forget, which is what joining or being invited to a room again does.
	Clear(ctx context.Context, userID, roomID string) error
	// Of answers whether one user has forgotten one room.
	Of(ctx context.Context, userID, roomID string) (*RoomForget, error)
	// ListForUser returns the user's forgotten rooms, which is one query per synchronisation
	// rather than one per room they have ever been in.
	ListForUser(ctx context.Context, userID string) (map[string]*RoomForget, error)
}

// NewForgetRepo returns the forget store bound to db.
func NewForgetRepo(db *gorm.DB) ForgetRepo { return &forgetRepo{db: db} }

type forgetRepo struct{ db *gorm.DB }

// WithTx implements [ForgetRepo].
func (r *forgetRepo) WithTx(tx *gorm.DB) ForgetRepo { return &forgetRepo{db: tx} }

// Mark implements [ForgetRepo].
func (r *forgetRepo) Mark(ctx context.Context, forget *RoomForget) (int64, error) {
	var existing RoomForget
	err := r.db.WithContext(ctx).Where("user_id = ? AND room_id = ?", forget.UserID, forget.RoomID).
		First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := r.db.WithContext(ctx).Create(forget).Error; err != nil {
			return 0, err
		}
		return forget.ForgottenAt, nil
	}
	if err := r.db.WithContext(ctx).Model(&RoomForget{}).Where("id = ?", existing.ID).
		Update("forgotten_at", forget.ForgottenAt).Error; err != nil {
		return 0, err
	}
	return forget.ForgottenAt, nil
}

// Clear implements [ForgetRepo].
func (r *forgetRepo) Clear(ctx context.Context, userID, roomID string) error {
	return r.db.WithContext(ctx).
		Where("user_id = ? AND room_id = ?", userID, roomID).Delete(&RoomForget{}).Error
}

// Of implements [ForgetRepo].
func (r *forgetRepo) Of(ctx context.Context, userID, roomID string) (*RoomForget, error) {
	var row RoomForget
	err := r.db.WithContext(ctx).Where("user_id = ? AND room_id = ?", userID, roomID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListForUser implements [ForgetRepo].
func (r *forgetRepo) ListForUser(ctx context.Context, userID string) (map[string]*RoomForget, error) {
	var rows []*RoomForget
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]*RoomForget, len(rows))
	for _, row := range rows {
		out[row.RoomID] = row
	}
	return out, nil
}
