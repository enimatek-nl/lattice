package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"

	"github.com/enimatek-nl/lattice/internal/authcheck"
	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"gorm.io/gorm"
)

// The outcomes of judging an arriving event. They are recorded in the receipt row,
// and where the event is kept, beside it.
const (
	// OutcomeStored: the event passed, and the room's state was changed by it.
	OutcomeStored = "stored"
	// OutcomeDuplicate: this server already had the event.
	OutcomeDuplicate = "duplicate"
	// OutcomeSoftFailed: the event is stored and relayed, but this room's current
	// state does not support it, so it changes nothing and no client is told.
	OutcomeSoftFailed = "soft_failed"
	// OutcomeRejected: the event itself was refused. It stays in the receipt row with
	// the reason, and is nowhere else.
	OutcomeRejected = "rejected"
)

// maxAuthChainEvents bounds how many events the auth chain of one arriving event may
// cost. An auth chain is small in practice - a handful of state events and the power
// changes behind them - and an unbounded one is a way for a peer to make this server
// fetch a room.
const maxAuthChainEvents = 64

// maxAuthChainDepth bounds how far down the chain this server walks. Every level is
// another round trip to a peer, and a chain deeper than this is either a room with a
// very unusual history or somebody testing what this server spends.
const maxAuthChainDepth = 4

// applyIncomingPDU is the consuming half of the federation queue. The event has
// arrived, and its format, signature and content hash have been judged; now the
// room's own rules decide whether it may be in the room and what it does to the
// room's state when it is.
//
// The specification's remaining checks have two different outcomes, and the
// difference is the whole point of doing them this way:
//
//   - The rules read against the event's own auth chain. A rule that refuses here
//     is a rejection: the event is refused as an event, not as a claim. It stays in
//     the receipt row for the operator and nowhere else - not in the timeline, not
//     in the state, never cited as a parent, never a forward extremity.
//   - The rules read against this room's current state. A rule that refuses here is
//     a soft failure: the event is stored, it is relayed on, and it is hidden from
//     clients and from the state. Tomorrow's state may support it; today's does not.
//
// The chain is checked transitively as well. An arriving event that cites a power
// levels event nobody ever authorised would otherwise be judged by rules the sender
// made up, so every event in the chain is put through the rules against its own
// chain, and the arriving event is refused if any of them fails. That is the
// difference between "the room's rules allowed this" and "the sender says the room's
// rules allowed this".
func (s *Service) applyIncomingPDU(ctx context.Context, pdu matrix.Object, version, peer, eventID string) (string, string, error) {
	roomID := federation.EventField(pdu, "room_id")
	if eventID == "" || roomID == "" {
		return OutcomeRejected, "the event names no id or no room", nil
	}
	if _, err := s.db.EventRepo.Get(eventID); err == nil {
		return OutcomeDuplicate, "", nil
	} else if !persist.IsNotFound(err) {
		return "", "", matrix.ErrInternal(err)
	}

	// The id a room version from 3 onwards does not carry inside the event is passed in
	// rather than read back out of it: the id is the hash of the event, and writing it
	// into the event would change what it hashes to. The bytes this server stores stay
	// as they arrived; the copy the rules read names itself.
	named := withEventID(pdu, eventID)
	graph := authcheck.NewGraph(version)
	graph.Add(pduEvent(named))
	// This server's own state goes into the graph first. The events it names are the
	// ones an event in this room is expected to cite, so reading them locally keeps a
	// joined room judgeable without a single fetch; only what the chain cites and this
	// room no longer holds has to be asked for.
	// complete records which events in the graph this server holds in full: an event
	// read from its own database, or fetched and judged from a peer. A state summary
	// says what a room's current state holds and is enough to read a rule against, but
	// it carries no prev_events or auth_events, and is therefore not something a chain
	// can be checked with.
	complete := map[string]bool{eventID: true}
	current, err := s.readCurrentState(roomID, graph, complete)
	if err != nil {
		return "", "", err
	}
	chain, problem := s.gatherAuthChain(ctx, graph, named, peer, complete)
	if problem != "" {
		return OutcomeRejected, problem, nil
	}

	event := graph.Lookup(eventID)
	if event == nil {
		return OutcomeRejected, "the event is not in the graph built for it", nil
	}
	// Check one: the room's rules against the events the arriving event cites as its
	// auth events - including the rule that the set it cites is exactly the set the
	// selection algorithm would name, which is the one that catches a sender picking
	// friendlier rules for itself.
	if problem := authcheck.Check(graph, stateOfAuthIDs(graph, event), event); problem != nil {
		return OutcomeRejected, problem.Error(), nil
	}
	// Check two: the same rules against this room's current state.
	softFailure := ""
	if problem := authcheck.Rules(graph, current, event); problem != nil {
		softFailure = problem.Error()
	}
	if problem := s.authChainIsAuthorised(graph, chain, complete, eventID); problem != "" {
		return OutcomeRejected, problem, nil
	}

	if err := s.storeIncomingPDU(pdu, eventID, softFailure, false); err != nil {
		return "", "", err
	}
	if softFailure != "" {
		return OutcomeSoftFailed, softFailure, nil
	}
	return OutcomeStored, "", nil
}

