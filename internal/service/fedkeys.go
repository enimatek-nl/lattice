package service

import (
	"context"
	"fmt"
	"log"
	"net/url"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// QueryKeys answers a client asking what the devices of a list of users look like - which
// is a question about several servers at once. This server's own users are read from its own
// rows; every other user is asked of the server their name names, and the answer is kept so
// that the next query for the same person costs nothing.
//
// A user whose server does not answer is reported in the failures section rather than making
// the whole query fail: a client encrypting for a room full of people needs the keys it can
// get, and needs to know which people it did not get them for.
func (s *Service) QueryKeys(ctx context.Context, requester string, wanted map[string][]string) (*KeyQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	own, remote, invalid := s.splitKeyQuery(wanted)
	result, err := s.localKeyQuery(ctx, requester, own)
	if err != nil {
		return nil, err
	}
	for _, userID := range invalid {
		if result.Failures == nil {
			result.Failures = matrix.Object{}
		}
		// The name is not one this server holds and not one that addresses a server, so
		// there is nobody to ask. Saying so is better than an empty answer, which a client
		// reads as "this person has no devices".
		result.Failures[userID] = map[string]string{"error": fmt.Sprintf("%s does not name a server this server can ask", userID)}
	}
	if len(remote) == 0 {
		pruneEmptyKeyMaps(result)
		return result, nil
	}
	if result.Failures == nil {
		result.Failures = matrix.Object{}
	}
	for server, users := range remote {
		answer, err := s.remoteKeyQuery(ctx, server, users)
		if err != nil {
			log.Printf("federation: asking %s for the device keys of %d user(s): %v", server, len(users), err)
			// A peer that is down today is not a reason to leave a client without the devices
			// that peer told this server about yesterday. The device list updates a peer sends
			// keep a copy here precisely so that encryption can go on through an outage; the
			// copy is stale by however long the peer has been unreachable, and saying so in the
			// log is the honest part. A server is only reported as a failure when this server
			// has nothing at all to answer with for the people asked about.
			answered := 0
			for userID := range users {
				devices, err := s.cachedRemoteDevices(userID)
				if err != nil || len(devices) == 0 {
					continue
				}
				result.DeviceKeys[userID] = devices
				answered++
			}
			if answered < len(users) {
				result.Failures[server] = map[string]string{"error": err.Error()}
			}
			continue
		}
		mergeKeyQuery(result, answer)
	}
	// The cross-signing maps of the answer may have been filled by the remote half alone,
	// and an empty map is left out rather than shipped.
	pruneEmptyKeyMaps(result)
	s.annotateDeviceDisplayNames(result)
	return result, nil
}

// annotateDeviceDisplayNames puts each device's human-readable name into the unsigned block of
// its key object, which is where the specification puts it and where clients look for it.
//
// The block is unsigned on purpose. A device's signature covers its keys; the name stuck to the
// device is chosen by whoever runs the server, and a client that showed a name as signed would be
// treating an operator's label as though the user had vouched for it.
//
// A key object that already carries a name keeps it. That is not deference: the answer from
// another server was built by the only party that knows its own users' devices, and overwriting
// what it chose with a cached copy of the same fact is how two servers end up disagreeing.
func (s *Service) annotateDeviceDisplayNames(result *KeyQueryResult) {
	for userID, devices := range result.DeviceKeys {
		// The names come from the device table for one of our users and from the cache for
		// somebody else's; both are a device id to a label.
		names := map[string]string{}
		if s.LocalUser(userID) {
			rows, err := s.db.DeviceRepo.List(userID)
			if err == nil {
				for _, row := range rows {
					names[row.DeviceID] = row.DisplayName
				}
			}
		} else if rows, err := s.db.RemoteDeviceRepo.ForUser(userID); err == nil {
			for _, row := range rows {
				names[row.DeviceID] = row.DisplayName
			}
		}
		for deviceID, object := range devices {
			name, ok := names[deviceID]
			if !ok || name == "" {
				continue
			}
			unsigned, _ := asObject(object["unsigned"])
			if unsigned == nil {
				unsigned = matrix.Object{}
			}
			if existing, ok := unsigned["device_display_name"].(string); ok && existing != "" {
				continue
			}
			unsigned["device_display_name"] = name
			object["unsigned"] = unsigned
		}
	}
}

// ClaimKeys hands out one-time keys for the requested devices. The devices of this server are
// taken from its own store; the rest are claimed from the server that holds them, which is
// the only place that can mark a one-time key as spent. A claim answered locally for a remote
// device would hand out a key that nobody can decrypt with, and would leave the real device
// with a key it believes is still unused.
func (s *Service) ClaimKeys(ctx context.Context, request map[string]map[string]string) (*KeyClaimResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	own, remote, invalid := s.splitKeyClaim(request)
	result, err := s.localKeyClaim(ctx, own)
	if err != nil {
		return nil, err
	}
	for _, userID := range invalid {
		if result.Failures == nil {
			result.Failures = matrix.Object{}
		}
		result.Failures[userID] = map[string]string{"error": fmt.Sprintf("%s does not name a server this server can ask", userID)}
	}
	if len(remote) == 0 {
		return result, nil
	}
	if result.Failures == nil {
		result.Failures = matrix.Object{}
	}
	for server, devices := range remote {
		answer, err := s.remoteKeyClaim(ctx, server, devices)
		if err != nil {
			log.Printf("federation: asking %s for one-time keys of %d user(s): %v", server, len(devices), err)
			result.Failures[server] = map[string]string{"error": err.Error()}
			continue
		}
		for userID, devices := range answer.OneTimeKeys {
			result.OneTimeKeys[userID] = devices
		}
	}
	return result, nil
}

// ServeKeyQuery answers POST /_matrix/federation/v1/user/keys/query for a peer: the device
// keys and cross-signing keys this server publishes for the users it was asked about.
//
// Only this server's own users are answered. A device list is published by the server whose
// name is in the user id, and handing over a copy of somebody else's would be this server
// vouching for keys it has no way to check. A peer that asked about a user who is not ours
// is given no entry for them rather than an error: the answer to that question, from where
// this server stands, is that there is nothing to say.
//
// The device key objects are handed over exactly as they were published. Their signatures are
// made by the user's own devices, and re-encoding them on the way out breaks the verification
// the peer's clients do afterwards.
func (s *Service) ServeKeyQuery(ctx context.Context, wanted map[string][]string) (matrix.Object, error) {
	own := map[string][]string{}
	for userID, devices := range wanted {
		if !s.LocalUser(userID) {
			continue
		}
		own[userID] = devices
	}
	answer := matrix.Object{
		"device_keys":       matrix.Object{},
		"master_keys":       matrix.Object{},
		"self_signing_keys": matrix.Object{},
		"user_signing_keys": matrix.Object{},
	}
	if len(own) == 0 {
		return answer, nil
	}
	result, err := s.localKeyQuery(ctx, "", own)
	if err != nil {
		return nil, err
	}
	// The label of a device is served here too, in the same unsigned block a client sees. It is
	// metadata this server holds rather than something the device signed, and a peer that keeps a
	// copy of the list keeps the label with it instead of showing a device nameless.
	s.annotateDeviceDisplayNames(result)
	for userID, devices := range result.DeviceKeys {
		entry := matrix.Object{}
		for deviceID, object := range devices {
			entry[deviceID] = object
		}
		answer["device_keys"].(matrix.Object)[userID] = entry
	}
	for field, keys := range map[string]map[string]matrix.Object{
		"master_keys":       result.MasterKeys,
		"self_signing_keys": result.SelfSigningKeys,
		"user_signing_keys": result.UserSigningKeys,
	} {
		if len(keys) == 0 {
			continue
		}
		section := matrix.Object{}
		for userID, object := range keys {
			section[userID] = object
		}
		answer[field] = section
	}
	// The endpoint carries one stream id for a question that may name several users, and the
	// newest position among them is the only number that is true of the answer as a whole. A
	// peer compares it against what it has seen before to decide whether its copy is behind.
	answer["stream_id"] = s.deviceStreamOf(own)
	return answer, nil
}

// ServeKeyClaim answers POST /_matrix/federation/v1/user/keys/claim for a peer: one one-time
// key per requested device of this server's users.
//
// Answering for another server's user is not possible in principle, not merely against the
// rules: the point of a claim is that the key stops being available afterwards, and only the
// server holding the key can make that happen.
func (s *Service) ServeKeyClaim(ctx context.Context, request map[string]map[string]string) (matrix.Object, error) {
	own, _, _ := s.splitKeyClaim(request)
	result, err := s.localKeyClaim(ctx, own)
	if err != nil {
		return nil, err
	}
	answer := matrix.Object{"one_time_keys": matrix.Object{}}
	for userID, devices := range result.OneTimeKeys {
		entry := matrix.Object{}
		for deviceID, keys := range devices {
			inner := matrix.Object{}
			for keyID, object := range keys {
				inner[keyID] = object
			}
			entry[deviceID] = inner
		}
		answer["one_time_keys"].(matrix.Object)[userID] = entry
	}
	return answer, nil
}

// remoteKeyQuery asks one server for the device keys of the users of theirs the client asked
// about, and keeps the answer.
//
// The new form of the question is tried first because it names several users at once. A peer
// that does not know it answers M_UNRECOGNIZED, and the older per-user endpoint is used
// instead; both are in the specification, and a server that has implemented only one is a
// normal thing to meet.
func (s *Service) remoteKeyQuery(ctx context.Context, server string, wanted map[string][]string) (*KeyQueryResult, error) {
	transport := s.federationTransport()
	if transport == nil {
		return nil, fmt.Errorf("this server does not federate, so %s cannot be asked", server)
	}
	deviceKeys := matrix.Object{}
	for userID, devices := range wanted {
		list := matrix.Array{}
		for _, deviceID := range devices {
			list = append(list, deviceID)
		}
		deviceKeys[userID] = list
	}
	answer, err := transport.Post(ctx, server, "/_matrix/federation/v1/user/keys/query",
		matrix.Object{"device_keys": deviceKeys})
	if err != nil {
		if !isUnrecognized(err) {
			return nil, err
		}
		return s.remoteKeyQueryLegacy(ctx, transport, server, wanted)
	}
	result := newKeyQueryResult()
	if section, ok := answer["device_keys"].(matrix.Object); ok {
		for userID, value := range section {
			devices, ok := value.(matrix.Object)
			if !ok {
				continue
			}
			for deviceID, value := range devices {
				object, ok := asObject(value)
				if !ok {
					continue
				}
				if result.DeviceKeys[userID] == nil {
					result.DeviceKeys[userID] = map[string]matrix.Object{}
				}
				result.DeviceKeys[userID][deviceID] = object
				s.cacheRemoteDevice(server, userID, deviceID, object)
			}
		}
	}
	s.readRemoteCrossSigning(server, answer)
	for field, target := range map[string]map[string]matrix.Object{
		"master_keys":       result.MasterKeys,
		"self_signing_keys": result.SelfSigningKeys,
		"user_signing_keys": result.UserSigningKeys,
	} {
		section, ok := answer[field].(matrix.Object)
		if !ok {
			continue
		}
		for userID, value := range section {
			if object, ok := asObject(value); ok {
				target[userID] = object
			}
		}
	}
	return result, nil
}

// remoteKeyQueryLegacy asks the older per-user form of the same question:
// GET /_matrix/federation/v1/user/devices/{userId}.
//
// That response is signed by the server that wrote it, which is what makes it worth checking:
// the payload lists devices the owner claims to have, and an answer from an unexpected
// direction would otherwise be believed. The signature is checked against the key the peer
// publishes for itself, so a mismatch means either the answer or the peer's key document
// changed somewhere between the two requests.
func (s *Service) remoteKeyQueryLegacy(ctx context.Context, transport *federation.Transport, server string, wanted map[string][]string) (*KeyQueryResult, error) {
	result := newKeyQueryResult()
	for userID := range wanted {
		answer, err := transport.Get(ctx, server,
			"/_matrix/federation/v1/user/devices/"+url.PathEscape(userID), nil)
		if err != nil {
			if isUnrecognized(err) {
				// The server has no user by that name. That is an answer, not a failure, and
				// the client learns there is nothing to encrypt to.
				continue
			}
			return nil, err
		}
		if err := s.verifyPeerSigned(server, answer); err != nil {
			return nil, fmt.Errorf("%s signed the device list of %s badly: %w", server, userID, err)
		}
		devices, ok := answer["devices"].([]any)
		if !ok {
			continue
		}
		for _, value := range devices {
			entry, ok := asObject(value)
			if !ok {
				continue
			}
			deviceID, _ := entry["device_id"].(string)
			if deviceID == "" {
				continue
			}
			object := matrix.Object{
				"user_id":   userID,
				"device_id": deviceID,
			}
			if keys, ok := asObject(entry["keys"]); ok {
				object["keys"] = keys
			}
			if signatures, ok := asObject(entry["signatures"]); ok {
				object["signatures"] = signatures
			}
			if result.DeviceKeys[userID] == nil {
				result.DeviceKeys[userID] = map[string]matrix.Object{}
			}
			result.DeviceKeys[userID][deviceID] = object
			s.cacheRemoteDevice(server, userID, deviceID, object)
		}
		for field, usage := range map[string]string{
			"master_key":       CrossSigningMaster,
			"self_signing_key": CrossSigningSelfSigning,
			"user_signing_key": CrossSigningUserSigning,
		} {
			object, ok := asObject(answer[field])
			if !ok {
				continue
			}
			s.storeRemoteCrossSigning(server, userID, usage, object)
		}
	}
	return result, nil
}

// remoteKeyClaim asks one server to hand over one-time keys for its users' devices. There is
// no older form of this question to fall back to: the endpoint predates the query endpoint and
// both servers in a conversation are expected to know it.
func (s *Service) remoteKeyClaim(ctx context.Context, server string, request map[string]map[string]string) (*KeyClaimResult, error) {
	transport := s.federationTransport()
	if transport == nil {
		return nil, fmt.Errorf("this server does not federate, so %s cannot be asked", server)
	}
	oneTimeKeys := matrix.Object{}
	for userID, devices := range request {
		entry := matrix.Object{}
		for deviceID, algorithm := range devices {
			entry[deviceID] = algorithm
		}
		oneTimeKeys[userID] = entry
	}
	answer, err := transport.Post(ctx, server, "/_matrix/federation/v1/user/keys/claim",
		matrix.Object{"one_time_keys": oneTimeKeys})
	if err != nil {
		return nil, err
	}
	result := &KeyClaimResult{OneTimeKeys: map[string]map[string]map[string]matrix.Object{}, Failures: matrix.Object{}}
	section, ok := answer["one_time_keys"].(matrix.Object)
	if !ok {
		return result, nil
	}
	for userID, value := range section {
		devices, ok := value.(matrix.Object)
		if !ok {
			continue
		}
		for deviceID, value := range devices {
			keys, ok := asObject(value)
			if !ok {
				continue
			}
			// A peer may return the key as an object or as the encoded string some servers
			// used to send. Both mean the same key to the client, and the object form is what
			// this server's own claims produce, so a string is put back into that shape.
			inner := map[string]matrix.Object{}
			for keyID, key := range keys {
				if object, ok := asObject(key); ok {
					inner[keyID] = object
					continue
				}
				if encoded, ok := key.(string); ok {
					inner[keyID] = matrix.Object{"key": encoded}
				}
			}
			if len(inner) == 0 {
				continue
			}
			if result.OneTimeKeys[userID] == nil {
				result.OneTimeKeys[userID] = map[string]map[string]matrix.Object{}
			}
			result.OneTimeKeys[userID][deviceID] = inner
		}
	}
	return result, nil
}

// cacheRemoteDevice keeps what a peer said about one of its user's devices. The cache exists
// because a client asks the same question repeatedly while it sets up an encrypted room, and
// because the device list updates a peer sends in between are the deltas of exactly this copy.
func (s *Service) cacheRemoteDevice(server, userID, deviceID string, object matrix.Object) {
	device := &persist.RemoteDevice{
		UserID:   userID,
		DeviceID: deviceID,
		KeyJSON:  string(matrix.Encode(object)),
		SeenTS:   s.NowMS(),
		Origin:   server,
	}
	// The label may sit in the unsigned block of the key object, or - in the older per-user form
	// of the answer - beside it. An empty one never replaces a name this server already has: a
	// peer that lists a device without saying what it is called has not un-named it.
	if unsigned, ok := asObject(object["unsigned"]); ok {
		if name, ok := unsigned["device_display_name"].(string); ok {
			device.DisplayName = name
		}
	}
	if name, ok := object["device_display_name"].(string); ok && name != "" {
		device.DisplayName = name
	}
	if existing, err := s.db.RemoteDeviceRepo.ForUser(userID); err == nil {
		for _, row := range existing {
			if row.DeviceID == deviceID && row.DisplayName != "" {
				if device.DisplayName == "" {
					device.DisplayName = row.DisplayName
				}
				break
			}
		}
	}
	if err := s.db.RemoteDeviceRepo.Set(device); err != nil {
		log.Printf("federation: the cached device %s of %s could not be stored: %v", deviceID, userID, err)
	}
}

// storeRemoteCrossSigning keeps a cross-signing key a peer published for one of its users, so
// that a client asking about that user later reads the same answer it would have got had the
// query happened now. The key is stored as it arrived: this server cannot sign it and must not
// alter what it cannot vouch for.
func (s *Service) storeRemoteCrossSigning(server, userID, usage string, object matrix.Object) {
	if err := s.db.CrossSigningRepo.Set(&persist.CrossSigningKey{
		UserID: userID,
		KeyID:  crossSigningKeyIDOf(object),
		Usage:  usage,
		Data:   string(mustCanonical(object)),
	}); err != nil {
		log.Printf("federation: the %s key of %s could not be stored: %v", usage, userID, err)
	}
}

// readRemoteCrossSigning stores the cross-signing sections of a peer's key query answer.
func (s *Service) readRemoteCrossSigning(server string, answer matrix.Object) {
	for field, usage := range map[string]string{
		"master_keys":       CrossSigningMaster,
		"self_signing_keys": CrossSigningSelfSigning,
		"user_signing_keys": CrossSigningUserSigning,
	} {
		section, ok := answer[field].(matrix.Object)
		if !ok {
			continue
		}
		for userID, value := range section {
			if object, ok := asObject(value); ok {
				s.storeRemoteCrossSigning(server, userID, usage, object)
			}
		}
	}
}

// verifyPeerSigned checks the signatures a peer put on a response it signed, against the keys
// that peer publishes for itself. A response with no signatures is accepted: the request went
// to the server named in the user id over a connection authenticated by that server's key, and
// the newer key query endpoints do not sign their answers at all.
func (s *Service) verifyPeerSigned(server string, object matrix.Object) error {
	signatures, ok := object["signatures"].(matrix.Object)
	if !ok || len(signatures) == 0 {
		return nil
	}
	transport := s.federationTransport()
	if transport == nil {
		return fmt.Errorf("this server does not federate")
	}
	for keyID, value := range signatures {
		if _, ok := value.(string); !ok {
			continue
		}
		public, err := transport.VerifyKeyOf(context.Background(), server, keyID)
		if err != nil {
			return fmt.Errorf("the key %s of %s is not usable: %w", keyID, server, err)
		}
		if err := matrix.VerifyEd25519Signature(object, server, keyID, public); err != nil {
			return err
		}
	}
	return nil
}

// deviceStreamOf reports the newest device list position of the users asked about, which is
// the number a peer compares its own copy against.
func (s *Service) deviceStreamOf(wanted map[string][]string) int64 {
	var highest int64
	for userID := range wanted {
		rows, err := s.db.DeviceKeyRepo.List(userID)
		if err != nil {
			continue
		}
		for _, row := range rows {
			if row.StreamPos > highest {
				highest = row.StreamPos
			}
		}
	}
	return highest
}

// splitKeyQuery separates the users this server can answer for from the ones it has to ask
// about. own is the flat user to devices map the local read takes; remote groups the rest by
// the server their ids name, which is who gets asked. A user id whose server part is not a
// server name cannot be asked of anybody, and is returned as invalid rather than guessed at.
func (s *Service) splitKeyQuery(wanted map[string][]string) (own map[string][]string, remote map[string]map[string][]string, invalid []string) {
	own, remote = map[string][]string{}, map[string]map[string][]string{}
	for userID, devices := range wanted {
		if s.LocalUser(userID) {
			own[userID] = devices
			continue
		}
		server := matrix.DomainOf(userID)
		if !matrix.ValidServerName(server) {
			invalid = append(invalid, userID)
			continue
		}
		if remote[server] == nil {
			remote[server] = map[string][]string{}
		}
		remote[server][userID] = devices
	}
	return own, remote, invalid
}

// splitKeyClaim is [Service.splitKeyQuery] for the one-time key claim request, whose per-user
// value names devices and the algorithm wanted rather than a list of device ids.
func (s *Service) splitKeyClaim(request map[string]map[string]string) (own map[string]map[string]string, remote map[string]map[string]map[string]string, invalid []string) {
	own, remote = map[string]map[string]string{}, map[string]map[string]map[string]string{}
	for userID, devices := range request {
		if s.LocalUser(userID) {
			own[userID] = devices
			continue
		}
		server := matrix.DomainOf(userID)
		if !matrix.ValidServerName(server) {
			invalid = append(invalid, userID)
			continue
		}
		if remote[server] == nil {
			remote[server] = map[string]map[string]string{}
		}
		remote[server][userID] = devices
	}
	return own, remote, invalid
}

// newKeyQueryResult is an empty answer with every section ready to be filled.
func newKeyQueryResult() *KeyQueryResult {
	return &KeyQueryResult{
		DeviceKeys:      map[string]map[string]matrix.Object{},
		Failures:        matrix.Object{},
		MasterKeys:      map[string]matrix.Object{},
		SelfSigningKeys: map[string]matrix.Object{},
		UserSigningKeys: map[string]matrix.Object{},
	}
}

// mergeKeyQuery folds what a peer answered into the answer this server is giving. A device the
// peer names wins over the cached copy this server already had, because the peer is the one
// that holds the device.
func mergeKeyQuery(into, from *KeyQueryResult) {
	for userID, devices := range from.DeviceKeys {
		if into.DeviceKeys[userID] == nil {
			into.DeviceKeys[userID] = map[string]matrix.Object{}
		}
		for deviceID, object := range devices {
			into.DeviceKeys[userID][deviceID] = object
		}
	}
	// The local read leaves a section it has nothing for as a nil map, which is what an
	// answer that omits the section means. Filling it in here has to start the map first.
	for _, pair := range []struct {
		dst *map[string]matrix.Object
		src map[string]matrix.Object
	}{
		{&into.MasterKeys, from.MasterKeys},
		{&into.SelfSigningKeys, from.SelfSigningKeys},
		{&into.UserSigningKeys, from.UserSigningKeys},
	} {
		if len(pair.src) == 0 {
			continue
		}
		if *pair.dst == nil {
			*pair.dst = map[string]matrix.Object{}
		}
		for userID, object := range pair.src {
			(*pair.dst)[userID] = object
		}
	}
}

// pruneEmptyKeyMaps turns the cross-signing sections that ended up empty back into absent
// ones. A client reading an empty master_keys object can take it as "this user has no master
// key", which is a statement about the user rather than about what this server could find.
func pruneEmptyKeyMaps(result *KeyQueryResult) {
	if len(result.MasterKeys) == 0 {
		result.MasterKeys = nil
	}
	if len(result.SelfSigningKeys) == 0 {
		result.SelfSigningKeys = nil
	}
	if len(result.UserSigningKeys) == 0 {
		result.UserSigningKeys = nil
	}
}

// crossSigningKeyIDOf reads the id a cross-signing object gives itself, which is the name of
// the single key inside it.
func crossSigningKeyIDOf(object matrix.Object) string {
	keys, ok := asObject(object["keys"])
	if !ok {
		return ""
	}
	for id := range keys {
		return id
	}
	return ""
}

// SignAsServer adds this server's own signature to an object a peer is expected to check,
// which is what the older federation endpoints that return claims rather than events require.
// The object is signed over its canonical form with the signature and unsigned blocks removed,
// so adding a signature to an already signed object would be a different act - none of the
// endpoints here does that.
func (s *Service) SignAsServer(ctx context.Context, object matrix.Object) error {
	pair, err := s.SigningKey(ctx)
	if err != nil {
		return err
	}
	signature, err := pair.Sign(object)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	object["signatures"] = matrix.Object{
		s.serverName: matrix.Object{pair.ID: signature},
	}
	return nil
}

// cachedRemoteDevices is the copy this server holds of a remote user's devices, kept current
// by the device list updates that server sends. A device marked deleted is left out: a peer
// that announces a deletion keeps a row so that the absence is known, and answering with the
// device anyway would be undoing the deletion.
func (s *Service) cachedRemoteDevices(userID string) (map[string]matrix.Object, error) {
	rows, err := s.db.RemoteDeviceRepo.ForUser(userID)
	if err != nil {
		return nil, err
	}
	out := map[string]matrix.Object{}
	for _, row := range rows {
		if row.Deleted || row.KeyJSON == "" {
			continue
		}
		object, err := decodeJSONObject(row.KeyJSON)
		if err != nil {
			log.Printf("federation: the cached keys of %s device %s are unreadable: %v", userID, row.DeviceID, err)
			continue
		}
		out[row.DeviceID] = object
	}
	return out, nil
}
