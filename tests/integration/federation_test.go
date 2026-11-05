package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/app"
	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/federation"
	"github.com/enimatek/lattice/internal/logging"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Federation tests need two homeservers. One server cannot show what federation is
// for: whether a claim from a stranger is believed, and on what evidence.
//
// The two nodes run in this process, on real listeners, with real TLS certificates
// issued by a test certificate authority and real X-Matrix signatures. What a test
// supplies instead of the internet is the addressing: a table that says where a server
// name lives, and a dialer that resolves the name to a loopback address. Signing, key
// discovery, key checking and event handling are the production code paths.

// fedNode is one homeserver in a two-server test.
type fedNode struct {
	t        *testing.T
	name     string
	app      *app.App
	client   string
	federate string
	// dial is shared between the nodes: it is how one finds the other.
	dial *testDial
	http *http.Client
}

// testDial maps a server name to the URL its federation listener answers on, and lets
// the dialer resolve the name to a loopback address. A deployment does DNS; a test on
// one machine has a table.
type testDial struct {
	mu     sync.Mutex
	target map[string]string
}

func (d *testDial) register(serverName, url string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.target[serverName] = url
}

// Resolve implements [federation.Resolver]. The DialURL carries the *name*, not the
// address, because that is the name the TLS certificate is verified against — which is
// the whole point of federation's trust model.
func (d *testDial) Resolve(ctx context.Context, serverName string) (federation.Target, error) {
	d.mu.Lock()
	url, ok := d.target[serverName]
	d.mu.Unlock()
	if !ok {
		return federation.Target{}, fmt.Errorf("%s has no address in this test", serverName)
	}
	return federation.Target{ServerName: serverName, DialURL: url, HostHeader: serverName}, nil
}

// dialLoopbackByName resolves "hs1:port" to 127.0.0.1:port, so the URL can name the
// server the certificate is for while the connection goes to the local listener.
func dialLoopbackByName(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if len(host) < 2 || host[:2] != "hs" {
		// Already an address: the client API listeners and the local test server dial
		// directly. Only a federation target carries the server's *name*, which is what
		// its TLS certificate is verified against.
		var direct net.Dialer
		return direct.DialContext(ctx, network, address)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
}

// testCA issues the two servers' certificates. Real TLS verification is part of what
// federation is: a key document that arrives over an unverified connection is a
// stranger's assertion about who they are.
type testCA struct {
	certPath string
	keyPath  string
	pool     *x509.CertPool
}

func newTestCA(t *testing.T, dir string) *testCA {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the CA key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		t.Fatalf("drawing a serial: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "lattice federation test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating the CA certificate: %v", err)
	}
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing the CA certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		t.Fatalf("encoding the CA key: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("writing the CA key: %v", err)
	}
	pool := x509.NewCertPool()
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the CA certificate: %v", err)
	}
	pool.AddCert(certificate)
	return &testCA{certPath: certPath, keyPath: keyPath, pool: pool}
}

// newFederationNode starts one homeserver that reaches the other through dial.
func newFederationNode(t *testing.T, name string, ca *testCA, dial *testDial) *fedNode {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.ServerName = name
	cfg.DataDir = dir
	cfg.Store.Path = filepath.Join(dir, "lattice.db")
	cfg.TLS.CertFile = filepath.Join(dir, "tls", name+".crt")
	cfg.TLS.KeyFile = filepath.Join(dir, "tls", name+".key")
	cfg.TLS.CACertFile = ca.certPath
	cfg.TLS.CAKeyFile = ca.keyPath
	cfg.HTTP.ClientAddr = "127.0.0.1:0"
	cfg.HTTP.FederationAddr = "127.0.0.1:0"
	cfg.HTTP.RequestTimeout = config.Duration(10 * time.Second)
	cfg.Registration.Open = true
	cfg.Registration.SharedSecret = "complement"
	cfg.Registration.BcryptCost = 4
	cfg.Log = config.LogConfig{Level: "warn", Format: "json"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid configuration for %s: %v", name, err)
	}

	transport := &http.Transport{
		DialContext:     dialLoopbackByName,
		TLSClientConfig: &tls.Config{RootCAs: ca.pool},
	}
	ctx, cancel := context.WithCancel(context.Background())
	instance, err := app.NewWithOptions(ctx, cfg, logging.NewFor(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), app.Options{
		FederationResolver:  dial,
		FederationTransport: transport,
	})
	if err != nil {
		cancel()
		t.Fatalf("building %s: %v", name, err)
	}
	if err := instance.Prepare(); err != nil {
		cancel()
		_ = instance.Close()
		t.Fatalf("preparing %s: %v", name, err)
	}
	served := make(chan error, 1)
	go func() { served <- instance.Serve(ctx) }()

	node := &fedNode{
		t:        t,
		name:     name,
		app:      instance,
		client:   "http://" + instance.ClientAddr(),
		federate: "https://" + instance.FederationAddr(),
		dial:     dial,
		http:     &http.Client{Transport: transport, Timeout: 10 * time.Second},
	}
	// The name is reachable once the listener is known, which is why registration
	// happens here rather than at construction.
	dial.register(name, node.federate)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil && err.Error() != "context canceled" {
				t.Errorf("%s stopped with an error: %v", name, err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("%s did not stop within 10s", name)
		}
		_ = instance.Close()
	})
	return node
}

