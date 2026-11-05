package client

import (
	"errors"
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/users"
)

// registrationRequest is the POST /register request body.
type registrationRequest struct {
	Username                 string          `json:"username"`
	Password                 string          `json:"password"`
	DeviceID                 string          `json:"device_id"`
	InitialDeviceDisplayName string          `json:"initial_device_display_name"`
	InhibitLogin             bool            `json:"inhibit_login"`
	RefreshToken             bool            `json:"refresh_token"`
	Auth                     *uiaAuthPayload `json:"auth"`
}

// uiaAuthPayload is the "auth" member of a User-Interactive Auth request.
type uiaAuthPayload struct {
	Session    string `json:"session"`
	Type       string `json:"type"`
	Identifier struct {
		Type string `json:"type"`
		User string `json:"user"`
	} `json:"identifier"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

// handleRegisterGet implements GET /register: it lists the offered flows.
func (d *Deps) handleRegisterGet(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"flows":  d.registrationFlows(),
		"params": map[string]any{},
	})
}

// registrationFlows lists the flows offered for registration.
func (d *Deps) registrationFlows() []auth.Flow {
	if d.Cfg.Registration.Open {
		return []auth.Flow{{Stages: []string{auth.StageDummy}}}
	}
	// Closed registration still needs a flow list; the password flow lets an
	// existing account create another one only when an administrator enables that
	// policy, so by default no flow is offered.
	return []auth.Flow{}
}

// handleRegisterAvailable implements GET /register/available, which reports
// whether a user ID can still be claimed.
func (d *Deps) handleRegisterAvailable(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	if username == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A username query parameter is required."))
		return
	}
	localpart := d.Users.LocalpartOf(username)
	if err := d.Users.ValidateLocalpart(localpart); err != nil {
		matrixerr.WriteError(w, http.StatusBadRequest, err)
		return
	}
	userID := "@" + localpart + ":" + d.serverName()
	if _, err := d.Users.Get(r.Context(), userID); err == nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeUserInUse,
			"That user ID is already taken."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"available": true})
}

// handleRegister implements POST /register.
//
// Request contents are validated before the User-Interactive Auth challenge:
// interoperable servers report M_INVALID_USERNAME / M_USER_IN_USE for unusable
// input rather than a 401, which is what clients (and Complement) expect.
func (d *Deps) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registrationRequest
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &req) {
		return
	}
	if req.RefreshToken {
		// Refresh tokens are optional in the specification and not implemented
		// yet; refuse explicitly instead of silently ignoring the request.
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
			"This server does not issue short-lived access tokens with refresh tokens."))
		return
	}

	if req.Username != "" || req.Password != "" {
		if err := d.Users.CheckPasswordPolicy(req.Password); err != nil {
			matrixerr.WriteError(w, http.StatusBadRequest, err)
			return
		}
	}
	if req.Username != "" {
		localpart := d.Users.LocalpartOf(req.Username)
		if err := d.Users.ValidateLocalpart(localpart); err != nil {
			matrixerr.WriteError(w, http.StatusBadRequest, err)
			return
		}
		if _, err := d.Users.Get(r.Context(), "@"+localpart+":"+d.serverName()); err == nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeUserInUse,
				"That user ID is already taken."))
			return
		}
	}

	if req.Auth == nil || req.Auth.Type == "" {
		d.challenge(w, d.registrationFlows())
		return
	}

	if !d.authorizeRegistration(w, r, req) {
		return
	}

	registration, err := d.Users.Register(r.Context(), users.RegisterParams{
		Username:                 req.Username,
		Password:                 req.Password,
		DeviceID:                 req.DeviceID,
		InitialDeviceDisplayName: req.InitialDeviceDisplayName,
		InhibitLogin:             req.InhibitLogin,
	})
	if err != nil {
		d.writeRegistrationError(w, err)
		return
	}
	if req.Auth.Session != "" {
		d.UIA.Drop(req.Auth.Session)
	}

	body := map[string]any{
		"user_id":   registration.UserID,
		"device_id": registration.DeviceID,
	}
	if registration.AccessToken != "" {
		body["access_token"] = registration.AccessToken
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// authorizeRegistration validates the "auth" member. It returns true when the
// request may proceed; otherwise a response has already been written.
func (d *Deps) authorizeRegistration(w http.ResponseWriter, r *http.Request, req registrationRequest) bool {
	stage := req.Auth.Type
	if !flowAllowsStage(d.registrationFlows(), stage) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"Authentication type "+stage+" is not offered for registration by this server."))
		return false
	}

	switch stage {
	case auth.StageDummy:
		if !d.Cfg.Registration.Open {
			matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
				"Registration is closed on this homeserver."))
			return false
		}
		// m.login.dummy carries no credentials, so it is only meaningful as the
		// completion of a session this server issued: an unknown or missing
		// session gets a fresh challenge instead of a free pass.
		if req.Auth.Session == "" {
			d.challenge(w, d.registrationFlows())
			return false
		}
		if _, ok := d.UIA.CompleteStage(req.Auth.Session, auth.StageDummy); !ok {
			d.challenge(w, d.registrationFlows())
			return false
		}
		return true

	case auth.StagePassword:
		identifier := req.Auth.Identifier.User
		password := req.Auth.Password
		if password == "" {
			password = req.Password
		}
		user, err := d.Users.FindForLogin(r.Context(), identifier)
		if err != nil || !users.CheckPassword(user, password) {
			d.failAuth(w, req, "Authentication failed.")
			return false
		}
		if req.Auth.Session != "" {
			d.UIA.CompleteStage(req.Auth.Session, auth.StagePassword)
		}
		return true
	}

	matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
		"Authentication type "+stage+" is not supported."))
	return false
}

// challenge answers a User-Interactive Auth challenge: the error code the
// specification defines for a missing stage, plus the flows, params and session
// that clients need to continue.
func (d *Deps) challenge(w http.ResponseWriter, flows []auth.Flow) {
	sess := d.UIA.NewSession(flows, nil)
	matrixerr.Write(w, http.StatusUnauthorized, matrixerr.New(
		matrixerr.CodeNoAuth, "Missing privileges to complete this action.",
	).WithExtra(d.UIA.ChallengeBody(sess)))
}

// failAuth answers a failed authentication attempt, re-issuing the challenge when
// the caller referenced a session.
func (d *Deps) failAuth(w http.ResponseWriter, req registrationRequest, msg string) {
	extra := map[string]any{}
	if req.Auth.Session != "" {
		if sess, ok := d.UIA.GetSession(req.Auth.Session); ok {
			extra = d.UIA.ChallengeBody(sess)
		}
	}
	matrixerr.Write(w, http.StatusForbidden,
		matrixerr.New(matrixerr.CodeForbidden, msg).WithExtra(extra))
}

// writeRegistrationError maps registration failures onto the error codes the
// specification defines for this endpoint.
func (d *Deps) writeRegistrationError(w http.ResponseWriter, err error) {
	if me, ok := matrixerr.As(err); ok {
		switch me.Code {
		case matrixerr.CodeUserInUse, matrixerr.CodeInvalidUsername, matrixerr.CodeWeakPassword, matrixerr.CodeInvalidParam:
			matrixerr.Write(w, http.StatusBadRequest, me)
		default:
			matrixerr.Write(w, http.StatusForbidden, me)
		}
		return
	}
	if errors.Is(err, users.ErrUnknownUser) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden, "Authentication failed."))
		return
	}
	d.Log.Error("registration failed", "error", err)
	matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
		"The server encountered an internal error."))
}
