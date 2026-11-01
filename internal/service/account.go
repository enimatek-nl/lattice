package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"golang.org/x/crypto/bcrypt"
)

// SessionResult describes a newly created session.
type SessionResult struct {
	UserID       string        `json:"user_id"`
	DeviceID     string        `json:"device_id"`
	AccessToken  string        `json:"access_token"`
	RefreshToken string        `json:"refresh_token,omitempty"`
	ExpiresInMS  int64         `json:"expires_in_ms,omitempty"`
	ValidUntilTS int64         `json:"valid_until_ts,omitempty"`
	HomeServer   string        `json:"home_server,omitempty"`
	WellKnown    matrix.Object `json:"well_known,omitempty"`
	NextBatch    string        `json:"next_batch,omitempty"`
}

// RegisterParams captures the fields of a register request.
type RegisterParams struct {
	Username    string
	Password    string
	DeviceID    string
	Auth        matrix.Object
	Guest       bool
	Token       string
	Admin       bool
	DisplayName string
	// DeviceDisplayName is the initial_device_display_name of the request, which
	// names the new device in the device list.
	DeviceDisplayName string
	// CallerAdmin reports whether the request already carries an administrator
	// access token, which is what the org.matrix.lattice.admin stage relies on.
	CallerAdmin bool
}

// Register creates an account, a device and an access token.
//
// Registration sits behind a User-Interactive Auth exchange: a request with no
// auth dict is answered with a 401 that lists the supported flows, which is how
// clients discover how to register. Guest accounts skip the exchange, because a
// client asking for a throwaway session has nothing to authenticate with.
//
// The first account created on a fresh homeserver becomes an administrator so
// that administrative endpoints can be reached without out-of-band setup.
func (s *Service) Register(ctx context.Context, params RegisterParams) (*SessionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !params.Guest {
		if err := s.authorizeRegistration(params); err != nil {
			return nil, err
		}
	}

	localpart := params.Username
	if localpart == "" {
		if params.Guest {
			localpart = matrix.NewUserLocalpart()
		} else {
			return nil, matrix.ErrInvalidUsername("a username is required")
		}
	}
	localpart = strings.ToLower(localpart)
	if !matrix.ValidLocalpart(localpart) {
		return nil, matrix.ErrInvalidUsername("user ID @%s:%s is invalid", localpart, s.serverName)
	}
	if _, err := s.db.UserRepo.GetByLocalpart(localpart); err == nil {
		return nil, matrix.ErrConflict(matrix.ErrCodeUserInUse, "user ID is already taken")
	} else if !persist.IsNotFound(err) {
		return nil, matrix.ErrInternal(err)
	}

	password := params.Password
	if params.Guest {
		password = matrix.NewToken()
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}

	existing, err := s.db.UserRepo.Count()
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	user := &persist.User{
		ID:           matrix.UserMxid(localpart, s.serverName),
		Localpart:    localpart,
		PasswordHash: string(hash),
		Guest:        params.Guest,
		Admin:        params.Admin || existing == 0,
	}
	if err := s.db.UserRepo.Create(user); err != nil {
		if persist.IsDuplicate(err) {
			return nil, matrix.ErrConflict(matrix.ErrCodeUserInUse, "user ID is already taken")
		}
		return nil, matrix.ErrInternal(err)
	}
	if params.DisplayName != "" {
		if err := s.db.ProfileRepo.Set(user.ID, ProfileDisplayName, params.DisplayName); err != nil {
			return nil, matrix.ErrInternal(err)
		}
	}
	if err := s.SeedPushRules(ctx, user.ID); err != nil {
		return nil, err
	}
	return s.newSession(ctx, user, params.DeviceID, params.DeviceDisplayName)
}

// Login verifies credentials and mints a session. An unknown device ID creates
// a new device, which is what clients do on first login; initialDeviceName is
// the name such a new device gets.
func (s *Service) Login(ctx context.Context, user, password, deviceID, initialDeviceName string) (*SessionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	account, err := s.localUser(user)
	if err != nil {
		return nil, err
	}
	if err := s.verifyPassword(account, password); err != nil {
		return nil, err
	}
	return s.newSession(ctx, account, deviceID, initialDeviceName)
}

// localUser resolves a login identifier — a user ID or a bare localpart — into
// a local account. Unknown and deactivated accounts are reported identically so
// the endpoint never confirms that a user exists.
func (s *Service) localUser(identifier string) (*persist.User, error) {
	localpart := strings.ToLower(strings.TrimSpace(identifier))
	if localpart == "" {
		return nil, matrix.ErrForbidden("invalid credentials")
	}
	if strings.HasPrefix(localpart, "@") {
		localpart = matrix.LocalpartOf(localpart)
	}
	account, err := s.db.UserRepo.GetByLocalpart(localpart)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrForbidden("invalid credentials")
		}
		return nil, matrix.ErrInternal(err)
	}
	if account.Deactivated {
		return nil, matrix.ErrForbidden("user has been deactivated")
	}
	return account, nil
}

