package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// handleRoomHierarchy implements GET /_matrix/client/v1/rooms/{roomId}/hierarchy.
//
//	@Summary	Read the space hierarchy
//	@Tags		spaces
//	@Produce	json
//	@Param		roomId		path	string	true	"Space to start the walk at"
//	@Param		from		query	string	false	"Pagination token"
//	@Param		limit		query	int		false	"Maximum number of rooms per page"
//	@Param		max_depth	query	int		false	"Maximum distance from the given room"
//	@Param		min_depth	query	int		false	"Minimum distance from the given room"
//	@Success	200			{object}	service.HierarchyResult
//	@Router		/_matrix/client/v1/rooms/{roomIdOrAlias}/hierarchy [get]
func (a *App) handleRoomHierarchy(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	maxDepth, err := queryInt(r, "max_depth", 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	minDepth, err := queryInt(r, "min_depth", 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.svc.RoomHierarchy(r.Context(), service.HierarchyParams{
		RoomID:   r.PathValue("roomIdOrAlias"),
		UserID:   auth.GetUserID(r),
		Limit:    limit,
		MaxDepth: maxDepth,
		MinDepth: minDepth,
		From:     queryValue(r, "from", ""),
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleMutualRooms implements GET /_matrix/client/v1/mutual_rooms. The room id
// anchors the request: the requester has to be able to read it and the target
// user has to be part of it, which stops the endpoint from being used to map
// the social graph.
//
//	@Summary	Rooms shared by two users
//	@Tags		spaces
//	@Produce	json
//	@Param		room_id	query	string	true	"A room both users are expected to be in"
//	@Param		user_id	query	string	true	"The other user"
//	@Success	200		{object}	map[string]any
//	@Router		/_matrix/client/v1/mutual_rooms [get]
func (a *App) handleMutualRooms(w http.ResponseWriter, r *http.Request) {
	requester := auth.GetUserID(r)
	target := queryValue(r, "user_id", "")
	if target == "" || target == requester {
		a.writeError(w, r, matrix.ErrInvalidParam("a user_id other than the requester is required"))
		return
	}
	if roomID := queryValue(r, "room_id", ""); roomID != "" {
		if err := a.svc.RequireReadable(r.Context(), roomID, requester); err != nil {
			a.writeError(w, r, err)
			return
		}
		if err := a.svc.RequireJoined(r.Context(), roomID, target); err != nil {
			a.writeError(w, r, err)
			return
		}
	}
	rooms, err := a.svc.MutualRooms(r.Context(), requester, target)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{"rooms": roomIDList(rooms)})
}

// handleUserDirectRooms implements GET /_matrix/client/v1/users/{userId}/rooms,
// the direct message listing a client can use instead of reading the m.direct
// account data itself.
//
//	@Summary	Direct rooms of a user
//	@Tags		spaces
//	@Produce	json
//	@Param		userId	path	string	true	"User whose direct rooms are listed"
//	@Success	200		{object}	map[string]any
//	@Router		/_matrix/client/v1/users/{userId}/rooms [get]
func (a *App) handleUserDirectRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := a.svc.DirectRooms(r.Context(), r.PathValue("userId"), auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{"rooms": roomIDList(rooms)})
}

// roomIDList flattens direct room entries into their identifiers, which is what
// both listings return.
func roomIDList(rooms []service.DirectRoom) []string {
	out := make([]string, 0, len(rooms))
	for _, room := range rooms {
		out = append(out, room.RoomID)
	}
	return out
}
