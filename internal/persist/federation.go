package persist

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Federation needs four kinds of memory that a single-server homeserver has no use
// for: the transactions it has already answered, the events it has been sent but not
// yet placed in a room, the traffic it owes to other servers, and the devices that
// live elsewhere. Each is a table of its own below, and each exists because the fact
// it holds cannot be derived from the rest of the database after the fact.

// InboundTxn records a transaction a peer sent and the answer this server gave it.
//
// A transaction id is the sender's unit of idempotence: it retries the same id until a
// 200 arrives, so a receiver that answers the second attempt as if it were the first
// applies every event in the transaction twice. In a room that cannot be undone - the
// duplicate has its own id, its own place in the graph, and its own push notification.
// Keeping the answer rather than a flag means the retry is answered with what was
// actually said the first time, including per-event errors.
type InboundTxn struct {
	// Origin and TxnID together are the key. Transaction ids only have to be unique
	// per sending server, so the pair is the only correct identity for a row.
	Origin string `json:"origin" gorm:"uniqueIndex:uq_inbound_txn,priority:1;size:255"`
	TxnID  string `json:"txn_id" gorm:"uniqueIndex:uq_inbound_txn,priority:2;size:255"`
	// Result is the response body this server returned, in canonical-ready JSON.
	Result string `json:"-" gorm:"type:text"`
	// PDUs and EDUs count what the transaction carried, which is what an operator
	// comparing two servers' logs reads to tell a refused batch from an empty one.
	PDUs       int   `json:"pdus"`
	EDUs       int   `json:"edus"`
	ReceivedTS int64 `json:"received_ts" gorm:"index"`

	gorm.Model
}

// InboundTxnRepo is the contract for the transaction log of the server-server API.
type InboundTxnRepo interface {
	// Get returns the transaction, or a not-found error that [IsNotFound] reports.
	Get(origin, txnID string) (*InboundTxn, error)
	// Store remembers a transaction. A conflict changes nothing: two peers can retry
	// the same transaction at the same moment, and the first answer wins.
	Store(txn *InboundTxn) error
	// Prune forgets transactions received before the given timestamp and reports how
	// many rows went away. The window only has to outlive the sender's retries, so a
	// day is the same bound the client transaction log uses.
	Prune(beforeTS int64) (int64, error)
}

// InboundTxnRepoImpl is the GORM backed InboundTxnRepo.
type InboundTxnRepoImpl struct {
	db *gorm.DB
}

// Get implements [InboundTxnRepo].
func (r *InboundTxnRepoImpl) Get(origin, txnID string) (*InboundTxn, error) {
	var txn InboundTxn
	if err := r.db.Where("origin = ? AND txn_id = ?", origin, txnID).First(&txn).Error; err != nil {
		return nil, err
	}
	return &txn, nil
}

// Store implements [InboundTxnRepo].
func (r *InboundTxnRepoImpl) Store(txn *InboundTxn) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "origin"}, {Name: "txn_id"}},
		DoNothing: true,
	}).Create(txn).Error
}

// Prune implements [InboundTxnRepo].
func (r *InboundTxnRepoImpl) Prune(beforeTS int64) (int64, error) {
	result := r.db.Where("received_ts < ?", beforeTS).Delete(&InboundTxn{})
	return result.RowsAffected, result.Error
}

// NewInboundTxnRepo returns the InboundTxnRepo implementation for db.
func NewInboundTxnRepo(db *gorm.DB) InboundTxnRepo { return &InboundTxnRepoImpl{db: db} }

