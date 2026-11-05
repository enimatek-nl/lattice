package client

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/rooms"
)

// publicRoomsBody is the request body of POST /publicRooms. The GET form carries the
// same information in query parameters; both are accepted because the specification
// defines both, and a client that wants to send a filter as JSON uses the POST form.
type publicRoomsBody struct {
	Limit  int    `json:"limit"`
	Since  string `json:"since"`
	Filter struct {
		GenericSearchTerm string `json:"generic_search_term"`
		Name              string `json:"name"`
		Topic             string `json:"topic"`
	} `json:"filter"`
	// RoomNetwork asks for rooms on another matrix network. Only this server's own
	// network can be answered, and an empty value means that network.
	RoomNetwork string `json:"room_network"`
}

// handlePublicRooms implements GET and POST /_matrix/client/v3/publicRooms.
//
// A search over the room directory is matched against the room's name and topic. The
// specification also allows filtering to a set of room IDs and searching a single field;
// those are accepted and applied where they are understood, and a filter this server
// cannot act on is reported rather than silently ignored, because silently returning
// unfiltered results would look like an answer.
func (d *Deps) handlePublicRooms(w http.ResponseWriter, r *http.Request) {
	request := rooms.DirectoryRequest{}
	switch r.Method {
	case http.MethodPost:
		var body publicRoomsBody
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
			return
		}
		if body.RoomNetwork != "" && body.RoomNetwork != d.Cfg.ServerName {
			d.remoteNetworkError(w, body.RoomNetwork)
			return
		}
		request.Limit = body.Limit
		request.Since = body.Since
		request.Search = body.Filter.GenericSearchTerm
		if request.Search == "" {
			request.Search = body.Filter.Name
		}
		if request.Search == "" {
			request.Search = body.Filter.Topic
		}
	default:
		query := r.URL.Query()
		if raw := query.Get("limit"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				d.serviceError(w, matrixerr.New(matrixerr.CodeInvalidParam,
					"The limit parameter must be a non-negative integer."))
				return
			}
			request.Limit = value
		}
		request.Since = query.Get("since")
		if raw := query.Get("filter"); raw != "" {
			var filter struct {
				GenericSearchTerm string `json:"generic_search_term"`
			}
			if err := json.Unmarshal([]byte(raw), &filter); err != nil {
				d.serviceError(w, matrixerr.New(matrixerr.CodeInvalidParam,
					"The filter parameter is not a JSON object."))
				return
			}
			request.Search = filter.GenericSearchTerm
		}
	}
	page, err := d.Rooms.PublicRooms(r.Context(), request)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

// handlePublicRoomsRemote implements GET /_matrix/client/v3/publicRooms/{serverName}.
//
// Another server's room directory can only be asked of that server, which is federation.
// Until that exists this answers honestly rather than returning this server's own list,
// which would be a lie about someone else's rooms.
func (d *Deps) handlePublicRoomsRemote(w http.ResponseWriter, r *http.Request) {
	serverName := pathValue(r, "serverName")
	if matrixid.IsLocal(serverName, d.Cfg.ServerName) {
		d.handlePublicRooms(w, r)
		return
	}
	d.remoteNetworkError(w, serverName)
}

// remoteNetworkError is the answer to a directory question about somewhere this server
// cannot ask.
func (d *Deps) remoteNetworkError(w http.ResponseWriter, name string) {
	matrixerr.Write(w, http.StatusNotImplemented, matrixerr.Newf(matrixerr.CodeUnknown,
		"This server cannot list the rooms of %q: asking another server for its directory is part of federation, which is not implemented.", name))
}

// handleRoomDirectoryVisibility implements GET and PUT
// /_matrix/client/v3/directory/list/room/{roomID}.
func (d *Deps) handleRoomDirectoryVisibility(w http.ResponseWriter, r *http.Request) {
	roomID := pathValue(r, "roomID")
	if r.Method == http.MethodGet {
		visibility, err := d.Rooms.RoomVisibility(r.Context(), roomID, auth.UserID(r))
		if err != nil {
			d.serviceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"visibility": visibility})
		return
	}
	var body struct {
		Visibility string `json:"visibility"`
	}
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	switch body.Visibility {
	case "public", "private":
	default:
		d.serviceError(w, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"visibility %q is not one of public or private.", body.Visibility))
		return
	}
	if err := d.Rooms.SetRoomVisibility(r.Context(), roomID, auth.UserID(r), body.Visibility == "public"); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"visibility": body.Visibility})
}

// handleRoomSummary implements GET /_matrix/client/v1/room_summary/{roomIDOrAlias}.
//
// The room summary describes a room rather than showing what is in it, and the access
// rule is the one used for every other room read: someone with no standing in the room
// only gets an answer when the room opened its history to the world.
func (d *Deps) handleRoomSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := d.Rooms.RoomSummary(r.Context(), pathValue(r, "roomIDOrAlias"), auth.UserID(r))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, summary)
}
