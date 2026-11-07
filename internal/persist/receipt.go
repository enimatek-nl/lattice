package persist

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Receipt is one user's read position in a room: which event they have read, of which kind,
// and the stream position the last move took.
//
// A receipt is not an event. It has no event ID, no authorization and no place in the room's
// graph — it is a current value about a person, like presence is, which is why it is stored as
// one row per (room, kind, user) and carries a position in the same stream events use: a client
// asks "what has happened since my last sync", and a receipt that moved has happened.
type Receipt struct {
	// ID is this row's key. A receipt is one row per (room, kind, user, thread) that moves,
	// and an update has to be able to find the row it is replacing.
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	RoomID      string `gorm:"column:room_id;type:text;not null"`
	ReceiptType string `gorm:"column:receipt_type;type:text;not null"`
	UserID      string `gorm:"column:user_id;type:text;not null"`
	// EventID is the event the user has read up to. It is not checked again on the way out:
	// an event this server has never held can still be named, which is what a receipt for a
	// message that arrives later over federation looks like.
	EventID string `gorm:"column:event_id;type:text;not null"`
	// ThreadID scopes the receipt to one thread, which is what MSC2028's receipts look like.
	// Empty means the room's timeline rather than a thread.
	ThreadID string `gorm:"column:thread_id;type:text;not null;default:''"`
	// ThreadScoped records that a receipt belongs to a thread even when the thread ID is the
	// special "m.main". Kept apart from ThreadID so an empty one is not read as "no thread".
	ThreadScoped bool  `gorm:"column:thread_scoped;not null;default:false"`
	TS           int64 `gorm:"column:ts;not null;default:0"`
	// StreamOrdering is the position the last move took.
	StreamOrdering int64 `gorm:"column:stream_ordering;not null;default:0"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (Receipt) TableName() string { return "receipts" }

// ReceiptRepo is the read-receipt store contract.
type ReceiptRepo interface {
	// WithTx returns the repository working inside an open transaction.
	WithTx(tx *gorm.DB) ReceiptRepo
	// Put records that a user has read up to an event, replacing whatever position they had
	// for that room and kind of receipt, and returns the stream position the move took.
	Put(ctx context.Context, receipt *Receipt) (int64, error)
	// ListForRoom returns the receipts of a room whose position is greater than since and at
	// most until, oldest first. since of zero and until of zero ask for the newest of every
	// user, which is what an initial sync sends.
	ListForRoom(ctx context.Context, roomID string, since, until int64, limit int) ([]*Receipt, error)
	// CountForUser answers how many events in a room this user has not read, from their
	// receipt's position. It is the question a notification count is built from.
	CountUnread(ctx context.Context, roomID, userID string, after int64) (int64, error)
}

// NewReceiptRepo returns the receipt repository bound to db.
func NewReceiptRepo(db *gorm.DB) ReceiptRepo { return &receiptRepo{db: db} }

type receiptRepo struct{ db *gorm.DB }

// WithTx implements [ReceiptRepo].
func (r *receiptRepo) WithTx(tx *gorm.DB) ReceiptRepo { return &receiptRepo{db: tx} }

// Put implements [ReceiptRepo].
func (r *receiptRepo) Put(ctx context.Context, receipt *Receipt) (int64, error) {
	var existing Receipt
	err := r.db.WithContext(ctx).
		Where("room_id = ? AND receipt_type = ? AND user_id = ? AND thread_id = ?",
			receipt.RoomID, receipt.ReceiptType, receipt.UserID, receipt.ThreadID).
		First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := r.db.WithContext(ctx).Create(receipt).Error; err != nil {
			return 0, err
		}
		return receipt.StreamOrdering, nil
	}
	existing.EventID = receipt.EventID
	existing.TS = receipt.TS
	existing.ThreadScoped = receipt.ThreadScoped
	existing.StreamOrdering = receipt.StreamOrdering
	if err := r.db.WithContext(ctx).Model(&Receipt{}).
		Where("id = ?", existing.ID).
		Updates(map[string]any{
			"event_id":        existing.EventID,
			"ts":              existing.TS,
			"thread_scoped":   existing.ThreadScoped,
			"stream_ordering": existing.StreamOrdering,
		}).Error; err != nil {
		return 0, err
	}
	return existing.StreamOrdering, nil
}

// ListForRoom implements [ReceiptRepo].
func (r *receiptRepo) ListForRoom(ctx context.Context, roomID string, since, until int64, limit int) ([]*Receipt, error) {
	query := r.db.WithContext(ctx).Where("room_id = ?", roomID)
	if since > 0 || until > 0 {
		query = query.Where("stream_ordering > ? AND stream_ordering <= ?", since, until)
	}
	var rows []*Receipt
	if err := query.Order("stream_ordering asc").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	if since > 0 || until > 0 {
		return rows, nil
	}
	// An initial sync wants the newest receipt of each kind from each user, not the last
	// handful written to the room. The rows come back oldest first, so keeping the last one
	// seen per (kind, user, thread) is that answer.
	latest := map[string]*Receipt{}
	order := []string{}
	for _, row := range rows {
		key := row.ReceiptType + "\x00" + row.UserID + "\x00" + row.ThreadID
		if _, seen := latest[key]; !seen {
			order = append(order, key)
		}
		latest[key] = row
	}
	out := make([]*Receipt, 0, len(order))
	for _, key := range order {
		out = append(out, latest[key])
	}
	return out, nil
}

// CountUnread implements [ReceiptRepo].
func (r *receiptRepo) CountUnread(ctx context.Context, roomID, userID string, after int64) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&Event{}).
		Where("room_id = ? AND stream_ordering > ?", roomID, after).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
