package rooms

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
	"gorm.io/gorm"
)

// HoldsEvents says which of the given events this server already stores. A server catching up on
// a room has to know where the hole it is filling ends, or it would re-fetch a history it holds.
func (s *Service) HoldsEvents(ctx context.Context, eventIDs []string) (map[string]bool, error) {
	held := make(map[string]bool, len(eventIDs))
	if len(eventIDs) == 0 {
		return held, nil
	}
	rows, err := s.events.ByIDs(ctx, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read which events this server holds: %w", err)
	}
	for _, row := range rows {
		held[row.EventID] = true
	}
	return held, nil
}

// BundleOutcome reports what a bundle of handed-over events became here.
type BundleOutcome struct {
	// Accepted are the events now stored, including ones already held.
	Accepted []string
	// Outliers are the accepted events whose predecessors are not held. They are real
	// events, and their state counts, but this server cannot say where in the history they
	// sit, so they are kept out of client timelines until the gap is filled.
	Outliers []string
	// Refused maps an event to the reason it was not stored.
	Refused map[string]string
	// Unresolved lists the events this server could not judge: something they rely on is not
	// here. That is a gap, not a verdict, and it is what a missing-event fetch (T-4007) would
	// close.
	Unresolved []string
}

// RemoteBundle is one room's worth of events as another server handed them over: the room's
// state, what authorises it, and the event that made the asking server part of it.
type RemoteBundle struct {
	PDUs [][]byte
}

