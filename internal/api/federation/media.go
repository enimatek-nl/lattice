package federation

import (
	"bytes"
	"net/http"
	"os"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/media"
)

// Media endpoints a server answers for its own content:
// `GET /_matrix/federation/v1/media/download/{mediaId}` and
// `GET /_matrix/federation/v1/media/thumbnail/{mediaId}`.
//
// Both are signed requests, which is the reason they exist: an authenticated server-to-server channel
// replaced the open `/_matrix/media/*` downloads, and a server cannot present an access token because it is
// not a user.
//
// Both answer `multipart/mixed` with exactly two parts — a JSON metadata part, which the specification says
// is currently always empty, and the content — because that is the shape the caller is told to expect.
// Getting it wrong is not a formatting quibble: a caller that cannot find the content part has no content.
//
// Only content this server holds is served. Serving content this server happens to have *cached* from
// somebody else would turn this server into a proxy for whatever a peer asks for, under a name that is not
// this server's; the specification tells the caller to ask the origin server, and that is who gets asked.

// handleMediaDownload answers a peer's request for one of this server's files.
func (d *Deps) handleMediaDownload(w http.ResponseWriter, r *http.Request) {
	if d.Media == nil {
		d.mediaUnavailable(w)
		return
	}
	opened, err := d.Media.Open(r.Context(), d.Cfg.ServerName, r.PathValue("mediaId"), false,
		httpx.QueryInt64(r, "timeout_ms", 0))
	if err != nil {
		d.mediaError(w, err)
		return
	}
	file, err := os.Open(opened.Path)
	if err != nil {
		if os.IsNotExist(err) {
			matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeNotFound,
				"This server holds no such content."))
			return
		}
		d.Log.Error("federation: a media file could not be opened", "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The content could not be read."))
		return
	}
	defer func() { _ = file.Close() }()

	contentType := opened.Metadata.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := media.WriteMultipartResponse(w, contentType, opened.Metadata.UploadName, file); err != nil {
		d.Log.Error("federation: the media answer could not be written", "error", err)
	}
}

// handleMediaThumbnail answers a peer's request for a thumbnail of one of this server's files.
//
// The thumbnail is generated here rather than fetched from a third party: a thumbnail this server signs an
// answer about is this server's statement about the content, and passing through somebody else's reduced
// copy would be presenting it as this server's work.
func (d *Deps) handleMediaThumbnail(w http.ResponseWriter, r *http.Request) {
	if d.Media == nil {
		d.mediaUnavailable(w)
		return
	}
	width, widthOK := httpx.QueryInt(r, "width")
	height, heightOK := httpx.QueryInt(r, "height")
	if !widthOK || !heightOK {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A thumbnail request needs a width and a height."))
		return
	}
	thumbnail, err := d.Media.Thumbnail(r.Context(), d.Cfg.ServerName, r.PathValue("mediaId"),
		media.ThumbnailRequest{
			Width:    width,
			Height:   height,
			Method:   r.URL.Query().Get("method"),
			Animated: httpx.QueryBool(r, "animated", false),
		}, false, httpx.QueryInt64(r, "timeout_ms", 0))
	if err != nil {
		d.mediaError(w, err)
		return
	}
	if err := media.WriteMultipartResponse(w, thumbnail.ContentType, thumbnail.SourceName,
		bytes.NewReader(thumbnail.Bytes)); err != nil {
		d.Log.Error("federation: the thumbnail answer could not be written", "error", err)
	}
}

// mediaError writes a content repository failure to a peer, keeping the code the specification defines for
// this situation so the caller can tell "no such content" from "not uploaded yet" from "too large".
func (d *Deps) mediaError(w http.ResponseWriter, err error) {
	if _, isProtocol := matrixerr.As(err); !isProtocol {
		d.Log.Error("federation: the content repository failed", "error", err.Error())
	}
	matrixerr.WriteServiceError(w, err)
}

func (d *Deps) mediaUnavailable(w http.ResponseWriter) {
	d.Log.Error("federation: the content repository is not running")
	matrixerr.Write(w, http.StatusNotImplemented, matrixerr.New(matrixerr.CodeUnknown,
		"This server is not running a content repository."))
}
