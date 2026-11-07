// Package auth implements access tokens, User-Interactive Auth sessions and the
// authentication middleware for the Matrix APIs. Matrix authentication is
// deliberately not modelled as JWT sessions: tokens are opaque, revocable and
// tied to a device, as the specification requires.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
	"gorm.io/gorm"
)

// tokenBytes is the entropy of a minted access token.
const tokenBytes = 32

// TokenService mints, validates and revokes access tokens. Tokens are stored as
// SHA-256 digests so a database leak does not yield usable credentials.
type TokenService struct {
	tokens persist.TokenRepo
}

// NewTokenService returns a TokenService over the given repository.
func NewTokenService(tokens persist.TokenRepo) *TokenService {
	return &TokenService{tokens: tokens}
}

// Mint creates a new access token for a device and persists its digest inside
// tx, so account creation can be atomic.
func (t *TokenService) Mint(ctx context.Context, tx *gorm.DB, userID, deviceID string) (string, error) {
	raw, err := matrixid.GenerateRandomString(nil, tokenBytes)
	if err != nil {
		return "", fmt.Errorf("auth: cannot mint token: %w", err)
	}
	record := &persist.AccessToken{
		TokenHash: hashToken(raw),
		UserID:    userID,
		DeviceID:  deviceID,
	}
	repo := t.tokens
	if tx != nil {
		repo = repo.WithTx(tx)
	}
	if err := repo.Create(ctx, record); err != nil {
		return "", fmt.Errorf("auth: cannot store token: %w", err)
	}
	return raw, nil
}

// Validate resolves an access token to its session.
func (t *TokenService) Validate(ctx context.Context, raw string) (*persist.AccessToken, error) {
	if raw == "" {
		return nil, persist.ErrNotFound
	}
	record, err := t.tokens.ByHash(ctx, hashToken(raw))
	if err != nil {
		return nil, err
	}
	return record, nil
}

// RevokeDevice invalidates every token of one device.
func (t *TokenService) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	return t.tokens.DeleteByDevice(ctx, userID, deviceID)
}

// RevokeExcept invalidates every token of a user except the named device's, which is the
// session that asked for it.
func (t *TokenService) RevokeExcept(ctx context.Context, userID, keepDeviceID string) error {
	return t.tokens.DeleteByUserExcept(ctx, userID, keepDeviceID)
}

// RevokeUser invalidates every token of a user.
func (t *TokenService) RevokeUser(ctx context.Context, userID string) error {
	return t.tokens.DeleteByUser(ctx, userID)
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// sessionKey is the context key for the authenticated session.
type sessionKey struct{}

// Session is the authenticated identity attached to a request.
type Session struct {
	UserID   string
	DeviceID string
	Token    string
}

// AccountChecker lets the middleware reject tokens that belong to deactivated
// accounts without importing the users package.
type AccountChecker interface {
	IsActive(ctx context.Context, userID string) (bool, error)
}

// RequireAuth returns middleware that authenticates a request. A missing token
// yields M_MISSING_TOKEN, an unknown or revoked token yields M_UNKNOWN_TOKEN, and
// a deactivated account yields M_USER_DEACTIVATED.
func RequireAuth(tokens *TokenService, accounts AccountChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := httpx.AccessTokenFromRequest(r)
			if raw == "" {
				matrixerr.Write(w, http.StatusUnauthorized, matrixerr.New(
					matrixerr.CodeMissingToken, "Missing access token."))
				return
			}
			record, err := tokens.Validate(r.Context(), raw)
			if errors.Is(err, persist.ErrNotFound) {
				matrixerr.Write(w, http.StatusUnauthorized, matrixerr.New(
					matrixerr.CodeUnknownToken,
					"Invalid access token. Please re-authenticate."))
				return
			}
			if err != nil {
				matrixerr.WriteError(w, http.StatusUnauthorized, err)
				return
			}
			if accounts != nil {
				active, cerr := accounts.IsActive(r.Context(), record.UserID)
				if cerr != nil {
					matrixerr.WriteError(w, http.StatusInternalServerError, cerr)
					return
				}
				if !active {
					matrixerr.Write(w, http.StatusForbidden, matrixerr.New(
						matrixerr.CodeUserDeactivated, "This account has been deactivated."))
					return
				}
			}
			session := &Session{UserID: record.UserID, DeviceID: record.DeviceID, Token: raw}
			ctx := context.WithValue(r.Context(), sessionKey{}, session)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SessionFromRequest returns the authenticated session, or nil.
func SessionFromRequest(r *http.Request) *Session {
	s, _ := r.Context().Value(sessionKey{}).(*Session)
	return s
}

// UserID returns the authenticated user ID, or the empty string.
func UserID(r *http.Request) string {
	if s := SessionFromRequest(r); s != nil {
		return s.UserID
	}
	return ""
}

// DeviceID returns the authenticated device ID, or the empty string.
func DeviceID(r *http.Request) string {
	if s := SessionFromRequest(r); s != nil {
		return s.DeviceID
	}
	return ""
}

// ConstantTimeEqual compares two secrets without leaking timing differences.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
