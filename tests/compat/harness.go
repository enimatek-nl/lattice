// Package compat holds the compatibility suite: black box tests that talk to a
// running Lattice server over HTTP, exactly like a Matrix client does. Each
// endpoint gains a case here when it is implemented.
package compat

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
	"strings"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

func init() {
	// Server logs are noisy during a test run; set LATTICE_TEST_LOGS=1 to keep
	// them while debugging.
	if os.Getenv("LATTICE_TEST_LOGS") == "" {
		log.SetOutput(io.Discard)
	}
}

// Server is a running Lattice instance backed by an in-memory database.
type Server struct {
	t     *testing.T
	http  *httptest.Server
	db    *persist.Database
	svc   *service.Service
	app   *api.App
	users map[string]*Session
}

// Session is an authenticated client: an access token plus its device.
type Session struct {
	UserID    string
	DeviceID  string
	Token     string
	Refresh   string
	NextBatch string
}

// NewServer starts a homeserver for the duration of a test.
func NewServer(t *testing.T) *Server {
	t.Helper()
	return NewServerWith(t, api.Config{AllowGuests: true})
}

// NewServerWith starts a homeserver with an explicit API configuration and
// extra service options, so a test can try a differently configured server —
// one that only accepts registration tokens, for example.
func NewServerWith(t *testing.T, cfg api.Config, opts ...service.Option) *Server {
	t.Helper()
	// A file database keeps each test isolated; a shared in-memory
	// cache would be visible to the other tests in the same process.
	db, err := persist.NewDatabase(filepath.Join(t.TempDir(), "lattice.db"))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if cfg.ServerName == "" {
		cfg.ServerName = "test.server"
	}
	if !cfg.EnableMedia {
		cfg.EnableMedia = true
	}
	tokens := auth.NewTokenService("test-secret", auth.WithTTL(24*time.Hour))
	svc, err := service.New(db, cfg.ServerName, tokens, opts...)
	if err != nil {
		t.Fatalf("building service: %v", err)
	}
	app := api.New(svc, auth.NewStore(tokens, db), cfg)
	httpServer := httptest.NewServer(app.Handler())
	t.Cleanup(httpServer.Close)
	return &Server{
		t:     t,
		http:  httpServer,
		db:    db,
		svc:   svc,
		app:   app,
		users: map[string]*Session{},
	}
}

// registrationServer starts a server that only accepts the given registration
// tokens.
func registrationServer(t *testing.T, secrets ...string) *Server {
	t.Helper()
	return NewServerWith(t, api.Config{AllowGuests: true}, service.WithRegistrationSecrets(secrets...))
}

// URL returns the base URL of the running server.
func (s *Server) URL() string { return s.http.URL }

// Response pairs a status code with a decoded body.
type Response struct {
	Status int
	Body   map[string]any
	// Array holds the body when it is a JSON array rather than an object. Room
	// state (GET /rooms/{roomID}/state) is the endpoint that does this.
	Array   []any
	Raw     []byte
	Headers http.Header
}

// ErrCode returns the Matrix error code of a response body.
func (r Response) ErrCode() string {
	code, _ := r.Body["errcode"].(string)
	return code
}

// String reads a string field out of a response body.
func (r Response) String(key string) string {
	value, _ := r.Body[key].(string)
	return value
}

// Int reads an integer field out of a response body, or -1 when it is absent.
func (r Response) Int(key string) int64 {
	value, ok := r.Body[key].(float64)
	if !ok {
		return -1
	}
	return int64(value)
}

