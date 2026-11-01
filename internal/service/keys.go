package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// End-to-end key algorithms Lattice knows about. The keys themselves are opaque
// to the homeserver: it stores the JSON a client uploads and hands it back to
// other devices, while the clients do all of the cryptography and signature
// checking. That is what makes unverified end-to-end encryption work against a
// server that never touches a key.
const (
	KeyAlgorithmCurve25519         = "curve25519"
	KeyAlgorithmSignedCurve25519   = "signed_curve25519"
	KeyAlgorithmFallbackCurve25519 = "fallback_curve25519"
)

// KeyUploadParams captures the fields of POST /keys/upload.
type KeyUploadParams struct {
	UserID   string
	DeviceID string
	// DeviceKey is the device_key object, stored verbatim.
	DeviceKey matrix.Object
	// OneTimeKeys and FallbackKeys are the raw key maps of the request, keyed by
	// "algorithm:counter".
	OneTimeKeys  map[string]matrix.Object
	FallbackKeys map[string]matrix.Object
}

// KeyUploadResult is the response of POST /keys/upload: how many keys of each
// algorithm the device now has available, which is all a client reads from it.
type KeyUploadResult struct {
	OneTimeKeyCounts map[string]int64 `json:"one_time_key_counts"`
}

// UploadKeys stores the key material of a device.
//
// Re-uploading the identity key replaces it. A fresh batch of one-time keys is
// **added** to the ones the device still has on offer: a client counts the keys
// the server holds and uploads enough to reach its target, so replacing the pool
// instead of adding to it keeps that count below the target forever and the
// client uploads on every sync. Only a changed identity key records a device list
// change: one-time key uploads are frequent and their effect is visible in the
// counts a client gets back, so they must not wake every sync.
func (s *Service) UploadKeys(ctx context.Context, params KeyUploadParams) (*KeyUploadResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := s.db.DeviceRepo.Get(params.UserID, params.DeviceID); err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrInvalidParam("device %q does not exist", params.DeviceID)
		}
		return nil, matrix.ErrInternal(err)
	}

	if len(params.DeviceKey) > 0 {
		if err := s.storeDeviceKey(ctx, params); err != nil {
			return nil, err
		}
	}
	if err := s.storeOneTimeKeys(params, params.OneTimeKeys, false); err != nil {
		return nil, err
	}
	if err := s.storeOneTimeKeys(params, params.FallbackKeys, true); err != nil {
		return nil, err
	}
	counts, err := s.db.OneTimeKeyRepo.Counts(params.UserID, params.DeviceID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return &KeyUploadResult{OneTimeKeyCounts: counts}, nil
}

// storeDeviceKey keeps the identity key and tells the world about it, which is
// what makes other devices re-query this device.
func (s *Service) storeDeviceKey(ctx context.Context, params KeyUploadParams) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(params.DeviceKey)
	if err != nil {
		return matrix.ErrBadJSON(err)
	}
	pos := s.nextStreamPos()
	key := &persist.DeviceKey{
		UserID:    params.UserID,
		DeviceID:  params.DeviceID,
		KeyJSON:   string(raw),
		StreamPos: int64(pos),
	}
	if err := s.db.DeviceKeyRepo.Store(key); err != nil {
		return matrix.ErrInternal(err)
	}
	change := &persist.DeviceListChange{
		UserID:    params.UserID,
		DeviceID:  params.DeviceID,
		Kind:      persist.DeviceListChanged,
		StreamPos: int64(pos),
		TS:        s.NowMS(),
	}
	if err := s.db.DeviceListRepo.Append(change); err != nil {
		return matrix.ErrInternal(err)
	}
	s.Notify()
	return nil
}

// MaxOneTimeKeys is how many one-time keys a device may have on offer at once.
// Every known client tops its pool up to a number of this size - matrix-js-sdk
// and the Rust crypto stack both use 100 - so more than this only lets a looping
// or buggy client fill the database. A batch that does not fit is accepted as far
// as the ceiling and the surplus is dropped: the response reports the counts, and
// the client sees its target met.
const MaxOneTimeKeys = 100

