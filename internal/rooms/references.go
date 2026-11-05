package rooms

import (
	"context"
	"fmt"
	"strings"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/roomversion"
)

// writeReferences records what an event this server is building points at, in the shape the room's
// own version defines.
//
// Room versions 3 and later list event IDs. Room versions 1 and 2 list each ID together with the
// reference hash of the event being pointed at. Building one of those events with a plain list
// produces an event whose references a reader of that version cannot parse — this server's own reader
// included, the next time it walks the graph it has just written — and an event whose references read
// as empty is an event with no parents and no authorisation events, which resolves state, authorises
// and pages history all wrong without ever raising an error.
//
// known carries events built in the same pass that have not reached storage yet, which is what
// happens to the initial state of a new room: each event points at the one before it, and none of
// them is stored until the transaction that creates the room commits.
func (s *Service) writeReferences(ctx context.Context, rules roomversion.Rules, event map[string]any, known map[string]map[string]any) error {
	if rules.EventIDs == roomversion.EventIDServerAssigned {
		if _, ok := event["event_id"].(string); !ok {
			// In those versions the identifier is inside what gets hashed, so an event that has not
			// been named yet cannot be hashed yet. Naming it afterwards would sign an event that did
			// not have the name.
			return fmt.Errorf("rooms: the event being built in a room version %s room has no event ID yet", rules.ID)
		}
	}
	hashOf := func(id string) (string, error) {
		if candidate, ok := known[id]; ok {
			return events.ReferenceHashOf(rules, candidate)
		}
		row, err := s.events.ByID(ctx, id)
		if err != nil {
			return "", err
		}
		body, err := events.Parse(row.JSON)
		if err != nil {
			return "", fmt.Errorf("rooms: the stored event %s does not parse: %w", id, err)
		}
		return events.ReferenceHashOf(rules, body)
	}
	for _, field := range []string{"prev_events", "auth_events"} {
		rendered, err := events.References(rules, events.ReferencedIDs(event, field), hashOf)
		if err != nil {
			return fmt.Errorf("rooms: cannot describe the %s of the event being built: %w", field, err)
		}
		event[field] = rendered
	}
	return nil
}

// eventDomain returns the namespacing domain of an event ID of the $opaque_id:domain shape that
// room versions 1 and 2 use. A derived identifier, or a malformed one, has no domain to compare.
func eventDomain(eventID string) string {
	if len(eventID) < 2 || eventID[0] != '$' {
		return ""
	}
	_, domain, ok := strings.Cut(eventID[1:], ":")
	if !ok {
		return ""
	}
	return domain
}

// newEventID supplies the local part of an event ID for a room version in which the origin server
// chooses identifiers rather than deriving them from the event. As with a room ID, the local part is
// cryptographic randomness; two draws colliding would surface as a storage conflict rather than as
// one event quietly standing in for another.
func (s *Service) newEventID() (string, error) {
	local, err := matrixid.GenerateRandomString(nil, 18)
	if err != nil {
		return "", fmt.Errorf("rooms: cannot generate an event ID: %w", err)
	}
	if err := matrixid.EventIDValid("$" + local + ":" + s.name); err != nil {
		return "", fmt.Errorf("rooms: generated an invalid event ID: %w", err)
	}
	return local, nil
}

// nameAndReference does the two things a room version 1 or 2 room needs before an event can be
// hashed, and nothing at all elsewhere: give the event the identifier this server is choosing for it
// (which is inside what gets hashed, so it has to exist first), and render its references in the
// room's shape. Keeping them together is deliberate — doing one without the other produces an event
// that stores, signs and then fails every receiver's check.
func (s *Service) nameAndReference(event map[string]any, ctx context.Context, rules roomversion.Rules, known map[string]map[string]any) error {
	if rules.EventIDs == roomversion.EventIDServerAssigned {
		local, err := s.newEventID()
		if err != nil {
			return err
		}
		if _, err := events.MintEventID(rules, event, local, s.name); err != nil {
			return fmt.Errorf("rooms: %w", err)
		}
	}
	return s.writeReferences(ctx, rules, event, known)
}
