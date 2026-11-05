package redaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/roomversion"
)

// A stored event and the event a client is allowed to see are not the same object. The
// store keeps what arrived, because the bytes are what other servers signed; a client gets
// the redacted view once a redaction has been applied, the `redacts` field where its room
// version puts it, and a copy of the redaction under `unsigned.redacted_because` so it can
// learn why an event it had already read is now empty.
//
// That translation used to live in the rooms read paths, which left /sync answering the same
// question differently: same room, same event, redaction visible in one response and not in
// another. The servicer is one place that decides, and every path that hands an event to a
// client goes through it.

// Servicer turns stored events into the form a client is entitled to see.
type Servicer struct {
	events persist.EventRepo
	rooms  persist.RoomRepo
}

// NewServicer wires the translation to storage.
func NewServicer(events persist.EventRepo, rooms persist.RoomRepo) *Servicer {
	return &Servicer{events: events, rooms: rooms}
}

// ErrWithheld reports an event this server holds but has not yet worked out the consequence
// of. The specification's rule is a should: a redaction should not be sent to a client until
// both the redaction and the event it affects have been received and can be validated.
//
// The reason is the client, not the server's secrets. A client that is shown a redaction for
// an event it has never seen learns that something was destroyed without any way to see the
// destruction; a client that is shown it after both halves arrived sees an event go empty in
// front of it, which is what the event means.
var ErrWithheld = errors.New("redaction: the event is not yet in a form that may be shown to a client")

// Withheld reports whether an event must be kept from clients for now.
func (sv *Servicer) Withheld(ctx context.Context, event map[string]any) (bool, error) {
	eventType, _ := event["type"].(string)
	if eventType != "m.room.redaction" {
		return false, nil
	}
	id, _ := event["event_id"].(string)
	if id == "" {
		return true, nil
	}
	links, err := sv.events.RedactionLinks(ctx, []string{id})
	if err != nil {
		return false, fmt.Errorf("redaction: cannot read what %s asks for: %w", id, err)
	}
	link, ok := links[id]
	if !ok {
		// No link at all: this server never recorded the redaction as asking for anything.
		// Showing it would advertise a destruction that is not going to happen.
		return true, nil
	}
	return !link.Applied, nil
}

// withheldForChunk answers the same question for a whole chunk in one query.
func (sv *Servicer) withheldForChunk(ctx context.Context, events []map[string]any) (map[string]bool, error) {
	var ids []string
	for _, event := range events {
		if typ, _ := event["type"].(string); typ == "m.room.redaction" {
			if id, _ := event["event_id"].(string); id != "" {
				ids = append(ids, id)
			}
		}
	}
	withheld := map[string]bool{}
	if len(ids) == 0 {
		return withheld, nil
	}
	links, err := sv.events.RedactionLinks(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("redaction: cannot read what the chunk's redactions ask for: %w", err)
	}
	for _, id := range ids {
		link, ok := links[id]
		withheld[id] = !ok || !link.Applied
	}
	return withheld, nil
}

// Serve decodes one stored event into the client-facing form.
func (sv *Servicer) Serve(ctx context.Context, row *persist.Event) (map[string]any, error) {
	event, err := sv.decode(ctx, row)
	if err != nil {
		return nil, err
	}
	if withheld, err := sv.Withheld(ctx, event); err != nil {
		return nil, err
	} else if withheld {
		return nil, fmt.Errorf("%w: %s", ErrWithheld, row.EventID)
	}
	return sv.decorate(ctx, row, event), nil
}

// ServeChunk decodes a chunk, leaving out what is not yet showable. A chunk is where the
// rule matters most: a timeline that names a destruction the client cannot yet place is a
// timeline that describes a room the client cannot reconstruct.
func (sv *Servicer) ServeChunk(ctx context.Context, rows []*persist.Event) ([]map[string]any, error) {
	decoded := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		event, err := sv.decode(ctx, row)
		if err != nil {
			return nil, err
		}
		decoded = append(decoded, event)
	}
	withheld, err := sv.withheldForChunk(ctx, decoded)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(decoded))
	for _, event := range decoded {
		id, _ := event["event_id"].(string)
		if withheld[id] {
			continue
		}
		out = append(out, sv.decorate(ctx, rows[len(out)], event))
	}
	return out, nil
}

// decode gives the client the form of the bytes this server holds: the redacted view when a
// redaction has been applied, and `redacts` where the room version puts it.
func (sv *Servicer) decode(ctx context.Context, row *persist.Event) (map[string]any, error) {
	event, err := decodeStored(row)
	if err != nil {
		return nil, err
	}
	ForClient(sv.rulesFor(ctx, roomIDOf(event)), event)
	return event, nil
}

// decorate attaches `unsigned.redacted_because` for an event this server redacted, which is
// how a client learns what happened to an event it had already seen.
func (sv *Servicer) decorate(ctx context.Context, row *persist.Event, event map[string]any) map[string]any {
	if !row.Redacted || row.EventID == "" {
		return event
	}
	links, err := sv.events.RedactionsTargeting(ctx, row.EventID)
	if err != nil {
		// A read that cannot reach the links still returns the event. Withholding an event
		// because the audit trail is unreachable would lose the event, not protect anyone.
		return event
	}
	rules := sv.rulesFor(ctx, roomIDOf(event))
	for _, link := range links {
		if !link.Applied {
			continue
		}
		because, err := sv.events.ByID(ctx, link.RedactionID)
		if err != nil {
			continue
		}
		becauseEvent, err := decodeStored(because)
		if err != nil {
			continue
		}
		// The copy a client sees under `unsigned` gets the same adjustment as a redaction
		// read on its own: a client should not have to know which room version placed the
		// field it is looking for.
		ForClient(rules, becauseEvent)
		unsigned, _ := event["unsigned"].(map[string]any)
		if unsigned == nil {
			unsigned = map[string]any{}
		}
		unsigned["redacted_because"] = becauseEvent
		event["unsigned"] = unsigned
		return event
	}
	return event
}

// rulesFor reads the room version's rules, falling back to the zero value when the room is
// unknown to this server. The fallback is not a shrug: an event whose room this server has
// forgotten still has to be served or refused on some rule, and the zero Rules is the one
// that does not invent a `redacts` placement for a version nobody can name.
func (sv *Servicer) rulesFor(ctx context.Context, roomID string) roomversion.Rules {
	if roomID == "" {
		return roomversion.Rules{}
	}
	room, err := sv.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		return roomversion.Rules{}
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return roomversion.Rules{}
	}
	return rules
}

// decodeStored reads a stored event back as a protocol object. It does not go through
// `internal/events` because that package already depends on this one: the redaction rules
// are what decide an event's event ID, so the dependency runs the other way.
func decodeStored(row *persist.Event) (map[string]any, error) {
	event, err := matrixjson.DecodeMap(row.Serving())
	if err != nil {
		return nil, fmt.Errorf("redaction: stored event %s does not parse: %w", row.EventID, err)
	}
	if _, ok := event["type"]; !ok {
		return nil, fmt.Errorf("redaction: stored event %s has no type", row.EventID)
	}
	return event, nil
}

func roomIDOf(event map[string]any) string {
	roomID, _ := event["room_id"].(string)
	return roomID
}
