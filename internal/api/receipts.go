package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// handlePostReceipt implements POST /_matrix/client/v3/rooms/{roomId}/receipt/{receiptType}/{eventId}.
//
//	@Summary	Post a read receipt
//	@Tags		read markers
//	@Accept		json
//	@Produce	json
//	@Param		roomId		path	string	true	"Room ID"
//	@Param		receiptType	path	string	true	"Receipt type, m.read or m.read.private"
//	@Param		eventId		path	string	true	"Event the receipt points at"
//	@Success	200			{object}	map[string]any
//	@Router		/_matrix/client/v3/rooms/{roomId}/receipt/{receiptType}/{eventId} [post]
func (a *App) handlePostReceipt(w http.ResponseWriter, r *http.Request) {
	data := bodyObject(r)
	if err := a.svc.PostReceipt(r.Context(), r.PathValue("roomId"), auth.GetUserID(r),
		r.PathValue("receiptType"), r.PathValue("eventId"), data); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleRoomReceipts implements GET /_matrix/client/v1/rooms/{roomId}/receipts.
// The listing is room based rather than per event, which is how a client
// catches up after a reconnect.
//
//	@Summary	List read receipts of a room
//	@Tags		read markers
//	@Produce	json
//	@Param		roomId	path	string	true	"Room ID or alias"
//	@Success	200		{object}	map[string]any
//	@Router		/_matrix/client/v1/rooms/{roomIdOrAlias}/receipts [get]
func (a *App) handleRoomReceipts(w http.ResponseWriter, r *http.Request) {
	receipts, err := a.svc.RoomReceipts(r.Context(), r.PathValue("roomIdOrAlias"), auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, receipts)
}

// handleReadMarkers implements POST /_matrix/client/v3/rooms/{roomId}/read_markers.
//
//	@Summary	Update read markers
//	@Tags		read markers
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/rooms/{roomId}/read_markers [post]
func (a *App) handleReadMarkers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FullyRead   string `json:"fully_read"`
		SpecFull    string `json:"m.fully_read"`
		ReadReceipt string `json:"read_receipt"`
		SpecRead    string `json:"m.read"`
		PrivateRead string `json:"private_read"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	// The specification names the markers "m.fully_read" and "m.read"; the
	// unprefixed spellings are accepted as well.
	fullyRead := body.FullyRead
	if fullyRead == "" {
		fullyRead = body.SpecFull
	}
	readReceipt := body.ReadReceipt
	if readReceipt == "" {
		readReceipt = body.SpecRead
	}
	if readReceipt == "" {
		readReceipt = body.PrivateRead
	}
	if err := a.svc.SetReadMarker(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), fullyRead, readReceipt); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleNotifications implements GET /_matrix/client/v3/notifications.
//
//	@Summary	List push notifications
//	@Tags		read markers
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/notifications [get]
func (a *App) handleNotifications(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r)
	if target := queryValue(r, "user", ""); target != "" && target != userID {
		a.writeError(w, r, matrix.ErrInvalidParam("the user_id parameter is not supported"))
		return
	}
	limit, err := queryInt(r, "limit", 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	entries, nextToken, err := a.svc.Notifications(r.Context(), userID,
		queryValue(r, "from", ""), queryValue(r, "only", ""), limit)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	body := map[string]any{"notifications": entries}
	if nextToken != "" {
		body["next_token"] = nextToken
	}
	writeJSON(w, http.StatusOK, body)
}
