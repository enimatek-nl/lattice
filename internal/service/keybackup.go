package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// Server-side key backup: a client uploads its encrypted room keys so that a new
// device can read old messages. The sessions are encrypted with a key the
// homeserver never sees, and the auth_data that says whose backup it is carries
// a signature made with the user's master key, which only the clients can check.
// Lattice therefore stores both without looking inside them.

// BackupInfo describes one backup version.
type BackupInfo struct {
	Version   int64         `json:"version"`
	Algorithm string        `json:"algorithm"`
	AuthData  matrix.Object `json:"auth_data"`
}

// BackupSession is one room key as a client uploaded it.
type BackupSession struct {
	RoomID          string
	SessionID       string
	FirstKnownIndex int64
	// Data is the encrypted session blob, stored and returned as it came.
	Data matrix.RawMessage
}

// CreateKeyBackup starts a backup for a user and returns its version number.
// Versions count up from 1 per user, as the specification asks.
func (s *Service) CreateKeyBackup(ctx context.Context, userID, algorithm string, authData matrix.Object) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if algorithm == "" {
		return 0, matrix.ErrInvalidParam("the backup algorithm is required")
	}
	highest, err := s.db.KeyBackupRepo.HighestVersionNumber(userID)
	if err != nil {
		return 0, matrix.ErrInternal(err)
	}
	version := &persist.KeyBackupVersion{
		UserID:    userID,
		Version:   highest + 1,
		Algorithm: algorithm,
		AuthData:  string(matrix.Encode(authData)),
	}
	if err := s.db.KeyBackupRepo.CreateVersion(version); err != nil {
		return 0, matrix.ErrInternal(err)
	}
	return version.Version, nil
}

// UpdateKeyBackup replaces the algorithm and auth data of a backup version.
// Sessions already stored are left alone.
func (s *Service) UpdateKeyBackup(ctx context.Context, userID string, version int64, algorithm string,
	authData matrix.Object) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	row, err := s.resolveBackupVersion(ctx, userID, version)
	if err != nil {
		return err
	}
	if algorithm == "" {
		return matrix.ErrInvalidParam("the backup algorithm is required")
	}
	row.Algorithm = algorithm
	row.AuthData = string(matrix.Encode(authData))
	if err := s.db.KeyBackupRepo.UpdateVersion(row); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// KeyBackup returns one backup version. Version 0 asks for the latest one, which
// is what GET /room_keys/version without a version means.
func (s *Service) KeyBackup(ctx context.Context, userID string, version int64) (*BackupInfo, error) {
	row, err := s.resolveBackupVersion(ctx, userID, version)
	if err != nil {
		return nil, err
	}
	var authData matrix.Object
	if err := json.Unmarshal([]byte(row.AuthData), &authData); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return &BackupInfo{Version: row.Version, Algorithm: row.Algorithm, AuthData: authData}, nil
}

// DeleteKeyBackup removes a backup version and the sessions stored under it.
// Version 0 removes every backup the account has.
func (s *Service) DeleteKeyBackup(ctx context.Context, userID string, version int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if version == 0 {
		removed, err := s.db.KeyBackupRepo.DeleteAll(userID)
		if err != nil {
			return 0, matrix.ErrInternal(err)
		}
		return removed, nil
	}
	if _, err := s.resolveBackupVersion(ctx, userID, version); err != nil {
		return 0, err
	}
	removed, err := s.db.KeyBackupRepo.DeleteVersion(userID, version)
	if err != nil {
		return 0, matrix.ErrInternal(err)
	}
	return removed, nil
}

// StoreBackupSessions writes sessions under a backup version and returns how many
// were written. A client may only write to the version it created last: writing
// into a superseded backup would silently store keys nobody will ever read.
func (s *Service) StoreBackupSessions(ctx context.Context, userID string, version int64,
	sessions []BackupSession) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	row, err := s.writableBackupVersion(ctx, userID, version)
	if err != nil {
		return 0, err
	}
	rows := make([]*persist.BackupRoomKey, 0, len(sessions))
	for _, session := range sessions {
		if session.RoomID == "" || session.SessionID == "" {
			return 0, matrix.ErrInvalidParam("a backed up room key needs a room id and a session id")
		}
		if len(session.Data) == 0 {
			return 0, matrix.ErrInvalidParam("the session %s carries no key data", session.SessionID)
		}
		rows = append(rows, &persist.BackupRoomKey{
			UserID:          userID,
			Version:         row.Version,
			RoomID:          session.RoomID,
			SessionID:       session.SessionID,
			FirstKnownIndex: session.FirstKnownIndex,
			Data:            string(session.Data),
		})
	}
	stored, err := s.db.KeyBackupRepo.StoreKeys(rows)
	if err != nil {
		return 0, matrix.ErrInternal(err)
	}
	return stored, nil
}

