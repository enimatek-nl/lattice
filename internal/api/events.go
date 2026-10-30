package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// stateOnlyTypes lists event types that must be sent through the state
// endpoints instead of /send.
var stateOnlyTypes = map[string]bool{
	matrix.TypeCreate:         true,
	matrix.TypeMember:         true,
	matrix.TypePowerLevels:    true,
	matrix.TypeJoinRules:      true,
	matrix.TypeHistoryVis:     true,
	matrix.TypeGuestAccess:    true,
	matrix.TypeCanonicalAlias: true,
	matrix.TypeName:           true,
	matrix.TypeTopic:          true,
	matrix.TypeAvatar:         true,
}

// handleSendEvent implements PUT /_matrix/client/v3/rooms/{roomId}/send/{eventType}/{txnId}.
//
//	@Summary	Send a message event
//	@Tags		events
//	@Accept		json
//	@Produce	json
//	@Param		roomId		path	string	true	"Room ID"
//	@Param		eventType	path	string	true	"Event type"
//	@Param		txnId		path	string	true	"Client transaction ID"
//	@Success	200			{object}	map[string]string
//	@Router		/_matrix/client/v3/rooms/{roomId}/send/{eventType}/{txnId} [put]
func (a *App) handleSendEvent(w http.ResponseWriter, r *http.Request) {
	eventType := r.PathValue("eventType")
	if stateOnlyTypes[eventType] {
		a.writeError(w, r, matrix.ErrUnrecognized("state events of type %s must be sent to the state endpoint", eventType))
		return
	}
	content, err := readRawBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	event, err := a.svc.SendRoomEvent(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), eventType, content, r.PathValue("txnId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"event_id": event.EventID})
}

// handleSetRoomState implements PUT /_matrix/client/v3/rooms/{roomId}/state/{eventType}/{stateKey}.
//
//	@Summary	Set a room state event
//	@Tags		events
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]string
//	@Router		/_matrix/client/v3/rooms/{roomId}/state/{eventType}/{stateKey} [put]
func (a *App) handleSetRoomState(w http.ResponseWriter, r *http.Request) {
	content, err := readRawBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	txnID := queryValue(r, "txnId", "")
	event, err := a.svc.SetRoomState(r.Context(), r.PathValue("roomId"), auth.GetUserID(r),
		r.PathValue("eventType"), r.PathValue("stateKey"), content, txnID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"event_id": event.EventID})
}

// handleRoomState implements GET /_matrix/client/v3/rooms/{roomId}/state.
//
//	@Summary	Read the current room state
//	@Tags		events
//	@Produce	json
//	@Param		roomId	path	string	true	"Room ID"
//	@Success	200		{object}	map[string][]matrix.Event
//	@Router		/_matrix/client/v3/rooms/{roomId}/state [get]
func (a *App) handleRoomState(w http.ResponseWriter, r *http.Request) {
	var types []string
	if filter := r.URL.Query().Get("type"); filter != "" {
		types = splitList(filter)
	}
	events, err := a.svc.RoomState(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), types)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]matrix.Event{"state": events})
}

// handleRoomStateKey implements GET /_matrix/client/v3/rooms/{roomId}/state/{eventType}
// and GET /_matrix/client/v3/rooms/{roomId}/state/{eventType}/{stateKey}.
func (a *App) handleRoomStateKey(w http.ResponseWriter, r *http.Request) {
	stateKey := r.PathValue("stateKey")
	raw, err := a.svc.StateContent(r.Context(), r.PathValue("roomId"), r.PathValue("eventType"), stateKey, auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(raw))
}

// handleGetEvent implements GET /_matrix/client/v3/rooms/{roomId}/event/{eventId}.
func (a *App) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	event, err := a.svc.RoomEvent(r.Context(), r.PathValue("roomId"), r.PathValue("eventId"), auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, event)
}

