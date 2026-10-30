package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// Grouping keys understood by the JSON search form: events are grouped per
// conversation (a thread is one conversation) or per room.
const (
	SearchGroupSession = "session_id"
	SearchGroupRoom    = "room_id"
)

// searchGroupings lists the accepted grouping keys.
var searchGroupings = map[string]bool{
	SearchGroupSession: true,
	SearchGroupRoom:    true,
}

// SearchJSONParams is the MSC3575 style search request: terms and filters
// arrive inside a category, results come back grouped per conversation.
type SearchJSONParams struct {
	UserID      string
	Categories  []string
	Terms       []string
	Filter      SearchFilter
	Menu        SearchMenu
	OrderBy     string
	EventFields []string
	Groupings   []string
	Limit       int
}

// SearchMenu carries the interactive menu selections of a search form: the
// event types, rooms and senders the user ticked. A non-empty selection narrows
// the search, an empty one widens it again.
type SearchMenu struct {
	EventTypes []string
	Rooms      []string
	Senders    []string
}

// SearchJSONGroup is the slice of a search result that belongs to one
// conversation.
type SearchJSONGroup struct {
	GroupID string          `json:"group_id"`
	Events  []matrix.Object `json:"events"`
}

// SearchJSONHit is one matching event together with its grouping.
type SearchJSONHit struct {
	EventID         string            `json:"event_id"`
	Rank            float64           `json:"rank"`
	ResultsPerGroup []SearchJSONGroup `json:"results_per_group"`
}

// SearchJSONCategoryResult holds the answer of a single search category.
type SearchJSONCategoryResult struct {
	Limits    SearchLimits    `json:"limits"`
	OrderBy   string          `json:"order_by,omitempty"`
	Groupings []matrix.Object `json:"groupings,omitempty"`
	Hits      []SearchJSONHit `json:"hits"`
}

// SearchJSONResult is the payload of a JSON style search.
type SearchJSONResult struct {
	Categories map[string]SearchJSONCategoryResult `json:"search_categories"`
}

// SearchJSONParamsFrom decodes the body of a search request that uses
// search_categories. Only the room_events category is served; other categories
// are echoed back empty, which is what the specification allows.
func SearchJSONParamsFrom(userID string, body matrix.Object) (SearchJSONParams, error) {
	params := SearchJSONParams{
		UserID:    userID,
		OrderBy:   SearchOrderRecent,
		Groupings: []string{SearchGroupSession},
		Limit:     searchDefaultLimit,
	}
	raw, ok := body["search_categories"]
	if !ok {
		return params, matrix.ErrInvalidRequest("search_categories is required")
	}
	categories, ok := raw.(matrix.Object)
	if !ok {
		return params, matrix.ErrInvalidRequest("search_categories must be an object")
	}
	for name, rawCategory := range categories {
		if name != SearchCategoryRoomEvents {
			return params, matrix.ErrInvalidParam("unknown search category %q", name)
		}
		params.Categories = append(params.Categories, name)
		category, ok := rawCategory.(matrix.Object)
		if !ok {
			return params, matrix.ErrInvalidRequest("the room_events category must be an object")
		}
		if err := params.readSearchKey(category["search_key"]); err != nil {
			return params, err
		}
		if err := params.readCategorySettings(category); err != nil {
			return params, err
		}
	}
	return params, nil
}

// readSearchKey pulls the terms and the inline filters out of "search_key".
func (p *SearchJSONParams) readSearchKey(raw any) error {
	if raw == nil {
		return nil
	}
	key, ok := raw.(matrix.Object)
	if !ok {
		return matrix.ErrInvalidRequest("search_key must be an object")
	}
	terms, err := SearchTerms(key["term"])
	if err != nil {
		return err
	}
	p.Terms = append(p.Terms, terms...)
	if jsonKey, ok := key["json"].(matrix.Object); ok {
		// The full text search object of the draft: a query plus a filter.
		nested, err := SearchTerms(jsonKey["query"])
		if err != nil {
			return err
		}
		p.Terms = append(p.Terms, nested...)
		if filter, ok := jsonKey["filter"].(matrix.Object); ok {
			if err := p.Filter.readFrom(filter); err != nil {
				return err
			}
		}
	}
	return nil
}

// readCategorySettings reads order_by, event_fields, groupings, menu and limits.
func (p *SearchJSONParams) readCategorySettings(category matrix.Object) error {
	if order, ok := category["order_by"].(string); ok && order != "" {
		p.OrderBy = order
	}
	fields, err := stringList(category["event_fields"])
	if err != nil {
		return err
	}
	p.EventFields = fields
	if rawGroupings, ok := category["groupings"]; ok {
		entries, ok := rawGroupings.(matrix.Array)
		if !ok {
			return matrix.ErrInvalidRequest("groupings must be a list")
		}
		// A grouping entry is an object with a "key" field; the plain string
		// form is accepted too.
		p.Groupings = nil
		for _, entry := range entries {
			switch typed := entry.(type) {
			case string:
				p.Groupings = append(p.Groupings, typed)
			case matrix.Object:
				key, _ := typed["key"].(string)
				p.Groupings = append(p.Groupings, key)
			default:
				return matrix.ErrInvalidRequest("a grouping must be an object or a string")
			}
		}
	}
	if len(p.Groupings) == 0 {
		p.Groupings = []string{SearchGroupSession}
	}
	for _, key := range p.Groupings {
		if !searchGroupings[key] {
			return matrix.ErrInvalidParam("unknown grouping key %q", key)
		}
	}
	if menu, ok := category["menu"].(matrix.Object); ok {
		types, err := stringList(menu["event_types"])
		if err != nil {
			return err
		}
		p.Menu.EventTypes = types
		rooms, err := stringList(menu["rooms"])
		if err != nil {
			return err
		}
		p.Menu.Rooms = rooms
		senders, err := stringList(menu["senders"])
		if err != nil {
			return err
		}
		p.Menu.Senders = senders
	}
	if limits, ok := category["limits"].(matrix.Object); ok {
		size, ok := limits["limit"].(float64)
		if ok && size > 0 {
			p.Limit = int(size)
		}
	}
	return nil
}

