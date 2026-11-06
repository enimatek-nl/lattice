// Package media implements the content repository: the place an `mxc://` URI points at.
//
// Two things are kept apart on purpose. The bytes are files under a directory on disk, and what the URI
// promises about them — that it exists, what it is called, what type it is, how big it is, who uploaded
// it — is a row in the database. A URI whose metadata exists but whose file has gone is reported as
// missing rather than served as an empty file, and a file with no metadata is not reachable through any
// URI at all.
//
// The specification's security section is the reason the identifier rules are strict. A media ID is used
// as a file name, and `mxc://127.0.0.1/../../../etc/passwd` is a real attack that a blacklist of `.` and
// `/` does not close. So a media ID is required to consist only of alphanumeric characters, `_` and `-`,
// and the server-name component is required to be a valid server name, both checked before either is
// used to name anything on disk.
package media

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// Config is how the content repository is told to behave.
type Config struct {
	// Path is the directory content is stored under.
	Path string
	// MaxUploadBytes refuses an upload whose body is bigger than this, with 413 M_TOO_LARGE. The
	// specification's advice is that a server should not store files that are too large rather than store
	// them and refuse to serve them.
	MaxUploadBytes int64
	// MaxRemoteBytes is the same bound applied to content fetched from another server; exceeding it is
	// 502 M_TOO_LARGE rather than 413, because the client did not send anything too large.
	MaxRemoteBytes int64
	// MaxThumbnailSourceBytes refuses to thumbnail content bigger than this. Thumbnailing decodes an image
	// into memory, so this is the bound that protects the server rather than the client's disk.
	MaxThumbnailSourceBytes int64
	// MaxPendingUploadsPerUser bounds how many `mxc://` URIs a user may have created and not yet filled.
	MaxPendingUploadsPerUser int64
	// PendingUploadTTL is how long a created-but-unfilled URI stays reserved.
	PendingUploadTTL time.Duration
	// RemoteFetchTimeout bounds one request to another server's content repository.
	RemoteFetchTimeout time.Duration
	// MaxTimeoutMS caps the `timeout_ms` a client may ask for.
	MaxTimeoutMS int64
}

// Defaults are the values used when a deployment does not name its own. They are deliberately modest:
// the content repository is the easiest way for a server to run out of disk.
func Defaults() Config {
	return Config{
		MaxUploadBytes:           10 << 20, // 10 MiB
		MaxRemoteBytes:           10 << 20,
		MaxThumbnailSourceBytes:  25 << 20, // 25 MiB
		MaxPendingUploadsPerUser: 100,
		PendingUploadTTL:         24 * time.Hour,
		RemoteFetchTimeout:       20 * time.Second,
		MaxTimeoutMS:             20000,
	}
}

// RemoteFetcher obtains content that belongs to another server. [federation.MediaFetcher] implements it
// with a signed server-server request; leaving it nil means this server does not fetch remote content and
// every request for somebody else's `mxc://` URI is refused.
type RemoteFetcher interface {
	// FetchMedia returns the bytes of one piece of content and what its owner says about it. The
	// implementation must not return more than MaxBytes; exceeding it is reported as ErrTooLargeRemote.
	FetchMedia(ctx context.Context, server, mediaID string, maxBytes int64) (RemoteContent, error)
}

// RemoteContent is what a fetch of another server's content produced.
type RemoteContent struct {
	Bytes       []byte
	ContentType string
	FileName    string
}

// ErrTooLargeRemote marks content from another server that this server will not hold. It reaches the API
// layer, which answers 502 M_TOO_LARGE: the refusal is about a third party's file, not about the request
// the client made.
var ErrTooLargeRemote = errors.New("media: remote content is too large")

// Service is the content repository.
type Service struct {
	db     *persist.Database
	cfg    Config
	origin string
	log    *slog.Logger
	fetch  RemoteFetcher
	now    func() time.Time
}

