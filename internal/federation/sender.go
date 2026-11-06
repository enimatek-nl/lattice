package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// Sender is the outbound half of federation: the queue of things this server still
// owes other homeservers, and the worker that pays it off.
//
// The queue is in the database and the retries are bounded and staggered per
// destination, for the reasons the project's rules insist on: a remote server that is
// down must not stop local rooms from working, a restart must not lose what has not
// been sent, and a destination that never answers must not be retried forever in
// silence.
type Sender struct {
	repo   persist.FederationRepo
	events persist.EventRepo
	state  persist.StateRepo
	client *Client
	log    *slog.Logger
	ours   string

	// Now is the clock; MaxAge and MaxAttempts bound how long an item may stay owing;
	// Batch caps how many items one worker takes at a time.
	Now         func() time.Time
	MaxAge      time.Duration
	MaxAttempts int
	Batch       int
	// Idle is how long the worker waits when there is nothing to send.
	Idle time.Duration
	// CleanupInterval is how often the worker forgets what has expired: transactions it
	// has already answered, key documents past their life, and events it gave up on.
	// Zero means an hour.
	CleanupInterval time.Duration
	// GivenUpRetention is how long a queue row stays after this server stopped trying,
	// so that "why did this room never arrive?" can still be answered when somebody asks.
	GivenUpRetention time.Duration
	// ACLs says whether a room has excluded a server, so that a relay does not pass a room's
	// events to a server the room has thrown out. It is the room service; nil means no filtering,
	// which is what a sender without a room service wired to it is.
	ACLs RoomDenials

	// wake holds at most one notice that the queue has something in it. The paths that queue
	// work post it and the worker's idle wait reads it, so an event a local user just sent
	// leaves this server with the next pass rather than at the end of the idle interval.
	wake chan struct{}

	// flushMu serialises the passes this process makes over the queue. See FlushOnce.
	flushMu sync.Mutex
}

// defaultSenderIdle is how long the sender waits between passes over an empty queue.
//
// Two and a half hundred milliseconds was tried and is not safe yet: with the queue worked that
// eagerly, this server pushes a room's events at a neighbour while that neighbour is still in the
// middle of joining the room, and the integration suite starts failing federation tests that
// assume an event and the state around it arrive in one piece. Shortening this is a delivery
// improvement the server genuinely wants — a power level change that reaches a neighbour seconds
// late is a neighbour that answers the next question from stale state — but it has to come with
// the ordering fix, not ahead of it.
const defaultSenderIdle = 2 * time.Second

// RoomDenials is the room service's answer to whether a room excludes a homeserver.
type RoomDenials interface {
	DeniesServer(ctx context.Context, roomID, serverName string) (bool, error)
}

// NewSender returns a sender with the retry limits this project uses.
func NewSender(repo persist.FederationRepo, events persist.EventRepo, state persist.StateRepo, client *Client, serverName string, log *slog.Logger) *Sender {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Sender{
		repo:        repo,
		events:      events,
		state:       state,
		client:      client,
		log:         log,
		ours:        serverName,
		Now:         time.Now,
		MaxAge:      24 * time.Hour,
		MaxAttempts: 30,
		Batch:       maxPDUsPerTransaction,
		Idle:        defaultSenderIdle,
		wake:        make(chan struct{}, 1),

		CleanupInterval:  time.Hour,
		GivenUpRetention: givenUpRetention,
	}
}

