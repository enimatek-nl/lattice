package service

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// The usages a cross-signing key can have, as the specification spells them.
const (
	CrossSigningMaster      = "master"
	CrossSigningSelfSigning = "self-signing"
	CrossSigningUserSigning = "user-signing"
)

// usageSpellings maps every usage spelling seen in the wild onto the usage
// Lattice records. The specification writes self-signing and user-signing with a
// hyphen; the Rust crypto stack that Element uses writes self_signing and
// user_signing with an underscore. Both name the same key, and rejecting a
// correctly signed key over a hyphen leaves a client unable to set up
// encryption at all, so both are accepted. The object is stored exactly as it
// arrived: rewriting a signed object would break the signature that covers it.
var usageSpellings = map[string]string{
	"master":       CrossSigningMaster,
	"self-signing": CrossSigningSelfSigning,
	"self_signing": CrossSigningSelfSigning,
	"user-signing": CrossSigningUserSigning,
	"user_signing": CrossSigningUserSigning,
}

// usageOfName returns the canonical usage a spelling names.
func usageOfName(name string) (string, bool) {
	canonical, ok := usageSpellings[name]
	return canonical, ok
}

// CrossSigningUpload is the body of POST /keys/device_signing/upload.
type CrossSigningUpload struct {
	// Auth is the User-Interactive Auth dict proving the requester controls the
	// account, which matters because these keys decide what other clients trust.
	Auth matrix.Object
	// MasterKey, SelfSigningKey and UserSigningKey are the key objects as the
	// client built them.
	MasterKey      matrix.Object
	SelfSigningKey matrix.Object
	UserSigningKey matrix.Object
	// Reset is the org.matrix.cross_signing_reset flag: the user agreed to throw
	// the old keys away, which is the only way to replace a master key.
	Reset bool
}

// verifiedKey is a cross-signing key that passed verification: the object as the
// client sent it, its id, its usage and its public key.
type verifiedKey struct {
	object  matrix.Object
	id      string
	usage   string
	public  ed25519.PublicKey
	keyJSON string
}

// UploadCrossSigningKeys stores the master, self-signing and user-signing keys
// of a user.
//
// These keys are the root of trust of an end-to-end encrypted conversation, so
// Lattice checks them rather than storing them blind as it does with device
// keys: the key material has to be an ed25519 key whose published id matches it,
// the usage has to be what the field promises, and the signatures have to be
// valid over the canonical form of the object. A client that uploaded a key
// somebody else signed would otherwise hand out a key nobody can verify, and
// every conversation protected by it would be quietly worthless.
func (s *Service) UploadCrossSigningKeys(ctx context.Context, userID string, upload CrossSigningUpload) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.authorizeCrossSigning(userID, upload.Auth); err != nil {
		return err
	}

	existing, err := s.db.CrossSigningRepo.ByUsage(userID, CrossSigningMaster)
	if err != nil && !errors.Is(err, persist.ErrNoKey) {
		return matrix.ErrInternal(err)
	}

	var master *verifiedKey
	if len(upload.MasterKey) > 0 {
		master, err = verifyCrossSigningKey(upload.MasterKey, userID, CrossSigningMaster, nil)
		if err != nil {
			return err
		}
	} else {
		// Without a new master key the stored one signs the rest.
		if existing == nil {
			return matrix.ErrInvalidParam("a master key is required to sign the other cross-signing keys")
		}
		master, err = storedCrossSigningKey(existing)
		if err != nil {
			return err
		}
	}

	selfSigning, err := s.verifyOptionalKey(upload.SelfSigningKey, userID, CrossSigningSelfSigning, master)
	if err != nil {
		return err
	}
	userSigning, err := s.verifyOptionalKey(upload.UserSigningKey, userID, CrossSigningUserSigning, master)
	if err != nil {
		return err
	}

	if existing != nil && master != nil && existing.KeyID != master.id {
		if !upload.Reset {
			// Element expects this exact shape of refusal: it offers the user a
			// reset and sends the flag when they agree.
			return matrix.NewError(matrix.ErrCodeForbidden, http.StatusConflict,
				"attempted to replace existing cross-signing keys without the reset flag")
		}
		if _, err := s.db.CrossSigningRepo.DeleteAll(userID); err != nil {
			return matrix.ErrInternal(err)
		}
	}

	for _, key := range []*verifiedKey{master, selfSigning, userSigning} {
		if key == nil {
			continue
		}
		if err := s.db.CrossSigningRepo.Set(&persist.CrossSigningKey{
			UserID:      userID,
			KeyID:       key.id,
			Usage:       key.usage,
			Data:        key.keyJSON,
			FirstSeenMS: s.NowMS(),
		}); err != nil {
			return matrix.ErrInternal(err)
		}
	}
	// Other devices have to learn that the picture of this user changed, which
	// is what the device list change in a sync is for.
	return s.recordDeviceListChange(ctx, persist.DeviceListChanged, userID)
}

