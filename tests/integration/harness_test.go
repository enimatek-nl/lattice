// Package integration exercises the assembled homeserver over real HTTP and TLS
// listeners, the same way a client would reach it.
package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/app"
	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/logging"
)

// harness is a running homeserver with an isolated data directory.
type harness struct {
	t          *testing.T
	app        *app.App
	clientURL  string
	federation string
	client     *http.Client
	fedClient  *http.Client
	// logs captures everything the server logs, so tests can assert that no
	// credential ever reaches the log output.
	logs *safeBuffer
}

// safeBuffer is a mutex-guarded buffer: the server logs from many goroutines.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns everything written so far.
func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newHarness starts a homeserver on ephemeral ports and waits for readiness.
func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, t.TempDir(), nil)
}

// newHarnessWith starts a homeserver with the given data directory, applying an
// optional configuration override before validation.
func newHarnessWith(t *testing.T, dir string, mutate func(*config.Config)) *harness {
	t.Helper()

	cfg := config.Default()
	cfg.ServerName = "hs1"
	cfg.DataDir = dir
	cfg.Store.Path = filepath.Join(dir, "lattice.db")
	cfg.TLS.CertFile = filepath.Join(dir, "tls", "hs1.crt")
	cfg.TLS.KeyFile = filepath.Join(dir, "tls", "hs1.key")
	cfg.TLS.AutoGenerate = true
	cfg.HTTP.ClientAddr = "127.0.0.1:0"
	cfg.HTTP.FederationAddr = "127.0.0.1:0"
	cfg.HTTP.RequestTimeout = config.Duration(10 * time.Second)
	cfg.HTTP.ShutdownTimeout = config.Duration(15 * time.Second)
	cfg.Registration.Open = true
	cfg.Registration.SharedSecret = "complement"
	cfg.Registration.BcryptCost = 4
	cfg.Log = config.LogConfig{Level: "warn", Format: "json"}

	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid test configuration: %v", err)
	}

	buf := &safeBuffer{}
	log := logging.NewFor(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())

	instance, err := app.New(ctx, cfg, log)
	if err != nil {
		cancel()
		t.Fatalf("cannot build app: %v", err)
	}
	if err := instance.Prepare(); err != nil {
		cancel()
		_ = instance.Close()
		t.Fatalf("cannot prepare app: %v", err)
	}

	served := make(chan error, 1)
	go func() { served <- instance.Serve(ctx) }()

	h := &harness{
		t:          t,
		app:        instance,
		clientURL:  "http://" + instance.ClientAddr(),
		federation: "https://" + instance.FederationAddr(),
		logs:       buf,
		client:     &http.Client{Timeout: 10 * time.Second},
		fedClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		},
	}

	h.waitReady(t)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("server stopped with an error: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("server did not stop within 10s")
		}
		_ = instance.Close()
	})
	return h
}

// waitReady polls /_matrix/client/versions until it answers.
func (h *harness) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		res, err := h.client.Get(h.clientURL + "/_matrix/client/versions")
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %d", res.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("homeserver did not become ready: %v", lastErr)
}

// response bundles what a test usually needs from an HTTP call.
type response struct {
	status int
	header http.Header
	body   []byte
}

// decoded returns the body parsed as a JSON object.
func (r response) decoded(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("response body is not a JSON object: %v (%s)", err, r.body)
	}
	return out
}

// errcode returns the Matrix errcode, or the empty string.
func (r response) errcode(t *testing.T) string {
	t.Helper()
	code, _ := r.decoded(t)["errcode"].(string)
	return code
}

// get performs a GET request.
func (h *harness) get(path string, token string) response {
	h.t.Helper()
	return h.do(h.t, http.MethodGet, h.clientURL+path, nil, token, h.client)
}

// post performs a POST request with a JSON body.
func (h *harness) post(t *testing.T, path string, body any, token string) response {
	t.Helper()
	return h.do(t, http.MethodPost, h.clientURL+path, body, token, h.client)
}

// put performs a PUT with a JSON body, which the endpoints that replace a value use.
func (h *harness) put(t *testing.T, path string, body any, token string) response {
	t.Helper()
	return h.do(t, http.MethodPut, h.clientURL+path, body, token, h.client)
}

