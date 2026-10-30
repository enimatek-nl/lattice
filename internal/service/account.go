package service

import (
	"context"
	"errors"
	"strings"
	"time"

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
}

// Register creates an account, a device and an access token.
//
// The first account created on a fresh homeserver becomes an administrator so
// that administrative endpoints can be reached without out-of-band setup.
func (s *Service) Register(ctx context.Context, params RegisterParams) (*SessionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkAuthFlow(ctx, params.Auth, params.Username); err != nil {
		return nil, err
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
	return s.newSession(ctx, user, params.DeviceID)
}

// Login verifies credentials and mints a session. An unknown device ID creates
// a new device, which is what clients do on first login.
func (s *Service) Login(ctx context.Context, user, password, deviceID string) (*SessionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identifier := strings.ToLower(strings.TrimSpace(user))
	if identifier == "" {
		return nil, matrix.ErrForbidden("invalid credentials")
	}
	if strings.HasPrefix(identifier, "@") {
		identifier = matrix.LocalpartOf(identifier)
	}
	stored, err := s.db.UserRepo.GetByLocalpart(identifier)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrForbidden("invalid credentials")
		}
		return nil, matrix.ErrInternal(err)
	}
	if stored.Deactivated {
		return nil, matrix.ErrForbidden("user has been deactivated")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return nil, matrix.ErrForbidden("invalid credentials")
		}
		return nil, matrix.ErrInternal(err)
	}
	return s.newSession(ctx, stored, deviceID)
}

// newSession creates the device, access token and session row for a user.
func (s *Service) newSession(ctx context.Context, user *persist.User, deviceID string) (*SessionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deviceID == "" {
		deviceID = matrix.NewDeviceID()
	}
	device, err := s.db.DeviceRepo.Get(user.ID, deviceID)
	if err != nil {
		if !persist.IsNotFound(err) {
			return nil, matrix.ErrInternal(err)
		}
		device = &persist.Device{
			DeviceID:     deviceID,
			UserID:       user.ID,
			LastActiveTS: s.NowMS(),
		}
		if err := s.db.DeviceRepo.Create(device); err != nil {
			return nil, matrix.ErrInternal(err)
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
		WellKnown:    matrix.Object{"m.homeserver": "https://" + s.serverName},
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
	return s.newSession(ctx, user, session.DeviceID)
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

// checkAuthFlow validates the optional User-Interactive Auth payload of a
// registration. Lattice supports the dummy and password flows out of the box.
func (s *Service) checkAuthFlow(ctx context.Context, auth matrix.Object, username string) error {
	if len(auth) == 0 {
		return nil
	}
	raw, ok := auth["type"].(string)
	if !ok {
		return matrix.ErrForbidden("malformed auth request")
	}
	switch raw {
	case "m.login.dummy", "m.login.registration_token", "org.matrix.lattice.admin":
		return nil
	case "m.login.password":
		identifier, _ := auth["identifier"].(matrix.Object)
		user, _ := identifier["user"].(string)
		password, _ := auth["password"].(string)
		if user == "" {
			user, _ = auth["user"].(string)
		}
		if user == "" {
			user = username
		}
		_, err := s.Login(ctx, user, password, "")
		if err != nil {
			return matrix.ErrForbidden("the user authentication passed in the request body is incorrect")
		}
		return nil
	default:
		return matrix.NewError(matrix.ErrCodeForbidden, 401,
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

// sessionTTL is the configured access token lifetime, exposed for the
// capabilities and versions endpoints.
func (s *Service) sessionTTL() time.Duration { return s.tokens.TTL() }
