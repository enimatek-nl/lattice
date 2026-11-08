// Package devicemessages delivers the events a client addresses to another user's devices instead of to a
// room.
//
// The distinction is not cosmetic and neither is the feature. A room event is written once, ordered against
// other events, and authorised by room state; a to-device message is written into one device's pigeonhole,
// has no room, no state, and no history, and is authorised by nothing except the fact that the sender is
// allowed to talk to the recipient. `m.room.encrypted` and `m.dummy` — the Megolm session key and its
// "please ignore this" companion — travel only this way, so a server that cannot deliver them can hold the
// encrypted room's events and still never let a client read one. That is the "Unable to decrypt" a user
// blames on the other person's device, when the fault is that the keys never crossed the room.
package devicemessages

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// Service stores and hands out the messages waiting on a device.
type Service struct {
	db       *persist.Database
	messages persist.DeviceMessageRepo
	devices  persist.DeviceRepo
	log      *slog.Logger
	// serverName decides whose devices this server may leave a note for. A message for another
	// server's device is not this server's to file: it has to be carried to that server, which is
	// federation and a separate piece of work.
	serverName string
	// SyncLimit caps how many messages one sync hands over. A device that has not synced while
	// a busy peer was sharing keys could otherwise be handed thousands of payloads in one
	// response, and a client that times out on its own sync would ask again.
	SyncLimit int
}

// NewService builds the service over an open database.
func NewService(db *persist.Database, serverName string, log *slog.Logger) *Service {
	return &Service{
		db:         db,
		messages:   db.DeviceMessages,
		devices:    db.Devices,
		log:        log,
		serverName: serverName,
		SyncLimit:  100,
	}
}

// Send files one send: `messages` maps a recipient user to the devices of theirs being addressed and the
// payload for each, where the device key `*` means every device that user has.
//
// The `*` is expanded here rather than at read time, and that is a decision rather than an optimisation.
// Each device reads at its own position, so a shared row would be spent by whichever device synced first
// and lost to the others; and a device created after this send was not addressed, should not be handed a
// key that was meant for the devices that existed when the sender looked them up.
//
// A recipient who lives on another server is not filed and not failed: the message has to be carried to
// that server as an ephemeral unit, and dropping one key request because that is not wired up yet is the
// better of the two wrong answers available — the sender's own device will retry the key share when it
// next sees the recipient's keys, and a client whose send had failed would stop trying.
func (s *Service) Send(ctx context.Context, sender, senderDevice, eventType, txnID string,
	messages map[string]map[string]json.RawMessage) error {
	if err := matrixid.EventTypeValid(eventType); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid event type.", eventType)
	}
	if len(messages) == 0 {
		return nil
	}
	var txn *string
	if txnID != "" {
		value := txnID
		txn = &value
	}
	rows := make([]*persist.DeviceMessage, 0, len(messages))
	var remote []string
	for user, byDevice := range messages {
		if err := matrixid.UserValid(user); err != nil {
			return matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", user)
		}
		domain, err := matrixid.UserDomain(user)
		if err != nil {
			return matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", user)
		}
		if !matrixid.IsLocal(domain, s.serverName) {
			remote = append(remote, user)
			continue
		}
		for device, content := range byDevice {
			targets, err := s.targets(ctx, user, device)
			if err != nil {
				return err
			}
			for _, target := range targets {
				rows = append(rows, &persist.DeviceMessage{
					SenderID:       sender,
					SenderDeviceID: senderDevice,
					UserID:         user,
					DeviceID:       target,
					EventType:      eventType,
					Content:        string(content),
					TxnID:          txn,
				})
			}
		}
	}
	if len(remote) > 0 {
		// Loud in the log, quiet to the client: see the note above about which wrong answer is
		// smaller. This is the federation ephemeral-unit path, and the list is what it will need.
		s.log.Warn("devicemessages: a send addressed devices on another server, which this server cannot yet reach",
			"event_type", eventType, "recipients", len(remote), "sender", sender)
	}
	if len(rows) == 0 {
		return nil
	}
	if _, err := s.messages.Put(ctx, rows); err != nil {
		return matrixerr.New(matrixerr.CodeUnknown, "This server could not file the message for delivery.")
	}
	return nil
}

// targets is the device list a message addressed to `device` of `user` actually goes to. An unknown device
// is skipped rather than refused: a client sends to a device list it read a moment ago, and a peer that has
// since logged out is a normal reason for the name to be stale. Refusing the whole send because one of
// twelve devices vanished would lose the message for the eleven that remain.
func (s *Service) targets(ctx context.Context, user, device string) ([]string, error) {
	if device != "*" {
		if device == "" {
			return nil, matrixerr.New(matrixerr.CodeInvalidParam,
				"A to-device message names the device to receive it, or * for all of them.")
		}
		return []string{device}, nil
	}
	devices, err := s.devices.ListByUser(ctx, user)
	if err != nil {
		return nil, err
	}
	targets := make([]string, 0, len(devices))
	for _, d := range devices {
		targets = append(targets, d.DeviceID)
	}
	return targets, nil
}

// Drain reads the messages waiting on one device between two sync positions, oldest first. The positions
// come from the same stream as room events, which is what lets one sync token answer "what happened to
// this room" and "what arrived for this device" with one number.
func (s *Service) Drain(ctx context.Context, userID, deviceID string, since, until int64) ([]*persist.DeviceMessage, error) {
	if deviceID == "" {
		// A token with no device behind it has no pigeonhole to read. A client authenticating
		// without a device is a legacy access token, and those are not owed to-device messages
		// they could not have asked for.
		return nil, nil
	}
	limit := s.SyncLimit
	if limit <= 0 {
		limit = 100
	}
	return s.messages.ListFor(ctx, userID, deviceID, since, until, limit)
}
