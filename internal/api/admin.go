package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// adminFlags maps the administrative account flags to their JSON field names.
var adminFlags = map[string]string{
	"lock":    "locked",
	"suspend": "suspended",
}

// handleAdminFlagGet implements GET /_matrix/client/v1/admin/{flag}/{userId}.
//
//	@Summary	Read an account flag (admin only)
//	@Tags		admin
//	@Produce	json
//	@Param		flag	path	string	true	"lock or suspend"
//	@Success	200		{object}	map[string]bool
//	@Router		/_matrix/client/v1/admin/{flag}/{userId} [get]
func (a *App) handleAdminFlagGet(w http.ResponseWriter, r *http.Request) {
	flag, ok := adminFlagOr404(a, w, r)
	if !ok {
		return
	}
	value, err := a.svc.AccountFlags(r.Context(), r.PathValue("userId"), flag)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{adminFlags[flag]: value})
}

// handleAdminFlagSet implements PUT /_matrix/client/v1/admin/{flag}/{userId}.
//
//	@Summary	Set an account flag (admin only)
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Param		flag	path	string	true	"lock or suspend"
//	@Success	200		{object}	map[string]bool
//	@Router		/_matrix/client/v1/admin/{flag}/{userId} [put]
func (a *App) handleAdminFlagSet(w http.ResponseWriter, r *http.Request) {
	flag, ok := adminFlagOr404(a, w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	value, err := a.svc.SetAccountFlag(r.Context(), r.PathValue("userId"), flag, enabled, true, nil)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{adminFlags[flag]: value})
}

// adminFlagOr404 resolves the flag path segment.
func adminFlagOr404(a *App, w http.ResponseWriter, r *http.Request) (string, bool) {
	flag := r.PathValue("flag")
	if _, ok := adminFlags[flag]; !ok {
		a.writeError(w, r, matrix.ErrNotFound("unknown account flag %q", flag))
		return "", false
	}
	return flag, true
}

// handleAdminMaintenance implements POST /_synapse/admin/v1/maintenance. It runs
// the housekeeping pass that drops expired to-device messages, remembered
// transaction ids and typing indicators nobody refreshed.
//
//	@Summary	Run server housekeeping
//	@Tags		administration
//	@Produce	json
//	@Success	200	{object}	service.MaintenanceReport
//	@Router		/_synapse/admin/v1/maintenance [post]
func (a *App) handleAdminMaintenance(w http.ResponseWriter, r *http.Request) {
	report, err := a.svc.RunMaintenance(r.Context())
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handleAdminReports implements GET /_matrix/client/v1/admin/reports: the abuse
// reports clients have filed, newest first.
//
//	@Summary	List abuse reports
//	@Tags		administration
//	@Produce	json
//	@Param		kind	query	string	false	"event, room or user"
//	@Param		room_id	query	string	false	"only reports about this room"
//	@Param		user_id	query	string	false	"only reports about this user"
//	@Param		reporter	query	string	false	"only reports filed by this user"
//	@Success	200	{object}	object
//	@Router		/_matrix/client/v1/admin/reports [get]
func (a *App) handleAdminReports(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	query := service.ReportQuery{
		Kind:     queryValue(r, "kind", ""),
		RoomID:   queryValue(r, "room_id", ""),
		EventID:  queryValue(r, "event_id", ""),
		UserID:   queryValue(r, "user_id", ""),
		Reporter: queryValue(r, "reporter", ""),
		Limit:    limit,
		Offset:   offset,
	}
	reports, total, err := a.svc.Reports(r.Context(), query)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{"reports": reports, "total": total})
}
