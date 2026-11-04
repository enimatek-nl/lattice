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
	// To bounds the page at the other end. A client that asks for a window between two tokens is
	// asking for a piece of the room's history, and answering with everything from `from` forward
	// hands it the rest of the room as well.
	To string
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
	dir, err := parseDirection(params.Dir, "b")
	if err != nil {
		return nil, err
	}
	limit := params.Limit
	if limit <= 0 || limit > 1000 {
		limit = 10
	}
	from, err := s.ParseToken(params.From)
	if err != nil {
		return nil, err
	}
	to, err := s.ParseToken(params.To)
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
	// MSC2675: which events of the window the user may see depends on when they were in the
	// room. The page's cursors stay those of the window that was walked, filtered or not, so
	// a client paging on never lands on a position it was never told about.
	gate, err := s.historyGateFor(ctx, params.RoomID, params.UserID)
	if err != nil {
		return nil, err
	}
	// The page ends where the client said it does. `from` and `to` name a window; answering with
	// everything from `from` forward hands over the rest of the room as well, and a client that
	// asked for a piece of history to render one stretch of it is given the room instead.
	if to > 0 {
		// The near bound is exclusive and the far one inclusive, which is how the two ends of a
		// walk differ: the token a page starts from has already been read, the token it stops at
		// has not. A client paging with `from=<previous end>&to=<somewhere>` would otherwise lose
		// the event sitting at `to` and see a hole in the room.
		bounded := make([]*persist.Event, 0, len(events))
		for _, event := range events {
			if dir == "f" {
				if event.StreamPos <= to {
					bounded = append(bounded, event)
				}
				continue
			}
			if event.StreamPos >= to {
				bounded = append(bounded, event)
			}
		}
		events = bounded
	}
	readable, _ := gate.keepReadable(events)
	// A former member of a room whose history opened at the join reads it as of their leave:
	// the pagination cursors stay the room's own, so paging on still walks the room, but what
	// arrives is what they were told while they were in it.
	if bound, ok := s.HistoryBound(ctx, params.RoomID, params.UserID); ok {
		until := make([]*persist.Event, 0, len(readable))
		for _, event := range readable {
			// Up to and including the bound: the event that ended the membership is part of
			// what the reader may see - it is their own membership - and it is the newest thing
			// in the history they are entitled to, so a capped page ends there rather than
			// reaching an event older still.
			if event.StreamPos <= bound {
				until = append(until, event)
			}
		}
		readable = until
	}
	result := &HistoryResult{Chunk: toWire(readable)}
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

	// A filter that asks for lazy loaded members gets a state block of one thing: the membership
	// events of the people whose events this page carries, as the room had them when those events
	// were written. That is the whole point of the request - a client paging through a room it is
	// not looking at does not want the room's member list, and the member list is most of what a
	// state block is made of.
	if wantsLazyMembers(params.Filter) {
		state, err := s.lazyMemberState(params.RoomID, events)
		if err != nil {
			return nil, err
		}
		result.State = state
		return result, nil
	}

	// Backward pagination starts in the past, so the state at the start of the
	// window has to be supplied as well.
	if dir == "b" {
		// The state of a page is the room's current state. Only the newest version of a
		// state key is kept, so the state as it stood at the start of the page cannot be
		// rebuilt; what is sent is the room as it is now, which is what a client paging
		// backwards uses to name the room it is looking at.
		// The state of a page is the state the room held when the page began, rebuilt from the
		// versions the room wrote of each key. Today's state would name the room the client is
		// paging through as it is now, which is a different room than the one in front of them.
		state, err := s.stateAsOf(ctx, params.RoomID, events[0].StreamPos, nil, nil, nil, params.UserID)
		if err != nil {
			return nil, err
		}
		result.State = state
	}
	return result, nil
}

