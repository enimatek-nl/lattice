package rooms

import (
	"context"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/roomversion"
	"gorm.io/gorm"
)

// Room upgrades.
//
// An upgrade does not move a room. It ends one conversation and starts another that
// inherits the first room's settings, and it leaves a note in each place so that a client
// knows where to go: a `predecessor` in the new room's create event, and an
// `m.room.tombstone` in the old room saying that room is not to be used any further.
//
// The whole thing is one transaction, with one exception that is deliberate: the side
// effects a tombstone has — carrying the users' push rules across — run after it commits,
// because those writers go through the pool and the pool has one writer, which this
// transaction is holding. That trap is recorded twice now (the push rule service, the
// presence service); it is how this storage layer is shaped, not a one-off.

// tombstoneEventType names the event that ends a room and points at its replacement.
const tombstoneEventType = "m.room.tombstone"

// transferableState is the state an upgrade carries into the replacement room.
//
// The specification leaves the set to the implementation and recommends exactly these
// types, so this is that list rather than an invention. Two absences matter:
//
//   - Membership is not carried over. A server cannot sign a join on behalf of a user on
//     another server, and pretending otherwise would forge somebody's arrival.
//   - `m.room.create` and `m.room.tombstone` are not carried over either. They record which
//     room this is and which room ended; copying them would describe the old room in the
//     new one.
//
// `m.room.canonical_alias` is also not carried over, although the aliases themselves are
// moved: the old room's canonical alias names a name that now resolves to the new room, and
// letting the new room call itself by a name the old room chose is a decision the person
// upgrading did not make.
var transferableState = []string{
	"m.room.server_acl",
	"m.room.encryption",
	"m.room.name",
	"m.room.avatar",
	"m.room.topic",
	"m.room.guest_access",
	"m.room.history_visibility",
	"m.room.join_rules",
	"m.room.power_levels",
}

// UpgradeRequest asks for a room to be replaced by one in another room version.
type UpgradeRequest struct {
	RoomID     string
	UserID     string
	NewVersion string
}

