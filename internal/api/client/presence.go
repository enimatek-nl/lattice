package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// Presence: GET and PUT /_matrix/client/v3/presence/{userID}/status.
//
// Three things about these endpoints are easy to get wrong, so they are stated where the
// code is.
//
// A user may only set their own presence: the path names a user, the token names another,
// and the path is not a permission.
//
// Reading somebody else's presence is answered even for a user this server has never
// reported. "Offline" is the answer to "are they around"; it is not the answer to "do I
// know them", and a client handed a 404 would show nothing at all.
//
// Presence for a user on another server is a different question — it arrives over
// federation, which this server does not do yet — so it is answered as not known rather
// than guessed.

// handlePresenceGet serves GET /_matrix/client/v3/presence/{userID}/status.
func (d *Deps) handlePresenceGet(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	local, err := d.localUser(userID)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	if !local {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeNotFound,
			"That user is not on this server, and this server has no presence of them to share."))
		return
	}
	status, err := d.Presence.Status(r.Context(), userID)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	body := map[string]any{
		"presence":         status.Presence,
		"currently_active": status.Active,
	}
	if status.Known {
		body["last_active_ago"] = status.LastActiveAgo
	}
	if status.StatusMsg != "" {
		body["status_msg"] = status.StatusMsg
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// handlePresencePut serves PUT /_matrix/client/v3/presence/{userID}/status.
func (d *Deps) handlePresencePut(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	if userID != auth.UserID(r) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"A user may only set their own presence."))
		return
	}
	var body struct {
		Presence  string `json:"presence"`
		StatusMsg string `json:"status_msg"`
	}
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if !persist.PresenceKnown(body.Presence) {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a presence this server knows.", body.Presence))
		return
	}
	if err := d.Presence.Set(r.Context(), userID, body.Presence, body.StatusMsg); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// applySyncPresence does what a `/sync` says about the user who is syncing.
//
// The parameter defaults to `online`, as the specification says. An unchanged status is
// not written again: a client polls, and writing on every poll would announce the user to
// everybody they share a room with over and over. An unrecognised value is ignored,
// because a client that mistypes a word still needs its events.
func (d *Deps) applySyncPresence(w http.ResponseWriter, r *http.Request, userID, setPresence string) bool {
	d.Presence.MarkActive(r.Context(), userID)
	if setPresence == "" {
		setPresence = persist.PresenceOnline
	}
	if !persist.PresenceKnown(setPresence) {
		d.Log.Debug("sync: ignoring an unknown set_presence value", "set_presence", setPresence)
		return true
	}
	if err := d.Presence.SetIfChanged(r.Context(), userID, setPresence); err != nil {
		d.serviceError(w, err)
		return false
	}
	return true
}

// localUser reports whether a user ID belongs to this server, answering a malformed ID
// rather than letting it reach a query.
func (d *Deps) localUser(userID string) (bool, error) {
	domain, err := matrixid.UserDomain(userID)
	if err != nil {
		return false, matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", userID)
	}
	return matrixid.IsLocal(domain, d.Cfg.ServerName), nil
}
