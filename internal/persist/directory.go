package persist

import "context"

import "gorm.io/gorm"

// The room directory is a property of the room, not of its state: whether the room is
// published is stored on the room, and the name and topic it is listed under are cached
// there so a search does not have to load the state of every published room.
//
// The cache is written wherever that state is written, so it cannot drift from the state
// a client sees. It is a cache of two short strings, not of the state itself: the room's
// state remains the authority, and the directory never answers with the cache where the
// state is being read.

// SetPublished records whether the room appears in this server's public room list.
func (r *roomRepo) SetPublished(ctx context.Context, roomID string, published bool) error {
	result := r.db.WithContext(ctx).Model(&Room{}).
		Where("room_id = ?", roomID).
		Update("published", published)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDirectoryText refreshes the cached name and topic a published room is listed under.
func (r *roomRepo) SetDirectoryText(ctx context.Context, roomID, name, topic string) error {
	if err := r.db.WithContext(ctx).Model(&Room{}).
		Where("room_id = ?", roomID).
		Updates(map[string]any{"name": name, "topic": topic}).Error; err != nil {
		return err
	}
	return nil
}

// ListPublished returns one page of the published rooms, newest activity first, and the
// total number of published rooms. `search` matches the cached name or topic; it is
// matched case-insensitively because a person looking for a room is not thinking about
// case.
func (r *roomRepo) ListPublished(ctx context.Context, search string, offset, limit int) ([]*Room, int64, error) {
	query := func() *gorm.DB {
		q := r.db.WithContext(ctx).Model(&Room{}).Where("published = ?", true)
		if search != "" {
			term := "%" + search + "%"
			q = q.Where("name LIKE ? OR topic LIKE ?", term, term)
		}
		return q
	}
	var total int64
	if err := query().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 1
	}
	var rows []*Room
	if err := query().Order("latest_stream DESC, id DESC").
		Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
