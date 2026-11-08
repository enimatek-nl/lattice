package persist

import (
	"context"

	"gorm.io/gorm"
)

// The stream is the single ordering a sync token speaks of.
//
// Events, account data, receipts and presence are different tables with different lifetimes, but a
// client's token has to be able to say "everything up to here". That only holds if every
// one of them takes its position from the same counter, so the counter is this: the
// highest position any of these tables holds, plus one.
//
// A single writer is what makes this safe to do with a read and a write rather than a lock.
// The pool is configured that way already (see [Open]), so two writers cannot read the same
// maximum and be handed the same position.

// streamTables are the tables that take part in the stream. Adding a table here is how a
// new kind of synchronisable data joins it; forgetting to is how a sync token would start
// answering "nothing changed" about data the client has not seen.
var streamTables = []string{"events", "account_data", "receipts", "presence", "typing", "device_list_changes", "device_keys"}

// nextStreamPosition returns the position the next change should take.
func nextStreamPosition(ctx context.Context, db *gorm.DB) (int64, error) {
	// The subquery name is not reserved in SQLite, but it is quoted anyway so that a
	// future table named like a keyword cannot make this fail in a confusing way.
	var next int64
	if err := db.WithContext(ctx).Raw(
		`SELECT COALESCE(MAX(stream_ordering), 0) + 1 FROM (` + unionOfStreamPositions() + `) AS stream_positions`,
	).Scan(&next).Error; err != nil {
		return 0, err
	}
	return next, nil
}

// maxStreamPosition returns the highest position any table holds, which is what a sync
// response reports as `next_batch`: the client is told that everything up to and including
// that number has been seen.
func maxStreamPosition(ctx context.Context, db *gorm.DB) (int64, error) {
	var max int64
	if err := db.WithContext(ctx).Raw(
		`SELECT COALESCE(MAX(stream_ordering), 0) FROM (` + unionOfStreamPositions() + `) AS stream_positions`,
	).Scan(&max).Error; err != nil {
		return 0, err
	}
	return max, nil
}

// unionOfStreamPositions builds the UNION ALL over the stream tables. The table names are
// constants in this file, never client input, so they are written into the statement
// directly rather than passed as parameters (which a UNION of table names cannot be).
func unionOfStreamPositions() string {
	query := ""
	for i, table := range streamTables {
		if i > 0 {
			query += " UNION ALL "
		}
		query += "SELECT stream_ordering FROM " + table
	}
	return query
}

// StreamPosition is the highest position in the stream. `/sync` reports it as
// `next_batch`; `/messages` pages over event positions only and reads them from the event
// repository.
func (d *Database) StreamPosition(ctx context.Context) (int64, error) {
	return maxStreamPosition(ctx, d.db)
}
