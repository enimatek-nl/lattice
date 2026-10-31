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
// Re-uploading the identity key replaces it, and a fresh batch of one-time keys
// replaces the ones that were never claimed. Only a changed identity key records
// a device list change: one-time key uploads are frequent and their effect is
// visible in the counts a client gets back, so they must not wake every sync.
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

// storeOneTimeKeys normalises and stores one batch of prekeys. An empty batch
// is a no-op: clients repeat the same upload on every start, and wiping the keys
// a device still has available would break in-flight sessions.
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
		raw, err := json.Marshal(normaliseKey(algorithm, key))
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
	if err := s.db.OneTimeKeyRepo.Replace(params.UserID, params.DeviceID, fallback, stored); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
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

// normaliseKey puts a one-time key into the shape the specification uses when it
// hands the key to another device: an object with a single field named after the
// algorithm. Some clients upload the same key under the literal field name
// "key", so that is rewritten rather than rejected.
func normaliseKey(algorithm string, key matrix.Object) matrix.Object {
	if _, ok := key[algorithm]; ok {
		return key
	}
	if value, ok := key["key"]; ok {
		return matrix.Object{algorithm: value}
	}
	if len(key) == 1 {
		for _, value := range key {
			return matrix.Object{algorithm: value}
		}
	}
	return key
}

// KeyQueryResult is the response of POST /keys/query.
type KeyQueryResult struct {
	DeviceKeys map[string]map[string]matrix.Object `json:"device_keys"`
	Failures   matrix.Object                       `json:"failures"`
}

// QueryKeys returns the published device keys of the requested users. An empty
// device list for a user asks for every device of that user.
func (s *Service) QueryKeys(ctx context.Context, wanted map[string][]string) (*KeyQueryResult, error) {
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

	// The cross-signing keys of a user live in the same map, named by their key
	// id instead of a device id. That is where clients look for them, and it is
	// how they learn who this user's identity vouches for.
	crossSigning, err := s.CrossSigningKeys(ctx, userIDs...)
	if err != nil {
		return nil, err
	}
	for userID, byKey := range crossSigning {
		if result.DeviceKeys[userID] == nil {
			result.DeviceKeys[userID] = map[string]matrix.Object{}
		}
		for keyID, object := range byKey {
			result.DeviceKeys[userID][keyID] = object
		}
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
	// MSC3288: tell the client it still has a usable fallback key per algorithm.
	var fallback []string
	for _, algorithm := range []string{KeyAlgorithmSignedCurve25519, KeyAlgorithmCurve25519} {
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
// A removed device is reported as a change: the specification reserves "left"
// for users who stopped sharing a room, which Lattice derives from membership
// state instead of from this log.
func (s *Service) DeviceListChanges(ctx context.Context, viewer string, from, to uint64) (*matrix.DeviceLists, error) {
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
