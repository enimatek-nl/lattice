package rooms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/roomversion"
)

// historyVisibility reads the room's m.room.history_visibility setting. A room with no
// such event is treated as `shared`, which is what the room version rules assume.
func (s *Service) historyVisibility(ctx context.Context, roomID string) (string, error) {
	entry, err := s.state.Get(ctx, roomID, "m.room.history_visibility", "")
	if err != nil {
		if notFound(err) {
			return "shared", nil
		}
		return "", err
	}
	event, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return "", err
	}
	content, _ := event["content"].(map[string]any)
	visibility, _ := content["history_visibility"].(string)
	if visibility == "" {
		return "shared", nil
	}
	return visibility, nil
}

// canonicalAliasEventType is the state event naming a room's canonical and alternative
// aliases.
const canonicalAliasEventType = "m.room.canonical_alias"

// CreateAlias points a local alias at a room.
//
// The requester must be a joined member. An alias is a way of naming a room to
// everyone, so letting a stranger attach one to a room they cannot see would let them
// advertise rooms they have no standing in.
func (s *Service) CreateAlias(ctx context.Context, roomID, alias, creator string) error {
	if err := matrixid.RoomAliasValid(alias); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room alias.", alias)
	}
	if _, domain, err := matrixid.SplitID(matrixid.SigilAlias, alias); err != nil || !matrixid.IsLocal(domain, s.name) {
		return matrixerr.Newf(matrixerr.CodeInvalidParam,
			"This server can only create aliases on its own name, not %q.", alias)
	}
	if _, err := s.requireRoom(ctx, roomID); err != nil {
		return err
	}
	membership, err := s.MembershipOf(ctx, roomID, creator)
	if err != nil {
		return err
	}
	if membership != eventauth.MembershipJoin {
		return matrixerr.New(matrixerr.CodeForbidden,
			"You must be a member of a room to give it an alias on this server.")
	}

	if existing, err := s.alias.ByAlias(ctx, alias); err == nil {
		if existing.RoomID == roomID {
			// The same mapping again is not a conflict; it is the same request.
			return nil
		}
		return matrixerr.Newf(matrixerr.CodeDuplicateAlias,
			"The alias %q already points at another room.", alias)
	} else if !notFound(err) {
		return err
	}

	err = s.db.Tx(ctx, func(tx *gorm.DB) error {
		if err := s.WithTx(tx).alias.Create(ctx, &persist.RoomAlias{
			Alias: alias, RoomID: roomID, Creator: creator,
		}); err != nil {
			if errors.Is(err, persist.ErrConflict) {
				return matrixerr.Newf(matrixerr.CodeDuplicateAlias, "The alias %q is already in use.", alias)
			}
			return fmt.Errorf("rooms: cannot store the alias: %w", err)
		}
		return nil
	})
	return err
}

// ResolveAlias turns an alias into a room ID.
func (s *Service) ResolveAlias(ctx context.Context, alias string) (string, error) {
	row, err := s.alias.ByAlias(ctx, alias)
	if err != nil {
		if notFound(err) {
			return "", matrixerr.Newf(matrixerr.CodeNotFound, "This server knows no alias %q.", alias)
		}
		return "", err
	}
	return row.RoomID, nil
}

// RoomAliases lists a room's aliases. Only someone with a current membership may look,
// because the alias list says a room exists and a stranger has no claim to know that.
func (s *Service) RoomAliases(ctx context.Context, roomID, requester string) ([]string, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipStrict); err != nil {
		return nil, err
	}
	rows, err := s.alias.ByRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Alias)
	}
	return out, nil
}

// DeleteAlias removes an alias mapping. The alias's creator may always remove the one
// they made; anyone else needs the room's kick power level, which is the level the
// Client-Server API treats as "may change this room's public-facing state".
func (s *Service) DeleteAlias(ctx context.Context, alias, requester, deviceID string) error {
	row, err := s.alias.ByAlias(ctx, alias)
	if err != nil {
		if notFound(err) {
			return matrixerr.Newf(matrixerr.CodeNotFound, "This server knows no alias %q.", alias)
		}
		return err
	}
	if row.Creator == requester {
		if err := s.deleteOne(ctx, row, requester, deviceID); err != nil {
			return err
		}
		return nil
	}
	pl, err := s.powerLevels(ctx, row.RoomID)
	if err != nil {
		return err
	}
	if pl.User(requester) < pl.Kick {
		return matrixerr.New(matrixerr.CodeForbidden,
			"Only the alias creator, or someone with the room's kick power level, can delete this alias.")
	}
	return s.deleteOne(ctx, row, requester, deviceID)
}

