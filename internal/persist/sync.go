package persist

import "context"

// Queries for client synchronisation. They are kept apart from the event-graph queries
// because they answer a different question: not "how does this room's history hang
// together" but "what happened to this user since they last looked".
//
// `stream_ordering` is the currency here. It is a global, monotonic insertion position,
// not a per-room counter, so one number can describe "everything this server has seen".
// It is deliberately not Matrix: the specification forbids using it for event ordering
// between rooms, and this is exactly why — a state resolution would be wrong, but a
// "what did this server persist while you were away" scan is precisely what it is for.

// EventsAfter returns the events in a room with a stream position greater than after,
// oldest first, at most limit (all of them when limit <= 0).
//
// Outliers and soft-failed events are left out because both are invisible to clients by
// definition: an outlier has no resolved place in the history, and a soft-failed event
// is one this server's current state says should not have been sent — the specification
// says it is stored and resolved but not relayed.
func (r *eventRepo) EventsAfter(ctx context.Context, roomID string, after int64, limit int) ([]*Event, error) {
	q := r.db.WithContext(ctx).
		Where("room_id = ? AND stream_ordering > ? AND outlier = ? AND soft_failed = ?", roomID, after, false, false).
		Order("stream_ordering ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []*Event
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountAfter counts the events a client would be shown after a stream position. It
// excludes exactly what EventsAfter excludes, because a `limited` flag computed over a
// different set than the one being returned would be a wrong flag.
func (r *eventRepo) CountAfter(ctx context.Context, roomID string, after int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&Event{}).
		Where("room_id = ? AND stream_ordering > ? AND outlier = ? AND soft_failed = ?", roomID, after, false, false).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

// LatestEvents returns the newest events in a room, oldest first in the slice, which is
// the shape a client timeline wants. Outliers and soft-failed events are excluded: an
// event whose place in the history is unresolved, or which this server has decided not
// to relay, has no place in a timeline.
func (r *eventRepo) LatestEvents(ctx context.Context, roomID string, limit int) ([]*Event, error) {
	if limit <= 0 {
		limit = 1
	}
	var rows []*Event
	if err := r.db.WithContext(ctx).
		Where("room_id = ? AND outlier = ? AND soft_failed = ?", roomID, false, false).
		Order("stream_ordering DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

// MaxStreamOrdering returns the highest stream position the server has assigned, which
// is what a sync token for "now" means. A table scan of a large event table would be
// expensive, so the ordering index is used by reading the last row.
func (r *eventRepo) MaxStreamOrdering(ctx context.Context) (int64, error) {
	var rows []Event
	err := r.db.WithContext(ctx).Model(&Event{}).
		Order("stream_ordering DESC").Limit(1).Find(&rows).Error
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].StreamOrdering, nil
}

// MembershipEventsFor returns the current m.room.member state entries that name a user,
// with the event loaded, so a caller can tell both what the membership is and when the
// server saw the change.
func (s *stateRepo) MembershipEventsFor(ctx context.Context, userID string) ([]*Event, error) {
	entries, err := s.MembershipsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.EventID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	events, err := (&eventRepo{db: s.db}).ByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return events, nil
}
