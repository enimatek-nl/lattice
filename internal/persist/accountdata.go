package persist

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// AccountData is one piece of per-user data that is not room state: the user's push
// rules, their room tags, their direct-room map. It is synchronised to every client
// through the `account_data` sections of `/sync`, so it carries a stream position like
// an event does: that position is what tells a client "something of mine changed since
// I last looked".
//
// The content is stored as the JSON the client sent, because a client that reads its own
// account data back must see what it wrote. Re-encoding it into Go structs would be free
// to drop keys this server does not model, and the user would lose them.
type AccountData struct {
	UserID   string `gorm:"column:user_id;type:text;not null"`
	DataType string `gorm:"column:data_type;type:text;not null"`
	Content  string `gorm:"column:content;type:text;not null"`
	// RoomID is empty for a value that belongs to the user, and the room for one that
	// belongs to the user *in* a room. The specification puts tags, DM flags and a client's
	// own annotations about one room here, and they are only ever read in that room's
	// context — which is also why the unique key is (user, room, type) and not (user,
	// type): the same client may annotate two rooms with the same name.
	RoomID         string `gorm:"column:room_id;type:text;not null;default:''"`
	StreamOrdering int64  `gorm:"column:stream_ordering;not null;default:0"`
	// Deleted marks a removal rather than a value. MSC3391 makes a removal an answer and
	// not an absence: a client that already holds this type has to be told to forget it, so
	// an incremental sync reports the type with empty content, while an initial sync —
	// where the client has nothing to forget — omits it. Deleting the row outright would
	// make the first case unanswerable and leave a stale value in every client that had
	// read it.
	Deleted bool `gorm:"column:deleted;not null;default:false"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (AccountData) TableName() string { return "account_data" }

// AccountDataRepo is the account data store contract.
type AccountDataRepo interface {
	// WithTx returns the repository working inside an open transaction.
	WithTx(tx *gorm.DB) AccountDataRepo
	// Get returns one piece of a user's account data, or ErrNotFound.
	Get(ctx context.Context, userID, dataType string) (*AccountData, error)
	// GetScoped is Get for one scope: an empty roomID is the user's own value, any other
	// is that user's value inside that room.
	GetScoped(ctx context.Context, userID, roomID, dataType string) (*AccountData, error)
	// PutScoped stores one value in one scope. A deleted value is stored as a tombstone:
	// see [AccountData.Deleted].
	PutScoped(ctx context.Context, userID, roomID, dataType, content string, deleted bool) (int64, error)
	// ListScopesSince returns every piece of a user's account data — their own and any
	// room's — whose position is greater than since. A sync reads this once and splits it
	// by scope, which is one query per sync instead of one per room.
	ListScopesSince(ctx context.Context, userID string, since int64) ([]*AccountData, error)
	// ListSince returns the user's account data of the given types whose position is
	// greater than since. An empty types list means every type.
	ListSince(ctx context.Context, userID string, since int64, types []string) ([]*AccountData, error)
	// Put stores one piece of account data and returns the stream position it was
	// given. Writing the same type again replaces it: account data is a current value,
	// not a history.
	Put(ctx context.Context, userID, dataType, content string) (int64, error)
	// Delete removes one type of a user's account data. Removing a type that was never
	// written is not an error: the state the caller asked for already holds.
	Delete(ctx context.Context, userID, dataType string) error
}

// NewAccountDataRepo returns the account data repository bound to db.
func NewAccountDataRepo(db *gorm.DB) AccountDataRepo { return &accountDataRepo{db: db} }

type accountDataRepo struct{ db *gorm.DB }

// WithTx returns the same repository working inside tx.
func (r *accountDataRepo) WithTx(tx *gorm.DB) AccountDataRepo { return &accountDataRepo{db: tx} }

// Get implements [AccountDataRepo].
func (r *accountDataRepo) Get(ctx context.Context, userID, dataType string) (*AccountData, error) {
	return r.GetScoped(ctx, userID, "", dataType)
}

// GetScoped implements [AccountDataRepo].
func (r *accountDataRepo) GetScoped(ctx context.Context, userID, roomID, dataType string) (*AccountData, error) {
	var row AccountData
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND room_id = ? AND data_type = ?", userID, roomID, dataType).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListSince implements [AccountDataRepo].
func (r *accountDataRepo) ListSince(ctx context.Context, userID string, since int64, types []string) ([]*AccountData, error) {
	q := r.db.WithContext(ctx).
		Where("user_id = ? AND room_id = ? AND stream_ordering > ?", userID, "", since).
		Order("stream_ordering ASC")
	if len(types) > 0 {
		q = q.Where("data_type IN ?", types)
	}
	var rows []*AccountData
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Put implements [AccountDataRepo].
//
// The stream position comes from the same counter events use, so one number describes
// "everything this server has seen" — which is what a sync token has to be able to say.
// The write and the position are taken in one statement pair inside the caller's
// transaction; two concurrent writers can therefore be given the same position, and that
// is harmless here: positions only have to be monotonic per row, not unique across the
// server.
func (r *accountDataRepo) Put(ctx context.Context, userID, dataType, content string) (int64, error) {
	return r.PutScoped(ctx, userID, "", dataType, content, false)
}

// ListScopesSince implements [AccountDataRepo].
func (r *accountDataRepo) ListScopesSince(ctx context.Context, userID string, since int64) ([]*AccountData, error) {
	var rows []*AccountData
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND stream_ordering > ?", userID, since).
		Order("stream_ordering ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// PutScoped implements [AccountDataRepo].
//
// The stream position comes from the same counter events use, so one number describes
// "everything this server has seen" — which is what a sync token has to be able to say.
// The write and the position are taken in one statement pair inside the caller's
// transaction; two concurrent writers can therefore be given the same position, and that
// is harmless here: positions only have to be monotonic per row, not unique across the
// server.
func (r *accountDataRepo) PutScoped(ctx context.Context, userID, roomID, dataType, content string, deleted bool) (int64, error) {
	next, err := nextStreamPosition(ctx, r.db)
	if err != nil {
		return 0, err
	}
	row := AccountData{UserID: userID, RoomID: roomID, DataType: dataType, Content: content,
		StreamOrdering: next, Deleted: deleted}
	// A map, not a struct: assigning a struct lets GORM apply Updates semantics, which skip
	// zero values, and `deleted: false` is the whole point of writing a value back over a
	// tombstone. The row would keep the content of the new write and stay invisible.
	err = r.db.WithContext(ctx).
		Where("user_id = ? AND room_id = ? AND data_type = ?", userID, roomID, dataType).
		Assign(map[string]any{"content": content, "stream_ordering": next, "deleted": deleted}).
		FirstOrCreate(&row).Error
	if err != nil {
		return 0, err
	}
	return next, nil
}

// Delete removes one type of a user's own account data. It is a tombstone write, not a
// row deletion: see [AccountData.Deleted]. Removing a type that is already absent, or
// never was, leaves the same tombstone either way and is not an error.
func (r *accountDataRepo) Delete(ctx context.Context, userID, dataType string) error {
	_, err := r.PutScoped(ctx, userID, "", dataType, "{}", true)
	return err
}
