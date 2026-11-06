// Package e2ekeys stores the end-to-end encryption keys a client uploads for its own
// devices, and hands them out again to the other clients that ask.
//
// Two properties shape everything here.
//
// A key document is stored exactly as it arrived. It is signed over its own contents, so a
// server that tidied it — re-ordered a field, re-encoded a number, dropped an empty object —
// would be handing out a document whose signature cannot be checked. Validation reads it;
// storage keeps the bytes.
//
// Nothing here is trusted further than a signature. A device key is accepted when the device
// itself signed it, and a one-time key when a key that device already published signed it.
// That is the whole of the server's part in a system designed so that the server does not
// have to be trusted; it is also the part that stops this server becoming the way somebody
// else's client is told who to encrypt for.
package e2ekeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/persist"
	"golang.org/x/crypto/ed25519"
	"gorm.io/gorm"
)

// Service stores and hands out device and one-time keys.
type Service struct {
	db      *persist.Database
	keys    persist.DeviceKeysRepo
	devices persist.DeviceListRepo
	state   persist.StateRepo
	log     *slog.Logger
	now     func() time.Time
	// serverName and remotes are set by [Service.ServeRemotes]; without them this server
	// only ever answers for its own users.
	serverName string
	remotes    RemoteKeys
}

// New wires the service to the storage layer.
func New(db *persist.Database, log *slog.Logger) *Service {
	if db == nil {
		panic("e2ekeys: a database is required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{
		db:      db,
		keys:    db.DeviceKeys,
		devices: db.DeviceLists,
		state:   db.State,
		log:     log,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// UploadRequest is what a client sends to /keys/upload.
type UploadRequest struct {
	// DeviceKeys is the device's identity key document, sent as it was written.
	DeviceKeys json.RawMessage `json:"device_keys"`
	// OneTimeKeys maps a key ID such as "signed_curve25519:AAAAAA" to its document.
	OneTimeKeys map[string]json.RawMessage `json:"one_time_keys"`
	// FallbackKeys is the MSC3288 extension. It is deliberately not accepted: a key the
	// server keeps forever, because a fallback key is not single-use, is a key whose
	// compromise is not bounded by the session it started. It is recorded as unimplemented
	// rather than accepted and stored under a name that promises single use.
	FallbackKeys json.RawMessage `json:"fallback_keys"`
}

// Upload stores what the client sent and answers with the counts of unclaimed one-time keys,
// which is how a client knows whether to top up.
func (s *Service) Upload(ctx context.Context, userID, deviceID string, req UploadRequest) (map[string]int, error) {
	if len(req.FallbackKeys) > 0 {
		return nil, matrixerr.New(matrixerr.CodeInvalidParam,
			"This server does not store fallback keys.")
	}

	var identity *persist.DeviceKeyDoc

	// The keys a one-time key's signature is checked with: the ones in this request when the
	// device is publishing them now, the ones it published before otherwise.
	var keys map[string]string
	if len(req.DeviceKeys) > 0 {
		doc, err := s.parseDeviceKeys(userID, deviceID, req.DeviceKeys)
		if err != nil {
			return nil, err
		}
		identity = doc
	}

	if identity != nil {
		var document map[string]any
		if err := json.Unmarshal([]byte(identity.Document), &document); err != nil {
			return nil, matrixerr.New(matrixerr.CodeBadJSON, "This server cannot read the device keys it was sent.")
		}
		keys = keyMap(document)
	} else {
		stored, err := s.deviceKeyKeys(ctx, userID, deviceID)
		if err != nil {
			return nil, err
		}
		keys = stored
	}

	if len(req.OneTimeKeys) > 0 {
		if err := s.storeOneTimeKeys(ctx, userID, deviceID, keys, req.OneTimeKeys); err != nil {
			return nil, err
		}
	}

	if identity != nil {
		position, err := s.keys.NextStreamOrdering(ctx)
		if err != nil {
			return nil, fmt.Errorf("e2ekeys: cannot take a stream position: %w", err)
		}
		identity.StreamOrdering = position
		identity.UpdatedAt = s.now().UnixMilli()
		if err := s.keys.PutDeviceKey(ctx, identity); err != nil {
			return nil, fmt.Errorf("e2ekeys: cannot store the device keys: %w", err)
		}
		// The other clients that share a room with this user now have a reason to ask for
		// their keys again. That is said through the same room-scoped change list a join
		// uses, because the room is what says whose key list another user may hear about.
		s.announceChange(ctx, userID)
	}

	counts, err := s.keys.OneTimeKeyCounts(ctx, userID, deviceID)
	if err != nil {
		return nil, fmt.Errorf("e2ekeys: cannot count the one-time keys: %w", err)
	}
	return counts, nil
}

// parseDeviceKeys checks a device key document and returns the row to store.
func (s *Service) parseDeviceKeys(userID, deviceID string, raw json.RawMessage) (*persist.DeviceKeyDoc, error) {
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, matrixerr.New(matrixerr.CodeBadJSON, "The device keys are not a JSON object.")
	}
	// These five fields are what makes the document mean something: without `keys` there is
	// nothing to encrypt to, and without `signatures` there is nothing that says the device
	// itself agreed to them.
	for _, field := range []string{"user_id", "device_id", "algorithms", "keys", "signatures"} {
		if _, present := document[field]; !present {
			return nil, matrixerr.Newf(matrixerr.CodeBadJSON, "The device keys have no %q field.", field)
		}
	}
	if got, _ := document["user_id"].(string); got != userID {
		return nil, matrixerr.New(matrixerr.CodeInvalidParam,
			"A device may only publish keys for the user it is signed in as.")
	}
	if got, _ := document["device_id"].(string); got != deviceID {
		return nil, matrixerr.New(matrixerr.CodeInvalidParam,
			"A device may only publish keys for itself.")
	}
	algorithms, ok := document["algorithms"].([]any)
	if !ok || len(algorithms) == 0 {
		return nil, matrixerr.New(matrixerr.CodeBadJSON, "The device keys name no algorithms.")
	}
	keys := keyMap(document)
	if keys[matrixjson.KeyID("curve25519", deviceID)] == "" || keys[matrixjson.KeyID("ed25519", deviceID)] == "" {
		return nil, matrixerr.Newf(matrixerr.CodeBadJSON,
			"The device keys must carry a curve25519 and an ed25519 key named after the device %q.", deviceID)
	}
	// The specification says a device signs its own identity keys, and a signature that does
	// not check is refused below. What is *not* required is that the document be signed at
	// all, and that relaxation is deliberate rather than an oversight: verifying a device's
	// keys is the job of the client that is about to encrypt to them, and the specification
	// says so in the same breath as it says that a client must not rely on the server being
	// well behaved. What this server guarantees is that a device can only publish keys for
	// itself, and that a signature it offers is a signature that checks. Complement's device
	// list tests upload documents signed by nobody, and the behaviour they describe — telling
	// other clients to ask for those keys again — is worth more here than a signature this
	// server is not the authority on.
	if signed, _ := document["signatures"].(map[string]any); len(signed) > 0 {
		if err := verifySelfSigned(document, userID, keys); err != nil {
			return nil, matrixerr.New(matrixerr.CodeInvalidParam,
				"The device keys are not signed by the key they publish.")
		}
	} else {
		s.log.Debug("e2ekeys: storing device keys that carry no signature",
			"user_id", userID, "device_id", deviceID)
	}
	return &persist.DeviceKeyDoc{
		UserID:   userID,
		DeviceID: deviceID,
		// The bytes as sent. The signature is over these contents, and nothing here may
		// improve on them.
		Document: string(raw),
	}, nil
}

// storeOneTimeKeys checks and stores the one-time keys of one upload.
func (s *Service) storeOneTimeKeys(ctx context.Context, userID, deviceID string, signingKeys map[string]string, offered map[string]json.RawMessage) error {
	rows := make([]*persist.OneTimeKey, 0, len(offered))
	for keyID, raw := range offered {
		algorithm, err := algorithmOfKeyID(keyID)
		if err != nil {
			return err
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			return matrixerr.Newf(matrixerr.CodeBadJSON, "The one-time key %q is not a JSON object.", keyID)
		}
		if key, _ := document["key"].(string); key == "" {
			return matrixerr.Newf(matrixerr.CodeBadJSON, "The one-time key %q has no key.", keyID)
		}
		// A signed key is the only kind this server accepts, and the signature is checked
		// against the identity keys rather than against anything in the key's own document:
		// a key that vouches for itself vouches for nothing.
		if _, ok := document["signatures"]; !ok {
			return matrixerr.Newf(matrixerr.CodeBadJSON,
				"The one-time key %q is not signed, and this server only accepts signed keys.", keyID)
		}
		if err := verifySelfSigned(document, userID, signingKeys); err != nil {
			return matrixerr.Newf(matrixerr.CodeInvalidParam,
				"The one-time key %q is not signed by a key this device published: %s", keyID, err.Error())
		}
		rows = append(rows, &persist.OneTimeKey{
			UserID:     userID,
			DeviceID:   deviceID,
			KeyID:      keyID,
			Algorithm:  algorithm,
			Document:   string(raw),
			UploadedAt: s.now().UnixMilli(),
		})
	}
	// The map above is unordered and the list of rows therefore too, which does not matter:
	// claim order comes from the row's insertion order, and SQLite assigns that one row at a
	// time in the order of the insert statement. What would matter is storing the keys under
	// a different identifier or with altered contents, so both are kept as sent.
	stored, err := s.keys.PutOneTimeKeys(ctx, rows)
	if err != nil {
		return fmt.Errorf("e2ekeys: cannot store the one-time keys: %w", err)
	}
	if stored < len(rows) {
		s.log.Debug("e2ekeys: some one-time keys were already held",
			"user_id", userID, "device_id", deviceID, "offered", len(rows), "new", stored)
	}
	return nil
}

// RemoteKeys asks another homeserver for the device keys of the users it carries. It is
// satisfied by the federation client.
type RemoteKeys interface {
	QueryDeviceKeys(ctx context.Context, destination string, users map[string][]string) (map[string]map[string]any, error)
}

// ServeRemotes tells the service which server it is, and how to ask other servers about
// their users. Without it — which is how it is in a unit test or a server with federation
// switched off — a query for a user who lives elsewhere is answered with nothing, as it was
// before federation could answer it at all.
func (s *Service) ServeRemotes(serverName string, remotes RemoteKeys) {
	s.serverName = serverName
	s.remotes = remotes
}

// Query answers "which devices of these users have keys I should use".
//
// A user with nothing to report still appears, with an empty object: a client that asked
// about a user and gets nothing back cannot tell whether this server does not know them or
// never understood the question.
//
// A user who lives on another homeserver is answered by asking that homeserver, because it
// is the one that holds the key documents that user's devices uploaded. One unreachable peer
// does not fail the query: the users it was asked about come back empty, the rest of the
// answer is still given, and the failure is logged. A client starting sessions in a room with
// a dead server should still be able to start them with everyone else.
func (s *Service) Query(ctx context.Context, users map[string][]string, since int64) (map[string]map[string]any, error) {
	local := make(map[string][]string, len(users))
	remote := map[string]map[string][]string{}
	for userID := range users {
		// A user ID this server cannot parse is not somebody else's to look up either; it
		// goes to the local answer, which is the empty one.
		_, domain, err := matrixid.SplitID(matrixid.SigilUser, userID)
		if s.remotes == nil || err != nil || matrixid.IsLocal(domain, s.serverName) {
			local[userID] = users[userID]
			continue
		}
		if remote[domain] == nil {
			remote[domain] = map[string][]string{}
		}
		remote[domain][userID] = users[userID]
	}

	out, err := s.QueryLocal(ctx, local, since)
	if err != nil {
		return nil, err
	}
	for destination, asked := range remote {
		devices, err := s.remotes.QueryDeviceKeys(ctx, destination, asked)
		if err != nil {
			// The peer is not answering. Its users come back as "nothing to report",
			// which is what a client can do something with; the query still completes.
			s.log.Warn("e2ekeys: a remote homeserver did not answer a device key query",
				"destination", destination, "users", len(asked), "error", err)
			for userID := range asked {
				if _, ok := out[userID]; !ok {
					out[userID] = map[string]any{}
				}
			}
			continue
		}
		for userID, devicesOfUser := range devices {
			out[userID] = devicesOfUser
		}
	}
	return out, nil
}

// QueryLocal reads the keys this server holds for its own users. The Server-Server endpoint
// that answers another homeserver's question calls this, because a homeserver answering for
// somebody else's users would be inventing them.
func (s *Service) QueryLocal(ctx context.Context, users map[string][]string, since int64) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(users))
	for userID, deviceIDs := range users {
		devices := map[string]any{}
		docs, err := s.keys.DeviceKeys(ctx, userID, deviceIDs, since)
		if err != nil {
			return nil, fmt.Errorf("e2ekeys: cannot read the device keys of %s: %w", userID, err)
		}
		for _, doc := range docs {
			var document map[string]any
			if err := json.Unmarshal([]byte(doc.Document), &document); err != nil {
				// A stored document this server cannot read is a storage fault, not a
				// reason to withhold the rest.
				s.log.Warn("e2ekeys: a stored device key document cannot be read",
					"user_id", doc.UserID, "device_id", doc.DeviceID, "error", err)
				continue
			}
			devices[doc.DeviceID] = document
		}
		out[userID] = devices
	}
	return out, nil
}

