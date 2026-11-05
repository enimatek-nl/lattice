package persist

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// RoomAlias maps a room alias owned by this server to a room.
//
// An alias is a name a client can address a room by, so it is stored apart from the
// room: a room may have none, several, and the mapping has to be resolvable without
// searching room state.
type RoomAlias struct {
	ID      uint64 `gorm:"primaryKey;autoIncrement"`
	Alias   string `gorm:"column:alias;size:255;not null"`
	RoomID  string `gorm:"column:room_id;size:255;not null"`
	Creator string `gorm:"column:creator;size:255;not null"`
}

// RoomAliasRepo persists room alias mappings.
type RoomAliasRepo interface {
	WithTx(tx *gorm.DB) RoomAliasRepo

	// Create stores a new mapping. An existing alias is ErrConflict, because
	// silently re-pointing an alias would move a name someone else is using.
	Create(ctx context.Context, alias *RoomAlias) error
	ByAlias(ctx context.Context, alias string) (*RoomAlias, error)
	ByRoom(ctx context.Context, roomID string) ([]*RoomAlias, error)
	// Delete removes a mapping. A missing mapping is ErrNotFound.
	Delete(ctx context.Context, alias string) error
}

type roomAliasRepo struct{ db *gorm.DB }

// NewRoomAliasRepo returns the room alias repository.
func NewRoomAliasRepo(db *gorm.DB) RoomAliasRepo { return &roomAliasRepo{db: db} }

// WithTx implements [RoomAliasRepo]. Skipping this would send the write to a separate
// connection while a transaction holds the only writer, which deadlocks.
func (r *roomAliasRepo) WithTx(tx *gorm.DB) RoomAliasRepo { return &roomAliasRepo{db: tx} }

// Create implements [RoomAliasRepo].
func (r *roomAliasRepo) Create(ctx context.Context, alias *RoomAlias) error {
	if err := r.db.WithContext(ctx).Create(alias).Error; err != nil {
		return asConflict(err)
	}
	return nil
}

// ByAlias implements [RoomAliasRepo].
func (r *roomAliasRepo) ByAlias(ctx context.Context, alias string) (*RoomAlias, error) {
	var row RoomAlias
	err := r.db.WithContext(ctx).Where("alias = ?", alias).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ByRoom implements [RoomAliasRepo].
func (r *roomAliasRepo) ByRoom(ctx context.Context, roomID string) ([]*RoomAlias, error) {
	var rows []*RoomAlias
	if err := r.db.WithContext(ctx).Where("room_id = ?", roomID).Order("alias").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Delete implements [RoomAliasRepo].
func (r *roomAliasRepo) Delete(ctx context.Context, alias string) error {
	res := r.db.WithContext(ctx).Where("alias = ?", alias).Delete(&RoomAlias{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
