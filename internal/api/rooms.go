package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// createRoomBody is the request body of POST /_matrix/client/v3/createRoom.
type createRoomBody struct {
	Preset             string         `json:"preset"`
	Name               string         `json:"name"`
	Topic              string         `json:"topic"`
	Type               string         `json:"type"`
	Visibility         string         `json:"visibility"`
	RoomAliasName      string         `json:"room_alias_name"`
	RoomVersion        string         `json:"room_version"`
	CreationContent    matrix.Object  `json:"creation_content,omitempty"`
	InitialState       []matrix.Event `json:"initial_state,omitempty"`
	PowerLevelOverride matrix.Object  `json:"power_level_content_override,omitempty"`
	Invite             []string       `json:"invite,omitempty"`
	// AdditionalCreators is accepted at the top level as well as inside creation_content,
	// because that is how clients spell the room version 12 property: some put it beside
	// room_version, some inside the create event content they hand in.
	AdditionalCreators []string `json:"additional_creators,omitempty"`
	IsDirect           bool     `json:"is_direct"`
	HistoryVisibility  string   `json:"history_visibility"`
	GuestAccess        string   `json:"guest_access"`
	JoinRule           string   `json:"join_rules"`
}

// handleCreateRoom implements POST /_matrix/client/v3/createRoom.
//
//	@Summary	Create a new room
//	@Tags		rooms
//	@Accept		json
//	@Produce	json
//	@Param		body	body		createRoomBody	true	"Room creation options"
//	@Success	200		{object}	map[string]string
//	@Router		/_matrix/client/v3/createRoom [post]
func (a *App) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var body createRoomBody
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	creator := auth.GetUserID(r)
	// A room type such as m.space belongs in the creation content of the
	// m.room.create event; the specification also accepts it at the top level.
	creation := body.CreationContent
	if body.Type != "" {
		if creation == nil {
			creation = matrix.Object{}
		}
		creation["type"] = body.Type
	}
	if len(body.AdditionalCreators) > 0 {
		if creation == nil {
			creation = matrix.Object{}
		}
		creation["additional_creators"] = body.AdditionalCreators
	}
	room, err := a.svc.CreateRoom(r.Context(), service.CreateRoomParams{
		Creator:                   creator,
		Alias:                     body.RoomAliasName,
		Name:                      body.Name,
		Topic:                     body.Topic,
		Preset:                    body.Preset,
		Visibility:                body.Visibility,
		JoinRule:                  body.JoinRule,
		HistoryVisibility:         body.HistoryVisibility,
		GuestAccess:               body.GuestAccess,
		RoomVersion:               body.RoomVersion,
		CreationContent:           creation,
		PowerLevelContentOverride: body.PowerLevelOverride,
		InitialStates:             body.InitialState,
		Invite:                    body.Invite,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	for _, invitee := range body.Invite {
		// A room created as a direct conversation says so in the invitations it makes, not
		// only in the creator's account data: a client deciding whether a room is a DM reads
		// is_direct off the membership event, and the other server - the one the invited user
		// lives on - only ever sees that event.
		var extra matrix.Object
		if body.IsDirect {
			extra = matrix.Object{"is_direct": true}
		}
		if err := a.svc.InviteRoom(r.Context(), room.ID, creator, invitee, "", extra); err != nil {
			a.writeError(w, r, err)
			return
		}
	}
	if body.IsDirect {
		if err := a.svc.MarkDirectRoom(r.Context(), creator, room.ID, body.Invite); err != nil {
			a.writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"room_id": room.ID})
}

// handleJoinRoom implements POST /_matrix/client/v3/join/{roomIdOrAlias}.
//
//	@Summary	Join a room by ID or alias
//	@Tags		rooms
//	@Accept		json
//	@Produce	json
//	@Param		roomIdOrAlias	path	string	true	"Room ID or alias"
//	@Success	200				{object}	map[string]string
//	@Router		/_matrix/client/v3/join/{roomIdOrAlias} [post]
func (a *App) handleJoinRoom(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("roomIdOrAlias")
	if target == "" {
		target = r.PathValue("roomId")
	}
	via := r.URL.Query()["server_name"]
	roomID, err := a.svc.JoinRoom(r.Context(), target, auth.GetUserID(r), bodyObject(r), via)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"room_id": roomID})
}

// handleJoinRoomByID implements POST /_matrix/client/v3/rooms/{roomId}/join.
//
//	@Summary	Join a room by room ID
//	@Tags		rooms
//	@Produce	json
//	@Param		roomId	path	string	true	"Room ID"
//	@Success	200		{object}	map[string]string
//	@Router		/_matrix/client/v3/rooms/{roomId}/join [post]
func (a *App) handleJoinRoomByID(w http.ResponseWriter, r *http.Request) {
	a.handleJoinRoom(w, r)
}

