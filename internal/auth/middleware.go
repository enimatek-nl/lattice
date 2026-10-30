package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// contextKey is a private type for values this package puts on the request
// context.
type contextKey string

const (
	ctxUserID    contextKey = "lattice.user_id"
	ctxDeviceID  contextKey = "lattice.device_id"
	ctxClaims    contextKey = "lattice.claims"
	ctxToken     contextKey = "lattice.token"
	ctxSessionID contextKey = "lattice.session_id"
)

// WriteError writes a Matrix error body to the response.
func WriteError(w http.ResponseWriter, err error) {
	merr := matrix.ErrAs(err, persist.IsNotFound)
	body := matrix.Object{
		"errcode": merr.Code,
		"error":   merr.Message,
	}
	if merr.RetryAfterMS > 0 {
		body["retry_after_ms"] = merr.RetryAfterMS
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(merr.HTTPStatus())
	if encErr := json.NewEncoder(w).Encode(body); encErr != nil {
		return
	}
}

// TokenFromRequest extracts a bearer token from the Authorization header,
// falling back to the access_token query parameter, which the specification
// still allows for clients that cannot set headers (events stream, websockets).
func TokenFromRequest(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header != "" {
		scheme, token, ok := strings.Cut(header, " ")
		if ok && strings.EqualFold(scheme, "bearer") {
			return strings.TrimSpace(token)
		}
		return strings.TrimSpace(header)
	}
	return r.URL.Query().Get("access_token")
}

// Store couples the middleware to the token service and the session table.
type Store struct {
	tokens *TokenService
	db     *persist.Database
}

// NewStore builds the authentication store used by the middleware.
func NewStore(tokens *TokenService, db *persist.Database) *Store {
	return &Store{tokens: tokens, db: db}
}

// Authenticate resolves the request's access token into claims and an active
// session. It returns a Matrix error when the token is missing, malformed or
// revoked.
func (s *Store) Authenticate(r *http.Request) (*Claims, *persist.Session, error) {
	token := TokenFromRequest(r)
	if token == "" {
		return nil, nil, matrix.ErrMissingToken()
	}
	claims, err := s.tokens.Parse(token)
	if err != nil {
		return nil, nil, matrix.ErrUnknownToken(false)
	}
	session, err := s.db.SessionRepo.GetByToken(token)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, nil, matrix.ErrUnknownToken(false)
		}
		return nil, nil, matrix.ErrInternal(err)
	}
	if session.UserID != claims.Subject {
		return nil, nil, matrix.ErrUnknownToken(false)
	}
	if session.ValidUntilTS > 0 && session.ValidUntilTS < nowMS() {
		return nil, nil, matrix.ErrUnknownToken(true)
	}
	user, err := s.db.UserRepo.GetByID(session.UserID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, nil, matrix.ErrUnknownToken(false)
		}
		return nil, nil, matrix.ErrInternal(err)
	}
	if user.Deactivated {
		return nil, nil, matrix.ErrUnknownToken(false)
	}
	if user.Locked || user.Suspended {
		return nil, nil, matrix.ErrForbidden("this account is suspended")
	}
	return claims, session, nil
}

// WithContext returns a copy of r carrying the authenticated identity.
func (s *Store) WithContext(r *http.Request, claims *Claims, session *persist.Session, token string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxUserID, claims.Subject)
	ctx = context.WithValue(ctx, ctxDeviceID, claims.DeviceID)
	ctx = context.WithValue(ctx, ctxClaims, claims)
	ctx = context.WithValue(ctx, ctxToken, token)
	ctx = context.WithValue(ctx, ctxSessionID, session.ID)
	return r.WithContext(ctx)
}

// Require wraps next with mandatory authentication.
func (s *Store) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, session, err := s.Authenticate(r)
		if err != nil {
			WriteError(w, err)
			return
		}
		next.ServeHTTP(w, s.WithContext(r, claims, session, TokenFromRequest(r)))
	})
}

// Optional wraps next with opportunistic authentication: an invalid token is
// ignored instead of rejecting the request, which is what several public
// endpoints allow.
func (s *Store) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TokenFromRequest(r) != "" {
			if claims, session, err := s.Authenticate(r); err == nil {
				next.ServeHTTP(w, s.WithContext(r, claims, session, TokenFromRequest(r)))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin wraps next and rejects non-administrators.
func (s *Store) RequireAdmin(next http.Handler) http.Handler {
	return s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAdmin(r) {
			WriteError(w, matrix.ErrForbidden("administrator privileges are required"))
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// GetUserID returns the authenticated user ID, or an empty string.
func GetUserID(r *http.Request) string {
	return contextString(r, ctxUserID)
}

// GetDeviceID returns the authenticated device ID, or an empty string.
func GetDeviceID(r *http.Request) string {
	return contextString(r, ctxDeviceID)
}

// GetToken returns the raw access token used for this request.
func GetToken(r *http.Request) string {
	return contextString(r, ctxToken)
}

// GetSessionID returns the database id of the active session.
func GetSessionID(r *http.Request) uint {
	v, _ := r.Context().Value(ctxSessionID).(uint)
	return v
}

// GetClaims returns the verified claims of the request, when authenticated.
func GetClaims(r *http.Request) *Claims {
	claims, _ := r.Context().Value(ctxClaims).(*Claims)
	return claims
}

// IsAdmin reports whether the request is authenticated as an administrator.
func IsAdmin(r *http.Request) bool {
	claims := GetClaims(r)
	return claims != nil && claims.Admin
}

// contextString reads a string value from the request context.
func contextString(r *http.Request, key contextKey) string {
	value, _ := r.Context().Value(key).(string)
	return value
}

// ErrNoSession signals that a request has no authenticated session.
var ErrNoSession = errors.New("request is not authenticated")

// nowMS returns the current time in unix milliseconds.
func nowMS() int64 { return time.Now().UnixMilli() }

// SessionFor returns the session row of an authenticated request.
func (s *Store) SessionFor(r *http.Request) (*persist.Session, error) {
	token := GetToken(r)
	if token == "" {
		return nil, ErrNoSession
	}
	session, err := s.db.SessionRepo.GetByToken(token)
	if err != nil {
		return nil, err
	}
	return session, nil
}
