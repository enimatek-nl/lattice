package client

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // the Synapse admin protocol specifies HMAC-SHA1
	"encoding/hex"
	"net/http"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/users"
)

// This file implements the Synapse-compatible shared-secret registration
// endpoint. It is NOT part of the Matrix specification; it exists so operators
// (and the Complement test harness, which requires it) can create accounts
// out of band. It is disabled unless a shared secret is configured, and the MAC
// is compared in constant time.

// adminRegisterRequest is the POST /_synapse/admin/v1/register body.
type adminRegisterRequest struct {
	Nonce    string `json:"nonce"`
	Username string `json:"username"`
	Password string `json:"password"`
	MAC      string `json:"mac"`
	Admin    bool   `json:"admin"`
}

// handleAdminRegisterGet issues a single-use nonce.
func (d *Deps) handleAdminRegisterGet(w http.ResponseWriter, r *http.Request) {
	if d.Cfg.Registration.SharedSecret == "" {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeUnrecognized,
			"This server does not support shared-secret registration."))
		return
	}
	nonce, err := d.UIA.NewNonce()
	if err != nil {
		d.writeInternal(w, "admin register nonce", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"nonce": nonce})
}

// handleAdminRegister completes shared-secret registration.
func (d *Deps) handleAdminRegister(w http.ResponseWriter, r *http.Request) {
	if d.Cfg.Registration.SharedSecret == "" {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeUnrecognized,
			"This server does not support shared-secret registration."))
		return
	}
	var req adminRegisterRequest
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &req) {
		return
	}
	if req.Nonce == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"A nonce is required."))
		return
	}
	if !d.UIA.ConsumeNonce(req.Nonce) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"Nonce unknown, expired or already used."))
		return
	}
	if !d.verifySharedSecretMAC(req) {
		matrixerr.Write(w, http.StatusUnauthorized, matrixerr.New(matrixerr.CodeUnauthorized,
			"MAC incorrect."))
		return
	}

	registration, err := d.Users.Register(r.Context(), users.RegisterParams{
		Username: req.Username,
		Password: req.Password,
		Admin:    req.Admin,
	})
	if err != nil {
		d.writeRegistrationError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"user_id":      registration.UserID,
		"access_token": registration.AccessToken,
		"device_id":    registration.DeviceID,
	})
}

// verifySharedSecretMAC recomputes the HMAC-SHA1 over
// nonce NUL username NUL password NUL ("admin"|"notadmin") and compares it in
// constant time.
func (d *Deps) verifySharedSecretMAC(req adminRegisterRequest) bool {
	h := hmac.New(sha1.New, []byte(d.Cfg.Registration.SharedSecret))
	h.Write([]byte(req.Nonce))
	h.Write([]byte{0x00})
	h.Write([]byte(req.Username))
	h.Write([]byte{0x00})
	h.Write([]byte(req.Password))
	h.Write([]byte{0x00})
	if req.Admin {
		h.Write([]byte("admin"))
	} else {
		h.Write([]byte("notadmin"))
	}
	expected := hex.EncodeToString(h.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(req.MAC))
}
