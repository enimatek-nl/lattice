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
	"sync"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/app"
	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/federation"
	"github.com/enimatek/lattice/internal/logging"
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
