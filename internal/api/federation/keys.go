package federation

import (
	"context"
	"net/http"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// DeviceKeys answers a device key query about this server's own users. It is the same store the
// Client-Server API reads, reached here through the Server-Server API.
type DeviceKeys interface {
	QueryLocal(ctx context.Context, users map[string][]string, since int64) (map[string]map[string]any, error)
}

// deviceKeyQueryRequest is the body of POST /_matrix/federation/v1/user/keys/query.
type deviceKeyQueryRequest struct {
	// DeviceKeys maps a user ID to the devices of theirs that are being asked about. An
	// empty list means every device that user has.
	DeviceKeys map[string][]string `json:"device_keys"`
}

// handleDeviceKeysQuery implements POST /_matrix/federation/v1/user/keys/query.
//
// A homeserver is the authority on the key documents the devices of its own users uploaded, and
// this is where another homeserver finds out what they are. Two things follow from that, and both
// are refusals rather than politeness.
//
// Only users in this server's own namespace are answered for. A request naming another server's
// users is not this server's question to answer, and looking them up anyway would let one homeserver
// put words into another's mouth about whose keys belong to whom.
//
// The answer carries the key documents exactly as they were uploaded. They are signed by the devices
// that own them, and the client that reads them is the one that checks those signatures; a server
// that re-encoded them on the way through would break the only check that matters.
//
// This server does not store cross-signing keys yet, so it does not report `master_keys` or
// `self_signing_keys`; the device keys a device uploaded are what it can honestly give.
func (d *Deps) handleDeviceKeysQuery(w http.ResponseWriter, r *http.Request) {
	var req deviceKeyQueryRequest
	if err := httpx.DecodeJSON(r, 1<<20, &req); err != nil {
		matrixerr.WriteError(w, http.StatusBadRequest, err)
		return
	}
	local := make(map[string][]string, len(req.DeviceKeys))
	for userID, deviceIDs := range req.DeviceKeys {
		_, domain, err := matrixid.SplitID(matrixid.SigilUser, userID)
		if err != nil || !matrixid.IsLocal(domain, d.Cfg.ServerName) {
			continue
		}
		local[userID] = deviceIDs
	}
	if len(local) == 0 {
		// Nothing in the question belongs to this server. That is answered with an empty
		// result rather than an error: the asking server has not done anything wrong by
		// naming users it hoped lived here, and an error would be read as a failure of the
		// transport.
		d.Log.Debug("federation: a device key query named nobody who lives here",
			"named", len(req.DeviceKeys))
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"device_keys": map[string]any{}})
		return
	}
	devices, err := d.E2EKeys.QueryLocal(r.Context(), local, 0)
	if err != nil {
		d.Log.Error("federation: cannot read the device keys this server was asked for", "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"device_keys": devices})
}
