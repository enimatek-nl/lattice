package e2ekeys

import (
	"context"
	"encoding/json"

	"github.com/enimatek/lattice/internal/matrixid"
)

// deviceListUpdateEDU is the ephemeral unit type that carries a device list change to another
// homeserver.
const deviceListUpdateEDU = "m.device_list_update"

// DeviceEDUs carries this server's users' device list changes to other homeservers. The
// federation sender satisfies it, and the unit is queued rather than sent: a device list update
// that arrives late is still true, unlike a typing notification, but it still must not block a
// local action on a remote server that may be down.
type DeviceEDUs interface {
	EnqueueEDU(ctx context.Context, destination, eduType string, content map[string]any) error
}

// ServeDeviceEDUs tells the service how to tell other servers about its own users' devices.
// Without it — a unit test, or federation switched off — a device list change stays a local
// fact, which is what it was before this existed.
func (s *Service) ServeDeviceEDUs(edus DeviceEDUs) { s.edus = edus }

// AnnounceDeviceListsToFederation tells the servers that share a joined room with a local user
// that this server holds device keys worth re-reading for them.
//
// The reason it is worth sending is that a client on the other server learns about the change
// from its own server, and its own server only learns about it from us. A key query can be
// answered by asking (see [Service.Query]), but nothing prompts the question otherwise: the
// other server's client never finds out that the answer changed.
//
// One update number is taken per announcement rather than per device. It is this server's number
// for this user, strictly increasing across announcements, which is what a receiving server uses
// to tell a newer update from one it has already applied.
func (s *Service) AnnounceDeviceListsToFederation(ctx context.Context, userID string) {
	if s.edus == nil || userID == "" {
		return
	}
	_, domain, err := matrixid.SplitID(matrixid.SigilUser, userID)
	if err != nil || !matrixid.IsLocal(domain, s.serverName) {
		// This server can only speak for the users it carries. A peer answers for its own.
		return
	}
	destinations, err := s.serversSharingRoomWith(ctx, userID)
	if err != nil {
		s.log.Warn("e2ekeys: cannot work out which servers to tell about a device list change",
			"user_id", userID, "error", err)
		return
	}
	if len(destinations) == 0 {
		return
	}
	devices, err := s.db.Devices.ListByUser(ctx, userID)
	if err != nil {
		s.log.Warn("e2ekeys: cannot read the devices of a user whose device list changed",
			"user_id", userID, "error", err)
		return
	}
	if len(devices) == 0 {
		// There is no device to announce. A user with no sessions has no device list, and a
		// peer asked about one would find nothing either way.
		return
	}
	documents, err := s.deviceDocuments(ctx, userID)
	if err != nil {
		s.log.Warn("e2ekeys: cannot read the device keys to announce", "user_id", userID, "error", err)
		return
	}
	stream, err := s.devices.NextDeviceStream(ctx, userID)
	if err != nil {
		s.log.Warn("e2ekeys: cannot take a device list update number", "user_id", userID, "error", err)
		return
	}
	for _, destination := range destinations {
		for _, device := range devices {
			content := map[string]any{
				"user_id":   userID,
				"device_id": device.DeviceID,
				"stream_id": stream,
				"deleted":   false,
			}
			if document, ok := documents[device.DeviceID]; ok {
				content["keys"] = document
			} else {
				// The device has not uploaded key material yet. The announcement still goes,
				// because what the peer needs to know is that this device exists and is worth
				// re-reading; an empty key set is the honest shape of "nothing so far", and an
				// invented one would be a signature waiting to be checked against nothing.
				content["keys"] = map[string]any{
					"user_id":    userID,
					"device_id":  device.DeviceID,
					"keys":       map[string]string{},
					"signatures": map[string]any{},
				}
			}
			if device.DisplayName != "" {
				content["device_display_name"] = device.DisplayName
			}
			if err := s.edus.EnqueueEDU(ctx, destination, deviceListUpdateEDU, content); err != nil {
				s.log.Warn("e2ekeys: cannot queue a device list update for a remote server",
					"destination", destination, "user_id", userID, "device_id", device.DeviceID, "error", err)
			}
		}
	}
}

// NoteRemoteDeviceListChange is the receiving half. A peer says a user it carries has devices
// worth re-reading; the users who may hear about that are the local users who share a room with
// them, which is the same question the local announcement answers, so the same recording is used.
//
// The key document the peer sent is not stored. This server answers a query about a remote user by
// asking that user's server, which holds the signed original; keeping a copy here would be a second
// place for the same fact to go stale, and a place where a signature could be checked against
// something other than what the device signed.
//
// The update number is not compared against a stored one either, for the same reason: numbers guard
// state this server does not keep. A repeated announcement costs a client a key query and nothing
// else.
func (s *Service) NoteRemoteDeviceListChange(ctx context.Context, userID string) {
	if userID == "" {
		return
	}
	_, domain, err := matrixid.SplitID(matrixid.SigilUser, userID)
	if err != nil || matrixid.IsLocal(domain, s.serverName) {
		// A peer announcing a user of this server's own is describing somebody it has no
		// business describing. Whatever it send, this server's own store is the answer.
		return
	}
	s.announceChange(ctx, userID)
}

// serversSharingRoomWith lists the other homeservers that hold a joined room with this user.
func (s *Service) serversSharingRoomWith(ctx context.Context, userID string) ([]string, error) {
	memberships, err := s.state.MembershipEventsFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, membership := range memberships {
		if membership.RoomID == "" || !isJoined(membership) {
			continue
		}
		members, err := s.state.CurrentMembers(ctx, membership.RoomID)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			server, err := matrixid.UserDomain(member)
			if err != nil || seen[server] || server == s.serverName {
				continue
			}
			seen[server] = true
			out = append(out, server)
		}
	}
	return out, nil
}

// deviceDocuments reads the key documents of a user's devices in the object form the ephemeral
// unit carries them in.
func (s *Service) deviceDocuments(ctx context.Context, userID string) (map[string]map[string]any, error) {
	rows, err := s.keys.DeviceKeys(ctx, userID, nil, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		var document map[string]any
		if err := json.Unmarshal([]byte(row.Document), &document); err != nil {
			continue
		}
		deviceID, _ := document["device_id"].(string)
		if deviceID == "" {
			deviceID = row.DeviceID
		}
		out[deviceID] = document
	}
	return out, nil
}
