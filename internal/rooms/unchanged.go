package rooms

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/persist"
)

// unchangedStateEvent answers the question a state write has to ask before it writes: would this
// change the room at all? A state event is a statement about a map slot — (type, state_key) — so a
// statement that repeats the one already in that slot, by the same sender, with the same content,
// adds nothing to the room. It is not written again, and the caller is told the ID of the event
// that already says it.
//
// Two of the Client-Server API's behaviours are this one rule, which is why they are decided in
// one place rather than per endpoint:
//
//   - sending the same state twice gives back the same event ID (`TestRoomCreationReportsEventsToMyself`'s
//     "Setting state twice is idempotent"), and
//   - joining a room you are already in does not put a second member event in it
//     ("Joining room twice is idempotent") — the membership endpoint is a state write like any
//     other, and the room's current member event says `join` for this user already.
//
// The comparison is of what would be written, not of what was asked for: by the time this is asked
// the content carries everything the server adds to it, so an authoriser for a restricted join, a
// display name brought from the profile, or a reason a client typed all have to match as well. That
// is what makes it safe to apply to membership as well as to a topic.
//
// Three things make an answer of "unchanged" wrong, and each is why it is checked here:
//
//   - a redacted state event is not the statement the new one would be. Writing the same content
//     back over a redaction *is* a change — the room's content comes back — so a redacted entry
//     never matches;
//   - a different sender is a different statement even with identical content, and the room's
//     `prev_sender` and the authorisation rules both care who said it;
//   - authorisation is asked before this question, not after: somebody who has lost the power to
//     set a room's topic is refused when they try, even if what they are trying to set is what the
//     topic already is.
//
// An event that is unchanged is also not given a new `transaction_id`: the event a client is shown
// keeps the ID of the send that first put it in the room. The transaction row of the repeat send
// still points at that event, so a retry of the repeat is idempotent like any other retry.
func (s *Service) unchangedStateEvent(ctx context.Context, event map[string]any) (string, error) {
	eventType, _ := event["type"].(string)
	stateKey, isState := event["state_key"].(string)
	if !isState {
		return "", nil
	}
	sender, _ := event["sender"].(string)
	roomID, _ := event["room_id"].(string)

	entry, err := s.state.Get(ctx, roomID, eventType, stateKey)
	if err != nil {
		if notFound(err) {
			return "", nil
		}
		return "", err
	}
	stored, err := s.events.ByID(ctx, entry.EventID)
	if err != nil {
		if notFound(err) {
			// A state entry whose event this server cannot read is a gap, not an answer about
			// what the room says. Writing the new event is the honest thing to do.
			return "", nil
		}
		return "", err
	}
	if stored.Redacted || stored.Sender != sender || stored.Outlier || stored.SoftFailed {
		return "", nil
	}
	// The bytes the room holds are the bytes that were signed, so the comparison is made on the
	// two content objects rather than on the events: key order and number form are what canonical
	// JSON settles, and an event ID is not a content comparison.
	current, err := events.Parse(rawForm(stored))
	if err != nil {
		return "", nil
	}
	currentContent, _ := current["content"]
	want, err := matrixjson.Canonical(event["content"])
	if err != nil {
		return "", nil
	}
	have, err := matrixjson.Canonical(currentContent)
	if err != nil {
		return "", nil
	}
	if !bytes.Equal(want, have) {
		return "", nil
	}
	// Membership is covered by what was compared: the state key is the person, the current entry
	// is by the same sender, and the membership value and anything the server adds to it — an
	// authoriser for a restricted join, a profile name, a reason — are in the content.
	return entry.EventID, nil
}

// recordRepeatSend points a repeat send's transaction at the event that already makes this
// statement, so that a retry of *this* request is idempotent like any other retry. The stored
// event's own `transaction_id` is deliberately left alone: it names the send that first put the
// statement in the room, and overwriting it would rewrite which send a client sees in the timeline.
func (s *Service) recordRepeatSend(ctx context.Context, req SendRequest, eventID string) error {
	if req.TxnID == "" || req.DeviceID == "" {
		return nil
	}
	err := s.events.TxnSave(ctx, &persist.EventTxn{
		DeviceID: req.DeviceID, RoomID: req.RoomID, TxnID: req.TxnID, EventID: eventID,
	})
	if err != nil && !errors.Is(err, persist.ErrConflict) {
		return fmt.Errorf("rooms: cannot record the transaction of a repeat send: %w", err)
	}
	return nil
}
