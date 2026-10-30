package persist

import (
	"gorm.io/gorm"
)

// Report kinds. The client-server API has three report endpoints, and their
// reports share one table so administrators can list them in one place.
const (
	ReportKindEvent = "event"
	ReportKindRoom  = "room"
	ReportKindUser  = "user"
)

// Report is an abuse report as filed by a client. The client-server API stops
// at accepting the report, but a report that only reaches the server log is
// useless to whoever has to act on it, so the content is kept.
type Report struct {
	// Kind is one of ReportKindEvent, ReportKindRoom or ReportKindUser.
	Kind string `json:"kind" gorm:"index;size:16"`
	// RoomID and EventID are set for event reports; RoomID alone for room
	// reports. UserID is the subject of a user report, and the room of an event
	// or room report when the subject happens to be a user.
	RoomID   string `json:"room_id" gorm:"index;size:255"`
	EventID  string `json:"event_id" gorm:"index;size:255"`
	UserID   string `json:"user_id" gorm:"index;size:255"`
	Reporter string `json:"reporter" gorm:"index;size:255"`
	Reason   string `json:"reason" gorm:"type:text"`
	// Score mirrors the spec field: a negative number where smaller means
	// worse. Zero means the client did not send one.
	Score int64 `json:"score"`
	// CreatedTS is when the report arrived, in milliseconds since the epoch.
	CreatedTS int64 `json:"created_ts" gorm:"index"`
	// EventContent keeps the reported event as it looked at report time. A
	// moderator reading the report later should not have to trust the event,
	// which the sender may well have redacted by then.
	EventContent string `json:"event_content" gorm:"type:text"`

	gorm.Model
}

// ReportFilter narrows a report listing. Empty strings match anything.
type ReportFilter struct {
	Kind     string
	RoomID   string
	EventID  string
	UserID   string
	Reporter string
	Limit    int
	Offset   int
}

// ReportRepo is the contract for abuse report storage access.
type ReportRepo interface {
	Add(report *Report) error
	// List returns matching reports, newest first.
	List(filter ReportFilter) ([]*Report, error)
	// Count returns how many reports match the filter, ignoring the limit.
	Count(filter ReportFilter) (int64, error)
	// Get returns a single report by id.
	Get(id uint) (*Report, error)
}

// ReportRepoImpl is the GORM-backed implementation of ReportRepo.
type ReportRepoImpl struct {
	db *gorm.DB
}

// apply adds the filter conditions to a query.
func applyReportFilter(query *gorm.DB, filter ReportFilter) *gorm.DB {
	if filter.Kind != "" {
		query = query.Where("kind = ?", filter.Kind)
	}
	if filter.RoomID != "" {
		query = query.Where("room_id = ?", filter.RoomID)
	}
	if filter.EventID != "" {
		query = query.Where("event_id = ?", filter.EventID)
	}
	if filter.UserID != "" {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.Reporter != "" {
		query = query.Where("reporter = ?", filter.Reporter)
	}
	return query
}

// Add implements [ReportRepo].
func (r *ReportRepoImpl) Add(report *Report) error {
	return r.db.Create(report).Error
}

// List implements [ReportRepo].
func (r *ReportRepoImpl) List(filter ReportFilter) ([]*Report, error) {
	var out []*Report
	query := applyReportFilter(r.db.Model(&Report{}), filter).Order("created_ts DESC, id DESC")
	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		query = query.Offset(filter.Offset)
	}
	if err := query.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Count implements [ReportRepo].
func (r *ReportRepoImpl) Count(filter ReportFilter) (int64, error) {
	var n int64
	if err := applyReportFilter(r.db.Model(&Report{}), filter).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// Get implements [ReportRepo].
func (r *ReportRepoImpl) Get(id uint) (*Report, error) {
	var report Report
	if err := r.db.First(&report, id).Error; err != nil {
		return nil, err
	}
	return &report, nil
}

// NewReportRepo returns the ReportRepo implementation for db.
func NewReportRepo(db *gorm.DB) ReportRepo {
	return &ReportRepoImpl{db: db}
}
