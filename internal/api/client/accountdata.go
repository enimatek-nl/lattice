package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
)

// handleAccountDataGet implements GET /_matrix/client/v3/user/{userID}/account_data/{type}.
//
// The document is written back as it was stored. Account data is the client's own record —
// which rooms it treats as private conversations, what it wants its interface to show — and a
// server that re-encoded it through its own structs would drop the keys it has no opinion
// about, so the client's next read would not match its last write.
func (d *Deps) handleAccountDataGet(w http.ResponseWriter, r *http.Request) {
	userID, ok := d.requireSelf(w, r)
	if !ok {
		return
	}
	content, err := d.AccountData.Get(r.Context(), userID, pathValue(r, "type"))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteRawJSON(w, http.StatusOK, content)
}

// handleAccountDataPut implements PUT /_matrix/client/v3/user/{userID}/account_data/{type}.
//
// Writing a type replaces it rather than adding to it: account data is a current value, and a
// client that wanted a history would keep one itself. An empty object is a legitimate write and
// erases whatever was there, which is how a client forgets a preference without deleting the
// key it is filed under.
func (d *Deps) handleAccountDataPut(w http.ResponseWriter, r *http.Request) {
	userID, ok := d.requireSelf(w, r)
	if !ok {
		return
	}
	raw, err := httpx.DecodeRawBody(r, d.maxBody())
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam, err.Error()))
		return
	}
	if err := d.AccountData.Put(r.Context(), userID, pathValue(r, "type"), raw); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}
