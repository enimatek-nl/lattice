package rooms

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/state"
)

// The room's current state is not where a state event happened to be written last. Two
// homeservers can each change the same state key without hearing about the other, and the
// specification names an algorithm — State Resolution 2 for room versions 2 to 11 — that
// makes every server reach the same answer whichever order the branches arrive in. Choosing
// by arrival does not converge: it leaves the room with a different state on each server,
// which is a room split wearing the appearance of one room.
//
// This file supplies what the algorithm was missing: the state each branch was carrying
// (T-3004a), a graph over stored events to walk auth chains in, and the step that writes the
// resolved answer back as the room's current state.

// storedGraph walks stored events and their auth events, which is how state resolution
// examines the past without loading the whole room into memory.
type storedGraph struct {
	events     persist.EventRepo
	ctx        context.Context
	log        *slog.Logger
	bodies     map[string]map[string]any
	authIDs    map[string][]string
	refusals   map[string]string
	askedAbout map[string]bool
	failure    error
}

func newStoredGraph(ctx context.Context, events persist.EventRepo, log *slog.Logger) *storedGraph {
	return &storedGraph{
		events: events, ctx: ctx, log: log,
		bodies: map[string]map[string]any{}, authIDs: map[string][]string{},
		refusals: map[string]string{}, askedAbout: map[string]bool{},
	}
}

// Event implements [state.Graph].
func (g *storedGraph) Event(id string) (map[string]any, bool) {
	if event, ok := g.bodies[id]; ok {
		return event, true
	}
	row, err := g.events.ByID(g.ctx, id)
	if err != nil {
		if !notFound(err) {
			g.failure = err
		}
		return nil, false
	}
	event, err := events.Parse(row.JSON)
	if err != nil {
		g.failure = fmt.Errorf("state resolution: stored event %s does not parse: %w", id, err)
		return nil, false
	}
	g.bodies[id] = event
	return event, true
}

// AuthEventIDs implements [state.Graph]. The auth events are read from the event itself:
// that is what the sender claims authorised it, and the algorithm's iterative auth checks
// examine the claim rather than this server's recollection of it.
func (g *storedGraph) AuthEventIDs(id string) []string {
	if ids, ok := g.authIDs[id]; ok {
		return ids
	}
	event, ok := g.Event(id)
	if !ok {
		return nil
	}
	ids := anyStrings(event["auth_events"])
	g.authIDs[id] = ids
	return ids
}

// Rejected implements [state.Graph]. A refusal this server recorded is reported so the
// resolution does not treat an event it rejected as an ordinary part of the past.
func (g *storedGraph) Rejected(id string) bool {
	if _, refused := g.refusals[id]; refused {
		return true
	}
	if g.askedAbout[id] {
		return false
	}
	g.askedAbout[id] = true
	reasons, err := g.events.Rejections(g.ctx, []string{id})
	if err != nil {
		g.log.Warn("state resolution: cannot read the refusals recorded for an event",
			slog.String("event_id", id), slog.Any("error", err))
		return false
	}
	if reason, ok := reasons[id]; ok {
		g.refusals[id] = reason
		return true
	}
	return false
}

// resolveGroups merges snapshots into one with the algorithm the room version names, and
// returns the snapshot that holds the answer. One snapshot in, one snapshot out: a room that
// is not forked pays nothing for the possibility.
func (s *Service) resolveGroups(ctx context.Context, roomID string, groupIDs []int64) (int64, error) {
	groupIDs = uniqueGroups(groupIDs)
	if len(groupIDs) == 0 {
		return s.groups.EmptyGroup(ctx, roomID)
	}
	if len(groupIDs) == 1 {
		return groupIDs[0], nil
	}
	rules, err := s.roomRules(ctx, roomID)
	if err != nil {
		return 0, err
	}
	graph := newStoredGraph(ctx, s.events, s.log)
	states := make([]state.State, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		snapshot, err := s.snapshotOfGroup(ctx, groupID)
		if err != nil {
			return 0, err
		}
		states = append(states, snapshot)
	}
	resolved, err := state.Resolve(rules, graph, states, eventauth.Opts{})
	if err != nil {
		return 0, fmt.Errorf("rooms: cannot resolve the state of %s: %w", roomID, err)
	}
	if graph.failure != nil {
		return 0, graph.failure
	}
	entries := make([]persist.StateGroupEntry, 0, len(resolved))
	for key, event := range resolved {
		eventID, _ := event["event_id"].(string)
		if eventID == "" {
			continue
		}
		entries = append(entries, persist.StateGroupEntry{
			Type: key.Type, StateKey: key.StateKey, EventID: eventID,
		})
	}
	group, err := s.groups.EnsureGroup(ctx, roomID, entries)
	if err != nil {
		return 0, fmt.Errorf("rooms: cannot record the resolved state of %s: %w", roomID, err)
	}
	return group, nil
}

