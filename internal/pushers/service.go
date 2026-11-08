// Package pushers holds where a user's devices have asked to be woken.
//
// A pusher is an address, not a preference. Account data says what a user wants; a pusher says
// "send the notification to this app on this device at this URL", and it was written by one
// session on behalf of one device. That is why the rows carry the session that wrote them: a
// password change stops other sessions working, and an address belonging to a dead session is
// not the new session's to keep using.
package pushers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/enimatek/lattice/internal/persist"
)

// ErrNoPushkey and its siblings are the refusals a client can act on. They are named here so
// the HTTP door can answer each in the room's own words instead of a bare "invalid param".
var (
	ErrNoPushkey   = errors.New("a push key is required")
	ErrNoAppID     = errors.New("an app ID is required")
	ErrNoDevice    = errors.New("a device display name is required")
	ErrNoKind      = errors.New("a pusher kind is required: http, or null to remove one")
	ErrEmailKind   = errors.New("this server does not deliver notifications by email")
	ErrUnknownKind = errors.New("the only pusher kind this server delivers is http")
	ErrNoURL       = errors.New("an http pusher needs data.url")
	ErrBadURL      = errors.New("data.url must be an https URL")
)

// Service writes and reads a user's pushers.
type Service struct {
	pushers persist.PusherRepo
}

// NewService binds the pusher store.
func NewService(db *persist.Database) *Service { return &Service{pushers: db.Pushers} }

// SetRequest is one client's statement about where it wants to be woken. `Kind` arrives as the
// raw JSON so that "the client said null" and "the client said nothing" are different facts:
// one removes a pusher and the other is a request the server cannot carry out.
type SetRequest struct {
	UserID    string
	TokenHash string
	Kind      *string
	Pushkey   string
	AppID     string
	// Data is the client's own object, kept as written; this server reads only `url` from it.
	Data           map[string]any
	HasData        bool
	AppDisplayName string
	DeviceDisplay  string
	ProfileTag     string
	Lang           string
}

// Set writes the pusher, or removes the one this client names when it asked for no kind.
func (s *Service) Set(ctx context.Context, req SetRequest) error {
	if req.Pushkey == "" {
		return ErrNoPushkey
	}
	if req.AppID == "" {
		return ErrNoAppID
	}
	if req.Kind == nil {
		return ErrNoKind
	}
	// `kind: null` is the specification's way of saying "stop notifying me here". The identity
	// is still (pushkey, app_id), so a client that has forgotten either is not removing the
	// pusher it thinks it is, and is told so rather than having nothing removed quietly.
	if *req.Kind == "" {
		return s.pushers.Remove(ctx, req.UserID, req.Pushkey, req.AppID)
	}
	switch *req.Kind {
	case "email":
		return ErrEmailKind
	case "http":
	default:
		return ErrUnknownKind
	}
	if req.DeviceDisplay == "" {
		return ErrNoDevice
	}
	if !req.HasData {
		return ErrNoURL
	}
	if err := checkURL(req.Data); err != nil {
		return err
	}
	encoded, err := json.Marshal(req.Data)
	if err != nil {
		return fmt.Errorf("pushers: the data an http pusher carries cannot be stored: %w", err)
	}
	return s.pushers.Put(ctx, &persist.Pusher{
		UserID:            req.UserID,
		Pushkey:           req.Pushkey,
		AppID:             req.AppID,
		Kind:              *req.Kind,
		AppDisplayName:    req.AppDisplayName,
		DeviceDisplayName: req.DeviceDisplay,
		ProfileTag:        req.ProfileTag,
		Lang:              req.Lang,
		Data:              string(encoded),
		TokenHash:         req.TokenHash,
	})
}

// checkURL is the one thing this server needs from `data`: somewhere to knock. The
// specification asks for https, and a pusher pointing at plain http would send a device's
// notification content over the open network — which is why the scheme is checked rather than
// trusted.
func checkURL(data map[string]any) error {
	raw, _ := data["url"].(string)
	if raw == "" {
		return ErrNoURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ErrBadURL
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return ErrBadURL
	}
	return nil
}

// List answers what a user's devices have asked for.
func (s *Service) List(ctx context.Context, userID string) ([]*persist.Pusher, error) {
	return s.pushers.ListForUser(ctx, userID)
}

// ForgetToken drops the pushers one session wrote, which is what logging that session out owes.
func (s *Service) ForgetToken(ctx context.Context, userID, tokenHash string) error {
	return s.pushers.DeleteByToken(ctx, userID, tokenHash)
}

// ForgetUser drops every pusher a user has, which is what ending every session owes: an
// address nobody can log into again is not a notification path, it is a leak.
func (s *Service) ForgetUser(ctx context.Context, userID string) error {
	return s.pushers.DeleteForUser(ctx, userID)
}

// ForgetAllButToken drops the pushers every session except one wrote. A password change uses
// it alongside revoking those sessions: the session that proved the new password keeps its
// address, and every session that has just stopped working loses its.
func (s *Service) ForgetAllButToken(ctx context.Context, userID, keepTokenHash string) error {
	return s.pushers.DeleteByTokenExcept(ctx, userID, keepTokenHash)
}
