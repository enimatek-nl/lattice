// Package httpserver owns the HTTP listeners, middleware chain and routing of
// the Matrix APIs.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// ctxKey is the private type for context values set by this package.
type ctxKey string

const (
	ctxKeyRequestID ctxKey = "request_id"
	ctxKeyRoute     ctxKey = "route_pattern"
)

// RequestIDFromContext returns the request ID attached by the middleware.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so that the first one listed is the outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Recover converts panics into 500 responses without leaking internals.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic in HTTP handler",
						"path", r.URL.Path,
						"panic", rec,
						"stack", string(debug.Stack()),
					)
					matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(
						matrixerr.CodeUnknown, "The server encountered an internal error."))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestID attaches a random request identifier to the context and echoes it in
// a response header.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := matrixid.GenerateRandomString(nil, 9)
			if err != nil {
				id = "unknown"
			}
			w.Header().Set("X-Lattice-Request-ID", id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, id)))
		})
	}
}

// responseRecorder captures the status and body size for access logging.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rr *responseRecorder) WriteHeader(code int) {
	if rr.status == 0 {
		rr.status = code
	}
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if rr.status == 0 {
		rr.status = http.StatusOK
	}
	n, err := rr.ResponseWriter.Write(b)
	rr.bytes += n
	return n, err
}

// Flush passes through to the wrapped writer when it supports http.Flusher.
func (rr *responseRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// AccessLog logs each request. Token values are never logged: the logger itself
// redacts sensitive attributes and access tokens live in headers/query values
// that are deliberately not recorded here.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFromContext(r.Context()),
			)
		})
	}
}

// MaxBody limits the request body size.
func MaxBody(limit int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limit > 0 && r.Body != nil {
				if r.ContentLength > limit {
					matrixerr.Write(w, http.StatusRequestEntityTooLarge, matrixerr.New(
						matrixerr.CodeTooLarge, "Request body is too large."))
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds the wall-clock time of a request. The handler goroutine keeps
// running until it observes context cancellation, but its writes are discarded
// once the deadline passes so the client always receives a well-formed Matrix
// error instead of a truncated body.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			guard := &timeoutWriter{ResponseWriter: w}
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if rec := recover(); rec != nil {
						// Recovered here so a late panic cannot escape the timeout
						// goroutine; the outer Recover middleware already handled the
						// non-timed-out case.
						guard.notePanic(rec)
					}
				}()
				next.ServeHTTP(guard, r.WithContext(ctx))
			}()

			select {
			case <-done:
				guard.finalise()
			case <-ctx.Done():
				if guard.timedOut() {
					return
				}
				guard.writeTimeout()
				<-done // let the handler goroutine finish before returning
			}
		})
	}
}

type timeoutWriter struct {
	http.ResponseWriter
	mu        sync.Mutex
	wroteHead bool
	timedOut_ bool
	panic     any
}

func (t *timeoutWriter) WriteHeader(code int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timedOut_ {
		return
	}
	t.wroteHead = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *timeoutWriter) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timedOut_ {
		return len(b), nil
	}
	t.wroteHead = true
	return t.ResponseWriter.Write(b)
}

func (t *timeoutWriter) notePanic(rec any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.panic = rec
}

func (t *timeoutWriter) timedOut() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.timedOut_
}

func (t *timeoutWriter) writeTimeout() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timedOut_ = true
	if t.wroteHead {
		// A partial body was already sent; nothing sensible can be appended.
		return
	}
	t.wroteHead = true
	matrixerr.Write(t.ResponseWriter, http.StatusGatewayTimeout, matrixerr.New(
		matrixerr.CodeUnknown, "The request timed out on this server."))
}

func (t *timeoutWriter) finalise() {
	t.mu.Lock()
	rec := t.panic
	timedOut := t.timedOut_
	wrote := t.wroteHead
	t.mu.Unlock()
	if rec != nil && !timedOut && !wrote {
		panic(rec)
	}
}

// SecurityHeaders sets conservative response headers.
func SecurityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	}
}