// federationPair returns two homeservers that can reach each other.
func federationPair(t *testing.T) (*fedNode, *fedNode) {
	t.Helper()
	dir := t.TempDir()
	ca := newTestCA(t, dir)
	dial := &testDial{target: map[string]string{}}
	first := newFederationNode(t, "hs1", ca, dial)
	second := newFederationNode(t, "hs2", ca, dial)
	return first, second
}

// signedRequest sends an authenticated federation request from one node to the other,
// signed by the sending node's own current signing key.
func (n *fedNode) signedRequest(t *testing.T, to *fedNode, method, target string, body []byte) (int, []byte) {
	t.Helper()
	header, err := federation.AuthorizationHeader(n.name, n.app.Keys, to.name, method, target, body)
	if err != nil {
		t.Fatalf("signing the request from %s: %v", n.name, err)
	}
	return n.rawRequest(t, to, method, target, header, body)
}

// rawRequest sends a federation request with a caller-supplied Authorization header.
func (n *fedNode) rawRequest(t *testing.T, to *fedNode, method, target, header string, body []byte) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, to.federate+target, reader)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if header != "" {
		request.Header.Set("Authorization", header)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := n.http.Do(request)
	if err != nil {
		t.Fatalf("the request from %s to %s failed: %v", n.name, to.name, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	return response.StatusCode, payload
}

// clientDo performs a Client-Server API call against this node.
func (n *fedNode) clientDo(t *testing.T, method, path string, body any, token string) (int, map[string]any) {
	t.Helper()
	status, payload := n.doWithStatus(t, method, path, body, token)
	var decoded map[string]any
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatalf("%s answered something that is not a JSON object: %s", path, payload)
		}
	}
	return status, decoded
}

// clientDoList is the same request for the endpoints whose body is a JSON array, which
// GET /rooms/{roomID}/state is: its own schema says "Array of ClientEvent", so a helper
// that can only read objects cannot read it.
func (n *fedNode) clientDoList(t *testing.T, path, token string) []map[string]any {
	t.Helper()
	_, payload := n.doWithStatus(t, http.MethodGet, path, nil, token)
	var events []map[string]any
	if err := json.Unmarshal(payload, &events); err != nil {
		t.Fatalf("%s answered something that is not a JSON array: %s", path, payload)
	}
	return events
}

// doWithStatus performs a client-API request and reports the status and the body.
func (n *fedNode) doWithStatus(t *testing.T, method, path string, body any, token string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding the request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, n.client+path, reader)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := n.http.Do(request)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the answer from %s: %v", path, err)
	}
	return response.StatusCode, payload
}

// registerUser gives a local user an access token.
func (n *fedNode) registerUser(t *testing.T, localpart, password string) string {
	t.Helper()
	status, challenge := n.clientDo(t, http.MethodPost, "/_matrix/client/v3/register", map[string]any{
		"username": localpart,
		"password": password,
	}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("registering %s on %s: registration did not ask to be authenticated: %d %v", localpart, n.name, status, challenge)
	}
	session, _ := challenge["session"].(string)
	auth := map[string]any{"type": "m.login.dummy"}
	if session != "" {
		auth["session"] = session
	}
	status, body := n.clientDo(t, http.MethodPost, "/_matrix/client/v3/register", map[string]any{
		"username": localpart,
		"password": password,
		"auth":     auth,
	}, "")
	if status != http.StatusOK {
		t.Fatalf("registering %s on %s: %v", localpart, n.name, body)
	}
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatalf("registration on %s returned no token: %v", n.name, body)
	}
	return token
}

// roomEvents is the state and timeline this server holds for a room, as the events
// actually stored — which is what a PDU has to be for another server to accept it.
type roomEvents struct {
	State    []map[string]any
	Messages []map[string]any
}

func (n *fedNode) roomSnapshot(t *testing.T, roomID, token string) roomEvents {
	t.Helper()
	list := n.clientDoList(t, "/_matrix/client/v3/rooms/"+roomID+"/state", token)
	status, page := n.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, token)
	if status != http.StatusOK {
		t.Fatalf("reading the messages of %s: %v", roomID, page)
	}
	events := roomEvents{}
	events.State = append(events.State, list...)
	if timeline, ok := page["chunk"].([]any); ok {
		for _, entry := range timeline {
			if event, ok := entry.(map[string]any); ok {
				events.Messages = append(events.Messages, event)
			}
		}
	}
	return events
}

// sendTransaction hands a set of events to another server as one transaction.
func (n *fedNode) sendTransaction(t *testing.T, to *fedNode, txnID string, pdus []map[string]any) (int, transactionAnswer) {
	t.Helper()
	raw := make([]json.RawMessage, 0, len(pdus))
	for _, event := range pdus {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encoding the PDU: %v", err)
		}
		raw = append(raw, encoded)
	}
	body, err := json.Marshal(map[string]any{
		"origin":           n.name,
		"origin_server_ts": time.Now().UnixMilli(),
		"pdus":             raw,
	})
	if err != nil {
		t.Fatalf("encoding the transaction: %v", err)
	}
	status, payload := n.signedRequest(t, to, http.MethodPut, "/_matrix/federation/v1/send/"+txnID, body)
	var answer transactionAnswer
	if err := json.Unmarshal(payload, &answer); err != nil {
		t.Fatalf("%s answered a transaction with something unreadable: %s", to.name, payload)
	}
	return status, answer
}