// New builds the service. cfg.Path must be a writable directory; it is created if it does not exist.
func New(db *persist.Database, cfg Config, origin string, log *slog.Logger, fetch RemoteFetcher) (*Service, error) {
	if cfg.Path == "" {
		return nil, errors.New("media: a content repository needs a directory")
	}
	defaults := Defaults()
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = defaults.MaxUploadBytes
	}
	if cfg.MaxRemoteBytes <= 0 {
		cfg.MaxRemoteBytes = defaults.MaxRemoteBytes
	}
	if cfg.MaxThumbnailSourceBytes <= 0 {
		cfg.MaxThumbnailSourceBytes = defaults.MaxThumbnailSourceBytes
	}
	if cfg.MaxPendingUploadsPerUser <= 0 {
		cfg.MaxPendingUploadsPerUser = defaults.MaxPendingUploadsPerUser
	}
	if cfg.PendingUploadTTL <= 0 {
		cfg.PendingUploadTTL = defaults.PendingUploadTTL
	}
	if cfg.RemoteFetchTimeout <= 0 {
		cfg.RemoteFetchTimeout = defaults.RemoteFetchTimeout
	}
	if cfg.MaxTimeoutMS <= 0 {
		cfg.MaxTimeoutMS = defaults.MaxTimeoutMS
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(cfg.Path, 0o750); err != nil {
		return nil, fmt.Errorf("media: cannot create the content repository directory: %w", err)
	}
	return &Service{
		db: db, cfg: cfg, origin: origin, log: log, fetch: fetch, now: time.Now,
	}, nil
}

// Configuration reports the settings in force, which the API needs in order to tell a client the limit it
// will actually enforce rather than one that was configured and then overridden.
func (s *Service) Configuration() Config { return s.cfg }

// SetRemote wires the fetcher used for content that belongs to another server. It is separate from New
// because federation is enabled in a later step than storage, and a server that does not federate still has
// a content repository for its own content.
func (s *Service) SetRemote(fetch RemoteFetcher) { s.fetch = fetch }

// WithClock replaces the service's notion of now. Tests use it to expire a pending upload without
// waiting for one.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Upload is what POST /_matrix/media/v3/upload does: take bytes, return the URI they will be served from.
func (s *Service) Upload(ctx context.Context, userID, fileName, contentType string, body io.Reader) (string, error) {
	mediaID, err := s.newMediaID()
	if err != nil {
		return "", err
	}
	if err := s.checkPendingBudget(ctx, userID); err != nil {
		return "", err
	}
	stored, err := s.store(ctx, s.origin, mediaID, fileName, contentType, userID, body)
	if err != nil {
		return "", err
	}
	return URI(stored.Origin, stored.MediaID), nil
}

// Create is POST /_matrix/media/v1/create: reserve a URI now, upload the bytes later. The reservation
// expires so that a client which creates URIs and abandons them cannot fill the repository with holes.
func (s *Service) Create(ctx context.Context, userID string) (uri string, expiresAt int64, err error) {
	s.purgeExpired(ctx)
	mediaID, err := s.newMediaID()
	if err != nil {
		return "", 0, err
	}
	if err := s.checkPendingBudget(ctx, userID); err != nil {
		return "", 0, err
	}
	now := s.now()
	expiresAt = now.Add(s.cfg.PendingUploadTTL).UnixMilli()
	metadata := &persist.MediaMetadata{
		Origin:           s.origin,
		MediaID:          mediaID,
		Local:            true,
		CreatedBy:        userID,
		CreatedAt:        now.UnixMilli(),
		PendingExpiresAt: expiresAt,
	}
	if err := s.db.Media.Put(ctx, metadata); err != nil {
		return "", 0, err
	}
	return URI(s.origin, mediaID), expiresAt, nil
}

// UploadToURI is PUT /_matrix/media/v3/upload/{serverName}/{mediaId}: fill in a URI that Create reserved.
//
// The URI must exist, must be this server's own, and must belong to the user uploading. Accepting a body
// for an address that was never reserved would let an uploader pick their own identifiers, including ones
// already in use; accepting one for another user's reservation would let a user overwrite content that is
// not theirs.
func (s *Service) UploadToURI(ctx context.Context, userID, server, mediaID, fileName, contentType string, body io.Reader) (string, error) {
	if err := ValidateOrigin(server, s.origin); err != nil {
		return "", err
	}
	if err := ValidateMediaID(mediaID); err != nil {
		return "", err
	}
	existing, err := s.db.Media.Get(ctx, server, mediaID)
	if err != nil {
		if errors.Is(err, persist.ErrNotFound) {
			return "", matrixerr.Newf(matrixerr.CodeNotFound,
				"No media identifier %s was created on this server.", mediaID)
		}
		return "", err
	}
	if !existing.Local {
		return "", matrixerr.New(matrixerr.CodeForbidden, "That content belongs to another server.")
	}
	if existing.CreatedBy != userID {
		return "", matrixerr.New(matrixerr.CodeForbidden, "That media identifier was created by another user.")
	}
	if existing.UploadedAt != 0 {
		// The specification gives this its own code and status: the address already has content, so a second
		// upload is refused rather than replacing bytes somebody else already holds a link to. M_FORBIDDEN
		// would suggest the caller is the wrong person, which is a different fact and the wrong lead.
		return "", matrixerr.New(matrixerr.CodeCannotOverwriteMedia,
			"That media identifier already has content.")
	}
	stored, err := s.store(ctx, server, mediaID, fileName, contentType, userID, body)
	if err != nil {
		return "", err
	}
	return URI(stored.Origin, stored.MediaID), nil
}

