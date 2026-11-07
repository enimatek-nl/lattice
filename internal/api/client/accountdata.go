package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
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
// client that wanted a history would keep one itself. An empty object is not a stored empty
// document but a removal (MSC3391) — the same thing this endpoint's DELETE means — because a
// client needs one verb per dialect that can take a value back, and the server has to remember
// that it did so in order to tell the client's other sessions to forget it.
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

// handleAccountDataDelete implements DELETE /_matrix/client/v3/user/{userID}/account_data/{type}.
//
// Clients remove account data in two ways — an empty PUT, and this — and both mean the same
// thing here: the type is no longer stored. Answering a delete of something that was never
// written with an error would make a client retry a change that is already true.
func (d *Deps) handleAccountDataDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := d.requireSelf(w, r)
	if !ok {
		return
	}
	if err := d.AccountData.Delete(r.Context(), userID, pathValue(r, "type")); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// accountDataCaller resolves whose account data a request touches. The stable room-scoped
// paths name only the room, so the caller comes from the access token; the user-scoped paths —
// and MSC3391's room form, which nests the room under the user — name a user, and that user has
// to be the one the token belongs to.
func (d *Deps) accountDataCaller(w http.ResponseWriter, r *http.Request) (string, bool) {
	if pathValue(r, "userID") != "" {
		return d.requireSelf(w, r)
	}
	return auth.UserID(r), true
}

// handleRoomAccountDataGet implements GET /_matrix/client/v3/rooms/{roomID}/account_data/{type}.
//
// Room account data is the user's own record about one room — a tag, an annotation, the flag
// that says this room is a DM — and it is only ever read in that room's context. It is not
// room state: nothing about it is agreed with anybody else, and no other member sees it.
func (d *Deps) handleRoomAccountDataGet(w http.ResponseWriter, r *http.Request) {
	userID, ok := d.accountDataCaller(w, r)
	if !ok {
		return
	}
	content, err := d.AccountData.GetRoom(r.Context(), userID, pathValue(r, "roomID"), pathValue(r, "type"))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteRawJSON(w, http.StatusOK, content)
}

// handleRoomAccountDataPut implements PUT /_matrix/client/v3/rooms/{roomID}/account_data/{type}.
func (d *Deps) handleRoomAccountDataPut(w http.ResponseWriter, r *http.Request) {
	userID, ok := d.accountDataCaller(w, r)
	if !ok {
		return
	}
	raw, err := httpx.DecodeRawBody(r, d.maxBody())
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam, err.Error()))
		return
	}
	if err := d.AccountData.PutRoom(r.Context(), userID, pathValue(r, "roomID"), pathValue(r, "type"), raw); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleRoomAccountDataDelete implements DELETE, for both the stable room path and the
// MSC3391 path that nests the room under the user.
func (d *Deps) handleRoomAccountDataDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := d.accountDataCaller(w, r)
	if !ok {
		return
	}
	if err := d.AccountData.DeleteRoom(r.Context(), userID, pathValue(r, "roomID"), pathValue(r, "type")); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}