// ReceivedPDU is an event a peer sent that this server has stored but not yet placed
// in a room.
//
// The event arrives inside a transaction and has to survive the transaction: the peer
// is told it was handled, so it will not send it again, and an event that is dropped
// between the ack and the room is missing from the room forever. It is kept in the form
// it arrived in, for the same reason the local events are - the signature and the id
// belong to those exact bytes.
type ReceivedPDU struct {
	// EventID is unique because the same event may be sent to this server by several
	// peers, or twice by one peer through different transactions. A duplicate is not
	// an error; a second copy is.
	EventID string `json:"event_id" gorm:"uniqueIndex:uq_received_pdu_event;size:255"`
	RoomID  string `json:"room_id" gorm:"index;size:255"`
	Origin  string `json:"origin" gorm:"size:255"`
	TxnID   string `json:"txn_id" gorm:"size:255"`
	// RoomVersion is the version of the room the event was named under. The version
	// decides the name: an event id in room version 3 is spelled differently from one in
	// version 4 and later. A room this server had never heard of is named with the
	// version this server creates rooms with, recorded here so the join that learns the
	// truth can see which rows were guessed.
	RoomVersion string `json:"room_version" gorm:"size:32"`
	JSON        string `json:"-" gorm:"type:text"`
	Received    int64  `json:"received_ts" gorm:"index"`
	// Processed is set once the event has been put through the room's auth rules.
	Processed bool `json:"processed" gorm:"index"`
	// Outcome records what processing decided: stored, duplicate, soft-failed or
	// rejected, with the reason beside it. An event that was refused is kept, because
	// "why is this event not in my room" is the first question an operator asks about
	// a federation problem, and the answer is in the row.
	Outcome string `json:"outcome" gorm:"size:32"`
	Reason  string `json:"reason" gorm:"size:255"`

	gorm.Model
}

// ReceivedPDURepo is the contract for events received from peers.
type ReceivedPDURepo interface {
	// Store queues an event, reporting false when an event with that id is already
	// known - which is the ordinary case once two servers share a room.
	Store(pdu *ReceivedPDU) (bool, error)
	// Pending lists events that have not been processed, oldest first.
	Pending(limit int) ([]*ReceivedPDU, error)
	// Get returns one queued event by id.
	Get(eventID string) (*ReceivedPDU, error)
	// List returns the receipts this server holds, newest first, processed or not.
	// Pending answers "what is still waiting"; this answers "what have peers sent me",
	// which is the question that matters once an event is judged as it arrives.
	List(limit int) ([]*ReceivedPDU, error)
	// Mark records what processing decided.
	Mark(eventID, outcome, reason string) error
	// Count reports how many events are waiting, which is the number an operator
	// watches when a room is not catching up.
	Count() (int64, error)
	// Prune drops processed events older than the given timestamp.
	Prune(beforeTS int64) (int64, error)
}

// ReceivedPDURepoImpl is the GORM backed ReceivedPDURepo.
type ReceivedPDURepoImpl struct {
	db *gorm.DB
}

// Store implements [ReceivedPDURepo].
func (r *ReceivedPDURepoImpl) Store(pdu *ReceivedPDU) (bool, error) {
	result := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "event_id"}},
		DoNothing: true,
	}).Create(pdu)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// Pending implements [ReceivedPDURepo].
