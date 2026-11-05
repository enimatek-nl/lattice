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
	UserID         string `gorm:"column:user_id;type:text;not null"`
	DataType       string `gorm:"column:data_type;type:text;not null"`
	Content        string `gorm:"column:content;type:text;not null"`
	StreamOrdering int64  `gorm:"column:stream_ordering;not null;default:0"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (AccountData) TableName() string { return "account_data" }

// AccountDataRepo is the account data store contract.
type AccountDataRepo interface {
	// WithTx returns the repository working inside an open transaction.
	WithTx(tx *gorm.DB) AccountDataRepo
	// Get returns one piece of a user's account data, or ErrNotFound.
	Get(ctx context.Context, userID, dataType string) (*AccountData, error)
	// ListSince returns the user's account data of the given types whose position is
	// greater than since. An empty types list means every type.
	ListSince(ctx context.Context, userID string, since int64, types []string) ([]*AccountData, error)
	// Put stores one piece of account data and returns the stream position it was
	// given. Writing the same type again replaces it: account data is a current value,
	// not a history.
	Put(ctx context.Context, userID, dataType, content string) (int64, error)
}

// NewAccountDataRepo returns the account data repository bound to db.
func NewAccountDataRepo(db *gorm.DB) AccountDataRepo { return &accountDataRepo{db: db} }

type accountDataRepo struct{ db *gorm.DB }

// WithTx returns the same repository working inside tx.
func (r *accountDataRepo) WithTx(tx *gorm.DB) AccountDataRepo { return &accountDataRepo{db: tx} }

// Get implements [AccountDataRepo].
func (r *accountDataRepo) Get(ctx context.Context, userID, dataType string) (*AccountData, error) {
	var row AccountData
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND data_type = ?", userID, dataType).First(&row).Error
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
		Where("user_id = ? AND stream_ordering > ?", userID, since).
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
	next, err := nextStreamPosition(ctx, r.db)
	if err != nil {
		return 0, err
	}
	row := AccountData{UserID: userID, DataType: dataType, Content: content, StreamOrdering: next}
	err = r.db.WithContext(ctx).
		Where("user_id = ? AND data_type = ?", userID, dataType).
		Assign(AccountData{Content: content, StreamOrdering: next}).
		FirstOrCreate(&row).Error
	if err != nil {
		return 0, err
	}
	return next, nil
}
