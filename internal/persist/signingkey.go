package persist

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// SigningKey is an ed25519 signing key belonging to this server. The private
// half is stored base64-encoded; the row must never be logged or serialised into
// an API response.
type SigningKey struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	KeyID      string `gorm:"column:key_id;size:64;not null"` // e.g. "v1"; the full id is ed25519:v1
	Algorithm  string `gorm:"column:algorithm;size:32;not null"`
	PublicKey  string `gorm:"column:public_key;size:128;not null"`  // unpadded base64
	PrivateKey string `gorm:"column:private_key;size:256;not null"` // unpadded base64 of the ed25519 key
	Active     bool   `gorm:"not null;default:true"`
	CreatedAt  time.Time
}

// SigningKeyRepo is the signing key store contract.
type SigningKeyRepo interface {
	// WithTx returns a repository bound to an open transaction.
	WithTx(tx *gorm.DB) SigningKeyRepo
	Create(ctx context.Context, key *SigningKey) error
	Active(ctx context.Context) (*SigningKey, error)
	ByKeyID(ctx context.Context, keyID string) (*SigningKey, error)
	List(ctx context.Context) ([]SigningKey, error)
}

// SigningKeyRepoImpl is the GORM implementation of [SigningKeyRepo].
type SigningKeyRepoImpl struct {
	db *gorm.DB
}

// WithTx implements [SigningKeyRepo].
func (s *SigningKeyRepoImpl) WithTx(tx *gorm.DB) SigningKeyRepo {
	return &SigningKeyRepoImpl{db: tx}
}

// Create implements [SigningKeyRepo].
func (s *SigningKeyRepoImpl) Create(ctx context.Context, key *SigningKey) error {
	return s.db.WithContext(ctx).Create(key).Error
}

// Active implements [SigningKeyRepo].
func (s *SigningKeyRepoImpl) Active(ctx context.Context) (*SigningKey, error) {
	var key SigningKey
	err := s.db.WithContext(ctx).Where("active = ?", true).Order("id ASC").First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &key, err
}

// ByKeyID implements [SigningKeyRepo].
func (s *SigningKeyRepoImpl) ByKeyID(ctx context.Context, keyID string) (*SigningKey, error) {
	var key SigningKey
	err := s.db.WithContext(ctx).Where("key_id = ?", keyID).First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &key, err
}

// List implements [SigningKeyRepo].
func (s *SigningKeyRepoImpl) List(ctx context.Context) ([]SigningKey, error) {
	var keys []SigningKey
	err := s.db.WithContext(ctx).Order("id ASC").Find(&keys).Error
	return keys, err
}

// NewSigningKeyRepo returns the GORM implementation of [SigningKeyRepo].
func NewSigningKeyRepo(db *gorm.DB) SigningKeyRepo {
	return &SigningKeyRepoImpl{db: db}
}
