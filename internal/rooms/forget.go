package rooms

import (
	"context"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/persist"
)

// ErrStillInRoom says a forget was refused because the requester is still in the room. The
// Client-Server API does not give this case an error code of its own, so it answers 400
// `M_UNKNOWN` — a bad request that is not the client's malformed body but the client's
// impossible intention — and the sentinel is how the handler tells it from a storage fault
// without matching on the words of a message.
var ErrStillInRoom = errors.New("still a member of the room")

// Forget carries out POST /rooms/{roomID}/forget: this user asks this server to stop telling
// them about a room they are out of.
//
// What is written is one row about that person, not an edit to the room. The membership event
// that took them out stays in the room's history exactly as it was signed, and every other
// member's view of the room is untouched. What stops is this user's view: the room leaves their
// `/sync`, and the reads a former member is otherwise entitled to — `/messages`, `/state`,
// `/members` — are refused until they come back. That is the asymmetry the endpoint exists for:
// "I do not want to see this" is a fact about the viewer.
//
// Someone still in the room is refused. Leaving is the step that says it, and it has its own
// endpoint, its own authorisation and its own event; letting forget do both would let a client
// remove itself from a room without the room ever being told.
func (s *Service) Forget(ctx context.Context, roomID, userID string) error {
	if _, err := s.requireRoom(ctx, roomID); err != nil {
		return err
	}
	membership, err := s.MembershipOf(ctx, roomID, userID)
	if err != nil {
		return err
	}
	switch membership {
	case eventauth.MembershipJoin, eventauth.MembershipInvite, eventauth.MembershipKnock:
		return fmt.Errorf("%w: %s is %s of this room", ErrStillInRoom, userID, membership)
	}
	// The position is the moment the forget took effect, and a synchronisation needs it: a
	// window that already holds the membership event that took the user out is news the client
	// has not been told yet, and dropping it would lose the leaving from the client's record of
	// the room forever.
	position, err := s.db.StreamPosition(ctx)
	if err != nil {
		return fmt.Errorf("rooms: cannot read the stream position: %w", err)
	}
	if _, err := s.forgotten.Mark(ctx, &persist.RoomForget{
		UserID:      userID,
		RoomID:      roomID,
		ForgottenAt: position,
	}); err != nil {
		return fmt.Errorf("rooms: cannot record that %s forgot %s: %w", userID, roomID, err)
	}
	return nil
}

// forgotten answers whether one user has forgotten one room.
func (s *Service) forgottenRoom(ctx context.Context, roomID, userID string) (*persist.RoomForget, error) {
	mark, err := s.forgotten.Of(ctx, userID, roomID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read whether %s forgot %s: %w", userID, roomID, err)
	}
	return mark, nil
}

// rememberRoomAgain clears the forget marker, which is what coming back to a room does: joining
// it again, or being invited into it, undoes the request not to be told. The membership event is
// what says so; the marker is only this server remembering the earlier request.
func (s *Service) rememberRoomAgain(ctx context.Context, roomID, userID string) error {
	if err := s.forgotten.Clear(ctx, userID, roomID); err != nil {
		return fmt.Errorf("rooms: cannot clear the forget of %s for %s: %w", userID, roomID, err)
	}
	return nil
}