// federationGet performs a GET against the HTTPS federation listener.
func (h *harness) federationGet(t *testing.T, path string) response {
	t.Helper()
	return h.do(t, http.MethodGet, h.federation+path, nil, "", h.fedClient)
}

// federationPost performs a POST against the HTTPS federation listener.
func (h *harness) federationPost(t *testing.T, path string, body any) response {
	t.Helper()
	return h.do(t, http.MethodPost, h.federation+path, body, "", h.fedClient)
}

// raw performs a request with an explicit method and optional raw body, which the
// error-semantics tests need.
func (h *harness) raw(t *testing.T, method, path string, rawBody []byte, token string) response {
	t.Helper()
	var reader io.Reader
	if rawBody != nil {
		reader = bytes.NewReader(rawBody)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, h.clientURL+path, reader)
	if err != nil {
		t.Fatalf("cannot build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return h.roundTrip(t, req)
}

// do marshals body (when non-nil) to JSON and performs the request.
func (h *harness) do(t *testing.T, method, url string, body any, token string, client *http.Client) response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			reader = bytes.NewReader(v)
		case string:
			reader = strings.NewReader(v)
		default:
			encoded, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("cannot encode request body: %v", err)
			}
			reader = bytes.NewReader(encoded)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	if err != nil {
		t.Fatalf("cannot build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return h.roundTrip(t, req)
}

func (h *harness) roundTrip(t *testing.T, req *http.Request) response {
	t.Helper()
	res, err := h.httpClientFor(req.URL).Do(req)
	if err != nil {
		t.Fatalf("request %s %s failed: %v", req.Method, req.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("cannot read response body: %v", err)
	}
	return response{status: res.StatusCode, header: res.Header, body: body}
}

func (h *harness) httpClientFor(u *url.URL) *http.Client {
	if u.Scheme == "https" {
		return h.fedClient
	}
	return h.client
}

// register performs the two-step registration the specification describes, exactly
// as clients and Complement do: an unauthenticated attempt that yields a UIA
// challenge, then the same request with m.login.dummy.
func (h *harness) register(t *testing.T, username, password, deviceID string) (userID, accessToken, devID string) {
	t.Helper()
	body := map[string]any{"username": username, "password": password}
	if deviceID != "" {
		body["device_id"] = deviceID
	}

	challenge := h.post(t, "/_matrix/client/v3/register", body, "")
	if challenge.status != http.StatusUnauthorized {
		t.Fatalf("register step 1: got %d (%s), want 401", challenge.status, challenge.body)
	}
	challengeBody := challenge.decoded(t)
	session, _ := challengeBody["session"].(string)

	flows, ok := challengeBody["flows"].([]any)
	if !ok || len(flows) == 0 {
		t.Fatalf("register step 1: no flows in challenge: %s", challenge.body)
	}
	for _, flow := range flows {
		mapOfFlow, ok := flow.(map[string]any)
		if !ok {
			t.Fatalf("register step 1: flow is not an object: %v", flow)
		}
		if _, ok := mapOfFlow["stages"].([]any); !ok {
			t.Fatalf("register step 1: flow has no stages: %v", flow)
		}
	}

	auth := map[string]any{"type": "m.login.dummy"}
	if session != "" {
		auth["session"] = session
	}
	body["auth"] = auth
	completed := h.post(t, "/_matrix/client/v3/register", body, "")
	if completed.status != http.StatusOK {
		t.Fatalf("register step 2: got %d (%s), want 200", completed.status, completed.body)
	}
	out := completed.decoded(t)
	userID, _ = out["user_id"].(string)
	accessToken, _ = out["access_token"].(string)
	devID, _ = out["device_id"].(string)
	if userID == "" || accessToken == "" || devID == "" {
		t.Fatalf("register step 2: incomplete response: %s", completed.body)
	}
	return userID, accessToken, devID
}

// mustStatus fails the test unless the status matches.
func (h *harness) mustStatus(t *testing.T, res response, want int) response {
	t.Helper()
	if res.status != want {
		t.Fatalf("status = %d, want %d (body: %s)", res.status, want, res.body)
	}
	return res
}
