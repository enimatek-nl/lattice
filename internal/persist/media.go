package persist

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// MediaMetadata is what this server knows about one piece of content: the file itself lives on disk
// under the content repository's path, and this row is what makes an `mxc://` URI mean something.
//
// The key is (Origin, MediaID) rather than MediaID alone because the same identifier can arrive from two
// different servers. `mxc://a/x` and `mxc://b/x` are different pieces of content and a cache that merged
// them would be serving one server's bytes under another's name.
type MediaMetadata struct {
	// Origin is the server name from the `mxc://` URI — the authority component. The Server-Server API
	// requires content to be fetched from exactly this server, so it is part of the identity of the
	// content and not a routing hint that can be dropped.
	Origin string `gorm:"column:origin;type:text;not null;primaryKey"`
	// MediaID is the opaque path component. It is validated on the way in (alphanumeric, `_` and `-`)
	// before it is ever used as a file name; the row stores what was validated.
	MediaID string `gorm:"column:media_id;type:text;not null;primaryKey"`
	// Local says this server holds the content itself rather than a copy of somebody else's. It decides
	// who may serve it, who must fetch it, and which errors are 413 rather than 502.
	Local bool `gorm:"column:local;not null;default:false"`
	// ContentType is what the uploader said the bytes are, as it will be handed back on a download. It is
	// stored verbatim because the specification calculates `Content-Disposition` from it; sanitising it
	// here would change the answer the client gets.
	ContentType string `gorm:"column:content_type;type:text;not null;default:''"`
	// UploadName is the file name the uploader gave, empty when none was given. It only ever affects the
	// `Content-Disposition` header.
	UploadName string `gorm:"column:upload_name;type:text;not null;default:''"`
	// SizeBytes is the size of the stored bytes. It is kept so a download can refuse, and a remote fetch
	// can refuse *before* downloading, rather than buffering a file nobody asked for.
	SizeBytes int64 `gorm:"column:size_bytes;not null;default:0"`
	// SHA256Hex is the hash of the stored bytes. It is not required by the protocol; it is what makes a
	// cache eviction or a corrupted file detectable rather than assumed.
	SHA256Hex string `gorm:"column:sha256_hex;type:text;not null;default:''"`
	// CreatedAt is when this row was made, in milliseconds since the epoch.
	CreatedAt int64 `gorm:"column:created_at;not null;default:0"`
	// UploadedAt is when the bytes arrived, 0 while an `mxc://` URI created by
	// POST /_matrix/media/v1/create is still waiting for its body. A zero value here is the difference
	// between "not uploaded yet" (504 M_NOT_YET_UPLOADED) and "does not exist" (404 M_NOT_FOUND).
	UploadedAt int64 `gorm:"column:uploaded_at;not null;default:0"`
	// PendingExpiresAt is when an un-uploaded URI stops being reserved, 0 for content that is already
	// uploaded. The specification asks servers to bound how long a created-but-unused URI may sit around.
	PendingExpiresAt int64 `gorm:"column:pending_expires_at;not null;default:0"`
	// CreatedBy is the local user who uploaded this, empty for content fetched from another server. A
	// quota or an administrative deletion needs it, and it is the only ownership record kept.
	CreatedBy string `gorm:"column:created_by;type:text;not null;default:''"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (MediaMetadata) TableName() string { return "media_metadata" }

// MediaRepo is the content repository's metadata store.
//
// The bytes themselves are files under the repository's directory; this store keeps the promises about
// them. They are deliberately separate: a metadata row and its file are written in that order, and a row
// whose file has gone is reported as missing rather than served as empty.
type MediaRepo interface {
	// Put stores (or replaces) metadata for one piece of content.
	Put(ctx context.Context, metadata *MediaMetadata) error
	// Get returns one piece of content's metadata, or ErrNotFound when nothing is known about it.
	Get(ctx context.Context, origin, mediaID string) (*MediaMetadata, error)
	// MarkUploaded fills in the size, hash and timestamp once the bytes have been written.
	MarkUploaded(ctx context.Context, origin, mediaID string, sizeBytes int64, sha256Hex string, at int64) error
	// Delete forgets one piece of content. The caller removes the file; a row removed without its file
	// leaves an orphan on disk, which is why the removal is the caller's business.
	Delete(ctx context.Context, origin, mediaID string) error
	// PurgeExpired removes created-but-never-uploaded rows whose reservation has passed, and returns what
	// it removed so the caller can remove any partial file too.
	PurgeExpired(ctx context.Context, nowMillis int64) ([]*MediaMetadata, error)
	// CountPendingFor counts how many URIs this user has created and not yet filled as of nowMillis. The
	// specification asks servers to bound that number rather than let it grow without limit.
	CountPendingFor(ctx context.Context, userID string, nowMillis int64) (int64, error)
}

// NewMediaRepo returns the media metadata repository bound to db.
func NewMediaRepo(db *gorm.DB) MediaRepo { return &mediaRepo{db: db} }

type mediaRepo struct{ db *gorm.DB }

func (r *mediaRepo) Put(ctx context.Context, metadata *MediaMetadata) error {
	if metadata == nil || metadata.Origin == "" || metadata.MediaID == "" {
		return errors.New("persist: media metadata needs an origin and a media id")
	}
	return r.db.WithContext(ctx).Save(metadata).Error
}

func (r *mediaRepo) Get(ctx context.Context, origin, mediaID string) (*MediaMetadata, error) {
	var metadata MediaMetadata
	err := r.db.WithContext(ctx).
		Where("origin = ? AND media_id = ?", origin, mediaID).
		First(&metadata).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &metadata, nil
}

func (r *mediaRepo) MarkUploaded(ctx context.Context, origin, mediaID string, sizeBytes int64, sha256Hex string, at int64) error {
	result := r.db.WithContext(ctx).Model(&MediaMetadata{}).
		Where("origin = ? AND media_id = ?", origin, mediaID).
		Updates(map[string]any{
			"uploaded_at":        at,
			"size_bytes":         sizeBytes,
			"sha256_hex":         sha256Hex,
			"pending_expires_at": 0,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *mediaRepo) Delete(ctx context.Context, origin, mediaID string) error {
	return r.db.WithContext(ctx).
		Where("origin = ? AND media_id = ?", origin, mediaID).
		Delete(&MediaMetadata{}).Error
}

func (r *mediaRepo) PurgeExpired(ctx context.Context, nowMillis int64) ([]*MediaMetadata, error) {
	var expired []*MediaMetadata
	if err := r.db.WithContext(ctx).
		Where("uploaded_at = 0 AND pending_expires_at > 0 AND pending_expires_at <= ?", nowMillis).
		Find(&expired).Error; err != nil {
		return nil, err
	}
	if len(expired) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(expired))
	for _, metadata := range expired {
		ids = append(ids, metadata.MediaID)
	}
	err := r.db.WithContext(ctx).
		Where("uploaded_at = 0 AND media_id IN ?", ids).
		Delete(&MediaMetadata{}).Error
	if err != nil {
		return nil, err
	}
	return expired, nil
}

func (r *mediaRepo) CountPendingFor(ctx context.Context, userID string, nowMillis int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&MediaMetadata{}).
		Where("created_by = ? AND uploaded_at = 0 AND (pending_expires_at = 0 OR pending_expires_at > ?)",
			userID, nowMillis).
		Count(&count).Error
	return count, err
}
