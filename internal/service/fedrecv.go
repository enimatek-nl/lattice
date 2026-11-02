package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// Federation arrives through one door: a signed transaction that carries a batch of
// events and a batch of ephemeral messages, named by a transaction id the sender will
// retry until this server answers. Everything in this file hangs off that fact, and two
// rules follow from it and shape the rest.
//
// The first is that the answer has to be remembered. A sender retries the same
// transaction id until it gets a 200, so a receiver that processes the batch twice
// applies every event in it twice - and a duplicated event in a room cannot be taken
// back. The second is that a peer speaks only for itself: it may describe its own users'
// typing, receipts and devices, and nothing it says about another server's users is
// authoritative, however it arrived.

// HandleSendTransaction answers PUT /_matrix/federation/v1/send/{txnId} for the peer
// whose signature the middleware verified. The peer's name comes from the signature and
// never from the body: a body field that disagrees with the signature is a claim, and a
// claim is not an identity.
func (s *Service) HandleSendTransaction(ctx context.Context, peer, txnID string, body matrix.Object) (matrix.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	txn, err := federation.ParseTransaction(txnID, body)
	if err != nil {
		return nil, err
	}
	if txn.Origin != peer {
		return nil, matrix.ErrUnauthorized(
			"the transaction claims to come from %s, but the request is signed by %s", txn.Origin, peer)
	}
	if !federation.ValidTxnID(txn.TxnID) {
		return nil, matrix.ErrInvalidParam("the transaction id is not usable")
	}

	// A retry gets the previous answer, unchanged. This is checked before anything is
	// processed rather than after, because the whole point is to not process it twice.
	if seen, err := s.db.InboundTxnRepo.Get(peer, txn.TxnID); err == nil {
		replay := matrix.Object{}
		if seen.Result != "" {
			if err := json.Unmarshal([]byte(seen.Result), &replay); err != nil {
				return nil, matrix.ErrInternal(fmt.Errorf("the stored answer to %s from %s cannot be read: %v",
					txn.TxnID, peer, err))
			}
		}
		return replay, nil
	} else if !persist.IsNotFound(err) {
		return nil, matrix.ErrInternal(err)
	}

	for _, edu := range txn.EDUs {
		if err := s.applyEDU(ctx, peer, edu); err != nil {
			// An EDU that cannot be applied is dropped and logged. Ephemeral messages
			// have no place in the answer - the specification gives per-event results
			// only - and an unknown or malformed one is not a reason to refuse the
			// events in the same transaction.
			log.Printf("federation: %s sent an unusable %s: %v", peer, federation.EDUType(edu), err)
		}
	}

	results := make(federation.PDUResults, len(txn.PDUs))
	for _, pdu := range txn.PDUs {
		eventID, problem := s.queuePDU(ctx, peer, txn.TxnID, pdu)
		if problem != "" {
			results[eventID] = federation.PDUResult{Error: problem}
			continue
		}
		results[eventID] = federation.PDUResult{}
	}

	answer := results.Body()
	encoded, err := matrix.CanonicalJSON(answer)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	if err := s.db.InboundTxnRepo.Store(&persist.InboundTxn{
		Origin:     peer,
		TxnID:      txn.TxnID,
		Result:     string(encoded),
		PDUs:       len(txn.PDUs),
		EDUs:       len(txn.EDUs),
		ReceivedTS: s.NowMS(),
	}); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	s.Notify()
	return answer, nil
}

