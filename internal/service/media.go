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

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// DefaultMaxUploadSize is the media upload limit (10 MiB).
const DefaultMaxUploadSize = 10 * 1024 * 1024

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

// ReserveMedia handles POST /_matrix/media/v1/create, which mints a media id
// and MXC URI before the client uploads the bytes.
func (s *Service) ReserveMedia(ctx context.Context, userID, filename, contentType string, size int64) (matrix.Object, error) {
	if size > s.maxUploadSize {
		return nil, matrix.NewError(matrix.ErrCodeLimit, 413,
			"file too large, maximum "+strconv.FormatInt(s.maxUploadSize, 10)+" bytes")
	}
	mediaID, err := s.UploadMedia(ctx, userID, filename, contentType, nil)
	if err != nil {
		return nil, err
	}
	return matrix.Object{
		"content_uri": "mxc://" + s.serverName + "/" + mediaID,
	}, nil
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
		return matrix.ErrConflict(matrix.ErrCodeUnknown, "media id already exists")
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
