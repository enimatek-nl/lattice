package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Media is an uploaded piece of content. Content is stored inline for now; the
// blob column keeps the payload out of the way of queries.
type Media struct {
	MediaID     string `json:"media_id" gorm:"uniqueIndex:uq_media_scope;size:255"`
	Origin      string `json:"origin" gorm:"uniqueIndex:uq_media_scope;size:255"`
	UserID      string `json:"user_id" gorm:"index;size:255"`
	FileName    string `json:"filename" gorm:"size:255"`
	ContentType string `json:"content_type" gorm:"size:128"`
	Size        int64  `json:"size"`
	Blob        []byte `json:"-" gorm:"type:blob"`

	gorm.Model
}

// MediaRepo is the contract for media storage access.
type MediaRepo interface {
	Create(media *Media) error
	Save(media *Media) error
	Get(origin, mediaID string) (*Media, error)
	Count() (int64, error)
}

// MediaRepoImpl is the GORM backed MediaRepo.
type MediaRepoImpl struct {
	db *gorm.DB
}

// Create implements [MediaRepo].
func (r *MediaRepoImpl) Create(media *Media) error {
	return r.db.Create(media).Error
}

// Save implements [MediaRepo]. The (origin, media id) pair identifies the
// content, so saving again fills in a reservation made by POST /create.
func (r *MediaRepoImpl) Save(media *Media) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "media_id"}, {Name: "origin"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"user_id", "file_name", "content_type", "size", "blob", "updated_at",
		}),
	}).Create(media).Error
}

// Get implements [MediaRepo].
func (r *MediaRepoImpl) Get(origin, mediaID string) (*Media, error) {
	var media Media
	err := r.db.Where("media_id = ? AND origin = ?", mediaID, origin).First(&media).Error
	return &media, err
}

// Count implements [MediaRepo].
func (r *MediaRepoImpl) Count() (int64, error) {
	var n int64
	err := r.db.Model(&Media{}).Count(&n).Error
	return n, err
}

// NewMediaRepo returns the MediaRepo implementation for db.
func NewMediaRepo(db *gorm.DB) MediaRepo {
	return &MediaRepoImpl{db: db}
}
