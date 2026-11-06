package federation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/roomversion"
)

// maxKeyAge bounds how long a published key document may be trusted, whatever its own
// expiry says. The specification requires this clamp: without it, one compromised or
// careless homeserver could publish a key that stays trusted for a year, with no way
// for its owner to take it back.
const maxKeyAge = 7 * 24 * time.Hour

// keyFetchInterval is how often the keyring will ask a server for its keys again when
// the cached answer is already useless. Flooding a remote with key requests on every
// signature check is both rude and a way to hide the fact that nothing is cached.
const keyFetchInterval = time.Minute

// Keyring knows the signing keys of other homeservers: what is cached, and where to
// look when the cache has nothing.
type Keyring struct {
	repo     persist.FederationRepo
	resolver Resolver
	// HTTP makes the key requests. Key endpoints carry no credential, so this client
	// needs no signer — but it does need the resolver, so it dials the address the
	// name pointed at.
	HTTP *http.Client
	// clients hands out the per-destination clients that verify a key document's TLS
	// connection against the server's name rather than the address it resolved to.
	clients  *nameVerifyingClients
	ours     string
	notaries []string
	log      *slog.Logger
	Now      func() time.Time

	mu        sync.Mutex
	lastFetch map[string]time.Time
}

// NewKeyring returns a keyring that caches in repo and fetches through the resolver.
func NewKeyring(repo persist.FederationRepo, resolver Resolver, httpClient *http.Client, serverName string, notaries []string, log *slog.Logger) *Keyring {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Keyring{
		repo:      repo,
		resolver:  resolver,
		HTTP:      httpClient,
		clients:   newNameVerifyingClients(httpClient),
		ours:      serverName,
		notaries:  notaries,
		log:       log,
		Now:       time.Now,
		lastFetch: map[string]time.Time{},
	}
}

// Lookup returns a signing key for a server, fetching the server's published keys when
// this server does not already hold a usable copy.
//
// Answering "no such key" is a real outcome, and the honest one: a key this server has
// never seen, or saw and can no longer trust, is not a key.
func (k *Keyring) Lookup(ctx context.Context, origin, keyID string) (ed25519.PublicKey, error) {
	if origin == k.ours {
		return nil, fmt.Errorf("federation: %s is this server; its own keys are not looked up over the network", origin)
	}
	rows, err := k.keys(ctx, origin, true)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.KeyID != keyID {
			continue
		}
		if len(row.Key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("federation: the recorded key %s for %s is %d bytes", keyID, origin, len(row.Key))
		}
		return ed25519.PublicKey(row.Key), nil
	}
	return nil, fmt.Errorf("federation: %s has never published a key %s that this server holds", origin, keyID)
}

// LookupForRoom returns the lookup that checking an event uses.
//
// The room version matters because the expiry of a key document matters only where the
// room's rules say it does: versions 1 to 4 ignore it, and refusing an old room's
// events for that reason would be this server inventing a rule.
func (k *Keyring) LookupForRoom(ctx context.Context, rules roomversion.Rules) func(origin, keyID string) (ed25519.PublicKey, error) {
	enforce := !roomversion.IgnoresKeyExpiry(rules)
	return func(origin, keyID string) (ed25519.PublicKey, error) {
		rows, err := k.keys(ctx, origin, enforce)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.KeyID != keyID {
				continue
			}
			if len(row.Key) != ed25519.PublicKeySize {
				return nil, fmt.Errorf("federation: the recorded key %s for %s is %d bytes", keyID, origin, len(row.Key))
			}
			return ed25519.PublicKey(row.Key), nil
		}
		return nil, fmt.Errorf("federation: %s has never published a key %s that this server holds", origin, keyID)
	}
}

