package client

import (
	"errors"
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/users"
)

// loginRequest is the POST /login request body.
type loginRequest struct {
	Type       string `json:"type"`
	Identifier struct {
		Type string `json:"type"`
		User string `json:"user"`
	} `json:"identifier"`
	Address                  string `json:"address"`
	Medium                   string `json:"medium"`
	User                     string `json:"user"`
	Password                 string `json:"password"`
	DeviceID                 string `json:"device_id"`
	InitialDeviceDisplayName string `json:"initial_device_display_name"`
	RefreshToken             bool   `json:"refresh_token"`
}

// handleLoginFlows implements GET /login.
func (d *Deps) handleLoginFlows(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"flows": []map[string]any{
			{"type": auth.StagePassword},
		},
	})
}

// handleLogin implements POST /login with the m.login.password flow.
func (d *Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &req) {
		return
	}
	if req.RefreshToken {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
			"This server does not issue short-lived access tokens with refresh tokens."))
		return
	}
	if req.Type != auth.StagePassword {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeUnknown,
			"Invalid login type "+req.Type+"."))
		return
	}

	identifier := req.Identifier.User
	if identifier == "" {
		identifier = req.User
	}
	if req.Identifier.Type != "" && req.Identifier.Type != "m.id.user" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeUnknown,
			"Unsupported identifier type "+req.Identifier.Type+"."))
		return
	}

	user, err := d.Users.FindForLogin(r.Context(), identifier)
	if err != nil {
		if errors.Is(err, users.ErrUnknownUser) || errors.Is(err, persist.ErrNotFound) {
			// The same answer for unknown users and wrong passwords, so login
			// cannot be used to enumerate accounts.
			matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
				"Wrong user ID or password."))
			return
		}
		d.Log.Error("login lookup failed", "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	// A deactivated account is named as one. Its password hash is gone, so testing the
	// password first would answer "wrong password" to someone who needs to be told the
	// account itself has ended.
	if user.Deactivated {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeUserDeactivated,
			"This account has been deactivated."))
		return
	}
	if !users.CheckPassword(user, req.Password) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"Wrong user ID or password."))
		return
	}

	session, err := d.Users.NewSession(r.Context(), user.UserID, req.DeviceID, req.InitialDeviceDisplayName)
	if err != nil {
		if me, ok := matrixerr.As(err); ok {
			matrixerr.WriteError(w, http.StatusBadRequest, me)
			return
		}
		d.Log.Error("login failed", "user_id", user.UserID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"user_id":      user.UserID,
		"access_token": session.AccessToken,
		"device_id":    session.DeviceID,
	})
}
