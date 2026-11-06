package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"

	"github.com/enimatek/lattice/internal/matrixerr"
)

// Thumbnail is one generated thumbnail and what it is.
type Thumbnail struct {
	Bytes       []byte
	ContentType string
	// FromSource says the answer was the original content rather than a reduced copy of it, which is what
	// the specification asks for when the content is already smaller than what the client asked for.
	FromSource bool
	// SourceName is the file name the uploader gave the content being thumbnailed, so that a thumbnail can
	// be named after its original instead of being anonymous.
	SourceName string
}

// ThumbnailRequest is what a client asked for.
type ThumbnailRequest struct {
	Width  int
	Height int
	// Method is "scale" or "crop", the two the specification names.
	Method string
	// Animated is the client's preference for an animated answer. This server produces static thumbnails,
	// so a true here changes nothing except that an animated source is still reduced to its first frame —
	// which is the behaviour the specification prescribes for a server that cannot animate.
	Animated bool
}

// maxThumbnailDimension refuses an absurd request before anything is decoded. A client asking for a
// 100000x100000 thumbnail is asking for an allocation, not for a thumbnail.
const maxThumbnailDimension = 4096

// Thumbnail resolves content and reduces it.
//
// The bounds are checked before the bytes are read or decoded: the specification's advice is that a server
// should not try to thumbnail an image that is too large, because decoding is the expensive part and it
// happens in this server's memory. Local content that is too large is 413 M_TOO_LARGE and remote content
// is 502, the same distinction the download path makes between what the client sent and what a third party
// sent.
func (s *Service) Thumbnail(ctx context.Context, server, mediaID string, req ThumbnailRequest, allowRemote bool, timeoutMS int64) (Thumbnail, error) {
	opened, err := s.Open(ctx, server, mediaID, allowRemote, timeoutMS)
	if err != nil {
		return Thumbnail{}, err
	}
	if req.Width <= 0 || req.Height <= 0 {
		return Thumbnail{}, matrixerr.New(matrixerr.CodeInvalidParam,
			"A thumbnail request needs positive dimensions.")
	}
	if req.Width > maxThumbnailDimension || req.Height > maxThumbnailDimension {
		return Thumbnail{}, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"This server thumbnails up to %d by %d pixels.", maxThumbnailDimension, maxThumbnailDimension)
	}
	method := req.Method
	if method == "" {
		method = "scale"
	}
	if method != "scale" && method != "crop" {
		return Thumbnail{}, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"Unknown thumbnail method %q.", method)
	}
	if opened.Metadata.SizeBytes > s.cfg.MaxThumbnailSourceBytes {
		return tooLargeToThumbnail(opened.Metadata.Local, s.cfg.MaxThumbnailSourceBytes)
	}

	source, err := readCapped(opened.Path, s.cfg.MaxThumbnailSourceBytes)
	if err != nil {
		if errors.Is(err, errOverflow) {
			return tooLargeToThumbnail(opened.Metadata.Local, s.cfg.MaxThumbnailSourceBytes)
		}
		if os.IsNotExist(err) {
			return Thumbnail{}, matrixerr.New(matrixerr.CodeNotFound, "This server holds no such content.")
		}
		return Thumbnail{}, fmt.Errorf("media: the content to thumbnail could not be read: %w", err)
	}
	thumbnail, err := MakeThumbnail(source, opened.Metadata.ContentType, req.Width, req.Height, method)
	if err != nil {
		if errors.Is(err, ErrNotImage) {
			return Thumbnail{}, matrixerr.New(matrixerr.CodeUnknown,
				"Cannot generate thumbnails for the requested content.").WithStatus(400)
		}
		return Thumbnail{}, err
	}
	thumbnail.SourceName = opened.Metadata.UploadName
	return thumbnail, nil
}

// tooLargeToThumbnail is the refusal, with the status that says whose content it was: a client that sent an
// oversized image is told 413, a client that asked this server to proxy somebody else's is told 502.
func tooLargeToThumbnail(local bool, limit int64) (Thumbnail, error) {
	if local {
		return Thumbnail{}, matrixerr.Newf(matrixerr.CodeTooLarge,
			"This server thumbnails content up to %d bytes.", limit)
	}
	return Thumbnail{}, matrixerr.New(matrixerr.CodeTooLarge,
		"That content is too large for this server to thumbnail.").WithStatus(502)
}

