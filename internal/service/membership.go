package service

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// joinAccountProblem says whether the account a join is asked for is one this server can
// act for, and names the reason when it is not. "You must have an account to join a room"
// covers three different situations, and an operator cannot tell them apart from that
// sentence: the account belongs to a different server name (which is what happens when
// -server-name changes under a database that already has accounts - the old sessions still
// authenticate, the client still looks logged in, and every join then fails), the account
// is not in this database at all, or the account was deactivated.
func (s *Service) joinAccountProblem(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.LocalUser(userID) {
		return matrix.ErrForbidden("the session belongs to %s, which is not an account of this server (%s)"+
			" - a changed -server-name leaves existing accounts able to read this server but unable to join rooms", userID, s.serverName)
	}
	user, err := s.db.UserRepo.GetByID(userID)
	if persist.IsNotFound(err) || user == nil {
		return matrix.ErrForbidden("the account %s is not registered on this server", userID)
	}
	if err != nil {
		return matrix.ErrInternal(err)
	}
	if user.Deactivated {
		return matrix.ErrForbidden("the account %s has been deactivated", userID)
	}
	return nil
}

// JoinRoom makes a user join a room by ID or alias and returns the room ID.
//
// The join is rejected when the room's join rule requires an invite, when the
// user is banned, or when the room does not exist locally.
func (s *Service) JoinRoom(ctx context.Context, idOrAlias, userID string, extra matrix.Object, via []string) (string, error) {
	// An alias another server hosts names a room whose address only that server can
	// give, and it is also one of the servers that can be asked for the join: the alias
	// is published there, so a server that resolves the alias has just been told it is
	// in the room.
	target := idOrAlias
	if strings.HasPrefix(idOrAlias, "#") {
		if alias := matrix.Alias(idOrAlias); alias.Domain() != s.serverName {
			roomID, err := s.ResolveRemoteAlias(ctx, alias)
			if err != nil {
				return "", err
			}
			target = roomID
			if !containsServer(via, alias.Domain()) {
				via = append(via, alias.Domain())
			}
		}
	}
	room, err := s.MustRoom(ctx, target)
	if err != nil {
		// A room this server has never heard of is not necessarily a room that does not
		// exist: it may live on another server, and the servers to ask are the ones the
		// client named. Asking is the only way to find out, and the answer is the room's
		// state - which is what the join handshake is for.
		//
		// The miss is asked about as a spec error because MustRoom has already turned the
		// storage miss into one: testing the storage error here would be a check that can
		// only ever be false, which is how a join request came to answer 404 for a room
		// the server had not yet tried to reach.
		if matrix.HasCode(err, matrix.ErrCodeNotFound) && strings.HasPrefix(target, "!") {
			if _, ferr := s.JoinFederatedRoom(ctx, target, userID, via); ferr != nil {
				return "", ferr
			}
			return target, nil
		}
		return "", err
	}
	if !s.LocalRoom(room.ID) {
		if _, ferr := s.JoinFederatedRoom(ctx, room.ID, userID, via); ferr != nil {
			return "", ferr
		}
		return room.ID, nil
	}
	current := s.MembershipOf(ctx, room.ID, userID)
	if current == matrix.MembershipJoin {
		return room.ID, nil
	}
	if current == matrix.MembershipBan {
		return "", matrix.ErrForbidden("you are banned from this room")
	}
	if problem := s.joinAccountProblem(ctx, userID); problem != nil {
		return "", problem
	}

	problem := s.joinRuleProblem(ctx, room.ID, userID, current)
	if problem != nil {
		return "", problem
	}

	content := matrix.MemberContent{
		Membership:  matrix.MembershipJoin,
		DisplayName: s.displayName(ctx, userID),
		AvatarURL:   s.avatarURL(ctx, userID),
	}
	if reason, ok := extra["reason"].(string); ok {
		content.Reason = reason
	}
	// Whatever else the client sent in the body of the join is its own, and goes
	// into the member event unchanged. Dropping it would break a bridge that
	// identifies itself there, and no server-side rule says what those keys may be.
	if len(extra) > 0 {
		content.Extra = matrix.Object{}
		for key, value := range extra {
			switch key {
			case "reason", "membership", "type":
				continue
			}
			content.Extra[key] = value
		}
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
	joined := make(map[string]bool)
	if rooms, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipJoin); err == nil {
		for _, id := range rooms {
			joined[id] = true
		}
	}
	return content.GrantsAccessTo(func(room string) bool { return joined[room] })
}

