package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/e2ekeys"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/sync"
)

// Device and one-time keys: `/keys/upload`, `/keys/query` and `/keys/claim`.
//
// These endpoints are the server's part in a design whose point is that the server is not
// trusted. That is why the checks here are strict about signatures and loose about nothing
// else: this server is the postbox, and a postbox that quietly accepted unaddressed mail
// would be the weakest link in every conversation that passes through it.
//
// What is *not* here, and is said rather than hidden: keys are not carried between servers
// (no federation yet), and fallback keys are refused rather than stored — a fallback key
// outlives the session that used it, so storing one under a name that promises single use
// would be a promise this server cannot keep.

// handleKeysUpload implements POST and PUT /_matrix/client/v3/keys/upload.
//
// Both methods are served because the specification says POST and clients written against
// older text send PUT; a client that sent the right document to the right path is not the
// client to punish for the specification's history.
func (d *Deps) handleKeysUpload(w http.ResponseWriter, r *http.Request) {
	deviceID := auth.DeviceID(r)
	if deviceID == "" {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"This access token has no device, and keys belong to a device."))
		return
	}
	var req e2ekeys.UploadRequest
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &req) {
		return
	}
	counts, err := d.E2EKeys.Upload(r.Context(), auth.UserID(r), deviceID, req)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"one_time_key_counts": counts})
}

// handleKeysQuery implements POST /_matrix/client/v3/keys/query.
func (d *Deps) handleKeysQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceKeys map[string][]string `json:"device_keys"`
		Since      string              `json:"since"`
	}
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if len(body.DeviceKeys) == 0 {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A key query must name at least one user."))
		return
	}
	var since int64
	if body.Since != "" {
		position, err := sync.ParseToken(body.Since)
		if err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
				"The since parameter is not a token this server issued."))
			return
		}
		since = int64(position)
	}
	devices, err := d.E2EKeys.Query(r.Context(), body.DeviceKeys, since)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"device_keys": devices})
}

// handleKeysClaim implements POST /_matrix/client/v3/keys/claim.
func (d *Deps) handleKeysClaim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OneTimeKeys map[string]map[string]string `json:"one_time_keys"`
		Limit       map[string]int               `json:"limit"`
	}
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if len(body.OneTimeKeys) == 0 {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A key claim must name at least one device."))
		return
	}
	claimed, err := d.E2EKeys.Claim(r.Context(), body.OneTimeKeys, body.Limit)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"one_time_keys": claimed})
}
