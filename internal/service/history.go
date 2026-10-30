package service

import (
	"context"
	"encoding/json"
	"log"
	"sort"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// HistoryParams captures the query parameters of /messages.
type HistoryParams struct {
	RoomID    string
	UserID    string
	From      string
	Dir       string
	Limit     int
	Filter    string
	NotBefore string
}

// HistoryResult is a page of room history.
type HistoryResult struct {
	Start string         `json:"start"`
	End   string         `json:"end"`
	Chunk []matrix.Event `json:"chunk"`
	State []matrix.Event `json:"state,omitempty"`
}

// History returns a page of room events around a pagination token.
//
// Tokens are stream positions; "b" walks backwards from the token and "f"
// forwards. The end token always refers to the boundary that was consumed, so
// clients can page without skipping or repeating events.
func (s *Service) History(ctx context.Context, params HistoryParams) (*HistoryResult, error) {
	if err := s.RequireReadable(ctx, params.RoomID, params.UserID); err != nil {
		return nil, err
	}
	dir := params.Dir
	if dir == "" {
		dir = "b"
	}
	if dir != "b" && dir != "f" {
		return nil, matrix.ErrInvalidParam("dir must be \"f\" or \"b\", got %q", params.Dir)
	}
	limit := params.Limit
	if limit <= 0 || limit > 1000 {
		limit = 10
	}
	from, err := s.ParseToken(params.From)
	if err != nil {
		return nil, err
	}
	notBefore, err := s.ParseToken(params.NotBefore)
	if err != nil {
		return nil, err
	}
	events, err := s.db.EventRepo.History(params.RoomID, from, dir, limit, notBefore)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	result := &HistoryResult{Chunk: toWire(events)}
	if err := s.annotateThreads(params.RoomID, params.UserID, result.Chunk); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		result.Start = s.Token(from)
		result.End = s.Token(from)
		return result, nil
	}
	if dir == "b" {
		result.Start = s.Token(events[0].StreamPos)
		result.End = s.Token(events[len(events)-1].StreamPos)
	} else {
		result.Start = s.Token(events[0].StreamPos)
		result.End = s.Token(events[len(events)-1].StreamPos + 1)
	}

	// Backward pagination starts in the past, so the state at the start of the
	// window has to be supplied as well.
	if dir == "b" {
		state, err := s.stateEvents(ctx, params.RoomID, events[0].StreamPos, nil)
		if err != nil {
			return nil, err
		}
		result.State = state
	}
	return result, nil
}

