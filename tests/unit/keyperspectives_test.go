package unit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A room keeps the events of homeservers that stopped existing: a renamed server
// leaves its signatures behind in every room it spoke in, and its keys can then only
// come from someone who saw them while they were current. The tests below hold the two
// halves of that. Without a key server, such a room is shut forever; with an
// unbounded one, a single dead name turns a join into minutes of timeouts and lets
// whoever answers for a dead name say anything about it.

// keyDocumentOf publishes a server's verify key the way a server publishes its own: a
// document naming itself, signed by the key it names.
func keyDocumentOf(t *testing.T, serverName string, key federation.KeyPair, validUntilMS int64) matrix.Object {
	t.Helper()
	document := matrix.Object{
		"server_name":    serverName,
		"valid_until_ts": validUntilMS,
		"verify_keys":    matrix.Object{key.ID: matrix.Object{"key": key.PublicKey()}},
	}
	signature, err := key.Sign(document)
	if err != nil {
		t.Fatalf("signing the key document of %s: %v", serverName, err)
	}
	document["signatures"] = matrix.Object{serverName: matrix.Object{key.ID: signature}}
	return document
}

// signedByKey replaces a document's self-signature with one made by another key, filed
// under the key the document publishes: it names a server without that server having
// said anything.
func signedByKey(t *testing.T, document matrix.Object, key federation.KeyPair, keyID string) matrix.Object {
	t.Helper()
	unsigned := without(document, "signatures")
	signature, err := key.Sign(unsigned)
	if err != nil {
		t.Fatalf("signing a key document with another key: %v", err)
	}
	serverName, _ := unsigned["server_name"].(string)
	return withField(unsigned, "signatures", matrix.Object{serverName: matrix.Object{keyID: signature}})
}

// writeJSON answers a request with an object, which is how every federation answer
// this is about arrives.
func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("writing the answer: %v", err)
	}
}

// keyServer is a server asked for somebody else's keys. It answers a key query with
// the documents it was told to hold, and counts the queries it answered.
type keyServer struct {
	// documents maps a server name to the document handed over for it.
	documents map[string]matrix.Object
	// failures names the servers answered about without a document, the way a real
	// key server reports the ones it has never heard of.
	failures map[string]string
	queries  atomic.Int64
}

