package rooms

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/persist"
)

// The authorisation rules ask three questions of the past: what holds this state key,
// what event has this ID, and was this event rejected on receipt. Two implementations
// answer them — one over a set of events being built in memory, one over stored
// events — so the rules do not have to know which they are talking to.

// memorySnapshot answers the rules over events held in memory. Room creation uses it,
// because the state of a room being built does not exist in the database yet.
type memorySnapshot struct {
	byKey map[eventauth.Key]map[string]any
	byID  map[string]map[string]any
}

func newMemorySnapshot() *memorySnapshot {
	return &memorySnapshot{
		byKey: map[eventauth.Key]map[string]any{},
		byID:  map[string]map[string]any{},
	}
}

// add puts an event into the snapshot, indexing it both by state key and by event ID,
// which is how the rules resolve auth events.
func (m *memorySnapshot) add(event map[string]any) {
	id, _ := event["event_id"].(string)
	if id != "" {
		m.byID[id] = event
	}
	if stateKey, ok := event["state_key"].(string); ok {
		eventType, _ := event["type"].(string)
		m.byKey[eventauth.Key{Type: eventType, StateKey: stateKey}] = event
	}
}

// State implements [eventauth.Snapshot].
func (m *memorySnapshot) State(key eventauth.Key) (map[string]any, bool) {
	event, ok := m.byKey[key]
	return event, ok
}

// Event implements [eventauth.Snapshot].
func (m *memorySnapshot) Event(id string) (map[string]any, bool) {
	event, ok := m.byID[id]
	return event, ok
}

// Rejected implements [eventauth.Snapshot]. An event built by this server in this
// moment has not been rejected; nothing has judged it yet.
func (m *memorySnapshot) Rejected(string) bool { return false }

// dbSnapshot answers the rules over stored events. It caches what it reads, because a
// single authorisation pass asks for the same create, power levels and membership
// events repeatedly.
type dbSnapshot struct {
	events   persist.EventRepo
	ctx      context.Context
	roomID   string
	state    map[eventauth.Key]map[string]any
	byID     map[string]map[string]any
	err      error
	rejected map[string]string
	asked    map[string]bool
}

// State implements [eventauth.Snapshot].
func (d *dbSnapshot) State(key eventauth.Key) (map[string]any, bool) {
	event, ok := d.state[key]
	return event, ok
}

// Event implements [eventauth.Snapshot]. A load failure is remembered in d.err so the
// caller can tell "missing" from "the database failed"; the rules treat both as
// unresolvable, and that distinction is what keeps a storage fault from looking like
// a malformed event.
func (d *dbSnapshot) Event(id string) (map[string]any, bool) {
	event, err := d.load(id)
	if err != nil {
		return nil, false
	}
	return event, true
}

// load returns the parsed event, or the storage error.
func (d *dbSnapshot) load(id string) (map[string]any, error) {
	if event, ok := d.byID[id]; ok {
		return event, nil
	}
	row, err := d.events.ByID(d.ctx, id)
	if err != nil {
		if !notFound(err) {
			d.err = err
		}
		return nil, err
	}
	event, err := events.Parse(row.JSON)
	if err != nil {
		d.err = err
		return nil, err
	}
	d.byID[id] = event
	return event, nil
}

// Rejected implements [eventauth.Snapshot].
//
// A refusal is a verdict this server stands by even though it did not keep the event.
// Without it, the same refused event can be offered again, or an event citing it judged
// as though nothing had been decided — and two servers that once agreed that an event was
// bad would then disagree about its successors. Events this server accepted are never
// reported here, because an accepted event is stored and `Event` answers for it.
func (d *dbSnapshot) Rejected(id string) bool {
	if id == "" {
		return false
	}
	if _, refused := d.rejected[id]; refused {
		return true
	}
	if d.asked[id] {
		return false
	}
	if d.asked == nil {
		d.asked = map[string]bool{}
	}
	d.asked[id] = true
	reasons, err := d.events.Rejections(d.ctx, []string{id})
	if err != nil {
		if !notFound(err) {
			d.err = err
		}
		return false
	}
	if reason, ok := reasons[id]; ok {
		if d.rejected == nil {
			d.rejected = map[string]string{}
		}
		d.rejected[id] = reason
		return true
	}
	return false
}

// notFound recognises both the repository sentinel and the driver-level error, so a
// missing event is treated as missing whatever layer reported it.
func notFound(err error) bool {
	return errors.Is(err, persist.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}
