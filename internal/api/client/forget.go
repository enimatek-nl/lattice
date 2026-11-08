package client

import (
	"errors"
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/rooms"
)

// handleRoomForget implements POST /rooms/{roomID}/forget: stop telling me about this room.
//
// The room is not changed by this. The membership event that took the user out of it stays in the
// history exactly as it was signed, every other member's view is untouched, and what stops is this
// user's: the room leaves their sync, and the reads a former member would otherwise be entitled to
// are refused until they come back to it.
func (d *Deps) handleRoomForget(w http.ResponseWriter, r *http.Request) {
	if err := d.Rooms.Forget(r.Context(), pathValue(r, "roomID"), auth.UserID(r)); err != nil {
		if errors.Is(err, rooms.ErrStillInRoom) {
			// The specification gives this case no error code of its own, and the reference
			// behaviour is 400 `M_UNKNOWN`: the request is well formed and the intention behind
			// it cannot be carried out, because leaving is a step with its own endpoint.
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeUnknown,
				"You cannot forget a room you are still in. Leave it first."))
			return
		}
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}
