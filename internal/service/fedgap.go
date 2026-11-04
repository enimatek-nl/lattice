package service

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"time"

	"github.com/enimatek-nl/lattice/internal/authcheck"
	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/stateres"
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
	// gapStateEvents caps the state events this server fetches by id from a peer that answers
	// a state question with ids only. A room's whole state is rarely this large, and closing
	// one gap is not worth an unbounded number of requests.
	gapStateEvents = 256
)

// fillGraphGap asks the peer that delivered an event for the parents of that event this
// server was never sent, and puts what comes back into the room - in the order the room
// was built, so the timeline of a room that was briefly apart reads as one history
// afterwards. It returns how many events it managed to attach, and a sentence when the
// gap is still there, which the caller logs rather than turns into a refusal: a gap is
// not proof that the event is wrong, and an event with a hole next to it is judged on its
// own auth events.
func (s *Service) fillGraphGap(ctx context.Context, pdu matrix.Object, version, peer, eventID string) (int, string) {
	roomID := roomOfPDU(pdu, version, "")
	if roomID == "" || eventID == "" {
		return 0, ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gapFillBudget)
	defer cancel()
	filled, why, found := s.walkEventGap(ctx, pdu, version, peer, eventID, roomID)
	if !found {
		return filled, ""
	}
	// Closing the history and knowing the state are two questions. This server keeps the state
	// a room holds, not a snapshot of the state at every event, so a hole in the history also
	// means that the state which changed inside that hole may never have reached this server -
	// and the events above the hole are judged against whatever picture this server happens to
	// be holding. Asking the peer what its state was at the event that cites the hole, and
	// settling this server's own picture against it, is what turns two partial views of a room
	// into one, instead of leaving the room to whichever event was written down last.
	if settled, settleWhy := s.settleStateWithPeer(ctx, roomID, version, peer, eventID); settled > 0 {
		log.Printf("federation: %s's state at %s settled %d pieces of state in %s", peer, eventID, settled, roomID)
	} else if settleWhy != "" {
		log.Printf("federation: asking %s for its state at %s did not settle the gap in %s: %s",
			peer, eventID, roomID, settleWhy)
	}
	return filled, why
}

