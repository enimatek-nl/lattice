// Package api implements the Matrix client-server HTTP API. Handlers decode
// requests, call the service layer and serialise responses; business rules live
// in internal/service.
//
//	@title			Lattice
//	@version		0.1.0
//	@description	The Matrix client-server API implemented by Lattice, a minimal
//	@description	single-server homeserver. Server-server (federation) endpoints
//	@description	are only mounted when the server starts with -federation.
//	@description	The generated document is a by-product of `make swagger` and is
//	@description	not committed.
//	@BasePath		/
package api

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/federation"
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
	// RateLimits guards the expensive writes: the room writes that cost the
	// most, media uploads, and the authentication attempts a stranger can make.
	RateLimits RateLimits
	// Federation mounts the server-server API: the routes under
	// /_matrix/federation and /_matrix/key/v2, each authenticated by the peer's
	// signature. With it off those paths are not registered at all, so they answer
	// M_UNRECOGNIZED exactly like any other unknown path, and the binary is a
	// single-server homeserver.
	Federation bool
	// FederationAdvertise is the host and port this server publishes under
	// /.well-known/matrix/server, which is where peers go looking for it - including
	// for the key document they need to believe a request signed by this server.
	// Empty means the server name as it stands, and a name without a port means
	// port 8448: right for a server that listens there, wrong for a deployment whose
	// federation arrives through a reverse proxy on 443, where every peer ends up
	// unable to verify this server and refusing it.
	FederationAdvertise string
	// FederationPeers maps a server name to the URL to reach that server on,
	// bypassing well-known delegation and TLS. Tests need it because they cannot
	// run a certified HTTPS server; an operator needs it when a peer is reachable
	// through a fixed tunnel.
	FederationPeers map[string]string
	// FederationRootCAs replaces the certificate authorities outbound federation
	// requests trust. Empty means the system trust store.
	FederationRootCAs *x509.CertPool
}

// App owns the HTTP handlers and their dependencies.
type App struct {
	svc    *service.Service
	auth   *auth.Store
	cfg    Config
	mux    *http.ServeMux
	routes []string
	logger *log.Logger
	// limiter holds the token buckets of the expensive endpoints.
	limiter *limiter
	// rejections counts rate limit rejections for the log.
	rejections *rejections
	// federation talks to peer homeservers and verifies what they send back. It is
	// nil while the federation routes are off.
	federation *federation.Transport
}

// New builds the API application.
func New(svc *service.Service, store *auth.Store, cfg Config) *App {
	if cfg.MaxUploadSize <= 0 {
		cfg.MaxUploadSize = service.DefaultMaxUploadSize
	}
	if cfg.Version == "" {
		cfg.Version = service.LatticeVersion
	}
	cfg.RateLimits = cfg.RateLimits.withDefaults()
	app := &App{
		svc:        svc,
		auth:       store,
		cfg:        cfg,
		mux:        http.NewServeMux(),
		logger:     log.New(log.Writer(), "lattice: ", log.LstdFlags),
		limiter:    newLimiter(cfg.RateLimits),
		rejections: &rejections{},
	}
	if cfg.Federation {
		app.federation = federation.NewTransport(svc, federation.Config{
			PeerBaseURLs: cfg.FederationPeers,
			UserAgent:    "Lattice/" + cfg.Version,
			RootCAs:      cfg.FederationRootCAs,
		})
		// The service sends federation traffic too - the outbox has to run with or
		// without an inbound request - so it holds the same client the routes use.
		svc.SetFederationTransport(app.federation)
	}
	app.registerRoutes()
	// Anything the route table does not cover gets a Matrix error instead of the
	// plain "404 page not found" of net/http. Clients render the raw text of that
	// page as an opaque "Server returned 404 error", which hides which endpoint
	// the homeserver does not implement.
	app.mux.HandleFunc("/", app.handleUnknownEndpoint)
	return app
}

// handleUnknownEndpoint answers every unrouted path with M_UNRECOGNIZED. A path
// the route table knows under another method still gets a 405: registering a
// catch-all pattern takes that behaviour away from net/http, so the handler
// restores it from the route table.
func (a *App) handleUnknownEndpoint(w http.ResponseWriter, r *http.Request) {
	if allowed := a.methodsFor(r.URL.Path); len(allowed) > 0 && !containsMethod(allowed, r.Method) {
		sort.Strings(allowed)
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		err := matrix.ErrUnrecognized("no handler for %s %s; this endpoint accepts %s",
			r.Method, r.URL.Path, strings.Join(allowed, ", "))
		err.Status = http.StatusMethodNotAllowed
		a.writeError(w, r, err)
		return
	}
	// A peer that reaches a server with federation switched off is looking at a
	// configuration problem, and the ordinary message does not say so.
	if !a.cfg.Federation && federation.IsFederationPath(r.URL.Path) {
		a.writeError(w, r, matrix.ErrUnrecognized(
			"no handler for %s %s; this server does not serve the server-server API", r.Method, r.URL.Path))
		return
	}
	a.writeError(w, r, matrix.ErrUnrecognized("no handler for %s %s", r.Method, r.URL.Path))
}

