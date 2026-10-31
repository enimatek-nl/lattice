package persist

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CrossSigningKey is one cross-signing key of a user: the master key, the
// self-signing key or the user-signing key. Unlike the device keys of
// internal/persist/keys.go these objects are checked by Lattice before they are
// stored, because a client that trusts them trusts everything signed by them.
type CrossSigningKey struct {
	UserID string `json:"user_id" gorm:"uniqueIndex:uq_crosssigning_key;size:255"`
	// KeyID is the id of the key itself, e.g. "ed25519:OzD3sw7mkY". A client
	// reads the cross-signing keys out of POST /keys/query as entries of the
	// device map, so the key id is what names them there.
	KeyID string `json:"key_id" gorm:"uniqueIndex:uq_crosssigning_key;size:255"`
	// Usage is master, self-signing or user-signing.
	Usage string `json:"usage" gorm:"size:32"`
	// Data is the key object as the client uploaded it, with the signatures
	// merged in as further clients sign it.
	Data        string `json:"data" gorm:"type:text"`
	FirstSeenMS int64  `json:"first_seen_ms"`

	gorm.Model
}

// CrossSigningRepo is the contract for cross-signing key storage access.
type CrossSigningRepo interface {
	// Set stores a key, replacing an earlier key with the same id.
	Set(key *CrossSigningKey) error
	Get(userID, keyID string) (*CrossSigningKey, error)
	// List returns the keys of a user, oldest first, which is the order a client
	// expects them in when it rebuilds its picture of who to trust.
	List(userID string) ([]*CrossSigningKey, error)
	// ByUsage returns the single key of a usage, or ErrNoKey when the user has
	// none.
	ByUsage(userID, usage string) (*CrossSigningKey, error)
	// DeleteAll drops every key of a user, which is what account deactivation
	// asks for.
	DeleteAll(userID string) (int64, error)
}

// ErrNoKey reports that a user has no key of the requested kind.
var ErrNoKey = errors.New("no such key")

// CrossSigningRepoImpl is the GORM backed CrossSigningRepo.
type CrossSigningRepoImpl struct {
	db *gorm.DB
}

// NewCrossSigningRepo builds the repository over an open connection.
func NewCrossSigningRepo(db *gorm.DB) *CrossSigningRepoImpl {
	return &CrossSigningRepoImpl{db: db}
}

// NewCrossSigningRepoTx rebinds the repository to a transaction.
func NewCrossSigningRepoTx(tx *gorm.DB) *CrossSigningRepoImpl {
	return NewCrossSigningRepo(tx)
}

// Set implements [CrossSigningRepo].
func (r *CrossSigningRepoImpl) Set(key *CrossSigningKey) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "key_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"usage", "data", "first_seen_ms"}),
	}).Create(key).Error
}

// Get implements [CrossSigningRepo].
func (r *CrossSigningRepoImpl) Get(userID, keyID string) (*CrossSigningKey, error) {
	var key CrossSigningKey
	if err := r.db.Where("user_id = ? AND key_id = ?", userID, keyID).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoKey
		}
		return nil, err
	}
	return &key, nil
}

// List implements [CrossSigningRepo].
func (r *CrossSigningRepoImpl) List(userID string) ([]*CrossSigningKey, error) {
	var rows []*CrossSigningKey
	err := r.db.Where("user_id = ?", userID).Order("first_seen_ms asc, id asc").Find(&rows).Error
	return rows, err
}

// ByUsage implements [CrossSigningRepo].
func (r *CrossSigningRepoImpl) ByUsage(userID, usage string) (*CrossSigningKey, error) {
	var key CrossSigningKey
	if err := r.db.Where("user_id = ? AND usage = ?", userID, usage).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoKey
		}
		return nil, err
	}
	return &key, nil
}

// DeleteAll implements [CrossSigningRepo].
func (r *CrossSigningRepoImpl) DeleteAll(userID string) (int64, error) {
	// Removed for real, not flagged: a key that is only marked as deleted still
	// holds its place in the unique index over (user_id, key_id), and the next
	// upload of that id would then update a row no read can see.
	result := r.db.Unscoped().Where("user_id = ?", userID).Delete(&CrossSigningKey{})
	return result.RowsAffected, result.Error
}
