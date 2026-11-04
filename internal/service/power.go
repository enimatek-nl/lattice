package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/power"
)

// Default power levels, taken from the "Power level restrictions" section of
// the specification. The arithmetic itself lives in internal/power; these names
// are the ones the service layer uses.
const (
	PowerAdmin          = power.Admin
	PowerDefaultUser    = power.DefaultUser
	PowerDefaultInvite  = power.DefaultInvite
	PowerDefaultKick    = power.DefaultKick
	PowerDefaultBan     = power.DefaultBan
	PowerDefaultRedact  = power.DefaultRedact
	PowerDefaultState   = power.DefaultState
	PowerDefaultMessage = power.DefaultMessage
	// PowerTombstone is the level a room version 12 room asks for to put up its
	// m.room.tombstone. Room version 12 gives its creators power no level can name, so the
	// ordinary administrator level no longer reaches the event that ends a room: the level
	// has to be raised above the room's own state default to keep the door shut to everyone
	// but the creators.
	PowerTombstone = 150
)

// powerLevels decodes the current m.room.power_levels event, falling back to
// the specification defaults.
func (s *Service) powerLevels(ctx context.Context, roomID string) matrix.PowerLevelsContent {
	levels := power.Defaults()
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypePowerLevels, "")
	if err != nil {
		return levels
	}
	if err := matrix.DecodeContent(raw, &levels); err != nil {
		return levels
	}
	return power.ApplyDefaults(levels)
}

// PowerLevel returns the effective power level of a user in a room.
func (s *Service) PowerLevel(ctx context.Context, roomID, userID string) int64 {
	levels := s.powerLevels(ctx, roomID)
	creators, privileged := s.roomCreators(ctx, roomID)
	return power.LevelOf(levels, userID, creators, privileged)
}

// roomCreators returns the people a room names as its creators, and whether naming them
// is what gives them power.
//
// Room version 12 is where that changes: its create event names a sender and may name
// additional_creators beside it, and every one of them holds a level no power levels
// event can write. Earlier versions know only the creator, and give them the administrator
// level the way they give anybody else their level.
func (s *Service) roomCreators(ctx context.Context, roomID string) ([]string, bool) {
	version := DefaultRoomVersion
	creator := ""
	if room, err := s.db.RoomRepo.Get(roomID); err == nil {
		version = RoomVersionOf(room)
		creator = room.Creator
	}
	privileged := eventcrypto.UsesHashedRoomIDs(version)
	content := matrix.Object{}
	if raw, err := s.db.StateRepo.Content(roomID, matrix.TypeCreate, ""); err == nil {
		if err := matrix.DecodeContent(matrix.RawMessage(raw), &content); err != nil {
			content = matrix.Object{}
		}
	}
	return power.CreatorsOf(content, creator, privileged), privileged
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
	required := power.ActionRequired(levels, action, fallback)
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
// because the sender is not joined, because the room has been replaced, or
// because its power level is too low.
func (s *Service) RequireSend(ctx context.Context, roomID, userID string) error {
	if err := s.RequireJoined(ctx, roomID, userID); err != nil {
		return err
	}
	if replacement := s.roomReplacement(ctx, roomID); replacement != "" {
		// A tombstone says the room "is not intended to be used any further". The
		// specification also asks servers to raise the old room's power levels, which
		// this server does when upgrading; this is the part of the rule that holds
		// even for the room's administrators. Membership changes go through their own
		// endpoints, so people can still leave a room that has been closed.
		return matrix.ErrForbidden("this room has been replaced by %s and no longer accepts events",
			replacement)
	}
	levels := s.powerLevels(ctx, roomID)
	return s.requireLevel(ctx, levels, roomID, userID, "", false, levels.SendDefault)
}

// roomReplacement returns the room a tombstone points at, or the empty string
// when the room has not been replaced. A state read that fails is read as "no
// tombstone", the way the power level read treats a missing or broken event: the
// request then falls through to the ordinary permission checks, which still
// decide it.
func (s *Service) roomReplacement(ctx context.Context, roomID string) string {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeTombstone, "")
	if err != nil {
		return ""
	}
	var content matrix.TombstoneContent
	if err := matrix.DecodeContent(raw, &content); err != nil {
		return ""
	}
	return content.ReplacementRoom
}

// RequireState rejects requests that may not set a specific state type.
func (s *Service) RequireState(ctx context.Context, roomID, userID, typ string) error {
	// m.room.member is judged by its membership rather than by the default level of the event
	// type. The specification gives m.room.member a default of 50, which is right for inviting
	// somebody else and wrong for joining oneself: a room whose members may not write their own
	// membership cannot carry a display name at all. validateMemberStateWrite makes that
	// distinction - invite, kick and ban against their levels, join and knock against the room's
	// join rule - and repeating the check here would only add a second place to get wrong.
	if typ == matrix.TypeMember {
		return nil
	}
	levels := s.powerLevels(ctx, roomID)
	return s.requireLevel(ctx, levels, roomID, userID, typ, true, PowerDefaultState)
}

// requireLevel compares a user's power level against a required level.
func (s *Service) requireLevel(ctx context.Context, levels matrix.PowerLevelsContent, roomID, userID, typ string, isState bool, fallback int64) error {
	required := power.EventRequired(levels, typ, isState)
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
		// A ban closes the door on the person it landed on, whatever the room's history
		// rule says.
		return false
	}
	if s.HistoryVisibility(ctx, roomID) == matrix.HistoryWorldReadable {
		return true
	}
	// A room's history also belongs to the people who were in it. Leaving ends what a member
	// may see *now*; it does not unmake the past they took part in, and a client that has
	// walked out of a room still has to be able to read the part it was given. Which events of
	// that past are actually shown is the per-event question MSC2675 asks, answered by
	// [historyGate] - `joined` and `invited` stop the window at the membership, `shared` and
	// the world-readable room do not. Somebody who was never in the room has no past here and
	// is still refused.
	return s.everJoined(ctx, roomID, userID)
}

// HistoryBound names where a reader's view of a room stops. A current member has no bound; a
// former member of a room whose history opened at the join or the invite is shown the room as
// it stood when their membership ended, and nothing after it. That is the same MSC2675 rule the
// per-event gate applies to a single event, told at the scale of a whole read: the state, the
// member list and the history of such a room are read *as of* the leave, because the room
// decided it was telling its members what happened while they were one.
func (s *Service) HistoryBound(ctx context.Context, roomID, userID string) (uint64, bool) {
	if userID == "" || !s.everJoined(ctx, roomID, userID) {
		return 0, false
	}
	switch s.MembershipOf(ctx, roomID, userID) {
	case matrix.MembershipJoin, matrix.MembershipInvite:
		return 0, false
	case matrix.MembershipBan:
		return 0, false
	}
	switch s.HistoryVisibility(ctx, roomID) {
	case matrix.HistoryJoined, matrix.HistoryInvited:
	default:
		return 0, false
	}
	pos, err := s.membershipPos(roomID, userID)
	if err != nil || pos == 0 {
		return 0, false
	}
	return pos, true
}

// everJoined says this user has been joined in the room at some point. The membership row
// outlives the join that made it, and keeps that fact for exactly this question.
func (s *Service) everJoined(ctx context.Context, roomID, userID string) bool {
	row, err := s.db.MembershipRepo.Get(roomID, userID)
	if err != nil {
		return false
	}
	return row.WasJoined
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
