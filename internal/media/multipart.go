package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

// The federation content repository speaks `multipart/mixed`: a metadata part and a content part. Both
// directions live here — the answer this server gives, and the answer this server reads from a peer — so a
// mistake cannot hide in one direction and be papered over in the other.

// multipartMetadataType is the content type the specification gives the first part. It currently always
// holds an empty object; sending it anyway is what makes the answer the specified shape rather than a shape
// that happens to work.
const multipartMetadataType = "application/json"

// WriteMultipartResponse answers a federation media request: `multipart/mixed` with a metadata part and then
// the content, streamed rather than buffered.
func WriteMultipartResponse(w http.ResponseWriter, contentType, fileName string, body io.Reader) error {
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())

	metadata, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": []string{multipartMetadataType}})
	if err != nil {
		return err
	}
	if _, err := metadata.Write([]byte("{}")); err != nil {
		return err
	}

	header := textproto.MIMEHeader{"Content-Type": []string{contentType}}
	if fileName != "" {
		header["Content-Disposition"] = []string{ContentDisposition("attachment", fileName)}
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, body); err != nil {
		return err
	}
	return writer.Close()
}

// RemoteMediaPart is what a peer's multipart answer carried.
type RemoteMediaPart struct {
	Content     []byte
	ContentType string
	FileName    string
	// Location is the URL the second part pointed at instead of carrying bytes. The specification allows a
	// peer to do that; this server does not follow it (see ParseMultipartResponse), and the caller needs to
	// know that the content it asked for was not in the answer.
	Location string
}

// ParseMultipartResponse reads a peer's media answer.
//
// The specified shape is two parts: JSON metadata, then the content — either its bytes or a `Location`
// header pointing at them. A body that is not multipart at all is accepted as the content itself, because
// that is what the deprecated endpoints and the servers that predate the federation endpoint actually send;
// Complement's own federation fixture answers the federation path that way too. Knowing which format was
// promised where is not the same as accepting anything.
//
// A `Location` is reported rather than followed. The URL comes from a federation peer, and fetching whatever
// a peer names — including an address on this server's own network — is the shape of attack that server-name
// resolution and the federation IP policy exist to prevent. Following one needs its own resolution and
// address checks, so it stays a task rather than becoming a line of code.
func ParseMultipartResponse(header http.Header, body []byte) (RemoteMediaPart, error) {
	mimeType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		mimeType = strings.ToLower(strings.TrimSpace(strings.Split(header.Get("Content-Type"), ";")[0]))
	}
	if mimeType != "multipart/mixed" {
		return RemoteMediaPart{
			Content:     body,
			ContentType: mimeType,
			FileName:    dispositionFileName(header.Get("Content-Disposition")),
		}, nil
	}

	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	first, err := reader.NextPart()
	if err != nil {
		return RemoteMediaPart{}, fmt.Errorf("the media answer has no metadata part: %w", err)
	}
	metadataBody, err := io.ReadAll(first)
	if err != nil {
		return RemoteMediaPart{}, fmt.Errorf("the media answer's metadata could not be read: %w", err)
	}
	if partType := first.Header.Get("Content-Type"); partType != "" && !strings.HasPrefix(partType, multipartMetadataType) {
		return RemoteMediaPart{}, fmt.Errorf("the media answer's first part is %s, not metadata", partType)
	}
	var metadata map[string]any
	if err := json.Unmarshal(metadataBody, &metadata); err != nil {
		return RemoteMediaPart{}, fmt.Errorf("the media answer's metadata is not a JSON object: %w", err)
	}

	second, err := reader.NextPart()
	if err != nil {
		return RemoteMediaPart{}, fmt.Errorf("the media answer has no content part: %w", err)
	}
	if location := second.Header.Get("Location"); location != "" {
		part, err := io.ReadAll(second)
		if err != nil {
			return RemoteMediaPart{}, fmt.Errorf("the media answer's content part could not be read: %w", err)
		}
		return RemoteMediaPart{
			Content:     part,
			ContentType: second.Header.Get("Content-Type"),
			Location:    location,
		}, nil
	}
	content, err := io.ReadAll(second)
	if err != nil {
		return RemoteMediaPart{}, fmt.Errorf("the media answer's bytes could not be read: %w", err)
	}
	partType := second.Header.Get("Content-Type")
	if partType == "" {
		partType = "application/octet-stream"
	}
	return RemoteMediaPart{
		Content:     content,
		ContentType: partType,
		FileName:    dispositionFileName(second.Header.Get("Content-Disposition")),
	}, nil
}

// dispositionFileName reads the file name out of a Content-Disposition header. A header that is absent or
// malformed yields no name: it only ever names a download, so a mistake in it is not a reason to refuse the
// bytes.
func dispositionFileName(value string) string {
	if value == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return params["filename"]
}
