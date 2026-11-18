package federation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// Identity is what the transport needs from the local homeserver: the name peers
// know it by, and the key it signs with today. The service layer implements it,
// which keeps this package free of database access.
type Identity interface {
	// ServerName is the homeserver name, the identity every signature makes.
	ServerName() string
	// SigningKey returns the keypair to sign with. Implementations generate the
	// first one and keep it, because a server that changes its signing key
	// invalidates every event it signed before.
	SigningKey(ctx context.Context) (KeyPair, error)
}

// DefaultTimeout bounds one request to a peer. Federation queries are cheap and
// synchronous from the caller's point of view - a join waits on a make_join - so
// the bound is generous without being unbounded.
const DefaultTimeout = 30 * time.Second

// maxResponseBody caps how much of a peer's answer is read. Peers are untrusted:
// a hostile or broken one must not be able to make Lattice allocate memory.
const maxResponseBody = 20 << 20

// Discovery is how a server name becomes a URL. The specification's resolution
// order is well-known delegation, then SRV records, then the hostname with port
// 8448. Lattice implements the well-known step and the fallback, and skips SRV:
// a name that carries an explicit port is used as given, because that is what the
// specification does with it, and because it is the spelling a test or a tunnel
// needs.
const (
	// defaultFederationPort is the port a server name without one implies.
	defaultFederationPort = 8448
	// wellKnownTTL is how long a successful /.well-known/matrix/server answer is
	// reused. The specification recommends 24 hours, with 48 as a ceiling.
	wellKnownTTL = 24 * time.Hour
	// wellKnownFailureTTL is how long a failed lookup is remembered, so a broken
	// peer is not re-probed for every request. The specification suggests an hour.
	wellKnownFailureTTL = time.Hour
	// keyRefetchGap is the shortest distance between two fetches of the same
	// server's keys. Without it, a flood of requests signed with an unknown key
	// becomes a flood of outbound fetches.
	keyRefetchGap = 30 * time.Second
	// keyFetchTimeout bounds one attempt at a key document, which is a small document
	// whose answer decides whether a peer can be spoken to at all. The transport's
	// general timeout is for transactions carrying a room's worth of events; applying
	// that to a key fetch means half a minute waiting for a server that is simply
	// gone, and a room whose state names a handful of those servers multiplies it into
	// minutes of a client staring at "Joining…".
	keyFetchTimeout = 10 * time.Second
	// keyFailureStart and keyFailureCap bound how long a failed key attempt is held
	// back before another is made. A server removed from the DNS does not become
	// reachable because an event of some room asks about it again, and without a
	// memory of the failure every event of that room pays for the fetch again.
	keyFailureStart = 30 * time.Second
	keyFailureCap   = 5 * time.Minute
	// staleKeyCache is how long a key document taken from a key server after its own
	// expiry is kept. What was signed cannot change, so re-asking for it is expense
	// without information.
	staleKeyCache = 24 * time.Hour
)

// keyFailureWindow is how long a failed attempt at a server is held back: doubling
// from [keyFailureStart] up to [keyFailureCap]. Enough distance that a dead peer
// stops costing a room's worth of timeouts, short enough that a peer renewing its
// certificate is not shut out for the rest of the day.
func keyFailureWindow(attempts int) time.Duration {
	window := keyFailureStart
	for i := 1; i < attempts && window < keyFailureCap; i++ {
		window *= 2
	}
	if window > keyFailureCap {
		return keyFailureCap
	}
	return window
}