// handleLeaveRoom implements POST /_matrix/client/v3/rooms/{roomId}/leave.
//
//	@Summary	Leave a room
//	@Tags		rooms
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/rooms/{roomId}/leave [post]
func (a *App) handleLeaveRoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.LeaveRoom(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), body.Reason); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleKnockRoom implements POST /_matrix/client/v3/knock/{roomIdOrAlias} and
// POST /_matrix/client/v3/rooms/{roomId}/knock.
func (a *App) handleKnockRoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	target := r.PathValue("roomIdOrAlias")
	if target == "" {
		target = r.PathValue("roomId")
	}
	// The servers the client names are the ones it thinks can let the user in. For a room this
	// server has never met - and a room id of the versions that hash room ids names no server to
	// ask - they may be the only names there are.
	via := append(r.URL.Query()["via"], r.URL.Query()["server_name"]...)
	roomID, err := a.svc.KnockRoom(r.Context(), target, auth.GetUserID(r), body.Reason, via)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"room_id": roomID})
}

// handleForgetRoom implements POST /_matrix/client/v3/rooms/{roomId}/forget.
func (a *App) handleForgetRoom(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.ForgetRoom(r.Context(), r.PathValue("roomId"), auth.GetUserID(r)); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleInvite implements POST /_matrix/client/v3/rooms/{roomId}/invite.
//
//	@Summary	Invite a user to a room
//	@Tags		rooms
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/rooms/{roomId}/invite [post]
func (a *App) handleInvite(w http.ResponseWriter, r *http.Request) {
	// The body is read as an object rather than a fixed struct, because an invitation can
	// carry keys this server does not name and they have to survive: is_direct in
	// particular is how a client tells a direct conversation from a group, and it is read
	// off the membership event the invitation becomes.
	var body matrix.Object
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	invitee, _ := body["user_id"].(string)
	reason, _ := body["reason"].(string)
	if invitee == "" {
		a.writeError(w, r, matrix.ErrInvalidParam("the invite names no user"))
		return
	}
	extra := matrix.Object{}
	for key, value := range body {
		switch key {
		case "user_id", "reason", "third_party_signed":
			continue
		}
		extra[key] = value
	}
	if signed, ok := body["third_party_signed"].(matrix.Object); ok && len(signed) > 0 {
		// A third-party invite only makes sense together with an identity server
		// signature exchange, which Lattice does not speak. Saying so beats
		// accepting a request that then invites nobody.
		a.writeError(w, r, matrix.ErrInvalidParam("third-party invites are not supported"))
		return
	}
	if err := a.svc.InviteRoom(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), invitee, reason, extra); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleKick implements POST /_matrix/client/v3/rooms/{roomId}/kick.
func (a *App) handleKick(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string `json:"user_id"`
		Reason string `json:"reason"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.KickRoom(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), body.UserID, body.Reason); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleBan implements POST /_matrix/client/v3/rooms/{roomId}/ban.
func (a *App) handleBan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string `json:"user_id"`
		Reason string `json:"reason"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.BanRoom(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), body.UserID, body.Reason); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleUnban implements POST /_matrix/client/v3/rooms/{roomId}/unban.
func (a *App) handleUnban(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.UnbanRoom(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), body.UserID); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleJoinedRooms implements GET /_matrix/client/v3/joined_rooms.
//
//	@Summary	List the rooms the caller has joined
//	@Tags		rooms
//	@Produce	json
//	@Success	200	{object}	map[string][]string
//	@Router		/_matrix/client/v3/joined_rooms [get]
func (a *App) handleJoinedRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := a.svc.JoinedRooms(r.Context(), auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"joined_rooms": rooms})
}

// handleRoomSummary implements GET /_matrix/client/v1/rooms/{roomIdOrAlias}/summary.
func (a *App) handleRoomSummary(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("roomIdOrAlias")
	if target == "" {
		target = r.PathValue("roomId")
	}
	summary, err := a.svc.RoomSummary(r.Context(), target, auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// handleUpgradeRoom implements POST /_matrix/client/v3/rooms/{roomId}/upgrade.
func (a *App) handleUpgradeRoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NewVersion string `json:"new_version"`
		Reason     string `json:"reason"`
		// AdditionalCreators are the users the new room is to hold as creators beside the one
		// doing the upgrade. Only a room version 12 successor reads them.
		AdditionalCreators []string `json:"additional_creators,omitempty"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	newRoomID, err := a.svc.UpgradeRoom(r.Context(), r.PathValue("roomId"), auth.GetUserID(r), body.NewVersion, body.Reason, body.AdditionalCreators)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"replacement_room": newRoomID})
}

// bodyObject decodes a JSON body into a generic object. An empty or
// undecodable body yields an empty object: the endpoints that use it treat a
// body they cannot read as no body at all rather than rejecting the request.
func bodyObject(r *http.Request) matrix.Object {
	out := matrix.Object{}
	_ = decodeBody(r, &out)
	return out
}