func (r *ReceivedPDURepoImpl) Pending(limit int) ([]*ReceivedPDU, error) {
	var pdus []*ReceivedPDU
	q := r.db.Where("processed = ?", false).Order("id asc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&pdus).Error
	return pdus, err
}

// Get implements [ReceivedPDURepo].
// List implements [ReceivedPDURepo].
func (r *ReceivedPDURepoImpl) List(limit int) ([]*ReceivedPDU, error) {
	var rows []*ReceivedPDU
	q := r.db.Order("received desc, id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&rows).Error
	return rows, err
}

func (r *ReceivedPDURepoImpl) Get(eventID string) (*ReceivedPDU, error) {
	var pdu ReceivedPDU
	if err := r.db.Where("event_id = ?", eventID).First(&pdu).Error; err != nil {
		return nil, err
	}
	return &pdu, nil
}

// Mark implements [ReceivedPDURepo].
func (r *ReceivedPDURepoImpl) Mark(eventID, outcome, reason string) error {
	return r.db.Model(&ReceivedPDU{}).Where("event_id = ?", eventID).
		Updates(map[string]any{"processed": true, "outcome": outcome, "reason": reason}).Error
}

// Count implements [ReceivedPDURepo].
func (r *ReceivedPDURepoImpl) Count() (int64, error) {
	var count int64
	err := r.db.Model(&ReceivedPDU{}).Where("processed = ?", false).Count(&count).Error
	return count, err
}

// Prune implements [ReceivedPDURepo].
func (r *ReceivedPDURepoImpl) Prune(beforeTS int64) (int64, error) {
	result := r.db.Where("processed = ? AND received < ?", true, beforeTS).Delete(&ReceivedPDU{})
	return result.RowsAffected, result.Error
}

// NewReceivedPDURepo returns the ReceivedPDURepo implementation for db.
func NewReceivedPDURepo(db *gorm.DB) ReceivedPDURepo { return &ReceivedPDURepoImpl{db: db} }

// OutboxItem is one PDU or EDU this server owes another server.
//
// Items are stored individually rather than as ready-made transactions because the
// batch is a transport detail: what has to survive a restart is the list of things that
// need to go to a server, and which of them have already been put on the wire.
type OutboxItem struct {
	// Destination is the server name this item is addressed to, which is the name the
	// signature and the URL are built from - not a hostname.
	Destination string `json:"destination" gorm:"index:idx_outbox_destination,priority:1;size:255"`
	// Kind is "pdu" or "edu". The two have separate limits inside a transaction, so
	// packing has to tell them apart.
	Kind string `json:"kind" gorm:"index:idx_outbox_destination,priority:2;size:8"`
	// RoomID and EventID name the payload for an operator's log and for the room
	// access control list; the payload itself is what is sent.
	RoomID  string `json:"room_id" gorm:"size:255"`
	EventID string `json:"event_id" gorm:"size:255"`
	// EdgeID makes an item idempotent per destination: an event sent twice to the same
	// server is one item, and so is a to-device message with a message id the sender
	// already used. Items with no natural identity get a random one, which means "no
	// de-duplication intended" without colliding with each other.
	EdgeID string `json:"edge_id" gorm:"uniqueIndex:uq_outbox_edge;size:255"`
	// Payload is the PDU or EDU, encoded as JSON.
	Payload   string `json:"-" gorm:"type:text"`
	CreatedTS int64  `json:"created_ts" gorm:"index:idx_outbox_destination,priority:3"`

	gorm.Model
}

// OutboundTxn is a transaction this server has put on the wire and not yet had
// acknowledged.
//
// The specification allows one outstanding transaction per destination: a sender must
// retry the same id and wait for the 200 before sending a different one. That makes the
// retry trivially correct - same path, same id, same bytes - and it is why the queue
// holds at most one of these per destination rather than a pile of them.
type OutboundTxn struct {
	Destination string `json:"destination" gorm:"uniqueIndex:uq_outbound_txn,priority:1;size:255"`
	TxnID       string `json:"txn_id" gorm:"uniqueIndex:uq_outbound_txn,priority:2;size:255"`
	// Payload is the whole transaction body, kept so a retry sends the same bytes
	// rather than a reconstruction of what the items said at the time.
	Payload string `json:"-" gorm:"type:text"`
	// Items lists the outbox rows this transaction covers, so a 200 can retire exactly
	// those and no others that arrived while it was in flight.
	Items   string `json:"items" gorm:"type:text"`
	PDUs    int    `json:"pdus"`
	EDUs    int    `json:"edus"`
	Tries   int    `json:"tries"`
	NextTry int64  `json:"next_try"`
	LastErr string `json:"last_err" gorm:"size:255"`
	Created int64  `json:"created"`
	gorm.Model
}

// OutboxRepo is the contract for traffic this server owes its peers.
type OutboxRepo interface {
	// Append queues items, ignoring items already queued under the same edge. It
	// reports how many rows were actually added.
	Append(items ...*OutboxItem) (int, error)
	// Outstanding returns the transaction in flight for a destination, or a
	// not-found error when nothing is in flight.
	Outstanding(destination string) (*OutboundTxn, error)
	// Pack moves queued items into the transaction in flight for a destination.
	Pack(txn *OutboundTxn) error
	// DueDestinations lists destinations that have work ready now, honouring the
	// backoff recorded on an outstanding transaction.
	DueDestinations(now int64, limit int) ([]string, error)
	// Acknowledge retires a transaction and the items it carried.
	Acknowledge(destination, txnID string) error
	// Retry records a failed attempt and when to make the next one.
	Retry(destination, txnID string, nextTry int64, lastErr string) error
	// NextItems lists queued items of a destination in the order they were created,
	// stopping at the per-transaction limits.
	NextItems(destination string, maxPDUs, maxEDUs int) ([]*OutboxItem, error)
	// Count reports how many items are waiting, for an operator or a test.
	Count() (int64, error)
	// Prune drops queued items older than the given timestamp and reports how many
	// went away. Backlog that a peer will not receive is not worth keeping forever: a
	// PDU that could not be delivered for a day is a PDU the rest of the room has
	// moved past, and it can be fetched through the room's graph instead.
	Prune(beforeTS int64) (int64, error)
}

// OutboxRepoImpl is the GORM backed OutboxRepo.
type OutboxRepoImpl struct {
	db *gorm.DB
}

// Append implements [OutboxRepo].
func (r *OutboxRepoImpl) Append(items ...*OutboxItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&items)
	if result.Error != nil {
		return 0, result.Error
	}
	return int(result.RowsAffected), nil
}

// Outstanding implements [OutboxRepo].
func (r *OutboxRepoImpl) Outstanding(destination string) (*OutboundTxn, error) {
	var txn OutboundTxn
	if err := r.db.Where("destination = ?", destination).Order("id asc").First(&txn).Error; err != nil {
		return nil, err
	}
	return &txn, nil
}

// Pack implements [OutboxRepo].
func (r *OutboxRepoImpl) Pack(txn *OutboundTxn) error {
	return r.db.Create(txn).Error
}

// DueDestinations implements [OutboxRepo].
func (r *OutboxRepoImpl) DueDestinations(now int64, limit int) ([]string, error) {
	// A destination is due when it has queued items and no transaction in flight that
	// is still backing off.
	q := r.db.Model(&OutboxItem{}).
		Joins("LEFT JOIN outbound_txns ON outbound_txns.destination = outbox_items.destination").
		Where("outbox_items.destination <> ''").
		Where("outbound_txns.id IS NULL OR outbound_txns.next_try <= ?", now).
		Group("outbox_items.destination").
		Order("min(outbox_items.created_ts) asc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var destinations []string
	err := q.Pluck("outbox_items.destination", &destinations).Error
	if err != nil {
		return nil, err
	}
	// A destination whose only work is the outstanding transaction itself - its items
	// were packed and retired from the queue view - is due when its retry time has
	// come.
	var inFlight []string
	r.db.Model(&OutboundTxn{}).Where("next_try <= ?", now).Pluck("destination", &inFlight)
	seen := make(map[string]bool, len(destinations)+len(inFlight))
	out := make([]string, 0, len(destinations)+len(inFlight))
	for _, destination := range append(destinations, inFlight...) {
		if destination == "" || seen[destination] {
			continue
		}
		seen[destination] = true
		out = append(out, destination)
	}
	return out, nil
}

// Acknowledge implements [OutboxRepo].
func (r *OutboxRepoImpl) Acknowledge(destination, txnID string) error {
	var txn OutboundTxn
	if err := r.db.Where("destination = ? AND txn_id = ?", destination, txnID).First(&txn).Error; err != nil {
		if IsNotFound(err) {
			return nil
		}
		return err
	}
	ids := splitIDs(txn.Items)
	if err := r.db.Delete(&OutboxItem{}, ids).Error; err != nil {
		return err
	}
	return r.db.Delete(&OutboundTxn{}, txn.ID).Error
}

// Retry implements [OutboxRepo].
func (r *OutboxRepoImpl) Retry(destination, txnID string, nextTry int64, lastErr string) error {
	return r.db.Model(&OutboundTxn{}).
		Where("destination = ? AND txn_id = ?", destination, txnID).
		Updates(map[string]any{
			"tries":      gorm.Expr("tries + 1"),
			"next_try":   nextTry,
			"last_err":   lastErr,
			"updated_at": time.Now(),
		}).Error
}

// NextItems implements [OutboxRepo].
func (r *OutboxRepoImpl) NextItems(destination string, maxPDUs, maxEDUs int) ([]*OutboxItem, error) {
	// The two kinds are read separately because the transaction limits are separate,
	// and re-ordered into creation order so an event reaches a peer before the events
	// that build on it whenever both are in the same batch.
	var items []*OutboxItem
	if err := r.db.Where("destination = ? AND kind = ?", destination, "pdu").
		Order("created_ts asc, id asc").Limit(maxPDUs).Find(&items).Error; err != nil {
		return nil, err
	}
	var edus []*OutboxItem
	if err := r.db.Where("destination = ? AND kind = ?", destination, "edu").
		Order("created_ts asc, id asc").Limit(maxEDUs).Find(&edus).Error; err != nil {
		return nil, err
	}
	return append(items, edus...), nil
}

// Count implements [OutboxRepo].
func (r *OutboxRepoImpl) Count() (int64, error) {
	var count int64
	err := r.db.Model(&OutboxItem{}).Count(&count).Error
	return count, err
}

// Prune implements [OutboxRepo].
func (r *OutboxRepoImpl) Prune(beforeTS int64) (int64, error) {
	result := r.db.Where("created_ts < ?", beforeTS).Delete(&OutboxItem{})
	return result.RowsAffected, result.Error
}

// NewOutboxRepo returns the OutboxRepo implementation for db.
func NewOutboxRepo(db *gorm.DB) OutboxRepo { return &OutboxRepoImpl{db: db} }

// splitIDs reads the comma separated item ids of an outstanding transaction.
func splitIDs(raw string) []uint {
	out := []uint{}
	current := uint(0)
	parsed := false
	for _, char := range raw {
		switch {
		case char >= '0' && char <= '9':
			current = current*10 + uint(char-'0')
			parsed = true
		case char == ',':
			if parsed {
				out = append(out, current)
			}
			current, parsed = 0, false
		}
	}
	if parsed {
		out = append(out, current)
	}
	return out
}

// RemoteDevice is this server's copy of a device that lives on another server.
//
// The cache exists because an encrypted message has to be addressed to devices, and the
// devices of a remote user are not stored anywhere reachable at send time. It is
// maintained by `m.device_list_update` EDUs and refilled by a device query when the
// sequence of updates shows a gap.
type RemoteDevice struct {
	UserID   string `json:"user_id" gorm:"uniqueIndex:uq_remotedevice,priority:1;size:255;index:idx_remotedevice_user"`
	DeviceID string `json:"device_id" gorm:"uniqueIndex:uq_remotedevice,priority:2;size:128"`
	// KeyJSON is the device key object exactly as the owning server published it. The
	// signatures on it belong to that user's devices, not to this server, and
	// re-encoding the object breaks them.
	KeyJSON     string `json:"-" gorm:"type:text"`
	DisplayName string `json:"device_display_name" gorm:"size:255"`
	// Deleted keeps a device that was removed, because a peer learning about a device
	// list without the deletion looks like a device that still exists.
	Deleted bool `json:"deleted"`
	// StreamID is the position in the owner's device list sequence that this update
	// arrived at, which is what a gap in the sequence is detected against.
	StreamID int64  `json:"stream_id"`
	SeenTS   int64  `json:"seen_ts"`
	Origin   string `json:"origin" gorm:"size:255"`

	gorm.Model
}

// RemoteDeviceRepo is the contract for the device cache of remote users.
type RemoteDeviceRepo interface {
	Set(device *RemoteDevice) error
	// Forget removes a device that this server has never heard of, which is how a
	// deletion of an unknown device is recorded without inventing a row.
	Forget(userID, deviceID string) error
	// ForUser lists the devices of one remote user.
	ForUser(userID string) ([]*RemoteDevice, error)
	// Users lists the remote users this server holds device data for.
	Users() ([]string, error)
}

// RemoteDeviceRepoImpl is the GORM backed RemoteDeviceRepo.
type RemoteDeviceRepoImpl struct {
	db *gorm.DB
}

// Set implements [RemoteDeviceRepo].
func (r *RemoteDeviceRepoImpl) Set(device *RemoteDevice) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "device_id"}},
		// The names are the column names, not the JSON names: a peer's device object
		// calls the field device_display_name, the table calls it display_name, and
		// confusing the two is an SQL error rather than a data problem.
		DoUpdates: clause.AssignmentColumns([]string{
			"key_json", "display_name", "deleted", "stream_id", "seen_ts", "origin", "updated_at",
		}),
	}).Create(device).Error
}

