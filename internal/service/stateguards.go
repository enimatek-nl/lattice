package service

import (
	"context"
	"encoding/json"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
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
		return validateMemberStateWrite(sender, stateKey, matrix.MustParseObject(content))
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

// validateMemberStateWrite reads a membership written through the state endpoint. It is a plain
// function because nothing in it reads the room: what the room thinks of the membership is decided
// by its rules when the event is appended.
//
// The endpoint is allowed to carry m.room.member: it is how a room's moderators ban or kick
// without the dedicated endpoints, and how a room is set up at all in some clients. Which power
// a membership asks of the sender, whether the joiner is banned, whether the join rule admits
// them, whether a kicked user sits below the kicker - all of that is the room's rules, and the
// rules are read once, when the event is appended. See [Service.judgeLocalEvent].
func validateMemberStateWrite(sender, stateKey string, content matrix.Object) error {
	membership, _ := content["membership"].(string)
	if membership == "" {
		return matrix.ErrInvalidRequest("an m.room.member event must state a membership")
	}
	if _, known := knownMemberships[membership]; !known {
		return matrix.ErrInvalidRequest("%q is not a membership", membership)
	}
	// The room's rules decide a membership written through the state endpoint exactly as they
	// decide one sent through the join, invite, kick and ban endpoints: the event is judged
	// when it is appended. What is decided here is what the endpoint means, which no room rule
	// says: a membership is not something one user can write for another unless the room's own
	// rules hand them that power, and a room does not invite or ban a moderator through the
	// moderator's own hand.
	switch membership {
	case matrix.MembershipInvite:
		if stateKey == sender {
			return matrix.ErrForbidden("you cannot invite yourself to a room")
		}
	case matrix.MembershipBan:
		if stateKey == sender {
			return matrix.ErrForbidden("you cannot ban yourself from a room this way")
		}
	}
	return nil
}

// knownMemberships are the memberships this server writes.
var knownMemberships = map[string]bool{
	matrix.MembershipJoin:   true,
	matrix.MembershipInvite: true,
	matrix.MembershipLeave:  true,
	matrix.MembershipBan:    true,
	matrix.MembershipKnock:  true,
}