func (k *keyServer) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_matrix/key/v2/query" {
			http.NotFound(w, r)
			return
		}
		k.queries.Add(1)
		var request struct {
			ServerKeys map[string]any `json:"server_keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("the key query is not readable: %v", err)
		}
		answer := matrix.Object{"server_keys": []any{}}
		failures := matrix.Object{}
		list := []any{}
		for name := range request.ServerKeys {
			if document, found := k.documents[name]; found {
				list = append(list, document)
				continue
			}
			if reason, found := k.failures[name]; found {
				failures[name] = reason
			}
		}
		answer["server_keys"] = list
		if len(failures) > 0 {
			answer["failures"] = failures
		}
		writeJSON(t, w, answer)
	})
}

// TestAKeyServerAnswersForAServerThatCannot is the whole reason a key server exists: a
// room whose state carries an event from a server that no longer publishes keys, which
// nothing else can open.
func TestAKeyServerAnswersForAServerThatCannot(t *testing.T) {
	dead, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("generating the key of the server that cannot answer: %v", err)
	}
	nowMS := time.Now().UnixMilli()
	notary := &keyServer{documents: map[string]matrix.Object{
		"dead.example": keyDocumentOf(t, "dead.example", dead, nowMS+60_000),
	}}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "this server is gone", http.StatusInternalServerError)
	}))
	defer peer.Close()
	server := httptest.NewServer(notary.handler(t))
	defer server.Close()

	transport := federation.NewTransport(fixedIdentity{name: "origin.example", key: identityKey(t)}, federation.Config{
		PeerBaseURLs:    map[string]string{"dead.example": peer.URL, "notary.example": server.URL},
		KeyPerspectives: []string{"notary.example"},
	})
	publicKey, err := transport.VerifyKeyOfAsOf(context.Background(), "dead.example", dead.ID, nowMS)
	if err != nil {
		t.Fatalf("the key server's answer was not enough: %v", err)
	}
	if !publicKey.Equal(keyPairPublic(t, dead)) {
		t.Error("the key that came back is not the one the dead server signs with")
	}
	if got := notary.queries.Load(); got != 1 {
		t.Errorf("the key server answered %d queries, want 1", got)
	}
}

// TestAServerThatDoesNotAnswerIsWaitedForOnce is what a join spends on a peer that is
// simply gone. A room's state is verified event by event, and every event of a dead
// server would otherwise pay for the same impossible fetch again: that is the wait a
// client feels as "Joining…" for minutes.
func TestAServerThatDoesNotAnswerIsWaitedForOnce(t *testing.T) {
	var asked atomic.Int64
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		http.Error(w, "no keys here", http.StatusInternalServerError)
	}))
	defer peer.Close()

	transport := federation.NewTransport(fixedIdentity{name: "origin.example", key: identityKey(t)}, federation.Config{
		PeerBaseURLs: map[string]string{"dead.example": peer.URL},
	})
	var lastErr error
	for i := 0; i < 20; i++ {
		_, lastErr = transport.VerifyKeyOf(context.Background(), "dead.example", "ed25519:whatever")
	}
	if lastErr == nil {
		t.Fatal("a server that answers nothing produced no error")
	}
	if got := asked.Load(); got != 1 {
		t.Fatalf("the dead server was asked %d times for twenty lookups, want 1", got)
	}
	if !strings.Contains(lastErr.Error(), "nothing is asked of that server again") {
		t.Errorf("the error does not say the peer is held back: %v", lastErr)
	}
}

// TestAStalledKeyFetchIsNotWaitedFor pins that a key document is a small thing with a
// small budget. The general transport timeout is for transactions carrying a room's
// worth of events, and a server that neither answers nor refuses used to borrow it for
// every key it once signed.
func TestAStalledKeyFetchIsNotWaitedFor(t *testing.T) {
	release := make(chan struct{})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The answer arrives late, which is what a server that is going away looks like
		// from here.
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		http.Error(w, "late", http.StatusInternalServerError)
	}))
	defer peer.Close()
	defer close(release)

	transport := federation.NewTransport(fixedIdentity{name: "origin.example", key: identityKey(t)}, federation.Config{
		PeerBaseURLs:    map[string]string{"silent.example": peer.URL},
		KeyFetchTimeout: 50 * time.Millisecond,
	})
	started := time.Now()
	if _, err := transport.VerifyKeyOf(context.Background(), "silent.example", "ed25519:whatever"); err == nil {
		t.Fatal("a fetch that never finished produced no error")
	}
	if waited := time.Since(started); waited > time.Second {
		t.Errorf("the fetch was waited for %s, long past the budget of the attempt", waited)
	}
}

// TestConcurrentKeyLookupsFetchOnce pins the single flight. Verifying a room's state
// means verifying a hundred events at once, all of them signed by the same handful of
// servers.
func TestConcurrentKeyLookupsFetchOnce(t *testing.T) {
	var asked atomic.Int64
	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	nowMS := time.Now().UnixMilli()
	release := make(chan struct{})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeJSON(t, w, keyDocumentOf(t, "peer.example", key, nowMS+60_000))
	}))
	defer peer.Close()

	transport := federation.NewTransport(fixedIdentity{name: "origin.example", key: identityKey(t)}, federation.Config{
		PeerBaseURLs: map[string]string{"peer.example": peer.URL},
	})
	results := make([]error, 20)
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, results[index] = transport.VerifyKeyOf(context.Background(), "peer.example", key.ID)
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	group.Wait()

	if got := asked.Load(); got != 1 {
		t.Errorf("the peer was asked %d times for twenty lookups, want 1", got)
	}
	for i, err := range results {
		if err != nil {
			t.Errorf("lookup %d failed: %v", i, err)
		}
	}
}

// TestAKeyServerCannotVouchForWhateverItLikes is the discipline that keeps the
// fallback from being a skeleton key: a document has to name the server asked about
// and be signed by the keys it publishes, or it proves nothing.
func TestAKeyServerCannotVouchForWhateverItLikes(t *testing.T) {
	nowMS := time.Now().UnixMilli()
	someoneElse, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	published, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	cases := []struct {
		name     string
		document matrix.Object
		want     string
	}{
		{
			name:     "a document for another server",
			document: keyDocumentOf(t, "someone.else", someoneElse, nowMS+60_000),
			want:     "holds no keys",
		},
		{
			// The document names the asked server and publishes a key, but the signature
			// was made by a key it does not publish: nothing in it came from the server it
			// claims to speak for.
			name:     "a document whose signature is not its published key's",
			document: signedByKey(t, keyDocumentOf(t, "dead.example", published, nowMS+60_000), someoneElse, published.ID),
			want:     "does not hold",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notary := &keyServer{documents: map[string]matrix.Object{"dead.example": tc.document}}
			server := httptest.NewServer(notary.handler(t))
			defer server.Close()
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "gone", http.StatusInternalServerError)
			}))
			defer peer.Close()

			transport := federation.NewTransport(fixedIdentity{name: "origin.example", key: identityKey(t)}, federation.Config{
				PeerBaseURLs:    map[string]string{"dead.example": peer.URL, "notary.example": server.URL},
				KeyPerspectives: []string{"notary.example"},
			})
			if _, err := transport.VerifyKeyOf(context.Background(), "dead.example", "ed25519:whatever"); err == nil {
				t.Fatal("the answer was believed")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error does not name the problem (%s): %v", tc.want, err)
			}
		})
	}
}

// TestAnExpiredKeyVerifiesWhatItSignedWhileItLived is the bound that comes with the
// fallback. A key server hands over what it saw, and documents expire: believing one
// about the events written before its own expiry is what makes a dead server's room
// joinable, while believing it about anything newer is believing whoever answers for
// that name today.
func TestAnExpiredKeyVerifiesWhatItSignedWhileItLived(t *testing.T) {
	dead, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	expiredMS := time.Now().Add(-24 * time.Hour).UnixMilli()
	notary := &keyServer{documents: map[string]matrix.Object{
		"dead.example": keyDocumentOf(t, "dead.example", dead, expiredMS),
	}}
	server := httptest.NewServer(notary.handler(t))
	defer server.Close()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusInternalServerError)
	}))
	defer peer.Close()

	transport := federation.NewTransport(fixedIdentity{name: "origin.example", key: identityKey(t)}, federation.Config{
		PeerBaseURLs:    map[string]string{"dead.example": peer.URL, "notary.example": server.URL},
		KeyPerspectives: []string{"notary.example"},
	})

	if _, err := transport.VerifyKeyOfAsOf(context.Background(), "dead.example", dead.ID, expiredMS-1000); err != nil {
		t.Errorf("an event written while the document lived was refused: %v", err)
	}
	_, err = transport.VerifyKeyOfAsOf(context.Background(), "dead.example", dead.ID, expiredMS+1000)
	if err == nil {
		t.Fatal("an expired key was accepted for an event written after it expired")
	}
	if !strings.Contains(err.Error(), "expired document") {
		t.Errorf("the error does not say the document expired: %v", err)
	}
	// Something happening now is never signed by a key whose document has expired,
	// whatever that key signed in the past.
	if _, err := transport.VerifyKeyOf(context.Background(), "dead.example", dead.ID); err == nil {
		t.Error("an expired key was accepted for a live request")
	}
}

// TestAKeyServerIsAskedOnlyAfterThePeerItself is what keeps the fallback from making
// every verification a network argument with a third party: a peer that publishes its
// own keys is believed the direct way, and never spends a query on anyone else.
func TestAKeyServerIsAskedOnlyAfterThePeerItself(t *testing.T) {
	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	nowMS := time.Now().UnixMilli()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, keyDocumentOf(t, "peer.example", key, nowMS+60_000))
	}))
	defer peer.Close()
	notary := &keyServer{documents: map[string]matrix.Object{}}
	server := httptest.NewServer(notary.handler(t))
	defer server.Close()

	transport := federation.NewTransport(fixedIdentity{name: "origin.example", key: identityKey(t)}, federation.Config{
		PeerBaseURLs:    map[string]string{"peer.example": peer.URL, "notary.example": server.URL},
		KeyPerspectives: []string{"notary.example"},
	})
	publicKey, err := transport.VerifyKeyOf(context.Background(), "peer.example", key.ID)
	if err != nil {
		t.Fatalf("the peer's own answer was not enough: %v", err)
	}
	if !publicKey.Equal(keyPairPublic(t, key)) {
		t.Error("the key that came back is not the peer's")
	}
	if got := notary.queries.Load(); got != 0 {
		t.Errorf("the key server answered %d queries although the peer answered, want 0", got)
	}
}

// TestAKeyServerNamesItsFailure is the polite half of the protocol: a key server that
// has never heard of a server says so, and that reason is what reaches the operator
// instead of a bare "no keys".
func TestAKeyServerNamesItsFailure(t *testing.T) {
	notary := &keyServer{
		documents: map[string]matrix.Object{},
		failures:  map[string]string{"never.heard.of.me": "NOT_FOUND"},
	}
	server := httptest.NewServer(notary.handler(t))
	defer server.Close()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer peer.Close()

	transport := federation.NewTransport(fixedIdentity{name: "origin.example", key: identityKey(t)}, federation.Config{
		PeerBaseURLs:    map[string]string{"never.heard.of.me": peer.URL, "notary.example": server.URL},
		KeyPerspectives: []string{"notary.example"},
	})
	_, err := transport.VerifyKeyOf(context.Background(), "never.heard.of.me", "ed25519:whatever")
	if err == nil {
		t.Fatal("nothing was known about the server and nothing was reported")
	}
	if !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Errorf("the key server's reason did not reach the error: %v", err)
	}
	if !strings.Contains(err.Error(), "the server itself said") {
		t.Errorf("the error dropped what the peer answered first: %v", err)
	}
}

// identityKey is the signing identity of a transport, which every test above needs and
// none of them is about.
func identityKey(t *testing.T) federation.KeyPair {
	t.Helper()
	key, err := federation.NewKeyPair()
	if err != nil {
		t.Fatalf("generating an identity key: %v", err)
	}
	return key
}
