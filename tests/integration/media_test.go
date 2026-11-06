package integration

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"testing"
)

// The content repository is the part of this server that hands out other people's files. The tests here are
// about the promises that make that safe and comprehensible: which URI the bytes live at, what a downloader
// is told about them, what happens when the bytes belong to somebody else's server, and what happens when a
// request asks for more than this server is willing to hold.

// uploadMedia posts bytes to the upload endpoint with the content type and file name a client would send.
func uploadMedia(t *testing.T, h *harness, token string, content []byte, contentType, fileName string) response {
	t.Helper()
	path := "/_matrix/media/v3/upload"
	if fileName != "" {
		path += "?filename=" + url.QueryEscape(fileName)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.clientURL+path, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("cannot build the upload request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return h.roundTrip(t, req)
}

// getRaw performs a GET and keeps the headers, which is the whole point for an endpoint whose answer is a
// header as much as a body.
func getRaw(t *testing.T, h *harness, path, token string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.clientURL+path, nil)
	if err != nil {
		t.Fatalf("cannot build the request for %s: %v", path, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return h.roundTrip(t, req)
}

// pngOf writes a PNG of exact dimensions with a gradient, so that a resampler which loses rows or
// columns cannot hide behind a flat colour.
func pngOf(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: byte(x), G: byte(y), B: 0x80, A: 0xff})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("cannot build a test image: %v", err)
	}
	return buffer.Bytes()
}

// mxcID takes the media identifier out of an mxc:// URI. The origin is checked by mxcOrigin when a test
// cares which server the URI names.
func mxcID(t *testing.T, answer map[string]any) string {
	t.Helper()
	uri, _ := answer["content_uri"].(string)
	_, mediaID := mxcOf(t, uri)
	if mediaID == "" {
		t.Fatalf("content_uri %q has no media identifier", uri)
	}
	return mediaID
}

// mxcOf splits an mxc:// URI into origin and media identifier.
func mxcOf(t *testing.T, uri string) (origin, mediaID string) {
	t.Helper()
	const prefix = "mxc://"
	if len(uri) <= len(prefix) || uri[:len(prefix)] != prefix {
		t.Fatalf("%q is not an mxc:// URI", uri)
	}
	rest := uri[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[:i], rest[i+1:]
		}
	}
	t.Fatalf("%q has no media identifier", uri)
	return "", ""
}

// TestMediaUploadAndDownload covers the round trip and the headers that come back with it.
func TestMediaUploadAndDownload(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "mediaalice", "pw-mediaalice", "")

	content := []byte("Hello from the content repository")
	uploaded := uploadMedia(t, h, token, content, "text/plain", "greeting.txt")
	if uploaded.status != http.StatusOK {
		t.Fatalf("upload: status %d (body %s)", uploaded.status, uploaded.body)
	}
	mediaID := mxcID(t, uploaded.decoded(t))

	// The deprecated endpoint is open to anybody who knows the URI. That is the specification's design,
	// and it is exactly why the authenticated door below also has to exist.
	open := getRaw(t, h, "/_matrix/media/v3/download/hs1/"+mediaID, "")
	if open.status != http.StatusOK {
		t.Fatalf("unauthenticated download: status %d (body %s)", open.status, open.body)
	}
	if !bytes.Equal(open.body, content) {
		t.Errorf("the downloaded bytes are not what was uploaded: %q", open.body)
	}
	if got := open.header.Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q, want the type the uploader declared", got)
	}
	if got := open.header.Get("Content-Security-Policy"); got == "" {
		t.Errorf("served content carries no content security policy")
	}
	if got := open.header.Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q", got)
	}
	if disposition := dispositionOf(t, open); disposition.filename != "greeting.txt" {
		t.Errorf("Content-Disposition = %q, want the name the uploader gave", disposition.raw)
	}

	// The authenticated door, and the refusal when the token is taken away.
	withToken := getRaw(t, h, "/_matrix/client/v1/media/download/hs1/"+mediaID, token)
	if withToken.status != http.StatusOK || !bytes.Equal(withToken.body, content) {
		t.Errorf("authenticated download: status %d, %d bytes", withToken.status, len(withToken.body))
	}
	if anonymous := getRaw(t, h, "/_matrix/client/v1/media/download/hs1/"+mediaID, ""); anonymous.status != http.StatusUnauthorized {
		t.Errorf("an anonymous request on the authenticated endpoint: status %d, want 401", anonymous.status)
	}

	// A download may be given the name to present, in the path rather than as a query parameter: what a
	// browser writes in a save dialog comes from the URL it was handed.
	renamed := getRaw(t, h, "/_matrix/media/v3/download/hs1/"+mediaID+"/other.txt", "")
	if renamed.status != http.StatusOK {
		t.Fatalf("named download: status %d (body %s)", renamed.status, renamed.body)
	}
	if disposition := dispositionOf(t, renamed); disposition.filename != "other.txt" {
		t.Errorf("a named download presented %q, want other.txt", disposition.raw)
	}

	// An upload with no name has no name to present. The bytes are still served; a header that invented a
	// name would be a claim about a file nobody uploaded.
	anonymousName := uploadMedia(t, h, token, []byte("nameless"), "application/octet-stream", "")
	namelessID := mxcID(t, anonymousName.decoded(t))
	nameless := getRaw(t, h, "/_matrix/media/v3/download/hs1/"+namelessID, "")
	if disposition := dispositionOf(t, nameless); disposition.filename != "" {
		t.Errorf("an unnamed upload presented the name %q", disposition.filename)
	}
	if disposition := dispositionOf(t, nameless); disposition.disposition != "attachment" {
		t.Errorf("an unknown content type was offered inline: %q", disposition.raw)
	}
}

