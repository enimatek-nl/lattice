package service

import (
	"context"
	"encoding/json"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/power"
)

// validateStateEvent rejects state updates that the state endpoints may not
// carry, and checks the power level rules of m.room.power_levels.
func (s *Service) validateStateEvent(ctx context.Context, roomID, sender, typ, stateKey string, content matrix.RawMessage) error {
	switch typ {
	case matrix.TypeCreate:
		return matrix.ErrForbidden("the m.room.create event cannot be replaced")
	case matrix.TypeEncryption:
		// The specification never says a room may be re-encrypted with a different
		// algorithm, and a room whose encryption changes cannot decrypt what came
		// before it: clients treat the first m.room.encryption as final and would
		// silently keep sending under settings nobody agreed to. Once set, it stays.
		if _, err := s.db.StateRepo.Get(roomID, matrix.TypeEncryption, ""); err == nil {
			return matrix.ErrForbidden("a room's encryption settings cannot be changed once set")
		} else if !persist.IsNotFound(err) {
			return matrix.ErrInternal(err)
		}
	case matrix.TypeMember:
		return s.validateMemberStateWrite(ctx, roomID, sender, stateKey, matrix.MustParseObject(content))
	case matrix.TypePowerLevels:
		return s.validatePowerLevels(ctx, roomID, sender, matrix.MustParseObject(content))
	case matrix.TypeCanonicalAlias:
		return s.validateCanonicalAlias(ctx, roomID, content)
	}
	return nil
}

// validateCanonicalAlias checks the alias and alt_aliases of an
// m.room.canonical_alias event against the alias table of this server.
//
// A canonical alias is the address a room advertises, so an entry that points at
// nothing (or at another room) makes the room unjoinable through its own stated
// address: a client that follows it gets a different room or a 404. The
// specification therefore splits the two mistakes: a string that is not an alias
// at all is a parameter error (M_INVALID_PARAM), while an alias that exists but
// belongs to somebody else's room is a bad alias (M_BAD_ALIAS).
func (s *Service) validateCanonicalAlias(ctx context.Context, roomID string, content matrix.RawMessage) error {
	var body struct {
		Alias      *string  `json:"alias"`
		AltAliases []string `json:"alt_aliases"`
	}
	if err := json.Unmarshal(content, &body); err != nil {
		return matrix.ErrBadJSON(err)
	}
	aliases := body.AltAliases
	if body.Alias != nil {
		aliases = append([]string{*body.Alias}, aliases...)
	}
	for _, candidate := range aliases {
		if candidate == "" {
			// An empty alias is the same as no alias: the room says it has no
			// canonical address rather than claiming a broken one.
			continue
		}
		if !matrix.Alias(candidate).Valid() {
			return matrix.ErrInvalidParam("%q is not a room alias", candidate)
		}
		// Only aliases this server hosts can be checked; a remote alias is the
		// other server's business to resolve.
		if matrix.Alias(candidate).Domain() != s.serverName {
			continue
		}
		row, err := s.db.AliasRepo.Get(candidate)
		if err != nil {
			if persist.IsNotFound(err) {
				return matrix.ErrBadAlias("alias %s does not exist on this server", candidate)
			}
			return matrix.ErrInternal(err)
		}
		if row.RoomID != roomID {
			return matrix.ErrBadAlias("alias %s points at room %s, not %s", candidate, row.RoomID, roomID)
		}
	}
	return nil
}

// validatePowerLevels enforces the power level restrictions of the
// specification: changing the level of a user requires a level that is at
// least as high as both the current and the requested level of that user.
func (s *Service) validatePowerLevels(ctx context.Context, roomID, sender string, content matrix.Object) error {
	requested := intMap(content["users"])
	if len(requested) == 0 {
		return nil
	}
	levels := s.powerLevels(ctx, roomID)
	have := s.PowerLevel(ctx, roomID, sender)
	for userID, want := range requested {
		current, explicit := levels.Users[userID]
		if !explicit {
			current = s.PowerLevel(ctx, roomID, userID)
		}
		if current == want {
			continue
		}
		needed := power.ChangeRequired(current, want)
		if have < needed {
			return matrix.ErrForbidden(
				"changing the power level of %s requires a power level of %d, you have %d", userID, needed, have)
		}
	}
	return nil
}

// intMap coerces a decoded JSON value into a map of integers.
func intMap(value any) map[string]int64 {
	out := map[string]int64{}
	switch typed := value.(type) {
	case map[string]any:
		for key, raw := range typed {
			switch number := raw.(type) {
			case float64:
				out[key] = int64(number)
			case int64:
				out[key] = number
			case int:
				out[key] = int64(number)
			}
		}
	case map[string]int64:
		for key, number := range typed {
			out[key] = number
		}
	}
	return out
}

// validateMemberStateWrite judges a membership event written through the state
// endpoint.
//
// The endpoint is allowed to carry m.room.member: it is how a room's moderators ban or
// kick without the dedicated endpoints, and how a room is set up at all in some clients.
// Refusing it outright was a guess at safety; what the specification actually asks is
// that the membership decide which power level the sender must hold, so invite needs the
// room's invite level, a kick its kick level and a ban its ban level. Two memberships
// stay personal whatever the sender's power: joining and knocking are things only the
// user in question can do, and no level buys them.
func (s *Service) validateMemberStateWrite(ctx context.Context, roomID, sender, stateKey string, content matrix.Object) error {
	membership, _ := content["membership"].(string)
	if membership == "" {
		return matrix.ErrInvalidRequest("an m.room.member event must state a membership")
	}
	switch membership {
	case matrix.MembershipJoin, matrix.MembershipKnock:
		if stateKey != sender {
			return matrix.ErrForbidden("only the user themselves can join or knock on a room")
		}
		// A membership written through the state endpoint is still an entry into the room, so
		// the room's join rule decides it. Checking only the power level here would let a room
		// moderator walk a stranger into an invite-only room, and checking nothing at all would
		// make the state endpoint a door with no guard.
		current := s.MembershipOf(ctx, roomID, sender)
		if membership == matrix.MembershipJoin {
			return s.joinRuleProblem(ctx, roomID, sender, current)
		}
		return s.knockProblem(ctx, roomID, current)
	case matrix.MembershipLeave:
		if stateKey == sender {
			return nil
		}
		if !s.Allow(ctx, roomID, sender, "kick", PowerDefaultKick) {
			return matrix.ErrForbidden("you need permission to remove another user from this room")
		}
		return nil
	case matrix.MembershipInvite:
		if stateKey == sender {
			return matrix.ErrForbidden("you cannot invite yourself to a room")
		}
		if !s.Allow(ctx, roomID, sender, "invite", PowerDefaultInvite) {
			return matrix.ErrForbidden("you need permission to invite users to this room")
		}
		return nil
	case matrix.MembershipBan:
		if stateKey == sender {
			return matrix.ErrForbidden("you cannot ban yourself from a room this way")
		}
		if !s.Allow(ctx, roomID, sender, "ban", PowerDefaultBan) {
			return matrix.ErrForbidden("you need permission to ban users from this room")
		}
		return nil
	default:
		return matrix.ErrInvalidRequest("%q is not a membership", membership)
	}
}
