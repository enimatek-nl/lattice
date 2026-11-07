package rooms

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/redaction"
	"github.com/enimatek/lattice/internal/roomversion"
)

// A redaction is not applied by the server that sent it. Every server that sees one
// decides for itself whether it may destroy anything, which is why the authorisation
// rules accept redaction events like any other event and then say — in the pinned
// specification's own words — that "the ability to send a redaction event does not mean
// that the redaction itself should be performed".
//
// Two things follow for this server.
//
// The first is the permission test: the redaction is applied if its sender's power level
// reaches the room's redact level, or if its sender's domain matches the domain of the
// sender of the event being redacted. The second condition is what lets a homeserver clean
// up its own users' messages without anybody gaining power over another server's events.
//
// The second is that the two halves of a redaction can arrive in either order. A redaction
// whose target this server does not have is kept as a link and left unapplied; the target
// may come from a `/send` transaction, from `/backfill`, or from a join, and when it
// arrives the link is re-checked. Nothing about the redaction is guessed in the meantime,
// and an unapplied redaction destroys nothing.
//
// Power levels are read from the room's current state at the moment of application, not
// from the state the redaction was authorised against. A redaction that waits a long time
// for its partner is therefore judged by the room as it is now: a user who has since been
// demoted can no longer redact, and one who has since been promoted can. That is a
// decision, recorded as DEC-022, and the alternative — the power levels at authorisation
// time — is not stored per event yet (T-3004 would give it).

// RedactRequest is a client asking this server to redact one of a room's events.
type RedactRequest struct {
	RoomID   string
	Sender   string
	DeviceID string
	TxnID    string
	TargetID string
	Reason   string
}

// Redact sends an `m.room.redaction` event for the named target.
//
// The target field is placed by room version, not by the client: versions up to 10 carry a
// top-level `redacts`, version 11 moved it into `content`, and a redaction that names its
// target in the wrong place names nothing at all.
//
// This endpoint refuses a redaction this server would never apply, rather than accepting it
// and letting it sit in the history destroying nothing. The authorisation rules do not
// consider the redact level, so accepting such an event would leave a permanent artefact
// that every later reader has to reason about.
func (s *Service) Redact(ctx context.Context, req RedactRequest) (string, error) {
	room, err := s.rooms.ByRoomID(ctx, req.RoomID)
	if err != nil {
		if notFound(err) {
			return "", matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", req.RoomID)
		}
		return "", fmt.Errorf("rooms: cannot load the room: %w", err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return "", matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot authorise events in.",
			req.RoomID, room.RoomVersion).WithExtra(map[string]any{"room_version": room.RoomVersion})
	}
	target, err := s.events.ByID(ctx, req.TargetID)
	if err != nil {
		if notFound(err) {
			return "", matrixerr.Newf(matrixerr.CodeNotFound, "Event %q is not known to this server.", req.TargetID)
		}
		return "", fmt.Errorf("rooms: cannot read the event to redact: %w", err)
	}
	// The target has to be in this room. Answering differently for an event that exists
	// elsewhere would tell a caller which room holds somebody else's event.
	if target.RoomID != req.RoomID {
		return "", matrixerr.Newf(matrixerr.CodeNotFound, "Event %q is not known to this server.", req.TargetID)
	}
	sender, err := s.eventSenderOf(ctx, target)
	if err != nil {
		return "", err
	}
	// A redaction this server is about to create carries this server's name in its
	// identifier, which is the fact room versions 1 and 2 compare.
	allowed, err := s.mayRedact(ctx, rules, req.RoomID, req.Sender, sender, s.name, req.TargetID)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", matrixerr.New(matrixerr.CodeForbidden,
			"Your power level in this room does not allow you to redact that event.")
	}

	content := map[string]any{}
	extra := map[string]any{}
	if rules.RedactsInContent {
		content["redacts"] = req.TargetID
	} else {
		extra["redacts"] = req.TargetID
	}
	if req.Reason != "" {
		content["reason"] = req.Reason
	}
	return s.Send(ctx, SendRequest{
		RoomID:    req.RoomID,
		Sender:    req.Sender,
		DeviceID:  req.DeviceID,
		TxnID:     req.TxnID,
		EventType: "m.room.redaction",
		Content:   content,
		Extra:     extra,
	})
}

