package persist

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Relation is one event's statement that it relates to another event: a reply in a thread,
// an edit, a reaction.
//
// The statement itself lives in the event's `m.relates_to`, which is inside the canonical JSON
// that the event's signature covers; this row is not a second copy of that claim but an index
// over it. It exists because "what points at this event, newest first" is a question with an
// answer of a handful of rows in a room that may hold a million, and scanning the timeline to
// find them would make every thread view and every edit chain cost the room's history. The
// event's own bytes stay the authority: this table is written when the event is stored and
// deleted when a redaction takes the content that made the claim, so it never disagrees with
// what a client is served.
type Relation struct {
	// ID is this row's key; the identity of a relation is its event ID, indexed below.
	ID      uint64 `gorm:"primaryKey;autoIncrement"`
	EventID string `gorm:"column:event_id;type:text;not null"`
	RoomID  string `gorm:"column:room_id;type:text;not null"`
	Sender  string `gorm:"column:sender;type:text;not null"`
	// EventType is the relating event's own type, kept because the endpoint filters on it
	// (`…/relations/{eventID}/m.thread/m.room.message`) and because it survives a redaction:
	// a redaction strips content, not type, so the filter still has an answer to work with.
	EventType string `gorm:"column:event_type;type:text;not null"`
	// RelType is `m.relates_to.rel_type`: `m.thread`, `m.replace`, `m.annotation`, or a name
	// a proposal is still arguing about. Empty is deliberately absent: an `m.relates_to` with
	// only an `event_id` is a reply's fallback, which the specification does not treat as a
	// relation of this kind.
	RelType string `gorm:"column:rel_type;type:text;not null"`
	// TargetEventID is the event being related to. There is no foreign key: an event may
	// legitimately relate to one this server does not hold yet — a reply over federation
	// often arrives before what it answers — and refusing that would reject valid Matrix.
	TargetEventID  string `gorm:"column:target_event_id;type:text;not null"`
	OriginServerTS int64  `gorm:"column:origin_server_ts;not null;default:0"`
	StreamOrdering int64  `gorm:"column:stream_ordering;not null;default:0"`
}

// TableName pins the table name: it is a table about events, so letting GORM pluralise
// "Relation" into "relations" would collide with nothing but says less about what is in it.
func (Relation) TableName() string { return "event_relations" }

// RelationRepo indexes which events relate to which.
type RelationRepo interface {
	WithTx(tx *gorm.DB) RelationRepo

	// Put records one relation. Re-putting the same event's relation is not an error: an
	// event is persisted once, but a state reconciliation may hand it over again.
	Put(ctx context.Context, relation *Relation) error
	// DeleteByEvent forgets the relation an event claimed, which is what a redaction does
	// to it: the content that named the target is gone from what anyone may read.
	DeleteByEvent(ctx context.Context, eventID string) error
	// ForTarget returns the relations pointing at one event, in the direction a client is
	// walking, newest first backwards and oldest first forwards. `from` is exclusive, which
	// is how a pagination token names the last event a client already has. An empty relType
	// or eventType asks for every kind. Outliers are excluded: an event this server has not
	// accepted into the timeline is not a relation it will show.
	ForTarget(ctx context.Context, roomID, targetEventID, relType, eventType string, from int64, backwards bool, limit int) ([]*Relation, error)
	// CountForTarget answers how many relations point at one event, which is what a thread's
	// "N replies" badge is. relType, include and exclude narrow what counts, in the same way
	// they narrow a page, so the number agrees with the page beside it. `until` is a stream
	// position a requester may not read past — zero says there is no such limit — because a
	// count that includes events the reader cannot see is a way of telling them about those
	// events after all.
	CountForTarget(ctx context.Context, roomID, targetEventID, relType string, include, exclude []string, until int64) (int64, error)
	// ThreadsInRoom returns the room's threads newest-first (or oldest-first) ordered by where
	// each thread's newest event sits in the stream — which is not where each thread started,
	// and a client's thread list wants the thread somebody just replied to at the top. `from`
	// is a position compared against that newest event, exclusive, and is what makes the page
	// a page. relType names what makes a thread: "m.thread" today. include and exclude are the
	// event types the caller counts as part of a thread, which is also MSC3440's reading: the
	// filters choose which events make a thread what it is, not only which ones are printed.
	// `until` again bounds the read by what the requester may see.
	ThreadsInRoom(ctx context.Context, roomID, relType string, include, exclude []string, from, until int64, backwards bool, limit int) ([]*ThreadSummary, error)
	// HasRelationBy answers whether one of the relations pointing at an event was claimed by a
	// particular sender, which is MSC3440's "current_user_participated": a client highlights the
	// conversations the person reading it has taken part in.
	HasRelationBy(ctx context.Context, roomID, targetEventID, relType, sender string, until int64) (bool, error)
	// LatestForTarget returns the newest relation pointing at one event, which is the event a
	// thread list shows as the thread's last word. The filters are the same as the list's, so
	// the last word of a thread agrees with the total printed next to it.
	LatestForTarget(ctx context.Context, roomID, targetEventID, relType string, include, exclude []string, until int64) (*Relation, error)
}

// NewRelationRepo returns the relation index bound to db.
func NewRelationRepo(db *gorm.DB) RelationRepo { return &relationRepo{db: db} }

type relationRepo struct{ db *gorm.DB }

// WithTx implements [RelationRepo].
func (r *relationRepo) WithTx(tx *gorm.DB) RelationRepo { return &relationRepo{db: tx} }

