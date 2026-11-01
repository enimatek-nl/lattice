package service

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"strconv"
	"strings"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// DefaultMaxUploadSize is the media upload limit (10 MiB).
const DefaultMaxUploadSize = 10 * 1024 * 1024

// DefaultMediaReservationWindow is how long a media id minted by
// POST /_matrix/media/v3/create waits for the bytes that were promised for it.
// The specification gives this 24 hours; a shorter window makes abandoned uploads
// (a client that crashed mid-upload) disappear sooner, a longer one keeps a
// client able to resume.
const DefaultMediaReservationWindow = 24 * time.Hour

// MaxUploadSize is the largest upload this server accepts. It is configured
// through the server options.
func (s *Service) MaxUploadSize() int64 { return s.maxUploadSize }

// SetMaxUploadSize configures the upload limit.
func (s *Service) SetMaxUploadSize(size int64) {
	if size > 0 {
		s.maxUploadSize = size
	}
}

// MediaConfig describes the media repository of this homeserver.
func (s *Service) MediaConfig() matrix.Object {
	return matrix.Object{
		"m.upload.size":      s.maxUploadSize,
		"content_repository": "",
	}
}

// UploadMedia stores content and returns its media id.
func (s *Service) UploadMedia(ctx context.Context, userID, filename, contentType string, data []byte) (string, error) {
	if int64(len(data)) > s.maxUploadSize {
		return "", matrix.NewError(matrix.ErrCodeLimit, 413,
			"file too large, maximum "+strconv.FormatInt(s.maxUploadSize, 10)+" bytes")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	mediaID := matrix.NewMediaID()
	media := &persist.Media{
		MediaID:     mediaID,
		Origin:      s.serverName,
		UserID:      userID,
		FileName:    filename,
		ContentType: contentType,
		Size:        int64(len(data)),
		Blob:        data,
	}
	if err := s.db.MediaRepo.Create(media); err != nil {
		return "", matrix.ErrInternal(err)
	}
	return mediaID, nil
}

// ReserveMedia handles POST /_matrix/media/v3/create, which mints a media id
// and MXC URI before the client uploads the bytes. The response also carries the
// URL to upload to and when the reservation expires, both of which the
// specification asks for: Element and matrix-js-sdk use this async flow for
// every upload, including the room avatar, so a reservation without a promised
// upload URL and deadline is a client left guessing.
func (s *Service) ReserveMedia(ctx context.Context, userID, filename, contentType string, size int64) (matrix.Object, error) {
	if size > s.maxUploadSize {
		return nil, matrix.NewError(matrix.ErrCodeLimit, 413,
			"file too large, maximum "+strconv.FormatInt(s.maxUploadSize, 10)+" bytes")
	}
	mediaID, err := s.UploadMedia(ctx, userID, filename, contentType, nil)
	if err != nil {
		return nil, err
	}
	expires := s.NowMS() + s.reservationWindow.Milliseconds()
	return matrix.Object{
		"content_uri": "mxc://" + s.serverName + "/" + mediaID,
		// The same path the authenticated upload uses: Lattice has no unauthenticated
		// media writes, so a client that follows this URL sends its token like always.
		"upload_url":    "/_matrix/media/v3/upload/" + s.serverName + "/" + mediaID,
		"expires_ms":    s.reservationWindow.Milliseconds(),
		"expires_at_ts": expires,
	}, nil
}

// CommitMedia finishes an async upload: POST /_matrix/media/v3/commit/{server}
// /{mediaId} tells the server the bytes have been sent, and a client that gets
// there first may wait up to timeout for the PUT to arrive.
//
// There is no "uploaded" flag to flip. A media row either holds bytes or it is a
// reservation, so committing means confirming that content arrived; if it has
// not, this waits for it and then reports the reservation as unknown, which is
// also what a download of a content-less media id says.
func (s *Service) CommitMedia(ctx context.Context, userID, mediaID string, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		media, err := s.db.MediaRepo.Get(s.serverName, mediaID)
		switch {
		case err != nil && !persist.IsNotFound(err):
			return matrix.ErrInternal(err)
		case err == nil && (media.Size > 0 || len(media.Blob) > 0):
			if media.UserID != "" && media.UserID != userID {
				return matrix.ErrForbidden("media %s belongs to another user", mediaID)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return matrix.ErrNotFound("no content was uploaded for media id %s", mediaID)
		case <-ticker.C:
		}
	}
}

// UploadToReservedMedia stores bytes against a media id created by
// POST /_matrix/media/v3/create. The reservation row is replaced in place, so a
// media id can only receive content once.
func (s *Service) UploadToReservedMedia(ctx context.Context, userID, mediaID, filename, contentType string, data []byte) error {
	if int64(len(data)) > s.maxUploadSize {
		return matrix.NewError(matrix.ErrCodeLimit, 413,
			"file too large, maximum "+strconv.FormatInt(s.maxUploadSize, 10)+" bytes")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	existing, err := s.db.MediaRepo.Get(s.serverName, mediaID)
	switch {
	case err != nil && !persist.IsNotFound(err):
		return matrix.ErrInternal(err)
	case err == nil && existing.UserID != "" && existing.UserID != userID:
		return matrix.ErrForbidden("media %s belongs to another user", mediaID)
	case err == nil && (existing.Size > 0 || len(existing.Blob) > 0):
		// The id already holds content. M_CANNOT_OVERWRITE_MEDIA is the code the
		// specification gives this collision: the media repository never replaces a
		// file, so an id that was used once is permanently taken and a client that
		// reuses one must mint a new id rather than retry.
		return matrix.ErrConflict(matrix.ErrCodeCannotOverwrite, "media id %s already has content", mediaID)
	}
	return s.db.MediaRepo.Save(&persist.Media{
		MediaID:     mediaID,
		Origin:      s.serverName,
		UserID:      userID,
		FileName:    filename,
		ContentType: contentType,
		Size:        int64(len(data)),
		Blob:        data,
	})
}

// Media returns stored content by origin and media id.
func (s *Service) Media(ctx context.Context, origin, mediaID string) (*persist.Media, error) {
	if mediaID == "" {
		return nil, matrix.ErrNotFound("no such media id")
	}
	if origin != s.serverName {
		return nil, matrix.ErrNotFound("media %s is not hosted by this server", mediaID)
	}
	media, err := s.db.MediaRepo.Get(origin, mediaID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no such media id")
		}
		return nil, matrix.ErrInternal(err)
	}
	if media.Size == 0 && len(media.Blob) == 0 {
		// A media id minted by POST /create whose bytes have not arrived yet. The
		// reservation is real, so the honest answer is "not yet" (504) rather than
		// "does not exist": a client that uploads a room avatar and reads it back is
		// racing its own PUT, and a retry is the only recovery that works.
		return nil, matrix.ErrNotYetUploaded("media %s has no content yet", mediaID)
	}
	return media, nil
}

// Thumbnail scales an image to fit the requested box. method is "scale" or
// "crop". Non-image content yields a not found error, which is how homeservers
// report missing thumbnails.
func (s *Service) Thumbnail(ctx context.Context, origin, mediaID string, width, height int, method string) ([]byte, string, error) {
	media, err := s.Media(ctx, origin, mediaID)
	if err != nil {
		return nil, "", err
	}
	if width <= 0 || height <= 0 {
		return nil, "", matrix.ErrInvalidParam("width and height must be positive")
	}
	src, _, err := image.Decode(bytes.NewReader(media.Blob))
	if err != nil {
		return nil, "", matrix.ErrNotFound("media %s has no thumbnail", mediaID)
	}
	scaled := scaleImage(src, width, height, method == "crop")
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, "", matrix.ErrInternal(err)
	}
	return buf.Bytes(), "image/png", nil
}

