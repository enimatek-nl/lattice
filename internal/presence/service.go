// Package presence keeps what this server knows about whether its users are around.
//
// Presence is a small feature with a sharp edge: it is a current value, but clients are
// told about *changes*. That is why every change takes a position in the same stream
// events use, and why a mere sighting of a user (a request, a sync) deliberately does not.
package presence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// Service reads and writes presence.
type Service struct {
	db       *persist.Database
	presence persist.PresenceRepo
	name     string
	log      *slog.Logger
	// now is replaced in tests, for the same reason the room service replaces it: the
	// server's clock is the answer, and a test has to control it to assert on it.
	now func() time.Time
}

// New builds the presence service.
func New(db *persist.Database, serverName string, log *slog.Logger) *Service {
	return &Service{
		db:       db,
		presence: db.Presence,
		name:     serverName,
		log:      log,
		now:      time.Now,
	}
}

// Status is a user's presence as a client is told it.
type Status struct {
	Presence   string
	StatusMsg  string
	LastActive int64
	// LastActiveAgo is how long ago this server last saw the user, which is what the
	// client is shown. The difference is taken here, against the service's clock, rather
	// than in the handler: the clock that saw the user is the only one allowed to say how
	// long ago that was.
	LastActiveAgo int64
	Active        bool
	// Known says whether this server has ever reported this user. A user it has never
	// seen is answered as offline rather than as an error: the question "are they around"
	// has the answer "no" before it has the answer "I don't know them".
	Known bool
}

// Set stores a presence change and takes a stream position for it, so that other users
// who share a room with this one are told.
func (s *Service) Set(ctx context.Context, userID, state, statusMsg string) error {
	if !persist.PresenceKnown(state) {
		return matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a presence this server knows.", state)
	}
	if _, err := s.presence.Set(ctx, userID, state, statusMsg, state == persist.PresenceOnline, s.now().UnixMilli()); err != nil {
		return fmt.Errorf("presence: cannot store the status of %s: %w", userID, err)
	}
	return nil
}

// SetIfChanged stores a presence change only when it differs from what is stored.
//
// This is what keeps `/sync?set_presence=online` from announcing the user to everybody
// they share a room with on every poll: an unchanged status is not news.
func (s *Service) SetIfChanged(ctx context.Context, userID, state string) error {
	if !persist.PresenceKnown(state) {
		return matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a presence this server knows.", state)
	}
	current, err := s.presence.Get(ctx, userID)
	if err == nil && current.State == state {
		return nil
	}
	if err != nil && !errors.Is(err, persist.ErrNotFound) {
		return fmt.Errorf("presence: cannot read the status of %s: %w", userID, err)
	}
	return s.Set(ctx, userID, state, "")
}

// Status reads a user's presence.
func (s *Service) Status(ctx context.Context, userID string) (Status, error) {
	row, err := s.presence.Get(ctx, userID)
	if errors.Is(err, persist.ErrNotFound) {
		return Status{Presence: persist.PresenceOffline}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("presence: cannot read the status of %s: %w", userID, err)
	}
	ago := s.now().UnixMilli() - row.LastActiveTS
	if ago < 0 {
		// A timestamp ahead of the clock means the clock moved backwards. Zero is the
		// honest floor; a negative age is a number no client can show.
		ago = 0
	}
	return Status{
		Presence:      row.State,
		StatusMsg:     row.StatusMsg,
		LastActive:    row.LastActiveTS,
		LastActiveAgo: ago,
		Active:        row.Active,
		Known:         true,
	}, nil
}

// MarkActive records that this server saw the user just now, without announcing it.
func (s *Service) MarkActive(ctx context.Context, userID string) {
	if err := s.presence.MarkActive(ctx, userID, s.now().UnixMilli()); err != nil {
		// Being unable to note that somebody is around is not worth failing a request
		// over, and it is worth a line in the log.
		s.log.Warn("presence: cannot note that a user was seen", "user_id", userID, "error", err)
	}
}

// Announce gives a user's presence a new stream position, keeping the status message they
// chose. It is what a room join does for the members of the room: from now on these users
// share a room, so their presence is each other's to see.
func (s *Service) Announce(ctx context.Context, userID string) error {
	domain, err := matrixid.UserDomain(userID)
	if err != nil {
		return err
	}
	if !matrixid.IsLocal(domain, s.name) {
		// Presence for a user on another server arrives over federation, which is not
		// implemented; inventing it here would tell clients something the server does
		// not know.
		return nil
	}
	statusMsg := ""
	current, err := s.presence.Get(ctx, userID)
	if err == nil {
		statusMsg = current.StatusMsg
	} else if !errors.Is(err, persist.ErrNotFound) {
		return fmt.Errorf("presence: cannot read the status of %s: %w", userID, err)
	}
	if _, err := s.presence.Set(ctx, userID, persist.PresenceOnline, statusMsg, true, s.now().UnixMilli()); err != nil {
		return fmt.Errorf("presence: cannot announce %s: %w", userID, err)
	}
	return nil
}

// EnsureDefaults gives every account this server has that has no presence row one, so
// that a server upgraded into this code reports its existing users instead of answering
// "offline" for all of them until they happen to sync.
func (s *Service) EnsureDefaults(ctx context.Context) (int, error) {
	ids, err := s.db.Users.ListUserIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("presence: cannot list the accounts: %w", err)
	}
	created := 0
	for _, userID := range ids {
		_, err := s.presence.Get(ctx, userID)
		if err == nil {
			continue
		}
		if !errors.Is(err, persist.ErrNotFound) {
			return created, fmt.Errorf("presence: cannot read the status of %s: %w", userID, err)
		}
		if _, err := s.presence.Set(ctx, userID, persist.PresenceOnline, "", true, s.now().UnixMilli()); err != nil {
			return created, fmt.Errorf("presence: cannot store the status of %s: %w", userID, err)
		}
		created++
	}
	return created, nil
}
