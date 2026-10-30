package api

import (
	"io"
	"net/http"
	"strconv"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// handleMediaUpload implements POST /_matrix/media/v3/upload and the PUT form
// used for reserved media IDs.
//
//	@Summary	Upload media
//	@Tags		media
//	@Accept		application/octet-stream
//	@Produce	json
//	@Param		serverName	path	string	false	"Reserved media server name"
//	@Param		mediaId		path	string	false	"Reserved media ID"
//	@Success	200			{object}	map[string]string
//	@Router		/_matrix/media/v3/upload [post]
func (a *App) handleMediaUpload(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r)
	data, err := a.readUploadBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	filename := queryValue(r, "filename", "")
	contentType := r.Header.Get("Content-Type")

	mediaID := r.PathValue("mediaId")
	if mediaID == "" {
		mediaID, err = a.svc.UploadMedia(r.Context(), userID, filename, contentType, data)
	} else {
		err = a.svc.UploadToReservedMedia(r.Context(), userID, mediaID, filename, contentType, data)
	}
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"content_uri": "mxc://" + a.svc.ServerName() + "/" + mediaID,
	})
}

// handleMediaCreate implements POST /_matrix/media/v3/create.
//
//	@Summary	Reserve a media ID
//	@Tags		media
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	matrix.Object
//	@Router		/_matrix/media/v3/create [post]
func (a *App) handleMediaCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string `json:"name"`
		Type          string `json:"type"`
		Size          int64  `json:"size"`
		ContentLength int64  `json:"content_length"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	size := body.Size
	if size == 0 {
		size = body.ContentLength
	}
	reservation, err := a.svc.ReserveMedia(r.Context(), auth.GetUserID(r), body.Name, body.Type, size)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reservation)
}

// handleMediaDownload implements GET /_matrix/media/v3/download/{serverName}/{mediaId}
// and the authenticated /_matrix/client/v1/media variant.
//
//	@Summary	Download media
//	@Tags		media
//	@Produce	application/octet-stream
//	@Param		serverName	path	string	true	"Media origin"
//	@Param		mediaId		path	string	true	"Media ID"
//	@Success	200			{file}	binary
//	@Router		/_matrix/client/v1/media/download/{serverName}/{mediaId} [get]
func (a *App) handleMediaDownload(w http.ResponseWriter, r *http.Request) {
	media, err := a.svc.Media(r.Context(), r.PathValue("serverName"), r.PathValue("mediaId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if media.ContentType != "" {
		w.Header().Set("Content-Type", media.ContentType)
	}
	if media.FileName != "" {
		w.Header().Set("Content-Disposition", contentDisposition(media.FileName))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(media.Blob)), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(media.Blob)
}

// handleMediaThumbnail implements GET /_matrix/media/v3/thumbnail/{serverName}/{mediaId}.
//
//	@Summary	Download a media thumbnail
//	@Tags		media
//	@Produce	image/png
//	@Success	200	{file}	binary
//	@Router		/_matrix/media/v3/thumbnail/{serverName}/{mediaId} [get]
func (a *App) handleMediaThumbnail(w http.ResponseWriter, r *http.Request) {
	width, err := queryInt(r, "width", 128)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	height, err := queryInt(r, "height", 128)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	data, contentType, err := a.svc.Thumbnail(r.Context(), r.PathValue("serverName"), r.PathValue("mediaId"),
		width, height, queryValue(r, "method", "scale"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleMediaConfig implements GET /_matrix/media/v3/config.
//
//	@Summary	Read media limits
//	@Tags		media
//	@Produce	json
//	@Success	200	{object}	matrix.Object
//	@Router		/_matrix/media/v3/config [get]
func (a *App) handleMediaConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.svc.MediaConfig())
}

// handleMediaPreviewURL implements GET /_matrix/media/v3/preview_url. URL
// previews need an outbound fetcher, which Lattice does not have.
func (a *App) handleMediaPreviewURL(w http.ResponseWriter, r *http.Request) {
	a.writeError(w, r, matrix.ErrNotImplemented("url previews"))
}

// readUploadBody reads an upload body, enforcing the configured size limit.
func (a *App) readUploadBody(r *http.Request) ([]byte, error) {
	limit := a.svc.MaxUploadSize()
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, matrix.ErrNotJSON("could not read the upload body")
	}
	if int64(len(body)) > limit {
		return nil, matrix.ErrTooLarge(limit)
	}
	return body, nil
}

// contentDisposition renders a safe Content-Disposition header.
func contentDisposition(filename string) string {
	return `inline; filename="` + escapeHeader(filename) + `"`
}

// escapeHeader removes characters that would break a HTTP header value.
func escapeHeader(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range value {
		switch r {
		case '"', '\\', '\n', '\r':
			continue
		default:
			if r < 0x20 {
				continue
			}
			out = append(out, r)
		}
	}
	return string(out)
}
