package client

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/roomversion"
)

// specVersions lists the global Matrix specification versions this server
// understands. The pinned specification is v1.19 (docs/specification-version.md).
const (
	specVersionMajor = 1
	specVersionMinor = 19
)

// The room versions this server reports are the ones in internal/roomversion, which is
// the same table that decides the rules applied to a room. There is one source of
// truth on purpose: an advertised version the server cannot create a room in is a
// promise the server breaks on the first request.
func defaultRoomVersion() string { return roomversion.Default().ID }

func availableRoomVersions() map[string]string { return roomversion.Available() }

// handleVersions implements GET /_matrix/client/versions.
func (d *Deps) handleVersions(w http.ResponseWriter, r *http.Request) {
	versions := make([]string, 0, specVersionMinor+1)
	for i := 1; i <= specVersionMinor; i++ {
		versions = append(versions, fmt.Sprintf("v%d.%d", specVersionMajor, i))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"versions":          versions,
		"unstable_features": d.unstableFeatures(),
	})
}

// unstableFeatures reports in-progress features. Empty means "none advertised",
// which the specification permits; the value must be an object when present.
func (d *Deps) unstableFeatures() map[string]bool {
	return map[string]bool{}
}

// handleCapabilities implements GET /_matrix/client/v1/capabilities.
func (d *Deps) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	available := availableRoomVersions()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"capabilities": map[string]any{
			"m.room_versions": map[string]any{
				"default":   defaultRoomVersion(),
				"available": available,
			},
			// m.change_password is false because password changes are not implemented
			// yet; advertising it would be a lie. The room versions above are now
			// honest: internal/roomversion lists only versions this server can create
			// rooms in.
			"m.change_password": map[string]any{"enabled": false},
		},
	})
}

// handleWellKnownClient implements GET /.well-known/matrix/client.
func (d *Deps) handleWellKnownClient(w http.ResponseWriter, r *http.Request) {
	if d.Cfg.WellKnown.ClientBaseURL == "" {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeNotFound,
			"This server does not publish a client discovery document."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"m.homeserver": map[string]string{"base_url": d.Cfg.WellKnown.ClientBaseURL},
	})
}

// handleWellKnownServer implements GET /.well-known/matrix/server.
func (d *Deps) handleWellKnownServer(w http.ResponseWriter, r *http.Request) {
	if d.Cfg.WellKnown.ServerBaseURL == "" {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeNotFound,
			"This server does not publish a server discovery document."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"m.server": d.Cfg.WellKnown.ServerBaseURL,
	})
}

// handleHealth is an operational endpoint, not part of Matrix.
func (d *Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"server_name":    d.Cfg.ServerName,
		"server_version": d.Version,
		"uptime_s":       strconv.FormatInt(int64(time.Since(d.Started).Seconds()), 10),
	})
}

// serverName returns the configured server name.
func (d *Deps) serverName() string { return d.Cfg.ServerName }

// maxBody returns the client API body limit.
func (d *Deps) maxBody() int64 { return d.Cfg.HTTP.MaxBodyBytes }