// verifyOptionalKey checks a cross-signing key a client may leave out.
func (s *Service) verifyOptionalKey(object matrix.Object, userID, usage string, master *verifiedKey) (*verifiedKey, error) {
	if len(object) == 0 {
		return nil, nil
	}
	return verifyCrossSigningKey(object, userID, usage, master)
}

// verifyCrossSigningKey checks one key object and returns what was verified.
//
// The master key signs itself; the self-signing and user-signing keys are signed
// by the master key. Every signature is made over the canonical encoding of the
// object without its signatures, which is why Lattice builds that encoding
// itself instead of trusting the bytes the client sent.
func verifyCrossSigningKey(object matrix.Object, userID, usage string, master *verifiedKey) (*verifiedKey, error) {
	if got := authString(object, "user_id"); got != userID {
		return nil, matrix.ErrInvalidParam("the %s key belongs to %s, not to %s", usage, got, userID)
	}
	keys, _ := object["keys"].(matrix.Object)
	if len(keys) != 1 {
		return nil, matrix.ErrInvalidParam("the %s key must carry exactly one public key", usage)
	}
	var id, encoded string
	for keyID, value := range keys {
		id = keyID
		encoded, _ = value.(string)
	}
	if id == "" || encoded == "" {
		return nil, matrix.ErrInvalidParam("the %s key is not usable", usage)
	}
	public, err := matrix.DecodeEd25519Key(encoded)
	if err != nil {
		return nil, matrix.ErrInvalidParam("the %s key is not an ed25519 key: %v", usage, err)
	}
	if id != matrix.Ed25519KeyID(public) {
		return nil, matrix.ErrInvalidParam("the id of the %s key does not match its key material", usage)
	}
	if !hasUsage(object["usage"], usage) {
		return nil, matrix.ErrInvalidParam(
			"the usage of the %s key is %v, which does not name that key", usage, object["usage"])
	}

	if usage == CrossSigningMaster {
		// The master key vouches for itself.
		if err := matrix.VerifyEd25519Signature(object, userID, id, public); err != nil {
			return nil, matrix.ErrInvalidParam("the master key is not usable: %v", err)
		}
	} else {
		// The self-signing and user-signing keys are vouched for by the master
		// key, which is what ties them to this user's identity.
		if master == nil {
			return nil, matrix.ErrInvalidParam("the %s key cannot be verified without a master key", usage)
		}
		if err := matrix.VerifyEd25519Signature(object, userID, master.id, master.public); err != nil {
			return nil, matrix.ErrInvalidParam("the %s key is not usable: %v", usage, err)
		}
	}

	encodedObject, err := json.Marshal(object)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return &verifiedKey{object: object, id: id, usage: usage, public: public, keyJSON: string(encodedObject)}, nil
}

