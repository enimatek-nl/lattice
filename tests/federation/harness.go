// Package federation holds the server-server suite: black box tests that run two
// Lattice homeservers in one process and let them talk to each other over HTTP,
// exactly as two homeservers on the internet do.
//
// The shape of the suite follows from what federation actually is: a claim cannot
// be checked from inside one server. A bug in a join is a disagreement between two
// databases, and a test with one server cannot see it. Every case here therefore
// has two sides, and asserts about both.
package federation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

func init() {
	// Server logs are noisy during a test run; set LATTICE_TEST_LOGS=1 to keep
	// them while debugging. Federation is the suite where they matter most,
	// because a refused request says why.
	if os.Getenv("LATTICE_TEST_LOGS") == "" {
		log.SetOutput(io.Discard)
	}
}

// The two server names of the default cluster. They are ordinary names with a port
// left off, which is the spelling a server name has in an event, in a user ID and
// in a signature.
const (
	serverOne = "one.example"
	serverTwo = "two.example"
)

// cluster is a pair of running homeservers that know how to reach each other.
type cluster struct {
	t       *testing.T
	servers map[string]*server
	order   []string
}

// server is one running homeserver: its app, its service, the URL peers reach it on
// and the URL a test drives its client API on.
type server struct {
	t      *testing.T
	name   string
	app    *api.App
	svc    *service.Service
	http   *httptest.Server
	db     *persist.Database
	users  map[string]*session
	dbPath string
}

// session is an authenticated client of one server: an access token and its device.
type session struct {
	UserID   string
	DeviceID string
	Token    string
}

// lazyHandler lets a test learn a server's URL before the app behind it exists.
// Peer base URLs are part of the API configuration, so the address has to be known
// before either app is built; the handler is installed a moment later.
type lazyHandler struct {
	mu    sync.Mutex
	inner http.Handler
}

func (l *lazyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	inner := l.inner
	l.mu.Unlock()
	if inner == nil {
		http.Error(w, "server not started", http.StatusInternalServerError)
		return
	}
	inner.ServeHTTP(w, r)
}

func (l *lazyHandler) set(inner http.Handler) {
	l.mu.Lock()
	l.inner = inner
	l.mu.Unlock()
}

// newCluster starts the default two-server cluster with federation on. Each server
// is pinned to the other's loopback URL through FederationPeers: they cannot run
// TLS with a certificate the other trusts, and the point of these tests is the
// Matrix protocol, not certificate distribution. The pinning is exactly the
// tunnel case an operator names on the command line, so the code under test is the
// production path rather than a test double.
func newCluster(t *testing.T) *cluster {
	t.Helper()
	return newClusterNamed(t, serverOne, serverTwo)
}

// newClusterNamed starts a cluster of the given server names.
func newClusterNamed(t *testing.T, names ...string) *cluster {
	t.Helper()
	c := &cluster{t: t, servers: map[string]*server{}, order: names}
	holders := map[string]*lazyHandler{}
	listeners := map[string]*httptest.Server{}
	peers := map[string]string{}
	directory := t.TempDir()

	for _, name := range names {
		holder := &lazyHandler{}
		httpServer := httptest.NewServer(holder)
		t.Cleanup(httpServer.Close)
		holders[name] = holder
		listeners[name] = httpServer
		peers[name] = httpServer.URL
	}
	for _, name := range names {
		c.servers[name] = c.start(name, holders[name], listeners[name], peers, filepath.Join(directory, name+".db"))
	}
	return c
}

// disconnect closes the listener of a server, so requests to it are refused at
// once. It is how a test asks what happens when a peer is not there without
// waiting on a DNS or connect timeout.
func (c *cluster) disconnect(name string) {
	c.t.Helper()
	c.byName(name).http.Close()
}

// dbPathOf is the database file a server was started with, for a test that wants to
// open the same file again.
func (c *cluster) dbPathOf(name string) string { return c.servers[name].dbPath }