// Opened is a piece of content this server is willing to serve, and where its bytes are.
type Opened struct {
	Metadata persist.MediaMetadata
	Path     string
}

// Open resolves an `mxc://` component pair to bytes on disk, fetching from the origin server if this
// server does not hold the content and is allowed to.
//
// allowRemote is the client's protection against a routing loop: a server asked for somebody else's
// content with allow_remote=false must not go and ask, because that is how a request ends up circulating
// between servers forever. Refusing with M_NOT_FOUND rather than silently fetching is what makes the
// parameter mean anything.
func (s *Service) Open(ctx context.Context, server, mediaID string, allowRemote bool, timeoutMS int64) (Opened, error) {
	if err := ValidateOrigin(server, s.origin); err != nil {
		return Opened{}, err
	}
	if err := ValidateMediaID(mediaID); err != nil {
		return Opened{}, err
	}
	if server == s.origin {
		return s.openLocal(ctx, server, mediaID, timeoutMS)
	}
	if !allowRemote {
		return Opened{}, matrixerr.New(matrixerr.CodeNotFound,
			"This server does not hold that content and was told not to ask for it.")
	}
	return s.openRemote(ctx, server, mediaID)
}

func (s *Service) openLocal(ctx context.Context, server, mediaID string, timeoutMS int64) (Opened, error) {
	metadata, err := s.db.Media.Get(ctx, server, mediaID)
	if err != nil {
		if errors.Is(err, persist.ErrNotFound) {
			return Opened{}, matrixerr.New(matrixerr.CodeNotFound, "This server holds no such content.")
		}
		return Opened{}, err
	}
	path := s.pathFor(metadata.Origin, metadata.MediaID)
	if metadata.UploadedAt == 0 {
		// Created and not yet filled. The specification lets the client ask the server to wait; answering
		// immediately is allowed ("the content repository MAY respond before the timeout") and is what this
		// server does, because waiting would hold a connection open on a guess about when the upload comes.
		return Opened{}, matrixerr.Newf(matrixerr.CodeNotYetUploaded,
			"That content has been reserved but not uploaded yet. It expires in %d milliseconds at the latest.",
			s.pendingRemainingMS(metadata, timeoutMS))
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Opened{}, matrixerr.New(matrixerr.CodeNotFound,
				"This server knows about that content but can no longer find its bytes.")
		}
		return Opened{}, fmt.Errorf("media: cannot reach the stored bytes: %w", err)
	}
	return Opened{Metadata: *metadata, Path: path}, nil
}

func (s *Service) openRemote(ctx context.Context, server, mediaID string) (Opened, error) {
	if metadata, err := s.db.Media.Get(ctx, server, mediaID); err == nil {
		path := s.pathFor(metadata.Origin, metadata.MediaID)
		if metadata.UploadedAt != 0 {
			if _, statErr := os.Stat(path); statErr == nil {
				return Opened{Metadata: *metadata, Path: path}, nil
			}
		}
	} else if !errors.Is(err, persist.ErrNotFound) {
		return Opened{}, err
	}
	if s.fetch == nil {
		return Opened{}, matrixerr.New(matrixerr.CodeNotFound,
			"This server does not hold that content and is not able to ask another server for it.")
	}

	fetchCtx, cancel := context.WithTimeout(ctx, s.cfg.RemoteFetchTimeout)
	defer cancel()
	content, err := s.fetch.FetchMedia(fetchCtx, server, mediaID, s.cfg.MaxRemoteBytes)
	if err != nil {
		if errors.Is(err, ErrTooLargeRemote) {
			return Opened{}, matrixerr.New(matrixerr.CodeTooLarge,
				"That content is too large for this server to hold.").WithStatus(502)
		}
		return Opened{}, err
	}
	if int64(len(content.Bytes)) > s.cfg.MaxRemoteBytes {
		return Opened{}, matrixerr.New(matrixerr.CodeTooLarge,
			"That content is too large for this server to hold.").WithStatus(502)
	}

	now := s.now()
	metadata := &persist.MediaMetadata{
		Origin:      server,
		MediaID:     mediaID,
		Local:       false,
		ContentType: content.ContentType,
		UploadName:  content.FileName,
		SizeBytes:   int64(len(content.Bytes)),
		SHA256Hex:   hex.EncodeToString(sha256Sum(content.Bytes)),
		CreatedAt:   now.UnixMilli(),
		UploadedAt:  now.UnixMilli(),
	}
	// The bytes go first. A row without bytes would be a promise this server cannot keep; a file without a
	// row is unreachable, which is the failure mode to prefer.
	path := s.pathFor(server, mediaID)
	if err := writeFile(path, content.Bytes); err != nil {
		return Opened{}, err
	}
	if err := s.db.Media.Put(ctx, metadata); err != nil {
		return Opened{}, err
	}
	return Opened{Metadata: *metadata, Path: path}, nil
}

