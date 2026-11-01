// Package federation implements the Matrix server-server layer: the signing
// identity of this homeserver, the signed transport to peer servers, and the
// verification of the signatures those servers present. The HTTP handlers that
// expose it live in internal/api, and the room logic it feeds lives in
// internal/service; this package holds no storage of its own and talks to the
// database only through the [Identity] the caller injects.
//
// Everything here is written against the v1.19 server-server specification. Two
// of its rules shape the whole package: a server is identified by the ed25519
// keys it publishes (so a key that changes on restart invalidates everything the
// server signed before), and every request is authenticated by a signature over
// the exact request target the peer received (so the transport has to sign the
// bytes it is about to send, not a re-encoding of them).
package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// KeyAlgorithm is the only signing algorithm the specification defines for
// server keys.
const KeyAlgorithm = "ed25519"

// MaxKeyValidity caps how long a published key document is trusted, however far
// ahead its own valid_until_ts reaches. The specification requires it: without a
// cap, a compromised server could publish a key valid for years and leave the
// owner no way to revoke it.
const MaxKeyValidity = 7 * 24 * time.Hour

// keyVersionLength is the length other implementations use for the version part
// of a key id. It is arbitrary, but a short version reads as a mistake in a
// key id such as "ed25519:0kBpa0".
const keyVersionLength = 8

// versionAlphabet is the character set the specification allows in the version
// part of a key id: [a-zA-Z0-9_].
const versionAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"

// KeyPair is one signing keypair of this homeserver.
type KeyPair struct {
	// Version is the part after the colon in the key id.
	Version string
	// ID is the key id peers see: "ed25519:" plus the version.
	ID string
	// Public is the half published under verify_keys.
	Public ed25519.PublicKey
	// Private is the half that signs. It never leaves this process.
	Private ed25519.PrivateKey
}

// NewKeyPair generates a fresh signing pair. The version is random rather than
// derived from the key so that rotating a key cannot be anticipated by anyone
// who already knows the pair.
func NewKeyPair() (KeyPair, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generating an ed25519 key: %w", err)
	}
	version, err := randomKeyVersion()
	if err != nil {
		return KeyPair{}, err
	}
	return KeyPair{
		Version: version,
		ID:      matrix.SignatureKeyID(KeyAlgorithm, version),
		Public:  public,
		Private: private,
	}, nil
}

// randomKeyVersion draws keyVersionLength characters from versionAlphabet.
func randomKeyVersion() (string, error) {
	max := big.NewInt(int64(len(versionAlphabet)))
	out := make([]byte, keyVersionLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("drawing a key version: %w", err)
		}
		out[i] = versionAlphabet[n.Int64()]
	}
	return string(out), nil
}

