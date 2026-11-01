package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// Everything this server owes another server goes through one queue. The queue exists
// because federation traffic cannot be sent inline: the peer may be down, and a local
// user waiting for a slow server across the internet is a user whose message failed for a
// reason that has nothing to do with their homeserver. It also exists because the
// specification's delivery rule is a queue rule: a sender must retry the same transaction
// id and wait for the answer before starting a different transaction to the same server,
// which is only implementable if the unit of work is kept somewhere durable.

// The outbox's timing. A destination that is not reachable is retried with exponential
// backoff from a floor to a ceiling: retrying faster than the floor turns a peer outage
// into a denial of service that this server is administering, and retrying slower than the
// ceiling means a recovered peer sits idle for minutes after it comes back.
const (
	outboxRetryFloor   = 5 * time.Second
	outboxRetryCeiling = 10 * time.Minute
	// outboxDestinationsPerFlush bounds one pass, so one unreachable peer cannot make
	// a flush run past the lifetime of the request that triggered it.
	outboxDestinationsPerFlush = 16
)

// SetFederationTransport hands the service the signed client it uses to talk to peers.
// The transport is built by the API layer because it is configured there, and it takes
// the service as its identity - the pair is a cycle only in construction, never in time:
// the transport sends nothing until a route or the outbox asks it to.
func (s *Service) SetFederationTransport(transport *federation.Transport) {
	s.federationMu.Lock()
	defer s.federationMu.Unlock()
	s.outboxTraffic = transport
}

// federationTransport returns the outbound client, or nil when this server does not
// speak federation.
func (s *Service) federationTransport() *federation.Transport {
	if !s.federationEnabled {
		return nil
	}
	s.federationMu.Lock()
	defer s.federationMu.Unlock()
	return s.outboxTraffic
}

// outboxWanted reports whether traffic should be queued for peers at all. With
// -federation off this server has nobody to talk to, and queueing would be a table of
// rows that can only ever be pruned.
func (s *Service) outboxWanted() bool {
	return s.federationTransport() != nil
}

// destinationsOfRoom lists the servers that have a joined user in a room, which is the
// set a room's events have to reach. This server is left out: it already has the event,
// and a server the room has excluded by ACL is left out too.
//
// The ACL is applied here rather than only on arrival because a room cannot uninvite a
// server that keeps relaying its events: the rule works by every server refusing to carry
// what the room forbids, including the servers that agree with the room.
func (s *Service) destinationsOfRoom(ctx context.Context, roomID string) ([]string, error) {
	members, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	destinations := serverNamesOf(members, s.serverName)
	acl, err := s.roomServerACL(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if len(acl) == 0 {
		return destinations, nil
	}
	allowed := make([]string, 0, len(destinations))
	for _, destination := range destinations {
		if federation.ServerAllowed(acl, destination) {
			allowed = append(allowed, destination)
		}
	}
	return allowed, nil
}

// destinationsOfRoomIgnoringACL is the same list before the room's ACL is applied.
func (s *Service) destinationsOfRoomIgnoringACL(ctx context.Context, roomID string) ([]string, error) {
	members, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return serverNamesOf(members, s.serverName), nil
}

// roomServerACL reads the m.room.server_acl state of a room, or an empty object when the
// room has none.
func (s *Service) roomServerACL(ctx context.Context, roomID string) (matrix.Object, error) {
	event, err := s.db.StateRepo.Get(roomID, matrix.TypeServerACL, "")
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, nil
		}
		return nil, matrix.ErrInternal(err)
	}
	// The content column holds the content object of the event, which is where the
	// allow and deny lists are.
	content := matrix.Object{}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		// An ACL that cannot be read is an ACL that cannot be applied. The safe reading
		// of that is to say nothing rather than to let everything through, and to say it
		// loudly enough that an operator notices their room is not being policed.
		log.Printf("federation: the server ACL of %s cannot be read: %v", roomID, err)
		return nil, nil
	}
	return content, nil
}

