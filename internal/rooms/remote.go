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
	roomID, _ := event["room_id"].(string)
	if roomID == "" {
		// Room version 12: the create event carries no identifier because its identifier is the hash
		// of it. Nothing else may arrive without one — an event in a room whose rules this server
		// cannot read is an event it must not store.
		derived, named, deriveErr := roomOfNamelessCreate(event, "")
		if deriveErr != nil {
			return "", deriveErr
		}
		if !named {
			return "", fmt.Errorf("the event carries no room_id, so its rules cannot be checked")
		}
		roomID = derived
	}
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil && notFound(err) {
		// A room this server has never heard of can still be a room it is being told
		// about: the create event is how a server learns a room exists. Nothing else may
		// introduce one — an event in an unknown room is an event in a room this server
		// cannot check the rules of.
		if created, createErr := s.adoptRemoteRoom(ctx, event, roomID); createErr != nil {
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

	// The name this event is filed under is decided here, from the event itself, rather than
	// accepted from the offer: in the room versions whose identifiers are hashes, the identifier
	// is the hash and a PDU need not — and often does not — carry one. What the sender wrote is
	// kept only as a note for the log, since a sender that labels an event wrongly is worth
	// noticing even when the event is fine.
	carried, _ := event["event_id"].(string)
	eventID, err := events.AssignEventID(rules, event)
	if err != nil {
		return "", fmt.Errorf("that event cannot be named in room version %s: %w", room.RoomVersion, err)
	}
	if carried != "" && carried != eventID {
		s.log.Warn("rooms: an offered event was labelled with an ID its own content does not make; "+
			"it is filed under the ID the room version computes",
			slog.String("room_id", roomID),
			slog.String("labelled_event_id", carried),
			slog.String("computed_event_id", eventID))
	}

	existing, err := s.events.ByID(ctx, eventID)
	if err != nil && !notFound(err) {
		// A storage failure is not the sender's fault. Treating it as "not stored here"
		// would answer a database problem with a verdict about someone else's event, and
		// that verdict would then be remembered.
		return "", fmt.Errorf("rooms: cannot look up the event being offered: %w", err)
	}
	if existing != nil {
		// The same event arriving twice is not an error and must not be stored twice.
		return eventID, nil
	}

	// The room's own exclusion of servers is checked before anything else that costs anything:
	// authorising an event from a server the room has thrown out is work spent on an event that
	// cannot be kept. It comes after the duplicate check because an event already held here was
	// taken before the exclusion existed, and refusing it now would answer a retry with a verdict
	// that contradicts this server's own history.
	if err := s.refusedByACL(ctx, event, roomID, eventID); err != nil {
		return "", err
	}

	if verify == nil {
		return "", ErrNoRemoteVerification
	}
	if err := verify(event, rules); err != nil {
		reason := fmt.Sprintf("the event failed its signature and hash checks: %v", err)
		if errors.Is(err, events.ErrKeysUnavailable) {
			// The keys to check the signature were not obtainable. Nothing has been said about
			// the event: the neighbour may have rotated its keys, this fetch may have failed, and
			// the next attempt may verify perfectly. A refusal here is a homeserver refusing a
			// room it was happy with a second earlier.
			return "", fmt.Errorf("rooms: the event cannot be checked yet: %w", err)
		}
		if err := s.refuse(ctx, roomID, eventID, reason); err != nil {
			return "", err
		}
		return "", errors.New(reason)
	}
	// The event signed itself and hashes to the name it is filed under, so any verdict this
	// server holds against that name was about different bytes. See clearStaleVerdict.
	s.clearStaleVerdict(ctx, eventID)

	// The auth events the sender cited have to be present here, both to authorise this
	// event and to authorise whatever comes after it. Fetching missing ones is the
	// event-auth chain work of T-4007.
	authIDs := events.ReferencedIDs(event, "auth_events")
	authEvents, err := s.events.ByIDs(ctx, authIDs)
	if err != nil {
		return "", fmt.Errorf("rooms: cannot read the auth events of a remote event: %w", err)
	}
	refusals, err := s.events.Rejections(ctx, authIDs)
	if err != nil {
		return "", fmt.Errorf("rooms: cannot read the refusals recorded for a remote event: %w", err)
	}
	dropStoredRefusals(refusals, authEvents)
	if len(authEvents)+len(refusals) < len(authIDs) {
		// Nothing is known about at least one of the events it relies on. That is a gap,
		// not a verdict: the gap may be filled (T-4007) and the same event may then be
		// perfectly good. It is therefore not recorded as a refusal.
		return "", fmt.Errorf("the auth events it cites are not stored here, so it cannot be authorised")
	}
	prevIDs := events.ReferencedIDs(event, "prev_events")
	if len(prevIDs) > 0 {
		prevEvents, err := s.events.ByIDs(ctx, prevIDs)
		if err != nil {
			return "", fmt.Errorf("rooms: cannot read the predecessors of a remote event: %w", err)
		}
		if len(prevEvents) != len(prevIDs) {
			// Without the parents this event hangs off, this server cannot say where in the
			// history it belongs. Storing it as an outlier would keep it from clients until the
			// gap was filled, and refusing it outright would be a verdict on an event that may be
			// perfectly good. What is missing is named so the caller can go and get it.
			held := make(map[string]bool, len(prevEvents))
			for _, entry := range prevEvents {
				held[entry.EventID] = true
			}
			missing := make([]string, 0, len(prevIDs))
			for _, id := range prevIDs {
				if !held[id] {
					missing = append(missing, id)
				}
			}
			return "", &MissingPredecessorsError{EventID: eventID, RoomID: roomID, Missing: missing}
		}
	}

	snapshot := &citedSnapshot{byID: map[string]map[string]any{},
		byKey: map[eventauth.Key]map[string]any{}, refusals: refusals}
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
	if err := eventauth.Check(rules, event, snapshot, s.authOpts(ctx, rules)); err != nil {
		reason := fmt.Sprintf("the event is not authorised by the events it cites: %v", err)
		if eventauth.IsUnjudgeable(err) {
			// One of the events this one cites for its authorisation is not here. That is a gap
			// somebody can still fill, not a judgement about this event. Recording it would refuse
			// the event forever, and then refuse every event that cites it — which is how a room
			// whose events arrived out of order turns into a room that can never be joined.
			return "", &MissingAuthEventsError{EventID: eventID, RoomID: roomID}
		}
		return "", s.refuse(ctx, roomID, eventID, reason)
	}

	// The current-state check decides whether this event is relayed to clients, not
	// whether it exists.
	current, err := s.loadState(ctx, roomID)
	if err != nil {
		return "", err
	}
	softFailed := eventauth.Check(rules, event, current, s.authOpts(ctx, rules)) != nil

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

// refuse records this server's verdict about an event and returns the error to hand back
// to the sender.
//
// Only verdicts about the event itself belong here: it is malformed for its room version,
// its ID is not the hash its content makes, its content hash does not match, its origin
// server did not sign it, or the events it cites do not authorise it. A refusal that was
// really "this server could not judge yet" — unknown room, missing predecessors, an auth
// event nobody has sent us — is not recorded, because the missing context may arrive and
// the same event may then be entirely good. Recording that kind of refusal would turn a
// temporary gap into a permanent ban on an event this server never actually judged.
//
// The verdict outlives the event because the event is not stored: the rules say an event
// citing a refused auth event is itself refused, and the sender is entitled to the same
// answer the second time it asks.
func (s *Service) refuse(ctx context.Context, roomID, eventID, reason string) error {
	if eventID != "" {
		if stored, err := s.events.ByID(ctx, eventID); err == nil && stored != nil {
			// Nothing is recorded against an event this server holds. An ID that was
			// accepted is not refused, and recording it would let a stranger's claim —
			// bytes under a good event's ID — become a verdict against that good event.
			return errors.New(reason)
		}
		s.log.Warn("rooms: this server refused an event and remembers it",
			slog.String("room_id", roomID), slog.String("event_id", eventID), slog.String("reason", reason))
		if err := s.events.RecordRejection(ctx, &persist.EventRejection{
			EventID: eventID, RoomID: roomID, Reason: reason, Refuser: s.name,
		}); err != nil {
			s.log.Warn("rooms: cannot record a refusal",
				slog.String("event_id", eventID), slog.Any("error", err))
		}
	}
	return errors.New(reason)
}

// adoptRemoteRoom records a room from a remote create event.
//
// The room version comes from the create event, because that is what the room's rules
// are: a server that assumed its own default version would authorise the room's later
// events against rules the room never chose.
func (s *Service) adoptRemoteRoom(ctx context.Context, event map[string]any, roomID string) (bool, error) {
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
	// Room version 12 arrives here with no identifier on the event and one in hand from the caller,
	// or with neither and an event that says what it hashes to; either way the event is not the place
	// to read it from, and `roomOfNamelessCreate` has already made the two agree when both exist.
	if carried, ok := event["room_id"].(string); ok && carried != "" {
		if roomID != "" && carried != roomID {
			return false, fmt.Errorf("rooms: a create event names room %q while it was offered as %q", carried, roomID)
		}
		roomID = carried
	}
	if roomID == "" {
		return false, fmt.Errorf("rooms: a create event that names no room and hashes to no room cannot introduce one")
	}
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

// dropStoredRefusals forgets the refusals that name events this server holds.
//
// A refusal is a verdict about the bytes somebody showed under an event ID. A tampered copy of a
// real event — right ID, broken signature — earns one, and this server keeps that verdict so the
// same forgery offered twice gets the same answer. What it cannot do is stand against the event
// itself: that was stored only after its reference hash and its origin server's signature were
// checked, which is the thing the forgery failed. Nothing is recorded against an event held here
// (see refuse); this is the same rule from the other side. Without it, one forged copy arriving
// before the genuine one would refuse every later event that cites the genuine event, and a room
// would be finished off by whoever lied first.
func dropStoredRefusals(refusals map[string]string, stored []*persist.Event) {
	if len(refusals) == 0 {
		return
	}
	for _, entry := range stored {
		delete(refusals, entry.EventID)
	}
}

// clearStaleVerdict forgets a refusal this server filed against an event ID that has now arrived
// properly made.
//
// A refusal is filed against a name, and a name can be attached to tampered bytes by anybody: the
// verdict is about the bytes that were shown, not about the event. So when the event itself turns
// up — signing itself, hashing to that name — the verdict has to go, or this server would refuse
// every later event in the room for citing an event it has just accepted, and the room would be
// finished off by whoever lied first.
func (s *Service) clearStaleVerdict(ctx context.Context, eventID string) {
	if eventID == "" {
		return
	}
	reasons, err := s.events.Rejections(ctx, []string{eventID})
	if err != nil || len(reasons) == 0 {
		return
	}
	if err := s.events.ForgetRejection(ctx, eventID); err != nil {
		s.log.Warn("rooms: cannot forget a refusal this server filed against an event it has accepted",
			slog.String("event_id", eventID), slog.Any("error", err))
		return
	}
	s.log.Info("rooms: an event arrived properly made, so the refusal filed against its name is dropped",
		slog.String("event_id", eventID), slog.String("previous_verdict", reasons[eventID]))
}

// MissingAuthEventsError says an event could not be judged because events it cites for its
// authorisation are not held here. It is named rather than folded into a refusal so that a caller
// can go and ask for them, and so that nothing is recorded against an event this server never
// actually judged.
type MissingAuthEventsError struct {
	EventID string
	RoomID  string
}

func (e *MissingAuthEventsError) Error() string {
	return fmt.Sprintf("rooms: %s cannot be judged: the events it cites for authorisation are not held here", e.EventID)
}

// citedSnapshot is the state an event's own auth events describe: what the sending
// server says authorised the event.
type citedSnapshot struct {
	byID  map[string]map[string]any
	byKey map[eventauth.Key]map[string]any
	// refusals holds the auth events this server was told about and refused, by ID.
	refusals map[string]string
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

// Rejected implements [eventauth.Snapshot].
//
// Two different things can be absent from this server: an event it has never been told
// about, and an event it was told about and refused. The caller has already stopped the
// first; the second is reported here, because an event that cites a refused auth event is
// not merely unresolvable — it is invalid, and saying so is what stops the sender offering
// the same chain again.
func (s *citedSnapshot) Rejected(id string) bool {
	_, refused := s.refusals[id]
	return refused
}

// ErrNoRemoteVerification says a caller forgot to supply the signature checker. Storing
// an unverified event from a stranger is not an option, so this is a fault in this
// server rather than a condition the sender can cause.
var ErrNoRemoteVerification = errors.New("rooms: no verification supplied for a remote event")
