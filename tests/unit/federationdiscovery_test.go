package unit

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/federation"
)

// Discovery is where federation's attack surface begins: a server name leads to a
// document a stranger writes, an SRV record, and a DNS answer, and the result is this
// server opening a connection somewhere. These tests check that the chain is followed
// as the specification describes and that it can be refused.

func TestFederationIPpolicy(t *testing.T) {
	t.Parallel()
	defaults, err := federation.DefaultIPpolicy()
	if err != nil {
		t.Fatalf("building the default policy: %v", err)
	}
	blocked := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.5.4", "192.168.0.7",
		"169.254.169.254", "100.64.0.1", "224.0.0.5", "fd00::1", "0.0.0.0",
	}
	for _, raw := range blocked {
		if err := defaults.Allow(net.ParseIP(raw)); err == nil {
			t.Errorf("the default policy lets this server dial %s", raw)
		}
	}
	allowed := []string{"203.0.113.5", "93.184.216.34", "2001:db8::1"}
	for _, raw := range allowed {
		if err := defaults.Allow(net.ParseIP(raw)); err != nil {
			t.Errorf("the default policy refuses %s: %v", raw, err)
		}
	}

	// An allow list narrows the world; a deny entry still wins inside it.
	narrow, err := federation.NewIPpolicy([]string{"203.0.113.0/24"}, []string{"203.0.113.7"})
	if err != nil {
		t.Fatalf("building the policy: %v", err)
	}
	if err := narrow.Allow(net.ParseIP("203.0.113.9")); err != nil {
		t.Errorf("an address inside the allow list was refused: %v", err)
	}
	if err := narrow.Allow(net.ParseIP("198.51.100.2")); err == nil {
		t.Errorf("an address outside the allow list was accepted")
	}
	if err := narrow.Allow(net.ParseIP("203.0.113.7")); err == nil {
		t.Errorf("a denied address inside the allow list was accepted")
	}

	// A rule that cannot be parsed is a startup failure, not a missing rule.
	if _, err := federation.NewIPpolicy([]string{"localhost"}, nil); err == nil {
		t.Errorf("a hostname was accepted where an address was required")
	}
	if _, err := federation.NewIPpolicy(nil, []string{"203.0.113.5"}); err != nil {
		t.Errorf("a bare address should be a usable rule: %v", err)
	}
	single, _ := federation.NewIPpolicy(nil, []string{"203.0.113.5"})
	if err := single.Allow(net.ParseIP("203.0.113.5")); err == nil {
		t.Errorf("a bare address in the deny list does not refuse that address")
	}
	if err := single.Allow(net.ParseIP("203.0.113.6")); err != nil {
		t.Errorf("the rule covers more than the address it names: %v", err)
	}
}

// discoveryTargets answers the DNS half of discovery without a DNS server.
type dnsAnswers struct {
	host func(name string) ([]string, error)
	srv  func(name string) ([]*net.SRV, error)
}

func (a dnsAnswers) LookupHost(_ context.Context, name string) ([]string, error) {
	return a.host(name)
}

func (a dnsAnswers) LookupSRV(_ context.Context, name string) ([]*net.SRV, error) {
	return a.srv(name)
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func newDiscovery(policy federation.IPpolicy, wellKnown http.Handler, answers dnsAnswers) (*federation.Discovery, *string) {
	server := httptest.NewTLSServer(wellKnown)
	// The well-known request goes to https://<name>:8448, which does not exist in a
	// test; the dialer sends it to the test listener whatever the name. What is under
	// test is what discovery does with the answer, not TLS.
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("tcp", strings.TrimPrefix(server.URL, "https://"))
		},
	}
	d := federation.NewDiscovery(&http.Client{Transport: transport, Timeout: 5 * time.Second}, policy)
	d.LookupHost = answers.LookupHost
	d.LookupSRV = answers.LookupSRV
	d.Now = time.Now
	return d, &server.URL
}

func TestFederationDiscoveryExplicitPort(t *testing.T) {
	t.Parallel()
	policy, _ := federation.NewIPpolicy(nil, nil)
	d, _ := newDiscovery(policy, http.NotFoundHandler(), dnsAnswers{
		host: func(name string) ([]string, error) {
			if name == "matrix.example" {
				return []string{"203.0.113.20"}, nil
			}
			return nil, notFound(name)
		},
		srv: func(name string) ([]*net.SRV, error) { return nil, notFound(name) },
	})
	target, err := d.Resolve(context.Background(), "matrix.example:9443")
	if err != nil {
		t.Fatalf("resolving a name with an explicit port: %v", err)
	}
	if target.DialURL != "https://203.0.113.20:9443" {
		t.Errorf("dial target %q, want the resolved address on the stated port", target.DialURL)
	}
	// A name with a port does not delegate, but the certificate is still the name's.
	if target.HostHeader != "matrix.example" {
		t.Errorf("the certificate name is %q, want the name that was asked for", target.HostHeader)
	}
}

