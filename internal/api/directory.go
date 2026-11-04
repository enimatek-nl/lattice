package api

import (
	"encoding/json"
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// handleGetAlias implements GET /_matrix/client/v3/directory/room/{roomAlias}.
//
//	@Summary	Resolve a room alias
//	@Tags		directory
//	@Produce	json
//	@Param		roomAlias	path	string	true	"Room alias"
//	@Success	200			{object}	map[string]any
//	@Router		/_matrix/client/v3/directory/room/{roomAlias} [get]
func (a *App) handleGetAlias(w http.ResponseWriter, r *http.Request) {
	alias := r.PathValue("roomAlias")
	roomID, err := a.svc.RoomOrAlias(r.Context(), alias)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	servers, err := a.svc.RoomServers(r.Context(), roomID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room_id": roomID, "servers": servers})
}

// handleSetAlias implements PUT /_matrix/client/v3/directory/room/{roomAlias}.
//
//	@Summary	Create a room alias
//	@Tags		directory
//	@Accept		json
//	@Produce	json
//	@Param		roomAlias	path	string				true	"Room alias"
//	@Param		body		body	map[string]string	true	"Room ID to point at"
//	@Success	200			{object}	map[string]any
//	@Router		/_matrix/client/v3/directory/room/{roomAlias} [put]
func (a *App) handleSetAlias(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RoomID string `json:"room_id"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if _, err := a.svc.CreateAlias(r.Context(), r.PathValue("roomAlias"), body.RoomID, auth.GetUserID(r)); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleDeleteAlias implements DELETE /_matrix/client/v3/directory/room/{roomAlias}.
func (a *App) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteAlias(r.Context(), r.PathValue("roomAlias"), auth.GetUserID(r)); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleRoomAliases implements GET /_matrix/client/v3/rooms/{roomId}/aliases.
func (a *App) handleRoomAliases(w http.ResponseWriter, r *http.Request) {
	aliases, err := a.svc.ListRoomAliases(r.Context(), r.PathValue("roomId"), auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"aliases": aliases})
}

// handlePublicRooms implements GET and POST /_matrix/client/v3/publicRooms.
//
//	@Summary	List the public room directory
//	@Tags		directory
//	@Produce	json
//	@Success	200	{object}	service.PublicRoomsResult
//	@Router		/_matrix/client/v3/publicRooms [get]
func (a *App) handlePublicRooms(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// The filter is an object on the POST and a bare string on the GET, which is
		// what the specification says and what clients send: a body whose filter is a
		// string is a client that read an older draft, and answering it with a shape
		// error is how a directory search becomes unusable.
		Filter   publicRoomsFilter `json:"filter"`
		Limit    int               `json:"limit"`
		Since    string            `json:"since"`
		RoomType string            `json:"room_network"`
		// Server names whose directory is being asked for. A client reading another
		// server's shelf gets that server's answer, because "published" is a decision a
		// server makes about its own rooms and no other server can make it.
		Server string `json:"server"`
	}
	if r.Method == http.MethodPost {
		if err := decodeBody(r, &body); err != nil {
			a.writeError(w, r, err)
			return
		}
	} else {
		body.Filter.GenericSearchTerm = queryValue(r, "filter", "")
		limit, err := queryInt(r, "limit", 100)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		body.Limit, body.Since = limit, queryValue(r, "since", "")
		body.Server = queryValue(r, "server", "")
	}
	if body.Server != "" && body.Server != a.svc.ServerName() {
		// Another server's directory: ask that server, and answer the client with what it
		// says. A directory entry this server invented for a room it does not host would
		// carry no way to reach the room anyway.
		answer, err := a.svc.RemotePublicRooms(r.Context(), body.Server,
			body.Filter.GenericSearchTerm, body.Limit, body.Since)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, answer)
		return
	}
	result, err := a.svc.PublicRooms(r.Context(), body.Filter.GenericSearchTerm, body.Limit, body.Since)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// publicRoomsFilter is the filter object of POST /publicRooms. Only
// generic_search_term is honoured; room_types is accepted and ignored, because a
// room's type is state this server does not index in the directory table.
type publicRoomsFilter struct {
	GenericSearchTerm string     `json:"generic_search_term"`
	RoomTypes         *[]*string `json:"room_types"`
}

// UnmarshalJSON also accepts a bare string, which is the shape older clients send.
func (f *publicRoomsFilter) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		f.GenericSearchTerm = text
		return nil
	}
	type filter publicRoomsFilter
	var decoded filter
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*f = publicRoomsFilter(decoded)
	return nil
}

// handleGetVisibility implements GET /_matrix/client/v3/rooms/{roomId}/directory/visibility.
func (a *App) handleGetVisibility(w http.ResponseWriter, r *http.Request) {
	visibility, err := a.svc.DirectoryVisibility(r.Context(), r.PathValue("roomId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"visibility": visibility})
}

// handleSetVisibility implements PUT /_matrix/client/v3/rooms/{roomId}/directory/visibility.
func (a *App) handleSetVisibility(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Visibility string `json:"visibility"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.SetDirectoryVisibility(r.Context(), r.PathValue("roomId"), body.Visibility, auth.GetUserID(r)); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleMembers implements GET /_matrix/client/v3/rooms/{roomId}/members.
//
//	@Summary	List room members
//	@Tags		rooms
//	@Produce	json
//	@Param		roomId		path	string	true	"Room ID"
//	@Param		membership	query	string	false	"Membership filter"
//	@Success	200			{object}	map[string]any
//	@Router		/_matrix/client/v3/rooms/{roomId}/members [get]
func (a *App) handleMembers(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if _, err := a.svc.MustRoom(r.Context(), roomID); err != nil {
		a.writeError(w, r, err)
		return
	}
	userID := auth.GetUserID(r)
	if userID != "" {
		if err := a.svc.RequireReadable(r.Context(), roomID, userID); err != nil {
			a.writeError(w, r, err)
			return
		}
	} else if !a.svc.GuestsCanRead(r.Context(), roomID) {
		a.writeError(w, r, matrix.ErrForbidden("room is not readable without an access token"))
		return
	}
	membership := queryValue(r, "membership", "")
	notMembership := queryValue(r, "not_membership", "")
	var filters []string
	if membership != "" {
		filters = append(filters, splitList(membership)...)
	}
	// A former member of a room whose history opened at the join reads the members the room
	// had when they left, not the ones it has now.
	bound, _ := a.svc.HistoryBound(r.Context(), roomID, userID)
	members, err := a.svc.Members(r.Context(), roomID, userID, filters, notMembership, bound)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	// The chunk is the member events themselves, in client format, so each entry
	// carries type, state_key, sender and content like any other event. A wrapper of
	// user_id/membership keys answers a different question, and a client reaching for
	// the event of a membership finds nothing to verify.
	chunk := make([]any, 0, len(members))
	for _, member := range members {
		if member.Event == nil {
			// A membership row with no event behind it: a room this server knows the
			// user is in but never saw the membership for. Answer with what is known
			// rather than leaving the member out.
			stateKey := member.UserID
			chunk = append(chunk, &matrix.Event{
				RoomID:   roomID,
				Type:     matrix.TypeMember,
				StateKey: &stateKey,
				Sender:   member.UserID,
				Content: matrix.Encode(matrix.MemberContent{
					Membership:  member.Membership,
					DisplayName: member.DisplayName,
					AvatarURL:   member.AvatarURL,
				}),
			})
			continue
		}
		event := *member.Event
		event.RoomID = roomID
		chunk = append(chunk, &event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"chunk": chunk})
}

// handleJoinedMembers implements GET /_matrix/client/v3/rooms/{roomId}/joined_members.
func (a *App) handleJoinedMembers(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if err := a.svc.RequireJoined(r.Context(), roomID, auth.GetUserID(r)); err != nil {
		a.writeError(w, r, err)
		return
	}
	joined, err := a.svc.JoinedMembers(r.Context(), roomID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	count := int64(len(joined))
	writeJSON(w, http.StatusOK, map[string]any{
		"joined":              joined,
		"joined_member_count": count,
	})
}

// handleUserDirectorySearch implements POST /_matrix/client/v3/user_directory/search.
func (a *App) handleUserDirectorySearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SearchTerm string `json:"search_term"`
		Limit      int    `json:"limit"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	users, limited, err := a.svc.SearchUserDirectory(r.Context(), auth.GetUserID(r), body.SearchTerm, body.Limit)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": users, "limited": limited})
}

// splitList splits a comma separated query parameter into trimmed parts.
func splitList(value string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(value); i++ {
		if i == len(value) || value[i] == ',' {
			part := trimSpace(value[start:i])
			if part != "" {
				parts = append(parts, part)
			}
			start = i + 1
		}
	}
	return parts
}

// trimSpace trims surrounding spaces, tabs and newlines.
func trimSpace(value string) string {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\t' || value[0] == '\n') {
		value = value[1:]
	}
	for len(value) > 0 && (value[len(value)-1] == ' ' || value[len(value)-1] == '\t' || value[len(value)-1] == '\n') {
		value = value[:len(value)-1]
	}
	return value
}
