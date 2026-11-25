package service

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// SearchOrderRank is the ordering the specification defaults to. This server has
// no text index, so it ranks by how often the terms appear in an event and lets
// recency break a tie.
const SearchOrderRank = "rank"

// searchOrders lists the order_by values of the specification search.
var searchOrders = map[string]bool{
	SearchOrderRank:   true,
	SearchOrderRecent: true,
}

// SearchSpecParams is the request of POST /_matrix/client/v3/search as the
// specification defines it: terms and filters arrive inside the "room_events"
// category of search_categories.
type SearchSpecParams struct {
	UserID string
	// Terms are the words to look for, AND-ed together.
	Terms []string
	// EscapeRegex asked for a literal match. This server only ever matches
	// literally, so the flag never changes the answer.
	EscapeRegex bool
	// Filter narrows the search to rooms, senders and event types.
	Filter SearchFilter
	// Before and After ask for the events around each match. They arrive in the
	// category as event_context.
	Before int
	After  int
	// Context reports whether a page was asked for the events around its matches.
	Context bool
	// Keys restricts matching to fields of the event, e.g. "content.body".
	Keys []string
	// OrderBy is "rank" or "recent"; empty means the specification's default.
	OrderBy string
	// Limit and Offset page through the matches. Offset carries the opaque
	// next_batch token of an earlier answer.
	Limit  int
	Offset int
}

// SearchSpecHit is one result of the "room_events" category: the event as a
// client reads it, plus the rank it scored. A result that carries only an event
// id forces every client to fetch what it was just told about, and the
// specification hands the event over.
type SearchSpecHit struct {
	Rank   float64       `json:"rank"`
	Result matrix.Object `json:"result"`
	// Context is the window around the match, when the request asked for one.
	Context *SearchSpecContext `json:"context,omitempty"`
}

// SearchSpecContext is the events around a match. Both windows are ordered away
// from the match, so the neighbour comes first in each.
type SearchSpecContext struct {
	EventsBefore []matrix.Object `json:"events_before"`
	EventsAfter  []matrix.Object `json:"events_after"`
}

// SearchSpecCategory is the answer of a single search category.
type SearchSpecCategory struct {
	// Count is the number of matches, not the number of results on this page.
	Count int `json:"count"`
	// Highlights lists the words a client should mark up in the results.
	Highlights []string        `json:"highlights,omitempty"`
	Results    []SearchSpecHit `json:"results"`
	// NextBatch continues where this page ended; absent at the last page.
	NextBatch string `json:"next_batch,omitempty"`
}

// SearchSpecResult is the payload of a specification-shaped search.
type SearchSpecResult struct {
	Categories map[string]SearchSpecCategory `json:"search_categories"`
}

// SearchSpecParamsFrom decodes a specification search body. Only "room_events"
// is served; other categories are accepted and answered empty, which is what
// the specification allows. Unknown fields are ignored throughout, as the
// specification asks.
// The batch token is read from the body and, when the body is silent, from the
// query string: Synapse answers it from either and clients split on it.
func SearchSpecParamsFrom(userID string, body matrix.Object, query url.Values) (SearchSpecParams, error) {
	params := SearchSpecParams{UserID: userID, OrderBy: SearchOrderRank, Limit: searchDefaultLimit}
	raw, ok := body["search_categories"]
	if !ok {
		return params, matrix.ErrInvalidRequest("search_categories is required")
	}
	categories, ok := raw.(matrix.Object)
	if !ok {
		return params, matrix.ErrInvalidRequest("search_categories must be an object")
	}
	rawCategory, ok := categories[SearchCategoryRoomEvents]
	if !ok {
		// A search without the room_events category has nothing to look in; the
		// caller answers an empty room_events category.
		return params, nil
	}
	category, ok := rawCategory.(matrix.Object)
	if !ok {
		return params, matrix.ErrInvalidRequest("the room_events category must be an object")
	}
	terms, err := SearchTerms(category["search_term"])
	if err != nil {
		return params, err
	}
	// A term is a sentence, not a phrase: the words in it have to appear, which is
	// how the server-side search a client aims at reads it.
	params.Terms = searchWords(terms)
	if escape, ok := category["escape_regex"].(bool); ok {
		params.EscapeRegex = escape
	}
	keys, err := stringList(category["keys"])
	if err != nil {
		return params, matrix.ErrInvalidRequest("keys must be a list of strings")
	}
	params.Keys = keys
	if order, ok := category["order_by"].(string); ok && order != "" {
		if !searchOrders[order] {
			return params, matrix.ErrInvalidParam("unknown order_by %q", order)
		}
		params.OrderBy = order
	}
	if rawFilter, ok := category["filter"].(matrix.Object); ok {
		if err := params.Filter.readFrom(rawFilter); err != nil {
			return params, err
		}
	}
	// The specification keeps the page size inside the filter; a flat limit is
	// read as well, because that is where an older reader looks.
	limit := 0.0
	if raw, ok := category["limit"].(float64); ok {
		limit = raw
	}
	if rawFilter, ok := category["filter"].(matrix.Object); ok {
		if raw, ok := rawFilter["limit"].(float64); ok && raw > 0 {
			limit = raw
		}
	}
	if limit > 0 {
		params.Limit = clampSearchLimit(int(limit))
	}
	if raw, ok := category["event_context"].(matrix.Object); ok {
		before, _ := raw["before_limit"].(float64)
		after, _ := raw["after_limit"].(float64)
		params.Before = clampSearchContext(before)
		params.After = clampSearchContext(after)
		params.Context = true
	}
	batch, _ := category["next_batch"].(string)
	if batch == "" {
		batch = query.Get("next_batch")
	}
	if batch != "" {
		offset, err := strconv.Atoi(batch)
		if err != nil || offset < 0 {
			return params, matrix.ErrInvalidParam("next_batch is not a search batch token")
		}
		params.Offset = offset
	}
	return params, nil
}

