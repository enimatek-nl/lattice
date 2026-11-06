package media

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/media"
	"github.com/enimatek/lattice/internal/persist"
)

// handleUpload is POST /_matrix/media/v3/upload: the body is the file, and the response is the URI it will
// be served from.
//
// The limit that refuses an oversized upload is the smaller of the content repository's own bound and the
// HTTP layer's body bound. Reporting one and enforcing the other would be a lie a client finds out about by
// having a 1 MiB upload cut off at 1 MiB while the server advertised 10.
func (d *Deps) handleUpload(w http.ResponseWriter, r *http.Request) {
	if d.Media == nil {
		d.unavailable(w)
		return
	}
	limit := d.effectiveUploadLimit()
	body := http.MaxBytesReader(w, r.Body, limit)
	uri, err := d.Media.Upload(r.Context(), auth.UserID(r),
		r.URL.Query().Get("filename"), contentTypeOf(r), body)
	if err != nil {
		d.serviceError(w, err, limit)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"content_uri": uri})
}

// handleCreate is POST /_matrix/media/v1/create: reserve a URI now and put the bytes in later, which is
// what lets a client show an upload as having started before the file has finished arriving.
func (d *Deps) handleCreate(w http.ResponseWriter, r *http.Request) {
	if d.Media == nil {
		d.unavailable(w)
		return
	}
	uri, expiresAt, err := d.Media.Create(r.Context(), auth.UserID(r))
	if err != nil {
		d.serviceError(w, err, d.effectiveUploadLimit())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"content_uri":       uri,
		"unused_expires_at": expiresAt,
	})
}

// handleUploadToURI is PUT /_matrix/media/v3/upload/{serverName}/{mediaId}: fill in a reservation.
func (d *Deps) handleUploadToURI(w http.ResponseWriter, r *http.Request) {
	if d.Media == nil {
		d.unavailable(w)
		return
	}
	limit := d.effectiveUploadLimit()
	body := http.MaxBytesReader(w, r.Body, limit)
	uri, err := d.Media.UploadToURI(r.Context(), auth.UserID(r),
		r.PathValue("serverName"), r.PathValue("mediaId"),
		r.URL.Query().Get("filename"), contentTypeOf(r), body)
	if err != nil {
		d.serviceError(w, err, limit)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"content_uri": uri})
}

// handleConfig is GET /_matrix/media/v3/config.
func (d *Deps) handleConfig(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"m.upload.size": d.effectiveUploadLimit()})
}

// handleDownload and handleDownloadNamed are the two forms of the download endpoint. The named form is in
// the specification (and marked deprecated) rather than a query parameter, because the file name a browser
// puts in a `save as` dialog comes from the URL it was given.
func (d *Deps) handleDownload(w http.ResponseWriter, r *http.Request) {
	d.serve(w, r, "")
}

func (d *Deps) handleDownloadNamed(w http.ResponseWriter, r *http.Request) {
	d.serve(w, r, r.PathValue("fileName"))
}

func (d *Deps) serve(w http.ResponseWriter, r *http.Request, namedFile string) {
	if d.Media == nil {
		d.unavailable(w)
		return
	}
	opened, err := d.Media.Open(r.Context(),
		r.PathValue("serverName"), r.PathValue("mediaId"),
		httpx.QueryBool(r, "allow_remote", true), httpx.QueryInt64(r, "timeout_ms", 0))
	if err != nil {
		d.serviceError(w, err, d.effectiveUploadLimit())
		return
	}
	file, err := os.Open(opened.Path)
	if err != nil {
		if os.IsNotExist(err) {
			matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeNotFound,
				"This server holds no such content."))
			return
		}
		d.log().Error("media: a stored file could not be opened", "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The content could not be read."))
		return
	}
	defer func() { _ = file.Close() }()

	contentType := opened.Metadata.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	fileName := namedFile
	if fileName == "" {
		fileName = opened.Metadata.UploadName
	}
	setServingHeaders(w, contentType, media.DispositionFor(contentType), fileName)
	serveContent(w, r, contentType, statModTime(opened.Path, opened.Metadata), file)
}

// handleThumbnail answers a thumbnail request on either surface.
// statModTime is when the stored bytes last changed, which is what a client's conditional request is
// compared against. A file that has gone since the metadata was read leaves the row's own timestamp.
func statModTime(path string, metadata persist.MediaMetadata) time.Time {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.UnixMilli(metadata.CreatedAt)
}

func (d *Deps) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	if d.Media == nil {
		d.unavailable(w)
		return
	}
	width, widthOK := httpx.QueryInt(r, "width")
	height, heightOK := httpx.QueryInt(r, "height")
	if !widthOK || !heightOK {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A thumbnail request needs a width and a height."))
		return
	}
	thumbnail, err := d.Media.Thumbnail(r.Context(),
		r.PathValue("serverName"), r.PathValue("mediaId"),
		media.ThumbnailRequest{
			Width:    width,
			Height:   height,
			Method:   r.URL.Query().Get("method"),
			Animated: httpx.QueryBool(r, "animated", false),
		},
		httpx.QueryBool(r, "allow_remote", true), httpx.QueryInt64(r, "timeout_ms", 0))
	if err != nil {
		d.serviceError(w, err, d.effectiveUploadLimit())
		return
	}
	// A thumbnail is named after the thing it thumbnails when the uploader gave it a name: a client saving
	// a thumbnail of "sunset.jpg" should not end up with a file called "thumbnail".
	setServingHeaders(w, thumbnail.ContentType, media.DispositionFor(thumbnail.ContentType), thumbnail.SourceName)
	serveContent(w, r, thumbnail.ContentType, time.Now(), bytes.NewReader(thumbnail.Bytes))
}

