package federation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// testPNG is a small image with a recognisable size, generated rather than embedded so that the
// thumbnail assertions can name its dimensions.
func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			picture.Set(x, y, color.RGBA{R: byte(x % 256), G: byte(y % 256), B: 0xff, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, picture); err != nil {
		t.Fatalf("encoding the test image: %v", err)
	}
	return buf.Bytes()
}

// uploadContent puts bytes on a server through the media endpoint. The harness's own request
// helper reads answers as JSON, and a picture is not JSON, so media goes over a plain request.
func uploadContent(t *testing.T, s *server, sess *session, filename, contentType string, body []byte) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost,
		s.url()+"/_matrix/media/v3/upload?filename="+url.QueryEscape(filename), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building the upload: %v", err)
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+sess.Token)
	got := roundTrip(t, request)
	if got.Status != http.StatusOK {
		t.Fatalf("uploading %s to %s: status %d (%s)", filename, s.name, got.Status, got.Raw)
	}
	uri := stringField(got.Body, "content_uri")
	if !strings.HasPrefix(uri, "mxc://") {
		t.Fatalf("the upload returned %q, want an mxc URI", uri)
	}
	_, mediaID, _ := strings.Cut(strings.TrimPrefix(uri, "mxc://"), "/")
	if mediaID == "" {
		t.Fatalf("the upload returned %q, which names no media id", uri)
	}
	return mediaID
}

// clientGet performs a client API GET without assuming the answer is JSON, which media is not.
func clientGet(t *testing.T, s *server, sess *session, path string) response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, s.url()+path, nil)
	if err != nil {
		t.Fatalf("building GET %s: %v", path, err)
	}
	if sess != nil {
		request.Header.Set("Authorization", "Bearer "+sess.Token)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	got, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET %s on %s: %v", path, s.name, err)
	}
	defer func() { _ = got.Body.Close() }()
	raw, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("reading the body of GET %s on %s: %v", path, s.name, err)
	}
	out := response{Status: got.StatusCode, Raw: raw, Headers: got.Header}
	// A JSON error body is decoded as well, so that a test can read the errcode of a refusal
	// without knowing in advance whether the answer was a picture.
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

// mediaParts reads a peer's multipart media answer: the media type of the answer, and each part
// as a media type plus bytes.
func mediaParts(t *testing.T, header http.Header, raw []byte) (string, []struct {
	ContentType string
	Body        []byte
}) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("the answer's content type %q is not readable: %v", header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	parts := []struct {
		ContentType string
		Body        []byte
	}{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading the parts of the answer: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("reading a part of the answer: %v", err)
		}
		_ = part.Close()
		parts = append(parts, struct {
			ContentType string
			Body        []byte
		}{ContentType: part.Header.Get("Content-Type"), Body: body})
	}
	return mediaType, parts
}