func TestFederationDiscoveryWellKnown(t *testing.T) {
	t.Parallel()
	var hits int32
	policy, _ := federation.DefaultIPpolicy()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/matrix/server" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"m.server":"delegate.example:9443"}`)
	})
	d, _ := newDiscovery(policy, handler, dnsAnswers{
		host: func(name string) ([]string, error) {
			if name == "delegate.example" {
				return []string{"203.0.113.30"}, nil
			}
			return nil, notFound(name)
		},
		srv: func(name string) ([]*net.SRV, error) { return nil, notFound(name) },
	})
	target, err := d.Resolve(context.Background(), "origin.example")
	if err != nil {
		t.Fatalf("following a delegation: %v", err)
	}
	if target.DialURL != "https://203.0.113.30:9443" {
		t.Errorf("dial target %q, want the delegate's address on the delegate's port", target.DialURL)
	}
	if target.HostHeader != "origin.example" {
		t.Errorf("the certificate name is %q, want the name that was asked for, not the delegate's", target.HostHeader)
	}

	// The answer is cached, so a room full of events does not cause a room full of
	// well-known lookups.
	first := atomic.LoadInt32(&hits)
	for range 3 {
		if _, err := d.Resolve(context.Background(), "origin.example"); err != nil {
			t.Fatalf("the second resolution failed: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != first {
		t.Errorf("the delegation was asked for %d times; it is supposed to be cached", got)
	}
}

func TestFederationDiscoveryRefusesPrivateAddresses(t *testing.T) {
	t.Parallel()
	policy, _ := federation.DefaultIPpolicy()
	for _, delegated := range []string{"127.0.0.1:8448", "169.254.169.254:80", "10.0.0.5:8448"} {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"m.server":`+jsonQuoted(delegated)+`}`)
		})
		d, _ := newDiscovery(policy, handler, dnsAnswers{
			host: func(name string) ([]string, error) { return nil, notFound(name) },
			srv:  func(name string) ([]*net.SRV, error) { return nil, notFound(name) },
		})
		if _, err := d.Resolve(context.Background(), "tricky.example"); err == nil {
			t.Errorf("delegating to %s was accepted", delegated)
		} else if !strings.Contains(err.Error(), "denied") {
			t.Errorf("delegating to %s failed with %v, want the denial to be named", delegated, err)
		}
	}
}

func TestFederationDiscoverySRV(t *testing.T) {
	t.Parallel()
	policy, _ := federation.NewIPpolicy(nil, nil)
	d, _ := newDiscovery(policy, http.NotFoundHandler(), dnsAnswers{
		host: func(name string) ([]string, error) { return []string{"203.0.113.40"}, nil },
		srv: func(name string) ([]*net.SRV, error) {
			if name != "_matrix._tcp.matrix.example" {
				return nil, notFound(name)
			}
			return []*net.SRV{
				{Target: "far.example.", Port: 9999, Priority: 20, Weight: 10},
				{Target: "near.example.", Port: 8448, Priority: 10, Weight: 1},
				{Target: "heavy.example.", Port: 8449, Priority: 10, Weight: 90},
			}, nil
		},
	})
	target, err := d.Resolve(context.Background(), "matrix.example")
	if err != nil {
		t.Fatalf("following an SRV record: %v", err)
	}
	// Lowest priority wins; among equals the heaviest weight wins.
	if target.DialURL != "https://203.0.113.40:8449" {
		t.Errorf("dial target %q, want the heaviest record of the lowest priority", target.DialURL)
	}
	// The certificate belongs to the name that was asked for, never to the SRV target.
	if target.HostHeader != "matrix.example" {
		t.Errorf("the certificate name is %q, want the name that was asked for", target.HostHeader)
	}
}

func TestFederationDiscoveryBrokenWellKnownIsNoWellKnown(t *testing.T) {
	t.Parallel()
	policy, _ := federation.NewIPpolicy(nil, nil)
	cases := map[string]string{
		"not JSON at all":        `<html>`,
		"no m.server":            `{"other":"x"}`,
		"an invalid server name": `{"m.server":"not a server name"}`,
		"pointing at itself":     `{"m.server":"origin.example:8448"}`,
	}
	for name, body := range cases {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		})
		d, _ := newDiscovery(policy, handler, dnsAnswers{
			host: func(host string) ([]string, error) { return []string{"203.0.113.50"}, nil },
			srv:  func(record string) ([]*net.SRV, error) { return nil, notFound(record) },
		})
		target, err := d.Resolve(context.Background(), "origin.example")
		if err != nil {
			t.Errorf("%s: resolution failed instead of falling back: %v", name, err)
			continue
		}
		if target.DialURL != "https://203.0.113.50:8448" {
			t.Errorf("%s: dial target %q, want the name itself on the federation port", name, target.DialURL)
		}
	}
}

// jsonQuoted quotes a string for embedding in a JSON document.
func jsonQuoted(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}
