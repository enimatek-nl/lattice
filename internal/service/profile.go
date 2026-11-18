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

// UserSummary describes a user by the name and picture they chose for their account.
type UserSummary struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// DirectoryEntry is one line of a user directory answer. The rooms are the ones the directory is
// willing to name as a way to reach this person, which is also what makes an entry a reason: a name
// is only offered to the people who were told it somewhere.
type DirectoryEntry struct {
	UserID      string   `json:"user_id"`
	DisplayName string   `json:"display_name,omitempty"`
	AvatarURL   string   `json:"avatar_url,omitempty"`
	Rooms       []string `json:"rooms,omitempty"`
}

const (
	// directoryDefaultLimit is the cap a search that names none gets.
	directoryDefaultLimit = 200
	// directoryRoomsPerEntry caps how many rooms one entry names. The specification wants the
	// rooms ordered by relevance, and a person's fortieth shared room is not what a client is
	// choosing between.
	directoryRoomsPerEntry = 3
	// directoryRowFactor is how much wider than the limit of people the row cap is drawn, because
	// one person of many rooms occupies one row per room.
	directoryRowFactor = 20
)

// SearchUserDirectory answers who the caller may look up, by a search term.
//
// Two rooms make a person findable: a public one, which opens its members to anybody, and one the
// caller stands in, which opens them to the caller and to nobody else. The name an entry carries is
// the one the user chose for the account, never the one they chose for a room - a room's name for
// somebody is that room's business, which is the whole of what the suite's room-specific-name cases
// are guarding. Matching happens on the user id and on that account name.
func (s *Service) SearchUserDirectory(ctx context.Context, searcher, term string, limit int) ([]DirectoryEntry, bool, error) {
	if limit <= 0 {
		limit = directoryDefaultLimit
	}
	peers, err := s.db.MembershipRepo.DirectoryPeers(searcher, limit*directoryRowFactor)
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	// The query answers with a person per room; the answer is a person, naming the rooms.
	var order []string
	byUser := make(map[string][]string, len(peers))
	for _, peer := range peers {
		if _, seen := byUser[peer.UserID]; !seen {
			order = append(order, peer.UserID)
		}
		if len(byUser[peer.UserID]) < directoryRoomsPerEntry {
			byUser[peer.UserID] = append(byUser[peer.UserID], peer.RoomID)
		}
	}
	profiles, err := s.profileSummaries(ctx, order)
	if err != nil {
		return nil, false, err
	}
	// An empty answer is an empty list: `results` is a required property and a client counts it.
	out := []DirectoryEntry{}
	needle := strings.ToLower(term)
	matched := 0
	for _, id := range order {
		summary := profiles[id]
		if needle != "" &&
			!strings.Contains(strings.ToLower(id), needle) &&
			!strings.Contains(strings.ToLower(summary.DisplayName), needle) {
			continue
		}
		matched++
		if len(out) < limit {
			out = append(out, DirectoryEntry{
				UserID:      id,
				DisplayName: summary.DisplayName,
				AvatarURL:   summary.AvatarURL,
				Rooms:       byUser[id],
			})
		}
	}
	return out, matched > limit, nil
}
