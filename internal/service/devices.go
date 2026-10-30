package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"golang.org/x/crypto/bcrypt"
)

// Device describes a client device of a user.
type Device struct {
	DeviceID     string `json:"device_id"`
	DisplayName  string `json:"display_name,omitempty"`
	LastActiveTS int64  `json:"last_active_ts"`
	LastActiveIP string `json:"last_active_ip,omitempty"`
}

// ListDevices returns the devices of a user.
func (s *Service) ListDevices(ctx context.Context, userID string) ([]Device, error) {
	devices, err := s.db.DeviceRepo.List(userID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	out := make([]Device, 0, len(devices))
	for _, device := range devices {
		out = append(out, Device{
			DeviceID:     device.DeviceID,
			DisplayName:  device.DisplayName,
			LastActiveTS: device.LastActiveTS,
			LastActiveIP: device.LastActiveIP,
		})
	}
	return out, nil
}

// DeviceByID returns a single device.
func (s *Service) DeviceByID(ctx context.Context, userID, deviceID string) (*Device, error) {
	device, err := s.db.DeviceRepo.Get(userID, deviceID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no such device %s", deviceID)
		}
		return nil, matrix.ErrInternal(err)
	}
	return &Device{
		DeviceID:     device.DeviceID,
		DisplayName:  device.DisplayName,
		LastActiveTS: device.LastActiveTS,
		LastActiveIP: device.LastActiveIP,
	}, nil
}