// disposition is a parsed Content-Disposition header.
type disposition struct {
	raw         string
	disposition string
	filename    string
}

func dispositionOf(t *testing.T, res response) disposition {
	t.Helper()
	raw := res.header.Get("Content-Disposition")
	if raw == "" {
		t.Fatalf("the response carries no Content-Disposition header")
	}
	main, params, err := mime.ParseMediaType(raw)
	if err != nil {
		t.Fatalf("Content-Disposition %q does not parse: %v", raw, err)
	}
	return disposition{raw: raw, disposition: main, filename: params["filename"]}
}

// TestMediaThumbnails checks the two rules the specification states as MUSTs about thumbnails: nothing is
// enlarged, and a request for a thumbnail of something already smaller gets the original.
func TestMediaThumbnails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "thumbalice", "pw-thumbalice", "")

	large := pngOf(t, 100, 80)
	mediaID := mxcID(t, uploadMedia(t, h, token, large, "image/png", "large.png").decoded(t))

	scaled := getRaw(t, h, "/_matrix/media/v3/thumbnail/hs1/"+mediaID+"?width=32&height=32&method=scale", "")
	if scaled.status != http.StatusOK {
		t.Fatalf("scale thumbnail: status %d (body %s)", scaled.status, scaled.body)
	}
	if got := scaled.header.Get("Content-Type"); got != "image/png" {
		t.Errorf("a thumbnail of a PNG came back as %q", got)
	}
	width, height := dimensionsOf(t, scaled.body)
	// Fitted inside 32x32, keeping the aspect: the limiting axis reaches 32 and the other stays below it.
	if width != 32 || height > 32 || height < 1 {
		t.Errorf("a 100x80 image scaled to 32x32 came back as %dx%d", width, height)
	}
	if width > 100 || height > 80 {
		t.Errorf("a thumbnail is larger than its source: %dx%d of 100x80", width, height)
	}

	cropped := getRaw(t, h, "/_matrix/media/v3/thumbnail/hs1/"+mediaID+"?width=40&height=20&method=crop", "")
	if cropped.status != http.StatusOK {
		t.Fatalf("crop thumbnail: status %d (body %s)", cropped.status, cropped.body)
	}
	if width, height = dimensionsOf(t, cropped.body); width != 40 || height != 20 {
		t.Errorf("a crop to 40x20 came back as %dx%d", width, height)
	}

	// Something already smaller than the request is returned as it is rather than enlarged.
	small := pngOf(t, 10, 10)
	smallID := mxcID(t, uploadMedia(t, h, token, small, "image/png", "small.png").decoded(t))
	untouched := getRaw(t, h, "/_matrix/media/v3/thumbnail/hs1/"+smallID+"?width=64&height=64&method=scale", "")
	if untouched.status != http.StatusOK {
		t.Fatalf("thumbnail of a small image: status %d (body %s)", untouched.status, untouched.body)
	}
	if !bytes.Equal(untouched.body, small) {
		width, height := dimensionsOf(t, untouched.body)
		t.Errorf("a 10x10 image asked for at 64x64 was re-encoded instead of returned; got %dx%d",
			width, height)
	}

	// A request that does not say how big has no answer to give.
	missing := getRaw(t, h, "/_matrix/media/v3/thumbnail/hs1/"+mediaID, "")
	if missing.status != http.StatusBadRequest || missing.errcode(t) != "M_MISSING_PARAMS" {
		t.Errorf("a thumbnail request with no dimensions: status %d %s", missing.status, missing.body)
	}

	// Content that is not an image is refused with the specification's error, not served as a thumbnail of
	// nothing.
	notImage := mxcID(t, uploadMedia(t, h, token, []byte("just words"), "text/plain", "notes.txt").decoded(t))
	refused := getRaw(t, h, "/_matrix/media/v3/thumbnail/hs1/"+notImage+"?width=32&height=32", "")
	if refused.status != http.StatusBadRequest || refused.errcode(t) != "M_UNKNOWN" {
		t.Errorf("thumbnailing a text file: status %d %s", refused.status, refused.body)
	}
}

