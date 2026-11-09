package federation

import (
	"context"
	"time"
)

// How hard this server tries to close a hole that a transaction left behind.
//
// The timeout is generous because a hole is usually closed by one round trip to the neighbour that
// just handed over the event, and the second cheapest ask (walking the citations one event at a time)
// is a handful of them. The debounce stops a chatty room asking the same question of a peer once per
// event, and the parallelism cap exists so that one neighbour relaying a burst into fifty rooms
// cannot occupy every connection this server has while its own clients wait for a sync.
const (
	holeFillTimeout     = 45 * time.Second
	holeFillDebounce    = 10 * time.Second
	holeFillParallelism = 4
)

// fillHoles closes, in the background, the holes that a transaction left underneath events this
// server has already taken and answered for.
//
// The order is the point. An event whose predecessors had not arrived is stored as an outlier, which
// means it counts in the room's state, is invisible in a client's timeline, and is a hole this server
// intends to close. Closing it *before* answering the peer would have been the confident thing and
// the wrong one twice over: it puts the peer's round trip — a server that may be slow, or may be
// gone — inside this server's answer, so a neighbour that is down delays events that were taken
// perfectly well; and it removes the discontinuity before the client is ever told about it, when the
// `limited` flag on a sync exists precisely so that a client knows its timeline is not the room's
// history. Synapse's own bug (matrix-org/synapse#16463) was a client silently believing a shorter
// timeline than the room held. So: answer, then repair.
//
// Repair is best-effort and paced. A hole that stays open is not an emergency — the event is stored,
// its successors will be judged against it, and the next transaction for the room offers the question
// again. A hole that this server tries to close four hundred times a second, on the other hand, is a
// way for one neighbour to spend another's resources.
func (d *Deps) fillHoles(ctx context.Context, origin string, holes []hole) {
	if d.Backfill == nil || origin == "" || len(holes) == 0 {
		return
	}
	d.fillMu.Lock()
	if d.fillStarted == nil {
		d.fillStarted = map[string]time.Time{}
	}
	if d.fillSlots == nil {
		d.fillSlots = make(chan struct{}, holeFillParallelism)
	}
	due := make([]hole, 0, len(holes))
	now := time.Now()
	for _, missing := range holes {
		key := missing.roomID + " " + missing.eventID
		if started, seen := d.fillStarted[key]; seen && now.Sub(started) < holeFillDebounce {
			continue
		}
		d.fillStarted[key] = now
		due = append(due, missing)
	}
	// A hole whose key is being worked on is dropped rather than queued: the fill in progress reads
	// the room's holes from the store when it runs, so a second identical ask would be the same ask.
	if len(due) == 0 {
		d.fillMu.Unlock()
		return
	}
	select {
	case d.fillSlots <- struct{}{}:
		d.fillMu.Unlock()
	default:
		d.fillMu.Unlock()
		// Said at Warn, because the consequence is a room whose history waits: the events are safe
		// here and unread by clients until something closes the gap, and "this server was already
		// filling four holes" is the only reason.
		d.Log.Warn("federation: a hole will wait; this server is already filling too many",
			"origin", origin, "holes", len(due))
		return
	}

	// The request's context ends when this handler returns, and the repair is not the peer's
	// transaction any more. What carries over is only the values in it; the deadline is this one.
	gone := context.WithoutCancel(ctx)
	go func() {
		defer func() { <-d.fillSlots }()
		ctx, cancel := context.WithTimeout(gone, holeFillTimeout)
		defer cancel()
		for _, missing := range due {
			d.fillOneHole(ctx, origin, missing)
		}
	}()
}

// fillOneHole asks one server for what lies beneath one event, and relinks what arrives.
//
// The cheapest and best ask is the one that does not need to know the names of the missing events —
// `earliest_events` is this server's own frontier, `latest_events` is the event that cited them — and
// it closes a hole of any length in one round trip. The fallback walks the citations this event did
// make, one event at a time, for the neighbour that answers `/event` but has never heard of
// `/get_missing_events`.
func (d *Deps) fillOneHole(ctx context.Context, origin string, missing hole) {
	destinations := []string{origin}
	outcome, err := d.Backfill.FillBehind(ctx, missing.roomID, destinations, missing.eventID)
	if err != nil {
		d.Log.Warn("federation: the history behind an accepted event could not be fetched",
			"room_id", missing.roomID, "event_id", missing.eventID, "origin", origin, "error", err)
		return
	}
	if len(outcome.Stored) == 0 {
		// Either the peer had nothing, or the hole was not a run of events but a single named
		// predecessor — the auth-chain shape, where the names are known. Ask for those by name.
		if names := missing.citations; len(names) > 0 {
			if _, err := d.Backfill.FillFrom(ctx, missing.roomID, destinations, names); err != nil {
				d.Log.Warn("federation: the events an accepted event cites could not be fetched",
					"room_id", missing.roomID, "event_id", missing.eventID, "origin", origin, "error", err)
			}
		}
	}
}
