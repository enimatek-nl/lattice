package api

import (
	"encoding/json"
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
)

// handleSendToDevice implements PUT /_matrix/client/v3/sendToDevice/{eventType}/{txnId}.
//
//	@Summary	Send to device
//	@Tags		devices
//	@Accept		json
//	@Produce	json
//	@Param		eventType	path	string	true	"Event type"
//	@Param		txnId		path	string	true	"Client transaction id"
//	@Success	200			{object}	map[string]any
//	@Router		/_matrix/client/v3/sendToDevice/{eventType}/{txnId} [put]
func (a *App) handleSendToDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages map[string]map[string]json.RawMessage `json:"messages"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.SendToDevice(r.Context(), auth.GetUserID(r), auth.GetDeviceID(r),
		r.PathValue("eventType"), r.PathValue("txnId"), body.Messages); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