// transactionAnswer is what a destination says about a transaction.
type transactionAnswer struct {
	PDUs map[string]struct {
		Error string `json:"error"`
	} `json:"pdus"`
}

// TestFederationHandover moves a room's beginning from one homeserver to the other and
// reads it back: the accepting server must end up with the same events, byte for byte,
// and must be able to answer a request for them.
func TestFederationHandover(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	token := hs1.registerUser(t, "fedalice", "pw-fedalice")
	status, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "name": "across the wire"}, token)
	if status != http.StatusOK {
		t.Fatalf("creating the room: %v", created)
	}
	roomID, _ := created["room_id"].(string)
	if roomID == "" {
		t.Fatalf("no room id: %v", created)
	}
	status, sent := hs1.clientDo(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/txn1",
		map[string]any{"msgtype": "m.text", "body": "hello from hs1"}, token)
	if status != http.StatusOK {
		t.Fatalf("sending the message: %v", sent)
	}
	messageID, _ := sent["event_id"].(string)

	snapshot := hs1.roomSnapshot(t, roomID, token)
	// The create event has to arrive before anything that cites it, and an event whose
	// predecessors are unknown is refused — so the handover is in causal order. A server
	// that was told about the missing events first asks for them later; fetching them on
	// demand is task T-4007 and is recorded as outstanding, not quietly skipped.
	handover := byDepth(dedupe(snapshot.State, snapshot.Messages))
	message := eventWithID(snapshot.Messages, messageID)
	status, answer := hs1.sendTransaction(t, hs2, "handover-1", handover)
	if status != http.StatusOK {
		t.Fatalf("the transaction was refused as a whole: %d %v", status, answer)
	}
	for eventID, result := range answer.PDUs {
		if result.Error != "" {
			t.Errorf("%s refused %s: %s", hs2.name, eventID, result.Error)
		}
	}
	if len(answer.PDUs) != len(handover) {
		t.Errorf("%s answered for %d of the %d events it was handed", hs2.name, len(answer.PDUs), len(handover))
	}

	// Reading the event back from the server that received it proves more than an
	// acceptance: the event has to be stored, findable by the ID its own content hashes
	// to, and in a room this server now knows about. hs1 may ask, because the room's own
	// state says a user of hs1 is in it; hs2 may not ask hs1, because as far as hs1 is
	// concerned nobody from hs2 ever joined.
	target := "/_matrix/federation/v1/event/" + messageID
	status, payload := hs1.signedRequest(t, hs2, http.MethodGet, target, nil)
	if status != http.StatusOK {
		t.Fatalf("%s asking %s for the event: %d %s", hs1.name, hs2.name, status, payload)
	}
	var fromReceiver struct {
		Origin string            `json:"origin"`
		PDUs   []json.RawMessage `json:"pdus"`
	}
	if err := json.Unmarshal(payload, &fromReceiver); err != nil {
		t.Fatalf("decoding the event answer: %v", err)
	}
	if fromReceiver.Origin != hs2.name || len(fromReceiver.PDUs) != 1 {
		t.Fatalf("the event answer is the wrong shape: %s", payload)
	}
	if status, payload = hs2.signedRequest(t, hs1, http.MethodGet, target, nil); status != http.StatusForbidden {
		t.Errorf("%s was handed an event from a room it has nobody in: %d %s", hs2.name, status, payload)
	}

	var received map[string]any
	if err := json.Unmarshal(fromReceiver.PDUs[0], &received); err != nil {
		t.Fatalf("decoding the stored event: %v", err)
	}
	var original map[string]any
	if err := json.Unmarshal(eventJSON(t, message), &original); err != nil {
		t.Fatalf("decoding the event that was sent: %v", err)
	}
	for _, field := range []string{"event_id", "room_id", "sender", "type", "content", "signatures", "hashes", "prev_events", "auth_events"} {
		if fmt.Sprint(received[field]) != fmt.Sprint(original[field]) {
			t.Errorf("the two servers disagree about %s of the event: %v against %v",
				field, received[field], original[field])
		}
	}
}

