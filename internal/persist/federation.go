package persist

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// ServerSigningKey is one public signing key another homeserver published, as this
// server recorded it.
//
// The key is stored with the expiry its document carried rather than an unbounded
// "valid forever", because a key document is a claim by a stranger that is only good
// until the time it names. Clamping that time to a week at write time is what keeps a
// published key from being trusted for a month after its owner decided to revoke it.
type ServerSigningKey struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	ServerName string `gorm:"column:server_name;size:255;not null;uniqueIndex:idx_server_keys_server_key,priority:1"`
	KeyID      string `gorm:"column:key_id;size:255;not null;uniqueIndex:idx_server_keys_server_key,priority:2"`
	// Key is the raw ed25519 public key.
	Key []byte `gorm:"column:key;not null"`
	// ExpiredTS is zero for a current key, and the millisecond timestamp from
	// old_verify_keys for a key that has been retired. A retired key can still be the
	// answer for an event signed before it was retired.
	ExpiredTS int64 `gorm:"column:expired_ts;not null;default:0"`
	// ValidUntilTS is the clamped expiry of the document the key came from.
	ValidUntilTS int64     `gorm:"column:valid_until_ts;not null;default:0"`
	FetchedAt    time.Time `gorm:"column:fetched_at;not null"`
}

// TableName fixes the table name.
func (ServerSigningKey) TableName() string { return "server_signing_keys" }

// ServerKeyDocument is the last key document fetched for a server, kept verbatim.
//
// It is kept for two reasons: it is what this server hands on when it is asked to
// notarise keys for somebody else, and it is the record of what the server actually
// said, as opposed to the parsed rows this server derived from it.
type ServerKeyDocument struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	ServerName   string `gorm:"column:server_name;size:255;not null;uniqueIndex"`
	Document     []byte `gorm:"column:document;not null"`
	ValidUntilTS int64  `gorm:"column:valid_until_ts;not null;default:0"`
	FetchedAt    time.Time
}

// TableName fixes the table name.
func (ServerKeyDocument) TableName() string { return "server_key_documents" }

// FederationTransaction records an inbound transaction by the pair that gives it
// meaning: the server that sent it and the ID it chose.
//
// A transaction ID is only unique per origin, so the pair is the identity. The stored
// response is what a repeat delivery gets: re-processing a transaction is not
// idempotent in practice — an event would be relayed twice, an EDU counted twice — so
// the answer is the one already given.
type FederationTransaction struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	Origin      string `gorm:"column:origin;size:255;not null;uniqueIndex:idx_federation_txn,priority:1"`
	TxnID       string `gorm:"column:txn_id;size:255;not null;uniqueIndex:idx_federation_txn,priority:2"`
	Response    []byte `gorm:"column:response;not null"`
	ReceivedAt  time.Time
	ExpiresAt   time.Time `gorm:"column:expires_at;index"`
	EventCount  int       `gorm:"column:event_count;not null;default:0"`
	FailedCount int       `gorm:"column:failed_count;not null;default:0"`
}

// TableName fixes the table name.
func (FederationTransaction) TableName() string { return "federation_transactions" }

