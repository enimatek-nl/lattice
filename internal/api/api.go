// Package api implements the Matrix client-server HTTP API. Handlers decode
// requests, call the service layer and serialise responses; business rules live
// in internal/service.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// Config holds the runtime options of the API layer. Values come from flags or
// environment variables, never from literals in handlers.
type Config struct {
	// ServerName is the homeserver domain.
	ServerName string
	// Version is the Lattice version reported by /versions.
	Version string
	// RegistrationDisabled closes self-service registration.
	RegistrationDisabled bool
	// RegistrationSecrets enable registration_token login flows.
	RegistrationSecrets []string
	// AllowGuests enables guest account creation.
	AllowGuests bool
	// EnableMedia turns the media endpoints on.
	EnableMedia bool
	// MaxUploadSize limits media uploads in bytes.
	MaxUploadSize int64
	// SupportEmail and AdminMatrixID are published under the well-known
	// support document.
	SupportEmail  string
	AdminMatrixID string
}

// App owns the HTTP handlers and their dependencies.
type App struct {
	svc    *service.Service
	auth   *auth.Store
	cfg    Config
	mux    *http.ServeMux
	routes []string
	logger *log.Logger
}

// New builds the API application.
func New(svc *service.Service, store *auth.Store, cfg Config) *App {
	if cfg.MaxUploadSize <= 0 {
		cfg.MaxUploadSize = service.DefaultMaxUploadSize
	}
	if cfg.Version == "" {
		cfg.Version = service.LatticeVersion
	}
	app := &App{
		svc:    svc,
		auth:   store,
		cfg:    cfg,
		mux:    http.NewServeMux(),
		logger: log.New(log.Writer(), "lattice: ", log.LstdFlags),
	}
	app.registerRoutes()
	return app
}

// Handler returns the routed handler. CORS is applied by the caller so this
// package stays free of third party middleware.
func (a *App) Handler() http.Handler { return a.mux }

// Registered lists the method-and-path patterns this server knows about. The
// endpoint status tool compares this list with the specification index.
func (a *App) Registered() []string { return a.routes }

// handle registers a pattern on the mux and remembers it.
func (a *App) handle(pattern string, handler http.HandlerFunc) {
	a.mux.HandleFunc(pattern, handler)
	a.routes = append(a.routes, pattern)
}

// authenticated registers a pattern behind the authentication middleware.
func (a *App) authenticated(pattern string, handler http.HandlerFunc) {
	a.handle(pattern, a.auth.Require(handler).ServeHTTP)
}

// optionallyAuthenticated registers a pattern that works with or without a
// valid token.
func (a *App) optionallyAuthenticated(pattern string, handler http.HandlerFunc) {
	a.handle(pattern, a.auth.Optional(handler).ServeHTTP)
}

// adminOnly registers a pattern that requires administrator privileges.
func (a *App) adminOnly(pattern string, handler http.HandlerFunc) {
	a.handle(pattern, a.auth.RequireAdmin(handler).ServeHTTP)
}

// Service exposes the business logic to helper functions in this package.
func (a *App) Service() *service.Service { return a.svc }

// writeJSON serialises a response body.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("encoding response: %v", err)
	}
}

// writeError converts any error into a Matrix error response.
func (a *App) writeError(w http.ResponseWriter, r *http.Request, err error) {
	merr := matrix.ErrAs(err, isNotFound)
	if merr.HTTPStatus() >= 500 {
		log.Printf("%s %s: %v", r.Method, r.URL.Path, merr)
	}
	auth.WriteError(w, merr)
}

// decodeBody decodes a JSON request body into target.
func decodeBody(r *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		return matrix.ErrNotJSON("could not read the request body")
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		var syntaxErr *json.UnmarshalTypeError
		if errors.As(err, &syntaxErr) {
			return matrix.ErrBadJSON(err)
		}
		return matrix.ErrNotJSON("request body was not valid JSON: %v", err)
	}
	return nil
}

// queryValue returns a query parameter or its default.
func queryValue(r *http.Request, key, fallback string) string {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback
	}
	return value
}

// queryInt returns an integer query parameter or its default.
func queryInt(r *http.Request, key string, fallback int) (int, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, matrix.ErrInvalidParam("query parameter %q must be an integer", key)
	}
	return parsed, nil
}

// queryDurationMS returns a millisecond query parameter as a duration.
func queryDurationMS(r *http.Request, key string, fallback time.Duration) (time.Duration, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback, nil
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, matrix.ErrInvalidParam("query parameter %q must be a number of milliseconds", key)
	}
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// rawQueryObject reads a JSON object out of a query parameter, which some
// endpoints accept instead of a body.
func rawQueryObject(r *http.Request, key string) matrix.Object {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return matrix.Object{}
	}
	return matrix.MustParseObject([]byte(raw))
}

// isNotFound maps storage level not-found errors onto M_NOT_FOUND.
func isNotFound(err error) bool {
	return persist.IsNotFound(err)
}

// requireSelf checks that the {userId} path parameter names the authenticated
// user. Personal data endpoints (profiles, presence, OpenID tokens) are only
// writable by their owner, so handlers call this before touching the service.
func requireSelf(w http.ResponseWriter, r *http.Request) bool {
	target := r.PathValue("userId")
	if target == "" || target == auth.GetUserID(r) {
		return true
	}
	auth.WriteError(w, matrix.ErrForbidden("%s may only modify its own data", auth.GetUserID(r)))
	return false
}
