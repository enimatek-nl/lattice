package persist

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Presence is one user's presence as this server reports it: whether they are around,
// what they said they are doing, and the stream position their last change took.
//
// The position is what makes presence synchronisable. A presence row is a current value,
// like account data is, but a client needs to be told about the *change*, so each change
// takes a position in the same stream events use.
type Presence struct {
	UserID string `gorm:"column:user_id;type:text;not null"`
	// State is one of the three values the Client-Server API defines: online,
	// unavailable, offline. It is stored as text because a client compares the words and
	// a number here would only invite a mapping mistake.
	State string `gorm:"column:state;type:text;not null"`
	// StatusMsg is the user's own words, empty when they set none.
	StatusMsg string `gorm:"column:status_msg;type:text;not null;default:''"`
	// LastActiveTS is when this server last saw the user do something, in milliseconds
	// since the epoch. A client is shown how long ago that was rather than the
	// timestamp, which is what the specification's `last_active_ago` says.
	LastActiveTS int64 `gorm:"column:last_active_ts;not null;default:0"`
	// Active says whether this server considers the user present right now. It is kept
	// apart from State because a user can set `unavailable` while still being connected,
	// and a client needs both facts to show the right dot.
	Active bool `gorm:"column:active;not null;default:false"`
	// StreamOrdering is the position the last change took.
	StreamOrdering int64 `gorm:"column:stream_ordering;not null;default:0"`
}

// TableName fixes the table name rather than letting GORM pluralise the type name.
func (Presence) TableName() string { return "presence" }

// PresenceRepo is the presence store contract.
type PresenceRepo interface {
	// WithTx returns the repository working inside an open transaction.
	WithTx(tx *gorm.DB) PresenceRepo
	// Get returns one user's presence, or ErrNotFound when this server has never
	// reported any.
	Get(ctx context.Context, userID string) (*Presence, error)
	// Set stores a presence change and returns the stream position it took. Writing the
	// same user again replaces their current value: presence is a state, not a history.
	Set(ctx context.Context, userID, state, statusMsg string, active bool, nowMillis int64) (int64, error)
	// MarkActive refreshes when this server last saw the user, without taking a stream
	// position. Seeing somebody is not a change worth telling other users about.
	MarkActive(ctx context.Context, userID string, nowMillis int64) error
	// ListChangedFor returns the presence of the given users whose position is greater
	// than since and at most until, oldest first. An empty user list asks for nothing:
	// it is not treated as "everybody", because presence for users who share no room is
	// not this user's to see.
	ListChangedFor(ctx context.Context, userIDs []string, since, until int64) ([]*Presence, error)
}

// NewPresenceRepo returns the presence repository bound to db.
func NewPresenceRepo(db *gorm.DB) PresenceRepo { return &presenceRepo{db: db} }

type presenceRepo struct{ db *gorm.DB }

// WithTx implements [PresenceRepo].
func (r *presenceRepo) WithTx(tx *gorm.DB) PresenceRepo { return &presenceRepo{db: tx} }

// Get implements [PresenceRepo].
func (r *presenceRepo) Get(ctx context.Context, userID string) (*Presence, error) {
	var row Presence
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Set implements [PresenceRepo].
func (r *presenceRepo) Set(ctx context.Context, userID, state, statusMsg string, active bool, nowMillis int64) (int64, error) {
	next, err := nextStreamPosition(ctx, r.db)
	if err != nil {
		return 0, err
	}
	row := Presence{UserID: userID}
	err = r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Assign(Presence{
			State:          state,
			StatusMsg:      statusMsg,
			Active:         active,
			LastActiveTS:   nowMillis,
			StreamOrdering: next,
		}).
		FirstOrCreate(&row).Error
	if err != nil {
		return 0, err
	}
	return next, nil
}

// MarkActive implements [PresenceRepo].
//
// A row is created when there is none, because "this server saw this user" is a fact worth
// having even before they set a status. It takes no stream position on purpose: a user
// typing, reading or simply being alive is not news to anybody else, and giving every such
// moment a position would put a presence event in every sync.
func (r *presenceRepo) MarkActive(ctx context.Context, userID string, nowMillis int64) error {
	row := Presence{UserID: userID, State: PresenceOnline, Active: true, LastActiveTS: nowMillis}
	return r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Assign(Presence{Active: true, LastActiveTS: nowMillis}).
		FirstOrCreate(&row).Error
}

// ListChangedFor implements [PresenceRepo].
func (r *presenceRepo) ListChangedFor(ctx context.Context, userIDs []string, since, until int64) ([]*Presence, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	var rows []*Presence
	err := r.db.WithContext(ctx).
		Where("user_id IN ? AND stream_ordering > ? AND stream_ordering <= ?", userIDs, since, until).
		Order("stream_ordering ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// The presence values the Client-Server API defines. Anything else a client sends is
// refused rather than stored, because a stored value no client expects would be shown as
// nothing at all.
const (
	PresenceOnline      = "online"
	PresenceUnavailable = "unavailable"
	PresenceOffline     = "offline"
)

// PresenceKnown reports whether a presence value is one of those three.
func PresenceKnown(state string) bool {
	switch state {
	case PresenceOnline, PresenceUnavailable, PresenceOffline:
		return true
	default:
		return false
	}
}