// start builds one homeserver and installs it behind its listener.
func (c *cluster) start(name string, holder *lazyHandler, httpServer *httptest.Server, peers map[string]string, dbPath string) *server {
	c.t.Helper()
	db, err := persist.NewDatabase(dbPath)
	if err != nil {
		c.t.Fatalf("opening database for %s: %v", name, err)
	}
	c.t.Cleanup(func() { _ = db.Close() })
	tokens := authTokens(name)
	svc, err := service.New(db, name, tokens, service.WithFederation(true))
	if err != nil {
		c.t.Fatalf("building service for %s: %v", name, err)
	}
	app := api.New(svc, auth.NewStore(tokens, db), api.Config{
		ServerName:      name,
		Version:         "0.1.0-test",
		EnableMedia:     true,
		AllowGuests:     true,
		Federation:      true,
		FederationPeers: peers,
	})
	holder.set(app.Handler())
	return &server{
		t:      c.t,
		name:   name,
		app:    app,
		svc:    svc,
		http:   httpServer,
		db:     db,
		users:  map[string]*session{},
		dbPath: dbPath,
	}
}

// serverOne is the server a test acts on behalf of; serverTwo is the peer.
func (c *cluster) one() *server { return c.servers[serverOne] }
func (c *cluster) two() *server { return c.servers[serverTwo] }

// byName returns a server of the cluster by name.
func (c *cluster) byName(name string) *server { return c.servers[name] }

// peer is the signed transport of a server: the client it uses when it talks to
// another homeserver, which signs with that server's own key.
func (s *server) peer() *federation.Transport {
	s.t.Helper()
	transport := s.app.Federation()
	if transport == nil {
		s.t.Fatalf("%s has no federation transport", s.name)
	}
	return transport
}

// url is where a test drives the client API of a server.
func (s *server) url() string { return s.http.URL }

// authTokens builds the token service of a test server. Each server gets its own
// secret so a token minted by one is not accepted by the other, which is the
// situation a real pair of homeservers is in.
func authTokens(name string) *auth.TokenService {
	return auth.NewTokenService("test-secret-"+name, auth.WithTTL(24*time.Hour))
}

// startPlain runs a homeserver with the federation flag off, for the tests that pin
// what the flag must not do.
func startPlain(t *testing.T, name string) *server {
	t.Helper()
	db, err := persist.NewDatabase(filepath.Join(t.TempDir(), name+".db"))
	if err != nil {
		t.Fatalf("opening database for %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tokens := authTokens(name)
	svc, err := service.New(db, name, tokens)
	if err != nil {
		t.Fatalf("building service for %s: %v", name, err)
	}
	app := api.New(svc, auth.NewStore(tokens, db), api.Config{
		ServerName:  name,
		Version:     "0.1.0-test",
		EnableMedia: true,
		AllowGuests: true,
	})
	httpServer := httptest.NewServer(app.Handler())
	t.Cleanup(httpServer.Close)
	return &server{
		t:      t,
		name:   name,
		app:    app,
		svc:    svc,
		http:   httpServer,
		db:     db,
		users:  map[string]*session{},
		dbPath: filepath.Join(t.TempDir(), name+".db"),
	}
}

// response pairs a status code with a decoded body.
type response struct {
	Status  int
	Body    map[string]any
	Raw     []byte
	Headers http.Header
}

// errCode returns the Matrix error code of a response body.
func (r response) errCode() string {
	code, _ := r.Body["errcode"].(string)
	return code
}

// stringField reads a string out of a response body.
func (r response) stringField(key string) string {
	value, _ := r.Body[key].(string)
	return value
}

// do performs a client API request against one server.
func (s *server) do(method, path, token string, body any) response {
	s.t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("encoding a request body: %v", err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, s.url()+path, payload)
	if err != nil {
		s.t.Fatalf("building %s %s: %v", method, path, err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return roundTrip(s.t, request)
}

// doAs performs a client API request against a named server of the cluster.
func (c *cluster) doAs(name, method, path, token string, body any) response {
	c.t.Helper()
	return c.byName(name).do(method, path, token, body)
}

// mustDo performs a request and fails the test unless the status is the wanted one.
func (s *server) mustDo(want int, method, path, token string, body any) response {
	s.t.Helper()
	got := s.do(method, path, token, body)
	if got.Status != want {
		s.t.Fatalf("%s %s: status %d, want %d (body %s)", method, path, got.Status, want, got.Raw)
	}
	return got
}

// roundTrip sends a request and decodes the answer.
func roundTrip(t *testing.T, request *http.Request) response {
	t.Helper()
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", request.Method, request.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body of %s %s: %v", request.Method, request.URL, err)
	}
	out := response{Status: resp.StatusCode, Raw: raw, Headers: resp.Header}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Body); err != nil {
			t.Fatalf("decoding the body of %s %s: %v (body %s)", request.Method, request.URL, err, raw)
		}
	}
	return out
}

