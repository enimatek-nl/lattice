package persist

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// User is a local Matrix account. Passwords are stored only as bcrypt hashes and
// access tokens only as digests, so this row is safe to back up but never to log.
type User struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	UserID       string `gorm:"column:user_id;size:255;not null"`
	Localpart    string `gorm:"column:localpart;size:128;not null"`
	PasswordHash string `gorm:"column:password_hash;size:255;not null"`
	Admin        bool   `gorm:"not null;default:false"`
	Deactivated  bool   `gorm:"not null;default:false"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UserRepo is the account store contract.
type UserRepo interface {
	// WithTx returns a repository bound to an open transaction, so that reads and
	// writes inside the transaction see the same connection and the same state.
	WithTx(tx *gorm.DB) UserRepo
	Create(ctx context.Context, user *User) error
	ByUserID(ctx context.Context, userID string) (*User, error)
	ByLocalpart(ctx context.Context, localpart string) (*User, error)
	SetDeactivated(ctx context.Context, userID string, deactivated bool) error
	Count(ctx context.Context) (int64, error)
	// ListUserIDs returns every account on this server in a stable order. It exists
	// for work that has to visit each user exactly once, such as giving accounts that
	// predate a feature the data that feature assumes.
	ListUserIDs(ctx context.Context) ([]string, error)
}

// UserRepoImpl is the GORM implementation of [UserRepo].
type UserRepoImpl struct {
	db *gorm.DB
}

// WithTx implements [UserRepo].
func (u *UserRepoImpl) WithTx(tx *gorm.DB) UserRepo {
	return &UserRepoImpl{db: tx}
}

// Create implements [UserRepo].
func (u *UserRepoImpl) Create(ctx context.Context, user *User) error {
	if err := u.db.WithContext(ctx).Create(user).Error; err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

// ByUserID implements [UserRepo].
func (u *UserRepoImpl) ByUserID(ctx context.Context, userID string) (*User, error) {
	var user User
	err := u.db.WithContext(ctx).Where("user_id = ?", userID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &user, err
}

// ByLocalpart implements [UserRepo].
func (u *UserRepoImpl) ByLocalpart(ctx context.Context, localpart string) (*User, error) {
	var user User
	err := u.db.WithContext(ctx).Where("localpart = ?", localpart).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &user, err
}

// SetDeactivated implements [UserRepo].
func (u *UserRepoImpl) SetDeactivated(ctx context.Context, userID string, deactivated bool) error {
	res := u.db.WithContext(ctx).Model(&User{}).Where("user_id = ?", userID).
		Update("deactivated", deactivated)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Count implements [UserRepo].
func (u *UserRepoImpl) Count(ctx context.Context) (int64, error) {
	var n int64
	err := u.db.WithContext(ctx).Model(&User{}).Count(&n).Error
	return n, err
}

// ListUserIDs implements [UserRepo].
func (u *UserRepoImpl) ListUserIDs(ctx context.Context) ([]string, error) {
	var ids []string
	if err := u.db.WithContext(ctx).Model(&User{}).Order("user_id ASC").Pluck("user_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// NewUserRepo returns the GORM implementation of [UserRepo].
func NewUserRepo(db *gorm.DB) UserRepo {
	return &UserRepoImpl{db: db}
}
