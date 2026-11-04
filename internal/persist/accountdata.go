package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AccountData stores global (RoomID empty) and room scoped account data.
type AccountData struct {
	UserID string `json:"user_id" gorm:"uniqueIndex:uq_accountdata_scope;size:255"`
	// RoomID is empty for global account data.
	RoomID  string `json:"room_id,omitempty" gorm:"uniqueIndex:uq_accountdata_scope;size:255"`
	Type    string `json:"type" gorm:"uniqueIndex:uq_accountdata_scope;size:255"`
	Content string `json:"content" gorm:"type:text"`
	// StreamPos records when the data changed so sync can report deltas.
	StreamPos uint64 `json:"stream_pos" gorm:"index"`

	gorm.Model
}

// AccountDataRepo is the contract for account data storage access.
type AccountDataRepo interface {
	Set(userID, roomID, typ, content string, streamPos uint64) error
	Get(userID, roomID, typ string) (*AccountData, error)
	List(userID, roomID string, since uint64) ([]*AccountData, error)
	ListGlobal(userID string, since uint64) ([]*AccountData, error)
	// RoomsForUser lists room ids for which the user has account data of a type.
	RoomTypes(userID, roomID string) (map[string]string, error)
	// ContentsForRooms returns the content of one account data type for each of the given
	// rooms, keyed by room id. The sliding sync tag filter asks which rooms a user has
	// tagged, and asks it of the whole list at once.
	ContentsForRooms(userID string, roomIDs []string, typ string) (map[string]string, error)
}

// AccountDataRepoImpl is the GORM backed AccountDataRepo.
type AccountDataRepoImpl struct {
	db *gorm.DB
}

// Set implements [AccountDataRepo].
func (r *AccountDataRepoImpl) Set(userID, roomID, typ, content string, streamPos uint64) error {
	row := AccountData{
		UserID:    userID,
		RoomID:    roomID,
		Type:      typ,
		Content:   content,
		StreamPos: streamPos,
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "room_id"}, {Name: "type"}},
		DoUpdates: clause.AssignmentColumns([]string{"content", "stream_pos", "updated_at"}),
	}).Create(&row).Error
}

// Get implements [AccountDataRepo].
func (r *AccountDataRepoImpl) Get(userID, roomID, typ string) (*AccountData, error) {
	var row AccountData
	err := r.db.Where("user_id = ? AND room_id = ? AND type = ?", userID, roomID, typ).
		First(&row).Error
	return &row, err
}

// List implements [AccountDataRepo] for a single room scope.
func (r *AccountDataRepoImpl) List(userID, roomID string, since uint64) ([]*AccountData, error) {
	var rows []*AccountData
	q := r.db.Where("user_id = ? AND room_id = ?", userID, roomID)
	if since > 0 {
		q = q.Where("stream_pos > ?", since)
	}
	err := q.Order("id asc").Find(&rows).Error
	return rows, err
}

// ListGlobal implements [AccountDataRepo].
func (r *AccountDataRepoImpl) ListGlobal(userID string, since uint64) ([]*AccountData, error) {
	return r.List(userID, "", since)
}

// RoomTypes implements [AccountDataRepo], returning type -> content for a room.
func (r *AccountDataRepoImpl) RoomTypes(userID, roomID string) (map[string]string, error) {
	rows, err := r.List(userID, roomID, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Type] = row.Content
	}
	return out, nil
}

// ContentsForRooms implements [AccountDataRepo].
func (r *AccountDataRepoImpl) ContentsForRooms(userID string, roomIDs []string, typ string) (map[string]string, error) {
	out := map[string]string{}
	if len(roomIDs) == 0 {
		return out, nil
	}
	var rows []*AccountData
	if err := r.db.Where("user_id = ? AND room_id IN ? AND type = ?", userID, roomIDs, typ).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.RoomID] = row.Content
	}
	return out, nil
}

// NewAccountDataRepo returns the AccountDataRepo implementation for db.
func NewAccountDataRepo(db *gorm.DB) AccountDataRepo {
	return &AccountDataRepoImpl{db: db}
}