// ErrNotImage says the content is not something this server knows how to reduce. The specification's
// answer for that is 400 M_UNKNOWN, "cannot generate thumbnails for the requested content".
var ErrNotImage = errors.New("media: the content is not an image this server can thumbnail")

// MakeThumbnail reduces image bytes to at most width by height.
//
// The rules it has to satisfy come from the Client-Server API's thumbnail section, and two of them pull
// against each other: "scale tries to return an image where either the width or the height is smaller than
// the requested size", and "servers must never return thumbnails smaller than the client's requested
// dimensions, unless the content being thumbnailed is smaller than the dimensions". Keeping an aspect ratio
// makes the first unavoidable along one axis, so this server fits the image inside the requested box — as
// every implementation does — and honours the second as far as it can: the limiting axis reaches the
// requested size, and when the source is smaller than the request along both axes the source is returned
// unchanged instead of enlarged. Upscaling is forbidden outright.
func MakeThumbnail(source []byte, contentType string, width, height int, method string) (Thumbnail, error) {
	if method == "" {
		method = "scale"
	}
	decoded, err := decodeImage(source, contentType)
	if err != nil {
		return Thumbnail{}, err
	}

	bounds := decoded.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return Thumbnail{}, ErrNotImage
	}

	// Already no bigger than what was asked for: hand back the original. The specification prefers this to
	// thumbnailing, and it spares a client a re-encoded copy of bytes it could have used as they were.
	if sourceWidth <= width && sourceHeight <= height {
		return Thumbnail{
			Bytes:       source,
			ContentType: originalContentType(contentType),
			FromSource:  true,
		}, nil
	}

	targetWidth, targetHeight, crop := fit(sourceWidth, sourceHeight, width, height, method)
	if targetWidth > sourceWidth {
		targetWidth = sourceWidth
	}
	if targetHeight > sourceHeight {
		targetHeight = sourceHeight
	}
	if targetWidth < 1 || targetHeight < 1 {
		return Thumbnail{}, ErrNotImage
	}

	return encode(reduce(decoded, bounds, targetWidth, targetHeight, crop), contentType)
}

// fit decides the size to produce. For "scale" the image is fitted inside the box, keeping its aspect. For
// "crop" the box's aspect is imposed, and the result is the requested size, capped at the source size by
// the caller so nothing is enlarged.
func fit(sourceWidth, sourceHeight, width, height int, method string) (int, int, bool) {
	if method == "crop" {
		return width, height, true
	}
	scale := minRatio(sourceWidth, sourceHeight, width, height)
	targetWidth := int(scale*float64(sourceWidth) + 0.5)
	targetHeight := int(scale*float64(sourceHeight) + 0.5)
	return max(targetWidth, 1), max(targetHeight, 1), false
}

// minRatio is the largest scale that keeps an image inside width by height.
func minRatio(sourceWidth, sourceHeight, width, height int) float64 {
	return min(float64(width)/float64(sourceWidth), float64(height)/float64(sourceHeight))
}

// reduce resamples the image, optionally cropping to the requested aspect first.
//
// The filter is a box average: each output pixel is the mean of the source area it covers, which is cheap
// and does not alias the way nearest-neighbour does when shrinking. The Go standard library has no
// resampler, so a content repository that wants thumbnails has to hold one — which is why this file exists
// rather than a dependency.
func reduce(src image.Image, bounds image.Rectangle, width, height int, crop bool) image.Image {
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	regionWidth, regionHeight := sourceWidth, sourceHeight
	offsetX, offsetY := 0, 0
	if crop {
		// The largest region with the requested aspect, centred on the image.
		target := float64(width) / float64(height)
		if float64(sourceWidth)/float64(sourceHeight) > target {
			if cropped := int(float64(sourceHeight) * target); cropped > 0 {
				regionWidth = cropped
				offsetX = (sourceWidth - cropped) / 2
			}
		} else if cropped := int(float64(sourceWidth) / target); cropped > 0 {
			regionHeight = cropped
			offsetY = (sourceHeight - cropped) / 2
		}
	}

	out := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		y0 := y * regionHeight / height
		y1 := (y+1)*regionHeight/height + 1
		y1 = min(max(y1, y0+1), regionHeight)
		for x := 0; x < width; x++ {
			x0 := x * regionWidth / width
			x1 := (x+1)*regionWidth/width + 1
			x1 = min(max(x1, x0+1), regionWidth)
			out.Set(x, y, average(src, bounds, offsetX, offsetY, x0, y0, x1, y1))
		}
	}
	return out
}

