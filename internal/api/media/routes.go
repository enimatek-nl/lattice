// Package media exposes the Matrix media repository endpoints. The repository
// itself arrives in a later milestone. The routes are registered now for two
// reasons required by the protocol: a wrong method on these paths must answer
// 405 (not 404), and an unsupported operation must answer with an explicit
// error rather than a fake success.
package media

import (
	"net/http"

	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/httpserver"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
)

// Deps are the services the media API needs.
type Deps struct {
	Cfg *config.Config
}

// Register installs the media routes. requireAuth wraps handlers that need a
// valid access token.
func Register(r *httpserver.Router, d *Deps, requireAuth func(http.Handler) http.Handler) {
	notImplemented := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		matrixerr.Write(w, http.StatusNotImplemented, matrixerr.New(matrixerr.CodeUnknown,
			"The media repository is not implemented by this server yet."))
	})
	authenticated := func(h http.Handler) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			requireAuth(h).ServeHTTP(w, req)
		}
	}
	r.Handle("POST", "/_matrix/media/v3/upload", authenticated(notImplemented))
	r.Handle("POST", "/_matrix/media/v1/upload", authenticated(notImplemented))

	// The configuration endpoint is public and only reports the upload limit,
	// which this server does enforce at the HTTP layer.
	configHandler := func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"m.upload.size": d.Cfg.HTTP.MaxBodyBytes,
		})
	}
	r.Handle("GET", "/_matrix/media/v3/config", configHandler)
	r.Handle("GET", "/_matrix/media/v1/config", configHandler)
}