// queuePDU stores an event a peer sent, and returns the id it is answered under and an
// empty problem string when it was taken.
//
// What is *not* done here is deciding whether the event belongs in the room: that needs
// the room's auth chain and state, and is the next step of this plan. The event is kept
// in the form it arrived in and acknowledged, which is the honest pair of things to do
// at this point - the peer is told the event is safe here, and it stays here until it
// can be judged.
func (s *Service) queuePDU(ctx context.Context, peer, txnID string, pdu matrix.Object) (eventID, problem string) {
	eventID = federation.EventField(pdu, "event_id")
	roomID := federation.EventField(pdu, "room_id")
	if roomID == "" {
		return "", "a PDU must name the room it belongs to"
	}
	if federation.EventField(pdu, "type") == "" {
		return eventID, "a PDU must carry a type"
	}
	sender := federation.EventField(pdu, "sender")
	if sender == "" {
		return eventID, "a PDU must name its sender"
	}
	known := true
	version := ""
	if room, err := s.db.RoomRepo.Get(roomID); err == nil {
		version = RoomVersionOf(room)
	} else if persist.IsNotFound(err) {
		known = false
	} else {
		return eventID, "this server could not look up the room of the event"
	}
	if !known {
		// A room nobody here is in. A membership event naming one of this server's
		// users is how a server learns of a room at all - the invite arrives before any
		// way to ask about the room - so those are taken in. Everything else for a room
		// this server has no part in is refused: the room version, and so the event's
		// whole identity, cannot be checked here.
		if federation.EventField(pdu, "type") != matrix.TypeMember ||
			matrix.DomainOf(federation.EventField(pdu, "state_key")) != s.serverName {
			return eventID, "this server is not in the room the event belongs to"
		}
		// The room version is not known yet, and the version decides how an event is
		// named. The version this server creates rooms with is used to name the event in
		// the meantime: for the versions in common use that is the same name, and the
		// join that follows replaces the guess with the room's real version.
		version = DefaultRoomVersion
	}
	if eventID == "" {
		derived, err := eventcrypto.EventID(pdu, version)
		if err != nil {
			return "", fmt.Sprintf("the id of the event cannot be derived: %v", err)
		}
		eventID = derived
	}
	if acl, err := s.roomServerACL(ctx, roomID); err == nil && !federation.ServerAllowed(acl, peer) {
		// The room has excluded this server. Answering without an error is deliberate: a
		// refusal is something the sending server retries, and a retry is how an
		// exclusion turns into a flood.
		log.Printf("federation: %s sent %s to a room that excludes it", peer, eventID)
		return eventID, ""
	}
	// The checks the specification performs on an event that arrives, in its order:
	// is it an event at all, does the server that made it sign it, and does its content
	// hash say what it says. The order is the specification's and it is not interchangeable:
	// a format check is free, a signature check costs a key fetched over the network, and
	// both come before any of the room's rules are read.
	pdu, problem = s.judgeIncomingPDU(ctx, pdu, version)
	if problem != "" {
		return eventID, problem
	}
	// A server may deliver an event whose sender lives somewhere else entirely: it is
	// relaying what the origin server signed, and the signature - checked before this
	// point - is what says so. The sender is therefore not required to be a user of the
	// peer that sent it.
	encoded, err := matrix.CanonicalJSON(pdu)
	if err != nil {
		return eventID, fmt.Sprintf("the PDU cannot be encoded: %v", err)
	}
	added, err := s.db.ReceivedPDURepo.Store(&persist.ReceivedPDU{
		EventID:     eventID,
		RoomID:      roomID,
		Origin:      peer,
		TxnID:       txnID,
		RoomVersion: version,
		JSON:        string(encoded),
		Received:    s.NowMS(),
	})
	if err != nil {
		return eventID, "this server could not store the event"
	}
	if !added {
		// The same event by another route, or by a retry of a transaction this server
		// has already seen. Not an error: the sender is asking the same question twice,
		// and the answer is the same both times.
		log.Printf("federation: %s sent %s again", peer, eventID)
	}
	// The queue is consumed here rather than by a later loop, and that is a decision
	// rather than a shortcut. The answer a peer is waiting for is the answer to "what
	// did you decide about this event", a retry of the same transaction has to get the
	// same answer, and the state of a room must not depend on when this server gets
	// round to its backlog. The row stays either way: an event this server refused is
	// kept with the reason, because "why is this event not in my room" is the first
	// question an operator asks about a federation problem.
	outcome, reason, err := s.applyIncomingPDU(ctx, pdu, version, peer, eventID)
	if err != nil {
		log.Printf("federation: %s could not be judged: %v", eventID, err)
		return eventID, "this server could not process the event"
	}
	if err := s.db.ReceivedPDURepo.Mark(eventID, outcome, reason); err != nil {
		log.Printf("federation: the outcome of %s could not be recorded: %v", eventID, err)
	}
	switch outcome {
	case OutcomeStored, OutcomeSoftFailed:
		stored, err := s.db.EventRepo.Get(eventID)
		if err != nil {
			break
		}
		// A soft-failed event is relayed on. This server's state does not support it,
		// and the servers whose state might are the ones that have to hear about it;
		// refusing to pass it on is how a room ends up forked on one server's opinion.
		s.queueEventForPeersExcept(ctx, stored, version, peer)
		if outcome == OutcomeStored {
			s.Notify()
			if err := s.notifyParticipants(ctx, roomID, stored); err != nil {
				log.Printf("push rule evaluation for the received event %s failed: %v", eventID, err)
			}
		}
	}
	return eventID, ""
}