// hasUsage reports whether the usage field of a key object names the usage want,
// under any of its accepted spellings.
func hasUsage(value any, want string) bool {
	matches := func(name string) bool {
		canonical, ok := usageOfName(name)
		return ok && canonical == want
	}
	switch typed := value.(type) {
	case []string:
		if len(typed) == 0 {
			return false
		}
		for _, name := range typed {
			if !matches(name) {
				return false
			}
		}
		return true
	case []any:
		if len(typed) == 0 {
			return false
		}
		for _, item := range typed {
			name, ok := item.(string)
			if !ok || !matches(name) {
				return false
			}
		}
		return true
	}
	// A single string is what some clients write; accept it.
	if name, ok := value.(string); ok {
		return matches(name)
	}
	return false
}

// storedCrossSigningKey rebuilds a verified key from storage, so a stored master
// key can sign keys uploaded later.
func storedCrossSigningKey(row *persist.CrossSigningKey) (*verifiedKey, error) {
	object, err := matrix.DecodeCanonical([]byte(row.Data))
	if err != nil {
		return nil, matrix.ErrInvalidParam("the stored %s key cannot be read: %v", row.Usage, err)
	}
	typed, ok := object.(matrix.Object)
	if !ok {
		return nil, matrix.ErrInvalidParam("the stored %s key is not an object", row.Usage)
	}
	keys, _ := typed["keys"].(matrix.Object)
	public, err := matrix.DecodeEd25519Key(authString(keys, row.KeyID))
	if err != nil {
		return nil, matrix.ErrInvalidParam("the stored %s key is not an ed25519 key: %v", row.Usage, err)
	}
	return &verifiedKey{object: typed, id: row.KeyID, usage: row.Usage, public: public}, nil
}

// authorizeCrossSigning runs the interactive auth exchange in front of a
// cross-signing upload. Only a stage that proves the user controls the account
// is enough here: the keys decide what other clients trust, so an attacker who
// can reach a logged-in session must not be able to replace them.
func (s *Service) authorizeCrossSigning(userID string, auth matrix.Object) error {
	flows := []AuthFlow{{Stages: []string{StagePassword}}}
	session := s.beginAuthSession(flows, authString(auth, "session"))
	defer func() {
		if session.finished() {
			s.dropAuthSession(session)
		}
	}()

	if len(auth) == 0 {
		return s.authChallenge(session, "additional authentication is required to upload cross-signing keys")
	}
	stage := authString(auth, "type")
	switch stage {
	case StagePassword:
		if err := s.checkPasswordAuth(auth, userID); err != nil {
			var merr *matrix.Error
			if errors.As(err, &merr) && merr.HTTPStatus() == http.StatusForbidden {
				return s.authChallenge(session, "the user authentication passed in the request body is incorrect")
			}
			return err
		}
	case "":
		return s.authChallenge(session, "the authentication request did not name a stage")
	default:
		return s.authChallenge(session, "session does not support authentication protocol %s", stage)
	}
	session.complete(stage)
	return nil
}

// CrossSigningKeys returns the cross-signing keys of the listed users, keyed by
// user id and then by key id. POST /keys/query reports them in the same map as
// the device keys, because that is where clients look for them.
func (s *Service) CrossSigningKeys(ctx context.Context, userIDs ...string) (map[string]map[string]matrix.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := map[string]map[string]matrix.Object{}
	for _, userID := range userIDs {
		rows, err := s.db.CrossSigningRepo.List(userID)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		for _, row := range rows {
			var object matrix.Object
			if err := json.Unmarshal([]byte(row.Data), &object); err != nil {
				return nil, matrix.ErrInternal(err)
			}
			if out[userID] == nil {
				out[userID] = map[string]matrix.Object{}
			}
			out[userID][row.KeyID] = object
		}
	}
	return out, nil
}

// SignatureUpload is the body of POST /keys/signatures/upload.
//
// Two shapes arrive here and both have to work. The current one is signed_keys:
// a flat list of key objects, each naming its own user and carrying its own
// signatures. The deprecated one is signed_users: a map of user id to a map of
// entity id to object. matrix-js-sdk and the Rust crypto inside Element send
// signed_keys, so accepting only the old shape quietly throws away a client's
// device signatures and nothing ever trusts the device.
type SignatureUpload struct {
	// SignedUsers maps the user whose keys are signed to the entity (a device id
	// or a cross-signing key id) and the object holding its new signatures.
	SignedUsers map[string]map[string]matrix.Object
	// SignedKeys holds key objects in the order a client sent them.
	SignedKeys []matrix.Object
}

