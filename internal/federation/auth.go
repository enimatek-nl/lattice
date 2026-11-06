// Package federation talks to other homeservers: it finds them, signs requests the way
// the Server-Server API requires, checks signatures the same servers make, and moves
// transactions. Everything in here treats the remote server as untrusted: a homeserver
// on the Matrix network is a stranger with a key, not a peer to be believed.
package federation

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/enimatek/lattice/internal/matrixjson"
)

// Signer signs a JSON object with this server's current signing key. *keys.Service
// implements it; taking the interface keeps the request signing testable without
// loading key files.
type Signer interface {
	// KeyID is the ID of the key a signature will be attributed to.
	KeyID() string
	// SignJSON adds this server's signature to obj, in place, as Signing JSON does.
	SignJSON(obj map[string]any) error
}

// Authorization is a parsed X-Matrix parameter set.
type Authorization struct {
	Origin      string
	Destination string
	Key         string
	Signature   string
}

// ErrNoAuthorization says a request carried no X-Matrix credential at all.
var ErrNoAuthorization = errors.New("federation: the request carries no X-Matrix authorization")

// AuthorizationHeader signs one request and returns the value of its Authorization
// header.
//
// The signed object is the one the specification names, with the specification's keys:
// `method`, `uri`, `origin`, `destination`, and `content` only when there is a body — an
// empty content object is a different document from no content at all, and would break the
// signature. It is built as a map on both sides because the keys that must be absent differ
// between requests, and canonical JSON cares exactly.
//
// Naming the destination is what stops a signature made for one homeserver being replayed
// against another, which is why a receiver must check it.
func AuthorizationHeader(origin string, signer Signer, destination, method, target string, body []byte) (string, error) {
	if origin == "" || destination == "" {
		return "", errors.New("federation: signing a request needs an origin and a destination")
	}
	if signer == nil {
		return "", errors.New("federation: signing a request needs a signer")
	}
	object := map[string]any{
		"method":      strings.ToUpper(method),
		"uri":         target,
		"origin":      origin,
		"destination": destination,
	}
	if len(body) > 0 {
		content, err := matrixjson.Decode(body)
		if err != nil {
			return "", fmt.Errorf("federation: cannot sign a request whose body is not JSON: %w", err)
		}
		object["content"] = content
	}
	if err := signer.SignJSON(object); err != nil {
		return "", fmt.Errorf("federation: cannot sign the request: %w", err)
	}
	signatures, _ := object["signatures"].(map[string]any)
	byKey, _ := signatures[origin].(map[string]any)
	signature, _ := byKey[signer.KeyID()].(string)
	if signature == "" {
		return "", errors.New("federation: signing produced no signature under this server's key ID")
	}
	return fmt.Sprintf("X-Matrix origin=%q,destination=%q,key=%q,sig=%q",
		origin, destination, signer.KeyID(), signature), nil
}

// ParseAuthorization reads an X-Matrix header value.
//
// The names are case-insensitive, values may be quoted, a quoted value may contain
// backslash escapes, and whitespace around the commas is allowed. Older servers put
// colons in values that need no quoting, so a value is taken up to the next comma
// rather than the next colon.
func ParseAuthorization(header string) (Authorization, error) {
	value := strings.TrimSpace(header)
	if value == "" {
		return Authorization{}, ErrNoAuthorization
	}
	scheme, params, found := strings.Cut(value, " ")
	if !found || !strings.EqualFold(strings.TrimSpace(scheme), "X-Matrix") {
		return Authorization{}, fmt.Errorf("federation: the authorization scheme is %q, not X-Matrix", strings.TrimSpace(scheme))
	}
	auth := Authorization{}
	for _, pair := range splitParams(params) {
		name, raw, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		unquoted, err := unquote(strings.TrimSpace(raw))
		if err != nil {
			return Authorization{}, fmt.Errorf("federation: the %s parameter is malformed: %w", name, err)
		}
		switch name {
		case "origin":
			auth.Origin = unquoted
		case "destination":
			auth.Destination = unquoted
		case "key":
			auth.Key = unquoted
		case "sig", "signature":
			auth.Signature = unquoted
		}
		// Unknown parameters are ignored, as the specification says.
	}
	if auth.Origin == "" || auth.Key == "" || auth.Signature == "" {
		return Authorization{}, fmt.Errorf("federation: the X-Matrix credential is missing origin, key or sig")
	}
	return auth, nil
}