// destinationsOfUser lists the servers that share a room with a user. Device list updates
// go to exactly those servers, because they are the servers that would otherwise send an
// encrypted message to a device that has been replaced.
//
// A server that shares no room with the user is not told about the user's devices. That is
// the narrower rule the specification allows - it asks for updates to the servers that
// already receive them - and the wider alternative would let any peer that learns a user
// id subscribe to that user's device history.
func (s *Service) destinationsOfUser(ctx context.Context, userID string) ([]string, error) {
	rooms, err := s.db.MembershipRepo.RoomsFor(userID, matrix.MembershipJoin)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, roomID := range rooms {
		destinations, err := s.destinationsOfRoom(ctx, roomID)
		if err != nil {
			return nil, err
		}
		for _, destination := range destinations {
			if destination == matrix.DomainOf(userID) || seen[destination] {
				continue
			}
			seen[destination] = true
			out = append(out, destination)
		}
	}
	return out, nil
}

// serverNamesOf reduces membership rows to the distinct server names in them.
func serverNamesOf(members []*persist.RoomMembership, except string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, member := range members {
		serverName := matrix.DomainOf(member.UserID)
		if serverName == "" || serverName == except || seen[serverName] {
			continue
		}
		seen[serverName] = true
		out = append(out, serverName)
	}
	return out
}

// queueForPeers adds items to the outbox. A failure here is logged rather than returned:
// a local user whose message could not be copied to a peer has sent a message that
// worked, and the room on the other side is this server's problem to repair, not the
// user's to be told about.
func (s *Service) queueForPeers(items ...*persist.OutboxItem) {
	if !s.outboxWanted() || len(items) == 0 {
		return
	}
	now := s.NowMS()
	for _, item := range items {
		if item == nil {
			continue
		}
		if item.CreatedTS == 0 {
			item.CreatedTS = now
		}
		if item.EdgeID == "" {
			item.EdgeID = federation.NewTxnID(time.UnixMilli(item.CreatedTS))
		}
	}
	added, err := s.db.OutboxRepo.Append(items...)
	if err != nil {
		log.Printf("federation: could not queue %d item(s) for peers: %v", len(items), err)
		return
	}
	if added < len(items) {
		log.Printf("federation: %d of %d queued item(s) were already waiting", len(items)-added, len(items))
	}
}

// queueEventForPeers queues a locally created event for every server in its room, in the
// form that room's version defines.
//
// The form matters: room version 3 and later carry no event id in the event, and the
// client-facing copy has one added on the way out. Sending that copy would describe an
// event whose id is part of its own hash.
func (s *Service) queueEventForPeers(ctx context.Context, event *persist.Event, version string) {
	if !s.outboxWanted() || event == nil || event.JSON == "" {
		return
	}
	destinations, err := s.destinationsOfRoom(ctx, event.RoomID)
	if err != nil {
		return
	}
	if event.Type == matrix.TypeServerACL {
		// The ACL itself travels to every server in the room, including the ones it
		// excludes. A ban the banned server is never told about is not a ban: that
		// server keeps relaying the room to everybody else, and the only software that
		// can make it stop is its own.
		withACL, err := s.destinationsOfRoomIgnoringACL(ctx, event.RoomID)
		if err == nil {
			destinations = withACL
		}
	}
	if len(destinations) == 0 {
		return
	}
	pdu, err := transportPDU(event.JSON, version)
	if err != nil {
		log.Printf("federation: the event %s cannot be prepared for peers: %v", event.EventID, err)
		return
	}
	encoded, err := matrix.CanonicalJSON(pdu)
	if err != nil {
		log.Printf("federation: the event %s cannot be encoded for peers: %v", event.EventID, err)
		return
	}
	items := make([]*persist.OutboxItem, 0, len(destinations))
	for _, destination := range destinations {
		items = append(items, &persist.OutboxItem{
			Destination: destination,
			Kind:        "pdu",
			RoomID:      event.RoomID,
			EventID:     event.EventID,
			// The edge is the event and the destination: an event is delivered to a
			// server once, however many rooms and retries carry it there.
			EdgeID:  "pdu:" + destination + ":" + event.EventID,
			Payload: string(encoded),
		})
	}
	s.queueForPeers(items...)
}

// transportPDU renders a stored event as the PDU of its room version.
func transportPDU(stored string, version string) (matrix.Object, error) {
	decoded, err := matrix.DecodeCanonical([]byte(stored))
	if err != nil {
		return nil, fmt.Errorf("the stored event cannot be decoded: %w", err)
	}
	pdu, ok := decoded.(matrix.Object)
	if !ok {
		return nil, fmt.Errorf("the stored event is not an object")
	}
	// Room version 3 and later do not carry an event id in the event: the id is the hash
	// of the event, so an id inside the event would be part of what it hashes to.
	if usesDerivedEventIDs(version) {
		delete(pdu, "event_id")
	}
	return pdu, nil
}

