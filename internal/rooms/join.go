package rooms

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// JoinTemplate is the template a resident server hands to a server whose user wants to join.
// It is [Service.MembershipTemplate] asked for a join, and it exists because the join endpoints
// and the outbound join path all name the same thing.
func (s *Service) JoinTemplate(ctx context.Context, roomID, userID string) (map[string]any, string, error) {
	return s.MembershipTemplate(ctx, roomID, userID, eventauth.MembershipJoin)
}

// StatePDUs returns the room's current state as the bytes each event arrived with.
//
// These are the stored bytes, not what a client is shown. A redaction is applied by each
// server to its own copy, and the hashes and signatures in an event were taken over the form
// it was signed in, so handing a peer this server's redacted view would give it an event whose
// own content hash does not match. The redaction servicer is for clients; federation is not a
// client.
func (s *Service) StatePDUs(ctx context.Context, roomID string) ([]json.RawMessage, error) {
	rows, err := s.state.All(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot load the room state to hand over: %w", err)
	}
	out := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		stored, err := s.events.ByID(ctx, row.EventID)
		if err != nil {
			if notFound(err) {
				// A state entry whose event is gone is a broken row, not a reason to
				// refuse the whole room to a peer.
				continue
			}
			return nil, fmt.Errorf("rooms: cannot read a state event to hand over: %w", err)
		}
		out = append(out, json.RawMessage(rawForm(stored)))
	}
	return out, nil
}

// AuthChainPDUs returns the events that authorise the given events, and what authorises
// those, recursively — including the seeds themselves, which is what a server receiving a
// room needs in order to check the state it is being handed rather than take it on trust.
//
// Walking `auth_events` as recorded in each event is deliberate: it is the set the origin
// servers cited, and it is what the room version's rules were applied to. The state that this
// server happened to hold when an event arrived is a different set, and can disagree.
func (s *Service) AuthChainPDUs(ctx context.Context, seeds []string) ([]json.RawMessage, error) {
	seen := map[string]bool{}
	queue := append([]string(nil), seeds...)
	out := make([]json.RawMessage, 0, len(seeds))
	// A room's auth chain is a DAG, so `seen` terminates it. The bound is for a chain that
	// is longer than any real room's: a peer's answer is untrusted input.
	for guard := 0; len(queue) > 0 && guard < maxAuthChainWalk; guard++ {
		guard++
		id := queue[0]
		queue = queue[1:]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		stored, err := s.events.ByID(ctx, id)
		if err != nil {
			if notFound(err) {
				// A gap in the chain is reported by its absence. Inventing an answer would
				// tell the peer that the chain is closed when it is not.
				continue
			}
			return nil, fmt.Errorf("rooms: cannot read an auth event to hand over: %w", err)
		}
		event, err := events.Parse(rawForm(stored))
		if err != nil {
			return nil, fmt.Errorf("rooms: a stored auth event does not parse: %w", err)
		}
		out = append(out, json.RawMessage(rawForm(stored)))
		queue = append(queue, events.ReferencedIDs(event, "auth_events")...)
	}
	return out, nil
}

// AuthChainIDs is AuthChainPDUs for a caller that only needs to name the events.
func (s *Service) AuthChainIDs(ctx context.Context, seeds []string) ([]string, error) {
	pdus, err := s.AuthChainPDUs(ctx, seeds)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pdus))
	for _, pdu := range pdus {
		event, err := events.Parse(pdu)
		if err != nil {
			continue
		}
		if id, _ := event["event_id"].(string); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// ServersInRoom lists the homeservers that have a joined user in the room. A server being
// handed a room asks for this so it knows whom to talk to afterwards, and a server that is
// asked for it should not have to reveal every member's localpart to say it.
func (s *Service) ServersInRoom(ctx context.Context, roomID string) ([]string, error) {
	members, err := s.state.CurrentMembers(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read a room's members to name its servers: %w", err)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(members))
	for _, member := range members {
		domain, err := matrixid.UserDomain(member)
		if err != nil || seen[domain] {
			continue
		}
		seen[domain] = true
		out = append(out, domain)
	}
	return out, nil
}

// rawForm is the bytes an event is answered with over federation: the stored form, which is
// the form its signatures cover. A redacted event keeps its unredacted bytes here for the same
// reason — the peer checks the content hash the origin recorded.
func rawForm(stored *persist.Event) []byte {
	return stored.JSON
}

// maxAuthChainWalk bounds one walk of an auth chain. Real rooms need a few hundred steps;
// the bound is there because the graph being walked was described to us by somebody else.
const maxAuthChainWalk = 20000