// splitParams splits on commas that are not inside a quoted value.
func splitParams(s string) []string {
	var parts []string
	var current strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' && (i == 0 || s[i-1] != '\\'):
			quoted = !quoted
			current.WriteByte(c)
		case c == ',' && !quoted:
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// unquote removes the surrounding quotes and resolves backslash escapes.
func unquote(s string) (string, error) {
	if !strings.HasPrefix(s, `"`) {
		return s, nil
	}
	if !strings.HasSuffix(s, `"`) || len(s) < 2 {
		return "", errors.New("a quoted value is not closed")
	}
	body := s[1 : len(s)-1]
	var out strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			out.WriteByte(body[i])
			continue
		}
		if i+1 >= len(body) {
			return "", errors.New("a value ends with a backslash")
		}
		i++
		out.WriteByte(body[i])
	}
	return out.String(), nil
}

// VerifyRequest checks an X-Matrix credential against the request it arrived with.
//
// expectedDestination is this server's name. A credential naming somebody else is
// refused: the signature is over an object that includes the destination precisely so
// that it cannot be carried to another homeserver and used there.
//
// The request body is read here and returned, because the signature covers the parsed
// body and the handler needs the same bytes to parse again.
func VerifyRequest(r *http.Request, expectedDestination string, lookup func(origin, keyID string) (ed25519.PublicKey, error)) (Authorization, []byte, error) {
	auth, err := ParseAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		return Authorization{}, nil, err
	}
	if auth.Destination != "" && auth.Destination != expectedDestination {
		return Authorization{}, nil, fmt.Errorf("federation: the credential is addressed to %q, not to this server (%q)",
			auth.Destination, expectedDestination)
	}
	var body []byte
	if r.Body != nil {
		body, err = readAll(r, maxFederationAuthBodyBytes)
		if err != nil {
			return Authorization{}, nil, err
		}
	}
	object := map[string]any{
		"method":      strings.ToUpper(r.Method),
		"uri":         r.URL.RequestURI(),
		"origin":      auth.Origin,
		"destination": auth.Destination,
	}
	if auth.Destination == "" {
		// Older servers omit the parameter, and then omit the field it would have
		// carried. Leaving the empty key in the object would change what is signed.
		delete(object, "destination")
	}
	if len(body) > 0 {
		content, err := matrixjson.Decode(body)
		if err != nil {
			return Authorization{}, nil, fmt.Errorf("federation: the request body is not JSON, so its signature cannot be checked: %w", err)
		}
		object["content"] = content
	}
	// The signature travels in a header, but signing JSON is defined over an object
	// that carries its signatures inline. Rebuilding that object is what makes the
	// check the same one the sender performed.
	object["signatures"] = map[string]any{auth.Origin: map[string]any{auth.Key: auth.Signature}}
	result, err := matrixjson.VerifyJSON(object, lookup)
	if err != nil {
		return Authorization{}, nil, fmt.Errorf("federation: the credential cannot be checked: %w", err)
	}
	if !result.VerifiedAll([]string{auth.Origin}) {
		// The reason the signature did not verify is kept in the failure record, and it is worth more than
		// the refusal alone: "the key was not published", "the key could not be fetched" and "the signature
		// does not match" are three different problems for whoever is debugging a room that will not federate.
		reason := result.Failures[auth.Origin]
		if reason == "" {
			reason = "no signature from that server verified"
		}
		return Authorization{}, nil, fmt.Errorf(
			"federation: the credential from %s is not signed by a key that server published under %s: %s",
			auth.Origin, auth.Key, reason)
	}
	return auth, body, nil
}

// maxFederationAuthBodyBytes bounds what request signing will read from a stranger.
// Federation bodies are larger than client bodies, and a transaction is capped at 50
// PDUs; 20 MiB is the existing federation body limit in this server.
const maxFederationAuthBodyBytes = 20 << 20

// readAll reads a request body up to a limit and puts it back for the handler, which
// has to parse the same bytes the signature covers.
func readAll(r *http.Request, limit int64) ([]byte, error) {
	if r.ContentLength > limit {
		return nil, fmt.Errorf("federation: the request body is %d bytes, over the %d byte limit", r.ContentLength, limit)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("federation: cannot read the request body: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("federation: the request body is over the %d byte limit", limit)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
