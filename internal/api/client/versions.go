package client

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
)

// specVersions lists the global Matrix specification versions this server
// understands. The pinned specification is v1.19 (docs/specification-version.md).
const (
	specVersionMajor = 1
	specVersionMinor = 19
)

// defaultRoomVersion is the room version used for newly created rooms. Room
// creation is implemented in the rooms milestone; the value is reported here so
// clients can plan, and the compliance matrix records the gap explicitly.
const defaultRoomVersion = "11"

// availableRoomVersions maps room version identifiers to their stability.
var availableRoomVersions = map[string]string{
	"11": "stable",
}

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
	available := make(map[string]string, len(availableRoomVersions))
	for version, stability := range availableRoomVersions {
		available[version] = stability
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"capabilities": map[string]any{
			"m.room_versions": map[string]any{
				"default":   defaultRoomVersion,
				"available": available,
			},
			// m.change_password is false because password changes are not
			// implemented yet; advertising it would be a lie. The advertised room
			// version is likewise aspirational until the rooms milestone, which the
			// compliance matrix records as a known inconsistency.
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
