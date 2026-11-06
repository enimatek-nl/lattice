package client

import (
	"encoding/json"
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/users"
)

// Profile: GET /_matrix/client/v3/profile/{userID}, and GET, PUT and DELETE
// /_matrix/client/v3/profile/{userID}/{keyName}.
//
// The reads carry no access token and the writes do, and the asymmetry is the point of the endpoint: a
// room's members have to be able to ask how a person is presented without having any standing of their
// own, and only that person's own server may answer.
//
// The pinned specification gives these endpoints a general shape — one field named in the path, an
// arbitrary `keyName`, a body holding exactly that one key — and leaves extra fields to
// `m.profile_fields`, which a server advertises in `/versions` when it supports them. This server does
// not advertise it and holds only the two standard fields, `displayname` and `avatar_url`; a request for
// any other `keyName` is refused rather than quietly stored, because a field nothing reads is a claim
// the server would be making about a user for no reason. The general path is registered anyway, so that
// an unknown field is refused as a field rather than as an unrecognized endpoint.

// profileFields are the profile fields this server holds. A profile is small and fixed for a reason:
// its values are copied into the member events of every room the user stands in.
var profileFields = map[string]bool{"displayname": true, "avatar_url": true}

// handleProfileGet serves GET /_matrix/client/v3/profile/{userID}.
func (d *Deps) handleProfileGet(w http.ResponseWriter, r *http.Request) {
	profile, ok := d.profileFor(w, r, r.PathValue("userID"))
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, profileFieldsBody(profile))
}

// handleProfileFieldGet serves GET /_matrix/client/v3/profile/{userID}/{keyName}.
func (d *Deps) handleProfileFieldGet(w http.ResponseWriter, r *http.Request) {
	keyName := r.PathValue("keyName")
	if !profileFields[keyName] {
		d.unknownProfileField(w, keyName)
		return
	}
	profile, ok := d.profileFor(w, r, r.PathValue("userID"))
	if !ok {
		return
	}
	body := profileFieldsBody(profile)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{keyName: body[keyName]})
}

// handleProfileFieldPut serves PUT /_matrix/client/v3/profile/{userID}/{keyName}.
func (d *Deps) handleProfileFieldPut(w http.ResponseWriter, r *http.Request) {
	keyName := r.PathValue("keyName")
	userID := r.PathValue("userID")
	if !profileFields[keyName] {
		d.unknownProfileField(w, keyName)
		return
	}
	if !d.ownsProfile(w, r, userID) {
		return
	}
	value, ok := d.profileFieldBody(w, r, keyName)
	if !ok {
		return
	}
	var err error
	if keyName == "displayname" {
		_, err = d.Users.SetProfile(r.Context(), userID, value, nil)
	} else {
		_, err = d.Users.SetProfile(r.Context(), userID, nil, value)
	}
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleProfileFieldDelete serves DELETE /_matrix/client/v3/profile/{userID}/{keyName}.
func (d *Deps) handleProfileFieldDelete(w http.ResponseWriter, r *http.Request) {
	keyName := r.PathValue("keyName")
	userID := r.PathValue("userID")
	if !profileFields[keyName] {
		d.unknownProfileField(w, keyName)
		return
	}
	if !d.ownsProfile(w, r, userID) {
		return
	}
	if _, err := d.Users.ClearProfile(r.Context(), userID, keyName); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// profileFor reads the profile of a local account, answering the cases that are not the account
// service's to answer: a user this server is not the authority for, and a user it has never heard of.
func (d *Deps) profileFor(w http.ResponseWriter, r *http.Request, userID string) (users.Profile, bool) {
	local, err := d.localUser(userID)
	if err != nil {
		d.serviceError(w, err)
		return users.Profile{}, false
	}
	if !local {
		// A profile for a user on another server is a different request, not this one answered
		// approximately. Synapse asks the other server over federation
		// (`GET /_matrix/federation/v1/profile/{userID}`); this server does not make that call yet, and
		// answering from a member event it happens to hold would present another server's word about a
		// person as though this one had checked it.
		matrixerr.Write(w, http.StatusNotFound, matrixerr.New(matrixerr.CodeNotFound,
			"That user is not on this server, and this server is not the authority on how they are presented."))
		return users.Profile{}, false
	}
	profile, err := d.Users.ProfileOf(r.Context(), userID)
	if err != nil {
		d.serviceError(w, err)
		return users.Profile{}, false
	}
	return profile, true
}

// ownsProfile refuses a profile write the token does not belong to. The path names a user; the token
// names a user; only the token is a permission.
func (d *Deps) ownsProfile(w http.ResponseWriter, r *http.Request, userID string) bool {
	if userID != auth.UserID(r) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"A user may only change their own profile."))
		return false
	}
	return true
}

// profileFieldBody reads the one-key body the profile PUT asks for.
//
// The key must be the one the path named — that is a MUST in the specification, and a body that names a
// different field than the path is a request whose target the server would have to guess. A JSON null is
// taken as a clear, because a client that wants to remove a value has two ways to ask and both of them
// mean the same thing here.
func (d *Deps) profileFieldBody(w http.ResponseWriter, r *http.Request, keyName string) (*string, bool) {
	raw, err := httpx.DecodeRawBody(r, d.maxBody())
	if err != nil {
		d.serviceError(w, err)
		return nil, false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeNotJSON,
			"A profile request body is a JSON object."))
		return nil, false
	}
	if len(body) != 1 {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeBadJSON,
			"A profile request body carries exactly one field, and this one carries %d.", len(body)))
		return nil, false
	}
	value, present := body[keyName]
	if !present {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeBadJSON,
			"The profile body must name %q, which is the field the path asks for.", keyName))
		return nil, false
	}
	if string(value) == "null" {
		empty := ""
		return &empty, true
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q must be a string.", keyName))
		return nil, false
	}
	return &text, true
}

// profileFieldsBody renders both fields. Both are always present: an absent key and an empty one are the
// same fact to a client, and a response that sometimes omits one makes a client guess.
func profileFieldsBody(profile users.Profile) map[string]any {
	return map[string]any{
		"displayname": profile.Displayname,
		"avatar_url":  profile.AvatarURL,
	}
}

// unknownProfileField answers a `keyName` this server does not hold. It is M_NOT_FOUND rather than
// M_UNRECOGNIZED: the endpoint is understood, the field is not, and a client that sent an ordinary
// profile request should be able to tell the two apart.
func (d *Deps) unknownProfileField(w http.ResponseWriter, keyName string) {
	matrixerr.Write(w, http.StatusNotFound, matrixerr.Newf(matrixerr.CodeNotFound,
		"This server holds no profile field %q.", keyName))
}