// verifyPassword compares a candidate password with the stored hash.
func (s *Service) verifyPassword(account *persist.User, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return matrix.ErrForbidden("invalid credentials")
		}
		return matrix.ErrInternal(err)
	}
	return nil
}

// newSession creates the device, access token and session row for a user. The
// initial device name only applies to a device that does not exist yet.
func (s *Service) newSession(ctx context.Context, user *persist.User, deviceID, initialDeviceName string) (*SessionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deviceID == "" {
		deviceID = matrix.NewDeviceID()
	}
	// Only existence matters here: an existing device is reused as it is, a
	// missing one is created and announced to the other devices.
	_, err := s.db.DeviceRepo.Get(user.ID, deviceID)
	if err != nil && !persist.IsNotFound(err) {
		return nil, matrix.ErrInternal(err)
	}
	if persist.IsNotFound(err) {
		device := &persist.Device{
			DeviceID:     deviceID,
			UserID:       user.ID,
			DisplayName:  initialDeviceName,
			LastActiveTS: s.NowMS(),
		}
		if err := s.db.DeviceRepo.Create(device); err != nil {
			return nil, matrix.ErrInternal(err)
		}
		// Announce the new device, or the other devices would never collect
		// its end-to-end keys.
		if err := s.recordDeviceListChange(ctx, persist.DeviceListChanged, user.ID, deviceID); err != nil {
			return nil, err
		}
	}

	accessToken, err := s.tokens.Generate(user.ID, deviceID, user.Localpart, user.Admin)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	refreshToken := matrix.NewToken()
	validUntil := s.Now().Add(s.tokens.TTL())
	session := &persist.Session{
		Token:          accessToken,
		RefreshToken:   refreshToken,
		UserID:         user.ID,
		DeviceID:       deviceID,
		ValidUntilTS:   validUntil.UnixMilli(),
		NextBatchToken: s.Token(s.StreamPos()),
	}
	if err := s.db.SessionRepo.Create(session); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return &SessionResult{
		UserID:       user.ID,
		DeviceID:     deviceID,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresInMS:  s.tokens.TTL().Milliseconds(),
		ValidUntilTS: validUntil.UnixMilli(),
		HomeServer:   s.serverName,
		WellKnown:    s.HomeServerConfiguration(),
		NextBatch:    session.NextBatchToken,
	}, nil
}

// Logout revokes a single access token.
func (s *Service) Logout(ctx context.Context, accessToken string) error {
	if accessToken == "" {
		return matrix.ErrMissingToken()
	}
	if err := s.db.SessionRepo.DeleteByToken(accessToken); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// LogoutAll revokes every session of a user except the current device.
func (s *Service) LogoutAll(ctx context.Context, userID, exceptDeviceID string) error {
	if err := s.db.SessionRepo.DeleteExceptDevice(userID, exceptDeviceID); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// Refresh exchanges a refresh token for a new access token.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*SessionResult, error) {
	session, err := s.db.SessionRepo.GetByRefreshToken(refreshToken)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrUnknownToken(false)
		}
		return nil, matrix.ErrInternal(err)
	}
	user, err := s.db.UserRepo.GetByID(session.UserID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	if err := s.db.SessionRepo.DeleteByToken(session.Token); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return s.newSession(ctx, user, session.DeviceID, "")
}

// UsernameAvailable reports whether a localpart is free.
func (s *Service) UsernameAvailable(ctx context.Context, username string) (bool, error) {
	localpart := strings.ToLower(strings.TrimSpace(username))
	if localpart == "" || !matrix.ValidLocalpart(localpart) {
		return false, matrix.ErrInvalidUsername("username %q is invalid", username)
	}
	_, err := s.db.UserRepo.GetByLocalpart(localpart)
	if err == nil {
		return false, nil
	}
	if persist.IsNotFound(err) {
		return true, nil
	}
	return false, matrix.ErrInternal(err)
}

// checkAuthFlow validates the optional User-Interactive Auth payload of an
// administrative action such as changing a password or deleting a device.
// Account registration goes through authorizeRegistration instead, which is the
// one clients discover by asking for a flow list.
func (s *Service) checkAuthFlow(ctx context.Context, auth matrix.Object, username string) error {
	if len(auth) == 0 {
		return nil
	}
	raw, ok := auth["type"].(string)
	if !ok {
		return matrix.ErrForbidden("malformed auth request")
	}
	switch raw {
	case StageDummy, StageRegistrationToken, StageAdmin:
		return nil
	case StagePassword:
		if err := s.checkPasswordAuth(auth, username); err != nil {
			var merr *matrix.Error
			if errors.As(err, &merr) && merr.HTTPStatus() == http.StatusForbidden {
				return matrix.ErrForbidden("the user authentication passed in the request body is incorrect")
			}
			return err
		}
		return nil
	default:
		return matrix.NewError(matrix.ErrCodeForbidden, http.StatusUnauthorized,
			"session does not support authentication protocol "+raw)
	}
}

// Whoami returns the user ID bound to an access token.
func (s *Service) Whoami(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", matrix.ErrMissingToken()
	}
	if !s.UserExists(ctx, userID) {
		return "", matrix.ErrUnknownToken(false)
	}
	return userID, nil
}