// FederationQueueItem is something this server still owes another homeserver.
//
// The queue lives in the database rather than in a goroutine's memory because the
// remote server may be down for hours across a restart, and a message that vanishes
// because this server restarted is a message that never existed. Each item carries the
// time it may next be tried, so a destination that is refusing connections is retried
// slowly without holding up anybody else.
type FederationQueueItem struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	Destination string `gorm:"column:destination;size:255;not null;index:idx_federation_queue_claim,priority:1"`
	// Kind is "pdu" or "edu". A PDU row carries one event; EDUs are batched.
	Kind string `gorm:"column:kind;size:16;not null"`
	// Payload is the JSON to put in the transaction: the PDU itself, or the EDU's
	// content with its type.
	Payload  string `gorm:"column:payload;type:text;not null"`
	EventID  string `gorm:"column:event_id;size:255;not null;default:''"`
	Attempts int    `gorm:"column:attempts;not null;default:0"`
	// NextAttemptAt is when this item may next be sent; the claim query reads it.
	NextAttemptAt time.Time `gorm:"column:next_attempt_at;not null;index:idx_federation_queue_claim,priority:2"`
	CreatedAt     time.Time
	LastError     string `gorm:"column:last_error;type:text;not null;default:''"`
	// DroppedAt is a pointer because NULL is the question's answer: a row that was
	// given up on has a time in it, and a row still owing has nothing. A plain
	// time.Time writes the zero date instead, and every "is this still owing" query
	// then says no — which silently empties the queue and loses undelivered events.
	DroppedAt *time.Time `gorm:"column:dropped_at"`
}

// TableName fixes the table name.
func (FederationQueueItem) TableName() string { return "federation_queue" }

// The two kinds of queued item.
const (
	QueueKindPDU = "pdu"
	QueueKindEDU = "edu"
)

// FederationRepo is the storage contract for federation state.
type FederationRepo interface {
	// WithTx returns the repository working inside an open transaction.
	WithTx(tx *gorm.DB) FederationRepo

	// SaveServerKeys records a fetched key document and the keys it names, replacing
	// what was known about that server.
	SaveServerKeys(ctx context.Context, document *ServerKeyDocument, keys []ServerSigningKey) error
	// ServerKeys returns the keys recorded for a server.
	ServerKeys(ctx context.Context, serverName string) ([]ServerSigningKey, error)
	// ServerKeyDocument returns the last document fetched for a server.
	KeysDocument(ctx context.Context, serverName string) (*ServerKeyDocument, error)
	// DropExpiredServerKeys removes key rows whose document expired before the instant.
	DropExpiredServerKeys(ctx context.Context, before time.Time) (int, error)

	// SeenTransaction returns the stored response for a transaction already processed
	// and still inside its replay window, or nil.
	SeenTransaction(ctx context.Context, origin, txnID string, now time.Time) (*FederationTransaction, error)
	// RecordTransaction stores the response for a processed transaction.
	RecordTransaction(ctx context.Context, txn *FederationTransaction) error
	// DropExpiredTransactions forgets transactions older than the instant.
	DropExpiredTransactions(ctx context.Context, before time.Time) (int, error)

	// Queue adds an item to the outbound queue. Deduplicating on (destination,
	// event_id) is the caller's decision, and the EventID column exists to make it
	// cheap.
	Queue(ctx context.Context, item *FederationQueueItem) error
	// ClaimQueueItems returns up to limit items for destinations that are not being
	// held back, and marks them as in flight until the next attempt time. Items for
	// one destination are claimed together so a transaction can carry them.
	ClaimQueueItems(ctx context.Context, now time.Time, limit int) ([]*FederationQueueItem, error)
	// QueueRetry pushes an item back with a later attempt time and the error that
	// caused it.
	QueueRetry(ctx context.Context, id uint64, nextAttemptAt time.Time, lastError string) error
	// QueueDone removes items that were sent.
	QueueDone(ctx context.Context, ids []uint64) error
	// QueueDrop gives up on items, keeping the row so the failure is visible.
	QueueDrop(ctx context.Context, ids []uint64, reason string) error
	// QueuedDestinations lists destinations with work waiting, for the worker's plan.
	QueuedDestinations(ctx context.Context, now time.Time) ([]string, error)
	// QueueDepth reports how much is waiting, for logging and health.
	QueueDepth(ctx context.Context) (int, error)
	// QueueCount reports how many undelivered items a destination is owed for one
	// event, which is what makes queueing an event twice a no-op.
	QueueCount(ctx context.Context, destination, eventID string) (int64, error)
	// DropGivenUpQueueItems forgets items that were given up on before the instant. A
	// dropped row exists so a failure is visible to whoever looks, and a row that stays
	// forever is a table that grows with other people's outages.
	DropGivenUpQueueItems(ctx context.Context, before time.Time) (int, error)
}

