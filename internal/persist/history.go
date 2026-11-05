package persist

import "context"

// EventsWindow returns the non-outlier events in a room between two stream positions.
//
// `from` is exclusive on both sides: the event exactly at `from` has already been seen by
// whichever endpoint issued that token. Ordering follows the direction a client is
// walking the timeline in — oldest first going forward, newest first going back — because
// that is the order the page is read in, and reversing it in the caller would hide which
// end of the room the page came from.
func (r *eventRepo) EventsWindow(ctx context.Context, roomID string, from int64, backwards bool, limit int) ([]*Event, error) {
	if limit <= 0 {
		limit = 1
	}
	order := "stream_ordering ASC"
	op := ">"
	if backwards {
		order = "stream_ordering DESC"
		op = "<"
	}
	var rows []*Event
	err := r.db.WithContext(ctx).
		Where("room_id = ? AND stream_ordering "+op+" ? AND outlier = ? AND soft_failed = ?", roomID, from, false, false).
		Order(order).Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