// Do performs a request. A nil body sends nothing; a non-nil body is encoded
// as JSON. token may be empty.
func (s *Server) Do(method, path, token string, body any) Response {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("encoding request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, s.http.URL+path, reader)
	if err != nil {
		s.t.Fatalf("building request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return s.roundTrip(req)
}

// DoRaw sends a raw body with an explicit content type.
func (s *Server) DoRaw(method, path, token, contentType string, payload []byte) Response {
	s.t.Helper()
	req, err := http.NewRequest(method, s.http.URL+path, bytes.NewReader(payload))
	if err != nil {
		s.t.Fatalf("building request %s %s: %v", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return s.roundTrip(req)
}

// roundTrip executes a request and decodes the response.
func (s *Server) roundTrip(req *http.Request) Response {
	s.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatalf("reading the response of %s %s: %v", req.Method, req.URL.Path, err)
	}
	out := Response{Status: resp.StatusCode, Raw: raw, Headers: resp.Header}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Body); err != nil {
			out.Body = nil
			// A JSON array is a valid Matrix body: GET /rooms/{roomID}/state returns
			// one. Keep it where a test can reach it rather than calling it unparsable.
			if err := json.Unmarshal(raw, &out.Array); err != nil {
				out.Body = map[string]any{}
				// Media downloads are binary on purpose; only complain when a JSON
				// body fails to parse.
				if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
					s.t.Logf("response of %s %s is not JSON: %s", req.Method, req.URL.Path, raw)
				}
			}
		}
	}
	return out
}

// MustDo performs a request and fails the test unless the status matches.
func (s *Server) MustDo(method, path, token string, body any, want int) Response {
	s.t.Helper()
	resp := s.Do(method, path, token, body)
	if resp.Status != want {
		s.t.Fatalf("%s %s: got status %d body %s, want %d", method, path, resp.Status, resp.Raw, want)
	}
	return resp
}

// Register creates an account and remembers its session.
func (s *Server) Register(localpart, password string) *Session {
	s.t.Helper()
	resp := s.MustDo(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{
		"username": localpart,
		"password": password,
		"auth":     map[string]any{"type": "m.login.dummy"},
	}, http.StatusOK)
	session := &Session{
		UserID:    resp.String("user_id"),
		DeviceID:  resp.String("device_id"),
		Token:     resp.String("access_token"),
		Refresh:   resp.String("refresh_token"),
		NextBatch: resp.String("next_batch"),
	}
	if session.UserID != matrix.UserMxid(localpart, "test.server") {
		s.t.Fatalf("register returned user_id %q, want %q", session.UserID, matrix.UserMxid(localpart, "test.server"))
	}
	s.users[localpart] = session
	return session
}

// Guest registers a guest account.
func (s *Server) Guest() *Session {
	s.t.Helper()
	resp := s.MustDo(http.MethodPost, "/_matrix/client/v3/register", "", map[string]any{"kind": "guest"}, http.StatusOK)
	return &Session{
		UserID:   resp.String("user_id"),
		DeviceID: resp.String("device_id"),
		Token:    resp.String("access_token"),
	}
}

// Login authenticates an existing account.
func (s *Server) Login(localpart, password string) *Session {
	s.t.Helper()
	resp := s.MustDo(http.MethodPost, "/_matrix/client/v3/login", "", map[string]any{
		"type":     "m.login.password",
		"user":     localpart,
		"password": password,
	}, http.StatusOK)
	session := &Session{
		UserID:    resp.String("user_id"),
		DeviceID:  resp.String("device_id"),
		Token:     resp.String("access_token"),
		Refresh:   resp.String("refresh_token"),
		NextBatch: resp.String("next_batch"),
	}
	s.users[localpart] = session
	return session
}

// CreateRoom creates a room as the given session.
func (s *Server) CreateRoom(session *Session, body map[string]any) string {
	s.t.Helper()
	resp := s.MustDo(http.MethodPost, "/_matrix/client/v3/createRoom", session.Token, body, http.StatusOK)
	roomID := resp.String("room_id")
	if roomID == "" {
		s.t.Fatalf("createRoom returned no room_id: %s", resp.Raw)
	}
	return roomID
}

// Send posts a message and returns its event ID.
func (s *Server) Send(session *Session, roomID, txnID, text string) string {
	s.t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/%s", roomID, txnID)
	resp := s.MustDo(http.MethodPut, path, session.Token, map[string]any{
		"msgtype": "m.text",
		"body":    text,
	}, http.StatusOK)
	return resp.String("event_id")
}

// Sync performs a sync and returns the decoded response.
func (s *Server) Sync(session *Session, query string) (matrix.SyncResponse, Response) {
	s.t.Helper()
	resp := s.MustDo(http.MethodGet, "/_matrix/client/v3/sync"+query, session.Token, nil, http.StatusOK)
	var parsed matrix.SyncResponse
	if err := json.Unmarshal(resp.Raw, &parsed); err != nil {
		s.t.Fatalf("decoding the sync response: %v (%s)", err, resp.Raw)
	}
	return parsed, resp
}

// SyncSince syncs from the session's stored next batch token.
func (s *Server) SyncSince(session *Session, timeoutMS int) (matrix.SyncResponse, Response) {
	s.t.Helper()
	return s.Sync(session, fmt.Sprintf("?since=%s&timeout=%d", session.NextBatch, timeoutMS))
}

// MustSync runs a sync and returns only the decoded response.
func (s *Server) MustSync(session *Session, query string) matrix.SyncResponse {
	s.t.Helper()
	synced, _ := s.Sync(session, query)
	return synced
}

// MustFields fails unless a response body carries every named key.
func (s *Server) MustFields(resp Response, keys ...string) {
	s.t.Helper()
	for _, key := range keys {
		if _, ok := resp.Body[key]; !ok {
			s.t.Fatalf("response is missing %q: %s", key, resp.Raw)
		}
	}
}

// SessionFor returns the remembered session of a localpart.
func (s *Server) SessionFor(localpart string) *Session {
	s.t.Helper()
	session, ok := s.users[localpart]
	if !ok {
		s.t.Fatalf("no session recorded for %q", localpart)
	}
	return session
}

// As returns the access token of a session.
func (session *Session) As() string { return session.Token }