// Config configures a [Transport].
type Config struct {
	// PeerBaseURLs maps a server name to the URL to reach that server on,
	// bypassing name resolution and TLS discovery. It exists for tests, which
	// cannot run a publicly certified HTTPS server, and for setups where a peer
	// is reachable through a fixed tunnel.
	PeerBaseURLs map[string]string
	// UserAgent names this implementation on every request, so an operator
	// reading their peer's access log can see who called.
	UserAgent string
	// Timeout bounds a single request. Zero means [DefaultTimeout].
	Timeout time.Duration
	// Now is overridable so time-dependent behaviour is testable.
	Now func() time.Time
	// RootCAs replaces the trusted certificate authorities of outbound requests.
	// Federation is TLS-first: a peer is reached at https://host:8448, and the
	// certificate is the only thing between this server and an impostor that knows
	// the server's name. Deployments behind a private CA name that CA here instead of
	// disabling verification, which would trade the one check that matters for
	// convenience.
	RootCAs *x509.CertPool
	// KeyPerspectives names the servers asked for a peer's keys when the peer cannot
	// hand them over itself - the second half of the specification's key exchange,
	// which corroborates a peer's keys with a third party.
	//
	// It exists for the state a room carries about servers that no longer answer: a
	// renamed or deleted homeserver leaves events behind in every room it spoke in,
	// and its signing keys can then only come from someone who saw them while they
	// were current. A key server is asked only after the peer's own answer failed or
	// turned out unusable, so a healthy peer is still trusted the direct way the whole
	// transport is built on.
	KeyPerspectives []string
	// KeyFetchTimeout bounds one attempt at a key document - at the peer itself, and
	// at each key server asked after it. A small document whose answer decides whether
	// a peer can be spoken to at all should not borrow the timeout of a transaction
	// carrying a room's worth of events.
	KeyFetchTimeout time.Duration
}

// Transport talks to other homeservers: it resolves their names, signs every
// request with this server's key, verifies their answers' status codes into
// Matrix errors, and keeps the verify keys it has already checked.
//
// Keys of peers are trusted on direct fetch: Lattice asks the peer for its own
// document at /_matrix/key/v2/server, checks that it names the server that was
// asked, that it is signed by a key it publishes, and that valid_until_ts has not
// passed. That is the model the specification allows when TLS authenticates the
// connection, since the certificate proves the peer owns the hostname. Going
// through notary servers - the other half of the specification's key exchange,
// which corroborates a peer's keys with a third party - is not implemented: it is
// recorded in docs/FINDINGS.md as deferred, along with acting as a notary for others.
type Transport struct {
	cfg      Config
	identity Identity
	client   *http.Client
	now      func() time.Time

	mu    sync.Mutex
	keys  map[string]*keyEntry
	peers map[string]*peerEntry
	// flights holds the key fetch one server is currently busy with, so that the
	// twenty events waiting for the same document do not each start one.
	flights map[string]keyFlight
}

// keyFlight is the key fetch a server is busy with: waiting on its channel is waiting
// for the answer to have reached the cache.
type keyFlight chan struct{}

// keyEntry is a peer's verified key set together with the moment it stops being
// usable, which is the earlier of the document's own expiry and the seven day cap.
// The published document is kept as it was decoded because a notary re-signs that
// document rather than a reconstruction of it.
type keyEntry struct {
	set       *KeySet
	document  matrix.Object
	expiresMS int64
	fetchedMS int64
	// stale says the set came from a document that had already passed its own
	// valid_until_ts. Such a key verifies the events written while it was current and
	// nothing newer, which is the difference between believing an old server and
	// believing whoever answers for it today.
	stale bool
	// failures, failedMS and lastErr remember a failed attempt, so the next caller is
	// answered from memory instead of by waiting all over again.
	failures int
	failedMS int64
	lastErr  string
}

// peerEntry is a resolved base URL and how long it may be reused.
type peerEntry struct {
	baseURL   string
	expiresMS int64
}

// NewTransport builds a transport for the given local identity.
func NewTransport(identity Identity, cfg Config) *Transport {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Lattice"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.KeyFetchTimeout <= 0 {
		cfg.KeyFetchTimeout = keyFetchTimeout
	}
	transport := &http.Transport{}
	if cfg.RootCAs != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: cfg.RootCAs, MinVersion: tls.VersionTLS12}
	}
	return &Transport{
		cfg:      cfg,
		identity: identity,
		client:   &http.Client{Timeout: cfg.Timeout, Transport: transport},
		now:      cfg.Now,
		keys:     map[string]*keyEntry{},
		peers:    map[string]*peerEntry{},
		flights:  map[string]keyFlight{},
	}
}

// ServerName is the name this transport signs as.
func (t *Transport) ServerName() string { return t.identity.ServerName() }

