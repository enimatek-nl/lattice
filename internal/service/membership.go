package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// JoinRoom makes a user join a room by ID or alias and returns the room ID.
//
// The join is rejected when the room's join rule requires an invite, when the
// user is banned, or when the room does not exist locally.
func (s *Service) JoinRoom(ctx context.Context, idOrAlias, userID string, extra matrix.Object) (string, error) {
	room, err := s.MustRoom(ctx, idOrAlias)
	if err != nil {
		return "", err
	}
	if !s.LocalRoom(room.ID) {
		return "", matrix.ErrNotFound("room %s is not hosted by this server", room.ID)
	}
	current := s.MembershipOf(ctx, room.ID, userID)
	if current == matrix.MembershipJoin {
		return room.ID, nil
	}
	if current == matrix.MembershipBan {
		return "", matrix.ErrForbidden("you are banned from this room")
	}
	if !s.UserExists(ctx, userID) {
		return "", matrix.ErrForbidden("you must have an account to join a room")
	}

	joinRule, err := s.db.StateRepo.JoinRule(room.ID)
	if err != nil {
		return "", matrix.ErrInternal(err)
	}
	switch joinRule {
	case matrix.JoinRulesPublic:
	case matrix.JoinRulesInvite:
		if current != matrix.MembershipInvite && !s.IsAdmin(ctx, userID) {
			return "", matrix.ErrForbidden("an invite is required to join this room")
		}
	case matrix.JoinRulesKnock, matrix.JoinRulesKnockRestricted:
		if current != matrix.MembershipKnock && current != matrix.MembershipInvite {
			return "", matrix.ErrForbidden("you must knock on this room before joining")
		}
	case matrix.JoinRulesRestricted:
		if !s.restrictedJoinAllowed(ctx, room.ID, userID) && current != matrix.MembershipInvite {
			return "", matrix.ErrForbidden("you are not permitted to join this room")
		}
	default:
		return "", matrix.ErrForbidden("join rule %q is not supported", joinRule)
	}

	content := matrix.MemberContent{
		Membership:  matrix.MembershipJoin,
		DisplayName: s.displayName(ctx, userID),
		AvatarURL:   s.avatarURL(ctx, userID),
	}
	if reason, ok := extra["reason"].(string); ok {
		content.Reason = reason
	}
	if err := s.appendMember(ctx, room.ID, userID, userID, content); err != nil {
		return "", err
	}
	return room.ID, nil
}

// restrictedJoinAllowed reports whether a user already belongs to one of the
// rooms that grant restricted access.
func (s *Service) restrictedJoinAllowed(ctx context.Context, roomID, userID string) bool {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeJoinRules, "")
	if err != nil {
		return false
	}
	var content matrix.JoinRulesContent
	if err := matrix.DecodeContent(raw, &content); err != nil {
		return false
	}
	rooms, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipJoin)
	if err != nil {
		return false
	}
	joined := make(map[string]bool, len(rooms))
	for _, id := range rooms {
		joined[id] = true
	}
	for _, allow := range content.Allow {
		if allow.Type == "m.room_membership" && joined[allow.RoomID] {
			return true
		}
	}
	return false
}

// InviteRoom invites a user to a room.
func (s *Service) InviteRoom(ctx context.Context, roomID, inviter, invitee string, reason string) error {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return err
	}
	if err := s.RequireJoined(ctx, room.ID, inviter); err != nil {
		return err
	}
	if !s.Allow(ctx, room.ID, inviter, "invite", PowerDefaultInvite) {
		return matrix.ErrForbidden("you do not have permission to invite users to this room")
	}
	if !s.UserExists(ctx, invitee) {
		return matrix.ErrNotFound("user %s does not exist", invitee)
	}
	target := s.MembershipOf(ctx, room.ID, invitee)
	switch target {
	case matrix.MembershipJoin:
		return matrix.ErrConflict(matrix.ErrCodeUnknown, "user is already joined")
	case matrix.MembershipBan:
		return matrix.ErrForbidden("user is banned from this room")
	}
	content := matrix.MemberContent{
		Membership:  matrix.MembershipInvite,
		DisplayName: s.displayName(ctx, invitee),
		AvatarURL:   s.avatarURL(ctx, invitee),
		Reason:      reason,
	}
	return s.appendMember(ctx, room.ID, inviter, invitee, content)
}

// KnockRoom records a knock on a room, which is the request-to-join flow.
func (s *Service) KnockRoom(ctx context.Context, idOrAlias, userID, reason string) (string, error) {
	room, err := s.MustRoom(ctx, idOrAlias)
	if err != nil {
		return "", err
	}
	joinRule, err := s.db.StateRepo.JoinRule(room.ID)
	if err != nil {
		return "", matrix.ErrInternal(err)
	}
	if joinRule != matrix.JoinRulesKnock && joinRule != matrix.JoinRulesKnockRestricted {
		return "", matrix.ErrForbidden("this room does not allow knocking")
	}
	if s.MembershipOf(ctx, room.ID, userID) != "" {
		return "", matrix.ErrForbidden("you already have a membership in this room")
	}
	content := matrix.MemberContent{
		Membership:  matrix.MembershipKnock,
		DisplayName: s.displayName(ctx, userID),
		AvatarURL:   s.avatarURL(ctx, userID),
		Reason:      reason,
	}
	if err := s.appendMember(ctx, room.ID, userID, userID, content); err != nil {
		return "", err
	}
	return room.ID, nil
}

// LeaveRoom makes a user leave a room.
func (s *Service) LeaveRoom(ctx context.Context, roomID, userID, reason string) error {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return err
	}
	membership := s.MembershipOf(ctx, room.ID, userID)
	if membership == "" || membership == matrix.MembershipLeave {
		return matrix.ErrForbidden("you cannot leave a room you are not a member of")
	}
	content := matrix.MemberContent{
		Membership: matrix.MembershipLeave,
		Reason:     reason,
	}
	return s.appendMember(ctx, room.ID, userID, userID, content)
}

