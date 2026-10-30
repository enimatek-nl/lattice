package persist

import (
	"strings"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"gorm.io/gorm"
)

// Event is a persisted Matrix event. The full event is reconstructed from the
// columns below so history can be queried without decoding JSON blobs.
type Event struct {
	EventID string `json:"event_id" gorm:"uniqueIndex;size:255"`
	RoomID  string `json:"room_id" gorm:"index:idx_event_room_stream,priority:1;size:255"`
	// StreamPos is a globally monotonic position used for sync tokens and
	// pagination.
	StreamPos       uint64 `json:"stream_pos" gorm:"uniqueIndex;index:idx_event_room_stream,priority:2"`
	Type            string `json:"type" gorm:"index:idx_event_room_stream,priority:3;size:128"`
	StateKey        string `json:"state_key,omitempty" gorm:"size:255"`
	Sender          string `json:"sender" gorm:"size:255"`
	OriginServerTS  int64  `json:"origin_server_ts"`
	Content         string `json:"content" gorm:"type:text"`
	Depth           int64  `json:"depth"`
	Redacted        bool   `json:"redacted"`
	RedactionReason string `json:"-" gorm:"size:255"`
	// RedactionEvent holds the serialised m.room.redaction event that redacted
	// this event, which clients receive as unsigned.redacted_because.
	RedactionEvent string `json:"-" gorm:"type:text"`
	// RelatesTo and RelType denormalise the m.relates_to relation of an event
	// content so relation queries do not have to decode JSON.
	RelatesTo string `json:"-" gorm:"index:idx_event_relates,priority:1;size:255"`
	RelType   string `json:"-" gorm:"index:idx_event_relates,priority:2;size:64"`

	gorm.Model
}

// Matrix converts the stored event into its wire representation. Age is
// derived from the current time.
func (e *Event) Matrix() matrix.Event {
	content := matrix.RawMessage(e.Content)
	if len(content) == 0 {
		content = matrix.RawMessage("{}")
	}
	ev := matrix.Event{
		RoomID:         e.RoomID,
		Type:           e.Type,
		Sender:         e.Sender,
		OriginServerTS: e.OriginServerTS,
		ID:             e.EventID,
		Content:        content,
		Depth:          e.Depth,
	}
	if e.StateKey != "" {
		key := e.StateKey
		ev.StateKey = &key
	}
	if e.Type == matrix.TypeRedaction {
		// The redaction target is a top level field of the event, even though
		// Lattice keeps it in the serialised content.
		var content matrix.RedactionContent
		if err := matrix.DecodeContent(matrix.RawMessage(e.Content), &content); err == nil {
			ev.Redacts = content.Redacts
		}
	}
	age := time.Now().UnixMilli() - e.OriginServerTS
	if age < 0 {
		age = 0
	}
	ev.Unsigned = matrix.Object{"age": age}
	if e.Redacted {
		evidence := matrix.Object{}
		if e.RedactionReason != "" {
			evidence["reason"] = e.RedactionReason
		}
		if e.RedactionEvent != "" {
			evidence = matrix.MustParseObject([]byte(e.RedactionEvent))
		}
		ev.Unsigned["redacted_because"] = evidence
		if e.Type != matrix.TypeMember && e.Type != matrix.TypeCreate {
			ev.Content = matrix.RawMessage("{}")
		}
	}
	return ev
}

// EventRepo is the contract for event storage access.
type EventRepo interface {
	Create(event *Event) error
	Get(eventID string) (*Event, error)
	GetInRoom(roomID, eventID string) (*Event, error)
	ListByIDs(ids []string) ([]*Event, error)
	// History returns events of a room ordered by stream position, descending
	// when dir is "b" (backwards) and ascending when dir is "f". from is the
	// exclusive stream position to start at.
	History(roomID string, from uint64, dir string, limit int, notBefore uint64) ([]*Event, error)
	// Timeline returns the events of a room with a stream position greater than
	// since, oldest first.
	Timeline(roomID string, since uint64, limit int) ([]*Event, error)
	// TimelineFor returns timeline events for a set of rooms.
	TimelineFor(roomIDs []string, since uint64, limit int) ([]*Event, error)
	// LatestStreamPos returns the highest stream position in use.
	LatestStreamPos() (uint64, error)
	// LatestRoomStreamPos returns the highest stream position of a room.
	LatestRoomStreamPos(roomID string) (uint64, error)
	// Count returns the number of persisted events.
	Count() (int64, error)
	// MarkRedacted remembers the redaction evidence for an event and reports
	// whether an unredacted event with that id existed.
	MarkRedacted(eventID, reason, redactionEvent string) (bool, error)
	// RecentEventIDs returns the ids of the most recent events of a room,
	// used to build prev_events.
	RecentEventIDs(roomID string, limit int, excludeType string) ([]string, error)
	// HistoryByTimestamp finds events around an origin_server_ts; "b" searches
	// backwards in time, "f" forwards.
	HistoryByTimestamp(roomID string, ts int64, dir string, limit int) ([]*Event, error)
	// Relations lists events that relate to a target event.
	Relations(roomID, eventID, relType, eventType string, limit int, from, to uint64) ([]*Event, error)
	// Threads lists the events that participate in threads, oldest first.
	Threads(roomID string) ([]*Event, error)
	// ThreadsFor lists the m.thread relations of a room whose root is one of
	// the given event ids, oldest first.
	ThreadsFor(roomID string, rootIDs []string) ([]*Event, error)
	// CountBefore counts events of a room before a stream position.
	CountBefore(roomID string, pos uint64) (int64, error)
	// Search lists unredacted events whose content mentions every term, newest
	// first. Server-side search is deliberately a plain substring match: no
	// tokenizer, no stemming, ASCII case folding only.
	Search(filter SearchQuery) ([]*Event, error)
}

