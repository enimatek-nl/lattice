package service

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"time"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// An event that arrives citing a parent this server was never sent is a hole in the room,
// and the hole does not close by itself: the event can still be judged from its auth
// events, but the history between the two ends is missing, and a client reading the room
// finds a jump. This server notices that hole and asks about it, which is what
// get_missing_events is for, and what a server that never asks is eventually refused for
// by peers that do.
//
// Asking is bounded three ways, because a gap is a question and a question can be asked
// about a room forever: at most [gapFillRounds] rounds per arriving event, at most
// [gapFillEvents] events per round, and [gapFillBudget] of wall-clock time. The asking
// runs on its own clock for the reason the receipt of an event already does: the sender
// that delivered the event is waiting for a verdict, and if it gives up halfway the work
// this server started is not undone by that.

const (
	// gapFillRounds is how many times one arriving event may send this server back to the
	// peer. A room whose history is one linear chain needs one round; a peer that answers
	// partially gets a second chance, and no more.
	gapFillRounds = 3
	// gapFillEvents is the `limit` this server asks for. Synapse asks for the same number.
	gapFillEvents = 20
	// gapFillBudget bounds the asking, network and all, for one arriving event.
	gapFillBudget = 10 * time.Second
)

// fillGraphGap asks the peer that delivered an event for the parents of that event this
// server was never sent, and puts what comes back into the room - in the order the room
// was built, so the timeline of a room that was briefly apart reads as one history
// afterwards. It returns how many events it managed to attach, and a sentence when the
// gap is still there, which the caller logs rather than turns into a refusal: a gap is
// not proof that the event is wrong, and an event with a hole next to it is judged on its
// own auth events.
func (s *Service) fillGraphGap(ctx context.Context, pdu matrix.Object, version, peer, eventID string) (int, string) {
	roomID := federation.EventField(pdu, "room_id")
	if roomID == "" || eventID == "" {
		return 0, ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gapFillBudget)
	defer cancel()
	transport := s.federationTransport()
	applied := 0
	for round := 0; round < gapFillRounds; round++ {
		missing := s.missingParents(idsOfPDUList(pdu, "prev_events"))
		if len(missing) == 0 {
			return applied, ""
		}
		if round == 0 {
			log.Printf("federation: %s sent %s for %s without %d of its parents", peer, eventID, roomID, len(missing))
		}
		// The two ends of the gap, named the way the endpoint asks for them: where this
		// server's own knowledge of the room ends, and the event that cites what it was
		// never given. Without the first the peer has no place to stop walking, and a
		// server that holds nothing of a room has no gap to fill - it has a room to join.
		leaves, err := s.db.EventRepo.ForwardExtremities(roomID, 1)
		if err != nil || len(leaves) == 0 {
			return applied, "this server holds no part of the room the gap is in"
		}
		earliest, err := s.db.EventRepo.Get(leaves[0])
		if err != nil {
			return applied, fmt.Sprintf("reading where this server's knowledge of %s ends: %v", roomID, err)
		}
		answer, err := transport.Post(ctx, peer,
			"/_matrix/federation/v1/get_missing_events/"+url.PathEscape(roomID), matrix.Object{
				"earliest_events": idsToAny([]string{earliest.EventID}),
				"latest_events":   idsToAny([]string{eventID}),
				"limit":           gapFillEvents,
				// The depth of where our knowledge ends is as far back as this question
				// reaches: a peer answering a recent gap with the room's beginning is
				// publishing history the question did not ask for.
				"min_depth": earliest.Depth,
			})
		if err != nil {
			return applied, fmt.Sprintf("%s could not fill the gap: %v", peer, err)
		}
		found := pduArray(answer, "events")
		if len(found) == 0 {
			return applied, fmt.Sprintf("%s holds none of the %d events missing from %s", peer, len(missing), roomID)
		}

		// A peer filling a gap hands over events, not ids: a room version from 3 onwards
		// carries no id inside the event, so each one is hashed to find out what it is
		// called, then goes through the same receipt checks as an event that was pushed
		// here. A peer cannot launder an unsigned or mis-hashed event through an answer to
		// a question this server asked.
		ordered := make([]matrix.Object, 0, len(found))
		for _, fetched := range found {
			if roomOfPDU(fetched, version, roomID) != roomID {
				log.Printf("federation: %s answered a gap in %s with an event of %s",
					peer, roomID, federation.EventField(fetched, "room_id"))
				continue
			}
			judged, problem := s.judgeIncomingPDU(ctx, fetched, version)
			if problem != "" {
				// Ignored rather than fatal: one unusable event in an answer must not
				// discard the rest of a history the peer did send correctly.
				log.Printf("federation: %s answered a gap in %s with an event this server cannot use: %s",
					peer, roomID, problem)
				continue
			}
			id := federation.EventField(judged, "event_id")
			if id == "" {
				derived, err := eventcrypto.EventID(judged, version)
				if err != nil {
					continue
				}
				id = derived
			}
			if _, err := s.db.EventRepo.Get(id); err == nil {
				continue // already had it, which is how a gap that overlaps closes
			}
			ordered = append(ordered, judged)
		}
		if len(ordered) == 0 {
			return applied, fmt.Sprintf("%s answered the gap in %s with nothing this server can use", peer, roomID)
		}
		before := applied
		// Oldest first: a parent has to be in the room before the event that cites it, both
		// because the auth checks read the state an event was built on and because a client
		// reading the room afterwards should not see the child arrive before its parent.
		sort.Slice(ordered, func(i, j int) bool {
			di, dj := numberField(ordered[i], "depth"), numberField(ordered[j], "depth")
			if di != dj {
				return di < dj
			}
			return numberField(ordered[i], "origin_server_ts") < numberField(ordered[j], "origin_server_ts")
		})
		for _, fetched := range ordered {
			id := federation.EventField(fetched, "event_id")
			if id == "" {
				derived, err := eventcrypto.EventID(fetched, version)
				if err != nil {
					continue
				}
				id = derived
			}
			outcome, reason, err := s.applyIncomingPDU(ctx, fetched, version, peer, id)
			if err != nil {
				log.Printf("federation: the gap in %s carried %s, which this server could not process: %v", roomID, id, err)
				continue
			}
			if outcome == OutcomeRejected {
				log.Printf("federation: %s answered the gap in %s with %s, which the room refuses: %s", peer, roomID, id, reason)
				continue
			}
			applied++
			// Relayed to nobody. These events came from a server that is in the room, and
			// every other server in it can ask the same question of this one now that this
			// one holds them: passing them on unasked is how one server's gap becomes a
			// broadcast.
		}
		if applied == before {
			// The peer answered and nothing landed. Asking it the same question again would
			// get the same answer, so the gap is reported and left open rather than worked
			// through three rounds of a refusal.
			return applied, fmt.Sprintf("%s answered the gap in %s with events the room refuses", peer, roomID)
		}
	}
	// The rounds are spent and the hole may still be there. That is reported, not
	// punished: the arriving event is judged on its own auth events either way.
	return applied, fmt.Sprintf("the gap in %s is still open after %d rounds", roomID, gapFillRounds)
}

