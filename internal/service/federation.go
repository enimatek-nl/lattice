package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// SigningKey returns the ed25519 keypair this server signs federation requests
// and events with, generating it on first use.
//
// The pair is stored, not derived, and the first one is never replaced behind an
// existing row's back: a peer that verified a signature yesterday looks the same
// key id up today, and a server that mints a fresh pair on each restart invalidates
// every event it signed before the restart. The pair is cached in memory after the
// first read, behind a mutex, so two concurrent first requests cannot each mint a
// key and leave half the room trusting one and half the other.
func (s *Service) SigningKey(ctx context.Context) (federation.KeyPair, error) {
	if err := ctx.Err(); err != nil {
		return federation.KeyPair{}, err
	}
	s.signingKeyMu.Lock()
	defer s.signingKeyMu.Unlock()
	if s.signingKey != nil {
		return *s.signingKey, nil
	}
	row, err := s.db.ServerKeyRepo.Current()
	if persist.IsNotFound(err) {
		row, err = s.generateServerKey()
		if err != nil {
			return federation.KeyPair{}, err
		}
	} else if err != nil {
		return federation.KeyPair{}, matrix.ErrInternal(err)
	}
	pair, err := federation.KeyPairFromStorage(row.KeyID, row.PublicKey, row.PrivateSeed)
	if err != nil {
		// A row that cannot be read back is a storage fault, not a configuration
		// problem: reporting it as one would invite an operator to delete the key
		// and quietly invalidate the server's history.
		return federation.KeyPair{}, matrix.ErrInternal(fmt.Errorf("stored signing key %s: %w", row.KeyID, err))
	}
	s.signingKey = &pair
	return pair, nil
}

// generateServerKey mints and stores the first signing pair.
func (s *Service) generateServerKey() (*persist.ServerKey, error) {
	pair, err := federation.NewKeyPair()
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	row := &persist.ServerKey{
		KeyID:       pair.ID,
		Version:     pair.Version,
		PublicKey:   pair.PublicKey(),
		PrivateSeed: pair.Seed(),
		CreatedTS:   s.NowMS(),
	}
	if err := s.db.ServerKeyRepo.Store(row); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	// A fresh key in a database that already holds events means the previous key is gone -
	// usually a database that was not persisted across a restart. Every event this server
	// has already sent is signed by the lost key, so peers can no longer believe any of
	// them and the rooms this server hosts stop federating. The loss is silent until a peer
	// refuses something, which is why it is said out loud here.
	if events, err := s.db.EventRepo.Count(); err == nil && events > 0 {
		log.Printf("warning: this server generated a new signing key (%s) while its database holds %d events; "+
			"those events are signed by a key this server no longer has, so peers will refuse them - the signing "+
			"key lives in the database, which has to be persisted across restarts", row.KeyID, events)
	}
	// Another process may have won the race and inserted its own pair; re-read so
	// both sides end up with the same answer rather than each using its own.
	stored, err := s.db.ServerKeyRepo.Current()
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return stored, nil
}

// keyValidityMargin keeps a published key document inside the validity window peers
// enforce rather than on its edge. The window a week; the margin is an hour of clock
// disagreement between this server and whoever reads the document.
const keyValidityMargin = time.Hour

// ServerKeysDocument publishes this server's signing key for peers: the object
// served at GET /_matrix/key/v2/server, signed by the key it publishes.
//
// valid_until_ts is set to the far edge of the window the specification allows,
// because this key is never rotated - a shorter window would only make peers
// re-fetch a document that cannot change. The seven day cap is still what peers
// apply, so nothing here outlives the rule.
func (s *Service) ServerKeysDocument(ctx context.Context) (matrix.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := s.SigningKey(ctx)
	if err != nil {
		return nil, err
	}
	validUntil := s.NowMS() + (federation.MaxKeyValidity - keyValidityMargin).Milliseconds()
	document, err := federation.KeyDocument(s.serverName, key, validUntil)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return document, nil
}

// ServerKeysDocumentBody answers GET /_matrix/key/v2/server as bytes, in the
// canonical form the self-signature was made over. The handler writes these
// without re-encoding them: a document that is signed has no room for a byte
// this server added on the way out.
func (s *Service) ServerKeysDocumentBody(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := s.SigningKey(ctx)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	validUntil := s.NowMS() + (federation.MaxKeyValidity - keyValidityMargin).Milliseconds()
	body, err := federation.KeyDocumentBody(s.serverName, key, validUntil)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return body, nil
}

