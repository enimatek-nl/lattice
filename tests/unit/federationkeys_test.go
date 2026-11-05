package unit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/federation"
	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/roomversion"
)

// A homeserver's claim to have sent an event is worth exactly as much as the key it was
// signed with, and the key arrives over the network from a server that may be lying,
// out of date, or somebody else entirely. These tests check what this server is willing
// to believe about a stranger's signing keys.

// keyPublisher is one server's published key document and the handler that serves it.
type keyPublisher struct {
	serverName string
	keyID      string
	public     ed25519.PublicKey
	private    ed25519.PrivateKey
	document   []byte
	hits       int32
}

// publishedKeysDocument builds a signed key document. validUntil is what the document
// claims, not what this server will accept: the clamping is under test elsewhere.
func newKeyPublisher(t *testing.T, serverName, keyID string, validUntil time.Time, old map[string]any) *keyPublisher {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	if old == nil {
		old = map[string]any{}
	}
	document := map[string]any{
		"server_name":     serverName,
		"verify_keys":     map[string]any{keyID: map[string]any{"key": matrixjson.EncodeUnpadded(public)}},
		"old_verify_keys": old,
		"valid_until_ts":  validUntil.UnixMilli(),
	}
	if err := matrixjson.SignJSONInPlace(serverName, keyID, private, document); err != nil {
		t.Fatalf("signing the key document: %v", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encoding the key document: %v", err)
	}
	return &keyPublisher{serverName: serverName, keyID: keyID, public: public, private: private, document: encoded}
}

// serve answers GET /_matrix/key/v2/server with the document.
func (f *keyPublisher) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.hits, 1)
		if !strings.Contains(r.URL.Path, "/_matrix/key/v2/server") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(f.document)
	}))
	t.Cleanup(server.Close)
	return server
}

// keyringOn wires a keyring to a repository and a resolver that points at the given
// URLs.
func keyringOn(t *testing.T, targets map[string]string, notaries []string, now func() time.Time) (*federation.Keyring, *persist.Database) {
	t.Helper()
	db, err := persist.Open(filepath.Join(t.TempDir(), "keys.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	resolver := &federation.StaticResolver{Targets: map[string]federation.Target{}}
	for name, url := range targets {
		resolver.Targets[name] = federation.Target{ServerName: name, DialURL: url, HostHeader: name}
	}
	keyring := federation.NewKeyring(db.Federation, resolver, &http.Client{Timeout: 5 * time.Second},
		"ours.example", notaries, slog.New(slog.DiscardHandler))
	if now != nil {
		keyring.Now = now
	}
	return keyring, db
}

func TestFederationKeyringFetchesAndCaches(t *testing.T) {
	t.Parallel()
	fixture := newKeyPublisher(t, "keys.example", "ed25519:one", time.Now().Add(24*time.Hour), nil)
	server := fixture.serve(t)
	keyring, _ := keyringOn(t, map[string]string{"keys.example": server.URL}, nil, time.Now)

	key, err := keyring.Lookup(context.Background(), "keys.example", "ed25519:one")
	if err != nil {
		t.Fatalf("looking up the published key: %v", err)
	}
	if !bytes.Equal(key, fixture.public) {
		t.Errorf("the key the keyring handed back is not the key the server published")
	}
	if _, err := keyring.Lookup(context.Background(), "keys.example", "ed25519:one"); err != nil {
		t.Fatalf("the second lookup failed: %v", err)
	}
	if hits := atomic.LoadInt32(&fixture.hits); hits != 1 {
		t.Errorf("the key document was fetched %d times; the second lookup should have been served from the cache", hits)
	}

	// A key the server never published is not a key, and the answer says so.
	if _, err := keyring.Lookup(context.Background(), "keys.example", "ed25519:two"); err == nil {
		t.Errorf("a key that was never published resolved anyway")
	}
	if _, err := keyring.Lookup(context.Background(), "nobody.example", "ed25519:one"); err == nil {
		t.Errorf("a key for a server this server cannot reach resolved anyway")
	}
}

func TestFederationKeyringRefusesDocumentsItCannotTrust(t *testing.T) {
	t.Parallel()

	// A document that claims to be about a server, signed by somebody else.
	forged := newKeyPublisher(t, "liar.example", "ed25519:one", time.Now().Add(24*time.Hour), nil)
	other := newKeyPublisher(t, "liar.example", "ed25519:one", time.Now().Add(24*time.Hour), nil)
	tampered := map[string]any{}
	if err := json.Unmarshal(forged.document, &tampered); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	tampered["verify_keys"] = map[string]any{
		"ed25519:one": map[string]any{"key": matrixjson.EncodeUnpadded(other.public)},
	}
	tamperedBody, err := json.Marshal(tampered)
	if err != nil {
		t.Fatalf("encoding the tampered document: %v", err)
	}
	signed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tamperedBody)
	}))
	defer signed.Close()
	keyring, db := keyringOn(t, map[string]string{"liar.example": signed.URL}, nil, time.Now)
	if _, err := keyring.Lookup(context.Background(), "liar.example", "ed25519:one"); err == nil {
		t.Errorf("a key document whose signature does not match the key it publishes was trusted")
	}
	rows, err := db.Federation.ServerKeys(context.Background(), "liar.example")
	if err != nil {
		t.Fatalf("reading what was recorded: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d keys from an untrusted document were recorded", len(rows))
	}

	// A document that is about a different server than the one asked for.
	elsewhere := newKeyPublisher(t, "someone-else.example", "ed25519:one", time.Now().Add(24*time.Hour), nil)
	mislabelled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(elsewhere.document)
	}))
	defer mislabelled.Close()
	mixed, _ := keyringOn(t, map[string]string{"asked.example": mislabelled.URL}, nil, time.Now)
	if _, err := mixed.Lookup(context.Background(), "asked.example", "ed25519:one"); err == nil {
		t.Errorf("a key document about another server was accepted")
	}

	// A document that has already expired.
	expired := newKeyPublisher(t, "stale.example", "ed25519:one", time.Now().Add(-time.Hour), nil)
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(expired.document)
	}))
	defer stale.Close()
	staleKeyring, _ := keyringOn(t, map[string]string{"stale.example": stale.URL}, nil, time.Now)
	if _, err := staleKeyring.Lookup(context.Background(), "stale.example", "ed25519:one"); err == nil {
		t.Errorf("an expired key document was trusted")
	}
}