// TestFederationRefusals checks what this server will not accept from another server,
// and that a refusal names the event it is about.
func TestFederationRefusals(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	token := hs1.registerUser(t, "fedalice", "pw-fedalice")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat"}, token)
	roomID, _ := created["room_id"].(string)
	snapshot := hs1.roomSnapshot(t, roomID, token)
	create := snapshot.State[0]

	t.Run("an unauthenticated transaction is refused", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"origin": hs1.name, "origin_server_ts": 1, "pdus": []any{}})
		status, payload := hs1.rawRequest(t, hs2, http.MethodPut, "/_matrix/federation/v1/send/no-credential", "", body)
		if status != http.StatusUnauthorized {
			t.Errorf("status %d, want 401: %s", status, payload)
		}
		if code := errCode(payload); code != "M_UNAUTHORIZED" {
			t.Errorf("the error code is %q, want M_UNAUTHORIZED", code)
		}
	})

	t.Run("a credential naming a key the sender does not publish is refused", func(t *testing.T) {
		header, err := federation.AuthorizationHeader(hs1.name, hs1.app.Keys, hs2.name,
			http.MethodPut, "/_matrix/federation/v1/send/bad-key", []byte(`{}`))
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
		// The same credential, attributed to a key ID hs1 has never published.
		status, payload := hs1.rawRequest(t, hs2, http.MethodPut, "/_matrix/federation/v1/send/bad-key",
			replaceKeyID(t, header, "ed25519:nosuchkey"), []byte(`{}`))
		if status != http.StatusUnauthorized {
			t.Errorf("status %d, want 401: %s", status, payload)
		}
	})

	t.Run("a body changed after signing is refused", func(t *testing.T) {
		signed := []byte(`{"origin":"hs1","origin_server_ts":1,"pdus":[]}`)
		header, err := federation.AuthorizationHeader(hs1.name, hs1.app.Keys, hs2.name,
			http.MethodPut, "/_matrix/federation/v1/send/tampered", signed)
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
		swapped := []byte(`{"origin":"hs1","origin_server_ts":1,"pdus":[{"event_id":"$x"}]}`)
		status, payload := hs1.rawRequest(t, hs2, http.MethodPut, "/_matrix/federation/v1/send/tampered", header, swapped)
		if status != http.StatusUnauthorized {
			t.Errorf("status %d, want 401: %s", status, payload)
		}
	})

	t.Run("a credential addressed to somebody else is refused", func(t *testing.T) {
		// The credential says it is a conversation with hs3. hs2 is the one answering,
		// so the signature — valid in isolation — is not this server's to accept.
		header, err := federation.AuthorizationHeader(hs1.name, hs1.app.Keys, "hs3.example",
			http.MethodPut, "/_matrix/federation/v1/send/wrong-destination", []byte(`{}`))
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
		status, payload := hs1.rawRequest(t, hs2, http.MethodPut, "/_matrix/federation/v1/send/wrong-destination", header, []byte(`{}`))
		if status != http.StatusUnauthorized {
			t.Errorf("status %d, want 401: %s", status, payload)
		}
	})

	t.Run("a transaction naming an origin its credential did not is refused", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"origin":           "hs9.example",
			"origin_server_ts": time.Now().UnixMilli(),
			"pdus":             []any{},
		})
		// The credential is hs1's, the body claims somebody else. A transaction whose
		// signature came from hs1 but whose content speaks as another server is not a
		// mistake worth a gentler answer.

		// somebody else with the same credential.
		header, err := federation.AuthorizationHeader(hs1.name, hs1.app.Keys, hs2.name,
			http.MethodPut, "/_matrix/federation/v1/send/origin-mismatch", body)
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
		status, payload := hs1.rawRequest(t, hs2, http.MethodPut, "/_matrix/federation/v1/send/origin-mismatch", header, body)
		if status != http.StatusForbidden {
			t.Errorf("status %d, want 403: %s", status, payload)
		}
	})

	t.Run("an event signed by nobody is reported event by event", func(t *testing.T) {
		forged := cloneEvent(t, create)
		forged["signatures"] = map[string]any{
			hs1.name: map[string]string{"ed25519:" + hs1.name: "not-a-signature"},
		}
		_, answer := hs1.sendTransaction(t, hs2, "forged-1", []map[string]any{forged})
		result, ok := answer.PDUs[stringAny(create, "event_id")]
		if !ok {
			t.Fatalf("the answer does not mention the event: %v", answer.PDUs)
		}
		if result.Error == "" {
			t.Errorf("%s accepted an event whose signature does not check", hs2.name)
		} else {
			t.Logf("refused, as expected: %s", result.Error)
		}
	})

	t.Run("an event in a room this server does not know is refused", func(t *testing.T) {
		orphan := cloneEvent(t, snapshot.Messages[0])
		_, answer := hs1.sendTransaction(t, hs2, "orphan-1", []map[string]any{orphan})
		result, ok := answer.PDUs[stringAny(snapshot.Messages[0], "event_id")]
		if !ok {
			t.Fatalf("the answer does not mention the message: %v", answer.PDUs)
		}
		if result.Error == "" {
			t.Errorf("%s accepted a message in a room it was never told about", hs2.name)
		}
	})

	t.Run("a transaction carrying too many events is refused as a whole", func(t *testing.T) {
		bulk := make([]map[string]any, 0, federation.MaxPDUs+1)
		for range federation.MaxPDUs + 1 {
			bulk = append(bulk, cloneEvent(t, create))
		}
		status, answer := hs1.sendTransaction(t, hs2, "too-many", bulk)
		if status != http.StatusBadRequest {
			t.Errorf("status %d, want 400: %v", status, answer)
		}
	})

	t.Run("a replayed transaction is answered as it was the first time", func(t *testing.T) {
		forged := cloneEvent(t, create)
		forged["signatures"] = map[string]any{
			hs1.name: map[string]string{"ed25519:" + hs1.name: "still-not-a-signature"},
		}
		body := transactionBody(t, hs1.name, []map[string]any{forged})
		header, err := federation.AuthorizationHeader(hs1.name, hs1.app.Keys, hs2.name,
			http.MethodPut, "/_matrix/federation/v1/send/replay-1", body)
		if err != nil {
			t.Fatalf("signing: %v", err)
		}
		firstStatus, firstBody := hs1.rawRequest(t, hs2, http.MethodPut, "/_matrix/federation/v1/send/replay-1", header, body)
		secondStatus, secondBody := hs1.rawRequest(t, hs2, http.MethodPut, "/_matrix/federation/v1/send/replay-1", header, body)
		if firstStatus != secondStatus || !bytes.Equal(firstBody, secondBody) {
			t.Errorf("the same transaction was answered differently:\n  first:  %d %s\n  second: %d %s",
				firstStatus, firstBody, secondStatus, secondBody)
		}
	})

}

