package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
)

// handleUserDirectorySearch implements POST /_matrix/client/v3/user_directory/search: finding people
// rather than finding rooms.
//
// `search_term` is read as a pointer because the specification makes it required, and an absent one
// is a request that never said what to look for rather than a request to search for the empty string:
// one is a malformed request and answers 400 M_MISSING_PARAM, the other is a term with no word in it
// and answers with nobody. `limit` is a pointer so that an omitted one is not confused with a zero
// one; both mean "the default of ten", because that is what the specification's default is worth when
// a client sends a number it did not mean.
//
// What the search may return is not this handler's decision; it is in the userdir service, where the
// rule about whose names may be matched against whom is stated once.
func (d *Deps) handleUserDirectorySearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Limit      *int    `json:"limit"`
		SearchTerm *string `json:"search_term"`
	}
	if r.ContentLength != 0 {
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
			return
		}
	}
	if body.SearchTerm == nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParam,
			"A user search says what to search for."))
		return
	}
	limit := 0
	if body.Limit != nil {
		limit = *body.Limit
		if limit < 0 {
			d.serviceError(w, matrixerr.New(matrixerr.CodeInvalidParam,
				"The limit must not be negative."))
			return
		}
	}
	page, err := d.UserDirectory.Search(r.Context(), auth.UserID(r), *body.SearchTerm, limit)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