// SearchQuery narrows [EventRepo.Search]. An empty slice means "no filter on
// this field"; an empty RoomIDs list matches nothing, because the caller decides
// which rooms the requester may read.
type SearchQuery struct {
	RoomIDs    []string
	NotRooms   []string
	Types      []string
	NotTypes   []string
	Senders    []string
	NotSenders []string
	// Terms are AND-ed: every term has to appear in the serialised content.
	Terms []string
	Limit int
}

// EventRepoImpl is the GORM backed EventRepo.
type EventRepoImpl struct {
	db *gorm.DB
}

// Create implements [EventRepo].
func (r *EventRepoImpl) Create(event *Event) error {
	return r.db.Create(event).Error
}

// Get implements [EventRepo].
func (r *EventRepoImpl) Get(eventID string) (*Event, error) {
	var event Event
	err := r.db.Where("event_id = ?", eventID).First(&event).Error
	return &event, err
}

// GetInRoom implements [EventRepo].
func (r *EventRepoImpl) GetInRoom(roomID, eventID string) (*Event, error) {
	var event Event
	err := r.db.Where("room_id = ? AND event_id = ?", roomID, eventID).
		First(&event).Error
	return &event, err
}

// ListByIDs implements [EventRepo].
func (r *EventRepoImpl) ListByIDs(ids []string) ([]*Event, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var events []*Event
	err := r.db.Where("event_id IN ?", ids).Order("stream_pos asc").Find(&events).Error
	return events, err
}