// dimensionsOf decodes an image and reports its size, failing the test if the bytes are not an image.
func dimensionsOf(t *testing.T, content []byte) (int, int) {
	t.Helper()
	decoded, err := png.Decode(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("the response is not a PNG: %v", err)
	}
	bounds := decoded.Bounds()
	return bounds.Dx(), bounds.Dy()
}

// TestMediaLimitsAndUnknownContent covers the refusals: too large for this server, not here at all, and
// remote content this server was told not to go and fetch.
func TestMediaLimitsAndUnknownContent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "limitalice", "pw-limitalice", "")

	limit := uploadLimitOf(t, h)

	oversized := make([]byte, limit+1024)
	for i := range oversized {
		oversized[i] = byte(i)
	}
	tooLarge := uploadMedia(t, h, token, oversized, "application/octet-stream", "big.bin")
	if tooLarge.status != http.StatusRequestEntityTooLarge || tooLarge.errcode(t) != "M_TOO_LARGE" {
		t.Errorf("an upload of %d bytes against a limit of %d: status %d %s",
			len(oversized), limit, tooLarge.status, tooLarge.body)
	}

	// An unknown media identifier is M_NOT_FOUND, and the URI of another server is not this server's to
	// answer for when the client asked it not to fetch.
	missing := getRaw(t, h, "/_matrix/media/v3/download/hs1/nothinghereatall", "")
	if missing.status != http.StatusNotFound || missing.errcode(t) != "M_NOT_FOUND" {
		t.Errorf("an unknown media identifier: status %d %s", missing.status, missing.body)
	}
	elsewhere := getRaw(t, h, "/_matrix/media/v3/download/elsewhere.example/someid?allow_remote=false", "")
	if elsewhere.status != http.StatusNotFound {
		t.Errorf("remote content with allow_remote=false: status %d %s", elsewhere.status, elsewhere.body)
	}

	// A media identifier is used as a file name, so the character whitelist is a security boundary rather
	// than tidiness. Percent-encoding and UTF-8 tricks are why this is a whitelist.
	for _, traversal := range []string{"..", "../../etc/passwd", "a/b", "..%2f..%2fetc"} {
		res := getRaw(t, h, "/_matrix/media/v3/download/hs1/"+traversal, "")
		if res.status == http.StatusOK {
			t.Errorf("a media identifier %q was served", traversal)
		}
		if res.status != http.StatusBadRequest && res.status != http.StatusNotFound {
			t.Errorf("media identifier %q: status %d, want a refusal of the identifier or an absent file",
				traversal, res.status)
		}
	}

	// The URL preview module is not implemented, and the specification's answer for an endpoint a server
	// does not have is M_UNRECOGNIZED.
	preview := getRaw(t, h, "/_matrix/media/v3/preview_url?url=https://example.com", "")
	if preview.status != http.StatusNotFound || preview.errcode(t) != "M_UNRECOGNIZED" {
		t.Errorf("a URL preview request: status %d %s", preview.status, preview.body)
	}
}

