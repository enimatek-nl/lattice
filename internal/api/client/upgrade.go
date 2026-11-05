package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/rooms"
)

// handleRoomUpgrade implements POST /_matrix/client/v3/rooms/{roomID}/upgrade.
//
// The response names the new room rather than the old one's new state, because that is the
// only thing the client can act on: the old room still exists, still holds its history, and
// now also holds a tombstone telling everybody where its conversation went.
func (d *Deps) handleRoomUpgrade(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NewVersion string `json:"new_version"`
	}
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if body.NewVersion == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"An upgrade must say which room version to move to."))
		return
	}
	replacement, err := d.Rooms.Upgrade(r.Context(), rooms.UpgradeRequest{
		RoomID:     r.PathValue("roomID"),
		UserID:     auth.UserID(r),
		NewVersion: body.NewVersion,
	})
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"replacement_room": replacement})
}