// gatherAuthChain adds the events the arriving event cites, and the events those
// cite, to the graph. Events this server does not have are fetched from the peer
// that delivered the event - a rule read against a half-seen room is not the same
// rule - and each fetched event goes through the same receipt checks as one that was
// pushed here, so a peer cannot launder an unsigned event through an auth-chain
// fetch.
//
// It returns the ids it walked, which is what the transitive check reads: the room's
// current state is in the graph too, but those events are not this event's chain and
// are not checked as if they were.
func (s *Service) gatherAuthChain(ctx context.Context, graph *authcheck.Graph, pdu matrix.Object, peer string, complete map[string]bool) (map[string]bool, string) {
	chain := map[string]bool{}
	want := idsOfPDUList(pdu, "auth_events")
	for depth := 0; depth < maxAuthChainDepth && len(want) > 0; depth++ {
		next := make([]string, 0, len(want))
		for _, id := range want {
			if id == "" || chain[id] {
				continue
			}
			chain[id] = true
			next = append(next, id)
		}
		if len(chain) > maxAuthChainEvents {
			return chain, fmt.Sprintf("the auth chain of the event is larger than %d events", maxAuthChainEvents)
		}
		want = nil
		stored, err := s.db.EventRepo.ListByIDs(next)
		if err != nil {
			return chain, fmt.Sprintf("reading the events the auth chain cites: %v", err)
		}
		found := map[string]bool{}
		for _, row := range stored {
			// A stored event of a room version from 3 onwards carries no id inside itself -
			// the id is the hash of the event, and writing it in would change what it hashes
			// to. The graph keys events by id, so the id has to be put back before the copy
			// is added: an unnamed event is dropped, and the graph then falls back to the
			// state summary of the same event, which carries no prev_events and no
			// auth_events. That looks like an event that cites no auth events, and the room
			// refuses an honest ban over it.
			graph.Add(pduEvent(withEventID(matrix.MustParseObject([]byte(row.JSON)), row.EventID)))
			complete[row.EventID] = true
			found[row.EventID] = true
		}
		for _, id := range next {
			if found[id] {
				want = append(want, graph.Lookup(id).AuthIDs...)
				continue
			}
			// Not held as an event. It may still be held as a summary of the room's
			// state, which is enough to read the rules against and not enough to check
			// the chain with - so it is asked for, and the summary is only used when the
			// peer cannot produce the event itself.
			obj, fetchErr := s.fetchEventFromPeer(ctx, peer, id)
			if fetchErr != nil {
				if summary := graph.Lookup(id); summary != nil {
					want = append(want, summary.AuthIDs...)
					continue
				}
				return chain, fmt.Sprintf("the auth chain cites %s, which %s could not give: %v", id, peer, fetchErr)
			}
			judged, problem := s.judgeFetchedEvent(ctx, obj, graph.Version, peer)
			if problem != "" {
				return chain, problem
			}
			// The peer is asked for one id and has to answer with that event. A room
			// version from 3 onwards does not carry an id inside the event, so what the
			// peer sent is hashed and compared: an event that hashes to something else is
			// not the event that was asked for, whoever sent it.
			if got := federation.EventField(judged, "event_id"); got != "" && got != id {
				return chain, fmt.Sprintf("this server asked %s for %s and was given %s", peer, id, got)
			} else if got == "" {
				derived, err := eventcrypto.EventID(judged, graph.Version)
				if err != nil {
					return chain, fmt.Sprintf("the event %s sent for %s cannot be named: %v", peer, id, err)
				}
				if derived != id {
					return chain, fmt.Sprintf("%s sent an event that hashes to %s when %s was asked for", peer, derived, id)
				}
			}
			graph.Add(pduEvent(withEventID(judged, id)))
			complete[id] = true
			want = append(want, idsOfPDUList(judged, "auth_events")...)
		}
	}
	return chain, ""
}

