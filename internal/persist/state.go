package persist

import (
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RoomState is the current state of a room: one row per (room, type,
// state_key). It points at the event that currently holds that state.
type RoomState struct {
	RoomID    string `json:"room_id" gorm:"uniqueIndex:uq_state_room_type_key;size:255"`
	Type      string `json:"type" gorm:"uniqueIndex:uq_state_room_type_key;size:128"`
	StateKey  string `json:"state_key" gorm:"uniqueIndex:uq_state_room_type_key;size:255"`
	EventID   string `json:"event_id" gorm:"index;size:255"`
	StreamPos uint64 `json:"stream_pos"`
	Sender    string `json:"sender" gorm:"size:255"`
	Content   string `json:"content" gorm:"type:text"`

	gorm.Model
}

// StateRepo is the contract for current room state storage access.
type StateRepo interface {
	Upsert(state *RoomState) error
	Get(roomID, typ, stateKey string) (*RoomState, error)
	Content(roomID, typ, stateKey string) (matrix.RawMessage, error)
	// ForRooms answers the same state question of many rooms at once, keyed by room id. A
	// sliding sync filter asks it of every room a user is in, and walking those rooms one
	// at a time would make the endpoint's least interesting step its slowest.
	ForRooms(roomIDs []string, typ, stateKey string) (map[string]*RoomState, error)
	// SpaceChildren returns the room ids that the given rooms name in an m.space.child
	// event. The sliding sync space filter stops here on purpose: the MSC forbids walking
	// into subspaces, so it is the client's job to list them.
	SpaceChildren(spaceIDs []string) (map[string]bool, error)
	List(roomID string, types []string, notSentBy string) ([]*RoomState, error)
	Events(roomID string, types []string) ([]*Event, error)
	MemberEvents(roomID string) ([]*Event, error)
	Delete(roomID, typ, stateKey string) error
	JoinRule(roomID string) (string, error)
}

// StateRepoImpl is the GORM backed StateRepo.
type StateRepoImpl struct {
	db *gorm.DB
}

// Upsert implements [StateRepo].
func (r *StateRepoImpl) Upsert(state *RoomState) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "room_id"}, {Name: "type"}, {Name: "state_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"event_id", "stream_pos", "sender", "content", "updated_at",
		}),
	}).Create(state).Error
}

// Get implements [StateRepo].
func (r *StateRepoImpl) Get(roomID, typ, stateKey string) (*RoomState, error) {
	var state RoomState
	err := r.db.Where("room_id = ? AND type = ? AND state_key = ?", roomID, typ, stateKey).
		First(&state).Error
	return &state, err
}

// ForRooms implements [StateRepo].
func (r *StateRepoImpl) ForRooms(roomIDs []string, typ, stateKey string) (map[string]*RoomState, error) {
	out := map[string]*RoomState{}
	if len(roomIDs) == 0 {
		return out, nil
	}
	var rows []*RoomState
	if err := r.db.Where("room_id IN ? AND type = ? AND state_key = ?", roomIDs, typ, stateKey).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.RoomID] = row
	}
	return out, nil
}

// SpaceChildren implements [StateRepo].
func (r *StateRepoImpl) SpaceChildren(spaceIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(spaceIDs) == 0 {
		return out, nil
	}
	var rows []*RoomState
	if err := r.db.Where("room_id IN ? AND type = ?", spaceIDs, "m.space.child").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		// A child whose event has been redacted is no longer in the space: the state
		// row is kept with an empty content, and the emptiness is the leaving.
		if strings.TrimSpace(row.Content) == "" || row.Content == "{}" {
			continue
		}
		out[row.StateKey] = true
	}
	return out, nil
}

// Content implements [StateRepo]. A missing state key yields a not found error.
func (r *StateRepoImpl) Content(roomID, typ, stateKey string) (matrix.RawMessage, error) {
	state, err := r.Get(roomID, typ, stateKey)
	if err != nil {
		return nil, err
	}
	return matrix.RawMessage(state.Content), nil
}

// List implements [StateRepo].
func (r *StateRepoImpl) List(roomID string, types []string, notSentBy string) ([]*RoomState, error) {
	var rows []*RoomState
	q := r.db.Where("room_id = ?", roomID)
	if len(types) > 0 {
		q = q.Where("type IN ?", types)
	}
	if notSentBy != "" {
		q = q.Where("sender <> ?", notSentBy)
	}
	err := q.Order("id asc").Find(&rows).Error
	return rows, err
}

// Events implements [StateRepo] by joining the state rows onto their events.
func (r *StateRepoImpl) Events(roomID string, types []string) ([]*Event, error) {
	rows, err := r.List(roomID, types, "")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.EventID)
	}
	return NewEventRepo(r.db).ListByIDs(ids)
}

// MemberEvents implements [StateRepo].
func (r *StateRepoImpl) MemberEvents(roomID string) ([]*Event, error) {
	return r.Events(roomID, []string{matrix.TypeMember})
}

// Delete implements [StateRepo].
func (r *StateRepoImpl) Delete(roomID, typ, stateKey string) error {
	return r.db.Where("room_id = ? AND type = ? AND state_key = ?", roomID, typ, stateKey).
		Unscoped().Delete(&RoomState{}).Error
}

// JoinRule implements [StateRepo]. Defaults to invite when unknown.
func (r *StateRepoImpl) JoinRule(roomID string) (string, error) {
	raw, err := r.Content(roomID, matrix.TypeJoinRules, "")
	if err != nil {
		if IsNotFound(err) {
			return matrix.JoinRulesInvite, nil
		}
		return "", err
	}
	var c matrix.JoinRulesContent
	if err := matrix.DecodeContent(raw, &c); err != nil {
		return "", err
	}
	if c.JoinRule == "" {
		return matrix.JoinRulesInvite, nil
	}
	return c.JoinRule, nil
}

// NewStateRepo returns the StateRepo implementation for db.
func NewStateRepo(db *gorm.DB) StateRepo {
	return &StateRepoImpl{db: db}
}