// UpdateDevice renames a device.
func (s *Service) UpdateDevice(ctx context.Context, userID, deviceID, displayName string) error {
	device, err := s.db.DeviceRepo.Get(userID, deviceID)
	if err != nil {
		if persist.IsNotFound(err) {
			return matrix.ErrNotFound("no such device %s", deviceID)
		}
		return matrix.ErrInternal(err)
	}
	device.DisplayName = displayName
	if err := s.db.DeviceRepo.Save(device); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// DeleteDevices removes devices and their sessions. Unknown device IDs are
// reported in the result so clients can reconcile their state.
func (s *Service) DeleteDevices(ctx context.Context, userID string, deviceIDs []string, auth matrix.Object) (map[string]matrix.Object, error) {
	if err := s.checkAuthFlow(ctx, auth, matrix.LocalpartOf(userID)); err != nil {
		return nil, err
	}
	result := map[string]matrix.Object{}
	for _, deviceID := range deviceIDs {
		if deviceID == "" {
			continue
		}
		if _, err := s.db.DeviceRepo.Get(userID, deviceID); err != nil {
			if persist.IsNotFound(err) {
				result[deviceID] = matrix.Object{
					"errcode": matrix.ErrCodeNotFound,
					"error":   "No device with that ID",
				}
				continue
			}
			return nil, matrix.ErrInternal(err)
		}
		if err := s.db.DeviceRepo.Delete(userID, deviceID); err != nil {
			return nil, matrix.ErrInternal(err)
		}
		if err := s.db.SessionRepo.DeleteByDevice(userID, deviceID); err != nil {
			return nil, matrix.ErrInternal(err)
		}
	}
	return result, nil
}

// DeleteDevice removes a single device and its sessions.
func (s *Service) DeleteDevice(ctx context.Context, userID, deviceID string) error {
	if err := s.db.DeviceRepo.Delete(userID, deviceID); err != nil {
		return matrix.ErrInternal(err)
	}
	if err := s.db.SessionRepo.DeleteByDevice(userID, deviceID); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// ChangePassword replaces the password of an account and revokes its sessions.
func (s *Service) ChangePassword(ctx context.Context, userID, oldPassword, newPassword string, auth matrix.Object) error {
	user, err := s.db.UserRepo.GetByID(userID)
	if err != nil {
		return matrix.ErrUnknownToken(false)
	}
	if oldPassword != "" {
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPassword)); err != nil {
			return matrix.ErrForbidden("the old password was incorrect")
		}
	} else if len(auth) > 0 {
		if err := s.checkAuthFlow(ctx, auth, user.Localpart); err != nil {
			return err
		}
	} else {
		return matrix.ErrForbidden("the old password was incorrect")
	}
	if newPassword == "" {
		return matrix.NewError(matrix.ErrCodeWeakPassword, 400, "password is too short")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	user.PasswordHash = string(hash)
	if err := s.db.UserRepo.Save(user); err != nil {
		return matrix.ErrInternal(err)
	}
	if err := s.db.SessionRepo.DeleteByUser(userID); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// DeactivateAccount disables an account and revokes all of its sessions.
func (s *Service) DeactivateAccount(ctx context.Context, userID string, auth matrix.Object) (matrix.Object, error) {
	if err := s.checkAuthFlow(ctx, auth, matrix.LocalpartOf(userID)); err != nil {
		return nil, err
	}
	if !s.LocalUser(userID) {
		return nil, matrix.ErrNotFound("no such user %s", userID)
	}
	if err := s.db.UserRepo.Deactivate(userID); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	if err := s.db.SessionRepo.DeleteByUser(userID); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	rooms, err := s.db.MembershipRepo.RoomsFor(userID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	for _, roomID := range rooms {
		if err := s.db.MembershipRepo.Forget(roomID, userID); err != nil {
			return nil, matrix.ErrInternal(err)
		}
	}
	return matrix.Object{
		"id_server":               nil,
		"3pids_disconnected_json": "[]",
		"unbind_credentials":      matrix.Object{"id_server": nil, "id_access_token": nil},
	}, nil
}

// SetAccountFlag locks or suspends an account. flag is "lock" or "suspend".
func (s *Service) SetAccountFlag(ctx context.Context, targetUserID, flag string, enabled, skipAuth bool, auth matrix.Object) (bool, error) {
	if !s.LocalUser(targetUserID) {
		return false, matrix.ErrNotFound("no such user %s", targetUserID)
	}
	if !skipAuth {
		if err := s.checkAuthFlow(ctx, auth, matrix.LocalpartOf(targetUserID)); err != nil {
			return false, err
		}
	}
	user, err := s.db.UserRepo.GetByID(targetUserID)
	if err != nil {
		if persist.IsNotFound(err) {
			return false, matrix.ErrNotFound("no such user %s", targetUserID)
		}
		return false, matrix.ErrInternal(err)
	}
	switch flag {
	case "lock":
		user.Locked = enabled
	case "suspend":
		user.Suspended = enabled
		if enabled {
			if err := s.db.SessionRepo.DeleteByUser(targetUserID); err != nil {
				return false, matrix.ErrInternal(err)
			}
		}
	default:
		return false, matrix.ErrInvalidParam("unknown account flag %q", flag)
	}
	if err := s.db.UserRepo.Save(user); err != nil {
		return false, matrix.ErrInternal(err)
	}
	return enabled, nil
}

// AccountFlags reports the administrative flags of an account.
func (s *Service) AccountFlags(ctx context.Context, targetUserID, flag string) (bool, error) {
	user, err := s.db.UserRepo.GetByID(targetUserID)
	if err != nil {
		if persist.IsNotFound(err) {
			return false, matrix.ErrNotFound("no such user %s", targetUserID)
		}
		return false, matrix.ErrInternal(err)
	}
	switch flag {
	case "lock":
		return user.Locked, nil
	case "suspend":
		return user.Suspended, nil
	default:
		return false, matrix.ErrInvalidParam("unknown account flag %q", flag)
	}
}

// OpenIDToken mints a token that a third party can exchange for the user's
// identity through federation.
func (s *Service) OpenIDToken(ctx context.Context, userID string) (matrix.Object, error) {
	token := matrix.NewToken()
	expiresAt := s.Now().Add(24 * time.Hour)
	key := "openid." + token
	if err := s.db.MetaRepo.Set(key, userID+"|"+FormatPos(uint64(expiresAt.Unix()))); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return matrix.Object{
		"access_token":       token,
		"expires_in":         int64(24 * time.Hour / time.Second),
		"matrix_server_name": s.serverName,
		"token_type":         "Bearer",
	}, nil
}

// OpenIDUser resolves an OpenID token into a user ID.
func (s *Service) OpenIDUser(ctx context.Context, token string) (string, error) {
	value, err := s.db.MetaRepo.Get("openid." + token)
	if err != nil {
		if persist.IsNotFound(err) {
			return "", matrix.ErrNotFound("unknown OpenID token")
		}
		return "", matrix.ErrInternal(err)
	}
	userID, expiry, found := strings.Cut(value, "|")
	if !found {
		return "", matrix.ErrNotFound("unknown OpenID token")
	}
	if expirySeconds, err := strconv.ParseInt(expiry, 10, 64); err != nil ||
		expirySeconds < s.Now().Unix() {
		return "", matrix.ErrNotFound("OpenID token has expired")
	}
	return userID, nil
}

// WhoIs returns the sessions and devices of a user, for administrators.
func (s *Service) WhoIs(ctx context.Context, targetUserID string) (matrix.Object, error) {
	if !s.LocalUser(targetUserID) {
		return nil, matrix.ErrNotFound("user %s not found", targetUserID)
	}
	devices, err := s.db.DeviceRepo.List(targetUserID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	sessions := matrix.Object{}
	for _, device := range devices {
		sessions[device.DeviceID] = matrix.Object{
			"device_id":      device.DeviceID,
			"display_name":   device.DisplayName,
			"last_seen_ts":   device.LastActiveTS,
			"last_seen_ip":   device.LastActiveIP,
			"conn_last_seen": device.LastActiveTS,
		}
	}
	return matrix.Object{"devices": sessions}, nil
}
