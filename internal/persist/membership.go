package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RoomMembership denormalises the current m.room.member state so membership
// queries (joined rooms, member lists, mutual rooms) stay cheap.
type RoomMembership struct {
	RoomID     string `json:"room_id" gorm:"uniqueIndex:uq_membership_room_user;size:255"`
	UserID     string `json:"user_id" gorm:"uniqueIndex:uq_membership_room_user;size:255"`
	Membership string `json:"membership" gorm:"index;size:16"`
	EventID    string `json:"event_id" gorm:"size:255"`
	SentTS     int64  `json:"sent_ts"`

	gorm.Model
}

// MembershipRepo is the contract for room membership storage access.
type MembershipRepo interface {
	Set(membership *RoomMembership) error
	Get(roomID, userID string) (*RoomMembership, error)
	// RoomsFor lists the rooms of a user whose membership is one of the given
	// values (all values when the list is empty).
	RoomsFor(userID string, memberships ...string) ([]string, error)
	// Members lists the members of a room whose membership is one of the given
	// values.
	Members(roomID string, memberships ...string) ([]*RoomMembership, error)
	Count(roomID string, memberships ...string) (int64, error)
	// MutualRooms lists rooms that both users have joined.
	MutualRooms(userID, otherUserID string) ([]string, error)
	Forget(roomID, userID string) error
	// DirectoryUsers lists distinct joined users of public rooms for the user
	// directory.
	DirectoryUsers(except string, limit int) ([]string, error)
}

// MembershipRepoImpl is the GORM backed MembershipRepo.
type MembershipRepoImpl struct {
	db *gorm.DB
}

// Set implements [MembershipRepo].
func (r *MembershipRepoImpl) Set(membership *RoomMembership) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "room_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"membership", "event_id", "sent_ts", "updated_at",
		}),
	}).Create(membership).Error
}

// Get implements [MembershipRepo].
func (r *MembershipRepoImpl) Get(roomID, userID string) (*RoomMembership, error) {
	var row RoomMembership
	err := r.db.Where("room_id = ? AND user_id = ?", roomID, userID).First(&row).Error
	return &row, err
}

// RoomsFor implements [MembershipRepo].
func (r *MembershipRepoImpl) RoomsFor(userID string, memberships ...string) ([]string, error) {
	var ids []string
	q := r.db.Model(&RoomMembership{}).Where("user_id = ?", userID)
	if len(memberships) > 0 {
		q = q.Where("membership IN ?", memberships)
	}
	err := q.Order("id asc").Pluck("room_id", &ids).Error
	return ids, err
}

// Members implements [MembershipRepo].
func (r *MembershipRepoImpl) Members(roomID string, memberships ...string) ([]*RoomMembership, error) {
	var rows []*RoomMembership
	q := r.db.Where("room_id = ?", roomID)
	if len(memberships) > 0 {
		q = q.Where("membership IN ?", memberships)
	}
	err := q.Order("id asc").Find(&rows).Error
	return rows, err
}

// Count implements [MembershipRepo].
func (r *MembershipRepoImpl) Count(roomID string, memberships ...string) (int64, error) {
	var n int64
	q := r.db.Model(&RoomMembership{}).Where("room_id = ?", roomID)
	if len(memberships) > 0 {
		q = q.Where("membership IN ?", memberships)
	}
	err := q.Count(&n).Error
	return n, err
}

// MutualRooms implements [MembershipRepo].
func (r *MembershipRepoImpl) MutualRooms(userID, otherUserID string) ([]string, error) {
	sub := r.db.Model(&RoomMembership{}).Select("room_id").
		Where("user_id = ? AND membership = ?", otherUserID, "join")
	var ids []string
	err := r.db.Model(&RoomMembership{}).
		Where("user_id = ? AND membership = ? AND room_id IN (?)", userID, "join", sub).
		Order("id asc").Pluck("room_id", &ids).Error
	return ids, err
}

// Forget implements [MembershipRepo]. The row is removed for real: a soft
// delete would keep holding the unique (room, user) index, so a later re-join
// would be swallowed by the conflict update and stay invisible.
func (r *MembershipRepoImpl) Forget(roomID, userID string) error {
	return r.db.Unscoped().Where("room_id = ? AND user_id = ?", roomID, userID).
		Delete(&RoomMembership{}).
		Error
}

// DirectoryUsers implements [MembershipRepo].
func (r *MembershipRepoImpl) DirectoryUsers(except string, limit int) ([]string, error) {
	sub := r.db.Model(&RoomVisibility{}).Select("room_id").
		Where("visibility = ?", "public")
	q := r.db.Model(&RoomMembership{}).
		Where("membership = ? AND room_id IN (?)", "join", sub)
	if except != "" {
		q = q.Where("user_id <> ?", except)
	}
	var ids []string
	err := q.Distinct().Order("user_id asc").Limit(limit).Pluck("user_id", &ids).Error
	return ids, err
}

// NewMembershipRepo returns the MembershipRepo implementation for db.
func NewMembershipRepo(db *gorm.DB) MembershipRepo {
	return &MembershipRepoImpl{db: db}
}