// judgeIncomingPDU runs the three checks an arriving event has to survive before it is
// worth putting anywhere: the shape the room version gives an event, the signature of
// the server that made it, and its own content hash. It returns the event this server
// will keep - which is not always the one that arrived - and an empty problem string
// when the event is taken.
//
// A hash that does not match is not a refusal. The specification reads it as "we were
// given a redacted copy, or somebody added fields to the event in transit", and asks
// the receiver to keep the redacted copy it calculated itself. That is the honest
// answer: the signature, which is made over the redacted form, has already been checked,
// so the fields this server cannot account for are dropped rather than believed.
func (s *Service) judgeIncomingPDU(ctx context.Context, pdu matrix.Object, version string) (matrix.Object, string) {
	if err := eventcrypto.PDUCheck(pdu, version); err != nil {
		return nil, fmt.Sprintf("the event is not a valid event for room version %s: %v", version, err)
	}
	if problem := s.verifyPDUSignature(ctx, pdu, version); problem != "" {
		return nil, problem
	}
	if err := eventcrypto.VerifyContentHash(pdu, version); err != nil {
		redacted := eventcrypto.RedactedForm(pdu, version)
		if !eventcrypto.IsRedactedForm(pdu, version) && !sameEventBytes(pdu, redacted) {
			log.Printf("federation: the content hash of %s does not cover the event as received; "+
				"keeping the redacted copy this server computed", federation.EventField(pdu, "event_id"))
		}
		return redacted, ""
	}
	return pdu, ""
}

// verifyPDUSignature checks that the server named as an event's author signed it, with a
// key that server currently publishes.
//
// Which server that is depends on the room version. Versions 1 and 2 say it twice over:
// in the origin field, and in the domain of the id the origin server chose. Later
// versions dropped both, and the author is the server of the user who sent it - which is
// also what the specification's list of expected signatures says. The peer that
// delivered the event is not the author and is not asked to sign it: a PDU is signed by
// the server that made it precisely so that it can be relayed.
func (s *Service) verifyPDUSignature(ctx context.Context, pdu matrix.Object, version string) string {
	transport := s.federationTransport()
	if transport == nil {
		// Federation is off, so this server has no way to look up a signing key and no
		// business accepting an event that needs one.
		return "this server cannot check the signature of a remote event"
	}
	signers := []string{matrix.DomainOf(federation.EventField(pdu, "sender"))}
	if number, err := strconv.Atoi(strings.TrimSpace(version)); err == nil && number <= 2 {
		if origin := federation.EventField(pdu, "origin"); origin != "" {
			signers = append(signers, origin)
		}
		if domain := matrix.DomainOf(federation.EventField(pdu, "event_id")); domain != "" {
			signers = append(signers, domain)
		}
	}
	signatures, _ := asObject(pdu["signatures"])
	for _, serverName := range signers {
		if serverName == "" {
			continue
		}
		byKey, ok := asObject(signatures[serverName])
		if !ok || len(byKey) == 0 {
			return fmt.Sprintf("the event carries no signature by %s, which made it", serverName)
		}
		var problem = "the signature of the event does not verify"
		for keyID := range byKey {
			publicKey, err := transport.VerifyKeyOf(ctx, serverName, keyID)
			if err != nil {
				problem = fmt.Sprintf("the signing key %s of %s could not be used: %v", keyID, serverName, err)
				continue
			}
			if err := eventcrypto.EventSignature(pdu, version, serverName, keyID, publicKey); err != nil {
				problem = err.Error()
				continue
			}
			return ""
		}
		return problem
	}
	return "the event names no server that could have signed it"
}