// GetRaw performs a signed GET and returns the answer as it arrived: the content type, the
// headers and the bytes. Media is what needs this. A JSON endpoint's answer is decoded and
// re-encoded on its way to a client, and a picture that goes through the same hands arrives as
// a different file - its hash changes, and the event that named it no longer describes it.
// limit caps how much of the answer is read, because the peer decides how big its own content
// is and this server should not agree in advance to hold all of it in memory.
func (t *Transport) GetRaw(ctx context.Context, destination, path string, query url.Values, limit int64) (http.Header, []byte, error) {
	if query == nil {
		query = url.Values{}
	}
	if query.Get("origin") == "" {
		query.Set("origin", t.identity.ServerName())
	}
	return t.roundTrip(ctx, destination, http.MethodGet, path, query, nil, limit)
}

// Get performs a signed GET and returns the JSON object the peer answers with.
// The query gets an origin parameter when it has none, which is what senders do
// here so a receiver can tie the request to the signing server even before it
// looks at the header.
func (t *Transport) Get(ctx context.Context, destination, path string, query url.Values) (matrix.Object, error) {
	if query == nil {
		query = url.Values{}
	}
	if query.Get("origin") == "" {
		query.Set("origin", t.identity.ServerName())
	}
	return t.do(ctx, destination, http.MethodGet, path, query, nil)
}

// Post performs a signed POST with a JSON body.
func (t *Transport) Post(ctx context.Context, destination, path string, body matrix.Object) (matrix.Object, error) {
	return t.do(ctx, destination, http.MethodPost, path, nil, body)
}

// Put performs a signed PUT with a JSON body.
func (t *Transport) Put(ctx context.Context, destination, path string, body matrix.Object) (matrix.Object, error) {
	return t.do(ctx, destination, http.MethodPut, path, nil, body)
}

// do signs and sends one request. The uri that is signed is rebuilt from the same
// values the URL is built from, so the signature covers the bytes on the wire:
// url.Values.Encode sorts its keys, and RawQuery is set to exactly that string, so
// a peer verifying the target it received sees the target that was signed.
func (t *Transport) do(ctx context.Context, destination, method, path string, query url.Values, body matrix.Object) (matrix.Object, error) {
	_, raw, err := t.roundTrip(ctx, destination, method, path, query, body, maxResponseBody)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return matrix.Object{}, nil
	}
	// Canonical decoding keeps numbers exact, which matters as soon as an answer
	// carries an event that has to be verified: a timestamp re-encoded through
	// float64 changes the bytes the signature was made over.
	decoded, err := matrix.DecodeCanonical(raw)
	if err != nil {
		return nil, fmt.Errorf("decoding the answer from %s: %w", destination, err)
	}
	object, ok := decoded.(matrix.Object)
	if !ok {
		return nil, fmt.Errorf("the answer from %s is not a JSON object", destination)
	}
	return object, nil
}

