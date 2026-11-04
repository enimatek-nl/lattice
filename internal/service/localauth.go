package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/authcheck"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"gorm.io/gorm"
)

// judgeLocalEvent asks the room's rules about an event this server is about to sign and append.
//
// This is the half that was missing. The rules of a room live in one place - `authcheck`, the
// specification's list, read event kind by event kind - and until now that list was consulted
// only about events arriving from a peer. What this server originated was judged instead by
// hand-written guards beside each endpoint, and the two had drifted: a knock that opened a door
// is one instance (step 19), and the drift in that direction cannot be noticed from inside,
// because this server accepts its own event while every peer holding the same room refuses the
// identical one. From here the same list answers for both directions, and a rule is written down
// once.
//
// [authcheck.Rules] and not [authcheck.Check]. The check that `Check` adds reads an event's own
// choice of auth events, and exists to catch a sender picking a friendlier set of rules for
// itself; here this server chose them, with the same selection the rule would recompute, so the
// rule would only be checking this server's arithmetic. The rules themselves are the question.
//
// The judgement runs inside the append transaction, after the event has been built and identified
// and before a single row is written. The state it reads is then the state the event was built
// against, and an event the room refuses leaves nothing behind - not a stream position, not a
// transaction record, not a membership row.
func (s *Service) judgeLocalEvent(ctx context.Context, tx *gorm.DB, roomID, version string, built *pdu) error {
	graph := authcheck.NewGraph(version)
	graph.Originating = true
	state, err := stateForAuth(persist.NewStateRepo(tx), persist.NewEventRepo(tx), roomID, graph)
	if err != nil {
		return err
	}
	event := pduEvent(withEventID(built.object, built.eventID))
	graph.Add(event)
	// A restricted join asks who belongs to one of the rooms on the allow list, and that fact
	// is not in this room: no state snapshot of the restricted room can answer it. A peer
	// naming a join has to carry a claim for this server to check; when this server is the one
	// joining its own user it simply knows, and says so. The rule still reads the room's allow
	// list, so a list of rooms named here admits nothing unless the room admits people through
	// them.
	if event.Type == matrix.TypeMember && event.Membership() == matrix.MembershipJoin &&
		event.StateKey != nil && *event.StateKey == event.Sender {
		graph.Admits = s.admittedRooms(ctx, roomID, event.Sender)
	}
	if problem := authcheck.Rules(graph, state, event); problem != nil {
		return matrix.ErrForbidden("%v", problem)
	}
	return nil
}

// admittedRooms lists the rooms of a restricted room's allow list that this server holds this
// user as a joined member of. It is the fact the allow list asks about and a state snapshot of the
// restricted room cannot carry.
func (s *Service) admittedRooms(ctx context.Context, roomID, userID string) []string {
	if err := ctx.Err(); err != nil {
		return nil
	}
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeJoinRules, "")
	if err != nil {
		return nil
	}
	var rules matrix.JoinRulesContent
	if err := matrix.DecodeContent(raw, &rules); err != nil {
		return nil
	}
	if len(rules.Allow) == 0 {
		return nil
	}
	joined := make(map[string]bool, len(rules.Allow))
	if rooms, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipJoin); err == nil {
		for _, id := range rooms {
			joined[id] = true
		}
	}
	admitting := make([]string, 0, len(rules.Allow))
	for _, rule := range rules.Allow {
		if rule.RoomID != "" && joined[rule.RoomID] {
			admitting = append(admitting, rule.RoomID)
		}
	}
	return admitting
}

// stateForAuth reads a room's current state into the shape the rules read: which event holds each
// (type, state_key), and those events themselves in the graph, because the rules read the content
// of the join rules, the power levels and the create event rather than only their ids. A state
// entry whose event this server does not hold is added as a summary - enough to read a rule
// against, not enough to walk a chain.
//
// The repos are named rather than taken from the service because the judgement of a locally
// originated event has to read the state inside the transaction that appends it: the same reason
// the event's prev_events are chosen there, and the reason a state read from outside could judge
// an event against a room that no longer looks like that.
func stateForAuth(states persist.StateRepo, events persist.EventRepo, roomID string, graph *authcheck.Graph) (authcheck.State, error) {
	state := authcheck.State{}
	rows, err := states.List(roomID, nil, "")
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		state[authcheck.Key{Type: row.Type, StateKey: row.StateKey}] = row.EventID
		ids = append(ids, row.EventID)
	}
	stored, err := events.ListByIDs(ids)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	for _, row := range stored {
		graph.Add(pduEvent(withEventID(matrix.MustParseObject([]byte(row.JSON)), row.EventID)))
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
