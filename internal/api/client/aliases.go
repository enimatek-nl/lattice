package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// handleAliasPut implements PUT /_matrix/client/v3/directory/room/{roomAlias}.
func (d *Deps) handleAliasPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RoomID string `json:"room_id"`
	}
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
		return
	}
	if body.RoomID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"An alias must be given the room it points at."))
		return
	}
	if err := d.Rooms.CreateAlias(r.Context(), body.RoomID, pathValue(r, "roomAlias"), auth.UserID(r)); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleAliasGet implements GET /_matrix/client/v3/directory/room/{roomAlias}.
//
// The answer names this server as a place the room can be reached, which is what the
// `servers` hint means: not "these servers claim to host it", but "this server resolved
// the alias here".
func (d *Deps) handleAliasGet(w http.ResponseWriter, r *http.Request) {
	roomID, err := d.Rooms.ResolveAlias(r.Context(), pathValue(r, "roomAlias"))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"room_id": roomID,
		"servers": []string{d.Cfg.ServerName},
	})
}

// handleAliasDelete implements DELETE /_matrix/client/v3/directory/room/{roomAlias}.
func (d *Deps) handleAliasDelete(w http.ResponseWriter, r *http.Request) {
	if err := d.Rooms.DeleteAlias(r.Context(), pathValue(r, "roomAlias"), auth.UserID(r), auth.DeviceID(r)); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleRoomAliases implements GET /_matrix/client/v3/rooms/{roomID}/aliases.
func (d *Deps) handleRoomAliases(w http.ResponseWriter, r *http.Request) {
	aliases, err := d.Rooms.RoomAliases(r.Context(), r.PathValue("roomID"), auth.UserID(r))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	if aliases == nil {
		aliases = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"aliases": aliases})
}

// resolveRoomIdentifier accepts either a room ID or a local alias in a path, which is
// what the join endpoint is specified to take.
func (d *Deps) resolveRoomIdentifier(w http.ResponseWriter, r *http.Request, identifier string) (string, bool) {
	if identifier == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A room ID or room alias is required."))
		return "", false
	}
	if identifier[0] == '#' {
		if err := matrixid.RoomAliasValid(identifier); err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not a valid room alias.", identifier))
			return "", false
		}
		_, domain, err := matrixid.SplitID(matrixid.SigilAlias, identifier)
		if err != nil || !matrixid.IsLocal(domain, d.Cfg.ServerName) {
			// Resolving another server's alias means asking that server, which is
			// federation. A 404 would claim the alias does not exist, which is a
			// different statement from "this server cannot ask".
			matrixerr.Write(w, http.StatusNotImplemented, matrixerr.New(matrixerr.CodeUnknown,
				"This server cannot resolve a room alias owned by another server yet: that requires federation."))
			return "", false
		}
		roomID, err := d.Rooms.ResolveAlias(r.Context(), identifier)
		if err != nil {
			d.serviceError(w, err)
			return "", false
		}
		return roomID, true
	}
	if err := matrixid.RoomIDValid(identifier); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is neither a valid room ID nor a room alias.", identifier))
		return "", false
	}
	return identifier, true
}
