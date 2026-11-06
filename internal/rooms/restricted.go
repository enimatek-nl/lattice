package rooms

import (
	"context"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/roomversion"
)

// errUnableToGrantJoin says the room's own restriction is satisfied but this server cannot
// stand behind the join. It is a different answer from a refusal, and the distinction is the
// whole point: the joining user is entitled to be in the room, and this resident server just
// is not in a position to say so — it has nobody on its own user roster who could have
// invited them. A joining server that hears this is meant to ask a different resident server
// rather than give up, which is why the specification gives it its own error code.
var errUnableToGrantJoin = errors.New("rooms: this server cannot authorise the join")

// errAllowRoomNotHeld says this server cannot tell whether the membership it would rely on in an
// allow room is still current.
//
// A restricted room points at another room and lets in that room's members. A server that is not
// itself in the room pointed at keeps a copy of it that nobody is obliged to update: it has no local
// member there, so the room's changes are not sent to it, and the membership it is holding may be
// weeks old. Granting the join from that copy would be granting it on a question this server cannot
// answer, and refusing on it would be the same invention the other way round. So the answer is
// M_UNABLE_TO_GRANT_JOIN: the room may well let the user in, and a server that is in the allow room
// is the one that can say so.
var errAllowRoomNotHeld = errors.New("rooms: this server is not in the room the restriction points at")

// RestrictedJoin works out what a join this server is about to create needs from the room's
// restricted join rule, and returns the user ID that the join event has to name as its
// authoriser. An empty return means the room asks nothing extra of this join: the rule is
// public, or the user was invited or is already in the room, and an invited user does not need
// anybody to vouch for them.
//
// Two separate questions are decided here, and they fail differently.
//
// The first is whether the joining user satisfies one of the room's allow conditions. That is
// the room's own rule, evaluated by `eventauth.MayJoinRestricted` against the state this
// server holds; nobody in an allow room is a refusal, and it is the same answer for every
// server in the room.
//
// The second is who signs. Room version 8's rule 4.2 requires the join event to be signed by
// the homeserver of the user named in `join_authorised_via_users_server`, and the
// specification describes that user as somebody on the resident server, in the room, who could
// have issued the invite. So the authoriser is chosen from this server's own joined members
// who hold the invite power level: naming somebody else's user would put a claim on the room
// that the named user's server never made, and the event would be rejected by every server that
// checks the signature rather than trusting the sender.
func (s *Service) RestrictedJoin(ctx context.Context, roomID, userID string) (string, error) {
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return "", matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", roomID)
		}
		return "", fmt.Errorf("rooms: cannot load the room for a restricted join: %w", err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return "", matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot authorise a join in.",
			roomID, room.RoomVersion).WithExtra(map[string]any{"room_version": room.RoomVersion})
	}
	if !rules.SupportsRestrictedJoin {
		// A room carrying a join rule its version cannot express is not this function's to
		// explain: the authorisation rules treat the room as one that requires an invite, and
		// saying anything here would be this server inventing a rule of its own.
		return "", nil
	}
	snapshot, err := s.loadState(ctx, roomID)
	if err != nil {
		return "", err
	}
	joinRules, have := snapshot.State(eventauth.Key{Type: eventauth.TypeJoinRules, StateKey: ""})
	if !have {
		return "", nil
	}
	rule, _ := contentOfEvent(joinRules)["join_rule"].(string)
	if rule != eventauth.JoinRuleRestricted && rule != eventauth.JoinRuleKnockRestricted {
		return "", nil
	}
	if rule == eventauth.JoinRuleKnockRestricted && !rules.SupportsKnockRestricted {
		return "", nil
	}

	// An invitation settles the question. The specification says the restrictions "will not
	// affect" a user who has an invite, and requiring an authoriser on top of an invitation
	// would turn a room's "yes" into a second opinion.
	if member, ok := snapshot.State(eventauth.Key{Type: eventauth.TypeMember, StateKey: userID}); ok {
		switch membershipOfEvent(member) {
		case eventauth.MembershipJoin, eventauth.MembershipInvite:
			return "", nil
		}
	}

	probe := map[string]any{"type": eventauth.TypeMember, "room_id": roomID, "state_key": userID}
	if err := eventauth.MayJoinRestricted(rules, probe, snapshot, eventauth.Opts{
		JoinedInRoom: func(allowRoom, member string) (bool, error) {
			state, err := s.MembershipOf(ctx, allowRoom, member)
			if err != nil {
				return false, err
			}
			if state != eventauth.MembershipJoin {
				return false, nil
			}
			// The membership says "joined" here and now. Whether that is still what the allow room
			// says is a second question, and this server is only told the answer while it is a member
			// of that room itself.
			current, err := s.stillTracksRoom(ctx, allowRoom)
			if err != nil {
				return false, err
			}
			if !current {
				return false, errAllowRoomNotHeld
			}
			return true, nil
		},
	}); err != nil {
		if failure, ok := eventauth.AsFailure(err); ok && failure.Rule == eventauth.RuleAllowLookup {
			return "", matrixerr.New(matrixerr.CodeUnableToGrantJoin,
				"This server cannot confirm the join conditions of this room.")
		}
		// The user satisfies none of the room's conditions. This is the answer every server in
		// the room gives, so there is another door to try and the joining server is told so.
		detail, _ := eventauth.AsFailure(err)
		return "", matrixerr.New(matrixerr.CodeForbidden, "You are not allowed to join this room.").
			WithExtra(map[string]any{
				"rule":   detail.Rule,
				"reason": "the room's join rule is restricted and the joining user satisfies none of its conditions.",
			})
	}

	authoriser, err := s.joinAuthoriser(ctx, roomID, rules)
	if errors.Is(err, errUnableToGrantJoin) {
		// Not a refusal: the room let the user in and this server merely cannot sign for it. The
		// joining server is expected to ask another resident server in the room.
		return "", matrixerr.New(matrixerr.CodeUnableToGrantJoin,
			"This server cannot authorise your join of this room.")
	}
	if err != nil {
		return "", err
	}
	return authoriser, nil
}

