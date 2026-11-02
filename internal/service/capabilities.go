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
		// A client creating a room without naming a version needs to know which one
		// it is getting: room versions change event ids, and a client that guesses
		// wrong cannot verify what the server sent.
		"default_room_version": DefaultRoomVersion,
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
//
// The room version values are the strings "stable" and "unstable", not the "1"
// that early implementations of this endpoint used: a client that cannot match
// its room version to a stable entry tells the user the room needs an upgrade.
// Lattice creates and stores every version it lists here and applies one set of
// state and redaction rules to all of them; it does not vary them per version.
func (s *Service) Capabilities() matrix.Object {
	available := make(map[string]string, len(s.roomVersions))
	for _, version := range s.roomVersions {
		available[version] = "stable"
	}
	return matrix.Object{
		"capabilities": map[string]any{
			"m.room_versions": map[string]any{
				"default":   DefaultRoomVersion,
				"available": available,
			},
			"m.change_password": map[string]bool{"enabled": true},
			"m.set_avatar_url":  map[string]bool{"enabled": true},
			"m.whoami":          map[string]bool{"enabled": true},
		},
	}
}

// HomeServerConfiguration returns the well-known client document: the base URL
// clients should talk to. The specification nests it under base_url, and the
// same document is returned by /login and /register.
func (s *Service) HomeServerConfiguration() matrix.Object {
	return matrix.Object{
		"m.homeserver": matrix.Object{"base_url": s.PublicBaseURL()},
	}
}

// PublicBaseURL is the URL clients should reach this homeserver on, as
// published in the discovery documents.
func (s *Service) PublicBaseURL() string {
	if s.publicBaseURL != "" {
		return s.publicBaseURL
	}
	return "https://" + s.serverName
}