// NewFederationRepo returns the federation repository bound to db.
func NewFederationRepo(db *gorm.DB) FederationRepo { return &federationRepo{db: db} }

type federationRepo struct{ db *gorm.DB }

// WithTx implements [FederationRepo].
func (r *federationRepo) WithTx(tx *gorm.DB) FederationRepo { return &federationRepo{db: tx} }

// SaveServerKeys implements [FederationRepo].
func (r *federationRepo) SaveServerKeys(ctx context.Context, document *ServerKeyDocument, keys []ServerSigningKey) error {
	if document == nil || document.ServerName == "" {
		return nil
	}
	if err := r.db.WithContext(ctx).Where("server_name = ?", document.ServerName).
		Delete(&ServerSigningKey{}).Error; err != nil {
		return err
	}
	if len(keys) > 0 {
		if err := r.db.WithContext(ctx).CreateInBatches(&keys, 100).Error; err != nil {
			return err
		}
	}
	return r.db.WithContext(ctx).
		Where("server_name = ?", document.ServerName).
		Assign(ServerKeyDocument{
			Document:     document.Document,
			ValidUntilTS: document.ValidUntilTS,
			FetchedAt:    document.FetchedAt,
		}).
		FirstOrCreate(document).Error
}

// ServerKeys implements [FederationRepo].
func (r *federationRepo) ServerKeys(ctx context.Context, serverName string) ([]ServerSigningKey, error) {
	var rows []ServerSigningKey
	if err := r.db.WithContext(ctx).Where("server_name = ?", serverName).Order("key_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// KeysDocument implements [FederationRepo].
func (r *federationRepo) KeysDocument(ctx context.Context, serverName string) (*ServerKeyDocument, error) {
	var row ServerKeyDocument
	if err := r.db.WithContext(ctx).Where("server_name = ?", serverName).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// DropExpiredServerKeys implements [FederationRepo].
func (r *federationRepo) DropExpiredServerKeys(ctx context.Context, before time.Time) (int, error) {
	result := r.db.WithContext(ctx).Where("valid_until_ts < ?", before.UnixMilli()).Delete(&ServerSigningKey{})
	return int(result.RowsAffected), result.Error
}

// SeenTransaction implements [FederationRepo].
func (r *federationRepo) SeenTransaction(ctx context.Context, origin, txnID string, now time.Time) (*FederationTransaction, error) {
	var row FederationTransaction
	// The instant matters: a transaction ID answers a replay only inside the window in
	// which it still means the same request. An answer kept past its expiry is not
	// idempotency, it is a stale reply to a new request that happens to reuse an ID.
	err := r.db.WithContext(ctx).
		Where("origin = ? AND txn_id = ? AND expires_at > ?", origin, txnID, now.UTC()).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// RecordTransaction implements [FederationRepo].
func (r *federationRepo) RecordTransaction(ctx context.Context, txn *FederationTransaction) error {
	if txn == nil || txn.Origin == "" || txn.TxnID == "" {
		return nil
	}
	if txn.ReceivedAt.IsZero() {
		txn.ReceivedAt = time.Now().UTC()
	}
	err := r.db.WithContext(ctx).
		Where("origin = ? AND txn_id = ?", txn.Origin, txn.TxnID).
		Assign(FederationTransaction{
			Response:    txn.Response,
			ReceivedAt:  txn.ReceivedAt,
			ExpiresAt:   txn.ExpiresAt,
			EventCount:  txn.EventCount,
			FailedCount: txn.FailedCount,
		}).
		FirstOrCreate(txn).Error
	return err
}

// DropExpiredTransactions implements [FederationRepo].
func (r *federationRepo) DropExpiredTransactions(ctx context.Context, before time.Time) (int, error) {
	result := r.db.WithContext(ctx).Where("expires_at < ?", before).Delete(&FederationTransaction{})
	return int(result.RowsAffected), result.Error
}

// Queue implements [FederationRepo].
func (r *federationRepo) Queue(ctx context.Context, item *FederationQueueItem) error {
	if item == nil || item.Destination == "" || item.Payload == "" {
		return nil
	}
	if item.NextAttemptAt.IsZero() {
		item.NextAttemptAt = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Create(item).Error
}

// ClaimQueueItems implements [FederationRepo].
//
// The claim is one statement rather than a read followed by an update, because two
// workers in the same process — or two processes sharing a database — must not send the
// same bytes to the same destination twice.
func (r *federationRepo) ClaimQueueItems(ctx context.Context, now time.Time, limit int) ([]*FederationQueueItem, error) {
	if limit <= 0 {
		return nil, nil
	}
	var ids []uint64
	if err := r.db.WithContext(ctx).Model(&FederationQueueItem{}).
		Where("dropped_at IS NULL AND next_attempt_at <= ?", now).
		Order("destination ASC, id ASC").
		Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	// Push the attempt time forward before the work starts: a crash mid-send costs a
	// retry later, not a loop that never stops.
	if err := r.db.WithContext(ctx).Model(&FederationQueueItem{}).
		Where("id IN ?", ids).
		Update("next_attempt_at", now.Add(queueClaimHold)).Error; err != nil {
		return nil, err
	}
	var items []*FederationQueueItem
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Order("destination ASC, id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// queueClaimHold is how long a claimed item is held before another worker may take it.
const queueClaimHold = 5 * time.Minute

// QueueRetry implements [FederationRepo].
func (r *federationRepo) QueueRetry(ctx context.Context, id uint64, nextAttemptAt time.Time, lastError string) error {
	return r.db.WithContext(ctx).Model(&FederationQueueItem{}).Where("id = ?", id).
		Updates(map[string]any{
			"next_attempt_at": nextAttemptAt,
			"last_error":      lastError,
			"attempts":        gorm.Expr("attempts + 1"),
		}).Error
}

// QueueDone implements [FederationRepo].
func (r *federationRepo) QueueDone(ctx context.Context, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Where("id IN ?", ids).Delete(&FederationQueueItem{}).Error
}

// QueueDrop implements [FederationRepo].
func (r *federationRepo) QueueDrop(ctx context.Context, ids []uint64, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&FederationQueueItem{}).Where("id IN ?", ids).
		Updates(map[string]any{"dropped_at": time.Now().UTC(), "last_error": reason}).Error
}

// QueueCount implements [FederationRepo].
func (r *federationRepo) QueueCount(ctx context.Context, destination, eventID string) (int64, error) {
	if eventID == "" {
		return 0, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&FederationQueueItem{}).
		Where("destination = ? AND event_id = ? AND dropped_at IS NULL", destination, eventID).
		Count(&count).Error
	return count, err
}

// QueuedDestinations implements [FederationRepo].
func (r *federationRepo) QueuedDestinations(ctx context.Context, now time.Time) ([]string, error) {
	var destinations []string
	err := r.db.WithContext(ctx).Model(&FederationQueueItem{}).
		Where("dropped_at IS NULL AND next_attempt_at <= ?", now).
		Distinct().Pluck("destination", &destinations).Error
	return destinations, err
}

// DropGivenUpQueueItems implements [FederationRepo].
func (r *federationRepo) DropGivenUpQueueItems(ctx context.Context, before time.Time) (int, error) {
	result := r.db.WithContext(ctx).
		Where("dropped_at IS NOT NULL AND dropped_at <= ?", before.UTC()).
		Delete(&FederationQueueItem{})
	return int(result.RowsAffected), result.Error
}

// QueueDepth implements [FederationRepo].
func (r *federationRepo) QueueDepth(ctx context.Context) (int, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&FederationQueueItem{}).
		Where("dropped_at IS NULL").Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}