// Upgrade replaces a room with a new one in the requested room version and returns the new
// room's ID.
//
// The specification's order is: check that the user may send `m.room.tombstone`, create the
// replacement, replicate the transferable state, move the local aliases, then send the
// tombstone. The permission check is not written out in front, because sending the tombstone
// goes through the same authorisation as any other event, and this is one transaction: if
// the user may not send it, nothing else happened either. Checking first and creating later
// would leave a room that exists but was never announced, which is worse than no room.
func (s *Service) Upgrade(ctx context.Context, req UpgradeRequest) (string, error) {
	// Only a version this server actually runs, not merely one it has rules for. A room
	// upgraded into a version whose state resolution is not written lands in a room the
	// server can store and cannot evaluate: it would accept events, fail to work out who is
	// in the room, and give every other server a different answer. Creation already refuses
	// this; an upgrade is the same doorway.
	rules, known := roomversion.Get(req.NewVersion)
	if !known || !roomversion.IsOffered(req.NewVersion) {
		return "", matrixerr.New(matrixerr.CodeUnsupportedRoomVersion,
			"This server does not support that room version.").
			WithExtra(map[string]any{"room_version": req.NewVersion})
	}
	if rules.DomainlessRoomIDs {
		// Room version 12 can be created and cannot be upgraded into, and the two answers are about
		// different things. Creating one starts from what the client asks for; upgrading one carries a
		// room across, and an upgrade into a privileged-creator room has two jobs this server does not
		// yet do. It would have to take the creators of the old room — the sender of *its* create event
		// and its `additional_creators` — and carry them into the new room's `additional_creators`,
		// which changes the bytes the new room's identifier is a hash of. And it would have to strike
		// those users out of the power levels event it copies across, because rule 10.4 of that version
		// refuses a power levels event that names a creator: without that, upgrading a version 11 room
		// produces a version 12 room whose own opening state this server refuses, which is what was
		// happening here before this refusal existed. Both belong with the upgrade path, not bolted onto
		// the end of this function, so the answer is a 400 that says what is missing rather than a room
		// that cannot be entered. T-4105.
		return "", matrixerr.New(matrixerr.CodeUnsupportedRoomVersion,
			"This server cannot upgrade a room into room version 12 yet: the room's creators have to be carried across, and the copied power levels must stop naming them.").
			WithExtra(map[string]any{"room_version": req.NewVersion})
	}
	room, err := s.rooms.ByRoomID(ctx, req.RoomID)
	if err != nil {
		if errors.Is(err, persist.ErrNotFound) {
			return "", matrixerr.Newf(matrixerr.CodeNotFound,
				"Room %q is not known to this server.", req.RoomID)
		}
		return "", fmt.Errorf("rooms: cannot read the room to upgrade: %w", err)
	}
	_, domain, err := matrixid.SplitID(matrixid.SigilRoom, req.RoomID)
	if err != nil {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room ID.", req.RoomID)
	}
	if !matrixid.IsLocal(domain, s.name) {
		return "", matrixerr.Newf(matrixerr.CodeBadState,
			"This server cannot upgrade %q because the room is not on it.", req.RoomID)
	}
	// A room that has already been upgraded has an end, and a second one would leave two
	// replacements with no way to tell which one a client should be in.
	entry, err := s.state.Get(ctx, req.RoomID, tombstoneEventType, "")
	if err != nil && !errors.Is(err, persist.ErrNotFound) {
		return "", fmt.Errorf("rooms: cannot read the room's tombstone: %w", err)
	}
	if entry != nil {
		return "", matrixerr.New(matrixerr.CodeBadState, "That room has already been upgraded.")
	}

	createContent, err := s.replacementCreateContent(ctx, req.RoomID, req.UserID, rules.ID)
	if err != nil {
		return "", err
	}
	copied, err := s.copyableState(ctx, req.RoomID)
	if err != nil {
		return "", err
	}
	newRoomID, err := s.newRoomID()
	if err != nil {
		return "", err
	}

	var created, tombstoneID string
	err = s.db.Tx(ctx, func(tx *gorm.DB) error {
		svc := s.WithTx(tx)
		id, err := svc.createRoom(ctx, rules, newRoomID, req.UserID, createContent, copied, room.Published)
		if err != nil {
			return err
		}
		// The names move with the conversation. An alias left on the old room would be a
		// name that leads to a room its own server says is finished.
		if moved, err := svc.alias.MoveRoom(ctx, req.RoomID, id); err != nil {
			return fmt.Errorf("rooms: cannot move the room's aliases: %w", err)
		} else if moved > 0 {
			s.log.Info("rooms: moved aliases across a room upgrade",
				"old_room_id", req.RoomID, "new_room_id", id, "aliases", moved)
		}
		eventID, err := svc.send(ctx, SendRequest{
			RoomID:    req.RoomID,
			Sender:    req.UserID,
			EventType: tombstoneEventType,
			IsState:   true,
			StateKey:  "",
			Content: map[string]any{
				"body":             "This room has been replaced",
				"replacement_room": id,
			},
		})
		if err != nil {
			return err
		}
		created, tombstoneID = id, eventID
		return nil
	})
	if err != nil {
		return "", err
	}

	// After the commit: the join side effects of the new room, and whatever the tombstone
	// means elsewhere.
	s.announceArrival(ctx, created, req.UserID)
	s.applySideEffects(ctx, tombstoneID)
	return created, nil
}

// replacementCreateContent builds the new room's create event: the room version, the
// predecessor, and the room type and federation flag the old room was created with.
//
// The type is copied rather than inferred. A space that upgraded into a plain room would
// stop being a space, and nothing in the request asked for that.
func (s *Service) replacementCreateContent(ctx context.Context, roomID, creator, version string) (map[string]any, error) {
	predecessor, err := s.lastKnownEvent(ctx, roomID)
	if err != nil {
		return nil, err
	}
	content := map[string]any{
		"creator":      creator,
		"room_version": version,
		"predecessor":  predecessor,
	}
	entry, err := s.state.Get(ctx, roomID, eventauth.TypeCreate, "")
	if err != nil {
		if errors.Is(err, persist.ErrNotFound) {
			return content, nil
		}
		return nil, fmt.Errorf("rooms: cannot read the create event of the room being upgraded: %w", err)
	}
	stored, err := s.events.ByID(ctx, entry.EventID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read the create event of the room being upgraded: %w", err)
	}
	body, err := events.Parse(stored.Serving())
	if err != nil {
		return nil, err
	}
	if createBody, ok := body["content"].(map[string]any); ok {
		if roomType, ok := createBody["type"].(string); ok && roomType != "" {
			content["type"] = roomType
		}
		if federate, ok := createBody["m.federate"].(bool); ok {
			content["m.federate"] = federate
		}
	}
	return content, nil
}