// parseDirection validates a pagination direction. The specification defaults
// dir to "b" for /messages, /relations and /threads, so that is the fallback
// here too; timestamp_to_event asks for "f" and passes its own default.
func parseDirection(dir, fallback string) (string, error) {
	if dir == "" {
		return fallback, nil
	}
	if dir != "b" && dir != "f" {
		return "", matrix.ErrInvalidParam("dir must be \"f\" or \"b\", got %q", dir)
	}
	return dir, nil
}

// RoomEvent returns a single event of a room.
// RoomEvent answers GET /rooms/{roomID}/event/{eventID}.
//
// Every refusal here is a 404 rather than a 403. The specification says the event is not
// found when the user may not read it, and Complement checks exactly that: an answer that
// distinguishes "there is no such event" from "there is one and you may not see it" tells a
// caller what the room holds, which is the very thing history visibility exists to hide.
// The reads that name a *room* - /messages, /context, /state - keep the 403 that says the
// room itself is closed.
func (s *Service) RoomEvent(ctx context.Context, roomID, eventID, userID string) (*matrix.Event, error) {
	if err := s.RequireReadable(ctx, roomID, userID); err != nil {
		return nil, matrix.ErrNotFound("no event %s in room %s", eventID, roomID)
	}
	event, err := s.db.EventRepo.GetInRoom(roomID, eventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no event %s in room %s", eventID, roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	if event.SoftFailed {
		// An event this server's state does not support is not this room's history as
		// far as clients are concerned, whichever way they ask for it by id.
		return nil, matrix.ErrNotFound("no event %s in room %s", eventID, roomID)
	}
	gate, err := s.historyGateFor(ctx, roomID, userID)
	if err != nil {
		return nil, err
	}
	if !gate.allows(event) {
		// The event is in the room and the user may read the room; what says no is the room's
		// own decision about when its history opened. The answer is the same 404 an event that
		// does not exist earns, for the reason above.
		return nil, matrix.ErrNotFound("no event %s in room %s", eventID, roomID)
	}
	wire := event.Matrix()
	// MSC2675 lists GET /rooms/{roomId}/event/{eventId} among the endpoints that
	// bundle a thread's summary, so a client opening one event sees the same
	// unsigned.m.thread block it gets from /messages or /sync.
	annotated := []matrix.Event{wire}
	if err := s.annotateThreads(roomID, userID, annotated); err != nil {
		return nil, err
	}
	return &annotated[0], nil
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
	gate, err := s.historyGateFor(ctx, roomID, userID)
	if err != nil {
		return nil, err
	}
	if !gate.allows(center) {
		return nil, matrix.ErrForbidden("this room hides events from before your membership")
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
	// The state around the centre event is the state that room was carrying when that event was
	// written - not the room's current state. A name, a topic or a membership that moved since
	// then would otherwise be shown as the context of an event that never saw it, and in a forked
	// history "moved since then" is exactly where today's state and that event's state part.
	state, err := s.stateAsOf(ctx, roomID, center.StreamPos, nil, nil, nil, userID)
	if err != nil {
		return nil, err
	}
	// The neighbour blocks are history and follow the room's decision about when it opened.
	before, _ = gate.keepReadable(before)
	after, _ = gate.keepReadable(after)
	result := &EventContextResult{
		Event:        center.Matrix(),
		EventsBefore: toWire(before),
		EventsAfter:  toWire(after),
		State:        state,
	}
	// The centre event goes through the same helper the neighbour blocks use, so
	// /context carries unsigned.m.thread for the event it is about as well.
	centre := []matrix.Event{result.Event}
	if err := s.annotateThreads(roomID, userID, centre); err != nil {
		return nil, err
	}
	result.Event = centre[0]
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
	dir, err := parseDirection(dir, "f")
	if err != nil {
		return nil, err
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

// Relations lists the events that relate to an event, paginated the way
// /messages is: "b" (the default) walks newest first from the `from` token, "f"
// walks oldest first from it, and `to` bounds the far end of the window.
func (s *Service) Relations(ctx context.Context, roomID, eventID, relType, eventType, userID string, limit int, from, to, dir string) ([]matrix.Event, string, error) {
	if err := s.RequireReadable(ctx, roomID, userID); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 1000 {
		limit = 10
	}
	direction, err := parseDirection(dir, "b")
	if err != nil {
		return nil, "", err
	}
	fromPos, err := s.ParseToken(from)
	if err != nil {
		return nil, "", err
	}
	toPos, err := s.ParseToken(to)
	if err != nil {
		return nil, "", err
	}
	events, err := s.db.EventRepo.Relations(persist.RelationQuery{
		RoomID:    roomID,
		EventID:   eventID,
		RelType:   relType,
		EventType: eventType,
		Limit:     limit,
		From:      fromPos,
		To:        toPos,
		Forward:   direction == "f",
	})
	if err != nil {
		return nil, "", matrix.ErrInternal(err)
	}
	next := ""
	if len(events) == limit {
		// The cursor names a position in the room, and is taken before the visibility filter
		// so that paging continues where the walk stopped rather than where the showing did.
		next = s.Token(events[len(events)-1].StreamPos)
	}
	gate, err := s.historyGateFor(ctx, roomID, userID)
	if err != nil {
		return nil, "", err
	}
	events, _ = gate.keepReadable(events)
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
	// page when walking backwards, or of the next one when walking forwards.
	From string
	// To bounds the far end of the window; it is the counterpart of From.
	To    string
	Dir   string
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

// Threads lists the threads of a room, by default the most recently active
// first. The counting happens in the database (EventRepo.ThreadStats) and the
// roots and latest replies are then loaded in two batched reads, so the cost
// grows with the number of threads rather than with the number of replies.
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
	dir, err := parseDirection(params.Dir, "b")
	if err != nil {
		return nil, "", err
	}
	from, err := s.ParseToken(params.From)
	if err != nil {
		return nil, "", err
	}
	to, err := s.ParseToken(params.To)
	if err != nil {
		return nil, "", err
	}
	stats, err := s.db.EventRepo.ThreadStats(params.RoomID, params.UserID)
	if err != nil {
		return nil, "", matrix.ErrInternal(err)
	}

	// Every thread needs its root (who started it, is it still there) and its
	// most recent reply (what the listing shows), so both sets are loaded in one
	// query each.
	rootIDs := make([]string, 0, len(stats))
	positions := make([]uint64, 0, len(stats))
	for _, stat := range stats {
		lastPos := stat.LatestPos
		rootIDs = append(rootIDs, stat.RootID)
		positions = append(positions, lastPos)
	}
	roots, err := s.db.EventRepo.GetByIDs(rootIDs)
	if err != nil {
		return nil, "", matrix.ErrInternal(err)
	}
	latestByPos, err := s.db.EventRepo.ByPositions(params.RoomID, positions)
	if err != nil {
		return nil, "", matrix.ErrInternal(err)
	}

	// An empty listing answers with an empty chunk, never null: a client reads
	// chunk as an array.
	threads := make([]Thread, 0, len(stats))
	for _, stat := range stats {
		root, ok := roots[stat.RootID]
		if !ok || root.RoomID != params.RoomID {
			// A thread whose root is missing (or lives in another room) is not
			// listed.
			continue
		}
		if include == "mine" && root.Sender != params.UserID && stat.Mine == 0 {
			continue
		}
		lastPos := stat.LatestPos
		if root.StreamPos > lastPos {
			lastPos = root.StreamPos
		}
		// Threads are paginated by their most recent activity, so the window the
		// tokens describe is measured in that position: walking backwards reads
		// below `from` down to `to`, walking forwards reads above `from` up to
		// `to`.
		if !positionInWindow(lastPos, from, to, dir == "f") {
			continue
		}
		latest := root
		if reply, ok := latestByPos[stat.LatestPos]; ok {
			latest = reply
		}
		threads = append(threads, Thread{
			Root:        root.Matrix(),
			LatestEvent: latest.Matrix(),
			Count:       stat.Replies,
			NumEvents:   stat.Replies + 1,
			LastPos:     lastPos,
		})
	}
	if dir == "f" {
		sort.Slice(threads, func(i, j int) bool { return threads[i].LastPos < threads[j].LastPos })
	} else {
		sort.Slice(threads, func(i, j int) bool { return threads[i].LastPos > threads[j].LastPos })
	}
	if relationship == "latest" && len(threads) > 1 {
		// "latest" asks for the single most recently active thread of the window,
		// whichever direction the listing walks in.
		newest := threads[0]
		for _, thread := range threads[1:] {
			if thread.LastPos > newest.LastPos {
				newest = thread
			}
		}
		threads = []Thread{newest}
	}
	nextBatch := ""
	if len(threads) > limit {
		threads = threads[:limit]
		nextBatch = s.Token(threads[limit-1].LastPos)
	}
	return threads, nextBatch, nil
}

// positionInWindow reports whether a stream position lies inside the pagination
// window described by the from and to tokens for the requested direction. Both
// bounds are exclusive, which is how /messages treats its tokens.
func positionInWindow(pos, from, to uint64, forward bool) bool {
	if forward {
		return (from == 0 || pos > from) && (to == 0 || pos < to)
	}
	return (from == 0 || pos < from) && (to == 0 || pos > to)
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

// historyGate answers the half of MSC2675 that a room-level permission check cannot: not
// whether a user may read a room, but which of its events they may read. A room whose history
// visibility is `joined` shows a member the events that were sent while they were a member, and
// `invited` opens the history from the invite onwards. Without this half, a member who joined
// last week can ask for the room's first event by id and be answered, in a room that decided
// they should not see it.
//
// The gate reads the room's membership events for this user once and answers from them. A room
// that named `shared`, named nothing, or named a visibility this server does not know needs
// none of that - the room is open to everyone the room-level check admitted, which is what
// every read on this server has always done - so the common case costs no query at all.
type historyGate struct {
	visibility string
	userID     string
	// positions and memberships are where this user's membership of the room changed and
	// what it became there, oldest first: the room's own record of the user's comings and
	// goings.
	positions   []uint64
	memberships []string
}

// historyGateFor builds the gate for one room and one user. Callers have already decided that
// the user may read the room at all; the gate only answers the "when" half.
func (s *Service) historyGateFor(ctx context.Context, roomID, userID string) (*historyGate, error) {
	gate := &historyGate{visibility: s.HistoryVisibility(ctx, roomID), userID: userID}
	if gate.visibility != matrix.HistoryJoined && gate.visibility != matrix.HistoryInvited {
		return gate, nil
	}
	rows, err := s.db.EventRepo.StateHistory(roomID, matrix.TypeMember, userID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	for _, row := range rows {
		var content matrix.MemberContent
		if err := matrix.DecodeContent(matrix.RawMessage(row.Content), &content); err != nil || content.Membership == "" {
			continue
		}
		gate.positions = append(gate.positions, row.StreamPos)
		gate.memberships = append(gate.memberships, content.Membership)
	}
	return gate, nil
}

// allows reports whether this user may read this event.
func (g *historyGate) allows(event *persist.Event) bool {
	switch g.visibility {
	case matrix.HistoryJoined, matrix.HistoryInvited:
	default:
		return true
	}
	// A user's own membership events are theirs whatever the room decided. A client that
	// cannot see the event which opened or closed their membership cannot tell the user why
	// a room appeared or went away.
	if event.Type == matrix.TypeMember && event.StateKey == g.userID {
		return true
	}
	switch g.membershipAt(event.StreamPos) {
	case matrix.MembershipJoin:
		return true
	case matrix.MembershipInvite:
		// An invite is the point at which `invited` opened the room to this user; for
		// `joined` it is not enough, because the room chose the join as that point.
		return g.visibility == matrix.HistoryInvited
	}
	return false
}

// membershipAt is what the user's membership was at a position: the last change at or before it,
// and nothing when the room says the user was not in it yet.
func (g *historyGate) membershipAt(pos uint64) string {
	membership := ""
	for i, at := range g.positions {
		if at > pos {
			break
		}
		membership = g.memberships[i]
	}
	return membership
}

// keepReadable drops from a batch the events this user may not read, and says how many it
// dropped: a caller that trimmed a page wants to know that it did.
func (g *historyGate) keepReadable(events []*persist.Event) ([]*persist.Event, int) {
	if g.visibility != matrix.HistoryJoined && g.visibility != matrix.HistoryInvited {
		return events, 0
	}
	kept := make([]*persist.Event, 0, len(events))
	for _, event := range events {
		if g.allows(event) {
			kept = append(kept, event)
		}
	}
	return kept, len(events) - len(kept)
}

// historyGates holds one gate per room for a read that crosses rooms - a search, a sync - so
// that a hundred rooms cost a hundred cheap questions rather than one per event.
type historyGates struct {
	service *Service
	ctx     context.Context
	userID  string
	gates   map[string]*historyGate
}

func (s *Service) historyGates(ctx context.Context, userID string) *historyGates {
	return &historyGates{service: s, ctx: ctx, userID: userID, gates: map[string]*historyGate{}}
}

// allows answers for the room the event belongs to, building that room's gate the first time
// an event of it arrives.
func (h *historyGates) allows(event *persist.Event) (bool, error) {
	gate, ok := h.gates[event.RoomID]
	if !ok {
		var err error
		gate, err = h.service.historyGateFor(h.ctx, event.RoomID, h.userID)
		if err != nil {
			return false, err
		}
		h.gates[event.RoomID] = gate
	}
	return gate.allows(event), nil
}

// lazyMemberState builds the state block of a lazy loaded page: for each sender in the chunk,
// the membership event the room held for them at the point their own newest event in the chunk
// was written. A sender the room never described is simply absent.
func (s *Service) lazyMemberState(roomID string, events []*persist.Event) ([]matrix.Event, error) {
	newest := map[string]uint64{}
	for _, event := range events {
		if event.Sender == "" {
			continue
		}
		if event.StreamPos > newest[event.Sender] {
			newest[event.Sender] = event.StreamPos
		}
	}
	type entry struct {
		sender string
		event  matrix.Event
	}
	entries := make([]entry, 0, len(newest))
	for sender, pos := range newest {
		member, err := s.stateEventUntil(roomID, matrix.TypeMember, sender, pos)
		if err != nil {
			if persist.IsNotFound(err) {
				// The room never said what this user was: a peer's event whose membership the
				// room never carried is not a membership event this server can invent.
				continue
			}
			return nil, err
		}
		entries = append(entries, entry{sender: sender, event: member.Matrix()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sender < entries[j].sender })
	out := make([]matrix.Event, 0, len(entries))
	for _, item := range entries {
		out = append(out, item.event)
	}
	return out, nil
}

// lazySection is one filter section that may ask for lazy loading, in either spelling.
type lazySection struct {
	LazyLoadMembers *bool      `json:"lazy_load_members"`
	LazyLoadOptions lazyOption `json:"lazy_load_options"`
}

// wantsLazyMembers reads a lazy-loading request out of the `filter` a /messages request carries.
// Clients write it three ways: flat on the filter itself, inside the `room_event_filter` wrapper a
// stored filter document uses, and in the MSC4222 `lazy_load_options` block. A server that reads
// one spelling answers the other two with the room's whole member list.
func wantsLazyMembers(raw string) bool {
	if raw == "" {
		return false
	}
	var doc struct {
		lazySection
		RoomEventFilter struct {
			lazySection
		} `json:"room_event_filter"`
		Room struct {
			State    lazySection `json:"state"`
			Timeline lazySection `json:"timeline"`
		} `json:"room"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return false
	}
	for _, section := range []lazySection{
		doc.lazySection,
		doc.RoomEventFilter.lazySection,
		doc.Room.State,
		doc.Room.Timeline,
	} {
		if (section.LazyLoadMembers != nil && *section.LazyLoadMembers) || section.LazyLoadOptions.LazyLoadMembers {
			return true
		}
	}
	return false
}
