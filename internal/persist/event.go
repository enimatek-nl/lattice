package persist

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Event is a persisted Matrix event. Most of it is reconstructed from the columns
// below, so history can be queried without decoding JSON blobs; the signed form of
// the event lives in the JSON column, because it is the only copy its own signature
// covers.
type Event struct {
	EventID string `json:"event_id" gorm:"uniqueIndex;size:255"`
	RoomID  string `json:"room_id" gorm:"index:idx_event_room_stream,priority:1;index:idx_event_room_rel,priority:1;size:255"`
	// StreamPos is a globally monotonic position used for sync tokens and
	// pagination.
	StreamPos uint64 `json:"stream_pos" gorm:"uniqueIndex;index:idx_event_room_stream,priority:2"`
	Type      string `json:"type" gorm:"index:idx_event_room_stream,priority:3;size:128"`
	StateKey  string `json:"state_key,omitempty" gorm:"size:255"`
	// IsState records that the event is a state event. It cannot be derived from
	// StateKey, because the state key of m.room.name, m.room.topic, m.room.create
	// and friends is the empty string: without this flag a client cannot tell a
	// state event from a message, ignores the room name change, and cannot find
	// the room version any more.
	IsState         bool   `json:"is_state"`
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
	// content so relation queries do not have to decode JSON. The room-scoped
	// index is what /threads and the per-room /relations listings read: both ask
	// for one room and one relation type, and the thread aggregation groups by the
	// root on top of it.
	RelatesTo string `json:"-" gorm:"index:idx_event_relates,priority:1;index:idx_event_room_rel,priority:3;size:255"`
	RelType   string `json:"-" gorm:"index:idx_event_relates,priority:2;index:idx_event_room_rel,priority:2;size:64"`

	// JSON is the event in the form the room version defines: the object that was
	// hashed, signed and identified. It is kept because it cannot be rebuilt. A
	// signature is made over the exact bytes of that object, so re-serialising an
	// event from the columns above - which drop prev_events, hashes and the order of
	// keys - produces an event its own signature does not cover. Federation sends this
	// copy, and a client is served from it too.
	JSON string `json:"-" gorm:"type:text"`
	// PrevEvents and AuthEvents are the JSON arrays that place this event in the
	// room's graph. They are stored rather than recomputed because they are part of
	// what the event *is*: in room version 3 and later the event id is the hash of the
	// event including its prev_events and auth_events, so deriving them again later -
	// from a graph that has moved on - would describe a different event.
	PrevEvents string `json:"-" gorm:"type:text"`
	AuthEvents string `json:"-" gorm:"type:text"`
	// SoftFailed marks an event that did not pass its auth checks against its auth
	// chain but is kept anyway: a later event may still cite it, and what decides the
	// room state is the resolution over the state at the event. RejectedReason marks
	// an event that must never be used, with the reason kept for the operator who asks.
	SoftFailed bool `json:"soft_failed"`
	// NotInTimeline marks an event this server holds because the room's state names it,
	// not because the event happened here: the state that arrives with a join into a room
	// that lives elsewhere. Timeline reads skip them - a client reading the room's history
	// must not see another server's year-old rename arrive as the newest thing in the room
	// - while state reads, federation replies and the auth-chain work still see them. The
	// flag is written the wrong way round on purpose: a "is in the timeline" flag would
	// default to false for every event stored before it existed.
	NotInTimeline  bool   `json:"not_in_timeline"`
	RejectedReason string `json:"-" gorm:"size:255"`

	gorm.Model
}

// ForwardExtremity records that an event is a leaf of a room's graph: no other
// event cites it in prev_events. New events attach to the leaves, so this is the set
// that decides where the next event goes, and the set that has to converge between
// servers for a room to stay one room.
//
// It is kept as rows rather than derived on the fly because deriving it means asking
// which of a room's events are not named by any other event - a full scan of the room
// on every send. Depth and OriginServerTS ride along so the leaves can be ordered the
// way the specification asks, deepest first.
type ForwardExtremity struct {
	RoomID         string `json:"room_id" gorm:"uniqueIndex:uq_extremity_event,priority:1;index:idx_extremity_room,priority:1;size:255"`
	EventID        string `json:"event_id" gorm:"uniqueIndex:uq_extremity_event,priority:2;size:255"`
	Depth          int64  `json:"depth" gorm:"index:idx_extremity_room,priority:2"`
	OriginServerTS int64  `json:"origin_server_ts" gorm:"index:idx_extremity_room,priority:3"`

	gorm.Model
}