// KickRoom removes a member from a room.
func (s *Service) KickRoom(ctx context.Context, roomID, requester, target, reason string) error {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return err
	}
	if err := s.RequireJoined(ctx, room.ID, requester); err != nil {
		return err
	}
	if !s.Allow(ctx, room.ID, requester, "kick", PowerDefaultKick) {
		return matrix.ErrForbidden("you do not have permission to kick users from this room")
	}
	if s.MembershipOf(ctx, room.ID, target) != matrix.MembershipJoin {
		return matrix.ErrNotFound("user %s is not a member of the room", target)
	}
	content := matrix.MemberContent{
		Membership: matrix.MembershipLeave,
		Reason:     reason,
	}
	return s.appendMember(ctx, room.ID, requester, target, content)
}

// BanRoom bans a user from a room.
func (s *Service) BanRoom(ctx context.Context, roomID, requester, target, reason string) error {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return err
	}
	if err := s.RequireJoined(ctx, room.ID, requester); err != nil {
		return err
	}
	if !s.Allow(ctx, room.ID, requester, "ban", PowerDefaultBan) {
		return matrix.ErrForbidden("you do not have permission to ban users from this room")
	}
	content := matrix.MemberContent{
		Membership: matrix.MembershipBan,
		Reason:     reason,
	}
	return s.appendMember(ctx, room.ID, requester, target, content)
}

// UnbanRoom revokes a ban.
func (s *Service) UnbanRoom(ctx context.Context, roomID, requester, target string) error {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return err
	}
	if err := s.RequireJoined(ctx, room.ID, requester); err != nil {
		return err
	}
	if !s.Allow(ctx, room.ID, requester, "ban", PowerDefaultBan) {
		return matrix.ErrForbidden("you do not have permission to unban users in this room")
	}
	if s.MembershipOf(ctx, room.ID, target) != matrix.MembershipBan {
		return matrix.ErrNotFound("user %s is not banned from the room", target)
	}
	content := matrix.MemberContent{Membership: matrix.MembershipLeave}
	return s.appendMember(ctx, room.ID, requester, target, content)
}

// ForgetRoom drops the user's membership record so the room disappears from
// their account. The user must no longer be joined.
func (s *Service) ForgetRoom(ctx context.Context, roomID, userID string) error {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return err
	}
	membership := s.MembershipOf(ctx, room.ID, userID)
	if membership == matrix.MembershipJoin {
		return matrix.ErrForbidden("you cannot forget a room you are joined to")
	}
	if membership == "" {
		return nil
	}
	if err := s.db.MembershipRepo.Forget(room.ID, userID); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// JoinedRooms lists the room IDs a user has joined.
func (s *Service) JoinedRooms(ctx context.Context, userID string) ([]string, error) {
	ids, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipJoin)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return ids, nil
}

// Member is a room member entry of /members and /joined_members.
type Member struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Membership  string `json:"membership,omitempty"`
	Event       *matrix.Event
}

// Members lists members of a room, optionally filtered by membership and
// excluded by a "not membership" filter.
func (s *Service) Members(ctx context.Context, roomID string, memberships []string, notMembership string) ([]Member, error) {
	all := []string{
		matrix.MembershipJoin, matrix.MembershipInvite,
		matrix.MembershipLeave, matrix.MembershipBan, matrix.MembershipKnock,
	}
	if len(memberships) == 0 {
		if notMembership != "" {
			memberships = all
		} else {
			// The specification defaults the membership filter to join.
			memberships = []string{matrix.MembershipJoin}
		}
	}
	for _, want := range memberships {
		if want == "*" {
			memberships = all
			break
		}
	}
	rows, err := s.db.MembershipRepo.Members(roomID, memberships...)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	out := make([]Member, 0, len(rows))
	// The rows to report are collected first so the member events and the profiles
	// behind them can each be read in one query.
	kept := make([]int, 0, len(rows))
	for i, row := range rows {
		if notMembership != "" && row.Membership == notMembership {
			continue
		}
		kept = append(kept, i)
	}
	userIDs := make([]string, 0, len(kept))
	eventIDs := make([]string, 0, len(kept))
	for _, i := range kept {
		userIDs = append(userIDs, rows[i].UserID)
		eventIDs = append(eventIDs, rows[i].EventID)
	}
	profiles, err := s.profileSummaries(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	events, err := s.db.EventRepo.GetByIDs(eventIDs)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	for _, i := range kept {
		row := rows[i]
		member := Member{
			UserID:      row.UserID,
			DisplayName: profiles[row.UserID].DisplayName,
			AvatarURL:   profiles[row.UserID].AvatarURL,
			Membership:  row.Membership,
		}
		if event, ok := events[row.EventID]; ok {
			ev := event.Matrix()
			member.Event = &ev
		}
		out = append(out, member)
	}
	return out, nil
}

// JoinedMembers implements GET /_matrix/client/v3/rooms/{roomId}/joined_members
// and returns the joined members keyed by user ID.
func (s *Service) JoinedMembers(ctx context.Context, roomID string) (map[string]matrix.Object, error) {
	rows, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	joined := make(map[string]matrix.Object, len(rows))
	userIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, row.UserID)
	}
	profiles, err := s.profileSummaries(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		entry := matrix.Object{}
		summary := profiles[row.UserID]
		if summary.DisplayName != "" {
			entry["display_name"] = summary.DisplayName
		}
		if summary.AvatarURL != "" {
			entry["avatar_url"] = summary.AvatarURL
		}
		joined[row.UserID] = entry
	}
	return joined, nil
}
