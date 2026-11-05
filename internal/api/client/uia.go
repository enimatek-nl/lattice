package client

import (
	"errors"
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/users"
)

// This file holds the User-Interactive Auth helpers shared by the endpoints that
// guard destructive operations (deleting a device, changing a password) with a
// single m.login.password stage.

// passwordOnlyFlow is the flow offered by endpoints that require a fresh password
// confirmation.
func passwordOnlyFlow() []auth.Flow {
	return []auth.Flow{{Stages: []string{auth.StagePassword}}}
}

// forbiddenChallenge answers with a User-Interactive Auth challenge carrying the
// M_FORBIDDEN error code, which is what endpoints guarding destructive actions
// use. The body always includes session, flows and params, because clients need
// them to continue.
func (d *Deps) forbiddenChallenge(w http.ResponseWriter, flows []auth.Flow) {
	sess := d.UIA.NewSession(flows, nil)
	matrixerr.Write(w, http.StatusUnauthorized, matrixerr.New(matrixerr.CodeForbidden,
		"Missing privileges to complete this action.",
	).WithExtra(d.UIA.ChallengeBody(sess)))
}

// authorizePasswordStage verifies an m.login.password credential. It returns the
// authenticated user ID; when it returns false, a response has been written.
//
// A session is optional here: a client may answer the challenge directly with
// credentials. Unlike m.login.dummy, this stage proves something on its own.
func (d *Deps) authorizePasswordStage(w http.ResponseWriter, r *http.Request, payload *uiaAuthPayload) (string, bool) {
	flows := passwordOnlyFlow()
	if payload == nil || payload.Type != auth.StagePassword {
		d.forbiddenChallenge(w, flows)
		return "", false
	}
	identifier := payload.Identifier.User
	if identifier == "" {
		d.forbiddenChallenge(w, flows)
		return "", false
	}

	user, err := d.Users.FindForLogin(r.Context(), identifier)
	if err != nil {
		if !errors.Is(err, users.ErrUnknownUser) && !errors.Is(err, persist.ErrNotFound) {
			d.writeInternal(w, "user-interactive auth", err)
			return "", false
		}
		// Unknown account or wrong password: same answer, plus a fresh challenge
		// so the client can retry.
		matrixerr.Write(w, http.StatusUnauthorized, matrixerr.New(matrixerr.CodeForbidden,
			"Authentication failed.",
		).WithExtra(d.challengeExtras(payload.Session, flows)))
		return "", false
	}
	if !users.CheckPassword(user, payload.Password) {
		matrixerr.Write(w, http.StatusUnauthorized, matrixerr.New(matrixerr.CodeForbidden,
			"Authentication failed.",
		).WithExtra(d.challengeExtras(payload.Session, flows)))
		return "", false
	}
	if payload.Session != "" {
		d.UIA.CompleteStage(payload.Session, auth.StagePassword)
	}
	return user.UserID, true
}

// challengeExtras returns the challenge members of an existing session when the
// client named one that is still live, and a new session otherwise.
func (d *Deps) challengeExtras(sessionID string, flows []auth.Flow) map[string]any {
	if sessionID != "" {
		if sess, ok := d.UIA.GetSession(sessionID); ok {
			return d.UIA.ChallengeBody(sess)
		}
	}
	return d.UIA.ChallengeBody(d.UIA.NewSession(flows, nil))
}

// flowAllowsStage reports whether any offered flow contains the stage.
func flowAllowsStage(flows []auth.Flow, stage string) bool {
	for _, flow := range flows {
		for _, s := range flow.Stages {
			if s == stage {
				return true
			}
		}
	}
	return false
}