// roundTrip signs and sends one request, and hands back the answer without reading it as
// anything in particular.
func (t *Transport) roundTrip(ctx context.Context, destination, method, path string, query url.Values, body matrix.Object, limit int64) (http.Header, []byte, error) {
	if limit <= 0 {
		limit = maxResponseBody
	}
	base, err := t.baseURL(ctx, destination)
	if err != nil {
		return nil, nil, err
	}
	key, err := t.identity.SigningKey(ctx)
	if err != nil {
		return nil, nil, err
	}

	// A caller may hand over a path that already carries its query. Splitting it off
	// and merging is not tidiness: appending a second "?" would leave the earlier
	// parameters inside the path and glue the transport's own parameters onto the last
	// value, so a make_join that offered room version 11 arrived offering "11?origin="
	// and the peer answered that no version was shared.
	if cut := strings.Index(path, "?"); cut >= 0 {
		merged, err := url.ParseQuery(path[cut+1:])
		if err != nil {
			return nil, nil, fmt.Errorf("the query of %s: %w", path, err)
		}
		if query == nil {
			query = url.Values{}
		}
		for name, values := range merged {
			query[name] = append(query[name], values...)
		}
		path = path[:cut]
	}

	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	// A request that is not a GET always carries a body, so it always signs one:
	// a nil body would be signed as no content member while the receiver would
	// parse the "null" that json.Marshal produces, and the two documents differ.
	if method != http.MethodGet && body == nil {
		body = matrix.Object{}
	}
	fields, err := SignAuthorization(key, method, target, t.identity.ServerName(), destination, body)
	if err != nil {
		return nil, nil, fmt.Errorf("signing a request for %s: %w", destination, err)
	}

	destinationURL, err := url.Parse(base + target)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving %s on %s: %w", target, destination, err)
	}
	var payload io.Reader
	if method != http.MethodGet {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding the body for %s: %w", destination, err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, destinationURL.String(), payload)
	if err != nil {
		return nil, nil, fmt.Errorf("building a request for %s: %w", destination, err)
	}
	request.Header.Set("Authorization", fields.AuthorizationHeader())
	request.Header.Set("User-Agent", t.cfg.UserAgent)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := t.client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("requesting %s from %s: %w", target, destination, err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return nil, nil, fmt.Errorf("reading the answer from %s: %w", destination, err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, nil, peerError(response.StatusCode, raw, destination)
	}
	return response.Header, raw, nil
}

// peerError turns a non-2xx answer into the error the peer reported. The errcode
// survives because callers branch on it: a join refused with
// M_UNABLE_TO_GRANT_JOIN means "ask somebody else", and flattening the answer
// into a string loses the only part of it that is actionable.
func peerError(status int, raw []byte, destination string) error {
	var body struct {
		Errcode     string `json:"errcode"`
		Error       string `json:"error"`
		RoomVersion string `json:"room_version"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Errcode != "" {
		if body.Errcode == string(matrix.ErrCodeUnauthorized) {
			// A peer refusing to believe this server's signature is not the client's
			// authentication failing, and M_UNAUTHORIZED is exactly the code a client reads as
			// "your access token is no good" - a client logs its user out over it, and the
			// message it shows names a server the user has never heard of. What the answer
			// means is that the peer could not identify this server, which is an operator's
			// problem: a peer checks a request from here against
			// https://<this server>/_matrix/key/v2/server at its federation port, and one that
			// cannot reach that document has no way to believe anything sent from here.
			log.Printf("federation: %s answered %d with M_UNAUTHORIZED: %s - a peer that cannot "+
				"fetch this server's /_matrix/key/v2/server cannot verify a request from it; check "+
				"that the federation port answers over TLS and that /.well-known/matrix/server "+
				"names the host and port peers should use", destination, status, body.Error)
			return &matrix.Error{
				Code:    matrix.ErrCodeUnknown,
				Message: fmt.Sprintf("%s would not accept a request signed by this server: %s", destination, body.Error),
				Status:  http.StatusBadGateway,
			}
		}
		err := &matrix.Error{
			Code:    matrix.ErrorCode(body.Errcode),
			Message: body.Error,
			Status:  status,
		}
		if body.RoomVersion != "" {
			// M_INCOMPATIBLE_ROOM_VERSION is meaningless without the version it refused.
			// The specification puts the room's version in the body for exactly that reason:
			// the answer to "we cannot speak to this room" is the version the room is in.
			err.Extra = matrix.Object{"room_version": body.RoomVersion}
		}
		return err
	}
	return &matrix.Error{
		Code:    matrix.ErrCodeUnknown,
		Message: fmt.Sprintf("%s answered %d", destination, status),
		Status:  status,
	}
}

// Keys returns the verified keys of a server, fetching them when the cache is
// empty or past the validity of what it holds.
func (t *Transport) Keys(ctx context.Context, serverName string) (*KeySet, error) {
	return t.keySet(ctx, serverName, false)
}

// keySet reads the cache and, when that cannot answer, fetches: first from the peer
// itself, then from the key servers this transport was given.
//
// Three things decide how long a caller waits here, and all three exist because a
// room's state is verified event by event. What the cache holds answers the question
// whenever it is still in force. A failed attempt is remembered with growing
// distance, so a server that no longer answers is waited for once rather than once
// per event of every room that still names it. And one attempt per server is in
// flight at a time, because verifying a room means asking the same handful of
// questions at the same moment.
func (t *Transport) keySet(ctx context.Context, serverName string, force bool) (*KeySet, error) {
	for {
		nowMS := t.now().UnixMilli()
		t.mu.Lock()
		entry := t.keys[serverName]
		// A forced refresh is still throttled. Both reasons for forcing one - an unknown
		// key id and a validity window the cache cannot cover - can be provoked by a
		// peer, and a homeserver that answers every provocation with an outbound request
		// is a reflection amplifier.
		if entry != nil && entry.expiresMS > nowMS &&
			(!force || nowMS-entry.fetchedMS < keyRefetchGap.Milliseconds()) {
			set := entry.set
			t.mu.Unlock()
			return set, nil
		}
		if entry != nil && entry.failures > 0 &&
			nowMS-entry.failedMS < keyFailureWindow(entry.failures).Milliseconds() {
			window := keyFailureWindow(entry.failures)
			message := entry.lastErr
			t.mu.Unlock()
			return nil, fmt.Errorf("no key could be had for %s: %s (nothing is asked of that server again for %s)",
				serverName, message, window)
		}
		// Whoever is already fetching this server's document wants the same answer, so
		// wait for them and read what they left behind.
		if waiting := t.flights[serverName]; waiting != nil {
			t.mu.Unlock()
			select {
			case <-waiting:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		t.flights[serverName] = make(keyFlight)
		t.mu.Unlock()

		fetched, err := t.fetchKeys(ctx, serverName, nowMS)

		t.mu.Lock()
		close(t.flights[serverName])
		delete(t.flights, serverName)
		if entry == nil {
			entry = &keyEntry{}
			t.keys[serverName] = entry
		}
		failedMS := t.now().UnixMilli()
		switch {
		case err != nil && entry.set != nil && entry.expiresMS > failedMS:
			// A peer that is briefly unreachable must not invalidate the keys that are
			// still inside their validity window: signatures of older events have to stay
			// checkable, and the specification asks the same of key servers.
			err = nil
		case err != nil:
			entry.failures++
			entry.failedMS = failedMS
			entry.lastErr = err.Error()
		default:
			entry.set = fetched.set
			entry.document = fetched.document
			entry.stale = fetched.stale
			if fetched.stale {
				// What was signed cannot change, so the document is kept for a while; it is
				// only ever believed about events written before its own moment.
				entry.expiresMS = failedMS + staleKeyCache.Milliseconds()
			} else {
				entry.expiresMS = fetched.set.Expires(failedMS)
			}
			entry.fetchedMS = failedMS
			entry.failures = 0
			entry.lastErr = ""
		}
		set := entry.set
		t.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if set == nil {
			return nil, fmt.Errorf("no keys are known for %s", serverName)
		}
		return set, nil
	}
}

// keyFetch is what one attempt at a peer's signing keys produced.
type keyFetch struct {
	set      *KeySet
	document matrix.Object
	// stale says the document had already passed its own valid_until_ts when it
	// arrived, which is normal for a server that has been dead for years and is
	// answered by someone who saw the document while it lived.
	stale bool
}

// fetchKeys asks the peer for its own key document and, when that cannot be had or
// cannot be believed, asks each named key server in turn.
//
// The peer is always asked first: trusting a peer's own keys over a TLS connection is
// the model this transport is built on, and a key server only corroborates it. What is
// asked of a key server's answer is that the document names the server asked about and
// is signed by the keys it publishes - the same discipline the peer's own answer is
// held to, and the boundary past which a key server stops corroborating and starts
// choosing. A key server is otherwise a source the operator named, which is the trust
// the whole scheme rests on: whoever is named can relay keys, and cannot invent a
// signature - an event is only ever believed when the key it names verifies it.
//
// A key server's answer is then judged as a document rather than as a fresh one - the
// clock is left out on purpose, because the reason a dead server's keys are wanted is
// to verify events it signed while it was alive. [Transport.VerifyKeyOfAsOf] is what
// keeps that belief bounded: such a key verifies the events written up to its own
// expiry and nothing newer.
func (t *Transport) fetchKeys(ctx context.Context, serverName string, nowMS int64) (keyFetch, error) {
	document, err := t.getKeyDocument(ctx, serverName)
	if err == nil {
		set, verr := VerifyKeyDocument(document, serverName, nowMS)
		if verr == nil {
			return keyFetch{set: set, document: document}, nil
		}
		err = verr
	}
	if len(t.cfg.KeyPerspectives) == 0 {
		return keyFetch{}, err
	}
	problem := fmt.Errorf("no key server answered (the server itself said: %v)", err)
	for _, notary := range t.cfg.KeyPerspectives {
		if notary == "" || notary == serverName {
			continue
		}
		handed, qerr := t.queryKeyServer(ctx, notary, serverName)
		if qerr != nil {
			// Both halves belong in the error. "Why can this room not be opened" is asked
			// of an operator who has to fix one of two things: a peer that is not answering,
			// or a key server that was never told about it.
			problem = fmt.Errorf("%s: %v (the server itself said: %v)", notary, qerr, err)
			continue
		}
		set, verr := verifyKeyDocument(handed, serverName)
		if verr != nil {
			// A document that does not name the asked server, or is not signed by the keys
			// it publishes, is refused rather than forgiven: that is the one check standing
			// between this server and a key server vouching for anything it likes.
			problem = fmt.Errorf("%s answered for %s, and the answer does not hold: %v", notary, serverName, verr)
			continue
		}
		return keyFetch{set: set, document: handed, stale: set.ValidUntilMS <= nowMS}, nil
	}
	return keyFetch{}, problem
}

// getKeyDocument asks a peer for its own published keys, under the short bound a small
// document deserves.
func (t *Transport) getKeyDocument(ctx context.Context, serverName string) (matrix.Object, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, t.cfg.KeyFetchTimeout)
	defer cancel()
	return t.Get(fetchCtx, serverName, "/_matrix/key/v2/server", nil)
}

// queryKeyServer asks one key server for another server's keys. The specification's
// answer is a list of documents plus a map naming the servers it could not answer for;
// the document naming the server asked about is the one that counts, and a missing one
// is reported with whatever reason the key server gave.
func (t *Transport) queryKeyServer(ctx context.Context, notary, serverName string) (matrix.Object, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, t.cfg.KeyFetchTimeout)
	defer cancel()
	answer, err := t.Post(fetchCtx, notary, "/_matrix/key/v2/query", matrix.Object{
		"server_keys": matrix.Object{serverName: matrix.Object{}},
	})
	if err != nil {
		return nil, err
	}
	if reason := failureReason(answer, serverName); reason != "" {
		return nil, fmt.Errorf("it says: %s", reason)
	}
	documents, err := objectList(answer["server_keys"], "server_keys", false)
	if err != nil {
		return nil, err
	}
	for _, document := range documents {
		if named, _ := document["server_name"].(string); named == serverName {
			return document, nil
		}
	}
	return nil, fmt.Errorf("it holds no keys for %s", serverName)
}

// failureReason reads what a key server said about the one server it could not answer
// for.
func failureReason(answer matrix.Object, serverName string) string {
	failures, _ := answer["failures"].(map[string]any)
	if failures == nil {
		return ""
	}
	reason, _ := failures[serverName].(string)
	return reason
}

// PeerKeys returns a peer's verified keys together with the published document.
// The document is what a notary signs: handing out keys this server vouches for
// means signing the peer's own answer, not a summary of it, so the caller gets the
// fields as they arrived.
//
// minValidUntilMS is what the requester asked the keys to stay valid until. When
// the cached document would expire before that moment the keys are fetched again,
// because a notary that answers with keys that are already stale to the asker has
// answered in vain.
func (t *Transport) PeerKeys(ctx context.Context, serverName string, minValidUntilMS int64) (*KeySet, matrix.Object, error) {
	t.mu.Lock()
	entry := t.keys[serverName]
	force := entry != nil && entry.expiresMS < minValidUntilMS
	t.mu.Unlock()
	set, err := t.keySet(ctx, serverName, force)
	if err != nil {
		return nil, nil, err
	}
	t.mu.Lock()
	entry = t.keys[serverName]
	var document matrix.Object
	if entry != nil {
		document = entry.document
	}
	t.mu.Unlock()
	return set, document, nil
}

// VerifyKeyOf resolves the key a peer signed with, for something happening now: a
// request arriving over the wire, or a key server's answer about a live server. A key
// known only from a document that has since expired is refused here, whatever it once
// signed.
func (t *Transport) VerifyKeyOf(ctx context.Context, serverName, keyID string) (ed25519.PublicKey, error) {
	return t.VerifyKeyOfAsOf(ctx, serverName, keyID, t.now().UnixMilli())
}

// VerifyKeyOfAsOf resolves the key that signed something written at asOfMS. The moment
// matters because a room keeps verifying events whose servers have been gone for years:
// a key learned from a key server after that document expired is believed for those
// events and for nothing written after the document's own expiry, which is the
// difference between believing an old server and believing whoever answers for it today.
//
// An unknown key id triggers one refresh of that peer's document - keys are rotated,
// and the classic symptom of a cache that never refreshes is a peer whose every request
// is refused after it reboots - but never more than once per [keyRefetchGap], so an
// attacker cannot turn bad requests into outbound traffic.
func (t *Transport) VerifyKeyOfAsOf(ctx context.Context, serverName, keyID string, asOfMS int64) (ed25519.PublicKey, error) {
	set, err := t.keySet(ctx, serverName, false)
	if err != nil {
		return nil, err
	}
	if publicKey, found := set.Key(keyID); found {
		if err := t.believes(set, keyID, asOfMS); err != nil {
			return nil, err
		}
		return publicKey, nil
	}
	nowMS := t.now().UnixMilli()
	t.mu.Lock()
	entry := t.keys[serverName]
	recent := entry != nil && nowMS-entry.fetchedMS < keyRefetchGap.Milliseconds()
	t.mu.Unlock()
	if recent {
		return nil, fmt.Errorf("%s signed with %s, which it does not publish", serverName, keyID)
	}
	set, err = t.keySet(ctx, serverName, true)
	if err != nil {
		return nil, err
	}
	publicKey, found := set.Key(keyID)
	if !found {
		return nil, fmt.Errorf("%s signed with %s, which it does not publish", serverName, keyID)
	}
	if err := t.believes(set, keyID, asOfMS); err != nil {
		return nil, err
	}
	return publicKey, nil
}

// believes is the bound that comes with a stale document: its keys are the peer's, but
// only up to the moment the peer stopped vouching for them.
func (t *Transport) believes(set *KeySet, keyID string, asOfMS int64) error {
	if asOfMS <= set.ValidUntilMS {
		return nil
	}
	t.mu.Lock()
	stale := false
	if entry := t.keys[set.ServerName]; entry != nil {
		stale = entry.stale
	}
	t.mu.Unlock()
	if !stale {
		// A document fetched from the peer itself carries its own expiry and is in force
		// until then; anything reaching here with a stale document has a cache to read.
		return nil
	}
	return fmt.Errorf("the key %s is known only from %s's expired document, which vouches for nothing written after %d",
		keyID, set.ServerName, set.ValidUntilMS)
}

// baseURL resolves where a server name is reachable: an injected URL, then the
// /.well-known/matrix/server delegation, then the name itself on the port it
// names or on 8448.
func (t *Transport) baseURL(ctx context.Context, serverName string) (string, error) {
	if serverName == "" {
		return "", errors.New("no server name to reach")
	}
	if base, found := t.cfg.PeerBaseURLs[serverName]; found {
		return strings.TrimSuffix(base, "/"), nil
	}
	nowMS := t.now().UnixMilli()
	t.mu.Lock()
	entry := t.peers[serverName]
	fresh := entry != nil && entry.expiresMS > nowMS
	cached := entry
	t.mu.Unlock()
	if fresh {
		if cached.baseURL == "" {
			return "", fmt.Errorf("no federation endpoint known for %s", serverName)
		}
		return cached.baseURL, nil
	}
	base, err := t.resolveBaseURL(ctx, serverName)
	if err != nil {
		// The failure is remembered as well as the success: a peer whose
		// well-known document is broken should be probed again in an hour, not on
		// every request.
		t.rememberBase(serverName, "", nowMS+wellKnownFailureTTL.Milliseconds())
		return "", err
	}
	t.rememberBase(serverName, base, nowMS+wellKnownTTL.Milliseconds())
	return base, nil
}

// rememberBase stores a resolution result, an empty URL meaning "this name could
// not be resolved".
func (t *Transport) rememberBase(serverName, baseURL string, expiresMS int64) {
	t.mu.Lock()
	t.peers[serverName] = &peerEntry{baseURL: baseURL, expiresMS: expiresMS}
	t.mu.Unlock()
}

// resolveBaseURL applies the discovery rules: a name with an explicit port is used
// as it stands, otherwise the well-known delegation is consulted, and its m.server
// value is used in place of the name.
func (t *Transport) resolveBaseURL(ctx context.Context, serverName string) (string, error) {
	host, _, hasPort := ServerNameParts(serverName)
	if !hasPort {
		// A well-known document that is missing, unparseable, or on a server that
		// refuses the connection means "no delegation", not "this peer cannot be
		// reached": the discovery rules fall back to the name itself on the registered
		// federation port. Treating the lookup's failure as fatal makes every server
		// that does not publish a delegation unreachable, which is most of them - and
		// the failure arrives as a connection error rather than a 404, because the
		// lookup goes to port 443 whether anything listens there or not.
		delegated, err := t.wellKnownServer(ctx, host)
		if err != nil {
			log.Printf("federation: no server delegation from %s, using the name as it stands: %v", host, err)
		}
		if delegated != "" {
			serverName = delegated
		}
	}
	// A name without a port means the registered federation port, not 443: a
	// homeserver behind a reverse proxy on 443 publishes that through
	// /.well-known/matrix/server rather than by dropping the port.
	if _, _, hasPort := ServerNameParts(serverName); !hasPort {
		serverName = fmt.Sprintf("%s:%d", serverName, defaultFederationPort)
	}
	return "https://" + serverName, nil
}

// wellKnownServer asks a hostname for its delegation. Any answer that is not a
// usable document means "no delegation", which is what the specification says to
// treat it as: the lookup then falls back to the hostname itself.
func (t *Transport) wellKnownServer(ctx context.Context, host string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/.well-known/matrix/server", nil)
	if err != nil {
		return "", fmt.Errorf("building the well-known request for %s: %w", host, err)
	}
	request.Header.Set("User-Agent", t.cfg.UserAgent)
	response, err := t.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("asking %s for its federation server: %w", host, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading the well-known document of %s: %w", host, err)
	}
	var document struct {
		Server string `json:"m.server"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", nil
	}
	delegated := strings.TrimSpace(document.Server)
	if delegated == "" || strings.Contains(delegated, "/") {
		return "", nil
	}
	return delegated, nil
}

// ServerNameParts splits a Matrix server name into the host to connect to and the
// port to connect on, reporting whether the name named a port at all.
//
// The grammar is host[:port], where host may be an IPv6 literal in brackets. That
// is the whole trap: the colons inside an IPv6 literal are not port separators, and
// the specification demands brackets when a literal carries a port. So "::1" is
// loopback with no port - which means the default federation port applies - while
// "[::1]:9093" is loopback on 9093. Getting this wrong sends a homeserver's traffic
// to the wrong place, which looks like a broken peer.
func ServerNameParts(serverName string) (host, port string, hasPort bool) {
	if strings.HasPrefix(serverName, "[") {
		closing := strings.Index(serverName, "]")
		if closing == -1 {
			return serverName, "", false
		}
		rest := serverName[closing+1:]
		switch {
		case rest == "":
			return serverName[:closing+1], "", false
		case rest == ":":
			// A trailing colon names no port; the default applies rather than an
			// empty one.
			return serverName[:closing+1], "", false
		case strings.HasPrefix(rest, ":"):
			return serverName[:closing+1], rest[1:], true
		default:
			// Trailing junk after the brackets is not a port, and is not this
			// function's business either: the host is what a caller needs.
			return serverName, "", false
		}
	}
	if strings.Count(serverName, ":") > 1 {
		// More than one bare colon is an IPv6 literal, and an unbracketed literal
		// cannot carry a port.
		return serverName, "", false
	}
	if at := strings.Index(serverName, ":"); at >= 0 {
		if at == len(serverName)-1 {
			return serverName[:at], "", false
		}
		return serverName[:at], serverName[at+1:], true
	}
	return serverName, "", false
}

// LoadCAs reads PEM certificate files and returns the trust store they add to. An
// empty list returns nil, which means "use the system's trust store" - the answer for
// almost every deployment.
func LoadCAs(paths []string) (*x509.CertPool, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		// A system without a trust store still gets the named CAs, rather than an
		// empty pool that would reject every peer including the ones that are fine.
		pool = x509.NewCertPool()
	}
	for _, path := range paths {
		blob, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("the certificate authority %s cannot be read: %w", path, err)
		}
		if !pool.AppendCertsFromPEM(blob) {
			return nil, fmt.Errorf("the certificate authority %s holds no certificate", path)
		}
	}
	return pool, nil
}
