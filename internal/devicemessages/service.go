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

// Emitter is the federation queue: the way a message leaves this server for another one.
type Emitter interface {
	EnqueueEDU(ctx context.Context, destination, eduType string, content map[string]any) error
}

// EDUType is the ephemeral unit that carries a to-device message between servers. It is the name
// the specification gives it, and both ends have to agree: a server that invented its own would
// have a working half of a feature.
const EDUType = "m.direct_to_device"

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
	// EDUs carries a message whose recipient lives on another server. It is the federation
	// transaction queue, and it is an interface with one method because this service has no
	// business knowing how a transaction is retried, only that a unit can be handed to
	// somebody who will.
	EDUs Emitter
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
			queued, err := s.emitRemote(ctx, sender, senderDevice, user, byDevice, eventType)
			if err != nil {
				return err
			}
			if queued {
				remote = remote[:len(remote)-1]
			}
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

// emitRemote hands a remote recipient's messages to the federation queue, one ephemeral unit per
// addressed device.
//
// One unit per device rather than one per user is what the specification asks for and what the receiving
// server can act on without guessing; the exception is `*`, which stays `*` because this server does not
// know another server's device list, and the server that does is the one holding the recipient's devices.
//
// It returns false when there is no queue to hand to, which is the state this server is in until the
// federation wiring is present: the message is then dropped and logged, and the client's own retry of the
// key share remains the way the conversation recovers.
func (s *Service) emitRemote(ctx context.Context, sender, senderDevice, user string, byDevice map[string]json.RawMessage, eventType string) (bool, error) {
	if s.EDUs == nil {
		return false, nil
	}
	domain, err := matrixid.UserDomain(user)
	if err != nil {
		return false, nil
	}
	for device, content := range byDevice {
		var payload any
		if err := json.Unmarshal(content, &payload); err != nil {
			// The sender sent something this server cannot re-encode as JSON. Dropping one
			// device's payload beats failing a send that other recipients already received.
			s.log.Warn("devicemessages: a remote to-device payload could not be read",
				"recipient", user, "device", device, "error", err)
			continue
		}
		if err := s.EDUs.EnqueueEDU(ctx, domain, EDUType, map[string]any{
			"sender":        sender,
			"sender_device": senderDevice,
			"user":          user,
			"device":        device,
			// `msgtype` is what the specification calls the to-device event's type on the
			// wire. The name is a fossil from when the only thing worth carrying between
			// devices was an encrypted room payload; both ends have to use it because there
			// is no other field that says what the payload is.
			"msgtype": eventType,
			"content": payload,
		}); err != nil {
			return false, err
		}
	}
	return true, nil
}

// AcceptRemote files a message that another server carried for one of this server's devices.
//
// The trust question is the same one a device-list update answers, and gets the same answer: a peer may
// speak for users on its own domain and for nobody else's. The transaction this arrived inside is
// authenticated, which proves *who* said it; this check is about *what* they are allowed to say, and
// without it any peer could leave a note in any local device's pigeonhole.
//
// The content is stored as the peer sent it and read back verbatim. The server cannot see into an
// encrypted payload and has no business editing one it can see into.
func (s *Service) AcceptRemote(ctx context.Context, origin, sender, senderDevice, user, device, eventType string, content json.RawMessage) error {
	if user == "" {
		return nil
	}
	domain, err := matrixid.UserDomain(user)
	if err != nil || !matrixid.IsLocal(domain, s.serverName) {
		s.log.Warn("devicemessages: a server carried a to-device message for a user it does not carry",
			"origin", origin, "user_id", user)
		return nil
	}
	if senderDomain, err := matrixid.UserDomain(sender); sender != "" && err == nil && !matrixid.IsLocal(senderDomain, origin) {
		s.log.Warn("devicemessages: a server carried a to-device message from a sender it does not speak for",
			"origin", origin, "sender", sender, "user_id", user)
		return nil
	}
	targets, err := s.targets(ctx, user, device)
	if err != nil {
		return err
	}
	rows := make([]*persist.DeviceMessage, 0, len(targets))
	for _, target := range targets {
		rows = append(rows, &persist.DeviceMessage{
			SenderID:       sender,
			SenderDeviceID: senderDevice,
			UserID:         user,
			DeviceID:       target,
			EventType:      eventType,
			Content:        string(content),
		})
	}
	if len(rows) == 0 {
		return nil
	}
	_, err = s.messages.Put(ctx, rows)
	return err
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