// joinRuleProblem answers whether the room's current join rule lets this user become a joined
// member, given the membership they hold now. It is asked by the join endpoints and by the state
// endpoint, which can carry a membership event: the rule belongs to the room, not to whichever
// door the request came through.
func (s *Service) joinRuleProblem(ctx context.Context, roomID, userID, current string) error {
	joinRule, err := s.db.StateRepo.JoinRule(roomID)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	switch joinRule {
	case matrix.JoinRulesPublic:
		return nil
	case matrix.JoinRulesInvite:
		if current != matrix.MembershipInvite && !s.IsAdmin(ctx, userID) {
			return matrix.ErrForbidden("an invite is required to join this room")
		}
		return nil
	case matrix.JoinRulesKnock:
		if current != matrix.MembershipKnock && current != matrix.MembershipInvite {
			return matrix.ErrForbidden("you must knock on this room before joining")
		}
		return nil
	case matrix.JoinRulesKnockRestricted:
		// A knock_restricted room answers two ways. Whoever the allow list admits walks in on
		// that; the rest have to knock and wait for an invite. Requiring the knock first turns
		// the half the rule exists for - admitting a member of another room - into a wait, and
		// a room that admits its own space's members through a knock is not that room.
		if s.restrictedJoinAllowed(ctx, roomID, userID) {
			return nil
		}
		if current != matrix.MembershipKnock && current != matrix.MembershipInvite {
			return matrix.ErrForbidden("you must knock on this room before joining")
		}
		return nil
	case matrix.JoinRulesRestricted:
		if !s.restrictedJoinAllowed(ctx, roomID, userID) && current != matrix.MembershipInvite {
			return matrix.ErrForbidden("you are not permitted to join this room")
		}
		return nil
	default:
		return matrix.ErrForbidden("join rule %q is not supported", joinRule)
	}
}

// InviteRoom invites a user to a room.
func (s *Service) InviteRoom(ctx context.Context, roomID, inviter, invitee, reason string, extra matrix.Object) error {
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
	// A user this server hosts has to exist: inviting a name that owns no account here is a
	// typo, and a typo is an error rather than an invitation nobody can use. A user on
	// another server is an address rather than an account this server can look up - the
	// server that owns the user decides whether it exists - so refusing a remote name is a
	// room refusing to reach users it has not met yet.
	if matrix.DomainOf(invitee) == s.serverName && !s.UserExists(ctx, invitee) {
		return matrix.ErrNotFound("user %s does not exist", invitee)
	}
	if invitee == inviter {
		return matrix.ErrForbidden("you cannot invite yourself to a room")
	}
	target := s.MembershipOf(ctx, room.ID, invitee)
	switch target {
	case matrix.MembershipJoin:
		// Already a member: the invite would be a membership change nobody asked
		// for. The specification answers M_FORBIDDEN rather than a conflict, because
		// the room is not in a state the client can resolve by retrying - the user is
		// simply already there.
		return matrix.ErrForbidden("user %s is already joined to this room", invitee)
	case matrix.MembershipBan:
		return matrix.ErrForbidden("user is banned from this room")
	}
	// Whether the invitee's server is already part of the room is asked before the invite
	// is written, because the invite itself puts that server in the room: asked afterwards,
	// every invitation looks like one the room already reaches, and the endpoint that ships
	// the authorising state is never used.
	needsInvite := false
	if !s.LocalUser(invitee) {
		shares, err := s.roomSharesServer(ctx, room.ID, matrix.DomainOf(invitee))
		if err != nil {
			return err
		}
		needsInvite = !shares
	}
	content := matrix.MemberContent{
		Membership:  matrix.MembershipInvite,
		DisplayName: s.displayName(ctx, invitee),
		AvatarURL:   s.avatarURL(ctx, invitee),
		Reason:      reason,
		// The keys of an invitation this server does not name belong to the client that
		// sent it, and one of them decides how the room is read: is_direct is how a client
		// knows a room is a conversation with one person rather than a group. Dropping it
		// turns every direct chat into a group chat, in this server's view and in every
		// server the invitation travels to.
		Extra: extra,
	}
	if err := s.appendMember(ctx, room.ID, inviter, invitee, content); err != nil {
		return err
	}
	// An invite to a user whose server is not part of the room has exactly one road: the
	// invite endpoint, which ships the invite together with the state that authorises it.
	// The ordinary relay only reaches servers the room already talks to, and a room inviting
	// somebody new is inviting a server it has never spoken to.
	if needsInvite {
		if err := s.InviteRemoteServer(ctx, room.ID, invitee); err != nil {
			// The invite stands here either way: the room has decided, and the event
			// travels to that server as soon as it can be reached. Refusing the invite
			// because a peer is not answering would be this server deciding the room's
			// business on a peer's behalf.
			log.Printf("service: inviting %s into %s over federation: %v", invitee, room.ID, err)
		}
	}
	return nil
}