// eventSenderOf reads the sender of a stored event.
func (s *Service) eventSenderOf(ctx context.Context, row *persist.Event) (string, error) {
	event, err := events.Parse(row.JSON)
	if err != nil {
		return "", fmt.Errorf("rooms: stored event %s does not parse: %w", row.EventID, err)
	}
	sender, _ := event["sender"].(string)
	return sender, nil
}

// noteRedaction records what a redaction event asks for and applies it if it may. It runs
// inside the transaction that stores the event, so a redaction and the destruction it
// causes are either both durable or neither.
func (s *Service) noteRedaction(ctx context.Context, event map[string]any) error {
	eventType, _ := event["type"].(string)
	if eventType != "m.room.redaction" {
		return nil
	}
	redactionID, _ := event["event_id"].(string)
	roomID, _ := event["room_id"].(string)
	if redactionID == "" || roomID == "" {
		return nil
	}
	rules, err := s.roomRules(ctx, roomID)
	if err != nil {
		return err
	}
	targetID, named := redaction.TargetOf(rules, event)
	if !named {
		// A redaction that names no event destroys nothing. It stays in the history as
		// sent — it is a valid event, and rewriting other servers' events because they are
		// useless is not this server's call — but it is worth knowing that rooms are
		// carrying them.
		s.log.Debug("a redaction names no event to redact",
			slog.String("event_id", redactionID), slog.String("room_id", roomID))
		return nil
	}
	if err := s.events.RecordRedaction(ctx, &persist.Redaction{
		RedactionID: redactionID, TargetID: targetID, RoomID: roomID,
	}); err != nil {
		return fmt.Errorf("rooms: cannot record a redaction: %w", err)
	}
	return s.applyRedaction(ctx, redactionID, targetID)
}

// applyWaitingRedactions is the other direction: this event may be the partner a redaction
// has been waiting for.
func (s *Service) applyWaitingRedactions(ctx context.Context, targetID string) error {
	links, err := s.events.RedactionsTargeting(ctx, targetID)
	if err != nil {
		return fmt.Errorf("rooms: cannot read the redactions waiting for an event: %w", err)
	}
	for _, link := range links {
		if link.Applied {
			continue
		}
		if err := s.applyRedaction(ctx, link.RedactionID, targetID); err != nil {
			return err
		}
	}
	return nil
}