// queueEDUForDestinations queues one ephemeral message for a set of servers.
func (s *Service) queueEDUForDestinations(destinations []string, eduType, edge string, content matrix.Object) {
	if !s.outboxWanted() || len(destinations) == 0 {
		return
	}
	encoded, err := matrix.CanonicalJSON(matrix.Object{"edu_type": eduType, "content": content})
	if err != nil {
		log.Printf("federation: the %s message cannot be encoded: %v", eduType, err)
		return
	}
	items := make([]*persist.OutboxItem, 0, len(destinations))
	for _, destination := range destinations {
		items = append(items, &persist.OutboxItem{
			Destination: destination,
			Kind:        "edu",
			EdgeID:      edgeFor(edge, destination, eduType),
			Payload:     string(encoded),
		})
	}
	s.queueForPeers(items...)
}

// edgeFor builds the de-duplication key of an ephemeral message. An empty edge means the
// message has no natural identity - a typing indicator is the same statement whenever it
// is sent - and the caller says so explicitly rather than leaving it to a random id.
func edgeFor(edge, destination, eduType string) string {
	if edge == "" {
		return ""
	}
	return "edu:" + destination + ":" + eduType + ":" + edge
}

// queueTypingForPeers tells the servers in a room that one of this server's users is, or
// is no longer, typing.
func (s *Service) queueTypingForPeers(ctx context.Context, roomID, userID string, typing bool) {
	if !s.outboxWanted() {
		return
	}
	destinations, err := s.destinationsOfRoom(ctx, roomID)
	if err != nil || len(destinations) == 0 {
		return
	}
	// The typing EDU describes a state rather than an event, so there is nothing to
	// de-duplicate: the same statement twice is the same statement, and a server that
	// stops typing has to be able to say so again.
	s.queueEDUForDestinations(destinations, "m.typing", "", matrix.Object{
		"room_id": roomID,
		"user_id": userID,
		"typing":  typing,
	})
}

// queueReceiptForPeers tells the servers in a room that one of this server's users read
// an event.
func (s *Service) queueReceiptForPeers(ctx context.Context, roomID, userID, receiptType, eventID string, ts int64, data matrix.Object) {
	if !s.outboxWanted() || receiptType != ReceiptRead {
		// Only read receipts travel over federation in this server's vocabulary;
		// read markers and the newer receipt types have no EDU of their own yet.
		return
	}
	destinations, err := s.destinationsOfRoom(ctx, roomID)
	if err != nil || len(destinations) == 0 {
		return
	}
	reader := matrix.Object{"ts": ts}
	if threadID, _ := data["thread_id"].(string); threadID != "" {
		reader["thread_id"] = threadID
	}
	// The edge carries the thread, because a receipt moved within a thread is a
	// different statement from the same receipt unthreaded.
	edge := eventID
	if thread, _ := data["thread_id"].(string); thread != "" {
		edge = eventID + ":" + thread
	}
	s.queueEDUForDestinations(destinations, "m.receipt", edge, matrix.Object{
		roomID: matrix.Object{
			receiptType: matrix.Object{
				userID: matrix.Object{
					"data":      reader,
					"event_ids": []any{eventID},
				},
			},
		},
	})
}

// queueToDeviceForPeers forwards a send-to-device message to the servers of the users it
// names. This is the path an Olm handshake takes between two homeservers: without it a
// local device and a remote device can never open a session, and both sides report an
// unreadable conversation.
func (s *Service) queueToDeviceForPeers(ctx context.Context, senderID, senderDevice, eventType, txnID string, messages map[string]map[string]matrix.RawMessage) {
	if !s.outboxWanted() {
		return
	}
	byServer := matrix.Object{}
	for target, byDevice := range messages {
		serverName := matrix.DomainOf(target)
		if serverName == "" || serverName == s.serverName {
			continue
		}
		entries, ok := byServer[serverName].(matrix.Object)
		if !ok {
			entries = matrix.Object{}
			byServer[serverName] = entries
		}
		devices, ok := entries[target].(matrix.Object)
		if !ok {
			devices = matrix.Object{}
			entries[target] = devices
		}
		for device, content := range byDevice {
			if device == "" {
				device = "*"
			}
			body, err := matrix.DecodeCanonical(content)
			if err != nil {
				log.Printf("federation: the %s message for %s is not JSON: %v", eventType, target, err)
				continue
			}
			devices[device] = body
		}
	}
	for serverName, messages := range byServer {
		entries, ok := messages.(matrix.Object)
		if !ok || len(entries) == 0 {
			continue
		}
		// The client's transaction id names the message on the wire, which is what
		// makes a retried PUT /sendToDevice deliver a room key once rather than as many
		// times as the client retried.
		s.queueEDUForDestinations([]string{serverName}, "m.direct_to_device", txnID, matrix.Object{
			"message_id": txnID,
			"sender":     senderID,
			"type":       eventType,
			"messages":   entries,
		})
	}
}