// keys returns the cached keys for a server, refreshing them when they are missing or
// no longer usable.
func (k *Keyring) keys(ctx context.Context, serverName string, enforceExpiry bool) ([]persist.ServerSigningKey, error) {
	rows, err := k.repo.ServerKeys(ctx, serverName)
	if err != nil {
		return nil, fmt.Errorf("federation: cannot read the cached keys for %s: %w", serverName, err)
	}
	now := k.now()
	usable := make([]persist.ServerSigningKey, 0, len(rows))
	for _, row := range rows {
		// A retired key is usable until the moment it says it stopped being usable,
		// which is what lets an event signed before a rotation still be checked.
		if row.ExpiredTS > 0 {
			if !enforceExpiry || row.ExpiredTS > now.UnixMilli() {
				usable = append(usable, row)
			}
			continue
		}
		if !enforceExpiry || row.ValidUntilTS > now.UnixMilli() {
			usable = append(usable, row)
		}
	}
	if len(usable) > 0 {
		return usable, nil
	}
	if !k.shouldFetch(serverName, now) {
		if len(rows) == 0 {
			return nil, fmt.Errorf("federation: %s has no keys recorded and was asked for too recently to ask again", serverName)
		}
		return nil, fmt.Errorf("federation: the keys recorded for %s have expired and %s was asked too recently to be asked again",
			serverName, serverName)
	}
	document, keys, err := k.fetch(ctx, serverName)
	if err != nil {
		// A failed attempt is not as lasting as an answered one. The interval between asks is long
		// because a server's key document does not change minute to minute; a connection that did
		// not open, or a reply that never came, says nothing about the keys and waiting a minute
		// for the next try would leave every event this server is sent from there unjudgeable for
		// a minute — long enough for a join to fail and for the room's events to be refused and
		// retried over and over. The next ask comes round soon instead, still bounded so that a
		// server which genuinely publishes nothing is not hammered once per event.
		k.noteFailedFetch(serverName, k.now())
		k.log.Warn("federation: could not fetch a server's keys",
			"server_name", serverName, "error", err)
		return nil, err
	}
	// An answered ask is worth the full interval before another: the document the server gave
	// does not change minute to minute, and asking again on every event would be a denial of
	// service this server hands itself.
	k.noteFetch(serverName, k.now())
	_ = document
	if err := k.repo.SaveServerKeys(ctx, document, keys); err != nil {
		return nil, fmt.Errorf("federation: cannot record the keys fetched for %s: %w", serverName, err)
	}
	return keys, nil
}

// shouldFetch says whether this server may be asked for its keys again yet. It does not record the
// ask: whether the attempt is worth the full interval is only known once it has been answered, and
// charging a failed attempt the same as an answered one is what makes a transient connection
// problem cost a minute of unjudgeable events. See noteFailedFetch.
func (k *Keyring) shouldFetch(serverName string, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	last, ok := k.lastFetch[serverName]
	return !ok || now.Sub(last) >= keyFetchInterval
}

// noteFetch records an answered ask.
func (k *Keyring) noteFetch(serverName string, now time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lastFetch[serverName] = now
}

// keyFetchRetry is how soon a server is asked again after an attempt that never got an answer.
const keyFetchRetry = 2 * time.Second

// noteFailedFetch moves the next ask for a server close enough to retry that a busy network does
// not stand between this server and a room's events for a full interval.
func (k *Keyring) noteFailedFetch(serverName string, now time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lastFetch[serverName] = now.Add(-(keyFetchInterval - keyFetchRetry))
}

// fetchedKeys is a validated key document and the keys it publishes.
type fetchedKeys struct {
	serverName   string
	validUntilMS int64
	verify       map[string]ed25519.PublicKey
	old          map[string]oldKey
	document     []byte
}

// oldKey is a retired key and the moment it stopped being usable.
type oldKey struct {
	key       ed25519.PublicKey
	expiredMS int64
}

// fetch asks a server for its keys, directly and then through any notary, and returns
// the first answer that stands up to checking.
func (k *Keyring) fetch(ctx context.Context, serverName string) (*persist.ServerKeyDocument, []persist.ServerSigningKey, error) {
	var problems []string
	if document, err := k.fetchDirect(ctx, serverName); err == nil {
		return k.record(serverName, document)
	} else {
		problems = append(problems, err.Error())
	}
	for _, notary := range k.notaries {
		document, err := k.fetchViaNotary(ctx, notary, serverName)
		if err != nil {
			problems = append(problems, fmt.Sprintf("via %s: %v", notary, err))
			continue
		}
		return k.record(serverName, document)
	}
	return nil, nil, fmt.Errorf("federation: no usable key document for %s (%s)", serverName, strings.Join(problems, "; "))
}

// httpFor returns the client to reach a resolved key endpoint with: one that verifies the
// certificate against the server's name when the address is an IP that name resolved to.
func (k *Keyring) httpFor(resolved Target) *http.Client {
	if k.clients == nil {
		return k.HTTP
	}
	return k.clients.clientFor(resolved)
}