// applyRedaction destroys the content of an event if this server is allowed to.
func (s *Service) applyRedaction(ctx context.Context, redactionID, targetID string) error {
	redactionRow, err := s.events.ByID(ctx, redactionID)
	if err != nil {
		if notFound(err) {
			return nil
		}
		return fmt.Errorf("rooms: cannot read a redaction event: %w", err)
	}
	targetRow, err := s.events.ByID(ctx, targetID)
	if err != nil {
		if notFound(err) {
			// Still waiting for the partner. The link stays unapplied and the next time
			// either event is seen the question is asked again.
			return nil
		}
		return fmt.Errorf("rooms: cannot read the event a redaction names: %w", err)
	}
	redactionEvent, err := events.Parse(redactionRow.JSON)
	if err != nil {
		return fmt.Errorf("rooms: redaction %s does not parse: %w", redactionID, err)
	}
	targetEvent, err := events.Parse(targetRow.JSON)
	if err != nil {
		return fmt.Errorf("rooms: event %s does not parse: %w", targetID, err)
	}
	roomID, _ := redactionEvent["room_id"].(string)
	if targetRoom, _ := targetEvent["room_id"].(string); targetRoom == "" || targetRoom != roomID {
		// A redaction only reaches inside its own room. A sender that names an event from
		// elsewhere is not given a way to destroy it here.
		s.log.Warn("a redaction names an event outside its own room",
			slog.String("event_id", redactionID), slog.String("room_id", roomID))
		return nil
	}
	rules, err := s.roomRules(ctx, roomID)
	if err != nil {
		return err
	}
	sender, _ := redactionEvent["sender"].(string)
	targetSender, _ := targetEvent["sender"].(string)
	allowed, err := s.mayRedact(ctx, rules, roomID, sender, targetSender, eventDomain(redactionID), targetID)
	if err != nil {
		return err
	}
	if !allowed {
		s.log.Info("a redaction is not applied: its sender may not redact that event",
			slog.String("event_id", redactionID),
			slog.String("redacts", targetID),
			slog.String("sender", sender))
		return nil
	}
	if targetRow.Redacted {
		// Already destroyed by an earlier redaction. Redaction cannot be undone, and a
		// second one does not deepen it; marking this one applied keeps it out of the
		// waiting list.
		return s.events.MarkRedactionApplied(ctx, redactionID)
	}
	canonical, err := events.Canonical(events.Redacted(rules, targetEvent))
	if err != nil {
		return fmt.Errorf("rooms: cannot encode the redacted form of %s: %w", targetID, err)
	}
	if err := s.events.MarkRedacted(ctx, targetID, canonical); err != nil {
		return fmt.Errorf("rooms: cannot store the redacted form of %s: %w", targetID, err)
	}
	// What the event said about other events went with its content, so the index of it goes
	// too. Leaving the row behind would keep an edit or a thread reply listed under the event
	// it related to, when the client reading it is no longer allowed to see that it is one.
	if err := s.relations.DeleteByEvent(ctx, targetID); err != nil {
		return fmt.Errorf("rooms: cannot drop the relation index of %s: %w", targetID, err)
	}
	if err := s.events.MarkRedactionApplied(ctx, redactionID); err != nil {
		return fmt.Errorf("rooms: cannot record an applied redaction: %w", err)
	}
	s.log.Debug("redaction applied",
		slog.String("redaction_id", redactionID), slog.String("redacts", targetID))
	return nil
}

// mayRedact answers the permission question with the room's power levels.
func (s *Service) mayRedact(ctx context.Context, rules roomversion.Rules, roomID, redactionSender, targetSender, redactionDomain, targetEventID string) (bool, error) {
	levels := eventauth.DefaultPowerLevels()
	row, err := s.state.Get(ctx, roomID, "m.room.power_levels", "")
	if err != nil {
		if notFound(err) {
			return levels.MayRedact(rules, redactionSender, targetSender, redactionDomain, targetEventID), nil
		}
		return false, fmt.Errorf("rooms: cannot read the room's power levels: %w", err)
	}
	powerEvent, err := s.eventJSON(ctx, row.EventID)
	if err != nil {
		return false, err
	}
	content, _ := powerEvent["content"].(map[string]any)
	levels, err = eventauth.LoadPowerLevels(rules, content)
	if err != nil {
		return false, fmt.Errorf("rooms: the room's power levels do not parse: %w", err)
	}
	return levels.MayRedact(rules, redactionSender, targetSender, redactionDomain, targetEventID), nil
}

// roomRules returns the room version rules a room's events are judged by.
func (s *Service) roomRules(ctx context.Context, roomID string) (roomversion.Rules, error) {
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return roomversion.Rules{}, fmt.Errorf("rooms: %s is not a room this server knows", roomID)
		}
		return roomversion.Rules{}, fmt.Errorf("rooms: cannot read the room of an event: %w", err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return roomversion.Rules{}, fmt.Errorf("rooms: room %s uses room version %s, which this server does not handle", roomID, room.RoomVersion)
	}
	return rules, nil
}
