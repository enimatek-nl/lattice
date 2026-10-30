package service

import (
	"context"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// ProfileKeys lists the profile keys with special meaning in the spec.
const (
	ProfileDisplayName = "displayname"
	ProfileAvatarURL   = "avatar_url"
)

// displayName returns the display name of a user, falling back to its
// localpart.
func (s *Service) displayName(ctx context.Context, userID string) string {
	value, err := s.db.ProfileRepo.Get(userID, ProfileDisplayName)
	if err != nil || value == "" {
		return matrix.LocalpartOf(userID)
	}
	return value
}

// avatarURL returns the avatar URL of a user, or an empty string.
func (s *Service) avatarURL(ctx context.Context, userID string) string {
	value, err := s.db.ProfileRepo.Get(userID, ProfileAvatarURL)
	if err != nil {
		return ""
	}
	return value
}

// Profile returns all profile keys of a user. Missing profiles yield an empty
// object rather than an error, matching the spec for unknown users.
func (s *Service) Profile(ctx context.Context, userID string) (matrix.Object, error) {
	if !s.LocalUser(userID) {
		return nil, matrix.ErrNotFound("profile not found for user %q", userID)
	}
	if !s.UserExists(ctx, userID) {
		return nil, matrix.ErrNotFound("profile not found for user %q", userID)
	}
	fields, err := s.db.ProfileRepo.All(userID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	out := matrix.Object{}
	for key, value := range fields {
		out[key] = value
	}
	return out, nil
}

// ProfileKey returns a single profile key of a user. A key that was never set
// is reported as missing, which is how the spec asks for it.
func (s *Service) ProfileKey(ctx context.Context, userID, key string) (string, error) {
	if !s.LocalUser(userID) {
		return "", matrix.ErrNotFound("profile not found for user %q", userID)
	}
	value, err := s.db.ProfileRepo.Get(userID, key)
	if err != nil {
		if persist.IsNotFound(err) {
			return "", matrix.ErrNotFound("no %s for user %q", key, userID)
		}
		return "", matrix.ErrInternal(err)
	}
	return value, nil
}

// SetProfileKey writes a single profile key and mirrors display name changes
// into the m.room.member events of rooms the user has joined.
func (s *Service) SetProfileKey(ctx context.Context, userID, key, value string) error {
	if !s.LocalUser(userID) {
		return matrix.ErrNotFound("profile not found for user %q", userID)
	}
	if err := s.db.ProfileRepo.Set(userID, key, value); err != nil {
		return matrix.ErrInternal(err)
	}
	if key != ProfileDisplayName && key != ProfileAvatarURL {
		return nil
	}
	rooms, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipJoin)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	for _, roomID := range rooms {
		content := matrix.MemberContent{
			Membership:  matrix.MembershipJoin,
			DisplayName: s.displayName(ctx, userID),
			AvatarURL:   s.avatarURL(ctx, userID),
		}
		if err := s.appendMember(ctx, roomID, userID, userID, content); err != nil {
			return err
		}
	}
	return nil
}

// DeleteProfileKey removes a single profile key.
func (s *Service) DeleteProfileKey(ctx context.Context, userID, key string) error {
	if !s.LocalUser(userID) {
		return matrix.ErrNotFound("profile not found for user %q", userID)
	}
	if err := s.db.ProfileRepo.Delete(userID, key); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// UserSummary describes a user for the user directory.
type UserSummary struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// SearchUserDirectory returns users that share a public room with the caller,
// filtered by a search term.
func (s *Service) SearchUserDirectory(ctx context.Context, searcher, term string, limit int) ([]UserSummary, bool, error) {
	if limit <= 0 {
		limit = 200
	}
	userIDs, err := s.db.MembershipRepo.DirectoryUsers(searcher, limit*2)
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	var out []UserSummary
	needle := strings.ToLower(term)
	for _, id := range userIDs {
		summary := UserSummary{
			UserID:      id,
			DisplayName: s.displayName(ctx, id),
			AvatarURL:   s.avatarURL(ctx, id),
		}
		if needle != "" &&
			!strings.Contains(strings.ToLower(id), needle) &&
			!strings.Contains(strings.ToLower(summary.DisplayName), needle) {
			continue
		}
		out = append(out, summary)
		if len(out) >= limit {
			return out, true, nil
		}
	}
	return out, false, nil
}
