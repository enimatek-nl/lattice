package service

import (
	"context"
	"sort"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// Sort orders accepted by the search endpoint.
const (
	SearchOrderRecent       = "recent"
	SearchOrderAlphabetical = "alphabetical"
	SearchOrderCreatedTS    = "create_ts"
	SearchOrderOriginTS     = "origin_server_ts"
)

// SearchCategoryRoomEvents is the only search category Lattice serves.
const SearchCategoryRoomEvents = "room_events"

// Page size bounds of the search endpoint.
const (
	searchDefaultLimit = 10
	searchMaxLimit     = 1000
)

// searchOrderings lists the sort orders this server understands.
var searchOrderings = map[string]bool{
	SearchOrderRecent:       true,
	SearchOrderAlphabetical: true,
	SearchOrderCreatedTS:    true,
	SearchOrderOriginTS:     true,
}

// SearchFilter narrows a search to a subset of events, mirroring the filter
// object of the specification.
type SearchFilter struct {
	Rooms      []string
	NotRooms   []string
	Types      []string
	NotTypes   []string
	Senders    []string
	NotSenders []string
}

// SearchParams captures the body of POST /_matrix/client/v3/search.
type SearchParams struct {
	UserID       string
	Terms        []string
	Filter       SearchFilter
	Limit        int
	OrderBy      string
	EventFields  []string
	IncludeState bool
}

// SearchHit names a matching event together with its relevance.
type SearchHit struct {
	EventID string  `json:"event_id"`
	Rank    float64 `json:"rank"`
}

// SearchChunk is the window of matching events of one room.
type SearchChunk struct {
	Chunk []matrix.Object `json:"chunk"`
	Start string          `json:"start"`
	End   string          `json:"end"`
}

// SearchRoom groups the matches of a single room.
type SearchRoom struct {
	Rank          float64     `json:"rank"`
	ResultsEvents SearchChunk `json:"results_events"`
	// State holds the state events of the room when the client asked for them
	// with include_state.
	State []matrix.Event `json:"state,omitempty"`
}

// SearchCategory summarises the matches of a search category.
type SearchCategory struct {
	Count      int64       `json:"count"`
	Limited    bool        `json:"limited"`
	Highlights []string    `json:"highlights,omitempty"`
	Results    []SearchHit `json:"results"`
}

// SearchLimits reports the limits the server applied.
type SearchLimits struct {
	MaxSize int `json:"max_size,omitempty"`
}

// SearchEcho tells the client how its request was interpreted.
type SearchEcho struct {
	Limits       SearchLimits `json:"limits"`
	OrderBy      string       `json:"order_by,omitempty"`
	Keywords     []string     `json:"keywords,omitempty"`
	EventFields  []string     `json:"event_fields,omitempty"`
	IncludeState bool         `json:"include_state"`
}

// SearchResult is the payload of the search endpoint. The deprecated revision
// answers with one category, "room_events".
type SearchResult struct {
	Categories       map[string]SearchCategory `json:"categories"`
	Results          map[string]SearchRoom     `json:"results"`
	SearchCategories map[string]SearchEcho     `json:"search_categories"`
}

// Search looks for events the requester may read whose content mentions every
// search term. Matching is a substring comparison over the serialised content,
// which keeps the search honest without pulling in a text index.
func (s *Service) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	if len(params.Terms) == 0 {
		return nil, matrix.ErrInvalidParam("at least one search term is required")
	}
	order := params.OrderBy
	if order == "" {
		order = SearchOrderRecent
	}
	if !searchOrderings[order] {
		return nil, matrix.ErrInvalidParam("unknown order_by %q", params.OrderBy)
	}
	limit := clampSearchLimit(params.Limit)
	rooms, err := s.searchableRooms(ctx, params.UserID, params.Filter)
	if err != nil {
		return nil, err
	}
	empty := &SearchResult{
		Categories: map[string]SearchCategory{SearchCategoryRoomEvents: {
			Results:    []SearchHit{},
			Highlights: params.Terms,
		}},
		Results:          map[string]SearchRoom{},
		SearchCategories: searchEcho(params, order, limit, false),
	}
	if len(rooms) == 0 {
		return empty, nil
	}
	events, err := s.db.EventRepo.Search(persist.SearchQuery{
		RoomIDs:    rooms,
		NotRooms:   params.Filter.NotRooms,
		Types:      params.Filter.Types,
		NotTypes:   params.Filter.NotTypes,
		Senders:    params.Filter.Senders,
		NotSenders: params.Filter.NotSenders,
		Terms:      params.Terms,
		Limit:      limit,
	})
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	if len(events) == 0 {
		return empty, nil
	}
	sortSearchEvents(events, order)

	result := &SearchResult{
		Categories: map[string]SearchCategory{SearchCategoryRoomEvents: {
			Count:      int64(len(events)),
			Limited:    len(events) == limit,
			Highlights: params.Terms,
			Results:    make([]SearchHit, 0, len(events)),
		}},
		Results:          map[string]SearchRoom{},
		SearchCategories: searchEcho(params, order, limit, len(events) == limit),
	}
	category := result.Categories[SearchCategoryRoomEvents]
	for index, event := range events {
		// Ranking is deliberately shallow: the earlier an event appears in the
		// requested order, the more relevant it is.
		rank := 1 - float64(index)/float64(len(events)+1)
		category.Results = append(category.Results, SearchHit{EventID: event.EventID, Rank: rank})
		room, ok := result.Results[event.RoomID]
		if !ok {
			room = SearchRoom{Rank: rank, ResultsEvents: SearchChunk{
				Chunk: []matrix.Object{},
				Start: s.Token(event.StreamPos),
			}}
		}
		room.ResultsEvents.Chunk = append(room.ResultsEvents.Chunk,
			eventObject(event.Matrix(), params.EventFields))
		room.ResultsEvents.End = s.Token(event.StreamPos + 1)
		result.Results[event.RoomID] = room
	}
	result.Categories[SearchCategoryRoomEvents] = category
	if params.IncludeState {
		for roomID := range result.Results {
			room := result.Results[roomID]
			room.State = s.searchStateEvents(ctx, roomID)
			result.Results[roomID] = room
		}
	}
	return result, nil
}

