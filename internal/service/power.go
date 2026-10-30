package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// Default power levels, taken from the "Power level restrictions" section of
// the specification.
const (
	PowerAdmin          = 100
	PowerDefaultUser    = 0
	PowerDefaultInvite  = 0
	PowerDefaultKick    = 50
	PowerDefaultBan     = 50
	PowerDefaultRedact  = 50
	PowerDefaultState   = 50
	PowerDefaultMessage = 0
)

// powerLevels decodes the current m.room.power_levels event, falling back to
// the specification defaults.
func (s *Service) powerLevels(ctx context.Context, roomID string) matrix.PowerLevelsContent {
	levels := matrix.PowerLevelsContent{
		Invite:       PowerDefaultInvite,
		Kick:         PowerDefaultKick,
		Ban:          PowerDefaultBan,
		Redact:       PowerDefaultRedact,
		SendDefault:  PowerDefaultMessage,
		UsersDefault: PowerDefaultUser,
	}
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypePowerLevels, "")
	if err != nil {
		return levels
	}
	if err := matrix.DecodeContent(raw, &levels); err != nil {
		return levels
	}
	if levels.Kick == 0 {
		levels.Kick = PowerDefaultKick
	}
	if levels.Ban == 0 {
		levels.Ban = PowerDefaultBan
	}
	if levels.Redact == 0 {
		levels.Redact = PowerDefaultRedact
	}
	return levels
}

// PowerLevel returns the effective power level of a user in a room.
func (s *Service) PowerLevel(ctx context.Context, roomID, userID string) int64 {
	levels := s.powerLevels(ctx, roomID)
	if level, ok := levels.Users[userID]; ok {
		return level
	}
	room, err := s.db.RoomRepo.Get(roomID)
	if err == nil && room.Creator == userID {
		return PowerAdmin
	}
	return levels.UsersDefault
}

// eventLevel returns the power level required to send a given event type.
func eventLevel(levels matrix.PowerLevelsContent, typ string, isState bool) int64 {
	if level, ok := levels.Events[typ]; ok {
		return level
	}
	if isState {
		return PowerDefaultState
	}
	return levels.SendDefault
}

// MembershipOf returns the current membership of a user in a room, or an empty
// string when the user has no membership event.
func (s *Service) MembershipOf(ctx context.Context, roomID, userID string) string {
	row, err := s.db.MembershipRepo.Get(roomID, userID)
	if err != nil {
		return ""
	}
	return row.Membership
}

// IsJoined reports whether a user has joined a room.
func (s *Service) IsJoined(ctx context.Context, roomID, userID string) bool {
	return s.MembershipOf(ctx, roomID, userID) == matrix.MembershipJoin
}

// Allow reports whether a user may perform an action whose required power level
// is stored in the power levels event under the given key.
func (s *Service) Allow(ctx context.Context, roomID, userID, action string, fallback int64) bool {
	levels := s.powerLevels(ctx, roomID)
	required := fallback
	switch action {
	case "invite":
		required = levels.Invite
	case "kick":
		required = levels.Kick
	case "ban":
		required = levels.Ban
	case "redact":
		required = levels.Redact
	}
	return s.PowerLevel(ctx, roomID, userID) >= required
}

// RequireJoined rejects requests from users that are not joined to the room.
func (s *Service) RequireJoined(ctx context.Context, roomID, userID string) error {
	if s.MembershipOf(ctx, roomID, userID) != matrix.MembershipJoin {
		return matrix.ErrForbidden("you are not joined to the room")
	}
	return nil
}

// RequireSend rejects requests that may not send events into a room, either
// because the sender is not joined or because its power level is too low.
func (s *Service) RequireSend(ctx context.Context, roomID, userID string) error {
	if err := s.RequireJoined(ctx, roomID, userID); err != nil {
		return err
	}
	levels := s.powerLevels(ctx, roomID)
	return s.requireLevel(ctx, levels, roomID, userID, "", false, levels.SendDefault)
}

// RequireState rejects requests that may not set a specific state type.
func (s *Service) RequireState(ctx context.Context, roomID, userID, typ string) error {
	levels := s.powerLevels(ctx, roomID)
	return s.requireLevel(ctx, levels, roomID, userID, typ, true, PowerDefaultState)
}

// requireLevel compares a user's power level against a required level.
func (s *Service) requireLevel(ctx context.Context, levels matrix.PowerLevelsContent, roomID, userID, typ string, isState bool, fallback int64) error {
	required := eventLevel(levels, typ, isState)
	if required == 0 {
		required = fallback
	}
	have := s.PowerLevel(ctx, roomID, userID)
	if have < required {
		return matrix.ErrForbidden("insufficient power level: need %d, have %d", required, have)
	}
	return nil
}

// RequireReadable rejects requests from users that may not read a room, which
// depends on their membership and the room's history visibility.
func (s *Service) RequireReadable(ctx context.Context, roomID, userID string) error {
	if s.canRead(ctx, roomID, userID) {
		return nil
	}
	if s.MembershipOf(ctx, roomID, userID) == matrix.MembershipBan {
		return matrix.ErrForbidden("you are banned from this room")
	}
	return matrix.ErrForbidden("you do not have permission to read events in this room")
}

// canRead reports whether a user may read a room: joined and invited members
// always can, banned users never, and everybody else needs world readable
// history.
func (s *Service) canRead(ctx context.Context, roomID, userID string) bool {
	switch s.MembershipOf(ctx, roomID, userID) {
	case matrix.MembershipJoin, matrix.MembershipInvite:
		return true
	case matrix.MembershipBan:
		return false
	}
	return s.HistoryVisibility(ctx, roomID) == matrix.HistoryWorldReadable
}

// HistoryVisibility returns the history visibility of a room, defaulting to
// "shared".
func (s *Service) HistoryVisibility(ctx context.Context, roomID string) string {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeHistoryVis, "")
	if err != nil {
		return matrix.HistoryShared
	}
	var content matrix.HistoryVisibilityContent
	if err := matrix.DecodeContent(raw, &content); err != nil || content.HistoryVisibility == "" {
		return matrix.HistoryShared
	}
	return content.HistoryVisibility
}

// RoomIDsForUser lists rooms of a user filtered by membership.
func (s *Service) RoomIDsForUser(ctx context.Context, userID string, memberships ...string) ([]string, error) {
	ids, err := s.db.MembershipRepo.RoomsFor(userID, memberships...)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return ids, nil
}

// UserIDsForRoom lists the user IDs of a room filtered by membership.
func (s *Service) UserIDsForRoom(ctx context.Context, roomID string, memberships ...string) ([]string, error) {
	rows, err := s.db.MembershipRepo.Members(roomID, memberships...)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	return ids, nil
}