// SearchSpec answers a specification search: matches in events the requester can
// read, ranked, paged, with the words to highlight.
func (s *Service) SearchSpec(ctx context.Context, params SearchSpecParams) (*SearchSpecResult, error) {
	if len(params.Terms) == 0 {
		return nil, matrix.ErrInvalidParam("at least one search term is required")
	}
	if !searchOrders[params.OrderBy] {
		return nil, matrix.ErrInvalidParam("unknown order_by %q", params.OrderBy)
	}
	limit := clampSearchLimit(params.Limit)
	answer := SearchSpecResult{Categories: map[string]SearchSpecCategory{}}
	category := SearchSpecCategory{Results: []SearchSpecHit{}, Highlights: searchHighlights(params.Terms)}
	rooms, err := s.searchableRooms(ctx, params.UserID, params.Filter)
	if err != nil {
		return nil, err
	}
	if len(rooms) > 0 {
		// The scan window is read whole and narrowed afterwards: a room that
		// opened its history at the join keeps events from before that moment from
		// this reader, and a count or a page cut before that gate would promise
		// matches the reader never receives.
		events, err := s.db.EventRepo.Search(persist.SearchQuery{
			RoomIDs:    rooms,
			NotRooms:   params.Filter.NotRooms,
			Types:      params.Filter.Types,
			NotTypes:   params.Filter.NotTypes,
			Senders:    params.Filter.Senders,
			NotSenders: params.Filter.NotSenders,
			Terms:      params.Terms,
			Keys:       params.Keys,
			Limit:      searchScanLimit,
		})
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		kept, err := s.readableSearchEvents(ctx, params.UserID, events)
		if err != nil {
			return nil, err
		}
		sortSearchResults(kept, params)
		page := searchWindow(kept, params.Offset, limit)
		category.Count = len(kept)
		// The rank descends over the page, so a client can show the ordering it
		// was given even though the scores themselves are coarse.
		for index, event := range page {
			hit := SearchSpecHit{
				Rank:   1 - float64(index)/float64(len(page)+1),
				Result: eventObject(event.Matrix(), nil),
			}
			if params.Context {
				hit.Context, err = s.searchContext(ctx, params, event)
				if err != nil {
					return nil, err
				}
			}
			category.Results = append(category.Results, hit)
		}
		// A full page promises another one; whether it holds anything the next
		// request shows. This is what a client that paginates to the end expects.
		if len(page) == limit && limit > 0 {
			category.NextBatch = strconv.Itoa(params.Offset + len(page))
		}
	}
	answer.Categories[SearchCategoryRoomEvents] = category
	return &answer, nil
}