// joinAuthoriser picks the user this server will name as the authoriser of a restricted join:
// one of this server's own joined members who can invite. The pick is sorted rather than first
// out of the database, so that the same room on the same server makes the same choice twice and
// two join attempts do not differ for a reason nobody can see afterwards.
func (s *Service) joinAuthoriser(ctx context.Context, roomID string, rules roomversion.Rules) (string, error) {
	members, err := s.state.CurrentMembers(ctx, roomID)
	if err != nil {
		return "", fmt.Errorf("rooms: cannot read a room's members to authorise a join: %w", err)
	}
	entry, err := s.state.Get(ctx, roomID, eventauth.TypePowerLevels, "")
	if err != nil {
		if notFound(err) {
			return "", errUnableToGrantJoin
		}
		return "", fmt.Errorf("rooms: cannot read a room's power levels to authorise a join: %w", err)
	}
	plEvent, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return "", err
	}
	levels, err := eventauth.LoadPowerLevels(rules, contentOfEvent(plEvent))
	if err != nil {
		return "", fmt.Errorf("rooms: a room's power levels do not load: %w", err)
	}
	for _, member := range members {
		domain, err := matrixid.UserDomain(member)
		if err != nil || !matrixid.IsLocal(domain, s.name) {
			// Somebody else's user cannot be named: the join event would need that server's
			// signature, and this server does not hold its key.
			continue
		}
		if levels.User(member) >= levels.Invite {
			return member, nil
		}
	}
	return "", errUnableToGrantJoin
}

// contentOfEvent is the content map of a decoded event, or an empty map. A state event with no
// content is treated as one with no keys rather than as a broken room.
func contentOfEvent(event map[string]any) map[string]any {
	content, _ := event["content"].(map[string]any)
	if content == nil {
		return map[string]any{}
	}
	return content
}

// membershipOfEvent reads a member event's membership.
func membershipOfEvent(event map[string]any) string {
	membership, _ := contentOfEvent(event)["membership"].(string)
	return membership
}

// JoinAuthoriserValid reports whether this server can stand behind naming `candidate` as the
// authoriser of a join of this room: the candidate has to live here, be in the room, and be able
// to invite. The first condition is the one that cannot be argued around. Room version 8 rule 4.2
// demands the join event be signed by the homeserver of the named user, and this server holds only
// its own key — so naming somebody else's user produces an event this server signs and every other
// server rejects, which looks like a broken room rather than a refused join.
func (s *Service) JoinAuthoriserValid(ctx context.Context, roomID, candidate string) (bool, error) {
	if candidate == "" {
		return false, nil
	}
	if err := matrixid.UserValid(candidate); err != nil {
		return false, nil
	}
	domain, err := matrixid.UserDomain(candidate)
	if err != nil || !matrixid.IsLocal(domain, s.name) {
		return false, nil
	}
	membership, err := s.MembershipOf(ctx, roomID, candidate)
	if err != nil {
		return false, err
	}
	if membership != eventauth.MembershipJoin {
		return false, nil
	}
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("rooms: cannot load the room for a join authoriser: %w", err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return false, nil
	}
	entry, err := s.state.Get(ctx, roomID, eventauth.TypePowerLevels, "")
	if err != nil {
		if notFound(err) {
			// A room with no power levels event puts its creator at 100 and everyone else at 0;
			// the defaults decide, exactly as the authorisation rules would.
			levels := eventauth.DefaultPowerLevels()
			creator := ""
			if create, err := s.state.Get(ctx, roomID, eventauth.TypeCreate, ""); err == nil {
				if event, err := s.eventJSON(ctx, create.EventID); err == nil {
					creator, _ = contentOfEvent(event)["creator"].(string)
				}
			}
			if creator != "" {
				levels.Users[creator] = 100
			}
			return levels.User(candidate) >= levels.Invite, nil
		}
		return false, fmt.Errorf("rooms: cannot read a room's power levels for a join authoriser: %w", err)
	}
	plEvent, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return false, err
	}
	levels, err := eventauth.LoadPowerLevels(rules, contentOfEvent(plEvent))
	if err != nil {
		return false, fmt.Errorf("rooms: a room's power levels do not load: %w", err)
	}
	return levels.User(candidate) >= levels.Invite, nil
}

// stillTracksRoom says whether this server's copy of a room is one it is still being told about:
// either the room is this server's own, or a user of this server is a joined member of it, which is
// what puts this server on the room's list of servers to send changes to.
func (s *Service) stillTracksRoom(ctx context.Context, roomID string) (bool, error) {
	if _, domain, err := matrixid.SplitID(matrixid.SigilRoom, roomID); err == nil {
		if matrixid.IsLocal(domain, s.name) {
			return true, nil
		}
	}
	members, err := s.state.CurrentMembers(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("rooms: cannot read the members of the room a restriction points at: %w", err)
	}
	for _, member := range members {
		domain, err := matrixid.UserDomain(member)
		if err == nil && matrixid.IsLocal(domain, s.name) {
			return true, nil
		}
	}
	return false, nil
}
