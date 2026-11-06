package rooms

import (
	"context"
	"fmt"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixid"
)

// ProfileReader is the account layer's answer about what this server states about one of its own users.
//
// It is an interface of two strings rather than the account layer's own type because the room layer has
// no business knowing what else an account is: it needs a name and a picture to write into a member
// event, and the fewer assumptions it makes about where those came from, the less the two layers move
// together every time an account model changes.
type ProfileReader interface {
	ProfileFields(ctx context.Context, userID string) (displayname, avatarURL string, err error)
}

// SetProfileReader attaches the account layer. Without it a member event carries no profile, which is a
// legal event — the profile fields in `m.room.member` are the server's statement, not a requirement of
// the event format.
func (s *Service) SetProfileReader(r ProfileReader) { s.profiles = r }

// profileForMember returns the profile fields this server states about a user, for the member event a
// room is about to hold.
//
// Only a local user's profile is copied. A remote user's member event is written by that user's own
// server, and a copy taken from whatever this server happens to have remembered would be this server
// putting words in another's mouth — the disagreement is resolved by who signed the event, and signing
// it here would make the lie official.
func (s *Service) profileForMember(ctx context.Context, userID string) map[string]any {
	if s.profiles == nil {
		return nil
	}
	_, domain, err := matrixid.SplitID('@', userID)
	if err != nil || !matrixid.IsLocal(domain, s.name) {
		return nil
	}
	displayname, avatarURL, err := s.profiles.ProfileFields(ctx, userID)
	if err != nil {
		// A profile read that failed is not a reason to refuse a membership event. The event is about
		// membership; the name in it is a convenience.
		s.log.Warn("a member event is being written without a profile", "user_id", userID, "error", err)
		return nil
	}
	return map[string]any{"displayname": displayname, "avatar_url": avatarURL}
}

// RefreshProfile re-states a user's membership in every room they stand in, with the profile this
// server now states about them.
//
// A room's view of a person is the `m.room.member` event it holds, and that event carried a copy of the
// profile from the moment it was written. Without this, changing a display name changes what a client
// sees in the user's own profile and changes nothing in the room, so the same person is named two ways
// at once — and the worse part is that a room that was told once is never told again.
//
// These are ordinary sends through the room's own rules, not a bypass: the event is authored by the user
// about themselves, and a room that would not take it (a user who cannot speak there, a room mid-join)
// keeps the member event it has. One room refusing does not stop the others being told, and the first
// failure is what the caller is told about.
func (s *Service) RefreshProfile(ctx context.Context, userID, displayname, avatarURL string) error {
	if s.profiles == nil {
		return nil
	}
	roomIDs, err := s.JoinedRooms(ctx, userID)
	if err != nil {
		return fmt.Errorf("rooms: cannot list the rooms of %s to restate their profile: %w", userID, err)
	}
	var firstErr error
	for _, roomID := range roomIDs {
		_, err := s.Send(ctx, SendRequest{
			RoomID:    roomID,
			Sender:    userID,
			EventType: eventauth.TypeMember,
			StateKey:  userID,
			IsState:   true,
			Content: map[string]any{
				"membership":  eventauth.MembershipJoin,
				"displayname": displayname,
				"avatar_url":  avatarURL,
			},
		})
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("rooms: %s could not restate its profile in %s: %w", userID, roomID, err)
		}
	}
	return firstErr
}
