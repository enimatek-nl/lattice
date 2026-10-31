package api

import (
	"net/http"
	"strconv"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

// Server-side room key backup. The stored sessions and the auth_data of a backup
// are encrypted with keys Lattice never sees, so everything here is stored and
// returned exactly as it arrived; the clients decide what it means and whether it
// belongs to them.

// handleKeyBackupVersion implements GET /_matrix/client/v3/room_keys/version and
// the deprecated GET /_matrix/client/v3/room_keys/version/{version}.
//
//	@Summary	Get the latest key backup version
//	@Tags		e2ee
//	@Produce	json
//	@Success	200	{object}	service.BackupInfo
//	@Router		/_matrix/client/v3/room_keys/version [get]
func (a *App) handleKeyBackupVersion(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	info, err := a.svc.KeyBackup(r.Context(), auth.GetUserID(r), version)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{
		"version":   info.Version,
		"algorithm": info.Algorithm,
		"auth_data": info.AuthData,
		"count":     a.backupKeyCount(r, info.Version),
		"etag":      strconv.FormatInt(info.Version, 10),
	})
}

// handleCreateKeyBackup implements POST /_matrix/client/v3/room_keys/version.
//
//	@Summary	Create a new key backup version
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]int64
//	@Router		/_matrix/client/v3/room_keys/version [post]
func (a *App) handleCreateKeyBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Algorithm string        `json:"algorithm"`
		AuthData  matrix.Object `json:"auth_data"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	version, err := a.svc.CreateKeyBackup(r.Context(), auth.GetUserID(r), body.Algorithm, body.AuthData)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"version": version})
}

// handleUpdateKeyBackup implements PUT /_matrix/client/v3/room_keys/version/{version},
// the deprecated form of putting a new version.
//
//	@Summary	Update a key backup version
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/version/{version} [put]
func (a *App) handleUpdateKeyBackup(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	var body struct {
		Algorithm string        `json:"algorithm"`
		AuthData  matrix.Object `json:"auth_data"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.UpdateKeyBackup(r.Context(), auth.GetUserID(r), version, body.Algorithm, body.AuthData); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{})
}

// handleDeleteKeyBackup implements DELETE /_matrix/client/v3/room_keys/version/{version}.
//
//	@Summary	Delete a key backup version
//	@Tags		e2ee
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/version/{version} [delete]
func (a *App) handleDeleteKeyBackup(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if _, err := a.svc.DeleteKeyBackup(r.Context(), auth.GetUserID(r), version); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{})
}

// handleGetBackupKeys implements GET /_matrix/client/v3/room_keys/keys.
//
//	@Summary	Get all backed up room keys
//	@Tags		e2ee
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/keys [get]
func (a *App) handleGetBackupKeys(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	keys, err := a.svc.BackupSessions(r.Context(), auth.GetUserID(r), version, "", "",
		r.URL.Query()["rooms"])
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, backupRoomsResponse(keys))
}

