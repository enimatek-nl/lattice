package federation

import (
	"crypto/ed25519"
	"fmt"
	"net/http"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// AuthFields are the parameters of an "Authorization: X-Matrix ..." header. They
// name who signed the request, which of its keys did, and the signature of the
// request itself.
type AuthFields struct {
	// Origin is the server name of the sending server - the identity the
	// signature is made by, before any delegation to another host.
	Origin string
	// Destination is the server name of the receiving server. Senders must
	// always include it; receivers must accept requests without it because older
	// servers do not send it, and must refuse a request when it names somebody
	// else.
	Destination string
	// KeyID is the id, algorithm included, of the key that signed.
	KeyID string
	// Signature is the base64 signature of the request object.
	Signature string
}

// AuthorizationHeader renders the header value. The specification's compatibility
// advice is followed to the letter: one space after the scheme, lower-case
// parameter names, no whitespace around the commas, no backslashes in values. The
// signature parameter is written as "sig" because that is the spelling in the
// specification's own example and in the reference implementation; its parameter
// list calls it "signature", and [ParseAuthorization] accepts both.
func (f AuthFields) AuthorizationHeader() string {
	var b strings.Builder
	b.WriteString("X-Matrix ")
	write := func(name, value string) {
		if b.Len() > len("X-Matrix ") {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%s=%q", name, value)
	}
	write("origin", f.Origin)
	if f.Destination != "" {
		write("destination", f.Destination)
	}
	write("key", f.KeyID)
	write("sig", f.Signature)
	return b.String()
}

// ParseAuthorization reads the value of an X-Matrix Authorization header.
//
// The grammar is the auth-parameter form of RFC 9110 section 11.4: the scheme,
// then comma-separated name=value pairs, values either quoted (with backslash
// escapes, which the recipient must unescape) or bare tokens. Parameter names are
// case-insensitive, unknown parameters are ignored, and for compatibility with
// older servers a bare value may contain colons - which matters because key ids
// are "ed25519:something" and a sender that forgets to quote them would otherwise
// be unreadable.
func ParseAuthorization(value string) (AuthFields, error) {
	rest := strings.TrimSpace(value)
	scheme, rest, found := strings.Cut(rest, " ")
	if !found || !strings.EqualFold(scheme, "X-Matrix") {
		return AuthFields{}, fmt.Errorf("the authorization scheme is %q, not X-Matrix", scheme)
	}
	var fields AuthFields
	seen := map[string]bool{}
	for {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		name, tail, more := strings.Cut(rest, "=")
		if !more {
			return AuthFields{}, fmt.Errorf("the authorization parameter %q has no value", name)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		rest = tail
		var param string
		if strings.HasPrefix(rest, `"`) {
			decoded, tail, err := readQuoted(rest)
			if err != nil {
				return AuthFields{}, err
			}
			param, rest = decoded, tail
		} else {
			// A bare token runs to the next comma; colons are allowed inside it. The
			// comma is left in place because the loop below expects the separator to
			// still be there, and consuming it here makes every header that quotes
			// nothing fail to parse.
			if end := strings.IndexByte(rest, ','); end >= 0 {
				param, rest = rest[:end], rest[end:]
			} else {
				param, rest = rest, ""
			}
			param = strings.TrimSpace(param)
		}
		seen[name] = true
		switch name {
		case "origin":
			fields.Origin = param
		case "destination":
			fields.Destination = param
		case "key":
			fields.KeyID = param
		case "sig", "signature":
			fields.Signature = param
		}
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, ",") {
			return AuthFields{}, fmt.Errorf("unexpected text in the authorization header: %q", rest)
		}
		rest = rest[1:]
	}
	if fields.Origin == "" {
		return AuthFields{}, fmt.Errorf("the authorization header names no origin")
	}
	if fields.KeyID == "" {
		return AuthFields{}, fmt.Errorf("the authorization header names no key")
	}
	if !seen["sig"] && !seen["signature"] {
		return AuthFields{}, fmt.Errorf("the authorization header carries no signature")
	}
	return fields, nil
}

// readQuoted reads a quoted auth parameter, unescaping backslash pairs, and
// returns the value plus whatever follows the closing quote.
func readQuoted(value string) (string, string, error) {
	var out strings.Builder
	for i := 1; i < len(value); i++ {
		switch value[i] {
		case '\\':
			if i+1 >= len(value) {
				return "", "", fmt.Errorf("the quoted authorization value ends with a backslash")
			}
			out.WriteByte(value[i+1])
			i++
		case '"':
			return out.String(), value[i+1:], nil
		default:
			out.WriteByte(value[i])
		}
	}
	return "", "", fmt.Errorf("the quoted authorization value is not closed")
}

// requestObject builds the JSON object a signature is made over: the method, the
// exact request target, the two server names, and the body.
//
// The uri is the path plus query as it is sent - no scheme, no host - because the
// peer verifies over the target it received, so a transport that re-encoded the
// query string on the way out would fail its own signature.
//
// content is the parsed body, and nil means the request has no body and therefore
// signs no content member at all. A GET is always in that case, whatever the
// caller passes, and this is not a detail: Synapse leaves the member out of a GET's
// signed object and gomatrixserverlib marshals it with "omitempty", so a server
// that substitutes an empty object verifies a different document from the one the
// peer signed. It then refuses every signed GET that peer makes - including the
// make_join a federated join begins with - and has its own signed GETs refused in
// return. Both sides were tested against each other before this was written down.
func requestObject(method, uri, origin, destination string, content matrix.Object) matrix.Object {
	object := matrix.Object{
		"method":      strings.ToUpper(method),
		"uri":         uri,
		"origin":      origin,
		"destination": destination,
	}
	if content != nil && strings.ToUpper(method) != http.MethodGet {
		object["content"] = content
	}
	return object
}

// SignAuthorization produces the header for an outgoing request.
func SignAuthorization(key KeyPair, method, uri, origin, destination string, content matrix.Object) (AuthFields, error) {
	object := requestObject(method, uri, origin, destination, content)
	signature, err := key.Sign(object)
	if err != nil {
		return AuthFields{}, err
	}
	return AuthFields{
		Origin:      origin,
		Destination: destination,
		KeyID:       key.ID,
		Signature:   signature,
	}, nil
}

// VerifyAuthorization checks the signature of an incoming request against the
// public key the caller resolved for fields.KeyID. content follows the same rule
// as when signing: nil for a GET or a request with no body, the parsed body
// otherwise.
//
// The signature is checked over an object whose signatures member holds the value
// being verified, which is how matrix.VerifyEd25519Signature reads it: that helper
// removes the signatures member again before canonicalising, so the bytes that go
// into the verification are the ones the sender signed.
//
// A sender that left destination out of the header - which older servers do, and
// receivers are told to accept - signed an object without that member, so the
// member is dropped here too rather than compared against an empty string.
func VerifyAuthorization(fields AuthFields, method, uri string, content matrix.Object, publicKey ed25519.PublicKey) error {
	object := requestObject(method, uri, fields.Origin, fields.Destination, content)
	if fields.Destination == "" {
		delete(object, "destination")
	}
	object["signatures"] = matrix.Object{
		fields.Origin: matrix.Object{fields.KeyID: fields.Signature},
	}
	return matrix.VerifyEd25519Signature(object, fields.Origin, fields.KeyID, publicKey)
}