// deleteOne removes one alias mapping and, if the room advertised that alias as its
// canonical or alternative alias, writes a corrected m.room.canonical_alias event.
//
// A room that goes on naming an alias that no longer resolves is lying to every client
// that follows it, and this server refuses exactly that when the event is written
// (checkCanonicalAlias). Leaving the old event behind after a deletion would contradict
// our own rule. The correction needs the room's state power; if the requester does not
// have it, the alias still goes — the mapping is this server's to remove — and the
// uncorrected event is logged rather than silently rewritten by someone with power.
func (s *Service) deleteOne(ctx context.Context, row *persist.RoomAlias, requester, deviceID string) error {
	if err := s.alias.Delete(ctx, row.Alias); err != nil {
		if notFound(err) {
			return matrixerr.Newf(matrixerr.CodeNotFound, "This server knows no alias %q.", row.Alias)
		}
		return err
	}
	s.dropFromCanonical(ctx, row.RoomID, row.Alias, requester, deviceID)
	return nil
}

func (s *Service) dropFromCanonical(ctx context.Context, roomID, alias, requester, deviceID string) {
	entry, err := s.state.Get(ctx, roomID, canonicalAliasEventType, "")
	if err != nil {
		if !notFound(err) {
			s.log.Warn("cannot read the canonical alias of a room whose alias was deleted",
				slog.String("room_id", roomID), slog.String("error", err.Error()))
		}
		return
	}
	event, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		s.log.Warn("the canonical alias event of a room cannot be read",
			slog.String("room_id", roomID), slog.String("error", err.Error()))
		return
	}
	content, _ := event["content"].(map[string]any)
	if len(content) == 0 {
		return
	}

	changed := false
	next := make(map[string]any, len(content))
	for key, value := range content {
		next[key] = value
	}
	if named, _ := next["alias"].(string); named == alias {
		delete(next, "alias")
		changed = true
	}
	if listed, ok := next["alt_aliases"].([]any); ok {
		kept := make([]any, 0, len(listed))
		for _, entry := range listed {
			if named, _ := entry.(string); named == alias {
				changed = true
				continue
			}
			kept = append(kept, entry)
		}
		if changed {
			if len(kept) == 0 {
				delete(next, "alt_aliases")
			} else {
				next["alt_aliases"] = kept
			}
		}
	}
	if !changed {
		return
	}

	if _, err := s.send(ctx, SendRequest{
		RoomID:    roomID,
		Sender:    requester,
		DeviceID:  deviceID,
		EventType: canonicalAliasEventType,
		StateKey:  "",
		IsState:   true,
		Content:   next,
	}); err != nil {
		s.log.Warn("the alias was deleted but the room's canonical alias could not be corrected",
			slog.String("room_id", roomID), slog.String("alias", alias), slog.String("error", err.Error()))
	}
}

// powerLevels reads the room's current power levels, falling back to the defaults when
// the room has no m.room.power_levels event.
func (s *Service) powerLevels(ctx context.Context, roomID string) (*eventauth.PowerLevels, error) {
	room, err := s.requireRoom(ctx, roomID)
	if err != nil {
		return nil, err
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return nil, matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot evaluate.", roomID, room.RoomVersion)
	}
	row, err := s.state.Get(ctx, roomID, eventauth.TypePowerLevels, "")
	if err != nil {
		if notFound(err) {
			return eventauth.DefaultPowerLevels(), nil
		}
		return nil, err
	}
	event, err := s.eventJSON(ctx, row.EventID)
	if err != nil {
		return nil, err
	}
	content, _ := event["content"].(map[string]any)
	return eventauth.LoadPowerLevels(rules, content)
}

// checkCanonicalAlias validates the aliases named by an m.room.canonical_alias event.
//
// The room is not allowed to advertise an alias that does not resolve here: a client
// that followed it would land somewhere else and conclude the server lied. An alias
// with bad syntax is the client's mistake (M_INVALID_PARAM); a well formed alias that
// this server cannot resolve to this room is a bad value (M_BAD_ALIAS).
func (s *Service) checkCanonicalAlias(ctx context.Context, roomID string, content map[string]any) error {
	if value, present := content["alias"]; present {
		if err := s.checkAliasPointsHere(ctx, roomID, value); err != nil {
			return err
		}
	}
	if value, present := content["alt_aliases"]; present {
		list, ok := value.([]any)
		if !ok {
			return matrixerr.New(matrixerr.CodeInvalidParam, "alt_aliases must be an array of room aliases.")
		}
		for _, entry := range list {
			if err := s.checkAliasPointsHere(ctx, roomID, entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) checkAliasPointsHere(ctx context.Context, roomID string, value any) error {
	alias, ok := value.(string)
	if !ok {
		return matrixerr.New(matrixerr.CodeInvalidParam, "A room alias must be a string.")
	}
	if err := matrixid.RoomAliasValid(alias); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room alias.", alias)
	}
	row, err := s.alias.ByAlias(ctx, alias)
	if err != nil {
		if notFound(err) {
			return matrixerr.Newf(matrixerr.CodeBadAlias, "This server has no alias %q, so the room cannot claim it.", alias)
		}
		return err
	}
	if row.RoomID != roomID {
		return matrixerr.Newf(matrixerr.CodeBadAlias, "The alias %q points at a different room.", alias)
	}
	return nil
}