// Wake notices the worker that the queue has work. It never blocks, and one notice covers any
// number of events queued behind it: the worker looks at the whole queue when it wakes.
func (s *Sender) Wake() {
	if s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Relay queues one event for every other server that has a joined member in the room.
//
// "Which servers" is answered now, from the room's current membership, because that is
// the set the specification's delivery model is about: every server with somebody in
// the room should hear about it. A server that joins later is told by the join
// protocol, not by a resend.
func (s *Sender) Relay(ctx context.Context, roomID, eventID string) error {
	stored, err := s.events.ByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("federation: cannot read the event to relay: %w", err)
	}
	if stored.SoftFailed {
		// A soft-failed event is stored and resolved but not relayed: the whole point
		// is that this server does not pass on an event that its own current state
		// says should not have been sent.
		return nil
	}
	members, err := s.state.CurrentMembers(ctx, roomID)
	if err != nil {
		return fmt.Errorf("federation: cannot read the room's members to relay an event: %w", err)
	}
	destinations := serverNames(members, s.ours)
	if s.ACLs != nil && len(destinations) > 0 {
		// A room that has excluded a server is not served by passing events to it. The exclusion
		// is the room's decision, made in its state, and a server that keeps a room's traffic
		// flowing to a server the room threw out is undoing that decision on the room's behalf.
		allowed := make([]string, 0, len(destinations))
		for _, destination := range destinations {
			denied, err := s.ACLs.DeniesServer(ctx, roomID, destination)
			if err != nil {
				return fmt.Errorf("federation: cannot read whether the room excludes %s: %w", destination, err)
			}
			if !denied {
				allowed = append(allowed, destination)
			}
		}
		destinations = allowed
	}
	if len(destinations) == 0 {
		return nil
	}
	now := s.now()
	for _, destination := range destinations {
		already, err := s.queuedFor(ctx, destination, eventID)
		if err != nil {
			return err
		}
		if already {
			// The same event queued twice for the same server would be the same event
			// delivered twice, which for a state event is harmless and for an EDU-free
			// PDU is a second copy of a message.
			continue
		}
		if err := s.repo.Queue(ctx, &persist.FederationQueueItem{
			Destination:   destination,
			Kind:          persist.QueueKindPDU,
			Payload:       string(stored.JSON),
			EventID:       eventID,
			NextAttemptAt: now,
		}); err != nil {
			return fmt.Errorf("federation: cannot queue the event for %s: %w", destination, err)
		}
	}
	s.Wake()
	return nil
}

