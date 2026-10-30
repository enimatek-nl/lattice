// Package realtime provides a fan-out notification hub. Sync long-polling
// handlers subscribe, wait for a change notification, and unsubscribe when
// they return, so no goroutine outlives its request.
package realtime

import "sync"

// Hub broadcasts "something changed" notifications to its subscribers. The
// notification carries no payload: subscribers re-read the database, which
// keeps the hub free of ordering and consistency concerns.
type Hub struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{subscribers: make(map[chan struct{}]struct{})}
}

// Notify wakes every subscriber. Sends are non-blocking: a subscriber that has
// not drained its channel simply misses this notification and will be woken by
// the next one (or by its own poll deadline).
func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe registers a listener. The returned cancel function must be called
// (usually deferred) to release the channel.
func (h *Hub) Subscribe() (ch <-chan struct{}, cancel func()) {
	notify := make(chan struct{}, 1)
	h.mu.Lock()
	h.subscribers[notify] = struct{}{}
	h.mu.Unlock()
	return notify, func() {
		h.mu.Lock()
		delete(h.subscribers, notify)
		h.mu.Unlock()
	}
}

// Subscribers reports how many listeners are attached. Used by tests.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}
