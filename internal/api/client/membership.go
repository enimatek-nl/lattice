package client

import (
	"net/http"
	"net/url"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/rooms"
)

// membershipBody is the request body of the membership endpoints. The fields the
// endpoints share are `user_id` and `reason`; `via` names the servers to route a join
// through, and `third_party_signed` carries a third-party invite.
//
// `via` is accepted and ignored for a room this server already has, because it only
// matters when the room must be reached over another server. `third_party_signed` is
// refused: honouring it means checking an invitation signed by an identity server that
// this server has never spoken to, and pretending otherwise would accept an unchecked
// assertion that a user was invited.
type membershipBody struct {
	UserID           string         `json:"user_id"`
	Reason           string         `json:"reason"`
	Via              []string       `json:"via"`
	ThirdPartySigned map[string]any `json:"third_party_signed"`
}

// handleJoin implements POST /_matrix/client/v3/join/{roomIDOrAlias}.
func (d *Deps) handleJoin(w http.ResponseWriter, r *http.Request) {
	extra, ok := d.jsonBody(w, r)
	if !ok {
		return
	}
	// The specification offers two spellings of the same action: POST /join/{roomID}
	// and POST /rooms/{roomID}/join. Clients use both, and answering one of them with
	// M_UNRECOGNIZED looks like a broken server rather than a missing feature.
	identifier := pathValue(r, "roomIDOrAlias")
	if identifier == "" {
		identifier = r.PathValue("roomID")
	}
	roomID, ok := d.resolveRoomIdentifier(w, r, identifier)
	if !ok {
		return
	}

	eventID, err := d.Rooms.SetMembership(r.Context(), rooms.MembershipChange{
		RoomID:     roomID,
		Target:     auth.UserID(r),
		Sender:     auth.UserID(r),
		DeviceID:   auth.DeviceID(r),
		Membership: eventauth.MembershipJoin,
		Reason:     reasonOf(extra),
		Extra:      extra,
		Via:        viaServers(r),
	})
	if err != nil {
		d.serviceError(w, err)
		return
	}
	_ = eventID
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"room_id": roomID})
}

// viaServers reads the servers a client names as able to reach a room. The Client-Server API
// spells them as repeated `server_name` query parameters; `via` is the older spelling and
// clients still send it, so both are read. They are only a starting point: which of them holds
// the room is what the join handshake finds out.
func viaServers(r *http.Request) []string {
	values := append([]string{}, r.URL.Query()["server_name"]...)
	return append(values, r.URL.Query()["via"]...)
}

// handleLeave implements POST /_matrix/client/v3/rooms/{roomID}/leave.
func (d *Deps) handleLeave(w http.ResponseWriter, r *http.Request) {
	var body membershipBody
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	userID := auth.UserID(r)
	if _, err := d.Rooms.SetMembership(r.Context(), rooms.MembershipChange{
		RoomID:     r.PathValue("roomID"),
		Target:     userID,
		Sender:     userID,
		DeviceID:   auth.DeviceID(r),
		Membership: eventauth.MembershipLeave,
		Reason:     body.Reason,
	}); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKnock implements POST /_matrix/client/v3/rooms/{roomID}/knock.
//
// Whether knocking is allowed is a property of the room — its join rule and its room
// version — so the decision is left to the authorisation rules rather than duplicated
// here as a version check.
func (d *Deps) handleKnock(w http.ResponseWriter, r *http.Request) {
	extra, ok := d.jsonBody(w, r)
	if !ok {
		return
	}
	userID := auth.UserID(r)
	if _, err := d.Rooms.SetMembership(r.Context(), rooms.MembershipChange{
		RoomID:     r.PathValue("roomID"),
		Target:     userID,
		Sender:     userID,
		DeviceID:   auth.DeviceID(r),
		Membership: eventauth.MembershipKnock,
		Reason:     reasonOf(extra),
		Extra:      extra,
	}); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleInvite implements POST /_matrix/client/v3/rooms/{roomID}/invite.
func (d *Deps) handleInvite(w http.ResponseWriter, r *http.Request) {
	var body membershipBody
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if body.ThirdPartySigned != nil {
		matrixerr.Write(w, http.StatusNotImplemented, matrixerr.New(matrixerr.CodeUnknown,
			"Third-party invites are not implemented: this server cannot verify an invitation signed by an identity server it has not spoken to."))
		return
	}
	if body.UserID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"An invitation must name the user to invite."))
		return
	}
	// An invitation to an account that does not exist here would create state that
	// names a user this server knows nothing about, so the account is looked up first.
	if domain, err := matrixid.UserDomain(body.UserID); err == nil && matrixid.IsLocal(domain, d.Cfg.ServerName) {
		if _, err := d.Users.Get(r.Context(), body.UserID); err != nil {
			d.serviceError(w, err)
			return
		}
	}

	if _, err := d.Rooms.SetMembership(r.Context(), rooms.MembershipChange{
		RoomID:     r.PathValue("roomID"),
		Target:     body.UserID,
		Sender:     auth.UserID(r),
		DeviceID:   auth.DeviceID(r),
		Membership: eventauth.MembershipInvite,
		Reason:     body.Reason,
	}); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKick implements POST /_matrix/client/v3/rooms/{roomID}/kick.
