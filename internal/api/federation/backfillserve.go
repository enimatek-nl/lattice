package federation

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
)

// backfillLimit is the most history one `/backfill` request may take. The asker states a limit, and
// this server's own ceiling sits over it: an endpoint that hands a room's whole past to whichever peer
// asks for it is a way of making a server do unbounded work on somebody else's schedule, and the room
// version rules put a ceiling on how many events one federation response may carry anyway.
const backfillLimit = 100

// handleBackfill implements GET /_matrix/federation/v1/backfill/{roomId}: the history before an event.
//
// `/get_missing_events` asks a peer for what lies between two points the asker can name. This endpoint
// answers the question that one cannot answer, which is a room running out *behind* a server rather
// than opening a hole inside it: the server that joined last week holds nothing below its oldest event,
// so it names that event and asks what came before. It is the request a client makes without knowing
// it, by paging backwards through a room this server has only just joined, and a server that does not
// answer it tells that client — in the only language the protocol has — that the room began on the day
// this server heard about it.
//
// The answer comes oldest first, which is what a server filing a run of history in one pass needs, and
// what this server's own walk of the graph already produces.
//
// Who may ask is the same test `/event` and `/get_missing_events` apply — a server the room has
// excluded gets nothing, and neither does a server with nobody in the room — and history visibility is
// applied event by event afterwards. A server that joined late is not entitled to everything the room
// has ever said; it is entitled to what the room's rules say its members may see, which is a different
// and smaller thing.
func (d *Deps) handleBackfill(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if roomID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"The room ID is missing."))
		return
	}
	query := r.URL.Query()
	since := query.Get("source")
	if since == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"The source event is missing."))
		return
	}
	limit := backfillLimit
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
				"The limit parameter must be a non-negative integer."))
			return
		}
		if value < limit {
			limit = value
		}
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if d.roomExcludes(r, roomID, origin) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"The requesting server is excluded from this room by the room's access control."))
		return
	}
	if !d.serverIsInRoom(r, roomID, origin) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"This server does not know you to be in this room."))
		return
	}
	if d.Gaps == nil {
		d.Log.Error("federation: backfill is not wired to a room service")
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"This server cannot answer that question."))
		return
	}
	// The walk is the same one `/get_missing_events` walks, with the floor left out: the asking server
	// has nothing to name below, which is why it is asking. What bounds the work is the limit, and the
	// ceiling it named, and the room's own beginning.
	events, err := d.Gaps.EventsBetween(r.Context(), roomID, nil, []string{since}, limit, 0)
	if err != nil {
		d.Log.Warn("federation: a peer's request for history could not be answered",
			"room_id", roomID, "origin", origin, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"This server could not read its history."))
		return
	}
	rules, err := d.rulesForRoom(r.Context(), roomID)
	if err != nil {
		var protocol *matrixerr.Error
		if errors.As(err, &protocol) && protocol.Code == matrixerr.CodeIncompatibleRoomVersion {
			matrixerr.Write(w, http.StatusForbidden, protocol)
			return
		}
		d.Log.Warn("federation: the room of a backfill request could not be read",
			"room_id", roomID, "origin", origin, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"This server could not read that room."))
		return
	}
	pdus := make([]json.RawMessage, 0, len(events))
	for _, event := range events {
		raw, err := d.pduForPeer(r.Context(), event, origin, rules)
		if err != nil {
			// Left out rather than handed over untrimmed: the check that failed is the one deciding
			// whether this peer may see the body of this event.
			d.Log.Warn("federation: a backfill event could not be put into the form a peer may be given",
				"room_id", roomID, "event_id", event.EventID, "origin", origin, "error", err)
			continue
		}
		pdus = append(pdus, json.RawMessage(raw))
	}
	d.Log.Info("federation: a peer's request for history was answered",
		"room_id", roomID, "origin", origin, "events", len(pdus), "since", since)
	// The envelope is the specification's, timestamps and all: `origin` and `origin_server_ts` are not
	// decoration but the peer's evidence that this answer came from a server and a moment, which is how
	// an answer that contradicts another server's gets traced afterwards.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"origin":           d.Cfg.ServerName,
		"origin_server_ts": time.Now().UnixMilli(),
		"pdus":             pdus,
	})
}