// uploadLimitOf reads the limit the server advertises, which is also the one it enforces.
func uploadLimitOf(t *testing.T, h *harness) int64 {
	t.Helper()
	config := h.get("/_matrix/media/v3/config", "")
	if config.status != http.StatusOK {
		t.Fatalf("media config: status %d (%s)", config.status, config.body)
	}
	value, ok := config.decoded(t)["m.upload.size"].(float64)
	if !ok || value <= 0 {
		t.Fatalf("m.upload.size = %v", config.decoded(t)["m.upload.size"])
	}
	return int64(value)
}

// TestMediaCreateThenUpload covers the two-step upload: reserve a URI, then fill it. Its point is the state
// in between, which has its own answer (M_NOT_YET_UPLOADED) and its own ownership rules.
func TestMediaCreateThenUpload(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, aliceToken, _ := h.register(t, "createalice", "pw-createalice", "")
	_, bobToken, _ := h.register(t, "createbob", "pw-createbob", "")

	created := h.post(t, "/_matrix/media/v1/create", map[string]any{}, aliceToken)
	if created.status != http.StatusOK {
		t.Fatalf("create: status %d (body %s)", created.status, created.body)
	}
	mediaID := mxcID(t, created.decoded(t))
	if _, present := created.decoded(t)["unused_expires_at"]; !present {
		t.Errorf("a reserved URI is not given an expiry: %v", created.decoded(t))
	}

	// Reserved but empty is a different fact from absent, and the specification gives it its own code and
	// status: a client waiting for a slow upload should not be told the file does not exist.
	waiting := getRaw(t, h, "/_matrix/media/v3/download/hs1/"+mediaID, "")
	if waiting.status != http.StatusGatewayTimeout || waiting.errcode(t) != "M_NOT_YET_UPLOADED" {
		t.Errorf("content that has not been uploaded yet: status %d %s", waiting.status, waiting.body)
	}

	// Somebody else's reservation is not this user's to fill.
	impostor := h.raw(t, http.MethodPut, "/_matrix/media/v3/upload/hs1/"+mediaID+"?filename=stolen.txt",
		[]byte("not mine"), bobToken)
	if impostor.status != http.StatusForbidden || impostor.errcode(t) != "M_FORBIDDEN" {
		t.Errorf("filling another user's reservation: status %d %s", impostor.status, impostor.body)
	}
	if still := getRaw(t, h, "/_matrix/media/v3/download/hs1/"+mediaID, ""); still.status != http.StatusGatewayTimeout {
		t.Errorf("the refused upload changed the state of the reservation: status %d %s", still.status, still.body)
	}

	filled := h.raw(t, http.MethodPut, "/_matrix/media/v3/upload/hs1/"+mediaID+"?filename=mine.txt",
		[]byte("at last"), aliceToken)
	if filled.status != http.StatusOK {
		t.Fatalf("filling the reservation: status %d (body %s)", filled.status, filled.body)
	}
	got := getRaw(t, h, "/_matrix/media/v3/download/hs1/"+mediaID, "")
	if got.status != http.StatusOK || string(got.body) != "at last" {
		t.Errorf("the uploaded content: status %d %q", got.status, got.body)
	}
	if disposition := dispositionOf(t, got); disposition.filename != "mine.txt" {
		t.Errorf("the file name given with the body was not kept: %q", disposition.raw)
	}

	// Filling it again is the specification's own refusal, with its own code: the address already has
	// content, and replacing bytes that other people hold links to is not what a second upload means.
	again := h.raw(t, http.MethodPut, "/_matrix/media/v3/upload/hs1/"+mediaID, []byte("again"), aliceToken)
	if again.status != http.StatusConflict || again.errcode(t) != "M_CANNOT_OVERWRITE_MEDIA" {
		t.Errorf("uploading over existing content: status %d %s", again.status, again.body)
	}
	if after := getRaw(t, h, "/_matrix/media/v3/download/hs1/"+mediaID, ""); string(after.body) != "at last" {
		t.Errorf("the refused upload changed the stored content: %q", after.body)
	}

	// A URI that was never reserved cannot be uploaded to: accepting it would let an uploader choose
	// identifiers, including ones already in use.
	neverCreated := h.raw(t, http.MethodPut, "/_matrix/media/v3/upload/hs1/neverreserved", []byte("x"), aliceToken)
	if neverCreated.status != http.StatusNotFound || neverCreated.errcode(t) != "M_NOT_FOUND" {
		t.Errorf("uploading to an unreserved identifier: status %d %s", neverCreated.status, neverCreated.body)
	}

}

