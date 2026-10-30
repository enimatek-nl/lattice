package persist

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PushRule is one rule of a user's push rule set. Kind is one of override,
// content, room, sender or underride.
type PushRule struct {
	UserID     string `json:"user_id" gorm:"uniqueIndex:uq_pushrule_scope;size:255"`
	Kind       string `json:"kind" gorm:"uniqueIndex:uq_pushrule_scope;size:32"`
	RuleID     string `json:"rule_id" gorm:"uniqueIndex:uq_pushrule_scope;size:255"`
	Enabled    bool   `json:"enabled"`
	Default    bool   `json:"default"`
	Actions    string `json:"actions" gorm:"type:text"`
	Conditions string `json:"conditions,omitempty" gorm:"type:text"`
	// Positions keeps the ordering of rules within a kind.
	Positions int64 `json:"-"`

	gorm.Model
}

// PushRuleRepo is the contract for push rule storage access.
type PushRuleRepo interface {
	Set(rule *PushRule) error
	Get(userID, kind, ruleID string) (*PushRule, error)
	List(userID, kind string) ([]*PushRule, error)
	Delete(userID, kind, ruleID string) error
	SetEnabled(userID, kind, ruleID string, enabled bool) error
	SetActions(userID, kind, ruleID, actions string) error
	// Order rewrites the positions of the rules of one kind so that they follow
	// the supplied rule id order. Ids that are not listed keep their relative
	// order at the end of the kind.
	Order(userID, kind string, ruleIDs []string) error
	Count(userID string) (int64, error)
}

// PushRuleRepoImpl is the GORM backed PushRuleRepo.
type PushRuleRepoImpl struct {
	db *gorm.DB
}

// Set implements [PushRuleRepo].
func (r *PushRuleRepoImpl) Set(rule *PushRule) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "kind"}, {Name: "rule_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"enabled", "actions", "conditions", "default", "updated_at",
		}),
	}).Create(rule).Error
}

// Get implements [PushRuleRepo].
func (r *PushRuleRepoImpl) Get(userID, kind, ruleID string) (*PushRule, error) {
	var rule PushRule
	err := r.db.Where("user_id = ? AND kind = ? AND rule_id = ?", userID, kind, ruleID).
		First(&rule).Error
	return &rule, err
}

// List implements [PushRuleRepo].
func (r *PushRuleRepoImpl) List(userID, kind string) ([]*PushRule, error) {
	var rules []*PushRule
	q := r.db.Where("user_id = ?", userID)
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	err := q.Order("positions asc, id asc").Find(&rules).Error
	return rules, err
}

// Delete implements [PushRuleRepo]. The row is dropped for real: a soft delete
// would keep occupying the unique (user, kind, rule) index, which stops the
// same rule id from being created again.
func (r *PushRuleRepoImpl) Delete(userID, kind, ruleID string) error {
	return r.db.Unscoped().Where("user_id = ? AND kind = ? AND rule_id = ?", userID, kind, ruleID).
		Delete(&PushRule{}).
		Error
}

// SetEnabled implements [PushRuleRepo].
func (r *PushRuleRepoImpl) SetEnabled(userID, kind, ruleID string, enabled bool) error {
	return r.db.Model(&PushRule{}).
		Where("user_id = ? AND kind = ? AND rule_id = ?", userID, kind, ruleID).
		Update("enabled", enabled).Error
}

// SetActions implements [PushRuleRepo].
func (r *PushRuleRepoImpl) SetActions(userID, kind, ruleID, actions string) error {
	return r.db.Model(&PushRule{}).
		Where("user_id = ? AND kind = ? AND rule_id = ?", userID, kind, ruleID).
		Update("actions", actions).Error
}

// Order implements [PushRuleRepo]. Positions are rewritten from zero, which
// keeps the ordering dense after repeated before/after moves.
func (r *PushRuleRepoImpl) Order(userID, kind string, ruleIDs []string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for position, ruleID := range ruleIDs {
			err := tx.Model(&PushRule{}).
				Where("user_id = ? AND kind = ? AND rule_id = ?", userID, kind, ruleID).
				Update("positions", position).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// Count implements [PushRuleRepo].
func (r *PushRuleRepoImpl) Count(userID string) (int64, error) {
	var n int64
	err := r.db.Model(&PushRule{}).Where("user_id = ?", userID).Count(&n).Error
	return n, err
}

// NewPushRuleRepo returns the PushRuleRepo implementation for db.
func NewPushRuleRepo(db *gorm.DB) PushRuleRepo {
	return &PushRuleRepoImpl{db: db}
}

// Pusher is a push notification endpoint registered by a client.
type Pusher struct {
	PushKey  string `json:"pushkey" gorm:"uniqueIndex:uq_pusher_scope;size:255"`
	Kind     string `json:"kind" gorm:"uniqueIndex:uq_pusher_scope;size:32"`
	AppID    string `json:"app_id" gorm:"uniqueIndex:uq_pusher_scope;size:255"`
	AppName  string `json:"app_display_name" gorm:"size:255"`
	DeviceID string `json:"device_id" gorm:"size:64"`
	UserID   string `json:"user_id" gorm:"index;size:255"`
	Lang     string `json:"lang" gorm:"size:32"`
	Data     string `json:"data" gorm:"type:text"`
	Active   bool   `json:"active"`

	gorm.Model
}

// PusherRepo is the contract for pusher storage access.
type PusherRepo interface {
	Set(pusher *Pusher) error
	List(userID string) ([]*Pusher, error)
	Delete(userID, pushKey, appID string) error
	Get(pushKey, appID string) (*Pusher, error)
}

// PusherRepoImpl is the GORM backed PusherRepo.
type PusherRepoImpl struct {
	db *gorm.DB
}

// Set implements [PusherRepo].
func (r *PusherRepoImpl) Set(pusher *Pusher) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "push_key"}, {Name: "kind"}, {Name: "app_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"app_name", "device_id", "user_id", "lang", "data", "active", "updated_at",
		}),
	}).Create(pusher).Error
}

