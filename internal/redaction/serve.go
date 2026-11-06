package redaction

import (
	"context"
	"errors"
	"fmt"

	"encoding/json"

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
	groups persist.StateGroupRepo
}

// NewServicer wires the translation to storage. The state-group repository is what answers
// "what did this room think about this user when this event was sent", so it is only needed
// for the reader-aware paths; passing nil leaves those fields unattached rather than guessed.
func NewServicer(events persist.EventRepo, rooms persist.RoomRepo, groups persist.StateGroupRepo) *Servicer {
	return &Servicer{events: events, rooms: rooms, groups: groups}
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

// Serve decodes one stored event into the client-facing form, for a response that is not
// addressed to one particular reader — so it carries no `unsigned.membership`. See ServeAs.
func (sv *Servicer) Serve(ctx context.Context, row *persist.Event) (map[string]any, error) {
	return sv.ServeAs(ctx, row, "")
}

// ServeAs decodes one stored event as it goes to one named user. The reader matters because
// part of what a client is entitled to see is about itself: MSC4115 asks for the reader's own
// membership of the room as of the moment each event was sent, which is not a fact about the
// event and cannot be read off it. A message Alice sent before Bob joined says, to Bob, that
// he was not in the room when it was written — which is how a client rendering history
// backwards knows which part of it it was entitled to read at the time.
func (sv *Servicer) ServeAs(ctx context.Context, row *persist.Event, reader string) (map[string]any, error) {
	event, err := sv.decode(ctx, row)
	if err != nil {
		return nil, err
	}
	if withheld, err := sv.Withheld(ctx, event); err != nil {
		return nil, err
	} else if withheld {
		return nil, fmt.Errorf("%w: %s", ErrWithheld, row.EventID)
	}
	return sv.decorate(ctx, row, event, reader), nil
}

// ServeChunk decodes a chunk for a response with no particular reader.
func (sv *Servicer) ServeChunk(ctx context.Context, rows []*persist.Event) ([]map[string]any, error) {
	return sv.ServeChunkAs(ctx, rows, "")
}

// ServeChunkAs decodes a chunk, leaving out what is not yet showable. A chunk is where the
// rule matters most: a timeline that names a destruction the client cannot yet place is a
// timeline that describes a room the client cannot reconstruct.
func (sv *Servicer) ServeChunkAs(ctx context.Context, rows []*persist.Event, reader string) ([]map[string]any, error) {
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
	// decoded[i] is rows[i] — decode appends in row order and bails out rather than
	// skipping, so the two stay aligned. Pairing them any other way (by the length of the
	// output, for instance) attaches one event's `unsigned` to another event's body as soon
	// as one event is withheld.
	out := make([]map[string]any, 0, len(decoded))
	for i, event := range decoded {
		id, _ := event["event_id"].(string)
		if withheld[id] {
			continue
		}
		out = append(out, sv.decorate(ctx, rows[i], event, reader))
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
func (sv *Servicer) decorate(ctx context.Context, row *persist.Event, event map[string]any, reader string) map[string]any {
	event = showPrevState(row, event)
	event = showTransactionID(row, event)
	event = sv.showMembership(ctx, row, event, reader)
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

// showMembership attaches the reader's own membership of the room as of the moment this event
// was sent, which is MSC4115. It comes from the state snapshot the event was sent against —
// the same snapshot the room's authorisation rules judged that event by — because the room's
// state now answers a different question, and answers it wrongly in the case that matters most:
// a user reading history from after they left, or a message written before they joined.
//
// A snapshot that holds no member event for the reader is `leave`: the protocol treats a user
// with no membership event as not a member, and a client should not have to know that.
// Something else is a missing snapshot. For an event this server has no snapshot of at all —
// one received from a peer, or one written before snapshots were recorded — this server did not
// see what that room thought, so it says nothing rather than guessing `leave`.
func (sv *Servicer) showMembership(ctx context.Context, row *persist.Event, event map[string]any, reader string) map[string]any {
	if reader == "" || row.EventID == "" || sv.groups == nil {
		return event
	}
	unsigned, _ := event["unsigned"].(map[string]any)
	if unsigned != nil {
		// A value already on the event stays. `unsigned` is also where another server records
		// what it added around an event, and replacing its claim about its own room would be
		// this server speaking for a room it has no part in.
		if _, ok := unsigned["membership"]; ok {
			return event
		}
	}
	membership := ""
	memberID, err := sv.groups.StateEntryAt(ctx, row.EventID, "m.room.member", reader)
	switch {
	case err == nil:
		member, err := sv.events.ByID(ctx, memberID)
		if err == nil {
			if memberEvent, err := decodeStored(member); err == nil {
				content, _ := memberEvent["content"].(map[string]any)
				membership, _ = content["membership"].(string)
			}
		}
	case errors.Is(err, persist.ErrNotFound):
		if _, gerr := sv.groups.GroupOf(ctx, row.EventID); gerr == nil {
			membership = "leave"
		}
		// No snapshot for the event at all: leave the field off, which is the difference
		// between "nobody" and "we were not there".
	}
	if err != nil && !errors.Is(err, persist.ErrNotFound) {
		// A lookup that failed is not evidence that the reader was not a member.
		return event
	}
	// Anything else in that field would be a value this server invented; the room versions
	// allow these five, and a stored event that says otherwise is not answered for.
	switch membership {
	case "join", "invite", "leave", "ban", "knock":
	default:
		return event
	}
	if unsigned == nil {
		unsigned = map[string]any{}
	}
	unsigned["membership"] = membership
	event["unsigned"] = unsigned
	return event
}

// showPrevState attaches what a state event replaced to `unsigned`, which is where the
// specification puts the things a server adds around an event rather than things the event's
// sender wrote: what the topic was before this one, what the power levels said before the change,
// and what an invited user was invited as — the last of these is how a client knows that the
// invitation it is answering was offered as a direct message, and by whom.
//
// Only the server that wrote the event can answer this, and only from the moment it wrote it, so
// the values come from persistence rather than from anything in the event itself. They stay on
// this server's side of the wire: other homeservers receive the signed bytes, and an `unsigned`
// field added here would be a claim about our own state that they had no part in.
func showPrevState(row *persist.Event, event map[string]any) map[string]any {
	if len(row.PrevContent) == 0 && row.PrevSender == "" {
		return event
	}
	unsigned, _ := event["unsigned"].(map[string]any)
	if unsigned == nil {
		unsigned = map[string]any{}
	}
	if len(row.PrevContent) > 0 {
		var content map[string]any
		if err := json.Unmarshal(row.PrevContent, &content); err == nil {
			unsigned["prev_content"] = content
		}
	}
	if row.PrevSender != "" {
		unsigned["prev_sender"] = row.PrevSender
	}
	event["unsigned"] = unsigned
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

// showTransactionID attaches the transaction ID the sending device named this event with, which
// is how a client picks its own send out of a timeline — and, on a device with more than one
// session, how it tells which of them the request came from.
//
// The value is the client's own string, kept from the request that created the event. It is not
// part of what the signature covers and it does not travel onward: a peer homeserver receives the
// signed event, and a transaction ID chosen here says nothing about what happened there.
func showTransactionID(row *persist.Event, event map[string]any) map[string]any {
	if row.TxnID == "" {
		return event
	}
	unsigned, _ := event["unsigned"].(map[string]any)
	if unsigned == nil {
		unsigned = map[string]any{}
	}
	unsigned["transaction_id"] = row.TxnID
	event["unsigned"] = unsigned
	return event
}