// TestMediaIsFetchedFromTheServerThatHoldsIt is the federation half: a server asked for content that
// belongs to another server has to go and ask that server, hold what it was given, and be able to answer
// again without asking twice.
func TestMediaIsFetchedFromTheServerThatHoldsIt(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	aliceToken := hs1.registerUser(t, "mediahost", "pw-mediahost")
	content := pngOf(t, 64, 48)
	uploaded := nodeRaw(t, hs1, http.MethodPost, "/_matrix/media/v3/upload?filename=host.png",
		content, "image/png", aliceToken)
	if uploaded.status != http.StatusOK {
		t.Fatalf("upload on %s: status %d (body %s)", hs1.name, uploaded.status, uploaded.body)
	}
	var answer map[string]any
	if err := json.Unmarshal(uploaded.body, &answer); err != nil {
		t.Fatalf("the upload answered something unparseable: %s", uploaded.body)
	}
	uri, _ := answer["content_uri"].(string)
	if uri == "" {
		t.Fatalf("no content_uri: %v", answer)
	}
	_, mediaID := mxcOf(t, uri)

	bobToken := hs2.registerUser(t, "mediaguest", "pw-mediaguest")

	// hs2 does not hold this content, so it asks hs1 over federation and keeps what it is given.
	first := nodeRaw(t, hs2, http.MethodGet, "/_matrix/media/v3/download/hs1/"+mediaID, nil, "", bobToken)
	if first.status != http.StatusOK {
		t.Fatalf("%s could not fetch %s's content: status %d (body %s)", hs2.name, hs1.name, first.status, first.body)
	}
	if !bytes.Equal(first.body, content) {
		t.Errorf("%s handed back %d bytes, not the %d bytes %s holds",
			hs2.name, len(first.body), len(content), hs1.name)
	}
	if got := first.header.Get("Content-Type"); got != "image/png" {
		t.Errorf("%s reported the fetched content as %q", hs2.name, got)
	}

	// The second request is answered from what is now held here. The way to see that is to take hs1's
	// content away: a server that fetched again would answer "not found".
	if err := removeMediaOf(hs1, mediaID); err != nil {
		t.Fatalf("cannot take %s's content away for the second request: %v", hs1.name, err)
	}
	second := nodeRaw(t, hs2, http.MethodGet, "/_matrix/media/v3/download/hs1/"+mediaID, nil, "", bobToken)
	if second.status != http.StatusOK || !bytes.Equal(second.body, content) {
		t.Errorf("%s did not answer the second request from its own copy: status %d, %d bytes",
			hs2.name, second.status, len(second.body))
	}

	// A thumbnail of remote content is made here, from the bytes held here: a thumbnail is this server's
	// statement about the content, not somebody else's reduced copy passed through.
	thumb := nodeRaw(t, hs2, http.MethodGet, "/_matrix/media/v3/thumbnail/hs1/"+mediaID+
		"?width=16&height=16&method=scale", nil, "", bobToken)
	if thumb.status != http.StatusOK {
		t.Fatalf("thumbnail of remote content on %s: status %d (body %s)", hs2.name, thumb.status, thumb.body)
	}
	if width, height := dimensionsOf(t, thumb.body); width > 64 || height > 48 || width > 16 {
		t.Errorf("a thumbnail of remote content came back as %dx%d", width, height)
	}
}

// hs1Raw and hs2Raw are one helper for the federation nodes, which hold their own client.
func nodeRaw(t *testing.T, node *fedNode, method, path string, content []byte, contentType, token string) response {
	t.Helper()
	var body io.Reader
	if content != nil {
		body = bytes.NewReader(content)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, node.client+path, body)
	if err != nil {
		t.Fatalf("cannot build the request for %s: %v", path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := node.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("cannot read the answer from %s: %v", path, err)
	}
	return response{status: res.StatusCode, header: res.Header, body: payload}
}

// removeMediaOf deletes one piece of content from a node's repository directory, which is how a test shows
// that a second request was answered from a copy rather than by asking again.
func removeMediaOf(node *fedNode, mediaID string) error {
	return os.Remove(node.app.Cfg.Media.Path + "/" + node.name + "/" + mediaID)
}