// TestFederationEventAccess checks who is told an event. A server that has nobody in a
// room may not read its history; a server the room's own state says is present may.
func TestFederationEventAccess(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	token := hs1.registerUser(t, "fedcarol", "pw-fedcarol")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat"}, token)
	roomID, _ := created["room_id"].(string)
	_, sent := hs1.clientDo(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/txn1",
		map[string]any{"msgtype": "m.text", "body": "yours to read"}, token)
	messageID, _ := sent["event_id"].(string)

	snapshot := hs1.roomSnapshot(t, roomID, token)
	_, answer := hs1.sendTransaction(t, hs2, "access-handover", byDepth(dedupe(snapshot.State, snapshot.Messages)))
	for eventID, result := range answer.PDUs {
		if result.Error != "" {
			t.Fatalf("%s refused %s: %s", hs2.name, eventID, result.Error)
		}
	}

	target := "/_matrix/federation/v1/event/" + messageID
	status, payload := hs1.signedRequest(t, hs2, http.MethodGet, target, nil)
	if status != http.StatusOK {
		t.Errorf("%s asking for an event in a room the state says it is in: %d %s", hs1.name, status, payload)
	}
	status, payload = hs2.signedRequest(t, hs1, http.MethodGet, target, nil)
	if status != http.StatusForbidden {
		t.Errorf("%s was handed an event from a room it has nobody in: %d %s", hs2.name, status, payload)
	}
}

// TestFederationSenderQueue checks the outbound half: an event queued for a remote
// server is delivered by the worker, and an unreachable server does not lose it.
func TestFederationSenderQueue(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	token := hs1.registerUser(t, "fedbob", "pw-fedbob")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat"}, token)
	roomID, _ := created["room_id"].(string)
	_, sent := hs1.clientDo(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/txn1",
		map[string]any{"msgtype": "m.text", "body": "queued"}, token)
	messageID, _ := sent["event_id"].(string)
	snapshot := hs1.roomSnapshot(t, roomID, token)

	ctx := context.Background()
	for _, event := range byDepth(dedupe(snapshot.State, snapshot.Messages)) {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encoding the event: %v", err)
		}
		if err := hs1.app.Sender.QueuePDU(ctx, hs2.name, stringAny(event, "event_id"), encoded); err != nil {
			t.Fatalf("queueing %s: %v", stringAny(event, "event_id"), err)
		}
	}
	if _, err := hs1.app.Sender.FlushOnce(ctx); err != nil {
		t.Fatalf("flushing the queue: %v", err)
	}
	// The worker is running too, so arrival is polled rather than assumed: what matters
	// is that the queue is worked, not which pass of it delivered the event.
	var status int
	var payload []byte
	deadline := time.Now().Add(15 * time.Second)
	for {
		status, payload = hs1.signedRequest(t, hs2, http.MethodGet, "/_matrix/federation/v1/event/"+messageID, nil)
		if status == http.StatusOK || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status != http.StatusOK {
		t.Fatalf("the queued events never arrived: %d %s", status, payload)
	}

	// A destination that is not listening must keep the work rather than discard it.
	broken := &testDial{target: map[string]string{}}
	broken.register("nowhere", "https://hs9:1")
	dead := newFederationNode(t, "hs1b", newTestCA(t, t.TempDir()), broken)
	if err := dead.app.Sender.QueuePDU(ctx, "nowhere", "$queued-only", []byte(`{"event_id":"$queued-only"}`)); err != nil {
		t.Fatalf("queueing for an unreachable server: %v", err)
	}
	if _, err := dead.app.Sender.FlushOnce(ctx); err != nil {
		t.Logf("the flush reported the failure: %v", err)
	}
	outstanding, err := dead.app.DB.Federation.QueueCount(ctx, "nowhere", "$queued-only")
	if err != nil {
		t.Fatalf("reading the queue: %v", err)
	}
	if outstanding == 0 {
		t.Errorf("the undeliverable event left the queue; it is now nobody's to retry")
	}
}

// transactionBody encodes a PDU set the way the Server-Server API wants it.
func transactionBody(t *testing.T, origin string, pdus []map[string]any) []byte {
	t.Helper()
	raw := make([]json.RawMessage, 0, len(pdus))
	for _, event := range pdus {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encoding a PDU: %v", err)
		}
		raw = append(raw, encoded)
	}
	body, err := json.Marshal(map[string]any{
		"origin":           origin,
		"origin_server_ts": time.Now().UnixMilli(),
		"pdus":             raw,
	})
	if err != nil {
		t.Fatalf("encoding the transaction: %v", err)
	}
	return body
}

// cloneEvent copies a decoded event so a test can change one field of it.
func cloneEvent(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encoding the event: %v", err)
	}
	var copy map[string]any
	if err := json.Unmarshal(encoded, &copy); err != nil {
		t.Fatalf("decoding the event: %v", err)
	}
	return copy
}

// stringAny reads a string field.
func stringAny(event map[string]any, field string) string {
	value, _ := event[field].(string)
	return value
}

// dedupe joins event lists without repeating an event. The timeline of a young room
// carries its state events as well as its messages, and a server is answered for each
// event it was given, not for each mention.
func dedupe(lists ...[]map[string]any) []map[string]any {
	seen := map[string]bool{}
	var out []map[string]any
	for _, list := range lists {
		for _, event := range list {
			id := stringAny(event, "event_id")
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, event)
		}
	}
	return out
}

