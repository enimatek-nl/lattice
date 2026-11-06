package media

import (
	"fmt"
	"strconv"
	"strings"
)

// How content is presented to a browser is a security decision the specification makes for the server, so
// it is written here once and used by the client API, the deprecated media API and the federation API
// alike. Getting it wrong in one of the three is still wrong for the user whose client fetches from that
// one.

// inlineSafeTypes are the content types the Client-Server API allows to be served with
// `Content-Disposition: inline`. Anything else has to be an attachment.
//
// The list is an allow-list rather than a blocklist of `text/html` because the danger is a client rendering
// a document in the homeserver's origin: a type nobody thought of is exactly the one that carries a
// cross-site scripting bug. Trusting the uploader's declared type is acceptable for the same reason — a
// client only ever renders according to the header this server sends.
var inlineSafeTypes = map[string]bool{
	"text/css": true, "text/plain": true, "text/csv": true,
	"application/json": true, "application/ld+json": true,
	"image/jpeg": true, "image/gif": true, "image/png": true, "image/apng": true,
	"image/webp": true, "image/avif": true,
	"video/mp4": true, "video/webm": true, "video/ogg": true, "video/quicktime": true,
	"audio/mp4": true, "audio/webm": true, "audio/aac": true, "audio/mpeg": true,
	"audio/ogg": true, "audio/wave": true, "audio/wav": true, "audio/x-wav": true,
	"audio/x-pn-wav": true, "audio/flac": true, "audio/x-flac": true,
}

// DispositionFor answers whether content of this type may be shown inline.
//
// An unknown or empty type becomes `attachment`, which is the safe direction: the specification explicitly
// permits servers to answer every download with `attachment`, so a type this server has not heard of is not
// a reason to take a risk on behalf of a client that cannot render it anyway.
func DispositionFor(contentType string) string {
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if inlineSafeTypes[mimeType] {
		return "inline"
	}
	return "attachment"
}

// CanonicalType reduces a declared content type to its media type, lower-cased. Parameters such as
// `charset` are kept in the header a client is sent, but the inline decision and the thumbnail decision are
// made on the type alone.
func CanonicalType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

// ContentDisposition builds the header: `inline` or `attachment`, with a file name when one is known.
//
// A name outside ASCII is carried twice: the quoted form for readers that only understand the original
// syntax, and the RFC 5987 `filename*` form, which is the one that actually preserves it. A header is
// nominally ASCII, and a file called ☕.png has to survive that.
func ContentDisposition(disposition, fileName string) string {
	if fileName == "" {
		return disposition
	}
	quoted := strconv.Quote(fileName)
	if isASCII(fileName) {
		return disposition + "; filename=" + quoted
	}
	return disposition + "; filename=" + quoted + "; filename*=UTF-8''" + escapeExtended(fileName)
}

// RecommendedCSP is the content security policy the specification recommends for served media: the file is
// data, never a document with a script in it.
const RecommendedCSP = "sandbox; default-src 'none'; script-src 'none'; plugin-types application/pdf; " +
	"style-src 'unsafe-inline'; object-src 'self';"

// isASCII says a string fits a header without an encoded form.
func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

// escapeExtended percent-encodes what RFC 5987 will not carry literally.
func escapeExtended(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("!#$%&'*+-.:^_~", c) >= 0 {
			out.WriteByte(c)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", c)
	}
	return out.String()
}