// sameEventBytes compares two events by their canonical encoding, the only comparison
// that does not depend on how a sender spelled a number or ordered a key.
func sameEventBytes(a, b matrix.Object) bool {
	left, err := matrix.CanonicalJSON(a)
	if err != nil {
		return false
	}
	right, err := matrix.CanonicalJSON(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

// applyEDU applies one ephemeral message. A type this server does not handle is
// ignored, which is what the specification asks for and what lets a server grow without
// breaking its peers.
func (s *Service) applyEDU(ctx context.Context, peer string, edu matrix.Object) error {
	content := federation.EDUContent(edu)
	if content == nil {
		return fmt.Errorf("the %s message carries no content object", federation.EDUType(edu))
	}
	switch federation.EDUType(edu) {
	case "m.typing":
		return s.applyTypingEDU(peer, content)
	case "m.receipt":
		return s.applyReceiptEDU(peer, content)
	case "m.direct_to_device":
		return s.applyToDeviceEDU(peer, content)
	case "m.device_list_update":
		return s.applyDeviceListEDU(peer, content)
	case "m.signing_key_update":
		return s.applySigningKeyEDU(peer, content)
	case "m.presence":
		// Lattice keeps presence local. Accepting the message and doing nothing is the
		// correct answer for an EDU whose semantics this server does not implement; a
		// peer is not told it is unsupported, because the specification has no way to
		// say so and a peer that is told tends to keep asking.
		return nil
	default:
		return fmt.Errorf("this server does not know the ephemeral message type %q", federation.EDUType(edu))
	}
}

// applyTypingEDU records that a user of the sending server is, or is no longer, typing.
func (s *Service) applyTypingEDU(peer string, content matrix.Object) error {
	roomID, _ := content["room_id"].(string)
	userID, _ := content["user_id"].(string)
	if roomID == "" || userID == "" {
		return fmt.Errorf("a typing message must name a room and a user")
	}
	if err := belongsToServer(userID, peer); err != nil {
		return err
	}
	typing, _ := content["typing"].(bool)
	now := s.NowMS()
	expires := int64(0)
	if typing {
		// The typing EDU carries no timeout, unlike the client API. Two minutes is
		// longer than any client's typing indicator waits and shorter than the gap
		// between two keystrokes from a slow one, so the indicator does not linger.
		expires = now + typingEDULifetime
	}
	if err := s.db.TypingRepo.Set(&persist.Typing{
		RoomID:    roomID,
		UserID:    userID,
		Typing:    typing,
		SentTS:    now,
		ExpiresTS: expires,
		StreamPos: s.nextStreamPos(),
	}); err != nil {
		return fmt.Errorf("recording the typing state: %w", err)
	}
	return nil
}

// typingEDULifetime is how long a typing state from a peer is kept before it lapses on
// its own.
const typingEDULifetime = 2 * 60 * 1000

// applyReceiptEDU records read receipts for users of the sending server.
func (s *Service) applyReceiptEDU(peer string, content matrix.Object) error {
	applied := 0
	for roomID, receiptsAny := range content {
		roomReceipts, ok := asObject(receiptsAny)
		if !ok {
			continue
		}
		for receiptType, byUserAny := range roomReceipts {
			if receiptType != ReceiptRead {
				// m.read and threaded read receipts are what this server stores; a
				// receipt type it does not model is dropped rather than guessed at.
				continue
			}
			byUser, ok := asObject(byUserAny)
			if !ok {
				continue
			}
			for userID, entryAny := range byUser {
				if err := belongsToServer(userID, peer); err != nil {
					return err
				}
				entry, ok := asObject(entryAny)
				if !ok {
					continue
				}
				eventIDs, _ := entry["event_ids"].([]any)
				data, ok := asObject(entry["data"])
				if !ok {
					data = matrix.Object{}
				}
				record := &persist.Receipt{
					RoomID:      roomID,
					UserID:      userID,
					ReceiptType: receiptType,
					StreamPos:   s.nextStreamPos(),
					SentTS:      s.NowMS(),
				}
				if len(eventIDs) > 0 {
					record.EventID, _ = eventIDs[0].(string)
				}
				if threadID, _ := data["thread_id"].(string); threadID != "" {
					record.Data = string(mustCanonical(matrix.Object{"thread_id": threadID}))
				}
				if ts, err := number(data["ts"]); err == nil {
					record.SentTS = ts
				}
				if err := s.db.ReceiptRepo.Set(record); err != nil {
					return fmt.Errorf("recording the receipt of %s in %s: %w", userID, roomID, err)
				}
				applied++
			}
		}
	}
	if applied == 0 {
		return fmt.Errorf("the receipt message named no receipt this server can record")
	}
	return nil
}

// applyToDeviceEDU delivers a send-to-device message from a peer. This is the EDU that
// carries an Olm room key between a local and a remote device: refusing it locks both
// sides out of a conversation, which is why it is handled here rather than deferred.
func (s *Service) applyToDeviceEDU(peer string, content matrix.Object) error {
	messageID, _ := content["message_id"].(string)
	eventType, _ := content["type"].(string)
	sender, _ := content["sender"].(string)
	if messageID == "" || eventType == "" || sender == "" {
		return fmt.Errorf("a send-to-device message needs a message id, a type and a sender")
	}
	if len([]rune(messageID)) > 32 {
		return fmt.Errorf("the message id of a send-to-device message is at most 32 characters")
	}
	if err := belongsToServer(sender, peer); err != nil {
		return err
	}
	messages, ok := asObject(content["messages"])
	if !ok || len(messages) == 0 {
		return fmt.Errorf("the send-to-device message carries no messages")
	}
	queued := 0
	for userID, byDeviceAny := range messages {
		if matrix.DomainOf(userID) != s.serverName && matrix.DomainOf(userID) != peer {
			// Delivering to a third server's users is that server's job; a peer that
			// forwards traffic for someone else cannot be relied on to have the right
			// picture of who is where.
			continue
		}
		byDevice, ok := asObject(byDeviceAny)
		if !ok {
			continue
		}
		for deviceID, messageAny := range byDevice {
			body, ok := asObject(messageAny)
			if !ok {
				continue
			}
			// The message id is kept as the transaction of the row, so a retried
			// transaction - or a retried EDU inside it - cannot deliver the same room
			// key twice.
			if err := s.db.ToDeviceRepo.Set(&persist.ToDevice{
				SenderID:     sender,
				SenderDevice: "",
				TxnID:        messageID,
				TargetID:     userID,
				TargetDevice: deviceID,
				Type:         eventType,
				Content:      string(mustCanonical(body)),
				TS:           s.NowMS(),
				StreamPos:    s.nextStreamPos(),
			}); err != nil {
				return fmt.Errorf("queueing the send-to-device message for %s: %w", userID, err)
			}
			queued++
		}
	}
	if queued == 0 {
		return fmt.Errorf("the send-to-device message named no device of this server or of the sending server")
	}
	return nil
}

// applyDeviceListEDU folds one incremental change to a remote user's device list into
// this server's cache.
func (s *Service) applyDeviceListEDU(peer string, content matrix.Object) error {
	userID, _ := content["user_id"].(string)
	deviceID, _ := content["device_id"].(string)
	if userID == "" || deviceID == "" {
		return fmt.Errorf("a device list update must name a user and a device")
	}
	if err := belongsToServer(userID, peer); err != nil {
		return err
	}
	streamID, err := number(content["stream_id"])
	if err != nil {
		return fmt.Errorf("a device list update needs a stream id: %w", err)
	}
	var predecessors []int64
	if list, ok := content["prev_id"].([]any); ok {
		for _, entry := range list {
			if value, err := number(entry); err == nil {
				predecessors = append(predecessors, value)
			}
		}
	}
	device := &persist.RemoteDevice{
		UserID:   userID,
		DeviceID: deviceID,
		StreamID: streamID,
		SeenTS:   s.NowMS(),
		Origin:   peer,
	}
	if name, ok := content["device_display_name"].(string); ok {
		device.DisplayName = name
	}
	if deleted, ok := content["deleted"].(bool); ok {
		device.Deleted = deleted
	}
	if keys, ok := asObject(content["keys"]); ok {
		device.KeyJSON = string(mustCanonical(keys))
	}
	if device.Deleted {
		if err := s.db.RemoteDeviceRepo.Forget(userID, deviceID); err != nil {
			return fmt.Errorf("forgetting the device %s of %s: %w", deviceID, userID, err)
		}
	} else {
		if err := s.db.RemoteDeviceRepo.Set(device); err != nil {
			return fmt.Errorf("caching the device %s of %s: %w", deviceID, userID, err)
		}
	}
	stream, err := s.db.DeviceStreamRepo.Advance(userID, streamID, predecessors, s.NowMS())
	if err != nil {
		return fmt.Errorf("recording the device list position of %s: %w", userID, err)
	}
	if stream.GapSeen {
		// The update named a predecessor this server never received, so the cache of
		// this user's devices is missing something. The repair - re-querying the whole
		// device list - belongs with the endpoints that fetch device keys.
		log.Printf("federation: the device list of %s has a gap on record; it needs re-querying", userID)
	}
	return nil
}

// applySigningKeyEDU records the cross-signing keys a peer announces for one of its
// users. The keys are stored unverified, as they arrive: whether to trust them is the
// user's decision, made on their own devices, and this server's job is to pass the
// evidence along unchanged.
func (s *Service) applySigningKeyEDU(peer string, content matrix.Object) error {
	userID, _ := content["user_id"].(string)
	if userID == "" {
		return fmt.Errorf("a signing key update must name a user")
	}
	if err := belongsToServer(userID, peer); err != nil {
		return err
	}
	stored := 0
	for field, usage := range map[string]string{
		"master_key":       "master",
		"self_signing_key": "self_signing",
		"user_signing_key": "user_signing",
	} {
		key, ok := asObject(content[field])
		if !ok {
			continue
		}
		keyID, err := crossSigningKeyID(key)
		if err != nil {
			return fmt.Errorf("the %s of %s is unusable: %w", field, userID, err)
		}
		if err := s.db.CrossSigningRepo.Set(&persist.CrossSigningKey{
			UserID:      userID,
			KeyID:       keyID,
			Usage:       usage,
			Data:        string(mustCanonical(key)),
			FirstSeenMS: s.NowMS(),
		}); err != nil {
			return fmt.Errorf("recording the %s of %s: %w", field, userID, err)
		}
		stored++
	}
	if stored == 0 {
		return fmt.Errorf("the signing key update carried no key")
	}
	return nil
}

// crossSigningKeyID reads the id of a cross-signing key from its keys object, which must
// hold exactly one property named <algorithm>:<unpadded base64 public key>.
func crossSigningKeyID(key matrix.Object) (string, error) {
	keys, ok := asObject(key["keys"])
	if !ok || len(keys) != 1 {
		return "", fmt.Errorf("a cross-signing key must carry exactly one public key")
	}
	for keyID := range keys {
		if !strings.Contains(keyID, ":") {
			return "", fmt.Errorf("the key id %q does not name an algorithm", keyID)
		}
		return keyID, nil
	}
	return "", fmt.Errorf("unreachable")
}

// belongsToServer reports whether a user ID names a user of the given server. A server
// that accepts statements about somebody else's users is agreeing to relay forgeries:
// the typing indicator, the device list and the receipts of a user are their own
// server's to report.
func belongsToServer(userID, serverName string) error {
	if !strings.HasPrefix(userID, "@") {
		return fmt.Errorf("%q is not a user id", userID)
	}
	if matrix.DomainOf(userID) != serverName {
		return fmt.Errorf("%s is not a user of %s", userID, serverName)
	}
	return nil
}

// asObject reads a decoded JSON object, accepting the alias this codebase uses and the
// plain map a decoder produces.
func asObject(value any) (matrix.Object, bool) {
	if object, ok := value.(matrix.Object); ok {
		return object, true
	}
	if asMap, ok := value.(map[string]any); ok {
		return asMap, true
	}
	return nil, false
}

// number reads a JSON number without letting it pass through float64.
func number(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s is not a whole number", typed.String())
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%v is not a number", value)
	}
}

// mustCanonical encodes a value canonically, falling back to the empty object. It is
// used where a value came from decoded JSON and is being re-stored, so failure would
// mean the input was not JSON at all.
func mustCanonical(value any) []byte {
	encoded, err := matrix.CanonicalJSON(value)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}
