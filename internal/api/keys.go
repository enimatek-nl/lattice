package api

import (
	"encoding/json"
	"net/http"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// The end-to-end-encryption endpoints store and redistribute the key material
// clients produce. Lattice never interprets it: the JSON a client uploads is the
// JSON another client gets back, and all signature checking happens on the
// clients.

// handleKeysUpload implements POST /_matrix/client/v3/keys/upload.
//
//	@Summary	Upload device identity and one-time keys
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Param		body	body		service.KeyUploadParams	true	"Key material"
//	@Success	200		{object}	service.KeyUploadResult
//	@Router		/_matrix/client/v3/keys/upload [post]
func (a *App) handleKeysUpload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"device_id"`
		// DeviceKeys is what the specification names the identity key of the
		// device, and is what the Rust crypto stack Element uses sends. DeviceKey
		// is the older spelling some clients write. Reading only the second one
		// answers a spec-shaped upload with 200 while storing nothing, which leaves
		// the device absent from /keys/query and its signatures unresolvable.
		DeviceKeys   matrix.Object            `json:"device_keys"`
		DeviceKey    matrix.Object            `json:"device_key"`
		OneTimeKeys  map[string]matrix.Object `json:"one_time_keys"`
		FallbackKeys map[string]matrix.Object `json:"fallback_keys"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	deviceID := auth.GetDeviceID(r)
	if body.DeviceID != "" && body.DeviceID != deviceID {
		a.writeError(w, r, matrix.ErrInvalidParam("the device_id in the request does not match the access token"))
		return
	}
	deviceKey := body.DeviceKeys
	if len(deviceKey) == 0 {
		deviceKey = body.DeviceKey
	}
	result, err := a.svc.UploadKeys(r.Context(), service.KeyUploadParams{
		UserID:       auth.GetUserID(r),
		DeviceID:     deviceID,
		DeviceKey:    deviceKey,
		OneTimeKeys:  body.OneTimeKeys,
		FallbackKeys: body.FallbackKeys,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleKeysQuery implements POST /_matrix/client/v3/keys/query.
//
//	@Summary	Query the device keys of users
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Param		body	body		map[string][]string	true	"device_keys: user ID to device IDs (empty for all)"
//	@Success	200		{object}	service.KeyQueryResult
//	@Router		/_matrix/client/v3/keys/query [post]
func (a *App) handleKeysQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceKeys map[string][]string `json:"device_keys"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.svc.QueryKeys(r.Context(), auth.GetUserID(r), body.DeviceKeys)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleKeysClaim implements POST /_matrix/client/v3/keys/claim.
//
//	@Summary	Claim one-time keys for devices
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Param		body	body		map[string]map[string]string	true	"one_time_keys: user ID to device ID to algorithm"
//	@Success	200		{object}	service.KeyClaimResult
//	@Router		/_matrix/client/v3/keys/claim [post]
func (a *App) handleKeysClaim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OneTimeKeys map[string]map[string]string `json:"one_time_keys"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.svc.ClaimKeys(r.Context(), body.OneTimeKeys)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleKeysChanges implements GET /_matrix/client/v3/keys/changes, which lists
// the users whose devices changed between two positions in the event stream.
//
//	@Summary	List users whose devices changed
//	@Tags		e2ee
//	@Produce	json
//	@Param		from	query	string	true	"Stream position to start after"
//	@Param		to		query	string	true	"Stream position to include"
//	@Success	200		{object}	matrix.DeviceLists
//	@Router		/_matrix/client/v3/keys/changes [get]
func (a *App) handleKeysChanges(w http.ResponseWriter, r *http.Request) {
	from, err := a.tokenParam(r, "from")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	to, err := a.tokenParam(r, "to")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	lists, err := a.svc.DeviceListChanges(r.Context(), auth.GetUserID(r), from, to, auth.GetDeviceID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if lists == nil {
		lists = &matrix.DeviceLists{Changed: []string{}, Left: []string{}}
	}
	writeJSON(w, http.StatusOK, lists)
}

// tokenParam reads a stream position from a required query parameter.
func (a *App) tokenParam(r *http.Request, key string) (uint64, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return 0, matrix.ErrInvalidParam("the %q query parameter is required", key)
	}
	pos, err := a.svc.ParseToken(value)
	if err != nil {
		return 0, err
	}
	return pos, nil
}

// handleCrossSigningUpload implements
// POST /_matrix/client/v3/keys/device_signing/upload.
//
// The interactive auth exchange stands in front of the request only when it
// would change an identity that is already stored: a new account publishes its
// first set of keys, and a client may repeat the stored set, without an auth
// dict.
//
//	@Summary	Upload cross-signing keys
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/keys/device_signing/upload [post]
func (a *App) handleCrossSigningUpload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Auth           matrix.Object `json:"auth"`
		MasterKey      matrix.Object `json:"master_key"`
		SelfSigningKey matrix.Object `json:"self_signing_key"`
		UserSigningKey matrix.Object `json:"user_signing_key"`
	}
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.UploadCrossSigningKeys(r.Context(), auth.GetUserID(r), service.CrossSigningUpload{
		Auth:           body.Auth,
		MasterKey:      body.MasterKey,
		SelfSigningKey: body.SelfSigningKey,
		UserSigningKey: body.UserSigningKey,
	}); err != nil {
		a.writeError(w, r, err)
		return
	}
	// The specification answers with an empty object: what was uploaded is what
	// a client reads back through POST /keys/query.
	writeJSON(w, http.StatusOK, matrix.Object{})
}

// handleSignatureUpload implements POST /_matrix/client/v3/keys/signatures/upload.
//
//	@Summary	Upload key signatures
//	@Tags		e2ee
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/keys/signatures/upload [post]
func (a *App) handleSignatureUpload(w http.ResponseWriter, r *http.Request) {
	// Three shapes reach this endpoint. The specification's body is a bare map of
	// user ID to key ID to signed object with no wrapper at all, and that is what
	// ruma - the types behind the Rust crypto stack Element ships - and
	// matrix-js-sdk both send. The wrapped "signed_users" and "signed_keys"
	// shapes are what Lattice has taken from the start and keeps accepting.
	// Reading only the wrappers answers a real client's upload with 200 while
	// storing nothing: the self-signing key's signature on the device never
	// lands, and that session stays unverified for good - which is what left
	// Element asking a brand-new account to verify itself.
	var members map[string]json.RawMessage
	if err := decodeBody(r, &members); err != nil {
		a.writeError(w, r, err)
		return
	}
	upload := service.SignatureUpload{}
	if raw, ok := members["signed_users"]; ok {
		if err := json.Unmarshal(raw, &upload.SignedUsers); err != nil {
			a.writeError(w, r, matrix.ErrBadJSON(err))
			return
		}
	}
	if raw, ok := members["signed_keys"]; ok {
		if err := json.Unmarshal(raw, &upload.SignedKeys); err != nil {
			a.writeError(w, r, matrix.ErrBadJSON(err))
			return
		}
	}
	if _, wrapped := members["signed_users"]; !wrapped {
		if _, alsoWrapped := members["signed_keys"]; !alsoWrapped {
			// Neither wrapper: the body itself is the specification's map.
			upload.SignedUsers = make(map[string]map[string]matrix.Object, len(members))
			for userID, raw := range members {
				byEntity := map[string]matrix.Object{}
				if err := json.Unmarshal(raw, &byEntity); err != nil {
					a.writeError(w, r, matrix.ErrBadJSON(err))
					return
				}
				upload.SignedUsers[userID] = byEntity
			}
		}
	}
	if err := a.svc.UploadKeySignatures(r.Context(), auth.GetUserID(r), upload); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrix.Object{})
}
