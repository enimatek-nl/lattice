package api

import (
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// loginBody is the request body of POST /_matrix/client/v3/login.
type loginBody struct {
	Type       string        `json:"type"`
	Identifier loginIDField  `json:"identifier"`
	User       string        `json:"user"`
	Address    string        `json:"address"`
	Medium     string        `json:"medium"`
	Password   string        `json:"password"`
	DeviceID   string        `json:"device_id"`
	DeviceName string        `json:"initial_device_display_name"`
	Token      string        `json:"token"`
	Refresh    bool          `json:"refresh_token"`
	ExpiresIn  int64         `json:"expires_in_ms"`
	Auth       matrix.Object `json:"auth,omitempty"`
}

// loginIDField is the identifier object of a login request.
type loginIDField struct {
	Type    string `json:"type"`
	User    string `json:"user"`
	Address string `json:"address"`
	Medium  string `json:"medium"`
}

// userID picks the user out of the identifier object or the top level fields.
func (b loginBody) userID() string {
	if b.Identifier.User != "" {
		return b.Identifier.User
	}
	if b.User != "" {
		return b.User
	}
	if b.Identifier.Address != "" {
		return b.Identifier.Address
	}
	return b.Address
}

// handleRegister implements POST /_matrix/client/v3/register.
//
//	@Summary	Register a new user account
//	@Tags		account
//	@Accept		json
//	@Produce	json
//	@Param		body	body		service.RegisterParams	true	"Registration request"
//	@Success	200		{object}	service.SessionResult
//	@Router		/_matrix/client/v3/register [post]
func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string        `json:"username"`
		Password    string        `json:"password"`
		DeviceID    string        `json:"device_id"`
		Kind        string        `json:"kind"`
		Refresh     bool          `json:"refresh_token"`
		Inhibit     bool          `json:"inhibit_login"`
		Admin       bool          `json:"admin"`
		DisplayName string        `json:"displayname"`
		Token       string        `json:"token"`
		DeviceName  string        `json:"initial_device_display_name"`
		Stage       string        `json:"type"`
		Session     string        `json:"session"`
		Auth        matrix.Object `json:"auth,omitempty"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	guest := body.Kind == "guest"
	if guest {
		if !a.cfg.AllowGuests {
			a.writeError(w, r, matrix.ErrForbidden("guest registration is disabled on this homeserver"))
			return
		}
	} else if a.cfg.RegistrationDisabled {
		a.writeError(w, r, matrix.ErrForbidden("registration is disabled on this homeserver"))
		return
	}
	result, err := a.svc.Register(r.Context(), service.RegisterParams{
		Username:          body.Username,
		Password:          body.Password,
		DeviceID:          body.DeviceID,
		Auth:              authDict(body.Stage, body.Session, body.Auth),
		Guest:             guest,
		Token:             body.Token,
		Admin:             body.Admin && auth.IsAdmin(r),
		CallerAdmin:       auth.IsAdmin(r),
		DisplayName:       body.DisplayName,
		DeviceDisplayName: body.DeviceName,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// authDict folds the legacy top level type and session keys of a registration
// request into its User-Interactive Auth dict. Older clients put the stage name
// beside the username rather than under auth, and Synapse still accepts that.
func authDict(stage, session string, in matrix.Object) matrix.Object {
	if in == nil {
		if stage == "" && session == "" {
			return nil
		}
		in = matrix.Object{}
	}
	if _, ok := in["type"]; !ok && stage != "" {
		in["type"] = stage
	}
	if _, ok := in["session"]; !ok && session != "" {
		in["session"] = session
	}
	return in
}

// handleRegisterFlows implements GET /_matrix/client/v3/register, which answers
// with the User-Interactive Auth challenge instead of registering anything.
// Clients call it to discover the supported flows before sending credentials.
//
//	@Summary	List supported registration flows
//	@Tags		account
//	@Produce	json
//	@Success	401	{object}	service.AuthInfo
//	@Router		/_matrix/client/v3/register [get]
func (a *App) handleRegisterFlows(w http.ResponseWriter, r *http.Request) {
	if a.cfg.RegistrationDisabled {
		a.writeError(w, r, matrix.ErrForbidden("registration is disabled on this homeserver"))
		return
	}
	a.writeError(w, r, a.svc.RegistrationChallenge(r.Context(), queryValue(r, "session", "")))
}

// handleRegistrationTokenValidity implements
// GET /_matrix/client/v1/register/m.login.registration_token/validity, which
// tells a client whether a registration token is a way in.
//
//	@Summary	Check whether registration tokens are supported
//	@Tags		account
//	@Produce	json
//	@Success	200	{object}	map[string]bool
//	@Router		/_matrix/client/v1/register/m.login.registration_token/validity [get]
func (a *App) handleRegistrationTokenValidity(w http.ResponseWriter, r *http.Request) {
	if a.cfg.RegistrationDisabled {
		a.writeError(w, r, matrix.ErrForbidden("registration is disabled on this homeserver"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"supported": a.svc.RegistrationTokenSupported()})
}

// handleRegisterAvailable implements GET /_matrix/client/v3/register/available.
//
//	@Summary	Check whether a user ID is available
//	@Tags		account
//	@Produce	json
//	@Param		user_id	query	string	true	"Desired localpart or user ID"
//	@Success	200		{object}	map[string]bool
//	@Router		/_matrix/client/v3/register/available [get]
func (a *App) handleRegisterAvailable(w http.ResponseWriter, r *http.Request) {
	username := queryValue(r, "user_id", "")
	if username == "" {
		username = queryValue(r, "username", "")
	}
	available, err := a.svc.UsernameAvailable(r.Context(), username)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"available": available})
}

// handleLoginFlows implements GET /_matrix/client/v3/login.
//
//	@Summary	List supported login flows
//	@Tags		account
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/login [get]
func (a *App) handleLoginFlows(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.svc.LoginFlows())
}

// handleLogin implements POST /_matrix/client/v3/login.
//
//	@Summary	Authenticate and obtain an access token
//	@Tags		account
//	@Accept		json
//	@Produce	json
//	@Param		body	body		loginBody	true	"Login request"
//	@Success	200		{object}	service.SessionResult
//	@Router		/_matrix/client/v3/login [post]
func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body loginBody
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	switch body.Type {
	case "", "m.login.password":
	default:
		a.writeError(w, r, matrix.NewError(matrix.ErrCodeForbidden, http.StatusForbidden,
			"session does not support authentication protocol "+body.Type))
		return
	}
	result, err := a.svc.Login(r.Context(), body.userID(), body.Password, body.DeviceID, body.DeviceName)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleRefresh implements POST /_matrix/client/v3/refresh.
//
//	@Summary	Exchange a refresh token for a new access token
//	@Tags		account
//	@Produce	json
//	@Success	200	{object}	service.SessionResult
//	@Router		/_matrix/client/v3/refresh [post]
func (a *App) handleRefresh(w http.ResponseWriter, r *http.Request) {
	token := queryValue(r, "refresh_token", "")
	result, err := a.svc.Refresh(r.Context(), token)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleLogout implements POST /_matrix/client/v3/logout.
//
//	@Summary	Invalidate the current session
//	@Tags		account
//	@Produce	json
//	@Success	200	{object}	map[string]string
//	@Router		/_matrix/client/v3/logout [post]
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.Logout(r.Context(), auth.GetToken(r)); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id_server_unbind_result": "no-support"})
}

// handleLogoutAll implements POST /_matrix/client/v3/logout/all.
//
//	@Summary	Invalidate every session except the current one
//	@Tags		account
//	@Produce	json
//	@Success	200	{object}	map[string]string
//	@Router		/_matrix/client/v3/logout/all [post]
func (a *App) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.LogoutAll(r.Context(), auth.GetUserID(r), auth.GetDeviceID(r)); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id_server_unbind_result": "no-support"})
}

// handleWhoami implements GET /_matrix/client/v3/account/whoami.
//
//	@Summary	Identify the owner of an access token
//	@Tags		account
//	@Produce	json
//	@Success	200	{object}	map[string]string
//	@Router		/_matrix/client/v3/account/whoami [get]
func (a *App) handleWhoami(w http.ResponseWriter, r *http.Request) {
	userID, err := a.svc.Whoami(r.Context(), auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user_id": userID, "device_id": auth.GetDeviceID(r)})
}

// handleChangePassword implements POST /_matrix/client/v3/account/password.
//
//	@Summary	Change the account password
//	@Tags		account
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]string
//	@Router		/_matrix/client/v3/account/password [post]
func (a *App) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OldPassword string        `json:"old_password"`
		NewPassword string        `json:"new_password"`
		Auth        matrix.Object `json:"auth,omitempty"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.ChangePassword(r.Context(), auth.GetUserID(r), body.OldPassword, body.NewPassword, body.Auth); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id_server_unbind_result": "no-support"})
}

// handleDeactivate implements POST /_matrix/client/v3/account/deactivate.
//
//	@Summary	Deactivate an account
//	@Tags		account
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	matrix.Object
//	@Router		/_matrix/client/v3/account/deactivate [post]
func (a *App) handleDeactivate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Auth matrix.Object `json:"auth,omitempty"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.svc.DeactivateAccount(r.Context(), auth.GetUserID(r), body.Auth)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleCapabilities documents the capabilities endpoint in discovery.go and
// is registered there.