// register creates an account on a server through the open registration flow.
func (s *server) register(localpart, password string) *session {
	s.t.Helper()
	if existing := s.users[localpart]; existing != nil {
		return existing
	}
	got := s.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{
		"username":                    localpart,
		"password":                    password,
		"auth":                        map[string]any{"type": "m.login.dummy"},
		"initial_device_display_name": "test device",
	})
	sess := &session{
		UserID:   got.stringField("user_id"),
		DeviceID: got.stringField("device_id"),
		Token:    got.stringField("access_token"),
	}
	if sess.UserID == "" || sess.Token == "" {
		s.t.Fatalf("registration returned no session: %s", got.Raw)
	}
	s.users[localpart] = sess
	return sess
}

// createRoom makes a room and returns its id.
func (s *server) createRoom(sess *session, body map[string]any) string {
	s.t.Helper()
	got := s.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/createRoom", sess.Token, body)
	roomID := got.stringField("room_id")
	if roomID == "" {
		s.t.Fatalf("createRoom returned no room id: %s", got.Raw)
	}
	return roomID
}

// send posts a message and returns the event id.
func (s *server) send(sess *session, roomID, text string) string {
	s.t.Helper()
	got := s.mustDo(http.StatusOK, http.MethodPut,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/%s", roomID, matrix.NewEventID()),
		sess.Token, matrix.Object{"msgtype": "m.text", "body": text})
	eventID := got.stringField("event_id")
	if eventID == "" {
		s.t.Fatalf("send returned no event id: %s", got.Raw)
	}
	return eventID
}

// join joins a room.
func (s *server) join(sess *session, roomID string) {
	s.t.Helper()
	s.mustDo(http.StatusOK, http.MethodPost,
		fmt.Sprintf("/_matrix/client/v3/rooms/%s/join", roomID), sess.Token, map[string]any{})
}

// uploadDeviceKeys publishes a device's identity keys, signed by a key the test
// holds. The object is signed here rather than smuggled in unsigned because the
// whole point of the endpoints that serve it is that a homeserver hands the bytes
// over without touching them.
func (s *server) uploadDeviceKeys(sess *session, keys matrix.Object) matrix.Object {
	s.t.Helper()
	got := s.mustDo(http.StatusOK, http.MethodPost, "/_matrix/client/v3/keys/upload", sess.Token,
		matrix.Object{"device_keys": keys, "one_time_key_counts": matrix.Object{"signed_curve25519": 5}})
	if _, ok := got.Body["one_time_key_counts"]; !ok {
		s.t.Fatalf("keys/upload returned no key counts: %s", got.Raw)
	}
	return keys
}

// objectField reads a nested object out of a response body.
func (r response) objectField(key string) matrix.Object {
	object, _ := r.Body[key].(map[string]any)
	return object
}

// arrayField reads a nested array out of a response body.
func (r response) arrayField(key string) []any {
	array, _ := r.Body[key].([]any)
	return array
}
