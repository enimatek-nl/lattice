package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// handleWhoAmI implements GET /account/whoami.
func (d *Deps) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user_id": auth.UserID(r)})
}

// handleLogout implements POST /logout: the current device only.
func (d *Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	session := auth.SessionFromRequest(r)
	if err := d.endDevice(r, session.UserID, session.DeviceID); err != nil {
		d.writeInternal(w, "logout", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleLogoutAll implements POST /logout/all.
func (d *Deps) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	session := auth.SessionFromRequest(r)
	if err := d.Tokens.RevokeUser(r.Context(), session.UserID); err != nil {
		d.writeInternal(w, "logout/all", err)
		return
	}
	if err := d.DB.Devices.DeleteByUser(r.Context(), session.UserID); err != nil {
		d.writeInternal(w, "logout/all", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// endDevice removes a device and every token that belongs to it.
func (d *Deps) endDevice(r *http.Request, userID, deviceID string) error {
	if err := d.Tokens.RevokeDevice(r.Context(), userID, deviceID); err != nil {
		return err
	}
	return d.DB.Devices.Delete(r.Context(), userID, deviceID)
}

// deviceView is the client-visible representation of a device.
type deviceView struct {
	DeviceID    string `json:"device_id"`
	DisplayName string `json:"display_name,omitempty"`
	LastSeenIP  string `json:"last_seen_ip,omitempty"`
	LastSeenTS  any    `json:"last_seen_ts,omitempty"`
}

func toDeviceView(device persist.Device) deviceView {
	view := deviceView{DeviceID: device.DeviceID, DisplayName: device.DisplayName, LastSeenIP: device.LastSeenIP}
	if device.LastSeenTS != nil {
		view.LastSeenTS = device.LastSeenTS.UnixMilli()
	}
	return view
}

// handleDevicesList implements GET /devices.
func (d *Deps) handleDevicesList(w http.ResponseWriter, r *http.Request) {
	devices, err := d.DB.Devices.ListByUser(r.Context(), auth.UserID(r))
	if err != nil {
		d.writeInternal(w, "devices", err)
		return
	}
	views := make([]deviceView, 0, len(devices))
	for _, device := range devices {
		views = append(views, toDeviceView(device))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"devices": views})
}

// handleDeviceGet implements GET /devices/{deviceID}.
func (d *Deps) handleDeviceGet(w http.ResponseWriter, r *http.Request) {
	device, ok := d.lookupDevice(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDeviceView(*device))
}

// handleDevicePut implements PUT /devices/{deviceID}.
func (d *Deps) handleDevicePut(w http.ResponseWriter, r *http.Request) {
	device, ok := d.lookupDevice(w, r)
	if !ok {
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if err := d.DB.Devices.SetDisplayName(r.Context(), device.UserID, device.DeviceID, body.DisplayName); err != nil {
		d.writeInternal(w, "devices update", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// deviceDeleteRequest is the DELETE /devices/{deviceID} body. The specification
// requires User-Interactive Auth for this endpoint, and clients may send no body
// at all to discover the offered flows.
type deviceDeleteRequest struct {
	Auth *uiaAuthPayload `json:"auth"`
}

// handleDeviceDelete implements DELETE /devices/{deviceID}.
func (d *Deps) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	device, ok := d.lookupDevice(w, r)
	if !ok {
		return
	}
	var req deviceDeleteRequest
	if r.ContentLength != 0 {
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &req) {
			return
		}
	}
	authenticatedAs, ok := d.authorizePasswordStage(w, r, req.Auth)
	if !ok {
		return
	}
	// The credentials must belong to the account making the request, otherwise one
	// session could delete another user's device.
	if authenticatedAs != auth.UserID(r) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"The authenticated user does not own this device."))
		return
	}
	if err := d.endDevice(r, device.UserID, device.DeviceID); err != nil {
		d.writeInternal(w, "devices delete", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// lookupDevice resolves the {deviceID} path parameter for the authenticated user.
func (d *Deps) lookupDevice(w http.ResponseWriter, r *http.Request) (*persist.Device, bool) {
	userID := auth.UserID(r)
	deviceID := r.PathValue("deviceID")
	if err := matrixid.DeviceIDValid(deviceID); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam, err.Error()))
		return nil, false
	}
	device, err := d.DB.Devices.ByDeviceID(r.Context(), userID, deviceID)
	if err != nil {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeNotFound, "Device not found."))
		return nil, false
	}
	return device, true
}

// writeInternal logs an unexpected error and answers with a generic Matrix error.
func (d *Deps) writeInternal(w http.ResponseWriter, what string, err error) {
	d.Log.Error("request failed", "operation", what, "error", err)
	matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
		"The server encountered an internal error."))
}
