package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
)

// Filter is the part of a sync filter this server acts on.
//
// A filter is the client's request about how much to send, so the fields that change what
// a client receives are honoured, and the ones that would need features this server does
// not have yet are accepted and recorded as ignored rather than refused: refusing a
// filter that also asks for a timeline limit would break clients that only came here for
// the limit.
type Filter struct {
	Room struct {
		Timeline EventFilter `json:"timeline"`
		State    EventFilter `json:"state"`
	} `json:"room"`
}

// EventFilter is a filter over one kind of event stream.
type EventFilter struct {
	Limit    *int     `json:"limit"`
	Types    []string `json:"types"`
	NotTypes []string `json:"not_types"`
	// Senders, NotSenders, URLs, NotURLs and LazyLoadOptions are parsed so an unknown
	// field is not mistaken for a typo in a field we do support, but they do not narrow
	// anything yet.
	Senders         *[]string `json:"senders"`
	NotSenders      *[]string `json:"not_senders"`
	URLs            *[]string `json:"urls"`
	NotURLs         *[]string `json:"not_urls"`
	LazyLoadOptions any       `json:"lazy_load_options"`
	// LazyLoadMembers is read from the state filter only, which is what the reference
	// servers key off and what the specification's own guidance points at: the flag is
	// accepted on the timeline filter too, but the timeline is a list of events the client
	// asked for, not a state section that can be trimmed.
	LazyLoadMembers bool `json:"lazy_load_members"`
}

// Allows reports whether an event type survives this filter. An empty `types` list means
// "no types" — the client asked for nothing of this kind, which is how a client turns off
// state in a sync — while an absent list means "everything".
func (f EventFilter) Allows(eventType string) bool {
	if f.Types != nil {
		for _, wanted := range f.Types {
			if wanted == eventType {
				return true
			}
		}
		return false
	}
	for _, unwanted := range f.NotTypes {
		if unwanted == eventType {
			return false
		}
	}
	return true
}

// AllowsTimeline and AllowsState apply the filter to the two event lists a room carries.
func (f Filter) AllowsTimeline(eventType string) bool { return f.Room.Timeline.Allows(eventType) }

// AllowsState applies the filter to state events.
func (f Filter) AllowsState(eventType string) bool { return f.Room.State.Allows(eventType) }

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
func (s *Service) SaveFilter(ctx context.Context, userID string, body []byte) (int64, error) {
	if !json.Valid(body) {
		return 0, matrixerr.New(matrixerr.CodeInvalidParam, "A filter must be a JSON object.")
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