// store writes one upload out: bytes first, then the row that describes them.
//
// The order matters. A row whose file is missing is a lie about content existing; a file with no row is
// simply unreachable and is cleaned up by nothing but a restart of the process, which is the lesser of the
// two.
func (s *Service) store(ctx context.Context, origin, mediaID, fileName, contentType, userID string, body io.Reader) (*persist.MediaMetadata, error) {
	s.purgeExpired(ctx)
	path := s.pathFor(origin, mediaID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("media: cannot prepare the content repository: %w", err)
	}
	digest, size, err := writeLimited(path, body, s.cfg.MaxUploadBytes)
	if err != nil {
		if errors.Is(err, errOverflow) {
			return nil, matrixerr.Newf(matrixerr.CodeTooLarge,
				"This server accepts content up to %d bytes.", s.cfg.MaxUploadBytes)
		}
		return nil, err
	}
	now := s.now()
	metadata := &persist.MediaMetadata{
		Origin:      origin,
		MediaID:     mediaID,
		Local:       origin == s.origin,
		ContentType: contentType,
		UploadName:  fileName,
		SizeBytes:   size,
		SHA256Hex:   digest,
		CreatedBy:   userID,
		CreatedAt:   now.UnixMilli(),
		UploadedAt:  now.UnixMilli(),
	}
	if err := s.db.Media.Put(ctx, metadata); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return metadata, nil
}

// checkPendingBudget keeps one user from creating an unlimited number of reserved URIs.
func (s *Service) checkPendingBudget(ctx context.Context, userID string) error {
	count, err := s.db.Media.CountPendingFor(ctx, userID, s.now().UnixMilli())
	if err != nil {
		return err
	}
	if count >= s.cfg.MaxPendingUploadsPerUser {
		return matrixerr.Newf(matrixerr.CodeLimitExceeded,
			"This user already has %d uploads waiting to be filled.", count)
	}
	return nil
}

// purgeExpired drops reservations whose TTL has passed. It runs opportunistically rather than from a
// worker: the cost is one indexed query on the paths that create reservations, and it keeps the repository
// from accumulating URIs that will never be filled.
func (s *Service) purgeExpired(ctx context.Context) {
	expired, err := s.db.Media.PurgeExpired(ctx, s.now().UnixMilli())
	if err != nil {
		s.log.Warn("content repository: expired reservations could not be cleared", "error", err)
		return
	}
	for _, metadata := range expired {
		if err := os.Remove(s.pathFor(metadata.Origin, metadata.MediaID)); err != nil && !os.IsNotExist(err) {
			s.log.Warn("content repository: a partial upload could not be removed",
				"media_id", metadata.MediaID, "error", err)
		}
	}
}

func (s *Service) pendingRemainingMS(metadata *persist.MediaMetadata, timeoutMS int64) int64 {
	if metadata.PendingExpiresAt == 0 {
		return s.cfg.MaxTimeoutMS
	}
	remaining := metadata.PendingExpiresAt - s.now().UnixMilli()
	if remaining < 0 {
		return 0
	}
	if timeoutMS > 0 && timeoutMS < remaining {
		return timeoutMS
	}
	return remaining
}