// HasSigningKey reports whether this server publishes a key id, which is what the
// GET /_matrix/key/v2/server/{keyID} variant answers. An unknown id is a plain no:
// the caller must not learn whether the id belongs to some other server, and no
// key material is created or rotated by asking.
func (s *Service) HasSigningKey(ctx context.Context, keyID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if keyID == "" {
		return false, nil
	}
	_, err := s.db.ServerKeyRepo.Get(keyID)
	if persist.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, matrix.ErrInternal(err)
	}
	return true, nil
}

// FederatedDeviceList is the answer to GET /federation/v1/user/devices/{userId}:
// the device keys of a local user, the version marker of that list, and the
// cross-signing keys that tell a peer whom to trust about those devices.
type FederatedDeviceList struct {
	UserID         string
	StreamID       int64
	Devices        matrix.Array
	MasterKey      matrix.Object
	SelfSigningKey matrix.Object
	UserSigningKey matrix.Object
}

// FederatedDevices serves a peer the device keys of a local user.
//
// The stream id is the newest stream position any of the user's device keys
// reached, which is the marker m.device_list_update EDUs correlate with once they
// are implemented (step 11): a peer that has seen that position knows its copy is
// current, and a peer that has not knows it must re-query. Every device key
// publish advances the position, so the number moves exactly when the list does.
//
// The device key objects are handed over unchanged. Their signatures are made by
// the user's own devices, and a homeserver that re-encoded them on the way out
// would break the verification the peer's clients do - the same rule that governs
// the client-facing key endpoints.
func (s *Service) FederatedDevices(ctx context.Context, userID string) (*FederatedDeviceList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.UserExists(ctx, userID) {
		return nil, matrix.ErrNotFound("user %s does not exist", userID)
	}
	list := &FederatedDeviceList{UserID: userID, Devices: matrix.Array{}}

	rows, err := s.db.DeviceKeyRepo.List(userID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	devices, err := s.db.DeviceRepo.List(userID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	displayNames := map[string]string{}
	for _, device := range devices {
		displayNames[device.DeviceID] = device.DisplayName
	}
	for _, row := range rows {
		object, err := decodeJSONObject(row.KeyJSON)
		if err != nil {
			return nil, err
		}
		entry := matrix.Object{"device_id": row.DeviceID, "keys": object}
		if name := displayNames[row.DeviceID]; name != "" {
			entry["device_display_name"] = name
		}
		list.Devices = append(list.Devices, entry)
		if row.StreamPos > list.StreamID {
			list.StreamID = row.StreamPos
		}
	}

	// A user with devices but no published keys still answers, with an empty list
	// and a stream id of zero: the peer learns that there is nothing to encrypt to,
	// which is different from the user not existing at all.
	keys, err := s.db.CrossSigningRepo.List(userID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	for _, row := range keys {
		object, err := decodeJSONObject(row.Data)
		if err != nil {
			return nil, err
		}
		switch row.Usage {
		case CrossSigningMaster:
			list.MasterKey = object
		case CrossSigningSelfSigning:
			list.SelfSigningKey = object
		case CrossSigningUserSigning:
			list.UserSigningKey = object
		}
	}
	return list, nil
}

// Notarise re-signs a peer's key document with this server's key, which is what an
// intermediate notary does: the requester checks both signatures and only has to
// trust its own list of notaries.
func (s *Service) Notarise(ctx context.Context, document matrix.Object) (matrix.Object, error) {
	key, err := s.SigningKey(ctx)
	if err != nil {
		return nil, err
	}
	signed, err := json.Marshal(document)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	var copy matrix.Object
	if err := json.Unmarshal(signed, &copy); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	signature, err := key.Sign(copy)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	signatures, _ := copy["signatures"].(matrix.Object)
	if signatures == nil {
		signatures = matrix.Object{}
	}
	byKey, _ := signatures[s.serverName].(matrix.Object)
	if byKey == nil {
		byKey = matrix.Object{}
	}
	byKey[key.ID] = signature
	signatures[s.serverName] = byKey
	copy["signatures"] = signatures
	return copy, nil
}