// refusedParents names the parents of an arriving event that this server has already
// looked at and refused. An event built on a refusal is refused too: letting a peer grow a
// branch upwards out of an event the room turned down would put the refused event back in
// the room by implication, one event at a time.
//
// A parent that is merely *unknown* is not on this list. Not having seen an event is a gap
// ([Service.fillGraphGap] asks about it); having decided about one and said no is a fact,
// and only the fact is held against the event above it.
func (s *Service) refusedParents(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	refused := make([]string, 0, len(ids))
	for _, id := range ids {
		receipt, err := s.db.ReceivedPDURepo.Get(id)
		if err != nil || receipt == nil {
			continue
		}
		if receipt.Outcome == OutcomeRejected {
			refused = append(refused, id)
		}
	}
	return refused
}

// missingParents names the events of this room that this server does not hold. They may
// be missing because they were never sent, or because the peer sent them and this server
// refused them - which is a fact about the room, and a reason to ask rather than to
// assume: a server that has refused one parent should find out what the rest was.
func (s *Service) missingParents(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	held, err := s.db.EventRepo.GetByIDs(ids)
	if err != nil {
		log.Printf("federation: asking which parents this server holds failed: %v", err)
		return nil
	}
	missing := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, found := held[id]; !found {
			missing = append(missing, id)
		}
	}
	return missing
}

// idsToAny lifts a list of ids into the shape a JSON body takes.
func idsToAny(ids []string) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}