// authChainIsAuthorised puts every event of the chain this server holds in full through
// the room's rules, read against that event's own chain. The event that arrives is left
// out: its caller checks it with the stricter rule set. Events held only as a state
// summary are left out too - there is no chain to check them against, and refusing an
// event because this server stores its own state thinly would be refusing the room.
func (s *Service) authChainIsAuthorised(graph *authcheck.Graph, chain map[string]bool, complete map[string]bool, arriving string) string {
	for id := range chain {
		if id == arriving || !complete[id] {
			continue
		}
		ev := graph.Lookup(id)
		if ev == nil {
			continue
		}
		if ev.Type != matrix.TypeCreate && len(ev.AuthIDs) == 0 {
			return fmt.Sprintf("the auth chain carries %s, which cites no auth events of its own", id)
		}
		if problem := authcheck.Rules(graph, stateOfAuthIDs(graph, ev), ev); problem != nil {
			return fmt.Sprintf("the auth chain carries %s, which the rules refuse: %v", id, problem)
		}
	}
	return ""
}

// stateOfAuthIDs builds the state snapshot an event's own auth events describe, which
// is what the rules read when the state at the event is not stored.
func stateOfAuthIDs(graph *authcheck.Graph, event *authcheck.Event) authcheck.State {
	state := authcheck.State{}
	for _, id := range event.AuthIDs {
		auth := graph.Lookup(id)
		if auth == nil {
			continue
		}
		state[authcheck.Key{Type: auth.Type, StateKey: authcheck.StateKeyOfString(auth.StateKey)}] = auth.ID
	}
	return state
}

// readCurrentState adds this room's current state to the graph and returns it as the
// snapshot the rules read. The events are added as well, because the rules read the
// content of the state and not only which ids hold it.
func (s *Service) readCurrentState(roomID string, graph *authcheck.Graph, complete map[string]bool) (authcheck.State, error) {
	state := authcheck.State{}
	rows, err := s.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		state[authcheck.Key{Type: row.Type, StateKey: row.StateKey}] = row.EventID
		ids = append(ids, row.EventID)
	}
	// The state's events are read in one go, because the rules read their content and
	// the chain walk reads what they cite. A state entry whose event this server does not
	// hold is added as a summary, which rules can be read against and a chain cannot.
	stored, err := s.db.EventRepo.ListByIDs(ids)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	for _, row := range stored {
		// Named for the same reason as in gatherAuthChain: the id is not inside the event
		// in the newer room versions, and the graph cannot hold an event it cannot name.
		graph.Add(pduEvent(withEventID(matrix.MustParseObject([]byte(row.JSON)), row.EventID)))
		complete[row.EventID] = true
	}
	for _, row := range rows {
		if graph.Lookup(row.EventID) != nil {
			continue
		}
		key := row.StateKey
		graph.Add(&authcheck.Event{
			ID:       row.EventID,
			Type:     row.Type,
			StateKey: &key,
			Sender:   row.Sender,
			Content:  matrix.MustParseObject([]byte(row.Content)),
		})
	}
	return state, nil
}

