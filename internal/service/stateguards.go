package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// validateStateEvent rejects state updates that the state endpoints may not
// carry, and checks the power level rules of m.room.power_levels.
func (s *Service) validateStateEvent(ctx context.Context, roomID, sender, typ, stateKey string, content matrix.RawMessage) error {
	switch typ {
	case matrix.TypeCreate:
		// A room already exists, so its create event is a fact about the past: in room
		// version 12 it is also the hash the room is named by, and rewriting it would rename
		// the room. The request is refused as a bad request rather than a permission problem -
		// no level anywhere lets a client send this event into a room that is already made.
		return matrix.NewError(matrix.ErrCodeForbidden, http.StatusBadRequest,
			"the m.room.create event cannot be replaced")
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
	case matrix.TypePowerLevels:
		return s.validatePowerLevelsWrite(ctx, roomID, content)
	case matrix.TypeCanonicalAlias:
		return s.validateCanonicalAlias(ctx, roomID, content)
	}
	return nil
}

// maxCanonicalJSON is the largest magnitude canonical JSON can carry without losing a
// digit: 2^53-1, past which a double stops being exact. A power level outside it cannot be
// written into an event at all, so a client that asks for one is handed a 400 naming the
// number rather than a 500 from the signer.
const maxCanonicalJSON = float64(1<<53 - 1)

// validatePowerLevelsWrite checks a power levels event at the door, before it is hashed,
// signed and judged by the room's rules. Two problems are worth catching here rather than
// letting the rules answer: a level outside what canonical JSON can name cannot be signed,
// and a room version 12 power levels event that names one of the room's creators is
// refused by rule 10.4 - in both cases the client typed something into a field and wants
// the field named back, not a 403 from a rule it never could have satisfied.
func (s *Service) validatePowerLevelsWrite(ctx context.Context, roomID string, content matrix.RawMessage) error {
	var decoded matrix.Object
	if err := json.Unmarshal(content, &decoded); err != nil {
		return matrix.ErrBadJSON(err)
	}
	for _, field := range []string{"users_default", "events_default", "state_default", "ban", "redact", "kick", "invite"} {
		if value, present := decoded[field]; present {
			if err := levelIsSpellable(field, value); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"users", "events", "notifications"} {
		value, present := decoded[field]
		if !present {
			continue
		}
		entries, ok := value.(map[string]any)
		if !ok {
			return matrix.ErrInvalidRequest("content.%s of an m.room.power_levels event must be an object", field)
		}
		for name, entry := range entries {
			if err := levelIsSpellable(field+"."+name, entry); err != nil {
				return err
			}
		}
	}
	creators, privileged := s.roomCreators(ctx, roomID)
	if privileged {
		if named := powerLevelsNamesUsers(decoded, creators); len(named) > 0 {
			return matrix.NewError(matrix.ErrCodeForbidden, http.StatusBadRequest,
				fmt.Sprintf("a room version 12 power levels event cannot name %s, who is a creator of this room",
					strings.Join(named, ", ")))
		}
	}
	return nil
}

// levelIsSpellable reports whether a power level is a whole number canonical JSON can
// carry.
func levelIsSpellable(field string, value any) error {
	switch typed := value.(type) {
	case float64:
		if typed != math.Trunc(typed) {
			return matrix.ErrInvalidRequest("content.%s of an m.room.power_levels event is %v, which is not a whole number", field, typed)
		}
		if typed > maxCanonicalJSON || typed < -maxCanonicalJSON {
			return matrix.ErrInvalidRequest("content.%s of an m.room.power_levels event is %v, past the largest number canonical JSON can name", field, typed)
		}
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE") {
			return matrix.ErrInvalidRequest("content.%s of an m.room.power_levels event is %s, which is not a plain whole number", field, text)
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return matrix.ErrInvalidRequest("content.%s of an m.room.power_levels event is not a number: %s", field, text)
		}
		if parsed > maxCanonicalJSON || parsed < -maxCanonicalJSON {
			return matrix.ErrInvalidRequest("content.%s of an m.room.power_levels event is %s, past the largest number canonical JSON can name", field, text)
		}
	default:
		return matrix.ErrInvalidRequest("content.%s of an m.room.power_levels event must be a whole number", field)
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