// searchStateEvents lists the state events of a room, which is what
// include_state asks the server to attach to a search result.
func (s *Service) searchStateEvents(ctx context.Context, roomID string) []matrix.Event {
	rows, err := s.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		return []matrix.Event{}
	}
	events := make([]matrix.Event, 0, len(rows))
	for _, row := range rows {
		event, err := s.db.EventRepo.Get(row.EventID)
		if err != nil {
			continue
		}
		events = append(events, event.Matrix())
	}
	return events
}

// searchEcho repeats the request settings, which is what the specification asks
// the server to return.
func searchEcho(params SearchParams, order string, limit int, limited bool) map[string]SearchEcho {
	limits := SearchLimits{}
	if limited {
		limits.MaxSize = limit
	}
	return map[string]SearchEcho{SearchCategoryRoomEvents: {
		Limits:       limits,
		OrderBy:      order,
		Keywords:     params.Terms,
		EventFields:  params.EventFields,
		IncludeState: params.IncludeState,
	}}
}

// searchableRooms resolves which rooms a search may read. An explicit room
// list is checked room by room, so an unreadable room is a hard error instead
// of a silent gap in the results.
func (s *Service) searchableRooms(ctx context.Context, userID string, filter SearchFilter) ([]string, error) {
	if len(filter.Rooms) > 0 {
		rooms := make([]string, 0, len(filter.Rooms))
		for _, idOrAlias := range filter.Rooms {
			room, err := s.MustRoom(ctx, idOrAlias)
			if err != nil {
				return nil, err
			}
			if err := s.RequireReadable(ctx, room.ID, userID); err != nil {
				return nil, err
			}
			rooms = append(rooms, room.ID)
		}
		return rooms, nil
	}
	rooms, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipJoin, matrix.MembershipInvite)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return rooms, nil
}

// sortSearchEvents orders the matches according to the requested order_by.
func sortSearchEvents(events []*persist.Event, order string) {
	switch order {
	case SearchOrderAlphabetical:
		sort.SliceStable(events, func(i, j int) bool {
			return events[i].EventID < events[j].EventID
		})
	case SearchOrderCreatedTS, SearchOrderOriginTS:
		sort.SliceStable(events, func(i, j int) bool {
			return events[i].OriginServerTS < events[j].OriginServerTS
		})
	default: // recent: newest first, which is what the repository returns.
	}
}

// eventObject renders an event as a JSON object, keeping only the fields the
// client asked for. A projected event never loses its identity, because an
// event without an id or a type is useless to a client.
func eventObject(event matrix.Event, fields []string) matrix.Object {
	full := matrix.MustParseObject([]byte(matrix.Encode(event)))
	if len(fields) == 0 {
		return full
	}
	kept := matrix.Object{}
	for _, field := range fields {
		if value, ok := full[field]; ok {
			kept[field] = value
		}
	}
	for _, required := range []string{"event_id", "type", "sender", "room_id"} {
		if value, ok := full[required]; ok && kept[required] == nil {
			kept[required] = value
		}
	}
	return kept
}

// SearchTerms decodes the "json" field of a search filter, which the
// specification allows as a string, a list of strings, or a full text search
// object.
func SearchTerms(raw any) ([]string, error) {
	switch typed := raw.(type) {
	case nil:
		return nil, nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, nil
		}
		return []string{typed}, nil
	case []any:
		var terms []string
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, matrix.ErrInvalidRequest("search terms must be strings")
			}
			if strings.TrimSpace(text) != "" {
				terms = append(terms, text)
			}
		}
		return terms, nil
	case matrix.Object:
		query, _ := typed["query"].(string)
		if strings.TrimSpace(query) == "" {
			return nil, nil
		}
		return []string{query}, nil
	default:
		return nil, matrix.ErrInvalidRequest("the search body must be a string, a list of strings or an object")
	}
}
