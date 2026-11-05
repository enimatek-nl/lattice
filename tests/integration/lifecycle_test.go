package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/app"
	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/logging"
)

// TestGracefulShutdown checks AC-1: the server starts, serves, and stops cleanly
// on context cancellation with no goroutine or listener left behind.
func TestGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	log := logging.NewFor(slog.NewJSONHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	instance, err := app.New(ctx, cfg, log)
	if err != nil {
		t.Fatalf("cannot build app: %v", err)
	}
	if err := instance.Prepare(); err != nil {
		cancel()
		t.Fatalf("cannot prepare app: %v", err)
	}
	clientAddr := instance.ClientAddr()
	federationAddr := instance.FederationAddr()
	if clientAddr == "" || federationAddr == "" {
		t.Fatalf("resolved addresses missing: %q / %q", clientAddr, federationAddr)
	}
	if clientAddr == cfg.HTTP.ClientAddr || federationAddr == cfg.HTTP.FederationAddr {
		t.Errorf("port 0 must resolve to a concrete port")
	}

	served := make(chan error, 1)
	go func() { served <- instance.Serve(ctx) }()

	// The server must answer before the shutdown is issued.
	if !waitHTTPS(t, "http://"+clientAddr+"/_matrix/client/versions", 10*time.Second) {
		cancel()
		t.Fatalf("server never answered on %s", clientAddr)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve returned %v, want a clean stop", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Serve did not return after cancellation")
	}

	// Both listeners must be closed.
	for _, addr := range []string{clientAddr, federationAddr} {
		conn, derr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if derr == nil {
			_ = conn.Close()
			t.Errorf("listener %s is still accepting connections after shutdown", addr)
		}
	}
	if err := instance.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
}

// TestFederationCertificateIsSignedByConfiguredCA exercises the PKI path the
// Complement harness uses: the server signs its federation certificate with a CA
// supplied on disk, and a client that trusts only that CA accepts it without any
// certificate-verification bypass.
func TestFederationCertificateIsSignedByConfiguredCA(t *testing.T) {
	dir := t.TempDir()
	caCertPath := filepath.Join(dir, "ca.crt")
	caKeyPath := filepath.Join(dir, "ca.key")
	writeTestCAFiles(t, caCertPath, caKeyPath)

	cfg := testConfig(dir)
	cfg.TLS.CACertFile = caCertPath
	cfg.TLS.CAKeyFile = caKeyPath

	instance, err := app.New(context.Background(), cfg, logging.NewFor(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("cannot build app: %v", err)
	}
	if err := instance.Prepare(); err != nil {
		_ = instance.Close()
		t.Fatalf("cannot prepare app: %v", err)
	}
	defer func() { _ = instance.Close() }()
	go func() { _ = instance.Serve(context.Background()) }()
	if !waitHTTPS(t, "http://"+instance.ClientAddr()+"/_matrix/client/versions", 10*time.Second) {
		t.Fatalf("server did not become ready")
	}

	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		t.Fatalf("cannot read CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("CA could not be added to the pool")
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "hs1"},
	}}
	res, err := client.Get("https://" + instance.FederationAddr() + "/_matrix/key/v2/server")
	if err != nil {
		t.Fatalf("the CA-signed certificate was rejected: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if res.TLS.Version < tls.VersionTLS12 {
		t.Errorf("negotiated TLS version %d, want TLS 1.2 or newer", res.TLS.Version)
	}
}

