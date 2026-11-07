package client

import (
	"encoding/json"
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// handleReceipt implements POST /rooms/{roomID}/receipt/{receiptType}/{eventID}: the caller says
// how far they have read. The room is named by the path, so the receipt does not carry a room
// field — and over Federation it does, which is why the same object has to be written two ways
// depending on which door it came in by.
func (d *Deps) handleReceipt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ThreadID string `json:"thread_id"`
	}
	if r.ContentLength != 0 {
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
			return
		}
	}
	if err := d.Rooms.PutReceipt(r.Context(), pathValue(r, "roomID"),
		pathValue(r, "receiptType"), pathValue(r, "eventID"), auth.UserID(r), body.ThreadID); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// readMarkersRequest is the POST /rooms/{roomID}/read_markers body: two read positions moved in
// one call, one of them this server keeps as the caller's own account data rather than as a
// receipt other servers hear about.
type readMarkersRequest struct {
	FullyRead    string `json:"m.fully_read"`
	Read         string `json:"m.read"`
	MarkedUnread *bool  `json:"m.marked_unread"`
}

// handleReadMarkers implements POST /rooms/{roomID}/read_markers.
//
// `m.read` is the receipt, and it is sent through the same path the receipt endpoint uses so
// the two doors cannot disagree about what a read position means. `m.fully_read` is the reader's
// own bookmark: nothing else in the room needs to know it, so it is stored as room-scoped account
// data and reaches the caller's `/sync` through `account_data`.
func (d *Deps) handleReadMarkers(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r)
	roomID := pathValue(r, "roomID")
	var req readMarkersRequest
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &req) {
		return
	}
	if req.FullyRead == "" && req.Read == "" && req.MarkedUnread == nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
			"A read marker names at least one of \"m.fully_read\" and \"m.read\"."))
		return
	}
	if req.Read != "" {
		if err := d.Rooms.PutReceipt(r.Context(), roomID, "m.read", req.Read, userID, ""); err != nil {
			d.serviceError(w, err)
			return
		}
	}
	if req.FullyRead != "" {
		if err := matrixid.EventIDValid(req.FullyRead); err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not an event ID.", req.FullyRead))
			return
		}
		// The event has to be one of this room: a bookmark pointing at an event the room
		// never held would be a marker the client cannot follow.
		if _, err := d.Rooms.Event(r.Context(), roomID, req.FullyRead, userID); err != nil {
			d.serviceError(w, err)
			return
		}
		content, err := json.Marshal(map[string]any{"event_id": req.FullyRead})
		if err != nil {
			d.writeInternal(w, "read marker encode", err)
			return
		}
		if err := d.AccountData.PutRoom(r.Context(), userID, roomID, "m.fully_read", content); err != nil {
			d.serviceError(w, err)
			return
		}
	}
	if req.MarkedUnread != nil {
		// MSC4283's flag is recorded as account data rather than dropped, so a client that
		// sets it can read back what it set. Computing the unread *count* from these markers
		// is T-3005a and is still open.
		content, err := json.Marshal(map[string]any{"marker": "unread",
			"m.marked_unread": *req.MarkedUnread})
		if err != nil {
			d.writeInternal(w, "unread marker encode", err)
			return
		}
		if err := d.AccountData.PutRoom(r.Context(), userID, roomID, "m.marked_unread", content); err != nil {
			d.serviceError(w, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}