// average blends the source pixels one output pixel covers. The sum cannot overflow: each channel is at
// most 65535 and at most maxThumbnailDimension squared pixels contribute, which is far inside uint64.
func average(src image.Image, bounds image.Rectangle, offsetX, offsetY, x0, y0, x1, y1 int) color.RGBA64 {
	// Each channel is at most 0xffff, and at most maxThumbnailDimension squared pixels contribute, so the
	// sums stay far inside uint64.
	var r, g, b, a, count uint64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			// RGBA() is premultiplied, which is the correct space to average in: a transparent pixel
			// should not drag the colour of its neighbours towards black.
			rr, gg, bb, aa := src.At(bounds.Min.X+offsetX+x, bounds.Min.Y+offsetY+y).RGBA()
			r += uint64(rr)
			g += uint64(gg)
			b += uint64(bb)
			a += uint64(aa)
			count++
		}
	}
	if count == 0 {
		return color.RGBA64{}
	}
	return color.RGBA64{
		R: uint16(r / count),
		G: uint16(g / count),
		B: uint16(b / count),
		A: uint16(a / count),
	}
}

// decodeImage decodes the formats this server can thumbnail: the ones the Go standard library decodes
// without a third-party dependency. The specification leaves the supported types unspecified, so what
// matters is that an unsupported type is refused with the specification's error rather than served or
// guessed at.
//
// A GIF is reduced to its first frame. Producing an animated thumbnail would mean encoding APNG or WebP,
// neither of which the standard library encodes; the specification says a server that cannot animate should
// behave as though animation had not been asked for.
func decodeImage(source []byte, contentType string) (image.Image, error) {
	reader := bytes.NewReader(source)
	switch contentType {
	case "image/png":
		decoded, err := png.Decode(reader)
		if err != nil {
			return nil, ErrNotImage
		}
		return decoded, nil
	case "image/jpeg":
		decoded, err := jpeg.Decode(reader)
		if err != nil {
			return nil, ErrNotImage
		}
		return decoded, nil
	case "image/gif":
		decoded, err := gif.Decode(reader)
		if err != nil {
			return nil, ErrNotImage
		}
		return decoded, nil
	default:
		decoded, _, err := image.Decode(reader)
		if err != nil {
			return nil, ErrNotImage
		}
		return decoded, nil
	}
}

// encode writes a reduced image out. PNG is chosen for everything except content the uploader already
// declared photographic: it is lossless, so a diagram or a screenshot is not degraded into a blur of
// blocking artefacts.
func encode(img image.Image, contentType string) (Thumbnail, error) {
	var buffer bytes.Buffer
	if contentType == "image/jpeg" {
		if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 80}); err != nil {
			return Thumbnail{}, fmt.Errorf("media: the thumbnail could not be encoded: %w", err)
		}
		return Thumbnail{Bytes: buffer.Bytes(), ContentType: "image/jpeg"}, nil
	}
	if err := png.Encode(&buffer, img); err != nil {
		return Thumbnail{}, fmt.Errorf("media: the thumbnail could not be encoded: %w", err)
	}
	return Thumbnail{Bytes: buffer.Bytes(), ContentType: "image/png"}, nil
}

// originalContentType names the bytes handed back unchanged. An unrecognised type is reported as
// image/png only because the caller asked for an image; a client that receives bytes it cannot decode has
// been told what the upload claimed to be.
func originalContentType(contentType string) string {
	switch contentType {
	case "image/png", "image/jpeg", "image/gif":
		return contentType
	default:
		return "image/png"
	}
}

// readCapped reads a whole file that is expected to stay under a size, so the limit is stated where the
// read happens rather than assumed from a metadata row.
func readCapped(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	if int64(len(content)) > maxBytes {
		return nil, errOverflow
	}
	return content, nil
}
