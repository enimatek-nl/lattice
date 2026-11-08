package client

import (
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
)

// handleTyping implements PUT /rooms/{roomID}/typing/{userID}: the caller says it is typing, for
// as long as it says.
//
// The body's `typing` is read as a pointer because the specification makes it required and gives
// it a meaning when it is false: an absent key is a request that does not say what it means, while
// `"typing": false` says the user has stopped. Reading them the same way would make a malformed
// request silently stop a person's indicator. `timeout` is likewise a pointer, so an omitted one
// is not confused with a zero-length one; an omitted timeout keeps the indicator for nothing,
// which is what an unstated length is worth.
//
// What the caller says is not verified against the clock and is not kept after it runs out: a
// typing indicator is a hand raised, not a fact about the room.
func (d *Deps) handleTyping(w http.ResponseWriter, r *http.Request) {
	userID := pathValue(r, "userID")
	if userID != auth.UserID(r) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"A user may only set their own typing."))
		return
	}
	var body struct {
		Typing  *bool  `json:"typing"`
		Timeout *int64 `json:"timeout"`
	}
	if r.ContentLength != 0 {
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
			return
		}
	}
	if body.Typing == nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
			"A typing notification says whether the user is typing."))
		return
	}
	timeout := time.Duration(0)
	if body.Timeout != nil {
		timeout = time.Duration(*body.Timeout) * time.Millisecond
	}
	if err := d.Typing.Set(r.Context(), pathValue(r, "roomID"), userID, *body.Typing, timeout); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}