// fetchEventFromPeer asks the peer for one event by id. An auth chain is not judgeable
// without the events in it, and the peer that cited an event is the one server that is
// known to have it.
func (s *Service) fetchEventFromPeer(ctx context.Context, peer, eventID string) (matrix.Object, error) {
	transport := s.federationTransport()
	if transport == nil || peer == "" {
		return nil, fmt.Errorf("federation is off, so the event cannot be fetched")
	}
	path := "/_matrix/federation/v1/event/" + url.PathEscape(eventID)
	body, err := transport.Get(ctx, peer, path, nil)
	if err != nil {
		return nil, err
	}
	pdus, _ := body["pdus"].([]any)
	for _, entry := range pdus {
		if pdu, ok := entry.(matrix.Object); ok {
			return pdu, nil
		}
	}
	return nil, fmt.Errorf("%s answered with no event", peer)
}

// judgeFetchedEvent runs an event fetched from a peer through the same receipt checks
// as an event that was pushed at this server, so a peer cannot launder an unsigned or
// tampered event through an auth-chain fetch.
func (s *Service) judgeFetchedEvent(ctx context.Context, pdu matrix.Object, version, peer string) (matrix.Object, string) {
	judged, problem := s.judgeIncomingPDU(ctx, pdu, version)
	if problem != "" {
		return nil, fmt.Sprintf("the auth chain cites an event that %s delivered badly: %s", peer, problem)
	}
	return judged, ""
}

// withEventID returns a copy of a PDU that names itself, for the code that works with
// ids. See [Service.applyIncomingPDU] for why the copy and the stored bytes differ.
func withEventID(pdu matrix.Object, eventID string) matrix.Object {
	if eventID == "" || federation.EventField(pdu, "event_id") != "" {
		return pdu
	}
	named := matrix.Object{}
	for field, value := range pdu {
		named[field] = value
	}
	named["event_id"] = eventID
	return named
}

// storeIncomingPDU writes an accepted event to the room: the event itself, its place
// in the graph, and the state and side effects it carries when the rules allowed it.
// A soft-failed event is written and nothing else - that is what makes it a claim the
// room does not currently apply.
// notInTimeline says whether the event belongs to this room's timeline. The state that
// arrives with a join from another server does not: see [persist.Event.NotInTimeline].
func (s *Service) storeIncomingPDU(pdu matrix.Object, eventID, softFailure string, notInTimeline bool) error {
	roomID := federation.EventField(pdu, "room_id")
	encoded := matrix.Encode(objectOf(pdu, "content"))
	pos := s.nextStreamPos()
	draft := &EventDraft{
		Kind:    "room_event",
		RoomID:  roomID,
		Type:    federation.EventField(pdu, "type"),
		Sender:  federation.EventField(pdu, "sender"),
		Content: encoded,
	}
	event := &persist.Event{
		EventID:        eventID,
		RoomID:         roomID,
		StreamPos:      pos,
		Type:           draft.Type,
		Sender:         draft.Sender,
		OriginServerTS: numberField(pdu, "origin_server_ts"),
		Content:        string(encoded),
		Depth:          numberField(pdu, "depth"),
		JSON:           string(matrix.Encode(pdu)),
		PrevEvents:     encodedIDs(idsOfPDUList(pdu, "prev_events")),
		AuthEvents:     encodedIDs(idsOfPDUList(pdu, "auth_events")),
		SoftFailed:     softFailure != "",
		NotInTimeline:  notInTimeline,
	}
	if key, ok := pdu["state_key"].(string); ok {
		event.StateKey = key
		event.IsState = true
		draft.StateKey = &key
	}
	event.RelatesTo, event.RelType = relationOf(encoded)

	return s.db.Tx(func(tx *gorm.DB) error {
		events := persist.NewEventRepo(tx)
		if err := events.Create(event); err != nil {
			if persist.IsDuplicate(err) {
				// The event is already here: some other path reached it first - the state a
				// join handed over, an event relayed by a peer, the same delivery twice.
				// The copy on disk wins and the side effects are skipped, because running
				// them again would be a second opinion on state this server has already
				// resolved, and because a join that re-presents an event it already holds is
				// a join that should simply succeed.
				return nil
			}
			return fmt.Errorf("insert the received event: %w", err)
		}
		if softFailure != "" {
			// Stored, and nothing else: not in the state, not applied to the room, and
			// not a leaf of the graph this server sends from or builds on. A later event
			// may cite it, and that is the point of keeping it.
			return nil
		}
		if event.IsState {
			state := persist.NewStateRepo(tx)
			if err := state.Upsert(&persist.RoomState{
				RoomID:    roomID,
				Type:      event.Type,
				StateKey:  event.StateKey,
				EventID:   event.EventID,
				StreamPos: pos,
				Sender:    event.Sender,
				Content:   event.Content,
			}); err != nil {
				return fmt.Errorf("update the room state: %w", err)
			}
			if err := s.applySideEffects(tx, draft, event, pos); err != nil {
				log.Printf("federation: the side effects of %s could not be applied: %v", event.EventID, err)
			}
		}
		if event.Type == matrix.TypeRedaction && redactionTargetKnown(events, roomID, event) {
			if err := applyRedaction(tx, event); err != nil {
				log.Printf("federation: %s could not redact its target: %v", event.EventID, err)
			}
		}
		if err := events.AdvanceForwardExtremities(roomID, idsOfPDUList(pdu, "prev_events"), &persist.ForwardExtremity{
			EventID:        event.EventID,
			Depth:          event.Depth,
			OriginServerTS: event.OriginServerTS,
		}); err != nil {
			return fmt.Errorf("advance the graph leaves: %w", err)
		}
		return nil
	})
}