// eventWithID finds one event in a list by its ID.
func eventWithID(events []map[string]any, eventID string) map[string]any {
	for _, event := range events {
		if stringAny(event, "event_id") == eventID {
			return event
		}
	}
	return nil
}

// byDepth puts events in the order their room's history caused them, which is the order
// a receiving server can check them in.
func byDepth(events []map[string]any) []map[string]any {
	sorted := append([]map[string]any{}, events...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, _ := sorted[i]["depth"].(float64)
		right, _ := sorted[j]["depth"].(float64)
		if left != right {
			return left < right
		}
		return stringAny(sorted[i], "event_id") < stringAny(sorted[j], "event_id")
	})
	return sorted
}

// eventJSON encodes a decoded event back to bytes.
func eventJSON(t *testing.T, event any) []byte {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encoding the event: %v", err)
	}
	return encoded
}

// errCode reads the error code from an error body.
func errCode(payload []byte) string {
	var problem struct {
		ErrCode string `json:"errcode"`
	}
	_ = json.Unmarshal(payload, &problem)
	return problem.ErrCode
}

// replaceKeyID attributes a valid signature to a key the sender does not publish, which
// is how a test checks that a destination looks the key up instead of trusting whatever
// a credential claims.
func replaceKeyID(t *testing.T, header, keyID string) string {
	t.Helper()
	index := bytes.Index([]byte(header), []byte(`key="`))
	if index < 0 {
		t.Fatalf("the credential has no key parameter: %s", header)
	}
	at := index + len(`key="`)
	end := bytes.Index([]byte(header[at:]), []byte(`"`))
	if end < 0 {
		t.Fatalf("the credential's key parameter is not quoted: %s", header)
	}
	return header[:at] + keyID + header[at+end:]
}

// TestFederationRefusalIsAVerdict checks what a server remembers about an event it
// refused. The event is not stored — storing it would fork this room's history against
// every server that recomputes its hash — but the verdict has to survive it, for three
// reasons the specification cares about: the same event offered again gets the same
// answer, an event citing the refused one is refused *because of it* rather than reported
// as a gap, and a refusal that was only "this server could not judge yet" is not recorded
// at all, because the missing part may still turn up and the same event may then be good.
func TestFederationRefusalIsAVerdict(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	token := hs1.registerUser(t, "feddave", "pw-feddave")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat"}, token)
	roomID, _ := created["room_id"].(string)
	_, sent := hs1.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/txn-verdict",
		map[string]any{"msgtype": "m.text", "body": "the one that stays home"}, token)
	subjectID, _ := sent["event_id"].(string)

	snapshot := hs1.roomSnapshot(t, roomID, token)
	version := "11"
	for _, event := range snapshot.State {
		if stringAny(event, "type") != "m.room.create" {
			continue
		}
		if content, ok := event["content"].(map[string]any); ok {
			if asked, ok := content["room_version"].(string); ok && asked != "" {
				version = asked
			}
		}
	}
	rules := roomversion.MustGet(version)

	// Everything except that one message goes to hs2: it ends up with a real room, and
	// with one message it has never seen, which is the subject this test needs.
	var given []map[string]any
	subject := map[string]any{}
	for _, event := range byDepth(dedupe(snapshot.State, snapshot.Messages)) {
		if stringAny(event, "event_id") == subjectID {
			subject = event
			continue
		}
		given = append(given, event)
	}
	if len(subject) == 0 {
		t.Fatalf("the message meant to be refused is not in %s's history", hs1.name)
	}
	_, answer := hs1.sendTransaction(t, hs2, "verdict-setup", given)
	for eventID, result := range answer.PDUs {
		if result.Error != "" {
			t.Fatalf("%s refused %s while being told about the room: %s", hs2.name, eventID, result.Error)
		}
	}

	// The subject arrives with a signature its origin server never made.
	forged := cloneEvent(t, subject)
	forged["signatures"] = map[string]any{
		hs1.name: map[string]string{"ed25519:nonsense": "not-a-signature"},
	}
	_, answer = hs1.sendTransaction(t, hs2, "verdict-forged", []map[string]any{forged})
	refused, ok := answer.PDUs[subjectID]
	if !ok {
		t.Fatalf("the answer does not mention %s: %v", subjectID, answer.PDUs)
	}
	if refused.Error == "" {
		t.Fatalf("%s accepted an event its origin server did not sign", hs2.name)
	}
	if !strings.Contains(refused.Error, "signature") {
		t.Errorf("refused for the wrong reason: %s", refused.Error)
	}

	// The verdict outlives the event.
	ctx := context.Background()
	reasons, err := hs2.app.DB.Events.Rejections(ctx, []string{subjectID})
	if err != nil {
		t.Fatalf("reading the refusals recorded by %s: %v", hs2.name, err)
	}
	if !strings.Contains(reasons[subjectID], "signature") {
		t.Errorf("%s did not remember why it refused %s: %v", hs2.name, subjectID, reasons)
	}

	// An event that cites the refused one is refused for that reason. This is the case
	// the memory exists for: without it, hs2 would answer "I have never seen that auth
	// event" — a gap someone could still fill — when the truth is that it saw it and said
	// no.
	cited := cloneEvent(t, given[len(given)-1])
	cited["content"] = map[string]any{"msgtype": "m.text", "body": "cites an event that was refused"}
	authEvents, _ := cited["auth_events"].([]any)
	cited["auth_events"] = append(append([]any{}, authEvents...), subjectID)
	delete(cited, "event_id")
	if err := events.Seal(hs1.name, rules, cited, hs1.app.Keys); err != nil {
		t.Fatalf("sealing the citing event: %v", err)
	}
	citingID, err := events.EventID(rules, cited)
	if err != nil {
		t.Fatalf("the citing event's ID: %v", err)
	}
	cited["event_id"] = citingID

	_, answer = hs1.sendTransaction(t, hs2, "verdict-cite", []map[string]any{cited})
	result, ok := answer.PDUs[citingID]
	if !ok {
		t.Fatalf("the answer does not mention %s: %v", citingID, answer.PDUs)
	}
	if result.Error == "" {
		t.Errorf("%s accepted an event whose auth event it had refused", hs2.name)
	} else if !strings.Contains(result.Error, "rejected on receipt") || !strings.Contains(result.Error, subjectID) {
		t.Errorf("the cascade was not named: %s", result.Error)
	}
	if reasons, err := hs2.app.DB.Events.Rejections(ctx, []string{citingID}); err != nil {
		t.Fatalf("reading the recorded refusal: %v", err)
	} else if !strings.Contains(reasons[citingID], "rejected on receipt") {
		t.Errorf("%s did not remember its verdict about the citing event: %v", hs2.name, reasons)
	}

	// A refusal that was only a gap is not a verdict, and is not recorded as one: the
	// missing event may still arrive, and the same event may then be perfectly good.
	gapped := cloneEvent(t, subject)
	gapped["content"] = map[string]any{"msgtype": "m.text", "body": "cites an event nobody mentioned"}
	authEvents, _ = gapped["auth_events"].([]any)
	gapped["auth_events"] = append(append([]any{}, authEvents...), "$never-mentioned:hs1")
	delete(gapped, "event_id")
	if err := events.Seal(hs1.name, rules, gapped, hs1.app.Keys); err != nil {
		t.Fatalf("sealing the gapped event: %v", err)
	}
	gappedID, err := events.EventID(rules, gapped)
	if err != nil {
		t.Fatalf("the gapped event's ID: %v", err)
	}
	gapped["event_id"] = gappedID

	_, answer = hs1.sendTransaction(t, hs2, "verdict-gap", []map[string]any{gapped})
	result, ok = answer.PDUs[gappedID]
	if !ok {
		t.Fatalf("the answer does not mention %s: %v", gappedID, answer.PDUs)
	}
	if result.Error == "" {
		t.Fatalf("%s accepted an event whose auth event it cannot resolve", hs2.name)
	}
	if reasons, err := hs2.app.DB.Events.Rejections(ctx, []string{gappedID}); err != nil {
		t.Fatalf("reading the recorded refusals: %v", err)
	} else if reason, recorded := reasons[gappedID]; recorded {
		t.Errorf("%s recorded a verdict about an event it could not judge: %s", hs2.name, reason)
	}
}