// List implements [PusherRepo].
func (r *PusherRepoImpl) List(userID string) ([]*Pusher, error) {
	var pushers []*Pusher
	err := r.db.Where("user_id = ?", userID).Order("id asc").Find(&pushers).Error
	return pushers, err
}

// Delete implements [PusherRepo]. Only the owner of a pusher can remove it, and
// the row is dropped for real so the same pushkey and app id can be registered
// again afterwards.
func (r *PusherRepoImpl) Delete(userID, pushKey, appID string) error {
	return r.db.Unscoped().Where("user_id = ? AND push_key = ? AND app_id = ?", userID, pushKey, appID).
		Delete(&Pusher{}).
		Error
}

// Get implements [PusherRepo].
func (r *PusherRepoImpl) Get(pushKey, appID string) (*Pusher, error) {
	var pusher Pusher
	err := r.db.Where("push_key = ? AND app_id = ?", pushKey, appID).First(&pusher).Error
	return &pusher, err
}

// NewPusherRepo returns the PusherRepo implementation for db.
func NewPusherRepo(db *gorm.DB) PusherRepo {
	return &PusherRepoImpl{db: db}
}

// Notification is a counted notification for a user, produced when an event
// triggers a push rule.
type Notification struct {
	UserID  string `json:"user_id" gorm:"index;size:255"`
	RoomID  string `json:"room_id" gorm:"size:255"`
	EventID string `json:"event_id" gorm:"index;size:255"`
	TS      int64  `json:"ts"`
	// StreamPos positions the notification in the event stream so unread
	// counts can be computed against a sync token.
	StreamPos  uint64 `json:"stream_pos" gorm:"index"`
	CountTweak string `json:"count_tweak" gorm:"size:32"`
	// Highlighted records that the matched rule asked for the highlight tweak,
	// which lets sync report highlight counts without re-evaluating rules.
	Highlighted bool `json:"highlighted"`
	// Actions keeps the action list of the rule that fired, so
	// GET /notifications can hand the client exactly what the rule decided.
	Actions  string `json:"actions" gorm:"type:text"`
	Notified bool   `json:"notified"`

	gorm.Model
}

// NotificationRepo is the contract for notification storage access.
type NotificationRepo interface {
	Create(notification *Notification) error
	List(userID string, fromPos int64, limit int) ([]*Notification, int64, error)
	CountAfter(userID, roomID string, streamPos uint64) (int64, error)
	CountHighlightedAfter(userID, roomID string, streamPos uint64) (int64, error)
}

// NotificationRepoImpl is the GORM backed NotificationRepo.
type NotificationRepoImpl struct {
	db *gorm.DB
}

// Create implements [NotificationRepo].
func (r *NotificationRepoImpl) Create(notification *Notification) error {
	return r.db.Create(notification).Error
}

// List implements [NotificationRepo]. The returned string is a pagination
// token for the next page.
func (r *NotificationRepoImpl) List(userID string, fromPos int64, limit int) ([]*Notification, int64, error) {
	var rows []*Notification
	q := r.db.Model(&Notification{}).Where("user_id = ?", userID)
	if fromPos > 0 {
		q = q.Where("id > ?", fromPos)
	}
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Order("id asc").Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	next := int64(0)
	if len(rows) > 0 {
		next = int64(rows[len(rows)-1].ID)
	}
	return rows, next, nil
}

// CountAfter implements [NotificationRepo].
func (r *NotificationRepoImpl) CountAfter(userID, roomID string, streamPos uint64) (int64, error) {
	var n int64
	err := r.db.Model(&Notification{}).
		Where("user_id = ? AND room_id = ? AND stream_pos > ?", userID, roomID, streamPos).
		Count(&n).Error
	return n, err
}

// CountHighlightedAfter implements [NotificationRepo].
func (r *NotificationRepoImpl) CountHighlightedAfter(userID, roomID string, streamPos uint64) (int64, error) {
	var n int64
	err := r.db.Model(&Notification{}).
		Where("user_id = ? AND room_id = ? AND stream_pos > ? AND highlighted = ?", userID, roomID, streamPos, true).
		Count(&n).Error
	return n, err
}

// NewNotificationRepo returns the NotificationRepo implementation for db.
func NewNotificationRepo(db *gorm.DB) NotificationRepo {
	return &NotificationRepoImpl{db: db}
}