// Put implements [RelationRepo].
func (r *relationRepo) Put(ctx context.Context, relation *Relation) error {
	var existing Relation
	err := r.db.WithContext(ctx).Where("event_id = ?", relation.EventID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.WithContext(ctx).Create(relation).Error
	}
	return r.db.WithContext(ctx).Model(&Relation{}).Where("id = ?", existing.ID).
		Updates(map[string]any{
			"rel_type":         relation.RelType,
			"target_event_id":  relation.TargetEventID,
			"event_type":       relation.EventType,
			"stream_ordering":  relation.StreamOrdering,
			"origin_server_ts": relation.OriginServerTS,
		}).Error
}

// DeleteByEvent implements [RelationRepo].
func (r *relationRepo) DeleteByEvent(ctx context.Context, eventID string) error {
	return r.db.WithContext(ctx).Where("event_id = ?", eventID).Delete(&Relation{}).Error
}

// ForTarget implements [RelationRepo].
func (r *relationRepo) ForTarget(ctx context.Context, roomID, targetEventID, relType, eventType string, from int64, backwards bool, limit int) ([]*Relation, error) {
	if limit <= 0 {
		limit = 1
	}
	query := r.db.WithContext(ctx).
		Where("room_id = ? AND target_event_id = ?", roomID, targetEventID)
	if relType != "" {
		query = query.Where("rel_type = ?", relType)
	}
	if eventType != "" {
		query = query.Where("event_type = ?", eventType)
	}
	if from > 0 {
		if backwards {
			query = query.Where("stream_ordering < ?", from)
		} else {
			query = query.Where("stream_ordering > ?", from)
		}
	}
	order := "stream_ordering ASC"
	if backwards {
		order = "stream_ordering DESC"
	}
	var rows []*Relation
	if err := query.Order(order).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountForTarget implements [RelationRepo].
func (r *relationRepo) CountForTarget(ctx context.Context, roomID, targetEventID, relType string, include, exclude []string, until int64) (int64, error) {
	query := r.db.WithContext(ctx).Model(&Relation{}).
		Where("room_id = ? AND target_event_id = ?", roomID, targetEventID)
	if relType != "" {
		query = query.Where("rel_type = ?", relType)
	}
	query = applyTypeLists(query, include, exclude)
	if until > 0 {
		query = query.Where("stream_ordering <= ?", until)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// ThreadSummary is one thread as the room's thread list describes it: the event everything
// points at, where the thread's newest event sits in the stream, and how many events point at
// the root. It is a read of the index, not a stored thing: a thread is a shape the relations
// make, and storing it separately would be a second answer to "what is in this thread".
type ThreadSummary struct {
	RootEventID  string `gorm:"column:target_event_id"`
	LatestStream int64  `gorm:"column:latest"`
	Total        int64  `gorm:"column:total"`
}

// ThreadsInRoom implements [RelationRepo].
func (r *relationRepo) ThreadsInRoom(ctx context.Context, roomID, relType string, include, exclude []string, from, until int64, backwards bool, limit int) ([]*ThreadSummary, error) {
	if limit <= 0 {
		limit = 1
	}
	query := r.db.WithContext(ctx).Table("event_relations").
		Select("target_event_id, MAX(stream_ordering) AS latest, COUNT(*) AS total").
		Where("room_id = ? AND rel_type = ?", roomID, relType).
		Group("target_event_id")
	query = applyTypeLists(query, include, exclude)
	if until > 0 {
		query = query.Where("stream_ordering <= ?", until)
	}
	if from > 0 {
		if backwards {
			query = query.Having("MAX(stream_ordering) < ?", from)
		} else {
			query = query.Having("MAX(stream_ordering) > ?", from)
		}
	}
	order := "latest ASC"
	if backwards {
		order = "latest DESC"
	}
	var rows []*ThreadSummary
	if err := query.Order(order).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// LatestForTarget implements [RelationRepo].
func (r *relationRepo) LatestForTarget(ctx context.Context, roomID, targetEventID, relType string, include, exclude []string, until int64) (*Relation, error) {
	query := r.db.WithContext(ctx).
		Where("room_id = ? AND target_event_id = ? AND rel_type = ?", roomID, targetEventID, relType)
	query = applyTypeLists(query, include, exclude)
	if until > 0 {
		query = query.Where("stream_ordering <= ?", until)
	}
	var row Relation
	err := query.Order("stream_ordering DESC").Limit(1).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// HasRelationBy implements [RelationRepo].
func (r *relationRepo) HasRelationBy(ctx context.Context, roomID, targetEventID, relType, sender string, until int64) (bool, error) {
	if sender == "" {
		return false, nil
	}
	query := r.db.WithContext(ctx).Model(&Relation{}).
		Where("room_id = ? AND target_event_id = ? AND rel_type = ? AND sender = ?", roomID, targetEventID, relType, sender)
	if until > 0 {
		query = query.Where("stream_ordering <= ?", until)
	}
	var count int64
	if err := query.Limit(1).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// applyTypeLists puts MSC3440's include/exclude onto a query. An empty include list means
// every type, which is what "include nothing in particular" has to mean; an empty exclude list
// excludes nothing. A type appearing in both is the exclude's to decide, because a caller who
// names a type twice has said less than one who names it once.
func applyTypeLists(query *gorm.DB, include, exclude []string) *gorm.DB {
	if len(include) > 0 {
		query = query.Where("event_type IN ?", include)
	}
	if len(exclude) > 0 {
		query = query.Where("event_type NOT IN ?", exclude)
	}
	return query
}