// handleStoreBackupKeys implements PUT /_matrix/client/v3/room_keys/keys.
//
//	@Summary	Back up the room keys of every room
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/keys [put]
func (a *App) handleStoreBackupKeys(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	var body struct {
		Rooms map[string]backupRoomBody `json:"rooms"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	var sessions []service.BackupSession
	for roomID, room := range body.Rooms {
		sessions = append(sessions, room.sessions(roomID)...)
	}
	stored, err := a.svc.StoreBackupSessions(r.Context(), auth.GetUserID(r), version, sessions)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"key_count": stored})
}

// handleDeleteBackupKeys implements DELETE /_matrix/client/v3/room_keys/keys.
//
//	@Summary	Delete every backed up room key of a version
//	@Tags		e2ee
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/keys [delete]
func (a *App) handleDeleteBackupKeys(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if _, err := a.svc.DeleteBackupSessions(r.Context(), auth.GetUserID(r), version, "", ""); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{})
}

// handleGetBackupRoom implements GET /_matrix/client/v3/room_keys/keys/{roomId}.
//
//	@Summary	Get the backed up room keys of one room
//	@Tags		e2ee
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/keys/{roomId} [get]
func (a *App) handleGetBackupRoom(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	roomID := r.PathValue("roomId")
	keys, err := a.svc.BackupSessions(r.Context(), auth.GetUserID(r), version, roomID, "", nil)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if len(keys) == 0 {
		a.writeError(w, r, matrix.ErrNotFound("no backed up room keys for %s", roomID))
		return
	}
	writeJSON(w, http.StatusOK, backupRoomResponse(keys))
}

// handleStoreBackupRoom implements PUT /_matrix/client/v3/room_keys/keys/{roomId}.
//
//	@Summary	Back up the room keys of one room
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/keys/{roomId} [put]
func (a *App) handleStoreBackupRoom(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	roomID := r.PathValue("roomId")
	var body backupRoomBody
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	stored, err := a.svc.StoreBackupSessions(r.Context(), auth.GetUserID(r), version, body.sessions(roomID))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"key_count": stored})
}

// handleDeleteBackupRoom implements DELETE /_matrix/client/v3/room_keys/keys/{roomId}.
//
//	@Summary	Delete the backed up room keys of one room
//	@Tags		e2ee
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/keys/{roomId} [delete]
func (a *App) handleDeleteBackupRoom(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if _, err := a.svc.DeleteBackupSessions(r.Context(), auth.GetUserID(r), version,
		r.PathValue("roomId"), ""); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{})
}

// handleGetBackupSession implements
// GET /_matrix/client/v3/room_keys/keys/{roomId}/{sessionId}.
//
//	@Summary	Get one backed up room key
//	@Tags		e2ee
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/keys/{roomId}/{sessionId} [get]
func (a *App) handleGetBackupSession(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	roomID, sessionID := r.PathValue("roomId"), r.PathValue("sessionId")
	keys, err := a.svc.BackupSessions(r.Context(), auth.GetUserID(r), version, roomID, sessionID, nil)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if len(keys) == 0 {
		a.writeError(w, r, matrix.ErrNotFound("no backed up room key for session %s", sessionID))
		return
	}
	writeJSON(w, http.StatusOK, backupSessionResponse(keys[0]))
}

// handleStoreBackupSession implements
// PUT /_matrix/client/v3/room_keys/keys/{roomId}/{sessionId}.
//
//	@Summary	Back up one room key
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/keys/{roomId}/{sessionId} [put]
func (a *App) handleStoreBackupSession(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	var body backupSessionBody
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	index := int64(0)
	if body.FirstKnownIndex != nil {
		index = *body.FirstKnownIndex
	}
	_, err = a.svc.StoreBackupSessions(r.Context(), auth.GetUserID(r), version, []service.BackupSession{{
		RoomID:          r.PathValue("roomId"),
		SessionID:       r.PathValue("sessionId"),
		FirstKnownIndex: index,
		Data:            body.SessionData,
	}})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{})
}

// handleDeleteBackupSession implements
// DELETE /_matrix/client/v3/room_keys/keys/{roomId}/{sessionId}.
//
//	@Summary	Delete one backed up room key
//	@Tags		e2ee
//	@Produce	json
//	@Router		/_matrix/client/v3/room_keys/keys/{roomId}/{sessionId} [delete]
func (a *App) handleDeleteBackupSession(w http.ResponseWriter, r *http.Request) {
	version, err := backupVersionParam(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if _, err := a.svc.DeleteBackupSessions(r.Context(), auth.GetUserID(r), version,
		r.PathValue("roomId"), r.PathValue("sessionId")); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{})
}

// backupRoomBody is the per-room part of an upload.
type backupRoomBody struct {
	FirstKnownIndex *int64                       `json:"first_known_index"`
	Sessions        map[string]backupSessionBody `json:"sessions"`
}

// sessions flattens the room's sessions, carrying the room level index down to
// the sessions that do not state one of their own.
func (b backupRoomBody) sessions(roomID string) []service.BackupSession {
	out := make([]service.BackupSession, 0, len(b.Sessions))
	for sessionID, session := range b.Sessions {
		index := b.FirstKnownIndex
		if session.FirstKnownIndex != nil {
			index = session.FirstKnownIndex
		}
		known := int64(0)
		if index != nil {
			known = *index
		}
		out = append(out, service.BackupSession{
			RoomID:          roomID,
			SessionID:       sessionID,
			FirstKnownIndex: known,
			Data:            session.SessionData,
		})
	}
	return out
}

// backupSessionBody is one session as a client uploads it. The session data is
// kept as raw JSON so that what comes back is what was sent.
type backupSessionBody struct {
	FirstKnownIndex *int64            `json:"first_known_index"`
	SessionData     matrix.RawMessage `json:"session_data"`
}

// backupVersionParam reads the version a key backup request means: the version
// query parameter, or the path segment of the deprecated endpoint forms. The
// zero value asks for the latest version.
func backupVersionParam(r *http.Request) (int64, error) {
	value := r.URL.Query().Get("version")
	if value == "" {
		value = r.PathValue("version")
	}
	if value == "" {
		return 0, nil
	}
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil || version < 1 {
		return 0, matrix.ErrInvalidParam("the key backup version %q is not a positive integer", value)
	}
	return version, nil
}

// backupKeyCount reports how many sessions a backup holds, which the version
// document carries so a client can tell whether the backup is complete without
// downloading it.
func (a *App) backupKeyCount(r *http.Request, version int64) int64 {
	count, err := a.svc.CountBackupKeys(r.Context(), auth.GetUserID(r), version)
	if err != nil {
		return 0
	}
	return count
}

// backupRoomsResponse renders sessions grouped by room, the shape of the
// whole-account read.
func backupRoomsResponse(keys []*persist.BackupRoomKey) matrix.Object {
	byRoom := map[string][]*persist.BackupRoomKey{}
	for _, key := range keys {
		byRoom[key.RoomID] = append(byRoom[key.RoomID], key)
	}
	rooms := matrix.Object{}
	for roomID, rows := range byRoom {
		rooms[roomID] = matrix.Object{
			"first_known_index": firstKnownIndex(rows),
			"sessions":          backupRoomSessions(rows),
		}
	}
	return matrix.Object{"rooms": rooms}
}

// backupRoomResponse renders the sessions of a single room.
func backupRoomResponse(keys []*persist.BackupRoomKey) matrix.Object {
	return matrix.Object{
		"first_known_index": firstKnownIndex(keys),
		"sessions":          backupRoomSessions(keys),
	}
}

// backupSessionResponse renders one session.
func backupSessionResponse(key *persist.BackupRoomKey) matrix.Object {
	return matrix.Object{
		"first_known_index": key.FirstKnownIndex,
		"session_data":      matrix.RawMessage(key.Data),
	}
}

func backupRoomSessions(keys []*persist.BackupRoomKey) matrix.Object {
	sessions := matrix.Object{}
	for _, key := range keys {
		sessions[key.SessionID] = matrix.Object{
			"first_known_index": key.FirstKnownIndex,
			"session_data":      matrix.RawMessage(key.Data),
		}
	}
	return sessions
}

// firstKnownIndex returns the lowest index of a set of sessions: the oldest
// message they can decrypt between them.
func firstKnownIndex(keys []*persist.BackupRoomKey) int64 {
	var lowest int64
	for i, key := range keys {
		if i == 0 || key.FirstKnownIndex < lowest {
			lowest = key.FirstKnownIndex
		}
	}
	return lowest
}
