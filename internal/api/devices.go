package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// handleListDevices implements GET /_matrix/client/v3/devices.
//
//	@Summary	List the devices of the caller
//	@Tags		devices
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/devices [get]
func (a *App) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := a.svc.ListDevices(r.Context(), auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

// handleGetDevice implements GET /_matrix/client/v3/devices/{deviceId}.
func (a *App) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	device, err := a.svc.DeviceByID(r.Context(), auth.GetUserID(r), r.PathValue("deviceId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, device)
}

// handleUpdateDevice implements PUT /_matrix/client/v3/devices/{deviceId}.
//
//	@Summary	Rename a device
//	@Tags		devices
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/devices/{deviceId} [put]
func (a *App) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.UpdateDevice(r.Context(), auth.GetUserID(r), r.PathValue("deviceId"), body.DisplayName); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleDeleteDevice implements DELETE /_matrix/client/v3/devices/{deviceId}.
func (a *App) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteDevice(r.Context(), auth.GetUserID(r), r.PathValue("deviceId")); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleDeleteDevices implements POST /_matrix/client/v3/delete_devices.
//
//	@Summary	Delete several devices
//	@Tags		devices
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/delete_devices [post]
func (a *App) handleDeleteDevices(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Devices []string      `json:"devices"`
		Auth    matrix.Object `json:"auth,omitempty"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	failed, err := a.svc.DeleteDevices(r.Context(), auth.GetUserID(r), body.Devices, body.Auth)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"failed": failed})
}

// handleWhoIs implements GET /_synapse/admin/v1/whois/{userId}.
//
//	@Summary	Inspect the sessions of a user (admin only)
//	@Tags		admin
//	@Produce	json
//	@Success	200	{object}	matrix.Object
//	@Router		/_synapse/admin/v1/whois/{userId} [get]
func (a *App) handleWhoIs(w http.ResponseWriter, r *http.Request) {
	whois, err := a.svc.WhoIs(r.Context(), r.PathValue("userId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, whois)
}

// handleAdminGetUser implements GET /_synapse/admin/v2/users/{userId}.
//
//	@Summary	Read administrative account flags (admin only)
//	@Tags		admin
//	@Produce	json
//	@Success	200	{object}	matrix.Object
//	@Router		/_synapse/admin/v2/users/{userId} [get]
func (a *App) handleAdminGetUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")
	locked, err := a.svc.AccountFlags(r.Context(), userID, "lock")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	suspended, err := a.svc.AccountFlags(r.Context(), userID, "suspend")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{
		"user_id":   userID,
		"locked":    locked,
		"suspended": suspended,
		"admin":     a.svc.IsAdmin(r.Context(), userID),
	})
}

// handleAdminUpdateUser implements PUT /_synapse/admin/v2/users/{userId}.
//
//	@Summary	Update administrative account flags (admin only)
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	matrix.Object
//	@Router		/_synapse/admin/v2/users/{userId} [put]
func (a *App) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Locked    *bool `json:"locked"`
		Suspended *bool `json:"suspended"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	userID := r.PathValue("userId")
	result := matrix.Object{"user_id": userID}
	if body.Locked != nil {
		value, err := a.svc.SetAccountFlag(r.Context(), userID, "lock", *body.Locked, true, nil)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		result["locked"] = value
	}
	if body.Suspended != nil {
		value, err := a.svc.SetAccountFlag(r.Context(), userID, "suspend", *body.Suspended, true, nil)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		result["suspended"] = value
	}
	writeJSON(w, http.StatusOK, result)
}
