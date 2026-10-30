package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// handleSearch implements POST /_matrix/client/v3/search. Two request shapes are
// served: the historical flat body and the search_categories body that carries
// interactive filters and grouping.
//
//	@Summary	Search for events
//	@Tags		events
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/search [post]
func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	var body matrix.Object
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if _, ok := body["search_categories"]; ok {
		params, err := service.SearchJSONParamsFrom(auth.GetUserID(r), body)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		result, err := a.svc.SearchJSON(r.Context(), params)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	params, err := legacySearchParams(auth.GetUserID(r), body)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.svc.Search(r.Context(), params)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// legacySearchParams reads the flat search body of the original specification.
func legacySearchParams(userID string, body matrix.Object) (service.SearchParams, error) {
	params := service.SearchParams{UserID: userID}
	terms, err := service.SearchTerms(body["search_term"])
	if err != nil {
		return params, err
	}
	params.Terms = terms
	if filter, ok := body["filter"].(matrix.Object); ok {
		if err := service.ReadSearchFilter(&params.Filter, filter); err != nil {
			return params, err
		}
	}
	if raw, ok := body["event_fields"].(matrix.Array); ok {
		fields, err := service.StringList(raw)
		if err != nil {
			return params, err
		}
		params.EventFields = fields
	}
	params.OrderBy, _ = body["order_by"].(string)
	if limit, ok := body["limit"].(float64); ok {
		params.Limit = int(limit)
	}
	params.IncludeState, _ = body["include_state"].(bool)
	return params, nil
}