// KeyPairFromStorage rebuilds a pair from the row that holds it. The seed is the
// 32-byte form the storage keeps; the expanded key Go needs is derived from it.
func KeyPairFromStorage(keyID, publicKey, privateSeed string) (KeyPair, error) {
	algorithm, version, found := strings.Cut(keyID, ":")
	if !found || algorithm != KeyAlgorithm || version == "" {
		return KeyPair{}, fmt.Errorf("key id %q is not an %s key id", keyID, KeyAlgorithm)
	}
	seed, err := base64.RawStdEncoding.DecodeString(privateSeed)
	if err != nil {
		seed, err = base64.StdEncoding.DecodeString(privateSeed)
		if err != nil {
			return KeyPair{}, fmt.Errorf("decoding the private seed: %w", err)
		}
	}
	if len(seed) != ed25519.SeedSize {
		return KeyPair{}, fmt.Errorf("private seed is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	private := ed25519.NewKeyFromSeed(seed)
	public, ok := private.Public().(ed25519.PublicKey)
	if !ok {
		return KeyPair{}, fmt.Errorf("the private key has no ed25519 public half")
	}
	published, err := matrix.DecodeEd25519Key(publicKey)
	if err != nil {
		return KeyPair{}, fmt.Errorf("stored public key: %w", err)
	}
	if !published.Equal(public) {
		return KeyPair{}, fmt.Errorf("the stored public key does not belong to the stored seed")
	}
	return KeyPair{Version: version, ID: keyID, Public: public, Private: private}, nil
}

// Seed encodes the private half in the form storage keeps, for the row that is
// written once when a key is first generated.
func (k KeyPair) Seed() string {
	return base64.RawStdEncoding.EncodeToString(k.Private.Seed())
}

// PublicKey encodes the public half the way the key endpoints publish it.
func (k KeyPair) PublicKey() string {
	return base64.RawStdEncoding.EncodeToString(k.Public)
}

// Sign makes the signature of object under this key, over the canonical form of
// the object with signatures and unsigned blocks removed. The returned value is
// the unpadded base64 a JSON signature field carries.
func (k KeyPair) Sign(object matrix.Object) (string, error) {
	raw, err := matrix.SignEd25519(k.Private, object)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(raw), nil
}

// KeyDocument builds the object that GET /_matrix/key/v2/server answers, signed
// by its own key. The response is self-signed on purpose: a notary adds a second
// signature later, and a server that trusts a peer directly checks this one.
func KeyDocument(serverName string, key KeyPair, validUntilMS int64) (matrix.Object, error) {
	document := matrix.Object{
		"server_name": serverName,
		"verify_keys": matrix.Object{
			key.ID: matrix.Object{"key": key.PublicKey()},
		},
		// Rotation is not implemented, so there are no retired keys to name.
		// The field stays present because clients and notaries read it as a map.
		"old_verify_keys": matrix.Object{},
		"valid_until_ts":  validUntilMS,
	}
	signature, err := key.Sign(document)
	if err != nil {
		return nil, err
	}
	document["signatures"] = matrix.Object{
		serverName: matrix.Object{key.ID: signature},
	}
	return document, nil
}

// KeySet is the verify keys of one server, with the validity window that was
// read from its published document.
type KeySet struct {
	// ServerName is the name the document claims, checked against the name that
	// was asked for.
	ServerName string
	// ValidUntilMS is the document's own expiry.
	ValidUntilMS int64
	// Keys maps key id to public key, for verifying signatures.
	Keys map[string]ed25519.PublicKey
	// OldKeys holds retired keys, which still verify old events but never sign
	// requests.
	OldKeys map[string]ed25519.PublicKey
}

// KeyIDs lists the key ids a verified set publishes, in sorted order. Callers
// iterate it when they sign or report a key, and a stable order keeps the answer of
// two runs of the same server comparable.
func (s *KeySet) KeyIDs() []string {
	ids := make([]string, 0, len(s.Keys))
	for keyID := range s.Keys {
		ids = append(ids, keyID)
	}
	sort.Strings(ids)
	return ids
}

// VerifyKeyDocument checks a published key document and returns its keys.
//
// The checks are the ones a server has to make before it trusts a signature with
// the keys inside: the document must name the server that was asked for (a
// notary that answers for someone else must not hand over keys under the wrong
// name), every key must be an ed25519 key with a specification-shaped version,
// the document must be signed by one of its own keys (which is what binds the
// keys to the name), and valid_until_ts must not have passed. The document's own
// expiry is additionally capped at [MaxKeyValidity] by [KeySet.Expires], which
// is the rule the specification imposes so a long-dated key cannot outlive a
// compromise.
func VerifyKeyDocument(document matrix.Object, serverName string, nowMS int64) (*KeySet, error) {
	claimed, _ := document["server_name"].(string)
	if claimed == "" {
		return nil, fmt.Errorf("the key document names no server")
	}
	if claimed != serverName {
		return nil, fmt.Errorf("the key document is for %s, not for %s", claimed, serverName)
	}
	validUntil, err := documentInt64(document, "valid_until_ts")
	if err != nil {
		return nil, err
	}
	if validUntil <= nowMS {
		return nil, fmt.Errorf("the key document of %s expired at %d", serverName, validUntil)
	}
	keys, err := decodeVerifyKeys(document, "verify_keys", true)
	if err != nil {
		return nil, err
	}
	oldKeys, err := decodeVerifyKeys(document, "old_verify_keys", false)
	if err != nil {
		return nil, err
	}
	// The self-signature is what proves the keys belong to the name: anyone can
	// publish a document naming someone else's keys, but only the owner can sign
	// it.
	if err := verifyAnySelfSignature(document, serverName, keys); err != nil {
		return nil, err
	}
	return &KeySet{
		ServerName:   serverName,
		ValidUntilMS: validUntil,
		Keys:         keys,
		OldKeys:      oldKeys,
	}, nil
}

// verifyAnySelfSignature accepts the document when at least one of its own
// published keys signs it correctly. A document signed by nobody, or signed under
// a key it does not publish, proves nothing. The signature sits under the server
// name, and any of the published key ids may have made it.
func verifyAnySelfSignature(document matrix.Object, serverName string, keys map[string]ed25519.PublicKey) error {
	for keyID, publicKey := range keys {
		if err := matrix.VerifyEd25519Signature(document, serverName, keyID, publicKey); err == nil {
			return nil
		}
	}
	return fmt.Errorf("the key document of %s is not signed by any key it publishes", serverName)
}

// decodeVerifyKeys reads a verify_keys-shaped map out of a document. A missing
// field is allowed; an empty verify_keys is not, because it would leave nothing
// to verify signatures with.
func decodeVerifyKeys(document matrix.Object, field string, required bool) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	raw, ok := document[field]
	if !ok || raw == nil {
		if required {
			return nil, fmt.Errorf("the key document has no %s", field)
		}
		return out, nil
	}
	byKey, ok := raw.(matrix.Object)
	if !ok {
		if byAny, anyOK := raw.(map[string]any); anyOK {
			byKey = matrix.Object(byAny)
		} else {
			return nil, fmt.Errorf("%s is not an object", field)
		}
	}
	if required && len(byKey) == 0 {
		return nil, fmt.Errorf("the key document publishes no keys")
	}
	for keyID, value := range byKey {
		algorithm, version, found := strings.Cut(keyID, ":")
		if !found || algorithm != KeyAlgorithm || !validKeyVersion(version) {
			return nil, fmt.Errorf("%s names %q, which is not an %s key id with a valid version",
				field, keyID, KeyAlgorithm)
		}
		entry, ok := value.(matrix.Object)
		if !ok {
			entryMap, anyOK := value.(map[string]any)
			if !anyOK {
				return nil, fmt.Errorf("the entry for %s is not an object", keyID)
			}
			entry = matrix.Object(entryMap)
		}
		encoded, ok := entry["key"].(string)
		if !ok || encoded == "" {
			return nil, fmt.Errorf("the entry for %s has no key", keyID)
		}
		publicKey, err := matrix.DecodeEd25519Key(encoded)
		if err != nil {
			return nil, fmt.Errorf("the entry for %s: %w", keyID, err)
		}
		out[keyID] = publicKey
	}
	return out, nil
}

// validKeyVersion checks the [a-zA-Z0-9_] shape the specification requires for
// the version part of a key id. A key id that does not fit cannot be looked up by
// a peer, so accepting it would publish a key nobody can use.
func validKeyVersion(version string) bool {
	if version == "" {
		return false
	}
	for i := 0; i < len(version); i++ {
		c := version[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

// Expires reports the millisecond timestamp after which the keys must no longer
// be used, which is the earlier of what the document claims and the seven day
// cap the specification imposes.
func (s *KeySet) Expires(nowMS int64) int64 {
	capped := nowMS + MaxKeyValidity.Milliseconds()
	if s.ValidUntilMS < capped {
		return s.ValidUntilMS
	}
	return capped
}

// Key returns the public key registered under a key id.
func (s *KeySet) Key(keyID string) (ed25519.PublicKey, bool) {
	publicKey, found := s.Keys[keyID]
	return publicKey, found
}

// documentInt64 reads an integer field of a JSON object. Numbers arrive as
// json.Number through [matrix.DecodeCanonical] and as float64 through a plain
// json.Unmarshal, and both spellings appear here because a key document can be
// decoded either way.
func documentInt64(document matrix.Object, field string) (int64, error) {
	value, ok := document[field]
	if !ok || value == nil {
		return 0, fmt.Errorf("the key document has no %s", field)
	}
	switch typed := value.(type) {
	case float64:
		return int64(typed), nil
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s is not an integer: %w", field, err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%s is a %T, not a number", field, value)
	}
}