// walkEventGap asks a peer for the events between this server's end of the room and an
// arriving event. It returns how many of them it put into the room, a sentence when the hole
// is still there, and whether a hole was found at all.
func (s *Service) walkEventGap(ctx context.Context, pdu matrix.Object, version, peer, eventID, roomID string) (int, string, bool) {
	transport := s.federationTransport()
	applied := 0
	for round := 0; round < gapFillRounds; round++ {
		missing := s.missingParents(idsOfPDUList(pdu, "prev_events"))
		if len(missing) == 0 {
			return applied, "", applied > 0
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
			return applied, "this server holds no part of the room the gap is in", true
		}
		earliest, err := s.db.EventRepo.Get(leaves[0])
		if err != nil {
			return applied, fmt.Sprintf("reading where this server's knowledge of %s ends: %v", roomID, err), true
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
			return applied, fmt.Sprintf("%s could not fill the gap: %v", peer, err), true
		}
		found := pduArray(answer, "events")
		if len(found) == 0 {
			return applied, fmt.Sprintf("%s holds none of the %d events missing from %s", peer, len(missing), roomID), true
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
			return applied, fmt.Sprintf("%s answered the gap in %s with nothing this server can use", peer, roomID), true
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
			return applied, fmt.Sprintf("%s answered the gap in %s with events the room refuses", peer, roomID), true
		}
	}
	// The rounds are spent and the hole may still be there. That is reported, not
	// punished: the arriving event is judged on its own auth events either way.
	return applied, fmt.Sprintf("the gap in %s is still open after %d rounds", roomID, gapFillRounds), true
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

// settleStateWithPeer resolves this server's copy of a room against the state a peer holds at
// one of the room's events, and writes what the two servers' disagreement settles to.
//
// The peer is not asked to be right. Its claim is judged the same way an event pushed here is
// - format, then the signature, then the content hash - and what reaches the room is the
// outcome of the resolution algorithm the specification describes, run over both servers'
// state. That is the whole point of the exercise: a room whose history arrived in pieces has
// two partial views of itself, and the algorithm is how the two become one rather than being
// left to whichever event was written down last.
func (s *Service) settleStateWithPeer(ctx context.Context, roomID, version, peer, atEventID string) (int, string) {
	transport := s.federationTransport()
	if transport == nil || peer == "" {
		return 0, "federation is off, so the state cannot be asked for"
	}
	claimed, chain, why := s.peerStateAt(ctx, transport, peer, roomID, atEventID)
	if why != "" {
		return 0, why
	}
	// Both lists are judged before any of them is read. The state is what the resolution
	// compares, and the chain is what it compares it with: the algorithm walks the power
	// events an event was built on, so an event whose chain is missing cannot be placed on
	// that walk, and an ordering built on gaps is not the ordering the algorithm describes.
	type peerEvent struct {
		id  string
		pdu matrix.Object
	}
	judged := make(map[string]matrix.Object, len(claimed)+len(chain))
	hold := func(list []matrix.Object) []peerEvent {
		kept := make([]peerEvent, 0, len(list))
		for _, pdu := range list {
			if roomOfPDU(pdu, version, roomID) != roomID {
				continue
			}
			named, problem := s.judgeIncomingPDU(ctx, pdu, version)
			if problem != "" {
				log.Printf("federation: %s answered a state question about %s with an event this server cannot use: %s",
					peer, roomID, problem)
				continue
			}
			id := federation.EventField(named, "event_id")
			if id == "" {
				derived, err := eventcrypto.EventID(named, version)
				if err != nil {
					continue
				}
				id = derived
			}
			judged[id] = named
			kept = append(kept, peerEvent{id: id, pdu: named})
		}
		return kept
	}
	chainHeld := hold(chain)
	claimedHeld := hold(claimed)

	graph := authcheck.NewGraph(version)
	complete := map[string]bool{}
	ours, err := s.readCurrentState(roomID, graph, complete)
	if err != nil {
		return 0, fmt.Sprintf("reading this server's own state of %s: %v", roomID, err)
	}
	for _, held := range append(chainHeld, claimedHeld...) {
		graph.Add(pduEvent(withEventID(held.pdu, held.id)))
		complete[held.id] = true
	}
	theirs := make(authcheck.State, len(claimedHeld))
	for _, held := range claimedHeld {
		key, isState := held.pdu["state_key"].(string)
		if !isState {
			continue
		}
		theirs[authcheck.Key{Type: federation.EventField(held.pdu, "type"), StateKey: key}] = held.id
	}
	if len(theirs) == 0 {
		return 0, fmt.Sprintf("%s named no state of %s that this server could check", peer, roomID)
	}
	resolved, err := stateres.Resolve(graph, []authcheck.State{ours, theirs})
	if err != nil {
		return 0, fmt.Sprintf("resolving this server's state of %s against %s's: %v", roomID, peer, err)
	}
	stored := 0
	for key, id := range resolved {
		if ours[key] == id {
			continue // this server already holds what the two servers settled on
		}
		if held := ours[key]; held != "" {
			// The two views disagree about a key this server already has an answer for, and the
			// resolution names the peer's event. A snapshot a peer hands over as the state at an
			// event is evidence about the hole in this server's history; it is not a verdict on a
			// piece of state this server resolved from its own graph, and taking it would let a
			// server that has simply not been told about something undo the decision. The
			// disagreement is logged rather than buried: it is a room holding two answers, and
			// which one wins is a question about the graph, not about who answered a question.
			log.Printf("federation: %s and this server disagree about %s in %s: this server keeps %s and will not take %s from a snapshot",
				peer, key, roomID, held, id)
			continue
		}
		pdu, held := judged[id]
		if !held {
			// The resolution named an event this server holds no usable copy of. There is
			// nothing to write: the room keeps the event it has and says so in a log line.
			log.Printf("federation: resolving %s with %s chose %s (%s), which this server holds no copy of",
				roomID, peer, key, id)
			continue
		}
		if _, err := s.storeCitedState(pdu, id, version, roomID); err != nil {
			return stored, fmt.Sprintf("keeping the state %s and this server settled on: %v", roomID, err)
		}
		stored++
	}
	return stored, ""
}

// peerStateAt asks a peer what the state of a room was at one of its events, and answers with
// the state events and the events of the auth chain that goes with them.
//
// /state is asked first because it hands over complete events, which is what a resolution
// needs. A peer that does not serve it - the ids-only endpoint is the newer one, and some
// servers answer only that - is asked for /state_ids and then for the events behind the state.
// The events behind the auth chain are not fetched in that case: that list is a room's whole
// authority set, and a server that keeps the state a room holds rather than a snapshot per
// event is not going to fetch a room's history to decide which of two claims about one key
// stands. The resolution still runs; it just weighs less, and says so.
func (s *Service) peerStateAt(ctx context.Context, transport *federation.Transport, peer, roomID, atEventID string) (state, chain []matrix.Object, why string) {
	query := url.Values{"event_id": []string{atEventID}}
	body, err := transport.Get(ctx, peer, "/_matrix/federation/v1/state/"+url.PathEscape(roomID), query)
	if err == nil {
		return pduArray(body, "pdus"), pduArray(body, "auth_chain"), ""
	}
	if !isUnrecognized(err) {
		return nil, nil, fmt.Sprintf("%s could not say what the state of %s was at %s: %v", peer, roomID, atEventID, err)
	}
	body, err = transport.Get(ctx, peer, "/_matrix/federation/v1/state_ids/"+url.PathEscape(roomID), query)
	if err != nil {
		return nil, nil, fmt.Sprintf("%s could not say what the state of %s was at %s: %v", peer, roomID, atEventID, err)
	}
	ids, err := stringList(body["pdu_ids"])
	if err != nil {
		return nil, nil, fmt.Sprintf("%s answered the state of %s with ids this server cannot read: %v", peer, roomID, err)
	}
	if len(ids) > gapStateEvents {
		log.Printf("federation: %s names %d events as the state of %s; this server asks for the first %d",
			peer, len(ids), roomID, gapStateEvents)
		ids = ids[:gapStateEvents]
	}
	state = make([]matrix.Object, 0, len(ids))
	for _, id := range ids {
		pdu, err := s.fetchEventFromPeer(ctx, peer, id)
		if err != nil {
			log.Printf("federation: %s named %s as state of %s but would not hand it over: %v", peer, id, roomID, err)
			continue
		}
		state = append(state, pdu)
	}
	return state, nil, ""
}
