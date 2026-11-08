package rooms

import (
	"context"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/redaction"
	"github.com/enimatek/lattice/internal/stream"
)

// relationThread is the relation kind that makes a thread. It is spelled here rather than in the
// storage layer because it is a protocol name, and the storage layer's job is to index whatever
// relation kinds events claim, not to know which ones have a page of their own.
const relationThread = "m.thread"

// ThreadsRequest is one page of a room's threads, as `/threads` asks for it.
type ThreadsRequest struct {
	RoomID string
	// Include and Exclude are MSC3440's event-type lists. They decide which events count as
	// part of a thread — and so which thread is the newest and how big it is — not merely which
	// ones get printed, which is why they reach the query rather than a loop over the results.
	Include []string
	Exclude []string
	// From is a pagination token, compared against each thread's newest event.
	From string
	// Dir is "f" or "b"; empty is "b", newest thread first.
	Dir   string
	Limit int
	// Requester is who is asking.
	Requester string
}

// Thread is one entry of the list: the thread's root event, with the thread's aggregation
// carried inside it under `unsigned.m.relations.m.thread`.
//
// MSC3440's draft asked for three sibling fields — `event`, `latest_event`, `total` — and what
// every implementation and client settled on is the shape this is: the event itself, bundled the
// way a bundled relation is bundled elsewhere in the timeline. It is also the shape that survives
// a client already knowing how to read a room: a thread root in a chunk is an event, and the
// thread's last word and its count hang off it where an edit's replacement already does.
type Thread struct {
	Event        map[string]any `json:"-"`
	LatestEvent  map[string]any
	Total        int64
	Participated bool
}

// ThreadsPage is the response.
type ThreadsPage struct {
	Chunk     []map[string]any `json:"chunk"`
	NextBatch string           `json:"next_batch,omitempty"`
	PrevBatch string           `json:"prev_batch,omitempty"`
}

// threadEntry puts one thread's aggregation inside the event it is about, which is where a
// client looks for it.
func threadEntry(root, latest map[string]any, total int64, participated bool) map[string]any {
	unsigned, _ := root["unsigned"].(map[string]any)
	if unsigned == nil {
		unsigned = map[string]any{}
	}
	relations, _ := unsigned["m.relations"].(map[string]any)
	if relations == nil {
		relations = map[string]any{}
	}
	relations[relationThread] = map[string]any{
		"latest_event":              latest,
		"count":                     total,
		"current_user_participated": participated,
	}
	unsigned["m.relations"] = relations
	root["unsigned"] = unsigned
	return root
}

