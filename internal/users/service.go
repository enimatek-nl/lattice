// Package users implements local account management: registration policy,
// password hashing and the account lookups the API layer needs.
package users

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// Service manages local accounts.
type Service struct {
	db     *persist.Database
	tokens *auth.TokenService
	cfg    config.RegistrationConfig
	log    *slog.Logger
	server string
}

// New builds the account service.
func New(db *persist.Database, tokens *auth.TokenService, cfg config.RegistrationConfig, serverName string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{db: db, tokens: tokens, cfg: cfg, log: log, server: serverName}
}

// ServerName returns the homeserver name this service allocates IDs under.
func (s *Service) ServerName() string { return s.server }

// RegisterParams describes an account creation request.
type RegisterParams struct {
	Username                 string
	Password                 string
	DeviceID                 string
	InitialDeviceDisplayName string
	Admin                    bool
	InhibitLogin             bool
}

// Registration is the result of a successful registration.
type Registration struct {
	UserID      string
	DeviceID    string
	AccessToken string
	Admin       bool
}

// Register creates an account, a device and an access token atomically: if any
// step fails no partial state is left behind (REQ-DB-003).
func (s *Service) Register(ctx context.Context, p RegisterParams) (*Registration, error) {
	localpart, err := s.localpartFor(p.Username)
	if err != nil {
		return nil, err
	}
	if err := s.checkPasswordPolicy(p.Password); err != nil {
		return nil, err
	}
	passwordHash := ""
	if p.Password != "" {
		hash, herr := bcrypt.GenerateFromPassword([]byte(p.Password), s.cfg.BcryptCost)
		if herr != nil {
			return nil, fmt.Errorf("users: cannot hash password: %w", herr)
		}
		passwordHash = string(hash)
	}
	userID := "@" + localpart + ":" + s.server

	var deviceID string
	var accessToken string
	err = s.db.Tx(ctx, func(tx *gorm.DB) error {
		user := &persist.User{
			UserID:       userID,
			Localpart:    localpart,
			PasswordHash: passwordHash,
			Admin:        p.Admin,
		}
		if err := s.db.Users.WithTx(tx).Create(ctx, user); err != nil {
			if errors.Is(err, persist.ErrConflict) {
				return matrixerr.New(matrixerr.CodeUserInUse, "That user ID is already taken.")
			}
			return err
		}

		deviceID, err = s.allocateDeviceID(ctx, s.db.Devices.WithTx(tx), userID, p.DeviceID)
		if err != nil {
			return err
		}
		device := &persist.Device{
			UserID:      userID,
			DeviceID:    deviceID,
			DisplayName: p.InitialDeviceDisplayName,
			LastSeenTS:  ptr(time.Now().UTC()),
		}
		if err := s.db.Devices.WithTx(tx).Create(ctx, device); err != nil {
			if errors.Is(err, persist.ErrConflict) {
				return matrixerr.Newf(matrixerr.CodeInvalidParam, "Device ID %q is already in use.", p.DeviceID)
			}
			return err
		}
		if !p.InhibitLogin {
			accessToken, err = s.tokens.Mint(ctx, tx, userID, deviceID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.log.Info("account registered", "user_id", userID, "device_id", deviceID, "admin", p.Admin)
	return &Registration{UserID: userID, DeviceID: deviceID, AccessToken: accessToken, Admin: p.Admin}, nil
}

// localpartFor maps a requested username to a valid localpart, generating one
// when the caller did not supply a username.
func (s *Service) localpartFor(username string) (string, error) {
	if strings.TrimSpace(username) == "" {
		generated, err := matrixid.GenerateRandomString(nil, 8)
		if err != nil {
			return "", fmt.Errorf("users: cannot generate localpart: %w", err)
		}
		return "u" + strings.ToLower(strings.ReplaceAll(generated, "-", "_")), nil
	}
	// The specification requires homeservers to downcase usernames.
	localpart := matrixid.UsernameToLocalpart(strings.TrimSpace(username))
	if verr := matrixid.ValidateRegistrationLocalpart(localpart); verr != nil {
		return "", matrixerr.New(matrixerr.CodeInvalidUsername, verr.Error())
	}
	return localpart, nil
}

// allocateDeviceID validates a requested device ID or generates a free one.
func (s *Service) allocateDeviceID(ctx context.Context, devices persist.DeviceRepo, userID, requested string) (string, error) {
	if requested != "" {
		if verr := matrixid.DeviceIDValid(requested); verr != nil {
			return "", matrixerr.New(matrixerr.CodeInvalidParam, verr.Error())
		}
		return requested, nil
	}
	for attempt := 0; attempt < 8; attempt++ {
		candidate, err := matrixid.GenerateRandomString(nil, 8)
		if err != nil {
			return "", fmt.Errorf("users: cannot generate device ID: %w", err)
		}
		if _, err := devices.ByDeviceID(ctx, userID, candidate); errors.Is(err, persist.ErrNotFound) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", matrixerr.New(matrixerr.CodeUnknown, "Cannot allocate a device ID.")
}

// Get returns a local account by full user ID.
func (s *Service) Get(ctx context.Context, userID string) (*persist.User, error) {
	user, err := s.db.Users.ByUserID(ctx, userID)
	if errors.Is(err, persist.ErrNotFound) {
		return nil, matrixerr.New(matrixerr.CodeNotFound, "User not found.")
	}
	return user, err
}

// IsActive implements [auth.AccountChecker].
func (s *Service) IsActive(ctx context.Context, userID string) (bool, error) {
	user, err := s.db.Users.ByUserID(ctx, userID)
	if errors.Is(err, persist.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !user.Deactivated, nil
}

// FindForLogin resolves a login identifier, which may be a full user ID or a
// bare localpart. Unknown users are reported as ErrNotFound so callers can avoid
// user enumeration.
func (s *Service) FindForLogin(ctx context.Context, identifier string) (*persist.User, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, ErrUnknownUser
	}
	if strings.HasPrefix(identifier, string(matrixid.SigilUser)) {
		user, err := s.db.Users.ByUserID(ctx, strings.ToLower(identifier))
		if errors.Is(err, persist.ErrNotFound) {
			return nil, ErrUnknownUser
		}
		return user, err
	}
	if strings.Contains(identifier, ":") {
		return nil, ErrUnknownUser
	}
	localpart := matrixid.UsernameToLocalpart(identifier)
	user, err := s.db.Users.ByLocalpart(ctx, localpart)
	if errors.Is(err, persist.ErrNotFound) {
		return nil, ErrUnknownUser
	}
	return user, err
}

// CheckPassword verifies a password against a stored bcrypt hash.
func CheckPassword(user *persist.User, password string) bool {
	if user.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
}

// ErrUnknownUser is returned when a login identifier does not resolve locally.
var ErrUnknownUser = errors.New("users: unknown user")

func ptr[T any](v T) *T { return &v }

// LocalpartOf maps a username to the localpart it would allocate, applying the
// downcasing rule the specification requires for user IDs.
func (s *Service) LocalpartOf(username string) string {
	return matrixid.UsernameToLocalpart(strings.TrimSpace(username))
}

// ValidateLocalpart checks a localpart against the identifier grammar and local
// registration policy, returning M_INVALID_USERNAME on failure.
func (s *Service) ValidateLocalpart(localpart string) error {
	if err := matrixid.ValidateRegistrationLocalpart(localpart); err != nil {
		return matrixerr.New(matrixerr.CodeInvalidUsername, err.Error())
	}
	return nil
}

// Session is a newly created client session.
type Session struct {
	UserID      string
	DeviceID    string
	AccessToken string
}

// NewSession creates a device and access token for an existing account. The
// device row and the token digest are written in one transaction.
func (s *Service) NewSession(ctx context.Context, userID, deviceID, displayName string) (*Session, error) {
	user, err := s.db.Users.ByUserID(ctx, userID)
	if errors.Is(err, persist.ErrNotFound) {
		return nil, ErrUnknownUser
	}
	if err != nil {
		return nil, err
	}
	if user.Deactivated {
		return nil, matrixerr.New(matrixerr.CodeUserDeactivated, "This account has been deactivated.")
	}

	var session Session
	err = s.db.Tx(ctx, func(tx *gorm.DB) error {
		allocated, aerr := s.allocateDeviceID(ctx, s.db.Devices.WithTx(tx), user.UserID, deviceID)
		if aerr != nil {
			return aerr
		}
		device := &persist.Device{
			UserID:      user.UserID,
			DeviceID:    allocated,
			DisplayName: displayName,
			LastSeenTS:  ptr(time.Now().UTC()),
		}
		if cerr := s.db.Devices.WithTx(tx).Create(ctx, device); cerr != nil {
			if errors.Is(cerr, persist.ErrConflict) {
				return matrixerr.Newf(matrixerr.CodeInvalidParam, "Device ID %q is already in use.", deviceID)
			}
			return cerr
		}
		token, terr := s.tokens.Mint(ctx, tx, user.UserID, allocated)
		if terr != nil {
			return terr
		}
		session = Session{UserID: user.UserID, DeviceID: allocated, AccessToken: token}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// CheckPasswordPolicy applies the configured password policy to a plaintext
// password without touching the database, so API handlers can reject weak input
// before starting a User-Interactive Auth exchange.
func (s *Service) CheckPasswordPolicy(password string) error {
	return s.checkPasswordPolicy(password)
}

// checkPasswordPolicy is the single place the password policy is enforced.
func (s *Service) checkPasswordPolicy(password string) error {
	if password == "" {
		// The specification makes a password optional: an account may be created
		// for application-service or token-based login.
		return nil
	}
	if len(password) < s.cfg.MinPasswordLength {
		return matrixerr.Newf(matrixerr.CodeWeakPassword,
			"Password must be at least %d characters.", s.cfg.MinPasswordLength)
	}
	return nil
}