// entities returns the user id, entity id and object triples either shape of the
// request describes.
func (u SignatureUpload) entities(requester string) []signatureTarget {
	out := make([]signatureTarget, 0, len(u.SignedUsers)+len(u.SignedKeys))
	for userID, objects := range u.SignedUsers {
		for entityID, object := range objects {
			out = append(out, signatureTarget{UserID: userID, EntityID: entityID, Object: object})
		}
	}
	for _, object := range u.SignedKeys {
		userID, _ := object["user_id"].(string)
		if userID == "" {
			userID = requester
		}
		entityID := signatureEntityID(object)
		if entityID == "" {
			continue
		}
		out = append(out, signatureTarget{UserID: userID, EntityID: entityID, Object: object})
	}
	return out
}

// signatureEntityID names the key a signed_keys object describes: the device id
// for a device key, and the single key id for a cross-signing key (which carries
// no device id).
func signatureEntityID(object matrix.Object) string {
	if deviceID, ok := object["device_id"].(string); ok && deviceID != "" {
		return deviceID
	}
	keys, _ := object["keys"].(map[string]any)
	for keyID := range keys {
		return keyID
	}
	return ""
}

// signatureTarget is one key whose signatures are being extended.
type signatureTarget struct {
	UserID   string
	EntityID string
	Object   matrix.Object
}

// UploadKeySignatures merges signatures into stored device and cross-signing
// keys. This is how clients record that they verified each other, which is what
// makes the shield in a client turn grey instead of red.
//
// A signature made with a key Lattice has is verified; one made by a key it does
// not have is stored as it came, because the clients that read it hold the key
// and check it themselves. Entities nobody has are skipped: a signature for a
// device that was deleted is not an error.
func (s *Service) UploadKeySignatures(ctx context.Context, requester string, upload SignatureUpload) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, target := range upload.entities(requester) {
		if err := s.mergeSignatures(ctx, requester, target.UserID, target.EntityID, target.Object); err != nil {
			return err
		}
	}
	return s.recordDeviceListChange(ctx, persist.DeviceListChanged, requester)
}

// mergeSignatures adds the signatures of one incoming object to the stored
// object of the same entity.
func (s *Service) mergeSignatures(ctx context.Context, requester, userID, entityID string, incoming matrix.Object) error {
	stored, err := s.storedKeyObject(ctx, userID, entityID)
	if err != nil {
		// Unknown entity: nothing to sign.
		return nil
	}
	if stored == nil {
		return nil
	}
	signatures, _ := incoming["signatures"].(matrix.Object)
	if len(signatures) == 0 {
		return nil
	}
	existing, _ := stored["signatures"].(matrix.Object)
	if existing == nil {
		existing = matrix.Object{}
	}
	for signer, byKey := range signatures {
		entries, ok := byKey.(matrix.Object)
		if !ok {
			continue
		}
		for keyID, value := range entries {
			signature, ok := value.(string)
			if !ok {
				continue
			}
			public, found := s.lookupPublicKey(ctx, signer, keyID)
			if found {
				probe := matrix.Object{"signatures": matrix.Object{signer: matrix.Object{keyID: signature}}}
				if err := matrix.VerifyEd25519Signature(mergeForProbe(stored, probe), signer, keyID, public); err != nil {
					return matrix.ErrInvalidParam("the signature under %s from %s is not valid: %v", keyID, signer, err)
				}
			}
			byExisting, _ := existing[signer].(matrix.Object)
			if byExisting == nil {
				byExisting = matrix.Object{}
			}
			byExisting[keyID] = signature
			existing[signer] = byExisting
		}
	}
	stored["signatures"] = existing
	return s.storeKeyObject(ctx, userID, entityID, stored)
}