// readFrom fills a search filter from a JSON object.
func (f *SearchFilter) readFrom(body matrix.Object) error {
	var err error
	if f.Rooms, err = stringList(body["rooms"]); err != nil {
		return err
	}
	if f.NotRooms, err = stringList(body["not_rooms"]); err != nil {
		return err
	}
	if f.Types, err = stringList(body["types"]); err != nil {
		return err
	}
	if f.NotTypes, err = stringList(body["not_types"]); err != nil {
		return err
	}
	if f.Senders, err = stringList(body["senders"]); err != nil {
		return err
	}
	if f.NotSenders, err = stringList(body["not_senders"]); err != nil {
		return err
	}
	return nil
}

// ReadSearchFilter fills a search filter from a JSON object. It is exported so
// the API layer can read the filter of a flat search body.
func ReadSearchFilter(filter *SearchFilter, body matrix.Object) error {
	return filter.readFrom(body)
}

// StringList decodes an optional array of strings.
func StringList(raw any) ([]string, error) {
	return stringList(raw)
}

// stringList decodes an optional array of strings.
func stringList(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.(matrix.Array)
	if !ok {
		return nil, matrix.ErrInvalidRequest("expected a list of strings")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, matrix.ErrInvalidRequest("expected a list of strings")
		}
		out = append(out, text)
	}
	return out, nil
}

// SearchJSON answers a MSC3575 style search: the matches are grouped per
// conversation so a client can render whole threads.
func (s *Service) SearchJSON(ctx context.Context, params SearchJSONParams) (*SearchJSONResult, error) {
	if len(params.Terms) == 0 {
		return nil, matrix.ErrInvalidParam("at least one search term is required")
	}
	if !searchOrderings[params.OrderBy] {
		return nil, matrix.ErrInvalidParam("unknown order_by %q", params.OrderBy)
	}
	limit := clampSearchLimit(params.Limit)
	filter := params.Filter
	// Menu selections narrow the filter the search form started from.
	filter.Types = mergeStringLists(filter.Types, params.Menu.EventTypes)
	filter.Rooms = mergeStringLists(filter.Rooms, params.Menu.Rooms)
	filter.Senders = mergeStringLists(filter.Senders, params.Menu.Senders)
	rooms, err := s.searchableRooms(ctx, params.UserID, filter)
	if err != nil {
		return nil, err
	}
	result := &SearchJSONResult{Categories: map[string]SearchJSONCategoryResult{}}
	category := SearchJSONCategoryResult{
		OrderBy:   params.OrderBy,
		Hits:      []SearchJSONHit{},
		Groupings: groupingWire(params.Groupings),
	}
	if len(rooms) > 0 {
		events, err := s.db.EventRepo.Search(persist.SearchQuery{
			RoomIDs:    rooms,
			NotRooms:   filter.NotRooms,
			Types:      filter.Types,
			NotTypes:   filter.NotTypes,
			Senders:    filter.Senders,
			NotSenders: filter.NotSenders,
			Terms:      params.Terms,
			Limit:      limit,
		})
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		sortSearchEvents(events, params.OrderBy)
		category.Limits.MaxSize = limit
		for index, event := range events {
			hit := SearchJSONHit{
				EventID:         event.EventID,
				Rank:            1 - float64(index)/float64(len(events)+1),
				ResultsPerGroup: s.searchGroups(ctx, event, params),
			}
			category.Hits = append(category.Hits, hit)
		}
	}
	for _, name := range params.Categories {
		result.Categories[name] = category
	}
	if len(result.Categories) == 0 {
		result.Categories[SearchCategoryRoomEvents] = category
	}
	return result, nil
}

// groupingWire renders the grouping keys for the response echo.
func groupingWire(keys []string) []matrix.Object {
	out := make([]matrix.Object, 0, len(keys))
	for _, key := range keys {
		out = append(out, matrix.Object{"key": key})
	}
	return out
}

// searchGroups builds the results_per_group entries of one hit.
func (s *Service) searchGroups(ctx context.Context, event *persist.Event, params SearchJSONParams) []SearchJSONGroup {
	wire := eventObject(event.Matrix(), params.EventFields)
	groups := make([]SearchJSONGroup, 0, len(params.Groupings))
	for _, key := range params.Groupings {
		groupID := event.RoomID
		if key == SearchGroupSession {
			// A thread is one conversation: its root identifies the session.
			if event.RelType == matrix.RelThread && event.RelatesTo != "" {
				groupID = event.RelatesTo
			}
		}
		groups = append(groups, SearchJSONGroup{GroupID: groupID, Events: []matrix.Object{wire}})
	}
	return groups
}

// mergeStringLists combines two filter lists. An empty selection is a no-op, so
// an unticked menu leaves the filter alone.
func mergeStringLists(base, selection []string) []string {
	if len(selection) == 0 {
		return base
	}
	if len(base) == 0 {
		return selection
	}
	merged := make([]string, 0, len(base)+len(selection))
	merged = append(merged, base...)
	return append(merged, selection...)
}

// clampSearchLimit keeps the page size inside the bounds this server serves.
func clampSearchLimit(limit int) int {
	if limit <= 0 {
		return searchDefaultLimit
	}
	if limit > searchMaxLimit {
		return searchMaxLimit
	}
	return limit
}