// History implements [EventRepo].
func (r *EventRepoImpl) History(roomID string, from uint64, dir string, limit int, notBefore uint64) ([]*Event, error) {
	var events []*Event
	q := r.db.Where("room_id = ?", roomID)
	if from > 0 {
		if dir == "f" {
			q = q.Where("stream_pos > ?", from)
		} else {
			q = q.Where("stream_pos < ?", from)
		}
	}
	if notBefore > 0 {
		q = q.Where("stream_pos >= ?", notBefore)
	}
	order := "stream_pos desc"
	if dir == "f" {
		order = "stream_pos asc"
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Order(order).Find(&events).Error
	return events, err
}

// Timeline implements [EventRepo].
func (r *EventRepoImpl) Timeline(roomID string, since uint64, limit int) ([]*Event, error) {
	return r.TimelineFor([]string{roomID}, since, limit)
}

// TimelineFor implements [EventRepo].
func (r *EventRepoImpl) TimelineFor(roomIDs []string, since uint64, limit int) ([]*Event, error) {
	if len(roomIDs) == 0 {
		return nil, nil
	}
	var events []*Event
	q := r.db.Where("room_id IN ? AND stream_pos > ?", roomIDs, since).
		Order("stream_pos asc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&events).Error
	return events, err
}

// LatestStreamPos implements [EventRepo].
func (r *EventRepoImpl) LatestStreamPos() (uint64, error) {
	var pos *uint64
	err := r.db.Model(&Event{}).Select("max(stream_pos)").Scan(&pos).Error
	if err != nil {
		return 0, err
	}
	if pos == nil {
		return 0, nil
	}
	return *pos, nil
}

// LatestRoomStreamPos implements [EventRepo].
func (r *EventRepoImpl) LatestRoomStreamPos(roomID string) (uint64, error) {
	var pos *uint64
	err := r.db.Model(&Event{}).Where("room_id = ?", roomID).
		Select("max(stream_pos)").Scan(&pos).Error
	if err != nil {
		return 0, err
	}
	if pos == nil {
		return 0, nil
	}
	return *pos, nil
}

// Count implements [EventRepo].
func (r *EventRepoImpl) Count() (int64, error) {
	var n int64
	err := r.db.Model(&Event{}).Count(&n).Error
	return n, err
}

// MarkRedacted implements [EventRepo]. It reports whether an unredacted event
// was found, which lets callers answer with M_NOT_FOUND for a target that is
// already gone instead of silently redacting nothing.
func (r *EventRepoImpl) MarkRedacted(eventID, reason, redactionEvent string) (bool, error) {
	result := r.db.Model(&Event{}).Where("event_id = ? AND redacted = ?", eventID, false).
		Updates(map[string]any{
			"redacted":         true,
			"redaction_reason": reason,
			"redaction_event":  redactionEvent,
		})
	return result.RowsAffected > 0, result.Error
}

// RecentEventIDs implements [EventRepo].
func (r *EventRepoImpl) RecentEventIDs(roomID string, limit int, excludeType string) ([]string, error) {
	var ids []string
	q := r.db.Model(&Event{}).Where("room_id = ?", roomID)
	if excludeType != "" {
		q = q.Where("type <> ?", excludeType)
	}
	err := q.Order("stream_pos desc").Limit(limit).Pluck("event_id", &ids).Error
	return ids, err
}

// HistoryByTimestamp implements [EventRepo].
func (r *EventRepoImpl) HistoryByTimestamp(roomID string, ts int64, dir string, limit int) ([]*Event, error) {
	var events []*Event
	q := r.db.Where("room_id = ?", roomID)
	order := "origin_server_ts desc"
	switch dir {
	case "f":
		q = q.Where("origin_server_ts >= ?", ts)
		order = "origin_server_ts asc"
	default:
		q = q.Where("origin_server_ts <= ?", ts)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Order(order).Find(&events).Error
	return events, err
}

// Relations implements [EventRepo].
func (r *EventRepoImpl) Relations(roomID, eventID, relType, eventType string, limit int, from, to uint64) ([]*Event, error) {
	var events []*Event
	q := r.db.Where("room_id = ? AND relates_to = ?", roomID, eventID)
	if relType != "" {
		q = q.Where("rel_type = ?", relType)
	}
	if eventType != "" {
		q = q.Where("type = ?", eventType)
	}
	if from > 0 {
		q = q.Where("stream_pos > ?", from)
	}
	if to > 0 {
		q = q.Where("stream_pos < ?", to)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Order("stream_pos asc").Find(&events).Error
	return events, err
}

// Threads implements [EventRepo].
func (r *EventRepoImpl) Threads(roomID string) ([]*Event, error) {
	var events []*Event
	err := r.db.Where("room_id = ? AND rel_type = ?", roomID, "m.thread").
		Order("stream_pos asc").Find(&events).Error
	return events, err
}

// ThreadsFor implements [EventRepo].
func (r *EventRepoImpl) ThreadsFor(roomID string, rootIDs []string) ([]*Event, error) {
	if len(rootIDs) == 0 {
		return nil, nil
	}
	var events []*Event
	err := r.db.Where("room_id = ? AND rel_type = ? AND relates_to IN ?", roomID, "m.thread", rootIDs).
		Order("stream_pos asc").Find(&events).Error
	return events, err
}

// CountBefore implements [EventRepo].
func (r *EventRepoImpl) CountBefore(roomID string, pos uint64) (int64, error) {
	var n int64
	err := r.db.Model(&Event{}).Where("room_id = ? AND stream_pos < ?", roomID, pos).
		Count(&n).Error
	return n, err
}

// Search implements [EventRepo].
func (r *EventRepoImpl) Search(query SearchQuery) ([]*Event, error) {
	if len(query.RoomIDs) == 0 {
		return nil, nil
	}
	var events []*Event
	q := r.db.Where("room_id IN ? AND redacted = ?", query.RoomIDs, false)
	if len(query.NotRooms) > 0 {
		q = q.Where("room_id NOT IN ?", query.NotRooms)
	}
	if len(query.Types) > 0 {
		q = q.Where("type IN ?", query.Types)
	}
	if len(query.NotTypes) > 0 {
		q = q.Where("type NOT IN ?", query.NotTypes)
	}
	if len(query.Senders) > 0 {
		q = q.Where("sender IN ?", query.Senders)
	}
	if len(query.NotSenders) > 0 {
		q = q.Where("sender NOT IN ?", query.NotSenders)
	}
	for _, term := range query.Terms {
		if term == "" {
			continue
		}
		q = q.Where("content LIKE ? ESCAPE '\\'", "%"+escapeLike(term)+"%")
	}
	if query.Limit > 0 {
		q = q.Limit(query.Limit)
	}
	err := q.Order("stream_pos desc").Find(&events).Error
	return events, err
}

// escapeLike neutralises the wildcards of LIKE so a search term is matched
// literally.
func escapeLike(term string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(term)
}

// NewEventRepo returns the EventRepo implementation for db.
func NewEventRepo(db *gorm.DB) EventRepo {
	return &EventRepoImpl{db: db}
}