// setServingHeaders carries the two headers the specification asks for on served content: a disposition,
// and a policy that says the bytes are data and not a document.
//
// The content security policy matters more than it looks. Media is served from the homeserver's own origin,
// so an HTML file uploaded as `text/html` and shown inline is a script running with the homeserver's
// cookies. The disposition rule (attachment for anything not on the allow-list) closes that, and the policy
// is the second lock.
func setServingHeaders(w http.ResponseWriter, contentType, disposition, fileName string) {
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", media.ContentDisposition(disposition, fileName))
	header.Set("Content-Security-Policy", media.RecommendedCSP)
	header.Set("Cross-Origin-Resource-Policy", "cross-origin")
	// A deployment that puts a shared cache in front of this server has to decide what may be cached and
	// for how long; guessing on its behalf from here would be a policy stated in the wrong place.
	header.Set("Cache-Control", "private")
}

// serveContent hands the bytes over, with range requests honoured so a client can resume a large video.
func serveContent(w http.ResponseWriter, r *http.Request, contentType string, modTime time.Time, seek io.ReadSeeker) {
	http.ServeContent(w, r, fileNameFor(contentType), modTime, seek)
}

// fileNameFor gives ServeContent something to infer a type from. The type is already set explicitly, so
// this only stops ServeContent from second-guessing it from an extension it does not have.
func fileNameFor(contentType string) string {
	switch media.CanonicalType(contentType) {
	case "image/png":
		return "media.png"
	case "image/jpeg":
		return "media.jpg"
	case "image/gif":
		return "media.gif"
	case "text/plain":
		return "media.txt"
	default:
		return "media"
	}
}

// effectiveUploadLimit is the largest upload this server will actually take end to end.
func (d *Deps) effectiveUploadLimit() int64 {
	limits := []int64{}
	if d.Cfg != nil {
		if d.Cfg.Media.MaxUploadBytes > 0 {
			limits = append(limits, d.Cfg.Media.MaxUploadBytes)
		}
		if d.Cfg.HTTP.MaxBodyBytes > 0 {
			limits = append(limits, d.Cfg.HTTP.MaxBodyBytes)
		}
	}
	if d.Media != nil {
		if cfg := d.Media.Configuration(); cfg.MaxUploadBytes > 0 {
			limits = append(limits, cfg.MaxUploadBytes)
		}
	}
	limit := int64(0)
	for _, candidate := range limits {
		if limit == 0 || candidate < limit {
			limit = candidate
		}
	}
	return limit
}

// serviceError writes a content repository failure with the status the protocol prescribes. The one case
// worth spelling out is a body the HTTP layer cut short: that is the same refusal as an oversized upload,
// and it must not turn into a 500 because the cut happened in a middleware.
func (d *Deps) serviceError(w http.ResponseWriter, err error, uploadLimit int64) {
	// The HTTP layer cuts a body short at its own bound, and that cut arrives here as an ordinary read
	// failure with a type of its own. It is the same refusal as an oversized upload and must not become a
	// 500 because the truncation happened in a middleware.
	var truncated *http.MaxBytesError
	if errors.As(err, &truncated) {
		limit := uploadLimit
		if truncated.Limit > 0 {
			limit = truncated.Limit
		}
		matrixerr.Write(w, http.StatusRequestEntityTooLarge, matrixerr.Newf(matrixerr.CodeTooLarge,
			"This server accepts content up to %d bytes.", limit))
		return
	}
	if _, isProtocol := matrixerr.As(err); !isProtocol {
		d.log().Error("media: the content repository failed", "error", err.Error())
	}
	matrixerr.WriteServiceError(w, err)
}

func (d *Deps) unavailable(w http.ResponseWriter) {
	d.log().Error("media: the content repository is not running")
	matrixerr.Write(w, http.StatusNotImplemented, matrixerr.New(matrixerr.CodeUnknown,
		"This server is not running a content repository."))
}

func (d *Deps) log() *slog.Logger {
	if d.Log == nil {
		return slog.Default()
	}
	return d.Log
}

// writeUnrecognized answers an endpoint this server does not implement, in the form a peer is told to read
// as "do not ask again".
func writeUnrecognized(w http.ResponseWriter, message string) {
	matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeUnrecognized, message))
}

// contentTypeOf reads the declared type of an upload. An absent type is kept as empty rather than guessed
// at: what the uploader said is what a downloader is told, and a server that invents a type is deciding what
// a client will render.
func contentTypeOf(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Content-Type"))
	if value == "" {
		return "application/octet-stream"
	}
	return value
}
