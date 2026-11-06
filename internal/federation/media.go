package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/enimatek/lattice/internal/media"
)

// MediaFetcher obtains content that belongs to another server, for this server's content repository.
//
// A server asking another server for a file is a federation request like any other: it is signed, and the
// answer is untrusted input. That is worth stating because a media download looks like an ordinary file
// fetch and invites the shortcut of trusting the other side's headers, sizes and redirects.
type MediaFetcher struct {
	client *Client
	log    *slog.Logger
}

// NewMediaFetcher wires the fetcher to a federation client.
func NewMediaFetcher(client *Client, log *slog.Logger) *MediaFetcher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &MediaFetcher{client: client, log: log}
}

// FetchMedia asks `server` for the bytes behind one of its media identifiers.
//
// The Server-Server API requires content to be fetched from the server named in the `mxc://` URI, and the
// authenticated federation endpoint is tried first. If it does not produce content, the deprecated
// unauthenticated endpoint is tried once, with `allow_remote=false`.
//
// The fallback is taken on any unsuccessful answer rather than only on `404 M_UNRECOGNIZED`, which is a
// decision worth recording. Servers explain a missing endpoint in several ways — a 404 whose body is not
// JSON, a 405, and on 2026-11-13, Complement's own federation fixture answering 400 for its authenticated
// media route — and a homeserver that insists on one shape refuses to fetch content from servers that would
// happily serve it. What is not widened is where the request goes: only the origin server named in the URI is
// asked, through this server's own resolver and dialer, so the second attempt costs a request and buys an
// attacker nothing.
//
// maxBytes is a hard bound rather than a hint: content from another server is fetched into memory here,
// and a bound that is checked after the fact is a bound that was already exceeded.
func (f *MediaFetcher) FetchMedia(ctx context.Context, server, mediaID string, maxBytes int64) (media.RemoteContent, error) {
	if maxBytes <= 0 {
		return media.RemoteContent{}, errors.New("federation: a media fetch needs a size bound")
	}
	target := "/_matrix/federation/v1/media/download/" + mediaID
	response, header, err := f.client.GetRaw(ctx, server, target, maxBytes+1)
	if err != nil {
		return media.RemoteContent{}, err
	}
	if successful(response) {
		return contentFrom(server, response, header, maxBytes)
	}
	firstFailure := remoteMediaFailure(server, response).Error()

	// The authenticated endpoint did not produce content, so the deprecated one is tried once. The
	// `allow_remote=false` is required by the specification and is not decoration: without it a server asked
	// for content it does not hold would ask that content's origin server, and when this server is the one
	// asking, that is itself — which is how one download turns into a request to itself.
	fallback := "/_matrix/media/v3/download/" + server + "/" + mediaID + "?allow_remote=false"
	f.log.Debug("federation: the media endpoint did not answer, trying the deprecated one",
		"server", server, "media_id", mediaID, "first_answer", firstFailure)
	response, header, err = f.client.GetRaw(ctx, server, fallback, maxBytes+1)
	if err != nil {
		return media.RemoteContent{}, err
	}
	if !successful(response) {
		// A peer that gave a specific reason for refusing is reported as it spoke, because "that content is
		// not here" is worth more to an operator than a summary of two attempts. A peer that answered
		// something unrecognisable gets both attempts named.
		if definitive(response) {
			return media.RemoteContent{}, remoteMediaFailure(server, response)
		}
		return media.RemoteContent{}, fmt.Errorf("federation: %s: %s (the deprecated endpoint said: %s)",
			server, firstFailure, remoteMediaFailure(server, response).Error())
	}
	return contentFrom(server, response, header, maxBytes)
}

// successful says a peer's answer is one to read rather than to explain.
func successful(response Response) bool {
	return response.StatusCode >= 200 && response.StatusCode <= 299
}

// contentFrom applies the size bound and reads the answer.
func contentFrom(server string, response Response, header http.Header, maxBytes int64) (media.RemoteContent, error) {
	if int64(len(response.Body)) > maxBytes {
		return media.RemoteContent{}, fmt.Errorf("federation: %s: %w", server, media.ErrTooLargeRemote)
	}
	return parseRemoteMedia(header, response.Body)
}

// definitive says a peer gave a reason this server should pass on rather than paper over: the content is
// known not to be there, is not this server's to have, or is too large to fetch.
func definitive(response Response) bool {
	var problem struct {
		ErrCode string `json:"errcode"`
	}
	if err := json.Unmarshal(response.Body, &problem); err != nil {
		return false
	}
	return problem.ErrCode == "M_NOT_FOUND" || problem.ErrCode == "M_FORBIDDEN" ||
		problem.ErrCode == "M_TOO_LARGE" || problem.ErrCode == "M_NOT_YET_UPLOADED"
}

// remoteMediaFailure turns another server's refusal into an error that names what it said, because "the
// remote server has no such content" and "the remote server refuses to serve it" are different things for
// an operator to read.
func remoteMediaFailure(server string, response Response) error {
	var problem struct {
		ErrCode string `json:"errcode"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(response.Body, &problem)
	if problem.ErrCode != "" {
		return fmt.Errorf("federation: %s answered %s for media: %s", server, problem.ErrCode, problem.Error)
	}
	return fmt.Errorf("federation: %s answered HTTP %d for media", server, response.StatusCode)
}

// parseRemoteMedia turns a peer's answer into content this server can store.
func parseRemoteMedia(header http.Header, body []byte) (media.RemoteContent, error) {
	part, err := media.ParseMultipartResponse(header, body)
	if err != nil {
		return media.RemoteContent{}, fmt.Errorf("federation: %w", err)
	}
	if part.Location != "" {
		return media.RemoteContent{}, fmt.Errorf(
			"federation: the peer pointed the media at %s, and this server does not fetch a URL it was handed by a peer",
			part.Location)
	}
	return media.RemoteContent{
		Bytes:       part.Content,
		ContentType: part.ContentType,
		FileName:    part.FileName,
	}, nil
}
