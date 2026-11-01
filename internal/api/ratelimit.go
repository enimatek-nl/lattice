package api

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// limitKind selects which token bucket an endpoint draws from. Splitting the
// budgets keeps a chatty media upload from starving room sends, and a
// brute-force attempt from starving an authenticated client.
type limitKind int

const (
	// limitWrite guards the expensive room writes: /send, /createRoom,
	// /join and /invite.
	limitWrite limitKind = iota
	// limitAuth guards login, refresh and registration attempts.
	limitAuth
	// limitUpload guards media uploads.
	limitUpload
)

// String names a bucket for logs and error messages.
func (k limitKind) String() string {
	switch k {
	case limitAuth:
		return "auth"
	case limitUpload:
		return "upload"
	default:
		return "write"
	}
}

// RateLimits configures the token buckets that guard the expensive writes.
// Values come from flags in cmd/server, never from literals in handlers.
type RateLimits struct {
	// Disabled answers every request, which is how a test or an operator turns
	// the limiter off.
	Disabled bool
	// WriteBurst is how many expensive room writes may happen at once per
	// device; WriteRate is how many per second the bucket refills to.
	WriteBurst int
	WriteRate  float64
	// AuthBurst and AuthRate guard login, refresh and registration attempts.
	// These are keyed by client address because an attempt has no device yet.
	AuthBurst int
	AuthRate  float64
	// UploadBurst and UploadRate guard media uploads per device.
	UploadBurst int
	UploadRate  float64
}

// DefaultRateLimits are the values a server runs with when nobody configured
// the limiter. They are deliberately generous: a single-user homeserver should
// never notice them, while a script hammering one device stops after its burst.
func DefaultRateLimits() RateLimits {
	return RateLimits{
		WriteBurst:  100,
		WriteRate:   30,
		AuthBurst:   20,
		AuthRate:    2,
		UploadBurst: 20,
		UploadRate:  5,
	}
}

// withDefaults fills in every unset field, so a caller can override one bucket
// without restating the others.
func (l RateLimits) withDefaults() RateLimits {
	defaults := DefaultRateLimits()
	if l.WriteBurst == 0 {
		l.WriteBurst = defaults.WriteBurst
	}
	if l.WriteRate == 0 {
		l.WriteRate = defaults.WriteRate
	}
	if l.AuthBurst == 0 {
		l.AuthBurst = defaults.AuthBurst
	}
	if l.AuthRate == 0 {
		l.AuthRate = defaults.AuthRate
	}
	if l.UploadBurst == 0 {
		l.UploadBurst = defaults.UploadBurst
	}
	if l.UploadRate == 0 {
		l.UploadRate = defaults.UploadRate
	}
	return l
}

// burst and rate pick the settings of a bucket.
func (l RateLimits) burst(kind limitKind) int {
	switch kind {
	case limitAuth:
		return l.AuthBurst
	case limitUpload:
		return l.UploadBurst
	default:
		return l.WriteBurst
	}
}

// rate picks the refill rate of a bucket in requests per second.
func (l RateLimits) rate(kind limitKind) float64 {
	switch kind {
	case limitAuth:
		return l.AuthRate
	case limitUpload:
		return l.UploadRate
	default:
		return l.WriteRate
	}
}

// limitKey identifies one bucket: which kind of work and whose budget it is.
type limitKey struct {
	kind    limitKind
	subject string
}

// bucket is a token bucket refilled lazily: tokens are computed when someone
// asks for one, so an idle client costs nothing but its map entry.
type bucket struct {
	tokens float64
	seen   time.Time
}

// limiter holds the per-subject token buckets. The map is guarded by a mutex
// rather than sharded behind a goroutine: buckets are tiny, refill is arithmetic,
// and the whole point is to answer with a 429 instead of doing work.
type limiter struct {
	limits RateLimits
	now    func() time.Time

	mu        sync.Mutex
	buckets   map[limitKey]*bucket
	lastPrune time.Time
}

// newLimiter builds a limiter with the defaults filled in.
func newLimiter(limits RateLimits) *limiter {
	return &limiter{
		limits:  limits.withDefaults(),
		now:     time.Now,
		buckets: map[limitKey]*bucket{},
	}
}

