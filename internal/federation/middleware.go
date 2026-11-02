package federation

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// maxRequestBody caps how much of a peer's request body is buffered for the
// signature check. A transaction is bounded by the specification at 50 PDUs and
// 100 EDUs; anything past a comfortable multiple of that is either a broken peer
// or an attack, and the signature has to be made over the whole body either way.
const maxRequestBody = 10 << 20

// peerContextKey carries the authenticated identity of the remote server that
// made a request. It is unexported so nothing outside this package can put a
// server name on a context that was not signed for.
type peerContextKey struct{}

// Peer reports the server name of the authenticated peer that sent this request,
// or "" when the request was not authenticated. Handlers that act on a remote
// server's word - accepting a transaction, signing a join - read the origin from
// here rather than from a body field, because the body is the peer's claim while
// the signature is the proof.
func Peer(ctx context.Context) string {
	name, _ := ctx.Value(peerContextKey{}).(string)
	return name
}

// Authenticate wraps a federation handler and refuses every request this server
// cannot attribute to a known peer.
//
// The checks are the specification's, in the order that fails cheapest first:
// a readable X-Matrix header, a destination that names this server when it names
// anybody (an older peer may leave the parameter out, and a receiver must accept
// that), a key the origin server actually publishes, and then the signature over
// the request as it was received - method, raw request target, the two server
// names, and the body. A failure answers 401 M_UNAUTHORIZED: the request is not
// malformed, it is unauthenticated, and the difference matters to a client that
// is deciding whether to retry.
func (t *Transport) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
		if err != nil {
			t.refuse(w, r, matrix.ErrNotJSON("could not read the request body"))
			return
		}
		// The handler still has to be able to read the body after the signature
		// was made over it, so the request keeps the bytes that were verified.
		r.Body = io.NopCloser(bytes.NewReader(body))

		fields, err := ParseAuthorization(r.Header.Get("Authorization"))
		if err != nil {
			t.refuse(w, r, matrix.ErrUnauthorized("%v", err))
			return
		}
		if fields.Destination != "" && fields.Destination != t.ServerName() {
			t.refuse(w, r, matrix.ErrUnauthorized(
				"the request is addressed to %s, not to %s", fields.Destination, t.ServerName()))
			return
		}
		// A GET carries its origin in the query string as well; a disagreement
		// between the two is a peer that signed one request and sent another.
		if r.Method == http.MethodGet {
			if queryOrigin := r.URL.Query().Get("origin"); queryOrigin != "" && queryOrigin != fields.Origin {
				t.refuse(w, r, matrix.ErrUnauthorized(
					"the query names origin %s while the signature is by %s", queryOrigin, fields.Origin))
				return
			}
		}

		var content matrix.Object
		if r.Method != http.MethodGet {
			decoded, failure := t.contentToVerify(body)
			if failure != nil {
				t.refuse(w, r, failure)
				return
			}
			content = decoded
		}

		publicKey, err := t.VerifyKeyOf(r.Context(), fields.Origin, fields.KeyID)
		if err != nil {
			t.refuse(w, r, matrix.ErrUnauthorized("%v", err))
			return
		}
		if err := VerifyAuthorization(fields, r.Method, requestTarget(r), content, publicKey); err != nil {
			t.refuse(w, r, matrix.ErrUnauthorized("the request from %s is not signed correctly: %v", fields.Origin, err))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerContextKey{}, fields.Origin)))
	})
}

// contentToVerify decodes a request body into the object the signature covers.
// Canonical decoding is not a nicety here: a timestamp that goes through float64
// can come out with different digits, and then a correctly signed request fails
// its own signature check.
//
// An absent body signs no content member, so it decodes to nil rather than to an
// empty object: that is what a sender with nothing to send signs, and adding a
// member would verify a different document.
func (t *Transport) contentToVerify(body []byte) (matrix.Object, *matrix.Error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	decoded, err := matrix.DecodeCanonical(body)
	if err != nil {
		return nil, matrix.ErrNotJSON("the request body is not valid JSON: %v", err)
	}
	object, ok := decoded.(matrix.Object)
	if !ok {
		return nil, matrix.ErrNotJSON("the request body is not a JSON object")
	}
	return object, nil
}

// requestTarget returns the path and query exactly as the peer sent them. The
// signature covers that string, so it has to be the request line's target rather
// than a re-encoding of the parsed URL: percent-escapes, parameter order and
// empty values all survive in the raw form and none of them survive a round trip
// through net/url.
func requestTarget(r *http.Request) string {
	if r.RequestURI != "" {
		return r.RequestURI
	}
	return r.URL.RequestURI()
}

// refuse answers a request that could not be attributed to a peer. These failures
// are logged because they are almost always an operational problem - a peer with
// a stale key, a proxy that rewrote the request target - and the reason is
// invisible to the other side.
func (t *Transport) refuse(w http.ResponseWriter, r *http.Request, err *matrix.Error) {
	log.Printf("federation: refusing %s %s from %s: %s", r.Method, requestTarget(r),
		orUnknown(r.Header.Get("Authorization")), err.Message)
	auth.WriteError(w, err)
}

// orUnknown digs the claimed origin out of an authorization header for the log,
// without treating a parse failure as a reason to say nothing.
func orUnknown(authorization string) string {
	fields, err := ParseAuthorization(authorization)
	if err != nil || fields.Origin == "" {
		return "an unauthenticated peer"
	}
	return fields.Origin
}

// IsFederationPath reports whether a request path belongs to the server-server
// API. It is exported for the API layer, which keeps federation routes out of the
// mux entirely when the flag is off.
func IsFederationPath(path string) bool {
	return strings.HasPrefix(path, "/_matrix/federation/") || strings.HasPrefix(path, "/_matrix/key/v2/")
}