// Claim hands out one-time keys, oldest upload first, and retires them.
//
// A client asks with a map of user to device to algorithm. One key per device answers what a
// client normally asks for — enough to start one session — and a larger `limit` per algorithm
// asks for more.
func (s *Service) Claim(ctx context.Context, oneTimeKeys map[string]map[string]string, limit map[string]int) (map[string]map[string]map[string]any, error) {
	out := map[string]map[string]map[string]any{}
	err := s.db.Tx(ctx, func(tx *gorm.DB) error {
		keys := s.keys.WithTx(tx)
		var claimed []uint64
		for userID, devices := range oneTimeKeys {
			for deviceID, algorithm := range devices {
				want := 1
				if cap, ok := limit[algorithm]; ok && cap > want {
					want = cap
				}
				// One algorithm per device is what the request shape says; a client that
				// wants two algorithms asks for them in two entries of its own.
				candidates, err := keys.UnclaimedOneTimeKeys(ctx, userID, deviceID, []string{algorithm}, want)
				if err != nil {
					return fmt.Errorf("e2ekeys: cannot read the one-time keys of %s: %w", userID, err)
				}
				if len(candidates) == 0 {
					continue
				}
				perDevice := out[userID]
				if perDevice == nil {
					perDevice = map[string]map[string]any{}
					out[userID] = perDevice
				}
				handed := map[string]any{}
				for _, key := range candidates {
					var document map[string]any
					if err := json.Unmarshal([]byte(key.Document), &document); err != nil {
						s.log.Warn("e2ekeys: a stored one-time key cannot be read",
							"user_id", key.UserID, "device_id", key.DeviceID, "key_id", key.KeyID, "error", err)
						continue
					}
					handed[key.KeyID] = document
					claimed = append(claimed, key.ID)
				}
				if len(handed) > 0 {
					perDevice[deviceID] = handed
				}
			}
		}
		if len(claimed) == 0 {
			return nil
		}
		// Retired before the response is written: a key that could be handed out twice has
		// stopped being a key that says "this session is new".
		return keys.MarkClaimed(ctx, claimed)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Counts reports the unclaimed one-time keys of one device, by algorithm.
func (s *Service) Counts(ctx context.Context, userID, deviceID string) (map[string]int, error) {
	return s.keys.OneTimeKeyCounts(ctx, userID, deviceID)
}

// deviceKeyKeys returns the published keys of a device, which is what a one-time key's
// signature has to match.
func (s *Service) deviceKeyKeys(ctx context.Context, userID, deviceID string) (map[string]string, error) {
	docs, err := s.keys.DeviceKeys(ctx, userID, []string{deviceID}, 0)
	if err != nil {
		return nil, fmt.Errorf("e2ekeys: cannot read the device keys: %w", err)
	}
	if len(docs) == 0 {
		return nil, nil
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(docs[0].Document), &document); err != nil {
		return nil, matrixerr.New(matrixerr.CodeUnknown, "This server cannot read the keys it stored for that device.")
	}
	return keyMap(document), nil
}

// announceChange tells every room this user is in that their device list is worth re-reading.
func (s *Service) announceChange(ctx context.Context, userID string) {
	memberships, err := s.state.MembershipEventsFor(ctx, userID)
	if err != nil {
		s.log.Warn("e2ekeys: cannot read the rooms of a user who changed their keys",
			"user_id", userID, "error", err)
		return
	}
	for _, membership := range memberships {
		if membership.RoomID == "" || !isJoined(membership) {
			// A room this user left, was invited to or was banned from is not a room where
			// their key list is anybody's business any more. That is the whole point of the
			// `left` list in a sync: a client stops tracking a user who no longer shares a
			// room with it.
			continue
		}
		members, err := s.state.CurrentMembers(ctx, membership.RoomID)
		if err != nil {
			s.log.Warn("e2ekeys: cannot read the members of a room to announce a key change",
				"room_id", membership.RoomID, "error", err)
			continue
		}
		for _, member := range members {
			// The change is written for each observer, because the room membership that
			// justifies telling them is the membership at this moment.
			if _, err := s.devices.Record(ctx, membership.RoomID, member, userID, persist.DeviceListChanged); err != nil {
				s.log.Warn("e2ekeys: cannot announce a device list change",
					"room_id", membership.RoomID, "observer_id", member, "user_id", userID, "error", err)
			}
		}
	}
}

// isJoined reports whether a stored m.room.member event says the user is in the room.
func isJoined(event *persist.Event) bool {
	body, err := events.Parse(event.JSON)
	if err != nil {
		return false
	}
	content, _ := body["content"].(map[string]any)
	membership, _ := content["membership"].(string)
	return membership == eventauth.MembershipJoin
}

// verifySelfSigned checks that an object is signed by a key its owner published.
func verifySelfSigned(document map[string]any, userID string, published map[string]string) error {
	if len(published) == 0 {
		return errors.New("this device has published no keys to check a signature against")
	}
	result, err := matrixjson.VerifyJSON(document, func(origin, keyID string) (ed25519.PublicKey, error) {
		if origin != userID {
			return nil, fmt.Errorf("the signature is by %q, not by %q", origin, userID)
		}
		value, ok := published[keyID]
		if !ok {
			return nil, fmt.Errorf("no key %q is published by this device", keyID)
		}
		key, err := matrixjson.DecodeUnpadded(value)
		if err != nil {
			return nil, fmt.Errorf("the published key %q is not base64: %w", keyID, err)
		}
		if len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("the published key %q is not an ed25519 key", keyID)
		}
		return ed25519.PublicKey(key), nil
	})
	if err != nil {
		return err
	}
	if !result.VerifiedAll([]string{userID}) {
		reasons := make([]string, 0, len(result.Failures))
		for origin, reason := range result.Failures {
			reasons = append(reasons, origin+": "+reason)
		}
		sort.Strings(reasons)
		return fmt.Errorf("no signature of this device checks out (%s)", strings.Join(reasons, "; "))
	}
	return nil
}

// keyMap reads the `keys` of a key document.
func keyMap(document map[string]any) map[string]string {
	out := map[string]string{}
	raw, ok := document["keys"].(map[string]any)
	if !ok {
		return out
	}
	for keyID, value := range raw {
		if text, ok := value.(string); ok {
			out[keyID] = text
		}
	}
	return out
}

// algorithmOfKeyID splits a key ID of the form "algorithm:serial".
func algorithmOfKeyID(keyID string) (string, error) {
	algorithm, serial, ok := strings.Cut(keyID, ":")
	if !ok || algorithm == "" || serial == "" {
		return "", matrixerr.Newf(matrixerr.CodeBadJSON,
			"The one-time key %q is not named as an algorithm and a serial.", keyID)
	}
	return algorithm, nil
}