// newMediaID makes an identifier that satisfies the media-id rule the specification insists on.
func (s *Service) newMediaID() (string, error) {
	// 22 base64url characters is 132 bits of randomness, and base64url's alphabet is a subset of the
	// allowed one (A-Za-z0-9 and `-`; `_` never appears because padding is not used).
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("media: cannot choose a media identifier: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(raw[:])
	if err := ValidateMediaID(id); err != nil {
		return "", err
	}
	return id, nil
}

// pathFor turns a validated (origin, mediaID) pair into a path under the repository directory.
//
// Both components are validated by the caller before reaching here, which is the only thing standing
// between a request and the filesystem — so this function does no escaping and nothing else either: an
// unvalidated component must not reach it.
func (s *Service) pathFor(origin, mediaID string) string {
	return filepath.Join(s.cfg.Path, origin, mediaID)
}

// URI formats an `mxc://` URI.
func URI(origin, mediaID string) string { return "mxc://" + origin + "/" + mediaID }

// SplitURI takes an `mxc://` URI to its two validated components.
func SplitURI(uri string) (origin, mediaID string, err error) {
	rest, found := strings.CutPrefix(uri, "mxc://")
	if !found {
		return "", "", matrixerr.New(matrixerr.CodeInvalidParam, "That is not a Matrix content URI.")
	}
	origin, mediaID, found = strings.Cut(rest, "/")
	if !found {
		return "", "", matrixerr.New(matrixerr.CodeInvalidParam, "A Matrix content URI needs a media identifier.")
	}
	if err := ValidateOrigin(origin, origin); err != nil {
		return "", "", err
	}
	if err := ValidateMediaID(mediaID); err != nil {
		return "", "", err
	}
	return origin, mediaID, nil
}

// ValidateMediaID applies the rule the specification states twice, once as a format and once as a
// security requirement: only alphanumeric characters, `_` and `-`. This is a whitelist rather than a
// blacklist of `.` and `/` because percent-encoded and UTF-8 encoded traversals defeat blacklists.
func ValidateMediaID(mediaID string) error {
	if mediaID == "" || len(mediaID) > 255 {
		return matrixerr.New(matrixerr.CodeInvalidParam, "A media identifier must be between 1 and 255 characters.")
	}
	for i := 0; i < len(mediaID); i++ {
		c := mediaID[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			return matrixerr.New(matrixerr.CodeInvalidParam,
				"A media identifier may only contain letters, digits, underscores and hyphens.")
		}
	}
	return nil
}

// ValidateOrigin checks the server-name component of an `mxc://` URI.
//
// A valid server name cannot contain a path separator, and `Validate` relies on that: it is the check
// that keeps `mxc://example.com/../../etc/passwd` from becoming a filesystem path. The extra explicit
// refusal of separators is belt-and-braces for a server-name grammar that is tightened or loosened later.
func ValidateOrigin(origin, local string) error {
	if origin == local {
		return nil
	}
	if strings.ContainsRune(origin, '/') || strings.ContainsRune(origin, '\\') || strings.ContainsRune(origin, 0) {
		return matrixerr.New(matrixerr.CodeInvalidParam, "That is not a valid server name.")
	}
	if err := matrixid.ServerNameValid(origin); err != nil {
		return matrixerr.New(matrixerr.CodeInvalidParam, "That is not a valid server name.")
	}
	return nil
}

// sha256Sum is the hash of a complete byte slice.
func sha256Sum(content []byte) []byte {
	sum := sha256.Sum256(content)
	return sum[:]
}

// errOverflow marks a body that went past the size the server will hold.
var errOverflow = errors.New("media: content exceeded the limit")

// writeLimited copies a body to a file, refusing it if it is larger than maxBytes. It writes to a
// temporary name and renames, so a refused or interrupted upload never leaves a file at the final path
// while the row says nothing about it.
func writeLimited(path string, body io.Reader, maxBytes int64) (sha256Hex string, size int64, err error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("media: cannot stage an upload: %w", err)
	}
	name := temp.Name()
	defer func() {
		if err != nil {
			_ = temp.Close()
			_ = os.Remove(name)
		}
	}()

	hash := sha256.New()
	// Reading one byte past the limit is what distinguishes "exactly at the limit" from "over it" without
	// buffering the body to find out.
	limited := io.LimitReader(body, maxBytes+1)
	written, err := io.Copy(io.MultiWriter(temp, hash), limited)
	if err != nil {
		return "", 0, fmt.Errorf("media: the upload could not be read: %w", err)
	}
	if written > maxBytes {
		err = errOverflow
		return "", 0, err
	}
	if err := temp.Close(); err != nil {
		return "", 0, fmt.Errorf("media: the upload could not be written: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return "", 0, fmt.Errorf("media: the upload could not be put in place: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

// writeFile puts bytes at a path the caller has already validated, atomically.
func writeFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("media: cannot prepare the content repository: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".fetch-*")
	if err != nil {
		return fmt.Errorf("media: cannot stage a download: %w", err)
	}
	name := temp.Name()
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("media: the fetched content could not be written: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("media: the fetched content could not be written: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("media: the fetched content could not be put in place: %w", err)
	}
	return nil
}