// fetchDirect GETs /_matrix/key/v2/server from the server itself.
func (k *Keyring) fetchDirect(ctx context.Context, serverName string) ([]byte, error) {
	target, err := k.resolver.Resolve(ctx, serverName)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.DialURL+"/_matrix/key/v2/server", nil)
	if err != nil {
		return nil, err
	}
	request.Host = target.HostHeader
	request.Header.Set("Accept", "application/json")
	response, err := k.httpFor(target).Do(request)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", serverName, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, keyDocumentMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s's keys: %w", serverName, err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s to the key request", serverName, response.Status)
	}
	return body, nil
}

// keyDocumentMaxBytes bounds what a key document may be. A key document is small; a
// large one is either a mistake or an attempt to make this server hold somebody else's
// garbage.
const keyDocumentMaxBytes = 64 << 10

// fetchViaNotary asks another homeserver what it says about a server's keys.
//
// The answer is still checked against the keys it names: a notary adds a second
// opinion, it does not replace the requirement that the document be signed by the
// server it is about.
func (k *Keyring) fetchViaNotary(ctx context.Context, notary, serverName string) ([]byte, error) {
	target, err := k.resolver.Resolve(ctx, notary)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"server_keys": map[string]any{serverName: map[string]any{}},
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.DialURL+"/_matrix/key/v2/query", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Host = target.HostHeader
	request.Header.Set("Content-Type", "application/json")
	response, err := k.httpFor(target).Do(request)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the notary %s: %w", notary, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("cannot read the notary's answer: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s to the key query", notary, response.Status)
	}
	var answer struct {
		ServerKeys []json.RawMessage `json:"server_keys"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		return nil, fmt.Errorf("the notary's answer is not readable: %w", err)
	}
	for _, raw := range answer.ServerKeys {
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			continue
		}
		if named, _ := document["server_name"].(string); named == serverName {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("%s said nothing about %s's keys", notary, serverName)
}

// record validates a key document and turns it into rows.
func (k *Keyring) record(serverName string, raw []byte) (*persist.ServerKeyDocument, []persist.ServerSigningKey, error) {
	parsed, err := k.validate(serverName, raw)
	if err != nil {
		return nil, nil, err
	}
	now := k.now()
	clamped := now.Add(maxKeyAge)
	validUntilMS := parsed.validUntilMS
	if time.UnixMilli(validUntilMS).After(clamped) {
		validUntilMS = clamped.UnixMilli()
	}
	document := &persist.ServerKeyDocument{
		ServerName:   serverName,
		Document:     append([]byte(nil), parsed.document...),
		ValidUntilTS: validUntilMS,
		FetchedAt:    now,
	}
	keys := make([]persist.ServerSigningKey, 0, len(parsed.verify)+len(parsed.old))
	for keyID, key := range parsed.verify {
		keys = append(keys, persist.ServerSigningKey{
			ServerName:   serverName,
			KeyID:        keyID,
			Key:          append([]byte(nil), key...),
			ValidUntilTS: validUntilMS,
			FetchedAt:    now,
		})
	}
	for keyID, entry := range parsed.old {
		keys = append(keys, persist.ServerSigningKey{
			ServerName:   serverName,
			KeyID:        keyID,
			Key:          append([]byte(nil), entry.key...),
			ExpiredTS:    entry.expiredMS,
			ValidUntilTS: validUntilMS,
			FetchedAt:    now,
		})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].KeyID < keys[j].KeyID })
	return document, keys, nil
}

// validate checks a key document: that it is about the server it was fetched for, that
// its shape is what the specification describes, that it is signed by a key it itself
// publishes, and that it is not already past its own expiry.
func (k *Keyring) validate(serverName string, raw []byte) (*fetchedKeys, error) {
	object, err := matrixjson.DecodeMap(raw)
	if err != nil {
		return nil, fmt.Errorf("the key document from %s is not readable JSON: %w", serverName, err)
	}
	named, _ := object["server_name"].(string)
	if named != serverName {
		return nil, fmt.Errorf("the key document is about %q, not about %s", named, serverName)
	}
	validUntil, ok := wholeNumber(object["valid_until_ts"])
	if !ok {
		return nil, fmt.Errorf("the key document from %s has no valid_until_ts", serverName)
	}
	if validUntil <= k.now().UnixMilli() {
		return nil, fmt.Errorf("the key document from %s expired at %d, which is in the past", serverName, validUntil)
	}
	parsed := &fetchedKeys{
		serverName:   serverName,
		validUntilMS: validUntil,
		verify:       map[string]ed25519.PublicKey{},
		old:          map[string]oldKey{},
		document:     raw,
	}
	verifyKeys, _ := object["verify_keys"].(map[string]any)
	if len(verifyKeys) == 0 {
		return nil, fmt.Errorf("the key document from %s publishes no verify_keys", serverName)
	}
	for keyID, value := range verifyKeys {
		key, err := verifyKeyOf(value)
		if err != nil {
			return nil, fmt.Errorf("the verify key %s from %s is unusable: %w", keyID, serverName, err)
		}
		if err := keyIDValid(keyID); err != nil {
			return nil, fmt.Errorf("the key ID %s from %s is not a key ID: %w", keyID, serverName, err)
		}
		parsed.verify[keyID] = key
	}
	oldKeys, _ := object["old_verify_keys"].(map[string]any)
	for keyID, value := range oldKeys {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("the old key %s from %s is not an object", keyID, serverName)
		}
		key, err := verifyKeyOf(entry)
		if err != nil {
			return nil, fmt.Errorf("the old key %s from %s is unusable: %w", keyID, serverName, err)
		}
		if err := keyIDValid(keyID); err != nil {
			return nil, fmt.Errorf("the key ID %s from %s is not a key ID: %w", keyID, serverName, err)
		}
		expired, ok := wholeNumber(entry["expired_ts"])
		if !ok {
			return nil, fmt.Errorf("the old key %s from %s has no expired_ts", keyID, serverName)
		}
		parsed.old[keyID] = oldKey{key: key, expiredMS: expired}
	}

	// A key document is a self-declaration: the only thing that makes it more than a
	// list of numbers is that it is signed by a key it publishes.
	signatureCheck := func(origin, keyID string) (ed25519.PublicKey, error) {
		if origin != serverName {
			return nil, fmt.Errorf("this document is checked against %s's own keys only", serverName)
		}
		if key, ok := parsed.verify[keyID]; ok {
			return key, nil
		}
		if entry, ok := parsed.old[keyID]; ok {
			return entry.key, nil
		}
		return nil, fmt.Errorf("the document does not publish %s", keyID)
	}
	result, err := matrixjson.VerifyJSON(object, signatureCheck)
	if err != nil {
		return nil, fmt.Errorf("the key document from %s cannot be checked: %w", serverName, err)
	}
	if !result.VerifiedAll([]string{serverName}) {
		return nil, fmt.Errorf("the key document from %s is not signed by a key it publishes", serverName)
	}
	return parsed, nil
}

// verifyKeyOf reads the "key" field of a verify key object.
func verifyKeyOf(value any) (ed25519.PublicKey, error) {
	entry, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("it is not an object")
	}
	encoded, ok := entry["key"].(string)
	if !ok || encoded == "" {
		return nil, errors.New("it has no key field")
	}
	raw, err := matrixjson.DecodeUnpadded(encoded)
	if err != nil {
		return nil, fmt.Errorf("its key is not unpadded base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("its key is %d bytes, not %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// keyIDValid checks the algorithm prefix and the version's characters, which the
// specification restricts to [a-zA-Z0-9_].
func keyIDValid(keyID string) error {
	algorithm, version, found := strings.Cut(keyID, ":")
	if !found {
		return errors.New("it has no algorithm prefix")
	}
	if algorithm != matrixjson.Algorithm {
		return fmt.Errorf("its algorithm %q is not %s", algorithm, matrixjson.Algorithm)
	}
	if version == "" {
		return errors.New("it has no key version")
	}
	for i := 0; i < len(version); i++ {
		c := version[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return fmt.Errorf("its version contains %q, which is not allowed", c)
	}
	return nil
}

// wholeNumber reads a JSON number that must be an integer. matrixjson.Decode keeps
// numbers as json.Number so that a large timestamp does not pass through a float.
func wholeNumber(value any) (int64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case int64:
		return number, true
	case float64:
		if number != float64(int64(number)) {
			return 0, false
		}
		return int64(number), true
	default:
		return 0, false
	}
}

func (k *Keyring) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}
