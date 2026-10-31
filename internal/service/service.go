// Package service holds the homeserver business logic: appending events, room
// lifecycle, membership transitions and sync. Handlers in internal/api stay
// thin and delegate here.
package service

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/realtime"
)

// Service bundles the dependencies the business logic needs.
type Service struct {
	db         *persist.Database
	serverName string
	hub        *realtime.Hub
	tokens     *auth.TokenService
	// stream is a process-wide monotonic counter backing event stream
	// positions. It is seeded from the database on start-up.
	stream atomic.Uint64
	// maxUploadSize limits media uploads in bytes.
	maxUploadSize int64
	// reservationWindow is how long a media id minted by POST /_matrix/media/v3/create
	// waits for its content before maintenance drops it.
	reservationWindow time.Duration
	// registrationSecrets are the tokens the m.login.registration_token stage
	// accepts. An empty list means registration is open to anyone.
	registrationSecrets []string
	// publicBaseURL is the URL published in the discovery documents. It defaults
	// to https:// plus the server name, which is wrong whenever clients reach
	// the server through a proxy or a tunnel.
	publicBaseURL string
	// uiaMu guards the in-memory User-Interactive Auth sessions, which remember
	// the stages a client has already completed.
	uiaMu       sync.Mutex
	uiaSessions map[string]*authSession
	// now is overridable for tests.
	now func() time.Time
}

// Option customises a [Service].
type Option func(*Service)

// WithClock injects a clock, which makes time dependent behaviour testable.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithMaxUploadSize sets the media upload limit in bytes.
func WithMaxUploadSize(size int64) Option {
	return func(s *Service) { s.maxUploadSize = size }
}

// WithMediaReservationWindow sets how long POST /_matrix/media/v3/create keeps a
// media id that has not received its bytes yet.
func WithMediaReservationWindow(window time.Duration) Option {
	return func(s *Service) {
		if window > 0 {
			s.reservationWindow = window
		}
	}
}

// WithRegistrationSecrets restricts self-service registration to clients that
// present one of these tokens, which turns the server into an invitation only
// one and replaces the open m.login.dummy registration flow.
func WithRegistrationSecrets(secrets ...string) Option {
	return func(s *Service) {
		for _, secret := range secrets {
			if secret = strings.TrimSpace(secret); secret != "" {
				s.registrationSecrets = append(s.registrationSecrets, secret)
			}
		}
	}
}

// WithPublicBaseURL publishes the URL clients should use in the well-known
// document and in login and register responses.
func WithPublicBaseURL(url string) Option {
	return func(s *Service) { s.publicBaseURL = strings.TrimRight(strings.TrimSpace(url), "/") }
}

// New builds a service for the given database and server name.
func New(db *persist.Database, serverName string, tokens *auth.TokenService, opts ...Option) (*Service, error) {
	s := &Service{
		db:                db,
		serverName:        serverName,
		hub:               realtime.NewHub(),
		tokens:            tokens,
		now:               time.Now,
		maxUploadSize:     DefaultMaxUploadSize,
		reservationWindow: DefaultMediaReservationWindow,
		uiaSessions:       map[string]*authSession{},
	}
	for _, opt := range opts {
		opt(s)
	}
	pos, err := db.EventRepo.LatestStreamPos()
	if err != nil {
		return nil, err
	}
	s.stream.Store(pos)
	return s, nil
}

// DB exposes the storage layer to the API package.
func (s *Service) DB() *persist.Database { return s.db }

// Tokens exposes the token service to the API package.
func (s *Service) Tokens() *auth.TokenService { return s.tokens }

// ServerName returns the configured homeserver name.
func (s *Service) ServerName() string { return s.serverName }

// Hub returns the realtime hub used to wake long polling sync calls.
func (s *Service) Hub() *realtime.Hub { return s.hub }

// Now returns the current time using the injected clock.
func (s *Service) Now() time.Time { return s.now() }

// NowMS returns the current time in unix milliseconds.
func (s *Service) NowMS() int64 { return s.now().UnixMilli() }

// nextStreamPos allocates the next event stream position. Positions are shared
// by all rooms so a single token can describe a global sync position.
func (s *Service) nextStreamPos() uint64 {
	return s.stream.Add(1)
}

// StreamPos returns the current highest stream position.
func (s *Service) StreamPos() uint64 { return s.stream.Load() }

// Token renders a stream position as a sync token.
func (s *Service) Token(pos uint64) string { return auth.StreamPos(pos) }

// ParseToken converts a sync token into a stream position.
func (s *Service) ParseToken(token string) (uint64, error) {
	pos, err := auth.ParseStreamPos(token)
	if err != nil {
		return 0, matrix.ErrInvalidParam("invalid batch token %q", token)
	}
	return pos, nil
}

// Notify wakes all long polling sync calls. Call it after persisting new data.
func (s *Service) Notify() { s.hub.Notify() }

// Localroom reports whether a room identifier belongs to this homeserver.
func (s *Service) LocalRoom(roomID string) bool {
	return matrix.DomainOf(roomID) == s.serverName
}

// LocalUser reports whether a user identifier belongs to this homeserver.
func (s *Service) LocalUser(userID string) bool {
	return matrix.DomainOf(userID) == s.serverName
}

// UserExists reports whether a local user account exists and is active.
func (s *Service) UserExists(ctx context.Context, userID string) bool {
	if !s.LocalUser(userID) {
		return false
	}
	user, err := s.db.UserRepo.GetByID(userID)
	if err != nil {
		if persist.IsNotFound(err) {
			log.Printf("user lookup failed for %s: %v", userID, err)
		}
		return false
	}
	return !user.Deactivated
}

// RoomOrAlias resolves a room ID or alias into a room ID.
func (s *Service) RoomOrAlias(ctx context.Context, idOrAlias string) (string, error) {
	if idOrAlias == "" {
		return "", matrix.ErrNotFound("no such room")
	}
	switch idOrAlias[0] {
	case '#':
		alias := matrix.Alias(idOrAlias)
		if !alias.Valid() {
			return "", matrix.ErrInvalidRequest("invalid room alias %q", idOrAlias)
		}
		if alias.Domain() != s.serverName {
			return "", matrix.ErrNotFound("alias %s is not hosted by this server", idOrAlias)
		}
		row, err := s.db.AliasRepo.Get(idOrAlias)
		if err != nil {
			if persist.IsNotFound(err) {
				return "", matrix.ErrNotFound("no such alias %s", idOrAlias)
			}
			return "", matrix.ErrInternal(err)
		}
		return row.RoomID, nil
	case '!':
		return idOrAlias, nil
	default:
		return "", matrix.ErrInvalidRequest("%q is not a room ID or alias", idOrAlias)
	}
}

// MustRoom resolves a room ID or alias and verifies the room exists.
func (s *Service) MustRoom(ctx context.Context, idOrAlias string) (*persist.Room, error) {
	roomID, err := s.RoomOrAlias(ctx, idOrAlias)
	if err != nil {
		return nil, err
	}
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no such room %s", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	return room, nil
}

// FormatPos renders a stream position without the token prefix, for pagination
// tokens of room history.
func FormatPos(pos uint64) string { return strconv.FormatUint(pos, 10) }
