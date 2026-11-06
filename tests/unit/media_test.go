package unit

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/media"
	"github.com/enimatek/lattice/internal/persist"
)

// mediaService builds a content repository over a temporary database and directory, with a clock the test
// can move.
func mediaService(t *testing.T, fetch media.RemoteFetcher) (*media.Service, *persist.Database, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := persist.Open(filepath.Join(dir, "media.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("cannot open the database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := filepath.Join(dir, "media")
	service, err := media.New(db, media.Config{
		Path:                     repo,
		MaxUploadBytes:           1 << 20,
		MaxRemoteBytes:           1 << 20,
		MaxThumbnailSourceBytes:  1 << 20,
		MaxPendingUploadsPerUser: 2,
		PendingUploadTTL:         time.Hour,
	}, "hs1", slog.New(slog.DiscardHandler), fetch)
	if err != nil {
		t.Fatalf("cannot build the content repository: %v", err)
	}
	return service, db, repo
}

// pngBytes builds an image of exact dimensions.
func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: byte(x), G: byte(y), B: 0x40, A: 0xff})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("cannot build an image: %v", err)
	}
	return buffer.Bytes()
}

// TestMediaUploadThenOpen checks that an upload lands in both places the content repository keeps things:
// bytes on disk under a path built from validated components, and a row that describes them.
func TestMediaUploadThenOpen(t *testing.T) {
	t.Parallel()
	service, _, repo := mediaService(t, nil)
	ctx := context.Background()

	uri, err := service.Upload(ctx, "@alice:hs1", "notes.txt", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if uri != "mxc://hs1/"+uri[len("mxc://hs1/"):] {
		t.Fatalf("the URI is not this server's: %q", uri)
	}
	mediaID := uri[len("mxc://hs1/"):]
	if strings.ContainsAny(mediaID, "/\\. ") {
		t.Errorf("a generated media identifier contains a character that could name a path: %q", mediaID)
	}

	opened, err := service.Open(ctx, "hs1", mediaID, true, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.Metadata.ContentType != "text/plain" || opened.Metadata.UploadName != "notes.txt" {
		t.Errorf("the metadata lost what the uploader said: %+v", opened.Metadata)
	}
	if opened.Metadata.SizeBytes != 5 || opened.Metadata.SHA256Hex == "" {
		t.Errorf("the metadata does not describe the bytes: %+v", opened.Metadata)
	}
	content, err := os.ReadFile(opened.Path)
	if err != nil {
		t.Fatalf("the stored bytes are not where the row says: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("stored %q", content)
	}
	if opened.Path != filepath.Join(repo, "hs1", mediaID) {
		t.Errorf("the bytes are at %q, want them under %q", opened.Path, repo)
	}
}

// TestMediaIdentifierRules is the security rule the specification states twice: a media identifier is used
// as a file name, so the whitelist is what stands between a request and the filesystem.
func TestMediaIdentifierRules(t *testing.T) {
	t.Parallel()
	service, _, repo := mediaService(t, nil)
	ctx := context.Background()

	for _, mediaID := range []string{"", "..", "../../etc/passwd", "a/b", "a\\b", "with space",
		"percent%20encoded", "dot.dot", "xéx"} {
		if _, err := service.Open(ctx, "hs1", mediaID, false, 0); !isInvalidParam(err) {
			t.Errorf("opening media identifier %q: got %v, want a refusal of the identifier", mediaID, err)
		}
	}
	for _, origin := range []string{"", "..", "exa mple", "a/b"} {
		if _, err := service.Open(ctx, origin, "abc", false, 0); !isInvalidParam(err) {
			t.Errorf("opening an origin %q: got %v, want a refusal", origin, err)
		}
	}
	// Nothing was written under the repository for any of those attempts.
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("cannot read the repository directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("refused identifiers left something on disk: %v", entries)
	}
	// The forms the specification allows are accepted.
	for _, mediaID := range []string{"a", "ABCxyz019", "under_score", "hy-phen"} {
		if err := media.ValidateMediaID(mediaID); err != nil {
			t.Errorf("ValidateMediaID(%q) = %v", mediaID, err)
		}
	}
}

func isInvalidParam(err error) bool {
	failure, ok := matrixerr.As(err)
	return ok && failure.Code == matrixerr.CodeInvalidParam
}

// TestMediaCreateThenUploadToURI covers the two-step upload, including the state between the two calls and
// the ownership of a reservation.
func TestMediaCreateThenUploadToURI(t *testing.T) {
	t.Parallel()
	service, _, _ := mediaService(t, nil)
	ctx := context.Background()

	uri, expiresAt, err := service.Create(ctx, "@alice:hs1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if expiresAt <= time.Now().UnixMilli() {
		t.Errorf("the reservation does not expire in the future: %d", expiresAt)
	}
	mediaID := uri[len("mxc://hs1/"):]

	if _, err := service.Open(ctx, "hs1", mediaID, true, 0); !isNotYetUploaded(err) {
		t.Errorf("content that has not been uploaded yet: got %v, want M_NOT_YET_UPLOADED", err)
	}

	if _, err := service.UploadToURI(ctx, "@bob:hs1", "hs1", mediaID, "mine.txt", "text/plain",
		strings.NewReader("not alice's")); err == nil {
		t.Errorf("bob filled alice's reservation")
	}
	if _, err := service.UploadToURI(ctx, "@alice:hs1", "hs1", "neverreserved", "x", "text/plain",
		strings.NewReader("x")); !isNotFound(err) {
		t.Errorf("uploading to an identifier that was never created: got %v, want M_NOT_FOUND", err)
	}

	uriAgain, err := service.UploadToURI(ctx, "@alice:hs1", "hs1", mediaID, "mine.txt", "text/plain",
		strings.NewReader("at last"))
	if err != nil {
		t.Fatalf("filling the reservation: %v", err)
	}
	if uriAgain != uri {
		t.Errorf("filling a reservation produced a different URI: %q and %q", uri, uriAgain)
	}
	opened, err := service.Open(ctx, "hs1", mediaID, true, 0)
	if err != nil {
		t.Fatalf("open after the upload: %v", err)
	}
	if opened.Metadata.UploadName != "mine.txt" {
		t.Errorf("the name given with the body was not kept: %+v", opened.Metadata)
	}
	// A reservation that has been filled cannot be filled again: two bodies for one URI is the second
	// one's problem, not the first one's to answer for.
	if _, err := service.UploadToURI(ctx, "@alice:hs1", "hs1", mediaID, "again.txt", "text/plain",
		strings.NewReader("again")); !isCannotOverwrite(err) {
		t.Errorf("a filled reservation was filled a second time: %v", err)
	}
}

func isNotYetUploaded(err error) bool {
	failure, ok := matrixerr.As(err)
	return ok && failure.Code == matrixerr.CodeNotYetUploaded
}

// isCannotOverwrite is the specification's answer for an upload to an address that already has content.
func isCannotOverwrite(err error) bool {
	failure, ok := matrixerr.As(err)
	return ok && failure.Code == matrixerr.CodeCannotOverwriteMedia
}

func isNotFound(err error) bool {
	failure, ok := matrixerr.As(err)
	return ok && failure.Code == matrixerr.CodeNotFound
}

// TestMediaPendingBudgetAndExpiry checks that creating and abandoning URIs is not free, and that a
// reservation stops being held when it expires.
func TestMediaPendingBudgetAndExpiry(t *testing.T) {
	t.Parallel()
	service, _, _ := mediaService(t, nil)
	now := time.Now()
	service.WithClock(func() time.Time { return now })
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, _, err := service.Create(ctx, "@alice:hs1"); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, _, err := service.Create(ctx, "@alice:hs1"); err == nil {
		t.Errorf("a third reservation was allowed under a budget of two")
	}
	// A different user's reservations are not counted against this one.
	if _, _, err := service.Create(ctx, "@bob:hs1"); err != nil {
		t.Errorf("bob was refused under alice's budget: %v", err)
	}

	// Move past the expiry: the abandoned reservations stop being held, and the budget is free again.
	now = now.Add(2 * time.Hour)
	if _, err := service.Upload(ctx, "@alice:hs1", "fresh.txt", "text/plain", strings.NewReader("fresh")); err != nil {
		t.Errorf("after the reservations expired, an upload failed: %v", err)
	}
}

// TestMediaOversizedUploadIsRefused checks the refusal and, just as importantly, that a refused upload
// leaves nothing behind.
func TestMediaOversizedUploadIsRefused(t *testing.T) {
	t.Parallel()
	service, _, repo := mediaService(t, nil)
	ctx := context.Background()

	_, err := service.Upload(ctx, "@alice:hs1", "big.bin", "application/octet-stream",
		io.LimitReader(&infiniteReader{}, 2<<20))
	if failure, ok := matrixerr.As(err); !ok || failure.Code != matrixerr.CodeTooLarge {
		t.Fatalf("an oversized upload: got %v, want M_TOO_LARGE", err)
	}
	if entries, err := os.ReadDir(filepath.Join(repo, "hs1")); err == nil && len(entries) != 0 {
		t.Errorf("the refused upload left files behind: %v", entries)
	}
	// Nothing about that upload is reachable afterwards: no file at the path it would have used, and no
	// row that would answer with anything other than "no such content".
	if _, err := service.Open(ctx, "hs1", "anything", false, 0); !isNotFound(err) {
		t.Errorf("the refused upload left something reachable: %v", err)
	}
}

type infiniteReader struct{ at int }

func (r *infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r.at + i)
	}
	r.at += len(p)
	return len(p), nil
}

// countingFetcher is a stand-in for another server's content repository.
type countingFetcher struct {
	calls   int
	content []byte
	err     error
}

func (f *countingFetcher) FetchMedia(ctx context.Context, server, mediaID string, maxBytes int64) (media.RemoteContent, error) {
	f.calls++
	if f.err != nil {
		return media.RemoteContent{}, f.err
	}
	return media.RemoteContent{Bytes: f.content, ContentType: "image/png", FileName: "remote.png"}, nil
}

// TestMediaRemoteContentIsHeldOnce asks the same remote content twice. The second answer must come from
// this server's own copy: a server that asked again on every request would put a remote server in the path
// of every download its own users make.
func TestMediaRemoteContentIsHeldOnce(t *testing.T) {
	t.Parallel()
	fetcher := &countingFetcher{content: pngBytes(t, 64, 48)}
	service, _, repo := mediaService(t, fetcher)
	ctx := context.Background()

	first, err := service.Open(ctx, "elsewhere.example", "remotepic", true, 0)
	if err != nil {
		t.Fatalf("the first open: %v", err)
	}
	if fetcher.calls != 1 {
		t.Fatalf("the remote server was asked %d times for the first request", fetcher.calls)
	}
	if first.Metadata.Local {
		t.Errorf("content from another server is recorded as this server's own")
	}
	if first.Metadata.ContentType != "image/png" || first.Metadata.UploadName != "remote.png" {
		t.Errorf("what the other server said was not kept: %+v", first.Metadata)
	}
	if first.Path != filepath.Join(repo, "elsewhere.example", "remotepic") {
		t.Errorf("remote content is at %q, which is not under a directory named for its origin", first.Path)
	}

	second, err := service.Open(ctx, "elsewhere.example", "remotepic", true, 0)
	if err != nil {
		t.Fatalf("the second open: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("the remote server was asked %d times for two requests", fetcher.calls)
	}
	stored, err := os.ReadFile(second.Path)
	if err != nil || !bytes.Equal(stored, fetcher.content) {
		t.Errorf("the cached copy is not the content: %v (%d bytes)", err, len(stored))
	}

	// A request that says do not fetch is refused rather than fetched.
	if _, err := service.Open(ctx, "elsewhere.example", "remotepic", false, 0); !isNotFound(err) {
		t.Errorf("allow_remote=false: got %v, want M_NOT_FOUND", err)
	}
	// Content from another server that is too large for this server is a 502, because the oversized thing
	// arrived from a third party and not from the client that is asking.
	tooBig := &countingFetcher{content: make([]byte, 4<<20)}
	bigService, _, _ := mediaService(t, tooBig)
	if _, err := bigService.Open(ctx, "elsewhere.example", "huge", true, 0); !isTooLarge(err) {
		t.Errorf("oversized remote content: got %v, want M_TOO_LARGE", err)
	} else if failure, _ := matrixerr.As(err); matrixerr.Status(failure) != 502 {
		t.Errorf("oversized remote content answers %d, want 502", matrixerr.Status(failure))
	}
}

func isTooLarge(err error) bool {
	failure, ok := matrixerr.As(err)
	return ok && failure.Code == matrixerr.CodeTooLarge
}

// TestMediaNoFetcherMeansNoRemoteContent checks what a server that does not federate says about somebody
// else's file.
func TestMediaNoFetcherMeansNoRemoteContent(t *testing.T) {
	t.Parallel()
	service, _, _ := mediaService(t, nil)
	if _, err := service.Open(context.Background(), "elsewhere.example", "pic", true, 0); !isNotFound(err) {
		t.Errorf("remote content with no federation: got %v, want M_NOT_FOUND", err)
	}
}

// jpegOf builds a JPEG of exact dimensions.
func jpegOf(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: byte(x), G: byte(y), B: 0x40, A: 0xff})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("cannot build a JPEG: %v", err)
	}
	return buffer.Bytes()
}

