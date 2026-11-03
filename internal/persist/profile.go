package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProfileField stores a single profile key (displayname, avatar_url, ...) for
// a user. Profile keys are open ended in the spec, so they are stored as
// key/value rows instead of columns.
type ProfileField struct {
	UserID string `json:"user_id" gorm:"uniqueIndex:uq_profile_user_key;size:255"`
	Key    string `json:"key" gorm:"uniqueIndex:uq_profile_user_key;size:128"`
	Value  string `json:"value" gorm:"type:text"`

	gorm.Model
}

// ProfileRepo is the contract for user profile storage access.
type ProfileRepo interface {
	Set(userID, key, value string) error
	Get(userID, key string) (string, error)
	// FieldsFor loads the named keys for many users in one query, keyed
	// user ID, then key. A user without a row for a key simply has no entry.
	FieldsFor(userIDs []string, keys ...string) (map[string]map[string]string, error)
	All(userID string) (map[string]string, error)
	Delete(userID, key string) error
}

// ProfileRepoImpl is the GORM backed ProfileRepo.
type ProfileRepoImpl struct {
	db *gorm.DB
}

// Set implements [ProfileRepo]. An empty value deletes the key.
func (r *ProfileRepoImpl) Set(userID, key, value string) error {
	if value == "" {
		return r.Delete(userID, key)
	}
	field := ProfileField{UserID: userID, Key: key, Value: value}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&field).Error
}

// Get implements [ProfileRepo].
func (r *ProfileRepoImpl) Get(userID, key string) (string, error) {
	var field ProfileField
	err := r.db.Where("user_id = ? AND key = ?", userID, key).First(&field).Error
	if err != nil {
		return "", err
	}
	return field.Value, nil
}

// FieldsFor implements [ProfileRepo]. Membership and directory listings show a
// name and an avatar per member, and looking those up one row at a time turns a
// member list into a query per member.
func (r *ProfileRepoImpl) FieldsFor(userIDs []string, keys ...string) (map[string]map[string]string, error) {
	if len(userIDs) == 0 {
		return map[string]map[string]string{}, nil
	}
	query := r.db.Where("user_id IN ?", userIDs)
	if len(keys) > 0 {
		query = query.Where("key IN ?", keys)
	}
	var fields []ProfileField
	if err := query.Find(&fields).Error; err != nil {
		return nil, err
	}
	out := make(map[string]map[string]string, len(userIDs))
	for _, field := range fields {
		if out[field.UserID] == nil {
			out[field.UserID] = map[string]string{}
		}
		out[field.UserID][field.Key] = field.Value
	}
	return out, nil
}

// All implements [ProfileRepo].
func (r *ProfileRepoImpl) All(userID string) (map[string]string, error) {
	var fields []ProfileField
	if err := r.db.Where("user_id = ?", userID).Find(&fields).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		out[f.Key] = f.Value
	}
	return out, nil
}

// Delete implements [ProfileRepo]. The row is dropped for real so the same key
// can be set again afterwards.
func (r *ProfileRepoImpl) Delete(userID, key string) error {
	return r.db.Unscoped().Where("user_id = ? AND key = ?", userID, key).
		Unscoped().Delete(&ProfileField{}).
		Error
}

// NewProfileRepo returns the ProfileRepo implementation for db.
func NewProfileRepo(db *gorm.DB) ProfileRepo {
	return &ProfileRepoImpl{db: db}
}
