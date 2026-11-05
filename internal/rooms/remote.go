package rooms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/roomversion"
	"gorm.io/gorm"
)

// VerifyPDU checks the hashes and the origin server's signature of an event that
// arrived over federation. The caller supplies it rather than the room service doing
// the checking itself, because knowing which keys to trust for which room version is
// the federation keyring's job, not this package's.
type VerifyPDU func(event map[string]any, rules roomversion.Rules) error

// AcceptRemoteEvent stores an event that another homeserver sent us, having checked the
// things the Server-Server API requires of a receiver.
//
// The order of the checks is the specification's, and each failure has a different
// consequence:
//
//   - an event that is not a valid event in its room version, or whose ID is not the
//     hash its content makes, is dropped: storing it would fork this room's history
//     against every server that recomputes that hash;
//   - an event whose origin signature does not check is dropped: it is a claim nobody
//     made;
//   - an event its auth events do not authorise is rejected: it is not stored, and the
//     response says so, so the sender knows rather than retries forever;
//   - an event that its own branch authorises but this room's *current* state does not
//     is stored and marked soft-failed. That is the ban-evading message: it is a real
//     event that arrived on a branch that did not know about the ban, and rejecting it
//     would make every later event that cites it unresolvable. It is kept out of
//     clients' timelines and out of what this server cites.
//
// This is the receiving half of the checks; the state the event is authorised against
// is the set of auth events the origin server cited, which is what a server can check
// without having replayed the room's history to that point. Full state-at-event
// resolution needs per-event state snapshots, which is task T-3004. The decision and its
// reasoning are recorded as DEC-018 in `.agent-work/REQUIREMENTS.md`.
func (s *Service) AcceptRemoteEvent(ctx context.Context, pdu []byte, verify VerifyPDU) (string, error) {
	event, err := events.Parse(pdu)
	if err != nil {
		return "", fmt.Errorf("that event could not be read: %w", err)
	}
	eventID, _ := event["event_id"].(string)
	roomID, _ := event["room_id"].(string)
	if roomID == "" {
		return "", fmt.Errorf("the event carries no room_id, so its rules cannot be checked")
	}
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil && notFound(err) {
		// A room this server has never heard of can still be a room it is being told
		// about: the create event is how a server learns a room exists. Nothing else may
		// introduce one — an event in an unknown room is an event in a room this server
		// cannot check the rules of.
		if created, createErr := s.adoptRemoteRoom(ctx, event); createErr != nil {
			return "", createErr
		} else if !created {
			return "", fmt.Errorf("room %q is not known to this server", roomID)
		}
		room, err = s.rooms.ByRoomID(ctx, roomID)
	}
	if err != nil {
		return "", fmt.Errorf("rooms: cannot load the room for a remote event: %w", err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return "", fmt.Errorf("room %q uses room version %s, which this server cannot check events in",
			roomID, room.RoomVersion)
	}

	existing, err := s.events.ByID(ctx, eventID)
	if err == nil && existing != nil {
		// The same event arriving twice is not an error and must not be stored twice.
		return eventID, nil
	}

	if verify == nil {
		return "", ErrNoRemoteVerification
	}
	if err := verify(event, rules); err != nil {
		return "", fmt.Errorf("the event failed its signature and hash checks: %w", err)
	}

	// The auth events the sender cited have to be present here, both to authorise this
	// event and to authorise whatever comes after it. Fetching missing ones is the
	// event-auth chain work of T-4007.
	authIDs := anyStrings(event["auth_events"])
	authEvents, err := s.events.ByIDs(ctx, authIDs)
	if err != nil {
		return "", fmt.Errorf("rooms: cannot read the auth events of a remote event: %w", err)
	}
	if len(authEvents) != len(authIDs) {
		return "", fmt.Errorf("the auth events it cites are not stored here, so it cannot be authorised")
	}
	prevIDs := anyStrings(event["prev_events"])
	if len(prevIDs) > 0 {
		prevEvents, err := s.events.ByIDs(ctx, prevIDs)
		if err != nil {
			return "", fmt.Errorf("rooms: cannot read the predecessors of a remote event: %w", err)
		}
		if len(prevEvents) != len(prevIDs) {
			// Without the parents this event hangs off, this server cannot say where in
			// the history it belongs. Storing it as an outlier would hide it from
			// clients until the gap was filled; asking for the gap is T-4007.
			return "", fmt.Errorf("its predecessors are not stored here")
		}
	}

	snapshot := &citedSnapshot{byID: map[string]map[string]any{}, byKey: map[eventauth.Key]map[string]any{}}
	for _, entry := range authEvents {
		body, err := events.Parse(entry.JSON)
		if err != nil {
			return "", fmt.Errorf("rooms: cannot parse an auth event: %w", err)
		}
		snapshot.byID[entry.EventID] = body
		if entry.IsState() {
			snapshot.byKey[eventauth.Key{Type: entry.Type, StateKey: *entry.StateKey}] = body
		}
	}
	if err := eventauth.Check(rules, event, snapshot, eventauth.Opts{}); err != nil {
		return "", fmt.Errorf("the event is not authorised by the events it cites: %w", err)
	}

	// The current-state check decides whether this event is relayed to clients, not
	// whether it exists.
	current, err := s.loadState(ctx, roomID)
	if err != nil {
		return "", err
	}
	softFailed := eventauth.Check(rules, event, current, eventauth.Opts{}) != nil

	var accepted string
	var stream int64
	err = s.db.Tx(ctx, func(tx *gorm.DB) error {
		service := s.WithTx(tx)
		// The stream position is taken inside the transaction: two servers handing over
		// events at once must not be given the same position.
		position, err := service.events.NextStreamOrdering(ctx)
		if err != nil {
			return err
		}
		if err := service.persistEvent(ctx, event, position); err != nil {
			return err
		}
		stream, accepted = position, eventID
		return nil
	})
	if err != nil {
		return "", err
	}
	if softFailed {
		if err := s.events.MarkSoftFailed(ctx, accepted, true); err != nil {
			s.log.Warn("rooms: cannot mark a soft-failed event",
				slog.String("event_id", accepted), slog.Any("error", err))
		}
		s.log.Info("rooms: an event arrived that its own branch authorises but this room's current state does not",
			slog.String("room_id", roomID), slog.String("event_id", accepted),
			slog.String("sender", stringOr(event["sender"])))
		return accepted, nil
	}
	if err := s.rooms.SetLatestStream(ctx, roomID, stream); err != nil {
		s.log.Warn("rooms: cannot record the latest stream position of a room",
			slog.String("room_id", roomID), slog.Any("error", err))
	}
	s.applySideEffects(ctx, accepted)
	return accepted, nil
}

// adoptRemoteRoom records a room from a remote create event.
//
// The room version comes from the create event, because that is what the room's rules
// are: a server that assumed its own default version would authorise the room's later
// events against rules the room never chose.
func (s *Service) adoptRemoteRoom(ctx context.Context, event map[string]any) (bool, error) {
	eventType, _ := event["type"].(string)
	stateKey, hasStateKey := event["state_key"].(string)
	if eventType != eventauth.TypeCreate || !hasStateKey || stateKey != "" {
		return false, nil
	}
	content, _ := event["content"].(map[string]any)
	version, _ := content["room_version"].(string)
	if version == "" {
		// A create event with no room_version is a room version 1 room, which is what
		// the specification says a missing version means.
		version = "1"
	}
	rules, ok := roomversion.Get(version)
	if !ok {
		return false, fmt.Errorf("the room asks for room version %s, which this server cannot handle", version)
	}
	roomID, _ := event["room_id"].(string)
	if err := s.rooms.Create(ctx, &persist.Room{
		RoomID:      roomID,
		RoomVersion: rules.ID,
		Creator:     stringOr(event["sender"]),
		CreatedTS:   int64Of(event["origin_server_ts"]),
	}); err != nil {
		if !errors.Is(err, persist.ErrConflict) {
			return false, fmt.Errorf("rooms: cannot record the room a create event announced: %w", err)
		}
	}
	return true, nil
}

// citedSnapshot is the state an event's own auth events describe: what the sending
// server says authorised the event.
type citedSnapshot struct {
	byID  map[string]map[string]any
	byKey map[eventauth.Key]map[string]any
}

// State implements [eventauth.Snapshot].
func (s *citedSnapshot) State(key eventauth.Key) (map[string]any, bool) {
	event, ok := s.byKey[key]
	return event, ok
}

// Event implements [eventauth.Snapshot].
func (s *citedSnapshot) Event(id string) (map[string]any, bool) {
	event, ok := s.byID[id]
	return event, ok
}

// Rejected implements [eventauth.Snapshot]. An event this server has never seen cannot
// be a rejected one; the caller refuses an event whose auth events are missing before
// getting here.
func (s *citedSnapshot) Rejected(string) bool { return false }

// ErrNoRemoteVerification says a caller forgot to supply the signature checker. Storing
// an unverified event from a stranger is not an option, so this is a fault in this
// server rather than a condition the sender can cause.
var ErrNoRemoteVerification = errors.New("rooms: no verification supplied for a remote event")
