package sync

// The state a room was in at the end of the batch a client has just been shown.
//
// A sync batch says "here are the last N events, and here is the room's state". Those two
// answers sit at different points in the room: the state is read as of the *start* of the
// window, so the events that changed it since are in the timeline while their new values are
// not in the state. Clients therefore have to replay the timeline over the state to know what
// the room looks like now — and that is only safe if they keep every membership event they
// were shown, which is a rule with no way to enforce it and a lazy-loading filter whose whole
// point is to drop membership events.
//
// MSC4222's answer is to give the state as of the *last* event of the batch instead, named
// `state_after`, in place of `state`. A server that answers this way can trim the membership
// it sends, because the members a client needs are the ones whose events are in front of it.
// This server already records the state each event was sent against (T-3004a), so the answer
// is a lookup rather than a replay: the snapshot attached to the newest event in the batch is
// the state at the end of the batch, including that event's own change when it is a state
// event. Rooms whose newest event has no recorded snapshot — events written before that
// feature existed, or history pulled in out of order — fall back to the current state, which
// is the same approximation the `state` section already makes and is recorded as such.
//
// The name of the response field follows the name of the flag the client sent. That is the
// usual unstable-to-stable convention rather than caution: a client that asked with
// `org.matrix.msc4222.use_state_after` is a client that reads
// `org.matrix.msc4222.state_after`, and answering it only under the stable name tells it the
// feature did not happen.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/redaction"
)

func (s *Service) stateAfter(ctx context.Context, userID, roomID string, timeline []Event, position int64, filter Filter) (*Events, error) {
	ids, err := s.snapshotOf(ctx, roomID, timeline)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return &Events{Events: []Event{}, PrevBatch: Token(position).String()}, nil
	}
	stored, err := s.events.ByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot read the state at the end of a batch in %s: %w", roomID, err)
	}
	// Under lazy loading the members worth sending are the ones this batch speaks about.
	// The specification asks for "at least" that set, so this is a choice about size rather
	// than about correctness — but it is the choice MSC4222 exists to make possible, and
	// sending every member of a large room to a client that saw one message is the cost the
	// filter was written to avoid.
	speakers := timelineSenders(timeline)
	out := make([]Event, 0, len(stored))
	for _, event := range stored {
		if !filter.AllowsState(event.Type) {
			continue
		}
		if filter.Room.State.LazyLoadMembers && event.Type == eventauth.TypeMember &&
			event.StateKey != nil && !speakers[*event.StateKey] {
			continue
		}
		body, err := s.clientEvent(ctx, userID, event)
		if err != nil {
			if errors.Is(err, redaction.ErrWithheld) {
				continue
			}
			return nil, err
		}
		out = append(out, body)
	}
	sortEvents(out)
	return &Events{Events: out, PrevBatch: Token(position).String()}, nil
}

// snapshotOf returns the events that made up the room's state at the moment the newest event
// in the batch was sent. The batch is oldest-first, so its last element is the end of it.
func (s *Service) snapshotOf(ctx context.Context, roomID string, timeline []Event) ([]string, error) {
	newest := ""
	if len(timeline) > 0 {
		newest, _ = timeline[len(timeline)-1]["event_id"].(string)
	}
	if newest != "" {
		groupID, err := s.groups.GroupOf(ctx, newest)
		switch {
		case err == nil:
			entries, err := s.groups.Entries(ctx, groupID)
			if err != nil {
				return nil, fmt.Errorf("sync: cannot read the state snapshot of %s: %w", newest, err)
			}
			ids := make([]string, 0, len(entries))
			for _, entry := range entries {
				ids = append(ids, entry.EventID)
			}
			return ids, nil
		case errors.Is(err, persist.ErrNotFound):
			// No snapshot for that event: the room's history reached this server without one.
			// The current state is the same approximation the `state` section makes.
		default:
			return nil, fmt.Errorf("sync: cannot read the state snapshot of %s: %w", newest, err)
		}
	}
	entries, err := s.state.All(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot read the state of %s: %w", roomID, err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.EventID)
	}
	return ids, nil
}

// timelineSenders names everyone who sent an event in this batch.
func timelineSenders(timeline []Event) map[string]bool {
	out := make(map[string]bool, len(timeline))
	for _, event := range timeline {
		if sender, ok := event["sender"].(string); ok && sender != "" {
			out[sender] = true
		}
	}
	return out
}

// sortEvents orders a state list by type then state key, so two syncs of the same room answer
// with the same bytes.
func sortEvents(out []Event) {
	sort.Slice(out, func(i, j int) bool {
		left, _ := out[i]["type"].(string)
		right, _ := out[j]["type"].(string)
		if left != right {
			return left < right
		}
		leftKey, _ := out[i]["state_key"].(string)
		rightKey, _ := out[j]["state_key"].(string)
		return leftKey < rightKey
	})
}
