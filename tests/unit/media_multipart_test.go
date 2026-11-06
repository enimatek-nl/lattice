package unit

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/media"
)

// TestMultipartMediaRoundTrip writes a federation media answer and reads it back. Both directions are in
// this package on purpose: a server whose writer and reader disagree would pass a test of either one alone
// and fail every download a peer makes.
func TestMultipartMediaRoundTrip(t *testing.T) {
	t.Parallel()
	content := []byte("the bytes a peer asked for")
	recorder := httptest.NewRecorder()
	if err := media.WriteMultipartResponse(recorder, "text/plain", "greeting.txt", bytes.NewReader(content)); err != nil {
		t.Fatalf("writing the answer: %v", err)
	}
	response := recorder.Result()
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "multipart/mixed; boundary=") {
		t.Fatalf("the answer is not multipart/mixed: %q", response.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("cannot read the answer: %v", err)
	}

	part, err := media.ParseMultipartResponse(response.Header, body)
	if err != nil {
		t.Fatalf("reading the answer back: %v", err)
	}
	if !bytes.Equal(part.Content, content) {
		t.Errorf("the content part is %q", part.Content)
	}
	if part.ContentType != "text/plain" {
		t.Errorf("the content part's type is %q", part.ContentType)
	}
	if part.FileName != "greeting.txt" {
		t.Errorf("the content part's file name is %q", part.FileName)
	}
	if part.Location != "" {
		t.Errorf("the content part claims a location: %q", part.Location)
	}

	// The first part is the metadata, and the specification says it currently holds an empty object. That
	// is worth pinning: a caller that reads two parts reads the *first* one as metadata, and a server that
	// puts the file there is answering in a language nobody reads.
	_, params, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	first, err := reader.NextPart()
	if err != nil {
		t.Fatalf("the answer has no first part: %v", err)
	}
	metadata, err := io.ReadAll(first)
	if err != nil {
		t.Fatalf("cannot read the metadata part: %v", err)
	}
	if got := first.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("the metadata part is %q, want application/json", got)
	}
	var decoded map[string]any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatalf("the metadata part is not a JSON object: %v (%s)", err, metadata)
	}
	if len(decoded) != 0 {
		t.Errorf("the metadata part carries fields the specification does not define: %v", decoded)
	}
}

// TestMultipartMediaNamesRoundTrip covers the file names that a header cannot carry literally. A file called
// ☕.png has to survive a header that is nominally ASCII, and the way it does is the RFC 5987 form.
func TestMultipartMediaNamesRoundTrip(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"plain.png", "with spaces.png", "sem;colon.png", "☕.png", `quote"inside.png`} {
		recorder := httptest.NewRecorder()
		if err := media.WriteMultipartResponse(recorder, "image/png", name, strings.NewReader("x")); err != nil {
			t.Fatalf("writing an answer named %q: %v", name, err)
		}
		response := recorder.Result()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("cannot read the answer: %v", err)
		}
		part, err := media.ParseMultipartResponse(response.Header, body)
		if err != nil {
			t.Fatalf("reading an answer named %q: %v", name, err)
		}
		if part.FileName != name {
			t.Errorf("the name %q came back as %q", name, part.FileName)
		}
	}
}