// AcceptRemoteBundle stores a bundle of events a remote server handed over in one go — the
// answer to `/send_join` — rather than one event arriving on its own.
//
// What makes this a different operation from [Service.AcceptRemoteEvent] is what a handover
// forces. A single event that arrives with its predecessors missing is a gap: this server
// refuses it and asks for the gap, which is what a transaction is for. A handed-over room is
// different in kind. It arrives as a set — the room's state, its auth chain, and the join —
// whose members cite each other, and whose join cites predecessors (the room's forward
// extremities) that are not in the set at all. Requiring every predecessor to be present
// before accepting anything would refuse the handover of every room that has ever had a
// message in it.
//
// So a bundle is taken as a snapshot:
//
//   - every event is verified on its own — valid for its room version, its ID the hash its
//     content makes, its content hash matching, its origin server having signed it. A bundle is
//     not trusted as a whole; each event in it still has to stand up by itself;
//   - an event is authorised against the auth events it cites, which are either stored here
//     already or part of the same bundle. The bundle is applied in dependency order by walking
//     it to a fixed point, so an event whose authorisation events are also in the bundle is
//     judged once those are in;
//   - an event whose predecessors are not held is stored as an outlier: its state counts and it
//     can authorise later events, but it is kept out of client timelines and out of the room's
//     forward extremities until the gap is filled. Excluding it from timelines is the honest
//     choice — this server does not know where it belongs in the history, and inventing a place
//     for it would be worse than not showing it;
//   - the room's current state is not used to soft-fail these events. In a handover the current
//     state is what the bundle is building, so checking these events against it would be
//     circular; the gate is each event's own auth chain plus its origin's signature. Recorded as
//     DEC-028.
//
// Nothing in a bundle is relayed on. It is what one server told this one about a room, not
// something this server witnessed.
func (s *Service) AcceptRemoteBundle(ctx context.Context, bundle RemoteBundle, verify VerifyPDU) (BundleOutcome, error) {
	outcome := BundleOutcome{Refused: map[string]string{}}
	if verify == nil {
		return outcome, ErrNoRemoteVerification
	}

	type pending struct {
		event   map[string]any
		eventID string
		roomID  string
	}
	pendingList := make([]pending, 0, len(bundle.PDUs))
	rooms := map[string]bool{}
	for _, pdu := range bundle.PDUs {
		event, err := events.Parse(pdu)
		if err != nil {
			// No ID can be read, so no verdict can be filed against one. It is still said.
			outcome.Refused[""] = fmt.Sprintf("an event in the bundle could not be read: %v", err)
			continue
		}
		eventID, _ := event["event_id"].(string)
		roomID, _ := event["room_id"].(string)
		if roomID == "" {
			outcome.Refused[eventID] = "an event in the bundle carries no room_id"
			continue
		}
		pendingList = append(pendingList, pending{event: event, eventID: eventID, roomID: roomID})
		rooms[roomID] = true
	}

	// A bundle is how a server learns a room exists: the create event in it is the room's own
	// account of which version of the rules it chose. Nothing else in the bundle may introduce
	// a room.
	for _, entry := range pendingList {
		if _, err := s.rooms.ByRoomID(ctx, entry.roomID); err == nil {
			continue
		} else if !notFound(err) {
			return outcome, fmt.Errorf("rooms: cannot load a room named by a handed-over bundle: %w", err)
		}
		if _, err := s.adoptRemoteRoom(ctx, entry.event); err != nil {
			outcome.Refused[entry.eventID] = err.Error()
		}
	}

	// Verify each event against the rules of the room it claims to belong to.
	versions := map[string]roomversion.Rules{}
	verified := make([]pending, 0, len(pendingList))
	for _, entry := range pendingList {
		rules, ok := versions[entry.roomID]
		if !ok {
			room, err := s.rooms.ByRoomID(ctx, entry.roomID)
			if err != nil {
				outcome.Refused[entry.eventID] = fmt.Sprintf("room %q is not known to this server", entry.roomID)
				continue
			}
			rules, ok = roomversion.Get(room.RoomVersion)
			if !ok {
				outcome.Refused[entry.eventID] = fmt.Sprintf(
					"room %q uses room version %s, which this server cannot check events in",
					entry.roomID, room.RoomVersion)
				continue
			}
			versions[entry.roomID] = rules
		}
		if err := verify(entry.event, rules); err != nil {
			reason := fmt.Sprintf("the event failed its signature and hash checks: %v", err)
			if err := s.refuse(ctx, entry.roomID, entry.eventID, reason); err != nil {
				reason = err.Error()
			}
			outcome.Refused[entry.eventID] = reason
			continue
		}
		verified = append(verified, entry)
	}

	// Walk to a fixed point. An event can be judged once the events that authorise it are
	// known, and some of those are in the bundle itself; a bundle from another server is not
	// promised to be in any order, so the pass repeats until one makes no progress at all.
	known := map[string]map[string]any{}
	// The room's latest stream position moves with what lands, so it is tracked rather than
	// re-read per event.
	latest := map[string]int64{}
	for len(verified) > 0 {
		progress := false
		remaining := make([]pending, 0, len(verified))
		for _, entry := range verified {
			rules := versions[entry.roomID]
			stored, err := s.events.ByID(ctx, entry.eventID)
			if err != nil && !notFound(err) {
				outcome.Refused[entry.eventID] = fmt.Sprintf("rooms: cannot look up an event in a bundle: %v", err)
				progress = true
				continue
			}
			if stored != nil {
				// The same event from two servers is one event, not two.
				outcome.Accepted = append(outcome.Accepted, entry.eventID)
				known[entry.eventID] = entry.event
				progress = true
				continue
			}
			accepted, outlier, unresolved, err := s.storeBundleEvent(ctx, entry.event, rules, known, latest)
			if err != nil {
				outcome.Refused[entry.eventID] = err.Error()
				progress = true
				continue
			}
			if unresolved {
				// Something that authorises it is not here yet. Another pass may bring it.
				remaining = append(remaining, entry)
				continue
			}
			outcome.Accepted = append(outcome.Accepted, accepted)
			if outlier {
				outcome.Outliers = append(outcome.Outliers, accepted)
			}
			known[entry.eventID] = entry.event
			progress = true
		}
		if !progress {
			for _, entry := range remaining {
				outcome.Unresolved = append(outcome.Unresolved, entry.eventID)
			}
			break
		}
		verified = remaining
	}

	for roomID, position := range latest {
		if position == 0 {
			continue
		}
		if err := s.rooms.SetLatestStream(ctx, roomID, position); err != nil {
			s.log.Warn("rooms: cannot record the latest stream position of a handed-over room",
				slog.String("room_id", roomID), slog.Any("error", err))
		}
	}
	if len(outcome.Outliers) > 0 {
		s.log.Info("rooms: a handed-over room is partly unlinked here; filling the gaps is the missing-event fetch of T-4007",
			slog.Int("rooms", len(rooms)),
			slog.Int("accepted", len(outcome.Accepted)),
			slog.Int("outliers", len(outcome.Outliers)))
	}
	return outcome, nil
}

