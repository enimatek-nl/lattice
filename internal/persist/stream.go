package persist

import "errors"

// ErrNoStreamPos is returned when no table holding a stream position could be read at all,
// which means the schema is not the one this build expects.
var ErrNoStreamPos = errors.New("no stream position table could be read")

// streamModels are the models that carry an event stream position. The list matters because the
// counter that hands those positions out is seeded from them at start-up: a position that is
// higher than the seed is a position a restarted server will hand out again, and every client
// holding a since token past it silently stops receiving anything.
//
// A model whose table has no stream_pos column is skipped rather than fatal: the schema is allowed
// to change under this function, and a missing column is a table that does not participate, not a
// reason to refuse to start.
var streamModels = []any{
	&Event{},
	&RoomState{},
	&AccountData{},
	&Typing{},
	&ToDevice{},
	&Receipt{},
	&ReadMarker{},
	&DeviceKey{},
	&DeviceListChange{},
	&Notification{},
}

// LatestStreamPos returns the highest event stream position the database holds, across every
// table that carries one.
//
// Asking only the event table is not enough, and the difference is visible after a restart. A
// server spends most of its life writing things that are not events - receipts, to-device
// messages, device list updates, read markers - and each of those takes a position from the same
// counter. Seed from events alone and a server that has been running for a week comes back with a
// counter behind the tokens it issued, so the changes it makes next are, as far as its own sync is
// concerned, already in the past.
func (d *Database) LatestStreamPos() (uint64, error) {
	var best uint64
	seen := false
	for _, model := range streamModels {
		var pos uint64
		if err := d.gorm.Model(model).Select("COALESCE(MAX(stream_pos), 0)").Scan(&pos).Error; err != nil {
			continue
		}
		seen = true
		if pos > best {
			best = pos
		}
	}
	if !seen {
		return 0, ErrNoStreamPos
	}
	return best, nil
}