// BackupSessions returns the stored sessions of a version. An empty roomID asks
// for every room, and a roomID without sessionID for every session of that room.
func (s *Service) BackupSessions(ctx context.Context, userID string, version int64, roomID, sessionID string,
	roomIDs []string) ([]*persist.BackupRoomKey, error) {
	row, err := s.resolveBackupVersion(ctx, userID, version)
	if err != nil {
		return nil, err
	}
	if sessionID != "" {
		key, err := s.db.KeyBackupRepo.GetKey(userID, row.Version, roomID, sessionID)
		if err != nil {
			if errIsNoBackup(err) {
				return nil, matrix.ErrNotFound("no stored room key for session %s", sessionID)
			}
			return nil, matrix.ErrInternal(err)
		}
		return []*persist.BackupRoomKey{key}, nil
	}
	if roomID != "" {
		roomIDs = []string{roomID}
	}
	keys, err := s.db.KeyBackupRepo.Keys(userID, row.Version, roomIDs)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return keys, nil
}

// DeleteBackupSessions removes sessions: one session, the sessions of one room,
// or every session of the version.
func (s *Service) DeleteBackupSessions(ctx context.Context, userID string, version int64, roomID,
	sessionID string) (int64, error) {
	row, err := s.resolveBackupVersion(ctx, userID, version)
	if err != nil {
		return 0, err
	}
	removed, err := s.db.KeyBackupRepo.DeleteKeys(userID, row.Version, roomID, sessionID)
	if err != nil {
		return 0, matrix.ErrInternal(err)
	}
	return removed, nil
}

// CountBackupKeys reports how many sessions a backup version holds. Version 0
// asks about the latest one. The version document carries this number so a client
// can tell whether a backup looks complete without downloading it.
func (s *Service) CountBackupKeys(ctx context.Context, userID string, version int64) (int64, error) {
	row, err := s.resolveBackupVersion(ctx, userID, version)
	if err != nil {
		return 0, err
	}
	count, err := s.db.KeyBackupRepo.CountKeys(userID, row.Version, "")
	if err != nil {
		return 0, matrix.ErrInternal(err)
	}
	return count, nil
}

// DropKeyBackups removes every backup of an account, which is what account
// deactivation asks for.
func (s *Service) DropKeyBackups(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.db.KeyBackupRepo.DeleteAll(userID); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// resolveBackupVersion returns the version a request means, or a 404 when the
// account has no such backup. Version 0 means the latest one.
func (s *Service) resolveBackupVersion(ctx context.Context, userID string, version int64) (*persist.KeyBackupVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var (
		row *persist.KeyBackupVersion
		err error
	)
	if version == 0 {
		row, err = s.db.KeyBackupRepo.LatestVersion(userID)
	} else {
		row, err = s.db.KeyBackupRepo.GetVersion(userID, version)
	}
	if err != nil {
		if errIsNoBackup(err) {
			if version == 0 {
				return nil, matrix.ErrNotFound("this user has no key backup")
			}
			return nil, matrix.ErrNotFound("this user has no key backup version %d", version)
		}
		return nil, matrix.ErrInternal(err)
	}
	return row, nil
}

// writableBackupVersion additionally rejects a write to a version other than the
// latest, which the specification answers with M_WRONG_KEY_VERSION.
func (s *Service) writableBackupVersion(ctx context.Context, userID string, version int64) (*persist.KeyBackupVersion, error) {
	latest, err := s.db.KeyBackupRepo.LatestVersion(userID)
	if err != nil {
		if errIsNoBackup(err) {
			return nil, matrix.ErrNotFound("this user has no key backup to write to")
		}
		return nil, matrix.ErrInternal(err)
	}
	if version != 0 && version != latest.Version {
		return nil, matrix.NewError(matrix.ErrCodeWrongKeyVersion, 400,
			"the key backup version of this request is not the current one")
	}
	return latest, nil
}

// errIsNoBackup reports whether a repository said "no such backup".
func errIsNoBackup(err error) bool {
	return errors.Is(err, persist.ErrNoBackup)
}