// storeBundleEvent authorises one handed-over event against the events it cites — those stored
// here and those accepted from the same bundle — and stores it.
//
// The return values say what became of it: its ID once stored, whether it landed without its
// predecessors, whether it could not be judged yet, and why it was refused.
func (s *Service) storeBundleEvent(ctx context.Context, event map[string]any, rules roomversion.Rules,
	known map[string]map[string]any, latest map[string]int64) (string, bool, bool, error) {
	eventID, _ := event["event_id"].(string)
	roomID, _ := event["room_id"].(string)
	// A room's exclusion of servers holds when the room arrives in a bundle as well as when one
	// event is offered at a time: otherwise a server the room threw out could hand over the whole
	// room, or slip an event in among a bundle of someone else's.
	if err := s.refusedByACL(ctx, event, roomID, eventID); err != nil {
		return "", false, false, err
	}

	authIDs := anyStrings(event["auth_events"])
	stored, err := s.events.ByIDs(ctx, authIDs)
	if err != nil {
		return "", false, false, fmt.Errorf("rooms: cannot read the auth events of a handed-over event: %w", err)
	}
	refusals, err := s.events.Rejections(ctx, authIDs)
	if err != nil {
		return "", false, false, fmt.Errorf("rooms: cannot read the refusals recorded for a handed-over event: %w", err)
	}
	snapshot := &citedSnapshot{byID: map[string]map[string]any{},
		byKey: map[eventauth.Key]map[string]any{}, refusals: refusals}
	for _, entry := range stored {
		body, err := events.Parse(entry.JSON)
		if err != nil {
			return "", false, false, fmt.Errorf("rooms: cannot parse an auth event: %w", err)
		}
		snapshot.byID[entry.EventID] = body
		if entry.IsState() {
			snapshot.byKey[eventauth.Key{Type: entry.Type, StateKey: *entry.StateKey}] = body
		}
	}
	resolved := len(stored) + len(refusals)
	for _, id := range authIDs {
		if _, here := snapshot.byID[id]; here {
			continue
		}
		body, ok := known[id]
		if !ok {
			continue
		}
		snapshot.byID[id] = body
		if stateKey, isState := body["state_key"].(string); isState {
			snapshot.byKey[eventauth.Key{Type: stringOr(body["type"]), StateKey: stateKey}] = body
		}
		resolved++
	}
	if resolved < len(authIDs) {
		// Something it relies on for authorisation is nowhere near. That is not a verdict
		// about the event, and filing one would make the gap permanent.
		return "", false, true, nil
	}
	if err := eventauth.Check(rules, event, snapshot, eventauth.Opts{}); err != nil {
		return "", false, false, s.refuse(ctx, roomID, eventID,
			fmt.Sprintf("the event is not authorised by the events it cites: %v", err))
	}

	// An event whose predecessors arrived with the bundle is linked into the history; one
	// whose predecessors did not is an outlier.
	outlier := false
	for _, id := range anyStrings(event["prev_events"]) {
		if _, ok := known[id]; ok {
			continue
		}
		if held, err := s.events.ByID(ctx, id); err == nil && held != nil {
			continue
		}
		outlier = true
		break
	}

	var accepted string
	var stream int64
	err = s.db.Tx(ctx, func(tx *gorm.DB) error {
		service := s.WithTx(tx)
		position, err := service.events.NextStreamOrdering(ctx)
		if err != nil {
			return err
		}
		if err := service.persistEvent(ctx, event, position); err != nil {
			return err
		}
		if outlier {
			if err := service.events.MarkOutlier(ctx, eventID, true); err != nil {
				return err
			}
		}
		stream, accepted = position, eventID
		return nil
	})
	if err != nil {
		return "", false, false, err
	}
	if stream > latest[roomID] {
		latest[roomID] = stream
	}
	if outlier {
		s.log.Debug("rooms: a handed-over event is stored without its predecessors",
			slog.String("room_id", roomID), slog.String("event_id", eventID),
			slog.String("type", stringOr(event["type"])))
	}
	return accepted, outlier, false, nil
}
