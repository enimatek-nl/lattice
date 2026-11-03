// Package auth mints and verifies the JWT access tokens used by the
// client-server API and provides the authentication middleware that protects
// routes. Tokens are only honoured while a matching session row exists, so
// deleting a session revokes its token.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/golang-jwt/jwt/v5"
)

// defaultTTL is the default lifetime of an access token.
const defaultTTL = 24 * time.Hour

// Claims are the JWT claims carried by a Lattice access token.
type Claims struct {
	jwt.RegisteredClaims
	// DeviceID binds the token to a single client device.
	DeviceID string `json:"device_id,omitempty"`
	// Localpart is the user's localpart, kept for convenience.
	Localpart string `json:"localpart,omitempty"`
	// Admin mirrors the administrator flag of the account.
	Admin bool `json:"admin,omitempty"`
}

// TokenService signs and verifies access tokens.
type TokenService struct {
	secret []byte
	ttl    time.Duration
	issuer string
}

// Option customises a [TokenService].
type Option func(*TokenService)

// WithTTL overrides the access token lifetime.
func WithTTL(ttl time.Duration) Option {
	return func(t *TokenService) { t.ttl = ttl }
}

// WithIssuer sets the issuer claim of minted tokens.
func WithIssuer(issuer string) Option {
	return func(t *TokenService) { t.issuer = issuer }
}

// ErrInvalidToken is returned when a token cannot be verified.
var ErrInvalidToken = errors.New("invalid or expired token")

// NewTokenService builds a token service for the given secret.
func NewTokenService(secret string, opts ...Option) *TokenService {
	svc := &TokenService{secret: []byte(secret), ttl: defaultTTL}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Generate mints an access token for a user and device.
func (t *TokenService) Generate(userID, deviceID, localpart string, admin bool) (string, error) {
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    t.issuer,
			ID:        randomToken(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		},
		DeviceID:  deviceID,
		Localpart: localpart,
		Admin:     admin,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString(t.secret)
	if err != nil {
		return "", fmt.Errorf("sign access token: %w", err)
	}
	return signed, nil
}

// Parse verifies a token and returns its claims.
func (t *TokenService) Parse(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", tok.Header["alg"])
		}
		return t.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || parsed == nil || !parsed.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// TTL returns the configured access token lifetime.
func (t *TokenService) TTL() time.Duration { return t.ttl }

// randomToken returns a random identifier used as the JWT ID claim, which
// keeps two sessions of the same device apart.
func randomToken() string {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// StreamPos renders a stream position as an opaque token.
func StreamPos(pos uint64) string { return "s" + strconv.FormatUint(pos, 10) }

// ParseStreamPos converts a sync token back into a stream position. An empty
// token yields zero (the start of the stream).
func ParseStreamPos(token string) (uint64, error) {
	if token == "" {
		return 0, nil
	}
	if len(token) > 1 && (token[0] == 's' || token[0] == 't') {
		token = token[1:]
	}
	pos, err := strconv.ParseUint(token, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid stream token %q: %w", token, err)
	}
	return pos, nil
}

// TokenSecretKey names the row that holds the secret this database was first started with.
const TokenSecretKey = "token.secret"

// TokenSecret returns the secret that signs access tokens: the one the operator configured,
// or the one this database was given when it first started.
//
// A secret generated at startup and thrown away at shutdown logs everybody out on restart.
// The sessions it authorised are rows in the database and are still current; what is gone is
// the signature that lets them be read, so the client is told its token is unknown and the
// only way back in is a password it may not even have - an SSO login, a registration token,
// a phone holding the token in a keystore. An upgrade restart is not a logout, so a generated
// secret is kept next to the sessions it made.
func TokenSecret(db *persist.Database, configured string) (secret string, generated bool, err error) {
	if configured != "" {
		return configured, false, nil
	}
	stored, err := db.MetaRepo.Get(TokenSecretKey)
	if err != nil && !persist.IsNotFound(err) {
		return "", false, err
	}
	if stored != "" {
		return stored, false, nil
	}
	value, err := db.MetaRepo.GetOrSet(TokenSecretKey, func() (string, error) {
		made := matrix.NewToken()
		if made == "" {
			return "", fmt.Errorf("generating an access token secret")
		}
		return made, nil
	})
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}
