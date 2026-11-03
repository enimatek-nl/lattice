package service

import (
	"context"
	"errors"
	"log"
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

// AccountParams are the fields of an account this homeserver stores: what a client
// supplies in a registration, and what an operator supplies to `lattice add-user`.
type AccountParams struct {
	// Username is the localpart; the user ID is it with this server's name on the end.
	Username string
	// Password is stored as a bcrypt hash and never kept in plain text.
	Password string
	// DisplayName seeds the m.displayname profile entry of the new account.
	DisplayName string
	// Admin marks the account an administrator. The first account on a fresh
	// server is one whatever this says.
	Admin bool
	// Guest marks an account created for a throwaway session.
	Guest bool
}

// CreateAccount validates a name, hashes a password and stores a new local account.
// It authenticates nobody and opens no session: it is the part of registration that
// is still there once the client exchange is taken away, which is what lets
// `lattice add-user` put an account on a server that has closed self-service
// registration with -disable-registration.
//
// [Service.Register] has already checked the name before it gets here. The checks are
// repeated rather than trusted because this is an entry point in its own right: a
// caller that never went through the authentication exchange must still not be able to
// store an account with a name this server could never hold, or one that shadows an
// account already in the file.
//
// The first account on a fresh server becomes an administrator, so that
// administrative endpoints can be reached without out-of-band setup.
func (s *Service) CreateAccount(ctx context.Context, params AccountParams) (*persist.User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	localpart := strings.ToLower(params.Username)
	if !matrix.ValidLocalpart(localpart) {
		return nil, matrix.ErrInvalidUsername("user ID @%s:%s is invalid", localpart, s.serverName)
	}
	if _, err := s.db.UserRepo.GetByLocalpart(localpart); err == nil {
		return nil, matrix.ErrUserInUse("user ID is already taken")
	} else if !persist.IsNotFound(err) {
		return nil, matrix.ErrInternal(err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(params.Password), bcrypt.DefaultCost)
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
			return nil, matrix.ErrUserInUse("user ID is already taken")
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
	return user, nil
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

	// The body is checked before the authentication challenge. An account name this
	// server could never hold is a client error no amount of authenticating fixes,
	// and a taken user ID has to be reported as such rather than answered with a
	// 401 that sends the client off to authenticate for nothing. The one request
	// that is not a registration attempt is the one with neither a username nor an
	// auth dict: that is how a client asks which flows exist, and it has to be
	// answered with the challenge.
	localpart := strings.ToLower(params.Username)
	switch {
	case localpart != "":
		if !matrix.ValidLocalpart(localpart) {
			return nil, matrix.ErrInvalidUsername("user ID @%s:%s is invalid", localpart, s.serverName)
		}
		if _, err := s.db.UserRepo.GetByLocalpart(localpart); err == nil {
			return nil, matrix.ErrUserInUse("user ID is already taken")
		} else if !persist.IsNotFound(err) {
			return nil, matrix.ErrInternal(err)
		}
	case params.Guest:
		localpart = matrix.NewUserLocalpart()
	default:
		stage := ""
		if params.Auth != nil {
			stage, _ = params.Auth["type"].(string)
		}
		if stage != "" {
			return nil, matrix.ErrInvalidUsername("a username is required")
		}
	}

	if !params.Guest {
		if err := s.authorizeRegistration(params); err != nil {
			return nil, err
		}
	}

	password := params.Password
	if params.Guest {
		password = matrix.NewToken()
	}
	user, err := s.CreateAccount(ctx, AccountParams{
		Username:    localpart,
		Password:    password,
		DisplayName: params.DisplayName,
		Admin:       params.Admin,
		Guest:       params.Guest,
	})
	if err != nil {
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
func (s *Service) Logout(ctx context.Context, userID, deviceID, accessToken string) error {
	if accessToken == "" {
		return matrix.ErrMissingToken()
	}
	if err := s.db.SessionRepo.DeleteByToken(accessToken); err != nil {
		return matrix.ErrInternal(err)
	}
	if userID == "" || deviceID == "" {
		return nil
	}
	// Signing out ends the device, not only the token it used. A device is the key
	// bundle of a session: leaving it behind means other devices keep encrypting
	// for an end-to-end session nobody can open any more, and the device list of
	// the account fills up with a row for every sign-out.
	if err := s.db.DeviceRepo.Delete(userID, deviceID); err != nil {
		return matrix.ErrInternal(err)
	}
	if err := s.db.SessionRepo.DeleteByDevice(userID, deviceID); err != nil {
		return matrix.ErrInternal(err)
	}
	return s.DropDeviceKeys(ctx, userID, deviceID)
}

// LogoutAll ends every session of an account. The specification is explicit that
// this includes the access token that made the request: signing out of
// everything is the request, and keeping the caller signed in is the one outcome
// it did not ask for. The devices go with the sessions, and their device keys
// with the devices.
func (s *Service) LogoutAll(ctx context.Context, userID string) error {
	if err := s.db.SessionRepo.DeleteByUser(userID); err != nil {
		return matrix.ErrInternal(err)
	}
	if err := s.db.DeviceRepo.DeleteAll(userID, ""); err != nil {
		return matrix.ErrInternal(err)
	}
	return s.DropDeviceKeys(ctx, userID)
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

// checkAuthFlow validates the User-Interactive Auth payload of a destructive
// administrative action such as deactivating an account or deleting a device.
// Account registration goes through authorizeRegistration instead, which is the
// one clients discover by asking for a flow list.
//
// The password is required. A request that already carries an access token is not
// proof that whoever holds the token knows the password, and these are the actions
// where that difference matters: deleting a device or an account destroys the
// evidence its owner would want to look at after an intrusion. The dummy stage is
// therefore not accepted here, whatever it means during registration - and a
// request with no auth at all is answered with the challenge, not with the action.
func (s *Service) checkAuthFlow(ctx context.Context, auth matrix.Object, username string) error {
	stage, _ := auth["type"].(string)
	if stage == "" {
		return s.adminAuthChallenge(username, "additional authentication is required")
	}
	switch stage {
	case StagePassword:
		account, password, err := s.passwordAuth(auth, username)
		if err != nil {
			var merr *matrix.Error
			if errors.As(err, &merr) && merr.HTTPStatus() == http.StatusForbidden {
				return s.adminAuthChallenge(username, "the user authentication passed in the request body is incorrect")
			}
			return err
		}
		// Authenticating as somebody else must not carry the right to act on this
		// account: deleting a device, changing a password or deactivating an account
		// are all destructive acts on one person's session, and knowing your own
		// password proves nothing about theirs.
		if matrix.LocalpartOf(account.ID) != matrix.LocalpartOf(username) {
			return matrix.ErrForbidden("the authentication in the request body belongs to another account")
		}
		if err := s.verifyPassword(account, password); err != nil {
			var merr *matrix.Error
			if errors.As(err, &merr) && merr.HTTPStatus() == http.StatusForbidden {
				return s.adminAuthChallenge(username, "the user authentication passed in the request body is incorrect")
			}
			return err
		}
		return nil
	case StageAdmin:
		// The administrator stage stands in for the password only for an account that
		// is an administrator, and only because an administrator acting on another
		// account does not have that account's password.
		account, err := s.localUser(username)
		if err == nil && account.Admin {
			return nil
		}
		return s.adminAuthChallenge(username, "only an administrator may authenticate this way")
	default:
		return s.adminAuthChallenge(username, "session does not support authentication protocol "+stage)
	}
}

// adminAuthChallenge answers a destructive action whose authentication is not
// settled: the 401 with the stages that would be accepted, the parameters they
// need and a session id, which is the shape clients stop on.
//
// Lattice checks the credentials in the request rather than the session, because
// the action is already authenticated by the access token and a session would add
// a table to expire. The session id is returned because clients read it and log it.
func (s *Service) adminAuthChallenge(username, reason string) *matrix.Error {
	log.Printf("interactive auth: %s for %s", reason, username)
	return matrix.NewError(matrix.ErrCodeForbidden, http.StatusUnauthorized, reason).WithExtra(matrix.Object{
		"flows":     []AuthFlow{{Stages: []string{StagePassword}}},
		"params":    matrix.Object{},
		"session":   matrix.NewToken(),
		"completed": []string{},
	})
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