// TestParseRemoteMediaShapes covers the answers a peer might give that are not the specified multipart form,
// and the one shape this server refuses to follow.
func TestParseRemoteMediaShapes(t *testing.T) {
	t.Parallel()

	// A plain body from a server that has no federation media endpoint — the deprecated endpoints, and the
	// servers that predate them — is the content itself.
	plain, err := media.ParseMultipartResponse(
		headerOf("Content-Type", "image/png", "Content-Disposition", `inline; filename="photo.png"`),
		[]byte("bytes"))
	if err != nil {
		t.Fatalf("a plain answer: %v", err)
	}
	if plain.ContentType != "image/png" || plain.FileName != "photo.png" || string(plain.Content) != "bytes" {
		t.Errorf("a plain answer was read as %+v", plain)
	}

	// A Location instead of bytes is reported, not fetched. Where the URL points is a peer's choice, and
	// that is exactly why this server does not act on it.
	body, header := multipartAnswer(t, textHeader("application/json"), []byte("{}"),
		textproto.MIMEHeader{
			"Content-Type": []string{"image/png"},
			"Location":     []string{"http://127.0.0.1:8008/_matrix/client/v3/sync"},
		}, nil)
	part, err := media.ParseMultipartResponse(header, body)
	if err != nil {
		t.Fatalf("a Location answer: %v", err)
	}
	if part.Location == "" {
		t.Errorf("a Location answer was read as content: %+v", part)
	}

	// An answer that claims to be multipart and is not is an error, not an empty file handed to the
	// caller's users.
	_, err = media.ParseMultipartResponse(headerOf("Content-Type", "multipart/mixed; boundary=zzz"),
		[]byte("this is not multipart at all"))
	if err == nil {
		t.Errorf("a malformed multipart answer was accepted")
	}

	// A first part that is not metadata means the peer is speaking a different shape; guessing which part
	// holds the file is how a server ends up serving a JSON blob as an image.
	// A first part that is not metadata means the peer is speaking a different shape; guessing which part
	// holds the file is how a server ends up serving a JSON blob as an image.
	body, header = multipartAnswer(t, textHeader("image/png"), []byte("not metadata"), nil, nil)
	if _, err := media.ParseMultipartResponse(header, body); err == nil {
		t.Errorf("an answer with no metadata part was accepted")
	}
}

// TestContentDispositionRules covers the header the specification makes a security decision about: the
// content type decides whether bytes may be shown inline, and everything else is an attachment.
func TestContentDispositionRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		contentType string
		want        string
	}{
		{"image/png", "inline"},
		{"text/plain", "inline"},
		{"Text/Plain; charset=utf-8", "inline"},
		{"text/html", "attachment"},
		{"image/svg+xml", "attachment"},
		{"application/octet-stream", "attachment"},
		{"", "attachment"},
	}
	for _, tc := range cases {
		if got := media.DispositionFor(tc.contentType); got != tc.want {
			t.Errorf("DispositionFor(%q) = %q, want %q", tc.contentType, got, tc.want)
		}
	}
	if got := media.CanonicalType("Text/Plain; charset=utf-8"); got != "text/plain" {
		t.Errorf("CanonicalType = %q", got)
	}

	// An unnamed upload carries no name in the header rather than an invented one.
	if got := media.ContentDisposition("attachment", ""); got != "attachment" {
		t.Errorf("an unnamed disposition is %q", got)
	}
	header := media.ContentDisposition("attachment", "☕.png")
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		t.Fatalf("the disposition %q does not parse: %v", header, err)
	}
	if params["filename"] != "☕.png" {
		t.Errorf("a unicode name came back as %q from %q", params["filename"], header)
	}
}

// multipartAnswer builds a peer's answer body and the outer header that describes it. It is written by hand
// rather than through httptest.ResponseRecorder because the recorder puts its own content type on anything
// written to it, which is precisely the header this test is about.
func multipartAnswer(t *testing.T, firstHeader textproto.MIMEHeader, firstBody []byte,
	secondHeader textproto.MIMEHeader, secondBody []byte) ([]byte, http.Header) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreatePart(firstHeader)
	if err != nil {
		t.Fatalf("cannot write the first part: %v", err)
	}
	if _, err := part.Write(firstBody); err != nil {
		t.Fatalf("cannot write the first part: %v", err)
	}
	if secondHeader != nil {
		part, err = writer.CreatePart(secondHeader)
		if err != nil {
			t.Fatalf("cannot write the second part: %v", err)
		}
		if _, err := part.Write(secondBody); err != nil {
			t.Fatalf("cannot write the second part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("cannot finish the answer: %v", err)
	}
	header := http.Header{}
	header.Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	return buffer.Bytes(), header
}

// headerOf builds a header from alternating names and values.
func headerOf(pairs ...string) http.Header {
	header := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		header.Set(pairs[i], pairs[i+1])
	}
	return header
}

// textHeader builds the part header the multipart writer takes.
func textHeader(contentType string) textproto.MIMEHeader {
	return textproto.MIMEHeader{"Content-Type": []string{contentType}}
}