// lastKnownEvent names the newest event in a room, which is what a `predecessor` points at:
// both fields the specification requires, the room and the event, are filled in here rather
// than left for a client to guess.
func (s *Service) lastKnownEvent(ctx context.Context, roomID string) (map[string]any, error) {
	latest, err := s.events.LatestEvents(ctx, roomID, 1)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read the last event of %s: %w", roomID, err)
	}
	if len(latest) == 0 {
		return nil, matrixerr.Newf(matrixerr.CodeBadState,
			"Room %q has no events, so this server cannot point a replacement at it.", roomID)
	}
	return map[string]any{"room_id": roomID, "event_id": latest[0].EventID}, nil
}

// copyableState reads the state the upgrade carries over.
//
// An entry that cannot be read or parsed is skipped with a line in the log rather than
// failing the upgrade: a room whose name could not be copied is still a room, and the person
// upgrading is better served by a replacement missing one setting than by no replacement at
// all.
func (s *Service) copyableState(ctx context.Context, roomID string) ([]StateContent, error) {
	var out []StateContent
	for _, eventType := range transferableState {
		entries, err := s.state.ByType(ctx, roomID, eventType)
		if err != nil {
			return nil, fmt.Errorf("rooms: cannot read the %s state of %s: %w", eventType, roomID, err)
		}
		for _, entry := range entries {
			stored, err := s.events.ByID(ctx, entry.EventID)
			if err != nil {
				s.log.Warn("rooms: cannot read a state event to copy across an upgrade",
					"room_id", roomID, "type", eventType, "state_key", entry.StateKey, "error", err)
				continue
			}
			body, err := events.Parse(stored.Serving())
			if err != nil {
				s.log.Warn("rooms: cannot parse a state event to copy across an upgrade",
					"room_id", roomID, "type", eventType, "error", err)
				continue
			}
			content, ok := body["content"].(map[string]any)
			if !ok {
				s.log.Warn("rooms: skipping a state event with no content to copy across an upgrade",
					"room_id", roomID, "type", eventType)
				continue
			}
			out = append(out, StateContent{Type: eventType, StateKey: entry.StateKey, Content: content})
		}
	}
	return out, nil
}

// validatePredecessor checks the `predecessor` a client put in `creation_content`, and fills
// in the event the specification requires when this server can see which event that is.
//
// A predecessor this server has is checked; one it does not have is somebody else's room and
// is taken as given, because refusing it would make a room unupgradable just because its
// first half lives elsewhere.
func (s *Service) validatePredecessor(ctx context.Context, raw any) (map[string]any, error) {
	switch value := raw.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		roomID, _ := value["room_id"].(string)
		if err := matrixid.RoomIDValid(roomID); err != nil {
			return nil, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"A predecessor must name a room, and %q is not a room ID.", roomID)
		}
		eventID, _ := value["event_id"].(string)
		if eventID != "" {
			if err := matrixid.EventIDValid(eventID); err != nil {
				return nil, matrixerr.Newf(matrixerr.CodeInvalidParam,
					"A predecessor must name an event, and %q is not an event ID.", eventID)
			}
		}
		if _, err := s.rooms.ByRoomID(ctx, roomID); err == nil {
			if eventID == "" {
				last, err := s.lastKnownEvent(ctx, roomID)
				if err != nil {
					return nil, err
				}
				eventID, _ = last["event_id"].(string)
			}
		} else if !errors.Is(err, persist.ErrNotFound) {
			return nil, fmt.Errorf("rooms: cannot read the predecessor room: %w", err)
		}
		if eventID == "" {
			return nil, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"A predecessor must name an event in the room it points at, and %q gives this server no way to know one.", roomID)
		}
		return map[string]any{"room_id": roomID, "event_id": eventID}, nil
	default:
		return nil, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"A predecessor must be an object naming a room and an event in it.")
	}
}

// announceUpgrade tells the rest of the server that a tombstone ended a room, which is how a
// server that did not perform the upgrade itself still knows to carry a user's rules across.
func (s *Service) announceUpgrade(ctx context.Context, roomID string, content map[string]any) {
	if s.upgrades == nil {
		return
	}
	replacement, _ := content["replacement_room"].(string)
	if err := matrixid.RoomIDValid(replacement); err != nil {
		// A tombstone that names no usable room is a room that ended with no successor.
		// Nothing follows from it.
		return
	}
	if err := s.upgrades.RoomUpgraded(ctx, roomID, replacement); err != nil {
		s.log.Warn("rooms: cannot carry room rules across an upgrade",
			"old_room_id", roomID, "new_room_id", replacement, "error", err)
	}
}
