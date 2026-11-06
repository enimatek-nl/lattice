package federation

import (
	"context"
	"net/http"
	"strconv"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/rooms"
)

// HierarchyReader answers another homeserver's question about a room this server holds: what the room
// is, which of its children the asking server could see, and which it could not. It is implemented by
// the room service, because a room's summary is that room's state — and the Client-Server API's own
// hierarchy endpoint has to give the same answer about the same room.
type HierarchyReader interface {
	HierarchyQuery(ctx context.Context, roomID, origin string, suggestedOnly bool) (rooms.HierarchyAnswer, error)
}

// handleHierarchy implements GET /_matrix/federation/v1/hierarchy/{roomId}.
//
// A room this server does not hold and a room the asking server has no standing to see are answered the
// same way, 404. The distinction is real — one is a room this server has never heard of, the other is a
// room it is keeping — but the answer that gives away least is the one to give a peer, and a peer does
// not need to know which it is in order to finish its walk.
//
// The answer is not signed. The Server-Server API signs the responses that carry events, so that a
// receiver can tell that the events are what their origin said they were; this carries summaries, which
// the asking server weighs as one server's word about its own rooms and checks against its own state
// wherever it can.
func (d *Deps) handleHierarchy(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if roomID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A hierarchy query needs a room ID."))
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	suggestedOnly := false
	if raw := r.URL.Query().Get("suggested_only"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not a boolean.", raw))
			return
		}
		suggestedOnly = parsed
	}
	answer, err := d.Hierarchies.HierarchyQuery(r.Context(), roomID, origin, suggestedOnly)
	if err != nil {
		d.answerServiceError(w, "hierarchy", roomID, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, answer)
}
