package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Receipt is a read receipt of one user for a room, keyed so a newer receipt
// replaces the older one.
type Receipt struct {
	RoomID      string `json:"room_id" gorm:"uniqueIndex:uq_receipt_scope;size:255"`
	UserID      string `json:"user_id" gorm:"uniqueIndex:uq_receipt_scope;size:255"`
	ReceiptType string `json:"receipt_type" gorm:"uniqueIndex:uq_receipt_scope;size:32"`
	EventID     string `json:"event_id" gorm:"index;size:255"`
	StreamPos   uint64 `json:"stream_pos" gorm:"index"`
	SentTS      int64  `json:"ts"`
	Data        string `json:"-" gorm:"type:text"`

	gorm.Model
}

// ReceiptRepo is the contract for read receipt storage access.
type ReceiptRepo interface {
	Set(receipt *Receipt) error
	ListForRoom(roomID string, since uint64) ([]*Receipt, error)
	// ForRoom lists every stored receipt of a room, regardless of when it
	// changed, which is what the receipt listing endpoint reports.
	ForRoom(roomID string) ([]*Receipt, error)
	ListForUser(userID string, since uint64) ([]*Receipt, error)
	// ListForRooms lists the receipts of several rooms that changed after a position,
	// which is what a sync delivers: a client is told about everybody who read
	// something in a room it is in, not only about itself.
	ListForRooms(roomIDs []string, since uint64) ([]*Receipt, error)
	LatestForUser(userID, roomID, receiptType string) (*Receipt, error)
}

// ReceiptRepoImpl is the GORM backed ReceiptRepo.
type ReceiptRepoImpl struct {
	db *gorm.DB
}

// Set implements [ReceiptRepo]. A user keeps one receipt per room and type, so
// a newer receipt replaces the stored one.
func (r *ReceiptRepoImpl) Set(receipt *Receipt) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "room_id"}, {Name: "user_id"}, {Name: "receipt_type"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"event_id", "stream_pos", "sent_ts", "data", "updated_at",
		}),
	}).Create(receipt).Error
}

// ListForRoom implements [ReceiptRepo].
func (r *ReceiptRepoImpl) ListForRoom(roomID string, since uint64) ([]*Receipt, error) {
	var rows []*Receipt
	err := r.db.Where("room_id = ? AND stream_pos > ?", roomID, since).
		Order("stream_pos asc").Find(&rows).Error
	return rows, err
}

// ForRoom implements [ReceiptRepo].
func (r *ReceiptRepoImpl) ForRoom(roomID string) ([]*Receipt, error) {
	var rows []*Receipt
	err := r.db.Where("room_id = ?", roomID).Order("stream_pos asc").Find(&rows).Error
	return rows, err
}

// ListForUser implements [ReceiptRepo].
func (r *ReceiptRepoImpl) ListForUser(userID string, since uint64) ([]*Receipt, error) {
	var rows []*Receipt
	err := r.db.Where("user_id = ? AND stream_pos > ?", userID, since).
		Order("stream_pos asc").Find(&rows).Error
	return rows, err
}

// ListForRooms implements [ReceiptRepo].
func (r *ReceiptRepoImpl) ListForRooms(roomIDs []string, since uint64) ([]*Receipt, error) {
	if len(roomIDs) == 0 {
		return nil, nil
	}
	var rows []*Receipt
	err := r.db.Where("room_id IN ? AND stream_pos > ?", roomIDs, since).
		Order("stream_pos asc").Find(&rows).Error
	return rows, err
}

// LatestForUser implements [ReceiptRepo].
func (r *ReceiptRepoImpl) LatestForUser(userID, roomID, receiptType string) (*Receipt, error) {
	var row Receipt
	err := r.db.Where("room_id = ? AND user_id = ? AND receipt_type = ?",
		roomID, userID, receiptType).First(&row).Error
	return &row, err
}

// NewReceiptRepo returns the ReceiptRepo implementation for db.
func NewReceiptRepo(db *gorm.DB) ReceiptRepo {
	return &ReceiptRepoImpl{db: db}
}

// ReadMarker records the fully read marker and the read receipt of a user in a
// room, as set by /read_markers.
type ReadMarker struct {
	UserID             string `json:"user_id" gorm:"uniqueIndex:uq_readmarker_scope;size:255"`
	RoomID             string `json:"room_id" gorm:"uniqueIndex:uq_readmarker_scope;size:255"`
	FullyReadEventID   string `json:"fully_read" gorm:"size:255"`
	ReadReceiptEventID string `json:"read_receipt" gorm:"size:255"`

	gorm.Model
}

// ReadMarkerRepo is the contract for read marker storage access.
type ReadMarkerRepo interface {
	Set(marker *ReadMarker) error
	Get(userID, roomID string) (*ReadMarker, error)
}

// ReadMarkerRepoImpl is the GORM backed ReadMarkerRepo.
type ReadMarkerRepoImpl struct {
	db *gorm.DB
}

// Set implements [ReadMarkerRepo]. The (user, room) pair identifies the marker,
// so writing it twice updates the row instead of adding one.
func (r *ReadMarkerRepoImpl) Set(marker *ReadMarker) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "room_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"fully_read_event_id", "read_receipt_event_id", "updated_at",
		}),
	}).Create(marker).Error
}

// Get implements [ReadMarkerRepo].
func (r *ReadMarkerRepoImpl) Get(userID, roomID string) (*ReadMarker, error) {
	var marker ReadMarker
	err := r.db.Where("user_id = ? AND room_id = ?", userID, roomID).First(&marker).Error
	return &marker, err
}

// NewReadMarkerRepo returns the ReadMarkerRepo implementation for db.
func NewReadMarkerRepo(db *gorm.DB) ReadMarkerRepo {
	return &ReadMarkerRepoImpl{db: db}
}