// TestLocallyWrittenEventsNameTheirOriginServer: a PDU says which server made it.
//
// The name is not decoration. It is what another server checks the signature against when the
// sender's own key is not the one in the event's sender field, what a room's exclusion of servers
// is matched against, and what the auth rules of some room versions read. An event that omits it is
// a PDU that cannot be traced to whoever sent it, and a homeserver that produces those is asking
// every peer to guess.
func TestLocallyWrittenEventsNameTheirOriginServer(t *testing.T) {
	t.Parallel()
	hs1, _ := federationPair(t)

	token := hs1.registerUser(t, "originwatcher", "pw-originwatcher")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, token)
	roomID, _ := created["room_id"].(string)
	_, sent := hs1.clientDo(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/origin-check",
		map[string]any{"msgtype": "m.text", "body": "who said this"}, token)
	eventID, _ := sent["event_id"].(string)
	if eventID == "" {
		t.Fatal("the message has no event ID")
	}
	entry, err := hs1.app.DB.State.Get(context.Background(), roomID, "m.room.create", "")
	if err != nil || entry == nil {
		t.Fatalf("%s has no create event for %s: %v", hs1.name, roomID, err)
	}
	// Both a message and a state event: they are written by different paths in the room service,
	// and a PDU missing its origin server is a protocol fault whichever kind it is.
	for _, id := range []string{eventID, entry.EventID} {
		row, err := hs1.app.DB.Events.ByID(context.Background(), id)
		if err != nil || row == nil {
			t.Fatalf("%s does not hold %s: %v", hs1.name, id, err)
		}
		var event map[string]any
		if err := json.Unmarshal(row.JSON, &event); err != nil {
			t.Fatalf("%s's copy of %s is unreadable: %v", hs1.name, id, err)
		}
		if origin := stringAny(event, "origin"); origin != hs1.name {
			t.Errorf("the PDU of %s names %q as its origin server, wanting %s", id, origin, hs1.name)
		}
		if stamp, ok := event["origin_server_ts"].(float64); !ok || stamp <= 0 {
			t.Errorf("the PDU of %s has no origin server timestamp: %v", id, event["origin_server_ts"])
		}
	}
}