// handleMessages implements GET /_matrix/client/v3/rooms/{roomId}/messages.
//
//	@Summary	Paginate room history
//	@Tags		events
//	@Produce	json
//	@Param		roomId	path	string	true	"Room ID"
//	@Param		from	query	string	true	"Stream token to paginate from"
//	@Success	200		{object}	service.HistoryResult
//	@Router		/_matrix/client/v3/rooms/{roomId}/messages [get]
func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 10)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.svc.History(r.Context(), service.HistoryParams{
		RoomID:    r.PathValue("roomId"),
		UserID:    auth.GetUserID(r),
		From:      queryValue(r, "from", ""),
		Dir:       queryValue(r, "dir", "b"),
		Limit:     limit,
		Filter:    queryValue(r, "filter", ""),
		NotBefore: queryValue(r, "not_before", ""),
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleEventContext implements GET /_matrix/client/v3/rooms/{roomId}/context/{eventId}.
func (a *App) handleEventContext(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 10)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.svc.EventContext(r.Context(), r.PathValue("roomId"), r.PathValue("eventId"), auth.GetUserID(r), limit)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleRelations implements GET /_matrix/client/v3/rooms/{roomId}/relations/{eventId}
// and its /{relType}/{eventType} variants. The filters are taken from the path
// when present and from the query otherwise.
func (a *App) handleRelations(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 10)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	relType := r.PathValue("relType")
	if filter := queryValue(r, "rel_type", ""); filter != "" {
		relType = filter
	}
	eventType := r.PathValue("eventType")
	if filter := queryValue(r, "event_type", ""); filter != "" {
		eventType = filter
	}
	events, nextBatch, err := a.svc.Relations(r.Context(), r.PathValue("roomId"), r.PathValue("eventId"),
		relType, eventType, auth.GetUserID(r), limit,
		queryValue(r, "from", ""), queryValue(r, "to", ""))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	body := map[string]any{"chunk": events}
	if nextBatch != "" {
		body["next_batch"] = nextBatch
	}
	writeJSON(w, http.StatusOK, body)
}

// handleThreads implements GET /_matrix/client/v1/rooms/{roomId}/threads.
func (a *App) handleThreads(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 10)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	threads, nextBatch, err := a.svc.Threads(r.Context(), service.ThreadParams{
		RoomID:       r.PathValue("roomId"),
		UserID:       auth.GetUserID(r),
		Include:      queryValue(r, "include", "all"),
		Relationship: queryValue(r, "relationship", "all"),
		From:         queryValue(r, "from", ""),
		Limit:        limit,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	body := map[string]any{"chunk": threads}
	if nextBatch != "" {
		body["next_batch"] = nextBatch
	}
	writeJSON(w, http.StatusOK, body)
}

// handleTimestampToEvent implements GET /_matrix/client/v1/rooms/{roomId}/timestamp_to_event.
func (a *App) handleTimestampToEvent(w http.ResponseWriter, r *http.Request) {
	ts, err := strconv.ParseInt(queryValue(r, "ts", ""), 10, 64)
	if err != nil {
		a.writeError(w, r, matrix.ErrInvalidParam("query parameter \"ts\" must be a unix timestamp in milliseconds"))
		return
	}
	dir := queryValue(r, "dir", "f")
	event, err := a.svc.EventByTimestamp(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), ts, dir)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"origin_server_ts": event.OriginServerTS, "event_id": event.ID})
}

// handleRedact implements PUT /_matrix/client/v3/rooms/{roomId}/redact/{eventId}/{txnId}
// and POST /_matrix/client/v3/rooms/{roomId}/redact/{eventId}.
//
//	@Summary	Redact an event
//	@Tags		events
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]string
//	@Router		/_matrix/client/v3/rooms/{roomId}/redact/{eventId}/{txnId} [put]
func (a *App) handleRedact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
		TxnID  string `json:"txn_id"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	txnID := r.PathValue("txnId")
	if txnID == "" {
		txnID = body.TxnID
	}
	event, err := a.svc.RedactEvent(r.Context(), r.PathValue("roomId"), auth.GetUserID(r),
		r.PathValue("eventId"), body.Reason, txnID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"event_id": event.EventID})
}

// handleReportEvent implements POST /_matrix/client/v3/rooms/{roomId}/report/{eventId}.
func (a *App) handleReportEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
		Score  *int   `json:"score"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.ReportEvent(r.Context(), r.PathValue("roomId"), r.PathValue("eventId"),
		auth.GetUserID(r), body.Reason, body.Score); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleReportRoom implements POST /_matrix/client/v3/rooms/{roomId}/report.
func (a *App) handleReportRoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.ReportRoom(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), body.Reason); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleReportUser implements POST /_matrix/client/v3/users/{userId}/report.
func (a *App) handleReportUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.ReportUser(r.Context(), r.PathValue("userId"), auth.GetUserID(r), body.Reason); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleTyping implements PUT /_matrix/client/v3/rooms/{roomId}/typing/{userId}.
func (a *App) handleTyping(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Typing    bool  `json:"typing"`
		TimeoutMS int64 `json:"timeout"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	roomID := r.PathValue("roomId")
	userID := auth.GetUserID(r)
	if r.PathValue("userId") != userID {
		a.writeError(w, r, matrix.ErrForbidden("only the owner of a typing state may change it"))
		return
	}
	if err := a.svc.RequireJoined(r.Context(), roomID, userID); err != nil {
		a.writeError(w, r, err)
		return
	}
	timeout := time.Duration(body.TimeoutMS) * time.Millisecond
	if err := a.svc.SetTyping(r.Context(), roomID, userID, body.Typing, timeout); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// readRawBody reads a JSON body verbatim so unknown fields survive.
func readRawBody(r *http.Request) (matrix.RawMessage, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		return nil, matrix.ErrNotJSON("could not read the request body")
	}
	if len(body) == 0 {
		return nil, matrix.ErrNotJSON("a content object is required")
	}
	if !json.Valid(body) {
		return nil, matrix.ErrNotJSON("request body was not valid JSON")
	}
	return matrix.RawMessage(body), nil
}
