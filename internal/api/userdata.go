package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// handleGetAccountData implements GET /_matrix/client/v3/user/{userId}/account_data/{type}.
func (a *App) handleGetAccountData(w http.ResponseWriter, r *http.Request) {
	a.getAccountData(w, r, "")
}

// handleGetRoomAccountData implements GET /_matrix/client/v3/user/{userId}/rooms/{roomId}/account_data/{type}.
func (a *App) handleGetRoomAccountData(w http.ResponseWriter, r *http.Request) {
	a.getAccountData(w, r, r.PathValue("roomId"))
}

// getAccountData serves both the global and room scoped account data reads.
func (a *App) getAccountData(w http.ResponseWriter, r *http.Request, roomID string) {
	raw, err := a.svc.AccountData(r.Context(), r.PathValue("userId"), auth.GetUserID(r), roomID, r.PathValue("type"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if len(raw) == 0 {
		a.writeError(w, r, matrix.ErrNotFound("no account data of type %q", r.PathValue("type")))
		return
	}
	writeJSON(w, http.StatusOK, raw)
}

// handleSetAccountData implements PUT /_matrix/client/v3/user/{userId}/account_data/{type}.
//
//	@Summary	Set global account data
//	@Tags		user data
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/user/{userId}/account_data/{type} [put]
func (a *App) handleSetAccountData(w http.ResponseWriter, r *http.Request) {
	a.setAccountData(w, r, "")
}

// handleSetRoomAccountData implements PUT /_matrix/client/v3/user/{userId}/rooms/{roomId}/account_data/{type}.
func (a *App) handleSetRoomAccountData(w http.ResponseWriter, r *http.Request) {
	a.setAccountData(w, r, r.PathValue("roomId"))
}

// setAccountData stores global or room scoped account data.
func (a *App) setAccountData(w http.ResponseWriter, r *http.Request, roomID string) {
	content, err := readRawBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.SetAccountData(r.Context(), r.PathValue("userId"), auth.GetUserID(r), roomID, r.PathValue("type"), content); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleRoomTags implements GET /_matrix/client/v3/user/{userId}/rooms/{roomId}/tags.
func (a *App) handleRoomTags(w http.ResponseWriter, r *http.Request) {
	tags, err := a.svc.RoomTags(r.Context(), r.PathValue("userId"), auth.GetUserID(r), r.PathValue("roomId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// handleSetRoomTag implements PUT /_matrix/client/v3/user/{userId}/rooms/{roomId}/tags/{tag}.
func (a *App) handleSetRoomTag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Order any `json:"order"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.SetRoomTag(r.Context(), r.PathValue("userId"), auth.GetUserID(r), r.PathValue("roomId"), r.PathValue("tag"), body.Order); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleDeleteRoomTag implements DELETE /_matrix/client/v3/user/{userId}/rooms/{roomId}/tags/{tag}.
func (a *App) handleDeleteRoomTag(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteRoomTag(r.Context(), r.PathValue("userId"), auth.GetUserID(r), r.PathValue("roomId"), r.PathValue("tag")); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleGetProfile implements GET /_matrix/client/v3/profile/{userId}.
//
//	@Summary	Read a user profile
//	@Tags		user data
//	@Produce	json
//	@Param		userId	path	string	true	"User ID"
//	@Success	200		{object}	matrix.Object
//	@Router		/_matrix/client/v3/profile/{userId} [get]
func (a *App) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	// A name that carries another server's address is described by that server, so the
	// read goes out rather than failing: a member list on this server contains remote
	// users, and a client showing "@bob:other.example" to nobody's benefit is a member
	// list that lost half its people.
	profile, err := a.svc.ProfileForReader(r.Context(), r.PathValue("userId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

// handleGetProfileKey implements GET /_matrix/client/v3/profile/{userId}/{key}.
func (a *App) handleGetProfileKey(w http.ResponseWriter, r *http.Request) {
	value, err := a.svc.ProfileKeyForReader(r.Context(), r.PathValue("userId"), r.PathValue("key"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{r.PathValue("key"): value})
}

// handleSetProfileKey implements PUT /_matrix/client/v3/profile/{userId}/{key}.
//
//	@Summary	Update a profile field
//	@Tags		user data
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/profile/{userId}/{key} [put]
func (a *App) handleSetProfileKey(w http.ResponseWriter, r *http.Request) {
	if !requireSelf(w, r) {
		return
	}
	var body matrix.Object
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	key := r.PathValue("key")
	value, _ := body[key].(string)
	if err := a.svc.SetProfileKey(r.Context(), r.PathValue("userId"), key, value); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleDeleteProfileKey implements DELETE /_matrix/client/v3/profile/{userId}/{key}.
func (a *App) handleDeleteProfileKey(w http.ResponseWriter, r *http.Request) {
	if !requireSelf(w, r) {
		return
	}
	if err := a.svc.DeleteProfileKey(r.Context(), r.PathValue("userId"), r.PathValue("key")); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleGetPresence implements GET /_matrix/client/v3/presence/{userId}/status.
func (a *App) handleGetPresence(w http.ResponseWriter, r *http.Request) {
	presence, err := a.svc.Presence(r.Context(), r.PathValue("userId"), auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, presence)
}

// handleSetPresence implements PUT /_matrix/client/v3/presence/{userId}/status.
//
//	@Summary	Set the presence state
//	@Tags		user data
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/presence/{userId}/status [put]
func (a *App) handleSetPresence(w http.ResponseWriter, r *http.Request) {
	if !requireSelf(w, r) {
		return
	}
	var body struct {
		Presence        string `json:"presence"`
		StatusMsg       string `json:"status_msg"`
		CurrentlyActive *bool  `json:"currently_active"`
		LastActiveAgo   *int64 `json:"last_active_ago"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.SetPresence(r.Context(), r.PathValue("userId"), body.Presence, body.StatusMsg, body.CurrentlyActive); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleOpenIDToken implements POST /_matrix/client/v3/user/{userId}/openid/request_token.
func (a *App) handleOpenIDToken(w http.ResponseWriter, r *http.Request) {
	if !requireSelf(w, r) {
		return
	}
	token, err := a.svc.OpenIDToken(r.Context(), r.PathValue("userId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, token)
}
