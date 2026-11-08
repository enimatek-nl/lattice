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
	alias := pathValue(r, "roomAlias")
	if _, domain, err := matrixid.SplitID(matrixid.SigilAlias, alias); err == nil &&
		!matrixid.IsLocal(domain, d.Cfg.ServerName) {
		// Another server's alias is that server's to answer. Asking it is what makes the difference
		// between "this alias does not exist" and "this server has never looked", and a client that
		// typed the alias into a join box cannot tell those apart either — but the person on the other
		// side of it can, and a 404 from a server that never asked is how a working room comes to look
		// like a deleted one.
		roomID, servers, err := d.resolveRemoteAlias(r, alias, domain)
		if err != nil {
			d.serviceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"room_id": roomID, "servers": servers})
		return
	}
	roomID, err := d.Rooms.ResolveAlias(r.Context(), alias)
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
func (d *Deps) resolveRoomIdentifier(w http.ResponseWriter, r *http.Request, identifier string) (string, []string, bool) {
	if identifier == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A room ID or room alias is required."))
		return "", nil, false
	}
	if identifier[0] == '#' {
		if err := matrixid.RoomAliasValid(identifier); err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not a valid room alias.", identifier))
			return "", nil, false
		}
		_, domain, err := matrixid.SplitID(matrixid.SigilAlias, identifier)
		if err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not a valid room alias.", identifier))
			return "", nil, false
		}
		if !matrixid.IsLocal(domain, d.Cfg.ServerName) {
			roomID, servers, err := d.resolveRemoteAlias(r, identifier, domain)
			if err != nil {
				d.serviceError(w, err)
				return "", nil, false
			}
			return roomID, servers, true
		}
		roomID, err := d.Rooms.ResolveAlias(r.Context(), identifier)
		if err != nil {
			d.serviceError(w, err)
			return "", nil, false
		}
		return roomID, nil, true
	}
	if err := matrixid.RoomIDValid(identifier); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is neither a valid room ID nor a room alias.", identifier))
		return "", nil, false
	}
	return identifier, nil, true
}

// resolveRemoteAlias asks another server what one of its aliases names, and returns the room along with
// the servers that could take a join of it.
//
// The alias's own server is asked first, because it is the authority on its own alias table and the
// answer it gives is current. The `via` and `server_name` hints the client sent are tried after it, and
// only after: they name servers the client *hopes* are in the room, which is a worse authority than the
// one that owns the name, and a join that goes through them is a join that starts from a server the client
// chose rather than one the room's own alias pointed at. A client that passes them because its server is
// unreachable still gets a working join, which is the whole reason the parameter exists.
func (d *Deps) resolveRemoteAlias(r *http.Request, alias, owner string) (string, []string, error) {
	if d.RemoteAliases == nil {
		// Not a refusal to implement — this server is simply not federating. The wording says so,
		// because a client told "not implemented" goes looking for a version mismatch.
		return "", nil, matrixerr.New(matrixerr.CodeUnknown,
			"This server is not federating, so it cannot ask another server what one of its room aliases names.")
	}
	attempts := []string{owner}
	for _, hint := range viaServers(r) {
		if hint != "" && hint != owner {
			attempts = append(attempts, hint)
		}
	}
	var lastErr error
	for _, destination := range attempts {
		roomID, servers, err := d.RemoteAliases.QueryDirectory(r.Context(), destination, alias)
		if err == nil {
			return roomID, servers, nil
		}
		lastErr = err
		d.Log.Warn("client: an alias query to another server did not resolve",
			"alias", alias, "destination", destination, "error", err)
	}
	if lastErr == nil {
		lastErr = matrixerr.Newf(matrixerr.CodeNotFound, "No server could resolve the room alias %q.", alias)
	}
	return "", nil, lastErr
}