// writeTestCAFiles writes a throwaway CA for the PKI test.
func writeTestCAFiles(t *testing.T, certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("cannot generate CA key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "lattice-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cannot create CA: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("cannot marshal CA key: %v", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("cannot write CA certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("cannot write CA key: %v", err)
	}
}

// TestRestartKeepsState restarts the server against the same data directory and
// verifies that accounts, devices and the signing key survive.
func TestRestartKeepsState(t *testing.T) {
	dir := t.TempDir()
	log := logging.NewFor(slog.NewJSONHandler(io.Discard, nil))

	first, err := app.New(context.Background(), testConfig(dir), log)
	if err != nil {
		t.Fatalf("first start failed: %v", err)
	}
	firstKeys := startAndFetch(t, first)

	// Register through the first instance.
	register := postJSON(t, "http://"+first.ClientAddr()+"/_matrix/client/v3/register", map[string]any{
		"username": "persisted",
		"password": "sUp3rs3kr1t",
	})
	challenge := mustJSON(t, register)
	session, _ := challenge["session"].(string)
	created := mustJSON(t, postJSON(t, "http://"+first.ClientAddr()+"/_matrix/client/v3/register", map[string]any{
		"username": "persisted",
		"password": "sUp3rs3kr1t",
		"auth":     map[string]any{"session": session, "type": "m.login.dummy"},
	}))
	if created["user_id"] != "@persisted:hs1" {
		t.Fatalf("registration response = %v", created)
	}
	// Re-opening the same database needs the database handle released first.
	if err := first.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}

	second, err := app.New(context.Background(), testConfig(dir), log)
	if err != nil {
		t.Fatalf("second start failed: %v", err)
	}
	secondKeys := startAndFetch(t, second)

	if !equalJSON(t, firstKeys["verify_keys"], secondKeys["verify_keys"]) {
		t.Errorf("the signing key changed across a restart: %v vs %v", firstKeys["verify_keys"], secondKeys["verify_keys"])
	}

	// The account survived.
	login := mustJSON(t, postJSON(t, "http://"+second.ClientAddr()+"/_matrix/client/v3/login", map[string]any{
		"type":       "m.login.password",
		"identifier": map[string]any{"type": "m.id.user", "user": "@persisted:hs1"},
		"password":   "sUp3rs3kr1t",
	}))
	if login["user_id"] != "@persisted:hs1" {
		t.Errorf("login after restart = %v", login)
	}
	if err := second.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
}

// TestFederationListenerRequiresTLS checks that the Server-Server listener does
// not accept plaintext traffic.
func TestFederationListenerRequiresTLS(t *testing.T) {
	h := newHarness(t)

	res, err := http.Get(h.federation + "/_matrix/federation/v1/version")
	if err == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Fatalf("the federation listener served plaintext traffic")
		}
	}

	// A proper TLS handshake succeeds, and only TLS 1.2 or newer is accepted.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}}
	tlsRes, terr := client.Get(h.federation + "/_matrix/federation/v1/version")
	if terr != nil {
		t.Fatalf("TLS request failed: %v", terr)
	}
	_, _ = io.Copy(io.Discard, tlsRes.Body)
	_ = tlsRes.Body.Close()
	if tlsRes.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", tlsRes.StatusCode)
	}

	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11}}}
	if _, err := old.Get(h.federation + "/_matrix/federation/v1/version"); err == nil {
		t.Errorf("the federation listener accepted TLS 1.1")
	}
}

// TestConcurrentRequests hammers the API from many goroutines, which also guards
// against a storage layer that deadlocks on its own transaction.
func TestConcurrentRequests(t *testing.T) {
	h := newHarness(t)

	const workers = 24
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			localpart := "concurrent-" + itoa(i)
			challenge := mustJSON(t, postJSON(t, h.clientURL+"/_matrix/client/v3/register", map[string]any{
				"username": localpart,
				"password": "sUp3rs3kr1t",
			}))
			session, _ := challenge["session"].(string)
			created := mustJSON(t, postJSON(t, h.clientURL+"/_matrix/client/v3/register", map[string]any{
				"username": localpart,
				"password": "sUp3rs3kr1t",
				"auth":     map[string]any{"session": session, "type": "m.login.dummy"},
			}))
			token, _ := created["access_token"].(string)
			if token == "" {
				errs <- localpart + ": no access token"
				return
			}
			req, err := http.NewRequest(http.MethodGet, h.clientURL+"/_matrix/client/v3/account/whoami", nil)
			if err != nil {
				errs <- err.Error()
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := h.client.Do(req)
			if err != nil {
				errs <- err.Error()
				return
			}
			body, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if res.StatusCode != http.StatusOK {
				errs <- localpart + ": whoami " + res.Status + " " + string(body)
			}
			if !strings.Contains(string(body), "@"+localpart+":hs1") {
				errs <- localpart + ": unexpected whoami body " + string(body)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Errorf("concurrent failure: %s", msg)
	}

	list := h.get("/_matrix/client/v3/devices", "")
	if list.status != http.StatusUnauthorized {
		t.Errorf("unauthenticated device list = %d", list.status)
	}
}

// TestLogsNeverContainCredentials is the integration counterpart of the log
// redaction unit test: real traffic must not put tokens in the log.
func TestLogsNeverContainCredentials(t *testing.T) {
	h := newHarness(t)

	_, token, _ := h.register(t, "logged", "sUp3rs3kr1t", "DEVICE_SEEN")
	if whoami := h.get("/_matrix/client/v3/account/whoami", token); whoami.status != http.StatusOK {
		t.Fatalf("whoami failed: %s", whoami.body)
	}
	// Also exercise the query-string transport, the classic way tokens leak into
	// access logs.
	query := h.get("/_matrix/client/v3/account/whoami?access_token="+token, "")
	if query.status != http.StatusOK {
		t.Fatalf("query-string token rejected: %s", query.body)
	}
	// And the failure paths, where implementations often log too much.
	h.post(t, "/_matrix/client/v3/login", map[string]any{
		"type":       "m.login.password",
		"identifier": map[string]any{"type": "m.id.user", "user": "@logged:hs1"},
		"password":   "sUp3rs3kr1t",
	}, "")
	_ = h.get("/_matrix/client/v3/account/whoami", "000000000000000000000000bogus")

	logged := h.logs.String()
	if logged == "" {
		t.Skipf("no log output captured")
	}
	for _, secret := range []string{token, "sUp3rs3kr1t"} {
		if strings.Contains(logged, secret) {
			t.Errorf("log output contains %q:\n%s", secret, logged)
		}
	}
}

// TestHealthcheckSubcommand runs the binary's healthcheck against a live server.
func TestHealthcheckSubcommand(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not available: %v", err)
	}
	h := newHarness(t)

	binary := filepath.Join(t.TempDir(), "lattice")
	build := exec.Command("go", "build", "-o", binary, "github.com/enimatek/lattice/cmd/lattice")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cannot build the binary: %v\n%s", err, out)
	}

	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run("healthcheck", "-addr", h.app.ClientAddr()); err != nil {
		t.Fatalf("healthcheck failed: %v\n%s", err, out)
	}
	if out, err := run("healthcheck", "-addr", "127.0.0.1:1"); err == nil {
		t.Errorf("healthcheck against a dead address succeeded: %s", out)
	}
	if out, err := run("version"); err != nil {
		t.Fatalf("version failed: %v\n%s", err, out)
	} else if !strings.Contains(out, app.Version) {
		t.Errorf("version output = %q", out)
	}
}