// QueuePDU offers one event to one destination. It is separate from Relay because a
// server sometimes knows a particular remote should have a particular event — after a
// join, when catching up on history — and because a test of the handover must be able
// to ask for it without inventing a room membership.
func (s *Sender) QueuePDU(ctx context.Context, destination, eventID string, pdu []byte) error {
	if destination == "" || len(pdu) == 0 {
		return nil
	}
	if err := s.repo.Queue(ctx, &persist.FederationQueueItem{
		Destination:   destination,
		Kind:          persist.QueueKindPDU,
		Payload:       string(pdu),
		EventID:       eventID,
		NextAttemptAt: s.now(),
	}); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// queuedFor reports whether an event is already waiting for a destination.
func (s *Sender) queuedFor(ctx context.Context, destination, eventID string) (bool, error) {
	count, err := s.repo.QueueCount(ctx, destination, eventID)
	if err != nil {
		return false, fmt.Errorf("federation: cannot ask whether %s already owes %s: %w", s.ours, destination, err)
	}
	return count > 0, nil
}

// EnqueueEDU queues an ephemeral unit for one destination. EDUs are not stored: a
// presence update that arrives an hour late is not late, it is wrong.
func (s *Sender) EnqueueEDU(ctx context.Context, destination, eduType string, content map[string]any) error {
	payload, err := json.Marshal(EDU{Type: eduType, Content: content})
	if err != nil {
		return fmt.Errorf("federation: cannot encode the %s unit: %w", eduType, err)
	}
	if err := s.repo.Queue(ctx, &persist.FederationQueueItem{
		Destination:   destination,
		Kind:          persist.QueueKindEDU,
		Payload:       string(payload),
		NextAttemptAt: s.now(),
	}); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// FlushOnce takes what is due, sends it grouped by destination, and puts back what a
// remote refused. It returns how many items were delivered.
func (s *Sender) FlushOnce(ctx context.Context) (int, error) {
	// One pass at a time. The claim is atomic in the database, so two processes cannot take the
	// same row; this is about the order a room's events leave in. Two passes running at once in
	// this process can take the earlier event and the later one and deliver them concurrently,
	// which hands a neighbour a room's state before the event that room's new state sits on. A
	// neighbour then refuses an event it would have accepted a moment later, and a refusal is
	// remembered.
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	now := s.now()
	items, err := s.repo.ClaimQueueItems(ctx, now, s.batch())
	if err != nil {
		return 0, fmt.Errorf("federation: cannot claim the queued work: %w", err)
	}
	if len(items) == 0 {
		return 0, nil
	}
	sent := 0
	for _, destination := range destinationsOf(items) {
		batch := itemsFor(items, destination)
		// The oldest half of the batch goes first; the rest stays claimed until its
		// hold expires rather than being held in memory, which keeps a crash from
		// losing track of it.
		if len(batch) > maxPDUsPerTransaction {
			batch = batch[:maxPDUsPerTransaction]
		}
		count, err := s.sendBatch(ctx, destination, batch, now)
		if err != nil {
			s.log.Warn("federation: a transaction could not be delivered",
				"destination", destination, "items", len(batch), "error", err)
			continue
		}
		sent += count
	}
	return sent, nil
}

// sendBatch delivers one transaction's worth of items to one destination.
func (s *Sender) sendBatch(ctx context.Context, destination string, items []*persist.FederationQueueItem, now time.Time) (int, error) {
	var pdus []json.RawMessage
	var edus []EDU
	pduItems := make([]*persist.FederationQueueItem, 0, len(items))
	eduItems := make([]*persist.FederationQueueItem, 0, len(items))
	for _, item := range items {
		switch item.Kind {
		case persist.QueueKindEDU:
			var edu EDU
			if err := json.Unmarshal([]byte(item.Payload), &edu); err != nil {
				s.dropItems(ctx, []*persist.FederationQueueItem{item}, "the queued unit is not readable")
				continue
			}
			edus = append(edus, edu)
			eduItems = append(eduItems, item)
		default:
			pdus = append(pdus, json.RawMessage(item.Payload))
			pduItems = append(pduItems, item)
		}
	}
	if len(pdus) == 0 && len(edus) == 0 {
		return 0, nil
	}
	results, err := s.client.SendTransaction(ctx, destination, s.transactionID(now, items), pdus, edus)
	if err != nil {
		s.retryItems(ctx, items, now, err)
		return 0, err
	}
	failed := map[string]string{}
	for _, result := range results {
		if result.Error != "" {
			failed[result.EventID] = result.Error
		}
	}
	var done []*persist.FederationQueueItem
	var rejected []*persist.FederationQueueItem
	for _, item := range pduItems {
		if problem, ok := failed[item.EventID]; ok {
			// The neighbour refused this event, and it is asked again. That looks wrong — a
			// refusal is an answer, and an answer that is asked again wastes both servers'
			// time — but a large share of refusals are the neighbour saying it cannot judge
			// yet rather than that the event is bad: it does not hold the room, or the auth
			// events, or the predecessor this event hangs off. Those gaps are usually the
			// neighbour's to fill a moment later, and an event dropped over one is a room whose
			// history differs on the two servers forever, which is the outcome a refusal was
			// meant to avoid. The retry budget is the bound: an event refused on its merits is
			// asked a few times and then given up on with the reason kept.
			s.log.Warn("federation: a remote server refused an event",
				"destination", destination, "event_id", item.EventID, "reason", problem)
			rejected = append(rejected, item)
			continue
		}
		done = append(done, item)
	}
	done = append(done, eduItems...)
	if err := s.repo.QueueDone(ctx, idsOf(done)); err != nil {
		return 0, fmt.Errorf("federation: cannot clear the delivered items: %w", err)
	}
	if len(rejected) > 0 {
		s.retryItems(ctx, rejected, now, errors.New("the destination refused the event"))
	}
	return len(done), nil
}

// Cleanup forgets what has expired: transactions this server has already answered, key
// documents whose life has run out, and queue rows it gave up on.
//
// It is not optional housekeeping. Transaction rows are how a replay is answered
// identically, and they are the reason a sender cannot simply make every retry a fresh
// request; key documents outlive their usefulness; and a dropped queue row is the record
// of a room that never arrived. Left alone, all three grow with other people's outages.
func (s *Sender) Cleanup(ctx context.Context) (int, error) {
	now := s.now()
	forgotten := 0
	transactions, err := s.repo.DropExpiredTransactions(ctx, now)
	if err != nil {
		return forgotten, fmt.Errorf("federation: cannot forget answered transactions: %w", err)
	}
	forgotten += transactions
	keys, err := s.repo.DropExpiredServerKeys(ctx, now)
	if err != nil {
		return forgotten, fmt.Errorf("federation: cannot forget expired keys: %w", err)
	}
	forgotten += keys
	givenUp, err := s.repo.DropGivenUpQueueItems(ctx, now.Add(-s.retention()))
	if err != nil {
		return forgotten, fmt.Errorf("federation: cannot forget given-up queue items: %w", err)
	}
	forgotten += givenUp
	if forgotten > 0 {
		s.log.Info("federation: forgot what had expired", "records", forgotten,
			"transactions", transactions, "keys", keys, "given_up_queue_items", givenUp)
	}
	return forgotten, nil
}

// givenUpRetention is how long a queue row stays after this server stopped trying.
const givenUpRetention = 30 * 24 * time.Hour

func (s *Sender) retention() time.Duration {
	if s.GivenUpRetention > 0 {
		return s.GivenUpRetention
	}
	return givenUpRetention
}

// Run works until the context ends. One worker per process is enough for the volumes
// this project targets; the claim query is written so that more than one would not
// double-send.
func (s *Sender) Run(ctx context.Context) error {
	idle := s.Idle
	if idle <= 0 {
		idle = defaultSenderIdle
	}
	// The zero value makes the first pass clean up straight away, which is what a
	// process that was down for a day needs when it comes back.
	var lastCleanup time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		sent, err := s.FlushOnce(ctx)
		if err != nil {
			s.log.Warn("federation: the sender hit a problem", "error", err)
		}
		if time.Since(lastCleanup) >= s.cleanupInterval() {
			if _, err := s.Cleanup(ctx); err != nil {
				s.log.Warn("federation: cannot tidy the federation tables", "error", err)
			}
			lastCleanup = time.Now()
		}
		if sent == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-s.wake:
			case <-time.After(idle):
			}
			continue
		}
	}
}

