package service

import (
	"runtime"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// LatticeVersion is the version string of this homeserver implementation.
const LatticeVersion = "0.1.0"

// UnstableFeatures lists the spec features this server advertises, keyed by the
// feature identifier used by /versions.
var UnstableFeatures = map[string]bool{
	"org.matrix.msc3026": true, // blank user IDs
	"org.matrix.msc3440": true, // sync without filter
	"org.matrix.msc3890": true, // membership endpoints accept ?user_id=
	"org.matrix.msc4222": true, // GET /rooms/{roomId}/joined_members
}

// unstableFeaturesCopy returns a copy suitable for serialisation.
func (s *Service) unstableFeaturesCopy() map[string]bool {
	features := make(map[string]bool, len(UnstableFeatures))
	for name, enabled := range UnstableFeatures {
		features[name] = enabled
	}
	return features
}

// ServerVersion identifies this implementation, as reported by /versions.
func (s *Service) ServerVersion() string {
	return "Lattice/" + LatticeVersion + " (" + runtime.Version() + ")"
}

// SpecVersions lists the client-server specification revisions this server
// claims to implement.
var SpecVersions = []string{
	"v1.0", "v1.1", "v1.2", "v1.3", "v1.4", "v1.5", "v1.6", "v1.7", "v1.8",
	"v1.9", "v1.10", "v1.11",
}

// Versions implements GET /_matrix/client/versions.
func (s *Service) Versions() map[string]any {
	return map[string]any{
		"versions":          SpecVersions,
		"unstable_features": s.unstableFeaturesCopy(),
	}
}

// LoginFlow describes one supported login flow.
type LoginFlow struct {
	Type string `json:"type"`
}

// LoginFlows implements GET /_matrix/client/v3/login.
func (s *Service) LoginFlows() map[string]any {
	flows := []LoginFlow{{Type: "m.login.password"}}
	return map[string]any{
		"flows":                       flows,
		"m.login.password":            map[string]any{},
		"m.login.application_service": map[string]any{},
	}
}

// Capabilities implements GET /_matrix/client/v1/capabilities.
func (s *Service) Capabilities() matrix.Object {
	return matrix.Object{
		"capabilities": map[string]any{
			"m.room_versions": map[string]any{
				"default":   DefaultRoomVersion,
				"available": map[string]string{DefaultRoomVersion: "1"},
			},
			"m.change_password": map[string]bool{"enabled": true},
			"m.set_avatar_url":  map[string]bool{"enabled": true},
			"m.whoami":          map[string]bool{"enabled": true},
		},
	}
}

// HomeServerConfiguration returns the well-known client document.
func (s *Service) HomeServerConfiguration() map[string]string {
	return map[string]string{"m.homeserver": "https://" + s.serverName}
}