// TestAPeerAsksForContentWeHostAsTwoParts is the serving half of federation media. The two parts
// are the shape: a peer reads the description first, and only then decides what to do with a
// body it has not seen yet.
func TestAPeerAsksForContentWeHostAsTwoParts(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	one := c.one()

	alice := one.register("alice", "correct horse")
	picture := testPNG(t, 32, 16)
	mediaID := uploadContent(t, one, alice, "test.png", "image/png", picture)

	header, raw, err := c.two().peer().GetRaw(t.Context(), serverOne,
		"/_matrix/federation/v1/media/download/"+url.PathEscape(mediaID), nil, 1<<20)
	if err != nil {
		t.Fatalf("%s asking %s for the media %s: %v", serverTwo, serverOne, mediaID, err)
	}
	mediaType, parts := mediaParts(t, header, raw)
	if mediaType != "multipart/mixed" {
		t.Errorf("the answer is %s, want multipart/mixed", mediaType)
	}
	// Exactly two. A peer that counts parts throws an answer with a third in it away.
	if len(parts) != 2 {
		t.Fatalf("the answer carries %d parts, want exactly 2", len(parts))
	}
	if parts[0].ContentType != "application/json" {
		t.Errorf("the first part is %s, want application/json", parts[0].ContentType)
	}
	description := map[string]any{}
	if err := json.Unmarshal(parts[0].Body, &description); err != nil {
		t.Fatalf("the description part is not JSON: %v (%s)", err, parts[0].Body)
	}
	if stringField(description, "media_type") != "image/png" {
		t.Errorf("the description names media_type %v, want image/png", description["media_type"])
	}
	if length, ok := description["media_length"].(float64); !ok || int64(length) != int64(len(picture)) {
		t.Errorf("the description says media_length %v, want %d", description["media_length"], len(picture))
	}
	if stringField(description, "upload_name") != "test.png" {
		t.Errorf("the description names upload_name %v, want test.png", description["upload_name"])
	}
	if !bytes.Equal(parts[1].Body, picture) {
		t.Errorf("the content part is %d bytes, want the %d bytes that were uploaded", len(parts[1].Body), len(picture))
	}

	// The thumbnail: the box in the query, which is what current servers send.
	header, raw, err = c.two().peer().GetRaw(t.Context(), serverOne,
		"/_matrix/federation/v1/media/thumbnail/"+url.PathEscape(mediaID),
		url.Values{"width": []string{"8"}, "height": []string{"8"}, "method": []string{"scale"}}, 1<<20)
	if err != nil {
		t.Fatalf("%s asking %s for a thumbnail of %s: %v", serverTwo, serverOne, mediaID, err)
	}
	_, parts = mediaParts(t, header, raw)
	if len(parts) != 2 {
		t.Fatalf("the thumbnail answer carries %d parts, want exactly 2", len(parts))
	}
	scaled, err := png.Decode(bytes.NewReader(parts[1].Body))
	if err != nil {
		t.Fatalf("the second part of the thumbnail answer is not a PNG: %v", err)
	}
	if bounds := scaled.Bounds(); bounds.Dx() > 8 || bounds.Dy() > 8 {
		t.Errorf("the thumbnail is %dx%d, want it to fit 8x8", bounds.Dx(), bounds.Dy())
	}

	// And the older spelling, with the box in the path: a peer running an older version of the
	// specification still has to be able to ask.
	header, raw, err = c.two().peer().GetRaw(t.Context(), serverOne,
		fmt.Sprintf("/_matrix/federation/v1/media/thumbnail/%s/8/8", url.PathEscape(mediaID)), nil, 1<<20)
	if err != nil {
		t.Fatalf("%s asking %s for a thumbnail of %s the older way: %v", serverTwo, serverOne, mediaID, err)
	}
	if _, parts = mediaParts(t, header, raw); len(parts) != 2 {
		t.Errorf("the older-spelling thumbnail answer carries %d parts, want exactly 2", len(parts))
	}
}

