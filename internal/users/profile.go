package users

import (
	"context"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
)

// Profile is what this server states about an account: the name it is shown as and the picture it is
// shown with.
//
// This is not the account. An account is a localpart, a password hash and a set of devices; a profile
// is a claim this server makes about it, which other servers may carry in their room state and may
// disagree with. A user of another server has a profile this server did not decide, which is why a
// profile read is answered about a local account and a member event is answered by the room's own
// state.
type Profile struct {
	UserID      string
	Displayname string
	AvatarURL   string
}

// profileMaxFieldBytes is this server's own bound on a profile field's length, in bytes.
//
// The specification does not impose one: the profile PUT says the client's value MUST be a string, and
// that servers MAY validate. A bound is kept anyway because a profile is copied into a room's member
// events, so an unbounded one is amplified into every room a user joins — and the refusal is
// M_TOO_LARGE, which is what the specification's error vocabulary has for an entity that was too large,
// rather than M_INVALID_PARAM, which would claim the value was the wrong sort of value.
const profileMaxFieldBytes = 255

// ProfileBroadcaster is the room layer's side of a profile change.
//
// A profile is not only read from this endpoint: a room's members see each other through the
// `m.room.member` events the room holds, and those events carried a copy of the profile at the moment
// they were written. Leaving them stale is what makes a client show two different names for one person
// depending on which event it happens to be rendering. So a change here has to be pushed out as new
// member events, and only the room layer can do that — it knows which rooms the user stands in, and it
// is the only place allowed to append to them.
type ProfileBroadcaster interface {
	RefreshProfile(ctx context.Context, userID, displayname, avatarURL string) error
}

// SetProfileBroadcaster attaches the room layer that receives profile changes. Unset, a profile still
// changes; the rooms simply keep the member events they already have.
func (s *Service) SetProfileBroadcaster(b ProfileBroadcaster) { s.profiles = b }

// ProfileOf reads an account's profile.
func (s *Service) ProfileOf(ctx context.Context, userID string) (Profile, error) {
	user, err := s.Get(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	return Profile{UserID: user.UserID, Displayname: user.Displayname, AvatarURL: user.AvatarURL}, nil
}

// ProfileFields answers the same question as [Service.ProfileOf] in two strings, which is the shape the
// room layer asks for. It exists so that a room can write a member event without importing the account
// package's types: the room needs a name and a picture, and nothing about what else an account is.
func (s *Service) ProfileFields(ctx context.Context, userID string) (string, string, error) {
	profile, err := s.ProfileOf(ctx, userID)
	if err != nil {
		return "", "", err
	}
	return profile.Displayname, profile.AvatarURL, nil
}

// SetProfile writes the fields a client named and leaves the others alone, which is what the two
// separate endpoints need: a client PUTting a display name does not have to resend an avatar URL it
// did not read.
//
// The value is checked here rather than at the HTTP edge because it is checked for the account, not for
// the request: a display name over the bound the specification gives would be a claim this server hands
// to peers that then refuse it, and an avatar that is not a content URI is a pointer no server can
// follow.
func (s *Service) SetProfile(ctx context.Context, userID string, displayname, avatarURL *string) (Profile, error) {
	current, err := s.ProfileOf(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	next := current
	if displayname != nil {
		value, err := profileFieldValue("displayname", *displayname)
		if err != nil {
			return Profile{}, err
		}
		next.Displayname = value
	}
	if avatarURL != nil {
		value, err := profileFieldValue("avatar_url", *avatarURL)
		if err != nil {
			return Profile{}, err
		}
		next.AvatarURL = value
	}
	if err := s.db.Users.SetProfile(ctx, userID, next.Displayname, next.AvatarURL); err != nil {
		if err == persist.ErrNotFound {
			return Profile{}, matrixerr.Newf(matrixerr.CodeNotFound, "No profile is held for %s.", userID)
		}
		return Profile{}, err
	}
	if s.profiles != nil {
		if err := s.profiles.RefreshProfile(ctx, userID, next.Displayname, next.AvatarURL); err != nil {
			// The profile has changed. A room that would not take the refreshed member event — one the
			// user cannot speak in, one mid-join, one whose remote server is down — is reported and left
			// as it is, because undoing an account's own name change over a room's own trouble would be
			// the stranger outcome.
			s.log.Warn("a profile changed and a room could not be told",
				"user_id", userID, "error", err)
		}
	}
	return next, nil
}

// ClearProfile removes one profile field, which is what DELETE /profile/{userId}/{keyName} asks for.
//
// "Cleared" and "never set" are the same state here on purpose. A room's member events are rewritten
// with the empty value, so a client that was rendering a name stops rendering it; keeping a marker for
// "this user removed their name" would be a distinction no reader acts on.
func (s *Service) ClearProfile(ctx context.Context, userID, keyName string) (Profile, error) {
	current, err := s.ProfileOf(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	next := current
	switch keyName {
	case "displayname":
		next.Displayname = ""
	case "avatar_url":
		next.AvatarURL = ""
	default:
		return Profile{}, matrixerr.Newf(matrixerr.CodeNotFound,
			"This server holds no profile field %q.", keyName)
	}
	if err := s.db.Users.SetProfile(ctx, userID, next.Displayname, next.AvatarURL); err != nil {
		if err == persist.ErrNotFound {
			return Profile{}, matrixerr.Newf(matrixerr.CodeNotFound, "No profile is held for %s.", userID)
		}
		return Profile{}, err
	}
	if s.profiles != nil {
		if err := s.profiles.RefreshProfile(ctx, userID, next.Displayname, next.AvatarURL); err != nil {
			s.log.Warn("a profile changed and a room could not be told",
				"user_id", userID, "error", err)
		}
	}
	return next, nil
}

// profileFieldValue is where a profile value is accepted or refused.
//
// The length is checked; the shape is not. That is a decision and not an oversight. The specification
// makes the client responsible for sending a content URI as an avatar, says plainly that servers MAY
// validate, and says clients MUST expect values they did not ask for. Complement — this project's
// interoperability reference — puts the string "LemurLover" into an avatar_url and expects success, so a
// server that refuses it is enforcing a rule the specification left optional and failing a test that is
// not about that rule. The shape is enforced where it is acted on: fetching media has to parse the
// identifier, and that is where [matrixid.SplitMXC] belongs.
func profileFieldValue(keyName, value string) (string, error) {
	if len(value) > profileMaxFieldBytes {
		return "", matrixerr.Newf(matrixerr.CodeTooLarge,
			"A profile field may be at most %d bytes; %q is %d.", profileMaxFieldBytes, keyName, len(value))
	}
	return value, nil
}
