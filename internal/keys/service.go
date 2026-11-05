// Package keys manages this server's ed25519 signing key and the signed server
// key document published under /_matrix/key/v2/server.
package keys

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/persist"
)

// defaultKeyID is the key version used for the first signing key. Key rotation
// (MSC, and the spec's old_verify_keys handling) is tracked as a later milestone;
// the version string is kept separate so rotation does not require a redesign.
const defaultKeyID = "v1"

// keyLifetime is how far ahead valid_until_ts is set.
const keyLifetime = 7 * 24 * time.Hour

// LatestServerVersion is reported in the server key document.
const LatestServerVersion = "1.19"

// Service owns the active signing key.
type Service struct {
	serverName string
	repo       persist.SigningKeyRepo

	mu         sync.RWMutex
	version    string
	keyID      string
	priv       ed25519.PrivateKey
	pub        ed25519.PublicKey
	validUntil time.Time
}

// Load reads the active signing key, creating and persisting one when the store
// is empty.
func Load(ctx context.Context, serverName string, repo persist.SigningKeyRepo) (*Service, error) {
	s := &Service{serverName: serverName, repo: repo}
	stored, err := repo.Active(ctx)
	if errors.Is(err, persist.ErrNotFound) {
		stored, err = s.create(ctx)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if stored.Algorithm != matrixjson.Algorithm {
		return nil, fmt.Errorf("keys: unsupported stored algorithm %q", stored.Algorithm)
	}
	pub, err := matrixjson.DecodeUnpadded(stored.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("keys: stored public key is not base64: %w", err)
	}
	priv, err := matrixjson.DecodeUnpadded(stored.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("keys: stored private key is not base64: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize || len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("keys: stored key has an unexpected size")
	}
	if !ed25519.PublicKey(pub).Equal(ed25519.PrivateKey(priv).Public()) {
		return nil, errors.New("keys: stored key pair is inconsistent")
	}
	s.version = stored.KeyID
	s.keyID = matrixjson.KeyID(matrixjson.Algorithm, stored.KeyID)
	s.priv = ed25519.PrivateKey(priv)
	s.pub = ed25519.PublicKey(pub)
	s.validUntil = time.Now().Add(keyLifetime)
	return s, nil
}

func (s *Service) create(ctx context.Context) (*persist.SigningKey, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("keys: cannot generate key: %w", err)
	}
	stored := &persist.SigningKey{
		KeyID:      defaultKeyID,
		Algorithm:  matrixjson.Algorithm,
		PublicKey:  matrixjson.EncodeUnpadded(pub),
		PrivateKey: matrixjson.EncodeUnpadded(priv),
		Active:     true,
	}
	if err := s.repo.Create(ctx, stored); err != nil {
		return nil, fmt.Errorf("keys: cannot persist signing key: %w", err)
	}
	return stored, nil
}

// Version returns the key version part of the signing key identifier.
func (s *Service) Version() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// KeyID returns the full signing key identifier, e.g. "ed25519:v1".
func (s *Service) KeyID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keyID
}

// PublicKey returns the verification key.
func (s *Service) PublicKey() ed25519.PublicKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pub
}

// Sign produces a raw ed25519 signature with the active key.
func (s *Service) Sign(data []byte) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ed25519.Sign(s.priv, data)
}

// SignJSON signs a Matrix object under this server's name and key id.
func (s *Service) SignJSON(obj map[string]any) error {
	return matrixjson.SignJSONInPlace(s.serverName, s.KeyID(), s.priv, obj)
}

// VerifyLocal resolves verification keys for this server only. Remote key
// resolution arrives with the federation milestone.
func (s *Service) VerifyLocal(origin, keyID string) (ed25519.PublicKey, error) {
	if origin != s.serverName {
		return nil, fmt.Errorf("keys: no key known for origin %q", origin)
	}
	if keyID != s.KeyID() {
		return nil, fmt.Errorf("keys: no key %q for %q", keyID, origin)
	}
	return s.PublicKey(), nil
}

// ServerKeyDocument returns the signed GET /_matrix/key/v2/server response.
// minValidUntil extends valid_until_ts when a caller demands fresher keys.
func (s *Service) ServerKeyDocument(minValidUntil time.Time) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	validUntil := s.validUntil
	if minValidUntil.After(validUntil) {
		// Only extend, never shorten, and never beyond the normal lifetime window.
		latest := time.Now().Add(keyLifetime)
		if minValidUntil.After(latest) {
			minValidUntil = latest
		}
		validUntil = minValidUntil
	}
	if time.Until(validUntil) < 24*time.Hour {
		validUntil = time.Now().Add(keyLifetime)
	}

	doc := map[string]any{
		"server_name":    s.serverName,
		"latest_version": LatestServerVersion,
		"verify_keys": map[string]any{
			s.keyID: map[string]any{"key": matrixjson.EncodeUnpadded(s.pub)},
		},
		"old_verify_keys": map[string]any{},
		"valid_until_ts":  validUntil.UnixMilli(),
	}
	if err := matrixjson.SignJSONInPlace(s.serverName, s.keyID, s.priv, doc); err != nil {
		return nil, err
	}
	s.validUntil = validUntil
	return doc, nil
}