// Forget implements [RemoteDeviceRepo].
func (r *RemoteDeviceRepoImpl) Forget(userID, deviceID string) error {
	return r.db.Where("user_id = ? AND device_id = ?", userID, deviceID).Delete(&RemoteDevice{}).Error
}

// ForUser implements [RemoteDeviceRepo].
func (r *RemoteDeviceRepoImpl) ForUser(userID string) ([]*RemoteDevice, error) {
	var devices []*RemoteDevice
	err := r.db.Where("user_id = ?", userID).Order("device_id asc").Find(&devices).Error
	return devices, err
}

// Users implements [RemoteDeviceRepo].
func (r *RemoteDeviceRepoImpl) Users() ([]string, error) {
	var users []string
	err := r.db.Model(&RemoteDevice{}).Distinct().Pluck("user_id", &users).Error
	return users, err
}

// NewRemoteDeviceRepo returns the RemoteDeviceRepo implementation for db.
func NewRemoteDeviceRepo(db *gorm.DB) RemoteDeviceRepo { return &RemoteDeviceRepoImpl{db: db} }

// DeviceStream records how far this server has followed the device list of a remote
// user.
//
// Device list updates form a small graph of their own: each update names the stream ids
// it builds on, and a server that cannot find any of them has missed an update and has
// to re-query the whole list. Recording the last id seen, and whether a gap was seen,
// is what lets a server answer "is my copy of this user's devices current?".
type DeviceStream struct {
	UserID   string `json:"user_id" gorm:"uniqueIndex:uq_devicestream_user;size:255"`
	StreamID int64  `json:"stream_id"`
	// GapSeen records that an update named a predecessor this server never received.
	// The cache is then untrustworthy until a device query refills it.
	GapSeen bool  `json:"gap_seen"`
	SeenTS  int64 `json:"seen_ts"`

	gorm.Model
}