// RoomEvent returns a single event of a room.
func (s *Service) RoomEvent(ctx context.Context, roomID, eventID, userID string) (*matrix.Event, error) {
	if err := s.RequireReadable(ctx, roomID, userID); err != nil {
		return nil, err
	}
	event, err := s.db.EventRepo.GetInRoom(roomID, eventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no event %s in room %s", eventID, roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	wire := event.Matrix()
	return &wire, nil
}

// EventContextResult is the payload of /context/{eventId}.
type EventContextResult struct {
	Event        matrix.Event   `json:"event"`
	EventsBefore []matrix.Event `json:"events_before"`
	EventsAfter  []matrix.Event `json:"events_after"`
	State        []matrix.Event `json:"state"`
	Start        string         `json:"start"`
	End          string         `json:"end"`
}

// EventContext returns the neighbourhood of an event.
func (s *Service) EventContext(ctx context.Context, roomID, eventID, userID string, limit int) (*EventContextResult, error) {
	if err := s.RequireReadable(ctx, roomID, userID); err != nil {
		return nil, err
	}
	center, err := s.db.EventRepo.GetInRoom(roomID, eventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no event %s in room %s", eventID, roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	before, err := s.db.EventRepo.History(roomID, center.StreamPos, "b", limit, 0)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	after, err := s.db.EventRepo.History(roomID, center.StreamPos, "f", limit, 0)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	state, err := s.stateEvents(ctx, roomID, center.StreamPos, nil)
	if err != nil {
		return nil, err
	}
	result := &EventContextResult{
		Event:        center.Matrix(),
		EventsBefore: toWire(before),
		EventsAfter:  toWire(after),
		State:        state,
	}
	if err := s.annotateThreads(roomID, userID, result.EventsBefore); err != nil {
		return nil, err
	}
	if err := s.annotateThreads(roomID, userID, result.EventsAfter); err != nil {
		return nil, err
	}
	if len(before) > 0 {
		result.Start = s.Token(before[len(before)-1].StreamPos)
	} else {
		result.Start = s.Token(center.StreamPos)
	}
	if len(after) > 0 {
		result.End = s.Token(after[len(after)-1].StreamPos + 1)
	} else {
		result.End = s.Token(center.StreamPos + 1)
	}
	return result, nil
}

// EventByTimestamp finds the event closest to a timestamp in the requested
// direction, which is what timestamp_to_event uses to start a history window.
func (s *Service) EventByTimestamp(ctx context.Context, roomID, userID string, ts int64, dir string) (*matrix.Event, error) {
	if err := s.RequireReadable(ctx, roomID, userID); err != nil {
		return nil, err
	}
	if dir == "" {
		dir = "f"
	}
	if dir != "f" && dir != "b" {
		return nil, matrix.ErrInvalidParam("dir must be \"f\" or \"b\", got %q", dir)
	}
	if ts <= 0 {
		return nil, matrix.ErrInvalidParam("ts must be a unix timestamp in milliseconds")
	}
	events, err := s.db.EventRepo.HistoryByTimestamp(roomID, ts, dir, 1)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	if len(events) == 0 {
		if dir == "b" {
			return nil, matrix.ErrNotFound("no event found at or before %d", ts)
		}
		return nil, matrix.ErrNotFound("no event found at or after %d", ts)
	}
	wire := events[0].Matrix()
	return &wire, nil
}

// Relations lists the events that relate to an event, oldest first.
func (s *Service) Relations(ctx context.Context, roomID, eventID, relType, eventType, userID string, limit int, from, to string) ([]matrix.Event, string, error) {
	if err := s.RequireReadable(ctx, roomID, userID); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 1000 {
		limit = 10
	}
	fromPos, err := s.ParseToken(from)
	if err != nil {
		return nil, "", err
	}
	toPos, err := s.ParseToken(to)
	if err != nil {
		return nil, "", err
	}
	events, err := s.db.EventRepo.Relations(roomID, eventID, relType, eventType, limit, fromPos, toPos)
	if err != nil {
		return nil, "", matrix.ErrInternal(err)
	}
	next := ""
	if len(events) == limit {
		next = s.Token(events[len(events)-1].StreamPos)
	}
	return toWire(events), next, nil
}

// ThreadParams captures the query parameters of GET /v1/rooms/{roomId}/threads.
type ThreadParams struct {
	RoomID string
	UserID string
	// Include is "all" (every thread) or "mine" (threads the user started or
	// took part in).
	Include string
	// Relationship is "all" (every thread) or "latest" (only the most recently
	// active thread).
	Relationship string
	// From is a pagination token pointing at the last activity of the previous
	// page.
	From  string
	Limit int
}

// Thread is one entry of the /threads chunk: the root event of a thread with
// the size of the thread and its most recent reply attached.
type Thread struct {
	Root        matrix.Event `json:"-"`
	LatestEvent matrix.Event `json:"latest_event"`
	// Count counts the replies of the thread.
	Count int64 `json:"count"`
	// NumEvents counts every event of the thread, the root included.
	NumEvents int64 `json:"num_events"`
	// LastPos is the stream position of the most recent event of the thread.
	LastPos uint64 `json:"-"`
}

// MarshalJSON inlines the fields of the root event, which is the shape clients
// read from /threads.
func (t Thread) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(t.Root)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	fields["count"] = t.Count
	fields["num_events"] = t.NumEvents
	fields["latest_event"] = t.LatestEvent
	return json.Marshal(fields)
}

// Threads lists the threads of a room, most recently active first.
func (s *Service) Threads(ctx context.Context, params ThreadParams) ([]Thread, string, error) {
	if err := s.RequireReadable(ctx, params.RoomID, params.UserID); err != nil {
		return nil, "", err
	}
	include := params.Include
	if include == "" {
		include = "all"
	}
	if include != "all" && include != "mine" {
		return nil, "", matrix.ErrInvalidParam("include must be \"all\" or \"mine\", got %q", params.Include)
	}
	relationship := params.Relationship
	if relationship == "" {
		relationship = "all"
	}
	if relationship != "all" && relationship != "latest" {
		return nil, "", matrix.ErrInvalidParam("relationship must be \"all\" or \"latest\", got %q", params.Relationship)
	}
	limit := params.Limit
	if limit <= 0 || limit > 1000 {
		limit = 10
	}
	from, err := s.ParseToken(params.From)
	if err != nil {
		return nil, "", err
	}
	replies, err := s.db.EventRepo.Threads(params.RoomID)
	if err != nil {
		return nil, "", matrix.ErrInternal(err)
	}

	type digest struct {
		count        int64
		latest       *persist.Event
		lastPos      uint64
		participated bool
	}
	byRoot := map[string]*digest{}
	for _, event := range replies {
		if event.RelatesTo == "" {
			continue
		}
		entry, ok := byRoot[event.RelatesTo]
		if !ok {
			entry = &digest{}
			byRoot[event.RelatesTo] = entry
		}
		entry.count++
		if event.Sender == params.UserID {
			entry.participated = true
		}
		if entry.latest == nil || event.StreamPos > entry.latest.StreamPos {
			entry.latest = event
		}
		if event.StreamPos > entry.lastPos {
			entry.lastPos = event.StreamPos
		}
	}

	var threads []Thread
	for rootID, entry := range byRoot {
		root, err := s.db.EventRepo.GetInRoom(params.RoomID, rootID)
		if err != nil {
			// A thread whose root is missing (or lives in another room) is not
			// listed.
			continue
		}
		if include == "mine" && root.Sender != params.UserID && !entry.participated {
			continue
		}
		lastPos := entry.lastPos
		if root.StreamPos > lastPos {
			lastPos = root.StreamPos
		}
		if from > 0 && lastPos >= from {
			continue
		}
		latest := root
		if entry.latest != nil {
			latest = entry.latest
		}
		threads = append(threads, Thread{
			Root:        root.Matrix(),
			LatestEvent: latest.Matrix(),
			Count:       entry.count,
			NumEvents:   entry.count + 1,
			LastPos:     lastPos,
		})
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].LastPos > threads[j].LastPos })
	if relationship == "latest" && len(threads) > 1 {
		threads = threads[:1]
	}
	nextBatch := ""
	if len(threads) > limit {
		threads = threads[:limit]
		nextBatch = s.Token(threads[limit-1].LastPos)
	}
	return threads, nextBatch, nil
}

