package persist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// A room's state is not one row per state key and nothing more: the same room has a
// different state at each point in its history, and two servers can hold different states
// at points that descend from the same event. Resolving that — which is what State
// Resolution 2 is for — needs the state each branch was carrying, so the state has to be
// recorded per event rather than only as "where this room is now".
//
// A snapshot is stored as a group: an identity for one exact set of (type, state_key) →
// event ID entries, held once and referenced by every event that descends from it. A linear
// room therefore holds one group per state change and not one per event, and a fork shows up
// as two groups with a common ancestor — which is the shape the resolution algorithm works
// on. The identity is a hash of the entries, so two branches that happen to agree share a
// group without anybody having to notice.

// StateGroup is one snapshot of a room's state.
type StateGroup struct {
	ID int64 `gorm:"primaryKey;autoIncrement"`
	// RoomID and Hash carry one composite index: a snapshot is unique within its room, not
	// across all rooms. Two new rooms have the same empty snapshot, and an identity that
	// collided between them would make one room's state the other's.
	RoomID string `gorm:"column:room_id;size:255;not null;index;uniqueIndex:idx_state_group_room_hash,priority:1"`
	Hash   string `gorm:"column:hash;size:64;not null;uniqueIndex:idx_state_group_room_hash,priority:2"`
}

// TableName fixes the table name.
func (StateGroup) TableName() string { return "state_groups" }

// StateGroupEntry is one state key held by a group.
type StateGroupEntry struct {
	ID       uint64 `gorm:"primaryKey;autoIncrement"`
	GroupID  int64  `gorm:"column:group_id;not null;index"`
	Type     string `gorm:"column:type;size:255;not null"`
	StateKey string `gorm:"column:state_key;size:255;not null"`
	EventID  string `gorm:"column:event_id;size:255;not null;index"`
}

// TableName fixes the table name.
func (StateGroupEntry) TableName() string { return "state_group_entries" }

// EventStateGroup records the state an event was sent against.
type EventStateGroup struct {
	EventID string `gorm:"column:event_id;size:255;not null;uniqueIndex"`
	GroupID int64  `gorm:"column:group_id;not null;index"`
}

// TableName fixes the table name.
func (EventStateGroup) TableName() string { return "event_state_groups" }

// StateGroupRepo persists per-event room state.
type StateGroupRepo interface {
	WithTx(tx *gorm.DB) StateGroupRepo

	// EnsureGroup returns the ID of the group holding exactly these entries for the room,
	// creating it when it has not been seen before. Entries may be given in any order.
	EnsureGroup(ctx context.Context, roomID string, entries []StateGroupEntry) (int64, error)
	// Entries returns the entries of a group.
	Entries(ctx context.Context, groupID int64) ([]StateGroupEntry, error)
	// GroupOf returns the group an event was sent against.
	GroupOf(ctx context.Context, eventID string) (int64, error)
	// Attach records which group an event was sent against. Re-attaching the same event to
	// the same group is not an error.
	Attach(ctx context.Context, eventID string, groupID int64) error
	// EmptyGroup returns the group with no entries — the state before the create event.
	EmptyGroup(ctx context.Context, roomID string) (int64, error)
}

type stateGroupRepo struct{ db *gorm.DB }

// NewStateGroupRepo returns the per-event state repository.
func NewStateGroupRepo(db *gorm.DB) StateGroupRepo { return &stateGroupRepo{db: db} }

// WithTx implements [StateGroupRepo].
func (r *stateGroupRepo) WithTx(tx *gorm.DB) StateGroupRepo { return &stateGroupRepo{db: tx} }

// EnsureGroup implements [StateGroupRepo].
func (r *stateGroupRepo) EnsureGroup(ctx context.Context, roomID string, entries []StateGroupEntry) (int64, error) {
	hash := stateHash(entries)
	var group StateGroup
	err := r.db.WithContext(ctx).Where("room_id = ? AND hash = ?", roomID, hash).First(&group).Error
	if err == nil {
		return group.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	group = StateGroup{RoomID: roomID, Hash: hash}
	if err := r.db.WithContext(ctx).Create(&group).Error; err != nil {
		if conflict := asConflict(err); conflict != nil {
			// Another writer made the same snapshot in the same instant. Its row is the
			// answer; making it twice would only split the identity.
			if err := r.db.WithContext(ctx).Where("room_id = ? AND hash = ?", roomID, hash).First(&group).Error; err != nil {
				return 0, err
			}
			return group.ID, nil
		}
		return 0, err
	}
	if len(entries) > 0 {
		rows := make([]StateGroupEntry, 0, len(entries))
		for _, entry := range entries {
			entry.GroupID = group.ID
			entry.ID = 0
			rows = append(rows, entry)
		}
		if err := r.db.WithContext(ctx).CreateInBatches(&rows, 200).Error; err != nil {
			return 0, err
		}
	}
	return group.ID, nil
}

// Entries implements [StateGroupRepo].
func (r *stateGroupRepo) Entries(ctx context.Context, groupID int64) ([]StateGroupEntry, error) {
	var rows []StateGroupEntry
	if err := r.db.WithContext(ctx).Where("group_id = ?", groupID).
		Order("type, state_key").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GroupOf implements [StateGroupRepo].
func (r *stateGroupRepo) GroupOf(ctx context.Context, eventID string) (int64, error) {
	var row EventStateGroup
	if err := r.db.WithContext(ctx).Where("event_id = ?", eventID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return row.GroupID, nil
}

// Attach implements [StateGroupRepo].
func (r *stateGroupRepo) Attach(ctx context.Context, eventID string, groupID int64) error {
	if _, err := r.GroupOf(ctx, eventID); err == nil {
		return r.db.WithContext(ctx).Model(&EventStateGroup{}).
			Where("event_id = ?", eventID).Update("group_id", groupID).Error
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := r.db.WithContext(ctx).Create(&EventStateGroup{EventID: eventID, GroupID: groupID}).Error; err != nil {
		return asConflict(err)
	}
	return nil
}

// EmptyGroup implements [StateGroupRepo].
func (r *stateGroupRepo) EmptyGroup(ctx context.Context, roomID string) (int64, error) {
	return r.EnsureGroup(ctx, roomID, nil)
}

// stateHash identifies a state snapshot. The entries are sorted first so that the same set
// always hashes the same way whatever order it arrived in, and the parts of an entry are
// separated by a byte that cannot appear in them so no two different sets can collide by
// running together.
func stateHash(entries []StateGroupEntry) string {
	sorted := append([]StateGroupEntry{}, entries...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Type != sorted[j].Type {
			return sorted[i].Type < sorted[j].Type
		}
		if sorted[i].StateKey != sorted[j].StateKey {
			return sorted[i].StateKey < sorted[j].StateKey
		}
		return sorted[i].EventID < sorted[j].EventID
	})
	var b strings.Builder
	for _, entry := range sorted {
		b.WriteString(entry.Type)
		b.WriteByte(0)
		b.WriteString(entry.StateKey)
		b.WriteByte(0)
		b.WriteString(entry.EventID)
		b.WriteByte(0)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
