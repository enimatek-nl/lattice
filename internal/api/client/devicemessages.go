package client

import (
	"encoding/json"
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// sendToDeviceBody is the request body of PUT /_matrix/client/v3/sendToDevice/{eventType}/{txnId}.
//
// `messages` is a map of recipient user to map of device to payload, and the payload is kept as raw bytes
// on purpose. A to-device payload is normally an encrypted blob whose bytes the recipient decrypts; a
// server that decoded it into a Go map and re-encoded it would be tampering with something it cannot see
// into, and would break any payload whose key order or number formatting did not survive the round trip.
type sendToDeviceBody struct {
	Messages map[string]map[string]json.RawMessage `json:"messages"`
}

// handleSendToDevice implements PUT /_matrix/client/v3/sendToDevice/{eventType}/{txnId}.
//
// Two things make this endpoint worth more than a glance. The first is that it is the only way a device can
// reach another device: an encrypted room's Megolm keys are shared this way, so a homeserver that answers
// it badly does not have a broken endpoint, it has a room full of clients that cannot read each other. The
// second is the transaction ID: clients retry this request, and a retried key request delivered twice is a
// device being asked to decrypt the same session twice. Idempotency therefore belongs to the storage layer,
// where a second send with the same ID files nothing and still answers 200.
//
// An empty `messages` is accepted and does nothing. The specification does not forbid it, and a client that
// looked up a recipient's devices and found none has already expressed the only decision available to it.
func (d *Deps) handleSendToDevice(w http.ResponseWriter, r *http.Request) {
	eventType := r.PathValue("eventType")
	txnID := r.PathValue("txnId")
	if err := matrixid.EventTypeValid(eventType); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid event type.", eventType))
		return
	}
	if txnID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A to-device send names a transaction ID, so that a retry is the same send rather than a second one."))
		return
	}
	var body sendToDeviceBody
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if d.DeviceMessages == nil {
		matrixerr.Write(w, http.StatusNotImplemented, matrixerr.New(matrixerr.CodeUnknown,
			"This server is not delivering messages between devices."))
		return
	}
	if err := d.DeviceMessages.Send(r.Context(), auth.UserID(r), auth.DeviceID(r), eventType, txnID, body.Messages); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}
