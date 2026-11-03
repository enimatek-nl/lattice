package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// FederationMedia is the media this server hosts, asked for by a peer over the server-server
// API. It is the local read with the origin filled in, because the federation endpoint names
// only the media id: a peer is by definition asking the server whose name is in the mxc URI it
// is holding.
func (s *Service) FederationMedia(ctx context.Context, mediaID string) (*persist.Media, error) {
	return s.Media(ctx, s.serverName, mediaID)
}

// FederationThumbnail is [Service.FederationMedia]'s thumbnail form: the box is given in the
// query, and a peer asking for a thumbnail of somebody else's content is asking the server that
// holds the original.
func (s *Service) FederationThumbnail(ctx context.Context, mediaID string, width, height int, method string) ([]byte, string, error) {
	return s.Thumbnail(ctx, s.serverName, mediaID, width, height, method)
}

// RemoteMedia fetches content this server does not hold from the server its mxc URI names, and
// keeps the copy.
//
// Caching is what makes the client API honest: the specification lets a client ask its own
// server for anybody's content, and a server that refused everything it did not host would push
// every client into talking to every other server - with their access token in the request, in
// front of a server that has no business seeing it.
func (s *Service) RemoteMedia(ctx context.Context, origin, mediaID string) (*persist.Media, error) {
	transport := s.federationTransport()
	if transport == nil {
		return nil, matrix.ErrNotFound("media %s is not hosted here, and this server does not federate", mediaID)
	}
	if !matrix.ValidServerName(origin) {
		return nil, matrix.ErrNotFound("%s is not a server name, so nobody can be asked for %s", origin, mediaID)
	}
	// The peer decides how big its own content is, so this server puts a ceiling on how much of
	// it it is willing to hold in memory rather than agreeing in advance to whatever arrives.
	header, raw, err := transport.GetRaw(ctx, origin,
		"/_matrix/federation/v1/media/download/"+url.PathEscape(mediaID), nil, s.MaxUploadSize())
	if err != nil {
		log.Printf("federation: asking %s for the media %s: %v", origin, mediaID, err)
		return nil, matrix.ErrNotFound("%s could not be asked for the media %s", origin, mediaID)
	}
	contentType, fileName, body, err := splitMediaMultipart(header, raw)
	if err != nil {
		return nil, matrix.ErrNotFound("%s answered the request for %s in a form this server cannot read", origin, mediaID)
	}
	media := &persist.Media{
		MediaID:     mediaID,
		Origin:      origin,
		ContentType: contentType,
		FileName:    fileName,
		Size:        int64(len(body)),
		Blob:        body,
	}
	if err := s.db.MediaRepo.Create(media); err != nil {
		// The copy did not make it to disk. The content is still answerable for this one
		// request, and a peer that asked twice is not owed a second fetch: the next request
		// will ask again and probably succeed.
		log.Printf("federation: the copy of %s fetched from %s could not be stored: %v", mediaID, origin, err)
	}
	return media, nil
}

// RemoteThumbnail asks the server that holds a piece of content for a thumbnail of it.
//
// The peer's answer is passed on rather than re-made here. Scaling content this server does not
// hold would mean fetching the whole original for every thumbnail request, and the peer has
// already looked at the file once: what it produced is a thumbnail of the same content, which is
// what the client asked for.
func (s *Service) RemoteThumbnail(ctx context.Context, origin, mediaID string, width, height int, method string) ([]byte, string, error) {
	transport := s.federationTransport()
	if transport == nil {
		return nil, "", matrix.ErrNotFound("media %s is not hosted here, and this server does not federate", mediaID)
	}
	if !matrix.ValidServerName(origin) {
		return nil, "", matrix.ErrNotFound("%s is not a server name, so nobody can be asked for %s", origin, mediaID)
	}
	query := url.Values{
		"width":  []string{fmt.Sprintf("%d", width)},
		"height": []string{fmt.Sprintf("%d", height)},
		"method": []string{method},
	}
	header, raw, err := transport.GetRaw(ctx, origin,
		"/_matrix/federation/v1/media/thumbnail/"+url.PathEscape(mediaID), query, s.MaxUploadSize())
	if err != nil {
		log.Printf("federation: asking %s for a thumbnail of %s: %v", origin, mediaID, err)
		return nil, "", matrix.ErrNotFound("%s could not be asked for a thumbnail of %s", origin, mediaID)
	}
	contentType, _, body, err := splitMediaMultipart(header, raw)
	if err != nil {
		return nil, "", matrix.ErrNotFound("%s answered the thumbnail request for %s in a form this server cannot read", origin, mediaID)
	}
	return body, contentType, nil
}

// splitMediaMultipart reads the two parts a peer's media answer is made of: a JSON description
// and the content itself, in that order, which is what the specification asks for and what every
// server in the wild sends.
//
// An answer that is not multipart at all is accepted as the content with the media type the
// response header names. Servers have sent media that way for years, and a client that is told
// "your peer is badly formed" instead of being shown the picture has lost something either way.
func splitMediaMultipart(header http.Header, raw []byte) (contentType, fileName string, body []byte, err error) {
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		contentType := header.Get("Content-Type")
		if contentType == "" {
			return "", "", nil, fmt.Errorf("the answer carries neither a multipart boundary nor a media type")
		}
		return contentType, "", raw, nil
	}
	reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	first, err := reader.NextPart()
	if err != nil {
		return "", "", nil, fmt.Errorf("the description part is missing: %w", err)
	}
	description, err := io.ReadAll(io.LimitReader(first, maxMediaDescription))
	if err != nil {
		return "", "", nil, fmt.Errorf("reading the description part: %w", err)
	}
	_ = first.Close()
	object, err := decodeJSONObject(string(description))
	if err != nil {
		return "", "", nil, fmt.Errorf("the description part is not a JSON object: %w", err)
	}
	// The two endpoints describe their first part differently: a download names media_type and
	// upload_name, a thumbnail names content_type. Reading both spellings is not leniency, it is
	// the fact that they are different endpoints.
	for _, field := range []string{"media_type", "content_type"} {
		if value, ok := object[field].(string); ok && value != "" {
			contentType = value
		}
	}
	if value, ok := object["upload_name"].(string); ok {
		fileName = value
	}
	second, err := reader.NextPart()
	if err != nil {
		return "", "", nil, fmt.Errorf("the content part is missing: %w", err)
	}
	defer func() { _ = second.Close() }()
	body, err = io.ReadAll(second)
	if err != nil {
		return "", "", nil, fmt.Errorf("reading the content part: %w", err)
	}
	if contentType == "" {
		contentType = second.Header.Get("Content-Type")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType, fileName, body, nil
}

// maxMediaDescription caps the JSON part of a peer's media answer. It describes a file, so a
// kilobyte is already generous, and an answer that spends its first part on something else is
// not a description this server has any use for.
const maxMediaDescription = 4 << 10
