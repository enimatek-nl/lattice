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

// noteRelation indexes the relation an event claims, if it claims one.
//
// `m.relates_to` is content, and content is what a signature covers, so the claim is stored once
// — inside the event. This row is only a way to ask "what points at this event" without reading
// the room. It is written at the same funnel that stores the event, which is what keeps a locally
// sent edit and one that arrived over federation in one index rather than two.
//
// Two shapes are deliberately not indexed. An `m.relates_to` with an `event_id` and no `rel_type`
// is a reply's fallback for clients that do not understand threads; the specification does not
// treat it as a relation, and inventing a relation kind for it here would change what a client
// sees in `/sync`. And an `m.relates_to` with a `key` and no `rel_type` is an emote or a reaction
// shortcut in some clients' output: without a target event there is nothing to point at.
func (s *Service) noteRelation(ctx context.Context, event map[string]any, row *persist.Event) error {
	content, _ := event["content"].(map[string]any)
	relates, _ := content["m.relates_to"].(map[string]any)
	if relates == nil {
		return nil
	}
	target, _ := relates["event_id"].(string)
	relType, _ := relates["rel_type"].(string)
	if target == "" || relType == "" {
		return nil
	}
	if err := s.relations.Put(ctx, &persist.Relation{
		EventID:        row.EventID,
		RoomID:         row.RoomID,
		Sender:         row.Sender,
		EventType:      row.Type,
		RelType:        relType,
		TargetEventID:  target,
		OriginServerTS: row.OriginServerTS,
		StreamOrdering: row.StreamOrdering,
	}); err != nil {
		return fmt.Errorf("rooms: cannot index the relation of %s: %w", row.EventID, err)
	}
	return nil
}

// RelationsRequest is one page of what relates to one event, as `/relations` asks for it.
type RelationsRequest struct {
	// RoomID is the room to look in. A relation is a fact about one room: an event cannot
	// relate to an event in another room and have that mean anything to a client.
	RoomID string
	// TargetEventID is the event being asked about.
	TargetEventID string
	// RelType and EventType narrow the page, exactly as the two extra path segments of
	// `/relations/{eventID}/{relType}/{eventType}` do. Empty means "any".
	RelType   string
	EventType string
	// From is the pagination token. Empty means the beginning of the direction being walked,
	// which is where a client that has never asked starts.
	From string
	// Dir is "f" for oldest first or "b" for newest first; empty is "b", which is what the
	// specification makes the default and what a client reading a thread's tail expects.
	Dir string
	// Limit is the maximum number of events; zero means the default.
	Limit int
	// Requester is who is asking, which decides how far back they may read.
	Requester string
}

// RelationsPage is the response.
type RelationsPage struct {
	Chunk []map[string]any `json:"chunk"`
	// NextBatch continues in the same direction. It is omitted when there is nothing further
	// this requester may read, which is how a client paginating a thread knows it has reached
	// the end — the same rule `/messages` follows for `end`.
	NextBatch string `json:"next_batch,omitempty"`
	// PrevBatch continues in the opposite direction from the first event of this page, so a
	// client that walked to the newest reply can walk back without starting over.
	PrevBatch string `json:"prev_batch,omitempty"`
}

// Relations returns what relates to one event.
//
// The same entitlement question as every other room read is asked first: a room this server has
// never heard of answers 403 rather than 404, and a former member reads only up to the moment
// they left. Then the page is read in the direction asked for, with the requester's horizon
// applied after reading one past the limit — so a short page means the relations ended, while a
// full page trimmed by the horizon means the requester simply may not read further, which is the
// difference a client needs in order to know whether to ask again.
//
// An unknown target event answers an empty page rather than an error. The alternative, saying
// "no such event", tells a non-member that an event identifier is not in use in this room, and
// an empty page is the same answer to a legitimate client.
func (s *Service) Relations(ctx context.Context, req RelationsRequest) (RelationsPage, error) {
	if err := s.requireHistoryAccess(ctx, req.RoomID, req.Requester); err != nil {
		return RelationsPage{}, err
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
			return RelationsPage{}, matrixerr.New(matrixerr.CodeInvalidParam,
				"The pagination token is not one this server gave out.")
		}
		from = int64(position)
	}
	// One more row than asked for, to find out whether there is more after this page.
	rows, err := s.relations.ForTarget(ctx, req.RoomID, req.TargetEventID, req.RelType, req.EventType, from, backwards, limit+1)
	if err != nil {
		return RelationsPage{}, fmt.Errorf("rooms: cannot read the relations of %s: %w", req.TargetEventID, err)
	}
	horizon, err := s.readHorizon(ctx, req.RoomID, req.Requester)
	if err != nil {
		return RelationsPage{}, err
	}

	page := RelationsPage{Chunk: make([]map[string]any, 0, limit)}
	more := false
	hiddenByHorizon := false
	for index := range rows {
		if len(page.Chunk) == limit {
			more = true
			break
		}
		row := rows[index]
		if horizon > 0 && row.StreamOrdering > horizon {
			hiddenByHorizon = true
			continue
		}
		stored, err := s.events.ByID(ctx, row.EventID)
		if err != nil {
			if notFound(err) {
				// The index names an event this server no longer holds. Serving nothing
				// for it is the only honest answer; the index entry itself is what a
				// redaction or a room purge would have left behind.
				continue
			}
			return RelationsPage{}, err
		}
		if stored.Outlier || stored.SoftFailed {
			// Not accepted into the timeline, so not something to show as a reply.
			continue
		}
		body, err := s.serve.ServeAs(ctx, stored, req.Requester)
		if err != nil {
			if errors.Is(err, redaction.ErrWithheld) {
				continue
			}
			return RelationsPage{}, err
		}
		page.Chunk = append(page.Chunk, body)
		if page.PrevBatch == "" {
			page.PrevBatch = stream.Position(row.StreamOrdering).String()
		}
		page.NextBatch = stream.Position(row.StreamOrdering).String()
	}
	if !more || hiddenByHorizon {
		page.NextBatch = ""
	}
	return page, nil
}

// RelationCount answers how many events relate to one event, of one kind if a kind is named.
// It is the number behind a thread's "N replies", and it is a count of what this server holds,
// not of what the requester may read — a count that changed per reader would let someone measure
// the room through the visibility rules.
func (s *Service) RelationCount(ctx context.Context, roomID, targetEventID, relType string) (int64, error) {
	count, err := s.relations.CountForTarget(ctx, roomID, targetEventID, relType, nil, nil, 0)
	if err != nil {
		return 0, fmt.Errorf("rooms: cannot count the relations of %s: %w", targetEventID, err)
	}
	return count, nil
}
