package persist

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Room is a room this server knows about: either created locally or joined from a
// federated one. The room version is stored here because every other decision —
// authorisation rules, redaction profile, event ID format, state resolution — is
// taken from it, and a room never changes version except by being replaced.
type Room struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	RoomID       string `gorm:"column:room_id;size:255;not null"`
	RoomVersion  string `gorm:"column:room_version;size:32;not null"`
	Creator      string `gorm:"column:creator;size:255;not null"`
	CreatedTS    int64  `gorm:"column:created_ts;not null;default:0"`
	LatestStream int64  `gorm:"column:latest_stream;not null;default:0"`
	// Published says whether the room is in this server's public room directory.
	// Publishing is a decision someone with standing in the room makes; it is not
	// derived from the join rule, because a room can be joinable by anyone and still
	// not want to be listed.
	Published bool `gorm:"column:published;not null;default:false"`
	// Name and Topic cache the text the room is listed under so the directory can be
	// searched without loading the state of every published room. They are rewritten
	// whenever that state is written; the state event stays the authority.
	Name      string `gorm:"column:name;size:255;not null;default:''"`
	Topic     string `gorm:"column:topic;size:255;not null;default:''"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Event is a persisted PDU.
//
// The canonical JSON is stored verbatim in JSON: it is what the content hash and
// the reference hash are computed over, and what peers compare signatures against.
// Reconstructing an event from columns and re-encoding it would change those bytes
// and invalidate the signatures, so the columns exist only for lookup.
type Event struct {
	ID      uint64 `gorm:"primaryKey;autoIncrement"`
	EventID string `gorm:"column:event_id;size:255;not null"`
	RoomID  string `gorm:"column:room_id;size:255;not null"`
	Type    string `gorm:"column:type;size:255;not null"`
	// StateKey is NULL for a non-state event and the empty string for a state
	// event whose state key is empty (m.room.create, m.room.join_rules, ...).
	// Collapsing those two would make an unstate event look like a state event.
	StateKey         *string `gorm:"column:state_key;size:255"`
	Sender           string  `gorm:"column:sender;size:255;not null"`
	Depth            int64   `gorm:"column:depth;not null;default:0"`
	OriginServerTS   int64   `gorm:"column:origin_server_ts;not null;default:0"`
	ReceivedTS       int64   `gorm:"column:received_ts;not null;default:0"`
	JSON             []byte  `gorm:"column:json;not null"`
	Redacted         bool    `gorm:"column:redacted;not null;default:false"`
	RedactedJSON     []byte  `gorm:"column:redacted_json"`
	Outlier          bool    `gorm:"column:outlier;not null;default:false"`
	SoftFailed       bool    `gorm:"column:soft_failed;not null;default:false"`
	StreamOrdering   int64   `gorm:"column:stream_ordering;not null;default:0"`
	ReceivedAtServer time.Time
}

// IsState reports whether the event is a state event, using the NULL-versus-empty
// distinction the schema preserves.
func (e *Event) IsState() bool { return e.StateKey != nil }

// TableName pins the table name instead of relying on GORM's inflection of
// "state", which is not a countable noun.
func (RoomState) TableName() string { return "room_state" }

// TableName pins the table name for the same reason.
func (EventAuthContext) TableName() string { return "event_auth_context" }

// TableName pins the table name for the same reason.
func (EventTxn) TableName() string { return "event_txn" }

// EventEdge records a reference from one event to another: `prev` for the event
// graph and `auth` for the authorisation chain. Keeping references as rows is what
// makes history a graph walk instead of an insertion-order scan.
//
// There is deliberately no foreign key on ReferenceID: Matrix servers legitimately
// hold events whose predecessors or auth events they do not have yet (a failed
// backfill is normal, not corruption), and a constraint would reject valid state.
type EventEdge struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	EventID     string `gorm:"column:event_id;size:255;not null"`
	ReferenceID string `gorm:"column:reference_id;size:255;not null"`
	Kind        string `gorm:"column:kind;size:8;not null"`
}

// Edge kinds.
const (
	EdgePrev = "prev"
	EdgeAuth = "auth"
)

// RoomState is the current state of a room, keyed the way Matrix state is keyed.
// It is stored separately from the per-event snapshots so that "what is the state
// now" does not require a state resolution over history.
type RoomState struct {
	ID       uint64 `gorm:"primaryKey;autoIncrement"`
	RoomID   string `gorm:"column:room_id;size:255;not null"`
	Type     string `gorm:"column:type;size:255;not null"`
	StateKey string `gorm:"column:state_key;size:255;not null"`
	EventID  string `gorm:"column:event_id;size:255;not null"`
}

// EventAuthContext records the state that authorised an event. Persisting it means
// re-authorisation and federation never have to recompute the past, and an event
// received later can be checked against the state as it was then.
type EventAuthContext struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	EventID      string `gorm:"column:event_id;size:255;not null"`
	StateEventID string `gorm:"column:state_event_id;size:255;not null"`
}

// EventTxn maps a client transaction ID to the event it produced, which is what
// makes `PUT /send/{type}/{txn}` idempotent per device.
type EventTxn struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	DeviceID  string `gorm:"column:device_id;size:255;not null"`
	TxnID     string `gorm:"column:txn_id;size:255;not null"`
	EventID   string `gorm:"column:event_id;size:255;not null"`
	CreatedAt time.Time
}

// asConflict maps a uniqueness violation to ErrConflict, so callers can react to
// deduplication without matching driver error strings.
func asConflict(err error) error {
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// RoomRepo persists rooms.
type RoomRepo interface {
	WithTx(tx *gorm.DB) RoomRepo
	Create(ctx context.Context, room *Room) error
	ByRoomID(ctx context.Context, roomID string) (*Room, error)
	// SetLatestStream advances the room's read position. It is separate from Create
	// so a room's identity is written once and its position changes on its own.
	SetLatestStream(ctx context.Context, roomID string, stream int64) error

	// SetPublished, SetDirectoryText and ListPublished are the room directory: which
	// rooms this server advertises, and what it advertises about them.
	SetPublished(ctx context.Context, roomID string, published bool) error
	SetDirectoryText(ctx context.Context, roomID, name, topic string) error
	ListPublished(ctx context.Context, search string, offset, limit int) ([]*Room, int64, error)
}

type roomRepo struct{ db *gorm.DB }

// NewRoomRepo returns the room repository.
func NewRoomRepo(db *gorm.DB) RoomRepo { return &roomRepo{db: db} }

// WithTx implements [RoomRepo].
func (r *roomRepo) WithTx(tx *gorm.DB) RoomRepo { return &roomRepo{db: tx} }

// Create implements [RoomRepo].
func (r *roomRepo) Create(ctx context.Context, room *Room) error {
	if err := r.db.WithContext(ctx).Create(room).Error; err != nil {
		return asConflict(err)
	}
	return nil
}

// ByRoomID implements [RoomRepo].
func (r *roomRepo) ByRoomID(ctx context.Context, roomID string) (*Room, error) {
	var room Room
	err := r.db.WithContext(ctx).Where("room_id = ?", roomID).First(&room).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &room, nil
}

// SetLatestStream implements [RoomRepo].
func (r *roomRepo) SetLatestStream(ctx context.Context, roomID string, stream int64) error {
	res := r.db.WithContext(ctx).Model(&Room{}).Where("room_id = ?", roomID).
		Update("latest_stream", stream)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// EventRepo persists events and the reference edges between them.
type EventRepo interface {
	WithTx(tx *gorm.DB) EventRepo

	Insert(ctx context.Context, event *Event) error
	ByID(ctx context.Context, eventID string) (*Event, error)
	ByIDs(ctx context.Context, eventIDs []string) ([]*Event, error)

	// Link records a reference edge. Duplicate links are ignored: the same event may
	// legitimately be referenced by many events, and re-linking is not an error.
	Link(ctx context.Context, eventID, referenceID, kind string) error
	ReferencedBy(ctx context.Context, eventID, kind string) ([]string, error)
	References(ctx context.Context, eventID, kind string) ([]string, error)

	// EventsAfter, CountAfter, LatestEvents and MaxStreamOrdering answer client
	// synchronisation questions in stream order; see sync.go for why stream order is the
	// right ordering for that question and not for anything about the event graph.
	EventsAfter(ctx context.Context, roomID string, after int64, limit int) ([]*Event, error)
	// EventsWindow pages a room's timeline in either direction from a stream position,
	// for `/messages`.
	EventsWindow(ctx context.Context, roomID string, from int64, backwards bool, limit int) ([]*Event, error)
	CountAfter(ctx context.Context, roomID string, after int64) (int64, error)
	LatestEvents(ctx context.Context, roomID string, limit int) ([]*Event, error)
	MaxStreamOrdering(ctx context.Context) (int64, error)

	// ForwardExtremities returns the event IDs in a room that no other stored event
	// cites as a predecessor. New events attach here.
	ForwardExtremities(ctx context.Context, roomID string) ([]string, error)

	// NextStreamOrdering returns a fresh global ordering number. Callers must hold a
	// transaction, so concurrent senders cannot take the same number.
	NextStreamOrdering(ctx context.Context) (int64, error)

	// TxnLookup returns the event a device's transaction ID already produced.
	TxnLookup(ctx context.Context, deviceID, txnID string) (*EventTxn, error)
	TxnSave(ctx context.Context, txn *EventTxn) error
}

type eventRepo struct{ db *gorm.DB }

// NewEventRepo returns the event repository.
func NewEventRepo(db *gorm.DB) EventRepo { return &eventRepo{db: db} }

// WithTx implements [EventRepo].
func (r *eventRepo) WithTx(tx *gorm.DB) EventRepo { return &eventRepo{db: tx} }

// Insert implements [EventRepo]. A duplicate event ID is reported as ErrConflict,
// which is how event deduplication works: the same event arriving twice (over
// federation, or from a retrying client) is not an error at the protocol level but
// must not create a second row.
func (r *eventRepo) Insert(ctx context.Context, event *Event) error {
	if err := r.db.WithContext(ctx).Create(event).Error; err != nil {
		return asConflict(err)
	}
	return nil
}

// ByID implements [EventRepo].
func (r *eventRepo) ByID(ctx context.Context, eventID string) (*Event, error) {
	var event Event
	err := r.db.WithContext(ctx).Where("event_id = ?", eventID).First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// ByIDs implements [EventRepo]. Missing IDs are simply absent from the result: a
// caller asking for an auth chain has to cope with gaps, and turning a gap into an
// error would make partial state unreadable.
func (r *eventRepo) ByIDs(ctx context.Context, eventIDs []string) ([]*Event, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	var events []*Event
	if err := r.db.WithContext(ctx).Where("event_id IN ?", eventIDs).Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// Link implements [EventRepo].
func (r *eventRepo) Link(ctx context.Context, eventID, referenceID, kind string) error {
	edge := EventEdge{EventID: eventID, ReferenceID: referenceID, Kind: kind}
	err := r.db.WithContext(ctx).
		Where("event_id = ? AND reference_id = ? AND kind = ?", eventID, referenceID, kind).
		FirstOrCreate(&edge).Error
	if err != nil {
		return asConflict(err)
	}
	return nil
}

// ReferencedBy implements [EventRepo]: which events cite eventID as a reference of
// the given kind.
func (r *eventRepo) ReferencedBy(ctx context.Context, eventID, kind string) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&EventEdge{}).
		Where("reference_id = ? AND kind = ?", eventID, kind).
		Pluck("event_id", &ids).Error
	return ids, err
}

// References implements [EventRepo]: the events eventID cites.
func (r *eventRepo) References(ctx context.Context, eventID, kind string) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&EventEdge{}).
		Where("event_id = ? AND kind = ?", eventID, kind).
		Pluck("reference_id", &ids).Error
	return ids, err
}

// ForwardExtremities implements [EventRepo].
//
// Soft-failed events are excluded: a soft-failed event is kept for reference but is
// not part of the accepted history, so attaching to it would build new history on a
// branch the server has already decided not to use.
func (r *eventRepo) ForwardExtremities(ctx context.Context, roomID string) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&Event{}).
		Where("room_id = ? AND soft_failed = 0 AND outlier = 0", roomID).
		Where("NOT EXISTS (SELECT 1 FROM event_edges e WHERE e.reference_id = events.event_id AND e.kind = ?)", EdgePrev).
		Order("stream_ordering DESC").
		Pluck("event_id", &ids).Error
	return ids, err
}

// NextStreamOrdering implements [EventRepo].
func (r *eventRepo) NextStreamOrdering(ctx context.Context) (int64, error) {
	var next int64
	if err := r.db.WithContext(ctx).Model(&Event{}).
		Select("COALESCE(MAX(stream_ordering), 0) + 1").Scan(&next).Error; err != nil {
		return 0, err
	}
	return next, nil
}

// TxnLookup implements [EventRepo].
func (r *eventRepo) TxnLookup(ctx context.Context, deviceID, txnID string) (*EventTxn, error) {
	var txn EventTxn
	err := r.db.WithContext(ctx).Where("device_id = ? AND txn_id = ?", deviceID, txnID).First(&txn).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &txn, nil
}

// TxnSave implements [EventRepo]. A repeat of the same (device, txn) pair is
// ErrConflict so the caller can return the original event instead of creating a
// second one.
func (r *eventRepo) TxnSave(ctx context.Context, txn *EventTxn) error {
	if err := r.db.WithContext(ctx).Create(txn).Error; err != nil {
		return asConflict(err)
	}
	return nil
}

// StateRepo persists current room state.
type StateRepo interface {
	WithTx(tx *gorm.DB) StateRepo

	// Set replaces the state entry for (room, type, state_key). Matrix state is a
	// map, so a new event for the same key supersedes the old one.
	Set(ctx context.Context, state *RoomState) error
	Get(ctx context.Context, roomID, eventType, stateKey string) (*RoomState, error)
	All(ctx context.Context, roomID string) ([]*RoomState, error)

	// ByType returns the current state entries of one event type in a room, which is
	// how a member list is read: membership is state, one entry per user.
	ByType(ctx context.Context, roomID, eventType string) ([]*RoomState, error)

	// MembershipEventsFor returns the same entries as MembershipsOf with their events
	// loaded, so a caller can read both the membership and when it changed.
	MembershipEventsFor(ctx context.Context, userID string) ([]*Event, error)

	// MembershipsOf returns the current m.room.member state entries that name a user,
	// across every room. It backs "which rooms is this user in" without scanning all
	// state.
	MembershipsOf(ctx context.Context, userID string) ([]*RoomState, error)

	// RememberAuthContext records the state that authorised an event.
	RememberAuthContext(ctx context.Context, eventID string, stateEventIDs []string) error
	AuthContext(ctx context.Context, eventID string) ([]string, error)
}

type stateRepo struct{ db *gorm.DB }

// NewStateRepo returns the room state repository.
func NewStateRepo(db *gorm.DB) StateRepo { return &stateRepo{db: db} }

// WithTx implements [StateRepo].
func (s *stateRepo) WithTx(tx *gorm.DB) StateRepo { return &stateRepo{db: tx} }

// Set implements [StateRepo].
func (s *stateRepo) Set(ctx context.Context, state *RoomState) error {
	var existing RoomState
	err := s.db.WithContext(ctx).
		Where("room_id = ? AND type = ? AND state_key = ?", state.RoomID, state.Type, state.StateKey).
		First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := s.db.WithContext(ctx).Create(state).Error; err != nil {
			return asConflict(err)
		}
		return nil
	}
	existing.EventID = state.EventID
	if err := s.db.WithContext(ctx).Model(&existing).Update("event_id", state.EventID).Error; err != nil {
		return err
	}
	return nil
}

// Get implements [StateRepo].
func (s *stateRepo) Get(ctx context.Context, roomID, eventType, stateKey string) (*RoomState, error) {
	var state RoomState
	err := s.db.WithContext(ctx).
		Where("room_id = ? AND type = ? AND state_key = ?", roomID, eventType, stateKey).
		First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// All implements [StateRepo].
func (s *stateRepo) All(ctx context.Context, roomID string) ([]*RoomState, error) {
	var states []*RoomState
	if err := s.db.WithContext(ctx).Where("room_id = ?", roomID).Find(&states).Error; err != nil {
		return nil, err
	}
	return states, nil
}

// ByType implements [StateRepo].
func (s *stateRepo) ByType(ctx context.Context, roomID, eventType string) ([]*RoomState, error) {
	var states []*RoomState
	if err := s.db.WithContext(ctx).
		Where("room_id = ? AND type = ?", roomID, eventType).
		Order("state_key").Find(&states).Error; err != nil {
		return nil, err
	}
	return states, nil
}

// MembershipsOf implements [StateRepo].
func (s *stateRepo) MembershipsOf(ctx context.Context, userID string) ([]*RoomState, error) {
	var states []*RoomState
	if err := s.db.WithContext(ctx).
		Where("type = ? AND state_key = ?", "m.room.member", userID).
		Order("room_id").Find(&states).Error; err != nil {
		return nil, err
	}
	return states, nil
}

// RememberAuthContext implements [StateRepo]. Replacing the whole set keeps the
// snapshot consistent with the event: an auth context is a property of the event,
// not something that drifts.
func (s *stateRepo) RememberAuthContext(ctx context.Context, eventID string, stateEventIDs []string) error {
	if err := s.db.WithContext(ctx).
		Where("event_id = ?", eventID).Delete(&EventAuthContext{}).Error; err != nil {
		return err
	}
	if len(stateEventIDs) == 0 {
		return nil
	}
	rows := make([]EventAuthContext, 0, len(stateEventIDs))
	for _, id := range stateEventIDs {
		rows = append(rows, EventAuthContext{EventID: eventID, StateEventID: id})
	}
	if err := s.db.WithContext(ctx).Create(&rows).Error; err != nil {
		return asConflict(err)
	}
	return nil
}

// AuthContext implements [StateRepo].
func (s *stateRepo) AuthContext(ctx context.Context, eventID string) ([]string, error) {
	var ids []string
	err := s.db.WithContext(ctx).Model(&EventAuthContext{}).
		Where("event_id = ?", eventID).Pluck("state_event_id", &ids).Error
	return ids, err
}
