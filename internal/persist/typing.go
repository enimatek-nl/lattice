package persist

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Typing is one user's "I am typing" indicator in one room: when it stops being true, and the
// stream position its last change took.
//
// Like a receipt and unlike an event, it has no event ID, no authorisation and no place in the
// room's graph — it is a current value about a person that is expected to go stale on its own.
// Two consequences follow, and they are why this table is shaped the way it is:
//
//   - It carries a position in the same stream events use, because a client's sync window asks
//     "what changed in this room since my token", and a typing list that changed has changed.
//
//   - A row is never deleted, only emptied. `until_ts` of zero means "this user is not typing
//     here", which is a tombstone in the same sense [AccountData.Deleted] is one: the row has to
//     stay because the position it carries is how a window knows the room's typing list moved,
//     and a deleted row leaves no position behind. A stream whose maximum can fall would hand a
//     client a `next_batch` older than the token it already holds.
//
// The table therefore holds at most one small row per (room, user) who has ever typed, which is
// the same growth account data's tombstones allow.
type Typing struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement"`
	RoomID string `gorm:"column:room_id;type:text;not null"`
	UserID string `gorm:"column:user_id;type:text;not null"`
	// UntilTS is when the indicator stops being true, in milliseconds since the epoch; zero says
	// the user is not typing. A client supplies the timeout, so a non-zero value is the client's
	// own promise about how long it means to keep typing.
	UntilTS int64 `gorm:"column:until_ts;not null;default:0"`
	// StreamOrdering is the position the last change took: the indicator being set, moved,
	// stopped by the user, or swept past its timeout.
	StreamOrdering int64 `gorm:"column:stream_ordering;not null;default:0"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (Typing) TableName() string { return "typing" }

// TypingEDUType is the ephemeral unit this indicator travels as, down `/sync` and over
// federation alike.
const TypingEDUType = "m.typing"

// TypingRepo is the typing-indicator store contract.
type TypingRepo interface {
	// WithTx returns the repository working inside an open transaction.
	WithTx(tx *gorm.DB) TypingRepo
	// Set marks a user as typing until untilMillis, or clears their indicator when typing is
	// false, and returns the stream position the change took. One row per (room, user): typing
	// again is the same statement refreshed, not a new fact. A change that changes nothing —
	// stopping when not typing, above all — takes no position and returns zero, so a room is not
	// reported to every client watching it because one user pressed a key twice.
	Set(ctx context.Context, roomID, userID string, typing bool, untilMillis int64) (int64, error)
	// List returns the users whose indicator has not expired as of nowMillis, sorted. An expired
	// row that has not been swept yet is simply not in the answer.
	List(ctx context.Context, roomID string, nowMillis int64) ([]string, error)
	// ExpireRoom empties the indicators of one room that have passed their timeout, each taking a
	// fresh stream position, and returns how many were emptied. An indicator timing out is a
	// change to the list, and a client that is shown somebody typing has to be shown them stop.
	ExpireRoom(ctx context.Context, roomID string, nowMillis int64) (int, error)
	// LastChange returns the highest stream position this room's typing holds, or zero when it
	// holds none. A sync window compares this against its own bounds to decide whether the room's
	// typing list is news to this client.
	LastChange(ctx context.Context, roomID string) (int64, error)
	// EmptyForUser clears a user's indicator in a room without taking a position: a user who has
	// left is not typing there, and nobody needs to be told that separately, because their
	// membership event says it.
	EmptyForUser(ctx context.Context, roomID, userID string) error
}

// NewTypingRepo returns the typing repository bound to db.
func NewTypingRepo(db *gorm.DB) TypingRepo { return &typingRepo{db: db} }

type typingRepo struct{ db *gorm.DB }

// WithTx implements [TypingRepo].
func (r *typingRepo) WithTx(tx *gorm.DB) TypingRepo { return &typingRepo{db: tx} }

// find returns the row for this room and user, or nil when there is none.
func (r *typingRepo) find(ctx context.Context, roomID, userID string) (*Typing, error) {
	var row Typing
	err := r.db.WithContext(ctx).
		Where("room_id = ? AND user_id = ?", roomID, userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Set implements [TypingRepo].
func (r *typingRepo) Set(ctx context.Context, roomID, userID string, typing bool, untilMillis int64) (int64, error) {
	existing, err := r.find(ctx, roomID, userID)
	if err != nil {
		return 0, err
	}
	if !typing {
		if existing == nil || existing.UntilTS == 0 {
			// Nothing to stop. Taking a position here would tell every client watching the
			// room that its typing list changed, twice in a row, identically.
			return 0, nil
		}
		next, err := nextStreamPosition(ctx, r.db)
		if err != nil {
			return 0, err
		}
		if err := r.db.WithContext(ctx).Model(&Typing{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{"until_ts": 0, "stream_ordering": next}).Error; err != nil {
			return 0, err
		}
		return next, nil
	}
	next, err := nextStreamPosition(ctx, r.db)
	if err != nil {
		return 0, err
	}
	if existing == nil {
		if err := r.db.WithContext(ctx).Create(&Typing{
			RoomID:         roomID,
			UserID:         userID,
			UntilTS:        untilMillis,
			StreamOrdering: next,
		}).Error; err != nil {
			return 0, err
		}
		return next, nil
	}
	if err := r.db.WithContext(ctx).Model(&Typing{}).
		Where("id = ?", existing.ID).
		Updates(map[string]any{"until_ts": untilMillis, "stream_ordering": next}).Error; err != nil {
		return 0, err
	}
	return next, nil
}

// List implements [TypingRepo].
func (r *typingRepo) List(ctx context.Context, roomID string, nowMillis int64) ([]string, error) {
	var users []string
	err := r.db.WithContext(ctx).Model(&Typing{}).
		Where("room_id = ? AND until_ts > ?", roomID, nowMillis).
		Order("user_id asc").
		Pluck("user_id", &users).Error
	if err != nil {
		return nil, err
	}
	return users, nil
}

// ExpireRoom implements [TypingRepo].
func (r *typingRepo) ExpireRoom(ctx context.Context, roomID string, nowMillis int64) (int, error) {
	var rows []*Typing
	err := r.db.WithContext(ctx).
		Where("room_id = ? AND until_ts > ? AND until_ts <= ?", roomID, 0, nowMillis).
		Find(&rows).Error
	if err != nil {
		return 0, err
	}
	swept := 0
	for _, row := range rows {
		next, err := nextStreamPosition(ctx, r.db)
		if err != nil {
			return swept, err
		}
		if err := r.db.WithContext(ctx).Model(&Typing{}).
			Where("id = ?", row.ID).
			Updates(map[string]any{"until_ts": 0, "stream_ordering": next}).Error; err != nil {
			return swept, err
		}
		swept++
	}
	return swept, nil
}

// LastChange implements [TypingRepo].
func (r *typingRepo) LastChange(ctx context.Context, roomID string) (int64, error) {
	var last int64
	if err := r.db.WithContext(ctx).Model(&Typing{}).
		Where("room_id = ?", roomID).
		Select("COALESCE(MAX(stream_ordering), 0)").Scan(&last).Error; err != nil {
		return 0, err
	}
	return last, nil
}

// EmptyForUser implements [TypingRepo].
func (r *typingRepo) EmptyForUser(ctx context.Context, roomID, userID string) error {
	existing, err := r.find(ctx, roomID, userID)
	if err != nil || existing == nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&Typing{}).
		Where("id = ?", existing.ID).Update("until_ts", 0).Error
}
