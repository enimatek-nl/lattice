package persist

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeviceMessage is one event addressed to one device, waiting for that device to ask for it in `/sync`.
//
// It is not an event in a room and it borrows nothing from the event graph, which is why it has its own
// table rather than a flag on `events`. A to-device message has no room ID, no state, no auth events and
// no history: it is a note left in one device's pigeonhole, and the only ordering that means anything is
// the order the devices read it in. `m.room.encrypted` payloads travel this way, which is why a homeserver
// that cannot deliver them cannot hold an encrypted conversation.
//
// The row is per *device*, not per user, even when the sender addressed the whole user with `*`. That is
// not a simplification: each device keeps its own read position, and a row shared by three devices would
// be either read by the first and invisible to the rest, or read three times over.
type DeviceMessage struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`
	// SenderID and SenderDeviceID are recorded because a client shows who a to-device message
	// came from, and the payload itself is encrypted: without these columns the recipient could
	// not tell a key request from a stranger without decrypting it first.
	SenderID       string `gorm:"column:sender_id;type:text;not null;uniqueIndex:idx_device_messages_txn,priority:2"`
	SenderDeviceID string `gorm:"column:sender_device_id;type:text;not null;default:''"`
	// UserID and DeviceID name the recipient. A sync reads the rows that name the device asking.
	UserID   string `gorm:"column:user_id;type:text;not null;index:idx_device_messages_user_stream,priority:1;uniqueIndex:idx_device_messages_txn,priority:3"`
	DeviceID string `gorm:"column:device_id;type:text;not null;uniqueIndex:idx_device_messages_txn,priority:4"`
	// EventType is the to-device type, such as `m.room.encrypted` or `m.dummy`. It is not an
	// event type in the room sense and is not checked against a room's event rules.
	EventType string `gorm:"column:event_type;type:text;not null"`
	// Content is the payload as the sender wrote it, stored verbatim. The server does not read
	// it, and a server that re-encoded it would be tampering with an encrypted payload it
	// cannot see into.
	Content string `gorm:"column:content;type:text;not null"`
	// StreamOrdering places the message between two sync tokens, which is what makes delivery
	// exactly-once-per-token rather than once-per-read: a client that syncs twice with the same
	// `since` is entitled to the same messages twice.
	StreamOrdering int64 `gorm:"column:stream_ordering;not null;default:0;index:idx_device_messages_user_stream,priority:2"`
	// TxnID is the sender's transaction ID for this send, and is the reason a retried
	// `PUT /sendToDevice` does not deliver the same key request twice. It is NULL when the sender
	// sent without one, and NULLs do not collide in a unique index, so an un-transactioned send
	// is never refused for looking like another.
	//
	// The idempotency key is the transaction **per recipient device**, which is what the four
	// columns together mean. Keying it on the transaction alone was the first version of this and
	// it was quietly worse than a duplicate: one send addressed to two devices stored the first
	// row and discarded the second as a repeat of itself, so a client sharing a Megolm session
	// with a room of three saw the key reach the first participant only. The specification's
	// transaction ID deduplicates a retried *request*; a request is not one message, it is a set
	// of them, and the pair that identifies one is (who sent it, to which device).
	TxnID *string `gorm:"column:txn_id;type:text;uniqueIndex:idx_device_messages_txn,priority:1"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (DeviceMessage) TableName() string { return "device_messages" }

// DeviceMessageRepo is the store for messages waiting on a device.
type DeviceMessageRepo interface {
	WithTx(tx *gorm.DB) DeviceMessageRepo
	// Put stores a batch of messages and returns the stream position they were given. Rows
	// whose transaction ID this server has already seen are dropped silently, because a retry
	// of a send the server already accepted is a success, not an error.
	Put(ctx context.Context, rows []*DeviceMessage) (int64, error)
	// ListFor returns the messages addressed to one device in `(since, until]`, oldest first.
	// A device is named rather than a user because two devices of one user read at different
	// speeds, and one device's key request must not be spent by another.
	ListFor(ctx context.Context, userID, deviceID string, since, until int64, limit int) ([]*DeviceMessage, error)
	// PurgeBefore deletes messages older than a stream position, which is how a server keeps a
	// device that never syncs from filling the database with keys nobody will ever ask for.
	PurgeBefore(ctx context.Context, before int64) (int64, error)
}

type deviceMessageRepo struct{ db *gorm.DB }

// NewDeviceMessageRepo builds the store over an open database.
func NewDeviceMessageRepo(db *gorm.DB) DeviceMessageRepo { return &deviceMessageRepo{db: db} }

// WithTx implements [DeviceMessageRepo].
func (r *deviceMessageRepo) WithTx(tx *gorm.DB) DeviceMessageRepo { return &deviceMessageRepo{db: tx} }

// Put implements [DeviceMessageRepo].
func (r *deviceMessageRepo) Put(ctx context.Context, rows []*DeviceMessage) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	next, err := nextStreamPosition(ctx, r.db)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		// One position for the batch. The messages were sent by one request and a client
		// reading at any position inside the batch either sees all of them or none, which is
		// what "sent together" should mean for a set of key requests.
		row.StreamOrdering = next
	}
	err = r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(rows, 100).Error
	if err != nil {
		return 0, err
	}
	return next, nil
}

// ListFor implements [DeviceMessageRepo].
func (r *deviceMessageRepo) ListFor(ctx context.Context, userID, deviceID string, since, until int64, limit int) ([]*DeviceMessage, error) {
	query := r.db.WithContext(ctx).
		Where("user_id = ? AND stream_ordering > ? AND stream_ordering <= ?", userID, since, until)
	if deviceID != "" {
		query = query.Where("device_id = ?", deviceID)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	var rows []*DeviceMessage
	if err := query.Order("stream_ordering ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// PurgeBefore implements [DeviceMessageRepo].
func (r *deviceMessageRepo) PurgeBefore(ctx context.Context, before int64) (int64, error) {
	result := r.db.WithContext(ctx).Where("stream_ordering < ?", before).Delete(&DeviceMessage{})
	return result.RowsAffected, result.Error
}
