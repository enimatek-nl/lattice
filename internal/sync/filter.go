package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/enimatek/lattice/internal/eventfilter"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
)

// Filter is the part of a sync filter this server acts on.
//
// A filter is the client's request about how much to send, so the fields that change what a
// client receives are honoured, and the ones that would need features this server does not have
// yet are accepted and recorded as ignored rather than refused: refusing a filter that also asks
// for a timeline limit would break clients that only came here for the limit.
type Filter struct {
	Room struct {
		Timeline eventfilter.RoomEventFilter `json:"timeline"`
		State    eventfilter.EventFilter     `json:"state"`
	} `json:"room"`
}

// AllowsTimeline and AllowsState apply the type lists of the filter to the two event lists a
// room carries. The parts that need the event itself — the sender lists and `contains_url` — are
// applied by AllowsTimelineContent and AllowsStateContent, which run once the event is in its
// client form.
func (f Filter) AllowsTimeline(eventType string) bool { return f.Room.Timeline.AllowsType(eventType) }

// AllowsState applies the filter to state events.
func (f Filter) AllowsState(eventType string) bool { return f.Room.State.AllowsType(eventType) }

// AllowsTimelineContent is the rest of the timeline filter, read from the served event.
func (f Filter) AllowsTimelineContent(event map[string]any) bool {
	return f.Room.Timeline.AllowsContent(event)
}

// AllowsStateContent is the rest of the state filter, read from the served event.
func (f Filter) AllowsStateContent(event map[string]any) bool {
	return f.Room.State.AllowsContent(event)
}

// TimelineLimit is how many timeline events to return. The specification's default is 10.
func (f Filter) TimelineLimit() int {
	if f.Room.Timeline.Limit != nil {
		limit := *f.Room.Timeline.Limit
		// A limit of zero means "no timeline events", which a client uses when it only
		// wants the room's state. A negative limit is treated the same way rather than
		// as a request for everything.
		if limit < 0 {
			return 0
		}
		if limit > maxTimelineLimit {
			return maxTimelineLimit
		}
		return limit
	}
	return defaultTimelineLimit
}

// filter resolves the `filter` parameter, which the specification allows to be either a
// stored filter ID or an inline JSON object.
func (s *Service) filter(ctx context.Context, userID, raw string) (Filter, error) {
	if raw == "" {
		return Filter{}, nil
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		// The same check the create door runs. A filter given inline to /sync is the same
		// document, and a client that is told its filter is wrong once and not told again
		// has been trained to distrust the answer it did get.
		if err := ValidateFilter([]byte(raw)); err != nil {
			return Filter{}, err
		}
		var filter Filter
		if err := json.Unmarshal([]byte(raw), &filter); err != nil {
			return Filter{}, matrixerr.New(matrixerr.CodeInvalidParam,
				"The filter is not a valid JSON object.")
		}
		return filter, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return Filter{}, matrixerr.New(matrixerr.CodeInvalidParam,
			"A sync filter is either a filter ID or a JSON object.")
	}
	stored, err := s.filters.ByID(ctx, userID, id)
	if err != nil {
		if notFound(err) {
			// A filter ID the client made up is a bad request. Treating it as "no
			// filter" would return more events than the client asked for and let the
			// client believe its filter worked.
			return Filter{}, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"This server has no filter with ID %d.", id)
		}
		return Filter{}, fmt.Errorf("sync: cannot read filter %d: %w", id, err)
	}
	var filter Filter
	if err := json.Unmarshal([]byte(stored.Body), &filter); err != nil {
		return Filter{}, fmt.Errorf("sync: stored filter %d does not parse: %w", id, err)
	}
	return filter, nil
}

// SaveFilter stores a filter for a user and returns its ID.
//
// The document is checked before it is kept: a filter this server accepted while unable to make
// sense of it would answer every later /sync with fewer events than the client meant to ask for.
func (s *Service) SaveFilter(ctx context.Context, userID string, body []byte) (int64, error) {
	if err := ValidateFilter(body); err != nil {
		return 0, err
	}
	filter := &persist.Filter{UserID: userID, Body: string(body)}
	if err := s.filters.Save(ctx, filter); err != nil {
		return 0, fmt.Errorf("sync: cannot store the filter: %w", err)
	}
	return int64(filter.ID), nil
}

// FilterBody returns a stored filter exactly as it was sent, which is what
// `GET /user/{user}/filter/{id}` must answer.
func (s *Service) FilterBody(ctx context.Context, userID string, id int64) ([]byte, error) {
	stored, err := s.filters.ByID(ctx, userID, id)
	if err != nil {
		if notFound(err) {
			return nil, matrixerr.Newf(matrixerr.CodeNotFound, "This server has no filter with ID %d.", id)
		}
		return nil, fmt.Errorf("sync: cannot read filter %d: %w", id, err)
	}
	return []byte(stored.Body), nil
}

func notFound(err error) bool {
	return err == persist.ErrNotFound
}
