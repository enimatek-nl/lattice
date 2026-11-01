package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ServerKey is one signing keypair of this homeserver. Federation identifies a
// server by the public half of such a pair: every request it makes carries a
// signature made with the private half, and every event it signs names the key
// id. The pair therefore has to outlive restarts - a server that mints a fresh
// key on every boot invalidates the events it signed yesterday - so the private
// half lives in the database next to the public one and is never regenerated
// behind an existing row's back.
//
// The private half is kept as the 32-byte ed25519 seed rather than the expanded
// 64-byte key: it is the shortest form that survives a round trip, and Go
// rebuilds the expanded key from it. It is excluded from JSON so it cannot leave
// the process inside a debug dump of a row.
type ServerKey struct {
	// KeyID is the algorithm and the version joined with a colon, as the key
	// appears in verify_keys and in Authorization headers: "ed25519:0kBpa0".
	KeyID string `json:"key_id" gorm:"uniqueIndex:uq_serverkey_key_id;size:128"`
	// Version is the part after the colon. The specification restricts it to
	// [a-zA-Z0-9_].
	Version string `json:"version" gorm:"size:64"`
	// PublicKey is the unpadded base64 of the ed25519 public key, which is what
	// /_matrix/key/v2/server publishes.
	PublicKey string `json:"public_key" gorm:"size:128"`
	// PrivateSeed is the unpadded base64 of the 32-byte ed25519 seed.
	PrivateSeed string `json:"-" gorm:"type:text"`
	// CreatedTS is when the key was generated, in milliseconds. Rotation is not
	// implemented, so exactly one row is expected; the newest one wins.
	CreatedTS int64 `json:"created_ts" gorm:"index"`

	gorm.Model
}

// ServerKeyRepo is the contract for the signing keys of this server.
type ServerKeyRepo interface {
	// Store inserts a key. The key id is unique and a repeated insert changes
	// nothing, which keeps a racing first boot from minting two pairs and leaving
	// half the events signed with one and half with the other.
	Store(key *ServerKey) error
	// Current returns the newest key, or a not-found error that
	// [IsNotFound] reports when this server has none yet.
	Current() (*ServerKey, error)
	// Get returns the key with the given id, or a not-found error.
	Get(keyID string) (*ServerKey, error)
	// List returns every key, oldest first.
	List() ([]*ServerKey, error)
}

// ServerKeyRepoImpl is the GORM backed ServerKeyRepo.
type ServerKeyRepoImpl struct {
	db *gorm.DB
}

// Store implements [ServerKeyRepo].
func (r *ServerKeyRepoImpl) Store(key *ServerKey) error {
	return r.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key_id"}}, DoNothing: true}).
		Create(key).Error
}

// Current implements [ServerKeyRepo].
func (r *ServerKeyRepoImpl) Current() (*ServerKey, error) {
	var key ServerKey
	if err := r.db.Order("created_ts desc, id desc").First(&key).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

// Get implements [ServerKeyRepo].
func (r *ServerKeyRepoImpl) Get(keyID string) (*ServerKey, error) {
	var key ServerKey
	if err := r.db.Where("key_id = ?", keyID).First(&key).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

// List implements [ServerKeyRepo].
func (r *ServerKeyRepoImpl) List() ([]*ServerKey, error) {
	var keys []*ServerKey
	err := r.db.Order("created_ts asc, id asc").Find(&keys).Error
	return keys, err
}

// NewServerKeyRepo returns the ServerKeyRepo implementation for db.
func NewServerKeyRepo(db *gorm.DB) ServerKeyRepo {
	return &ServerKeyRepoImpl{db: db}
}
