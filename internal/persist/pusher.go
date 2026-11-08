package persist

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Pusher is a place a client has asked this server to notify it: an app on a device, reachable
// at a URL, for one user.
//
// A pusher belongs to the session that wrote it, which is why the token hash is stored rather
// than the user alone. It is not a preference in the way account data is: a pusher is a device
// saying "wake me here", and when that session stops working the address it gave stops being
// someone else's to keep using. The password change that invalidates a session is expected to
// take that session's pushers with it.
//
// The identity of a pusher is (user, pushkey, app_id), which is what the specification says a
// client is replacing when it sets the same one again. It is not the token: a user setting the
// same pusher from a second device replaces the first device's row rather than adding a second
// notification path they never asked for.
type Pusher struct {
	// ID is this row's key; the identity used by callers is the triple indexed below.
	ID                uint64 `gorm:"primaryKey;autoIncrement"`
	UserID            string `gorm:"column:user_id;type:text;not null"`
	Pushkey           string `gorm:"column:pushkey;type:text;not null"`
	AppID             string `gorm:"column:app_id;type:text;not null"`
	Kind              string `gorm:"column:kind;size:32;not null"`
	AppDisplayName    string `gorm:"column:app_display_name;type:text"`
	DeviceDisplayName string `gorm:"column:device_display_name;type:text"`
	ProfileTag        string `gorm:"column:profile_tag;type:text"`
	Lang              string `gorm:"column:lang;size:32"`
	// Data is the client's own `data` object, kept as written. This server does not decide
	// what belongs in it; a push gateway and this server are different readers of the same
	// field, and rewriting it here would be guessing.
	Data      string `gorm:"column:data;type:text"`
	TokenHash string `gorm:"column:token_hash;size:255;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName pins the name; the default "pushers" is already right, and pinning it says so.
func (Pusher) TableName() string { return "pushers" }

// PusherRepo is the store of where a user's devices want to be woken.
type PusherRepo interface {
	WithTx(tx *gorm.DB) PusherRepo

	// Put writes a pusher, replacing whatever this user already had under the same
	// (pushkey, app_id).
	Put(ctx context.Context, pusher *Pusher) error
	// Remove drops the pusher a client named, which is how `kind: null` takes one away.
	Remove(ctx context.Context, userID, pushkey, appID string) error
	// ListForUser returns the user's pushers, in the order they were written.
	ListForUser(ctx context.Context, userID string) ([]*Pusher, error)
	// DeleteByTokenExcept drops every pusher of a user that was NOT written by the named
	// token. That is what a password change owes: the session that proved the new password
	// keeps its address, and every session that has just stopped working loses its.
	DeleteByTokenExcept(ctx context.Context, userID, keepTokenHash string) error
	// DeleteByToken drops every pusher written by one token, which is what logging that
	// session out owes.
	DeleteByToken(ctx context.Context, userID, tokenHash string) error
	// DeleteForUser drops every pusher of a user, which is what logging every session out
	// owes — no session is left that could be woken.
	DeleteForUser(ctx context.Context, userID string) error
}

// NewPusherRepo returns the pusher store bound to db.
func NewPusherRepo(db *gorm.DB) PusherRepo { return &pusherRepo{db: db} }

type pusherRepo struct{ db *gorm.DB }

// WithTx implements [PusherRepo].
func (r *pusherRepo) WithTx(tx *gorm.DB) PusherRepo { return &pusherRepo{db: tx} }

// Put implements [PusherRepo].
func (r *pusherRepo) Put(ctx context.Context, pusher *Pusher) error {
	var existing Pusher
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND pushkey = ? AND app_id = ?", pusher.UserID, pusher.Pushkey, pusher.AppID).
		First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.WithContext(ctx).Create(pusher).Error
	}
	if err != nil {
		return err
	}
	// The row is replaced under the same key rather than deleted and recreated, so that the
	// creation time a client may read is the first time this address was asked for.
	pusher.ID = existing.ID
	return r.db.WithContext(ctx).Save(pusher).Error
}

// Remove implements [PusherRepo].
func (r *pusherRepo) Remove(ctx context.Context, userID, pushkey, appID string) error {
	return r.db.WithContext(ctx).
		Where("user_id = ? AND pushkey = ? AND app_id = ?", userID, pushkey, appID).
		Delete(&Pusher{}).Error
}

// ListForUser implements [PusherRepo].
func (r *pusherRepo) ListForUser(ctx context.Context, userID string) ([]*Pusher, error) {
	var rows []*Pusher
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// DeleteByTokenExcept implements [PusherRepo].
func (r *pusherRepo) DeleteByTokenExcept(ctx context.Context, userID, keepTokenHash string) error {
	return r.db.WithContext(ctx).
		Where("user_id = ? AND token_hash <> ?", userID, keepTokenHash).
		Delete(&Pusher{}).Error
}

// DeleteForUser implements [PusherRepo].
func (r *pusherRepo) DeleteForUser(ctx context.Context, userID string) error {
	return r.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&Pusher{}).Error
}

// DeleteByToken implements [PusherRepo].
func (r *pusherRepo) DeleteByToken(ctx context.Context, userID, tokenHash string) error {
	return r.db.WithContext(ctx).
		Where("user_id = ? AND token_hash = ?", userID, tokenHash).
		Delete(&Pusher{}).Error
}