func TestFederationKeyringClampsHowLongAKeyIsTrusted(t *testing.T) {
	t.Parallel()
	// A server may publish a key that it says is good for a year. This server decides
	// how long it will honour that claim, because a key that stays trusted for a year
	// is a key whose owner cannot take back.
	fixture := newKeyPublisher(t, "years.example", "ed25519:one", time.Now().Add(365*24*time.Hour), nil)
	server := fixture.serve(t)
	now := time.Now()
	clock := func() time.Time { return now }
	keyring, db := keyringOn(t, map[string]string{"years.example": server.URL}, nil, clock)
	if _, err := keyring.Lookup(context.Background(), "years.example", "ed25519:one"); err != nil {
		t.Fatalf("looking up the key: %v", err)
	}
	document, err := db.Federation.KeysDocument(context.Background(), "years.example")
	if err != nil {
		t.Fatalf("reading the recorded document: %v", err)
	}
	if document.ValidUntilTS > now.Add(7*24*time.Hour).UnixMilli() {
		t.Errorf("the key is trusted until %d, which is more than a week from %d", document.ValidUntilTS, now.UnixMilli())
	}
}

func TestFederationKeyringExpiryFollowsTheRoomVersion(t *testing.T) {
	t.Parallel()
	// Room versions 1 to 4 ignore the expiry of a key. Refusing an old room's events
	// for that reason would be this server inventing a rule that the room never had.
	fixture := newKeyPublisher(t, "oldroom.example", "ed25519:one", time.Now().Add(time.Hour), nil)
	server := fixture.serve(t)
	now := time.Now()
	clock := func() time.Time { return now }
	keyring, _ := keyringOn(t, map[string]string{"oldroom.example": server.URL}, nil, clock)
	if _, err := keyring.Lookup(context.Background(), "oldroom.example", "ed25519:one"); err != nil {
		t.Fatalf("looking up the key while it is valid: %v", err)
	}

	// Two days later the cached key has passed its own expiry.
	now = now.Add(48 * time.Hour)
	lookup := keyring.LookupForRoom(context.Background(), roomversion.Rules{ID: "1"})
	if _, err := lookup("oldroom.example", "ed25519:one"); err != nil {
		t.Errorf("a room version that ignores key expiry refused an expired key: %v", err)
	}
	if _, err := keyring.Lookup(context.Background(), "oldroom.example", "ed25519:one"); err == nil {
		t.Errorf("an expired key was accepted where the room version does enforce expiry")
	}
}

func TestFederationKeyringUsesRetiredKeysUntilTheyExpire(t *testing.T) {
	t.Parallel()
	// A rotated key stays usable for events signed before the rotation, until the moment
	// its owner says it stopped being usable.
	retiredPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating the retired key: %v", err)
	}
	future := newKeyPublisher(t, "rotated.example", "ed25519:current", time.Now().Add(24*time.Hour), map[string]any{
		"ed25519:previous": map[string]any{
			"key":        matrixjson.EncodeUnpadded(retiredPublic),
			"expired_ts": time.Now().Add(24 * time.Hour).UnixMilli(),
		},
	})
	server := future.serve(t)
	keyring, _ := keyringOn(t, map[string]string{"rotated.example": server.URL}, nil, time.Now)
	if _, err := keyring.Lookup(context.Background(), "rotated.example", "ed25519:previous"); err != nil {
		t.Errorf("a retired key that has not expired yet was refused: %v", err)
	}
	if _, err := keyring.Lookup(context.Background(), "rotated.example", "ed25519:current"); err != nil {
		t.Errorf("the current key was refused: %v", err)
	}
}

func TestFederationKeyringAsksANotaryWhenTheServerDoesNotAnswer(t *testing.T) {
	t.Parallel()
	// The server whose keys are wanted is down. Another homeserver that has met it can
	// say what it published — and the answer is still checked against that server's own
	// signature, because a notary adds a second opinion rather than replacing the check.
	fixture := newKeyPublisher(t, "quiet.example", "ed25519:one", time.Now().Add(24*time.Hour), nil)
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving an address: %v", err)
	}
	address := dead.Addr().String()
	_ = dead.Close()

	notary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/_matrix/key/v2/query") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "quiet.example") {
			t.Errorf("the notary was asked about the wrong server: %s", body)
		}
		_, _ = fmt.Fprintf(w, `{"server_keys":[%s]}`, string(fixture.document))
	}))
	defer notary.Close()

	keyring, _ := keyringOn(t, map[string]string{
		"quiet.example":  "http://" + address,
		"notary.example": notary.URL,
	}, []string{"notary.example"}, time.Now)
	key, err := keyring.Lookup(context.Background(), "quiet.example", "ed25519:one")
	if err != nil {
		t.Fatalf("the notary's copy of the keys was not used: %v", err)
	}
	if !bytes.Equal(key, fixture.public) {
		t.Errorf("the key obtained through the notary is not the published key")
	}
}
