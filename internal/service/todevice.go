package service

import (
	"context"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// toDeviceLimit caps how many send-to-device messages one sync delivers.
const toDeviceLimit = 100

// refusesOnTheToDevicePath says whether an event type named in the
// /sendToDevice/{eventType}/{txnId} path is a room event with no business being
// pushed straight to a device.
//
// m.room.encrypted is deliberately **not** refused, and it is not really an
// exception: it is the Olm envelope. Establishing an Olm session means handing an
// encrypted message to another device, and every client does that through this
// path with this type - the specification's Olm walkthrough gives the content
// (algorithm, sender_key, ciphertext keyed by the recipient's Curve25519 key)
// while the path supplies the type, so the per-device value carries no wrapper.
// Refusing it reads as tidy input validation and breaks end-to-end encryption
// outright: the claimed one-time key is spent, the room key never arrives, and the
// other device answers every message with "the sender's device has not sent us the
// keys for this message". The room events that genuinely do not belong here -
// m.room.message, m.room.member and the rest - are still refused, because no
// reading of this endpoint makes them sensible.
func refusesOnTheToDevicePath(eventType string) bool {
	return strings.HasPrefix(eventType, "m.room.") && eventType != matrix.TypeEncrypted
}

// SendToDevice stores messages addressed to other users' devices rather than to
// a room; clients pick them up in the to_device section of a sync. Recipients
// that are remote or unknown are skipped silently, as the specification asks.
//
// The (sender, transaction, recipient, device) tuple is unique, so a client
// that retries the request does not deliver the message twice.
func (s *Service) SendToDevice(ctx context.Context, senderID, senderDevice, eventType, txnID string,
	messages map[string]map[string]matrix.RawMessage) error {
	if txnID == "" {
		return matrix.ErrInvalidParam("the transaction id is required")
	}
	if eventType == "" {
		return matrix.ErrInvalidParam("the event type is required")
	}
	if refusesOnTheToDevicePath(eventType) {
		return matrix.ErrInvalidParam("%q is a room event and cannot be sent to a device", eventType)
	}
	if len(messages) == 0 {
		return nil
	}
	remote := map[string]map[string]matrix.RawMessage{}
	for target, byDevice := range messages {
		if !s.UserExists(ctx, target) {
			// A user of another server is not this server's user, but their server is
			// reachable and is the only place their devices are. With federation off
			// there is nowhere to send it, and the specification's rule - silently skip
			// the unknown - is what applies.
			if !s.outboxWanted() || matrix.DomainOf(target) == s.serverName || matrix.DomainOf(target) == "" {
				continue
			}
			remote[target] = byDevice
		}
		for device, content := range byDevice {
			if device == "" {
				device = "*"
			}
			message := &persist.ToDevice{
				SenderID:     senderID,
				SenderDevice: senderDevice,
				TxnID:        txnID,
				TargetID:     target,
				TargetDevice: device,
				Type:         eventType,
				Content:      string(content),
				TS:           s.NowMS(),
				StreamPos:    s.nextStreamPos(),
			}
			if err := s.db.ToDeviceRepo.Set(message); err != nil {
				return matrix.ErrInternal(err)
			}
		}
	}
	// The handoff that makes cross-server E2EE possible: the message also goes to the
	// servers of the remote recipients, carrying the client's transaction id as the
	// message id so a retry cannot deliver the same room key twice.
	s.queueToDeviceForPeers(ctx, senderID, senderDevice, eventType, txnID, remote)
	s.Notify()
	return nil
}

// toDeviceEvents returns the send-to-device events a device has not received
// yet, oldest first. Messages stay in the store so that a device which has been
// offline keeps everything it missed; [ToDeviceRepo.Prune] exists for the
// cleanup, which needs a per-device watermark before it is safe to call.
func (s *Service) toDeviceEvents(userID, deviceID string, since uint64) ([]matrix.Event, error) {
	rows, err := s.db.ToDeviceRepo.Pending(userID, deviceID, since, toDeviceLimit)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	events := make([]matrix.Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, matrix.Event{
			Type:           row.Type,
			Sender:         row.SenderID,
			OriginServerTS: row.TS,
			Content:        matrix.RawMessage(row.Content),
		})
	}
	return events, nil
}
