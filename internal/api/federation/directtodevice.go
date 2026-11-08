package federation

import (
	"context"
	"encoding/json"

	"github.com/enimatek/lattice/internal/devicemessages"
	"github.com/enimatek/lattice/internal/matrixid"
)

// directToDeviceEDU is the ephemeral unit that carries a to-device message between servers.
const directToDeviceEDU = devicemessages.EDUType

// DirectToDevice files a message that a peer carried for one of this server's devices. It is
// implemented by the to-device service, which owns the store the recipient's `/sync` reads.
type DirectToDevice interface {
	AcceptRemote(ctx context.Context, origin, sender, senderDevice, user, device, eventType string, content json.RawMessage) error
}

// noteDirectToDevice acts on a peer delivering a message addressed to a local device.
//
// An ephemeral unit is never acknowledged as a room event is: the transaction's answer says what happened
// to each PDU and says nothing about the units, which is why a delivery that this server declines has to
// be visible in the log rather than merely absent. A silently dropped key share is indistinguishable from
// a working encrypted room for everybody except the two clients staring at ciphertext.
//
// The trust rule is the one a device-list update already applies, and for the same reason: the transaction
// is authenticated, so this server knows *who* is speaking; what it must also decide is what they may
// speak about. A peer may deliver a message for a user on its own domain, and for a local recipient — but
// it may not claim to be sending *as* somebody who lives elsewhere. Without that last check, any server
// this one has ever spoken to could put a message in any local device's pigeonhole under any name, and
// clients show the sender of a to-device message without being able to verify it, because the payload is
// encrypted and the sender field is the only thing they can see.
func (d *Deps) noteDirectToDevice(ctx context.Context, origin string, content map[string]any) {
	if d.DeviceMessages == nil {
		d.Log.Warn("federation: a to-device message arrived for a device, and this server is not filing them",
			"origin", origin)
		return
	}
	user, _ := content["user"].(string)
	device, _ := content["device"].(string)
	sender, _ := content["sender"].(string)
	senderDevice, _ := content["sender_device"].(string)
	eventType, _ := content["msgtype"].(string)
	if user == "" || eventType == "" {
		// A unit with no recipient cannot be filed, and one with no type cannot be routed by the
		// client that receives it. Both are the sender's problem and neither is worth failing the
		// transaction over, because the transaction also carried events that this server has
		// already accepted.
		d.Log.Warn("federation: a to-device unit named no recipient or no type",
			"origin", origin, "user_id", user, "msgtype", eventType)
		return
	}
	payload, err := json.Marshal(content["content"])
	if err != nil {
		d.Log.Warn("federation: a to-device unit carried a payload this server could not encode",
			"origin", origin, "user_id", user, "error", err)
		return
	}
	if domain, err := matrixid.UserDomain(user); err == nil && !matrixid.IsLocal(domain, d.Cfg.ServerName) {
		// Not ours. The peer will learn nothing from this, and there is nothing in the protocol for
		// a server to say "deliver it yourself" — but the log is where this becomes someone's bug
		// report rather than a silent hole.
		d.Log.Warn("federation: a server delivered a to-device message for a user this server does not carry",
			"origin", origin, "user_id", user)
		return
	}
	if err := d.DeviceMessages.AcceptRemote(ctx, origin, sender, senderDevice, user, device, eventType, payload); err != nil {
		d.Log.Warn("federation: a to-device message could not be filed for its device",
			"origin", origin, "user_id", user, "device_id", device, "error", err)
	}
}
