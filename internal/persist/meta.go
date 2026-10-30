package persist

import "gorm.io/gorm"

// Meta is a simple key/value store for server level state such as the signing
// secret that backs access tokens.
type Meta struct {
	Key   string `json:"key" gorm:"uniqueIndex;size:128"`
	Value string `json:"value" gorm:"type:text"`

	gorm.Model
}

// MetaRepo is the contract for the server metadata store.
type MetaRepo interface {
	Get(key string) (string, error)
	Set(key, value string) error
	GetOrSet(key string, generate func() (string, error)) (string, error)
}

// MetaRepoImpl is the GORM backed MetaRepo.
type MetaRepoImpl struct {
	db *gorm.DB
}

// Get implements [MetaRepo].
func (r *MetaRepoImpl) Get(key string) (string, error) {
	var meta Meta
	err := r.db.Where("key = ?", key).First(&meta).Error
	if err != nil {
		return "", err
	}
	return meta.Value, nil
}

// Set implements [MetaRepo].
func (r *MetaRepoImpl) Set(key, value string) error {
	meta := Meta{Key: key, Value: value}
	return r.db.Where("key = ?", key).Assign(meta).FirstOrCreate(&Meta{}).Error
}

// GetOrSet implements [MetaRepo].
func (r *MetaRepoImpl) GetOrSet(key string, generate func() (string, error)) (string, error) {
	value, err := r.Get(key)
	if err == nil && value != "" {
		return value, nil
	}
	if err != nil && !IsNotFound(err) {
		return "", err
	}
	value, err = generate()
	if err != nil {
		return "", err
	}
	if err := r.Set(key, value); err != nil {
		return "", err
	}
	return value, nil
}

// NewMetaRepo returns the MetaRepo implementation for db.
func NewMetaRepo(db *gorm.DB) MetaRepo {
	return &MetaRepoImpl{db: db}
}