// retryItems pushes items back with a delay that grows with the number of attempts.
func (s *Sender) retryItems(ctx context.Context, items []*persist.FederationQueueItem, now time.Time, cause error) {
	var keep []*persist.FederationQueueItem
	var given []*persist.FederationQueueItem
	for _, item := range items {
		if now.Sub(item.CreatedAt) > s.MaxAge || item.Attempts >= s.MaxAttempts {
			given = append(given, item)
			continue
		}
		keep = append(keep, item)
	}
	for _, item := range keep {
		if err := s.repo.QueueRetry(ctx, item.ID, now.Add(backoff(item.Attempts)), cause.Error()); err != nil {
			s.log.Warn("federation: cannot reschedule a queued item", "id", item.ID, "error", err)
		}
	}
	if len(given) > 0 {
		s.log.Error("federation: giving up on queued items",
			"count", len(given), "max_age", s.MaxAge.String(), "max_attempts", s.MaxAttempts,
			"error", cause.Error())
		s.dropItems(ctx, given, "gave up after retries")
	}
}

// dropItems marks items as no longer owing, keeping the row so the failure is visible
// to whoever looks.
func (s *Sender) dropItems(ctx context.Context, items []*persist.FederationQueueItem, reason string) {
	if len(items) == 0 {
		return
	}
	if err := s.repo.QueueDrop(ctx, idsOf(items), reason); err != nil {
		s.log.Warn("federation: cannot mark queued items as dropped", "error", err)
	}
}

// backoff is exponential with a ceiling: a server that is down gets asked again soon at
// first and slowly later, and never asked a hundred times a second.
func backoff(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 12 {
		attempts = 12
	}
	delay := time.Second << attempts
	if delay > time.Hour {
		delay = time.Hour
	}
	return delay
}

// transactionID makes an ID unique to this sender and this batch.
func (s *Sender) transactionID(now time.Time, items []*persist.FederationQueueItem) string {
	lowest := uint64(0)
	for _, item := range items {
		if lowest == 0 || item.ID < lowest {
			lowest = item.ID
		}
	}
	return fmt.Sprintf("lat-%d-%d", now.UnixNano(), lowest)
}

func (s *Sender) batch() int {
	if s.Batch > 0 {
		if s.Batch > maxPDUsPerTransaction {
			return maxPDUsPerTransaction
		}
		return s.Batch
	}
	return maxPDUsPerTransaction
}

func (s *Sender) cleanupInterval() time.Duration {
	if s.CleanupInterval > 0 {
		return s.CleanupInterval
	}
	return time.Hour
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// serverNames turns user IDs into the distinct servers that must be told, dropping
// this one.
func serverNames(userIDs []string, ours string) []string {
	seen := map[string]bool{}
	var names []string
	for _, userID := range userIDs {
		domain, err := matrixid.UserDomain(userID)
		if err != nil || domain == "" || domain == ours || seen[domain] {
			continue
		}
		seen[domain] = true
		names = append(names, domain)
	}
	sort.Strings(names)
	return names
}

func destinationsOf(items []*persist.FederationQueueItem) []string {
	seen := map[string]bool{}
	var names []string
	for _, item := range items {
		if seen[item.Destination] {
			continue
		}
		seen[item.Destination] = true
		names = append(names, item.Destination)
	}
	return names
}

func itemsFor(items []*persist.FederationQueueItem, destination string) []*persist.FederationQueueItem {
	var chosen []*persist.FederationQueueItem
	for _, item := range items {
		if item.Destination == destination {
			chosen = append(chosen, item)
		}
	}
	return chosen
}

func idsOf(items []*persist.FederationQueueItem) []uint64 {
	ids := make([]uint64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}
