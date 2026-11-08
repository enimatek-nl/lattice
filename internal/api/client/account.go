package client

import (
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
)

// The two endpoints that end an account's ability to prove itself: changing its password and
// deactivating it. Both are guarded by User-Interactive Auth, because the session that is asking
// is exactly the thing that may have been stolen.

// passwordChangeRequest is the POST /account/password body. An empty body is allowed: it is how
// a client discovers the flows this endpoint offers.
type passwordChangeRequest struct {
	Auth        *uiaAuthPayload `json:"auth"`
	NewPassword string          `json:"new_password"`
	// LogoutDevices defaults to true, which is the safe direction: someone who changes a
	// password because they think they have been compromised means it.
	LogoutDevices *bool `json:"logout_devices"`
}

// handlePasswordChange implements POST /account/password.
func (d *Deps) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	var req passwordChangeRequest
	if r.ContentLength != 0 {
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &req) {
			return
		}
	}
	authenticatedAs, ok := d.authorizePasswordStage(w, r, req.Auth)
	if !ok {
		return
	}
	// The credentials have to be the ones of the account making the request; otherwise any
	// session that can produce someone else's password could change their password.
	if authenticatedAs != auth.UserID(r) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"The authenticated user does not own this session."))
		return
	}
	if req.NewPassword == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
			"A new password is required."))
		return
	}
	if err := d.Users.SetPassword(r.Context(), authenticatedAs, req.NewPassword); err != nil {
		d.serviceError(w, err)
		return
	}
	// The old password stops working because the hash it was checked against is gone. The
	// other sessions stop working too, unless this client asked to keep them; the one that
	// asked stays, because a client that has just proved its password should not be sent to
	// the login door by the very call that changed it.
	if req.LogoutDevices == nil || *req.LogoutDevices {
		if err := d.Tokens.RevokeExcept(r.Context(), authenticatedAs, auth.DeviceID(r)); err != nil {
			d.writeInternal(w, "password change revoke", err)
			return
		}
		// The pushers go with the sessions they belonged to. A pusher is an app on a device
		// saying "wake me here", written by one session; when that session stops working the
		// address stops being anybody's to keep using, and the next person to hold this
		// account would otherwise inherit a notification path they never asked for. The
		// session that just proved the new password keeps its own, because it is still here.
		if err := d.Pushers.ForgetAllButToken(r.Context(), authenticatedAs, auth.TokenHash(r)); err != nil {
			d.writeInternal(w, "password change pushers", err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// deactivateRequest is the POST /account/deactivate body.
type deactivateRequest struct {
	Auth *uiaAuthPayload `json:"auth"`
	// Erase asks the server to forget the account entirely rather than to make it unusable.
	// This server keeps the account row either way — rooms, membership events and keys name
	// it — so the flag does not change the answer, and the code says so rather than
	// pretending to a distinction it does not make.
	Erase bool `json:"erase"`
}

// handleDeactivate implements POST /account/deactivate.
func (d *Deps) handleDeactivate(w http.ResponseWriter, r *http.Request) {
	var req deactivateRequest
	if r.ContentLength != 0 {
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &req) {
			return
		}
	}
	authenticatedAs, ok := d.authorizePasswordStage(w, r, req.Auth)
	if !ok {
		return
	}
	if authenticatedAs != auth.UserID(r) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"The authenticated user does not own this session."))
		return
	}
	if err := d.Users.Deactivate(r.Context(), authenticatedAs); err != nil {
		d.serviceError(w, err)
		return
	}
	// Every token goes, including this one: deactivating an account cannot leave a session of
	// it running, and the request that did it has already been answered from the authenticated
	// identity rather than from the token.
	if err := d.Tokens.RevokeUser(r.Context(), authenticatedAs); err != nil {
		d.writeInternal(w, "deactivate revoke", err)
		return
	}
	if req.Erase {
		d.Log.Info("an account asked to be erased; this server deactivates it and keeps the row",
			"user_id", authenticatedAs)
	}
	// This server speaks to no identity server, so there is nothing to unbind.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id_server":               "",
		"id_server_unbind_result": "no-support",
	})
}
