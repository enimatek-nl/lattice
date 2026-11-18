package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Typing is the transient typing state of a user in a room. It is surfaced as
// an ephemeral event by sync.
type Typing struct {
	RoomID string `json:"room_id" gorm:"uniqueIndex:uq_typing_scope;size:255"`
	UserID string `json:"user_id" gorm:"uniqueIndex:uq_typing_scope;size:255"`
	Typing bool   `json:"typing"`
	SentTS int64  `json:"ts"`
	// ExpiresTS is the wall clock time (ms) at which an active typing state
	// lapses on its own, taken from the timeout the client supplied.
	ExpiresTS int64 `json:"expires_ts"`
	// StreamPos records the position of the last change so sync can report
	// typing deltas.
	StreamPos uint64 `json:"stream_pos" gorm:"index"`

	gorm.Model
}

// TypingRepo is the contract for typing state storage access.
type TypingRepo interface {
	Set(typing *Typing) error
	List(roomID string) ([]*Typing, error)
	Recent(roomID string, since uint64) ([]*Typing, error)
	ActiveUserIDs(roomID, except string, nowMS int64) ([]string, error)
	// Prune drops typing states that stopped changing before nowMS-graceMS and
	// are no longer active, returning the number of rows removed. The grace
	// window keeps the "somebody stopped typing" delta deliverable.
	Prune(nowMS, graceMS int64) (int64, error)
}

// TypingRepoImpl is the GORM backed TypingRepo.
type TypingRepoImpl struct {
	db *gorm.DB
}

// Set implements [TypingRepo].
func (r *TypingRepoImpl) Set(typing *Typing) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "room_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"typing", "sent_ts", "expires_ts", "stream_pos", "updated_at"}),
	}).Create(typing).Error
}

// List implements [TypingRepo].
func (r *TypingRepoImpl) List(roomID string) ([]*Typing, error) {
	var rows []*Typing
	err := r.db.Where("room_id = ?", roomID).Find(&rows).Error
	return rows, err
}

// Recent implements [TypingRepo]: rows of a room changed after a position.
func (r *TypingRepoImpl) Recent(roomID string, since uint64) ([]*Typing, error) {
	var rows []*Typing
	err := r.db.Where("room_id = ? AND stream_pos > ?", roomID, since).
		Order("stream_pos asc").Find(&rows).Error
	return rows, err
}

// ActiveUserIDs implements [TypingRepo]: users whose typing state is still
// within the timeout they asked for.
func (r *TypingRepoImpl) ActiveUserIDs(roomID, except string, nowMS int64) ([]string, error) {
	q := r.db.Model(&Typing{}).Where("room_id = ? AND typing = ?", roomID, true)
	if nowMS > 0 {
		q = q.Where("expires_ts > ?", nowMS)
	}
	if except != "" {
		q = q.Where("user_id <> ?", except)
	}
	var ids []string
	err := q.Pluck("user_id", &ids).Error
	return ids, err
}

// Prune implements [TypingRepo].
func (r *TypingRepoImpl) Prune(nowMS, graceMS int64) (int64, error) {
	result := r.db.Unscoped().
		Where("sent_ts < ? AND (typing = ? OR expires_ts <= ?)", nowMS-graceMS, false, nowMS).
		Unscoped().Delete(&Typing{})
	return result.RowsAffected, result.Error
}

// NewTypingRepo returns the TypingRepo implementation for db.
func NewTypingRepo(db *gorm.DB) TypingRepo {
	return &TypingRepoImpl{db: db}
}

// Txn records the event created for a client transaction id so that retries of
// PUT /send and PUT /state are idempotent.
type Txn struct {
	UserID  string `json:"user_id" gorm:"uniqueIndex:uq_txn_scope;size:255"`
	Kind    string `json:"kind" gorm:"uniqueIndex:uq_txn_scope;size:32"`
	RoomID  string `json:"room_id" gorm:"uniqueIndex:uq_txn_scope;size:255"`
	TxnID   string `json:"txn_id" gorm:"uniqueIndex:uq_txn_scope;size:255"`
	EventID string `json:"event_id" gorm:"index:idx_txn_reader,priority:2;size:255"`
	// DeviceID is which device of the sender wrote the event. A transaction id is the sender's
	// own note about one of its sends, and devices do not share notes - so it is kept, to be
	// handed back to exactly the device that wrote it and to nobody else.
	DeviceID  string `json:"device_id" gorm:"index:idx_txn_reader,priority:1;size:255"`
	StreamPos uint64 `json:"stream_pos" gorm:"index"`
	// SentTS is the origin_server_ts of the event the transaction produced; the
	// retention window of the table is measured against it.
	SentTS int64 `json:"sent_ts" gorm:"index"`

	gorm.Model
}

// TxnRepo is the contract for transaction id storage access.
type TxnRepo interface {
	Set(txn *Txn) error
	Get(userID, kind, roomID, txnID string) (*Txn, error)
	// ForSender answers "which of these events did this device send, and what did it call
	// them?". It is the read side of a transaction id: the note a device attached to its own
	// send, keyed by the event it produced. Another device - of the same user or of another -
	// is not answered, so a device that never sent the event learns nothing about what the
	// sender's send was called.
	ForSender(userID, deviceID string, eventIDs []string) (map[string]string, error)
	// Expire deletes transaction records of events sent before the given
	// timestamp, so replay protection covers a rolling window instead of
	// growing forever.
	Expire(beforeTS int64) (int64, error)
}

// TxnRepoImpl is the GORM backed TxnRepo.
type TxnRepoImpl struct {
	db *gorm.DB
}

// Set implements [TxnRepo].
func (r *TxnRepoImpl) Set(txn *Txn) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"}, {Name: "kind"}, {Name: "room_id"}, {Name: "txn_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"event_id", "stream_pos", "sent_ts", "updated_at"}),
	}).Create(txn).Error
}

// Get implements [TxnRepo].
func (r *TxnRepoImpl) Get(userID, kind, roomID, txnID string) (*Txn, error) {
	var txn Txn
	err := r.db.Where("user_id = ? AND kind = ? AND room_id = ? AND txn_id = ?",
		userID, kind, roomID, txnID).First(&txn).Error
	return &txn, err
}

// ForSender implements [TxnRepo].
//
// The device is part of the question rather than a filter applied afterwards: a row written by
// another device is not an answer, and an empty device - a send that came from inside the
// server, or from a session that has since been deleted - answers nothing.
func (r *TxnRepoImpl) ForSender(userID, deviceID string, eventIDs []string) (map[string]string, error) {
	if userID == "" || deviceID == "" || len(eventIDs) == 0 {
		return nil, nil
	}
	var rows []*Txn
	if err := r.db.Where("user_id = ? AND device_id = ? AND event_id IN ?", userID, deviceID, eventIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.EventID] = row.TxnID
	}
	return out, nil
}

// Expire implements [TxnRepo].
func (r *TxnRepoImpl) Expire(beforeTS int64) (int64, error) {
	result := r.db.Unscoped().Where("sent_ts < ?", beforeTS).Delete(&Txn{})
	return result.RowsAffected, result.Error
}

// NewTxnRepo returns the TxnRepo implementation for db.
func NewTxnRepo(db *gorm.DB) TxnRepo {
	return &TxnRepoImpl{db: db}
}