func (d *Deps) handleKick(w http.ResponseWriter, r *http.Request) {
	d.changeMembershipOfOther(w, r, eventauth.MembershipLeave)
}

// handleBan implements POST /_matrix/client/v3/rooms/{roomID}/ban.
func (d *Deps) handleBan(w http.ResponseWriter, r *http.Request) {
	d.changeMembershipOfOther(w, r, eventauth.MembershipBan)
}

// handleUnban implements POST /_matrix/client/v3/rooms/{roomID}/unban.
//
// Un-banning is a leave event for that user: the specification has no "unbanned"
// membership, and the absence of a ban is expressed by the member event no longer
// saying ban.
func (d *Deps) handleUnban(w http.ResponseWriter, r *http.Request) {
	d.changeMembershipOfOther(w, r, eventauth.MembershipLeave)
}

func (d *Deps) changeMembershipOfOther(w http.ResponseWriter, r *http.Request, membership string) {
	var body membershipBody
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if body.UserID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"The request must name the user it acts on."))
		return
	}
	if _, err := d.Rooms.SetMembership(r.Context(), rooms.MembershipChange{
		RoomID:     r.PathValue("roomID"),
		Target:     body.UserID,
		Sender:     auth.UserID(r),
		DeviceID:   auth.DeviceID(r),
		Membership: membership,
		Reason:     body.Reason,
	}); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleMembers implements GET /_matrix/client/v3/rooms/{roomID}/members.
//
// `not_membership` excludes one membership rather than selecting one, which is what
// clients ask for: the useful list is "everyone here, or everyone who was invited".
func (d *Deps) handleMembers(w http.ResponseWriter, r *http.Request) {
	filter := rooms.MemberFilter{Exclude: r.URL.Query().Get("not_membership")}
	members, err := d.Rooms.Members(r.Context(), r.PathValue("roomID"), auth.UserID(r), filter)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"chunk": members})
}

// handleJoinedMembers implements GET /_matrix/client/v3/rooms/{roomID}/joined_members.
//
// The display name and avatar come from the member event, which is what this server
// has: profile management is a separate part of the API and is not implemented yet, so
// a joined member who has not set a display name is reported without one rather than
// with a made-up value.
func (d *Deps) handleJoinedMembers(w http.ResponseWriter, r *http.Request) {
	members, err := d.Rooms.JoinedMembers(r.Context(), r.PathValue("roomID"), auth.UserID(r))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	joined := make(map[string]any, len(members))
	for _, event := range members {
		userID, _ := event["state_key"].(string)
		content, _ := event["content"].(map[string]any)
		displayName, _ := content["display_name"].(string)
		avatarURL, _ := content["avatar_url"].(string)
		joined[userID] = map[string]any{"display_name": displayName, "avatar_url": avatarURL}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"joined": joined})
}

// handleJoinedRooms implements GET /_matrix/client/v3/joined_rooms.
func (d *Deps) handleJoinedRooms(w http.ResponseWriter, r *http.Request) {
	roomIDs, err := d.Rooms.JoinedRooms(r.Context(), auth.UserID(r))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"joined_rooms": roomIDs})
}

// pathValue unescapes a path wildcard. A room ID contains no reserved characters in
// practice, but a state key can contain anything, and a handler that reads the raw
// value would treat an escaped slash as two segments.
// jsonBody decodes a request body that is an arbitrary JSON object, because the join and
// knock endpoints copy their extra keys into the member event.
func (d *Deps) jsonBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var body map[string]any
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return nil, false
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, true
}

// reasonOf reads the documented `reason` key out of a membership body.
func reasonOf(body map[string]any) string {
	reason, _ := body["reason"].(string)
	return reason
}

func pathValue(r *http.Request, name string) string {
	value := r.PathValue(name)
	if decoded, err := url.PathUnescape(value); err == nil {
		return decoded
	}
	return value
}