// TestEventOfferedWithoutItsLabelIsNamedByItsContent covers what a PDU is called, not what
// it says. In every room version from 3 on, the event format does not list an event_id among
// the required properties: the identifier is the reference hash of the event, which each
// receiver computes for itself. Two failures follow from missing that. A server that demands
// the label refuses events that are perfectly well formed — and a room it is being told about
// stays half-empty forever. A server that believes the label stores an event under a name no
// other server uses, so the room's servers cannot agree about which event is which.
//
// Both are checked here: an event offered with its event_id removed, and an event offered with
// a label its own content does not make. Each has to end up filed under the ID the content
// makes, and be readable by that ID.
func TestEventOfferedWithoutItsLabelIsNamedByItsContent(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	token := hs1.registerUser(t, "labelalice", "pw-labelalice")
	status, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "private_chat", "name": "who names an event"}, token)
	if status != http.StatusOK {
		t.Fatalf("creating the room: %v", created)
	}
	roomID, _ := created["room_id"].(string)
	if roomID == "" {
		t.Fatalf("no room id: %v", created)
	}

	send := func(txn, body string) string {
		t.Helper()
		status, answer := hs1.clientDo(t, http.MethodPut,
			"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/"+txn,
			map[string]any{"msgtype": "m.text", "body": body}, token)
		if status != http.StatusOK {
			t.Fatalf("sending %q: %v", body, answer)
		}
		id, _ := answer["event_id"].(string)
		if id == "" {
			t.Fatalf("no event id for %q: %v", body, answer)
		}
		return id
	}
	keptID := send("txn-kept", "handed over normally")
	unlabelledID := send("txn-unlabelled", "offered with no label at all")
	mislabelledID := send("txn-mislabelled", "offered under somebody else's label")

	snapshot := hs1.roomSnapshot(t, roomID, token)
	all := byDepth(dedupe(snapshot.State, snapshot.Messages))

	// The room up to and including the first message, but not the two events this test is
	// about: otherwise the answer proves nothing about whether they were taken.
	handover := make([]map[string]any, 0, len(all))
	for _, event := range all {
		id := stringAny(event, "event_id")
		if id == unlabelledID || id == mislabelledID {
			continue
		}
		handover = append(handover, event)
	}
	if eventWithID(handover, keptID) == nil {
		t.Fatalf("the snapshot does not hold %s, so this test would prove nothing", keptID)
	}
	status, answer := hs1.sendTransaction(t, hs2, "labelling-setup", handover)
	if status != http.StatusOK {
		t.Fatalf("the setup transaction was refused as a whole: %d %v", status, answer)
	}
	for eventID, result := range answer.PDUs {
		if result.Error != "" {
			t.Fatalf("%s refused a setup event %s: %s", hs2.name, eventID, result.Error)
		}
	}

	// The event the room version does not require a label for.
	unlabelled := copyEvent(eventWithID(all, unlabelledID))
	delete(unlabelled, "event_id")
	if status, answer := hs1.sendTransaction(t, hs2, "labelling-unlabelled",
		[]map[string]any{unlabelled}); status != http.StatusOK {
		t.Fatalf("the unlabelled event: %d %v", status, answer)
	} else if problem, ok := answer.PDUs[unlabelledID]; !ok {
		t.Errorf("%s did not answer for the unlabelled event under the ID its content makes: %v",
			hs2.name, answer.PDUs)
	} else if problem.Error != "" {
		t.Errorf("%s refused the unlabelled event: %s", hs2.name, problem.Error)
	}

	// The event carrying a label that its content contradicts.
	mislabelled := copyEvent(eventWithID(all, mislabelledID))
	mislabelled["event_id"] = "$not-the-event-this-content-makes:hs1"
	if status, answer := hs1.sendTransaction(t, hs2, "labelling-mislabelled",
		[]map[string]any{mislabelled}); status != http.StatusOK {
		t.Fatalf("the mislabelled event: %d %v", status, answer)
	} else if problem, ok := answer.PDUs[mislabelledID]; !ok {
		t.Errorf("%s did not answer for the mislabelled event under the ID its content makes: %v",
			hs2.name, answer.PDUs)
	} else if problem.Error != "" {
		t.Errorf("%s refused the mislabelled event: %s", hs2.name, problem.Error)
	}

	// An acceptance is only worth what the server can afterwards be asked about. Each event
	// has to be findable by the ID its content makes, and the copy handed back has to carry
	// that ID — a client was never told the label, so a stored event without one is a message
	// nobody can quote, redact, or reply to.
	for _, id := range []string{unlabelledID, mislabelledID} {
		status, payload := hs1.signedRequest(t, hs2, http.MethodGet,
			"/_matrix/federation/v1/event/"+id, nil)
		if status != http.StatusOK {
			t.Errorf("%s does not hold %s, which it accepted: %d %s", hs2.name, id, status, payload)
			continue
		}
		var answer struct {
			PDUs []json.RawMessage `json:"pdus"`
		}
		if err := json.Unmarshal(payload, &answer); err != nil || len(answer.PDUs) != 1 {
			t.Errorf("the answer about %s is the wrong shape: %s", id, payload)
			continue
		}
		var stored map[string]any
		if err := json.Unmarshal(answer.PDUs[0], &stored); err != nil {
			t.Errorf("decoding the stored event %s: %v", id, err)
			continue
		}
		if got := stringAny(stored, "event_id"); got != id {
			t.Errorf("%s stores %s under %s", hs2.name, id, got)
		}
	}
}

// copyEvent makes a shallow copy of a decoded event so a test can change one field without
// editing what the snapshot holds.
func copyEvent(event map[string]any) map[string]any {
	out := make(map[string]any, len(event))
	for key, value := range event {
		out[key] = value
	}
	return out
}
