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
	"github.com/enimatek-nl/lattice/internal/federation"
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
	// roomVersions are the room versions this server speaks: the ones it will create,
	// the ones it offers in a make_join, and the ones it will hold a room in. It is
	// configuration rather than a constant because the list grows with the specification,
	// and a deployment may want to offer fewer than it understands.
	roomVersions []string
	// federationEnabled is set by -federation. It decides whether the API layer
	// mounts the server-server routes and whether the outbox queues anything; the
	// signing key exists either way, because events are signed where they are created.
	federationEnabled bool
	// outboxTraffic is the signed client this server sends federation traffic with. The
	// API layer builds it and hands it over, because its configuration lives there.
	federationMu  sync.Mutex
	outboxTraffic *federation.Transport
	// outboxLocks serialise attempts per destination, which is what keeps the
	// specification's one-outstanding-transaction rule true inside one process.
	outboxMu    sync.Mutex
	outboxLocks map[string]*sync.Mutex
	// signingKeyMu guards the cached server signing key, so two concurrent first
	// requests cannot each mint a keypair.
	signingKeyMu sync.Mutex
	signingKey   *federation.KeyPair
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

// WithFederation turns the server-server layer on: the signing key of this
// homeserver is generated at start-up, and the API layer mounts the federation
// routes. Without it Lattice is a single-server homeserver, routes and database
// alike.
func WithFederation(enabled bool) Option {
	return func(s *Service) { s.federationEnabled = enabled }
}

// WithRoomVersions replaces the room versions this server speaks. Versions this
// server does not speak are refused at creation, and a room in a version it does not
// speak cannot be joined - which is why the join handshake says so by name rather than
// as a room it could not find.
func WithRoomVersions(versions ...string) Option {
	return func(s *Service) { s.roomVersions = versions }
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
		roomVersions:      append([]string(nil), SupportedRoomVersions...),
		uiaSessions:       map[string]*authSession{},
	}
	for _, opt := range opts {
		opt(s)
	}
	// The counter is seeded from the highest position the database holds, not from the highest
	// event: a restarted server must not hand out positions it already issued, or the clients
	// holding a since token around that point stop seeing anything at all.
	pos, err := db.LatestStreamPos()
	if err != nil {
		return nil, err
	}
	s.stream.Store(pos)
	// The signing key is minted at start-up whether or not the server-server API is
	// mounted, because every event this server creates is hashed and signed with it.
	// A server that started signing later would hold events that no other homeserver
	// can check, and rooms created before the first key existed cannot be repaired
	// afterwards: the signature and the hash are part of what those events are.
	if _, err := s.SigningKey(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// DB exposes the storage layer to the API package.
func (s *Service) DB() *persist.Database { return s.db }

// FederationEnabled reports whether this process serves the server-server API.
func (s *Service) FederationEnabled() bool { return s.federationEnabled }

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

// LocalRoom reports whether this homeserver hosts a room.
//
// For a room version up to 11 that is a question about the identifier: the id names the
// server that made the room, so this server hosts it exactly when the id names this
// server. Room version 12 breaks that reading - the id is a hash of the room's create event
// and names no server at all - so the same question is asked of the room itself: the server
// named by the creator in that create event is the one that made the room, which is what
// the identifier used to say.
//
// Holding a copy is deliberately not the test. An invite leaves a room row behind on the
// invited server, and a past join leaves one where the user once was; a server that treated
// either as hosting would answer a join from its own copy instead of asking the room, and
// would write a membership the room has not agreed to - the two servers then hold different
// histories under one room id. The places that need the weaker fact ("do we know this room?")
// read the room row directly.
func (s *Service) LocalRoom(roomID string) bool {
	if !matrix.IsHashedRoomID(roomID) {
		return matrix.DomainOf(roomID) == s.serverName
	}
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		return false
	}
	return matrix.DomainOf(room.Creator) == s.serverName
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
			// The alias belongs to another server, which is the only one that knows what
			// it points at. Asking is what the specification's alias resolution is: an
			// answer of "not hosted here" without the question leaves a client with a room
			// it cannot reach although it named it exactly.
			return s.ResolveRemoteAlias(ctx, alias)
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