// Threads returns the room's threads, newest first by default.
//
// A thread is not a thing this server stores. It is the shape that events pointing at one event
// make, which is why the same index that answers `/relations` answers this too, and why a thread
// with no replies in it is not a thread yet: nothing points at its root, so nothing orders it.
//
// Ordering is by each thread's newest event rather than by its root, and that is the whole point
// of the endpoint — a client opening a room wants the conversation somebody just took part in at
// the top, not the oldest one. The read horizon applies to the aggregation as well as to what is
// printed, so a former member's thread list and its counts stop where their reading stops, and
// `count` never reports events they may not see.
func (s *Service) Threads(ctx context.Context, req ThreadsRequest) (ThreadsPage, error) {
	if err := s.requireHistoryAccess(ctx, req.RoomID, req.Requester); err != nil {
		return ThreadsPage{}, err
	}
	backwards := req.Dir != "f"
	limit := req.Limit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	var from int64
	if req.From != "" {
		position, err := stream.Parse(req.From)
		if err != nil {
			return ThreadsPage{}, matrixerr.New(matrixerr.CodeInvalidParam,
				"The pagination token is not one this server gave out.")
		}
		from = int64(position)
	}
	horizon, err := s.readHorizon(ctx, req.RoomID, req.Requester)
	if err != nil {
		return ThreadsPage{}, err
	}
	summaries, err := s.relations.ThreadsInRoom(ctx, req.RoomID, relationThread, req.Include, req.Exclude, from, horizon, backwards, limit+1)
	if err != nil {
		return ThreadsPage{}, fmt.Errorf("rooms: cannot read the threads of %s: %w", req.RoomID, err)
	}

	page := ThreadsPage{Chunk: make([]map[string]any, 0, limit)}
	more := false
	for index := range summaries {
		if len(page.Chunk) == limit {
			more = true
			break
		}
		summary := summaries[index]
		root, latest, err := s.threadEvents(ctx, req.RoomID, summary.RootEventID, req.Requester, req.Include, req.Exclude, horizon)
		if err != nil {
			return ThreadsPage{}, err
		}
		if root == nil || latest == nil {
			// The thread's root is gone, or it has been withheld from this requester. A
			// thread cannot be shown without its root, and saying so with an absent thread is
			// better than inventing a partial one.
			continue
		}
		total, err := s.relations.CountForTarget(ctx, req.RoomID, summary.RootEventID, relationThread, req.Include, req.Exclude, horizon)
		if err != nil {
			return ThreadsPage{}, fmt.Errorf("rooms: cannot count the thread under %s: %w", summary.RootEventID, err)
		}
		// Taking part in a thread means having said something in it, which includes starting it.
		participated := root["sender"] == req.Requester
		if !participated {
			participated, err = s.relations.HasRelationBy(ctx, req.RoomID, summary.RootEventID, relationThread, req.Requester, horizon)
			if err != nil {
				return ThreadsPage{}, fmt.Errorf("rooms: cannot tell whether %s is in the thread under %s: %w", req.Requester, summary.RootEventID, err)
			}
		}
		page.Chunk = append(page.Chunk, threadEntry(root, latest, total, participated))
		if page.PrevBatch == "" {
			page.PrevBatch = stream.Position(summary.LatestStream).String()
		}
		page.NextBatch = stream.Position(summary.LatestStream).String()
	}
	if !more {
		page.NextBatch = ""
	}
	return page, nil
}

// threadEvents serves a thread's root and its newest event, in the form this requester may
// read. A redacted root is still a root — a redaction takes its content, not the fact that
// events point at it — and it is served redacted, the way `/messages` serves it.
func (s *Service) threadEvents(ctx context.Context, roomID, rootEventID, requester string, include, exclude []string, horizon int64) (root, latest map[string]any, err error) {
	rootRow, err := s.events.ByID(ctx, rootEventID)
	if err != nil {
		if notFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if rootRow.Outlier || rootRow.RoomID != roomID {
		return nil, nil, nil
	}
	rel, err := s.relations.LatestForTarget(ctx, roomID, rootEventID, relationThread, include, exclude, horizon)
	if err != nil {
		if errors.Is(err, persist.ErrNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	latestRow, err := s.events.ByID(ctx, rel.EventID)
	if err != nil {
		if notFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if latestRow.Outlier || latestRow.SoftFailed {
		return nil, nil, nil
	}
	// Neither half of the entry is served unless both are readable. A thread whose root the
	// requester was not part of when it was written is not a conversation they were invited
	// into, and handing over the newest reply while hiding the root would leave a client
	// rendering a reply to an event it cannot name.
	visible, err := s.history.Decide(ctx, []*persist.Event{rootRow, latestRow}, requester)
	if err != nil {
		return nil, nil, fmt.Errorf("rooms: cannot decide whether the reader may read this thread: %w", err)
	}
	if !visible[rootRow.EventID] || !visible[latestRow.EventID] {
		return nil, nil, nil
	}
	root, err = s.serve.ServeAs(ctx, rootRow, requester)
	if err != nil {
		if errors.Is(err, redaction.ErrWithheld) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	latest, err = s.serve.ServeAs(ctx, latestRow, requester)
	if err != nil {
		if errors.Is(err, redaction.ErrWithheld) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return root, latest, nil
}