// storeOneTimeKeys stores one batch of prekeys. An empty batch is a no-op:
// clients repeat the same upload on every start, and wiping the keys a device
// still has on offer would take away the keys other devices are about to claim.
//
// A key object is stored exactly as it arrived, because the claiming device
// verifies the signature the device made over that very object. A server that
// rewrites it on the way in - renaming the field, dropping the signatures -
// hands out a key no client can verify: the Olm session that should follow never
// opens and the room keys never arrive, which looks like a broken recipient.
// Which field name carries the key is the client's business: the specification
// spells the value both as "key" and as the algorithm name, and clients differ
// about which they upload.
func (s *Service) storeOneTimeKeys(params KeyUploadParams, keys map[string]matrix.Object, fallback bool) error {
	if len(keys) == 0 {
		return nil
	}
	nowMS := s.NowMS()
	stored := make([]*persist.OneTimeKey, 0, len(keys))
	for keyID, key := range keys {
		algorithm := algorithmOfKeyID(keyID)
		if algorithm == "" {
			return matrix.ErrInvalidParam("one-time key %q does not name an algorithm", keyID)
		}
		raw, err := json.Marshal(key)
		if err != nil {
			return matrix.ErrBadJSON(err)
		}
		stored = append(stored, &persist.OneTimeKey{
			UserID:    params.UserID,
			DeviceID:  params.DeviceID,
			KeyID:     keyID,
			Algorithm: algorithm,
			Fallback:  fallback,
			KeyJSON:   string(raw),
			StoredTS:  nowMS,
		})
	}
	sort.Slice(stored, func(i, j int) bool { return stored[i].KeyID < stored[j].KeyID })
	if fallback {
		// MSC3288: a fresh fallback key replaces the one still outstanding.
		if err := s.db.OneTimeKeyRepo.ReplaceFallback(params.UserID, params.DeviceID, stored); err != nil {
			return matrix.ErrInternal(err)
		}
		return nil
	}
	fitting, err := fitWithinCeiling(s.db.OneTimeKeyRepo.Counts, params.UserID, params.DeviceID, stored)
	if err != nil {
		return err
	}
	if err := s.db.OneTimeKeyRepo.Add(fitting); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// fitWithinCeiling trims a batch to what the device may still have on offer,
// counted per algorithm. counts is injected so the ceiling can be tested without
// a database.
func fitWithinCeiling(counts func(userID, deviceID string) (map[string]int64, error),
	userID, deviceID string, keys []*persist.OneTimeKey) ([]*persist.OneTimeKey, error) {
	held, err := counts(userID, deviceID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	added := map[string]int64{}
	out := make([]*persist.OneTimeKey, 0, len(keys))
	for _, key := range keys {
		if held[key.Algorithm]+added[key.Algorithm] >= MaxOneTimeKeys {
			continue
		}
		added[key.Algorithm]++
		out = append(out, key)
	}
	return out, nil
}

// algorithmOfKeyID splits the algorithm off a key id of the form
// "signed_curve25519:AAAAAQ".
func algorithmOfKeyID(keyID string) string {
	algorithm, _, found := strings.Cut(keyID, ":")
	if !found || algorithm == "" {
		return ""
	}
	return algorithm
}

// KeyQueryResult is the response of POST /keys/query.
type KeyQueryResult struct {
	DeviceKeys map[string]map[string]matrix.Object `json:"device_keys"`
	Failures   matrix.Object                       `json:"failures"`
	// MasterKeys, SelfSigningKeys and UserSigningKeys hold the cross-signing keys,
	// one map per usage. They are separate from DeviceKeys because they are not
	// devices: a cross-signing object has no device_id, and a client that parses a
	// device entry expects one. A response that mixes the two is rejected by the
	// Rust crypto stack, which then reports that keys could not be set up.
	MasterKeys      map[string]matrix.Object `json:"master_keys,omitempty"`
	SelfSigningKeys map[string]matrix.Object `json:"self_signing_keys,omitempty"`
	// UserSigningKeys only carries the requesting user's own key, as the
	// specification asks: another user's user-signing key is not needed to decide
	// whether their devices are trusted.
	UserSigningKeys map[string]matrix.Object `json:"user_signing_keys,omitempty"`
}

// QueryKeys returns the published device keys of the requested users. An empty
// device list for a user asks for every device of that user. The requester is
// needed because their own user-signing key is the only one reported.
func (s *Service) QueryKeys(ctx context.Context, requester string, wanted map[string][]string) (*KeyQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	userIDs := make([]string, 0, len(wanted))
	for userID := range wanted {
		userIDs = append(userIDs, userID)
	}
	result := &KeyQueryResult{
		DeviceKeys: map[string]map[string]matrix.Object{},
		Failures:   matrix.Object{},
	}
	if len(userIDs) == 0 {
		return result, nil
	}
	keys, err := s.db.DeviceKeyRepo.List(userIDs...)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	for _, key := range keys {
		if limited := wanted[key.UserID]; len(limited) > 0 && !containsString(limited, key.DeviceID) {
			continue
		}
		var object matrix.Object
		if err := json.Unmarshal([]byte(key.KeyJSON), &object); err != nil {
			return nil, matrix.ErrInternal(err)
		}
		if result.DeviceKeys[key.UserID] == nil {
			result.DeviceKeys[key.UserID] = map[string]matrix.Object{}
		}
		result.DeviceKeys[key.UserID][key.DeviceID] = object
	}

	// The cross-signing keys go into the maps the specification names for them.
	result.MasterKeys = map[string]matrix.Object{}
	result.SelfSigningKeys = map[string]matrix.Object{}
	result.UserSigningKeys = map[string]matrix.Object{}
	for _, userID := range userIDs {
		rows, err := s.db.CrossSigningRepo.List(userID)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		for _, row := range rows {
			object, err := decodeJSONObject(row.Data)
			if err != nil {
				return nil, err
			}
			switch row.Usage {
			case CrossSigningMaster:
				result.MasterKeys[userID] = object
			case CrossSigningSelfSigning:
				result.SelfSigningKeys[userID] = object
			case CrossSigningUserSigning:
				if userID == requester {
					result.UserSigningKeys[userID] = object
				}
			}
		}
	}
	// A section a client has nothing to read is left out entirely rather than
	// shipped as an empty map.
	if len(result.MasterKeys) == 0 {
		result.MasterKeys = nil
	}
	if len(result.SelfSigningKeys) == 0 {
		result.SelfSigningKeys = nil
	}
	if len(result.UserSigningKeys) == 0 {
		result.UserSigningKeys = nil
	}
	return result, nil
}

// KeyClaimResult is the response of POST /keys/claim.
type KeyClaimResult struct {
	OneTimeKeys map[string]map[string]map[string]matrix.Object `json:"one_time_keys"`
	Failures    matrix.Object                                  `json:"failures"`
}

// ClaimKeys hands out one one-time key per requested device. A key is marked
// taken by a conditional update, so two concurrent claims never receive the same
// key; when a device has run out of ordinary prekeys its fallback key is handed
// out without being consumed, as MSC3288 asks.
func (s *Service) ClaimKeys(ctx context.Context, request map[string]map[string]string) (*KeyClaimResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &KeyClaimResult{
		OneTimeKeys: map[string]map[string]map[string]matrix.Object{},
		Failures:    matrix.Object{},
	}
	for userID, devices := range request {
		for deviceID, algorithm := range devices {
			if algorithm == "" {
				algorithm = KeyAlgorithmSignedCurve25519
			}
			key, err := s.takeOneTimeKey(userID, deviceID, algorithm)
			if err != nil {
				return nil, err
			}
			if key == nil {
				continue
			}
			if result.OneTimeKeys[userID] == nil {
				result.OneTimeKeys[userID] = map[string]map[string]matrix.Object{}
			}
			if result.OneTimeKeys[userID][deviceID] == nil {
				result.OneTimeKeys[userID][deviceID] = map[string]matrix.Object{}
			}
			result.OneTimeKeys[userID][deviceID][key.KeyID] = oneTimeKeyObject(key)
		}
	}
	return result, nil
}

// takeOneTimeKey claims the oldest available key of a device, falling back to
// the device's fallback key when it has none left.
func (s *Service) takeOneTimeKey(userID, deviceID, algorithm string) (*persist.OneTimeKey, error) {
	available, err := s.db.OneTimeKeyRepo.Available(userID, deviceID, algorithm, false, 1)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	if len(available) > 0 {
		claimed, err := s.db.OneTimeKeyRepo.Claim(available[0].ID, s.NowMS())
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		// The conditional update lost to a concurrent claim: report no key so
		// the client retries instead of reusing a key that is already in use.
		if !claimed {
			return nil, nil
		}
		return available[0], nil
	}
	fallback, err := s.db.OneTimeKeyRepo.Available(userID, deviceID, algorithm, true, 1)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	if len(fallback) == 0 {
		return nil, nil
	}
	return fallback[0], nil
}

// oneTimeKeyObject decodes a stored key back into the JSON shape clients expect.
func oneTimeKeyObject(key *persist.OneTimeKey) matrix.Object {
	var object matrix.Object
	if err := json.Unmarshal([]byte(key.KeyJSON), &object); err != nil {
		// A row that does not decode is a storage fault, not a client error, so
		// surface it as the raw text rather than dropping the key silently.
		return matrix.Object{"stored": key.KeyJSON}
	}
	return object
}

// OneTimeKeyCounts returns the available key counts of a device, which every
// sync reports so a client knows when to upload more keys.
func (s *Service) OneTimeKeyCounts(ctx context.Context, userID, deviceID string) (matrix.DeviceOneTimeCount, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	counts, err := s.db.OneTimeKeyRepo.Counts(userID, deviceID)
	if err != nil {
		return nil, nil, matrix.ErrInternal(err)
	}
	out := matrix.DeviceOneTimeCount{}
	for _, algorithm := range []string{KeyAlgorithmCurve25519, KeyAlgorithmSignedCurve25519, KeyAlgorithmFallbackCurve25519} {
		out[algorithm] = counts[algorithm]
	}
	// MSC3288: tell the client it still has a usable fallback key. The name in
	// device_unused_fallback_key_types is the algorithm the key id was uploaded
	// under, so a key sent as fallback_curve25519:... is reported under that name.
	// Looking only for plain curve25519 - or only for the name Element uses -
	// leaves the section empty and the client never learns it has a fallback key.
	var fallback []string
	for _, algorithm := range []string{KeyAlgorithmFallbackCurve25519, KeyAlgorithmSignedCurve25519} {
		available, err := s.db.OneTimeKeyRepo.Available(userID, deviceID, algorithm, true, 1)
		if err != nil {
			return nil, nil, matrix.ErrInternal(err)
		}
		if len(available) > 0 {
			fallback = append(fallback, algorithm)
		}
	}
	return out, fallback, nil
}

// DeviceListChanges lists the users whose devices changed between two stream
// positions, limited to the people the viewer shares a room with. Both the
// /keys/changes endpoint and the device_lists section of a sync use it.
//
// excludeDevice is the device asking. A change that device caused itself - every
// upload of its own keys records one - is left out: told that its own device list
// changed, a client goes and fetches its own keys back, and a client that uploads
// keys when it does that ends up querying keys over and over. Changes with no
// device behind them (a cross-signing upload) are kept, because a device does
// want to learn about new cross-signing keys for its own user.
//
// A removed device is reported as a change: the specification reserves "left"
// for users who stopped sharing a room, which Lattice derives from membership
// state instead of from this log.
func (s *Service) DeviceListChanges(ctx context.Context, viewer string, from, to uint64, excludeDevice string) (*matrix.DeviceLists, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	changes, err := s.db.DeviceListRepo.Since(int64(from), int64(to), deviceListChangeLimit)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	lists := &matrix.DeviceLists{Changed: []string{}, Left: []string{}}
	seen := map[string]bool{}
	for _, change := range changes {
		if seen[change.UserID] {
			continue
		}
		seen[change.UserID] = true
		if change.UserID == viewer && change.DeviceID != "" && change.DeviceID == excludeDevice {
			continue
		}
		if change.UserID != viewer {
			rooms, err := s.db.MembershipRepo.MutualRooms(viewer, change.UserID)
			if err != nil {
				return nil, matrix.ErrInternal(err)
			}
			if len(rooms) == 0 {
				continue
			}
		}
		lists.Changed = append(lists.Changed, change.UserID)
	}
	if len(lists.Changed) == 0 {
		return nil, nil
	}
	sort.Strings(lists.Changed)
	lists.Left = []string{}
	return lists, nil
}

// deviceListChangeLimit bounds the work one sync does on the change log. A
// client that sleeps for weeks still catches up: the next /keys/query after a
// full sync fetches everything again.
const deviceListChangeLimit = 500

// containsString reports whether needle is in haystack.
func containsString(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

// DropDeviceKeys removes the key material of devices that no longer exist and
// records the devices as changed, which is how a client learns to stop
// encrypting for a device that was deleted.
func (s *Service) DropDeviceKeys(ctx context.Context, userID string, deviceIDs ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.db.DeviceKeyRepo.Delete(userID, deviceIDs...); err != nil {
		return matrix.ErrInternal(err)
	}
	if err := s.db.OneTimeKeyRepo.Delete(userID, deviceIDs...); err != nil {
		return matrix.ErrInternal(err)
	}
	return s.recordDeviceListChange(ctx, persist.DeviceListLeft, userID, deviceIDs...)
}

// recordDeviceListChange appends a device list change for a user and wakes the
// waiting syncs. kind is persist.DeviceListChanged or persist.DeviceListLeft.
func (s *Service) recordDeviceListChange(ctx context.Context, kind, userID string, deviceIDs ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pos := s.nextStreamPos()
	change := &persist.DeviceListChange{
		UserID:    userID,
		Kind:      kind,
		StreamPos: int64(pos),
		TS:        s.NowMS(),
	}
	if len(deviceIDs) > 0 {
		change.DeviceID = deviceIDs[0]
	}
	if err := s.db.DeviceListRepo.Append(change); err != nil {
		return matrix.ErrInternal(err)
	}
	s.Notify()
	return nil
}