// queueDeviceListForPeers announces a change to one of this server's user's devices.
func (s *Service) queueDeviceListForPeers(ctx context.Context, userID, deviceID string, deleted bool, streamID int64) {
	if !s.outboxWanted() {
		return
	}
	destinations, err := s.destinationsOfUser(ctx, userID)
	if err != nil || len(destinations) == 0 {
		return
	}
	content := matrix.Object{
		"user_id":   userID,
		"stream_id": streamID,
	}
	if deviceID != "" {
		content["device_id"] = deviceID
	}
	if previous, err := s.db.DeviceStreamRepo.Get(userID); err == nil && previous.StreamID > 0 {
		// The EDU names the update it builds on, so a receiver that missed one knows it
		// has to re-query rather than quietly applying a partial picture.
		content["prev_id"] = []any{previous.StreamID}
	}
	if deleted {
		content["deleted"] = true
	} else if deviceID != "" {
		if keys, err := s.db.DeviceKeyRepo.Get(userID, deviceID); err == nil && keys.KeyJSON != "" {
			if object, err := matrix.DecodeCanonical([]byte(keys.KeyJSON)); err == nil {
				content["keys"] = object
			}
		}
		if device, err := s.db.DeviceRepo.Get(userID, deviceID); err == nil && device.DisplayName != "" {
			content["device_display_name"] = device.DisplayName
		}
	}
	if err := s.db.DeviceStreamRepo.Clear(userID, streamID, s.NowMS()); err != nil {
		log.Printf("federation: could not record the device list position of %s: %v", userID, err)
	}
	s.queueEDUForDestinations(destinations, "m.device_list_update", fmt.Sprintf("%s:%d", deviceID, streamID), content)
}

// queueSigningKeysForPeers announces new cross-signing keys for one of this server's
// users. The keys are forwarded as the user's own devices uploaded them, signatures and
// all: this server cannot vouch for them and must not change what it cannot vouch for.
func (s *Service) queueSigningKeysForPeers(ctx context.Context, userID string) {
	if !s.outboxWanted() {
		return
	}
	destinations, err := s.destinationsOfUser(ctx, userID)
	if err != nil || len(destinations) == 0 {
		return
	}
	keys, err := s.db.CrossSigningRepo.List(userID)
	if err != nil || len(keys) == 0 {
		return
	}
	content := matrix.Object{"user_id": userID}
	for _, key := range keys {
		object, err := matrix.DecodeCanonical([]byte(key.Data))
		if err != nil {
			continue
		}
		switch key.Usage {
		case "master":
			content["master_key"] = object
		case "self_signing":
			content["self_signing_key"] = object
		case "user_signing":
			content["user_signing_key"] = object
		}
	}
	if len(content) == 1 {
		return
	}
	s.queueEDUForDestinations(destinations, "m.signing_key_update", fmt.Sprintf("%d", s.NowMS()), content)
}