// methodsFor lists the methods the route table registers for a concrete path.
// Patterns are the method-prefixed strings net/http uses; a {name} segment
// matches any single segment and a pattern without a method accepts them all.
func (a *App) methodsFor(path string) []string {
	var methods []string
	for _, pattern := range a.routes {
		method, template, hasMethod := strings.Cut(pattern, " ")
		if !hasMethod {
			method, template = "", pattern
		}
		if !matchTemplate(template, path) {
			continue
		}
		if method == "" {
			// The path is registered for every method, so the method cannot be the
			// reason the request went unmatched.
			return nil
		}
		methods = append(methods, method)
	}
	return methods
}

// matchTemplate compares a route pattern with a request path segment by segment.
// A {name} segment matches one non-empty segment, and a pattern that ends in a
// slash is a subtree match, exactly like net/http reads it.
func matchTemplate(template, path string) bool {
	want := strings.Split(strings.TrimSuffix(template, "/"), "/")
	got := strings.Split(path, "/")
	if strings.HasSuffix(template, "/") {
		if len(got) < len(want) {
			return false
		}
		got = got[:len(want)]
	} else if len(want) != len(got) {
		return false
	}
	for i := range want {
		if strings.HasPrefix(want[i], "{") && strings.HasSuffix(want[i], "}") {
			if got[i] == "" {
				// An empty segment is not a value, it is a missing one.
				return false
			}
			continue
		}
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

// containsMethod reports whether a method is in the list.
func containsMethod(methods []string, method string) bool {
	for _, candidate := range methods {
		if candidate == method {
			return true
		}
	}
	return false
}

// Handler returns the routed handler. CORS is applied by the caller so this
// package stays free of third party middleware.
func (a *App) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path, r.URL.RawPath = currentAPIPath(r.URL.Path, r.URL.RawPath)
		a.mux.ServeHTTP(w, r)
	})
}

// pathRewrites maps the older spelling of a versioned API prefix onto the one
// this server routes.
var pathRewrites = []struct{ from, to string }{
	{"/_matrix/client/r0/", "/_matrix/client/v3/"},
	{"/_matrix/media/r0/", "/_matrix/media/v3/"},
}

// currentAPIPath rewrites a request path that uses a retired version prefix.
//
// "r0" was the release candidate prefix of the client-server API and "v1" of the
// media API; both are still sent by older clients and by test suites written
// against them. The endpoints behind them are the current ones, so answering them
// with M_UNRECOGNIZED would be a filing-cabinet error rather than a missing
// feature. RawPath moves with Path, because net/http matches the escaped form of
// a path whenever it is a valid encoding of it.
func currentAPIPath(path, rawPath string) (string, string) {
	for _, rewrite := range pathRewrites {
		if strings.HasPrefix(path, rewrite.from) {
			path = rewrite.to + strings.TrimPrefix(path, rewrite.from)
			if strings.HasPrefix(rawPath, rewrite.from) {
				rawPath = rewrite.to + strings.TrimPrefix(rawPath, rewrite.from)
			}
			return path, rawPath
		}
	}
	return path, rawPath
}

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

// limited registers a pattern behind a token bucket. Authentication runs first
// so the bucket belongs to the device that is asking; for the endpoints without
// a token yet (login, registration) the bucket belongs to the client address.
func (a *App) limited(pattern string, kind limitKind, handler http.HandlerFunc) {
	a.authenticated(pattern, a.rateLimited(kind, handler))
}

// anonymousLimited registers an unauthenticated pattern behind a token bucket.
func (a *App) anonymousLimited(pattern string, kind limitKind, handler http.HandlerFunc) {
	a.handle(pattern, a.rateLimited(kind, handler))
}

// Service exposes the business logic to helper functions in this package.
func (a *App) Service() *service.Service { return a.svc }

// writeJSON serialises a response body.
func writeJSON(w http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		log.Printf("encoding response: %v", err)
		return
	}
	writeRawJSON(w, status, encoded)
}

// writeRawJSON answers with bytes the caller produced itself. It exists for
// bodies whose exact bytes matter: the standard library's JSON encoder appends a
// newline, and a peer that verifies a signature over the bytes it received - as
// the Complement suite does for a server's key document - reads that byte as part
// of what was signed. Encoding with json.Marshal and writing the result leaves
// the body exactly as it was built.
func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		log.Printf("writing response: %v", err)
	}
}

// writeError converts any error into a Matrix error response.
//
// A request whose context has finished is answered with nothing at all: a long
// poll the client gave up on - Element restarting its sync loop, a browser tab
// closing - is not a server fault, there is nobody left to read a body, and
// logging it as an internal error buries the faults that matter.
func (a *App) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
		return
	}
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
	// encoding/json reads invalid UTF-8 by quietly substituting it, which turns a
	// broken client into a stored string nobody can read back. JSON is defined to be
	// UTF-8, so the bytes are checked before anything is decoded from them.
	if !utf8.Valid(body) {
		return matrix.ErrNotJSON("request body was not valid JSON: it is not valid UTF-8")
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

// Federation exposes the peer transport to the outbound side of the server-server
// API, which is where transactions and join requests are sent from. It returns nil
// while the federation routes are off, so callers that need it must check the flag.
func (a *App) Federation() *federation.Transport { return a.federation }
