package service

import (
	"context"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// toDeviceLimit caps how many send-to-device messages one sync delivers.
const toDeviceLimit = 100

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
	if strings.HasPrefix(eventType, "m.room.") {
		return matrix.ErrInvalidParam("%q is a room event and cannot be sent to a device", eventType)
	}
	if len(messages) == 0 {
		return nil
	}
	for target, byDevice := range messages {
		if !s.UserExists(ctx, target) {
			continue
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
