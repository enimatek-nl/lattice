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

// profileSummaries loads what a listing shows about users: the display name,
// which falls back to the localpart of the user id just like displayName does,
// and the avatar URL. Listing members one profile at a time was a query per
// member, which is what made a member list expensive.
func (s *Service) profileSummaries(ctx context.Context, userIDs []string) (map[string]UserSummary, error) {
	seen := make(map[string]bool, len(userIDs))
	ids := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return map[string]UserSummary{}, nil
	}
	fields, err := s.db.ProfileRepo.FieldsFor(ids, ProfileDisplayName, ProfileAvatarURL)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	out := make(map[string]UserSummary, len(ids))
	for _, id := range ids {
		name := fields[id][ProfileDisplayName]
		if name == "" {
			name = matrix.LocalpartOf(id)
		}
		out[id] = UserSummary{UserID: id, DisplayName: name, AvatarURL: fields[id][ProfileAvatarURL]}
	}
	return out, nil
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
	// The profile of this one user is read once, not once per room.
	name, avatar := s.displayName(ctx, userID), s.avatarURL(ctx, userID)
	for _, roomID := range rooms {
		content := matrix.MemberContent{
			Membership:  matrix.MembershipJoin,
			DisplayName: name,
			AvatarURL:   avatar,
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
	profiles, err := s.profileSummaries(ctx, userIDs)
	if err != nil {
		return nil, false, err
	}
	var out []UserSummary
	needle := strings.ToLower(term)
	for _, id := range userIDs {
		summary := profiles[id]
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