// clampSearchContext keeps a requested context window inside what one answer
// carries: a hundred neighbours per match on a page of ten is a refusal waiting
// to happen, so the request is answered with the widest window served.
func clampSearchContext(requested float64) int {
	if requested <= 0 {
		return 0
	}
	if requested > searchContextLimit {
		return searchContextLimit
	}
	return int(requested)
}

// searchContext reads the events around a match, in the ordering the request
// expects: each window starts next to the match and grows away from it. What a
// room keeps from this reader stays kept.
func (s *Service) searchContext(ctx context.Context, params SearchSpecParams, event *persist.Event) (*SearchSpecContext, error) {
	gates := s.historyGates(ctx, params.UserID)
	window := func(limit int, dir string) ([]matrix.Object, error) {
		if limit == 0 {
			return []matrix.Object{}, nil
		}
		rows, err := s.db.EventRepo.History(event.RoomID, event.StreamPos, dir, limit, 0)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		out := make([]matrix.Object, 0, len(rows))
		for _, row := range rows {
			allowed, err := gates.allows(row)
			if err != nil {
				return nil, err
			}
			if allowed {
				out = append(out, eventObject(row.Matrix(), nil))
			}
		}
		return out, nil
	}
	before, err := window(params.Before, "b")
	if err != nil {
		return nil, err
	}
	after, err := window(params.After, "f")
	if err != nil {
		return nil, err
	}
	return &SearchSpecContext{EventsBefore: before, EventsAfter: after}, nil
}

// searchContextLimit is the widest context window one match carries.
const searchContextLimit = 100

// searchScanLimit is the number of newest matches a search reads before it drops
// what the reader may not see and cuts a page. It is the same ceiling a client can
// ask for in one page, so a search never reads more than it could answer.
const searchScanLimit = searchMaxLimit

// readableSearchEvents drops the matches a room keeps from this reader. A room
// that opens its history at the join or the invite hides everything before that
// moment, and /search is a read of the same history as /messages.
func (s *Service) readableSearchEvents(ctx context.Context, userID string, events []*persist.Event) ([]*persist.Event, error) {
	gates := s.historyGates(ctx, userID)
	kept := make([]*persist.Event, 0, len(events))
	for _, event := range events {
		allowed, err := gates.allows(event)
		if err != nil {
			return nil, err
		}
		if allowed {
			kept = append(kept, event)
		}
	}
	return kept, nil
}

// searchWindow cuts one page out of the matches a reader may see.
func searchWindow(events []*persist.Event, offset, limit int) []*persist.Event {
	if offset >= len(events) {
		return nil
	}
	events = events[offset:]
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events
}

// sortSearchResults orders the matches by the requested ordering. Ranking has
// no text index to lean on, so it counts how often the terms appear in the
// event and lets recency break a tie.
func sortSearchResults(events []*persist.Event, params SearchSpecParams) {
	if params.OrderBy == SearchOrderRecent {
		sortSearchEvents(events, SearchOrderRecent)
		return
	}
	scores := make(map[string]int, len(events))
	for _, event := range events {
		scores[event.EventID] = searchScore(event, params.Terms)
	}
	sort.SliceStable(events, func(i, j int) bool {
		left, right := scores[events[i].EventID], scores[events[j].EventID]
		if left != right {
			return left > right
		}
		return events[i].StreamPos > events[j].StreamPos
	})
}

// searchScore counts case-insensitive occurrences of every term in an event.
func searchScore(event *persist.Event, terms []string) int {
	content := strings.ToLower(event.Content)
	sender := strings.ToLower(event.Sender)
	eventType := strings.ToLower(event.Type)
	score := 0
	for _, term := range terms {
		needle := strings.ToLower(term)
		if needle == "" {
			continue
		}
		score += strings.Count(content, needle) * 2
		score += strings.Count(sender, needle) + strings.Count(eventType, needle)
	}
	return score
}

// searchWords splits every search term into words and keeps the distinct ones.
// "Message 4" asks for the word Message and the word 4, so an event that says
// "Message number 4" matches; treating the pair as one phrase would hide it.
func searchWords(terms []string) []string {
	return searchHighlights(strings.FieldsFunc(strings.Join(terms, " "), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}))
}

// searchHighlights are the distinct terms a client should mark up, in the order
// the client typed them.
func searchHighlights(terms []string) []string {
	seen := make(map[string]bool, len(terms))
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
	}
	return out
}

// readFrom fills a search filter from a JSON object of the specification.
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