// FlushFederationOutbox delivers what is waiting, and reports how many transactions were
// attempted. It is called by the background loop and by an operator or a test that wants
// the queue drained now.
func (s *Service) FlushFederationOutbox(ctx context.Context) (int, error) {
	transport := s.federationTransport()
	if transport == nil {
		return 0, nil
	}
	destinations, err := s.db.OutboxRepo.DueDestinations(s.NowMS(), outboxDestinationsPerFlush)
	if err != nil {
		return 0, matrix.ErrInternal(err)
	}
	if len(destinations) == 0 {
		return 0, nil
	}
	// Destinations are worked on concurrently, and one at a time within each: a peer
	// that is asleep for half a minute must not delay another peer's queue, and the
	// specification's one-outstanding-transaction rule means two transactions to the
	// same server are never in flight together.
	attempted := 0
	var counted sync.Mutex
	var waiting sync.WaitGroup
	for _, destination := range destinations {
		destination := destination
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			sent, err := s.flushDestination(ctx, transport, destination)
			counted.Lock()
			attempted += sent
			counted.Unlock()
			if err != nil && ctx.Err() == nil {
				// A single destination's failure is reported and does not disturb the
				// others: an unreachable peer is a normal state of the internet.
				log.Printf("federation: delivering to %s: %v", destination, err)
			}
		}()
	}
	waiting.Wait()
	return attempted, nil
}

// flushDestination moves one destination's queue forward by a single transaction.
func (s *Service) flushDestination(ctx context.Context, transport *federation.Transport, destination string) (int, error) {
	if lock := s.outboxLock(destination); lock != nil {
		// One attempt per destination at a time, from this process: two in-flight
		// transactions to one server would break the ordering the specification asks
		// for, and would make a retry ambiguous about which attempt the answer is for.
		lock.Lock()
		defer lock.Unlock()
	}

	if outstanding, err := s.db.OutboxRepo.Outstanding(destination); err == nil {
		return s.attemptTransaction(ctx, transport, outstanding)
	} else if !persist.IsNotFound(err) {
		return 0, matrix.ErrInternal(err)
	}

	items, err := s.db.OutboxRepo.NextItems(destination, federation.MaxPDUsPerTransaction, federation.MaxEDUsPerTransaction)
	if err != nil {
		return 0, matrix.ErrInternal(err)
	}
	if len(items) == 0 {
		return 0, nil
	}
	pduList, eduList, ids := packedItems(items)
	txn := &persist.OutboundTxn{
		Destination: destination,
		TxnID:       federation.NewTxnID(time.UnixMilli(s.NowMS())),
		Payload:     string(packedPayload(s.serverName, s.NowMS(), pduList, eduList)),
		Items:       joinIDs(ids),
		PDUs:        len(pduList),
		EDUs:        len(eduList),
		Created:     s.NowMS(),
		NextTry:     s.NowMS(),
	}
	if err := s.db.OutboxRepo.Pack(txn); err != nil {
		return 0, matrix.ErrInternal(err)
	}
	return s.attemptTransaction(ctx, transport, txn)
}

// attemptTransaction puts one transaction on the wire, and records either the answer or
// the next attempt. The bytes of a retry are the bytes of the first attempt, which is
// what lets the receiver's transaction log do its job.
func (s *Service) attemptTransaction(ctx context.Context, transport *federation.Transport, txn *persist.OutboundTxn) (int, error) {
	body, err := matrix.DecodeCanonical([]byte(txn.Payload))
	if err != nil {
		return 1, fmt.Errorf("the queued transaction %s cannot be decoded: %w", txn.TxnID, err)
	}
	payload, ok := body.(matrix.Object)
	if !ok {
		return 1, fmt.Errorf("the queued transaction %s is not an object", txn.TxnID)
	}
	// The origin and timestamp of a retry are kept as they were when the transaction was
	// made: a retry is the same request, and a receiver that compares what it answers
	// with what was asked should be able to.
	answer, err := transport.SendTransaction(ctx, txn.Destination, &federation.Transaction{
		TxnID:          txn.TxnID,
		Origin:         payload["origin"].(string),
		OriginServerTS: payloadOriginTS(payload),
		PDUs:           payloadObjects(payload["pdus"]),
		EDUs:           payloadObjects(payload["edus"]),
	})
	if err != nil {
		next := s.retryDelay(txn.Tries)
		if markErr := s.db.OutboxRepo.Retry(txn.Destination, txn.TxnID, next, truncate(err.Error(), 255)); markErr != nil {
			log.Printf("federation: could not record the retry of %s to %s: %v", txn.TxnID, txn.Destination, markErr)
		}
		return 1, err
	}
	// A per-event problem is not a transport failure: the transaction arrived, was
	// understood, and the peer said what it did with each event. Retrying it would be
	// wrong - the answer is the same every time - and staying silent would leave an
	// operator with a room that is missing messages and no trace of why.
	for _, problem := range answer.Rejections() {
		log.Printf("federation: %s refused an event from this server: %s", txn.Destination, problem)
	}
	if err := s.db.OutboxRepo.Acknowledge(txn.Destination, txn.TxnID); err != nil {
		return 1, matrix.ErrInternal(err)
	}
	return 1, nil
}