// DeviceStreamRepo is the contract for the device list positions of remote users.
type DeviceStreamRepo interface {
	Get(userID string) (*DeviceStream, error)
	// Advance moves the position of a user forward, and records a gap when the update
	// did not build on where this server thinks it is.
	Advance(userID string, streamID int64, predecessors []int64, nowMS int64) (*DeviceStream, error)
	// Clear marks a user's cache as freshly queried.
	Clear(userID string, streamID int64, nowMS int64) error
}

// DeviceStreamRepoImpl is the GORM backed DeviceStreamRepo.
type DeviceStreamRepoImpl struct {
	db *gorm.DB
}

// Get implements [DeviceStreamRepo].
func (r *DeviceStreamRepoImpl) Get(userID string) (*DeviceStream, error) {
	var stream DeviceStream
	if err := r.db.Where("user_id = ?", userID).First(&stream).Error; err != nil {
		return nil, err
	}
	return &stream, nil
}

// Advance implements [DeviceStreamRepo].
func (r *DeviceStreamRepoImpl) Advance(userID string, streamID int64, predecessors []int64, nowMS int64) (*DeviceStream, error) {
	stream, err := r.Get(userID)
	if err != nil {
		if !IsNotFound(err) {
			return nil, err
		}
		stream = &DeviceStream{UserID: userID}
	}
	gap := stream.SeenTS == 0 && len(predecessors) > 0
	if len(predecessors) > 0 {
		gap = true
		for _, predecessor := range predecessors {
			if predecessor == stream.StreamID {
				gap = false
				break
			}
		}
	}
	stream.StreamID = streamID
	stream.GapSeen = stream.GapSeen || gap
	stream.SeenTS = nowMS
	if err := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"stream_id", "gap_seen", "seen_ts", "updated_at"}),
	}).Create(stream).Error; err != nil {
		return nil, err
	}
	return stream, nil
}

// Clear implements [DeviceStreamRepo].
func (r *DeviceStreamRepoImpl) Clear(userID string, streamID int64, nowMS int64) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"stream_id", "gap_seen", "seen_ts", "updated_at"}),
	}).Create(&DeviceStream{UserID: userID, StreamID: streamID, SeenTS: nowMS}).Error
}

// NewDeviceStreamRepo returns the DeviceStreamRepo implementation for db.
func NewDeviceStreamRepo(db *gorm.DB) DeviceStreamRepo { return &DeviceStreamRepoImpl{db: db} }

// ErrGapSeen reports that a remote device cache is missing an update.
var ErrGapSeen = errors.New("the device list of this user has a gap on record")