// redactionTargetKnown reports whether this server holds the event a redaction names.
// A redaction of an event this server never received is still an authorised event; it
// simply has nothing here to do.
func redactionTargetKnown(events persist.EventRepo, roomID string, redaction *persist.Event) bool {
	var content matrix.RedactionContent
	if err := matrix.DecodeContent(matrix.RawMessage(redaction.Content), &content); err != nil {
		return false
	}
	if content.Redacts == "" {
		return false
	}
	_, err := events.GetInRoom(roomID, content.Redacts)
	return err == nil
}

// pduEvent reads a PDU into the shape the authorization rules read. The id arrays come
// from the PDU because they are part of what the event is, not of what the room looks
// like now.
func pduEvent(pdu matrix.Object) *authcheck.Event {
	event := &authcheck.Event{
		ID:             federation.EventField(pdu, "event_id"),
		RoomID:         federation.EventField(pdu, "room_id"),
		Type:           federation.EventField(pdu, "type"),
		Sender:         federation.EventField(pdu, "sender"),
		OriginServerTS: numberField(pdu, "origin_server_ts"),
		Content:        objectOf(pdu, "content"),
		PrevIDs:        idsOfPDUList(pdu, "prev_events"),
		AuthIDs:        idsOfPDUList(pdu, "auth_events"),
	}
	if key, ok := pdu["state_key"].(string); ok {
		event.StateKey = &key
	}
	if redacts, ok := pdu["redacts"].(string); ok {
		event.Redacts = redacts
	}
	if redacts, ok := event.Content["redacts"].(string); ok {
		event.Redacts = redacts
	}
	return event
}

// objectOf reads an object field of a PDU, defaulting to an empty object.
func objectOf(pdu matrix.Object, field string) matrix.Object {
	if value, ok := pdu[field].(matrix.Object); ok {
		return value
	}
	return matrix.Object{}
}

// idsOfPDUList reads a list of event ids out of a PDU, in either of the two shapes the
// specification allows: a bare id, or the v1/v2 pair of id and origin.
func idsOfPDUList(pdu matrix.Object, field string) []string {
	list, _ := pdu[field].([]any)
	ids := make([]string, 0, len(list))
	for _, entry := range list {
		switch value := entry.(type) {
		case string:
			ids = append(ids, value)
		case []any:
			if len(value) > 0 {
				if id, ok := value[0].(string); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// numberField reads an integer field of a PDU.
func numberField(pdu matrix.Object, field string) int64 {
	switch value := pdu[field].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		n, _ := value.Int64()
		return n
	}
	return 0
}