// mergeForProbe puts a single signature onto a copy of the object that is to be
// verified, which is what matrix.VerifyEd25519Signature needs.
func mergeForProbe(stored, probe matrix.Object) matrix.Object {
	out := matrix.Object{}
	for key, value := range stored {
		if key == "signatures" {
			continue
		}
		out[key] = value
	}
	out["signatures"] = probe["signatures"]
	return out
}

// storedKeyObject returns the stored object of a device or cross-signing key.
func (s *Service) storedKeyObject(ctx context.Context, userID, entityID string) (matrix.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row, err := s.db.CrossSigningRepo.Get(userID, entityID)
	if err == nil {
		object, err := decodeJSONObject(row.Data)
		if err != nil {
			return nil, err
		}
		return object, nil
	}
	if !errors.Is(err, persist.ErrNoKey) {
		return nil, matrix.ErrInternal(err)
	}
	device, err := s.db.DeviceKeyRepo.Get(userID, entityID)
	if err != nil {
		if errors.Is(err, persist.ErrNoKey) {
			return nil, nil
		}
		return nil, matrix.ErrInternal(err)
	}
	return decodeJSONObject(device.KeyJSON)
}

// storeKeyObject writes an updated object back where it came from.
func (s *Service) storeKeyObject(ctx context.Context, userID, entityID string, object matrix.Object) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	if _, err := s.db.CrossSigningRepo.Get(userID, entityID); err == nil {
		return s.db.CrossSigningRepo.Set(&persist.CrossSigningKey{
			UserID:      userID,
			KeyID:       entityID,
			Usage:       usageOf(object),
			Data:        string(encoded),
			FirstSeenMS: s.NowMS(),
		})
	} else if !errors.Is(err, persist.ErrNoKey) {
		return matrix.ErrInternal(err)
	}
	device, err := s.db.DeviceKeyRepo.Get(userID, entityID)
	if err != nil {
		if errors.Is(err, persist.ErrNoKey) {
			return nil
		}
		return matrix.ErrInternal(err)
	}
	device.KeyJSON = string(encoded)
	return s.db.DeviceKeyRepo.Store(device)
}

// lookupPublicKey returns the public key a signature claims to come from, when
// Lattice has the key that goes with it. Both cross-signing keys and device keys
// are searched, because a signature can be made by either.
func (s *Service) lookupPublicKey(ctx context.Context, userID, keyID string) (ed25519.PublicKey, bool) {
	if err := ctx.Err(); err != nil {
		return nil, false
	}
	if row, err := s.db.CrossSigningRepo.Get(userID, keyID); err == nil {
		if object, err := decodeJSONObject(row.Data); err == nil {
			if keys, ok := object["keys"].(matrix.Object); ok {
				if public, err := matrix.DecodeEd25519Key(authString(keys, keyID)); err == nil {
					return public, true
				}
			}
		}
	}
	if device, err := s.db.DeviceKeyRepo.Get(userID, keyID); err == nil {
		if object, err := decodeJSONObject(device.KeyJSON); err == nil {
			if keys, ok := object["keys"].(matrix.Object); ok {
				if public, err := matrix.DecodeEd25519Key(authString(keys, keyID)); err == nil {
					return public, true
				}
			}
		}
	}
	return nil, false
}

// usageOf returns the first usage a key object declares, which is all the stored
// row needs.
func usageOf(object matrix.Object) string {
	switch typed := object["usage"].(type) {
	case []string:
		if len(typed) > 0 {
			return typed[0]
		}
	case []any:
		if len(typed) > 0 {
			if name, ok := typed[0].(string); ok {
				return name
			}
		}
	}
	return ""
}

// decodeJSONObject decodes a stored object.
func decodeJSONObject(raw string) (matrix.Object, error) {
	var object matrix.Object
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return object, nil
}

// DropCrossSigning removes the cross-signing keys of an account.
func (s *Service) DropCrossSigning(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.db.CrossSigningRepo.DeleteAll(userID); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}