// snapshotOfGroup turns a stored snapshot into the map the resolver works on.
func (s *Service) snapshotOfGroup(ctx context.Context, groupID int64) (state.State, error) {
	entries, err := s.groups.Entries(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read a state snapshot: %w", err)
	}
	snapshot := state.State{}
	for _, entry := range entries {
		event, ok := (&storedGraph{events: s.events, ctx: ctx, bodies: map[string]map[string]any{}}).Event(entry.EventID)
		if !ok {
			// A snapshot naming an event this server does not have. Leaving the key out is
			// the only honest answer: inventing one would decide the room's state from an
			// event nobody has seen.
			s.log.Warn("a state snapshot names an event that is not stored",
				slog.Int64("state_group", groupID), slog.String("event_id", entry.EventID))
			continue
		}
		snapshot[eventauth.Key{Type: entry.Type, StateKey: entry.StateKey}] = event
	}
	return snapshot, nil
}

// reconcileCurrentState writes the room's current state from the snapshots rather than from
// the order events were persisted in.
//
// An unbranching room takes the cheap path: one forward extremity, and the state entry the
// event itself changed has already been written. The moment the room has more than one
// extremity, or an event joins two branches, the answer has to be resolved over the
// snapshots and written back whole.
func (s *Service) reconcileCurrentState(ctx context.Context, row *persist.Event, prevCount int) error {
	roomID := row.RoomID
	extremities, err := s.events.ForwardExtremities(ctx, roomID)
	if err != nil {
		return fmt.Errorf("rooms: cannot read the room's forward extremities: %w", err)
	}
	if len(extremities) == 0 {
		return nil
	}
	if len(extremities) == 1 && prevCount <= 1 {
		return nil
	}
	groups := make([]int64, 0, len(extremities))
	for _, eventID := range extremities {
		groupID, err := s.groups.GroupOf(ctx, eventID)
		if err != nil {
			if notFound(err) {
				// An extremity with no recorded snapshot predates this feature; the current
				// state it left behind stays as it is rather than being rebuilt from a guess.
				s.log.Debug("a forward extremity has no recorded state snapshot",
					slog.String("room_id", roomID), slog.String("event_id", eventID))
				return nil
			}
			return fmt.Errorf("rooms: cannot read an extremity's state snapshot: %w", err)
		}
		groups = append(groups, groupID)
	}
	resolved, err := s.resolveGroups(ctx, roomID, groups)
	if err != nil {
		return err
	}
	if len(extremities) > 1 {
		s.log.Info("a room's state was resolved across a fork",
			slog.String("room_id", roomID), slog.Int("extremities", len(extremities)))
	}
	return s.applySnapshotAsCurrentState(ctx, roomID, resolved)
}

// applySnapshotAsCurrentState makes the resolved snapshot the room's current state. Keys are
// only ever replaced, never removed: Matrix state has no operation that deletes a state key,
// so a resolution that leaves a key out means the old holder stays — which is what the
// resolver decided, not something this server adds.
func (s *Service) applySnapshotAsCurrentState(ctx context.Context, roomID string, groupID int64) error {
	entries, err := s.groups.Entries(ctx, groupID)
	if err != nil {
		return fmt.Errorf("rooms: cannot read the resolved state: %w", err)
	}
	for _, entry := range entries {
		current, err := s.state.Get(ctx, roomID, entry.Type, entry.StateKey)
		if err != nil {
			if !notFound(err) {
				return fmt.Errorf("rooms: cannot read a state key while resolving: %w", err)
			}
		} else if current.EventID == entry.EventID {
			continue
		}
		if err := s.state.Set(ctx, &persist.RoomState{
			RoomID: roomID, Type: entry.Type, StateKey: entry.StateKey, EventID: entry.EventID,
		}); err != nil {
			return fmt.Errorf("rooms: cannot apply the resolved state: %w", err)
		}
	}
	return nil
}

// uniqueGroups drops repeated snapshot IDs, preserving order.
func uniqueGroups(groups []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(groups))
	for _, group := range groups {
		if group == 0 || seen[group] {
			continue
		}
		seen[group] = true
		out = append(out, group)
	}
	return out
}
