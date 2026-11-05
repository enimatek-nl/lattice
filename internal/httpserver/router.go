package httpserver

import (
	"net/http"
	"sort"
	"strings"

	"github.com/enimatek/lattice/internal/matrixerr"
)

// Router wraps http.ServeMux so that the Matrix error semantics for unknown
// paths and unknown methods are produced by the server rather than by the
// stdlib:
//
//   - a path that no route matches            -> 404 {"errcode":"M_UNRECOGNIZED"}
//   - a known path with the wrong method      -> 405 {"errcode":"M_UNRECOGNIZED"}
//
// Routes are registered in the serving mux with method prefixes (the project
// convention) and additionally in a path-only "probe" mux used to decide which
// of the two cases applies. This is needed because Go's ServeMux answers method
// mismatches with an empty body and cannot be told otherwise.
type Router struct {
	mux     *http.ServeMux
	probe   *http.ServeMux
	allowed map[string]map[string]bool
}

// NewRouter returns an empty Router.
func NewRouter() *Router {
	return &Router{
		mux:     http.NewServeMux(),
		probe:   http.NewServeMux(),
		allowed: make(map[string]map[string]bool),
	}
}

// Handle registers h for the given method and path pattern. Patterns use the
// Go 1.22+ mux syntax, for example "/_matrix/client/v3/rooms/{roomID}/state".
func (r *Router) Handle(method, pattern string, h http.HandlerFunc) {
	method = strings.ToUpper(method)
	if pattern == "" {
		panic("httpserver: empty path pattern")
	}
	if _, exists := r.allowed[pattern]; !exists {
		r.allowed[pattern] = make(map[string]bool)
		// The probe mux sees the same path patterns without the method prefix.
		r.probe.HandleFunc(pattern, http.NotFound)
	}
	if r.allowed[pattern][method] {
		panic("httpserver: duplicate registration for " + method + " " + pattern)
	}
	r.allowed[pattern][method] = true
	r.mux.HandleFunc(method+" "+pattern, h)
}

// HandleAuth is a convenience for authenticated endpoints: the handler receives
// the already-authenticated request.
func (r *Router) HandleAuth(method, pattern string, h http.HandlerFunc) {
	r.Handle(method, pattern, h)
}

// Handler returns the routed handler.
func (r *Router) Handler() http.Handler {
	// Go's ServeMux in this toolchain does not expose a fallback handler, and it
	// answers method mismatches with an empty body. Both cases are decided here,
	// before delegating, so clients always get a Matrix error response.
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, pattern := r.probe.Handler(req)
		if pattern == "" {
			r.unknown(w, req)
			return
		}
		if !r.methodAllowed(pattern, req.Method) {
			r.wrongMethod(w, req, pattern)
			return
		}
		r.mux.ServeHTTP(w, req)
	})
}

// MirrorFrom records the path and method knowledge of another router without
// serving its endpoints.
//
// The Client-Server API and the Server-Server API listen on different ports, but a
// client that pokes a federation path on the client port must still learn the
// method set that path has: a wrong method answers 405 M_UNRECOGNIZED with an
// Allow header, not a bare 404. A request that does use one of those methods is
// answered with a Matrix 404 that says where the endpoint lives. Mirroring only
// the method set means no additional API is exposed on this listener.
func (r *Router) MirrorFrom(other *Router) {
	for pattern, methods := range other.allowed {
		if _, exists := r.allowed[pattern]; exists {
			continue
		}
		copied := make(map[string]bool, len(methods))
		for method, ok := range methods {
			copied[method] = ok
		}
		r.allowed[pattern] = copied
		r.probe.HandleFunc(pattern, http.NotFound)
		r.mux.HandleFunc(pattern, mirroredAway(pattern))
	}
}

// mirroredAway answers requests that reached the listener which does not serve
// that endpoint.
func mirroredAway(pattern string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.Newf(
			matrixerr.CodeUnrecognized,
			"Endpoint %s is not served on this listener.", pattern,
		))
	}
}

// Patterns returns the registered path patterns, sorted. Useful for diagnostics
// and tests.
func (r *Router) Patterns() []string {
	out := make([]string, 0, len(r.allowed))
	for p := range r.allowed {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// AllowedMethods returns the methods accepted for pattern, sorted.
func (r *Router) AllowedMethods(pattern string) []string {
	out := make([]string, 0, 4)
	for m := range r.allowed[pattern] {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func (r *Router) methodAllowed(pattern, method string) bool {
	methods := r.allowed[pattern]
	if methods[method] {
		return true
	}
	// ServeMux serves HEAD using the GET handler; mirror that here.
	if method == http.MethodHead && methods[http.MethodGet] {
		return true
	}
	return false
}

func (r *Router) wrongMethod(w http.ResponseWriter, req *http.Request, pattern string) {
	methods := r.AllowedMethods(pattern)
	if len(methods) > 0 {
		w.Header().Set("Allow", strings.Join(methods, ", "))
	}
	matrixerr.Write(w, http.StatusMethodNotAllowed, matrixerr.Newf(
		matrixerr.CodeUnrecognized,
		"Unrecognized method %s for endpoint %s.",
		req.Method, pattern,
	))
}

func (r *Router) unknown(w http.ResponseWriter, req *http.Request) {
	matrixerr.Write(w, http.StatusNotFound, matrixerr.Newf(
		matrixerr.CodeUnrecognized,
		"Unrecognized endpoint %s.", req.URL.Path,
	))
}