// retryDelay is the exponential backoff of a destination, in milliseconds since the
// epoch.
//
// A slice of the delay is subtracted at random. Servers that all lose the same peer come
// back in step without it, and a fleet of homeservers retrying one recovering server in
// the same second is an outage that the recovering server causes itself.
func (s *Service) retryDelay(tries int) int64 {
	delay := outboxRetryFloor
	for i := 0; i < tries && delay < outboxRetryCeiling; i++ {
		delay *= 2
	}
	if delay > outboxRetryCeiling {
		delay = outboxRetryCeiling
	}
	if jitter := delay / 4; jitter > 0 {
		if n, err := rand.Int(rand.Reader, big.NewInt(int64(jitter))); err == nil {
			delay -= time.Duration(n.Int64()) * time.Millisecond
		}
	}
	return s.NowMS() + delay.Milliseconds()
}

// StartFederationOutbox drains the outbox on a timer until the context is cancelled or
// the returned stop function is called.
func (s *Service) StartFederationOutbox(ctx context.Context, period time.Duration) func() {
	if period <= 0 || !s.outboxWanted() {
		return func() {}
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if _, err := s.FlushFederationOutbox(loopCtx); err != nil {
					log.Printf("federation: flushing the outbox: %v", err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// outboxLock keeps one attempt per destination in flight within this process.
func (s *Service) outboxLock(destination string) *sync.Mutex {
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if s.outboxLocks == nil {
		s.outboxLocks = map[string]*sync.Mutex{}
	}
	lock, ok := s.outboxLocks[destination]
	if !ok {
		lock = &sync.Mutex{}
		s.outboxLocks[destination] = lock
	}
	return lock
}

// packedItems sorts queued items into the two lists a transaction carries, keeping the
// ids of the items the transaction covers.
func packedItems(items []*persist.OutboxItem) (pdus, edus matrix.Array, ids []uint) {
	pdus = matrix.Array{}
	edus = matrix.Array{}
	for _, item := range items {
		body, err := matrix.DecodeCanonical([]byte(item.Payload))
		if err != nil {
			log.Printf("federation: dropping a queued %s for %s that cannot be decoded: %v",
				item.Kind, item.Destination, err)
			continue
		}
		if item.Kind == "pdu" {
			pdus = append(pdus, body)
		} else {
			edus = append(edus, body)
		}
		ids = append(ids, item.ID)
	}
	return pdus, edus, ids
}

// packedPayload renders the transaction body for the wire.
func packedPayload(serverName string, now int64, pdus, edus matrix.Array) []byte {
	body := matrix.Object{
		// The origin is this server - the same name the signature is made by. A
		// transaction whose body names somebody else is refused by the receiver, and
		// naming the *destination* here is the easy mistake, since the destination is the
		// value at hand when the row was written.
		"origin":           serverName,
		"origin_server_ts": now,
		"pdus":             pdus,
	}
	if len(edus) > 0 {
		body["edus"] = edus
	}
	encoded, err := matrix.CanonicalJSON(body)
	if err != nil {
		return []byte(`{"pdus":[]}`)
	}
	return encoded
}

// joinIDs renders item ids for storage.
func joinIDs(ids []uint) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprint(id))
	}
	return strings.Join(parts, ",")
}

// payloadObjects reads an array of objects out of a stored transaction.
func payloadObjects(value any) []matrix.Object {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]matrix.Object, 0, len(list))
	for _, entry := range list {
		if object, ok := entry.(matrix.Object); ok {
			out = append(out, object)
		} else if asMap, mapOK := entry.(map[string]any); mapOK {
			out = append(out, asMap)
		}
	}
	return out
}

// payloadOriginTS reads the timestamp of a stored transaction.
func payloadOriginTS(payload matrix.Object) int64 {
	switch typed := payload["origin_server_ts"].(type) {
	case int64:
		return typed
	case json.Number:
		value, err := typed.Int64()
		if err == nil {
			return value
		}
	}
	return 0
}

// truncate shortens a message for a column.
func truncate(message string, limit int) string {
	if len(message) <= limit {
		return message
	}
	return message[:limit]
}