// TestContentThatIsNotHereIsFetchedFromTheServerThatHoldsIt is the asking half, and the reason an
// mxc URI works at all: a client asks its own server for anybody's content, and its own server
// goes and gets it.
func TestContentThatIsNotHereIsFetchedFromTheServerThatHoldsIt(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	one, two := c.one(), c.two()

	alice := one.register("alice", "correct horse")
	bob := two.register("bob", "rubberduck")
	picture := testPNG(t, 24, 24)
	mediaID := uploadContent(t, one, alice, "cat.png", "image/png", picture)

	got := clientGet(t, two, bob, "/_matrix/client/v1/media/download/"+serverOne+"/"+mediaID)
	if got.Status != http.StatusOK {
		t.Fatalf("%s serving %s's content to %s: status %d (%s)", serverTwo, serverOne, bob.UserID, got.Status, got.Raw)
	}
	if !bytes.Equal(got.Raw, picture) {
		t.Errorf("%s served %d bytes, want the %d that were uploaded", serverTwo, len(got.Raw), len(picture))
	}
	if got.Headers.Get("Content-Type") != "image/png" {
		t.Errorf("the content is served as %q, want image/png", got.Headers.Get("Content-Type"))
	}
	// The name the uploader gave is part of the answer: it is what a client offers as the default
	// file name when the user saves the thing.
	if disposition := got.Headers.Get("Content-Disposition"); !strings.Contains(disposition, "cat.png") {
		t.Errorf("the answer's Content-Disposition is %q, which does not name cat.png", disposition)
	}

	// The copy is kept, which is the other half of the promise: the client's own server is now
	// able to answer, and a peer that has gone away does not take the content with it.
	cached, err := two.db.MediaRepo.Get(serverOne, mediaID)
	if err != nil {
		t.Fatalf("%s kept no copy of %s: %v", serverTwo, mediaID, err)
	}
	if cached.ContentType != "image/png" || cached.Size != int64(len(picture)) {
		t.Errorf("the copy on %s is %s of %d bytes, want image/png of %d",
			serverTwo, cached.ContentType, cached.Size, len(picture))
	}
	// The peer goes away and the content stays. A copy fetched once is this server's to serve,
	// and an outage next door is not a reason for a client's avatar to break.
	c.blackout(serverOne)
	again := clientGet(t, two, bob, "/_matrix/client/v1/media/download/"+serverOne+"/"+mediaID)
	if again.Status != http.StatusOK || !bytes.Equal(again.Raw, picture) {
		t.Errorf("with %s unreachable %s served status %d and %d bytes, want 200 and %d",
			serverOne, serverTwo, again.Status, len(again.Raw), len(picture))
	}
	c.restore(serverOne)

	// Content that does not exist anywhere is said so, rather than guessed at: the client asked
	// for one thing and is told whether it exists.
	missing := clientGet(t, two, bob, "/_matrix/client/v1/media/download/"+serverOne+"/nothingatall")
	if missing.Status != http.StatusNotFound {
		t.Errorf("asking for media that is not there answered %d, want 404 (%s)", missing.Status, missing.Raw)
	}

	// A thumbnail of content held elsewhere is asked of the server that has it, rather than
	// pulling the whole file down to resize it here.
	thumbnail := clientGet(t, two, bob, "/_matrix/client/v1/media/thumbnail/"+serverOne+"/"+mediaID+"?width=8&height=8&method=scale")
	if thumbnail.Status != http.StatusOK {
		t.Fatalf("a remote thumbnail answered %d (%s)", thumbnail.Status, thumbnail.Raw)
	}
	scaled, err := png.Decode(bytes.NewReader(thumbnail.Raw))
	if err != nil {
		t.Fatalf("the remote thumbnail is not an image: %v", err)
	}
	if bounds := scaled.Bounds(); bounds.Dx() > 8 || bounds.Dy() > 8 {
		t.Errorf("the remote thumbnail is %dx%d, want it to fit 8x8", bounds.Dx(), bounds.Dy())
	}
}

// TestMediaOfAServerThatDoesNotAnswerIsNotFound is the failure a client can act on. The content
// is somebody else's to hold, and a server that cannot reach them cannot invent it.
func TestMediaOfAServerThatDoesNotAnswerIsNotFound(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	two := c.two()
	bob := two.register("bob", "rubberduck")

	c.disconnect(serverOne)
	got := clientGet(t, two, bob, "/_matrix/client/v1/media/download/"+serverOne+"/whatever")
	if got.Status != http.StatusNotFound {
		t.Errorf("asking for content on a server that does not answer gave %d, want 404 (%s)", got.Status, got.Raw)
	}
	if _, err := two.db.MediaRepo.Get(serverOne, "whatever"); err == nil {
		t.Errorf("%s stored a copy of content it could not fetch", serverTwo)
	}
	c.restore(serverOne)
}