// allow spends one token of the subject's bucket. When the bucket is empty it
// returns how many milliseconds the client has to wait, which becomes
// retry_after_ms in the M_LIMIT_EXCEEDED body the specification asks for.
func (l *limiter) allow(kind limitKind, subject string) int64 {
	if l.limits.Disabled || subject == "" {
		return 0
	}
	now := l.now()
	burst, rate := float64(l.limits.burst(kind)), l.limits.rate(kind)
	if rate <= 0 {
		rate = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)

	key := limitKey{kind: kind, subject: subject}
	entry, ok := l.buckets[key]
	if !ok {
		entry = &bucket{tokens: burst}
		l.buckets[key] = entry
	}
	entry.tokens += now.Sub(entry.seen).Seconds() * rate
	if entry.tokens > burst {
		entry.tokens = burst
	}
	entry.seen = now
	if entry.tokens >= 1 {
		entry.tokens--
		return 0
	}
	wait := (1 - entry.tokens) / rate
	if wait < 0 {
		wait = 0
	}
	return int64((time.Duration(wait*float64(time.Second)) + time.Millisecond - 1) / time.Millisecond)
}

// idleBucketTTL is how long a bucket may go unused before it is dropped. The
// budget of a client that comes back after that starts fresh, which is fine:
// the limiter protects a running server, it is not an accounting ledger.
const idleBucketTTL = 30 * time.Minute

// pruneInterval bounds how often the map is swept. Sweeping happens on the
// request path instead of in a background goroutine, which keeps the limiter
// free of lifecycle management of its own.
const pruneInterval = time.Minute

// prune drops buckets nobody has touched recently.
func (l *limiter) prune(now time.Time) {
	if !l.lastPrune.IsZero() && now.Sub(l.lastPrune) < pruneInterval {
		return
	}
	for key, entry := range l.buckets {
		if now.Sub(entry.seen) > idleBucketTTL {
			delete(l.buckets, key)
		}
	}
	l.lastPrune = now
}

// rateLimited wraps handler so it draws a token from the bucket of the given
// kind before doing any work. The subject is the device of the authenticated
// request, and the client address when there is no device yet, so an anonymous
// flood cannot borrow someone else's budget.
func (a *App) rateLimited(kind limitKind, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject := a.limitSubject(r, kind)
		if retryAfter := a.limiter.allow(kind, subject); retryAfter > 0 {
			a.logLimited(r, kind, subject, retryAfter, a.rejections.seen(kind, subject))
			a.writeError(w, r, matrix.ErrLimitExceeded(retryAfter))
			return
		}
		handler(w, r)
	}
}

// limitSubject names whose budget a request draws from.
func (a *App) limitSubject(r *http.Request, kind limitKind) string {
	if kind != limitAuth {
		if device := auth.GetDeviceID(r); device != "" {
			return device
		}
	}
	return clientAddress(r)
}

// clientAddress is the peer address of a request with the port removed. Lattice
// has no proxy support yet, so this is the connecting address and not a
// spoofable header.
func clientAddress(r *http.Request) string {
	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

// rejections counts rate limit rejections per bucket so the log stays useful:
// one line for the first rejection of a bucket and one every hundredth after
// that, instead of one per request.
type rejections struct {
	mu     sync.Mutex
	counts map[limitKey]int
}

// seen records a rejection and reports how many this bucket has had.
func (r *rejections) seen(kind limitKind, subject string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts == nil {
		r.counts = map[limitKey]int{}
	}
	key := limitKey{kind: kind, subject: subject}
	r.counts[key]++
	return r.counts[key]
}

// logLimited writes one line for the first rejection of a bucket and one every
// hundredth after it.
func (a *App) logLimited(r *http.Request, kind limitKind, subject string, retryAfter int64, count int) {
	if count != 1 && count%100 != 0 {
		return
	}
	a.logger.Printf("rate limited bucket=%s subject=%s endpoint=%s %s retry_after_ms=%d rejections=%d",
		kind, subject, r.Method, r.URL.Path, retryAfter, count)
}
