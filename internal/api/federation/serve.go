package federation

import (
	"context"
	"encoding/json"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/redaction"
	"github.com/enimatek/lattice/internal/roomversion"
)

// pduForPeer returns the bytes a peer is given for an event this server holds, and is the one place
// federation decides that.
//
// Two things can take an event's body away. The room can have destroyed it: a permitted redaction is
// a fact about the room, it is stored, and the peer is handed the redacted form because a redaction
// removes that content from the network's view and not just from one client's. Or the peer was never
// entitled to read it: that is a fact about the reader and the room's history visibility, worked out
// at the moment of serving, and it changes nothing on disk.
//
// Both are applied here, and in that order — an event the room redacted is handed over in its
// redacted form, and one the peer may not read is trimmed on top of whatever form that was. A peer
// that is given an event it is entitled to sees exactly the bytes the room agreed on, signatures
// included, because what a peer verifies is the stored form.
func (d *Deps) pduForPeer(ctx context.Context, stored *persist.Event, origin string, rules roomversion.Rules) ([]byte, error) {
	raw := pduBytesFor(stored)
	if d.Gaps == nil {
		// Not wired: this server can still honour the room's own redactions, but it cannot say
		// anything about who is entitled to read what. It says so loudly rather than quietly
		// answering every history request as if every peer were entitled to everything.
		d.Log.Error("federation: history visibility cannot be checked; a peer is being served" +
			" events without an entitlement decision")
		return raw, nil
	}
	visible, err := d.Gaps.VisibilityToPeer(ctx, stored, origin)
	if err != nil {
		d.Log.Warn("federation: a peer's entitlement to an event could not be determined",
			"room_id", stored.RoomID, "event_id", stored.EventID, "origin", origin, "error", err)
		visible = false
	}
	if visible {
		return raw, nil
	}
	return redactForPeer(raw, rules)
}

// rulesForRoom resolves the room version rules a PDU must be trimmed by. A room whose version this
// server cannot apply rules to is an error rather than a zero value: guessing a redaction profile
// would hand a peer content in a form no room version defines.
func (d *Deps) rulesForRoom(ctx context.Context, roomID string) (roomversion.Rules, error) {
	room, err := d.DB.Rooms.ByRoomID(ctx, roomID)
	if err != nil {
		return roomversion.Rules{}, err
	}
	rules, known := roomversion.Get(room.RoomVersion)
	if !known {
		return roomversion.Rules{}, matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"This server cannot apply that room version's rules.").
			WithExtra(map[string]any{"room_version": room.RoomVersion})
	}
	return rules, nil
}

// pduBytesFor chooses the form of a stored event that goes to a peer. A redacted event is handed over
// in its redacted form: a redaction removes content from the event as far as the rest of the Matrix
// network is concerned too, so republishing the original body under it would undo what the room
// decided. The bytes are otherwise the event as stored, because what a peer verifies is the exact
// form that was signed.
func pduBytesFor(event *persist.Event) []byte {
	if event.Redacted && len(event.RedactedJSON) > 0 {
		return event.RedactedJSON
	}
	return event.JSON
}

// redactForPeer puts an event into the form a peer that was not entitled to its content is given:
// the redaction rules of the room version applied to the event as handed over. The graph fields and
// the signatures survive, because the peer still has to be able to place and verify the event — what
// it is not given is the body. The event as stored is never changed by this: the room did not redact
// it, this reader was simply not entitled to it, and a server that rewrote stored history on the
// basis of who asked would be making the room say something it never said.
func redactForPeer(raw []byte, rules roomversion.Rules) ([]byte, error) {
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, err
	}
	return json.Marshal(redaction.Apply(rules, event))
}