// imageSize decodes an image and reports its dimensions.
func imageSize(t *testing.T, content []byte) (int, int) {
	t.Helper()
	decoded, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("the answer is not a decodable image: %v", err)
	}
	bounds := decoded.Bounds()
	return bounds.Dx(), bounds.Dy()
}

// TestMakeThumbnail covers the two MUSTs of the thumbnail section: nothing is enlarged, and a source smaller
// than the request is returned as it is.
func TestMakeThumbnail(t *testing.T) {
	t.Parallel()
	large := pngBytes(t, 100, 80)

	scaled, err := media.MakeThumbnail(large, "image/png", 32, 32, "scale")
	if err != nil {
		t.Fatalf("scale: %v", err)
	}
	width, height := imageSize(t, scaled.Bytes)
	if width != 32 || height > 32 || height < 1 {
		t.Errorf("100x80 scaled to 32x32 gave %dx%d", width, height)
	}
	if scaled.ContentType != "image/png" {
		t.Errorf("a PNG thumbnail came back as %q", scaled.ContentType)
	}

	cropped, err := media.MakeThumbnail(large, "image/png", 40, 20, "crop")
	if err != nil {
		t.Fatalf("crop: %v", err)
	}
	if width, height = imageSize(t, cropped.Bytes); width != 40 || height != 20 {
		t.Errorf("a crop to 40x20 gave %dx%d", width, height)
	}

	small := pngBytes(t, 10, 10)
	untouched, err := media.MakeThumbnail(small, "image/png", 64, 64, "scale")
	if err != nil {
		t.Fatalf("thumbnailing a small image: %v", err)
	}
	if !untouched.FromSource || !bytes.Equal(untouched.Bytes, small) {
		t.Errorf("a 10x10 image asked for at 64x64 was changed rather than returned")
	}

	// A source that is larger along one axis and smaller along the other is reduced along the one, not
	// enlarged along the other.
	mixed := pngBytes(t, 200, 8)
	reduced, err := media.MakeThumbnail(mixed, "image/png", 100, 100, "scale")
	if err != nil {
		t.Fatalf("thumbnailing a wide image: %v", err)
	}
	width, height = imageSize(t, reduced.Bytes)
	if width > 200 || height > 8 {
		t.Errorf("a 200x8 image was enlarged to %dx%d", width, height)
	}

	// Content that is not an image is refused with the sentinel the API turns into the specification's
	// error.
	if _, err := media.MakeThumbnail([]byte("not an image"), "text/plain", 32, 32, "scale"); !errors.Is(err, media.ErrNotImage) {
		t.Errorf("thumbnailing text: got %v, want ErrNotImage", err)
	}

	// A JPEG source stays a JPEG: a client that asked for a thumbnail of a photograph should not be handed
	// a lossless re-encode of it.
	jpegBytes := jpegOf(t, 60, 60)
	photo, err := media.MakeThumbnail(jpegBytes, "image/jpeg", 30, 30, "scale")
	if err != nil {
		t.Fatalf("thumbnailing a JPEG: %v", err)
	}
	if photo.ContentType != "image/jpeg" {
		t.Errorf("a JPEG thumbnail came back as %q", photo.ContentType)
	}
}

// TestThumbnailSizeLimits checks that the bound is applied before an image is decoded, because decoding is
// where the memory goes.
func TestThumbnailSizeLimits(t *testing.T) {
	t.Parallel()
	service, _, _ := mediaService(t, nil)
	ctx := context.Background()

	source := pngBytes(t, 400, 400)
	uri, err := service.Upload(ctx, "@alice:hs1", "big.png", "image/png", bytes.NewReader(source))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	mediaID := uri[len("mxc://hs1/"):]
	if _, err := service.Thumbnail(ctx, "hs1", mediaID,
		media.ThumbnailRequest{Width: 32, Height: 32, Method: "scale"}, true, 0); err != nil {
		t.Errorf("a thumbnail within the limit failed: %v", err)
	}
	for _, bad := range []media.ThumbnailRequest{
		{Width: 0, Height: 32},
		{Width: -1, Height: 32},
		{Width: 100000, Height: 100000},
		{Width: 32, Height: 32, Method: "sideways"},
	} {
		if _, err := service.Thumbnail(ctx, "hs1", mediaID, bad, true, 0); err == nil {
			t.Errorf("a request %+v was accepted", bad)
		}
	}
}
