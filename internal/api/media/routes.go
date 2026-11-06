// Package media exposes the content repository over HTTP: the deprecated unauthenticated
// `/_matrix/media/*` surface, the authenticated `/_matrix/client/v1/media/*` surface that replaced it,
// and the configuration endpoint that tells a client the upload limit this server actually enforces.
//
// Both surfaces are registered because the specification deprecates the old one without withdrawing it: a
// client that has not been updated yet still has to be able to fetch the file it was pointed at. What they
// differ in is who may ask — `/_matrix/media/*` is open to anybody who knows the URI, which is exactly why
// the authenticated form exists — so a request carries a flag recording which door it came through, and the
// handlers stay honest about that.
package media

import (
	"log/slog"
	"net/http"

	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/httpserver"
	"github.com/enimatek/lattice/internal/media"
)

// Deps are the services the media API needs.
type Deps struct {
	Cfg *config.Config
	Log *slog.Logger
	// Media is the content repository itself.
	Media *media.Service
}

// Register installs the media routes. requireAuth wraps the handlers that need a valid access token.
func Register(r *httpserver.Router, d *Deps, requireAuth func(http.Handler) http.Handler) {
	authenticated := func(h http.HandlerFunc) http.HandlerFunc {
		return requireAuth(h).ServeHTTP
	}

	// Uploads are always authenticated: content is stored on somebody's server and attributed to them.
	r.Handle("POST", "/_matrix/media/v3/upload", authenticated(d.handleUpload))
	r.Handle("POST", "/_matrix/media/v1/upload", authenticated(d.handleUpload))
	r.Handle("POST", "/_matrix/media/r0/upload", authenticated(d.handleUpload))
	r.Handle("POST", "/_matrix/media/v1/create", authenticated(d.handleCreate))
	r.Handle("PUT", "/_matrix/media/v3/upload/{serverName}/{mediaId}", authenticated(d.handleUploadToURI))
	r.Handle("PUT", "/_matrix/media/v1/upload/{serverName}/{mediaId}", authenticated(d.handleUploadToURI))

	// Downloads on the old paths are public by specification — knowing the URI is the credential — and
	// authenticated on the new ones. The old paths stay registered so existing clients keep working while
	// the ecosystem moves.
	r.Handle("GET", "/_matrix/media/v3/download/{serverName}/{mediaId}", d.handleDownload)
	r.Handle("GET", "/_matrix/media/v3/download/{serverName}/{mediaId}/{fileName}", d.handleDownloadNamed)
	r.Handle("GET", "/_matrix/media/v1/download/{serverName}/{mediaId}", d.handleDownload)
	r.Handle("GET", "/_matrix/media/r0/download/{serverName}/{mediaId}", d.handleDownload)
	r.Handle("GET", "/_matrix/media/v3/thumbnail/{serverName}/{mediaId}", d.handleThumbnail)
	r.Handle("GET", "/_matrix/media/v1/thumbnail/{serverName}/{mediaId}", d.handleThumbnail)
	r.Handle("GET", "/_matrix/media/r0/thumbnail/{serverName}/{mediaId}", d.handleThumbnail)

	r.Handle("GET", "/_matrix/client/v1/media/download/{serverName}/{mediaId}",
		authenticated(d.handleDownload))
	r.Handle("GET", "/_matrix/client/v1/media/download/{serverName}/{mediaId}/{fileName}",
		authenticated(d.handleDownloadNamed))
	r.Handle("GET", "/_matrix/client/v1/media/thumbnail/{serverName}/{mediaId}",
		authenticated(d.handleThumbnail))

	// The configuration endpoint is public: it tells a client how big an upload may be before it tries.
	configHandler := func(w http.ResponseWriter, req *http.Request) {
		d.handleConfig(w, req)
	}
	r.Handle("GET", "/_matrix/media/v3/config", configHandler)
	r.Handle("GET", "/_matrix/media/v1/config", configHandler)

	// URL previews are a separate module and are not implemented. The specification's answer for an
	// endpoint this server does not have is M_UNRECOGNIZED, which is also what tells a peer to stop asking.
	notThere := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeUnrecognized(w, "This server does not generate URL previews.")
	})
	r.Handle("GET", "/_matrix/media/v3/preview_url", notThere)
	r.Handle("GET", "/_matrix/client/v1/media/preview_url", authenticated(notThere))
}
