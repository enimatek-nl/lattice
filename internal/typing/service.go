// Package typing keeps the "is typing in this room" indicator.
//
// A typing indicator is not an event. It has no event ID, no authorisation, no place in the
// room's graph, and — unlike a read receipt — no meaning once its timeout passes. It is the
// closest thing Matrix has to a hand raised in a meeting: the client says "I am typing for the
// next N milliseconds", the server repeats that to the other members for as long as it is true,
// and then it is simply not true any more.
//
// Two things follow for the implementation. The indicator has to be joinable to a sync window,
// so each change takes a position in the same stream events use ([persist.Typing]); and the
// indicator going stale has to be reported rather than left to fade, because a client that was
// shown somebody typing has to be shown them stop. Both are handled by the sweep in
// [Service.Sweep], which runs where the indicator is read.
package typing

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
)

// maxTimeout is how long this server will keep an indicator alive on a client's word. The
// specification sets no ceiling, and an unbounded one is an invitation to a stale "X is typing"
// on other people's screens for hours — a client that crashed cannot take it back. Five minutes
// is longer than any typing burst a person makes without the client refreshing the indicator, and
// a client that means to keep typing refreshes it. This is a decision of ours, not a rule from
// the specification, and it is recorded here so it is not mistaken for one.
const maxTimeout = 5 * time.Minute

// Service answers "who is typing here", and takes a client's word for it.
type Service struct {
	db     *persist.Database
	typing persist.TypingRepo
	state  persist.StateRepo
	// Now is the clock. It is a field so that a test can make an indicator expire without
	// sleeping for the timeout it was given.
	Now func() time.Time
}

// New returns a typing service over db.
func New(db *persist.Database) *Service {
	return &Service{db: db, typing: db.Typing, state: db.State, Now: time.Now}
}

// WithTx returns the same service working inside an open transaction.
func (s *Service) WithTx(tx *gorm.DB) *Service {
	return &Service{db: s.db, typing: s.typing.WithTx(tx), state: s.state.WithTx(tx), Now: s.Now}
}

// Set records what a client says about its own typing. `timeout` is how long the statement should
// stay true; when typing is false the timeout is not read, because stopping has no length.
//
// Only a member of the room may raise the indicator: the indicator is shown to that room's
// members, and letting a non-member set one would put a name on their screens that the room has
// no reason to trust.
func (s *Service) Set(ctx context.Context, roomID, userID string, typing bool, timeout time.Duration) error {
	if timeout < 0 {
		return matrixerr.Newf(matrixerr.CodeInvalidParam,
			"A typing timeout is a number of milliseconds, not %d.", timeout.Milliseconds())
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	if typing {
		member, err := s.isMember(ctx, roomID, userID)
		if err != nil {
			return err
		}
		if !member {
			return matrixerr.Newf(matrixerr.CodeForbidden,
				"%s is not a member of %s, so their typing is not that room's to show.", userID, roomID)
		}
	}
	until := s.nowMillis() + timeout.Milliseconds()
	if _, err := s.typing.Set(ctx, roomID, userID, typing, until); err != nil {
		return err
	}
	return nil
}

// Users returns who is typing in a room right now, oldest indicator's owner first by user ID.
// The sweep runs first: a reader asking the question is the natural moment to notice that an
// indicator has gone stale, and the answer it gets should be the current one rather than the one
// from before the last timeout.
func (s *Service) Users(ctx context.Context, roomID string) ([]string, error) {
	if _, err := s.typing.ExpireRoom(ctx, roomID, s.nowMillis()); err != nil {
		return nil, err
	}
	return s.typing.List(ctx, roomID, s.nowMillis())
}

// LastChange is the stream position of the room's most recent typing change, zero when the room
// has never had one. A sync window compares it against its own bounds.
func (s *Service) LastChange(ctx context.Context, roomID string) (int64, error) {
	return s.typing.LastChange(ctx, roomID)
}

// Sweep turns this room's expired indicators into stream positions. It is called where the
// indicator is read, and it is a write: an indicator timing out is a change to what the room's
// members are shown, and a window that never learns it leaves a hand raised forever.
func (s *Service) Sweep(ctx context.Context, roomID string) error {
	_, err := s.typing.ExpireRoom(ctx, roomID, s.nowMillis())
	return err
}

// Empty drops one user's indicator without taking a position. A user who has left a room is not
// typing in it; their membership event is what tells everybody else that, so the indicator is
// quietly emptied rather than announced.
func (s *Service) Empty(ctx context.Context, roomID, userID string) error {
	return s.typing.EmptyForUser(ctx, roomID, userID)
}

// isMember asks the room's current membership state. Typing is rate-limited traffic in a small
// room, so reading the room's members is affordable here; the alternative — a one-row lookup —
// would need the membership value carried somewhere other than the event that states it.
func (s *Service) isMember(ctx context.Context, roomID, userID string) (bool, error) {
	members, err := s.state.CurrentMembers(ctx, roomID)
	if err != nil {
		return false, err
	}
	for _, member := range members {
		if member == userID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) nowMillis() int64 { return s.Now().UnixMilli() }