// ReportQuery narrows the administrator's view of stored abuse reports. Empty
// strings match anything.
type ReportQuery struct {
	Kind     string
	RoomID   string
	EventID  string
	UserID   string
	Reporter string
	Limit    int
	Offset   int
}

// ReportRecord is a stored abuse report as shown to administrators.
type ReportRecord struct {
	ID           uint          `json:"id"`
	Kind         string        `json:"kind"`
	RoomID       string        `json:"room_id,omitempty"`
	EventID      string        `json:"event_id,omitempty"`
	UserID       string        `json:"user_id,omitempty"`
	Reporter     string        `json:"reporter"`
	Reason       string        `json:"reason,omitempty"`
	Score        int64         `json:"score,omitempty"`
	CreatedTS    int64         `json:"origin_server_ts"`
	EventContent matrix.Object `json:"event_content,omitempty"`
}

// reportFilter turns a service level query into a storage filter.
func reportFilter(query ReportQuery, limit, offset int) persist.ReportFilter {
	return persist.ReportFilter{
		Kind:     query.Kind,
		RoomID:   query.RoomID,
		EventID:  query.EventID,
		UserID:   query.UserID,
		Reporter: query.Reporter,
		Limit:    limit,
		Offset:   offset,
	}
}

// validReportKind reports whether kind names one of the three report kinds.
func validReportKind(kind string) bool {
	switch kind {
	case persist.ReportKindEvent, persist.ReportKindRoom, persist.ReportKindUser:
		return true
	}
	return false
}

