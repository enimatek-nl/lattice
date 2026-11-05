package auth

import (
	"crypto/subtle"
	"sync"
	"time"

	"github.com/enimatek/lattice/internal/matrixid"
)

// UIA auth stage names used by this server.
const (
	StageDummy    = "m.login.dummy"
	StagePassword = "m.login.password"
)

// Flow is one User-Interactive Auth flow: an ordered list of stages plus the
// parameters a client needs for them. params is always emitted as an object, even
// when empty, because clients read it unconditionally.
type Flow struct {
	Stages []string       `json:"stages"`
	Params map[string]any `json:"params"`
}

// UIASession is an in-flight User-Interactive Auth session.
type UIASession struct {
	ID        string
	Flows     []Flow
	Completed map[string]bool
	Attempts  int
	ExpiresAt time.Time
	// Subject carries context a later stage needs, for example the account being
	// registered so validation results survive across UIA rounds.
	Subject map[string]any
}

// Expired reports whether the session is past its deadline.
func (s *UIASession) Expired(now time.Time) bool {
	return now.After(s.ExpiresAt)
}

// UIAService keeps UIA sessions and one-time nonces in memory. The store is
// bounded and TTL-checked on access; when multiple processes share a database in
// a future deployment this moves to the database, which is recorded as a
// documented limitation.
type UIAService struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	sessions   map[string]*UIASession
	nonces     map[string]time.Time
	now        func() time.Time
}

// NewUIAService creates a service with the given lifetime and capacity.
func NewUIAService(ttl time.Duration, maxEntries int) *UIAService {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = 4096
	}
	return &UIAService{
		ttl:        ttl,
		maxEntries: maxEntries,
		sessions:   make(map[string]*UIASession),
		nonces:     make(map[string]time.Time),
		now:        time.Now,
	}
}

// NewSession starts a UIA session offering the given flows.
func (u *UIAService) NewSession(flows []Flow, subject map[string]any) *UIASession {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.evictLocked()

	id, err := matrixid.GenerateRandomString(nil, 12)
	if err != nil {
		id = "unavailable"
	}
	sess := &UIASession{
		ID:        id,
		Flows:     withParams(flows),
		Completed: map[string]bool{},
		ExpiresAt: u.now().Add(u.ttl),
		Subject:   subject,
	}
	u.sessions[id] = sess
	return sess
}

// GetSession looks up a live session.
func (u *UIAService) GetSession(id string) (*UIASession, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	sess, ok := u.sessions[id]
	if !ok {
		return nil, false
	}
	if sess.Expired(u.now()) {
		delete(u.sessions, id)
		return nil, false
	}
	return sess, true
}

// CompleteStage marks a stage as completed and returns the session.
func (u *UIAService) CompleteStage(id, stage string) (*UIASession, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	sess, ok := u.sessions[id]
	if !ok || sess.Expired(u.now()) {
		if ok {
			delete(u.sessions, id)
		}
		return nil, false
	}
	sess.Completed[stage] = true
	return sess, true
}

// Drop removes a session, e.g. after successful completion.
func (u *UIAService) Drop(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.sessions, id)
}

// FailedAttempt records a failed stage attempt and reports whether the session
// has exceeded the attempt budget.
func (u *UIAService) FailedAttempt(id string, limit int) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	sess, ok := u.sessions[id]
	if !ok {
		return true
	}
	sess.Attempts++
	if limit > 0 && sess.Attempts > limit {
		delete(u.sessions, id)
		return true
	}
	return false
}

// withParams makes sure every flow carries a params object.
func withParams(flows []Flow) []Flow {
	out := make([]Flow, 0, len(flows))
	for _, flow := range flows {
		if flow.Params == nil {
			flow.Params = map[string]any{}
		}
		out = append(out, flow)
	}
	return out
}

// ChallengeBody renders the 401 User-Interactive Auth body.
func (u *UIAService) ChallengeBody(sess *UIASession) map[string]any {
	completed := make([]string, 0, len(sess.Completed))
	for stage := range sess.Completed {
		completed = append(completed, stage)
	}
	return map[string]any{
		"session":   sess.ID,
		"flows":     sess.Flows,
		"params":    map[string]any{},
		"completed": completed,
	}
}

// NewNonce issues a single-use nonce for shared-secret registration.
func (u *UIAService) NewNonce() (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.evictNoncesLocked()
	nonce, err := matrixid.GenerateRandomString(nil, 16)
	if err != nil {
		return "", err
	}
	u.nonces[nonce] = u.now().Add(u.ttl)
	return nonce, nil
}

// ConsumeNonce verifies and invalidates a nonce. Reuse is rejected.
func (u *UIAService) ConsumeNonce(nonce string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	expiry, ok := u.nonces[nonce]
	if !ok {
		return false
	}
	for stored := range u.nonces {
		if subtle.ConstantTimeCompare([]byte(stored), []byte(nonce)) == 1 {
			delete(u.nonces, stored)
			break
		}
	}
	return u.now().Before(expiry)
}

// evictLocked drops expired sessions and enforces the capacity limit.
func (u *UIAService) evictLocked() {
	now := u.now()
	for id, sess := range u.sessions {
		if sess.Expired(now) {
			delete(u.sessions, id)
		}
	}
	if len(u.sessions) >= u.maxEntries {
		// Drop the oldest-expiring session to keep the store bounded.
		var oldestID string
		var oldest time.Time
		for id, sess := range u.sessions {
			if oldestID == "" || sess.ExpiresAt.Before(oldest) {
				oldestID, oldest = id, sess.ExpiresAt
			}
		}
		if oldestID != "" {
			delete(u.sessions, oldestID)
		}
	}
}

func (u *UIAService) evictNoncesLocked() {
	now := u.now()
	for nonce, expiry := range u.nonces {
		if now.After(expiry) {
			delete(u.nonces, nonce)
		}
	}
}