// decodeStringList decodes a JSON array of strings, returning nothing for a column
// that was never filled.
func decodeStringList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
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
		PrevEvents:     decodeStringList(e.PrevEvents),
		AuthEvents:     decodeStringList(e.AuthEvents),
	}
	// The stored PDU carries what the columns cannot: the origin server name, the
	// content hashes and the server signature. A client that checks an event for
	// itself - a key-sharing dialog, a bridge, an audit tool - needs those fields,
	// and they only exist in the form that was signed.
	if e.JSON != "" {
		if pdu := matrix.MustParseObject([]byte(e.JSON)); len(pdu) > 0 {
			if origin, ok := pdu["origin"].(string); ok {
				ev.Origin = origin
			}
			if hashes, ok := pdu["hashes"].(matrix.Object); ok {
				ev.Hashes = hashes
			}
			if signatures, ok := pdu["signatures"].(matrix.Object); ok {
				ev.Signatures = signatures
			}
		}
		// A redacted event is served from its columns, never from the stored copy:
		// the stored copy still contains the content the redaction destroyed, and a
		// redaction that leaves the original readable has not redacted anything.
		if !e.Redacted {
			ev.Stored = matrix.RawMessage(e.JSON)
		}
	}
	// A state event carries state_key even when the key is the empty string.
	// Rows written before the IsState column existed are recognised by their
	// non-empty key, which is all that can be recovered about them.
	if e.IsState || e.StateKey != "" {
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
	// GetByIDs loads many events in one query, keyed by event id. A caller that
	// asks for an id the database does not have simply finds no entry for it.
	GetByIDs(ids []string) (map[string]*Event, error)
	// ByPositions loads the events of one room that sit at the given stream
	// positions, keyed by position: one query for the newest reply of every thread
	// in a listing.
	ByPositions(roomID string, positions []uint64) (map[uint64]*Event, error)
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
	// ForwardExtremities lists the leaves of a room's graph, deepest first, which is
	// what a new event attaches to.
	ForwardExtremities(roomID string, limit int) ([]string, error)
	// AdvanceForwardExtremities replaces the leaves a new event cited with the new
	// event, so the graph has one leaf again rather than one per fork.
	AdvanceForwardExtremities(roomID string, consumed []string, added *ForwardExtremity) error
	// HistoryByTimestamp finds events around an origin_server_ts; "b" searches
	// backwards in time, "f" forwards.
	HistoryByTimestamp(roomID string, ts int64, dir string, limit int) ([]*Event, error)
	// Relations lists the unredacted events that relate to a target event.
	Relations(query RelationQuery) ([]*Event, error)
	// LatestActivity returns the stream position of the most recent event each
	// sender wrote in a room; room summaries use it to pick the members a client
	// is most likely to care about.
	LatestActivity(roomID string) (map[string]uint64, error)
	// ThreadsFor lists the m.thread relations of a room whose root is one of
	// the given event ids, oldest first.
	ThreadsFor(roomID string, rootIDs []string) ([]*Event, error)
	// ThreadStats aggregates the unredacted m.thread replies of a room per root,
	// which is what the threads listing needs: counting them in SQL instead of
	// loading every reply of every thread.
	ThreadStats(roomID, userID string) ([]ThreadStat, error)
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

// GetByIDs implements [EventRepo]. One SELECT covers every id, which is the
// point: the sync and history paths hydrate state blocks and notification lists
// from id columns, and doing that one row at a time is an N+1 on the hot path.
func (r *EventRepoImpl) GetByIDs(ids []string) (map[string]*Event, error) {
	events, err := r.ListByIDs(ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Event, len(events))
	for _, event := range events {
		out[event.EventID] = event
	}
	return out, nil
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

// hiddenFromTimeline keeps two kinds of event out of what clients read as a room's
// history: events that failed their auth checks against the room's current state - the
// specification is precise about that, they are stored and later events may cite them,
// but they are not sent to clients and not applied to the state - and events that only
// arrived as another server's state. Federation and state reads, which do have to see
// both, do not call this.
func hiddenFromTimeline(q *gorm.DB) *gorm.DB {
	return q.Where("soft_failed = ? AND not_in_timeline = ?", false, false)
}

// History implements [EventRepo].
func (r *EventRepoImpl) History(roomID string, from uint64, dir string, limit int, notBefore uint64) ([]*Event, error) {
	var events []*Event
	q := hiddenFromTimeline(r.db.Where("room_id = ?", roomID))
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
	q := hiddenFromTimeline(r.db.Where("room_id IN ? AND stream_pos > ?", roomIDs, since)).
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

// ForwardExtremities implements [EventRepo].
func (r *EventRepoImpl) ForwardExtremities(roomID string, limit int) ([]string, error) {
	var ids []string
	q := r.db.Model(&ForwardExtremity{}).Where("room_id = ?", roomID)
	q = q.Order("depth desc, origin_server_ts desc, event_id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Pluck("event_id", &ids).Error
	return ids, err
}

// AdvanceForwardExtremities implements [EventRepo].
func (r *EventRepoImpl) AdvanceForwardExtremities(roomID string, consumed []string, added *ForwardExtremity) error {
	if len(consumed) > 0 {
		if err := r.db.Where("room_id = ? AND event_id IN ?", roomID, consumed).
			Delete(&ForwardExtremity{}).Error; err != nil {
			return err
		}
	}
	if added == nil {
		return nil
	}
	added.RoomID = roomID
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(added).Error
}

// HistoryByTimestamp implements [EventRepo].
func (r *EventRepoImpl) HistoryByTimestamp(roomID string, ts int64, dir string, limit int) ([]*Event, error) {
	var events []*Event
	q := hiddenFromTimeline(r.db.Where("room_id = ?", roomID))
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

// RelationQuery narrows [EventRepo.Relations].
type RelationQuery struct {
	RoomID  string
	EventID string
	// RelType and EventType narrow the listing; an empty string matches all.
	RelType   string
	EventType string
	Limit     int
	// From and To are stream positions. Which one bounds the start depends on
	// the direction: forward reads above From and below To, backwards reads
	// below From and above To.
	From uint64
	To   uint64
	// Forward reads oldest first; the zero value walks newest first.
	Forward bool
}

// Relations implements [EventRepo].
//
// Redacted relations are never listed. MSC2675 (merged; the "Redactions" part
// of "Event relationships" in the v1.19 client-server spec) says redacted
// relations "should not be taken into consideration in bundled aggregations, nor
// should they be returned from /relations". The target of the relation is not
// covered by that rule: the same MSC states that requesting /relations on a
// redacted event still returns its relations, in line with /context and
// /messages.
func (r *EventRepoImpl) Relations(query RelationQuery) ([]*Event, error) {
	var events []*Event
	q := hiddenFromTimeline(r.db.Where("room_id = ? AND relates_to = ? AND redacted = ?",
		query.RoomID, query.EventID, false))
	if query.RelType != "" {
		q = q.Where("rel_type = ?", query.RelType)
	}
	if query.EventType != "" {
		q = q.Where("type = ?", query.EventType)
	}
	if query.Forward {
		if query.From > 0 {
			q = q.Where("stream_pos > ?", query.From)
		}
		if query.To > 0 {
			q = q.Where("stream_pos < ?", query.To)
		}
	} else {
		if query.From > 0 {
			q = q.Where("stream_pos < ?", query.From)
		}
		if query.To > 0 {
			q = q.Where("stream_pos > ?", query.To)
		}
	}
	if query.Limit > 0 {
		q = q.Limit(query.Limit)
	}
	order := "stream_pos desc"
	if query.Forward {
		order = "stream_pos asc"
	}
	err := q.Order(order).Find(&events).Error
	return events, err
}

// ByPositions implements [EventRepo]. Stream positions are unique; the room
// filter keeps a position from another room out of the answer.
func (r *EventRepoImpl) ByPositions(roomID string, positions []uint64) (map[uint64]*Event, error) {
	out := map[uint64]*Event{}
	if len(positions) == 0 {
		return out, nil
	}
	var events []*Event
	if err := hiddenFromTimeline(r.db.Where("room_id = ? AND stream_pos IN ?", roomID, positions)).
		Find(&events).Error; err != nil {
		return nil, err
	}
	for _, event := range events {
		out[event.StreamPos] = event
	}
	return out, nil
}

// LatestActivity implements [EventRepo]. It scans the events of one room, which
// is why callers only ask when a room has more members than they need.
func (r *EventRepoImpl) LatestActivity(roomID string) (map[string]uint64, error) {
	var rows []struct {
		Sender string
		Pos    uint64
	}
	err := r.db.Model(&Event{}).Where("room_id = ?", roomID).
		Select("sender, max(stream_pos) as pos").Group("sender").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(rows))
	for _, row := range rows {
		out[row.Sender] = row.Pos
	}
	return out, nil
}

// ThreadStat is one aggregated thread: the root it hangs under and how big it
// has grown.
type ThreadStat struct {
	RootID    string
	Replies   int64
	LatestPos uint64
	// Mine counts the replies the requester sent, the input of
	// current_user_participated.
	Mine int64
}

// ThreadStats implements [EventRepo]. Redacted replies stay out of the numbers,
// which is MSC2675's rule for bundled aggregations.
func (r *EventRepoImpl) ThreadStats(roomID, userID string) ([]ThreadStat, error) {
	var stats []ThreadStat
	err := r.db.Model(&Event{}).
		Where("room_id = ? AND rel_type = ? AND relates_to <> ? AND redacted = ?",
			roomID, "m.thread", "", false).
		Select("relates_to AS root_id, COUNT(*) AS replies, MAX(stream_pos) AS latest_pos, "+
			"SUM(CASE WHEN sender = ? THEN 1 ELSE 0 END) AS mine", userID).
		Group("relates_to").Scan(&stats).Error
	return stats, err
}

// ThreadsFor implements [EventRepo], with the same redaction rule as the thread
// aggregation.
func (r *EventRepoImpl) ThreadsFor(roomID string, rootIDs []string) ([]*Event, error) {
	if len(rootIDs) == 0 {
		return nil, nil
	}
	var events []*Event
	err := r.db.Where("room_id = ? AND rel_type = ? AND relates_to IN ? AND redacted = ?",
		roomID, "m.thread", rootIDs, false).
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
	q := hiddenFromTimeline(r.db.Where("room_id IN ? AND redacted = ?", query.RoomIDs, false))
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