// Reports returns the stored abuse reports, newest first, together with the
// number of reports that match regardless of the limit.
func (s *Service) Reports(ctx context.Context, query ReportQuery) ([]ReportRecord, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, matrix.ErrInternal(err)
	}
	if query.Kind != "" && !validReportKind(query.Kind) {
		return nil, 0, matrix.ErrInvalidParam("unknown report kind %q", query.Kind)
	}
	limit := query.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	filter := reportFilter(query, limit, query.Offset)
	reports, err := s.db.ReportRepo.List(filter)
	if err != nil {
		return nil, 0, matrix.ErrInternal(err)
	}
	total, err := s.db.ReportRepo.Count(reportFilter(query, 0, 0))
	if err != nil {
		return nil, 0, matrix.ErrInternal(err)
	}
	records := make([]ReportRecord, 0, len(reports))
	for _, report := range reports {
		records = append(records, reportRecord(report))
	}
	return records, total, nil
}

// reportRecord converts a stored report into its administrator-facing form.
func reportRecord(report *persist.Report) ReportRecord {
	record := ReportRecord{
		ID:        report.ID,
		Kind:      report.Kind,
		RoomID:    report.RoomID,
		EventID:   report.EventID,
		UserID:    report.UserID,
		Reporter:  report.Reporter,
		Reason:    report.Reason,
		Score:     report.Score,
		CreatedTS: report.CreatedTS,
	}
	if report.EventContent != "" {
		content := matrix.Object{}
		if err := json.Unmarshal([]byte(report.EventContent), &content); err == nil {
			record.EventContent = content
		}
	}
	return record
}

// ReportEvent records an abuse report about an event. The request is validated
// so that clients get honest errors instead of a silent success, and the report
// is stored for whoever has to act on it.
func (s *Service) ReportEvent(ctx context.Context, roomID, eventID, userID, reason string, score *int) error {
	if err := s.RequireJoined(ctx, roomID, userID); err != nil {
		return err
	}
	event, err := s.db.EventRepo.GetInRoom(roomID, eventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return matrix.ErrNotFound("no event %s in room %s", eventID, roomID)
		}
		return matrix.ErrInternal(err)
	}
	if score != nil && *score > 0 {
		return matrix.ErrInvalidParam("score must be a negative number, got %d", *score)
	}
	report := &persist.Report{
		Kind:     persist.ReportKindEvent,
		RoomID:   roomID,
		EventID:  eventID,
		Reporter: userID,
		Reason:   reason,
		// The content is copied because the reported event may be redacted
		// before anyone reads the report.
		EventContent: event.Content,
		CreatedTS:    s.NowMS(),
	}
	if score != nil {
		report.Score = int64(*score)
	}
	if err := s.db.ReportRepo.Add(report); err != nil {
		return matrix.ErrInternal(err)
	}
	log.Printf("abuse report room=%s event=%s by=%s score=%v reason=%q", roomID, eventID, userID, score, reason)
	return nil
}

// ReportRoom records a report about a room as a whole.
func (s *Service) ReportRoom(ctx context.Context, roomID, userID, reason string) error {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return err
	}
	if err := s.RequireReadable(ctx, room.ID, userID); err != nil {
		return err
	}
	report := &persist.Report{
		Kind:      persist.ReportKindRoom,
		RoomID:    room.ID,
		Reporter:  userID,
		Reason:    reason,
		CreatedTS: s.NowMS(),
	}
	if err := s.db.ReportRepo.Add(report); err != nil {
		return matrix.ErrInternal(err)
	}
	log.Printf("room report room=%s by=%s reason=%q", room.ID, userID, reason)
	return nil
}

// ReportUser records a report about a user of this homeserver.
func (s *Service) ReportUser(ctx context.Context, targetID, reporterID, reason string) error {
	if !s.UserExists(ctx, targetID) {
		return matrix.ErrNotFound("no user %s", targetID)
	}
	if targetID == reporterID {
		return matrix.ErrInvalidParam("reporting your own account is not useful")
	}
	report := &persist.Report{
		Kind:      persist.ReportKindUser,
		UserID:    targetID,
		Reporter:  reporterID,
		Reason:    reason,
		CreatedTS: s.NowMS(),
	}
	if err := s.db.ReportRepo.Add(report); err != nil {
		return matrix.ErrInternal(err)
	}
	log.Printf("user report user=%s by=%s reason=%q", targetID, reporterID, reason)
	return nil
}
