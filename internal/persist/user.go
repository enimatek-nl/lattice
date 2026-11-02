package persist

import (
	"gorm.io/gorm"
)

// User is a Matrix account on this homeserver.
type User struct {
	// ID is the full Matrix user ID, e.g. "@alice:example.com".
	ID string `json:"id" gorm:"uniqueIndex;size:255"`
	// Localpart is the part of the ID before the server name.
	Localpart string `json:"localpart" gorm:"uniqueIndex;size:128"`
	// PasswordHash is a bcrypt hash and is never serialised to clients.
	PasswordHash string `json:"-" gorm:"size:255"`
	// Admin marks server administrators.
	Admin bool `json:"admin"`
	// Deactivated is set when the account was deactivated by its owner.
	Deactivated bool `json:"deactivated"`
	// Locked and Suspended are administrative flags.
	Locked    bool `json:"locked"`
	Suspended bool `json:"suspended"`
	// Guest marks accounts created with kind=guest.
	Guest bool `json:"guest"`

	gorm.Model
}

// UserRepo is the contract for user storage access.
type UserRepo interface {
	Create(user *User) error
	GetByID(id string) (*User, error)
	GetByLocalpart(localpart string) (*User, error)
	Save(user *User) error
	List(limit, offset int) ([]*User, error)
	Count() (int64, error)
	// CountOutsideDomain counts the accounts this server cannot act for: the ones
	// whose user ID does not end in ":"+domain. A changed -server-name is what
	// produces them, and they keep authenticating while being unable to join rooms.
	CountOutsideDomain(domain string) (int64, error)
	Deactivate(id string) error
}

// UserRepoImpl is the GORM backed UserRepo.
type UserRepoImpl struct {
	db *gorm.DB
}

// Create implements [UserRepo].
func (r *UserRepoImpl) Create(user *User) error {
	return r.db.Create(user).Error
}

// GetByID implements [UserRepo].
func (r *UserRepoImpl) GetByID(id string) (*User, error) {
	var user User
	err := r.db.Where("id = ?", id).First(&user).Error
	return &user, err
}

// GetByLocalpart implements [UserRepo].
func (r *UserRepoImpl) GetByLocalpart(localpart string) (*User, error) {
	var user User
	err := r.db.Where("localpart = ?", localpart).First(&user).Error
	return &user, err
}

// Save implements [UserRepo].
func (r *UserRepoImpl) Save(user *User) error {
	return r.db.Save(user).Error
}

// List implements [UserRepo].
func (r *UserRepoImpl) List(limit, offset int) ([]*User, error) {
	var users []*User
	err := r.db.Where("deactivated = ?", false).
		Order("id asc").Limit(limit).Offset(offset).Find(&users).Error
	return users, err
}

// Count implements [UserRepo].
func (r *UserRepoImpl) Count() (int64, error) {
	var n int64
	err := r.db.Model(&User{}).Where("deactivated = ?", false).Count(&n).Error
	return n, err
}

// CountOutsideDomain implements [UserRepo]. The domain of a user ID is what follows
// its first colon, since a localpart may not contain one; LIKE is used rather than a
// dialect's string functions, with the wildcards of the name escaped so a name cannot
// match an account it does not own.
func (r *UserRepoImpl) CountOutsideDomain(domain string) (int64, error) {
	var n int64
	err := r.db.Model(&User{}).Where("deactivated = ?", false).
		Where("id NOT LIKE ? ESCAPE '\\'", "%:"+escapeLike(domain)).Count(&n).Error
	return n, err
}

// Deactivate implements [UserRepo].
func (r *UserRepoImpl) Deactivate(id string) error {
	res := r.db.Model(&User{}).Where("id = ?", id).
		Updates(map[string]any{"deactivated": true, "password_hash": ""})
	return res.Error
}

// NewUserRepo returns the UserRepo implementation for db.
func NewUserRepo(db *gorm.DB) UserRepo {
	return &UserRepoImpl{db: db}
}
