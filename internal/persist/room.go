package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Room is a Matrix room hosted by this homeserver.
type Room struct {
	// ID is the full room ID, e.g. "!abc:example.com".
	ID string `json:"room_id" gorm:"uniqueIndex;size:255"`
	// Creator is the user ID of the room creator.
	Creator string `json:"creator" gorm:"index;size:255"`
	// RoomVersion is the room version in effect, e.g. "11".
	RoomVersion string `json:"room_version" gorm:"size:32"`
	// JoinRule mirrors m.room.join_rules so the public room directory can be
	// queried without decoding state.
	JoinRule string `json:"join_rule" gorm:"size:32"`

	gorm.Model
}

// RoomRepo is the contract for room storage access.
type RoomRepo interface {
	Create(room *Room) error
	Get(id string) (*Room, error)
	Exists(id string) (bool, error)
	Save(room *Room) error
	ListByIDs(ids []string) ([]*Room, error)
}

// RoomRepoImpl is the GORM backed RoomRepo.
type RoomRepoImpl struct {
	db *gorm.DB
}

// Create implements [RoomRepo].
func (r *RoomRepoImpl) Create(room *Room) error {
	return r.db.Create(room).Error
}

// Get implements [RoomRepo].
func (r *RoomRepoImpl) Get(id string) (*Room, error) {
	var room Room
	err := r.db.Where("id = ?", id).First(&room).Error
	return &room, err
}

// Exists implements [RoomRepo].
func (r *RoomRepoImpl) Exists(id string) (bool, error) {
	var n int64
	err := r.db.Model(&Room{}).Where("id = ?", id).Count(&n).Error
	return n > 0, err
}

// Save implements [RoomRepo].
func (r *RoomRepoImpl) Save(room *Room) error {
	return r.db.Save(room).Error
}

// ListByIDs implements [RoomRepo].
func (r *RoomRepoImpl) ListByIDs(ids []string) ([]*Room, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rooms []*Room
	err := r.db.Where("id IN ?", ids).Find(&rooms).Error
	return rooms, err
}

// NewRoomRepo returns the RoomRepo implementation for db.
func NewRoomRepo(db *gorm.DB) RoomRepo {
	return &RoomRepoImpl{db: db}
}

// RoomAlias maps a room alias to a room ID.
type RoomAlias struct {
	Alias   string `json:"alias" gorm:"uniqueIndex;size:255"`
	RoomID  string `json:"room_id" gorm:"index;size:255"`
	Creator string `json:"creator" gorm:"size:255"`
	Server  string `json:"server" gorm:"size:255"`

	gorm.Model
}

// AliasRepo is the contract for room alias storage access.
type AliasRepo interface {
	Create(alias *RoomAlias) error
	Get(alias string) (*RoomAlias, error)
	Delete(alias string) error
	ListForRoom(roomID string) ([]*RoomAlias, error)
	List(limit, since int64, filter string) ([]*RoomAlias, int64, error)
}

// AliasRepoImpl is the GORM backed AliasRepo.
type AliasRepoImpl struct {
	db *gorm.DB
}

// Create implements [AliasRepo].
func (r *AliasRepoImpl) Create(alias *RoomAlias) error {
	return r.db.Create(alias).Error
}

// Get implements [AliasRepo].
func (r *AliasRepoImpl) Get(alias string) (*RoomAlias, error) {
	var row RoomAlias
	err := r.db.Where("alias = ?", alias).First(&row).Error
	return &row, err
}

// Delete implements [AliasRepo]. The row is dropped for real, so a removed
// alias can be pointing at a room again later.
func (r *AliasRepoImpl) Delete(alias string) error {
	return r.db.Unscoped().Where("alias = ?", alias).Delete(&RoomAlias{}).Error
}

// ListForRoom implements [AliasRepo].
func (r *AliasRepoImpl) ListForRoom(roomID string) ([]*RoomAlias, error) {
	var rows []*RoomAlias
	err := r.db.Where("room_id = ?", roomID).Order("id asc").Find(&rows).Error
	return rows, err
}

// List implements [AliasRepo], paginated by row id.
func (r *AliasRepoImpl) List(limit, since int64, filter string) ([]*RoomAlias, int64, error) {
	var rows []*RoomAlias
	q := r.db.Order("id asc")
	if since > 0 {
		q = q.Where("id > ?", since)
	}
	if filter != "" {
		q = q.Where("alias LIKE ?", "%"+filter+"%")
	}
	if limit > 0 {
		q = q.Limit(int(limit))
	}
	err := q.Find(&rows).Error
	return rows, 0, err
}

// NewAliasRepo returns the AliasRepo implementation for db.
func NewAliasRepo(db *gorm.DB) AliasRepo {
	return &AliasRepoImpl{db: db}
}

// RoomVisibility records whether a room is published in the room directory.
type RoomVisibility struct {
	RoomID     string `json:"room_id" gorm:"uniqueIndex;size:255"`
	Visibility string `json:"visibility" gorm:"size:32"`

	gorm.Model
}

// VisibilityRepo is the contract for room directory visibility storage access.
type VisibilityRepo interface {
	Set(roomID, visibility string) error
	Get(roomID string) (string, error)
	Published(limit, offset int, filter string) ([]*Room, int64, error)
}

// VisibilityRepoImpl is the GORM backed VisibilityRepo.
type VisibilityRepoImpl struct {
	db *gorm.DB
}

// Set implements [VisibilityRepo].
func (r *VisibilityRepoImpl) Set(roomID, visibility string) error {
	row := RoomVisibility{RoomID: roomID, Visibility: visibility}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "room_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"visibility"}),
	}).Create(&row).Error
}

// Get implements [VisibilityRepo]. Unpublished rooms default to private.
func (r *VisibilityRepoImpl) Get(roomID string) (string, error) {
	var row RoomVisibility
	err := r.db.Where("room_id = ?", roomID).First(&row).Error
	if err != nil {
		if IsNotFound(err) {
			return "private", nil
		}
		return "", err
	}
	return row.Visibility, nil
}

// Published implements [VisibilityRepo].
func (r *VisibilityRepoImpl) Published(limit, offset int, filter string) ([]*Room, int64, error) {
	q := r.db.Model(&RoomVisibility{}).Where("visibility = ?", "public")
	if filter != "" {
		q = q.Where("room_id IN (?)", r.db.Model(&RoomState{}).
			Select("room_id").Where("type = ? AND content LIKE ?", "m.room.name", "%"+filter+"%"))
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var ids []string
	if err := q.Order("id asc").Offset(offset).Limit(limit).
		Pluck("room_id", &ids).Error; err != nil {
		return nil, 0, err
	}
	rooms, err := NewRoomRepo(r.db).ListByIDs(ids)
	return rooms, total, err
}

// NewVisibilityRepo returns the VisibilityRepo implementation for db.
func NewVisibilityRepo(db *gorm.DB) VisibilityRepo {
	return &VisibilityRepoImpl{db: db}
}