// KnockRoom records a knock on a room, which is the request-to-join flow.
func (s *Service) KnockRoom(ctx context.Context, idOrAlias, userID, reason string) (string, error) {
	room, err := s.MustRoom(ctx, idOrAlias)
	if err != nil {
		// A room held nowhere here is not a room that cannot be knocked on - it is a room
		// that lives on another server, and the knock has to be carried there.
		if matrix.HasCode(err, matrix.ErrCodeNotFound) && strings.HasPrefix(idOrAlias, "!") {
			if _, ferr := s.KnockFederatedRoom(ctx, idOrAlias, userID, reason); ferr != nil {
				return "", ferr
			}
			return idOrAlias, nil
		}
		return "", err
	}
	if !s.LocalRoom(room.ID) {
		if problem := s.knownMembershipBlocksKnock(ctx, room.ID, userID); problem != nil {
			return "", matrix.ErrForbidden("%v", problem)
		}
		return s.KnockFederatedRoom(ctx, room.ID, userID, reason)
	}
	joinRule, err := s.db.StateRepo.JoinRule(room.ID)
	if err != nil {
		return "", matrix.ErrInternal(err)
	}
	if problem := s.knockProblemWith(ctx, joinRule, s.MembershipOf(ctx, room.ID, userID)); problem != nil {
		return "", problem
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

// knockProblem answers whether the room's join rule admits a knock from a user holding the given
// membership. Knocking is not a courtesy: a room that has not said it may be knocked on has no
// knock to accept, and a user who already has a membership in the room has no reason to knock.
func (s *Service) knockProblem(ctx context.Context, roomID, current string) error {
	joinRule, err := s.db.StateRepo.JoinRule(roomID)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	return s.knockProblemWith(ctx, joinRule, current)
}

// knockProblemWith is knockProblem with the join rule already read, for the callers that needed it
// for another reason anyway.
func (s *Service) knockProblemWith(ctx context.Context, joinRule, current string) error {
	if joinRule != matrix.JoinRulesKnock && joinRule != matrix.JoinRulesKnockRestricted {
		return matrix.ErrForbidden("this room does not allow knocking")
	}
	// A knock can be made again. The room answers a knock with an invite or a refusal, and a
	// refusal is a leave - so "I have already knocked" would bar the second try of the one
	// sequence knocking exists for. A knock of a user already in the room, already invited,
	// or banned is not a request but a mistake: the room has already answered that user.
	switch current {
	case matrix.MembershipJoin, matrix.MembershipInvite, matrix.MembershipBan:
		return matrix.ErrForbidden("you already have a membership in this room")
	}
	return nil
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
	if !s.LocalRoom(room.ID) {
		// A room this server does not host is left through the server that does. The room's
		// graph belongs to that server: a leave made here attaches to a copy of the graph,
		// and the room's own server is left with a member it still thinks is present.
		if err := s.LeaveFederatedRoom(ctx, room.ID, userID, reason); err == nil {
			return nil
		} else {
			// The peer's refusal is not a reason to keep somebody in a room they have asked
			// to leave, so the leave is recorded locally and relayed like any other event.
			log.Printf("service: leaving %s over federation: %v", room.ID, err)
		}
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
	switch s.MembershipOf(ctx, room.ID, target) {
	case matrix.MembershipJoin, matrix.MembershipInvite, matrix.MembershipKnock:
		// A kick is how a room withdraws any standing it granted somebody: it throws out a
		// member, and it takes back an invitation or a knock. Refusing the invited case is
		// what strands an invite a room has already changed its mind about - the membership
		// stays, the user keeps seeing the room, and only a ban clears it.
	default:
		// Kicking somebody who is not in the room is refused rather than reported as
		// missing: the caller has the room, has the user id, and is not permitted to do
		// this to them. Clients read 404 as "that user was never here", which is a
		// different claim from the true one.
		return matrix.ErrForbidden("user %s is not a member of the room", target)
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
		// The specification answers a forgotten room that is still joined with a bad
		// request rather than a refusal: the client asked for something the room
		// allows no version of, and leaving first is a different call.
		return matrix.NewError(matrix.ErrCodeUnknown, http.StatusBadRequest,
			"you cannot forget a room you are joined to")
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
		summary := profiles[row.UserID]
		// Both keys are always written. The endpoint answers "who is here and how do
		// they appear", and a member with no display name or avatar still has both:
		// a client that finds no avatar_url key has to guess whether the member has
		// no image or whether the server answered something else.
		entry := matrix.Object{
			"display_name": summary.DisplayName,
			"avatar_url":   summary.AvatarURL,
		}
		joined[row.UserID] = entry
	}
	return joined, nil
}