// scaleImage resizes an image with a box filter, cropping or fitting to the
// requested size.
func scaleImage(src image.Image, width, height int, crop bool) image.Image {
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	targetW, targetH := width, height
	if !crop {
		if srcW >= srcH {
			targetH = srcH * width / srcW
			targetW = width
		} else {
			targetW = srcW * height / srcH
			targetH = height
		}
		if targetW < 1 {
			targetW = 1
		}
		if targetH < 1 {
			targetH = 1
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	for y := 0; y < targetH; y++ {
		for x := 0; x < targetW; x++ {
			srcX := bounds.Min.X + x*srcW/targetW
			srcY := bounds.Min.Y + y*srcH/targetH
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}

// ParseMediaSize reads the width, height and method query parameters of a
// thumbnail request.
func ParseMediaSize(values map[string]string) (width, height int, method string) {
	width = 320
	height = 240
	method = "scale"
	if value, err := strconv.Atoi(values["width"]); err == nil && value > 0 {
		width = value
	}
	if value, err := strconv.Atoi(values["height"]); err == nil && value > 0 {
		height = value
	}
	if value, ok := values["method"]; ok && value != "" {
		method = value
	}
	return width, height, method
}

// MXC parses an mxc:// URI into origin and media id.
func MXC(uri string) (origin, mediaID string, ok bool) {
	if !strings.HasPrefix(uri, "mxc://") {
		return "", "", false
	}
	rest := strings.TrimPrefix(uri, "mxc://")
	origin, mediaID, found := strings.Cut(rest, "/")
	if !found || origin == "" || mediaID == "" {
		return "", "", false
	}
	return origin, mediaID, true
}