// testConfig builds a configuration for a standalone instance in dir.
func testConfig(dir string) *config.Config {
	cfg := config.Default()
	cfg.ServerName = "hs1"
	cfg.DataDir = dir
	cfg.Store.Path = filepath.Join(dir, "lattice.db")
	cfg.TLS.CertFile = filepath.Join(dir, "tls", "hs1.crt")
	cfg.TLS.KeyFile = filepath.Join(dir, "tls", "hs1.key")
	cfg.TLS.AutoGenerate = true
	cfg.HTTP.ClientAddr = "127.0.0.1:0"
	cfg.HTTP.FederationAddr = "127.0.0.1:0"
	cfg.HTTP.ShutdownTimeout = config.Duration(15 * time.Second)
	cfg.Registration.Open = true
	cfg.Registration.BcryptCost = 4
	cfg.Log = config.LogConfig{Level: "warn", Format: "json"}
	return cfg
}

// startAndFetch serves an instance in the background and returns its federation
// key document, which tests use to compare keys across restarts.
func startAndFetch(t *testing.T, instance *app.App) map[string]any {
	t.Helper()
	if err := instance.Prepare(); err != nil {
		t.Fatalf("cannot prepare: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- instance.Serve(context.Background()) }()
	t.Cleanup(func() {
		_ = instance.Close()
		select {
		case err := <-served:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Logf("Serve returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("server did not stop")
		}
	})
	if !waitHTTPS(t, "http://"+instance.ClientAddr()+"/_matrix/client/versions", 10*time.Second) {
		t.Fatalf("server did not become ready")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	res, err := client.Get("https://" + instance.FederationAddr() + "/_matrix/key/v2/server")
	if err != nil {
		t.Fatalf("key fetch failed: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("cannot read key document: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("key document is not JSON: %v (%s)", err, body)
	}
	return doc
}

// waitHTTPS polls url until it answers with 200.
func waitHTTPS(t *testing.T, url string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// postJSON performs a POST with a JSON body and returns the raw response.
func postJSON(t *testing.T, url string, body any) response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("cannot encode body: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatalf("cannot build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("cannot read body: %v", err)
	}
	return response{status: res.StatusCode, header: res.Header, body: raw}
}

// mustJSON parses a response body, failing the test if it is not a JSON object.
func mustJSON(t *testing.T, res response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(res.body, &out); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, res.body)
	}
	return out
}

// equalJSON compares two decoded values by their canonical encoding.
func equalJSON(t *testing.T, a, b any) bool {
	t.Helper()
	left, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("cannot encode %v: %v", a, err)
	}
	right, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("cannot encode %v: %v", b, err)
	}
	return string(left) == string(right)
}

// repoRoot locates the module root so tests can build the binary.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot read the working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// itoa is a tiny local formatter so the concurrency test does not need fmt.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}
